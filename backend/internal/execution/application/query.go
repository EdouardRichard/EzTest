package executionapp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// P6 todo limits (spec §4.3.2).
const (
	defaultTodoLimit = 50
	maxTodoLimit     = 200
)

// Todo kind and severity values produced by the execution context.
const (
	TodoKindHumanRequest = "human_request"
	TodoKindDeadWorkItem = "dead_work_item"

	SeverityWarning = "warning"
	SeverityMajor   = "major"
)

// HumanRequestTakeoverKind is the human_request kind that only admins may see
// in the todo feed (spec §4.3.2).
const HumanRequestTakeoverKind = "login_takeover"

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

// HumanRequestRow is one open human_request row returned by the P6 read
// source. Kind is the human_request kind (login_takeover/conflict/other).
type HumanRequestRow struct {
	ID           int64
	Kind         string
	VersionID    int64
	VersionTitle string
	Title        string
	CreatedAt    time.Time
	ExpiresAt    *time.Time
}

// DeadWorkItemRow is one dead work_item returned by the P6 read source.
type DeadWorkItemRow struct {
	ID           int64
	VersionID    int64
	VersionTitle string
	Title        string
	CreatedAt    time.Time
}

// QueryReader is the execution context's P6 read-only data source. It is
// implemented outside the application layer; dead work items are owned by the
// agent context, so the implementation crosses that boundary through its
// application port, never through SQL. The application service adds todo
// projection, limit normalization and the admin-only takeover filter.
type QueryReader interface {
	// CountExecutionPass returns passed finished case_runs (num) over finished
	// case_runs in passed+failed+blocked+inconclusive (denom), for one version
	// when versionID is non-nil and globally otherwise.
	CountExecutionPass(ctx context.Context, versionID *int64) (num, denom int, err error)
	// CountEvidenceCompleteness returns finished case_runs with at least one
	// evidence row (num) over the same denominator as CountExecutionPass.
	CountEvidenceCompleteness(ctx context.Context, versionID *int64) (num, denom int, err error)
	// ListOpenHumanRequests returns open human requests. includeTakeover is
	// false for non-admin viewers so login_takeover rows are never fetched.
	ListOpenHumanRequests(ctx context.Context, includeTakeover bool, limit int) ([]HumanRequestRow, error)
	// ListDeadWorkItems returns dead work items. work_item is owned by the
	// agent context, so the implementation must read it through the agent
	// application port, never through a cross-context SQL join.
	ListDeadWorkItems(ctx context.Context, limit int) ([]DeadWorkItemRow, error)
}

// QueryService is the P6 read-only query port of the execution context.
type QueryService struct {
	reader QueryReader
}

// NewQueryService wires the execution read-only query port.
func NewQueryService(reader QueryReader) *QueryService {
	return &QueryService{reader: reader}
}

func (s *QueryService) requireReader() error {
	if s == nil || s.reader == nil {
		return platform.Unavailable("execution query reader is not configured")
	}
	return nil
}

// CountExecutionPass returns the execution pass-rate counters (spec §5.2). A
// zero denominator is a valid observation.
func (s *QueryService) CountExecutionPass(ctx context.Context, versionID *int64) (int, int, error) {
	if err := s.requireReader(); err != nil {
		return 0, 0, err
	}
	if versionID != nil && *versionID <= 0 {
		return 0, 0, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	return s.reader.CountExecutionPass(ctx, versionID)
}

// CountEvidenceCompleteness returns the evidence completeness counters
// (spec §5.2). A zero denominator is a valid observation.
func (s *QueryService) CountEvidenceCompleteness(ctx context.Context, versionID *int64) (int, int, error) {
	if err := s.requireReader(); err != nil {
		return 0, 0, err
	}
	if versionID != nil && *versionID <= 0 {
		return 0, 0, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	return s.reader.CountEvidenceCompleteness(ctx, versionID)
}

// OpenHumanRequests returns open human requests as human_request todos. The
// login_takeover kind is admin-only: a non-admin viewer neither asks the reader
// for it nor receives it, even if a reader returns one by mistake.
func (s *QueryService) OpenHumanRequests(ctx context.Context, viewer platform.Actor, limit int) ([]TodoHit, error) {
	if err := s.requireReader(); err != nil {
		return nil, err
	}
	includeTakeover := viewer.IsAdmin()
	rows, err := s.reader.ListOpenHumanRequests(ctx, includeTakeover, normalizeTodoLimit(limit))
	if err != nil {
		return nil, err
	}
	items := make([]TodoHit, 0, len(rows))
	for _, row := range rows {
		if !includeTakeover && strings.TrimSpace(row.Kind) == HumanRequestTakeoverKind {
			continue
		}
		items = append(items, TodoHit{
			Kind:         TodoKindHumanRequest,
			Title:        row.Title,
			VersionID:    row.VersionID,
			VersionTitle: row.VersionTitle,
			Severity:     SeverityWarning,
			CreatedAt:    row.CreatedAt,
			ExpiresAt:    row.ExpiresAt,
			URL:          fmt.Sprintf("/versions/%d?tab=execution", row.VersionID),
		})
	}
	return items, nil
}

// DeadWorkItems returns dead work items as dead_work_item todos. Callers must
// gate this on the admin role (spec §4.3.2); the signature intentionally has no
// viewer because the workspace service never calls it for non-admins.
func (s *QueryService) DeadWorkItems(ctx context.Context, limit int) ([]TodoHit, error) {
	if err := s.requireReader(); err != nil {
		return nil, err
	}
	rows, err := s.reader.ListDeadWorkItems(ctx, normalizeTodoLimit(limit))
	if err != nil {
		return nil, err
	}
	items := make([]TodoHit, 0, len(rows))
	for _, row := range rows {
		items = append(items, TodoHit{
			Kind:         TodoKindDeadWorkItem,
			Title:        row.Title,
			VersionID:    row.VersionID,
			VersionTitle: row.VersionTitle,
			Severity:     SeverityMajor,
			CreatedAt:    row.CreatedAt,
			URL:          fmt.Sprintf("/versions/%d?tab=execution", row.VersionID),
		})
	}
	return items, nil
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
