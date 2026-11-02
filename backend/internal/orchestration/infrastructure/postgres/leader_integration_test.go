//go:build integration

package orchestrationpg_test

import (
	"context"
	"testing"
	"time"

	orchestrationpg "github.com/EdouardRichard/EzTest/backend/internal/orchestration/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

func waitUntil(t *testing.T, timeout time.Duration, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLeaderIsMutuallyExclusiveAndFailsOver(t *testing.T) {
	pool := testutil.NewDB(t)
	leaderA := orchestrationpg.NewLeader(pool, orchestrationpg.WithLeaderRetry(50*time.Millisecond))
	leaderB := orchestrationpg.NewLeader(pool, orchestrationpg.WithLeaderRetry(50*time.Millisecond))
	if leaderA.IsLeader() || leaderB.IsLeader() {
		t.Fatal("a leader claimed leadership before Run")
	}

	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelA()
	defer cancelB()
	runA := make(chan error, 1)
	runB := make(chan error, 1)
	go func() { runA <- leaderA.Run(ctxA) }()
	go func() { runB <- leaderB.Run(ctxB) }()

	waitUntil(t, 5*time.Second, "exactly one leader", func() bool {
		return leaderA.IsLeader() != leaderB.IsLeader()
	})
	firstIsA := leaderA.IsLeader()
	if firstIsA {
		cancelA()
	} else {
		cancelB()
	}

	// The surviving replica must take over once the leader's session closes.
	waitUntil(t, 5*time.Second, "leader failover", func() bool {
		if firstIsA {
			return leaderB.IsLeader()
		}
		return leaderA.IsLeader()
	})
	if firstIsA && leaderA.IsLeader() {
		t.Fatal("cancelled leader still reports leadership")
	}

	if firstIsA {
		select {
		case err := <-runA:
			if err != nil {
				t.Fatalf("leader A Run = %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("leader A did not stop")
		}
		cancelB()
		select {
		case err := <-runB:
			if err != nil {
				t.Fatalf("leader B Run = %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("leader B did not stop")
		}
		return
	}

	select {
	case err := <-runB:
		if err != nil {
			t.Fatalf("leader B Run = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("leader B did not stop")
	}
	cancelA()
	select {
	case err := <-runA:
		if err != nil {
			t.Fatalf("leader A Run = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("leader A did not stop")
	}
}
