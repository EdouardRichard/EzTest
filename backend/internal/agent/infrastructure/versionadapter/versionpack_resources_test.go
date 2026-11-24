package versionadapter_test

import (
	"context"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	versionadapter "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/versionadapter"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// resourceBoundVersion is one version bound to an environment whose single SUT
// carries a discovered page (status proposed), an approved page, an API and a
// database, plus a version-level account subset. v0.2.4-B: the SUT catalog is
// nested under the environment and the assets hang off the SUT.
func resourceBoundVersion() versionapp.VersionView {
	return versionapp.VersionView{
		ID:            2001,
		Title:         "订单中心 · 资源绑定",
		Status:        "running",
		EnvironmentID: 1101,
		Binding: versionapp.EnvironmentSUTCatalog{
			VersionID:     2001,
			EnvironmentID: 1101,
			Environment: resourceapp.EnvironmentView{
				ID: "1101", Name: "local", Status: "active",
				CreatedAt: testTime, UpdatedAt: testTime,
				SUTs: []resourceapp.SUTView{
					{
						ID: "1001", EnvironmentID: "1101", Name: "订单中心", Intro: "订单",
						BaseURL: "http://sut.test", CreatedAt: testTime, UpdatedAt: testTime,
						Pages: []resourceapp.PageView{
							{
								ID: "1301", SUTID: "1001", Name: "订单列表",
								URL: "http://sut.test/orders", RouteTemplate: "/orders",
								Status: "approved", Source: "admin", CreatedAt: testTime, UpdatedAt: testTime,
							},
							{
								ID: "1304", SUTID: "1001", Name: "登录页",
								URL: "http://sut.test/login", RouteTemplate: "/login",
								Status: "proposed", Source: "ai", CreatedAt: testTime, UpdatedAt: testTime,
							},
						},
						APIs: []resourceapp.APIView{
							{
								ID: "1401", SUTID: "1001", Method: "GET",
								PathTemplate: "/api/orders", Name: "查询订单",
								Status: "approved", Source: "manual", CreatedAt: testTime, UpdatedAt: testTime,
							},
						},
						Databases: []resourceapp.DatabaseView{
							{ID: "1501", SUTID: "1001", Name: "order_db", ReadOnly: true, Status: "active", CreatedAt: testTime, UpdatedAt: testTime},
							{ID: "1503", SUTID: "1001", Name: "legacy_db", ReadOnly: false, Status: "disabled", CreatedAt: testTime, UpdatedAt: testTime},
						},
					},
				},
			},
			Accounts: []resourceapp.AccountView{
				{ID: "1601", SUTID: "1001", Username: "admin", Roles: []string{"admin"}, Status: "active", CreatedAt: testTime, UpdatedAt: testTime},
			},
		},
		CreatedAt: testTime,
		UpdatedAt: testTime,
	}
}

// TestVersionBindingHasResources pins the design locator path: a version that
// binds pages/APIs exposes them in the agent VersionPack the design context
// renders (page name + route template locators) and in the sut.page_list tool
// inventory. The execution side (navigable page URLs / API endpoints of the
// task-context SUT binding) is pinned by the VersionAdapter Resolve tests in
// the execution adapters package. A version with zero bound pages is exactly
// what made the product-owner AI design cite an empty page list.
func TestVersionBindingHasResources(t *testing.T) {
	version := resourceBoundVersion()
	versions := &fakeVersions{getFn: func(context.Context, int64) (versionapp.VersionView, error) {
		return version, nil
	}}
	requirements := &fakeRequirements{
		listFn: func(context.Context, int64, string, int, *int64) ([]versionapp.RequirementView, bool, error) {
			return []versionapp.RequirementView{}, false, nil
		},
		getFn: func(context.Context, int64, int64) (versionapp.RequirementDetail, error) {
			return versionapp.RequirementDetail{}, nil
		},
	}
	adapter := versionadapter.New(requirements, nil, versions, nil, nil)

	pack, err := adapter.VersionPack(context.Background(), 2001)
	if err != nil {
		t.Fatalf("VersionPack: %v", err)
	}
	// v0.2.4-B review round 1, Important 1: the pack feeds the design AI's page
	// locators, so it must carry the EFFECTIVE scope only. The proposed page 1304
	// is catalog state a discovery run suggested; it is not part of the version's
	// scope and citing it would send the design model at a page nobody approved.
	if len(pack.Pages) != 1 {
		t.Fatalf("version pack pages = %#v, want only the approved page", pack.Pages)
	}
	if pack.Pages[0] != "订单列表 /orders" {
		t.Fatalf("version pack pages = %#v, want the approved name + route template locator", pack.Pages)
	}
	if len(pack.APIs) != 1 || pack.APIs[0] != "GET /api/orders" {
		t.Fatalf("version pack apis = %#v, want the bound API shape", pack.APIs)
	}

	pages, err := adapter.ListPages(context.Background(), 2001, 1001)
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("resource page inventory = %#v, want the 1 approved page + 1 approved API", pages)
	}
	if pages[0].Path != "/orders" || pages[1].Path != "/api/orders" {
		t.Fatalf("inventory routes = %q/%q, want /orders and /api/orders", pages[0].Path, pages[1].Path)
	}
	for _, item := range pages {
		if item.ID == "1304" {
			t.Fatalf("sut.page_list exposes the PROPOSED page: %#v", item)
		}
	}
}

// TestDatabaseListIncludesEnvironmentDatabases pins R-E1-2: the agent VersionPack
// carries the databases of the SUTs of the bound environment
// (id/name/kind/read_only) so both the context prompt and the sut.database_list
// tool expose the same list without the model guessing an id.
func TestDatabaseListIncludesEnvironmentDatabases(t *testing.T) {
	version := resourceBoundVersion()
	versions := &fakeVersions{getFn: func(context.Context, int64) (versionapp.VersionView, error) {
		return version, nil
	}}
	requirements := &fakeRequirements{
		listFn: func(context.Context, int64, string, int, *int64) ([]versionapp.RequirementView, bool, error) {
			return []versionapp.RequirementView{}, false, nil
		},
		getFn: func(context.Context, int64, int64) (versionapp.RequirementDetail, error) {
			return versionapp.RequirementDetail{}, nil
		},
	}
	adapter := versionadapter.New(requirements, nil, versions, nil, nil)

	pack, err := adapter.VersionPack(context.Background(), 2001)
	if err != nil {
		t.Fatalf("VersionPack: %v", err)
	}
	if len(pack.Databases) == 0 {
		t.Fatal("version pack carries no databases; the design model cannot choose a database_id")
	}
	database := pack.Databases[0]
	if database.ID != "1501" || database.Name != "order_db" ||
		database.Kind != "postgres" || !database.ReadOnly {
		t.Fatalf("version pack database = %#v, want the bound order_db", database)
	}
	// The disabled legacy_db is catalog state, not version scope: the model must
	// not be offered a database_id whose credential execution would refuse.
	if len(pack.Databases) != 1 {
		t.Fatalf("version pack databases = %#v, want only the ACTIVE database", pack.Databases)
	}

	listed, err := adapter.ListDatabases(context.Background(), 2001, 0)
	if err != nil {
		t.Fatalf("ListDatabases: %v", err)
	}
	if len(listed) != 1 || listed[0] != database {
		t.Fatalf("sut.database_list databases = %#v, want only the active bound database", listed)
	}

	filtered, err := adapter.ListDatabases(context.Background(), 2001, 1001)
	if err != nil || len(filtered) != 1 {
		t.Fatalf("filtered ListDatabases = %#v err=%v", filtered, err)
	}
	if _, err := adapter.ListDatabases(context.Background(), 2001, 9999); err == nil {
		t.Fatal("ListDatabases accepted an unbound sut_id")
	}

	var _ tools.DatabaseLister = adapter
}
