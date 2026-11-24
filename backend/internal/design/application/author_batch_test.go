package designapp

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
)

// fakeAuthorBatchReader answers the two author-batch reads from fixed values and
// records what the scheduler asked for, so a test can pin that the predicate is
// scoped to the requirement's CURRENT revision (not the requirement id).
type fakeAuthorBatchReader struct {
	state       AuthorBatchState
	deadline    time.Time
	err         error
	deadlineErr error
	calls       int
	sinceCalls  int
	gotVersion  int64
	gotReqVer   int64
}

func (r *fakeAuthorBatchReader) AuthorBatchState(_ context.Context, versionID, requirementVersionID int64) (AuthorBatchState, error) {
	r.calls++
	r.gotVersion = versionID
	r.gotReqVer = requirementVersionID
	return r.state, r.err
}

func (r *fakeAuthorBatchReader) AuthorBatchStallDeadline(_ context.Context, versionID, requirementVersionID int64) (time.Time, error) {
	r.sinceCalls++
	r.gotVersion = versionID
	r.gotReqVer = requirementVersionID
	return r.deadline, r.deadlineErr
}

// seedAuthorBatchGraph seeds one clarified requirement whose coverage union is
// complete through a CONFIRMED case plus one draft per extra AC group. This is
// the H-F6 reachability premise: coverage can be complete without the author
// batch of this requirement version having produced anything at all.
func seedAuthorBatchGraph(repo *fakeRepo, draftACs [][]string) {
	seedJudgeGraph(repo, 10, 100, []string{"AC-1", "AC-2"}, [][]string{{"AC-2"}}, draftACs)
}

// TestJudgeEnqueuesWhenNoAuthorBatch is the H-F6 leg that proves the new gate
// does not create a permanent pending: coverage reached through confirmed cases
// plus a draft authored by ANOTHER path (host proposal / discovery revision)
// leaves NO design.requirement work item for this requirement version. The
// predicate must read that as NoBatch and still enqueue the judge — a "false"
// here would strand every such draft in review_state pending forever (SC-E4).
func TestJudgeEnqueuesWhenNoAuthorBatch(t *testing.T) {
	planner, repo, enqueuer, judgments, _ := newJudgeSchedulerFixture()
	ctx := context.Background()
	seedAuthorBatchGraph(repo, [][]string{{"AC-1"}})
	reader := &fakeAuthorBatchReader{state: AuthorBatchNoBatch}
	if got := planner.SetAuthorBatchReader(reader); got != planner {
		t.Fatal("SetAuthorBatchReader must return the planner")
	}
	if err := planner.ensureJudgeTasks(ctx, 1001); err != nil {
		t.Fatalf("ensureJudgeTasks: %v", err)
	}
	judges := enqueuer.byType(TaskTypeDesignJudge)
	if len(judges) != 1 {
		t.Fatalf("judge runs = %d, want 1 (no author batch must not block the judge)", len(judges))
	}
	if judges[0].DedupeKey != "judge:10:100" {
		t.Fatalf("judge dedupe = %q, want judge:10:100", judges[0].DedupeKey)
	}
	if reader.calls != 1 || reader.gotVersion != 1001 || reader.gotReqVer != 100 {
		t.Fatalf("author batch read = (%d,%d) x%d, want (1001,100) x1",
			reader.gotVersion, reader.gotReqVer, reader.calls)
	}
	if len(judgments.exhausted) != 0 {
		t.Fatalf("no-batch leg must not escalate: %v", judgments.exhausted)
	}
}

// TestJudgeEnqueuesWhenAuthorBatchTerminal pins the second true leg: the author
// batch of this requirement version exists and every generation is terminal
// (done / dead / failed at its attempt cap), so the judge owns the drafts now.
func TestJudgeEnqueuesWhenAuthorBatchTerminal(t *testing.T) {
	planner, repo, enqueuer, judgments, _ := newJudgeSchedulerFixture()
	ctx := context.Background()
	seedAuthorBatchGraph(repo, [][]string{{"AC-1"}})
	planner.SetAuthorBatchReader(&fakeAuthorBatchReader{state: AuthorBatchAllTerminal})
	if err := planner.ensureJudgeTasks(ctx, 1001); err != nil {
		t.Fatalf("ensureJudgeTasks: %v", err)
	}
	if got := len(enqueuer.byType(TaskTypeDesignJudge)); got != 1 {
		t.Fatalf("judge runs = %d, want 1 (a settled author batch releases the judge)", got)
	}
	if len(judgments.exhausted) != 0 {
		t.Fatalf("settled batch must not escalate: %v", judgments.exhausted)
	}
}

// TestJudgeDefersInFlightAuthorBatch pins the deferral leg: while the author
// batch of this requirement version still owes work, the judge is NOT enqueued
// (it would review a partial draft set and burn a generation on it). Deferral
// writes no escalation fact — the leased row is still inside its stall allowance.
func TestJudgeDefersInFlightAuthorBatch(t *testing.T) {
	planner, repo, enqueuer, judgments, _ := newJudgeSchedulerFixture()
	ctx := context.Background()
	seedAuthorBatchGraph(repo, [][]string{{"AC-1"}})
	reader := &fakeAuthorBatchReader{
		state:    AuthorBatchInFlight,
		deadline: planner.clock.Now().Add(time.Minute),
	}
	planner.SetAuthorBatchReader(reader)
	if err := planner.ensureJudgeTasks(ctx, 1001); err != nil {
		t.Fatalf("ensureJudgeTasks: %v", err)
	}
	if got := len(enqueuer.byType(TaskTypeDesignJudge)); got != 0 {
		t.Fatalf("judge runs = %d, want 0 while the author batch is in flight", got)
	}
	if reader.sinceCalls != 1 {
		t.Fatalf("stall-clock reads = %d, want 1 (the deferral must be bounded)", reader.sinceCalls)
	}
	if len(judgments.exhausted) != 0 {
		t.Fatalf("a batch inside its stall allowance must not escalate: %v", judgments.exhausted)
	}
}

// TestJudgeEscalatesStalledAuthorBatch is the H-F6 timeout leg (mirror of the
// DEV-E3 "in flight, otherwise escalate" invariant): a LEASE-HOLDING authoring
// run that stops making progress must not pin the drafts in pending forever.
// Once its stall deadline passes, every PENDING draft of the
// requirement version gets the judge_exhausted escalation fact (review_state
// judge_unavailable, human override reachable) and the degradation event is
// emitted once per NEWLY created fact — never per reconcile pass.
func TestJudgeEscalatesStalledAuthorBatch(t *testing.T) {
	planner, repo, enqueuer, judgments, _ := newJudgeSchedulerFixture()
	ctx := context.Background()
	seedAuthorBatchGraph(repo, [][]string{{"AC-1"}})
	// A second pending draft: escalation must cover EVERY pending draft.
	seedJudgeGraph(repo, 10, 100, []string{"AC-1", "AC-2"}, nil, [][]string{{"AC-1"}, {"AC-2"}})
	reader := &fakeAuthorBatchReader{
		state:    AuthorBatchInFlight,
		deadline: planner.clock.Now().Add(-time.Second),
	}
	planner.SetAuthorBatchReader(reader)
	publisher := planner.events.(*fakePublisher)

	if err := planner.ensureJudgeTasks(ctx, 1001); err != nil {
		t.Fatalf("ensureJudgeTasks: %v", err)
	}
	if got := len(enqueuer.byType(TaskTypeDesignJudge)); got != 0 {
		t.Fatalf("judge runs = %d, want 0 (a stalled batch escalates, it does not judge)", got)
	}
	for _, pair := range [][2]int64{{7001, 8001}, {7002, 8002}} {
		if !judgments.exhausted[pair] {
			t.Fatalf("stalled batch must escalate every pending draft; missing %v (facts=%v)", pair, judgments.exhausted)
		}
	}
	events := publisher.byType(JudgeExhaustedEventType)
	if len(events) != 2 {
		t.Fatalf("degradation events = %d, want one per newly created fact (2)", len(events))
	}
	if got := events[0].Payload["case_id"]; got != "7001" {
		t.Fatalf("event case_id = %v, want 7001", got)
	}
	if got := events[0].Scope["requirement_id"]; got != "10" {
		t.Fatalf("event scope requirement_id = %v, want 10", got)
	}

	// A second reconcile pass: the facts already exist, so no new event (every
	// persisted event wakes a reconcile pass; re-publishing would self-sustain).
	if err := planner.ensureJudgeTasks(ctx, 1001); err != nil {
		t.Fatalf("ensureJudgeTasks (repeat): %v", err)
	}
	if got := len(publisher.byType(JudgeExhaustedEventType)); got != 2 {
		t.Fatalf("degradation events after a repeat pass = %d, want 2 (creation transition only)", got)
	}
}

// TestJudgeEscalationSparesJudgedDrafts pins that the stalled-batch escalation
// only targets PENDING drafts: a draft already judged pass for its current
// content keeps its verdict (DeriveReviewState ranks judge_exhausted above pass,
// so a false fact would demote a reviewed case).
func TestJudgeEscalationSparesJudgedDrafts(t *testing.T) {
	planner, repo, enqueuer, judgments, _ := newJudgeSchedulerFixture()
	ctx := context.Background()
	seedJudgeGraph(repo, 10, 100, []string{"AC-1", "AC-2"}, nil, [][]string{{"AC-1"}, {"AC-2"}})
	seedJudgePass(judgments, 1, 7002, 8002)
	planner.SetAuthorBatchReader(&fakeAuthorBatchReader{
		state:    AuthorBatchInFlight,
		deadline: planner.clock.Now().Add(-time.Hour),
	})
	if err := planner.ensureJudgeTasks(ctx, 1001); err != nil {
		t.Fatalf("ensureJudgeTasks: %v", err)
	}
	if !judgments.exhausted[[2]int64{7001, 8001}] {
		t.Fatal("the unjudged draft must be escalated")
	}
	if judgments.exhausted[[2]int64{7002, 8002}] {
		t.Fatal("a judged-pass draft must not get a false judge_exhausted fact")
	}
	if got := len(enqueuer.byType(TaskTypeDesignJudge)); got != 0 {
		t.Fatalf("judge runs = %d, want 0", got)
	}
}

// TestJudgeQueuedBatchDefersWithoutEscalating pins review I1 at the predicate
// level: an in-flight batch with NO stall clock (every row queued or parked for a
// human) defers the judge and is NEVER escalated, however long it waits. A zero
// deadline must not read as "infinitely old".
func TestJudgeQueuedBatchDefersWithoutEscalating(t *testing.T) {
	planner, repo, enqueuer, judgments, _ := newJudgeSchedulerFixture()
	ctx := context.Background()
	seedAuthorBatchGraph(repo, [][]string{{"AC-1"}})
	planner.SetAuthorBatchReader(&fakeAuthorBatchReader{state: AuthorBatchInFlight})
	if err := planner.ensureJudgeTasks(ctx, 1001); err != nil {
		t.Fatalf("ensureJudgeTasks: %v", err)
	}
	if got := len(enqueuer.byType(TaskTypeDesignJudge)); got != 0 {
		t.Fatalf("judge runs = %d, want 0", got)
	}
	if len(judgments.exhausted) != 0 {
		t.Fatalf("a queued/human-parked batch must never escalate: %v", judgments.exhausted)
	}
}

// TestJudgeAuthorBatchReadFailureFailsClosed pins constitution VII.6: an
// unreadable author batch is an error, never a silent "no batch" (which would
// judge a partial set) and never a silent escalation.
func TestJudgeAuthorBatchReadFailureFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reader *fakeAuthorBatchReader
	}{
		{"state read fails", &fakeAuthorBatchReader{err: fmt.Errorf("work item store down")}},
		{"stall-clock read fails", &fakeAuthorBatchReader{
			state: AuthorBatchInFlight, deadlineErr: fmt.Errorf("work item store down"),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			planner, repo, enqueuer, judgments, _ := newJudgeSchedulerFixture()
			ctx := context.Background()
			seedAuthorBatchGraph(repo, [][]string{{"AC-1"}})
			planner.SetAuthorBatchReader(tc.reader)
			if err := planner.ensureJudgeTasks(ctx, 1001); err == nil {
				t.Fatal("an unreadable author batch must fail closed")
			}
			if got := len(enqueuer.byType(TaskTypeDesignJudge)); got != 0 {
				t.Fatalf("failed author-batch read enqueued %d judge runs, want 0", got)
			}
			if len(judgments.exhausted) != 0 {
				t.Fatalf("failed author-batch read escalated: %v", judgments.exhausted)
			}
		})
	}
}

// TestJudgeAuthorBatchUnwiredKeepsLegacyBehavior pins the degraded-assembly
// contract shared with SetReworkBudgetReader: without the port the scheduler
// keeps today's behavior (coverage + pending drafts) instead of failing every
// reconcile pass. Production wires the reader (composition-root wiring guard).
func TestJudgeAuthorBatchUnwiredKeepsLegacyBehavior(t *testing.T) {
	planner, repo, enqueuer, _, _ := newJudgeSchedulerFixture()
	ctx := context.Background()
	seedAuthorBatchGraph(repo, [][]string{{"AC-1"}})
	if err := planner.ensureJudgeTasks(ctx, 1001); err != nil {
		t.Fatalf("ensureJudgeTasks: %v", err)
	}
	if got := len(enqueuer.byType(TaskTypeDesignJudge)); got != 1 {
		t.Fatalf("judge runs = %d, want 1 (unwired reader keeps the legacy predicate)", got)
	}
}

// TestJudgeInFlightAuthorBatchStillDrivesRework pins the gate's scope: it gates
// only the ENQUEUE of a new judge generation. A draft already judged fail for
// its current content must still get its rework while another authoring
// generation is in flight — otherwise the rework loop would stall behind an
// unrelated work item.
func TestJudgeInFlightAuthorBatchStillDrivesRework(t *testing.T) {
	planner, repo, enqueuer, judgments, settings := newJudgeSchedulerFixture()
	settings[maxReworkRoundsKey] = 1
	ctx := context.Background()
	seedJudgeGraph(repo, 10, 100, []string{"AC-1", "AC-2"}, nil, [][]string{{"AC-1"}, {"AC-2"}})
	judgments.judgments = append(judgments.judgments, design.Judgment{
		ID: 1, VersionID: 1001, RequirementID: 10, CaseID: 7001, CaseVersionID: 8001,
		ContentHash: strings.Repeat("a", 64), Round: 1, Verdict: design.VerdictFail,
	})
	planner.SetAuthorBatchReader(&fakeAuthorBatchReader{
		state:    AuthorBatchInFlight,
		deadline: planner.clock.Now().Add(time.Minute),
	})
	if err := planner.ensureJudgeTasks(ctx, 1001); err != nil {
		t.Fatalf("ensureJudgeTasks: %v", err)
	}
	if got := len(enqueuer.byType(TaskTypeDesignJudge)); got != 0 {
		t.Fatalf("judge runs = %d, want 0 (batch in flight)", got)
	}
	reworks := enqueuer.byType(TaskTypeDesignRework)
	if len(reworks) != 1 {
		t.Fatalf("rework items = %d, want 1 (the fail observation must not be gated)", len(reworks))
	}
	if reworks[0].DedupeKey != "rework:7001:8001" {
		t.Fatalf("rework dedupe = %q, want rework:7001:8001", reworks[0].DedupeKey)
	}
}

// TestAuthorBatchStateValues pins the port's enum values (the contract the
// composition-root adapter answers with).
func TestAuthorBatchStateValues(t *testing.T) {
	for _, tc := range []struct {
		state AuthorBatchState
		want  string
	}{
		{AuthorBatchNoBatch, "no_batch"},
		{AuthorBatchAllTerminal, "all_terminal"},
		{AuthorBatchInFlight, "in_flight"},
	} {
		if string(tc.state) != tc.want {
			t.Fatalf("author batch state = %q, want %q", tc.state, tc.want)
		}
	}
}

// recordingLogHandler captures planner log records so a test can pin WHEN the
// scheduler speaks (attribution without a per-pass log storm).
type recordingLogHandler struct {
	records []slog.Record
}

func (h *recordingLogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingLogHandler) Handle(_ context.Context, record slog.Record) error {
	h.records = append(h.records, record.Clone())
	return nil
}

func (h *recordingLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *recordingLogHandler) WithGroup(string) slog.Handler { return h }

func (h *recordingLogHandler) countAt(level slog.Level) int {
	count := 0
	for _, record := range h.records {
		if record.Level == level {
			count++
		}
	}
	return count
}

// TestStalledBatchWarnsOnlyOnTheCreatingPass pins review M3: the stalled
// condition persists across every reconcile pass, so the escalation WARN must
// fire only on the pass that actually wrote facts — otherwise the signal buries
// itself in its own repetition.
func TestStalledBatchWarnsOnlyOnTheCreatingPass(t *testing.T) {
	planner, repo, _, _, _ := newJudgeSchedulerFixture()
	ctx := context.Background()
	seedAuthorBatchGraph(repo, [][]string{{"AC-1"}})
	planner.SetAuthorBatchReader(&fakeAuthorBatchReader{
		state:    AuthorBatchInFlight,
		deadline: planner.clock.Now().Add(-time.Second),
	})
	logs := &recordingLogHandler{}
	planner.SetLogger(slog.New(logs))
	for pass := 0; pass < 3; pass++ {
		if err := planner.ensureJudgeTasks(ctx, 1001); err != nil {
			t.Fatalf("ensureJudgeTasks pass %d: %v", pass, err)
		}
	}
	if got := logs.countAt(slog.LevelWarn); got != 1 {
		t.Fatalf("escalation warnings = %d over three passes, want exactly 1 (creation transition only)", got)
	}
}

// TestQueuedBatchDeferralIsAttributable pins review M2/③: the unbounded deferral
// of a queued/human-parked batch is not silent — it is logged with its reason.
// It is a DEBUG record because this is the expected state on every pass while
// the author is queued, not a degradation; a per-pass WARN would be a log storm.
func TestQueuedBatchDeferralIsAttributable(t *testing.T) {
	planner, repo, _, _, _ := newJudgeSchedulerFixture()
	ctx := context.Background()
	seedAuthorBatchGraph(repo, [][]string{{"AC-1"}})
	planner.SetAuthorBatchReader(&fakeAuthorBatchReader{state: AuthorBatchInFlight})
	logs := &recordingLogHandler{}
	planner.SetLogger(slog.New(logs))
	if err := planner.ensureJudgeTasks(ctx, 1001); err != nil {
		t.Fatalf("ensureJudgeTasks: %v", err)
	}
	if logs.countAt(slog.LevelWarn) != 0 {
		t.Fatal("a normally queued batch must not warn on every reconcile pass")
	}
	found := false
	for _, record := range logs.records {
		if record.Level == slog.LevelDebug && strings.Contains(record.Message, "queued or awaiting a human") {
			found = true
		}
	}
	if !found {
		t.Fatal("the queued-batch deferral must be attributable (a debug record naming the reason)")
	}
}
