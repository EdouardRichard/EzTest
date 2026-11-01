// Package workspaceproviders adapts the owning contexts' read-only query
// services to the workspace todo aggregation (spec §4.3.2/§7). Providers only
// call QueryService methods; they never open a database connection or touch
// another context's tables.
package workspaceproviders

import (
	"context"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	workspaceapp "github.com/EdouardRichard/EzTest/backend/internal/workspace/application"
)

// VersionQueryService is the version context's P6 read-only surface needed by
// the todo feed: draft requirements and versions with pending design work.
type VersionQueryService interface {
	ListDraftRequirements(ctx context.Context, limit int) ([]versionapp.TodoHit, error)
	ListPendingDesignVersions(ctx context.Context) ([]versionapp.TodoHit, error)
}

// VersionProvider supplies requirement_confirmation and design_pending todos.
type VersionProvider struct {
	query VersionQueryService
}

// NewVersionProvider wires the version todo provider.
func NewVersionProvider(query VersionQueryService) *VersionProvider {
	return &VersionProvider{query: query}
}

// Items returns the version-context todo hits. Both sources are fetched with the
// workspace maximum so the service can count before truncating the response.
func (p *VersionProvider) Items(ctx context.Context, _ platform.Actor) ([]workspaceapp.TodoItem, error) {
	if p == nil || p.query == nil {
		return nil, platform.Unavailable("version todo provider is not configured")
	}
	drafts, err := p.query.ListDraftRequirements(ctx, workspaceapp.MaxLimit)
	if err != nil {
		return nil, err
	}
	pending, err := p.query.ListPendingDesignVersions(ctx)
	if err != nil {
		return nil, err
	}

	items := make([]workspaceapp.TodoItem, 0, len(drafts)+len(pending))
	for _, hit := range drafts {
		items = append(items, workspaceapp.TodoItem{
			Kind:         hit.Kind,
			Title:        hit.Title,
			VersionID:    hit.VersionID,
			VersionTitle: hit.VersionTitle,
			Severity:     hit.Severity,
			CreatedAt:    hit.CreatedAt,
			ExpiresAt:    hit.ExpiresAt,
			URL:          hit.URL,
		})
	}
	for _, hit := range pending {
		items = append(items, workspaceapp.TodoItem{
			Kind:         hit.Kind,
			Title:        hit.Title,
			VersionID:    hit.VersionID,
			VersionTitle: hit.VersionTitle,
			Severity:     hit.Severity,
			CreatedAt:    hit.CreatedAt,
			ExpiresAt:    hit.ExpiresAt,
			URL:          hit.URL,
		})
	}
	return items, nil
}

// Compile-time guarantee that the production query service satisfies the port.
var _ VersionQueryService = (*versionapp.QueryService)(nil)
