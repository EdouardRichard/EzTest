// Package eventing is the persistent event-log domain. Events are scoped to a
// channel and ordered by a channel-local cursor (seq).
package eventing

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by Repository.Get when no event matches the
// requested channel cursor.
var ErrNotFound = errors.New("event not found")

// Event is one persisted event. Seq is the channel-scoped cursor; it is not a
// business entity ID.
type Event struct {
	Channel   string
	Seq       int64
	Type      string
	Scope     map[string]any
	Payload   map[string]any
	ActorID   *int64
	RequestID string
	CreatedAt time.Time
}

// SnapshotPage is one page of events plus the channel cursor observed when the
// page was read.
type SnapshotPage struct {
	Events  []Event
	Cursor  int64
	HasMore bool
}

// Bounds describes the cursor range currently persisted for a channel. Both
// values are zero for a channel that has no events yet.
type Bounds struct {
	FirstSeq int64
	LastSeq  int64
}

// Repository appends events and reads channel-scoped snapshots.
type Repository interface {
	// Append allocates the next channel-local seq and persists the event,
	// returning the stored event including Seq and CreatedAt.
	Append(ctx context.Context, event Event) (Event, error)
	// Snapshot returns a page of events. after == 0 requests the newest page
	// (returned in ascending seq order); after > 0 returns events with
	// seq > after in ascending order. Cursor is the channel's last_seq.
	Snapshot(ctx context.Context, channel string, after int64, limit int) (SnapshotPage, error)
	// Bounds returns the first and last persisted seq for the channel.
	Bounds(ctx context.Context, channel string) (Bounds, error)
}
