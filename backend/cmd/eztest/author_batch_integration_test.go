//go:build integration

package main

// v0.2.4-H T10 (spec §3.6 H-F5/H-F6, SC-E4 "无永久 pending"): the real-DB legs.
//
// H-F6 — the judge scheduling predicate now reads the AUTHOR BATCH settle state
// of the requirement version through a targeted work_item port. Three legs, all
// against the real assembly (real queue, real design graph, real planner
// reconcile, real version_fact escalation, real HTTP override route):
//
//	① no author batch      -> the judge IS enqueued (drafts authored by another
//	                          path must never strand in pending)
//	② author batch in flight -> the judge is deferred, no escalation fact
//	②a queued / waiting_human -> deferred FOREVER without escalation (review I1:
//	                          no lease means no progress clock, and both waits
//	                          are unbounded and normal)
//	③ lease-holding run stalled past its own attempt allowance -> judge_exhausted
//	                          per pending draft, review_state judge_unavailable,
//	                          human override reachable (mirror of the DEV-E3
//	                          invariant)
//
// H-F5 — EnsureCoverageRepair derives its generation key from the TARGETED
// design.requirement source. The busy-version leg pushes the older authoring
// generations out of the capped newest-500 ListByVersion window with 550 newer
// terminal rows and proves the targeted read still counts them (the capped
// derivation is computed side by side as the real bad sample).
//
// Run: cd backend && go test -tags=integration -p 1 ./cmd/eztest/ \
//        -run 'TestJudgeAuthorBatch|TestCoverageRepairGeneration' -count=1 -v

import (
	"context"
	"fmt"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	agentpg "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// poolExecDone settles one work item as a terminal generation. It is a fixture
// shortcut for the FILLER rows only (the legs that assert lifecycle semantics
// drive the real claim/run/fail/complete predicates instead).
func poolExecDone(ctx context.Context, pool *pgxpool.Pool, id int64) error {
	_, err := pool.Exec(ctx, `UPDATE work_item SET status = 'done', updated_at = now() WHERE id = $1`, id)
	return err
}

// authorBatchTestReader is the production reader over the real work_item table.
func authorBatchTestReader(h *judgeLoopHarness) *authorBatchReader {
	return &authorBatchReader{source: &authorBatchSQLSource{pool: h.pool}}
}

// seedOpenAuthorGeneration enqueues one MORE design.requirement generation of
// the requirement version through the real queue, with the production payload
// and the production repair-key shape. It models an authoring generation that
// is still owed work while the coverage union is already complete.
func seedOpenAuthorGeneration(h *judgeLoopHarness, reqID, reqVerID int64) agent.WorkItem {
	h.t.Helper()
	item, created, err := h.queue.Ensure(h.ctx(), agent.NewWorkItem{
		VersionID: h.versionID,
		Team:      string(agent.TeamDesign),
		Type:      designapp.TaskTypeDesignRequirement,
		DedupeKey: fmt.Sprintf("design:%d:%d:repair:1", reqID, reqVerID),
		Payload: map[string]any{
			"requirement_id":         platformid.Format(reqID),
			"requirement_version_id": platformid.Format(reqVerID),
			"reason":                 "ac_gap",
		},
	})
	if err != nil || !created {
		h.t.Fatalf("seed open author generation: created=%v err=%v", created, err)
	}
	return item
}

// judgeExhaustedFactCount counts the current judge_exhausted facts of the
// version (the real append-only fact table, not a projection).
func judgeExhaustedFactCount(h *judgeLoopHarness) int {
	h.t.Helper()
	var count int
	if err := h.pool.QueryRow(h.ctx(),
		`SELECT count(*) FROM version_fact WHERE version_id = $1 AND kind = $2 AND superseded_by IS NULL`,
		h.versionID, judgeExhaustedFactKind).Scan(&count); err != nil {
		h.t.Fatalf("count judge_exhausted facts: %v", err)
	}
	return count
}

// TestJudgeAuthorBatchStateSQLIntegration pins the port adapter against the real
// work_item table and the REAL queue transitions: no rows is NoBatch (not "all
// terminal"), an open generation is InFlight, a retryable failure is still
// InFlight, and a generation driven to dead through the real claim/fail/retry
// path settles the batch. Rows of another requirement version never count.
func TestJudgeAuthorBatchStateSQLIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	ids, err := platformid.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	versionID := ids.Next()
	const reqID, reqVerID = int64(10), int64(100)
	queue := agentapp.NewWorkQueue(agentpg.NewWorkItemRepository(pool, ids), clock.System{}, agentapp.DefaultLease, agentapp.DefaultMaxAttempts)
	reader := &authorBatchReader{source: &authorBatchSQLSource{pool: pool}}

	want := func(label string, state designapp.AuthorBatchState) {
		t.Helper()
		got, err := reader.AuthorBatchState(ctx, versionID, reqVerID)
		if err != nil {
			t.Fatalf("%s: AuthorBatchState: %v", label, err)
		}
		if got != state {
			t.Fatalf("%s: author batch state = %q, want %q", label, got, state)
		}
	}
	want("no author batch", designapp.AuthorBatchNoBatch)
	if deadline, err := reader.AuthorBatchStallDeadline(ctx, versionID, reqVerID); err != nil || !deadline.IsZero() {
		t.Fatalf("no batch stall deadline = %v (err %v), want zero", deadline, err)
	}

	payload := map[string]any{
		"requirement_id":         platformid.Format(reqID),
		"requirement_version_id": platformid.Format(reqVerID),
	}
	item, created, err := queue.Ensure(ctx, agent.NewWorkItem{
		VersionID: versionID, Team: string(agent.TeamDesign), Type: designapp.TaskTypeDesignRequirement,
		DedupeKey: fmt.Sprintf("design:%d:%d", reqID, reqVerID), Payload: payload,
	})
	if err != nil || !created {
		t.Fatalf("ensure author generation: created=%v err=%v", created, err)
	}
	want("open generation", designapp.AuthorBatchInFlight)
	// Review I1: a QUEUED row holds no lease, so its frozen updated_at must not
	// start a stall clock — however long it waits for a worker.
	if deadline, err := reader.AuthorBatchStallDeadline(ctx, versionID, reqVerID); err != nil || !deadline.IsZero() {
		t.Fatalf("queued generation stall deadline = %v (err %v), want zero (no lease, no clock)", deadline, err)
	}

	// A row of ANOTHER requirement version must not settle or stall this batch.
	if _, _, err := queue.Ensure(ctx, agent.NewWorkItem{
		VersionID: versionID, Team: string(agent.TeamDesign), Type: designapp.TaskTypeDesignRequirement,
		DedupeKey: fmt.Sprintf("design:%d:%d", reqID, reqVerID+1),
		Payload: map[string]any{
			"requirement_id":         platformid.Format(reqID),
			"requirement_version_id": platformid.Format(reqVerID + 1),
		},
	}); err != nil {
		t.Fatalf("ensure other requirement version: %v", err)
	}
	want("another requirement version is ignored", designapp.AuthorBatchInFlight)

	// REAL retry path: claim -> running -> retryable fail. Below the attempt cap
	// the queue still owes the batch its drafts.
	claimed, ok, err := queue.ClaimForTypes(ctx, "author-batch-test", []string{designapp.TaskTypeDesignRequirement}, 0)
	if err != nil || !ok {
		t.Fatalf("claim author generation: ok=%v err=%v", ok, err)
	}
	if err := queue.MarkRunning(ctx, claimed.ID, claimed.LeaseEpoch); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if err := queue.Fail(ctx, claimed.ID, claimed.LeaseEpoch, fmt.Errorf("author run failed"), true); err != nil {
		t.Fatalf("fail author generation: %v", err)
	}
	var status string
	var attempts, maxAttempts int
	if err := pool.QueryRow(ctx, `SELECT status, attempts, max_attempts FROM work_item WHERE id = $1`, item.ID).
		Scan(&status, &attempts, &maxAttempts); err != nil {
		t.Fatalf("read author row: %v", err)
	}
	if attempts >= maxAttempts {
		t.Fatalf("fixture precondition broken: attempts=%d/%d, want a retryable failure", attempts, maxAttempts)
	}
	want("retryable failure still owes the batch", designapp.AuthorBatchInFlight)
	// The retry is queued again (open, waiting out its backoff and a free
	// worker): it holds no lease, so it must not start a stall clock either.
	if deadline, err := reader.AuthorBatchStallDeadline(ctx, versionID, reqVerID); err != nil || !deadline.IsZero() {
		t.Fatalf("retry-queued stall deadline = %v (err %v), want zero", deadline, err)
	}

	// Drive it to the attempt cap through the same real predicates.
	for {
		next, ok, err := queue.ClaimForTypes(ctx, "author-batch-test", []string{designapp.TaskTypeDesignRequirement}, 0)
		if err != nil {
			t.Fatalf("claim retry: %v", err)
		}
		if !ok {
			if _, err := pool.Exec(ctx,
				`UPDATE work_item SET available_at = now() WHERE version_id = $1 AND status = 'open'`, versionID); err != nil {
				t.Fatalf("fast-forward backoff: %v", err)
			}
			continue
		}
		if next.ID != item.ID {
			// The other requirement version's row: settle it out of the way.
			if err := queue.MarkRunning(ctx, next.ID, next.LeaseEpoch); err != nil {
				t.Fatalf("mark running other version: %v", err)
			}
			if err := queue.Complete(ctx, next.ID, next.LeaseEpoch); err != nil {
				t.Fatalf("complete other version: %v", err)
			}
			continue
		}
		if err := queue.MarkRunning(ctx, next.ID, next.LeaseEpoch); err != nil {
			t.Fatalf("mark running retry: %v", err)
		}
		if err := queue.Fail(ctx, next.ID, next.LeaseEpoch, fmt.Errorf("author run failed"), true); err != nil {
			t.Fatalf("fail retry: %v", err)
		}
		if err := pool.QueryRow(ctx, `SELECT status, attempts, max_attempts FROM work_item WHERE id = $1`, item.ID).
			Scan(&status, &attempts, &maxAttempts); err != nil {
			t.Fatalf("read author row: %v", err)
		}
		if status == string(agent.WorkItemDead) {
			break
		}
		if _, err := pool.Exec(ctx,
			`UPDATE work_item SET available_at = now() WHERE id = $1 AND status = 'open'`, item.ID); err != nil {
			t.Fatalf("fast-forward backoff: %v", err)
		}
	}
	want("dead generation settles the batch", designapp.AuthorBatchAllTerminal)
	if deadline, err := reader.AuthorBatchStallDeadline(ctx, versionID, reqVerID); err != nil || !deadline.IsZero() {
		t.Fatalf("settled batch stall deadline = %v (err %v), want zero", deadline, err)
	}
}

// TestJudgeAuthorBatchNoBatchEnqueuesRealDB is H-F6 leg ①: the coverage union is
// complete but NO design.requirement work item is bound to this requirement
// version (the drafts came from another authoring path). The judge must still be
// enqueued, run, pass and be confirmable — a "false" here would be a NEW
// permanent pending introduced by the gate itself.
func TestJudgeAuthorBatchNoBatchEnqueuesRealDB(t *testing.T) {
	h := newJudgeLoopHarness(t)
	reqID, reqVerID := h.setupDesigningRequirement(2)
	h.setAuthorMode("full")
	h.setJudgeMode("pass")
	h.requireRunDone(h.runWorkItems(designapp.TaskTypeDesignRequirement), 1, "author")

	// Model "drafts authored by another path": the requirement version has no
	// authoring work item at all. The drafts and the coverage union stay real.
	// (work_item_attempt is the only FK child of work_item.)
	if _, err := h.pool.Exec(h.ctx(), `
		DELETE FROM work_item_attempt WHERE work_item_id IN (
			SELECT id FROM work_item WHERE version_id = $1 AND type = $2
		)`, h.versionID, designapp.TaskTypeDesignRequirement); err != nil {
		t.Fatalf("remove authoring attempts: %v", err)
	}
	if _, err := h.pool.Exec(h.ctx(),
		`DELETE FROM work_item WHERE version_id = $1 AND type = $2`,
		h.versionID, designapp.TaskTypeDesignRequirement); err != nil {
		t.Fatalf("remove authoring rows: %v", err)
	}
	reader := authorBatchTestReader(h)
	state, err := reader.AuthorBatchState(h.ctx(), h.versionID, reqVerID)
	if err != nil || state != designapp.AuthorBatchNoBatch {
		t.Fatalf("author batch state = %q (err %v), want no_batch", state, err)
	}

	h.reconcile()
	judges := h.workItemsOfType(designapp.TaskTypeDesignJudge)
	if len(judges) != 1 {
		t.Fatalf("judge items = %d, want 1 (no author batch must not block the judge)", len(judges))
	}
	if got := judgeExhaustedFactCount(h); got != 0 {
		t.Fatalf("judge_exhausted facts = %d, want 0 on the no-batch leg", got)
	}
	h.requireRunDone(h.runWorkItems(designapp.TaskTypeDesignJudge), 1, "judge")
	for _, card := range h.draftCards() {
		if state := h.reviewState(card.CaseID, card.CurrentVersionID); state != design.ReviewStatePass {
			t.Fatalf("review state = %s, want pass (the draft was never stranded)", state)
		}
	}
	if result := h.confirmRequirement(reqID); len(result.Confirmed) == 0 {
		t.Fatal("no cases confirmed on the no-author-batch leg")
	}
}

// TestJudgeAuthorBatchInFlightDefersRealDB is H-F6 leg ②: an authoring generation
// of this requirement version is still owed work, so the judge is deferred (it
// would review a partial draft set) and NO escalation fact is written.
func TestJudgeAuthorBatchInFlightDefersRealDB(t *testing.T) {
	h := newJudgeLoopHarness(t)
	reqID, reqVerID := h.setupDesigningRequirement(2)
	h.setAuthorMode("full")
	h.setJudgeMode("pass")
	h.requireRunDone(h.runWorkItems(designapp.TaskTypeDesignRequirement), 1, "author")
	seedOpenAuthorGeneration(h, reqID, reqVerID)

	reader := authorBatchTestReader(h)
	state, err := reader.AuthorBatchState(h.ctx(), h.versionID, reqVerID)
	if err != nil || state != designapp.AuthorBatchInFlight {
		t.Fatalf("author batch state = %q (err %v), want in_flight", state, err)
	}

	h.reconcile()
	if items := h.workItemsOfType(designapp.TaskTypeDesignJudge); len(items) != 0 {
		t.Fatalf("judge items = %d, want 0 while the author batch is in flight", len(items))
	}
	if got := judgeExhaustedFactCount(h); got != 0 {
		t.Fatalf("judge_exhausted facts = %d, want 0 (a fresh in-flight batch must not escalate)", got)
	}
	for _, card := range h.draftCards() {
		if state := h.reviewState(card.CaseID, card.CurrentVersionID); state != design.ReviewStatePending {
			t.Fatalf("review state = %s, want pending while the batch is in flight", state)
		}
	}
}

// TestJudgeAuthorBatchLeasedWithinAllowanceDefersRealDB is the T10 supplementary
// real-DB leg (review R7): an authoring generation that HOLDS A LEASE (real claim
// + MarkRunning) and is STILL WITHIN its attempt allowance must defer the judge
// with a real, future stall deadline and NO escalation. The InFlightDefers leg
// above seeds an OPEN row, which exercises the zero-deadline (no lease) branch and
// overlaps the queued leg; this leg is the distinct "lease-holding, clock live,
// not yet stalled" case that only a unit test covered before. It then proves the
// deferral is not a dead end: completing the running generation settles the batch
// and the judge is scheduled normally.
func TestJudgeAuthorBatchLeasedWithinAllowanceDefersRealDB(t *testing.T) {
	h := newJudgeLoopHarness(t)
	reqID, reqVerID := h.setupDesigningRequirement(2)
	h.setAuthorMode("full")
	h.setJudgeMode("pass")
	h.requireRunDone(h.runWorkItems(designapp.TaskTypeDesignRequirement), 1, "author")
	seeded := seedOpenAuthorGeneration(h, reqID, reqVerID)
	// REAL claim + MarkRunning: the row holds a lease and its updated_at is a live
	// progress clock, fresh (within the allowance) — not aged.
	claimed, ok, err := h.queue.ClaimForTypes(h.ctx(), "author-batch-within-allowance", []string{designapp.TaskTypeDesignRequirement}, 0)
	if err != nil || !ok || claimed.ID != seeded.ID {
		t.Fatalf("claim the seeded generation: ok=%v id=%d want=%d err=%v", ok, claimed.ID, seeded.ID, err)
	}
	if err := h.queue.MarkRunning(h.ctx(), claimed.ID, claimed.LeaseEpoch); err != nil {
		t.Fatalf("mark running: %v", err)
	}

	reader := authorBatchTestReader(h)
	if state, err := reader.AuthorBatchState(h.ctx(), h.versionID, reqVerID); err != nil || state != designapp.AuthorBatchInFlight {
		t.Fatalf("leased batch state = %q (err %v), want in_flight", state, err)
	}
	deadline, err := reader.AuthorBatchStallDeadline(h.ctx(), h.versionID, reqVerID)
	if err != nil || deadline.IsZero() {
		t.Fatalf("lease-holding row stall deadline = %v (err %v), want a real deadline (a live clock exists)", deadline, err)
	}
	if !time.Now().Before(deadline) {
		t.Fatalf("fresh lease-holding row deadline %v is already past; the row must still be WITHIN its allowance", deadline)
	}

	for pass := 0; pass < 3; pass++ {
		h.reconcile()
	}
	if items := h.workItemsOfType(designapp.TaskTypeDesignJudge); len(items) != 0 {
		t.Fatalf("judge items = %d, want 0 while a leased author is still within its allowance", len(items))
	}
	if got := judgeExhaustedFactCount(h); got != 0 {
		t.Fatalf("judge_exhausted facts = %d, want 0 (a leased author within its allowance is NOT stalled)", got)
	}
	for _, card := range h.draftCards() {
		if state := h.reviewState(card.CaseID, card.CurrentVersionID); state != design.ReviewStatePending {
			t.Fatalf("draft %d review state = %s, want pending (no false escalation within the allowance)", card.CaseID, state)
		}
	}

	// Not a dead end: the running generation completes, the batch settles, the
	// judge is scheduled and the drafts pass review without any human override.
	if err := h.queue.Complete(h.ctx(), claimed.ID, claimed.LeaseEpoch); err != nil {
		t.Fatalf("complete the running generation: %v", err)
	}
	h.reconcile()
	if items := h.workItemsOfType(designapp.TaskTypeDesignJudge); len(items) != 1 {
		t.Fatalf("judge items after the leased author completed = %d, want 1", len(items))
	}
	h.requireRunDone(h.runWorkItems(designapp.TaskTypeDesignJudge), 1, "judge after leased author")
	for _, card := range h.draftCards() {
		if state := h.reviewState(card.CaseID, card.CurrentVersionID); state != design.ReviewStatePass {
			t.Fatalf("review state = %s, want pass (the in-allowance deferral cost nothing)", state)
		}
	}
}

// ageAuthorRow pushes ONLY the last-progress clock of one row back, leaving its
// real status/payload/attempts untouched (the same fixture technique the retry
// legs use for available_at).
func ageAuthorRow(h *judgeLoopHarness, id int64, age time.Duration) {
	h.t.Helper()
	if _, err := h.pool.Exec(h.ctx(),
		`UPDATE work_item SET updated_at = now() - make_interval(secs => $2) WHERE id = $1`,
		id, age.Seconds()); err != nil {
		h.t.Fatalf("age the authoring row clock: %v", err)
	}
}

// TestJudgeAuthorBatchQueuedNeverEscalatesRealDB is the review-I1 regression leg:
// an authoring generation that is merely QUEUED (open, never claimed) has a
// frozen updated_at — the Renew statement only advances it for lease-holding
// rows. However long it waits for a worker, it must NOT be read as "stalled":
// escalating here would stamp judge_exhausted over drafts whose author had not
// even started, and that escalation outranks a later pass. The leg then proves
// the deferral is not a dead end: once the queued generation actually runs, the
// judge is scheduled normally.
func TestJudgeAuthorBatchQueuedNeverEscalatesRealDB(t *testing.T) {
	h := newJudgeLoopHarness(t)
	reqID, reqVerID := h.setupDesigningRequirement(2)
	h.setAuthorMode("full")
	h.setJudgeMode("pass")
	h.requireRunDone(h.runWorkItems(designapp.TaskTypeDesignRequirement), 1, "author")
	queued := seedOpenAuthorGeneration(h, reqID, reqVerID)

	// Ten times the whole default-cap allowance: a queue wait has no upper bound.
	ageAuthorRow(h, queued.ID, 10*3*designapp.AuthorBatchStallWindowPerAttempt)
	reader := authorBatchTestReader(h)
	if state, err := reader.AuthorBatchState(h.ctx(), h.versionID, reqVerID); err != nil || state != designapp.AuthorBatchInFlight {
		t.Fatalf("queued batch state = %q (err %v), want in_flight", state, err)
	}
	if deadline, err := reader.AuthorBatchStallDeadline(h.ctx(), h.versionID, reqVerID); err != nil || !deadline.IsZero() {
		t.Fatalf("queued batch stall deadline = %v (err %v), want zero (no lease, no clock)", deadline, err)
	}

	for pass := 0; pass < 3; pass++ {
		h.reconcile()
	}
	if items := h.workItemsOfType(designapp.TaskTypeDesignJudge); len(items) != 0 {
		t.Fatalf("judge items = %d, want 0 while the authoring generation is queued", len(items))
	}
	if got := judgeExhaustedFactCount(h); got != 0 {
		t.Fatalf("judge_exhausted facts = %d, want 0 (a queued author is NOT a stalled author)", got)
	}
	drafts := h.draftCards()
	for _, card := range drafts {
		if state := h.reviewState(card.CaseID, card.CurrentVersionID); state != design.ReviewStatePending {
			t.Fatalf("draft %d review state = %s, want pending (no false escalation)", card.CaseID, state)
		}
	}

	// Not a dead end: the queued generation runs, the batch settles, the judge
	// is scheduled and the drafts pass review without any human override.
	h.requireRunDone(h.runWorkItems(designapp.TaskTypeDesignRequirement), 1, "queued author")
	h.reconcile()
	if items := h.workItemsOfType(designapp.TaskTypeDesignJudge); len(items) != 1 {
		t.Fatalf("judge items after the queued author ran = %d, want 1", len(items))
	}
	h.requireRunDone(h.runWorkItems(designapp.TaskTypeDesignJudge), 1, "judge after queue")
	for _, card := range h.draftCards() {
		if state := h.reviewState(card.CaseID, card.CurrentVersionID); state != design.ReviewStatePass {
			t.Fatalf("review state = %s, want pass (the queue wait cost nothing)", state)
		}
	}
}

// TestJudgeAuthorBatchWaitingHumanNeverEscalatesRealDB is the second review-I1
// leg: a row parked for a human holds no lease and its clock is frozen, but a
// human taking more than an hour to answer is entirely normal. It must never be
// escalated. (design.requirement has no waiting_human writer today — the state
// is set directly as a fixture, exactly as the retryable-failure leg does.)
func TestJudgeAuthorBatchWaitingHumanNeverEscalatesRealDB(t *testing.T) {
	h := newJudgeLoopHarness(t)
	reqID, reqVerID := h.setupDesigningRequirement(2)
	h.setAuthorMode("full")
	h.setJudgeMode("pass")
	h.requireRunDone(h.runWorkItems(designapp.TaskTypeDesignRequirement), 1, "author")
	parked := seedOpenAuthorGeneration(h, reqID, reqVerID)
	if _, err := h.pool.Exec(h.ctx(),
		`UPDATE work_item SET status = $2 WHERE id = $1`, parked.ID, string(agent.WorkItemWaitingHuman)); err != nil {
		t.Fatalf("park the authoring row for a human: %v", err)
	}
	ageAuthorRow(h, parked.ID, 10*3*designapp.AuthorBatchStallWindowPerAttempt)

	reader := authorBatchTestReader(h)
	if state, err := reader.AuthorBatchState(h.ctx(), h.versionID, reqVerID); err != nil || state != designapp.AuthorBatchInFlight {
		t.Fatalf("parked batch state = %q (err %v), want in_flight", state, err)
	}
	if deadline, err := reader.AuthorBatchStallDeadline(h.ctx(), h.versionID, reqVerID); err != nil || !deadline.IsZero() {
		t.Fatalf("parked batch stall deadline = %v (err %v), want zero (a human wait has no deadline)", deadline, err)
	}
	h.reconcile()
	if items := h.workItemsOfType(designapp.TaskTypeDesignJudge); len(items) != 0 {
		t.Fatalf("judge items = %d, want 0 while the batch awaits a human", len(items))
	}
	if got := judgeExhaustedFactCount(h); got != 0 {
		t.Fatalf("judge_exhausted facts = %d, want 0 (a slow human is NOT a stalled run)", got)
	}
}

// TestJudgeAuthorBatchStalledEscalatesRealDB is H-F6 leg ③: an authoring run that
// HOLDS A LEASE stops making progress (a worker whose heartbeat died, a lease
// nobody recovers). Past its own attempt allowance the scheduler escalates EVERY
// pending draft through the real judge_exhausted fact (review_state
// judge_unavailable) and the human decision point is reachable — the mirror of
// DEV-E3's "in flight, otherwise escalate" invariant, so the deferral can never
// become a permanent pending.
func TestJudgeAuthorBatchStalledEscalatesRealDB(t *testing.T) {
	h := newJudgeLoopHarness(t)
	reqID, reqVerID := h.setupDesigningRequirement(2)
	h.setAuthorMode("full")
	h.setJudgeMode("pass")
	h.requireRunDone(h.runWorkItems(designapp.TaskTypeDesignRequirement), 1, "author")
	seeded := seedOpenAuthorGeneration(h, reqID, reqVerID)
	// REAL claim + MarkRunning: the row now holds a lease, so its updated_at is
	// a live progress clock — the only state in which "no progress" means stall.
	claimed, ok, err := h.queue.ClaimForTypes(h.ctx(), "author-batch-stall", []string{designapp.TaskTypeDesignRequirement}, 0)
	if err != nil || !ok || claimed.ID != seeded.ID {
		t.Fatalf("claim the seeded generation: ok=%v id=%d want=%d err=%v", ok, claimed.ID, seeded.ID, err)
	}
	if err := h.queue.MarkRunning(h.ctx(), claimed.ID, claimed.LeaseEpoch); err != nil {
		t.Fatalf("mark running: %v", err)
	}

	// Fast-forward ONLY the progress clock, past this row's own allowance.
	ageAuthorRow(h, claimed.ID, 10*3*designapp.AuthorBatchStallWindowPerAttempt)
	reader := authorBatchTestReader(h)
	if state, err := reader.AuthorBatchState(h.ctx(), h.versionID, reqVerID); err != nil || state != designapp.AuthorBatchInFlight {
		t.Fatalf("stalled batch state = %q (err %v), want in_flight (a stalled row is NOT terminal)", state, err)
	}
	deadline, err := reader.AuthorBatchStallDeadline(h.ctx(), h.versionID, reqVerID)
	if err != nil || deadline.IsZero() {
		t.Fatalf("lease-holding stalled row deadline = %v (err %v), want a real deadline", deadline, err)
	}
	if !time.Now().After(deadline) {
		t.Fatalf("fixture precondition broken: stall deadline %v is still in the future", deadline)
	}

	drafts := h.draftCards()
	if len(drafts) == 0 {
		t.Fatal("fixture precondition broken: no drafts to escalate")
	}
	h.reconcile()
	if items := h.workItemsOfType(designapp.TaskTypeDesignJudge); len(items) != 0 {
		t.Fatalf("judge items = %d, want 0 (a stalled batch escalates instead of judging)", len(items))
	}
	if got := judgeExhaustedFactCount(h); got != len(drafts) {
		t.Fatalf("judge_exhausted facts = %d, want one per pending draft (%d)", got, len(drafts))
	}
	for _, card := range drafts {
		if state := h.reviewState(card.CaseID, card.CurrentVersionID); state != design.ReviewStateJudgeUnavailable {
			t.Fatalf("draft %d review state = %s, want judge_unavailable", card.CaseID, state)
		}
	}
	// The escalation event is emitted once per NEW fact, never per pass.
	events := h.eventCount("design.judge_exhausted")
	if events != len(drafts) {
		t.Fatalf("design.judge_exhausted events = %d, want %d", events, len(drafts))
	}
	h.reconcile()
	if again := h.eventCount("design.judge_exhausted"); again != events {
		t.Fatalf("degradation events after a repeat pass = %d, want %d (creation transition only)", again, events)
	}

	// The human decision point is reachable: override, then confirm.
	for _, card := range drafts {
		rec := h.overrideRoute(card.CaseID, "admin", "作者批次长期未结算，人工决断")
		if rec.Code != 200 {
			t.Fatalf("override route status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
	}
	if result := h.confirmRequirement(reqID); len(result.Confirmed) == 0 {
		t.Fatal("no cases confirmed after the stalled-batch escalation was overridden")
	}
}

// TestCoverageRepairGenerationBusyVersion is the H-F5 real-DB leg: a busy version
// pushes the older design.requirement generations out of the capped newest-500
// ListByVersion window. The capped derivation undercounts them and derives a key
// that COLLIDES with a terminal generation (Ensure then reports created=false
// and the requirement is never repaired again); the targeted source counts every
// generation of the requirement version regardless of the window.
func TestCoverageRepairGenerationBusyVersion(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	ids, err := platformid.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	versionID := ids.Next()
	const reqID, reqVerID = int64(3005), int64(3006)
	queue := agentapp.NewWorkQueue(agentpg.NewWorkItemRepository(pool, ids), clock.System{}, agentapp.DefaultLease, agentapp.DefaultMaxAttempts)
	baseKey := fmt.Sprintf("design:%d:%d", reqID, reqVerID)
	payload := map[string]any{
		"requirement_id":         platformid.Format(reqID),
		"requirement_version_id": platformid.Format(reqVerID),
	}

	// Two TERMINAL authoring generations of the requirement version.
	for i, key := range []string{baseKey, baseKey + ":repair:1"} {
		item, created, err := queue.Ensure(ctx, agent.NewWorkItem{
			VersionID: versionID, Team: string(agent.TeamDesign), Type: designapp.TaskTypeDesignRequirement,
			DedupeKey: key, Payload: payload,
		})
		if err != nil || !created {
			t.Fatalf("ensure authoring generation %d: created=%v err=%v", i, created, err)
		}
		if err := poolExecDone(ctx, pool, item.ID); err != nil {
			t.Fatalf("settle authoring generation %d: %v", i, err)
		}
	}

	// 550 NEWER terminal rows of another type: the older authoring generations
	// fall out of the newest-500 page (ListByVersion orders by id DESC).
	const filler = 550
	for i := 0; i < filler; i++ {
		item, created, err := queue.Ensure(ctx, agent.NewWorkItem{
			VersionID: versionID, Team: string(agent.TeamDesign), Type: designapp.TaskTypeDesignCritic,
			DedupeKey: fmt.Sprintf("critic:%d:%d", reqVerID, i), Payload: payload,
		})
		if err != nil || !created {
			t.Fatalf("ensure filler %d: created=%v err=%v", i, created, err)
		}
		if err := poolExecDone(ctx, pool, item.ID); err != nil {
			t.Fatalf("settle filler %d: %v", i, err)
		}
	}

	window, err := queue.ListByVersion(ctx, versionID, agentapp.MaxListLimit)
	if err != nil {
		t.Fatalf("list capped window: %v", err)
	}
	if len(window) != agentapp.MaxListLimit {
		t.Fatalf("capped window size = %d, want %d", len(window), agentapp.MaxListLimit)
	}
	cappedKey, cappedExhausted, cappedGeneration := coverageRepairDedupeKey(window, baseKey, reqVerID)
	if cappedKey != baseKey || cappedExhausted || cappedGeneration != 0 {
		t.Fatalf("fixture precondition broken: the capped derivation = %q (exhausted=%v, generation=%d), want the colliding base key %q "+
			"(the busy version must push BOTH authoring generations out of the window)", cappedKey, cappedExhausted, cappedGeneration, baseKey)
	}

	enqueuer := designTaskEnqueuer{queue: queue, coverageRepairs: &authorBatchSQLSource{pool: pool}}
	created, err := enqueuer.EnsureCoverageRepair(ctx, designapp.TaskInput{
		VersionID: versionID, Team: string(agent.TeamDesign), Type: designapp.TaskTypeDesignRequirement,
		DedupeKey: baseKey, Payload: payload,
	}, reqVerID)
	if err != nil {
		t.Fatalf("EnsureCoverageRepair: %v", err)
	}
	if !created {
		t.Fatal("the targeted generation counter must create the third authoring generation on a busy version")
	}
	var key string
	if err := pool.QueryRow(ctx,
		`SELECT dedupe_key FROM work_item WHERE version_id = $1 AND type = $2 AND status = 'open'`,
		versionID, designapp.TaskTypeDesignRequirement).Scan(&key); err != nil {
		t.Fatalf("read the created generation: %v", err)
	}
	if key != baseKey+":repair:2" {
		t.Fatalf("created generation key = %q, want %q", key, baseKey+":repair:2")
	}

	// Idempotent: the open generation is never stampeded by a second pass.
	again, err := enqueuer.EnsureCoverageRepair(ctx, designapp.TaskInput{
		VersionID: versionID, Team: string(agent.TeamDesign), Type: designapp.TaskTypeDesignRequirement,
		DedupeKey: baseKey, Payload: payload,
	}, reqVerID)
	if err != nil || again {
		t.Fatalf("second pass created=%v err=%v, want no duplicate of the in-flight generation", again, err)
	}
}
