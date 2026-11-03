//go:build integration

package agentpg

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

func p6AgentInt64Ptr(value int64) *int64 { return &value }

func p6SeedWorkItem(t *testing.T, pool *pgxpool.Pool, id, versionID int64, workType, status string, attempts int, createdAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO work_item (id, version_id, team, type, status, attempts, max_attempts, dedupe_key, created_at, updated_at)
		VALUES ($1, $2, 'execution', $3, $4, $5, 3, $6, $7, $7)
	`, id, versionID, workType, status, attempts, fmt.Sprintf("p6-item-%d", id), createdAt); err != nil {
		t.Fatalf("seed work item: %v", err)
	}
}

func TestP6AgentQueryReaderCountTaskRetries(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool)
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

	p6SeedWorkItem(t, pool, 8001, 1001, "execute.case", "open", 0, base)
	p6SeedWorkItem(t, pool, 8002, 1001, "execute.case", "done", 1, base)
	p6SeedWorkItem(t, pool, 8003, 1001, "execute.case", "failed", 2, base)
	p6SeedWorkItem(t, pool, 8004, 1001, "execute.case", "dead", 5, base)
	p6SeedWorkItem(t, pool, 8011, 1002, "design.plan", "done", 1, base)

	num, denom, err := reader.CountTaskRetries(ctx, nil)
	if err != nil {
		t.Fatalf("CountTaskRetries global: %v", err)
	}
	if num != 2 || denom != 4 {
		t.Fatalf("global counts = %d/%d, want 2/4", num, denom)
	}

	num, denom, err = reader.CountTaskRetries(ctx, p6AgentInt64Ptr(1001))
	if err != nil {
		t.Fatalf("CountTaskRetries version: %v", err)
	}
	if num != 2 || denom != 3 {
		t.Fatalf("version counts = %d/%d, want 2/3", num, denom)
	}

	num, denom, err = reader.CountTaskRetries(ctx, p6AgentInt64Ptr(1002))
	if err != nil {
		t.Fatalf("CountTaskRetries version 1002: %v", err)
	}
	if num != 0 || denom != 1 {
		t.Fatalf("version 1002 counts = %d/%d, want 0/1", num, denom)
	}

	num, denom, err = reader.CountTaskRetries(ctx, p6AgentInt64Ptr(1003))
	if err != nil {
		t.Fatalf("CountTaskRetries empty version: %v", err)
	}
	if num != 0 || denom != 0 {
		t.Fatalf("empty version counts = %d/%d, want 0/0", num, denom)
	}
}

func TestP6AgentQueryReaderListDeadWorkItems(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool)
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

	p6SeedWorkItem(t, pool, 8001, 1001, "execute.case", "dead", 3, base.Add(time.Hour))
	p6SeedWorkItem(t, pool, 8002, 1002, "design.plan", "dead", 3, base.Add(2*time.Hour))
	p6SeedWorkItem(t, pool, 8003, 1001, "execute.case", "failed", 2, base.Add(3*time.Hour))
	p6SeedWorkItem(t, pool, 8004, 1001, "execute.case", "open", 0, base.Add(4*time.Hour))

	rows, err := reader.ListDeadWorkItems(ctx, 10)
	if err != nil {
		t.Fatalf("ListDeadWorkItems: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 dead items", len(rows))
	}
	if rows[0].ID != 8002 || rows[1].ID != 8001 {
		t.Fatalf("ids = %d,%d, want 8002,8001 (created_at DESC)", rows[0].ID, rows[1].ID)
	}
	if rows[0].VersionID != 1002 || rows[0].Title != "design.plan 重试耗尽" {
		t.Fatalf("row = %#v", rows[0])
	}
	if !rows[0].CreatedAt.Equal(base.Add(2 * time.Hour)) {
		t.Fatalf("created_at = %v, want %v", rows[0].CreatedAt, base.Add(2*time.Hour))
	}

	limited, err := reader.ListDeadWorkItems(ctx, 1)
	if err != nil {
		t.Fatalf("ListDeadWorkItems limit: %v", err)
	}
	if len(limited) != 1 || limited[0].ID != 8002 {
		t.Fatalf("limited rows = %#v", limited)
	}
}

func TestP6AgentQueryReaderRequiresPool(t *testing.T) {
	if _, err := NewQueryReader(nil); err == nil {
		t.Fatal("expected an error for a nil pool")
	}
}

/* ------------------- Task 62 fix round 1: read SQL coverage ------------------ */

// p62WorkItemSeed is one 0.2.1 work_item fixture row. An empty Kind seeds a
// legacy pre-0.2.1 row (the column defaults to the empty string).
type p62WorkItemSeed struct {
	ID        int64
	VersionID int64
	Kind      string
	Goal      string
	Status    string
	CreatedBy string
	LastError string
	Target    string
	DependsOn []int64
	ClaimedBy string
	Attempts  int
	CreatedAt time.Time
}

func p62SeedWorkItem(t *testing.T, pool *pgxpool.Pool, seed p62WorkItemSeed) {
	t.Helper()
	createdBy := seed.CreatedBy
	if createdBy == "" {
		createdBy = "state_machine"
	}
	goal := seed.Goal
	if goal == "" {
		goal = fmt.Sprintf("task 62 work item %d", seed.ID)
	}
	target := seed.Target
	if target == "" {
		target = "{}"
	}
	var claimedBy, leaseUntil, lastError any
	if seed.ClaimedBy != "" {
		claimedBy = seed.ClaimedBy
		leaseUntil = seed.CreatedAt.Add(time.Hour)
	}
	if seed.LastError != "" {
		lastError = seed.LastError
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO work_item (id, version_id, team, type, kind, goal, target, status,
			dedupe_key, depends_on, created_by, attempts, max_attempts, claimed_by,
			lease_until, last_error, available_at, created_at, updated_at)
		VALUES ($1, $2, 'execution', 'task62.read', $3, $4, $5::jsonb, $6, $7, $8, $9,
		        $10, 3, $11, $12, $13, $14, $14, $14)
	`, seed.ID, seed.VersionID, seed.Kind, goal, target, seed.Status,
		fmt.Sprintf("task62-%d", seed.ID), seed.DependsOn, createdBy, seed.Attempts,
		claimedBy, leaseUntil, lastError, seed.CreatedAt); err != nil {
		t.Fatalf("seed task 62 work item %d: %v", seed.ID, err)
	}
}

func p62ReadWorkItems(t *testing.T, reader *QueryReader, query agentapp.WorkItemQuery) ([]agent.WorkItem, bool) {
	t.Helper()
	items, hasMore, err := reader.ListWorkItems(context.Background(), query)
	if err != nil {
		t.Fatalf("ListWorkItems(%+v): %v", query, err)
	}
	return items, hasMore
}

func p62WorkItemIDs(items []agent.WorkItem) []int64 {
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

// TestVersionReadWorkItemsSQL executes ListWorkItems: kind/state/created_by
// filters, the empty-kind legacy fence, the Task 46 cancel marker and the
// dead-to-failed projection (a dead row is projected as failed, so the
// state=failed filter must return it) and the limit+1 page boundary.
func TestVersionReadWorkItemsSQL(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool)
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	cancelMarker := agentapp.JobCanceledReason

	p62SeedWorkItem(t, pool, p62WorkItemSeed{ID: 8301, VersionID: 1001, Kind: agent.WorkItemKindExecuteBatch, Status: "done", Attempts: 1, CreatedAt: base})
	p62SeedWorkItem(t, pool, p62WorkItemSeed{ID: 8302, VersionID: 1001, Kind: agent.WorkItemKindExecuteBatch, Status: "failed", CreatedBy: "planner", LastError: "boom", Attempts: 1, CreatedAt: base.Add(time.Minute)})
	p62SeedWorkItem(t, pool, p62WorkItemSeed{ID: 8303, VersionID: 1001, Kind: agent.WorkItemKindDesignCases, Status: "failed", CreatedBy: "planner", LastError: cancelMarker, Attempts: 1, CreatedAt: base.Add(2 * time.Minute)})
	p62SeedWorkItem(t, pool, p62WorkItemSeed{ID: 8304, VersionID: 1001, Kind: agent.WorkItemKindDesignCases, Status: "dead", Attempts: 3, CreatedAt: base.Add(3 * time.Minute)})
	p62SeedWorkItem(t, pool, p62WorkItemSeed{ID: 8305, VersionID: 1001, Kind: agent.WorkItemKindDesignCases, Status: "dead", CreatedBy: "planner", LastError: cancelMarker, Attempts: 3, CreatedAt: base.Add(4 * time.Minute)})
	p62SeedWorkItem(t, pool, p62WorkItemSeed{ID: 8306, VersionID: 1001, Kind: agent.WorkItemKindExecuteBatch, Status: "open", CreatedBy: "trigger", CreatedAt: base.Add(5 * time.Minute)})
	// Legacy pre-0.2.1 row: empty kind, different status and created_by.
	p62SeedWorkItem(t, pool, p62WorkItemSeed{ID: 8307, VersionID: 1001, Kind: "", Status: "done", CreatedBy: "planner", CreatedAt: base.Add(6 * time.Minute)})
	// Same-kind row of another version.
	p62SeedWorkItem(t, pool, p62WorkItemSeed{ID: 8401, VersionID: 1002, Kind: agent.WorkItemKindExecuteBatch, Status: "done", Attempts: 1, CreatedAt: base.Add(7 * time.Minute)})

	scope := func(query agentapp.WorkItemQuery) agentapp.WorkItemQuery {
		query.VersionID = 1001
		if query.Limit == 0 {
			query.Limit = 10
		}
		return query
	}

	// kind filter is version scoped and fences the legacy empty kind.
	items, hasMore := p62ReadWorkItems(t, reader, scope(agentapp.WorkItemQuery{Kind: agent.WorkItemKindExecuteBatch}))
	if ids := p62WorkItemIDs(items); !slices.Equal(ids, []int64{8306, 8302, 8301}) || hasMore {
		t.Fatalf("kind filter = %v has_more=%v, want [8306 8302 8301] false", ids, hasMore)
	}
	if ids := p62WorkItemIDs(mustP62List(t, reader, scope(agentapp.WorkItemQuery{}))); !slices.Equal(ids, []int64{8306, 8305, 8304, 8303, 8302, 8301}) {
		t.Fatalf("unfiltered ids = %v, want legacy 8307 fenced out", ids)
	}

	// created_by filter.
	planner, _ := p62ReadWorkItems(t, reader, scope(agentapp.WorkItemQuery{CreatedBy: "planner"}))
	if ids := p62WorkItemIDs(planner); !slices.Equal(ids, []int64{8305, 8303, 8302}) {
		t.Fatalf("created_by filter = %v, want [8305 8303 8302]", ids)
	}

	// state filters: the cancel marker is cancelled; failed covers stored
	// failed rows without the marker AND every dead row (dead+marker included,
	// because the projection only reclassifies failed+marker as cancelled).
	failed, _ := p62ReadWorkItems(t, reader, scope(agentapp.WorkItemQuery{State: "failed"}))
	if ids := p62WorkItemIDs(failed); !slices.Equal(ids, []int64{8305, 8304, 8302}) {
		t.Fatalf("state=failed = %v, want [8305 8304 8302] (dead rows project as failed)", ids)
	}
	designFailed, _ := p62ReadWorkItems(t, reader, scope(agentapp.WorkItemQuery{Kind: agent.WorkItemKindDesignCases, State: "failed"}))
	if ids := p62WorkItemIDs(designFailed); !slices.Equal(ids, []int64{8305, 8304}) {
		t.Fatalf("design.cases failed = %v, want [8305 8304]", ids)
	}
	cancelled, _ := p62ReadWorkItems(t, reader, scope(agentapp.WorkItemQuery{State: "cancelled"}))
	if ids := p62WorkItemIDs(cancelled); !slices.Equal(ids, []int64{8303}) {
		t.Fatalf("state=cancelled = %v, want [8303] (cancel marker only)", ids)
	}
	open, _ := p62ReadWorkItems(t, reader, scope(agentapp.WorkItemQuery{State: "open"}))
	if ids := p62WorkItemIDs(open); !slices.Equal(ids, []int64{8306}) {
		t.Fatalf("state=open = %v, want [8306]", ids)
	}
	if ids := p62WorkItemIDs(mustP62List(t, reader, scope(agentapp.WorkItemQuery{State: "done"}))); !slices.Equal(ids, []int64{8301}) {
		t.Fatalf("state=done = %v, want [8301] (legacy 8307 fenced)", ids)
	}

	// End-to-end filter + projection: every row the failed filter returns must
	// project as state=failed through the application service.
	service := agentapp.NewQueryService(reader)
	views, _, err := service.WorkItems(ctx, agentapp.WorkItemFilter{VersionID: 1001, State: "failed", Limit: 10})
	if err != nil {
		t.Fatalf("WorkItems(failed): %v", err)
	}
	viewIDs := make([]int64, 0, len(views))
	for _, view := range views {
		if view.State != string(agent.WorkItemFailed) {
			t.Fatalf("failed page view %d state = %q, want %q", view.ID, view.State, agent.WorkItemFailed)
		}
		viewIDs = append(viewIDs, view.ID)
	}
	if !slices.Equal(viewIDs, []int64{8305, 8304, 8302}) {
		t.Fatalf("failed page views = %v, want [8305 8304 8302]", viewIDs)
	}
	cancelledViews, _, err := service.WorkItems(ctx, agentapp.WorkItemFilter{VersionID: 1001, State: "cancelled", Limit: 10})
	if err != nil {
		t.Fatalf("WorkItems(cancelled): %v", err)
	}
	if len(cancelledViews) != 1 || cancelledViews[0].ID != 8303 || cancelledViews[0].State != agentapp.JobStateCancelled {
		t.Fatalf("cancelled views = %#v", cancelledViews)
	}

	// Page boundary: limit+1 read reports has_more and the cursor resumes.
	page, more := p62ReadWorkItems(t, reader, scope(agentapp.WorkItemQuery{Limit: 2}))
	if ids := p62WorkItemIDs(page); !slices.Equal(ids, []int64{8306, 8305}) || !more {
		t.Fatalf("first page = %v has_more=%v, want [8306 8305] true", ids, more)
	}
	cursor := page[len(page)-1].ID
	page, more = p62ReadWorkItems(t, reader, scope(agentapp.WorkItemQuery{BeforeID: &cursor, Limit: 2}))
	if ids := p62WorkItemIDs(page); !slices.Equal(ids, []int64{8304, 8303}) || !more {
		t.Fatalf("second page = %v has_more=%v, want [8304 8303] true", ids, more)
	}
	later := page[len(page)-1].ID
	page, more = p62ReadWorkItems(t, reader, scope(agentapp.WorkItemQuery{BeforeID: &later, Limit: 2}))
	if ids := p62WorkItemIDs(page); !slices.Equal(ids, []int64{8302, 8301}) || more {
		t.Fatalf("last page = %v has_more=%v, want [8302 8301] false", ids, more)
	}
	// A failed page that fills the limit exactly must not report has_more.
	exact, more := p62ReadWorkItems(t, reader, scope(agentapp.WorkItemQuery{State: "failed", Limit: 3}))
	if len(exact) != 3 || more {
		t.Fatalf("exact failed page rows = %d has_more=%v, want 3 false", len(exact), more)
	}

	// Empty version: empty non-nil page, no has_more.
	empty, more := p62ReadWorkItems(t, reader, agentapp.WorkItemQuery{VersionID: 1003, Limit: 10})
	if empty == nil || len(empty) != 0 || more {
		t.Fatalf("empty page = %#v has_more=%v, want empty false", empty, more)
	}
}

func mustP62List(t *testing.T, reader *QueryReader, query agentapp.WorkItemQuery) []agent.WorkItem {
	t.Helper()
	items, _ := p62ReadWorkItems(t, reader, query)
	return items
}

// TestVersionReadSingleWorkItemSQL executes GetWorkItem: the 0.2.1 projection
// columns, version scoping and the missing/foreign/legacy ErrNotFound fence.
func TestVersionReadSingleWorkItemSQL(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool)
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)

	p62SeedWorkItem(t, pool, p62WorkItemSeed{
		ID: 8308, VersionID: 1001, Kind: agent.WorkItemKindExecuteBatch,
		Goal: "执行批次", Status: "claimed", CreatedBy: "planner",
		Target: `{"case_ids":["11"]}`, DependsOn: []int64{8301},
		ClaimedBy: "worker-1", Attempts: 2, CreatedAt: base,
	})
	p62SeedWorkItem(t, pool, p62WorkItemSeed{
		ID: 8307, VersionID: 1001, Kind: "", Status: "done", CreatedAt: base,
	})
	p62SeedWorkItem(t, pool, p62WorkItemSeed{
		ID: 8401, VersionID: 1002, Kind: agent.WorkItemKindExecuteBatch, Status: "done", CreatedAt: base,
	})

	item, err := reader.GetWorkItem(ctx, 1001, 8308)
	if err != nil {
		t.Fatalf("GetWorkItem: %v", err)
	}
	if item.ID != 8308 || item.VersionID != 1001 || item.Kind != agent.WorkItemKindExecuteBatch ||
		item.Goal != "执行批次" || item.Status != agent.WorkItemClaimed || item.CreatedBy != "planner" ||
		item.Attempts != 2 {
		t.Fatalf("item = %#v", item)
	}
	if string(item.Target) != `{"case_ids": ["11"]}` && string(item.Target) != `{"case_ids":["11"]}` {
		t.Fatalf("target = %s", item.Target)
	}
	if !slices.Equal(item.DependsOn, []int64{8301}) {
		t.Fatalf("depends_on = %v, want [8301]", item.DependsOn)
	}
	if item.ClaimedBy == nil || *item.ClaimedBy != "worker-1" {
		t.Fatalf("claimed_by = %#v", item.ClaimedBy)
	}
	if item.LeaseUntil == nil || !item.LeaseUntil.Equal(base.Add(time.Hour)) {
		t.Fatalf("lease_until = %#v", item.LeaseUntil)
	}
	if !item.CreatedAt.Equal(base) || !item.UpdatedAt.Equal(base) {
		t.Fatalf("timestamps = %v/%v", item.CreatedAt, item.UpdatedAt)
	}

	other, err := reader.GetWorkItem(ctx, 1002, 8401)
	if err != nil || other.ID != 8401 || other.VersionID != 1002 {
		t.Fatalf("GetWorkItem(1002, 8401) = %#v/%v", other, err)
	}

	for name, scope := range map[string][2]int64{
		"foreign version": {1002, 8308},
		"missing id":      {1001, 999999},
		"legacy kind":     {1001, 8307},
	} {
		if _, err := reader.GetWorkItem(ctx, scope[0], scope[1]); !errors.Is(err, agent.ErrNotFound) {
			t.Fatalf("%s err = %v, want agent.ErrNotFound", name, err)
		}
	}
	if _, err := reader.GetWorkItem(ctx, 0, 8308); err == nil {
		t.Fatal("expected an error for a non-positive version id")
	}
	if _, err := reader.GetWorkItem(ctx, 1001, 0); err == nil {
		t.Fatal("expected an error for a non-positive work item id")
	}
}
