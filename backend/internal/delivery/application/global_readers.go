package application

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// GlobalIssueFilter is the cross-version issue filter of GET /api/issues
// (spec §7.10): the version-scoped §7.3 filter plus versionId. Cursor is the
// opaque keyset cursor owned by the store; Limit is clamped to the shared
// 1–100 page bound.
type GlobalIssueFilter struct {
	VersionID  *int64
	Status     string
	Severity   string
	Category   string
	BatchID    *int64
	CaseRunID  *int64
	CaseNodeID *int64
	Query      string
	Cursor     string
	Limit      int
}

// GlobalIssueRow is one row of the global issue centre: the issue aggregate, its
// refs and the owning version's display name.
type GlobalIssueRow struct {
	Issue       domain.Issue
	Refs        []domain.IssueRef
	VersionName string
}

// GlobalIssueReader is the cross-version issue page query of GET /api/issues.
// *GlobalReaderService implements it for the HTTP layer.
type GlobalIssueReader interface {
	ListGlobalIssues(ctx context.Context, filter GlobalIssueFilter) ([]GlobalIssueRow, string, bool, error)
}

// GlobalReportFilter is the cross-version report filter of GET /api/reports
// (spec §7.10): optional versionId and exact status plus the opaque keyset
// cursor and the 1–100 page bound.
type GlobalReportFilter struct {
	VersionID *int64
	Status    string
	Cursor    string
	Limit     int
}

// GlobalReportRow is one row of the global report centre: the report summary,
// the owning version's display name and the batch number.
type GlobalReportRow struct {
	Report      domain.Report
	VersionName string
	BatchNo     int
}

// GlobalReportReader is the cross-version report page query of GET /api/reports.
// *GlobalReaderService implements it for the HTTP layer.
type GlobalReportReader interface {
	ListGlobalReports(ctx context.Context, filter GlobalReportFilter) ([]GlobalReportRow, string, bool, error)
}

// GlobalIssueStore is the delivery-owned persistence port of the global issue
// page. Implementations query only the delivery context's issue/issue_ref tables
// (Agent.md §6.2: never another context's tables); VersionName may be left empty
// and is filled by GlobalReaderService through VersionNameReader.
type GlobalIssueStore interface {
	ListGlobalIssues(ctx context.Context, filter GlobalIssueFilter) ([]GlobalIssueRow, string, bool, error)
}

// GlobalReportStore is the delivery-owned persistence port of the global report
// page. It queries only the report table; BatchNo is derived from the stored
// report content (content.batch.batchNo). VersionName may be left empty.
type GlobalReportStore interface {
	ListGlobalReports(ctx context.Context, filter GlobalReportFilter) ([]GlobalReportRow, string, bool, error)
}

// VersionNameReader resolves the display names of the versions referenced by a
// global page. The delivery context never reads the version table; the
// composition root wires the version-context adapter over the version
// application service.
type VersionNameReader interface {
	VersionNames(ctx context.Context, versionIDs []int64) (map[int64]string, error)
}

// GlobalReaderDeps are the ports of the global centres. Each list method only
// requires its own store plus the version-name reader; both stores and the
// version reader are mandatory for the full read surface.
type GlobalReaderDeps struct {
	Issues   GlobalIssueStore
	Reports  GlobalReportStore
	Versions VersionNameReader
}

// GlobalReaderService implements the read-only cross-version centres of spec
// §7.10: it validates/normalizes the filter, delegates the page to the
// delivery-owned store and enriches each row with the version display name.
type GlobalReaderService struct {
	deps GlobalReaderDeps
}

// NewGlobalReaderService wires the global centres to their ports.
func NewGlobalReaderService(deps GlobalReaderDeps) *GlobalReaderService {
	return &GlobalReaderService{deps: deps}
}

var (
	_ GlobalIssueReader  = (*GlobalReaderService)(nil)
	_ GlobalReportReader = (*GlobalReaderService)(nil)
)

// ListGlobalIssues returns one page of the cross-version issue centre.
func (s *GlobalReaderService) ListGlobalIssues(ctx context.Context, filter GlobalIssueFilter) ([]GlobalIssueRow, string, bool, error) {
	if s == nil || s.deps.Issues == nil {
		return nil, "", false, errors.New("delivery: global issue store is not configured")
	}
	normalized, err := normalizeGlobalIssueFilter(filter)
	if err != nil {
		return nil, "", false, err
	}
	rows, next, hasMore, err := s.deps.Issues.ListGlobalIssues(ctx, normalized)
	if err != nil {
		return nil, "", false, fmt.Errorf("delivery: list global issues: %w", err)
	}
	if err := s.enrichIssueVersions(ctx, rows); err != nil {
		return nil, "", false, err
	}
	if rows == nil {
		rows = []GlobalIssueRow{}
	}
	return rows, next, hasMore, nil
}

// ListGlobalReports returns one page of the cross-version report centre.
func (s *GlobalReaderService) ListGlobalReports(ctx context.Context, filter GlobalReportFilter) ([]GlobalReportRow, string, bool, error) {
	if s == nil || s.deps.Reports == nil {
		return nil, "", false, errors.New("delivery: global report store is not configured")
	}
	normalized, err := normalizeGlobalReportFilter(filter)
	if err != nil {
		return nil, "", false, err
	}
	rows, next, hasMore, err := s.deps.Reports.ListGlobalReports(ctx, normalized)
	if err != nil {
		return nil, "", false, fmt.Errorf("delivery: list global reports: %w", err)
	}
	if err := s.enrichReportVersions(ctx, rows); err != nil {
		return nil, "", false, err
	}
	if rows == nil {
		rows = []GlobalReportRow{}
	}
	return rows, next, hasMore, nil
}

// enrichIssueVersions fills VersionName for every row. An empty page needs no
// version lookup, so the global centre still serves an empty list when no
// version can be resolved.
func (s *GlobalReaderService) enrichIssueVersions(ctx context.Context, rows []GlobalIssueRow) error {
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.Issue.VersionID)
	}
	names, err := s.versionNames(ctx, ids)
	if err != nil {
		return err
	}
	for index := range rows {
		rows[index].VersionName = names[rows[index].Issue.VersionID]
	}
	return nil
}

// enrichReportVersions fills VersionName for every row (same empty-page rule).
func (s *GlobalReaderService) enrichReportVersions(ctx context.Context, rows []GlobalReportRow) error {
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.Report.VersionID)
	}
	names, err := s.versionNames(ctx, ids)
	if err != nil {
		return err
	}
	for index := range rows {
		rows[index].VersionName = names[rows[index].Report.VersionID]
	}
	return nil
}

// versionNames resolves the distinct version ids of a page. A duplicate id is
// looked up once; an empty id set skips the port entirely.
func (s *GlobalReaderService) versionNames(ctx context.Context, versionIDs []int64) (map[int64]string, error) {
	distinct := make([]int64, 0, len(versionIDs))
	seen := make(map[int64]bool, len(versionIDs))
	for _, id := range versionIDs {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		distinct = append(distinct, id)
	}
	if len(distinct) == 0 {
		return map[int64]string{}, nil
	}
	if s.deps.Versions == nil {
		return nil, errors.New("delivery: version name reader is not configured")
	}
	names, err := s.deps.Versions.VersionNames(ctx, distinct)
	if err != nil {
		return nil, fmt.Errorf("delivery: load version names: %w", err)
	}
	if names == nil {
		names = map[int64]string{}
	}
	return names, nil
}

// normalizeGlobalIssueFilter validates and bounds a global issue filter. The HTTP
// boundary already validates the query string; the service re-validates so a
// direct caller cannot send an unbounded page or an invalid enum into SQL.
func normalizeGlobalIssueFilter(filter GlobalIssueFilter) (GlobalIssueFilter, error) {
	if filter.VersionID != nil && *filter.VersionID <= 0 {
		return GlobalIssueFilter{}, globalInvalid("versionId", "versionId must be a positive id")
	}
	if filter.BatchID != nil && *filter.BatchID <= 0 {
		return GlobalIssueFilter{}, globalInvalid("batchId", "batchId must be a positive id")
	}
	if filter.CaseRunID != nil && *filter.CaseRunID <= 0 {
		return GlobalIssueFilter{}, globalInvalid("caseRunId", "caseRunId must be a positive id")
	}
	if filter.CaseNodeID != nil && *filter.CaseNodeID <= 0 {
		return GlobalIssueFilter{}, globalInvalid("caseNodeId", "caseNodeId must be a positive id")
	}
	if filter.Status != "" {
		switch filter.Status {
		case domain.StatusOpen, domain.StatusConfirmed, domain.StatusDismissed:
		default:
			return GlobalIssueFilter{}, globalInvalid("status", "status must be open, confirmed or dismissed")
		}
	}
	if filter.Severity != "" {
		if err := domain.ValidateSeverity(filter.Severity); err != nil {
			return GlobalIssueFilter{}, globalInvalid("severity", "severity must be blocker, major, minor or info")
		}
	}
	if filter.Category != "" {
		if err := domain.ValidateCategory(filter.Category); err != nil {
			return GlobalIssueFilter{}, globalInvalid("category", "category must be defect, requirement_gap or other")
		}
	}
	if err := validateGlobalQuery(filter.Query); err != nil {
		return GlobalIssueFilter{}, err
	}
	if err := validateGlobalCursor(filter.Cursor); err != nil {
		return GlobalIssueFilter{}, err
	}
	filter.Limit = clampGlobalLimit(filter.Limit)
	return filter, nil
}

// normalizeGlobalReportFilter validates and bounds a global report filter.
func normalizeGlobalReportFilter(filter GlobalReportFilter) (GlobalReportFilter, error) {
	if filter.VersionID != nil && *filter.VersionID <= 0 {
		return GlobalReportFilter{}, globalInvalid("versionId", "versionId must be a positive id")
	}
	if filter.Status != "" && !domain.IsValidReportStatus(filter.Status) {
		return GlobalReportFilter{}, globalInvalid("status", "status must be draft, published or stale")
	}
	if err := validateGlobalCursor(filter.Cursor); err != nil {
		return GlobalReportFilter{}, err
	}
	filter.Limit = clampGlobalLimit(filter.Limit)
	return filter, nil
}

// validateGlobalQuery enforces the §7.3 title-search rune bound.
func validateGlobalQuery(query string) error {
	if utf8.RuneCountInString(query) > domain.MaxIssueQueryRunes {
		return globalInvalid("q", fmt.Sprintf("q must be at most %d characters", domain.MaxIssueQueryRunes))
	}
	return nil
}

// validateGlobalCursor accepts the empty first-page cursor and rejects anything
// the opaque cursor codec cannot decode.
func validateGlobalCursor(cursor string) error {
	if _, err := platform.DecodeCursor(cursor); err != nil {
		return globalInvalid("cursor", "cursor is not a valid pagination cursor")
	}
	return nil
}

// clampGlobalLimit applies the shared 1–100 page bound; invalid values fall back
// to the default instead of producing an unbounded SQL page.
func clampGlobalLimit(limit int) int {
	switch {
	case limit <= 0:
		return platform.DefaultLimit
	case limit > platform.MaxLimit:
		return platform.MaxLimit
	default:
		return limit
	}
}

// globalInvalid builds the 422 invalid_argument error of a rejected filter.
func globalInvalid(field, message string) *domain.Error {
	return domain.NewInvalid(field, domain.ReasonInvalidArgument, message)
}
