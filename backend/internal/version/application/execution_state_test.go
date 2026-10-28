package application_test

import (
	"context"
	"sync"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// beforeExecutionWrite, when set, runs once at the start of the next fake
// execution-state write. Tests use it to commit a concurrent write between the
// service's read and its write, exercising the column-scoped guards.
var beforeExecutionWrite func(id int64)

// fakeAllocatorMu serialises the in-memory native allocate/release writes so
// the concurrent allocation test exercises the service wiring without racing on
// the fake map. The real atomicity guarantee is the single PostgreSQL UPDATE,
// covered by the repository integration test.
var fakeAllocatorMu sync.Mutex

// nativeBatchAllocator mirrors the execution adapter's optional native allocator
// port (T17). The adapter prefers this method set over its serialised
// read-then-write fallback, so a signature drift must fail here.
type nativeBatchAllocator interface {
	AllocateBatchNo(ctx context.Context, versionID int64) (int, error)
	ReleaseBatchNo(ctx context.Context, versionID int64, allocated int) error
}

// The production version service must keep satisfying the adapter's native
// allocator so T17 actually uses the atomic path.
var _ nativeBatchAllocator = (*application.VersionService)(nil)

// runExecutionHook runs and clears the one-shot concurrency hook.
func (r *fakeVersionRepo) runExecutionHook(id int64) {
	hook := beforeExecutionWrite
	beforeExecutionWrite = nil
	if hook != nil {
		hook(id)
	}
}

// SetPaused mirrors the PostgreSQL column-scoped pause write: only
// paused/pause_mode change, and only an executing or continuing version accepts
// the write. batch_no is never read or written here.
func (r *fakeVersionRepo) SetPaused(_ context.Context, id int64, paused bool, pauseMode string, actor int64) (*domain.Version, error) {
	r.runExecutionHook(id)
	stored, ok := r.versions[id]
	if !ok || (stored.Status != domain.VersionExecuting && stored.Status != domain.VersionContinuing) {
		return nil, domain.NewStateConflict("version_state_conflict", "version status does not allow this action")
	}
	stored.Paused = paused
	stored.PauseMode = pauseMode
	stored.Revision++
	stored.UpdatedBy = actor
	stored.UpdatedAt = r.stamp(id, stored.Revision)
	r.versions[id] = stored
	return &stored, nil
}

// SetBatchNo mirrors the PostgreSQL monotonic batch write: only batch_no
// changes, a lower number loses to the stored value (rows=0 => conflict), and
// an equal number is accepted by the SQL guard. The service short-circuits the
// equal/no-op case before calling here. paused is never read or written.
func (r *fakeVersionRepo) SetBatchNo(_ context.Context, id int64, batchNo int, actor int64) (*domain.Version, error) {
	r.runExecutionHook(id)
	stored, ok := r.versions[id]
	if !ok || (stored.Status != domain.VersionExecuting && stored.Status != domain.VersionContinuing) {
		return nil, domain.NewStateConflict("version_state_conflict", "version status does not allow this action")
	}
	if stored.BatchNo > batchNo {
		return nil, domain.NewStateConflict("version_state_conflict", "version batchNo does not allow this action")
	}
	stored.BatchNo = batchNo
	stored.Revision++
	stored.UpdatedBy = actor
	stored.UpdatedAt = r.stamp(id, stored.Revision)
	r.versions[id] = stored
	return &stored, nil
}

// AllocateBatchNo mirrors the PostgreSQL single-statement allocation: the
// executing version's batch_no is incremented in one step and the new value is
// returned. A missing row is not_found, a non-executing row is a state conflict.
func (r *fakeVersionRepo) AllocateBatchNo(_ context.Context, id int64, actor int64) (int, error) {
	r.runExecutionHook(id)
	fakeAllocatorMu.Lock()
	defer fakeAllocatorMu.Unlock()
	stored, ok := r.versions[id]
	if !ok {
		return 0, domain.NewNotFound("version", id)
	}
	if stored.Status != domain.VersionExecuting {
		return 0, domain.NewStateConflict("version_state_conflict", "version status does not allow this action")
	}
	stored.BatchNo++
	stored.Revision++
	stored.UpdatedBy = actor
	stored.UpdatedAt = r.stamp(id, stored.Revision)
	r.versions[id] = stored
	return stored.BatchNo, nil
}

// ReleaseBatchNo mirrors the guarded compensation: batch_no is decremented only
// while the stored value still equals the allocated one; any other state is a
// no-op (the number was already compensated or overtaken).
func (r *fakeVersionRepo) ReleaseBatchNo(_ context.Context, id int64, allocated int, actor int64) error {
	fakeAllocatorMu.Lock()
	defer fakeAllocatorMu.Unlock()
	stored, ok := r.versions[id]
	if !ok || stored.BatchNo != allocated {
		return nil
	}
	stored.BatchNo--
	stored.Revision++
	stored.UpdatedBy = actor
	stored.UpdatedAt = r.stamp(id, stored.Revision)
	r.versions[id] = stored
	return nil
}

// CompleteExecution mirrors the guarded completion: only an unpaused executing
// version may complete. A paused or non-executing row loses as a conflict.
func (r *fakeVersionRepo) CompleteExecution(_ context.Context, id int64, actor int64) (*domain.Version, error) {
	r.runExecutionHook(id)
	stored, ok := r.versions[id]
	if !ok || stored.Status != domain.VersionExecuting || stored.Paused {
		return nil, domain.NewStateConflict("version_state_conflict", "version status does not allow this action")
	}
	stored.Status = domain.VersionCompleted
	stored.Revision++
	stored.UpdatedBy = actor
	stored.UpdatedAt = r.stamp(id, stored.Revision)
	r.versions[id] = stored
	return &stored, nil
}

// UpdateExecutionState is the legacy all-three-column port method. The service
// no longer calls it; the fake mirrors the PostgreSQL compatibility shim by
// applying the column-scoped writes instead of replacing from a stale read.
func (r *fakeVersionRepo) UpdateExecutionState(ctx context.Context, id int64, paused bool, pauseMode string, batchNo int, actor int64) (*domain.Version, error) {
	if _, err := r.SetPaused(ctx, id, paused, pauseMode, actor); err != nil {
		return nil, err
	}
	return r.SetBatchNo(ctx, id, batchNo, actor)
}

// seedExecuting stores an executing version with the P6 execution defaults.
func seedExecuting(f *versionFixture) domain.Version {
	return f.repo.seed(domain.Version{
		ID: 100, Name: "Order v2", Status: domain.VersionExecuting,
		PauseMode: domain.PauseModeGraceful, CreatedBy: 1, UpdatedBy: 1,
	})
}

// assertPauseEvent asserts the version.status_changed event MarkPaused /
// CompleteExecution publishes for an execution-state action.
func assertPauseEvent(t *testing.T, event application.VersionEvent, versionID int64, from, to string, paused bool) {
	t.Helper()
	if event.Type != "version.status_changed" || event.VersionID != versionID ||
		event.ActorID != 42 || event.Source != domain.SourceUser {
		t.Fatalf("event = %+v", event)
	}
	if event.Detail["from"] != from || event.Detail["to"] != to || event.Detail["paused"] != paused {
		t.Fatalf("event detail = %+v, want from=%s to=%s paused=%v", event.Detail, from, to, paused)
	}
}

func TestMarkPausedRequiresExecutingAndMirrorsState(t *testing.T) {
	f := newVersionFixture()
	stored := seedExecuting(f)

	paused, err := f.service.MarkPaused(context.Background(), stored.ID, true, domain.PauseModeGraceful, "user", 42)
	if err != nil {
		t.Fatalf("MarkPaused: %v", err)
	}
	if !paused.Paused || paused.PauseMode != domain.PauseModeGraceful {
		t.Fatalf("paused/mode = %v/%s, want true/graceful", paused.Paused, paused.PauseMode)
	}
	if paused.Status != domain.VersionExecuting {
		t.Fatalf("pause changed status to %s, want executing", paused.Status)
	}
	if paused.Revision != 2 || paused.UpdatedBy != 42 {
		t.Fatalf("revision/updatedBy = %d/%d, want 2/42", paused.Revision, paused.UpdatedBy)
	}
	if len(f.events.events) != 1 {
		t.Fatalf("events = %d, want 1 (%+v)", len(f.events.events), f.events.events)
	}
	assertPauseEvent(t, f.events.events[0], stored.ID, domain.VersionExecuting, domain.VersionExecuting, true)

	// Completion requires an unpaused executing version, so resume first.
	resumed, err := f.service.MarkPaused(context.Background(), stored.ID, false, "", "", 42)
	if err != nil {
		t.Fatalf("MarkPaused resume: %v", err)
	}
	if resumed.Paused || resumed.Revision != 3 {
		t.Fatalf("resumed paused/revision = %v/%d, want false/3", resumed.Paused, resumed.Revision)
	}

	completed, err := f.service.CompleteExecution(context.Background(), stored.ID, 42)
	if err != nil {
		t.Fatalf("CompleteExecution: %v", err)
	}
	if completed.Status != domain.VersionCompleted || completed.Revision != 4 {
		t.Fatalf("status/revision = %s/%d, want completed/4", completed.Status, completed.Revision)
	}
	if got, err := f.repo.Get(context.Background(), stored.ID); err != nil || got.Status != domain.VersionCompleted {
		t.Fatalf("stored version = %+v err=%v, want completed", got, err)
	}
	if len(f.events.events) != 3 {
		t.Fatalf("events = %d, want 3 (%+v)", len(f.events.events), f.events.events)
	}
	assertPauseEvent(t, f.events.events[1], stored.ID, domain.VersionExecuting, domain.VersionExecuting, false)
	assertPauseEvent(t, f.events.events[2], stored.ID, domain.VersionExecuting, domain.VersionCompleted, false)
}

func TestMarkPausedAllowsContinuingAndResume(t *testing.T) {
	f := newVersionFixture()
	stored := f.repo.seed(domain.Version{
		ID: 100, Name: "Order v2", Status: domain.VersionContinuing,
		PauseMode: domain.PauseModeGraceful, CreatedBy: 1, UpdatedBy: 1,
	})

	paused, err := f.service.MarkPaused(context.Background(), stored.ID, true, domain.PauseModeImmediate, "restart", 42)
	if err != nil {
		t.Fatalf("MarkPaused continuing: %v", err)
	}
	if !paused.Paused || paused.PauseMode != domain.PauseModeImmediate || paused.Status != domain.VersionContinuing {
		t.Fatalf("paused/mode/status = %v/%s/%s", paused.Paused, paused.PauseMode, paused.Status)
	}

	resumed, err := f.service.MarkPaused(context.Background(), stored.ID, false, "", "", 42)
	if err != nil {
		t.Fatalf("MarkPaused resume: %v", err)
	}
	if resumed.Paused || resumed.Status != domain.VersionContinuing {
		t.Fatalf("resumed paused/status = %v/%s, want false/continuing", resumed.Paused, resumed.Status)
	}
	if resumed.PauseMode != domain.PauseModeImmediate {
		t.Fatalf("resume changed pause mode to %q, want the stored immediate", resumed.PauseMode)
	}
	if len(f.events.events) != 2 {
		t.Fatalf("events = %d, want 2", len(f.events.events))
	}
	assertPauseEvent(t, f.events.events[1], stored.ID, domain.VersionContinuing, domain.VersionContinuing, false)
}

func TestMarkPausedRejectsNonExecutableState(t *testing.T) {
	for _, status := range []string{
		domain.VersionDraft, domain.VersionClarifying, domain.VersionDesigning,
		domain.VersionReviewing, domain.VersionCompleted, domain.VersionArchived,
	} {
		t.Run(status, func(t *testing.T) {
			f := newVersionFixture()
			stored := f.repo.seed(domain.Version{
				ID: 100, Name: "Order v2", Status: status,
				PauseMode: domain.PauseModeGraceful, CreatedBy: 1, UpdatedBy: 1,
			})

			_, err := f.service.MarkPaused(context.Background(), stored.ID, true, domain.PauseModeGraceful, "user", 42)

			assertDomainReason(t, err, domain.KindStateConflict, "version_state_conflict")
			if got := f.repo.versions[stored.ID]; got.Paused || got.Revision != 1 {
				t.Fatalf("blocked pause wrote the row: %+v", got)
			}
			if len(f.events.events) != 0 {
				t.Fatalf("blocked pause published events: %+v", f.events.events)
			}
		})
	}
}

func TestMarkPausedValidatesPauseMode(t *testing.T) {
	for _, mode := range []string{"", "fast", "GRACEFUL"} {
		t.Run("mode "+mode, func(t *testing.T) {
			f := newVersionFixture()
			stored := seedExecuting(f)

			_, err := f.service.MarkPaused(context.Background(), stored.ID, true, mode, "user", 42)

			assertDomainReason(t, err, domain.KindInvalid, "invalid_pause_mode")
			if got := f.repo.versions[stored.ID]; got.Paused || got.Revision != 1 {
				t.Fatalf("invalid mode wrote the row: %+v", got)
			}
			if len(f.events.events) != 0 {
				t.Fatalf("invalid mode published events: %+v", f.events.events)
			}
		})
	}
}

func TestSetBatchNoIsMonotonic(t *testing.T) {
	t.Run("increase stores the batch number without changing status", func(t *testing.T) {
		f := newVersionFixture()
		stored := seedExecuting(f)

		updated, err := f.service.SetBatchNo(context.Background(), stored.ID, 3, 42)
		if err != nil {
			t.Fatalf("SetBatchNo: %v", err)
		}
		if updated.BatchNo != 3 || updated.Status != domain.VersionExecuting {
			t.Fatalf("batchNo/status = %d/%s, want 3/executing", updated.BatchNo, updated.Status)
		}
		if updated.Revision != 2 || updated.UpdatedBy != 42 {
			t.Fatalf("revision/updatedBy = %d/%d, want 2/42", updated.Revision, updated.UpdatedBy)
		}
		if len(f.events.events) != 0 {
			t.Fatalf("SetBatchNo published events: %+v", f.events.events)
		}
	})

	t.Run("decrease is a 409 and writes nothing", func(t *testing.T) {
		f := newVersionFixture()
		stored := seedExecuting(f)
		if _, err := f.service.SetBatchNo(context.Background(), stored.ID, 3, 42); err != nil {
			t.Fatalf("SetBatchNo seed: %v", err)
		}
		before := f.repo.versions[stored.ID]

		_, err := f.service.SetBatchNo(context.Background(), stored.ID, 2, 42)

		assertDomainReason(t, err, domain.KindStateConflict, "version_state_conflict")
		after := f.repo.versions[stored.ID]
		if after.BatchNo != 3 || after.Revision != before.Revision || after.UpdatedBy != before.UpdatedBy {
			t.Fatalf("decrease wrote the row: %+v", after)
		}
	})

	t.Run("equal batch number is idempotent", func(t *testing.T) {
		f := newVersionFixture()
		stored := seedExecuting(f)
		if _, err := f.service.SetBatchNo(context.Background(), stored.ID, 3, 42); err != nil {
			t.Fatalf("SetBatchNo seed: %v", err)
		}
		before := f.repo.versions[stored.ID]

		updated, err := f.service.SetBatchNo(context.Background(), stored.ID, 3, 43)
		if err != nil {
			t.Fatalf("SetBatchNo equal: %v", err)
		}
		if updated.BatchNo != 3 || updated.Revision != before.Revision || updated.UpdatedBy != before.UpdatedBy {
			t.Fatalf("equal batch number rewrote the row: %+v", updated)
		}
	})

	t.Run("negative batch number is a 422", func(t *testing.T) {
		f := newVersionFixture()
		stored := seedExecuting(f)

		_, err := f.service.SetBatchNo(context.Background(), stored.ID, -1, 42)

		assertDomainReason(t, err, domain.KindInvalid, "invalid_batch_no")
		if got := f.repo.versions[stored.ID]; got.BatchNo != 0 || got.Revision != 1 {
			t.Fatalf("negative batch number wrote the row: %+v", got)
		}
	})
}

func TestCompleteExecutionOnlyFromExecuting(t *testing.T) {
	t.Run("executing completes with a status_changed event", func(t *testing.T) {
		f := newVersionFixture()
		stored := seedExecuting(f)

		completed, err := f.service.CompleteExecution(context.Background(), stored.ID, 42)
		if err != nil {
			t.Fatalf("CompleteExecution: %v", err)
		}
		if completed.Status != domain.VersionCompleted || completed.Revision != 2 {
			t.Fatalf("status/revision = %s/%d, want completed/2", completed.Status, completed.Revision)
		}
		if len(f.events.events) != 1 {
			t.Fatalf("events = %d, want 1", len(f.events.events))
		}
		assertPauseEvent(t, f.events.events[0], stored.ID, domain.VersionExecuting, domain.VersionCompleted, false)
	})

	t.Run("continuing stays reserved for P7", func(t *testing.T) {
		f := newVersionFixture()
		stored := f.repo.seed(domain.Version{
			ID: 100, Name: "Order v2", Status: domain.VersionContinuing,
			PauseMode: domain.PauseModeGraceful, CreatedBy: 1, UpdatedBy: 1,
		})

		_, err := f.service.CompleteExecution(context.Background(), stored.ID, 42)

		assertDomainReason(t, err, domain.KindStateConflict, "version_state_conflict")
		if got := f.repo.versions[stored.ID]; got.Status != domain.VersionContinuing {
			t.Fatalf("status = %s, want continuing", got.Status)
		}
		if len(f.events.events) != 0 {
			t.Fatalf("blocked complete published events: %+v", f.events.events)
		}
	})

	for _, status := range []string{
		domain.VersionDraft, domain.VersionClarifying, domain.VersionDesigning,
		domain.VersionReviewing, domain.VersionCompleted, domain.VersionArchived,
	} {
		t.Run("from "+status, func(t *testing.T) {
			f := newVersionFixture()
			stored := f.repo.seed(domain.Version{
				ID: 100, Name: "Order v2", Status: status,
				PauseMode: domain.PauseModeGraceful, CreatedBy: 1, UpdatedBy: 1,
			})

			_, err := f.service.CompleteExecution(context.Background(), stored.ID, 42)

			assertDomainReason(t, err, domain.KindStateConflict, "version_state_conflict")
			if got := f.repo.versions[stored.ID]; got.Status != status || got.Revision != 1 {
				t.Fatalf("blocked complete wrote the row: %+v", got)
			}
			if len(f.events.events) != 0 {
				t.Fatalf("blocked complete published events: %+v", f.events.events)
			}
		})
	}

	t.Run("repeat complete is a conflict", func(t *testing.T) {
		f := newVersionFixture()
		stored := seedExecuting(f)
		if _, err := f.service.CompleteExecution(context.Background(), stored.ID, 42); err != nil {
			t.Fatalf("first complete: %v", err)
		}
		before := f.repo.versions[stored.ID]

		_, err := f.service.CompleteExecution(context.Background(), stored.ID, 42)

		assertDomainReason(t, err, domain.KindStateConflict, "version_state_conflict")
		if got := f.repo.versions[stored.ID]; got.Status != domain.VersionCompleted || got.Revision != before.Revision {
			t.Fatalf("repeat complete wrote the row: %+v", got)
		}
		if len(f.events.events) != 1 {
			t.Fatalf("repeat complete published events: %+v", f.events.events)
		}
	})

	t.Run("missing version is 404", func(t *testing.T) {
		f := newVersionFixture()

		_, err := f.service.CompleteExecution(context.Background(), 999, 42)

		assertDomainReason(t, err, domain.KindNotFound, "not_found")
	})
}

// TestMarkPausedDoesNotOverwriteConcurrentBatchNo is the stale-read regression
// for the pause path: MarkPaused reads batch_no only for its event/detail, and
// a batch committed between that read and the pause write must survive.
func TestMarkPausedDoesNotOverwriteConcurrentBatchNo(t *testing.T) {
	f := newVersionFixture()
	stored := seedExecuting(f)
	beforeExecutionWrite = func(id int64) {
		if _, err := f.repo.SetBatchNo(context.Background(), id, 5, 43); err != nil {
			t.Fatalf("concurrent SetBatchNo: %v", err)
		}
	}
	t.Cleanup(func() { beforeExecutionWrite = nil })

	paused, err := f.service.MarkPaused(context.Background(), stored.ID, true, domain.PauseModeGraceful, "user", 42)
	if err != nil {
		t.Fatalf("MarkPaused: %v", err)
	}
	if !paused.Paused || paused.BatchNo != 5 {
		t.Fatalf("pause wrote a stale batchNo: %+v", paused)
	}
	if got := f.repo.versions[stored.ID]; !got.Paused || got.BatchNo != 5 {
		t.Fatalf("stored row = %+v, want paused with batchNo 5", got)
	}
}

// TestSetBatchNoDoesNotClearConcurrentPause is the stale-read regression for
// the batch path: a pause committed between SetBatchNo's read and its write
// must survive because the batch write is scoped to batch_no alone.
func TestSetBatchNoDoesNotClearConcurrentPause(t *testing.T) {
	f := newVersionFixture()
	stored := seedExecuting(f)
	beforeExecutionWrite = func(id int64) {
		if _, err := f.repo.SetPaused(context.Background(), id, true, domain.PauseModeImmediate, 43); err != nil {
			t.Fatalf("concurrent MarkPaused: %v", err)
		}
	}
	t.Cleanup(func() { beforeExecutionWrite = nil })

	updated, err := f.service.SetBatchNo(context.Background(), stored.ID, 3, 42)
	if err != nil {
		t.Fatalf("SetBatchNo: %v", err)
	}
	if updated.BatchNo != 3 || !updated.Paused || updated.PauseMode != domain.PauseModeImmediate {
		t.Fatalf("batch write cleared a concurrent pause: %+v", updated)
	}
	if got := f.repo.versions[stored.ID]; got.BatchNo != 3 || !got.Paused {
		t.Fatalf("stored row = %+v, want batchNo 3 with paused preserved", got)
	}
}

// TestSetBatchNoLosesToConcurrentHigherBatch proves the monotonic guard is
// enforced at write time, not only against the service's earlier read.
func TestSetBatchNoLosesToConcurrentHigherBatch(t *testing.T) {
	f := newVersionFixture()
	stored := seedExecuting(f)
	beforeExecutionWrite = func(id int64) {
		if _, err := f.repo.SetBatchNo(context.Background(), id, 5, 43); err != nil {
			t.Fatalf("concurrent SetBatchNo: %v", err)
		}
	}
	t.Cleanup(func() { beforeExecutionWrite = nil })

	_, err := f.service.SetBatchNo(context.Background(), stored.ID, 3, 42)

	assertDomainReason(t, err, domain.KindStateConflict, "version_state_conflict")
	if got := f.repo.versions[stored.ID]; got.BatchNo != 5 {
		t.Fatalf("stale batch write rolled the row back to %d, want 5", got.BatchNo)
	}
}

// TestAllocateBatchNoIncrementsAtomically covers the native allocation path:
// every call increments the executing version's batch_no exactly once and
// returns a distinct value, even when callers race.
func TestAllocateBatchNoIncrementsAtomically(t *testing.T) {
	f := newVersionFixture()
	stored := seedExecuting(f)

	const callers = 2
	values := make([]int, callers)
	errs := make([]error, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			values[i], errs[i] = f.service.AllocateBatchNo(context.Background(), stored.ID)
		}(i)
	}
	close(start)
	wg.Wait()

	seen := make(map[int]bool, callers)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("AllocateBatchNo[%d]: %v", i, err)
		}
		if values[i] < 1 || values[i] > callers {
			t.Fatalf("AllocateBatchNo[%d] = %d, want 1..%d", i, values[i], callers)
		}
		if seen[values[i]] {
			t.Fatalf("duplicate batch number %d in %v", values[i], values)
		}
		seen[values[i]] = true
	}

	got := f.repo.versions[stored.ID]
	if got.BatchNo != callers || got.Status != domain.VersionExecuting {
		t.Fatalf("stored batchNo/status = %d/%s, want %d/executing", got.BatchNo, got.Status, callers)
	}
	if got.Revision != 1+callers {
		t.Fatalf("revision = %d, want one bump per allocation = %d", got.Revision, 1+callers)
	}
	if got.UpdatedBy != 0 {
		t.Fatalf("updatedBy = %d, want the adapter's system actor 0", got.UpdatedBy)
	}
	if len(f.events.events) != 0 {
		t.Fatalf("AllocateBatchNo published events: %+v", f.events.events)
	}
}

// TestReleaseBatchNoRestoresOnlyMatchingValue covers the compensating
// decrement: only the current newest number is handed back, a stale or unknown
// number is a no-op, and a missing version is tolerated because the number was
// already compensated.
func TestReleaseBatchNoRestoresOnlyMatchingValue(t *testing.T) {
	f := newVersionFixture()
	stored := seedExecuting(f)
	ctx := context.Background()

	first, err := f.service.AllocateBatchNo(ctx, stored.ID)
	if err != nil {
		t.Fatalf("AllocateBatchNo first: %v", err)
	}
	second, err := f.service.AllocateBatchNo(ctx, stored.ID)
	if err != nil {
		t.Fatalf("AllocateBatchNo second: %v", err)
	}
	if first != 1 || second != 2 {
		t.Fatalf("allocated = %d/%d, want 1/2", first, second)
	}

	// Releasing the stale first number must not decrement the newest one.
	if err := f.service.ReleaseBatchNo(ctx, stored.ID, first); err != nil {
		t.Fatalf("ReleaseBatchNo stale: %v", err)
	}
	if got := f.repo.versions[stored.ID]; got.BatchNo != second {
		t.Fatalf("stale release changed batchNo to %d, want %d", got.BatchNo, second)
	}

	// Releasing the still-current number restores the previous value.
	if err := f.service.ReleaseBatchNo(ctx, stored.ID, second); err != nil {
		t.Fatalf("ReleaseBatchNo current: %v", err)
	}
	if got := f.repo.versions[stored.ID]; got.BatchNo != first {
		t.Fatalf("release left batchNo %d, want %d", got.BatchNo, first)
	}

	// Repeating the compensation and releasing an unknown number are no-ops.
	if err := f.service.ReleaseBatchNo(ctx, stored.ID, second); err != nil {
		t.Fatalf("ReleaseBatchNo repeat: %v", err)
	}
	if err := f.service.ReleaseBatchNo(ctx, stored.ID, 99); err != nil {
		t.Fatalf("ReleaseBatchNo unknown: %v", err)
	}
	if got := f.repo.versions[stored.ID]; got.BatchNo != first {
		t.Fatalf("no-op releases changed batchNo to %d, want %d", got.BatchNo, first)
	}

	// A version that disappeared between allocation and compensation is fine.
	if err := f.service.ReleaseBatchNo(ctx, 999, 1); err != nil {
		t.Fatalf("ReleaseBatchNo missing version: %v", err)
	}
}

// TestAllocateBatchNoRequiresExecuting covers the status guard and the missing
// version mapping of the native allocation.
func TestAllocateBatchNoRequiresExecuting(t *testing.T) {
	ctx := context.Background()
	for _, status := range []string{
		domain.VersionDraft, domain.VersionClarifying, domain.VersionDesigning,
		domain.VersionReviewing, domain.VersionContinuing, domain.VersionCompleted,
		domain.VersionArchived,
	} {
		t.Run(status, func(t *testing.T) {
			f := newVersionFixture()
			stored := f.repo.seed(domain.Version{
				ID: 100, Name: "Order v2", Status: status,
				PauseMode: domain.PauseModeGraceful, CreatedBy: 1, UpdatedBy: 1,
			})

			_, err := f.service.AllocateBatchNo(ctx, stored.ID)

			assertDomainReason(t, err, domain.KindStateConflict, "version_state_conflict")
			if got := f.repo.versions[stored.ID]; got.BatchNo != 0 || got.Revision != 1 {
				t.Fatalf("blocked allocation wrote the row: %+v", got)
			}
		})
	}

	t.Run("missing version is 404", func(t *testing.T) {
		f := newVersionFixture()

		_, err := f.service.AllocateBatchNo(ctx, 999)

		assertDomainReason(t, err, domain.KindNotFound, "not_found")
	})
}

// TestCompleteExecutionRejectsPausedVersion covers the completion guard from
// both the persisted flag and the service's own pause action.
func TestCompleteExecutionRejectsPausedVersion(t *testing.T) {
	t.Run("paused through MarkPaused", func(t *testing.T) {
		f := newVersionFixture()
		stored := seedExecuting(f)
		if _, err := f.service.MarkPaused(context.Background(), stored.ID, true, domain.PauseModeGraceful, "user", 42); err != nil {
			t.Fatalf("MarkPaused: %v", err)
		}
		before := f.repo.versions[stored.ID]

		_, err := f.service.CompleteExecution(context.Background(), stored.ID, 42)

		assertDomainReason(t, err, domain.KindStateConflict, "version_state_conflict")
		if got := f.repo.versions[stored.ID]; got.Status != domain.VersionExecuting ||
			!got.Paused || got.Revision != before.Revision {
			t.Fatalf("paused complete wrote the row: %+v", got)
		}
		if len(f.events.events) != 1 {
			t.Fatalf("paused complete published events: %+v", f.events.events)
		}
	})

	t.Run("stored paused flag", func(t *testing.T) {
		f := newVersionFixture()
		stored := f.repo.seed(domain.Version{
			ID: 100, Name: "Order v2", Status: domain.VersionExecuting,
			Paused: true, PauseMode: domain.PauseModeImmediate, CreatedBy: 1, UpdatedBy: 1,
		})

		_, err := f.service.CompleteExecution(context.Background(), stored.ID, 42)

		assertDomainReason(t, err, domain.KindStateConflict, "version_state_conflict")
		if got := f.repo.versions[stored.ID]; got.Status != domain.VersionExecuting || got.Revision != 1 {
			t.Fatalf("paused complete wrote the row: %+v", got)
		}
		if len(f.events.events) != 0 {
			t.Fatalf("paused complete published events: %+v", f.events.events)
		}
	})
}

// TestCompleteExecutionRejectsConcurrentPause closes the read-then-write race
// on completion: the service read sees paused=false, but a pause that commits
// before the completion write must still win and turn the action into a 409.
func TestCompleteExecutionRejectsConcurrentPause(t *testing.T) {
	f := newVersionFixture()
	stored := seedExecuting(f)
	beforeExecutionWrite = func(id int64) {
		if _, err := f.repo.SetPaused(context.Background(), id, true, domain.PauseModeGraceful, 43); err != nil {
			t.Fatalf("concurrent MarkPaused: %v", err)
		}
	}
	t.Cleanup(func() { beforeExecutionWrite = nil })

	_, err := f.service.CompleteExecution(context.Background(), stored.ID, 42)

	assertDomainReason(t, err, domain.KindStateConflict, "version_state_conflict")
	if got := f.repo.versions[stored.ID]; got.Status != domain.VersionExecuting || !got.Paused {
		t.Fatalf("concurrent pause lost to the completion: %+v", got)
	}
	if len(f.events.events) != 0 {
		t.Fatalf("rejected complete published events: %+v", f.events.events)
	}
}
