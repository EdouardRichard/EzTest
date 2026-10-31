package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	chain "github.com/EdouardRichard/EzTest/backend/internal/chain/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

type fakeChainService struct {
	view     chain.ChainView
	err      error
	calls    int
	version  int64
	filter   chain.ChainFilter
	hasRead  bool
	readWith func(versionID int64)
}

func (f *fakeChainService) Read(_ context.Context, versionID int64, filter chain.ChainFilter) (chain.ChainView, error) {
	f.calls++
	f.hasRead = true
	f.version = versionID
	f.filter = filter
	if f.err != nil {
		return chain.ChainView{}, f.err
	}
	view := f.view
	// Mimic ChainService: the normalized filter is echoed in the view.
	if view.Filters == (chain.ChainFilter{}) {
		view.Filters = filter.Normalize()
	}
	return view, nil
}

func newChainMux(t *testing.T, svc Service) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, New(svc))
	return mux
}

func getChain(t *testing.T, mux *http.ServeMux, target string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder
}

func decodeChainData(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response %s: %v", recorder.Body.String(), err)
	}
	if envelope.Data == nil {
		t.Fatalf("response has no data object: %s", recorder.Body.String())
	}
	return envelope.Data
}

func sampleChainView() chain.ChainView {
	return chain.ChainView{
		VersionID:      "42",
		GeneratedAt:    time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC),
		SnapshotCursor: 123,
		Counts: map[string]int{
			"requirements": 1, "scenarios": 1, "cases": 1, "waves": 1,
			"case_runs": 1, "issues": 1, "reports": 1, "evidence": 2,
		},
		Nodes: []chain.Node{{
			ID:            "case_run:200",
			Kind:          chain.NodeCaseRun,
			RefID:         "200",
			Label:         "Checkout run",
			Status:        "failed",
			VersionSUTID:  int64Ptr(5),
			VersionSUTIDs: []int64{5, 6},
			CaseVersionID: int64Ptr(100),
			WaveID:        int64Ptr(7),
			CaseRunID:     int64Ptr(200),
			CreatedAt:     time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC),
			Meta:          map[string]any{"failure_class": "application", "evidence_count": 2},
		}},
		Edges: []chain.Edge{{
			From: "case_version:100", To: "case_run:200", Kind: chain.EdgeExecutedBy,
		}},
		Truncated: true,
		Truncation: chain.Truncation{
			MaxNodes: 300, MaxEdges: 600, MaxRequirements: 100,
			Reason: chain.TruncationReasonNodeBudget,
		},
		Replay: chain.ReplayInfo{Mode: chain.ReplayModeLive, Approximate: true},
	}
}

func TestChainHandlerParsesFiltersAndMapsView(t *testing.T) {
	svc := &fakeChainService{view: sampleChainView()}
	mux := newChainMux(t, svc)

	recorder := getChain(t, mux,
		"/api/versions/42/exploration-chain?sut_id=5&page_id=50&wave_id=7&status=open&result=failed"+
			"&event_from=10&event_to=20&focus_node=issue%3A300&limit=50")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !svc.hasRead || svc.version != 42 {
		t.Fatalf("service read version = %d, want 42", svc.version)
	}
	want := chain.ChainFilter{
		VersionSUTID: int64Ptr(5),
		PageID:       int64Ptr(50),
		WaveID:       int64Ptr(7),
		Status:       "open",
		Result:       "failed",
		EventFrom:    int64Ptr(10),
		EventTo:      int64Ptr(20),
		FocusNode:    "issue:300",
		Limit:        50,
	}
	if !sameFilter(svc.filter, want) {
		t.Errorf("filter = %+v, want %+v", svc.filter, want)
	}

	data := decodeChainData(t, recorder)
	if data["version_id"] != "42" {
		t.Errorf("version_id = %v, want the string 42", data["version_id"])
	}
	if data["snapshot_cursor"] != float64(123) {
		t.Errorf("snapshot_cursor = %v, want 123", data["snapshot_cursor"])
	}
	if data["generated_at"] != "2026-11-01T04:00:00Z" {
		t.Errorf("generated_at = %v", data["generated_at"])
	}
	if data["truncated"] != true {
		t.Errorf("truncated = %v, want true", data["truncated"])
	}

	filters := data["filters"].(map[string]any)
	if filters["sut_id"] != "5" || filters["page_id"] != "50" || filters["wave_id"] != "7" {
		t.Errorf("filters ids = %v, want decimal strings", filters)
	}
	if filters["status"] != "open" || filters["result"] != "failed" {
		t.Errorf("filters text = %v", filters)
	}
	if filters["event_from"] != float64(10) || filters["event_to"] != float64(20) {
		t.Errorf("filters event bounds = %v", filters)
	}

	truncation := data["truncation"].(map[string]any)
	if truncation["reason"] != chain.TruncationReasonNodeBudget ||
		truncation["max_nodes"] != float64(300) || truncation["max_requirements"] != float64(100) {
		t.Errorf("truncation = %v", truncation)
	}

	replay := data["replay"].(map[string]any)
	if replay["mode"] != chain.ReplayModeLive || replay["approximate"] != true {
		t.Errorf("replay = %v, want live/approximate", replay)
	}
	if replay["wave_id"] != nil || replay["event_from"] != nil {
		t.Errorf("replay optional bounds = %v, want nulls", replay)
	}

	nodes := data["nodes"].([]any)
	if len(nodes) != 1 {
		t.Fatalf("nodes = %v", data["nodes"])
	}
	node := nodes[0].(map[string]any)
	if node["id"] != "case_run:200" || node["kind"] != "case_run" || node["ref_id"] != "200" {
		t.Errorf("node identity = %v", node)
	}
	if node["version_sut_id"] != "5" || node["case_version_id"] != "100" ||
		node["wave_id"] != "7" || node["case_run_id"] != "200" {
		t.Errorf("node id projection = %v", node)
	}
	if node["status"] != "failed" || node["created_at"] != "2026-11-01T03:00:00Z" {
		t.Errorf("node status/time = %v", node)
	}
	if node["requirement_id"] != nil || node["issue_id"] != nil {
		t.Errorf("unset node ids must be null: %v", node)
	}
	meta := node["meta"].(map[string]any)
	if meta["failure_class"] != "application" {
		t.Errorf("node meta = %v", meta)
	}

	edges := data["edges"].([]any)
	if len(edges) != 1 {
		t.Fatalf("edges = %v", data["edges"])
	}
	edge := edges[0].(map[string]any)
	if edge["from"] != "case_version:100" || edge["to"] != "case_run:200" || edge["kind"] != "executed_by" {
		t.Errorf("edge = %v", edge)
	}

	counts := data["counts"].(map[string]any)
	if counts["requirements"] != float64(1) || counts["evidence"] != float64(2) {
		t.Errorf("counts = %v", counts)
	}
}

func TestChainHandlerSerializesMetaIDArraysAsDecimalStrings(t *testing.T) {
	view := chain.ChainView{
		VersionID:   "42",
		GeneratedAt: time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC),
		Replay:      chain.ReplayInfo{Mode: chain.ReplayModeLive, Approximate: true},
		Nodes: []chain.Node{{
			ID:        "requirement:1",
			Kind:      chain.NodeRequirement,
			RefID:     "1",
			Label:     "Checkout",
			CreatedAt: time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC),
			Meta: map[string]any{
				"page_ids":       []int64{50, 51},
				"issue_ids":      []int{300},
				"case_run_ids":   []int32{200},
				"evidence_count": 2,
				"refs": []map[string]any{
					{"kind": "issue", "ref_id": int64(300)},
					{"kind": "case_run", "ref_id": 200},
				},
			},
		}},
	}
	svc := &fakeChainService{view: view}
	mux := newChainMux(t, svc)

	recorder := getChain(t, mux, "/api/versions/42/exploration-chain")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	data := decodeChainData(t, recorder)
	node := data["nodes"].([]any)[0].(map[string]any)
	meta := node["meta"].(map[string]any)

	if got := meta["page_ids"]; !equalStrings(got, []string{"50", "51"}) {
		t.Errorf("page_ids = %#v, want [\"50\",\"51\"]", got)
	}
	if got := meta["issue_ids"]; !equalStrings(got, []string{"300"}) {
		t.Errorf("issue_ids = %#v, want [\"300\"]", got)
	}
	if got := meta["case_run_ids"]; !equalStrings(got, []string{"200"}) {
		t.Errorf("case_run_ids = %#v, want [\"200\"]", got)
	}
	if meta["evidence_count"] != float64(2) {
		t.Errorf("non-ID meta value = %#v, want the numeric count", meta["evidence_count"])
	}
	refs := meta["refs"].([]any)
	for index, want := range []string{"300", "200"} {
		ref := refs[index].(map[string]any)
		if ref["ref_id"] != want {
			t.Errorf("refs[%d].ref_id = %#v, want %q", index, ref["ref_id"], want)
		}
	}
}

func equalStrings(value any, want []string) bool {
	list, ok := value.([]any)
	if !ok || len(list) != len(want) {
		return false
	}
	for index, item := range list {
		if item != want[index] {
			return false
		}
	}
	return true
}

func TestChainHandlerEmptyGraphIsNot404(t *testing.T) {
	svc := &fakeChainService{view: chain.ChainView{
		VersionID:   "42",
		GeneratedAt: time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC),
		Counts:      map[string]int{},
		Replay:      chain.ReplayInfo{Mode: chain.ReplayModeLive, Approximate: true},
	}}
	mux := newChainMux(t, svc)

	recorder := getChain(t, mux, "/api/versions/42/exploration-chain")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	data := decodeChainData(t, recorder)
	// Empty graph must serialize as [] (not null) so the UI can render it.
	if nodes, ok := data["nodes"].([]any); !ok || len(nodes) != 0 {
		t.Errorf("nodes = %#v, want []", data["nodes"])
	}
	if edges, ok := data["edges"].([]any); !ok || len(edges) != 0 {
		t.Errorf("edges = %#v, want []", data["edges"])
	}
	if counts, ok := data["counts"].(map[string]any); !ok || len(counts) != 0 {
		t.Errorf("counts = %#v, want {}", data["counts"])
	}
	replay := data["replay"].(map[string]any)
	if replay["approximate"] != true {
		t.Errorf("replay = %v, want approximate=true", replay)
	}
}

func TestChainHandlerPassesFocusNodeThrough(t *testing.T) {
	svc := &fakeChainService{view: sampleChainView()}
	mux := newChainMux(t, svc)

	recorder := getChain(t, mux, "/api/versions/42/exploration-chain?focus_node=issue%3A300")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if svc.filter.FocusNode != "issue:300" {
		t.Errorf("focus_node = %q, want issue:300", svc.filter.FocusNode)
	}
}

func TestChainHandlerRejectsInvalidQueries(t *testing.T) {
	tests := []struct {
		name   string
		query  string
		field  string
		status int
	}{
		{name: "invalid sut id", query: "?sut_id=abc", field: "sut_id", status: http.StatusBadRequest},
		{name: "zero sut id", query: "?sut_id=0", field: "sut_id", status: http.StatusBadRequest},
		{name: "invalid page id", query: "?page_id=-1", field: "page_id", status: http.StatusBadRequest},
		{name: "invalid wave id", query: "?wave_id=1.5", field: "wave_id", status: http.StatusBadRequest},
		{name: "negative event from", query: "?event_from=-1", field: "event_from", status: http.StatusBadRequest},
		{name: "non numeric event to", query: "?event_to=soon", field: "event_to", status: http.StatusBadRequest},
		{name: "non numeric focus ref", query: "?focus_node=issue%3Aabc", field: "focus_node", status: http.StatusBadRequest},
		{name: "invalid focus kind", query: "?focus_node=Issue%3A300", field: "focus_node", status: http.StatusBadRequest},
		{name: "negative focus ref", query: "?focus_node=issue%3A-1", field: "focus_node", status: http.StatusBadRequest},
		{name: "limit below range", query: "?limit=0", field: "limit", status: http.StatusBadRequest},
		{name: "limit above range", query: "?limit=1001", field: "limit", status: http.StatusBadRequest},
		{name: "non numeric limit", query: "?limit=many", field: "limit", status: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc := &fakeChainService{view: sampleChainView()}
			mux := newChainMux(t, svc)
			recorder := getChain(t, mux, "/api/versions/42/exploration-chain"+test.query)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, test.status, recorder.Body.String())
			}
			if svc.calls != 0 {
				t.Errorf("service was called for an invalid query")
			}
			var envelope struct {
				Error map[string]any `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if envelope.Error["code"] != "invalid_argument" {
				t.Errorf("error code = %v, want invalid_argument", envelope.Error["code"])
			}
			details, _ := envelope.Error["details"].(map[string]any)
			if details["field"] != test.field {
				t.Errorf("error field = %v, want %s", details["field"], test.field)
			}
		})
	}
}

func TestChainHandlerRejectsInvalidVersionPath(t *testing.T) {
	svc := &fakeChainService{view: sampleChainView()}
	mux := newChainMux(t, svc)

	recorder := getChain(t, mux, "/api/versions/not-a-number/exploration-chain")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", recorder.Code, recorder.Body.String())
	}
	if svc.calls != 0 {
		t.Errorf("service was called for an invalid version id")
	}
}

func TestChainHandlerMapsServiceErrors(t *testing.T) {
	svc := &fakeChainService{err: platform.NotFound("version")}
	mux := newChainMux(t, svc)

	recorder := getChain(t, mux, "/api/versions/42/exploration-chain")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if envelope.Error["code"] != "not_found" {
		t.Errorf("error code = %v, want not_found", envelope.Error["code"])
	}
}

func TestChainHandlerMapsUnknownServiceErrorTo500(t *testing.T) {
	svc := &fakeChainService{err: errors.New("boom")}
	mux := newChainMux(t, svc)

	recorder := getChain(t, mux, "/api/versions/42/exploration-chain")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", recorder.Code, recorder.Body.String())
	}
}

func sameFilter(left, right chain.ChainFilter) bool {
	return sameID(left.VersionSUTID, right.VersionSUTID) &&
		sameID(left.PageID, right.PageID) &&
		sameID(left.WaveID, right.WaveID) &&
		sameID(left.EventFrom, right.EventFrom) &&
		sameID(left.EventTo, right.EventTo) &&
		left.Status == right.Status &&
		left.Result == right.Result &&
		left.FocusNode == right.FocusNode &&
		left.Limit == right.Limit
}

func sameID(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func int64Ptr(value int64) *int64 { return &value }
