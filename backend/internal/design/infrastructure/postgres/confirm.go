package designpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/jackc/pgx/v5"
)

// confirmationTx is the design.ConfirmationTx implementation backed by one
// pgx transaction that holds the requirement advisory lock.
type confirmationTx struct {
	tx            pgx.Tx
	versionID     int64
	requirementID int64
}

var _ design.ConfirmationTx = (*confirmationTx)(nil)

// ResolveConfirmationTarget maps a confirmation scope/target to the owning
// requirement/scenario/case inside one version.
func (r *Repository) ResolveConfirmationTarget(ctx context.Context, versionID int64, scope string, targetID int64) (design.ConfirmationTarget, error) {
	target := design.ConfirmationTarget{Scope: scope, TargetID: targetID}
	switch scope {
	case design.ConfirmScopeCase:
		err := r.pool.QueryRow(ctx, `
			SELECT ce.id, s.id, s.requirement_id
			FROM case_entity ce
			JOIN case_scenario s ON s.id = ce.scenario_id
			WHERE ce.id = $1 AND s.version_id = $2
		`, targetID, versionID).Scan(&target.CaseID, &target.ScenarioID, &target.RequirementID)
		if errors.Is(err, pgx.ErrNoRows) {
			return design.ConfirmationTarget{}, design.ErrNotFound
		}
		if err != nil {
			return design.ConfirmationTarget{}, fmt.Errorf("resolve case confirmation target: %w", err)
		}
	case design.ConfirmScopeScenario:
		err := r.pool.QueryRow(ctx, `
			SELECT s.id, s.requirement_id
			FROM case_scenario s
			WHERE s.id = $1 AND s.version_id = $2
		`, targetID, versionID).Scan(&target.ScenarioID, &target.RequirementID)
		if errors.Is(err, pgx.ErrNoRows) {
			return design.ConfirmationTarget{}, design.ErrNotFound
		}
		if err != nil {
			return design.ConfirmationTarget{}, fmt.Errorf("resolve scenario confirmation target: %w", err)
		}
	case design.ConfirmScopeRequirement:
		err := r.pool.QueryRow(ctx, `
			SELECT r.id FROM requirement r WHERE r.id = $1 AND r.version_id = $2
		`, targetID, versionID).Scan(&target.RequirementID)
		if errors.Is(err, pgx.ErrNoRows) {
			return design.ConfirmationTarget{}, design.ErrNotFound
		}
		if err != nil {
			return design.ConfirmationTarget{}, fmt.Errorf("resolve requirement confirmation target: %w", err)
		}
	default:
		return design.ConfirmationTarget{}, fmt.Errorf("unsupported confirmation scope %q", scope)
	}
	return target, nil
}

// WithConfirmationLock runs fn with the requirement advisory lock held. The
// confirmation reads take row locks on the selected case versions so concurrent
// batches serialize.
func (r *Repository) WithConfirmationLock(ctx context.Context, versionID, requirementID int64, fn func(context.Context, design.ConfirmationTx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin confirmation transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := lockRequirement(ctx, tx, versionID, requirementID); err != nil {
		return err
	}
	if err := fn(ctx, &confirmationTx{tx: tx, versionID: versionID, requirementID: requirementID}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit confirmation transaction: %w", err)
	}
	return nil
}

// Requirement returns the locked requirement projection.
func (t *confirmationTx) Requirement(ctx context.Context) (design.RequirementRef, error) {
	return requirementRef(ctx, t.tx, t.versionID, t.requirementID)
}

// Scenario returns one scenario of the confirmation requirement.
func (t *confirmationTx) Scenario(ctx context.Context, scenarioID int64) (design.Scenario, error) {
	row := t.tx.QueryRow(ctx, `
		SELECT id, version_id, requirement_id, requirement_version_id, name, description,
		       COALESCE(version_sut_ids, '[]'::jsonb), COALESCE(page_ids, '[]'::jsonb),
		       COALESCE(api_ids, '[]'::jsonb), sort_order, created_at, updated_at
		FROM case_scenario
		WHERE id = $1 AND version_id = $2 AND requirement_id = $3
	`, scenarioID, t.versionID, t.requirementID)
	scenario, err := scanScenario(row)
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, design.ErrNotFound) {
		return design.Scenario{}, design.ErrNotFound
	}
	return scenario, err
}

// Case returns one locked case of the confirmation requirement.
func (t *confirmationTx) Case(ctx context.Context, caseID int64) (design.CaseWithCurrent, error) {
	row := t.tx.QueryRow(ctx, caseWithCurrentQuery+`
		WHERE ce.id = $1 AND s.version_id = $2 AND s.requirement_id = $3
		FOR UPDATE OF ce, cv
	`, caseID, t.versionID, t.requirementID)
	item, err := scanCaseWithCurrent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return design.CaseWithCurrent{}, design.ErrNotFound
	}
	return item, err
}

// CasesByScenario returns the locked cases of one scenario.
func (t *confirmationTx) CasesByScenario(ctx context.Context, scenarioID int64) ([]design.CaseWithCurrent, error) {
	rows, err := t.tx.Query(ctx, caseWithCurrentQuery+`
		WHERE s.id = $1 AND s.version_id = $2 AND s.requirement_id = $3
		ORDER BY ce.id
		FOR UPDATE OF ce, cv
	`, scenarioID, t.versionID, t.requirementID)
	if err != nil {
		return nil, fmt.Errorf("read confirmation scenario cases: %w", err)
	}
	return scanCaseList(rows)
}

// CasesByRequirement returns the locked cases across all current-version
// scenarios of the confirmation requirement.
func (t *confirmationTx) CasesByRequirement(ctx context.Context) ([]design.CaseWithCurrent, error) {
	rows, err := t.tx.Query(ctx, caseWithCurrentQuery+`
		WHERE s.version_id = $1 AND s.requirement_id = $2
		ORDER BY ce.id
		FOR UPDATE OF ce, cv
	`, t.versionID, t.requirementID)
	if err != nil {
		return nil, fmt.Errorf("read confirmation requirement cases: %w", err)
	}
	return scanCaseList(rows)
}

// ConfirmCaseVersion freezes one draft version and projects the entity status.
func (t *confirmationTx) ConfirmCaseVersion(ctx context.Context, command design.ConfirmCaseVersionCommand) error {
	confirmation := command.Confirmation
	at := confirmation.ConfirmedAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	tag, err := t.tx.Exec(ctx, `
		UPDATE case_version
		SET status = 'confirmed', updated_at = $2
		WHERE id = $1 AND status = 'draft'
	`, confirmation.CaseVersionID, at)
	if err != nil {
		return fmt.Errorf("confirm case version: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return design.ErrStateConflict
	}

	var caseID int64
	if err := t.tx.QueryRow(ctx, `
		SELECT case_id FROM case_version WHERE id = $1
	`, confirmation.CaseVersionID).Scan(&caseID); err != nil {
		return fmt.Errorf("read confirmed case id: %w", err)
	}
	entityStatus := command.EntityStatus
	if entityStatus == "" {
		entityStatus = design.CaseStatusConfirmed
	}
	if _, err := t.tx.Exec(ctx, `
		UPDATE case_entity SET status = $2, updated_at = $3 WHERE id = $1
	`, caseID, entityStatus, at); err != nil {
		return fmt.Errorf("project case entity status: %w", err)
	}

	affected, err := encodeIDStrings(confirmation.AffectedCaseVersionIDs)
	if err != nil {
		return err
	}
	snapshot := confirmation.Snapshot
	if snapshot == nil {
		snapshot = map[string]any{}
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode confirmation snapshot: %w", err)
	}
	if _, err := t.tx.Exec(ctx, `
		INSERT INTO case_confirmation (
			id, case_version_id, scope, target_scope_id, batch_id, affected_case_version_ids,
			snapshot, confirmed_by, confirmed_at, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb,$8,$9,$9,$9)
	`, confirmation.ID, confirmation.CaseVersionID, confirmation.Scope,
		confirmation.TargetScopeID, confirmation.BatchID, string(affected),
		string(snapshotJSON), confirmation.ConfirmedBy, confirmation.ConfirmedAt); err != nil {
		return fmt.Errorf("insert case confirmation: %w", err)
	}
	return nil
}

// SupersedeVersions moves older confirmed/stale versions of one case to
// superseded after a new version is confirmed (spec §4.1).
func (t *confirmationTx) SupersedeVersions(ctx context.Context, caseID, keepVersionID int64, at time.Time) error {
	_, err := t.tx.Exec(ctx, `
		UPDATE case_version
		SET status = 'superseded', updated_at = $3
		WHERE case_id = $1 AND id <> $2 AND status IN ('confirmed', 'stale')
	`, caseID, keepVersionID, at)
	if err != nil {
		return fmt.Errorf("supersede older case versions: %w", err)
	}
	return nil
}

func scanCaseList(rows pgx.Rows) ([]design.CaseWithCurrent, error) {
	defer rows.Close()
	items := make([]design.CaseWithCurrent, 0)
	for rows.Next() {
		item, err := scanCaseWithCurrent(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan case list: %w", err)
	}
	return items, nil
}

// WithStaleLock runs fn with the requirement advisory lock held.
func (r *Repository) WithStaleLock(ctx context.Context, versionID, requirementID int64, fn func(context.Context, design.StaleTx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin stale transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := lockRequirement(ctx, tx, versionID, requirementID); err != nil {
		return err
	}
	if err := fn(ctx, &staleTx{tx: tx, versionID: versionID, requirementID: requirementID}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit stale transaction: %w", err)
	}
	return nil
}

// staleTx is the design.StaleTx implementation.
type staleTx struct {
	tx            pgx.Tx
	versionID     int64
	requirementID int64
}

var _ design.StaleTx = (*staleTx)(nil)

func (t *staleTx) Requirement(ctx context.Context) (design.RequirementRef, error) {
	return requirementRef(ctx, t.tx, t.versionID, t.requirementID)
}

func (t *staleTx) CasesByRequirement(ctx context.Context) ([]design.CaseWithCurrent, error) {
	rows, err := t.tx.Query(ctx, caseWithCurrentQuery+`
		WHERE s.version_id = $1 AND s.requirement_id = $2
		ORDER BY ce.id
		FOR UPDATE OF ce, cv
	`, t.versionID, t.requirementID)
	if err != nil {
		return nil, fmt.Errorf("read stale requirement cases: %w", err)
	}
	return scanCaseList(rows)
}

func (t *staleTx) MarkCaseVersionStale(ctx context.Context, caseVersionID int64, at time.Time) error {
	if _, err := t.tx.Exec(ctx, `
		UPDATE case_version SET status = 'stale', updated_at = $2
		WHERE id = $1 AND status = 'confirmed'
	`, caseVersionID, at); err != nil {
		return fmt.Errorf("mark case version stale: %w", err)
	}
	return nil
}

func (t *staleTx) MarkCaseEntityStale(ctx context.Context, caseID int64, at time.Time) error {
	if _, err := t.tx.Exec(ctx, `
		UPDATE case_entity SET status = 'stale', updated_at = $2
		WHERE id = $1 AND status = 'confirmed'
	`, caseID, at); err != nil {
		return fmt.Errorf("mark case entity stale: %w", err)
	}
	return nil
}

// ListOutdatedConfirmedRequirements is the reconciler stale safety net: it
// finds requirements whose current revision moved while a confirmed current
// case version still points at an older revision.
func (r *Repository) ListOutdatedConfirmedRequirements(ctx context.Context, limit int) ([]design.OutdatedRequirement, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT r.version_id, r.id, r.current_version_id
		FROM requirement r
		JOIN case_scenario s ON s.requirement_id = r.id AND s.version_id = r.version_id
		JOIN case_entity ce ON ce.scenario_id = s.id
		JOIN case_version cv ON cv.id = ce.current_version_id
		WHERE cv.status = 'confirmed'
		  AND r.current_version_id IS NOT NULL
		  AND s.requirement_version_id <> r.current_version_id
		ORDER BY r.id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list outdated confirmed requirements: %w", err)
	}
	defer rows.Close()
	outdated := make([]design.OutdatedRequirement, 0)
	for rows.Next() {
		var item design.OutdatedRequirement
		if err := rows.Scan(&item.VersionID, &item.RequirementID, &item.CurrentRequirementVersionID); err != nil {
			return nil, fmt.Errorf("scan outdated requirement: %w", err)
		}
		outdated = append(outdated, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan outdated requirements: %w", err)
	}
	return outdated, nil
}
