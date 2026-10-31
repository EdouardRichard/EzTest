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

func createSUT(t *testing.T, e *env, name string) resourceapp.SUTView {
	t.Helper()
	view, err := e.svc.CreateSUT(context.Background(), resourceapp.SUTInput{Name: name, Intro: name}, 42)
	if err != nil {
		t.Fatalf("CreateSUT(%q): %v", name, err)
	}
	return view
}

func createEnvironment(t *testing.T, e *env, sutID int64, name string, isDefault bool) resourceapp.EnvironmentView {
	t.Helper()
	view, err := e.svc.CreateEnvironment(context.Background(), sutID, resourceapp.EnvironmentInput{
		Name: name, BaseURL: "https://" + name + ".example.com", IsDefault: isDefault,
	}, 42)
	if err != nil {
		t.Fatalf("CreateEnvironment(%q): %v", name, err)
	}
	return view
}

func TestRepositoryUniquenessAndDefaultUnset(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	sut := createSUT(t, e, "Order Center")
	sutID := int64Of(t, sut.ID)
	if _, err := e.repo.GetSUT(ctx, sutID); err != nil {
		t.Fatalf("GetSUT: %v", err)
	}
	_, err := e.svc.CreateSUT(ctx, resourceapp.SUTInput{Name: "order center"}, 42)
	assertAPIErr(t, err, 409, "conflict")
	_, err = e.svc.CreateSUT(ctx, resourceapp.SUTInput{Name: " "}, 42)
	assertAPIErr(t, err, 400, "invalid_argument")

	prod := createEnvironment(t, e, sutID, "prod", true)
	if !prod.IsDefault {
		t.Fatal("prod is not default")
	}
	staging := createEnvironment(t, e, sutID, "staging", true)
	if !staging.IsDefault {
		t.Fatal("staging is not default")
	}
	var defaults int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM sut_environment WHERE sut_id = $1 AND is_default`, sutID).Scan(&defaults); err != nil {
		t.Fatalf("count defaults: %v", err)
	}
	if defaults != 1 {
		t.Fatalf("defaults = %d, want 1", defaults)
	}
	storedProd, err := e.repo.GetEnvironment(ctx, int64Of(t, prod.ID))
	if err != nil {
		t.Fatalf("GetEnvironment: %v", err)
	}
	if storedProd.IsDefault {
		t.Fatal("previous default still set in the database")
	}
	_, err = e.svc.CreateEnvironment(ctx, sutID, resourceapp.EnvironmentInput{
		Name: "PROD", BaseURL: "https://prod2.example.com",
	}, 42)
	assertAPIErr(t, err, 409, "conflict")

	environmentID := int64Of(t, staging.ID)
	page, err := e.svc.CreatePage(ctx, environmentID, resourceapp.PageInput{
		Name: "Order list", URL: "https://app.example.com/orders?state=open",
	}, 42)
	if err != nil {
		t.Fatalf("CreatePage: %v", err)
	}
	if page.RouteTemplate != "/orders" {
		t.Fatalf("route = %q", page.RouteTemplate)
	}
	_, err = e.svc.CreatePage(ctx, environmentID, resourceapp.PageInput{
		Name: "duplicate", URL: "https://app.example.com/orders?x=1",
	}, 42)
	assertAPIErr(t, err, 409, "conflict")

	api, err := e.svc.CreateAPI(ctx, environmentID, resourceapp.APIInput{
		Method: "GET", PathTemplate: "/api/orders", Name: "List orders",
	}, 42)
	if err != nil {
		t.Fatalf("CreateAPI: %v", err)
	}
	_, err = e.svc.CreateAPI(ctx, environmentID, resourceapp.APIInput{
		Method: "GET", PathTemplate: "/api/orders", Name: "dup",
	}, 42)
	assertAPIErr(t, err, 409, "conflict")

	database, err := e.svc.CreateDatabase(ctx, sutID, resourceapp.DatabaseInput{
		EnvironmentID: environmentID, Name: "Orders DB",
		DSN: "postgres://db.example.com:5432/orders", Username: "reader", Password: "pw",
	}, 42)
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if !database.PasswordSet || !database.ReadOnly {
		t.Fatalf("database view = %#v", database)
	}
	_, err = e.svc.CreateDatabase(ctx, sutID, resourceapp.DatabaseInput{
		EnvironmentID: environmentID, Name: "orders db",
		DSN: "postgres://db.example.com:5432/other", Username: "reader",
	}, 42)
	assertAPIErr(t, err, 409, "conflict")

	account, err := e.svc.CreateAccount(ctx, sutID, resourceapp.AccountInput{
		EnvironmentID: environmentID, Username: "Order_Admin", Password: "secret",
		Roles: []string{"order_admin", "auditor"},
	}, 42)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if !account.PasswordSet || len(account.Roles) != 2 {
		t.Fatalf("account = %#v", account)
	}
	_, err = e.svc.CreateAccount(ctx, sutID, resourceapp.AccountInput{
		EnvironmentID: environmentID, Username: "order_admin", Password: "x",
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
	if updated.Name != "Orders Platform" {
		t.Fatalf("updated = %#v", updated)
	}
	_ = page
	_ = api

	var storedPassword string
	if err := e.pool.QueryRow(ctx, `SELECT password FROM test_account WHERE id = $1`, int64Of(t, account.ID)).Scan(&storedPassword); err != nil {
		t.Fatalf("read password: %v", err)
	}
	if storedPassword != "secret" {
		t.Fatalf("stored password = %q", storedPassword)
	}
}

func TestRepositoryAggregationAndCatalog(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	first := createSUT(t, e, "Order Center")
	firstID := int64Of(t, first.ID)
	prod := createEnvironment(t, e, firstID, "prod", true)
	prodID := int64Of(t, prod.ID)

	page, err := e.svc.CreatePage(ctx, prodID, resourceapp.PageInput{Name: "orders", URL: "https://orders.example.com/orders"}, 42)
	if err != nil {
		t.Fatalf("CreatePage: %v", err)
	}
	api, err := e.svc.CreateAPI(ctx, prodID, resourceapp.APIInput{Method: "POST", PathTemplate: "/api/orders", Name: "Create order"}, 42)
	if err != nil {
		t.Fatalf("CreateAPI: %v", err)
	}
	database, err := e.svc.CreateDatabase(ctx, firstID, resourceapp.DatabaseInput{
		EnvironmentID: prodID, Name: "orders", DSN: "postgres://db/orders", Username: "reader",
	}, 42)
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	account, err := e.svc.CreateAccount(ctx, firstID, resourceapp.AccountInput{
		EnvironmentID: prodID, Username: "admin", Password: "pw", Roles: []string{"admin"},
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

	second := createSUT(t, e, "Billing")
	secondID := int64Of(t, second.ID)
	dev := createEnvironment(t, e, secondID, "dev", false)
	devID := int64Of(t, dev.ID)
	if _, err := e.svc.CreatePage(ctx, devID, resourceapp.PageInput{Name: "invoices", URL: "https://billing.example.com/invoices"}, 42); err != nil {
		t.Fatalf("CreatePage second: %v", err)
	}

	detail, err := e.svc.GetSUT(ctx, firstID)
	if err != nil {
		t.Fatalf("GetSUT: %v", err)
	}
	if len(detail.Environments) != 1 {
		t.Fatalf("environments = %#v", detail.Environments)
	}
	nested := detail.Environments[0]
	if len(nested.Pages) != 1 || len(nested.APIs) != 1 || len(nested.Databases) != 1 || len(nested.Accounts) != 1 {
		t.Fatalf("nested = %#v", nested)
	}
	if nested.Pages[0].ID != page.ID || nested.APIs[0].ID != api.ID ||
		nested.Databases[0].ID != database.ID || nested.Accounts[0].ID != account.ID {
		t.Fatalf("nested ids = %#v", nested)
	}

	environment, err := e.svc.GetEnvironment(ctx, prodID)
	if err != nil {
		t.Fatalf("GetEnvironment: %v", err)
	}
	if len(environment.Pages) != 1 || len(environment.Accounts) != 1 {
		t.Fatalf("environment = %#v", environment)
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
	if len(catalog) != 2 || catalog[0].ID != first.ID || catalog[1].ID != second.ID {
		t.Fatalf("catalog = %#v", catalog)
	}
	if len(catalog[1].Environments) != 1 || catalog[1].Environments[0].Pages[0].RouteTemplate != "/invoices" {
		t.Fatalf("catalog second = %#v", catalog[1])
	}

	if _, err := e.repo.GetSUT(ctx, 123456789); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("missing SUT error = %v", err)
	}
}

func TestRepositoryImportSkipsAndCascade(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	sut := createSUT(t, e, "Order Center")
	sutID := int64Of(t, sut.ID)
	environment := createEnvironment(t, e, sutID, "prod", true)
	environmentID := int64Of(t, environment.ID)
	if _, err := e.svc.CreatePage(ctx, environmentID, resourceapp.PageInput{Name: "orders", URL: "https://app.example.com/orders"}, 42); err != nil {
		t.Fatalf("CreatePage: %v", err)
	}

	pages, err := e.svc.ImportPages(ctx, environmentID, []resourceapp.PageInput{
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

	if _, err := e.svc.CreateAPI(ctx, environmentID, resourceapp.APIInput{Method: "GET", PathTemplate: "/api/orders", Name: "orders"}, 42); err != nil {
		t.Fatalf("CreateAPI: %v", err)
	}
	apis, err := e.svc.ImportAPIs(ctx, environmentID, []resourceapp.APIInput{
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

	_, err = e.svc.ImportPages(ctx, environmentID, []resourceapp.PageInput{
		{Name: "valid", URL: "https://app.example.com/valid"},
		{Name: "invalid", URL: "not-a-url"},
	}, 42)
	apiErr := assertAPIErr(t, err, 400, "invalid_argument")
	if index, ok := apiErr.Details["index"].(int); !ok || index != 1 {
		t.Fatalf("details = %#v", apiErr.Details)
	}
	var pageCount int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM sut_page WHERE environment_id = $1`, environmentID).Scan(&pageCount); err != nil {
		t.Fatalf("count pages: %v", err)
	}
	if pageCount != 3 {
		t.Fatalf("pages = %d after rejected batch, want 3", pageCount)
	}

	if _, err := e.svc.CreateDatabase(ctx, sutID, resourceapp.DatabaseInput{
		EnvironmentID: environmentID, Name: "orders", DSN: "postgres://db/orders", Username: "reader",
	}, 42); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if _, err := e.svc.CreateAccount(ctx, sutID, resourceapp.AccountInput{
		EnvironmentID: environmentID, Username: "admin", Password: "pw",
	}, 42); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	e.usage.inUse["sut"] = true
	if err := e.svc.DeleteSUT(ctx, sutID, 42); err == nil {
		t.Fatal("delete of in-use SUT succeeded")
	}
	e.usage.inUse["sut"] = false
	if err := e.svc.DeleteEnvironment(ctx, environmentID, 42); err != nil {
		t.Fatalf("DeleteEnvironment: %v", err)
	}
	var environments int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM sut_environment WHERE sut_id = $1`, sutID).Scan(&environments); err != nil {
		t.Fatalf("count environments: %v", err)
	}
	if environments != 0 {
		t.Fatalf("environments = %d", environments)
	}

	if err := e.svc.DeleteSUT(ctx, sutID, 42); err != nil {
		t.Fatalf("DeleteSUT: %v", err)
	}
	if _, err := e.repo.GetSUT(ctx, sutID); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("deleted SUT error = %v", err)
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
	if first[0].ID != "1005" || first[1].ID != "1004" {
		t.Fatalf("first page = %#v", first)
	}
	before := int64Of(t, first[1].ID)
	second, hasMore, err := e.svc.ListSUTs(ctx, 2, &before)
	if err != nil {
		t.Fatalf("ListSUTs page 2: %v", err)
	}
	if len(second) != 2 || !hasMore || second[0].ID != "1003" {
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
	if len(repos) != 2 || !hasMore || repos[0].ID != "1008" || repos[1].ID != "1007" {
		t.Fatalf("repos = %#v, hasMore = %v", repos, hasMore)
	}
	before = int64Of(t, repos[1].ID)
	last, hasMore, err := e.svc.ListRepositories(ctx, 2, &before)
	if err != nil {
		t.Fatalf("ListRepositories page 2: %v", err)
	}
	if len(last) != 1 || hasMore || last[0].ID != "1006" {
		t.Fatalf("last = %#v, hasMore = %v", last, hasMore)
	}
}

func TestRepositoryPasswordNeverLeaksInViews(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sut := createSUT(t, e, "Order Center")
	sutID := int64Of(t, sut.ID)
	environment := createEnvironment(t, e, sutID, "prod", true)
	environmentID := int64Of(t, environment.ID)

	database, err := e.svc.CreateDatabase(ctx, sutID, resourceapp.DatabaseInput{
		EnvironmentID: environmentID, Name: "orders", DSN: "postgres://db/orders",
		Username: "reader", Password: "db-secret",
	}, 42)
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if !database.PasswordSet {
		t.Fatal("password_set = false")
	}
	account, err := e.svc.CreateAccount(ctx, sutID, resourceapp.AccountInput{
		EnvironmentID: environmentID, Username: "admin", Password: "acct-secret",
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
