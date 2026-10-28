package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// ExecutionQueries exposes the few cross-aggregate reads the composition root
// needs but no single aggregate repository owns: the scheduler/recovery active
// batch queue, the agent-side case-run lookup (the agent knows only a case-run
// id, while CaseRunRepository reads are batch-scoped) and the evidence refs of
// recorded assertions. Keeping them in one small adapter avoids widening the
// repository ports that T12/T15 pinned.
type ExecutionQueries struct {
	q querier
}

// NewExecutionQueries returns the cross-aggregate query adapter.
func NewExecutionQueries(pool *pgxpool.Pool) *ExecutionQueries {
	return &ExecutionQueries{q: pool}
}

var (
	_ application.BatchQueue        = (*ExecutionQueries)(nil)
	_ application.CaseRunLocator    = (*ExecutionQueries)(nil)
	_ application.ResidualLeaseScan = (*ExecutionQueries)(nil)
)

// ActiveBatches returns the planned/running batches across every version,
// oldest first. Both the scheduler and crash recovery consume this view
// (spec §5.3, §5.4).
func (r *ExecutionQueries) ActiveBatches(ctx context.Context) ([]domain.RunBatch, error) {
	rows, err := r.q.Query(ctx,
		`SELECT `+batchColumnList+` FROM run_batch
		 WHERE status IN ('planned', 'running')
		 ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list active batches: %w", err)
	}
	defer rows.Close()

	batches := make([]domain.RunBatch, 0)
	for rows.Next() {
		batch, err := scanBatch(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan active batch: %w", err)
		}
		batches = append(batches, batch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list active batches: %w", err)
	}
	return batches, nil
}

// ResidualLeaseBatchIDs returns the distinct batches whose case runs already
// left running but still carry a session_id: the release write of a finished
// run was lost, or a crashed worker left the holder behind. It deliberately does
// not join run_batch, so a batch that already reached a terminal status (and
// therefore left the scheduler's active-batch queue) is still swept (spec §5.4,
// §5.12 invariant 7). Recovery.Sweep wires this scan through
// application.ResidualLeaseScan.
func (r *ExecutionQueries) ResidualLeaseBatchIDs(ctx context.Context) ([]int64, error) {
	rows, err := r.q.Query(ctx, `
		SELECT DISTINCT batch_id
		FROM case_run
		WHERE status <> 'running' AND session_id IS NOT NULL
		ORDER BY batch_id`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list residual lease batches: %w", err)
	}
	defer rows.Close()

	batchIDs := make([]int64, 0)
	for rows.Next() {
		var batchID int64
		if err := rows.Scan(&batchID); err != nil {
			return nil, fmt.Errorf("postgres: scan residual lease batch: %w", err)
		}
		batchIDs = append(batchIDs, batchID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list residual lease batches: %w", err)
	}
	return batchIDs, nil
}

// GetCaseRunByID resolves one case run by its id alone for the execution agent
// tools; a missing row is the same sentinel the batch-scoped read returns.
func (r *ExecutionQueries) GetCaseRunByID(ctx context.Context, caseRunID int64) (domain.CaseRun, error) {
	run, err := scanCaseRun(r.q.QueryRow(ctx,
		`SELECT `+caseRunColumnList+` FROM case_run WHERE id = $1`, caseRunID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CaseRun{}, caseRunNotFound(caseRunID)
		}
		return domain.CaseRun{}, fmt.Errorf("postgres: get case run by id: %w", err)
	}
	return run, nil
}

// ListByRef returns the evidence ids linked to the given result rows, grouped by
// result id and ordered by evidence id. It serves the case-run detail's
// per-assertion evidence ids (evidence_ref.ref_kind=assertion_result).
func (r *ExecutionQueries) ListByRef(ctx context.Context, refKind string, refIDs []int64) (map[int64][]int64, error) {
	refs := make(map[int64][]int64)
	if len(refIDs) == 0 {
		return refs, nil
	}
	rows, err := r.q.Query(ctx, `
		SELECT ref_id, evidence_id
		FROM evidence_ref
		WHERE ref_kind = $1 AND ref_id = ANY($2)
		ORDER BY ref_id, evidence_id`, refKind, refIDs)
	if err != nil {
		return nil, fmt.Errorf("postgres: list evidence refs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var refID, evidenceID int64
		if err := rows.Scan(&refID, &evidenceID); err != nil {
			return nil, fmt.Errorf("postgres: scan evidence ref: %w", err)
		}
		refs[refID] = append(refs[refID], evidenceID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list evidence refs: %w", err)
	}
	return refs, nil
}
