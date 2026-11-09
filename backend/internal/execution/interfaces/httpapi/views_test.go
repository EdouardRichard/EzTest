package httpapi

import (
	"context"
	"encoding/json"
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

// TestCaseRunSummaryViewExplainsEveryTerminalRun reproduces QA Q2's Medium
// residual: the persisted rows carried last_error/verdict_reason but their
// terminal_cause/next_action were empty, so the execution page could not explain
// a blocked/failed/inconclusive run. The projection must derive one stable cause
// and one human next action from the persisted terminal data, while an explicit
// settlement cause (budget/agent stop) always wins and a clean verdict stays
// silent.
func TestCaseRunSummaryViewExplainsEveryTerminalRun(t *testing.T) {
	cases := []struct {
		name      string
		run       execution.CaseRun
		wantCause string
		wantNext  string
	}{
		{
			name: "blocked missing account",
			run: execution.CaseRun{
				Status: execution.CaseRunBlocked, FailureClass: execution.FailureClassBlocked,
				Summary: execution.CaseRunSummary{
					VerdictReason: "missing_account",
					LastError:     "the admin account is not configured",
				},
			},
			wantCause: "missing_account",
			wantNext:  execution.NextActionResolveBlocker,
		},
		{
			name: "blocked application evidence",
			run: execution.CaseRun{
				Status: execution.CaseRunBlocked, FailureClass: execution.FailureClassApplication,
				Summary: execution.CaseRunSummary{
					VerdictReason: execution.VerdictReasonAssertionFailed,
					LastError:     "the login failure message contradicts the requirement",
				},
			},
			wantCause: execution.VerdictReasonAssertionFailed,
			wantNext:  execution.NextActionReviewFailure,
		},
		{
			name: "incomplete without settlement cause",
			run: execution.CaseRun{
				Status:  execution.CaseRunInconclusive,
				Summary: execution.CaseRunSummary{VerdictReason: execution.VerdictReasonIncomplete},
			},
			wantCause: execution.VerdictReasonIncomplete,
			wantNext:  execution.NextActionRerun,
		},
		{
			name: "visual assertion not run",
			run: execution.CaseRun{
				Status:  execution.CaseRunInconclusive,
				Summary: execution.CaseRunSummary{VerdictReason: execution.VerdictReasonVisualAssertionNotRun},
			},
			wantCause: execution.VerdictReasonVisualAssertionNotRun,
			wantNext:  execution.NextActionRepairVisual,
		},
		{
			name: "failed environment",
			run: execution.CaseRun{
				Status: execution.CaseRunFailed, FailureClass: execution.FailureClassEnvironment,
				Summary: execution.CaseRunSummary{VerdictReason: execution.VerdictReasonAgentFailure},
			},
			wantCause: string(execution.FailureClassEnvironment),
			wantNext:  execution.NextActionRerun,
		},
		{
			name: "superseded",
			run: execution.CaseRun{
				Status:  execution.CaseRunSuperseded,
				Summary: execution.CaseRunSummary{VerdictReason: "case_version_superseded"},
			},
			wantCause: "case_version_superseded",
			wantNext:  execution.NextActionReconfirm,
		},
		{
			name: "explicit settlement cause wins",
			run: execution.CaseRun{
				Status:  execution.CaseRunInconclusive,
				Summary: execution.CaseRunSummary{VerdictReason: execution.VerdictReasonIncomplete, TerminalCause: execution.TerminalCauseTokenBudget},
			},
			wantCause: execution.TerminalCauseTokenBudget,
			wantNext:  execution.NextActionRerun,
		},
		{
			name: "passed verdict stays silent",
			run: execution.CaseRun{
				Status:  execution.CaseRunPassed,
				Summary: execution.CaseRunSummary{VerdictReason: execution.VerdictReasonPassed},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.run.ID, tc.run.WaveID, tc.run.IntentID, tc.run.CaseVersionID = 3001, 2001, 1001, 41
			view := toCaseRunView(tc.run, caseMeta{}, nil, 0)
			if view.Summary.TerminalCause != tc.wantCause {
				t.Fatalf("terminal cause = %q, want %q (last_error %q)",
					view.Summary.TerminalCause, tc.wantCause, view.Summary.LastError)
			}
			if view.Summary.NextAction != tc.wantNext {
				t.Fatalf("next action = %q, want %q", view.Summary.NextAction, tc.wantNext)
			}
		})
	}
}

// TestCaseRunSummaryViewCarriesUsage pins the QA Q2 usage gap: the observed
// agent-run usage persisted on the terminal summary is exposed by the case-run
// projection so the execution page does not depend on Agent Center for it.
func TestCaseRunSummaryViewCarriesUsage(t *testing.T) {
	usage := execution.CaseRunUsage{Turns: 15, TotalTokens: 222159, PromptTokens: 200000, CompletionTokens: 22159}
	view := toCaseRunView(execution.CaseRun{
		ID: 3001, WaveID: 2001, IntentID: 1001, CaseVersionID: 41,
		Status:  execution.CaseRunInconclusive,
		Summary: execution.CaseRunSummary{VerdictReason: "incomplete", Usage: &usage},
	}, caseMeta{}, nil, 0)
	if view.Summary.Usage == nil {
		t.Fatal("summary usage is nil, want the persisted usage")
	}
	if view.Summary.Usage.Turns != 15 || view.Summary.Usage.TotalTokens != 222159 ||
		view.Summary.Usage.PromptTokens != 200000 || view.Summary.Usage.CompletionTokens != 22159 {
		t.Fatalf("summary usage view = %+v", view.Summary.Usage)
	}

	without := toCaseRunView(execution.CaseRun{
		ID: 3002, WaveID: 2001, IntentID: 1001, CaseVersionID: 41,
		Status:  execution.CaseRunPassed,
		Summary: execution.CaseRunSummary{VerdictReason: "passed"},
	}, caseMeta{}, nil, 0)
	if without.Summary.Usage != nil {
		t.Fatalf("usage view = %+v, want nil when the row has no persisted usage", without.Summary.Usage)
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

// v0.2.3 X-07: the case_run view exposes the dispatch snapshot and a stable
// terminal cause, and never surfaces a raw failure enum as the human reason
// when a mapped cause exists.
func TestCaseRunSummaryViewExposesDispatchAndStableCause(t *testing.T) {
	view := toCaseRunView(execution.CaseRun{
		ID: 3001, WaveID: 2001, IntentID: 1001, CaseVersionID: 41,
		Status: execution.CaseRunFailed, FailureClass: execution.FailureClassExecutorLost,
		Summary: execution.CaseRunSummary{
			VerdictReason: execution.VerdictReasonAgentFailure,
			LastError:     "executor_lost",
			RetryReason:   execution.TerminalCauseExecutorTimeout,
			Dispatch: &execution.CaseRunDispatchDiagnostic{
				OwnerExecutorID: 7,
				Endpoint:        "http://127.0.0.1:13336",
				ExecutorStatus:  "active",
				RegistryHit:     true,
				DispatchAt:      "2026-11-01T01:00:00Z",
				Attempt:         2,
			},
		},
	}, caseMeta{}, nil, 0)
	payload := viewJSON(t, view)
	summary := payload["summary"].(map[string]any)
	if summary["terminal_cause"] != string(execution.FailureClassExecutorLost) {
		t.Fatalf("terminal_cause = %v, want executor_lost", summary["terminal_cause"])
	}
	if summary["last_error"] != "" {
		t.Fatalf("raw failure enum leaked as last_error: %v", summary["last_error"])
	}
	dispatch, ok := summary["dispatch"].(map[string]any)
	if !ok {
		t.Fatalf("summary.dispatch missing: %+v", summary)
	}
	if dispatch["owner_executor_id"] != "7" || dispatch["endpoint"] != "http://127.0.0.1:13336" {
		t.Fatalf("dispatch = %+v", dispatch)
	}
	if dispatch["registry_hit"] != true || dispatch["attempt"] != float64(2) {
		t.Fatalf("dispatch route/attempt = %+v", dispatch)
	}
	if summary["retry_reason"] != execution.TerminalCauseExecutorTimeout {
		t.Fatalf("retry_reason = %v, want executor_timeout", summary["retry_reason"])
	}

	// A non-terminal running row must not expose a terminal verdict reason even
	// when an older deployment persisted one (X-07 running-row fallback).
	running := toCaseRunView(execution.CaseRun{
		ID: 3002, WaveID: 2001, IntentID: 1001, CaseVersionID: 41,
		Status: execution.CaseRunRunning, FailureClass: execution.FailureClassExecutorLost,
		Summary: execution.CaseRunSummary{
			VerdictReason: execution.VerdictReasonAgentFailure,
			LastError:     "executor_lost",
		},
	}, caseMeta{}, nil, 0)
	runningPayload := viewJSON(t, running)
	runningSummary := runningPayload["summary"].(map[string]any)
	if runningSummary["verdict_reason"] != "" {
		t.Fatalf("running row exposes verdict_reason %v", runningSummary["verdict_reason"])
	}
	if runningPayload["failure_class"] != "" {
		t.Fatalf("running row exposes failure_class %v", runningPayload["failure_class"])
	}
	if runningSummary["last_error"] != "" {
		t.Fatalf("running row exposes raw last_error %v", runningSummary["last_error"])
	}
}

func viewJSON(t *testing.T, view CaseRunView) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal view: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode view: %v", err)
	}
	return payload
}
