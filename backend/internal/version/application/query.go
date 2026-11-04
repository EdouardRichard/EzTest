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

/* ------------------- version-scoped facts / plan-item reads ------------------ */

// Version read limits (0.2.1 contract: default 20, cap 100, matching the frozen
// Cursor/Limit parameters).
const (
	defaultVersionReadLimit = 20
	maxVersionReadLimit     = 100
)

// FactReadFilter is the user-supplied filter of the version fact list. Kind is
// trimmed by the service; IncludeSuperseded widens the page to the append-only
// history (default false = current facts only). BeforeID is the cursor.
type FactReadFilter struct {
	Kind              string
	IncludeSuperseded bool
	BeforeID          *int64
	Limit             int
}

// FactReadQuery is the normalized form handed to the version read source.
type FactReadQuery struct {
	VersionID         int64
	Kind              string
	IncludeSuperseded bool
	BeforeID          *int64
	Limit             int
}

// FactReadRow is one version_fact row of a read page, including the append
// timestamp the domain entity does not carry.
type FactReadRow struct {
	ID           int64
	VersionID    int64
	Kind         string
	Key          string
	Payload      map[string]any
	Confidence   *float64
	Source       string
	SourceRef    string
	EvidenceIDs  []int64
	ContentHash  string
	SupersededBy *int64
	CreatedAt    time.Time
}

// FactReadView is the frozen FactView projection of one fact.
type FactReadView struct {
	ID           int64
	VersionID    int64
	Kind         string
	Key          string
	Payload      map[string]any
	Confidence   *float64
	Source       string
	SourceRef    string
	EvidenceIDs  []int64
	ContentHash  string
	SupersededBy *int64
	CreatedAt    time.Time
}

// PlanItemReadFilter is the user-supplied filter of the plan-item list. Status
// is trimmed by the service and empty means every status.
type PlanItemReadFilter struct {
	Status   string
	BeforeID *int64
	Limit    int
}

// PlanItemReadQuery is the normalized form handed to the version read source.
type PlanItemReadQuery struct {
	VersionID int64
	Status    string
	BeforeID  *int64
	Limit     int
}

// PlanItemReadRow is one version_plan_item row of a read page.
type PlanItemReadRow struct {
	ID         int64
	VersionID  int64
	Phase      string
	Title      string
	Status     string
	DependsOn  []int64
	WorkItemID *int64
	Decision   string
	Order      int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// PlanItemReadView is the frozen PlanItemView projection of one plan item.
type PlanItemReadView struct {
	ID         int64
	VersionID  int64
	Phase      string
	Title      string
	Status     string
	DependsOn  []int64
	WorkItemID *int64
	Decision   string
	Order      int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// VersionReadReader is the version context's read-only source for facts, plan
// items and the version existence probe. It is implemented outside the
// application layer over the version context's own tables; it never writes and
// every list method returns at most query.Limit+1 rows so the service can
// report has_more without a COUNT query.
type VersionReadReader interface {
	// VersionExists reports whether the version row exists. A missing version
	// makes the version-scoped read routes 404.
	VersionExists(ctx context.Context, versionID int64) (bool, error)
	// ListFacts returns one version's facts ordered by id DESC, optionally
	// including superseded rows.
	ListFacts(ctx context.Context, query FactReadQuery) ([]FactReadRow, bool, error)
	// ListPlanItems returns one version's plan items ordered by id DESC,
	// optionally filtered by status.
	ListPlanItems(ctx context.Context, query PlanItemReadQuery) ([]PlanItemReadRow, bool, error)
}

// VersionReadService is the read-only application port behind
// GET /api/versions/{id}/facts and /plan-items. It normalizes the cursor and
// limit, validates the version scope and never mutates state.
type VersionReadService struct {
	reader VersionReadReader
}

// NewVersionReadService wires the version read model.
func NewVersionReadService(reader VersionReadReader) *VersionReadService {
	return &VersionReadService{reader: reader}
}

func (s *VersionReadService) requireReader() error {
	if s == nil || s.reader == nil {
		return platform.Unavailable("version read model is not configured")
	}
	return nil
}

// VersionExists reports whether the version exists. The version-scoped read
// routes use it to turn a missing/foreign version into a 404 before any read.
func (s *VersionReadService) VersionExists(ctx context.Context, versionID int64) (bool, error) {
	if versionID <= 0 {
		return false, platform.Invalid("id", "must be a positive decimal ID")
	}
	if err := s.requireReader(); err != nil {
		return false, err
	}
	return s.reader.VersionExists(ctx, versionID)
}

// Facts returns one page of a version's facts, newest first. By default only
// the current facts (superseded_by IS NULL) are returned; IncludeSuperseded
// returns the whole append-only history.
func (s *VersionReadService) Facts(ctx context.Context, versionID int64, filter FactReadFilter) ([]FactReadView, bool, error) {
	if versionID <= 0 {
		return nil, false, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if filter.BeforeID != nil && *filter.BeforeID <= 0 {
		return nil, false, platform.Invalid("cursor", "must be a positive decimal ID")
	}
	if err := s.requireReader(); err != nil {
		return nil, false, err
	}
	rows, hasMore, err := s.reader.ListFacts(ctx, FactReadQuery{
		VersionID:         versionID,
		Kind:              strings.TrimSpace(filter.Kind),
		IncludeSuperseded: filter.IncludeSuperseded,
		BeforeID:          filter.BeforeID,
		Limit:             normalizeVersionReadLimit(filter.Limit),
	})
	if err != nil {
		return nil, false, err
	}
	views := make([]FactReadView, 0, len(rows))
	for _, row := range rows {
		views = append(views, FactReadView{
			ID:           row.ID,
			VersionID:    row.VersionID,
			Kind:         row.Kind,
			Key:          row.Key,
			Payload:      factPayload(row.Payload),
			Confidence:   row.Confidence,
			Source:       row.Source,
			SourceRef:    row.SourceRef,
			EvidenceIDs:  factEvidenceIDs(row.EvidenceIDs),
			ContentHash:  row.ContentHash,
			SupersededBy: row.SupersededBy,
			CreatedAt:    row.CreatedAt,
		})
	}
	return views, hasMore, nil
}

// PlanItems returns one page of a version's plan items, newest first. A status
// filter is trimmed and empty means every status; each view carries its
// depends_on list and work item link.
func (s *VersionReadService) PlanItems(ctx context.Context, versionID int64, filter PlanItemReadFilter) ([]PlanItemReadView, bool, error) {
	if versionID <= 0 {
		return nil, false, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if filter.BeforeID != nil && *filter.BeforeID <= 0 {
		return nil, false, platform.Invalid("cursor", "must be a positive decimal ID")
	}
	if err := s.requireReader(); err != nil {
		return nil, false, err
	}
	rows, hasMore, err := s.reader.ListPlanItems(ctx, PlanItemReadQuery{
		VersionID: versionID,
		Status:    strings.TrimSpace(filter.Status),
		BeforeID:  filter.BeforeID,
		Limit:     normalizeVersionReadLimit(filter.Limit),
	})
	if err != nil {
		return nil, false, err
	}
	views := make([]PlanItemReadView, 0, len(rows))
	for _, row := range rows {
		views = append(views, PlanItemReadView{
			ID:         row.ID,
			VersionID:  row.VersionID,
			Phase:      row.Phase,
			Title:      row.Title,
			Status:     row.Status,
			DependsOn:  planDependencyIDs(row.DependsOn),
			WorkItemID: row.WorkItemID,
			Decision:   row.Decision,
			Order:      row.Order,
			CreatedAt:  row.CreatedAt,
			UpdatedAt:  row.UpdatedAt,
		})
	}
	return views, hasMore, nil
}

func normalizeVersionReadLimit(limit int) int {
	if limit <= 0 {
		return defaultVersionReadLimit
	}
	if limit > maxVersionReadLimit {
		return maxVersionReadLimit
	}
	return limit
}

func factPayload(payload map[string]any) map[string]any {
	if payload == nil {
		return map[string]any{}
	}
	return payload
}

func factEvidenceIDs(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}

func planDependencyIDs(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}
