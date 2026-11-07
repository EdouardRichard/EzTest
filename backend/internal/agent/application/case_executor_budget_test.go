package agentapp

import (
	"encoding/json"
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// observedCaseExecutorRunUsage is the cumulative usage of the real 5-step /
// 3-assertion case AC-FR-025-01 of the PO user-trial retry (2026-11-03, case run
// 376007071272796163). The run used 409,842 tokens after 28 turns and was
// settled by the retired 400k token hard stop; v0.22 keeps that usage as
// telemetry and must continue instead (spec R-A2-1).
var observedCaseExecutorRunUsage = Usage{Tokens: 409842, Turns: 28}

// observedFR011CaseExecutorRunUsage is the cumulative usage of the real 4-step /
// 5-assertion case AC-FR-011-01 of the PO final E2E (2026-11-03, case run
// 376026576434761738). The run used 813,006 tokens after 44 turns and was
// settled by the retired 800k token hard stop; 44 turns is still below the
// 60-turn case envelope, so the token cap was the only thing that ended it.
var observedFR011CaseExecutorRunUsage = Usage{Tokens: 813006, Turns: 44}

// TestCaseExecutorBudgetAllowsExecution pins R-A2-1/R-A2-4 for the case
// executor: cumulative tokens are telemetry only, so neither observed
// production usage may open a settlement while the run is inside its turn and
// wall-clock guards. The profile keeps its case-scale 60/780/900 guards and its
// runner-enforced budget is explicitly unlimited (MaxTokens=0 +
// UnlimitedTokens=true), independent of a stale stored profile row.
func TestCaseExecutorBudgetAllowsExecution(t *testing.T) {
	runner := &Runner{}
	item := agent.WorkItem{VersionID: 1, Type: WorkTypeExecuteCase}
	liveExecutor := agent.AgentProfile{
		Key:     ProfileKeyCaseExecutor,
		Budgets: agent.ProfileBudgets{MaxTurns: 60, SoftSeconds: 780, HardSeconds: 900},
	}
	liveBudget := runner.resourceBudget(item, liveExecutor)

	for name, usage := range map[string]Usage{
		"O13":       observedCaseExecutorRunUsage,
		"FR-011-01": observedFR011CaseExecutorRunUsage,
	} {
		if got := liveBudget.Decide(usage); got != BudgetContinue {
			t.Fatalf("case_executor decision at %s usage = %q, want %q", name, got, BudgetContinue)
		}
		if cause := liveBudget.ExceededBy(usage); cause != "" {
			t.Fatalf("case_executor cause at %s usage = %q, want none", name, cause)
		}
	}

	// Reproduction of the retired failure: the 20k/400k/800k token caps that
	// used to settle these runs are telemetry now; the same usage continues.
	for _, legacyCap := range []int{20000, 400000, 800000} {
		legacy := Budget{MaxTokens: legacyCap, RunSeconds: 780, MaxTurnsFuse: 59}
		if got := legacy.Decide(observedFR011CaseExecutorRunUsage); got != BudgetContinue {
			t.Fatalf("legacy %dk token budget decision = %q, want %q",
				legacyCap/1000, got, BudgetContinue)
		}
		if cause := legacy.ExceededBy(observedFR011CaseExecutorRunUsage); cause != "" {
			t.Fatalf("legacy %dk token budget cause = %q, want none", legacyCap/1000, cause)
		}
	}

	// The case executor has no token ceiling: budget is cost observability, not
	// a hard stop for a productive loop. Turn/time guards remain.
	if !liveBudget.UnlimitedTokens || liveBudget.MaxTokens != 0 {
		t.Fatalf("live case_executor budget = {max=%d unlimited=%v}, want unlimited",
			liveBudget.MaxTokens, liveBudget.UnlimitedTokens)
	}
	if budget := runner.resourceBudget(item, agent.AgentProfile{
		Key:     ProfileKeyCaseExecutor,
		Budgets: agent.ProfileBudgets{MaxTurns: 12, SoftSeconds: 300, HardSeconds: 300},
	}); !budget.UnlimitedTokens || budget.MaxTokens != 0 {
		t.Fatalf("case_executor budget from a stale profile = {max=%d unlimited=%v}, want unlimited",
			budget.MaxTokens, budget.UnlimitedTokens)
	}
	if got := liveBudget.Decide(Usage{Tokens: 1_268_436, Turns: 40}); got != BudgetContinue {
		t.Fatalf("unlimited budget decision at the observed FR-113-03 usage = %q, want %q", got, BudgetContinue)
	}
	if got := liveBudget.Decide(Usage{Tokens: 50_000_000, Turns: 59}); got != BudgetSettle {
		t.Fatalf("unlimited budget decision at the turn fuse = %q, want %q", got, BudgetSettle)
	}
	if cause := liveBudget.ExceededBy(Usage{Tokens: 50_000_000, Turns: 59}); cause != BudgetCauseTurns {
		t.Fatalf("case_executor cause at the turn fuse = %q, want %q", cause, BudgetCauseTurns)
	}
	if cause := liveBudget.ExceededBy(Usage{Tokens: 50_000_000, Seconds: 780}); cause != BudgetCauseTime {
		t.Fatalf("case_executor cause at the time budget = %q, want %q", cause, BudgetCauseTime)
	}

	// Token telemetry is unlimited for the design and chat teams too.
	designHost := agent.AgentProfile{
		Key:     ProfileKeyDesignHost,
		Budgets: agent.ProfileBudgets{MaxTurns: 16, SoftSeconds: 600, HardSeconds: 900},
	}
	if budget := runner.resourceBudget(agent.WorkItem{VersionID: 1, Type: WorkTypeDesignRound}, designHost); !budget.UnlimitedTokens || budget.MaxTokens != 0 {
		t.Fatalf("design token budget = {max=%d unlimited=%v}, want unlimited",
			budget.MaxTokens, budget.UnlimitedTokens)
	}
	clarification := agent.AgentProfile{
		Key:     ProfileKeyClarificationHost,
		Budgets: agent.ProfileBudgets{MaxTurns: 12, SoftSeconds: 600, HardSeconds: 900},
	}
	if budget := runner.resourceBudget(agent.WorkItem{VersionID: 1, Type: WorkTypeChatTurn}, clarification); !budget.UnlimitedTokens || budget.MaxTokens != 0 {
		t.Fatalf("clarification token budget = {max=%d unlimited=%v}, want unlimited",
			budget.MaxTokens, budget.UnlimitedTokens)
	}

	// The work item's 0.2.1 budget declaration still wins for the enforced
	// guards. A legacy max_tokens is recorded for telemetry but never decides.
	declared := agent.WorkItem{VersionID: 1, Type: WorkTypeExecuteCase}
	declared.Budget = json.RawMessage(`{"max_tokens":50000,"run_seconds":120,"max_turns_fuse":5}`)
	overridden := runner.resourceBudget(declared, liveExecutor)
	if overridden.MaxTokens != 50000 || overridden.RunSeconds != 120 || overridden.MaxTurnsFuse != 5 {
		t.Fatalf("declared execute.case budget = %#v, want max_tokens/run_seconds/max_turns_fuse 50000/120/5", overridden)
	}
	if cause := overridden.ExceededBy(Usage{Tokens: observedFR011CaseExecutorRunUsage.Tokens, Turns: 4}); cause != "" {
		t.Fatalf("declared execute.case token cause = %q, want none (tokens are telemetry)", cause)
	}
	if got := overridden.Decide(Usage{Tokens: 50_000_000, Turns: 4}); got != BudgetContinue {
		t.Fatalf("declared execute.case decision inside the turn fuse = %q, want %q", got, BudgetContinue)
	}
	if got := overridden.Decide(Usage{Tokens: 50_000_000, Turns: 5}); got != BudgetSettle {
		t.Fatalf("declared execute.case decision at the turn fuse = %q, want %q", got, BudgetSettle)
	}
}
