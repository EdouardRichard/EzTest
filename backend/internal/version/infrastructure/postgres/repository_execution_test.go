//go:build integration

package postgres_test

import (
	"context"
	"sync"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
)

// TestVersionRepositoryExecutionState covers the P6 execution columns end to
// end: scanner/defaults, the column-scoped pause and batch writes, the monotonic
// batch guard and the guarded completion (spec §3.3/§5.4).
func TestVersionRepositoryExecutionState(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewVersionRepository(pool)

	stored := insertVersion(t, repo, domain.Version{ID: 6001, Name: "Execution state", CreatedBy: 7, UpdatedBy: 7})
	if stored.Paused || stored.PauseMode != domain.PauseModeGraceful || stored.BatchNo != 0 {
		t.Fatalf("new version execution defaults = paused=%v mode=%q batch=%d, want false/graceful/0",
			stored.Paused, stored.PauseMode, stored.BatchNo)
	}

	// The fresh scanner path (Get) sees the same defaults.
	loaded, err := repo.Get(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Get fresh: %v", err)
	}
	if loaded.Paused || loaded.PauseMode != domain.PauseModeGraceful || loaded.BatchNo != 0 {
		t.Fatalf("loaded execution defaults = %+v", loaded)
	}

	if _, err := pool.Exec(ctx, `UPDATE version SET status = 'executing' WHERE id = $1`, stored.ID); err != nil {
		t.Fatalf("set executing: %v", err)
	}

	// SetPaused writes paused/pause_mode only; batch_no keeps its stored value.
	paused, err := repo.SetPaused(ctx, stored.ID, true, domain.PauseModeImmediate, 42)
	if err != nil {
		t.Fatalf("SetPaused: %v", err)
	}
	if !paused.Paused || paused.PauseMode != domain.PauseModeImmediate || paused.BatchNo != 0 ||
		paused.Status != domain.VersionExecuting || paused.Revision != 2 || paused.UpdatedBy != 42 {
		t.Fatalf("paused row = %+v", paused)
	}

	// SetBatchNo writes batch_no only; the concurrent pause flag survives.
	withBatch, err := repo.SetBatchNo(ctx, stored.ID, 4, 43)
	if err != nil {
		t.Fatalf("SetBatchNo: %v", err)
	}
	if withBatch.BatchNo != 4 || !withBatch.Paused || withBatch.PauseMode != domain.PauseModeImmediate ||
		withBatch.Revision != 3 || withBatch.UpdatedBy != 43 {
		t.Fatalf("batch row = %+v", withBatch)
	}

	// The SQL guard is monotonic (batch_no <= $new), so an equal write is
	// accepted while a lower one is a conflict that leaves the row untouched.
	equal, err := repo.SetBatchNo(ctx, stored.ID, 4, 44)
	if err != nil {
		t.Fatalf("SetBatchNo equal: %v", err)
	}
	if equal.BatchNo != 4 || equal.Revision != 4 || !equal.Paused {
		t.Fatalf("equal batch row = %+v", equal)
	}
	_, err = repo.SetBatchNo(ctx, stored.ID, 3, 45)
	assertVersionKind(t, err, domain.KindStateConflict, "version_state_conflict")
	afterDecrease, err := repo.Get(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Get after decrease: %v", err)
	}
	if afterDecrease.BatchNo != 4 || afterDecrease.Revision != 4 || !afterDecrease.Paused {
		t.Fatalf("rejected decrease changed the row: %+v", afterDecrease)
	}

	// Resume rewrites paused only and keeps the batch number.
	resumed, err := repo.SetPaused(ctx, stored.ID, false, domain.PauseModeImmediate, 46)
	if err != nil {
		t.Fatalf("SetPaused resume: %v", err)
	}
	if resumed.Paused || resumed.BatchNo != 4 || resumed.PauseMode != domain.PauseModeImmediate ||
		resumed.Revision != 5 {
		t.Fatalf("resumed row = %+v", resumed)
	}

	// Completion is refused while paused.
	if _, err := repo.SetPaused(ctx, stored.ID, true, domain.PauseModeImmediate, 47); err != nil {
		t.Fatalf("SetPaused before complete: %v", err)
	}
	_, err = repo.CompleteExecution(ctx, stored.ID, 48)
	assertVersionKind(t, err, domain.KindStateConflict, "version_state_conflict")
	afterPausedComplete, err := repo.Get(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Get after paused complete: %v", err)
	}
	if afterPausedComplete.Status != domain.VersionExecuting || !afterPausedComplete.Paused ||
		afterPausedComplete.Revision != 6 {
		t.Fatalf("paused complete changed the row: %+v", afterPausedComplete)
	}

	// An unpaused executing version completes.
	if _, err := repo.SetPaused(ctx, stored.ID, false, domain.PauseModeImmediate, 49); err != nil {
		t.Fatalf("SetPaused before complete: %v", err)
	}
	completed, err := repo.CompleteExecution(ctx, stored.ID, 50)
	if err != nil {
		t.Fatalf("CompleteExecution: %v", err)
	}
	if completed.Status != domain.VersionCompleted || completed.Revision != 8 || completed.Paused {
		t.Fatalf("completed row = %+v", completed)
	}

	// Completing closes both execution-state write paths.
	_, err = repo.SetPaused(ctx, stored.ID, true, domain.PauseModeGraceful, 51)
	assertVersionKind(t, err, domain.KindStateConflict, "version_state_conflict")
	_, err = repo.SetBatchNo(ctx, stored.ID, 5, 51)
	assertVersionKind(t, err, domain.KindStateConflict, "version_state_conflict")
	_, err = repo.CompleteExecution(ctx, stored.ID, 51)
	assertVersionKind(t, err, domain.KindStateConflict, "version_state_conflict")

	// Missing rows are the same state conflict, matching the port contract.
	_, err = repo.SetPaused(ctx, 999999, true, domain.PauseModeGraceful, 44)
	assertVersionKind(t, err, domain.KindStateConflict, "version_state_conflict")
	_, err = repo.SetBatchNo(ctx, 999999, 1, 44)
	assertVersionKind(t, err, domain.KindStateConflict, "version_state_conflict")
	_, err = repo.CompleteExecution(ctx, 999999, 44)
	assertVersionKind(t, err, domain.KindStateConflict, "version_state_conflict")

	// The paused partial index exists on the renamed singular table.
	var indexName *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.idx_versions_paused')::text`).Scan(&indexName); err != nil {
		t.Fatalf("read paused index: %v", err)
	}
	if indexName == nil || *indexName != "idx_versions_paused" {
		t.Fatalf("paused index = %v, want idx_versions_paused", indexName)
	}
}

// TestVersionRepositoryExecutionStateIndependentColumns races pause and batch
// writes on the same executing version: both must commit because neither write
// touches the other's column.
func TestVersionRepositoryExecutionStateIndependentColumns(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewVersionRepository(pool)

	stored := insertVersion(t, repo, domain.Version{ID: 6002, Name: "Execution columns", CreatedBy: 7, UpdatedBy: 7})
	if _, err := pool.Exec(ctx, `UPDATE version SET status = 'executing' WHERE id = $1`, stored.ID); err != nil {
		t.Fatalf("set executing: %v", err)
	}

	start := make(chan struct{})
	var (
		wg                 sync.WaitGroup
		pauseErr, batchErr error
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, pauseErr = repo.SetPaused(ctx, stored.ID, true, domain.PauseModeImmediate, 42)
	}()
	go func() {
		defer wg.Done()
		<-start
		_, batchErr = repo.SetBatchNo(ctx, stored.ID, 7, 43)
	}()
	close(start)
	wg.Wait()

	if pauseErr != nil {
		t.Fatalf("concurrent SetPaused: %v", pauseErr)
	}
	if batchErr != nil {
		t.Fatalf("concurrent SetBatchNo: %v", batchErr)
	}
	got, err := repo.Get(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Paused || got.PauseMode != domain.PauseModeImmediate || got.BatchNo != 7 {
		t.Fatalf("concurrent pause/batch lost a column: %+v", got)
	}
	if got.Status != domain.VersionExecuting || got.Revision != 3 {
		t.Fatalf("status/revision = %s/%d, want executing/3", got.Status, got.Revision)
	}
}

// TestVersionRepositoryExecutionStateBatchMonotonicConcurrent races a lower and
// a higher batch number: the SQL guard must keep the higher value, and any
// rejected write must be a version_state_conflict.
func TestVersionRepositoryExecutionStateBatchMonotonicConcurrent(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewVersionRepository(pool)

	stored := insertVersion(t, repo, domain.Version{ID: 6003, Name: "Batch guard", CreatedBy: 7, UpdatedBy: 7})
	if _, err := pool.Exec(ctx, `UPDATE version SET status = 'executing' WHERE id = $1`, stored.ID); err != nil {
		t.Fatalf("set executing: %v", err)
	}

	batches := []int{5, 3}
	errs := make([]error, len(batches))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, batchNo := range batches {
		wg.Add(1)
		go func(i, batchNo int) {
			defer wg.Done()
			<-start
			_, errs[i] = repo.SetBatchNo(ctx, stored.ID, batchNo, 42)
		}(i, batchNo)
	}
	close(start)
	wg.Wait()

	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
			continue
		}
		assertVersionKind(t, err, domain.KindStateConflict, "version_state_conflict")
	}
	if successes == 0 || successes > len(batches) {
		t.Fatalf("successful writes = %d, want 1..%d", successes, len(batches))
	}
	got, err := repo.Get(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.BatchNo != 5 {
		t.Fatalf("batchNo = %d, want the monotonic maximum 5", got.BatchNo)
	}
	if got.Revision != 1+successes {
		t.Fatalf("revision = %d, want 1+%d", got.Revision, successes)
	}
}

// TestVersionRepositoryAllocateBatchNoAtomic covers the native single-statement
// allocation: racing callers each increment batch_no exactly once and receive a
// distinct number, so the unique (version_id, batch_no) index can only reject a
// genuinely concurrent batch creator.
func TestVersionRepositoryAllocateBatchNoAtomic(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewVersionRepository(pool)

	stored := insertVersion(t, repo, domain.Version{ID: 6005, Name: "Native allocation", CreatedBy: 7, UpdatedBy: 7})
	if _, err := pool.Exec(ctx, `UPDATE version SET status = 'executing' WHERE id = $1`, stored.ID); err != nil {
		t.Fatalf("set executing: %v", err)
	}

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
			values[i], errs[i] = repo.AllocateBatchNo(ctx, stored.ID, 42)
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

	got, err := repo.Get(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.BatchNo != callers || got.Status != domain.VersionExecuting {
		t.Fatalf("batchNo/status = %d/%s, want %d/executing", got.BatchNo, got.Status, callers)
	}
	if got.Revision != 1+callers || got.UpdatedBy != 42 {
		t.Fatalf("revision/updatedBy = %d/%d, want %d/42", got.Revision, got.UpdatedBy, 1+callers)
	}
}

// TestVersionRepositoryReleaseBatchNo covers the guarded compensation: only a
// release whose number is still stored decrements batch_no; stale, repeated and
// unknown releases are no-ops, and a missing version is tolerated.
func TestVersionRepositoryReleaseBatchNo(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewVersionRepository(pool)

	stored := insertVersion(t, repo, domain.Version{ID: 6006, Name: "Native release", CreatedBy: 7, UpdatedBy: 7})
	if _, err := pool.Exec(ctx, `UPDATE version SET status = 'executing' WHERE id = $1`, stored.ID); err != nil {
		t.Fatalf("set executing: %v", err)
	}

	first, err := repo.AllocateBatchNo(ctx, stored.ID, 42)
	if err != nil {
		t.Fatalf("AllocateBatchNo first: %v", err)
	}
	second, err := repo.AllocateBatchNo(ctx, stored.ID, 42)
	if err != nil {
		t.Fatalf("AllocateBatchNo second: %v", err)
	}
	if first != 1 || second != 2 {
		t.Fatalf("allocated = %d/%d, want 1/2", first, second)
	}

	// Retrieving the stale first number must not roll the newest one back.
	if err := repo.ReleaseBatchNo(ctx, stored.ID, first, 43); err != nil {
		t.Fatalf("ReleaseBatchNo stale: %v", err)
	}
	afterStale, err := repo.Get(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Get after stale release: %v", err)
	}
	if afterStale.BatchNo != second {
		t.Fatalf("stale release changed batchNo to %d, want %d", afterStale.BatchNo, second)
	}

	// Releasing the still-current number restores the previous value.
	if err := repo.ReleaseBatchNo(ctx, stored.ID, second, 43); err != nil {
		t.Fatalf("ReleaseBatchNo current: %v", err)
	}
	afterCurrent, err := repo.Get(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Get after current release: %v", err)
	}
	if afterCurrent.BatchNo != first {
		t.Fatalf("release left batchNo %d, want %d", afterCurrent.BatchNo, first)
	}
	if afterCurrent.UpdatedBy != 43 {
		t.Fatalf("updatedBy = %d, want the compensating actor 43", afterCurrent.UpdatedBy)
	}

	// Repeating a compensation or releasing an unknown number is a no-op.
	if err := repo.ReleaseBatchNo(ctx, stored.ID, second, 44); err != nil {
		t.Fatalf("ReleaseBatchNo repeat: %v", err)
	}
	if err := repo.ReleaseBatchNo(ctx, stored.ID, 99, 44); err != nil {
		t.Fatalf("ReleaseBatchNo unknown: %v", err)
	}
	afterNoops, err := repo.Get(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Get after no-op releases: %v", err)
	}
	if afterNoops.BatchNo != first || afterNoops.UpdatedBy != 43 {
		t.Fatalf("no-op releases wrote the row: %+v", afterNoops)
	}

	// A row that disappeared between allocation and compensation is tolerated.
	if err := repo.ReleaseBatchNo(ctx, 999999, 1, 44); err != nil {
		t.Fatalf("ReleaseBatchNo missing version: %v", err)
	}
}

// TestVersionRepositoryAllocateBatchNoGuards covers the allocation status guard
// and the missing-version mapping.
func TestVersionRepositoryAllocateBatchNoGuards(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewVersionRepository(pool)

	draft := insertVersion(t, repo, domain.Version{ID: 6007, Name: "Draft allocation", CreatedBy: 7, UpdatedBy: 7})
	_, err := repo.AllocateBatchNo(ctx, draft.ID, 42)
	assertVersionKind(t, err, domain.KindStateConflict, "version_state_conflict")
	afterDraft, err := repo.Get(ctx, draft.ID)
	if err != nil {
		t.Fatalf("Get draft: %v", err)
	}
	if afterDraft.BatchNo != 0 || afterDraft.Revision != 1 {
		t.Fatalf("blocked allocation wrote the row: %+v", afterDraft)
	}

	_, err = repo.AllocateBatchNo(ctx, 999999, 42)
	assertVersionKind(t, err, domain.KindNotFound, "not_found")
}

// TestVersionRepositoryExecutionStatePauseCompleteRace races a pause against a
// completion: the guarded completion and the status-guarded pause must not both
// win, and the stored row must match the winner.
func TestVersionRepositoryExecutionStatePauseCompleteRace(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewVersionRepository(pool)

	stored := insertVersion(t, repo, domain.Version{ID: 6004, Name: "Pause complete race", CreatedBy: 7, UpdatedBy: 7})
	if _, err := pool.Exec(ctx, `UPDATE version SET status = 'executing' WHERE id = $1`, stored.ID); err != nil {
		t.Fatalf("set executing: %v", err)
	}

	var (
		wg                    sync.WaitGroup
		pauseErr, completeErr error
	)
	start := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, pauseErr = repo.SetPaused(ctx, stored.ID, true, domain.PauseModeGraceful, 42)
	}()
	go func() {
		defer wg.Done()
		<-start
		_, completeErr = repo.CompleteExecution(ctx, stored.ID, 43)
	}()
	close(start)
	wg.Wait()

	got, err := repo.Get(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	switch {
	case pauseErr == nil && completeErr == nil:
		t.Fatalf("pause and completion both won: row=%+v", got)
	case completeErr == nil:
		assertVersionKind(t, pauseErr, domain.KindStateConflict, "version_state_conflict")
		if got.Status != domain.VersionCompleted || got.Paused {
			t.Fatalf("completion won but row = %+v", got)
		}
	case pauseErr == nil:
		assertVersionKind(t, completeErr, domain.KindStateConflict, "version_state_conflict")
		if got.Status != domain.VersionExecuting || !got.Paused {
			t.Fatalf("pause won but row = %+v", got)
		}
	default:
		t.Fatalf("neither pause (%v) nor completion (%v) won", pauseErr, completeErr)
	}
}
