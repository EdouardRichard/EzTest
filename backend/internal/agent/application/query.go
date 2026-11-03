package agentapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// P6 todo limits (spec §4.3.2).
const (
	defaultTodoLimit = 50
	maxTodoLimit     = 200
)

// Version-scoped work_item read limits (0.2.1 contract: default 20, cap 100;
// the frozen Cursor/Limit parameters carry the same bounds).
const (
	defaultWorkItemReadLimit = 20
	maxWorkItemReadLimit     = 100
)

// WorkItemFilter is the user-supplied filter of the version-scoped work_item
// list. VersionID is mandatory; every other field narrows the page. The same
// struct is validated by the application service and by the HTTP layer so an
// invalid enum never reaches the read source.
type WorkItemFilter struct {
	VersionID int64
	Kind      string
	State     string
	CreatedBy string
	BeforeID  *int64
	Limit     int
}

// WorkItemQuery is the normalized form of WorkItemFilter handed to the read
// source. Limit is already bounded; BeforeID is nil on the first page.
type WorkItemQuery struct {
	VersionID int64
	Kind      string
	State     string
	CreatedBy string
	BeforeID  *int64
	Limit     int
}

// WorkItemLeaseView is the active lease projection of WorkItemView. The frozen
// contract exposes a nullable lease; it is absent when the item is not leased.
type WorkItemLeaseView struct {
	Owner     string
	ExpiresAt *time.Time
}

// WorkItemReadView is the frozen WorkItemView projection of one 0.2.1 job
// (spec §5.4). State is the API lifecycle value: a user-canceled row is stored
// failed with the cancel marker and is projected as cancelled, like the job
// control API. Target/Inputs/Expected/Budget/Result are JSON objects and never
// carry prompts or credentials (the producers only store references/summaries).
type WorkItemReadView struct {
	ID             int64
	VersionID      int64
	Kind           string
	Goal           string
	State          string
	Target         json.RawMessage
	Inputs         json.RawMessage
	Expected       json.RawMessage
	Budget         json.RawMessage
	DependsOn      []int64
	Lease          *WorkItemLeaseView
	Attempt        int
	Result         json.RawMessage
	CreatedBy      string
	IdempotencyKey *string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// DeadWorkItemRow is one work_item in the dead state returned by the P6 read
// source. Title is the display title of the exhausted task.
type DeadWorkItemRow struct {
	ID        int64
	VersionID int64
	Title     string
	CreatedAt time.Time
}

// QueryReader is the agent context's read-only data source for the P6 work-item
// metrics/todo feed and the 0.2.1 version-scoped work-item (job) reads. It is
// implemented outside the application layer; the application service normalizes
// the version scope, the filters and the limits.
type QueryReader interface {
	// CountTaskRetries returns work_items with attempts>1 (num) over work_items
	// with attempts>=1 (denom), for one version when versionID is non-nil and
	// globally otherwise.
	CountTaskRetries(ctx context.Context, versionID *int64) (num, denom int, err error)
	// ListDeadWorkItems returns work_items in the dead state, newest first.
	// work_item is owned by the agent context, so the execution reader obtains
	// these rows through this application read instead of SQL.
	ListDeadWorkItems(ctx context.Context, limit int) ([]DeadWorkItemRow, error)
	// ListWorkItems returns one version-scoped page of 0.2.1 work items
	// ordered by id DESC. Implementations fetch query.Limit+1 rows and report
	// hasMore, so the application never emits an unbounded read. Only rows
	// carrying a 0.2.1 kind are eligible (legacy rows have an empty kind).
	ListWorkItems(ctx context.Context, query WorkItemQuery) ([]agent.WorkItem, bool, error)
	// GetWorkItem loads one version-scoped 0.2.1 work item. A row of another
	// version or a missing row is agent.ErrNotFound.
	GetWorkItem(ctx context.Context, versionID, id int64) (agent.WorkItem, error)
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

// WorkItems returns one version-scoped page of the 0.2.1 job projections,
// newest first. filter.VersionID is mandatory and every enum filter is
// validated here, so a direct application caller cannot request an unbounded or
// foreign-version read.
func (s *QueryService) WorkItems(ctx context.Context, filter WorkItemFilter) ([]WorkItemReadView, bool, error) {
	query, err := normalizeWorkItemFilter(filter)
	if err != nil {
		return nil, false, err
	}
	if err := s.requireReader(); err != nil {
		return nil, false, err
	}
	items, hasMore, err := s.reader.ListWorkItems(ctx, query)
	if err != nil {
		return nil, false, err
	}
	views := make([]WorkItemReadView, 0, len(items))
	for _, item := range items {
		views = append(views, workItemReadView(item))
	}
	return views, hasMore, nil
}

// WorkItem returns one version-scoped work item. A row owned by another version
// or a missing row is a typed not_found, never a cross-version read.
func (s *QueryService) WorkItem(ctx context.Context, versionID, workItemID int64) (WorkItemReadView, error) {
	if versionID <= 0 {
		return WorkItemReadView{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if workItemID <= 0 {
		return WorkItemReadView{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	if err := s.requireReader(); err != nil {
		return WorkItemReadView{}, err
	}
	item, err := s.reader.GetWorkItem(ctx, versionID, workItemID)
	if err != nil {
		if errors.Is(err, agent.ErrNotFound) {
			return WorkItemReadView{}, platform.NotFound("work item")
		}
		return WorkItemReadView{}, err
	}
	return workItemReadView(item), nil
}

// ValidateWorkItemFilter checks the frozen enum filters of the work-item list
// before any read. It is exported so the HTTP layer can reject a bad query
// string without calling the application read, and the service repeats it for
// direct callers.
func ValidateWorkItemFilter(filter WorkItemFilter) error {
	if filter.VersionID <= 0 {
		return platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if filter.Kind != "" && !agent.ValidWorkItemKind(filter.Kind) {
		return platform.Invalid("kind", "must be one of explore.login, explore.page, explore.api, explore.coverage, explore.gap, design.cases, review.cases, execute.batch, analyze.failure, report.version, knowledge.propose")
	}
	if filter.State != "" && !validWorkItemStateFilter(filter.State) {
		return platform.Invalid("state", "must be one of open, claimed, running, waiting_human, done, failed, cancelled")
	}
	if filter.CreatedBy != "" && !agent.ValidWorkItemCreatedBy(filter.CreatedBy) {
		return platform.Invalid("created_by", "must be one of state_machine, planner, assistant, trigger")
	}
	if filter.BeforeID != nil && *filter.BeforeID <= 0 {
		return platform.Invalid("cursor", "must be a positive decimal ID")
	}
	return nil
}

func normalizeWorkItemFilter(filter WorkItemFilter) (WorkItemQuery, error) {
	filter.Kind = strings.TrimSpace(filter.Kind)
	filter.State = strings.TrimSpace(filter.State)
	filter.CreatedBy = strings.TrimSpace(filter.CreatedBy)
	if err := ValidateWorkItemFilter(filter); err != nil {
		return WorkItemQuery{}, err
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultWorkItemReadLimit
	}
	if limit > maxWorkItemReadLimit {
		limit = maxWorkItemReadLimit
	}
	return WorkItemQuery{
		VersionID: filter.VersionID,
		Kind:      filter.Kind,
		State:     filter.State,
		CreatedBy: filter.CreatedBy,
		BeforeID:  filter.BeforeID,
		Limit:     limit,
	}, nil
}

// workItemReadView projects one stored row onto the frozen WorkItemView. A
// canceled row is stored as failed with JobCanceledReason (the frozen schema has
// no cancelled status) and is projected as cancelled; a residual legacy dead
// row is reported as failed because the contract enum has no dead value.
func workItemReadView(item agent.WorkItem) WorkItemReadView {
	view := WorkItemReadView{
		ID:             item.ID,
		VersionID:      item.VersionID,
		Kind:           item.Kind,
		Goal:           item.Goal,
		State:          workItemReadState(item),
		Target:         jsonObject(item.Target),
		Inputs:         jsonObject(item.Inputs),
		Expected:       jsonObject(item.Expected),
		Budget:         jsonObject(item.Budget),
		DependsOn:      item.DependsOn,
		Attempt:        item.Attempts,
		Result:         jsonObject(item.Result),
		CreatedBy:      item.CreatedBy,
		IdempotencyKey: item.IdempotencyKey,
		CreatedAt:      item.CreatedAt,
		UpdatedAt:      item.UpdatedAt,
	}
	if item.ClaimedBy != nil || item.LeaseUntil != nil {
		lease := WorkItemLeaseView{ExpiresAt: item.LeaseUntil}
		if item.ClaimedBy != nil {
			lease.Owner = *item.ClaimedBy
		}
		view.Lease = &lease
	}
	return view
}

func workItemReadState(item agent.WorkItem) string {
	if isCanceledJob(item) {
		return JobStateCancelled
	}
	if item.Status == agent.WorkItemDead {
		return string(agent.WorkItemFailed)
	}
	return string(item.Status)
}

func validWorkItemStateFilter(state string) bool {
	switch state {
	case string(agent.WorkItemOpen), string(agent.WorkItemClaimed), string(agent.WorkItemRunning),
		string(agent.WorkItemWaitingHuman), string(agent.WorkItemDone), string(agent.WorkItemFailed),
		JobStateCancelled:
		return true
	default:
		return false
	}
}

// jsonObject keeps a stored JSON object intact and defaults a nil/empty column
// to an empty object, so the wire projection never emits null for the frozen
// object fields.
func jsonObject(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
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
