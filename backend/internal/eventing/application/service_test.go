package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
)

var ctx = context.Background()

// TestServicePublishBroadcastsCommittedEvent proves the service broadcasts only
// the event the store committed, and surfaces store failures without
// broadcasting anything.
func TestServicePublishBroadcastsCommittedEvent(t *testing.T) {
	store := &fakeStore{nextSeq: 41}
	broker := &fakeBroker{}
	svc := application.NewService(store, broker)

	published, err := svc.Publish(ctx, eventing.NewEvent{Channel: "system", Type: "system.test", Summary: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if published.Seq != 42 {
		t.Fatalf("seq=%d want the store-assigned 42", published.Seq)
	}
	if len(store.published) != 1 || store.published[0].Summary != "hello" {
		t.Fatalf("store published=%+v", store.published)
	}
	if len(broker.published) != 1 || broker.published[0].Seq != 42 {
		t.Fatalf("broadcast=%+v", broker.published)
	}

	store.publishErr = errors.New("database down")
	if _, err := svc.Publish(ctx, eventing.NewEvent{Channel: "system", Type: "system.test"}); err == nil {
		t.Fatal("publish error was swallowed")
	}
	if len(broker.published) != 1 {
		t.Fatalf("failed publish was broadcast: %+v", broker.published)
	}
}

// TestServicePublishWithoutBroker keeps the service usable in wiring that does
// not stream (nil broker means no live subscribers).
func TestServicePublishWithoutBroker(t *testing.T) {
	store := &fakeStore{nextSeq: 1}
	svc := application.NewService(store, nil)
	if _, err := svc.Publish(ctx, eventing.NewEvent{Channel: "system", Type: "system.test"}); err != nil {
		t.Fatal(err)
	}
}

// TestServicePublishRejectsInvalidEnvelope proves the §7.2 limits are enforced
// before the store is touched: an over-limit event is neither persisted nor
// broadcast.
func TestServicePublishRejectsInvalidEnvelope(t *testing.T) {
	store := &fakeStore{nextSeq: 1}
	broker := &fakeBroker{}
	svc := application.NewService(store, broker)

	_, err := svc.Publish(ctx, eventing.NewEvent{
		Channel: "system",
		Type:    "system.test",
		Summary: strings.Repeat("通", 501),
	})
	if !errors.Is(err, eventing.ErrInvalidEvent) {
		t.Fatalf("Publish error=%v, want ErrInvalidEvent", err)
	}
	if len(store.published) != 0 {
		t.Fatalf("invalid event reached the store: %+v", store.published)
	}
	if len(broker.published) != 0 {
		t.Fatalf("invalid event was broadcast: %+v", broker.published)
	}
}

// TestServiceSnapshot proves the snapshot use case composes ListBefore with the
// channel's latest seq, and propagates an unknown channel.
func TestServiceSnapshot(t *testing.T) {
	store := &fakeStore{
		latestSeq:  7,
		before:     []eventing.Event{{Seq: 5}, {Seq: 6}},
		beforeMore: true,
	}
	svc := application.NewService(store, &fakeBroker{})

	items, cursor, more, err := svc.Snapshot(ctx, "system", 7, 2)
	if err != nil {
		t.Fatal(err)
	}
	if cursor != 7 || !more || len(items) != 2 || items[0].Seq != 5 || items[1].Seq != 6 {
		t.Fatalf("items=%+v cursor=%d more=%v", items, cursor, more)
	}
	if store.gotBeforeSeq != 7 || store.gotLimit != 2 {
		t.Fatalf("store got before_seq=%d limit=%d want 7/2", store.gotBeforeSeq, store.gotLimit)
	}

	store = &fakeStore{latestErr: eventing.ErrChannelNotFound}
	svc = application.NewService(store, &fakeBroker{})
	if _, _, _, err := svc.Snapshot(ctx, "nope", 0, 20); !errors.Is(err, eventing.ErrChannelNotFound) {
		t.Fatalf("err=%v want ErrChannelNotFound", err)
	}
	if store.gotBeforeSeq != 0 || store.gotLimit != 0 {
		t.Fatal("snapshot queried events for an unknown channel")
	}
}

// TestServiceSubscribe proves the live subscriptions are delegated to the hub
// unchanged.
func TestServiceSubscribe(t *testing.T) {
	broker := &fakeBroker{}
	svc := application.NewService(&fakeStore{}, broker)

	events, cancel := svc.Subscribe("system")
	if broker.subscribedChannel != "system" {
		t.Fatalf("hub subscribed to %q", broker.subscribedChannel)
	}
	if events == nil {
		t.Fatal("Subscribe returned a nil channel")
	}
	cancel()
	if !broker.canceled {
		t.Fatal("cancel did not reach the hub subscription")
	}
}

// fakeStore is an in-memory eventing.Store that records calls.
type fakeStore struct {
	published    []eventing.NewEvent
	publishErr   error
	nextSeq      int64
	latestSeq    int64
	latestErr    error
	before       []eventing.Event
	beforeMore   bool
	beforeErr    error
	gotBeforeSeq int64
	gotLimit     int
	after        []eventing.Event
	afterErr     error
	gotAfterSeq  int64
}

func (s *fakeStore) Publish(_ context.Context, event eventing.NewEvent) (eventing.Event, error) {
	if s.publishErr != nil {
		return eventing.Event{}, s.publishErr
	}
	s.published = append(s.published, event)
	s.nextSeq++
	return eventing.Event{Seq: s.nextSeq, Channel: event.Channel, Type: event.Type, TS: event.TS}, nil
}

func (s *fakeStore) ListAfter(_ context.Context, _ string, afterSeq int64, limit int) ([]eventing.Event, error) {
	s.gotAfterSeq = afterSeq
	s.gotLimit = limit
	return s.after, s.afterErr
}

func (s *fakeStore) ListBefore(_ context.Context, _ string, beforeSeq int64, limit int) ([]eventing.Event, bool, error) {
	s.gotBeforeSeq = beforeSeq
	s.gotLimit = limit
	return s.before, s.beforeMore, s.beforeErr
}

func (s *fakeStore) LatestSeq(context.Context, string) (int64, error) {
	return s.latestSeq, s.latestErr
}

// fakeBroker is an in-memory application.Broker that records calls.
type fakeBroker struct {
	published         []eventing.Event
	subscribedChannel string
	canceled          bool
}

func (b *fakeBroker) Publish(event eventing.Event) {
	b.published = append(b.published, event)
}

func (b *fakeBroker) Subscribe(channel string) (<-chan eventing.Event, func()) {
	b.subscribedChannel = channel
	events := make(chan eventing.Event, 1)
	return events, func() {
		b.canceled = true
		close(events)
	}
}
