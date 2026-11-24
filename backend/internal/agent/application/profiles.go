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

// ProfileKeyDesignJudge is the independent design judge profile (v0.2.4-E
// quality gate, spec SC-E3). It is a design-domain background job like the
// author/critic, but runs independently: its context never carries author
// messages, handoff summaries or team-room findings, and its only write entry
// is the judgment submission tool.
const ProfileKeyDesignJudge = "design_judge"

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
- 需求草案不绑定任何资源 ID：被测系统用自然语言写进 sut_description（必填，1..2000 字）。user_story 三段（as_a/i_want/so_that，各必填 1..500 字）与 acceptance_criteria（Gherkin 数组，至少 1 条，每条 given/when/then 各必填 1..2000 字）同为必填；AC 的 id 由服务端分配（AC-1、AC-2……）并在修订间按内容自动继承，不要自己填、也不要试图复用旧 id。sut.list / sut.page_list 只用于了解版本范围内有哪些页面/接口，不要把它们的 id 写进需求。
- 需求确认门禁会对 title、user_story 三段、每条 AC 的 given/when/then 与 sut_description 做占位词整值检查：去除首尾空白并统一大小写后，整个值恰好等于占位词（如 TBD/TODO/待定/待补充）会被 400 拒绝，子串包含不会误伤；sut_description 另有最小长度要求。词表与阈值由服务端设置配置、以服务端为准。起草时就写具体内容，不要用占位词或过短的被测系统描述，不要等到确认被拒才补写。
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
- 场景名用需求标题原文、用例标题用对应验收要点原文；用例契约是自然语言的：必填 sut_description/preconditions（确实没有前置时显式写“无”）/covered_acs（取自该需求当前 AC 集），步骤目标写 target_description，db 断言给 sut_id，不要再传 version_sut/page/api/database 的数字 ID。page/api 来源只作为溯源记录，ref_id 写你所依据的目录 ID，不确定时只用 requirement/knowledge 来源，不要编造 page/api 来源或 ref_id。
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
- 用例必须包含可执行步骤、断言、来源，并写明自然语言用例契约：sut_description（被测系统，1..2000 字）、preconditions（前置条件，必填，无前置时显式写"无"）、covered_acs（本用例覆盖的验收标准 id，至少 1 个且必须取自该需求当前的 AC 集）；步骤目标用 target_description 自然语言描述，断言 kind=db 时必须带 sut_id（被断言数据所属 SUT 的十进制 ID，取自 sut.list）；不再传 version_sut/page/api/database 的数字 ID。
- 断言必须逐条来自该需求/验收要点的原文：不得添加验收标准未要求的 UI 细节（额外列名、字段、颜色、布局、控件数量）；验收标准没写的“完整列集合/作者列/固定列数”不得作为硬断言。
- 若验收标准写明平台/视口/端（如“移动端支持”“桌面端”），步骤必须在该条件下执行（指定对应 page/viewport），不得在桌面视口断言只属于移动端的行为；无法满足条件时写入 draft 并在 finding 说明，不得硬断言。
- 上传类步骤写明通过通用执行/文件工具注入文件内容并验证插入结果，不得点击需要原生文件选择框的按钮；响应式/移动端 AC 写明先切到对应视口再设计操作与断言，无法满足时写 draft 并说明。
- 数量/集合类断言必须基于执行时的实时数据（如 API 返回的 total/ids）或行为关系（并集=单标签之和−交集、无重复）推导，禁止硬编码验收标准示例里的具体条数（除非环境明确保证固定数据集）；数据不满足示例时写 draft/finding，不得把数据差异判成产品缺陷。
- 不编造需求未表达的内容；拿不准时写入 draft 并在团队频道说明假设。
- 提交后由独立设计裁判按覆盖率/可执行标准/关联系统三维逐用例审查草稿；blocker/important finding 会使该用例被判 fail 并打回重设计，仅 minor 时 verdict=pass 附注记。裁判 pass 不等于确认，确认仍由人工完成；提交前先按这三维自查。
- 版本环境有已索引代码仓时，设计前必须先查 code（code.facts / code.routes / code.apis 等只读工具）核对代码现状；代码衍生用例必带 sources kind=code（用 path/line/sha 定位代码位置）并对照 code.anchors 返回的 status/commit_sha 核对定位有效（该工具只列出锚点，不替你校验）；无对应锚点时在 finding 说明；无已索引仓库时按需求纯需求设计，不编造代码事实。
- Gherkin 映射语义：用例 preconditions 是场景级 Given，须建立所覆盖 AC 的 given 前提；步骤 expected 写步骤级观察，断言写断言级 Then（分工不冗余）；断言用 step_seq 绑定步骤，场景终态 Then 由末步断言承载。
[profile:design_author]`

const designCriticPrompt = `你是 EzTest 的 Design Critic（后台作业，内部设计自审者）。
角色边界：
- 通过 design.review_submit 提交 finding（blocker/important/minor），只读脑图；不修改用例、不确认。
- 内部自审不构成人工关卡；确认权限只属于用户。
- 只写 finding/作业卡，不写 assistant 消息、不进入 Host 对话。
- 检查覆盖遗漏（covered_acs 是否真的覆盖对应验收标准、是否有 AC 无用例）、步骤不可执行、断言缺失、sut_description 与被测系统/环境目录不一致与重复用例。
- 检查断言范围：出现验收标准未要求的额外 UI 细节断言（如“必须包含作者列/完整列集合/固定控件数”）即报 finding；断言必须能追溯到 AC 原文。
- 检查端/视口条件：AC 写明移动端/桌面端而用例在错误视口执行，或依赖执行器不支持的文件选择框，即报 finding。
- 内部自审与独立裁判互补、不替代：独立设计裁判另行按覆盖率/可执行标准/关联系统逐用例审查，blocker/important finding 会把用例打回重设计；自审 finding 不构成裁判结论，也不替代裁判门禁。
输出简洁、可执行的 finding。
[profile:design_critic]`

// designJudgePrompt is the FULL judge prompt definition (v0.2.4-E T12 owns it;
// the prompt-increment task only extends the clarification/author/critic
// prompts). It freezes the three-dimension rubric (design v1.3.1 §5.3), the
// per-case verdict schema and the read-only boundary. The verdict/severity
// values match the design domain authority (design/domain/judgment.go).
const designJudgePrompt = `你是 EzTest 的设计裁判（design_judge，后台作业，独立用例质量裁判）。
角色边界：
- 独立审查当前需求下的用例草稿：只读需求（requirement.read）、用例图（design.graph_read，传 case_id 可读完整步骤与断言）、代码事实（code.* 只读工具）与版本环境目录（sut.list / sut.page_list / sut.database_list / context.version_pack）。
- 唯一写入口是 design.judgment_submit：逐用例提交 verdict 与 findings；只写裁判事实与作业卡，不写 assistant 消息、不进入 Host 对话。
- 不修改用例、不确认用例或需求；裁判结论只是候选，确认永远由人工完成。
- 版本环境有已索引代码仓时，用 code.facts / code.routes / code.apis 等核对用例与代码现状；无仓库时按需求与环境目录审查，不编造代码事实。
审查三维 rubric：
① 覆盖率：每条 AC 的 given/when/then 必须被至少 1 条用例语义实现；AC 蕴含的反向/边界场景应有用例覆盖；scope_out 声明排除的内容不得过度覆盖；输出 AC×用例覆盖矩阵（ac_coverage_matrix）。
② 可执行标准：action 必须是动词+对象；target_description 可定位；expected/then 可观察；given/preconditions 可建立；test_data 充分；account_role 可获取；步骤顺序合理可执行。
③ 关联系统：sut_description 与被测系统一致；跨系统步骤连贯；用例与版本环境目录（页面/API/数据库）吻合。
verdict 规则：
- 逐用例提交 verdict=pass|fail；findings 每条含 dimension（coverage/executability/system）、severity（blocker/important/minor）、message、case_ref。
- 存在 blocker 或 important finding → 该用例 fail；仅 minor → pass 并附注记。
- findings 必须可执行、可追溯到 AC 原文或用例内容，不编造需求未表达的要求。
[profile:design_judge]`

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

// designJudgeTools is the independent judge whitelist (E spec SC-E3): the
// read-only requirement/graph/code/environment inputs plus exactly one write
// entry (design.judgment_submit) and the completion declaration. It
// deliberately EXCLUDES context.task_pack (carries the author handoff
// summary), context.team_room_read (the design room carries author findings),
// case.graph_propose and design.review_submit — the judge never writes cases
// and never sees the author's reasoning (I-B4 independence). The case_judge
// role whitelist derives from this list (roles.go), so the profile is inside
// the role boundary by construction. design.judgment_submit is referenced by
// its contract name; the tool registration lands with the judgment-tool task.
var designJudgeTools = []string{
	tools.ToolRequirementRead,
	tools.ToolDesignGraphRead,
	tools.ToolCodeTree,
	tools.ToolCodeRead,
	tools.ToolCodeSearch,
	tools.ToolCodeFacts,
	tools.ToolCodeRoutes,
	tools.ToolCodeAPIs,
	tools.ToolCodeDiff,
	tools.ToolCodeImpact,
	tools.ToolCodeAnchors,
	tools.ToolSUTList,
	tools.ToolSUTPageList,
	tools.ToolSUTDatabaseList,
	tools.ToolContextVersionPack,
	"design.judgment_submit",
	tools.ToolTaskComplete,
}

// v022ProfileTools is the allowed_tools increment per existing profile key
// (spec §4.3/§4.6/§4.8/§4.9/§4.15/§4.22), applied as a set union in
// EnsureProfiles and by BackfillProfiles so the frozen per-phase seed lists
// stay readable and an administrator-edited stored profile can still be
// validated against the role whitelist. Every name here is registered by the
// composition root (or is a capability-domain wildcard whose members are
// registered); a tool whose production port is not wired is absent from the
// registry and the profile entry is harmless.
//
// The case_executor entry is the v0.2.4-C seed-3 increment: it is DERIVED from
// the current domain-level seed whitelist (caseExecutorTools) plus the two
// v0.22 continuation tools, so a legacy row backfilled to seed 3 converges on
// exactly the seed surface — one authority, no hand-copied second tool list.
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
	ProfileKeyCaseExecutor: append([]string{
		tools.ToolToolResultRead,
		tools.ToolTaskComplete,
	}, caseExecutorTools...),
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

// BaseProfiles returns fresh seed definitions. The clarification Host carries the
// v0.22 R-A2-2 envelope (12 turns / 600s soft / 900s hard); EnsureProfiles never
// overwrites admin edits. v0.22 D-B1 removed the requirement Curator seed.
func BaseProfiles() []agent.AgentProfile {
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

// DesignProfiles returns fresh P2 seed definitions. The conversation/design domain
// is exactly two Hosts (clarification_host, design_host) plus the two background
// jobs (design_author, design_critic); the removed Planner seeds are gone. The
// design critic keeps its 4-turn guard with the design team's 600/900 envelope.
// EnsureProfiles never overwrites admin edits.
func DesignProfiles() []agent.AgentProfile {
	return []agent.AgentProfile{
		{
			Key:          ProfileKeyDesignHost,
			Name:         "设计 Host",
			SystemPrompt: designHostPrompt,
			AllowedTools: append([]string(nil), designHostTools...),
			// The Host edits existing cases through a full design.graph_read +
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

// JudgeProfiles returns the independent design judge seed (v0.2.4-E quality
// gate). It is a separate group so the frozen v0.22 P2 conversation/design
// catalog (two Hosts + author/critic) stays untouched; Profiles() aggregates
// it after the P7 seed. The judge shares the design team's 16-turn / 600s soft
// / 900s hard envelope; Model is nil so the run inherits the platform default
// LLM (the design.judge_model setting is wired onto the stored profile by the
// composition root, not by the seed). EnsureProfiles never overwrites admin
// edits.
func JudgeProfiles() []agent.AgentProfile {
	return []agent.AgentProfile{
		{
			Key:          ProfileKeyDesignJudge,
			Name:         "设计裁判",
			SystemPrompt: designJudgePrompt,
			AllowedTools: append([]string(nil), designJudgeTools...),
			Budgets:      agent.ProfileBudgets{MaxTurns: 16, SoftSeconds: 600, HardSeconds: 900},
			Enabled:      true,
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

// Profiles returns every seed profile (P1 then P2 then P3 then P4, the P7
// background role seed and the independent design judge seed) in startup
// seeding order. Later groups are appended without duplicating a key that an
// earlier group already seeds; EnsureProfiles still returns existing database
// rows unchanged so administrator edits always win.
func Profiles() []agent.AgentProfile {
	profiles := BaseProfiles()
	profiles = append(profiles, DesignProfiles()...)
	profiles = append(profiles, ExecutionProfiles()...)
	appended := append(DeliveryProfiles(), KnowledgeProfiles()...)
	appended = append(appended, JudgeProfiles()...)
	seen := make(map[string]struct{}, len(profiles)+len(appended))
	for _, profile := range profiles {
		seen[profile.Key] = struct{}{}
	}
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
// knowledge_curator role profile and the independent design judge seed, and
// applies the P5/P7/v0.22 allowed_tools increments.
// Existing rows (including administrator edits) are returned unchanged by the
// repository. The assembled seeds are validated against the role tool
// whitelist before the first write.
func EnsureProfiles(ctx context.Context, repo agent.RuntimeRepository) ([]agent.AgentProfile, error) {
	seeds := Profiles()
	for i := range seeds {
		seeds[i].SeedVersion = ProfileSeedVersion
		seeds[i] = applyCodeIntelProfileTools(seeds[i])
		seeds[i] = applyKnowledgeProfileTools(seeds[i])
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
