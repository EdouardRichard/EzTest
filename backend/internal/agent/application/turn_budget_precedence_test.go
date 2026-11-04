package agentapp

import (
	"encoding/json"
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// TestTurnBudgetPrecedence pins the QA Q2 investigation: the 15/16-turn stop was
// not a stale 16-turn cap. The live case_executor profile carries 60 turns and
// its empty work-item budget ({}) must not be clamped by the chat-scale
// agent.max_turns guard or by the design floor. The default resource budget for
// execute.case therefore has a 59-turn fuse, and reaching that fuse reports the
// concrete turn budget cause instead of an unnamed settlement.
func TestTurnBudgetPrecedence(t *testing.T) {
	runner := &Runner{settings: p3Settings{"agent.max_turns": 12}}
	executor := agent.AgentProfile{
		Key:     ProfileKeyCaseExecutor,
		Budgets: agent.ProfileBudgets{MaxTurns: 60, SoftSeconds: 780, HardSeconds: 900},
	}

	if got := runner.maxTurns(executor); got != 60 {
		t.Fatalf("case_executor max turns = %d, want the profile's 60 (not the chat guard or a 16-turn cap)", got)
	}

	item := agent.WorkItem{VersionID: 1, Type: WorkTypeExecuteCase, Budget: json.RawMessage(`{}`)}
	budget := runner.resourceBudget(item, executor)
	if budget.MaxTurnsFuse != 59 {
		t.Fatalf("execute.case turn fuse = %d, want 59 (one below the 60-turn loop cap)", budget.MaxTurnsFuse)
	}
	if budget.MaxTokens != CaseExecutorRunMaxTokens {
		t.Fatalf("execute.case token budget = %d, want %d", budget.MaxTokens, CaseExecutorRunMaxTokens)
	}
	if budget.RunSeconds != 780 {
		t.Fatalf("execute.case run seconds = %d, want the profile's 780", budget.RunSeconds)
	}
	if got := budget.ExceededBy(Usage{Turns: 15}); got != "" {
		t.Fatalf("turn cause at 15 turns = %q, want none (the live envelope allows 60)", got)
	}
	if got := budget.ExceededBy(Usage{Turns: 59}); got != BudgetCauseTurns {
		t.Fatalf("turn cause at the fuse = %q, want %q", got, BudgetCauseTurns)
	}
	// With an unlimited token budget the token cap is not a cause at 15 turns.
	// not a 16-turn cap: the same envelope reports the token cause there.
	if got := budget.ExceededBy(Usage{Turns: 15, Tokens: 50_000_000}); got != "" {
		t.Fatalf("cause at the token budget = %q, want %q", got, BudgetCauseTokens)
	}
}

// TestTurnBudgetDeclaredItemStillWins pins that the precedence fix is not a
// blanket override: an explicitly declared work-item fuse stays the most
// specific source (Task 9). The live QA items declared {} so they never exercised
// this path.
func TestTurnBudgetDeclaredItemStillWins(t *testing.T) {
	runner := &Runner{settings: p3Settings{"agent.max_turns": 12}}
	executor := agent.AgentProfile{
		Key:     ProfileKeyCaseExecutor,
		Budgets: agent.ProfileBudgets{MaxTurns: 60, SoftSeconds: 780, HardSeconds: 900},
	}
	item := agent.WorkItem{
		VersionID: 1, Type: WorkTypeExecuteCase,
		Budget: json.RawMessage(`{"max_turns_fuse":5}`),
	}
	if got := runner.resourceBudget(item, executor).MaxTurnsFuse; got != 5 {
		t.Fatalf("declared turn fuse = %d, want the explicit 5", got)
	}
}
