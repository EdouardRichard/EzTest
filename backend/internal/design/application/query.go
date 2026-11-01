package designapp

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

// Todo kind and severity values produced by the design context.
const (
	TodoKindCaseConfirmation = "case_confirmation"
	SeverityWarning          = "warning"
)

// SearchHit is one cross-context search result of the design context. ID is
// the logical case id; CaseVersionID is kept on the row to make the current
// version explicit but the deep link uses the case id.
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

// CaseSearchRow is the raw current case-version row returned by the P6 read
// source. CaseID is the logical case id and CaseVersionID its current version.
type CaseSearchRow struct {
	CaseID        int64
	CaseVersionID int64
	VersionID     int64
	VersionTitle  string
	Title         string
	Status        string
	UpdatedAt     time.Time
}

// DraftCaseRow is one current case version in draft status.
type DraftCaseRow struct {
	CaseID       int64
	VersionID    int64
	VersionTitle string
	Title        string
	CreatedAt    time.Time
}

// QueryReader is the design context's P6 read-only data source. It is
// implemented outside the application layer (SQL over the design tables); the
// application service adds URL construction, limit normalization and todo
// projection.
type QueryReader interface {
	// CountCaseConfirmation returns confirmed current case versions (num) over
	// current case versions in draft+confirmed+stale (denom), for one version
	// when versionID is non-nil and globally otherwise.
	CountCaseConfirmation(ctx context.Context, versionID *int64) (num, denom int, err error)
	SearchCases(ctx context.Context, q string, limit int) ([]CaseSearchRow, error)
	ListDraftCases(ctx context.Context, limit int) ([]DraftCaseRow, error)
	// PendingDesignCounts returns the pending design count of every requested
	// version in one batch: clarified requirements without an active design
	// scope. Versions with a zero pending count are omitted from the map. The
	// version context's reader consumes this through its application port so it
	// never joins the design tables.
	PendingDesignCounts(ctx context.Context, versionIDs []int64) (map[int64]int, error)
}

// QueryService is the P6 read-only query port of the design context.
type QueryService struct {
	reader QueryReader
}

// NewQueryService wires the design read-only query port.
func NewQueryService(reader QueryReader) *QueryService {
	return &QueryService{reader: reader}
}

func (s *QueryService) requireReader() error {
	if s == nil || s.reader == nil {
		return platform.Unavailable("design query reader is not configured")
	}
	return nil
}

// CountCaseConfirmation returns the case confirmation rate counters
// (spec §5.2). A zero denominator is a valid observation.
func (s *QueryService) CountCaseConfirmation(ctx context.Context, versionID *int64) (int, int, error) {
	if err := s.requireReader(); err != nil {
		return 0, 0, err
	}
	if versionID != nil && *versionID <= 0 {
		return 0, 0, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	return s.reader.CountCaseConfirmation(ctx, versionID)
}

// SearchCases returns current case-version title matches with their version
// context and the design workspace deep link.
func (s *QueryService) SearchCases(ctx context.Context, q string, limit int) ([]SearchHit, error) {
	if err := s.requireReader(); err != nil {
		return nil, err
	}
	rows, err := s.reader.SearchCases(ctx, strings.TrimSpace(q), normalizeSearchLimit(limit))
	if err != nil {
		return nil, err
	}
	hits := make([]SearchHit, 0, len(rows))
	for _, row := range rows {
		hits = append(hits, SearchHit{
			ID:           row.CaseID,
			Title:        row.Title,
			VersionID:    row.VersionID,
			VersionTitle: row.VersionTitle,
			Status:       row.Status,
			URL:          fmt.Sprintf("/versions/%d?tab=design&case=%d", row.VersionID, row.CaseID),
		})
	}
	return hits, nil
}

// ListDraftCases returns the current draft cases as case_confirmation todos.
// limit defaults to 50 and is capped at 200.
func (s *QueryService) ListDraftCases(ctx context.Context, limit int) ([]TodoHit, error) {
	if err := s.requireReader(); err != nil {
		return nil, err
	}
	rows, err := s.reader.ListDraftCases(ctx, normalizeTodoLimit(limit))
	if err != nil {
		return nil, err
	}
	items := make([]TodoHit, 0, len(rows))
	for _, row := range rows {
		items = append(items, TodoHit{
			Kind:         TodoKindCaseConfirmation,
			Title:        row.Title,
			VersionID:    row.VersionID,
			VersionTitle: row.VersionTitle,
			Severity:     SeverityWarning,
			CreatedAt:    row.CreatedAt,
			URL:          fmt.Sprintf("/versions/%d?tab=design&case=%d", row.VersionID, row.CaseID),
		})
	}
	return items, nil
}

// PendingDesignCounts batches the pending design count of the requested
// versions for the version read source. Non-positive IDs are dropped and
// duplicates are collapsed before the read; an empty input returns an empty map
// without touching the reader.
func (s *QueryService) PendingDesignCounts(ctx context.Context, versionIDs []int64) (map[int64]int, error) {
	if err := s.requireReader(); err != nil {
		return nil, err
	}
	unique := make([]int64, 0, len(versionIDs))
	seen := make(map[int64]struct{}, len(versionIDs))
	for _, id := range versionIDs {
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
		return map[int64]int{}, nil
	}
	counts, err := s.reader.PendingDesignCounts(ctx, unique)
	if err != nil {
		return nil, err
	}
	if counts == nil {
		counts = map[int64]int{}
	}
	return counts, nil
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
