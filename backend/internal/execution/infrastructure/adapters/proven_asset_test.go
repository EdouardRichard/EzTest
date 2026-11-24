package adapters

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// fakeAssetRepo is a minimal in-memory resource.Repository for the proven-asset
// adapter tests. The embedded nil interface panics on any method the adapter
// path must NOT touch — the tests pin exactly the used surface.
type fakeAssetRepo struct {
	resource.Repository
	suts      []resource.SUT
	pages     []resource.Page
	apis      []resource.API
	databases []resource.TargetDatabase
}

func (f *fakeAssetRepo) GetSUT(_ context.Context, id int64) (resource.SUT, error) {
	for _, sut := range f.suts {
		if sut.ID == id {
			return sut, nil
		}
	}
	return resource.SUT{}, resource.ErrNotFound
}

func (f *fakeAssetRepo) ListPages(_ context.Context, sutID int64) ([]resource.Page, error) {
	out := make([]resource.Page, 0)
	for _, page := range f.pages {
		if page.SUTID == sutID {
			out = append(out, page)
		}
	}
	return out, nil
}

func (f *fakeAssetRepo) CreatePage(_ context.Context, page resource.Page) (resource.Page, error) {
	f.pages = append(f.pages, page)
	return page, nil
}

func (f *fakeAssetRepo) UpdatePage(_ context.Context, page resource.Page) (resource.Page, error) {
	for i, item := range f.pages {
		if item.ID == page.ID {
			f.pages[i] = page
			return page, nil
		}
	}
	return resource.Page{}, resource.ErrNotFound
}

func (f *fakeAssetRepo) ListAPIs(_ context.Context, sutID int64) ([]resource.API, error) {
	out := make([]resource.API, 0)
	for _, api := range f.apis {
		if api.SUTID == sutID {
			out = append(out, api)
		}
	}
	return out, nil
}

func (f *fakeAssetRepo) CreateAPI(_ context.Context, api resource.API) (resource.API, error) {
	f.apis = append(f.apis, api)
	return api, nil
}

func (f *fakeAssetRepo) UpdateAPI(_ context.Context, api resource.API) (resource.API, error) {
	for i, item := range f.apis {
		if item.ID == api.ID {
			f.apis[i] = api
			return api, nil
		}
	}
	return resource.API{}, resource.ErrNotFound
}

func (f *fakeAssetRepo) ListDatabases(_ context.Context, sutID int64) ([]resource.TargetDatabase, error) {
	out := make([]resource.TargetDatabase, 0)
	for _, database := range f.databases {
		if database.SUTID == sutID {
			out = append(out, database)
		}
	}
	return out, nil
}

func (f *fakeAssetRepo) CreateDatabase(_ context.Context, database resource.TargetDatabase) (resource.TargetDatabase, error) {
	f.databases = append(f.databases, database)
	return database, nil
}

type seqAssetIDs struct{ next int64 }

func (s *seqAssetIDs) Next() int64 { s.next++; return s.next }

type fixedAssetClock struct{ at time.Time }

func (c fixedAssetClock) Now() time.Time { return c.at }

var assetAdapterNow = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

func newProvenAssetAdapter(repo *fakeAssetRepo) *ProvenAssetAdapter {
	svc := resourceapp.New(repo, &seqAssetIDs{next: 7000}, fixedAssetClock{at: assetAdapterNow}, nil, nil, nil)
	return NewProvenAssetAdapter(svc)
}

func assetAdapterSUT() resource.SUT {
	return resource.SUT{
		ID: 9001, EnvironmentID: 7001, Name: "订单系统",
		BaseURL: "https://orders.example.com",
		Source:  resource.SUTSourceAdmin, Status: resource.SUTStatusApproved,
	}
}

// TestProvenAssetAdapterPageRegistration: the adapter creates the proven page
// through the resource authority, re-parses the decimal view id back to int64,
// and reports the ACTUAL row source/status (ai/approved).
func TestProvenAssetAdapterPageRegistration(t *testing.T) {
	repo := &fakeAssetRepo{suts: []resource.SUT{assetAdapterSUT()}}
	adapter := newProvenAssetAdapter(repo)
	registered, err := adapter.RegisterProvenAsset(context.Background(), tools.AssetRegisterInput{
		SUTID: 9001, AssetKind: "page", Name: "订单页", URL: "https://orders.example.com/orders",
	}, 3001, assetAdapterNow)
	if err != nil {
		t.Fatalf("RegisterProvenAsset: %v", err)
	}
	if registered.ID != 7001 || registered.Source != "ai" || registered.Status != "approved" {
		t.Fatalf("registered = %+v, want id 7001 ai/approved", registered)
	}
	if len(repo.pages) != 1 || repo.pages[0].ID != registered.ID {
		t.Fatalf("catalog row id must match the parsed id: %+v", repo.pages)
	}
}

// TestProvenAssetAdapterIdempotentHitActualSource: an idempotent hit on an
// admin-created approved page reports the row's ACTUAL source (admin), never a
// hardcoded ai.
func TestProvenAssetAdapterIdempotentHitActualSource(t *testing.T) {
	repo := &fakeAssetRepo{
		suts: []resource.SUT{assetAdapterSUT()},
		pages: []resource.Page{{
			ID: 101, SUTID: 9001, Name: "订单页", URL: "https://orders.example.com/orders",
			RouteTemplate: "/orders", PageType: "page",
			Source: resource.PageSourceAdmin, Status: resource.PageStatusApproved,
		}},
	}
	adapter := newProvenAssetAdapter(repo)
	registered, err := adapter.RegisterProvenAsset(context.Background(), tools.AssetRegisterInput{
		SUTID: 9001, AssetKind: "page", Name: "订单页", URL: "https://orders.example.com/orders",
	}, 3001, assetAdapterNow)
	if err != nil {
		t.Fatalf("RegisterProvenAsset: %v", err)
	}
	if registered.ID != 101 || registered.Source != "admin" || registered.Status != "approved" {
		t.Fatalf("registered = %+v, want the existing admin/approved row 101", registered)
	}
	if len(repo.pages) != 1 {
		t.Fatalf("idempotent hit duplicated the row: %d", len(repo.pages))
	}
}

// TestProvenAssetAdapterRejectedFreshProofPassthrough: the server-derived
// provenAt reaches the resource revival rule — old evidence on a rejected page
// propagates the 409 fresh-proof conflict; fresh evidence approves in place.
func TestProvenAssetAdapterRejectedFreshProofPassthrough(t *testing.T) {
	rejectAt := assetAdapterNow.Add(-30 * time.Minute)
	repo := &fakeAssetRepo{
		suts: []resource.SUT{assetAdapterSUT()},
		pages: []resource.Page{{
			ID: 101, SUTID: 9001, Name: "订单页", URL: "https://orders.example.com/orders",
			RouteTemplate: "/orders", PageType: "page",
			Source: resource.PageSourceAI, Status: resource.PageStatusRejected,
			CreatedAt: rejectAt.Add(-time.Hour), UpdatedAt: rejectAt,
		}},
	}
	adapter := newProvenAssetAdapter(repo)
	in := tools.AssetRegisterInput{SUTID: 9001, AssetKind: "page", Name: "订单页", URL: "https://orders.example.com/orders"}

	_, err := adapter.RegisterProvenAsset(context.Background(), in, 3001, rejectAt.Add(-time.Hour))
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 409 || apiErr.Code != "rejected_requires_fresh_proof" {
		t.Fatalf("old evidence must propagate the fresh-proof conflict, got %v", err)
	}
	if repo.pages[0].Status != resource.PageStatusRejected {
		t.Fatalf("old evidence revived the rejected page: %+v", repo.pages[0])
	}

	registered, err := adapter.RegisterProvenAsset(context.Background(), in, 3001, rejectAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("fresh evidence must revive: %v", err)
	}
	if registered.ID != 101 || registered.Status != "approved" {
		t.Fatalf("fresh proof must approve in place: %+v", registered)
	}
}

// TestProvenAssetAdapterAPIAndDatabase: the api path creates ai/approved; the db
// path reports the ACTUAL lifecycle status (active) with the ai registration
// source, and a same-name different-DSN collision propagates the 409 identity
// conflict.
func TestProvenAssetAdapterAPIAndDatabase(t *testing.T) {
	repo := &fakeAssetRepo{suts: []resource.SUT{assetAdapterSUT()}}
	adapter := newProvenAssetAdapter(repo)
	ctx := context.Background()

	api, err := adapter.RegisterProvenAsset(ctx, tools.AssetRegisterInput{
		SUTID: 9001, AssetKind: "api", Name: "创建订单", Method: "POST", Path: "/api/orders",
	}, 3001, assetAdapterNow)
	if err != nil {
		t.Fatalf("api registration: %v", err)
	}
	if api.Source != "ai" || api.Status != "approved" || api.ID <= 0 {
		t.Fatalf("api registered = %+v, want ai/approved with a parsed id", api)
	}

	db, err := adapter.RegisterProvenAsset(ctx, tools.AssetRegisterInput{
		SUTID: 9001, AssetKind: "db", Name: "order_db",
		DSN: "postgres://db-a.example.com:5432/orders", Username: "reader",
	}, 3001, assetAdapterNow)
	if err != nil {
		t.Fatalf("db registration: %v", err)
	}
	if db.Source != "ai" || db.Status != "active" || db.ID <= 0 {
		t.Fatalf("db registered = %+v, want ai registration source + ACTUAL active status", db)
	}

	_, err = adapter.RegisterProvenAsset(ctx, tools.AssetRegisterInput{
		SUTID: 9001, AssetKind: "db", Name: "order_db",
		DSN: "postgres://db-b.example.com:5432/orders", Username: "reader",
	}, 3001, assetAdapterNow)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 409 || apiErr.Code != "database_identity_conflict" {
		t.Fatalf("same-name different-DSN must propagate the identity conflict, got %v", err)
	}
}

// TestProvenAssetAdapterErrorPropagation: an unknown SUT is a typed 404, an
// unknown kind is invalid_argument, and a nil resource service fails as an
// internal configuration error — none is a silent success.
func TestProvenAssetAdapterErrorPropagation(t *testing.T) {
	repo := &fakeAssetRepo{suts: []resource.SUT{assetAdapterSUT()}}
	adapter := newProvenAssetAdapter(repo)
	ctx := context.Background()

	_, err := adapter.RegisterProvenAsset(ctx, tools.AssetRegisterInput{
		SUTID: 4242, AssetKind: "page", Name: "x", URL: "https://x.example.com",
	}, 3001, assetAdapterNow)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 404 {
		t.Fatalf("unknown sut must be a typed 404, got %v", err)
	}

	_, err = adapter.RegisterProvenAsset(ctx, tools.AssetRegisterInput{
		SUTID: 9001, AssetKind: "widget", Name: "x",
	}, 3001, assetAdapterNow)
	if !errors.As(err, &apiErr) || apiErr.Status != 400 {
		t.Fatalf("unknown kind must be invalid_argument, got %v", err)
	}

	var nilAdapter *ProvenAssetAdapter = NewProvenAssetAdapter(nil)
	if _, err := nilAdapter.RegisterProvenAsset(ctx, tools.AssetRegisterInput{
		SUTID: 9001, AssetKind: "page", Name: "x", URL: "https://x.example.com",
	}, 3001, assetAdapterNow); err == nil {
		t.Fatal("a nil resource service must fail, not silently succeed")
	}
}

// compile-time: the adapter satisfies the execution write port.
var _ executionapp.ProvenAssetWriter = (*ProvenAssetAdapter)(nil)
