package httpapi

// API-19 / R-EX-4: GET /api/versions/{id}/scope-snapshot?wave_id= returns the
// frozen wave scope. wave_id is required (400 when missing or not a decimal
// ID); an unknown wave and a wave of another version are both 404 so the API
// never leaks cross-version existence. An empty stored snapshot is an object.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

const scopeSnapshotFixtureBody = `{"version_id":7,"frozen_at":"2026-11-05T06:00:00Z","version_suts":[{"version_sut_id":5001,"page_ids":[10],"repository_ids":[7,9]}]}`

type scopeSnapshotFixture struct {
	handler *Handler
	waves   *waveHandlerWaves
}

func newScopeSnapshotFixture() *scopeSnapshotFixture {
	waves := newWaveHandlerWaves()
	waves.seed(execution.RunWave{
		ID:            7001,
		VersionID:     7,
		Status:        execution.WaveRunning,
		ScopeSnapshot: scopeSnapshotFixtureBody,
	})
	waves.seed(execution.RunWave{
		ID:        7002,
		VersionID: 8,
		Status:    execution.WaveRunning,
	})
	waves.seed(execution.RunWave{
		ID:        7003,
		VersionID: 7,
		Status:    execution.WavePlanned,
	})
	versions := &waveHandlerVersions{ids: map[int64]bool{7: true, 8: true}}
	return &scopeSnapshotFixture{
		handler: New(Deps{Waves: waves, Versions: versions}),
		waves:   waves,
	}
}

func getScopeSnapshot(handler *Handler, versionID, query string) *httptest.ResponseRecorder {
	target := "/api/versions/" + versionID + "/scope-snapshot"
	if query != "" {
		target += "?" + query
	}
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.SetPathValue("id", versionID)
	recorder := httptest.NewRecorder()
	handler.ScopeSnapshot(recorder, request)
	return recorder
}

func decodeScopeSnapshotBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode body %s: %v", recorder.Body.String(), err)
	}
	return envelope.Data
}

func TestScopeSnapshotReadAPI(t *testing.T) {
	f := newScopeSnapshotFixture()

	got := getScopeSnapshot(f.handler, "7", "wave_id=7001")
	t.Logf("GET scope-snapshot -> %d %s", got.Code, got.Body.String())
	if got.Code != http.StatusOK {
		t.Fatalf("scope-snapshot = %d body=%s, want 200", got.Code, got.Body.String())
	}
	data := decodeScopeSnapshotBody(t, got)
	if data["wave_id"] != "7001" || data["status"] != string(execution.WaveRunning) {
		t.Fatalf("data = %v, want wave 7001 running", data)
	}
	snapshot, ok := data["scope_snapshot"].(map[string]any)
	if !ok {
		t.Fatalf("scope_snapshot = %T %v, want a JSON object", data["scope_snapshot"], data["scope_snapshot"])
	}
	if _, ok := snapshot["version_suts"]; !ok {
		t.Fatalf("scope_snapshot = %v, want the frozen version_suts", snapshot)
	}
	if _, ok := data["counts"].(map[string]any); !ok {
		t.Fatalf("counts = %T %v, want a JSON object", data["counts"], data["counts"])
	}
}

func TestScopeSnapshotReadAPIRequiresWaveID(t *testing.T) {
	f := newScopeSnapshotFixture()
	for _, test := range []struct {
		name  string
		query string
	}{
		{name: "missing", query: ""},
		{name: "empty", query: "wave_id="},
		{name: "non-numeric", query: "wave_id=abc"},
		{name: "zero", query: "wave_id=0"},
		{name: "negative", query: "wave_id=-1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := getScopeSnapshot(f.handler, "7", test.query)
			t.Logf("GET scope-snapshot wave_id=%q -> %d", test.query, recorder.Code)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("wave_id=%q -> %d body=%s, want 400", test.query, recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestScopeSnapshotReadAPINotFound(t *testing.T) {
	f := newScopeSnapshotFixture()

	// Unknown wave.
	unknown := getScopeSnapshot(f.handler, "7", "wave_id=999999")
	t.Logf("GET scope-snapshot unknown wave -> %d", unknown.Code)
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown wave -> %d body=%s, want 404", unknown.Code, unknown.Body.String())
	}

	// A wave that belongs to another version must not leak across the version
	// boundary: it is the same 404 as an unknown wave.
	crossVersion := getScopeSnapshot(f.handler, "7", "wave_id=7002")
	t.Logf("GET scope-snapshot cross-version wave -> %d", crossVersion.Code)
	if crossVersion.Code != http.StatusNotFound {
		t.Fatalf("cross-version wave -> %d body=%s, want 404", crossVersion.Code, crossVersion.Body.String())
	}

	// Unknown version.
	unknownVersion := getScopeSnapshot(f.handler, "999", "wave_id=7001")
	if unknownVersion.Code != http.StatusNotFound {
		t.Fatalf("unknown version -> %d body=%s, want 404", unknownVersion.Code, unknownVersion.Body.String())
	}
}

func TestScopeSnapshotReadAPIEmptySnapshotIsObject(t *testing.T) {
	f := newScopeSnapshotFixture()

	got := getScopeSnapshot(f.handler, "7", "wave_id=7003")
	t.Logf("GET scope-snapshot empty snapshot -> %d body=%s", got.Code, got.Body.String())
	if got.Code != http.StatusOK {
		t.Fatalf("empty snapshot -> %d body=%s, want 200", got.Code, got.Body.String())
	}
	data := decodeScopeSnapshotBody(t, got)
	snapshot, ok := data["scope_snapshot"].(map[string]any)
	if !ok || len(snapshot) != 0 {
		t.Fatalf("scope_snapshot = %T %v, want the empty object {}", data["scope_snapshot"], data["scope_snapshot"])
	}
}

func TestScopeSnapshotRouteRegistered(t *testing.T) {
	f := newScopeSnapshotFixture()
	passThrough := func(next http.Handler) http.Handler { return next }
	mux := http.NewServeMux()
	f.handler.Register(mux, passThrough, passThrough)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/versions/7/scope-snapshot?wave_id=7001", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/versions/7/scope-snapshot = %d body=%s, want 200 (route registered)",
			recorder.Code, recorder.Body.String())
	}
}
