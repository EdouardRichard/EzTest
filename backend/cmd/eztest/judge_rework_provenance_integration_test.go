//go:build integration

package main

import (
	"context"
	"fmt"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	agentpg "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// TestJudgeReworkProvenanceSQLIntegration pins the fix-round-1 targeted SQL
// source against the real work_item table and the REAL production writer
// (WorkQueue.Ensure, which creates OPEN items — the exact race window the
// review found): a rework request counts from creation regardless of status,
// the from-version binding selects the rework responding to one specific fail,
// and the query is immune to the generic newest-500 ListByVersion window that
// a long version's chat turns can push the rework row out of.
func TestJudgeReworkProvenanceSQLIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	ids, err := platformid.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	versionID := ids.Next()
	const caseID, failedVersion = int64(7001), int64(8001)

	queue := agentapp.NewWorkQueue(agentpg.NewWorkItemRepository(pool, ids), clock.System{}, 0, 3)
	reader := &judgeReworkWorkItemReader{source: &judgeReworkSQLSource{pool: pool}}

	// No rework yet: zero, no error.
	got, err := reader.ReworkRequestedAt(ctx, versionID, caseID, failedVersion)
	if err != nil || !got.IsZero() {
		t.Fatalf("pre-insert got %v err %v, want zero/nil", got, err)
	}

	// The production writer: handleJudgeFail's payload shape (decimal-string
	// IDs, case_version_id = the FAILED version being reworked from). Ensure
	// creates the item OPEN — non-terminal, exactly the race window.
	rework, created, err := queue.Ensure(ctx, agent.NewWorkItem{
		VersionID: versionID,
		Team:      string(agent.TeamDesign),
		Type:      designapp.TaskTypeDesignRework,
		DedupeKey: fmt.Sprintf("rework:%d:%d", caseID, failedVersion),
		Payload: map[string]any{
			"case_id":         platformid.Format(caseID),
			"case_version_id": platformid.Format(failedVersion),
			"round":           2,
		},
	})
	if err != nil || !created {
		t.Fatalf("ensure rework: created=%v err=%v", created, err)
	}
	if rework.Status.Terminal() {
		t.Fatalf("fixture precondition broken: Ensure created a terminal item (%s)", rework.Status)
	}

	// RACE (review Important): the OPEN rework request must count immediately.
	got, err = reader.ReworkRequestedAt(ctx, versionID, caseID, failedVersion)
	if err != nil || !got.Equal(rework.CreatedAt) {
		t.Fatalf("open rework got %v err %v, want %v (request counts from creation)", got, err, rework.CreatedAt)
	}

	// FROM-VERSION: a query for a different fail version finds nothing.
	got, err = reader.ReworkRequestedAt(ctx, versionID, caseID, failedVersion+1)
	if err != nil || !got.IsZero() {
		t.Fatalf("other version got %v err %v, want zero (from-version binding)", got, err)
	}
	// A different case finds nothing.
	got, err = reader.ReworkRequestedAt(ctx, versionID, caseID+1, failedVersion)
	if err != nil || !got.IsZero() {
		t.Fatalf("other case got %v err %v, want zero", got, err)
	}

	// Status transitions never change the answer: running (the judge may
	// settle mid-rework) and done both count.
	for _, status := range []agent.WorkItemStatus{agent.WorkItemRunning, agent.WorkItemDone} {
		if _, err := pool.Exec(ctx, `UPDATE work_item SET status = $1 WHERE id = $2`, string(status), rework.ID); err != nil {
			t.Fatalf("set status %s: %v", status, err)
		}
		got, err = reader.ReworkRequestedAt(ctx, versionID, caseID, failedVersion)
		if err != nil || !got.Equal(rework.CreatedAt) {
			t.Fatalf("status %s got %v err %v, want %v", status, got, err, rework.CreatedAt)
		}
	}

	// WINDOW (review Minor#4): push the rework row out of the generic
	// newest-500 ListByVersion page with 501 newer chat.turn rows, prove the
	// old mechanism would miss it, and prove the targeted query still finds it.
	// Noise ids live in a dedicated high range (1<<62 + offset) far above any
	// snowflake id, so they sort first under ORDER BY id DESC.
	const noiseBase = int64(1)<<62 + 700000
	if _, err := pool.Exec(ctx, `
		INSERT INTO work_item (id, version_id, team, type, dedupe_key, payload)
		SELECT $1::bigint + g, $2, 'clarification', 'chat.turn', 'noise:' || g, '{}'::jsonb
		FROM generate_series(1, 501) AS g
	`, noiseBase, versionID); err != nil {
		t.Fatalf("insert noise rows: %v", err)
	}
	page, err := queue.ListByVersion(ctx, versionID, agentapp.MaxListLimit)
	if err != nil {
		t.Fatalf("ListByVersion: %v", err)
	}
	if len(page) != agentapp.MaxListLimit {
		t.Fatalf("generic page size = %d, want the full %d-row window", len(page), agentapp.MaxListLimit)
	}
	for _, item := range page {
		if item.ID == rework.ID {
			t.Fatal("fixture precondition broken: the rework row is still inside the generic window")
		}
	}
	got, err = reader.ReworkRequestedAt(ctx, versionID, caseID, failedVersion)
	if err != nil || !got.Equal(rework.CreatedAt) {
		t.Fatalf("beyond-window got %v err %v, want %v (targeted query must not ride the capped list)", got, err, rework.CreatedAt)
	}

	// KIND AUTHORITY: a contract-kind row typed design.rework is not a rework
	// under WorkTypeForItem (kind authoritative when present).
	if _, err := pool.Exec(ctx, `
		INSERT INTO work_item (id, version_id, team, type, kind, dedupe_key, payload)
		VALUES ($1, $2, 'design', 'design.rework', 'design.cases', 'kind-noise', $3::jsonb)
	`, ids.Next(), versionID, fmt.Sprintf(`{"case_id":"%d","case_version_id":"%d"}`, caseID, failedVersion+2)); err != nil {
		t.Fatalf("insert kind-noise row: %v", err)
	}
	got, err = reader.ReworkRequestedAt(ctx, versionID, caseID, failedVersion+2)
	if err != nil || !got.IsZero() {
		t.Fatalf("kind-noise got %v err %v, want zero (kind authoritative)", got, err)
	}

	// Nil pool fails closed.
	nilReader := &judgeReworkWorkItemReader{source: &judgeReworkSQLSource{}}
	if _, err := nilReader.ReworkRequestedAt(ctx, versionID, caseID, failedVersion); err == nil {
		t.Fatal("nil pool must fail closed")
	}
}
