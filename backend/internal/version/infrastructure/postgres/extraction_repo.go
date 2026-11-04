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
	"encoding/json"
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
	error, report, created_at, updated_at`

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
// advisory lock is keyed by (version_id, attachment_id) - not by sha - so
// concurrent Start calls for different inputs of the same attachment serialize:
// exactly one run stays active and every superseded run is archived. On the
// replay path the stored segments are refreshed (a changed text_sha256 reopens
// the segment) and totals/expected_count are recomputed so a done run never
// keeps orphan pending segments. A replay of an already superseded sha returns
// the currently active run instead of resurrecting the old cursor.
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
			($2::bigint)::text
		))
	`, run.VersionID, run.AttachmentID); err != nil {
		return versionapp.ExtractionRun{}, fmt.Errorf("create extraction run: lock: %w", err)
	}

	existing, err := scanExtractionRun(tx.QueryRow(ctx, `
		SELECT `+extractionRunColumns+` FROM requirement_extraction_run
		WHERE version_id = $1 AND attachment_id = $2 AND input_sha256 = $3
		ORDER BY id LIMIT 1
		FOR UPDATE
	`, run.VersionID, run.AttachmentID, run.InputSHA256))
	if err == nil {
		// The requested sha was already superseded and another run is active:
		// return the active input instead of archiving it for a dead cursor.
		if existing.Status == versionapp.ExtractionRunDone || existing.Status == versionapp.ExtractionRunFailed {
			active, activeErr := scanExtractionRun(tx.QueryRow(ctx, `
				SELECT `+extractionRunColumns+` FROM requirement_extraction_run
				WHERE version_id = $1 AND attachment_id = $2 AND id <> $3
				  AND status IN ('pending', 'running', 'paused')
				ORDER BY id DESC LIMIT 1
				FOR UPDATE
			`, run.VersionID, run.AttachmentID, existing.ID))
			if activeErr == nil {
				if err := tx.Commit(ctx); err != nil {
					return versionapp.ExtractionRun{}, fmt.Errorf("create extraction run: commit replay: %w", err)
				}
				return active, nil
			}
			if !errors.Is(activeErr, pgx.ErrNoRows) {
				return versionapp.ExtractionRun{}, fmt.Errorf("create extraction run: active lookup: %w", activeErr)
			}
		}
		if err := archiveActiveRunsTx(ctx, tx, run.VersionID, run.AttachmentID, existing.ID, run.UpdatedAt); err != nil {
			return versionapp.ExtractionRun{}, err
		}
		if err := r.upsertSegments(ctx, tx, existing.ID, segments, run.UpdatedAt); err != nil {
			return versionapp.ExtractionRun{}, err
		}
		if err := refreshReplayedRunTx(ctx, tx, existing.ID, run.ExpectedCount, run.UpdatedAt); err != nil {
			return versionapp.ExtractionRun{}, err
		}
		updated, err := scanExtractionRun(tx.QueryRow(ctx, `
			SELECT `+extractionRunColumns+` FROM requirement_extraction_run WHERE id = $1
		`, existing.ID))
		if err != nil {
			return versionapp.ExtractionRun{}, fmt.Errorf("create extraction run: reread: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return versionapp.ExtractionRun{}, fmt.Errorf("create extraction run: commit: %w", err)
		}
		return updated, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return versionapp.ExtractionRun{}, fmt.Errorf("create extraction run: lookup: %w", err)
	}

	// The document changed: archive every other active cursor of this
	// attachment instead of silently mixing two inputs.
	if err := archiveActiveRunsTx(ctx, tx, run.VersionID, run.AttachmentID, 0, run.UpdatedAt); err != nil {
		return versionapp.ExtractionRun{}, err
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
	if err := r.upsertSegments(ctx, tx, run.ID, segments, run.UpdatedAt); err != nil {
		return versionapp.ExtractionRun{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return versionapp.ExtractionRun{}, fmt.Errorf("create extraction run: commit: %w", err)
	}
	return run, nil
}

func archiveActiveRunsTx(ctx context.Context, tx pgx.Tx, versionID, attachmentID, keepID int64, now time.Time) error {
	if _, err := tx.Exec(ctx, `
		UPDATE requirement_extraction_run
		SET status = 'failed', error = 'archived: input_sha256 changed', updated_at = $4
		WHERE version_id = $1 AND attachment_id = $2 AND id <> $3
		  AND status IN ('pending', 'running', 'paused')
	`, versionID, attachmentID, keepID, now); err != nil {
		return fmt.Errorf("create extraction run: archive superseded: %w", err)
	}
	return nil
}

// refreshReplayedRunTx recomputes the replay segment totals and reopens a
// terminal run that received pending segments (changed text or a new segment),
// so a done run can never keep orphan pending segments. A paused run stays
// paused; expected_count is refreshed when the replay supplied a document.
func refreshReplayedRunTx(ctx context.Context, tx pgx.Tx, runID int64, expected *int, now time.Time) error {
	if _, err := tx.Exec(ctx, `
		WITH agg AS (
			SELECT count(*) AS total,
			       count(*) FILTER (WHERE status = 'done') AS done,
			       count(*) FILTER (WHERE status = 'skipped') AS skipped,
			       count(*) FILTER (WHERE status = 'failed') AS failed,
			       count(*) FILTER (WHERE status = 'pending') AS pending,
			       count(*) FILTER (WHERE status = 'running') AS running,
			       count(*) FILTER (WHERE status = 'failed' AND attempts < $4) AS retryable
			FROM requirement_extraction_segment
			WHERE run_id = $1
		)
		UPDATE requirement_extraction_run r
		SET total_segments = agg.total,
		    done_segments = agg.done,
		    failed_segments = agg.failed,
		    expected_count = COALESCE($2::int, r.expected_count),
		    status = CASE
		        WHEN r.status = 'paused' THEN r.status
		        WHEN agg.pending > 0 AND r.status IN ('done', 'failed') THEN 'pending'
		        WHEN agg.total > 0 AND agg.done + agg.skipped = agg.total THEN 'done'
		        WHEN agg.pending = 0 AND agg.running = 0 AND agg.retryable = 0 THEN 'failed'
		        ELSE r.status
		    END,
		    error = CASE
		        WHEN r.status = 'paused' THEN r.error
		        WHEN agg.pending > 0 AND r.status IN ('done', 'failed') THEN NULL
		        WHEN agg.total > 0 AND agg.done + agg.skipped = agg.total THEN NULL
		        ELSE r.error
		    END,
		    updated_at = $3
		FROM agg
		WHERE r.id = $1
	`, runID, expected, now, versionapp.DefaultExtractionMaxAttempts); err != nil {
		return fmt.Errorf("create extraction run: refresh replay: %w", err)
	}
	return nil
}

// upsertSegments inserts missing (run_id, seq) rows and refreshes the stored
// text identity on replay. When text_sha256 changed the segment is reopened
// (pending, attempts=0) so the changed text is re-extracted instead of being
// skipped by a DO NOTHING conflict.
func (r *ExtractionRepository) upsertSegments(ctx context.Context, tx pgx.Tx, runID int64, segments []versionapp.ExtractionSegment, now time.Time) error {
	for _, segment := range segments {
		id := segment.ID
		if id == 0 {
			id = r.ids.Next()
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO requirement_extraction_segment (
				id, run_id, seq, heading_path, char_start, char_end, text_sha256,
				status, attempts, extracted_count, error, renewed_at,
				created_at, updated_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', 0, 0, NULL, NULL, $8, $8)
			ON CONFLICT (run_id, seq) DO UPDATE
			SET heading_path = EXCLUDED.heading_path,
			    char_start = EXCLUDED.char_start,
			    char_end = EXCLUDED.char_end,
			    text_sha256 = EXCLUDED.text_sha256,
			    status = CASE WHEN requirement_extraction_segment.text_sha256
			                       IS DISTINCT FROM EXCLUDED.text_sha256
			                  THEN 'pending' ELSE requirement_extraction_segment.status END,
			    attempts = CASE WHEN requirement_extraction_segment.text_sha256
			                         IS DISTINCT FROM EXCLUDED.text_sha256
			                    THEN 0 ELSE requirement_extraction_segment.attempts END,
			    extracted_count = CASE WHEN requirement_extraction_segment.text_sha256
			                                IS DISTINCT FROM EXCLUDED.text_sha256
			                           THEN 0 ELSE requirement_extraction_segment.extracted_count END,
			    error = CASE WHEN requirement_extraction_segment.text_sha256
			                      IS DISTINCT FROM EXCLUDED.text_sha256
			                 THEN NULL ELSE requirement_extraction_segment.error END,
			    renewed_at = CASE WHEN requirement_extraction_segment.text_sha256
			                           IS DISTINCT FROM EXCLUDED.text_sha256
			                      THEN NULL ELSE requirement_extraction_segment.renewed_at END,
			    updated_at = CASE WHEN requirement_extraction_segment.text_sha256
			                           IS DISTINCT FROM EXCLUDED.text_sha256
			                      THEN EXCLUDED.updated_at
			                      ELSE requirement_extraction_segment.updated_at END
		`, id, runID, segment.Seq, segment.HeadingPath, segment.CharStart,
			segment.CharEnd, segment.TextSHA256, now); err != nil {
			return fmt.Errorf("create extraction run: upsert segment %d: %w", segment.Seq, err)
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

// ClaimSegments first gates on the run status (a paused/archived run is not
// claimable; the run row is locked so Pause waits for one claim), recovers stale
// running leases (older than five minutes; attempts exhausted becomes failed),
// then claims pending/due-failed segments with FOR UPDATE SKIP LOCKED. A claim
// increments attempts and resets the lease.
func (r *ExtractionRepository) ClaimSegments(ctx context.Context, runID int64, limit int, now time.Time) ([]versionapp.ExtractionSegment, error) {
	if limit <= 0 {
		limit = versionapp.DefaultExtractionClaimBatch
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("claim extraction segments: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var runStatus string
	if err := tx.QueryRow(ctx, `
		SELECT status FROM requirement_extraction_run WHERE id = $1 FOR UPDATE
	`, runID).Scan(&runStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, versionapp.ErrExtractionRunNotFound
		}
		return nil, fmt.Errorf("claim extraction segments: run gate: %w", err)
	}
	if versionapp.ExtractionRunStatus(runStatus) != versionapp.ExtractionRunPending &&
		versionapp.ExtractionRunStatus(runStatus) != versionapp.ExtractionRunRunning {
		return nil, nil
	}

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

// RenewSegmentLeases extends the running lease of the worker's outstanding
// claimed segments (heartbeat). Terminal segments are untouched, so renewing a
// batch never resurrects a segment that already finished.
func (r *ExtractionRepository) RenewSegmentLeases(ctx context.Context, runID int64, seqs []int, now time.Time) (int, error) {
	if len(seqs) == 0 {
		return 0, nil
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE requirement_extraction_segment
		SET renewed_at = $3, updated_at = $3
		WHERE run_id = $1 AND seq = ANY($2::int[]) AND status = 'running'
	`, runID, seqs, now)
	if err != nil {
		return 0, fmt.Errorf("renew extraction segment leases: %w", err)
	}
	return int(tag.RowsAffected()), nil
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
// in a single transaction. The run row is locked first: a terminal transition
// on a superseded (archived) run, or on a done/failed run, is rejected with
// ErrExtractionRunState and rolled back. A paused run still finishes the batch
// it already claimed, so its marks are allowed.
func (r *ExtractionRepository) markSegmentTerminal(ctx context.Context, runID int64, seq int, now time.Time, transition func(pgx.Tx) error, failureMessage string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mark extraction segment: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := requireWritableExtractionRunTx(ctx, tx, runID); err != nil {
		return err
	}
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

// requireWritableExtractionRunTx locks the run and rejects transitions when it
// is terminal or superseded by a newer run of the same attachment.
func requireWritableExtractionRunTx(ctx context.Context, tx pgx.Tx, runID int64) error {
	var versionID, attachmentID int64
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT version_id, attachment_id, status
		FROM requirement_extraction_run WHERE id = $1 FOR UPDATE
	`, runID).Scan(&versionID, &attachmentID, &status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return versionapp.ErrExtractionRunNotFound
		}
		return fmt.Errorf("lock extraction run: %w", err)
	}
	switch versionapp.ExtractionRunStatus(status) {
	case versionapp.ExtractionRunPending, versionapp.ExtractionRunRunning, versionapp.ExtractionRunPaused:
	default:
		return versionapp.ErrExtractionRunState
	}
	var superseded bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM requirement_extraction_run
			WHERE version_id = $1 AND attachment_id = $2 AND id > $3
		)
	`, versionID, attachmentID, runID).Scan(&superseded); err != nil {
		return fmt.Errorf("check extraction run generation: %w", err)
	}
	if superseded {
		return versionapp.ErrExtractionRunState
	}
	return nil
}

// requireNotSupersededExtractionRunTx locks the run and rejects retries of an
// archived run.
func requireNotSupersededExtractionRunTx(ctx context.Context, tx pgx.Tx, runID int64) error {
	var versionID, attachmentID int64
	if err := tx.QueryRow(ctx, `
		SELECT version_id, attachment_id FROM requirement_extraction_run
		WHERE id = $1 FOR UPDATE
	`, runID).Scan(&versionID, &attachmentID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return versionapp.ErrExtractionRunNotFound
		}
		return fmt.Errorf("lock extraction run: %w", err)
	}
	var superseded bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM requirement_extraction_run
			WHERE version_id = $1 AND attachment_id = $2 AND id > $3
		)
	`, versionID, attachmentID, runID).Scan(&superseded); err != nil {
		return fmt.Errorf("check extraction run generation: %w", err)
	}
	if superseded {
		return versionapp.ErrExtractionRunState
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

// requireActiveExtractionRunTx fences requirement emits: the source run must
// exist for the same version/attachment, be active (pending/running/paused) and
// not be superseded by a newer run of the attachment.
func requireActiveExtractionRunTx(ctx context.Context, tx pgx.Tx, source versionapp.ExtractionSource) error {
	var versionID, attachmentID int64
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT version_id, attachment_id, status
		FROM requirement_extraction_run WHERE id = $1 FOR SHARE
	`, source.RunID).Scan(&versionID, &attachmentID, &status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return versionapp.ErrExtractionRunNotFound
		}
		return fmt.Errorf("lock extraction run: %w", err)
	}
	if versionID != source.VersionID || (source.AttachmentID > 0 && attachmentID != source.AttachmentID) {
		return versionapp.ErrExtractionRunState
	}
	switch versionapp.ExtractionRunStatus(status) {
	case versionapp.ExtractionRunPending, versionapp.ExtractionRunRunning, versionapp.ExtractionRunPaused:
	default:
		return versionapp.ErrExtractionRunState
	}
	var superseded bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM requirement_extraction_run
			WHERE version_id = $1 AND attachment_id = $2 AND id > $3
		)
	`, versionID, attachmentID, source.RunID).Scan(&superseded); err != nil {
		return fmt.Errorf("check extraction run generation: %w", err)
	}
	if superseded {
		return versionapp.ErrExtractionRunState
	}
	return nil
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
// Superseded runs are rejected with ErrExtractionRunState.
func (r *ExtractionRepository) RetryFailedSegments(ctx context.Context, runID int64, now time.Time) (int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("retry extraction segments: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := requireNotSupersededExtractionRunTx(ctx, tx, runID); err != nil {
		return 0, err
	}
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
	if err := requireNotSupersededExtractionRunTx(ctx, tx, runID); err != nil {
		return err
	}
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

// SaveReconciliation persists the full reconciliation report (JSONB) plus the
// expected/extracted counts shown by the UI.
func (r *ExtractionRepository) SaveReconciliation(ctx context.Context, runID int64, report versionapp.ReconciliationReport, now time.Time) error {
	report = normalizeReconciliationReport(report)
	payload, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("save extraction reconciliation: marshal: %w", err)
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE requirement_extraction_run
		SET expected_count = $2, extracted_count = $3, report = $4::jsonb, updated_at = $5
		WHERE id = $1
	`, runID, report.Expected, report.Extracted, string(payload), now)
	if err != nil {
		return fmt.Errorf("save extraction reconciliation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return versionapp.ErrExtractionRunNotFound
	}
	return nil
}

// normalizeReconciliationReport keeps the persisted JSON stable: empty id lists
// are encoded as [] instead of null.
func normalizeReconciliationReport(report versionapp.ReconciliationReport) versionapp.ReconciliationReport {
	if report.MissingIDs == nil {
		report.MissingIDs = []string{}
	}
	if report.PlannedIDs == nil {
		report.PlannedIDs = []string{}
	}
	return report
}

// CountLowConfidence counts the requirements attributed to the run whose
// confidence is below the configured threshold. A NULL confidence (not scored
// by the model) is not counted as low confidence.
func (r *ExtractionRepository) CountLowConfidence(ctx context.Context, runID int64, threshold float64) (int, error) {
	var count int
	if err := r.pool.QueryRow(ctx, `
		SELECT count(*) FROM requirement
		WHERE extraction_run_id = $1 AND confidence IS NOT NULL AND confidence < $2
	`, runID, threshold).Scan(&count); err != nil {
		return 0, fmt.Errorf("count low confidence requirements: %w", err)
	}
	return count, nil
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
// and only gets a new immutable revision when the content hash changed. The run
// is fenced: emitting from a superseded (archived) run is rejected, so an old
// in-flight worker cannot overwrite the new run's provenance.
func (r *ExtractionRepository) UpsertExtractedRequirements(ctx context.Context, source versionapp.ExtractionSource, requirements []versionapp.ExtractionRequirement, now time.Time) (versionapp.ExtractionEmitCounts, error) {
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
	if err := requireActiveExtractionRunTx(ctx, tx, source); err != nil {
		return versionapp.ExtractionEmitCounts{}, err
	}
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
	var reportJSON []byte
	if err := row.Scan(&run.ID, &run.VersionID, &run.AttachmentID, &run.InputSHA256, &status,
		&run.TotalSegments, &run.DoneSegments, &run.FailedSegments, &run.ExpectedCount,
		&run.ExtractedCount, &errorText, &reportJSON, &run.CreatedAt, &run.UpdatedAt); err != nil {
		return versionapp.ExtractionRun{}, err
	}
	run.Status = versionapp.ExtractionRunStatus(status)
	if errorText != nil {
		run.Error = *errorText
	}
	if len(reportJSON) > 0 && string(reportJSON) != "{}" {
		var report versionapp.ReconciliationReport
		if err := json.Unmarshal(reportJSON, &report); err != nil {
			return versionapp.ExtractionRun{}, fmt.Errorf("decode extraction report: %w", err)
		}
		run.Report = &report
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
