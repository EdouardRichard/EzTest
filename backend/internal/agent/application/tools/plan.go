package tools

// Host plan write tool (spec R-A8-2/API-4, docs/version-V0.22/B §9.1).
//
// PlanUpdateTool is the model-facing half of the plan write path; the other half
// is PATCH /api/versions/{id}/plan-items/{itemId}. Both write through
// versionapp.PlanService.Update and keep version_plan_item the single source of
// truth. The tool receives version_id from the runtime-injected Scope, never
// from the model, and requires the revision the model last read so concurrent
// writers cannot overwrite each other.
//
// Wiring: this task deliberately does not register the tool. The Host profiles
// (clarification_host/design_host) and the version-context PlanWriter adapter are
// owned by the runner/composition-root task (Task 36), which must:
//
//   - implement PlanWriter on top of *versionapp.PlanService.Update (mapping
//     versionapp.ConflictError to *PlanRevisionConflict, and letting
//     platform.NotFound/platform.Invalid pass through), and
//   - add NewPlanUpdateTool(adapter) to the Host registry and allow
//     tools.ToolPlanUpdate in the two Host profile tool lists.
//
// plan.update is not part of the frozen P1-P7 registry sets, so nothing here
// exposes it to a profile that has not explicitly allowed it.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// Plan tool names and error codes.
const (
	// ToolPlanUpdate is the Host plan write tool (spec R-A8-2).
	ToolPlanUpdate = "plan.update"
	// CodeConflict is the 409-like plan write conflict code: the presented
	// revision is stale. The model must re-read the plan item before retrying.
	CodeConflict = "conflict"
)

// PlanPatch is one partial plan-item write (spec R-A8-2). Empty Title/Status/
// Decision mean "keep the stored value"; a nil Order keeps the stored sort order.
// Phase, DependsOn and WorkItemID are not patchable.
type PlanPatch struct {
	Title    string
	Status   string
	Decision string
	Order    *int
}

// PlanItem is the stored plan item a PlanWriter returns. Revision is the
// optimistic-lock token bumped by every real write.
type PlanItem struct {
	ID         int64
	VersionID  int64
	Phase      string
	Title      string
	Status     string
	DependsOn  []int64
	WorkItemID *int64
	Decision   string
	Order      int
	Revision   int
}

// PlanRevisionConflict is the stale-revision failure a PlanWriter returns. The
// version-context adapter maps *versionapp.ConflictError onto this type because
// the tools package cannot import the version application (the version context
// already imports this package). Actual is 0 when the store only reported the
// generic sentinel and the stored revision is unknown.
type PlanRevisionConflict struct {
	ItemID   int64
	Expected int
	Actual   int
}

// Error implements error.
func (e *PlanRevisionConflict) Error() string {
	if e == nil {
		return "plan item revision conflict"
	}
	if e.Actual > 0 {
		return fmt.Sprintf("plan item %d revision conflict: expected %d, stored %d", e.ItemID, e.Expected, e.Actual)
	}
	return fmt.Sprintf("plan item %d revision conflict: expected %d", e.ItemID, e.Expected)
}

// PlanWriter is the minimal version plan write port of the plan.update tool. The
// production adapter wraps *versionapp.PlanService; tests use a fake. Business
// failures must be returned as *PlanRevisionConflict, *tools.Error or
// *platform.Error; unexpected failures stay plain errors and fail the run.
type PlanWriter interface {
	Update(ctx context.Context, versionID, itemID int64, patch PlanPatch, revision int) (PlanItem, error)
}

// PlanUpdateTool is the Host plan.update tool.
type PlanUpdateTool struct {
	plan PlanWriter
}

// NewPlanUpdateTool builds the plan.update tool around a plan writer.
func NewPlanUpdateTool(plan PlanWriter) *PlanUpdateTool {
	return &PlanUpdateTool{plan: plan}
}

var _ Tool = (*PlanUpdateTool)(nil)

// Name implements Tool.
func (t *PlanUpdateTool) Name() string { return ToolPlanUpdate }

// Description implements Tool.
func (t *PlanUpdateTool) Description() string {
	return "更新版本计划项（plan.update）：按 revision 乐观锁写入 title/status/decision/order 中的一项或多项，返回更新后的计划项（含新的 revision）。revision 必须是最近一次读到的值；冲突时重新读取计划项后再更新。"
}

// Parameters implements Tool. version_id is intentionally absent: it comes from
// the injected scope.
func (t *PlanUpdateTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"item_id": map[string]any{
			"type":        "string",
			"pattern":     `^[1-9][0-9]*$`,
			"description": "要更新的计划项 ID（十进制字符串，来自 briefing/plan 列表）。",
		},
		"title": map[string]any{
			"type":        "string",
			"description": "新的标题；省略或空字符串表示保留原值。",
		},
		"status": map[string]any{
			"type":        "string",
			"description": "新的状态（如 pending/in_progress/done）；省略或空字符串表示保留原值。",
		},
		"decision": map[string]any{
			"type":        "string",
			"description": "记录到该计划项的决策；省略或空字符串表示保留原值。",
		},
		"order": map[string]any{
			"type":        "integer",
			"minimum":     0,
			"description": "新的排序值；省略表示保留原排序。",
		},
		"revision": map[string]any{
			"type":        "integer",
			"minimum":     1,
			"description": "最近一次读到的计划项 revision（乐观锁）。过期会返回 conflict 及当前存储的 revision。",
		},
	}, "item_id", "revision")
}

// planUpdateParams accepts both the decimal-string and plain-number forms of the
// numeric arguments; the schema advertises the canonical form but models do send
// integers for a revision.
type planUpdateParams struct {
	ItemID   json.RawMessage `json:"item_id"`
	Title    *string         `json:"title"`
	Status   *string         `json:"status"`
	Decision *string         `json:"decision"`
	Order    *int            `json:"order"`
	Revision json.RawMessage `json:"revision"`
}

// Execute implements Tool.
func (t *PlanUpdateTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t == nil || t.plan == nil {
		return nil, missingDep("plan writer")
	}
	if scope.VersionID <= 0 {
		return nil, planInvalid("scope", "plan.update requires the injected version scope")
	}
	var params planUpdateParams
	if err := decodeParams(raw, &params); err != nil {
		err.Retryable = true
		return nil, err
	}
	itemID, itemErr := parsePlanPositiveInt("item_id", params.ItemID, "must be a positive decimal ID")
	if itemErr != nil {
		return nil, itemErr
	}
	revision, revisionErr := parsePlanPositiveInt("revision", params.Revision, "must be a positive integer")
	if revisionErr != nil {
		return nil, revisionErr
	}
	patch := PlanPatch{}
	if params.Title != nil {
		patch.Title = *params.Title
	}
	if params.Status != nil {
		patch.Status = *params.Status
	}
	if params.Decision != nil {
		patch.Decision = *params.Decision
	}
	if params.Order != nil {
		if *params.Order < 0 {
			return nil, planInvalid("order", "must not be negative")
		}
		order := *params.Order
		patch.Order = &order
	}
	stored, err := t.plan.Update(ctx, scope.VersionID, itemID, patch, int(revision))
	if err != nil {
		return nil, mapPlanWriteError(err)
	}
	return planItemResult(stored), nil
}

// planInvalid builds a retryable invalid_argument: the model can fix the field
// and call again.
func planInvalid(field, message string) *Error {
	err := Invalid(field, message)
	err.Retryable = true
	return err
}

// parsePlanPositiveInt accepts a JSON number or a decimal string and requires a
// positive integer.
func parsePlanPositiveInt(field string, raw json.RawMessage, message string) (int64, *Error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return 0, planInvalid(field, message)
	}
	text := string(trimmed)
	if trimmed[0] == '"' {
		var decoded string
		if err := json.Unmarshal(trimmed, &decoded); err != nil {
			return 0, planInvalid(field, message)
		}
		text = decoded
	}
	value, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	if err != nil || value <= 0 {
		return 0, planInvalid(field, message)
	}
	return value, nil
}

// mapPlanWriteError turns the plan port failures into model-visible tool errors.
// Unexpected failures stay internal and fail the run.
func mapPlanWriteError(err error) error {
	if err == nil {
		return nil
	}
	var toolErr *Error
	if errors.As(err, &toolErr) {
		return toolErr
	}
	var conflict *PlanRevisionConflict
	if errors.As(err, &conflict) {
		details := map[string]any{
			"item_id":           idString(conflict.ItemID),
			"expected_revision": conflict.Expected,
		}
		if conflict.Actual > 0 {
			details["actual_revision"] = conflict.Actual
		}
		return &Error{Code: CodeConflict, Message: conflict.Error(), Details: details}
	}
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case 404:
			return &Error{Code: CodeNotFound, Message: apiErr.Message, Details: apiErr.Details}
		case 409:
			// The plan write path has exactly one 409 cause: a stale revision.
			// An adapter may forward the conflict as a platform error when it
			// cannot expose the typed revision details.
			return &Error{Code: CodeConflict, Message: apiErr.Message, Details: apiErr.Details}
		case 400:
			return &Error{Code: CodeInvalidArgument, Message: apiErr.Message, Retryable: true, Details: apiErr.Details}
		}
	}
	return fmt.Errorf("plan.update: %w", err)
}

// planItemResult renders the stored plan item for the model. IDs are decimal
// strings per the platform's JSON convention.
func planItemResult(item PlanItem) map[string]any {
	dependsOn := make([]string, 0, len(item.DependsOn))
	for _, dependencyID := range item.DependsOn {
		dependsOn = append(dependsOn, idString(dependencyID))
	}
	result := map[string]any{
		"id":           idString(item.ID),
		"version_id":   idString(item.VersionID),
		"phase":        item.Phase,
		"title":        item.Title,
		"status":       item.Status,
		"decision":     item.Decision,
		"order":        item.Order,
		"revision":     item.Revision,
		"depends_on":   dependsOn,
		"work_item_id": nil,
	}
	if item.WorkItemID != nil {
		result["work_item_id"] = idString(*item.WorkItemID)
	}
	return result
}
