package httpapi

import (
	"context"
	"testing"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// historicalCaseReader serves the executable read as empty and resolves one
// superseded case version through the historical label read.
type historicalCaseReader struct {
	label executionapp.CaseVersionLabel
	err   error
}

func (historicalCaseReader) GetCaseVersion(context.Context, int64, int64) (execution.CaseSnapshot, error) {
	return execution.CaseSnapshot{}, execution.ErrNotFound
}

func (historicalCaseReader) ListExecutable(context.Context, int64) ([]execution.CaseSnapshot, error) {
	return nil, nil
}

func (historicalCaseReader) ResolveReplacement(context.Context, int64, int64) (execution.CaseSnapshot, bool, error) {
	return execution.CaseSnapshot{}, false, nil
}

func (r historicalCaseReader) CaseVersionLabel(context.Context, int64, int64) (executionapp.CaseVersionLabel, error) {
	if r.err != nil {
		return executionapp.CaseVersionLabel{}, r.err
	}
	return r.label, nil
}

// TestCaseVersionProjectionResolvesSupersededRun pins the PO E2E F11 fix: a
// case_run whose confirmed version was superseded no longer renders with an
// empty case_id/title; the historical label read supplies the logical case and
// title the row lost when the version left the executable set.
func TestCaseVersionProjectionResolvesSupersededRun(t *testing.T) {
	handler := New(Deps{Cases: historicalCaseReader{label: executionapp.CaseVersionLabel{
		CaseID: 9001, CaseVersionID: 44, Title: "被取代的登录用例", Revision: 2, Status: "superseded",
	}}})
	snapshot, meta := handler.caseVersionProjection(context.Background(), 2001, 44)
	if snapshot != nil {
		t.Fatalf("snapshot = %+v, want nil for a superseded version", snapshot)
	}
	if meta.CaseID != "9001" || meta.Title != "被取代的登录用例" {
		t.Fatalf("scene = %+v, want the historical case id/title", meta)
	}
}

// TestCaseVersionProjectionKeepsExecutableReadFirst pins that the fallback never
// overrides the current+confirmed snapshot used for execution.
func TestCaseVersionProjectionKeepsExecutableReadFirst(t *testing.T) {
	handler := New(Deps{Cases: executableCaseReader{snapshot: execution.CaseSnapshot{
		CaseID: 9002, CaseVersionID: 45, Title: "当前用例",
	}}})
	snapshot, meta := handler.caseVersionProjection(context.Background(), 2001, 45)
	if snapshot == nil || snapshot.CaseVersionID != 45 {
		t.Fatalf("snapshot = %+v, want the executable read", snapshot)
	}
	if meta.CaseID != "9002" || meta.Title != "当前用例" {
		t.Fatalf("meta = %+v", meta)
	}
}

type executableCaseReader struct {
	snapshot execution.CaseSnapshot
}

func (r executableCaseReader) GetCaseVersion(context.Context, int64, int64) (execution.CaseSnapshot, error) {
	return r.snapshot, nil
}

func (executableCaseReader) ListExecutable(context.Context, int64) ([]execution.CaseSnapshot, error) {
	return nil, nil
}

func (executableCaseReader) ResolveReplacement(context.Context, int64, int64) (execution.CaseSnapshot, bool, error) {
	return execution.CaseSnapshot{}, false, nil
}

func (executableCaseReader) CaseVersionLabel(context.Context, int64, int64) (executionapp.CaseVersionLabel, error) {
	return executionapp.CaseVersionLabel{}, execution.ErrNotFound
}

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

// TestCaseRunSummaryViewCarriesIncompleteCause pins the QA Q2 UI contract: an
// incomplete case run exposes the concrete terminal cause, the human-readable
// explanation and the stable next action so the execution UI can render an
// actionable row instead of a bare "incomplete".
func TestCaseRunSummaryViewCarriesIncompleteCause(t *testing.T) {
	view := toCaseRunView(execution.CaseRun{
		ID: 3001, WaveID: 2001, IntentID: 1001, CaseVersionID: 41,
		Status: execution.CaseRunInconclusive,
		Summary: execution.CaseRunSummary{
			VerdictReason: "incomplete",
			TerminalCause: execution.TerminalCauseTokenBudget,
			LastError:     "token budget: used 222159 of 200000 tokens after 15 turns; the case recorded 1 of 4 steps before the run ended",
			NextAction:    execution.NextActionRerun,
		},
	}, caseMeta{}, nil, 0)
	if view.Summary.TerminalCause != execution.TerminalCauseTokenBudget {
		t.Fatalf("terminal cause = %q", view.Summary.TerminalCause)
	}
	if view.Summary.LastError == "" {
		t.Fatal("last error was dropped from the case-run summary view")
	}
	if view.Summary.NextAction != execution.NextActionRerun {
		t.Fatalf("next action = %q", view.Summary.NextAction)
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
