package workspaceproviders

import (
	"context"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	workspaceapp "github.com/EdouardRichard/EzTest/backend/internal/workspace/application"
)

// DeliveryQueryService is the delivery context's P6 read-only surface needed by
// the todo feed.
type DeliveryQueryService interface {
	ListOpenIssues(ctx context.Context, limit int) ([]deliveryapp.TodoHit, error)
}

// DeliveryProvider supplies issue_confirmation todos.
type DeliveryProvider struct {
	query DeliveryQueryService
}

// NewDeliveryProvider wires the delivery todo provider.
func NewDeliveryProvider(query DeliveryQueryService) *DeliveryProvider {
	return &DeliveryProvider{query: query}
}

// Items returns the open issues as issue_confirmation todos. The source is
// fetched with the workspace maximum so counts stay pre-limit.
func (p *DeliveryProvider) Items(ctx context.Context, _ platform.Actor) ([]workspaceapp.TodoItem, error) {
	if p == nil || p.query == nil {
		return nil, platform.Unavailable("delivery todo provider is not configured")
	}
	hits, err := p.query.ListOpenIssues(ctx, workspaceapp.MaxLimit)
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
var _ DeliveryQueryService = (*deliveryapp.QueryService)(nil)
