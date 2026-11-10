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

const versionColumns = `id, title, description, goal, status, auto_design_new_requirements,
	closed_at, close_reason, close_run_id, created_by, created_at, updated_at`

// Repository stores versions, version_sut bindings, the version-level
// repository overrides and the version account subset.
type Repository struct {
	pool *pgxpool.Pool
	ids  platformid.Generator
}

// New wraps a pgx pool. The generator supplies primary keys for the binding
// override/account rows; business entity IDs are assigned by the application
// service.
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

// Create inserts the version, its environment-only binding, the initial
// repository overrides and the version account subset in one transaction. A
// uniqueness failure rolls the whole aggregate back.
func (r *Repository) Create(ctx context.Context, v version.Version, bindings []version.Binding) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("create version: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO version (`+versionColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`, v.ID, v.Title, v.Description, v.Goal, v.Status, v.AutoDesignNewRequirements,
		v.ClosedAt, v.CloseReason, v.CloseRunID, v.CreatedBy, v.CreatedAt, v.UpdatedAt); err != nil {
		return wrapDB("insert version", err)
	}

	for _, binding := range bindings {
		scopeExclude, err := encodeScopeExclude(binding.ScopeExclude)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO version_sut (id, version_id, sut_id, environment_id, code_baseline_commit, scope_exclude)
			VALUES ($1, $2, $3, $4, $5, $6::jsonb)
		`, binding.ID, v.ID, binding.SUTID, binding.EnvironmentID,
			nullableCommit(binding.CodeBaselineCommit), scopeExclude); err != nil {
			return wrapDB("insert version_sut", err)
		}
		if err := insertBindingRepositories(ctx, tx, r.ids, v.ID, binding.RepositoryOverrides); err != nil {
			return err
		}
		if err := insertBindingAccounts(ctx, tx, r.ids, v.ID, binding.Accounts); err != nil {
			return err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("create version: commit: %w", err)
	}
	return nil
}

// UpdateBinding rewrites the v0.22 binding projection of one existing version
// in one transaction: version_sut (environment/scope_exclude/code baseline),
// the version_repository_override rows and the version_account rows. The
// version row is locked first and the write only proceeds while its
// updated_at still equals expectedUpdatedAt, so concurrent binding or stage
// writers settle deterministically (ErrBindingConflict) instead of silently
// losing an update.
func (r *Repository) UpdateBinding(ctx context.Context, v version.Version, binding version.Binding, expectedUpdatedAt time.Time) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("update version binding: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var storedUpdatedAt time.Time
	err = tx.QueryRow(ctx, `SELECT updated_at FROM version WHERE id = $1 FOR UPDATE`, v.ID).Scan(&storedUpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return version.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("update version binding: lock version: %w", err)
	}
	if !storedUpdatedAt.Equal(expectedUpdatedAt) {
		return version.ErrBindingConflict
	}

	scopeExclude, err := encodeScopeExclude(binding.ScopeExclude)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE version_sut
		SET environment_id = $3, scope_exclude = $4::jsonb,
			code_baseline_commit = $5, updated_at = $6
		WHERE id = $1 AND version_id = $2
	`, binding.ID, v.ID, binding.EnvironmentID, scopeExclude,
		nullableCommit(binding.CodeBaselineCommit), v.UpdatedAt)
	if err != nil {
		return wrapDB("update version_sut", err)
	}
	if tag.RowsAffected() == 0 {
		return version.ErrNotFound
	}
	if _, err := tx.Exec(ctx, `DELETE FROM version_repository_override WHERE version_id = $1`, v.ID); err != nil {
		return wrapDB("delete version repository overrides", err)
	}
	if err := insertBindingRepositories(ctx, tx, r.ids, v.ID, binding.RepositoryOverrides); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM version_account WHERE version_id = $1`, v.ID); err != nil {
		return wrapDB("delete version accounts", err)
	}
	if err := insertBindingAccounts(ctx, tx, r.ids, v.ID, binding.Accounts); err != nil {
		return err
	}
	tag, err = tx.Exec(ctx, `UPDATE version SET updated_at = $2 WHERE id = $1`, v.ID, v.UpdatedAt)
	if err != nil {
		return wrapDB("update version timestamp", err)
	}
	if tag.RowsAffected() == 0 {
		return version.ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("update version binding: commit: %w", err)
	}
	return nil
}

func insertBindingRepositories(
	ctx context.Context, tx pgx.Tx, ids platformid.Generator, versionID int64,
	overrides []version.RepositoryOverride,
) error {
	for _, override := range overrides {
		if _, err := tx.Exec(ctx, `
			INSERT INTO version_repository_override (id, version_id, repo_id, included)
			VALUES ($1, $2, $3, $4)
		`, ids.Next(), versionID, override.RepoID, override.Included); err != nil {
			return wrapDB("insert version repository override", err)
		}
	}
	return nil
}

func insertBindingAccounts(
	ctx context.Context, tx pgx.Tx, ids platformid.Generator, versionID int64,
	accounts []version.AccountBinding,
) error {
	for _, account := range accounts {
		if _, err := tx.Exec(ctx, `
			INSERT INTO version_account (id, version_id, account_id, enabled)
			VALUES ($1, $2, $3, true)
		`, ids.Next(), versionID, account.AccountID); err != nil {
			return wrapDB("insert version account", err)
		}
	}
	return nil
}

// Get loads one version and its environment-only bindings with the version
// repository overrides and account subset.
func (r *Repository) Get(ctx context.Context, id int64) (version.Version, []version.Binding, error) {
	v, err := scanVersion(r.pool.QueryRow(ctx, `SELECT `+versionColumns+` FROM version WHERE id = $1`, id))
	if err != nil {
		return version.Version{}, nil, wrapDB("get version", err)
	}

	rows, err := r.pool.Query(ctx, `
		SELECT id, version_id, sut_id, environment_id,
			COALESCE(code_baseline_commit, ''), scope_exclude
		FROM version_sut WHERE version_id = $1 ORDER BY id
	`, id)
	if err != nil {
		return version.Version{}, nil, fmt.Errorf("get version bindings: %w", err)
	}
	defer rows.Close()

	bindings := make([]version.Binding, 0)
	for rows.Next() {
		var binding version.Binding
		var scopeRaw []byte
		if err := rows.Scan(&binding.ID, &binding.VersionID, &binding.SUTID, &binding.EnvironmentID,
			&binding.CodeBaselineCommit, &scopeRaw); err != nil {
			return version.Version{}, nil, fmt.Errorf("scan version binding: %w", err)
		}
		if len(scopeRaw) > 0 {
			if err := json.Unmarshal(scopeRaw, &binding.ScopeExclude); err != nil {
				return version.Version{}, nil, fmt.Errorf("decode version scope_exclude: %w", err)
			}
		}
		binding.ScopeExclude = version.NewScopeExclude(
			binding.ScopeExclude.Pages, binding.ScopeExclude.APIs, binding.ScopeExclude.Databases,
		)
		binding.RepositoryOverrides = []version.RepositoryOverride{}
		binding.Accounts = []version.AccountBinding{}
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		return version.Version{}, nil, fmt.Errorf("get version bindings: %w", err)
	}
	if len(bindings) == 0 {
		return v, bindings, nil
	}

	overrideRows, err := r.pool.Query(ctx, `
		SELECT repo_id, included
		FROM version_repository_override
		WHERE version_id = $1
		ORDER BY repo_id
	`, id)
	if err != nil {
		return version.Version{}, nil, fmt.Errorf("get version repository overrides: %w", err)
	}
	defer overrideRows.Close()
	for overrideRows.Next() {
		var repoID int64
		var included bool
		if err := overrideRows.Scan(&repoID, &included); err != nil {
			return version.Version{}, nil, fmt.Errorf("scan version repository override: %w", err)
		}
		// The environment-only model has one binding per version.
		bindings[0].RepositoryOverrides = append(bindings[0].RepositoryOverrides,
			version.RepositoryOverride{RepoID: repoID, Included: included})
	}
	if err := overrideRows.Err(); err != nil {
		return version.Version{}, nil, fmt.Errorf("get version repository overrides: %w", err)
	}

	accountRows, err := r.pool.Query(ctx, `
		SELECT account_id
		FROM version_account
		WHERE version_id = $1 AND enabled
		ORDER BY account_id
	`, id)
	if err != nil {
		return version.Version{}, nil, fmt.Errorf("get version accounts: %w", err)
	}
	defer accountRows.Close()
	for accountRows.Next() {
		var accountID int64
		if err := accountRows.Scan(&accountID); err != nil {
			return version.Version{}, nil, fmt.Errorf("scan version account: %w", err)
		}
		bindings[0].Accounts = append(bindings[0].Accounts, version.AccountBinding{AccountID: accountID})
	}
	if err := accountRows.Err(); err != nil {
		return version.Version{}, nil, fmt.Errorf("get version accounts: %w", err)
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
		UPDATE version SET title = $2, description = $3, goal = $4,
			auto_design_new_requirements = $5, updated_at = $6
		WHERE id = $1
	`, v.ID, v.Title, v.Description, v.Goal, v.AutoDesignNewRequirements, v.UpdatedAt)
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

// SetCodeBaselineCommit writes the resolved commit SHA of one version_sut row
// (P5 §5.5). An empty commit clears the column to NULL so the binding is
// treated as unresolved again.
func (r *Repository) SetCodeBaselineCommit(ctx context.Context, versionSUTID int64, commitSHA string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE version_sut SET code_baseline_commit = NULLIF($2, ''), updated_at = now()
		WHERE id = $1
	`, versionSUTID, commitSHA)
	if err != nil {
		return wrapDB("set version code baseline commit", err)
	}
	if tag.RowsAffected() == 0 {
		return version.ErrNotFound
	}
	return nil
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

// PageInUse reports whether the page is inside the effective scope of a version
// binding: it belongs to a bound environment and is not excluded. A resource
// that is explicitly excluded is not part of the version range, so deleting it
// is not blocked by this guard.
func (r *Repository) PageInUse(ctx context.Context, id int64) (bool, error) {
	return r.exists(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM version_sut vs
			JOIN sut_page p ON p.sut_id = vs.sut_id AND p.environment_id = vs.environment_id
			WHERE p.id = $1 AND p.status = 'approved'
			  AND NOT (vs.scope_exclude -> 'pages' @> to_jsonb(p.id))
		)`, id)
}

// APIInUse reports whether the API is inside the effective scope of a version
// binding: it belongs to a bound environment and is not excluded.
func (r *Repository) APIInUse(ctx context.Context, id int64) (bool, error) {
	return r.exists(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM version_sut vs
			JOIN sut_api a ON a.sut_id = vs.sut_id AND a.environment_id = vs.environment_id
			WHERE a.id = $1 AND a.status = 'approved'
			  AND NOT (vs.scope_exclude -> 'apis' @> to_jsonb(a.id))
		)`, id)
}

// DatabaseInUse reports whether the target database is inside the effective
// scope of a version binding: it belongs to a bound environment, is active and
// is not excluded.
func (r *Repository) DatabaseInUse(ctx context.Context, id int64) (bool, error) {
	return r.exists(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM version_sut vs
			JOIN target_database d ON d.sut_id = vs.sut_id AND d.environment_id = vs.environment_id
			WHERE d.id = $1 AND d.status = 'active'
			  AND NOT (vs.scope_exclude -> 'databases' @> to_jsonb(d.id))
		)`, id)
}

// AccountInUse reports whether any version enables the test account.
func (r *Repository) AccountInUse(ctx context.Context, id int64) (bool, error) {
	return r.exists(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM version_account WHERE account_id = $1
		)`, id)
}

// RepositoryInUse reports whether any SUT association or version override
// references the code repository.
func (r *Repository) RepositoryInUse(ctx context.Context, id int64) (bool, error) {
	return r.exists(ctx, `
		SELECT EXISTS (SELECT 1 FROM sut_repository WHERE repo_id = $1)
			OR EXISTS (SELECT 1 FROM version_repository_override WHERE repo_id = $1)`, id)
}

// ReferencingVersions lists the versions whose binding references a resource of
// one kind, joined with the version lifecycle status/stage so the resource
// delete conflict can report whether a binding edit is still possible. Its
// predicates mirror the usage guards, so a positive in-use check is explained by
// the versions it returns.
func (r *Repository) ReferencingVersions(ctx context.Context, kind string, id int64) ([]version.ReferencingVersion, error) {
	var query string
	switch kind {
	case "sut":
		query = `
			SELECT DISTINCT v.id, v.title, v.status, v.stage, v.suspended_from
			FROM version v
			JOIN version_sut vs ON vs.version_id = v.id
			WHERE vs.sut_id = $1
			ORDER BY v.id`
	case "environment":
		query = `
			SELECT DISTINCT v.id, v.title, v.status, v.stage, v.suspended_from
			FROM version v
			JOIN version_sut vs ON vs.version_id = v.id
			WHERE vs.environment_id = $1
			ORDER BY v.id`
	case "page":
		query = `
			SELECT DISTINCT v.id, v.title, v.status, v.stage, v.suspended_from
			FROM version v
			JOIN version_sut vs ON vs.version_id = v.id
			JOIN sut_page p ON p.sut_id = vs.sut_id AND p.environment_id = vs.environment_id
			WHERE p.id = $1 AND p.status = 'approved'
			  AND NOT (vs.scope_exclude -> 'pages' @> to_jsonb(p.id))
			ORDER BY v.id`
	case "api":
		query = `
			SELECT DISTINCT v.id, v.title, v.status, v.stage, v.suspended_from
			FROM version v
			JOIN version_sut vs ON vs.version_id = v.id
			JOIN sut_api a ON a.sut_id = vs.sut_id AND a.environment_id = vs.environment_id
			WHERE a.id = $1 AND a.status = 'approved'
			  AND NOT (vs.scope_exclude -> 'apis' @> to_jsonb(a.id))
			ORDER BY v.id`
	case "database":
		query = `
			SELECT DISTINCT v.id, v.title, v.status, v.stage, v.suspended_from
			FROM version v
			JOIN version_sut vs ON vs.version_id = v.id
			JOIN target_database d ON d.sut_id = vs.sut_id AND d.environment_id = vs.environment_id
			WHERE d.id = $1 AND d.status = 'active'
			  AND NOT (vs.scope_exclude -> 'databases' @> to_jsonb(d.id))
			ORDER BY v.id`
	case "account":
		query = `
			SELECT DISTINCT v.id, v.title, v.status, v.stage, v.suspended_from
			FROM version v
			JOIN version_account va ON va.version_id = v.id
			WHERE va.account_id = $1
			ORDER BY v.id`
	case "repository":
		query = `
			SELECT DISTINCT v.id, v.title, v.status, v.stage, v.suspended_from
			FROM version v
			LEFT JOIN version_sut vs ON vs.version_id = v.id
			WHERE vs.sut_id IN (SELECT sut_id FROM sut_repository WHERE repo_id = $1)
			   OR EXISTS (
				SELECT 1 FROM version_repository_override vro
				WHERE vro.version_id = v.id AND vro.repo_id = $1
			   )
			ORDER BY v.id`
	default:
		return nil, fmt.Errorf("referencing versions: unknown resource kind %q", kind)
	}
	rows, err := r.pool.Query(ctx, query, id)
	if err != nil {
		return nil, fmt.Errorf("referencing versions: %w", err)
	}
	defer rows.Close()
	refs := make([]version.ReferencingVersion, 0)
	for rows.Next() {
		var ref version.ReferencingVersion
		var stage string
		var suspendedFrom *string
		if err := rows.Scan(&ref.ID, &ref.Title, &ref.Status, &stage, &suspendedFrom); err != nil {
			return nil, fmt.Errorf("scan referencing version: %w", err)
		}
		ref.Stage = version.Stage(stage)
		if suspendedFrom != nil {
			previous := version.Stage(*suspendedFrom)
			ref.SuspendedFrom = &previous
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("referencing versions: %w", err)
	}
	return refs, nil
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
	if err := row.Scan(&v.ID, &v.Title, &v.Description, &v.Goal, &v.Status,
		&v.AutoDesignNewRequirements, &v.ClosedAt, &v.CloseReason, &v.CloseRunID,
		&v.CreatedBy, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return version.Version{}, err
	}
	return v, nil
}

func encodeScopeExclude(scope version.ScopeExclude) (string, error) {
	normalized := version.NewScopeExclude(scope.Pages, scope.APIs, scope.Databases)
	raw, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("encode version scope_exclude: %w", err)
	}
	return string(raw), nil
}

// nullableCommit stores an unresolved baseline as SQL NULL so the column CHECK
// only ever sees a full commit SHA.
func nullableCommit(commit string) any {
	if commit == "" {
		return nil
	}
	return commit
}

var (
	_ version.Repository             = (*Repository)(nil)
	_ version.VersionCloseRepository = (*Repository)(nil)
)
