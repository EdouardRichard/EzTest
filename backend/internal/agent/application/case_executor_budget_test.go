package agentapp

import (
	"encoding/json"
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// observedCaseExecutorRunUsage is the cumulative usage of the real 5-step /
// 3-assertion case AC-FR-025-01 (鍙戝竷鏂囩珷锛堟鍚戯級) of the PO user-trial retry
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

// observedFR011CaseExecutorRunUsage is the cumulative usage of the real 4-step /
// 5-assertion case AC-FR-011-01 鐧诲綍鎴愬姛锛堟鍚戯級 of the PO final E2E
// (2026-11-03, case run 376026576434761738, wave 376026576434761728). Its
// summary is the failure Task 109 removes:
//
//	"token budget: used 813006 of 800000 tokens after 44 turns; the case
//	 recorded 3 of 4 steps before the run ended"
//
// with assertion_total=1, verdict_reason=incomplete, terminal_cause=token_budget
// and next_action=rerun: the 800k cap raised by Task 103 still turned the next
// turn into the tool-less settlement turn during the last step, so the remaining
// assertions were never recorded. 44 turns is still below the 60-turn case
// envelope, so only the token budget ended the run. Evidence:
// docs/version-V0.2.1/acceptance/evidence/user-trials-team/product-owner/e2e/
// (tools/out-exec-detail.json).
var observedFR011CaseExecutorRunUsage = Usage{Tokens: 813006, Turns: 44}

// TestCaseExecutorBudgetAllowsExecution pins Task 89, its Task 97 raise, its
// Task 103 raise and its Task 109 raise: execute.case runs through the
// case_executor profile, which must get an explicit case-scale token floor
// instead of the generic 20k chat budget. The floor is enforced by the runner,
// not read from the stored profile row: profiles are seeded once and
// EnsureProfiles never overwrites an existing row, so a stale chat-scale row
// must not push the executor back to 20k (the same reasoning as the Task 79
// design floor). O13 showed that the 400k floor was not enough for a complete
// 5-step/3-assertion case (409,842 tokens used after 28 turns, only 2/5 steps,
// 0/3 assertions); FR-011-01 then showed that the 800k floor raised by Task 103
// was still not enough for a complete 4-step/5-assertion case (813,006 tokens
// used after 44 turns, only 3/4 steps, 1/5 assertions). The raised bound must
// keep both observed usages inside the run while remaining a bounded cap. The
// raise is case-executor-only, and a work item's more specific 0.2.1 budget
// declaration still wins over the profile floor.
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

	// The FR-011-01 observation must continue too: 44 turns is inside the
	// 59-turn fuse and below the 60-turn loop cap, so the 800k token cap was the
	// only resource that ended the run (terminal_cause=token_budget). 813,006
	// tokens must not settle: the next tool turn is the one that executes the
	// remaining step and records the remaining assertions.
	if got := liveBudget.Decide(observedFR011CaseExecutorRunUsage); got != BudgetContinue {
		t.Fatalf("case_executor budget decision at the observed FR-011-01 usage = %q, want %q",
			got, BudgetContinue)
	}
	if cause := liveBudget.ExceededBy(observedFR011CaseExecutorRunUsage); cause != "" {
		t.Fatalf("case_executor cause at the observed FR-011-01 usage = %q, want none", cause)
	}

	// Reproduction of the FR-011-01 failure: the same usage under the 800k
	// envelope raised by Task 103 settles on the token cause, exactly the
	// recorded terminal_cause=token_budget (44 turns, 3 of 4 steps, 1 of 5
	// assertions).
	previousCap := Budget{MaxTokens: 800000, RunSeconds: 780, MaxTurnsFuse: 59}
	if got := previousCap.Decide(observedFR011CaseExecutorRunUsage); got != BudgetSettle {
		t.Fatalf("800k budget decision at the observed FR-011-01 usage = %q, want %q",
			got, BudgetSettle)
	}
	if cause := previousCap.ExceededBy(observedFR011CaseExecutorRunUsage); cause != BudgetCauseTokens {
		t.Fatalf("800k cause at the observed FR-011-01 usage = %q, want %q",
			cause, BudgetCauseTokens)
	}

	// The case executor has no token ceiling: budget is cost observability, not
	// a hard stop for a productive loop. Turn/time/tool-failure guards remain.
	if CaseExecutorRunMaxTokens != 0 {
		t.Fatalf("case_executor token cap = %d, want 0 (unlimited)", CaseExecutorRunMaxTokens)
	}
	if !liveBudget.UnlimitedTokens || liveBudget.MaxTokens != 0 {
		t.Fatalf("live case_executor budget = {max=%d unlimited=%v}, want unlimited", liveBudget.MaxTokens, liveBudget.UnlimitedTokens)
	}
	if budget := runner.resourceBudget(item, agent.AgentProfile{
		Key:     ProfileKeyCaseExecutor,
		Budgets: agent.ProfileBudgets{MaxTurns: 12, SoftSeconds: 300, HardSeconds: 300},
	}); !budget.UnlimitedTokens || budget.MaxTokens != 0 {
		t.Fatalf("case_executor budget from a stale profile = {max=%d unlimited=%v}, want unlimited", budget.MaxTokens, budget.UnlimitedTokens)
	}
	if got := liveBudget.Decide(Usage{Tokens: 1_268_436, Turns: 40}); got != BudgetContinue {
		t.Fatalf("unlimited budget decision at the observed FR-113-03 usage = %q, want %q", got, BudgetContinue)
	}
	if got := liveBudget.Decide(Usage{Tokens: 50_000_000, Turns: 59}); got != BudgetSettle {
		t.Fatalf("unlimited budget decision at the turn fuse = %q, want %q", got, BudgetSettle)
	}
	if cause := liveBudget.ExceededBy(Usage{Tokens: 50_000_000, Turns: observedFR011CaseExecutorRunUsage.Turns}); cause != "" {
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
	// The declared 50k cap, not the 1.2M floor, bounds the run: the observed
	// FR-011-01 usage is over the declaration and must settle on the token cause.
	if cause := overridden.ExceededBy(Usage{Tokens: observedFR011CaseExecutorRunUsage.Tokens, Turns: 4}); cause != BudgetCauseTokens {
		t.Fatalf("declared execute.case cause at the observed FR-011-01 usage = %q, want %q (the item declaration must still win)",
			cause, BudgetCauseTokens)
	}
}
