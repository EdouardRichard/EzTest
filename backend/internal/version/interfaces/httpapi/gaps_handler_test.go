package versionapi_test

// W1-14A (spec R-EX-5, API-40): GET /api/versions/{id}/gaps exposes the current
// version_fact rows of kind=gap. It reuses the version facts read model
// (include_superseded=false), so gaps are current facts only and a version with
// no gap answers an empty array instead of 404. Every id is a decimal string.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versionapi "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
)

func getVersionGaps(handler *versionapi.VersionReadHandler, versionID string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/api/versions/"+versionID+"/gaps", nil)
	request.SetPathValue("id", versionID)
	recorder := httptest.NewRecorder()
	handler.Gaps(recorder, request)
	return recorder
}

func decodeGapsData(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode body %s: %v", recorder.Body.String(), err)
	}
	return envelope.Data
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
	data := decodeGapsData(t, got)
	raw, ok := data["items"].([]any)
	if !ok || len(raw) != 1 {
		t.Fatalf("items = %T %v, want one entry", data["items"], data["items"])
	}
	item := raw[0].(map[string]any)
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

	// The gap list reuses the current-only facts read with kind=gap.
	if model.factCalls != 1 {
		t.Fatalf("facts calls = %d, want 1", model.factCalls)
	}
	filter := model.factQuery[0]
	if filter.Kind != "gap" || filter.IncludeSuperseded {
		t.Fatalf("facts filter = %+v, want kind=gap and current facts only", filter)
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
	if body := got.Body.String(); !strings.Contains(body, `"items":[]`) {
		t.Fatalf("body = %s, want an empty items array (not 404)", body)
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
