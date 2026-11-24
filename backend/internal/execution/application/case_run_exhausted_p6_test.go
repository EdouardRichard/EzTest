package executionapp_test

import (
	"context"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// exhaustedRunResult is the observed result of a case executor run whose turn
// budget exhausted during the settlement turn (P6). The settlement cause is the
// turn budget so the terminal explanation names it.
func exhaustedRunResult() agentapp.RunResult {
	return agentapp.RunResult{
		Status:           "failed",
		Turns:            3,
		Usage:            map[string]int{"total_tokens": 4096},
		SettlementCause:  agentapp.BudgetCauseTurns,
		SettlementDetail: "turn budget: used 3 of 3 turns after 3 turns",
		TerminalReason:   "budget",
	}
}

func budgetRunError() error {
	return &agentapp.RunError{Reason: "budget", Retryable: false}
}

// TestBudgetExhaustedRedispatchesWithinAttempts pins design §4.5: a turn-budget
// exhaustion is re-dispatched while the dispatch attempt budget remains, instead
// of immediately writing a terminal verdict. The row goes back to pending with a
// bumped epoch so the scheduler reconciler re-dispatches it.
func TestBudgetExhaustedRedispatchesWithinAttempts(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareOneOfFourSteps(t, fixture)

	// attempt 1 of 3: still inside the attempt budget.
	if err := fixture.runner.Finalize(context.Background(), 3001, 3, exhaustedRunResult(), budgetRunError(), 1, 3); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	stored := fixture.caseRuns.get(t, 3001)
	if stored.Status != execution.CaseRunPending {
		t.Fatalf("status = %q, want pending (re-dispatch within attempts), not a terminal verdict", stored.Status)
	}
	if stored.Status.Finished() {
		t.Fatalf("exhausted run within attempts must not be terminal, got %q", stored.Status)
	}
	if stored.DispatchEpoch <= 3 {
		t.Fatalf("dispatch epoch = %d, want bumped past 3 for the re-dispatch", stored.DispatchEpoch)
	}
}

// TestBudgetExhaustedInconclusiveNotFailed pins the core P6 invariant: once the
// dispatch attempt budget is exhausted, a turn-budget exhaustion settles
// inconclusive (rerun), NEVER failed. Budget starvation is a GOV defect, not a
// model-incapable failure verdict (constitution art.3, design §4.5).
func TestBudgetExhaustedInconclusiveNotFailed(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareOneOfFourSteps(t, fixture)

	// attempt 3 of 3: the attempt budget is exhausted.
	if err := fixture.runner.Finalize(context.Background(), 3001, 3, exhaustedRunResult(), budgetRunError(), 3, 3); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	stored := fixture.caseRuns.get(t, 3001)
	if stored.Status == execution.CaseRunFailed {
		t.Fatalf("exhausted budget must NOT be failed (P6 exhausted≠failed), got failed")
	}
	if stored.Status != execution.CaseRunInconclusive {
		t.Fatalf("status = %q, want inconclusive after the attempt budget", stored.Status)
	}
	if stored.Summary.TerminalCause != execution.TerminalCauseTurnBudget {
		t.Fatalf("terminal cause = %q, want %q", stored.Summary.TerminalCause, execution.TerminalCauseTurnBudget)
	}
	if stored.Summary.NextAction != execution.NextActionRerun {
		t.Fatalf("next action = %q, want %q", stored.Summary.NextAction, execution.NextActionRerun)
	}
}
