package storagepg

import (
	"context"
	"errors"
	"fmt"

	"github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository stores content objects and references in PostgreSQL.
type Repository struct {
	pool *pgxpool.Pool
}

// New wraps a pgx pool.
func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// PutObject inserts an object with ON CONFLICT (sha256) DO NOTHING and returns
// the stored row plus whether this call created it.
func (r *Repository) PutObject(ctx context.Context, object storage.Object) (storage.Object, bool, error) {
	var stored storage.Object
	err := r.pool.QueryRow(ctx, `
		INSERT INTO content_object (sha256, size, mime, relative_path)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (sha256) DO NOTHING
		RETURNING sha256, size, mime, relative_path, created_at
	`, object.SHA256, object.Size, object.MIME, object.RelativePath).Scan(
		&stored.SHA256, &stored.Size, &stored.MIME, &stored.RelativePath, &stored.CreatedAt,
	)
	if err == nil {
		return stored, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return storage.Object{}, false, fmt.Errorf("insert content object: %w", err)
	}
	stored, err = r.GetObject(ctx, object.SHA256)
	if err != nil {
		return storage.Object{}, false, err
	}
	return stored, false, nil
}

// GetObject returns one object by SHA-256.
func (r *Repository) GetObject(ctx context.Context, sha256 string) (storage.Object, error) {
	var object storage.Object
	err := r.pool.QueryRow(ctx, `
		SELECT sha256, size, mime, relative_path, created_at
		FROM content_object
		WHERE sha256 = $1
	`, sha256).Scan(&object.SHA256, &object.Size, &object.MIME, &object.RelativePath, &object.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return storage.Object{}, storage.ErrNotFound
	}
	if err != nil {
		return storage.Object{}, fmt.Errorf("read content object: %w", err)
	}
	return object, nil
}

// AddRef inserts a reference row and returns it with the database timestamp.
func (r *Repository) AddRef(ctx context.Context, ref storage.Ref) (storage.Ref, error) {
	err := r.pool.QueryRow(ctx, `
		INSERT INTO content_ref (id, object_id, owner_kind, owner_id, role)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING created_at
	`, ref.ID, ref.ObjectID, ref.OwnerKind, ref.OwnerID, ref.Role).Scan(&ref.CreatedAt)
	if err != nil {
		return storage.Ref{}, fmt.Errorf("insert content ref: %w", err)
	}
	return ref, nil
}

// ListRefs returns active references for one owner ordered by ID.
func (r *Repository) ListRefs(ctx context.Context, ownerKind string, ownerID int64) ([]storage.Ref, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, object_id, owner_kind, owner_id, role, created_at, deleted_at
		FROM content_ref
		WHERE owner_kind = $1 AND owner_id = $2 AND deleted_at IS NULL
		ORDER BY id ASC
	`, ownerKind, ownerID)
	if err != nil {
		return nil, fmt.Errorf("query content refs: %w", err)
	}
	defer rows.Close()

	refs := make([]storage.Ref, 0)
	for rows.Next() {
		var ref storage.Ref
		if err := rows.Scan(&ref.ID, &ref.ObjectID, &ref.OwnerKind, &ref.OwnerID, &ref.Role, &ref.CreatedAt, &ref.DeletedAt); err != nil {
			return nil, fmt.Errorf("scan content ref: %w", err)
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan content refs: %w", err)
	}
	return refs, nil
}

// SoftDeleteRef marks a reference deleted. Deleting an already-deleted or
// missing reference reports ErrNotFound.
func (r *Repository) SoftDeleteRef(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE content_ref SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL
	`, id)
	if err != nil {
		return fmt.Errorf("soft delete content ref: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return nil
}
