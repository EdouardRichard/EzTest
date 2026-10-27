// Package resource adapts the P2 resource application services to the codeintel
// RepoCatalog and AppModelCatalog ports (design spec §3.5). The adapter projects
// each P2 code repository and app model node onto the minimal codeintel read
// model; it never reaches into P2 infrastructure and never writes P2 state
// except through the resource application service.
package resource

import (
	"context"
	"errors"
	"fmt"
	"time"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codedomain "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	resourcedomain "github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// appModelPageSize is the cursor page size used while draining a system's app
// model nodes. P2 serves at most 100 rows per page over HTTP; the application
// repository accepts the same page size here.
const appModelPageSize = 100

// allAppModelKinds is the documented app model node kinds, in the order the
// codeintel prompt and impact pages consume them.
var allAppModelKinds = []string{
	resourcedomain.AppModelKindPage,
	resourcedomain.AppModelKindRoute,
	resourcedomain.AppModelKindAPI,
	resourcedomain.AppModelKindComponent,
}

// CodeRepositoryReader is the narrow slice of the P2 code repository service the
// adapter needs. *resource/application.CodeRepoService satisfies it.
type CodeRepositoryReader interface {
	Get(ctx context.Context, id int64) (*resourcedomain.CodeRepository, error)
}

// AppModelReader is the narrow slice of the P2 catalog service the adapter
// needs. *resource/application.CatalogService satisfies it.
type AppModelReader interface {
	ListAppModels(ctx context.Context, f resourcedomain.AppModelFilter) ([]resourcedomain.AppModelNode, bool, error)
}

// Catalog is the codeintel RepoCatalog/AppModelCatalog adapter over the P2
// application services.
type Catalog struct {
	codeRepos CodeRepositoryReader
	appModels AppModelReader
}

// NewCatalog wires the adapter to the P2 code repository reader and, optionally,
// the P2 app model reader. P2 splits those two reads across CodeRepoService and
// CatalogService, so a caller that needs both passes both; a caller that only
// reads repositories may omit the app model reader and will get a configuration
// error from ListBySystem.
func NewCatalog(codeRepos CodeRepositoryReader, appModels ...AppModelReader) *Catalog {
	catalog := &Catalog{codeRepos: codeRepos}
	if len(appModels) > 0 {
		catalog.appModels = appModels[0]
	}
	return catalog
}

var (
	_ codeintelapp.RepoCatalog     = (*Catalog)(nil)
	_ codeintelapp.AppModelCatalog = (*Catalog)(nil)
)

// Get returns the codeintel projection of one P2 code repository. The status is
// reported as stored so the caller decides whether a disabled repository is
// usable; a missing repository is the spec §7.1 422 code_repository_not_found.
func (c *Catalog) Get(ctx context.Context, repoID int64) (codeintelapp.RepoInfo, error) {
	if c == nil || c.codeRepos == nil {
		return codeintelapp.RepoInfo{}, errors.New("codeintel: resource code repository catalog is not configured")
	}
	repo, err := c.codeRepos.Get(ctx, repoID)
	if err != nil {
		return codeintelapp.RepoInfo{}, mapResourceLookupError("repoId", "code_repository_not_found", err)
	}
	if repo == nil {
		return codeintelapp.RepoInfo{}, codedomain.NewInvalid("repoId", "code_repository_not_found",
			fmt.Sprintf("code repository %d not found", repoID))
	}

	var lastIndexedAt *time.Time
	if repo.LastIndexedAt != nil {
		indexedAt := *repo.LastIndexedAt
		lastIndexedAt = &indexedAt
	}
	return codeintelapp.RepoInfo{
		ID:            repo.ID,
		Key:           repo.Key,
		Name:          repo.Name,
		LocalPath:     repo.LocalPath,
		FixedBranch:   repo.FixedBranch,
		IncludeGlobs:  cloneStrings(repo.IncludeGlobs),
		ExcludeGlobs:  cloneStrings(repo.ExcludeGlobs),
		Status:        repo.Status,
		LastIndexedAt: lastIndexedAt,
	}, nil
}

// ListBySystem returns every app model node of systemID for the requested kinds.
// An empty kinds slice means all four documented kinds. The P2 list is a cursor
// page, so the adapter drains every page instead of returning only the first.
func (c *Catalog) ListBySystem(ctx context.Context, systemID int64, kinds []string) ([]codeintelapp.AppModelNode, error) {
	if c == nil || c.appModels == nil {
		return nil, errors.New("codeintel: resource app model catalog is not configured")
	}
	requested := kinds
	if len(requested) == 0 {
		requested = allAppModelKinds
	}

	nodes := make([]codeintelapp.AppModelNode, 0)
	seen := make(map[int64]struct{})
	for _, kind := range requested {
		if kind == "" {
			continue
		}
		afterID := int64(0)
		for {
			page, more, err := c.appModels.ListAppModels(ctx, resourcedomain.AppModelFilter{
				SystemID: systemID,
				Kind:     kind,
				Page:     resourcedomain.Page{Limit: appModelPageSize, AfterID: afterID},
			})
			if err != nil {
				return nil, err
			}
			for _, node := range page {
				if _, ok := seen[node.ID]; ok {
					continue
				}
				seen[node.ID] = struct{}{}
				nodes = append(nodes, codeintelapp.AppModelNode{
					ID:       node.ID,
					SystemID: node.SystemID,
					Kind:     node.Kind,
					Key:      node.Key,
					Name:     node.Name,
					Path:     node.Path,
					Method:   node.Method,
				})
			}
			if !more {
				break
			}
			if len(page) == 0 {
				return nil, fmt.Errorf("codeintel: list app models: empty page for kind %q with more set", kind)
			}
			next := page[len(page)-1].ID
			if next <= afterID {
				return nil, fmt.Errorf("codeintel: list app models: cursor did not advance past %d", afterID)
			}
			afterID = next
		}
	}
	return nodes, nil
}

// mapResourceLookupError translates a P2 not-found into the codeintel
// validation error carrying the spec §7.1 reason. Operational failures are
// returned unchanged so they surface as internal errors instead of a 422.
func mapResourceLookupError(field, reason string, err error) error {
	var domainErr *resourcedomain.Error
	if errors.As(err, &domainErr) && domainErr.Kind == resourcedomain.KindNotFound {
		return codedomain.NewInvalid(field, reason, domainErr.Error())
	}
	return err
}

// cloneStrings copies a P2 string slice so a codeintel projection never aliases
// stored state.
func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}
