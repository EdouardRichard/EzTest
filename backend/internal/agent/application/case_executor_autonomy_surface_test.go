package agentapp_test

// case_executor autonomy surface (v0.2.4-F T15, SC-F2/F3/F4/F5/F11, ruling P18).
// The F domain tasks (T9/T10/T11/T13) registered sut.read / sut.register /
// sut.asset_register / fixture.upload / fixture.download in the runtime registry
// and the admin catalog, but deliberately left the case_executor profile
// whitelist to the consolidation task. This test pins the LAST wiring leg: the
// seeded whitelist must expose all five tools AND the runtime registry must
// actually contain them when the production ports are wired — the intersection
// (ToolSet.Names) is what the model can see and call. The prompt must teach the
// same tools (model-facing contract surface ⑤).
//
// Stub discipline (19 §4 method ⑨): the resource stub embeds a nil ResourcePort,
// so any port method the registration path does not need panics instead of
// silently supplying a fiat answer; only the additive SUTReader surface is
// implemented. The registrar/library stubs answer with empty candidates — this
// test exercises reachability (whitelist ∩ registry), not tool semantics (pinned
// by the tools-package tests).

import (
	"context"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

type autonomyResourcePort struct {
	tools.ResourcePort // nil embedded: unused methods panic (never called at registration)
}

func (autonomyResourcePort) ReadSUT(context.Context, int64, int64) (tools.SUTDetail, error) {
	return tools.SUTDetail{}, nil
}

type autonomyAssetRegistrar struct{}

func (autonomyAssetRegistrar) RegisterAsset(context.Context, tools.Scope, tools.AssetRegisterInput) (any, error) {
	return map[string]any{}, nil
}

type autonomySUTRegistrar struct{}

func (autonomySUTRegistrar) RegisterSUT(context.Context, tools.Scope, tools.SUTRegisterInput) (any, error) {
	return map[string]any{}, nil
}

type autonomyFixtureLibrary struct{}

func (autonomyFixtureLibrary) UploadFixture(context.Context, tools.Scope, tools.FixtureUploadInput) (any, error) {
	return map[string]any{}, nil
}

func (autonomyFixtureLibrary) DownloadFixture(context.Context, tools.Scope, tools.FixtureDownloadInput) (any, error) {
	return map[string]any{}, nil
}

// autonomyToolNames is the F whitelist increment (contract: spec SC-F2/F3/F4/F5
// name these tools as the executor's autonomous-loop surface).
var autonomyToolNames = []string{
	tools.ToolSUTRead,
	tools.ToolSUTRegister,
	tools.ToolSUTAssetRegister,
	tools.ToolFixtureUpload,
	tools.ToolFixtureDownload,
}

func TestCaseExecutorAutonomyToolsCallable(t *testing.T) {
	registry := tools.NewKnowledgeRegistry(tools.Deps{
		Resource:          autonomyResourcePort{},
		AssetRegistration: tools.AssetRegistrationDeps{Registrar: autonomyAssetRegistrar{}},
		SUTRegistration:   tools.SUTRegistrationDeps{Registrar: autonomySUTRegistrar{}},
		SessionFixtures:   tools.SessionFixtureDeps{Library: autonomyFixtureLibrary{}},
	})
	var allowed []string
	found := false
	for _, profile := range agentapp.ExecutionProfiles() {
		if profile.Key == agentapp.ProfileKeyCaseExecutor {
			allowed = profile.AllowedTools
			found = true
			break
		}
	}
	if !found {
		t.Fatal("case_executor profile is not seeded")
	}
	set := tools.NewToolSet("case_executor", allowed)
	visible := make(map[string]bool)
	for _, name := range set.Names(registry) {
		visible[name] = true
	}
	for _, name := range autonomyToolNames {
		if _, registered := registry.Get(name); !registered {
			t.Fatalf("%s is not in the runtime registry with wired ports (registration leg broken)", name)
		}
		if !set.Allows(name) {
			t.Fatalf("case_executor whitelist does not allow %s (execution_profiles.go caseExecutorTools)", name)
		}
		if !visible[name] {
			t.Fatalf("%s is not visible to case_executor (whitelist ∩ registry)", name)
		}
	}
}

func TestCaseExecutorPromptTeachesAutonomyLoop(t *testing.T) {
	for _, profile := range agentapp.ExecutionProfiles() {
		if profile.Key != agentapp.ProfileKeyCaseExecutor {
			continue
		}
		for _, name := range autonomyToolNames {
			if !strings.Contains(profile.SystemPrompt, name) {
				t.Fatalf("case_executor prompt does not teach %s (model-facing surface ⑤)", name)
			}
		}
		// The prompt must teach the resolution order and the proof-before-register
		// rule, not merely name the tools (spec SC-F2/F3; design §6.2/§6.3).
		for _, want := range []string{"state-save", "state-load", "evidence_refs"} {
			if !strings.Contains(profile.SystemPrompt, want) {
				t.Fatalf("case_executor prompt is missing the %s guidance", want)
			}
		}
		return
	}
	t.Fatal("case_executor profile is not seeded")
}

// TestBackfillSeed7RowGainsAutonomyToolsAndPrompt pins the production upgrade
// path for EXISTING deployments (constitution art.7.5: a whitelist increment
// that only reaches fresh rows is "code present but unusable"). A row already
// reconciled to seed 7 — with either historical prompt body (the pre-T12 skill
// era body T12 left unregistered, or the T12 natural-language body) and a
// pre-F whitelist — must gain the five autonomy tools (derived increment union,
// admin additions preserved) and the current autonomous-loop prompt.
func TestBackfillSeed7RowGainsAutonomyToolsAndPrompt(t *testing.T) {
	for _, body := range []string{
		agentapp.RetiredSeed7PreT12CaseExecutorBody,
		agentapp.RetiredSeed7T12CaseExecutorBody,
	} {
		repo := newFakeBackfillRepo()
		profile := plantCaseExecutorRow(repo, 7, body, agent.PromptSourceSeed)
		profile.Budgets = agent.ProfileBudgets{MaxTurns: 0, SoftSeconds: 1200, HardSeconds: 2400}
		profile.AllowedTools = []string{
			"context.task_pack", "sut.list", "browser.cli", "exec.*",
			"http.request", "db.query", "execution.step_record", "admin.custom_tool",
		}
		repo.profiles[profile.Key] = profile
		audit := &fakeProfileAudit{}

		if err := agentapp.BackfillProfiles(context.Background(), repo, audit); err != nil {
			t.Fatalf("BackfillProfiles: %v", err)
		}
		got := repo.profiles[profile.Key]
		for _, name := range autonomyToolNames {
			if !toolListContains(got.AllowedTools, name) {
				t.Fatalf("seed-7 row did not gain %s: %v", name, got.AllowedTools)
			}
		}
		if !toolListContains(got.AllowedTools, "admin.custom_tool") {
			t.Fatalf("backfill dropped an admin-added tool: %v", got.AllowedTools)
		}
		current, _ := agentapp.DefaultPrompt(agentapp.ProfileKeyCaseExecutor)
		if got.SystemPrompt != current {
			t.Fatalf("seed-7 prompt was not upgraded to the autonomous-loop seed:\n%s", got.SystemPrompt)
		}
		if got.SeedVersion != agentapp.ProfileSeedVersion {
			t.Fatalf("seed version = %d, want %d", got.SeedVersion, agentapp.ProfileSeedVersion)
		}
		revisions := repo.revisions[profile.Key]
		latest := revisions[len(revisions)-1]
		if latest.Source != agent.PromptSourceSeed || latest.SystemPrompt != current {
			t.Fatalf("latest prompt revision is not the appended seed upgrade: %+v", latest)
		}
	}
}
