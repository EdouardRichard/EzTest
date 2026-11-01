package providers

import (
	"context"
	"strings"
	"time"

	chainapp "github.com/EdouardRichard/EzTest/backend/internal/chain/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// EventSnapshotReader is the eventing application snapshot read surface. The
// eventing service (and its repository) satisfies it.
type EventSnapshotReader interface {
	Snapshot(ctx context.Context, channel string, after int64, limit int) (eventingapp.Snapshot, error)
}

// EventCursorProvider returns the latest event seq of a channel as the chain's
// snapshot cursor and maps an event seq onto its created_at timestamp (spec
// §8.4). Snapshot's Cursor is the channel's last_seq even when the page is empty.
type EventCursorProvider struct {
	events EventSnapshotReader
}

// NewEventCursorProvider wires the eventing snapshot reader.
func NewEventCursorProvider(events EventSnapshotReader) *EventCursorProvider {
	return &EventCursorProvider{events: events}
}

var (
	_ chainapp.CursorReader    = (*EventCursorProvider)(nil)
	_ chainapp.EventTimeReader = (*EventCursorProvider)(nil)
)

// LatestCursor returns the channel's latest seq.
func (p *EventCursorProvider) LatestCursor(ctx context.Context, channel string) (int64, error) {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return 0, platform.Invalid("channel", "must not be empty")
	}
	snapshot, err := p.events.Snapshot(ctx, channel, 0, 1)
	if err != nil {
		return 0, err
	}
	return snapshot.Cursor, nil
}

// EventTime returns the created_at of the event at seq. Seq is one-based and
// channel-local (eventing append order). The eventing snapshot API cannot seek
// to the first event with an after cursor because after == 0 means "newest
// page", so seq 1 falls back to scanning the newest page, which contains the
// first event while the channel holds at most eventingapp.MaxSnapshotLimit
// events. An out-of-range or unreadable seq reports found=false.
func (p *EventCursorProvider) EventTime(ctx context.Context, channel string, seq int64) (time.Time, bool, error) {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return time.Time{}, false, platform.Invalid("channel", "must not be empty")
	}
	if seq <= 0 {
		return time.Time{}, false, platform.Invalid("seq", "must be a positive event sequence")
	}
	if seq == 1 {
		snapshot, err := p.events.Snapshot(ctx, channel, 0, eventingapp.MaxSnapshotLimit)
		if err != nil {
			return time.Time{}, false, err
		}
		for _, event := range snapshot.Data {
			if event.Seq == seq {
				return event.CreatedAt, true, nil
			}
		}
		return time.Time{}, false, nil
	}
	snapshot, err := p.events.Snapshot(ctx, channel, seq-1, 1)
	if err != nil {
		return time.Time{}, false, err
	}
	for _, event := range snapshot.Data {
		if event.Seq == seq {
			return event.CreatedAt, true, nil
		}
	}
	return time.Time{}, false, nil
}
