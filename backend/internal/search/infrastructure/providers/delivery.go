package providers

import (
	"context"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	searchapp "github.com/EdouardRichard/EzTest/backend/internal/search/application"
)

// DeliveryQueries is the portion of the delivery context's QueryService needed
// by the issue and report search adapters. The owning QueryService maps report
// kind to the 波次报告/版本报告 title and builds the delivery-tab deep links.
type DeliveryQueries interface {
	SearchIssues(ctx context.Context, q string, limit int) ([]deliveryapp.SearchHit, error)
	SearchReports(ctx context.Context, q string, limit int) ([]deliveryapp.SearchHit, error)
}

// IssueSearcher adapts issue title search (type=issue).
type IssueSearcher struct {
	query DeliveryQueries
}

// NewIssueSearcher wires the issue search adapter.
func NewIssueSearcher(query DeliveryQueries) *IssueSearcher {
	return &IssueSearcher{query: query}
}

// Search returns the owning context's issue hits as search items.
func (s *IssueSearcher) Search(ctx context.Context, q string, limit int) ([]searchapp.Item, error) {
	if s == nil || s.query == nil {
		return nil, notConfigured("issue")
	}
	hits, err := s.query.SearchIssues(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	return deliveryItems(hits), nil
}

// ReportSearcher adapts report search (type=report).
type ReportSearcher struct {
	query DeliveryQueries
}

// NewReportSearcher wires the report search adapter.
func NewReportSearcher(query DeliveryQueries) *ReportSearcher {
	return &ReportSearcher{query: query}
}

// Search returns the owning context's report hits as search items.
func (s *ReportSearcher) Search(ctx context.Context, q string, limit int) ([]searchapp.Item, error) {
	if s == nil || s.query == nil {
		return nil, notConfigured("report")
	}
	hits, err := s.query.SearchReports(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	return deliveryItems(hits), nil
}

func deliveryItems(hits []deliveryapp.SearchHit) []searchapp.Item {
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
