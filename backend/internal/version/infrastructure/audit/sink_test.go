package audit

import (
	"context"
	"testing"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditdomain "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
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

// TestSinkRecordsLifecycleEntry pins the lifecycle projection: action, version
// target, from/to/phase metadata, actor and request correlation.
func TestSinkRecordsLifecycleEntry(t *testing.T) {
	recorder := &fakeRecorder{}
	sink := NewSink(recorder)
	ctx := auditapp.WithMeta(auditapp.WithActorID(context.Background(), 3), auditapp.RequestMeta{
		RequestID: "req_version_audit_1",
		IP:        "203.0.113.7",
		UserAgent: "eztest-version-test",
	})

	err := sink.Record(ctx, versionapp.VersionAuditEvent{
		Action:    "version.continued",
		VersionID: 42,
		From:      "completed",
		To:        "continuing",
		Phase:     "clarifying",
		ActorID:   7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(recorder.entries) != 1 {
		t.Fatalf("entries=%d want 1", len(recorder.entries))
	}
	entry := recorder.entries[0]
	if entry.Action != "version.continued" || entry.TargetType != "version" || entry.TargetID != "42" {
		t.Fatalf("entry=%+v", entry)
	}
	if entry.Metadata["from"] != "completed" || entry.Metadata["to"] != "continuing" ||
		entry.Metadata["phase"] != "clarifying" {
		t.Fatalf("metadata=%v", entry.Metadata)
	}
	if entry.ActorUserID == nil || *entry.ActorUserID != 7 {
		t.Fatalf("actor=%v want explicit 7", entry.ActorUserID)
	}
	if entry.RequestID != "req_version_audit_1" || entry.IP != "203.0.113.7" ||
		entry.UserAgent != "eztest-version-test" {
		t.Fatalf("request meta=%+v", entry)
	}
}

// TestSinkRecordsRequirementEntry pins the aggregate-write projection: the
// caller's target and metadata are kept and no version from/to is invented.
func TestSinkRecordsRequirementEntry(t *testing.T) {
	recorder := &fakeRecorder{}
	sink := NewSink(recorder)

	err := sink.Record(context.Background(), versionapp.VersionAuditEvent{
		Action:     "version.requirement_created",
		VersionID:  42,
		TargetType: "requirement",
		TargetID:   "77",
		ActorID:    7,
		Metadata:   map[string]any{"status": "draft", "revision": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := recorder.entries[0]
	if entry.TargetType != "requirement" || entry.TargetID != "77" {
		t.Fatalf("target=%q/%q want requirement/77", entry.TargetType, entry.TargetID)
	}
	if entry.Metadata["status"] != "draft" || entry.Metadata["revision"] != 1 {
		t.Fatalf("metadata=%v", entry.Metadata)
	}
	if _, ok := entry.Metadata["from"]; ok {
		t.Fatalf("non-lifecycle entry invented from/to: %v", entry.Metadata)
	}
}

// TestSinkFailureAndDisabledContract pins the best-effort contract and the nil
// recorder no-op used by tests and partially wired processes.
func TestSinkFailureAndDisabledContract(t *testing.T) {
	operational := &fakeRecorder{err: context.DeadlineExceeded}
	if err := NewSink(operational).Record(context.Background(), versionapp.VersionAuditEvent{
		Action: "version.created", VersionID: 1,
	}); err == nil {
		t.Fatal("want recorder error")
	}
	if err := NewSink(nil).Record(context.Background(), versionapp.VersionAuditEvent{
		Action: "version.created", VersionID: 1,
	}); err != nil {
		t.Fatalf("disabled sink: %v", err)
	}
}
