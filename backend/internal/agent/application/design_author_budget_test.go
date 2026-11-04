package agentapp

import (
	"encoding/json"
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// boundedAuthorTurnTokens is the conservative per-turn token envelope of one
// focused case-author run: the profile prompt, version briefing/facts, one
// design.graph_read result and the bounded human-history/team-room tail. The
// live 0.2.1 flood reached 300k-670k conservative tokens per turn (170 replayed
// author summaries plus 10 full team-room posts), so three author turns no
// longer fit the 200k design token budget and the run settled before
// case.graph_propose.
const boundedAuthorTurnTokens = 32000

// TestDesignBudgetAllowsAllCaseAuthors pins the per-requirement case-author
// budget envelope. The live v0.2.1 database was seeded before the design budget
// was raised (case_author 8/300/600, design_planner 12/300/600) and
// EnsureProfiles never overwrites an existing row, so the runner must enforce
// the design-authoring floor in code: 16 turns, 600s soft / 900s hard and the
// 200k design token budget. Three bounded author turns (read + propose +
// summary) must fit inside the token budget, and a work item's explicit budget
// declaration must still win over the floor.
func TestDesignBudgetAllowsAllCaseAuthors(t *testing.T) {
	runner := &Runner{}
	staleProfiles := []agent.AgentProfile{
		{
			Key:     ProfileKeyCaseAuthor,
			Budgets: agent.ProfileBudgets{MaxTurns: 8, SoftSeconds: 300, HardSeconds: 600},
		},
		{
			Key:     ProfileKeyDesignPlanner,
			Budgets: agent.ProfileBudgets{MaxTurns: 12, SoftSeconds: 300, HardSeconds: 600},
		},
		{
			Key:     ProfileKeyDesignHost,
			Budgets: agent.ProfileBudgets{MaxTurns: 12, SoftSeconds: 300, HardSeconds: 600},
		},
	}
	workTypes := []string{WorkTypeDesignRequirement, WorkTypeDesignGap, WorkTypeDesignRound}
	for _, profile := range staleProfiles {
		if got := runner.maxTurns(profile); got < DesignRunMaxTurns {
			t.Fatalf("%s max turns = %d, want at least the design floor %d", profile.Key, got, DesignRunMaxTurns)
		}
		if got := runner.softSeconds(profile); got < DesignRunMinSeconds {
			t.Fatalf("%s soft seconds = %d, want at least the design floor %d", profile.Key, got, DesignRunMinSeconds)
		}
		if got := runner.hardSeconds(profile); got < DesignRunMinHardSeconds {
			t.Fatalf("%s hard seconds = %d, want at least the design floor %d", profile.Key, got, DesignRunMinHardSeconds)
		}
		for _, workType := range workTypes {
			budget := runner.resourceBudget(agent.WorkItem{VersionID: 1, Type: workType}, profile)
			if budget.MaxTokens != DesignRunMaxTokens {
				t.Fatalf("%s/%s token budget = %d, want the design budget %d",
					profile.Key, workType, budget.MaxTokens, DesignRunMaxTokens)
			}
			if budget.MaxTokens < DesignRunMinAuthorTurns*boundedAuthorTurnTokens {
				t.Fatalf("%s/%s token budget = %d, want at least %d (%d bounded author turns x %d tokens)",
					profile.Key, workType, budget.MaxTokens,
					DesignRunMinAuthorTurns*boundedAuthorTurnTokens, DesignRunMinAuthorTurns, boundedAuthorTurnTokens)
			}
			if budget.RunSeconds < DesignRunMinSeconds {
				t.Fatalf("%s/%s run seconds = %d, want at least %d",
					profile.Key, workType, budget.RunSeconds, DesignRunMinSeconds)
			}
			if budget.MaxTurnsFuse < DesignRunMaxTurns-1 {
				t.Fatalf("%s/%s turn fuse = %d, want at least %d",
					profile.Key, workType, budget.MaxTurnsFuse, DesignRunMaxTurns-1)
			}
		}
	}

	// The chat-scale clarification profile stays on the generic budget: the
	// design floor must not leak into the P1 clarification team.
	clarification := agent.AgentProfile{
		Key:     ProfileKeyClarificationHost,
		Budgets: agent.ProfileBudgets{MaxTurns: 12, SoftSeconds: 300, HardSeconds: 600},
	}
	if got := runner.maxTurns(clarification); got != defaultMaxTurns {
		t.Fatalf("clarification max turns = %d, want the chat-scale %d", got, defaultMaxTurns)
	}
	if got := runner.softSeconds(clarification); got != 300 {
		t.Fatalf("clarification soft seconds = %d, want 300", got)
	}
	if got := runner.resourceBudget(agent.WorkItem{VersionID: 1, Type: WorkTypeChatTurn}, clarification).MaxTokens; got != DefaultBudgetMaxTokens {
		t.Fatalf("clarification token budget = %d, want %d", got, DefaultBudgetMaxTokens)
	}

	// A work item's 0.2.1 budget declaration is the most specific source and
	// still wins over the profile floor.
	declared := agent.WorkItem{VersionID: 1, Type: WorkTypeDesignRequirement}
	declared.Budget = json.RawMessage(`{"max_tokens":50000,"run_seconds":120,"max_turns_fuse":5}`)
	budget := runner.resourceBudget(declared, staleProfiles[0])
	if budget.MaxTokens != 50000 || budget.RunSeconds != 120 || budget.MaxTurnsFuse != 5 {
		t.Fatalf("declared item budget = %#v, want max_tokens/run_seconds/max_turns_fuse 50000/120/5", budget)
	}
}

// TestDesignBudgetExpandsCriticAndDataPlanner makes the design floor's profile
// scope explicit (Task 79 fix round). P2Profiles seeds design_critic and
// data_planner at the lighter 4/120/300, but every design profile shares the
// 200k design token budget and can face a full design.graph_read + submit turn.
// The runner deliberately raises their runtime envelope to 16/600/900; the
// seeded row remains the admin-visible default. Pinning the expansion here means
// it is a documented envelope, not a silent override.
func TestDesignBudgetExpandsCriticAndDataPlanner(t *testing.T) {
	runner := &Runner{}
	checks := []struct {
		key      string
		workType string
	}{
		{ProfileKeyDesignCritic, WorkTypeDesignCritic},
		{ProfileKeyDataPlanner, WorkTypeDesignDataPlan},
	}
	for _, check := range checks {
		profile := agent.AgentProfile{
			Key:     check.key,
			Budgets: agent.ProfileBudgets{MaxTurns: 4, SoftSeconds: 120, HardSeconds: 300},
		}
		if got := runner.maxTurns(profile); got != DesignRunMaxTurns {
			t.Fatalf("%s max turns = %d, want the expanded design floor %d", check.key, got, DesignRunMaxTurns)
		}
		if got := runner.softSeconds(profile); got != DesignRunMinSeconds {
			t.Fatalf("%s soft seconds = %d, want the expanded design floor %d", check.key, got, DesignRunMinSeconds)
		}
		if got := runner.hardSeconds(profile); got != DesignRunMinHardSeconds {
			t.Fatalf("%s hard seconds = %d, want the expanded design floor %d", check.key, got, DesignRunMinHardSeconds)
		}
		budget := runner.resourceBudget(agent.WorkItem{VersionID: 1, Type: check.workType}, profile)
		if budget.MaxTokens != DesignRunMaxTokens {
			t.Fatalf("%s/%s token budget = %d, want the design budget %d",
				check.key, check.workType, budget.MaxTokens, DesignRunMaxTokens)
		}
		if budget.RunSeconds != DesignRunMinSeconds || budget.MaxTurnsFuse != DesignRunMaxTurns-1 {
			t.Fatalf("%s/%s budget = %#v, want run_seconds %d and turn fuse %d",
				check.key, check.workType, budget, DesignRunMinSeconds, DesignRunMaxTurns-1)
		}
	}
}
