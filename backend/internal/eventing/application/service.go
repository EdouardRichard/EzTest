package eventingapp

import (
	"context"
	"regexp"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

const (
	// DefaultSnapshotLimit is used when a snapshot request omits limit.
	DefaultSnapshotLimit = 50
	// MaxSnapshotLimit caps the snapshot page size.
	MaxSnapshotLimit = 200
)

var channelPattern = regexp.MustCompile(`^(?:system|version:[1-9][0-9]*|user:[1-9][0-9]*)$`)

// ValidChannel reports whether channel is one of the P0 channels:
// system, version:{id} or user:{id}.
func ValidChannel(channel string) bool {
	return channelPattern.MatchString(channel)
}

// Snapshot is one page of a channel's event log.
type Snapshot struct {
	Data    []eventing.Event
	Cursor  int64
	HasMore bool
}

// Service publishes events and serves channel snapshots/subscriptions.
type Service struct {
	repo     eventing.Repository
	hub      *Hub
	notifier Notifier
}

// New wires the event service to its repository and in-process hub. The
// notifier is optional so P0-P6 deployments (and unit tests) keep working
// without cross-instance fan-out; when set, every append is followed by a
// best-effort NOTIFY carrying the persisted (channel, seq) reference.
func New(repo eventing.Repository, hub *Hub, notifiers ...Notifier) *Service {
	if hub == nil {
		hub = NewHub(DefaultHubBuffer)
	}
	var notifier Notifier
	if len(notifiers) > 0 {
		notifier = notifiers[0]
	}
	return &Service{repo: repo, hub: hub, notifier: notifier}
}

// Publish appends an event in its channel and broadcasts it after the append
// commits. Channel and type are validated before touching the repository.
// Cross-instance notification is best-effort: NOTIFY is delivered on commit,
// but if it is lost (or the notifier fails) SSE clients recover through
// Last-Event-ID replay.
func (s *Service) Publish(ctx context.Context, event Event) error {
	if !ValidChannel(event.Channel) {
		return platform.Invalid("channel", "unsupported event channel")
	}
	if strings.TrimSpace(event.Type) == "" {
		return platform.Invalid("type", "event type is required")
	}
	stored, err := s.repo.Append(ctx, eventing.Event{
		Channel:   event.Channel,
		Type:      event.Type,
		Scope:     event.Scope,
		Payload:   event.Payload,
		ActorID:   event.ActorID,
		RequestID: event.RequestID,
	})
	if err != nil {
		return err
	}
	s.hub.Broadcast(stored)
	if s.notifier != nil {
		_ = s.notifier.Notify(ctx, NewEventRef(stored))
	}
	return nil
}

// Snapshot returns a channel page. after == 0 selects the newest page.
func (s *Service) Snapshot(ctx context.Context, channel string, after int64, limit int) (Snapshot, error) {
	if !ValidChannel(channel) {
		return Snapshot{}, platform.Invalid("channel", "unsupported event channel")
	}
	if after < 0 {
		return Snapshot{}, platform.Invalid("after", "after must be zero or a positive cursor")
	}
	if limit <= 0 {
		limit = DefaultSnapshotLimit
	}
	if limit > MaxSnapshotLimit {
		limit = MaxSnapshotLimit
	}
	page, err := s.repo.Snapshot(ctx, channel, after, limit)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Data: page.Events, Cursor: page.Cursor, HasMore: page.HasMore}, nil
}

// Bounds returns the persisted cursor range for a channel.
func (s *Service) Bounds(ctx context.Context, channel string) (eventing.Bounds, error) {
	if !ValidChannel(channel) {
		return eventing.Bounds{}, platform.Invalid("channel", "unsupported event channel")
	}
	return s.repo.Bounds(ctx, channel)
}

// Subscribe registers an in-process subscriber for the channel. The returned
// cancel function is idempotent and closes the channel.
func (s *Service) Subscribe(channel string) (<-chan eventing.Event, func()) {
	if !ValidChannel(channel) {
		closed := make(chan eventing.Event)
		close(closed)
		return closed, func() {}
	}
	return s.hub.Subscribe(channel)
}
