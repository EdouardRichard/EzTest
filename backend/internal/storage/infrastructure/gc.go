package storagepg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GCRepository persists gc_run/gc_tombstone and provides the paged ledger scans
// used by the GC service. Owner existence checks deliberately stay in the
// application (OwnerLookup), never in these queries.
type GCRepository struct {
	pool *pgxpool.Pool
}

var _ storageapp.GCRepository = (*GCRepository)(nil)

// NewGCRepository wraps a pgx pool.
func NewGCRepository(pool *pgxpool.Pool) *GCRepository {
	return &GCRepository{pool: pool}
}

// CreateRun inserts one running run. Before the insert, inside the same
// transaction, an expired running row is reclaimed as failed (lease cleared):
// the previous process is gone, so keeping the uk_gc_run_running index held
// would wedge every later pass with 409 gc_running for ever. A live run keeps
// the index and still maps to domain.ErrGCRunning.
func (r *GCRepository) CreateRun(ctx context.Context, run storage.GCRun) (storage.GCRun, error) {
	report, err := gcReportJSON(run.Report)
	if err != nil {
		return storage.GCRun{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return storage.GCRun{}, fmt.Errorf("insert gc run: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Reclaim every stale running row (there can be at most one): lease_until
	// in the past, or a legacy row that predates lease stamping and is older
	// than the fixed GCLease window.
	now := time.Now().UTC()
	if _, err := tx.Exec(ctx, `
		UPDATE gc_run
		SET status = 'failed',
		    error = 'gc run lease expired (previous process stopped)',
		    finished_at = $1,
		    lease_owner = NULL,
		    lease_until = NULL,
		    updated_at = $1
		WHERE status = 'running'
		  AND (
		        (lease_until IS NOT NULL AND lease_until < $1)
		     OR (lease_until IS NULL AND started_at < $2)
		  )
	`, now, now.Add(-storageapp.GCLease)); err != nil {
		return storage.GCRun{}, fmt.Errorf("reclaim stale gc run: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO gc_run (
			id, mode, status, actor_id, actor_kind, lease_owner, lease_until,
			started_at, finished_at,
			scanned_objects, scanned_refs, deleted_objects, deleted_refs, deleted_bytes,
			report, error, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::jsonb,$16,now(),now())
	`, run.ID, string(run.Mode), string(run.Status), run.ActorID, string(run.ActorKind),
		nullableString(run.LeaseOwner), run.LeaseUntil, run.StartedAt, run.FinishedAt,
		run.ScannedObjects, run.ScannedRefs, run.DeletedObjects, run.DeletedRefs, run.DeletedBytes,
		report, nullableString(run.Error))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "uk_gc_run_running" {
			return storage.GCRun{}, storage.ErrGCRunning
		}
		return storage.GCRun{}, fmt.Errorf("insert gc run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return storage.GCRun{}, fmt.Errorf("insert gc run: commit: %w", err)
	}
	return run, nil
}

// UpdateRun persists a run's terminal state and report, clearing its lease.
func (r *GCRepository) UpdateRun(ctx context.Context, run storage.GCRun) error {
	report, err := gcReportJSON(run.Report)
	if err != nil {
		return err
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE gc_run
		SET status = $2, finished_at = $3, scanned_objects = $4, scanned_refs = $5,
		    deleted_objects = $6, deleted_refs = $7, deleted_bytes = $8,
		    report = $9::jsonb, error = $10,
		    lease_owner = NULL, lease_until = NULL,
		    updated_at = now()
		WHERE id = $1
	`, run.ID, string(run.Status), run.FinishedAt, run.ScannedObjects, run.ScannedRefs,
		run.DeletedObjects, run.DeletedRefs, run.DeletedBytes, report, nullableString(run.Error))
	if err != nil {
		return fmt.Errorf("update gc run: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return nil
}

// GetRun returns one run or domain.ErrNotFound.
func (r *GCRepository) GetRun(ctx context.Context, id int64) (storage.GCRun, error) {
	var run storage.GCRun
	var mode, status, actorKind string
	var report []byte
	var errorText *string
	err := r.pool.QueryRow(ctx, `
		SELECT id, mode, status, actor_id, actor_kind, started_at, finished_at,
		       scanned_objects, scanned_refs, deleted_objects, deleted_refs, deleted_bytes,
		       report, error
		FROM gc_run
		WHERE id = $1
	`, id).Scan(
		&run.ID, &mode, &status, &run.ActorID, &actorKind, &run.StartedAt, &run.FinishedAt,
		&run.ScannedObjects, &run.ScannedRefs, &run.DeletedObjects, &run.DeletedRefs, &run.DeletedBytes,
		&report, &errorText,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return storage.GCRun{}, storage.ErrNotFound
	}
	if err != nil {
		return storage.GCRun{}, fmt.Errorf("read gc run: %w", err)
	}
	run.Mode = storage.GCMode(mode)
	run.Status = storage.GCRunStatus(status)
	run.ActorKind = storage.GCActorKind(actorKind)
	run.Report = append(json.RawMessage(nil), report...)
	if errorText != nil {
		run.Error = *errorText
	}
	return run, nil
}

// ListRuns returns runs in ID-descending order.
func (r *GCRepository) ListRuns(ctx context.Context, limit int) ([]storage.GCRun, error) {
	if limit <= 0 {
		limit = storageapp.DefaultGCRunLimit
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, mode, status, actor_id, actor_kind, started_at, finished_at,
		       scanned_objects, scanned_refs, deleted_objects, deleted_refs, deleted_bytes,
		       report, error
		FROM gc_run
		ORDER BY id DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list gc runs: %w", err)
	}
	defer rows.Close()
	runs := make([]storage.GCRun, 0)
	for rows.Next() {
		var run storage.GCRun
		var mode, status, actorKind string
		var report []byte
		var errorText *string
		if err := rows.Scan(
			&run.ID, &mode, &status, &run.ActorID, &actorKind, &run.StartedAt, &run.FinishedAt,
			&run.ScannedObjects, &run.ScannedRefs, &run.DeletedObjects, &run.DeletedRefs, &run.DeletedBytes,
			&report, &errorText,
		); err != nil {
			return nil, fmt.Errorf("scan gc run: %w", err)
		}
		run.Mode = storage.GCMode(mode)
		run.Status = storage.GCRunStatus(status)
		run.ActorKind = storage.GCActorKind(actorKind)
		run.Report = append(json.RawMessage(nil), report...)
		if errorText != nil {
			run.Error = *errorText
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan gc runs: %w", err)
	}
	return runs, nil
}

// CreateTombstones appends tombstones in one batch.
func (r *GCRepository) CreateTombstones(ctx context.Context, tombstones []storage.GCTombstone) error {
	if len(tombstones) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, tombstone := range tombstones {
		batch.Queue(`
			INSERT INTO gc_tombstone (
				id, run_id, object_sha, size_bytes, mime, relative_path, reason,
				deleted_at, created_at, updated_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now(),now())
		`, tombstone.ID, tombstone.RunID, tombstone.ObjectSHA, tombstone.Size, tombstone.MIME,
			tombstone.RelativePath, tombstone.Reason, tombstone.DeletedAt)
	}
	results := r.pool.SendBatch(ctx, batch)
	defer results.Close()
	for range tombstones {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("insert gc tombstone: %w", err)
		}
	}
	return nil
}

// ListTombstones returns one run's tombstones in ID order.
func (r *GCRepository) ListTombstones(ctx context.Context, runID int64) ([]storage.GCTombstone, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, run_id, object_sha, size_bytes, mime, relative_path, reason, deleted_at
		FROM gc_tombstone
		WHERE run_id = $1
		ORDER BY id ASC
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("list gc tombstones: %w", err)
	}
	defer rows.Close()
	tombstones := make([]storage.GCTombstone, 0)
	for rows.Next() {
		var tombstone storage.GCTombstone
		if err := rows.Scan(
			&tombstone.ID, &tombstone.RunID, &tombstone.ObjectSHA, &tombstone.Size,
			&tombstone.MIME, &tombstone.RelativePath, &tombstone.Reason, &tombstone.DeletedAt,
		); err != nil {
			return nil, fmt.Errorf("scan gc tombstone: %w", err)
		}
		tombstones = append(tombstones, tombstone)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan gc tombstones: %w", err)
	}
	return tombstones, nil
}

// ListRefsPage returns live refs with ID > afterID, joined with object metadata.
func (r *GCRepository) ListRefsPage(ctx context.Context, afterID int64, limit int) ([]storageapp.GCRef, error) {
	if limit <= 0 {
		limit = storageapp.DefaultGCBatchSize
	}
	rows, err := r.pool.Query(ctx, `
		SELECT r.id, r.object_id, o.size, o.mime, o.relative_path,
		       r.owner_kind, r.owner_id, r.role, o.created_at, r.created_at
		FROM content_ref r
		JOIN content_object o ON o.sha256 = r.object_id
		WHERE r.deleted_at IS NULL AND r.id > $1
		ORDER BY r.id ASC
		LIMIT $2
	`, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list content refs page: %w", err)
	}
	defer rows.Close()
	refs := make([]storageapp.GCRef, 0)
	for rows.Next() {
		var ref storageapp.GCRef
		if err := rows.Scan(
			&ref.RefID, &ref.ObjectSHA, &ref.Size, &ref.MIME, &ref.RelativePath,
			&ref.OwnerKind, &ref.OwnerID, &ref.Role, &ref.ObjectCreatedAt, &ref.RefCreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan content ref page: %w", err)
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan content refs page: %w", err)
	}
	return refs, nil
}

// LiveRefCounts returns the live-ref count per object SHA (missing SHAs = 0).
func (r *GCRepository) LiveRefCounts(ctx context.Context, shas []string) (map[string]int, error) {
	counts := make(map[string]int, len(shas))
	for _, sha := range shas {
		counts[sha] = 0
	}
	if len(shas) == 0 {
		return counts, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT object_id, count(*)
		FROM content_ref
		WHERE deleted_at IS NULL AND object_id = ANY($1)
		GROUP BY object_id
	`, shas)
	if err != nil {
		return nil, fmt.Errorf("read live ref counts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sha string
		var count int
		if err := rows.Scan(&sha, &count); err != nil {
			return nil, fmt.Errorf("scan live ref count: %w", err)
		}
		counts[sha] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan live ref counts: %w", err)
	}
	return counts, nil
}

// DeleteRefs soft-deletes refs; missing/already-deleted ids are ignored so
// concurrent GC passes converge.
func (r *GCRepository) DeleteRefs(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	if _, err := r.pool.Exec(ctx, `
		UPDATE content_ref SET deleted_at = now()
		WHERE id = ANY($1) AND deleted_at IS NULL
	`, ids); err != nil {
		return fmt.Errorf("soft delete content refs: %w", err)
	}
	return nil
}

// ListUnreferencedObjects returns objects without live refs older than the
// cutoff, oldest first.
func (r *GCRepository) ListUnreferencedObjects(ctx context.Context, olderThan time.Time, limit int) ([]storage.Object, error) {
	if limit <= 0 {
		limit = storageapp.DefaultGCBatchSize
	}
	rows, err := r.pool.Query(ctx, `
		SELECT o.sha256, o.size, o.mime, o.relative_path, o.created_at
		FROM content_object o
		WHERE o.created_at < $1
		  AND NOT EXISTS (
		      SELECT 1 FROM content_ref r
		      WHERE r.object_id = o.sha256 AND r.deleted_at IS NULL
		  )
		ORDER BY o.created_at ASC, o.sha256 ASC
		LIMIT $2
	`, olderThan, limit)
	if err != nil {
		return nil, fmt.Errorf("list unreferenced objects: %w", err)
	}
	defer rows.Close()
	objects := make([]storage.Object, 0)
	for rows.Next() {
		var object storage.Object
		if err := rows.Scan(&object.SHA256, &object.Size, &object.MIME, &object.RelativePath, &object.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan unreferenced object: %w", err)
		}
		objects = append(objects, object)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan unreferenced objects: %w", err)
	}
	return objects, nil
}

// DeleteObjects removes the objects that still have no live ref, deleting their
// soft-deleted ref rows first (the content_ref FK requires it), and returns the
// SHAs it actually removed. Live-referenced objects are left untouched.
func (r *GCRepository) DeleteObjects(ctx context.Context, shas []string) ([]string, error) {
	if len(shas) == 0 {
		return []string{}, nil
	}
	rows, err := r.pool.Query(ctx, `
		WITH removable AS (
			SELECT o.sha256
			FROM content_object o
			WHERE o.sha256 = ANY($1)
			  AND NOT EXISTS (
			      SELECT 1 FROM content_ref r
			      WHERE r.object_id = o.sha256 AND r.deleted_at IS NULL
			  )
		), dead_refs AS (
			DELETE FROM content_ref r
			USING removable
			WHERE r.object_id = removable.sha256
		)
		DELETE FROM content_object o
		USING removable
		WHERE o.sha256 = removable.sha256
		RETURNING o.sha256
	`, shas)
	if err != nil {
		return nil, fmt.Errorf("delete content objects: %w", err)
	}
	defer rows.Close()
	deleted := make([]string, 0, len(shas))
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, fmt.Errorf("scan deleted object: %w", err)
		}
		deleted = append(deleted, sha)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan deleted objects: %w", err)
	}
	return deleted, nil
}

// ListSupersededExports returns replaced export artifacts older than the
// cutoff joined with the content_ref that owns their bytes.
func (r *GCRepository) ListSupersededExports(ctx context.Context, olderThan time.Time, limit int) ([]storageapp.SupersededExport, error) {
	if limit <= 0 {
		limit = storageapp.DefaultGCBatchSize
	}
	rows, err := r.pool.Query(ctx, `
		SELECT a.id, a.version_id, a.owner_kind, a.owner_id, a.file_format, a.revision,
		       COALESCE(r.id, 0), o.sha256, o.size, o.mime, o.relative_path,
		       a.created_at, o.created_at
		FROM export_artifact a
		JOIN content_object o ON o.sha256 = a.content_object_id
		LEFT JOIN content_ref r
		       ON r.object_id = a.content_object_id
		      AND r.owner_kind = 'export'
		      AND r.owner_id = a.id
		      AND r.deleted_at IS NULL
		WHERE a.created_at < $1
		  AND a.revision < (
		      SELECT MAX(a2.revision) FROM export_artifact a2
		      WHERE a2.version_id = a.version_id
		        AND a2.owner_kind = a.owner_kind
		        AND a2.owner_id = a.owner_id
		        AND a2.file_format = a.file_format
		  )
		ORDER BY a.created_at ASC, a.id ASC
		LIMIT $2
	`, olderThan, limit)
	if err != nil {
		return nil, fmt.Errorf("list superseded exports: %w", err)
	}
	defer rows.Close()
	artifacts := make([]storageapp.SupersededExport, 0)
	for rows.Next() {
		var artifact storageapp.SupersededExport
		if err := rows.Scan(
			&artifact.ArtifactID, &artifact.VersionID, &artifact.OwnerKind, &artifact.OwnerID,
			&artifact.Format, &artifact.Revision, &artifact.RefID, &artifact.ObjectSHA,
			&artifact.Size, &artifact.MIME, &artifact.RelativePath,
			&artifact.ArtifactCreatedAt, &artifact.ObjectCreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan superseded export: %w", err)
		}
		artifacts = append(artifacts, artifact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan superseded exports: %w", err)
	}
	return artifacts, nil
}

// ExistingObjectSHAs returns the subset of shas present in content_object.
func (r *GCRepository) ExistingObjectSHAs(ctx context.Context, shas []string) ([]string, error) {
	if len(shas) == 0 {
		return []string{}, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT sha256 FROM content_object WHERE sha256 = ANY($1)
	`, shas)
	if err != nil {
		return nil, fmt.Errorf("read existing object shas: %w", err)
	}
	defer rows.Close()
	existing := make([]string, 0, len(shas))
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, fmt.Errorf("scan existing object sha: %w", err)
		}
		existing = append(existing, sha)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan existing object shas: %w", err)
	}
	return existing, nil
}

func gcReportJSON(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "{}", nil
	}
	if !json.Valid(raw) {
		return "", fmt.Errorf("gc report is not valid JSON")
	}
	return string(raw), nil
}

func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
