package versionapp_test

import (
	"context"
	"sort"
	"strconv"
	"testing"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// fakePlanRepo is an in-memory version.PlanRepository. List mirrors the SQL
// adapter's (sort_order, id) ordering; dependency ordering is the service's
// responsibility.
type fakePlanRepo struct {
	items  map[int64]version.PlanItem
	writes int
}

func newFakePlanRepo() *fakePlanRepo {
	return &fakePlanRepo{items: map[int64]version.PlanItem{}}
}

func (f *fakePlanRepo) List(_ context.Context, versionID int64, status string) ([]version.PlanItem, error) {
	items := make([]version.PlanItem, 0, len(f.items))
	for _, item := range f.items {
		if item.VersionID != versionID {
			continue
		}
		if status != "" && item.Status != status {
			continue
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Order != items[j].Order {
			return items[i].Order < items[j].Order
		}
		return items[i].ID < items[j].ID
	})
	return items, nil
}

func (f *fakePlanRepo) Upsert(_ context.Context, item version.PlanItem) (version.PlanItem, bool, error) {
	existing, ok := f.items[item.ID]
	if ok && samePlanItem(existing, item) {
		return existing, false, nil
	}
	f.items[item.ID] = item
	f.writes++
	return item, true, nil
}

func samePlanItem(a, b version.PlanItem) bool {
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

var _ version.PlanRepository = (*fakePlanRepo)(nil)

// TestPlanDependencyOrder pins deterministic planning order: an item whose
// dependency is also listed is always returned after that dependency, even when
// its sort_order is smaller, and plan writes emit the plan.item_changed contract
// event only when the item actually changes.
func TestPlanDependencyOrder(t *testing.T) {
	repo := newFakePlanRepo()
	publisher := &fakePublisher{}
	service := versionapp.NewPlanService(repo, &seqIDs{}, publisher)
	ctx := context.Background()

	independent, err := service.Upsert(ctx, version.PlanItem{
		VersionID: 9, Phase: "prepare", Title: "confirm requirements", Order: 0,
	})
	if err != nil {
		t.Fatalf("Upsert independent: %v", err)
	}
	dependency, err := service.Upsert(ctx, version.PlanItem{
		VersionID: 9, Phase: "execute", Title: "run cases", Order: 2,
	})
	if err != nil {
		t.Fatalf("Upsert dependency: %v", err)
	}
	dependent, err := service.Upsert(ctx, version.PlanItem{
		VersionID: 9, Phase: "execute", Title: "fix failures", Order: 1,
		DependsOn: []int64{dependency.ID},
	})
	if err != nil {
		t.Fatalf("Upsert dependent: %v", err)
	}
	if dependent.ID == dependency.ID || dependent.ID == independent.ID {
		t.Fatalf("plan item IDs are not distinct: %#v", []version.PlanItem{independent, dependency, dependent})
	}

	items, err := service.List(ctx, 9, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []int64{independent.ID, dependency.ID, dependent.ID}
	if len(items) != len(want) {
		t.Fatalf("plan items = %#v, want %d items", items, len(want))
	}
	for i, id := range want {
		if items[i].ID != id {
			t.Fatalf("plan order = %#v, want IDs %v (dependency first)", items, want)
		}
	}

	// Each create is a real change and publishes plan.item_changed.
	if len(publisher.events) != 3 {
		t.Fatalf("plan.item_changed events after creates = %d, want 3", len(publisher.events))
	}

	// A status change is a real change: emit the contract event with the new
	// status and the required payload fields.
	dependent.Status = "done"
	updated, err := service.Upsert(ctx, dependent)
	if err != nil {
		t.Fatalf("Upsert status change: %v", err)
	}
	if updated.Status != "done" {
		t.Fatalf("updated status = %q, want done", updated.Status)
	}
	if len(publisher.events) != 4 {
		t.Fatalf("plan.item_changed events after status change = %d, want 4", len(publisher.events))
	}
	changed := publisher.events[3]
	if changed.Type != "plan.item_changed" || changed.Channel != "version:9" {
		t.Fatalf("event = %#v, want plan.item_changed on version:9", changed)
	}
	if changed.Payload["plan_item_id"] != strconv.FormatInt(dependent.ID, 10) {
		t.Fatalf("event plan_item_id = %#v, want %d", changed.Payload["plan_item_id"], dependent.ID)
	}
	if changed.Payload["phase"] != "execute" || changed.Payload["status"] != "done" || changed.Payload["order"] != 1 {
		t.Fatalf("event payload = %#v, want phase=execute status=done order=1", changed.Payload)
	}

	// Re-writing the same plan item is not a change and publishes nothing.
	if _, err := service.Upsert(ctx, updated); err != nil {
		t.Fatalf("Upsert unchanged: %v", err)
	}
	if len(publisher.events) != 4 {
		t.Fatalf("plan.item_changed events after no-op upsert = %d, want 4", len(publisher.events))
	}

	// The optional status filter is passed through to the repository.
	done, err := service.List(ctx, 9, "done")
	if err != nil {
		t.Fatalf("List done: %v", err)
	}
	if len(done) != 1 || done[0].ID != dependent.ID {
		t.Fatalf("done plan items = %#v, want only %d", done, dependent.ID)
	}
}
