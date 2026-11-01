package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	chain "github.com/EdouardRichard/EzTest/backend/internal/chain/domain"
)

func TestP6HandlerParsesIncrementalFilters(t *testing.T) {
	svc := &fakeChainService{view: sampleChainView()}
	mux := newChainMux(t, svc)

	recorder := getChain(t, mux,
		"/api/versions/42/exploration-chain?status=failed&result=interrupted&q=Checkout"+
			"&abnormal_only=true&issue_status=confirmed&issue_severity=major&requirement_limit=40")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	want := chain.ChainFilter{
		Status:           "failed",
		Result:           "interrupted",
		Q:                "Checkout",
		AbnormalOnly:     true,
		IssueStatus:      "confirmed",
		IssueSeverity:    "major",
		RequirementLimit: 40,
	}
	if !sameFilter(svc.filter, want) {
		t.Errorf("filter = %+v, want %+v", svc.filter, want)
	}

	data := decodeChainData(t, recorder)
	filters := data["filters"].(map[string]any)
	if filters["q"] != "Checkout" || filters["abnormal_only"] != true {
		t.Errorf("filters q/abnormal = %v", filters)
	}
	if filters["issue_status"] != "confirmed" || filters["issue_severity"] != "major" {
		t.Errorf("filters issue = %v", filters)
	}
	if filters["requirement_limit"] != float64(40) {
		t.Errorf("filters requirement_limit = %v", filters["requirement_limit"])
	}
}

func TestP6HandlerSerializesCountsMetaLayersAndReplay(t *testing.T) {
	view := sampleChainView()
	view.CaseRunStatusCounts = chain.CaseRunStatusCounts{Passed: 2, Failed: 1, Interrupted: 1}
	view.IssueSeverityCounts = chain.IssueSeverityCounts{Major: 1, Trivial: 2}
	view.IssueStatusCounts = chain.IssueStatusCounts{Open: 2, Merged: 1}
	view.Coverage = chain.CoverageCounts{Requirements: 3, Covered: 2, Uncovered: 1}
	view.DegradedLayers = []string{chain.LayerDesign, chain.LayerDelivery}
	view.TruncatedLayers = chain.TruncatedLayers{Requirements: true, Issues: true}
	view.Replay = chain.ReplayInfo{
		Mode: chain.ReplayModeRange, EventFrom: int64Ptr(5), EventTo: int64Ptr(9),
		Approximate: true, EventLimit: 3, Truncated: true,
	}
	view.Nodes[0].Meta["merged_into_id"] = int64(999)
	view.Nodes[0].Meta["requirement_id"] = "1"
	view.Nodes[0].Meta["evidence_count"] = 4

	svc := &fakeChainService{view: view}
	mux := newChainMux(t, svc)
	recorder := getChain(t, mux, "/api/versions/42/exploration-chain?event_from=5&event_to=9")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	data := decodeChainData(t, recorder)
	counts := data["counts"].(map[string]any)
	byStatus := counts["by_case_run_status"].(map[string]any)
	if byStatus["passed"] != float64(2) || byStatus["failed"] != float64(1) || byStatus["interrupted"] != float64(1) {
		t.Errorf("by_case_run_status = %v", byStatus)
	}
	bySeverity := counts["by_issue_severity"].(map[string]any)
	if bySeverity["major"] != float64(1) || bySeverity["trivial"] != float64(2) {
		t.Errorf("by_issue_severity = %v", bySeverity)
	}
	byIssueStatus := counts["by_issue_status"].(map[string]any)
	if byIssueStatus["open"] != float64(2) || byIssueStatus["merged"] != float64(1) {
		t.Errorf("by_issue_status = %v", byIssueStatus)
	}
	coverage := counts["coverage"].(map[string]any)
	if coverage["requirements"] != float64(3) || coverage["covered"] != float64(2) || coverage["uncovered"] != float64(1) {
		t.Errorf("coverage = %v", coverage)
	}
	// P4 scalar counts stay next to the typed objects.
	if counts["requirements"] != float64(1) || counts["evidence"] != float64(2) {
		t.Errorf("legacy counts = %v", counts)
	}

	degraded := data["degraded_layers"].([]any)
	if len(degraded) != 2 || degraded[0] != chain.LayerDesign || degraded[1] != chain.LayerDelivery {
		t.Errorf("degraded_layers = %v", degraded)
	}
	truncatedLayers := data["truncated_layers"].(map[string]any)
	if truncatedLayers["requirements"] != true || truncatedLayers["issues"] != true || truncatedLayers["cases"] != false {
		t.Errorf("truncated_layers = %v", truncatedLayers)
	}
	replay := data["replay"].(map[string]any)
	if replay["event_limit"] != float64(3) || replay["truncated"] != true {
		t.Errorf("replay event fields = %v", replay)
	}

	node := data["nodes"].([]any)[0].(map[string]any)
	meta := node["meta"].(map[string]any)
	if meta["merged_into_id"] != "999" || meta["requirement_id"] != "1" ||
		meta["evidence_count"] != float64(4) {
		t.Errorf("typed meta projection = %v", meta)
	}
}

func TestP6HandlerEmptyLayersSerializeAsArrays(t *testing.T) {
	svc := &fakeChainService{view: chain.ChainView{
		VersionID:   "42",
		GeneratedAt: time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC),
		Counts:      map[string]int{},
		Replay:      chain.ReplayInfo{Mode: chain.ReplayModeLive, Approximate: true, EventLimit: 500},
	}}
	mux := newChainMux(t, svc)
	recorder := getChain(t, mux, "/api/versions/42/exploration-chain")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	data := decodeChainData(t, recorder)
	if degraded, ok := data["degraded_layers"].([]any); !ok || len(degraded) != 0 {
		t.Errorf("degraded_layers = %#v, want []", data["degraded_layers"])
	}
	if layers, ok := data["truncated_layers"].(map[string]any); !ok || len(layers) != 5 {
		t.Errorf("truncated_layers = %#v, want an all-false object", data["truncated_layers"])
	}
	replay := data["replay"].(map[string]any)
	if replay["event_limit"] != float64(500) || replay["truncated"] != false {
		t.Errorf("replay = %v", replay)
	}
}

func TestP6HandlerRejectsInvalidIncrementalQueries(t *testing.T) {
	tests := []struct {
		name  string
		query string
		field string
	}{
		{name: "invalid status enum", query: "?status=bogus", field: "status"},
		{name: "invalid result enum", query: "?result=maybe", field: "result"},
		{name: "q too long", query: "?q=" + strings.Repeat("%E6%B5%8B", 101), field: "q"},
		{name: "invalid abnormal bool", query: "?abnormal_only=yes", field: "abnormal_only"},
		{name: "invalid issue status", query: "?issue_status=bogus", field: "issue_status"},
		{name: "invalid issue severity", query: "?issue_severity=critical", field: "issue_severity"},
		{name: "requirement limit low", query: "?requirement_limit=0", field: "requirement_limit"},
		{name: "requirement limit high", query: "?requirement_limit=501", field: "requirement_limit"},
		{name: "requirement limit textual", query: "?requirement_limit=many", field: "requirement_limit"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc := &fakeChainService{view: sampleChainView()}
			mux := newChainMux(t, svc)
			recorder := getChain(t, mux, "/api/versions/42/exploration-chain"+test.query)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", recorder.Code, recorder.Body.String())
			}
			if svc.calls != 0 {
				t.Errorf("service was called for an invalid P6 query")
			}
			var envelope struct {
				Error map[string]any `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if envelope.Error["code"] != "invalid_argument" {
				t.Errorf("error code = %v", envelope.Error["code"])
			}
			details, _ := envelope.Error["details"].(map[string]any)
			if details["field"] != test.field {
				t.Errorf("error field = %v, want %s", details["field"], test.field)
			}
		})
	}
}

func TestP6HandlerAcceptsDocumentedEnums(t *testing.T) {
	for _, status := range []string{
		"pending", "running", "paused", "passed", "failed", "blocked", "inconclusive",
		"skipped", "interrupted", "superseded", "planned", "done", "cancelled",
		"draft", "clarified", "closed", "obsolete", "open", "confirmed", "dismissed",
		"merged", "published", "stale", "reopened",
	} {
		svc := &fakeChainService{view: sampleChainView()}
		mux := newChainMux(t, svc)
		recorder := getChain(t, mux, "/api/versions/42/exploration-chain?status="+status)
		if recorder.Code != http.StatusOK {
			t.Errorf("status %q = %d, want 200; body = %s", status, recorder.Code, recorder.Body.String())
		}
	}
	for _, result := range []string{"passed", "failed", "blocked", "inconclusive", "skipped", "interrupted"} {
		svc := &fakeChainService{view: sampleChainView()}
		mux := newChainMux(t, svc)
		recorder := getChain(t, mux, "/api/versions/42/exploration-chain?result="+result)
		if recorder.Code != http.StatusOK {
			t.Errorf("result %q = %d, want 200; body = %s", result, recorder.Code, recorder.Body.String())
		}
	}
}
