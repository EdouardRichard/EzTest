package eventingapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
)

type fakeEventReader struct {
	events   map[string]eventing.Event
	err      error
	calls    int
	lastCh   string
	lastSeq  int64
	returned []eventing.Event
}

func (f *fakeEventReader) Get(_ context.Context, channel string, seq int64) (eventing.Event, error) {
	f.calls++
	f.lastCh, f.lastSeq = channel, seq
	if f.err != nil {
		return eventing.Event{}, f.err
	}
	event, ok := f.events[channel+"|"+itoa(seq)]
	if !ok {
		return eventing.Event{}, eventing.ErrNotFound
	}
	f.returned = append(f.returned, event)
	return event, nil
}

type fakeBroadcaster struct {
	events []eventing.Event
}

func (f *fakeBroadcaster) BroadcastLoaded(event eventing.Event) {
	f.events = append(f.events, event)
}

func itoa(value int64) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

func fixtureEvent(seq int64) eventing.Event {
	return eventing.Event{
		Channel:   "system",
		Seq:       seq,
		Type:      "settings.updated",
		Payload:   map[string]any{"key": "instance.heartbeat_seconds"},
		RequestID: "req_1",
	}
}

func storedRef(t *testing.T, event eventing.Event) string {
	t.Helper()
	payload, err := EncodeEventRef(NewEventRef(event))
	if err != nil {
		t.Fatalf("EncodeEventRef: %v", err)
	}
	return payload
}

func TestDispatcherLoadsAndBroadcastsOnce(t *testing.T) {
	event := fixtureEvent(7)
	reader := &fakeEventReader{events: map[string]eventing.Event{"system|7": event}}
	sink := &fakeBroadcaster{}
	dispatcher := NewDispatcher(reader, sink)
	payload := storedRef(t, event)

	if err := dispatcher.Handle(context.Background(), payload); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(sink.events) != 1 || sink.events[0].Seq != 7 || sink.events[0].Type != "settings.updated" {
		t.Fatalf("broadcast events = %#v", sink.events)
	}
	if reader.calls != 1 || reader.lastCh != "system" || reader.lastSeq != 7 {
		t.Fatalf("reader calls=%d ch=%q seq=%d", reader.calls, reader.lastCh, reader.lastSeq)
	}

	// The same notification must not be delivered twice.
	if err := dispatcher.Handle(context.Background(), payload); err != nil {
		t.Fatalf("second Handle: %v", err)
	}
	if len(sink.events) != 1 {
		t.Fatalf("duplicate delivered: %d events", len(sink.events))
	}
	if reader.calls != 1 {
		t.Fatalf("duplicate caused %d loads, want 1", reader.calls)
	}
}

func TestDispatcherDedupeCacheIsBounded(t *testing.T) {
	reader := &fakeEventReader{events: map[string]eventing.Event{}}
	sink := &fakeBroadcaster{}
	dispatcher := NewDispatcher(reader, sink)

	deliver := func(seq int64) {
		t.Helper()
		event := fixtureEvent(seq)
		reader.events["system|"+itoa(seq)] = event
		if err := dispatcher.Handle(context.Background(), storedRef(t, event)); err != nil {
			t.Fatalf("Handle(%d): %v", seq, err)
		}
	}
	for seq := int64(1); seq <= RecentEventLimit+1; seq++ {
		deliver(seq)
	}
	if len(sink.events) != RecentEventLimit+1 {
		t.Fatalf("delivered %d events, want %d", len(sink.events), RecentEventLimit+1)
	}
	// The oldest id has been evicted, so a redelivery is accepted again; the
	// next-oldest is now the oldest and still cached.
	deliver(1)
	if len(sink.events) != RecentEventLimit+2 {
		t.Fatalf("evicted id was not redelivered: %d events", len(sink.events))
	}
	reader.calls = 0
	event := fixtureEvent(3)
	if err := dispatcher.Handle(context.Background(), storedRef(t, event)); err != nil {
		t.Fatalf("Handle(3): %v", err)
	}
	if reader.calls != 0 {
		t.Fatalf("still-cached id reloaded %d times", reader.calls)
	}
}

func TestDispatcherRejectsMalformedPayloads(t *testing.T) {
	reader := &fakeEventReader{events: map[string]eventing.Event{}}
	sink := &fakeBroadcaster{}
	dispatcher := NewDispatcher(reader, sink)

	payloads := []string{
		"",
		"not-json",
		`{"channel":"bogus","seq":1}`,
		`{"channel":"system","seq":0}`,
		`{"channel":"system","seq":-3}`,
		`{"channel":"","seq":1}`,
	}
	for _, payload := range payloads {
		if err := dispatcher.Handle(context.Background(), payload); err == nil {
			t.Fatalf("payload %q: expected error", payload)
		}
	}
	if reader.calls != 0 || len(sink.events) != 0 {
		t.Fatalf("malformed payload reached reader/sink: calls=%d events=%d", reader.calls, len(sink.events))
	}
}

func TestDispatcherRetriesFailedLoad(t *testing.T) {
	wantErr := errors.New("db down")
	reader := &fakeEventReader{err: wantErr, events: map[string]eventing.Event{}}
	sink := &fakeBroadcaster{}
	dispatcher := NewDispatcher(reader, sink)
	event := fixtureEvent(3)
	payload := storedRef(t, event)

	if err := dispatcher.Handle(context.Background(), payload); !errors.Is(err, wantErr) {
		t.Fatalf("Handle error = %v, want %v", err, wantErr)
	}
	if len(sink.events) != 0 {
		t.Fatalf("failed load broadcast %d events", len(sink.events))
	}

	// A failed load must not poison the dedupe cache: the next notification for
	// the same id retries and succeeds.
	reader.err = nil
	reader.events["system|3"] = event
	if err := dispatcher.Handle(context.Background(), payload); err != nil {
		t.Fatalf("retry Handle: %v", err)
	}
	if len(sink.events) != 1 || sink.events[0].Seq != 3 {
		t.Fatalf("retry events = %#v", sink.events)
	}
}

func TestDispatcherReportsUnknownEvent(t *testing.T) {
	reader := &fakeEventReader{events: map[string]eventing.Event{}}
	sink := &fakeBroadcaster{}
	dispatcher := NewDispatcher(reader, sink)
	event := fixtureEvent(9)
	if err := dispatcher.Handle(context.Background(), storedRef(t, event)); !errors.Is(err, eventing.ErrNotFound) {
		t.Fatalf("Handle error = %v, want ErrNotFound", err)
	}
	if len(sink.events) != 0 {
		t.Fatalf("unknown event broadcast %d events", len(sink.events))
	}
}

func TestEventRefRoundTrip(t *testing.T) {
	ref := NewEventRef(eventing.Event{Channel: "version:42", Seq: 17})
	payload, err := EncodeEventRef(ref)
	if err != nil {
		t.Fatalf("EncodeEventRef: %v", err)
	}
	decoded, err := DecodeEventRef(payload)
	if err != nil {
		t.Fatalf("DecodeEventRef: %v", err)
	}
	if decoded != ref {
		t.Fatalf("decoded = %#v, want %#v", decoded, ref)
	}
}

func TestDispatcherObserverReceivesLoadedEventOnce(t *testing.T) {
	event := fixtureEvent(8)
	reader := &fakeEventReader{events: map[string]eventing.Event{"system|8": event}}
	sink := &fakeBroadcaster{}
	var observed []eventing.Event
	dispatcher := NewDispatcher(reader, sink, WithDispatcherObserver(func(_ context.Context, got eventing.Event) error {
		observed = append(observed, got)
		return nil
	}))
	payload := storedRef(t, event)

	if err := dispatcher.Handle(context.Background(), payload); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(observed) != 1 || observed[0].Seq != 8 || observed[0].Type != "settings.updated" {
		t.Fatalf("observer events = %#v", observed)
	}
	if len(sink.events) != 1 {
		t.Fatalf("broadcast events = %d, want 1", len(sink.events))
	}

	// A duplicate NOTIFY is dropped before the observer runs.
	if err := dispatcher.Handle(context.Background(), payload); err != nil {
		t.Fatalf("second Handle: %v", err)
	}
	if len(observed) != 1 {
		t.Fatalf("observer ran for a duplicate: %d", len(observed))
	}
}

func TestDispatcherObserverErrorDoesNotSkipBroadcast(t *testing.T) {
	event := fixtureEvent(9)
	reader := &fakeEventReader{events: map[string]eventing.Event{"system|9": event}}
	sink := &fakeBroadcaster{}
	dispatcher := NewDispatcher(reader, sink, WithDispatcherObserver(func(context.Context, eventing.Event) error {
		return errors.New("reload failed")
	}))

	err := dispatcher.Handle(context.Background(), storedRef(t, event))
	if err == nil || !strings.Contains(err.Error(), "reload failed") {
		t.Fatalf("Handle error = %v, want observer error", err)
	}
	if len(sink.events) != 1 || sink.events[0].Seq != 9 {
		t.Fatalf("broadcast events = %#v", sink.events)
	}
}
