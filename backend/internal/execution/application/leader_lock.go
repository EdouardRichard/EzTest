package application

import (
	"context"
	"fmt"
)

// TickLock serialises one periodic tick across EzTest API instances (design
// spec §10.5). leader.Locker satisfies it; tests use an in-memory fake. A nil
// TickLock keeps the historical single-instance behavior: every tick runs.
type TickLock interface {
	// Try acquires the lock without blocking. When ok is true the caller owns
	// the lock for the tick and must call release exactly once.
	Try(ctx context.Context, key int64) (release func(), ok bool, err error)
}

// The periodic loops derive adjacent advisory keys from the configured
// EZTEST_LEADER_LOCK_KEY. Two API instances still exclude each other on the same
// loop (same key), but the scheduler, autonomy and maintenance ticks of one
// instance never block each other: a long autonomous advance must not skip
// dispatch ticks, and the daily maintenance must not delay either.
const (
	schedulerLoopKeyOffset   int64 = 0
	autonomyLoopKeyOffset    int64 = 1
	maintenanceLoopKeyOffset int64 = 2
)

// SchedulerLeaderKey returns the advisory lock key of the dispatch loop.
func SchedulerLeaderKey(base int64) int64 { return base + schedulerLoopKeyOffset }

// AutonomyLeaderKey returns the advisory lock key of the autonomous worker.
func AutonomyLeaderKey(base int64) int64 { return base + autonomyLoopKeyOffset }

// MaintenanceLeaderKey returns the advisory lock key of the daily maintenance
// loop.
func MaintenanceLeaderKey(base int64) int64 {
	return base + maintenanceLoopKeyOffset
}

// acquireTickLock attempts the multi-instance lock of one tick. A nil lock
// always grants the tick so a single-instance process (or a unit test) is
// unaffected; a configured lock propagates its infrastructure error instead of
// silently disabling the loop.
func acquireTickLock(ctx context.Context, lock TickLock, key int64) (func(), bool, error) {
	if lock == nil {
		return func() {}, true, nil
	}
	release, ok, err := lock.Try(ctx, key)
	if err != nil {
		return func() {}, false, err
	}
	if !ok {
		return func() {}, false, nil
	}
	if release == nil {
		release = func() {}
	}
	return release, true, nil
}

// acquireSchedulerTick is the scheduler's named use of acquireTickLock. The
// returned error names the caller's loop so the composition root's warning log
// identifies which tick failed.
func acquireSchedulerTick(ctx context.Context, deps SchedulerDeps) (func(), bool, error) {
	release, ok, err := acquireTickLock(ctx, deps.Leader, deps.LeaderKey)
	if err != nil {
		return func() {}, false, fmt.Errorf("execution: acquire scheduler leader lock: %w", err)
	}
	return release, ok, nil
}

// acquireAutonomyTick is the autonomous worker's named use of acquireTickLock.
func acquireAutonomyTick(ctx context.Context, deps AutonomyWorkerDeps) (func(), bool, error) {
	release, ok, err := acquireTickLock(ctx, deps.Leader, deps.LeaderKey)
	if err != nil {
		return func() {}, false, fmt.Errorf("execution autonomy: acquire leader lock: %w", err)
	}
	return release, ok, nil
}
