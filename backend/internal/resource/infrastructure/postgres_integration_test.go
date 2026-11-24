//go:build integration

package resourcepg_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fixedClock struct{ at time.Time }

func (f fixedClock) Now() time.Time { return f.at }

type seqIDs struct{ next int64 }

func (s *seqIDs) Next() int64 { s.next++; return s.next }

type fakeUsage struct {
	inUse map[string]bool
}

func (f *fakeUsage) SUTInUse(context.Context, int64) (bool, error) { return f.inUse["sut"], nil }
func (f *fakeUsage) EnvironmentInUse(context.Context, int64) (bool, error) {
	return f.inUse["environment"], nil
}
func (f *fakeUsage) PageInUse(context.Context, int64) (bool, error) { return f.inUse["page"], nil }
func (f *fakeUsage) APIInUse(context.Context, int64) (bool, error)  { return f.inUse["api"], nil }
func (f *fakeUsage) DatabaseInUse(context.Context, int64) (bool, error) {
	return f.inUse["database"], nil
}
func (f *fakeUsage) AccountInUse(context.Context, int64) (bool, error) {
	return f.inUse["account"], nil
}
func (f *fakeUsage) RepositoryInUse(context.Context, int64) (bool, error) {
	return f.inUse["repository"], nil
}
func (f *fakeUsage) ReferencingVersions(context.Context, string, int64) ([]resourceapp.VersionRef, error) {
	return nil, nil
}

var testNow = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

type env struct {
	svc   *resourceapp.Service
	repo  *resourcepg.Repository
	pool  *pgxpool.Pool
	usage *fakeUsage
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testutil.NewDB(t)
	repo := resourcepg.New(pool)
	usage := &fakeUsage{inUse: map[string]bool{}}
	svc := resourceapp.New(repo, &seqIDs{next: 1000}, fixedClock{at: testNow}, usage, nil, nil)
	return &env{svc: svc, repo: repo, pool: pool, usage: usage}
}

func int64Of(t *testing.T, raw string) int64 {
	t.Helper()
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return value
}

func familyPtr(value string) *string { return &value }

func assertAPIErr(t *testing.T, err error, status int, code string) *platform.Error {
	t.Helper()
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want platform error", err)
	}
	if apiErr.Status != status || apiErr.Code != code {
		t.Fatalf("error = %d/%s, want %d/%s", apiErr.Status, apiErr.Code, status, code)
	}
	return apiErr
}

// testBaseURL derives a valid absolute base_url from a SUT name (base_url moved
// down from the environment to the SUT in v0.2.4-B).
func testBaseURL(name string) string {
	return "https://" + strings.ToLower(strings.Join(strings.Fields(name), "-")) + ".example.com"
}

// createEnvironment creates one top-level environment.
func createEnvironment(t *testing.T, e *env, name string) resourceapp.EnvironmentView {
	t.Helper()
	view, err := e.svc.CreateEnvironment(context.Background(), resourceapp.EnvironmentInput{
		Name: name, Description: name + " environment",
	}, 42)
	if err != nil {
		t.Fatalf("CreateEnvironment(%q): %v", name, err)
	}
	return view
}

// createSUT creates one SUT inside a dedicated top-level environment, so each
// SUT of a test is independently owned and the per-environment name uniqueness
// does not interfere.
func createSUT(t *testing.T, e *env, name string) resourceapp.SUTView {
	t.Helper()
	environment := createEnvironment(t, e, name+"-env")
	return createSUTIn(t, e, int64Of(t, environment.ID), name)
}

// createSUTIn creates one SUT inside an existing environment.
func createSUTIn(t *testing.T, e *env, environmentID int64, name string) resourceapp.SUTView {
	t.Helper()
	view, err := e.svc.CreateSUT(context.Background(), environmentID, resourceapp.SUTInput{
		Name: name, Intro: name, BaseURL: testBaseURL(name),
	}, 42)
	if err != nil {
		t.Fatalf("CreateSUT(%q): %v", name, err)
	}
	return view
}

func TestRepositoryUniquenessInTheInvertedHierarchy(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	environment := createEnvironment(t, e, "prod")
	environmentID := int64Of(t, environment.ID)
	sut := createSUTIn(t, e, environmentID, "Order Center")
	sutID := int64Of(t, sut.ID)
	if stored, err := e.repo.GetSUT(ctx, sutID); err != nil {
		t.Fatalf("GetSUT: %v", err)
	} else if stored.EnvironmentID != environmentID || stored.BaseURL != testBaseURL("Order Center") {
		t.Fatalf("stored sut = %#v", stored)
	}

	// The SUT name is unique per environment, case-insensitively.
	_, err := e.svc.CreateSUT(ctx, environmentID, resourceapp.SUTInput{
		Name: "order center", BaseURL: "https://dup.example.com",
	}, 42)
	assertAPIErr(t, err, 409, "conflict")
	_, err = e.svc.CreateSUT(ctx, environmentID, resourceapp.SUTInput{Name: "   ", BaseURL: "https://blank.example.com"}, 42)
	assertAPIErr(t, err, 400, "invalid_argument")
	// base_url is mandatory on the SUT now.
	_, err = e.svc.CreateSUT(ctx, environmentID, resourceapp.SUTInput{Name: "No URL"}, 42)
	apiErr := assertAPIErr(t, err, 400, "invalid_argument")
	if apiErr.Details["field"] != "base_url" {
		t.Fatalf("details = %#v", apiErr.Details)
	}
	// The same name in another environment is fine.
	other := createEnvironment(t, e, "staging")
	twin := createSUTIn(t, e, int64Of(t, other.ID), "Order Center")
	if twin.Name != "Order Center" {
		t.Fatalf("twin = %#v", twin)
	}

	// Environment names are globally unique and case-insensitive.
	_, err = e.svc.CreateEnvironment(ctx, resourceapp.EnvironmentInput{Name: "PROD"}, 42)
	assertAPIErr(t, err, 409, "conflict")

	// The old derived hierarchy is gone: no sut_environment table, no is_default.
	var legacyTable bool
	if err := e.pool.QueryRow(ctx, `SELECT to_regclass('sut_environment') IS NOT NULL`).Scan(&legacyTable); err != nil {
		t.Fatalf("check sut_environment: %v", err)
	}
	if legacyTable {
		t.Fatal("sut_environment still exists after the v0.2.4-B inversion")
	}

	page, err := e.svc.CreatePage(ctx, sutID, resourceapp.PageInput{
		Name: "Order list", URL: "https://app.example.com/orders?state=open",
	}, 42)
	if err != nil {
		t.Fatalf("CreatePage: %v", err)
	}
	if page.RouteTemplate != "/orders" || page.SUTID != sut.ID {
		t.Fatalf("page = %#v", page)
	}
	_, err = e.svc.CreatePage(ctx, sutID, resourceapp.PageInput{
		Name: "duplicate", URL: "https://app.example.com/orders?x=1",
	}, 42)
	apiErr = assertAPIErr(t, err, 409, "page_url_exists")
	if apiErr.Details["field"] != "url" {
		t.Fatalf("details = %#v", apiErr.Details)
	}
	// The same normalized URL under the twin SUT is a different page.
	if _, err := e.svc.CreatePage(ctx, int64Of(t, twin.ID), resourceapp.PageInput{
		Name: "Order list", URL: "https://app.example.com/orders",
	}, 42); err != nil {
		t.Fatalf("page uniqueness must be scoped to the SUT: %v", err)
	}

	api, err := e.svc.CreateAPI(ctx, sutID, resourceapp.APIInput{
		Method: "GET", PathTemplate: "/api/orders", Name: "List orders",
	}, 42)
	if err != nil {
		t.Fatalf("CreateAPI: %v", err)
	}
	if api.SUTID != sut.ID {
		t.Fatalf("api = %#v", api)
	}
	_, err = e.svc.CreateAPI(ctx, sutID, resourceapp.APIInput{
		Method: "GET", PathTemplate: "/api/orders", Name: "dup",
	}, 42)
	assertAPIErr(t, err, 409, "conflict")
	if _, err := e.svc.CreateAPI(ctx, int64Of(t, twin.ID), resourceapp.APIInput{
		Method: "GET", PathTemplate: "/api/orders", Name: "List orders",
	}, 42); err != nil {
		t.Fatalf("api uniqueness must be scoped to the SUT: %v", err)
	}

	database, err := e.svc.CreateDatabase(ctx, sutID, resourceapp.DatabaseInput{
		Name: "Orders DB",
		DSN:  "postgres://db.example.com:5432/orders", Username: "reader", Password: "pw",
	}, 42)
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if !database.PasswordSet || !database.ReadOnly || database.SUTID != sut.ID {
		t.Fatalf("database view = %#v", database)
	}
	_, err = e.svc.CreateDatabase(ctx, sutID, resourceapp.DatabaseInput{
		Name: "orders db",
		DSN:  "postgres://db.example.com:5432/other", Username: "reader",
	}, 42)
	assertAPIErr(t, err, 409, "conflict")
	if _, err := e.svc.CreateDatabase(ctx, int64Of(t, twin.ID), resourceapp.DatabaseInput{
		Name: "Orders DB", DSN: "postgres://db.example.com:5432/twin", Username: "reader",
	}, 42); err != nil {
		t.Fatalf("database name uniqueness must be scoped to the SUT: %v", err)
	}

	account, err := e.svc.CreateAccount(ctx, resourceapp.AccountInput{
		SUTID: &sutID, Username: "Order_Admin", Password: "secret",
		Roles: []string{"order_admin", "auditor"},
	}, 42)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if !account.PasswordSet || len(account.Roles) != 2 || account.SUTID != sut.ID {
		t.Fatalf("account = %#v", account)
	}
	// The pool stays global: the username is unique across SUTs.
	_, err = e.svc.CreateAccount(ctx, resourceapp.AccountInput{
		Username: "order_admin", Password: "x",
	}, 42)
	assertAPIErr(t, err, 409, "conflict")

	repo, err := e.svc.CreateRepository(ctx, resourceapp.RepositoryInput{
		Name: "Orders Service", Path: "https://git.example.com/orders.git",
		Include: []string{"src/**"}, Exclude: []string{"vendor/**"},
	}, 42)
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	if repo.CreatedBy != 42 || len(repo.Include) != 1 || len(repo.Exclude) != 1 {
		t.Fatalf("repository = %#v", repo)
	}
	_, err = e.svc.CreateRepository(ctx, resourceapp.RepositoryInput{
		Name: "orders service", Path: "https://git.example.com/other.git",
	}, 42)
	assertAPIErr(t, err, 409, "conflict")

	updatedName := "Orders Platform"
	updated, err := e.svc.UpdateSUT(ctx, sutID, resourceapp.SUTPatch{Name: &updatedName}, 42)
	if err != nil {
		t.Fatalf("UpdateSUT: %v", err)
	}
	if updated.Name != "Orders Platform" || updated.EnvironmentID != environment.ID {
		t.Fatalf("updated = %#v", updated)
	}

	var storedPassword string
	if err := e.pool.QueryRow(ctx, `SELECT password FROM test_account WHERE id = $1`, int64Of(t, account.ID)).Scan(&storedPassword); err != nil {
		t.Fatalf("read password: %v", err)
	}
	if storedPassword != "secret" {
		t.Fatalf("stored password = %q", storedPassword)
	}
	var storedAffinity int64
	if err := e.pool.QueryRow(ctx, `SELECT sut_id FROM test_account WHERE id = $1`, int64Of(t, account.ID)).Scan(&storedAffinity); err != nil {
		t.Fatalf("read account affinity: %v", err)
	}
	if storedAffinity != sutID {
		t.Fatalf("stored affinity = %d, want %d", storedAffinity, sutID)
	}
}

func TestSUTFamilyRoundTripAndClear(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	environment := createEnvironment(t, e, "prod")
	created, err := e.svc.CreateSUT(ctx, int64Of(t, environment.ID), resourceapp.SUTInput{
		Name: "Order Center", Intro: "orders", BaseURL: "https://order.example.com",
		Family: familyPtr("  payments  "),
	}, 42)
	if err != nil {
		t.Fatalf("CreateSUT: %v", err)
	}
	sutID := int64Of(t, created.ID)
	if created.Family == nil || *created.Family != "payments" {
		t.Fatalf("created family = %v", created.Family)
	}

	stored, err := e.repo.GetSUT(ctx, sutID)
	if err != nil {
		t.Fatalf("GetSUT: %v", err)
	}
	if stored.Family == nil || *stored.Family != "payments" {
		t.Fatalf("stored family = %v", stored.Family)
	}

	list, _, err := e.svc.ListSUTs(ctx, 10, nil)
	if err != nil {
		t.Fatalf("ListSUTs: %v", err)
	}
	if len(list) != 1 || list[0].Family == nil || *list[0].Family != "payments" {
		t.Fatalf("list = %#v", list)
	}
	detail, err := e.svc.GetSUT(ctx, sutID)
	if err != nil {
		t.Fatalf("GetSUT view: %v", err)
	}
	if detail.Family == nil || *detail.Family != "payments" {
		t.Fatalf("detail family = %v", detail.Family)
	}

	// Renaming keeps the family; a present family replaces it.
	renamed := "Orders Platform"
	updated, err := e.svc.UpdateSUT(ctx, sutID, resourceapp.SUTPatch{Name: &renamed}, 42)
	if err != nil {
		t.Fatalf("UpdateSUT name: %v", err)
	}
	if updated.Family == nil || *updated.Family != "payments" {
		t.Fatalf("kept family = %v", updated.Family)
	}
	updated, err = e.svc.UpdateSUT(ctx, sutID, resourceapp.SUTPatch{Family: familyPtr("order-center")}, 42)
	if err != nil {
		t.Fatalf("UpdateSUT family: %v", err)
	}
	if updated.Family == nil || *updated.Family != "order-center" {
		t.Fatalf("updated family = %v", updated.Family)
	}
	stored, err = e.repo.GetSUT(ctx, sutID)
	if err != nil {
		t.Fatalf("GetSUT: %v", err)
	}
	if stored.Family == nil || *stored.Family != "order-center" {
		t.Fatalf("stored family = %v", stored.Family)
	}

	// Uppercase families are rejected by the service and by the DB CHECK.
	_, err = e.svc.UpdateSUT(ctx, sutID, resourceapp.SUTPatch{Family: familyPtr("Orders")}, 42)
	assertAPIErr(t, err, 400, "invalid_argument")
	_, err = e.repo.CreateSUT(ctx, resource.SUT{
		ID: 700001, EnvironmentID: int64Of(t, environment.ID), Name: "Uppercase Family",
		BaseURL: "https://upper.example.com", Family: familyPtr("Orders"),
		Source: resource.SUTSourceAdmin, Status: resource.SUTStatusApproved,
		CreatedBy: 42, CreatedAt: testNow, UpdatedAt: testNow,
	})
	if err == nil {
		t.Fatal("repo accepted an uppercase family")
	}
	// The environment FK is enforced by the database, not only by the service.
	if _, err := e.repo.CreateSUT(ctx, resource.SUT{
		ID: 700002, EnvironmentID: 987654321, Name: "Ghost",
		BaseURL: "https://ghost.example.com",
		Source:  resource.SUTSourceAdmin, Status: resource.SUTStatusApproved,
		CreatedBy: 42, CreatedAt: testNow, UpdatedAt: testNow,
	}); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("an unknown environment_id must be rejected, got %v", err)
	}

	// The empty string clears the family.
	cleared, err := e.svc.UpdateSUT(ctx, sutID, resourceapp.SUTPatch{Family: familyPtr("")}, 42)
	if err != nil {
		t.Fatalf("UpdateSUT clear: %v", err)
	}
	if cleared.Family != nil {
		t.Fatalf("cleared family = %v", cleared.Family)
	}
	var family *string
	if err := e.pool.QueryRow(ctx, `SELECT family FROM sut WHERE id = $1`, sutID).Scan(&family); err != nil {
		t.Fatalf("read family: %v", err)
	}
	if family != nil {
		t.Fatalf("db family = %q, want NULL", *family)
	}
}

func TestRepositoryAggregationAndCatalog(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	prod := createEnvironment(t, e, "prod")
	prodID := int64Of(t, prod.ID)
	first := createSUTIn(t, e, prodID, "Order Center")
	firstID := int64Of(t, first.ID)

	page, err := e.svc.CreatePage(ctx, firstID, resourceapp.PageInput{Name: "orders", URL: "https://orders.example.com/orders"}, 42)
	if err != nil {
		t.Fatalf("CreatePage: %v", err)
	}
	api, err := e.svc.CreateAPI(ctx, firstID, resourceapp.APIInput{Method: "POST", PathTemplate: "/api/orders", Name: "Create order"}, 42)
	if err != nil {
		t.Fatalf("CreateAPI: %v", err)
	}
	database, err := e.svc.CreateDatabase(ctx, firstID, resourceapp.DatabaseInput{
		Name: "orders", DSN: "postgres://db/orders", Username: "reader",
	}, 42)
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	account, err := e.svc.CreateAccount(ctx, resourceapp.AccountInput{
		SUTID: &firstID, Username: "admin", Password: "pw", Roles: []string{"admin"},
	}, 42)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	repository, err := e.svc.CreateRepository(ctx, resourceapp.RepositoryInput{
		Name: "orders", Path: "https://git.example.com/orders.git",
	}, 42)
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	// A second SUT of the same environment plus a second environment with its own
	// SUT: the catalog is a forest of environments.
	sibling := createSUTIn(t, e, prodID, "Billing")
	if _, err := e.svc.CreatePage(ctx, int64Of(t, sibling.ID), resourceapp.PageInput{
		Name: "invoices", URL: "https://billing.example.com/invoices",
	}, 42); err != nil {
		t.Fatalf("CreatePage sibling: %v", err)
	}
	dev := createEnvironment(t, e, "dev")
	second := createSUTIn(t, e, int64Of(t, dev.ID), "Shipping")
	if _, err := e.svc.CreatePage(ctx, int64Of(t, second.ID), resourceapp.PageInput{
		Name: "shipments", URL: "https://shipping.example.com/shipments",
	}, 42); err != nil {
		t.Fatalf("CreatePage second: %v", err)
	}

	detail, err := e.svc.GetSUT(ctx, firstID)
	if err != nil {
		t.Fatalf("GetSUT: %v", err)
	}
	if detail.EnvironmentID != prod.ID || detail.BaseURL != testBaseURL("Order Center") {
		t.Fatalf("sut hierarchy = %#v", detail)
	}
	if len(detail.Pages) != 1 || len(detail.APIs) != 1 || len(detail.Databases) != 1 {
		t.Fatalf("nested = %#v", detail)
	}
	if detail.Pages[0].ID != page.ID || detail.APIs[0].ID != api.ID || detail.Databases[0].ID != database.ID {
		t.Fatalf("nested ids = %#v", detail)
	}

	environment, err := e.svc.GetEnvironment(ctx, prodID)
	if err != nil {
		t.Fatalf("GetEnvironment: %v", err)
	}
	if len(environment.SUTs) != 2 {
		t.Fatalf("environment suts = %#v", environment.SUTs)
	}
	if len(environment.SUTs[0].Pages) != 1 || len(environment.SUTs[1].Pages) != 1 {
		t.Fatalf("environment nested assets = %#v", environment.SUTs)
	}

	// The environment code pool is the union of its SUTs' repositories (GD-03).
	if _, err := e.svc.AddSUTRepository(ctx, firstID, resourceapp.SUTRepositoryInput{
		RepositoryID: int64Of(t, repository.ID), Role: "backend",
	}, 42); err != nil {
		t.Fatalf("AddSUTRepository first: %v", err)
	}
	secondRepo, err := e.svc.CreateRepository(ctx, resourceapp.RepositoryInput{
		Name: "billing", Path: "https://git.example.com/billing.git",
	}, 42)
	if err != nil {
		t.Fatalf("CreateRepository second: %v", err)
	}
	if _, err := e.svc.AddSUTRepository(ctx, int64Of(t, sibling.ID), resourceapp.SUTRepositoryInput{
		RepositoryID: int64Of(t, secondRepo.ID),
	}, 42); err != nil {
		t.Fatalf("AddSUTRepository sibling: %v", err)
	}
	pool_, err := e.svc.ListEnvironmentRepositories(ctx, prodID)
	if err != nil {
		t.Fatalf("ListEnvironmentRepositories: %v", err)
	}
	if len(pool_) != 2 || pool_[0].Repository.ID != repository.ID || pool_[1].Repository.ID != secondRepo.ID {
		t.Fatalf("environment code pool = %#v", pool_)
	}
	if pool_[0].SUTID != first.ID || pool_[1].SUTID != sibling.ID {
		t.Fatalf("pool ownership = %#v", pool_)
	}
	emptyPool, err := e.svc.ListEnvironmentRepositories(ctx, int64Of(t, dev.ID))
	if err != nil {
		t.Fatalf("ListEnvironmentRepositories dev: %v", err)
	}
	if len(emptyPool) != 0 {
		t.Fatalf("dev pool = %#v", emptyPool)
	}

	// The baseline write path round-trips through the same column codeintel and
	// the version binding resolution share.
	const commit = "0123456789abcdef0123456789abcdef01234567"
	if err := e.svc.SetSUTRepositoryBaselineCommit(ctx, firstID, int64Of(t, repository.ID), commit); err != nil {
		t.Fatalf("SetSUTRepositoryBaselineCommit: %v", err)
	}
	pool_, err = e.svc.ListEnvironmentRepositories(ctx, prodID)
	if err != nil {
		t.Fatalf("ListEnvironmentRepositories after baseline: %v", err)
	}
	if pool_[0].CodeBaselineCommit != commit || pool_[1].CodeBaselineCommit != "" {
		t.Fatalf("baselines = %#v", pool_)
	}
	var storedCommit *string
	if err := e.pool.QueryRow(ctx, `
		SELECT code_baseline_commit FROM sut_repository WHERE sut_id = $1 AND repo_id = $2
	`, firstID, int64Of(t, repository.ID)).Scan(&storedCommit); err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	if storedCommit == nil || *storedCommit != commit {
		t.Fatalf("stored baseline = %v", storedCommit)
	}

	repoView, err := e.svc.GetRepository(ctx, int64Of(t, repository.ID))
	if err != nil {
		t.Fatalf("GetRepository: %v", err)
	}
	if repoView.Name != "orders" || repoView.CreatedBy != 42 {
		t.Fatalf("repository = %#v", repoView)
	}

	catalog, err := e.svc.Catalog(ctx)
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(catalog) != 2 || catalog[0].ID != prod.ID || catalog[1].ID != dev.ID {
		t.Fatalf("catalog = %#v", catalog)
	}
	if len(catalog[0].SUTs) != 2 || len(catalog[1].SUTs) != 1 {
		t.Fatalf("catalog suts = %#v", catalog)
	}
	if catalog[1].SUTs[0].Pages[0].RouteTemplate != "/shipments" {
		t.Fatalf("catalog second = %#v", catalog[1])
	}

	accounts, err := e.svc.ListAllAccounts(ctx)
	if err != nil {
		t.Fatalf("ListAllAccounts: %v", err)
	}
	if len(accounts) != 1 || accounts[0].ID != account.ID {
		t.Fatalf("accounts = %#v", accounts)
	}

	assets, err := e.svc.ListAssetsBySUT(ctx, firstID)
	if err != nil {
		t.Fatalf("ListAssetsBySUT: %v", err)
	}
	if len(assets.Pages) != 1 || len(assets.APIs) != 1 || len(assets.Databases) != 1 {
		t.Fatalf("assets = %#v", assets)
	}
	suts, err := e.svc.ListSUTsByEnvironment(ctx, prodID)
	if err != nil {
		t.Fatalf("ListSUTsByEnvironment: %v", err)
	}
	if len(suts) != 2 || suts[0].ID != first.ID || suts[1].ID != sibling.ID {
		t.Fatalf("suts by environment = %#v", suts)
	}

	if _, err := e.repo.GetSUT(ctx, 123456789); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("missing SUT error = %v", err)
	}
	if _, err := e.repo.GetEnvironment(ctx, 123456789); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("missing environment error = %v", err)
	}
}

func TestRepositoryImportSkipsAndCascade(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	environment := createEnvironment(t, e, "prod")
	environmentID := int64Of(t, environment.ID)
	sut := createSUTIn(t, e, environmentID, "Order Center")
	sutID := int64Of(t, sut.ID)
	if _, err := e.svc.CreatePage(ctx, sutID, resourceapp.PageInput{Name: "orders", URL: "https://app.example.com/orders"}, 42); err != nil {
		t.Fatalf("CreatePage: %v", err)
	}

	pages, err := e.svc.ImportPages(ctx, sutID, []resourceapp.PageInput{
		{Name: "orders again", URL: "https://app.example.com/orders"},
		{Name: "users", URL: "https://app.example.com/users"},
		{Name: "users again", URL: "https://app.example.com/users?x=1"},
		{Name: "reports", URL: "https://app.example.com/reports"},
	}, 42)
	if err != nil {
		t.Fatalf("ImportPages: %v", err)
	}
	if pages.Created != 2 || pages.Skipped != 2 || len(pages.Items) != 2 {
		t.Fatalf("import pages = %#v", pages)
	}

	if _, err := e.svc.CreateAPI(ctx, sutID, resourceapp.APIInput{Method: "GET", PathTemplate: "/api/orders", Name: "orders"}, 42); err != nil {
		t.Fatalf("CreateAPI: %v", err)
	}
	apis, err := e.svc.ImportAPIs(ctx, sutID, []resourceapp.APIInput{
		{Method: "GET", PathTemplate: "/api/orders", Name: "orders again"},
		{Method: "POST", PathTemplate: "/api/orders", Name: "create"},
		{Method: "POST", PathTemplate: "/api/orders", Name: "create again"},
		{Method: "GET", PathTemplate: "/api/users", Name: "users"},
	}, 42)
	if err != nil {
		t.Fatalf("ImportAPIs: %v", err)
	}
	if apis.Created != 2 || apis.Skipped != 2 {
		t.Fatalf("import apis = %#v", apis)
	}

	_, err = e.svc.ImportPages(ctx, sutID, []resourceapp.PageInput{
		{Name: "valid", URL: "https://app.example.com/valid"},
		{Name: "invalid", URL: "not-a-url"},
	}, 42)
	apiErr := assertAPIErr(t, err, 400, "invalid_argument")
	if index, ok := apiErr.Details["index"].(int); !ok || index != 1 {
		t.Fatalf("details = %#v", apiErr.Details)
	}
	var pageCount int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM sut_page WHERE sut_id = $1`, sutID).Scan(&pageCount); err != nil {
		t.Fatalf("count pages: %v", err)
	}
	if pageCount != 3 {
		t.Fatalf("pages = %d after rejected batch, want 3", pageCount)
	}

	if _, err := e.svc.CreateDatabase(ctx, sutID, resourceapp.DatabaseInput{
		Name: "orders", DSN: "postgres://db/orders", Username: "reader",
	}, 42); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if _, err := e.svc.CreateAccount(ctx, resourceapp.AccountInput{
		SUTID: &sutID, Username: "admin", Password: "pw",
	}, 42); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	e.usage.inUse["sut"] = true
	if err := e.svc.DeleteSUT(ctx, sutID, 42); err == nil {
		t.Fatal("delete of in-use SUT succeeded")
	}
	e.usage.inUse["sut"] = false
	if err := e.svc.DeleteSUT(ctx, sutID, 42); err != nil {
		t.Fatalf("DeleteSUT: %v", err)
	}
	if _, err := e.repo.GetSUT(ctx, sutID); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("deleted SUT error = %v", err)
	}
	var assets int
	if err := e.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM sut_page) + (SELECT count(*) FROM sut_api)
		     + (SELECT count(*) FROM target_database)
	`).Scan(&assets); err != nil {
		t.Fatalf("count assets: %v", err)
	}
	if assets != 0 {
		t.Fatalf("deleting the SUT left %d asset rows", assets)
	}
	// The account pool is global: deleting a SUT clears the affinity instead of
	// deleting the credential.
	var affinity *int64
	if err := e.pool.QueryRow(ctx, `SELECT sut_id FROM test_account WHERE username = 'admin'`).Scan(&affinity); err != nil {
		t.Fatalf("read account affinity: %v", err)
	}
	if affinity != nil {
		t.Fatalf("affinity = %d, want NULL after the SUT was deleted", *affinity)
	}

	// Deleting the environment cascades its SUTs.
	sibling := createSUTIn(t, e, environmentID, "Billing")
	if err := e.svc.DeleteEnvironment(ctx, environmentID, 42); err != nil {
		t.Fatalf("DeleteEnvironment: %v", err)
	}
	if _, err := e.repo.GetSUT(ctx, int64Of(t, sibling.ID)); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("the environment cascade must remove its SUTs, got %v", err)
	}
	var suts int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM sut`).Scan(&suts); err != nil {
		t.Fatalf("count suts: %v", err)
	}
	if suts != 0 {
		t.Fatalf("suts = %d", suts)
	}
}

func TestRepositoryPagination(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	for _, name := range []string{"S1", "S2", "S3", "S4", "S5"} {
		createSUT(t, e, name)
	}
	first, hasMore, err := e.svc.ListSUTs(ctx, 2, nil)
	if err != nil {
		t.Fatalf("ListSUTs: %v", err)
	}
	if len(first) != 2 || !hasMore {
		t.Fatalf("first = %#v, hasMore = %v", first, hasMore)
	}
	if first[0].ID != "1014" || first[1].ID != "1011" {
		t.Fatalf("first page = %#v", first)
	}
	before := int64Of(t, first[1].ID)
	second, hasMore, err := e.svc.ListSUTs(ctx, 2, &before)
	if err != nil {
		t.Fatalf("ListSUTs page 2: %v", err)
	}
	if len(second) != 2 || !hasMore || second[0].ID != "1008" {
		t.Fatalf("second = %#v, hasMore = %v", second, hasMore)
	}

	for _, name := range []string{"repo-a", "repo-b", "repo-c"} {
		if _, err := e.svc.CreateRepository(ctx, resourceapp.RepositoryInput{
			Name: name, Path: "https://git.example.com/" + name + ".git",
		}, 42); err != nil {
			t.Fatalf("CreateRepository: %v", err)
		}
	}
	repos, hasMore, err := e.svc.ListRepositories(ctx, 2, nil)
	if err != nil {
		t.Fatalf("ListRepositories: %v", err)
	}
	if len(repos) != 2 || !hasMore || repos[0].ID != "1018" || repos[1].ID != "1017" {
		t.Fatalf("repos = %#v, hasMore = %v", repos, hasMore)
	}
	before = int64Of(t, repos[1].ID)
	last, hasMore, err := e.svc.ListRepositories(ctx, 2, &before)
	if err != nil {
		t.Fatalf("ListRepositories page 2: %v", err)
	}
	if len(last) != 1 || hasMore || last[0].ID != "1016" {
		t.Fatalf("last = %#v, hasMore = %v", last, hasMore)
	}
}

func TestRepositoryPasswordNeverLeaksInViews(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sut := createSUT(t, e, "Order Center")
	sutID := int64Of(t, sut.ID)

	database, err := e.svc.CreateDatabase(ctx, sutID, resourceapp.DatabaseInput{
		Name: "orders", DSN: "postgres://db/orders",
		Username: "reader", Password: "db-secret",
	}, 42)
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if !database.PasswordSet {
		t.Fatal("password_set = false")
	}
	account, err := e.svc.CreateAccount(ctx, resourceapp.AccountInput{
		SUTID: &sutID, Username: "admin", Password: "acct-secret",
	}, 42)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if !account.PasswordSet {
		t.Fatal("password_set = false")
	}
	if strings.Contains(account.Username, "acct-secret") || strings.Contains(database.DSN, "db-secret") {
		t.Fatal("view contains secret material")
	}
}

// TestRepositorySUTRepositories pins the v0.22 DB-12/API-17 adapter: SUT-level
// multi-repository rows round-trip through the PG repository in deterministic
// repository-ID order, trim the role label and report the natural-key conflict.
func TestRepositorySUTRepositories(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sut := createSUT(t, e, "Order Center")
	sutID := int64Of(t, sut.ID)

	backend, err := e.svc.CreateRepository(ctx, resourceapp.RepositoryInput{
		Name: "orders-backend", Path: "https://git.example.com/orders-backend.git",
	}, 42)
	if err != nil {
		t.Fatalf("CreateRepository backend: %v", err)
	}
	frontend, err := e.svc.CreateRepository(ctx, resourceapp.RepositoryInput{
		Name: "orders-frontend", Path: "https://git.example.com/orders-frontend.git",
	}, 42)
	if err != nil {
		t.Fatalf("CreateRepository frontend: %v", err)
	}
	// Add the higher ID first so the read order proves the repo-ID sort, not
	// insertion order.
	if _, err := e.svc.AddSUTRepository(ctx, sutID, resourceapp.SUTRepositoryInput{
		RepositoryID: int64Of(t, frontend.ID), Role: "  frontend  ",
	}, 42); err != nil {
		t.Fatalf("AddSUTRepository frontend: %v", err)
	}
	if _, err := e.svc.AddSUTRepository(ctx, sutID, resourceapp.SUTRepositoryInput{
		RepositoryID: int64Of(t, backend.ID), Role: "backend",
	}, 42); err != nil {
		t.Fatalf("AddSUTRepository backend: %v", err)
	}
	if _, err := e.svc.AddSUTRepository(ctx, sutID, resourceapp.SUTRepositoryInput{
		RepositoryID: int64Of(t, backend.ID),
	}, 42); err == nil {
		t.Fatal("duplicate SUT repository association accepted")
	} else {
		assertAPIErr(t, err, 409, "conflict")
	}

	list, err := e.svc.ListSUTRepositories(ctx, sutID)
	if err != nil {
		t.Fatalf("ListSUTRepositories: %v", err)
	}
	if len(list) != 2 || list[0].Repository.ID != backend.ID || list[1].Repository.ID != frontend.ID {
		t.Fatalf("list = %#v", list)
	}
	if list[0].Role != "backend" || list[1].Role != "frontend" {
		t.Fatalf("roles = %q/%q, want trimmed labels", list[0].Role, list[1].Role)
	}
	if !list[0].Enabled || list[0].SUTID != sut.ID {
		t.Fatalf("association = %#v", list[0])
	}
	if list[0].Repository.Name != "orders-backend" {
		t.Fatalf("repository projection = %#v", list[0].Repository)
	}

	var storedRole string
	if err := e.pool.QueryRow(ctx, `SELECT role FROM sut_repository WHERE sut_id = $1 AND repo_id = $2`,
		sutID, int64Of(t, frontend.ID)).Scan(&storedRole); err != nil {
		t.Fatalf("read role: %v", err)
	}
	if storedRole != "frontend" {
		t.Fatalf("stored role = %q, want trimmed", storedRole)
	}

	if _, err := e.svc.ListSUTRepositories(ctx, 999999); err == nil {
		t.Fatal("missing SUT list accepted")
	} else {
		assertAPIErr(t, err, 404, "not_found")
	}

	// Direct adapter delete round-trip (the service exposes add/list only).
	associations, err := e.repo.ListSUTRepositories(ctx, sutID)
	if err != nil || len(associations) != 2 {
		t.Fatalf("adapter list = %#v/%v", associations, err)
	}
	if err := e.repo.DeleteSUTRepository(ctx, associations[0].ID); err != nil {
		t.Fatalf("DeleteSUTRepository: %v", err)
	}
	if err := e.repo.DeleteSUTRepository(ctx, associations[0].ID); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("missing DeleteSUTRepository = %v", err)
	}
	remaining, err := e.repo.ListSUTRepositories(ctx, sutID)
	if err != nil || len(remaining) != 1 {
		t.Fatalf("remaining associations = %#v/%v", remaining, err)
	}
}

// TestDeleteSUTCleansSUTRepositories pins the cascade hygiene of API-17 rows:
// sut_repository has no cross-table FK in v0.22, so deleting a SUT must remove
// its associations explicitly instead of leaving dangling rows.
func TestDeleteSUTCleansSUTRepositories(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sut := createSUT(t, e, "Order Center")
	sutID := int64Of(t, sut.ID)
	repository, err := e.svc.CreateRepository(ctx, resourceapp.RepositoryInput{
		Name: "orders", Path: "https://git.example.com/orders.git",
	}, 42)
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	if _, err := e.svc.AddSUTRepository(ctx, sutID, resourceapp.SUTRepositoryInput{
		RepositoryID: int64Of(t, repository.ID),
	}, 42); err != nil {
		t.Fatalf("AddSUTRepository: %v", err)
	}

	if err := e.svc.DeleteSUT(ctx, sutID, 42); err != nil {
		t.Fatalf("DeleteSUT: %v", err)
	}
	var associations int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM sut_repository WHERE sut_id = $1`, sutID).
		Scan(&associations); err != nil {
		t.Fatalf("count sut_repository: %v", err)
	}
	if associations != 0 {
		t.Fatalf("sut_repository rows survived the SUT delete: %d", associations)
	}
}
