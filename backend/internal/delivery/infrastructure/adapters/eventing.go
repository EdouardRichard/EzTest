package adapters

import (
	"context"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
)

// EventingPublisher is the eventing application publish surface
// (*eventingapp.Service satisfies it).
type EventingPublisher interface {
	Publish(ctx context.Context, event eventingapp.Event) error
}

// EventPublisher adapts the delivery event port onto the shared eventing
// service. Every P4 delivery event is published on the version channel.
type EventPublisher struct {
	publisher EventingPublisher
}

// NewEventPublisher wires the delivery event publisher.
func NewEventPublisher(publisher EventingPublisher) *EventPublisher {
	return &EventPublisher{publisher: publisher}
}

var _ deliveryapp.EventPublisher = (*EventPublisher)(nil)

// Publish implements deliveryapp.EventPublisher.
func (p *EventPublisher) Publish(ctx context.Context, eventType string, versionID int64, payload map[string]any) error {
	if p == nil || p.publisher == nil {
		return nil
	}
	versionIDString := platformid.Format(versionID)
	return p.publisher.Publish(ctx, eventingapp.Event{
		Channel:   "version:" + versionIDString,
		Type:      eventType,
		Scope:     map[string]any{"version_id": versionIDString},
		Payload:   payload,
		RequestID: requestid.From(ctx),
	})
}

// AuditRecorder is the audit application record surface (*auditapp.Service
// satisfies it).
type AuditRecorder interface {
	Record(ctx context.Context, event auditapp.Event) error
}

// AuditSink adapts the delivery audit port onto the shared audit service. A
// zero actor is stored as NULL (agent/system writes).
type AuditSink struct {
	recorder AuditRecorder
}

// NewAuditSink wires the delivery audit sink.
func NewAuditSink(recorder AuditRecorder) *AuditSink {
	return &AuditSink{recorder: recorder}
}

var _ deliveryapp.AuditSink = (*AuditSink)(nil)

// Write implements deliveryapp.AuditSink.
func (s *AuditSink) Write(ctx context.Context, actorID int64, action, targetKind, targetID string, metadata map[string]any) error {
	if s == nil || s.recorder == nil {
		return nil
	}
	var actor *int64
	if actorID > 0 {
		value := actorID
		actor = &value
	}
	return s.recorder.Record(ctx, auditapp.Event{
		ActorID:    actor,
		Action:     action,
		TargetKind: targetKind,
		TargetID:   targetID,
		RequestID:  requestid.From(ctx),
		Metadata:   metadata,
	})
}
