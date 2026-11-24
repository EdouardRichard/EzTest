package versionadapter_test

import (
	"context"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	versionadapter "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/versionadapter"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// sutReadFixture is an environment with an approved SUT (carrying approved and
// proposed assets), a proposed SUT and a rejected SUT, for the sut.read /
// sut.list effective-scope gate (R-B4).
func sutReadFixture() versionapp.VersionView {
	approved := resourceapp.SUTView{
		ID: "1001", EnvironmentID: "1101", Name: "订单中心", Intro: "订单管理后台",
		BaseURL: "https://orders.example.com", Source: "admin", Status: "approved",
		CreatedAt: testTime, UpdatedAt: testTime,
		Pages: []resourceapp.PageView{
			{ID: "1301", SUTID: "1001", Name: "订单列表", RouteTemplate: "/orders", Status: "approved", CreatedAt: testTime, UpdatedAt: testTime},
			{ID: "1304", SUTID: "1001", Name: "登录页", RouteTemplate: "/login", Status: "proposed", CreatedAt: testTime, UpdatedAt: testTime},
		},
		APIs: []resourceapp.APIView{
			{ID: "1401", SUTID: "1001", Method: "GET", PathTemplate: "/api/orders", Name: "查询订单", Status: "approved", CreatedAt: testTime, UpdatedAt: testTime},
		},
		Databases: []resourceapp.DatabaseView{
			{ID: "1501", SUTID: "1001", Name: "order_db", ReadOnly: true, Status: "active", CreatedAt: testTime, UpdatedAt: testTime},
		},
	}
	proposed := resourceapp.SUTView{
		ID: "1002", EnvironmentID: "1101", Name: "候选系统", BaseURL: "https://proposed.example.com",
		Source: "ai", Status: "proposed", CreatedAt: testTime, UpdatedAt: testTime,
		Pages: []resourceapp.PageView{{ID: "1399", SUTID: "1002", Name: "p", RouteTemplate: "/p", Status: "approved", CreatedAt: testTime, UpdatedAt: testTime}},
	}
	rejected := resourceapp.SUTView{
		ID: "1003", EnvironmentID: "1101", Name: "被驳系统", BaseURL: "https://rejected.example.com",
		Source: "ai", Status: "rejected", CreatedAt: testTime, UpdatedAt: testTime,
	}
	return versionapp.VersionView{
		ID: 2001, Status: "running", EnvironmentID: 1101,
		Binding: versionapp.EnvironmentSUTCatalog{
			VersionID: 2001, EnvironmentID: 1101,
			Environment: resourceapp.EnvironmentView{
				ID: "1101", Name: "local", Status: "active", CreatedAt: testTime, UpdatedAt: testTime,
				SUTs: []resourceapp.SUTView{approved, proposed, rejected},
			},
		},
		CreatedAt: testTime, UpdatedAt: testTime,
	}
}

func sutReadAdapter(view versionapp.VersionView) *versionadapter.Adapter {
	versions := &fakeVersions{getFn: func(context.Context, int64) (versionapp.VersionView, error) {
		return view, nil
	}}
	requirements := &fakeRequirements{
		listFn: func(context.Context, int64, string, int, *int64) ([]versionapp.RequirementView, bool, error) {
			return []versionapp.RequirementView{}, false, nil
		},
		getFn: func(context.Context, int64, int64) (versionapp.RequirementDetail, error) {
			return versionapp.RequirementDetail{}, nil
		},
	}
	return versionadapter.New(requirements, nil, versions, nil, nil)
}

// TestListSUTsEnriched pins the sut.list enrichment (SC-F2): base_url/source/
// status are emitted, and only effective (approved) SUTs appear.
func TestListSUTsEnriched(t *testing.T) {
	adapter := sutReadAdapter(sutReadFixture())
	suts, err := adapter.ListSUTs(context.Background(), 2001)
	if err != nil {
		t.Fatalf("ListSUTs: %v", err)
	}
	if len(suts) != 1 {
		t.Fatalf("ListSUTs = %d SUTs, want only the approved one (proposed/rejected excluded)", len(suts))
	}
	got := suts[0]
	if got.SUTID != 1001 || got.BaseURL != "https://orders.example.com" {
		t.Fatalf("sut summary = %+v", got)
	}
	if got.Source != "admin" || got.Status != "approved" {
		t.Fatalf("source/status = %q/%q, want admin/approved", got.Source, got.Status)
	}
}

// TestReadSUTDetail pins the sut.read detail surface: identity/labels plus the
// effective asset summary (the proposed page is excluded).
func TestReadSUTDetail(t *testing.T) {
	adapter := sutReadAdapter(sutReadFixture())
	detail, err := adapter.ReadSUT(context.Background(), 2001, 1001)
	if err != nil {
		t.Fatalf("ReadSUT: %v", err)
	}
	if detail.SUTID != 1001 || detail.Name != "订单中心" || detail.Intro != "订单管理后台" {
		t.Fatalf("detail identity = %+v", detail)
	}
	if detail.BaseURL != "https://orders.example.com" || detail.Source != "admin" || detail.Status != "approved" {
		t.Fatalf("detail base_url/source/status = %+v", detail)
	}
	if detail.EnvironmentID != 1101 || detail.Environment != "local" {
		t.Fatalf("detail environment = %+v", detail)
	}
	if len(detail.Pages) != 1 || detail.Pages[0].ID != "1301" {
		t.Fatalf("detail pages = %#v, want only the approved page 1301", detail.Pages)
	}
	if len(detail.APIs) != 1 || detail.APIs[0].ID != "1401" {
		t.Fatalf("detail apis = %#v, want the approved API 1401", detail.APIs)
	}
	if len(detail.Databases) != 1 || detail.Databases[0].ID != "1501" {
		t.Fatalf("detail databases = %#v, want the active database 1501", detail.Databases)
	}
}

// TestReadSUTExcludesNonApprovedSUTs pins the R-B4 SUT-level gate: a proposed or
// rejected SUT id is a 404 exactly like an id of another version.
func TestReadSUTExcludesNonApprovedSUTs(t *testing.T) {
	adapter := sutReadAdapter(sutReadFixture())
	for _, sutID := range []int64{1002, 1003, 9999} {
		_, err := adapter.ReadSUT(context.Background(), 2001, sutID)
		if err == nil {
			t.Fatalf("ReadSUT(%d) resolved a non-effective SUT", sutID)
		}
		apiErr, ok := err.(*platform.Error)
		if !ok || apiErr.Status != 404 {
			t.Fatalf("ReadSUT(%d) error = %v, want 404", sutID, err)
		}
	}
}

// TestReadSUTRejectsInvalidIDs: non-positive ids are field errors, never a read.
func TestReadSUTRejectsInvalidIDs(t *testing.T) {
	adapter := sutReadAdapter(sutReadFixture())
	if _, err := adapter.ReadSUT(context.Background(), 2001, 0); err == nil {
		t.Fatal("ReadSUT(0) must reject a non-positive sut_id")
	}
	// The compile-time assertion that the adapter satisfies the tool surface.
	var _ tools.SUTReader = adapter
}
