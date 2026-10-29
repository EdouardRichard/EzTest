// Package agentevent adapts the eventing context's publisher to the Agent
// runtime's activity-event port. Every agent activity is published as one
// `agent.activity` event on the session's `agent:{session_id}` channel; the
// agent application layer owns the detail contract and this adapter only maps
// the envelope (design spec §9.1).
package agentevent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
)

// ActivityType is the frozen event type of every Agent activity event.
const ActivityType = "agent.activity"

// Publisher is the slice of the eventing use case the sink needs. The eventing
// application Service satisfies it.
type Publisher interface {
	Publish(ctx context.Context, event eventing.NewEvent) (eventing.Event, error)
}

// Sink publishes Agent activity events through the P0 event bus.
type Sink struct {
	publisher Publisher
}

// NewSink returns the activity sink. A nil publisher disables publishing (used
// by partially wired processes and tests).
func NewSink(publisher Publisher) *Sink {
	return &Sink{publisher: publisher}
}

// PublishActivity maps one Agent activity onto the frozen V1 envelope and
// publishes it synchronously, so the returned error is the publisher's. The
// caller decides what a publish failure means: the run services log it and keep
// the run alive.
func (s *Sink) PublishActivity(ctx context.Context, event agentapp.ActivityEvent) error {
	if s == nil || s.publisher == nil {
		return nil
	}
	channel := event.Channel
	if channel == "" {
		channel = domain.ChannelForSession(event.SessionID)
	}
	sessionID := strconv.FormatInt(event.SessionID, 10)
	agentRole := event.AgentRole
	// The P3 envelope only references a version when one is stamped; a nil or
	// empty version id keeps the P1 event shape.
	var versionID *string
	if event.VersionID != nil && *event.VersionID != "" {
		versionID = event.VersionID
	}
	detail, err := encodeDetail(event.Detail)
	if err != nil {
		return err
	}
	_, err = s.publisher.Publish(ctx, eventing.NewEvent{
		Channel:   channel,
		Type:      ActivityType,
		VersionID: versionID,
		AgentRole: &agentRole,
		SessionID: &sessionID,
		Summary:   event.Summary,
		Detail:    detail,
	})
	return err
}

// encodeDetail marshals the activity detail into the JSON object the event
// envelope requires. A nil detail becomes the empty object, never JSON null.
func encodeDetail(detail map[string]any) (json.RawMessage, error) {
	if detail == nil {
		return json.RawMessage("{}"), nil
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		return nil, fmt.Errorf("agent: encode activity detail: %w", err)
	}
	return encoded, nil
}
