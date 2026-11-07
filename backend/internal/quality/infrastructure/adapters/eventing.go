// Package adapters wires the quality context's outbound ports onto the shared
// eventing and visual contexts. It keeps the quality application layer free of
// infrastructure imports.
package adapters

import (
	"context"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	qualityapp "github.com/EdouardRichard/EzTest/backend/internal/quality/application"
)

// EventingPublisher is the eventing application publish surface
// (*eventingapp.Service satisfies it).
type EventingPublisher interface {
	Publish(ctx context.Context, event eventingapp.Event) error
}

// EventPublisher adapts the quality eval.* port onto the shared eventing service.
// Every eval event is published on the version channel (EV-20).
type EventPublisher struct {
	publisher EventingPublisher
}

// NewEventPublisher wires the quality eval event publisher.
func NewEventPublisher(publisher EventingPublisher) *EventPublisher {
	return &EventPublisher{publisher: publisher}
}

var _ qualityapp.EventPublisher = (*EventPublisher)(nil)

// Publish implements qualityapp.EventPublisher.
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
