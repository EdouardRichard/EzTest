//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
)

// versionTestCommit is a valid Git commit id for the binding snapshots.
const versionTestCommit = "0123456789abcdef0123456789abcdef01234567"

// insertVersion creates one version through the repository and returns the
// stored row. The caller supplies the explicit Snowflake id.
func insertVersion(t *testing.T, repo *postgres.VersionRepository, v domain.Version) domain.Version {
	t.Helper()
	if v.Status == "" {
		v.Status = domain.VersionDraft
	}
	if v.Revision == 0 {
		v.Revision = 1
	}
	if err := repo.Create(context.Background(), &v); err != nil {
		t.Fatalf("create version %d: %v", v.ID, err)
	}
	return v
}

// assertVersionKind asserts err is a *domain.Error with kind and reason.
func assertVersionKind(t *testing.T, err error, kind domain.ErrorKind, reason string) *domain.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("want *domain.Error kind=%s reason=%s, got nil", kind, reason)
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("want *domain.Error, got %T (%v)", err, err)
	}
	if domainErr.Kind != kind || domainErr.Reason != reason {
		t.Fatalf("error kind/reason = %s/%s, want %s/%s (%v)",
			domainErr.Kind, domainErr.Reason, kind, reason, err)
	}
	return domainErr
}

// assertVersionMatches compares versions ignoring the server-managed timestamps.
func assertVersionMatches(t *testing.T, got, want domain.Version) {
	t.Helper()
	got.CreatedAt, got.UpdatedAt = time.Time{}, time.Time{}
	want.CreatedAt, want.UpdatedAt = time.Time{}, time.Time{}
	if got.ID != want.ID || got.Name != want.Name || got.Label != want.Label ||
		got.Description != want.Description || got.Status != want.Status ||
		!sameVersionInt64(got.SystemID, want.SystemID) ||
		!sameVersionInt64(got.EnvironmentID, want.EnvironmentID) ||
		!sameVersionInt64(got.CodeRepositoryID, want.CodeRepositoryID) ||
		!sameVersionString(got.CodeBranch, want.CodeBranch) ||
		!sameVersionString(got.CodeBaselineCommit, want.CodeBaselineCommit) ||
		got.Revision != want.Revision || got.CreatedBy != want.CreatedBy || got.UpdatedBy != want.UpdatedBy {
		t.Fatalf("version = %+v\nwant %+v", got, want)
	}
}

func sameVersionInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameVersionString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func TestVersionRepositoryRoundTripAndList(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewVersionRepository(pool)

	// A minimal draft round-trips through the repository.
	minimal := insertVersion(t, repo, domain.Version{
		ID: 1001, Name: "Order v2", CreatedBy: 7, UpdatedBy: 7,
	})
	if minimal.CreatedAt.IsZero() || minimal.UpdatedAt.IsZero() {
		t.Fatalf("created timestamps not populated: %+v", minimal)
	}
	loaded, err := repo.Get(ctx, minimal.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	assertVersionMatches(t, *loaded, minimal)
	if !loaded.CreatedAt.Equal(minimal.CreatedAt) || !loaded.UpdatedAt.Equal(minimal.UpdatedAt) {
		t.Fatalf("timestamps did not round-trip: %v/%v vs %v/%v",
			loaded.CreatedAt, loaded.UpdatedAt, minimal.CreatedAt, minimal.UpdatedAt)
	}

	// A fully populated version round-trips every binding column.
	full := insertVersion(t, repo, domain.Version{
		ID: 1002, Name: "Shop v3", Label: "v3", Description: "shop rewrite",
		SystemID: ptrInt64(7), EnvironmentID: ptrInt64(8), CodeRepositoryID: ptrInt64(9),
		CodeBranch: ptrString("release/3.0"), CodeBaselineCommit: ptrString(versionTestCommit),
		CreatedBy: 7, UpdatedBy: 8,
	})
	loadedFull, err := repo.Get(ctx, full.ID)
	if err != nil {
		t.Fatalf("Get full: %v", err)
	}
	assertVersionMatches(t, *loadedFull, full)

	insertVersion(t, repo, domain.Version{ID: 1003, Name: "Third", CreatedBy: 7, UpdatedBy: 7})

	// Default ordering is updated_at DESC, id DESC.
	page, more, err := repo.List(ctx, domain.VersionFilter{Page: domain.Page{Limit: 2}})
	if err != nil {
		t.Fatalf("List first page: %v", err)
	}
	if len(page) != 2 || page[0].ID != 1003 || page[1].ID != 1002 || !more {
		t.Fatalf("first page ids=%v more=%v", versionIDs(page), more)
	}
	next, more, err := repo.List(ctx, domain.VersionFilter{Page: domain.Page{
		Limit: 2, Cursor: platform.EncodeCursor(page[len(page)-1].ID),
	}})
	if err != nil {
		t.Fatalf("List second page: %v", err)
	}
	if len(next) != 1 || next[0].ID != 1001 || more {
		t.Fatalf("second page ids=%v more=%v", versionIDs(next), more)
	}

	// Status filtering is applied before pagination.
	if _, err := repo.UpdateStatus(ctx, 1001, domain.VersionDraft, domain.VersionClarifying, 7); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	clarifying, more, err := repo.List(ctx, domain.VersionFilter{
		Status: domain.VersionClarifying,
		Page:   domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatalf("List clarifying: %v", err)
	}
	if len(clarifying) != 1 || clarifying[0].ID != 1001 || more {
		t.Fatalf("clarifying ids=%v more=%v", versionIDs(clarifying), more)
	}
	empty, more, err := repo.List(ctx, domain.VersionFilter{
		Status: "archived",
		Page:   domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatalf("List archived: %v", err)
	}
	if len(empty) != 0 || more {
		t.Fatalf("archived ids=%v more=%v", versionIDs(empty), more)
	}

	// A zero limit is a programming error, not an empty page.
	if _, _, err := repo.List(ctx, domain.VersionFilter{Page: domain.Page{Limit: 0}}); err == nil {
		t.Fatal("List accepted a zero limit")
	}

	// Missing rows are not-found reads.
	if _, err := repo.Get(ctx, 999999); err != nil {
		assertVersionKind(t, err, domain.KindNotFound, "not_found")
	}
}

func TestVersionRepositoryCursorAcrossTimestamps(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewVersionRepository(pool)

	for _, id := range []int64{4001, 4002, 4003} {
		insertVersion(t, repo, domain.Version{ID: id, Name: "Cursor order", CreatedBy: 1, UpdatedBy: 1})
	}
	base := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	for id, at := range map[int64]time.Time{
		4001: base.Add(3 * time.Minute),
		4002: base.Add(2 * time.Minute),
		4003: base.Add(time.Minute),
	} {
		if _, err := pool.Exec(ctx, `UPDATE versions SET updated_at = $2 WHERE id = $1`, id, at); err != nil {
			t.Fatalf("set updated_at for %d: %v", id, err)
		}
	}

	page, more, err := repo.List(ctx, domain.VersionFilter{Page: domain.Page{Limit: 3}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := versionIDs(page); len(got) != 3 || got[0] != 4001 || got[1] != 4002 || got[2] != 4003 || more {
		t.Fatalf("updated_at ordering ids=%v more=%v", got, more)
	}
	next, more, err := repo.List(ctx, domain.VersionFilter{Page: domain.Page{
		Limit: 3, Cursor: platform.EncodeCursor(4002),
	}})
	if err != nil {
		t.Fatalf("List after cursor: %v", err)
	}
	if got := versionIDs(next); len(got) != 1 || got[0] != 4003 || more {
		t.Fatalf("cursor page ids=%v more=%v", got, more)
	}

	// Equal updated_at values fall back to id DESC and the composite keyset.
	if _, err := pool.Exec(ctx,
		`UPDATE versions SET updated_at = $2 WHERE id = ANY($1)`,
		[]int64{4002, 4003}, base.Add(2*time.Minute)); err != nil {
		t.Fatalf("tie updated_at: %v", err)
	}
	page, _, err = repo.List(ctx, domain.VersionFilter{Page: domain.Page{Limit: 3}})
	if err != nil {
		t.Fatalf("List tie: %v", err)
	}
	if got := versionIDs(page); len(got) != 3 || got[0] != 4001 || got[1] != 4003 || got[2] != 4002 {
		t.Fatalf("tie ordering ids=%v, want [4001 4003 4002]", got)
	}
}

func TestVersionRepositoryMetadataAndBindingsCAS(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewVersionRepository(pool)

	stored := insertVersion(t, repo, domain.Version{
		ID: 2001, Name: "Old name", Label: "old", Description: "old",
		CreatedBy: 7, UpdatedBy: 7,
	})

	// Metadata is a revision CAS: the matching revision bumps to 2.
	updated, err := repo.UpdateMetadata(ctx, stored.ID, 1, "New name", "v2", "new", 42)
	if err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if updated.Name != "New name" || updated.Label != "v2" || updated.Description != "new" ||
		updated.Revision != 2 || updated.UpdatedBy != 42 || updated.Status != domain.VersionDraft {
		t.Fatalf("updated metadata = %+v", updated)
	}
	reloaded, err := repo.Get(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Get after metadata: %v", err)
	}
	if reloaded.Name != "New name" || reloaded.Revision != 2 {
		t.Fatalf("reloaded = %+v", reloaded)
	}

	// A stale revision never writes.
	if _, err := repo.UpdateMetadata(ctx, stored.ID, 1, "Stale", "", "", 42); err != nil {
		assertVersionKind(t, err, domain.KindStateConflict, "revision_conflict")
	}
	afterStale, err := repo.Get(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Get after stale: %v", err)
	}
	if afterStale.Name != "New name" || afterStale.Revision != 2 {
		t.Fatalf("stale metadata update wrote the row: %+v", afterStale)
	}

	// Bindings are a revision CAS and replace the whole set.
	bindings := domain.VersionBindings{
		SystemID: ptrInt64(7), EnvironmentID: ptrInt64(8), CodeRepositoryID: ptrInt64(9),
		CodeBranch: ptrString("main"), CodeBaselineCommit: ptrString(versionTestCommit),
	}
	bound, err := repo.UpdateBindings(ctx, stored.ID, 2, bindings, 43)
	if err != nil {
		t.Fatalf("UpdateBindings: %v", err)
	}
	if bound.Revision != 3 || bound.SystemID == nil || *bound.SystemID != 7 ||
		bound.CodeRepositoryID == nil || *bound.CodeRepositoryID != 9 ||
		bound.CodeBranch == nil || *bound.CodeBranch != "main" ||
		bound.CodeBaselineCommit == nil || *bound.CodeBaselineCommit != versionTestCommit ||
		bound.UpdatedBy != 43 {
		t.Fatalf("bound = %+v", bound)
	}
	if _, err := repo.UpdateBindings(ctx, stored.ID, 2, bindings, 43); err != nil {
		assertVersionKind(t, err, domain.KindStateConflict, "revision_conflict")
	}

	// Designing blocks binding replacement even with the right revision.
	if _, err := repo.UpdateStatus(ctx, stored.ID, domain.VersionDraft, domain.VersionClarifying, 43); err != nil {
		t.Fatalf("UpdateStatus to clarifying: %v", err)
	}
	if _, err := repo.UpdateStatus(ctx, stored.ID, domain.VersionClarifying, domain.VersionDesigning, 43); err != nil {
		t.Fatalf("UpdateStatus to designing: %v", err)
	}
	designing, err := repo.Get(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Get designing: %v", err)
	}
	_, err = repo.UpdateBindings(ctx, stored.ID, designing.Revision, domain.VersionBindings{}, 43)
	assertVersionKind(t, err, domain.KindStateConflict, "version_state_conflict")
	unchanged, err := repo.Get(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Get after blocked binding: %v", err)
	}
	if unchanged.Revision != designing.Revision || unchanged.SystemID == nil || *unchanged.SystemID != 7 {
		t.Fatalf("blocked binding update wrote the row: %+v", unchanged)
	}

	// Missing rows on CAS writes are reported as conflicts, matching the
	// repository port contract.
	if _, err := repo.UpdateMetadata(ctx, 999999, 1, "Missing", "", "", 43); err != nil {
		assertVersionKind(t, err, domain.KindStateConflict, "revision_conflict")
	}
	if _, err := repo.UpdateBindings(ctx, 999999, 1, domain.VersionBindings{}, 43); err != nil {
		assertVersionKind(t, err, domain.KindStateConflict, "revision_conflict")
	}
	if _, err := repo.UpdateStatus(ctx, 999999, domain.VersionDraft, domain.VersionClarifying, 43); err != nil {
		assertVersionKind(t, err, domain.KindStateConflict, "version_state_conflict")
	}

	// Duplicate explicit Snowflake ids are a uniqueness conflict.
	duplicate := &domain.Version{ID: 2001, Name: "Duplicate", Status: domain.VersionDraft, Revision: 1, CreatedBy: 7, UpdatedBy: 7}
	assertVersionKind(t, repo.Create(ctx, duplicate), domain.KindConflict, "duplicate_key")
}

func TestVersionRepositoryStatusCountsAndUnclarified(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewVersionRepository(pool)

	stored := insertVersion(t, repo, domain.Version{ID: 3001, Name: "Order v2", CreatedBy: 7, UpdatedBy: 7})

	counts, err := repo.Counts(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Counts empty: %v", err)
	}
	if counts != (domain.VersionCounts{}) {
		t.Fatalf("empty counts = %+v", counts)
	}
	ids, open, err := repo.ListUnclarified(ctx, stored.ID)
	if err != nil {
		t.Fatalf("ListUnclarified empty: %v", err)
	}
	if len(ids) != 0 || open != 0 {
		t.Fatalf("empty warnings ids=%v open=%d", ids, open)
	}

	// Two requirements (one draft, one clarified) and three questions. The
	// version-level open question counts toward both Counts and the enter-design
	// warnings; requirement-level open questions only count toward Counts.
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirements (id, version_id, title, status, created_by, updated_by)
		VALUES (3101, $1, 'Draft requirement', 'draft', 7, 7),
		       (3102, $1, 'Clarified requirement', 'clarified', 7, 7)`, stored.ID); err != nil {
		t.Fatalf("insert requirements: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO clarification_questions
			(id, version_id, requirement_id, question, status, answer, created_by)
		VALUES (3201, $1, NULL, 'Version-level open?', 'open', '', 7),
		       (3202, $1, 3101, 'Requirement-level open?', 'open', '', 7),
		       (3203, $1, 3102, 'Answered?', 'answered', 'yes', 7)`, stored.ID); err != nil {
		t.Fatalf("insert questions: %v", err)
	}

	counts, err = repo.Counts(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if counts.RequirementCount != 2 || counts.ClarifiedCount != 1 || counts.OpenQuestionCount != 2 {
		t.Fatalf("counts = %+v, want 2/1/2", counts)
	}
	ids, open, err = repo.ListUnclarified(ctx, stored.ID)
	if err != nil {
		t.Fatalf("ListUnclarified: %v", err)
	}
	if len(ids) != 1 || ids[0] != 3101 || open != 1 {
		t.Fatalf("warnings ids=%v open=%d, want [3101]/1", ids, open)
	}

	// Status is a status CAS: the first draft->clarifying transition wins.
	updated, err := repo.UpdateStatus(ctx, stored.ID, domain.VersionDraft, domain.VersionClarifying, 42)
	if err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if updated.Status != domain.VersionClarifying || updated.Revision != 2 || updated.UpdatedBy != 42 {
		t.Fatalf("status update = %+v", updated)
	}
	if _, err := repo.UpdateStatus(ctx, stored.ID, domain.VersionDraft, domain.VersionClarifying, 42); err != nil {
		assertVersionKind(t, err, domain.KindStateConflict, "version_state_conflict")
	}

	// Requirement operations never change Version.status (INV-4): confirm,
	// reopen and delete requirements directly and re-read the version.
	if _, err := pool.Exec(ctx, `UPDATE requirements SET status = 'clarified', confirmed_at = now(), confirmed_by = 7 WHERE id = 3101`); err != nil {
		t.Fatalf("confirm requirement: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE requirements SET status = 'draft', confirmed_at = NULL, confirmed_by = NULL WHERE id = 3101`); err != nil {
		t.Fatalf("reopen requirement: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM requirements WHERE id = 3102`); err != nil {
		t.Fatalf("delete requirement: %v", err)
	}
	afterOps, err := repo.Get(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Get after requirement ops: %v", err)
	}
	if afterOps.Status != domain.VersionClarifying || afterOps.Revision != 2 {
		t.Fatalf("requirement ops changed the version: %+v", afterOps)
	}
}

func TestVersionRepositoryConcurrentMetadataCAS(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewVersionRepository(pool)

	stored := insertVersion(t, repo, domain.Version{ID: 5001, Name: "Race", CreatedBy: 7, UpdatedBy: 7})

	// Two writers holding the same revision: exactly one wins, the other gets
	// revision_conflict, and the winner's revision is stored.
	const writers = 2
	results := make(chan error, writers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			_, err := repo.UpdateMetadata(ctx, stored.ID, 1, strings.Repeat("w", i+1), "", "", int64(i+1))
			results <- err
		}(i)
	}
	var successes, conflicts int
	for i := 0; i < writers; i++ {
		err := <-results
		switch {
		case err == nil:
			successes++
		default:
			assertVersionKind(t, err, domain.KindStateConflict, "revision_conflict")
			conflicts++
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes/conflicts = %d/%d, want 1/1", successes, conflicts)
	}
	final, err := repo.Get(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Get final: %v", err)
	}
	if final.Revision != 2 {
		t.Fatalf("final revision = %d, want 2", final.Revision)
	}
}

// versionIDs projects the ids of a version page for readable failures.
func versionIDs(versions []domain.Version) []int64 {
	ids := make([]int64, len(versions))
	for i, v := range versions {
		ids[i] = v.ID
	}
	return ids
}

func ptrInt64(v int64) *int64    { return &v }
func ptrString(v string) *string { return &v }
