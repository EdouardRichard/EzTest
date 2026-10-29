package application

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// discardMaintenanceLogger keeps the new tests' output pristine.
func discardMaintenanceLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeTickLock is an in-memory TickLock. It records every attempt and every
// release so a test can assert the tick was gated and the lease was returned.
type fakeTickLock struct {
	mu       sync.Mutex
	ok       bool
	err      error
	calls    int
	released int
	lastKey  int64
}

func newFakeTickLock(ok bool) *fakeTickLock { return &fakeTickLock{ok: ok} }

func (f *fakeTickLock) Try(_ context.Context, key int64) (func(), bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastKey = key
	if f.err != nil {
		return nil, false, f.err
	}
	if !f.ok {
		return nil, false, nil
	}
	return func() {
		f.mu.Lock()
		f.released++
		f.mu.Unlock()
	}, true, nil
}

func (f *fakeTickLock) stats() (calls, released int, lastKey int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.released, f.lastKey
}

// TestLeaderLoopKeysAreDistinct pins the per-loop key derivation: instances
// exclude each other on the same loop, while the scheduler, autonomy and
// maintenance loops of one instance never block each other (a long autonomy
// tick must not starve dispatch).
func TestLeaderLoopKeysAreDistinct(t *testing.T) {
	base := int64(1163411512)
	keys := []int64{SchedulerLeaderKey(base), AutonomyLeaderKey(base), MaintenanceLeaderKey(base)}
	if keys[0] != base {
		t.Fatalf("scheduler key = %d, want the configured base %d", keys[0], base)
	}
	seen := make(map[int64]struct{}, len(keys))
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			t.Fatalf("leader loop keys are not distinct: %v", keys)
		}
		seen[key] = struct{}{}
	}
}

// ---------------------------------------------------------------------------
// Execution scheduler: one tick per leader
// ---------------------------------------------------------------------------

// TestSchedulerSkipsTickWithoutLeaderLock pins spec §10.5: a non-leader
// instance must not dispatch anything, and it must not release a lock it never
// acquired.
func TestSchedulerSkipsTickWithoutLeaderLock(t *testing.T) {
	scheduler, world := newSchedulerTest(t, 1)
	world.seedPending(1, "P0")
	lock := newFakeTickLock(false)
	scheduler.deps.Leader = lock
	scheduler.deps.LeaderKey = 4242

	if err := scheduler.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if started := world.startedOrder(); len(started) != 0 {
		t.Fatalf("started case runs = %v, want none while another instance holds the lock", started)
	}
	calls, released, lastKey := lock.stats()
	if calls != 1 || lastKey != 4242 {
		t.Fatalf("lock attempts=(%d, key %d), want (1, 4242)", calls, lastKey)
	}
	if released != 0 {
		t.Fatalf("released %d times without holding the lock, want 0", released)
	}
}

// TestSchedulerHoldsLeaderLockForEachTick asserts the lock covers exactly one
// tick and is released afterwards: the next tick must attempt the lock again.
func TestSchedulerHoldsLeaderLockForEachTick(t *testing.T) {
	scheduler, world := newSchedulerTest(t, 1)
	world.seedPending(1, "P0")
	lock := newFakeTickLock(true)
	scheduler.deps.Leader = lock
	scheduler.deps.LeaderKey = 4242

	if err := scheduler.RunOnce(context.Background()); err != nil {
		t.Fatalf("first RunOnce: %v", err)
	}
	world.waitStarted(t, 1)
	calls, released, _ := lock.stats()
	if calls != 1 || released != 1 {
		t.Fatalf("after first tick attempts=%d releases=%d, want 1/1", calls, released)
	}

	if err := scheduler.RunOnce(context.Background()); err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	calls, released, _ = lock.stats()
	if calls != 2 || released != 2 {
		t.Fatalf("after second tick attempts=%d releases=%d, want 2/2", calls, released)
	}
}

// TestSchedulerPropagatesLeaderLockError surfaces an infrastructure failure
// instead of silently skipping the tick forever.
func TestSchedulerPropagatesLeaderLockError(t *testing.T) {
	scheduler, world := newSchedulerTest(t, 1)
	world.seedPending(1, "P0")
	lock := newFakeTickLock(true)
	lock.err = errors.New("database unavailable")
	scheduler.deps.Leader = lock

	err := scheduler.RunOnce(context.Background())
	if err == nil || !errors.Is(err, lock.err) {
		t.Fatalf("RunOnce error = %v, want %v", err, lock.err)
	}
	if started := world.startedOrder(); len(started) != 0 {
		t.Fatalf("started case runs = %v, want none after a lock error", started)
	}
}

// ---------------------------------------------------------------------------
// Autonomous worker: one tick per leader
// ---------------------------------------------------------------------------

// TestAutonomyWorkerSkipsTickWithoutLeaderLock pins the same single-active
// guarantee for the autonomous chain: a non-leader does not even scan its
// work queue.
func TestAutonomyWorkerSkipsTickWithoutLeaderLock(t *testing.T) {
	world := newAutoWorld(t)
	lock := newFakeTickLock(false)
	world.worker.deps.Leader = lock
	world.worker.deps.LeaderKey = AutonomyLeaderKey(5150)

	if err := world.worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if scans := world.store.pendingCallCount(); scans != 0 {
		t.Fatalf("pending batch scans = %d, want 0 while another instance holds the lock", scans)
	}
	if calls, released, lastKey := lock.stats(); calls != 1 || released != 0 || lastKey != AutonomyLeaderKey(5150) {
		t.Fatalf("lock attempts=%d releases=%d key=%d, want 1/0/%d", calls, released, lastKey, AutonomyLeaderKey(5150))
	}
}

// TestAutonomyWorkerRunsTickUnderLeaderLockAndReleases asserts the leader
// advances the stored chain and returns the lock at the end of the tick.
func TestAutonomyWorkerRunsTickUnderLeaderLockAndReleases(t *testing.T) {
	world := newAutoWorld(t)
	lock := newFakeTickLock(true)
	world.worker.deps.Leader = lock
	world.worker.deps.LeaderKey = AutonomyLeaderKey(5150)

	if err := world.worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if scans := world.store.pendingCallCount(); scans == 0 {
		t.Fatal("leader tick did not scan the autonomy work queue")
	}
	if calls, released, _ := lock.stats(); calls != 1 || released != 1 {
		t.Fatalf("lock attempts=%d releases=%d, want 1/1", calls, released)
	}
}

// TestAutonomyWorkerPropagatesLeaderLockError keeps the tick error visible for
// the loop's warning log.
func TestAutonomyWorkerPropagatesLeaderLockError(t *testing.T) {
	world := newAutoWorld(t)
	lock := newFakeTickLock(true)
	lock.err = errors.New("database unavailable")
	world.worker.deps.Leader = lock

	if err := world.worker.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce = nil, want the lock error")
	}
}

// ---------------------------------------------------------------------------
// Daily maintenance entry
// ---------------------------------------------------------------------------

// fakeMaintenanceGC records the GC request of one maintenance tick.
type fakeMaintenanceGC struct {
	calls   int
	last    GCRequest
	report  GCReport
	err     error
	started chan struct{}
}

func (f *fakeMaintenanceGC) Run(_ context.Context, req GCRequest) (GCReport, error) {
	f.calls++
	f.last = req
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	return f.report, f.err
}

// TestDailyMaintenanceOnlyRunsOnTheLeader pins the "leader only" rule: a
// non-leader must not touch the GC at all.
func TestDailyMaintenanceOnlyRunsOnTheLeader(t *testing.T) {
	gc := &fakeMaintenanceGC{}
	lock := newFakeTickLock(false)
	maintenance := NewDailyMaintenance(MaintenanceDeps{
		GC:        gc,
		Leader:    lock,
		LeaderKey: 6161,
		Logger:    discardMaintenanceLogger(),
	})
	if err := maintenance.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if gc.calls != 0 {
		t.Fatalf("GC calls = %d, want 0 for a non-leader", gc.calls)
	}
	if calls, released, lastKey := lock.stats(); calls != 1 || released != 0 || lastKey != 6161 {
		t.Fatalf("lock attempts=%d releases=%d key=%d, want 1/0/6161", calls, released, lastKey)
	}
}

// TestDailyMaintenanceDefaultsToDryRun pins the safe default of spec §10.2:
// the scheduled maintenance never deletes unless explicitly enabled.
func TestDailyMaintenanceDefaultsToDryRun(t *testing.T) {
	gc := &fakeMaintenanceGC{report: GCReport{DryRun: true, Candidates: 2}}
	lock := newFakeTickLock(true)
	maintenance := NewDailyMaintenance(MaintenanceDeps{
		GC:        gc,
		Leader:    lock,
		LeaderKey: 6161,
		Logger:    discardMaintenanceLogger(),
	})
	if err := maintenance.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if gc.calls != 1 || !gc.last.DryRun {
		t.Fatalf("GC calls=%d dryRun=%v, want 1/true", gc.calls, gc.last.DryRun)
	}
	if _, released, _ := lock.stats(); released != 1 {
		t.Fatalf("lock releases = %d, want 1", released)
	}
}

// TestDailyMaintenancePurgesWhenEnabled follows the explicit production opt-in:
// EZTEST_GC_ENABLED=true turns the scheduled run into a real purge.
func TestDailyMaintenancePurgesWhenEnabled(t *testing.T) {
	gc := &fakeMaintenanceGC{}
	maintenance := NewDailyMaintenance(MaintenanceDeps{
		GC:        gc,
		Leader:    newFakeTickLock(true),
		LeaderKey: 6161,
		Purge:     true,
		Logger:    discardMaintenanceLogger(),
	})
	if err := maintenance.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if gc.calls != 1 || gc.last.DryRun {
		t.Fatalf("GC calls=%d dryRun=%v, want 1/false", gc.calls, gc.last.DryRun)
	}
}

// TestDailyMaintenanceSurfacesErrors verifies the GC and lock failures are
// returned (for the loop's warning log) and a successful run releases the lock.
func TestDailyMaintenanceSurfacesErrors(t *testing.T) {
	lockErr := errors.New("database unavailable")
	maintenance := NewDailyMaintenance(MaintenanceDeps{
		GC:        &fakeMaintenanceGC{},
		Leader:    &fakeTickLock{err: lockErr},
		LeaderKey: 6161,
		Logger:    discardMaintenanceLogger(),
	})
	if err := maintenance.RunOnce(context.Background()); !errors.Is(err, lockErr) {
		t.Fatalf("lock error = %v, want %v", err, lockErr)
	}

	gcErr := errors.New("gc failed")
	lock := newFakeTickLock(true)
	maintenance = NewDailyMaintenance(MaintenanceDeps{
		GC:        &fakeMaintenanceGC{err: gcErr},
		Leader:    lock,
		LeaderKey: 6161,
		Logger:    discardMaintenanceLogger(),
	})
	if err := maintenance.RunOnce(context.Background()); !errors.Is(err, gcErr) {
		t.Fatalf("gc error = %v, want %v", err, gcErr)
	}
	if _, released, _ := lock.stats(); released != 1 {
		t.Fatalf("lock releases = %d, want 1 after the failed GC", released)
	}
}

// TestDailyMaintenanceStartRunsImmediatelyAndStops covers the lifecycle the
// composition root starts and stops: a first pass runs without waiting a full
// day, and Stop is idempotent.
func TestDailyMaintenanceStartRunsImmediatelyAndStops(t *testing.T) {
	gc := &fakeMaintenanceGC{started: make(chan struct{}, 1)}
	lock := newFakeTickLock(true)
	maintenance := NewDailyMaintenance(MaintenanceDeps{
		GC:        gc,
		Leader:    lock,
		LeaderKey: 6161,
		Interval:  time.Hour,
		Logger:    discardMaintenanceLogger(),
	})
	maintenance.Start(context.Background())
	select {
	case <-gc.started:
	case <-time.After(3 * time.Second):
		t.Fatal("maintenance did not run its first pass immediately")
	}
	maintenance.Stop()
	maintenance.Stop()

	if _, released, _ := lock.stats(); released == 0 {
		t.Fatal("maintenance did not release the leader lock before Stop returned")
	}
}

// TestDailyMaintenanceRepeatsOnItsInterval proves the daily loop passes the
// configured interval to RunOnce again instead of stopping after the first
// pass.
func TestDailyMaintenanceRepeatsOnItsInterval(t *testing.T) {
	gc := &fakeMaintenanceGC{started: make(chan struct{}, 8)}
	maintenance := NewDailyMaintenance(MaintenanceDeps{
		GC:        gc,
		Leader:    newFakeTickLock(true),
		LeaderKey: 6161,
		Interval:  10 * time.Millisecond,
		Logger:    discardMaintenanceLogger(),
	})
	maintenance.Start(context.Background())
	defer maintenance.Stop()
	for i := 0; i < 2; i++ {
		select {
		case <-gc.started:
		case <-time.After(3 * time.Second):
			t.Fatalf("maintenance pass %d did not run", i+1)
		}
	}
}

// TestDailyMaintenanceRequiresGC keeps a misconfigured composition root loud.
func TestDailyMaintenanceRequiresGC(t *testing.T) {
	maintenance := NewDailyMaintenance(MaintenanceDeps{Logger: discardMaintenanceLogger()})
	if err := maintenance.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce without a GC service = nil, want an error")
	}
}
