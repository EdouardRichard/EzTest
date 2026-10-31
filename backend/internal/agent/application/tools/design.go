package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// P2 design tool names (architecture §7.3 / contracts/agent-profiles-tools.md
// P2 increment). case.confirm and the granular HLD write tools are deliberately
// never registered: confirmation belongs to the user/server and the graph write
// path is unified through case.graph_propose.
const (
	ToolDesignGraphRead      = "design.graph_read"
	ToolCaseGraphPropose     = "case.graph_propose"
	ToolDesignReviewSubmit   = "design.review_submit"
	ToolDesignDataPlanSubmit = "design.data_plan_submit"
)

// Design proposal bounds (P2-D-18). The design application re-validates the
// same limits; the tool rejects early so the model gets a precise field.
const (
	maxProposalScenarios = 3
	maxProposalCases     = 10
	maxCaseSteps         = 50
	maxCaseAssertions    = 50
	maxCaseSources       = 20
)

var graphHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// GraphProposal is the parsed, tool-validated case.graph_propose payload. IDs
// are parsed to int64 here; the design adapter maps this into its own input.
type GraphProposal struct {
	RequirementID        int64
	RequirementVersionID int64
	BaseGraphHash        string
	Scenarios            []ScenarioProposal
	Cases                []CaseProposal
}

// ScenarioProposal is one scenario node in a graph proposal.
type ScenarioProposal struct {
	Ref           string
	ID            int64
	Name          string
	Description   string
	VersionSUTIDs []int64
	PageIDs       []int64
	APIIDs        []int64
	SortOrder     int
}

// CaseProposal is one case node (with its immutable version content) in a
// graph proposal.
type CaseProposal struct {
	Ref                 string
	ID                  int64
	ScenarioRef         string
	Title               string
	Description         string
	Preconditions       string
	TestData            json.RawMessage
	Priority            string
	DefaultVersionSUTID int64
	VersionSUTIDs       []int64
	PageIDs             []int64
	APIIDs              []int64
	Steps               []StepProposal
	Assertions          []AssertionProposal
	Sources             []SourceProposal
}

// StepProposal is one case step. Zero target IDs mean "inherit/null".
type StepProposal struct {
	Seq                int
	Action             string
	Expected           string
	TargetVersionSUTID int64
	TargetPageID       int64
	TargetAPIID        int64
	AccountRole        string
	Data               json.RawMessage
}

// AssertionProposal is one assertion attached to a step sequence.
type AssertionProposal struct {
	StepSeq            int
	Kind               string
	Target             string
	Expected           string
	TargetVersionSUTID int64
	TargetAPIID        int64
	DatabaseID         int64
}

// SourceProposal is one provenance entry for a case version.
type SourceProposal struct {
	Kind  string
	RefID string
	Path  string
	Line  *int
	SHA   string
}

// ReviewFinding is one Critic finding.
type ReviewFinding struct {
	Severity      string
	Message       string
	RequirementID int64
	CaseID        int64
}

// ReviewInput is the parsed design.review_submit payload.
type ReviewInput struct {
	Summary  string
	Findings []ReviewFinding
}

// DataPlanInput is the parsed design.data_plan_submit payload. Nil fields mean
// "not provided" so the service keeps the current draft value.
type DataPlanInput struct {
	RequirementID int64
	Preconditions *string
	TestData      json.RawMessage
}

// DesignPort is the design bounded context's application surface consumed by
// the P2 tools. The adapter (agent/infrastructure/designadapter) enforces the
// version scope and returns *tools.Error or *platform.Error for business
// failures.
type DesignPort interface {
	ReadGraph(ctx context.Context, versionID, requirementID, caseID int64) (any, error)
	ProposeGraph(ctx context.Context, versionID int64, in GraphProposal, actorID, runID int64) (any, error)
	SubmitReview(ctx context.Context, versionID, requirementID int64, runID int64, in ReviewInput) (any, error)
	SubmitDataPlan(ctx context.Context, versionID, caseVersionID int64, runID int64, in DataPlanInput) (any, error)
}

// --- payloads --------------------------------------------------------------

type graphReadPayload struct {
	RequirementID *string `json:"requirement_id"`
	CaseID        *string `json:"case_id"`
}

type graphProposalPayload struct {
	RequirementID        string            `json:"requirement_id"`
	RequirementVersionID string            `json:"requirement_version_id"`
	BaseGraphHash        *string           `json:"base_graph_hash"`
	Scenarios            []scenarioPayload `json:"scenarios"`
	Cases                []casePayload     `json:"cases"`
}

type scenarioPayload struct {
	Ref           string   `json:"ref"`
	ID            *string  `json:"id"`
	Name          string   `json:"name"`
	Description   *string  `json:"description"`
	VersionSUTIDs []string `json:"version_sut_ids"`
	PageIDs       []string `json:"page_ids"`
	APIIDs        []string `json:"api_ids"`
	SortOrder     *int     `json:"sort_order"`
}

type casePayload struct {
	Ref                 string             `json:"ref"`
	ID                  *string            `json:"id"`
	ScenarioRef         string             `json:"scenario_ref"`
	Title               string             `json:"title"`
	Description         *string            `json:"description"`
	Preconditions       *string            `json:"preconditions"`
	TestData            json.RawMessage    `json:"test_data"`
	Priority            *string            `json:"priority"`
	DefaultVersionSUTID *string            `json:"default_version_sut_id"`
	VersionSUTIDs       []string           `json:"version_sut_ids"`
	PageIDs             []string           `json:"page_ids"`
	APIIDs              []string           `json:"api_ids"`
	Steps               []stepPayload      `json:"steps"`
	Assertions          []assertionPayload `json:"assertions"`
	Sources             []sourcePayload    `json:"sources"`
}

type stepPayload struct {
	Seq                int             `json:"seq"`
	Action             string          `json:"action"`
	Expected           *string         `json:"expected"`
	TargetVersionSUTID *string         `json:"target_version_sut_id"`
	TargetPageID       *string         `json:"target_page_id"`
	TargetAPIID        *string         `json:"target_api_id"`
	AccountRole        *string         `json:"account_role"`
	Data               json.RawMessage `json:"data"`
}

type assertionPayload struct {
	StepSeq            int     `json:"step_seq"`
	Kind               string  `json:"kind"`
	Target             string  `json:"target"`
	Expected           *string `json:"expected"`
	TargetVersionSUTID *string `json:"target_version_sut_id"`
	TargetAPIID        *string `json:"target_api_id"`
	DatabaseID         *string `json:"database_id"`
}

type sourcePayload struct {
	Kind  string  `json:"kind"`
	RefID string  `json:"ref_id"`
	Path  *string `json:"path"`
	Line  *int    `json:"line"`
	SHA   *string `json:"sha"`
}

type reviewPayload struct {
	Summary  *string          `json:"summary"`
	Findings []findingPayload `json:"findings"`
}

type findingPayload struct {
	Severity      string  `json:"severity"`
	Message       string  `json:"message"`
	RequirementID *string `json:"requirement_id"`
	CaseID        *string `json:"case_id"`
}

type dataPlanPayload struct {
	CaseVersionID *string         `json:"case_version_id"`
	Preconditions *string         `json:"preconditions"`
	TestData      json.RawMessage `json:"test_data"`
}

// --- tools -----------------------------------------------------------------

type designGraphRead struct{ deps Deps }

func (t *designGraphRead) Name() string { return ToolDesignGraphRead }

func (t *designGraphRead) Description() string {
	return "读取当前版本的设计脑图（需求/场景/用例摘要）；传 case_id 时返回该用例当前版本的完整步骤/断言/来源。"
}

func (t *designGraphRead) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"requirement_id": idStringSchema(),
		"case_id":        idStringSchema(),
	})
}

func (t *designGraphRead) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t.deps.Design == nil {
		return nil, missingDep("design")
	}
	var payload graphReadPayload
	if err := decodeParams(raw, &payload); err != nil {
		return nil, err
	}
	requirementID := scope.RequirementID
	if payload.RequirementID != nil && strings.TrimSpace(*payload.RequirementID) != "" {
		parsed, err := parsePositiveID("requirement_id", *payload.RequirementID)
		if err != nil {
			return nil, err
		}
		if scope.RequirementID > 0 && parsed != scope.RequirementID {
			return nil, Invalid("requirement_id", "must match the injected requirement scope")
		}
		requirementID = parsed
	}
	caseID := int64(0)
	if payload.CaseID != nil && strings.TrimSpace(*payload.CaseID) != "" {
		parsed, err := parsePositiveID("case_id", *payload.CaseID)
		if err != nil {
			return nil, err
		}
		caseID = parsed
	}
	result, err := t.deps.Design.ReadGraph(ctx, scope.VersionID, requirementID, caseID)
	if err != nil {
		return nil, mapPortError(err)
	}
	if caseID > 0 && requirementID > 0 {
		if detail, ok := result.(map[string]any); ok {
			if got, _ := detail["requirement_id"].(string); got != "" && got != fmt.Sprintf("%d", requirementID) {
				return nil, Invalid("case_id", "case does not belong to the injected requirement scope")
			}
		}
	}
	return result, nil
}

type caseGraphPropose struct{ deps Deps }

func (t *caseGraphPropose) Name() string { return ToolCaseGraphPropose }

func (t *caseGraphPropose) Description() string {
	return "提交场景/用例草案（case.graph_propose）。只能创建或更新 draft；不能确认、删除或用例状态变更。scenarios[].id/cases[].id 更新已有节点时传入，新建时省略。"
}

func (t *caseGraphPropose) Parameters() json.RawMessage {
	return graphProposeSchema()
}

func (t *caseGraphPropose) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t.deps.Design == nil {
		return nil, missingDep("design")
	}
	var payload graphProposalPayload
	if err := decodeParams(raw, &payload); err != nil {
		return nil, err
	}
	proposal, err := parseGraphProposal(payload, scope)
	if err != nil {
		return nil, err
	}
	result, portErr := t.deps.Design.ProposeGraph(ctx, scope.VersionID, proposal, 0, scope.AgentRunID)
	if portErr != nil {
		return nil, mapPortError(portErr)
	}
	return result, nil
}

type designReviewSubmit struct{ deps Deps }

func (t *designReviewSubmit) Name() string { return ToolDesignReviewSubmit }

func (t *designReviewSubmit) Description() string {
	return "提交设计评审发现（design.review_submit）：写入团队频道的 finding 帖子，不构成人工关卡、不改变用例状态。"
}

func (t *designReviewSubmit) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"summary": map[string]any{"type": "string", "maxLength": 4000},
		"findings": map[string]any{
			"type": "array",
			"items": schemaObject(map[string]any{
				"severity":       map[string]any{"type": "string", "enum": []any{"blocker", "important", "minor"}},
				"message":        map[string]any{"type": "string", "minLength": 1, "maxLength": 2000},
				"requirement_id": idStringSchema(),
				"case_id":        idStringSchema(),
			}, "severity", "message"),
		},
	}, "findings")
}

func (t *designReviewSubmit) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t.deps.Design == nil {
		return nil, missingDep("design")
	}
	var payload reviewPayload
	if err := decodeParams(raw, &payload); err != nil {
		return nil, err
	}
	if scope.RequirementID <= 0 {
		return nil, Invalid("requirement_id", "design.review_submit requires a requirement-scoped run")
	}
	summary := ""
	if payload.Summary != nil {
		summary = strings.TrimSpace(*payload.Summary)
	}
	if err := validateText("summary", summary, 0, 4000); err != nil {
		return nil, err
	}
	findings := make([]ReviewFinding, 0, len(payload.Findings))
	for index, finding := range payload.Findings {
		field := func(name string) string { return fmt.Sprintf("findings[%d].%s", index, name) }
		severity := strings.TrimSpace(finding.Severity)
		switch severity {
		case "blocker", "important", "minor":
		default:
			return nil, Invalid(field("severity"), "must be one of blocker, important, minor")
		}
		message := strings.TrimSpace(finding.Message)
		if err := validateText(field("message"), message, 1, 2000); err != nil {
			return nil, err
		}
		requirementID, err := parseOptionalID(field("requirement_id"), finding.RequirementID)
		if err != nil {
			return nil, err
		}
		caseID, err := parseOptionalID(field("case_id"), finding.CaseID)
		if err != nil {
			return nil, err
		}
		findings = append(findings, ReviewFinding{
			Severity: severity, Message: message, RequirementID: requirementID, CaseID: caseID,
		})
	}
	result, portErr := t.deps.Design.SubmitReview(ctx, scope.VersionID, scope.RequirementID, scope.AgentRunID, ReviewInput{
		Summary: summary, Findings: findings,
	})
	if portErr != nil {
		return nil, mapPortError(portErr)
	}
	return result, nil
}

type designDataPlanSubmit struct{ deps Deps }

func (t *designDataPlanSubmit) Name() string { return ToolDesignDataPlanSubmit }

func (t *designDataPlanSubmit) Description() string {
	return "为当前 draft 用例提交测试数据/前置条件（design.data_plan_submit），生成新的 draft revision；已确认或 stale 版本不可修改。"
}

func (t *designDataPlanSubmit) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"case_version_id": idStringSchema(),
		"preconditions":   map[string]any{"type": "string", "maxLength": 20000},
		"test_data":       map[string]any{"type": "object", "additionalProperties": true},
	}, "case_version_id")
}

func (t *designDataPlanSubmit) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t.deps.Design == nil {
		return nil, missingDep("design")
	}
	var payload dataPlanPayload
	if err := decodeParams(raw, &payload); err != nil {
		return nil, err
	}
	if payload.CaseVersionID == nil || strings.TrimSpace(*payload.CaseVersionID) == "" {
		return nil, Invalid("case_version_id", "must be a positive decimal ID")
	}
	caseVersionID, err := parsePositiveID("case_version_id", *payload.CaseVersionID)
	if err != nil {
		return nil, err
	}
	if scope.CaseVersionID > 0 && caseVersionID != scope.CaseVersionID {
		return nil, Invalid("case_version_id", "must match the injected case version scope")
	}
	var preconditions *string
	if payload.Preconditions != nil {
		trimmed := *payload.Preconditions
		if err := validateText("preconditions", trimmed, 0, 20000); err != nil {
			return nil, err
		}
		preconditions = &trimmed
	}
	testData := json.RawMessage(nil)
	if len(bytes.TrimSpace(payload.TestData)) > 0 && string(bytes.TrimSpace(payload.TestData)) != "null" {
		trimmed := bytes.TrimSpace(payload.TestData)
		if !json.Valid(trimmed) || !bytes.HasPrefix(trimmed, []byte("{")) {
			return nil, Invalid("test_data", "must be a JSON object")
		}
		testData = append(json.RawMessage(nil), trimmed...)
	}
	result, portErr := t.deps.Design.SubmitDataPlan(ctx, scope.VersionID, caseVersionID, scope.AgentRunID, DataPlanInput{
		RequirementID: scope.RequirementID, Preconditions: preconditions, TestData: testData,
	})
	if portErr != nil {
		return nil, mapPortError(portErr)
	}
	return result, nil
}

// --- validation helpers ----------------------------------------------------

func parseGraphProposal(payload graphProposalPayload, scope Scope) (GraphProposal, *Error) {
	proposal := GraphProposal{}
	requirementID, err := parseRequiredID("requirement_id", payload.RequirementID)
	if err != nil {
		return GraphProposal{}, err
	}
	requirementVersionID, err := parseRequiredID("requirement_version_id", payload.RequirementVersionID)
	if err != nil {
		return GraphProposal{}, err
	}
	if scope.RequirementID > 0 && requirementID != scope.RequirementID {
		return GraphProposal{}, Invalid("requirement_id", "must match the injected requirement scope")
	}
	if scope.RequirementVersionID > 0 && requirementVersionID != scope.RequirementVersionID {
		return GraphProposal{}, Invalid("requirement_version_id", "must match the injected requirement version scope")
	}
	proposal.RequirementID = requirementID
	proposal.RequirementVersionID = requirementVersionID

	if payload.BaseGraphHash != nil && strings.TrimSpace(*payload.BaseGraphHash) != "" {
		hash := strings.TrimSpace(*payload.BaseGraphHash)
		if !graphHashPattern.MatchString(hash) {
			return GraphProposal{}, Invalid("base_graph_hash", "must be 64 lowercase hex characters")
		}
		proposal.BaseGraphHash = hash
	}

	if len(payload.Scenarios) == 0 {
		return GraphProposal{}, Invalid("scenarios", "must contain at least one scenario")
	}
	if len(payload.Scenarios) > maxProposalScenarios {
		return GraphProposal{}, Invalid("scenarios", fmt.Sprintf("must contain at most %d scenarios per proposal", maxProposalScenarios))
	}
	scenarioRefs := make(map[string]struct{}, len(payload.Scenarios))
	for index, scenario := range payload.Scenarios {
		field := func(name string) string { return fmt.Sprintf("scenarios[%d].%s", index, name) }
		ref, err := validateRef(field("ref"), scenario.Ref)
		if err != nil {
			return GraphProposal{}, err
		}
		if _, duplicate := scenarioRefs[ref]; duplicate {
			return GraphProposal{}, Invalid(field("ref"), "must be unique within the proposal")
		}
		scenarioRefs[ref] = struct{}{}
		if err := validateText(field("name"), scenario.Name, 1, 200); err != nil {
			return GraphProposal{}, err
		}
		description := stringValueOr(scenario.Description, "")
		if err := validateText(field("description"), description, 0, 20000); err != nil {
			return GraphProposal{}, err
		}
		id, err := parseOptionalID(field("id"), scenario.ID)
		if err != nil {
			return GraphProposal{}, err
		}
		versionSUTs, err := parseIDList(field("version_sut_ids"), scenario.VersionSUTIDs)
		if err != nil {
			return GraphProposal{}, err
		}
		pageIDs, err := parseIDList(field("page_ids"), scenario.PageIDs)
		if err != nil {
			return GraphProposal{}, err
		}
		apiIDs, err := parseIDList(field("api_ids"), scenario.APIIDs)
		if err != nil {
			return GraphProposal{}, err
		}
		sortOrder := 0
		if scenario.SortOrder != nil {
			if *scenario.SortOrder < 0 {
				return GraphProposal{}, Invalid(field("sort_order"), "must be zero or positive")
			}
			sortOrder = *scenario.SortOrder
		}
		proposal.Scenarios = append(proposal.Scenarios, ScenarioProposal{
			Ref: ref, ID: id, Name: scenario.Name, Description: description,
			VersionSUTIDs: versionSUTs, PageIDs: pageIDs, APIIDs: apiIDs, SortOrder: sortOrder,
		})
	}

	if len(payload.Cases) == 0 {
		return GraphProposal{}, Invalid("cases", "must contain at least one case")
	}
	if len(payload.Cases) > maxProposalCases {
		return GraphProposal{}, Invalid("cases", fmt.Sprintf("must contain at most %d cases per proposal", maxProposalCases))
	}
	caseRefs := make(map[string]struct{}, len(payload.Cases))
	for index, item := range payload.Cases {
		field := func(name string) string { return fmt.Sprintf("cases[%d].%s", index, name) }
		ref, err := validateRef(field("ref"), item.Ref)
		if err != nil {
			return GraphProposal{}, err
		}
		if _, duplicate := caseRefs[ref]; duplicate {
			return GraphProposal{}, Invalid(field("ref"), "must be unique within the proposal")
		}
		caseRefs[ref] = struct{}{}
		if _, ok := scenarioRefs[item.ScenarioRef]; !ok {
			return GraphProposal{}, Invalid(field("scenario_ref"), "must reference a scenario ref in the same proposal")
		}
		if err := validateText(field("title"), item.Title, 1, 200); err != nil {
			return GraphProposal{}, err
		}
		description := stringValueOr(item.Description, "")
		if err := validateText(field("description"), description, 0, 20000); err != nil {
			return GraphProposal{}, err
		}
		preconditions := stringValueOr(item.Preconditions, "")
		if err := validateText(field("preconditions"), preconditions, 0, 20000); err != nil {
			return GraphProposal{}, err
		}
		testData := json.RawMessage(nil)
		if len(bytes.TrimSpace(item.TestData)) > 0 && string(bytes.TrimSpace(item.TestData)) != "null" {
			trimmed := bytes.TrimSpace(item.TestData)
			if !json.Valid(trimmed) || !bytes.HasPrefix(trimmed, []byte("{")) {
				return GraphProposal{}, Invalid(field("test_data"), "must be a JSON object")
			}
			testData = append(json.RawMessage(nil), trimmed...)
		}
		priority := "P2"
		if item.Priority != nil && strings.TrimSpace(*item.Priority) != "" {
			priority = strings.TrimSpace(*item.Priority)
		}
		switch priority {
		case "P0", "P1", "P2", "P3":
		default:
			return GraphProposal{}, Invalid(field("priority"), "must be one of P0, P1, P2, P3")
		}
		defaultSUT, err := parseRequiredID(field("default_version_sut_id"), stringValueOr(item.DefaultVersionSUTID, ""))
		if err != nil {
			return GraphProposal{}, err
		}
		versionSUTs, err := parseIDList(field("version_sut_ids"), item.VersionSUTIDs)
		if err != nil {
			return GraphProposal{}, err
		}
		if len(versionSUTs) == 0 {
			return GraphProposal{}, Invalid(field("version_sut_ids"), "must contain at least one version SUT")
		}
		if !containsID(versionSUTs, defaultSUT) {
			return GraphProposal{}, Invalid(field("default_version_sut_id"), "must be part of cases[].version_sut_ids")
		}
		pageIDs, err := parseIDList(field("page_ids"), item.PageIDs)
		if err != nil {
			return GraphProposal{}, err
		}
		apiIDs, err := parseIDList(field("api_ids"), item.APIIDs)
		if err != nil {
			return GraphProposal{}, err
		}
		id, err := parseOptionalID(field("id"), item.ID)
		if err != nil {
			return GraphProposal{}, err
		}

		if len(item.Steps) == 0 {
			return GraphProposal{}, Invalid(field("steps"), "must contain at least one step")
		}
		if len(item.Steps) > maxCaseSteps {
			return GraphProposal{}, Invalid(field("steps"), fmt.Sprintf("must contain at most %d steps", maxCaseSteps))
		}
		steps := make([]StepProposal, 0, len(item.Steps))
		for stepIndex, step := range item.Steps {
			stepField := func(name string) string { return fmt.Sprintf("cases[%d].steps[%d].%s", index, stepIndex, name) }
			if step.Seq != stepIndex+1 {
				return GraphProposal{}, Invalid(stepField("seq"), "must be contiguous starting at 1")
			}
			if err := validateText(stepField("action"), step.Action, 1, 2000); err != nil {
				return GraphProposal{}, err
			}
			expected := stringValueOr(step.Expected, "")
			if err := validateText(stepField("expected"), expected, 0, 20000); err != nil {
				return GraphProposal{}, err
			}
			targetSUT, err := parseOptionalID(stepField("target_version_sut_id"), step.TargetVersionSUTID)
			if err != nil {
				return GraphProposal{}, err
			}
			if targetSUT > 0 && !containsID(versionSUTs, targetSUT) {
				return GraphProposal{}, Invalid(stepField("target_version_sut_id"), "must be part of cases[].version_sut_ids")
			}
			targetPage, err := parseOptionalID(stepField("target_page_id"), step.TargetPageID)
			if err != nil {
				return GraphProposal{}, err
			}
			targetAPI, err := parseOptionalID(stepField("target_api_id"), step.TargetAPIID)
			if err != nil {
				return GraphProposal{}, err
			}
			accountRole := stringValueOr(step.AccountRole, "")
			if err := validateText(stepField("account_role"), accountRole, 0, 200); err != nil {
				return GraphProposal{}, err
			}
			stepData := json.RawMessage(nil)
			if len(bytes.TrimSpace(step.Data)) > 0 && string(bytes.TrimSpace(step.Data)) != "null" {
				trimmed := bytes.TrimSpace(step.Data)
				if !json.Valid(trimmed) || !bytes.HasPrefix(trimmed, []byte("{")) {
					return GraphProposal{}, Invalid(stepField("data"), "must be a JSON object")
				}
				stepData = append(json.RawMessage(nil), trimmed...)
			}
			steps = append(steps, StepProposal{
				Seq: step.Seq, Action: step.Action, Expected: expected,
				TargetVersionSUTID: targetSUT, TargetPageID: targetPage, TargetAPIID: targetAPI,
				AccountRole: accountRole, Data: stepData,
			})
		}

		if len(item.Assertions) > maxCaseAssertions {
			return GraphProposal{}, Invalid(field("assertions"), fmt.Sprintf("must contain at most %d assertions", maxCaseAssertions))
		}
		assertions := make([]AssertionProposal, 0, len(item.Assertions))
		for assertionIndex, assertion := range item.Assertions {
			assertionField := func(name string) string {
				return fmt.Sprintf("cases[%d].assertions[%d].%s", index, assertionIndex, name)
			}
			if assertion.StepSeq < 1 || assertion.StepSeq > len(steps) {
				return GraphProposal{}, Invalid(assertionField("step_seq"), "must reference an existing step sequence")
			}
			switch assertion.Kind {
			case "ui", "api", "db", "text", "visual":
			default:
				return GraphProposal{}, Invalid(assertionField("kind"), "must be one of ui, api, db, text, visual")
			}
			if err := validateText(assertionField("target"), assertion.Target, 1, 2000); err != nil {
				return GraphProposal{}, err
			}
			expected := stringValueOr(assertion.Expected, "")
			if err := validateText(assertionField("expected"), expected, 0, 2000); err != nil {
				return GraphProposal{}, err
			}
			targetSUT, err := parseOptionalID(assertionField("target_version_sut_id"), assertion.TargetVersionSUTID)
			if err != nil {
				return GraphProposal{}, err
			}
			if targetSUT > 0 && !containsID(versionSUTs, targetSUT) {
				return GraphProposal{}, Invalid(assertionField("target_version_sut_id"), "must be part of cases[].version_sut_ids")
			}
			targetAPI, err := parseOptionalID(assertionField("target_api_id"), assertion.TargetAPIID)
			if err != nil {
				return GraphProposal{}, err
			}
			databaseID, err := parseOptionalID(assertionField("database_id"), assertion.DatabaseID)
			if err != nil {
				return GraphProposal{}, err
			}
			if assertion.Kind == "db" && databaseID <= 0 {
				return GraphProposal{}, Invalid(assertionField("database_id"), "is required for db assertions")
			}
			assertions = append(assertions, AssertionProposal{
				StepSeq: assertion.StepSeq, Kind: assertion.Kind, Target: assertion.Target, Expected: expected,
				TargetVersionSUTID: targetSUT, TargetAPIID: targetAPI, DatabaseID: databaseID,
			})
		}

		if len(item.Sources) > maxCaseSources {
			return GraphProposal{}, Invalid(field("sources"), fmt.Sprintf("must contain at most %d sources", maxCaseSources))
		}
		sources := make([]SourceProposal, 0, len(item.Sources))
		for sourceIndex, source := range item.Sources {
			sourceField := func(name string) string {
				return fmt.Sprintf("cases[%d].sources[%d].%s", index, sourceIndex, name)
			}
			switch source.Kind {
			case "requirement", "code", "page", "api", "knowledge":
			default:
				return GraphProposal{}, Invalid(sourceField("kind"), "must be one of requirement, code, page, api, knowledge")
			}
			if err := validateText(sourceField("ref_id"), source.RefID, 1, 200); err != nil {
				return GraphProposal{}, err
			}
			path := stringValueOr(source.Path, "")
			if err := validateText(sourceField("path"), path, 0, 1000); err != nil {
				return GraphProposal{}, err
			}
			if source.Line != nil && *source.Line < 0 {
				return GraphProposal{}, Invalid(sourceField("line"), "must be zero or positive")
			}
			sources = append(sources, SourceProposal{
				Kind: source.Kind, RefID: source.RefID, Path: path, Line: source.Line,
				SHA: stringValueOr(source.SHA, ""),
			})
		}

		proposal.Cases = append(proposal.Cases, CaseProposal{
			Ref: ref, ID: id, ScenarioRef: item.ScenarioRef, Title: item.Title,
			Description: description, Preconditions: preconditions, TestData: testData,
			Priority: priority, DefaultVersionSUTID: defaultSUT,
			VersionSUTIDs: versionSUTs, PageIDs: pageIDs, APIIDs: apiIDs,
			Steps: steps, Assertions: assertions, Sources: sources,
		})
	}
	return proposal, nil
}

func parseRequiredID(field, raw string) (int64, *Error) {
	if strings.TrimSpace(raw) == "" {
		return 0, Invalid(field, "must be a positive decimal ID")
	}
	return parsePositiveID(field, raw)
}

func parseOptionalID(field string, raw *string) (int64, *Error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return 0, nil
	}
	return parsePositiveID(field, *raw)
}

func parseIDList(field string, raw []string) ([]int64, *Error) {
	ids := make([]int64, 0, len(raw))
	seen := make(map[int64]struct{}, len(raw))
	for index, value := range raw {
		if strings.TrimSpace(value) == "" {
			return nil, Invalid(fmt.Sprintf("%s[%d]", field, index), "must be a positive decimal ID")
		}
		id, err := parsePositiveID(fmt.Sprintf("%s[%d]", field, index), value)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

func validateRef(field, raw string) (string, *Error) {
	ref := strings.TrimSpace(raw)
	if ref == "" {
		return "", Invalid(field, "must not be empty")
	}
	if err := validation.String("ref", ref, 1, 64); err != nil {
		return "", invalidFromPlatform(err)
	}
	return ref, nil
}

func containsID(ids []int64, target int64) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

func stringValueOr(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return *value
}

// --- schemas ---------------------------------------------------------------

func idStringSchema() map[string]any {
	return map[string]any{"type": "string", "pattern": `^[1-9][0-9]*$`}
}

func idArraySchema() map[string]any {
	return map[string]any{"type": "array", "items": idStringSchema()}
}

func graphProposeSchema() json.RawMessage {
	stepSchema := schemaObject(map[string]any{
		"seq":                   map[string]any{"type": "integer", "minimum": 1},
		"action":                map[string]any{"type": "string", "minLength": 1, "maxLength": 2000},
		"expected":              map[string]any{"type": "string", "maxLength": 20000},
		"target_version_sut_id": idStringSchema(),
		"target_page_id":        idStringSchema(),
		"target_api_id":         idStringSchema(),
		"account_role":          map[string]any{"type": "string", "maxLength": 200},
		"data":                  map[string]any{"type": "object", "additionalProperties": true},
	}, "seq", "action")
	assertionSchema := schemaObject(map[string]any{
		"step_seq":              map[string]any{"type": "integer", "minimum": 1},
		"kind":                  map[string]any{"type": "string", "enum": []any{"ui", "api", "db", "text", "visual"}},
		"target":                map[string]any{"type": "string", "minLength": 1, "maxLength": 2000},
		"expected":              map[string]any{"type": "string", "maxLength": 2000},
		"target_version_sut_id": idStringSchema(),
		"target_api_id":         idStringSchema(),
		"database_id":           idStringSchema(),
	}, "step_seq", "kind", "target")
	sourceSchema := schemaObject(map[string]any{
		"kind":   map[string]any{"type": "string", "enum": []any{"requirement", "code", "page", "api", "knowledge"}},
		"ref_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 200},
		"path":   map[string]any{"type": "string", "maxLength": 1000},
		"line":   map[string]any{"type": "integer", "minimum": 0},
		"sha":    map[string]any{"type": "string", "maxLength": 64},
	}, "kind", "ref_id")
	caseSchema := schemaObject(map[string]any{
		"ref":                    map[string]any{"type": "string", "minLength": 1, "maxLength": 64},
		"id":                     idStringSchema(),
		"scenario_ref":           map[string]any{"type": "string", "minLength": 1, "maxLength": 64},
		"title":                  map[string]any{"type": "string", "minLength": 1, "maxLength": 200},
		"description":            map[string]any{"type": "string", "maxLength": 20000},
		"preconditions":          map[string]any{"type": "string", "maxLength": 20000},
		"test_data":              map[string]any{"type": "object", "additionalProperties": true},
		"priority":               map[string]any{"type": "string", "enum": []any{"P0", "P1", "P2", "P3"}},
		"default_version_sut_id": idStringSchema(),
		"version_sut_ids":        idArraySchema(),
		"page_ids":               idArraySchema(),
		"api_ids":                idArraySchema(),
		"steps":                  map[string]any{"type": "array", "minItems": 1, "maxItems": maxCaseSteps, "items": stepSchema},
		"assertions":             map[string]any{"type": "array", "maxItems": maxCaseAssertions, "items": assertionSchema},
		"sources":                map[string]any{"type": "array", "maxItems": maxCaseSources, "items": sourceSchema},
	}, "ref", "scenario_ref", "title", "default_version_sut_id", "version_sut_ids", "steps")
	scenarioSchema := schemaObject(map[string]any{
		"ref":             map[string]any{"type": "string", "minLength": 1, "maxLength": 64},
		"id":              idStringSchema(),
		"name":            map[string]any{"type": "string", "minLength": 1, "maxLength": 200},
		"description":     map[string]any{"type": "string", "maxLength": 20000},
		"version_sut_ids": idArraySchema(),
		"page_ids":        idArraySchema(),
		"api_ids":         idArraySchema(),
		"sort_order":      map[string]any{"type": "integer", "minimum": 0},
	}, "ref", "name")
	return schemaObject(map[string]any{
		"requirement_id":         idStringSchema(),
		"requirement_version_id": idStringSchema(),
		"base_graph_hash":        map[string]any{"type": "string", "pattern": `^[0-9a-f]{64}$`},
		"scenarios":              map[string]any{"type": "array", "minItems": 1, "maxItems": maxProposalScenarios, "items": scenarioSchema},
		"cases":                  map[string]any{"type": "array", "minItems": 1, "maxItems": maxProposalCases, "items": caseSchema},
	}, "requirement_id", "requirement_version_id", "scenarios", "cases")
}
