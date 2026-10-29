// Package chain adapts the delivery context's issue reads to the P8
// exploration-chain provider port (spec §5.2 query 8). The provider returns the
// complete issue layer (status/severity filtering belongs to the chain service).
package chain

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	chainapp "github.com/EdouardRichard/EzTest/backend/internal/chain/application"
)

// Reads implements chainapp.DeliveryProvider on the delivery issue tables.
type Reads struct {
	pool *pgxpool.Pool
}

// New returns the delivery-side chain reads provider.
func New(pool *pgxpool.Pool) *Reads {
	return &Reads{pool: pool}
}

var _ chainapp.DeliveryProvider = (*Reads)(nil)

// Issues returns every issue of versionID, scoped to batchID when one is given.
// Evidence and code-anchor references are counted with one grouped issue_ref
// subquery. The case node is COALESCE(issue.case_node_id, case_run.case_node_id),
// so an issue that only records its case_run still resolves its case — this is
// what lets the chain service degrade an older-attempt issue to case_issue
// (Task 6 deferred edge case) instead of leaving it unlinked.
func (r *Reads) Issues(ctx context.Context, versionID int64, batchID *int64) ([]chainapp.IssueRow, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("delivery chain reads: provider is not configured")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT i.id, i.title, i.severity, i.status, i.category, i.confidence,
		       i.case_run_id, COALESCE(i.case_node_id, cr.case_node_id),
		       COALESCE(refs.evidence_count, 0), COALESCE(refs.code_ref_count, 0),
		       i.created_at
		FROM issue i
		LEFT JOIN case_run cr ON cr.id = i.case_run_id
		LEFT JOIN (
			SELECT r.issue_id,
			       count(*) FILTER (WHERE r.ref_kind = 'evidence') AS evidence_count,
			       count(*) FILTER (WHERE r.ref_kind = 'code_anchor') AS code_ref_count
			FROM issue_ref r
			JOIN issue scoped ON scoped.id = r.issue_id
			WHERE scoped.version_id = $1
			  AND ($2::bigint IS NULL OR scoped.batch_id = $2)
			GROUP BY r.issue_id
		) refs ON refs.issue_id = i.id
		WHERE i.version_id = $1
		  AND ($2::bigint IS NULL OR i.batch_id = $2)
		ORDER BY i.id`, versionID, batchID)
	if err != nil {
		return nil, fmt.Errorf("delivery chain reads: load issues: %w", err)
	}
	defer rows.Close()

	issues := make([]chainapp.IssueRow, 0)
	for rows.Next() {
		var row chainapp.IssueRow
		if err := rows.Scan(&row.ID, &row.Title, &row.Severity, &row.Status,
			&row.Category, &row.Confidence, &row.CaseRunID, &row.CaseNodeID,
			&row.EvidenceCount, &row.CodeRefCount, &row.CreatedAt); err != nil {
			return nil, fmt.Errorf("delivery chain reads: scan issue: %w", err)
		}
		issues = append(issues, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("delivery chain reads: load issues: %w", err)
	}
	return issues, nil
}
