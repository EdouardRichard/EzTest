package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	chain "github.com/EdouardRichard/EzTest/backend/internal/chain/application"
	chainhttp "github.com/EdouardRichard/EzTest/backend/internal/chain/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// fakeBuilder is the minimal Builder seam of the handler: it records the
// forwarded query and returns a canned snapshot or error.
type fakeBuilder struct {
	snapshot chain.Snapshot
	err      error
	calls    int
	query    chain.Query
}

func (f *fakeBuilder) Build(_ context.Context, q chain.Query) (chain.Snapshot, error) {
	f.calls++
	f.query = q
	return f.snapshot, f.err
}

func newTestServer(t *testing.T, builder *fakeBuilder) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	handlers := chainhttp.NewHandlers(builder)
	if handlers == nil {
		t.Fatal("NewHandlers returned nil for a wired builder")
	}
	handlers.RegisterRoutes(mux)
	return mux
}

func doGet(t *testing.T, handler http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, http.NoBody)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

type errorEnvelope struct {
	Error struct {
		Code      string         `json:"code"`
		Message   string         `json:"message"`
		RequestID string         `json:"request_id"`
		Retryable bool           `json:"retryable"`
		Details   map[string]any `json:"details"`
	} `json:"error"`
}

func decodeError(t *testing.T, recorder *httptest.ResponseRecorder) errorEnvelope {
	t.Helper()
	var envelope errorEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error envelope: %v body=%s", err, recorder.Body)
	}
	return envelope
}

func requireStatus(t *testing.T, recorder *httptest.ResponseRecorder, want int) {
	t.Helper()
	if recorder.Code != want {
		t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body, want)
	}
}

// dataEnvelope is the success shape of the endpoint.
type dataEnvelope struct {
	Data struct {
		VersionID   string           `json:"versionId"`
		Batch       map[string]any   `json:"batch"`
		Nodes       []map[string]any `json:"nodes"`
		Edges       []map[string]any `json:"edges"`
		Counts      map[string]any   `json:"counts"`
		Truncated   map[string]any   `json:"truncated"`
		GeneratedAt string           `json:"generatedAt"`
	} `json:"data"`
}

func decodeData(t *testing.T, recorder *httptest.ResponseRecorder) dataEnvelope {
	t.Helper()
	var envelope dataEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode data envelope: %v body=%s", err, recorder.Body)
	}
	return envelope
}

// sampleSnapshot is a one-requirement snapshot with every response section.
func sampleSnapshot(t *testing.T) chain.Snapshot {
	t.Helper()
	generatedAt := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	priority := "must"
	uncovered := false
	caseCount := 2
	executedCount := 2
	failedCount := 0
	openQuestions := 1
	return chain.Snapshot{
		VersionID: "101",
		Batch: chain.BatchDTO{
			ID: "7001", BatchNo: 2, Kind: "incremental", Status: "done",
		},
		Nodes: []chain.NodeDTO{
			{
				ID: "requirement:1", Kind: chain.NodeKindRequirement,
				Title: "login", Status: "clarified",
				Priority: &priority, Uncovered: &uncovered, CaseCount: &caseCount,
				ExecutedCount: &executedCount, FailedCount: &failedCount,
				OpenQuestionCount: &openQuestions,
			},
		},
		Edges: []chain.EdgeDTO{
			{ID: "requirement_case:1:11", Source: "requirement:1", Target: "case:11",
				Kind: chain.EdgeKindRequirementCase},
		},
		Counts: chain.CountsDTO{
			Requirements: 1, Cases: 2, Results: 2, Issues: 1,
			ByResultStatus:        map[string]int{"passed": 2, "failed": 0},
			ByIssueSeverity:       map[string]int{"major": 1, "blocker": 0},
			UncoveredRequirements: 1, RevalidationCases: 0, SupplementCases: 1,
		},
		Truncated:   chain.TruncatedDTO{Cases: true},
		GeneratedAt: generatedAt,
	}
}

// ---------------------------------------------------------------------------
// Success path
// ---------------------------------------------------------------------------

// TestSuccessEnvelopeAndQueryForwarding proves the handler forwards every
// §5.1 query parameter and renders the {"data": snapshot} envelope with
// camelCase fields and no-store caching.
func TestSuccessEnvelopeAndQueryForwarding(t *testing.T) {
	builder := &fakeBuilder{snapshot: sampleSnapshot(t)}
	server := newTestServer(t, builder)

	recorder := doGet(t, server,
		"/api/versions/101/exploration-chain"+
			"?batchId=all&requirementId=2&resultStatus=failed&abnormalOnly=1"+
			"&issueStatus=open&issueSeverity=major&q=login&includeSupplement=false"+
			"&revalidationOnly=true&limit=500&requirementLimit=20")

	requireStatus(t, recorder, http.StatusOK)
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if builder.calls != 1 {
		t.Fatalf("builder calls = %d, want 1", builder.calls)
	}
	wantQuery := chain.Query{
		VersionID: "101", BatchID: "all", RequirementID: "2",
		ResultStatus: "failed", IssueStatus: "open", IssueSeverity: "major",
		Search: "login", AbnormalOnly: true, IncludeSupplement: false,
		RevalidationOnly: true, Limit: 500, RequirementLimit: 20,
	}
	if builder.query != wantQuery {
		t.Fatalf("query = %+v, want %+v", builder.query, wantQuery)
	}

	body := decodeData(t, recorder)
	if body.Data.VersionID != "101" {
		t.Fatalf("data.versionId = %q", body.Data.VersionID)
	}
	if body.Data.Batch["id"] != "7001" || body.Data.Batch["batchNo"] != float64(2) ||
		body.Data.Batch["kind"] != "incremental" || body.Data.Batch["status"] != "done" {
		t.Fatalf("data.batch = %v", body.Data.Batch)
	}
	if len(body.Data.Nodes) != 1 {
		t.Fatalf("data.nodes = %v", body.Data.Nodes)
	}
	node := body.Data.Nodes[0]
	if node["id"] != "requirement:1" || node["kind"] != "requirement" ||
		node["title"] != "login" || node["status"] != "clarified" {
		t.Fatalf("node = %v", node)
	}
	if node["uncovered"] != false || node["caseCount"] != float64(2) ||
		node["executedCount"] != float64(2) || node["failedCount"] != float64(0) ||
		node["openQuestionCount"] != float64(1) || node["priority"] != "must" {
		t.Fatalf("requirement node fields = %v", node)
	}
	if _, ok := node["attempt"]; ok {
		t.Fatalf("requirement node exposes a result field: %v", node)
	}
	if len(body.Data.Edges) != 1 {
		t.Fatalf("data.edges = %v", body.Data.Edges)
	}
	edge := body.Data.Edges[0]
	if edge["id"] != "requirement_case:1:11" || edge["source"] != "requirement:1" ||
		edge["target"] != "case:11" || edge["kind"] != "requirement_case" {
		t.Fatalf("edge = %v", edge)
	}
	counts := body.Data.Counts
	if counts["requirements"] != float64(1) || counts["cases"] != float64(2) ||
		counts["results"] != float64(2) || counts["issues"] != float64(1) ||
		counts["uncoveredRequirements"] != float64(1) ||
		counts["revalidationCases"] != float64(0) ||
		counts["supplementCases"] != float64(1) {
		t.Fatalf("counts = %v", counts)
	}
	byResultStatus, ok := counts["byResultStatus"].(map[string]any)
	if !ok || byResultStatus["passed"] != float64(2) {
		t.Fatalf("counts.byResultStatus = %v", counts["byResultStatus"])
	}
	byIssueSeverity, ok := counts["byIssueSeverity"].(map[string]any)
	if !ok || byIssueSeverity["major"] != float64(1) {
		t.Fatalf("counts.byIssueSeverity = %v", counts["byIssueSeverity"])
	}
	truncated := body.Data.Truncated
	if truncated["cases"] != true || truncated["requirements"] != false ||
		truncated["results"] != false || truncated["issues"] != false {
		t.Fatalf("truncated = %v", truncated)
	}
	if _, err := time.Parse(time.RFC3339, body.Data.GeneratedAt); err != nil {
		t.Fatalf("generatedAt = %q: %v", body.Data.GeneratedAt, err)
	}
}

// TestDefaults proves the documented defaults: limit 300, requirementLimit 100
// and includeSupplement true.
func TestDefaults(t *testing.T) {
	builder := &fakeBuilder{snapshot: sampleSnapshot(t)}
	server := newTestServer(t, builder)

	requireStatus(t, doGet(t, server, "/api/versions/101/exploration-chain"), http.StatusOK)

	wantQuery := chain.Query{
		VersionID: "101", IncludeSupplement: true, Limit: 300, RequirementLimit: 100,
	}
	if builder.query != wantQuery {
		t.Fatalf("query = %+v, want %+v", builder.query, wantQuery)
	}
}

// TestEmptyArraysAreRendered proves a version without nodes renders empty JSON
// arrays rather than null.
func TestEmptyArraysAreRendered(t *testing.T) {
	builder := &fakeBuilder{snapshot: chain.Snapshot{
		VersionID: "101",
		Nodes:     []chain.NodeDTO{},
		Edges:     []chain.EdgeDTO{},
		Counts: chain.CountsDTO{
			ByResultStatus:  map[string]int{},
			ByIssueSeverity: map[string]int{},
		},
	}}
	server := newTestServer(t, builder)

	recorder := doGet(t, server, "/api/versions/101/exploration-chain")
	requireStatus(t, recorder, http.StatusOK)
	if !strings.Contains(recorder.Body.String(), `"nodes":[]`) {
		t.Fatalf("body does not render an empty nodes array: %s", recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), `"edges":[]`) {
		t.Fatalf("body does not render an empty edges array: %s", recorder.Body)
	}
}

// ---------------------------------------------------------------------------
// 400 invalid argument
// ---------------------------------------------------------------------------

// TestInvalidQueryParameters proves every malformed §5.1 parameter is a 400
// invalid_argument with the stable details, before the service is called.
func TestInvalidQueryParameters(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		field   string
		wantMin any
		wantMax any
	}{
		{name: "limit zero", query: "limit=0", field: "limit", wantMin: float64(50), wantMax: float64(2000)},
		{name: "limit below minimum", query: "limit=49", field: "limit", wantMin: float64(50), wantMax: float64(2000)},
		{name: "limit above maximum", query: "limit=2001", field: "limit", wantMin: float64(50), wantMax: float64(2000)},
		{name: "limit not an integer", query: "limit=abc", field: "limit", wantMin: float64(50), wantMax: float64(2000)},
		{name: "requirement limit zero", query: "requirementLimit=0", field: "requirementLimit", wantMin: float64(1), wantMax: float64(500)},
		{name: "requirement limit above maximum", query: "requirementLimit=501", field: "requirementLimit", wantMin: float64(1), wantMax: float64(500)},
		{name: "requirement limit not an integer", query: "requirementLimit=x", field: "requirementLimit", wantMin: float64(1), wantMax: float64(500)},
		{name: "invalid batch", query: "batchId=abc", field: "batchId"},
		{name: "invalid requirement", query: "requirementId=abc", field: "requirementId"},
		{name: "invalid result status", query: "resultStatus=running", field: "resultStatus"},
		{name: "invalid issue status", query: "issueStatus=closed", field: "issueStatus"},
		{name: "invalid issue severity", query: "issueSeverity=critical", field: "issueSeverity"},
		{name: "invalid abnormal flag", query: "abnormalOnly=maybe", field: "abnormalOnly"},
		{name: "invalid supplement flag", query: "includeSupplement=maybe", field: "includeSupplement"},
		{name: "invalid revalidation flag", query: "revalidationOnly=maybe", field: "revalidationOnly"},
		{name: "search too long", query: "q=" + strings.Repeat("a", 101), field: "q", wantMax: float64(100)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			builder := &fakeBuilder{snapshot: sampleSnapshot(t)}
			server := newTestServer(t, builder)

			recorder := doGet(t, server, "/api/versions/101/exploration-chain?"+test.query)
			requireStatus(t, recorder, http.StatusBadRequest)

			envelope := decodeError(t, recorder)
			if envelope.Error.Code != platform.CodeInvalidArgument {
				t.Fatalf("error code = %q, want invalid_argument", envelope.Error.Code)
			}
			if got, _ := envelope.Error.Details["field"].(string); got != test.field {
				t.Fatalf("details.field = %v, want %q (body=%s)", envelope.Error.Details["field"], test.field, recorder.Body)
			}
			reason, _ := envelope.Error.Details["reason"].(string)
			if reason != chain.ReasonInvalidArgument {
				t.Fatalf("details.reason = %q, want invalid_argument", reason)
			}
			if test.wantMin != nil {
				if got := envelope.Error.Details["min"]; got != test.wantMin {
					t.Fatalf("details.min = %v, want %v", got, test.wantMin)
				}
			}
			if test.wantMax != nil {
				if got := envelope.Error.Details["max"]; got != test.wantMax {
					t.Fatalf("details.max = %v, want %v", got, test.wantMax)
				}
			}
			if builder.calls != 0 {
				t.Fatalf("builder calls = %d, want 0 (invalid query)", builder.calls)
			}
		})
	}
}

// TestMalformedVersionPath proves a non-decimal {versionId} is a 400 before the
// service is called.
func TestMalformedVersionPath(t *testing.T) {
	for _, path := range []string{
		"/api/versions/abc/exploration-chain",
		"/api/versions/0/exploration-chain",
		"/api/versions/-1/exploration-chain",
	} {
		builder := &fakeBuilder{snapshot: sampleSnapshot(t)}
		server := newTestServer(t, builder)
		recorder := doGet(t, server, path)
		requireStatus(t, recorder, http.StatusBadRequest)
		envelope := decodeError(t, recorder)
		if envelope.Error.Code != platform.CodeInvalidArgument {
			t.Fatalf("%s: error code = %q", path, envelope.Error.Code)
		}
		if field, _ := envelope.Error.Details["field"].(string); field != "versionId" {
			t.Fatalf("%s: details.field = %v, want versionId", path, envelope.Error.Details["field"])
		}
		if builder.calls != 0 {
			t.Fatalf("%s: builder was called", path)
		}
	}
}

// TestServiceInvalidArgumentMapping proves an ErrInvalidArgument returned by the
// service is rendered with its field and bounds.
func TestServiceInvalidArgumentMapping(t *testing.T) {
	builder := &fakeBuilder{err: &chain.ErrInvalidArgument{
		Field:   "limit",
		Reason:  chain.ReasonInvalidArgument,
		Min:     50,
		Max:     2000,
		Message: "limit must be between 50 and 2000",
	}}
	server := newTestServer(t, builder)

	recorder := doGet(t, server, "/api/versions/101/exploration-chain")
	requireStatus(t, recorder, http.StatusBadRequest)
	envelope := decodeError(t, recorder)
	if envelope.Error.Code != platform.CodeInvalidArgument {
		t.Fatalf("error code = %q", envelope.Error.Code)
	}
	if field, _ := envelope.Error.Details["field"].(string); field != "limit" {
		t.Fatalf("details.field = %v", envelope.Error.Details["field"])
	}
	if envelope.Error.Details["min"] != float64(50) || envelope.Error.Details["max"] != float64(2000) {
		t.Fatalf("details = %v", envelope.Error.Details)
	}
	if envelope.Error.Message != "limit must be between 50 and 2000" {
		t.Fatalf("message = %q", envelope.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// 404 / 500
// ---------------------------------------------------------------------------

// TestNotFoundMapping proves unknown versions and foreign batches are 404s with
// a stable reason.
func TestNotFoundMapping(t *testing.T) {
	tests := []struct {
		resource string
		id       int64
		reason   string
	}{
		{resource: "version", id: 999, reason: "version_not_found"},
		{resource: "batch", id: 88, reason: "batch_not_found"},
	}
	for _, test := range tests {
		t.Run(test.resource, func(t *testing.T) {
			builder := &fakeBuilder{err: &chain.ErrNotFound{Resource: test.resource, ID: test.id}}
			server := newTestServer(t, builder)

			recorder := doGet(t, server, "/api/versions/101/exploration-chain")
			requireStatus(t, recorder, http.StatusNotFound)
			envelope := decodeError(t, recorder)
			if envelope.Error.Code != platform.CodeNotFound {
				t.Fatalf("error code = %q, want not_found", envelope.Error.Code)
			}
			if reason, _ := envelope.Error.Details["reason"].(string); reason != test.reason {
				t.Fatalf("details.reason = %q, want %q", reason, test.reason)
			}
		})
	}
}

// TestUnexpectedErrorMapping proves an unexpected service failure is a 500
// internal error without leaking the provider message.
func TestUnexpectedErrorMapping(t *testing.T) {
	builder := &fakeBuilder{err: errors.New("database exploded")}
	server := newTestServer(t, builder)

	recorder := doGet(t, server, "/api/versions/101/exploration-chain")
	requireStatus(t, recorder, http.StatusInternalServerError)
	envelope := decodeError(t, recorder)
	if envelope.Error.Code != platform.CodeInternal {
		t.Fatalf("error code = %q, want internal", envelope.Error.Code)
	}
	if strings.Contains(envelope.Error.Message, "database exploded") {
		t.Fatalf("internal error leaked the provider message: %q", envelope.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// Method handling
// ---------------------------------------------------------------------------

// TestNonGetIsMethodNotAllowed proves the route is GET-only (spec §5.1).
func TestNonGetIsMethodNotAllowed(t *testing.T) {
	builder := &fakeBuilder{snapshot: sampleSnapshot(t)}
	server := newTestServer(t, builder)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		request := httptest.NewRequest(method, "/api/versions/101/exploration-chain",
			strings.NewReader(`{}`))
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s status = %d, want 405", method, recorder.Code)
		}
	}
	if builder.calls != 0 {
		t.Fatalf("builder calls = %d, want 0", builder.calls)
	}
}
