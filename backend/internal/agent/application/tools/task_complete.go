package tools

// Task completion tool (v0.22 spec R-A3-1..R-A3-4, design §4.3; A §5.2 D-A14).
//
// task.complete is the model-facing half of the completion criterion. The
// declaration alone never ends a run: the injected Validator re-reads the
// server-side state for the task type (extraction run done + count
// reconciliation, target plan item done + decision written, design revision /
// review artifact present, Host turn answered or handed off), resolves the one
// plan item target this completion writes, and only then does the injected
// CompletionWriter persist that item's status/decision and the progress fact in
// one transaction/outbox. A rejected declaration is a retryable
// invalid_argument tool error, so the model can fix the missing work instead of
// the run failing (R-A3-3).
//
// Every W0 completion writes exactly one plan item (R-A3-4): a validator that
// cannot resolve a positive target fails closed as an internal error, and the
// completion writer takes the target only from the verified commit — there is
// no fact-only success path and no second target the writer could disagree
// with.
//
// Wiring (Task 36): the composition root fills tools.Deps.Completion per run and
// registers this group through the existing registration list:
//
//	tools.RegisterP4ToolGroup(tools.TaskCompleteTools)
//
// NewP7Registry composes registered P4 groups, so runner.go needs no new
// registration hook. The runner must still translate *TaskCompleteResult into
// Usage.Completed (the result implements CompletionSignal) before Budget.Decide;
// that Lead-only wiring belongs to Task 36.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// Completion tool name, status and bounds.
const (
	// ToolTaskComplete is the v0.22 completion declaration tool (R-A3-1).
	ToolTaskComplete = "task.complete"
	// CompletionStatusCompleted is the only accepted task.complete status:
	// task.complete signals a successful completion, not a failed run.
	CompletionStatusCompleted = "completed"

	maxCompletionSummaryRunes  = 4000
	maxCompletionArtifacts     = 64
	maxCompletionArtifactRunes = 512
)

// CompletionTaskKind selects the server-side completion rule (R-A3-2). The
// runner resolves it from the work item/profile and binds the matching
// Validator; the model never supplies it.
type CompletionTaskKind string

const (
	// CompletionKindExtraction validates an extraction run: run done and
	// expected count reconciled with the extracted count.
	CompletionKindExtraction CompletionTaskKind = "extraction"
	// CompletionKindPlanItem validates the target plan item: status done and a
	// decision recorded.
	CompletionKindPlanItem CompletionTaskKind = "plan_item"
	// CompletionKindDesign validates a design run: revision/review artifact
	// present.
	CompletionKindDesign CompletionTaskKind = "design"
	// CompletionKindHost validates a Host turn: the current question answered or
	// handed off.
	CompletionKindHost CompletionTaskKind = "host"
)

// Completion validation sentinels. Validators may wrap them in *CompletionError
// to carry the structured details the tool surfaces to the model.
var (
	// ErrMissingArtifacts reports a declaration without the deliverables the
	// task type requires (R-A3-3: forged completion is rejected).
	ErrMissingArtifacts = errors.New("task.complete requires deliverables")
	// ErrNotComplete reports that a server-side completion condition is not met.
	ErrNotComplete = errors.New("task.complete completion criteria are not met")
)

// CompletionError is a rejected completion declaration. It always becomes a
// retryable invalid_argument tool error; Missing names what is absent and Hint
// tells the model how to fix it. Details are copied into the tool error.
type CompletionError struct {
	Missing  string
	Hint     string
	Details  map[string]any
	sentinel error
}

// Error implements error.
func (e *CompletionError) Error() string {
	if e == nil {
		return "task.complete rejected"
	}
	message := "completion rejected: " + e.Missing
	if e.Hint != "" {
		message += "; " + e.Hint
	}
	return message
}

// Unwrap exposes ErrMissingArtifacts / ErrNotComplete to errors.Is.
func (e *CompletionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.sentinel
}

// NewMissingArtifactsError builds the "declaration without deliverables"
// rejection. kind names the task type and examples are concrete artifact
// references the model can send (for example extraction:<run_id>).
func NewMissingArtifactsError(kind CompletionTaskKind, examples ...string) *CompletionError {
	hint := "include at least one concrete artifact reference and retry"
	details := map[string]any{"field": "artifacts", "kind": string(kind)}
	if len(examples) > 0 {
		allowed := append([]string(nil), examples...)
		details["allowed"] = allowed
		hint = "include at least one concrete artifact reference (for example " +
			strings.Join(allowed, ", ") + ") and retry"
	}
	return &CompletionError{
		Missing:  fmt.Sprintf("artifacts is empty but this %s task requires deliverables", kind),
		Hint:     hint,
		Details:  details,
		sentinel: ErrMissingArtifacts,
	}
}

// RequireArtifacts rejects an empty artifact list for a task kind that has
// explicit deliverables. It is the shared (a) validation of R-A3.
func RequireArtifacts(req CompletionRequest, kind CompletionTaskKind, examples ...string) error {
	if len(req.Artifacts) > 0 {
		return nil
	}
	return NewMissingArtifactsError(kind, examples...)
}

// NewNotCompleteError builds a structured "criterion not met" rejection.
func NewNotCompleteError(missing, hint string, details map[string]any) *CompletionError {
	return &CompletionError{
		Missing:  missing,
		Hint:     hint,
		Details:  details,
		sentinel: ErrNotComplete,
	}
}

// NewExtractionNotDoneError reports an extraction run that has not finished.
func NewExtractionNotDoneError(state ExtractionReconciliation) *CompletionError {
	details := map[string]any{
		"field":     "extraction",
		"run_id":    idString(state.RunID),
		"done":      state.Done,
		"expected":  state.Expected,
		"extracted": state.Extracted,
	}
	return NewNotCompleteError(
		fmt.Sprintf("extraction run %d is not finished", state.RunID),
		"wait for the extraction run to finish (all segments done) and retry",
		details,
	)
}

// NewExtractionMismatchError reports a reconciled extraction whose extracted
// count still differs from the declared/expected count. The reported gap is
// always non-negative: a shortfall is "missing", an excess is "over_extracted"
// (never a negative missing count).
func NewExtractionMismatchError(state ExtractionReconciliation) *CompletionError {
	details := map[string]any{
		"field":     "extraction",
		"run_id":    idString(state.RunID),
		"expected":  state.Expected,
		"extracted": state.Extracted,
	}
	var message string
	switch gap := state.Extracted - state.Expected; {
	case gap > 0:
		details["over_extracted"] = gap
		message = fmt.Sprintf("extraction run %d extracted %d of %d requirements (%d over-extracted)",
			state.RunID, state.Extracted, state.Expected, gap)
	case gap < 0:
		details["missing"] = -gap
		message = fmt.Sprintf("extraction run %d reconciled %d of %d requirements (%d missing)",
			state.RunID, state.Extracted, state.Expected, -gap)
	default:
		message = fmt.Sprintf("extraction run %d reconciled %d of %d requirements",
			state.RunID, state.Extracted, state.Expected)
	}
	return NewNotCompleteError(
		message,
		"reconcile the extraction counts (re-run the extraction or resolve the mismatched requirements) and retry",
		details,
	)
}

// NewPlanItemNotDoneError reports a target plan item that is not done yet.
func NewPlanItemNotDoneError(itemID int64, status string) *CompletionError {
	return NewNotCompleteError(
		fmt.Sprintf("plan item %d is %q, not done", itemID, status),
		"finish the plan item (plan.update status=done plus decision) and retry",
		map[string]any{
			"field":   "plan_item",
			"item_id": idString(itemID),
			"status":  status,
		},
	)
}

// NewPlanItemMissingDecisionError reports a done plan item without a decision.
func NewPlanItemMissingDecisionError(itemID int64) *CompletionError {
	return NewNotCompleteError(
		fmt.Sprintf("plan item %d is done but has no decision", itemID),
		"record the plan-item decision with plan.update and retry",
		map[string]any{
			"field":   "decision",
			"item_id": idString(itemID),
		},
	)
}

// CompletionRequest is one decoded task.complete declaration plus the
// runtime-injected scope. Artifacts are trimmed, deduplicated and non-empty.
type CompletionRequest struct {
	Scope     Scope
	Summary   string
	Artifacts []string
	Status    string
}

// CompletionVerification is the server-side result of a passing completion
// criterion: the single plan item target this completion must write. Every W0
// completion targets a plan item (R-A3-4), so a validator that cannot name a
// positive target fails closed instead of allowing a fact-only success.
type CompletionVerification struct {
	// PlanItemID is the target plan item the validator verified or resolved for
	// this run. It is the only target the completion writer receives.
	PlanItemID int64
}

// Validator is the server-side completion criterion of one task type
// (R-A3-2). It returns the verified plan item target on success.
// Implementations may return *Error, *CompletionError or a *platform.Error;
// unexpected errors stay internal and fail the run.
type Validator interface {
	Validate(ctx context.Context, req CompletionRequest) (CompletionVerification, error)
}

// CompletionCommit is a verified completion handed to the write port. PlanItemID
// is copied unchanged from the validator's CompletionVerification, so the write
// target is the verified target by construction; the writer has no separate
// target to disagree with.
type CompletionCommit struct {
	Scope      Scope
	PlanItemID int64
	Summary    string
	Artifacts  []string
	Status     string
}

// CompletionOutcome reports what the write port persisted.
type CompletionOutcome struct {
	PlanItemID       int64
	PlanStatus       string
	Decision         string
	PlanItemRevision int
	FactID           int64
	FactKey          string
}

// CompletionWriter persists the completion side effects (plan item
// status/decision and progress fact) in one transaction or outbox entry
// (R-A3-4/R-A8-3). The production Postgres adapter lands with Task 36 wiring.
type CompletionWriter interface {
	Commit(ctx context.Context, commit CompletionCommit) (CompletionOutcome, error)
}

// CompletionSignal is implemented by successful task.complete results. The
// runner translates it into Usage.Completed before Budget.Decide (Task 36
// wiring); returning the result is how this package avoids touching runner.go.
type CompletionSignal interface {
	SignalsCompletion() bool
}

// TaskCompleteResult is the model-visible success payload and the completion
// signal. Completed is always true on a successful call. PlanItemRevision is
// the new plan-item optimistic-lock revision the write port returned, so the
// runner (Task 36) can reuse it without a re-read.
type TaskCompleteResult struct {
	Completed        bool   `json:"completed"`
	Status           string `json:"status"`
	PlanItemID       string `json:"plan_item_id,omitempty"`
	PlanStatus       string `json:"plan_status,omitempty"`
	PlanItemRevision int    `json:"plan_item_revision,omitempty"`
	FactID           string `json:"fact_id,omitempty"`
}

// SignalsCompletion implements CompletionSignal.
func (r *TaskCompleteResult) SignalsCompletion() bool { return r != nil && r.Completed }

var (
	_ CompletionSignal = (*TaskCompleteResult)(nil)
	_ Tool             = (*TaskCompleteTool)(nil)
)

// TaskCompleteTool is the task.complete tool.
type TaskCompleteTool struct {
	validator Validator
	writer    CompletionWriter
}

// NewTaskCompleteTool builds the tool around the server-side validator and the
// verified-completion writer of one run.
func NewTaskCompleteTool(validator Validator, writer CompletionWriter) *TaskCompleteTool {
	return &TaskCompleteTool{validator: validator, writer: writer}
}

// Name implements Tool.
func (t *TaskCompleteTool) Name() string { return ToolTaskComplete }

// Description implements Tool.
func (t *TaskCompleteTool) Description() string {
	return "声明当前任务已完成（task.complete）：提交完成摘要、产物引用与状态；服务端会按任务类型校验交付物（抽取 run 完成且条数对账、plan item 完成且 decision 已写、设计 revision/评审产物存在、Host 本轮问题已回答或转交），校验通过才会结束 run 并写入 plan 状态/decision 与 progress fact。缺少产物时返回可重试的 invalid_argument，请补齐后重试。"
}

// Parameters implements Tool. Scope IDs are intentionally absent; they are
// injected by the runtime.
func (t *TaskCompleteTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"summary": map[string]any{
			"type":        "string",
			"minLength":   1,
			"maxLength":   maxCompletionSummaryRunes,
			"description": "完成摘要：说明已完成的交付物与关键结果（作为 plan decision / progress fact 的内容）。",
		},
		"artifacts": map[string]any{
			"type":     "array",
			"maxItems": maxCompletionArtifacts,
			"items": map[string]any{
				"type":      "string",
				"minLength": 1,
				"maxLength": maxCompletionArtifactRunes,
			},
			"description": "交付物引用列表（如 extraction:<run_id>、design:<case_version_id>、plan_item:<id>）；有明确交付物的任务类型必须提供。",
		},
		"status": map[string]any{
			"type":        "string",
			"enum":        []string{CompletionStatusCompleted},
			"description": "完成状态；省略时默认为 completed。",
		},
	}, "summary")
}

// taskCompleteParams is the model-visible argument shape. A pointer status lets
// the tool distinguish "absent" (default completed) from an explicit value.
type taskCompleteParams struct {
	Summary   string   `json:"summary"`
	Artifacts []string `json:"artifacts"`
	Status    *string  `json:"status"`
}

// Execute implements Tool.
func (t *TaskCompleteTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t == nil || t.validator == nil {
		return nil, missingDep("completion validator")
	}
	if t.writer == nil {
		return nil, missingDep("completion writer")
	}
	if scope.VersionID <= 0 {
		return nil, Invalid("scope", "task.complete requires the injected version scope")
	}
	var params taskCompleteParams
	if err := decodeParams(raw, &params); err != nil {
		err.Retryable = true
		return nil, err
	}
	req, toolErr := normalizeCompletionRequest(scope, params)
	if toolErr != nil {
		return nil, toolErr
	}
	verification, err := t.validator.Validate(ctx, req)
	if err != nil {
		return nil, mapCompletionError(err)
	}
	// Every W0 completion writes one plan item's status/decision plus the
	// progress fact (R-A3-4). A validator that verified the task but resolved no
	// target is a wiring bug, not a model-fixable rejection: fail the run
	// instead of signaling Completed with a fact-only write.
	if verification.PlanItemID <= 0 {
		return nil, fmt.Errorf("task.complete validator resolved no target plan item; every completion must target a plan item")
	}
	outcome, err := t.writer.Commit(ctx, CompletionCommit{
		Scope:      req.Scope,
		PlanItemID: verification.PlanItemID,
		Summary:    req.Summary,
		Artifacts:  req.Artifacts,
		Status:     req.Status,
	})
	if err != nil {
		return nil, mapCompletionError(err)
	}
	// A writer that persisted a different item than the one the validator
	// verified is a contract violation; never turn it into a completion signal.
	if outcome.PlanItemID != verification.PlanItemID {
		return nil, fmt.Errorf("task.complete writer wrote plan item %d but the validator verified target %d",
			outcome.PlanItemID, verification.PlanItemID)
	}
	result := &TaskCompleteResult{
		Completed:        true,
		Status:           req.Status,
		PlanItemID:       idString(verification.PlanItemID),
		PlanStatus:       outcome.PlanStatus,
		PlanItemRevision: outcome.PlanItemRevision,
	}
	if outcome.FactID > 0 {
		result.FactID = idString(outcome.FactID)
	}
	return result, nil
}

// normalizeCompletionRequest validates the declaration fields. Every argument
// failure is a retryable invalid_argument: the model can correct it.
func normalizeCompletionRequest(scope Scope, params taskCompleteParams) (CompletionRequest, *Error) {
	summary := strings.TrimSpace(params.Summary)
	if err := validation.String("summary", summary, 1, maxCompletionSummaryRunes); err != nil {
		toolErr := invalidFromPlatform(err)
		toolErr.Retryable = true
		return CompletionRequest{}, toolErr
	}
	status := CompletionStatusCompleted
	if params.Status != nil {
		status = strings.ToLower(strings.TrimSpace(*params.Status))
		if status == "" {
			status = CompletionStatusCompleted
		}
	}
	if status != CompletionStatusCompleted {
		err := completionInvalid("status", `must be "completed": task.complete only signals a successful completion`)
		err.Details["allowed"] = []string{CompletionStatusCompleted}
		return CompletionRequest{}, err
	}
	artifacts, toolErr := normalizeCompletionArtifacts(params.Artifacts)
	if toolErr != nil {
		return CompletionRequest{}, toolErr
	}
	return CompletionRequest{
		Scope:     scope,
		Summary:   summary,
		Artifacts: artifacts,
		Status:    status,
	}, nil
}

// normalizeCompletionArtifacts trims and deduplicates artifact references while
// preserving order. Empty or oversized entries are argument errors, not silent
// drops: the model must send concrete references.
func normalizeCompletionArtifacts(values []string) ([]string, *Error) {
	if len(values) > maxCompletionArtifacts {
		return nil, completionInvalid("artifacts",
			fmt.Sprintf("must contain at most %d items", maxCompletionArtifacts))
	}
	artifacts := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, raw := range values {
		artifact := strings.TrimSpace(raw)
		if artifact == "" {
			return nil, completionInvalid(fmt.Sprintf("artifacts[%d]", index),
				"must not be empty; remove the entry or send a concrete artifact reference")
		}
		if utf8.RuneCountInString(artifact) > maxCompletionArtifactRunes {
			return nil, completionInvalid(fmt.Sprintf("artifacts[%d]", index),
				fmt.Sprintf("must be at most %d characters", maxCompletionArtifactRunes))
		}
		if _, duplicate := seen[artifact]; duplicate {
			continue
		}
		seen[artifact] = struct{}{}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, nil
}

// completionInvalid builds the retryable argument error of this tool.
func completionInvalid(field, message string) *Error {
	err := Invalid(field, message)
	err.Retryable = true
	return err
}

// mapCompletionError turns validation rejections into the model-visible
// retryable invalid_argument error and maps classified port failures. Anything
// unexpected stays internal and fails the run.
func mapCompletionError(err error) error {
	if err == nil {
		return nil
	}
	var toolErr *Error
	if errors.As(err, &toolErr) {
		return toolErr
	}
	var completionErr *CompletionError
	if errors.As(err, &completionErr) {
		// Copy the validator's details and add the hint there too: the message
		// already carries it, but every structured rejection must expose the
		// same field/hint contract to the model.
		details := make(map[string]any, len(completionErr.Details)+1)
		for key, value := range completionErr.Details {
			details[key] = value
		}
		if completionErr.Hint != "" {
			details["hint"] = completionErr.Hint
		}
		return &Error{
			Code:      CodeInvalidArgument,
			Message:   completionErr.Error(),
			Retryable: true,
			Details:   details,
		}
	}
	if errors.Is(err, ErrMissingArtifacts) {
		return &Error{
			Code:      CodeInvalidArgument,
			Message:   "completion rejected: artifacts is empty but this task requires deliverables; include at least one concrete artifact reference and retry",
			Retryable: true,
			Details: map[string]any{
				"field": "artifacts",
				"hint":  "include at least one concrete artifact reference (for example extraction:<run_id>) and retry",
			},
		}
	}
	if errors.Is(err, ErrNotComplete) {
		return &Error{
			Code:      CodeInvalidArgument,
			Message:   "completion rejected: the task's completion criteria are not met; finish the required work and retry",
			Retryable: true,
			Details: map[string]any{
				"field": "completion",
				"hint":  "finish the missing deliverable or state transition, then retry",
			},
		}
	}
	return mapPortError(err)
}

// --- extraction validator ---------------------------------------------------

// verifyCompletionTarget turns the run's bound plan item into the single write
// target of a passing completion. A missing target is a wiring failure, not a
// model-fixable rejection, so it fails the run.
func verifyCompletionTarget(kind CompletionTaskKind, planItemID int64) (CompletionVerification, error) {
	if planItemID <= 0 {
		return CompletionVerification{}, fmt.Errorf("%s completion requires a positive target plan item ID", kind)
	}
	return CompletionVerification{PlanItemID: planItemID}, nil
}

// ExtractionReconciliation is the server-read state of a version's requirement
// extraction run. Expected is the declared/derived requirement count and
// Extracted is the reconciled count (R-A7-8).
type ExtractionReconciliation struct {
	RunID     int64
	Done      bool
	Expected  int
	Extracted int
}

// ExtractionReader reads the extraction run and its reconciliation for a
// version. The version-context adapter (Task 36) implements it; tests use a
// fake. Business failures must be *Error or *platform.Error.
type ExtractionReader interface {
	ExtractionDone(ctx context.Context, versionID int64) (ExtractionReconciliation, error)
}

// ExtractionCompletionValidator validates the extraction completion criterion
// (R-A3-2): the run is done and expected == extracted. It also carries the
// run's target plan item into the verified write target.
type ExtractionCompletionValidator struct {
	runs       ExtractionReader
	planItemID int64
}

// NewExtractionCompletionValidator builds the extraction validator for one run
// target. planItemID is the work item's plan item that completion writes; it is
// runtime data and never model-supplied.
func NewExtractionCompletionValidator(runs ExtractionReader, planItemID int64) *ExtractionCompletionValidator {
	return &ExtractionCompletionValidator{runs: runs, planItemID: planItemID}
}

var _ Validator = (*ExtractionCompletionValidator)(nil)

// Validate implements Validator.
func (v *ExtractionCompletionValidator) Validate(ctx context.Context, req CompletionRequest) (CompletionVerification, error) {
	if v == nil || v.runs == nil {
		return CompletionVerification{}, missingDep("extraction completion reader")
	}
	verification, err := verifyCompletionTarget(CompletionKindExtraction, v.planItemID)
	if err != nil {
		return CompletionVerification{}, err
	}
	if err := RequireArtifacts(req, CompletionKindExtraction, "extraction:<run_id>"); err != nil {
		return CompletionVerification{}, err
	}
	state, err := v.runs.ExtractionDone(ctx, req.Scope.VersionID)
	if err != nil {
		return CompletionVerification{}, err
	}
	if !state.Done {
		return CompletionVerification{}, NewExtractionNotDoneError(state)
	}
	if state.Expected != state.Extracted {
		return CompletionVerification{}, NewExtractionMismatchError(state)
	}
	return verification, nil
}

// --- plan-item validator ----------------------------------------------------

// PlanItemSnapshot is the completion view of one version plan item.
type PlanItemSnapshot struct {
	ID        int64
	VersionID int64
	Status    string
	Decision  string
	Revision  int
}

// PlanItemReader reads one plan item for the plan-item completion criterion.
// The version-context adapter (Task 36) implements it over the plan read path;
// tests use a fake.
type PlanItemReader interface {
	GetPlanItem(ctx context.Context, versionID, itemID int64) (PlanItemSnapshot, error)
}

// PlanItemCompletionValidator validates the plan-item completion criterion
// (R-A3-2): the target item is done and a decision is recorded.
type PlanItemCompletionValidator struct {
	items  PlanItemReader
	itemID int64
}

// NewPlanItemCompletionValidator builds the validator for one target plan item.
func NewPlanItemCompletionValidator(items PlanItemReader, itemID int64) *PlanItemCompletionValidator {
	return &PlanItemCompletionValidator{items: items, itemID: itemID}
}

var _ Validator = (*PlanItemCompletionValidator)(nil)

// Validate implements Validator. It re-reads the target item and returns its ID
// as the single verified write target.
func (v *PlanItemCompletionValidator) Validate(ctx context.Context, req CompletionRequest) (CompletionVerification, error) {
	if v == nil || v.items == nil {
		return CompletionVerification{}, missingDep("plan item completion reader")
	}
	verification, err := verifyCompletionTarget(CompletionKindPlanItem, v.itemID)
	if err != nil {
		return CompletionVerification{}, err
	}
	item, err := v.items.GetPlanItem(ctx, req.Scope.VersionID, v.itemID)
	if err != nil {
		return CompletionVerification{}, err
	}
	if item.ID > 0 && item.ID != v.itemID {
		return CompletionVerification{}, fmt.Errorf("plan item reader returned item %d for target %d", item.ID, v.itemID)
	}
	if !planItemDone(item.Status) {
		return CompletionVerification{}, NewPlanItemNotDoneError(v.itemID, item.Status)
	}
	if strings.TrimSpace(item.Decision) == "" {
		return CompletionVerification{}, NewPlanItemMissingDecisionError(v.itemID)
	}
	return verification, nil
}

// planItemDone reports the terminal done statuses of version_plan_item.
func planItemDone(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "done", "completed":
		return true
	default:
		return false
	}
}

// --- design validator -------------------------------------------------------

// DesignRevisionReader reports whether a version has a case revision/review
// artifact. The design context adapter lands with Task 36 wiring; the validator
// fails closed until it is provided.
type DesignRevisionReader interface {
	DesignRevisionExists(ctx context.Context, versionID int64) (bool, error)
}

// DesignCompletionValidator validates the design completion criterion (R-A3-2):
// a revision/review artifact exists. It requires delivered artifacts, carries
// the run's target plan item into the write target, and fails closed on an
// unwired reader as an internal error.
type DesignCompletionValidator struct {
	revisions  DesignRevisionReader
	planItemID int64
}

// NewDesignCompletionValidator builds the design validator for one run target.
// planItemID is the work item's plan item that completion writes. A nil reader
// is a wiring failure: Validate returns the internal missing-dependency error
// instead of a model-retryable rejection.
func NewDesignCompletionValidator(revisions DesignRevisionReader, planItemID int64) *DesignCompletionValidator {
	return &DesignCompletionValidator{revisions: revisions, planItemID: planItemID}
}

var _ Validator = (*DesignCompletionValidator)(nil)

// Validate implements Validator.
func (v *DesignCompletionValidator) Validate(ctx context.Context, req CompletionRequest) (CompletionVerification, error) {
	if v == nil || v.revisions == nil {
		return CompletionVerification{}, missingDep("design revision completion reader")
	}
	verification, err := verifyCompletionTarget(CompletionKindDesign, v.planItemID)
	if err != nil {
		return CompletionVerification{}, err
	}
	if err := RequireArtifacts(req, CompletionKindDesign,
		"design:<case_version_id>", "review:<case_version_id>"); err != nil {
		return CompletionVerification{}, err
	}
	exists, err := v.revisions.DesignRevisionExists(ctx, req.Scope.VersionID)
	if err != nil {
		return CompletionVerification{}, err
	}
	if !exists {
		return CompletionVerification{}, NewNotCompleteError(
			"design revision/review artifact is missing",
			"produce a case revision (and its review) before completing the design task",
			map[string]any{"field": "design_revision"},
		)
	}
	return verification, nil
}

// --- host validator ---------------------------------------------------------

// HostTurnReader reports whether the current Host turn's question was answered
// or handed off. The human-request adapter lands with Task 23/36 wiring; the
// validator fails closed until it is provided.
type HostTurnReader interface {
	TurnAnswered(ctx context.Context, scope Scope) (bool, error)
}

// HostCompletionValidator validates the Host completion criterion (R-A3-2): the
// current turn's question was answered or handed off. It carries the run's
// target plan item into the write target and fails closed on an unwired reader
// as an internal error.
type HostCompletionValidator struct {
	turns      HostTurnReader
	planItemID int64
}

// NewHostCompletionValidator builds the Host validator for one run target.
// planItemID is the work item's plan item that completion writes. A nil reader
// is a wiring failure: Validate returns the internal missing-dependency error
// instead of a model-retryable rejection.
func NewHostCompletionValidator(turns HostTurnReader, planItemID int64) *HostCompletionValidator {
	return &HostCompletionValidator{turns: turns, planItemID: planItemID}
}

var _ Validator = (*HostCompletionValidator)(nil)

// Validate implements Validator.
func (v *HostCompletionValidator) Validate(ctx context.Context, req CompletionRequest) (CompletionVerification, error) {
	if v == nil || v.turns == nil {
		return CompletionVerification{}, missingDep("host turn completion reader")
	}
	verification, err := verifyCompletionTarget(CompletionKindHost, v.planItemID)
	if err != nil {
		return CompletionVerification{}, err
	}
	answered, err := v.turns.TurnAnswered(ctx, req.Scope)
	if err != nil {
		return CompletionVerification{}, err
	}
	if !answered {
		return CompletionVerification{}, NewNotCompleteError(
			"the current turn's question is neither answered nor handed off",
			"answer the question or hand it off, then retry task.complete",
			map[string]any{"field": "host_turn"},
		)
	}
	return verification, nil
}

// --- registry hook ----------------------------------------------------------

// TaskCompleteTools returns the W0 task.complete tool group. The composition
// root registers it through the existing tool-group registration list:
//
//	tools.RegisterP4ToolGroup(tools.TaskCompleteTools)
//
// A zero CompletionDeps returns no tool, so assemblies that do not wire the
// validator/writer never expose a half-wired completion tool.
func TaskCompleteTools(deps Deps) []Tool {
	if deps.Completion.Validator == nil || deps.Completion.Writer == nil {
		return nil
	}
	return []Tool{NewTaskCompleteTool(deps.Completion.Validator, deps.Completion.Writer)}
}
