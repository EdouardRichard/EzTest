// Package chain adapts the execution context's case-run reads to the P8
// exploration-chain provider port (spec §5.2 query 6). The provider returns the
// complete result layer (one latest row per case node); request filters and
// truncation belong to the chain service.
package chain

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	chainapp "github.com/EdouardRichard/EzTest/backend/internal/chain/application"
)

// Reads implements chainapp.ExecutionProvider on the execution tables.
type Reads struct {
	pool *pgxpool.Pool
}

// New returns the execution-side chain reads provider.
func New(pool *pgxpool.Pool) *Reads {
	return &Reads{pool: pool}
}

var _ chainapp.ExecutionProvider = (*Reads)(nil)

// LatestResults returns one row per case node: the newest attempt of the case
// (attempt desc, then id desc) within the selected batch, or within every batch
// of the version when batchID is nil. The evidence count is aggregated in the
// same statement, so a result node never needs a second count query.
func (r *Reads) LatestResults(ctx context.Context, versionID int64, batchID *int64) ([]chainapp.ResultRow, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("execution chain reads: provider is not configured")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, case_node_id, batch_id, attempt, status, duration_ms,
		       COALESCE(failure_class, ''), summary, evidence_count
		FROM (
			SELECT DISTINCT ON (cr.case_node_id)
			       cr.id, cr.case_node_id, cr.batch_id, cr.attempt, cr.status,
			       cr.duration_ms, cr.failure_class, cr.summary,
			       (SELECT count(*) FROM evidence e WHERE e.case_run_id = cr.id) AS evidence_count
			FROM case_run cr
			WHERE cr.version_id = $1
			  AND ($2::bigint IS NULL OR cr.batch_id = $2)
			ORDER BY cr.case_node_id, cr.attempt DESC, cr.id DESC
		) latest
		ORDER BY case_node_id`, versionID, batchID)
	if err != nil {
		return nil, fmt.Errorf("execution chain reads: load latest results: %w", err)
	}
	defer rows.Close()

	results := make([]chainapp.ResultRow, 0)
	for rows.Next() {
		var row chainapp.ResultRow
		if err := rows.Scan(&row.ID, &row.CaseNodeID, &row.BatchID, &row.Attempt,
			&row.Status, &row.DurationMs, &row.FailureClass, &row.Summary,
			&row.EvidenceCount); err != nil {
			return nil, fmt.Errorf("execution chain reads: scan result: %w", err)
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("execution chain reads: load latest results: %w", err)
	}
	return results, nil
}
