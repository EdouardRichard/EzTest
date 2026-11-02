package versionpg

import (
	"context"
	"errors"
	"fmt"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// planItemColumns is the column list of one version_plan_item row. The logical
// Order field is persisted as sort_order (the name "order" is a SQL keyword).
const planItemColumns = `id, version_id, phase, title, status, depends_on,
	work_item_id, decision, sort_order`

// PlanRepository stores the version task plan (spec §6.1, task_plan.md).
type PlanRepository struct {
	pool *pgxpool.Pool
}

// NewPlanRepository wraps a pgx pool. Plan-item IDs are assigned by the
// application service.
func NewPlanRepository(pool *pgxpool.Pool) *PlanRepository {
	return &PlanRepository{pool: pool}
}

// List returns one version's plan items filtered by status (empty = all) in
// (sort_order, id) order. Dependency ordering is applied by the service.
func (r *PlanRepository) List(ctx context.Context, versionID int64, status string) ([]version.PlanItem, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+planItemColumns+` FROM version_plan_item
		WHERE version_id = $1 AND ($2 = '' OR status = $2)
		ORDER BY sort_order, id
	`, versionID, status)
	if err != nil {
		return nil, fmt.Errorf("list plan items: %w", err)
	}
	defer rows.Close()

	items := make([]version.PlanItem, 0)
	for rows.Next() {
		item, err := scanPlanItem(rows)
		if err != nil {
			return nil, fmt.Errorf("list plan items: scan: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list plan items: %w", err)
	}
	return items, nil
}

// Upsert inserts a new plan item or updates the row with the same primary key.
// It reports whether the stored values actually changed so the service only
// publishes plan.item_changed for real changes. depends_on entries must name
// existing plan items of the same version; a plan item can never move to
// another version.
func (r *PlanRepository) Upsert(ctx context.Context, item version.PlanItem) (version.PlanItem, bool, error) {
	if item.ID <= 0 || item.VersionID <= 0 {
		return version.PlanItem{}, false, fmt.Errorf("upsert plan item: id and version id must be positive")
	}
	dependencies := item.DependsOn
	if dependencies == nil {
		dependencies = []int64{}
	}
	for _, dependencyID := range dependencies {
		if dependencyID == item.ID {
			return version.PlanItem{}, false, version.ErrPlanDependencyNotFound
		}
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return version.PlanItem{}, false, fmt.Errorf("upsert plan item: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if len(dependencies) > 0 {
		var found int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM version_plan_item
			WHERE version_id = $1 AND id = ANY($2::BIGINT[])
		`, item.VersionID, dependencies).Scan(&found); err != nil {
			return version.PlanItem{}, false, fmt.Errorf("upsert plan item: check dependencies: %w", err)
		}
		if found != len(dependencies) {
			return version.PlanItem{}, false, version.ErrPlanDependencyNotFound
		}
	}

	existing, err := scanPlanItem(tx.QueryRow(ctx, `
		SELECT `+planItemColumns+` FROM version_plan_item WHERE id = $1 FOR UPDATE
	`, item.ID))
	switch {
	case err == nil:
		if existing.VersionID != item.VersionID {
			return version.PlanItem{}, false, version.ErrPlanNotFound
		}
		if sameStoredPlanItem(existing, item) {
			return existing, false, nil
		}
		stored, err := scanPlanItem(tx.QueryRow(ctx, `
			UPDATE version_plan_item SET
				phase = $2, title = $3, status = $4, depends_on = $5,
				work_item_id = $6, decision = $7, sort_order = $8, updated_at = now()
			WHERE id = $1
			RETURNING `+planItemColumns+`
		`, item.ID, item.Phase, item.Title, item.Status, dependencies,
			item.WorkItemID, item.Decision, item.Order))
		if err != nil {
			return version.PlanItem{}, false, fmt.Errorf("upsert plan item: update: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return version.PlanItem{}, false, fmt.Errorf("upsert plan item: commit: %w", err)
		}
		return stored, true, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return version.PlanItem{}, false, fmt.Errorf("upsert plan item: lookup: %w", err)
	}

	stored, err := scanPlanItem(tx.QueryRow(ctx, `
		INSERT INTO version_plan_item (
			id, version_id, phase, title, status, depends_on,
			work_item_id, decision, sort_order, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now(), now())
		RETURNING `+planItemColumns+`
	`, item.ID, item.VersionID, item.Phase, item.Title, item.Status, dependencies,
		item.WorkItemID, item.Decision, item.Order))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return version.PlanItem{}, false, version.ErrNotFound
		}
		return version.PlanItem{}, false, fmt.Errorf("upsert plan item: insert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return version.PlanItem{}, false, fmt.Errorf("upsert plan item: commit: %w", err)
	}
	return stored, true, nil
}

func scanPlanItem(row rowScanner) (version.PlanItem, error) {
	var item version.PlanItem
	if err := row.Scan(&item.ID, &item.VersionID, &item.Phase, &item.Title,
		&item.Status, &item.DependsOn, &item.WorkItemID, &item.Decision, &item.Order); err != nil {
		return version.PlanItem{}, err
	}
	if item.DependsOn == nil {
		item.DependsOn = []int64{}
	}
	return item, nil
}

// sameStoredPlanItem compares the persisted fields of one plan item. Ordering
// slices are compared element by element; the timestamps are not part of the
// domain entity.
func sameStoredPlanItem(a, b version.PlanItem) bool {
	if a.ID != b.ID || a.VersionID != b.VersionID || a.Phase != b.Phase ||
		a.Title != b.Title || a.Status != b.Status || a.Decision != b.Decision ||
		a.Order != b.Order {
		return false
	}
	if (a.WorkItemID == nil) != (b.WorkItemID == nil) {
		return false
	}
	if a.WorkItemID != nil && *a.WorkItemID != *b.WorkItemID {
		return false
	}
	if len(a.DependsOn) != len(b.DependsOn) {
		return false
	}
	for i := range a.DependsOn {
		if a.DependsOn[i] != b.DependsOn[i] {
			return false
		}
	}
	return true
}

var _ version.PlanRepository = (*PlanRepository)(nil)
