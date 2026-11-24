//go:build integration

package versionpg_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	resource "github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure"
)

var testTime = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

// prodEnvironmentID is the environment the sample versions bind. v0.2.4-B: the
// binding is a column on the version row, so the fixtures only need the
// environment plus the account subset.
const prodEnvironmentID = int64(1101)

func newIDs(t *testing.T) *id.Snowflake {
	t.Helper()
	generator, err := id.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	return generator
}

// initialRef is the reference projection of a freshly created sampleVersion:
// version.Create writes status initial and the stage column defaults to created.
func initialRef(id int64, title string) version.ReferencingVersion {
	return version.ReferencingVersion{
		ID: id, Title: title, Status: version.StatusInitial, Stage: version.StageCreated,
	}
}

func sampleVersion(ids *id.Snowflake, actorID int64) version.Version {
	return version.Version{
		ID:            ids.Next(),
		Title:         "Orders Q4",
		Description:   "Q4 flows",
		Goal:          "verify checkout",
		Status:        version.StatusInitial,
		EnvironmentID: prodEnvironmentID,
		CreatedBy:     actorID,
		CreatedAt:     testTime,
		UpdatedAt:     testTime,
	}
}

// sampleAccounts is the version account subset: the only per-version resource
// selection that survives the removal of the version_sut chain.
func sampleAccounts() []version.AccountBinding {
	return []version.AccountBinding{{AccountID: 1601}, {AccountID: 1602}}
}

// sampleBindings keeps the unrelated integration fixtures (conversation,
// requirement, closure, attachment, stage) compiling: a version's persisted
// binding is now its environment_id column plus this account subset, so the
// generator and version id arguments are no longer needed.
func sampleBindings(_ *id.Snowflake, _ int64) []version.AccountBinding {
	return sampleAccounts()
}

func TestRepositoryCreateGetListUpdate(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.New(pool, ids)
	ctx := context.Background()

	v := sampleVersion(ids, 42)
	v.AutoDesignNewRequirements = true
	accounts := sampleAccounts()
	if err := repo.Create(ctx, v, accounts); err != nil {
		t.Fatalf("Create: %v", err)
	}

	stored, storedAccounts, err := repo.Get(ctx, v.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Title != v.Title || stored.Description != "Q4 flows" || stored.Goal != "verify checkout" {
		t.Fatalf("stored version = %#v", stored)
	}
	if stored.Status != version.StatusInitial || !stored.AutoDesignNewRequirements {
		t.Fatalf("stored status = %#v", stored)
	}
	if stored.EnvironmentID != prodEnvironmentID {
		t.Fatalf("stored environment binding = %d, want %d", stored.EnvironmentID, prodEnvironmentID)
	}
	if stored.CreatedBy != 42 || !stored.CreatedAt.Equal(testTime) || !stored.UpdatedAt.Equal(testTime) {
		t.Fatalf("stored audit columns = %#v", stored)
	}
	if len(storedAccounts) != 2 || storedAccounts[0].AccountID != 1601 || storedAccounts[1].AccountID != 1602 {
		t.Fatalf("accounts = %#v", storedAccounts)
	}
	t.Logf("created version=%d environment=%d accounts=%#v", v.ID, stored.EnvironmentID, storedAccounts)

	// Raw rows: the binding lives on the version row and the derived chain is gone.
	var environmentID int64
	if err := pool.QueryRow(ctx, `SELECT environment_id FROM version WHERE id = $1`, v.ID).Scan(&environmentID); err != nil {
		t.Fatalf("query version.environment_id: %v", err)
	}
	if environmentID != prodEnvironmentID {
		t.Fatalf("persisted environment_id = %d", environmentID)
	}
	for _, table := range []string{"version_sut", "version_sut_resource", "version_repository_override"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatalf("check %s: %v", table, err)
		}
		if exists {
			t.Fatalf("%s must be dropped by the v0.2.4-B environment binding", table)
		}
	}
	var accountCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM version_account WHERE version_id = $1 AND enabled`, v.ID).Scan(&accountCount); err != nil {
		t.Fatalf("count accounts: %v", err)
	}
	if accountCount != 2 {
		t.Fatalf("account rows = %d", accountCount)
	}

	// A second version lands in the list with its own derived SUT count.
	secondVersion := sampleVersion(ids, 7)
	secondVersion.Title = "Billing Q4"
	if err := repo.Create(ctx, secondVersion, sampleAccounts()); err != nil {
		t.Fatalf("Create second: %v", err)
	}

	page, err := repo.List(ctx, 1, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page) != 1 || page[0].ID != secondVersion.ID {
		t.Fatalf("list = %#v", page)
	}
	older, err := repo.List(ctx, 10, &secondVersion.ID)
	if err != nil || len(older) != 1 || older[0].ID != v.ID {
		t.Fatalf("older = %#v, err = %v", older, err)
	}

	// Update only touches the editable version fields; the environment binding is
	// not part of this write path.
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
	if reloaded.EnvironmentID != prodEnvironmentID {
		t.Fatalf("update dropped the environment binding: %#v", reloaded)
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

// TestRepositoryListDerivesSUTCount pins the derived summary column: SUTCount is
// the number of SUTs in the environment the version binds, read from the
// resource catalog rather than stored on the version.
func TestRepositoryListDerivesSUTCount(t *testing.T) {
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
	exec(`INSERT INTO environment (id, name, status) VALUES (1101, 'prod', 'active'), (1102, 'staging', 'active')`)
	exec(`INSERT INTO sut (id, environment_id, name, base_url, created_by)
		VALUES (1001, 1101, 'Orders', 'https://orders.example.test', 42),
		       (1002, 1101, 'Billing', 'https://billing.example.test', 42),
		       (1003, 1102, 'Shipping', 'https://shipping.example.test', 42)`)

	prod := sampleVersion(ids, 42)
	prod.Title = "prod version"
	staging := sampleVersion(ids, 42)
	staging.Title = "staging version"
	staging.EnvironmentID = 1102
	for _, entity := range []version.Version{prod, staging} {
		if err := repo.Create(ctx, entity, nil); err != nil {
			t.Fatalf("Create %q: %v", entity.Title, err)
		}
	}

	summaries, err := repo.List(ctx, 10, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("summaries = %#v", summaries)
	}
	byTitle := map[string]int{}
	for _, summary := range summaries {
		byTitle[summary.Title] = summary.SUTCount
	}
	if byTitle["prod version"] != 2 || byTitle["staging version"] != 1 {
		t.Fatalf("derived SUT counts = %#v", byTitle)
	}
}

func TestRepositoryUpdateBinding(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.New(pool, ids)
	ctx := context.Background()

	v := sampleVersion(ids, 1)
	accounts := sampleAccounts()
	if err := repo.Create(ctx, v, accounts); err != nil {
		t.Fatalf("Create: %v", err)
	}

	next := v
	next.EnvironmentID = 1102
	next.UpdatedAt = testTime.Add(time.Hour)
	nextAccounts := []version.AccountBinding{{AccountID: 1604}}
	expectedUpdatedAt := v.UpdatedAt
	if err := repo.UpdateBinding(ctx, next, nextAccounts, expectedUpdatedAt); err != nil {
		t.Fatalf("UpdateBinding: %v", err)
	}

	reloaded, storedAccounts, err := repo.Get(ctx, v.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reloaded.UpdatedAt.Equal(testTime.Add(time.Hour)) {
		t.Fatalf("version updated_at = %v", reloaded.UpdatedAt)
	}
	if reloaded.EnvironmentID != 1102 {
		t.Fatalf("rebound environment = %d, want 1102", reloaded.EnvironmentID)
	}
	if len(storedAccounts) != 1 || storedAccounts[0].AccountID != 1604 {
		t.Fatalf("accounts = %#v", storedAccounts)
	}
	// The rewrite replaced the previous rows instead of appending to them.
	var accountCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM version_account WHERE version_id = $1`, v.ID).Scan(&accountCount); err != nil {
		t.Fatalf("count accounts: %v", err)
	}
	if accountCount != 1 {
		t.Fatalf("version_account rows = %d, want the rewritten subset", accountCount)
	}

	missing := next
	missing.ID = 999999
	if err := repo.UpdateBinding(ctx, missing, nextAccounts, expectedUpdatedAt); !errors.Is(err, version.ErrNotFound) {
		t.Fatalf("missing UpdateBinding = %v", err)
	}

	// An optimistic write derived from the old snapshot loses deterministically
	// once another writer bumped version.updated_at.
	if err := repo.UpdateBinding(ctx, next, nextAccounts, expectedUpdatedAt); !errors.Is(err, version.ErrBindingConflict) {
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
	if err := repo.Create(ctx, v, sampleAccounts()); err != nil {
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
	v.EnvironmentID = 1102

	done := make(chan error, 1)
	go func() {
		done <- repo.UpdateBinding(ctx, v, nil, expectedUpdatedAt)
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
	stored, storedAccounts, err := repo.Get(ctx, v.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !stored.UpdatedAt.Equal(testTime.Add(time.Hour)) {
		t.Fatalf("version updated_at = %v", stored.UpdatedAt)
	}
	if stored.EnvironmentID != 1102 {
		t.Fatalf("environment after serialized write = %d", stored.EnvironmentID)
	}
	if len(storedAccounts) != 0 {
		t.Fatalf("accounts after serialized write = %#v", storedAccounts)
	}
}

// TestRepositoryDuplicateAccountRollsBack proves Create is one transaction: a
// uniqueness failure on the version_account natural key rolls the version row
// back with it, so no half-written aggregate survives.
func TestRepositoryDuplicateAccountRollsBack(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.New(pool, ids)
	ctx := context.Background()

	v := sampleVersion(ids, 1)
	duplicated := []version.AccountBinding{{AccountID: 1601}, {AccountID: 1601}}
	if err := repo.Create(ctx, v, duplicated); !errors.Is(err, version.ErrDuplicate) {
		t.Fatalf("duplicate account error = %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM version WHERE id = $1`, v.ID).Scan(&count); err != nil {
		t.Fatalf("count version: %v", err)
	}
	if count != 0 {
		t.Fatalf("version row survived the rolled-back transaction: %d", count)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM version_account WHERE version_id = $1`, v.ID).Scan(&count); err != nil {
		t.Fatalf("count version_account: %v", err)
	}
	if count != 0 {
		t.Fatalf("version_account rows survived: %d", count)
	}
}

// TestRepositoryUsageQueries pins the delete-guard semantics after the v0.2.4-B
// inversion: a SUT is referenced when a version binds its environment, and a
// page/API/database is "in use" while it is in range (approved page/API, active
// database) and owned by a SUT of a bound environment. Exclusions no longer
// exist, so a proposed, disabled or foreign-environment resource is the only way
// to be out of range.
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
	// fixture needs the owning environment/SUT and the surrounding resources.
	exec(`INSERT INTO environment (id, name, status)
		VALUES (1101, 'prod', 'active'), (1102, 'staging', 'active')`)
	exec(`INSERT INTO sut (id, environment_id, name, base_url, created_by)
		VALUES (1001, 1101, 'Orders', 'https://orders.example.test', 42),
		       (1003, 1102, 'Shipping', 'https://staging.example.test', 42)`)
	exec(`INSERT INTO sut_page (id, sut_id, name, url, route_template, status)
		VALUES (1302, 1001, 'in range', 'https://orders.example.test/in-range', '/in-range', 'approved'),
		       (1304, 1001, 'proposed', 'https://orders.example.test/proposed', '/proposed', 'proposed'),
		       (1305, 1003, 'staging', 'https://staging.example.test/page', '/staging-page', 'approved')`)
	exec(`INSERT INTO sut_api (id, sut_id, method, path_template, name, status)
		VALUES (1403, 1001, 'GET', '/api/in-range', 'in range', 'approved'),
		       (1404, 1001, 'GET', '/api/proposed', 'proposed', 'proposed')`)
	exec(`INSERT INTO target_database (id, sut_id, name, dsn, username, status)
		VALUES (1503, 1001, 'in-range', 'postgres://in-range', 'reader', 'active'),
		       (1504, 1001, 'disabled', 'postgres://disabled', 'reader', 'disabled')`)
	exec(`INSERT INTO sut_repository (id, sut_id, repo_id, role, enabled)
		VALUES (7001, 1001, 1201, 'backend', true)`)
	// H-B14 SUT gate: a REJECTED and a PROPOSED SUT of the bound environment are
	// out of effective scope WHOLE (resourceapp.SUTView.IsApproved), so neither
	// they nor their approved/active assets are "in use" -- the delete guard must
	// not over-block on resources the version can never execute against. Seeded
	// with the resource domain constants, not literals.
	exec(`INSERT INTO sut (id, environment_id, name, base_url, created_by, status)
		VALUES (1004, 1101, 'Rejected', 'https://rejected.example.test', 42, $1),
		       (1005, 1101, 'Proposed', 'https://proposed.example.test', 42, $2)`,
		resource.SUTStatusRejected, resource.SUTStatusProposed)
	exec(`INSERT INTO sut_page (id, sut_id, name, url, route_template, status)
		VALUES (1306, 1004, 'rejected page', 'https://rejected.example.test/p', '/p', $1),
		       (1307, 1005, 'proposed page', 'https://proposed.example.test/p', '/p', $1)`,
		resource.PageStatusApproved)
	exec(`INSERT INTO sut_api (id, sut_id, method, path_template, name, status)
		VALUES (1405, 1004, 'GET', '/api/rejected', 'rejected', $1),
		       (1406, 1005, 'GET', '/api/proposed', 'proposed', $1)`,
		resource.APIStatusApproved)
	exec(`INSERT INTO target_database (id, sut_id, name, dsn, username, status)
		VALUES (1505, 1004, 'rejected-db', 'postgres://rejected', 'reader', $1),
		       (1506, 1005, 'proposed-db', 'postgres://proposed', 'reader', $1)`,
		resource.StatusActive)
	exec(`INSERT INTO sut_repository (id, sut_id, repo_id, role, enabled)
		VALUES (7002, 1004, 1202, 'backend', true),
		       (7003, 1005, 1203, 'backend', true)`)

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
	if err := repo.Create(ctx, v, sampleAccounts()); err != nil {
		t.Fatalf("Create: %v", err)
	}

	cases := []struct {
		name  string
		check func(context.Context, int64) (bool, error)
		id    int64
		want  bool
	}{
		{"sut of the bound environment", repo.SUTInUse, 1001, true},
		{"sut of an unbound environment", repo.SUTInUse, 1003, false},
		{"environment bound", repo.EnvironmentInUse, 1101, true},
		{"environment unbound", repo.EnvironmentInUse, 1102, false},
		{"page in range", repo.PageInUse, 1302, true},
		{"page proposed", repo.PageInUse, 1304, false},
		{"page other environment", repo.PageInUse, 1305, false},
		{"api in range", repo.APIInUse, 1403, true},
		{"api proposed", repo.APIInUse, 1404, false},
		{"database in range", repo.DatabaseInUse, 1503, true},
		{"database disabled", repo.DatabaseInUse, 1504, false},
		{"account bound", repo.AccountInUse, 1601, true},
		{"account unbound", repo.AccountInUse, 1604, false},
		{"repository associated", repo.RepositoryInUse, 1201, true},
		{"repository unbound", repo.RepositoryInUse, 1204, false},
		// H-B14 SUT gate (IsApproved mirror): rejected/proposed SUTs of the bound
		// environment and their assets are out of effective scope, hence not in use.
		{"rejected sut of the bound environment", repo.SUTInUse, 1004, false},
		{"proposed sut of the bound environment", repo.SUTInUse, 1005, false},
		{"page of a rejected sut", repo.PageInUse, 1306, false},
		{"page of a proposed sut", repo.PageInUse, 1307, false},
		{"api of a rejected sut", repo.APIInUse, 1405, false},
		{"api of a proposed sut", repo.APIInUse, 1406, false},
		{"database of a rejected sut", repo.DatabaseInUse, 1505, false},
		{"database of a proposed sut", repo.DatabaseInUse, 1506, false},
		{"repository of a rejected sut only", repo.RepositoryInUse, 1202, false},
		{"repository of a proposed sut only", repo.RepositoryInUse, 1203, false},
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
// conflict details: every referencing version carries its title and the resource
// range matches the usage guards (status and environment ownership), with the SUT
// resolved through the environment its version binds.
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
	exec(`INSERT INTO environment (id, name, status)
		VALUES (1101, 'prod', 'active'), (1102, 'staging', 'active')`)
	exec(`INSERT INTO sut (id, environment_id, name, base_url, created_by)
		VALUES (1001, 1101, 'Orders', 'https://orders.example.test', 42),
		       (1002, 1101, 'Billing', 'https://billing.example.test', 42),
		       (1003, 1102, 'Shipping', 'https://staging.example.test', 42)`)
	exec(`INSERT INTO sut_page (id, sut_id, name, url, route_template, status)
		VALUES (1301, 1001, 'prod', 'https://orders.example.test/prod', '/prod', 'approved'),
		       (1305, 1003, 'staging', 'https://staging.example.test/page', '/staging-page', 'approved'),
		       (1306, 1001, 'proposed', 'https://orders.example.test/proposed', '/proposed', 'proposed')`)
	exec(`INSERT INTO sut_api (id, sut_id, method, path_template, name, status)
		VALUES (1402, 1002, 'GET', '/api/in-range', 'in range', 'approved')`)
	exec(`INSERT INTO target_database (id, sut_id, name, dsn, username, status)
		VALUES (1502, 1001, 'in-range', 'postgres://in-range', 'reader', 'active'),
		       (1504, 1001, 'disabled', 'postgres://disabled', 'reader', 'disabled')`)
	exec(`INSERT INTO sut_repository (id, sut_id, repo_id, role, enabled)
		VALUES (7001, 1001, 1201, 'backend', true),
		       (7002, 1003, 1205, 'shipping', true)`)
	// H-B14 SUT gate: a rejected SUT of the bound prod environment contributes
	// nothing to effective scope, so its assets reference no version. Repository
	// 1201 is ALSO associated with the rejected SUT: the approved association
	// (1001) must still reference the version (the gate must not over-exclude).
	exec(`INSERT INTO sut (id, environment_id, name, base_url, created_by, status)
		VALUES (1004, 1101, 'Rejected', 'https://rejected.example.test', 42, $1)`,
		resource.SUTStatusRejected)
	exec(`INSERT INTO sut_page (id, sut_id, name, url, route_template, status)
		VALUES (1307, 1004, 'rejected page', 'https://rejected.example.test/p', '/p', $1)`,
		resource.PageStatusApproved)
	exec(`INSERT INTO sut_api (id, sut_id, method, path_template, name, status)
		VALUES (1405, 1004, 'GET', '/api/rejected', 'rejected', $1)`,
		resource.APIStatusApproved)
	exec(`INSERT INTO target_database (id, sut_id, name, dsn, username, status)
		VALUES (1505, 1004, 'rejected-db', 'postgres://rejected', 'reader', $1)`,
		resource.StatusActive)
	exec(`INSERT INTO sut_repository (id, sut_id, repo_id, role, enabled)
		VALUES (7003, 1004, 1202, 'backend', true),
		       (7004, 1004, 1201, 'backend', true)`)

	q4 := sampleVersion(ids, 42)
	q4.Title = "Orders Q4"
	q5 := sampleVersion(ids, 42)
	q5.Title = "Orders Q5"
	q5.EnvironmentID = 1102
	if err := repo.Create(ctx, q4, sampleAccounts()); err != nil {
		t.Fatalf("Create Q4: %v", err)
	}
	if err := repo.Create(ctx, q5, nil); err != nil {
		t.Fatalf("Create Q5: %v", err)
	}

	cases := []struct {
		name string
		kind string
		id   int64
		want []version.ReferencingVersion
	}{
		// Both SUTs of the prod environment are referenced by the version that
		// binds it; the staging SUT is referenced by the other version only.
		{"sut prod orders", "sut", 1001, []version.ReferencingVersion{initialRef(q4.ID, "Orders Q4")}},
		{"sut prod billing", "sut", 1002, []version.ReferencingVersion{initialRef(q4.ID, "Orders Q4")}},
		{"sut staging", "sut", 1003, []version.ReferencingVersion{initialRef(q5.ID, "Orders Q5")}},
		{"sut unbound", "sut", 1009, nil},
		{"environment prod", "environment", 1101, []version.ReferencingVersion{initialRef(q4.ID, "Orders Q4")}},
		{"environment staging", "environment", 1102, []version.ReferencingVersion{initialRef(q5.ID, "Orders Q5")}},
		{"page prod", "page", 1301, []version.ReferencingVersion{initialRef(q4.ID, "Orders Q4")}},
		{"page staging", "page", 1305, []version.ReferencingVersion{initialRef(q5.ID, "Orders Q5")}},
		{"page proposed", "page", 1306, nil},
		{"api of the second sut", "api", 1402, []version.ReferencingVersion{initialRef(q4.ID, "Orders Q4")}},
		{"database in range", "database", 1502, []version.ReferencingVersion{initialRef(q4.ID, "Orders Q4")}},
		{"database disabled", "database", 1504, nil},
		{"account bound", "account", 1601, []version.ReferencingVersion{initialRef(q4.ID, "Orders Q4")}},
		{"account unbound", "account", 1604, nil},
		{"repository of the bound sut", "repository", 1201, []version.ReferencingVersion{initialRef(q4.ID, "Orders Q4")}},
		{"repository of the staging sut", "repository", 1205, []version.ReferencingVersion{initialRef(q5.ID, "Orders Q5")}},
		{"repository unbound", "repository", 1204, nil},
		// H-B14 SUT gate: the rejected SUT and its assets reference no version;
		// repository 1201 stays referenced through its approved association.
		{"rejected sut", "sut", 1004, nil},
		{"page of a rejected sut", "page", 1307, nil},
		{"api of a rejected sut", "api", 1405, nil},
		{"database of a rejected sut", "database", 1505, nil},
		{"repository of a rejected sut only", "repository", 1202, nil},
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
