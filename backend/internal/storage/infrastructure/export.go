package storagepg

import (
	"context"
	"errors"
	"fmt"

	"github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ExportRepository stores export-artifact metadata rows.
type ExportRepository struct {
	pool *pgxpool.Pool
}

// NewExportRepository wraps a pgx pool.
func NewExportRepository(pool *pgxpool.Pool) *ExportRepository {
	return &ExportRepository{pool: pool}
}

// Create inserts one artifact row with its caller-assigned ID and revision.
func (r *ExportRepository) Create(ctx context.Context, artifact storage.ExportArtifact) (storage.ExportArtifact, error) {
	err := r.pool.QueryRow(ctx, `
		INSERT INTO export_artifact (
			id, version_id, owner_kind, owner_id, revision, file_format,
			content_object_id, size_bytes, mime, filename, status, created_by, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13)
		RETURNING created_at
	`, artifact.ID, artifact.VersionID, artifact.OwnerKind, artifact.OwnerID, artifact.Revision,
		artifact.Format, artifact.ContentObjectID, artifact.Size, artifact.MIME, artifact.Filename,
		artifact.Status, artifact.CreatedBy, artifact.CreatedAt).Scan(&artifact.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return storage.ExportArtifact{}, storage.ErrDuplicate
		}
		return storage.ExportArtifact{}, fmt.Errorf("insert export artifact: %w", err)
	}
	return artifact, nil
}

// Get returns one artifact by ID.
func (r *ExportRepository) Get(ctx context.Context, exportID int64) (storage.ExportArtifact, error) {
	var artifact storage.ExportArtifact
	err := r.pool.QueryRow(ctx, `
		SELECT id, version_id, owner_kind, owner_id, revision, file_format,
		       content_object_id, size_bytes, mime, filename, status, created_by, created_at
		FROM export_artifact
		WHERE id = $1
	`, exportID).Scan(
		&artifact.ID, &artifact.VersionID, &artifact.OwnerKind, &artifact.OwnerID,
		&artifact.Revision, &artifact.Format, &artifact.ContentObjectID, &artifact.Size,
		&artifact.MIME, &artifact.Filename, &artifact.Status, &artifact.CreatedBy, &artifact.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return storage.ExportArtifact{}, storage.ErrNotFound
	}
	if err != nil {
		return storage.ExportArtifact{}, fmt.Errorf("read export artifact: %w", err)
	}
	return artifact, nil
}

// MaxRevision returns the highest revision of one owner/format tuple.
func (r *ExportRepository) MaxRevision(ctx context.Context, versionID int64, ownerKind string, ownerID int64, format string) (int, error) {
	var maxRevision int
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(MAX(revision), 0)
		FROM export_artifact
		WHERE version_id = $1 AND owner_kind = $2 AND owner_id = $3 AND file_format = $4
	`, versionID, ownerKind, ownerID, format).Scan(&maxRevision)
	if err != nil {
		return 0, fmt.Errorf("read export max revision: %w", err)
	}
	return maxRevision, nil
}

// List returns artifacts of a version in ID-descending order.
func (r *ExportRepository) List(ctx context.Context, versionID int64, format string, limit int, beforeID *int64) ([]storage.ExportArtifact, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, version_id, owner_kind, owner_id, revision, file_format,
		       content_object_id, size_bytes, mime, filename, status, created_by, created_at
		FROM export_artifact
		WHERE version_id = $1
		  AND ($2 = '' OR file_format = $2)
		  AND ($3::bigint IS NULL OR id < $3)
		ORDER BY id DESC
		LIMIT $4
	`, versionID, format, beforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("list export artifacts: %w", err)
	}
	defer rows.Close()
	artifacts := make([]storage.ExportArtifact, 0)
	for rows.Next() {
		var artifact storage.ExportArtifact
		if err := rows.Scan(
			&artifact.ID, &artifact.VersionID, &artifact.OwnerKind, &artifact.OwnerID,
			&artifact.Revision, &artifact.Format, &artifact.ContentObjectID, &artifact.Size,
			&artifact.MIME, &artifact.Filename, &artifact.Status, &artifact.CreatedBy, &artifact.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan export artifact: %w", err)
		}
		artifacts = append(artifacts, artifact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan export artifacts: %w", err)
	}
	return artifacts, nil
}
