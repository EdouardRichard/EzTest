// Package domain holds the execution context's core types and invariants.
package domain

import agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"

// ExecutionProfileKey is the stable key of the execution agent profile
// (design spec §7.1). The profile is seeded with Builtin=false so the P1
// default-profile lookup never selects it as the runtime's diagnostic default.
const ExecutionProfileKey = "execution-agent"

// ExecutionDBAssertTool is the P7 db assertion tool (design spec §9.5). It is
// appended to the execution-agent allowlist seed; the composition root adds it
// idempotently to an already-seeded profile (EnsureToolAllowed, T17).
const ExecutionDBAssertTool = "execution.db_assert"

// ExecutionVisualAssertTool is the P8 visual assertion tool (design spec §9.3).
// It is appended to the execution-agent allowlist seed and added idempotently to
// an already-seeded profile by the composition root.
const ExecutionVisualAssertTool = "execution.visual_assert"

// ExecutionPrompt is the exact eight-point system prompt of design spec §7.2.
// It is the seed text for the execution-agent profile; an administrator may
// edit the stored prompt through the P1 profile API afterwards.
const ExecutionPrompt = `你是 EzTest 的执行 Agent，只执行给定用例快照。

规则：
1. 只执行给定用例快照，不修改用例、不新增断言、不越出环境白名单。
2. 严格按步骤顺序：执行 → 用 execution.step_record 记录该步骤结果 → 有断言则用 execution.assertion_record 记录（必须带证据）。
3. 任何 passed/failed 断言都必须先产生证据（截图/HTTP 交换）；无证据就如实报 inconclusive。
4. 失败分类：只有目标系统正常响应且断言不符才算 application；连不上/白名单/超时算 environment；401/登录跳转算 session；模型/工具/预算问题算 model；用例本身不可执行算 case。
5. 禁止伪造、禁止跳过步骤；无法完成时用 execution.case_finish(blocked|inconclusive) 并说明原因。
6. 恢复运行：先看已完成步骤，不要重跑；cleanup_required=true 时先用浏览器/HTTP 做 cleanup/seed，再继续剩余步骤。
7. 所有 URL 必须是环境 baseUrl 或其 allowed_hosts 内；不要尝试访问其他主机。
8. 你不持有、也不要索取任何账号密码/凭据；登录态由系统注入。用例数据中的敏感字段已打码，输入密码类步骤应视为不可执行并报告 case 问题。`

// ExecutionProfile returns the execution-agent seed of design spec §7.1: the
// stable key, the eight-point system prompt, the exact tool allowlist and the
// run budgets. Every call returns a fresh slice so a caller cannot mutate the
// seed a later startup persists. The profile is not a P1 builtin: the P1
// default-profile lookup must keep returning runtime-diagnostic.
func ExecutionProfile() agentdomain.AgentProfile {
	return agentdomain.AgentProfile{
		Key:         ExecutionProfileKey,
		Name:        "执行 Agent",
		Description: "",
		Builtin:     false,
		Enabled:     true,

		SystemPrompt:       ExecutionPrompt,
		Model:              "",
		AllowedTools:       executionAllowedTools(),
		MaxTurns:           30,
		RunSeconds:         900,
		HardTimeoutSeconds: 120,
		// design spec §7.1: thinking is configurable and defaults to false.
		Thinking:        "disabled",
		ReasoningEffort: "",
		MaxOutputTokens: 8192,
	}
}

// executionAllowedTools is the frozen tool allowlist of design spec §7.1. The
// names must match the tools registered by the agent_tool package exactly; the
// composition root validates them against the live registry before seeding.
func executionAllowedTools() []string {
	return []string{
		"execution.case_context",
		"browser.goto",
		"browser.snapshot",
		"browser.click",
		"browser.fill",
		"browser.press",
		"browser.wait_for",
		"browser.screenshot",
		"browser.console",
		"browser.network",
		"browser.download",
		"http.request",
		"execution.step_record",
		"execution.assertion_record",
		"execution.case_finish",
		ExecutionDBAssertTool,
		ExecutionVisualAssertTool,
	}
}
