//go:build integration

package deliverypg

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

func p6DeliveryInt64Ptr(value int64) *int64 { return &value }

func p6SeedDeliveryIssue(t *testing.T, pool *pgxpool.Pool, id, versionID int64, title, status, category, severity string, createdAt, updatedAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO delivery_issue (
			id, version_id, title, status, category, severity, dedupe_key, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, id, versionID, title, status, category, severity, fmt.Sprintf("%064x", id), createdAt, updatedAt); err != nil {
		t.Fatalf("seed delivery issue: %v", err)
	}
}

func p6SeedDeliveryReport(t *testing.T, pool *pgxpool.Pool, id, versionID int64, waveID *int64, kind, status string, createdAt, updatedAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO delivery_report (id, version_id, wave_id, kind, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, id, versionID, waveID, kind, status, createdAt, updatedAt); err != nil {
		t.Fatalf("seed delivery report: %v", err)
	}
}

func TestP6DeliveryQueryReaderCountIssueValidity(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool)
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

	p6SeedDeliveryIssue(t, pool, 7001, 1001, "confirmed issue", "confirmed", "ui", "major", base, base)
	p6SeedDeliveryIssue(t, pool, 7002, 1001, "dismissed issue", "dismissed", "api", "minor", base, base)
	p6SeedDeliveryIssue(t, pool, 7003, 1001, "open issue", "open", "db", "trivial", base, base)
	p6SeedDeliveryIssue(t, pool, 7004, 1001, "merged issue", "merged", "ui", "major", base, base)
	p6SeedDeliveryIssue(t, pool, 7011, 1002, "other confirmed", "confirmed", "ui", "major", base, base)

	num, denom, err := reader.CountIssueValidity(ctx, nil)
	if err != nil {
		t.Fatalf("CountIssueValidity global: %v", err)
	}
	if num != 2 || denom != 3 {
		t.Fatalf("global counts = %d/%d, want 2/3 (open/merged excluded)", num, denom)
	}

	num, denom, err = reader.CountIssueValidity(ctx, p6DeliveryInt64Ptr(1001))
	if err != nil {
		t.Fatalf("CountIssueValidity version: %v", err)
	}
	if num != 1 || denom != 2 {
		t.Fatalf("version counts = %d/%d, want 1/2", num, denom)
	}

	num, denom, err = reader.CountIssueValidity(ctx, p6DeliveryInt64Ptr(1003))
	if err != nil {
		t.Fatalf("CountIssueValidity empty version: %v", err)
	}
	if num != 0 || denom != 0 {
		t.Fatalf("empty version counts = %d/%d, want 0/0", num, denom)
	}
}

func TestP6DeliveryQueryReaderSearchIssues(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool)
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

	p6SeedDeliveryIssue(t, pool, 7001, 1001, "Login button 500", "open", "ui", "major", base, base.Add(time.Hour))
	p6SeedDeliveryIssue(t, pool, 7002, 1001, "Login crash", "confirmed", "api", "minor", base, base.Add(2*time.Hour))
	p6SeedDeliveryIssue(t, pool, 7003, 1001, "Checkout copy", "open", "ui", "trivial", base, base.Add(3*time.Hour))
	p6SeedDeliveryIssue(t, pool, 7011, 1002, "Login v2", "open", "ui", "major", base, base)

	rows, err := reader.SearchIssues(ctx, "login", 2)
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (limit)", len(rows))
	}
	if rows[0].ID != 7002 || rows[1].ID != 7001 {
		t.Fatalf("ids = %d,%d, want 7002,7001 (updated_at DESC)", rows[0].ID, rows[1].ID)
	}
	if rows[0].Title != "Login crash" || rows[0].Status != "confirmed" || rows[0].Severity != "minor" {
		t.Fatalf("row = %#v", rows[0])
	}
	if rows[0].VersionID != 1001 || rows[0].VersionTitle != "" {
		t.Fatalf("version fields = %#v", rows[0])
	}

	limited, err := reader.SearchIssues(ctx, "login", 1)
	if err != nil {
		t.Fatalf("SearchIssues limit: %v", err)
	}
	if len(limited) != 1 || limited[0].ID != 7002 {
		t.Fatalf("limited rows = %#v", limited)
	}
}

func TestP6DeliveryQueryReaderSearchReports(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool)
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	waveID := int64(3001)

	p6SeedDeliveryReport(t, pool, 9001, 1001, &waveID, "wave", "published", base, base.Add(time.Hour))
	p6SeedDeliveryReport(t, pool, 9002, 1001, nil, "version", "draft", base, base.Add(2*time.Hour))
	otherWaveID := int64(3002)
	p6SeedDeliveryReport(t, pool, 9003, 1002, &otherWaveID, "wave", "stale", base, base.Add(3*time.Hour))

	rows, err := reader.SearchReports(ctx, "波次", 10)
	if err != nil {
		t.Fatalf("SearchReports: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 wave reports", len(rows))
	}
	if rows[0].ID != 9003 || rows[1].ID != 9001 {
		t.Fatalf("ids = %d,%d, want 9003,9001 (updated_at DESC)", rows[0].ID, rows[1].ID)
	}
	if rows[0].Kind != "wave" || rows[0].Status != "stale" || rows[0].VersionID != 1002 || rows[0].VersionTitle != "" {
		t.Fatalf("row = %#v", rows[0])
	}

	published, err := reader.SearchReports(ctx, "published", 10)
	if err != nil {
		t.Fatalf("SearchReports status: %v", err)
	}
	if len(published) != 1 || published[0].ID != 9001 {
		t.Fatalf("published rows = %#v", published)
	}

	versions, err := reader.SearchReports(ctx, "版本", 10)
	if err != nil {
		t.Fatalf("SearchReports version: %v", err)
	}
	if len(versions) != 1 || versions[0].ID != 9002 || versions[0].Kind != "version" {
		t.Fatalf("version rows = %#v", versions)
	}

	if _, err := reader.SearchReports(ctx, "does-not-exist", 10); err != nil {
		t.Fatalf("SearchReports empty: %v", err)
	}
}

func TestP6DeliveryQueryReaderListOpenIssues(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool)
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

	p6SeedDeliveryIssue(t, pool, 7001, 1001, "Open one", "open", "ui", "major", base, base)
	p6SeedDeliveryIssue(t, pool, 7002, 1001, "Open two", "open", "api", "minor", base.Add(time.Hour), base.Add(time.Hour))
	p6SeedDeliveryIssue(t, pool, 7003, 1001, "Confirmed", "confirmed", "db", "trivial", base.Add(2*time.Hour), base.Add(2*time.Hour))
	p6SeedDeliveryIssue(t, pool, 7011, 1002, "Open other version", "open", "ui", "major", base.Add(3*time.Hour), base.Add(3*time.Hour))

	rows, err := reader.ListOpenIssues(ctx, 10)
	if err != nil {
		t.Fatalf("ListOpenIssues: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3 open issues", len(rows))
	}
	if rows[0].ID != 7011 || rows[1].ID != 7002 || rows[2].ID != 7001 {
		t.Fatalf("ids = %d,%d,%d, want created_at DESC", rows[0].ID, rows[1].ID, rows[2].ID)
	}
	if rows[0].Title != "Open other version" || rows[0].Severity != "major" || rows[0].VersionTitle != "" {
		t.Fatalf("row = %#v", rows[0])
	}

	limited, err := reader.ListOpenIssues(ctx, 2)
	if err != nil {
		t.Fatalf("ListOpenIssues limit: %v", err)
	}
	if len(limited) != 2 || limited[0].ID != 7011 || limited[1].ID != 7002 {
		t.Fatalf("limited rows = %#v", limited)
	}
}

func TestP6DeliveryQueryReaderRequiresPool(t *testing.T) {
	if _, err := NewQueryReader(nil); err == nil {
		t.Fatal("expected an error for a nil pool")
	}
}
