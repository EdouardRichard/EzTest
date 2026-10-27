//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// catalogDefaultCompanyKey is the single company P2 operates on.
const catalogDefaultCompanyKey = "default"

// seedCatalogCompany restores the default company that testutil.OpenTestDB's
// truncate removes. The W1 repository tests seed it with raw SQL instead of
// changing the shared truncate helper.
func seedCatalogCompany(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *domain.Company {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`INSERT INTO companies (key, name) VALUES ('default', 'Default') ON CONFLICT (key) DO NOTHING`); err != nil {
		t.Fatalf("seed default company: %v", err)
	}
	var company domain.Company
	if err := pool.QueryRow(ctx,
		`SELECT id, key, name, status, created_at, updated_at FROM companies WHERE key = 'default'`).Scan(
		&company.ID, &company.Key, &company.Name, &company.Status,
		&company.CreatedAt, &company.UpdatedAt); err != nil {
		t.Fatalf("load seeded company: %v", err)
	}
	return &company
}

// assertCatalogDomainError returns the domain error carried by err.
func assertCatalogDomainError(t *testing.T, err error) *domain.Error {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("err=%v (%T), want *domain.Error", err, err)
	}
	return domainErr
}

// assertCatalogKind asserts the domain error kind and, when reason is not
// empty, the details.reason code.
func assertCatalogKind(t *testing.T, err error, kind domain.ErrorKind, reason string) {
	t.Helper()
	domainErr := assertCatalogDomainError(t, err)
	if domainErr.Kind != kind {
		t.Fatalf("kind=%q want %q (err=%v)", domainErr.Kind, kind, err)
	}
	if reason != "" && domainErr.Reason != reason {
		t.Fatalf("reason=%q want %q (err=%v)", domainErr.Reason, reason, err)
	}
}

// catalogSystemMatches compares systems ignoring the server-managed timestamps.
func catalogSystemMatches(a, b domain.System) bool {
	a.CreatedAt, a.UpdatedAt = time.Time{}, time.Time{}
	b.CreatedAt, b.UpdatedAt = time.Time{}, time.Time{}
	return a == b
}

// catalogEnvironmentMatches compares environments ignoring the server-managed
// timestamps.
func catalogEnvironmentMatches(a, b domain.Environment) bool {
	a.CreatedAt, a.UpdatedAt = time.Time{}, time.Time{}
	b.CreatedAt, b.UpdatedAt = time.Time{}, time.Time{}
	return reflect.DeepEqual(a, b)
}

// assertCatalogJSON compares two JSON documents semantically, so the JSONB
// representation (key order, whitespace) does not matter.
func assertCatalogJSON(t *testing.T, got, want json.RawMessage) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("got json %q: %v", got, err)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("want json %q: %v", want, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("json=%s want %s", got, want)
	}
}

// assertCatalogAppModel compares app model nodes, comparing Meta semantically
// and ignoring the server-managed timestamps.
func assertCatalogAppModel(t *testing.T, got, want domain.AppModelNode) {
	t.Helper()
	assertCatalogJSON(t, got.Meta, want.Meta)
	got.Meta, want.Meta = nil, nil
	got.CreatedAt, got.UpdatedAt = time.Time{}, time.Time{}
	want.CreatedAt, want.UpdatedAt = time.Time{}, time.Time{}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("node=%+v\nwant %+v", got, want)
	}
}

func catalogInt64(value int64) *int64 { return &value }

func TestCatalogRepositoryRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewCatalogRepository(pool)
	company := seedCatalogCompany(t, ctx, pool)

	gotCompany, err := repo.GetCompanyByKey(ctx, catalogDefaultCompanyKey)
	if err != nil {
		t.Fatal(err)
	}
	if gotCompany.ID != company.ID || gotCompany.Key != "default" || gotCompany.Name != "Default" ||
		gotCompany.Status != domain.StatusActive {
		t.Fatalf("company=%+v", gotCompany)
	}
	if gotCompany.CreatedAt.IsZero() || gotCompany.UpdatedAt.IsZero() {
		t.Fatalf("company timestamps=%v/%v", gotCompany.CreatedAt, gotCompany.UpdatedAt)
	}

	// The application layer relies on the repository applying the DDL defaults:
	// an empty Status becomes active and a zero TTL becomes 3600.
	sys := &domain.System{
		CompanyID: company.ID,
		Key:       "shop",
		Name:      "Shop",
		Kind:      domain.SystemKindWeb,
		BaseURL:   "http://localhost:13335",
		Notes:     "primary system",
	}
	if err := repo.CreateSystem(ctx, sys); err != nil {
		t.Fatal(err)
	}
	if sys.ID == 0 || sys.Status != domain.StatusActive || sys.DefaultSessionTTLSeconds != 3600 {
		t.Fatalf("created system=%+v", sys)
	}
	if sys.CreatedAt.IsZero() || sys.UpdatedAt.IsZero() {
		t.Fatalf("created system timestamps=%v/%v", sys.CreatedAt, sys.UpdatedAt)
	}

	loadedSystem, err := repo.GetSystem(ctx, sys.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !catalogSystemMatches(*loadedSystem, *sys) {
		t.Fatalf("system=%+v\nwant %+v", *loadedSystem, *sys)
	}
	if !loadedSystem.CreatedAt.Equal(sys.CreatedAt) || !loadedSystem.UpdatedAt.Equal(sys.UpdatedAt) {
		t.Fatalf("system timestamps=%v/%v want %v/%v",
			loadedSystem.CreatedAt, loadedSystem.UpdatedAt, sys.CreatedAt, sys.UpdatedAt)
	}

	dev := &domain.Environment{
		SystemID:     sys.ID,
		Key:          "dev",
		Name:         "Development",
		BaseURL:      "http://localhost:13335",
		Kind:         domain.EnvironmentKindDev,
		AllowedHosts: []string{"localhost:13335", "127.0.0.1:13335"},
		LoginCheck: &domain.LoginCheck{
			URL:          "http://localhost:13335/account",
			ExpectStatus: []int{200, 302},
			BodyContains: "Sign out",
		},
		AllowAutoLogin: true,
	}
	if err := repo.CreateEnvironment(ctx, dev); err != nil {
		t.Fatal(err)
	}
	if dev.ID == 0 || dev.CreatedAt.IsZero() || dev.UpdatedAt.IsZero() {
		t.Fatalf("created environment=%+v", dev)
	}
	loadedDev, err := repo.GetEnvironment(ctx, dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !catalogEnvironmentMatches(*loadedDev, *dev) {
		t.Fatalf("environment=%+v\nwant %+v", *loadedDev, *dev)
	}

	// An environment without login_check stores and returns SQL NULL.
	prod := &domain.Environment{
		SystemID:        sys.ID,
		Key:             "prod",
		Name:            "Production",
		BaseURL:         "https://shop.example.test",
		Kind:            domain.EnvironmentKindProd,
		IsProd:          true,
		RequireApproval: true,
	}
	if err := repo.CreateEnvironment(ctx, prod); err != nil {
		t.Fatal(err)
	}
	loadedProd, err := repo.GetEnvironment(ctx, prod.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedProd.LoginCheck != nil {
		t.Fatalf("prod login_check=%+v want nil", loadedProd.LoginCheck)
	}
	if loadedProd.AllowedHosts == nil {
		t.Fatal("prod allowed_hosts is nil, want an empty slice")
	}
	if !catalogEnvironmentMatches(*loadedProd, *prod) {
		t.Fatalf("production environment=%+v\nwant %+v", *loadedProd, *prod)
	}

	envs, err := repo.ListEnvironments(ctx, sys.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 2 || envs[0].ID != dev.ID || envs[1].ID != prod.ID {
		t.Fatalf("environments=%+v", envs)
	}
	if envs, err := repo.ListEnvironments(ctx, 999999); err != nil || len(envs) != 0 {
		t.Fatalf("environments of missing system=%v err=%v", envs, err)
	}
	if count, err := repo.CountEnvironments(ctx, sys.ID); err != nil || count != 2 {
		t.Fatalf("CountEnvironments=%d err=%v", count, err)
	}
	if count, err := repo.CountAccesses(ctx, sys.ID); err != nil || count != 0 {
		t.Fatalf("CountAccesses=%d err=%v", count, err)
	}
	if count, err := repo.CountSessions(ctx, dev.ID); err != nil || count != 0 {
		t.Fatalf("CountSessions=%d err=%v", count, err)
	}

	page := &domain.AppModelNode{
		SystemID: sys.ID,
		Kind:     domain.AppModelKindPage,
		Key:      "page:/orders",
		Name:     "Orders",
		Path:     "/orders",
	}
	if err := repo.CreateAppModel(ctx, page); err != nil {
		t.Fatal(err)
	}
	if page.ID == 0 || page.Source != "manual" {
		t.Fatalf("created page node=%+v", page)
	}
	assertCatalogJSON(t, page.Meta, json.RawMessage(`{}`))

	api := &domain.AppModelNode{
		SystemID: sys.ID,
		ParentID: &page.ID,
		Kind:     domain.AppModelKindAPI,
		Key:      "api:GET /api/orders",
		Name:     "List orders",
		Path:     "/api/orders",
		Method:   "GET",
		Source:   "import",
		Meta:     json.RawMessage(`{"scope":"read"}`),
	}
	if err := repo.CreateAppModel(ctx, api); err != nil {
		t.Fatal(err)
	}
	loadedAPI, err := repo.GetAppModel(ctx, api.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedAPI.ParentID == nil || *loadedAPI.ParentID != page.ID {
		t.Fatalf("api parent=%v want %d", loadedAPI.ParentID, page.ID)
	}
	if loadedAPI.Source != "import" {
		t.Fatalf("api source=%q want import", loadedAPI.Source)
	}
	assertCatalogAppModel(t, *loadedAPI, *api)

	nodes, more, err := repo.ListAppModels(ctx, domain.AppModelFilter{
		SystemID: sys.ID,
		Page:     domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 || more || nodes[0].ID != page.ID || nodes[1].ID != api.ID {
		t.Fatalf("nodes=%+v more=%v", nodes, more)
	}

	byKey, err := repo.ListAppModelsByKeys(ctx, sys.ID,
		[]string{"page:/orders", "api:GET /api/orders", "api:DELETE /api/orders"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byKey) != 2 {
		t.Fatalf("byKey=%v", byKey)
	}
	if node, ok := byKey["page:/orders"]; !ok || node.ID != page.ID {
		t.Fatalf("page by key=%+v ok=%v", node, ok)
	}
	if _, ok := byKey["api:GET /api/orders"]; !ok {
		t.Fatal("api node missing from byKey")
	}
	if _, ok := byKey["api:DELETE /api/orders"]; ok {
		t.Fatal("unknown key unexpectedly present in byKey")
	}
	emptyByKey, err := repo.ListAppModelsByKeys(ctx, sys.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if emptyByKey == nil || len(emptyByKey) != 0 {
		t.Fatalf("byKey for empty keys=%v want non-nil empty map", emptyByKey)
	}

	// Update every mutable field; the key is immutable for all four resources.
	sys.Name = "Shop v2"
	sys.Kind = domain.SystemKindAPI
	sys.BaseURL = "http://localhost:13336"
	sys.DefaultSessionTTLSeconds = 7200
	sys.Notes = ""
	sys.Status = domain.StatusDisabled
	if err := repo.UpdateSystem(ctx, sys); err != nil {
		t.Fatal(err)
	}
	reloadedSystem, err := repo.GetSystem(ctx, sys.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !catalogSystemMatches(*reloadedSystem, *sys) {
		t.Fatalf("updated system=%+v\nwant %+v", *reloadedSystem, *sys)
	}
	if !reloadedSystem.CreatedAt.Equal(sys.CreatedAt) || !reloadedSystem.UpdatedAt.After(sys.CreatedAt) {
		t.Fatalf("updated system timestamps=%v/%v created=%v",
			reloadedSystem.CreatedAt, reloadedSystem.UpdatedAt, sys.CreatedAt)
	}
	if disabled, more, err := repo.ListSystems(ctx, domain.SystemFilter{
		Status: domain.StatusDisabled,
		Page:   domain.Page{Limit: 10},
	}); err != nil || len(disabled) != 1 || more {
		t.Fatalf("disabled systems=%+v more=%v err=%v", disabled, more, err)
	}

	dev.Name = "Development v2"
	dev.BaseURL = "http://localhost:13336"
	dev.Kind = domain.EnvironmentKindStaging
	dev.IsProd = true
	dev.AllowedHosts = []string{"localhost:13336"}
	dev.LoginCheck = nil
	dev.AllowAutoLogin = false
	dev.RequireApproval = true
	if err := repo.UpdateEnvironment(ctx, dev); err != nil {
		t.Fatal(err)
	}
	reloadedDev, err := repo.GetEnvironment(ctx, dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !catalogEnvironmentMatches(*reloadedDev, *dev) {
		t.Fatalf("updated environment=%+v\nwant %+v", *reloadedDev, *dev)
	}
	if !reloadedDev.CreatedAt.Equal(dev.CreatedAt) || !reloadedDev.UpdatedAt.After(dev.CreatedAt) {
		t.Fatalf("updated environment timestamps=%v/%v created=%v",
			reloadedDev.CreatedAt, reloadedDev.UpdatedAt, dev.CreatedAt)
	}

	api.Name = "List orders v2"
	api.Path = "/api/v2/orders"
	api.ParentID = nil
	api.Meta = json.RawMessage(`{"scope":"write"}`)
	if err := repo.UpdateAppModel(ctx, api); err != nil {
		t.Fatal(err)
	}
	reloadedAPI, err := repo.GetAppModel(ctx, api.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloadedAPI.ParentID != nil {
		t.Fatalf("updated api parent=%v want nil", reloadedAPI.ParentID)
	}
	if !reloadedAPI.UpdatedAt.After(api.CreatedAt) {
		t.Fatalf("updated node updated_at=%v created_at=%v", reloadedAPI.UpdatedAt, api.CreatedAt)
	}
	assertCatalogAppModel(t, *reloadedAPI, *api)

	if err := repo.DeleteEnvironment(ctx, dev.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetEnvironment(ctx, dev.ID); err == nil {
		t.Fatal("deleted environment still readable")
	}
	if envs, err := repo.ListEnvironments(ctx, sys.ID); err != nil || len(envs) != 1 || envs[0].ID != prod.ID {
		t.Fatalf("environments after delete=%+v err=%v", envs, err)
	}
	if count, err := repo.CountEnvironments(ctx, sys.ID); err != nil || count != 1 {
		t.Fatalf("CountEnvironments after delete=%d err=%v", count, err)
	}

	// The API node was re-parented to the root above, so it is deleted on its
	// own before the page. Subtree cascade is covered by
	// TestCatalogRepositoryAppModelFiltersAndCascade.
	if err := repo.DeleteAppModel(ctx, api.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetAppModel(ctx, api.ID); err == nil {
		t.Fatal("deleted app model still readable")
	}
	if err := repo.DeleteAppModel(ctx, page.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetAppModel(ctx, page.ID); err == nil {
		t.Fatal("deleted app model still readable")
	}

	// Deleting the system cascades to its remaining environment.
	if err := repo.DeleteSystem(ctx, sys.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetSystem(ctx, sys.ID); err == nil {
		t.Fatal("deleted system still readable")
	}
	if _, err := repo.GetEnvironment(ctx, prod.ID); err == nil {
		t.Fatal("environment survived the system delete")
	}
	remaining, more, err := repo.ListSystems(ctx, domain.SystemFilter{Page: domain.Page{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 || more {
		t.Fatalf("systems after delete=%+v more=%v", remaining, more)
	}
}

func TestCatalogRepositorySystemPaginationAndFilters(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewCatalogRepository(pool)
	company := seedCatalogCompany(t, ctx, pool)

	systems := []*domain.System{
		{CompanyID: company.ID, Key: "shop-one", Name: "One", Kind: domain.SystemKindWeb, BaseURL: "http://localhost:13335/1"},
		{CompanyID: company.ID, Key: "shop-two", Name: "Two", Kind: domain.SystemKindWeb, BaseURL: "http://localhost:13335/2"},
		{CompanyID: company.ID, Key: "shop-api", Name: "API", Kind: domain.SystemKindAPI, BaseURL: "http://localhost:13335/3", Status: domain.StatusDisabled},
	}
	for _, system := range systems {
		if err := repo.CreateSystem(ctx, system); err != nil {
			t.Fatal(err)
		}
	}

	first, more, err := repo.ListSystems(ctx, domain.SystemFilter{Page: domain.Page{Limit: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || !more || first[0].ID != systems[0].ID || first[1].ID != systems[1].ID {
		t.Fatalf("first page=%+v more=%v", first, more)
	}
	second, more, err := repo.ListSystems(ctx, domain.SystemFilter{
		Page: domain.Page{Limit: 2, AfterID: first[len(first)-1].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || more || second[0].ID != systems[2].ID {
		t.Fatalf("second page=%+v more=%v", second, more)
	}
	all, more, err := repo.ListSystems(ctx, domain.SystemFilter{Page: domain.Page{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || more {
		t.Fatalf("all=%+v more=%v", all, more)
	}

	web, more, err := repo.ListSystems(ctx, domain.SystemFilter{Kind: domain.SystemKindWeb, Page: domain.Page{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(web) != 2 || more {
		t.Fatalf("web systems=%+v more=%v", web, more)
	}
	active, more, err := repo.ListSystems(ctx, domain.SystemFilter{Status: domain.StatusActive, Page: domain.Page{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 || more {
		t.Fatalf("active systems=%+v more=%v", active, more)
	}
	activeWeb, more, err := repo.ListSystems(ctx, domain.SystemFilter{
		Status: domain.StatusActive,
		Kind:   domain.SystemKindWeb,
		Page:   domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(activeWeb) != 2 || more {
		t.Fatalf("active web systems=%+v more=%v", activeWeb, more)
	}
	none, more, err := repo.ListSystems(ctx, domain.SystemFilter{
		Status: domain.StatusDisabled,
		Kind:   domain.SystemKindWeb,
		Page:   domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 || more {
		t.Fatalf("disabled web systems=%+v more=%v", none, more)
	}

	if _, _, err := repo.ListSystems(ctx, domain.SystemFilter{Page: domain.Page{Limit: 0}}); err == nil {
		t.Fatal("ListSystems accepted a zero limit")
	}
}

func TestCatalogRepositoryAppModelFiltersAndCascade(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewCatalogRepository(pool)
	company := seedCatalogCompany(t, ctx, pool)

	sys := &domain.System{CompanyID: company.ID, Key: "shop", Name: "Shop", Kind: domain.SystemKindWeb, BaseURL: "http://localhost:13335"}
	if err := repo.CreateSystem(ctx, sys); err != nil {
		t.Fatal(err)
	}

	page := &domain.AppModelNode{SystemID: sys.ID, Kind: domain.AppModelKindPage, Key: "page:/orders", Name: "Orders", Path: "/orders"}
	if err := repo.CreateAppModel(ctx, page); err != nil {
		t.Fatal(err)
	}
	route := &domain.AppModelNode{SystemID: sys.ID, ParentID: &page.ID, Kind: domain.AppModelKindRoute, Key: "route:/orders", Name: "Order list", Path: "/orders"}
	if err := repo.CreateAppModel(ctx, route); err != nil {
		t.Fatal(err)
	}
	api := &domain.AppModelNode{
		SystemID: sys.ID, ParentID: &route.ID, Kind: domain.AppModelKindAPI,
		Key: "api:GET /api/orders", Name: "List orders", Path: "/api/orders", Method: "GET",
	}
	if err := repo.CreateAppModel(ctx, api); err != nil {
		t.Fatal(err)
	}
	component := &domain.AppModelNode{SystemID: sys.ID, Kind: domain.AppModelKindComponent, Key: "component:OrderTable", Name: "OrderTable"}
	if err := repo.CreateAppModel(ctx, component); err != nil {
		t.Fatal(err)
	}

	all, more, err := repo.ListAppModels(ctx, domain.AppModelFilter{SystemID: sys.ID, Page: domain.Page{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 || more {
		t.Fatalf("all nodes=%+v more=%v", all, more)
	}

	apis, more, err := repo.ListAppModels(ctx, domain.AppModelFilter{
		SystemID: sys.ID,
		Kind:     domain.AppModelKindAPI,
		Page:     domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(apis) != 1 || more || apis[0].ID != api.ID {
		t.Fatalf("api nodes=%+v more=%v", apis, more)
	}

	children, more, err := repo.ListAppModels(ctx, domain.AppModelFilter{
		SystemID:  sys.ID,
		ParentKey: "route:/orders",
		Page:      domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 || more || children[0].ID != api.ID {
		t.Fatalf("children=%+v more=%v", children, more)
	}
	orphans, more, err := repo.ListAppModels(ctx, domain.AppModelFilter{
		SystemID:  sys.ID,
		ParentKey: "route:/missing",
		Page:      domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 0 || more {
		t.Fatalf("children of missing parent=%+v more=%v", orphans, more)
	}

	first, more, err := repo.ListAppModels(ctx, domain.AppModelFilter{SystemID: sys.ID, Page: domain.Page{Limit: 3}})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 3 || !more {
		t.Fatalf("first page=%+v more=%v", first, more)
	}
	rest, more, err := repo.ListAppModels(ctx, domain.AppModelFilter{
		SystemID: sys.ID,
		Page:     domain.Page{Limit: 3, AfterID: first[len(first)-1].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 1 || more || rest[0].ID != component.ID {
		t.Fatalf("rest=%+v more=%v", rest, more)
	}

	// Deleting the root cascades through route and api but leaves the
	// unrelated component.
	if err := repo.DeleteAppModel(ctx, page.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetAppModel(ctx, route.ID); err == nil {
		t.Fatal("route survived the page delete")
	}
	if _, err := repo.GetAppModel(ctx, api.ID); err == nil {
		t.Fatal("api survived the page delete")
	}
	if _, err := repo.GetAppModel(ctx, component.ID); err != nil {
		t.Fatalf("unrelated component disappeared: %v", err)
	}
}

func TestCatalogRepositoryConflictsAndDependencies(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewCatalogRepository(pool)
	company := seedCatalogCompany(t, ctx, pool)

	sys := &domain.System{CompanyID: company.ID, Key: "shop", Name: "Shop", Kind: domain.SystemKindWeb, BaseURL: "http://localhost:13335"}
	if err := repo.CreateSystem(ctx, sys); err != nil {
		t.Fatal(err)
	}
	duplicateSystem := &domain.System{CompanyID: company.ID, Key: "shop", Name: "Dup", Kind: domain.SystemKindWeb, BaseURL: "http://localhost:13335"}
	assertCatalogKind(t, repo.CreateSystem(ctx, duplicateSystem), domain.KindConflict, "duplicate_key")

	// (company_id, key) is unique, so another company may reuse the key.
	if _, err := pool.Exec(ctx, `INSERT INTO companies (key, name) VALUES ('other', 'Other')`); err != nil {
		t.Fatal(err)
	}
	var otherCompanyID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM companies WHERE key = 'other'`).Scan(&otherCompanyID); err != nil {
		t.Fatal(err)
	}
	otherSystem := &domain.System{CompanyID: otherCompanyID, Key: "shop", Name: "Other shop", Kind: domain.SystemKindWeb, BaseURL: "http://localhost:13335"}
	if err := repo.CreateSystem(ctx, otherSystem); err != nil {
		t.Fatalf("same key under another company: %v", err)
	}

	env := &domain.Environment{SystemID: sys.ID, Key: "dev", Name: "Dev", BaseURL: "http://localhost:13335", Kind: domain.EnvironmentKindDev}
	if err := repo.CreateEnvironment(ctx, env); err != nil {
		t.Fatal(err)
	}
	duplicateEnv := &domain.Environment{SystemID: sys.ID, Key: "dev", Name: "Dup", BaseURL: "http://localhost:13335", Kind: domain.EnvironmentKindDev}
	assertCatalogKind(t, repo.CreateEnvironment(ctx, duplicateEnv), domain.KindConflict, "duplicate_key")

	node := &domain.AppModelNode{SystemID: sys.ID, Kind: domain.AppModelKindPage, Key: "page:/orders", Name: "Orders", Path: "/orders"}
	if err := repo.CreateAppModel(ctx, node); err != nil {
		t.Fatal(err)
	}
	duplicateNode := &domain.AppModelNode{SystemID: sys.ID, Kind: domain.AppModelKindPage, Key: "page:/orders", Name: "Dup", Path: "/orders"}
	assertCatalogKind(t, repo.CreateAppModel(ctx, duplicateNode), domain.KindConflict, "duplicate_key")

	// Missing referenced rows surface as dependency errors (SQLSTATE 23503).
	orphanSystem := &domain.System{CompanyID: 999999, Key: "orphan", Name: "Orphan", Kind: domain.SystemKindWeb, BaseURL: "http://localhost:13335"}
	assertCatalogKind(t, repo.CreateSystem(ctx, orphanSystem), domain.KindDependency, "has_dependencies")
	orphanEnv := &domain.Environment{SystemID: 999999, Key: "dev", Name: "Orphan", BaseURL: "http://localhost:13335", Kind: domain.EnvironmentKindDev}
	assertCatalogKind(t, repo.CreateEnvironment(ctx, orphanEnv), domain.KindDependency, "has_dependencies")
	orphanNode := &domain.AppModelNode{SystemID: 999999, Kind: domain.AppModelKindPage, Key: "page:/orphan", Name: "Orphan", Path: "/orphan"}
	assertCatalogKind(t, repo.CreateAppModel(ctx, orphanNode), domain.KindDependency, "has_dependencies")
	badParent := &domain.AppModelNode{
		SystemID: sys.ID, ParentID: catalogInt64(999999),
		Kind: domain.AppModelKindRoute, Key: "route:/orphan", Name: "Orphan", Path: "/orphan",
	}
	assertCatalogKind(t, repo.CreateAppModel(ctx, badParent), domain.KindDependency, "has_dependencies")

	// Re-parenting an existing node to a missing node is a write constraint
	// violation too, so it must also map to a dependency error.
	reparented := &domain.AppModelNode{
		ID: node.ID, SystemID: sys.ID, ParentID: catalogInt64(999999),
		Kind: domain.AppModelKindPage, Key: "page:/orders", Name: "Orders", Path: "/orders",
		Meta: json.RawMessage(`{}`),
	}
	assertCatalogKind(t, repo.UpdateAppModel(ctx, reparented), domain.KindDependency, "has_dependencies")
}

func TestCatalogRepositoryNotFound(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewCatalogRepository(pool)
	seedCatalogCompany(t, ctx, pool)

	_, err := repo.GetCompanyByKey(ctx, "missing")
	assertCatalogKind(t, err, domain.KindNotFound, "not_found")
	_, err = repo.GetSystem(ctx, 999999)
	assertCatalogKind(t, err, domain.KindNotFound, "not_found")
	_, err = repo.GetEnvironment(ctx, 999999)
	assertCatalogKind(t, err, domain.KindNotFound, "not_found")
	_, err = repo.GetAppModel(ctx, 999999)
	assertCatalogKind(t, err, domain.KindNotFound, "not_found")

	missingSystem := &domain.System{
		ID: 999999, CompanyID: 1, Key: "missing", Name: "Missing", Kind: domain.SystemKindWeb,
		BaseURL: "http://localhost:13335", DefaultSessionTTLSeconds: 3600, Status: domain.StatusActive,
	}
	assertCatalogKind(t, repo.UpdateSystem(ctx, missingSystem), domain.KindNotFound, "not_found")
	missingEnv := &domain.Environment{
		ID: 999999, SystemID: 1, Key: "missing", Name: "Missing", BaseURL: "http://localhost:13335",
		Kind: domain.EnvironmentKindDev, AllowedHosts: []string{},
	}
	assertCatalogKind(t, repo.UpdateEnvironment(ctx, missingEnv), domain.KindNotFound, "not_found")
	missingNode := &domain.AppModelNode{
		ID: 999999, SystemID: 1, Kind: domain.AppModelKindPage, Key: "page:/missing", Name: "Missing",
		Path: "/missing", Meta: json.RawMessage(`{}`),
	}
	assertCatalogKind(t, repo.UpdateAppModel(ctx, missingNode), domain.KindNotFound, "not_found")

	assertCatalogKind(t, repo.DeleteSystem(ctx, 999999), domain.KindNotFound, "not_found")
	assertCatalogKind(t, repo.DeleteEnvironment(ctx, 999999), domain.KindNotFound, "not_found")
	assertCatalogKind(t, repo.DeleteAppModel(ctx, 999999), domain.KindNotFound, "not_found")
}
