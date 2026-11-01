package workspaceproviders

import (
	"context"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	workspaceapp "github.com/EdouardRichard/EzTest/backend/internal/workspace/application"
)

// DesignQueryService is the design context's P6 read-only surface needed by the
// todo feed.
type DesignQueryService interface {
	ListDraftCases(ctx context.Context, limit int) ([]designapp.TodoHit, error)
}

// DesignProvider supplies case_confirmation todos.
type DesignProvider struct {
	query DesignQueryService
}

// NewDesignProvider wires the design todo provider.
func NewDesignProvider(query DesignQueryService) *DesignProvider {
	return &DesignProvider{query: query}
}

// Items returns the draft cases as case_confirmation todos. The source is
// fetched with the workspace maximum so counts stay pre-limit.
func (p *DesignProvider) Items(ctx context.Context, _ platform.Actor) ([]workspaceapp.TodoItem, error) {
	if p == nil || p.query == nil {
		return nil, platform.Unavailable("design todo provider is not configured")
	}
	hits, err := p.query.ListDraftCases(ctx, workspaceapp.MaxLimit)
	if err != nil {
		return nil, err
	}
	items := make([]workspaceapp.TodoItem, 0, len(hits))
	for _, hit := range hits {
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
var _ DesignQueryService = (*designapp.QueryService)(nil)
