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

func newIDs(t *testing.T) *id.Snowflake {
	t.Helper()
	generator, err := id.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	return generator
}

func ptr[T any](value T) *T { return &value }

func sampleVersion(ids *id.Snowflake, actorID int64) version.Version {
	return version.Version{
		ID:        ids.Next(),
		Title:     "Orders Q4",
		Status:    version.StatusInitial,
		CreatedBy: actorID,
		CreatedAt: testTime,
		UpdatedAt: testTime,
	}
}

func sampleBindings(ids *id.Snowflake, versionID int64) []version.Binding {
	return []version.Binding{
		{
			ID:               ids.Next(),
			VersionID:        versionID,
			SUTID:            1001,
			EnvironmentID:    1101,
			CodeRepositoryID: ptr(int64(1201)),
			Scope:            version.Scope{Pages: []int64{1301, 1302}, APIs: []int64{1401}},
			DatabaseIDs:      []int64{1501},
			Accounts:         []version.AccountBinding{{AccountID: 1601, Role: "order_admin"}},
		},
		{
			ID:            ids.Next(),
			VersionID:     versionID,
			SUTID:         1002,
			EnvironmentID: 1102,
			Scope:         version.Scope{Pages: []int64{1303}, APIs: []int64{}},
			DatabaseIDs:   []int64{},
			Accounts:      []version.AccountBinding{{AccountID: 1602}},
		},
	}
}

func TestRepositoryCreateGetListUpdate(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.New(pool, ids)
	ctx := context.Background()

	v := sampleVersion(ids, 42)
	v.AutoDesignNewRequirements = true
	bindings := sampleBindings(ids, v.ID)
	if err := repo.Create(ctx, v, bindings); err != nil {
		t.Fatalf("Create: %v", err)
	}

	stored, storedBindings, err := repo.Get(ctx, v.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Title != v.Title || stored.Status != version.StatusInitial || !stored.AutoDesignNewRequirements {
		t.Fatalf("stored version = %#v", stored)
	}
	if stored.CreatedBy != 42 || !stored.CreatedAt.Equal(testTime) || !stored.UpdatedAt.Equal(testTime) {
		t.Fatalf("stored audit columns = %#v", stored)
	}
	if len(storedBindings) != 2 {
		t.Fatalf("bindings = %#v", storedBindings)
	}

	first := findBinding(t, storedBindings, 1001)
	if first.CodeRepositoryID == nil || *first.CodeRepositoryID != 1201 {
		t.Fatalf("repository = %v", first.CodeRepositoryID)
	}
	if got := first.Scope.Pages; len(got) != 2 || got[0] != 1301 || got[1] != 1302 {
		t.Fatalf("scope pages = %#v", got)
	}
	if got := first.Scope.APIs; len(got) != 1 || got[0] != 1401 {
		t.Fatalf("scope apis = %#v", got)
	}
	if got := first.DatabaseIDs; len(got) != 1 || got[0] != 1501 {
		t.Fatalf("database ids = %#v", got)
	}
	if len(first.Accounts) != 1 || first.Accounts[0].AccountID != 1601 || first.Accounts[0].Role != "order_admin" {
		t.Fatalf("accounts = %#v", first.Accounts)
	}

	second := findBinding(t, storedBindings, 1002)
	if second.CodeRepositoryID != nil {
		t.Fatalf("repository = %v, want nil", second.CodeRepositoryID)
	}
	if second.Scope.Pages == nil || len(second.Scope.Pages) != 1 || second.Scope.Pages[0] != 1303 {
		t.Fatalf("pages = %#v", second.Scope.Pages)
	}
	if second.Scope.APIs == nil || len(second.Scope.APIs) != 0 {
		t.Fatalf("apis = %#v", second.Scope.APIs)
	}
	if second.DatabaseIDs == nil || len(second.DatabaseIDs) != 0 {
		t.Fatalf("database ids = %#v", second.DatabaseIDs)
	}

	// Raw rows: scope JSONB shape, resource rows and role.
	var scopeRaw []byte
	if err := pool.QueryRow(ctx, `SELECT scope FROM version_sut WHERE id = $1`, first.ID).Scan(&scopeRaw); err != nil {
		t.Fatalf("query scope: %v", err)
	}
	var scope map[string]any
	if err := json.Unmarshal(scopeRaw, &scope); err != nil {
		t.Fatalf("decode scope: %v", err)
	}
	pages, _ := scope["pages"].([]any)
	if len(pages) != 2 || pages[0].(float64) != 1301 || pages[1].(float64) != 1302 {
		t.Fatalf("scope JSON = %s", scopeRaw)
	}

	rows, err := pool.Query(ctx, `
		SELECT kind, resource_id, role, selected FROM version_sut_resource
		WHERE version_sut_id = $1 ORDER BY id`, first.ID)
	if err != nil {
		t.Fatalf("query resources: %v", err)
	}
	defer rows.Close()
	type resourceRow struct {
		kind, role string
		id         int64
		selected   bool
	}
	resourceRows := []resourceRow{}
	for rows.Next() {
		var row resourceRow
		if err := rows.Scan(&row.kind, &row.id, &row.role, &row.selected); err != nil {
			t.Fatalf("scan resource: %v", err)
		}
		resourceRows = append(resourceRows, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(resourceRows) != 2 {
		t.Fatalf("resource rows = %#v", resourceRows)
	}
	if resourceRows[0].kind != "database" || resourceRows[0].id != 1501 || resourceRows[0].selected != true {
		t.Fatalf("database row = %#v", resourceRows[0])
	}
	if resourceRows[1].kind != "account" || resourceRows[1].id != 1601 || resourceRows[1].role != "order_admin" {
		t.Fatalf("account row = %#v", resourceRows[1])
	}

	// A second version lands in the list with its own SUT count.
	secondVersion := sampleVersion(ids, 7)
	secondVersion.Title = "Billing Q4"
	secondBindings := sampleBindings(ids, secondVersion.ID)[:1]
	if err := repo.Create(ctx, secondVersion, secondBindings); err != nil {
		t.Fatalf("Create second: %v", err)
	}

	page, err := repo.List(ctx, 1, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page) != 1 || page[0].ID != secondVersion.ID {
		t.Fatalf("list = %#v", page)
	}
	if page[0].SUTCount != 1 || page[0].Title != "Billing Q4" || page[0].Status != version.StatusInitial {
		t.Fatalf("summary = %#v", page[0])
	}

	older, err := repo.List(ctx, 10, &secondVersion.ID)
	if err != nil {
		t.Fatalf("List older: %v", err)
	}
	if len(older) != 1 || older[0].ID != v.ID || older[0].SUTCount != 2 {
		t.Fatalf("older = %#v", older)
	}

	// Update only touches title/auto flag/updated_at.
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
	if !reloaded.CreatedAt.Equal(testTime) || !reloaded.UpdatedAt.Equal(testTime.Add(time.Hour)) {
		t.Fatalf("timestamps = %v/%v", reloaded.CreatedAt, reloaded.UpdatedAt)
	}
	if reloaded.Status != version.StatusInitial || reloaded.CreatedBy != 42 {
		t.Fatalf("update changed immutable fields: %#v", reloaded)
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

func findBinding(t *testing.T, bindings []version.Binding, sutID int64) version.Binding {
	t.Helper()
	for _, binding := range bindings {
		if binding.SUTID == sutID {
			return binding
		}
	}
	t.Fatalf("binding for SUT %d not stored", sutID)
	return version.Binding{}
}

func TestRepositoryDuplicateBindingRollsBack(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.New(pool, ids)
	ctx := context.Background()

	v := sampleVersion(ids, 1)
	bindings := sampleBindings(ids, v.ID)
	duplicated := []version.Binding{
		bindings[0],
		bindings[1],
		{
			ID:            ids.Next(),
			VersionID:     v.ID,
			SUTID:         bindings[0].SUTID,
			EnvironmentID: bindings[0].EnvironmentID,
			Scope:         version.Scope{Pages: []int64{}, APIs: []int64{}},
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
}

func TestRepositoryUsageQueries(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.New(pool, ids)
	ctx := context.Background()

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
	bindings := sampleBindings(ids, v.ID)
	if err := repo.Create(ctx, v, bindings); err != nil {
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
		{"environment bound", repo.EnvironmentInUse, 1102, true},
		{"environment unbound", repo.EnvironmentInUse, 1103, false},
		{"page in scope", repo.PageInUse, 1301, true},
		{"second page in scope", repo.PageInUse, 1302, true},
		{"page unbound", repo.PageInUse, 1304, false},
		{"api in scope", repo.APIInUse, 1401, true},
		{"api unbound", repo.APIInUse, 1402, false},
		{"database bound", repo.DatabaseInUse, 1501, true},
		{"database unbound", repo.DatabaseInUse, 1503, false},
		{"account bound", repo.AccountInUse, 1602, true},
		{"account unbound", repo.AccountInUse, 1604, false},
		{"repository bound", repo.RepositoryInUse, 1201, true},
		{"repository unbound", repo.RepositoryInUse, 1203, false},
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
