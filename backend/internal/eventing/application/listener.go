package eventingapp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// NotifyChannel is the PostgreSQL NOTIFY channel used to fan events out across
// API instances.
const NotifyChannel = "eztest_events"

// RecentEventLimit bounds the cross-instance de-duplication cache. The listener
// only needs to absorb duplicate NOTIFY deliveries in a short window; SSE
// replay remains the durable catch-up path.
const RecentEventLimit = 4096

// EventRef is the persisted identity of one event: the event table primary key
// (channel, seq). P0's event table has no global id column, so P7 carries this
// pair (JSON encoded) in the NOTIFY payload and re-reads the row before
// delivering it to the local hub. Keeping the pair — instead of seq alone —
// keeps the reference unambiguous across channels.
type EventRef struct {
	Channel string `json:"channel"`
	Seq     int64  `json:"seq"`
}

// NewEventRef builds the persisted reference of a stored event.
func NewEventRef(event eventing.Event) EventRef {
	return EventRef{Channel: event.Channel, Seq: event.Seq}
}

// Key returns the in-memory de-duplication key of this reference.
func (r EventRef) Key() string {
	return r.Channel + "\x00" + strconv.FormatInt(r.Seq, 10)
}

// EncodeEventRef renders the NOTIFY payload.
func EncodeEventRef(ref EventRef) (string, error) {
	encoded, err := json.Marshal(ref)
	if err != nil {
		return "", fmt.Errorf("encode event ref: %w", err)
	}
	return string(encoded), nil
}

// DecodeEventRef parses and validates a NOTIFY payload.
func DecodeEventRef(payload string) (EventRef, error) {
	var ref EventRef
	if err := json.Unmarshal([]byte(payload), &ref); err != nil {
		return EventRef{}, fmt.Errorf("decode event ref: %w", err)
	}
	if !ValidChannel(ref.Channel) {
		return EventRef{}, platform.Invalid("channel", "unsupported event channel")
	}
	if ref.Seq <= 0 {
		return EventRef{}, platform.Invalid("seq", "event sequence must be positive")
	}
	return ref, nil
}

// Notifier publishes the persisted event reference to the other API
// instances. PostgreSQL implements it with pg_notify('eztest_events', ...).
// Notification loss is tolerated: SSE clients catch up through Last-Event-ID
// replay, so Publish treats a notifier failure as best-effort.
type Notifier interface {
	Notify(ctx context.Context, ref EventRef) error
}

// EventReader loads one persisted event by its channel cursor. The PostgreSQL
// repository implements it; missing events are reported as eventing.ErrNotFound.
type EventReader interface {
	Get(ctx context.Context, channel string, seq int64) (eventing.Event, error)
}

// LocalBroadcaster delivers an event already loaded from the persisted log to
// the in-process subscribers. Hub implements it.
type LocalBroadcaster interface {
	BroadcastLoaded(event eventing.Event)
}

// EventObserver is notified after a persisted event has been broadcast to the
// local hub. The settings service uses it to reload its per-process cache when
// another replica publishes settings.updated. The observer runs after broadcast
// so a slow/failing observer never delays SSE delivery.
type EventObserver func(context.Context, eventing.Event) error

// DispatcherOption customizes a Dispatcher.
type DispatcherOption func(*Dispatcher)

// WithDispatcherObserver installs an observer invoked once per delivered event.
func WithDispatcherObserver(observer EventObserver) DispatcherOption {
	return func(d *Dispatcher) {
		d.observer = observer
	}
}

// Dispatcher turns cross-instance NOTIFY payloads into local hub deliveries:
// decode the reference, de-duplicate repeated notifications, load the row from
// the event log (DB is the truth) and broadcast it. It performs no I/O other
// than the repository load, so it is unit-testable without PostgreSQL.
type Dispatcher struct {
	events   EventReader
	sink     LocalBroadcaster
	observer EventObserver
	recent   *recentEvents
}

// NewDispatcher wires a dispatcher. A nil reader or sink makes Handle fail
// without panicking (the listener logs and retries on the next notification).
func NewDispatcher(events EventReader, sink LocalBroadcaster, options ...DispatcherOption) *Dispatcher {
	dispatcher := &Dispatcher{events: events, sink: sink, recent: newRecentEvents(RecentEventLimit)}
	for _, option := range options {
		option(dispatcher)
	}
	return dispatcher
}

// Handle processes one NOTIFY payload. Duplicate references are ignored;
// malformed payloads, unknown rows and load failures are returned so the
// listener can log them.
func (d *Dispatcher) Handle(ctx context.Context, payload string) error {
	if d == nil || d.events == nil || d.sink == nil {
		return platform.Unavailable("event listener is not configured")
	}
	ref, err := DecodeEventRef(payload)
	if err != nil {
		return err
	}
	if d.recent.contains(ref.Key()) {
		return nil
	}
	event, err := d.events.Get(ctx, ref.Channel, ref.Seq)
	if err != nil {
		return err
	}
	d.sink.BroadcastLoaded(event)
	d.recent.add(ref.Key())
	if d.observer != nil {
		if err := d.observer(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

// recentEvents is a bounded insertion-ordered set. When it overflows the
// oldest key is evicted, which only means an abnormally delayed duplicate could
// be delivered twice; subscribers already de-duplicate by (channel, seq) and
// the SSE replay path is authoritative.
type recentEvents struct {
	capacity int
	order    []string
	set      map[string]struct{}
}

func newRecentEvents(capacity int) *recentEvents {
	if capacity <= 0 {
		capacity = RecentEventLimit
	}
	return &recentEvents{capacity: capacity, set: make(map[string]struct{}, capacity)}
}

func (r *recentEvents) contains(key string) bool {
	_, ok := r.set[key]
	return ok
}

func (r *recentEvents) add(key string) {
	if _, ok := r.set[key]; ok {
		return
	}
	if len(r.order) >= r.capacity {
		oldest := r.order[0]
		r.order = r.order[1:]
		delete(r.set, oldest)
	}
	r.order = append(r.order, key)
	r.set[key] = struct{}{}
}
