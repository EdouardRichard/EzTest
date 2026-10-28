package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// AutonomyStore implements application.AutonomyBatchStore over run_batch and
// version (spec §6.2/§6.7). It is the persistence surface of the autonomous
// worker: every advance derives from a stored read plus one compare-and-swap,
// so a worker restart resumes the chain and a duplicate tick is harmless.
type AutonomyStore struct {
	q querier
}

// NewAutonomyStore returns the autonomy store backed by pool.
func NewAutonomyStore(pool *pgxpool.Pool) *AutonomyStore {
	return &AutonomyStore{q: pool}
}

var _ application.AutonomyBatchStore = (*AutonomyStore)(nil)

// autonomyBatchColumnList is batchColumnList qualified with the b alias: the
// autonomy scans join version, which also has an id column, so every batch column
// must be qualified.
const autonomyBatchColumnList = `b.id, b.version_id, b.batch_no, b.status, b.paused,
	b.pause_mode, b.pause_reason, b.baseline_hash, b.graph_hash, b.kind,
	b.parent_batch_id, b.base_fallback_reason, b.supplement_attempted,
	b.autonomy_done, b.code_baseline_commit, b.head_commit,
	b.case_count, b.passed_count, b.failed_count, b.blocked_count,
	b.inconclusive_count, b.skipped_count, b.interrupted_count, b.created_by,
	b.started_at, b.finished_at, b.failure_reason, b.created_at, b.updated_at`

// PendingAutonomyBatches returns the terminal batches whose chain is not
// settled, newest first. Paused versions and versions outside
// executing/continuing are skipped, matching the worker's skip rules so a stale
// scan never starts work for a paused/unrelated version.
func (s *AutonomyStore) PendingAutonomyBatches(ctx context.Context, limit int) ([]domain.RunBatch, error) {
	if limit <= 0 {
		return []domain.RunBatch{}, nil
	}
	rows, err := s.q.Query(ctx, `
		SELECT `+autonomyBatchColumnList+`
		FROM run_batch b
		JOIN version v ON v.id = b.version_id
		WHERE b.status IN ('done', 'failed', 'cancelled')
		  AND b.autonomy_done = false
		  AND v.status IN ('executing', 'continuing')
		  AND v.paused = false
		ORDER BY b.id DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list pending autonomy batches: %w", err)
	}
	defer rows.Close()

	batches := make([]domain.RunBatch, 0, limit)
	for rows.Next() {
		batch, err := scanBatch(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan pending autonomy batch: %w", err)
		}
		batches = append(batches, batch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list pending autonomy batches: %w", err)
	}
	return batches, nil
}

// DesigningVersions returns the continuing versions whose continue_phase is
// designing and that are not paused, each with its newest terminal batch (nil
// when the version never ran one). The newest batch is resolved by batch_no then
// id, so the incremental design run always points at the latest executed batch.
func (s *AutonomyStore) DesigningVersions(ctx context.Context, limit int) ([]application.DesigningVersion, error) {
	if limit <= 0 {
		return []application.DesigningVersion{}, nil
	}
	rows, err := s.q.Query(ctx, `
		SELECT `+versionRefColumns+`,
		       b.id, b.version_id, b.batch_no, b.status, b.paused, b.pause_mode,
		       b.pause_reason, b.baseline_hash, b.graph_hash, b.kind, b.parent_batch_id,
		       b.base_fallback_reason, b.supplement_attempted, b.autonomy_done,
		       b.code_baseline_commit, b.head_commit,
		       b.case_count, b.passed_count, b.failed_count, b.blocked_count,
		       b.inconclusive_count, b.skipped_count, b.interrupted_count,
		       b.created_by, b.started_at, b.finished_at, b.failure_reason,
		       b.created_at, b.updated_at
		FROM version v
		LEFT JOIN LATERAL (
			SELECT rb.*
			FROM run_batch rb
			WHERE rb.version_id = v.id
			  AND rb.status IN ('done', 'failed', 'cancelled')
			ORDER BY rb.batch_no DESC, rb.id DESC
			LIMIT 1
		) b ON true
		WHERE v.status = 'continuing'
		  AND v.continue_phase = 'designing'
		  AND v.paused = false
		ORDER BY v.id
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list designing versions: %w", err)
	}
	defer rows.Close()

	designing := make([]application.DesigningVersion, 0, limit)
	for rows.Next() {
		item, err := scanDesigningVersion(rows)
		if err != nil {
			return nil, err
		}
		designing = append(designing, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list designing versions: %w", err)
	}
	return designing, nil
}

// versionRefColumns is the version projection order scanDesigningVersion
// expects. It mirrors the execution version adapter's VersionRef.
const versionRefColumns = `v.id, v.status, v.paused, v.pause_mode,
	COALESCE(v.continue_phase, ''), v.batch_no, v.system_id, v.environment_id,
	v.code_baseline_commit`

// scanDesigningVersion scans one designing-version row: the version projection
// followed by an optional batch row (all NULL when the version has no terminal
// batch yet).
func scanDesigningVersion(row rowScanner) (application.DesigningVersion, error) {
	var (
		item               application.DesigningVersion
		pauseMode          string
		continuePhase      string
		systemID           *int64
		environmentID      *int64
		codeBaselineCommit *string
		batch              domain.RunBatch
		batchID            *int64
		batchVersionID     *int64
		batchNo            *int
		status             *string
		paused             *bool
		batchPauseMode     *string
		pauseReason        *string
		baselineHash       *string
		graphHash          *string
		kind               *string
		parentBatchID      *int64
		baseFallback       *string
		supplementTried    *bool
		autonomyDone       *bool
		codeCommit         *string
		headCommit         *string
		caseCount          *int
		passedCount        *int
		failedCount        *int
		blockedCount       *int
		inconclusiveCount  *int
		skippedCount       *int
		interruptedCount   *int
		createdBy          *int64
		createdAt          *time.Time
		updatedAt          *time.Time
		startedAt          *time.Time
		finishedAt         *time.Time
		failureReason      *string
	)
	if err := row.Scan(
		&item.Version.ID, &item.Version.Status, &item.Version.Paused, &pauseMode,
		&continuePhase, &item.Version.BatchNo, &systemID, &environmentID,
		&codeBaselineCommit,
		&batchID, &batchVersionID, &batchNo, &status, &paused, &batchPauseMode,
		&pauseReason, &baselineHash, &graphHash, &kind,
		&parentBatchID, &baseFallback, &supplementTried, &autonomyDone,
		&codeCommit, &headCommit, &caseCount, &passedCount, &failedCount,
		&blockedCount, &inconclusiveCount, &skippedCount, &interruptedCount,
		&createdBy, &startedAt, &finishedAt, &failureReason, &createdAt, &updatedAt,
	); err != nil {
		return application.DesigningVersion{}, fmt.Errorf("postgres: scan designing version: %w", err)
	}
	item.Version.PauseMode = pauseMode
	item.Version.ContinuePhase = continuePhase
	item.Version.SystemID = systemID
	item.Version.EnvironmentID = environmentID
	item.Version.CodeBaselineCommit = codeBaselineCommit
	if batchID == nil {
		return item, nil
	}
	batch.ID = *batchID
	if batchVersionID != nil {
		batch.VersionID = *batchVersionID
	}
	if batchNo != nil {
		batch.BatchNo = *batchNo
	}
	if status != nil {
		batch.Status = domain.BatchStatus(*status)
	}
	if paused != nil {
		batch.Paused = *paused
	}
	if batchPauseMode != nil {
		batch.PauseMode = domain.PauseMode(*batchPauseMode)
	}
	if pauseReason != nil {
		batch.PauseReason = *pauseReason
	}
	if baselineHash != nil {
		batch.BaselineHash = *baselineHash
	}
	if graphHash != nil {
		batch.GraphHash = *graphHash
	}
	if kind != nil {
		batch.Kind = domain.BatchKind(*kind)
	}
	batch.ParentBatchID = parentBatchID
	if baseFallback != nil {
		batch.BaseFallbackReason = *baseFallback
	}
	if supplementTried != nil {
		batch.SupplementAttempted = *supplementTried
	}
	if autonomyDone != nil {
		batch.AutonomyDone = *autonomyDone
	}
	batch.CodeBaselineCommit = codeCommit
	batch.HeadCommit = headCommit
	if caseCount != nil {
		batch.CaseCount = *caseCount
	}
	if passedCount != nil {
		batch.PassedCount = *passedCount
	}
	if failedCount != nil {
		batch.FailedCount = *failedCount
	}
	if blockedCount != nil {
		batch.BlockedCount = *blockedCount
	}
	if inconclusiveCount != nil {
		batch.InconclusiveCount = *inconclusiveCount
	}
	if skippedCount != nil {
		batch.SkippedCount = *skippedCount
	}
	if interruptedCount != nil {
		batch.InterruptedCount = *interruptedCount
	}
	if createdBy != nil {
		batch.CreatedBy = *createdBy
	}
	batch.StartedAt = startedAt
	batch.FinishedAt = finishedAt
	if failureReason != nil {
		batch.FailureReason = *failureReason
	}
	if createdAt != nil {
		batch.CreatedAt = *createdAt
	}
	if updatedAt != nil {
		batch.UpdatedAt = *updatedAt
	}
	item.Parent = &batch
	return item, nil
}

// MarkSupplementAttempted is the per-batch one-cycle CAS of spec §6.2 step 5:
// the supplement cycle runs at most once per batch. won reports whether this
// call performed the transition.
func (s *AutonomyStore) MarkSupplementAttempted(ctx context.Context, batchID int64) (bool, error) {
	return s.markFlag(ctx, "supplement_attempted", batchID)
}

// MarkAutonomyDone is the chain-settled CAS of spec §6.2 step 6. A lost CAS
// means another advance already settled the batch.
func (s *AutonomyStore) MarkAutonomyDone(ctx context.Context, batchID int64) (bool, error) {
	return s.markFlag(ctx, "autonomy_done", batchID)
}

// ResetAutonomyDone reopens a settled chain (autonomy_done true -> false). The
// manual-analysis handler calls the worker wrapper after it starts a new
// failure analysis on a completed chain so the worker re-enters the batch.
// supplement_attempted deliberately stays true: the post-batch supplement cycle
// already ran for this batch and a re-analysis must not run it twice.
func (s *AutonomyStore) ResetAutonomyDone(ctx context.Context, batchID int64) (bool, error) {
	var id int64
	err := s.q.QueryRow(ctx,
		`UPDATE run_batch SET autonomy_done = false, updated_at = now()
		 WHERE id = $1 AND autonomy_done = true
		 RETURNING id`, batchID).Scan(&id)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("postgres: reset run batch autonomy_done: %w", err)
	}
	var exists bool
	if scanErr := s.q.QueryRow(ctx,
		`SELECT true FROM run_batch WHERE id = $1`, batchID).Scan(&exists); scanErr != nil {
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return false, batchNotFound(batchID)
		}
		return false, fmt.Errorf("postgres: check run batch %d: %w", batchID, scanErr)
	}
	// The row exists but the flag was already false: a plain retry, nothing reset.
	return false, nil
}

// VersionsAwaitingFinalize is the DB-derived finalize sweep: executing or
// continuing unpaused versions whose every batch is terminal and settled
// (autonomy_done=true) with at least one batch, and that have no active batch.
// It repairs the crash window between a batch's autonomy_done CAS and the
// version TryFinalize, which the pending-batch scan cannot observe. TryFinalize
// re-checks every condition and is idempotent.
func (s *AutonomyStore) VersionsAwaitingFinalize(ctx context.Context, limit int) ([]int64, error) {
	if limit <= 0 {
		return []int64{}, nil
	}
	rows, err := s.q.Query(ctx, `
		SELECT v.id
		FROM version v
		WHERE v.status IN ('executing', 'continuing')
		  AND v.paused = false
		  AND EXISTS (
			SELECT 1 FROM run_batch b WHERE b.version_id = v.id)
		  AND NOT EXISTS (
			SELECT 1 FROM run_batch b
			WHERE b.version_id = v.id AND b.status IN ('planned', 'running'))
		  AND NOT EXISTS (
			SELECT 1 FROM run_batch b
			WHERE b.version_id = v.id
			  AND b.status IN ('done', 'failed', 'cancelled')
			  AND b.autonomy_done = false)
		ORDER BY v.id
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list versions awaiting finalize: %w", err)
	}
	defer rows.Close()

	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("postgres: scan version awaiting finalize: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list versions awaiting finalize: %w", err)
	}
	return ids, nil
}

// markFlag performs one boolean false -> true CAS and reports whether this call
// won. column is a package constant, never caller input, so it is safe to
// interpolate.
func (s *AutonomyStore) markFlag(ctx context.Context, column string, batchID int64) (bool, error) {
	var id int64
	err := s.q.QueryRow(ctx,
		`UPDATE run_batch SET `+column+` = true, updated_at = now()
		 WHERE id = $1 AND `+column+` = false
		 RETURNING id`, batchID).Scan(&id)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("postgres: mark run batch %s: %w", column, err)
	}
	var exists bool
	if scanErr := s.q.QueryRow(ctx,
		`SELECT true FROM run_batch WHERE id = $1`, batchID).Scan(&exists); scanErr != nil {
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return false, batchNotFound(batchID)
		}
		return false, fmt.Errorf("postgres: check run batch %d: %w", batchID, scanErr)
	}
	// The row exists but the flag was already true: another advance won.
	return false, nil
}

// SupplementChild returns the supplement batch whose parent is parentBatchID, or
// nil when none exists. The single-active-batch index means at most one exists.
func (s *AutonomyStore) SupplementChild(ctx context.Context, parentBatchID int64) (*domain.RunBatch, error) {
	batch, err := scanBatch(s.q.QueryRow(ctx, `
		SELECT `+batchColumnList+`
		FROM run_batch
		WHERE kind = 'supplement' AND parent_batch_id = $1
		ORDER BY id DESC
		LIMIT 1`, parentBatchID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: read supplement child of batch %d: %w", parentBatchID, err)
	}
	return &batch, nil
}
