package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// assertionResultColumnList is the shared column order of every
// assertion_result query and the order scanAssertionResult expects.
const assertionResultColumnList = `id, batch_id, case_run_id, step_result_id,
	assertion_id, kind, target, expected, actual, verdict, failure_reason,
	created_at, updated_at`

// AssertionRepository implements application.AssertionRepository on a pgx
// querier.
type AssertionRepository struct {
	q querier
}

// NewAssertionRepository returns an assertion-result repository backed by pool.
func NewAssertionRepository(pool *pgxpool.Pool) application.AssertionRepository {
	return &AssertionRepository{q: pool}
}

var _ application.AssertionRepository = (*AssertionRepository)(nil)

// scanAssertionResult scans one row in assertionResultColumnList order.
func scanAssertionResult(row rowScanner) (domain.AssertionResult, error) {
	var (
		assertion    domain.AssertionResult
		stepResultID *int64
	)
	if err := row.Scan(
		&assertion.ID,
		&assertion.BatchID,
		&assertion.CaseRunID,
		&stepResultID,
		&assertion.AssertionID,
		&assertion.Kind,
		&assertion.Target,
		&assertion.Expected,
		&assertion.Actual,
		&assertion.Verdict,
		&assertion.FailureReason,
		&assertion.CreatedAt,
		&assertion.UpdatedAt,
	); err != nil {
		return domain.AssertionResult{}, err
	}
	assertion.StepResultID = stepResultID
	return assertion, nil
}

// Upsert inserts the assertion or updates the row with the same
// (case_run_id, assertion_id) in place. The stored id is preserved on update.
func (r *AssertionRepository) Upsert(ctx context.Context, assertion domain.AssertionResult) (domain.AssertionResult, error) {
	stored, err := scanAssertionResult(r.q.QueryRow(ctx, `
		INSERT INTO assertion_result
			(id, batch_id, case_run_id, step_result_id, assertion_id, kind, target,
			 expected, actual, verdict, failure_reason, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11,
		        COALESCE($12, now()), COALESCE($13, now()))
		ON CONFLICT (case_run_id, assertion_id) DO UPDATE
		SET step_result_id = EXCLUDED.step_result_id,
		    kind = EXCLUDED.kind,
		    target = EXCLUDED.target,
		    expected = EXCLUDED.expected,
		    actual = EXCLUDED.actual,
		    verdict = EXCLUDED.verdict,
		    failure_reason = EXCLUDED.failure_reason,
		    updated_at = EXCLUDED.updated_at
		RETURNING `+assertionResultColumnList,
		assertion.ID, assertion.BatchID, assertion.CaseRunID,
		nullableInt(assertion.StepResultID), assertion.AssertionID,
		assertion.Kind, assertion.Target, assertion.Expected, assertion.Actual,
		assertion.Verdict, assertion.FailureReason, timeOrNow(assertion.CreatedAt),
		timeOrNow(assertion.UpdatedAt)))
	if err != nil {
		switch uniqueConstraint(err) {
		case "pk_assertion_result":
			return domain.AssertionResult{}, fmt.Errorf(
				"%w: assertion result %d already exists",
				application.ErrConflict, assertion.ID)
		}
		return domain.AssertionResult{}, fmt.Errorf("postgres: upsert assertion result: %w", err)
	}
	return stored, nil
}

// ListByCaseRun returns the assertions of one case run ordered by id.
func (r *AssertionRepository) ListByCaseRun(ctx context.Context, caseRunID int64) ([]domain.AssertionResult, error) {
	rows, err := r.q.Query(ctx,
		`SELECT `+assertionResultColumnList+` FROM assertion_result
		 WHERE case_run_id = $1 ORDER BY id ASC`, caseRunID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list assertion results: %w", err)
	}
	defer rows.Close()

	assertions := make([]domain.AssertionResult, 0)
	for rows.Next() {
		assertion, err := scanAssertionResult(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan assertion result: %w", err)
		}
		assertions = append(assertions, assertion)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list assertion results: %w", err)
	}
	return assertions, nil
}
