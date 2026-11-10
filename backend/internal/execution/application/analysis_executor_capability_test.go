package executionapp

// v0.2.3 X-02: an executor capability limitation is a blocked run, not an
// application defect. The deterministic analysis must read the structured step
// marker before the text heuristics and must not emit a P4 issue candidate.

import (
	"context"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

func executorLimitationObserved(code string) map[string]any {
	return map[string]any{
		"summary":                       "页面上没有上传按钮，无法选择本地文件",
		execution.ExecutorLimitationKey: map[string]any{"code": code},
	}
}

// TestExecutorLimitationBlockedNoIssue: even with a failed assertion (which
// normally wins as application evidence), the structured limitation marker
// classifies the run blocked with terminal_cause=executor_capability and emits
// no failure candidate and no team-room finding.
func TestExecutorLimitationBlockedNoIssue(t *testing.T) {
	fixture := newAnalysisFixture()
	fixture.addCase(41, 501, 601, "P0")
	fixture.addRun(9001, 1001, 41, execution.CaseRunFailed, execution.FailureClassApplication)
	fixture.results.steps[9001] = []execution.StepResult{{
		CaseRunID: 9001, Seq: 1, Status: execution.StepFailed,
		Observed: executorLimitationObserved("file_chooser_unavailable"),
	}}
	fixture.addFailedAssertion(9001, 1, 1)

	if err := fixture.analysis.AnalyzeWave(context.Background(), fixture.wave.ID); err != nil {
		t.Fatalf("AnalyzeWave: %v", err)
	}
	stored, ok := fixture.runs.byID[9001]
	if !ok {
		t.Fatal("case run disappeared")
	}
	if stored.Status != execution.CaseRunBlocked {
		t.Fatalf("status = %q, want blocked", stored.Status)
	}
	if stored.FailureClass != execution.FailureClassBlocked {
		t.Fatalf("failure class = %q, want blocked", stored.FailureClass)
	}
	if stored.Summary.TerminalCause != execution.TerminalCauseExecutorCapability {
		t.Fatalf("terminal cause = %q, want %q", stored.Summary.TerminalCause, execution.TerminalCauseExecutorCapability)
	}
	if stored.Summary.VerdictReason != execution.VerdictReasonBlocked {
		t.Fatalf("verdict reason = %q, want blocked", stored.Summary.VerdictReason)
	}
	if events := fixture.caseRunEvents(EventFailureCandidate); len(events) != 0 {
		t.Fatalf("executor limitation emitted %d failure candidate event(s)", len(events))
	}
	if fixture.team.count() != 0 {
		t.Fatalf("executor limitation posted %d team-room finding(s)", fixture.team.count())
	}
	if execution.FailureClassBlocked.IsIssueCandidate() {
		t.Fatal("blocked must not be an issue candidate")
	}
}

// TestExecutorLimitationBeatsMissingFeatureText: the observation text names a
// missing control (legacy heuristic → application), but the structured marker
// says the executor could not perform the action. The marker wins.
func TestExecutorLimitationBeatsMissingFeatureText(t *testing.T) {
	fixture := newAnalysisFixture()
	fixture.addCase(42, 502, 602, "P1")
	fixture.addRun(9002, 1002, 42, execution.CaseRunBlocked, execution.FailureClassBlocked)
	fixture.results.steps[9002] = []execution.StepResult{{
		CaseRunID: 9002, Seq: 1, Status: execution.StepBlocked,
		Observed: executorLimitationObserved("hover_unavailable"),
	}}

	if err := fixture.analysis.AnalyzeWave(context.Background(), fixture.wave.ID); err != nil {
		t.Fatalf("AnalyzeWave: %v", err)
	}
	stored := fixture.runs.byID[9002]
	if stored.Status != execution.CaseRunBlocked || stored.FailureClass != execution.FailureClassBlocked {
		t.Fatalf("run = %s/%s, want blocked/blocked (missing-feature text must not win)", stored.Status, stored.FailureClass)
	}
	if stored.Summary.TerminalCause != execution.TerminalCauseExecutorCapability {
		t.Fatalf("terminal cause = %q, want executor_capability", stored.Summary.TerminalCause)
	}
}

func TestFailureSubmitCannotEscalateExecutorLimitation(t *testing.T) {
	fixture := newAnalysisFixture()
	fixture.addCase(44, 504, 604, "P1")
	fixture.addRun(9004, 1004, 44, execution.CaseRunBlocked, execution.FailureClassBlocked)
	fixture.results.steps[9004] = []execution.StepResult{{
		CaseRunID: 9004, Seq: 1, Status: execution.StepBlocked,
		Observed: executorLimitationObserved("file_chooser_unavailable"),
	}}
	fixture.addFailedAssertion(9004, 1, 1)

	if _, err := fixture.analysis.SubmitFailure(context.Background(), tools.Scope{VersionID: 100, WaveID: 50}, tools.FailureSubmitInput{
		CaseRunID: 9004, FailureClass: "application", Summary: "product defect",
	}); err == nil {
		t.Fatal("executor capability limitation was escalated to an application defect")
	}
	if stored := fixture.runs.byID[9004]; stored.FailureClass != execution.FailureClassBlocked {
		t.Fatalf("failure class = %q, want blocked", stored.FailureClass)
	}
}

// TestRealMissingControlStillApplication is the guard rail: without the
// structured marker the existing missing-control text heuristic still produces
// failed(application) and an issue candidate.
func TestRealMissingControlStillApplication(t *testing.T) {
	fixture := newAnalysisFixture()
	fixture.addCase(43, 503, 603, "P2")
	fixture.addRun(9003, 1003, 43, execution.CaseRunBlocked, execution.FailureClassBlocked)
	fixture.results.steps[9003] = []execution.StepResult{{
		CaseRunID: 9003, Seq: 1, Status: execution.StepBlocked,
		Observed: map[string]any{"summary": "编辑页没有「发布时间」控件，无法设置定时发布"},
	}}

	if err := fixture.analysis.AnalyzeWave(context.Background(), fixture.wave.ID); err != nil {
		t.Fatalf("AnalyzeWave: %v", err)
	}
	stored := fixture.runs.byID[9003]
	if stored.Status != execution.CaseRunFailed || stored.FailureClass != execution.FailureClassApplication {
		t.Fatalf("run = %s/%s, want failed/application", stored.Status, stored.FailureClass)
	}
	if events := fixture.caseRunEvents(EventFailureCandidate); len(events) != 1 {
		t.Fatalf("failure candidate events = %d, want 1", len(events))
	}
}
