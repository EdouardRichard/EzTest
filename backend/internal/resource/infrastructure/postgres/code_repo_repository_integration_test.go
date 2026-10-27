//go:build integration

package postgres_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// codeRepoDefaultExcludeGlobs mirrors the code_repositories DDL default that
// CreateCodeRepo applies when the caller leaves ExcludeGlobs nil.
var codeRepoDefaultExcludeGlobs = []string{
	".git/**", "node_modules/**", "dist/**", "build/**", "vendor/**",
}

// codeRepoTime returns a pointer for a nullable timestamp column. The value is
// microsecond-aligned so it round-trips through TIMESTAMPTZ unchanged.
func codeRepoTime(value time.Time) *time.Time {
	value = value.UTC().Truncate(time.Microsecond)
	return &value
}

// codeRepoUTCTime normalizes a nullable timestamp to UTC. pgx scans
// TIMESTAMPTZ in the connection's local zone, so fixtures created in UTC must
// be normalized too before reflect.DeepEqual can compare them.
func codeRepoUTCTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}

// assertCodeRepoMatches compares code repositories ignoring the server-managed
// timestamps; every other column, including pointer fields, must match.
func assertCodeRepoMatches(t *testing.T, got, want domain.CodeRepository) {
	t.Helper()
	got.CreatedAt, got.UpdatedAt = time.Time{}, time.Time{}
	want.CreatedAt, want.UpdatedAt = time.Time{}, time.Time{}
	got.LastValidatedAt = codeRepoUTCTime(got.LastValidatedAt)
	want.LastValidatedAt = codeRepoUTCTime(want.LastValidatedAt)
	got.LastIndexedAt = codeRepoUTCTime(got.LastIndexedAt)
	want.LastIndexedAt = codeRepoUTCTime(want.LastIndexedAt)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("code repo=%+v\nwant %+v", got, want)
	}
}

func TestCodeRepositoryRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewCodeRepository(pool)

	// A minimal code repo: CreateCodeRepo applies the DDL defaults for the
	// branch, globs, status and validation placeholders. Code repositories have
	// no foreign keys, so no fixture rows are required.
	minimal := &domain.CodeRepository{
		Key:       "eztest",
		Name:      "EzTest",
		LocalPath: `D:\Project_new\EzTest`,
	}
	if err := repo.CreateCodeRepo(ctx, minimal); err != nil {
		t.Fatal(err)
	}
	if minimal.ID == 0 || minimal.CreatedAt.IsZero() || minimal.UpdatedAt.IsZero() {
		t.Fatalf("created minimal code repo=%+v", minimal)
	}
	if minimal.FixedBranch != "main" {
		t.Fatalf("fixed_branch=%q want main", minimal.FixedBranch)
	}
	if minimal.Status != domain.StatusActive {
		t.Fatalf("status=%q want active", minimal.Status)
	}
	if minimal.LastCommit != "" || minimal.LastValidationError != "" {
		t.Fatalf("validation placeholders=%q/%q want empty", minimal.LastCommit, minimal.LastValidationError)
	}
	if minimal.LastValidatedAt != nil || minimal.LastIndexedAt != nil {
		t.Fatalf("nullable timestamps=%v/%v want nil", minimal.LastValidatedAt, minimal.LastIndexedAt)
	}
	if minimal.IncludeGlobs == nil || len(minimal.IncludeGlobs) != 0 {
		t.Fatalf("include_globs=%v want non-nil empty slice", minimal.IncludeGlobs)
	}
	if !reflect.DeepEqual(minimal.ExcludeGlobs, codeRepoDefaultExcludeGlobs) {
		t.Fatalf("exclude_globs=%v want %v", minimal.ExcludeGlobs, codeRepoDefaultExcludeGlobs)
	}

	loadedMinimal, err := repo.GetCodeRepo(ctx, minimal.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertCodeRepoMatches(t, *loadedMinimal, *minimal)
	if !loadedMinimal.CreatedAt.Equal(minimal.CreatedAt) || !loadedMinimal.UpdatedAt.Equal(minimal.UpdatedAt) {
		t.Fatalf("minimal timestamps=%v/%v want %v/%v",
			loadedMinimal.CreatedAt, loadedMinimal.UpdatedAt, minimal.CreatedAt, minimal.UpdatedAt)
	}

	// A fully populated code repo round-trips every column, including the
	// validation audit fields and nullable timestamps.
	now := time.Now()
	full := &domain.CodeRepository{
		Key:                 "shop",
		Name:                "Shop",
		LocalPath:           `C:\work\shop`,
		FixedBranch:         "release/2.1",
		IncludeGlobs:        []string{"src/**", "docs/**"},
		ExcludeGlobs:        []string{"vendor/**"},
		Status:              domain.StatusDisabled,
		LastCommit:          "0123456789abcdef0123456789abcdef01234567",
		LastValidatedAt:     codeRepoTime(now),
		LastValidationError: "branch_not_found",
		LastIndexedAt:       codeRepoTime(now.Add(-time.Hour)),
	}
	if err := repo.CreateCodeRepo(ctx, full); err != nil {
		t.Fatal(err)
	}
	if full.ID == 0 || full.ID == minimal.ID || full.CreatedAt.IsZero() || full.UpdatedAt.IsZero() {
		t.Fatalf("created full code repo=%+v", full)
	}
	loadedFull, err := repo.GetCodeRepo(ctx, full.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertCodeRepoMatches(t, *loadedFull, *full)
	if loadedFull.LastValidatedAt == nil || !loadedFull.LastValidatedAt.Equal(*full.LastValidatedAt) {
		t.Fatalf("last_validated_at=%v want %v", loadedFull.LastValidatedAt, full.LastValidatedAt)
	}
	if loadedFull.LastIndexedAt == nil || !loadedFull.LastIndexedAt.Equal(*full.LastIndexedAt) {
		t.Fatalf("last_indexed_at=%v want %v", loadedFull.LastIndexedAt, full.LastIndexedAt)
	}

	// Update rewrites every mutable column, including the last_commit audit
	// values the validate action produces; key and ID stay immutable.
	full.Name = "Shop v2"
	full.LocalPath = `C:\work\shop-v2`
	full.FixedBranch = "main"
	full.IncludeGlobs = []string{"src/**"}
	full.ExcludeGlobs = []string{"build/**"}
	full.Status = domain.StatusActive
	full.LastCommit = "fedcba9876543210fedcba9876543210fedcba98"
	full.LastValidatedAt = codeRepoTime(now.Add(time.Minute))
	full.LastValidationError = ""
	full.LastIndexedAt = codeRepoTime(now.Add(2 * time.Minute))
	if err := repo.UpdateCodeRepo(ctx, full); err != nil {
		t.Fatal(err)
	}
	reloadedFull, err := repo.GetCodeRepo(ctx, full.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertCodeRepoMatches(t, *reloadedFull, *full)
	if reloadedFull.Key != "shop" {
		t.Fatalf("key=%q want shop", reloadedFull.Key)
	}
	if reloadedFull.LastCommit != full.LastCommit {
		t.Fatalf("last_commit=%q want %q", reloadedFull.LastCommit, full.LastCommit)
	}
	if !reloadedFull.CreatedAt.Equal(full.CreatedAt) || !reloadedFull.UpdatedAt.After(full.CreatedAt) {
		t.Fatalf("updated timestamps=%v/%v created=%v",
			reloadedFull.UpdatedAt, reloadedFull.CreatedAt, full.CreatedAt)
	}

	// Delete removes one row; a second delete reports not found.
	if err := repo.DeleteCodeRepo(ctx, minimal.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetCodeRepo(ctx, minimal.ID); err == nil {
		t.Fatal("deleted code repo still readable")
	}
	assertCatalogKind(t, repo.DeleteCodeRepo(ctx, minimal.ID), domain.KindNotFound, "not_found")

	remaining, more, err := repo.ListCodeRepos(ctx, domain.CodeRepoFilter{Page: domain.Page{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || more || remaining[0].ID != full.ID {
		t.Fatalf("code repos after delete=%+v more=%v", remaining, more)
	}

	if err := repo.DeleteCodeRepo(ctx, full.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetCodeRepo(ctx, full.ID); err == nil {
		t.Fatal("deleted full code repo still readable")
	}
}

func TestCodeRepositoryPaginationAndFilters(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewCodeRepository(pool)

	codeRepos := []*domain.CodeRepository{
		{Key: "repo-one", Name: "One", LocalPath: `C:\work\one`},
		{Key: "repo-two", Name: "Two", LocalPath: `C:\work\two`},
		{Key: "repo-disabled", Name: "Disabled", LocalPath: `C:\work\three`, Status: domain.StatusDisabled},
	}
	for _, codeRepo := range codeRepos {
		if err := repo.CreateCodeRepo(ctx, codeRepo); err != nil {
			t.Fatal(err)
		}
	}

	first, more, err := repo.ListCodeRepos(ctx, domain.CodeRepoFilter{Page: domain.Page{Limit: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || !more || first[0].ID != codeRepos[0].ID || first[1].ID != codeRepos[1].ID {
		t.Fatalf("first page=%+v more=%v", first, more)
	}
	second, more, err := repo.ListCodeRepos(ctx, domain.CodeRepoFilter{
		Page: domain.Page{Limit: 2, AfterID: first[len(first)-1].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || more || second[0].ID != codeRepos[2].ID {
		t.Fatalf("second page=%+v more=%v", second, more)
	}
	all, more, err := repo.ListCodeRepos(ctx, domain.CodeRepoFilter{Page: domain.Page{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || more {
		t.Fatalf("all=%+v more=%v", all, more)
	}

	active, more, err := repo.ListCodeRepos(ctx, domain.CodeRepoFilter{
		Status: domain.StatusActive,
		Page:   domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 || more {
		t.Fatalf("active code repos=%+v more=%v", active, more)
	}
	disabled, more, err := repo.ListCodeRepos(ctx, domain.CodeRepoFilter{
		Status: domain.StatusDisabled,
		Page:   domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(disabled) != 1 || more || disabled[0].ID != codeRepos[2].ID {
		t.Fatalf("disabled code repos=%+v more=%v", disabled, more)
	}
	none, more, err := repo.ListCodeRepos(ctx, domain.CodeRepoFilter{
		Status: "archived",
		Page:   domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 || more {
		t.Fatalf("unknown status code repos=%+v more=%v", none, more)
	}

	if _, _, err := repo.ListCodeRepos(ctx, domain.CodeRepoFilter{Page: domain.Page{Limit: 0}}); err == nil {
		t.Fatal("ListCodeRepos accepted a zero limit")
	}
}

func TestCodeRepositoryConflictsAndNotFound(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewCodeRepository(pool)

	if err := repo.CreateCodeRepo(ctx, &domain.CodeRepository{
		Key: "dup", Name: "First", LocalPath: `C:\work\first`,
	}); err != nil {
		t.Fatal(err)
	}
	duplicate := &domain.CodeRepository{Key: "dup", Name: "Dup", LocalPath: `C:\work\dup`}
	assertCatalogKind(t, repo.CreateCodeRepo(ctx, duplicate), domain.KindConflict, "duplicate_key")
	if duplicate.ID != 0 {
		t.Fatalf("failed insert wrote back an ID: %+v", duplicate)
	}

	_, err := repo.GetCodeRepo(ctx, 999999)
	assertCatalogKind(t, err, domain.KindNotFound, "not_found")

	missingUpdate := &domain.CodeRepository{
		ID: 999999, Key: "missing", Name: "Missing", LocalPath: `C:\work\missing`,
		FixedBranch: "main", IncludeGlobs: []string{}, ExcludeGlobs: []string{},
		Status: domain.StatusActive,
	}
	assertCatalogKind(t, repo.UpdateCodeRepo(ctx, missingUpdate), domain.KindNotFound, "not_found")

	assertCatalogKind(t, repo.DeleteCodeRepo(ctx, 999999), domain.KindNotFound, "not_found")
}

// TestCodeRepositoryUpdateLastIndexedAt pins the narrow P4 index-state write
// (ruling R44): only last_indexed_at changes, a concurrent admin edit is never
// reverted, a disabled repository keeps the 422 code_repository_disabled reason
// and an unknown id stays not found.
func TestCodeRepositoryUpdateLastIndexedAt(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewCodeRepository(pool)

	indexedAt := codeRepoTime(time.Now().Add(-time.Hour))
	active := &domain.CodeRepository{
		Key:             "indexed",
		Name:            "Indexed",
		LocalPath:       `C:\work\indexed`,
		LastCommit:      "0123456789abcdef0123456789abcdef01234567",
		LastValidatedAt: codeRepoTime(time.Now().Add(-2 * time.Hour)),
	}
	if err := repo.CreateCodeRepo(ctx, active); err != nil {
		t.Fatal(err)
	}

	// A concurrent admin edit lands after the caller would have read the row;
	// the narrow UPDATE must preserve it instead of restoring stale columns.
	if _, err := pool.Exec(ctx,
		`UPDATE code_repositories SET name = 'Renamed', local_path = 'D:\work\moved' WHERE id = $1`,
		active.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateLastIndexedAt(ctx, active.ID, *indexedAt); err != nil {
		t.Fatalf("UpdateLastIndexedAt: %v", err)
	}
	loaded, err := repo.GetCodeRepo(ctx, active.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LastIndexedAt == nil || !loaded.LastIndexedAt.Equal(*indexedAt) {
		t.Fatalf("last_indexed_at=%v want %v", loaded.LastIndexedAt, indexedAt)
	}
	if loaded.Name != "Renamed" || loaded.LocalPath != `D:\work\moved` {
		t.Fatalf("concurrent edit lost: name=%q local_path=%q", loaded.Name, loaded.LocalPath)
	}
	if loaded.LastCommit != active.LastCommit {
		t.Fatalf("last_commit=%q want %q", loaded.LastCommit, active.LastCommit)
	}
	if loaded.LastValidatedAt == nil || !loaded.LastValidatedAt.Equal(*active.LastValidatedAt) {
		t.Fatalf("last_validated_at=%v want %v", loaded.LastValidatedAt, active.LastValidatedAt)
	}
	if loaded.Status != domain.StatusActive {
		t.Fatalf("status=%q want active", loaded.Status)
	}
	if !loaded.UpdatedAt.Equal(active.UpdatedAt) {
		t.Fatalf("updated_at=%v want %v: only last_indexed_at may change", loaded.UpdatedAt, active.UpdatedAt)
	}

	// A disabled repository keeps the documented reason and is never written.
	disabled := &domain.CodeRepository{
		Key: "indexed-disabled", Name: "Disabled", LocalPath: `C:\work\disabled`,
		Status: domain.StatusDisabled,
	}
	if err := repo.CreateCodeRepo(ctx, disabled); err != nil {
		t.Fatal(err)
	}
	assertCatalogKind(t, repo.UpdateLastIndexedAt(ctx, disabled.ID, time.Now()),
		domain.KindInvalid, "code_repository_disabled")
	loadedDisabled, err := repo.GetCodeRepo(ctx, disabled.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedDisabled.LastIndexedAt != nil {
		t.Fatalf("disabled last_indexed_at=%v want nil", loadedDisabled.LastIndexedAt)
	}

	// An unknown id is the existing not-found reason.
	assertCatalogKind(t, repo.UpdateLastIndexedAt(ctx, 999999, time.Now()), domain.KindNotFound, "not_found")
}
