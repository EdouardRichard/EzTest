package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// fakePlanWriter implements PlanWriter for the plan.update tool tests. Every
// call is recorded so the test can assert the injected version scope, the
// addressed item and the optimistic-lock revision that reached the port.
type fakePlanWriter struct {
	updated   bool
	versionID int64
	itemID    int64
	patch     PlanPatch
	revision  int
	item      PlanItem
	err       error
}

func (f *fakePlanWriter) Update(_ context.Context, versionID, itemID int64, patch PlanPatch, revision int) (PlanItem, error) {
	f.updated = true
	f.versionID = versionID
	f.itemID = itemID
	f.patch = patch
	f.revision = revision
	if f.err != nil {
		return PlanItem{}, f.err
	}
	if f.item.ID != 0 {
		return f.item, nil
	}
	return PlanItem{
		ID:        itemID,
		VersionID: versionID,
		Phase:     "clarifying",
		Title:     "Clarify requirements",
		Status:    patch.Status,
		Decision:  patch.Decision,
		Order:     1,
		Revision:  revision + 1,
	}, nil
}

var _ PlanWriter = (*fakePlanWriter)(nil)

// TestPlanUpdateToolWrites is the task's contract test: the tool addresses the
// plan item with the injected version scope, forwards the revision-guarded patch
// and returns the stored item including its new revision.
func TestPlanUpdateToolWrites(t *testing.T) {
	plan := &fakePlanWriter{}
	tool := NewPlanUpdateTool(plan)
	value, err := tool.Execute(t.Context(), Scope{VersionID: 7}, json.RawMessage(
		`{"item_id":"9","status":"done","revision":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.updated || plan.versionID != 7 || plan.itemID != 9 {
		t.Fatalf("plan=%+v", plan)
	}
	if plan.patch.Status != "done" || plan.revision != 1 {
		t.Fatalf("patch=%+v revision=%d", plan.patch, plan.revision)
	}
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want an object", value)
	}
	if result["id"] != "9" || result["version_id"] != "7" {
		t.Fatalf("result ids = %#v", result)
	}
	if result["status"] != "done" || result["revision"] != 2 {
		t.Fatalf("result item = %#v, want the stored status and revision 2", result)
	}
}

// TestPlanUpdateToolAcceptsDecimalStringAndNumberIDs pins the transport shape:
// identifiers use the platform's decimal-string convention, but the tool also
// accepts the plain JSON numbers a model may emit (the revision in particular is
// not an ID and models write it as an integer).
func TestPlanUpdateToolAcceptsDecimalStringAndNumberIDs(t *testing.T) {
	plan := &fakePlanWriter{}
	tool := NewPlanUpdateTool(plan)
	_, err := tool.Execute(t.Context(), Scope{VersionID: 7}, json.RawMessage(
		`{"item_id":9,"revision":"2","title":"Refined","decision":"approved","order":0}`))
	if err != nil {
		t.Fatal(err)
	}
	if plan.itemID != 9 || plan.revision != 2 {
		t.Fatalf("item/revision = %d/%d, want 9/2", plan.itemID, plan.revision)
	}
	if plan.patch.Title != "Refined" || plan.patch.Decision != "approved" {
		t.Fatalf("patch = %+v", plan.patch)
	}
	if plan.patch.Order == nil || *plan.patch.Order != 0 {
		t.Fatalf("order = %v, want the explicit 0 pointer", plan.patch.Order)
	}
}

// TestPlanUpdateToolRejectsInvalidInput keeps malformed model input a typed,
// retryable invalid_argument with the offending field instead of an internal
// failure or a store call.
func TestPlanUpdateToolRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		field   string
	}{
		{"missing item id", `{"revision":1}`, "item_id"},
		{"zero item id", `{"item_id":"0","revision":1}`, "item_id"},
		{"non numeric item id", `{"item_id":"abc","revision":1}`, "item_id"},
		{"missing revision", `{"item_id":"9"}`, "revision"},
		{"zero revision", `{"item_id":"9","revision":0}`, "revision"},
		{"negative order", `{"item_id":"9","revision":1,"order":-1}`, "order"},
		{"unknown scope field", `{"item_id":"9","revision":1,"version_id":"7"}`, "parameters"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := &fakePlanWriter{}
			_, err := NewPlanUpdateTool(plan).Execute(t.Context(), Scope{VersionID: 7}, json.RawMessage(test.payload))
			toolErr := asToolError(t, err)
			if toolErr.Code != CodeInvalidArgument {
				t.Fatalf("code = %q, want %q (err %v)", toolErr.Code, CodeInvalidArgument, err)
			}
			if !toolErr.Retryable {
				t.Fatalf("field error must be retryable: %+v", toolErr)
			}
			if got := toolErr.Details["field"]; got != test.field {
				t.Fatalf("details.field = %#v, want %q (details %#v)", got, test.field, toolErr.Details)
			}
			if plan.updated {
				t.Fatalf("invalid input reached the plan writer: %+v", plan)
			}
		})
	}
}

// TestPlanUpdateToolRequiresVersionScope pins that the version always comes
// from the runtime-injected scope, never from the model.
func TestPlanUpdateToolRequiresVersionScope(t *testing.T) {
	plan := &fakePlanWriter{}
	_, err := NewPlanUpdateTool(plan).Execute(t.Context(), Scope{}, json.RawMessage(
		`{"item_id":"9","revision":1}`))
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeInvalidArgument || toolErr.Details["field"] != "scope" || !toolErr.Retryable {
		t.Fatalf("tool error = %+v", toolErr)
	}
	if plan.updated {
		t.Fatalf("missing scope reached the plan writer: %+v", plan)
	}
}

// TestPlanUpdateToolMapsRevisionConflict pins the stale-revision mapping: a
// non-retryable conflict carrying the expected and stored revisions so the model
// can re-read and retry instead of blindly repeating the write.
func TestPlanUpdateToolMapsRevisionConflict(t *testing.T) {
	plan := &fakePlanWriter{err: &PlanRevisionConflict{ItemID: 9, Expected: 1, Actual: 3}}
	_, err := NewPlanUpdateTool(plan).Execute(t.Context(), Scope{VersionID: 7}, json.RawMessage(
		`{"item_id":"9","status":"done","revision":1}`))
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeConflict {
		t.Fatalf("code = %q, want %q (err %v)", toolErr.Code, CodeConflict, err)
	}
	if toolErr.Retryable {
		t.Fatalf("revision conflict must not be blindly retryable: %+v", toolErr)
	}
	if toolErr.Details["expected_revision"] != 1 || toolErr.Details["actual_revision"] != 3 {
		t.Fatalf("conflict details = %#v", toolErr.Details)
	}
	if toolErr.Details["item_id"] != "9" {
		t.Fatalf("conflict item = %#v", toolErr.Details["item_id"])
	}
}

// TestPlanUpdateToolMapsPortErrors pins the rest of the port error contract:
// not found is a non-retryable not_found, and an adapter that forwards the
// service's typed conflict as a platform 409 still becomes the plan conflict.
func TestPlanUpdateToolMapsPortErrors(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		plan := &fakePlanWriter{err: platform.NotFound("plan item")}
		toolErr := executePlanUpdate(t, plan, `{"item_id":"9","revision":1}`)
		if toolErr.Code != CodeNotFound || toolErr.Retryable {
			t.Fatalf("tool error = %+v", toolErr)
		}
		if toolErr.Details["kind"] != "plan item" {
			t.Fatalf("details = %#v", toolErr.Details)
		}
	})
	t.Run("platform conflict", func(t *testing.T) {
		plan := &fakePlanWriter{err: platform.Conflict("plan item revision conflict")}
		toolErr := executePlanUpdate(t, plan, `{"item_id":"9","revision":1}`)
		if toolErr.Code != CodeConflict || toolErr.Retryable {
			t.Fatalf("tool error = %+v", toolErr)
		}
	})
	t.Run("internal failure stays internal", func(t *testing.T) {
		plan := &fakePlanWriter{err: context.DeadlineExceeded}
		_, err := NewPlanUpdateTool(plan).Execute(t.Context(), Scope{VersionID: 7}, json.RawMessage(
			`{"item_id":"9","revision":1}`))
		var toolErr *Error
		if err == nil || errorsAs(err, &toolErr) {
			t.Fatalf("internal failure must not become a tool error: %v", err)
		}
	})
}

// TestPlanUpdateToolSchemaHidesScope pins the tool schema contract shared by the
// P1 tools: the injected version scope is never a model-supplied parameter.
func TestPlanUpdateToolSchemaHidesScope(t *testing.T) {
	tool := NewPlanUpdateTool(nil)
	if tool.Name() != ToolPlanUpdate {
		t.Fatalf("name = %q, want %q", tool.Name(), ToolPlanUpdate)
	}
	raw := string(tool.Parameters())
	if strings.Contains(raw, "version_id") {
		t.Fatalf("schema leaks the injected version scope: %s", raw)
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.Parameters(), &schema); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Fatalf("schema shape = %#v", schema)
	}
	required, _ := schema["required"].([]any)
	found := map[string]bool{}
	for _, value := range required {
		if name, ok := value.(string); ok {
			found[name] = true
		}
	}
	if !found["item_id"] || !found["revision"] {
		t.Fatalf("required = %#v, want item_id and revision", required)
	}
	if tool.Description() == "" {
		t.Fatal("tool has no description")
	}
}

func executePlanUpdate(t *testing.T, plan PlanWriter, payload string) *Error {
	t.Helper()
	_, err := NewPlanUpdateTool(plan).Execute(t.Context(), Scope{VersionID: 7}, json.RawMessage(payload))
	return asToolError(t, err)
}
