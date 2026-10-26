package infrastructure_test

import (
	"testing"
	"time"

	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/eventing/infrastructure"
)

// TestHubDeliversPerChannel proves an event reaches only the subscribers of its
// own channel, in publish order.
func TestHubDeliversPerChannel(t *testing.T) {
	hub := infrastructure.NewHub()
	system, cancelSystem := hub.Subscribe("system")
	defer cancelSystem()
	other, cancelOther := hub.Subscribe("version:1")
	defer cancelOther()

	for seq := int64(1); seq <= 3; seq++ {
		hub.Publish(eventing.Event{Channel: "system", Seq: seq, Type: "system.test"})
	}
	if got := len(system); got != 3 {
		t.Fatalf("system buffered=%d want 3", got)
	}
	if got := len(other); got != 0 {
		t.Fatalf("other channel buffered=%d want 0", got)
	}
	for want := int64(1); want <= 3; want++ {
		event := <-system
		if event.Seq != want {
			t.Fatalf("seq=%d want %d (publish order must be preserved)", event.Seq, want)
		}
	}
}

// TestHubUnsubscribeStopsDelivery proves cancel removes the subscriber, closes
// its channel and is idempotent.
func TestHubUnsubscribeStopsDelivery(t *testing.T) {
	hub := infrastructure.NewHub()
	events, cancel := hub.Subscribe("system")
	cancel()
	cancel() // idempotent

	hub.Publish(eventing.Event{Channel: "system", Seq: 1})
	if _, ok := <-events; ok {
		t.Fatal("canceled subscriber received an event")
	}
}

// TestHubFansOutToEverySubscriber proves the hub broadcasts to all subscribers
// of a channel.
func TestHubFansOutToEverySubscriber(t *testing.T) {
	hub := infrastructure.NewHub()
	first, cancelFirst := hub.Subscribe("system")
	defer cancelFirst()
	second, cancelSecond := hub.Subscribe("system")
	defer cancelSecond()

	hub.Publish(eventing.Event{Channel: "system", Seq: 7})
	for i, events := range [](<-chan eventing.Event){first, second} {
		select {
		case event := <-events:
			if event.Seq != 7 {
				t.Fatalf("subscriber %d seq=%d want 7", i, event.Seq)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d received nothing", i)
		}
	}
}

// TestHubDisconnectsSlowConsumer proves the per-subscriber buffer is bounded:
// a subscriber that stops reading is disconnected (channel closed) instead of
// making Publish block or growing memory without limit.
func TestHubDisconnectsSlowConsumer(t *testing.T) {
	hub := infrastructure.NewHub()
	events, cancel := hub.Subscribe("system")
	defer cancel()

	for seq := int64(1); seq <= 1000; seq++ {
		hub.Publish(eventing.Event{Channel: "system", Seq: seq})
	}

	buffered := 0
	for {
		select {
		case _, ok := <-events:
			if !ok {
				if buffered > 256 {
					t.Fatalf("slow consumer buffered %d events, want <= 256", buffered)
				}
				// Publishing to a hub with no subscriber left, and canceling an
				// already-disconnected subscription, must stay safe.
				hub.Publish(eventing.Event{Channel: "system", Seq: 1001})
				cancel()
				return
			}
			buffered++
		default:
			t.Fatalf("slow consumer was not disconnected after %d buffered events", buffered)
		}
	}
}

// TestHubPublishWithoutSubscribers does not block or panic when nobody listens.
func TestHubPublishWithoutSubscribers(t *testing.T) {
	hub := infrastructure.NewHub()
	done := make(chan struct{})
	go func() {
		defer close(done)
		hub.Publish(eventing.Event{Channel: "system", Seq: 1})
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish blocked without subscribers")
	}
}
