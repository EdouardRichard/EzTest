package agentapp

import (
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// TestDesignRunBudgetAllowsPropose pins that the design team's v0.22 runtime
// envelope no longer carries a token ceiling: the S4 "runtime exhausted before
// case.graph_propose" failure is removed by making token usage telemetry only
// (R-A2-1/R-A2-4). The design Host still gets its 16-turn / 600s soft guard and
// must not be clamped by the chat-scale agent.max_turns setting.
func TestDesignRunBudgetAllowsPropose(t *testing.T) {
	runner := &Runner{}
	designHost := agent.AgentProfile{
		Key: ProfileKeyDesignHost,
		Budgets: agent.ProfileBudgets{
			MaxTurns: 16, SoftSeconds: 600, HardSeconds: 900,
		},
	}
	budget := runner.resourceBudget(agent.WorkItem{VersionID: 1, Type: WorkTypeChatTurn}, designHost)
	if !budget.UnlimitedTokens || budget.MaxTokens != 0 {
		t.Fatalf("design token budget = {max=%d unlimited=%v}, want unlimited telemetry",
			budget.MaxTokens, budget.UnlimitedTokens)
	}
	if budget.RunSeconds != 600 {
		t.Fatalf("design run seconds = %d, want the profile budget 600", budget.RunSeconds)
	}
	if got := runner.maxTurns(designHost); got != 16 {
		t.Fatalf("design max turns = %d, want the profile budget 16 (the chat-scale agent.max_turns must not clamp it)", got)
	}

	// Clarification keeps the chat turn guard and its token budget is unlimited
	// telemetry too.
	clarification := agent.AgentProfile{
		Key:     ProfileKeyClarificationHost,
		Budgets: agent.ProfileBudgets{MaxTurns: 12, SoftSeconds: 600, HardSeconds: 900},
	}
	if got := runner.maxTurns(clarification); got != defaultMaxTurns {
		t.Fatalf("clarification max turns = %d, want the chat-scale %d", got, defaultMaxTurns)
	}
	if got := runner.resourceBudget(agent.WorkItem{VersionID: 1, Type: WorkTypeChatTurn}, clarification); !got.UnlimitedTokens || got.MaxTokens != 0 {
		t.Fatalf("clarification token budget = {max=%d unlimited=%v}, want unlimited telemetry",
			got.MaxTokens, got.UnlimitedTokens)
	}
}
