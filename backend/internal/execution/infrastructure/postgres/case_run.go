package executionpg

import (
	"context"
	"errors"
	"fmt"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const caseRunColumns = `id, wave_id, intent_id, case_version_id, status, attempts, failure_class,
	summary, started_at, finished_at, owner_instance, owner_executor_id, dispatch_epoch,
	dispatch_token, lease_until, paused_at, paused_by, pause_reason, evidence_dropped, created_at, updated_at`

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
	if err := r.insertCaseRun(ctx, r.pool, caseRun.WaveID, caseRun); err != nil {
		return err
	}
	return nil
}

// CreateAndTransition creates the case_run and applies the guarded, fenced
// transition in ONE transaction: the intermediate process row is never visible
// to a concurrent scheduler tick, and a failed transition rolls the insert back
// (no orphan anchor). The transition keeps the from->to guard and the dispatch
// fence — not a born-terminal write.
func (r *CaseRunRepository) CreateAndTransition(ctx context.Context, caseRun execution.CaseRun, from []execution.CaseRunStatus, to execution.CaseRunStatus, epoch int64, token string) error {
	ctx = contextOrBackground(ctx)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return wrapDB("insert case run: begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := r.insertCaseRun(ctx, tx, caseRun.WaveID, caseRun); err != nil {
		return err
	}
	ok, err := transitionCaseRun(ctx, tx, caseRun.ID, from, to, epoch, token)
	if err != nil {
		return err
	}
	if !ok {
		return execution.ErrStateConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return wrapDB("insert case run: commit", err)
	}
	return nil
}

// sqlExecer is the Exec surface shared by the pool and a transaction, so the
// insert runs either standalone (Create) or inside CreateAndTransition's tx.
type sqlExecer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// transitionCaseRun is the ONE guarded, fenced case_run status-transition
// statement, shared by Transition (pool) and CreateAndTransition (tx) so the
// guard can never drift between the two copies (constitution art.5.3/5.4).
// It reports whether a row transitioned; the caller maps a miss to its own
// conflict semantics.
func transitionCaseRun(ctx context.Context, exec sqlExecer, id int64, from []execution.CaseRunStatus, to execution.CaseRunStatus, epoch int64, token string) (bool, error) {
	tag, err := exec.Exec(ctx, `
		UPDATE case_run SET status = $3, updated_at = now()
		WHERE id = $1 AND status = ANY($2::text[])
		  AND ($4::BIGINT <= 0 OR dispatch_epoch = $4)
		  AND ($5 = '' OR dispatch_token = $5)
	`, id, statusStrings(from), string(to), epoch, token)
	if err != nil {
		return false, wrapDB("transition case run", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (r *CaseRunRepository) insertCaseRun(ctx context.Context, exec sqlExecer, waveID int64, caseRun execution.CaseRun) error {
	if waveID <= 0 {
		waveID = caseRun.WaveID
	}
	summary, err := encodeJSONObject("summary", caseRun.Summary)
	if err != nil {
		return err
	}
	_, err = exec.Exec(ctx, `
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
		SELECT `+caseRunColumns+` FROM case_run WHERE status = $1 ORDER BY id
	`, string(execution.CaseRunRunning))
	if err != nil {
		return nil, wrapDB("list running case runs", err)
	}
	defer rows.Close()
	return collectCaseRuns(rows)
}

// Update persists a server-side state change without a dispatch fence. It can
// never move a row INTO or OUT OF a terminal status: the only allowed writes
// are a non-terminal -> non-terminal change or a same-status rewrite (summary/
// class refinement of an already-settled row). Terminal verdicts go through the
// single terminal entry — settlement via UpdateCAS, reclassification via
// ReclassifyTerminal, process states via the guarded Transition primitives
// (V-0022, constitution art.3.6). A refused terminal transition is a loud
// ErrStateConflict, never a silent no-op.
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
		WHERE id = $1 AND (status = $2 OR ($2 <> ALL($16::text[]) AND status <> ALL($16::text[])))
	`, caseRun.ID, string(caseRun.Status), caseRun.Attempts, string(caseRun.FailureClass), summary,
		caseRun.StartedAt, caseRun.FinishedAt, caseRun.OwnerInstance, caseRun.OwnerExecutorID,
		caseRun.DispatchEpoch, caseRun.DispatchToken, caseRun.LeaseUntil, caseRun.PausedAt,
		caseRun.PausedBy, caseRun.PauseReason, statusStrings(execution.TerminalStatuses()))
	if err != nil {
		return false, wrapDB("update case run", err)
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	// Distinguish a vanished row (false,nil — the historical contract) from a
	// refused terminal transition (loud conflict — the single-entry guard).
	var stored string
	err = r.pool.QueryRow(ctx, `SELECT status FROM case_run WHERE id = $1`, caseRun.ID).Scan(&stored)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, wrapDB("update case run: probe", err)
	}
	return false, fmt.Errorf("%w: case_run %d terminal status write bypasses the single terminal entry (stored %s, requested %s)",
		execution.ErrStateConflict, caseRun.ID, stored, caseRun.Status)
}

// ReclassifyTerminal applies mutate under a row lock ONLY to an already-terminal
// failed/blocked/inconclusive verdict row, and only when the result stays within
// that reclassifiable set (the domain CanReclassifyTo rule). It is the guarded,
// fenced repository primitive of the terminal->terminal reclassification mode of
// the single terminal entry (V-0022): the settlement CAS (UpdateCAS) keeps
// refusing terminal rows, so reclassification needs its own sanctioned guard.
// It returns false,nil when the row is not reclassifiable or the fence was lost
// (another writer won); an illegal target status is a loud ErrStateConflict.
func (r *CaseRunRepository) ReclassifyTerminal(ctx context.Context, id int64, epoch int64, token string, mutate func(*execution.CaseRun) error) (bool, error) {
	ctx = contextOrBackground(ctx)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, wrapDB("case run reclassify: begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	run, err := scanCaseRun(tx.QueryRow(ctx, `SELECT `+caseRunColumns+` FROM case_run WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		return false, wrapDB("case run reclassify: load", err)
	}
	if !run.Status.CanReclassifyTo(run.Status) {
		// Not a failed/blocked/inconclusive terminal verdict: nothing to reclassify.
		return false, nil
	}
	if epoch > 0 && run.DispatchEpoch != epoch {
		return false, nil
	}
	if token != "" && run.DispatchToken != token {
		return false, nil
	}
	priorStatus := run.Status
	if mutate != nil {
		if err := mutate(&run); err != nil {
			return false, err
		}
	}
	if !priorStatus.CanReclassifyTo(run.Status) {
		return false, fmt.Errorf("%w: case_run %d reclassification %s -> %s leaves the reclassifiable verdict set",
			execution.ErrStateConflict, id, priorStatus, run.Status)
	}
	summary, err := encodeJSONObject("summary", run.Summary)
	if err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE case_run SET status = $2, attempts = $3, failure_class = $4, summary = $5::jsonb,
			started_at = $6, finished_at = $7, owner_instance = $8, owner_executor_id = $9,
			dispatch_epoch = $10, dispatch_token = $11, lease_until = $12, paused_at = $13,
			paused_by = $14, pause_reason = $15, updated_at = now()
		WHERE id = $1 AND status = ANY($16::text[])
	`, run.ID, string(run.Status), run.Attempts, string(run.FailureClass), summary,
		run.StartedAt, run.FinishedAt, run.OwnerInstance, run.OwnerExecutorID,
		run.DispatchEpoch, run.DispatchToken, run.LeaseUntil, run.PausedAt, run.PausedBy, run.PauseReason,
		statusStrings(execution.ReclassifiableStatuses()))
	if err != nil {
		return false, wrapDB("case run reclassify: update", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return false, wrapDB("case run reclassify: commit", err)
	}
	return true, nil
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
	ok, err := transitionCaseRun(ctx, r.pool, id, from, to, epoch, token)
	if err != nil {
		return false, err
	}
	if ok {
		return true, nil
	}
	return false, r.existsOrNotFound(ctx, "case_run", id, "transition case run")
}

// ClaimLease renews the lease for the given dispatch epoch.
func (r *CaseRunRepository) ClaimLease(ctx context.Context, id int64, epoch int64, until time.Time) (bool, error) {
	ctx = contextOrBackground(ctx)
	tag, err := r.pool.Exec(ctx, `
		UPDATE case_run SET lease_until = $3, updated_at = now()
		WHERE id = $1 AND dispatch_epoch = $2 AND status = ANY($4::text[])
	`, id, epoch, until, statusStrings([]execution.CaseRunStatus{execution.CaseRunRunning, execution.CaseRunPaused}))
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
		WHERE status = $2 AND owner_executor_id = $1
		ORDER BY id
	`, executorID, string(execution.CaseRunRunning))
	if err != nil {
		return nil, wrapDB("list running case runs by executor", err)
	}
	defer rows.Close()
	return collectCaseRuns(rows)
}

// AddEvidenceDrop increments the run's evidence-drop fact and returns the new
// total. It is a single atomic UPDATE (no dispatch fence): a drop is an
// append-only observation, and a terminal capture must still be able to record
// one after the fence is gone.
func (r *CaseRunRepository) AddEvidenceDrop(ctx context.Context, id int64, delta int64) (int64, error) {
	ctx = contextOrBackground(ctx)
	if delta <= 0 {
		return 0, nil
	}
	var total int64
	err := r.pool.QueryRow(ctx, `
		UPDATE case_run SET evidence_dropped = evidence_dropped + $2, updated_at = now()
		WHERE id = $1
		RETURNING evidence_dropped
	`, id, delta).Scan(&total)
	if err != nil {
		return 0, wrapDB("add case run evidence drop", err)
	}
	return total, nil
}

// LatestByCaseVersions returns the newest non-superseded case_run per
// case_version of a version (coverage analysis). A superseded run is a retired
// process row, not a result: including it would let a cancelled wave erase the
// coverage of an earlier passed run (QA release REL-Q2).
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
		  AND run.status <> $3
		ORDER BY run.case_version_id, run.id DESC
	`, versionID, caseVersionIDs, string(execution.CaseRunSuperseded))
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
		&run.PausedAt, &pausedBy, &run.PauseReason, &run.EvidenceDropped, &run.CreatedAt, &run.UpdatedAt)
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

const sutContextColumns = `id, case_run_id, sut_id, account_role, account_id,
	session_id, executor_id, storage_state, session_fixture_id, artifact_cursor, lease_epoch, status, closed_at, created_at, updated_at`

// CreateSUTContext inserts one per-SUT executor session reservation.
func (r *SUTContextRepository) CreateSUTContext(ctx context.Context, item execution.CaseRunSUTContext) error {
	ctx = contextOrBackground(ctx)
	storageState, err := encodeNullableJSONObject("storage_state", item.StorageState)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO case_run_sut_context (id, case_run_id, sut_id, account_role,
			account_id, session_id, executor_id, storage_state, session_fixture_id, artifact_cursor, lease_epoch, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10, $11, $12, COALESCE($13, now()), COALESCE($14, now()))
	`, r.insertID(item.ID), item.CaseRunID, item.SUTID, item.AccountRole,
		item.AccountID, item.SessionID, item.ExecutorID, storageState, item.SessionFixtureID, item.ArtifactCursor, item.LeaseEpoch, string(item.Status),
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
			session_id = $4, executor_id = $5, storage_state = $6::jsonb, session_fixture_id = $7, artifact_cursor = $8, lease_epoch = $9,
			status = $10, closed_at = $11, updated_at = now()
		WHERE id = $1
	`, item.ID, item.AccountRole, item.AccountID, item.SessionID, item.ExecutorID, storageState,
		item.SessionFixtureID, item.ArtifactCursor, item.LeaseEpoch, string(item.Status), item.ClosedAt); err != nil {
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
		WHERE context.status <> $3 AND context.created_at < $1 AND run.status <> $4
		ORDER BY context.id
		LIMIT $2
	`, createdBefore, effectiveLimit(limit), string(execution.SUTContextClosed), string(execution.CaseRunRunning))
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
		WHERE status <> $1 ORDER BY id
	`, string(execution.SUTContextClosed))
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
	var accountID, executorID *int64
	var status string
	err := row.Scan(&item.ID, &item.CaseRunID, &item.SUTID, &item.AccountRole,
		&accountID, &item.SessionID, &executorID, &storageStateRaw, &item.SessionFixtureID, &item.ArtifactCursor, &item.LeaseEpoch, &status,
		&item.ClosedAt, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return execution.CaseRunSUTContext{}, err
	}
	item.AccountID = accountID
	item.ExecutorID = executorID
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
