package version

import (
	"context"
	"errors"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// PlanItemStatusPending is the DB default of version_plan_item.status. Plan
// statuses are maintained by PlanService; the DB/contracts intentionally do not
// freeze an enum, so any non-empty value is accepted.
const PlanItemStatusPending = "pending"

// PlanItem persistence errors.
var (
	// ErrPlanNotFound reports a missing version_plan_item row.
	ErrPlanNotFound = errors.New("plan item not found")
	// ErrPlanDependencyNotFound reports a depends_on entry that does not name a
	// plan item of the same version.
	ErrPlanDependencyNotFound = errors.New("plan item dependency not found")
)

// PlanItem is one ordered entry of a version's task plan (spec §6.1,
// task_plan.md). The logical Order field is stored in the sort_order column
// (the name "order" is a SQL keyword). DependsOn holds plan-item IDs of the
// same version; the service lists a dependency before its dependants.
type PlanItem struct {
	ID         int64
	VersionID  int64
	Phase      string
	Title      string
	Status     string
	DependsOn  []int64
	WorkItemID *int64
	Decision   string
	Order      int
}

// Normalize trims textual fields, defaults an empty status to pending and
// deduplicates DependsOn while preserving its order. It is safe to call more
// than once.
func (p *PlanItem) Normalize() {
	p.Phase = strings.TrimSpace(p.Phase)
	p.Title = strings.TrimSpace(p.Title)
	p.Status = strings.TrimSpace(p.Status)
	if p.Status == "" {
		p.Status = PlanItemStatusPending
	}
	p.Decision = strings.TrimSpace(p.Decision)
	if p.DependsOn == nil {
		p.DependsOn = []int64{}
	}
	p.DependsOn = uniquePlanIDs(p.DependsOn)
}

// Validate checks the persisted invariants of one plan item. Dependency
// existence (same version) is a repository concern because it needs the store.
func (p PlanItem) Validate() error {
	if p.ID < 0 {
		return platform.Invalid("id", "must not be negative")
	}
	if p.VersionID <= 0 {
		return platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if err := validation.String("phase", p.Phase, 1, 0); err != nil {
		return err
	}
	if err := validation.String("title", p.Title, 1, 0); err != nil {
		return err
	}
	if err := validation.String("status", p.Status, 1, 0); err != nil {
		return err
	}
	if p.Order < 0 {
		return platform.Invalid("order", "must not be negative")
	}
	for _, dependencyID := range p.DependsOn {
		if dependencyID <= 0 {
			return platform.Invalid("depends_on", "must contain positive decimal IDs")
		}
		if dependencyID == p.ID {
			return platform.Invalid("depends_on", "must not contain the plan item itself")
		}
	}
	if p.WorkItemID != nil && *p.WorkItemID <= 0 {
		return platform.Invalid("work_item_id", "must be a positive decimal ID")
	}
	return nil
}

// uniquePlanIDs removes duplicates while preserving order. Non-positive values
// are kept so Validate can reject them instead of silently dropping caller
// mistakes.
func uniquePlanIDs(ids []int64) []int64 {
	unique := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	return unique
}

// PlanRepository persists the version_plan_item store.
type PlanRepository interface {
	// List returns one version's plan items in (sort_order, id) order, filtered
	// by status when it is not empty. Dependency ordering is applied by the
	// application service.
	List(ctx context.Context, versionID int64, status string) ([]PlanItem, error)

	// Upsert inserts a plan item or updates the row with the same ID. It reports
	// whether the stored item actually changed (a create always changes), so the
	// service only publishes plan.item_changed for real changes. A depends_on
	// entry that is not an existing plan item of the same version yields
	// ErrPlanDependencyNotFound; a missing version yields ErrNotFound.
	Upsert(ctx context.Context, item PlanItem) (stored PlanItem, changed bool, err error)
}
