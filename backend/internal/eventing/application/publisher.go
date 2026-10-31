// Package eventingapp defines the event publishing port shared by the other
// bounded contexts. The concrete service lives in this package and is wired by
// cmd/eztest.
package eventingapp

import "context"

// Event is one persisted, channel-scoped event.
type Event struct {
	Channel   string
	Type      string
	Scope     map[string]any
	Payload   map[string]any
	ActorID   *int64
	RequestID string
}

// Publisher appends an event and broadcasts it to in-process subscribers.
type Publisher interface {
	Publish(ctx context.Context, event Event) error
}
