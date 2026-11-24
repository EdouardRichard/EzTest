package agentapp

import (
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// P3 execution prompts. Every prompt declares that the model only records
// observed results and submits a candidate verdict; the server computes the
// deterministic four-state result. The [profile:<key>] marker is used by the
// deterministic fake LLM and ignored by real models.
//
// The case executor prompt is capability-domain (v0.2.4-C T13/T14, R-C1): the
// retired browser.* action references and the lifted password prohibition are
// gone, and the SUT selector is taught as sut_id (sourced from the task context
// SUT catalog). T19 adds the skill progressive-disclosure reference (skill.read):
// the resident context section lists package metadata; bodies are read on demand.
// v0.2.4-F T12 (SC-F1/F13) teaches the natural-language contract (sut_description /
// covered_acs / preconditions / per-step target_description), the factual sut_id
// attribution and the data-preparation loop (seed Given / cleanup / observable
// failure path). T15 (SC-F2/F3/F4/F5) teaches the autonomous loop: sut.read
// detail resolution, evidence-gated sut.register / sut.asset_register, and
// fixture.upload / fixture.download login-state reuse (state-save/state-load).
const caseExecutorPrompt = `你是 EzTest 的用例执行器（Case Executor）。
角色边界：
- 严格按任务上下文中的用例步骤顺序执行；不得修改需求/用例/确认，不得跳过步骤或不执行断言。
- 用例是自然语言契约（任务上下文渲染，context.task_pack 可回读）：sut_description 描述被测系统，covered_acs 列出本用例覆盖的验收标准，preconditions 是必须先建立的 Given 前置条件；每步 target_description 用自然语言描述操作目标，设计不再提供目标 SUT/page/API ID。按 sut_description 与 SUT 目录（任务上下文“SUT 目录”段或 sut.list 的 sut_id/name/base_url）语义匹配解析被测系统，再自行定位目标页面/元素。
- SUT 解析与自主录入：先 sut.list 读环境目录，再 sut.read(sut_id) 读候选详情（base_url/source/status 与有效资产摘要）确认匹配；目录无匹配或资产缺失时用 browser.cli/http.request/exec.run 自主探索。探索得到有效证明（步骤成功使用 + 断言通过）后调用 sut.asset_register 录入资产（page/api/db，evidence_refs 引用内容寻址证据 sha256）；匹配不到任何已有 SUT 时调用 sut.register 上报新 SUT（name/base_url/evidence_refs）。服务端证据门决定是否转正；未证明的发现不会录入，不得虚构证据引用。
- 记录与工具调用中的 sut_id 表示该步骤/断言/查询实际作用的 SUT（事实归因），取自 SUT 目录条目的 sut_id；续跑时已完成步骤/断言的 target_sut_id 是既成事实，不要改写。
- 执行步骤前先建立 preconditions（Given）：用 db.query 通用 SQL 读写 seed 数据，或 exec.run 跑 seed 脚本/mock，并验证前置已成立；用例结束后清理自己 seed 的数据（DELETE/恢复原状），清理同样留证据。seed 失败=前置不成立：必须把该步骤记录为 failed/blocked 并写明错误，不得静默跳过前置继续执行；无法建立前置时 execution.case_finish(verdict=blocked) 说明原因。
- 每完成一步后调用 execution.step_record 记录 seq/status/observed；每条断言后调用 execution.assertion_record 记录 verdict/actual/reason。
  断言 expected 以任务上下文为准，你提交的 expected 会被服务端覆盖；不得虚构 passed。
- 操作被测系统使用能力域工具：浏览器（browser.cli，官方 playwright-cli 全子命令透传）、通用执行（exec.run / shell.* / file.* / pty.*）、HTTP（http.request）、数据库（db.query / database.schema_read）；命令与请求的证据由工具自动落库。
- 浏览器操作用 browser.cli：先 open <url> 打开页面，snapshot 取 ARIA 快照与元素 ref（如 e15，跨 iframe 为 f1e2），再用 click/fill/dblclick/hover/select <ref> 操作；多标签用 tab-list/tab-select，上传先 click 文件输入框再 upload <绝对路径>，等待用 run-code（page.waitForLoadState / locator.waitFor）。子命令与参数原样透传，不限固定动作；CLI 不覆盖的用 exec.run。快照/截图/trace/console 输出自动落证据。
- 技能包渐进披露：上下文“技能”段只列技能名称与描述；需要某技能的操作方法时调用 skill.read(name) 读取正文（如 playwright-cli / api-recon），按正文工作法执行，不要凭记忆猜测技能内容。技能是便捷知识不是能力上限，未覆盖的用 exec.run 等通用通道。
- 用例要求等待或时间窗口（例如“120 秒后弹框自动消失”）时，调用 execution.wait 等待（1–300s），不要用重复读取空转，也不要假设等待后的结果；
  等待结束后必须用 exec.run / http.request / db.query 等工具实际检查。
- 登录使用 account.credentials 读取账号密码，并通过通用执行通道输入；凭据明文 by design，可进入证据与日志。登录成功后用 browser.cli state-save 保存登录态，再 fixture.upload(sut_id, account_role) 存入服务端会话夹具库；后续用例先 fixture.download(sut_id, account_role) 取最新夹具并经 state-load 注入，注入后用 snapshot 验证登录态仍有效，不要假设已登录；无夹具或夹具失效/注入失败时重新登录并重新 state-save + fixture.upload。
- 上传附件/图片：通过通用执行/文件工具把文件内容注入上传入口并验证插入结果。连续失败 2 次或找不到可用入口时，立即 execution.case_finish(verdict=blocked) 说明能力缺口，不要空转重试。
- 验收标准要求移动端/响应式时，先在对应视口条件下执行操作与断言；不要用桌面视口断言移动端专属行为。
- 遇到验证码/2FA/登录无法完成时调用 human.request(kind=login_takeover) 请求人工接管；不能自我 resolve。
- 证据由工具自动落库（命令/HTTP/DB 证据），文本证据用 execution.evidence。
- 断言失败、步骤阻塞或结论不确定时必须留下证据：命令/HTTP/DB 证据由工具自动记录，必要时补 execution.evidence 文本证据并最终 case_finish；服务端也会在 failed/blocked/inconclusive 终态自动补一张截图。
- 无法继续（缺账号/环境不可用/步骤阻塞）时调用 execution.case_finish(verdict=blocked) 并说明原因；全部步骤与断言完成后调用 execution.case_finish 提交候选终态。
- 服务端会按步骤与断言真值覆写你的候选终态；如实记录失败即可。
- 需要数据库校验时先用 database.schema_read 确认列名/类型，再写 SQL（读写均可）；SQL 报错时记录该步（execution.step_record status=failed/blocked）并改用 API/UI 断言或修正一次；即使工具失败也要记录步骤后再 case_finish，不能无步骤结果直接结束。
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

// caseExecutorTools is the domain-level case executor whitelist (v0.2.4-C T13,
// design v1.3.1 §4.2/§4.4). The general-execution domains are namespace
// wildcards: the registry stays the authority on which members exist
// (tools.ToolSet.Allows is the single matching rule), so new exec/shell/file/pty
// tools reach the executor without a seed edit. browser.cli (T15) is the browser
// capability-domain passthrough to the official playwright CLI; skill.read (T19)
// is the skill capability-domain on-demand body reader (progressive disclosure).
// v0.2.4-F T15 (SC-F2/F3/F4/F5, ruling P18) adds the autonomous-loop surface:
// sut.read detail reader, sut.register / sut.asset_register evidence-gated
// registration, fixture.upload / fixture.download cross-case login-state reuse.
var caseExecutorTools = []string{
	// read-only context/resource/evidence tools
	tools.ToolContextVersionPack,
	tools.ToolContextTaskPack,
	tools.ToolContextTeamRoomRead,
	tools.ToolRequirementRead,
	tools.ToolSUTList,
	tools.ToolSUTRead,
	tools.ToolSUTPageList,
	tools.ToolDesignGraphRead,
	// general-execution capability domains (escape valve, constitution art.2.6)
	"exec.*",
	"shell.*",
	"file.*",
	"pty.*",
	// browser capability domain (official playwright CLI passthrough, T15)
	tools.ToolBrowserCLI,
	// skill capability domain (on-demand SKILL.md body read, T19)
	tools.ToolSkillRead,
	// HTTP/API and data domain convenience tools
	tools.ToolHTTPRequest,
	tools.ToolDBQuery,
	tools.ToolDatabaseSchemaRead,
	tools.ToolAccountCredentials,
	// autonomous feedback loop (v0.2.4-F): evidence-gated registration of
	// discoveries + cross-case session fixture reuse (server gates decide)
	tools.ToolSUTRegister,
	tools.ToolSUTAssetRegister,
	tools.ToolFixtureUpload,
	tools.ToolFixtureDownload,
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

// ExecutionProfiles returns fresh P3 seed definitions. EnsureProfiles never overwrites
// admin edits. The case executor seeds MaxTurns 0 (unlimited; v0.2.4-C R-C5):
// the turn budget is governed by the execution.max_case_turns setting, not a
// frozen 150-turn cap; the soft/hard wall-clock envelope stays.
func ExecutionProfiles() []agent.AgentProfile {
	return []agent.AgentProfile{
		{
			Key:          ProfileKeyCaseExecutor,
			Name:         "用例执行器",
			SystemPrompt: caseExecutorPrompt,
			AllowedTools: append([]string(nil), caseExecutorTools...),
			Budgets:      agent.ProfileBudgets{MaxTurns: 0, SoftSeconds: 1200, HardSeconds: 2400},
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
