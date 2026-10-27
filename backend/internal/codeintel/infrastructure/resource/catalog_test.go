package resource_test

import (
	"context"
	"errors"
	"testing"
	"time"

	codedomain "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	codeintelresource "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/resource"
	resourcedomain "github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// fakeCodeRepoReader is an in-memory P2 code repository reader.
type fakeCodeRepoReader struct {
	repo  resourcedomain.CodeRepository
	err   error
	calls []int64
}

// Get returns the configured repository or failure.
func (f *fakeCodeRepoReader) Get(_ context.Context, id int64) (*resourcedomain.CodeRepository, error) {
	f.calls = append(f.calls, id)
	if f.err != nil {
		return nil, f.err
	}
	repo := f.repo
	return &repo, nil
}

// fakeAppModelReader is an in-memory P2 app model lister honouring the
// system/kind/after-id filters and the cursor page contract.
type fakeAppModelReader struct {
	nodes   []resourcedomain.AppModelNode
	err     error
	filters []resourcedomain.AppModelFilter
}

// ListAppModels mimics the P2 repository: filter by system and optional kind,
// order by ID ascending and return at most Limit rows plus a has-more flag.
func (f *fakeAppModelReader) ListAppModels(_ context.Context, filter resourcedomain.AppModelFilter) ([]resourcedomain.AppModelNode, bool, error) {
	f.filters = append(f.filters, filter)
	if f.err != nil {
		return nil, false, f.err
	}
	matches := make([]resourcedomain.AppModelNode, 0, len(f.nodes))
	for _, node := range f.nodes {
		if node.SystemID != filter.SystemID {
			continue
		}
		if filter.Kind != "" && node.Kind != filter.Kind {
			continue
		}
		if node.ID <= filter.Page.AfterID {
			continue
		}
		matches = append(matches, node)
	}
	limit := filter.Page.Limit
	if limit <= 0 {
		limit = 20
	}
	more := len(matches) > limit
	if more {
		matches = matches[:limit]
	}
	return matches, more, nil
}

// TestCatalogMapsRepoInfo pins the RepoCatalog projection: every codeintel field
// is copied from P2, a disabled repository is still readable (the caller decides
// whether it is usable), and the glob slices do not alias stored state.
func TestCatalogMapsRepoInfo(t *testing.T) {
	indexedAt := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	repo := resourcedomain.CodeRepository{
		ID:            42,
		Key:           "web",
		Name:          "Web",
		LocalPath:     "/srv/web",
		FixedBranch:   "main",
		IncludeGlobs:  []string{"src/**"},
		ExcludeGlobs:  []string{"dist/**"},
		Status:        resourcedomain.StatusDisabled,
		LastCommit:    "0123456789abcdef0123456789abcdef01234567",
		LastIndexedAt: &indexedAt,
	}
	reader := &fakeCodeRepoReader{repo: repo}
	catalog := codeintelresource.NewCatalog(reader)

	info, err := catalog.Get(context.Background(), 42)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if info.ID != 42 || info.Key != "web" || info.Name != "Web" {
		t.Fatalf("info=%+v", info)
	}
	if info.LocalPath != "/srv/web" || info.FixedBranch != "main" {
		t.Fatalf("path/branch=%q/%q", info.LocalPath, info.FixedBranch)
	}
	if len(info.IncludeGlobs) != 1 || info.IncludeGlobs[0] != "src/**" {
		t.Fatalf("includeGlobs=%v", info.IncludeGlobs)
	}
	if len(info.ExcludeGlobs) != 1 || info.ExcludeGlobs[0] != "dist/**" {
		t.Fatalf("excludeGlobs=%v", info.ExcludeGlobs)
	}
	if info.Status != resourcedomain.StatusDisabled {
		t.Fatalf("status=%q want disabled", info.Status)
	}
	if info.LastIndexedAt == nil || !info.LastIndexedAt.Equal(indexedAt) {
		t.Fatalf("lastIndexedAt=%v want %v", info.LastIndexedAt, indexedAt)
	}
	if len(reader.calls) != 1 || reader.calls[0] != 42 {
		t.Fatalf("reader calls=%v want [42]", reader.calls)
	}

	// The projection must own its slices and timestamp: changing the stored row
	// afterwards cannot change a returned RepoInfo.
	repo.IncludeGlobs[0] = "mutated"
	if info.IncludeGlobs[0] != "src/**" {
		t.Fatalf("includeGlobs aliases stored state: %v", info.IncludeGlobs)
	}
}

// TestCatalogMapsMissingRepoToValidationError pins the spec §7.1 reason: a
// missing P2 repository is a 422 code_repository_not_found, not a 404.
func TestCatalogMapsMissingRepoToValidationError(t *testing.T) {
	reader := &fakeCodeRepoReader{err: resourcedomain.NewNotFound("code repository", 7)}
	catalog := codeintelresource.NewCatalog(reader)

	_, err := catalog.Get(context.Background(), 7)
	if err == nil {
		t.Fatal("Get(missing)=nil want a domain error")
	}
	var domainErr *codedomain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("error %v (%T) is not a *codedomain.Error", err, err)
	}
	if !errors.Is(err, codedomain.ErrInvalid) {
		t.Fatalf("kind=%v want ErrInvalid", domainErr.Kind)
	}
	if domainErr.Reason != "code_repository_not_found" {
		t.Fatalf("reason=%q want code_repository_not_found", domainErr.Reason)
	}
}

// TestCatalogListsAppModelNodesBySystemAndKind pins the AppModelCatalog
// projection and the kind filter. An empty kind list means all documented kind.
func TestCatalogListsAppModelNodesBySystemAndKind(t *testing.T) {
	nodes := []resourcedomain.AppModelNode{
		{ID: 1, SystemID: 1, Kind: "page", Key: "page:/orders", Name: "Orders", Path: "/orders"},
		{ID: 2, SystemID: 1, Kind: "api", Key: "api:GET /orders", Name: "List orders", Path: "/orders", Method: "GET"},
		{ID: 3, SystemID: 1, Kind: "route", Key: "route:/orders", Name: "Orders route", Path: "/orders"},
		{ID: 4, SystemID: 1, Kind: "component", Key: "component:OrdersPage", Name: "OrdersPage", Path: "src/orders.tsx"},
		{ID: 5, SystemID: 2, Kind: "page", Key: "page:/billing", Name: "Billing", Path: "/billing"},
	}
	appModels := &fakeAppModelReader{nodes: nodes}
	catalog := codeintelresource.NewCatalog(nil, appModels)

	got, err := catalog.ListBySystem(context.Background(), 1, []string{"page", "api"})
	if err != nil {
		t.Fatalf("ListBySystem: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("nodes=%+v want 2 page/api nodes", got)
	}
	if got[0].ID != 1 || got[0].Kind != "page" || got[0].Key != "page:/orders" ||
		got[0].SystemID != 1 || got[0].Name != "Orders" || got[0].Path != "/orders" {
		t.Fatalf("page node=%+v", got[0])
	}
	if got[1].ID != 2 || got[1].Kind != "api" || got[1].Method != "GET" || got[1].Path != "/orders" {
		t.Fatalf("api node=%+v", got[1])
	}
	for _, filter := range appModels.filters {
		if filter.SystemID != 1 {
			t.Fatalf("filter systemID=%d want 1", filter.SystemID)
		}
	}

	// An empty kinds list means every documented app model kind.
	got, err = catalog.ListBySystem(context.Background(), 1, nil)
	if err != nil {
		t.Fatalf("ListBySystem(all): %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("nodes=%+v want all 4 nodes of system 1", got)
	}
}

// TestCatalogPagesThroughAppModelNodes proves the adapter follows the P2 cursor
// page instead of silently truncating at the first page.
func TestCatalogPagesThroughAppModelNodes(t *testing.T) {
	nodes := make([]resourcedomain.AppModelNode, 0, 101)
	for i := 1; i <= 101; i++ {
		nodes = append(nodes, resourcedomain.AppModelNode{
			ID:       int64(i),
			SystemID: 1,
			Kind:     "page",
			Key:      "page:/p",
			Name:     "P",
		})
	}
	appModels := &fakeAppModelReader{nodes: nodes}
	catalog := codeintelresource.NewCatalog(nil, appModels)

	got, err := catalog.ListBySystem(context.Background(), 1, []string{"page"})
	if err != nil {
		t.Fatalf("ListBySystem: %v", err)
	}
	if len(got) != 101 {
		t.Fatalf("nodes=%d want 101", len(got))
	}
	if len(appModels.filters) != 2 {
		t.Fatalf("page requests=%d want 2", len(appModels.filters))
	}
	if appModels.filters[1].Page.AfterID != 100 {
		t.Fatalf("second page afterID=%d want 100", appModels.filters[1].Page.AfterID)
	}
}

// TestIndexStateWriterDelegates pins the writer adapter: it forwards every
// argument to resource application's MarkIndexed and returns its error.
func TestIndexStateWriterDelegates(t *testing.T) {
	at := time.Date(2026, 10, 27, 11, 0, 0, 0, time.UTC)
	marker := &fakeIndexMarker{}
	writer := codeintelresource.NewIndexStateWriter(marker)

	if err := writer.MarkIndexed(context.Background(), 9, "0123456789abcdef0123456789abcdef01234567", at); err != nil {
		t.Fatalf("MarkIndexed: %v", err)
	}
	if marker.repoID != 9 || marker.commit != "0123456789abcdef0123456789abcdef01234567" || !marker.at.Equal(at) {
		t.Fatalf("marker=%+v", marker)
	}
	if marker.calls != 1 {
		t.Fatalf("calls=%d want 1", marker.calls)
	}

	marker.err = errors.New("resource database down")
	if err := writer.MarkIndexed(context.Background(), 9, "0123456789abcdef0123456789abcdef01234567", at); err == nil {
		t.Fatal("MarkIndexed error=nil want the writer failure")
	}
}

// fakeIndexMarker records the MarkIndexed call it receives.
type fakeIndexMarker struct {
	repoID int64
	commit string
	at     time.Time
	calls  int
	err    error
}

// MarkIndexed implements the resource application MarkIndexed shape.
func (f *fakeIndexMarker) MarkIndexed(_ context.Context, repoID int64, commit string, at time.Time) error {
	f.repoID = repoID
	f.commit = commit
	f.at = at
	f.calls++
	return f.err
}
