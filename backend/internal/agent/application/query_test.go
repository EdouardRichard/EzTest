package agentapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func agentP6Int64Ptr(value int64) *int64 { return &value }

// fakeP6RetryReader is the agent-context P6 retry read source used by the query
// port tests.
type fakeP6RetryReader struct {
	num   int
	denom int
	err   error
	calls int
	scope *int64

	deadRows  []agentapp.DeadWorkItemRow
	deadErr   error
	deadLimit int
	deadCalls int

	workItemRows  []agent.WorkItem
	workItemsMore bool
	workItemsErr  error
	workItemCalls int
	workItemQuery agentapp.WorkItemQuery

	getItem       agent.WorkItem
	getErr        error
	getCalls      int
	getVersionID  int64
	getWorkItemID int64
}

func (f *fakeP6RetryReader) CountTaskRetries(_ context.Context, versionID *int64) (int, int, error) {
	f.calls++
	f.scope = versionID
	return f.num, f.denom, f.err
}

func (f *fakeP6RetryReader) ListDeadWorkItems(_ context.Context, limit int) ([]agentapp.DeadWorkItemRow, error) {
	f.deadCalls++
	f.deadLimit = limit
	return f.deadRows, f.deadErr
}

func (f *fakeP6RetryReader) ListWorkItems(_ context.Context, query agentapp.WorkItemQuery) ([]agent.WorkItem, bool, error) {
	f.workItemCalls++
	f.workItemQuery = query
	return f.workItemRows, f.workItemsMore, f.workItemsErr
}

func (f *fakeP6RetryReader) GetWorkItem(_ context.Context, versionID, id int64) (agent.WorkItem, error) {
	f.getCalls++
	f.getVersionID, f.getWorkItemID = versionID, id
	return f.getItem, f.getErr
}

func TestP6ListDeadWorkItems(t *testing.T) {
	created := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	reader := &fakeP6RetryReader{deadRows: []agentapp.DeadWorkItemRow{
		{ID: 11, VersionID: 7, Title: "execute.case 重试耗尽", CreatedAt: created},
	}}
	service := agentapp.NewQueryService(reader)

	rows, err := service.ListDeadWorkItems(context.Background(), 999)
	if err != nil {
		t.Fatalf("ListDeadWorkItems: %v", err)
	}
	if reader.deadLimit != 200 {
		t.Fatalf("limit = %d, want clamp 200", reader.deadLimit)
	}
	if len(rows) != 1 || rows[0].ID != 11 || rows[0].VersionID != 7 {
		t.Fatalf("rows = %#v", rows)
	}
	if rows[0].Title != "execute.case 重试耗尽" || !rows[0].CreatedAt.Equal(created) {
		t.Fatalf("row = %#v", rows[0])
	}

	if _, err := service.ListDeadWorkItems(context.Background(), 0); err != nil {
		t.Fatalf("ListDeadWorkItems default: %v", err)
	}
	if reader.deadLimit != 50 {
		t.Fatalf("default limit = %d, want 50", reader.deadLimit)
	}

	want := errors.New("boom")
	failing := agentapp.NewQueryService(&fakeP6RetryReader{deadErr: want})
	if _, err := failing.ListDeadWorkItems(context.Background(), 10); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestP6CountTaskRetries(t *testing.T) {
	t.Run("empty store returns zero counts", func(t *testing.T) {
		reader := &fakeP6RetryReader{}
		service := agentapp.NewQueryService(reader)

		num, denom, err := service.CountTaskRetries(context.Background(), nil)
		if err != nil {
			t.Fatalf("CountTaskRetries: %v", err)
		}
		if num != 0 || denom != 0 {
			t.Fatalf("counts = %d/%d, want 0/0", num, denom)
		}
		if reader.calls != 1 || reader.scope != nil {
			t.Fatalf("reader calls = %d scope = %v, want one global call", reader.calls, reader.scope)
		}
	})

	t.Run("version filter is forwarded", func(t *testing.T) {
		reader := &fakeP6RetryReader{num: 2, denom: 9}
		service := agentapp.NewQueryService(reader)

		num, denom, err := service.CountTaskRetries(context.Background(), agentP6Int64Ptr(7))
		if err != nil {
			t.Fatalf("CountTaskRetries: %v", err)
		}
		if num != 2 || denom != 9 {
			t.Fatalf("counts = %d/%d, want 2/9", num, denom)
		}
		if reader.scope == nil || *reader.scope != 7 {
			t.Fatalf("scope = %v, want 7", reader.scope)
		}
	})

	t.Run("zero denominator is reported without error", func(t *testing.T) {
		service := agentapp.NewQueryService(&fakeP6RetryReader{})

		num, denom, err := service.CountTaskRetries(context.Background(), nil)
		if err != nil {
			t.Fatalf("CountTaskRetries: %v", err)
		}
		if num != 0 || denom != 0 {
			t.Fatalf("counts = %d/%d, want 0/0", num, denom)
		}
	})

	t.Run("non-positive version is rejected", func(t *testing.T) {
		reader := &fakeP6RetryReader{}
		service := agentapp.NewQueryService(reader)

		if _, _, err := service.CountTaskRetries(context.Background(), agentP6Int64Ptr(0)); err == nil {
			t.Fatal("expected invalid_argument for version_id=0")
		}
		if reader.calls != 0 {
			t.Fatalf("reader calls = %d, want 0", reader.calls)
		}
	})

	t.Run("reader error is propagated", func(t *testing.T) {
		want := errors.New("boom")
		service := agentapp.NewQueryService(&fakeP6RetryReader{err: want})

		if _, _, err := service.CountTaskRetries(context.Background(), nil); !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
	})

	t.Run("nil reader is unavailable", func(t *testing.T) {
		service := agentapp.NewQueryService(nil)
		_, _, err := service.CountTaskRetries(context.Background(), nil)
		if err == nil {
			t.Fatal("expected unavailable error without a reader")
		}
		if platform.AsError(err).Status != 503 {
			t.Fatalf("status = %d, want 503", platform.AsError(err).Status)
		}
	})
}

func sampleWorkItems() []agent.WorkItem {
	cancelledReason := agentapp.JobCanceledReason
	worker := "worker-1"
	expires := time.Date(2026, 10, 31, 12, 5, 0, 0, time.UTC)
	return []agent.WorkItem{
		{
			ID:         401,
			VersionID:  7,
			Kind:       agent.WorkItemKindExecuteBatch,
			Goal:       "执行批次",
			Status:     agent.WorkItemRunning,
			Target:     json.RawMessage(`{"case_ids":["11"]}`),
			Inputs:     json.RawMessage(`{}`),
			Expected:   json.RawMessage(`{}`),
			Budget:     json.RawMessage(`{"max_tokens":1000}`),
			DependsOn:  []int64{399},
			CreatedBy:  agent.WorkItemCreatedByStateMachine,
			ClaimedBy:  &worker,
			LeaseUntil: &expires,
			Attempts:   2,
			CreatedAt:  time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
			UpdatedAt:  time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
		},
		{
			ID:        402,
			VersionID: 7,
			Kind:      agent.WorkItemKindDesignCases,
			Goal:      "设计用例",
			Status:    agent.WorkItemFailed,
			CreatedBy: agent.WorkItemCreatedByPlanner,
			LastError: &cancelledReason,
			CreatedAt: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
		},
	}
}

func TestVersionWorkItemsRead(t *testing.T) {
	t.Run("filters are normalized and views projected", func(t *testing.T) {
		reader := &fakeP6RetryReader{workItemRows: sampleWorkItems(), workItemsMore: true}
		service := agentapp.NewQueryService(reader)
		before := int64(900)

		views, hasMore, err := service.WorkItems(context.Background(), agentapp.WorkItemFilter{
			VersionID: 7,
			Kind:      " execute.batch ",
			State:     " cancelled ",
			CreatedBy: " state_machine ",
			BeforeID:  &before,
			Limit:     999,
		})
		if err != nil {
			t.Fatalf("WorkItems: %v", err)
		}
		if reader.workItemCalls != 1 {
			t.Fatalf("reader calls = %d, want 1", reader.workItemCalls)
		}
		query := reader.workItemQuery
		if query.VersionID != 7 || query.Kind != "execute.batch" || query.State != "cancelled" ||
			query.CreatedBy != "state_machine" || query.Limit != 100 ||
			query.BeforeID == nil || *query.BeforeID != 900 {
			t.Fatalf("query = %#v", query)
		}
		if !hasMore {
			t.Fatal("has_more = false, want true")
		}
		if len(views) != 2 {
			t.Fatalf("views = %d, want 2", len(views))
		}

		running := views[0]
		if running.ID != 401 || running.VersionID != 7 || running.Kind != agent.WorkItemKindExecuteBatch ||
			running.State != string(agent.WorkItemRunning) || running.Attempt != 2 ||
			running.CreatedBy != agent.WorkItemCreatedByStateMachine {
			t.Fatalf("running view = %#v", running)
		}
		if string(running.Target) != `{"case_ids":["11"]}` || string(running.Budget) != `{"max_tokens":1000}` {
			t.Fatalf("running objects = %s / %s", running.Target, running.Budget)
		}
		if len(running.DependsOn) != 1 || running.DependsOn[0] != 399 {
			t.Fatalf("running depends_on = %#v", running.DependsOn)
		}
		if running.Lease == nil || running.Lease.Owner != "worker-1" ||
			running.Lease.ExpiresAt == nil || !running.Lease.ExpiresAt.Equal(time.Date(2026, 10, 31, 12, 5, 0, 0, time.UTC)) {
			t.Fatalf("running lease = %#v", running.Lease)
		}

		// A user-canceled row is stored as failed+marker and projected as
		// cancelled, matching the job control API.
		cancelled := views[1]
		if cancelled.State != agentapp.JobStateCancelled {
			t.Fatalf("cancelled state = %q, want %q", cancelled.State, agentapp.JobStateCancelled)
		}
		if cancelled.Lease != nil || cancelled.DependsOn != nil {
			t.Fatalf("cancelled lease/deps = %#v / %#v, want nil", cancelled.Lease, cancelled.DependsOn)
		}
		for _, object := range [][]byte{cancelled.Target, cancelled.Inputs, cancelled.Expected, cancelled.Budget, cancelled.Result} {
			if string(object) != `{}` {
				t.Fatalf("cancelled JSON object = %q, want {}", object)
			}
		}
	})

	t.Run("default limit is 20", func(t *testing.T) {
		reader := &fakeP6RetryReader{}
		service := agentapp.NewQueryService(reader)

		if _, _, err := service.WorkItems(context.Background(), agentapp.WorkItemFilter{VersionID: 7}); err != nil {
			t.Fatalf("WorkItems: %v", err)
		}
		if reader.workItemQuery.Limit != 20 {
			t.Fatalf("limit = %d, want 20", reader.workItemQuery.Limit)
		}
	})

	t.Run("empty store returns an empty list", func(t *testing.T) {
		service := agentapp.NewQueryService(&fakeP6RetryReader{})
		views, hasMore, err := service.WorkItems(context.Background(), agentapp.WorkItemFilter{VersionID: 7})
		if err != nil {
			t.Fatalf("WorkItems: %v", err)
		}
		if len(views) != 0 || hasMore {
			t.Fatalf("views = %#v hasMore = %v", views, hasMore)
		}
	})

	t.Run("invalid filters are rejected before the read", func(t *testing.T) {
		cases := []struct {
			name   string
			filter agentapp.WorkItemFilter
		}{
			{"version", agentapp.WorkItemFilter{}},
			{"kind", agentapp.WorkItemFilter{VersionID: 7, Kind: "unknown.kind"}},
			{"state dead", agentapp.WorkItemFilter{VersionID: 7, State: "dead"}},
			{"state unknown", agentapp.WorkItemFilter{VersionID: 7, State: "bogus"}},
			{"created_by", agentapp.WorkItemFilter{VersionID: 7, CreatedBy: "robot"}},
			{"before", agentapp.WorkItemFilter{VersionID: 7, BeforeID: agentP6Int64Ptr(0)}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				reader := &fakeP6RetryReader{}
				service := agentapp.NewQueryService(reader)
				if _, _, err := service.WorkItems(context.Background(), tc.filter); err == nil {
					t.Fatal("expected invalid_argument")
				} else if platform.AsError(err).Status != 400 {
					t.Fatalf("status = %d, want 400", platform.AsError(err).Status)
				}
				if reader.workItemCalls != 0 {
					t.Fatalf("reader calls = %d, want 0", reader.workItemCalls)
				}
			})
		}
	})

	t.Run("reader error is propagated", func(t *testing.T) {
		want := errors.New("boom")
		service := agentapp.NewQueryService(&fakeP6RetryReader{workItemsErr: want})
		if _, _, err := service.WorkItems(context.Background(), agentapp.WorkItemFilter{VersionID: 7}); !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
	})

	t.Run("single work item is version scoped", func(t *testing.T) {
		reader := &fakeP6RetryReader{getItem: sampleWorkItems()[0]}
		service := agentapp.NewQueryService(reader)

		view, err := service.WorkItem(context.Background(), 7, 401)
		if err != nil {
			t.Fatalf("WorkItem: %v", err)
		}
		if reader.getVersionID != 7 || reader.getWorkItemID != 401 {
			t.Fatalf("get scope = %d/%d, want 7/401", reader.getVersionID, reader.getWorkItemID)
		}
		if view.ID != 401 || view.VersionID != 7 || view.State != string(agent.WorkItemRunning) {
			t.Fatalf("view = %#v", view)
		}
	})

	t.Run("foreign or missing work item is not found", func(t *testing.T) {
		reader := &fakeP6RetryReader{getErr: agent.ErrNotFound}
		service := agentapp.NewQueryService(reader)

		_, err := service.WorkItem(context.Background(), 7, 999)
		if err == nil {
			t.Fatal("expected not_found")
		}
		if platform.AsError(err).Status != 404 || platform.AsError(err).Code != "not_found" {
			t.Fatalf("error = %#v, want 404 not_found", platform.AsError(err))
		}
		if reader.getVersionID != 7 {
			t.Fatalf("get scope = %d, want 7", reader.getVersionID)
		}
	})

	t.Run("non-positive ids are rejected before the read", func(t *testing.T) {
		reader := &fakeP6RetryReader{}
		service := agentapp.NewQueryService(reader)
		for _, ids := range [][2]int64{{0, 401}, {7, 0}, {-1, 401}} {
			if _, err := service.WorkItem(context.Background(), ids[0], ids[1]); err == nil {
				t.Fatalf("WorkItem(%d, %d) = nil error, want invalid_argument", ids[0], ids[1])
			}
		}
		if reader.getCalls != 0 {
			t.Fatalf("reader calls = %d, want 0", reader.getCalls)
		}
	})

	t.Run("nil reader is unavailable", func(t *testing.T) {
		service := agentapp.NewQueryService(nil)
		if _, _, err := service.WorkItems(context.Background(), agentapp.WorkItemFilter{VersionID: 7}); err == nil ||
			platform.AsError(err).Status != 503 {
			t.Fatalf("WorkItems error = %v, want 503", err)
		}
		if _, err := service.WorkItem(context.Background(), 7, 401); err == nil ||
			platform.AsError(err).Status != 503 {
			t.Fatalf("WorkItem error = %v, want 503", err)
		}
	})
}

// TestVersionWorkItemsDeadProjection pins the projection the state=failed SQL
// hint has to match: a stored dead row is reported as failed (the frozen enum
// has no dead), a stored failed row carrying the Task 46 cancel marker is
// reported as cancelled, and a dead row that still carries the marker stays
// failed because the marker only reclassifies failed rows.
func TestVersionWorkItemsDeadProjection(t *testing.T) {
	cancelledReason := agentapp.JobCanceledReason
	rows := []agent.WorkItem{
		{ID: 451, VersionID: 7, Kind: agent.WorkItemKindExecuteBatch, Status: agent.WorkItemFailed},
		{ID: 452, VersionID: 7, Kind: agent.WorkItemKindExecuteBatch, Status: agent.WorkItemDead},
		{ID: 453, VersionID: 7, Kind: agent.WorkItemKindExecuteBatch, Status: agent.WorkItemFailed, LastError: &cancelledReason},
		{ID: 454, VersionID: 7, Kind: agent.WorkItemKindExecuteBatch, Status: agent.WorkItemDead, LastError: &cancelledReason},
	}
	service := agentapp.NewQueryService(&fakeP6RetryReader{workItemRows: rows})

	views, _, err := service.WorkItems(context.Background(), agentapp.WorkItemFilter{VersionID: 7})
	if err != nil {
		t.Fatalf("WorkItems: %v", err)
	}
	want := map[int64]string{
		451: string(agent.WorkItemFailed),
		452: string(agent.WorkItemFailed),
		453: agentapp.JobStateCancelled,
		454: string(agent.WorkItemFailed),
	}
	if len(views) != len(want) {
		t.Fatalf("views = %d, want %d", len(views), len(want))
	}
	for _, view := range views {
		if view.State != want[view.ID] {
			t.Fatalf("view %d state = %q, want %q", view.ID, view.State, want[view.ID])
		}
	}
}
