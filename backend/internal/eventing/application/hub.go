package eventingapp

import (
	"sync"

	"github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
)

// DefaultHubBuffer is the per-subscriber queue depth. A subscriber that falls
// this far behind is disconnected instead of slowing publishers down.
const DefaultHubBuffer = 64

// Hub is the P0 in-process event fan-out. It is not durable and assumes a
// single API instance; cross-instance fan-out belongs to P7.
type Hub struct {
	mu          sync.Mutex
	buffer      int
	nextID      int
	subscribers map[string]map[int]*hubSubscriber
}

type hubSubscriber struct {
	ch chan eventing.Event
}

// NewHub builds a hub with the given per-subscriber buffer.
func NewHub(buffer int) *Hub {
	if buffer <= 0 {
		buffer = DefaultHubBuffer
	}
	return &Hub{
		buffer:      buffer,
		subscribers: make(map[string]map[int]*hubSubscriber),
	}
}

// Subscribe registers a channel subscriber. cancel is safe to call twice and
// closes the returned channel.
func (h *Hub) Subscribe(channel string) (<-chan eventing.Event, func()) {
	h.mu.Lock()
	if h.subscribers[channel] == nil {
		h.subscribers[channel] = make(map[int]*hubSubscriber)
	}
	h.nextID++
	id := h.nextID
	sub := &hubSubscriber{ch: make(chan eventing.Event, h.buffer)}
	h.subscribers[channel][id] = sub
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() { h.remove(channel, id) })
	}
	return sub.ch, cancel
}

func (h *Hub) remove(channel string, id int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	subscribers, ok := h.subscribers[channel]
	if !ok {
		return
	}
	sub, ok := subscribers[id]
	if !ok {
		return
	}
	delete(subscribers, id)
	if len(subscribers) == 0 {
		delete(h.subscribers, channel)
	}
	close(sub.ch)
}

// Broadcast delivers an event to every subscriber of its channel without
// blocking. A subscriber whose buffer is full is dropped and its channel is
// closed.
func (h *Hub) Broadcast(event eventing.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	subscribers, ok := h.subscribers[event.Channel]
	if !ok {
		return
	}
	for id, sub := range subscribers {
		select {
		case sub.ch <- event:
		default:
			delete(subscribers, id)
			close(sub.ch)
		}
	}
	if len(subscribers) == 0 {
		delete(h.subscribers, event.Channel)
	}
}

// BroadcastLoaded delivers an event that was loaded from the persisted log by
// the cross-instance listener. It shares the same subscriber fan-out as
// Broadcast; the distinct name documents that the event was not published by
// this process (no new DB row and no NOTIFY).
func (h *Hub) BroadcastLoaded(event eventing.Event) {
	h.Broadcast(event)
}
