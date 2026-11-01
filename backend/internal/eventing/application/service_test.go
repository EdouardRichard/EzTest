package eventingapp

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

type fakeRepo struct {
	appended      []eventing.Event
	appendFn      func(context.Context, eventing.Event) (eventing.Event, error)
	snapshotFn    func(context.Context, string, int64, int) (eventing.SnapshotPage, error)
	snapshotCalls int
	snapshotCh    string
	snapshotAfter int64
	snapshotLimit int
	bounds        eventing.Bounds
	boundsErr     error
}

func (f *fakeRepo) Append(ctx context.Context, event eventing.Event) (eventing.Event, error) {
	f.appended = append(f.appended, event)
	if f.appendFn != nil {
		return f.appendFn(ctx, event)
	}
	return event, nil
}

func (f *fakeRepo) Snapshot(ctx context.Context, channel string, after int64, limit int) (eventing.SnapshotPage, error) {
	f.snapshotCalls++
	f.snapshotCh, f.snapshotAfter, f.snapshotLimit = channel, after, limit
	if f.snapshotFn != nil {
		return f.snapshotFn(ctx, channel, after, limit)
	}
	return eventing.SnapshotPage{}, nil
}

func (f *fakeRepo) Bounds(context.Context, string) (eventing.Bounds, error) {
	return f.bounds, f.boundsErr
}

func TestPublishAppendsThenBroadcasts(t *testing.T) {
	at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	repo := &fakeRepo{appendFn: func(_ context.Context, event eventing.Event) (eventing.Event, error) {
		event.Seq = 7
		event.CreatedAt = at
		return event, nil
	}}
	svc := New(repo, NewHub(4))
	channel, cancel := svc.Subscribe("system")
	defer cancel()

	actor := int64(42)
	err := svc.Publish(context.Background(), Event{
		Channel:   "system",
		Type:      "settings.updated",
		Scope:     map[string]any{},
		Payload:   map[string]any{"key": "execution.global_concurrency"},
		ActorID:   &actor,
		RequestID: "req_123",
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if len(repo.appended) != 1 {
		t.Fatalf("appended = %d", len(repo.appended))
	}
	stored := repo.appended[0]
	if stored.Channel != "system" || stored.Type != "settings.updated" {
		t.Fatalf("stored = %#v", stored)
	}
	if stored.ActorID == nil || *stored.ActorID != 42 || stored.RequestID != "req_123" {
		t.Fatalf("stored actor/request = %#v", stored)
	}

	select {
	case got := <-channel:
		if got.Seq != 7 || got.Channel != "system" || got.Type != "settings.updated" {
			t.Fatalf("broadcast = %#v", got)
		}
		if !got.CreatedAt.Equal(at) {
			t.Fatalf("created_at = %v", got.CreatedAt)
		}
		if !reflect.DeepEqual(got.Payload, map[string]any{"key": "execution.global_concurrency"}) {
			t.Fatalf("payload = %#v", got.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("no broadcast received")
	}
}

func TestPublishRejectsInvalidChannelAndEmptyType(t *testing.T) {
	repo := &fakeRepo{}
	svc := New(repo, NewHub(1))

	cases := []struct {
		name   string
		event  Event
		status int
	}{
		{"unknown channel", Event{Channel: "bogus", Type: "x"}, 400},
		{"zero version", Event{Channel: "version:0", Type: "x"}, 400},
		{"non numeric user", Event{Channel: "user:abc", Type: "x"}, 400},
		{"empty type", Event{Channel: "system", Type: " "}, 400},
		{"missing channel", Event{Type: "x"}, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.Publish(context.Background(), tc.event)
			if err == nil {
				t.Fatal("expected error")
			}
			apiErr := platform.AsError(err)
			if apiErr.Status != tc.status {
				t.Fatalf("status = %d, want %d", apiErr.Status, tc.status)
			}
		})
	}
	if len(repo.appended) != 0 {
		t.Fatalf("repo called %d times", len(repo.appended))
	}
}

func TestSnapshotDefaultsAndCapsLimit(t *testing.T) {
	page := eventing.SnapshotPage{
		Events:  []eventing.Event{{Channel: "system", Seq: 3, Type: "settings.updated"}},
		Cursor:  9,
		HasMore: true,
	}
	repo := &fakeRepo{snapshotFn: func(context.Context, string, int64, int) (eventing.SnapshotPage, error) {
		return page, nil
	}}
	svc := New(repo, NewHub(1))

	got, err := svc.Snapshot(context.Background(), "system", 2, 0)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if repo.snapshotLimit != DefaultSnapshotLimit {
		t.Fatalf("default limit = %d, want %d", repo.snapshotLimit, DefaultSnapshotLimit)
	}
	if repo.snapshotCh != "system" || repo.snapshotAfter != 2 {
		t.Fatalf("args = %q/%d", repo.snapshotCh, repo.snapshotAfter)
	}
	if len(got.Data) != 1 || got.Cursor != 9 || !got.HasMore {
		t.Fatalf("snapshot = %#v", got)
	}

	if _, err := svc.Snapshot(context.Background(), "version:12", 0, 10000); err != nil {
		t.Fatalf("Snapshot cap: %v", err)
	}
	if repo.snapshotLimit != MaxSnapshotLimit {
		t.Fatalf("capped limit = %d, want %d", repo.snapshotLimit, MaxSnapshotLimit)
	}
}

func TestSnapshotRejectsInvalidInput(t *testing.T) {
	repo := &fakeRepo{}
	svc := New(repo, NewHub(1))

	for _, channel := range []string{"", "bogus", "version:x", "user:0"} {
		if _, err := svc.Snapshot(context.Background(), channel, 0, 10); err == nil {
			t.Fatalf("channel %q: expected error", channel)
		}
	}
	if _, err := svc.Snapshot(context.Background(), "system", -1, 10); err == nil {
		t.Fatal("negative after: expected error")
	}
	if repo.snapshotCalls != 0 {
		t.Fatalf("repo called %d times", repo.snapshotCalls)
	}
}

func TestBoundsDelegates(t *testing.T) {
	repo := &fakeRepo{bounds: eventing.Bounds{FirstSeq: 3, LastSeq: 9}}
	svc := New(repo, NewHub(1))

	bounds, err := svc.Bounds(context.Background(), "system")
	if err != nil {
		t.Fatalf("Bounds: %v", err)
	}
	if bounds.FirstSeq != 3 || bounds.LastSeq != 9 {
		t.Fatalf("bounds = %#v", bounds)
	}
	if _, err := svc.Bounds(context.Background(), "bogus"); err == nil {
		t.Fatal("expected invalid channel error")
	}
}

func TestSubscribeCancelClosesChannel(t *testing.T) {
	svc := New(&fakeRepo{}, NewHub(2))
	channel, cancel := svc.Subscribe("system")

	cancel()
	cancel() // must be idempotent

	if _, ok := <-channel; ok {
		t.Fatal("channel should be closed after cancel")
	}
}

func TestHubDropsSlowSubscriber(t *testing.T) {
	hub := NewHub(1)
	channel, cancel := hub.Subscribe("system")
	defer cancel()

	hub.Broadcast(eventing.Event{Channel: "system", Seq: 1})
	hub.Broadcast(eventing.Event{Channel: "system", Seq: 2}) // buffer full: disconnect

	first, ok := <-channel
	if !ok || first.Seq != 1 {
		t.Fatalf("first = %#v, ok = %v", first, ok)
	}
	if _, ok := <-channel; ok {
		t.Fatal("slow subscriber channel should be closed")
	}
}

func TestHubIsolatesChannels(t *testing.T) {
	hub := NewHub(2)
	system, cancelSystem := hub.Subscribe("system")
	defer cancelSystem()
	version, cancelVersion := hub.Subscribe("version:1")
	defer cancelVersion()

	hub.Broadcast(eventing.Event{Channel: "system", Seq: 1})
	if got := <-system; got.Seq != 1 {
		t.Fatalf("system event = %#v", got)
	}
	select {
	case got := <-version:
		t.Fatalf("unexpected cross-channel event: %#v", got)
	default:
	}
}

func TestValidChannel(t *testing.T) {
	cases := map[string]bool{
		"system":         true,
		"version:1":      true,
		"version:999999": true,
		"user:42":        true,
		"":               false,
		"version:":       false,
		"version:0":      false,
		"version:abc":    false,
		"user:-1":        false,
		"systemx":        false,
		"system:1":       false,
	}
	for channel, want := range cases {
		if got := ValidChannel(channel); got != want {
			t.Fatalf("ValidChannel(%q) = %v, want %v", channel, got, want)
		}
	}
}

func TestPublishRepoErrorIsReturnedWithoutBroadcast(t *testing.T) {
	wantErr := errors.New("insert failed")
	repo := &fakeRepo{appendFn: func(context.Context, eventing.Event) (eventing.Event, error) {
		return eventing.Event{}, wantErr
	}}
	svc := New(repo, NewHub(1))
	channel, cancel := svc.Subscribe("system")
	defer cancel()

	err := svc.Publish(context.Background(), Event{Channel: "system", Type: "x"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v", err)
	}
	select {
	case got := <-channel:
		t.Fatalf("unexpected broadcast: %#v", got)
	case <-time.After(20 * time.Millisecond):
	}
}

type fakeNotifier struct {
	refs   []EventRef
	err    error
	onCall func()
}

func (f *fakeNotifier) Notify(_ context.Context, ref EventRef) error {
	if f.onCall != nil {
		f.onCall()
	}
	f.refs = append(f.refs, ref)
	return f.err
}

func TestPublishNotifiesStoredEventAfterAppend(t *testing.T) {
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	repo := &fakeRepo{appendFn: func(_ context.Context, event eventing.Event) (eventing.Event, error) {
		event.Seq = 11
		event.CreatedAt = at
		return event, nil
	}}
	notifier := &fakeNotifier{}
	appended := false
	notifier.onCall = func() {
		if !appended {
			t.Error("notify happened before the event append")
		}
	}
	svc := New(repo, NewHub(4), notifier)
	repo.appendFn = func(_ context.Context, event eventing.Event) (eventing.Event, error) {
		appended = true
		event.Seq = 11
		event.CreatedAt = at
		return event, nil
	}

	channel, cancel := svc.Subscribe("system")
	defer cancel()
	if err := svc.Publish(context.Background(), Event{Channel: "system", Type: "settings.updated"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(notifier.refs) != 1 {
		t.Fatalf("notify calls = %d, want 1", len(notifier.refs))
	}
	if notifier.refs[0] != (EventRef{Channel: "system", Seq: 11}) {
		t.Fatalf("notified ref = %#v", notifier.refs[0])
	}
	select {
	case got := <-channel:
		if got.Seq != 11 {
			t.Fatalf("broadcast seq = %d", got.Seq)
		}
	case <-time.After(time.Second):
		t.Fatal("no local broadcast")
	}
}

func TestPublishToleratesNotifierFailure(t *testing.T) {
	repo := &fakeRepo{appendFn: func(_ context.Context, event eventing.Event) (eventing.Event, error) {
		event.Seq = 5
		return event, nil
	}}
	notifier := &fakeNotifier{err: errors.New("notify failed")}
	svc := New(repo, NewHub(2), notifier)
	channel, cancel := svc.Subscribe("system")
	defer cancel()

	if err := svc.Publish(context.Background(), Event{Channel: "system", Type: "x"}); err != nil {
		t.Fatalf("Publish must not fail on a lost notification: %v", err)
	}
	select {
	case got := <-channel:
		if got.Seq != 5 {
			t.Fatalf("broadcast seq = %d", got.Seq)
		}
	case <-time.After(time.Second):
		t.Fatal("lost notification also suppressed the local broadcast")
	}
}

func TestHubBroadcastLoadedDeliversToSubscribers(t *testing.T) {
	hub := NewHub(2)
	channel, cancel := hub.Subscribe("version:9")
	defer cancel()
	hub.BroadcastLoaded(eventing.Event{Channel: "version:9", Seq: 4, Type: "report.published"})
	select {
	case got := <-channel:
		if got.Seq != 4 || got.Type != "report.published" {
			t.Fatalf("broadcast = %#v", got)
		}
	default:
		t.Fatal("BroadcastLoaded did not reach the subscriber")
	}
}
