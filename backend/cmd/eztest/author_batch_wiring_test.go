package main

// v0.2.4-H T10 (H-F5/H-F6, spec §3.6): the composition-root half of the judge
// scheduling fixes.
//
//   - H-F6: the author-batch settle state the design planner's judge predicate
//     consumes. It is derived from the design.requirement work items of ONE
//     requirement version (targeted read, never the capped newest-500 window),
//     with the SAME terminal predicate the rework budget and the generation
//     counters use — one lifecycle truth, not a second switch.
//   - H-F5: EnsureCoverageRepair derives its generation key from that same
//     targeted source; an unwired source fails closed instead of silently
//     falling back to the capped window.
//
// The real-DB legs (no batch / in flight / stalled, and the busy-version
// generation) live in author_batch_integration_test.go.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
)

// authorWorkItem builds one design.requirement row of a requirement version.
func authorWorkItem(reqVersion string, status agent.WorkItemStatus, attempts, maxAttempts int, updatedAt time.Time) agent.WorkItem {
	return agent.WorkItem{
		Type:        designapp.TaskTypeDesignRequirement,
		Status:      status,
		Attempts:    attempts,
		MaxAttempts: maxAttempts,
		UpdatedAt:   updatedAt,
		Payload: mustJSON(map[string]string{
			"requirement_id": "10", "requirement_version_id": reqVersion,
		}),
	}
}

// TestAuthorBatchStateFromWorkItems pins the three-state enum against the work
// item lifecycle. "Terminal" is the SAME predicate the escalation budget uses
// (done / dead / failed at the attempt cap): a retryable failure still owes the
// requirement its drafts, so it is in flight, not settled.
func TestAuthorBatchStateFromWorkItems(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		items []agent.WorkItem
		want  designapp.AuthorBatchState
	}{
		{name: "no author batch at all", want: designapp.AuthorBatchNoBatch},
		{
			name:  "done generation is settled",
			items: []agent.WorkItem{authorWorkItem("100", agent.WorkItemDone, 1, 3, now)},
			want:  designapp.AuthorBatchAllTerminal,
		},
		{
			name:  "dead generation is settled",
			items: []agent.WorkItem{authorWorkItem("100", agent.WorkItemDead, 3, 3, now)},
			want:  designapp.AuthorBatchAllTerminal,
		},
		{
			name:  "failure at the attempt cap is settled",
			items: []agent.WorkItem{authorWorkItem("100", agent.WorkItemFailed, 3, 3, now)},
			want:  designapp.AuthorBatchAllTerminal,
		},
		{
			name:  "open generation is in flight",
			items: []agent.WorkItem{authorWorkItem("100", agent.WorkItemOpen, 0, 3, now)},
			want:  designapp.AuthorBatchInFlight,
		},
		{
			name:  "claimed generation is in flight",
			items: []agent.WorkItem{authorWorkItem("100", agent.WorkItemClaimed, 1, 3, now)},
			want:  designapp.AuthorBatchInFlight,
		},
		{
			name:  "running generation is in flight",
			items: []agent.WorkItem{authorWorkItem("100", agent.WorkItemRunning, 1, 3, now)},
			want:  designapp.AuthorBatchInFlight,
		},
		{
			name:  "waiting_human generation is in flight",
			items: []agent.WorkItem{authorWorkItem("100", agent.WorkItemWaitingHuman, 1, 3, now)},
			want:  designapp.AuthorBatchInFlight,
		},
		{
			name:  "retryable failure still owes the batch its drafts",
			items: []agent.WorkItem{authorWorkItem("100", agent.WorkItemFailed, 1, 3, now)},
			want:  designapp.AuthorBatchInFlight,
		},
		{
			name: "one settled generation does not settle the batch",
			items: []agent.WorkItem{
				authorWorkItem("100", agent.WorkItemDone, 1, 3, now),
				authorWorkItem("100", agent.WorkItemRunning, 1, 3, now),
			},
			want: designapp.AuthorBatchInFlight,
		},
		{
			name:  "another requirement version is not this batch",
			items: []agent.WorkItem{authorWorkItem("999", agent.WorkItemRunning, 1, 3, now)},
			want:  designapp.AuthorBatchNoBatch,
		},
		{
			name: "another work type is not the author batch",
			items: []agent.WorkItem{{
				Type: designapp.TaskTypeDesignJudge, Status: agent.WorkItemRunning,
				Payload: mustJSON(map[string]string{"requirement_version_id": "100"}),
			}},
			want: designapp.AuthorBatchNoBatch,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := authorBatchState(test.items, 100); got != test.want {
				t.Fatalf("authorBatchState = %q, want %q", got, test.want)
			}
		})
	}
}

// TestAuthorBatchStallDeadlineOnlyClocksLeasedRows is the review-I1 regression
// pin: updated_at advances ONLY while a lease is held (the Renew statement is
// guarded by the leased-status set), so a queued row (open — first claim OR a
// retry waiting for a free worker) and a row parked for a human have a frozen
// clock and an unbounded normal wait. They must NOT produce a stall deadline,
// however old their timestamp is; only claimed/running rows do.
func TestAuthorBatchStallDeadlineOnlyClocksLeasedRows(t *testing.T) {
	base := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	ancient := base.Add(-100 * time.Hour)
	tests := []struct {
		name      string
		items     []agent.WorkItem
		wantClock bool
	}{
		{name: "no rows"},
		{
			name:  "queued open row never starts the clock",
			items: []agent.WorkItem{authorWorkItem("100", agent.WorkItemOpen, 0, 3, ancient)},
		},
		{
			name:  "retry-queued open row (already claimed once) never starts the clock",
			items: []agent.WorkItem{authorWorkItem("100", agent.WorkItemOpen, 2, 3, ancient)},
		},
		{
			name:  "waiting_human row never starts the clock",
			items: []agent.WorkItem{authorWorkItem("100", agent.WorkItemWaitingHuman, 1, 3, ancient)},
		},
		{
			name:  "retryable failure waiting for re-claim never starts the clock",
			items: []agent.WorkItem{authorWorkItem("100", agent.WorkItemFailed, 1, 3, ancient)},
		},
		{
			name:      "claimed row starts the clock",
			items:     []agent.WorkItem{authorWorkItem("100", agent.WorkItemClaimed, 1, 3, ancient)},
			wantClock: true,
		},
		{
			name:      "running row starts the clock",
			items:     []agent.WorkItem{authorWorkItem("100", agent.WorkItemRunning, 1, 3, ancient)},
			wantClock: true,
		},
		{
			name: "a queued row does not drag the deadline of a leased one",
			items: []agent.WorkItem{
				authorWorkItem("100", agent.WorkItemOpen, 0, 3, ancient),
				authorWorkItem("100", agent.WorkItemRunning, 1, 3, base),
			},
			wantClock: true,
		},
		{
			name:  "terminal rows never start the clock",
			items: []agent.WorkItem{authorWorkItem("100", agent.WorkItemDone, 1, 3, ancient)},
		},
		{
			name:  "another requirement version never starts the clock",
			items: []agent.WorkItem{authorWorkItem("999", agent.WorkItemRunning, 1, 3, ancient)},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := authorBatchStallDeadline(test.items, 100)
			if err != nil {
				t.Fatalf("authorBatchStallDeadline: %v", err)
			}
			if test.wantClock {
				if got.IsZero() {
					t.Fatal("a lease-holding row must produce a stall deadline")
				}
				return
			}
			if !got.IsZero() {
				t.Fatalf("stall deadline = %v, want zero (queued/human/terminal rows carry no progress clock)", got)
			}
		})
	}
}

// TestAuthorBatchStallDeadlineUsesRowWindowAndEarliestRow pins the per-row
// allowance: the deadline is last progress + per-attempt window x the row's OWN
// max_attempts, and the batch takes the EARLIEST such deadline.
func TestAuthorBatchStallDeadlineUsesRowWindowAndEarliestRow(t *testing.T) {
	base := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	items := []agent.WorkItem{
		authorWorkItem("100", agent.WorkItemRunning, 1, 10, base),
		authorWorkItem("100", agent.WorkItemClaimed, 1, 3, base),
	}
	got, err := authorBatchStallDeadline(items, 100)
	if err != nil {
		t.Fatalf("authorBatchStallDeadline: %v", err)
	}
	want := base.Add(3 * designapp.AuthorBatchStallWindowPerAttempt)
	if !got.Equal(want) {
		t.Fatalf("stall deadline = %v, want the earliest row deadline %v (3 attempts, not 10)", got, want)
	}
}

// TestAuthorBatchRowStallWindowScalesWithMaxAttempts pins review I1's second
// half: the allowance follows the row's own attempt cap (queue default 3,
// domain bound 10, DB CHECK up to 20), not the platform default alone.
func TestAuthorBatchRowStallWindowScalesWithMaxAttempts(t *testing.T) {
	for _, attempts := range []int{1, 3, 10, 20} {
		want := time.Duration(attempts) * designapp.AuthorBatchStallWindowPerAttempt
		if got := authorBatchRowStallWindow(attempts); got != want {
			t.Fatalf("row window(%d) = %v, want %v", attempts, got, want)
		}
	}
	// A non-positive cap falls back to the queue default, never to a zero
	// window (which would escalate a leased row instantly).
	if got, want := authorBatchRowStallWindow(0),
		time.Duration(agentapp.DefaultMaxAttempts)*designapp.AuthorBatchStallWindowPerAttempt; got != want {
		t.Fatalf("row window(0) = %v, want the default-cap window %v", got, want)
	}
}

// TestAuthorBatchLeasedRowWithoutProgressFailsClosed pins constitution VII.6 on
// the anomalous case: a row that holds a lease but carries no progress timestamp
// is a data-integrity error, never a silent "no clock" that defers forever.
func TestAuthorBatchLeasedRowWithoutProgressFailsClosed(t *testing.T) {
	items := []agent.WorkItem{authorWorkItem("100", agent.WorkItemRunning, 1, 3, time.Time{})}
	if _, err := authorBatchStallDeadline(items, 100); err == nil {
		t.Fatal("a lease-holding row with no progress timestamp must fail closed")
	}
	reader := &authorBatchReader{source: stubAuthorSource{items: items}}
	if _, err := reader.AuthorBatchStallDeadline(context.Background(), 1001, 100); err == nil {
		t.Fatal("the reader must propagate the integrity error")
	}
}

// stubAuthorSource serves fixed rows to the reader (no database in unit tests).
type stubAuthorSource struct {
	items []agent.WorkItem
	err   error
}

func (s stubAuthorSource) AuthorItemsForRequirementVersion(context.Context, int64, int64) ([]agent.WorkItem, error) {
	return s.items, s.err
}

// TestAuthorBatchStallWindowCoversAuthorAttemptEnvelope derives the per-attempt
// allowance from the REAL authorities instead of a magic number: one attempt may
// run for the design_author hard budget and then wait out the queue's capped
// retry backoff. It also pins that a default-cap row's total allowance covers the
// whole retry envelope. If any authority grows past the allowance, this pin goes
// red and forces re-derivation.
func TestAuthorBatchStallWindowCoversAuthorAttemptEnvelope(t *testing.T) {
	hardSeconds := 0
	for _, profile := range agentapp.DesignProfiles() {
		if profile.Key == agentapp.ProfileKeyDesignAuthor {
			hardSeconds = profile.Budgets.HardSeconds
		}
	}
	if hardSeconds <= 0 {
		t.Fatal("design_author profile hard budget not found; the window derivation lost its source")
	}
	perAttempt := time.Duration(hardSeconds)*time.Second + agentapp.MaxBackoff
	if designapp.AuthorBatchStallWindowPerAttempt < perAttempt {
		t.Fatalf("AuthorBatchStallWindowPerAttempt = %v, want >= one attempt envelope %v (%ds hard + %v backoff)",
			designapp.AuthorBatchStallWindowPerAttempt, perAttempt, hardSeconds, agentapp.MaxBackoff)
	}
	envelope := time.Duration(agentapp.DefaultMaxAttempts) * perAttempt
	if got := authorBatchRowStallWindow(agentapp.DefaultMaxAttempts); got < envelope {
		t.Fatalf("default-cap row window = %v, want >= the full retry envelope %v", got, envelope)
	}
}

// TestAuthorBatchReaderFailsClosed pins constitution VII.6 on both reads: an
// unwired work-item source is an error, never a silent "no batch" (which would
// release the judge onto a partial draft set).
func TestAuthorBatchReaderFailsClosed(t *testing.T) {
	ctx := context.Background()
	for _, reader := range []*authorBatchReader{nil, {}, {source: nil}} {
		if _, err := reader.AuthorBatchState(ctx, 1001, 100); err == nil {
			t.Fatal("an unwired author-batch source must fail closed")
		}
		if _, err := reader.AuthorBatchStallDeadline(ctx, 1001, 100); err == nil {
			t.Fatal("an unwired author-batch stall-clock source must fail closed")
		}
	}
}

// TestGenerationSourcesFailClosed pins that neither generation counter falls
// back to the capped ListByVersion window when its targeted source is unwired:
// a silent fallback undercounts generations on a busy version and strands the
// draft set (H-F5 is exactly that hazard on the coverage-repair side).
func TestGenerationSourcesFailClosed(t *testing.T) {
	ctx := context.Background()
	enqueuer := designTaskEnqueuer{queue: &agentapp.WorkQueue{}}
	if _, err := enqueuer.EnsureCoverageRepair(ctx, designapp.TaskInput{
		VersionID: 1001, Type: designapp.TaskTypeDesignRequirement, DedupeKey: "design:10:100",
	}, 100); err == nil {
		t.Fatal("EnsureCoverageRepair without a targeted source must fail closed, not read the capped window")
	}
	if _, err := enqueuer.EnsureJudgeGeneration(ctx, designapp.TaskInput{
		VersionID: 1001, Type: designapp.TaskTypeDesignJudge, DedupeKey: "judge:10:100",
		Payload: map[string]any{"requirement_id": "10", "requirement_version_id": "100"},
	}, []designapp.JudgeCaseRef{{CaseID: 7001, CaseVersionID: 8001}}, nil); err == nil {
		t.Fatal("EnsureJudgeGeneration without a targeted source must fail closed")
	}
}

// TestJudgeAuthorBatchAndRepairSourcesWiredInProduction pins the composition
// root: the planner consumes the author-batch reader and the coverage-repair
// counter reads the targeted design.requirement source. Source pin because no
// database is available in unit tests (discovery_facts_wiring_test precedent).
func TestJudgeAuthorBatchAndRepairSourcesWiredInProduction(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	content := string(source)
	for _, fragment := range []string{
		"designPlanner.SetAuthorBatchReader(",
		"coverageRepairs:",
		"judgeGenerations:",
		// H-F5b: an unwired author-exhaustion store turns the spent authoring
		// budget back into a silent stranded requirement.
		"authorExhausted:",
	} {
		if !strings.Contains(content, fragment) {
			t.Fatalf("main.go must wire %q (H-F5/H-F6: targeted sources + author-batch predicate live in production)", fragment)
		}
	}
	if strings.Contains(content, "e.queue.ListByVersion(ctx, in.VersionID, agentapp.MaxListLimit)") {
		t.Fatal("EnsureCoverageRepair must not derive generations from the capped ListByVersion window (H-F5)")
	}
}
