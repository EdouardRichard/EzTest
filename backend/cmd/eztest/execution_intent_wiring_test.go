package main

// Tests for the leader-gated execution intent scan. The scan is only a startup/
// compatibility repair path since Task 78: initial intents are created by the
// explicit execute.batch stage job at executing, never by a case confirmation.
// Since Task 82 the scan is selection-scoped; this file pins the wake/interval
// scheduling, while the scoping itself is tested in the execution application.

import (
	"context"
	"sync"
	"testing"
	"time"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

type fakeIntentEnsurer struct {
	mu    sync.Mutex
	calls map[int64]int
}

func (f *fakeIntentEnsurer) EnsureInitialIntents(_ context.Context, versionID int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = make(map[int64]int)
	}
	f.calls[versionID]++
	return 0, nil
}

func (f *fakeIntentEnsurer) count(versionID int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[versionID]
}

// fakeVersionSummaryLister is the full-scan version projection.
type fakeVersionSummaryLister struct {
	mu   sync.Mutex
	list []versionapp.VersionSummary
	err  error
}

func (f *fakeVersionSummaryLister) List(_ context.Context, _ int, _ *int64) ([]versionapp.VersionSummary, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]versionapp.VersionSummary(nil), f.list...), false, f.err
}

// TestIntentScanDoesNotWakePerVersion pins the Task 82 wake contract: the
// per-version case-confirmed wake was removed with Task 78 (confirmations must
// not create intents), so the only repair trigger is one leader-gated full scan.
// A startup scan still runs once for the listed versions.
func TestIntentScanDoesNotWakePerVersion(t *testing.T) {
	intents := &fakeIntentEnsurer{}
	versions := &fakeVersionSummaryLister{list: []versionapp.VersionSummary{{ID: 42}, {ID: 99}}}
	wake := newExecutionIntentWake()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		runExecutionScheduler(ctx, p7FakeLeaderGate(true), intents, versions, nil, nil, wake, 0)
		close(done)
	}()

	// The startup request is a single full scan, not a per-version wake.
	waitUntil(t, 2*time.Second, func() bool { return intents.count(42) >= 1 && intents.count(99) >= 1 },
		"the startup repair scan did not cover every listed version")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runExecutionScheduler did not stop on context cancellation")
	}
}

// TestIntentScanCompatibilityInterval pins the switch: with the interval unset
// nothing scans periodically, and a positive EZTEST_INTENT_SCAN_INTERVAL
// restores the old periodic full scan.
func TestIntentScanCompatibilityInterval(t *testing.T) {
	t.Run("interval off is event-driven", func(t *testing.T) {
		intents := &fakeIntentEnsurer{}
		versions := &fakeVersionSummaryLister{list: []versionapp.VersionSummary{{ID: 7}}}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		go func() {
			// A nil wake disables the startup scan too, so any Ensure call can
			// only come from a periodic poll.
			runExecutionScheduler(ctx, p7FakeLeaderGate(true), intents, versions, nil, nil, nil, 0)
			close(done)
		}()
		time.Sleep(80 * time.Millisecond)
		if count := intents.count(7); count != 0 {
			t.Fatalf("version 7 scanned %d times with the interval off", count)
		}
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("runExecutionScheduler did not stop on context cancellation")
		}
	})

	t.Run("positive interval restores polling", func(t *testing.T) {
		intents := &fakeIntentEnsurer{}
		versions := &fakeVersionSummaryLister{list: []versionapp.VersionSummary{{ID: 7}}}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		go func() {
			runExecutionScheduler(ctx, p7FakeLeaderGate(true), intents, versions, nil, nil, nil, 5*time.Millisecond)
			close(done)
		}()
		waitUntil(t, 2*time.Second, func() bool { return intents.count(7) >= 1 },
			"compatibility interval did not run the periodic intent scan")
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("runExecutionScheduler did not stop on context cancellation")
		}
	})
}

// TestIntentScanIntervalSwitch pins the environment parsing, including the
// duration spellings of "disabled".
func TestIntentScanIntervalSwitch(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"", 0},
		{"0", 0},
		{"0s", 0},
		{"off", 0},
		{"5s", 5 * time.Second},
		{"250ms", 250 * time.Millisecond},
		{"bogus", 0},
		{"-1s", 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.raw, func(t *testing.T) {
			t.Setenv("EZTEST_INTENT_SCAN_INTERVAL", testCase.raw)
			if got := intentScanIntervalFromEnv(); got != testCase.want {
				t.Fatalf("intentScanIntervalFromEnv(%q) = %v, want %v", testCase.raw, got, testCase.want)
			}
		})
	}
}
