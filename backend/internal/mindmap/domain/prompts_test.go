package domain

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPromptVersions(t *testing.T) {
	cases := map[string]string{
		PromptVersionDesign:   "p5-design-v1",
		PromptVersionReview:   "p5-review-v1",
		PromptVersionDataPlan: "p5-data-plan-v1",
		PromptVersionExplore:  "p5-explore-v1",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("prompt version = %q, want %q", got, want)
		}
	}
}

func TestPromptsContainRequiredMarkers(t *testing.T) {
	cases := []struct {
		name     string
		prompt   string
		required []string
	}{
		{
			name:   "design",
			prompt: DesignPrompt,
			required: []string{
				"case.graph_propose",   // output must go through the submit tool
				"P0", "P1", "P2", "P3", // priority definitions
				"覆盖",    // coverage requirement
				"不得编造",  // no fabricated requirement ids or file paths
				"需求 ID", // explicit prohibition target
				"文件路径",  // explicit prohibition target
				"缺少",    // explain what is missing on failure
				"只读",    // role boundary
			},
		},
		{
			name:   "review",
			prompt: ReviewPrompt,
			required: []string{
				"case.review_submit",
				"blocker",
				"severity",
				"覆盖",
				"不得编造",
				"文件路径",
				"缺少",
				"只读",
			},
		},
		{
			name:   "data-plan",
			prompt: DataPlanPrompt,
			required: []string{
				"case.data_plan_submit",
				"preconditions",
				"test_data",
				"不得编造",
				"缺少",
			},
		},
		{
			name:   "explore",
			prompt: ExplorePrompt,
			required: []string{
				"app_model_propose",
				"GET",
				"白名单",
				"不得编造",
				"缺少",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if strings.TrimSpace(tc.prompt) == "" {
				t.Fatal("prompt is empty")
			}
			if n := utf8.RuneCountInString(tc.prompt); n > 32768 {
				t.Fatalf("prompt is %d runes, want at most 32768", n)
			}
			for _, marker := range tc.required {
				if !strings.Contains(tc.prompt, marker) {
					t.Errorf("prompt does not contain required marker %q", marker)
				}
			}
			if !strings.Contains(tc.prompt, "角色") {
				t.Error("prompt does not declare the agent role")
			}
		})
	}
}
