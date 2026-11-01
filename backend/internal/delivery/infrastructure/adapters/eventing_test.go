package adapters

import (
	"context"
	"testing"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
)

type fakeEventing struct {
	events []eventingapp.Event
}

func (f *fakeEventing) Publish(_ context.Context, event eventingapp.Event) error {
	f.events = append(f.events, event)
	return nil
}

type fakeAudit struct {
	events []auditapp.Event
}

func (f *fakeAudit) Record(_ context.Context, event auditapp.Event) error {
	f.events = append(f.events, event)
	return nil
}

func TestEventPublisherPublishesOnTheVersionChannel(t *testing.T) {
	events := &fakeEventing{}
	publisher := NewEventPublisher(events)
	if err := publisher.Publish(context.Background(), "issue.created", 7, map[string]any{"issue_id": "9001"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(events.events) != 1 {
		t.Fatalf("events = %+v", events.events)
	}
	event := events.events[0]
	if event.Channel != "version:7" || event.Type != "issue.created" || event.Scope["version_id"] != "7" {
		t.Fatalf("event = %+v", event)
	}
	if event.Payload["issue_id"] != "9001" {
		t.Fatalf("payload = %+v", event.Payload)
	}
}

func TestAuditSinkWritesHumanAndAgentEntries(t *testing.T) {
	recorder := &fakeAudit{}
	sink := NewAuditSink(recorder)
	ctx := context.Background()
	if err := sink.Write(ctx, 42, "delivery.issue.confirmed", "issue", "9001", map[string]any{"status": "confirmed"}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := sink.Write(ctx, 0, "delivery.issue.created", "issue", "9001", nil); err != nil {
		t.Fatalf("Write(agent): %v", err)
	}
	if len(recorder.events) != 2 {
		t.Fatalf("audit events = %+v", recorder.events)
	}
	human := recorder.events[0]
	if human.ActorID == nil || *human.ActorID != 42 || human.Action != "delivery.issue.confirmed" ||
		human.TargetKind != "issue" || human.TargetID != "9001" {
		t.Fatalf("human audit = %+v", human)
	}
	if recorder.events[1].ActorID != nil {
		t.Fatalf("agent audit actor = %v, want nil", recorder.events[1].ActorID)
	}
}

func TestNilDeliverySinksAreSafe(t *testing.T) {
	if err := NewEventPublisher(nil).Publish(context.Background(), "x", 1, nil); err != nil {
		t.Fatalf("nil event publisher: %v", err)
	}
	if err := NewAuditSink(nil).Write(context.Background(), 0, "x", "k", "1", nil); err != nil {
		t.Fatalf("nil audit sink: %v", err)
	}
}
