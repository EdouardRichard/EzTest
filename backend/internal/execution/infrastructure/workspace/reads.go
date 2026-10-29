// Package workspace adapts execution's human-gate projection to the P8
// todo-centre provider port (spec §6.3).
//
// One statement returns the blocked(session) case runs of active batches that
// are still the latest attempt of their case. A run with a waiting manual
// takeover becomes a `takeover` item; a run without one is an unresolved
// `gate` that the administrator-driven recovery owns. The LEFT JOIN LATERAL
// reads the resource-owned manual_takeover row because that waiting state is
// exactly what the execution API surfaces as `case_run.pending_takeover`; the
// join keeps the endpoint at the spec's one-query budget instead of issuing a
// per-row lookup through another context's service. The workspace application
// never reads another context's tables itself.
package workspace

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	workspaceapp "github.com/EdouardRichard/EzTest/backend/internal/workspace/application"
)

// queryer is the narrow pgx surface of the provider. *pgxpool.Pool satisfies it;
// tests substitute a fake that serves canned rows.
type queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Reads implements workspaceapp.ExecutionProvider on the execution tables.
type Reads struct {
	q queryer
}

// New returns the execution-side todo provider.
func New(pool *pgxpool.Pool) *Reads {
	return &Reads{q: pool}
}

var _ workspaceapp.ExecutionProvider = (*Reads)(nil)

// PendingTakeovers returns the blocked(session) case runs waiting for a human
// decision. `takeover.id` is non-NULL exactly when a waiting manual takeover
// exists for the run's session.
func (r *Reads) PendingTakeovers(ctx context.Context) ([]workspaceapp.PendingRow, error) {
	if r == nil || r.q == nil {
		return nil, errors.New("execution workspace reads: provider is not configured")
	}
	rows, err := r.q.Query(ctx, `
		SELECT cr.id, cr.version_id, v.name,
		       COALESCE(cr.case_snapshot->'node'->>'title', ''),
		       COALESCE(cr.failure_reason, ''),
		       cr.created_at,
		       takeover.id
		FROM case_run cr
		JOIN run_batch b ON b.id = cr.batch_id
		JOIN version v ON v.id = cr.version_id
		LEFT JOIN LATERAL (
			SELECT t.id
			FROM manual_takeover t
			WHERE t.session_id = cr.session_id
			  AND t.status = 'waiting'
			ORDER BY t.requested_at DESC, t.id DESC
			LIMIT 1
		) takeover ON true
		WHERE cr.status = 'blocked'
		  AND cr.failure_class = 'session'
		  AND b.status IN ('planned', 'running')
		  AND NOT EXISTS (
			SELECT 1 FROM case_run retry WHERE retry.retry_of_case_run_id = cr.id
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM case_run newer
			WHERE newer.batch_id = cr.batch_id
			  AND newer.case_node_id = cr.case_node_id
			  AND newer.attempt > cr.attempt
		  )
		ORDER BY cr.created_at DESC, cr.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("execution workspace reads: pending takeovers: %w", err)
	}
	defer rows.Close()

	pending := make([]workspaceapp.PendingRow, 0)
	for rows.Next() {
		var row workspaceapp.PendingRow
		if err := rows.Scan(&row.CaseRunID, &row.VersionID, &row.VersionName, &row.CaseTitle,
			&row.FailureReason, &row.CreatedAt, &row.TakeoverID); err != nil {
			return nil, fmt.Errorf("execution workspace reads: scan pending takeover: %w", err)
		}
		pending = append(pending, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("execution workspace reads: pending takeovers: %w", err)
	}
	return pending, nil
}
