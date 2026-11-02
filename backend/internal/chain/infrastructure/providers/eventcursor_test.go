package providers

import (
	"context"
	"errors"
	"testing"
	"time"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
)

type eventSnapshotCall struct {
	channel string
	after   int64
	limit   int
}

type fakeEventSnapshotReader struct {
	events []eventing.Event
	err    error
	calls  []eventSnapshotCall
}

func (f *fakeEventSnapshotReader) Snapshot(_ context.Context, channel string, after int64, limit int) (eventingapp.Snapshot, error) {
	f.calls = append(f.calls, eventSnapshotCall{channel: channel, after: after, limit: limit})
	if f.err != nil {
		return eventingapp.Snapshot{}, f.err
	}
	var data []eventing.Event
	if after == 0 {
		start := 0
		if len(f.events) > limit {
			start = len(f.events) - limit
		}
		data = append([]eventing.Event(nil), f.events[start:]...)
	} else {
		for _, event := range f.events {
			if event.Seq > after && len(data) < limit {
				data = append(data, event)
			}
		}
	}
	cursor := int64(0)
	if len(f.events) > 0 {
		cursor = f.events[len(f.events)-1].Seq
	}
	return eventingapp.Snapshot{Data: data, Cursor: cursor}, nil
}

func TestEventCursorProviderEventTimeBySeq(t *testing.T) {
	t1 := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
	store := &fakeEventSnapshotReader{events: []eventing.Event{
		{Channel: "version:42", Seq: 1, CreatedAt: t1},
		{Channel: "version:42", Seq: 2, CreatedAt: t1.Add(time.Minute)},
		{Channel: "version:42", Seq: 3, CreatedAt: t1.Add(2 * time.Minute)},
	}}
	provider := NewEventCursorProvider(store)

	got, found, err := provider.EventTime(context.Background(), "version:42", 2)
	if err != nil || !found {
		t.Fatalf("EventTime(2) = %v/%v/%v", got, found, err)
	}
	if !got.Equal(t1.Add(time.Minute)) {
		t.Errorf("EventTime(2) = %v, want %v", got, t1.Add(time.Minute))
	}
	if len(store.calls) != 1 || store.calls[0] != (eventSnapshotCall{channel: "version:42", after: 1, limit: 1}) {
		t.Errorf("snapshot calls = %+v, want one after=1 limit=1", store.calls)
	}

	got, found, err = provider.EventTime(context.Background(), "version:42", 1)
	if err != nil || !found {
		t.Fatalf("EventTime(1) = %v/%v/%v", got, found, err)
	}
	if !got.Equal(t1) {
		t.Errorf("EventTime(1) = %v, want %v", got, t1)
	}
	if last := store.calls[len(store.calls)-1]; last.after != 0 || last.limit != eventingapp.MaxSnapshotLimit {
		t.Errorf("seq 1 snapshot call = %+v, want the newest page fallback", last)
	}

	_, found, err = provider.EventTime(context.Background(), "version:42", 4)
	if err != nil || found {
		t.Fatalf("EventTime(4) = found %v err %v, want not found", found, err)
	}
}

func TestEventCursorProviderEventTimeErrors(t *testing.T) {
	store := &fakeEventSnapshotReader{}
	provider := NewEventCursorProvider(store)

	if _, _, err := provider.EventTime(context.Background(), " ", 1); err == nil {
		t.Fatal("EventTime accepted an empty channel")
	}
	if _, _, err := provider.EventTime(context.Background(), "version:42", 0); err == nil {
		t.Fatal("EventTime accepted seq 0")
	}

	boom := errors.New("event store down")
	store.err = boom
	if _, _, err := provider.EventTime(context.Background(), "version:42", 3); !errors.Is(err, boom) {
		t.Fatalf("EventTime error = %v, want the snapshot error", err)
	}
}
