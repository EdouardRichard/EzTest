package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
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

// maxGraphConvergeRetries bounds the server-side re-read/retry loop. A stale
// base_graph_hash is a concurrency condition the tool can repair deterministically
// by reading the current hash; spending the model's remaining budget on another
// read+propose turn is what made the S4 modification fail.
const maxGraphConvergeRetries = 2

var graphHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// proposalIDPattern keeps proposal_id stable and cheap to key an idempotency
// cache on; it intentionally rejects whitespace and control characters.
var proposalIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// GraphProposal is the parsed, tool-validated case.graph_propose payload. IDs
// are parsed to int64 here; the design adapter maps this into its own input.
type GraphProposal struct {
	RequirementID        int64
	RequirementVersionID int64
	BaseGraphHash        string
	// ProposalID/BaseRevision implement the v0.22 E-1 idempotency contract
	// (R-E1-5) at the tool boundary. The design context keys idempotency on the
	// natural case key; the tool additionally replays the same result for a
	// repeated proposal_id and rejects a stale base_revision before writing.
	ProposalID string
	// BaseRevision is the requirement revision the proposal was composed
	// against (1-based). Zero means "not supplied".
	BaseRevision int
	Scenarios    []ScenarioProposal
	Cases        []CaseProposal
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
	ProposalID           *string           `json:"proposal_id"`
	BaseRevision         *int              `json:"base_revision"`
	Scenarios            []scenarioPayload `json:"scenarios"`
	Cases                []casePayload     `json:"cases"`
	// Sources is the proposal-level default provenance. The schema accepts it
	// because models legitimately attach the requirement/knowledge sources once
	// for the whole batch; each case inherits it unless it declares its own.
	Sources []sourcePayload `json:"sources"`
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

// caseGraphProposalCacheEntry is one successful proposal result replayed for a
// duplicate proposal_id within the same run-scoped registry. The digest makes a
// proposal_id collision (same id, different payload) a visible conflict instead
// of silently returning someone else's result.
type caseGraphProposalCacheEntry struct {
	digest string
	result any
}

type caseGraphPropose struct {
	deps Deps

	mu        sync.Mutex
	proposals map[string]caseGraphProposalCacheEntry
}

func (t *caseGraphPropose) Name() string { return ToolCaseGraphPropose }

func (t *caseGraphPropose) Description() string {
	return "提交场景/用例草案（case.graph_propose）。只能创建或更新 draft；不能确认、删除或用例状态变更。" +
		"合法顶层字段：requirement_id、requirement_version_id、base_graph_hash、proposal_id、base_revision、scenarios、cases、sources；每个字段的说明/取值见 Parameters schema。" +
		"scenarios[].id/cases[].id 更新已有节点时传入，新建时省略。sources 放在每个 case 内；也可在顶层传一份作为所有用例的默认来源。" +
		"sources.kind=requirement 时 ref_id 必须是 requirement_version_id（当前需求版本 ID），不是 requirement_id。" +
		"每条步骤与断言都必须有非空的 expected（可观察的成功判据）。" +
		"未知字段（unknown fields）会被严格拒绝：不做别名映射；错误会给出最接近的合法字段名与合法字段清单。" +
		"断言 kind=db 时必须传 database_id，且只能使用当前版本绑定环境中的数据库 id：先调用 sut.database_list（或读取 context.version_pack 的 databases 清单），禁止猜测或用库名/DSN 代替。" +
		"proposal_id 用于幂等：同一次提交的重试请复用同一个 proposal_id，重复提交返回首次结果、不会重复创建 case revision。" +
		"base_revision 用于乐观并发：从 design.graph_read 读取需求 revision 后传入；过期会返回不可重试的 state_conflict（带 base_revision/current_revision），需重读后再提交。" +
		"枚举取值：priority=P0/P1/P2/P3；assertion.kind=ui/api/db/text/visual；sources.kind=requirement/code/page/api/knowledge。"
}

func (t *caseGraphPropose) Parameters() json.RawMessage {
	return graphProposeSchema()
}

func (t *caseGraphPropose) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t.deps.Design == nil {
		return nil, missingDep("design")
	}
	var payload graphProposalPayload
	if err := decodeGraphProposalParams(raw, &payload); err != nil {
		return nil, err
	}
	// The bound database inventory is only read when the proposal actually
	// carries a db assertion; a db-less proposal never pays for the lookup.
	allowedDatabaseIDs := t.allowedDatabaseIDs(ctx, scope, payload)
	proposal, err := parseGraphProposal(payload, scope, allowedDatabaseIDs)
	if err != nil {
		return nil, retryableGraphFieldError(err)
	}

	// R-E1-5: a duplicate proposal_id must return the first result without
	// re-proposing; it is checked before the revision guard so a legitimate
	// retry still succeeds after the graph advanced.
	digest := proposalDigest(raw)
	if proposal.ProposalID != "" {
		if result, found, cacheErr := t.cachedProposal(scope.VersionID, proposal.RequirementID, proposal.ProposalID, digest); cacheErr != nil {
			return nil, cacheErr
		} else if found {
			return result, nil
		}
	}

	if err := t.checkBaseRevision(ctx, scope, proposal); err != nil {
		return nil, err
	}

	var (
		result  any
		portErr error
	)
	for attempt := 0; attempt <= maxGraphConvergeRetries; attempt++ {
		result, portErr = t.deps.Design.ProposeGraph(ctx, scope.VersionID, proposal, 0, scope.AgentRunID)
		if portErr == nil || attempt == maxGraphConvergeRetries || !isGraphChangedConflict(portErr) {
			break
		}
		// Re-read the graph and retry with the fresh hash inside the same tool
		// call, instead of returning a conflict the model must spend another
		// turn on (the final S4 modification ran out of budget there).
		freshHash, readErr := t.readGraphHash(ctx, scope.VersionID, proposal.RequirementID)
		if readErr != nil || freshHash == "" || freshHash == proposal.BaseGraphHash {
			break
		}
		proposal.BaseGraphHash = freshHash
	}
	if portErr != nil {
		return nil, mapPortError(portErr)
	}
	if proposal.ProposalID != "" {
		t.rememberProposal(scope.VersionID, proposal.RequirementID, proposal.ProposalID, digest, result)
	}
	return result, nil
}

// proposalDigest fingerprints the submitted payload bytes. Whitespace differences
// are not normalized: a retry is expected to resend the same arguments.
func proposalDigest(raw json.RawMessage) string {
	sum := sha256.Sum256(bytes.TrimSpace(raw))
	return hex.EncodeToString(sum[:])
}

func (t *caseGraphPropose) cacheKey(versionID, requirementID int64, proposalID string) string {
	return fmt.Sprintf("%d:%d:%s", versionID, requirementID, proposalID)
}

func (t *caseGraphPropose) cachedProposal(versionID, requirementID int64, proposalID, digest string) (any, bool, *Error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, found := t.proposals[t.cacheKey(versionID, requirementID, proposalID)]
	if !found {
		return nil, false, nil
	}
	if entry.digest != digest {
		err := &Error{
			Code:      CodeStateConflict,
			Message:   fmt.Sprintf("proposal_id %q was already used for a different proposal payload; use a new proposal_id for a changed proposal", proposalID),
			Retryable: false,
			Field:     "proposal_id",
			Hint:      "the duplicate was not applied; submit the changed proposal with a fresh proposal_id",
			Details:   map[string]any{"proposal_id": proposalID},
		}
		return nil, false, err
	}
	return entry.result, true, nil
}

func (t *caseGraphPropose) rememberProposal(versionID, requirementID int64, proposalID, digest string, result any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.proposals == nil {
		t.proposals = make(map[string]caseGraphProposalCacheEntry)
	}
	t.proposals[t.cacheKey(versionID, requirementID, proposalID)] = caseGraphProposalCacheEntry{
		digest: digest, result: result,
	}
}

// allowedDatabaseIDs resolves the bound environment database inventory for the
// db assertions of one payload. The read is best-effort: a missing lister or a
// read failure leaves the list empty (the design context still validates the
// database binding and the model still receives the hint), while an available
// list turns a missing/unbound database_id into a precise allowed-values error.
func (t *caseGraphPropose) allowedDatabaseIDs(ctx context.Context, scope Scope, payload graphProposalPayload) []string {
	if !payloadHasDBAssertion(payload) {
		return nil
	}
	lister, ok := t.deps.Resource.(DatabaseLister)
	if !ok {
		return nil
	}
	items, err := lister.ListDatabases(ctx, scope.VersionID, 0)
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if id := strings.TrimSpace(item.ID); id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func payloadHasDBAssertion(payload graphProposalPayload) bool {
	for _, item := range payload.Cases {
		for _, assertion := range item.Assertions {
			if strings.TrimSpace(assertion.Kind) == "db" {
				return true
			}
		}
	}
	return false
}

// checkBaseRevision rejects a proposal composed against a stale requirement
// revision (R-E1-5). It is a non-retryable conflict: blindly retrying the same
// base_revision cannot succeed, the model must re-read the current revision.
// When no revision information is returned the guard stays out of the way; the
// design context's own stale-version check is the authority.
func (t *caseGraphPropose) checkBaseRevision(ctx context.Context, scope Scope, proposal GraphProposal) *Error {
	if proposal.BaseRevision <= 0 {
		return nil
	}
	result, err := t.deps.Design.ReadGraph(ctx, scope.VersionID, proposal.RequirementID, 0)
	if err != nil {
		return nil
	}
	current, found := requirementRevisionFromRead(result, proposal.RequirementID)
	if !found || current == proposal.BaseRevision {
		return nil
	}
	return &Error{
		Code:      CodeStateConflict,
		Message:   fmt.Sprintf("base_revision %d is stale; the requirement is now revision %d", proposal.BaseRevision, current),
		Retryable: false,
		Field:     "base_revision",
		Hint:      "re-read design.graph_read and resubmit with the current requirement revision",
		Details: map[string]any{
			"base_revision":    proposal.BaseRevision,
			"current_revision": current,
			"requirement_id":   idString(proposal.RequirementID),
		},
	}
}

// requirementRevisionFromRead extracts requirements[].revision for one
// requirement from the design.graph_read payload.
func requirementRevisionFromRead(result any, requirementID int64) (int, bool) {
	if result == nil {
		return 0, false
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return 0, false
	}
	var payload struct {
		Requirements []struct {
			RequirementID string `json:"requirement_id"`
			Revision      int    `json:"revision"`
		} `json:"requirements"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return 0, false
	}
	target := idString(requirementID)
	for _, requirement := range payload.Requirements {
		if requirement.RequirementID == target {
			return requirement.Revision, true
		}
	}
	return 0, false
}

// retryableGraphFieldError promotes every field-level graph validation failure
// to the v0.22 E-1 retryable shape (R-E1-3) while preserving the original
// message/field. Validation errors are always model-fixable arguments.
func retryableGraphFieldError(err *Error) *Error {
	if err == nil {
		return nil
	}
	err.Retryable = true
	if err.Field == "" {
		if field, _ := err.Details["field"].(string); field != "" {
			err.Field = field
			err.Details["field"] = field
		}
	}
	return err
}

// readGraphHash reads the current design graph and returns its hash.
func (t *caseGraphPropose) readGraphHash(ctx context.Context, versionID, requirementID int64) (string, error) {
	result, err := t.deps.Design.ReadGraph(ctx, versionID, requirementID, 0)
	if err != nil {
		return "", err
	}
	return graphHashFromRead(result), nil
}

// graphHashFromRead extracts the graph hash from the design adapter payload.
func graphHashFromRead(result any) string {
	if object, ok := result.(map[string]any); ok {
		hash, _ := object["graph_hash"].(string)
		return strings.TrimSpace(hash)
	}
	if result == nil {
		return ""
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return ""
	}
	var payload struct {
		GraphHash string `json:"graph_hash"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.GraphHash)
}

// isGraphChangedConflict reports the optimistic-lock conflict the tool can
// repair by re-reading; other state conflicts (stale requirement version,
// requirement not clarified) need a different action.
func isGraphChangedConflict(err error) bool {
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		return apiErr.Status == 409 && apiErr.Code == "state_conflict" &&
			strings.Contains(apiErr.Message, "design graph changed")
	}
	return false
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

func parseGraphProposal(payload graphProposalPayload, scope Scope, allowedDatabaseIDs []string) (GraphProposal, *Error) {
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
	if payload.ProposalID != nil && strings.TrimSpace(*payload.ProposalID) != "" {
		proposalID := strings.TrimSpace(*payload.ProposalID)
		if !proposalIDPattern.MatchString(proposalID) {
			return GraphProposal{}, InvalidWithAllowed("proposal_id",
				"must be 1-128 characters of letters, digits, dot, underscore, colon or hyphen (no spaces)",
				nil, "reuse the same stable id for a retry of this proposal")
		}
		proposal.ProposalID = proposalID
	}
	if payload.BaseRevision != nil {
		if *payload.BaseRevision < 1 {
			return GraphProposal{}, InvalidField("base_revision", "must be a positive requirement revision (1-based)")
		}
		proposal.BaseRevision = *payload.BaseRevision
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
	topLevelSources, sourceErr := parseSources("sources", payload.Sources, requirementID, requirementVersionID)
	if sourceErr != nil {
		return GraphProposal{}, sourceErr
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
			return GraphProposal{}, InvalidWithAllowed(field("priority"),
				"must be one of P0, P1, P2, P3", []string{"P0", "P1", "P2", "P3"},
				"use the exact enum value; case priority is P0/P1/P2/P3")
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
			if err := validateText(stepField("expected"), expected, 1, 20000); err != nil {
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
				return GraphProposal{}, InvalidWithAllowed(assertionField("kind"),
					fmt.Sprintf("%q is not a supported assertion kind", assertion.Kind),
					[]string{"ui", "api", "db", "text", "visual"},
					"use ui for page checks, api for endpoint checks, db for database checks, text for visible copy; visual requires an explicit baseline")
			}
			if err := validateText(assertionField("target"), assertion.Target, 1, 2000); err != nil {
				return GraphProposal{}, err
			}
			expected := stringValueOr(assertion.Expected, "")
			if err := validateText(assertionField("expected"), expected, 1, 2000); err != nil {
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
			if assertion.Kind == "db" {
				if databaseID <= 0 {
					return GraphProposal{}, InvalidWithAllowed(assertionField("database_id"),
						"database_id is required for db assertions; call sut.database_list and reuse a bound database id",
						allowedDatabaseIDs,
						"call sut.database_list first (or read context.version_pack databases); never guess a database_id or use a database name/DSN")
				}
				if len(allowedDatabaseIDs) > 0 && !containsString(allowedDatabaseIDs, idString(databaseID)) {
					return GraphProposal{}, InvalidWithAllowed(assertionField("database_id"),
						fmt.Sprintf("database_id %q is not bound to this version's environment", idString(databaseID)),
						allowedDatabaseIDs,
						"choose one of the allowed database ids returned by sut.database_list; ids are not inferred from names")
				}
			}
			assertions = append(assertions, AssertionProposal{
				StepSeq: assertion.StepSeq, Kind: assertion.Kind, Target: assertion.Target, Expected: expected,
				TargetVersionSUTID: targetSUT, TargetAPIID: targetAPI, DatabaseID: databaseID,
			})
		}

		if len(item.Sources) > maxCaseSources {
			return GraphProposal{}, Invalid(field("sources"), fmt.Sprintf("must contain at most %d sources", maxCaseSources))
		}
		caseSources, sourceErr := parseSources(field("sources"), item.Sources, requirementID, requirementVersionID)
		if sourceErr != nil {
			return GraphProposal{}, sourceErr
		}
		if len(caseSources) == 0 && len(topLevelSources) > 0 {
			caseSources = append([]SourceProposal(nil), topLevelSources...)
		}

		proposal.Cases = append(proposal.Cases, CaseProposal{
			Ref: ref, ID: id, ScenarioRef: item.ScenarioRef, Title: item.Title,
			Description: description, Preconditions: preconditions, TestData: testData,
			Priority: priority, DefaultVersionSUTID: defaultSUT,
			VersionSUTIDs: versionSUTs, PageIDs: pageIDs, APIIDs: apiIDs,
			Steps: steps, Assertions: assertions, Sources: caseSources,
		})
	}
	return proposal, nil
}

// parseSources validates one provenance list (case-level or proposal-level) and
// resolves an unambiguous requirement-id citation to the requirement version.
func parseSources(fieldPrefix string, raw []sourcePayload, requirementID, requirementVersionID int64) ([]SourceProposal, *Error) {
	sources := make([]SourceProposal, 0, len(raw))
	for sourceIndex, source := range raw {
		sourceField := func(name string) string {
			return fmt.Sprintf("%s[%d].%s", fieldPrefix, sourceIndex, name)
		}
		switch source.Kind {
		case "requirement", "code", "page", "api", "knowledge":
		default:
			return nil, InvalidWithAllowed(sourceField("kind"),
				fmt.Sprintf("%q is not a supported source kind", source.Kind),
				[]string{"requirement", "code", "page", "api", "knowledge"},
				"requirement sources cite the current requirement_version_id; page/api sources cite bound catalog ids")
		}
		if err := validateText(sourceField("ref_id"), source.RefID, 1, 200); err != nil {
			return nil, err
		}
		// A requirement source is an immutable provenance reference, so it
		// must be the requirement version. A model that cites the requirement
		// id it was scoped with is unambiguous: resolve exactly that reference
		// to the proposal's current requirement version. Any other ID is left
		// untouched and validated by the design context.
		refID := source.RefID
		if source.Kind == "requirement" && refID == fmt.Sprintf("%d", requirementID) {
			refID = fmt.Sprintf("%d", requirementVersionID)
		}
		path := stringValueOr(source.Path, "")
		if err := validateText(sourceField("path"), path, 0, 1000); err != nil {
			return nil, err
		}
		if source.Line != nil && *source.Line < 0 {
			return nil, Invalid(sourceField("line"), "must be zero or positive")
		}
		sources = append(sources, SourceProposal{
			Kind: source.Kind, RefID: refID, Path: path, Line: source.Line,
			SHA: stringValueOr(source.SHA, ""),
		})
	}
	return sources, nil
}

// --- strict decode and unknown-field suggestions ---------------------------

// decodeGraphProposalParams keeps R-E1-1's strict decoding (DisallowUnknownFields)
// and adds R-E1-4's self-correction data: the exact JSON path of the unknown
// field, the nearest legal field name at that nesting level, and the full legal
// field list. Aliases are never mapped silently.
func decodeGraphProposalParams(raw json.RawMessage, target *graphProposalPayload) *Error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		trimmed = []byte(`{}`)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return InvalidField("parameters", "parameters must be a JSON object")
	}
	if path, unknown, allowed, found := findUnknownJSONField(trimmed, reflect.TypeOf(*target), ""); found {
		suggestion := nearestJSONField(unknown, allowed)
		message := fmt.Sprintf("unknown field %q; nearest legal field is %q; unknown fields are rejected and aliases are not accepted", unknown, suggestion)
		err := InvalidWithAllowed(path, message, allowed,
			"remove or rename the unknown field; the tool schema lists every legal field and design.graph_read shows the current graph shape")
		err.Details["unknown"] = unknown
		err.Details["suggestion"] = suggestion
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return InvalidField("parameters", "invalid parameters: "+err.Error())
	}
	return nil
}

// findUnknownJSONField walks the raw JSON alongside the target Go type and
// returns the first unknown object key with its full path. Reaching a
// json.RawMessage/interface field (test_data/data) stops the walk: those are
// intentionally free-form objects.
func findUnknownJSONField(raw json.RawMessage, typ reflect.Type, path string) (string, string, []string, bool) {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ == reflect.TypeOf(json.RawMessage{}) || typ.Kind() == reflect.Interface {
		return "", "", nil, false
	}
	switch typ.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return "", "", nil, false
		}
		allowed, byName := jsonStructFields(typ)
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			valueType, ok := byName[key]
			if !ok {
				return joinJSONPath(path, key), key, allowed, true
			}
			if foundPath, unknown, fields, found := findUnknownJSONField(object[key], valueType, joinJSONPath(path, key)); found {
				return foundPath, unknown, fields, true
			}
		}
	case reflect.Slice, reflect.Array:
		element := typ.Elem()
		for element.Kind() == reflect.Pointer {
			element = element.Elem()
		}
		if element.Kind() != reflect.Struct {
			return "", "", nil, false
		}
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return "", "", nil, false
		}
		for index, item := range items {
			if foundPath, unknown, fields, found := findUnknownJSONField(item, element, fmt.Sprintf("%s[%d]", path, index)); found {
				return foundPath, unknown, fields, true
			}
		}
	}
	return "", "", nil, false
}

// jsonStructFields returns the sorted JSON names of a struct plus their types.
func jsonStructFields(typ reflect.Type) ([]string, map[string]reflect.Type) {
	allowed := make([]string, 0, typ.NumField())
	byName := make(map[string]reflect.Type, typ.NumField())
	for index := 0; index < typ.NumField(); index++ {
		field := typ.Field(index)
		if field.PkgPath != "" {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		allowed = append(allowed, name)
		byName[name] = field.Type
	}
	sort.Strings(allowed)
	return allowed, byName
}

func joinJSONPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// nearestJSONField picks the legal field with the smallest edit distance
// (case-insensitive); ties resolve lexicographically for determinism.
func nearestJSONField(unknown string, allowed []string) string {
	lower := strings.ToLower(unknown)
	best := ""
	bestDistance := -1
	for _, candidate := range allowed {
		distance := levenshteinDistance(lower, strings.ToLower(candidate))
		if bestDistance < 0 || distance < bestDistance || (distance == bestDistance && candidate < best) {
			best, bestDistance = candidate, distance
		}
	}
	return best
}

// levenshteinDistance is the classic two-row edit distance over runes.
func levenshteinDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}
	previous := make([]int, len(br)+1)
	current := make([]int, len(br)+1)
	for j := 0; j <= len(br); j++ {
		previous[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		current[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			deletion := previous[j] + 1
			insertion := current[j-1] + 1
			substitution := previous[j-1] + cost
			current[j] = minInt(deletion, minInt(insertion, substitution))
		}
		previous, current = current, previous
	}
	return previous[len(br)]
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
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

// graphProposeSchema is the self-describing v0.22 E-1 contract (R-E1-1). Every
// field documents its unit/values/when-required, enums are explicit, the
// assertion branches are expressed with anyOf (db requires database_id), two
// complete examples (including a db assertion) show the whole shape, and the
// root description names the legal top-level fields and the strict
// unknown-field policy. additionalProperties stays false at every level.
func graphProposeSchema() json.RawMessage {
	id := func(description string) map[string]any {
		schema := idStringSchema()
		schema["description"] = description
		return schema
	}
	ids := func(description string) map[string]any {
		schema := idArraySchema()
		schema["description"] = description
		return schema
	}
	stepSchema := schemaObject(map[string]any{
		"seq": map[string]any{
			"type": "integer", "minimum": 1,
			"description": "步骤序号，从 1 开始且必须连续（1..N），断言 step_seq 引用它。",
		},
		"action": map[string]any{
			"type": "string", "minLength": 1, "maxLength": 2000,
			"description": "必填。本步骤的可执行动作（点击/输入/调用等），不得为空。",
		},
		"expected": map[string]any{
			"type": "string", "minLength": 1, "maxLength": 20000,
			"description": "必填。可观察的成功结果；不能为空或只写“成功”。",
		},
		"target_version_sut_id": id("可选。步骤目标 version_sut 的十进制 ID，必须属于 cases[].version_sut_ids；省略表示沿用用例默认 SUT。"),
		"target_page_id":        id("可选。步骤目标页面的十进制 ID（来自 sut.page_list 的 kind=page 条目）；不是 path/URL。"),
		"target_api_id":         id("可选。步骤目标 API 的十进制 ID（来自 sut.page_list 的 kind=api 条目）；不是 method+path。"),
		"account_role": map[string]any{
			"type": "string", "maxLength": 200,
			"description": "可选。执行该步骤使用的账号角色（如 admin），对应绑定环境的测试账号角色。",
		},
		"data": map[string]any{
			"type": "object", "additionalProperties": true,
			"description": "可选。步骤输入数据（自由 JSON 对象），键值由用例自行约定。",
		},
	}, "seq", "action", "expected")
	assertionProperties := map[string]any{
		"step_seq": map[string]any{
			"type": "integer", "minimum": 1,
			"description": "必填。断言所属步骤的 seq（必须对应 cases[].steps[].seq 中已存在的步骤）。",
		},
		"kind": map[string]any{
			"type":        "string",
			"enum":        []any{"ui", "api", "db", "text", "visual"},
			"description": "必填。断言类型枚举：ui=页面元素/交互；api=接口响应；db=数据库（必须同时给 database_id）；text=页面可见文案；visual=截图/视觉基线（仅需求明确要求时使用）。",
		},
		"target": map[string]any{
			"type": "string", "minLength": 1, "maxLength": 2000,
			"description": "必填。断言对象：ui 用元素/选择器描述，api 用 method+path，db 用表/查询，text/visual 用区域描述。",
		},
		"expected": map[string]any{
			"type": "string", "minLength": 1, "maxLength": 2000,
			"description": "必填。可观察的成功判据（期望值），不能为空。",
		},
		"target_version_sut_id": id("可选。断言目标 version_sut 的十进制 ID，必须属于 cases[].version_sut_ids。"),
		"target_api_id":         id("可选。api 断言的目标 API 十进制 ID（来自 sut.page_list 的 kind=api 条目）。"),
		"database_id":           id("kind=db 时必填。绑定环境目标数据库的十进制 ID：只能取 sut.database_list / context.version_pack.databases 返回的 id，禁止猜测或用库名/DSN 代替。"),
	}
	assertionSchema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"step_seq", "kind", "target", "expected"},
		"properties":           assertionProperties,
		"description":          "断言对象。按 kind 分支（anyOf）：ui=target_page_id+步骤动作+expected；api=target_api_id/method+path+expected；db=database_id+查询/expected；text=可见文案；visual=基线/截图断言。",
		"anyOf": []any{
			map[string]any{
				"description": "ui 断言分支：页面元素/交互结果",
				"properties":  map[string]any{"kind": map[string]any{"enum": []any{"ui"}}},
			},
			map[string]any{
				"description": "api 断言分支：接口响应的 method/path/状态码或响应体",
				"properties":  map[string]any{"kind": map[string]any{"enum": []any{"api"}}},
			},
			map[string]any{
				"description": "db 断言分支：必须提供绑定环境的 database_id（来自 sut.database_list）",
				"properties": map[string]any{
					"kind":        map[string]any{"enum": []any{"db"}},
					"database_id": idStringSchema(),
				},
				"required": []string{"database_id"},
			},
			map[string]any{
				"description": "text 断言分支：页面可见文案",
				"properties":  map[string]any{"kind": map[string]any{"enum": []any{"text"}}},
			},
			map[string]any{
				"description": "visual 断言分支：仅在需求/验收标准显式要求视觉基线时使用",
				"properties":  map[string]any{"kind": map[string]any{"enum": []any{"visual"}}},
			},
		},
	}
	sourceSchema := schemaObject(map[string]any{
		"kind": map[string]any{
			"type": "string", "enum": []any{"requirement", "code", "page", "api", "knowledge"},
			"description": "必填。来源类型枚举：requirement=需求版本；code=代码位置；page=已绑定页面；api=已绑定接口；knowledge=已批准知识。",
		},
		"ref_id": map[string]any{
			"type": "string", "minLength": 1, "maxLength": 200,
			"description": "必填。来源引用 ID：kind=requirement 时必须是当前 requirement_version_id（不是 requirement_id）；page/api 用绑定的 catalog ID；code 用文件路径/引用；knowledge 用知识 ID。",
		},
		"path": map[string]any{
			"type": "string", "maxLength": 1000,
			"description": "可选。来源文件路径或定位路径（code 来源常用）。",
		},
		"line": map[string]any{
			"type": "integer", "minimum": 0,
			"description": "可选。来源行号（0 或正整数）。",
		},
		"sha": map[string]any{
			"type": "string", "maxLength": 64,
			"description": "可选。来源内容/提交的 SHA 摘要。",
		},
	}, "kind", "ref_id")
	caseSchema := schemaObject(map[string]any{
		"ref": map[string]any{
			"type": "string", "minLength": 1, "maxLength": 64,
			"description": "必填。本次 proposal 内的临时引用名，断言/scenario_ref 用它关联；不是数据库 ID。",
		},
		"id": id("可选。更新已有用例时传用例十进制 ID；新建时省略。"),
		"scenario_ref": map[string]any{
			"type": "string", "minLength": 1, "maxLength": 64,
			"description": "必填。所属场景在本 proposal 内的 ref（必须在 scenarios 中存在）。",
		},
		"title": map[string]any{
			"type": "string", "minLength": 1, "maxLength": 200,
			"description": "必填。用例标题，建议使用对应验收要点原文。",
		},
		"description": map[string]any{
			"type": "string", "maxLength": 20000,
			"description": "可选。用例说明。",
		},
		"preconditions": map[string]any{
			"type": "string", "maxLength": 20000,
			"description": "可选。前置条件（账号、数据、环境状态）。",
		},
		"test_data": map[string]any{
			"type": "object", "additionalProperties": true,
			"description": "可选。测试数据（自由 JSON 对象）。",
		},
		"priority": map[string]any{
			"type": "string", "enum": []any{"P0", "P1", "P2", "P3"},
			"description": "可选，默认 P2。优先级枚举：P0 最高（核心阻断）→ P3 最低。只能取 P0/P1/P2/P3。",
		},
		"default_version_sut_id": id("必填。默认 version_sut 的十进制 ID，必须属于 cases[].version_sut_ids。"),
		"version_sut_ids":        ids("必填。该用例可使用的 version_sut 十进制 ID 列表，至少一个。"),
		"page_ids":               ids("可选。用例涉及的已绑定页面十进制 ID 列表（来自 sut.page_list 的 kind=page）。"),
		"api_ids":                ids("可选。用例涉及的已绑定接口十进制 ID 列表（来自 sut.page_list 的 kind=api）。"),
		"steps": map[string]any{
			"type": "array", "minItems": 1, "maxItems": maxCaseSteps, "items": stepSchema,
			"description": "必填。可执行步骤，seq 从 1 连续递增，至少一步。",
		},
		"assertions": map[string]any{
			"type": "array", "maxItems": maxCaseAssertions, "items": assertionSchema,
			"description": "可选。断言列表；kind=db 的项必须带 database_id。",
		},
		"sources": map[string]any{
			"type": "array", "maxItems": maxCaseSources, "items": sourceSchema,
			"description": "可选。该用例的来源；省略时继承顶层 sources。",
		},
	}, "ref", "scenario_ref", "title", "default_version_sut_id", "version_sut_ids", "steps")
	scenarioSchema := schemaObject(map[string]any{
		"ref": map[string]any{
			"type": "string", "minLength": 1, "maxLength": 64,
			"description": "必填。本次 proposal 内的场景引用名（唯一），cases[].scenario_ref 引用它。",
		},
		"id": id("可选。更新已有场景时传场景十进制 ID；新建时省略。"),
		"name": map[string]any{
			"type": "string", "minLength": 1, "maxLength": 200,
			"description": "必填。场景名，建议使用需求标题原文。",
		},
		"description": map[string]any{
			"type": "string", "maxLength": 20000,
			"description": "可选。场景说明。",
		},
		"version_sut_ids": ids("可选。场景使用的 version_sut 十进制 ID 列表。"),
		"page_ids":        ids("可选。场景关联的已绑定页面十进制 ID 列表。"),
		"api_ids":         ids("可选。场景关联的已绑定接口十进制 ID 列表。"),
		"sort_order": map[string]any{
			"type": "integer", "minimum": 0,
			"description": "可选。场景排序（0 或正整数，越小越靠前）。",
		},
	}, "ref", "name")
	properties := map[string]any{
		"requirement_id":         id("必填。需求十进制 ID，必须与本次 run 注入的需求作用域一致。"),
		"requirement_version_id": id("必填。当前需求版本十进制 ID（不是 requirement_id），必须与注入的需求版本作用域一致；sources.kind=requirement 的 ref_id 用它。"),
		"base_graph_hash": map[string]any{
			"type": "string", "pattern": `^[0-9a-f]{64}$`,
			"description": "可选。design.graph_read 返回的当前 graph_hash（64 位小写十六进制）；用于乐观锁，过期会触发服务端重读并重试。",
		},
		"proposal_id": map[string]any{
			"type": "string", "pattern": `^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`,
			"description": "可选。幂等键：同一次提交的重试复用同一个 proposal_id；重复提交返回首次结果，不会重复创建 case revision。改内容时必须换新 id。",
		},
		"base_revision": map[string]any{
			"type": "integer", "minimum": 1,
			"description": "可选。提交所基于的需求 revision（1 开始，取自 design.graph_read 的 requirements[].revision）；与当前 revision 不一致时返回不可重试的 state_conflict。",
		},
		"scenarios": map[string]any{
			"type": "array", "minItems": 1, "maxItems": maxProposalScenarios, "items": scenarioSchema,
			"description": "必填。场景列表，1..3 个；新增/更新场景都在这里提交。",
		},
		"cases": map[string]any{
			"type": "array", "minItems": 1, "maxItems": maxProposalCases, "items": caseSchema,
			"description": "必填。用例列表，1..10 个；每个用例带完整步骤/断言/来源。",
		},
		"sources": map[string]any{
			"type": "array", "maxItems": maxCaseSources, "items": sourceSchema,
			"description": "可选。所有用例的默认来源；用例自带 sources 时以用例为准。",
		},
	}
	document := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
		"required":             []string{"requirement_id", "requirement_version_id", "scenarios", "cases"},
		"description": "提交场景/用例 draft 的自描述 schema。合法顶层字段：requirement_id、requirement_version_id、base_graph_hash、proposal_id、base_revision、scenarios、cases、sources。" +
			" unknown fields are strictly rejected（未知字段会被拒绝，不做别名映射），错误会指出最接近的合法字段；db 断言必须带 database_id（来自 sut.database_list / context.version_pack.databases）。",
		"examples": graphProposeExamples(),
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return emptyObjectSchema
	}
	return raw
}

// graphProposeExamples returns two complete, copyable proposals: one ordinary
// ui/api update and one db assertion keyed by the bound database id. They are
// part of the model-visible contract (R-E1-1), not test fixtures.
func graphProposeExamples() []any {
	return []any{
		map[string]any{
			"requirement_id":         "501",
			"requirement_version_id": "700",
			"scenarios": []any{
				map[string]any{
					"ref": "s1", "name": "正常登录", "description": "手机号密码登录",
					"version_sut_ids": []any{"9001"}, "page_ids": []any{"9101"}, "api_ids": []any{"9201"},
				},
			},
			"cases": []any{
				map[string]any{
					"ref": "c1", "scenario_ref": "s1", "title": "账号密码登录成功", "priority": "P1",
					"default_version_sut_id": "9001", "version_sut_ids": []any{"9001"},
					"steps": []any{
						map[string]any{"seq": 1, "action": "打开登录页", "expected": "显示登录表单",
							"target_version_sut_id": "9001", "target_page_id": "9101"},
						map[string]any{"seq": 2, "action": "提交有效账号密码", "expected": "跳转到首页",
							"target_version_sut_id": "9001", "target_api_id": "9201"},
					},
					"assertions": []any{
						map[string]any{"step_seq": 1, "kind": "ui", "target": "登录表单", "expected": "表单可见"},
						map[string]any{"step_seq": 2, "kind": "api", "target": "POST /login", "expected": "HTTP 200"},
					},
					"sources": []any{
						map[string]any{"kind": "requirement", "ref_id": "700"},
					},
				},
			},
		},
		map[string]any{
			"requirement_id":         "501",
			"requirement_version_id": "700",
			"proposal_id":            "login-db-check-1",
			"base_revision":          3,
			"scenarios": []any{
				map[string]any{
					"ref": "s1", "name": "正常登录", "version_sut_ids": []any{"9001"},
				},
			},
			"cases": []any{
				map[string]any{
					"ref": "c1", "scenario_ref": "s1", "title": "登录成功后写入登录审计", "priority": "P1",
					"default_version_sut_id": "9001", "version_sut_ids": []any{"9001"},
					"steps": []any{
						map[string]any{"seq": 1, "action": "提交有效账号密码", "expected": "登录成功",
							"target_version_sut_id": "9001", "target_api_id": "9201"},
					},
					"assertions": []any{
						map[string]any{"step_seq": 1, "kind": "db",
							"target":   "select count(*) from login_audit where user_id = 42",
							"expected": "count = 1", "database_id": "1501"},
					},
					"sources": []any{
						map[string]any{"kind": "requirement", "ref_id": "700"},
					},
				},
			},
		},
	}
}
