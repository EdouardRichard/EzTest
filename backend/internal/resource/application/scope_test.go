package resourceapp

import (
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// v0.2.4-B review round 1, Important 1: the effective-scope rule (approved pages,
// approved APIs, active target databases) has exactly ONE implementation — here,
// in the resource projection layer — and it is expressed with the resource
// domain's own status constants so it cannot drift from the CHECK constraints
// that persist those statuses. Every version-scope consumer (the execution
// fingerprint and discovery scope, credential resolution, the agent tool
// inventories and VersionPack, the knowledge binding keys, the P9 login and
// binding keys) reads it through SUTView.EffectiveAssets /
// EnvironmentView.EffectiveScope / versionapp.EnvironmentSUTCatalog.EffectiveSUTs
// instead of re-implementing a status predicate.

// scopeFixtureSUT carries every status the catalog can hold for each asset kind.
func scopeFixtureSUT() SUTView {
	return SUTView{
		ID: "1001", EnvironmentID: "1101", Name: "订单中心", BaseURL: "http://sut.test",
		Pages: []PageView{
			{ID: "1301", SUTID: "1001", Name: "订单列表", RouteTemplate: "/orders", Status: resource.PageStatusApproved},
			{ID: "1302", SUTID: "1001", Name: "登录页", RouteTemplate: "/login", Status: resource.PageStatusApproved},
			{ID: "1303", SUTID: "1001", Name: "候选页", RouteTemplate: "/candidate", Status: resource.PageStatusProposed},
			{ID: "1304", SUTID: "1001", Name: "被拒页", RouteTemplate: "/rejected", Status: resource.PageStatusRejected},
		},
		APIs: []APIView{
			{ID: "1401", SUTID: "1001", Method: "GET", PathTemplate: "/api/orders", Status: resource.APIStatusApproved},
			{ID: "1402", SUTID: "1001", Method: "POST", PathTemplate: "/api/candidate", Status: resource.APIStatusProposed},
			{ID: "1403", SUTID: "1001", Method: "PUT", PathTemplate: "/api/rejected", Status: resource.APIStatusRejected},
		},
		Databases: []DatabaseView{
			{ID: "1501", SUTID: "1001", Name: "order_db", Status: resource.StatusActive},
			{ID: "1502", SUTID: "1001", Name: "legacy_db", Status: resource.StatusDisabled},
		},
	}
}

func TestSUTViewEffectiveAssetsIsTheVersionScope(t *testing.T) {
	scope := scopeFixtureSUT().EffectiveAssets()

	if len(scope.Pages) != 2 || scope.Pages[0].ID != "1301" || scope.Pages[1].ID != "1302" {
		t.Fatalf("effective pages = %#v, want only the two approved rows in catalog order", scope.Pages)
	}
	if len(scope.APIs) != 1 || scope.APIs[0].ID != "1401" {
		t.Fatalf("effective apis = %#v, want only the approved row", scope.APIs)
	}
	if len(scope.Databases) != 1 || scope.Databases[0].ID != "1501" {
		t.Fatalf("effective databases = %#v, want only the active row", scope.Databases)
	}
}

// TestSUTViewEffectiveAssetsKeepsTheRawCatalogLists pins the other half of the
// contract: the resource catalog UI must still see proposed/rejected/disabled
// rows, so the projection is additive and never mutates the raw lists.
func TestSUTViewEffectiveAssetsKeepsTheRawCatalogLists(t *testing.T) {
	sut := scopeFixtureSUT()
	scope := sut.EffectiveAssets()

	if len(sut.Pages) != 4 || len(sut.APIs) != 3 || len(sut.Databases) != 2 {
		t.Fatalf("raw catalog lists shrank to %d/%d/%d, want 4/3/2", len(sut.Pages), len(sut.APIs), len(sut.Databases))
	}
	// Mutating the returned scope must not reach back into the SUT view.
	scope.Pages[0].Name = "mutated"
	if sut.Pages[0].Name == "mutated" {
		t.Fatal("the effective projection aliases the raw catalog rows")
	}
}

func TestEnvironmentViewEffectiveScopeIsTheSUTUnionInOrder(t *testing.T) {
	first := scopeFixtureSUT()
	second := SUTView{
		ID: "1002", EnvironmentID: "1101", Name: "管理后台", BaseURL: "http://admin.test",
		Pages: []PageView{
			{ID: "1305", SUTID: "1002", Name: "仪表盘", RouteTemplate: "/dashboard", Status: resource.PageStatusApproved},
			{ID: "1306", SUTID: "1002", Name: "候选", RouteTemplate: "/maybe", Status: resource.PageStatusProposed},
		},
		APIs: []APIView{
			{ID: "1404", SUTID: "1002", Method: "GET", PathTemplate: "/api/admin", Status: resource.APIStatusApproved},
		},
		Databases: []DatabaseView{
			{ID: "1503", SUTID: "1002", Name: "admin_db", Status: resource.StatusDisabled},
		},
	}
	environment := EnvironmentView{ID: "1101", Name: "prod", Status: resource.StatusActive, SUTs: []SUTView{first, second}}

	scope := environment.EffectiveScope()

	// The union is per SUT, in catalog order: this is exactly the list the version
	// binding exposes as its flat effective scope.
	wantPages := []string{"1301", "1302", "1305"}
	if len(scope.Pages) != len(wantPages) {
		t.Fatalf("effective pages = %#v, want %v", scope.Pages, wantPages)
	}
	for index, want := range wantPages {
		if scope.Pages[index].ID != want {
			t.Fatalf("effective page %d = %q, want %q", index, scope.Pages[index].ID, want)
		}
	}
	if len(scope.APIs) != 2 || scope.APIs[0].ID != "1401" || scope.APIs[1].ID != "1404" {
		t.Fatalf("effective apis = %#v, want [1401 1404]", scope.APIs)
	}
	if len(scope.Databases) != 1 || scope.Databases[0].ID != "1501" {
		t.Fatalf("effective databases = %#v, want only the active 1501", scope.Databases)
	}
}

// TestEnvironmentViewEffectiveScopeWithoutSUTs pins the empty shape: a version
// bound to an environment whose SUTs were all deleted has an empty (non-nil)
// effective scope, so consumers can range over it without a nil check.
func TestEnvironmentViewEffectiveScopeWithoutSUTs(t *testing.T) {
	scope := EnvironmentView{ID: "1101", Name: "empty"}.EffectiveScope()
	if scope.Pages == nil || scope.APIs == nil || scope.Databases == nil {
		t.Fatalf("effective scope of an empty environment = %#v, want non-nil empty lists", scope)
	}
	if len(scope.Pages) != 0 || len(scope.APIs) != 0 || len(scope.Databases) != 0 {
		t.Fatalf("effective scope of an empty environment = %#v, want empty", scope)
	}
}
