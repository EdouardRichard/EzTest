package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// codeRepoColumnList is the column order every query of code_repositories uses
// and the scan order scanCodeRepo expects.
const codeRepoColumnList = `id, key, name, local_path, fixed_branch, include_globs, exclude_globs,
	status, last_commit, last_validated_at, last_validation_error, last_indexed_at,
	created_at, updated_at`

const (
	// defaultCodeRepoBranch mirrors the code_repositories DDL default.
	defaultCodeRepoBranch = "main"
	// defaultCodeRepoStatus mirrors the code_repositories DDL default.
	defaultCodeRepoStatus = domain.StatusActive
)

// defaultCodeRepoExcludeGlobs mirrors the code_repositories DDL default. It is
// applied on create when the caller leaves ExcludeGlobs nil, so the stored row
// matches what a plain INSERT with no column would produce.
var defaultCodeRepoExcludeGlobs = []string{
	".git/**", "node_modules/**", "dist/**", "build/**", "vendor/**",
}

// CodeRepository implements the code repository persistence port on a pgx pool.
type CodeRepository struct {
	pool *pgxpool.Pool
}

// NewCodeRepository returns a code repository backed by pool.
func NewCodeRepository(pool *pgxpool.Pool) *CodeRepository {
	return &CodeRepository{pool: pool}
}

var _ domain.CodeRepoRepository = (*CodeRepository)(nil)

// scanCodeRepo scans one row in codeRepoColumnList order. Both glob arrays are
// NOT NULL in the DDL; a nil slice is normalized to an empty one so callers
// always receive an initialized list.
func scanCodeRepo(row rowScanner) (domain.CodeRepository, error) {
	var codeRepo domain.CodeRepository
	if err := row.Scan(
		&codeRepo.ID,
		&codeRepo.Key,
		&codeRepo.Name,
		&codeRepo.LocalPath,
		&codeRepo.FixedBranch,
		&codeRepo.IncludeGlobs,
		&codeRepo.ExcludeGlobs,
		&codeRepo.Status,
		&codeRepo.LastCommit,
		&codeRepo.LastValidatedAt,
		&codeRepo.LastValidationError,
		&codeRepo.LastIndexedAt,
		&codeRepo.CreatedAt,
		&codeRepo.UpdatedAt,
	); err != nil {
		return domain.CodeRepository{}, err
	}
	if codeRepo.IncludeGlobs == nil {
		codeRepo.IncludeGlobs = []string{}
	}
	if codeRepo.ExcludeGlobs == nil {
		codeRepo.ExcludeGlobs = []string{}
	}
	return codeRepo, nil
}

// CreateCodeRepo inserts a code repository. An empty FixedBranch becomes main,
// an empty Status becomes active, nil IncludeGlobs becomes an empty array and
// nil ExcludeGlobs becomes the DDL's default exclusion list; the full stored
// row (ID and timestamps included) is written back to c.
func (r *CodeRepository) CreateCodeRepo(ctx context.Context, c *domain.CodeRepository) error {
	fixedBranch := c.FixedBranch
	if fixedBranch == "" {
		fixedBranch = defaultCodeRepoBranch
	}
	status := c.Status
	if status == "" {
		status = defaultCodeRepoStatus
	}
	includeGlobs := c.IncludeGlobs
	if includeGlobs == nil {
		includeGlobs = []string{}
	}
	excludeGlobs := c.ExcludeGlobs
	if excludeGlobs == nil {
		excludeGlobs = defaultCodeRepoExcludeGlobs
	}
	const query = `INSERT INTO code_repositories
		(key, name, local_path, fixed_branch, include_globs, exclude_globs, status,
		 last_commit, last_validated_at, last_validation_error, last_indexed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING ` + codeRepoColumnList
	created, err := scanCodeRepo(r.pool.QueryRow(ctx, query,
		c.Key, c.Name, c.LocalPath, fixedBranch, includeGlobs, excludeGlobs, status,
		c.LastCommit, c.LastValidatedAt, c.LastValidationError, c.LastIndexedAt))
	if err != nil {
		return catalogWriteError(err, "a code repository with this key already exists", "create code repository")
	}
	*c = created
	return nil
}

// GetCodeRepo returns one code repository by ID.
func (r *CodeRepository) GetCodeRepo(ctx context.Context, id int64) (*domain.CodeRepository, error) {
	codeRepo, err := scanCodeRepo(r.pool.QueryRow(ctx,
		`SELECT `+codeRepoColumnList+` FROM code_repositories WHERE id = $1`, id))
	if err != nil {
		return nil, catalogReadError(err, "code repository", id, "get code repository")
	}
	return &codeRepo, nil
}

// ListCodeRepos returns one page of code repositories ordered by ID ascending.
// An empty Status filter matches every status; more reports whether a row
// exists after the returned page.
func (r *CodeRepository) ListCodeRepos(ctx context.Context, f domain.CodeRepoFilter) ([]domain.CodeRepository, bool, error) {
	if f.Page.Limit <= 0 {
		return nil, false, fmt.Errorf("postgres: list code repositories: limit must be positive")
	}
	const query = `SELECT ` + codeRepoColumnList + `
		FROM code_repositories
		WHERE id > $1
		  AND ($2 = '' OR status = $2)
		ORDER BY id ASC
		LIMIT $3`
	rows, err := r.pool.Query(ctx, query, f.Page.AfterID, f.Status, f.Page.Limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: list code repositories: %w", err)
	}
	defer rows.Close()

	codeRepos := make([]domain.CodeRepository, 0, f.Page.Limit+1)
	for rows.Next() {
		codeRepo, err := scanCodeRepo(rows)
		if err != nil {
			return nil, false, fmt.Errorf("postgres: scan code repository: %w", err)
		}
		codeRepos = append(codeRepos, codeRepo)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("postgres: list code repositories: %w", err)
	}

	more := len(codeRepos) > f.Page.Limit
	if more {
		codeRepos = codeRepos[:f.Page.Limit]
	}
	return codeRepos, more, nil
}

// UpdateCodeRepo writes the mutable code repository columns, including the
// last_commit audit fields the validate action refreshes, and refreshes the
// server timestamps on c. The key is immutable after creation. Nil glob arrays
// are stored as empty arrays because both columns are NOT NULL.
func (r *CodeRepository) UpdateCodeRepo(ctx context.Context, c *domain.CodeRepository) error {
	includeGlobs := c.IncludeGlobs
	if includeGlobs == nil {
		includeGlobs = []string{}
	}
	excludeGlobs := c.ExcludeGlobs
	if excludeGlobs == nil {
		excludeGlobs = []string{}
	}
	const query = `UPDATE code_repositories
		SET name = $2, local_path = $3, fixed_branch = $4, include_globs = $5,
		    exclude_globs = $6, status = $7, last_commit = $8,
		    last_validated_at = $9, last_validation_error = $10, last_indexed_at = $11,
		    updated_at = now()
		WHERE id = $1
		RETURNING created_at, updated_at`
	if err := r.pool.QueryRow(ctx, query,
		c.ID, c.Name, c.LocalPath, c.FixedBranch, includeGlobs, excludeGlobs, c.Status,
		c.LastCommit, c.LastValidatedAt, c.LastValidationError, c.LastIndexedAt,
	).Scan(&c.CreatedAt, &c.UpdatedAt); err != nil {
		return catalogWriteBackError(err, "code repository", c.ID,
			"a code repository with this key already exists", "update code repository")
	}
	return nil
}

// DeleteCodeRepo removes a code repository. P2 registers no dependents, so the
// delete needs no dependency check (spec §6.7).
func (r *CodeRepository) DeleteCodeRepo(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM code_repositories WHERE id = $1`, id)
	if err != nil {
		return catalogWriteError(err, "duplicate key", "delete code repository")
	}
	if tag.RowsAffected() == 0 {
		return domain.NewNotFound("code repository", id)
	}
	return nil
}
