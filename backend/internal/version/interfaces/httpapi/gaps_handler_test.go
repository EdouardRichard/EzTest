package versionapi_test

// W1-14A (spec R-EX-5, API-40): GET /api/versions/{id}/gaps exposes the current
// version_fact rows of kind=gap. It reuses the version facts read model
// (include_superseded=false), so gaps are current facts only and a version with
// no gap answers an empty data array instead of 404. Fix round 1: the endpoint
// uses the shared httpx.List envelope (data array plus next_cursor/has_more), so
// it is shaped like the sibling facts route; the 100-row cap surfaces as
// has_more=true.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versionapi "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
)

type gapsEnvelope struct {
	Data       []map[string]any `json:"data"`
	NextCursor string           `json:"next_cursor"`
	HasMore    bool             `json:"has_more"`
}

func getVersionGaps(handler *versionapi.VersionReadHandler, versionID string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/api/versions/"+versionID+"/gaps", nil)
	request.SetPathValue("id", versionID)
	recorder := httptest.NewRecorder()
	handler.Gaps(recorder, request)
	return recorder
}

func decodeGapsEnvelope(t *testing.T, recorder *httptest.ResponseRecorder) gapsEnvelope {
	t.Helper()
	var envelope gapsEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode body %s: %v", recorder.Body.String(), err)
	}
	return envelope
}

func TestGapsReadAPI(t *testing.T) {
	model := &fakeVersionReadModel{
		exists: true,
		facts: []versionapp.FactReadView{
			{
				ID:         102,
				VersionID:  7,
				Kind:       "gap",
				Key:        "gap:1",
				Payload:    map[string]any{"dimension": "权限"},
				Confidence: func() *float64 { value := 0.8; return &value }(),
				CreatedAt:  testTime,
			},
		},
	}
	handler := versionapi.NewVersionReadHandler(model, nil)

	got := getVersionGaps(handler, "7")
	t.Logf("GET gaps -> %d %s", got.Code, got.Body.String())
	if got.Code != http.StatusOK {
		t.Fatalf("gaps = %d body=%s, want 200", got.Code, got.Body.String())
	}
	envelope := decodeGapsEnvelope(t, got)
	if len(envelope.Data) != 1 {
		t.Fatalf("data = %T %v, want one entry", envelope.Data, envelope.Data)
	}
	item := envelope.Data[0]
	if item["fact_id"] != "102" {
		t.Fatalf("fact_id = %T %v, want the decimal string 102", item["fact_id"], item["fact_id"])
	}
	if item["key"] != "gap:1" {
		t.Fatalf("key = %v, want gap:1", item["key"])
	}
	payload, ok := item["payload"].(map[string]any)
	if !ok || payload["dimension"] != "权限" {
		t.Fatalf("payload = %T %v", item["payload"], item["payload"])
	}
	if confidence, ok := item["confidence"].(float64); !ok || confidence != 0.8 {
		t.Fatalf("confidence = %T %v, want 0.8", item["confidence"], item["confidence"])
	}
	if _, ok := item["created_at"].(string); !ok {
		t.Fatalf("created_at = %T %v, want a timestamp string", item["created_at"], item["created_at"])
	}
	// The envelope carries the shared pagination fields even when there is no
	// next page, exactly like the sibling facts route.
	if envelope.NextCursor != "" || envelope.HasMore {
		t.Fatalf("next_cursor/has_more = %q/%v, want empty/false", envelope.NextCursor, envelope.HasMore)
	}

	// The gap list reuses the current-only facts read with kind=gap and the
	// 100-row cap.
	if model.factCalls != 1 {
		t.Fatalf("facts calls = %d, want 1", model.factCalls)
	}
	filter := model.factQuery[0]
	if filter.Kind != "gap" || filter.IncludeSuperseded || filter.Limit != 100 {
		t.Fatalf("facts filter = %+v, want kind=gap, current facts only and limit=100", filter)
	}
}

func TestGapsReadAPIEmptyIsArray(t *testing.T) {
	model := &fakeVersionReadModel{exists: true}
	handler := versionapi.NewVersionReadHandler(model, nil)

	got := getVersionGaps(handler, "7")
	t.Logf("GET gaps empty -> %d %s", got.Code, got.Body.String())
	if got.Code != http.StatusOK {
		t.Fatalf("gaps = %d body=%s, want 200", got.Code, got.Body.String())
	}
	envelope := decodeGapsEnvelope(t, got)
	if envelope.Data == nil || len(envelope.Data) != 0 {
		t.Fatalf("data = %T %v, want an empty array (not 404/null)", envelope.Data, envelope.Data)
	}
	if envelope.NextCursor != "" || envelope.HasMore {
		t.Fatalf("next_cursor/has_more = %q/%v, want empty/false", envelope.NextCursor, envelope.HasMore)
	}
}

// TestGapsReadAPICapReportsHasMore pins the 100-row cap: the truncated page
// carries has_more=true and the last returned id as next_cursor, matching the
// sibling facts envelope.
func TestGapsReadAPICapReportsHasMore(t *testing.T) {
	model := &fakeVersionReadModel{exists: true, factsMore: true}
	for index := 0; index < 100; index++ {
		model.facts = append(model.facts, versionapp.FactReadView{
			ID: int64(index + 1), VersionID: 7, Kind: "gap", Key: "gap", CreatedAt: testTime,
		})
	}
	handler := versionapi.NewVersionReadHandler(model, nil)

	got := getVersionGaps(handler, "7")
	t.Logf("GET gaps capped -> %d", got.Code)
	if got.Code != http.StatusOK {
		t.Fatalf("gaps = %d body=%s, want 200", got.Code, got.Body.String())
	}
	envelope := decodeGapsEnvelope(t, got)
	if len(envelope.Data) != 100 {
		t.Fatalf("data = %d items, want the 100 cap", len(envelope.Data))
	}
	if !envelope.HasMore {
		t.Fatalf("has_more = false, want true at the 100-row cap")
	}
	if envelope.NextCursor != "100" {
		t.Fatalf("next_cursor = %q, want the last id %q", envelope.NextCursor, "100")
	}
}

func TestGapsReadAPINotFound(t *testing.T) {
	model := &fakeVersionReadModel{exists: false}
	handler := versionapi.NewVersionReadHandler(model, nil)

	got := getVersionGaps(handler, "999")
	if got.Code != http.StatusNotFound {
		t.Fatalf("unknown version -> %d body=%s, want 404", got.Code, got.Body.String())
	}
}

func TestGapsRouteRegisteredAndAuthenticated(t *testing.T) {
	model := &fakeVersionReadModel{exists: true}
	handler := versionapi.NewVersionReadHandler(model, nil)
	authCalls := 0
	auth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authCalls++
			next.ServeHTTP(w, r)
		})
	}
	mux := http.NewServeMux()
	versionapi.RegisterVersionReadRoutes(mux, auth, handler)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/versions/7/gaps", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/versions/7/gaps = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	if authCalls != 1 {
		t.Fatalf("authenticated wrapper calls = %d, want 1", authCalls)
	}
}
