// Package versionpg: PostgreSQL adapter for the v0.22 A-7 requirement
// extraction cursor (spec R-A7-6..R-A7-9, docs/version-V0.22/A §7.6/§7.7).
//
// This file implements the ExtractionRepository port: idempotent runs keyed by
// (version_id, attachment_id, input_sha256), unique (run_id, seq) segments,
// FOR UPDATE SKIP LOCKED claiming with stale-lease recovery, failed-segment
// exponential backoff, and the requirement upsert keyed by
// (version_id, external_id) stored as requirement.source_key
// "extraction:<external_id>". Run counts are recomputed in the same transaction
// as every segment transition so the progress API never reads a torn cursor.
package versionpg

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const extractionRunColumns = `id, version_id, attachment_id, input_sha256, status,
	total_segments, done_segments, failed_segments, expected_count, extracted_count,
	error, created_at, updated_at`

const extractionSegmentColumns = `id, run_id, seq, heading_path, char_start, char_end,
	text_sha256, status, attempts, extracted_count, error, renewed_at`

// ExtractionRepository persists extraction runs, their segment cursor and the
// extracted requirement drafts.
type ExtractionRepository struct {
	pool *pgxpool.Pool
	ids  platformid.Generator
}

// NewExtractionRepository wraps a pgx pool. ids supplies primary keys when the
// caller did not assign one (runs, segments, requirements, revisions).
func NewExtractionRepository(pool *pgxpool.Pool, ids platformid.Generator) *ExtractionRepository {
	return &ExtractionRepository{pool: pool, ids: ids}
}

var _ versionapp.ExtractionRepository = (*ExtractionRepository)(nil)

// CreateRun is idempotent on (version_id, attachment_id, input_sha256). The
// advisory lock serializes concurrent Start calls for one input. A new sha
// archives the attachment's pending/running/paused runs as failed, then inserts
// the run and its missing segments (ON CONFLICT (run_id, seq) DO NOTHING).
func (r *ExtractionRepository) CreateRun(ctx context.Context, run versionapp.ExtractionRun, segments []versionapp.ExtractionSegment) (versionapp.ExtractionRun, error) {
	if r.pool == nil {
		return versionapp.ExtractionRun{}, errors.New("extraction repository: missing pool")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return versionapp.ExtractionRun{}, fmt.Errorf("create extraction run: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(hashtext(
			'eztest:extraction-run:' || ($1::bigint)::text || ':' ||
			($2::bigint)::text || ':' || $3
		))
	`, run.VersionID, run.AttachmentID, run.InputSHA256); err != nil {
		return versionapp.ExtractionRun{}, fmt.Errorf("create extraction run: lock: %w", err)
	}

	existing, err := scanExtractionRun(tx.QueryRow(ctx, `
		SELECT `+extractionRunColumns+` FROM requirement_extraction_run
		WHERE version_id = $1 AND attachment_id = $2 AND input_sha256 = $3
		ORDER BY id LIMIT 1
	`, run.VersionID, run.AttachmentID, run.InputSHA256))
	if err == nil {
		if insertErr := r.insertSegments(ctx, tx, existing.ID, segments); insertErr != nil {
			return versionapp.ExtractionRun{}, insertErr
		}
		if err := tx.Commit(ctx); err != nil {
			return versionapp.ExtractionRun{}, fmt.Errorf("create extraction run: commit: %w", err)
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return versionapp.ExtractionRun{}, fmt.Errorf("create extraction run: lookup: %w", err)
	}

	// The document changed: archive the superseded cursor instead of silently
	// mixing two inputs (done/failed runs stay as they are).
	if _, err := tx.Exec(ctx, `
		UPDATE requirement_extraction_run
		SET status = 'failed', error = 'archived: input_sha256 changed', updated_at = $4
		WHERE version_id = $1 AND attachment_id = $2 AND input_sha256 <> $3
		  AND status IN ('pending', 'running', 'paused')
	`, run.VersionID, run.AttachmentID, run.InputSHA256, run.UpdatedAt); err != nil {
		return versionapp.ExtractionRun{}, fmt.Errorf("create extraction run: archive superseded: %w", err)
	}

	if run.ID == 0 {
		run.ID = r.ids.Next()
	}
	if run.Status == "" {
		run.Status = versionapp.ExtractionRunPending
	}
	run.TotalSegments = len(segments)
	run.DoneSegments = 0
	run.FailedSegments = 0
	run.ExtractedCount = 0
	if _, err := tx.Exec(ctx, `
		INSERT INTO requirement_extraction_run (
			id, version_id, attachment_id, input_sha256, status,
			total_segments, done_segments, failed_segments, expected_count,
			extracted_count, error, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, 0, 0, $7, 0, NULL, $8, $9)
	`, run.ID, run.VersionID, run.AttachmentID, run.InputSHA256, string(run.Status),
		run.TotalSegments, run.ExpectedCount, run.CreatedAt, run.UpdatedAt); err != nil {
		return versionapp.ExtractionRun{}, fmt.Errorf("create extraction run: insert: %w", err)
	}
	if err := r.insertSegments(ctx, tx, run.ID, segments); err != nil {
		return versionapp.ExtractionRun{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return versionapp.ExtractionRun{}, fmt.Errorf("create extraction run: commit: %w", err)
	}
	return run, nil
}

func (r *ExtractionRepository) insertSegments(ctx context.Context, tx pgx.Tx, runID int64, segments []versionapp.ExtractionSegment) error {
	for _, segment := range segments {
		id := segment.ID
		if id == 0 {
			id = r.ids.Next()
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO requirement_extraction_segment (
				id, run_id, seq, heading_path, char_start, char_end, text_sha256,
				status, attempts, extracted_count, error, renewed_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', 0, 0, NULL, NULL)
			ON CONFLICT (run_id, seq) DO NOTHING
		`, id, runID, segment.Seq, segment.HeadingPath, segment.CharStart,
			segment.CharEnd, segment.TextSHA256); err != nil {
			return fmt.Errorf("create extraction run: insert segment %d: %w", segment.Seq, err)
		}
	}
	return nil
}

// GetRun returns one run.
func (r *ExtractionRepository) GetRun(ctx context.Context, runID int64) (versionapp.ExtractionRun, error) {
	run, err := scanExtractionRun(r.pool.QueryRow(ctx, `
		SELECT `+extractionRunColumns+` FROM requirement_extraction_run WHERE id = $1
	`, runID))
	if err != nil {
		return versionapp.ExtractionRun{}, wrapExtractionLookup("get extraction run", err)
	}
	return run, nil
}

// GetLatestRun returns the newest run of one attachment.
func (r *ExtractionRepository) GetLatestRun(ctx context.Context, versionID, attachmentID int64) (versionapp.ExtractionRun, error) {
	run, err := scanExtractionRun(r.pool.QueryRow(ctx, `
		SELECT `+extractionRunColumns+` FROM requirement_extraction_run
		WHERE version_id = $1 AND attachment_id = $2
		ORDER BY id DESC LIMIT 1
	`, versionID, attachmentID))
	if err != nil {
		return versionapp.ExtractionRun{}, wrapExtractionLookup("get latest extraction run", err)
	}
	return run, nil
}

// ListRuns returns all runs of one version in ascending run-id order.
func (r *ExtractionRepository) ListRuns(ctx context.Context, versionID int64) ([]versionapp.ExtractionRun, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+extractionRunColumns+` FROM requirement_extraction_run
		WHERE version_id = $1 ORDER BY id
	`, versionID)
	if err != nil {
		return nil, fmt.Errorf("list extraction runs: %w", err)
	}
	defer rows.Close()
	runs := make([]versionapp.ExtractionRun, 0)
	for rows.Next() {
		run, err := scanExtractionRun(rows)
		if err != nil {
			return nil, fmt.Errorf("list extraction runs: scan: %w", err)
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list extraction runs: %w", err)
	}
	return runs, nil
}

// ListRunsByStatus returns up to limit runs in one of the given statuses,
// ascending by id (the ProcessNext order).
func (r *ExtractionRepository) ListRunsByStatus(ctx context.Context, statuses []versionapp.ExtractionRunStatus, limit int) ([]versionapp.ExtractionRun, error) {
	if limit <= 0 {
		limit = 100
	}
	values := make([]string, 0, len(statuses))
	for _, status := range statuses {
		values = append(values, string(status))
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+extractionRunColumns+` FROM requirement_extraction_run
		WHERE status = ANY($1::text[])
		ORDER BY id
		LIMIT $2
	`, values, limit)
	if err != nil {
		return nil, fmt.Errorf("list extraction runs by status: %w", err)
	}
	defer rows.Close()
	runs := make([]versionapp.ExtractionRun, 0)
	for rows.Next() {
		run, err := scanExtractionRun(rows)
		if err != nil {
			return nil, fmt.Errorf("list extraction runs by status: scan: %w", err)
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list extraction runs by status: %w", err)
	}
	return runs, nil
}

// ListSegments returns the run's segments in seq order (progress UI).
func (r *ExtractionRepository) ListSegments(ctx context.Context, runID int64) ([]versionapp.ExtractionSegment, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+extractionSegmentColumns+` FROM requirement_extraction_segment
		WHERE run_id = $1 ORDER BY seq
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("list extraction segments: %w", err)
	}
	defer rows.Close()
	segments := make([]versionapp.ExtractionSegment, 0)
	for rows.Next() {
		segment, err := scanExtractionSegment(rows)
		if err != nil {
			return nil, fmt.Errorf("list extraction segments: scan: %w", err)
		}
		segments = append(segments, segment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list extraction segments: %w", err)
	}
	return segments, nil
}

// ClaimSegments first recovers stale running leases (older than five minutes;
// attempts exhausted becomes failed), then claims pending/due-failed segments
// with FOR UPDATE SKIP LOCKED. A claim increments attempts and resets the lease.
func (r *ExtractionRepository) ClaimSegments(ctx context.Context, runID int64, limit int, now time.Time) ([]versionapp.ExtractionSegment, error) {
	if limit <= 0 {
		limit = versionapp.DefaultExtractionClaimBatch
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("claim extraction segments: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	staleBefore := now.Add(-versionapp.DefaultExtractionLeaseTimeout)
	staleTag, err := tx.Exec(ctx, `
		UPDATE requirement_extraction_segment
		SET status = CASE WHEN attempts < $3 THEN 'pending' ELSE 'failed' END,
		    error = CASE WHEN attempts < $3 THEN NULL
		                 ELSE 'segment lease expired after max attempts' END,
		    renewed_at = NULL,
		    updated_at = $2
		WHERE run_id = $1 AND status = 'running'
		  AND renewed_at IS NOT NULL AND renewed_at < $4
	`, runID, now, versionapp.DefaultExtractionMaxAttempts, staleBefore)
	if err != nil {
		return nil, fmt.Errorf("claim extraction segments: recover stale leases: %w", err)
	}
	if staleTag.RowsAffected() > 0 {
		if err := recomputeExtractionRunTx(ctx, tx, runID, now, ""); err != nil {
			return nil, err
		}
	}

	rows, err := tx.Query(ctx, `
		SELECT `+extractionSegmentColumns+` FROM requirement_extraction_segment
		WHERE run_id = $1
		  AND attempts < $2
		  AND (status = 'pending'
		       OR (status = 'failed' AND (renewed_at IS NULL OR renewed_at <= $3)))
		ORDER BY seq
		LIMIT $4
		FOR UPDATE SKIP LOCKED
	`, runID, versionapp.DefaultExtractionMaxAttempts, now, limit)
	if err != nil {
		return nil, fmt.Errorf("claim extraction segments: select: %w", err)
	}
	claimed := make([]versionapp.ExtractionSegment, 0, limit)
	for rows.Next() {
		segment, scanErr := scanExtractionSegment(rows)
		if scanErr != nil {
			rows.Close()
			return nil, fmt.Errorf("claim extraction segments: scan: %w", scanErr)
		}
		claimed = append(claimed, segment)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim extraction segments: %w", err)
	}
	for index := range claimed {
		segment := &claimed[index]
		if err := tx.QueryRow(ctx, `
			UPDATE requirement_extraction_segment
			SET status = 'running', attempts = attempts + 1, error = NULL,
			    renewed_at = $3, updated_at = $3
			WHERE id = $1 AND run_id = $2 AND status IN ('pending', 'failed')
			RETURNING attempts, renewed_at
		`, segment.ID, runID, now).Scan(&segment.Attempts, &segment.RenewedAt); err != nil {
			return nil, fmt.Errorf("claim extraction segments: update %d: %w", segment.Seq, err)
		}
		segment.Status = versionapp.ExtractionSegmentRunning
		segment.Error = ""
	}
	// Marking the run running in the claim transaction keeps a crashed
	// "pending run with claimed segments" impossible.
	if _, err := tx.Exec(ctx, `
		UPDATE requirement_extraction_run SET status = 'running', updated_at = $2
		WHERE id = $1 AND status = 'pending'
	`, runID, now); err != nil {
		return nil, fmt.Errorf("claim extraction segments: mark run running: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("claim extraction segments: commit: %w", err)
	}
	return claimed, nil
}

// MarkSegmentDone finishes one claimed segment and recomputes the run counts.
func (r *ExtractionRepository) MarkSegmentDone(ctx context.Context, runID int64, seq, extractedCount int, now time.Time) error {
	return r.markSegmentTerminal(ctx, runID, seq, now, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE requirement_extraction_segment
			SET status = 'done', extracted_count = $3, error = NULL,
			    renewed_at = $4, updated_at = $4
			WHERE run_id = $1 AND seq = $2 AND status = 'running'
		`, runID, seq, extractedCount, now)
		if err != nil {
			return err
		}
		return requireSegmentTransition(ctx, tx, tag.RowsAffected(), runID, seq)
	}, "")
}

// MarkSegmentSkipped finishes one claimed segment as skipped (terminal, never
// retried) and recomputes the run counts.
func (r *ExtractionRepository) MarkSegmentSkipped(ctx context.Context, runID int64, seq int, reason string, now time.Time) error {
	return r.markSegmentTerminal(ctx, runID, seq, now, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE requirement_extraction_segment
			SET status = 'skipped', error = NULLIF($3, ''),
			    renewed_at = $4, updated_at = $4
			WHERE run_id = $1 AND seq = $2 AND status = 'running'
		`, runID, seq, reason, now)
		if err != nil {
			return err
		}
		return requireSegmentTransition(ctx, tx, tag.RowsAffected(), runID, seq)
	}, "")
}

// MarkSegmentFailed stores the failure and its exponential backoff retry time
// (segment.renewed_at = now + SegmentRetryDelay(attempts)).
func (r *ExtractionRepository) MarkSegmentFailed(ctx context.Context, runID int64, seq int, message string, now time.Time) error {
	var retryAt time.Time
	return r.markSegmentTerminal(ctx, runID, seq, now, func(tx pgx.Tx) error {
		var attempts int
		var status string
		if err := tx.QueryRow(ctx, `
			SELECT attempts, status FROM requirement_extraction_segment
			WHERE run_id = $1 AND seq = $2
			FOR UPDATE
		`, runID, seq).Scan(&attempts, &status); err != nil {
			return err
		}
		if versionapp.ExtractionSegmentStatus(status) != versionapp.ExtractionSegmentRunning {
			return versionapp.ErrExtractionSegmentState
		}
		retryAt = now.Add(versionapp.SegmentRetryDelay(attempts))
		_, err := tx.Exec(ctx, `
			UPDATE requirement_extraction_segment
			SET status = 'failed', error = $3, renewed_at = $4, updated_at = $5
			WHERE run_id = $1 AND seq = $2
		`, runID, seq, message, retryAt, now)
		return err
	}, message)
}

// markSegmentTerminal wraps one segment transition plus the run count refresh
// in a single transaction.
func (r *ExtractionRepository) markSegmentTerminal(ctx context.Context, runID int64, seq int, now time.Time, transition func(pgx.Tx) error, failureMessage string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mark extraction segment: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := transition(tx); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return versionapp.ErrExtractionSegmentNotFound
		}
		return fmt.Errorf("mark extraction segment %d: %w", seq, err)
	}
	if err := recomputeExtractionRunTx(ctx, tx, runID, now, failureMessage); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("mark extraction segment: commit: %w", err)
	}
	return nil
}

func requireSegmentTransition(ctx context.Context, tx pgx.Tx, affected int64, runID int64, seq int) error {
	if affected > 0 {
		return nil
	}
	var status string
	err := tx.QueryRow(ctx, `
		SELECT status FROM requirement_extraction_segment WHERE run_id = $1 AND seq = $2
	`, runID, seq).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return versionapp.ErrExtractionSegmentNotFound
	}
	if err != nil {
		return err
	}
	return versionapp.ErrExtractionSegmentState
}

// recomputeExtractionRunTx refreshes total/done/failed/extracted and the
// terminal run status in the same transaction as the segment transition.
func recomputeExtractionRunTx(ctx context.Context, tx pgx.Tx, runID int64, now time.Time, failureMessage string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE requirement_extraction_run r
		SET total_segments = agg.total,
		    done_segments = agg.done,
		    failed_segments = agg.failed,
		    extracted_count = (SELECT count(*) FROM requirement q WHERE q.extraction_run_id = r.id),
		    status = CASE
		        WHEN r.status = 'paused' THEN r.status
		        WHEN agg.total > 0 AND agg.done + agg.skipped = agg.total THEN 'done'
		        WHEN agg.pending = 0 AND agg.running = 0 AND agg.retryable = 0 THEN 'failed'
		        ELSE r.status
		    END,
		    error = CASE
		        WHEN r.status = 'paused' THEN r.error
		        WHEN agg.total > 0 AND agg.done + agg.skipped = agg.total THEN NULL
		        WHEN agg.pending = 0 AND agg.running = 0 AND agg.retryable = 0
		            THEN COALESCE(NULLIF($3, ''), r.error)
		        ELSE r.error
		    END,
		    updated_at = $2
		FROM (
		    SELECT count(*) AS total,
		           count(*) FILTER (WHERE status = 'done') AS done,
		           count(*) FILTER (WHERE status = 'failed') AS failed,
		           count(*) FILTER (WHERE status = 'skipped') AS skipped,
		           count(*) FILTER (WHERE status = 'pending') AS pending,
		           count(*) FILTER (WHERE status = 'running') AS running,
		           count(*) FILTER (WHERE status = 'failed' AND attempts < $4) AS retryable
		    FROM requirement_extraction_segment
		    WHERE run_id = $1
		) agg
		WHERE r.id = $1
	`, runID, now, failureMessage, versionapp.DefaultExtractionMaxAttempts); err != nil {
		return fmt.Errorf("recompute extraction run: %w", err)
	}
	return nil
}

// TransitionRun applies a guarded status transition. Starting or resuming a run
// re-evaluates the terminal state in the same transaction: a run paused
// mid-batch can already have every segment done, and must not stay pending.
func (r *ExtractionRepository) TransitionRun(ctx context.Context, runID int64, from []versionapp.ExtractionRunStatus, to versionapp.ExtractionRunStatus, now time.Time) (bool, error) {
	values := make([]string, 0, len(from))
	resumingPaused := false
	for _, status := range from {
		values = append(values, string(status))
		if status == versionapp.ExtractionRunPaused {
			resumingPaused = true
		}
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("transition extraction run: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		UPDATE requirement_extraction_run
		SET status = $3,
		    error = CASE WHEN $3 IN ('pending', 'running') THEN NULL ELSE error END,
		    updated_at = $4
		WHERE id = $1 AND status = ANY($2::text[])
	`, runID, values, string(to), now)
	if err != nil {
		return false, fmt.Errorf("transition extraction run: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM requirement_extraction_run WHERE id = $1)
		`, runID).Scan(&exists); err != nil {
			return false, fmt.Errorf("transition extraction run: %w", err)
		}
		if !exists {
			return false, versionapp.ErrExtractionRunNotFound
		}
		return false, nil
	}
	if to == versionapp.ExtractionRunRunning ||
		(to == versionapp.ExtractionRunPending && resumingPaused) {
		if err := recomputeExtractionRunTx(ctx, tx, runID, now, ""); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("transition extraction run: commit: %w", err)
	}
	return true, nil
}

// RetryFailedSegments gives every failed segment a fresh attempt budget.
func (r *ExtractionRepository) RetryFailedSegments(ctx context.Context, runID int64, now time.Time) (int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("retry extraction segments: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		UPDATE requirement_extraction_segment
		SET status = 'pending', attempts = 0, error = NULL, renewed_at = NULL, updated_at = $2
		WHERE run_id = $1 AND status = 'failed'
	`, runID, now)
	if err != nil {
		return 0, fmt.Errorf("retry extraction segments: %w", err)
	}
	if err := recomputeExtractionRunTx(ctx, tx, runID, now, ""); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("retry extraction segments: commit: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// RetrySegment resets one failed segment and a failed run back to pending.
func (r *ExtractionRepository) RetrySegment(ctx context.Context, runID int64, seq int, now time.Time) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("retry extraction segment: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT status FROM requirement_extraction_segment
		WHERE run_id = $1 AND seq = $2
		FOR UPDATE
	`, runID, seq).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return versionapp.ErrExtractionSegmentNotFound
		}
		return fmt.Errorf("retry extraction segment: %w", err)
	}
	if versionapp.ExtractionSegmentStatus(status) != versionapp.ExtractionSegmentFailed {
		return versionapp.ErrExtractionSegmentState
	}
	if _, err := tx.Exec(ctx, `
		UPDATE requirement_extraction_segment
		SET status = 'pending', attempts = 0, error = NULL, renewed_at = NULL, updated_at = $3
		WHERE run_id = $1 AND seq = $2
	`, runID, seq, now); err != nil {
		return fmt.Errorf("retry extraction segment: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE requirement_extraction_run
		SET status = CASE WHEN status = 'failed' THEN 'pending' ELSE status END,
		    error = CASE WHEN status = 'failed' THEN NULL ELSE error END,
		    updated_at = $2
		WHERE id = $1
	`, runID, now); err != nil {
		return fmt.Errorf("retry extraction segment: run: %w", err)
	}
	if err := recomputeExtractionRunTx(ctx, tx, runID, now, ""); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("retry extraction segment: commit: %w", err)
	}
	return nil
}

// SaveReconciliation persists the declared/extracted counts shown by the UI.
func (r *ExtractionRepository) SaveReconciliation(ctx context.Context, runID int64, expected, extracted int, now time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE requirement_extraction_run
		SET expected_count = $2, extracted_count = $3, updated_at = $4
		WHERE id = $1
	`, runID, expected, extracted, now)
	if err != nil {
		return fmt.Errorf("save extraction reconciliation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return versionapp.ErrExtractionRunNotFound
	}
	return nil
}

// ListExtractedExternalIDs returns the unique external ids attributed to the
// run, sorted for deterministic reconciliation.
func (r *ExtractionRepository) ListExtractedExternalIDs(ctx context.Context, versionID, runID int64) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT external_id FROM requirement
		WHERE version_id = $1 AND extraction_run_id = $2 AND external_id <> ''
	`, versionID, runID)
	if err != nil {
		return nil, fmt.Errorf("list extracted external ids: %w", err)
	}
	defer rows.Close()
	seen := make(map[string]struct{})
	ids := make([]string, 0)
	for rows.Next() {
		var externalID string
		if err := rows.Scan(&externalID); err != nil {
			return nil, fmt.Errorf("list extracted external ids: scan: %w", err)
		}
		if _, duplicate := seen[externalID]; duplicate {
			continue
		}
		seen[externalID] = struct{}{}
		ids = append(ids, externalID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list extracted external ids: %w", err)
	}
	sort.Strings(ids)
	return ids, nil
}

// UpsertExtractedRequirements upserts requirements keyed by
// (version_id, external_id) inside one transaction. The version advisory lock
// serializes concurrent segment sinks; an existing requirement keeps its status
// and only gets a new immutable revision when the content hash changed.
func (r *ExtractionRepository) UpsertExtractedRequirements(ctx context.Context, source versionapp.ExtractionSource, requirements []versionapp.ExtractionRequirement) (versionapp.ExtractionEmitCounts, error) {
	if r.pool == nil {
		return versionapp.ExtractionEmitCounts{}, errors.New("extraction repository: missing pool")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return versionapp.ExtractionEmitCounts{}, fmt.Errorf("upsert extracted requirements: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockVersionTx(ctx, tx, source.VersionID, "upsert extracted requirements"); err != nil {
		return versionapp.ExtractionEmitCounts{}, err
	}
	now := time.Now().UTC()
	counts := versionapp.ExtractionEmitCounts{}
	for _, requirement := range requirements {
		externalID := strings.TrimSpace(requirement.ExternalID)
		if externalID == "" {
			return versionapp.ExtractionEmitCounts{}, fmt.Errorf("upsert extracted requirements: empty external_id")
		}
		created, err := r.upsertExtractedRequirementTx(ctx, tx, source, externalID, requirement, now)
		if err != nil {
			return versionapp.ExtractionEmitCounts{}, err
		}
		if created {
			counts.Created++
		} else {
			counts.Updated++
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return versionapp.ExtractionEmitCounts{}, fmt.Errorf("upsert extracted requirements: commit: %w", err)
	}
	return counts, nil
}

func (r *ExtractionRepository) upsertExtractedRequirementTx(ctx context.Context, tx pgx.Tx, source versionapp.ExtractionSource, externalID string, requirement versionapp.ExtractionRequirement, now time.Time) (bool, error) {
	sourceKey := versionapp.ExtractionRequirementSourceKey(externalID)
	priority := strings.TrimSpace(requirement.Priority)
	if priority == "" {
		priority = version.DefaultPriority
	}
	acceptance := strings.Join(requirement.AcceptanceCriteria, "\n")
	revision := version.RequirementVersion{
		Title:              requirement.Title,
		Description:        requirement.Description,
		AcceptanceCriteria: acceptance,
		Priority:           priority,
		Constraints:        requirement.ConstraintsJSON,
	}
	contentHash, err := revision.ComputeContentHash()
	if err != nil {
		return false, fmt.Errorf("upsert extracted requirement %s: %w", externalID, err)
	}

	var requirementID int64
	var currentVersionID *int64
	err = tx.QueryRow(ctx, `
		SELECT id, current_version_id FROM requirement
		WHERE version_id = $1 AND source_key = $2 AND status <> 'obsolete'
		ORDER BY id LIMIT 1
		FOR UPDATE
	`, source.VersionID, sourceKey).Scan(&requirementID, &currentVersionID)
	switch {
	case err == nil:
		var currentHash string
		if currentVersionID != nil {
			if scanErr := tx.QueryRow(ctx, `
				SELECT content_hash FROM requirement_version WHERE id = $1
			`, *currentVersionID).Scan(&currentHash); scanErr != nil {
				return false, fmt.Errorf("upsert extracted requirement %s: current revision: %w", externalID, scanErr)
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE requirement
			SET extraction_run_id = $2, source_quote = $3, external_id = $4,
			    confidence = $5::float8::numeric, updated_at = $6
			WHERE id = $1
		`, requirementID, source.RunID, requirement.SourceQuote, externalID, requirement.Confidence, now); err != nil {
			return false, fmt.Errorf("upsert extracted requirement %s: provenance: %w", externalID, err)
		}
		if currentVersionID == nil || currentHash != contentHash {
			nextRevision := 1
			if currentVersionID != nil {
				if scanErr := tx.QueryRow(ctx, `
					SELECT revision FROM requirement_version WHERE id = $1
				`, *currentVersionID).Scan(&nextRevision); scanErr != nil {
					return false, fmt.Errorf("upsert extracted requirement %s: revision: %w", externalID, scanErr)
				}
				nextRevision++
			}
			versionID := r.ids.Next()
			if err := insertExtractionRevision(ctx, tx, versionID, requirementID, nextRevision,
				requirement, acceptance, priority, contentHash, now); err != nil {
				return false, fmt.Errorf("upsert extracted requirement %s: revision insert: %w", externalID, err)
			}
			if _, err := tx.Exec(ctx, `
				UPDATE requirement SET current_version_id = $2 WHERE id = $1
			`, requirementID, versionID); err != nil {
				return false, fmt.Errorf("upsert extracted requirement %s: repoint revision: %w", externalID, err)
			}
		}
		return false, nil
	case errors.Is(err, pgx.ErrNoRows):
		requirementID = r.ids.Next()
		if _, err := tx.Exec(ctx, `
			INSERT INTO requirement (
				id, version_id, current_version_id, status, target_version_sut_ids,
				created_at, updated_at, source_key,
				extraction_run_id, source_quote, external_id, confidence
			)
			VALUES ($1, $2, NULL, 'draft', '[]'::jsonb, $3, $3, $4, $5, $6, $7, $8::float8::numeric)
		`, requirementID, source.VersionID, now, sourceKey, source.RunID,
			requirement.SourceQuote, externalID, requirement.Confidence); err != nil {
			return false, fmt.Errorf("upsert extracted requirement %s: insert requirement: %w", externalID, err)
		}
		versionID := r.ids.Next()
		if err := insertExtractionRevision(ctx, tx, versionID, requirementID, 1,
			requirement, acceptance, priority, contentHash, now); err != nil {
			return false, fmt.Errorf("upsert extracted requirement %s: first revision: %w", externalID, err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE requirement SET current_version_id = $2 WHERE id = $1
		`, requirementID, versionID); err != nil {
			return false, fmt.Errorf("upsert extracted requirement %s: repoint first revision: %w", externalID, err)
		}
		return true, nil
	default:
		return false, fmt.Errorf("upsert extracted requirement %s: lookup: %w", externalID, err)
	}
}

func insertExtractionRevision(ctx context.Context, tx pgx.Tx, revisionID, requirementID int64, revision int, requirement versionapp.ExtractionRequirement, acceptance, priority, contentHash string, now time.Time) error {
	constraints := requirement.ConstraintsJSON
	if len(constraints) == 0 {
		constraints = []byte(`{}`)
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO requirement_version (
			id, requirement_id, revision, title, description, acceptance_criteria,
			priority, constraints, target_version_suts, target_pages, target_apis,
			content_hash, created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, '[]'::jsonb, '[]'::jsonb,
		        '[]'::jsonb, $9, $10)
	`, revisionID, requirementID, revision, requirement.Title, requirement.Description,
		acceptance, priority, string(constraints), contentHash, now)
	return err
}

func scanExtractionRun(row rowScanner) (versionapp.ExtractionRun, error) {
	var run versionapp.ExtractionRun
	var status string
	var errorText *string
	if err := row.Scan(&run.ID, &run.VersionID, &run.AttachmentID, &run.InputSHA256, &status,
		&run.TotalSegments, &run.DoneSegments, &run.FailedSegments, &run.ExpectedCount,
		&run.ExtractedCount, &errorText, &run.CreatedAt, &run.UpdatedAt); err != nil {
		return versionapp.ExtractionRun{}, err
	}
	run.Status = versionapp.ExtractionRunStatus(status)
	if errorText != nil {
		run.Error = *errorText
	}
	return run, nil
}

func scanExtractionSegment(row rowScanner) (versionapp.ExtractionSegment, error) {
	var segment versionapp.ExtractionSegment
	var status string
	var errorText *string
	if err := row.Scan(&segment.ID, &segment.RunID, &segment.Seq, &segment.HeadingPath,
		&segment.CharStart, &segment.CharEnd, &segment.TextSHA256, &status,
		&segment.Attempts, &segment.ExtractedCount, &errorText, &segment.RenewedAt); err != nil {
		return versionapp.ExtractionSegment{}, err
	}
	segment.Status = versionapp.ExtractionSegmentStatus(status)
	if errorText != nil {
		segment.Error = *errorText
	}
	return segment, nil
}

func wrapExtractionLookup(op string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return versionapp.ErrExtractionRunNotFound
	}
	return fmt.Errorf("%s: %w", op, err)
}
