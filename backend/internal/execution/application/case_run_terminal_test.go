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

// handoffPost returns the first typed handoff post of the fixture team room.
func handoffPost(posts []executionapp.TeamPost) *executionapp.TeamPost {
	for index := range posts {
		if posts[index].Kind == "handoff" {
			return &posts[index]
		}
	}
	return nil
}

// TestCaseBlockedRunCarriesTerminalExplanationAndHandoff pins QA Q2's remaining
// Medium residual for a blocked run: the deterministic blocked verdict used to
// persist no terminal_cause/next_action, so the execution page could only show a
// bare "阻塞" while the concrete reason lived in last_error. The settlement now
// persists the stable cause/next action even though the verdict itself is
// untouched, and a human-needed cause emits one typed handoff post.
func TestCaseBlockedRunCarriesTerminalExplanationAndHandoff(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)

	finished, err := fixture.runner.Finish(context.Background(), p3Scope(), execution.FinishCandidate{Verdict: execution.CaseRunBlocked})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.Status != execution.CaseRunBlocked {
		t.Fatalf("status = %q, want blocked: the explanation must not change the verdict", finished.Status)
	}
	if finished.Summary.TerminalCause != execution.VerdictReasonBlocked {
		t.Fatalf("terminal cause = %q, want %q", finished.Summary.TerminalCause, execution.VerdictReasonBlocked)
	}
	if finished.Summary.NextAction != execution.NextActionResolveBlocker {
		t.Fatalf("next action = %q, want %q", finished.Summary.NextAction, execution.NextActionResolveBlocker)
	}

	handoff := handoffPost(fixture.room.posts)
	if handoff == nil {
		t.Fatalf("posts = %+v, want a typed handoff for the human-needed blocked cause", fixture.room.posts)
	}
	if !strings.Contains(handoff.Content, execution.VerdictReasonBlocked) {
		t.Fatalf("handoff content = %q, want the terminal cause", handoff.Content)
	}
	if len(handoff.Refs) != 1 || handoff.Refs[0].Kind != "case_run" || handoff.Refs[0].ID != "3001" {
		t.Fatalf("handoff refs = %+v, want case_run 3001", handoff.Refs)
	}
	// QA Q2 also observed empty terminal_cause/next_action in the
	// case_run.finished SSE payload; the terminal write now carries them.
	payloads := eventPayloads(fixture.events, executionapp.EventCaseRunFinished)
	if len(payloads) != 1 {
		t.Fatalf("case_run.finished events = %d, want 1", len(payloads))
	}
	if got, _ := payloads[0]["terminal_cause"].(string); got != execution.VerdictReasonBlocked {
		t.Fatalf("event terminal_cause = %q, want %q", got, execution.VerdictReasonBlocked)
	}
	if got, _ := payloads[0]["next_action"].(string); got != execution.NextActionResolveBlocker {
		t.Fatalf("event next_action = %q, want %q", got, execution.NextActionResolveBlocker)
	}
}

// TestCaseAssertionFailureCarriesExplanationWithoutHandoff pins the other side
// of the classification: a failed(application) run explains itself with the
// stable assertion_failed cause and the review_failure next action, but does not
// duplicate the P4 finding handoff (the issue-candidate pipeline owns it).
func TestCaseAssertionFailureCarriesExplanationWithoutHandoff(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	if _, err := fixture.runner.StepRecord(context.Background(), p3Scope(), tools.StepRecordInput{
		Seq: 1, Status: "passed", Observed: "登录页可见", VersionSUTID: int64ptr(7),
	}); err != nil {
		t.Fatalf("StepRecord: %v", err)
	}
	if _, err := fixture.runner.AssertionRecord(context.Background(), p3Scope(), tools.AssertionRecordInput{
		StepSeq: 1, AssertionSeq: 1, Verdict: "failed", Actual: "按钮缺失", VersionSUTID: int64ptr(7),
	}); err != nil {
		t.Fatalf("AssertionRecord: %v", err)
	}

	finished, err := fixture.runner.Finish(context.Background(), p3Scope(), execution.FinishCandidate{
		Verdict: execution.CaseRunFailed,
	})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.Status != execution.CaseRunFailed || finished.FailureClass != execution.FailureClassApplication {
		t.Fatalf("run = %s/%s, want failed/application", finished.Status, finished.FailureClass)
	}
	if finished.Summary.TerminalCause != execution.VerdictReasonAssertionFailed {
		t.Fatalf("terminal cause = %q, want %q", finished.Summary.TerminalCause, execution.VerdictReasonAssertionFailed)
	}
	if finished.Summary.NextAction != execution.NextActionReviewFailure {
		t.Fatalf("next action = %q, want %q", finished.Summary.NextAction, execution.NextActionReviewFailure)
	}
	if handoff := handoffPost(fixture.room.posts); handoff != nil {
		t.Fatalf("unexpected handoff for an application failure: %+v", handoff)
	}
}

// TestCaseRunUsageSurfacedAfterFinalize pins the QA Q2 usage gap for the normal
// case_finish path: the verdict is written while the agent run is still active,
// so the run result's usage arrives in Finalize afterwards. Finalize attaches the
// observed usage to the terminal summary without changing the verdict.
func TestCaseRunUsageSurfacedAfterFinalize(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	if _, err := fixture.runner.StepRecord(context.Background(), p3Scope(), tools.StepRecordInput{
		Seq: 1, Status: "passed", Observed: "登录页可见", VersionSUTID: int64ptr(7),
	}); err != nil {
		t.Fatalf("StepRecord: %v", err)
	}
	if _, err := fixture.runner.AssertionRecord(context.Background(), p3Scope(), tools.AssertionRecordInput{
		StepSeq: 1, AssertionSeq: 1, Verdict: "passed", Actual: "按钮可见", VersionSUTID: int64ptr(7),
	}); err != nil {
		t.Fatalf("AssertionRecord: %v", err)
	}
	if _, err := fixture.runner.Finish(context.Background(), p3Scope(), execution.FinishCandidate{Verdict: execution.CaseRunPassed}); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	result := agentapp.RunResult{
		AgentRunID: 7001, Status: "done", Turns: 15,
		Usage: map[string]int{"total_tokens": 222159, "prompt_tokens": 200000, "completion_tokens": 22159},
	}
	if err := fixture.runner.Finalize(context.Background(), 3001, 3, result, nil, 1, 3); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	stored := fixture.caseRuns.get(t, 3001)
	if stored.Status != execution.CaseRunPassed || stored.Summary.VerdictReason != execution.VerdictReasonPassed {
		t.Fatalf("stored = %s/%q, want the unchanged passed verdict", stored.Status, stored.Summary.VerdictReason)
	}
	if stored.Summary.Usage == nil {
		t.Fatal("summary usage is nil, want the observed agent-run usage")
	}
	if stored.Summary.Usage.Turns != 15 || stored.Summary.Usage.TotalTokens != 222159 ||
		stored.Summary.Usage.PromptTokens != 200000 || stored.Summary.Usage.CompletionTokens != 22159 {
		t.Fatalf("summary usage = %+v, want 15 turns / 222159 total / 200000 prompt / 22159 completion",
			stored.Summary.Usage)
	}
}

// TestCasePassedRunHasNoTerminalExplanation pins that a clean verdict stays
// silent: no fabricated cause, no next action and no handoff.
func TestCasePassedRunHasNoTerminalExplanation(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	if _, err := fixture.runner.StepRecord(context.Background(), p3Scope(), tools.StepRecordInput{
		Seq: 1, Status: "passed", Observed: "登录页可见", VersionSUTID: int64ptr(7),
	}); err != nil {
		t.Fatalf("StepRecord: %v", err)
	}
	if _, err := fixture.runner.AssertionRecord(context.Background(), p3Scope(), tools.AssertionRecordInput{
		StepSeq: 1, AssertionSeq: 1, Verdict: "passed", Actual: "按钮可见", VersionSUTID: int64ptr(7),
	}); err != nil {
		t.Fatalf("AssertionRecord: %v", err)
	}
	finished, err := fixture.runner.Finish(context.Background(), p3Scope(), execution.FinishCandidate{Verdict: execution.CaseRunPassed})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.Summary.TerminalCause != "" || finished.Summary.NextAction != "" {
		t.Fatalf("passed summary = %+v, want no cause/next action", finished.Summary)
	}
	if handoff := handoffPost(fixture.room.posts); handoff != nil {
		t.Fatalf("unexpected handoff for a passed run: %+v", handoff)
	}
}
