package agentapp

import (
	"encoding/json"
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// observedCaseExecutorRunTokens is the cumulative usage of the two automatic
// case runs of the final small acceptance run (S5, 2026-11-03): 31,943 and
// 33,979 total tokens after four turns. Both runs ended inconclusive with
// assertion_total=0 because the generic 20k chat budget forced the tool-less
// settlement turn before any execution.step_record/assertion_record call.
const observedCaseExecutorRunTokens = 33979

// TestCaseExecutorBudgetAllowsExecution pins Task 89: execute.case runs through
// the case_executor profile, which must get an explicit case-scale token floor
// instead of the generic 20k chat budget. The floor is enforced by the runner,
// not read from the stored profile row: profiles are seeded once and
// EnsureProfiles never overwrites an existing row, so a stale chat-scale row
// must not push the executor back to 20k (the same reasoning as the Task 79
// design floor). The floor must exceed the observed 32-34k usage by enough
// headroom for the remaining steps/assertions, and a work item's more specific
// 0.2.1 budget declaration still wins over the profile floor.
func TestCaseExecutorBudgetAllowsExecution(t *testing.T) {
	runner := &Runner{}
	staleExecutor := agent.AgentProfile{
		Key:     ProfileKeyCaseExecutor,
		Budgets: agent.ProfileBudgets{MaxTurns: 12, SoftSeconds: 300, HardSeconds: 300},
	}
	budget := runner.resourceBudget(agent.WorkItem{VersionID: 1, Type: WorkTypeExecuteCase}, staleExecutor)

	const wantFloor = 200000
	if budget.MaxTokens != wantFloor {
		t.Fatalf("case_executor token budget = %d, want the explicit execution floor %d",
			budget.MaxTokens, wantFloor)
	}
	if budget.MaxTokens <= DefaultBudgetMaxTokens {
		t.Fatalf("case_executor token budget = %d, want above the generic chat budget %d",
			budget.MaxTokens, DefaultBudgetMaxTokens)
	}
	if budget.MaxTokens <= observedCaseExecutorRunTokens {
		t.Fatalf("case_executor token budget = %d, want above the observed S5 usage %d",
			budget.MaxTokens, observedCaseExecutorRunTokens)
	}

	// Behavioral reproduction: at the observed S5 usage the generic 20k budget
	// asks for the tool-less settlement turn, while the execution floor still
	// allows the next tool turn (the step/assertion writes the case run needs).
	chatBudget := Budget{MaxTokens: DefaultBudgetMaxTokens, RunSeconds: 900, MaxTurnsFuse: 60}
	if got := chatBudget.Decide(Usage{Tokens: observedCaseExecutorRunTokens, Turns: 4}); got != BudgetSettle {
		t.Fatalf("generic budget decision at the observed S5 usage = %q, want %q", got, BudgetSettle)
	}
	if got := budget.Decide(Usage{Tokens: observedCaseExecutorRunTokens, Turns: 4}); got != BudgetContinue {
		t.Fatalf("case_executor budget decision at the observed S5 usage = %q, want %q", got, BudgetContinue)
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
}
