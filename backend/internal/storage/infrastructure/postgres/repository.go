// Package postgres implements the storage metadata repository port with
// PostgreSQL.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
)

// metadataColumns is the column list shared by every storage_object query and
// the row order scanMetadata expects.
const metadataColumns = `sha256, size, content_type, relative_path, created_at`

// MetadataRepository implements storageapp.MetadataRepository on a pgx pool.
// The table is append-only: Insert never updates an existing row.
type MetadataRepository struct {
	pool *pgxpool.Pool
}

// NewMetadataRepository returns a storage metadata repository backed by pool.
func NewMetadataRepository(pool *pgxpool.Pool) *MetadataRepository {
	return &MetadataRepository{pool: pool}
}

// Insert persists one object's metadata and reports whether a new row was
// created. The sha256 primary key makes the insert idempotent: uploading bytes
// that already have a row does nothing and returns false.
func (r *MetadataRepository) Insert(ctx context.Context, metadata storageapp.ObjectMetadata) (bool, error) {
	const query = `INSERT INTO storage_object (sha256, size, content_type, relative_path)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (sha256) DO NOTHING`
	tag, err := r.pool.Exec(ctx, query,
		metadata.SHA256,
		metadata.Size,
		metadata.ContentType,
		metadata.RelativePath,
	)
	if err != nil {
		return false, fmt.Errorf("postgres: insert storage object metadata: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// Find returns the stored metadata for sha256. ok is false when no row exists.
func (r *MetadataRepository) Find(ctx context.Context, sha256 string) (storageapp.ObjectMetadata, bool, error) {
	const query = `SELECT ` + metadataColumns + ` FROM storage_object WHERE sha256 = $1`
	var metadata storageapp.ObjectMetadata
	err := r.pool.QueryRow(ctx, query, sha256).Scan(
		&metadata.SHA256,
		&metadata.Size,
		&metadata.ContentType,
		&metadata.RelativePath,
		&metadata.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return storageapp.ObjectMetadata{}, false, nil
	}
	if err != nil {
		return storageapp.ObjectMetadata{}, false, fmt.Errorf("postgres: find storage object metadata: %w", err)
	}
	return metadata, true, nil
}
