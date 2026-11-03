package agentapp

import (
	"encoding/json"
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// observedCaseExecutorRunUsage is the cumulative usage of the real 5-step /
// 3-assertion case AC-FR-025-01 (发布文章（正向）) of the PO user-trial retry
// (2026-11-03, case run 376007071272796163). Its summary is the failure Task 103
// removes:
//
//	"token budget: used 409842 of 400000 tokens after 28 turns; the case
//	 recorded 2 of 5 steps before the run ended"
//
// with assertion_total=0, verdict_reason=incomplete and
// terminal_cause=token_budget: the run was still on step 2 of 5 when the old
// 400k cap turned the next turn into the tool-less settlement turn, so it never
// reached the remaining steps or recorded any of the 3 assertions. Evidence:
// docs/version-V0.2.1/acceptance/evidence/user-trials-team/product-owner/retry/
// (result.json finding O13, tools/out-exec-detail.json).
var observedCaseExecutorRunUsage = Usage{Tokens: 409842, Turns: 28}

// TestCaseExecutorBudgetAllowsExecution pins Task 89, its Task 97 raise and its
// Task 103 raise: execute.case runs through the case_executor profile, which
// must get an explicit case-scale token floor instead of the generic 20k chat
// budget. The floor is enforced by the runner, not read from the stored profile
// row: profiles are seeded once and EnsureProfiles never overwrites an existing
// row, so a stale chat-scale row must not push the executor back to 20k (the
// same reasoning as the Task 79 design floor). O13 showed that the 400k floor is
// still not enough for a complete 5-step/3-assertion case (409,842 tokens used
// after 28 turns, only 2/5 steps, 0/3 assertions): the raised bound must keep
// that observed usage inside the run while remaining a bounded cap. The raise is
// case-executor-only, and a work item's more specific 0.2.1 budget declaration
// still wins over the profile floor.
func TestCaseExecutorBudgetAllowsExecution(t *testing.T) {
	runner := &Runner{}
	item := agent.WorkItem{VersionID: 1, Type: WorkTypeExecuteCase}
	liveExecutor := agent.AgentProfile{
		Key:     ProfileKeyCaseExecutor,
		Budgets: agent.ProfileBudgets{MaxTurns: 60, SoftSeconds: 780, HardSeconds: 900},
	}
	liveBudget := runner.resourceBudget(item, liveExecutor)

	// The O13 observation must continue: at 28 turns the live case_executor
	// envelope (60 turns / 780s) is inside both the turn fuse and the clock, so
	// only the token budget can decide. 409,842 tokens must not settle: the next
	// tool turn is the one that executes steps 3-5 and records the assertions.
	if got := liveBudget.Decide(observedCaseExecutorRunUsage); got != BudgetContinue {
		t.Fatalf("case_executor budget decision at the observed O13 usage = %q, want %q",
			got, BudgetContinue)
	}
	if cause := liveBudget.ExceededBy(observedCaseExecutorRunUsage); cause != "" {
		t.Fatalf("case_executor cause at the observed O13 usage = %q, want none", cause)
	}

	// Reproduction of the failure: the same O13 usage under the previous 400k
	// envelope settles with the recorded terminal cause, which is why the run
	// could not reach steps 3-5 or its 3 assertions.
	oldCap := Budget{MaxTokens: 400000, RunSeconds: 780, MaxTurnsFuse: 59}
	if got := oldCap.Decide(observedCaseExecutorRunUsage); got != BudgetSettle {
		t.Fatalf("old 400k budget decision at the observed O13 usage = %q, want %q",
			got, BudgetSettle)
	}
	if cause := oldCap.ExceededBy(observedCaseExecutorRunUsage); cause != BudgetCauseTokens {
		t.Fatalf("old 400k cause at the observed O13 usage = %q, want %q",
			cause, BudgetCauseTokens)
	}

	const wantCap = 800000
	if liveBudget.MaxTokens != wantCap {
		t.Fatalf("live case_executor token budget = %d, want the raised execution cap %d",
			liveBudget.MaxTokens, wantCap)
	}
	if budget := runner.resourceBudget(item, agent.AgentProfile{
		Key:     ProfileKeyCaseExecutor,
		Budgets: agent.ProfileBudgets{MaxTurns: 12, SoftSeconds: 300, HardSeconds: 300},
	}); budget.MaxTokens != wantCap {
		t.Fatalf("case_executor token budget from a stale profile = %d, want %d",
			budget.MaxTokens, wantCap)
	}
	if liveBudget.MaxTokens <= DefaultBudgetMaxTokens {
		t.Fatalf("case_executor token budget = %d, want above the generic chat budget %d",
			liveBudget.MaxTokens, DefaultBudgetMaxTokens)
	}
	if liveBudget.MaxTokens <= observedCaseExecutorRunUsage.Tokens {
		t.Fatalf("case_executor token budget = %d, want above the observed O13 usage %d",
			liveBudget.MaxTokens, observedCaseExecutorRunUsage.Tokens)
	}

	// The raise is a bounded cap, not an unbounded budget: 800k still settles.
	if got := liveBudget.Decide(Usage{Tokens: wantCap - 1, Turns: observedCaseExecutorRunUsage.Turns}); got != BudgetContinue {
		t.Fatalf("case_executor budget decision one token below the cap = %q, want %q", got, BudgetContinue)
	}
	if got := liveBudget.Decide(Usage{Tokens: wantCap, Turns: observedCaseExecutorRunUsage.Turns}); got != BudgetSettle {
		t.Fatalf("case_executor budget decision at the cap = %q, want %q", got, BudgetSettle)
	}
	if cause := liveBudget.ExceededBy(Usage{Tokens: wantCap, Turns: observedCaseExecutorRunUsage.Turns}); cause != BudgetCauseTokens {
		t.Fatalf("case_executor cause at the cap = %q, want %q", cause, BudgetCauseTokens)
	}

	// The raise is execution-only: the design team keeps its 200k budget and the
	// chat-scale clarification team keeps the generic 20k budget.
	if DesignRunMaxTokens != 200000 {
		t.Fatalf("design token budget = %d, want the unchanged 200000", DesignRunMaxTokens)
	}
	if DefaultBudgetMaxTokens != 20000 {
		t.Fatalf("default chat token budget = %d, want the unchanged 20000", DefaultBudgetMaxTokens)
	}
	designHost := agent.AgentProfile{
		Key:     ProfileKeyDesignHost,
		Budgets: agent.ProfileBudgets{MaxTurns: 16, SoftSeconds: 600, HardSeconds: 900},
	}
	if got := runner.resourceBudget(agent.WorkItem{VersionID: 1, Type: WorkTypeDesignRound}, designHost).MaxTokens; got != DesignRunMaxTokens {
		t.Fatalf("design token budget = %d, want the unchanged design budget %d", got, DesignRunMaxTokens)
	}
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
	overridden := runner.resourceBudget(declared, liveExecutor)
	if overridden.MaxTokens != 50000 || overridden.RunSeconds != 120 || overridden.MaxTurnsFuse != 5 {
		t.Fatalf("declared execute.case budget = %#v, want max_tokens/run_seconds/max_turns_fuse 50000/120/5", overridden)
	}
	// The declared 50k cap, not the 800k floor, bounds the run: the O13 usage is
	// over the declaration and must settle on the token cause.
	if cause := overridden.ExceededBy(Usage{Tokens: observedCaseExecutorRunUsage.Tokens, Turns: 4}); cause != BudgetCauseTokens {
		t.Fatalf("declared execute.case cause at the observed O13 usage = %q, want %q (the item declaration must still win)",
			cause, BudgetCauseTokens)
	}
}
