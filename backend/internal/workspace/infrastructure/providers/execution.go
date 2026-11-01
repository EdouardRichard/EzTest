package workspaceproviders

import (
	"context"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	workspaceapp "github.com/EdouardRichard/EzTest/backend/internal/workspace/application"
)

// ExecutionQueryService is the execution context's P6 read-only surface needed
// by the todo feed. OpenHumanRequests already filters login_takeover for
// non-admin viewers; DeadWorkItems has no viewer parameter because the
// workspace provider is the admin gate for dead work items.
type ExecutionQueryService interface {
	OpenHumanRequests(ctx context.Context, viewer platform.Actor, limit int) ([]executionapp.TodoHit, error)
	DeadWorkItems(ctx context.Context, limit int) ([]executionapp.TodoHit, error)
}

// ExecutionProvider supplies human_request and dead_work_item todos.
type ExecutionProvider struct {
	query ExecutionQueryService
}

// NewExecutionProvider wires the execution todo provider.
func NewExecutionProvider(query ExecutionQueryService) *ExecutionProvider {
	return &ExecutionProvider{query: query}
}

// Items returns open human requests for every viewer and dead work items for
// admins only. A regular user never triggers the dead work item read, matching
// the "non-admin does not query the admin-only group" rule (§4.3.2).
func (p *ExecutionProvider) Items(ctx context.Context, viewer platform.Actor) ([]workspaceapp.TodoItem, error) {
	if p == nil || p.query == nil {
		return nil, platform.Unavailable("execution todo provider is not configured")
	}
	human, err := p.query.OpenHumanRequests(ctx, viewer, workspaceapp.MaxLimit)
	if err != nil {
		return nil, err
	}
	items := make([]workspaceapp.TodoItem, 0, len(human))
	items = appendExecutionHits(items, human)

	if !viewer.IsAdmin() {
		return items, nil
	}
	dead, err := p.query.DeadWorkItems(ctx, workspaceapp.MaxLimit)
	if err != nil {
		return nil, err
	}
	return appendExecutionHits(items, dead), nil
}

func appendExecutionHits(items []workspaceapp.TodoItem, hits []executionapp.TodoHit) []workspaceapp.TodoItem {
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
	return items
}

// Compile-time guarantee that the production query service satisfies the port.
var _ ExecutionQueryService = (*executionapp.QueryService)(nil)
