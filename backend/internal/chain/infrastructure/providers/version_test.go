package providers

import (
	"context"
	"testing"
	"time"

	chain "github.com/EdouardRichard/EzTest/backend/internal/chain/domain"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// pagingRequirementLister mimics RequirementService.List: it normalizes the
// requested limit to maxLimit (the version application clamps every list page
// to platform/pagination.MaxLimit=100) and pages by the exclusive beforeID
// cursor in descending ID order.
type pagingRequirementLister struct {
	views    []versionapp.RequirementView
	maxLimit int
	limits   []int
	cursors  []*int64
}

func (s *pagingRequirementLister) List(_ context.Context, _ int64, _ string, limit int, beforeID *int64) ([]versionapp.RequirementView, bool, error) {
	if s.maxLimit > 0 && limit > s.maxLimit {
		limit = s.maxLimit
	}
	if limit <= 0 {
		limit = 20
	}
	s.limits = append(s.limits, limit)
	if beforeID == nil {
		s.cursors = append(s.cursors, nil)
	} else {
		cursor := *beforeID
		s.cursors = append(s.cursors, &cursor)
	}
	start := 0
	if beforeID != nil {
		for start < len(s.views) && s.views[start].ID >= *beforeID {
			start++
		}
	}
	if start >= len(s.views) {
		return nil, false, nil
	}
	end := start + limit
	hasMore := end < len(s.views)
	if end > len(s.views) {
		end = len(s.views)
	}
	return append([]versionapp.RequirementView(nil), s.views[start:end]...), hasMore, nil
}

// requirementViews returns count requirement views with descending IDs
// count..1, matching the version list order.
func requirementViews(count int) []versionapp.RequirementView {
	views := make([]versionapp.RequirementView, 0, count)
	updatedAt := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
	for id := count; id >= 1; id-- {
		views = append(views, versionapp.RequirementView{
			ID:        int64(id),
			Title:     "Requirement",
			Status:    "clarified",
			Revision:  1,
			UpdatedAt: updatedAt,
		})
	}
	return views
}

// TestVersionProviderRequirementsPagesPastListCap pins the chain root read
// against the version List clamp: the chain service asks for
// MaxRequirements+1 (101) roots, while one version List page returns at most
// 100 rows. The provider must follow the has_more/beforeID cursor so BuildChain
// can see the extra root and report requirement_budget truncation instead of
// silently hiding it.
func TestVersionProviderRequirementsPagesPastListCap(t *testing.T) {
	lister := &pagingRequirementLister{views: requirementViews(250), maxLimit: 100}
	provider := NewVersionProvider(lister, nil)

	nodes, err := provider.Requirements(context.Background(), 42, chain.DefaultMaxRequirements+1)
	if err != nil {
		t.Fatalf("Requirements: %v", err)
	}
	if got := len(nodes); got != chain.DefaultMaxRequirements+1 {
		t.Fatalf("len(nodes) = %d, want MaxRequirements+1 = %d", got, chain.DefaultMaxRequirements+1)
	}
	if nodes[0].RefID != "250" || nodes[len(nodes)-1].RefID != "150" {
		t.Errorf("root refs = %q..%q, want the newest 101 roots 250..150", nodes[0].RefID, nodes[len(nodes)-1].RefID)
	}
	for _, node := range nodes {
		if node.Kind != chain.NodeRequirement {
			t.Errorf("node %q kind = %q, want requirement", node.ID, node.Kind)
		}
	}
	if len(lister.limits) != 2 || lister.limits[0] != 100 || lister.limits[1] != 1 {
		t.Errorf("list limits = %v, want two pages [100 1]", lister.limits)
	}
	if len(lister.cursors) != 2 || lister.cursors[0] != nil {
		t.Fatalf("list cursors = %v, want a nil first cursor", lister.cursors)
	}
	if lister.cursors[1] == nil || *lister.cursors[1] != 151 {
		t.Errorf("second page cursor = %v, want the last id of page one (151)", lister.cursors[1])
	}
}

// TestVersionProviderRequirementsStopsAtExhaustion keeps the paginator from
// asking for another page when the version dataset is smaller than the root
// budget.
func TestVersionProviderRequirementsStopsAtExhaustion(t *testing.T) {
	lister := &pagingRequirementLister{views: requirementViews(5), maxLimit: 100}
	provider := NewVersionProvider(lister, nil)

	nodes, err := provider.Requirements(context.Background(), 42, chain.DefaultMaxRequirements+1)
	if err != nil {
		t.Fatalf("Requirements: %v", err)
	}
	if got := len(nodes); got != 5 {
		t.Errorf("len(nodes) = %d, want all 5 roots", got)
	}
	if len(lister.limits) != 1 {
		t.Errorf("list calls = %d, want one page for an exhausted dataset", len(lister.limits))
	}
}
