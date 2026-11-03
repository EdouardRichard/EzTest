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

// fourStepCaseSnapshot is the shape of the QA Q2 case: four steps with one
// assertion each, of which only step 1 completed before the agent run ended.
func fourStepCaseSnapshot() execution.CaseSnapshot {
	snapshot := caseRunSnapshot()
	snapshot.Steps = nil
	snapshot.Assertions = nil
	for seq := 1; seq <= 4; seq++ {
		snapshot.Steps = append(snapshot.Steps, execution.CaseStep{
			Seq: seq, Action: "执行步骤", Expected: "符合预期",
			TargetVersionSUTID: 7, AccountRole: "admin",
		})
		snapshot.Assertions = append(snapshot.Assertions, execution.CaseAssertion{
			StepSeq: seq, AssertionSeq: 1, Kind: "ui", Expected: "断言满足", TargetVersionSUTID: 7,
		})
	}
	return snapshot
}

// prepareOneOfFourSteps prepares the four-step case and records the step 1
// result the QA runs produced before the agent budget ended the run.
func prepareOneOfFourSteps(t *testing.T, fixture *caseRunnerFixture) {
	t.Helper()
	fixture.cases.snapshot = fourStepCaseSnapshot()
	if _, err := fixture.runner.Prepare(fixture.runCtx, p3WorkItem()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if _, err := fixture.runner.StepRecord(context.Background(), p3Scope(), tools.StepRecordInput{
		Seq: 1, Status: "passed", Observed: "登录弹窗可见", VersionSUTID: int64ptr(7),
	}); err != nil {
		t.Fatalf("StepRecord: %v", err)
	}
	if _, err := fixture.runner.AssertionRecord(context.Background(), p3Scope(), tools.AssertionRecordInput{
		StepSeq: 1, AssertionSeq: 1, Verdict: "passed", Actual: "弹窗可见", VersionSUTID: int64ptr(7),
	}); err != nil {
		t.Fatalf("AssertionRecord: %v", err)
	}
}

// TestCaseIncompleteCarriesActionableCause pins QA Q2's first half: a case run
// whose agent run ended in the tool-less settlement turn after step 1/4 must not
// be a silent inconclusive(incomplete). The terminal cause and the observable
// usage/limit are persisted on the summary (and exposed by the API view) so the
// UI/report can explain why the run stopped.
func TestCaseIncompleteCarriesActionableCause(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareOneOfFourSteps(t, fixture)

	result := agentapp.RunResult{
		Status:           "done",
		Turns:            15,
		Usage:            map[string]int{"total_tokens": 222159},
		SettlementCause:  agentapp.BudgetCauseTokens,
		SettlementDetail: "token budget: used 222159 of 200000 tokens after 15 turns",
		TerminalReason:   agentapp.BudgetCauseTokens,
	}
	if err := fixture.runner.Finalize(context.Background(), 3001, 3, result, nil, 1, 3); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	stored := fixture.caseRuns.get(t, 3001)
	if stored.Status != execution.CaseRunInconclusive {
		t.Fatalf("status = %q, want inconclusive", stored.Status)
	}
	if stored.Summary.VerdictReason != execution.VerdictReasonIncomplete {
		t.Fatalf("verdict reason = %q, want incomplete", stored.Summary.VerdictReason)
	}
	if stored.Summary.TerminalCause != execution.TerminalCauseTokenBudget {
		t.Fatalf("terminal cause = %q, want %q", stored.Summary.TerminalCause, execution.TerminalCauseTokenBudget)
	}
	if !strings.Contains(stored.Summary.LastError, "222159") ||
		!strings.Contains(stored.Summary.LastError, "200000") {
		t.Fatalf("last error = %q, want the observed tokens and the limit", stored.Summary.LastError)
	}
	if !strings.Contains(stored.Summary.LastError, "1 of 4") {
		t.Fatalf("last error = %q, want the recorded/total step progress", stored.Summary.LastError)
	}
	// QA Q2: the observed usage used to be visible only in Agent Center. The
	// terminal summary persists it so the execution page can show the same
	// numbers as the explanation/limit.
	if stored.Summary.Usage == nil {
		t.Fatal("summary usage is nil, want the observed agent-run usage")
	}
	if stored.Summary.Usage.Turns != 15 || stored.Summary.Usage.TotalTokens != 222159 {
		t.Fatalf("summary usage = %+v, want 15 turns / 222159 tokens", stored.Summary.Usage)
	}
}

// TestCaseIncompleteOffersHandoff pins QA Q2's second half: the terminal
// incomplete run must offer a human next step instead of a dead end. The
// summary carries a stable next_action and the execution team room receives one
// typed handoff post; no human_request row is fabricated because the case run is
// already terminal.
func TestCaseIncompleteOffersHandoff(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareOneOfFourSteps(t, fixture)

	result := agentapp.RunResult{
		Status:           "done",
		Turns:            15,
		Usage:            map[string]int{"total_tokens": 222159},
		SettlementCause:  agentapp.BudgetCauseTokens,
		SettlementDetail: "token budget: used 222159 of 200000 tokens after 15 turns",
		TerminalReason:   agentapp.BudgetCauseTokens,
	}
	if err := fixture.runner.Finalize(context.Background(), 3001, 3, result, nil, 1, 3); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	stored := fixture.caseRuns.get(t, 3001)
	if stored.Summary.NextAction != execution.NextActionRerun {
		t.Fatalf("next action = %q, want %q", stored.Summary.NextAction, execution.NextActionRerun)
	}
	if stored.Summary.HumanRequestID != 0 {
		t.Fatalf("a terminal run must not fabricate a human request id: %d", stored.Summary.HumanRequestID)
	}
	var handoff *executionapp.TeamPost
	for index := range fixture.room.posts {
		if fixture.room.posts[index].Kind == "handoff" {
			handoff = &fixture.room.posts[index]
			break
		}
	}
	if handoff == nil {
		t.Fatalf("posts = %+v, want a typed handoff post", fixture.room.posts)
	}
	if !strings.Contains(handoff.Content, string(stored.Summary.TerminalCause)) ||
		!strings.Contains(handoff.Content, "1 of 4") {
		t.Fatalf("handoff content = %q, want the cause and the step progress", handoff.Content)
	}
	if len(handoff.Refs) != 1 || handoff.Refs[0].Kind != "case_run" || handoff.Refs[0].ID != "3001" {
		t.Fatalf("handoff refs = %+v, want case_run 3001", handoff.Refs)
	}
}

// TestCaseIncompleteWithoutSettlementReportsAgentStopped covers the other path
// into a silent incomplete: the model produced a final answer without calling
// execution.case_finish and without any budget being hit. That cause is reported
// as agent_stopped, never hidden, and still offers the rerun handoff.
func TestCaseIncompleteWithoutSettlementReportsAgentStopped(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareOneOfFourSteps(t, fixture)

	result := agentapp.RunResult{Status: "done", Turns: 6, Usage: map[string]int{"total_tokens": 42000}}
	if err := fixture.runner.Finalize(context.Background(), 3001, 3, result, nil, 1, 3); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	stored := fixture.caseRuns.get(t, 3001)
	if stored.Status != execution.CaseRunInconclusive ||
		stored.Summary.VerdictReason != execution.VerdictReasonIncomplete {
		t.Fatalf("stored = %s/%s", stored.Status, stored.Summary.VerdictReason)
	}
	if stored.Summary.TerminalCause != execution.TerminalCauseAgentStopped {
		t.Fatalf("terminal cause = %q, want %q", stored.Summary.TerminalCause, execution.TerminalCauseAgentStopped)
	}
	if stored.Summary.NextAction != execution.NextActionRerun {
		t.Fatalf("next action = %q, want rerun", stored.Summary.NextAction)
	}
	if !strings.Contains(stored.Summary.LastError, "1 of 4") {
		t.Fatalf("last error = %q, want the step progress", stored.Summary.LastError)
	}
}

// TestCaseBudgetExitKeepsRecordedAssertions pins the execution-side half of Task
// 98: a budget-ended run retains the step/assertion results recorded before the
// budget hit. The terminal verdict stays inconclusive(incomplete) with the
// budget cause and rerun handoff (Task 92), so a real assertion result reaches
// the S5 evidence gate without the verdict pretending the case passed.
func TestCaseBudgetExitKeepsRecordedAssertions(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareOneOfFourSteps(t, fixture)

	result := agentapp.RunResult{
		Status:           "done",
		Turns:            23,
		Usage:            map[string]int{"total_tokens": 406167},
		SettlementCause:  agentapp.BudgetCauseTokens,
		SettlementDetail: "token budget: used 406167 of 400000 tokens after 23 turns",
		TerminalReason:   agentapp.BudgetCauseTokens,
	}
	if err := fixture.runner.Finalize(context.Background(), 3001, 3, result, nil, 1, 3); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	stored := fixture.caseRuns.get(t, 3001)
	if stored.Status != execution.CaseRunInconclusive ||
		stored.Summary.VerdictReason != execution.VerdictReasonIncomplete {
		t.Fatalf("stored = %s/%s, want inconclusive(incomplete); a budget exit must never become a pass",
			stored.Status, stored.Summary.VerdictReason)
	}
	if stored.Summary.TerminalCause != execution.TerminalCauseTokenBudget {
		t.Fatalf("terminal cause = %q, want %q", stored.Summary.TerminalCause, execution.TerminalCauseTokenBudget)
	}
	if stored.Summary.NextAction != execution.NextActionRerun {
		t.Fatalf("next action = %q, want %q", stored.Summary.NextAction, execution.NextActionRerun)
	}

	steps, err := fixture.results.ListSteps(context.Background(), 3001)
	if err != nil {
		t.Fatalf("ListSteps: %v", err)
	}
	if len(steps) != 1 || steps[0].Seq != 1 || steps[0].Status != execution.StepPassed {
		t.Fatalf("recorded steps = %#v, want the executed step 1 kept", steps)
	}
	assertions, err := fixture.results.ListAssertions(context.Background(), 3001)
	if err != nil {
		t.Fatalf("ListAssertions: %v", err)
	}
	if len(assertions) != 1 {
		t.Fatalf("recorded assertions = %d, want the executed assertion kept across the budget exit", len(assertions))
	}
	kept := assertions[0]
	if kept.StepSeq != 1 || kept.AssertionSeq != 1 || kept.Verdict != execution.AssertionPassed ||
		kept.Actual != "弹窗可见" {
		t.Fatalf("recorded assertion = %#v, want the real executed result", kept)
	}
}
