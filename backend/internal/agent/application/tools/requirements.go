package tools

import (
	"context"
	"encoding/json"
	"strings"
)

// Priority values frozen by P1-D6.
var requirementPriorities = []string{"P0", "P1", "P2", "P3"}

var relationKinds = []string{"depends_on", "conflicts_with", "overlaps", "shares_scope"}

const (
	maxRequirementTitle        = 200
	maxRequirementDescription  = 20000
	maxRequirementAcceptance   = 20000
	maxRequirementTargets      = 100
	maxRelationDescription     = 1000
	maxReopenReason            = 2000
	defaultRequirementPriority = "P2"
)

type requirementReadParams struct {
	RequirementID string `json:"requirement_id"`
}

type requirementRead struct{ deps Deps }

func (t *requirementRead) Name() string { return ToolRequirementRead }

func (t *requirementRead) Description() string {
	return "读取当前版本的需求列表；传入 requirement_id 时返回该需求详情（跨版本返回 not_found）。"
}

func (t *requirementRead) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"requirement_id": map[string]any{
			"type":        "string",
			"description": "需求 ID（十进制字符串）；省略时列出当前版本全部需求。",
		},
	})
}

func (t *requirementRead) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var params requirementReadParams
	if err := decodeParams(raw, &params); err != nil {
		return nil, err
	}
	if t.deps.Requirement == nil {
		return nil, missingDep("requirement")
	}
	rawID := strings.TrimSpace(params.RequirementID)
	if rawID == "" {
		items, err := t.deps.Requirement.ListRequirements(ctx, scope.VersionID)
		if err != nil {
			return nil, mapPortError(err)
		}
		requirements := make([]map[string]any, 0, len(items))
		for _, item := range items {
			requirements = append(requirements, requirementSummaryResult(item))
		}
		return map[string]any{
			"version_id":   idString(scope.VersionID),
			"requirements": requirements,
			"count":        len(requirements),
		}, nil
	}

	requirementID, toolErr := parsePositiveID("requirement_id", rawID)
	if toolErr != nil {
		return nil, toolErr
	}
	detail, err := t.deps.Requirement.GetRequirement(ctx, scope.VersionID, requirementID)
	if err != nil {
		return nil, mapPortError(err)
	}
	return map[string]any{"requirement": requirementDetailResult(detail)}, nil
}

type draftFields struct {
	Title              string          `json:"title"`
	Description        string          `json:"description"`
	AcceptanceCriteria string          `json:"acceptance_criteria"`
	Priority           string          `json:"priority"`
	Constraints        json.RawMessage `json:"constraints"`
	TargetVersionSUTs  []string        `json:"target_version_suts"`
	TargetPages        []string        `json:"target_pages"`
	TargetAPIs         []string        `json:"target_apis"`
}

type requirementDraftCreateParams struct {
	draftFields
}

type requirementDraftUpdateParams struct {
	RequirementID string `json:"requirement_id"`
	draftFields
}

func draftProperties() map[string]any {
	return map[string]any{
		"title": map[string]any{
			"type":        "string",
			"minLength":   1,
			"maxLength":   maxRequirementTitle,
			"description": "需求标题。",
		},
		"description": map[string]any{
			"type":        "string",
			"minLength":   1,
			"maxLength":   maxRequirementDescription,
			"description": "需求描述。",
		},
		"acceptance_criteria": map[string]any{
			"type":        "string",
			"minLength":   1,
			"maxLength":   maxRequirementAcceptance,
			"description": "可验证的验收标准。",
		},
		"priority": map[string]any{
			"type":        "string",
			"enum":        requirementPriorities,
			"description": "优先级，默认 P2。",
		},
		"constraints": map[string]any{
			"type":        "object",
			"description": "约束对象（business_rules/preconditions/exceptions/data_constraints/dependencies）。",
		},
		"target_version_suts": map[string]any{
			"type":        "array",
			"maxItems":    maxRequirementTargets,
			"description": "目标 version_sut ID（十进制字符串），必须属于当前版本。",
			"items":       map[string]any{"type": "string"},
		},
		"target_pages": map[string]any{
			"type":        "array",
			"maxItems":    maxRequirementTargets,
			"description": "目标页面标识，必须属于当前版本绑定。",
			"items":       map[string]any{"type": "string"},
		},
		"target_apis": map[string]any{
			"type":        "array",
			"maxItems":    maxRequirementTargets,
			"description": "目标 API 标识，必须属于当前版本绑定。",
			"items":       map[string]any{"type": "string"},
		},
	}
}

type requirementDraftCreate struct{ deps Deps }

func (t *requirementDraftCreate) Name() string { return ToolRequirementDraftCreate }

func (t *requirementDraftCreate) Description() string {
	return "在当前版本创建一条 draft 需求（服务端生成不可变 revision 1）。不能确认、删除或修改已澄清需求。"
}

func (t *requirementDraftCreate) Parameters() json.RawMessage {
	return schemaObject(draftProperties(), "title", "description", "acceptance_criteria")
}

func (t *requirementDraftCreate) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var params requirementDraftCreateParams
	if err := decodeParams(raw, &params); err != nil {
		return nil, err
	}
	draft, toolErr := buildDraft(params.draftFields)
	if toolErr != nil {
		return nil, toolErr
	}
	if t.deps.Requirement == nil {
		return nil, missingDep("requirement")
	}
	created, err := t.deps.Requirement.CreateDraft(ctx, scope.VersionID, draft)
	if err != nil {
		return nil, mapPortError(err)
	}
	return draftResult(scope.VersionID, created), nil
}

type requirementDraftUpdate struct{ deps Deps }

func (t *requirementDraftUpdate) Name() string { return ToolRequirementDraftUpdate }

func (t *requirementDraftUpdate) Description() string {
	return "修改当前版本中的 draft 需求并生成 revision+1。已澄清需求返回 state_conflict，应改用 requirement.reopen_propose。"
}

func (t *requirementDraftUpdate) Parameters() json.RawMessage {
	properties := draftProperties()
	properties["requirement_id"] = map[string]any{
		"type":        "string",
		"description": "要更新的需求 ID（十进制字符串）。",
	}
	return schemaObject(properties, "requirement_id", "title", "description", "acceptance_criteria")
}

func (t *requirementDraftUpdate) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var params requirementDraftUpdateParams
	if err := decodeParams(raw, &params); err != nil {
		return nil, err
	}
	requirementID, toolErr := parsePositiveID("requirement_id", params.RequirementID)
	if toolErr != nil {
		return nil, toolErr
	}
	draft, toolErr := buildDraft(params.draftFields)
	if toolErr != nil {
		return nil, toolErr
	}
	if t.deps.Requirement == nil {
		return nil, missingDep("requirement")
	}
	current, err := t.deps.Requirement.GetRequirement(ctx, scope.VersionID, requirementID)
	if err != nil {
		return nil, mapPortError(err)
	}
	if current.Status != "draft" {
		return nil, StateConflict("requirement is not a draft; use requirement.reopen_propose to propose reopening it")
	}
	updated, err := t.deps.Requirement.UpdateDraft(ctx, scope.VersionID, requirementID, draft)
	if err != nil {
		return nil, mapPortError(err)
	}
	return draftResult(scope.VersionID, updated), nil
}

type requirementRelationProposeParams struct {
	FromRequirementID string `json:"from_requirement_id"`
	ToRequirementID   string `json:"to_requirement_id"`
	Kind              string `json:"kind"`
	Description       string `json:"description"`
}

type requirementRelationPropose struct{ deps Deps }

func (t *requirementRelationPropose) Name() string { return ToolRequirementRelationPropose }

func (t *requirementRelationPropose) Description() string {
	return "提议两条同版本需求之间的关系（候选，不改变需求状态）；校验非自环与 kind 枚举。"
}

func (t *requirementRelationPropose) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"from_requirement_id": map[string]any{
			"type":        "string",
			"description": "源需求 ID。",
		},
		"to_requirement_id": map[string]any{
			"type":        "string",
			"description": "目标需求 ID。",
		},
		"kind": map[string]any{
			"type":        "string",
			"enum":        relationKinds,
			"description": "关系类型。",
		},
		"description": map[string]any{
			"type":        "string",
			"maxLength":   maxRelationDescription,
			"description": "关系说明。",
		},
	}, "from_requirement_id", "to_requirement_id", "kind")
}

func (t *requirementRelationPropose) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var params requirementRelationProposeParams
	if err := decodeParams(raw, &params); err != nil {
		return nil, err
	}
	fromID, toolErr := parsePositiveID("from_requirement_id", params.FromRequirementID)
	if toolErr != nil {
		return nil, toolErr
	}
	toID, toolErr := parsePositiveID("to_requirement_id", params.ToRequirementID)
	if toolErr != nil {
		return nil, toolErr
	}
	if fromID == toID {
		return nil, Invalid("to_requirement_id", "a requirement cannot relate to itself")
	}
	kind := strings.TrimSpace(params.Kind)
	if !contains(relationKinds, kind) {
		return nil, Invalid("kind", "must be one of depends_on, conflicts_with, overlaps, shares_scope")
	}
	description := strings.TrimSpace(params.Description)
	if err := validateText("description", description, 0, maxRelationDescription); err != nil {
		return nil, err
	}
	if t.deps.Requirement == nil {
		return nil, missingDep("requirement")
	}
	if err := t.deps.Requirement.ProposeRelation(ctx, scope.VersionID, fromID, toID, kind, description); err != nil {
		return nil, mapPortError(err)
	}
	return map[string]any{
		"relation": map[string]any{
			"from_requirement_id": idString(fromID),
			"to_requirement_id":   idString(toID),
			"kind":                kind,
			"description":         description,
		},
		"status": "proposed",
	}, nil
}

type requirementReopenProposeParams struct {
	RequirementID string `json:"requirement_id"`
	Reason        string `json:"reason"`
}

type requirementReopenPropose struct{ deps Deps }

func (t *requirementReopenPropose) Name() string { return ToolRequirementReopenPropose }

func (t *requirementReopenPropose) Description() string {
	return "在澄清会话中写入一条回退提议卡（rollback_proposal），不修改需求状态；由用户确认后走回退 API。"
}

func (t *requirementReopenPropose) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"requirement_id": map[string]any{
			"type":        "string",
			"description": "需要重新澄清的已确认需求 ID。",
		},
		"reason": map[string]any{
			"type":        "string",
			"minLength":   1,
			"maxLength":   maxReopenReason,
			"description": "建议回退的原因。",
		},
	}, "requirement_id", "reason")
}

func (t *requirementReopenPropose) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var params requirementReopenProposeParams
	if err := decodeParams(raw, &params); err != nil {
		return nil, err
	}
	requirementID, toolErr := parsePositiveID("requirement_id", params.RequirementID)
	if toolErr != nil {
		return nil, toolErr
	}
	reason := strings.TrimSpace(params.Reason)
	if err := validateText("reason", reason, 1, maxReopenReason); err != nil {
		return nil, err
	}
	if t.deps.Requirement == nil {
		return nil, missingDep("requirements")
	}
	current, err := t.deps.Requirement.GetRequirement(ctx, scope.VersionID, requirementID)
	if err != nil {
		return nil, mapPortError(err)
	}
	if current.Status != "clarified" {
		return nil, StateConflict("only a clarified requirement can be proposed for reopening")
	}
	if t.deps.RollbackProposal == nil {
		return nil, missingDep("rollback proposal")
	}
	proposal, err := t.deps.RollbackProposal(ctx, scope, requirementID, reason)
	if err != nil {
		return nil, mapPortError(err)
	}
	result := map[string]any{
		"status":         "recorded",
		"requirement_id": idString(requirementID),
		"reason":         reason,
	}
	if proposal != nil {
		result["proposal"] = proposal
	}
	return result, nil
}

func buildDraft(fields draftFields) (RequirementDraft, *Error) {
	title := strings.TrimSpace(fields.Title)
	description := strings.TrimSpace(fields.Description)
	acceptance := strings.TrimSpace(fields.AcceptanceCriteria)
	priority := strings.TrimSpace(fields.Priority)
	if priority == "" {
		priority = defaultRequirementPriority
	}

	if err := validateText("title", title, 1, maxRequirementTitle); err != nil {
		return RequirementDraft{}, err
	}
	if err := validateText("description", description, 1, maxRequirementDescription); err != nil {
		return RequirementDraft{}, err
	}
	if err := validateText("acceptance_criteria", acceptance, 1, maxRequirementAcceptance); err != nil {
		return RequirementDraft{}, err
	}
	if !contains(requirementPriorities, priority) {
		return RequirementDraft{}, Invalid("priority", "must be one of P0, P1, P2, P3")
	}
	if err := validateStringList("target_version_suts", fields.TargetVersionSUTs, maxRequirementTargets); err != nil {
		return RequirementDraft{}, err
	}
	if err := validateStringList("target_pages", fields.TargetPages, maxRequirementTargets); err != nil {
		return RequirementDraft{}, err
	}
	if err := validateStringList("target_apis", fields.TargetAPIs, maxRequirementTargets); err != nil {
		return RequirementDraft{}, err
	}

	constraints, toolErr := normalizeConstraints(fields.Constraints)
	if toolErr != nil {
		return RequirementDraft{}, toolErr
	}
	return RequirementDraft{
		Title:              title,
		Description:        description,
		AcceptanceCriteria: acceptance,
		Priority:           priority,
		Constraints:        constraints,
		TargetVersionSUTs:  normalizedList(fields.TargetVersionSUTs),
		TargetPages:        normalizedList(fields.TargetPages),
		TargetAPIs:         normalizedList(fields.TargetAPIs),
	}, nil
}

// normalizeConstraints accepts an absent/null value or a JSON object; arrays
// and scalars are rejected as invalid parameters.
func normalizeConstraints(raw json.RawMessage) (json.RawMessage, *Error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, Invalid("constraints", "must be a JSON object")
	}
	return raw, nil
}

func requirementSummaryResult(item RequirementSummary) map[string]any {
	return map[string]any{
		"id":                 idString(item.ID),
		"current_version_id": idString(item.CurrentVersionID),
		"revision":           item.Revision,
		"title":              item.Title,
		"status":             item.Status,
		"priority":           item.Priority,
	}
}

func requirementDetailResult(detail RequirementDetail) map[string]any {
	result := requirementSummaryResult(detail.RequirementSummary)
	result["description"] = detail.Description
	result["acceptance_criteria"] = detail.AcceptanceCriteria
	targets := map[string]any{
		"version_suts": normalizedList(detail.TargetVersionSUTs),
		"pages":        normalizedList(detail.TargetPages),
		"apis":         normalizedList(detail.TargetAPIs),
	}
	result["targets"] = targets
	if len(detail.Constraints) > 0 && string(detail.Constraints) != "null" {
		result["constraints"] = json.RawMessage(detail.Constraints)
	}
	return result
}

func draftResult(versionID int64, summary RequirementSummary) map[string]any {
	return map[string]any{
		"requirement_id": idString(summary.ID),
		"version_id":     idString(versionID),
		"revision":       summary.Revision,
		"status":         summary.Status,
		"title":          summary.Title,
	}
}

func normalizedList(values []string) []string {
	if values == nil {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, strings.TrimSpace(value))
	}
	return result
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
