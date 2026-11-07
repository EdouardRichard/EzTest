package agentcontext_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
)

/* -------------------------------- helpers -------------------------------- */

// compactionClock is the injectable time source of the transaction tests.
type compactionClock struct {
	now time.Time
}

func (c *compactionClock) Now() time.Time { return c.now }

func (c *compactionClock) advance(duration time.Duration) { c.now = c.now.Add(duration) }

func newCompactionClock() *compactionClock {
	return &compactionClock{now: time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)}
}

// fakeCompactionPublisher records the context.compacted events of one manager.
type fakeCompactionPublisher struct {
	events []agentcontext.CompactionEvent
	err    error
}

func (p *fakeCompactionPublisher) PublishCompaction(_ context.Context, event agentcontext.CompactionEvent) error {
	p.events = append(p.events, event)
	return p.err
}

func mustPolicy(t *testing.T, window, output int) agentcontext.CompactionPolicy {
	t.Helper()
	policy := agentcontext.NewCompactionPolicy(window, output)
	if err := policy.Validate(); err != nil {
		t.Fatalf("NewCompactionPolicy(%d, %d) = %+v: %v", window, output, policy, err)
	}
	return policy
}

/* ---------------------------- policy formulas ---------------------------- */

// TestCompactionPolicy128K pins the R-A5-1 formula on the reference 128K/8K
// model: usable 120000, trigger 96000, retain 19200, summary cap 4800.
func TestCompactionPolicy128K(t *testing.T) {
	policy := agentcontext.NewCompactionPolicy(128000, 8000)
	if policy.Window != 128000 || policy.Output != 8000 {
		t.Fatalf("window/output = %d/%d, want 128000/8000", policy.Window, policy.Output)
	}
	if policy.Usable != 120000 || policy.Trigger != 96000 ||
		policy.Retain != 19200 || policy.SummaryMax != 4800 {
		t.Fatalf("policy = %+v, want usable=120000 trigger=96000 retain=19200 summary_max=4800", policy)
	}
	if err := policy.Validate(); err != nil {
		t.Fatalf("128K policy is invalid: %v", err)
	}
}

// TestCompactionPolicy1M pins the formula on a 1M-window model: the numeric
// fields scale while summary_max is capped at 8192.
func TestCompactionPolicy1M(t *testing.T) {
	policy := agentcontext.NewCompactionPolicy(1_000_000, 8000)
	if policy.Usable != 992000 || policy.Trigger != 793600 ||
		policy.Retain != 158720 || policy.SummaryMax != 8192 {
		t.Fatalf("policy = %+v, want usable=992000 trigger=793600 retain=158720 summary_max=8192", policy)
	}
	if err := policy.Validate(); err != nil {
		t.Fatalf("1M policy is invalid: %v", err)
	}
}

// TestCompactionPolicyDefaultsAndOutputCap covers the R-A1-5 fallback window,
// the default output reserve and the 16000-token output cap.
func TestCompactionPolicyDefaultsAndOutputCap(t *testing.T) {
	defaults := agentcontext.NewCompactionPolicy(0, 0)
	if defaults.Window != 128000 || defaults.Output != 4096 {
		t.Fatalf("default window/output = %d/%d, want 128000/4096", defaults.Window, defaults.Output)
	}
	if defaults.Usable != 123904 || defaults.Trigger != 99123 ||
		defaults.Retain != 19824 || defaults.SummaryMax != 4956 {
		t.Fatalf("default policy = %+v", defaults)
	}

	capped := agentcontext.NewCompactionPolicy(200000, 40000)
	if capped.Output != 16000 || capped.Usable != 184000 ||
		capped.Trigger != 147200 || capped.Retain != 29440 || capped.SummaryMax != 7360 {
		t.Fatalf("capped policy = %+v, want output=16000 usable=184000 trigger=147200 retain=29440 summary_max=7360", capped)
	}
}

// TestCompactionPolicyValidation covers the load-time retain < trigger < usable
// check: an invalid configuration returns a typed policy error.
func TestCompactionPolicyValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*agentcontext.CompactionPolicy)
	}{
		{name: "retain equals trigger", mutate: func(p *agentcontext.CompactionPolicy) { p.Retain = p.Trigger }},
		{name: "retain above trigger", mutate: func(p *agentcontext.CompactionPolicy) { p.Retain = p.Trigger + 1 }},
		{name: "trigger equals usable", mutate: func(p *agentcontext.CompactionPolicy) { p.Trigger = p.Usable }},
		{name: "trigger above usable", mutate: func(p *agentcontext.CompactionPolicy) { p.Trigger = p.Usable + 1 }},
		{name: "zero usable", mutate: func(p *agentcontext.CompactionPolicy) {
			p.Usable = 0
			p.Trigger = 0
			p.Retain = 0
		}},
		{name: "zero window", mutate: func(p *agentcontext.CompactionPolicy) { p.Window = 0 }},
		{name: "negative retain", mutate: func(p *agentcontext.CompactionPolicy) { p.Retain = -1 }},
		{name: "zero summary max", mutate: func(p *agentcontext.CompactionPolicy) { p.SummaryMax = 0 }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			policy := agentcontext.NewCompactionPolicy(128000, 8000)
			testCase.mutate(&policy)
			err := policy.Validate()
			if !errors.Is(err, agentcontext.ErrInvalidCompactionPolicy) {
				t.Fatalf("Validate() error = %v, want ErrInvalidCompactionPolicy", err)
			}
			var policyErr *agentcontext.CompactionPolicyError
			if !errors.As(err, &policyErr) || policyErr.Field == "" {
				t.Fatalf("Validate() error = %#v, want a typed policy error with a field", err)
			}
		})
	}
}

/* -------------------------------- triggers ------------------------------- */

// TestCompactionTriggerValues pins the v0.22 enum and its spec spelling. A
// manual/cadence trigger is intentionally not part of v0.22.
func TestCompactionTriggerValues(t *testing.T) {
	values := map[agentcontext.CompactionTrigger]string{
		agentcontext.CompactionTriggerPressure:      "pressure",
		agentcontext.CompactionTriggerOverflow:      "overflow",
		agentcontext.CompactionTriggerStageBoundary: "stage_boundary",
	}
	for trigger, want := range values {
		if string(trigger) != want {
			t.Fatalf("trigger %q, want %q", trigger, want)
		}
		if !trigger.Valid() {
			t.Fatalf("trigger %q is not valid", trigger)
		}
	}
	if agentcontext.CompactionTrigger("manual").Valid() {
		t.Fatal("manual must not be a v0.22 compaction trigger")
	}
	if agentcontext.Pressure != agentcontext.CompactionTriggerPressure ||
		agentcontext.Overflow != agentcontext.CompactionTriggerOverflow ||
		agentcontext.StageBoundary != agentcontext.CompactionTriggerStageBoundary {
		t.Fatal("short trigger aliases do not match the canonical constants")
	}
}

// TestPressureTrigger covers R-A5-2 pressure: the round compacts when the last
// provider prompt plus the estimated new content reaches the trigger exactly.
func TestPressureTrigger(t *testing.T) {
	policy := mustPolicy(t, 128000, 8000)
	if policy.ShouldCompact(90000, 5999) {
		t.Fatal("compress before the trigger is reached")
	}
	if !policy.ShouldCompact(90000, 6000) {
		t.Fatal("96000 tokens must trigger compaction")
	}
	if !agentcontext.ShouldCompact(policy, 90000, 6000) {
		t.Fatal("package-level ShouldCompact disagrees with the policy")
	}

	decision, ok := policy.PressureDecision(90000, 6000)
	if !ok {
		t.Fatal("PressureDecision() = not triggered at the threshold")
	}
	if decision.Trigger != agentcontext.CompactionTriggerPressure || decision.Forced {
		t.Fatalf("decision = %+v, want a pressure trigger", decision)
	}
	if decision.Retain != policy.Retain || decision.Policy != policy {
		t.Fatalf("decision = %+v, want the normal retain budget %d", decision, policy.Retain)
	}
	if _, ok := policy.PressureDecision(90000, 5999); ok {
		t.Fatal("PressureDecision() triggered below the threshold")
	}
}

// TestOverflowHalvesRetain covers R-A5-2 overflow: forced compaction halves the
// retain budget to 8% of usable, caps retries at one and never mutates the
// normal policy.
func TestOverflowHalvesRetain(t *testing.T) {
	policy := mustPolicy(t, 128000, 8000)
	decision := policy.OverflowDecision(-1)
	if decision.Trigger != agentcontext.CompactionTriggerOverflow || !decision.Forced {
		t.Fatalf("decision = %+v, want a forced overflow trigger", decision)
	}
	if decision.Retain != 9600 || decision.Policy.Retain != 9600 {
		t.Fatalf("overflow retain = %d/%d, want 9600 (floor(0.08*120000))",
			decision.Retain, decision.Policy.Retain)
	}
	if err := decision.Policy.Validate(); err != nil {
		t.Fatalf("overflow policy is invalid: %v", err)
	}
	if decision.Policy.Trigger != policy.Trigger {
		t.Fatalf("overflow policy trigger = %d, want %d", decision.Policy.Trigger, policy.Trigger)
	}
	if decision.MaxRetries != 1 {
		t.Fatalf("overflow max retries = %d, want the v0.22 default 1", decision.MaxRetries)
	}
	if policy.Retain != 19200 {
		t.Fatalf("OverflowDecision mutated the normal retain to %d", policy.Retain)
	}
	if got := policy.OverflowDecision(5).MaxRetries; got != 1 {
		t.Fatalf("overflow max retries = %d, want the spec cap 1", got)
	}
	if got := policy.OverflowDecision(0).MaxRetries; got != 0 {
		t.Fatalf("overflow max retries = %d, want the explicitly configured 0", got)
	}

	large := mustPolicy(t, 1_000_000, 8000)
	if got := large.OverflowDecision(-1).Retain; got != 79360 {
		t.Fatalf("1M overflow retain = %d, want 79360", got)
	}
}

// TestStageBoundaryTrigger covers R-A5-2/R-A5-3 stage boundaries: the boundary
// yields a forced compaction decision even without token pressure, separately
// from the prefix-cache Invalidation.
func TestStageBoundaryTrigger(t *testing.T) {
	policy := mustPolicy(t, 128000, 8000)
	decision := policy.StageBoundaryDecision()
	if decision.Trigger != agentcontext.CompactionTriggerStageBoundary || !decision.Forced {
		t.Fatalf("decision = %+v, want a forced stage_boundary trigger", decision)
	}
	if decision.Retain != policy.Retain || decision.Policy != policy {
		t.Fatalf("decision = %+v, want the normal policy and retain", decision)
	}
	if _, pressure := policy.PressureDecision(0, 1); pressure {
		t.Fatal("stage boundary must be a distinct trigger, not pressure")
	}
	// R-A5-3: the trigger enum is separate from the prefix-cache Invalidation
	// type; only the emitted spelling of a stage transition coincides. The
	// compile-time separation is exercised by the type declarations themselves.
}

// TestCompactionTriggerDecisionCompactsWithoutInvalidation covers the trigger
// path into the compactor: pressure and overflow compact the append-only suffix
// even though the packet has no prefix-cache invalidation boundary, while the
// stable prefix stays byte-identical (R-A5-2/R-A5-3).
func TestCompactionTriggerDecisionCompactsWithoutInvalidation(t *testing.T) {
	ctx := context.Background()
	built, err := agentcontext.NewAssembler().Build(ctx, longHistoryRequest())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if built.Packet.InvalidationReason != "" {
		t.Fatalf("fixture invalidation = %q, want a normal round", built.Packet.InvalidationReason)
	}
	policy := mustPolicy(t, 128000, 8000)
	compactor := agentcontext.NewCompactor(nil)

	pressure, ok := policy.PressureDecision(policy.Trigger, 0)
	if !ok {
		t.Fatal("PressureDecision() = not triggered at the trigger")
	}
	compacted, err := compactor.CompactForDecision(ctx, built.Packet, pressure)
	if err != nil {
		t.Fatalf("CompactForDecision(pressure) error = %v", err)
	}
	if countSegments(compacted, agentcontext.SegmentSummary) != 1 {
		t.Fatalf("compacted summary segments = %d, want 1", countSegments(compacted, agentcontext.SegmentSummary))
	}
	if countSegments(compacted, agentcontext.SegmentHistory) >= countSegments(built.Packet, agentcontext.SegmentHistory) {
		t.Fatalf("history segments = %d -> %d, want older history folded",
			countSegments(built.Packet, agentcontext.SegmentHistory), countSegments(compacted, agentcontext.SegmentHistory))
	}
	if compacted.PrefixHash != built.Packet.PrefixHash || compacted.InvalidationReason != "" {
		t.Fatalf("prefix changed at a suffix compaction: hash %q -> %q, invalidation %q",
			built.Packet.PrefixHash, compacted.PrefixHash, compacted.InvalidationReason)
	}

	overflowed, err := compactor.CompactForDecision(ctx, built.Packet, policy.OverflowDecision(-1))
	if err != nil {
		t.Fatalf("CompactForDecision(overflow) error = %v", err)
	}
	if countSegments(overflowed, agentcontext.SegmentSummary) != 1 {
		t.Fatalf("overflow compacted summary segments = %d, want 1",
			countSegments(overflowed, agentcontext.SegmentSummary))
	}

	if _, err := compactor.CompactForDecision(ctx, built.Packet, agentcontext.CompactionDecision{}); err == nil {
		t.Fatal("CompactForDecision() accepted a decision without a trigger")
	}
}

// TestStageBoundaryTransactionPublishesEvent covers the stage_boundary trigger
// end to end: a forced stage transaction emits context.compacted with the
// stage_boundary reason even when the prompt is below the pressure threshold.
func TestStageBoundaryTransactionPublishesEvent(t *testing.T) {
	ctx := context.Background()
	publisher := &fakeCompactionPublisher{}
	manager := agentcontext.NewCompactionManager(
		agentcontext.WithCompactionClock(newCompactionClock()),
		agentcontext.WithCompactionPublisher(publisher),
	)
	policy := mustPolicy(t, 128000, 8000)
	outcome, err := manager.Run(ctx, 55, agentcontext.CompactionTriggerStageBoundary, policy,
		func(_ context.Context, transaction agentcontext.CompactionTransaction) (agentcontext.CompactionOutcome, error) {
			if transaction.Trigger != agentcontext.CompactionTriggerStageBoundary {
				t.Fatalf("operation trigger = %q, want stage_boundary", transaction.Trigger)
			}
			return agentcontext.CompactionOutcome{
				BeforeTokens: 50000,
				AfterTokens:  20000,
				RetainTokens: policy.Retain,
			}, nil
		})
	if err != nil {
		t.Fatalf("Run(stage_boundary) error = %v", err)
	}
	if outcome.AfterTokens != 20000 {
		t.Fatalf("outcome = %+v", outcome)
	}
	if len(publisher.events) != 1 || publisher.events[0].Trigger != agentcontext.CompactionTriggerStageBoundary {
		t.Fatalf("published events = %+v, want one stage_boundary context.compacted", publisher.events)
	}
}

/* ------------------------------ transactions ----------------------------- */

// TestCompactionLock covers R-A5-10: exactly one compaction transaction per
// conversation, a second start is busy, unrelated conversations are independent
// and finishing releases the lock for the next start.
func TestCompactionLock(t *testing.T) {
	ctx := context.Background()
	clock := newCompactionClock()
	manager := agentcontext.NewCompactionManager(agentcontext.WithCompactionClock(clock))
	policy := mustPolicy(t, 128000, 8000)

	first, err := manager.Start(ctx, 42, agentcontext.CompactionTriggerPressure, policy)
	if err != nil {
		t.Fatalf("first Start() error = %v", err)
	}
	if first.State != agentcontext.CompactionStateStarted || first.ID == "" {
		t.Fatalf("first transaction = %+v, want a started transaction with an id", first)
	}
	if _, err := manager.Start(ctx, 42, agentcontext.CompactionTriggerPressure, policy); !errors.Is(err, agentcontext.ErrCompactionBusy) {
		t.Fatalf("second Start() error = %v, want ErrCompactionBusy", err)
	}
	if _, err := manager.Run(ctx, 42, agentcontext.CompactionTriggerPressure, policy,
		func(context.Context, agentcontext.CompactionTransaction) (agentcontext.CompactionOutcome, error) {
			t.Fatal("a busy conversation must not run the compaction operation")
			return agentcontext.CompactionOutcome{}, nil
		}); !errors.Is(err, agentcontext.ErrCompactionBusy) || !agentcontext.IsRecoverableCompactionError(err) {
		t.Fatalf("busy Run() error = %v, want a recoverable ErrCompactionBusy", err)
	}
	if _, err := manager.Start(ctx, 43, agentcontext.CompactionTriggerOverflow, policy); err != nil {
		t.Fatalf("independent conversation Start() error = %v", err)
	}

	summary, err := manager.MarkSummary(ctx, first)
	if err != nil {
		t.Fatalf("MarkSummary() error = %v", err)
	}
	if summary.State != agentcontext.CompactionStateSummary {
		t.Fatalf("summary transaction state = %q, want %q", summary.State, agentcontext.CompactionStateSummary)
	}
	event, err := manager.Finish(ctx, summary, agentcontext.CompactionOutcome{
		BeforeTokens: 100000,
		AfterTokens:  50000,
		RetainTokens: policy.Retain,
	})
	if err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	if event.Trigger != agentcontext.CompactionTriggerPressure {
		t.Fatalf("event trigger = %q, want pressure", event.Trigger)
	}
	if _, err := manager.Start(ctx, 42, agentcontext.CompactionTriggerPressure, policy); err != nil {
		t.Fatalf("Start() after Finish() error = %v", err)
	}
}

// TestCompactionTTLCleanup covers R-A5-10 crash recovery: a started transaction
// without an end is stale after the 10-minute TTL and is cleaned up.
func TestCompactionTTLCleanup(t *testing.T) {
	if agentcontext.DefaultCompactionTTL != 10*time.Minute {
		t.Fatalf("DefaultCompactionTTL = %v, want 10m", agentcontext.DefaultCompactionTTL)
	}
	ctx := context.Background()
	clock := newCompactionClock()
	manager := agentcontext.NewCompactionManager(agentcontext.WithCompactionClock(clock))
	policy := mustPolicy(t, 128000, 8000)

	if _, err := manager.Start(ctx, 7, agentcontext.CompactionTriggerPressure, policy); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	clock.advance(agentcontext.DefaultCompactionTTL - time.Second)
	if _, err := manager.Start(ctx, 7, agentcontext.CompactionTriggerPressure, policy); !errors.Is(err, agentcontext.ErrCompactionBusy) {
		t.Fatalf("Start() before TTL error = %v, want ErrCompactionBusy", err)
	}

	clock.advance(2 * time.Second)
	if removed := manager.CleanupExpired(ctx); removed != 1 {
		t.Fatalf("CleanupExpired() = %d, want 1", removed)
	}
	if active, ok := manager.Active(ctx, 7); ok {
		t.Fatalf("stale transaction %+v survived cleanup", active)
	}
	if _, err := manager.Start(ctx, 7, agentcontext.CompactionTriggerPressure, policy); err != nil {
		t.Fatalf("Start() after cleanup error = %v", err)
	}

	// Start itself also reclaims an abandoned transaction, so a process that
	// only starts compactions never waits for a separate cleanup call.
	clock.advance(agentcontext.DefaultCompactionTTL + time.Second)
	if _, err := manager.Start(ctx, 7, agentcontext.CompactionTriggerPressure, policy); err != nil {
		t.Fatalf("Start() did not reclaim the stale transaction: %v", err)
	}
}

// TestThrashingGuard covers R-A5-11: two consecutive compactions that do not
// reduce the prompt size stop compaction with a thrashing error, while a
// reduction breaks the streak.
func TestThrashingGuard(t *testing.T) {
	ctx := context.Background()
	clock := newCompactionClock()
	manager := agentcontext.NewCompactionManager(agentcontext.WithCompactionClock(clock))
	policy := mustPolicy(t, 128000, 8000)

	run := func(before, after int) error {
		_, err := manager.Run(ctx, 9, agentcontext.CompactionTriggerPressure, policy,
			func(_ context.Context, transaction agentcontext.CompactionTransaction) (agentcontext.CompactionOutcome, error) {
				if transaction.State != agentcontext.CompactionStateSummary {
					t.Fatalf("operation transaction state = %q, want %q", transaction.State, agentcontext.CompactionStateSummary)
				}
				return agentcontext.CompactionOutcome{
					BeforeTokens: before,
					AfterTokens:  after,
					RetainTokens: policy.Retain,
				}, nil
			})
		return err
	}

	if err := run(1000, 1000); err != nil {
		t.Fatalf("first non-reducing compaction error = %v, want nil", err)
	}
	if err := run(1000, 900); err != nil {
		t.Fatalf("reducing compaction error = %v, want nil", err)
	}
	if err := run(1000, 1000); err != nil {
		t.Fatalf("single non-reducing compaction after a reduction error = %v, want nil", err)
	}
	if err := run(1000, 1000); !errors.Is(err, agentcontext.ErrCompactionThrashing) {
		t.Fatalf("second consecutive non-reducing compaction error = %v, want ErrCompactionThrashing", err)
	}
	// The failed guard must release the lock so a later reducing compaction can
	// still make progress.
	if err := run(1000, 800); err != nil {
		t.Fatalf("compaction after thrashing error = %v, want nil", err)
	}
}

/* --------------------------------- events -------------------------------- */

// TestCompactionEventPayload pins the R-A5-15 context.compacted payload shape
// and field names.
func TestCompactionEventPayload(t *testing.T) {
	if agentcontext.EventContextCompacted != "context.compacted" {
		t.Fatalf("EventContextCompacted = %q, want context.compacted", agentcontext.EventContextCompacted)
	}
	event := agentcontext.CompactionEvent{
		Trigger:          agentcontext.CompactionTriggerPressure,
		BeforeTokens:     97000,
		AfterTokens:      41000,
		RetainTokens:     19200,
		SummaryVersion:   3,
		CoveredMessageID: 812,
		CachedTokens:     12000,
		Duration:         1500 * time.Millisecond,
	}
	want := map[string]any{
		"trigger":            "pressure",
		"before_tokens":      97000,
		"after_tokens":       41000,
		"retain_tokens":      19200,
		"summary_version":    3,
		"covered_message_id": int64(812),
		"cached_tokens":      12000,
		"duration_ms":        int64(1500),
	}
	if payload := event.Payload(); !reflect.DeepEqual(payload, want) {
		t.Fatalf("payload = %#v, want %#v", payload, want)
	}
}

// TestCompactionPublishesEvent covers the publisher port: a completed
// transaction emits exactly one context.compacted event carrying the outcome.
func TestCompactionPublishesEvent(t *testing.T) {
	ctx := context.Background()
	clock := newCompactionClock()
	publisher := &fakeCompactionPublisher{}
	manager := agentcontext.NewCompactionManager(
		agentcontext.WithCompactionClock(clock),
		agentcontext.WithCompactionPublisher(publisher),
	)
	policy := mustPolicy(t, 128000, 8000)
	transaction, err := manager.Start(ctx, 21, agentcontext.CompactionTriggerOverflow, policy)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	transaction, err = manager.MarkSummary(ctx, transaction)
	if err != nil {
		t.Fatalf("MarkSummary() error = %v", err)
	}
	clock.advance(2500 * time.Millisecond)
	if _, err := manager.Finish(ctx, transaction, agentcontext.CompactionOutcome{
		BeforeTokens:     118000,
		AfterTokens:      39000,
		RetainTokens:     9600,
		SummaryVersion:   4,
		CoveredMessageID: 99,
		CachedTokens:     8800,
	}); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("published events = %d, want 1", len(publisher.events))
	}
	event := publisher.events[0]
	if event.Trigger != agentcontext.CompactionTriggerOverflow ||
		event.BeforeTokens != 118000 || event.AfterTokens != 39000 ||
		event.RetainTokens != 9600 || event.SummaryVersion != 4 ||
		event.CoveredMessageID != 99 || event.CachedTokens != 8800 {
		t.Fatalf("event = %+v", event)
	}
	if event.Duration != 2500*time.Millisecond {
		t.Fatalf("event duration = %v, want 2.5s", event.Duration)
	}
	if got := event.Payload()["duration_ms"]; got != int64(2500) {
		t.Fatalf("payload duration_ms = %#v, want 2500", got)
	}
}

/* ------------------------- failure and overflow -------------------------- */

// TestCompactionFailureIsRecoverable covers R-A5-10: a normal compaction
// failure keeps the original context, records an event and never fails the
// whole run.
func TestCompactionFailureIsRecoverable(t *testing.T) {
	ctx := context.Background()
	publisher := &fakeCompactionPublisher{}
	manager := agentcontext.NewCompactionManager(
		agentcontext.WithCompactionClock(newCompactionClock()),
		agentcontext.WithCompactionPublisher(publisher),
	)
	policy := mustPolicy(t, 128000, 8000)
	cause := errors.New("summarizer unavailable")

	_, err := manager.Run(ctx, 31, agentcontext.CompactionTriggerPressure, policy,
		func(context.Context, agentcontext.CompactionTransaction) (agentcontext.CompactionOutcome, error) {
			return agentcontext.CompactionOutcome{}, cause
		})
	if !errors.Is(err, cause) || !errors.Is(err, agentcontext.ErrCompactionFailed) {
		t.Fatalf("Run() error = %v, want the cause and ErrCompactionFailed", err)
	}
	if !agentcontext.IsRecoverableCompactionError(err) {
		t.Fatalf("Run() error = %v, want a recoverable compaction failure", err)
	}
	if errors.Is(err, agentcontext.ErrContextOverflowUnrecoverable) {
		t.Fatalf("Run() error = %v, must not be classified as unrecoverable overflow", err)
	}
	if len(publisher.events) != 1 || publisher.events[0].Err == "" {
		t.Fatalf("published failure events = %+v, want one event with an error", publisher.events)
	}
	if publisher.events[0].RetainTokens != policy.Retain {
		t.Fatalf("failure event retain = %d, want %d", publisher.events[0].RetainTokens, policy.Retain)
	}
	if _, err := manager.Start(ctx, 31, agentcontext.CompactionTriggerPressure, policy); err != nil {
		t.Fatalf("Start() after a failed compaction error = %v, want the lock released", err)
	}
}

// TestUnrecoverableOverflowFailsCompaction covers R-A5-11: only an overflow that
// cannot be recovered fails the operation, and the typed diagnostic is
// preserved.
func TestUnrecoverableOverflowFailsCompaction(t *testing.T) {
	ctx := context.Background()
	publisher := &fakeCompactionPublisher{}
	manager := agentcontext.NewCompactionManager(
		agentcontext.WithCompactionClock(newCompactionClock()),
		agentcontext.WithCompactionPublisher(publisher),
	)
	policy := mustPolicy(t, 128000, 8000)
	overflow := &agentcontext.ContextOverflowError{
		Policy:            policy,
		PromptTokens:      150000,
		LargestUnitTokens: 130000,
		Suggestion:        "split the input or raise the model context window",
	}

	_, err := manager.Run(ctx, 32, agentcontext.CompactionTriggerOverflow, policy,
		func(context.Context, agentcontext.CompactionTransaction) (agentcontext.CompactionOutcome, error) {
			return agentcontext.CompactionOutcome{}, overflow
		})
	if !errors.Is(err, agentcontext.ErrContextOverflowUnrecoverable) {
		t.Fatalf("Run() error = %v, want ErrContextOverflowUnrecoverable", err)
	}
	if agentcontext.IsRecoverableCompactionError(err) {
		t.Fatalf("Run() error = %v, must not be recoverable", err)
	}
	var typed *agentcontext.ContextOverflowError
	if !errors.As(err, &typed) {
		t.Fatalf("Run() error = %#v, want *ContextOverflowError", err)
	}
	if len(publisher.events) != 1 || publisher.events[0].Err == "" {
		t.Fatalf("published failure events = %+v, want one event with an error", publisher.events)
	}
}

/* ------------------- fix round 1: compaction stop contract ---------------- */

// TestCompactionStopIsRecoverable pins the R-A5-10 contract: a best-effort
// compaction stop must not fail the run. Thrashing (R-A5-11), a bare busy Start
// and a busy Run are recoverable; unrecoverable overflow, configuration errors
// and a canceled context are not.
func TestCompactionStopIsRecoverable(t *testing.T) {
	ctx := context.Background()
	manager := agentcontext.NewCompactionManager(agentcontext.WithCompactionClock(newCompactionClock()))
	policy := mustPolicy(t, 128000, 8000)

	run := func(conversationID int64, before, after int) error {
		_, err := manager.Run(ctx, conversationID, agentcontext.CompactionTriggerPressure, policy,
			func(context.Context, agentcontext.CompactionTransaction) (agentcontext.CompactionOutcome, error) {
				return agentcontext.CompactionOutcome{
					BeforeTokens: before,
					AfterTokens:  after,
					RetainTokens: policy.Retain,
				}, nil
			})
		return err
	}
	if err := run(71, 1000, 1000); err != nil {
		t.Fatalf("first non-reducing compaction error = %v, want nil", err)
	}
	thrashing := run(71, 1000, 1000)
	if !errors.Is(thrashing, agentcontext.ErrCompactionThrashing) {
		t.Fatalf("second non-reducing compaction error = %v, want ErrCompactionThrashing", thrashing)
	}
	if !agentcontext.IsRecoverableCompactionError(thrashing) {
		t.Fatalf("thrashing error = %v, want a best-effort recoverable stop", thrashing)
	}
	if errors.Is(thrashing, agentcontext.ErrContextOverflowUnrecoverable) {
		t.Fatalf("thrashing error = %v, must not be classified as unrecoverable overflow", thrashing)
	}

	// A second Start sees the live transaction and returns a bare busy error
	// outside Run; it belongs to the same recoverable stop class.
	if _, err := manager.Start(ctx, 72, agentcontext.CompactionTriggerPressure, policy); err != nil {
		t.Fatalf("first Start() error = %v", err)
	}
	busyStart, err := manager.Start(ctx, 72, agentcontext.CompactionTriggerPressure, policy)
	if !errors.Is(err, agentcontext.ErrCompactionBusy) {
		t.Fatalf("second Start() error = %v, want ErrCompactionBusy", err)
	}
	if busyStart.ID != "" {
		t.Fatalf("busy Start() returned transaction %+v, want the zero value", busyStart)
	}
	if !agentcontext.IsRecoverableCompactionError(err) {
		t.Fatalf("bare busy Start() error = %v, want a best-effort recoverable stop", err)
	}
	if _, err := manager.Run(ctx, 72, agentcontext.CompactionTriggerPressure, policy,
		func(context.Context, agentcontext.CompactionTransaction) (agentcontext.CompactionOutcome, error) {
			t.Fatal("a busy conversation must not run the compaction operation")
			return agentcontext.CompactionOutcome{}, nil
		}); !errors.Is(err, agentcontext.ErrCompactionBusy) || !agentcontext.IsRecoverableCompactionError(err) {
		t.Fatalf("busy Run() error = %v, want a recoverable ErrCompactionBusy", err)
	}

	// Unrecoverable overflow remains the only terminal compaction error.
	overflow := &agentcontext.ContextOverflowError{
		Policy:            policy,
		PromptTokens:      150000,
		LargestUnitTokens: 130000,
		Suggestion:        "split the input or raise the model context window",
	}
	if _, err := manager.Run(ctx, 73, agentcontext.CompactionTriggerOverflow, policy,
		func(context.Context, agentcontext.CompactionTransaction) (agentcontext.CompactionOutcome, error) {
			return agentcontext.CompactionOutcome{}, overflow
		}); !errors.Is(err, agentcontext.ErrContextOverflowUnrecoverable) ||
		agentcontext.IsRecoverableCompactionError(err) {
		t.Fatalf("unrecoverable overflow error = %v, want a terminal error", err)
	}

	// Configuration errors and a canceled context are not compaction stops.
	invalid := policy
	invalid.Retain = invalid.Trigger
	if err := invalid.Validate(); err == nil || agentcontext.IsRecoverableCompactionError(err) {
		t.Fatalf("invalid policy error = %v, want a terminal configuration error", err)
	}
	if agentcontext.IsRecoverableCompactionError(context.Canceled) {
		t.Fatal("context.Canceled must not be a recoverable compaction stop")
	}
	if agentcontext.IsRecoverableCompactionError(nil) {
		t.Fatal("nil must not be a recoverable compaction stop")
	}
}

// TestFinishRequiresSummaryState pins the R-A5-10 state machine: a started
// transaction cannot jump straight to end, the rejected transaction stays live
// and the proper start → summary → end path still completes.
func TestFinishRequiresSummaryState(t *testing.T) {
	ctx := context.Background()
	publisher := &fakeCompactionPublisher{}
	manager := agentcontext.NewCompactionManager(
		agentcontext.WithCompactionClock(newCompactionClock()),
		agentcontext.WithCompactionPublisher(publisher),
	)
	policy := mustPolicy(t, 128000, 8000)
	transaction, err := manager.Start(ctx, 51, agentcontext.CompactionTriggerPressure, policy)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	outcome := agentcontext.CompactionOutcome{
		BeforeTokens: 1000,
		AfterTokens:  500,
		RetainTokens: policy.Retain,
	}
	if _, err := manager.Finish(ctx, transaction, outcome); !errors.Is(err, agentcontext.ErrCompactionNotStarted) {
		t.Fatalf("Finish(started) error = %v, want ErrCompactionNotStarted", err)
	}
	if len(publisher.events) != 0 {
		t.Fatalf("rejected Finish published events = %+v, want none", publisher.events)
	}
	if active, ok := manager.Active(ctx, 51); !ok || active.State != agentcontext.CompactionStateStarted {
		t.Fatalf("active transaction = %+v, want the rejected start→end to stay live", active)
	}
	summary, err := manager.MarkSummary(ctx, transaction)
	if err != nil {
		t.Fatalf("MarkSummary() error = %v", err)
	}
	if _, err := manager.Finish(ctx, summary, outcome); err != nil {
		t.Fatalf("Finish(summary) error = %v", err)
	}
	if active, ok := manager.Active(ctx, 51); ok {
		t.Fatalf("finished transaction %+v is still active", active)
	}
}

// TestFinishCanceledContextReleasesLock covers R-A5-10 recovery: a Finish
// aborted by a canceled context is treated as a failed compaction and releases
// the live transaction immediately instead of leaking it until the TTL.
func TestFinishCanceledContextReleasesLock(t *testing.T) {
	ctx := context.Background()
	publisher := &fakeCompactionPublisher{}
	manager := agentcontext.NewCompactionManager(
		agentcontext.WithCompactionClock(newCompactionClock()),
		agentcontext.WithCompactionPublisher(publisher),
	)
	policy := mustPolicy(t, 128000, 8000)
	transaction, err := manager.Start(ctx, 52, agentcontext.CompactionTriggerPressure, policy)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	transaction, err = manager.MarkSummary(ctx, transaction)
	if err != nil {
		t.Fatalf("MarkSummary() error = %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := manager.Finish(canceled, transaction, agentcontext.CompactionOutcome{
		BeforeTokens: 1000,
		AfterTokens:  500,
		RetainTokens: policy.Retain,
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Finish(canceled) error = %v, want context.Canceled", err)
	}
	if active, ok := manager.Active(ctx, 52); ok {
		t.Fatalf("canceled Finish leaked active transaction %+v until the TTL", active)
	}
	if _, err := manager.Start(ctx, 52, agentcontext.CompactionTriggerPressure, policy); err != nil {
		t.Fatalf("Start() after canceled Finish error = %v, want the lock released", err)
	}
	if len(publisher.events) != 1 || publisher.events[0].Err == "" {
		t.Fatalf("published failure events = %+v, want one failure event for the aborted Finish", publisher.events)
	}
	if publisher.events[0].RetainTokens != policy.Retain {
		t.Fatalf("aborted Finish event retain = %d, want %d", publisher.events[0].RetainTokens, policy.Retain)
	}
}

// TestFinishRetainFallback covers the context.compacted retain contract: when an
// operation does not report a positive retain budget, the event publishes the
// transaction policy's retain instead of a meaningless zero.
func TestFinishRetainFallback(t *testing.T) {
	ctx := context.Background()
	publisher := &fakeCompactionPublisher{}
	manager := agentcontext.NewCompactionManager(
		agentcontext.WithCompactionClock(newCompactionClock()),
		agentcontext.WithCompactionPublisher(publisher),
	)
	policy := mustPolicy(t, 128000, 8000)

	transaction, err := manager.Start(ctx, 53, agentcontext.CompactionTriggerPressure, policy)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	transaction, err = manager.MarkSummary(ctx, transaction)
	if err != nil {
		t.Fatalf("MarkSummary() error = %v", err)
	}
	event, err := manager.Finish(ctx, transaction, agentcontext.CompactionOutcome{
		BeforeTokens: 1000,
		AfterTokens:  500,
		RetainTokens: 0,
	})
	if err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	if event.RetainTokens != policy.Retain {
		t.Fatalf("event retain = %d, want the policy retain %d", event.RetainTokens, policy.Retain)
	}
	if len(publisher.events) != 1 || publisher.events[0].RetainTokens != policy.Retain {
		t.Fatalf("published events = %+v, want retain %d", publisher.events, policy.Retain)
	}
}

// TestPublisherFailureDoesNotFailCompaction covers the event port contract:
// publishing context.compacted is best effort, so a transport failure never
// fails the compaction or the run.
func TestPublisherFailureDoesNotFailCompaction(t *testing.T) {
	ctx := context.Background()
	publisher := &fakeCompactionPublisher{err: errors.New("event store unavailable")}
	manager := agentcontext.NewCompactionManager(
		agentcontext.WithCompactionClock(newCompactionClock()),
		agentcontext.WithCompactionPublisher(publisher),
		agentcontext.WithCompactionLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	policy := mustPolicy(t, 128000, 8000)
	outcome, err := manager.Run(ctx, 81, agentcontext.CompactionTriggerPressure, policy,
		func(context.Context, agentcontext.CompactionTransaction) (agentcontext.CompactionOutcome, error) {
			return agentcontext.CompactionOutcome{
				BeforeTokens: 1000,
				AfterTokens:  500,
				RetainTokens: policy.Retain,
			}, nil
		})
	if err != nil {
		t.Fatalf("Run() with a failing publisher error = %v, want nil", err)
	}
	if outcome.AfterTokens != 500 {
		t.Fatalf("outcome = %+v, want the compaction outcome", outcome)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("published events = %d, want the single attempted event", len(publisher.events))
	}
}

/* ----------------- fix round 1: overflow decision contract ---------------- */

// TestOverflowDecisionUsesOverflowBudget covers R-A5-2/R-A5-11: the
// decision-aware entry point runs the transaction with the halved 8% overflow
// retain (9600 at 128K) and carries the overflow retry budget; the manager also
// enforces the overflow policy when an overflow decision carries only the normal
// policy.
func TestOverflowDecisionUsesOverflowBudget(t *testing.T) {
	ctx := context.Background()
	manager := agentcontext.NewCompactionManager(agentcontext.WithCompactionClock(newCompactionClock()))
	policy := mustPolicy(t, 128000, 8000)
	decision := policy.OverflowDecision(-1)
	if decision.Retain != 9600 {
		t.Fatalf("overflow decision retain = %d, want 9600", decision.Retain)
	}

	var observed agentcontext.CompactionTransaction
	outcome, err := manager.RunDecision(ctx, 61, decision,
		func(_ context.Context, transaction agentcontext.CompactionTransaction) (agentcontext.CompactionOutcome, error) {
			observed = transaction
			return agentcontext.CompactionOutcome{
				BeforeTokens: 118000,
				AfterTokens:  40000,
				RetainTokens: transaction.Policy.Retain,
			}, nil
		})
	if err != nil {
		t.Fatalf("RunDecision() error = %v", err)
	}
	if observed.Trigger != agentcontext.CompactionTriggerOverflow {
		t.Fatalf("transaction trigger = %q, want overflow", observed.Trigger)
	}
	if observed.Policy != policy.OverflowPolicy() || observed.Policy.Retain != 9600 {
		t.Fatalf("transaction policy = %+v, want the 8%% overflow policy", observed.Policy)
	}
	if observed.MaxRetries != 1 {
		t.Fatalf("transaction retry budget = %d, want the v0.22 overflow default 1", observed.MaxRetries)
	}
	if outcome.RetainTokens != 9600 {
		t.Fatalf("outcome retain = %d, want 9600", outcome.RetainTokens)
	}

	// An overflow trigger forces the 8% policy even when the decision only
	// carries the normal policy.
	transaction, err := manager.StartDecision(ctx, 62, agentcontext.CompactionDecision{
		Trigger: agentcontext.CompactionTriggerOverflow,
		Policy:  policy,
	})
	if err != nil {
		t.Fatalf("StartDecision(overflow) error = %v", err)
	}
	if transaction.Policy != policy.OverflowPolicy() || transaction.Policy.Retain != 9600 {
		t.Fatalf("normalized overflow policy = %+v, want the 8%% overflow policy", transaction.Policy)
	}
	if err := transaction.Policy.Validate(); err != nil {
		t.Fatalf("normalized overflow policy is invalid: %v", err)
	}
}
