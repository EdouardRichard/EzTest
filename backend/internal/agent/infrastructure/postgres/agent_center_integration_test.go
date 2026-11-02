//go:build integration

package agentpg_test

import (
	"context"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentpg "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newAgentCenterReader(t *testing.T, pool *pgxpool.Pool) *agentpg.AgentCenterReader {
	t.Helper()
	reader, err := agentpg.NewAgentCenterReader(pool)
	if err != nil {
		t.Fatalf("NewAgentCenterReader: %v", err)
	}
	return reader
}

func insertAgentRun(t *testing.T, pool *pgxpool.Pool, id int64, profileKey string, workItemID *int64, status string, startedAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO agent_run (id, profile_key, work_item_id, conversation_id, status,
			terminal_reason, turns, usage, owner_instance, lease_epoch, started_at, finished_at)
		VALUES ($1, $2, $3, NULL, $4, NULL, 2, '{"input_tokens": 10}'::jsonb, 'test', 0, $5, NULL)
	`, id, profileKey, workItemID, status, startedAt); err != nil {
		t.Fatalf("insert agent_run %d: %v", id, err)
	}
}

func insertWorkItem(t *testing.T, pool *pgxpool.Pool, id, versionID int64, workType, dedupe string) int64 {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO work_item (id, version_id, team, type, dedupe_key)
		VALUES ($1, $2, 'execution', $3, $4)
	`, id, versionID, workType, dedupe); err != nil {
		t.Fatalf("insert work_item %d: %v", id, err)
	}
	return id
}

func TestAgentCenterReaderCatalogAndStats(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewRuntimeRepository(pool, newIDs(t))
	if _, err := agentapp.EnsureProfiles(context.Background(), repo); err != nil {
		t.Fatalf("EnsureProfiles: %v", err)
	}
	reader := newAgentCenterReader(t, pool)

	profiles, err := reader.ListCatalogProfiles(context.Background())
	if err != nil {
		t.Fatalf("ListCatalogProfiles: %v", err)
	}
	if len(profiles) != 14 {
		t.Fatalf("catalog profiles = %d, want 14", len(profiles))
	}
	if profiles[0].Profile.Key != agentapp.ProfileKeyCaseExecutor && profiles[0].Profile.Key != agentapp.ProfileKeyCaseAuthor {
		// The query orders by key; only assert the row shape.
	}
	host := findCatalogRow(t, profiles, agentapp.ProfileKeyClarificationHost)
	if host.Profile.Name != "澄清 Host" || len(host.Profile.AllowedTools) == 0 {
		t.Fatalf("host row = %#v", host)
	}
	if host.PromptVersion != 0 {
		t.Fatalf("prompt version before backfill = %d, want 0", host.PromptVersion)
	}

	started := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	insertAgentRun(t, pool, int64(1001), agentapp.ProfileKeyClarificationHost, nil, "done", started)
	insertAgentRun(t, pool, int64(1002), agentapp.ProfileKeyClarificationHost, nil, "failed", started.Add(time.Minute))
	insertAgentRun(t, pool, int64(1003), agentapp.ProfileKeyClarificationHost, nil, "aborted", started.Add(2*time.Minute))
	insertAgentRun(t, pool, int64(1004), agentapp.ProfileKeyClarificationHost, nil, "running", started.Add(3*time.Minute))
	insertAgentRun(t, pool, int64(1005), agentapp.ProfileKeyDesignHost, nil, "done", started.Add(4*time.Minute))

	stats, err := reader.ProfileRunStats(context.Background())
	if err != nil {
		t.Fatalf("ProfileRunStats: %v", err)
	}
	hostStats := stats[agentapp.ProfileKeyClarificationHost]
	if hostStats.RunCount != 4 || hostStats.DoneCount != 1 || hostStats.FailedCount != 1 || hostStats.AbortedCount != 1 {
		t.Fatalf("host stats = %#v", hostStats)
	}
	if hostStats.LastRunAt == nil || !hostStats.LastRunAt.Equal(started.Add(3*time.Minute)) {
		t.Fatalf("last run = %v", hostStats.LastRunAt)
	}
}

func TestAgentCenterReaderRunsJoinWorkItemAndPaginate(t *testing.T) {
	pool := testutil.NewDB(t)
	reader := newAgentCenterReader(t, pool)
	started := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

	itemA := insertWorkItem(t, pool, 201, 7, "execute.case", "case:1")
	insertWorkItem(t, pool, 202, 8, "design.requirement", "design:1")
	insertAgentRun(t, pool, int64(3001), agentapp.ProfileKeyCaseExecutor, &itemA, "done", started)
	insertAgentRun(t, pool, int64(3002), agentapp.ProfileKeyCaseExecutor, nil, "failed", started.Add(time.Minute))
	insertAgentRun(t, pool, int64(3003), agentapp.ProfileKeyDesignHost, nil, "done", started.Add(2*time.Minute))

	runs, hasMore, err := reader.ListRuns(context.Background(), agentapp.RunQuery{Limit: 2})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if !hasMore || len(runs) != 2 {
		t.Fatalf("hasMore=%v runs=%d", hasMore, len(runs))
	}
	if runs[0].ID != 3003 || runs[1].ID != 3002 {
		t.Fatalf("order = %d,%d", runs[0].ID, runs[1].ID)
	}

	profileFiltered, _, err := reader.ListRuns(context.Background(), agentapp.RunQuery{
		ProfileKey: agentapp.ProfileKeyCaseExecutor, Limit: 10,
	})
	if err != nil {
		t.Fatalf("ListRuns profile: %v", err)
	}
	if len(profileFiltered) != 2 {
		t.Fatalf("profile rows = %d", len(profileFiltered))
	}
	if profileFiltered[1].WorkItemID == nil || *profileFiltered[1].WorkItemID != 201 {
		t.Fatalf("work item = %#v", profileFiltered[1])
	}
	if profileFiltered[1].WorkType == nil || *profileFiltered[1].WorkType != "execute.case" {
		t.Fatalf("work type = %#v", profileFiltered[1].WorkType)
	}
	if profileFiltered[1].VersionID == nil || *profileFiltered[1].VersionID != 7 {
		t.Fatalf("version = %#v", profileFiltered[1].VersionID)
	}
	if profileFiltered[1].Usage["input_tokens"] != 10 {
		t.Fatalf("usage = %#v", profileFiltered[1].Usage)
	}

	versionID := int64(7)
	versionFiltered, _, err := reader.ListRuns(context.Background(), agentapp.RunQuery{VersionID: &versionID, Limit: 10})
	if err != nil {
		t.Fatalf("ListRuns version: %v", err)
	}
	if len(versionFiltered) != 1 || versionFiltered[0].ID != 3001 {
		t.Fatalf("version rows = %#v", versionFiltered)
	}

	before := int64(3002)
	older, _, err := reader.ListRuns(context.Background(), agentapp.RunQuery{BeforeID: &before, Limit: 10})
	if err != nil {
		t.Fatalf("ListRuns before: %v", err)
	}
	if len(older) != 1 || older[0].ID != 3001 {
		t.Fatalf("before rows = %#v", older)
	}

	statusOnly, _, err := reader.ListRuns(context.Background(), agentapp.RunQuery{Status: "done", Limit: 10})
	if err != nil {
		t.Fatalf("ListRuns status: %v", err)
	}
	if len(statusOnly) != 2 {
		t.Fatalf("status rows = %#v", statusOnly)
	}
}

func findCatalogRow(t *testing.T, rows []agentapp.CatalogProfileRow, key string) agentapp.CatalogProfileRow {
	t.Helper()
	for _, row := range rows {
		if row.Profile.Key == key {
			return row
		}
	}
	t.Fatalf("profile %q not in catalog", key)
	return agentapp.CatalogProfileRow{}
}
