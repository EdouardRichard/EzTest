package audit_test

import (
	"context"
	"errors"
	"testing"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditdomain "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapaudit "github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/audit"
)

// fakeAuditRecorder captures the mapped audit entries.
type fakeAuditRecorder struct {
	entries []auditdomain.Entry
	err     error
}

// Record implements the audit application recorder.
func (f *fakeAuditRecorder) Record(_ context.Context, entry auditdomain.Entry) error {
	f.entries = append(f.entries, entry)
	return f.err
}

// TestAuditRecorderMapsAuditEvent pins the mindmap audit projection: actor,
// action, target pair and metadata.
func TestAuditRecorderMapsAuditEvent(t *testing.T) {
	ctx := context.Background()
	recorder := &fakeAuditRecorder{}
	adapter := mindmapaudit.NewAuditRecorder(recorder)
	var _ mindmapapp.AuditRecorder = adapter

	metadata := map[string]any{"from": "reviewing", "to": "executing", "caseCount": 12}
	if err := adapter.Record(ctx, mindmapapp.AuditEvent{
		Action:     "case.baseline_approved",
		ActorID:    42,
		TargetType: "case_version",
		TargetID:   "7",
		Metadata:   metadata,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len(recorder.entries) != 1 {
		t.Fatalf("recorded %d entries, want 1", len(recorder.entries))
	}
	entry := recorder.entries[0]
	if entry.Action != "case.baseline_approved" || entry.TargetType != "case_version" || entry.TargetID != "7" {
		t.Fatalf("entry=%+v want the audit event fields", entry)
	}
	if entry.ActorUserID == nil || *entry.ActorUserID != 42 {
		t.Fatalf("actor=%v want 42", entry.ActorUserID)
	}
	if entry.Metadata["from"] != "reviewing" || entry.Metadata["to"] != "executing" || entry.Metadata["caseCount"] != 12 {
		t.Fatalf("metadata=%v want the event metadata", entry.Metadata)
	}

	// The persisted metadata must not alias the caller's map.
	metadata["to"] = "mutated"
	if entry.Metadata["to"] != "executing" {
		t.Fatal("entry metadata aliases the caller map")
	}
}

// TestAuditRecorderStampsRequestMeta pins the P0 request correlation stamping and
// the anonymous-actor rule.
func TestAuditRecorderStampsRequestMeta(t *testing.T) {
	ctx := auditapp.WithMeta(context.Background(), auditapp.RequestMeta{
		RequestID: "req-1",
		IP:        "203.0.113.7",
		UserAgent: "eztest-test",
	})
	recorder := &fakeAuditRecorder{}
	adapter := mindmapaudit.NewAuditRecorder(recorder)

	if err := adapter.Record(ctx, mindmapapp.AuditEvent{Action: "case.exported", TargetType: "case_export", TargetID: "9"}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	entry := recorder.entries[0]
	if entry.RequestID != "req-1" || entry.IP != "203.0.113.7" || entry.UserAgent != "eztest-test" {
		t.Fatalf("request meta=%q/%q/%q want the context meta", entry.RequestID, entry.IP, entry.UserAgent)
	}
	if entry.ActorUserID != nil {
		t.Fatalf("actor=%v want nil for an anonymous event", entry.ActorUserID)
	}
}

// TestAuditRecorderFailureAndDisabledContract pins the returned recorder failure
// and the nil-recorder no-op.
func TestAuditRecorderFailureAndDisabledContract(t *testing.T) {
	ctx := context.Background()
	operational := errors.New("audit store down")
	adapter := mindmapaudit.NewAuditRecorder(&fakeAuditRecorder{err: operational})
	if err := adapter.Record(ctx, mindmapapp.AuditEvent{Action: "case.created"}); !errors.Is(err, operational) {
		t.Fatalf("Record error=%v want the recorder failure", err)
	}

	if err := mindmapaudit.NewAuditRecorder(nil).Record(ctx, mindmapapp.AuditEvent{Action: "case.created"}); err != nil {
		t.Fatalf("Record(nil recorder)=%v want nil", err)
	}
}
