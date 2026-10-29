// Package application is the eventing use case layer: it publishes events
// through the persistent store, broadcasts committed events to live
// subscribers, serves history snapshots and exposes subscriptions.
package application

import (
	"context"

	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
)

// Broker delivers committed events to live subscribers. The in-process hub
// implements it; the use case depends on the port so it can be tested without
// one.
type Broker interface {
	// Publish fans out a committed event to the channel's subscribers.
	Publish(event eventing.Event)
	// Subscribe registers a subscriber for channel and returns its event
	// channel plus an idempotent cancel function.
	Subscribe(channel string) (<-chan eventing.Event, func())
}

// Service is the eventing use case. The store is the source of truth; the hub
// is only a live-delivery shortcut, so a publish is broadcast only after the
// store commits it.
type Service struct {
	store eventing.Store
	hub   Broker
}

// NewService returns the eventing use case backed by store and hub. A nil hub
// disables live delivery while keeping publishing and snapshots available.
func NewService(store eventing.Store, hub Broker) *Service {
	return &Service{store: store, hub: hub}
}

// Publish validates the envelope, persists the event and then broadcasts the
// committed result.
func (s *Service) Publish(ctx context.Context, event eventing.NewEvent) (eventing.Event, error) {
	if err := eventing.Validate(event); err != nil {
		return eventing.Event{}, err
	}
	published, err := s.store.Publish(ctx, event)
	if err != nil {
		return eventing.Event{}, err
	}
	if s.hub != nil {
		s.hub.Publish(published)
	}
	return published, nil
}

// Snapshot returns the newest events below beforeSeq, the channel's latest seq
// at request time and whether an older page exists.
func (s *Service) Snapshot(ctx context.Context, channel string, beforeSeq int64, limit int) ([]eventing.Event, int64, bool, error) {
	cursor, err := s.store.LatestSeq(ctx, channel)
	if err != nil {
		return nil, 0, false, err
	}
	events, more, err := s.store.ListBefore(ctx, channel, beforeSeq, limit)
	if err != nil {
		return nil, 0, false, err
	}
	return events, cursor, more, nil
}

// Subscribe returns a live channel for channel and an idempotent cancel
// function.
func (s *Service) Subscribe(channel string) (<-chan eventing.Event, func()) {
	if s.hub == nil {
		events := make(chan eventing.Event)
		close(events)
		return events, func() {}
	}
	return s.hub.Subscribe(channel)
}
