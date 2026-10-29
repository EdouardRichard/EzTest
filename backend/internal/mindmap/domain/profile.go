package domain

// AgentProfileSeed is one idempotent P5 agent-profile seed (spec §8.1). It is
// the domain shape consumed by the agent runtime adapter; the P1 profile row is
// only created when the key does not exist yet, so an admin can iterate the
// prompt, model and tools through the existing P1 admin API.
type AgentProfileSeed struct {
	Key         string
	Name        string
	Description string
	Builtin     bool
	Enabled     bool

	SystemPrompt       string
	Model              string
	AllowedTools       []string
	MaxTurns           int
	RunSeconds         int
	HardTimeoutSeconds int
	Thinking           string
	ReasoningEffort    string
	MaxOutputTokens    int
}

// P5 profile keys, exactly as specified by design spec §8.1.
const (
	ProfileKeyCaseDesign     = "case-design"
	ProfileKeyCaseReview     = "case-review"
	ProfileKeyCaseDataPlan   = "case-data-plan"
	ProfileKeyAppExploration = "app-exploration"
)

// P5Profiles returns the four P5 seed profiles exactly as specified by design
// spec §8.1: keys, display names, tool allowlists and budgets. All seeds are
// Builtin=false so the P1 builtin lookup can never select one as the runtime's
// default profile. Every call returns fresh slices so callers may keep or modify
// the result without affecting later seeds.
func P5Profiles() []AgentProfileSeed {
	return []AgentProfileSeed{
		{
			Key:          ProfileKeyCaseDesign,
			Name:         "测试设计 Agent",
			Builtin:      false,
			Enabled:      true,
			SystemPrompt: DesignPrompt,
			Model:        "",
			AllowedTools: []string{
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
			MaxTurns:           24,
			RunSeconds:         1200,
			HardTimeoutSeconds: 90,
			Thinking:           "enabled",
			ReasoningEffort:    "max",
			MaxOutputTokens:    32768,
		},
		{
			Key:          ProfileKeyCaseReview,
			Name:         "设计评审 Agent",
			Builtin:      false,
			Enabled:      true,
			SystemPrompt: ReviewPrompt,
			Model:        "",
			AllowedTools: []string{
				"version.context_get",
				"case.graph_read",
				"code.facts",
				"code.routes",
				"code.apis",
				"code.read",
				"case.review_submit",
			},
			MaxTurns:           16,
			RunSeconds:         900,
			HardTimeoutSeconds: 90,
			Thinking:           "auto",
			ReasoningEffort:    "",
			MaxOutputTokens:    32768,
		},
		{
			Key:          ProfileKeyCaseDataPlan,
			Name:         "数据/前置规划 Agent",
			Builtin:      false,
			Enabled:      true,
			SystemPrompt: DataPlanPrompt,
			Model:        "",
			AllowedTools: []string{
				"version.context_get",
				"case.graph_read",
				"case.data_plan_submit",
			},
			MaxTurns:           10,
			RunSeconds:         600,
			HardTimeoutSeconds: 60,
			Thinking:           "auto",
			ReasoningEffort:    "",
			MaxOutputTokens:    16384,
		},
		{
			Key:          ProfileKeyAppExploration,
			Name:         "应用探索 Agent",
			Builtin:      false,
			Enabled:      true,
			SystemPrompt: ExplorePrompt,
			Model:        "",
			AllowedTools: []string{
				"explore.http",
				"app_model_propose",
				"code.routes",
				"code.apis",
			},
			MaxTurns:           16,
			RunSeconds:         600,
			HardTimeoutSeconds: 60,
			Thinking:           "auto",
			ReasoningEffort:    "",
			MaxOutputTokens:    16384,
		},
	}
}
