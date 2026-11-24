//go:build integration

package executionpg

import (
	"context"
	"errors"
	"testing"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// TestSessionFixtureAppendListRoundTrip pins the append-only fixture fact store
// against the real migrated schema (SC-F5): the (version,sut,environment,role)
// reuse key isolates lookups, a re-save appends a second row (latest wins, no
// rewrite, no unique key), and List returns append order.
func TestSessionFixtureAppendListRoundTrip(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()

	first := execution.SessionFixture{
		VersionID: 100, SUTID: 9001, EnvironmentID: 7001, Role: "admin",
		StorageStateRef: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CreatedBy:       42,
	}
	written, err := repo.SessionFixtures.AppendSessionFixture(ctx, first)
	if err != nil {
		t.Fatalf("append first: %v", err)
	}
	if written.ID <= 0 || written.CreatedAt.IsZero() {
		t.Fatalf("append must assign id and created_at: %+v", written)
	}

	second := first
	second.StorageStateRef = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	refreshed, err := repo.SessionFixtures.AppendSessionFixture(ctx, second)
	if err != nil {
		t.Fatalf("append second (re-save): %v", err)
	}
	if refreshed.ID <= written.ID {
		t.Fatalf("re-save must append a NEW row: %d then %d", written.ID, refreshed.ID)
	}

	rows, err := repo.SessionFixtures.ListSessionFixtures(ctx, 100, 9001, 7001, "admin")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (append-only history)", len(rows))
	}
	if rows[0].ID >= rows[1].ID {
		t.Fatalf("list must be append order (id asc): %d then %d", rows[0].ID, rows[1].ID)
	}
	if rows[1].StorageStateRef != second.StorageStateRef {
		t.Fatalf("latest ref = %q, want the re-saved ref (latest wins)", rows[1].StorageStateRef)
	}
	if rows[0].CreatedBy != 42 {
		t.Fatalf("created_by round-trip = %d", rows[0].CreatedBy)
	}

	// Reuse-key isolation: every key component discriminates.
	for _, key := range [][4]any{
		{int64(101), int64(9001), int64(7001), "admin"},
		{int64(100), int64(9002), int64(7001), "admin"},
		{int64(100), int64(9001), int64(7002), "admin"},
		{int64(100), int64(9001), int64(7001), "viewer"},
	} {
		other, err := repo.SessionFixtures.ListSessionFixtures(ctx, key[0].(int64), key[1].(int64), key[2].(int64), key[3].(string))
		if err != nil {
			t.Fatalf("list other key: %v", err)
		}
		if len(other) != 0 {
			t.Fatalf("key %v must be isolated, got %d rows", key, len(other))
		}
	}

	// The admin version listing reads the whole version's facts newest first.
	byVersion, hasMore, err := repo.SessionFixtures.ListSessionFixturesByVersion(ctx, 100, 50, nil)
	if err != nil {
		t.Fatalf("list by version: %v", err)
	}
	if len(byVersion) != 2 || byVersion[0].ID != refreshed.ID || hasMore {
		t.Fatalf("version listing = %+v hasMore=%v, want newest first, no more", byVersion, hasMore)
	}
}

// TestSessionFixtureVersionListingPaginates proves the admin listing does NOT
// silently truncate an unbounded append-only fact log (constitution art.7.6,
// fix round 1 Important 1): with >100 facts, a limit=100 page returns exactly
// 100 rows + hasMore=true + a next cursor, and following the cursor yields the
// remainder with hasMore=false — no fact is lost.
func TestSessionFixtureVersionListingPaginates(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()

	const total = 105
	var lastID int64
	for i := 0; i < total; i++ {
		written, err := repo.SessionFixtures.AppendSessionFixture(ctx, execution.SessionFixture{
			VersionID: 100, SUTID: 9001, EnvironmentID: 7001, Role: "admin",
			StorageStateRef: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
			CreatedBy:       int64(i),
		})
		if err != nil {
			t.Fatalf("append fact %d: %v", i, err)
		}
		lastID = written.ID
	}

	// First page: limit=100 -> 100 rows, hasMore=true, cursor = the 100th id.
	page1, hasMore, err := repo.SessionFixtures.ListSessionFixturesByVersion(ctx, 100, 100, nil)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 100 {
		t.Fatalf("page1 rows = %d, want 100", len(page1))
	}
	if !hasMore {
		t.Fatal("page1 hasMore must be true (105 facts > 100 limit); a false here is silent truncation")
	}
	// Newest first: page1[0] is the newest (last appended), page1[99] the 100th.
	if page1[0].ID != lastID {
		t.Fatalf("page1 newest = %d, want %d", page1[0].ID, lastID)
	}
	cursor := page1[len(page1)-1].ID

	// Second page: before=cursor -> the remaining 5, hasMore=false.
	page2, hasMore2, err := repo.SessionFixtures.ListSessionFixturesByVersion(ctx, 100, 100, &cursor)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != total-100 {
		t.Fatalf("page2 rows = %d, want %d (the remainder)", len(page2), total-100)
	}
	if hasMore2 {
		t.Fatal("page2 hasMore must be false (all facts consumed)")
	}
	// No overlap and no loss: every page2 id is below the cursor, and the union
	// covers all 105 distinct facts.
	seen := make(map[int64]bool, total)
	for _, f := range page1 {
		seen[f.ID] = true
	}
	for _, f := range page2 {
		if f.ID >= cursor {
			t.Fatalf("page2 id %d must be below cursor %d", f.ID, cursor)
		}
		if seen[f.ID] {
			t.Fatalf("page2 id %d overlaps page1", f.ID)
		}
		seen[f.ID] = true
	}
	if len(seen) != total {
		t.Fatalf("union covers %d facts, want %d (silent loss)", len(seen), total)
	}
}

// TestSessionFixtureValidation: the store rejects an invalid fact before any
// write (empty role / empty ref / non-positive ids).
func TestSessionFixtureValidation(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()
	valid := execution.SessionFixture{
		VersionID: 100, SUTID: 9001, EnvironmentID: 7001, Role: "admin",
		StorageStateRef: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}
	for name, mutate := range map[string]func(*execution.SessionFixture){
		"empty role": func(f *execution.SessionFixture) { f.Role = "" },
		"empty ref":  func(f *execution.SessionFixture) { f.StorageStateRef = "" },
		"zero sut":   func(f *execution.SessionFixture) { f.SUTID = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			bad := valid
			mutate(&bad)
			if _, err := repo.SessionFixtures.AppendSessionFixture(ctx, bad); err == nil {
				t.Fatalf("%s must be rejected", name)
			}
		})
	}
}

// TestSUTContextFixtureReference pins the reuse link column (SC-F5): the
// case_run_sut_context row records which session_fixture it used, the reference
// round-trips through Create/UpdateCAS/List, and the within-context FK rejects a
// dangling fixture id.
func TestSUTContextFixtureReference(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()
	newResolutionCaseRun(t, repo, 9401)

	fixture, err := repo.SessionFixtures.AppendSessionFixture(ctx, execution.SessionFixture{
		VersionID: 100, SUTID: 9001, EnvironmentID: 7001, Role: "admin",
		StorageStateRef: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	})
	if err != nil {
		t.Fatalf("append fixture: %v", err)
	}

	row := execution.CaseRunSUTContext{
		CaseRunID: 9401, SUTID: 9001, AccountRole: "admin",
		SessionID: "sess-fixture", LeaseEpoch: 1, Status: execution.SUTContextActive,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := repo.SUTContexts.CreateSUTContext(ctx, row); err != nil {
		t.Fatalf("create sut context: %v", err)
	}
	rows, err := repo.SUTContexts.ListByCaseRun(ctx, 9401)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list contexts = %+v err=%v", rows, err)
	}
	if rows[0].SessionFixtureID != nil {
		t.Fatalf("fresh context must have no fixture reference, got %v", rows[0].SessionFixtureID)
	}

	ok, err := repo.SUTContexts.UpdateCAS(ctx, rows[0].ID, func(target *execution.CaseRunSUTContext) error {
		target.SessionFixtureID = &fixture.ID
		return nil
	})
	if err != nil || !ok {
		t.Fatalf("UpdateCAS link: ok=%v err=%v", ok, err)
	}
	rows, err = repo.SUTContexts.ListByCaseRun(ctx, 9401)
	if err != nil || len(rows) != 1 {
		t.Fatalf("re-list contexts = %+v err=%v", rows, err)
	}
	if rows[0].SessionFixtureID == nil || *rows[0].SessionFixtureID != fixture.ID {
		t.Fatalf("fixture reference = %v, want %d", rows[0].SessionFixtureID, fixture.ID)
	}

	// A dangling fixture id violates the within-context FK.
	bogus := int64(999999999)
	dangling := execution.CaseRunSUTContext{
		CaseRunID: 9401, SUTID: 9002, AccountRole: "viewer",
		SessionID: "sess-dangling", SessionFixtureID: &bogus, LeaseEpoch: 1,
		Status: execution.SUTContextActive, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	err = repo.SUTContexts.CreateSUTContext(ctx, dangling)
	if !errors.Is(err, execution.ErrNotFound) {
		t.Fatalf("dangling fixture FK must map to ErrNotFound, got %v", err)
	}
}
