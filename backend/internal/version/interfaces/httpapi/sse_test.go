package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// TestStreamRejectsUnknownVersion proves the version existence gate of the SSE
// wrapper: a missing version is a 404 and never opens a stream.
func TestStreamRejectsUnknownVersion(t *testing.T) {
	handler, _ := newTestServer(t)
	recorder := doRequest(handler, http.MethodGet, "/api/versions/999/stream", testUserToken, "")
	requireError(t, recorder, http.StatusNotFound, platform.CodeNotFound, "not_found")
	if strings.Contains(recorder.Body.String(), "text/event-stream") {
		t.Fatalf("unknown version opened a stream: %s", recorder.Body.String())
	}
}

// TestStreamFixesChannel proves a client-supplied ?channel= is overwritten with
// version:{id}, and that the inherited Last-Event-ID/since replay and 400
// validation still work behind the wrapper.
func TestStreamFixesChannel(t *testing.T) {
	t.Run("client channel is ignored", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.versions.seed(versionapp.Version{ID: versionDetailID, Name: "Checkout"})
		deps.eventReader.latest = 7

		recorder := doRequest(handler, http.MethodGet,
			"/api/versions/100/stream?channel=another-channel", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		if len(deps.eventSubscriber.channels) == 0 {
			t.Fatal("the stream never subscribed: the wrapper did not delegate")
		}
		for _, channel := range deps.eventSubscriber.channels {
			if channel != "version:100" {
				t.Fatalf("subscribed channel=%q want version:100", channel)
			}
		}
		if len(deps.eventReader.channels) == 0 {
			t.Fatal("the stream never read the channel edge")
		}
		for _, channel := range deps.eventReader.channels {
			if channel != "version:100" {
				t.Fatalf("history channel=%q want version:100 (client channel must be ignored)", channel)
			}
		}
	})

	t.Run("Last-Event-ID replay uses the fixed channel", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.versions.seed(versionapp.Version{ID: versionDetailID, Name: "Checkout"})
		sessionID := "6001"
		deps.eventReader.latest = 5
		deps.eventReader.events = []eventing.Event{{
			Seq: 3, Channel: "version:100", Type: "clarification.session_started",
			VersionID: stringPtr("100"), SessionID: &sessionID,
			TS:      time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC),
			Summary: "clarification session started",
			Detail:  json.RawMessage(`{"sessionId":"6001"}`),
			Refs:    eventing.Refs{},
		}}

		request := newAuthenticatedRequest(http.MethodGet, "/api/versions/100/stream?channel=evil", nil)
		request.Header.Set("Last-Event-ID", "2")
		recorder := doRequestWith(handler, request)

		requireStatus(t, recorder, http.StatusOK)
		if got := recorder.Header().Get("Content-Type"); got != "text/event-stream" {
			t.Fatalf("Content-Type=%q want text/event-stream", got)
		}
		if got := recorder.Header().Get("X-Snapshot-Cursor"); got != "5" {
			t.Fatalf("X-Snapshot-Cursor=%q want 5", got)
		}
		body := recorder.Body.String()
		if !strings.Contains(body, "id: 3\n") ||
			!strings.Contains(body, "event: clarification.session_started\n") ||
			!strings.Contains(body, `"channel":"version:100"`) {
			t.Fatalf("stream body=%q want the replayed version event", body)
		}
		if deps.eventReader.lastAfterSeq != 2 {
			t.Fatalf("replay afterSeq=%d want 2 (Last-Event-ID)", deps.eventReader.lastAfterSeq)
		}
		for _, channel := range deps.eventReader.channels {
			if channel != "version:100" {
				t.Fatalf("history channel=%q want version:100", channel)
			}
		}
		for _, channel := range deps.eventSubscriber.channels {
			if channel != "version:100" {
				t.Fatalf("subscribed channel=%q want version:100", channel)
			}
		}
	})

	t.Run("since is inherited as the replay cursor", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.versions.seed(versionapp.Version{ID: versionDetailID, Name: "Checkout"})

		recorder := doRequest(handler, http.MethodGet, "/api/versions/100/stream?since=4", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		if deps.eventReader.lastAfterSeq != 4 {
			t.Fatalf("replay afterSeq=%d want since=4", deps.eventReader.lastAfterSeq)
		}
	})

	t.Run("an invalid since is inherited as a 400", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.versions.seed(versionapp.Version{ID: versionDetailID, Name: "Checkout"})

		recorder := doRequest(handler, http.MethodGet,
			"/api/versions/100/stream?since=not-a-number", testUserToken, "")
		requireError(t, recorder, http.StatusBadRequest, platform.CodeInvalidArgument, "")
	})
}

// TestStreamAuthRequiresToken proves the SSE wrapper sits behind the same login
// middleware as every other P3 route.
func TestStreamAuthRequiresToken(t *testing.T) {
	handler, _ := newTestServer(t)
	recorder := doRequest(handler, http.MethodGet, "/api/versions/100/stream", "", "")
	requireError(t, recorder, http.StatusUnauthorized, platform.CodeUnauthenticated, "")
}
