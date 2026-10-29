package resource_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	mindmapresource "github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/resource"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	resourcedomain "github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// fakeEnvironmentReader returns one configured environment.
type fakeEnvironmentReader struct {
	environment *resourcedomain.Environment
	err         error
	id          int64
}

// GetEnvironment implements the P2 environment read.
func (f *fakeEnvironmentReader) GetEnvironment(_ context.Context, id int64) (*resourcedomain.Environment, error) {
	f.id = id
	if f.err != nil {
		return nil, f.err
	}
	return f.environment, nil
}

// fakeAppModelCatalog serves app model pages and records the import payload.
type fakeAppModelCatalog struct {
	pages        [][]resourcedomain.AppModelNode
	more         []bool
	calls        int
	filters      []resourcedomain.AppModelFilter
	importSystem int64
	importInputs []resourceapp.AppModelInput
	importErr    error
	created      int
	updated      int
}

// ListAppModels implements the P2 app model page read.
func (f *fakeAppModelCatalog) ListAppModels(_ context.Context, filter resourcedomain.AppModelFilter) ([]resourcedomain.AppModelNode, bool, error) {
	f.filters = append(f.filters, filter)
	index := f.calls
	f.calls++
	if index >= len(f.pages) {
		return nil, false, nil
	}
	return f.pages[index], f.more[index], nil
}

// ImportAppModels records the caller payload and returns the configured counts.
func (f *fakeAppModelCatalog) ImportAppModels(_ context.Context, systemID int64, inputs []resourceapp.AppModelInput) (int, int, error) {
	f.importSystem = systemID
	f.importInputs = inputs
	return f.created, f.updated, f.importErr
}

// TestCatalogAdapterMapsEnvironment pins the environment projection, including
// the allowed-host allowlist.
func TestCatalogAdapterMapsEnvironment(t *testing.T) {
	ctx := context.Background()
	environments := &fakeEnvironmentReader{environment: &resourcedomain.Environment{
		ID:           21,
		SystemID:     11,
		Name:         "staging",
		BaseURL:      "https://staging.example.com",
		Kind:         resourcedomain.EnvironmentKindStaging,
		AllowedHosts: []string{"staging.example.com", "api.staging.example.com"},
	}}
	adapter := mindmapresource.NewCatalogAdapter(environments, &fakeAppModelCatalog{})
	var _ mindmapapp.CatalogGateway = adapter

	got, err := adapter.GetEnvironment(ctx, 21)
	if err != nil {
		t.Fatalf("GetEnvironment: %v", err)
	}
	want := mindmapapp.EnvironmentSnapshot{
		ID:           21,
		SystemID:     11,
		Name:         "staging",
		BaseURL:      "https://staging.example.com",
		Kind:         resourcedomain.EnvironmentKindStaging,
		AllowedHosts: []string{"staging.example.com", "api.staging.example.com"},
	}
	if got.ID != want.ID || got.SystemID != want.SystemID || got.Name != want.Name ||
		got.BaseURL != want.BaseURL || got.Kind != want.Kind {
		t.Fatalf("environment=%+v want %+v", got, want)
	}
	if len(got.AllowedHosts) != len(want.AllowedHosts) {
		t.Fatalf("AllowedHosts=%v want %v", got.AllowedHosts, want.AllowedHosts)
	}
	for i, host := range want.AllowedHosts {
		if got.AllowedHosts[i] != host {
			t.Fatalf("AllowedHosts=%v want %v", got.AllowedHosts, want.AllowedHosts)
		}
	}
	if environments.id != 21 {
		t.Fatalf("environment reader received id %d want 21", environments.id)
	}
}

// TestCatalogAdapterListAppModelsDrainsPages pins the app model projection and
// the cursor pagination loop.
func TestCatalogAdapterListAppModelsDrainsPages(t *testing.T) {
	ctx := context.Background()
	parentID := int64(31)
	catalog := &fakeAppModelCatalog{
		pages: [][]resourcedomain.AppModelNode{
			{{
				ID:       31,
				SystemID: 11,
				Kind:     resourcedomain.AppModelKindPage,
				Key:      "page:/login",
				Name:     "Login",
				Path:     "/login",
				Source:   resourcedomain.AppModelSourceExplored,
			}},
			{{
				ID:       32,
				SystemID: 11,
				ParentID: &parentID,
				Kind:     resourcedomain.AppModelKindRoute,
				Key:      "route:/home",
				Name:     "Home",
				Path:     "/home",
				Method:   "GET",
				Source:   resourcedomain.AppModelSourceExplored,
			}},
		},
		more: []bool{true, false},
	}
	adapter := mindmapresource.NewCatalogAdapter(&fakeEnvironmentReader{}, catalog)

	got, err := adapter.ListAppModels(ctx, 11)
	if err != nil {
		t.Fatalf("ListAppModels: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("mapped %d app models, want 2", len(got))
	}
	if got[0].ID != 31 || got[0].Key != "page:/login" || got[0].Kind != resourcedomain.AppModelKindPage ||
		got[0].Name != "Login" || got[0].Path != "/login" || got[0].Source != resourcedomain.AppModelSourceExplored {
		t.Fatalf("first app model=%+v", got[0])
	}
	if got[1].ParentID == nil || *got[1].ParentID != parentID || got[1].Method != "GET" {
		t.Fatalf("second app model=%+v want parent 31 and method GET", got[1])
	}
	if len(catalog.filters) != 2 {
		t.Fatalf("filters=%+v want two pages", catalog.filters)
	}
	if catalog.filters[0].SystemID != 11 || catalog.filters[1].Page.AfterID != 31 {
		t.Fatalf("filters=%+v want system 11 and after id 31", catalog.filters)
	}
}

// TestCatalogAdapterImportAppModelsForcesExploredSource pins the hardcoded
// P5 source: whatever the caller supplies, the imported node is 'explored'.
func TestCatalogAdapterImportAppModelsForcesExploredSource(t *testing.T) {
	ctx := context.Background()
	catalog := &fakeAppModelCatalog{created: 1, updated: 2}
	adapter := mindmapresource.NewCatalogAdapter(&fakeEnvironmentReader{}, catalog)

	meta := json.RawMessage(`{"depth":2}`)
	created, updated, err := adapter.ImportAppModels(ctx, 11, []mindmapapp.AppModelInput{
		{Key: "page:/login", Kind: resourcedomain.AppModelKindPage, Name: "Login", Path: "/login", ParentKey: "", Meta: meta, Source: resourcedomain.AppModelSourceManual},
		{Key: "route:/home", Kind: resourcedomain.AppModelKindRoute, Name: "Home", Path: "/home", ParentKey: "page:/login"},
	})
	if err != nil {
		t.Fatalf("ImportAppModels: %v", err)
	}
	if created != 1 || updated != 2 {
		t.Fatalf("created/updated=%d/%d want 1/2", created, updated)
	}
	if catalog.importSystem != 11 {
		t.Fatalf("import system=%d want 11", catalog.importSystem)
	}
	if len(catalog.importInputs) != 2 {
		t.Fatalf("imported %d inputs, want 2", len(catalog.importInputs))
	}
	for i, input := range catalog.importInputs {
		if input.Source != resourcedomain.AppModelSourceExplored {
			t.Fatalf("input %d source=%q want explored", i, input.Source)
		}
	}
	if catalog.importInputs[0].ParentKey != "" || catalog.importInputs[1].ParentKey != "page:/login" {
		t.Fatalf("parent keys=%q/%q want empty/page:/login", catalog.importInputs[0].ParentKey, catalog.importInputs[1].ParentKey)
	}
	if string(catalog.importInputs[0].Meta) != string(meta) {
		t.Fatalf("meta=%s want %s", catalog.importInputs[0].Meta, meta)
	}
}

// TestCatalogAdapterMapsResourceErrors pins the resource -> mindmap not_found
// translation with the mindmap-style reason.
func TestCatalogAdapterMapsResourceErrors(t *testing.T) {
	ctx := context.Background()
	adapter := mindmapresource.NewCatalogAdapter(
		&fakeEnvironmentReader{err: resourcedomain.NewNotFound("environment", 21)},
		&fakeAppModelCatalog{importErr: resourcedomain.NewNotFound("system", 11)},
	)

	_, err := adapter.GetEnvironment(ctx, 21)
	var mapped *mindmapdomain.Error
	if !errors.As(err, &mapped) || mapped.Kind != mindmapdomain.KindNotFound || mapped.Reason != "environment_not_found" {
		t.Fatalf("GetEnvironment error=%v want environment_not_found", err)
	}

	_, _, err = adapter.ImportAppModels(ctx, 11, nil)
	if !errors.As(err, &mapped) || mapped.Kind != mindmapdomain.KindNotFound || mapped.Reason != "system_not_found" {
		t.Fatalf("ImportAppModels error=%v want system_not_found", err)
	}
}
