package agentapp_test

import (
	"context"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

func TestP1ProfilesMatchContract(t *testing.T) {
	profiles := agentapp.P1Profiles()
	if len(profiles) != 3 {
		t.Fatalf("profiles = %d, want 3", len(profiles))
	}
	registry := tools.NewP1Registry(tools.Deps{})

	host := profiles[0]
	if host.Key != agentapp.ProfileKeyClarificationHost || host.Name != "澄清 Host" {
		t.Fatalf("host profile = %#v", host)
	}
	if !strings.Contains(host.SystemPrompt, "[profile:clarification_host]") {
		t.Fatalf("host prompt missing fake marker: %s", host.SystemPrompt)
	}
	if host.Budgets.MaxTurns != 12 || host.Budgets.SoftSeconds != 300 || host.Budgets.HardSeconds != 600 {
		t.Fatalf("host budgets = %#v", host.Budgets)
	}
	if len(host.AllowedTools) != 9 {
		t.Fatalf("host tools = %v", host.AllowedTools)
	}

	curator := profiles[1]
	if curator.Key != agentapp.ProfileKeyRequirementCurator || len(curator.AllowedTools) != 12 {
		t.Fatalf("curator profile = %#v", curator)
	}
	if !strings.Contains(curator.SystemPrompt, "[profile:requirement_curator]") {
		t.Fatalf("curator prompt missing fake marker")
	}

	scout := profiles[2]
	if scout.Key != agentapp.ProfileKeyContextScout || len(scout.AllowedTools) != 6 {
		t.Fatalf("scout profile = %#v", scout)
	}
	if !strings.Contains(scout.SystemPrompt, "[profile:context_scout]") {
		t.Fatalf("scout prompt missing fake marker")
	}

	for _, profile := range profiles {
		if !profile.Enabled {
			t.Fatalf("profile %s is not enabled by default", profile.Key)
		}
		for _, name := range profile.AllowedTools {
			if _, ok := registry.Get(name); !ok {
				t.Fatalf("profile %s lists unregistered tool %q", profile.Key, name)
			}
		}
	}
}

func TestP2ProfileSeeds(t *testing.T) {
	profiles := agentapp.P2Profiles()
	if len(profiles) != 5 {
		t.Fatalf("P2 profiles = %d, want 5", len(profiles))
	}
	registry := tools.NewP2Registry(tools.Deps{})

	type expectation struct {
		tools   []string
		budgets agent.ProfileBudgets
		marker  string
		mustSay []string
	}
	want := map[string]expectation{
		agentapp.ProfileKeyDesignHost: {
			tools: []string{
				tools.ToolContextVersionPack, tools.ToolContextTaskPack, tools.ToolContextTeamRoomRead,
				tools.ToolContextSummaryUpdate, tools.ToolRequirementRead, tools.ToolDesignGraphRead,
				tools.ToolCaseGraphPropose, tools.ToolAttachmentList, tools.ToolAttachmentReadText,
				tools.ToolSUTList, tools.ToolSUTPageList,
			},
			budgets: agent.ProfileBudgets{MaxTurns: 12, SoftSeconds: 300, HardSeconds: 600},
			marker:  "[profile:design_host]",
			mustSay: []string{"不确认", "draft"},
		},
		agentapp.ProfileKeyDesignPlanner: {
			tools: []string{
				tools.ToolContextVersionPack, tools.ToolContextTaskPack, tools.ToolContextTeamRoomRead,
				tools.ToolRequirementRead, tools.ToolDesignGraphRead,
			},
			budgets: agent.ProfileBudgets{MaxTurns: 6, SoftSeconds: 180, HardSeconds: 300},
			marker:  "[profile:design_planner]",
			mustSay: []string{"范围"},
		},
		agentapp.ProfileKeyCaseAuthor: {
			tools: []string{
				tools.ToolContextVersionPack, tools.ToolContextTaskPack, tools.ToolRequirementRead,
				tools.ToolDesignGraphRead, tools.ToolCaseGraphPropose, tools.ToolSUTList, tools.ToolSUTPageList,
			},
			budgets: agent.ProfileBudgets{MaxTurns: 8, SoftSeconds: 300, HardSeconds: 600},
			marker:  "[profile:case_author]",
			mustSay: []string{"draft", "不能确认"},
		},
		agentapp.ProfileKeyDesignCritic: {
			tools: []string{
				tools.ToolContextTaskPack, tools.ToolDesignGraphRead, tools.ToolDesignReviewSubmit,
			},
			budgets: agent.ProfileBudgets{MaxTurns: 4, SoftSeconds: 120, HardSeconds: 300},
			marker:  "[profile:design_critic]",
			mustSay: []string{"内部", "不构成人工关卡"},
		},
		agentapp.ProfileKeyDataPlanner: {
			tools: []string{
				tools.ToolContextTaskPack, tools.ToolDesignGraphRead, tools.ToolDesignDataPlanSubmit,
			},
			budgets: agent.ProfileBudgets{MaxTurns: 4, SoftSeconds: 120, HardSeconds: 300},
			marker:  "[profile:data_planner]",
			mustSay: []string{"draft"},
		},
	}

	seen := map[string]bool{}
	for _, profile := range profiles {
		expected, ok := want[profile.Key]
		if !ok {
			t.Fatalf("unexpected P2 profile %q", profile.Key)
		}
		seen[profile.Key] = true
		if !profile.Enabled {
			t.Fatalf("profile %s is not enabled by default", profile.Key)
		}
		if strings.Join(profile.AllowedTools, ",") != strings.Join(expected.tools, ",") {
			t.Fatalf("profile %s tools = %v, want %v", profile.Key, profile.AllowedTools, expected.tools)
		}
		if profile.Budgets != expected.budgets {
			t.Fatalf("profile %s budgets = %#v, want %#v", profile.Key, profile.Budgets, expected.budgets)
		}
		if !strings.Contains(profile.SystemPrompt, expected.marker) {
			t.Fatalf("profile %s prompt missing fake marker %q", profile.Key, expected.marker)
		}
		for _, phrase := range expected.mustSay {
			if !strings.Contains(profile.SystemPrompt, phrase) {
				t.Fatalf("profile %s prompt must contain %q: %s", profile.Key, phrase, profile.SystemPrompt)
			}
		}
		for _, name := range profile.AllowedTools {
			if _, ok := registry.Get(name); !ok {
				t.Fatalf("profile %s lists unregistered tool %q", profile.Key, name)
			}
		}
		if _, ever := agentapp.ProfileKeyForWorkType("case.confirm"); ever {
			t.Fatal("case.confirm must not map to a profile")
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("seen profiles = %v", seen)
	}
}

func TestProfileKeyForConversationKind(t *testing.T) {
	cases := map[string]string{
		"clarification": agentapp.ProfileKeyClarificationHost,
		"design":        agentapp.ProfileKeyDesignHost,
	}
	for kind, want := range cases {
		got, ok := agentapp.ProfileKeyForConversationKind(kind)
		if !ok || got != want {
			t.Fatalf("ProfileKeyForConversationKind(%q) = %q/%v, want %q", kind, got, ok, want)
		}
	}
	if _, ok := agentapp.ProfileKeyForConversationKind("bogus"); ok {
		t.Fatal("unknown conversation kind mapped to a profile")
	}
}

func TestProfileKeyForWorkTypeP2(t *testing.T) {
	cases := map[string]string{
		agentapp.WorkTypeDesignRound:       agentapp.ProfileKeyDesignPlanner,
		agentapp.WorkTypeDesignRequirement: agentapp.ProfileKeyCaseAuthor,
		agentapp.WorkTypeDesignGap:         agentapp.ProfileKeyCaseAuthor,
		agentapp.WorkTypeDesignCritic:      agentapp.ProfileKeyDesignCritic,
		agentapp.WorkTypeDesignDataPlan:    agentapp.ProfileKeyDataPlanner,
	}
	for workType, want := range cases {
		got, ok := agentapp.ProfileKeyForWorkType(workType)
		if !ok || got != want {
			t.Fatalf("ProfileKeyForWorkType(%q) = %q/%v, want %q", workType, got, ok, want)
		}
	}
	for _, reserved := range []string{"execute.case", "report.wave", "case.confirm"} {
		if _, ok := agentapp.ProfileKeyForWorkType(reserved); ok {
			t.Fatalf("reserved work type %q mapped to a P2 profile", reserved)
		}
	}
}

func TestEnsureProfilesIsIdempotentAndPreservesAdminEdits(t *testing.T) {
	repo := newFakeRuntimeRepo()
	ctx := context.Background()

	first, err := agentapp.EnsureProfiles(ctx, repo)
	if err != nil {
		t.Fatalf("EnsureProfiles: %v", err)
	}
	wantTotal := len(agentapp.P1Profiles()) + len(agentapp.P2Profiles())
	if len(first) != wantTotal {
		t.Fatalf("seeded = %d, want %d", len(first), wantTotal)
	}
	ids := map[string]int64{}
	for _, profile := range first {
		ids[profile.Key] = profile.ID
	}

	seed := repo.profiles[agentapp.ProfileKeyClarificationHost]
	seed.Name = "管理员改过的 Host"
	repo.profiles[agentapp.ProfileKeyClarificationHost] = seed

	second, err := agentapp.EnsureProfiles(ctx, repo)
	if err != nil {
		t.Fatalf("second EnsureProfiles: %v", err)
	}
	if len(second) != wantTotal {
		t.Fatalf("second seed = %d, want %d", len(second), wantTotal)
	}
	for _, profile := range second {
		if profile.ID != ids[profile.Key] {
			t.Fatalf("profile %s id changed: %d != %d", profile.Key, profile.ID, ids[profile.Key])
		}
	}
	if second[0].Name != "管理员改过的 Host" {
		t.Fatalf("admin edit overwritten: %q", second[0].Name)
	}
}
