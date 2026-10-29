// Package chain adapts the version context's requirement and batch reads to the
// P8 exploration-chain provider ports (spec §5.2 queries 1–2). Each method is a
// single SQL statement returning the complete version layer: request filters and
// truncation belong to the chain service, so this adapter never applies them.
package chain

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	chainapp "github.com/EdouardRichard/EzTest/backend/internal/chain/application"
)

// Reads implements chainapp.VersionProvider on the version context's tables.
type Reads struct {
	pool *pgxpool.Pool
}

// New returns the version-side chain reads provider.
func New(pool *pgxpool.Pool) *Reads {
	return &Reads{pool: pool}
}

var _ chainapp.VersionProvider = (*Reads)(nil)

// GetBatch resolves the selected batch of versionID in one query. The LATERAL
// join keeps the version row visible even when the version has no batch yet, so
// "missing version" and "missing batch" stay distinguishable:
//   - no version row                        → ErrNotFound{version}
//   - explicit batchID not owned by version → ErrNotFound{batch}
//   - no explicit batch and no batch row    → zero BatchRow, nil error
func (r *Reads) GetBatch(ctx context.Context, versionID int64, batchID *int64) (chainapp.BatchRow, error) {
	if r == nil || r.pool == nil {
		return chainapp.BatchRow{}, errors.New("version chain reads: provider is not configured")
	}
	var batch chainapp.BatchRow
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(b.id, 0), COALESCE(b.batch_no, 0),
		       COALESCE(b.kind, ''), COALESCE(b.status, '')
		FROM version v
		LEFT JOIN LATERAL (
			SELECT id, batch_no, kind, status
			FROM run_batch
			WHERE version_id = v.id
			  AND ($2::bigint IS NULL OR id = $2)
			ORDER BY created_at DESC, id DESC
			LIMIT 1
		) b ON true
		WHERE v.id = $1`, versionID, batchID).Scan(
		&batch.ID, &batch.BatchNo, &batch.Kind, &batch.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return chainapp.BatchRow{}, &chainapp.ErrNotFound{Resource: "version", ID: versionID}
	}
	if err != nil {
		return chainapp.BatchRow{}, fmt.Errorf("version chain reads: resolve batch: %w", err)
	}
	if batchID != nil && batch.ID == 0 {
		return chainapp.BatchRow{}, &chainapp.ErrNotFound{Resource: "batch", ID: *batchID}
	}
	return batch, nil
}

// Requirements returns the complete requirement layer of versionID in
// declaration order, with the open clarification-question count aggregated in
// the same statement (no per-row Count calls). Every status is returned: the
// chain service applies the query filters itself.
func (r *Reads) Requirements(ctx context.Context, versionID int64) ([]chainapp.RequirementRow, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("version chain reads: provider is not configured")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT r.id, r.title, r.status, r.priority,
		       (row_number() OVER (ORDER BY r.created_at, r.id))::int - 1 AS sort_order,
		       COALESCE(questions.open_count, 0)
		FROM requirement r
		LEFT JOIN (
			SELECT requirement_id, count(*) AS open_count
			FROM clarification_question
			WHERE version_id = $1 AND status = 'open' AND requirement_id IS NOT NULL
			GROUP BY requirement_id
		) questions ON questions.requirement_id = r.id
		WHERE r.version_id = $1
		ORDER BY r.created_at, r.id`, versionID)
	if err != nil {
		return nil, fmt.Errorf("version chain reads: load requirements: %w", err)
	}
	defer rows.Close()

	requirements := make([]chainapp.RequirementRow, 0)
	for rows.Next() {
		var row chainapp.RequirementRow
		if err := rows.Scan(&row.ID, &row.Title, &row.Status, &row.Priority,
			&row.SortOrder, &row.OpenQuestionCount); err != nil {
			return nil, fmt.Errorf("version chain reads: scan requirement: %w", err)
		}
		requirements = append(requirements, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("version chain reads: load requirements: %w", err)
	}
	return requirements, nil
}
