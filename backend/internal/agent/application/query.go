package agentapp

import (
	"context"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// P6 todo limits (spec §4.3.2).
const (
	defaultTodoLimit = 50
	maxTodoLimit     = 200
)

// DeadWorkItemRow is one work_item in the dead state returned by the P6 read
// source. Title is the display title of the exhausted task.
type DeadWorkItemRow struct {
	ID        int64
	VersionID int64
	Title     string
	CreatedAt time.Time
}

// QueryReader is the agent context's P6 read-only data source for the work-item
// retry metric and the dead work_item todo feed. It is implemented outside the
// application layer; the application service normalizes the version scope and
// the todo limit.
type QueryReader interface {
	// CountTaskRetries returns work_items with attempts>1 (num) over work_items
	// with attempts>=1 (denom), for one version when versionID is non-nil and
	// globally otherwise.
	CountTaskRetries(ctx context.Context, versionID *int64) (num, denom int, err error)
	// ListDeadWorkItems returns work_items in the dead state, newest first.
	// work_item is owned by the agent context, so the execution reader obtains
	// these rows through this application read instead of SQL.
	ListDeadWorkItems(ctx context.Context, limit int) ([]DeadWorkItemRow, error)
}

// QueryService is the P6 read-only query port of the agent context.
type QueryService struct {
	reader QueryReader
}

// NewQueryService wires the agent read-only query port.
func NewQueryService(reader QueryReader) *QueryService {
	return &QueryService{reader: reader}
}

func (s *QueryService) requireReader() error {
	if s == nil || s.reader == nil {
		return platform.Unavailable("agent query reader is not configured")
	}
	return nil
}

// CountTaskRetries returns the task retry-rate counters (spec §5.2). A zero
// denominator is a valid observation.
func (s *QueryService) CountTaskRetries(ctx context.Context, versionID *int64) (int, int, error) {
	if err := s.requireReader(); err != nil {
		return 0, 0, err
	}
	if versionID != nil && *versionID <= 0 {
		return 0, 0, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	return s.reader.CountTaskRetries(ctx, versionID)
}

// ListDeadWorkItems returns dead work items for the admin-only dead_work_item
// todo group (spec §4.3.2). Callers must gate this on the admin role; the
// signature intentionally has no viewer because the workspace service never
// calls it for non-admins. limit defaults to 50 and is capped at 200.
func (s *QueryService) ListDeadWorkItems(ctx context.Context, limit int) ([]DeadWorkItemRow, error) {
	if err := s.requireReader(); err != nil {
		return nil, err
	}
	return s.reader.ListDeadWorkItems(ctx, normalizeTodoLimit(limit))
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
