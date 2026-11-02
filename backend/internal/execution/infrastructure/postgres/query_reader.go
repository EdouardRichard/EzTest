package executionpg

import (
	"context"
	"fmt"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DeadWorkItemLister is the agent context's application read surface consumed by
// the execution reader. work_item is owned by the agent context, so the reader
// never joins it: main injects the agent query service, which satisfies this
// interface.
type DeadWorkItemLister interface {
	ListDeadWorkItems(ctx context.Context, limit int) ([]agentapp.DeadWorkItemRow, error)
}

var _ DeadWorkItemLister = (*agentapp.QueryService)(nil)

var _ executionapp.QueryReader = (*QueryReader)(nil)

// QueryReader is the execution context's P6 read-only source. It reads its own
// run_wave/case_run/evidence/human_request tables and obtains dead work items
// through the injected agent application port. Version titles are resolved by
// the consumer through the version application port, never by a cross-context
// join.
type QueryReader struct {
	pool          *pgxpool.Pool
	deadWorkItems DeadWorkItemLister
}

// NewQueryReader wires the execution read source. A nil pool or missing agent
// port is a wiring error.
func NewQueryReader(pool *pgxpool.Pool, deadWorkItems DeadWorkItemLister) (*QueryReader, error) {
	if pool == nil {
		return nil, fmt.Errorf("execution query reader: nil pool")
	}
	if deadWorkItems == nil {
		return nil, fmt.Errorf("execution query reader: dead work item lister is not configured")
	}
	return &QueryReader{pool: pool, deadWorkItems: deadWorkItems}, nil
}

// CountExecutionPass returns passed finished case_runs over the terminal
// passed+failed+blocked+inconclusive case_runs, for one version when versionID
// is non-nil and globally otherwise.
func (r *QueryReader) CountExecutionPass(ctx context.Context, versionID *int64) (int, int, error) {
	var num, denom int
	if err := r.pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE cr.status = 'passed'),
			count(*) FILTER (WHERE cr.status IN ('passed', 'failed', 'blocked', 'inconclusive'))
		FROM case_run cr
		JOIN run_wave w ON w.id = cr.wave_id
		WHERE $1::bigint IS NULL OR w.version_id = $1
	`, versionID).Scan(&num, &denom); err != nil {
		return 0, 0, fmt.Errorf("count execution pass: %w", err)
	}
	return num, denom, nil
}

// CountEvidenceCompleteness returns finished case_runs carrying at least one
// evidence row over the same terminal denominator as CountExecutionPass.
func (r *QueryReader) CountEvidenceCompleteness(ctx context.Context, versionID *int64) (int, int, error) {
	var num, denom int
	if err := r.pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (
				WHERE EXISTS (SELECT 1 FROM evidence e WHERE e.case_run_id = cr.id)
			),
			count(*)
		FROM case_run cr
		JOIN run_wave w ON w.id = cr.wave_id
		WHERE cr.status IN ('passed', 'failed', 'blocked', 'inconclusive')
		  AND ($1::bigint IS NULL OR w.version_id = $1)
	`, versionID).Scan(&num, &denom); err != nil {
		return 0, 0, fmt.Errorf("count evidence completeness: %w", err)
	}
	return num, denom, nil
}

// ListOpenHumanRequests returns open human requests, newest first with login
// takeovers first. includeTakeover is false for non-admin viewers so
// login_takeover rows are never fetched. The title is the request reason,
// falling back to the kind label.
func (r *QueryReader) ListOpenHumanRequests(ctx context.Context, includeTakeover bool, limit int) ([]executionapp.HumanRequestRow, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT
			id,
			kind,
			version_id,
			COALESCE(
				NULLIF(payload->>'reason', ''),
				CASE kind
					WHEN 'login_takeover' THEN '登录接管'
					WHEN 'conflict' THEN '账号冲突'
					ELSE '人工请求'
				END
			),
			created_at,
			expires_at
		FROM human_request
		WHERE status = 'open'
		  AND ($1::boolean OR kind <> 'login_takeover')
		ORDER BY (kind = 'login_takeover') DESC, created_at DESC, id DESC
		LIMIT $2
	`, includeTakeover, limit)
	if err != nil {
		return nil, fmt.Errorf("list open human requests: %w", err)
	}
	defer rows.Close()

	items := make([]executionapp.HumanRequestRow, 0)
	for rows.Next() {
		var item executionapp.HumanRequestRow
		if err := rows.Scan(&item.ID, &item.Kind, &item.VersionID, &item.Title,
			&item.CreatedAt, &item.ExpiresAt); err != nil {
			return nil, fmt.Errorf("scan human request: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan human requests: %w", err)
	}
	return items, nil
}

// ListDeadWorkItems returns dead work items through the agent application port,
// projected onto the execution todo row. The execution reader never reads the
// agent work_item table.
func (r *QueryReader) ListDeadWorkItems(ctx context.Context, limit int) ([]executionapp.DeadWorkItemRow, error) {
	source, err := r.deadWorkItems.ListDeadWorkItems(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("list dead work items: %w", err)
	}
	items := make([]executionapp.DeadWorkItemRow, 0, len(source))
	for _, row := range source {
		items = append(items, executionapp.DeadWorkItemRow{
			ID:        row.ID,
			VersionID: row.VersionID,
			Title:     row.Title,
			CreatedAt: row.CreatedAt,
		})
	}
	return items, nil
}
