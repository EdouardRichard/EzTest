package versionapp_test

import (
	"context"
	"testing"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// fakeVersionReadSource is the version-scoped read source double of the
// VersionReadService projection tests. Only ListPlanItems returns data; the
// other methods exist to satisfy the port.
type fakeVersionReadSource struct {
	planRows    []versionapp.PlanItemReadRow
	planHasMore bool
	planErr     error
	planQueries []versionapp.PlanItemReadQuery
}

func (f *fakeVersionReadSource) VersionExists(context.Context, int64) (bool, error) {
	return true, nil
}

func (f *fakeVersionReadSource) ListFacts(context.Context, versionapp.FactReadQuery) ([]versionapp.FactReadRow, bool, error) {
	return nil, false, nil
}

func (f *fakeVersionReadSource) ListPlanItems(_ context.Context, query versionapp.PlanItemReadQuery) ([]versionapp.PlanItemReadRow, bool, error) {
	f.planQueries = append(f.planQueries, query)
	return f.planRows, f.planHasMore, f.planErr
}

var _ versionapp.VersionReadReader = (*fakeVersionReadSource)(nil)

// TestVersionReadPlanItemsProjectsRevision pins the read-view mapping of the
// plan optimistic-lock token: the source row revision must reach the frozen
// PlanItemView, otherwise the Host/API consumer has to guess it and eats a
// revision conflict on the next plan.update (Task 9 review H4).
func TestVersionReadPlanItemsProjectsRevision(t *testing.T) {
	workItemID := int64(301)
	source := &fakeVersionReadSource{planRows: []versionapp.PlanItemReadRow{{
		ID:         201,
		VersionID:  7,
		Phase:      "execute",
		Title:      "Run batch",
		Status:     "pending",
		DependsOn:  []int64{199},
		WorkItemID: &workItemID,
		Decision:   "keep",
		Order:      3,
		Revision:   6,
	}}}
	service := versionapp.NewVersionReadService(source)

	views, hasMore, err := service.PlanItems(context.Background(), 7, versionapp.PlanItemReadFilter{
		Status: "pending",
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("PlanItems: %v", err)
	}
	if hasMore {
		t.Fatal("has_more = true, want false")
	}
	if len(views) != 1 {
		t.Fatalf("views = %d, want 1", len(views))
	}
	view := views[0]
	if view.Revision != 6 {
		t.Fatalf("view revision = %d, want the stored revision 6", view.Revision)
	}
	if view.ID != 201 || view.VersionID != 7 || view.Phase != "execute" ||
		view.Title != "Run batch" || view.Status != "pending" || view.Order != 3 {
		t.Fatalf("view projection = %#v", view)
	}
	if len(source.planQueries) != 1 || source.planQueries[0].VersionID != 7 ||
		source.planQueries[0].Status != "pending" || source.planQueries[0].Limit != 10 {
		t.Fatalf("plan queries = %#v", source.planQueries)
	}
}
