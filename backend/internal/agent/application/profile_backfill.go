package agentapp

import (
	"context"
	"strconv"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// ProfileSeedVersion is the v0.2.3 built-in profile seed revision. Fresh rows
// are written at this version; legacy rows start at 0 (migration default) and
// are reconciled once by BackfillProfiles.
const ProfileSeedVersion = 2

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

// legacyProfileBudgetUpgrades maps the two profiles whose v0.2.2 envelope
// changed to their old defaults. A budget is only raised when the stored value
// still equals the known legacy default, so an administrator custom budget is
// never overwritten (spec §3.3, clarify §C-02).
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
}

// legacySeedPromptHashes lives in prompt_seed.go with the rest of the seed
// prompt policy.

// BackfillProfiles reconciles every legacy profile row to the current v0.2.3
// seed exactly once, fenced by agent_profile.seed_version. The flow is
// idempotent and preserves administrator edits:
//
//   - allowed_tools becomes union(stored, v0.22 increment); the union and the
//     budget fence run inside the repository UPDATE against the current row, so
//     a concurrent administrator PATCH is preserved; a tool removed after the
//     marker is never re-added;
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
