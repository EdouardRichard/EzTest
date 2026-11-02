//go:build integration

package eventingpg_test

import (
	"context"
	"errors"
	"testing"
	"time"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	eventingpg "github.com/EdouardRichard/EzTest/backend/internal/eventing/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// waitReady blocks until a listener has registered its LISTEN, so the test
// does not race the first publication.
func waitReady(t *testing.T, listener *eventingpg.Listener) {
	t.Helper()
	select {
	case <-listener.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("listener did not become ready")
	}
}

func receive(t *testing.T, events <-chan eventing.Event, what string) eventing.Event {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: no event delivered", what)
		return eventing.Event{}
	}
}

func assertNoEvent(t *testing.T, events <-chan eventing.Event, what string) {
	t.Helper()
	select {
	case event := <-events:
		t.Fatalf("%s: unexpected duplicate event %#v", what, event)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestTwoListenersReceiveOnePublishedEventAndDropDuplicateNotify(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := eventingpg.New(pool)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// One API replica publishes; two other replicas have their own local hub
	// and listen on the shared NOTIFY channel.
	publisherHub := eventingapp.NewHub(8)
	service := eventingapp.New(repo, publisherHub, eventingpg.NewNotifier(pool))

	hubA := eventingapp.NewHub(8)
	hubB := eventingapp.NewHub(8)
	listenerA := eventingpg.NewListener(pool, repo, hubA, eventingpg.WithListenerRetry(50*time.Millisecond))
	listenerB := eventingpg.NewListener(pool, repo, hubB, eventingpg.WithListenerRetry(50*time.Millisecond))

	runErr := make(chan error, 2)
	for _, listener := range []*eventingpg.Listener{listenerA, listenerB} {
		go func(l *eventingpg.Listener) { runErr <- l.Run(ctx) }(listener)
	}
	waitReady(t, listenerA)
	waitReady(t, listenerB)

	eventsA, cancelA := hubA.Subscribe("system")
	defer cancelA()
	eventsB, cancelB := hubB.Subscribe("system")
	defer cancelB()

	if err := service.Publish(ctx, eventingapp.Event{
		Channel: "system",
		Type:    "instance.registered",
		Payload: map[string]any{"instance_id": "100", "node_id": 2, "instance_key": "host-a:42"},
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	eventA := receive(t, eventsA, "hub A")
	eventB := receive(t, eventsB, "hub B")
	if eventA.Type != "instance.registered" || eventA.Seq <= 0 {
		t.Fatalf("hub A event = %#v", eventA)
	}
	if eventA.Channel != "system" || eventA.Seq != eventB.Seq || eventA.Type != eventB.Type {
		t.Fatalf("hubs saw different events: A=%#v B=%#v", eventA, eventB)
	}
	if eventA.Payload["instance_key"] != "host-a:42" {
		t.Fatalf("payload = %#v", eventA.Payload)
	}

	// A duplicate NOTIFY for an already-delivered (channel, seq) must not be
	// delivered twice by either listener.
	payload, err := eventingapp.EncodeEventRef(eventingapp.NewEventRef(eventA))
	if err != nil {
		t.Fatalf("EncodeEventRef: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := pool.Exec(ctx, `SELECT pg_notify($1, $2)`, eventingapp.NotifyChannel, payload); err != nil {
			t.Fatalf("duplicate pg_notify: %v", err)
		}
	}
	assertNoEvent(t, eventsA, "hub A")
	assertNoEvent(t, eventsB, "hub B")

	cancel()
	for i := 0; i < 2; i++ {
		select {
		case err := <-runErr:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Fatalf("listener Run = %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("listener did not stop after cancel")
		}
	}
}

func TestListenerObserverFiresForCrossInstanceNotification(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := eventingpg.New(pool)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hub := eventingapp.NewHub(8)
	observed := make(chan eventing.Event, 8)
	listener := eventingpg.NewListener(pool, repo, hub,
		eventingpg.WithListenerRetry(50*time.Millisecond),
		eventingpg.WithListenerObserver(func(_ context.Context, event eventing.Event) error {
			observed <- event
			return nil
		}))

	runErr := make(chan error, 1)
	go func() { runErr <- listener.Run(ctx) }()
	waitReady(t, listener)

	service := eventingapp.New(repo, eventingapp.NewHub(8), eventingpg.NewNotifier(pool))
	if err := service.Publish(ctx, eventingapp.Event{
		Channel: "system",
		Type:    "settings.updated",
		Payload: map[string]any{"key": "retention.gc_dry_run"},
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case event := <-observed:
		if event.Type != "settings.updated" || event.Seq <= 0 {
			t.Fatalf("observed event = %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("listener observer did not receive the event")
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("listener Run = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("listener did not stop after cancel")
	}
}
