//go:build integration

package agentpg

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

func p6AgentInt64Ptr(value int64) *int64 { return &value }

func p6SeedWorkItem(t *testing.T, pool *pgxpool.Pool, id, versionID int64, workType, status string, attempts int, createdAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO work_item (id, version_id, team, type, status, attempts, max_attempts, dedupe_key, created_at, updated_at)
		VALUES ($1, $2, 'execution', $3, $4, $5, 3, $6, $7, $7)
	`, id, versionID, workType, status, attempts, fmt.Sprintf("p6-item-%d", id), createdAt); err != nil {
		t.Fatalf("seed work item: %v", err)
	}
}

func TestP6AgentQueryReaderCountTaskRetries(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool)
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

	p6SeedWorkItem(t, pool, 8001, 1001, "execute.case", "open", 0, base)
	p6SeedWorkItem(t, pool, 8002, 1001, "execute.case", "done", 1, base)
	p6SeedWorkItem(t, pool, 8003, 1001, "execute.case", "failed", 2, base)
	p6SeedWorkItem(t, pool, 8004, 1001, "execute.case", "dead", 5, base)
	p6SeedWorkItem(t, pool, 8011, 1002, "design.plan", "done", 1, base)

	num, denom, err := reader.CountTaskRetries(ctx, nil)
	if err != nil {
		t.Fatalf("CountTaskRetries global: %v", err)
	}
	if num != 2 || denom != 4 {
		t.Fatalf("global counts = %d/%d, want 2/4", num, denom)
	}

	num, denom, err = reader.CountTaskRetries(ctx, p6AgentInt64Ptr(1001))
	if err != nil {
		t.Fatalf("CountTaskRetries version: %v", err)
	}
	if num != 2 || denom != 3 {
		t.Fatalf("version counts = %d/%d, want 2/3", num, denom)
	}

	num, denom, err = reader.CountTaskRetries(ctx, p6AgentInt64Ptr(1002))
	if err != nil {
		t.Fatalf("CountTaskRetries version 1002: %v", err)
	}
	if num != 0 || denom != 1 {
		t.Fatalf("version 1002 counts = %d/%d, want 0/1", num, denom)
	}

	num, denom, err = reader.CountTaskRetries(ctx, p6AgentInt64Ptr(1003))
	if err != nil {
		t.Fatalf("CountTaskRetries empty version: %v", err)
	}
	if num != 0 || denom != 0 {
		t.Fatalf("empty version counts = %d/%d, want 0/0", num, denom)
	}
}

func TestP6AgentQueryReaderListDeadWorkItems(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool)
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

	p6SeedWorkItem(t, pool, 8001, 1001, "execute.case", "dead", 3, base.Add(time.Hour))
	p6SeedWorkItem(t, pool, 8002, 1002, "design.plan", "dead", 3, base.Add(2*time.Hour))
	p6SeedWorkItem(t, pool, 8003, 1001, "execute.case", "failed", 2, base.Add(3*time.Hour))
	p6SeedWorkItem(t, pool, 8004, 1001, "execute.case", "open", 0, base.Add(4*time.Hour))

	rows, err := reader.ListDeadWorkItems(ctx, 10)
	if err != nil {
		t.Fatalf("ListDeadWorkItems: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 dead items", len(rows))
	}
	if rows[0].ID != 8002 || rows[1].ID != 8001 {
		t.Fatalf("ids = %d,%d, want 8002,8001 (created_at DESC)", rows[0].ID, rows[1].ID)
	}
	if rows[0].VersionID != 1002 || rows[0].Title != "design.plan 重试耗尽" {
		t.Fatalf("row = %#v", rows[0])
	}
	if !rows[0].CreatedAt.Equal(base.Add(2 * time.Hour)) {
		t.Fatalf("created_at = %v, want %v", rows[0].CreatedAt, base.Add(2*time.Hour))
	}

	limited, err := reader.ListDeadWorkItems(ctx, 1)
	if err != nil {
		t.Fatalf("ListDeadWorkItems limit: %v", err)
	}
	if len(limited) != 1 || limited[0].ID != 8002 {
		t.Fatalf("limited rows = %#v", limited)
	}
}

func TestP6AgentQueryReaderRequiresPool(t *testing.T) {
	if _, err := NewQueryReader(nil); err == nil {
		t.Fatal("expected an error for a nil pool")
	}
}
