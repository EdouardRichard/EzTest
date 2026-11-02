package chain

import (
	"slices"
	"strconv"
	"testing"
	"time"
)

func testNode(kind NodeKind, refID int64, label string) Node {
	return Node{
		ID:    NodeID(kind, refID),
		Kind:  kind,
		RefID: strconv.FormatInt(refID, 10),
		Label: label,
	}
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}

func completeSource() SourceGraph {
	created := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)

	requirement := testNode(NodeRequirement, 1, "Checkout")
	requirement.Status = "clarified"
	requirement.CreatedAt = created
	requirement.UpdatedAt = created.Add(3 * time.Hour)
	requirement.Meta = map[string]any{"target_version_sut_ids": []int64{5, 6}}

	scenario := testNode(NodeScenario, 10, "Happy path")
	scenario.RequirementID = int64Ptr(1)
	scenario.VersionSUTIDs = []int64{5, 6}
	scenario.CreatedAt = created
	scenario.Meta = map[string]any{
		"page_ids": []int64{50, 51},
	}

	caseVersion := testNode(NodeCaseVersion, 100, "Pays with card")
	caseVersion.RequirementID = int64Ptr(1)
	caseVersion.ScenarioID = int64Ptr(10)
	caseVersion.VersionSUTID = int64Ptr(5)
	caseVersion.VersionSUTIDs = []int64{5, 6}
	caseVersion.PageID = int64Ptr(50)
	caseVersion.Status = "confirmed"
	caseVersion.Result = "failed"
	caseVersion.CreatedAt = created
	caseVersion.UpdatedAt = created.Add(2 * time.Hour)
	caseVersion.Meta = map[string]any{
		"page_ids": []int64{50, 51},
		"revision": 2,
	}

	wave := testNode(NodeWave, 7, "Wave 1")
	wave.Status = "completed"
	wave.CreatedAt = created

	caseRun := testNode(NodeCaseRun, 200, "Pays with card")
	caseRun.CaseVersionID = int64Ptr(100)
	caseRun.WaveID = int64Ptr(7)
	caseRun.VersionSUTID = int64Ptr(5)
	caseRun.VersionSUTIDs = []int64{5, 6}
	caseRun.PageID = int64Ptr(51)
	caseRun.Status = "done"
	caseRun.Result = "failed"
	caseRun.CreatedAt = created.Add(time.Hour)
	caseRun.Meta = map[string]any{
		"page_ids":        []int64{51},
		"wave_ids":        []int64{7},
		"evidence_count":  2,
		"step_count":      4,
		"assertion_count": 3,
	}

	issue := testNode(NodeIssue, 300, "Amount mismatch")
	issue.RequirementID = int64Ptr(1)
	issue.CaseRunID = int64Ptr(200)
	issue.WaveID = int64Ptr(7)
	issue.ReportID = int64Ptr(900)
	issue.MergedIntoID = int64Ptr(301)
	issue.Status = "open"
	issue.VersionSUTIDs = []int64{5, 6}
	issue.PageID = int64Ptr(51)
	issue.CreatedAt = created.Add(90 * time.Minute)
	issue.Meta = map[string]any{
		"page_ids": []int64{51},
		"wave_ids": []int64{7},
		"severity": "major",
		"category": "ui",
	}

	mergedTarget := testNode(NodeIssue, 301, "Duplicate amount mismatch")
	mergedTarget.Status = "merged"
	mergedTarget.CreatedAt = created.Add(91 * time.Minute)

	report := testNode(NodeReport, 900, "Wave 1 report")
	report.WaveID = int64Ptr(7)
	report.Status = "published"
	report.PageID = int64Ptr(51)
	report.VersionSUTIDs = []int64{5, 6}
	report.CreatedAt = created.Add(2 * time.Hour)
	report.Meta = map[string]any{
		"page_ids": []int64{51},
		"wave_ids": []int64{7},
		"kind":     "wave",
		"revision": 1,
		"as_of":    created,
	}

	closure := testNode(NodeClosure, 400, "Closed")
	closure.RequirementID = int64Ptr(1)
	closure.PageID = int64Ptr(51)
	closure.CreatedAt = created.Add(4 * time.Hour)
	closure.Meta = map[string]any{
		"page_ids":     []int64{51},
		"close_round":  1,
		"reopen_count": 0,
	}

	return SourceGraph{
		Requirements: []Node{requirement},
		Scenarios:    []Node{scenario},
		Cases:        []Node{caseVersion},
		Waves:        []Node{wave},
		CaseRuns:     []Node{caseRun},
		Issues:       []Node{issue, mergedTarget},
		Reports:      []Node{report},
		Closures:     []Node{closure},
		SUTs:         map[int64]string{5: "Pay", 6: "Order"},
		Pages:        map[int64]string{50: "Checkout", 51: "Cart"},
	}
}

type edgeKey struct {
	From string
	To   string
	Kind EdgeKind
}

func edgeKeys(edges []Edge) map[edgeKey]struct{} {
	out := make(map[edgeKey]struct{}, len(edges))
	for _, edge := range edges {
		out[edgeKey{From: edge.From, To: edge.To, Kind: edge.Kind}] = struct{}{}
	}
	return out
}

func nodeIndex(nodes []Node) map[string]Node {
	out := make(map[string]Node, len(nodes))
	for _, node := range nodes {
		out[node.ID] = node
	}
	return out
}

func assertNodeIDs(t *testing.T, view ChainView, wanted ...string) {
	t.Helper()
	nodes := nodeIndex(view.Nodes)
	if len(nodes) != len(wanted) {
		t.Fatalf("nodes = %v, want %v", sortedNodeIDs(view.Nodes), wanted)
	}
	for _, id := range wanted {
		if _, ok := nodes[id]; !ok {
			t.Fatalf("node %q missing from %v", id, sortedNodeIDs(view.Nodes))
		}
	}
}

func assertNoDanglingEdges(t *testing.T, view ChainView) {
	t.Helper()
	nodes := nodeIndex(view.Nodes)
	for _, edge := range view.Edges {
		if _, ok := nodes[edge.From]; !ok {
			t.Fatalf("edge %+v starts at a filtered-out node", edge)
		}
		if _, ok := nodes[edge.To]; !ok {
			t.Fatalf("edge %+v ends at a filtered-out node", edge)
		}
	}
}

func TestBuildChainCompleteGraph(t *testing.T) {
	now := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
	view := BuildChain(completeSource(), ChainFilter{}, Budget{}, 77, now)

	if view.SnapshotCursor != 77 {
		t.Errorf("SnapshotCursor = %d, want 77", view.SnapshotCursor)
	}
	if view.VersionID != "" {
		t.Errorf("VersionID = %q, want empty until the service fills it", view.VersionID)
	}
	if !view.GeneratedAt.Equal(now) {
		t.Errorf("GeneratedAt = %v, want %v", view.GeneratedAt, now)
	}
	if view.Truncated {
		t.Errorf("Truncated = true, want false for a small graph")
	}
	if view.Truncation.Reason != "" {
		t.Errorf("Truncation.Reason = %q, want empty", view.Truncation.Reason)
	}
	if view.Truncation.MaxNodes != DefaultMaxNodes ||
		view.Truncation.MaxEdges != DefaultMaxEdges ||
		view.Truncation.MaxRequirements != DefaultMaxRequirements {
		t.Errorf("Truncation = %+v, want the default maxima", view.Truncation)
	}

	assertNodeIDs(t, view,
		"requirement:1",
		"scenario:10",
		"case_version:100",
		"wave:7",
		"case_run:200",
		"issue:300",
		"issue:301",
		"report:900",
		"closure:400",
	)

	wantEdges := []Edge{
		{From: "requirement:1", To: "scenario:10", Kind: EdgeHasScenario},
		{From: "scenario:10", To: "case_version:100", Kind: EdgeHasCase},
		{From: "case_version:100", To: "case_run:200", Kind: EdgeExecutedBy},
		{From: "wave:7", To: "case_run:200", Kind: EdgeContains},
		{From: "case_run:200", To: "issue:300", Kind: EdgeRaised},
		{From: "wave:7", To: "report:900", Kind: EdgeReports},
		{From: "requirement:1", To: "closure:400", Kind: EdgeClosedBy},
		{From: "issue:300", To: "report:900", Kind: EdgeIncludedIn},
		{From: "issue:300", To: "issue:301", Kind: EdgeMergedInto},
	}
	gotEdges := edgeKeys(view.Edges)
	if len(gotEdges) != len(wantEdges) {
		t.Fatalf("edges = %v, want %d edges", view.Edges, len(wantEdges))
	}
	for _, edge := range wantEdges {
		if _, ok := gotEdges[edgeKey{From: edge.From, To: edge.To, Kind: edge.Kind}]; !ok {
			t.Errorf("edge %+v missing from %v", edge, view.Edges)
		}
	}

	wantCounts := map[string]int{
		"requirements": 1,
		"scenarios":    1,
		"cases":        1,
		"waves":        1,
		"case_runs":    1,
		"issues":       2,
		"reports":      1,
		"evidence":     2,
	}
	for key, want := range wantCounts {
		if got := view.Counts[key]; got != want {
			t.Errorf("Counts[%q] = %d, want %d (%v)", key, got, want, view.Counts)
		}
	}

	nodes := nodeIndex(view.Nodes)
	if got := nodes["case_version:100"].Result; got != "failed" {
		t.Errorf("case_version result = %q, want failed", got)
	}
	if got := nodes["case_run:200"].Meta["evidence_count"]; got != 2 {
		t.Errorf("case_run evidence_count = %v, want 2", got)
	}
	if got := nodes["issue:300"].MergedIntoID; got == nil || *got != 301 {
		t.Errorf("issue merged_into = %v, want 301", got)
	}
}

func filterSource() SourceGraph {
	src := completeSource()

	src.Requirements[0].Meta = map[string]any{
		"target_version_sut_ids": []int64{5, 6},
		"page_ids":               []int64{50, 51},
	}
	src.Scenarios[0].Meta = map[string]any{
		"version_sut_ids": []int64{5, 6},
		"page_ids":        []int64{50, 51},
	}
	src.Cases[0].Meta = map[string]any{
		"version_sut_ids": []int64{5, 6},
		"page_ids":        []int64{50, 51},
		"wave_ids":        []int64{7},
		"revision":        2,
	}
	src.CaseRuns[0].Meta = map[string]any{
		"version_sut_ids": []int64{5, 6},
		"page_ids":        []int64{51},
		"wave_ids":        []int64{7},
		"evidence_count":  2,
	}
	src.Issues[0].Meta = map[string]any{
		"version_sut_ids": []int64{5, 6},
		"page_ids":        []int64{51},
		"wave_ids":        []int64{7},
		"severity":        "major",
	}
	src.Reports[0].Meta = map[string]any{
		"version_sut_ids": []int64{5, 6},
		"page_ids":        []int64{51},
		"wave_ids":        []int64{7},
		"kind":            "wave",
	}
	src.Closures[0].Meta = map[string]any{
		"version_sut_ids": []int64{5, 6},
		"page_ids":        []int64{51},
	}
	src.Closures[0].VersionSUTIDs = []int64{5, 6}

	secondCase := testNode(NodeCaseVersion, 101, "Second case")
	secondCase.RequirementID = int64Ptr(1)
	secondCase.ScenarioID = int64Ptr(10)
	secondCase.VersionSUTID = int64Ptr(7)
	secondCase.VersionSUTIDs = []int64{7}
	secondCase.PageID = int64Ptr(52)
	secondCase.Status = "draft"
	secondCase.Result = "passed"
	secondCase.Meta = map[string]any{
		"version_sut_ids": []int64{7},
		"page_ids":        []int64{52},
		"wave_ids":        []int64{8},
	}

	secondWave := testNode(NodeWave, 8, "Wave 2")
	secondWave.Status = "running"

	secondRun := testNode(NodeCaseRun, 201, "Second case")
	secondRun.CaseVersionID = int64Ptr(101)
	secondRun.WaveID = int64Ptr(8)
	secondRun.VersionSUTID = int64Ptr(7)
	secondRun.VersionSUTIDs = []int64{7}
	secondRun.PageID = int64Ptr(52)
	secondRun.Status = "done"
	secondRun.Result = "passed"
	secondRun.Meta = map[string]any{
		"version_sut_ids": []int64{7},
		"page_ids":        []int64{52},
		"wave_ids":        []int64{8},
	}

	secondReport := testNode(NodeReport, 901, "Wave 2 report")
	secondReport.WaveID = int64Ptr(8)
	secondReport.Status = "draft"
	secondReport.PageID = int64Ptr(52)

	src.Cases = append(src.Cases, secondCase)
	src.Waves = append(src.Waves, secondWave)
	src.CaseRuns = append(src.CaseRuns, secondRun)
	src.Reports = append(src.Reports, secondReport)
	return src
}

func TestBuildChainFilters(t *testing.T) {
	now := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		filter  ChainFilter
		present []string
		absent  []string
		edges   []edgeKey
	}{
		{
			name:    "SUT filter keeps both SUT tags of a cross-SUT case",
			filter:  ChainFilter{VersionSUTID: int64Ptr(6)},
			present: []string{"requirement:1", "scenario:10", "case_version:100", "case_run:200", "issue:300", "report:900", "closure:400"},
			absent:  []string{"case_version:101", "case_run:201", "wave:7", "wave:8", "report:901", "issue:301"},
			edges: []edgeKey{
				{From: "requirement:1", To: "scenario:10", Kind: EdgeHasScenario},
				{From: "scenario:10", To: "case_version:100", Kind: EdgeHasCase},
				{From: "case_version:100", To: "case_run:200", Kind: EdgeExecutedBy},
				{From: "case_run:200", To: "issue:300", Kind: EdgeRaised},
				{From: "issue:300", To: "report:900", Kind: EdgeIncludedIn},
				{From: "requirement:1", To: "closure:400", Kind: EdgeClosedBy},
			},
		},
		{
			name:    "page filter uses every page of a node",
			filter:  ChainFilter{PageID: int64Ptr(51)},
			present: []string{"requirement:1", "scenario:10", "case_version:100", "case_run:200", "issue:300", "report:900", "closure:400"},
			absent:  []string{"case_version:101", "case_run:201", "report:901"},
		},
		{
			name:    "wave filter keeps the wave scope",
			filter:  ChainFilter{WaveID: int64Ptr(7)},
			present: []string{"wave:7", "case_version:100", "case_run:200", "issue:300", "report:900"},
			absent:  []string{"wave:8", "case_version:101", "case_run:201", "report:901", "requirement:1", "scenario:10", "closure:400"},
			edges: []edgeKey{
				{From: "wave:7", To: "case_run:200", Kind: EdgeContains},
				{From: "case_version:100", To: "case_run:200", Kind: EdgeExecutedBy},
				{From: "case_run:200", To: "issue:300", Kind: EdgeRaised},
				{From: "wave:7", To: "report:900", Kind: EdgeReports},
				{From: "issue:300", To: "report:900", Kind: EdgeIncludedIn},
			},
		},
		{
			name:    "status filter keeps matching nodes only",
			filter:  ChainFilter{Status: "Confirmed"},
			present: []string{"case_version:100"},
			absent:  []string{"case_run:200", "requirement:1", "scenario:10", "issue:300", "report:900"},
		},
		{
			name:    "result filter keeps matching result nodes and their induced edge",
			filter:  ChainFilter{Result: "FAILED"},
			present: []string{"case_version:100", "case_run:200"},
			absent:  []string{"case_version:101", "case_run:201", "issue:300", "report:900"},
			edges: []edgeKey{
				{From: "case_version:100", To: "case_run:200", Kind: EdgeExecutedBy},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			view := BuildChain(filterSource(), tc.filter, Budget{}, 0, now)
			nodes := nodeIndex(view.Nodes)
			for _, id := range tc.present {
				if _, ok := nodes[id]; !ok {
					t.Errorf("node %q missing from %v", id, sortedNodeIDs(view.Nodes))
				}
			}
			for _, id := range tc.absent {
				if _, ok := nodes[id]; ok {
					t.Errorf("node %q unexpectedly kept in %v", id, sortedNodeIDs(view.Nodes))
				}
			}
			assertNoDanglingEdges(t, view)
			if len(tc.edges) > 0 {
				if len(view.Edges) != len(tc.edges) {
					t.Errorf("edges = %v, want %d edges", view.Edges, len(tc.edges))
				}
				got := edgeKeys(view.Edges)
				for _, want := range tc.edges {
					if _, ok := got[want]; !ok {
						t.Errorf("edge %+v missing from %v", want, view.Edges)
					}
				}
			}
		})
	}

	t.Run("counts describe the filtered graph", func(t *testing.T) {
		view := BuildChain(filterSource(), ChainFilter{Status: "confirmed"}, Budget{}, 0, now)
		if got := view.Counts["cases"]; got != 1 {
			t.Errorf("Counts[cases] = %d, want 1", got)
		}
		if got := view.Counts["requirements"]; got != 0 {
			t.Errorf("Counts[requirements] = %d, want 0", got)
		}
		if got := view.Counts["evidence"]; got != 0 {
			t.Errorf("Counts[evidence] = %d, want 0 when the run is filtered out", got)
		}
	})
}

func TestBuildChainFocusNode(t *testing.T) {
	now := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)

	t.Run("focus keeps a filtered node and adds its ancestors", func(t *testing.T) {
		view := BuildChain(filterSource(), ChainFilter{
			Status:    "open",
			FocusNode: "case_version:100",
		}, Budget{}, 0, now)

		assertNodeIDs(t, view,
			"requirement:1",
			"scenario:10",
			"case_version:100",
			"issue:300",
		)
		got := edgeKeys(view.Edges)
		for _, want := range []edgeKey{
			{From: "requirement:1", To: "scenario:10", Kind: EdgeHasScenario},
			{From: "scenario:10", To: "case_version:100", Kind: EdgeHasCase},
		} {
			if _, ok := got[want]; !ok {
				t.Errorf("focus ancestor edge %+v missing from %v", want, view.Edges)
			}
		}
	})

	t.Run("focus adds the shortest path through report refs", func(t *testing.T) {
		view := BuildChain(filterSource(), ChainFilter{
			Status:    "open",
			FocusNode: "report:900",
		}, Budget{}, 0, now)

		assertNodeIDs(t, view,
			"requirement:1",
			"scenario:10",
			"case_version:100",
			"wave:7",
			"case_run:200",
			"issue:300",
			"report:900",
		)
		got := edgeKeys(view.Edges)
		for _, want := range []edgeKey{
			{From: "requirement:1", To: "scenario:10", Kind: EdgeHasScenario},
			{From: "scenario:10", To: "case_version:100", Kind: EdgeHasCase},
			{From: "case_version:100", To: "case_run:200", Kind: EdgeExecutedBy},
			{From: "wave:7", To: "case_run:200", Kind: EdgeContains},
			{From: "case_run:200", To: "issue:300", Kind: EdgeRaised},
			{From: "issue:300", To: "report:900", Kind: EdgeIncludedIn},
			{From: "wave:7", To: "report:900", Kind: EdgeReports},
		} {
			if _, ok := got[want]; !ok {
				t.Errorf("focus ancestor edge %+v missing from %v", want, view.Edges)
			}
		}
	})

	t.Run("unknown focus node is ignored", func(t *testing.T) {
		view := BuildChain(filterSource(), ChainFilter{
			Status:    "open",
			FocusNode: "issue:999999",
		}, Budget{}, 0, now)
		assertNodeIDs(t, view, "issue:300")
	})

	t.Run("focus and its ancestors survive a tight node budget", func(t *testing.T) {
		view := BuildChain(filterSource(), ChainFilter{
			Status:    "open",
			FocusNode: "case_version:100",
		}, Budget{MaxNodes: 2}, 0, now)

		nodes := nodeIndex(view.Nodes)
		for _, id := range []string{"requirement:1", "scenario:10", "case_version:100"} {
			if _, ok := nodes[id]; !ok {
				t.Fatalf("protected node %q was truncated: %v", id, sortedNodeIDs(view.Nodes))
			}
		}
		if _, ok := nodes["issue:300"]; ok {
			t.Errorf("unprotected issue was not truncated: %v", sortedNodeIDs(view.Nodes))
		}
		if !view.Truncated || view.Truncation.Reason != TruncationReasonNodeBudget {
			t.Errorf("truncation = %+v, want node_budget", view.Truncation)
		}
	})
}

func requirementBudgetSource() SourceGraph {
	start := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	src := SourceGraph{
		SUTs:  map[int64]string{},
		Pages: map[int64]string{},
	}
	for index := int64(1); index <= 3; index++ {
		requirement := testNode(NodeRequirement, index, "Requirement "+itoa(index))
		requirement.Status = "clarified"
		requirement.CreatedAt = start
		requirement.UpdatedAt = start.Add(time.Duration(index) * time.Hour)

		scenario := testNode(NodeScenario, 10+index, "Scenario "+itoa(index))
		scenario.RequirementID = int64Ptr(index)

		caseVersion := testNode(NodeCaseVersion, 100+index, "Case "+itoa(index))
		caseVersion.RequirementID = int64Ptr(index)
		caseVersion.ScenarioID = int64Ptr(10 + index)
		caseVersion.Status = "confirmed"

		closure := testNode(NodeClosure, 400+index, "Closed "+itoa(index))
		closure.RequirementID = int64Ptr(index)

		src.Requirements = append(src.Requirements, requirement)
		src.Scenarios = append(src.Scenarios, scenario)
		src.Cases = append(src.Cases, caseVersion)
		src.Closures = append(src.Closures, closure)
	}
	return src
}

func TestBuildChainRequirementBudget(t *testing.T) {
	now := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)

	t.Run("keeps the newest requirements by updated_at then id", func(t *testing.T) {
		view := BuildChain(requirementBudgetSource(), ChainFilter{}, Budget{MaxRequirements: 2}, 0, now)

		assertNodeIDs(t, view,
			"requirement:2", "scenario:12", "case_version:102", "closure:402",
			"requirement:3", "scenario:13", "case_version:103", "closure:403",
		)
		if !view.Truncated || view.Truncation.Reason != TruncationReasonRequirementBudget {
			t.Errorf("truncation = %+v, want requirement_budget", view.Truncation)
		}
		if got := view.Truncation.MaxRequirements; got != 2 {
			t.Errorf("Truncation.MaxRequirements = %d, want 2", got)
		}
		if got := view.Counts["requirements"]; got != 3 {
			t.Errorf("Counts[requirements] = %d, want the pre-truncation count 3", got)
		}
		if got := view.Counts["scenarios"]; got != 3 {
			t.Errorf("Counts[scenarios] = %d, want the pre-truncation count 3", got)
		}
	})

	t.Run("focus ancestry overrides the requirement budget", func(t *testing.T) {
		view := BuildChain(requirementBudgetSource(), ChainFilter{
			FocusNode: "case_version:101",
		}, Budget{MaxRequirements: 2}, 0, now)

		nodes := nodeIndex(view.Nodes)
		for _, id := range []string{"requirement:1", "scenario:11", "case_version:101", "requirement:2", "requirement:3"} {
			if _, ok := nodes[id]; !ok {
				t.Fatalf("node %q missing after focus override: %v", id, sortedNodeIDs(view.Nodes))
			}
		}
		if !view.Truncated || view.Truncation.Reason != TruncationReasonRequirementBudget {
			t.Errorf("truncation = %+v, want requirement_budget", view.Truncation)
		}
	})
}

// filteredRootSource builds a chain where requirement 4 does not match the SUT
// filter while its scenario, case, run, issue and closure do. Requirements 1..3
// are budget candidates, so MaxRequirements: 2 truncates requirement 1 and its
// subtree. Requirement 4 was never a budget candidate: its subtree must survive
// even though its root is not part of the filtered graph.
func filteredRootSource() SourceGraph {
	src := SourceGraph{
		SUTs:  map[int64]string{},
		Pages: map[int64]string{},
	}
	start := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	for index := int64(1); index <= 3; index++ {
		requirement := testNode(NodeRequirement, index, "Requirement "+itoa(index))
		requirement.Status = "clarified"
		requirement.VersionSUTIDs = []int64{6}
		requirement.CreatedAt = start.Add(time.Duration(index) * time.Hour)

		scenario := testNode(NodeScenario, 10+index, "Scenario "+itoa(index))
		scenario.RequirementID = int64Ptr(index)
		scenario.VersionSUTIDs = []int64{6}

		caseVersion := testNode(NodeCaseVersion, 100+index, "Case "+itoa(index))
		caseVersion.RequirementID = int64Ptr(index)
		caseVersion.ScenarioID = int64Ptr(10 + index)
		caseVersion.VersionSUTIDs = []int64{6}

		src.Requirements = append(src.Requirements, requirement)
		src.Scenarios = append(src.Scenarios, scenario)
		src.Cases = append(src.Cases, caseVersion)
	}

	orphan := testNode(NodeRequirement, 4, "Requirement 4")
	orphan.Status = "clarified"
	scenario := testNode(NodeScenario, 14, "Scenario 4")
	scenario.RequirementID = int64Ptr(4)
	scenario.VersionSUTIDs = []int64{6}
	caseVersion := testNode(NodeCaseVersion, 104, "Case 4")
	caseVersion.RequirementID = int64Ptr(4)
	caseVersion.ScenarioID = int64Ptr(14)
	caseVersion.VersionSUTIDs = []int64{6}
	caseRun := testNode(NodeCaseRun, 204, "Run 4")
	caseRun.CaseVersionID = int64Ptr(104)
	caseRun.VersionSUTIDs = []int64{6}
	issue := testNode(NodeIssue, 304, "Issue 4")
	issue.CaseRunID = int64Ptr(204)
	issue.VersionSUTIDs = []int64{6}
	closure := testNode(NodeClosure, 404, "Closed 4")
	closure.RequirementID = int64Ptr(4)
	closure.VersionSUTIDs = []int64{6}

	src.Requirements = append(src.Requirements, orphan)
	src.Scenarios = append(src.Scenarios, scenario)
	src.Cases = append(src.Cases, caseVersion)
	src.CaseRuns = append(src.CaseRuns, caseRun)
	src.Issues = append(src.Issues, issue)
	src.Closures = append(src.Closures, closure)
	return src
}

func TestBuildChainRequirementBudgetKeepsFilteredRootDescendants(t *testing.T) {
	now := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)

	view := BuildChain(
		filteredRootSource(),
		ChainFilter{VersionSUTID: int64Ptr(6)},
		Budget{MaxRequirements: 2},
		0,
		now,
	)

	if !view.Truncated || view.Truncation.Reason != TruncationReasonRequirementBudget {
		t.Fatalf("truncation = %+v, want requirement_budget", view.Truncation)
	}
	// Requirements 2 and 3 are the budget candidates that stay, with their
	// subtrees; requirement 1's subtree is truncated with its root. Requirement
	// 4's whole subtree survives because the filter (not the budget) removed the
	// root, so the budget never considered it.
	assertNodeIDs(t, view,
		"requirement:2", "scenario:12", "case_version:102",
		"requirement:3", "scenario:13", "case_version:103",
		"scenario:14", "case_version:104", "case_run:204", "issue:304", "closure:404",
	)
	if _, ok := nodeIndex(view.Nodes)["requirement:1"]; ok {
		t.Errorf("requirement:1 survived the requirement budget: %v", sortedNodeIDs(view.Nodes))
	}
	assertNoDanglingEdges(t, view)
}

func TestBuildChainNodeBudget(t *testing.T) {
	now := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)

	t.Run("drops low-priority layers before requirements and closures", func(t *testing.T) {
		view := BuildChain(completeSource(), ChainFilter{}, Budget{MaxNodes: 3}, 0, now)

		assertNodeIDs(t, view, "requirement:1", "scenario:10", "closure:400")
		if !view.Truncated || view.Truncation.Reason != TruncationReasonNodeBudget {
			t.Errorf("truncation = %+v, want node_budget", view.Truncation)
		}
		if len(view.Edges) != 2 {
			t.Errorf("edges = %v, want the two induced requirement edges", view.Edges)
		}
		if got := view.Counts["issues"]; got != 2 {
			t.Errorf("Counts[issues] = %d, want the pre-truncation count 2", got)
		}
	})

	t.Run("drops scenarios after case versions", func(t *testing.T) {
		view := BuildChain(completeSource(), ChainFilter{}, Budget{MaxNodes: 2}, 0, now)
		assertNodeIDs(t, view, "requirement:1", "closure:400")
		if !view.Truncated || view.Truncation.Reason != TruncationReasonNodeBudget {
			t.Errorf("truncation = %+v, want node_budget", view.Truncation)
		}
		if len(view.Edges) != 1 || view.Edges[0].Kind != EdgeClosedBy {
			t.Errorf("edges = %v, want only closed_by", view.Edges)
		}
	})
}

func TestBuildChainNodeBudgetDropsRequirementUnits(t *testing.T) {
	now := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)

	// 12 filtered nodes: low-priority layers are dropped first, then the oldest
	// requirement unit (requirement + closure) is removed to reach MaxNodes.
	view := BuildChain(requirementBudgetSource(), ChainFilter{}, Budget{MaxNodes: 4}, 0, now)

	assertNodeIDs(t, view,
		"requirement:2", "requirement:3",
		"closure:402", "closure:403",
	)
	if !view.Truncated || view.Truncation.Reason != TruncationReasonNodeBudget {
		t.Errorf("truncation = %+v, want node_budget", view.Truncation)
	}
	if got := view.Truncation.MaxNodes; got != 4 {
		t.Errorf("Truncation.MaxNodes = %d, want 4", got)
	}
	if got := view.Counts["requirements"]; got != 3 {
		t.Errorf("Counts[requirements] = %d, want the pre-truncation count 3", got)
	}
	assertNoDanglingEdges(t, view)
}

func TestBuildChainEdgeBudget(t *testing.T) {
	now := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
	view := BuildChain(completeSource(), ChainFilter{}, Budget{MaxEdges: 5}, 0, now)

	if !view.Truncated || view.Truncation.Reason != TruncationReasonEdgeBudget {
		t.Fatalf("truncation = %+v, want edge_budget", view.Truncation)
	}
	if len(view.Edges) != 5 {
		t.Fatalf("edges = %v, want 5", view.Edges)
	}
	got := edgeKeys(view.Edges)
	for _, want := range []edgeKey{
		{From: "requirement:1", To: "scenario:10", Kind: EdgeHasScenario},
		{From: "scenario:10", To: "case_version:100", Kind: EdgeHasCase},
		{From: "case_version:100", To: "case_run:200", Kind: EdgeExecutedBy},
		{From: "wave:7", To: "case_run:200", Kind: EdgeContains},
		{From: "case_run:200", To: "issue:300", Kind: EdgeRaised},
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("edge %+v missing from %v", want, view.Edges)
		}
	}
}

func TestBuildChainEdgeBudgetProtectsFocusPath(t *testing.T) {
	now := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)

	// 13 induced edges; the five issue:300 ancestry edges bypass the edge budget
	// even though the second case's has_case/executed_by edges would otherwise
	// win by the plain priority order.
	view := BuildChain(filterSource(), ChainFilter{FocusNode: "issue:300"}, Budget{MaxEdges: 5}, 0, now)

	if !view.Truncated || view.Truncation.Reason != TruncationReasonEdgeBudget {
		t.Fatalf("truncation = %+v, want edge_budget", view.Truncation)
	}
	want := []edgeKey{
		{From: "requirement:1", To: "scenario:10", Kind: EdgeHasScenario},
		{From: "scenario:10", To: "case_version:100", Kind: EdgeHasCase},
		{From: "case_version:100", To: "case_run:200", Kind: EdgeExecutedBy},
		{From: "wave:7", To: "case_run:200", Kind: EdgeContains},
		{From: "case_run:200", To: "issue:300", Kind: EdgeRaised},
	}
	if len(view.Edges) != len(want) {
		t.Fatalf("edges = %v, want the five protected focus path edges", view.Edges)
	}
	got := edgeKeys(view.Edges)
	for _, edge := range want {
		if _, ok := got[edge]; !ok {
			t.Errorf("focus path edge %+v missing from %v", edge, view.Edges)
		}
	}
	unprotected := edgeKey{From: "scenario:10", To: "case_version:101", Kind: EdgeHasCase}
	if _, ok := got[unprotected]; ok {
		t.Errorf("unprotected second-branch edge %+v survived the edge budget: %v", unprotected, view.Edges)
	}
}

func TestBuildChainEmptyGraph(t *testing.T) {
	now := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
	view := BuildChain(SourceGraph{}, ChainFilter{}, Budget{}, 42, now)

	if view.VersionID != "" {
		t.Errorf("VersionID = %q, want empty", view.VersionID)
	}
	if view.SnapshotCursor != 42 {
		t.Errorf("SnapshotCursor = %d, want 42", view.SnapshotCursor)
	}
	if !view.GeneratedAt.Equal(now) {
		t.Errorf("GeneratedAt = %v, want %v", view.GeneratedAt, now)
	}
	if view.Nodes == nil || len(view.Nodes) != 0 {
		t.Errorf("Nodes = %v, want a non-nil empty slice", view.Nodes)
	}
	if view.Edges == nil || len(view.Edges) != 0 {
		t.Errorf("Edges = %v, want a non-nil empty slice", view.Edges)
	}
	for _, key := range []string{"requirements", "scenarios", "cases", "waves", "case_runs", "issues", "reports", "evidence"} {
		if got, ok := view.Counts[key]; !ok || got != 0 {
			t.Errorf("Counts[%q] = %d (present=%v), want 0", key, got, ok)
		}
	}
	if view.Truncated {
		t.Errorf("Truncated = true, want false")
	}
	if view.Truncation != (Truncation{
		MaxNodes:        DefaultMaxNodes,
		MaxEdges:        DefaultMaxEdges,
		MaxRequirements: DefaultMaxRequirements,
	}) {
		t.Errorf("Truncation = %+v, want the default maxima", view.Truncation)
	}
	if view.Filters != (ChainFilter{}) {
		t.Errorf("Filters = %+v, want the normalized zero filter", view.Filters)
	}
	if view.Replay.Mode != ReplayModeLive || !view.Replay.Approximate {
		t.Errorf("Replay = %+v, want live with approximate=true", view.Replay)
	}

	clamped := BuildChain(SourceGraph{}, ChainFilter{}, Budget{MaxNodes: -1, MaxEdges: 9000, MaxRequirements: 0}, 0, now)
	if clamped.Truncation.MaxNodes != DefaultMaxNodes ||
		clamped.Truncation.MaxEdges != DefaultMaxEdges ||
		clamped.Truncation.MaxRequirements != DefaultMaxRequirements {
		t.Errorf("Truncation = %+v, want clamped defaults", clamped.Truncation)
	}
}

func TestBuildChainMergedIssue(t *testing.T) {
	now := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)

	t.Run("keeps merged_into only when the target is in the graph", func(t *testing.T) {
		src := completeSource()
		src.Issues = src.Issues[:1]
		view := BuildChain(src, ChainFilter{}, Budget{}, 0, now)
		if _, ok := edgeKeys(view.Edges)[edgeKey{From: "issue:300", To: "issue:301", Kind: EdgeMergedInto}]; ok {
			t.Errorf("merged_into edge points at an issue outside the graph: %v", view.Edges)
		}
	})

	t.Run("keeps the merged target node and edge", func(t *testing.T) {
		view := BuildChain(completeSource(), ChainFilter{}, Budget{}, 0, now)
		if _, ok := edgeKeys(view.Edges)[edgeKey{From: "issue:300", To: "issue:301", Kind: EdgeMergedInto}]; !ok {
			t.Errorf("merged_into edge missing: %v", view.Edges)
		}
	})
}

func TestBuildChainCrossSUTLabels(t *testing.T) {
	now := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
	src := completeSource()
	view := BuildChain(src, ChainFilter{}, Budget{}, 0, now)
	nodes := nodeIndex(view.Nodes)

	caseNode, ok := nodes["case_version:100"]
	if !ok {
		t.Fatalf("case_version:100 missing: %v", sortedNodeIDs(view.Nodes))
	}
	if got := caseNode.Meta["sut_labels"]; !slices.Equal(anyStrings(t, got), []string{"Pay", "Order"}) {
		t.Errorf("case_version sut_labels = %v, want [Pay Order]", got)
	}
	if got := caseNode.Meta["page_labels"]; !slices.Equal(anyStrings(t, got), []string{"Checkout", "Cart"}) {
		t.Errorf("case_version page_labels = %v, want [Checkout Cart]", got)
	}
	if !slices.Equal(caseNode.VersionSUTIDs, []int64{5, 6}) {
		t.Errorf("case_version VersionSUTIDs = %v, want [5 6]", caseNode.VersionSUTIDs)
	}

	requirement := nodes["requirement:1"]
	if got := requirement.Meta["sut_labels"]; !slices.Equal(anyStrings(t, got), []string{"Pay", "Order"}) {
		t.Errorf("requirement sut_labels = %v, want [Pay Order]", got)
	}

	if _, leaked := src.Cases[0].Meta["sut_labels"]; leaked {
		t.Errorf("BuildChain mutated the caller's Meta map: %v", src.Cases[0].Meta)
	}
}

func anyStrings(t *testing.T, value any) []string {
	t.Helper()
	labels, ok := value.([]string)
	if !ok {
		return nil
	}
	return labels
}

func TestBuildChainReplay(t *testing.T) {
	now := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		filter ChainFilter
		want   ReplayInfo
	}{
		{
			name:   "live",
			filter: ChainFilter{},
			want:   ReplayInfo{Mode: ReplayModeLive, Approximate: true},
		},
		{
			name:   "wave",
			filter: ChainFilter{WaveID: int64Ptr(7)},
			want:   ReplayInfo{Mode: ReplayModeWave, WaveID: int64Ptr(7), Approximate: true},
		},
		{
			name: "range normalizes the event bounds and is approximate",
			filter: ChainFilter{
				EventFrom: int64Ptr(9),
				EventTo:   int64Ptr(5),
			},
			want: ReplayInfo{Mode: ReplayModeRange, EventFrom: int64Ptr(5), EventTo: int64Ptr(9), Approximate: true},
		},
		{
			name: "event range wins over a wave filter",
			filter: ChainFilter{
				WaveID:    int64Ptr(7),
				EventFrom: int64Ptr(5),
				EventTo:   int64Ptr(9),
			},
			want: ReplayInfo{Mode: ReplayModeRange, WaveID: int64Ptr(7), EventFrom: int64Ptr(5), EventTo: int64Ptr(9), Approximate: true},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			view := BuildChain(SourceGraph{}, tc.filter, Budget{}, 0, now)
			if view.Replay.Mode != tc.want.Mode {
				t.Errorf("Replay.Mode = %q, want %q", view.Replay.Mode, tc.want.Mode)
			}
			if view.Replay.Approximate != tc.want.Approximate {
				t.Errorf("Replay.Approximate = %v, want %v", view.Replay.Approximate, tc.want.Approximate)
			}
			if !equalInt64Ptr(view.Replay.WaveID, tc.want.WaveID) {
				t.Errorf("Replay.WaveID = %v, want %v", view.Replay.WaveID, tc.want.WaveID)
			}
			if !equalInt64Ptr(view.Replay.EventFrom, tc.want.EventFrom) {
				t.Errorf("Replay.EventFrom = %v, want %v", view.Replay.EventFrom, tc.want.EventFrom)
			}
			if !equalInt64Ptr(view.Replay.EventTo, tc.want.EventTo) {
				t.Errorf("Replay.EventTo = %v, want %v", view.Replay.EventTo, tc.want.EventTo)
			}
			if !equalFilters(view.Filters, tc.filter.Normalize()) {
				t.Errorf("Filters = %+v, want the normalized filter %+v", view.Filters, tc.filter.Normalize())
			}
		})
	}
}

func equalInt64Ptr(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func equalFilters(left, right ChainFilter) bool {
	return equalInt64Ptr(left.VersionSUTID, right.VersionSUTID) &&
		equalInt64Ptr(left.PageID, right.PageID) &&
		equalInt64Ptr(left.WaveID, right.WaveID) &&
		left.Status == right.Status &&
		left.Result == right.Result &&
		left.FocusNode == right.FocusNode &&
		equalInt64Ptr(left.EventFrom, right.EventFrom) &&
		equalInt64Ptr(left.EventTo, right.EventTo) &&
		left.Limit == right.Limit
}

func TestBuildChainLimitLowerOnly(t *testing.T) {
	now := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		filter ChainFilter
		budget Budget
		want   int
	}{
		{
			name:   "limit cannot raise the configured budget",
			filter: ChainFilter{Limit: 1000},
			budget: Budget{MaxNodes: 300},
			want:   300,
		},
		{
			name:   "limit below the configured budget lowers it",
			filter: ChainFilter{Limit: 100},
			budget: Budget{MaxNodes: 300},
			want:   100,
		},
		{
			name:   "limit above a lower configured budget stays at the budget",
			filter: ChainFilter{Limit: 50},
			budget: Budget{MaxNodes: 5},
			want:   5,
		},
		{
			name:   "unset limit keeps the budget",
			filter: ChainFilter{},
			budget: Budget{MaxNodes: 10},
			want:   10,
		},
		{
			name:   "invalid limit falls back to the budget",
			filter: ChainFilter{Limit: 5000},
			budget: Budget{MaxNodes: 10},
			want:   10,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			view := BuildChain(SourceGraph{}, tc.filter, tc.budget, 0, now)
			if got := view.Truncation.MaxNodes; got != tc.want {
				t.Errorf("Truncation.MaxNodes = %d, want %d", got, tc.want)
			}
		})
	}
}

func sortedNodeIDs(nodes []Node) []string {
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.ID)
	}
	slices.Sort(ids)
	return ids
}
