package execution

import "testing"

func TestQuotaCrossSystemReservation(t *testing.T) {
	quota := Quota{Global: 3, PerSUT: 2, CrossSystem: 1}

	cases := []struct {
		name       string
		running    []RunningCase
		candidate  RunningCase
		capacity   int
		active     int
		contexts   int
		wantOK     bool
		wantReason string
	}{
		{
			name:      "single SUT candidate fits",
			candidate: RunningCase{CaseRunID: 1, VersionSUTIDs: []int64{10}},
			capacity:  8,
			contexts:  1,
			wantOK:    true,
		},
		{
			name:       "global concurrency exhausted",
			running:    []RunningCase{{CaseRunID: 1, VersionSUTIDs: []int64{10}}, {CaseRunID: 2, VersionSUTIDs: []int64{11}}, {CaseRunID: 3, VersionSUTIDs: []int64{12}}},
			candidate:  RunningCase{CaseRunID: 4, VersionSUTIDs: []int64{13}},
			capacity:   8,
			contexts:   1,
			wantOK:     false,
			wantReason: ReasonGlobalConcurrency,
		},
		{
			name:       "per SUT concurrency exhausted",
			running:    []RunningCase{{CaseRunID: 1, VersionSUTIDs: []int64{10}}, {CaseRunID: 2, VersionSUTIDs: []int64{10}}},
			candidate:  RunningCase{CaseRunID: 3, VersionSUTIDs: []int64{10}},
			capacity:   8,
			contexts:   1,
			wantOK:     false,
			wantReason: ReasonPerSUTConcurrency,
		},
		{
			name:      "cross-system candidate needs every SUT slot",
			candidate: RunningCase{CaseRunID: 1, VersionSUTIDs: []int64{10, 20}, CrossSystem: true},
			capacity:  8,
			contexts:  2,
			wantOK:    true,
		},
		{
			name:       "cross-system candidate rejected when one SUT is full",
			running:    []RunningCase{{CaseRunID: 1, VersionSUTIDs: []int64{10}}, {CaseRunID: 2, VersionSUTIDs: []int64{10}}},
			candidate:  RunningCase{CaseRunID: 3, VersionSUTIDs: []int64{10, 20}, CrossSystem: true},
			capacity:   8,
			contexts:   2,
			wantOK:     false,
			wantReason: ReasonPerSUTConcurrency,
		},
		{
			name:       "cross-system concurrency exhausted",
			running:    []RunningCase{{CaseRunID: 1, VersionSUTIDs: []int64{10, 20}, CrossSystem: true}},
			candidate:  RunningCase{CaseRunID: 2, VersionSUTIDs: []int64{11, 21}, CrossSystem: true},
			capacity:   8,
			contexts:   2,
			wantOK:     false,
			wantReason: ReasonCrossSystemConcurrency,
		},
		{
			name:       "executor capacity insufficient",
			running:    nil,
			candidate:  RunningCase{CaseRunID: 1, VersionSUTIDs: []int64{10, 20}, CrossSystem: true},
			capacity:   2,
			active:     1,
			contexts:   2,
			wantOK:     false,
			wantReason: ReasonExecutorCapacity,
		},
		{
			name:      "zero capacity means unknown and does not block",
			candidate: RunningCase{CaseRunID: 1, VersionSUTIDs: []int64{10}},
			capacity:  0,
			contexts:  1,
			wantOK:    true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := CanDispatch(quota, tc.running, tc.candidate, tc.capacity, tc.active, tc.contexts)
			if ok != tc.wantOK || reason != tc.wantReason {
				t.Fatalf("got ok=%v reason=%q, want ok=%v reason=%q", ok, reason, tc.wantOK, tc.wantReason)
			}
		})
	}
}

func TestQuotaReservesAllCrossSystemSlotsAtomically(t *testing.T) {
	// One free slot on sut 10, one free slot on sut 20, but the cross-system
	// candidate must be treated as one reservation of both slots.
	quota := Quota{Global: 3, PerSUT: 2, CrossSystem: 1}
	running := []RunningCase{
		{CaseRunID: 1, VersionSUTIDs: []int64{10}},
		{CaseRunID: 2, VersionSUTIDs: []int64{20}},
	}
	candidate := RunningCase{CaseRunID: 3, VersionSUTIDs: []int64{10, 20}, CrossSystem: true}
	if ok, reason := CanDispatch(quota, running, candidate, 8, 2, 2); !ok {
		t.Fatalf("cross-system candidate must fit: %s", reason)
	}
	running = append(running, RunningCase{CaseRunID: 4, VersionSUTIDs: []int64{10}})
	if ok, reason := CanDispatch(quota, running, candidate, 8, 3, 2); ok {
		t.Fatalf("cross-system candidate must not partially reserve: %s", reason)
	}
}
