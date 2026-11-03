package agentapp

import (
	"encoding/json"
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// observedCaseExecutorRunUsage is the cumulative usage of the real 4-step case
// run of the final-small S5 retry-6 acceptance run (2026-11-03, case run
// 375990017199177730). Its summary is the failure Task 97 removes:
//
//	"token budget: used 216814 of 200000 tokens after 15 turns; the case
//	 recorded 2 of 4 steps before the run ended"
//
// with assertion_total=0 and terminal_cause=token_budget: the run was still on
// step 2 of 4 when the old 200k cap turned the next turn into the tool-less
// settlement turn, so no execution.assertion_record could ever be written.
var observedCaseExecutorRunUsage = Usage{Tokens: 216814, Turns: 15}

// TestCaseExecutorBudgetAllowsExecution pins Task 89 and its Task 97 raise:
// execute.case runs through the case_executor profile, which must get an
// explicit case-scale token floor instead of the generic 20k chat budget. The
// floor is enforced by the runner, not read from the stored profile row:
// profiles are seeded once and EnsureProfiles never overwrites an existing row,
// so a stale chat-scale row must not push the executor back to 20k (the same
// reasoning as the Task 79 design floor). S5 retry-6 showed that the old 200k
// floor is not enough for a 4-step case (216,814 tokens used after 15 turns,
// only 2/4 steps, 0 assertions): the raised bound must keep that observed usage
// inside the run while remaining a bounded cap. A work item's more specific
// 0.2.1 budget declaration still wins over the profile floor.
func TestCaseExecutorBudgetAllowsExecution(t *testing.T) {
	runner := &Runner{}
	item := agent.WorkItem{VersionID: 1, Type: WorkTypeExecuteCase}
	staleExecutor := agent.AgentProfile{
		Key:     ProfileKeyCaseExecutor,
		Budgets: agent.ProfileBudgets{MaxTurns: 12, SoftSeconds: 300, HardSeconds: 300},
	}
	budget := runner.resourceBudget(item, staleExecutor)

	const wantCap = 400000
	if budget.MaxTokens != wantCap {
		t.Fatalf("case_executor token budget = %d, want the explicit execution cap %d",
			budget.MaxTokens, wantCap)
	}
	if budget.MaxTokens <= DefaultBudgetMaxTokens {
		t.Fatalf("case_executor token budget = %d, want above the generic chat budget %d",
			budget.MaxTokens, DefaultBudgetMaxTokens)
	}
	if budget.MaxTokens <= observedCaseExecutorRunUsage.Tokens {
		t.Fatalf("case_executor token budget = %d, want above the observed S5 retry-6 usage %d",
			budget.MaxTokens, observedCaseExecutorRunUsage.Tokens)
	}

	// The live case_executor profile carries 60 turns / 780s (see
	// TestTurnBudgetPrecedence), so at the S5 retry-6 observation of 15 turns
	// only the token budget can decide. The same usage that settled at 200k must
	// continue here: the next tool turn is the one that records step 3.
	liveExecutor := agent.AgentProfile{
		Key:     ProfileKeyCaseExecutor,
		Budgets: agent.ProfileBudgets{MaxTurns: 60, SoftSeconds: 780, HardSeconds: 900},
	}
	liveBudget := runner.resourceBudget(item, liveExecutor)
	if liveBudget.MaxTokens != wantCap {
		t.Fatalf("live case_executor token budget = %d, want %d", liveBudget.MaxTokens, wantCap)
	}
	if got := liveBudget.Decide(observedCaseExecutorRunUsage); got != BudgetContinue {
		t.Fatalf("case_executor budget decision at the observed S5 retry-6 usage = %q, want %q",
			got, BudgetContinue)
	}
	if cause := liveBudget.ExceededBy(observedCaseExecutorRunUsage); cause != "" {
		t.Fatalf("case_executor cause at the observed S5 retry-6 usage = %q, want none", cause)
	}

	// Reproduction of the failure: the same observed usage under the old 200k
	// envelope settles with the recorded S5 retry-6 cause, which is why the run
	// could not reach its remaining steps and assertions.
	oldCap := Budget{MaxTokens: 200000, RunSeconds: 780, MaxTurnsFuse: 59}
	if got := oldCap.Decide(observedCaseExecutorRunUsage); got != BudgetSettle {
		t.Fatalf("old 200k budget decision at the observed S5 retry-6 usage = %q, want %q",
			got, BudgetSettle)
	}
	if cause := oldCap.ExceededBy(observedCaseExecutorRunUsage); cause != BudgetCauseTokens {
		t.Fatalf("old 200k cause at the observed S5 retry-6 usage = %q, want %q",
			cause, BudgetCauseTokens)
	}

	// The raise is a bounded cap, not an unbounded budget: 400k still settles.
	if got := liveBudget.Decide(Usage{Tokens: wantCap - 1, Turns: observedCaseExecutorRunUsage.Turns}); got != BudgetContinue {
		t.Fatalf("case_executor budget decision one token below the cap = %q, want %q", got, BudgetContinue)
	}
	if got := liveBudget.Decide(Usage{Tokens: wantCap, Turns: observedCaseExecutorRunUsage.Turns}); got != BudgetSettle {
		t.Fatalf("case_executor budget decision at the cap = %q, want %q", got, BudgetSettle)
	}

	// The floor is execution-only: the chat-scale clarification profile keeps the
	// generic 20k budget.
	clarification := agent.AgentProfile{
		Key:     ProfileKeyClarificationHost,
		Budgets: agent.ProfileBudgets{MaxTurns: 12, SoftSeconds: 300, HardSeconds: 600},
	}
	if got := runner.resourceBudget(agent.WorkItem{VersionID: 1, Type: WorkTypeChatTurn}, clarification).MaxTokens; got != DefaultBudgetMaxTokens {
		t.Fatalf("clarification token budget = %d, want %d", got, DefaultBudgetMaxTokens)
	}

	// The work item's 0.2.1 budget declaration is the most specific source and
	// still wins over the profile floor (the budget stays configurable).
	declared := agent.WorkItem{VersionID: 1, Type: WorkTypeExecuteCase}
	declared.Budget = json.RawMessage(`{"max_tokens":50000,"run_seconds":120,"max_turns_fuse":5}`)
	overridden := runner.resourceBudget(declared, staleExecutor)
	if overridden.MaxTokens != 50000 || overridden.RunSeconds != 120 || overridden.MaxTurnsFuse != 5 {
		t.Fatalf("declared execute.case budget = %#v, want max_tokens/run_seconds/max_turns_fuse 50000/120/5", overridden)
	}
	// The declared 50k cap, not the 400k floor, bounds the run: the observed
	// usage is over the declaration and must settle on the token cause.
	if cause := overridden.ExceededBy(Usage{Tokens: observedCaseExecutorRunUsage.Tokens, Turns: 4}); cause != BudgetCauseTokens {
		t.Fatalf("declared execute.case cause at the observed usage = %q, want %q (the item declaration must still win)",
			cause, BudgetCauseTokens)
	}
}
