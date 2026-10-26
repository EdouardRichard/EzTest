package eventing

import (
	"context"
	"errors"
)

// Errors returned by event stores and use cases. Callers use errors.Is rather
// than comparing messages.
var (
	// ErrCursorTooOld means the requested replay cursor no longer joins the
	// channel's retained sequence: events were removed or the channel was
	// rebuilt and restarted its seq counter. A stream must send a stream.reset
	// control frame and let the client fetch a fresh snapshot.
	ErrCursorTooOld = errors.New("eventing: cursor is older than the retained events")
	// ErrChannelNotFound means no events were ever published on the channel.
	ErrChannelNotFound = errors.New("eventing: channel not found")
)

// Store persists events and reads them back in channel-local seq order. Publish
// is the only writer; ListAfter and ListBefore are the replay and snapshot
// reads; LatestSeq is the current end of the channel.
type Store interface {
	// Publish assigns the next channel-local seq in a single transaction and
	// returns the persisted event.
	Publish(ctx context.Context, event NewEvent) (Event, error)
	// ListAfter returns up to limit events with seq > afterSeq, ascending.
	// It returns ErrCursorTooOld when the cursor no longer joins the retained
	// sequence.
	ListAfter(ctx context.Context, channel string, afterSeq int64, limit int) ([]Event, error)
	// ListBefore returns up to limit of the newest events with seq < beforeSeq,
	// ascending. beforeSeq <= 0 means "from the latest event". The boolean
	// reports whether an older page exists.
	ListBefore(ctx context.Context, channel string, beforeSeq int64, limit int) ([]Event, bool, error)
	// LatestSeq returns the highest assigned seq, or ErrChannelNotFound when the
	// channel has never been published.
	LatestSeq(ctx context.Context, channel string) (int64, error)
}
