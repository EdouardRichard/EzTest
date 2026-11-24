package agentapp_test

import (
	"reflect"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

func TestProfileKeyForWorkTypeDelivery(t *testing.T) {
	cases := map[string]string{
		agentapp.WorkTypeIssueTriage:   agentapp.ProfileKeyIssueTriager,
		agentapp.WorkTypeReportWave:    agentapp.ProfileKeyReportWriter,
		agentapp.WorkTypeReportVersion: agentapp.ProfileKeyReportWriter,
	}
	for workType, want := range cases {
		got, ok := agentapp.ProfileKeyForWorkType(workType)
		if !ok || got != want {
			t.Fatalf("ProfileKeyForWorkType(%q) = %q/%v, want %q", workType, got, ok, want)
		}
	}
}

func TestDeliveryProfilesMatchContract(t *testing.T) {
	profiles := agentapp.DeliveryProfiles()
	if len(profiles) != 2 {
		t.Fatalf("delivery profiles = %d, want 2 (GD-17 removed version_steward)", len(profiles))
	}

	type expectation struct {
		name    string
		tools   []string
		budgets agent.ProfileBudgets
		marker  string
		mustSay []string
	}
	want := map[string]expectation{
		agentapp.ProfileKeyIssueTriager: {
			name: "问题分诊",
			tools: []string{
				tools.ToolContextTaskPack,
				tools.ToolContextTeamRoomRead,
				"issue.read",
				"issue.propose",
			},
			budgets: agent.ProfileBudgets{MaxTurns: 6, SoftSeconds: 180, HardSeconds: 300},
			marker:  "[profile:issue_triager]",
			mustSay: []string{"不伪造", "不改状态"},
		},
		agentapp.ProfileKeyReportWriter: {
			name: "报告撰写",
			tools: []string{
				tools.ToolContextTaskPack,
				tools.ToolContextTeamRoomRead,
				"report.data_read",
				"report.narrative_propose",
			},
			budgets: agent.ProfileBudgets{MaxTurns: 6, SoftSeconds: 180, HardSeconds: 300},
			marker:  "[profile:report_writer]",
			mustSay: []string{"统计", "叙述"},
		},
	}

	seen := map[string]bool{}
	for _, profile := range profiles {
		expected, ok := want[profile.Key]
		if !ok {
			t.Fatalf("unexpected delivery profile %q", profile.Key)
		}
		seen[profile.Key] = true
		if profile.Name != expected.name {
			t.Fatalf("profile %s name = %q, want %q", profile.Key, profile.Name, expected.name)
		}
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
		for _, forbidden := range []string{"case.confirm", "requirement.draft_create", "issue.merge"} {
			for _, name := range profile.AllowedTools {
				if name == forbidden {
					t.Fatalf("profile %s must not expose %q", profile.Key, forbidden)
				}
			}
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("seen delivery profiles = %v", seen)
	}
}

// TestDeliveryProfilePromptsPinGuardrailClause pins the frozen P4 prompt constraint
// from contracts/agent-profiles-tools.md §7.1 in every P4 seed prompt.
func TestDeliveryProfilePromptsPinGuardrailClause(t *testing.T) {
	const clause = "只调用白名单工具；不得伪造统计/证据；不得尝试确认需求/用例、合并/重开问题或直接改版本状态。"

	profiles := agentapp.DeliveryProfiles()
	if len(profiles) != 2 {
		t.Fatalf("delivery profiles = %d, want 2 (GD-17 removed version_steward)", len(profiles))
	}
	for _, profile := range profiles {
		if !strings.Contains(profile.SystemPrompt, clause) {
			t.Errorf("profile %s prompt missing guardrail clause %q: %s", profile.Key, clause, profile.SystemPrompt)
		}
	}
}

func TestProfilesMergesDeliveryWithoutDuplicatingOrOverwriting(t *testing.T) {
	base := append(agentapp.BaseProfiles(), agentapp.DesignProfiles()...)
	base = append(base, agentapp.ExecutionProfiles()...)
	delivery := agentapp.DeliveryProfiles()
	knowledge := agentapp.KnowledgeProfiles()
	judge := agentapp.JudgeProfiles()
	merged := agentapp.Profiles()

	if len(merged) != len(base)+len(delivery)+len(knowledge)+len(judge) {
		t.Fatalf("merged profiles = %d, want %d", len(merged), len(base)+len(delivery)+len(knowledge)+len(judge))
	}
	for i := range base {
		if !reflect.DeepEqual(merged[i], base[i]) {
			t.Fatalf("Profiles() overwrote existing profile at %d: %#v != %#v", i, merged[i], base[i])
		}
	}
	for i := range delivery {
		if !reflect.DeepEqual(merged[len(base)+i], delivery[i]) {
			t.Fatalf("Profiles() delivery profile at %d = %#v, want %#v", len(base)+i, merged[len(base)+i], delivery[i])
		}
	}
	for i := range knowledge {
		index := len(base) + len(delivery) + i
		if !reflect.DeepEqual(merged[index], knowledge[i]) {
			t.Fatalf("Profiles() knowledge profile at %d = %#v, want %#v", index, merged[index], knowledge[i])
		}
	}
	for i := range judge {
		index := len(base) + len(delivery) + len(knowledge) + i
		if !reflect.DeepEqual(merged[index], judge[i]) {
			t.Fatalf("Profiles() judge profile at %d = %#v, want %#v", index, merged[index], judge[i])
		}
	}
	seen := map[string]bool{}
	for _, profile := range merged {
		if seen[profile.Key] {
			t.Fatalf("Profiles() duplicated profile key %q", profile.Key)
		}
		seen[profile.Key] = true
	}
}
