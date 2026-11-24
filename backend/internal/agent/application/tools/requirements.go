package tools

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// Priority values frozen by P1-D6.
var requirementPriorities = []string{"P0", "P1", "P2", "P3"}

var relationKinds = []string{"depends_on", "conflicts_with", "overlaps", "shares_scope"}

// Requirement contract bounds. They are DERIVED from the version domain
// authority (single source, constitution art.5): the tool boundary rejects the
// same payloads the service would, with a tool-level error instead of a 400.
// Do not hand-copy a value here — a domain change must flow through (U1g, T22).
const (
	maxRequirementTitle          = version.RequirementTitleMax
	maxRequirementUserStoryPart  = version.RequirementUserStoryPartMax
	maxRequirementRules          = version.RequirementRulesMax
	maxRequirementACPart         = version.AcceptanceCriterionPartMax
	maxRequirementACs            = version.MaxAcceptanceCriteria
	maxRequirementSutDescription = version.RequirementSutDescriptionMax
	maxRequirementScopeOut       = version.RequirementScopeOutMax
	maxRequirementAccountRole    = version.RequirementAccountRoleMax
	maxRelationDescription       = version.RequirementRelationDescriptionMax
	maxReopenReason              = version.RequirementReasonMax
	defaultRequirementPriority   = version.DefaultPriority
)

type requirementReadParams struct {
	RequirementID string `json:"requirement_id"`
}

type requirementRead struct{ deps Deps }

func (t *requirementRead) Name() string { return ToolRequirementRead }

func (t *requirementRead) Description() string {
	return "读取当前版本的需求列表；传入 requirement_id 时返回该需求详情（跨版本返回 not_found）。返回是读取时刻的快照：并行写入（如需求 Curator 在同一轮创建 draft）可能尚未出现，需要确认最新状态时请重读，不要据一次空结果断言草案未创建。"
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
	Title              string                      `json:"title"`
	UserStory          userStoryFields             `json:"user_story"`
	Rules              string                      `json:"rules"`
	AcceptanceCriteria []acceptanceCriterionFields `json:"acceptance_criteria"`
	Priority           string                      `json:"priority"`
	Constraints        json.RawMessage             `json:"constraints"`
	SutDescription     string                      `json:"sut_description"`
	ScopeOut           string                      `json:"scope_out"`
	AccountRolesHint   []string                    `json:"account_roles_hint"`
}

// userStoryFields is the §5.1 three-part story as a model submits it.
type userStoryFields struct {
	AsA    string `json:"as_a"`
	IWant  string `json:"i_want"`
	SoThat string `json:"so_that"`
}

// acceptanceCriterionFields is one Gherkin criterion as a model submits it.
// There is no id field: ids are server-assigned and inherited by content, so a
// model cannot choose one.
type acceptanceCriterionFields struct {
	Given string `json:"given"`
	When  string `json:"when"`
	Then  string `json:"then"`
}

type requirementDraftCreateParams struct {
	draftFields
}

type requirementDraftUpdateParams struct {
	RequirementID string `json:"requirement_id"`
	draftFields
}

// draftRequiredFields are the §5.1 mandatory draft fields. requirement_id is
// added by the update tool; both tools must agree on this list, so it is stated
// once.
var draftRequiredFields = []string{"title", "user_story", "acceptance_criteria", "sut_description"}

func draftProperties() map[string]any {
	return map[string]any{
		"title": map[string]any{
			"type":        "string",
			"minLength":   1,
			"maxLength":   maxRequirementTitle,
			"description": "需求标题。",
		},
		"user_story": map[string]any{
			"type":        "object",
			"description": "用户故事三段式，三项均必填且各 1..500 字。",
			"properties": map[string]any{
				"as_a":    map[string]any{"type": "string", "minLength": 1, "maxLength": maxRequirementUserStoryPart, "description": "作为……（角色）"},
				"i_want":  map[string]any{"type": "string", "minLength": 1, "maxLength": maxRequirementUserStoryPart, "description": "我希望……（能力）"},
				"so_that": map[string]any{"type": "string", "minLength": 1, "maxLength": maxRequirementUserStoryPart, "description": "以便……（价值）"},
			},
			"required": []string{"as_a", "i_want", "so_that"},
		},
		"rules": map[string]any{
			"type":        "string",
			"maxLength":   maxRequirementRules,
			"description": "可选：业务规则、例外、数据约束等不属于用户故事的自由文本。",
		},
		"acceptance_criteria": map[string]any{
			"type":        "array",
			"minItems":    1,
			"maxItems":    maxRequirementACs,
			"description": "Gherkin 验收条件，至少 1 条；id 由服务端分配（AC-1、AC-2……）并在修订间按内容继承，不要自己填。",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"given": map[string]any{"type": "string", "minLength": 1, "maxLength": maxRequirementACPart, "description": "前置条件"},
					"when":  map[string]any{"type": "string", "minLength": 1, "maxLength": maxRequirementACPart, "description": "触发动作"},
					"then":  map[string]any{"type": "string", "minLength": 1, "maxLength": maxRequirementACPart, "description": "可观察结果"},
				},
				"required": []string{"given", "when", "then"},
			},
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
		"sut_description": map[string]any{
			"type":        "string",
			"minLength":   1,
			"maxLength":   maxRequirementSutDescription,
			"description": "被测系统的自然语言描述（1..2000 字）。不要填 SUT/页面/接口的 ID——需求不再绑定资源 ID，被测系统由执行团队按此描述自行解析。",
		},
		"scope_out": map[string]any{
			"type":        "string",
			"maxLength":   maxRequirementScopeOut,
			"description": "可选：明确不在本需求范围内的内容。",
		},
		"account_roles_hint": map[string]any{
			"type":        "array",
			"description": "可选：涉及的账号角色提示，每项 ≤64 字；空白项会被丢弃、重复项会被去重（与需求服务同一归一化器）。",
			"items":       map[string]any{"type": "string", "maxLength": maxRequirementAccountRole},
		},
	}
}

type requirementDraftCreate struct{ deps Deps }

func (t *requirementDraftCreate) Name() string { return ToolRequirementDraftCreate }

func (t *requirementDraftCreate) Description() string {
	return "在当前版本创建一条 draft 需求（服务端生成不可变 revision 1）。同一版本同一来源编号（title 开头的 FR/NFR 等编号）已存在时返回已有 draft（仅在新提交内容更丰富时补全），不会重复创建。不能确认、删除或修改已澄清需求。"
}

func (t *requirementDraftCreate) Parameters() json.RawMessage {
	return schemaObject(draftProperties(), draftRequiredFields...)
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
	return schemaObject(properties, append([]string{"requirement_id"}, draftRequiredFields...)...)
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
	priority := strings.TrimSpace(fields.Priority)
	if priority == "" {
		priority = defaultRequirementPriority
	}

	if err := validateText("title", title, 1, maxRequirementTitle); err != nil {
		return RequirementDraft{}, err
	}
	story := RequirementUserStory{
		AsA:    strings.TrimSpace(fields.UserStory.AsA),
		IWant:  strings.TrimSpace(fields.UserStory.IWant),
		SoThat: strings.TrimSpace(fields.UserStory.SoThat),
	}
	for _, part := range []struct{ name, value string }{
		{"user_story.as_a", story.AsA},
		{"user_story.i_want", story.IWant},
		{"user_story.so_that", story.SoThat},
	} {
		if err := validateText(part.name, part.value, 1, maxRequirementUserStoryPart); err != nil {
			return RequirementDraft{}, err
		}
	}
	rules := strings.TrimSpace(fields.Rules)
	if err := validateText("rules", rules, 0, maxRequirementRules); err != nil {
		return RequirementDraft{}, err
	}
	criteria, criterionErr := buildAcceptanceCriteria(fields.AcceptanceCriteria)
	if criterionErr != nil {
		return RequirementDraft{}, criterionErr
	}
	if !contains(requirementPriorities, priority) {
		return RequirementDraft{}, Invalid("priority", "must be one of P0, P1, P2, P3")
	}
	sutDescription := strings.TrimSpace(fields.SutDescription)
	if err := validateText("sut_description", sutDescription, 1, maxRequirementSutDescription); err != nil {
		return RequirementDraft{}, err
	}
	scopeOut := strings.TrimSpace(fields.ScopeOut)
	if err := validateText("scope_out", scopeOut, 0, maxRequirementScopeOut); err != nil {
		return RequirementDraft{}, err
	}
	// Role hints use the SAME normalizer as the version domain write side
	// (U1f, T22): trim, drop empties, de-duplicate, preserve first-occurrence
	// order. The tool used to reject a blank entry with 400 while the REST side
	// silently dropped it — one input, two results. The structural length bound
	// below still matches ValidateDraft (each role 1..RequirementAccountRoleMax).
	roles := version.NormalizeAccountRolesHint(fields.AccountRolesHint)
	if err := validateStringList("account_roles_hint", roles, 0); err != nil {
		return RequirementDraft{}, err
	}
	for index, role := range roles {
		field := "account_roles_hint[" + strconv.Itoa(index) + "]"
		if err := validateText(field, role, 1, maxRequirementAccountRole); err != nil {
			return RequirementDraft{}, err
		}
	}

	constraints, toolErr := normalizeConstraints(fields.Constraints)
	if toolErr != nil {
		return RequirementDraft{}, toolErr
	}
	return RequirementDraft{
		Title:              title,
		UserStory:          story,
		Rules:              rules,
		AcceptanceCriteria: criteria,
		Priority:           priority,
		Constraints:        constraints,
		SutDescription:     sutDescription,
		ScopeOut:           scopeOut,
		AccountRolesHint:   roles,
	}, nil
}

// buildAcceptanceCriteria validates the Gherkin criteria a model submitted. The
// ids are not part of the input and are not produced here: the version service
// assigns them (version.AssignACIDs) so that they stay stable across revisions.
func buildAcceptanceCriteria(raw []acceptanceCriterionFields) ([]RequirementAcceptanceCriterionInput, *Error) {
	if len(raw) == 0 {
		return nil, Invalid("acceptance_criteria", "must contain at least one item")
	}
	if len(raw) > maxRequirementACs {
		return nil, Invalid("acceptance_criteria",
			"must contain at most "+strconv.Itoa(maxRequirementACs)+" items")
	}
	criteria := make([]RequirementAcceptanceCriterionInput, 0, len(raw))
	for index, item := range raw {
		field := "acceptance_criteria[" + strconv.Itoa(index) + "]"
		criterion := RequirementAcceptanceCriterionInput{
			Given: strings.TrimSpace(item.Given),
			When:  strings.TrimSpace(item.When),
			Then:  strings.TrimSpace(item.Then),
		}
		for _, part := range []struct{ name, value string }{
			{".given", criterion.Given},
			{".when", criterion.When},
			{".then", criterion.Then},
		} {
			if err := validateText(field+part.name, part.value, 1, maxRequirementACPart); err != nil {
				return nil, err
			}
		}
		criteria = append(criteria, criterion)
	}
	return criteria, nil
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
	result["user_story"] = map[string]any{
		"as_a":    detail.UserStory.AsA,
		"i_want":  detail.UserStory.IWant,
		"so_that": detail.UserStory.SoThat,
	}
	if rules := strings.TrimSpace(detail.Rules); rules != "" {
		result["rules"] = rules
	}
	// Canonical id-sequence order (version.SortAcceptanceCriteria, design §3.6
	// "按 id 排序"): the structured array and ac_ids are emitted in the SAME
	// order the version/design contexts normalize to, not storage order (U1b, T22).
	sorted := version.SortAcceptanceCriteria(domainAcceptanceCriteria(detail.AcceptanceCriteria))
	criteria := make([]map[string]any, 0, len(sorted))
	for _, criterion := range sorted {
		criteria = append(criteria, map[string]any{
			"id":    criterion.ID,
			"given": criterion.Given,
			"when":  criterion.When,
			"then":  criterion.Then,
		})
	}
	result["acceptance_criteria"] = criteria
	// ac_ids is the flat set a design case's covered_acs must be drawn from
	// (design v1.3.1 §5.2); it is repeated here so the design author can read it
	// without walking the structured array. Derived from the domain authority.
	result["ac_ids"] = version.ACIDs(sorted)
	result["sut_description"] = detail.SutDescription
	if scopeOut := strings.TrimSpace(detail.ScopeOut); scopeOut != "" {
		result["scope_out"] = scopeOut
	}
	if roles := version.NormalizeAccountRolesHint(detail.AccountRolesHint); len(roles) > 0 {
		result["account_roles_hint"] = roles
	}
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

// domainAcceptanceCriteria projects the port DTOs onto the version domain type
// so the canonical ordering/ids derive from the domain authority (single source).
func domainAcceptanceCriteria(criteria []RequirementAcceptanceCriterion) []version.AcceptanceCriterion {
	out := make([]version.AcceptanceCriterion, 0, len(criteria))
	for _, criterion := range criteria {
		out = append(out, version.AcceptanceCriterion{
			ID: criterion.ID, Given: criterion.Given, When: criterion.When, Then: criterion.Then,
		})
	}
	return out
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
