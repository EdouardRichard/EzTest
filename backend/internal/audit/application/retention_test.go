package auditapp

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeRetentionStore struct {
	remaining int64
	batch     int
	err       error
	calls     int
	lastLimit int
}

func (f *fakeRetentionStore) DeleteOlderThan(_ context.Context, _ time.Time, limit int) (int64, error) {
	f.calls++
	f.lastLimit = limit
	if f.err != nil {
		return 0, f.err
	}
	if f.remaining <= 0 {
		return 0, nil
	}
	deleted := int64(limit)
	if f.remaining < deleted {
		deleted = f.remaining
	}
	f.remaining -= deleted
	return deleted, nil
}

type fakeRetentionRecorder struct {
	events []Event
}

func (f *fakeRetentionRecorder) Record(_ context.Context, event Event) error {
	f.events = append(f.events, event)
	return nil
}

func TestRetentionRunDeletesInBatchesAndAudits(t *testing.T) {
	events := &fakeRetentionStore{remaining: 5}
	audit := &fakeRetentionStore{remaining: 3}
	recorder := &fakeRetentionRecorder{}
	service := NewRetentionService(audit, events, recorder, 2)
	result, err := service.Run(context.Background(), time.Now().Add(-24*time.Hour), time.Now().Add(-48*time.Hour))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.EventsDeleted != 5 || result.AuditDeleted != 3 {
		t.Fatalf("result = %+v, want 5/3", result)
	}
	if events.calls != 3 || events.lastLimit != 2 {
		t.Fatalf("event delete calls = %d lastLimit = %d, want 3/2", events.calls, events.lastLimit)
	}
	if len(recorder.events) != 1 || recorder.events[0].Action != "retention.cleanup" {
		t.Fatalf("audit events = %#v", recorder.events)
	}
	if recorder.events[0].Metadata["events_deleted"] != int64(5) || recorder.events[0].Metadata["audit_deleted"] != int64(3) {
		t.Fatalf("audit metadata = %#v", recorder.events[0].Metadata)
	}
	if recorder.events[0].TargetKind != "retention" {
		t.Fatalf("audit target kind = %q, want retention", recorder.events[0].TargetKind)
	}
	cutoff, _ := recorder.events[0].Metadata["cutoff"].(string)
	if cutoff == "" {
		t.Fatalf("audit metadata cutoff missing: %#v", recorder.events[0].Metadata)
	}
}

func TestRetentionRunContinuesAfterEventFailure(t *testing.T) {
	events := &fakeRetentionStore{err: errors.New("event delete failed")}
	audit := &fakeRetentionStore{remaining: 1}
	service := NewRetentionService(audit, events, nil, 10)
	result, err := service.Run(context.Background(), time.Now(), time.Now())
	if err == nil {
		t.Fatal("Run succeeded despite the event failure")
	}
	if result.AuditDeleted != 1 {
		t.Fatalf("audit deleted = %d, want 1", result.AuditDeleted)
	}
}

func TestRetentionRunWithNilStores(t *testing.T) {
	service := NewRetentionService(nil, nil, nil, 0)
	result, err := service.Run(context.Background(), time.Now(), time.Now())
	if err != nil || result != (RetentionResult{}) {
		t.Fatalf("Run = %+v, %v", result, err)
	}
}
