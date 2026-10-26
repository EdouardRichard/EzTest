// Package infrastructure provides the eventing context's adapters: the
// PostgreSQL event store lives in the postgres subpackage and the in-process
// live delivery hub lives here.
package infrastructure

import (
	"sync"

	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
)

// hubBuffer is the per-subscriber delivery buffer. A subscriber that falls this
// far behind is disconnected instead of slowing publishers down or growing
// memory without bound; the client reconnects with Last-Event-ID and replays
// from the persistent store.
const hubBuffer = 256

// Hub is a single-instance, in-process fan-out of committed events. P0 runs one
// process, so no cross-instance broadcast is needed.
type Hub struct {
	mu          sync.Mutex
	subscribers map[string]map[int64]chan eventing.Event
	nextID      int64
}

// NewHub returns an empty hub.
func NewHub() *Hub {
	return &Hub{subscribers: make(map[string]map[int64]chan eventing.Event)}
}

// Subscribe registers a buffered subscriber for channel. The returned cancel
// function removes the subscription and closes the channel; it is idempotent.
// A subscriber that stops draining its channel is disconnected by Publish,
// which also closes the channel.
func (h *Hub) Subscribe(channel string) (<-chan eventing.Event, func()) {
	events := make(chan eventing.Event, hubBuffer)

	h.mu.Lock()
	h.nextID++
	id := h.nextID
	subscribers := h.subscribers[channel]
	if subscribers == nil {
		subscribers = make(map[int64]chan eventing.Event)
		h.subscribers[channel] = subscribers
	}
	subscribers[id] = events
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.removeLocked(channel, id)
		})
	}
	return events, cancel
}

// Publish broadcasts a committed event to every subscriber of its channel. It
// never blocks: a full subscriber buffer disconnects that subscriber.
func (h *Hub) Publish(event eventing.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()

	subscribers := h.subscribers[event.Channel]
	for id, events := range subscribers {
		select {
		case events <- event:
		default:
			// Slow consumer: disconnect. The client replays from its
			// Last-Event-ID, so dropping this subscriber loses nothing.
			delete(subscribers, id)
			close(events)
		}
	}
	if len(subscribers) == 0 {
		delete(h.subscribers, event.Channel)
	}
}

// removeLocked removes one subscription and closes its channel. The caller
// holds h.mu. A subscription already removed (by cancel racing Publish) is left
// untouched.
func (h *Hub) removeLocked(channel string, id int64) {
	subscribers := h.subscribers[channel]
	events, ok := subscribers[id]
	if !ok {
		return
	}
	delete(subscribers, id)
	if len(subscribers) == 0 {
		delete(h.subscribers, channel)
	}
	close(events)
}
