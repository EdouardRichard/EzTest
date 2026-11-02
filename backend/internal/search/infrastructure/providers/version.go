package providers

import (
	"context"

	searchapp "github.com/EdouardRichard/EzTest/backend/internal/search/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// VersionQueries is the portion of the version context's QueryService needed by
// the version and requirement search adapters. The owning QueryService already
// trims q, normalizes the per-type limit and builds the workspace deep links.
type VersionQueries interface {
	SearchVersions(ctx context.Context, q string, limit int) ([]versionapp.SearchHit, error)
	SearchRequirements(ctx context.Context, q string, limit int) ([]versionapp.SearchHit, error)
}

// VersionSearcher adapts version title search (type=version).
type VersionSearcher struct {
	query VersionQueries
}

// NewVersionSearcher wires the version search adapter.
func NewVersionSearcher(query VersionQueries) *VersionSearcher {
	return &VersionSearcher{query: query}
}

// Search returns the owning context's version hits as search items.
func (s *VersionSearcher) Search(ctx context.Context, q string, limit int) ([]searchapp.Item, error) {
	if s == nil || s.query == nil {
		return nil, notConfigured("version")
	}
	hits, err := s.query.SearchVersions(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	return versionItems(hits), nil
}

// RequirementSearcher adapts current requirement-revision title search
// (type=requirement).
type RequirementSearcher struct {
	query VersionQueries
}

// NewRequirementSearcher wires the requirement search adapter.
func NewRequirementSearcher(query VersionQueries) *RequirementSearcher {
	return &RequirementSearcher{query: query}
}

// Search returns the owning context's requirement hits as search items.
func (s *RequirementSearcher) Search(ctx context.Context, q string, limit int) ([]searchapp.Item, error) {
	if s == nil || s.query == nil {
		return nil, notConfigured("requirement")
	}
	hits, err := s.query.SearchRequirements(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	return versionItems(hits), nil
}

func versionItems(hits []versionapp.SearchHit) []searchapp.Item {
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
	return items
}
