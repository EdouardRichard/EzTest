package versionapp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// PlanPatch is the partial-write input of PlanService.Update (id/title/status/
// decision/order, spec R-A8-2). It aliases the domain type so the write entry
// points and the store share one shape.
type PlanPatch = version.PlanPatch

// ConflictError is the service-level stale-revision error of the plan write
// path. It unwraps to version.ErrPlanRevisionConflict (errors.Is) and carries
// the expected and stored revisions (errors.As). The HTTP plan handler maps it
// to 409 Conflict.
type ConflictError struct {
	VersionID int64
	ItemID    int64
	Expected  int
	Actual    int
}

func (e *ConflictError) Error() string {
	if e.Actual <= 0 {
		return fmt.Sprintf("plan item %d revision conflict: expected %d", e.ItemID, e.Expected)
	}
	return fmt.Sprintf("plan item %d revision conflict: expected %d, stored %d",
		e.ItemID, e.Expected, e.Actual)
}

// Unwrap exposes version.ErrPlanRevisionConflict to errors.Is.
func (e *ConflictError) Unwrap() error { return version.ErrPlanRevisionConflict }

// PlanService reads and writes the version task plan (spec §6.1, task_plan.md).
// It owns plan-item IDs, keeps the listing dependency-ordered and publishes
// plan.item_changed only when a write actually changes the stored item.
type PlanService struct {
	repo   version.PlanRepository
	ids    platformid.Generator
	events eventingapp.Publisher
}

// NewPlanService wires the plan store. ids assigns version_plan_item primary
// keys; events may be nil in degraded assemblies (event delivery is best
// effort and never rolls back a plan write).
func NewPlanService(repo version.PlanRepository, ids platformid.Generator, events eventingapp.Publisher) *PlanService {
	return &PlanService{repo: repo, ids: ids, events: events}
}

// List returns one version's plan items, optionally filtered by status (empty =
// all statuses). Within the returned set, an item is always ordered after the
// plan items it depends on, even when its sort_order is smaller; remaining
// items keep (order, id) order, so the result is deterministic for the same
// input.
func (s *PlanService) List(ctx context.Context, versionID int64, status string) ([]version.PlanItem, error) {
	if versionID <= 0 {
		return nil, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	items, err := s.repo.List(ctx, versionID, strings.TrimSpace(status))
	if err != nil {
		return nil, mapPlanRepositoryError(err)
	}
	return orderPlanItems(items), nil
}

// Upsert inserts a new plan item (ID == 0) or updates the row with the same
// ID. It returns the stored item. plan.item_changed is published (on
// version:{id}) only when the item was created or actually changed, with the
// contract's summary payload.
func (s *PlanService) Upsert(ctx context.Context, item version.PlanItem) (version.PlanItem, error) {
	item.Normalize()
	if item.ID == 0 {
		item.ID = s.ids.Next()
	}
	if err := item.Validate(); err != nil {
		return version.PlanItem{}, err
	}

	stored, changed, err := s.repo.Upsert(ctx, item)
	if err != nil {
		return version.PlanItem{}, mapPlanRepositoryError(err)
	}
	if changed {
		s.publishItemChanged(ctx, stored)
	}
	return stored, nil
}

// Update applies a partial write to one plan item with revision optimistic
// locking (spec R-A8-2, DB-20; B §9.1). versionID/itemID must be positive and
// revision is the value the caller last read. Empty Title/Status/Decision keep
// the stored value; a nil patch order keeps the stored order; a negative order
// is rejected. A stale revision returns *ConflictError wrapping
// version.ErrPlanRevisionConflict; a missing item (or an item of another
// version) returns platform.NotFound. plan.item_changed is published only when
// a field actually changed.
func (s *PlanService) Update(ctx context.Context, versionID, itemID int64, patch PlanPatch, revision int) (version.PlanItem, error) {
	if versionID <= 0 {
		return version.PlanItem{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if itemID <= 0 {
		return version.PlanItem{}, platform.Invalid("item_id", "must be a positive decimal ID")
	}
	if revision <= 0 {
		return version.PlanItem{}, platform.Invalid("revision", "must be a positive integer")
	}
	if patch.Order != nil && *patch.Order < 0 {
		return version.PlanItem{}, platform.Invalid("order", "must not be negative")
	}

	stored, changed, err := s.repo.Update(ctx, versionID, itemID, patch, revision)
	if err != nil {
		return version.PlanItem{}, mapPlanUpdateError(err, versionID, itemID, revision)
	}
	if changed {
		s.publishItemChanged(ctx, stored)
	}
	return stored, nil
}

// publishItemChanged emits the plan.item_changed contract event. Delivery is
// best effort: the version_plan_item row is the source of truth. The payload is
// a summary only (no prompts or task bodies); revision lets an optimistic
// consumer patch its local copy without refetching.
func (s *PlanService) publishItemChanged(ctx context.Context, item version.PlanItem) {
	if s.events == nil {
		return
	}
	versionID := platformid.Format(item.VersionID)
	payload := map[string]any{
		"plan_item_id": platformid.Format(item.ID),
		"phase":        item.Phase,
		"status":       item.Status,
		"order":        item.Order,
		"revision":     item.Revision,
	}
	if item.WorkItemID != nil {
		payload["work_item_id"] = platformid.Format(*item.WorkItemID)
	}
	if item.Decision != "" {
		payload["decision"] = item.Decision
	}
	_ = s.events.Publish(ctx, eventingapp.Event{
		Channel:   "version:" + versionID,
		Type:      "plan.item_changed",
		Scope:     map[string]any{"version_id": versionID},
		Payload:   payload,
		RequestID: requestid.From(ctx),
	})
}

// orderPlanItems applies a stable dependency-first topological order. The
// available set is ordered by (sort_order, id) at every step, so independent
// items keep their declared order. A cycle (only possible through data
// corruption, because the service rejects self-dependencies) is appended in
// (order, id) order instead of being dropped.
func orderPlanItems(items []version.PlanItem) []version.PlanItem {
	if len(items) < 2 {
		return items
	}
	ordered := append([]version.PlanItem(nil), items...)
	byID := make(map[int64]version.PlanItem, len(ordered))
	indegree := make(map[int64]int, len(ordered))
	dependants := make(map[int64][]int64, len(ordered))
	for _, item := range ordered {
		byID[item.ID] = item
		if _, ok := indegree[item.ID]; !ok {
			indegree[item.ID] = 0
		}
	}
	for _, item := range ordered {
		for _, dependencyID := range item.DependsOn {
			if dependencyID == item.ID {
				continue
			}
			if _, listed := byID[dependencyID]; !listed {
				// The dependency was filtered out of this listing; it cannot be
				// ordered here, so it imposes no constraint on the result.
				continue
			}
			indegree[item.ID]++
			dependants[dependencyID] = append(dependants[dependencyID], item.ID)
		}
	}

	available := make([]version.PlanItem, 0, len(ordered))
	for _, item := range ordered {
		if indegree[item.ID] == 0 {
			available = append(available, item)
		}
	}
	sortPlanItems(available)

	result := make([]version.PlanItem, 0, len(ordered))
	for len(available) > 0 {
		next := available[0]
		available = available[1:]
		result = append(result, next)
		for _, dependantID := range dependants[next.ID] {
			indegree[dependantID]--
			if indegree[dependantID] == 0 {
				available = append(available, byID[dependantID])
				sortPlanItems(available)
			}
		}
	}

	if len(result) == len(ordered) {
		return result
	}
	// Cycle fallback: keep every row, deterministically ordered.
	emitted := make(map[int64]struct{}, len(result))
	for _, item := range result {
		emitted[item.ID] = struct{}{}
	}
	leftover := make([]version.PlanItem, 0, len(ordered)-len(result))
	for _, item := range ordered {
		if _, ok := emitted[item.ID]; !ok {
			leftover = append(leftover, item)
		}
	}
	sortPlanItems(leftover)
	return append(result, leftover...)
}

func sortPlanItems(items []version.PlanItem) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Order != items[j].Order {
			return items[i].Order < items[j].Order
		}
		return items[i].ID < items[j].ID
	})
}

// mapPlanUpdateError translates the store's optimistic-lock errors into the
// service contract: stale revisions become *ConflictError, everything else
// keeps the shared plan repository mapping (not found, dependency, version).
func mapPlanUpdateError(err error, versionID, itemID int64, expected int) error {
	var revisionConflict *version.PlanRevisionConflictError
	if errors.As(err, &revisionConflict) {
		return &ConflictError{
			VersionID: versionID,
			ItemID:    itemID,
			Expected:  expected,
			Actual:    revisionConflict.Actual,
		}
	}
	if errors.Is(err, version.ErrPlanRevisionConflict) {
		// A store may only expose the sentinel; keep errors.Is usable and leave
		// the stored revision unknown.
		return &ConflictError{VersionID: versionID, ItemID: itemID, Expected: expected}
	}
	return mapPlanRepositoryError(err)
}

func mapPlanRepositoryError(err error) error {
	switch {
	case errors.Is(err, version.ErrPlanNotFound):
		return platform.NotFound("plan item")
	case errors.Is(err, version.ErrPlanDependencyNotFound):
		return platform.Invalid("depends_on", "must reference existing plan items of the same version")
	case errors.Is(err, version.ErrNotFound):
		return platform.NotFound("version")
	default:
		return err
	}
}
