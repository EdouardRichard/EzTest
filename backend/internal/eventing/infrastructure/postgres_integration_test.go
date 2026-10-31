//go:build integration

package eventingpg_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/eventing/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

func TestAppendAllocatesChannelSeqAndStoresEnvelope(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := eventingpg.New(pool)
	ctx := context.Background()
	actor := int64(9001)

	first, err := repo.Append(ctx, eventing.Event{
		Channel:   "system",
		Type:      "settings.updated",
		Scope:     map[string]any{"version_id": "1001"},
		Payload:   map[string]any{"key": "execution.global_concurrency"},
		ActorID:   &actor,
		RequestID: "req_abc",
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if first.Seq != 1 {
		t.Fatalf("first seq = %d", first.Seq)
	}
	if first.CreatedAt.IsZero() {
		t.Fatal("created_at is zero")
	}

	second, err := repo.Append(ctx, eventing.Event{Channel: "system", Type: "version.created"})
	if err != nil {
		t.Fatalf("Append second: %v", err)
	}
	if second.Seq != 2 {
		t.Fatalf("second seq = %d", second.Seq)
	}

	other, err := repo.Append(ctx, eventing.Event{Channel: "version:77", Type: "version.created"})
	if err != nil {
		t.Fatalf("Append other channel: %v", err)
	}
	if other.Seq != 1 {
		t.Fatalf("other channel seq = %d", other.Seq)
	}

	var envelope []byte
	if err := pool.QueryRow(ctx, `SELECT envelope FROM event WHERE channel = 'system' AND seq = 1`).Scan(&envelope); err != nil {
		t.Fatalf("read envelope: %v", err)
	}
	var stored map[string]any
	if err := json.Unmarshal(envelope, &stored); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	scope, _ := stored["scope"].(map[string]any)
	if scope["version_id"] != "1001" {
		t.Fatalf("scope = %#v", stored["scope"])
	}
	payload, _ := stored["payload"].(map[string]any)
	if payload["key"] != "execution.global_concurrency" {
		t.Fatalf("payload = %#v", stored["payload"])
	}
	if stored["actor_id"] != "9001" {
		t.Fatalf("actor_id = %#v, want string 9001", stored["actor_id"])
	}
	if stored["request_id"] != "req_abc" {
		t.Fatalf("request_id = %#v", stored["request_id"])
	}
}

func TestSnapshotLatestPageAndAfterCursor(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := eventingpg.New(pool)
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		if _, err := repo.Append(ctx, eventing.Event{Channel: "system", Type: fmt.Sprintf("event.%d", i)}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	latest, err := repo.Snapshot(ctx, "system", 0, 2)
	if err != nil {
		t.Fatalf("Snapshot latest: %v", err)
	}
	if len(latest.Events) != 2 || latest.Events[0].Seq != 4 || latest.Events[1].Seq != 5 {
		t.Fatalf("latest events = %#v", seqs(latest.Events))
	}
	if latest.Cursor != 5 || !latest.HasMore {
		t.Fatalf("latest cursor/hasMore = %d/%v", latest.Cursor, latest.HasMore)
	}

	after, err := repo.Snapshot(ctx, "system", 2, 2)
	if err != nil {
		t.Fatalf("Snapshot after: %v", err)
	}
	if len(after.Events) != 2 || after.Events[0].Seq != 3 || after.Events[1].Seq != 4 {
		t.Fatalf("after events = %#v", seqs(after.Events))
	}
	if !after.HasMore || after.Cursor != 5 {
		t.Fatalf("after cursor/hasMore = %d/%v", after.Cursor, after.HasMore)
	}

	tail, err := repo.Snapshot(ctx, "system", 4, 2)
	if err != nil {
		t.Fatalf("Snapshot tail: %v", err)
	}
	if len(tail.Events) != 1 || tail.Events[0].Seq != 5 || tail.HasMore {
		t.Fatalf("tail = %#v hasMore=%v", seqs(tail.Events), tail.HasMore)
	}

	empty, err := repo.Snapshot(ctx, "system", 5, 2)
	if err != nil {
		t.Fatalf("Snapshot empty: %v", err)
	}
	if len(empty.Events) != 0 || empty.Cursor != 5 || empty.HasMore {
		t.Fatalf("empty = %#v", empty)
	}

	unknown, err := repo.Snapshot(ctx, "user:1", 0, 10)
	if err != nil {
		t.Fatalf("Snapshot unknown: %v", err)
	}
	if len(unknown.Events) != 0 || unknown.Cursor != 0 || unknown.HasMore {
		t.Fatalf("unknown = %#v", unknown)
	}
}

func TestBounds(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := eventingpg.New(pool)
	ctx := context.Background()

	empty, err := repo.Bounds(ctx, "system")
	if err != nil {
		t.Fatalf("Bounds empty: %v", err)
	}
	if empty.FirstSeq != 0 || empty.LastSeq != 0 {
		t.Fatalf("empty bounds = %#v", empty)
	}

	for i := 0; i < 3; i++ {
		if _, err := repo.Append(ctx, eventing.Event{Channel: "system", Type: "x"}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	bounds, err := repo.Bounds(ctx, "system")
	if err != nil {
		t.Fatalf("Bounds: %v", err)
	}
	if bounds.FirstSeq != 1 || bounds.LastSeq != 3 {
		t.Fatalf("bounds = %#v", bounds)
	}
}

func TestAppendConcurrentAllocatesUniqueSeq(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := eventingpg.New(pool)
	ctx := context.Background()

	const writers = 8
	var wg sync.WaitGroup
	seqs := make(chan int64, writers)
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			event, err := repo.Append(ctx, eventing.Event{Channel: "system", Type: "x", CreatedAt: time.Now()})
			if err != nil {
				errs <- err
				return
			}
			seqs <- event.Seq
		}()
	}
	wg.Wait()
	close(seqs)
	close(errs)
	for err := range errs {
		t.Fatalf("Append: %v", err)
	}
	seen := make(map[int64]bool, writers)
	for seq := range seqs {
		if seq < 1 || seq > writers {
			t.Fatalf("seq out of range: %d", seq)
		}
		if seen[seq] {
			t.Fatalf("duplicate seq: %d", seq)
		}
		seen[seq] = true
	}
	if len(seen) != writers {
		t.Fatalf("unique seqs = %d, want %d", len(seen), writers)
	}
}

func seqs(events []eventing.Event) []int64 {
	result := make([]int64, 0, len(events))
	for _, event := range events {
		result = append(result, event.Seq)
	}
	return result
}
