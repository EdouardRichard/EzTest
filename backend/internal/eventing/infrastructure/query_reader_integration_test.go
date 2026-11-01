//go:build integration

package eventingpg

import (
	"context"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

func p6SeedEventChannel(t *testing.T, pool *pgxpool.Pool, channel string, lastSeq int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO event_channel (channel, last_seq) VALUES ($1, $2)
	`, channel, lastSeq); err != nil {
		t.Fatalf("seed event channel: %v", err)
	}
}

func p6SeedEvent(t *testing.T, pool *pgxpool.Pool, channel string, seq int64, eventType string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO event (channel, seq, type) VALUES ($1, $2, $3)
	`, channel, seq, eventType); err != nil {
		t.Fatalf("seed event: %v", err)
	}
}

func TestP6EventingQueryReaderCountEvents(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool)
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()

	p6SeedEventChannel(t, pool, "version:7", 3)
	p6SeedEventChannel(t, pool, "system", 2)
	p6SeedEvent(t, pool, "version:7", 1, "context.compacted")
	p6SeedEvent(t, pool, "version:7", 2, "context.compacted")
	p6SeedEvent(t, pool, "version:7", 3, "wave.started")
	p6SeedEvent(t, pool, "system", 1, "context.compacted")
	p6SeedEvent(t, pool, "system", 2, "wave.started")

	count, err := reader.CountEvents(ctx, "context.compacted", "")
	if err != nil {
		t.Fatalf("CountEvents global: %v", err)
	}
	if count != 3 {
		t.Fatalf("global count = %d, want 3", count)
	}

	count, err = reader.CountEvents(ctx, "context.compacted", "version:7")
	if err != nil {
		t.Fatalf("CountEvents channel: %v", err)
	}
	if count != 2 {
		t.Fatalf("channel count = %d, want 2", count)
	}

	count, err = reader.CountEvents(ctx, "wave.started", "system")
	if err != nil {
		t.Fatalf("CountEvents other channel: %v", err)
	}
	if count != 1 {
		t.Fatalf("channel count = %d, want 1", count)
	}

	count, err = reader.CountEvents(ctx, "unknown.type", "")
	if err != nil {
		t.Fatalf("CountEvents unknown type: %v", err)
	}
	if count != 0 {
		t.Fatalf("unknown count = %d, want 0", count)
	}

	count, err = reader.CountEvents(ctx, "context.compacted", "missing:1")
	if err != nil {
		t.Fatalf("CountEvents missing channel: %v", err)
	}
	if count != 0 {
		t.Fatalf("missing channel count = %d, want 0", count)
	}
}

func TestP6EventingQueryReaderRequiresPool(t *testing.T) {
	if _, err := NewQueryReader(nil); err == nil {
		t.Fatal("expected an error for a nil pool")
	}
}
