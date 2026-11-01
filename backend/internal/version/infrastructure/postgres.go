// Package versionpg is the PostgreSQL adapter for versions, SUT bindings and
// the bound database/account rows.
package versionpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const versionColumns = `id, title, status, auto_design_new_requirements, closed_at,
	close_reason, close_run_id, created_by, created_at, updated_at`

// Repository stores version, version_sut and version_sut_resource rows.
type Repository struct {
	pool *pgxpool.Pool
	ids  platformid.Generator
}

// New wraps a pgx pool. The generator supplies primary keys for the internal
// version_sut_resource join rows; business entity IDs are assigned by the
// application service.
func New(pool *pgxpool.Pool, ids platformid.Generator) *Repository {
	return &Repository{pool: pool, ids: ids}
}

type rowScanner interface {
	Scan(dest ...any) error
}

func wrapDB(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return version.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return version.ErrDuplicate
		case "23503": // foreign_key_violation
			return version.ErrNotFound
		}
	}
	return fmt.Errorf("%s: %w", op, err)
}

// Create inserts the version, every binding and its database/account rows in
// one transaction. A uniqueness failure rolls the whole aggregate back.
func (r *Repository) Create(ctx context.Context, v version.Version, bindings []version.Binding) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("create version: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO version (`+versionColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, v.ID, v.Title, v.Status, v.AutoDesignNewRequirements, v.ClosedAt,
		v.CloseReason, v.CloseRunID, v.CreatedBy, v.CreatedAt, v.UpdatedAt); err != nil {
		return wrapDB("insert version", err)
	}

	for _, binding := range bindings {
		scope, err := encodeScope(binding.Scope)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO version_sut (id, version_id, sut_id, environment_id, code_repo_id, scope)
			VALUES ($1, $2, $3, $4, $5, $6::jsonb)
		`, binding.ID, v.ID, binding.SUTID, binding.EnvironmentID,
			binding.CodeRepositoryID, scope); err != nil {
			return wrapDB("insert version_sut", err)
		}
		for _, databaseID := range binding.DatabaseIDs {
			if _, err := tx.Exec(ctx, `
				INSERT INTO version_sut_resource (id, version_sut_id, kind, resource_id, role)
				VALUES ($1, $2, 'database', $3, '')
			`, r.ids.Next(), binding.ID, databaseID); err != nil {
				return wrapDB("insert version_sut_resource database", err)
			}
		}
		for _, account := range binding.Accounts {
			if _, err := tx.Exec(ctx, `
				INSERT INTO version_sut_resource (id, version_sut_id, kind, resource_id, role)
				VALUES ($1, $2, 'account', $3, $4)
			`, r.ids.Next(), binding.ID, account.AccountID, account.Role); err != nil {
				return wrapDB("insert version_sut_resource account", err)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("create version: commit: %w", err)
	}
	return nil
}

// Get loads one version and its bindings with the selected resources.
func (r *Repository) Get(ctx context.Context, id int64) (version.Version, []version.Binding, error) {
	v, err := scanVersion(r.pool.QueryRow(ctx, `SELECT `+versionColumns+` FROM version WHERE id = $1`, id))
	if err != nil {
		return version.Version{}, nil, wrapDB("get version", err)
	}

	rows, err := r.pool.Query(ctx, `
		SELECT id, version_id, sut_id, environment_id, code_repo_id, scope
		FROM version_sut WHERE version_id = $1 ORDER BY id
	`, id)
	if err != nil {
		return version.Version{}, nil, fmt.Errorf("get version bindings: %w", err)
	}
	defer rows.Close()

	bindings := make([]version.Binding, 0)
	index := make(map[int64]int, 0)
	for rows.Next() {
		var binding version.Binding
		var scopeRaw []byte
		if err := rows.Scan(&binding.ID, &binding.VersionID, &binding.SUTID, &binding.EnvironmentID,
			&binding.CodeRepositoryID, &scopeRaw); err != nil {
			return version.Version{}, nil, fmt.Errorf("scan version binding: %w", err)
		}
		if err := json.Unmarshal(scopeRaw, &binding.Scope); err != nil {
			return version.Version{}, nil, fmt.Errorf("decode version scope: %w", err)
		}
		binding.Scope = version.NewScope(binding.Scope.Pages, binding.Scope.APIs)
		binding.DatabaseIDs = []int64{}
		binding.Accounts = []version.AccountBinding{}
		index[binding.ID] = len(bindings)
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		return version.Version{}, nil, fmt.Errorf("get version bindings: %w", err)
	}
	if len(bindings) == 0 {
		return v, bindings, nil
	}

	bindingIDs := make([]int64, 0, len(bindings))
	for _, binding := range bindings {
		bindingIDs = append(bindingIDs, binding.ID)
	}
	resourceRows, err := r.pool.Query(ctx, `
		SELECT version_sut_id, kind, resource_id, role
		FROM version_sut_resource
		WHERE version_sut_id = ANY($1::BIGINT[])
		ORDER BY id
	`, bindingIDs)
	if err != nil {
		return version.Version{}, nil, fmt.Errorf("get version binding resources: %w", err)
	}
	defer resourceRows.Close()
	for resourceRows.Next() {
		var bindingID, resourceID int64
		var kind, role string
		if err := resourceRows.Scan(&bindingID, &kind, &resourceID, &role); err != nil {
			return version.Version{}, nil, fmt.Errorf("scan version binding resource: %w", err)
		}
		position, ok := index[bindingID]
		if !ok {
			continue
		}
		switch kind {
		case "database":
			bindings[position].DatabaseIDs = append(bindings[position].DatabaseIDs, resourceID)
		case "account":
			bindings[position].Accounts = append(bindings[position].Accounts, version.AccountBinding{
				AccountID: resourceID,
				Role:      role,
			})
		}
	}
	if err := resourceRows.Err(); err != nil {
		return version.Version{}, nil, fmt.Errorf("get version binding resources: %w", err)
	}
	return v, bindings, nil
}

// List returns version summaries ordered by ID descending, counting bindings.
func (r *Repository) List(ctx context.Context, limit int, beforeID *int64) ([]version.Summary, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT v.id, v.title, v.status, v.created_at, v.updated_at, count(b.id)
		FROM version v
		LEFT JOIN version_sut b ON b.version_id = v.id
		WHERE ($1::BIGINT IS NULL OR v.id < $1)
		GROUP BY v.id, v.title, v.status, v.created_at, v.updated_at
		ORDER BY v.id DESC
		LIMIT $2
	`, beforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("list versions: %w", err)
	}
	defer rows.Close()

	summaries := make([]version.Summary, 0)
	for rows.Next() {
		var summary version.Summary
		if err := rows.Scan(&summary.ID, &summary.Title, &summary.Status,
			&summary.CreatedAt, &summary.UpdatedAt, &summary.SUTCount); err != nil {
			return nil, fmt.Errorf("scan version summary: %w", err)
		}
		summaries = append(summaries, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list versions: %w", err)
	}
	return summaries, nil
}

// Update rewrites the editable version fields. Status and ownership columns
// are never touched by the P0 API.
func (r *Repository) Update(ctx context.Context, v version.Version) (version.Version, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE version SET title = $2, auto_design_new_requirements = $3, updated_at = $4
		WHERE id = $1
	`, v.ID, v.Title, v.AutoDesignNewRequirements, v.UpdatedAt)
	if err != nil {
		return version.Version{}, wrapDB("update version", err)
	}
	if tag.RowsAffected() == 0 {
		return version.Version{}, version.ErrNotFound
	}
	return v, nil
}

// MarkRunning flips initial -> running. The WHERE guard makes repeated calls
// no-ops and reports whether this call changed the row.
func (r *Repository) MarkRunning(ctx context.Context, id int64, updatedAt time.Time) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE version SET status = 'running', updated_at = $2
		WHERE id = $1 AND status = 'initial'
	`, id, updatedAt)
	if err != nil {
		return false, wrapDB("mark version running", err)
	}
	return tag.RowsAffected() > 0, nil
}

// Reopen flips completed -> running and clears the close fields (P3 §5.9).
// The WHERE guard makes repeated calls no-ops.
func (r *Repository) Reopen(ctx context.Context, id int64, updatedAt time.Time) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE version SET status = 'running', closed_at = NULL, close_reason = '',
			close_run_id = NULL, updated_at = $2
		WHERE id = $1 AND status = 'completed'
	`, id, updatedAt)
	if err != nil {
		return false, wrapDB("reopen version", err)
	}
	return tag.RowsAffected() > 0, nil
}

// Close completes one running version (P4 spec §7.3/§7.4). It first takes the
// version-scoped advisory lock shared with execution intent creation, then locks
// the version row, re-runs the service predicate through recheck while the lock
// is held and only then applies the conditional running -> completed UPDATE. A
// recheck error rolls the transaction back untouched; a version that is no
// longer running is a state conflict.
func (r *Repository) Close(ctx context.Context, id int64, close version.VersionCloseFields, recheck func(context.Context, version.Version) error) (version.Version, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return version.Version{}, fmt.Errorf("close version: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockVersionTx(ctx, tx, id, "close version"); err != nil {
		return version.Version{}, err
	}
	current, err := scanVersion(tx.QueryRow(ctx, `SELECT `+versionColumns+` FROM version WHERE id = $1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return version.Version{}, version.ErrNotFound
	}
	if err != nil {
		return version.Version{}, wrapDB("close version: lock version", err)
	}
	if current.Status != version.StatusRunning {
		return version.Version{}, version.ErrStateConflict
	}
	if recheck != nil {
		if err := recheck(ctx, current); err != nil {
			return version.Version{}, err
		}
	}

	closedAt := close.ClosedAt
	if closedAt.IsZero() {
		closedAt = time.Now().UTC()
	}
	current.Status = version.StatusCompleted
	current.ClosedAt = &closedAt
	current.CloseReason = close.CloseReason
	current.CloseRunID = cloneCloseRunID(close.CloseRunID)
	current.UpdatedAt = closedAt
	tag, err := tx.Exec(ctx, `
		UPDATE version SET status = 'completed', closed_at = $2, close_reason = $3,
			close_run_id = $4, updated_at = $5
		WHERE id = $1 AND status = 'running'
	`, id, closedAt, close.CloseReason, close.CloseRunID, closedAt)
	if err != nil {
		return version.Version{}, wrapDB("close version", err)
	}
	if tag.RowsAffected() == 0 {
		return version.Version{}, version.ErrStateConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return version.Version{}, fmt.Errorf("close version: commit: %w", err)
	}
	return current, nil
}

// ReopenIfCompleted flips completed -> running, clears the close projection and
// returns the updated version. A version in any other status is returned
// unchanged with changed=false; a missing version yields ErrNotFound. The
// single-statement conditional UPDATE is the guard, so concurrent reopens and
// close/reopen races settle on one row version.
func (r *Repository) ReopenIfCompleted(ctx context.Context, id int64, reopenedAt time.Time) (version.Version, bool, error) {
	reopened, err := scanVersion(r.pool.QueryRow(ctx, `
		UPDATE version SET status = 'running', closed_at = NULL, close_reason = '',
			close_run_id = NULL, updated_at = $2
		WHERE id = $1 AND status = 'completed'
		RETURNING `+versionColumns, id, reopenedAt))
	if err == nil {
		return reopened, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return version.Version{}, false, wrapDB("reopen version", err)
	}
	// No row was updated: distinguish "not completed" (no-op) from "missing".
	current, err := scanVersion(r.pool.QueryRow(ctx, `
		SELECT `+versionColumns+` FROM version WHERE id = $1
	`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return version.Version{}, false, version.ErrNotFound
	}
	if err != nil {
		return version.Version{}, false, wrapDB("reopen version", err)
	}
	return current, false, nil
}

func cloneCloseRunID(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

// Stats returns the P1 detail aggregates: requirement counts, conversation
// count and (implicitly) the pending-design placeholder.
func (r *Repository) Stats(ctx context.Context, id int64) (version.VersionStats, error) {
	var stats version.VersionStats
	rows, err := r.pool.Query(ctx, `
		SELECT status, count(*) FROM requirement WHERE version_id = $1 GROUP BY status
	`, id)
	if err != nil {
		return version.VersionStats{}, fmt.Errorf("version requirement stats: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return version.VersionStats{}, fmt.Errorf("scan version requirement stats: %w", err)
		}
		switch version.RequirementStatus(status) {
		case version.RequirementDraft:
			stats.Requirements.Draft = count
		case version.RequirementClarified:
			stats.Requirements.Clarified = count
		case version.RequirementClosed:
			stats.Requirements.Closed = count
		case version.RequirementObsolete:
			stats.Requirements.Obsolete = count
		}
		stats.Requirements.Total += count
	}
	if err := rows.Err(); err != nil {
		return version.VersionStats{}, fmt.Errorf("version requirement stats: %w", err)
	}
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM conversation WHERE version_id = $1`, id).
		Scan(&stats.ConversationCount); err != nil {
		return version.VersionStats{}, fmt.Errorf("version conversation count: %w", err)
	}
	return stats, nil
}

// ListInitialVersionsWithContent returns initial versions that already have a
// conversation message or requirement (P1 reconciler projection repair).
func (r *Repository) ListInitialVersionsWithContent(ctx context.Context, limit int) ([]int64, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT v.id FROM version v
		WHERE v.status = 'initial'
		  AND (
			EXISTS (
				SELECT 1 FROM conversation c
				JOIN conversation_message m ON m.conversation_id = c.id
				WHERE c.version_id = v.id
			)
			OR EXISTS (SELECT 1 FROM requirement r WHERE r.version_id = v.id)
		  )
		ORDER BY v.id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list initial versions with content: %w", err)
	}
	defer rows.Close()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan initial version: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list initial versions with content: %w", err)
	}
	return ids, nil
}

// SUTInUse reports whether any version binding references the SUT.
func (r *Repository) SUTInUse(ctx context.Context, id int64) (bool, error) {
	return r.exists(ctx, `SELECT EXISTS (SELECT 1 FROM version_sut WHERE sut_id = $1)`, id)
}

// EnvironmentInUse reports whether any version binding references the
// environment.
func (r *Repository) EnvironmentInUse(ctx context.Context, id int64) (bool, error) {
	return r.exists(ctx, `SELECT EXISTS (SELECT 1 FROM version_sut WHERE environment_id = $1)`, id)
}

// PageInUse reports whether any binding scope contains the page ID.
func (r *Repository) PageInUse(ctx context.Context, id int64) (bool, error) {
	return r.exists(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM version_sut WHERE scope -> 'pages' @> to_jsonb($1::BIGINT)
		)`, id)
}

// APIInUse reports whether any binding scope contains the API ID.
func (r *Repository) APIInUse(ctx context.Context, id int64) (bool, error) {
	return r.exists(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM version_sut WHERE scope -> 'apis' @> to_jsonb($1::BIGINT)
		)`, id)
}

// DatabaseInUse reports whether any binding references the target database.
func (r *Repository) DatabaseInUse(ctx context.Context, id int64) (bool, error) {
	return r.exists(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM version_sut_resource WHERE kind = 'database' AND resource_id = $1
		)`, id)
}

// AccountInUse reports whether any binding references the test account.
func (r *Repository) AccountInUse(ctx context.Context, id int64) (bool, error) {
	return r.exists(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM version_sut_resource WHERE kind = 'account' AND resource_id = $1
		)`, id)
}

// RepositoryInUse reports whether any binding references the code repository.
func (r *Repository) RepositoryInUse(ctx context.Context, id int64) (bool, error) {
	return r.exists(ctx, `SELECT EXISTS (SELECT 1 FROM version_sut WHERE code_repo_id = $1)`, id)
}

func (r *Repository) exists(ctx context.Context, query string, id int64) (bool, error) {
	var used bool
	if err := r.pool.QueryRow(ctx, query, id).Scan(&used); err != nil {
		return false, fmt.Errorf("version usage query: %w", err)
	}
	return used, nil
}

func scanVersion(row rowScanner) (version.Version, error) {
	var v version.Version
	if err := row.Scan(&v.ID, &v.Title, &v.Status, &v.AutoDesignNewRequirements,
		&v.ClosedAt, &v.CloseReason, &v.CloseRunID, &v.CreatedBy, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return version.Version{}, err
	}
	return v, nil
}

func encodeScope(scope version.Scope) (string, error) {
	normalized := version.NewScope(scope.Pages, scope.APIs)
	raw, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("encode version scope: %w", err)
	}
	return string(raw), nil
}

var (
	_ version.Repository             = (*Repository)(nil)
	_ version.VersionCloseRepository = (*Repository)(nil)
)
