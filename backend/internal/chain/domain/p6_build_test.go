package chain

import (
	"strconv"
	"testing"
	"time"
)

// p6Source returns a small but complete P6 chain source: three requirements,
// two of them with a scenario/case, one case run, two issues, one report and
// one closure. It is shared by the P6 domain tests so the expected counts and
// weighted edges stay in one place.
func p6Source() SourceGraph {
	requirement := func(id int64, title string) Node {
		node := Node{ID: NodeID(NodeRequirement, id), Kind: NodeRequirement, RefID: idString(id), Label: title}
		node.RequirementID = &id
		return node
	}
	scenario := func(id, requirementID int64, title string) Node {
		node := Node{
			ID: NodeID(NodeScenario, id), Kind: NodeScenario, RefID: idString(id), Label: title,
			RequirementID: &requirementID,
		}
		return node
	}
	caseVersion := func(id, scenarioID, requirementID int64, title, status string) Node {
		node := Node{
			ID: NodeID(NodeCaseVersion, id), Kind: NodeCaseVersion, RefID: idString(id), Label: title,
			Status: status, ScenarioID: &scenarioID, RequirementID: &requirementID, CaseVersionID: &id,
			Meta: map[string]any{"step_count": 3, "assertion_count": 2},
		}
		return node
	}
	run := func(id, caseVersionID, waveID int64, status string) Node {
		node := Node{
			ID: NodeID(NodeCaseRun, id), Kind: NodeCaseRun, RefID: idString(id),
			Label: "Case run " + idString(id), Status: status, Result: status,
			CaseVersionID: &caseVersionID, WaveID: &waveID,
			Meta: map[string]any{
				"attempt": 2, "failure_class": "application", "evidence_count": 3,
				"step_count": 3, "assertion_count": 2, "duration_ms": 1500,
			},
		}
		return node
	}
	issue := func(id, requirementID, caseRunID int64, status, severity string) Node {
		node := Node{
			ID: NodeID(NodeIssue, id), Kind: NodeIssue, RefID: idString(id), Label: "Issue " + idString(id),
			Status: status, RequirementID: &requirementID, CaseRunID: &caseRunID,
			Meta: map[string]any{
				"severity": severity, "category": "api", "evidence_count": 2,
				"code_ref_count": 1, "merged_into_id": "999",
			},
		}
		return node
	}
	waveID := int64(7)
	wave := Node{
		ID: NodeID(NodeWave, waveID), Kind: NodeWave, RefID: idString(waveID), Label: "Wave 7",
		Status: "done", WaveID: &waveID, Meta: map[string]any{"trigger": "auto"},
	}
	reportID := int64(900)
	report := Node{
		ID: NodeID(NodeReport, reportID), Kind: NodeReport, RefID: idString(reportID), Label: "Wave 7 report",
		Status: "published", WaveID: &waveID, ReportID: &reportID,
		Meta: map[string]any{"kind": "wave", "revision": 2},
	}
	closureID := int64(400)
	closure := Node{
		ID: NodeID(NodeClosure, closureID), Kind: NodeClosure, RefID: idString(closureID), Label: "Closure round 1",
		Status: "closed", RequirementID: int64Ptr(1),
	}

	return SourceGraph{
		Requirements: []Node{requirement(1, "Checkout"), requirement(2, "Refund"), requirement(3, "Uncovered")},
		Scenarios: []Node{
			scenario(10, 1, "Checkout happy path"),
			scenario(11, 2, "Refund flow"),
		},
		Cases: []Node{
			caseVersion(100, 10, 1, "Checkout pays with card", "confirmed"),
			caseVersion(101, 11, 2, "Refund fails", "stale"),
		},
		Waves:    []Node{wave},
		CaseRuns: []Node{run(200, 100, 7, "failed")},
		Issues: []Node{
			issue(300, 1, 200, "open", "major"),
			issue(301, 2, 0, "dismissed", "minor"),
		},
		Reports:  []Node{report},
		Closures: []Node{closure},
	}
}

func idString(id int64) string { return strconv.FormatInt(id, 10) }

func p6NodeMap(nodes []Node) map[string]Node {
	out := make(map[string]Node, len(nodes))
	for _, node := range nodes {
		out[node.ID] = node
	}
	return out
}

func p6EdgeSet(edges []Edge) map[string]bool {
	out := make(map[string]bool, len(edges))
	for _, edge := range edges {
		out[string(edge.Kind)+"|"+edge.From+"|"+edge.To] = true
	}
	return out
}

func p6Build(t *testing.T, src SourceGraph, filter ChainFilter, budget Budget) ChainView {
	t.Helper()
	return BuildChain(src, filter, budget, 7, time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC))
}

func TestP6ChainFilterNormalize(t *testing.T) {
	filter := ChainFilter{
		Q:                "  Checkout ",
		AbnormalOnly:     true,
		IssueStatus:      " OPEN ",
		IssueSeverity:    " Major ",
		RequirementLimit: 25,
	}
	normalized := filter.Normalize()
	if normalized.Q != "Checkout" {
		t.Errorf("Q = %q, want Checkout", normalized.Q)
	}
	if !normalized.AbnormalOnly {
		t.Error("AbnormalOnly = false, want true")
	}
	if normalized.IssueStatus != "open" || normalized.IssueSeverity != "major" {
		t.Errorf("issue enums = %q/%q, want open/major", normalized.IssueStatus, normalized.IssueSeverity)
	}
	if normalized.RequirementLimit != 25 {
		t.Errorf("RequirementLimit = %d, want 25", normalized.RequirementLimit)
	}

	for _, invalid := range []int{-1, 0, 501} {
		got := ChainFilter{RequirementLimit: invalid}.Normalize()
		if got.RequirementLimit != 0 {
			t.Errorf("RequirementLimit(%d) = %d, want unset 0", invalid, got.RequirementLimit)
		}
	}
}

func TestP6BuildChainFilterSemantics(t *testing.T) {
	t.Run("q filters title-bearing nodes and trims induced edges", func(t *testing.T) {
		view := p6Build(t, p6Source(), ChainFilter{Q: "checkout"}, Budget{})

		nodes := p6NodeMap(view.Nodes)
		for _, id := range []string{"requirement:1", "scenario:10", "case_version:100"} {
			if _, ok := nodes[id]; !ok {
				t.Errorf("matching node %s missing", id)
			}
		}
		for _, id := range []string{"requirement:2", "scenario:11", "case_version:101", "requirement:3"} {
			if _, ok := nodes[id]; ok {
				t.Errorf("non-matching title node %s survived", id)
			}
		}
		// Context nodes are not title filters; they stay and are counted.
		for _, id := range []string{"wave:7", "case_run:200", "issue:300", "report:900"} {
			if _, ok := nodes[id]; !ok {
				t.Errorf("context node %s missing", id)
			}
		}
		edges := p6EdgeSet(view.Edges)
		if !edges["has_scenario|requirement:1|scenario:10"] || !edges["has_case|scenario:10|case_version:100"] {
			t.Errorf("matching subtree edges missing: %v", edges)
		}
		if edges["has_scenario|requirement:2|scenario:11"] || edges["has_case|scenario:11|case_version:101"] {
			t.Errorf("edges to filtered title nodes survived: %v", edges)
		}
		if view.Filters.Q != "checkout" {
			t.Errorf("Filters.Q = %q, want normalized original casing checkout", view.Filters.Q)
		}
	})

	t.Run("abnormal_only keeps only abnormal case runs", func(t *testing.T) {
		src := p6Source()
		src.Cases = append(src.Cases, Node{
			ID: "case_version:102", Kind: NodeCaseVersion, RefID: "102", Label: "Another case",
			ScenarioID: int64Ptr(10), RequirementID: int64Ptr(1), CaseVersionID: int64Ptr(102),
		})
		passed := src.CaseRuns[0]
		passed.ID, passed.RefID, passed.Status, passed.Result = "case_run:201", "201", "passed", "passed"
		passed.CaseVersionID = int64Ptr(102)
		src.CaseRuns = append(src.CaseRuns, passed)

		view := p6Build(t, src, ChainFilter{AbnormalOnly: true}, Budget{})
		nodes := p6NodeMap(view.Nodes)
		if _, ok := nodes["case_run:200"]; !ok {
			t.Error("failed case run missing under abnormal_only")
		}
		if _, ok := nodes["case_run:201"]; ok {
			t.Error("passed case run survived abnormal_only")
		}
		if got := view.Counts["case_runs"]; got != 1 {
			t.Errorf("Counts[case_runs] = %d, want 1", got)
		}
		if got := view.CaseRunStatusCounts.Failed; got != 1 {
			t.Errorf("by_case_run_status.failed = %d, want 1", got)
		}
	})

	t.Run("issue status and severity filters only prune issue nodes", func(t *testing.T) {
		byStatus := p6Build(t, p6Source(), ChainFilter{IssueStatus: "dismissed"}, Budget{})
		nodes := p6NodeMap(byStatus.Nodes)
		if _, ok := nodes["issue:300"]; ok {
			t.Error("open issue survived issue_status=dismissed")
		}
		if _, ok := nodes["issue:301"]; !ok {
			t.Error("dismissed issue missing")
		}
		if len(byStatus.Nodes) != 12 {
			t.Errorf("nodes = %d, want every non-issue node plus one issue", len(byStatus.Nodes))
		}

		bySeverity := p6Build(t, p6Source(), ChainFilter{IssueSeverity: "major"}, Budget{})
		nodes = p6NodeMap(bySeverity.Nodes)
		if _, ok := nodes["issue:300"]; !ok {
			t.Error("major issue missing")
		}
		if _, ok := nodes["issue:301"]; ok {
			t.Error("minor issue survived issue_severity=major")
		}
		if got := bySeverity.IssueSeverityCounts.Major; got != 1 {
			t.Errorf("by_issue_severity.major = %d, want 1", got)
		}
	})

	t.Run("requirement_limit overrides the configured root budget", func(t *testing.T) {
		src := SourceGraph{}
		for id := int64(1); id <= 3; id++ {
			src.Requirements = append(src.Requirements, Node{
				ID: NodeID(NodeRequirement, id), Kind: NodeRequirement, RefID: idString(id),
				Label: "Requirement " + idString(id),
			})
		}
		view := p6Build(t, src, ChainFilter{RequirementLimit: 2}, Budget{MaxRequirements: 100})
		if view.Truncation.MaxRequirements != 2 {
			t.Errorf("MaxRequirements = %d, want the filter override 2", view.Truncation.MaxRequirements)
		}
		if !view.Truncated || view.Truncation.Reason != TruncationReasonRequirementBudget {
			t.Errorf("truncation = %+v, want requirement_budget", view.Truncation)
		}
		if got := len(view.Nodes); got != 2 {
			t.Errorf("nodes = %d, want 2", got)
		}
		if !view.TruncatedLayers.Requirements {
			t.Error("truncated_layers.requirements = false, want true")
		}
	})
}

func TestP6LatestCaseRunPerCaseVersion(t *testing.T) {
	src := p6Source()
	// Higher wave wins even with a lower run id.
	olderWave := Node{
		ID: "case_run:210", Kind: NodeCaseRun, RefID: "210", Status: "passed", Result: "passed",
		CaseVersionID: int64Ptr(100), WaveID: int64Ptr(6),
	}
	// Same wave: the higher run id wins.
	sameWave := Node{
		ID: "case_run:205", Kind: NodeCaseRun, RefID: "205", Status: "passed", Result: "passed",
		CaseVersionID: int64Ptr(100), WaveID: int64Ptr(7),
	}
	src.CaseRuns = append(src.CaseRuns, olderWave, sameWave)

	view := p6Build(t, src, ChainFilter{}, Budget{})
	nodes := p6NodeMap(view.Nodes)
	if _, ok := nodes["case_run:205"]; !ok {
		t.Errorf("latest run case_run:205 missing: %v", view.Nodes)
	}
	for _, id := range []string{"case_run:200", "case_run:210"} {
		if _, ok := nodes[id]; ok {
			t.Errorf("superseded run %s survived", id)
		}
	}
	if got := view.Counts["case_runs"]; got != 1 {
		t.Errorf("Counts[case_runs] = %d, want 1", got)
	}
	if got := view.CaseRunStatusCounts.Passed; got != 1 {
		t.Errorf("by_case_run_status.passed = %d, want 1", got)
	}
	edges := p6EdgeSet(view.Edges)
	if !edges["executed_by|case_version:100|case_run:205"] {
		t.Errorf("latest executed_by edge missing: %v", edges)
	}
	if edges["executed_by|case_version:100|case_run:200"] || edges["executed_by|case_version:100|case_run:210"] {
		t.Errorf("stale executed_by edges survived: %v", edges)
	}
}

func TestP6CountsCoverageAndTypedMeta(t *testing.T) {
	view := p6Build(t, p6Source(), ChainFilter{}, Budget{})
	if got, want := view.Counts["requirements"], 3; got != want {
		t.Errorf("Counts[requirements] = %d, want %d", got, want)
	}
	if got := view.CaseRunStatusCounts.Failed; got != 1 {
		t.Errorf("by_case_run_status.failed = %d, want 1", got)
	}
	if got := view.IssueSeverityCounts.Major; got != 1 {
		t.Errorf("by_issue_severity.major = %d, want 1", got)
	}
	if got := view.IssueStatusCounts.Dismissed; got != 1 {
		t.Errorf("by_issue_status.dismissed = %d, want 1", got)
	}
	if view.Coverage != (CoverageCounts{Requirements: 3, Covered: 2, Uncovered: 1}) {
		t.Errorf("coverage = %+v, want 3/2/1", view.Coverage)
	}

	nodes := p6NodeMap(view.Nodes)
	if meta := nodes["requirement:1"].Meta; meta["case_count"] != 1 || meta["executed_count"] != 1 ||
		meta["failed_count"] != 1 || meta["uncovered"] != false {
		t.Errorf("requirement:1 meta = %v", meta)
	}
	if meta := nodes["requirement:2"].Meta; meta["case_count"] != 1 || meta["executed_count"] != 0 ||
		meta["failed_count"] != 0 {
		t.Errorf("requirement:2 meta = %v", meta)
	}
	if meta := nodes["requirement:3"].Meta; meta["uncovered"] != true {
		t.Errorf("requirement:3 uncovered meta = %v", meta)
	}
	if meta := nodes["scenario:10"].Meta; meta["requirement_id"] != "1" || meta["case_count"] != 1 {
		t.Errorf("scenario:10 meta = %v", meta)
	}
	if meta := nodes["case_version:100"].Meta; meta["scenario_id"] != "10" || meta["stale"] != false ||
		meta["step_count"] != 3 || meta["assertion_count"] != 2 {
		t.Errorf("case_version:100 meta = %v", meta)
	}
	if meta := nodes["case_version:101"].Meta; meta["stale"] != true {
		t.Errorf("case_version:101 stale meta = %v", meta)
	}
	if meta := nodes["wave:7"].Meta; meta["wave_status"] != "done" || meta["trigger"] != "auto" {
		t.Errorf("wave:7 meta = %v", meta)
	}
	if meta := nodes["case_run:200"].Meta; meta["attempt"] != 2 || meta["failure_class"] != "application" ||
		meta["evidence_count"] != 3 || meta["duration_ms"] != 1500 {
		t.Errorf("case_run:200 meta = %v", meta)
	}
	if meta := nodes["issue:300"].Meta; meta["severity"] != "major" || meta["issue_status"] != "open" ||
		meta["category"] != "api" || meta["evidence_count"] != 2 || meta["code_ref_count"] != 1 ||
		meta["merged_into_id"] != "999" {
		t.Errorf("issue:300 meta = %v", meta)
	}
	if meta := nodes["report:900"].Meta; meta["report_kind"] != "wave" ||
		meta["report_status"] != "published" || meta["revision"] != 2 {
		t.Errorf("report:900 meta = %v", meta)
	}
	if meta := nodes["closure:400"].Meta; meta["closure_status"] != "closed" {
		t.Errorf("closure:400 meta = %v", meta)
	}
}

func TestP6TruncatedLayers(t *testing.T) {
	view := p6Build(t, p6Source(), ChainFilter{}, Budget{MaxNodes: 10})
	if !view.Truncated {
		t.Fatal("view.Truncated = false, want true")
	}
	if !view.TruncatedLayers.Issues || !view.TruncatedLayers.CaseRuns {
		t.Errorf("truncated_layers = %+v, want issues and case_runs true", view.TruncatedLayers)
	}
	if view.TruncatedLayers.Cases || view.TruncatedLayers.Reports || view.TruncatedLayers.Requirements {
		t.Errorf("truncated_layers = %+v, want cases/reports/requirements untouched", view.TruncatedLayers)
	}

	within := p6Build(t, p6Source(), ChainFilter{}, Budget{})
	if within.TruncatedLayers != (TruncatedLayers{}) {
		t.Errorf("untruncated layers = %+v, want all false", within.TruncatedLayers)
	}
}
