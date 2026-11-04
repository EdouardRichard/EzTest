package agentapp

import (
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

func v022Profile(t *testing.T, profiles []agent.AgentProfile, key string) agent.AgentProfile {
	t.Helper()
	for _, profile := range profiles {
		if profile.Key == key {
			return profile
		}
	}
	t.Fatalf("profile %q is missing", key)
	return agent.AgentProfile{}
}

// TestProfileBudgetsV022 pins the R-A2-2 seeded profile envelopes: the
// clarification Host gets 12 turns / 600s soft / 900s hard, the design team
// keeps its turn counts with the same 600/900 wall-clock envelope, and the case
// executor keeps its 60/780/900 guards. Token accounting is telemetry only, so
// every conversation/design profile and the case executor run with
// MaxTokens=0 + UnlimitedTokens=true (R-A2-1/R-A2-4).
func TestProfileBudgetsV022(t *testing.T) {
	host := v022Profile(t, P1Profiles(), ProfileKeyClarificationHost)
	wantHost := agent.ProfileBudgets{MaxTurns: 12, SoftSeconds: 600, HardSeconds: 900}
	if host.Budgets != wantHost {
		t.Fatalf("clarification_host budgets = %+v, want %+v", host.Budgets, wantHost)
	}

	design := []struct {
		key   string
		turns int
	}{
		{ProfileKeyDesignHost, 16},
		{ProfileKeyDesignPlanner, 16},
		{ProfileKeyCaseAuthor, 16},
		{ProfileKeyDesignCritic, 4},
	}
	designProfiles := P2Profiles()
	for _, want := range design {
		profile := v022Profile(t, designProfiles, want.key)
		if profile.Budgets.MaxTurns != want.turns ||
			profile.Budgets.SoftSeconds != 600 || profile.Budgets.HardSeconds != 900 {
			t.Fatalf("%s budgets = %+v, want turns=%d soft=600 hard=900",
				want.key, profile.Budgets, want.turns)
		}
	}

	executor := v022Profile(t, P3Profiles(), ProfileKeyCaseExecutor)
	wantExecutor := agent.ProfileBudgets{MaxTurns: 60, SoftSeconds: 780, HardSeconds: 900}
	if executor.Budgets != wantExecutor {
		t.Fatalf("case_executor budgets = %+v, want %+v", executor.Budgets, wantExecutor)
	}

	runner := &Runner{}
	executorBudget := runner.resourceBudget(
		agent.WorkItem{VersionID: 1, Type: WorkTypeExecuteCase}, executor)
	if !executorBudget.UnlimitedTokens || executorBudget.MaxTokens != 0 {
		t.Fatalf("case_executor budget = {max=%d unlimited=%v}, want unlimited (0,true)",
			executorBudget.MaxTokens, executorBudget.UnlimitedTokens)
	}
	tokenHeavy := Usage{Tokens: 50_000_000, Turns: 1}
	if got := executorBudget.Decide(tokenHeavy); got != BudgetContinue {
		t.Fatalf("case_executor decide at 50M tokens = %q, want %q", got, BudgetContinue)
	}
	if cause := executorBudget.ExceededBy(tokenHeavy); cause != "" {
		t.Fatalf("case_executor cause at 50M tokens = %q, want none", cause)
	}

	conversation := append(append([]agent.AgentProfile{}, P1Profiles()...), P2Profiles()...)
	for _, profile := range conversation {
		budget := runner.resourceBudget(
			agent.WorkItem{VersionID: 1, Type: WorkTypeChatTurn}, profile)
		if !budget.UnlimitedTokens || budget.MaxTokens != 0 {
			t.Fatalf("%s budget = {max=%d unlimited=%v}, want unlimited token telemetry",
				profile.Key, budget.MaxTokens, budget.UnlimitedTokens)
		}
	}
}
