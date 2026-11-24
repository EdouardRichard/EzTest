//go:build integration

package main

// v0.2.4-E final fix wave (whole-branch review Critical #1 / Important #2).
//
// #1 — the reconcile/event loop must not sustain itself. Every persisted event
// wakes a full reconcile pass in production (main.go eventSvc.Observe(wake) ->
// reconciler.Notify -> runPass -> Planner.Reconcile -> ensureJudgeTasks). Judge
// exhaustion and a requested rework are DESIGNED expected outcomes that persist
// across many passes, so re-publishing them on every pass turns the control
// plane into a permanent event/SSE loop. These tests drive the real assembly to
// those states and then keep reconciling — with the production wake edge wired
// through the real event service — and require the event count to stay stable
// and the wake loop to settle.
//
// #2 — the judge generation counters must not read the capped newest-500
// ListByVersion window. A busy version pushes the older design.judge rows out of
// that page, the sequence undercounts back to 0, the derived dedupe key collides
// with the terminal generation, Ensure reports created=false without an error,
// and the draft set stays permanently pending (spec SC-E4 "无永久 pending").
//
// Run: cd backend && go test -tags=integration -p 1 ./cmd/eztest/ \
//        -run 'TestJudgeExhaustedEvent|TestReworkRequestedEvent|TestJudgeGenerationBusy' -v
//      (EZTEST_TEST_DATABASE_URL must point at the isolated test database.)

import (
	"context"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	eventingdomain "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
)

// wakeChannel returns a buffered channel and installs the production wake edge:
// every persisted event signals one coalescing wake, exactly as main.go's
// `eventSvc.Observe(wake)` -> `reconciler.Notify` does. The channel is the
// reconciler's coalescing notify channel; drainReconcileWake is its runPass.
func (h *judgeLoopHarness) wakeChannel() chan struct{} {
	h.t.Helper()
	wake := make(chan struct{}, 4096)
	h.events.Observe(func(_ context.Context, _ eventingdomain.Event) error {
		select {
		case wake <- struct{}{}:
		default:
		}
		return nil
	})
	return wake
}

// drainWake discards every already-queued wake (the coalescing channel collapses
// them into one pass anyway).
func drainWake(wake chan struct{}) {
	for len(wake) > 0 {
		<-wake
	}
}

// drainReconcileWake runs reconcile passes while wakes keep arriving, up to
// maxPasses. It returns the number of passes run. A self-sustaining
// publish->wake->reconcile->publish loop never empties the channel and therefore
// returns maxPasses; a settled control plane returns early.
func (h *judgeLoopHarness) drainReconcileWake(wake chan struct{}, maxPasses int) int {
	h.t.Helper()
	passes := 0
	for passes < maxPasses {
		pending := len(wake)
		if pending == 0 {
			return passes
		}
		for index := 0; index < pending; index++ {
			<-wake
		}
		h.reconcile()
		passes++
	}
	return passes
}

// judgeWorkItemCount counts the version's design.judge rows with a TARGETED
// query. workItemsOfType goes through the capped ListByVersion window, which is
// exactly what the busy-version test overflows.
func (h *judgeLoopHarness) judgeWorkItemCount() int {
	h.t.Helper()
	var count int
	if err := h.pool.QueryRow(h.ctx(),
		`SELECT count(*) FROM work_item WHERE version_id = $1 AND type = 'design.judge'`,
		h.versionID).Scan(&count); err != nil {
		h.t.Fatalf("count design.judge work items: %v", err)
	}
	return count
}

// openJudgeWorkItemCount counts the design.judge rows that are still queued or
// running (a fresh generation the worker has not settled yet).
func (h *judgeLoopHarness) openJudgeWorkItemCount() int {
	h.t.Helper()
	var count int
	if err := h.pool.QueryRow(h.ctx(),
		`SELECT count(*) FROM work_item WHERE version_id = $1 AND type = 'design.judge'
		   AND status IN ('open', 'claimed', 'running', 'waiting_human')`,
		h.versionID).Scan(&count); err != nil {
		h.t.Fatalf("count open design.judge work items: %v", err)
	}
	return count
}

// pushTerminalWorkItemNoise inserts n terminal design.critic rows with ids NEWER
// than every existing row of the version, so the newest-500 ListByVersion page
// no longer contains the older design.judge generations. They are terminal and
// carry their own dedupe keys, so they change no scheduling predicate — only the
// width of the generic window.
func (h *judgeLoopHarness) pushTerminalWorkItemNoise(n int) {
	h.t.Helper()
	ids := make([]int64, 0, n)
	for index := 0; index < n; index++ {
		ids = append(ids, h.ids.Next())
	}
	if _, err := h.pool.Exec(h.ctx(), `
		INSERT INTO work_item (id, version_id, team, type, dedupe_key, status, payload)
		SELECT id, $2, 'design', 'design.critic', 'window-noise:' || id, 'done', '{}'::jsonb
		FROM unnest($1::bigint[]) AS id
	`, ids, h.versionID); err != nil {
		h.t.Fatalf("push work-item window noise: %v", err)
	}
}

// drainJudgeGenerations drives the bounded judge generations of the current
// draft set to exhaustion in judge error mode: every generation's run fails, the
// retries reach their attempt cap, and the next reconcile opens a new generation
// until the draft-set budget is spent and the escalation facts are written.
func (h *judgeLoopHarness) drainJudgeGenerations() int {
	h.t.Helper()
	totalFailed := 0
	for generation := 0; generation < 8; generation++ {
		h.reconcile()
		runs := h.runWorkItems("design.judge")
		if len(runs) == 0 {
			break
		}
		for _, run := range runs {
			if run.err == nil {
				h.t.Fatalf("judge run unexpectedly succeeded in error mode: %+v", run.result)
			}
			totalFailed++
		}
	}
	if totalFailed == 0 {
		h.t.Fatal("no judge runs were attempted")
	}
	return totalFailed
}

// TestJudgeExhaustedEventEmitsOnlyOnTransition is Critical #1(a): the
// judge_exhausted escalation FACT is idempotent (RecordFact dedupe), but the
// best-effort degradation EVENT was published on every exhausted reconcile pass.
// With the production wake edge wired, that is a self-sustaining loop: event ->
// wake -> reconcile -> event. The event must be emitted only when the escalation
// fact is newly created.
func TestJudgeExhaustedEventEmitsOnlyOnTransition(t *testing.T) {
	h := newJudgeLoopHarness(t)
	h.setupDesigningRequirement(1)
	h.setAuthorMode("full")
	h.setJudgeMode("error")

	h.requireRunDone(h.runWorkItems("design.requirement"), 1, "author")
	drafts := h.draftCards()
	if len(drafts) != 2 {
		t.Fatalf("author produced %d drafts, want 2", len(drafts))
	}
	h.drainJudgeGenerations()

	for _, card := range drafts {
		if state := h.reviewState(card.CaseID, card.CurrentVersionID); state != design.ReviewStateJudgeUnavailable {
			t.Fatalf("review state = %s, want judge_unavailable", state)
		}
	}
	wantEvents := len(drafts)
	if got := h.eventCount("design.judge_exhausted"); got != wantEvents {
		t.Fatalf("judge_exhausted events after exhaustion = %d, want %d", got, wantEvents)
	}

	// Wire the production wake edge and take one more pass in the exhausted
	// state. A re-emitting pass refills the wake channel forever.
	wake := h.wakeChannel()
	drainWake(wake)
	h.reconcile()
	if passes := h.drainReconcileWake(wake, 24); passes >= 24 {
		t.Fatalf("reconcile/event loop did not settle after %d passes: judge_exhausted is re-published every pass (events now %d)",
			passes, h.eventCount("design.judge_exhausted"))
	}
	if got := h.eventCount("design.judge_exhausted"); got != wantEvents {
		t.Fatalf("judge_exhausted events after the wake loop = %d, want %d (emit only on fact creation)", got, wantEvents)
	}

	// Independent of the wake edge: repeated reconciles in the exhausted state
	// must not append a single further event row.
	for pass := 0; pass < 3; pass++ {
		h.reconcile()
	}
	if got := h.eventCount("design.judge_exhausted"); got != wantEvents {
		t.Fatalf("judge_exhausted events after 3 extra reconciles = %d, want %d", got, wantEvents)
	}
	for _, card := range drafts {
		var count int
		if err := h.pool.QueryRow(h.ctx(),
			`SELECT count(*) FROM version_fact WHERE version_id = $1 AND kind = $2 AND key = $3 AND superseded_by IS NULL`,
			h.versionID, judgeExhaustedFactKind, judgeExhaustedFactKey(card.CaseID, card.CurrentVersionID)).Scan(&count); err != nil {
			t.Fatalf("read judge_exhausted fact: %v", err)
		}
		if count != 1 {
			t.Fatalf("judge_exhausted facts for case %d = %d, want 1", card.CaseID, count)
		}
	}
}

// TestReworkRequestedEventEmitsOnlyOnTransition is Critical #1(b): while a
// fail-within-budget draft exists, handleJudgeFail re-published
// design.rework_requested on every reconcile pass even though the rework work
// item already existed. The event must follow the work-item creation transition.
func TestReworkRequestedEventEmitsOnlyOnTransition(t *testing.T) {
	h := newJudgeLoopHarness(t)
	h.setupDesigningRequirement(1)
	h.setAuthorMode("full")
	h.setJudgeMode("fail")

	h.requireRunDone(h.runWorkItems("design.requirement"), 1, "author")
	h.reconcile()
	h.requireRunDone(h.runWorkItems("design.judge"), 1, "judge round 1")

	// This pass observes the fails and creates one rework per logical case.
	h.reconcile()
	if got := len(h.workItemsOfType(agentapp.WorkTypeDesignRework)); got != 2 {
		t.Fatalf("rework items = %d, want 2", got)
	}
	if got := h.eventCount("design.rework_requested"); got != 2 {
		t.Fatalf("rework_requested events = %d, want 2", got)
	}

	// The rework items are still in flight (the author has not run yet): every
	// further pass sees the same fail-within-budget draft and must stay silent.
	wake := h.wakeChannel()
	drainWake(wake)
	h.reconcile()
	if passes := h.drainReconcileWake(wake, 24); passes >= 24 {
		t.Fatalf("reconcile/event loop did not settle after %d passes: rework_requested is re-published while the rework is in flight (events now %d)",
			passes, h.eventCount("design.rework_requested"))
	}
	for pass := 0; pass < 3; pass++ {
		h.reconcile()
	}
	if got := h.eventCount("design.rework_requested"); got != 2 {
		t.Fatalf("rework_requested events after extra reconciles = %d, want 2 (emit only on work-item creation)", got)
	}
	if got := len(h.workItemsOfType(agentapp.WorkTypeDesignRework)); got != 2 {
		t.Fatalf("rework items after extra reconciles = %d, want 2 (no duplicate generation)", got)
	}
}

// TestJudgeGenerationBusyVersionWindow is Important #2: with more than
// MaxListLimit newer work items on the version, the capped newest-500
// ListByVersion page no longer contains the older design.judge generations. The
// judge generation sequence/budget must be read with a TARGETED query (mirroring
// judgeReworkSQLSource), or the sequence undercounts, the dedupe key collides
// with the terminal generation and the draft set is stranded pending forever.
func TestJudgeGenerationBusyVersionWindow(t *testing.T) {
	h := newJudgeLoopHarness(t)
	h.setupDesigningRequirement(1)
	h.setAuthorMode("full")
	h.setJudgeMode("error")

	h.requireRunDone(h.runWorkItems("design.requirement"), 1, "author")
	drafts := h.draftCards()
	if len(drafts) != 2 {
		t.Fatalf("author produced %d drafts, want 2", len(drafts))
	}

	// Generation 1 runs and fails to its attempt cap (terminal).
	h.reconcile()
	if got := h.judgeWorkItemCount(); got != 1 {
		t.Fatalf("design.judge rows after the first pass = %d, want 1", got)
	}
	runs := h.runWorkItems("design.judge")
	if len(runs) == 0 {
		t.Fatal("generation 1 was never claimed")
	}
	for _, run := range runs {
		if run.err == nil {
			t.Fatalf("judge run unexpectedly succeeded in error mode: %+v", run.result)
		}
	}

	// Bury the judge rows under a busy version's newer work items.
	h.pushTerminalWorkItemNoise(agentapp.MaxListLimit + 50)
	if items, err := h.queue.ListByVersion(h.ctx(), h.versionID, agentapp.MaxListLimit); err != nil {
		t.Fatalf("list work items: %v", err)
	} else {
		for _, item := range items {
			if agentapp.WorkTypeForItem(item) == agentapp.WorkTypeDesignJudge {
				t.Fatal("the capped window still contains a design.judge row; the fixture does not reproduce the hazard")
			}
		}
	}

	// A fresh generation must still fire (created==true), not silently collide.
	h.reconcile()
	if got := h.judgeWorkItemCount(); got != 2 {
		t.Fatalf("design.judge rows after the busy-window pass = %d, want 2 (a new generation must fire)", got)
	}
	if got := h.openJudgeWorkItemCount(); got != 1 {
		t.Fatalf("open design.judge rows = %d, want 1 (the new generation is queued)", got)
	}

	// The budget must still be computed from the real generations: draining them
	// exhausts the draft-set budget and writes one escalation fact per draft.
	h.drainJudgeGenerations()
	for _, card := range drafts {
		var count int
		if err := h.pool.QueryRow(h.ctx(),
			`SELECT count(*) FROM version_fact WHERE version_id = $1 AND kind = $2 AND key = $3 AND superseded_by IS NULL`,
			h.versionID, judgeExhaustedFactKind, judgeExhaustedFactKey(card.CaseID, card.CurrentVersionID)).Scan(&count); err != nil {
			t.Fatalf("read judge_exhausted fact: %v", err)
		}
		if count != 1 {
			t.Fatalf("judge_exhausted facts for case %d = %d, want 1 (budget must still exhaust)", card.CaseID, count)
		}
		if state := h.reviewState(card.CaseID, card.CurrentVersionID); state != design.ReviewStateJudgeUnavailable {
			t.Fatalf("review state = %s, want judge_unavailable (no permanent pending)", state)
		}
	}
	if got := h.judgeWorkItemCount(); got != maxJudgeGenerations {
		t.Fatalf("design.judge rows at exhaustion = %d, want %d (bounded budget)", got, maxJudgeGenerations)
	}
}
