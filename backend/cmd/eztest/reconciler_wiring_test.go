package main

// Composition-root tests for the event-driven background loops: leadership
// acquisition must wake the reconciler, and the compatibility environment
// switches must parse "disabled" consistently.

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
)

// acquisitionTestGate is a mutable leader gate that also exposes the
// acquisition hook the composition root registers on the real Leader. The hook
// is captured so a test can simulate the advisory lock being acquired without
// a database.
type acquisitionTestGate struct {
	mu        sync.Mutex
	leader    bool
	callbacks []func()
}

func (g *acquisitionTestGate) IsLeader() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.leader
}

// OnAcquired matches the production Leader's optional acquisition surface.
func (g *acquisitionTestGate) OnAcquired(callback func()) {
	if callback == nil {
		return
	}
	g.mu.Lock()
	alreadyLeading := g.leader
	g.callbacks = append(g.callbacks, callback)
	g.mu.Unlock()
	if alreadyLeading {
		callback()
	}
}

func (g *acquisitionTestGate) acquire() {
	g.mu.Lock()
	g.leader = true
	callbacks := append([]func(){}, g.callbacks...)
	g.mu.Unlock()
	for _, callback := range callbacks {
		callback()
	}
}

func (g *acquisitionTestGate) hasCallback() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.callbacks) > 0
}

// countingDesignReconciler counts reconciliation passes without touching a
// database; every other reconciler dependency stays nil.
type countingDesignReconciler struct {
	mu    sync.Mutex
	calls int
}

func (d *countingDesignReconciler) Reconcile(context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	return nil
}

func (d *countingDesignReconciler) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func waitUntil(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !condition() {
		t.Fatal(message)
	}
}

// TestLeaderAcquisitionReconciles pins the failover wake: runReconciler must
// register on the leader gate and reconcile when this replica acquires the
// advisory lock, even when no event and no compatibility poll exists.
func TestLeaderAcquisitionReconciles(t *testing.T) {
	gate := &acquisitionTestGate{}
	design := &countingDesignReconciler{}
	reconciler := agentapp.NewReconciler(agentapp.ReconcilerDeps{
		Design:   design,
		Debounce: 5 * time.Millisecond,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		runReconciler(ctx, gate, reconciler)
		close(done)
	}()

	waitUntil(t, 2*time.Second, gate.hasCallback,
		"runReconciler did not register a leader-acquisition wake")
	if calls := design.callCount(); calls != 0 {
		t.Fatalf("reconcile ran %d times before leadership, want 0", calls)
	}

	gate.acquire()
	waitUntil(t, 2*time.Second, func() bool { return design.callCount() >= 1 },
		"leader acquisition did not wake the reconciler")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runReconciler did not stop on context cancellation")
	}
}

// TestReconcilerPollIntervalSwitch pins the compatibility switch parsing: all
// disabled spellings (including the duration form "0s") mean fully
// event-driven, and an unparseable value keeps the old 3s fallback.
func TestReconcilerPollIntervalSwitch(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"", 0},
		{"0", 0},
		{"0s", 0},
		{"off", 0},
		{"OFF", 0},
		{"3s", 3 * time.Second},
		{"500ms", 500 * time.Millisecond},
		{"bogus", 3 * time.Second},
	}
	for _, testCase := range cases {
		t.Run(testCase.raw, func(t *testing.T) {
			t.Setenv("EZTEST_RECONCILER_POLL_INTERVAL", testCase.raw)
			if got := reconcilerPollInterval(); got != testCase.want {
				t.Fatalf("reconcilerPollInterval(%q) = %v, want %v", testCase.raw, got, testCase.want)
			}
		})
	}
}
