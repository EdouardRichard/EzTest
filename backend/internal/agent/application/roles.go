package agentapp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// RoleKey is one of the seven 0.2.1 role-catalog keys (spec §5.2/§5.8, D19).
// The keys are fixed: assistant + six background roles.
type RoleKey string

// The seven D19 role keys.
const (
	RoleAssistant        RoleKey = "assistant"
	RoleCaseAuthor       RoleKey = "case_author"
	RoleCaseReviewer     RoleKey = "case_reviewer"
	RoleExplorer         RoleKey = "explorer"
	RoleFailureAnalyst   RoleKey = "failure_analyst"
	RoleReportWriter     RoleKey = "report_writer"
	RoleKnowledgeCurator RoleKey = "knowledge_curator"
)

// roleKeys is the frozen catalog order: the assistant first, then the six
// background roles.
var roleKeys = []RoleKey{
	RoleAssistant,
	RoleCaseAuthor,
	RoleCaseReviewer,
	RoleExplorer,
	RoleFailureAnalyst,
	RoleReportWriter,
	RoleKnowledgeCurator,
}

// RoleKeys returns a copy of the seven catalog keys in catalog order.
func RoleKeys() []RoleKey { return append([]RoleKey(nil), roleKeys...) }

// Valid reports whether the key is one of the seven D19 roles.
func (r RoleKey) Valid() bool {
	for _, key := range roleKeys {
		if key == r {
			return true
		}
	}
	return false
}

// RoleSpec is one catalog entry: the role's product name, model capability
// requirements (spec §7.3), work-item kinds, tool whitelist, existing profile
// seeds that execute it, system prompt and default work-item budget (§5.4).
type RoleSpec struct {
	Key           RoleKey
	Name          string
	Model         llmapp.ModelRequirements
	WorkItemKinds []string
	Tools         []string
	Profiles      []string
	SystemPrompt  string
	MaxTokens     int
	RunSeconds    int
	MaxTurnsFuse  int
}

// The role system prompts freeze the spec §5.2 boundaries. The [role:<key>]
// marker follows the existing [profile:<key>] convention so deterministic test
// doubles can identify a role while real models ignore the marker.
const assistantRolePrompt = `你是 EzTest 的测试助手（assistant）。
角色边界：
- 只与用户对话：澄清需求、解释设计、复述决定；需求草稿与用例修改以 draft/候选提交，等待用户确认。
- 通过 job.start 只能请求 design.cases / review.cases；不能请求 execute.batch、report.version 或 explore.*。
- 不能确认需求/用例、不能直接执行、不能使用浏览器/执行器工具、不能修改资源；确认只能由用户 API 触发。
- 拿不准时提问并给出默认假设，不编造用户未表达的内容。
[role:assistant]`

const caseAuthorRolePrompt = `你是 EzTest 的用例编写员（case_author）。
角色边界：
- 只生成/修改用例草稿（case.graph_propose）与测试数据草案；不能确认、执行或修改需求。
- 需求确认后按 design.cases 范围生成用例，步骤、断言、来源与目标 SUT 必须完整可执行。
- 不调用 browser.* / execution.* / case.confirm；确认只能由用户完成。
[role:case_author]`

const caseReviewerRolePrompt = `你是 EzTest 的用例评审员（case_reviewer）。
角色边界：
- 只通过 design.review_submit 提交评审发现（blocker/important/minor），可读取设计图。
- 不能修改用例、不能确认、不能执行；内部评审不构成人工关卡。
- 检查覆盖遗漏、步骤不可执行、断言缺失、目标 SUT 错误与重复用例。
[role:case_reviewer]`

const explorerRolePrompt = `你是 EzTest 的探索者（explorer）。
角色边界：
- 只执行 explore.* 探索意图，产出版本事实、证据与会话引用；供料不决策。
- 不生成用例、不修改需求、不运行已确认用例；不使用浏览器/执行器写工具或确认工具。
- 探索批量有上限，失败时记录明确原因，不越界。
[role:explorer]`

const failureAnalystRolePrompt = `你是 EzTest 的失败分析师（failure_analyst）。
角色边界：
- 只分析 analyze.failure 的失败证据与执行结果，产出归因与问题单草稿；不重跑、不修改用例。
- 只读取执行结果/证据与相关代码事实；不得虚构证据或统计。
- 不使用浏览器/执行器写工具，不确认需求或用例。
[role:failure_analyst]`

const reportWriterRolePrompt = `你是 EzTest 的报告撰写员（report_writer）。
角色边界：
- 只读取当前任务的确定性统计并提交报告叙述候选（report.narrative_propose）；不执行、不改数据。
- 统计数字、覆盖率与证据以服务端快照为准，不得篡改或伪造。
- 不确认需求/用例、不修改问题、不关闭版本。
[role:report_writer]`

const knowledgeCuratorRolePrompt = `你是 EzTest 的知识维护员（knowledge_curator）。
角色边界：
- 只通过 knowledge.propose 沉淀知识草稿（生效/失效由审核流程处理）；不改需求、不改用例、不执行。
- 知识内容来自已确认事实与成功经验，不包含凭据、内部路径或用户隐私。
- 不确认需求/用例，不运行测试，不直接改变知识审核状态。
[role:knowledge_curator]`

// 0.2.1 default work-item budget (spec §5.4): max_tokens 20000, run_seconds 120,
// max_turns_fuse 24. The completion criterion remains primary (§5.7).
const (
	roleDefaultMaxTokens    = 20000
	roleDefaultRunSeconds   = 120
	roleDefaultMaxTurnsFuse = 24
)

// roleSpecs is the immutable 7-role catalog. Tool entries are exact registered
// names or "namespace.*" wildcards for tools later phases register (explore.*).
// Profiles lists the seed profiles that execute the role; every role has at
// least one (knowledge_curator is seeded by the P7 knowledge increment).
var roleSpecs = []RoleSpec{
	{
		Key:   RoleAssistant,
		Name:  "测试助手",
		Model: llmapp.ModelRequirements{Tools: true, Vision: true, Reasoning: true},
		WorkItemKinds: []string{
			agent.WorkItemKindDesignCases,
			agent.WorkItemKindReviewCases,
		},
		Tools: []string{
			tools.ToolContextVersionPack,
			tools.ToolContextTaskPack,
			tools.ToolContextTeamRoomRead,
			tools.ToolContextSummaryUpdate,
			tools.ToolRequirementRead,
			tools.ToolRequirementDraftCreate,
			tools.ToolRequirementDraftUpdate,
			tools.ToolRequirementRelationPropose,
			tools.ToolRequirementReopenPropose,
			tools.ToolDesignGraphRead,
			tools.ToolCaseGraphPropose,
			tools.ToolAttachmentList,
			tools.ToolAttachmentReadText,
			tools.ToolSUTList,
			tools.ToolSUTPageList,
			tools.ToolCodeFacts,
			tools.ToolCodeRoutes,
			tools.ToolCodeAPIs,
			tools.ToolKnowledgePropose,
		},
		Profiles: []string{
			ProfileKeyClarificationHost,
			ProfileKeyRequirementCurator,
			ProfileKeyDesignHost,
		},
		SystemPrompt: assistantRolePrompt,
		MaxTokens:    roleDefaultMaxTokens,
		RunSeconds:   roleDefaultRunSeconds,
		MaxTurnsFuse: roleDefaultMaxTurnsFuse,
	},
	{
		Key:   RoleCaseAuthor,
		Name:  "用例编写员",
		Model: llmapp.ModelRequirements{Tools: true, Reasoning: true},
		WorkItemKinds: []string{
			agent.WorkItemKindDesignCases,
		},
		Tools: []string{
			tools.ToolContextVersionPack,
			tools.ToolContextTaskPack,
			tools.ToolContextTeamRoomRead,
			tools.ToolRequirementRead,
			tools.ToolDesignGraphRead,
			tools.ToolCaseGraphPropose,
			tools.ToolDesignDataPlanSubmit,
			tools.ToolSUTList,
			tools.ToolSUTPageList,
			tools.ToolCodeFacts,
			tools.ToolCodeRoutes,
			tools.ToolCodeAPIs,
			tools.ToolCodeDiff,
			tools.ToolCodeImpact,
			tools.ToolCodeAnchors,
			tools.ToolKnowledgePropose,
		},
		Profiles: []string{
			ProfileKeyCaseAuthor,
			ProfileKeyDesignPlanner,
			ProfileKeyDataPlanner,
		},
		SystemPrompt: caseAuthorRolePrompt,
		MaxTokens:    roleDefaultMaxTokens,
		RunSeconds:   roleDefaultRunSeconds,
		MaxTurnsFuse: roleDefaultMaxTurnsFuse,
	},
	{
		Key:   RoleCaseReviewer,
		Name:  "用例评审员",
		Model: llmapp.ModelRequirements{Tools: true, Reasoning: true},
		WorkItemKinds: []string{
			agent.WorkItemKindReviewCases,
		},
		Tools: []string{
			tools.ToolContextTaskPack,
			tools.ToolDesignGraphRead,
			tools.ToolDesignReviewSubmit,
		},
		Profiles: []string{
			ProfileKeyDesignCritic,
		},
		SystemPrompt: caseReviewerRolePrompt,
		MaxTokens:    roleDefaultMaxTokens,
		RunSeconds:   roleDefaultRunSeconds,
		MaxTurnsFuse: roleDefaultMaxTurnsFuse,
	},
	{
		Key:   RoleExplorer,
		Name:  "探索者",
		Model: llmapp.ModelRequirements{Tools: true, Vision: true, Reasoning: true},
		WorkItemKinds: []string{
			agent.WorkItemKindExploreLogin,
			agent.WorkItemKindExplorePage,
			agent.WorkItemKindExploreAPI,
			agent.WorkItemKindExploreCoverage,
			agent.WorkItemKindExploreGap,
		},
		Tools: []string{
			"explore.*",
			tools.ToolContextVersionPack,
			tools.ToolContextTaskPack,
			tools.ToolContextTeamRoomRead,
			tools.ToolRequirementRead,
			tools.ToolAttachmentList,
			tools.ToolAttachmentReadText,
			tools.ToolAttachmentImageRef,
			tools.ToolSUTList,
			tools.ToolSUTPageList,
			tools.ToolSUTPagePropose,
			tools.ToolCodeTree,
			tools.ToolCodeRead,
			tools.ToolCodeSearch,
			tools.ToolCodeFacts,
			tools.ToolCodeRoutes,
			tools.ToolCodeAPIs,
			tools.ToolCodeDiff,
			tools.ToolCodeImpact,
			tools.ToolKnowledgePropose,
		},
		Profiles: []string{
			ProfileKeyContextScout,
		},
		SystemPrompt: explorerRolePrompt,
		MaxTokens:    roleDefaultMaxTokens,
		RunSeconds:   roleDefaultRunSeconds,
		MaxTurnsFuse: roleDefaultMaxTurnsFuse,
	},
	{
		Key:   RoleFailureAnalyst,
		Name:  "失败分析师",
		Model: llmapp.ModelRequirements{Tools: true, Reasoning: true},
		WorkItemKinds: []string{
			agent.WorkItemKindAnalyzeFailure,
		},
		Tools: []string{
			tools.ToolContextTaskPack,
			tools.ToolContextTeamRoomRead,
			tools.ToolExecutionResultRead,
			tools.ToolExecutionEvidenceRead,
			tools.ToolAnalysisFailureSubmit,
			tools.ToolCodeDiff,
			tools.ToolCodeFacts,
			tools.ToolCodeAnchors,
			tools.ToolIssueRead,
			tools.ToolIssuePropose,
		},
		Profiles: []string{
			ProfileKeyFailureAnalyst,
			ProfileKeyIssueTriager,
		},
		SystemPrompt: failureAnalystRolePrompt,
		MaxTokens:    roleDefaultMaxTokens,
		RunSeconds:   roleDefaultRunSeconds,
		MaxTurnsFuse: roleDefaultMaxTurnsFuse,
	},
	{
		Key:   RoleReportWriter,
		Name:  "报告撰写员",
		Model: llmapp.ModelRequirements{Tools: true},
		WorkItemKinds: []string{
			agent.WorkItemKindReportVersion,
		},
		Tools: []string{
			tools.ToolContextTaskPack,
			tools.ToolContextTeamRoomRead,
			tools.ToolReportDataRead,
			tools.ToolReportNarrativePropose,
		},
		Profiles: []string{
			ProfileKeyReportWriter,
		},
		SystemPrompt: reportWriterRolePrompt,
		MaxTokens:    roleDefaultMaxTokens,
		RunSeconds:   roleDefaultRunSeconds,
		MaxTurnsFuse: roleDefaultMaxTurnsFuse,
	},
	{
		Key:   RoleKnowledgeCurator,
		Name:  "知识维护员",
		Model: llmapp.ModelRequirements{Tools: true},
		WorkItemKinds: []string{
			agent.WorkItemKindKnowledgePropose,
		},
		Tools: []string{
			// Read-only curation inputs (spec §5.2/§6.4: findings/evidence/
			// recipe, source version, existing knowledge) plus the single
			// propose tool; the curator never executes, edits requirements/cases
			// or changes review state.
			tools.ToolContextVersionPack,
			tools.ToolContextTaskPack,
			tools.ToolContextTeamRoomRead,
			tools.ToolRequirementRead,
			tools.ToolExecutionResultRead,
			tools.ToolExecutionEvidenceRead,
			tools.ToolSUTList,
			tools.ToolSUTPageList,
			tools.ToolCodeFacts,
			tools.ToolCodeRoutes,
			tools.ToolCodeAPIs,
			tools.ToolKnowledgePropose,
			// Structured retrieval tools (spec §10.2) are registered by the
			// retrieval task; whitelisting their names here keeps the role a
			// closed list while allowing that task to land without widening it.
			"knowledge.search",
			"knowledge.read",
		},
		// The knowledge_curator profile seed (p7_profiles.go) executes this
		// role, so the structured retrieval tools (knowledge.search/read,
		// registered by the retrieval task) are model-visible while the runtime
		// RoleToolGuard keeps the surface closed.
		Profiles:     []string{ProfileKeyKnowledgeCurator},
		SystemPrompt: knowledgeCuratorRolePrompt,
		MaxTokens:    roleDefaultMaxTokens,
		RunSeconds:   roleDefaultRunSeconds,
		MaxTurnsFuse: roleDefaultMaxTurnsFuse,
	},
}

// RoleCatalog returns a deep copy of the seven role specs in catalog order.
func RoleCatalog() []RoleSpec {
	specs := make([]RoleSpec, 0, len(roleSpecs))
	for _, spec := range roleSpecs {
		specs = append(specs, cloneRoleSpec(spec))
	}
	return specs
}

// Role returns one role spec by key.
func Role(key RoleKey) (RoleSpec, bool) {
	for _, spec := range roleSpecs {
		if spec.Key == key {
			return cloneRoleSpec(spec), true
		}
	}
	return RoleSpec{}, false
}

func cloneRoleSpec(spec RoleSpec) RoleSpec {
	spec.WorkItemKinds = append([]string(nil), spec.WorkItemKinds...)
	spec.Tools = append([]string(nil), spec.Tools...)
	spec.Profiles = append([]string(nil), spec.Profiles...)
	return spec
}

// RoleForProfile returns the catalog role that executes a stored profile key.
// ProfileAdminService uses it to apply the role whitelist to administrator
// edits, and ValidateRoleProfiles uses the same mapping at startup. Profiles
// outside the seven roles (case_executor, coverage_analyst, version_steward,
// ...) report false: they keep their own frozen contract whitelist.
func RoleForProfile(profileKey string) (RoleSpec, bool) {
	for _, spec := range roleSpecs {
		for _, mapped := range spec.Profiles {
			if mapped == profileKey {
				return cloneRoleSpec(spec), true
			}
		}
	}
	return RoleSpec{}, false
}

// ToolAllowed reports whether a role may see and call a tool. Unknown roles and
// unknown tool names are denied: the whitelist is closed (spec §5.2/§5.8).
func ToolAllowed(role RoleKey, tool string) bool {
	spec, ok := Role(role)
	if !ok {
		return false
	}
	return tools.NewToolSet(string(role), spec.Tools).Allows(tool)
}

// RoleToolSet returns the runtime-enforceable tool whitelist of a role.
func RoleToolSet(role RoleKey) (tools.ToolSet, bool) {
	spec, ok := Role(role)
	if !ok {
		return tools.ToolSet{}, false
	}
	return tools.NewToolSet(string(role), spec.Tools), true
}

// RoleWorkItemKinds returns a copy of the kinds a role may request/execute.
func RoleWorkItemKinds(role RoleKey) []string {
	spec, ok := Role(role)
	if !ok {
		return nil
	}
	return append([]string(nil), spec.WorkItemKinds...)
}

// RoleAllowsWorkItemKind reports whether the role owns the work-item kind.
func RoleAllowsWorkItemKind(role RoleKey, kind string) bool {
	spec, ok := Role(role)
	if !ok {
		return false
	}
	for _, allowed := range spec.WorkItemKinds {
		if allowed == kind {
			return true
		}
	}
	return false
}

// ValidateRoleProfiles fails closed when a seeded agent_profile that executes a
// catalog role allows a tool outside that role's whitelist. It is called by
// EnsureProfiles so a profile edit cannot silently widen a role's permissions
// (review focus 5). Profiles outside the seven roles (for example
// case_executor or version_steward) are not constrained by this catalog.
func ValidateRoleProfiles(profiles []agent.AgentProfile) error {
	roleByProfile := make(map[string]RoleKey)
	for _, spec := range roleSpecs {
		for _, profileKey := range spec.Profiles {
			if existing, duplicate := roleByProfile[profileKey]; duplicate {
				return platform.Invalid("role_profiles", fmt.Sprintf(
					"profile %q is mapped to both %q and %q", profileKey, existing, spec.Key,
				))
			}
			roleByProfile[profileKey] = spec.Key
		}
	}
	for _, profile := range profiles {
		role, ok := roleByProfile[profile.Key]
		if !ok {
			continue
		}
		set, ok := RoleToolSet(role)
		if !ok {
			return platform.Invalid("role", fmt.Sprintf("unknown role %q", role))
		}
		for _, name := range profile.AllowedTools {
			if !set.Allows(name) {
				return platform.Invalid("allowed_tools", fmt.Sprintf(
					"profile %q tool %q is outside the %q role whitelist", profile.Key, name, role,
				))
			}
		}
	}
	return nil
}

// ActionRoleToolDenied is the audit action of a role tool-whitelist denial
// (spec plan review focus 5: 403 + audit). Metadata never carries arguments.
const ActionRoleToolDenied = "agent.role.tool_denied"

// RoleToolGuard enforces one role's whitelist before registry execution and
// audits every denial. It is the runtime seam for the seven roles; the frozen
// 14 agent_profile seeds keep their own allowed_tools (validated as a subset by
// ValidateRoleProfiles). The guard is safe for concurrent use: the ToolSet is
// immutable and the audit recorder owns its synchronization.
type RoleToolGuard struct {
	role  RoleKey
	tools tools.ToolSet
	audit auditapp.Recorder
}

// NewRoleToolGuard builds the guard for one catalog role. An unknown role is a
// configuration error instead of an allow-everything guard.
func NewRoleToolGuard(role RoleKey, audit auditapp.Recorder) (*RoleToolGuard, error) {
	set, ok := RoleToolSet(role)
	if !ok {
		return nil, platform.Invalid("role", fmt.Sprintf("unknown role %q", role))
	}
	return &RoleToolGuard{role: role, tools: set, audit: audit}, nil
}

// Role returns the guarded role key.
func (g *RoleToolGuard) Role() RoleKey {
	if g == nil {
		return ""
	}
	return g.role
}

// Allows reports whether the guarded role may call the tool.
func (g *RoleToolGuard) Allows(name string) bool {
	if g == nil {
		return false
	}
	return g.tools.Allows(name)
}

// Definitions renders only the guarded role's visible tool definitions.
func (g *RoleToolGuard) Definitions(registry *tools.Registry) []llmapp.ToolDefinition {
	if g == nil {
		return nil
	}
	return g.tools.Definitions(registry)
}

// Execute audits and rejects a tool outside the role whitelist with a typed
// forbidden error, then delegates allowed calls to the registry.
func (g *RoleToolGuard) Execute(ctx context.Context, registry *tools.Registry, scope tools.Scope, name string, raw json.RawMessage) (any, error) {
	if g == nil {
		return nil, platform.Invalid("role", "role tool guard is not configured")
	}
	if !g.tools.Allows(name) {
		g.recordDenial(ctx, scope, name)
		return nil, tools.Forbidden(string(g.role), name)
	}
	return registry.Execute(ctx, scope, name, raw)
}

func (g *RoleToolGuard) recordDenial(ctx context.Context, scope tools.Scope, name string) {
	if g.audit == nil {
		return
	}
	actor := int64(0)
	metadata := map[string]any{
		"role": string(g.role),
		"tool": name,
	}
	if scope.AgentRunID > 0 {
		metadata["agent_run_id"] = fmt.Sprintf("%d", scope.AgentRunID)
	}
	if scope.VersionID > 0 {
		metadata["version_id"] = fmt.Sprintf("%d", scope.VersionID)
	}
	_ = g.audit.Record(ctx, auditapp.Event{
		ActorID:    &actor,
		Action:     ActionRoleToolDenied,
		TargetKind: "agent_tool",
		TargetID:   name,
		Metadata:   metadata,
	})
}
