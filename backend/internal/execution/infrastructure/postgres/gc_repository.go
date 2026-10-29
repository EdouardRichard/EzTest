package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/application"
)

// gcCandidateLimit bounds one purge run so a very large backlog cannot hold an
// unbounded slice in memory. Later runs continue where this one stopped.
const gcCandidateLimit = 10000

// gcProtectedCTETemplate is the reference-protection query of spec 10.2. The
// placeholder is the batch-keep parameter ($1 or $2 depending on the caller).
//
// Protected evidence:
//   - every artifact of an active (planned/running) batch;
//   - every artifact of the latest batchKeep batches of each version;
//   - evidence referenced by a published report (report_ref);
//   - evidence referenced by an open/confirmed issue (issue_ref);
//   - visual-baseline artifacts (evidence.kind);
//   - evidence referenced from the evidence_ref report/issue rows, for the
//     legacy P6 reference table.
const gcProtectedCTETemplate = `
WITH protected AS (
  SELECT ev.id
  FROM evidence ev
  JOIN run_batch b ON b.id = ev.batch_id
  WHERE b.status IN ('planned', 'running')
  UNION
  SELECT ev.id
  FROM evidence ev
  JOIN (
    SELECT id, row_number() OVER (PARTITION BY version_id ORDER BY batch_no DESC) AS rn
    FROM run_batch
  ) ranked ON ranked.id = ev.batch_id
  WHERE ranked.rn <= %s
  UNION
  SELECT ref.ref_id::bigint
  FROM report_ref ref
  JOIN report rp ON rp.id = ref.report_id
  WHERE ref.ref_kind = 'evidence'
    AND ref.ref_id ~ '^[0-9]+$'
    AND rp.status = 'published'
  UNION
  SELECT ref.ref_id::bigint
  FROM issue_ref ref
  JOIN issue i ON i.id = ref.issue_id
  WHERE ref.ref_kind = 'evidence'
    AND ref.ref_id ~ '^[0-9]+$'
    AND i.status IN ('open', 'confirmed')
  UNION
  SELECT ev.id
  FROM evidence ev
  WHERE ev.kind = 'visual_baseline'
  UNION
  SELECT er.evidence_id
  FROM evidence_ref er
  JOIN report rp ON rp.id = er.ref_id
  WHERE er.ref_kind = 'report' AND rp.status = 'published'
  UNION
  SELECT er.evidence_id
  FROM evidence_ref er
  JOIN issue i ON i.id = er.ref_id
  WHERE er.ref_kind = 'issue' AND i.status IN ('open', 'confirmed')
)`

// gcProtectedCTE returns the protection CTE bound to keepPlaceholder.
func gcProtectedCTE(keepPlaceholder string) string {
	return fmt.Sprintf(gcProtectedCTETemplate, keepPlaceholder)
}

// GCRepository implements application.GCRepository on a pgx querier.
type GCRepository struct {
	q querier
}

// NewGCRepository returns the evidence-GC repository backed by pool.
func NewGCRepository(pool *pgxpool.Pool) application.GCRepository {
	return &GCRepository{q: pool}
}

var _ application.GCRepository = (*GCRepository)(nil)

// ListCandidates returns unpurged evidence older than retentionDays that is not
// in the protection set.
func (r *GCRepository) ListCandidates(ctx context.Context, retentionDays, batchKeep int) ([]application.GCEvidence, error) {
	if retentionDays < 1 {
		retentionDays = 1
	}
	if batchKeep < 1 {
		batchKeep = 1
	}
	rows, err := r.q.Query(ctx, gcProtectedCTE("$2")+`
		SELECT ev.id, ev.batch_id, ev.path, ev.sha256, ev.size_bytes, ev.kind, ev.created_at
		FROM evidence ev
		WHERE ev.purged_at IS NULL
		  AND ev.created_at < now() - ($1::int * interval '1 day')
		  AND ev.id NOT IN (SELECT id FROM protected)
		ORDER BY ev.created_at ASC, ev.id ASC
		LIMIT `+fmt.Sprintf("%d", gcCandidateLimit),
		retentionDays, batchKeep)
	if err != nil {
		return nil, fmt.Errorf("postgres: list gc candidates: %w", err)
	}
	defer rows.Close()

	candidates := make([]application.GCEvidence, 0)
	for rows.Next() {
		var candidate application.GCEvidence
		if err := rows.Scan(&candidate.ID, &candidate.BatchID, &candidate.Path,
			&candidate.SHA256, &candidate.SizeBytes, &candidate.Kind, &candidate.CreatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan gc candidate: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list gc candidates: %w", err)
	}
	return candidates, nil
}

// CountProtected counts the currently unpurged evidence rows in the protection
// set. It is the `protected` number of the dry-run response.
func (r *GCRepository) CountProtected(ctx context.Context, batchKeep int) (int, error) {
	if batchKeep < 1 {
		batchKeep = 1
	}
	var count int
	if err := r.q.QueryRow(ctx, gcProtectedCTE("$1")+`
		SELECT count(*) FROM evidence ev
		WHERE ev.purged_at IS NULL AND ev.id IN (SELECT id FROM protected)`,
		batchKeep).Scan(&count); err != nil {
		return 0, fmt.Errorf("postgres: count protected evidence: %w", err)
	}
	return count, nil
}

// IsProtected re-checks one evidence row against every protection rule. The
// service calls it immediately before deletion so a reference created after the
// candidate listing is still honoured.
func (r *GCRepository) IsProtected(ctx context.Context, evidenceID int64, batchKeep int) (bool, error) {
	if batchKeep < 1 {
		batchKeep = 1
	}
	var protected bool
	err := r.q.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM evidence ev
			LEFT JOIN run_batch b ON b.id = ev.batch_id
			WHERE ev.id = $1 AND (
				ev.purged_at IS NOT NULL
				OR ev.kind = 'visual_baseline'
				OR b.status IN ('planned', 'running')
				OR EXISTS (
					SELECT 1 FROM (
						SELECT id, row_number() OVER (PARTITION BY version_id ORDER BY batch_no DESC) AS rn
						FROM run_batch
					) ranked
					WHERE ranked.id = ev.batch_id AND ranked.rn <= $2
				)
				OR EXISTS (
					SELECT 1 FROM report_ref ref
					JOIN report rp ON rp.id = ref.report_id
					WHERE ref.ref_kind = 'evidence' AND ref.ref_id = ev.id::text AND rp.status = 'published'
				)
				OR EXISTS (
					SELECT 1 FROM issue_ref iref
					JOIN issue i ON i.id = iref.issue_id
					WHERE iref.ref_kind = 'evidence' AND iref.ref_id = ev.id::text
					  AND i.status IN ('open', 'confirmed')
				)
				OR EXISTS (
					SELECT 1 FROM evidence_ref er
					JOIN report rp ON rp.id = er.ref_id
					WHERE er.ref_kind = 'report' AND er.evidence_id = ev.id AND rp.status = 'published'
				)
				OR EXISTS (
					SELECT 1 FROM evidence_ref er
					JOIN issue i ON i.id = er.ref_id
					WHERE er.ref_kind = 'issue' AND er.evidence_id = ev.id
					  AND i.status IN ('open', 'confirmed')
				)
			)
		)`, evidenceID, batchKeep).Scan(&protected)
	if err != nil {
		return false, fmt.Errorf("postgres: re-check evidence protection: %w", err)
	}
	return protected, nil
}

// MarkPurged writes the purge metadata on a still-unpurged row. A zero
// row-count means the row was purged or does not exist; the caller treats it as
// a failure and does not remove the file.
func (r *GCRepository) MarkPurged(ctx context.Context, evidenceID int64, at time.Time, reason string) error {
	tag, err := r.q.Exec(ctx, `
		UPDATE evidence
		SET purged_at = $2, purged_reason = $3, updated_at = $2
		WHERE id = $1 AND purged_at IS NULL`, evidenceID, at, reason)
	if err != nil {
		return fmt.Errorf("postgres: mark evidence purged: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("postgres: evidence %d is not an unpurged row", evidenceID)
	}
	return nil
}

// RollbackPurged clears the mark after a failed file removal. It only clears
// the exact mark this run wrote, so a concurrent purge is never disturbed.
func (r *GCRepository) RollbackPurged(ctx context.Context, evidenceID int64, at time.Time) error {
	if _, err := r.q.Exec(ctx, `
		UPDATE evidence
		SET purged_at = NULL, purged_reason = '', updated_at = now()
		WHERE id = $1 AND purged_at = $2`, evidenceID, at); err != nil {
		return fmt.Errorf("postgres: rollback evidence purge mark: %w", err)
	}
	return nil
}
