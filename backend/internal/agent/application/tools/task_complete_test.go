package tools

// Task 11 (v0.22 R-A3): task.complete is only a declaration; the server-side
// validators decide whether the run actually completed. These tests drive the
// tool, the validator ports and the completion-writer hand-off.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// --- fakes -----------------------------------------------------------------

type fakeCompletionValidator struct {
	requests []CompletionRequest
	// target is the verified plan item every passing validation reports. Zero
	// means the validator could not resolve the run's completion target; the
	// tool must reject that instead of falling back to a fact-only success.
	target int64
	err    error
}

func (f *fakeCompletionValidator) Validate(_ context.Context, req CompletionRequest) (CompletionVerification, error) {
	f.requests = append(f.requests, req)
	if f.err != nil {
		return CompletionVerification{}, f.err
	}
	return CompletionVerification{PlanItemID: f.target}, nil
}

type fakeCompletionWriter struct {
	commits []CompletionCommit
	outcome CompletionOutcome
	err     error
}

func (f *fakeCompletionWriter) Commit(_ context.Context, commit CompletionCommit) (CompletionOutcome, error) {
	f.commits = append(f.commits, commit)
	if f.err != nil {
		return CompletionOutcome{}, f.err
	}
	return f.outcome, nil
}

func executeTaskComplete(t *testing.T, tool *TaskCompleteTool, scope Scope, payload string) (any, error) {
	t.Helper()
	return tool.Execute(context.Background(), scope, json.RawMessage(payload))
}

// testJSON renders a value for the evidence log without changing assertions.
func testJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode evidence value: %v", err)
	}
	return string(encoded)
}

// --- tool argument and rejection behavior ----------------------------------

func TestTaskCompleteRejectsWithoutArtifacts(t *testing.T) {
	validator := &fakeCompletionValidator{err: ErrMissingArtifacts}
	writer := &fakeCompletionWriter{}
	tool := NewTaskCompleteTool(validator, writer)

	_, err := executeTaskComplete(t, tool, Scope{VersionID: 7},
		`{"summary":"done","status":"completed"}`)

	toolErr := asToolError(t, err)
	if toolErr.Code != CodeInvalidArgument {
		t.Fatalf("code = %q, want %q", toolErr.Code, CodeInvalidArgument)
	}
	if !toolErr.Retryable {
		t.Fatal("a missing-artifacts rejection must be retryable")
	}
	if got := toolErr.Details["field"]; got != "artifacts" {
		t.Fatalf("details.field = %#v, want artifacts", got)
	}
	if !strings.Contains(strings.ToLower(toolErr.Message), "artifact") {
		t.Fatalf("message does not name the missing artifact: %q", toolErr.Message)
	}
	if len(writer.commits) != 0 {
		t.Fatalf("writer was called on a rejected completion: %+v", writer.commits)
	}
	if len(validator.requests) != 1 {
		t.Fatalf("validator calls = %d, want 1", len(validator.requests))
	}
	if got := validator.requests[0].Status; got != "completed" {
		t.Fatalf("validator status = %q, want completed default", got)
	}
	t.Logf("reject payload: %s", testJSON(t, toolErr.Payload()))
}

func TestTaskCompleteAcceptsValidExtraction(t *testing.T) {
	validator := &fakeCompletionValidator{target: 9}
	writer := &fakeCompletionWriter{outcome: CompletionOutcome{
		PlanItemID:       9,
		PlanStatus:       "done",
		Decision:         "58/58",
		FactID:           77,
		FactKey:          "completion:42",
		PlanItemRevision: 3,
	}}
	tool := NewTaskCompleteTool(validator, writer)

	value, err := executeTaskComplete(t, tool, Scope{VersionID: 7, AgentRunID: 42},
		`{"summary":"58/58","artifacts":["extraction:1"],"status":"completed"}`)
	if err != nil {
		t.Fatalf("task.complete rejected a valid extraction: %v", err)
	}
	result, ok := value.(*TaskCompleteResult)
	if !ok {
		t.Fatalf("result type = %T, want *TaskCompleteResult", value)
	}
	if !result.Completed || !result.SignalsCompletion() {
		t.Fatalf("result does not signal completion: %+v", result)
	}
	if len(writer.commits) != 1 {
		t.Fatalf("writer calls = %d, want 1", len(writer.commits))
	}
	commit := writer.commits[0]
	if commit.Scope.VersionID != 7 || commit.Scope.AgentRunID != 42 {
		t.Fatalf("commit scope = %+v", commit.Scope)
	}
	if commit.Summary != "58/58" || commit.Status != "completed" {
		t.Fatalf("commit = %+v", commit)
	}
	if len(commit.Artifacts) != 1 || commit.Artifacts[0] != "extraction:1" {
		t.Fatalf("commit artifacts = %v", commit.Artifacts)
	}
	if commit.PlanItemID != validator.target {
		t.Fatalf("commit target = %d, want the target the validator verified (%d)",
			commit.PlanItemID, validator.target)
	}
	if result.PlanItemID != "9" {
		t.Fatalf("result plan_item_id = %q, want 9", result.PlanItemID)
	}
	if result.PlanItemRevision != 3 {
		t.Fatalf("result plan_item_revision = %d, want the store revision 3", result.PlanItemRevision)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("result is not JSON-encodable: %v", err)
	}
	if !strings.Contains(string(encoded), `"completed":true`) {
		t.Fatalf("model-visible result lacks completed=true: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"plan_item_revision":3`) {
		t.Fatalf("model-visible/result hand-off lacks the new plan revision: %s", encoded)
	}
	t.Logf("accept result: %s (commit target=%d summary=%q artifacts=%v status=%q)",
		encoded, commit.PlanItemID, commit.Summary, commit.Artifacts, commit.Status)
}

func TestTaskCompleteDefaultsStatusToCompleted(t *testing.T) {
	validator := &fakeCompletionValidator{target: 9}
	writer := &fakeCompletionWriter{outcome: CompletionOutcome{PlanItemID: 9, PlanStatus: "done"}}
	tool := NewTaskCompleteTool(validator, writer)

	if _, err := executeTaskComplete(t, tool, Scope{VersionID: 7},
		`{"summary":"done","artifacts":["extraction:1"]}`); err != nil {
		t.Fatalf("task.complete without status: %v", err)
	}
	if len(writer.commits) != 1 || writer.commits[0].Status != "completed" {
		t.Fatalf("commit status = %+v, want completed default", writer.commits)
	}
	if len(validator.requests) != 1 || validator.requests[0].Status != "completed" {
		t.Fatalf("validator status = %+v", validator.requests)
	}
}

func TestTaskCompleteRejectsUnsupportedStatus(t *testing.T) {
	validator := &fakeCompletionValidator{}
	writer := &fakeCompletionWriter{}
	tool := NewTaskCompleteTool(validator, writer)

	_, err := executeTaskComplete(t, tool, Scope{VersionID: 7},
		`{"summary":"done","artifacts":["extraction:1"],"status":"failed"}`)
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeInvalidArgument || !toolErr.Retryable {
		t.Fatalf("task error = %+v, want retryable invalid_argument", toolErr)
	}
	allowed, ok := toolErr.Details["allowed"].([]string)
	if !ok || len(allowed) != 1 || allowed[0] != "completed" {
		t.Fatalf("details.allowed = %#v, want [completed]", toolErr.Details["allowed"])
	}
	if len(validator.requests) != 0 || len(writer.commits) != 0 {
		t.Fatalf("validator/writer called on invalid status: %v/%v", validator.requests, writer.commits)
	}
}

func TestTaskCompleteRejectsEmptySummary(t *testing.T) {
	tool := NewTaskCompleteTool(&fakeCompletionValidator{}, &fakeCompletionWriter{})
	_, err := executeTaskComplete(t, tool, Scope{VersionID: 7}, `{"summary":"   "}`)
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeInvalidArgument || !toolErr.Retryable {
		t.Fatalf("task error = %+v, want retryable invalid_argument", toolErr)
	}
	if field := toolErr.Details["field"]; field != "summary" {
		t.Fatalf("details.field = %#v, want summary", field)
	}
}

func TestTaskCompleteRequiresVersionScope(t *testing.T) {
	tool := NewTaskCompleteTool(&fakeCompletionValidator{}, &fakeCompletionWriter{})
	_, err := executeTaskComplete(t, tool, Scope{}, `{"summary":"done","artifacts":["x"]}`)
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeInvalidArgument {
		t.Fatalf("code = %q, want invalid_argument", toolErr.Code)
	}
}

// TestTaskCompleteRejectsFactOnlyCompletion pins the removed bypass: a validator
// that passes without naming a plan item target must not produce a Completed
// signal (there is no fact-only success path for task.complete).
func TestTaskCompleteRejectsFactOnlyCompletion(t *testing.T) {
	validator := &fakeCompletionValidator{} // passes, but resolves no target
	writer := &fakeCompletionWriter{}
	tool := NewTaskCompleteTool(validator, writer)

	value, err := executeTaskComplete(t, tool, Scope{VersionID: 7, AgentRunID: 42},
		`{"summary":"requirements drafted","artifacts":["draft:5"]}`)
	if err == nil {
		t.Fatalf("fact-only completion was accepted: %+v", value)
	}
	if value != nil {
		t.Fatalf("fact-only completion returned a result instead of failing: %+v", value)
	}
	var toolErr *Error
	if errors.As(err, &toolErr) {
		t.Fatalf("missing target wiring must be an internal (non-retryable) error, got %+v", toolErr)
	}
	if len(writer.commits) != 0 {
		t.Fatalf("writer was called without a verified target: %+v", writer.commits)
	}
	if len(validator.requests) != 1 {
		t.Fatalf("validator calls = %d, want 1", len(validator.requests))
	}
}

// TestTaskCompleteRejectsMismatchedWriterTarget pins that a writer reporting a
// different plan item than the validator verified cannot silently complete.
func TestTaskCompleteRejectsMismatchedWriterTarget(t *testing.T) {
	validator := &fakeCompletionValidator{target: 9}
	writer := &fakeCompletionWriter{outcome: CompletionOutcome{PlanItemID: 10, PlanStatus: "done", FactID: 77}}
	tool := NewTaskCompleteTool(validator, writer)

	value, err := executeTaskComplete(t, tool, Scope{VersionID: 7, AgentRunID: 42},
		`{"summary":"done","artifacts":["extraction:1"]}`)
	if err == nil {
		t.Fatalf("writer that wrote plan item 10 for verified target 9 succeeded: %+v", value)
	}
	if value != nil {
		t.Fatalf("mismatched write returned a result instead of failing: %+v", value)
	}
	var toolErr *Error
	if errors.As(err, &toolErr) {
		t.Fatalf("target mismatch must be an internal error, got %+v", toolErr)
	}
	if len(writer.commits) != 1 || writer.commits[0].PlanItemID != 9 {
		t.Fatalf("writer commits = %+v, want one commit for the verified target 9", writer.commits)
	}
}

// TestTaskCompleteWriterNotCalledOnCompletionError pins that every structured
// validator rejection (not just the ErrMissingArtifacts sentinel) stops before
// the write, and that the hint survives into the structured details.
func TestTaskCompleteWriterNotCalledOnCompletionError(t *testing.T) {
	validator := &fakeCompletionValidator{
		target: 9,
		err: NewNotCompleteError(
			"plan item 9 is pending",
			"finish the plan item and retry",
			map[string]any{"field": "plan_item", "item_id": "9"},
		),
	}
	writer := &fakeCompletionWriter{}
	tool := NewTaskCompleteTool(validator, writer)

	_, err := executeTaskComplete(t, tool, Scope{VersionID: 7, AgentRunID: 42},
		`{"summary":"done","artifacts":["plan_item:9"]}`)
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeInvalidArgument || !toolErr.Retryable {
		t.Fatalf("task error = %+v, want retryable invalid_argument", toolErr)
	}
	if got := toolErr.Details["field"]; got != "plan_item" {
		t.Fatalf("details.field = %#v, want plan_item", got)
	}
	if got := toolErr.Details["hint"]; got != "finish the plan item and retry" {
		t.Fatalf("details.hint = %#v, want the validator hint", got)
	}
	if len(writer.commits) != 0 {
		t.Fatalf("writer was called on a *CompletionError rejection: %+v", writer.commits)
	}
}

// TestTaskCompleteRejectsModelSuppliedScopeIDs pins the strict decode: the
// model can never inject a version or plan item id through the arguments.
func TestTaskCompleteRejectsModelSuppliedScopeIDs(t *testing.T) {
	payloads := map[string]string{
		"version_id":   `{"summary":"done","artifacts":["extraction:1"],"version_id":9}`,
		"plan_item_id": `{"summary":"done","artifacts":["extraction:1"],"plan_item_id":"9"}`,
	}
	for name, payload := range payloads {
		t.Run(name, func(t *testing.T) {
			validator := &fakeCompletionValidator{target: 9}
			writer := &fakeCompletionWriter{}
			tool := NewTaskCompleteTool(validator, writer)

			_, err := executeTaskComplete(t, tool, Scope{VersionID: 7, AgentRunID: 42}, payload)
			toolErr := asToolError(t, err)
			if toolErr.Code != CodeInvalidArgument || !toolErr.Retryable {
				t.Fatalf("task error = %+v, want retryable invalid_argument", toolErr)
			}
			if got := toolErr.Details["field"]; got != "parameters" {
				t.Fatalf("details.field = %#v, want parameters", got)
			}
			if len(validator.requests) != 0 || len(writer.commits) != 0 {
				t.Fatalf("validator/writer called on rejected scope id: %v/%v", validator.requests, writer.commits)
			}
		})
	}
}

// --- extraction validator ---------------------------------------------------

type fakeExtractionReader struct {
	state     ExtractionReconciliation
	err       error
	calls     int
	versionID int64
}

func (f *fakeExtractionReader) ExtractionDone(_ context.Context, versionID int64) (ExtractionReconciliation, error) {
	f.calls++
	f.versionID = versionID
	return f.state, f.err
}

func TestExtractionCompletionValidatorRequiresArtifacts(t *testing.T) {
	reader := &fakeExtractionReader{state: ExtractionReconciliation{RunID: 5, Done: true, Expected: 58, Extracted: 58}}
	tool := NewTaskCompleteTool(NewExtractionCompletionValidator(reader, 9), &fakeCompletionWriter{})

	_, err := executeTaskComplete(t, tool, Scope{VersionID: 7}, `{"summary":"done"}`)
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeInvalidArgument || !toolErr.Retryable {
		t.Fatalf("task error = %+v, want retryable invalid_argument", toolErr)
	}
	if field := toolErr.Details["field"]; field != "artifacts" {
		t.Fatalf("details.field = %#v, want artifacts", field)
	}
	if !strings.Contains(toolErr.Message, "extraction") {
		t.Fatalf("message does not name the extraction deliverable: %q", toolErr.Message)
	}
	if reader.calls != 0 {
		t.Fatalf("reader calls = %d, want 0 before artifacts are supplied", reader.calls)
	}
}

func TestExtractionCompletionValidatorAcceptsReconciledRun(t *testing.T) {
	reader := &fakeExtractionReader{state: ExtractionReconciliation{RunID: 5, Done: true, Expected: 58, Extracted: 58}}
	writer := &fakeCompletionWriter{outcome: CompletionOutcome{PlanItemID: 9, PlanStatus: "done"}}
	tool := NewTaskCompleteTool(NewExtractionCompletionValidator(reader, 9), writer)

	if _, err := executeTaskComplete(t, tool, Scope{VersionID: 7},
		`{"summary":"58/58","artifacts":["extraction:5"]}`); err != nil {
		t.Fatalf("reconciled extraction rejected: %v", err)
	}
	if reader.versionID != 7 {
		t.Fatalf("reader version = %d, want 7", reader.versionID)
	}
	if len(writer.commits) != 1 {
		t.Fatalf("writer calls = %d, want 1", len(writer.commits))
	}
}

func TestExtractionCompletionValidatorRejectsUnfinishedRun(t *testing.T) {
	reader := &fakeExtractionReader{state: ExtractionReconciliation{RunID: 5, Done: false, Expected: 58, Extracted: 58}}
	tool := NewTaskCompleteTool(NewExtractionCompletionValidator(reader, 9), &fakeCompletionWriter{})

	_, err := executeTaskComplete(t, tool, Scope{VersionID: 7},
		`{"summary":"done","artifacts":["extraction:5"]}`)
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeInvalidArgument || !toolErr.Retryable {
		t.Fatalf("task error = %+v, want retryable invalid_argument", toolErr)
	}
	if !strings.Contains(strings.ToLower(toolErr.Message), "not finished") &&
		!strings.Contains(strings.ToLower(toolErr.Message), "unfinished") {
		t.Fatalf("message does not explain the unfinished run: %q", toolErr.Message)
	}
}

func TestExtractionCompletionValidatorRejectsCountMismatch(t *testing.T) {
	reader := &fakeExtractionReader{state: ExtractionReconciliation{RunID: 5, Done: true, Expected: 58, Extracted: 55}}
	tool := NewTaskCompleteTool(NewExtractionCompletionValidator(reader, 9), &fakeCompletionWriter{})

	_, err := executeTaskComplete(t, tool, Scope{VersionID: 7},
		`{"summary":"58/58","artifacts":["extraction:5"]}`)
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeInvalidArgument || !toolErr.Retryable {
		t.Fatalf("task error = %+v, want retryable invalid_argument", toolErr)
	}
	if !strings.Contains(toolErr.Message, "58") || !strings.Contains(toolErr.Message, "55") {
		t.Fatalf("message does not name the reconciliation counts: %q", toolErr.Message)
	}
	if got := toolErr.Details["expected"]; got != 58 {
		t.Fatalf("details.expected = %#v, want 58", got)
	}
	if got := toolErr.Details["extracted"]; got != 55 {
		t.Fatalf("details.extracted = %#v, want 55", got)
	}
	if got := toolErr.Details["missing"]; got != 3 {
		t.Fatalf("details.missing = %#v, want 3", got)
	}
	if got := toolErr.Details["field"]; got != "extraction" {
		t.Fatalf("details.field = %#v, want extraction", got)
	}
	hint, ok := toolErr.Details["hint"].(string)
	if !ok || !strings.Contains(hint, "reconcile") {
		t.Fatalf("details.hint = %#v, want the reconcile hint", toolErr.Details["hint"])
	}
	t.Logf("extraction mismatch payload: %s", testJSON(t, toolErr.Payload()))
}

// TestExtractionCompletionValidatorRejectsOverExtraction pins the non-negative
// reconciliation gap: extracted > expected reports over_extracted, never a
// negative missing count.
func TestExtractionCompletionValidatorRejectsOverExtraction(t *testing.T) {
	reader := &fakeExtractionReader{state: ExtractionReconciliation{RunID: 5, Done: true, Expected: 58, Extracted: 61}}
	tool := NewTaskCompleteTool(NewExtractionCompletionValidator(reader, 9), &fakeCompletionWriter{})

	_, err := executeTaskComplete(t, tool, Scope{VersionID: 7},
		`{"summary":"61/58","artifacts":["extraction:5"]}`)
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeInvalidArgument || !toolErr.Retryable {
		t.Fatalf("task error = %+v, want retryable invalid_argument", toolErr)
	}
	if got := toolErr.Details["expected"]; got != 58 {
		t.Fatalf("details.expected = %#v, want 58", got)
	}
	if got := toolErr.Details["extracted"]; got != 61 {
		t.Fatalf("details.extracted = %#v, want 61", got)
	}
	if got := toolErr.Details["over_extracted"]; got != 3 {
		t.Fatalf("details.over_extracted = %#v, want 3", got)
	}
	if missing, exists := toolErr.Details["missing"]; exists {
		t.Fatalf("details.missing = %#v must be absent when extracted > expected", missing)
	}
	if strings.Contains(toolErr.Message, "-3") {
		t.Fatalf("message reports a negative gap: %q", toolErr.Message)
	}
	if !strings.Contains(strings.ToLower(toolErr.Message), "over") {
		t.Fatalf("message does not explain the over-extraction: %q", toolErr.Message)
	}
	if !strings.Contains(toolErr.Message, "61") || !strings.Contains(toolErr.Message, "58") {
		t.Fatalf("message does not name the reconciliation counts: %q", toolErr.Message)
	}
	t.Logf("extraction over-count payload: %s", testJSON(t, toolErr.Payload()))
}

// --- plan-item validator ----------------------------------------------------

type fakePlanItemReader struct {
	item      PlanItemSnapshot
	err       error
	calls     int
	versionID int64
	itemID    int64
}

func (f *fakePlanItemReader) GetPlanItem(_ context.Context, versionID, itemID int64) (PlanItemSnapshot, error) {
	f.calls++
	f.versionID = versionID
	f.itemID = itemID
	return f.item, f.err
}

func TestPlanItemCompletionValidatorAcceptsDoneItemWithDecision(t *testing.T) {
	reader := &fakePlanItemReader{item: PlanItemSnapshot{ID: 9, VersionID: 7, Status: "done", Decision: "approved"}}
	writer := &fakeCompletionWriter{outcome: CompletionOutcome{PlanItemID: 9, PlanStatus: "done", FactID: 88}}
	tool := NewTaskCompleteTool(NewPlanItemCompletionValidator(reader, 9), writer)

	value, err := executeTaskComplete(t, tool, Scope{VersionID: 7},
		`{"summary":"plan item finished"}`)
	if err != nil {
		t.Fatalf("done plan item rejected: %v", err)
	}
	if reader.calls != 1 || reader.versionID != 7 || reader.itemID != 9 {
		t.Fatalf("reader call = %d version=%d item=%d", reader.calls, reader.versionID, reader.itemID)
	}
	if len(writer.commits) != 1 {
		t.Fatalf("writer calls = %d, want 1", len(writer.commits))
	}
	if got := writer.commits[0].PlanItemID; got != 9 {
		t.Fatalf("commit target = %d, want the plan item the validator re-read (9)", got)
	}
	result, ok := value.(*TaskCompleteResult)
	if !ok || result.PlanItemID != "9" {
		t.Fatalf("result = %#v, want plan_item_id 9", value)
	}
}

func TestPlanItemCompletionValidatorRejectsPendingItem(t *testing.T) {
	reader := &fakePlanItemReader{item: PlanItemSnapshot{ID: 9, VersionID: 7, Status: "pending"}}
	tool := NewTaskCompleteTool(NewPlanItemCompletionValidator(reader, 9), &fakeCompletionWriter{})

	_, err := executeTaskComplete(t, tool, Scope{VersionID: 7},
		`{"summary":"plan item finished"}`)
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeInvalidArgument || !toolErr.Retryable {
		t.Fatalf("task error = %+v, want retryable invalid_argument", toolErr)
	}
	if !strings.Contains(toolErr.Message, "pending") || !strings.Contains(toolErr.Message, "9") {
		t.Fatalf("message does not name the pending item: %q", toolErr.Message)
	}
	if got := toolErr.Details["status"]; got != "pending" {
		t.Fatalf("details.status = %#v, want pending", got)
	}
}

func TestPlanItemCompletionValidatorRejectsMissingDecision(t *testing.T) {
	reader := &fakePlanItemReader{item: PlanItemSnapshot{ID: 9, VersionID: 7, Status: "done", Decision: "  "}}
	validator := NewPlanItemCompletionValidator(reader, 9)

	_, err := validator.Validate(context.Background(), CompletionRequest{
		Scope:   Scope{VersionID: 7},
		Summary: "plan item finished",
		Status:  "completed",
	})
	if !errors.Is(err, ErrNotComplete) {
		t.Fatalf("error = %v, want ErrNotComplete", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "decision") {
		t.Fatalf("error does not name the missing decision: %v", err)
	}
}

func TestPlanItemCompletionValidatorRejectsMissingTarget(t *testing.T) {
	tool := NewTaskCompleteTool(NewPlanItemCompletionValidator(&fakePlanItemReader{}, 0), &fakeCompletionWriter{})
	_, err := executeTaskComplete(t, tool, Scope{VersionID: 7}, `{"summary":"done"}`)
	if err == nil {
		t.Fatal("a plan-item validator without a target must fail closed")
	}
	var toolErr *Error
	if errors.As(err, &toolErr) {
		t.Fatalf("missing wiring must stay an internal error, got %+v", toolErr)
	}
}

// --- design / host fail-closed validators -----------------------------------

type fakeDesignRevisionReader struct {
	exists    bool
	err       error
	versionID int64
}

func (f *fakeDesignRevisionReader) DesignRevisionExists(_ context.Context, versionID int64) (bool, error) {
	f.versionID = versionID
	return f.exists, f.err
}

func TestDesignCompletionValidatorRequiresArtifactsAndRevision(t *testing.T) {
	tool := NewTaskCompleteTool(NewDesignCompletionValidator(&fakeDesignRevisionReader{exists: false}, 9), &fakeCompletionWriter{})

	_, err := executeTaskComplete(t, tool, Scope{VersionID: 7}, `{"summary":"design done"}`)
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeInvalidArgument || !toolErr.Retryable || toolErr.Details["field"] != "artifacts" {
		t.Fatalf("missing artifacts = %+v, want retryable artifacts error", toolErr)
	}

	_, err = executeTaskComplete(t, tool, Scope{VersionID: 7},
		`{"summary":"design done","artifacts":["design:31"]}`)
	toolErr = asToolError(t, err)
	if toolErr.Code != CodeInvalidArgument || !toolErr.Retryable {
		t.Fatalf("missing design revision = %+v, want retryable invalid_argument", toolErr)
	}
	if !strings.Contains(strings.ToLower(toolErr.Message), "revision") {
		t.Fatalf("message does not name the missing revision: %q", toolErr.Message)
	}
}

func TestDesignCompletionValidatorFailsClosedWithoutReader(t *testing.T) {
	validator := NewDesignCompletionValidator(nil, 9)
	_, err := validator.Validate(context.Background(), CompletionRequest{
		Scope:   Scope{VersionID: 7},
		Summary: "design done",
		Status:  "completed",
	})
	if err == nil {
		t.Fatal("unwired design revision reader must fail closed")
	}
	var toolErr *Error
	if errors.As(err, &toolErr) {
		t.Fatalf("unwired design reader must be an internal (non-retryable) error, got %+v", toolErr)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "not configured") {
		t.Fatalf("error does not name the missing dependency: %v", err)
	}
}

func TestDesignCompletionValidatorAcceptsExistingRevision(t *testing.T) {
	reader := &fakeDesignRevisionReader{exists: true}
	writer := &fakeCompletionWriter{outcome: CompletionOutcome{PlanItemID: 9, PlanStatus: "done"}}
	tool := NewTaskCompleteTool(NewDesignCompletionValidator(reader, 9), writer)
	if _, err := executeTaskComplete(t, tool, Scope{VersionID: 7},
		`{"summary":"design done","artifacts":["design:31"]}`); err != nil {
		t.Fatalf("existing design revision rejected: %v", err)
	}
	if reader.versionID != 7 {
		t.Fatalf("reader version = %d, want 7", reader.versionID)
	}
	if len(writer.commits) != 1 || writer.commits[0].PlanItemID != 9 {
		t.Fatalf("design commit target = %+v, want 9", writer.commits)
	}
}

type fakeHostTurnReader struct {
	answered bool
	err      error
}

func (f *fakeHostTurnReader) TurnAnswered(_ context.Context, _ Scope) (bool, error) {
	return f.answered, f.err
}

func TestHostCompletionValidatorRequiresAnsweredTurn(t *testing.T) {
	tool := NewTaskCompleteTool(NewHostCompletionValidator(&fakeHostTurnReader{answered: false}, 9), &fakeCompletionWriter{})
	_, err := executeTaskComplete(t, tool, Scope{VersionID: 7, ConversationID: 3}, `{"summary":"answered"}`)
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeInvalidArgument || !toolErr.Retryable {
		t.Fatalf("unanswered host turn = %+v, want retryable invalid_argument", toolErr)
	}
	if !strings.Contains(strings.ToLower(toolErr.Message), "question") {
		t.Fatalf("message does not name the unanswered question: %q", toolErr.Message)
	}

	writer := &fakeCompletionWriter{outcome: CompletionOutcome{PlanItemID: 9, PlanStatus: "done"}}
	accepted := NewTaskCompleteTool(NewHostCompletionValidator(&fakeHostTurnReader{answered: true}, 9), writer)
	if _, err := executeTaskComplete(t, accepted, Scope{VersionID: 7, ConversationID: 3},
		`{"summary":"answered"}`); err != nil {
		t.Fatalf("answered host turn rejected: %v", err)
	}
	if len(writer.commits) != 1 || writer.commits[0].PlanItemID != 9 {
		t.Fatalf("host commit target = %+v, want 9", writer.commits)
	}
}

func TestHostCompletionValidatorFailsClosedWithoutReader(t *testing.T) {
	validator := NewHostCompletionValidator(nil, 9)
	_, err := validator.Validate(context.Background(), CompletionRequest{Scope: Scope{VersionID: 7}, Summary: "x", Status: "completed"})
	if err == nil {
		t.Fatal("unwired host turn reader must fail closed")
	}
	var toolErr *Error
	if errors.As(err, &toolErr) {
		t.Fatalf("unwired host reader must be an internal (non-retryable) error, got %+v", toolErr)
	}
	if errors.Is(err, ErrNotComplete) {
		t.Fatal("unwired host reader must not surface as a model-retryable completion rejection")
	}
}

// --- registry hook ----------------------------------------------------------

func TestTaskCompleteToolsRegistrationHook(t *testing.T) {
	if tools := TaskCompleteTools(Deps{}); len(tools) != 0 {
		t.Fatalf("TaskCompleteTools(zero deps) = %v, want no tool", tools)
	}
	tools := TaskCompleteTools(Deps{Completion: CompletionDeps{
		Validator: &fakeCompletionValidator{},
		Writer:    &fakeCompletionWriter{},
	}})
	if len(tools) != 1 || tools[0].Name() != ToolTaskComplete {
		t.Fatalf("TaskCompleteTools(configured deps) = %v", tools)
	}
}
