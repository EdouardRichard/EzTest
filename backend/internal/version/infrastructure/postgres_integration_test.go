//go:build integration

package versionpg_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure"
)

var testTime = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

const (
	baselineCommit      = "0123456789abcdef0123456789abcdef01234567"
	otherBaselineCommit = "fedcba9876543210fedcba9876543210fedcba98"
)

func newIDs(t *testing.T) *id.Snowflake {
	t.Helper()
	generator, err := id.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	return generator
}

func ptr[T any](value T) *T { return &value }

// initialRef is the reference projection of a freshly created sampleVersion:
// version.Create writes status initial and the stage column defaults to created.
func initialRef(id int64, title string) version.ReferencingVersion {
	return version.ReferencingVersion{
		ID: id, Title: title, Status: version.StatusInitial, Stage: version.StageCreated,
	}
}

func sampleVersion(ids *id.Snowflake, actorID int64) version.Version {
	return version.Version{
		ID:          ids.Next(),
		Title:       "Orders Q4",
		Description: "Q4 flows",
		Goal:        "verify checkout",
		Status:      version.StatusInitial,
		CreatedBy:   actorID,
		CreatedAt:   testTime,
		UpdatedAt:   testTime,
	}
}

// sampleBinding is the v0.22 environment-only binding: no single repository
// column, exclusions as a JSON document, version-level overrides and the global
// account subset.
func sampleBinding(ids *id.Snowflake, versionID int64) version.Binding {
	return version.Binding{
		ID:                 ids.Next(),
		VersionID:          versionID,
		SUTID:              1001,
		EnvironmentID:      1101,
		CodeBaselineCommit: baselineCommit,
		ScopeExclude: version.NewScopeExclude(
			[]int64{1301}, []int64{1401}, []int64{1501},
		),
		RepositoryOverrides: []version.RepositoryOverride{
			{RepoID: 1201, Included: false},
			{RepoID: 1203, Included: true},
		},
		Accounts: []version.AccountBinding{{AccountID: 1601}, {AccountID: 1602}},
	}
}

// sampleBindings keeps the unrelated integration fixtures (conversation,
// requirement, closure, attachment) compiling: a v0.22 version has exactly one
// environment-derived binding.
func sampleBindings(ids *id.Snowflake, versionID int64) []version.Binding {
	return []version.Binding{sampleBinding(ids, versionID)}
}

func TestRepositoryCreateGetListUpdate(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.New(pool, ids)
	ctx := context.Background()

	v := sampleVersion(ids, 42)
	v.AutoDesignNewRequirements = true
	binding := sampleBinding(ids, v.ID)
	if err := repo.Create(ctx, v, []version.Binding{binding}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	stored, storedBindings, err := repo.Get(ctx, v.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Title != v.Title || stored.Description != "Q4 flows" || stored.Goal != "verify checkout" {
		t.Fatalf("stored version = %#v", stored)
	}
	if stored.Status != version.StatusInitial || !stored.AutoDesignNewRequirements {
		t.Fatalf("stored status = %#v", stored)
	}
	if stored.CreatedBy != 42 || !stored.CreatedAt.Equal(testTime) || !stored.UpdatedAt.Equal(testTime) {
		t.Fatalf("stored audit columns = %#v", stored)
	}
	if len(storedBindings) != 1 {
		t.Fatalf("bindings = %#v", storedBindings)
	}

	first := storedBindings[0]
	if first.ID != binding.ID || first.SUTID != 1001 || first.EnvironmentID != 1101 {
		t.Fatalf("binding = %#v", first)
	}
	if first.CodeBaselineCommit != baselineCommit {
		t.Fatalf("code baseline commit = %q", first.CodeBaselineCommit)
	}
	if len(first.ScopeExclude.Pages) != 1 || first.ScopeExclude.Pages[0] != 1301 ||
		len(first.ScopeExclude.APIs) != 1 || first.ScopeExclude.APIs[0] != 1401 ||
		len(first.ScopeExclude.Databases) != 1 || first.ScopeExclude.Databases[0] != 1501 {
		t.Fatalf("scope_exclude = %#v", first.ScopeExclude)
	}
	if len(first.RepositoryOverrides) != 2 ||
		first.RepositoryOverrides[0].RepoID != 1201 || first.RepositoryOverrides[0].Included ||
		first.RepositoryOverrides[1].RepoID != 1203 || !first.RepositoryOverrides[1].Included {
		t.Fatalf("repository overrides = %#v", first.RepositoryOverrides)
	}
	if len(first.Accounts) != 2 || first.Accounts[0].AccountID != 1601 || first.Accounts[1].AccountID != 1602 {
		t.Fatalf("accounts = %#v", first.Accounts)
	}
	t.Logf("created version=%d binding=%d sut=%d environment=%d scope_exclude=%#v overrides=%#v accounts=%#v",
		v.ID, first.ID, first.SUTID, first.EnvironmentID,
		first.ScopeExclude, first.RepositoryOverrides, first.Accounts)

	// Raw rows: the scope_exclude JSONB shape and the version-level tables.
	var scopeRaw []byte
	if err := pool.QueryRow(ctx, `SELECT scope_exclude FROM version_sut WHERE id = $1`, first.ID).Scan(&scopeRaw); err != nil {
		t.Fatalf("query scope_exclude: %v", err)
	}
	var scope map[string]any
	if err := json.Unmarshal(scopeRaw, &scope); err != nil {
		t.Fatalf("decode scope_exclude: %v", err)
	}
	pages, _ := scope["pages"].([]any)
	if len(pages) != 1 || pages[0].(float64) != 1301 {
		t.Fatalf("scope_exclude JSON = %s", scopeRaw)
	}
	if _, ok := scope["databases"].([]any); !ok {
		t.Fatalf("scope_exclude has no databases list: %s", scopeRaw)
	}
	t.Logf("persisted version_sut.scope_exclude = %s", scopeRaw)

	var overrideCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM version_repository_override WHERE version_id = $1`, v.ID).Scan(&overrideCount); err != nil {
		t.Fatalf("count overrides: %v", err)
	}
	if overrideCount != 2 {
		t.Fatalf("override rows = %d", overrideCount)
	}
	var accountCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM version_account WHERE version_id = $1 AND enabled`, v.ID).Scan(&accountCount); err != nil {
		t.Fatalf("count accounts: %v", err)
	}
	if accountCount != 2 {
		t.Fatalf("account rows = %d", accountCount)
	}
	// The 0.21 resource join table is no longer written by the v0.22 binding.
	var legacyCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM version_sut_resource`).Scan(&legacyCount); err != nil {
		t.Fatalf("count legacy resources: %v", err)
	}
	if legacyCount != 0 {
		t.Fatalf("legacy version_sut_resource rows = %d", legacyCount)
	}

	// A second version lands in the list with its own SUT count.
	secondVersion := sampleVersion(ids, 7)
	secondVersion.Title = "Billing Q4"
	secondBinding := sampleBinding(ids, secondVersion.ID)
	if err := repo.Create(ctx, secondVersion, []version.Binding{secondBinding}); err != nil {
		t.Fatalf("Create second: %v", err)
	}

	page, err := repo.List(ctx, 1, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page) != 1 || page[0].ID != secondVersion.ID || page[0].SUTCount != 1 {
		t.Fatalf("list = %#v", page)
	}
	older, err := repo.List(ctx, 10, &secondVersion.ID)
	if err != nil || len(older) != 1 || older[0].ID != v.ID || older[0].SUTCount != 1 {
		t.Fatalf("older = %#v, err = %v", older, err)
	}

	// Update only touches the editable version fields.
	updated := stored
	updated.Title = "Orders Q4 renamed"
	updated.AutoDesignNewRequirements = false
	updated.UpdatedAt = testTime.Add(time.Hour)
	if _, err := repo.Update(ctx, updated); err != nil {
		t.Fatalf("Update: %v", err)
	}
	reloaded, _, err := repo.Get(ctx, v.ID)
	if err != nil {
		t.Fatalf("Get updated: %v", err)
	}
	if reloaded.Title != "Orders Q4 renamed" || reloaded.AutoDesignNewRequirements {
		t.Fatalf("updated = %#v", reloaded)
	}
	if reloaded.Description != "Q4 flows" || reloaded.Goal != "verify checkout" {
		t.Fatalf("update dropped description/goal: %#v", reloaded)
	}
	if !reloaded.CreatedAt.Equal(testTime) || !reloaded.UpdatedAt.Equal(testTime.Add(time.Hour)) {
		t.Fatalf("timestamps = %v/%v", reloaded.CreatedAt, reloaded.UpdatedAt)
	}

	if _, _, err := repo.Get(ctx, 999999); !errors.Is(err, version.ErrNotFound) {
		t.Fatalf("missing Get = %v", err)
	}
	missing := updated
	missing.ID = 999999
	if _, err := repo.Update(ctx, missing); !errors.Is(err, version.ErrNotFound) {
		t.Fatalf("missing Update = %v", err)
	}
}

func TestRepositoryUpdateBinding(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.New(pool, ids)
	ctx := context.Background()

	v := sampleVersion(ids, 1)
	binding := sampleBinding(ids, v.ID)
	if err := repo.Create(ctx, v, []version.Binding{binding}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	next := binding
	next.EnvironmentID = 1103
	next.CodeBaselineCommit = otherBaselineCommit
	next.ScopeExclude = version.NewScopeExclude(nil, []int64{1402}, nil)
	next.RepositoryOverrides = []version.RepositoryOverride{{RepoID: 1202, Included: true}}
	next.Accounts = []version.AccountBinding{{AccountID: 1604}}
	expectedUpdatedAt := v.UpdatedAt
	v.UpdatedAt = testTime.Add(time.Hour)
	if err := repo.UpdateBinding(ctx, v, next, expectedUpdatedAt); err != nil {
		t.Fatalf("UpdateBinding: %v", err)
	}

	reloaded, bindings, err := repo.Get(ctx, v.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reloaded.UpdatedAt.Equal(testTime.Add(time.Hour)) {
		t.Fatalf("version updated_at = %v", reloaded.UpdatedAt)
	}
	stored := bindings[0]
	if stored.EnvironmentID != 1103 || stored.CodeBaselineCommit != otherBaselineCommit {
		t.Fatalf("binding = %#v", stored)
	}
	if len(stored.ScopeExclude.Pages) != 0 || len(stored.ScopeExclude.APIs) != 1 ||
		stored.ScopeExclude.APIs[0] != 1402 || len(stored.ScopeExclude.Databases) != 0 {
		t.Fatalf("scope_exclude = %#v", stored.ScopeExclude)
	}
	if len(stored.RepositoryOverrides) != 1 || stored.RepositoryOverrides[0].RepoID != 1202 ||
		!stored.RepositoryOverrides[0].Included {
		t.Fatalf("overrides = %#v", stored.RepositoryOverrides)
	}
	if len(stored.Accounts) != 1 || stored.Accounts[0].AccountID != 1604 {
		t.Fatalf("accounts = %#v", stored.Accounts)
	}

	missing := v
	missing.ID = 999999
	missingBinding := next
	missingBinding.VersionID = missing.ID
	if err := repo.UpdateBinding(ctx, missing, missingBinding, expectedUpdatedAt); !errors.Is(err, version.ErrNotFound) {
		t.Fatalf("missing UpdateBinding = %v", err)
	}

	// An optimistic write derived from the old snapshot loses deterministically
	// once another writer bumped version.updated_at.
	if err := repo.UpdateBinding(ctx, v, next, expectedUpdatedAt); !errors.Is(err, version.ErrBindingConflict) {
		t.Fatalf("stale UpdateBinding = %v, want ErrBindingConflict", err)
	}
}

// TestRepositoryUpdateBindingSerializesOnVersionRowLock proves the binding write
// takes the version row lock before rewriting the projection: a concurrent
// transaction holding that lock blocks the update until it commits.
func TestRepositoryUpdateBindingSerializesOnVersionRowLock(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.New(pool, ids)
	ctx := context.Background()

	v := sampleVersion(ids, 1)
	binding := sampleBinding(ids, v.ID)
	if err := repo.Create(ctx, v, []version.Binding{binding}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock holder: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM version WHERE id = $1 FOR UPDATE`, v.ID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("lock version row: %v", err)
	}
	expectedUpdatedAt := v.UpdatedAt
	v.UpdatedAt = testTime.Add(time.Hour)
	next := binding
	next.ScopeExclude = version.NewScopeExclude(nil, nil, nil)

	done := make(chan error, 1)
	go func() {
		done <- repo.UpdateBinding(ctx, v, next, expectedUpdatedAt)
	}()
	select {
	case err := <-done:
		_ = tx.Rollback(ctx)
		t.Fatalf("UpdateBinding did not wait for the version row lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("release lock: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("UpdateBinding after lock release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("UpdateBinding did not finish after the lock was released")
	}
	stored, storedBindings, err := repo.Get(ctx, v.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !stored.UpdatedAt.Equal(testTime.Add(time.Hour)) {
		t.Fatalf("version updated_at = %v", stored.UpdatedAt)
	}
	if len(storedBindings[0].ScopeExclude.Pages) != 0 {
		t.Fatalf("scope after serialized write = %#v", storedBindings[0].ScopeExclude)
	}
}

func TestRepositorySetCodeBaselineCommit(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.New(pool, ids)
	ctx := context.Background()

	v := sampleVersion(ids, 1)
	binding := sampleBinding(ids, v.ID)
	binding.CodeBaselineCommit = ""
	if err := repo.Create(ctx, v, []version.Binding{binding}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, storedBindings, err := repo.Get(ctx, v.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := storedBindings[0].CodeBaselineCommit; got != "" {
		t.Fatalf("initial code baseline commit = %q, want empty", got)
	}

	if err := repo.SetCodeBaselineCommit(ctx, binding.ID, baselineCommit); err != nil {
		t.Fatalf("SetCodeBaselineCommit: %v", err)
	}
	_, storedBindings, err = repo.Get(ctx, v.ID)
	if err != nil {
		t.Fatalf("Get after backfill: %v", err)
	}
	if got := storedBindings[0].CodeBaselineCommit; got != baselineCommit {
		t.Fatalf("backfilled code baseline commit = %q, want %q", got, baselineCommit)
	}

	if err := repo.SetCodeBaselineCommit(ctx, binding.ID, otherBaselineCommit); err != nil {
		t.Fatalf("SetCodeBaselineCommit replace: %v", err)
	}
	_, storedBindings, err = repo.Get(ctx, v.ID)
	if err != nil {
		t.Fatalf("Get after replace: %v", err)
	}
	if got := storedBindings[0].CodeBaselineCommit; got != otherBaselineCommit {
		t.Fatalf("replaced code baseline commit = %q", got)
	}

	if err := repo.SetCodeBaselineCommit(ctx, binding.ID, ""); err != nil {
		t.Fatalf("SetCodeBaselineCommit clear: %v", err)
	}
	_, storedBindings, err = repo.Get(ctx, v.ID)
	if err != nil {
		t.Fatalf("Get after clear: %v", err)
	}
	if got := storedBindings[0].CodeBaselineCommit; got != "" {
		t.Fatalf("cleared code baseline commit = %q", got)
	}
	var isNull bool
	if err := pool.QueryRow(ctx,
		`SELECT code_baseline_commit IS NULL FROM version_sut WHERE id = $1`, binding.ID).Scan(&isNull); err != nil {
		t.Fatalf("query cleared column: %v", err)
	}
	if !isNull {
		t.Fatal("cleared code baseline commit must be stored as NULL")
	}

	if err := repo.SetCodeBaselineCommit(ctx, 999999, baselineCommit); !errors.Is(err, version.ErrNotFound) {
		t.Fatalf("missing SetCodeBaselineCommit = %v", err)
	}
	if err := repo.SetCodeBaselineCommit(ctx, binding.ID, "not-a-sha"); err == nil {
		t.Fatal("malformed commit was persisted")
	}
}

func TestRepositoryDuplicateBindingRollsBack(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.New(pool, ids)
	ctx := context.Background()

	v := sampleVersion(ids, 1)
	binding := sampleBinding(ids, v.ID)
	duplicated := []version.Binding{
		binding,
		{
			ID:            ids.Next(),
			VersionID:     v.ID,
			SUTID:         binding.SUTID,
			EnvironmentID: binding.EnvironmentID,
			ScopeExclude:  version.NewScopeExclude(nil, nil, nil),
		},
	}
	if err := repo.Create(ctx, v, duplicated); !errors.Is(err, version.ErrDuplicate) {
		t.Fatalf("duplicate binding error = %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM version WHERE id = $1`, v.ID).Scan(&count); err != nil {
		t.Fatalf("count version: %v", err)
	}
	if count != 0 {
		t.Fatalf("version row survived the rolled-back transaction: %d", count)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM version_sut WHERE version_id = $1`, v.ID).Scan(&count); err != nil {
		t.Fatalf("count version_sut: %v", err)
	}
	if count != 0 {
		t.Fatalf("version_sut rows survived: %d", count)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM version_account WHERE version_id = $1`, v.ID).Scan(&count); err != nil {
		t.Fatalf("count version_account: %v", err)
	}
	if count != 0 {
		t.Fatalf("version_account rows survived: %d", count)
	}
}

// TestRepositoryUsageQueries pins the v0.22 delete-guard semantics: a
// page/API/database is "in use" while it is inside a bound environment's
// effective range (approved/active and not scope-excluded). An explicitly
// excluded, proposed, disabled or foreign-environment resource is not part of
// the version range, so the guard must not block its deletion.
func TestRepositoryUsageQueries(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.New(pool, ids)
	ctx := context.Background()

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	// The usage guards join the version binding to the resource catalog, so the
	// fixture needs the owning SUT/environment and the surrounding resources.
	exec(`INSERT INTO sut (id, name, intro, created_by) VALUES (1001, 'Orders', '', 42)`)
	exec(`INSERT INTO sut_environment (id, sut_id, name, base_url)
		VALUES (1101, 1001, 'prod', 'https://orders.example.test'),
		       (1102, 1001, 'staging', 'https://staging.example.test')`)
	exec(`INSERT INTO sut_page (id, sut_id, environment_id, name, url, route_template, status)
		VALUES (1301, 1001, 1101, 'excluded', 'https://orders.example.test/excluded', '/excluded', 'approved'),
		       (1302, 1001, 1101, 'in range', 'https://orders.example.test/in-range', '/in-range', 'approved'),
		       (1304, 1001, 1101, 'proposed', 'https://orders.example.test/proposed', '/proposed', 'proposed'),
		       (1305, 1001, 1102, 'staging', 'https://staging.example.test/page', '/staging-page', 'approved')`)
	exec(`INSERT INTO sut_api (id, sut_id, environment_id, method, path_template, name, status)
		VALUES (1401, 1001, 1101, 'GET', '/api/excluded', 'excluded', 'approved'),
		       (1403, 1001, 1101, 'GET', '/api/in-range', 'in range', 'approved'),
		       (1404, 1001, 1101, 'GET', '/api/proposed', 'proposed', 'proposed')`)
	exec(`INSERT INTO target_database (id, sut_id, environment_id, name, dsn, username, status)
		VALUES (1501, 1001, 1101, 'excluded', 'postgres://excluded', 'reader', 'active'),
		       (1503, 1001, 1101, 'in-range', 'postgres://in-range', 'reader', 'active'),
		       (1504, 1001, 1101, 'disabled', 'postgres://disabled', 'reader', 'disabled')`)

	// The SUT-level repository association backs RepositoryInUse for inherited
	// repositories; version_repository_override covers appended ones.
	exec(`INSERT INTO sut_repository (id, sut_id, repo_id, role, enabled)
		VALUES (7001, 1001, 1201, 'backend', true)`)

	checks := []struct {
		name  string
		check func(context.Context, int64) (bool, error)
	}{
		{"sut", repo.SUTInUse},
		{"environment", repo.EnvironmentInUse},
		{"page", repo.PageInUse},
		{"api", repo.APIInUse},
		{"database", repo.DatabaseInUse},
		{"account", repo.AccountInUse},
		{"repository", repo.RepositoryInUse},
	}
	for _, check := range checks {
		if used, err := check.check(ctx, 777777); err != nil || used {
			t.Fatalf("%s before create = %v/%v", check.name, used, err)
		}
	}

	v := sampleVersion(ids, 1)
	binding := sampleBinding(ids, v.ID)
	if err := repo.Create(ctx, v, []version.Binding{binding}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	cases := []struct {
		name  string
		check func(context.Context, int64) (bool, error)
		id    int64
		want  bool
	}{
		{"sut bound", repo.SUTInUse, 1001, true},
		{"sut unbound", repo.SUTInUse, 1003, false},
		{"environment bound", repo.EnvironmentInUse, 1101, true},
		{"environment unbound", repo.EnvironmentInUse, 1102, false},
		// Excluded resources are outside the version range: the guard does not
		// block deleting them.
		{"page excluded", repo.PageInUse, 1301, false},
		{"page in range", repo.PageInUse, 1302, true},
		{"page proposed", repo.PageInUse, 1304, false},
		{"page other environment", repo.PageInUse, 1305, false},
		{"api excluded", repo.APIInUse, 1401, false},
		{"api in range", repo.APIInUse, 1403, true},
		{"api proposed", repo.APIInUse, 1404, false},
		{"database excluded", repo.DatabaseInUse, 1501, false},
		{"database in range", repo.DatabaseInUse, 1503, true},
		{"database disabled", repo.DatabaseInUse, 1504, false},
		{"account bound", repo.AccountInUse, 1601, true},
		{"account unbound", repo.AccountInUse, 1604, false},
		{"repository inherited", repo.RepositoryInUse, 1201, true},
		{"repository override", repo.RepositoryInUse, 1203, true},
		{"repository unbound", repo.RepositoryInUse, 1204, false},
	}
	for _, tc := range cases {
		used, err := tc.check(ctx, tc.id)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if used != tc.want {
			t.Fatalf("%s(%d) = %v, want %v", tc.name, tc.id, used, tc.want)
		}
	}
}

// TestRepositoryReferencingVersions pins the join used by the resource delete
// conflict details: every referencing version carries its title, the resource
// range matches the usage guards (status, environment, exclusions) and a
// repository is referenced through both the inherited SUT association and an
// explicit version override.
func TestRepositoryReferencingVersions(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.New(pool, ids)
	ctx := context.Background()

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	exec(`INSERT INTO sut (id, name, intro, created_by) VALUES (1001, 'Orders', '', 42)`)
	exec(`INSERT INTO sut_environment (id, sut_id, name, base_url)
		VALUES (1101, 1001, 'prod', 'https://orders.example.test'),
		       (1102, 1001, 'staging', 'https://staging.example.test')`)
	exec(`INSERT INTO sut_page (id, sut_id, environment_id, name, url, route_template, status)
		VALUES (1301, 1001, 1101, 'excluded', 'https://orders.example.test/excluded', '/excluded', 'approved'),
		       (1305, 1001, 1102, 'staging', 'https://staging.example.test/page', '/staging-page', 'approved')`)
	exec(`INSERT INTO sut_api (id, sut_id, environment_id, method, path_template, name, status)
		VALUES (1401, 1001, 1101, 'GET', '/api/excluded', 'excluded', 'approved'),
		       (1402, 1001, 1101, 'GET', '/api/in-range', 'in range', 'approved')`)
	exec(`INSERT INTO target_database (id, sut_id, environment_id, name, dsn, username, status)
		VALUES (1501, 1001, 1101, 'excluded', 'postgres://excluded', 'reader', 'active'),
		       (1502, 1001, 1101, 'in-range', 'postgres://in-range', 'reader', 'active')`)
	exec(`INSERT INTO sut_repository (id, sut_id, repo_id, role, enabled)
		VALUES (7001, 1001, 1201, 'backend', true)`)

	q4 := sampleVersion(ids, 42)
	q4.Title = "Orders Q4"
	q4Binding := sampleBinding(ids, q4.ID)
	q5 := sampleVersion(ids, 42)
	q5.Title = "Orders Q5"
	q5Binding := sampleBinding(ids, q5.ID)
	q5Binding.EnvironmentID = 1102
	q5Binding.ScopeExclude = version.NewScopeExclude(nil, nil, nil)
	q5Binding.RepositoryOverrides = nil
	q5Binding.Accounts = nil
	if err := repo.Create(ctx, q4, []version.Binding{q4Binding}); err != nil {
		t.Fatalf("Create Q4: %v", err)
	}
	if err := repo.Create(ctx, q5, []version.Binding{q5Binding}); err != nil {
		t.Fatalf("Create Q5: %v", err)
	}

	cases := []struct {
		name string
		kind string
		id   int64
		want []version.ReferencingVersion
	}{
		{"sut both", "sut", 1001, []version.ReferencingVersion{
			initialRef(q4.ID, "Orders Q4"), initialRef(q5.ID, "Orders Q5"),
		}},
		{"sut unbound", "sut", 1003, nil},
		{"environment prod", "environment", 1101, []version.ReferencingVersion{initialRef(q4.ID, "Orders Q4")}},
		{"environment staging", "environment", 1102, []version.ReferencingVersion{initialRef(q5.ID, "Orders Q5")}},
		{"page excluded", "page", 1301, nil},
		{"page staging", "page", 1305, []version.ReferencingVersion{initialRef(q5.ID, "Orders Q5")}},
		{"api excluded", "api", 1401, nil},
		{"api prod", "api", 1402, []version.ReferencingVersion{initialRef(q4.ID, "Orders Q4")}},
		{"database excluded", "database", 1501, nil},
		{"database prod", "database", 1502, []version.ReferencingVersion{initialRef(q4.ID, "Orders Q4")}},
		{"account bound", "account", 1601, []version.ReferencingVersion{initialRef(q4.ID, "Orders Q4")}},
		{"account unbound", "account", 1604, nil},
		{"repository inherited", "repository", 1201, []version.ReferencingVersion{
			initialRef(q4.ID, "Orders Q4"), initialRef(q5.ID, "Orders Q5"),
		}},
		{"repository override", "repository", 1203, []version.ReferencingVersion{initialRef(q4.ID, "Orders Q4")}},
		{"repository unbound", "repository", 1204, nil},
	}
	for _, tc := range cases {
		refs, err := repo.ReferencingVersions(ctx, tc.kind, tc.id)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(refs) != len(tc.want) {
			t.Fatalf("%s = %#v, want %#v", tc.name, refs, tc.want)
		}
		for index := range refs {
			if refs[index] != tc.want[index] {
				t.Fatalf("%s[%d] = %#v, want %#v", tc.name, index, refs[index], tc.want[index])
			}
		}
	}
	if _, err := repo.ReferencingVersions(ctx, "unknown", 1); err == nil {
		t.Fatal("unknown resource kind returned no error")
	}
}
