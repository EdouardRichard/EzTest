package agentpg

import (
	"context"
	"fmt"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

var _ agentapp.QueryReader = (*QueryReader)(nil)

// QueryReader is the agent context's P6 read-only source for the work_item
// retry metric and the dead work_item todo feed. It never writes; the
// application layer normalizes the version scope and the todo limit.
type QueryReader struct {
	pool *pgxpool.Pool
}

// NewQueryReader wraps a pgx pool. A nil pool is a wiring error.
func NewQueryReader(pool *pgxpool.Pool) (*QueryReader, error) {
	if pool == nil {
		return nil, fmt.Errorf("agent query reader: nil pool")
	}
	return &QueryReader{pool: pool}, nil
}

// cancelMarkerExclusionSQL is the single place the Task 46 cancel marker
// becomes SQL. The frozen work_item schema has no 'cancelled' status, so a
// user-canceled job is stored as failed with attempts=max_attempts and
// last_error=JobCanceledReason: that attempts value is cancel bookkeeping, not
// retry history. The P6 retry rate and the dead-work-item todo feed both append
// this predicate (the caller passes the marker as parameter $argIndex) so a
// cancel can never be counted as a retried task or surface as retry-exhausted
// work. The marker is the discriminator, not the status, so a future reconcile
// that moves canceled rows to dead stays correct.
func cancelMarkerExclusionSQL(column string, argIndex int) string {
	return fmt.Sprintf("AND (%s IS DISTINCT FROM $%d)", column, argIndex)
}

// CountTaskRetries returns work_items with attempts>1 over work_items with
// attempts>=1, for one version when versionID is non-nil and globally
// otherwise. User-canceled jobs are excluded from both counters: cancel
// exhausts attempts as terminal bookkeeping, which must not inflate the metric.
func (r *QueryReader) CountTaskRetries(ctx context.Context, versionID *int64) (int, int, error) {
	var num, denom int
	if err := r.pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE attempts > 1),
			count(*) FILTER (WHERE attempts >= 1)
		FROM work_item
		WHERE ($1::bigint IS NULL OR version_id = $1)
		  `+cancelMarkerExclusionSQL("last_error", 2)+`
	`, versionID, agentapp.JobCanceledReason).Scan(&num, &denom); err != nil {
		return 0, 0, fmt.Errorf("count task retries: %w", err)
	}
	return num, denom, nil
}

// ListDeadWorkItems returns work_items in the dead state, newest first. The
// todo title marks the retry exhaustion of the task type so the workspace
// provider can render it without reading the agent tables. A dead row that
// still carries the Task 46 cancel marker is excluded: it is a canceled job
// (not retry-exhausted work), so the todo feed must not offer it.
func (r *QueryReader) ListDeadWorkItems(ctx context.Context, limit int) ([]agentapp.DeadWorkItemRow, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, version_id, type, created_at
		FROM work_item
		WHERE status = 'dead'
		  `+cancelMarkerExclusionSQL("last_error", 2)+`
		ORDER BY created_at DESC, id DESC
		LIMIT $1
	`, limit, agentapp.JobCanceledReason)
	if err != nil {
		return nil, fmt.Errorf("list dead work items: %w", err)
	}
	defer rows.Close()

	items := make([]agentapp.DeadWorkItemRow, 0)
	for rows.Next() {
		var item agentapp.DeadWorkItemRow
		var workType string
		if err := rows.Scan(&item.ID, &item.VersionID, &workType, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan dead work item: %w", err)
		}
		item.Title = workType + " 重试耗尽"
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan dead work items: %w", err)
	}
	return items, nil
}

// ListWorkItems returns one version-scoped page of 0.2.1 work items ordered by
// id DESC. Legacy rows (empty kind) are fenced out: they are dispatched by the
// pre-0.2.1 claim path and are not part of the frozen WorkItemView enum. A
// canceled row is stored failed with the cancel marker; the cancelled/failed
// state filters translate that marker so the API state and the stored status
// never disagree. The marker only reclassifies failed rows: every dead row
// projects as failed (the contract enum has no dead), so the failed filter must
// include dead regardless of its last_error. The reader fetches limit+1 rows so
// the application can report has_more without a COUNT query.
func (r *QueryReader) ListWorkItems(ctx context.Context, query agentapp.WorkItemQuery) ([]agent.WorkItem, bool, error) {
	if query.VersionID <= 0 {
		return nil, false, fmt.Errorf("list work items: version id must be positive")
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+workItemColumns+` FROM work_item
		WHERE version_id = $1
		  AND kind <> ''
		  AND ($2 = '' OR kind = $2)
		  AND (
		        $3 = ''
		     OR ($3 = 'cancelled' AND status = 'failed' AND last_error IS NOT DISTINCT FROM $6)
		     OR ($3 = 'failed' AND (
		             status = 'dead'
		          OR (status = 'failed' AND last_error IS DISTINCT FROM $6)
		        ))
		     OR ($3 NOT IN ('cancelled', 'failed') AND status = $3)
		  )
		  AND ($4 = '' OR created_by = $4)
		  AND ($5::bigint IS NULL OR id < $5)
		ORDER BY id DESC
		LIMIT $7
	`, query.VersionID, query.Kind, query.State, query.CreatedBy, query.BeforeID,
		agentapp.JobCanceledReason, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list work items: %w", err)
	}
	defer rows.Close()

	items := make([]agent.WorkItem, 0)
	for rows.Next() {
		item, err := scanWorkItem(rows)
		if err != nil {
			return nil, false, fmt.Errorf("scan work item: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("scan work items: %w", err)
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return items, hasMore, nil
}

// GetWorkItem loads one work item scoped to versionID. A row of another version
// and a missing row are both agent.ErrNotFound; legacy rows (empty kind) are
// fenced out like the list. The frozen work_item schema has no read that can
// cross versions, so a foreign ID can never leak another version's job.
func (r *QueryReader) GetWorkItem(ctx context.Context, versionID, id int64) (agent.WorkItem, error) {
	if versionID <= 0 || id <= 0 {
		return agent.WorkItem{}, fmt.Errorf("get work item: version id and id must be positive")
	}
	item, err := scanWorkItem(r.pool.QueryRow(ctx, `
		SELECT `+workItemColumns+` FROM work_item
		WHERE id = $1 AND version_id = $2 AND kind <> ''
	`, id, versionID))
	if err != nil {
		return agent.WorkItem{}, wrapDB("get work item", err)
	}
	return item, nil
}
