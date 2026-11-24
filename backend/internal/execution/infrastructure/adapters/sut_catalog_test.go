package adapters

import (
	"context"
	"testing"

	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// resolutionCatalogFixture is an environment with three SUTs: an approved one
// (carrying both approved and proposed assets), a proposed one and a rejected
// one. The effective-scope projection must admit only the approved SUT and only
// its approved assets (R-B4 trust boundary).
func resolutionCatalogFixture() versionapp.VersionView {
	approved := resourceapp.SUTView{
		ID: "3", EnvironmentID: "4", Name: "订单系统", Intro: "订单管理", BaseURL: "https://orders.example.com",
		Source: "admin", Status: "approved",
		Pages: []resourceapp.PageView{
			{ID: "71", SUTID: "3", Name: "下单页", RouteTemplate: "/checkout", Status: "approved"},
			{ID: "74", SUTID: "3", Name: "候选页", RouteTemplate: "/proposed", Status: "proposed"},
		},
		APIs: []resourceapp.APIView{
			{ID: "72", SUTID: "3", Method: "POST", PathTemplate: "/api/orders", Status: "approved"},
			{ID: "75", SUTID: "3", Method: "GET", PathTemplate: "/api/proposed", Status: "proposed"},
		},
	}
	proposed := resourceapp.SUTView{
		ID: "5", EnvironmentID: "4", Name: "候选系统", BaseURL: "https://proposed.example.com",
		Source: "ai", Status: "proposed",
		Pages: []resourceapp.PageView{{ID: "91", SUTID: "5", Name: "p", RouteTemplate: "/p", Status: "approved"}},
	}
	rejected := resourceapp.SUTView{
		ID: "6", EnvironmentID: "4", Name: "被驳系统", BaseURL: "https://rejected.example.com",
		Source: "ai", Status: "rejected",
		Pages: []resourceapp.PageView{{ID: "92", SUTID: "6", Name: "p", RouteTemplate: "/p", Status: "approved"}},
	}
	environment := resourceapp.EnvironmentView{ID: "4", Name: "test", Status: "active",
		SUTs: []resourceapp.SUTView{approved, proposed, rejected}}
	return versionapp.VersionView{
		ID: 10, Status: "running", EnvironmentID: 4,
		Binding: versionapp.EnvironmentSUTCatalog{
			VersionID: 10, EnvironmentID: 4, Environment: environment,
		},
	}
}

// TestSUTCatalogExcludesNonApprovedSUTs pins the R-B4 SUT-level gate: a proposed
// or rejected SUT never enters the resolution range, even though its own assets
// are approved. Only the approved SUT is returned.
func TestSUTCatalogExcludesNonApprovedSUTs(t *testing.T) {
	adapter := NewSUTCatalogAdapter(&fakeVersionService{view: resolutionCatalogFixture()})
	suts, err := adapter.EffectiveSUTCatalog(context.Background(), 10)
	if err != nil {
		t.Fatalf("EffectiveSUTCatalog: %v", err)
	}
	if len(suts) != 1 {
		t.Fatalf("resolved catalog = %d SUTs, want only the approved one (proposed/rejected excluded)", len(suts))
	}
	got := suts[0]
	if got.SUTID != 3 {
		t.Fatalf("resolved SUT id = %d, want the approved 3", got.SUTID)
	}
	if got.Name != "订单系统" || got.Intro != "订单管理" || got.BaseURL != "https://orders.example.com" {
		t.Fatalf("identity/labels = %+v", got)
	}
	if got.Source != "admin" || got.Status != "approved" {
		t.Fatalf("source/status = %q/%q, want admin/approved", got.Source, got.Status)
	}
	if got.EnvironmentID != 4 {
		t.Fatalf("environment id = %d, want 4", got.EnvironmentID)
	}
}

// TestSUTCatalogExcludesUnapprovedAssets pins the asset-level gate: a proposed
// page/API of an approved SUT is not in the effective catalog (so it can never be
// recorded as a touched/verified asset).
func TestSUTCatalogExcludesUnapprovedAssets(t *testing.T) {
	adapter := NewSUTCatalogAdapter(&fakeVersionService{view: resolutionCatalogFixture()})
	suts, err := adapter.EffectiveSUTCatalog(context.Background(), 10)
	if err != nil {
		t.Fatalf("EffectiveSUTCatalog: %v", err)
	}
	if len(suts) != 1 {
		t.Fatalf("suts = %d, want 1", len(suts))
	}
	got := suts[0]
	if len(got.PageIDs) != 1 || got.PageIDs[0] != 71 {
		t.Fatalf("page ids = %v, want only the approved 71", got.PageIDs)
	}
	if len(got.APIIDs) != 1 || got.APIIDs[0] != 72 {
		t.Fatalf("api ids = %v, want only the approved 72", got.APIIDs)
	}
}
