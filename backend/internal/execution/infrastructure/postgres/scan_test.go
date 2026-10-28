package postgres

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// scanTestTime is a lossless TIMESTAMPTZ reading: UTC, whole seconds.
var scanTestTime = time.Date(2026, 10, 28, 9, 30, 0, 0, time.UTC)

// fakeScanRow is a rowScanner whose values are assigned to the scan
// destinations. It lets the scan functions be exercised without a database, so
// the column order and the JSONB/NULL conversion stay pinned by unit tests.
type fakeScanRow struct {
	values []any
}

// Scan implements rowScanner.
func (r fakeScanRow) Scan(dest ...any) error {
	if len(dest) != len(r.values) {
		return fmt.Errorf("fake row: %d destinations for %d values", len(dest), len(r.values))
	}
	for i := range dest {
		if err := assignScanValue(dest[i], r.values[i]); err != nil {
			return fmt.Errorf("fake row column %d: %w", i+1, err)
		}
	}
	return nil
}

// assignScanValue emulates the pgx codecs the repositories rely on: a nil value
// clears the destination (SQL NULL), a value is assigned through a pointer, and
// an optional column (pointer destination) is allocated with the value.
func assignScanValue(dest, value any) error {
	target := reflect.ValueOf(dest)
	if target.Kind() != reflect.Pointer || target.IsNil() {
		return fmt.Errorf("destination %T is not a non-nil pointer", dest)
	}
	elem := target.Elem()
	if value == nil {
		elem.Set(reflect.Zero(elem.Type()))
		return nil
	}
	source := reflect.ValueOf(value)
	if source.Type().AssignableTo(elem.Type()) {
		elem.Set(source)
		return nil
	}
	if source.Type().ConvertibleTo(elem.Type()) {
		elem.Set(source.Convert(elem.Type()))
		return nil
	}
	if elem.Kind() == reflect.Pointer {
		pointer := reflect.New(elem.Type().Elem())
		if source.Type().AssignableTo(pointer.Elem().Type()) {
			pointer.Elem().Set(source)
			elem.Set(pointer)
			return nil
		}
		if source.Type().ConvertibleTo(pointer.Elem().Type()) {
			pointer.Elem().Set(source.Convert(pointer.Elem().Type()))
			elem.Set(pointer)
			return nil
		}
	}
	return fmt.Errorf("cannot assign %T to %s", value, elem.Type())
}

// TestScanBatchRoundTrip pins the run_batch column order and the nullable
// commit/timestamp columns.
func TestScanBatchRoundTrip(t *testing.T) {
	codeCommit := strings.Repeat("c", 40)
	started := scanTestTime
	finished := scanTestTime.Add(time.Minute)
	batch, err := scanBatch(fakeScanRow{values: []any{
		int64(11), int64(22), 3, domain.BatchStatusRunning, true,
		domain.PauseModeImmediate, "restart", strings.Repeat("a", 64),
		codeCommit, nil,
		5, 2, 1, 1, 1, 0, 0, int64(99), started, finished,
		"executor crashed", scanTestTime, scanTestTime.Add(2 * time.Minute),
	}})
	if err != nil {
		t.Fatalf("scanBatch: %v", err)
	}
	if batch.ID != 11 || batch.VersionID != 22 || batch.BatchNo != 3 ||
		batch.Status != domain.BatchStatusRunning || !batch.Paused ||
		batch.PauseMode != domain.PauseModeImmediate || batch.PauseReason != "restart" {
		t.Fatalf("batch identity = %+v", batch)
	}
	if batch.CodeBaselineCommit == nil || *batch.CodeBaselineCommit != codeCommit {
		t.Fatalf("code baseline commit = %v", batch.CodeBaselineCommit)
	}
	if batch.HeadCommit != nil {
		t.Fatalf("head commit = %v, want nil", batch.HeadCommit)
	}
	if batch.CaseCount != 5 || batch.PassedCount != 2 || batch.FailedCount != 1 ||
		batch.BlockedCount != 1 || batch.InconclusiveCount != 1 ||
		batch.SkippedCount != 0 || batch.InterruptedCount != 0 {
		t.Fatalf("batch counts = %+v", batch)
	}
	if batch.StartedAt == nil || !batch.StartedAt.Equal(started) ||
		batch.FinishedAt == nil || !batch.FinishedAt.Equal(finished) {
		t.Fatalf("batch times = %v/%v", batch.StartedAt, batch.FinishedAt)
	}
	if batch.FailureReason != "executor crashed" || batch.CreatedBy != 99 {
		t.Fatalf("batch failure/creator = %q/%d", batch.FailureReason, batch.CreatedBy)
	}

	// Every optional field can be NULL.
	batch, err = scanBatch(fakeScanRow{values: []any{
		int64(12), int64(22), 1, domain.BatchStatusPlanned, false,
		domain.PauseModeGraceful, "", strings.Repeat("b", 64),
		nil, nil,
		0, 0, 0, 0, 0, 0, 0, int64(7), nil, nil,
		"", scanTestTime, scanTestTime,
	}})
	if err != nil {
		t.Fatalf("scanBatch nullables: %v", err)
	}
	if batch.CodeBaselineCommit != nil || batch.HeadCommit != nil ||
		batch.StartedAt != nil || batch.FinishedAt != nil {
		t.Fatalf("nullable batch columns not NULL: %+v", batch)
	}
}

// TestScanCaseRunSnapshotAndNulls pins the JSONB snapshot decode and every
// nullable column of case_run.
func TestScanCaseRunSnapshotAndNulls(t *testing.T) {
	snapshot := domain.CaseSnapshot{
		Node: domain.CaseSnapshotNode{
			ID: 7, Title: "order", Priority: "P1", Preconditions: "signed in",
			TestData: map[string]any{"sku": "A-1"},
		},
		Steps:      []domain.CaseSnapshotStep{{Seq: 1, Action: "open", Expected: "shown", Data: map[string]any{}}},
		Assertions: []domain.CaseSnapshotAssertion{{ID: 9, StepSeq: 1, Kind: "ui", Target: "#ok", Expression: "visible", Expected: "true"}},
		Sources:    []domain.CaseSnapshotSource{{RequirementID: 42}},
		GraphHash:  strings.Repeat("a", 64),
		CapturedAt: scanTestTime,
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}

	run, err := scanCaseRun(fakeScanRow{values: []any{
		int64(31), int64(11), int64(22), int64(7), 3, strings.Repeat("d", 64), raw,
		1, domain.CaseRunStatusPending, nil, "", "",
		nil, nil, nil, nil, nil,
		false, nil, nil, nil, int64(0),
		scanTestTime, scanTestTime,
	}})
	if err != nil {
		t.Fatalf("scanCaseRun: %v", err)
	}
	if run.ID != 31 || run.BatchID != 11 || run.CaseNodeID != 7 ||
		run.CaseRevision != 3 || run.Status != domain.CaseRunStatusPending ||
		run.CleanupRequired {
		t.Fatalf("case run identity = %+v", run)
	}
	if run.Snapshot.Node.ID != 7 || run.Snapshot.Node.Priority != "P1" ||
		len(run.Snapshot.Steps) != 1 || len(run.Snapshot.Assertions) != 1 ||
		run.Snapshot.Assertions[0].ID != 9 || len(run.Snapshot.Sources) != 1 {
		t.Fatalf("snapshot did not round-trip: %+v", run.Snapshot)
	}
	if run.FailureClass != nil || run.SessionID != nil || run.SessionSnapshotID != nil ||
		run.AgentSessionID != nil || run.AgentRunID != nil || run.RetryOfCaseRunID != nil ||
		run.InterruptedStepSeq != nil || run.StartedAt != nil || run.FinishedAt != nil {
		t.Fatalf("nullable case run columns not NULL: %+v", run)
	}

	// A fully populated row, including every nullable column.
	failureClass := domain.FailureClassApplication
	sessionID, sessionSnapshotID, agentSessionID, agentRunID := int64(1), int64(2), int64(3), int64(4)
	retryOf, interrupted := int64(5), 2
	started, finished := scanTestTime, scanTestTime.Add(time.Minute)
	run, err = scanCaseRun(fakeScanRow{values: []any{
		int64(32), int64(11), int64(22), int64(8), 1, strings.Repeat("e", 64), raw,
		2, domain.CaseRunStatusFailed, string(failureClass), "assertion failed", "submitted",
		sessionID, sessionSnapshotID, agentSessionID, agentRunID, retryOf,
		true, interrupted, started, finished, int64(1234),
		scanTestTime, scanTestTime,
	}})
	if err != nil {
		t.Fatalf("scanCaseRun full: %v", err)
	}
	if run.FailureClass == nil || *run.FailureClass != failureClass ||
		run.SessionID == nil || *run.SessionID != sessionID ||
		run.SessionSnapshotID == nil || *run.SessionSnapshotID != sessionSnapshotID ||
		run.AgentSessionID == nil || *run.AgentSessionID != agentSessionID ||
		run.AgentRunID == nil || *run.AgentRunID != agentRunID ||
		run.RetryOfCaseRunID == nil || *run.RetryOfCaseRunID != retryOf ||
		!run.CleanupRequired || run.InterruptedStepSeq == nil || *run.InterruptedStepSeq != interrupted {
		t.Fatalf("case run nullable columns = %+v", run)
	}
	if run.FailureReason != "assertion failed" || run.Summary != "submitted" ||
		run.DurationMS != 1234 || run.Attempt != 2 {
		t.Fatalf("case run text columns = %+v", run)
	}
	if run.StartedAt == nil || !run.StartedAt.Equal(started) ||
		run.FinishedAt == nil || !run.FinishedAt.Equal(finished) {
		t.Fatalf("case run times = %v/%v", run.StartedAt, run.FinishedAt)
	}

	// A malformed snapshot is an error, not a silently empty snapshot.
	_, err = scanCaseRun(fakeScanRow{values: []any{
		int64(33), int64(11), int64(22), int64(7), 1, strings.Repeat("f", 64), []byte("{"),
		1, domain.CaseRunStatusPending, nil, "", "",
		nil, nil, nil, nil, nil,
		false, nil, nil, nil, int64(0),
		scanTestTime, scanTestTime,
	}})
	if err == nil {
		t.Fatal("scanCaseRun accepted a malformed snapshot")
	}
}

// TestScanStepResultNullableTimes pins the step_result column order.
func TestScanStepResultNullableTimes(t *testing.T) {
	started := scanTestTime
	step, err := scanStepResult(fakeScanRow{values: []any{
		int64(41), int64(11), int64(31), 2, "click", "ok", "clicked",
		domain.StepStatusRunning, 3, started, nil, scanTestTime, scanTestTime,
	}})
	if err != nil {
		t.Fatalf("scanStepResult: %v", err)
	}
	if step.ID != 41 || step.CaseRunID != 31 || step.Seq != 2 ||
		step.Action != "click" || step.Expected != "ok" || step.Actual != "clicked" ||
		step.Status != domain.StepStatusRunning || step.EvidenceCount != 3 {
		t.Fatalf("step = %+v", step)
	}
	if step.StartedAt == nil || !step.StartedAt.Equal(started) || step.FinishedAt != nil {
		t.Fatalf("step times = %v/%v", step.StartedAt, step.FinishedAt)
	}
}

// TestScanAssertionResultNullableStep pins the optional step_result_id link.
func TestScanAssertionResultNullableStep(t *testing.T) {
	assertion, err := scanAssertionResult(fakeScanRow{values: []any{
		int64(51), int64(11), int64(31), nil, int64(7001), "ui", "#ok",
		"true", "true", "passed", "", scanTestTime, scanTestTime,
	}})
	if err != nil {
		t.Fatalf("scanAssertionResult: %v", err)
	}
	if assertion.StepResultID != nil {
		t.Fatalf("step_result_id = %v, want nil", assertion.StepResultID)
	}
	if assertion.ID != 51 || assertion.AssertionID != 7001 || assertion.Kind != "ui" ||
		assertion.Verdict != "passed" || assertion.FailureReason != "" {
		t.Fatalf("assertion = %+v", assertion)
	}
	stepID := int64(41)
	assertion, err = scanAssertionResult(fakeScanRow{values: []any{
		int64(52), int64(11), int64(31), stepID, int64(7002), "api", "/orders",
		"201", "500", "failed", "status mismatch", scanTestTime, scanTestTime,
	}})
	if err != nil {
		t.Fatalf("scanAssertionResult linked: %v", err)
	}
	if assertion.StepResultID == nil || *assertion.StepResultID != stepID {
		t.Fatalf("step_result_id = %v", assertion.StepResultID)
	}
}

// TestScanEvidenceJSONMetadataAndNulls pins the JSONB metadata decode and the
// nullable case-run/step links of evidence.
func TestScanEvidenceJSONMetadataAndNulls(t *testing.T) {
	metadata := []byte(`{"headers":{"Content-Type":"application/json"}}`)
	evidence, err := scanEvidence(fakeScanRow{values: []any{
		int64(61), int64(11), nil, nil, "screenshot", "versions/1/a.png",
		strings.Repeat("a", 64), int64(1024), "image/png", metadata, scanTestTime,
	}})
	if err != nil {
		t.Fatalf("scanEvidence: %v", err)
	}
	if evidence.CaseRunID != nil || evidence.StepResultID != nil {
		t.Fatalf("nullable evidence links = %+v", evidence)
	}
	if evidence.Metadata["headers"] == nil || evidence.SHA256 != strings.Repeat("a", 64) ||
		evidence.SizeBytes != 1024 || evidence.Kind != "screenshot" {
		t.Fatalf("evidence = %+v", evidence)
	}

	caseRunID := int64(31)
	stepID := int64(41)
	evidence, err = scanEvidence(fakeScanRow{values: []any{
		int64(62), int64(11), caseRunID, stepID, "download", "versions/1/b.bin",
		strings.Repeat("b", 64), int64(2048), "application/octet-stream", []byte(`{}`), scanTestTime,
	}})
	if err != nil {
		t.Fatalf("scanEvidence linked: %v", err)
	}
	if evidence.CaseRunID == nil || *evidence.CaseRunID != caseRunID ||
		evidence.StepResultID == nil || *evidence.StepResultID != stepID {
		t.Fatalf("evidence links = %+v", evidence)
	}
	if evidence.Metadata == nil || len(evidence.Metadata) != 0 {
		t.Fatalf("empty metadata = %+v", evidence.Metadata)
	}

	_, err = scanEvidence(fakeScanRow{values: []any{
		int64(63), int64(11), nil, nil, "screenshot", "versions/1/c.png",
		strings.Repeat("c", 64), int64(1), "image/png", []byte("not json"), scanTestTime,
	}})
	if err == nil {
		t.Fatal("scanEvidence accepted malformed metadata")
	}
}

// TestScanEvidenceRef pins the evidence_ref columns.
func TestScanEvidenceRef(t *testing.T) {
	ref, err := scanEvidenceRef(fakeScanRow{values: []any{
		int64(61), "assertion_result", int64(51), scanTestTime,
	}})
	if err != nil {
		t.Fatalf("scanEvidenceRef: %v", err)
	}
	if ref.EvidenceID != 61 || ref.RefKind != "assertion_result" || ref.RefID != 51 ||
		!ref.CreatedAt.Equal(scanTestTime) {
		t.Fatalf("evidence ref = %+v", ref)
	}
}

// TestNullableParameterHelpers pins the SQL parameter mapping used by every
// insert: nil means NULL, a value is dereferenced, and a zero timestamp falls
// back to the database clock.
func TestNullableParameterHelpers(t *testing.T) {
	if nullableInt(nil) != nil || nullableString(nil) != nil || nullableTime(nil) != nil {
		t.Fatal("nil pointers must map to NULL")
	}
	number := int64(7)
	text := "x"
	if got := nullableInt(&number); got != number {
		t.Fatalf("nullableInt = %v", got)
	}
	if got := nullableString(&text); got != text {
		t.Fatalf("nullableString = %v", got)
	}
	if got := nullableTime(&scanTestTime); got != scanTestTime {
		t.Fatalf("nullableTime = %v", got)
	}
	if got := timeOrNow(time.Time{}); got != nil {
		t.Fatalf("zero time = %v, want nil", got)
	}
	if got := timeOrNow(scanTestTime); got != scanTestTime {
		t.Fatalf("timeOrNow = %v", got)
	}
	if got := jsonParam(nil); got != nil {
		t.Fatalf("empty json = %v, want nil", got)
	}
	if got := jsonParam([]byte(`{"a":1}`)); got != `{"a":1}` {
		t.Fatalf("jsonParam = %v", got)
	}
	document, err := jsonObjectParam(nil)
	if err != nil || document != "{}" {
		t.Fatalf("nil metadata = %q, %v", document, err)
	}
	document, err = jsonObjectParam(map[string]any{"k": "v"})
	if err != nil {
		t.Fatalf("jsonObjectParam: %v", err)
	}
	if document == "" || !strings.Contains(document, `"k":"v"`) {
		t.Fatalf("metadata document = %q", document)
	}
}
