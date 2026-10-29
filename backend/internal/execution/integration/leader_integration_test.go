//go:build integration

package integration_test

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/leader"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// TestLeaderSerializesTicksAcrossInstances simulates two API instances that
// share one PostgreSQL database. Both run the same periodic tick concurrently;
// exactly one executes it, and the loser skips (design spec §10.5).
//
// The tick body stands in for "dispatch the pending batch": it is executed once
// per round, which is the same guarantee the scheduler and the autonomy worker
// get from their leader-gated loops.
func TestLeaderSerializesTicksAcrossInstances(t *testing.T) {
	pool := testutil.OpenRawTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	instanceA := leader.New(pool, "integration-a", logger)
	instanceB := leader.New(pool, "integration-b", logger)
	ctx := context.Background()
	const key = leader.DefaultLockKey
	const rounds = 10

	var executions atomic.Int64
	for round := 0; round < rounds; round++ {
		var attempted sync.WaitGroup
		attempted.Add(2)
		errs := make(chan error, 2)

		tick := func(locker *leader.Locker) {
			release, ok, err := locker.Try(ctx, key)
			attempted.Done()
			if err != nil {
				errs <- err
				return
			}
			if !ok {
				// Another instance is leading this tick: skip it.
				return
			}
			// Hold the lock until the other instance has attempted, so the two
			// ticks really overlap and the loser cannot acquire after the
			// winner released.
			attempted.Wait()
			executions.Add(1)
			release()
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); <-start; tick(instanceA) }()
		go func() { defer wg.Done(); <-start; tick(instanceB) }()
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("round %d: leader-gated tick failed: %v", round, err)
		}
		if got, want := executions.Load(), int64(round+1); got != want {
			t.Fatalf("round %d: executions = %d, want %d (exactly one instance per tick)", round, got, want)
		}
	}
}
