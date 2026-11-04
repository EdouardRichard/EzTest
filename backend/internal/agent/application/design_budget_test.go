package agentapp

import (
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// TestDesignRunBudgetAllowsPropose pins the S4 "runtime exhausted before
// case.graph_propose" failure. A design conversation carries the whole thread
// plus a full design-graph read; the generic chat-scale 20k-token budget can be
// exhausted after the read, which forces the settlement turn and disables tools
// exactly before the write. Design profiles therefore need a case-scale token
// budget and must not be clamped by the generic agent.max_turns guard.
func TestDesignRunBudgetAllowsPropose(t *testing.T) {
	runner := &Runner{}
	designHost := agent.AgentProfile{
		Key: ProfileKeyDesignHost,
		Budgets: agent.ProfileBudgets{
			MaxTurns: 16, SoftSeconds: 600, HardSeconds: 900,
		},
	}
	budget := runner.resourceBudget(agent.WorkItem{VersionID: 1, Type: WorkTypeChatTurn}, designHost)
	if budget.MaxTokens <= DefaultBudgetMaxTokens {
		t.Fatalf("design token budget = %d, want a case-scale budget above the %d chat default",
			budget.MaxTokens, DefaultBudgetMaxTokens)
	}
	if budget.RunSeconds != 600 {
		t.Fatalf("design run seconds = %d, want the profile budget 600", budget.RunSeconds)
	}
	if got := runner.maxTurns(designHost); got != 16 {
		t.Fatalf("design max turns = %d, want the profile budget 16 (the chat-scale agent.max_turns must not clamp it)", got)
	}

	// Clarification stays on the generic chat-scale budget.
	clarification := agent.AgentProfile{
		Key:     ProfileKeyClarificationHost,
		Budgets: agent.ProfileBudgets{MaxTurns: 12, SoftSeconds: 300, HardSeconds: 600},
	}
	if got := runner.maxTurns(clarification); got != defaultMaxTurns {
		t.Fatalf("clarification max turns = %d, want the chat-scale %d", got, defaultMaxTurns)
	}
	if got := runner.resourceBudget(agent.WorkItem{VersionID: 1, Type: WorkTypeChatTurn}, clarification).MaxTokens; got != DefaultBudgetMaxTokens {
		t.Fatalf("clarification token budget = %d, want %d", got, DefaultBudgetMaxTokens)
	}
}
