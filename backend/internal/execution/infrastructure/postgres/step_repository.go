package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// stepResultColumnList is the shared column order of every step_result query
// and the order scanStepResult expects.
const stepResultColumnList = `id, batch_id, case_run_id, seq, action, expected,
	actual, status, evidence_count, started_at, finished_at, created_at, updated_at`

// StepRepository implements application.StepRepository on a pgx querier.
type StepRepository struct {
	q querier
}

// NewStepRepository returns a step-result repository backed by pool.
func NewStepRepository(pool *pgxpool.Pool) application.StepRepository {
	return &StepRepository{q: pool}
}

var _ application.StepRepository = (*StepRepository)(nil)

// scanStepResult scans one row in stepResultColumnList order.
func scanStepResult(row rowScanner) (domain.StepResult, error) {
	var (
		step       domain.StepResult
		startedAt  *time.Time
		finishedAt *time.Time
	)
	if err := row.Scan(
		&step.ID,
		&step.BatchID,
		&step.CaseRunID,
		&step.Seq,
		&step.Action,
		&step.Expected,
		&step.Actual,
		&step.Status,
		&step.EvidenceCount,
		&startedAt,
		&finishedAt,
		&step.CreatedAt,
		&step.UpdatedAt,
	); err != nil {
		return domain.StepResult{}, err
	}
	step.StartedAt = startedAt
	step.FinishedAt = finishedAt
	return step, nil
}

// Upsert inserts the step or updates the row with the same (case_run_id, seq)
// in place. The stored id is preserved on update.
func (r *StepRepository) Upsert(ctx context.Context, step domain.StepResult) (domain.StepResult, error) {
	stored, err := scanStepResult(r.q.QueryRow(ctx, `
		INSERT INTO step_result
			(id, batch_id, case_run_id, seq, action, expected, actual, status,
			 evidence_count, started_at, finished_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11,
		        COALESCE($12, now()), COALESCE($13, now()))
		ON CONFLICT (case_run_id, seq) DO UPDATE
		SET action = EXCLUDED.action,
		    expected = EXCLUDED.expected,
		    actual = EXCLUDED.actual,
		    status = EXCLUDED.status,
		    evidence_count = EXCLUDED.evidence_count,
		    started_at = EXCLUDED.started_at,
		    finished_at = EXCLUDED.finished_at,
		    updated_at = EXCLUDED.updated_at
		RETURNING `+stepResultColumnList,
		step.ID, step.BatchID, step.CaseRunID, step.Seq, step.Action,
		step.Expected, step.Actual, step.Status, step.EvidenceCount,
		nullableTime(step.StartedAt), nullableTime(step.FinishedAt),
		timeOrNow(step.CreatedAt), timeOrNow(step.UpdatedAt)))
	if err != nil {
		switch uniqueConstraint(err) {
		case "pk_step_result":
			return domain.StepResult{}, fmt.Errorf("%w: step result %d already exists",
				application.ErrConflict, step.ID)
		}
		return domain.StepResult{}, fmt.Errorf("postgres: upsert step result: %w", err)
	}
	return stored, nil
}

// ListByCaseRun returns the steps of one case run ordered by seq.
func (r *StepRepository) ListByCaseRun(ctx context.Context, caseRunID int64) ([]domain.StepResult, error) {
	rows, err := r.q.Query(ctx,
		`SELECT `+stepResultColumnList+` FROM step_result
		 WHERE case_run_id = $1 ORDER BY seq ASC, id ASC`, caseRunID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list step results: %w", err)
	}
	defer rows.Close()

	steps := make([]domain.StepResult, 0)
	for rows.Next() {
		step, err := scanStepResult(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan step result: %w", err)
		}
		steps = append(steps, step)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list step results: %w", err)
	}
	return steps, nil
}
