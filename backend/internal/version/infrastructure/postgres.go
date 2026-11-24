// Package versionpg is the PostgreSQL adapter for versions, their direct
// environment binding and the version account subset.
package versionpg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	resource "github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// versionColumns is the version row projection, shared by the INSERT and every
// read. environment_id is the direct binding of v0.2.4-B: NOT NULL, but still a
// logical cross-context BIGINT with no physical foreign key (Agent.md §6.9.7, cf.
// version_0010 which keeps sut_id/repo_id/account_id logical for the same
// reason). The resource delete guard EnvironmentInUse is the protection against
// deleting a bound environment.
const versionColumns = `id, title, description, goal, status, auto_design_new_requirements,
	environment_id, closed_at, close_reason, close_run_id, created_by, created_at,
	updated_at, closure_kind, unverified`

// Repository stores versions and the account subset each version executes with.
type Repository struct {
	pool *pgxpool.Pool
	ids  platformid.Generator
}

// New wraps a pgx pool. The generator supplies primary keys for the version
// account rows; business entity IDs are assigned by the application service.
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

// Create inserts the version with its direct environment binding and the version
// account subset in one transaction. A uniqueness failure rolls the whole
// aggregate back.
func (r *Repository) Create(ctx context.Context, v version.Version, accounts []version.AccountBinding) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("create version: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// A fresh version closes normally; normalize the empty Go default to the
	// domain constant so the closure_kind CHECK never sees an empty string.
	if v.ClosureKind == "" {
		v.ClosureKind = version.ClosureKindNormal
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO version (`+versionColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
	`, v.ID, v.Title, v.Description, v.Goal, v.Status, v.AutoDesignNewRequirements,
		v.EnvironmentID, v.ClosedAt, v.CloseReason, v.CloseRunID, v.CreatedBy,
		v.CreatedAt, v.UpdatedAt, v.ClosureKind, v.Unverified); err != nil {
		return wrapDB("insert version", err)
	}
	if err := insertBindingAccounts(ctx, tx, r.ids, v.ID, accounts); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("create version: commit: %w", err)
	}
	return nil
}

// UpdateBinding rewrites the editable binding projection of one existing version
// in one transaction: version.environment_id and the version_account rows. The
// version row is locked first and the write only proceeds while its updated_at
// still equals expectedUpdatedAt, so concurrent binding or stage writers settle
// deterministically (ErrBindingConflict) instead of silently losing an update.
func (r *Repository) UpdateBinding(ctx context.Context, v version.Version, accounts []version.AccountBinding, expectedUpdatedAt time.Time) error {
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

	tag, err := tx.Exec(ctx, `
		UPDATE version SET environment_id = $2, updated_at = $3 WHERE id = $1
	`, v.ID, v.EnvironmentID, v.UpdatedAt)
	if err != nil {
		return wrapDB("update version binding", err)
	}
	if tag.RowsAffected() == 0 {
		return version.ErrNotFound
	}
	if _, err := tx.Exec(ctx, `DELETE FROM version_account WHERE version_id = $1`, v.ID); err != nil {
		return wrapDB("delete version accounts", err)
	}
	if err := insertBindingAccounts(ctx, tx, r.ids, v.ID, accounts); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("update version binding: commit: %w", err)
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

// Get loads one version with its direct environment binding and account subset.
func (r *Repository) Get(ctx context.Context, id int64) (version.Version, []version.AccountBinding, error) {
	v, err := scanVersion(r.pool.QueryRow(ctx, `SELECT `+versionColumns+` FROM version WHERE id = $1`, id))
	if err != nil {
		return version.Version{}, nil, wrapDB("get version", err)
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

	accounts := make([]version.AccountBinding, 0)
	for accountRows.Next() {
		var accountID int64
		if err := accountRows.Scan(&accountID); err != nil {
			return version.Version{}, nil, fmt.Errorf("scan version account: %w", err)
		}
		accounts = append(accounts, version.AccountBinding{AccountID: accountID})
	}
	if err := accountRows.Err(); err != nil {
		return version.Version{}, nil, fmt.Errorf("get version accounts: %w", err)
	}
	return v, accounts, nil
}

// List returns version summaries ordered by ID descending. SUTCount is the
// number of SUTs in the environment each version binds (v0.2.4-B): it is derived
// from the resource catalog, never stored.
func (r *Repository) List(ctx context.Context, limit int, beforeID *int64) ([]version.Summary, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT v.id, v.title, v.status, v.created_at, v.updated_at,
			(SELECT count(*) FROM sut s WHERE s.environment_id = v.environment_id)
		FROM version v
		WHERE ($1::BIGINT IS NULL OR v.id < $1)
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

// Update rewrites the editable version fields. Status, the environment binding
// and the ownership columns are never touched by this API.
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
		UPDATE version SET status = $3, updated_at = $2
		WHERE id = $1 AND status = $4
	`, id, updatedAt, version.StatusRunning, version.StatusInitial)
	if err != nil {
		return false, wrapDB("mark version running", err)
	}
	return tag.RowsAffected() > 0, nil
}

// Close completes one running version (P4 spec §7.3/§7.4). It first takes the
// version-scoped advisory lock shared with execution intent creation, then locks
// the version row, re-runs the service predicate through recheck while the lock
// is held and only then applies the conditional running -> completed UPDATE. A
// recheck error rolls the transaction back untouched; a version that is no
// longer running is a state conflict.
func (r *Repository) Close(ctx context.Context, id int64, close *version.VersionCloseFields, recheck func(context.Context, version.Version) error) (version.Version, error) {
	// nil close fields are a caller contract violation, not a silent default:
	// fail loud with a typed field error instead of dereferencing nil (H-G4
	// fix-wave Close pointer nil guard). All production callers pass non-nil.
	if close == nil {
		return version.Version{}, platform.Invalid("close", "version close fields are required")
	}
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
	closureKind := close.ClosureKind
	if closureKind == "" {
		closureKind = version.ClosureKindNormal
	}
	current.Status = version.StatusCompleted
	current.ClosedAt = &closedAt
	current.CloseReason = close.CloseReason
	current.CloseRunID = cloneCloseRunID(close.CloseRunID)
	current.ClosureKind = closureKind
	current.Unverified = close.Unverified
	current.UpdatedAt = closedAt
	tag, err := tx.Exec(ctx, `
		UPDATE version SET status = $6, closed_at = $2, close_reason = $3,
			close_run_id = $4, updated_at = $5, closure_kind = $7, unverified = $8
		WHERE id = $1 AND status = $9
	`, id, closedAt, close.CloseReason, close.CloseRunID, closedAt,
		version.StatusCompleted, closureKind, close.Unverified, version.StatusRunning)
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

// ReopenIfCompleted flips completed -> running, clears the close projection
// (including the waiver: closure_kind back to normal, unverified false) AND
// undoes the waiver's requirement-level effects in the same transaction, then
// returns the updated version.
//
// R-G8a lifecycle (reopen means re-verification, not laundering): a requirement
// whose LATEST closure round was waived (check_result.waived=true) is reopened
// closed -> clarified with its waiver flag cleared, so it must earn closure
// again through the normal predicates or a fresh waiver. Requirements closed
// NORMALLY stay closed — their closure was legitimate. Any remaining waived
// flag (a clarified requirement marked but not yet closed) is cleared too.
// Without the undo, a waived closure would survive the version reopen and the
// reconciler would re-close the version as normal/unverified=false, laundering
// the waiver into a fully-verified-looking close.
//
// A version in any other status is returned unchanged with changed=false; a
// missing version yields ErrNotFound. The single-statement conditional UPDATE
// is the guard, so concurrent reopens and close/reopen races settle on one row
// version.
func (r *Repository) ReopenIfCompleted(ctx context.Context, id int64, reopenedAt time.Time) (version.Version, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return version.Version{}, false, fmt.Errorf("reopen version: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	reopened, err := scanVersion(tx.QueryRow(ctx, `
		UPDATE version SET status = $3, closed_at = NULL, close_reason = '',
			close_run_id = NULL, updated_at = $2, closure_kind = $4, unverified = false
		WHERE id = $1 AND status = $5
		RETURNING `+versionColumns, id, reopenedAt, version.StatusRunning, version.ClosureKindNormal, version.StatusCompleted))
	if err == nil {
		if err := undoWaivedRequirementClosures(ctx, tx, id, reopenedAt); err != nil {
			return version.Version{}, false, err
		}
		// Clear any remaining waiver flags (clarified requirements marked but not
		// yet closed) atomically with the version reopen so the reconciler can
		// never observe a running version with stale waived requirements.
		if _, err := tx.Exec(ctx, `
			UPDATE requirement SET waived = false, updated_at = $2
			WHERE version_id = $1 AND waived
		`, id, reopenedAt); err != nil {
			return version.Version{}, false, wrapDB("reopen version: clear requirement waivers", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return version.Version{}, false, fmt.Errorf("reopen version: commit: %w", err)
		}
		return reopened, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return version.Version{}, false, wrapDB("reopen version", err)
	}
	// No row was updated: distinguish "not completed" (no-op) from "missing".
	current, err := scanVersion(tx.QueryRow(ctx, `
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

// undoWaivedRequirementClosures reopens the version's waiver-closed requirements
// (R-G8a): closed requirements whose LATEST closure round carries
// check_result.waived=true go back to clarified with the waiver flag cleared and
// the round's reopen_count incremented (the same bookkeeping the requirement
// reopen path does). Normally-closed requirements are untouched. The closure
// round is the authoritative fact — the requirement.waived flag alone is not
// consulted for the undo decision. Status values are domain-constant query
// parameters (no inline status literal). The candidate SELECT takes FOR UPDATE
// OF r so the waiver-closed set is stable under concurrent requirement writes
// (H-G4 T6: the prior plain SELECT left a READ COMMITTED race window that only
// the guarded UPDATE backstopped).
func undoWaivedRequirementClosures(ctx context.Context, tx pgx.Tx, versionID int64, reopenedAt time.Time) error {
	rows, err := tx.Query(ctx, `
		SELECT r.id FROM requirement r
		WHERE r.version_id = $1 AND r.status = $2
		  AND COALESCE((
		    SELECT (rc.check_result->>'waived')::boolean
		    FROM requirement_closure rc
		    WHERE rc.requirement_id = r.id
		    ORDER BY rc.close_round DESC, rc.id DESC
		    LIMIT 1
		  ), false)
		ORDER BY r.id
		FOR UPDATE OF r
	`, versionID, string(version.RequirementClosed))
	if err != nil {
		return wrapDB("reopen version: find waiver-closed requirements", err)
	}
	var ids []int64
	for rows.Next() {
		var requirementID int64
		if err := rows.Scan(&requirementID); err != nil {
			rows.Close()
			return wrapDB("reopen version: scan waiver-closed requirement", err)
		}
		ids = append(ids, requirementID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return wrapDB("reopen version: read waiver-closed requirements", err)
	}
	if len(ids) == 0 {
		return nil
	}
	// The status guard re-checks closed under the row lock: a concurrent
	// requirement reopen (closed -> draft) wins and is never overwritten.
	// RETURNING captures exactly the rows the guard hit, so the reopen_count
	// bookkeeping below bumps only those rounds — never a row a concurrent
	// reopen already moved (H-G4 T6 reopen_count bookkeeping).
	updatedRows, err := tx.Query(ctx, `
		UPDATE requirement
		SET status = $3, closed_at = NULL, closed_by_run_id = NULL, waived = false, updated_at = $2
		WHERE id = ANY($1) AND status = $4
		RETURNING id
	`, ids, reopenedAt, string(version.RequirementClarified), string(version.RequirementClosed))
	if err != nil {
		return wrapDB("reopen version: undo waived requirement closures", err)
	}
	var updatedIDs []int64
	for updatedRows.Next() {
		var requirementID int64
		if err := updatedRows.Scan(&requirementID); err != nil {
			updatedRows.Close()
			return wrapDB("reopen version: scan reopened requirement", err)
		}
		updatedIDs = append(updatedIDs, requirementID)
	}
	updatedRows.Close()
	if err := updatedRows.Err(); err != nil {
		return wrapDB("reopen version: read reopened requirements", err)
	}
	if len(updatedIDs) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE requirement_closure
		SET reopen_count = reopen_count + 1, updated_at = $2
		WHERE id IN (
		  SELECT DISTINCT ON (requirement_id) id FROM requirement_closure
		  WHERE requirement_id = ANY($1)
		  ORDER BY requirement_id, close_round DESC, id DESC
		)
	`, updatedIDs, reopenedAt); err != nil {
		return wrapDB("reopen version: bump waived closure reopen count", err)
	}
	return nil
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
		WHERE v.status = $2
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
	`, limit, version.StatusInitial)
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

// The usage guards and ReferencingVersions below resolve a resource reference
// through the inverted hierarchy (v0.2.4-B): a version binds an environment, an
// environment owns SUTs and a SUT owns the assets. The joins read resource-context
// tables directly, which is the established pattern for these guard predicates
// (cf. the v0.22 version_sut joins they replace); no cross-context foreign key is
// implied.
//
// H-B14: the asset-status predicates are the resource domain constants bound as
// query parameters (never SQL-inline literals), and every guard that joins a SUT
// also mirrors resourceapp.SUTView.IsApproved -- (s.status = approved OR s.status
// = '') -- so a proposed/rejected SUT contributes nothing to effective scope and
// its assets are not "in use" (the delete guard must not over-block on resources
// the version can never execute against). The empty-status branch is UNREACHABLE
// from the database (sut.status is NOT NULL DEFAULT 'approved' with a CHECK,
// resource_0005); it is kept only to mirror IsApproved exactly. The status
// constants are pure cross-context vocabulary (single source in resource/domain),
// the reuse R-H9 recognizes; the predicate authority stays
// resourceapp.SUTView.IsApproved.
//
// H-B1 (v0.2.4-H): these SQL mirrors are the REMAINING ones. The handoff adapter's
// mirrors are gone (its resources block is assembled from
// resourceapp.HandoffResourceReader) and so is the resource code-pool mirror
// (Service.ListEnvironmentRepositories now gates in Go). What is left here is
// pinned per status against the authority by
// TestVersionSUTGateMirrorsIsApproved (internal/version/infrastructure, real DB):
// a rule-shape drift, not just a value drift, fails that pin.

// SUTInUse reports whether any version binds an environment that contains the
// SUT in effective scope (the SUT itself must be approved, IsApproved mirror).
func (r *Repository) SUTInUse(ctx context.Context, id int64) (bool, error) {
	return r.exists(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM version v
			JOIN sut s ON s.environment_id = v.environment_id
			WHERE s.id = $1 AND (s.status = $2 OR s.status = '')
		)`, id, resource.SUTStatusApproved)
}

// EnvironmentInUse reports whether any version binds the environment.
func (r *Repository) EnvironmentInUse(ctx context.Context, id int64) (bool, error) {
	return r.exists(ctx, `SELECT EXISTS (SELECT 1 FROM version WHERE environment_id = $1)`, id)
}

// PageInUse reports whether the page is inside the effective scope of a version
// binding: it is an approved page of an APPROVED SUT of a bound environment.
// Exclusions no longer exist, so every approved page of an approved SUT of the
// environment is in range.
func (r *Repository) PageInUse(ctx context.Context, id int64) (bool, error) {
	return r.exists(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM version v
			JOIN sut s ON s.environment_id = v.environment_id
			JOIN sut_page p ON p.sut_id = s.id
			WHERE p.id = $1 AND p.status = $2 AND (s.status = $3 OR s.status = '')
		)`, id, resource.PageStatusApproved, resource.SUTStatusApproved)
}

// APIInUse reports whether the API is inside the effective scope of a version
// binding (approved API of an approved SUT of a bound environment).
func (r *Repository) APIInUse(ctx context.Context, id int64) (bool, error) {
	return r.exists(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM version v
			JOIN sut s ON s.environment_id = v.environment_id
			JOIN sut_api a ON a.sut_id = s.id
			WHERE a.id = $1 AND a.status = $2 AND (s.status = $3 OR s.status = '')
		)`, id, resource.APIStatusApproved, resource.SUTStatusApproved)
}

// DatabaseInUse reports whether the target database is inside the effective
// scope of a version binding: it is active and owned by an approved SUT of a
// bound environment.
func (r *Repository) DatabaseInUse(ctx context.Context, id int64) (bool, error) {
	return r.exists(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM version v
			JOIN sut s ON s.environment_id = v.environment_id
			JOIN target_database d ON d.sut_id = s.id
			WHERE d.id = $1 AND d.status = $2 AND (s.status = $3 OR s.status = '')
		)`, id, resource.StatusActive, resource.SUTStatusApproved)
}

// AccountInUse reports whether any version enables the test account.
func (r *Repository) AccountInUse(ctx context.Context, id int64) (bool, error) {
	return r.exists(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM version_account WHERE account_id = $1
		)`, id)
}

// RepositoryInUse reports whether any APPROVED SUT association references the
// code repository (IsApproved mirror: a proposed/rejected SUT contributes nothing
// to the code pool). The version-level overrides of v0.22 are gone, so the SUT
// associations are the only reference.
func (r *Repository) RepositoryInUse(ctx context.Context, id int64) (bool, error) {
	return r.exists(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM sut_repository sr
			JOIN sut s ON s.id = sr.sut_id
			WHERE sr.repo_id = $1 AND (s.status = $2 OR s.status = '')
		)`, id, resource.SUTStatusApproved)
}

// ReferencingVersions lists the versions whose binding references a resource of
// one kind, joined with the version lifecycle status/stage so the resource
// delete conflict can report whether a binding edit is still possible. Its
// predicates mirror the usage guards, so a positive in-use check is explained by
// the versions it returns.
func (r *Repository) ReferencingVersions(ctx context.Context, kind string, id int64) ([]version.ReferencingVersion, error) {
	var query string
	args := []any{id}
	switch kind {
	case "sut":
		query = `
			SELECT DISTINCT v.id, v.title, v.status, v.stage, v.suspended_from
			FROM version v
			JOIN sut s ON s.environment_id = v.environment_id
			WHERE s.id = $1 AND (s.status = $2 OR s.status = '')
			ORDER BY v.id`
		args = append(args, resource.SUTStatusApproved)
	case "environment":
		query = `
			SELECT DISTINCT v.id, v.title, v.status, v.stage, v.suspended_from
			FROM version v
			WHERE v.environment_id = $1
			ORDER BY v.id`
	case "page":
		query = `
			SELECT DISTINCT v.id, v.title, v.status, v.stage, v.suspended_from
			FROM version v
			JOIN sut s ON s.environment_id = v.environment_id
			JOIN sut_page p ON p.sut_id = s.id
			WHERE p.id = $1 AND p.status = $2 AND (s.status = $3 OR s.status = '')
			ORDER BY v.id`
		args = append(args, resource.PageStatusApproved, resource.SUTStatusApproved)
	case "api":
		query = `
			SELECT DISTINCT v.id, v.title, v.status, v.stage, v.suspended_from
			FROM version v
			JOIN sut s ON s.environment_id = v.environment_id
			JOIN sut_api a ON a.sut_id = s.id
			WHERE a.id = $1 AND a.status = $2 AND (s.status = $3 OR s.status = '')
			ORDER BY v.id`
		args = append(args, resource.APIStatusApproved, resource.SUTStatusApproved)
	case "database":
		query = `
			SELECT DISTINCT v.id, v.title, v.status, v.stage, v.suspended_from
			FROM version v
			JOIN sut s ON s.environment_id = v.environment_id
			JOIN target_database d ON d.sut_id = s.id
			WHERE d.id = $1 AND d.status = $2 AND (s.status = $3 OR s.status = '')
			ORDER BY v.id`
		args = append(args, resource.StatusActive, resource.SUTStatusApproved)
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
			JOIN sut s ON s.environment_id = v.environment_id
			WHERE s.id IN (SELECT sut_id FROM sut_repository WHERE repo_id = $1)
			  AND (s.status = $2 OR s.status = '')
			ORDER BY v.id`
		args = append(args, resource.SUTStatusApproved)
	default:
		return nil, fmt.Errorf("referencing versions: unknown resource kind %q", kind)
	}
	rows, err := r.pool.Query(ctx, query, args...)
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

func (r *Repository) exists(ctx context.Context, query string, args ...any) (bool, error) {
	var used bool
	if err := r.pool.QueryRow(ctx, query, args...).Scan(&used); err != nil {
		return false, fmt.Errorf("version usage query: %w", err)
	}
	return used, nil
}

func scanVersion(row rowScanner) (version.Version, error) {
	var v version.Version
	if err := row.Scan(&v.ID, &v.Title, &v.Description, &v.Goal, &v.Status,
		&v.AutoDesignNewRequirements, &v.EnvironmentID, &v.ClosedAt, &v.CloseReason,
		&v.CloseRunID, &v.CreatedBy, &v.CreatedAt, &v.UpdatedAt, &v.ClosureKind, &v.Unverified); err != nil {
		return version.Version{}, err
	}
	return v, nil
}

var (
	_ version.Repository             = (*Repository)(nil)
	_ version.VersionCloseRepository = (*Repository)(nil)
)
