package workspaceapp_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	workspaceapp "github.com/EdouardRichard/EzTest/backend/internal/workspace/application"
)

// fakeProvider is one todo source owned by a context. It records the viewer it
// was called with so the tests can assert the admin gate and provider fan-out.
type fakeProvider struct {
	items  []workspaceapp.TodoItem
	err    error
	calls  int
	viewer platform.Actor
}

func (f *fakeProvider) Items(_ context.Context, viewer platform.Actor) ([]workspaceapp.TodoItem, error) {
	f.calls++
	f.viewer = viewer
	return f.items, f.err
}

func item(kind, title string, createdAt time.Time) workspaceapp.TodoItem {
	return workspaceapp.TodoItem{
		Kind:         kind,
		Title:        title,
		VersionID:    2001,
		VersionTitle: "订单中心",
		Severity:     "warning",
		CreatedAt:    createdAt,
		URL:          "/versions/2001?tab=issues",
	}
}

func kinds(items []workspaceapp.TodoItem) []string {
	result := make([]string, 0, len(items))
	for _, it := range items {
		result = append(result, it.Kind)
	}
	return result
}

func assertKinds(t *testing.T, got []workspaceapp.TodoItem, want []string) {
	t.Helper()
	gotKinds := kinds(got)
	if len(gotKinds) != len(want) {
		t.Fatalf("kinds = %v, want %v", gotKinds, want)
	}
	for index := range want {
		if gotKinds[index] != want[index] {
			t.Fatalf("kinds = %v, want %v", gotKinds, want)
		}
	}
}

func TestSnapshotAggregatesAndOrdersSixKinds(t *testing.T) {
	now := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	versionProvider := &fakeProvider{items: []workspaceapp.TodoItem{
		item(workspaceapp.KindDesignPending, "2 条待设计", now),
		item(workspaceapp.KindRequirementConfirmation, "需求 A", now.Add(-time.Hour)),
	}}
	designProvider := &fakeProvider{items: []workspaceapp.TodoItem{
		item(workspaceapp.KindCaseConfirmation, "用例 A", now),
	}}
	executionProvider := &fakeProvider{items: []workspaceapp.TodoItem{
		item(workspaceapp.KindDeadWorkItem, "死任务 A", now),
		item(workspaceapp.KindHumanRequest, "人工请求 A", now),
	}}
	deliveryProvider := &fakeProvider{items: []workspaceapp.TodoItem{
		item(workspaceapp.KindIssueConfirmation, "问题 A", now),
	}}

	service := workspaceapp.NewService(versionProvider, designProvider, executionProvider, deliveryProvider)
	snapshot, err := service.Snapshot(context.Background(), platform.Actor{ID: 1, Role: "admin"}, 50)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	assertKinds(t, snapshot.Items, []string{
		workspaceapp.KindHumanRequest,
		workspaceapp.KindDesignPending,
		workspaceapp.KindIssueConfirmation,
		workspaceapp.KindRequirementConfirmation,
		workspaceapp.KindCaseConfirmation,
		workspaceapp.KindDeadWorkItem,
	})
	if snapshot.Counts.HumanRequest != 1 || snapshot.Counts.DesignPending != 1 ||
		snapshot.Counts.IssueConfirmation != 1 || snapshot.Counts.RequirementConfirmation != 1 ||
		snapshot.Counts.CaseConfirmation != 1 || snapshot.Counts.DeadWorkItem != 1 {
		t.Fatalf("counts = %+v, want one of each kind", snapshot.Counts)
	}
	for _, provider := range []*fakeProvider{versionProvider, designProvider, executionProvider, deliveryProvider} {
		if provider.calls != 1 {
			t.Fatalf("provider calls = %d, want 1", provider.calls)
		}
	}
	if executionProvider.viewer.IsAdmin() != true {
		t.Fatalf("execution provider viewer = %+v, want the admin actor", executionProvider.viewer)
	}
}

func TestSnapshotCountsAreComputedBeforeLimit(t *testing.T) {
	now := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	provider := &fakeProvider{items: []workspaceapp.TodoItem{
		item(workspaceapp.KindRequirementConfirmation, "需求 1", now),
		item(workspaceapp.KindRequirementConfirmation, "需求 2", now.Add(-time.Minute)),
		item(workspaceapp.KindRequirementConfirmation, "需求 3", now.Add(-2*time.Minute)),
	}}

	snapshot, err := workspaceapp.NewService(provider).Snapshot(
		context.Background(), platform.Actor{ID: 1, Role: "admin"}, 2)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snapshot.Items) != 2 {
		t.Fatalf("items = %d, want 2 after the limit", len(snapshot.Items))
	}
	if snapshot.Counts.RequirementConfirmation != 3 {
		t.Fatalf("requirement count = %d, want the pre-limit 3", snapshot.Counts.RequirementConfirmation)
	}
}

func TestSnapshotSortsWithinKindByCreatedAtDesc(t *testing.T) {
	now := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	older := item(workspaceapp.KindIssueConfirmation, "旧问题", now.Add(-time.Hour))
	newer := item(workspaceapp.KindIssueConfirmation, "新问题", now)
	tied := item(workspaceapp.KindIssueConfirmation, "同时间问题", now)

	provider := &fakeProvider{items: []workspaceapp.TodoItem{older, newer, tied}}
	snapshot, err := workspaceapp.NewService(provider).Snapshot(
		context.Background(), platform.Actor{ID: 1, Role: "admin"}, 50)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	assertKinds(t, snapshot.Items, []string{
		workspaceapp.KindIssueConfirmation,
		workspaceapp.KindIssueConfirmation,
		workspaceapp.KindIssueConfirmation,
	})
	if snapshot.Items[0].Title != "新问题" || snapshot.Items[1].Title != "同时间问题" || snapshot.Items[2].Title != "旧问题" {
		t.Fatalf("titles = %q, %q, %q; want newest first and stable ties",
			snapshot.Items[0].Title, snapshot.Items[1].Title, snapshot.Items[2].Title)
	}
}

func TestSnapshotDropsAdminOnlyDeadWorkItemsForRegularUsers(t *testing.T) {
	now := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	provider := &fakeProvider{items: []workspaceapp.TodoItem{
		item(workspaceapp.KindHumanRequest, "冲突请求", now),
		item(workspaceapp.KindDeadWorkItem, "死任务", now),
	}}

	snapshot, err := workspaceapp.NewService(provider).Snapshot(
		context.Background(), platform.Actor{ID: 9001, Role: "user"}, 50)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	assertKinds(t, snapshot.Items, []string{workspaceapp.KindHumanRequest})
	if snapshot.Counts.DeadWorkItem != 0 {
		t.Fatalf("dead count = %d, want 0 for a regular user", snapshot.Counts.DeadWorkItem)
	}
}

func TestSnapshotKeepsAdminOnlyDeadWorkItemsForAdmins(t *testing.T) {
	now := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	provider := &fakeProvider{items: []workspaceapp.TodoItem{
		item(workspaceapp.KindDeadWorkItem, "死任务", now),
	}}

	snapshot, err := workspaceapp.NewService(provider).Snapshot(
		context.Background(), platform.Actor{ID: 1, Role: "admin"}, 50)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	assertKinds(t, snapshot.Items, []string{workspaceapp.KindDeadWorkItem})
	if snapshot.Counts.DeadWorkItem != 1 {
		t.Fatalf("dead count = %d, want 1", snapshot.Counts.DeadWorkItem)
	}
}

func TestSnapshotEmptyState(t *testing.T) {
	snapshot, err := workspaceapp.NewService(&fakeProvider{}).Snapshot(
		context.Background(), platform.Actor{ID: 1, Role: "admin"}, 50)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.Items == nil {
		t.Fatal("items must be an empty non-nil slice")
	}
	if len(snapshot.Items) != 0 {
		t.Fatalf("items = %d, want 0", len(snapshot.Items))
	}
	if snapshot.Counts != (workspaceapp.Counts{}) {
		t.Fatalf("counts = %+v, want all zero", snapshot.Counts)
	}
}

func TestSnapshotPropagatesProviderErrors(t *testing.T) {
	sentinel := errors.New("query reader unavailable")
	provider := &fakeProvider{err: sentinel}

	_, err := workspaceapp.NewService(provider).Snapshot(
		context.Background(), platform.Actor{ID: 1, Role: "admin"}, 50)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Snapshot error = %v, want %v", err, sentinel)
	}
}

func TestSnapshotNormalizesLimit(t *testing.T) {
	now := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	makeItems := func(count int) []workspaceapp.TodoItem {
		items := make([]workspaceapp.TodoItem, 0, count)
		for index := 0; index < count; index++ {
			items = append(items, item(workspaceapp.KindCaseConfirmation, "用例", now.Add(-time.Duration(index)*time.Second)))
		}
		return items
	}

	t.Run("zero falls back to the default 50", func(t *testing.T) {
		snapshot, err := workspaceapp.NewService(&fakeProvider{items: makeItems(51)}).Snapshot(
			context.Background(), platform.Actor{ID: 1, Role: "admin"}, 0)
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if len(snapshot.Items) != workspaceapp.DefaultLimit {
			t.Fatalf("items = %d, want default %d", len(snapshot.Items), workspaceapp.DefaultLimit)
		}
		if snapshot.Counts.CaseConfirmation != 51 {
			t.Fatalf("count = %d, want the pre-limit 51", snapshot.Counts.CaseConfirmation)
		}
	})

	t.Run("above the maximum is capped at 200", func(t *testing.T) {
		snapshot, err := workspaceapp.NewService(&fakeProvider{items: makeItems(201)}).Snapshot(
			context.Background(), platform.Actor{ID: 1, Role: "admin"}, 201)
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if len(snapshot.Items) != workspaceapp.MaxLimit {
			t.Fatalf("items = %d, want max %d", len(snapshot.Items), workspaceapp.MaxLimit)
		}
		if snapshot.Counts.CaseConfirmation != 201 {
			t.Fatalf("count = %d, want 201", snapshot.Counts.CaseConfirmation)
		}
	})
}

func TestSnapshotDefaultsTo50ForNegativeLimit(t *testing.T) {
	items := make([]workspaceapp.TodoItem, 0, 51)
	now := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	for index := 0; index < 51; index++ {
		items = append(items, item(workspaceapp.KindCaseConfirmation, "用例", now))
	}
	snapshot, err := workspaceapp.NewService(&fakeProvider{items: items}).Snapshot(
		context.Background(), platform.Actor{ID: 1, Role: "admin"}, -1)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snapshot.Items) != workspaceapp.DefaultLimit {
		t.Fatalf("items = %d, want default %d", len(snapshot.Items), workspaceapp.DefaultLimit)
	}
}
