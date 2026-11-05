package httpapi

// API-19 / R-EX-4: GET /api/versions/{id}/scope-snapshot?wave_id= returns the
// frozen wave scope. wave_id is required and must be the canonical decimal form
// (digits only, no sign, no leading zeros; 400 otherwise). An unknown wave and
// a wave of another version are both 404 so the API never leaks cross-version
// existence.
//
// The stored snapshot is emitted verbatim: snowflake ids exceed 2^53, so the
// response must carry the exact number tokens of the stored JSON and never a
// value re-encoded through float64. The DB column is JSONB with
// CHECK (jsonb_typeof(scope_snapshot) = 'object') and DEFAULT '{}', so stored
// values are always objects; an empty/non-object value still degrades to {}.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

const scopeSnapshotFixtureBody = `{"version_id":7,"frozen_at":"2026-11-05T06:00:00Z","version_suts":[{"version_sut_id":5001,"page_ids":[10],"repository_ids":[7,9]}]}`

// scopeSnapshotSnowflakeBody is the Critical regression fixture: every id is a
// snowflake above 2^53 and must survive the read as the exact same token.
const scopeSnapshotSnowflakeBody = `{"version_id":375994775003729921,"version_suts":[{"version_sut_id":375994775003729922,"page_ids":[375994775003729923],"repository_ids":[9007199254740993]}]}`

const (
	snowflakeVersionID    = "375994775003729921"
	snowflakeVersionSUT   = "375994775003729922"
	snowflakePageID       = "375994775003729923"
	snowflakeRepositoryID = "9007199254740993"
)

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
	// 7003 uses the DB default value (JSONB DEFAULT '{}'): an empty snapshot is
	// still a JSON object.
	waves.seed(execution.RunWave{
		ID:            7003,
		VersionID:     7,
		Status:        execution.WavePlanned,
		ScopeSnapshot: `{}`,
	})
	waves.seed(execution.RunWave{
		ID:            7004,
		VersionID:     7,
		Status:        execution.WaveRunning,
		ScopeSnapshot: scopeSnapshotSnowflakeBody,
	})
	// 7005/7006 cannot exist in the DB (the CHECK only allows objects): they
	// exercise the defensive degradation to {} for legacy/hand-built rows.
	waves.seed(execution.RunWave{
		ID:            7005,
		VersionID:     7,
		Status:        execution.WaveRunning,
		ScopeSnapshot: `[1,2]`,
	})
	waves.seed(execution.RunWave{
		ID:            7006,
		VersionID:     7,
		Status:        execution.WaveRunning,
		ScopeSnapshot: `{"broken":`,
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

// decodeScopeSnapshotBodyNumbers decodes the response with UseNumber so the
// assertion compares the exact number tokens, not float64 approximations.
func decodeScopeSnapshotBodyNumbers(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(recorder.Body.Bytes()))
	decoder.UseNumber()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatalf("decode body %s: %v", recorder.Body.String(), err)
	}
	return envelope.Data
}

func jsonNumberString(t *testing.T, value any) string {
	t.Helper()
	number, ok := value.(json.Number)
	if !ok {
		t.Fatalf("value = %T %v, want json.Number (decode with UseNumber)", value, value)
	}
	return number.String()
}

func assertScopeSnapshotNumberArray(t *testing.T, field string, value any, want []string) {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("%s = %T %v, want an array", field, value, value)
	}
	if len(items) != len(want) {
		t.Fatalf("%s = %v, want %d ids", field, items, len(want))
	}
	for index, item := range items {
		if got := jsonNumberString(t, item); got != want[index] {
			t.Fatalf("%s[%d] = %s, want %s (float64 precision loss)", field, index, got, want[index])
		}
	}
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

// TestScopeSnapshotReadAPIPreservesSnowflakeIDs pins the Critical fix: the
// stored document is passed through as json.RawMessage, so every snowflake id
// (> 2^53) keeps its exact decimal token in the HTTP response.
func TestScopeSnapshotReadAPIPreservesSnowflakeIDs(t *testing.T) {
	f := newScopeSnapshotFixture()

	got := getScopeSnapshot(f.handler, "7", "wave_id=7004")
	t.Logf("GET scope-snapshot snowflake -> %d %s", got.Code, got.Body.String())
	if got.Code != http.StatusOK {
		t.Fatalf("scope-snapshot = %d body=%s, want 200", got.Code, got.Body.String())
	}

	data := decodeScopeSnapshotBodyNumbers(t, got)
	snapshot, ok := data["scope_snapshot"].(map[string]any)
	if !ok {
		t.Fatalf("scope_snapshot = %T %v, want a JSON object", data["scope_snapshot"], data["scope_snapshot"])
	}
	if id := jsonNumberString(t, snapshot["version_id"]); id != snowflakeVersionID {
		t.Fatalf("version_id = %s, want %s", id, snowflakeVersionID)
	}
	suts, ok := snapshot["version_suts"].([]any)
	if !ok || len(suts) != 1 {
		t.Fatalf("version_suts = %T %v, want one entry", snapshot["version_suts"], snapshot["version_suts"])
	}
	sut, ok := suts[0].(map[string]any)
	if !ok {
		t.Fatalf("version_suts[0] = %T, want an object", suts[0])
	}
	if id := jsonNumberString(t, sut["version_sut_id"]); id != snowflakeVersionSUT {
		t.Fatalf("version_sut_id = %s, want %s", id, snowflakeVersionSUT)
	}
	assertScopeSnapshotNumberArray(t, "page_ids", sut["page_ids"], []string{snowflakePageID})
	assertScopeSnapshotNumberArray(t, "repository_ids", sut["repository_ids"], []string{snowflakeRepositoryID})

	// The buggy float64 path renders 375994775003729921 as ...900 and
	// 9007199254740993 as ...992; neither truncated token may appear.
	body := got.Body.String()
	for _, truncated := range []string{"375994775003729900", "9007199254740992"} {
		if strings.Contains(body, truncated) {
			t.Fatalf("body contains float64-truncated id %q: %s", truncated, body)
		}
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
		// The OpenAPI query parameter is the canonical decimal form; ParseInt
		// alone would accept a sign and leading zeros.
		{name: "plus sign", query: "wave_id=%2B7"},
		{name: "leading zero", query: "wave_id=007"},
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

	// 7003 is the DB default '{}'. 7005/7006 are non-object/invalid values that
	// the JSONB CHECK makes unreachable; they must still degrade to {}.
	for _, test := range []struct {
		name   string
		waveID string
	}{
		{name: "db default empty object", waveID: "7003"},
		{name: "non-object", waveID: "7005"},
		{name: "invalid json", waveID: "7006"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := getScopeSnapshot(f.handler, "7", "wave_id="+test.waveID)
			t.Logf("GET scope-snapshot wave %s -> %d body=%s", test.waveID, got.Code, got.Body.String())
			if got.Code != http.StatusOK {
				t.Fatalf("wave %s -> %d body=%s, want 200", test.waveID, got.Code, got.Body.String())
			}
			data := decodeScopeSnapshotBody(t, got)
			snapshot, ok := data["scope_snapshot"].(map[string]any)
			if !ok || len(snapshot) != 0 {
				t.Fatalf("scope_snapshot = %T %v, want the empty object {}", data["scope_snapshot"], data["scope_snapshot"])
			}
			if !strings.Contains(got.Body.String(), `"scope_snapshot":{}`) {
				t.Fatalf("body = %s, want the literal scope_snapshot object {}", got.Body.String())
			}
		})
	}
}

func TestScopeSnapshotRouteRegistered(t *testing.T) {
	f := newScopeSnapshotFixture()
	passThrough := func(next http.Handler) http.Handler { return next }
	// Record the auth wrapper invocation: with a pure pass-through middleware the
	// test could not prove the route is mounted behind `authenticated`.
	authCalls := 0
	authenticated := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authCalls++
			next.ServeHTTP(w, r)
		})
	}
	mux := http.NewServeMux()
	f.handler.Register(mux, authenticated, passThrough)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/versions/7/scope-snapshot?wave_id=7001", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/versions/7/scope-snapshot = %d body=%s, want 200 (route registered)",
			recorder.Code, recorder.Body.String())
	}
	if authCalls != 1 {
		t.Fatalf("authenticated wrapper calls = %d, want 1 (route must be mounted behind auth)", authCalls)
	}
}
