//go:build integration

package agentpg_test

// v0.2.4-H T12 (H-E2): real-Postgres proof of the run-level crash sweep.
//
// The crash is reproduced with the production recovery path, not simulated: a
// claimed item's lease is left to expire (the process died), RecoverExpired
// settles the WORK ITEM, and the run row plus its streaming transcript head are
// then observed untouched — that is the defect. The sweep must close it through
// the existing terminal entries, and it must never touch a run whose attempt
// still owns the work item.
//
// Expectations come from the agent domain authorities (AgentRunStatus,
// AttemptStatus, MessageStreamState) and the work-item lease contract, never from
// observed repository output.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	agentpg "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

type crashSweepTestClock struct{ now time.Time }

func (c crashSweepTestClock) Now() time.Time { return c.now }

// crashedAttempt claims item, creates its running agent_run and opens a streaming
// transcript head, then lets the lease expire without ever finishing the run.
func crashedAttempt(t *testing.T, pool *pgxpool.Pool, queue *agentpg.WorkItemRepository,
	runtimeRepo *agentpg.RuntimeRepository, item agent.WorkItem) (agent.WorkItem, agent.AgentRun, agent.AgentMessage) {
	t.Helper()
	ctx := context.Background()
	claimed, ok, err := queue.Claim(ctx, "crashed-worker", time.Minute)
	if err != nil || !ok {
		t.Fatalf("Claim: %#v, %v", claimed, err)
	}
	if claimed.ID != item.ID {
		t.Fatalf("claimed %d, want the fixture item %d", claimed.ID, item.ID)
	}
	run, err := runtimeRepo.CreateRun(ctx, agent.AgentRun{
		ProfileKey:    "clarification_host",
		WorkItemID:    &claimed.ID,
		Status:        agent.AgentRunRunning,
		OwnerInstance: "crashed-worker",
		LeaseEpoch:    claimed.LeaseEpoch,
		Turns:         1,
		Usage:         map[string]int{"prompt_tokens": 42},
		StartedAt:     testTime,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	head, err := runtimeRepo.AppendMessage(ctx, agent.AgentMessage{
		RunID:       ptr(run.ID),
		Role:        agent.MessageRoleAssistant,
		Parts:       []agent.MessagePart{{Type: "text", Text: "崩溃前已生成的内容"}},
		StreamState: agent.MessageStreamStateStreaming,
		CreatedAt:   testTime,
	})
	if err != nil {
		t.Fatalf("AppendMessage(streaming): %v", err)
	}
	// The process dies: the lease expires and nothing writes the run terminal.
	if _, err := pool.Exec(ctx, `UPDATE work_item SET lease_until = now() - interval '1 minute' WHERE id = $1`, claimed.ID); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	return claimed, run, head
}

func readRunRow(t *testing.T, pool *pgxpool.Pool, runID int64) (string, string, *time.Time) {
	t.Helper()
	var status string
	var reason *string
	var finishedAt *time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT status, terminal_reason, finished_at FROM agent_run WHERE id = $1`, runID).Scan(&status, &reason, &finishedAt); err != nil {
		t.Fatalf("read agent_run: %v", err)
	}
	text := ""
	if reason != nil {
		text = *reason
	}
	return status, text, finishedAt
}

func readMessageStreamState(t *testing.T, pool *pgxpool.Pool, messageID int64) string {
	t.Helper()
	var state string
	if err := pool.QueryRow(context.Background(),
		`SELECT stream_state FROM agent_message WHERE id = $1`, messageID).Scan(&state); err != nil {
		t.Fatalf("read agent_message: %v", err)
	}
	return state
}

// TestCrashSweepSealsDeadItemStreamingHeadIntegration is the H-E2 dead-item leg:
// attempts exhausted -> work item dead -> the run and its transcript head must
// still reach a terminal state, with no later attempt to heal them.
func TestCrashSweepSealsDeadItemStreamingHeadIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	queue := agentpg.NewWorkItemRepository(pool, ids)
	runtimeRepo := agentpg.NewRuntimeRepository(pool, ids)
	ctx := context.Background()
	item := ensureItem(t, queue, 91, "t12-crash-dead", 0, 1)
	claimed, run, head := crashedAttempt(t, pool, queue, runtimeRepo, item)

	sweeper := agentapp.NewRunCrashSweeper(runtimeRepo, nil, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)), crashSweepTestClock{now: testTime})

	// The attempt still owns the item (claimed, matching epoch): nothing to sweep.
	before, err := sweeper.Sweep(ctx)
	if err != nil || before.Runs != 0 {
		t.Fatalf("sweep before recovery = %#v (%v), want nothing while the lease row is still leased", before, err)
	}

	recovered, err := queue.RecoverExpired(ctx)
	if err != nil || recovered != 1 {
		t.Fatalf("RecoverExpired = %d (%v), want the one expired item", recovered, err)
	}
	dead, err := queue.Get(ctx, claimed.ID)
	if err != nil || dead.Status != agent.WorkItemDead {
		t.Fatalf("recovered item status = %q (%v), want dead at the attempt cap", dead.Status, err)
	}
	// THE DEFECT: the work item is settled, the run and its head are not.
	if status, _, _ := readRunRow(t, pool, run.ID); status != string(agent.AgentRunRunning) {
		t.Fatalf("run status after RecoverExpired = %q, want the unrecovered running row this task fixes", status)
	}
	if state := readMessageStreamState(t, pool, head.ID); state != agent.MessageStreamStateStreaming {
		t.Fatalf("transcript stream_state after RecoverExpired = %q, want the unrecovered streaming head", state)
	}

	report, err := sweeper.Sweep(ctx)
	if err != nil || report.Runs != 1 || report.TranscriptHeads != 1 || report.Failed != 0 {
		t.Fatalf("sweep = %#v (%v), want the dead item's run and head recovered", report, err)
	}
	status, reason, finishedAt := readRunRow(t, pool, run.ID)
	if status != string(agent.AgentRunInterrupted) || reason != "interrupted" || finishedAt == nil {
		t.Fatalf("run terminal = %q/%q/%v, want interrupted with a reason and finished_at", status, reason, finishedAt)
	}
	if state := readMessageStreamState(t, pool, head.ID); state != agent.MessageStreamStateInterrupted {
		t.Fatalf("transcript stream_state = %q, want interrupted", state)
	}
	messages, err := runtimeRepo.ListRunMessages(ctx, run.ID, 0)
	if err != nil || len(messages) != 1 {
		t.Fatalf("ListRunMessages = %d rows (%v), want the one preserved row", len(messages), err)
	}
	if len(messages[0].Parts) != 1 || messages[0].Parts[0].Text != "崩溃前已生成的内容" {
		t.Fatalf("transcript parts = %#v, want the accumulated content preserved", messages[0].Parts)
	}

	// Idempotent: the run is terminal now, so a second sweep finds nothing.
	again, err := sweeper.Sweep(ctx)
	if err != nil || again.Runs != 0 {
		t.Fatalf("repeated sweep = %#v (%v), want 0", again, err)
	}
}

// TestListCrashedRunsSelectsOnlyOrphanedAttemptsIntegration pins the selector
// against the real lease/epoch SQL: a live attempt is never selected, a
// recovered-and-reclaimed item's OLD run is, and the new attempt's run is not.
func TestListCrashedRunsSelectsOnlyOrphanedAttemptsIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	queue := agentpg.NewWorkItemRepository(pool, ids)
	runtimeRepo := agentpg.NewRuntimeRepository(pool, ids)
	ctx := context.Background()

	// A healthy live attempt: claimed, lease in the future, matching epoch.
	liveItem := ensureItem(t, queue, 92, "t12-crash-live", 5, 3)
	_, liveRun, _ := crashedAttempt(t, pool, queue, runtimeRepo, liveItem)
	if _, err := pool.Exec(ctx, `UPDATE work_item SET lease_until = now() + interval '5 minutes' WHERE id = $1`, liveItem.ID); err != nil {
		t.Fatalf("renew live lease: %v", err)
	}

	// A crashed attempt that WILL be retried: recovered to open, then reclaimed.
	retryItem := ensureItem(t, queue, 92, "t12-crash-retry", 0, 3)
	crashedClaim, oldRun, oldHead := crashedAttempt(t, pool, queue, runtimeRepo, retryItem)
	if _, err := queue.RecoverExpired(ctx); err != nil {
		t.Fatalf("RecoverExpired: %v", err)
	}
	reopened, err := queue.Get(ctx, crashedClaim.ID)
	if err != nil || reopened.Status != agent.WorkItemOpen {
		t.Fatalf("recovered item status = %q (%v), want open while attempts remain", reopened.Status, err)
	}
	reclaimed, ok, err := queue.Claim(ctx, "new-worker", time.Minute)
	if err != nil || !ok || reclaimed.ID != retryItem.ID {
		t.Fatalf("reclaim = %#v, %v", reclaimed, err)
	}
	if reclaimed.LeaseEpoch <= oldRun.LeaseEpoch {
		t.Fatalf("reclaim epoch = %d, want a higher epoch than the crashed run's %d", reclaimed.LeaseEpoch, oldRun.LeaseEpoch)
	}
	newRun, err := runtimeRepo.CreateRun(ctx, agent.AgentRun{
		ProfileKey:    "clarification_host",
		WorkItemID:    &reclaimed.ID,
		Status:        agent.AgentRunRunning,
		OwnerInstance: "new-worker",
		LeaseEpoch:    reclaimed.LeaseEpoch,
		Usage:         map[string]int{},
		StartedAt:     testTime,
	})
	if err != nil {
		t.Fatalf("CreateRun(new): %v", err)
	}

	crashed, err := runtimeRepo.ListCrashedRuns(ctx, 50)
	if err != nil {
		t.Fatalf("ListCrashedRuns: %v", err)
	}
	if len(crashed) != 1 || crashed[0].ID != oldRun.ID {
		t.Fatalf("crashed runs = %#v, want only the taken-over run %d", crashed, oldRun.ID)
	}
	if crashed[0].LeaseEpoch != oldRun.LeaseEpoch || crashed[0].WorkItemID != retryItem.ID {
		t.Fatalf("crashed run identity = %#v, want the stored epoch and work item", crashed[0])
	}
	// agent_run.turns is only written by the terminal entry, so a crashed row
	// still carries the 0 CreateRun inserted: the sweep must carry the recorded
	// counters, never invent a turn count the run never persisted.
	if crashed[0].Turns != 0 || crashed[0].Usage["prompt_tokens"] != 42 {
		t.Fatalf("crashed run counters = %#v, want the recorded counters carried to the terminal write", crashed[0])
	}

	// The live run and the new attempt are untouched by a sweep.
	report, err := agentapp.NewRunCrashSweeper(runtimeRepo, nil, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)), crashSweepTestClock{now: testTime}).Sweep(ctx)
	if err != nil || report.Runs != 1 {
		t.Fatalf("sweep = %#v (%v), want only the orphaned attempt recovered", report, err)
	}
	if status, _, _ := readRunRow(t, pool, liveRun.ID); status != string(agent.AgentRunRunning) {
		t.Fatalf("live run status = %q, want running", status)
	}
	if status, _, _ := readRunRow(t, pool, newRun.ID); status != string(agent.AgentRunRunning) {
		t.Fatalf("new attempt run status = %q, want running", status)
	}
	if state := readMessageStreamState(t, pool, oldHead.ID); state != agent.MessageStreamStateInterrupted {
		t.Fatalf("orphaned transcript stream_state = %q, want interrupted", state)
	}
}

// TestFinishRunCannotOverwriteATerminalRun pins the terminal fence the crash sweep
// depends on. agent_run.lease_epoch is never bumped after CreateRun, so an attempt
// whose WORK ITEM lease was recovered can still write its own terminal; the sweep
// selects on status='running' and writes a few round trips later. Without a
// running-only condition on the single terminal entry, the sweep could overwrite
// a terminal the attempt itself wrote in that window — a second-writer truth hole.
// The expectation is the terminal-immutability contract shared with
// UpdateMessage's frozen transcript row, not observed repository output.
func TestFinishRunCannotOverwriteATerminalRun(t *testing.T) {
	pool := testutil.NewDB(t)
	runtimeRepo := agentpg.NewRuntimeRepository(pool, newIDs(t))
	ctx := context.Background()
	run, err := runtimeRepo.CreateRun(ctx, agent.AgentRun{
		ProfileKey:    "clarification_host",
		Status:        agent.AgentRunRunning,
		OwnerInstance: "worker-1",
		LeaseEpoch:    1,
		Usage:         map[string]int{},
		StartedAt:     testTime,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	finishedAt := testTime.Add(time.Minute)
	reason := "completed"
	if err := runtimeRepo.FinishRun(ctx, agent.AgentRun{
		ID: run.ID, LeaseEpoch: run.LeaseEpoch, Status: agent.AgentRunDone,
		TerminalReason: &reason, Turns: 3, Usage: map[string]int{"prompt_tokens": 10},
		FinishedAt: &finishedAt,
	}); err != nil {
		t.Fatalf("FinishRun(done): %v", err)
	}

	// The sweep's own terminal shape, same id and same (never-bumped) epoch.
	sweepReason := "interrupted"
	sweepAt := finishedAt.Add(time.Hour)
	err = runtimeRepo.FinishRun(ctx, agent.AgentRun{
		ID: run.ID, LeaseEpoch: run.LeaseEpoch, Status: agent.AgentRunInterrupted,
		TerminalReason: &sweepReason, FinishedAt: &sweepAt,
	})
	if !errors.Is(err, agent.ErrLeaseLost) {
		t.Fatalf("re-finish of a terminal run = %v, want the fenced ErrLeaseLost", err)
	}
	status, storedReason, storedAt := readRunRow(t, pool, run.ID)
	if status != string(agent.AgentRunDone) || storedReason != reason {
		t.Fatalf("terminal run = %q/%q, want the attempt's own done/%q preserved", status, storedReason, reason)
	}
	if storedAt == nil || !storedAt.Equal(finishedAt) {
		t.Fatalf("finished_at = %v, want the original %v", storedAt, finishedAt)
	}
}

// TestListCrashedRunsRejectsAnUnboundedList pins the bounded read: the sweep must
// never ask for an unbounded result set.
func TestListCrashedRunsRejectsAnUnboundedList(t *testing.T) {
	pool := testutil.NewDB(t)
	runtimeRepo := agentpg.NewRuntimeRepository(pool, newIDs(t))
	_, err := runtimeRepo.ListCrashedRuns(context.Background(), 0)
	if err == nil {
		t.Fatal("ListCrashedRuns(0) was accepted; the sweep read must be bounded")
	}
	var typed *platform.Error
	if !errors.As(err, &typed) || typed.Status != http.StatusBadRequest {
		t.Fatalf("ListCrashedRuns(0) error = %v, want a typed 400", err)
	}
}
