package audit

import (
	"context"
	"errors"
	"testing"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditdomain "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// fakeRecorder captures the audit entries handed to the P0 recorder.
type fakeRecorder struct {
	entries []auditdomain.Entry
	err     error
}

func (f *fakeRecorder) Record(_ context.Context, entry auditdomain.Entry) error {
	f.entries = append(f.entries, entry)
	return f.err
}

func testBatch() executiondomain.RunBatch {
	return executiondomain.RunBatch{
		ID:          8,
		VersionID:   7,
		BatchNo:     3,
		Status:      executiondomain.BatchStatusRunning,
		Paused:      true,
		PauseMode:   executiondomain.PauseModeImmediate,
		PauseReason: "restart",
		CaseCount:   4,
	}
}

func TestBatchCreatedRecordsAudit(t *testing.T) {
	recorder := &fakeRecorder{}
	sink := NewSink(recorder)

	if err := sink.BatchCreated(context.Background(), testBatch(), 42); err != nil {
		t.Fatal(err)
	}
	if len(recorder.entries) != 1 {
		t.Fatalf("entries = %+v", recorder.entries)
	}
	entry := recorder.entries[0]
	if entry.Action != ActionBatchCreated || entry.TargetType != "run_batch" || entry.TargetID != "8" {
		t.Fatalf("entry = %+v", entry)
	}
	if entry.ActorUserID == nil || *entry.ActorUserID != 42 {
		t.Fatalf("actor = %v", entry.ActorUserID)
	}
	if entry.Metadata["version_id"] != int64(7) || entry.Metadata["batch_no"] != 3 ||
		entry.Metadata["status"] != string(executiondomain.BatchStatusRunning) ||
		entry.Metadata["case_count"] != 4 {
		t.Fatalf("metadata = %#v", entry.Metadata)
	}
}

func TestBatchPausedAndResumedActions(t *testing.T) {
	recorder := &fakeRecorder{}
	sink := NewSink(recorder)
	ctx := context.Background()

	if err := sink.BatchPaused(ctx, testBatch(), true, 42); err != nil {
		t.Fatal(err)
	}
	if err := sink.BatchPaused(ctx, testBatch(), false, 42); err != nil {
		t.Fatal(err)
	}
	if len(recorder.entries) != 2 {
		t.Fatalf("entries = %+v", recorder.entries)
	}
	if recorder.entries[0].Action != ActionBatchPaused || recorder.entries[1].Action != ActionBatchResumed {
		t.Fatalf("actions = %q / %q", recorder.entries[0].Action, recorder.entries[1].Action)
	}
	if recorder.entries[0].Metadata["paused"] != true ||
		recorder.entries[0].Metadata["pause_mode"] != string(executiondomain.PauseModeImmediate) ||
		recorder.entries[0].Metadata["reason"] != "restart" {
		t.Fatalf("pause metadata = %#v", recorder.entries[0].Metadata)
	}
	if recorder.entries[1].Metadata["paused"] != false {
		t.Fatalf("resume metadata = %#v", recorder.entries[1].Metadata)
	}
}

func TestBatchCancelledRecordsAudit(t *testing.T) {
	recorder := &fakeRecorder{}
	if err := NewSink(recorder).BatchCancelled(context.Background(), 8, 7, 42); err != nil {
		t.Fatal(err)
	}
	if len(recorder.entries) != 1 {
		t.Fatalf("entries = %+v", recorder.entries)
	}
	entry := recorder.entries[0]
	if entry.Action != ActionBatchCancelled || entry.TargetType != "run_batch" || entry.TargetID != "8" {
		t.Fatalf("entry = %+v", entry)
	}
	if entry.ActorUserID == nil || *entry.ActorUserID != 42 {
		t.Fatalf("actor = %v", entry.ActorUserID)
	}
	if entry.Metadata["version_id"] != int64(7) {
		t.Fatalf("metadata = %#v", entry.Metadata)
	}
	if entry.RequestID != "" {
		t.Fatalf("unexpected request metadata: %+v", entry)
	}
}

func TestCaseFinishedRecordsOutcomeOnly(t *testing.T) {
	recorder := &fakeRecorder{}
	sink := NewSink(recorder)
	class := executiondomain.FailureClassApplication
	run := executiondomain.CaseRun{
		ID:            10,
		BatchID:       8,
		VersionID:     7,
		CaseNodeID:    101,
		Attempt:       2,
		Status:        executiondomain.CaseRunStatusFailed,
		FailureClass:  &class,
		FailureReason: "assertion_failed",
		Summary:       "secret looking summary",
		DurationMS:    1234,
	}

	if err := sink.CaseFinished(context.Background(), run, 42); err != nil {
		t.Fatal(err)
	}
	entry := recorder.entries[0]
	if entry.Action != ActionCaseFinished || entry.TargetType != "case_run" || entry.TargetID != "10" {
		t.Fatalf("entry = %+v", entry)
	}
	if entry.Metadata["batch_id"] != int64(8) || entry.Metadata["version_id"] != int64(7) ||
		entry.Metadata["case_node_id"] != int64(101) || entry.Metadata["attempt"] != 2 ||
		entry.Metadata["status"] != string(executiondomain.CaseRunStatusFailed) ||
		entry.Metadata["failure_class"] != string(executiondomain.FailureClassApplication) ||
		entry.Metadata["failure_reason"] != "assertion_failed" ||
		entry.Metadata["duration_ms"] != int64(1234) {
		t.Fatalf("metadata = %#v", entry.Metadata)
	}
	if _, exists := entry.Metadata["summary"]; exists {
		t.Fatalf("audit metadata must not carry the case summary: %#v", entry.Metadata)
	}
}

func TestCaseFinishedOmitsMissingFailureClass(t *testing.T) {
	recorder := &fakeRecorder{}
	run := executiondomain.CaseRun{
		ID:     10,
		Status: executiondomain.CaseRunStatusPassed,
	}
	if err := NewSink(recorder).CaseFinished(context.Background(), run, 0); err != nil {
		t.Fatal(err)
	}
	if _, exists := recorder.entries[0].Metadata["failure_class"]; exists {
		t.Fatalf("metadata = %#v", recorder.entries[0].Metadata)
	}
	if recorder.entries[0].ActorUserID != nil {
		t.Fatalf("actor = %v, want nil", recorder.entries[0].ActorUserID)
	}
}

func TestSinkStampsRequestMetadata(t *testing.T) {
	recorder := &fakeRecorder{}
	ctx := auditapp.WithMeta(context.Background(), auditapp.RequestMeta{
		RequestID: "req-1", IP: "127.0.0.1", UserAgent: "test-agent",
	})
	if err := NewSink(recorder).BatchCreated(ctx, testBatch(), 42); err != nil {
		t.Fatal(err)
	}
	entry := recorder.entries[0]
	if entry.RequestID != "req-1" || entry.IP != "127.0.0.1" || entry.UserAgent != "test-agent" {
		t.Fatalf("request metadata = %+v", entry)
	}
}

func TestSinkFallsBackToPlatformRequestID(t *testing.T) {
	recorder := &fakeRecorder{}
	ctx := platform.WithRequestID(context.Background(), "req-platform")
	if err := NewSink(recorder).BatchCreated(ctx, testBatch(), 42); err != nil {
		t.Fatal(err)
	}
	if got := recorder.entries[0].RequestID; got != "req-platform" {
		t.Fatalf("request id = %q", got)
	}
}

func TestSinkUsesContextActorWhenNoneGiven(t *testing.T) {
	recorder := &fakeRecorder{}
	ctx := auditapp.WithActorID(context.Background(), 55)
	if err := NewSink(recorder).BatchCreated(ctx, testBatch(), 0); err != nil {
		t.Fatal(err)
	}
	if entry := recorder.entries[0]; entry.ActorUserID == nil || *entry.ActorUserID != 55 {
		t.Fatalf("actor = %v", entry.ActorUserID)
	}
}

func TestSinkReturnsRecorderError(t *testing.T) {
	boom := errors.New("boom")
	recorder := &fakeRecorder{err: boom}
	err := NewSink(recorder).BatchCreated(context.Background(), testBatch(), 42)
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v", err)
	}
}

func TestSinkNilRecorderIsNoop(t *testing.T) {
	ctx := context.Background()
	sink := NewSink(nil)
	if err := sink.BatchCreated(ctx, testBatch(), 42); err != nil {
		t.Fatal(err)
	}
	if err := sink.BatchPaused(ctx, testBatch(), true, 42); err != nil {
		t.Fatal(err)
	}
	if err := sink.BatchCancelled(ctx, 8, 7, 42); err != nil {
		t.Fatal(err)
	}
	if err := sink.CaseFinished(ctx, executiondomain.CaseRun{ID: 1, CreatedAt: time.Now()}, 42); err != nil {
		t.Fatal(err)
	}
}
