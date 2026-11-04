package versionadapter_test

import (
	"context"
	"testing"

	versionadapter "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/versionadapter"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// resourceBoundVersion is one SUT binding that carries a discovered page (status
// proposed), an approved page, an API, a database and an account. It is the
// shape the version read model returns after the task-76 discovery hand-off and
// binding flow.
func resourceBoundVersion() versionapp.VersionView {
	return versionapp.VersionView{
		ID:     2001,
		Title:  "订单中心 · 资源绑定",
		Status: "running",
		Bindings: []versionapp.BindingView{
			{
				ID: 3001,
				SUT: resourceapp.SUTView{
					ID: "1001", Name: "订单中心", Intro: "订单", CreatedAt: testTime, UpdatedAt: testTime,
				},
				Environment: resourceapp.EnvironmentView{
					ID: "1101", SUTID: "1001", Name: "local", BaseURL: "http://sut.test",
					Status: "active", CreatedAt: testTime, UpdatedAt: testTime,
				},
				Pages: []resourceapp.PageView{
					{
						ID: "1301", SUTID: "1001", EnvironmentID: "1101", Name: "订单列表",
						URL: "http://sut.test/orders", RouteTemplate: "/orders",
						Status: "approved", Source: "admin", CreatedAt: testTime, UpdatedAt: testTime,
					},
					{
						ID: "1304", SUTID: "1001", EnvironmentID: "1101", Name: "登录页",
						URL: "http://sut.test/login", RouteTemplate: "/login",
						Status: "proposed", Source: "ai", CreatedAt: testTime, UpdatedAt: testTime,
					},
				},
				APIs: []resourceapp.APIView{
					{
						ID: "1401", SUTID: "1001", EnvironmentID: "1101", Method: "GET",
						PathTemplate: "/api/orders", Name: "查询订单",
						Status: "approved", Source: "manual", CreatedAt: testTime, UpdatedAt: testTime,
					},
				},
				Databases: []resourceapp.DatabaseView{
					{ID: "1501", SUTID: "1001", EnvironmentID: "1101", Name: "order_db", ReadOnly: true, Status: "active", CreatedAt: testTime, UpdatedAt: testTime},
				},
				Accounts: []resourceapp.AccountView{
					{ID: "1601", SUTID: "1001", EnvironmentID: "1101", Username: "admin", Roles: []string{"admin"}, Status: "active", CreatedAt: testTime, UpdatedAt: testTime},
				},
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
	if len(pack.Pages) != 2 {
		t.Fatalf("version pack pages = %#v, want the two bound pages the design AI reads", pack.Pages)
	}
	if pack.Pages[0] != "订单列表 /orders" || pack.Pages[1] != "登录页 /login" {
		t.Fatalf("version pack pages = %#v, want name + route template locators", pack.Pages)
	}
	if len(pack.APIs) != 1 || pack.APIs[0] != "GET /api/orders" {
		t.Fatalf("version pack apis = %#v, want the bound API shape", pack.APIs)
	}

	pages, err := adapter.ListPages(context.Background(), 2001, 3001)
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}
	if len(pages) != 3 {
		t.Fatalf("resource page inventory = %#v, want 2 pages + 1 API", pages)
	}
	if pages[0].Path != "/orders" || pages[1].Path != "/login" {
		t.Fatalf("page routes = %q/%q, want /orders and /login", pages[0].Path, pages[1].Path)
	}
}
