package agentapp

import (
	"context"
	"strconv"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// ProfileSeedVersion is the built-in profile seed revision. Fresh rows are
// written at this version; legacy rows start at 0 (migration default) and are
// reconciled once by BackfillProfiles. Seed 3 (v0.2.4-C T13) retires the 13
// browser.* atomic actions from stored whitelists and moves the case executor
// to the domain-level tool surface with an unlimited turn seed. Seed 4
// (v0.2.4-C T14) re-reconciles rows T13 already marked seed 3: the seed-2
// case_executor prompt body (still naming browser.*) was not in the upgrade
// allow-list, so T13 left it stale; T14 registers it (prompt_seed_retired.go)
// and bumps the marker so those rows get the current prompt. Seed 5
// (v0.2.4-C T15) re-reconciles seed-4 rows so the case_executor allowed_tools
// increment — DERIVED from caseExecutorTools, which now includes browser.cli —
// unions the browser capability-domain passthrough into stored whitelists. The
// browser-retirement fence stays at 3 (a row at seed 3+ keeps browser.cli).
// Seed 6 (v0.2.4-C T19) re-reconciles seed-5 rows for the skill capability
// domain: the derived increment now includes skill.read, and the pre-T19
// case_executor prompt body (registered in prompt_seed_skills.go) is upgraded
// to the progressive-disclosure prompt. Seed 7 (v0.2.4-E T12) adds the
// independent design_judge profile seed: EnsureProfiles inserts the new row at
// this version, and the bump re-reconciles existing rows to the new marker
// (no tool/budget/prompt change for existing keys — the judge increment adds a
// new profile, it does not widen existing ones). Seed 8 (v0.2.4-F T15)
// re-reconciles seed-7 rows for the autonomous-loop surface: the derived
// case_executor increment now includes sut.read / sut.register /
// sut.asset_register / fixture.upload / fixture.download, and the pre-T15
// case_executor prompt bodies (pre-T12 skill era + T12 natural-language era,
// registered in prompt_seed_autonomy.go) are upgraded to the autonomous-loop
// prompt.
const ProfileSeedVersion = 8

// browserRetirementSeedVersion is the seed revision that removed the browser.*
// atomic-action tools. The retired-domain cleanup below is fenced to rows BELOW
// this version so it runs exactly once per row: when a later seed re-introduces
// the browser domain (browser.cli, T15), rows already reconciled to seed 3 are
// never stripped again.
const browserRetirementSeedVersion = 3

// ActionProfileBackfilled audits one reconciled legacy profile (spec §3.3). The
// metadata never contains the prompt body.
const ActionProfileBackfilled = "agent_profile.backfilled"

// ProfileBackfillRepository is the persistence port of the one-time v0.2.3
// profile backfill (*agentpg.RuntimeRepository satisfies it).
type ProfileBackfillRepository interface {
	ListProfiles(ctx context.Context) ([]agent.AgentProfile, error)
	ListPromptRevisions(ctx context.Context, key string, limit int, beforeVersion *int) ([]agent.PromptRevision, bool, error)
	SavePromptRevision(ctx context.Context, write agent.PromptWrite) (agent.PromptWriteResult, error)
	ApplyProfileSeedBackfill(ctx context.Context, key string, update agent.ProfileSeedBackfill) (agent.AgentProfile, bool, error)
}

// legacyProfileBudgetUpgrades maps the profiles whose seeded envelope changed
// to their old defaults. A budget is only raised when the stored value still
// equals the known legacy default, so an administrator custom budget is never
// overwritten (spec §3.3, clarify §C-02). The case executor entry is the
// v0.2.4-C seed-3 change: the frozen 150-turn cap becomes the unlimited turn
// seed (MaxTurns 0; the turn budget is governed by execution.max_case_turns).
var legacyProfileBudgetUpgrades = map[string]struct {
	legacy  agent.ProfileBudgets
	current agent.ProfileBudgets
}{
	ProfileKeyClarificationHost: {
		legacy:  agent.ProfileBudgets{MaxTurns: 12, SoftSeconds: 300, HardSeconds: 600},
		current: agent.ProfileBudgets{MaxTurns: 12, SoftSeconds: 600, HardSeconds: 900},
	},
	ProfileKeyDesignCritic: {
		legacy:  agent.ProfileBudgets{MaxTurns: 4, SoftSeconds: 120, HardSeconds: 300},
		current: agent.ProfileBudgets{MaxTurns: 4, SoftSeconds: 600, HardSeconds: 900},
	},
	ProfileKeyCaseExecutor: {
		legacy:  agent.ProfileBudgets{MaxTurns: 150, SoftSeconds: 1200, HardSeconds: 2400},
		current: agent.ProfileBudgets{MaxTurns: 0, SoftSeconds: 1200, HardSeconds: 2400},
	},
}

// legacySeedPromptHashes lives in prompt_seed.go with the rest of the seed
// prompt policy.

// BackfillProfiles reconciles every legacy profile row to the current seed
// exactly once, fenced by agent_profile.seed_version. The flow is idempotent
// and preserves administrator edits:
//
//   - allowed_tools becomes union(stored, current increment) minus the retired
//     tool names (seed 3 drops the browser.* atomic actions); the union, the
//     removal and the budget fence run inside the repository UPDATE against the
//     current row, so a concurrent administrator PATCH is preserved; a tool
//     removed after the marker is never re-added;
//   - budgets are raised only when the stored row still equals the known legacy
//     defaults;
//   - an old built-in prompt revision (source=seed and a known legacy body) gets
//     a new append-only source=seed revision; source=admin/reset is untouched;
//   - model/name/enabled are never written;
//   - seed_version is written last, after the prompt upgrade succeeded.
//
// audit receives one agent_profile.backfilled event per actually-changed
// profile; pass nil when the caller has no audit trail (Task 20 wires the
// composition root's audit service).
func BackfillProfiles(ctx context.Context, repo ProfileBackfillRepository, audit auditapp.Recorder) error {
	if repo == nil {
		return platform.Unavailable("agent profile backfill repository is not configured")
	}
	profiles, err := repo.ListProfiles(ctx)
	if err != nil {
		return err
	}
	for _, stored := range profiles {
		if stored.SeedVersion >= ProfileSeedVersion {
			continue
		}
		update := agent.ProfileSeedBackfill{SeedVersion: ProfileSeedVersion}
		toolsPlanned := false
		if increment := v022ProfileTools[stored.Key]; len(increment) > 0 &&
			!profileToolsContainAll(stored.AllowedTools, increment) {
			update.AddAllowedTools = increment
			toolsPlanned = true
		}
		// The retired-browser cleanup belongs to the seed-2→3 transition only
		// (see browserRetirementSeedVersion): a row already at seed 3 keeps any
		// browser-domain tool a later seed legitimately re-adds.
		if stored.SeedVersion < browserRetirementSeedVersion {
			if removals := retiredBrowserTools(stored.AllowedTools); len(removals) > 0 {
				update.RemoveAllowedTools = removals
				toolsPlanned = true
			}
		}
		budgetsPlanned := false
		if rule, ok := legacyProfileBudgetUpgrades[stored.Key]; ok && stored.Budgets == rule.legacy {
			update.BudgetUpgrade = &agent.ProfileBudgetUpgrade{
				Legacy:  rule.legacy,
				Current: rule.current,
			}
			budgetsPlanned = true
		}
		promptChanged, err := backfillSeedPrompt(ctx, repo, stored.Key, stored.SystemPrompt)
		if err != nil {
			return err
		}

		appliedProfile, applied, err := repo.ApplyProfileSeedBackfill(ctx, stored.Key, update)
		if err != nil {
			return err
		}
		if !applied {
			continue
		}
		changed := make([]string, 0, 3)
		if toolsPlanned {
			changed = append(changed, "allowed_tools")
		}
		// A concurrent administrator budget edit makes the repository's budget
		// CASE skip the upgrade; only report the field when the row moved.
		if budgetsPlanned && appliedProfile.Budgets != stored.Budgets {
			changed = append(changed, "budgets")
		}
		if promptChanged {
			changed = append(changed, "system_prompt")
		}
		if len(changed) > 0 {
			recordProfileBackfilled(ctx, audit, appliedProfile, changed)
		}
	}
	return nil
}

// profileToolsContainAll reports whether every increment tool is already in the
// stored set. The repository still unions against the live row, so this is only
// the cheap "do we need to write at all" pre-check.
func profileToolsContainAll(stored, wanted []string) bool {
	seen := make(map[string]struct{}, len(stored))
	for _, name := range stored {
		seen[name] = struct{}{}
	}
	for _, name := range wanted {
		if _, ok := seen[name]; !ok {
			return false
		}
	}
	return true
}

// retiredBrowserTools derives the exact browser.* names to drop from a stored
// whitelist. Membership uses the capability-domain authority (tools.ToolDomain)
// applied to the row itself, so there is no hand-copied second list of the 13
// retired action names; a tool outside the browser domain is never returned.
func retiredBrowserTools(stored []string) []string {
	var removals []string
	for _, name := range stored {
		if tools.ToolDomain(name) == "browser" {
			removals = append(removals, name)
		}
	}
	return removals
}

// recordProfileBackfilled is best-effort: it runs after the marker write, so a
// crash in between loses the event but never re-applies or blocks the backfill,
// and a recorder failure never fails the upgrade.
func recordProfileBackfilled(
	ctx context.Context,
	audit auditapp.Recorder,
	profile agent.AgentProfile,
	fields []string,
) {
	if audit == nil {
		return
	}
	_ = audit.Record(ctx, auditapp.Event{
		Action:     ActionProfileBackfilled,
		TargetKind: "agent_profile",
		TargetID:   strconv.FormatInt(profile.ID, 10),
		Metadata: map[string]any{
			"profile_key":  profile.Key,
			"fields":       fields,
			"seed_version": profile.SeedVersion,
		},
	})
}
