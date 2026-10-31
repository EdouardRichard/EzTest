package executionpg

import (
	"context"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/jackc/pgx/v5"
)

const caseRunColumns = `id, wave_id, intent_id, case_version_id, status, attempts, failure_class,
	summary, started_at, finished_at, owner_instance, owner_executor_id, dispatch_epoch,
	dispatch_token, lease_until, paused_at, paused_by, pause_reason, created_at, updated_at`

// CaseRunRepository implements executionapp.CaseRunRepository.
type CaseRunRepository struct{ base }

// Get loads one case_run.
func (r *CaseRunRepository) Get(ctx context.Context, id int64) (execution.CaseRun, error) {
	ctx = contextOrBackground(ctx)
	run, err := scanCaseRun(r.pool.QueryRow(ctx, `SELECT `+caseRunColumns+` FROM case_run WHERE id = $1`, id))
	if err != nil {
		return execution.CaseRun{}, wrapDB("get case run", err)
	}
	return run, nil
}

// Create inserts one case_run.
func (r *CaseRunRepository) Create(ctx context.Context, caseRun execution.CaseRun) error {
	ctx = contextOrBackground(ctx)
	if err := r.insertCaseRun(ctx, caseRun.WaveID, caseRun); err != nil {
		return err
	}
	return nil
}

func (r *CaseRunRepository) insertCaseRun(ctx context.Context, waveID int64, caseRun execution.CaseRun) error {
	if waveID <= 0 {
		waveID = caseRun.WaveID
	}
	summary, err := encodeJSONObject("summary", caseRun.Summary)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO case_run (id, wave_id, intent_id, case_version_id, status, attempts,
			failure_class, summary, dispatch_epoch, dispatch_token)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10)
	`, r.insertID(caseRun.ID), waveID, caseRun.IntentID, caseRun.CaseVersionID,
		string(caseRun.Status), caseRun.Attempts, string(caseRun.FailureClass), summary,
		caseRun.DispatchEpoch, caseRun.DispatchToken)
	return wrapDB("insert case run", err)
}

// ListByWave returns one descending-ID page of a wave's case_runs.
func (r *CaseRunRepository) ListByWave(ctx context.Context, waveID int64, status string, limit int, beforeID *int64) ([]execution.CaseRun, bool, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT `+caseRunColumns+` FROM case_run
		WHERE wave_id = $1 AND ($2 = '' OR status = $2) AND ($3::BIGINT IS NULL OR id < $3)
		ORDER BY id DESC
		LIMIT $4
	`, waveID, status, beforeID, fetchLimit(limit))
	if err != nil {
		return nil, false, wrapDB("list case runs", err)
	}
	defer rows.Close()
	items, err := collectCaseRuns(rows)
	if err != nil {
		return nil, false, err
	}
	limit = effectiveLimit(limit)
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return items, hasMore, nil
}

// ListAllByWave returns every case_run of a wave.
func (r *CaseRunRepository) ListAllByWave(ctx context.Context, waveID int64) ([]execution.CaseRun, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT `+caseRunColumns+` FROM case_run WHERE wave_id = $1 ORDER BY id
	`, waveID)
	if err != nil {
		return nil, wrapDB("list all case runs", err)
	}
	defer rows.Close()
	return collectCaseRuns(rows)
}

// ListRunning returns every running case_run for quota accounting.
func (r *CaseRunRepository) ListRunning(ctx context.Context) ([]execution.CaseRun, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT `+caseRunColumns+` FROM case_run WHERE status = 'running' ORDER BY id
	`)
	if err != nil {
		return nil, wrapDB("list running case runs", err)
	}
	defer rows.Close()
	return collectCaseRuns(rows)
}

// Update persists a server-side state change without a dispatch fence.
func (r *CaseRunRepository) Update(ctx context.Context, caseRun execution.CaseRun) (bool, error) {
	ctx = contextOrBackground(ctx)
	summary, err := encodeJSONObject("summary", caseRun.Summary)
	if err != nil {
		return false, err
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE case_run SET status = $2, attempts = $3, failure_class = $4, summary = $5::jsonb,
			started_at = $6, finished_at = $7, owner_instance = $8, owner_executor_id = $9,
			dispatch_epoch = $10, dispatch_token = $11, lease_until = $12, paused_at = $13,
			paused_by = $14, pause_reason = $15, updated_at = now()
		WHERE id = $1
	`, caseRun.ID, string(caseRun.Status), caseRun.Attempts, string(caseRun.FailureClass), summary,
		caseRun.StartedAt, caseRun.FinishedAt, caseRun.OwnerInstance, caseRun.OwnerExecutorID,
		caseRun.DispatchEpoch, caseRun.DispatchToken, caseRun.LeaseUntil, caseRun.PausedAt,
		caseRun.PausedBy, caseRun.PauseReason)
	if err != nil {
		return false, wrapDB("update case run", err)
	}
	return tag.RowsAffected() > 0, nil
}

// UpdateCAS applies mutate under a row lock and only when the dispatch
// epoch/token still match and the run is not already terminal.
func (r *CaseRunRepository) UpdateCAS(ctx context.Context, id int64, epoch int64, token string, mutate func(*execution.CaseRun) error) (bool, error) {
	ctx = contextOrBackground(ctx)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, wrapDB("case run CAS: begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	run, err := scanCaseRun(tx.QueryRow(ctx, `SELECT `+caseRunColumns+` FROM case_run WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		return false, wrapDB("case run CAS: load", err)
	}
	if run.Status.Terminal() {
		return false, nil
	}
	if epoch > 0 && run.DispatchEpoch != epoch {
		return false, nil
	}
	if token != "" && run.DispatchToken != token {
		return false, nil
	}
	if mutate != nil {
		if err := mutate(&run); err != nil {
			return false, err
		}
	}
	summary, err := encodeJSONObject("summary", run.Summary)
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE case_run SET status = $2, attempts = $3, failure_class = $4, summary = $5::jsonb,
			started_at = $6, finished_at = $7, owner_instance = $8, owner_executor_id = $9,
			dispatch_epoch = $10, dispatch_token = $11, lease_until = $12, paused_at = $13,
			paused_by = $14, pause_reason = $15, updated_at = now()
		WHERE id = $1
	`, run.ID, string(run.Status), run.Attempts, string(run.FailureClass), summary,
		run.StartedAt, run.FinishedAt, run.OwnerInstance, run.OwnerExecutorID,
		run.DispatchEpoch, run.DispatchToken, run.LeaseUntil, run.PausedAt, run.PausedBy, run.PauseReason); err != nil {
		return false, wrapDB("case run CAS: update", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, wrapDB("case run CAS: commit", err)
	}
	return true, nil
}

// Transition applies a guarded status change with an optional dispatch fence.
func (r *CaseRunRepository) Transition(ctx context.Context, id int64, from []execution.CaseRunStatus, to execution.CaseRunStatus, epoch int64, token string) (bool, error) {
	ctx = contextOrBackground(ctx)
	tag, err := r.pool.Exec(ctx, `
		UPDATE case_run SET status = $3, updated_at = now()
		WHERE id = $1 AND status = ANY($2::text[])
		  AND ($4::BIGINT <= 0 OR dispatch_epoch = $4)
		  AND ($5 = '' OR dispatch_token = $5)
	`, id, statusStrings(from), string(to), epoch, token)
	if err != nil {
		return false, wrapDB("transition case run", err)
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	return false, r.existsOrNotFound(ctx, "case_run", id, "transition case run")
}

// ClaimLease renews the lease for the given dispatch epoch.
func (r *CaseRunRepository) ClaimLease(ctx context.Context, id int64, epoch int64, until time.Time) (bool, error) {
	ctx = contextOrBackground(ctx)
	tag, err := r.pool.Exec(ctx, `
		UPDATE case_run SET lease_until = $3, updated_at = now()
		WHERE id = $1 AND dispatch_epoch = $2 AND status IN ('running', 'paused')
	`, id, epoch, until)
	if err != nil {
		return false, wrapDB("claim case run lease", err)
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	return false, r.existsOrNotFound(ctx, "case_run", id, "claim case run lease")
}

// ListRunningByExecutor returns the running runs owned by one executor.
func (r *CaseRunRepository) ListRunningByExecutor(ctx context.Context, executorID int64) ([]execution.CaseRun, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT `+caseRunColumns+` FROM case_run
		WHERE status = 'running' AND owner_executor_id = $1
		ORDER BY id
	`, executorID)
	if err != nil {
		return nil, wrapDB("list running case runs by executor", err)
	}
	defer rows.Close()
	return collectCaseRuns(rows)
}

// LatestByCaseVersions returns the newest case_run per case_version of a
// version (coverage analysis).
func (r *CaseRunRepository) LatestByCaseVersions(ctx context.Context, versionID int64, caseVersionIDs []int64) (map[int64]execution.CaseRun, error) {
	ctx = contextOrBackground(ctx)
	result := map[int64]execution.CaseRun{}
	if len(caseVersionIDs) == 0 {
		return result, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT ON (run.case_version_id) `+prefixedColumns("run", caseRunColumns)+`
		FROM case_run run
		JOIN run_wave wave ON wave.id = run.wave_id
		WHERE wave.version_id = $1 AND run.case_version_id = ANY($2::BIGINT[])
		ORDER BY run.case_version_id, run.id DESC
	`, versionID, caseVersionIDs)
	if err != nil {
		return nil, wrapDB("latest case runs by case version", err)
	}
	defer rows.Close()
	for rows.Next() {
		run, err := scanCaseRun(rows)
		if err != nil {
			return nil, err
		}
		result[run.CaseVersionID] = run
	}
	return result, rows.Err()
}

func scanCaseRun(row rowScanner) (execution.CaseRun, error) {
	var run execution.CaseRun
	var status, failureClass string
	var summaryRaw []byte
	var ownerExecutorID, pausedBy *int64
	err := row.Scan(&run.ID, &run.WaveID, &run.IntentID, &run.CaseVersionID, &status, &run.Attempts,
		&failureClass, &summaryRaw, &run.StartedAt, &run.FinishedAt, &run.OwnerInstance,
		&ownerExecutorID, &run.DispatchEpoch, &run.DispatchToken, &run.LeaseUntil,
		&run.PausedAt, &pausedBy, &run.PauseReason, &run.CreatedAt, &run.UpdatedAt)
	if err != nil {
		return execution.CaseRun{}, err
	}
	run.Status = execution.CaseRunStatus(status)
	run.FailureClass = execution.FailureClass(failureClass)
	run.OwnerExecutorID = ownerExecutorID
	run.PausedBy = pausedBy
	if len(summaryRaw) > 0 {
		if err := decodeJSONObject("summary", summaryRaw, &run.Summary); err != nil {
			return execution.CaseRun{}, err
		}
	}
	return run, nil
}

func collectCaseRuns(rows pgx.Rows) ([]execution.CaseRun, error) {
	items := make([]execution.CaseRun, 0)
	for rows.Next() {
		run, err := scanCaseRun(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, run)
	}
	return items, rows.Err()
}

// SUTContextRepository implements executionapp.SUTContextRepository.
type SUTContextRepository struct{ base }

const sutContextColumns = `id, case_run_id, version_sut_id, account_role, account_id,
	browser_context_id, storage_state, lease_epoch, status, closed_at, created_at, updated_at`

// CreateSUTContext inserts one per-SUT executor context reservation.
func (r *SUTContextRepository) CreateSUTContext(ctx context.Context, item execution.CaseRunSUTContext) error {
	ctx = contextOrBackground(ctx)
	storageState, err := encodeNullableJSONObject("storage_state", item.StorageState)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO case_run_sut_context (id, case_run_id, version_sut_id, account_role,
			account_id, browser_context_id, storage_state, lease_epoch, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, COALESCE($10, now()), COALESCE($11, now()))
	`, r.insertID(item.ID), item.CaseRunID, item.VersionSUTID, item.AccountRole,
		item.AccountID, item.BrowserContextID, storageState, item.LeaseEpoch, string(item.Status),
		nullableTime(item.CreatedAt), nullableTime(item.UpdatedAt))
	return wrapDB("insert case run sut context", err)
}

// ListByCaseRun lists the SUT contexts of one case_run.
func (r *SUTContextRepository) ListByCaseRun(ctx context.Context, caseRunID int64) ([]execution.CaseRunSUTContext, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT `+sutContextColumns+` FROM case_run_sut_context
		WHERE case_run_id = $1 ORDER BY id
	`, caseRunID)
	if err != nil {
		return nil, wrapDB("list case run sut contexts", err)
	}
	defer rows.Close()
	items := make([]execution.CaseRunSUTContext, 0)
	for rows.Next() {
		item, err := scanSUTContext(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// UpdateCAS applies mutate to one SUT context under a row lock.
func (r *SUTContextRepository) UpdateCAS(ctx context.Context, id int64, mutate func(*execution.CaseRunSUTContext) error) (bool, error) {
	ctx = contextOrBackground(ctx)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, wrapDB("sut context CAS: begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	item, err := scanSUTContext(tx.QueryRow(ctx, `SELECT `+sutContextColumns+` FROM case_run_sut_context WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		return false, wrapDB("sut context CAS: load", err)
	}
	if mutate != nil {
		if err := mutate(&item); err != nil {
			return false, err
		}
	}
	storageState, err := encodeNullableJSONObject("storage_state", item.StorageState)
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE case_run_sut_context SET account_role = $2, account_id = $3,
			browser_context_id = $4, storage_state = $5::jsonb, lease_epoch = $6,
			status = $7, closed_at = $8, updated_at = now()
		WHERE id = $1
	`, item.ID, item.AccountRole, item.AccountID, item.BrowserContextID, storageState,
		item.LeaseEpoch, string(item.Status), item.ClosedAt); err != nil {
		return false, wrapDB("sut context CAS: update", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, wrapDB("sut context CAS: commit", err)
	}
	return true, nil
}

// ListStale returns contexts of non-running case_runs older than createdBefore.
func (r *SUTContextRepository) ListStale(ctx context.Context, createdBefore time.Time, limit int) ([]execution.CaseRunSUTContext, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT `+prefixedColumns("context", sutContextColumns)+`
		FROM case_run_sut_context context
		JOIN case_run run ON run.id = context.case_run_id
		WHERE context.status <> 'closed' AND context.created_at < $1 AND run.status <> 'running'
		ORDER BY context.id
		LIMIT $2
	`, createdBefore, effectiveLimit(limit))
	if err != nil {
		return nil, wrapDB("list stale sut contexts", err)
	}
	defer rows.Close()
	items := make([]execution.CaseRunSUTContext, 0)
	for rows.Next() {
		item, err := scanSUTContext(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ListAll returns every non-closed SUT context (orphan reconciliation).
func (r *SUTContextRepository) ListAll(ctx context.Context) ([]execution.CaseRunSUTContext, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT `+sutContextColumns+` FROM case_run_sut_context
		WHERE status <> 'closed' ORDER BY id
	`)
	if err != nil {
		return nil, wrapDB("list sut contexts", err)
	}
	defer rows.Close()
	items := make([]execution.CaseRunSUTContext, 0)
	for rows.Next() {
		item, err := scanSUTContext(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanSUTContext(row rowScanner) (execution.CaseRunSUTContext, error) {
	var item execution.CaseRunSUTContext
	var storageStateRaw []byte
	var accountID *int64
	var status string
	err := row.Scan(&item.ID, &item.CaseRunID, &item.VersionSUTID, &item.AccountRole,
		&accountID, &item.BrowserContextID, &storageStateRaw, &item.LeaseEpoch, &status,
		&item.ClosedAt, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return execution.CaseRunSUTContext{}, err
	}
	item.AccountID = accountID
	item.Status = execution.SUTContextStatus(status)
	if len(storageStateRaw) > 0 {
		var state map[string]any
		if err := decodeJSONObject("storage_state", storageStateRaw, &state); err != nil {
			return execution.CaseRunSUTContext{}, err
		}
		item.StorageState = state
	}
	return item, nil
}

func encodeNullableJSONObject(field string, value map[string]any) (any, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := encodeJSONObject(field, value)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}
