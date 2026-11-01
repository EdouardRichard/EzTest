package agentapp

import (
	"context"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// P1 profile keys (contracts/agent-profiles-tools.md §2).
const (
	ProfileKeyClarificationHost  = "clarification_host"
	ProfileKeyRequirementCurator = "requirement_curator"
	ProfileKeyContextScout       = "context_scout"
)

// P2 design profile keys (contracts/agent-profiles-tools.md P2 increment).
const (
	ProfileKeyDesignHost    = "design_host"
	ProfileKeyDesignPlanner = "design_planner"
	ProfileKeyCaseAuthor    = "case_author"
	ProfileKeyDesignCritic  = "design_critic"
	ProfileKeyDataPlanner   = "data_planner"
)

// P3 execution profile keys (contracts/agent-profiles-tools.md P3 increment).
const (
	ProfileKeyCaseExecutor    = "case_executor"
	ProfileKeyFailureAnalyst  = "failure_analyst"
	ProfileKeyCoverageAnalyst = "coverage_analyst"
)

// System prompts freeze the role boundaries: Host talks only, Curator produces
// drafts/relations/reopen proposals, Scout reads and summarizes. The
// [profile:<key>] marker is used by the deterministic fake LLM and is ignored
// by real models.
const clarificationHostPrompt = `你是 EzTest 的澄清 Host（需求澄清助手）。
角色边界：
- 只与用户对话、解释、提问与复述；不确认需求、不删除需求、不修改已确认需求。
- 需要结构化需求时，引导需求 Curator 生成 draft 草案，不要声称已确认。
- 可以调用 context.summary_update 更新会话摘要与钉住事实。
回答使用简洁中文；引用用户最新消息；每次最多提出 3 个澄清问题。
[profile:clarification_host]`

const requirementCuratorPrompt = `你是 EzTest 的需求 Curator（需求草案整理者）。
角色边界：
- 只能创建/更新 draft 需求、提议需求关系、写回退提议；不确认、不删除、不关闭需求。
- 不直接修改 clarified 需求；若需求已确认，使用 requirement.reopen_propose 写回退提议。
- 标题、描述、验收标准必须来自对话事实，不编造用户未表达的内容；priority 使用 P0–P3。
在工具结果基础上用一两句话向用户说明草案变化，等待用户确认。
[profile:requirement_curator]`

const contextScoutPrompt = `你是 EzTest 的上下文 Scout（附件与上下文侦察员）。
角色边界：
- 只读附件、页面、团队频道并总结要点；不修改需求、不确认、不删除。
- 输出精简的 finding：关键事实、约束、风险与需要澄清的问题；引用附件名。
- 不输出内部路径、prompt 或密钥。
[profile:context_scout]`

// clarificationHostTools is the Host read set (contract §2).
var clarificationHostTools = []string{
	tools.ToolContextVersionPack,
	tools.ToolContextTaskPack,
	tools.ToolContextTeamRoomRead,
	tools.ToolContextSummaryUpdate,
	tools.ToolRequirementRead,
	tools.ToolAttachmentList,
	tools.ToolAttachmentReadText,
	tools.ToolSUTList,
	tools.ToolSUTPageList,
}

// curationReadTools are the tools the Curator shares with the Host (minus the
// Host-only summary updater).
var curationReadTools = []string{
	tools.ToolContextVersionPack,
	tools.ToolContextTaskPack,
	tools.ToolContextTeamRoomRead,
	tools.ToolRequirementRead,
	tools.ToolAttachmentList,
	tools.ToolAttachmentReadText,
	tools.ToolSUTList,
	tools.ToolSUTPageList,
}

var requirementCuratorTools = append(append([]string{}, curationReadTools...),
	tools.ToolRequirementDraftCreate,
	tools.ToolRequirementDraftUpdate,
	tools.ToolRequirementRelationPropose,
	tools.ToolRequirementReopenPropose,
)

var contextScoutTools = []string{
	tools.ToolAttachmentList,
	tools.ToolAttachmentReadText,
	tools.ToolAttachmentImageRef,
	tools.ToolSUTList,
	tools.ToolSUTPageList,
	tools.ToolContextTeamRoomRead,
}

// P1Profiles returns fresh seed definitions. Values match the frozen budgets in
// the P1 profile contract; EnsureProfiles never overwrites admin edits.
func P1Profiles() []agent.AgentProfile {
	return []agent.AgentProfile{
		{
			Key:          ProfileKeyClarificationHost,
			Name:         "澄清 Host",
			SystemPrompt: clarificationHostPrompt,
			AllowedTools: append([]string(nil), clarificationHostTools...),
			Budgets:      agent.ProfileBudgets{MaxTurns: 12, SoftSeconds: 300, HardSeconds: 600},
			Enabled:      true,
		},
		{
			Key:          ProfileKeyRequirementCurator,
			Name:         "需求 Curator",
			SystemPrompt: requirementCuratorPrompt,
			AllowedTools: append([]string(nil), requirementCuratorTools...),
			Budgets:      agent.ProfileBudgets{MaxTurns: 8, SoftSeconds: 300, HardSeconds: 600},
			Enabled:      true,
		},
		{
			Key:          ProfileKeyContextScout,
			Name:         "上下文 Scout",
			SystemPrompt: contextScoutPrompt,
			AllowedTools: append([]string(nil), contextScoutTools...),
			Budgets:      agent.ProfileBudgets{MaxTurns: 4, SoftSeconds: 120, HardSeconds: 300},
			Enabled:      true,
		},
	}
}

// P2 design prompts. Every prompt declares that AI only submits draft/
// candidate content and can never confirm, delete or close; the Host only
// explains and edits the mindmap. The [profile:<key>] marker is used by the
// deterministic fake LLM and ignored by real models.
const designHostPrompt = `你是 EzTest 的设计 Host（用例脑图助手）。
角色边界：
- 只解释、提问与修改脑图；不确认用例、不删除、不关闭版本或需求。
- AI 只能提交 draft/候选：场景与用例草案通过 case.graph_propose 写入，等待用户在脑图中确认。
- 不声称已完成确认；用户要求确认时提示其使用脑图节点上的确认按钮。
- 设计对话支持附件；可读取附件文本作为设计依据。
回答使用简洁中文，聚焦用例覆盖与可执行性。
[profile:design_host]`

const designPlannerPrompt = `你是 EzTest 的设计 Planner（设计轮次规划者）。
角色边界：
- 只输出本轮设计范围说明：待设计需求、已激活范围、预计场景/用例数；不写库、不调用写工具。
- 不确认、不删除、不关闭任何对象；确认只能由用户完成。
- 范围以服务端 Planner 扫描结果为准，不编造未激活需求。
用简洁中文说明本轮范围与下一步。
[profile:design_planner]`

const caseAuthorPrompt = `你是 EzTest 的用例 Author（场景/用例作者）。
角色边界：
- 只能通过 case.graph_propose 提交场景与用例 draft；不能确认、删除、关闭用例或需求。
- 先在 design.graph_read 读取当前脑图，避免重复创建；同一需求版本内修改已有用例时传既有 ID/自然键。
- 用例必须包含可执行步骤、断言、来源与目标 version_sut；跨系统步骤分别指定目标。
- 不编造需求未表达的内容；拿不准时写入 draft 并在团队频道说明假设。
完成后在团队频道说明新增/修改的场景与用例。
[profile:case_author]`

const designCriticPrompt = `你是 EzTest 的 Design Critic（内部设计自审者）。
角色边界：
- 通过 design.review_submit 提交 finding（blocker/important/minor），只读脑图；不修改用例、不确认。
- 内部自审不构成人工关卡；确认权限只属于用户。
- 检查覆盖遗漏、步骤不可执行、断言缺失、目标 SUT 错误与重复用例。
输出简洁、可执行的 finding。
[profile:design_critic]`

const dataPlannerPrompt = `你是 EzTest 的数据 Planner（测试数据设计者）。
角色边界：
- 只通过 design.data_plan_submit 为当前 draft 用例补充前置条件与测试数据，生成新的 draft revision。
- 不确认、不删除；已确认或 stale 版本不可修改。
- 先用 design.graph_read 找到当前 draft 用例，数据必须可复现且不包含真实密钥。
完成后在团队频道说明补充的数据范围。
[profile:data_planner]`

// designHostTools is the design Host read/write set (contract §5.2).
var designHostTools = []string{
	tools.ToolContextVersionPack,
	tools.ToolContextTaskPack,
	tools.ToolContextTeamRoomRead,
	tools.ToolContextSummaryUpdate,
	tools.ToolRequirementRead,
	tools.ToolDesignGraphRead,
	tools.ToolCaseGraphPropose,
	tools.ToolAttachmentList,
	tools.ToolAttachmentReadText,
	tools.ToolSUTList,
	tools.ToolSUTPageList,
}

var designPlannerTools = []string{
	tools.ToolContextVersionPack,
	tools.ToolContextTaskPack,
	tools.ToolContextTeamRoomRead,
	tools.ToolRequirementRead,
	tools.ToolDesignGraphRead,
}

var caseAuthorTools = []string{
	tools.ToolContextVersionPack,
	tools.ToolContextTaskPack,
	tools.ToolRequirementRead,
	tools.ToolDesignGraphRead,
	tools.ToolCaseGraphPropose,
	tools.ToolSUTList,
	tools.ToolSUTPageList,
}

var designCriticTools = []string{
	tools.ToolContextTaskPack,
	tools.ToolDesignGraphRead,
	tools.ToolDesignReviewSubmit,
}

var dataPlannerTools = []string{
	tools.ToolContextTaskPack,
	tools.ToolDesignGraphRead,
	tools.ToolDesignDataPlanSubmit,
}

// P2Profiles returns fresh P2 seed definitions. Budgets and tool sets are
// frozen by the P2 profile contract; EnsureProfiles never overwrites admin
// edits.
func P2Profiles() []agent.AgentProfile {
	return []agent.AgentProfile{
		{
			Key:          ProfileKeyDesignHost,
			Name:         "设计 Host",
			SystemPrompt: designHostPrompt,
			AllowedTools: append([]string(nil), designHostTools...),
			Budgets:      agent.ProfileBudgets{MaxTurns: 12, SoftSeconds: 300, HardSeconds: 600},
			Enabled:      true,
		},
		{
			Key:          ProfileKeyDesignPlanner,
			Name:         "设计 Planner",
			SystemPrompt: designPlannerPrompt,
			AllowedTools: append([]string(nil), designPlannerTools...),
			Budgets:      agent.ProfileBudgets{MaxTurns: 6, SoftSeconds: 180, HardSeconds: 300},
			Enabled:      true,
		},
		{
			Key:          ProfileKeyCaseAuthor,
			Name:         "用例 Author",
			SystemPrompt: caseAuthorPrompt,
			AllowedTools: append([]string(nil), caseAuthorTools...),
			Budgets:      agent.ProfileBudgets{MaxTurns: 8, SoftSeconds: 300, HardSeconds: 600},
			Enabled:      true,
		},
		{
			Key:          ProfileKeyDesignCritic,
			Name:         "设计 Critic",
			SystemPrompt: designCriticPrompt,
			AllowedTools: append([]string(nil), designCriticTools...),
			Budgets:      agent.ProfileBudgets{MaxTurns: 4, SoftSeconds: 120, HardSeconds: 300},
			Enabled:      true,
		},
		{
			Key:          ProfileKeyDataPlanner,
			Name:         "数据 Planner",
			SystemPrompt: dataPlannerPrompt,
			AllowedTools: append([]string(nil), dataPlannerTools...),
			Budgets:      agent.ProfileBudgets{MaxTurns: 4, SoftSeconds: 120, HardSeconds: 300},
			Enabled:      true,
		},
	}
}

// Profiles returns every seed profile (P1 then P2 then P3 then P4) in startup
// seeding order. P4 profiles are appended without duplicating a key that an
// earlier phase already seeds; EnsureProfiles still returns existing database
// rows unchanged so administrator edits always win.
func Profiles() []agent.AgentProfile {
	profiles := P1Profiles()
	profiles = append(profiles, P2Profiles()...)
	profiles = append(profiles, P3Profiles()...)
	seen := make(map[string]struct{}, len(profiles)+len(P4Profiles()))
	for _, profile := range profiles {
		seen[profile.Key] = struct{}{}
	}
	for _, profile := range P4Profiles() {
		if _, duplicate := seen[profile.Key]; duplicate {
			continue
		}
		seen[profile.Key] = struct{}{}
		profiles = append(profiles, profile)
	}
	return profiles
}

// EnsureProfiles idempotently seeds the P1, P2, P3 and P4 profiles plus the P5
// and P7 allowed_tools increments. Existing rows (including administrator
// edits) are returned unchanged by the repository.
func EnsureProfiles(ctx context.Context, repo agent.RuntimeRepository) ([]agent.AgentProfile, error) {
	seeds := Profiles()
	for i := range seeds {
		seeds[i] = applyP5ProfileTools(seeds[i])
		seeds[i] = applyP7ProfileTools(seeds[i])
	}
	stored := make([]agent.AgentProfile, 0, len(seeds))
	for _, seed := range seeds {
		profile, _, err := repo.EnsureProfile(ctx, seed)
		if err != nil {
			return nil, err
		}
		stored = append(stored, profile)
	}
	return stored, nil
}
