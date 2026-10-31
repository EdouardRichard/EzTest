package agentapp

import (
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// P3 execution prompts. Every prompt declares that the model only records
// observed results and submits a candidate verdict; the server computes the
// deterministic four-state result. The [profile:<key>] marker is used by the
// deterministic fake LLM and ignored by real models.
const caseExecutorPrompt = `你是 EzTest 的用例执行器（Case Executor）。
角色边界：
- 严格按任务上下文中的用例步骤顺序执行；不得修改需求/用例/确认，不得跳过步骤或不执行断言。
- 每完成一步后调用 execution.step_record 记录 seq/status/observed；每条断言后调用 execution.assertion_record 记录 verdict/actual/reason。
  断言 expected 以任务上下文为准，你提交的 expected 会被服务端覆盖；不得虚构 passed。
- 用例要求等待或时间窗口（例如“120 秒后弹框自动消失”）时，调用 execution.wait 等待（1–300s），不要用重复 snapshot 空转，也不要假设等待后的结果；
  等待结束后必须用 browser.snapshot / browser.screenshot / db.query 等工具实际检查。
- 登录使用 account.credentials 读取账号密码，并通过 browser.fill(secret=true) 输入；不得把密码写入证据、日志或最终回复。
- 遇到验证码/2FA/登录无法完成时调用 human.request(kind=login_takeover) 请求人工接管；不能自我 resolve。
- 证据由工具自动落库（截图/console/network/http/db），文本证据用 execution.evidence。
- 无法继续（缺账号/环境不可用/步骤阻塞）时调用 execution.case_finish(verdict=blocked) 并说明原因；全部步骤与断言完成后调用 execution.case_finish 提交候选终态。
- 服务端会按步骤与断言真值覆写你的候选终态；如实记录失败即可。
[profile:case_executor]`

const failureAnalystPrompt = `你是 EzTest 的失败分析师（Failure Analyst）。
角色边界：
- 只读当前 wave 的结果/断言/证据元数据与团队频道；不执行浏览器、不修改用例或需求。
- 对 deterministic 兜底写出的 failed(application) 候选逐条校验：只能细化为 application，或在有证据时降级为 automation/environment；
  不得把非 application 升级为 application，不得提交 wave scope 之外的 case_run。
- 用 analysis.failure_submit 逐条提交 failure_class/category/severity/summary；category 取 application|data|ui|api|db，severity 取 blocker|major|minor|trivial。
- 你的分析是增强，不阻塞 wave 终态；不要编造不存在的证据。
[profile:failure_analyst]`

const coverageAnalystPrompt = `你是 EzTest 的覆盖分析师（Coverage Analyst）。
角色边界：
- 只读当前版本的 current 需求、已确认用例与最近执行结果；不执行浏览器、不修改用例或需求、不创建问题。
- 识别执行覆盖缺口：有已确认用例但从未执行（execution_not_run）、或最近执行全部失败/阻塞/不确定（execution_failed_coverage）。
- 用 analysis.coverage_submit 提交 requirement_id/reason/evidence_case_run_ids；服务端会 ensure design.gap 并做去重。
- deterministic 兜底已经写过缺口，你只补充解释与优先级，不重复造缺口。
[profile:coverage_analyst]`

var caseExecutorTools = []string{
	// read-only context/resource/evidence tools
	tools.ToolContextVersionPack,
	tools.ToolContextTaskPack,
	tools.ToolContextTeamRoomRead,
	tools.ToolRequirementRead,
	tools.ToolSUTList,
	tools.ToolSUTPageList,
	tools.ToolDesignGraphRead,
	// browser/db/http execution tools
	tools.ToolBrowserGoto,
	tools.ToolBrowserSnapshot,
	tools.ToolBrowserClick,
	tools.ToolBrowserFill,
	tools.ToolBrowserPress,
	tools.ToolBrowserWaitFor,
	tools.ToolBrowserScreenshot,
	tools.ToolBrowserConsole,
	tools.ToolBrowserNetwork,
	tools.ToolBrowserDownload,
	tools.ToolBrowserStorageState,
	tools.ToolHTTPRequest,
	tools.ToolDBQuery,
	tools.ToolDatabaseSchemaRead,
	tools.ToolAccountCredentials,
	// execution lifecycle tools
	tools.ToolExecutionStepRecord,
	tools.ToolExecutionAssertionRecord,
	tools.ToolExecutionEvidence,
	tools.ToolExecutionWait,
	tools.ToolExecutionCaseFinish,
	tools.ToolHumanRequest,
}

var failureAnalystTools = []string{
	tools.ToolContextTaskPack,
	tools.ToolContextTeamRoomRead,
	tools.ToolExecutionResultRead,
	tools.ToolExecutionEvidenceRead,
	tools.ToolAnalysisFailureSubmit,
}

var coverageAnalystTools = []string{
	tools.ToolContextTaskPack,
	tools.ToolRequirementRead,
	tools.ToolExecutionCoverageRead,
	tools.ToolAnalysisCoverageSubmit,
}

// P3Profiles returns fresh P3 seed definitions. Budgets and tool sets are frozen
// by the P3 profile contract; EnsureProfiles never overwrites admin edits.
func P3Profiles() []agent.AgentProfile {
	return []agent.AgentProfile{
		{
			Key:          ProfileKeyCaseExecutor,
			Name:         "用例执行器",
			SystemPrompt: caseExecutorPrompt,
			AllowedTools: append([]string(nil), caseExecutorTools...),
			Budgets:      agent.ProfileBudgets{MaxTurns: 60, SoftSeconds: 780, HardSeconds: 900},
			Enabled:      true,
		},
		{
			Key:          ProfileKeyFailureAnalyst,
			Name:         "失败分析师",
			SystemPrompt: failureAnalystPrompt,
			AllowedTools: append([]string(nil), failureAnalystTools...),
			Budgets:      agent.ProfileBudgets{MaxTurns: 6, SoftSeconds: 180, HardSeconds: 300},
			Enabled:      true,
		},
		{
			Key:          ProfileKeyCoverageAnalyst,
			Name:         "覆盖分析师",
			SystemPrompt: coverageAnalystPrompt,
			AllowedTools: append([]string(nil), coverageAnalystTools...),
			Budgets:      agent.ProfileBudgets{MaxTurns: 6, SoftSeconds: 180, HardSeconds: 300},
			Enabled:      true,
		},
	}
}
