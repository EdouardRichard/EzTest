package designpg

// W2-11 (WP-6, spec R-EX-6 / DB-30 / API-41): design-side persistence for the
// additive discovery case revision and its rollback.
//
// The observation provenance is a read-only projection over the execution
// tables (logical BIGINT references, no cross-context foreign keys): the design
// context answers "which requirement did this discovered target come from?"
// without importing the execution application. Rollback writes only design
// tables and never deletes a revision (DB-28: no case_revision table).

import (
	"context"
	"errors"
	"fmt"
	"time"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/jackc/pgx/v5"
)

var _ design.RollbackRepository = (*Repository)(nil)

// CaseVersionByObservation returns the revision produced by one discovery
// observation inside a version (the auto-confirm idempotency key).
func (r *Repository) CaseVersionByObservation(ctx context.Context, versionID, observationID int64) (design.CaseVersion, bool, error) {
	return r.caseVersionBy(ctx, `
		WHERE s.version_id = $1 AND cv.source_observation_id = $2
	`, versionID, observationID)
}

// CaseVersionByRollbackOf returns the replacement revision of one rolled-back
// auto-confirmed revision (the rollback idempotency key).
func (r *Repository) CaseVersionByRollbackOf(ctx context.Context, versionID, rolledBackVersionID int64) (design.CaseVersion, bool, error) {
	return r.caseVersionBy(ctx, `
		WHERE s.version_id = $1 AND cv.rollback_of = $2
	`, versionID, rolledBackVersionID)
}

func (r *Repository) caseVersionBy(ctx context.Context, where string, args ...any) (design.CaseVersion, bool, error) {
	var caseVersionID int64
	err := r.pool.QueryRow(ctx, `
		SELECT cv.id
		FROM case_version cv
		JOIN case_entity ce ON ce.id = cv.case_id
		JOIN case_scenario s ON s.id = ce.scenario_id
	`+where+`
		ORDER BY cv.id DESC
		LIMIT 1
	`, args...).Scan(&caseVersionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return design.CaseVersion{}, false, nil
	}
	if err != nil {
		return design.CaseVersion{}, false, fmt.Errorf("find discovery case version: %w", err)
	}
	version, err := loadCaseVersion(ctx, r.pool, caseVersionID)
	if err != nil {
		return design.CaseVersion{}, false, err
	}
	return version, true, nil
}

// WithRollbackLock runs one rollback transaction with the requirement advisory
// lock held, so a concurrent rollback of the same revision serializes and the
// second call sees the first replacement.
func (r *Repository) WithRollbackLock(ctx context.Context, versionID, requirementID int64, fn func(context.Context, design.RollbackTx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin design rollback transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := lockRequirement(ctx, tx, versionID, requirementID); err != nil {
		return err
	}
	if err := fn(ctx, &rollbackTx{tx: tx, versionID: versionID, requirementID: requirementID}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit design rollback transaction: %w", err)
	}
	return nil
}

// rollbackTx is the design.RollbackTx implementation backed by one pgx
// transaction.
type rollbackTx struct {
	tx            pgx.Tx
	versionID     int64
	requirementID int64
}

var _ design.RollbackTx = (*rollbackTx)(nil)

func (t *rollbackTx) Requirement(ctx context.Context) (design.RequirementRef, error) {
	return requirementRef(ctx, t.tx, t.versionID, t.requirementID)
}

// CaseVersionForUpdate locks and loads one case version with its children. The
// row lock serializes concurrent rollbacks of the same revision.
func (t *rollbackTx) CaseVersionForUpdate(ctx context.Context, caseVersionID int64) (design.CaseVersion, error) {
	var lockedID int64
	if err := t.tx.QueryRow(ctx, `
		SELECT id FROM case_version WHERE id = $1 FOR UPDATE
	`, caseVersionID).Scan(&lockedID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return design.CaseVersion{}, design.ErrNotFound
		}
		return design.CaseVersion{}, fmt.Errorf("lock case version: %w", err)
	}
	return loadCaseVersion(ctx, t.tx, caseVersionID)
}

// RollbackReplacement returns the existing replacement of one rolled-back
// revision (nil when none was written yet).
func (t *rollbackTx) RollbackReplacement(ctx context.Context, caseID, rolledBackVersionID int64) (*design.CaseVersion, error) {
	var replacementID int64
	err := t.tx.QueryRow(ctx, `
		SELECT id FROM case_version
		WHERE case_id = $1 AND rollback_of = $2
		ORDER BY revision DESC
		LIMIT 1
	`, caseID, rolledBackVersionID).Scan(&replacementID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read rollback replacement: %w", err)
	}
	version, err := loadCaseVersion(ctx, t.tx, replacementID)
	if err != nil {
		return nil, err
	}
	return &version, nil
}

// PriorConfirmedVersion returns the confirmed revision immediately before
// beforeRevision, i.e. the revision the auto-confirmation replaced.
func (t *rollbackTx) PriorConfirmedVersion(ctx context.Context, caseID int64, beforeRevision int) (design.CaseVersion, error) {
	var priorID int64
	err := t.tx.QueryRow(ctx, `
		SELECT cv.id
		FROM case_confirmation cc
		JOIN case_version cv ON cv.id = cc.case_version_id
		WHERE cv.case_id = $1 AND cv.revision < $2
		ORDER BY cv.revision DESC
		LIMIT 1
	`, caseID, beforeRevision).Scan(&priorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return design.CaseVersion{}, design.ErrNotFound
	}
	if err != nil {
		return design.CaseVersion{}, fmt.Errorf("read prior confirmed version: %w", err)
	}
	return loadCaseVersion(ctx, t.tx, priorID)
}

// InsertCaseVersion reuses the propose writer inside the rollback transaction.
func (t *rollbackTx) InsertCaseVersion(ctx context.Context, version design.CaseVersion) (design.CaseVersion, error) {
	return (&proposeTx{tx: t.tx, versionID: t.versionID}).InsertCaseVersion(ctx, version)
}

func (t *rollbackTx) SupersedeCaseVersion(ctx context.Context, caseVersionID int64, at time.Time) error {
	if _, err := t.tx.Exec(ctx, `
		UPDATE case_version
		SET status = 'superseded', updated_at = $2
		WHERE id = $1 AND status = 'confirmed'
	`, caseVersionID, at); err != nil {
		return fmt.Errorf("supersede rolled-back case version: %w", err)
	}
	return nil
}

func (t *rollbackTx) SetCurrentVersion(ctx context.Context, caseID, caseVersionID int64, status string, at time.Time) error {
	return (&proposeTx{tx: t.tx, versionID: t.versionID}).SetCurrentVersion(ctx, caseID, caseVersionID, status, at)
}

func (t *rollbackTx) InsertConfirmation(ctx context.Context, confirmation design.Confirmation) error {
	return insertConfirmation(ctx, t.tx, confirmation)
}
