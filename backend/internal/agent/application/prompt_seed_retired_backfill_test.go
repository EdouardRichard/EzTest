package agentapp_test

import (
	"context"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// plantCaseExecutorRow plants a case_executor profile row at the given seed
// version with the given prompt body as both the stored system_prompt and the
// latest prompt revision (source). It mirrors a real legacy DB row whose prompt
// was never upgraded to the capability-domain seed.
func plantCaseExecutorRow(repo *fakeBackfillRepo, seedVersion int, body, source string) agent.AgentProfile {
	profile := agent.AgentProfile{
		ID:           31,
		Key:          agentapp.ProfileKeyCaseExecutor,
		Name:         "用例执行器",
		SystemPrompt: body,
		AllowedTools: []string{
			"context.task_pack", "browser.goto", "browser.snapshot",
			"http.request", "db.query", "execution.step_record", "admin.custom_tool",
		},
		Budgets:     agent.ProfileBudgets{MaxTurns: 150, SoftSeconds: 1200, HardSeconds: 2400},
		Enabled:     true,
		SeedVersion: seedVersion,
	}
	repo.profiles[profile.Key] = profile
	repo.revisions[profile.Key] = []agent.PromptRevision{{
		ProfileKey:   profile.Key,
		Version:      1,
		SystemPrompt: body,
		Note:         "seed",
		Source:       source,
	}}
	return profile
}

// TestBackfillUpgradesStaleSeed2CaseExecutorPrompt pins the T14 seed-2 upgrade
// plus the T15 browser-domain re-introduction: a legacy row whose prompt still
// names the retired browser.* atomic actions is upgraded to the current
// capability-domain seed (the registered seed-2 digest is recognized), the
// retired atomic actions are dropped, the admin-added tool is kept, and the
// browser.cli passthrough (the seed-5 increment, derived from caseExecutorTools)
// is added. browser.cli is the NEW browser-domain tool, not a retired action.
func TestBackfillUpgradesStaleSeed2CaseExecutorPrompt(t *testing.T) {
	repo := newFakeBackfillRepo()
	plantCaseExecutorRow(repo, 2, agentapp.RetiredSeed2CaseExecutorBody, agent.PromptSourceSeed)
	audit := &fakeProfileAudit{}

	if err := agentapp.BackfillProfiles(context.Background(), repo, audit); err != nil {
		t.Fatalf("BackfillProfiles: %v", err)
	}
	got := repo.profiles[agentapp.ProfileKeyCaseExecutor]
	current, _ := agentapp.DefaultPrompt(agentapp.ProfileKeyCaseExecutor)
	if got.SystemPrompt != current {
		t.Fatalf("stale seed-2 prompt was not upgraded to the current seed:\n%s", got.SystemPrompt)
	}
	// The current seed names browser.cli (the domain passthrough) but no retired
	// atomic action: stripping browser.cli must leave no browser.* reference.
	if !strings.Contains(got.SystemPrompt, "browser.cli") {
		t.Fatalf("upgraded prompt does not name the browser.cli domain tool:\n%s", got.SystemPrompt)
	}
	if strings.Contains(strings.ReplaceAll(got.SystemPrompt, "browser.cli", ""), "browser.") {
		t.Fatalf("upgraded prompt still names a retired browser.* action:\n%s", got.SystemPrompt)
	}
	for _, name := range got.AllowedTools {
		if tools.ToolDomain(name) == "browser" && name != tools.ToolBrowserCLI {
			t.Fatalf("retired browser tool survived: %q", name)
		}
	}
	if !toolListContains(got.AllowedTools, tools.ToolBrowserCLI) {
		t.Fatalf("browser.cli was not added by the seed-5 increment: %v", got.AllowedTools)
	}
	if !toolListContains(got.AllowedTools, "admin.custom_tool") {
		t.Fatalf("admin-added tool lost: %v", got.AllowedTools)
	}
	if got.SeedVersion != agentapp.ProfileSeedVersion {
		t.Fatalf("seed version = %d, want %d", got.SeedVersion, agentapp.ProfileSeedVersion)
	}
	if len(audit.events) != 1 {
		t.Fatalf("audit events = %#v", audit.events)
	}
	fields, _ := audit.events[0].Metadata["fields"].([]string)
	if !containsString(fields, "system_prompt") {
		t.Fatalf("audit fields = %#v, want system_prompt", audit.events[0].Metadata["fields"])
	}
}

// TestBackfillUpgradesSeed3RowWithStaleSeed2Prompt is the regression pin for the
// ProfileSeedVersion bump: T13 already marked this row seed_version=3 but left
// its seed-2 prompt stale (the digest was not registered then). T14 must
// re-reconcile it. Reverting the bump to 3 makes BackfillProfiles skip the row
// and this test fails.
func TestBackfillUpgradesSeed3RowWithStaleSeed2Prompt(t *testing.T) {
	repo := newFakeBackfillRepo()
	profile := plantCaseExecutorRow(repo, 3, agentapp.RetiredSeed2CaseExecutorBody, agent.PromptSourceSeed)
	// T13 already reconciled the whitelist/budget on this row.
	profile.AllowedTools = []string{"context.task_pack", "exec.*", "http.request", "db.query", "execution.step_record"}
	profile.Budgets = agent.ProfileBudgets{MaxTurns: 0, SoftSeconds: 1200, HardSeconds: 2400}
	repo.profiles[profile.Key] = profile

	if err := agentapp.BackfillProfiles(context.Background(), repo, nil); err != nil {
		t.Fatalf("BackfillProfiles: %v", err)
	}
	got := repo.profiles[agentapp.ProfileKeyCaseExecutor]
	current, _ := agentapp.DefaultPrompt(agentapp.ProfileKeyCaseExecutor)
	if got.SystemPrompt != current {
		t.Fatalf("seed-3 row with a stale seed-2 prompt was not upgraded (ProfileSeedVersion bump missing?):\n%s", got.SystemPrompt)
	}
	if got.SeedVersion != agentapp.ProfileSeedVersion {
		t.Fatalf("seed version = %d, want %d", got.SeedVersion, agentapp.ProfileSeedVersion)
	}
}

// TestBackfillPreservesAdminCaseExecutorRevision confirms an administrator prompt
// revision is never overwritten, even when its body resembles a legacy seed.
func TestBackfillPreservesAdminCaseExecutorRevision(t *testing.T) {
	repo := newFakeBackfillRepo()
	plantCaseExecutorRow(repo, 2, agentapp.RetiredSeed2CaseExecutorBody, agent.PromptSourceAdmin)

	if err := agentapp.BackfillProfiles(context.Background(), repo, nil); err != nil {
		t.Fatalf("BackfillProfiles: %v", err)
	}
	got := repo.profiles[agentapp.ProfileKeyCaseExecutor]
	if got.SystemPrompt != agentapp.RetiredSeed2CaseExecutorBody {
		t.Fatalf("admin prompt revision was overwritten:\n%s", got.SystemPrompt)
	}
	if len(repo.writes) != 0 {
		t.Fatalf("admin revision triggered a prompt write: %#v", repo.writes)
	}
}

// TestBackfillDoesNotUpgradeUnknownCaseExecutorBody is the negative control: an
// unrecognized seed-body (not any registered digest) is left untouched, proving
// the upgrade is hash-gated rather than a blanket rewrite.
func TestBackfillDoesNotUpgradeUnknownCaseExecutorBody(t *testing.T) {
	repo := newFakeBackfillRepo()
	unknown := "你是自定义的执行器提示词。\n[profile:case_executor]"
	plantCaseExecutorRow(repo, 2, unknown, agent.PromptSourceSeed)

	if err := agentapp.BackfillProfiles(context.Background(), repo, nil); err != nil {
		t.Fatalf("BackfillProfiles: %v", err)
	}
	got := repo.profiles[agentapp.ProfileKeyCaseExecutor]
	if got.SystemPrompt != unknown {
		t.Fatalf("unknown seed body was wrongly upgraded:\n%s", got.SystemPrompt)
	}
}
