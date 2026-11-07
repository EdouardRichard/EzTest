package versionapp_test

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
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
	if ok {
		item.Revision = existing.Revision + 1
	} else {
		item.Revision = 1
	}
	f.items[item.ID] = item
	f.writes++
	return item, true, nil
}

// Update mirrors the SQL adapter's optimistic-lock contract: a missing item (or
// one owned by another version) is ErrPlanNotFound, a stale revision is a
// *version.PlanRevisionConflictError wrapping ErrPlanRevisionConflict, and a
// no-op patch neither bumps the revision nor reports a change.
func (f *fakePlanRepo) Update(_ context.Context, versionID, itemID int64, patch version.PlanPatch, expectedRevision int) (version.PlanItem, bool, error) {
	existing, ok := f.items[itemID]
	if !ok || existing.VersionID != versionID {
		return version.PlanItem{}, false, version.ErrPlanNotFound
	}
	if existing.Revision != expectedRevision {
		return version.PlanItem{}, false, &version.PlanRevisionConflictError{
			ItemID: itemID, Expected: expectedRevision, Actual: existing.Revision,
		}
	}
	updated := patch.Apply(existing)
	if samePlanItem(existing, updated) {
		return existing, false, nil
	}
	updated.Revision = existing.Revision + 1
	f.items[itemID] = updated
	f.writes++
	return updated, true, nil
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

// newPlanServiceTest builds a PlanService over an in-memory store seeded with
// one plan item (id 9, version 7, revision 1) for the optimistic-lock tests.
func newPlanServiceTest(t *testing.T) (*versionapp.PlanService, *fakePlanRepo) {
	t.Helper()
	service, repo, _ := newPlanServiceFixture(t)
	return service, repo
}

func newPlanServiceFixture(t *testing.T) (*versionapp.PlanService, *fakePlanRepo, *fakePublisher) {
	t.Helper()
	repo := newFakePlanRepo()
	repo.items[9] = version.PlanItem{
		ID: 9, VersionID: 7, Phase: "execute", Title: "run cases",
		Status: version.PlanItemStatusPending, Order: 1, Revision: 1,
	}
	publisher := &fakePublisher{}
	return versionapp.NewPlanService(repo, &seqIDs{}, publisher), repo, publisher
}

func planOrderPtr(value int) *int { return &value }

// TestPlanUpdatePersistsAndBumpsRevision pins the write path of R-A8-2: a
// revision-guarded patch persists the changed status/decision, returns the
// stored revision bumped 1 -> 2, and empty patch fields keep stored values.
func TestPlanUpdatePersistsAndBumpsRevision(t *testing.T) {
	svc, repo := newPlanServiceTest(t)
	ctx := requestContext()

	got, err := svc.Update(ctx, 7, 9, versionapp.PlanPatch{Status: "done", Decision: "ok"}, 1)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Status != "done" || got.Decision != "ok" || got.Revision != 2 {
		t.Fatalf("got = %+v, want status=done decision=ok revision=2", got)
	}
	stored := repo.items[9]
	if stored.Status != "done" || stored.Decision != "ok" || stored.Revision != 2 {
		t.Fatalf("stored = %+v, want persisted status/decision/revision", stored)
	}

	// Empty strings mean "keep the stored value"; a no-op patch leaves the
	// revision untouched and reports no change.
	unchanged, err := svc.Update(ctx, 7, 9, versionapp.PlanPatch{Title: "", Decision: ""}, 2)
	if err != nil {
		t.Fatalf("no-op Update: %v", err)
	}
	if unchanged.Status != "done" || unchanged.Title != "run cases" || unchanged.Revision != 2 {
		t.Fatalf("no-op update = %+v, want stored values with revision 2", unchanged)
	}

	// A non-nil order pointer is applied; a nil order keeps the stored order.
	order := 5
	reordered, err := svc.Update(ctx, 7, 9, versionapp.PlanPatch{Order: &order}, 2)
	if err != nil {
		t.Fatalf("order Update: %v", err)
	}
	if reordered.Order != 5 || reordered.Status != "done" || reordered.Revision != 3 {
		t.Fatalf("order update = %+v, want order=5 status=done revision=3", reordered)
	}
}

// TestPlanUpdateRevisionConflict pins the optimistic-lock rejection: a write
// carrying a stale revision fails with *versionapp.ConflictError wrapping
// version.ErrPlanRevisionConflict, leaks the stored revision, and writes
// nothing.
func TestPlanUpdateRevisionConflict(t *testing.T) {
	svc, repo := newPlanServiceTest(t)
	ctx := requestContext()

	_, err := svc.Update(ctx, 7, 9, versionapp.PlanPatch{Status: "done"}, 2)
	var conflict *versionapp.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want *versionapp.ConflictError", err)
	}
	if !errors.Is(err, version.ErrPlanRevisionConflict) {
		t.Fatalf("err = %v, want errors.Is(ErrPlanRevisionConflict)", err)
	}
	if conflict.VersionID != 7 || conflict.ItemID != 9 || conflict.Expected != 2 || conflict.Actual != 1 {
		t.Fatalf("conflict = %+v, want version=7 item=9 expected=2 actual=1", conflict)
	}
	if stored := repo.items[9]; stored.Status != version.PlanItemStatusPending || stored.Revision != 1 {
		t.Fatalf("stale write mutated the store: %+v", stored)
	}
}

// TestPlanUpdateNotFound pins the not-found mapping: an unknown item ID and an
// item owned by another version are both 404 plan items, never a conflict.
func TestPlanUpdateNotFound(t *testing.T) {
	svc, _ := newPlanServiceTest(t)
	ctx := requestContext()

	for _, tc := range []struct {
		name      string
		versionID int64
		itemID    int64
	}{
		{"missing item", 7, 404},
		{"item of another version", 8, 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Update(ctx, tc.versionID, tc.itemID, versionapp.PlanPatch{Status: "done"}, 1)
			var apiErr *platform.Error
			if !errors.As(err, &apiErr) {
				t.Fatalf("err = %v, want *platform.Error", err)
			}
			if apiErr.Status != 404 || apiErr.Code != "not_found" {
				t.Fatalf("apiErr = %+v, want 404 not_found", apiErr)
			}
			if errors.Is(err, version.ErrPlanRevisionConflict) {
				t.Fatalf("err = %v, not-found must not be a revision conflict", err)
			}
		})
	}
}

// TestPlanUpdatePublishesOnlyOnChange pins R-A8-6: plan.item_changed is emitted
// exactly once for a real write, with the updated payload, and never for a
// no-op or a rejected stale write.
func TestPlanUpdatePublishesOnlyOnChange(t *testing.T) {
	svc, _, publisher := newPlanServiceFixture(t)
	ctx := requestContext()

	if _, err := svc.Update(ctx, 7, 9, versionapp.PlanPatch{}, 1); err != nil {
		t.Fatalf("no-op Update: %v", err)
	}
	if len(publisher.events) != 0 {
		t.Fatalf("no-op update published %d events, want 0", len(publisher.events))
	}

	order := 3
	updated, err := svc.Update(ctx, 7, 9, versionapp.PlanPatch{Status: "done", Order: &order}, 1)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Revision != 2 {
		t.Fatalf("revision = %d, want 2", updated.Revision)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("plan.item_changed events = %d, want 1", len(publisher.events))
	}
	event := publisher.events[0]
	if event.Type != "plan.item_changed" || event.Channel != "version:7" {
		t.Fatalf("event = %#v, want plan.item_changed on version:7", event)
	}
	if event.Payload["plan_item_id"] != "9" || event.Payload["status"] != "done" || event.Payload["order"] != 3 {
		t.Fatalf("event payload = %#v, want plan_item_id=9 status=done order=3", event.Payload)
	}

	// A stale write must not publish.
	if _, err := svc.Update(ctx, 7, 9, versionapp.PlanPatch{Status: "pending"}, 1); err == nil {
		t.Fatal("stale Update succeeded, want conflict")
	}
	if len(publisher.events) != 1 {
		t.Fatalf("stale update published events: %d, want 1", len(publisher.events))
	}
}

// TestPlanUpdateValidatesArguments pins the service-level validation of the
// patch entry point before it reaches the store.
func TestPlanUpdateValidatesArguments(t *testing.T) {
	svc, _ := newPlanServiceTest(t)
	ctx := requestContext()
	negativeOrder := -1

	cases := []struct {
		name      string
		versionID int64
		itemID    int64
		patch     versionapp.PlanPatch
		revision  int
	}{
		{"version id", 0, 9, versionapp.PlanPatch{Status: "done"}, 1},
		{"item id", 7, 0, versionapp.PlanPatch{Status: "done"}, 1},
		{"revision", 7, 9, versionapp.PlanPatch{Status: "done"}, 0},
		{"negative order", 7, 9, versionapp.PlanPatch{Order: &negativeOrder}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Update(ctx, tc.versionID, tc.itemID, tc.patch, tc.revision)
			var apiErr *platform.Error
			if !errors.As(err, &apiErr) {
				t.Fatalf("err = %v, want *platform.Error", err)
			}
			if apiErr.Status != 400 || apiErr.Code != "invalid_argument" {
				t.Fatalf("apiErr = %+v, want 400 invalid_argument", apiErr)
			}
		})
	}
}
