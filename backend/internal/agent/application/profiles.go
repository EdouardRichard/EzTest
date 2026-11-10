package agentapp

import (
	"context"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// P1 profile keys (contracts/agent-profiles-tools.md §2). v0.22 D-B1 removes the
// requirement Curator and context Scout seeds: requirement drafting is a Host
// tool and attachments move to the deterministic server pipeline, so the
// clarification domain exposes exactly one Host.
const ProfileKeyClarificationHost = "clarification_host"

// P2 conversation/design profile keys (contracts/agent-profiles-tools.md P2
// increment, converged by v0.22 D-B1/R-B1-2): two Hosts plus two background
// jobs. The jobs only write artifacts/job cards and never append assistant
// messages; the removed Planner seeds are gone.
const (
	ProfileKeyDesignHost   = "design_host"
	ProfileKeyDesignAuthor = "design_author"
	ProfileKeyDesignCritic = "design_critic"
)

// P3 execution profile keys (contracts/agent-profiles-tools.md P3 increment).
// v0.22 R-B1-2/R-B1-6 keeps these execution/analysis profiles outside the
// two-Host conversation team.
const (
	ProfileKeyCaseExecutor    = "case_executor"
	ProfileKeyFailureAnalyst  = "failure_analyst"
	ProfileKeyCoverageAnalyst = "coverage_analyst"
)

// System prompts freeze the role boundaries: a Host talks only, the design
// Author produces case drafts/proposals and the Critic submits findings. The
// [profile:<key>] marker is used by the deterministic fake LLM and is ignored
// by real models.
const clarificationHostPrompt = `你是 EzTest 的澄清 Host（需求澄清助手）。
角色边界：
- 只与用户对话、解释、提问与复述；不确认需求、不删除需求、不修改已确认需求。
- 需要结构化需求时，直接用 requirement.draft_create / requirement.draft_update 生成或更新 draft 草案，不要声称已确认。
- 可以调用 context.summary_update 更新会话摘要与钉住事实。
- requirement.read 只是读取时刻的快照；若快照为空但会话已有 requirement_card，不要声称“未创建/需求数为 0”，以卡片与最新需求列表为准。
- 同一来源编号（如 FR-011）在版本内只对应一条 draft：需求列表/卡片已存在该编号时，直接用 requirement.draft_update 补全，不要再创建第二条。
- 资源目标（页面/API/version_sut）由 sut.list / sut.page_list 返回的十进制数字 id 表示，需求草案的 targets 必须使用这些 id；不要要求用户提供数字 ID，也不要接受 path/URL 作为目标。
- 附件图片的 caption 由平台视觉分析生成：先用 attachment.read_text 读取附件，并优先查看 images[].caption；caption 为空或 status=failed 时说明图片分析未完成，不要声称平台没有视觉能力，也不要臆测图片内容。
- 读取大附件时必须按 attachment.next_offset 继续，直到 attachment.text_complete=true；若只读了部分页，必须明确写“已读 X 字符/共 total_chars 字符”，不得把未读页当作附件解析缺失，也不得据此建议重新解析。images_truncated=true 时用 offset=0、image_offset=next_image_offset 续取 caption。
- 不能使用浏览器/执行器工具、不能确认需求或用例；确认只能由用户完成。
回答使用简洁中文；引用用户最新消息；每次最多提出 3 个澄清问题。
[profile:clarification_host]`

const designHostPrompt = `你是 EzTest 的设计 Host（用例脑图助手）。
角色边界：
- 只解释、提问与修改脑图；不确认用例、不删除、不关闭版本或需求。
- AI 只能提交 draft/候选：场景与用例草案通过 case.graph_propose 写入，等待用户在脑图中确认。
- 收到修改请求时：先 design.graph_read 读取目标用例一次，然后在同一轮内立即用 case.graph_propose 提交完整修改；不要只回复修改计划、不要等待用户二次授权。
- 每条步骤与断言的 expected 必须写成可观察结果，不得留空；修改时保留未变更内容并补齐空 expected。
- 断言 kind 默认用 ui/text/api（需要数据库校验时 db）；只有需求或验收标准明确要求视觉基线或像素级截图比对时才用 visual；“页面不显示某标识/文案”用 text/ui 断言，不要用 visual。
- 场景名用需求标题原文、用例标题用对应验收要点原文；页面/API 来源只能引用版本已绑定的 page/api ID，未绑定时只用 requirement/knowledge 来源，不要编造 page/api 来源或 ref_id。
- 不声称已完成确认；用户要求确认时提示其使用脑图节点上的确认按钮。
- 设计对话支持附件；可读取附件文本作为设计依据。附件图片的 caption 由平台视觉分析生成：用 attachment.read_text 读取附件并优先查看 images[].caption；caption 为空或 status=failed 时说明图片分析未完成，不要声称平台没有视觉能力，也不要臆测图片内容。
- 大附件必须按 attachment.next_offset 读到 text_complete=true；只读部分页时不得断言附件缺失或建议重新解析。
回答使用简洁中文，聚焦用例覆盖与可执行性。
[profile:design_host]`

const designAuthorPrompt = `你是 EzTest 的设计 Author（后台作业，场景/用例作者）。
角色边界：
- 按任务上下文（task pack、交接包摘要、当前图/用例、目标）生成或修订 case revision 与 graph proposal；只写产物表与作业卡，不写 assistant 消息、不进入 Host 对话。
- 只能通过 case.graph_propose 提交场景与用例 draft；不能确认、删除、关闭用例或需求。
- 先在 design.graph_read 读取当前脑图，避免重复创建；同一需求版本内修改已有用例时传既有 ID/自然键。
- 场景名用需求标题原文、用例标题用对应验收要点原文，降低与同轮设计重复的概率；同一自然键已存在时更新而不是重复创建。
- 用例必须包含可执行步骤、断言、来源与目标 version_sut；跨系统步骤分别指定目标。
- 断言 kind 默认用 ui/text/api（需要数据库校验时 db）；只有需求或验收标准明确要求视觉基线或像素级截图比对时才用 visual；“页面不显示某标识/文案”用 text/ui 断言，不要用 visual。
- 断言必须逐条来自该需求/验收要点的原文：不得添加验收标准未要求的 UI 细节（额外列名、字段、颜色、布局、控件数量）；验收标准没写的“完整列集合/作者列/固定列数”不得作为硬断言。
- 若验收标准写明平台/视口/端（如“移动端支持”“桌面端”），步骤必须在该条件下执行（指定对应 page/viewport），不得在桌面视口断言只属于移动端的行为；无法满足条件时写入 draft 并在 finding 说明，不得硬断言。
- 上传类步骤用 browser.upload_file（base64 文件内容注入文件输入框）并用 snapshot/screenshot 验证插入结果，不得点击需要原生文件选择框的按钮；响应式/移动端 AC 先用 browser.set_viewport(width,height) 切到对应视口再设计操作与断言，无法满足时写 draft 并说明。
- 数量/集合类断言必须基于执行时的实时数据（如 API 返回的 total/ids）或行为关系（并集=单标签之和−交集、无重复）推导，禁止硬编码验收标准示例里的具体条数（除非环境明确保证固定数据集）；数据不满足示例时写 draft/finding，不得把数据差异判成产品缺陷。
- 不编造需求未表达的内容；拿不准时写入 draft 并在团队频道说明假设。
[profile:design_author]`

const designCriticPrompt = `你是 EzTest 的 Design Critic（后台作业，内部设计自审者）。
角色边界：
- 通过 design.review_submit 提交 finding（blocker/important/minor），只读脑图；不修改用例、不确认。
- 内部自审不构成人工关卡；确认权限只属于用户。
- 只写 finding/作业卡，不写 assistant 消息、不进入 Host 对话。
- 检查覆盖遗漏、步骤不可执行、断言缺失、目标 SUT 错误与重复用例。
- 检查断言范围：出现验收标准未要求的额外 UI 细节断言（如“必须包含作者列/完整列集合/固定控件数”）即报 finding；断言必须能追溯到 AC 原文。
- 检查端/视口条件：AC 写明移动端/桌面端而用例在错误视口执行，或依赖执行器不支持的文件选择框，即报 finding。
- 检查断言 kind：无显式视觉基线要求的用例不得使用 visual；出现即报 finding。
输出简洁、可执行的 finding。
[profile:design_critic]`

// clarificationHostTools is the Host read/write set (contract §2).
var clarificationHostTools = []string{
	tools.ToolContextVersionPack,
	tools.ToolContextTaskPack,
	tools.ToolContextTeamRoomRead,
	tools.ToolContextSummaryUpdate,
	tools.ToolRequirementRead,
	tools.ToolRequirementDraftCreate,
	tools.ToolRequirementDraftUpdate,
	tools.ToolRequirementRelationPropose,
	tools.ToolRequirementReopenPropose,
	tools.ToolAttachmentList,
	tools.ToolAttachmentReadText,
	tools.ToolSUTList,
	tools.ToolSUTPageList,
}

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

var designAuthorTools = []string{
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

// v022ProfileTools is the v0.22 W0 allowed_tools increment per existing profile
// key (spec §4.3/§4.6/§4.8/§4.9/§4.15/§4.22). It is applied as a set union in
// EnsureProfiles so the frozen per-phase seed lists stay readable and an
// administrator-edited stored profile can still be validated against the role
// whitelist. Every name here is registered by the composition root; a tool whose
// production port is not wired is absent from the registry and the profile entry
// is harmless.
var v022ProfileTools = map[string][]string{
	ProfileKeyClarificationHost: {
		tools.ToolPlanUpdate,
		tools.ToolQuestionAsk,
		tools.ToolClarificationSearch,
		tools.ToolTaskComplete,
		tools.ToolAttachmentImageRef,
		tools.ToolToolResultRead,
	},
	ProfileKeyDesignHost: {
		tools.ToolPlanUpdate,
		tools.ToolQuestionAsk,
		tools.ToolClarificationSearch,
		tools.ToolTaskComplete,
		tools.ToolAttachmentImageRef,
		tools.ToolSUTDatabaseList,
		tools.ToolToolResultRead,
	},
	ProfileKeyDesignAuthor: {
		tools.ToolSUTDatabaseList,
		tools.ToolTaskComplete,
	},
	ProfileKeyDesignCritic: {
		tools.ToolSUTDatabaseList,
		tools.ToolTaskComplete,
	},
	ProfileKeyCaseExecutor: {
		tools.ToolToolResultRead,
		tools.ToolTaskComplete,
	},
}

// V022ProfileTools returns a copy of the v0.22 per-profile tool increment.
func V022ProfileTools() map[string][]string {
	cloned := make(map[string][]string, len(v022ProfileTools))
	for key, names := range v022ProfileTools {
		cloned[key] = append([]string(nil), names...)
	}
	return cloned
}

// applyV022ProfileTools merges the v0.22 increment into one seed profile.
func applyV022ProfileTools(profile agent.AgentProfile) agent.AgentProfile {
	return mergeProfileTools(profile, v022ProfileTools[profile.Key])
}

// P1Profiles returns fresh seed definitions. The clarification Host carries the
// v0.22 R-A2-2 envelope (12 turns / 600s soft / 900s hard); EnsureProfiles never
// overwrites admin edits. v0.22 D-B1 removed the requirement Curator seed.
func P1Profiles() []agent.AgentProfile {
	return []agent.AgentProfile{
		{
			Key:          ProfileKeyClarificationHost,
			Name:         "澄清 Host",
			SystemPrompt: clarificationHostPrompt,
			AllowedTools: append([]string(nil), clarificationHostTools...),
			// v0.22 R-A2-2: multi-turn clarification must not be cut off by the
			// old 300s soft / 600s hard chat clock; tokens are telemetry only.
			Budgets: agent.ProfileBudgets{MaxTurns: 12, SoftSeconds: 600, HardSeconds: 900},
			Enabled: true,
		},
	}
}

// P2Profiles returns fresh P2 seed definitions. The conversation/design domain
// is exactly two Hosts (clarification_host, design_host) plus the two background
// jobs (design_author, design_critic); the removed Planner seeds are gone. The
// design critic keeps its 4-turn guard with the design team's 600/900 envelope.
// EnsureProfiles never overwrites admin edits.
func P2Profiles() []agent.AgentProfile {
	return []agent.AgentProfile{
		{
			Key:          ProfileKeyDesignHost,
			Name:         "设计 Host",
			SystemPrompt: designHostPrompt,
			AllowedTools: append([]string(nil), designHostTools...),
			// The Host edits existing cases through a full case.graph_read +
			// case.graph_propose round trip inside one thread; the chat-scale
			// budget settled before the write in the final S4 run.
			Budgets: agent.ProfileBudgets{MaxTurns: 16, SoftSeconds: 600, HardSeconds: 900},
			Enabled: true,
		},
		{
			Key:          ProfileKeyDesignAuthor,
			Name:         "设计 Author",
			SystemPrompt: designAuthorPrompt,
			AllowedTools: append([]string(nil), designAuthorTools...),
			// Task 79: the per-requirement author needs the read, propose and
			// summary turns; one 67-requirement round runs the author once per
			// requirement, so the seed matches the design team envelope.
			Budgets: agent.ProfileBudgets{MaxTurns: 16, SoftSeconds: 600, HardSeconds: 900},
			Enabled: true,
		},
		{
			Key:          ProfileKeyDesignCritic,
			Name:         "设计 Critic",
			SystemPrompt: designCriticPrompt,
			AllowedTools: append([]string(nil), designCriticTools...),
			// v0.22 R-A2-2: the critic keeps its 4-turn guard but shares the
			// design team's 600s soft / 900s hard clock; tokens are telemetry.
			Budgets: agent.ProfileBudgets{MaxTurns: 4, SoftSeconds: 600, HardSeconds: 900},
			Enabled: true,
		},
	}
}

// Profile returns one built-in seed definition by key. The lookup covers the
// startup seeds only: removed v0.22 profiles never resolve, and an
// administrator-edited database row does not widen this catalog lookup.
func Profile(key string) (agent.AgentProfile, bool) {
	for _, profile := range Profiles() {
		if profile.Key == key {
			return profile, true
		}
	}
	return agent.AgentProfile{}, false
}

// Profiles returns every seed profile (P1 then P2 then P3 then P4 then the P7
// background role seed) in startup seeding order. Later phases are appended
// without duplicating a key that an earlier phase already seeds; EnsureProfiles
// still returns existing database rows unchanged so administrator edits always
// win.
func Profiles() []agent.AgentProfile {
	profiles := P1Profiles()
	profiles = append(profiles, P2Profiles()...)
	profiles = append(profiles, P3Profiles()...)
	seen := make(map[string]struct{}, len(profiles)+len(P4Profiles())+len(P7Profiles()))
	for _, profile := range profiles {
		seen[profile.Key] = struct{}{}
	}
	appended := append(P4Profiles(), P7Profiles()...)
	for _, profile := range appended {
		if _, duplicate := seen[profile.Key]; duplicate {
			continue
		}
		seen[profile.Key] = struct{}{}
		profiles = append(profiles, profile)
	}
	return profiles
}

// EnsureProfiles idempotently seeds the P1, P2, P3 and P4 profiles plus the P7
// knowledge_curator role profile and the P5 and P7 allowed_tools increments.
// Existing rows (including administrator edits) are returned unchanged by the
// repository. The assembled seeds are validated against the role tool
// whitelist before the first write.
func EnsureProfiles(ctx context.Context, repo agent.RuntimeRepository) ([]agent.AgentProfile, error) {
	seeds := Profiles()
	for i := range seeds {
		seeds[i].SeedVersion = ProfileSeedVersion
		seeds[i] = applyP5ProfileTools(seeds[i])
		seeds[i] = applyP7ProfileTools(seeds[i])
		seeds[i] = applyV022ProfileTools(seeds[i])
	}
	// The role catalog is the outer permission boundary: a seed profile that
	// executes a catalog role must stay inside that role's tool whitelist. Fail
	// closed before writing so a profile/tool edit cannot silently widen a role
	// (0.2.1 review focus 5).
	if err := ValidateRoleProfiles(seeds); err != nil {
		return nil, err
	}
	stored := make([]agent.AgentProfile, 0, len(seeds))
	for _, seed := range seeds {
		profile, _, err := repo.EnsureProfile(ctx, seed)
		if err != nil {
			return nil, err
		}
		stored = append(stored, profile)
	}
	// Existing rows (including administrator edits and rows widened before the
	// role guard existed) are returned unchanged by the repository, so they must
	// be validated too: a stored profile outside its role whitelist fails the
	// startup closed instead of reaching the runner (Task 8 fix round 1). The
	// runner call-site guard itself remains R51 / Task 10.
	if err := ValidateRoleProfiles(stored); err != nil {
		return nil, err
	}
	return stored, nil
}
