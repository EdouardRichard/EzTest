//go:build integration

package main

// v0.22 W0 fix round 1 (Task 36 review Important 4): the real PostgreSQL
// CompletionStore. One transaction writes the plan item status/decision and the
// progress fact; a retried completion is idempotent on both sides.

import (
	"context"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versioninfra "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
)

func TestPGCompletionStoreCommitsPlanAndFactAtomically(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	ids, err := platformid.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	now := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)

	versionID := ids.Next()
	versionRepo := versioninfra.New(pool, ids)
	if err := versionRepo.Create(ctx, versiondomain.Version{
		ID: versionID, Title: "Completion version", Status: versiondomain.StatusInitial,
		CreatedBy: 42, CreatedAt: now, UpdatedAt: now,
	}, nil); err != nil {
		t.Fatalf("create version: %v", err)
	}
	planRepo := versionpg.NewPlanRepository(pool)
	plan, _, err := planRepo.Upsert(ctx, versiondomain.PlanItem{
		ID: ids.Next(), VersionID: versionID, Phase: "designing",
		Title: "完成登录设计", Status: "in_progress", Order: 1,
	})
	if err != nil {
		t.Fatalf("seed plan item: %v", err)
	}

	store := pgCompletionStore{pool: pool, ids: ids}
	write := agentapp.CompletionWrite{
		VersionID: versionID, AgentRunID: 901, PlanItemID: plan.ID,
		PlanStatus: "done", Decision: "implemented login",
		FactKind: "progress", FactKey: "task.complete:run:901",
		FactPayload: map[string]any{
			"summary": "implemented login", "status": "completed",
			"artifacts": []any{}, "plan_item_id": "1",
		},
		Source: "task.complete", SourceRef: "task.complete:run:901",
	}
	result, err := store.CommitCompletion(ctx, write)
	if err != nil {
		t.Fatalf("CommitCompletion: %v", err)
	}
	if result.FactID <= 0 || result.PlanItemRevision != plan.Revision+1 {
		t.Fatalf("result = %#v (initial revision %d)", result, plan.Revision)
	}
	items, err := planRepo.List(ctx, versionID, "")
	if err != nil {
		t.Fatalf("list plan items: %v", err)
	}
	var stored versiondomain.PlanItem
	for _, item := range items {
		if item.ID == plan.ID {
			stored = item
		}
	}
	if stored.ID == 0 || stored.Status != "done" || stored.Decision != "implemented login" {
		t.Fatalf("stored plan item = %#v", stored)
	}
	var factCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM version_fact WHERE version_id = $1 AND kind = 'progress'`,
		versionID).Scan(&factCount); err != nil {
		t.Fatalf("count facts: %v", err)
	}
	if factCount != 1 {
		t.Fatalf("facts = %d, want one progress fact", factCount)
	}

	// A retried completion converges: the plan is not rewritten again and the
	// idempotent fact returns the stored row.
	retried, err := store.CommitCompletion(ctx, write)
	if err != nil {
		t.Fatalf("retried CommitCompletion: %v", err)
	}
	if retried.FactID != result.FactID || retried.PlanItemRevision != result.PlanItemRevision {
		t.Fatalf("retried result = %#v, want %#v", retried, result)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM version_fact WHERE version_id = $1 AND kind = 'progress'`,
		versionID).Scan(&factCount); err != nil {
		t.Fatalf("count facts after retry: %v", err)
	}
	if factCount != 1 {
		t.Fatalf("facts after retry = %d, want one", factCount)
	}
}
