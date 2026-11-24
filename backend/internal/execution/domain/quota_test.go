package execution

import "testing"

// TestSingleGlobalQuotaUnlimitedByDefault pins design P5 (v0.2.4-F, V-0015):
// the retired 3/2/1 three-dimension quota is replaced by ONE global concurrent
// case-run limit whose default is 0 = unlimited. Zero must dispatch freely --
// the old QuotaFromSettings coerced non-positive values back to 3/2/1, which
// made "unlimited" unreachable through every production entry point.
func TestSingleGlobalQuotaUnlimitedByDefault(t *testing.T) {
	quota := QuotaFromSettings(0)
	if quota.Global != 0 {
		t.Fatalf("QuotaFromSettings(0) = %+v, want Global 0 (unlimited): the 3/2/1 fallback is retired", quota)
	}
	// Far more running runs than any retired default (global 3 / per-SUT 2 /
	// cross-system 1). Restoring ANY of those finite defaults -- or a
	// cross-system-style throttle that counts running multi-SUT runs -- must
	// fail this assertion.
	running := make([]RunningCase, 0, 64)
	for i := 0; i < 64; i++ {
		running = append(running, RunningCase{CaseRunID: int64(i + 1)})
	}
	if ok, reason := CanDispatch(quota, running, 0, 0, 1); !ok {
		t.Fatalf("CanDispatch with unlimited quota and %d running = false (%s), want true", len(running), reason)
	}
	// A configured positive limit is honored verbatim (no coercion).
	if got := QuotaFromSettings(5); got.Global != 5 {
		t.Fatalf("QuotaFromSettings(5) = %+v, want Global 5", got)
	}
	// Negative values normalize to unlimited, never to a hidden finite default.
	if got := QuotaFromSettings(-3); got.Global != 0 {
		t.Fatalf("QuotaFromSettings(-3) = %+v, want Global 0", got)
	}
}

// TestQuotaGlobalAndCapacityGates is the dispatch-rule contract after design P5:
// the ONLY concurrency dimension is the single global limit; the structural
// capacity gate and the executor-capacity gate are preserved unchanged
// (v0.2.4-B C1-livelock fixes -- do not regress).
func TestQuotaGlobalAndCapacityGates(t *testing.T) {
	quota := Quota{Global: 3}

	cases := []struct {
		name       string
		running    []RunningCase
		capacity   int
		active     int
		contexts   int
		wantOK     bool
		wantReason string
	}{
		{
			name:     "a candidate fits under the global limit",
			capacity: 8,
			contexts: 1,
			wantOK:   true,
		},
		{
			name:       "global concurrency exhausted",
			running:    []RunningCase{{CaseRunID: 1}, {CaseRunID: 2}, {CaseRunID: 3}},
			capacity:   8,
			contexts:   1,
			wantOK:     false,
			wantReason: ReasonGlobalConcurrency,
		},
		{
			name:       "executor capacity insufficient",
			running:    nil,
			capacity:   2,
			active:     1,
			contexts:   2,
			wantOK:     false,
			wantReason: ReasonExecutorCapacity,
		},
		{
			name:     "zero capacity means unknown and does not block",
			capacity: 0,
			contexts: 1,
			wantOK:   true,
		},
		// Review I-new-1: a reservation larger than the executor's WHOLE capacity
		// is structurally unsatisfiable and must be distinguishable from a busy
		// executor, because only the second one clears by waiting. The first is
		// what left a run pending forever (Attempts and LeaseUntil are both set
		// after the gate, so neither the redispatch budget nor lease-expiry
		// recovery could ever fire).
		{
			name:       "requirement above the whole capacity is structural, not transient",
			running:    nil,
			capacity:   2,
			active:     0,
			contexts:   3,
			wantOK:     false,
			wantReason: ReasonContextRequirementExceedsCapacity,
		},
		{
			name:       "requirement above capacity stays structural even when idle",
			running:    nil,
			capacity:   1,
			active:     0,
			contexts:   2,
			wantOK:     false,
			wantReason: ReasonContextRequirementExceedsCapacity,
		},
		{
			name:       "requirement within capacity but busy is transient",
			running:    nil,
			capacity:   4,
			active:     3,
			contexts:   3,
			wantOK:     false,
			wantReason: ReasonExecutorCapacity,
		},
		// Review round 5, Minor 2 guard: the structural check is hoisted ABOVE the
		// concurrency check. This case is over quota AND over capacity at once, so
		// only the hoisted order reports the structural reason -- which matters
		// because the two reasons imply opposite operator actions (wait, versus
		// raise capacity or cut roles).
		{
			name:       "structural impossibility outranks an exhausted quota",
			running:    []RunningCase{{CaseRunID: 1}, {CaseRunID: 2}, {CaseRunID: 3}},
			capacity:   2,
			active:     0,
			contexts:   3,
			wantOK:     false,
			wantReason: ReasonContextRequirementExceedsCapacity,
		},
		{
			name:     "requirement exactly equal to capacity on an idle executor fits",
			running:  nil,
			capacity: 3,
			active:   0,
			contexts: 3,
			wantOK:   true,
		},
		// Review round 4, Minor 5 (C1 livelock): a caller that cannot say how many
		// contexts a run needs reserves ONE, the minimum -- never a product of
		// roles x bound SUTs, and never zero.
		{
			name:     "unknown context requirement floors to one and fits",
			capacity: 1,
			active:   0,
			contexts: 0,
			wantOK:   true,
		},
		{
			name:       "unknown context requirement floors to one, not zero",
			capacity:   1,
			active:     1,
			contexts:   0,
			wantOK:     false,
			wantReason: ReasonExecutorCapacity,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := CanDispatch(quota, tc.running, tc.capacity, tc.active, tc.contexts)
			if ok != tc.wantOK || reason != tc.wantReason {
				t.Fatalf("got ok=%v reason=%q, want ok=%v reason=%q", ok, reason, tc.wantOK, tc.wantReason)
			}
		})
	}
}

// TestQuotaGlobalLimitCountsEveryRunningRun pins that the single global limit
// counts ALL running case runs, regardless of which or how many SUTs a run
// touches: there is no per-SUT or cross-system bucket to hide in, and a
// multi-SUT run consumes exactly one global slot like any other run.
func TestQuotaGlobalLimitCountsEveryRunningRun(t *testing.T) {
	quota := Quota{Global: 2}
	running := []RunningCase{{CaseRunID: 1}, {CaseRunID: 2}}
	if ok, reason := CanDispatch(quota, running, 8, 0, 1); ok || reason != ReasonGlobalConcurrency {
		t.Fatalf("CanDispatch at the global limit = (%v, %q), want (false, %q)", ok, reason, ReasonGlobalConcurrency)
	}
	if ok, reason := CanDispatch(quota, running[:1], 8, 0, 1); !ok {
		t.Fatalf("CanDispatch below the global limit = false (%s), want true", reason)
	}
}
