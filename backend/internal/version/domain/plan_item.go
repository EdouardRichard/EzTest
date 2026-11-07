package version

import (
	"context"
	"errors"
	"fmt"
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
	// ErrPlanRevisionConflict reports a plan write whose expected revision no
	// longer matches the stored row. It backs the optimistic lock of the plan
	// write path (spec R-A8-2, DB-20).
	ErrPlanRevisionConflict = errors.New("plan item revision conflict")
)

// PlanRevisionConflictError is the typed stale-revision error of the plan
// store. It unwraps to ErrPlanRevisionConflict so callers can use errors.Is
// while still reading the stored revision through errors.As.
type PlanRevisionConflictError struct {
	ItemID   int64
	Expected int
	Actual   int
}

func (e *PlanRevisionConflictError) Error() string {
	return fmt.Sprintf("plan item %d revision conflict: expected %d, stored %d",
		e.ItemID, e.Expected, e.Actual)
}

// Unwrap exposes ErrPlanRevisionConflict to errors.Is.
func (e *PlanRevisionConflictError) Unwrap() error { return ErrPlanRevisionConflict }

// PlanItem is one ordered entry of a version's task plan (spec §6.1,
// task_plan.md). The logical Order field is stored in the sort_order column
// (the name "order" is a SQL keyword). DependsOn holds plan-item IDs of the
// same version; the service lists a dependency before its dependants. Revision
// is the optimistic-lock token of the plan write path: it starts at 1, is
// bumped by every real write, and a writer must present the revision it read.
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
	Revision   int
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
	if p.Revision < 0 {
		return platform.Invalid("revision", "must not be negative")
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

// PlanPatch is a partial plan-item write of the revision-guarded update path
// (spec R-A8-2). Empty Title/Status/Decision mean "keep the stored value"; a
// plan item field cannot be cleared through a patch. A nil Order keeps the
// stored sort order. Phase, DependsOn and WorkItemID are not patchable: the
// scheduling fields have dedicated owners.
type PlanPatch struct {
	Title    string
	Status   string
	Decision string
	Order    *int
}

// Apply returns item with the provided patch fields applied and textual fields
// trimmed. The result is not validated or persisted; the repository compares it
// with the stored row and writes only real changes.
func (p PlanPatch) Apply(item PlanItem) PlanItem {
	if title := strings.TrimSpace(p.Title); title != "" {
		item.Title = title
	}
	if status := strings.TrimSpace(p.Status); status != "" {
		item.Status = status
	}
	if decision := strings.TrimSpace(p.Decision); decision != "" {
		item.Decision = decision
	}
	if p.Order != nil {
		item.Order = *p.Order
	}
	return item
}

// PlanRepository persists the version_plan_item store.
type PlanRepository interface {
	// List returns one version's plan items in (sort_order, id) order, filtered
	// by status when it is not empty. Dependency ordering is applied by the
	// application service.
	List(ctx context.Context, versionID int64, status string) ([]PlanItem, error)

	// Upsert inserts a plan item or updates the row with the same ID. It reports
	// whether the stored item actually changed (a create always changes), so the
	// service only publishes plan.item_changed for real changes. A create starts
	// at revision 1; an update bumps revision by one. Upsert does not guard on
	// revision: it is the unscoped legacy/admin write, while Update is the
	// optimistic path of the plan write entry points. A depends_on entry that is
	// not an existing plan item of the same version yields
	// ErrPlanDependencyNotFound; a missing version yields ErrNotFound.
	Upsert(ctx context.Context, item PlanItem) (stored PlanItem, changed bool, err error)

	// Update applies a partial write guarded by revision optimistic locking: it
	// writes only when the row belongs to versionID and its stored revision
	// equals expectedRevision, then bumps revision by one and refreshes
	// updated_at. Empty patch fields keep stored values and a no-op patch
	// reports changed=false without bumping the revision. A stale revision
	// returns an error wrapping ErrPlanRevisionConflict (a
	// *PlanRevisionConflictError carrying the stored revision); a missing item,
	// or an item of another version, returns ErrPlanNotFound.
	Update(ctx context.Context, versionID, itemID int64, patch PlanPatch, expectedRevision int) (stored PlanItem, changed bool, err error)
}
