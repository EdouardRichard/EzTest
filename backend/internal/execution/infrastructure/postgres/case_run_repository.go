package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// caseRunColumnList is the shared column order of every case_run query and the
// order scanCaseRun expects.
const caseRunColumnList = `id, batch_id, version_id, case_node_id, case_revision,
	case_hash, case_snapshot, attempt, status, failure_class, failure_reason,
	summary, session_id, session_snapshot_id, agent_session_id, agent_run_id,
	retry_of_case_run_id, cleanup_required, interrupted_step_seq, started_at,
	finished_at, duration_ms, created_at, updated_at`

// CaseRunRepository implements application.CaseRunRepository on a pgx querier.
type CaseRunRepository struct {
	q querier
}

// NewCaseRunRepository returns a case-run repository backed by pool.
func NewCaseRunRepository(pool *pgxpool.Pool) application.CaseRunRepository {
	return &CaseRunRepository{q: pool}
}

var _ application.CaseRunRepository = (*CaseRunRepository)(nil)

// scanCaseRun scans one row in caseRunColumnList order and decodes the JSONB
// snapshot.
func scanCaseRun(row rowScanner) (domain.CaseRun, error) {
	var (
		run          domain.CaseRun
		snapshot     []byte
		failureClass *string
		sessionID    *int64
		snapshotID   *int64
		agentSession *int64
		agentRun     *int64
		retryOf      *int64
		interrupted  *int
		startedAt    *time.Time
		finishedAt   *time.Time
	)
	if err := row.Scan(
		&run.ID,
		&run.BatchID,
		&run.VersionID,
		&run.CaseNodeID,
		&run.CaseRevision,
		&run.CaseHash,
		&snapshot,
		&run.Attempt,
		&run.Status,
		&failureClass,
		&run.FailureReason,
		&run.Summary,
		&sessionID,
		&snapshotID,
		&agentSession,
		&agentRun,
		&retryOf,
		&run.CleanupRequired,
		&interrupted,
		&startedAt,
		&finishedAt,
		&run.DurationMS,
		&run.CreatedAt,
		&run.UpdatedAt,
	); err != nil {
		return domain.CaseRun{}, err
	}
	if err := json.Unmarshal(snapshot, &run.Snapshot); err != nil {
		return domain.CaseRun{}, fmt.Errorf("decode case snapshot: %w", err)
	}
	if failureClass != nil {
		class := domain.FailureClass(*failureClass)
		run.FailureClass = &class
	}
	run.SessionID = sessionID
	run.SessionSnapshotID = snapshotID
	run.AgentSessionID = agentSession
	run.AgentRunID = agentRun
	run.RetryOfCaseRunID = retryOf
	run.InterruptedStepSeq = interrupted
	run.StartedAt = startedAt
	run.FinishedAt = finishedAt
	return run, nil
}

// insertCaseRun inserts one run with its explicit id and returns the stored row.
// It is shared by CreateBatchWithRuns so the batch and its runs are one
// transaction.
func insertCaseRun(ctx context.Context, q querier, run domain.CaseRun) (domain.CaseRun, error) {
	snapshot, err := json.Marshal(run.Snapshot)
	if err != nil {
		return domain.CaseRun{}, fmt.Errorf("postgres: marshal case snapshot: %w", err)
	}
	stored, err := scanCaseRun(q.QueryRow(ctx, `
		INSERT INTO case_run
			(id, batch_id, version_id, case_node_id, case_revision, case_hash,
			 case_snapshot, attempt, status, failure_class, failure_reason, summary,
			 session_id, session_snapshot_id, agent_session_id, agent_run_id,
			 retry_of_case_run_id, cleanup_required, interrupted_step_seq,
			 started_at, finished_at, duration_ms, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $10, $11, $12,
		        $13, $14, $15, $16, $17, $18, $19, $20, $21, $22,
		        COALESCE($23, now()), COALESCE($24, now()))
		RETURNING `+caseRunColumnList,
		run.ID, run.BatchID, run.VersionID, run.CaseNodeID, run.CaseRevision,
		run.CaseHash, string(snapshot), run.Attempt, run.Status,
		nullableFailureClass(run.FailureClass), run.FailureReason, run.Summary,
		nullableInt(run.SessionID), nullableInt(run.SessionSnapshotID),
		nullableInt(run.AgentSessionID), nullableInt(run.AgentRunID),
		nullableInt(run.RetryOfCaseRunID), run.CleanupRequired,
		nullableInt(interruptedStepParam(run.InterruptedStepSeq)),
		nullableTime(run.StartedAt), nullableTime(run.FinishedAt), run.DurationMS,
		timeOrNow(run.CreatedAt), timeOrNow(run.UpdatedAt)))
	if err != nil {
		switch uniqueConstraint(err) {
		case "pk_case_run":
			return domain.CaseRun{}, fmt.Errorf("%w: case run %d already exists",
				application.ErrConflict, run.ID)
		case "uk_case_run_batch_case_attempt":
			return domain.CaseRun{}, fmt.Errorf(
				"%w: batch %d already has case %d attempt %d",
				application.ErrConflict, run.BatchID, run.CaseNodeID, run.Attempt)
		}
		return domain.CaseRun{}, fmt.Errorf("postgres: create case run: %w", err)
	}
	return stored, nil
}

// interruptedStepParam converts an optional int step number to an optional
// int64 binding value.
func interruptedStepParam(value *int) *int64 {
	if value == nil {
		return nil
	}
	step := int64(*value)
	return &step
}

// CreateCaseRun inserts one case run.
func (r *CaseRunRepository) CreateCaseRun(ctx context.Context, run domain.CaseRun) (domain.CaseRun, error) {
	return insertCaseRun(ctx, r.q, run)
}

// GetCaseRun returns one run of batchID.
func (r *CaseRunRepository) GetCaseRun(ctx context.Context, batchID, caseRunID int64) (domain.CaseRun, error) {
	run, err := scanCaseRun(r.q.QueryRow(ctx,
		`SELECT `+caseRunColumnList+` FROM case_run WHERE id = $1 AND batch_id = $2`,
		caseRunID, batchID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CaseRun{}, caseRunNotFound(caseRunID)
		}
		return domain.CaseRun{}, fmt.Errorf("postgres: get case run: %w", err)
	}
	return run, nil
}

// GetCaseRunForUpdate is GetCaseRun with a row lock, read inside one
// transaction.
func (r *CaseRunRepository) GetCaseRunForUpdate(ctx context.Context, batchID, caseRunID int64) (domain.CaseRun, error) {
	var run domain.CaseRun
	err := withTx(ctx, r.q, func(q querier) error {
		locked, err := scanCaseRun(q.QueryRow(ctx,
			`SELECT `+caseRunColumnList+` FROM case_run
			 WHERE id = $1 AND batch_id = $2 FOR UPDATE`, caseRunID, batchID))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return caseRunNotFound(caseRunID)
			}
			return fmt.Errorf("postgres: get case run for update: %w", err)
		}
		run = locked
		return nil
	})
	if err != nil {
		return domain.CaseRun{}, err
	}
	return run, nil
}

// ListCaseRuns returns one page newest first with an id keyset cursor.
func (r *CaseRunRepository) ListCaseRuns(ctx context.Context, batchID int64, status domain.CaseRunStatus, beforeID int64, limit int) ([]domain.CaseRun, bool, error) {
	if limit <= 0 {
		return nil, false, fmt.Errorf("postgres: list case runs: limit must be positive")
	}
	rows, err := r.q.Query(ctx, `
		SELECT `+caseRunColumnList+`
		FROM case_run
		WHERE batch_id = $1
		  AND ($2::text = '' OR status = $2)
		  AND ($3 = 0 OR id < $3)
		ORDER BY id DESC
		LIMIT $4`, batchID, status, beforeID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: list case runs: %w", err)
	}
	defer rows.Close()

	runs := make([]domain.CaseRun, 0, limit)
	for rows.Next() {
		run, err := scanCaseRun(rows)
		if err != nil {
			return nil, false, fmt.Errorf("postgres: scan case run: %w", err)
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("postgres: list case runs: %w", err)
	}
	hasMore := len(runs) > limit
	if hasMore {
		runs = runs[:limit]
	}
	return runs, hasMore, nil
}

// NextPending returns the next dispatchable pending run without claiming it.
// The deterministic order is snapshot priority P0..P3, then case_node_id
// ascending. FOR UPDATE SKIP LOCKED keeps a concurrent scheduler from blocking
// on a row it is already looking at.
func (r *CaseRunRepository) NextPending(ctx context.Context, batchID int64) (domain.CaseRun, error) {
	var run domain.CaseRun
	err := withTx(ctx, r.q, func(q querier) error {
		next, err := scanCaseRun(q.QueryRow(ctx, `
			SELECT `+caseRunColumnList+`
			FROM case_run
			WHERE batch_id = $1 AND status = 'pending'
			ORDER BY CASE case_snapshot->'node'->>'priority'
			           WHEN 'P0' THEN 0
			           WHEN 'P1' THEN 1
			           WHEN 'P2' THEN 2
			           WHEN 'P3' THEN 3
			           ELSE 4
			         END,
			         case_node_id ASC,
			         id ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1`, batchID))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return application.ErrNoPendingCaseRun
			}
			return fmt.Errorf("postgres: next pending case run: %w", err)
		}
		run = next
		return nil
	})
	if err != nil {
		return domain.CaseRun{}, err
	}
	return run, nil
}

// UpdateCaseRun rewrites every mutable execution column.
func (r *CaseRunRepository) UpdateCaseRun(ctx context.Context, run domain.CaseRun) (domain.CaseRun, error) {
	stored, err := scanCaseRun(r.q.QueryRow(ctx, `
		UPDATE case_run
		SET status = $2, failure_class = $3, failure_reason = $4, summary = $5,
		    session_id = $6, session_snapshot_id = $7, agent_session_id = $8,
		    agent_run_id = $9, retry_of_case_run_id = $10, cleanup_required = $11,
		    interrupted_step_seq = $12, started_at = $13, finished_at = $14,
		    duration_ms = $15, updated_at = COALESCE($16, now())
		WHERE id = $1
		RETURNING `+caseRunColumnList,
		run.ID, run.Status, nullableFailureClass(run.FailureClass),
		run.FailureReason, run.Summary, nullableInt(run.SessionID),
		nullableInt(run.SessionSnapshotID), nullableInt(run.AgentSessionID),
		nullableInt(run.AgentRunID), nullableInt(run.RetryOfCaseRunID),
		run.CleanupRequired, nullableInt(interruptedStepParam(run.InterruptedStepSeq)),
		nullableTime(run.StartedAt), nullableTime(run.FinishedAt), run.DurationMS,
		timeOrNow(run.UpdatedAt)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CaseRun{}, caseRunNotFound(run.ID)
		}
		if checkConstraint(err) == "ck_case_run_failure_class" {
			return domain.CaseRun{}, fmt.Errorf(
				"%w: case run %d failure_class %v is not allowed for status %s",
				application.ErrConflict, run.ID, run.FailureClass, run.Status)
		}
		return domain.CaseRun{}, fmt.Errorf("postgres: update case run: %w", err)
	}
	return stored, nil
}

// SetStatus applies one status CAS.
func (r *CaseRunRepository) SetStatus(ctx context.Context, caseRunID int64, from, to domain.CaseRunStatus, at time.Time) (domain.CaseRun, error) {
	stored, err := scanCaseRun(r.q.QueryRow(ctx, `
		UPDATE case_run
		SET status = $3,
		    started_at = CASE WHEN $3 = 'running' AND started_at IS NULL
		                      THEN $4 ELSE started_at END,
		    finished_at = CASE WHEN $3 IN ('passed', 'failed', 'blocked',
		                                   'inconclusive', 'skipped', 'interrupted')
		                       THEN COALESCE(finished_at, $4) ELSE finished_at END,
		    updated_at = $4
		WHERE id = $1 AND status = $2
		RETURNING `+caseRunColumnList,
		caseRunID, from, to, at))
	if err == nil {
		return stored, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.CaseRun{}, fmt.Errorf("postgres: set case run status: %w", err)
	}
	status, stateErr := r.caseRunState(ctx, caseRunID)
	if stateErr != nil {
		return domain.CaseRun{}, stateErr
	}
	if isTerminalCaseRunStatus(status) {
		return domain.CaseRun{}, domain.ErrCaseRunTerminal
	}
	return domain.CaseRun{}, fmt.Errorf("%w: case run %d is %s, not %s",
		application.ErrConflict, caseRunID, status, from)
}

// FinishCaseRun stores a terminal outcome with a CAS that requires the stored
// run to be running. An interrupted run is a resume checkpoint, not a verdict:
// recovery, an immediate pause and the recorder store it while the row leaves
// running, and only a resume may turn it back into pending. A late finish (a
// stale browser run or an agent submission after a pause) must therefore never
// overwrite it with a terminal outcome and erase its cleanup/interrupted-step
// checkpoint.
func (r *CaseRunRepository) FinishCaseRun(ctx context.Context, run domain.CaseRun) (domain.CaseRun, error) {
	if !isTerminalCaseRunStatus(run.Status) {
		return domain.CaseRun{}, fmt.Errorf("postgres: finish case run: status %s is not terminal", run.Status)
	}
	stored, err := scanCaseRun(r.q.QueryRow(ctx, `
		UPDATE case_run
		SET status = $2, failure_class = $3, failure_reason = $4, summary = $5,
		    finished_at = COALESCE($6, now()), duration_ms = $7,
		    cleanup_required = $8, interrupted_step_seq = $9, updated_at = now()
		WHERE id = $1 AND status = 'running'
		RETURNING `+caseRunColumnList,
		run.ID, run.Status, nullableFailureClass(run.FailureClass),
		run.FailureReason, run.Summary, nullableTime(run.FinishedAt),
		run.DurationMS, run.CleanupRequired,
		nullableInt(interruptedStepParam(run.InterruptedStepSeq))))
	if err == nil {
		return stored, nil
	}
	if checkConstraint(err) == "ck_case_run_failure_class" {
		return domain.CaseRun{}, fmt.Errorf(
			"%w: case run %d failure_class %v is not allowed for terminal status %s",
			application.ErrConflict, run.ID, run.FailureClass, run.Status)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.CaseRun{}, fmt.Errorf("postgres: finish case run: %w", err)
	}
	status, stateErr := r.caseRunState(ctx, run.ID)
	if stateErr != nil {
		return domain.CaseRun{}, stateErr
	}
	if isTerminalCaseRunStatus(status) {
		return domain.CaseRun{}, domain.ErrCaseRunTerminal
	}
	return domain.CaseRun{}, fmt.Errorf("%w: case run %d is %s and cannot be finished",
		application.ErrConflict, run.ID, status)
}

// caseRunState reads the current status of one run to classify a lost CAS.
func (r *CaseRunRepository) caseRunState(ctx context.Context, caseRunID int64) (domain.CaseRunStatus, error) {
	var status domain.CaseRunStatus
	err := r.q.QueryRow(ctx,
		`SELECT status FROM case_run WHERE id = $1`, caseRunID).Scan(&status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", caseRunNotFound(caseRunID)
		}
		return "", fmt.Errorf("postgres: read case run state: %w", err)
	}
	return status, nil
}

// isTerminalCaseRunStatus reports whether status ended the run.
func isTerminalCaseRunStatus(status domain.CaseRunStatus) bool {
	switch status {
	case domain.CaseRunStatusPassed, domain.CaseRunStatusFailed,
		domain.CaseRunStatusBlocked, domain.CaseRunStatusInconclusive,
		domain.CaseRunStatusSkipped, domain.CaseRunStatusInterrupted:
		return true
	default:
		return false
	}
}

// caseRunNotFound is the id-scoped not-found of the case-run reads and CAS
// fallbacks.
func caseRunNotFound(caseRunID int64) error {
	return fmt.Errorf("%w: case run %d", application.ErrCaseRunNotFound, caseRunID)
}
