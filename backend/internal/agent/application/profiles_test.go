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
	if len(profiles) != 1 {
		t.Fatalf("profiles = %d, want 1 (the clarification Host; Curator/Scout were removed)", len(profiles))
	}
	// W1-1 moved the unused test-only P1 constructor out of production; the
	// superset registry still registers every P1 tool name.
	registry := tools.NewP2Registry(tools.Deps{})

	host := profiles[0]
	if host.Key != agentapp.ProfileKeyClarificationHost || host.Name != "澄清 Host" {
		t.Fatalf("host profile = %#v", host)
	}
	if !strings.Contains(host.SystemPrompt, "[profile:clarification_host]") {
		t.Fatalf("host prompt missing fake marker: %s", host.SystemPrompt)
	}
	if host.Budgets.MaxTurns != 12 || host.Budgets.SoftSeconds != 600 || host.Budgets.HardSeconds != 900 {
		t.Fatalf("host budgets = %#v", host.Budgets)
	}
	if len(host.AllowedTools) != 13 {
		t.Fatalf("host tools = %v", host.AllowedTools)
	}
	// The Host now owns requirement drafting directly; the removed Curator
	// must not leave its tools unreachable.
	for _, name := range []string{
		tools.ToolRequirementDraftCreate, tools.ToolRequirementDraftUpdate,
		tools.ToolRequirementRelationPropose, tools.ToolRequirementReopenPropose,
	} {
		if !containsString(host.AllowedTools, name) {
			t.Fatalf("clarification_host is missing %q: %v", name, host.AllowedTools)
		}
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestP2ProfileSeeds(t *testing.T) {
	profiles := agentapp.P2Profiles()
	if len(profiles) != 3 {
		t.Fatalf("P2 profiles = %d, want 3 (design_host, design_author, design_critic)", len(profiles))
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
			budgets: agent.ProfileBudgets{MaxTurns: 16, SoftSeconds: 600, HardSeconds: 900},
			marker:  "[profile:design_host]",
			mustSay: []string{"不确认", "draft", "同一轮内立即", "expected", "视觉基线", "不要用 visual"},
		},
		agentapp.ProfileKeyDesignAuthor: {
			tools: []string{
				tools.ToolContextVersionPack, tools.ToolContextTaskPack, tools.ToolRequirementRead,
				tools.ToolDesignGraphRead, tools.ToolCaseGraphPropose, tools.ToolSUTList, tools.ToolSUTPageList,
			},
			budgets: agent.ProfileBudgets{MaxTurns: 16, SoftSeconds: 600, HardSeconds: 900},
			marker:  "[profile:design_author]",
			mustSay: []string{"后台作业", "draft", "不能确认", "视觉基线", "不要用 visual"},
		},
		agentapp.ProfileKeyDesignCritic: {
			tools: []string{
				tools.ToolContextTaskPack, tools.ToolDesignGraphRead, tools.ToolDesignReviewSubmit,
			},
			budgets: agent.ProfileBudgets{MaxTurns: 4, SoftSeconds: 600, HardSeconds: 900},
			marker:  "[profile:design_critic]",
			mustSay: []string{"后台作业", "内部", "不构成人工关卡", "视觉基线", "visual"},
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

// TestProfileSeedsAfterTeamConvergence pins the v0.22 D-B1 profile catalog: the
// clarification/design domain has exactly two Hosts (clarification_host,
// design_host) plus the two background jobs (design_author, design_critic), the
// removed Scout/Curator/Planner seeds are absent, and the execution/analysis
// seeds (case_executor, analysts, version_steward, report_writer,
// knowledge_curator) are untouched.
func TestProfileSeedsAfterTeamConvergence(t *testing.T) {
	for _, key := range []string{
		agentapp.ProfileKeyClarificationHost,
		agentapp.ProfileKeyDesignHost,
		agentapp.ProfileKeyDesignAuthor,
		agentapp.ProfileKeyDesignCritic,
	} {
		if _, ok := agentapp.Profile(key); !ok {
			t.Fatalf("conversation/design profile %q is missing", key)
		}
	}

	hostCount := 0
	for _, profile := range append(agentapp.P1Profiles(), agentapp.P2Profiles()...) {
		switch profile.Key {
		case agentapp.ProfileKeyClarificationHost, agentapp.ProfileKeyDesignHost:
			hostCount++
		case agentapp.ProfileKeyDesignAuthor, agentapp.ProfileKeyDesignCritic:
			// Background jobs: seeded for job runs, never a conversation Host.
		default:
			t.Fatalf("unexpected conversation/design profile %q", profile.Key)
		}
	}
	if hostCount != 2 {
		t.Fatalf("conversation Hosts = %d, want exactly 2", hostCount)
	}

	for _, removed := range []string{
		"context_scout", "requirement_curator", "design_planner", "data_planner", "case_author",
	} {
		if _, ok := agentapp.Profile(removed); ok {
			t.Fatalf("removed profile %q is still seeded", removed)
		}
	}
	for _, profile := range agentapp.ProfileSeeds() {
		if profile.Key == "context_scout" || profile.Key == "requirement_curator" ||
			profile.Key == "design_planner" || profile.Key == "data_planner" ||
			profile.Key == "case_author" {
			t.Fatalf("removed profile %q survived the convergence", profile.Key)
		}
	}

	for _, key := range []string{
		agentapp.ProfileKeyCaseExecutor,
		agentapp.ProfileKeyCoverageAnalyst,
		agentapp.ProfileKeyVersionSteward,
		agentapp.ProfileKeyFailureAnalyst,
		agentapp.ProfileKeyReportWriter,
		agentapp.ProfileKeyKnowledgeCurator,
	} {
		if _, ok := agentapp.Profile(key); !ok {
			t.Fatalf("execution/analysis profile %q must be retained", key)
		}
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
		agentapp.WorkTypeDesignRequirement: agentapp.ProfileKeyDesignAuthor,
		agentapp.WorkTypeDesignGap:         agentapp.ProfileKeyDesignAuthor,
		agentapp.WorkTypeDesignCritic:      agentapp.ProfileKeyDesignCritic,
		agent.WorkItemKindDesignCases:      agentapp.ProfileKeyDesignAuthor,
		agent.WorkItemKindReviewCases:      agentapp.ProfileKeyDesignCritic,
	}
	for workType, want := range cases {
		got, ok := agentapp.ProfileKeyForWorkType(workType)
		if !ok || got != want {
			t.Fatalf("ProfileKeyForWorkType(%q) = %q/%v, want %q", workType, got, ok, want)
		}
	}
	// The Planner/Curator/Scout work types were removed with their profiles.
	for _, removed := range []string{
		agentapp.WorkTypeDesignRound, agentapp.WorkTypeDesignDataPlan,
		agentapp.WorkTypeRequirementCurate, "attachment.ingest",
	} {
		if _, ok := agentapp.ProfileKeyForWorkType(removed); ok {
			t.Fatalf("removed work type %q still maps to a profile", removed)
		}
	}
	for _, reserved := range []string{"report.render_pdf", "case.confirm"} {
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
	wantTotal := len(agentapp.P1Profiles()) + len(agentapp.P2Profiles()) + len(agentapp.P3Profiles()) +
		len(agentapp.P4Profiles()) + len(agentapp.P7Profiles())
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
