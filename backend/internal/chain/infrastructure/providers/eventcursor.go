package providers

import (
	"context"
	"strings"

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
// snapshot cursor (spec §8.4). Snapshot's Cursor is the channel's last_seq even
// when the page is empty.
type EventCursorProvider struct {
	events EventSnapshotReader
}

// NewEventCursorProvider wires the eventing snapshot reader.
func NewEventCursorProvider(events EventSnapshotReader) *EventCursorProvider {
	return &EventCursorProvider{events: events}
}

var _ chainapp.CursorReader = (*EventCursorProvider)(nil)

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
