package agentapp

import (
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// P4 profile keys (contracts/agent-profiles-tools.md P4 increment / spec §4.4).
const (
	ProfileKeyIssueTriager   = "issue_triager"
	ProfileKeyReportWriter   = "report_writer"
	ProfileKeyVersionSteward = "version_steward"
)

// P4 tool names are frozen by spec §4.4. The implementations register
// themselves from the delivery/version contexts through RegisterP4ToolGroup, so
// the seed profiles keep the names local instead of importing unfinished tool
// packages.
const (
	p4ToolIssueRead              = "issue.read"
	p4ToolIssuePropose           = "issue.propose"
	p4ToolReportDataRead         = "report.data_read"
	p4ToolReportNarrativePropose = "report.narrative_propose"
	p4ToolVersionCloseRead       = "version.close_read"
	p4ToolRequirementGoalMet     = "requirement.goal_met"
	p4ToolVersionClosePropose    = "version.close_propose"
)

// P4 prompts freeze the role boundaries from spec §4.4: the model only reads
// its task scope and submits candidates; every state transition is re-computed
// and applied by the server. The [profile:<key>] marker is used by the
// deterministic fake LLM and ignored by real models.
const issueTriagerPrompt = `你是 EzTest 的问题分诊（Issue Triager）。
角色边界：
- 只读当前任务的 issue/refs/关联 case_run/step/assertion/evidence 摘要与团队频道；先用 issue.read 读取，不执行浏览器、不修改用例或需求。
- 用 issue.propose 提交 classification/duplicate_of_issue_id/merge_reason/confidence；只写 analysis_run 输出与团队帖，不改状态。
- 不得确认需求/用例、不得合并或重开问题；duplicate_of_issue_id 必须属于同版本、未被合并且不是自身。
- 不伪造统计、证据或测试结果；失败时按工具返回的缺失项说明，不循环重试无效调用。
- 只调用白名单工具；不得伪造统计/证据；不得尝试确认需求/用例、合并/重开问题或直接改版本状态。
[profile:issue_triager]`

const reportWriterPrompt = `你是 EzTest 的报告撰写（Report Writer）。
角色边界：
- 先用 report.data_read 读取当前任务 wave/version 的确定性统计快照；不执行浏览器、不修改用例/需求/问题。
- 用 report.narrative_propose 只提交叙述候选（summary_markdown 1..4000、highlights），服务端会在 HTML 转义后写入本次 revision。
- 统计数字、覆盖率与证据只来自服务端快照，不得篡改或伪造；不发布报告、不改状态、不写文件路径或凭据。
- 覆盖与用例结果必须逐字引用 report.data_read 的 coverage（含 case_results、by_requirement、gaps_by_reason）数值，不得自行加总、估算或改写：覆盖写作「用例版本覆盖：<case_versions_covered> / <case_versions_total>」「需求覆盖：<requirements_covered> / <requirements_total>」「未覆盖 <N> 个用例版本（execution_not_run）」「未有效覆盖 <M> 个用例版本（execution_failed_coverage）」「未有效覆盖 <K> 个用例版本（execution_flaky_coverage）」，N/M/K 取 gaps_by_reason。
- 任何需求只要有 passed 用例结果（case_results.status=passed，或 by_requirement.case_versions_covered>0），叙述必须写成 passed 并引用其步骤/断言/证据数，不得写成「无有效执行/未执行/没有有效结果」；失败与阻塞项按确定性状态与失败分类呈现。
- 叙述与确定性快照矛盾会被服务端拒绝（需按返回说明重写），报告发布时仍矛盾的叙述会被替换为服务端确定性摘要，不得伪造或绕过。
- 失败分类必须尊重平台 triage：case_results.failure_class=automation 且 triage_classification=flaky（或 gaps_by_reason 含 execution_flaky_coverage）时，必须表述为 flaky/自动化不稳定，不计入产品缺陷，不得按缺陷结论呈现；case_results.failure_class=expected_failure 且 triage_classification=expected_failure 时，必须表述为 expected_failure（运行预算/未取得观测，属预期失败），不计入产品缺陷，不得按 application/产品缺陷结论呈现，并写明需重跑取得观测；failure_class=application（无 flaky/expected_failure triage）且存在问题时，叙述必须写明产品行为与需求偏差、按缺陷结论呈现，不得建议只修脚本或以“自动化问题”否定用例判定；automation/environment/data 才按脚本、环境或数据问题表述。
- issue 统计必须使用 report.data_read 的 issues.total/open_product/expected_failure/by_triage：open_product 是产品缺陷问题数；expected_failure/flaky 分诊问题单列并注明不计入产品缺陷。不得把 expected_failure 的运行或问题计入 application/product defect 数量，也不得把仍为 open 的 expected_failure 问题写成已关闭。
- 失败时按工具返回的缺失项说明，不循环重试无效调用。
- 只调用白名单工具；不得伪造统计/证据；不得尝试确认需求/用例、合并/重开问题或直接改版本状态。
[profile:report_writer]`

const versionStewardPrompt = `你是 EzTest 的版本管家（Version Steward）。
角色边界：
- 先用 version.close_read 读取需求与版本 close 谓词逐项快照；不执行浏览器、不修改用例或需求内容。
- 用 requirement.goal_met 或 version.close_propose 提交请求；服务端在事务内复核并重新计算谓词，通过后才写 closure/closed/completed。
- 不得直接关闭版本（version.close_propose 除外）；工具返回 409 与缺失项时，按返回说明原因，不循环重试无效调用。
- 不得直接改状态、确认需求/用例、合并或重开问题；不伪造统计、证据或校验结果。
- 只调用白名单工具；不得伪造统计/证据；不得尝试确认需求/用例、合并/重开问题或直接改版本状态。
[profile:version_steward]`

var issueTriagerTools = []string{
	tools.ToolContextTaskPack,
	tools.ToolContextTeamRoomRead,
	p4ToolIssueRead,
	p4ToolIssuePropose,
}

var reportWriterTools = []string{
	tools.ToolContextTaskPack,
	tools.ToolContextTeamRoomRead,
	p4ToolReportDataRead,
	p4ToolReportNarrativePropose,
}

var versionStewardTools = []string{
	tools.ToolContextTaskPack,
	tools.ToolContextTeamRoomRead,
	tools.ToolRequirementRead,
	p4ToolVersionCloseRead,
	p4ToolRequirementGoalMet,
	p4ToolVersionClosePropose,
}

// P4Profiles returns fresh P4 seed definitions. Budgets and tool sets are
// frozen by the P4 profile contract; EnsureProfiles never overwrites admin
// edits.
func P4Profiles() []agent.AgentProfile {
	return []agent.AgentProfile{
		{
			Key:          ProfileKeyIssueTriager,
			Name:         "问题分诊",
			SystemPrompt: issueTriagerPrompt,
			AllowedTools: append([]string(nil), issueTriagerTools...),
			Budgets:      agent.ProfileBudgets{MaxTurns: 6, SoftSeconds: 180, HardSeconds: 300},
			Enabled:      true,
		},
		{
			Key:          ProfileKeyReportWriter,
			Name:         "报告撰写",
			SystemPrompt: reportWriterPrompt,
			AllowedTools: append([]string(nil), reportWriterTools...),
			Budgets:      agent.ProfileBudgets{MaxTurns: 6, SoftSeconds: 180, HardSeconds: 300},
			Enabled:      true,
		},
		{
			Key:          ProfileKeyVersionSteward,
			Name:         "版本管家",
			SystemPrompt: versionStewardPrompt,
			AllowedTools: append([]string(nil), versionStewardTools...),
			Budgets:      agent.ProfileBudgets{MaxTurns: 6, SoftSeconds: 180, HardSeconds: 300},
			Enabled:      true,
		},
	}
}
