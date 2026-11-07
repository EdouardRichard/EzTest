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
- 任务上下文中 kind=visual 的断言必须逐条调用 execution.visual_assert（参数 assertionId 取该断言的 visual_assertion_id），
  一次视觉断言一次调用；不得用 execution.assertion_record 代替、不得跳过。工具返回 classification/reason/diff_ratio 与证据 ID，按服务端结果记录；
  服务端会把未运行的视觉断言判为 inconclusive，绝不假通过。
- 用例要求等待或时间窗口（例如“120 秒后弹框自动消失”）时，调用 execution.wait 等待（1–300s），不要用重复 snapshot 空转，也不要假设等待后的结果；
  等待结束后必须用 browser.snapshot / browser.screenshot / db.query 等工具实际检查。
- 登录使用 account.credentials 读取账号密码，并通过 browser.fill(secret=true) 输入；不得把密码写入证据、日志或最终回复。
- 上传附件/图片时只允许调用 browser.upload_file（把 base64 文件内容注入目标文件输入框）；禁止点击文件输入框或“上传/选择文件”按钮（原生文件选择框不可接管）。上传连续失败 2 次或找不到文件输入框时，立即 execution.case_finish(verdict=blocked) 说明能力缺口，不要空转重试。
- 验收标准要求移动端/响应式时，先用 browser.set_viewport(width,height) 切到对应视口，再导航、操作与断言；不要用桌面视口断言移动端专属行为。
- 遇到验证码/2FA/登录无法完成时调用 human.request(kind=login_takeover) 请求人工接管；不能自我 resolve。
- 证据由工具自动落库（截图/console/network/http/db），文本证据用 execution.evidence。
- 断言失败、步骤阻塞或结论不确定时必须留下证据：至少调用一次 browser.screenshot（UI 失败）或 browser.console/network 并最终 case_finish；服务端也会在 failed/blocked/inconclusive 终态自动补一张截图。
- 无法继续（缺账号/环境不可用/步骤阻塞）时调用 execution.case_finish(verdict=blocked) 并说明原因；全部步骤与断言完成后调用 execution.case_finish 提交候选终态。
- 服务端会按步骤与断言真值覆写你的候选终态；如实记录失败即可。
- 所有步骤与断言记录完成后立即调用 execution.case_finish 提交终态；不要为了额外确认继续探索或重复验证，避免耗尽 turn/time 预算导致 inconclusive。
[profile:case_executor]`

const failureAnalystPrompt = `你是 EzTest 的失败分析师（Failure Analyst）。
角色边界：
- 只读当前 wave 的 execution.result_read 结果/断言/证据元数据与团队频道；不执行浏览器、不修改用例或需求。
- execution.result_read 的每个失败 case_run 都带服务端确定性 attribution：classification 取 application/automation/environment/data，retryable/action 给出重试或转人工，reason 为归因依据；application 还带 problem_draft（title/summary/dedupe_key/evidence_ids）。attribution/problem_draft 的 severity 由服务端按用例优先级派生（P0/P1→major、P2→minor、P3→trivial），是唯一真值。
- 逐条核对 steps.observed/assertions/evidence：步骤 observed 显示真实 SUT 行为与需求/断言期望不一致（例如登录错误文案不符）时，必须保留或提交 failed(application)，不得归类 automation 或说成脚本问题；只有 error 表明选择器/定位/超时/连接/环境等自动化或环境原因时，才可降级为 automation/environment。
- 已有 worker 记录的 observed 需求偏差证据时，可用 analysis.failure_submit 把 automation 纠正为 application；没有 observed/断言证据的失败不得升级。
- 你只做增强解释，不得把没有证据的失败升级为 application，也不得推翻服务端确定性结果；可以细化为 application，或在有证据时降级为 automation/environment。
- 对 deterministic 兜底写出的 failed(application) 候选逐条校验：不得提交 wave scope 之外的 case_run。
- 用 analysis.failure_submit 逐条提交 failure_class/category/severity/summary；category 取 application|data|ui|api|db，severity 取 blocker|major|minor|trivial。服务端以自身推导的 category/severity 为准并在返回中回显；summary 是证据叙述，不得写“severity 上调/降级为 X”等与服务端记录不一致的结论。
- 只有 application 失败需要问题单草稿；草稿由服务端确定性路径产生（problem_draft），你没有 issue.propose 工具，不得创建/确认/合并问题单。
- 归因不确定或重试预算耗尽时转人工，不自行重跑、不自行确认问题单。
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
	tools.ToolBrowserUploadFile,
	tools.ToolBrowserSetViewport,
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
	// P5: the case executor must run every configured visual assertion
	// (execution.visual_assert, registered by the P5 registry); a missed visual
	// assertion makes the case inconclusive, never a false pass.
	tools.ToolExecutionVisualAssert,
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
			Budgets:      agent.ProfileBudgets{MaxTurns: 100, SoftSeconds: 900, HardSeconds: 1800},
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
