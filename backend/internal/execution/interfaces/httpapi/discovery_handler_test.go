package httpapi

// W1-14A (spec R-EX-5, API-36..40): read-only execution discovery APIs.
//
//	GET /api/versions/{id}/discovery/observations?kind=&decision=&wave_id=&limit=
//	GET /api/versions/{id}/discovery/deltas?wave_id=
//	GET /api/versions/{id}/coverage
//
// The tests pin the frozen query validation (kind/decision enums, canonical
// decimal wave_id, clamped 1..200 limit), the decimal-string ID contract (a
// snowflake above 2^53 must never be re-encoded as a JSON number) and the
// five-way delta counts plus the discovery-side coverage aggregation.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

const discoverySnowflakeObservationID = "375994775003729921"

type discoveryQueryCall struct {
	versionID int64
	waveID    *int64
	kind      string
	decision  string
	limit     int
}

type fakeDiscoveryQuery struct {
	rows  []executionapp.DiscoveryObservationRow
	err   error
	calls []discoveryQueryCall
}

func (f *fakeDiscoveryQuery) ListByVersion(_ context.Context, versionID int64, waveID *int64, kind, decision string, limit int) ([]executionapp.DiscoveryObservationRow, error) {
	f.calls = append(f.calls, discoveryQueryCall{
		versionID: versionID, waveID: waveID, kind: kind, decision: decision, limit: limit,
	})
	return f.rows, f.err
}

var _ executionapp.DiscoveryQueryRepository = (*fakeDiscoveryQuery)(nil)

func discoveryInt64Ptr(value int64) *int64 { return &value }

func discoveryIntPtr(value int) *int { return &value }

func discoveryObservationRows() []executionapp.DiscoveryObservationRow {
	firstSeen := time.Date(2026, 11, 5, 6, 0, 0, 0, time.UTC)
	lastSeen := firstSeen.Add(2 * time.Minute)
	return []executionapp.DiscoveryObservationRow{
		{
			ID:               375994775003729921,
			VersionSUTID:     5001,
			EnvironmentID:    discoveryInt64Ptr(6001),
			WaveID:           discoveryInt64Ptr(7001),
			CaseRunID:        discoveryInt64Ptr(3001),
			Kind:             "page",
			Method:           "",
			Host:             "shop.example.com",
			ResourceTemplate: "/orders/{id}",
			StatusCode:       discoveryIntPtr(200),
			MIME:             "text/html",
			Occurrences:      3,
			Decision:         "proposed",
			ResourceID:       discoveryInt64Ptr(11),
			FactID:           discoveryInt64Ptr(21),
			FirstSeenAt:      firstSeen,
			LastSeenAt:       lastSeen,
		},
		{
			ID:               1002,
			VersionSUTID:     5001,
			EnvironmentID:    discoveryInt64Ptr(6001),
			WaveID:           discoveryInt64Ptr(7001),
			CaseRunID:        discoveryInt64Ptr(3002),
			Kind:             "api",
			Method:           "GET",
			Host:             "shop.example.com",
			ResourceTemplate: "/api/orders/{id}",
			StatusCode:       discoveryIntPtr(200),
			MIME:             "application/json",
			Occurrences:      5,
			Decision:         "new",
			FirstSeenAt:      firstSeen,
			LastSeenAt:       lastSeen,
		},
	}
}

func newDiscoveryHandlerFixture(rows []executionapp.DiscoveryObservationRow) *Handler {
	waves := newWaveHandlerWaves()
	waves.seed(execution.RunWave{ID: 7001, VersionID: 7, Status: execution.WaveRunning})
	waves.seed(execution.RunWave{ID: 7002, VersionID: 8, Status: execution.WaveRunning})
	versions := &waveHandlerVersions{ids: map[int64]bool{7: true, 8: true}}
	return New(Deps{
		Waves:     waves,
		Versions:  versions,
		Discovery: &fakeDiscoveryQuery{rows: rows},
	})
}

func getDiscovery(handler *Handler, versionID, path, query string) *httptest.ResponseRecorder {
	target := "/api/versions/" + versionID + "/" + path
	if query != "" {
		target += "?" + query
	}
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.SetPathValue("id", versionID)
	recorder := httptest.NewRecorder()
	switch path {
	case "discovery/observations":
		handler.DiscoveryObservations(recorder, request)
	case "discovery/deltas":
		handler.DiscoveryDeltas(recorder, request)
	case "coverage":
		handler.DiscoveryCoverage(recorder, request)
	default:
		panic("unknown discovery path " + path)
	}
	return recorder
}

func decodeDiscoveryData(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
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

func discoveryItems(t *testing.T, data map[string]any) []map[string]any {
	t.Helper()
	raw, ok := data["items"].([]any)
	if !ok {
		t.Fatalf("items = %T %v, want an array", data["items"], data["items"])
	}
	items := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		item, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("item = %T %v, want an object", entry, entry)
		}
		items = append(items, item)
	}
	return items
}

func assertDiscoveryString(t *testing.T, item map[string]any, field, want string) {
	t.Helper()
	got, ok := item[field].(string)
	if !ok {
		t.Fatalf("%s = %T %v, want a decimal string", field, item[field], item[field])
	}
	if got != want {
		t.Fatalf("%s = %q, want %q", field, got, want)
	}
}

// TestDiscoveryObservationsReadAPI pins the observation list projection: the
// snowflake observation id and every other id are decimal strings, the nullable
// provenance columns render null, and the query filters are forwarded.
func TestDiscoveryObservationsReadAPI(t *testing.T) {
	handler := newDiscoveryHandlerFixture(discoveryObservationRows())

	got := getDiscovery(handler, "7", "discovery/observations", "kind=page&decision=proposed&wave_id=7001&limit=10")
	t.Logf("GET observations -> %d %s", got.Code, got.Body.String())
	if got.Code != http.StatusOK {
		t.Fatalf("observations = %d body=%s, want 200", got.Code, got.Body.String())
	}
	data := decodeDiscoveryData(t, got)
	if count, ok := data["count"].(json.Number); !ok || count.String() != "2" {
		t.Fatalf("count = %T %v, want 2", data["count"], data["count"])
	}
	items := discoveryItems(t, data)
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	item := items[0]
	assertDiscoveryString(t, item, "observation_id", discoverySnowflakeObservationID)
	assertDiscoveryString(t, item, "version_sut_id", "5001")
	assertDiscoveryString(t, item, "environment_id", "6001")
	assertDiscoveryString(t, item, "wave_id", "7001")
	assertDiscoveryString(t, item, "case_run_id", "3001")
	assertDiscoveryString(t, item, "resource_id", "11")
	assertDiscoveryString(t, item, "fact_id", "21")
	if item["kind"] != "page" || item["host"] != "shop.example.com" ||
		item["resource_template"] != "/orders/{id}" || item["decision"] != "proposed" ||
		item["mime"] != "text/html" {
		t.Fatalf("observation item = %v", item)
	}
	if occurrences, ok := item["occurrences"].(json.Number); !ok || occurrences.String() != "3" {
		t.Fatalf("occurrences = %T %v, want 3", item["occurrences"], item["occurrences"])
	}
	if status, ok := item["status_code"].(json.Number); !ok || status.String() != "200" {
		t.Fatalf("status_code = %T %v, want 200", item["status_code"], item["status_code"])
	}
	if _, ok := item["first_seen_at"].(string); !ok {
		t.Fatalf("first_seen_at = %T %v, want a timestamp string", item["first_seen_at"], item["first_seen_at"])
	}
	if _, ok := item["last_seen_at"].(string); !ok {
		t.Fatalf("last_seen_at = %T %v, want a timestamp string", item["last_seen_at"], item["last_seen_at"])
	}

	// Nullable provenance columns keep their null meaning instead of a bogus id.
	second := items[1]
	if second["resource_id"] != nil || second["fact_id"] != nil {
		t.Fatalf("nullable resource/fact ids = %v/%v, want null", second["resource_id"], second["fact_id"])
	}

	query := handler.deps.Discovery.(*fakeDiscoveryQuery)
	if len(query.calls) != 1 {
		t.Fatalf("repository calls = %d, want 1", len(query.calls))
	}
	call := query.calls[0]
	if call.versionID != 7 || call.kind != "page" || call.decision != "proposed" || call.limit != 10 {
		t.Fatalf("repository call = %+v", call)
	}
	if call.waveID == nil || *call.waveID != 7001 {
		t.Fatalf("wave filter = %v, want 7001", call.waveID)
	}
}

func TestDiscoveryObservationsReadAPIRejectsBadFilters(t *testing.T) {
	handler := newDiscoveryHandlerFixture(discoveryObservationRows())
	for _, test := range []struct {
		name  string
		query string
	}{
		{name: "unknown kind", query: "kind=document"},
		{name: "unknown decision", query: "decision=ignored"},
		{name: "non-numeric wave", query: "wave_id=abc"},
		{name: "plus wave", query: "wave_id=%2B7001"},
		{name: "leading-zero wave", query: "wave_id=07001"},
		{name: "non-numeric limit", query: "limit=abc"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := getDiscovery(handler, "7", "discovery/observations", test.query)
			t.Logf("GET observations %q -> %d", test.query, got.Code)
			if got.Code != http.StatusBadRequest {
				t.Fatalf("%s -> %d body=%s, want 400", test.query, got.Code, got.Body.String())
			}
		})
	}
}

// TestDiscoveryObservationsReadAPIClampsLimit pins the documented clamp:
// out-of-range numeric limits are clamped to 1..200; absent means 50.
func TestDiscoveryObservationsReadAPIClampsLimit(t *testing.T) {
	for _, test := range []struct {
		query string
		want  int
	}{
		{query: "", want: 50},
		{query: "limit=0", want: 1},
		{query: "limit=-4", want: 1},
		{query: "limit=500", want: 200},
	} {
		t.Run(test.query, func(t *testing.T) {
			handler := newDiscoveryHandlerFixture(discoveryObservationRows())
			got := getDiscovery(handler, "7", "discovery/observations", test.query)
			if got.Code != http.StatusOK {
				t.Fatalf("%q -> %d body=%s, want 200", test.query, got.Code, got.Body.String())
			}
			query := handler.deps.Discovery.(*fakeDiscoveryQuery)
			if query.calls[0].limit != test.want {
				t.Fatalf("%q limit = %d, want %d", test.query, query.calls[0].limit, test.want)
			}
		})
	}
}

func TestDiscoveryObservationsReadAPINotFound(t *testing.T) {
	handler := newDiscoveryHandlerFixture(discoveryObservationRows())

	unknownVersion := getDiscovery(handler, "999", "discovery/observations", "")
	if unknownVersion.Code != http.StatusNotFound {
		t.Fatalf("unknown version -> %d body=%s, want 404", unknownVersion.Code, unknownVersion.Body.String())
	}

	// A wave of another version must not be readable through this version path.
	crossVersion := getDiscovery(handler, "7", "discovery/observations", "wave_id=7002")
	if crossVersion.Code != http.StatusNotFound {
		t.Fatalf("cross-version wave -> %d body=%s, want 404", crossVersion.Code, crossVersion.Body.String())
	}
}

// TestDiscoveryDeltasReadAPI pins the five-way counts and the reduced item
// projection of one wave (or the whole version when wave_id is omitted).
func TestDiscoveryDeltasReadAPI(t *testing.T) {
	rows := []executionapp.DiscoveryObservationRow{
		{ID: 1, VersionSUTID: 5001, WaveID: discoveryInt64Ptr(7001), Kind: "page", Host: "h", ResourceTemplate: "/a", Decision: "new"},
		{ID: 2, VersionSUTID: 5001, WaveID: discoveryInt64Ptr(7001), Kind: "page", Host: "h", ResourceTemplate: "/b", Decision: "proposed"},
		{ID: 3, VersionSUTID: 5001, WaveID: discoveryInt64Ptr(7001), Kind: "api", Method: "GET", Host: "h", ResourceTemplate: "/api/a", Decision: "proposed"},
		{ID: 4, VersionSUTID: 5001, WaveID: discoveryInt64Ptr(7001), Kind: "page", Host: "h", ResourceTemplate: "/c", Decision: "approved"},
		{ID: 5, VersionSUTID: 5001, WaveID: discoveryInt64Ptr(7001), Kind: "page", Host: "h", ResourceTemplate: "/d", Decision: "rejected"},
		{ID: 6, VersionSUTID: 5001, WaveID: discoveryInt64Ptr(7001), Kind: "page", Host: "h", ResourceTemplate: "/e", Decision: "out_of_scope"},
	}
	handler := newDiscoveryHandlerFixture(rows)

	got := getDiscovery(handler, "7", "discovery/deltas", "wave_id=7001")
	t.Logf("GET deltas -> %d %s", got.Code, got.Body.String())
	if got.Code != http.StatusOK {
		t.Fatalf("deltas = %d body=%s, want 200", got.Code, got.Body.String())
	}
	data := decodeDiscoveryData(t, got)
	counts, ok := data["counts"].(map[string]any)
	if !ok {
		t.Fatalf("counts = %T %v, want an object", data["counts"], data["counts"])
	}
	for decision, want := range map[string]string{
		"new": "1", "proposed": "2", "approved": "1", "rejected": "1", "out_of_scope": "1",
	} {
		value, ok := counts[decision].(json.Number)
		if !ok || value.String() != want {
			t.Fatalf("counts[%s] = %T %v, want %s", decision, counts[decision], counts[decision], want)
		}
	}
	items := discoveryItems(t, data)
	if len(items) != 6 {
		t.Fatalf("items = %d, want 6", len(items))
	}
	item := items[1]
	assertDiscoveryString(t, item, "observation_id", "2")
	if item["kind"] != "page" || item["decision"] != "proposed" || item["resource_template"] != "/b" {
		t.Fatalf("delta item = %v", item)
	}
	if _, ok := item["status_code"]; ok {
		t.Fatalf("delta item unexpectedly carries status_code: %v", item)
	}
	query := handler.deps.Discovery.(*fakeDiscoveryQuery)
	if len(query.calls) != 1 || query.calls[0].waveID == nil || *query.calls[0].waveID != 7001 {
		t.Fatalf("repository call = %+v", query.calls)
	}
}

func TestDiscoveryDeltasReadAPIRejectsBadWave(t *testing.T) {
	handler := newDiscoveryHandlerFixture(discoveryObservationRows())
	for _, test := range []struct {
		query string
		want  int
	}{
		{query: "wave_id=abc", want: http.StatusBadRequest},
		{query: "wave_id=7002", want: http.StatusNotFound},
	} {
		got := getDiscovery(handler, "7", "discovery/deltas", test.query)
		if got.Code != test.want {
			t.Fatalf("%q -> %d body=%s, want %d", test.query, got.Code, got.Body.String(), test.want)
		}
	}
}

// TestDiscoveryCoverageReadAPI pins the W1 discovery-side coverage: per kind the
// observed row count, the four classified buckets and the sorted, deduplicated,
// capped list of proposed templates that are still uncovered. Case coverage
// (T14) is deliberately W2.
func TestDiscoveryCoverageReadAPI(t *testing.T) {
	rows := []executionapp.DiscoveryObservationRow{
		{ID: 1, VersionSUTID: 5001, Kind: "page", ResourceTemplate: "/orders/{id}", Decision: "proposed"},
		{ID: 2, VersionSUTID: 5001, Kind: "page", ResourceTemplate: "/orders/{id}", Decision: "proposed"},
		{ID: 3, VersionSUTID: 5001, Kind: "page", ResourceTemplate: "/login", Decision: "approved"},
		{ID: 4, VersionSUTID: 5001, Kind: "page", ResourceTemplate: "/cart", Decision: "new"},
		{ID: 5, VersionSUTID: 5001, Kind: "page", ResourceTemplate: "/legacy", Decision: "rejected"},
		{ID: 6, VersionSUTID: 5001, Kind: "page", ResourceTemplate: "/admin", Decision: "out_of_scope"},
		{ID: 7, VersionSUTID: 5001, Kind: "api", Method: "GET", ResourceTemplate: "/api/orders/{id}", Decision: "proposed"},
		{ID: 8, VersionSUTID: 5001, Kind: "api", Method: "POST", ResourceTemplate: "/api/orders", Decision: "new"},
	}
	handler := newDiscoveryHandlerFixture(rows)

	got := getDiscovery(handler, "7", "coverage", "")
	t.Logf("GET coverage -> %d %s", got.Code, got.Body.String())
	if got.Code != http.StatusOK {
		t.Fatalf("coverage = %d body=%s, want 200", got.Code, got.Body.String())
	}
	data := decodeDiscoveryData(t, got)
	pages, ok := data["pages"].(map[string]any)
	if !ok {
		t.Fatalf("pages = %T %v, want an object", data["pages"], data["pages"])
	}
	wantCounts := map[string]string{"observed": "6", "proposed": "2", "approved": "1", "rejected": "1", "out_of_scope": "1"}
	for field, want := range wantCounts {
		value, ok := pages[field].(json.Number)
		if !ok || value.String() != want {
			t.Fatalf("pages[%s] = %T %v, want %s", field, pages[field], pages[field], want)
		}
	}
	uncovered, ok := pages["uncovered"].([]any)
	if !ok || len(uncovered) != 1 || uncovered[0] != "/orders/{id}" {
		t.Fatalf("pages.uncovered = %T %v, want [/orders/{id}]", pages["uncovered"], pages["uncovered"])
	}

	apis, ok := data["apis"].(map[string]any)
	if !ok {
		t.Fatalf("apis = %T %v, want an object", data["apis"], data["apis"])
	}
	if value, ok := apis["observed"].(json.Number); !ok || value.String() != "2" {
		t.Fatalf("apis.observed = %T %v, want 2", apis["observed"], apis["observed"])
	}
	apiUncovered, ok := apis["uncovered"].([]any)
	if !ok || len(apiUncovered) != 1 || apiUncovered[0] != "/api/orders/{id}" {
		t.Fatalf("apis.uncovered = %T %v", apis["uncovered"], apis["uncovered"])
	}

	// The aggregation reads the whole version, not a paginated page.
	query := handler.deps.Discovery.(*fakeDiscoveryQuery)
	if len(query.calls) != 1 || query.calls[0].limit != 0 || query.calls[0].versionID != 7 {
		t.Fatalf("coverage repository call = %+v", query.calls)
	}
}

func TestDiscoveryCoverageUncoveredIsSortedDedupedAndCapped(t *testing.T) {
	rows := make([]executionapp.DiscoveryObservationRow, 0, 205)
	for index := 0; index < 205; index++ {
		rows = append(rows, executionapp.DiscoveryObservationRow{
			ID: int64(index + 1), VersionSUTID: 5001, Kind: "page",
			ResourceTemplate: "/templates/" + strconv.Itoa(index), Decision: "proposed",
		})
	}
	handler := newDiscoveryHandlerFixture(rows)
	got := getDiscovery(handler, "7", "coverage", "")
	if got.Code != http.StatusOK {
		t.Fatalf("coverage = %d body=%s, want 200", got.Code, got.Body.String())
	}
	data := decodeDiscoveryData(t, got)
	pages := data["pages"].(map[string]any)
	uncovered, ok := pages["uncovered"].([]any)
	if !ok || len(uncovered) != 200 {
		t.Fatalf("pages.uncovered = %T len %d, want 200", pages["uncovered"], len(uncovered))
	}
	for index := 1; index < len(uncovered); index++ {
		left, right := uncovered[index-1].(string), uncovered[index].(string)
		if left > right {
			t.Fatalf("uncovered not sorted at %d: %q > %q", index, left, right)
		}
	}
}

func TestDiscoveryRoutesRegisteredAndAuthenticated(t *testing.T) {
	handler := newDiscoveryHandlerFixture(discoveryObservationRows())
	passThrough := func(next http.Handler) http.Handler { return next }
	authCalls := 0
	authenticated := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authCalls++
			next.ServeHTTP(w, r)
		})
	}
	mux := http.NewServeMux()
	handler.Register(mux, authenticated, passThrough)

	for _, path := range []string{
		"/api/versions/7/discovery/observations",
		"/api/versions/7/discovery/deltas",
		"/api/versions/7/coverage",
	} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s = %d body=%s, want 200 (route registered)", path, recorder.Code, recorder.Body.String())
		}
	}
	if authCalls != 3 {
		t.Fatalf("authenticated wrapper calls = %d, want 3", authCalls)
	}
}

func TestDiscoveryObservationsReadAPIReturnsEmptyItems(t *testing.T) {
	handler := newDiscoveryHandlerFixture(nil)
	got := getDiscovery(handler, "7", "discovery/observations", "")
	if got.Code != http.StatusOK {
		t.Fatalf("observations = %d body=%s, want 200", got.Code, got.Body.String())
	}
	data := decodeDiscoveryData(t, got)
	if count, ok := data["count"].(json.Number); !ok || count.String() != "0" {
		t.Fatalf("count = %T %v, want 0", data["count"], data["count"])
	}
	if !strings.Contains(got.Body.String(), `"items":[]`) {
		t.Fatalf("body = %s, want an empty items array", got.Body.String())
	}
}
