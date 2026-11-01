package deliveryapp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// P6 cross-context read limits (spec §4.3.1 search and §4.3.2 todos).
const (
	defaultSearchLimit = 5
	maxSearchLimit     = 20
	defaultTodoLimit   = 50
	maxTodoLimit       = 200
)

// Todo kind and severity values produced by the delivery context.
const (
	TodoKindIssueConfirmation = "issue_confirmation"

	SeverityMajor   = "major"
	SeverityWarning = "warning"
	SeverityInfo    = "info"
)

// SearchHit is one cross-context search result of the delivery context.
type SearchHit struct {
	ID           int64
	Title        string
	Subtitle     string
	VersionID    int64
	VersionTitle string
	Status       string
	URL          string
}

// TodoHit is one pending-work item of the workspace todo feed.
type TodoHit struct {
	Kind         string
	Title        string
	VersionID    int64
	VersionTitle string
	Severity     string
	CreatedAt    time.Time
	ExpiresAt    *time.Time
	URL          string
}

// IssueSearchRow is one delivery_issue title-match row.
type IssueSearchRow struct {
	ID           int64
	VersionID    int64
	VersionTitle string
	Title        string
	Status       string
	Severity     string
	UpdatedAt    time.Time
}

// ReportSearchRow is one delivery_report row used by the report search. Kind is
// wave/version and Status the report status.
type ReportSearchRow struct {
	ID           int64
	VersionID    int64
	VersionTitle string
	Kind         string
	Status       string
	UpdatedAt    time.Time
}

// OpenIssueRow is one open delivery_issue row.
type OpenIssueRow struct {
	ID           int64
	VersionID    int64
	VersionTitle string
	Title        string
	Severity     string
	CreatedAt    time.Time
}

// QueryReader is the delivery context's P6 read-only data source. It is
// implemented outside the application layer; the application service adds URL
// construction, severity mapping, limit normalization and todo projection.
type QueryReader interface {
	// CountIssueValidity returns confirmed issues (num) over confirmed+dismissed
	// issues (denom), for one version when versionID is non-nil and globally
	// otherwise. Open/merged issues are excluded from both counters.
	CountIssueValidity(ctx context.Context, versionID *int64) (num, denom int, err error)
	SearchIssues(ctx context.Context, q string, limit int) ([]IssueSearchRow, error)
	SearchReports(ctx context.Context, q string, limit int) ([]ReportSearchRow, error)
	ListOpenIssues(ctx context.Context, limit int) ([]OpenIssueRow, error)
}

// QueryService is the P6 read-only query port of the delivery context.
type QueryService struct {
	reader QueryReader
}

// NewQueryService wires the delivery read-only query port.
func NewQueryService(reader QueryReader) *QueryService {
	return &QueryService{reader: reader}
}

func (s *QueryService) requireReader() error {
	if s == nil || s.reader == nil {
		return platform.Unavailable("delivery query reader is not configured")
	}
	return nil
}

// CountIssueValidity returns the issue validity rate counters (spec §5.2). A
// zero denominator is a valid observation.
func (s *QueryService) CountIssueValidity(ctx context.Context, versionID *int64) (int, int, error) {
	if err := s.requireReader(); err != nil {
		return 0, 0, err
	}
	if versionID != nil && *versionID <= 0 {
		return 0, 0, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	return s.reader.CountIssueValidity(ctx, versionID)
}

// SearchIssues returns issue title matches with the issues workspace deep link.
func (s *QueryService) SearchIssues(ctx context.Context, q string, limit int) ([]SearchHit, error) {
	if err := s.requireReader(); err != nil {
		return nil, err
	}
	rows, err := s.reader.SearchIssues(ctx, strings.TrimSpace(q), normalizeSearchLimit(limit))
	if err != nil {
		return nil, err
	}
	hits := make([]SearchHit, 0, len(rows))
	for _, row := range rows {
		hits = append(hits, SearchHit{
			ID:           row.ID,
			Title:        row.Title,
			VersionID:    row.VersionID,
			VersionTitle: row.VersionTitle,
			Status:       row.Status,
			URL:          fmt.Sprintf("/versions/%d?tab=issues&issue=%d", row.VersionID, row.ID),
		})
	}
	return hits, nil
}

// SearchReports returns report matches. The title is derived from the report
// kind (spec §4.3.1) and the status is exposed as the subtitle.
func (s *QueryService) SearchReports(ctx context.Context, q string, limit int) ([]SearchHit, error) {
	if err := s.requireReader(); err != nil {
		return nil, err
	}
	rows, err := s.reader.SearchReports(ctx, strings.TrimSpace(q), normalizeSearchLimit(limit))
	if err != nil {
		return nil, err
	}
	hits := make([]SearchHit, 0, len(rows))
	for _, row := range rows {
		hits = append(hits, SearchHit{
			ID:           row.ID,
			Title:        reportKindTitle(row.Kind),
			Subtitle:     row.Status,
			VersionID:    row.VersionID,
			VersionTitle: row.VersionTitle,
			Status:       row.Status,
			URL:          fmt.Sprintf("/versions/%d?tab=reports&report=%d", row.VersionID, row.ID),
		})
	}
	return hits, nil
}

// ListOpenIssues returns open issues as issue_confirmation todos. The issue
// severity maps onto the todo severity vocabulary (spec §4.3.2).
func (s *QueryService) ListOpenIssues(ctx context.Context, limit int) ([]TodoHit, error) {
	if err := s.requireReader(); err != nil {
		return nil, err
	}
	rows, err := s.reader.ListOpenIssues(ctx, normalizeTodoLimit(limit))
	if err != nil {
		return nil, err
	}
	items := make([]TodoHit, 0, len(rows))
	for _, row := range rows {
		items = append(items, TodoHit{
			Kind:         TodoKindIssueConfirmation,
			Title:        row.Title,
			VersionID:    row.VersionID,
			VersionTitle: row.VersionTitle,
			Severity:     mapIssueSeverity(row.Severity),
			CreatedAt:    row.CreatedAt,
			URL:          fmt.Sprintf("/versions/%d?tab=issues&issue=%d", row.VersionID, row.ID),
		})
	}
	return items, nil
}

func reportKindTitle(kind string) string {
	switch kind {
	case "wave":
		return "波次报告"
	case "version":
		return "版本报告"
	default:
		return "报告"
	}
}

func mapIssueSeverity(severity string) string {
	switch severity {
	case SeverityMajor:
		return SeverityMajor
	case "minor":
		return SeverityWarning
	case "trivial":
		return SeverityInfo
	default:
		return SeverityWarning
	}
}

func normalizeSearchLimit(limit int) int {
	if limit <= 0 {
		return defaultSearchLimit
	}
	if limit > maxSearchLimit {
		return maxSearchLimit
	}
	return limit
}

func normalizeTodoLimit(limit int) int {
	if limit <= 0 {
		return defaultTodoLimit
	}
	if limit > maxTodoLimit {
		return maxTodoLimit
	}
	return limit
}
