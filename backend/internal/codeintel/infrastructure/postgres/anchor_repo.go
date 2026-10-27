package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// anchorColumnList is the shared column order of every anchor query and the
// order scanAnchor expects.
const anchorColumnList = `id, version_id, repo_id, subject_kind, subject_id, file_path,
	start_line, end_line, entity_id, commit_sha, confidence, created_by_kind,
	created_by, created_at`

// AnchorRepo implements domain.AnchorRepository on a pgx pool.
type AnchorRepo struct {
	pool *pgxpool.Pool
}

// NewAnchorRepository returns a code-anchor repository backed by pg.
func NewAnchorRepository(pg *pgxpool.Pool) *AnchorRepo {
	return &AnchorRepo{pool: pg}
}

var _ domain.AnchorRepository = (*AnchorRepo)(nil)

// scanAnchor scans one row in anchorColumnList order.
func scanAnchor(row rowScanner) (domain.CodeAnchor, error) {
	var anchor domain.CodeAnchor
	if err := row.Scan(
		&anchor.ID,
		&anchor.VersionID,
		&anchor.RepoID,
		&anchor.SubjectKind,
		&anchor.SubjectID,
		&anchor.FilePath,
		&anchor.StartLine,
		&anchor.EndLine,
		&anchor.EntityID,
		&anchor.CommitSHA,
		&anchor.Confidence,
		&anchor.CreatedByKind,
		&anchor.CreatedBy,
		&anchor.CreatedAt,
	); err != nil {
		return domain.CodeAnchor{}, err
	}
	return anchor, nil
}

// Create inserts one anchor. A duplicate of the same
// (version, subject kind, subject id, file, line range) point is a 409
// anchor_duplicate; a duplicate id is the generic duplicate_key conflict.
func (r *AnchorRepo) Create(ctx context.Context, anchor domain.CodeAnchor) error {
	confidence := anchor.Confidence
	if confidence == "" {
		confidence = domain.ConfidenceHigh
	}
	createdByKind := anchor.CreatedByKind
	if createdByKind == "" {
		createdByKind = "user"
	}
	const query = `INSERT INTO code_anchors
		(id, version_id, repo_id, subject_kind, subject_id, file_path,
		 start_line, end_line, entity_id, commit_sha, confidence, created_by_kind, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`
	if _, err := r.pool.Exec(ctx, query,
		anchor.ID, anchor.VersionID, anchor.RepoID, anchor.SubjectKind,
		anchor.SubjectID, anchor.FilePath, anchor.StartLine, anchor.EndLine,
		anchor.EntityID, anchor.CommitSHA, confidence, createdByKind, anchor.CreatedBy,
	); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode {
			if pgErr.ConstraintName == "code_anchors_pkey" {
				return domain.NewConflict("duplicate_key",
					fmt.Sprintf("codeintel: anchor %d already exists", anchor.ID))
			}
			return domain.NewConflict("anchor_duplicate",
				"codeintel: an anchor with the same subject, file and line range already exists")
		}
		return fmt.Errorf("postgres: create anchor: %w", err)
	}
	return nil
}

// Delete removes one anchor of versionID.
func (r *AnchorRepo) Delete(ctx context.Context, versionID, id int64) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM code_anchors WHERE id = $1 AND version_id = $2`, id, versionID)
	if err != nil {
		return fmt.Errorf("postgres: delete anchor: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return notFoundError("anchor", id)
	}
	return nil
}

// Get returns one anchor of versionID.
func (r *AnchorRepo) Get(ctx context.Context, versionID, id int64) (domain.CodeAnchor, error) {
	anchor, err := scanAnchor(r.pool.QueryRow(ctx,
		`SELECT `+anchorColumnList+`
		 FROM code_anchors WHERE id = $1 AND version_id = $2`, id, versionID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CodeAnchor{}, notFoundError("anchor", id)
		}
		return domain.CodeAnchor{}, fmt.Errorf("postgres: get anchor: %w", err)
	}
	return anchor, nil
}

// List returns the anchors of versionID ordered by id. An empty SubjectKind
// matches every kind; a nil SubjectID matches every subject.
func (r *AnchorRepo) List(ctx context.Context, versionID int64, filter domain.AnchorFilter) ([]domain.CodeAnchor, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+anchorColumnList+`
		 FROM code_anchors
		 WHERE version_id = $1
		   AND ($2 = '' OR subject_kind = $2)
		   AND ($3::bigint IS NULL OR subject_id = $3)
		 ORDER BY id ASC`,
		versionID, filter.SubjectKind, filter.SubjectID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list anchors: %w", err)
	}
	return scanAnchors(rows)
}

// ListByPaths returns the anchors of versionID whose file is one of paths,
// ordered by id. It backs the direct-anchor impact candidates of the changed
// files.
func (r *AnchorRepo) ListByPaths(ctx context.Context, versionID int64, paths []string) ([]domain.CodeAnchor, error) {
	if len(paths) == 0 {
		return []domain.CodeAnchor{}, nil
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+anchorColumnList+`
		 FROM code_anchors
		 WHERE version_id = $1 AND file_path = ANY($2::text[])
		 ORDER BY id ASC`,
		versionID, paths)
	if err != nil {
		return nil, fmt.Errorf("postgres: list anchors by path: %w", err)
	}
	return scanAnchors(rows)
}

// scanAnchors drains an anchor query.
func scanAnchors(rows pgx.Rows) ([]domain.CodeAnchor, error) {
	defer rows.Close()
	anchors := make([]domain.CodeAnchor, 0)
	for rows.Next() {
		anchor, err := scanAnchor(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan anchor: %w", err)
		}
		anchors = append(anchors, anchor)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list anchors: %w", err)
	}
	return anchors, nil
}
