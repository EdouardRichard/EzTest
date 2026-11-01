//go:build integration

package versionpg

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// p6PendingDesignFake is the design application port stub used by the version
// reader tests: it records the batch it received and returns canned counts.
type p6PendingDesignFake struct {
	ids    []int64
	counts map[int64]int
	err    error
}

func (f *p6PendingDesignFake) PendingDesignCounts(_ context.Context, versionIDs []int64) (map[int64]int, error) {
	f.ids = append([]int64(nil), versionIDs...)
	return f.counts, f.err
}

func p6VersionID(value int64) *int64 { return &value }

func p6SeedVersion(t *testing.T, pool *pgxpool.Pool, id int64, title, status string, updatedAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO version (id, title, status, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, 1, $4, $4)
	`, id, title, status, updatedAt); err != nil {
		t.Fatalf("seed version: %v", err)
	}
}

func p6SeedRequirement(t *testing.T, pool *pgxpool.Pool, versionID, requirementID, requirementVersionID int64, status, title string, createdAt, updatedAt time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement (id, version_id, current_version_id, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, requirementID, versionID, requirementVersionID, status, createdAt, updatedAt); err != nil {
		t.Fatalf("seed requirement: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement_version (id, requirement_id, revision, title, content_hash, created_at)
		VALUES ($1, $2, 1, $3, $4, $5)
	`, requirementVersionID, requirementID, title, strings.Repeat("a", 64), createdAt); err != nil {
		t.Fatalf("seed requirement version: %v", err)
	}
}

func TestP6VersionQueryReaderCountRequirementClosure(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool, &p6PendingDesignFake{})
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

	p6SeedVersion(t, pool, 1001, "Login SUT", "initial", base)
	p6SeedVersion(t, pool, 1002, "Checkout SUT", "running", base.Add(time.Hour))

	p6SeedRequirement(t, pool, 1001, 2001, 3001, "draft", "draft req", base, base)
	p6SeedRequirement(t, pool, 1001, 2002, 3002, "clarified", "clarified req", base, base)
	p6SeedRequirement(t, pool, 1001, 2003, 3003, "closed", "closed req", base, base)
	p6SeedRequirement(t, pool, 1001, 2004, 3004, "obsolete", "obsolete req", base, base)
	p6SeedRequirement(t, pool, 1002, 2011, 3011, "closed", "other version closed", base, base)
	p6SeedRequirement(t, pool, 1002, 2012, 3012, "draft", "other version draft", base, base)

	num, denom, err := reader.CountRequirementClosure(ctx, nil)
	if err != nil {
		t.Fatalf("CountRequirementClosure global: %v", err)
	}
	if num != 2 || denom != 5 {
		t.Fatalf("global counts = %d/%d, want 2/5 (obsolete excluded)", num, denom)
	}

	num, denom, err = reader.CountRequirementClosure(ctx, p6VersionID(1001))
	if err != nil {
		t.Fatalf("CountRequirementClosure version: %v", err)
	}
	if num != 1 || denom != 3 {
		t.Fatalf("version counts = %d/%d, want 1/3", num, denom)
	}

	num, denom, err = reader.CountRequirementClosure(ctx, p6VersionID(1003))
	if err != nil {
		t.Fatalf("CountRequirementClosure empty version: %v", err)
	}
	if num != 0 || denom != 0 {
		t.Fatalf("empty version counts = %d/%d, want 0/0", num, denom)
	}
}

func TestP6VersionQueryReaderSearchVersions(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool, &p6PendingDesignFake{})
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

	p6SeedVersion(t, pool, 1001, "Login SUT", "initial", base.Add(time.Hour))
	p6SeedVersion(t, pool, 1002, "Login API", "running", base.Add(2*time.Hour))
	p6SeedVersion(t, pool, 1003, "Checkout SUT", "completed", base.Add(3*time.Hour))

	rows, err := reader.SearchVersions(ctx, "login", 2)
	if err != nil {
		t.Fatalf("SearchVersions: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (limit)", len(rows))
	}
	if rows[0].ID != 1002 || rows[1].ID != 1001 {
		t.Fatalf("ids = %d,%d, want 1002,1001 (updated_at DESC)", rows[0].ID, rows[1].ID)
	}
	if rows[0].Title != "Login API" || rows[0].Status != "running" || !rows[0].UpdatedAt.Equal(base.Add(2*time.Hour)) {
		t.Fatalf("row = %#v", rows[0])
	}

	rows, err = reader.SearchVersions(ctx, "does-not-exist", 5)
	if err != nil {
		t.Fatalf("SearchVersions empty: %v", err)
	}
	if rows == nil || len(rows) != 0 {
		t.Fatalf("rows = %#v, want empty non-nil slice", rows)
	}
}

func TestP6VersionQueryReaderSearchRequirements(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool, &p6PendingDesignFake{})
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

	p6SeedVersion(t, pool, 1001, "Login SUT", "initial", base)
	p6SeedVersion(t, pool, 1002, "Login API", "running", base)
	p6SeedRequirement(t, pool, 1001, 2001, 3001, "clarified", "Email login", base, base.Add(time.Hour))

	// Requirement 2002 has a historical revision that matches q and a current
	// revision that also matches. Only the current revision is searched.
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement (id, version_id, current_version_id, status, created_at, updated_at)
		VALUES (2002, 1002, 3002, 'draft', $1, $2)
	`, base, base.Add(3*time.Hour)); err != nil {
		t.Fatalf("seed requirement: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement_version (id, requirement_id, revision, title, content_hash)
		VALUES (3021, 2002, 1, 'Login retry old', $1)
	`, strings.Repeat("b", 64)); err != nil {
		t.Fatalf("seed old requirement version: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement_version (id, requirement_id, revision, title, content_hash)
		VALUES (3002, 2002, 2, 'Login retry', $1)
	`, strings.Repeat("b", 64)); err != nil {
		t.Fatalf("seed current requirement version: %v", err)
	}

	rows, err := reader.SearchRequirements(ctx, "login", 5)
	if err != nil {
		t.Fatalf("SearchRequirements: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].ID != 2002 || rows[1].ID != 2001 {
		t.Fatalf("ids = %d,%d, want 2002,2001 (updated_at DESC)", rows[0].ID, rows[1].ID)
	}
	if rows[0].Title != "Login retry" || rows[0].Status != "draft" {
		t.Fatalf("row = %#v", rows[0])
	}
	if rows[0].VersionID != 1002 || rows[0].VersionTitle != "Login API" {
		t.Fatalf("version fields = %#v", rows[0])
	}
	if rows[1].VersionTitle != "Login SUT" {
		t.Fatalf("version title = %q, want Login SUT", rows[1].VersionTitle)
	}

	limited, err := reader.SearchRequirements(ctx, "login", 1)
	if err != nil {
		t.Fatalf("SearchRequirements limit: %v", err)
	}
	if len(limited) != 1 || limited[0].ID != 2002 {
		t.Fatalf("limited rows = %#v", limited)
	}
}

func TestP6VersionQueryReaderVersionTitles(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool, &p6PendingDesignFake{})
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	p6SeedVersion(t, pool, 1001, "Login SUT", "initial", base)
	p6SeedVersion(t, pool, 1002, "Checkout SUT", "initial", base)

	titles, err := reader.VersionTitles(ctx, []int64{1001, 1002, 9999})
	if err != nil {
		t.Fatalf("VersionTitles: %v", err)
	}
	if len(titles) != 2 || titles[1001] != "Login SUT" || titles[1002] != "Checkout SUT" {
		t.Fatalf("titles = %#v", titles)
	}

	empty, err := reader.VersionTitles(ctx, nil)
	if err != nil {
		t.Fatalf("VersionTitles empty: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("titles = %#v, want empty non-nil map", empty)
	}
}

func TestP6VersionQueryReaderListPendingDesignVersions(t *testing.T) {
	pool := testutil.NewDB(t)
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	p6SeedVersion(t, pool, 1001, "Login SUT", "initial", base.Add(time.Hour))
	p6SeedVersion(t, pool, 1002, "Checkout SUT", "initial", base.Add(2*time.Hour))

	pending := &p6PendingDesignFake{counts: map[int64]int{1001: 2, 1002: 0}}
	reader, err := NewQueryReader(pool, pending)
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()

	rows, err := reader.ListPendingDesignVersions(ctx)
	if err != nil {
		t.Fatalf("ListPendingDesignVersions: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].VersionID != 1001 || rows[0].VersionTitle != "Login SUT" || rows[0].PendingCount != 2 {
		t.Fatalf("row = %#v", rows[0])
	}
	if len(pending.ids) != 2 || pending.ids[0] != 1002 || pending.ids[1] != 1001 {
		t.Fatalf("port ids = %v, want [1002 1001] (version order)", pending.ids)
	}

	failing, err := NewQueryReader(pool, &p6PendingDesignFake{err: errors.New("boom")})
	if err != nil {
		t.Fatalf("NewQueryReader failing: %v", err)
	}
	if _, err := failing.ListPendingDesignVersions(ctx); err == nil {
		t.Fatal("expected the design port error to propagate")
	}
}

func TestP6VersionQueryReaderListDraftRequirements(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool, &p6PendingDesignFake{})
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	p6SeedVersion(t, pool, 1001, "Login SUT", "initial", base)

	p6SeedRequirement(t, pool, 1001, 2001, 3001, "draft", "Older draft", base, base)
	p6SeedRequirement(t, pool, 1001, 2002, 3002, "draft", "Newer draft", base.Add(time.Hour), base.Add(time.Hour))
	p6SeedRequirement(t, pool, 1001, 2003, 3003, "clarified", "Clarified", base.Add(2*time.Hour), base.Add(2*time.Hour))

	rows, err := reader.ListDraftRequirements(ctx, 10)
	if err != nil {
		t.Fatalf("ListDraftRequirements: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (draft only)", len(rows))
	}
	if rows[0].ID != 2002 || rows[1].ID != 2001 {
		t.Fatalf("ids = %d,%d, want 2002,2001 (created_at DESC)", rows[0].ID, rows[1].ID)
	}
	if rows[0].Title != "Newer draft" || rows[0].VersionID != 1001 || rows[0].VersionTitle != "Login SUT" {
		t.Fatalf("row = %#v", rows[0])
	}

	limited, err := reader.ListDraftRequirements(ctx, 1)
	if err != nil {
		t.Fatalf("ListDraftRequirements limit: %v", err)
	}
	if len(limited) != 1 || limited[0].ID != 2002 {
		t.Fatalf("limited rows = %#v", limited)
	}
}

func TestP6VersionQueryReaderRequiresPoolAndDesignPort(t *testing.T) {
	pool := testutil.NewDB(t)
	if _, err := NewQueryReader(nil, &p6PendingDesignFake{}); err == nil {
		t.Fatal("expected an error for a nil pool")
	}
	if _, err := NewQueryReader(pool, nil); err == nil {
		t.Fatal("expected an error for a missing pending design port")
	}
}
