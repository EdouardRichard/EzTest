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

// EnsureProfiles idempotently seeds the P1 profiles. Existing rows (including
// administrator edits) are returned unchanged by the repository.
func EnsureProfiles(ctx context.Context, repo agent.RuntimeRepository) ([]agent.AgentProfile, error) {
	seeds := P1Profiles()
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
