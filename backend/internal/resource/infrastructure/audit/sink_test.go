package audit

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditdomain "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
)

// fakeRecorder captures the mapped audit entries.
type fakeRecorder struct {
	entries []auditdomain.Entry
	err     error
}

func (r *fakeRecorder) Record(_ context.Context, entry auditdomain.Entry) error {
	r.entries = append(r.entries, entry)
	return r.err
}

// TestSinkRecordsSessionAudit pins the resource Session audit projection: the
// action, session/takeover target, state metadata, explicit actor and request
// correlation are carried; no storage state or instruction content ever is.
func TestSinkRecordsSessionAudit(t *testing.T) {
	recorder := &fakeRecorder{}
	sink := NewSink(recorder)
	ctx := auditapp.WithMeta(auditapp.WithActorID(context.Background(), 5), auditapp.RequestMeta{
		RequestID: "req_resource_audit_1",
		IP:        "203.0.113.9",
		UserAgent: "eztest-resource-test",
	})

	err := sink.RecordResourceAudit(ctx, resourceapp.AuditEvent{
		Action:     resourceapp.ActionSessionImported,
		TargetType: resourceapp.AuditTargetSession,
		TargetID:   "42",
		ActorID:    7,
		Metadata:   map[string]any{"state": "valid", "environment_id": int64(3)},
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := recorder.entries[0]
	if entry.Action != resourceapp.ActionSessionImported || entry.TargetType != "session" || entry.TargetID != "42" {
		t.Fatalf("entry=%+v", entry)
	}
	if entry.ActorUserID == nil || *entry.ActorUserID != 7 {
		t.Fatalf("actor=%v want explicit 7", entry.ActorUserID)
	}
	if entry.Metadata["state"] != "valid" || entry.Metadata["environment_id"] != int64(3) {
		t.Fatalf("metadata=%v", entry.Metadata)
	}
	if entry.RequestID != "req_resource_audit_1" || entry.IP != "203.0.113.9" ||
		entry.UserAgent != "eztest-resource-test" {
		t.Fatalf("request meta=%+v", entry)
	}
	raw, err := json.Marshal(entry.Metadata)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	for _, secret := range []string{"cookie", "Authorization", "storage_state", "localStorage"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("audit metadata leaks %q: %s", secret, raw)
		}
	}
}

// TestSinkSessionAuditFallsBackToContextActor pins the shared actor rule: an
// event without an actor uses the authenticated request context actor.
func TestSinkSessionAuditFallsBackToContextActor(t *testing.T) {
	recorder := &fakeRecorder{}
	sink := NewSink(recorder)
	ctx := auditapp.WithActorID(context.Background(), 9)

	if err := sink.RecordResourceAudit(ctx, resourceapp.AuditEvent{
		Action:     resourceapp.ActionSessionInvalidated,
		TargetType: resourceapp.AuditTargetSession,
		TargetID:   "42",
		Metadata:   map[string]any{"state": "invalid"},
	}); err != nil {
		t.Fatal(err)
	}
	if recorder.entries[0].ActorUserID == nil || *recorder.entries[0].ActorUserID != 9 {
		t.Fatalf("actor=%v want context actor 9", recorder.entries[0].ActorUserID)
	}
}

// TestSinkResourceAuditDisabled pins the nil-recorder no-op used by tests and
// partially wired processes.
func TestSinkResourceAuditDisabled(t *testing.T) {
	if err := NewSink(nil).RecordResourceAudit(context.Background(), resourceapp.AuditEvent{
		Action: "resource.session_imported",
	}); err != nil {
		t.Fatalf("disabled sink: %v", err)
	}
}
