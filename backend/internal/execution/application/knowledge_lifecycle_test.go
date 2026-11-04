package executionapp_test

import (
	"context"
	"errors"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// fakeCaseFailureHook records failed-case observations. A configured error or
// panic proves the executor treats the failure hook as best effort.
type fakeCaseFailureHook struct {
	facts []executionapp.CaseFailureFact
	err   error
	panic bool
}

func (h *fakeCaseFailureHook) OnCaseRunFailed(_ context.Context, fact executionapp.CaseFailureFact) error {
	h.facts = append(h.facts, fact)
	if h.panic {
		panic("knowledge failure hook exploded")
	}
	return h.err
}

// TestCaseRunFailureNotifiesKnowledgeFailureHook pins the production trigger of
// MarkSuspect: a case_run that reaches the failed verdict (either through the
// deterministic Finish aggregation or through Finalize after an agent-run
// failure) notifies the optional knowledge failure hook exactly once, while a
// passed/inconclusive run never does. A hook error or panic never changes the
// persisted verdict.
func TestCaseRunFailureNotifiesKnowledgeFailureHook(t *testing.T) {
	recordFailedAssertion := func(t *testing.T, fixture *caseRunnerFixture) {
		t.Helper()
		if _, err := fixture.runner.StepRecord(context.Background(), p3Scope(), tools.StepRecordInput{
			Seq: 1, Status: "passed",
		}); err != nil {
			t.Fatalf("StepRecord: %v", err)
		}
		if _, err := fixture.runner.AssertionRecord(context.Background(), p3Scope(), tools.AssertionRecordInput{
			StepSeq: 1, AssertionSeq: 1, Verdict: "failed", Actual: "mismatch",
		}); err != nil {
			t.Fatalf("AssertionRecord: %v", err)
		}
	}

	t.Run("a failed verdict notifies the hook once with the case fact", func(t *testing.T) {
		fixture := newCaseRunnerFixture(t)
		prepareCaseRun(t, fixture)
		recordFailedAssertion(t, fixture)

		finished, err := fixture.runner.Finish(context.Background(), p3Scope(), execution.FinishCandidate{
			Verdict: execution.CaseRunPassed,
		})
		if err != nil {
			t.Fatalf("Finish: %v", err)
		}
		if finished.Status != execution.CaseRunFailed {
			t.Fatalf("case_run status = %q, want failed", finished.Status)
		}
		if len(fixture.knowledgeFailure.facts) != 1 {
			t.Fatalf("failure hook calls = %d, want exactly one", len(fixture.knowledgeFailure.facts))
		}
		fact := fixture.knowledgeFailure.facts[0]
		if fact.VersionID != 10 || fact.CaseRunID != 3001 || fact.CaseVersionID != 41 {
			t.Fatalf("failure fact = %#v, want version 10 case_run 3001 case_version 41", fact)
		}
		if fact.FailureClass != execution.FailureClassApplication {
			t.Fatalf("failure class = %q, want application", fact.FailureClass)
		}
		if len(fact.PageIDs) != 1 || fact.PageIDs[0] != 71 {
			t.Fatalf("page ids = %#v, want the targeted page 71", fact.PageIDs)
		}
		if len(fact.AccountRoles) != 1 || fact.AccountRoles[0] != "admin" {
			t.Fatalf("account roles = %#v, want [admin]", fact.AccountRoles)
		}
		// The fact type carries identifiers/enums only; there is no title or
		// credential field to persist (spec §10.1 no-secret rule).
	})

	t.Run("a passed verdict never notifies the failure hook", func(t *testing.T) {
		fixture := newCaseRunnerFixture(t)
		prepareCaseRun(t, fixture)
		if _, err := fixture.runner.StepRecord(context.Background(), p3Scope(), tools.StepRecordInput{
			Seq: 1, Status: "passed",
		}); err != nil {
			t.Fatalf("StepRecord: %v", err)
		}
		if _, err := fixture.runner.AssertionRecord(context.Background(), p3Scope(), tools.AssertionRecordInput{
			StepSeq: 1, AssertionSeq: 1, Verdict: "passed", Actual: "Login 按钮可见",
		}); err != nil {
			t.Fatalf("AssertionRecord: %v", err)
		}
		finished, err := fixture.runner.Finish(context.Background(), p3Scope(), execution.FinishCandidate{
			Verdict: execution.CaseRunPassed,
		})
		if err != nil {
			t.Fatalf("Finish: %v", err)
		}
		if finished.Status != execution.CaseRunPassed {
			t.Fatalf("case_run status = %q, want passed", finished.Status)
		}
		if len(fixture.knowledgeFailure.facts) != 0 {
			t.Fatalf("failure hook calls = %d after a passed run, want 0", len(fixture.knowledgeFailure.facts))
		}
	})

	t.Run("a hook error or panic never changes the verdict", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			apply func(*fakeCaseFailureHook)
		}{
			{name: "error", apply: func(hook *fakeCaseFailureHook) { hook.err = errors.New("knowledge down") }},
			{name: "panic", apply: func(hook *fakeCaseFailureHook) { hook.panic = true }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				fixture := newCaseRunnerFixture(t)
				tc.apply(fixture.knowledgeFailure)
				prepareCaseRun(t, fixture)
				recordFailedAssertion(t, fixture)
				finished, err := fixture.runner.Finish(context.Background(), p3Scope(), execution.FinishCandidate{
					Verdict: execution.CaseRunPassed,
				})
				if err != nil {
					t.Fatalf("Finish: %v", err)
				}
				if finished.Status != execution.CaseRunFailed {
					t.Fatalf("verdict changed to %q by a best-effort hook", finished.Status)
				}
				if len(fixture.knowledgeFailure.facts) != 1 {
					t.Fatalf("failure hook calls = %d, want the one attempt", len(fixture.knowledgeFailure.facts))
				}
			})
		}
	})

	t.Run("an agent-run failure finalized to the failed verdict notifies", func(t *testing.T) {
		fixture := newCaseRunnerFixture(t)
		prepareCaseRun(t, fixture)
		if err := fixture.runner.Finalize(context.Background(), 3001, 3, agentapp.RunResult{},
			errors.New("agent loop exploded"), 3, 3); err != nil {
			t.Fatalf("Finalize: %v", err)
		}
		if stored := fixture.caseRuns.get(t, 3001); stored.Status != execution.CaseRunFailed {
			t.Fatalf("case_run status = %q, want failed", stored.Status)
		}
		if len(fixture.knowledgeFailure.facts) != 1 {
			t.Fatalf("failure hook calls = %d, want exactly one", len(fixture.knowledgeFailure.facts))
		}
		fact := fixture.knowledgeFailure.facts[0]
		if fact.CaseRunID != 3001 || fact.VersionID != 10 {
			t.Fatalf("failure fact = %#v, want case_run 3001 of version 10", fact)
		}
		if fact.FailureClass == "" {
			t.Fatalf("failure class = empty, want the classified agent-run failure")
		}
	})
}
