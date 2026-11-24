package executionapp_test

import (
	"context"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// budgetCaseSnapshot is the QA release T9 shape: four steps and one passing
// assertion on step 2. The token budget ended the run after steps 1-3 were
// recorded, so step 4 was never reached and no result exists for it.
func budgetCaseSnapshot() execution.CaseSnapshot {
	snapshot := fourStepCaseSnapshot()
	snapshot.Assertions = []execution.CaseAssertion{
		{StepSeq: 2, AssertionSeq: 1, Kind: execution.AssertionKindUI, Expected: "发布请求成功", SUTID: 7},
	}
	return snapshot
}

// prepareBudgetRun prepares the QA-shaped case and records the step results the
// budget-ended run produced before the stop.
func prepareBudgetRun(t *testing.T, fixture *caseRunnerFixture, recordedSteps int) {
	t.Helper()
	fixture.cases.snapshot = budgetCaseSnapshot()
	fixture.cases.executable = []execution.CaseSnapshot{fixture.cases.snapshot}
	if _, err := fixture.runner.Prepare(fixture.runCtx, executionWorkItem()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	for seq := 1; seq <= recordedSteps; seq++ {
		if _, err := fixture.runner.StepRecord(context.Background(), executionScope(), tools.StepRecordInput{
			Seq: seq, Status: "passed", Observed: "步骤完成", VersionSUTID: int64ptr(7),
		}); err != nil {
			t.Fatalf("StepRecord(%d): %v", seq, err)
		}
	}
	if _, err := fixture.runner.AssertionRecord(context.Background(), executionScope(), tools.AssertionRecordInput{
		StepSeq: 2, AssertionSeq: 1, Verdict: "passed", Actual: "POST /api/v1/posts 201", VersionSUTID: int64ptr(7),
	}); err != nil {
		t.Fatalf("AssertionRecord: %v", err)
	}
}

// finalizeBudgetEndedRun reproduces the worker finalization of a case executor
// run stopped by the token budget (58/60 turns, 1,251,485 tokens).
func finalizeBudgetEndedRun(t *testing.T, fixture *caseRunnerFixture) {
	t.Helper()
	result := agentapp.RunResult{
		Status: "done", Turns: 58,
		Usage:            map[string]int{"total_tokens": 1251485, "prompt_tokens": 1236899, "completion_tokens": 14586},
		SettlementCause:  agentapp.BudgetCauseTokens,
		SettlementDetail: "token budget: used 1251485 of 1200000 tokens after 58 turns",
		TerminalReason:   agentapp.BudgetCauseTokens,
	}
	if err := fixture.runner.Finalize(context.Background(), 3001, 3, result, nil, 1, 3); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
}

// budgetHandoffPost returns the terminal handoff post of the fixture room.
func budgetHandoffPost(posts []executionapp.TeamPost) *executionapp.TeamPost {
	for index := range posts {
		if posts[index].Kind == "handoff" {
			return &posts[index]
		}
	}
	return nil
}

// TestBudgetCauseWinsOverIncompleteSymptom reproduces QA release T9: a
// token-budget stop before the final step made the aggregate report
// inconclusive(incomplete). The explicit budget cause must win over that
// downstream symptom, the verdict itself must not change (no gate relaxation,
// no fake pass), and the run keeps its observed usage.
func TestBudgetCauseWinsOverIncompleteSymptom(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareBudgetRun(t, fixture, 3)

	finalizeBudgetEndedRun(t, fixture)

	stored := fixture.caseRuns.get(t, 3001)
	if stored.Status != execution.CaseRunInconclusive {
		t.Fatalf("status = %q, want the unchanged inconclusive verdict", stored.Status)
	}
	if stored.Summary.VerdictReason != execution.VerdictReasonIncomplete {
		t.Fatalf("verdict reason = %q, want the deterministic %q (the cause fix must not rewrite the verdict)",
			stored.Summary.VerdictReason, execution.VerdictReasonIncomplete)
	}
	if stored.Summary.TerminalCause != execution.TerminalCauseTokenBudget {
		t.Fatalf("terminal cause = %q, want %q: the run ended on the token budget, step 4 was never reached",
			stored.Summary.TerminalCause, execution.TerminalCauseTokenBudget)
	}
	if stored.Summary.NextAction != execution.NextActionRerun {
		t.Fatalf("next action = %q, want %q",
			stored.Summary.NextAction, execution.NextActionRerun)
	}
	if !strings.Contains(stored.Summary.LastError, "token budget") ||
		!strings.Contains(stored.Summary.LastError, "1251485") ||
		!strings.Contains(stored.Summary.LastError, "3 of 4") {
		t.Fatalf("last error = %q, want the observed budget, tokens and 3/4 step progress", stored.Summary.LastError)
	}
	if stored.Summary.Usage == nil || stored.Summary.Usage.Turns != 58 ||
		stored.Summary.Usage.TotalTokens != 1251485 {
		t.Fatalf("summary usage = %+v, want the observed 58 turns / 1251485 tokens", stored.Summary.Usage)
	}

	// The live SSE consumer must see the same correction, not only the detail API.
	payloads := eventPayloads(fixture.events, executionapp.EventCaseRunFinished)
	if len(payloads) != 1 {
		t.Fatalf("case_run.finished events = %d, want 1", len(payloads))
	}
	if got, _ := payloads[0]["terminal_cause"].(string); got != execution.TerminalCauseTokenBudget {
		t.Fatalf("event terminal_cause = %q, want %q", got, execution.TerminalCauseTokenBudget)
	}
	if got, _ := payloads[0]["next_action"].(string); got != execution.NextActionRerun {
		t.Fatalf("event next_action = %q, want %q", got, execution.NextActionRerun)
	}

	// The budget stop still needs a human (rerun with a fresh budget); the
	// handoff must name the budget cause.
	handoff := budgetHandoffPost(fixture.room.posts)
	if handoff == nil {
		t.Fatalf("posts = %+v, want the terminal handoff", fixture.room.posts)
	}
	if !strings.Contains(handoff.Content, execution.TerminalCauseTokenBudget) ||
		!strings.Contains(handoff.Content, execution.NextActionRerun) {
		t.Fatalf("handoff content = %q, want the budget cause and the rerun next action", handoff.Content)
	}
}

// TestCoverageNotMisledByBudget pins that the settlement correction does not
// fake coverage: a budget-ended run stays a real execution coverage gap (no
// pass is invented), the gap keeps the execution_failed_coverage reason, and
// the run itself explains the gap as a budget stop that needs a rerun.
func TestCoverageNotMisledByBudget(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareBudgetRun(t, fixture, 3)
	finalizeBudgetEndedRun(t, fixture)

	out, err := fixture.runner.CoverageRead(context.Background(), tools.Scope{VersionID: 10})
	if err != nil {
		t.Fatalf("CoverageRead: %v", err)
	}
	payload, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("coverage payload = %#v", out)
	}
	requirements, ok := payload["requirements"].([]map[string]any)
	if !ok || len(requirements) != 1 {
		t.Fatalf("requirement coverage = %#v, want the single requirement", payload["requirements"])
	}
	entry := requirements[0]
	if entry["coverage"] != "gap" {
		t.Fatalf("coverage = %v, want a real gap: a budget stop must not be counted as covered", entry["coverage"])
	}
	if entry["gap_reason"] != executionapp.GapReasonExecutionFailedCoverage {
		t.Fatalf("gap reason = %v, want %q", entry["gap_reason"], executionapp.GapReasonExecutionFailedCoverage)
	}
	if len(fixture.gaps.calls) != 0 { // no analysis ran: the coverage tool must not write gaps
		t.Fatalf("coverage read wrote %d design gaps, want 0 (read-only projection)", len(fixture.gaps.calls))
	}

	stored := fixture.caseRuns.get(t, 3001)
	if stored.Summary.TerminalCause != execution.TerminalCauseTokenBudget ||
		stored.Summary.NextAction != execution.NextActionRerun {
		t.Fatalf("stored cause/action = %q/%q, want %q/%q so the coverage gap is explained as a budget stop",
			stored.Summary.TerminalCause, stored.Summary.NextAction,
			execution.TerminalCauseTokenBudget, execution.NextActionRerun)
	}
}
