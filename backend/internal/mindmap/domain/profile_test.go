package domain

import (
	"reflect"
	"testing"
)

func TestP5ProfilesSeeds(t *testing.T) {
	seeds := P5Profiles()
	if len(seeds) != 4 {
		t.Fatalf("P5Profiles() returned %d seeds, want 4", len(seeds))
	}

	want := []struct {
		key          string
		name         string
		prompt       string
		tools        []string
		maxTurns     int
		runSeconds   int
		hardTimeout  int
		maxOutTokens int
	}{
		{
			key:    "case-design",
			name:   "测试设计 Agent",
			prompt: DesignPrompt,
			tools: []string{
				"version.context_get",
				"case.graph_read",
				"code.search",
				"code.read",
				"code.facts",
				"code.routes",
				"code.apis",
				"code.diff",
				"code.impact",
				"case.graph_propose",
			},
			maxTurns:     24,
			runSeconds:   1200,
			hardTimeout:  90,
			maxOutTokens: 32768,
		},
		{
			key:    "case-review",
			name:   "设计评审 Agent",
			prompt: ReviewPrompt,
			tools: []string{
				"version.context_get",
				"case.graph_read",
				"code.facts",
				"code.routes",
				"code.apis",
				"code.read",
				"case.review_submit",
			},
			maxTurns:     16,
			runSeconds:   900,
			hardTimeout:  90,
			maxOutTokens: 32768,
		},
		{
			key:    "case-data-plan",
			name:   "数据/前置规划 Agent",
			prompt: DataPlanPrompt,
			tools: []string{
				"version.context_get",
				"case.graph_read",
				"case.data_plan_submit",
			},
			maxTurns:     10,
			runSeconds:   600,
			hardTimeout:  60,
			maxOutTokens: 16384,
		},
		{
			key:    "app-exploration",
			name:   "应用探索 Agent",
			prompt: ExplorePrompt,
			tools: []string{
				"explore.http",
				"app_model_propose",
				"code.routes",
				"code.apis",
			},
			maxTurns:     16,
			runSeconds:   600,
			hardTimeout:  60,
			maxOutTokens: 16384,
		},
	}

	for i, expected := range want {
		seed := seeds[i]
		if seed.Key != expected.key {
			t.Errorf("seed[%d].Key = %q, want %q", i, seed.Key, expected.key)
		}
		if seed.Name != expected.name {
			t.Errorf("%s.Name = %q, want %q", expected.key, seed.Name, expected.name)
		}
		if seed.SystemPrompt != expected.prompt {
			t.Errorf("%s.SystemPrompt does not match the prompt constant", expected.key)
		}
		if !reflect.DeepEqual(seed.AllowedTools, expected.tools) {
			t.Errorf("%s.AllowedTools = %v, want %v", expected.key, seed.AllowedTools, expected.tools)
		}
		if seed.MaxTurns != expected.maxTurns {
			t.Errorf("%s.MaxTurns = %d, want %d", expected.key, seed.MaxTurns, expected.maxTurns)
		}
		if seed.RunSeconds != expected.runSeconds {
			t.Errorf("%s.RunSeconds = %d, want %d", expected.key, seed.RunSeconds, expected.runSeconds)
		}
		if seed.HardTimeoutSeconds != expected.hardTimeout {
			t.Errorf("%s.HardTimeoutSeconds = %d, want %d", expected.key, seed.HardTimeoutSeconds, expected.hardTimeout)
		}
		if seed.MaxOutputTokens != expected.maxOutTokens {
			t.Errorf("%s.MaxOutputTokens = %d, want %d", expected.key, seed.MaxOutputTokens, expected.maxOutTokens)
		}
		if seed.Builtin {
			t.Errorf("%s.Builtin = true, want false", expected.key)
		}
		if !seed.Enabled {
			t.Errorf("%s.Enabled = false, want true", expected.key)
		}
	}
}

func TestP5DesignProfileThinking(t *testing.T) {
	seeds := P5Profiles()
	design := seeds[0]
	if design.Thinking != "enabled" {
		t.Errorf("case-design.Thinking = %q, want enabled", design.Thinking)
	}
	if design.ReasoningEffort != "max" {
		t.Errorf("case-design.ReasoningEffort = %q, want max", design.ReasoningEffort)
	}
}

func TestP5ProfilesReturnsFreshSlices(t *testing.T) {
	first := P5Profiles()
	first[0].AllowedTools[0] = "mutated"
	first[0].SystemPrompt = "mutated"

	second := P5Profiles()
	if second[0].AllowedTools[0] != "version.context_get" {
		t.Errorf("AllowedTools were shared between calls: %v", second[0].AllowedTools)
	}
	if second[0].SystemPrompt == "mutated" {
		t.Error("SystemPrompt was shared between calls")
	}
}
