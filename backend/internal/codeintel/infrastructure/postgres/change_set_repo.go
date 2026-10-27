// Package postgres implements the codeintel context's repository ports with
// PostgreSQL. P4 ids are Snowflake values generated in the application layer
// (platform/id); repositories never mint ids. Every insert therefore supplies
// the caller's id explicitly, and payloads that carry the id reject a zero id
// with ErrInvalid and the details.reason code missing_id (D29). JSONB documents
// are exchanged as raw JSON bytes and never use a cross-context foreign key
// (design spec §5, §6.11).
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// uniqueViolationCode is PostgreSQL's SQLSTATE for unique_violation.
const uniqueViolationCode = "23505"

// jsonFallbackEmptyArray and jsonFallbackEmptyObject keep a JSONB NOT NULL
// column valid when the caller supplies no document and lets the insert keep
// the column's declared default instead of writing SQL NULL.
const (
	jsonFallbackEmptyArray  = "[]"
	jsonFallbackEmptyObject = "{}"
)

// rowScanner is the subset of pgx.Row and pgx.Rows used to scan one row.
type rowScanner interface {
	Scan(dest ...any) error
}

// jsonParam renders a caller-supplied JSONB document as a query parameter.
// pgx sends the string as JSON text when the statement casts the parameter to
// jsonb; an absent document falls back to fallback so NOT NULL JSONB columns
// keep their declared default.
func jsonParam(raw []byte, fallback string) string {
	if len(raw) == 0 {
		return fallback
	}
	return string(raw)
}

// isUniqueViolation reports whether err is a PostgreSQL unique violation.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode
}

// notFoundError builds the sentinel-classified not-found error of one
// repository resource. The reason is the §7.1 resource-specific code, for
// example analysis_not_found.
func notFoundError(resource string, id int64) error {
	return domain.NewNotFound(resource+"_not_found", fmt.Sprintf("codeintel: %s %d not found", resource, id))
}

// missingIDError builds the ErrInvalid/missing_id rejection of a repository
// write whose payload carries no application-generated Snowflake id (D29).
func missingIDError(resource string) error {
	return domain.NewInvalid("id", "missing_id", fmt.Sprintf("codeintel: %s id is required", resource))
}

// changeSetColumnList is the shared column order of every change-set query and
// the order scanChangeSet expects.
const changeSetColumnList = `id, repo_id, base_commit, head_commit, stats, changed_files, truncated, created_at`

// changedFileJSON is the persisted shape of one changed_files entry. The
// domain.ChangedFile type has no JSON tags, so the repository owns the stored
// document shape; it matches the API field names.
type changedFileJSON struct {
	Path         string  `json:"path"`
	Status       string  `json:"status"`
	OldPath      *string `json:"oldPath"`
	Additions    int     `json:"additions"`
	Deletions    int     `json:"deletions"`
	Binary       bool    `json:"binary"`
	HeadBlobHash string  `json:"headBlobHash"`
}

// marshalChangedFiles encodes the changed files as the JSONB document stored in
// code_change_sets.changed_files. An empty list is stored as [].
func marshalChangedFiles(files []domain.ChangedFile) (string, error) {
	entries := make([]changedFileJSON, 0, len(files))
	for _, file := range files {
		entries = append(entries, changedFileJSON{
			Path:         file.Path,
			Status:       file.Status,
			OldPath:      file.OldPath,
			Additions:    file.Additions,
			Deletions:    file.Deletions,
			Binary:       file.Binary,
			HeadBlobHash: file.HeadBlobHash,
		})
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// unmarshalChangedFiles decodes a stored changed_files document.
func unmarshalChangedFiles(raw []byte) ([]domain.ChangedFile, error) {
	if len(raw) == 0 {
		return []domain.ChangedFile{}, nil
	}
	var entries []changedFileJSON
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	files := make([]domain.ChangedFile, 0, len(entries))
	for _, entry := range entries {
		files = append(files, domain.ChangedFile{
			Path:         entry.Path,
			Status:       entry.Status,
			OldPath:      entry.OldPath,
			Additions:    entry.Additions,
			Deletions:    entry.Deletions,
			Binary:       entry.Binary,
			HeadBlobHash: entry.HeadBlobHash,
		})
	}
	return files, nil
}

// scanChangeSet scans one row in changeSetColumnList order and decodes the
// JSONB columns.
func scanChangeSet(row rowScanner) (domain.CodeChangeSet, error) {
	var (
		changeSet domain.CodeChangeSet
		stats     []byte
		files     []byte
	)
	if err := row.Scan(
		&changeSet.ID,
		&changeSet.RepoID,
		&changeSet.BaseCommit,
		&changeSet.HeadCommit,
		&stats,
		&files,
		&changeSet.Truncated,
		&changeSet.CreatedAt,
	); err != nil {
		return domain.CodeChangeSet{}, err
	}
	decoded, err := unmarshalChangedFiles(files)
	if err != nil {
		return domain.CodeChangeSet{}, fmt.Errorf("decode changed_files: %w", err)
	}
	changeSet.Stats = stats
	changeSet.Files = decoded
	return changeSet, nil
}

// ChangeSetRepo implements domain.ChangeSetRepository on a pgx pool.
type ChangeSetRepo struct {
	pool *pgxpool.Pool
}

// NewChangeSetRepository returns a change-set repository backed by pg.
func NewChangeSetRepository(pg *pgxpool.Pool) *ChangeSetRepo {
	return &ChangeSetRepo{pool: pg}
}

var _ domain.ChangeSetRepository = (*ChangeSetRepo)(nil)

// GetOrCreate stores cs once per (repo_id, base_commit, head_commit) and returns
// the stored row plus whether this call created it. A deterministic change set
// is never overwritten by a later identical request (design spec §6.1).
func (r *ChangeSetRepo) GetOrCreate(ctx context.Context, cs domain.CodeChangeSet) (domain.CodeChangeSet, bool, error) {
	changedFiles, err := marshalChangedFiles(cs.Files)
	if err != nil {
		return domain.CodeChangeSet{}, false, fmt.Errorf("postgres: encode change set files: %w", err)
	}
	const query = `INSERT INTO code_change_sets
		(id, repo_id, base_commit, head_commit, stats, changed_files, truncated)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6::jsonb, $7)
		ON CONFLICT (repo_id, base_commit, head_commit) DO NOTHING`
	tag, err := r.pool.Exec(ctx, query,
		cs.ID, cs.RepoID, cs.BaseCommit, cs.HeadCommit,
		jsonParam(cs.Stats, jsonFallbackEmptyObject), changedFiles, cs.Truncated,
	)
	if err != nil {
		return domain.CodeChangeSet{}, false, fmt.Errorf("postgres: create change set: %w", err)
	}
	created := tag.RowsAffected() == 1
	stored, err := scanChangeSet(r.pool.QueryRow(ctx,
		`SELECT `+changeSetColumnList+`
		 FROM code_change_sets
		 WHERE repo_id = $1 AND base_commit = $2 AND head_commit = $3`,
		cs.RepoID, cs.BaseCommit, cs.HeadCommit))
	if err != nil {
		return domain.CodeChangeSet{}, false, fmt.Errorf("postgres: read change set after upsert: %w", err)
	}
	return stored, created, nil
}

// GetByID returns one change set by id.
func (r *ChangeSetRepo) GetByID(ctx context.Context, id int64) (domain.CodeChangeSet, error) {
	stored, err := scanChangeSet(r.pool.QueryRow(ctx,
		`SELECT `+changeSetColumnList+` FROM code_change_sets WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CodeChangeSet{}, notFoundError("change_set", id)
		}
		return domain.CodeChangeSet{}, fmt.Errorf("postgres: get change set: %w", err)
	}
	return stored, nil
}
