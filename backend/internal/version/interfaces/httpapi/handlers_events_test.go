package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// versionEventSnapshotResponse is the flat P3 event snapshot envelope.
type versionEventSnapshotResponse struct {
	Data           []map[string]any `json:"data"`
	SnapshotCursor int64            `json:"snapshot_cursor"`
	HasMore        bool             `json:"has_more"`
}

// decodeEventSnapshot decodes the flat {data,snapshot_cursor,has_more} body.
func decodeEventSnapshot(t *testing.T, recorder *httptest.ResponseRecorder) versionEventSnapshotResponse {
	t.Helper()
	var envelope versionEventSnapshotResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode event snapshot: %v", err)
	}
	if envelope.Data == nil {
		t.Fatal("event snapshot data is null, want an array")
	}
	return envelope
}

// TestEventsSnapshot covers GET /api/versions/{id}/events: the flat P3 envelope,
// the V1 event payload, the version-channel scoping and the empty-channel case.
func TestEventsSnapshot(t *testing.T) {
	t.Run("flat snapshot uses the version channel and V1 payload", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.versions.seed(versionapp.Version{ID: versionDetailID, Name: "Checkout"})
		sessionID := "9007199254740992"
		deps.events.events = []eventing.Event{
			{
				Seq: 11, Channel: "version:100", Type: "clarification.question_updated",
				VersionID: stringPtr("100"), SessionID: &sessionID,
				TS:      time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC),
				Summary: "clarification question updated",
				Detail:  json.RawMessage(`{"questionId":"9007199254740993","status":"open"}`),
				Refs:    eventing.Refs{Requirement: []string{"9007199254740995"}},
			},
			{
				Seq: 12, Channel: "version:100", Type: "clarification.context_updated",
				VersionID: stringPtr("100"),
				TS:        time.Date(2026, 10, 27, 8, 1, 0, 0, time.UTC),
				Summary:   "clarification context updated",
				Detail:    json.RawMessage(`{"versionId":"100","tool":"context.summary_update","actorId":42}`),
			},
		}
		deps.events.cursor = 12
		deps.events.hasMore = true

		recorder := doRequest(handler, http.MethodGet, "/api/versions/100/events?before_seq=9&limit=2",
			testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		snapshot := decodeEventSnapshot(t, recorder)
		if snapshot.SnapshotCursor != 12 || !snapshot.HasMore {
			t.Fatalf("snapshot_cursor/has_more=%d/%v want 12/true",
				snapshot.SnapshotCursor, snapshot.HasMore)
		}
		if deps.events.lastChannel != "version:100" || deps.events.lastBeforeSeq != 9 ||
			deps.events.lastLimit != 2 {
			t.Fatalf("snapshot call channel/before/limit=%q/%d/%d",
				deps.events.lastChannel, deps.events.lastBeforeSeq, deps.events.lastLimit)
		}
		if len(snapshot.Data) != 2 {
			t.Fatalf("data length=%d want 2", len(snapshot.Data))
		}
		first := snapshot.Data[0]
		requireJSONNumber(t, first, "seq", 11)
		requireStringField(t, first, "channel", "version:100")
		requireStringField(t, first, "type", "clarification.question_updated")
		requireJSONString(t, first, "version_id", "100")
		requireJSONString(t, first, "session_id", "9007199254740992")
		requireNullField(t, first, "agent_role")
		if _, ok := first["ts"].(string); !ok || first["ts"] == "" {
			t.Fatalf("ts=%#v want an RFC3339 timestamp", first["ts"])
		}
		requireStringField(t, first, "summary", "clarification question updated")
		detail := requireMapField(t, first, "detail")
		requireJSONString(t, detail, "questionId", "9007199254740993")
		refs := requireMapField(t, first, "refs")
		if requirements := requireArrayField(t, refs, "requirement"); len(requirements) != 1 ||
			requirements[0] != "9007199254740995" {
			t.Fatalf("refs.requirement=%v", requirements)
		}
		for _, key := range []string{"evidence", "case", "code"} {
			if values := requireArrayField(t, refs, key); len(values) != 0 {
				t.Fatalf("refs.%s=%v want []", key, values)
			}
		}
		// clarification.context_updated is passed through with no special casing.
		second := snapshot.Data[1]
		requireStringField(t, second, "type", "clarification.context_updated")
		contextDetail := requireMapField(t, second, "detail")
		requireStringField(t, contextDetail, "tool", "context.summary_update")
		if values := requireArrayField(t, requireMapField(t, second, "refs"), "requirement"); len(values) != 0 {
			t.Fatalf("context refs.requirement=%v want []", values)
		}
	})

	t.Run("a version without events returns an empty snapshot", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.versions.seed(versionapp.Version{ID: versionDetailID, Name: "Checkout"})
		deps.events.err = eventing.ErrChannelNotFound

		recorder := doRequest(handler, http.MethodGet, "/api/versions/100/events", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		body := recorder.Body.String()
		if !strings.Contains(body, `"data":[]`) {
			t.Fatalf("body=%s want a non-null empty data array", body)
		}
		var envelope struct {
			SnapshotCursor int64 `json:"snapshot_cursor"`
			HasMore        bool  `json:"has_more"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode empty snapshot: %v", err)
		}
		if envelope.SnapshotCursor != 0 || envelope.HasMore {
			t.Fatalf("cursor/more=%d/%v want 0/false", envelope.SnapshotCursor, envelope.HasMore)
		}
		if deps.events.lastChannel != "version:100" {
			t.Fatalf("channel=%q want version:100", deps.events.lastChannel)
		}
	})

	t.Run("an unknown version is a 404", func(t *testing.T) {
		handler, _ := newTestServer(t)
		recorder := doRequest(handler, http.MethodGet, "/api/versions/999/events", testUserToken, "")
		requireError(t, recorder, http.StatusNotFound, platform.CodeNotFound, "not_found")
	})

	t.Run("query validation", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.versions.seed(versionapp.Version{ID: versionDetailID, Name: "Checkout"})

		recorder := doRequest(handler, http.MethodGet, "/api/versions/100/events?limit=0", testUserToken, "")
		requireError(t, recorder, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid_limit")

		recorder = doRequest(handler, http.MethodGet, "/api/versions/100/events?before_seq=abc", testUserToken, "")
		requireError(t, recorder, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid_before_seq")

		recorder = doRequest(handler, http.MethodGet, "/api/versions/100/events?before_seq=-1", testUserToken, "")
		requireError(t, recorder, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid_before_seq")
	})
}
