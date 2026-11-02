package executionpg

import (
	"context"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// ResultRepository implements the matching executionapp repository port.
type ResultRepository struct{ base }

const stepResultColumns = `id, case_run_id, seq, target_version_sut_id, status, observed,
	error, started_at, finished_at, created_at, updated_at`

const assertionResultColumns = `id, case_run_id, step_seq, assertion_seq, kind, verdict,
	reason, expected, actual, target_version_sut_id, created_at, updated_at`

// UpsertStep inserts or updates one step result fenced by the dispatch
// epoch/token. A stale fence returns false,nil and never overwrites the row.
func (r *ResultRepository) UpsertStep(ctx context.Context, result execution.StepResult, epoch int64, token string) (bool, error) {
	ctx = contextOrBackground(ctx)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, wrapDB("upsert step: begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fenceCaseRun(ctx, tx, result.CaseRunID, epoch, token); err != nil {
		if err == errFenceMismatch {
			return false, nil
		}
		return false, err
	}
	observed, err := encodeJSONObject("observed", result.Observed)
	if err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO step_result (id, case_run_id, seq, target_version_sut_id, status, observed,
			error, started_at, finished_at)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9)
		ON CONFLICT (case_run_id, seq) DO UPDATE SET
			target_version_sut_id = EXCLUDED.target_version_sut_id,
			status = EXCLUDED.status,
			observed = EXCLUDED.observed,
			error = EXCLUDED.error,
			started_at = EXCLUDED.started_at,
			finished_at = EXCLUDED.finished_at,
			updated_at = now()
	`, r.insertID(result.ID), result.CaseRunID, result.Seq, result.TargetVersionSUTID,
		string(result.Status), observed, result.Error, result.StartedAt, result.FinishedAt)
	if err != nil {
		return false, wrapDB("upsert step result", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, wrapDB("upsert step: commit", err)
	}
	return true, nil
}

// UpsertAssertion inserts or updates one assertion result fenced by the
// dispatch epoch/token.
func (r *ResultRepository) UpsertAssertion(ctx context.Context, result execution.AssertionResult, epoch int64, token string) (bool, error) {
	ctx = contextOrBackground(ctx)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, wrapDB("upsert assertion: begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fenceCaseRun(ctx, tx, result.CaseRunID, epoch, token); err != nil {
		if err == errFenceMismatch {
			return false, nil
		}
		return false, err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO assertion_result (id, case_run_id, step_seq, assertion_seq, kind, verdict,
			reason, expected, actual, target_version_sut_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (case_run_id, step_seq, assertion_seq) DO UPDATE SET
			kind = EXCLUDED.kind,
			verdict = EXCLUDED.verdict,
			reason = EXCLUDED.reason,
			expected = EXCLUDED.expected,
			actual = EXCLUDED.actual,
			target_version_sut_id = EXCLUDED.target_version_sut_id,
			updated_at = now()
	`, r.insertID(result.ID), result.CaseRunID, result.StepSeq, result.AssertionSeq,
		result.Kind, string(result.Verdict), result.Reason, result.Expected, result.Actual,
		result.TargetVersionSUTID)
	if err != nil {
		return false, wrapDB("upsert assertion result", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, wrapDB("upsert assertion: commit", err)
	}
	return true, nil
}

// ListSteps lists the step results of a case_run in execution order.
func (r *ResultRepository) ListSteps(ctx context.Context, caseRunID int64) ([]execution.StepResult, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT `+stepResultColumns+` FROM step_result
		WHERE case_run_id = $1 ORDER BY seq
	`, caseRunID)
	if err != nil {
		return nil, wrapDB("list step results", err)
	}
	defer rows.Close()
	items := make([]execution.StepResult, 0)
	for rows.Next() {
		var item execution.StepResult
		var status string
		var observedRaw []byte
		if err := rows.Scan(&item.ID, &item.CaseRunID, &item.Seq, &item.TargetVersionSUTID,
			&status, &observedRaw, &item.Error, &item.StartedAt, &item.FinishedAt,
			&item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.Status = execution.StepStatus(status)
		if len(observedRaw) > 0 {
			var observed map[string]any
			if err := decodeJSONObject("observed", observedRaw, &observed); err != nil {
				return nil, err
			}
			item.Observed = observed
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ListAssertions lists the assertion results of a case_run in case order.
func (r *ResultRepository) ListAssertions(ctx context.Context, caseRunID int64) ([]execution.AssertionResult, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT `+assertionResultColumns+` FROM assertion_result
		WHERE case_run_id = $1 ORDER BY step_seq, assertion_seq
	`, caseRunID)
	if err != nil {
		return nil, wrapDB("list assertion results", err)
	}
	defer rows.Close()
	items := make([]execution.AssertionResult, 0)
	for rows.Next() {
		var item execution.AssertionResult
		var verdict string
		if err := rows.Scan(&item.ID, &item.CaseRunID, &item.StepSeq, &item.AssertionSeq,
			&item.Kind, &verdict, &item.Reason, &item.Expected, &item.Actual,
			&item.TargetVersionSUTID, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.Verdict = execution.AssertionVerdict(verdict)
		items = append(items, item)
	}
	return items, rows.Err()
}
