package providers

import (
	"context"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	searchapp "github.com/EdouardRichard/EzTest/backend/internal/search/application"
)

// DesignQueries is the portion of the design context's QueryService needed by
// the case search adapter. The owning QueryService resolves the current case
// version title and builds the design-tab deep link.
type DesignQueries interface {
	SearchCases(ctx context.Context, q string, limit int) ([]designapp.SearchHit, error)
}

// CaseSearcher adapts current case-version title search (type=case).
type CaseSearcher struct {
	query DesignQueries
}

// NewCaseSearcher wires the case search adapter.
func NewCaseSearcher(query DesignQueries) *CaseSearcher {
	return &CaseSearcher{query: query}
}

// Search returns the owning context's case hits as search items.
func (s *CaseSearcher) Search(ctx context.Context, q string, limit int) ([]searchapp.Item, error) {
	if s == nil || s.query == nil {
		return nil, notConfigured("case")
	}
	hits, err := s.query.SearchCases(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	items := make([]searchapp.Item, 0, len(hits))
	for _, hit := range hits {
		items = append(items, searchapp.Item{
			ID:           decimalID(hit.ID),
			Title:        hit.Title,
			Subtitle:     hit.Subtitle,
			VersionID:    decimalID(hit.VersionID),
			VersionTitle: hit.VersionTitle,
			Status:       hit.Status,
			URL:          hit.URL,
		})
	}
	return items, nil
}
