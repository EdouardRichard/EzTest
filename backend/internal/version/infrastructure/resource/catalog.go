// Package resource adapts the P2 resource catalog and code-repository services
// to the P3 ResourceCatalog port (design spec §3.5/§6.8). The adapter projects
// each P2 resource onto the minimal P3 read model and translates a missing
// resource into a typed *application.ResourceError carrying the stable
// details.reason the HTTP layer and version service map to a 422.
package resource

import (
	"context"
	"errors"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// resourceStatusActive is the P2 lifecycle status of a usable resource.
const resourceStatusActive = "active"

// SystemReader is the narrow slice of the P2 catalog service the adapter needs.
// *resource/application.CatalogService satisfies it.
type SystemReader interface {
	GetSystem(ctx context.Context, id int64) (*domain.System, error)
}

// EnvironmentReader is the narrow slice of the P2 catalog service the adapter
// needs.
type EnvironmentReader interface {
	GetEnvironment(ctx context.Context, id int64) (*domain.Environment, error)
}

// CodeRepositoryReader is the narrow slice of the P2 code-repository service the
// adapter needs. *resource/application.CodeRepoService satisfies it.
type CodeRepositoryReader interface {
	Get(ctx context.Context, id int64) (*domain.CodeRepository, error)
}

// Catalog is the P3 ResourceCatalog adapter over the P2 services.
type Catalog struct {
	systems      SystemReader
	environments EnvironmentReader
	codeRepos    CodeRepositoryReader
}

// NewCatalog wires the adapter to the P2 catalog and code-repository readers.
// A nil reader only fails the lookups that need it; production injects the
// composed P2 services (T16).
func NewCatalog(systems SystemReader, environments EnvironmentReader, codeRepos CodeRepositoryReader) *Catalog {
	return &Catalog{systems: systems, environments: environments, codeRepos: codeRepos}
}

var _ application.ResourceCatalog = (*Catalog)(nil)

// GetSystem returns the P3 projection of one P2 system. A missing system is a
// ResourceError system_not_found; a disabled system is returned with its status
// so the binding validation decides whether it is usable.
func (c *Catalog) GetSystem(ctx context.Context, id int64) (application.ResourceRef, error) {
	if c.systems == nil {
		return application.ResourceRef{}, errors.New("version: resource catalog is not configured")
	}
	system, err := c.systems.GetSystem(ctx, id)
	if err != nil {
		return application.ResourceRef{}, mapLookupError("system_not_found", err)
	}
	return application.ResourceRef{
		ID:        system.ID,
		Name:      system.Name,
		Status:    system.Status,
		Available: true,
	}, nil
}

// GetEnvironment returns the P3 projection of one P2 environment. SystemID is
// always populated: the version binding validation is fail-closed and rejects
// an environment whose owning system cannot be verified.
func (c *Catalog) GetEnvironment(ctx context.Context, id int64) (application.ResourceRef, error) {
	if c.environments == nil {
		return application.ResourceRef{}, errors.New("version: resource catalog is not configured")
	}
	environment, err := c.environments.GetEnvironment(ctx, id)
	if err != nil {
		return application.ResourceRef{}, mapLookupError("environment_not_found", err)
	}
	systemID := environment.SystemID
	return application.ResourceRef{
		ID:   environment.ID,
		Name: environment.Name,
		// P2 environments have no lifecycle status column; a row that exists is
		// usable, which is what the version binding check expects.
		Status:    resourceStatusActive,
		SystemID:  &systemID,
		Available: true,
	}, nil
}

// GetCodeRepository returns the P3 projection of one P2 code repository.
func (c *Catalog) GetCodeRepository(ctx context.Context, id int64) (application.CodeRepoRef, error) {
	if c.codeRepos == nil {
		return application.CodeRepoRef{}, errors.New("version: resource catalog is not configured")
	}
	repository, err := c.codeRepos.Get(ctx, id)
	if err != nil {
		return application.CodeRepoRef{}, mapLookupError("code_repository_not_found", err)
	}
	return application.CodeRepoRef{
		ID:           repository.ID,
		Name:         repository.Name,
		Status:       repository.Status,
		LocalPath:    repository.LocalPath,
		FixedBranch:  repository.FixedBranch,
		IncludeGlobs: cloneStrings(repository.IncludeGlobs),
		ExcludeGlobs: cloneStrings(repository.ExcludeGlobs),
		LastCommit:   repository.LastCommit,
		Available:    true,
	}, nil
}

// mapLookupError translates a P2 not-found into the P3 typed lookup error while
// leaving operational failures untouched so they surface as 500s.
func mapLookupError(reason string, err error) error {
	var domainErr *domain.Error
	if errors.As(err, &domainErr) && domainErr.Kind == domain.KindNotFound {
		return &application.ResourceError{Reason: reason, Message: domainErr.Error()}
	}
	return err
}

// cloneStrings copies a P2 glob slice so the P3 projection never aliases
// stored state.
func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}
