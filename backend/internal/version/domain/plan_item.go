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

// PlanItemStatusInProgress is the open (non-terminal) plan-item status for work
// underway. The runtime Host-turn plan builder writes it and reads it back
// (cmd/eztest/v022_runtime_tools.go), so THAT read/write pair cannot drift apart.
// The extraction milestone's open-run projection (application.ExtractionPlanStatusRunning)
// currently shares the same value but is a SEPARATE constant, not derived from this
// one. The AST writer-link test (plan_item_test.go) enumerates that constant and
// requires its value to classify terminal or known-open, so a drift to an unclassified
// value (e.g. "running") fails; a drift to another known-open value (e.g. "pending")
// would pass classification yet break value equality. Deriving ExtractionPlanStatusRunning
// from this constant is registered as a named debt (task-18-report.md R4).
const PlanItemStatusInProgress = "in_progress"

// planItemTerminalStatuses is the single authoritative set of FINISHED plan-item
// statuses (lower-case). Both PlanItemTerminal (the Go predicate the handoff
// reader calls) and PlanItemTerminalStatuses (the text[] the briefing reader binds
// into SQL) derive from this one list, so THESE TWO readers (handoff + briefing)
// cannot disagree about "is this item finished?". The three former backend reader
// copies (agent context/briefing, agent summary, tools/task_complete) now DERIVE from
// this authority via version.PlanItemTerminal — v0.2.4-C T22 closed the live
// task_complete two-value divergence (regression pin
// TestPlanItemCompletionAcceptsAllTerminalStatuses). The only remaining mirror is the
// frontend plan-status.ts (consumed by PlanPanel), a guarded cross-language copy pinned
// both ways: Go TestEnumAuthorityFrontendPlanStatus reads that file, and
// plan-status.test.ts pins its set equal to this authority.
//
// The set is derived from the writers, not from either legacy reader copy
// (v0.2.4-B fix round 2: the briefing and handoff carried divergent hand-written
// sets, so a failed/closed item was finished in one reader and open in the other):
//   - done, failed: the extraction milestone writes these as the FINAL states of a
//     run (application.ExtractionPlanStatusDone / ExtractionPlanStatusFailed,
//     mapped from ExtractionRunDone / ExtractionRunFailed); the agent completion
//     path writes done (application.CompletionPlanStatusDone);
//   - in_progress: the milestone's open-run state, the runtime plan tools'
//     in-progress state, and the settlement writeback (application.SettlementPlanStatus
//     = "in_progress": a run that settled without a verified completion did NOT
//     finish its item, so the next run can continue it) — NON-terminal;
//   - completed, cancelled, canceled, skipped, closed: completion / abandonment /
//     skip / closure states the model may write through the free-form plan.update
//     tool — each denotes an item that will not progress further. Both cancelled
//     spellings are accepted because the tool does not constrain the status string.
//
// pending (the planner's initial status and Normalize's default) and in_progress
// (the milestone's open-run status) are NON-terminal, as is any other ongoing-work
// value. This predicate CLASSIFIES values for readers; it does not freeze what the
// DB/contracts accept (the enum above stays open).
var planItemTerminalStatuses = []string{
	"done", "completed", "cancelled", "canceled", "skipped", "failed", "closed",
}

// PlanItemTerminal reports whether a plan-item status denotes a finished item —
// one that must not be presented as open work. Normalization (trim + lower-case)
// matches the legacy handoff classifier so a stored " Done " or "FAILED"
// classifies identically. It is the single classification authority shared by the
// agent briefing and the design handoff readers.
func PlanItemTerminal(status string) bool {
	normalized := strings.ToLower(strings.TrimSpace(status))
	for _, terminal := range planItemTerminalStatuses {
		if normalized == terminal {
			return true
		}
	}
	return false
}

// PlanItemTerminalStatuses returns a copy of the authoritative terminal set for
// readers that filter in SQL. Bind it as a text[] parameter and compare against
// lower(btrim(status)) so the SQL classification matches PlanItemTerminal's
// trim + lower-case normalization. The copy keeps the authority unmodifiable.
func PlanItemTerminalStatuses() []string {
	return append([]string(nil), planItemTerminalStatuses...)
}

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
