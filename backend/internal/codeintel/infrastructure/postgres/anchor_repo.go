package codeintelpg

import (
	"context"
	"errors"
	"time"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AnchorRepository stores unified code_anchor rows (spec §3.4). The partial
// unique expression index uk_code_anchor_subject_point is mapped to
// codeintel.ErrConflict so the service can answer 409 anchor_duplicate.
type AnchorRepository struct {
	pool *pgxpool.Pool
}

// NewAnchorRepository wires the code-anchor repository.
func NewAnchorRepository(pool *pgxpool.Pool) *AnchorRepository {
	return &AnchorRepository{pool: pool}
}

var _ codeintelapp.AnchorRepository = (*AnchorRepository)(nil)

const anchorColumns = `id, version_id, repo_id, subject_kind, subject_id, file_path,
	start_line, end_line, entity_id, commit_sha, excerpt_sha, status, confidence,
	created_by_kind, created_by, last_validated_at, created_at, updated_at`

func scanAnchor(row rowScanner) (codeintel.Anchor, error) {
	var anchor codeintel.Anchor
	var subjectKind, status, confidence, createdByKind string
	var commitSHA *string
	if err := row.Scan(&anchor.ID, &anchor.VersionID, &anchor.RepoID, &subjectKind, &anchor.SubjectID,
		&anchor.FilePath, &anchor.StartLine, &anchor.EndLine, &anchor.EntityID, &commitSHA,
		&anchor.ExcerptSHA, &status, &confidence, &createdByKind, &anchor.CreatedBy,
		&anchor.LastValidatedAt, &anchor.CreatedAt, &anchor.UpdatedAt); err != nil {
		return codeintel.Anchor{}, err
	}
	anchor.SubjectKind = codeintel.AnchorSubject(subjectKind)
	anchor.Status = status
	anchor.Confidence = confidence
	anchor.CreatedByKind = createdByKind
	if commitSHA != nil {
		anchor.CommitSHA = *commitSHA
	}
	return anchor, nil
}

// Create inserts one anchor; a duplicate subject point maps to ErrConflict.
func (r *AnchorRepository) Create(ctx context.Context, anchor codeintel.Anchor) (codeintel.Anchor, error) {
	created, err := scanAnchor(r.pool.QueryRow(ctx, `
		INSERT INTO code_anchor (id, version_id, repo_id, subject_kind, subject_id, file_path,
			start_line, end_line, entity_id, commit_sha, excerpt_sha, status, confidence,
			created_by_kind, created_by, last_validated_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
		RETURNING `+anchorColumns,
		anchor.ID, anchor.VersionID, anchor.RepoID, string(anchor.SubjectKind), anchor.SubjectID,
		anchor.FilePath, anchor.StartLine, anchor.EndLine, anchor.EntityID, nullableString(anchor.CommitSHA),
		anchor.ExcerptSHA, anchor.Status, anchor.Confidence, anchor.CreatedByKind, anchor.CreatedBy,
		anchor.LastValidatedAt, anchor.CreatedAt, anchor.UpdatedAt))
	if err != nil {
		if isUniqueViolation(err, "uk_code_anchor_subject_point") {
			return codeintel.Anchor{}, codeintel.ErrConflict
		}
		return codeintel.Anchor{}, wrapDB("create code anchor", err)
	}
	return created, nil
}

// List returns the version anchors in creation order, optionally filtered by
// subject kind/id.
func (r *AnchorRepository) List(ctx context.Context, versionID int64, subjectKind string, subjectID int64) ([]codeintel.Anchor, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+anchorColumns+` FROM code_anchor
		WHERE version_id = $1
		  AND ($2::text = '' OR subject_kind = $2::text)
		  AND ($3::bigint = 0 OR subject_id = $3::bigint)
		ORDER BY id ASC
	`, versionID, subjectKind, subjectID)
	if err != nil {
		return nil, wrapDB("list code anchors", err)
	}
	defer rows.Close()
	anchors := make([]codeintel.Anchor, 0)
	for rows.Next() {
		anchor, scanErr := scanAnchor(rows)
		if scanErr != nil {
			return nil, wrapDB("scan code anchor", scanErr)
		}
		anchors = append(anchors, anchor)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("list code anchors", err)
	}
	return anchors, nil
}

// Get returns one anchor or ErrNotFound.
func (r *AnchorRepository) Get(ctx context.Context, id int64) (codeintel.Anchor, error) {
	anchor, err := scanAnchor(r.pool.QueryRow(ctx, `
		SELECT `+anchorColumns+` FROM code_anchor WHERE id = $1
	`, id))
	if err != nil {
		return codeintel.Anchor{}, wrapDB("get code anchor", err)
	}
	return anchor, nil
}

// UpdateStatus persists the re-validated status and timestamp.
func (r *AnchorRepository) UpdateStatus(ctx context.Context, id int64, status string, validatedAt time.Time) (codeintel.Anchor, error) {
	anchor, err := scanAnchor(r.pool.QueryRow(ctx, `
		UPDATE code_anchor
		SET status = $2, last_validated_at = $3, updated_at = $3
		WHERE id = $1
		RETURNING `+anchorColumns,
		id, status, validatedAt))
	if err != nil {
		return codeintel.Anchor{}, wrapDB("update code anchor status", err)
	}
	return anchor, nil
}

// Delete removes one anchor.
func (r *AnchorRepository) Delete(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM code_anchor WHERE id = $1`, id)
	if err != nil {
		return wrapDB("delete code anchor", err)
	}
	if tag.RowsAffected() == 0 {
		return codeintel.ErrNotFound
	}
	return nil
}

// nullableString renders an optional text parameter.
func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// isUniqueViolation reports whether err is a duplicate-key error of constraint.
func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return constraint == "" || pgErr.ConstraintName == constraint
}
