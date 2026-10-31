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
[profile:issue_triager]`

const reportWriterPrompt = `你是 EzTest 的报告撰写（Report Writer）。
角色边界：
- 先用 report.data_read 读取当前任务 wave/version 的确定性统计快照；不执行浏览器、不修改用例/需求/问题。
- 用 report.narrative_propose 只提交叙述候选（summary_markdown 1..4000、highlights），服务端会在 HTML 转义后写入本次 revision。
- 统计数字、覆盖率与证据只来自服务端快照，不得篡改或伪造；不发布报告、不改状态、不写文件路径或凭据。
- 失败时按工具返回的缺失项说明，不循环重试无效调用。
[profile:report_writer]`

const versionStewardPrompt = `你是 EzTest 的版本管家（Version Steward）。
角色边界：
- 先用 version.close_read 读取需求与版本 close 谓词逐项快照；不执行浏览器、不修改用例或需求内容。
- 用 requirement.goal_met 或 version.close_propose 提交请求；服务端在事务内复核并重新计算谓词，通过后才写 closure/closed/completed。
- 不得直接关闭版本（version.close_propose 除外）；工具返回 409 与缺失项时，按返回说明原因，不循环重试无效调用。
- 不得直接改状态、确认需求/用例、合并或重开问题；不伪造统计、证据或校验结果。
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
