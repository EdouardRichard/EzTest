// Package eventing adapts the P3 version event publisher port to the P0
// eventing use case (design spec §3.5/§6.11). Every version event is published
// on the fixed channel version:<version id> with the decimal string version id,
// and the single requirement reference is mapped onto eventing.Refs.Requirement
// (the frozen envelope has no field for the other P3 ids: they travel in
// detail).
package eventing

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	eventingdomain "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// Publisher is the narrow slice of the eventing use case the adapter needs.
// *eventing/application.Service satisfies it.
type Publisher interface {
	Publish(ctx context.Context, event eventingdomain.NewEvent) (eventingdomain.Event, error)
}

// EventPublisher publishes P3 version events through the P0 event bus.
type EventPublisher struct {
	events Publisher
}

// NewEventPublisher returns the adapter. A nil publisher disables publishing
// (used by partially wired processes and tests).
func NewEventPublisher(events Publisher) *EventPublisher {
	return &EventPublisher{events: events}
}

var _ application.EventPublisher = (*EventPublisher)(nil)

// Publish maps one VersionEvent onto the frozen V1 envelope and publishes it.
func (p *EventPublisher) Publish(ctx context.Context, event application.VersionEvent) error {
	if p == nil || p.events == nil {
		return nil
	}
	versionID := strconv.FormatInt(event.VersionID, 10)
	detail, err := encodeDetail(event)
	if err != nil {
		return err
	}
	refs := eventingdomain.Refs{}
	if event.RefRequirementID != nil {
		refs.Requirement = []string{strconv.FormatInt(*event.RefRequirementID, 10)}
	}
	_, err = p.events.Publish(ctx, eventingdomain.NewEvent{
		Channel:   fmt.Sprintf("version:%d", event.VersionID),
		Type:      event.Type,
		VersionID: &versionID,
		Summary:   event.Summary,
		Detail:    detail,
		Refs:      refs,
	})
	return err
}

// encodeDetail marshals the version event detail and stamps the actor/source
// pair spec §6.9 requires when the caller did not already carry it. A nil
// detail becomes the empty object the envelope expects, never JSON null.
func encodeDetail(event application.VersionEvent) (json.RawMessage, error) {
	detail := make(map[string]any, len(event.Detail)+2)
	for key, value := range event.Detail {
		detail[key] = value
	}
	if _, exists := detail["actorId"]; !exists && event.ActorID > 0 {
		detail["actorId"] = event.ActorID
	}
	if _, exists := detail["source"]; !exists && event.Source != "" {
		detail["source"] = event.Source
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		return nil, fmt.Errorf("version: encode event detail: %w", err)
	}
	return encoded, nil
}
