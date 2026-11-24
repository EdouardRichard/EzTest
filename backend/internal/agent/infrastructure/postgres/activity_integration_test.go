//go:build integration

package agentpg_test

// v0.2.4-D T10 (SC-D5, Q04=A-lite, V-0019 D-share): agent_activity becomes an
// idempotent FACT ledger keyed (run_id, seq) with a PRODUCTION read side.
//
// Expectations come from the contract: the migration's UNIQUE(run_id, seq), the
// SC-D5 fact kinds and the "facts exist independently of events" rule — not from
// observed repository output.
//
// TestAgentCenterRunActivityReadsTheFactLedger is the V-0019 reader-existence
// guard: it reads through the production AgentCenterService over the production
// postgres reader. Deleting/bypassing that production read call turns it RED.

import (
	"context"
	"strings"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	agentpg "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

func activityRun(t *testing.T, repo *agentpg.RuntimeRepository) agent.AgentRun {
	t.Helper()
	run, err := repo.CreateRun(context.Background(), agent.AgentRun{
		ProfileKey:    "case_executor",
		Status:        agent.AgentRunRunning,
		OwnerInstance: "instance-1",
		LeaseEpoch:    1,
		Usage:         map[string]int{},
		StartedAt:     testTime,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return run
}

func toolActivity(runID int64, seq int, tool string, started time.Time) agent.Activity {
	return agent.Activity{
		RunID:     runID,
		Seq:       seq,
		Type:      agent.ActivityTypeToolCall,
		Title:     tool,
		StartedAt: started,
		Payload:   map[string]any{"tool": tool, "tool_call_id": "call_1"},
	}
}

// TestInsertActivityIsIdempotentPerRunSeq pins the idempotency key: replaying the
// same (run_id, seq) never duplicates a row, while a new seq (or another run) is
// a new fact.
func TestInsertActivityIsIdempotentPerRunSeq(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewRuntimeRepository(pool, newIDs(t))
	ctx := context.Background()
	run := activityRun(t, repo)
	other := activityRun(t, repo)

	row := toolActivity(run.ID, 1, "requirement.read", testTime)
	for attempt := 0; attempt < 3; attempt++ {
		if err := repo.InsertActivity(ctx, row); err != nil {
			t.Fatalf("InsertActivity attempt %d: %v", attempt, err)
		}
	}
	rows, err := repo.ListRunActivity(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunActivity: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 (a replayed (run_id,seq) is the same fact)", len(rows))
	}

	if err := repo.InsertActivity(ctx, toolActivity(run.ID, 2, "schema.read", testTime.Add(time.Second))); err != nil {
		t.Fatalf("InsertActivity seq 2: %v", err)
	}
	// The key is per run: another run may reuse seq 1.
	if err := repo.InsertActivity(ctx, toolActivity(other.ID, 1, "requirement.read", testTime)); err != nil {
		t.Fatalf("InsertActivity other run: %v", err)
	}
	rows, err = repo.ListRunActivity(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunActivity: %v", err)
	}
	if len(rows) != 2 || rows[0].Seq != 1 || rows[1].Seq != 2 {
		t.Fatalf("rows = %#v, want seq 1 and 2 of this run only", rows)
	}
	otherRows, err := repo.ListRunActivity(ctx, other.ID)
	if err != nil {
		t.Fatalf("ListRunActivity(other): %v", err)
	}
	if len(otherRows) != 1 || otherRows[0].RunID != other.ID {
		t.Fatalf("other run rows = %#v, want its own single fact", otherRows)
	}
}

// TestInsertActivityFoldsTheCallEndIntoTheStartRow pins the tool-call lifecycle:
// start and end are ONE row, the end adds ended_at/error/payload keys, and a
// replayed start cannot erase the recorded outcome.
func TestInsertActivityFoldsTheCallEndIntoTheStartRow(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewRuntimeRepository(pool, newIDs(t))
	ctx := context.Background()
	run := activityRun(t, repo)

	start := toolActivity(run.ID, 1, "requirement.read", testTime)
	if err := repo.InsertActivity(ctx, start); err != nil {
		t.Fatalf("InsertActivity start: %v", err)
	}
	rows, err := repo.ListRunActivity(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunActivity: %v", err)
	}
	if len(rows) != 1 || rows[0].EndedAt != nil {
		t.Fatalf("in-flight fact = %#v, want one row with no end yet", rows)
	}
	startedID := rows[0].ID

	ended := testTime.Add(2 * time.Second)
	end := start
	end.EndedAt = &ended
	end.Error = "invalid_argument: requirement_id is required"
	end.Payload = map[string]any{"tool": "requirement.read", "duration_ms": 2000}
	if err := repo.InsertActivity(ctx, end); err != nil {
		t.Fatalf("InsertActivity end: %v", err)
	}
	rows, err = repo.ListRunActivity(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunActivity: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want the end folded into the start row", len(rows))
	}
	stored := rows[0]
	if stored.ID != startedID {
		t.Fatalf("row id = %d, want the original %d (upsert, not a new row)", stored.ID, startedID)
	}
	if stored.EndedAt == nil || !stored.EndedAt.Equal(ended) {
		t.Fatalf("ended_at = %#v, want %v", stored.EndedAt, ended)
	}
	if !stored.StartedAt.Equal(testTime) {
		t.Fatalf("started_at = %v, want the original start %v", stored.StartedAt, testTime)
	}
	if stored.Error != end.Error {
		t.Fatalf("error = %q, want the failure reason %q", stored.Error, end.Error)
	}
	if stored.Payload["tool_call_id"] != "call_1" {
		t.Fatalf("payload lost the start keys: %#v", stored.Payload)
	}
	if duration, ok := stored.Payload["duration_ms"].(float64); !ok || duration != 2000 {
		t.Fatalf("payload.duration_ms = %#v, want 2000", stored.Payload["duration_ms"])
	}

	// Replaying the START after the END must not erase the outcome.
	if err := repo.InsertActivity(ctx, start); err != nil {
		t.Fatalf("InsertActivity replayed start: %v", err)
	}
	rows, err = repo.ListRunActivity(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunActivity: %v", err)
	}
	if len(rows) != 1 || rows[0].EndedAt == nil || rows[0].Error != end.Error {
		t.Fatalf("a replayed start erased the outcome: %#v", rows)
	}
}

// TestInsertActivityStoresCompactionFacts pins the second fact kind and its
// coarse payload (trigger + before/after token counts).
func TestInsertActivityStoresCompactionFacts(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewRuntimeRepository(pool, newIDs(t))
	ctx := context.Background()
	run := activityRun(t, repo)
	ended := testTime.Add(time.Second)

	if err := repo.InsertActivity(ctx, agent.Activity{
		RunID:     run.ID,
		Seq:       1,
		Type:      agent.ActivityTypeCompaction,
		Title:     "context compaction (pressure)",
		StartedAt: testTime,
		EndedAt:   &ended,
		Payload: map[string]any{
			"trigger": "pressure", "before_tokens": 4000, "after_tokens": 900,
			"retain_tokens": 512, "summary_version": 1,
		},
	}); err != nil {
		t.Fatalf("InsertActivity compaction: %v", err)
	}
	rows, err := repo.ListRunActivity(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunActivity: %v", err)
	}
	if len(rows) != 1 || rows[0].Type != agent.ActivityTypeCompaction {
		t.Fatalf("rows = %#v, want one compaction fact", rows)
	}
	if trigger, _ := rows[0].Payload["trigger"].(string); trigger != "pressure" {
		t.Fatalf("payload.trigger = %#v", rows[0].Payload["trigger"])
	}
	if before, ok := rows[0].Payload["before_tokens"].(float64); !ok || before != 4000 {
		t.Fatalf("payload.before_tokens = %#v, want 4000", rows[0].Payload["before_tokens"])
	}
	if after, ok := rows[0].Payload["after_tokens"].(float64); !ok || after != 900 {
		t.Fatalf("payload.after_tokens = %#v, want 900", rows[0].Payload["after_tokens"])
	}
}

// TestInsertActivityRejectsAnInvalidFact pins the failure path: an unknown type
// or a missing key is a typed field error, never a silently stored row.
func TestInsertActivityRejectsAnInvalidFact(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewRuntimeRepository(pool, newIDs(t))
	ctx := context.Background()
	run := activityRun(t, repo)

	bad := toolActivity(run.ID, 1, "requirement.read", testTime)
	bad.Type = agent.ActivityType("thinking")
	if err := repo.InsertActivity(ctx, bad); err == nil {
		t.Fatalf("an unknown activity type was stored")
	}
	bad = toolActivity(run.ID, 0, "requirement.read", testTime)
	if err := repo.InsertActivity(ctx, bad); err == nil {
		t.Fatalf("a fact without a seq was stored")
	}
	rows, err := repo.ListRunActivity(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunActivity: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %#v, want none after rejected writes", rows)
	}
}

// TestActivityRunSeqUniqueConstraintExists pins the migration's idempotency key
// at the schema level: a UNIQUE constraint on exactly (run_id, seq) and a NOT
// NULL seq column. The constraint is the authority the UPSERT relies on.
func TestActivityRunSeqUniqueConstraintExists(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()

	var definition string
	if err := pool.QueryRow(ctx, `
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conname = 'uk_agent_activity_run_seq'
		  AND contype = 'u'
		  AND connamespace = current_schema()::regnamespace
	`).Scan(&definition); err != nil {
		t.Fatalf("read uk_agent_activity_run_seq: %v", err)
	}
	if !strings.Contains(definition, "run_id") || !strings.Contains(definition, "seq") {
		t.Fatalf("unique definition = %q, want UNIQUE (run_id, seq)", definition)
	}
	var notNull bool
	if err := pool.QueryRow(ctx, `
		SELECT attnotnull FROM pg_attribute
		WHERE attrelid = 'agent_activity'::regclass AND attname = 'seq'
	`).Scan(&notNull); err != nil {
		t.Fatalf("read agent_activity.seq: %v", err)
	}
	if !notNull {
		t.Fatalf("agent_activity.seq is nullable, want NOT NULL")
	}
	// The raw constraint really rejects a duplicate key: the UPSERT is not the
	// only thing standing between the ledger and duplicate facts.
	repo := agentpg.NewRuntimeRepository(pool, newIDs(t))
	run := activityRun(t, repo)
	if err := repo.InsertActivity(ctx, toolActivity(run.ID, 1, "requirement.read", testTime)); err != nil {
		t.Fatalf("InsertActivity: %v", err)
	}
	stored, err := repo.ListRunActivity(ctx, run.ID)
	if err != nil || len(stored) != 1 {
		t.Fatalf("ListRunActivity: %v rows %d", err, len(stored))
	}
	// The duplicate row's id is DERIVED from the stored one so it is guaranteed
	// distinct: a second snowflake generator restarts its sequence and can emit
	// an id that already exists, which would trip pk_agent_activity first and
	// make this assertion about the wrong constraint (found in the T10 fix wave).
	_, err = pool.Exec(ctx, `
		INSERT INTO agent_activity (id, run_id, seq, type) VALUES ($1, $2, 1, 'tool_call')
	`, stored[0].ID+1, run.ID)
	if err == nil {
		t.Fatalf("a raw duplicate (run_id, seq) insert succeeded")
	}
	if !strings.Contains(err.Error(), "uk_agent_activity_run_seq") {
		t.Fatalf("duplicate insert error = %v, want the unique constraint violation", err)
	}
}

// TestAgentCenterRunActivityReadsTheFactLedger is the V-0019 D-share closure
// guard. It exercises the PRODUCTION read chain —
// AgentCenterService.RunActivity -> AgentCenterReader.RunActivity -> the shared
// agent_activity query — over facts written through the production repository.
//
// Mutation proof (recorded in task-10-report.md): replacing the production read
// call in AgentCenterService.RunActivity with a stub answer turns this test RED.
func TestAgentCenterRunActivityReadsTheFactLedger(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewRuntimeRepository(pool, newIDs(t))
	ctx := context.Background()
	run := activityRun(t, repo)

	if err := repo.InsertActivity(ctx, toolActivity(run.ID, 1, "requirement.read", testTime)); err != nil {
		t.Fatalf("InsertActivity tool: %v", err)
	}
	ended := testTime.Add(time.Second)
	if err := repo.InsertActivity(ctx, agent.Activity{
		RunID: run.ID, Seq: 2, Type: agent.ActivityTypeCompaction,
		Title: "context compaction (overflow)", StartedAt: testTime, EndedAt: &ended,
		Payload: map[string]any{"trigger": "overflow", "before_tokens": 5000, "after_tokens": 800},
	}); err != nil {
		t.Fatalf("InsertActivity compaction: %v", err)
	}

	service := agentapp.NewAgentCenterService(productionCenterReader(t, pool), nil, nil)
	rows, err := service.RunActivity(ctx, run.ID)
	if err != nil {
		t.Fatalf("AgentCenterService.RunActivity: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("read rows = %d, want both facts through the production reader", len(rows))
	}
	if rows[0].Seq != 1 || rows[0].Type != agent.ActivityTypeToolCall || rows[0].Title != "requirement.read" {
		t.Fatalf("first fact = %#v, want the tool call", rows[0])
	}
	if rows[1].Seq != 2 || rows[1].Type != agent.ActivityTypeCompaction {
		t.Fatalf("second fact = %#v, want the compaction boundary", rows[1])
	}
	if before, ok := rows[1].Payload["before_tokens"].(float64); !ok || before != 5000 {
		t.Fatalf("compaction payload.before_tokens = %#v, want 5000", rows[1].Payload["before_tokens"])
	}
	if rows[0].ID == 0 || rows[0].CreatedAt.IsZero() || rows[0].UpdatedAt.IsZero() {
		t.Fatalf("fact bookkeeping is missing: %#v", rows[0])
	}
	// A run with no recorded boundary reads as an empty ledger, not an error.
	empty := activityRun(t, repo)
	emptyRows, err := service.RunActivity(ctx, empty.ID)
	if err != nil {
		t.Fatalf("RunActivity(empty run): %v", err)
	}
	if len(emptyRows) != 0 {
		t.Fatalf("empty run rows = %#v, want none", emptyRows)
	}
}

func productionCenterReader(t *testing.T, pool *pgxpool.Pool) *agentpg.AgentCenterReader {
	t.Helper()
	reader, err := agentpg.NewAgentCenterReader(pool)
	if err != nil {
		t.Fatalf("NewAgentCenterReader: %v", err)
	}
	return reader
}
