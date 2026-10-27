package agentaudit

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	audit "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
)

// capturingRecorder records the entries and the context the sink used.
type capturingRecorder struct {
	entries     []audit.Entry
	ctxErr      error
	deadline    time.Time
	hasDeadline bool
	err         error
}

func (r *capturingRecorder) Record(ctx context.Context, entry audit.Entry) error {
	r.entries = append(r.entries, entry)
	r.ctxErr = ctx.Err()
	r.deadline, r.hasDeadline = ctx.Deadline()
	return r.err
}

func TestRecordMapsEntry(t *testing.T) {
	recorder := &capturingRecorder{}
	sink := NewSink(recorder)
	actor := int64(5)
	ctx := auditapp.WithMeta(context.Background(), auditapp.RequestMeta{
		RequestID: "req_audit_1",
		IP:        "192.0.2.7",
		UserAgent: "agent-test",
	})
	err := sink.Record(ctx, agentapp.AuditEvent{
		Action:      agentapp.ActionToolInvoked,
		ActorUserID: &actor,
		TargetType:  "agent_run",
		TargetID:    "9",
		Metadata: map[string]any{
			"tool":        "file.list",
			"status":      "ok",
			"duration_ms": 12,
			"run_id":      9,
			"session_id":  1,
		},
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len(recorder.entries) != 1 {
		t.Fatalf("recorded entries=%d want 1", len(recorder.entries))
	}
	entry := recorder.entries[0]
	if entry.Action != agentapp.ActionToolInvoked {
		t.Fatalf("Action=%q", entry.Action)
	}
	if entry.ActorUserID == nil || *entry.ActorUserID != actor {
		t.Fatalf("ActorUserID=%v want %d", entry.ActorUserID, actor)
	}
	if entry.TargetType != "agent_run" || entry.TargetID != "9" {
		t.Fatalf("target=%q/%q", entry.TargetType, entry.TargetID)
	}
	wantMetadata := map[string]any{
		"tool":        "file.list",
		"status":      "ok",
		"duration_ms": 12,
		"run_id":      9,
		"session_id":  1,
	}
	if !reflect.DeepEqual(entry.Metadata, wantMetadata) {
		t.Fatalf("Metadata=%v want %v", entry.Metadata, wantMetadata)
	}
	// The request metadata attached by the HTTP guard is carried onto the entry
	// so an admin PATCH/delete is traceable to its request.
	if entry.RequestID != "req_audit_1" || entry.IP != "192.0.2.7" || entry.UserAgent != "agent-test" {
		t.Fatalf("request metadata=%q/%q/%q", entry.RequestID, entry.IP, entry.UserAgent)
	}
}

func TestRecordDetachesCancellation(t *testing.T) {
	recorder := &capturingRecorder{}
	sink := NewSink(recorder)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := sink.Record(ctx, agentapp.AuditEvent{
		Action:     agentapp.ActionRunAborted,
		TargetType: "agent_run",
		TargetID:   "9",
		Metadata:   map[string]any{"run_id": 9, "abort_code": "user_abort"},
	})
	if err != nil {
		t.Fatalf("Record after cancellation: %v", err)
	}
	if len(recorder.entries) != 1 {
		t.Fatalf("recorded entries=%d want 1", len(recorder.entries))
	}
	if recorder.ctxErr != nil {
		t.Fatalf("recorder context was cancelled: %v", recorder.ctxErr)
	}
	if !recorder.hasDeadline {
		t.Fatal("recorder context has no deadline")
	}
	remaining := time.Until(recorder.deadline)
	if remaining <= 0 || remaining > 3*time.Second {
		t.Fatalf("recorder deadline is %v away, want ~2s", remaining)
	}
}

func TestRecordLogsAndSwallowsRecorderError(t *testing.T) {
	silenceLogs(t)
	recorder := &capturingRecorder{err: errors.New("audit store is down")}
	sink := NewSink(recorder)
	err := sink.Record(context.Background(), agentapp.AuditEvent{
		Action:     agentapp.ActionSessionDeleted,
		TargetType: "agent_session",
		TargetID:   "1",
		Metadata:   map[string]any{"session_id": 1},
	})
	if err != nil {
		t.Fatalf("Record returned %v; audit failures must only be logged", err)
	}
	if len(recorder.entries) != 1 {
		t.Fatal("the recorder was not called")
	}
}

func TestRecordWithoutRecorderIsNoop(t *testing.T) {
	if err := NewSink(nil).Record(context.Background(), agentapp.AuditEvent{Action: "x"}); err != nil {
		t.Fatalf("Record with a nil recorder: %v", err)
	}
}

// silenceLogs discards the sink's failure log so test output stays clean.
func silenceLogs(t *testing.T) {
	t.Helper()
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
}
