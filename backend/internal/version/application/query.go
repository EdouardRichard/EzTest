package versionapp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// P6 cross-context read limits (spec §4.3.1 search and §4.3.2 todos). The HTTP
// layer may pass its own values; the query port still normalizes them so a
// direct application caller can never request an unbounded page.
const (
	defaultSearchLimit = 5
	maxSearchLimit     = 20
	defaultTodoLimit   = 50
	maxTodoLimit       = 200
)

// Todo kind values shared by the workspace aggregation. Each owning context
// exports the kinds it produces.
const (
	TodoKindRequirementConfirmation = "requirement_confirmation"
	TodoKindDesignPending           = "design_pending"
)

// Severity values of the todo contract (spec §4.3.2).
const (
	SeverityWarning = "warning"
)

// SearchHit is one cross-context search result of the version context. The URL
// is the deep link of the 0.2 workspace; VersionID and VersionTitle identify
// the owning version of a requirement hit.
type SearchHit struct {
	ID           int64
	Title        string
	Subtitle     string
	VersionID    int64
	VersionTitle string
	Status       string
	URL          string
}

// TodoHit is one pending-work item of the workspace todo feed. ExpiresAt is
// only set by sources with a deadline (human requests).
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

// VersionSearchRow is the raw version row returned by the P6 read source.
type VersionSearchRow struct {
	ID        int64
	Title     string
	Status    string
	UpdatedAt time.Time
}

// RequirementSearchRow is the raw current-revision requirement row returned by
// the P6 read source.
type RequirementSearchRow struct {
	ID           int64
	VersionID    int64
	VersionTitle string
	Title        string
	Status       string
	UpdatedAt    time.Time
}

// PendingDesignVersionRow is a version whose pending design count is positive.
type PendingDesignVersionRow struct {
	VersionID    int64
	VersionTitle string
	PendingCount int
}

// DraftRequirementRow is one current requirement in draft status.
type DraftRequirementRow struct {
	ID           int64
	VersionID    int64
	VersionTitle string
	Title        string
	CreatedAt    time.Time
}

// QueryReader is the version context's P6 read-only data source. It is
// implemented outside the application layer (SQL over version/requirement
// tables); every method is read-only and bounded by the passed limit. The
// application service adds URL construction, limit normalization and todo/kind
// projection so consumers only see the cross-context DTOs.
type QueryReader interface {
	// CountRequirementClosure returns closed requirements (num) over the
	// countable requirements (draft+clarified+closed, obsolete excluded), for
	// one version when versionID is non-nil and globally otherwise.
	CountRequirementClosure(ctx context.Context, versionID *int64) (num, denom int, err error)
	SearchVersions(ctx context.Context, q string, limit int) ([]VersionSearchRow, error)
	SearchRequirements(ctx context.Context, q string, limit int) ([]RequirementSearchRow, error)
	// VersionTitles returns the title of every existing id in one batch.
	VersionTitles(ctx context.Context, ids []int64) (map[int64]string, error)
	// ListPendingDesignVersions returns the versions with a positive pending
	// design count. The pending count is owned by the design context, so the
	// implementation must read it through the design application port, never
	// through a cross-context SQL join.
	ListPendingDesignVersions(ctx context.Context) ([]PendingDesignVersionRow, error)
	ListDraftRequirements(ctx context.Context, limit int) ([]DraftRequirementRow, error)
}

// QueryService is the P6 read-only query port of the version context. It is
// consumed by search, workspace, quality and agent-center aggregators; it never
// writes and never touches another context's storage.
type QueryService struct {
	reader QueryReader
}

// NewQueryService wires the version read-only query port.
func NewQueryService(reader QueryReader) *QueryService {
	return &QueryService{reader: reader}
}

func (s *QueryService) requireReader() error {
	if s == nil || s.reader == nil {
		return platform.Unavailable("version query reader is not configured")
	}
	return nil
}

// CountRequirementClosure returns the requirement closure rate counters
// (spec §5.2): closed / (draft+clarified+closed). A zero denominator is a valid
// observation and is returned unchanged with a nil error.
func (s *QueryService) CountRequirementClosure(ctx context.Context, versionID *int64) (int, int, error) {
	if err := s.requireReader(); err != nil {
		return 0, 0, err
	}
	if versionID != nil && *versionID <= 0 {
		return 0, 0, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	return s.reader.CountRequirementClosure(ctx, versionID)
}

// SearchVersions returns version title matches. q is trimmed; limit defaults to
// 5 and is capped at 20 (spec §4.3.1).
func (s *QueryService) SearchVersions(ctx context.Context, q string, limit int) ([]SearchHit, error) {
	if err := s.requireReader(); err != nil {
		return nil, err
	}
	rows, err := s.reader.SearchVersions(ctx, strings.TrimSpace(q), normalizeSearchLimit(limit))
	if err != nil {
		return nil, err
	}
	hits := make([]SearchHit, 0, len(rows))
	for _, row := range rows {
		hits = append(hits, SearchHit{
			ID:           row.ID,
			Title:        row.Title,
			VersionID:    row.ID,
			VersionTitle: row.Title,
			Status:       row.Status,
			URL:          versionOverviewURL(row.ID),
		})
	}
	return hits, nil
}

// SearchRequirements returns current requirement-revision title matches with
// their version context and the version workspace deep link.
func (s *QueryService) SearchRequirements(ctx context.Context, q string, limit int) ([]SearchHit, error) {
	if err := s.requireReader(); err != nil {
		return nil, err
	}
	rows, err := s.reader.SearchRequirements(ctx, strings.TrimSpace(q), normalizeSearchLimit(limit))
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
			URL:          fmt.Sprintf("/versions/%d?tab=requirements&requirement=%d", row.VersionID, row.ID),
		})
	}
	return hits, nil
}

// VersionTitles resolves version titles in one batch. Non-positive IDs are
// dropped and duplicates are collapsed before the read so callers can pass
// arbitrary ID sets without producing N+1 lookups.
func (s *QueryService) VersionTitles(ctx context.Context, ids []int64) (map[int64]string, error) {
	if err := s.requireReader(); err != nil {
		return nil, err
	}
	unique := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return map[int64]string{}, nil
	}
	titles, err := s.reader.VersionTitles(ctx, unique)
	if err != nil {
		return nil, err
	}
	if titles == nil {
		titles = map[int64]string{}
	}
	return titles, nil
}

// ListPendingDesignVersions returns one design_pending todo per version whose
// pending design count is positive (spec §4.3.2).
func (s *QueryService) ListPendingDesignVersions(ctx context.Context) ([]TodoHit, error) {
	if err := s.requireReader(); err != nil {
		return nil, err
	}
	rows, err := s.reader.ListPendingDesignVersions(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]TodoHit, 0, len(rows))
	for _, row := range rows {
		if row.PendingCount <= 0 {
			continue
		}
		items = append(items, TodoHit{
			Kind:         TodoKindDesignPending,
			Title:        fmt.Sprintf("%d 条待设计", row.PendingCount),
			VersionID:    row.VersionID,
			VersionTitle: row.VersionTitle,
			Severity:     SeverityWarning,
			URL:          fmt.Sprintf("/versions/%d?tab=design", row.VersionID),
		})
	}
	return items, nil
}

// ListDraftRequirements returns the current draft requirements as
// requirement_confirmation todos. limit defaults to 50 and is capped at 200.
func (s *QueryService) ListDraftRequirements(ctx context.Context, limit int) ([]TodoHit, error) {
	if err := s.requireReader(); err != nil {
		return nil, err
	}
	rows, err := s.reader.ListDraftRequirements(ctx, normalizeTodoLimit(limit))
	if err != nil {
		return nil, err
	}
	items := make([]TodoHit, 0, len(rows))
	for _, row := range rows {
		items = append(items, TodoHit{
			Kind:         TodoKindRequirementConfirmation,
			Title:        row.Title,
			VersionID:    row.VersionID,
			VersionTitle: row.VersionTitle,
			Severity:     SeverityWarning,
			CreatedAt:    row.CreatedAt,
			URL:          fmt.Sprintf("/versions/%d?tab=requirements&requirement=%d", row.VersionID, row.ID),
		})
	}
	return items, nil
}

func versionOverviewURL(versionID int64) string {
	return fmt.Sprintf("/versions/%d?tab=overview", versionID)
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
