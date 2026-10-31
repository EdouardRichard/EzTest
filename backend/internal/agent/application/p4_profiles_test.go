package agentapp_test

import (
	"reflect"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

func TestProfileKeyForWorkTypeP4(t *testing.T) {
	cases := map[string]string{
		agentapp.WorkTypeIssueTriage:      agentapp.ProfileKeyIssueTriager,
		agentapp.WorkTypeReportWave:       agentapp.ProfileKeyReportWriter,
		agentapp.WorkTypeReportVersion:    agentapp.ProfileKeyReportWriter,
		agentapp.WorkTypeRequirementClose: agentapp.ProfileKeyVersionSteward,
		agentapp.WorkTypeVersionClose:     agentapp.ProfileKeyVersionSteward,
	}
	for workType, want := range cases {
		got, ok := agentapp.ProfileKeyForWorkType(workType)
		if !ok || got != want {
			t.Fatalf("ProfileKeyForWorkType(%q) = %q/%v, want %q", workType, got, ok, want)
		}
	}
}

func TestP4ProfilesMatchContract(t *testing.T) {
	profiles := agentapp.P4Profiles()
	if len(profiles) != 3 {
		t.Fatalf("P4 profiles = %d, want 3", len(profiles))
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
		agentapp.ProfileKeyVersionSteward: {
			name: "版本管家",
			tools: []string{
				tools.ToolContextTaskPack,
				tools.ToolContextTeamRoomRead,
				tools.ToolRequirementRead,
				"version.close_read",
				"requirement.goal_met",
				"version.close_propose",
			},
			budgets: agent.ProfileBudgets{MaxTurns: 6, SoftSeconds: 180, HardSeconds: 300},
			marker:  "[profile:version_steward]",
			mustSay: []string{"复核", "缺失项"},
		},
	}

	seen := map[string]bool{}
	for _, profile := range profiles {
		expected, ok := want[profile.Key]
		if !ok {
			t.Fatalf("unexpected P4 profile %q", profile.Key)
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
		t.Fatalf("seen P4 profiles = %v", seen)
	}
}

func TestProfilesMergesP4WithoutDuplicatingOrOverwriting(t *testing.T) {
	base := append(agentapp.P1Profiles(), agentapp.P2Profiles()...)
	base = append(base, agentapp.P3Profiles()...)
	p4 := agentapp.P4Profiles()
	merged := agentapp.Profiles()

	if len(merged) != len(base)+len(p4) {
		t.Fatalf("merged profiles = %d, want %d", len(merged), len(base)+len(p4))
	}
	for i := range base {
		if !reflect.DeepEqual(merged[i], base[i]) {
			t.Fatalf("Profiles() overwrote existing profile at %d: %#v != %#v", i, merged[i], base[i])
		}
	}
	for i := range p4 {
		if !reflect.DeepEqual(merged[len(base)+i], p4[i]) {
			t.Fatalf("Profiles() P4 profile at %d = %#v, want %#v", len(base)+i, merged[len(base)+i], p4[i])
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
