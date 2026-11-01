package deliveryapp

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// P6 global issue/report read limits (spec §4.3.5): limit 1–100, default 20,
// q 1–100 runes of the issue title.
const (
	globalDefaultLimit  = 20
	globalMaxLimit      = 100
	globalQueryMaxRunes = 100
)

// GlobalIssueFilter is one cross-version descending-ID issue page filter.
// VersionID/Status/Severity/Category/Q filter when set; Before is the exclusive
// ID cursor of the previous page.
type GlobalIssueFilter struct {
	VersionID *int64
	Status    string
	Severity  string
	Category  string
	Q         string
	Before    int64
	Limit     int
}

// GlobalReportFilter is one cross-version descending-ID report page filter.
// VersionID/Status/Kind filter when set; Before is the exclusive ID cursor.
type GlobalReportFilter struct {
	VersionID *int64
	Status    string
	Kind      string
	Before    int64
	Limit     int
}

// GlobalIssueView is one read-only global issue row: the owning issue, the
// version title resolved through the version application port and the stable
// version-workspace deep link.
type GlobalIssueView struct {
	Issue        delivery.Issue
	VersionTitle string
	URL          string
}

// GlobalReportRow is one report header with its current revision. The adapter
// returns it as a single row so the service never issues a per-report lookup.
type GlobalReportRow struct {
	Report          delivery.Report
	CurrentRevision *delivery.ReportRevision
}

// GlobalReportView is one read-only global report row: the report header, its
// current revision, the version title and the stable version-workspace deep
// link. Report content bytes and internal storage refs never cross this type.
type GlobalReportView struct {
	Report          delivery.Report
	CurrentRevision *delivery.ReportRevision
	VersionTitle    string
	URL             string
}

// GlobalIssueReader is the delivery context's cross-version issue read port.
// Implementations return rows in descending ID order and never filter by
// version scope themselves.
type GlobalIssueReader interface {
	ListGlobalIssues(ctx context.Context, filter GlobalIssueFilter) ([]delivery.Issue, error)
}

// GlobalReportReader is the delivery context's cross-version report read port.
type GlobalReportReader interface {
	ListGlobalReports(ctx context.Context, filter GlobalReportFilter) ([]GlobalReportRow, error)
}

// VersionTitleReader is the narrow version application port used to resolve
// version titles in one batch (spec §9: version_title through the version
// context, never another context's tables).
type VersionTitleReader interface {
	VersionTitles(ctx context.Context, ids []int64) (map[int64]string, error)
}

// WithGlobalReader attaches the cross-version issue read port and the version
// title resolver. It returns the service so the assembly root can chain it.
func (s *IssueService) WithGlobalReader(reader GlobalIssueReader, titles VersionTitleReader) *IssueService {
	if s != nil {
		s.global = reader
		s.titles = titles
	}
	return s
}

// WithGlobalReader attaches the cross-version report read port and the version
// title resolver. It returns the service so the assembly root can chain it.
func (s *ReportService) WithGlobalReader(reader GlobalReportReader, titles VersionTitleReader) *ReportService {
	if s != nil {
		s.global = reader
		s.titles = titles
	}
	return s
}

// ListGlobal returns one cross-version issue page and whether another page
// exists. Filters are validated before the read; titles are resolved in one
// batch for the returned rows only.
func (s *IssueService) ListGlobal(ctx context.Context, f GlobalIssueFilter) ([]GlobalIssueView, bool, error) {
	if s == nil || s.global == nil {
		return nil, false, platform.Unavailable("delivery global issue reader is not configured")
	}
	if s.titles == nil {
		return nil, false, platform.Unavailable("version title reader is not configured")
	}
	limit, err := validateGlobalIssueFilter(&f)
	if err != nil {
		return nil, false, err
	}
	// One extra row is the exact has_more probe; the adapter caps the page at
	// the shared maximum.
	query := f
	query.Limit = limit + 1
	issues, err := s.global.ListGlobalIssues(ctx, query)
	if err != nil {
		return nil, false, fmt.Errorf("list global issues: %w", err)
	}
	hasMore := len(issues) > limit
	if hasMore {
		issues = issues[:limit]
	}

	titles, err := s.resolveVersionTitles(ctx, issueVersionIDs(issues))
	if err != nil {
		return nil, false, err
	}
	views := make([]GlobalIssueView, 0, len(issues))
	for _, issue := range issues {
		views = append(views, GlobalIssueView{
			Issue:        issue,
			VersionTitle: titles[issue.VersionID],
			URL:          fmt.Sprintf("/versions/%d?tab=issues&issue=%d", issue.VersionID, issue.ID),
		})
	}
	return views, hasMore, nil
}

// ListGlobal returns one cross-version report page and whether another page
// exists. Each row carries its current revision; titles are resolved in one
// batch. Report content and internal storage refs are never loaded.
func (s *ReportService) ListGlobal(ctx context.Context, f GlobalReportFilter) ([]GlobalReportView, bool, error) {
	if s == nil || s.global == nil {
		return nil, false, platform.Unavailable("delivery global report reader is not configured")
	}
	if s.titles == nil {
		return nil, false, platform.Unavailable("version title reader is not configured")
	}
	limit, err := validateGlobalReportFilter(&f)
	if err != nil {
		return nil, false, err
	}
	query := f
	query.Limit = limit + 1
	rows, err := s.global.ListGlobalReports(ctx, query)
	if err != nil {
		return nil, false, fmt.Errorf("list global reports: %w", err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}

	titles, err := s.resolveVersionTitles(ctx, reportVersionIDs(rows))
	if err != nil {
		return nil, false, err
	}
	views := make([]GlobalReportView, 0, len(rows))
	for _, row := range rows {
		views = append(views, GlobalReportView{
			Report:          row.Report,
			CurrentRevision: row.CurrentRevision,
			VersionTitle:    titles[row.Report.VersionID],
			URL:             fmt.Sprintf("/versions/%d?tab=reports&report=%d", row.Report.VersionID, row.Report.ID),
		})
	}
	return views, hasMore, nil
}

// resolveVersionTitles batches the version title lookup and skips the read for
// an empty page.
func (s *IssueService) resolveVersionTitles(ctx context.Context, ids []int64) (map[int64]string, error) {
	if len(ids) == 0 {
		return map[int64]string{}, nil
	}
	titles, err := s.titles.VersionTitles(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("resolve version titles: %w", err)
	}
	if titles == nil {
		return map[int64]string{}, nil
	}
	return titles, nil
}

// resolveVersionTitles batches the version title lookup and skips the read for
// an empty page.
func (s *ReportService) resolveVersionTitles(ctx context.Context, ids []int64) (map[int64]string, error) {
	if len(ids) == 0 {
		return map[int64]string{}, nil
	}
	titles, err := s.titles.VersionTitles(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("resolve version titles: %w", err)
	}
	if titles == nil {
		return map[int64]string{}, nil
	}
	return titles, nil
}

// validateGlobalIssueFilter normalizes the limit and rejects malformed filters
// before any SQL runs.
func validateGlobalIssueFilter(f *GlobalIssueFilter) (int, error) {
	if f == nil {
		return 0, platform.Invalid("filter", "must not be nil")
	}
	limit, err := validateGlobalPage(f.VersionID, f.Before, f.Limit)
	if err != nil {
		return 0, err
	}
	if f.Status != "" && !delivery.IssueStatus(f.Status).Valid() {
		return 0, platform.Invalid("status", "must be one of open, confirmed, dismissed, merged")
	}
	if f.Category != "" && !validIssueFilterCategory(f.Category) {
		return 0, platform.Invalid("category", "must be one of ui, api, db")
	}
	if f.Severity != "" && !validIssueFilterSeverity(f.Severity) {
		return 0, platform.Invalid("severity", "must be one of major, minor, trivial")
	}
	q := strings.TrimSpace(f.Q)
	if utf8.RuneCountInString(q) > globalQueryMaxRunes {
		return 0, platform.Invalid("q", "must be at most 100 characters")
	}
	f.Q = q
	return limit, nil
}

// validateGlobalReportFilter normalizes the limit and rejects malformed
// filters before any SQL runs.
func validateGlobalReportFilter(f *GlobalReportFilter) (int, error) {
	if f == nil {
		return 0, platform.Invalid("filter", "must not be nil")
	}
	limit, err := validateGlobalPage(f.VersionID, f.Before, f.Limit)
	if err != nil {
		return 0, err
	}
	if f.Status != "" && !delivery.ReportStatus(f.Status).Valid() {
		return 0, platform.Invalid("status", "must be one of draft, published, stale")
	}
	if f.Kind != "" && !delivery.ReportKind(f.Kind).Valid() {
		return 0, platform.Invalid("kind", "must be one of wave, version")
	}
	return limit, nil
}

// validateGlobalPage applies the shared version/before/limit page rules.
func validateGlobalPage(versionID *int64, before int64, limit int) (int, error) {
	if versionID != nil && *versionID <= 0 {
		return 0, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if before < 0 {
		return 0, platform.Invalid("before", "must not be negative")
	}
	if limit < 0 {
		return 0, platform.Invalid("limit", "must not be negative")
	}
	if limit == 0 {
		return globalDefaultLimit, nil
	}
	if limit > globalMaxLimit {
		return globalMaxLimit, nil
	}
	return limit, nil
}

// issueVersionIDs returns the de-duplicated version IDs of one page in first
// seen order.
func issueVersionIDs(issues []delivery.Issue) []int64 {
	ids := make([]int64, 0, len(issues))
	seen := make(map[int64]struct{}, len(issues))
	for _, issue := range issues {
		if issue.VersionID <= 0 {
			continue
		}
		if _, ok := seen[issue.VersionID]; ok {
			continue
		}
		seen[issue.VersionID] = struct{}{}
		ids = append(ids, issue.VersionID)
	}
	return ids
}

// reportVersionIDs returns the de-duplicated version IDs of one page in first
// seen order.
func reportVersionIDs(rows []GlobalReportRow) []int64 {
	ids := make([]int64, 0, len(rows))
	seen := make(map[int64]struct{}, len(rows))
	for _, row := range rows {
		versionID := row.Report.VersionID
		if versionID <= 0 {
			continue
		}
		if _, ok := seen[versionID]; ok {
			continue
		}
		seen[versionID] = struct{}{}
		ids = append(ids, versionID)
	}
	return ids
}
