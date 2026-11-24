package agentapp_test

import (
	"context"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// designJudgeWhitelist is the expected design_judge tool surface (E spec
// SC-E3): read-only requirement/graph/code/environment inputs plus exactly one
// write entry (design.judgment_submit) and the completion declaration. The
// judgment tool is referenced by its contract name; its registration is owned
// by the judgment-tool task (T13), so the whitelist pins the name here without
// a forward dependency on the tool struct.
var designJudgeWhitelist = []string{
	tools.ToolRequirementRead,
	tools.ToolDesignGraphRead,
	tools.ToolCodeTree,
	tools.ToolCodeRead,
	tools.ToolCodeSearch,
	tools.ToolCodeFacts,
	tools.ToolCodeRoutes,
	tools.ToolCodeAPIs,
	tools.ToolCodeDiff,
	tools.ToolCodeImpact,
	tools.ToolCodeAnchors,
	tools.ToolSUTList,
	tools.ToolSUTPageList,
	tools.ToolSUTDatabaseList,
	tools.ToolContextVersionPack,
	"design.judgment_submit",
	tools.ToolTaskComplete,
}

// TestDesignJudgeProfileAndRole is the T12 core acceptance: the judge profile
// exists, maps to the case_judge catalog role, carries none of the
// independence-leaking or case-writing tools, and stays inside the role
// whitelist (ValidateRoleProfiles fails closed otherwise).
func TestDesignJudgeProfileAndRole(t *testing.T) {
	p, ok := agentapp.Profile(agentapp.ProfileKeyDesignJudge)
	if !ok {
		t.Fatal("design_judge profile missing")
	}
	role, ok := agentapp.RoleForProfile(agentapp.ProfileKeyDesignJudge)
	if !ok || role.Key != agentapp.RoleCaseJudge {
		t.Fatalf("role = %v/%v, want case_judge", role.Key, ok)
	}
	for _, banned := range []string{
		tools.ToolContextTaskPack,
		tools.ToolContextTeamRoomRead,
		tools.ToolCaseGraphPropose,
		tools.ToolDesignReviewSubmit,
	} {
		for _, allowed := range p.AllowedTools {
			if allowed == banned {
				t.Fatalf("judge must not have %s", banned)
			}
		}
		if agentapp.ToolAllowed(agentapp.RoleCaseJudge, banned) {
			t.Fatalf("case_judge role must not allow %s", banned)
		}
	}
	if err := agentapp.ValidateRoleProfiles([]agent.AgentProfile{p}); err != nil {
		t.Fatalf("profile outside role whitelist: %v", err)
	}
	if strings.Join(p.AllowedTools, ",") != strings.Join(designJudgeWhitelist, ",") {
		t.Fatalf("judge tools = %v, want %v", p.AllowedTools, designJudgeWhitelist)
	}
	if strings.Join(role.Tools, ",") != strings.Join(designJudgeWhitelist, ",") {
		t.Fatalf("case_judge role tools = %v, want %v", role.Tools, designJudgeWhitelist)
	}
	if !p.Enabled {
		t.Fatal("design_judge seed is disabled")
	}
	if p.Model != nil {
		t.Fatalf("judge seed model = %v, want nil (inherits the platform default; design.judge_model wiring is the composition root's job)", *p.Model)
	}
	// The judge shares the design team envelope (design_author seed).
	if p.Budgets != (agent.ProfileBudgets{MaxTurns: 16, SoftSeconds: 600, HardSeconds: 900}) {
		t.Fatalf("judge budgets = %+v, want 16/600/900", p.Budgets)
	}
	if !strings.Contains(p.SystemPrompt, "[profile:design_judge]") {
		t.Fatalf("judge prompt missing fake marker: %s", p.SystemPrompt)
	}
	if !strings.Contains(role.SystemPrompt, "[role:case_judge]") {
		t.Fatalf("case_judge role prompt missing role marker: %s", role.SystemPrompt)
	}
}

// TestDesignJudgePromptTeachesRubricAndBoundary pins the FULL judge prompt
// definition (T12 owns it; the prompt-increment task only touches the
// clarification/author/critic prompts): the three rubric dimensions, the
// per-case verdict schema with finding fields and severity semantics, and the
// read-only/no-confirm/no-assistant-message boundary.
func TestDesignJudgePromptTeachesRubricAndBoundary(t *testing.T) {
	p, ok := agentapp.Profile(agentapp.ProfileKeyDesignJudge)
	if !ok {
		t.Fatal("design_judge profile missing")
	}
	for _, want := range []string{
		// dimension 1: coverage
		"覆盖率", "given/when/then", "反向", "scope_out", "AC×用例",
		// dimension 2: executability
		"可执行", "target_description", "test_data", "account_role", "步骤顺序",
		// dimension 3: related systems
		"关联系统", "sut_description", "环境目录",
		// verdict schema
		"design.judgment_submit", "pass|fail", "findings",
		"dimension", "severity", "message", "case_ref",
		"blocker", "important", "minor",
		// boundary
		"不修改用例", "不确认", "assistant",
	} {
		if !strings.Contains(p.SystemPrompt, want) {
			t.Fatalf("judge prompt lacks %q:\n%s", want, p.SystemPrompt)
		}
	}
	// Independence: the prompt must not point the judge at the author-leaking
	// context tools or the case-writing surfaces it does not have.
	for _, gone := range []string{
		"context.task_pack", "context.team_room_read",
		"case.graph_propose", "design.review_submit",
	} {
		if strings.Contains(p.SystemPrompt, gone) {
			t.Fatalf("judge prompt must not teach %q:\n%s", gone, p.SystemPrompt)
		}
	}
	role, ok := agentapp.Role(agentapp.RoleCaseJudge)
	if !ok {
		t.Fatal("case_judge role missing")
	}
	for _, want := range []string{"design.judgment_submit", "只读", "不修改用例", "不确认"} {
		if !strings.Contains(role.SystemPrompt, want) {
			t.Fatalf("case_judge role prompt lacks %q:\n%s", want, role.SystemPrompt)
		}
	}
}

// TestDesignJudgeRoleIsTypePathOnly pins the catalog shape: the judge consumes
// the 0.2.1 TYPE-path design.judge/design.rework work (same family as
// design.requirement/design.critic), so the role owns no kind-path work item
// kinds and must not enter the kind-path stage-entry matrix.
func TestDesignJudgeRoleIsTypePathOnly(t *testing.T) {
	role, ok := agentapp.Role(agentapp.RoleCaseJudge)
	if !ok {
		t.Fatal("case_judge role missing")
	}
	if len(role.WorkItemKinds) != 0 {
		t.Fatalf("case_judge work item kinds = %v, want none (type-path role)", role.WorkItemKinds)
	}
	for _, kind := range []string{
		agent.WorkItemKindDesignCases, agent.WorkItemKindReviewCases,
		agent.WorkItemKindExecuteBatch, agent.WorkItemKindReportVersion,
	} {
		if agentapp.RoleAllowsWorkItemKind(agentapp.RoleCaseJudge, kind) {
			t.Fatalf("case_judge must not own kind-path kind %q", kind)
		}
	}
	if strings.Join(role.Profiles, ",") != agentapp.ProfileKeyDesignJudge {
		t.Fatalf("case_judge profiles = %v, want [design_judge]", role.Profiles)
	}
	if !role.Model.Tools || !role.Model.Reasoning {
		t.Fatalf("case_judge model requirements = %+v, want tools+reasoning", role.Model)
	}
}

// TestEnsureProfilesSeedsDesignJudge pins the startup path: the judge is part
// of the aggregated seeds, lands at the current seed version, and the whole
// seeded set passes the role whitelist validation. The seed-version bump
// (judge increment) must have happened: seed 7 is the judge seed.
func TestEnsureProfilesSeedsDesignJudge(t *testing.T) {
	if agentapp.ProfileSeedVersion < 7 {
		t.Fatalf("ProfileSeedVersion = %d, want >= 7 (the design_judge seed increment)", agentapp.ProfileSeedVersion)
	}
	seeded, err := agentapp.EnsureProfiles(context.Background(), newFakeRuntimeRepo())
	if err != nil {
		t.Fatalf("EnsureProfiles: %v", err)
	}
	var judge *agent.AgentProfile
	for i := range seeded {
		if seeded[i].Key == agentapp.ProfileKeyDesignJudge {
			judge = &seeded[i]
		}
	}
	if judge == nil {
		t.Fatal("EnsureProfiles did not seed design_judge")
	}
	if judge.SeedVersion != agentapp.ProfileSeedVersion {
		t.Fatalf("judge seed version = %d, want %d", judge.SeedVersion, agentapp.ProfileSeedVersion)
	}
	if err := agentapp.ValidateRoleProfiles(seeded); err != nil {
		t.Fatalf("ValidateRoleProfiles(seeded) = %v", err)
	}
	occurrences := 0
	for _, profile := range agentapp.Profiles() {
		if profile.Key == agentapp.ProfileKeyDesignJudge {
			occurrences++
		}
	}
	if occurrences != 1 {
		t.Fatalf("design_judge appears %d times in Profiles(), want 1", occurrences)
	}
}

// TestDesignJudgeWhitelistToolsRegistered pins that every whitelisted tool is a
// registered catalog name. T13 registers design.judgment_submit in the design
// tool group, so the judgment write entry is now checked like every other name
// (the T12 forward-dependency skip is removed).
func TestDesignJudgeWhitelistToolsRegistered(t *testing.T) {
	catalog := tools.AdminCatalogRegistry()
	for _, name := range designJudgeWhitelist {
		if _, ok := catalog.Get(name); !ok {
			t.Fatalf("judge whitelist lists unregistered tool %q", name)
		}
	}
}

// TestProfileKeyForJudgeWorkTypes pins the T13 work-type mapping: the independent
// judge run resolves the design_judge profile, and the judge-fail rework reuses
// the design_author profile (the author revises the case; the judge re-judges the
// new version). Both are 0.2.1 type-path work types.
func TestProfileKeyForJudgeWorkTypes(t *testing.T) {
	if got, ok := agentapp.ProfileKeyForWorkType(agentapp.WorkTypeDesignJudge); !ok || got != agentapp.ProfileKeyDesignJudge {
		t.Fatalf("design.judge map = %q/%v, want design_judge", got, ok)
	}
	if got, ok := agentapp.ProfileKeyForWorkType(agentapp.WorkTypeDesignRework); !ok || got != agentapp.ProfileKeyDesignAuthor {
		t.Fatalf("design.rework map = %q/%v, want design_author", got, ok)
	}
	// The string literals are the published contract names (T8 task types).
	if got, _ := agentapp.ProfileKeyForWorkType("design.judge"); got != agentapp.ProfileKeyDesignJudge {
		t.Fatalf("design.judge literal map = %q", got)
	}
	if got, _ := agentapp.ProfileKeyForWorkType("design.rework"); got != agentapp.ProfileKeyDesignAuthor {
		t.Fatalf("design.rework literal map = %q", got)
	}
}

// TestBackfillReconcilesPreJudgeRows pins the seed-version bump mechanics: a
// stored row already at the pre-judge seed (6) is reconciled to the current
// marker without touching its tools, budgets or prompt (the judge increment
// adds a NEW profile; it does not widen existing ones).
//
// H-F10b #25: the loop covers EVERY pre-existing profile key, derived from the
// seed registry — not just design_author. A backfill that widened one particular
// profile's tools or rewrote its prompt would have passed the single-key version.
func TestBackfillReconcilesPreJudgeRows(t *testing.T) {
	repo := newFakeBackfillRepo()
	v022Tools := agentapp.V022ProfileTools()
	type stored struct {
		tools  string
		prompt string
	}
	before := map[string]stored{}
	for index, seed := range agentapp.Profiles() {
		seed.ID = int64(21 + index)
		seed.SeedVersion = 6
		seed.AllowedTools = append(seed.AllowedTools, v022Tools[seed.Key]...)
		repo.profiles[seed.Key] = seed
		repo.revisions[seed.Key] = []agent.PromptRevision{{
			ID: int64(1 + index), ProfileKey: seed.Key, Version: 1,
			SystemPrompt: seed.SystemPrompt, Source: agent.PromptSourceSeed,
		}}
		before[seed.Key] = stored{tools: strings.Join(seed.AllowedTools, ","), prompt: seed.SystemPrompt}
	}
	if len(before) == 0 {
		t.Fatal("the seed registry is empty; the backfill pin would go vacuous")
	}
	if err := agentapp.BackfillProfiles(context.Background(), repo, nil); err != nil {
		t.Fatalf("BackfillProfiles: %v", err)
	}
	for key, want := range before {
		got := repo.profiles[key]
		if got.SeedVersion != agentapp.ProfileSeedVersion {
			t.Errorf("%s: seed version = %d, want %d", key, got.SeedVersion, agentapp.ProfileSeedVersion)
		}
		if strings.Join(got.AllowedTools, ",") != want.tools {
			t.Errorf("%s: backfill changed the stored tools: %v", key, got.AllowedTools)
		}
		if got.SystemPrompt != want.prompt {
			t.Errorf("%s: backfill changed the stored prompt", key)
		}
	}
}
