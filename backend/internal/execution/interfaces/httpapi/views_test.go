package httpapi

import (
	"testing"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// TestEvidenceKindsIncludeVisualKinds pins the EvidenceView.kind API enum
// (spec §4.2): the eight P3 kinds stay valid and the two P5 visual evidence
// kinds are added for GET /evidence filtering and evidence views.
func TestEvidenceKindsIncludeVisualKinds(t *testing.T) {
	kinds := evidenceKinds()
	if len(kinds) != 10 {
		t.Fatalf("evidence kinds = %d, want 10", len(kinds))
	}
	seen := make(map[string]bool, len(kinds))
	for _, kind := range kinds {
		if seen[kind] {
			t.Fatalf("duplicate evidence kind %q", kind)
		}
		seen[kind] = true
	}
	for _, want := range []string{
		"screenshot", "snapshot", "console", "network", "http", "db", "download", "text",
		"visual_actual", "visual_diff",
	} {
		if !seen[want] {
			t.Fatalf("evidence kind %q is missing from the API enum", want)
		}
	}
}

// TestCaseRunSummaryViewCarriesVisualCounts pins the P5 summary fields
// (spec §4.2): visual_passed/failed/inconclusive are exposed and visual_skipped
// stays in the payload for compatibility but is 0 on the P5 path.
func TestCaseRunSummaryViewCarriesVisualCounts(t *testing.T) {
	view := toCaseRunView(execution.CaseRun{
		ID: 3001, WaveID: 2001, IntentID: 1001, CaseVersionID: 41,
		Status: execution.CaseRunInconclusive,
		Summary: execution.CaseRunSummary{
			VerdictReason: "missing_visual_baseline", VisualPassed: 2,
			VisualFailed: 1, VisualInconclusive: 3, VisualSkipped: 0,
		},
	}, caseMeta{}, nil, 0)
	if view.Summary.VisualPassed != 2 || view.Summary.VisualFailed != 1 ||
		view.Summary.VisualInconclusive != 3 || view.Summary.VisualSkipped != 0 {
		t.Fatalf("summary view = %+v", view.Summary)
	}
}
