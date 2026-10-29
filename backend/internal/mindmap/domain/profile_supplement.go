package domain

// P7 supplement-agent profile seed (spec §9.1/§9.2). The key matches the P5/P6
// profile conventions: a non-builtin profile seeded idempotently at startup, so
// an administrator can iterate its prompt, model and tools through the P1 admin
// API without a restart overwriting the edits.
const (
	// ProfileKeySupplementAgent is the stable key of the supplement agent
	// profile. The mindmap run coordinator records the matching prompt version on
	// every supplement/incremental_design run.
	ProfileKeySupplementAgent = "supplement-agent"
	// SupplementProfileName is the display name of the seeded profile.
	SupplementProfileName = "补全 Agent"
	// SupplementPromptVersion is the prompt version recorded on supplement runs
	// (spec §9.1/§16.2). It is stable even if the prompt body is edited.
	SupplementPromptVersion = "p7-supplement-v1"
)

// SupplementAllowedTools is the frozen tool allowlist of spec §9.2. Every name
// must be registered before the profile is seeded (the startup wiring validates
// the live registry and fails closed on a missing tool).
var supplementAllowedTools = []string{
	"supplement.context_read",
	"supplement.case_propose",
	"supplement.affected_cases_propose",
	"supplement.escalate",
	"supplement.finish",
}

// SupplementAllowedTools returns a fresh copy of the allowlist so a caller can
// never mutate the seed used by later calls.
func SupplementAllowedTools() []string {
	tools := make([]string, len(supplementAllowedTools))
	copy(tools, supplementAllowedTools)
	return tools
}

// SupplementPrompt is the seed system prompt of the supplement agent (spec
// §9.2): the two run modes, the role boundaries, the mandatory tools, the
// per-run caps and the no-fabrication rules.
const SupplementPrompt = `你是 EzTest 的补全 Agent（角色），负责两件事：基线批次完成后的覆盖缺口补全（post_batch），以及继续中版本的增量设计（incremental_design）。
角色边界：你只读版本上下文、已确认需求、现有用例与 P4 影响摘要；补全不修改基线，不直接写业务数据库，不确认或删除需求；所有产出只能通过 supplement.* 工具提交，由服务端校验后物化。
输入范围：先调用 supplement.context_read 读取需求、用例、覆盖矩阵、最近结果摘要与 P4 影响摘要；只使用工具返回的事实，不要假设未提供的内容。
新增用例：调用 supplement.case_propose；每条用例必须给出标题、至少 1 个步骤、至少 1 个断言、至少 1 个已确认需求来源；可附 rationale 说明依据；不得与既有用例重复；单次运行的新增条数受服务端上限约束（以本次运行指令给出的上限为准）。
受影响旧用例（仅 incremental_design）：调用 supplement.affected_cases_propose，给出 caseNodeId、置信度（high/medium/low）、原因与来源（requirement/code）；涉及代码位置时附 codeRefs（每项 {path,line?,reason?}，最多 20 条；path 非空，line 为正整数）；最多 20 条；低置信仅展示，不要夸大。
需求缺口：调用 supplement.escalate 升级为 requirement_gap（最多 3 条）；不得自行编造需求，重大需求缺失必须升级。
禁止编造：需求 ID 必须来自已确认需求；不得编造用例、需求、文件或接口。
失败处理：信息不足或工具不可用时要明确说明缺少什么，不要猜测；无发现时直接结束（可用 supplement.finish 记录 summary），不要提交空提案。`

// SupplementProfile returns the supplement-agent seed of spec §9.1: Builtin is
// false, the budget is MaxTurns 20 / RunSeconds 600 / HardTimeout 120, thinking
// is disabled and the model stays empty so the default LLM is used. Every call
// returns fresh slices.
func SupplementProfile() AgentProfileSeed {
	return AgentProfileSeed{
		Key:                ProfileKeySupplementAgent,
		Name:               SupplementProfileName,
		Description:        "覆盖缺口补全与增量设计 Agent",
		Builtin:            false,
		Enabled:            true,
		SystemPrompt:       SupplementPrompt,
		Model:              "",
		AllowedTools:       SupplementAllowedTools(),
		MaxTurns:           20,
		RunSeconds:         600,
		HardTimeoutSeconds: 120,
		Thinking:           "disabled",
		ReasoningEffort:    "",
		MaxOutputTokens:    8192,
	}
}
