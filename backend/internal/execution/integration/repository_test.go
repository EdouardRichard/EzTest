//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// executionTestTime is a lossless TIMESTAMPTZ reading (UTC, whole seconds).
var executionTestTime = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

const (
	executionTestVersion = int64(7301)
	executionTestActor   = int64(9001)
)

// executionSnapshot builds a complete case snapshot with the given priority.
func executionSnapshot(caseNodeID int64, priority string) domain.CaseSnapshot {
	return domain.CaseSnapshot{
		Node: domain.CaseSnapshotNode{
			ID: caseNodeID, Title: fmt.Sprintf("case %d", caseNodeID), Priority: priority,
			Preconditions: "signed in", TestData: map[string]any{"sku": "A-1"},
		},
		Steps: []domain.CaseSnapshotStep{
			{Seq: 1, Action: "open the list", Expected: "list shown", Data: map[string]any{}},
		},
		Assertions: []domain.CaseSnapshotAssertion{
			{ID: caseNodeID + 10000, StepSeq: 1, Kind: "ui", Target: "#submit",
				Expression: "visible", Expected: "true"},
		},
		Sources:    []domain.CaseSnapshotSource{{RequirementID: 42}},
		GraphHash:  strings.Repeat("a", 64),
		CapturedAt: executionTestTime,
	}
}

// executionBatch builds a fully populated run batch entity.
func executionBatch(id int64, batchNo int, status domain.BatchStatus) domain.RunBatch {
	return domain.RunBatch{
		ID: id, VersionID: executionTestVersion, BatchNo: batchNo, Status: status,
		PauseMode:    domain.PauseModeGraceful,
		BaselineHash: strings.Repeat("b", 64),
		CreatedBy:    executionTestActor,
		CreatedAt:    executionTestTime,
		UpdatedAt:    executionTestTime,
	}
}

// executionRun builds a pending case run whose snapshot carries priority.
func executionRun(id, batchID, caseNodeID int64, priority string) domain.CaseRun {
	snapshot := executionSnapshot(caseNodeID, priority)
	return domain.CaseRun{
		ID: id, BatchID: batchID, VersionID: executionTestVersion, CaseNodeID: caseNodeID,
		CaseRevision: 1, CaseHash: domain.CanonicalCaseHash(snapshot), Snapshot: snapshot,
		Attempt: 1, Status: domain.CaseRunStatusPending,
		CreatedAt: executionTestTime, UpdatedAt: executionTestTime,
	}
}

// createExecutionBatch seeds one batch through the repository.
func createExecutionBatch(t *testing.T, repo application.BatchRepository, batch domain.RunBatch, runs []domain.CaseRun) domain.RunBatch {
	t.Helper()
	created, err := repo.CreateBatchWithRuns(context.Background(), batch, runs)
	if err != nil {
		t.Fatalf("create batch %d: %v", batch.ID, err)
	}
	return created
}

// requireExecutionErrorIs asserts the repository returned the expected
// sentinel. It always asserts: a nil error must fail, never skip.
func requireExecutionErrorIs(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

// TestExecutionBatchRepositoryLifecycle covers batch creation with runs, the
// database-enforced single active batch (also while paused), status and pause
// CAS updates, atomic count updates, failure recording, pagination and the
// version/batch ownership checks.
func TestExecutionBatchRepositoryLifecycle(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewBatchRepository(pool)

	runs := []domain.CaseRun{
		executionRun(8001, 8401, 101, "P1"),
		executionRun(8002, 8401, 102, "P0"),
	}
	created := createExecutionBatch(t, repo, executionBatch(8401, 1, domain.BatchStatusRunning), runs)
	if created.CaseCount != len(runs) {
		t.Fatalf("case_count = %d, want %d", created.CaseCount, len(runs))
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() || created.StartedAt != nil {
		t.Fatalf("created batch timestamps = %+v", created)
	}

	loaded, err := repo.GetBatch(ctx, executionTestVersion, created.ID)
	if err != nil {
		t.Fatalf("GetBatch: %v", err)
	}
	if loaded.ID != created.ID || loaded.Status != domain.BatchStatusRunning ||
		loaded.BatchNo != 1 || loaded.BaselineHash != created.BaselineHash ||
		loaded.CaseCount != len(runs) {
		t.Fatalf("loaded batch = %+v", loaded)
	}
	_, err = repo.GetBatch(ctx, executionTestVersion+1, created.ID)
	requireExecutionErrorIs(t, err, application.ErrBatchNotFound)

	// uk_run_batch_active rejects a second planned/running batch of the version
	// before any run is inserted.
	_, err = repo.CreateBatchWithRuns(ctx, executionBatch(8402, 2, domain.BatchStatusRunning), nil)
	requireExecutionErrorIs(t, err, domain.ErrBatchActive)

	// Pausing keeps status='running', so the batch still occupies the active slot.
	paused, err := repo.SetPaused(ctx, created.ID, true, domain.PauseModeGraceful, "maintenance")
	if err != nil {
		t.Fatalf("SetPaused: %v", err)
	}
	if !paused.Paused || paused.PauseMode != domain.PauseModeGraceful || paused.PauseReason != "maintenance" {
		t.Fatalf("paused batch = %+v", paused)
	}
	_, err = repo.SetPaused(ctx, created.ID, true, domain.PauseModeImmediate, "again")
	requireExecutionErrorIs(t, err, domain.ErrAlreadyPaused)
	_, err = repo.CreateBatchWithRuns(ctx, executionBatch(8403, 3, domain.BatchStatusPlanned), nil)
	requireExecutionErrorIs(t, err, domain.ErrBatchActive)

	resumed, err := repo.SetPaused(ctx, created.ID, false, domain.PauseModeGraceful, "")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.Paused {
		t.Fatalf("resumed batch still paused: %+v", resumed)
	}
	_, err = repo.SetPaused(ctx, created.ID, false, domain.PauseModeGraceful, "")
	requireExecutionErrorIs(t, err, domain.ErrNotPaused)

	// Counts are recomputed from the stored runs, never supplied by the caller;
	// the two pending runs tally as case_count=2 with no terminal outcome.
	counted, err := repo.UpdateCounts(ctx, created.ID)
	if err != nil {
		t.Fatalf("UpdateCounts: %v", err)
	}
	if counted != (domain.BatchCounts{CaseCount: 2}) {
		t.Fatalf("counted tally = %+v", counted)
	}
	// A second recount is idempotent instead of accumulating a stale tally.
	recounted, err := repo.UpdateCounts(ctx, created.ID)
	if err != nil {
		t.Fatalf("UpdateCounts again: %v", err)
	}
	if recounted != counted {
		t.Fatalf("recounted tally = %+v, want %+v", recounted, counted)
	}

	withFailure, err := repo.UpdateBatchFailure(ctx, created.ID, "executor crashed")
	if err != nil {
		t.Fatalf("UpdateBatchFailure: %v", err)
	}
	if withFailure.FailureReason != "executor crashed" {
		t.Fatalf("failure reason = %q", withFailure.FailureReason)
	}

	// A status CAS only applies from the stored status.
	finishedAt := executionTestTime.Add(5 * time.Minute)
	finished, err := repo.UpdateBatchStatus(ctx, created.ID, domain.BatchStatusRunning, domain.BatchStatusDone, &finishedAt)
	if err != nil {
		t.Fatalf("UpdateBatchStatus: %v", err)
	}
	if finished.Status != domain.BatchStatusDone || finished.FinishedAt == nil ||
		!finished.FinishedAt.Equal(finishedAt) {
		t.Fatalf("finished batch = %+v", finished)
	}
	_, err = repo.UpdateBatchStatus(ctx, created.ID, domain.BatchStatusRunning, domain.BatchStatusFailed, nil)
	requireExecutionErrorIs(t, err, domain.ErrBatchFinished)
	// A done batch stays recountable so the finish that ended it can settle the
	// final tally; failed and cancelled batches are not recountable.
	settled, err := repo.UpdateCounts(ctx, created.ID)
	if err != nil {
		t.Fatalf("UpdateCounts after done: %v", err)
	}
	if settled != counted {
		t.Fatalf("settled tally = %+v, want %+v", settled, counted)
	}
	_, err = repo.SetPaused(ctx, created.ID, true, domain.PauseModeGraceful, "late")
	requireExecutionErrorIs(t, err, domain.ErrBatchFinished)

	// The terminal batch released the active slot, so a new batch is accepted.
	second := createExecutionBatch(t, repo, executionBatch(8402, 2, domain.BatchStatusRunning), nil)
	if second.ID != 8402 {
		t.Fatalf("second batch = %+v", second)
	}

	// (version_id, batch_no) is unique regardless of status.
	_, err = repo.CreateBatchWithRuns(ctx, executionBatch(8403, 2, domain.BatchStatusDone), nil)
	requireExecutionErrorIs(t, err, application.ErrConflict)

	// Newest first, id keyset cursor and has_more.
	page, more, err := repo.ListBatches(ctx, executionTestVersion, 0, 1)
	if err != nil {
		t.Fatalf("ListBatches first page: %v", err)
	}
	if len(page) != 1 || page[0].ID != second.ID || !more {
		t.Fatalf("first page ids=%v more=%v", batchIDs(page), more)
	}
	page, more, err = repo.ListBatches(ctx, executionTestVersion, second.ID, 10)
	if err != nil {
		t.Fatalf("ListBatches second page: %v", err)
	}
	if len(page) != 1 || page[0].ID != created.ID || more {
		t.Fatalf("second page ids=%v more=%v", batchIDs(page), more)
	}
	empty, more, err := repo.ListBatches(ctx, executionTestVersion+1, 0, 10)
	if err != nil || len(empty) != 0 || more {
		t.Fatalf("empty list ids=%v more=%v err=%v", batchIDs(empty), more, err)
	}
}

// TestExecutionNextPendingPriorityOrder pins the deterministic dispatch order:
// snapshot priority P0..P3 first, then case_node_id ascending.
func TestExecutionNextPendingPriorityOrder(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	batches := postgres.NewBatchRepository(pool)
	caseRuns := postgres.NewCaseRunRepository(pool)

	runs := []domain.CaseRun{
		executionRun(9001, 9401, 301, "P2"),
		executionRun(9002, 9401, 303, "P0"),
		executionRun(9003, 9401, 302, "P1"),
		executionRun(9004, 9401, 300, "P1"),
	}
	created := createExecutionBatch(t, batches, executionBatch(9401, 1, domain.BatchStatusRunning), runs)

	want := []int64{9002 /* P0 */, 9004 /* P1, lower node */, 9003 /* P1 */, 9001 /* P2 */}
	for _, runID := range want {
		next, err := caseRuns.NextPending(ctx, created.ID)
		if err != nil {
			t.Fatalf("NextPending: %v", err)
		}
		if next.ID != runID {
			t.Fatalf("NextPending = %d, want %d (order %v)", next.ID, runID, want)
		}
		if _, err := caseRuns.SetStatus(ctx, next.ID, domain.CaseRunStatusPending, domain.CaseRunStatusRunning, executionTestTime); err != nil {
			t.Fatalf("claim %d: %v", runID, err)
		}
	}
	_, err := caseRuns.NextPending(ctx, created.ID)
	requireExecutionErrorIs(t, err, application.ErrNoPendingCaseRun)
}

// TestExecutionCaseRunGetStatusAndFinish covers snapshot round-trip, ownership,
// listing, the status CAS and the terminal finish CAS.
func TestExecutionCaseRunGetStatusAndFinish(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	batches := postgres.NewBatchRepository(pool)
	caseRuns := postgres.NewCaseRunRepository(pool)

	run := executionRun(9101, 9501, 401, "P1")
	created := createExecutionBatch(t, batches, executionBatch(9501, 1, domain.BatchStatusRunning), []domain.CaseRun{run})

	loaded, err := caseRuns.GetCaseRun(ctx, created.ID, run.ID)
	if err != nil {
		t.Fatalf("GetCaseRun: %v", err)
	}
	if loaded.Status != domain.CaseRunStatusPending || loaded.Attempt != 1 ||
		loaded.Snapshot.Node.ID != 401 || loaded.Snapshot.Node.Priority != "P1" ||
		len(loaded.Snapshot.Steps) != 1 || len(loaded.Snapshot.Assertions) != 1 {
		t.Fatalf("loaded case run = %+v", loaded)
	}
	if loaded.CreatedAt.IsZero() || loaded.UpdatedAt.IsZero() {
		t.Fatalf("case run timestamps = %+v", loaded)
	}
	_, err = caseRuns.GetCaseRun(ctx, created.ID+1, run.ID)
	requireExecutionErrorIs(t, err, application.ErrCaseRunNotFound)

	locked, err := caseRuns.GetCaseRunForUpdate(ctx, created.ID, run.ID)
	if err != nil {
		t.Fatalf("GetCaseRunForUpdate: %v", err)
	}
	if locked.ID != run.ID || locked.CaseHash != run.CaseHash {
		t.Fatalf("locked case run = %+v", locked)
	}

	page, more, err := caseRuns.ListCaseRuns(ctx, created.ID, "", 0, 10)
	if err != nil {
		t.Fatalf("ListCaseRuns: %v", err)
	}
	if len(page) != 1 || page[0].ID != run.ID || more {
		t.Fatalf("case run page ids=%v more=%v", caseRunIDs(page), more)
	}
	page, more, err = caseRuns.ListCaseRuns(ctx, created.ID, domain.CaseRunStatusFailed, 0, 10)
	if err != nil || len(page) != 0 || more {
		t.Fatalf("failed page ids=%v more=%v err=%v", caseRunIDs(page), more, err)
	}

	running, err := caseRuns.SetStatus(ctx, run.ID, domain.CaseRunStatusPending, domain.CaseRunStatusRunning, executionTestTime)
	if err != nil {
		t.Fatalf("SetStatus running: %v", err)
	}
	if running.Status != domain.CaseRunStatusRunning || running.StartedAt == nil ||
		!running.StartedAt.Equal(executionTestTime) {
		t.Fatalf("running case run = %+v", running)
	}
	_, err = caseRuns.SetStatus(ctx, run.ID, domain.CaseRunStatusPending, domain.CaseRunStatusRunning, executionTestTime)
	requireExecutionErrorIs(t, err, application.ErrConflict)

	// UpdateCaseRun rewrites the mutable execution columns.
	running.SessionID = ptrInt64(51)
	running.AgentSessionID = ptrInt64(52)
	running.Summary = "half way"
	updated, err := caseRuns.UpdateCaseRun(ctx, running)
	if err != nil {
		t.Fatalf("UpdateCaseRun: %v", err)
	}
	if updated.SessionID == nil || *updated.SessionID != 51 ||
		updated.AgentSessionID == nil || *updated.AgentSessionID != 52 ||
		updated.Summary != "half way" {
		t.Fatalf("updated case run = %+v", updated)
	}

	// Finish stores the terminal decision and refuses a second finish.
	finishTime := executionTestTime.Add(2 * time.Minute)
	failureClass := domain.FailureClassApplication
	finished := updated
	finished.Status = domain.CaseRunStatusFailed
	finished.FailureClass = &failureClass
	finished.FailureReason = "assertion failed"
	finished.Summary = "checkout button missing"
	finished.DurationMS = 1234
	finished.FinishedAt = &finishTime
	stored, err := caseRuns.FinishCaseRun(ctx, finished)
	if err != nil {
		t.Fatalf("FinishCaseRun: %v", err)
	}
	if stored.Status != domain.CaseRunStatusFailed || stored.FailureClass == nil ||
		*stored.FailureClass != failureClass || stored.FailureReason != "assertion failed" ||
		stored.DurationMS != 1234 || stored.FinishedAt == nil ||
		!stored.FinishedAt.Equal(finishTime) {
		t.Fatalf("finished case run = %+v", stored)
	}
	_, err = caseRuns.FinishCaseRun(ctx, finished)
	requireExecutionErrorIs(t, err, domain.ErrCaseRunTerminal)
	_, err = caseRuns.SetStatus(ctx, run.ID, domain.CaseRunStatusRunning, domain.CaseRunStatusFailed, finishTime)
	requireExecutionErrorIs(t, err, domain.ErrCaseRunTerminal)

	// The direct create path rejects a duplicate (batch, case, attempt).
	_, err = caseRuns.CreateCaseRun(ctx, executionRun(9101, created.ID, 401, "P1"))
	requireExecutionErrorIs(t, err, application.ErrConflict)
	_, err = caseRuns.CreateCaseRun(ctx, executionRun(9102, created.ID, 401, "P1"))
	requireExecutionErrorIs(t, err, application.ErrConflict)
}

// TestExecutionUpdateCountsRecomputesFromRuns pins that UpdateCounts derives
// the whole tally from the stored case runs instead of trusting a caller
// tally: three sequential finishes accumulate exactly, and recounting twice is
// idempotent.
func TestExecutionUpdateCountsRecomputesFromRuns(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	batches := postgres.NewBatchRepository(pool)
	caseRuns := postgres.NewCaseRunRepository(pool)

	runs := []domain.CaseRun{
		executionRun(9301, 9701, 601, "P0"),
		executionRun(9302, 9701, 602, "P1"),
		executionRun(9303, 9701, 603, "P2"),
	}
	created := createExecutionBatch(t, batches, executionBatch(9701, 1, domain.BatchStatusRunning), runs)

	want := domain.BatchCounts{CaseCount: 3}
	got, err := batches.UpdateCounts(ctx, created.ID)
	if err != nil {
		t.Fatalf("UpdateCounts fresh batch: %v", err)
	}
	if got != want {
		t.Fatalf("fresh tally = %+v, want %+v", got, want)
	}
	// A second recount must return the same tally, not add to a caller's tally.
	if got, err = batches.UpdateCounts(ctx, created.ID); err != nil {
		t.Fatalf("UpdateCounts twice: %v", err)
	}
	if got != want {
		t.Fatalf("recounted tally = %+v, want %+v", got, want)
	}

	finishExecutionRun(t, caseRuns, runs[0], domain.CaseRunStatusPassed)
	want.PassedCount = 1
	if got, err = batches.UpdateCounts(ctx, created.ID); err != nil {
		t.Fatalf("UpdateCounts after passed: %v", err)
	}
	if got != want {
		t.Fatalf("tally after passed = %+v, want %+v", got, want)
	}

	finishExecutionRun(t, caseRuns, runs[1], domain.CaseRunStatusFailed)
	want.FailedCount = 1
	if got, err = batches.UpdateCounts(ctx, created.ID); err != nil {
		t.Fatalf("UpdateCounts after failed: %v", err)
	}
	if got != want {
		t.Fatalf("tally after failed = %+v, want %+v", got, want)
	}

	finishExecutionRun(t, caseRuns, runs[2], domain.CaseRunStatusBlocked)
	want.BlockedCount = 1
	if got, err = batches.UpdateCounts(ctx, created.ID); err != nil {
		t.Fatalf("UpdateCounts after blocked: %v", err)
	}
	if got != want {
		t.Fatalf("tally after blocked = %+v, want %+v", got, want)
	}

	// The recomputed tally is persisted, not merely returned.
	stored, err := batches.GetBatch(ctx, executionTestVersion, created.ID)
	if err != nil {
		t.Fatalf("GetBatch after recounts: %v", err)
	}
	if stored.CaseCount != want.CaseCount || stored.PassedCount != want.PassedCount ||
		stored.FailedCount != want.FailedCount || stored.BlockedCount != want.BlockedCount ||
		stored.InconclusiveCount != 0 || stored.SkippedCount != 0 || stored.InterruptedCount != 0 {
		t.Fatalf("stored tally = %+v, want %+v", stored, want)
	}

	// done still settles; failed/cancelled stop being recountable.
	if _, err := batches.UpdateBatchStatus(ctx, created.ID, domain.BatchStatusRunning, domain.BatchStatusDone, nil); err != nil {
		t.Fatalf("finish batch: %v", err)
	}
	if got, err = batches.UpdateCounts(ctx, created.ID); err != nil {
		t.Fatalf("UpdateCounts after done: %v", err)
	}
	if got != want {
		t.Fatalf("settled tally = %+v, want %+v", got, want)
	}
	failed := createExecutionBatch(t, batches, executionBatch(9702, 2, domain.BatchStatusRunning), nil)
	if _, err := batches.UpdateBatchStatus(ctx, failed.ID, domain.BatchStatusRunning, domain.BatchStatusFailed, nil); err != nil {
		t.Fatalf("fail batch: %v", err)
	}
	_, err = batches.UpdateCounts(ctx, failed.ID)
	requireExecutionErrorIs(t, err, domain.ErrBatchFinished)
	_, err = batches.UpdateCounts(ctx, 999999)
	requireExecutionErrorIs(t, err, application.ErrBatchNotFound)
}

// TestExecutionUpdateCountsConcurrentFinishes pins the lost-count fix: two
// finishers commit different cases and recount concurrently, and the stored
// tally still reflects both. Two further concurrent recounts must both observe
// the exact settled tally.
func TestExecutionUpdateCountsConcurrentFinishes(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	batches := postgres.NewBatchRepository(pool)
	caseRuns := postgres.NewCaseRunRepository(pool)

	passed := executionRun(9311, 9711, 611, "P0")
	failed := executionRun(9312, 9711, 612, "P0")
	created := createExecutionBatch(t, batches, executionBatch(9711, 1, domain.BatchStatusRunning), []domain.CaseRun{passed, failed})
	for _, run := range []domain.CaseRun{passed, failed} {
		if _, err := caseRuns.SetStatus(ctx, run.ID, domain.CaseRunStatusPending, domain.CaseRunStatusRunning, executionTestTime); err != nil {
			t.Fatalf("start case run %d: %v", run.ID, err)
		}
	}

	finishers := []struct {
		run    domain.CaseRun
		status domain.CaseRunStatus
	}{
		{passed, domain.CaseRunStatusPassed},
		{failed, domain.CaseRunStatusFailed},
	}
	var wg sync.WaitGroup
	finishErrs := make(chan error, len(finishers))
	recounts := make(chan domain.BatchCounts, len(finishers))
	for _, finisher := range finishers {
		wg.Add(1)
		go func(run domain.CaseRun, status domain.CaseRunStatus) {
			defer wg.Done()
			if err := finishRunningExecutionRun(ctx, caseRuns, run, status); err != nil {
				finishErrs <- fmt.Errorf("finish case run %d: %w", run.ID, err)
				return
			}
			counts, err := batches.UpdateCounts(ctx, created.ID)
			if err != nil {
				finishErrs <- fmt.Errorf("recount after case run %d: %w", run.ID, err)
				return
			}
			recounts <- counts
		}(finisher.run, finisher.status)
	}
	wg.Wait()
	close(finishErrs)
	close(recounts)
	for err := range finishErrs {
		t.Fatalf("concurrent finish: %v", err)
	}
	for counts := range recounts {
		if counts.CaseCount != 2 {
			t.Fatalf("concurrent recount case_count = %+v, want 2", counts)
		}
	}
	want := domain.BatchCounts{CaseCount: 2, PassedCount: 1, FailedCount: 1}
	stored, err := batches.GetBatch(ctx, executionTestVersion, created.ID)
	if err != nil {
		t.Fatalf("GetBatch after concurrent finishes: %v", err)
	}
	if stored.CaseCount != want.CaseCount || stored.PassedCount != want.PassedCount ||
		stored.FailedCount != want.FailedCount || stored.BlockedCount != 0 ||
		stored.InconclusiveCount != 0 || stored.SkippedCount != 0 || stored.InterruptedCount != 0 {
		t.Fatalf("stored tally after concurrent finishes = %+v, want %+v", stored, want)
	}

	// Two simultaneous recounts of the already settled batch must both see both
	// finished runs; a stale tally can never overwrite a fresher one.
	var recountWG sync.WaitGroup
	recountResults := make(chan domain.BatchCounts, 2)
	recountErrs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		recountWG.Add(1)
		go func() {
			defer recountWG.Done()
			counts, err := batches.UpdateCounts(ctx, created.ID)
			if err != nil {
				recountErrs <- err
				return
			}
			recountResults <- counts
		}()
	}
	recountWG.Wait()
	close(recountResults)
	close(recountErrs)
	for err := range recountErrs {
		t.Fatalf("concurrent recount: %v", err)
	}
	for counts := range recountResults {
		if counts != want {
			t.Fatalf("concurrent recount = %+v, want %+v", counts, want)
		}
	}
}

// finishExecutionRun drives a pending run through the pending -> running CAS
// and finishes it with status.
func finishExecutionRun(t *testing.T, repo application.CaseRunRepository, run domain.CaseRun, status domain.CaseRunStatus) {
	t.Helper()
	if _, err := repo.SetStatus(context.Background(), run.ID, domain.CaseRunStatusPending, domain.CaseRunStatusRunning, executionTestTime); err != nil {
		t.Fatalf("start case run %d: %v", run.ID, err)
	}
	if err := finishRunningExecutionRun(context.Background(), repo, run, status); err != nil {
		t.Fatalf("finish case run %d as %s: %v", run.ID, status, err)
	}
}

// finishRunningExecutionRun finishes an already-running run and returns the
// repository error, so it is safe to call from a goroutine.
func finishRunningExecutionRun(ctx context.Context, repo application.CaseRunRepository, run domain.CaseRun, status domain.CaseRunStatus) error {
	finished := run
	finished.Status = status
	finished.DurationMS = 10
	if status == domain.CaseRunStatusFailed {
		class := domain.FailureClassApplication
		finished.FailureClass = &class
		finished.FailureReason = "assertion failed"
	}
	_, err := repo.FinishCaseRun(ctx, finished)
	return err
}

// TestExecutionStepAssertionEvidenceAndRefs covers the step/assertion upsert
// uniqueness, evidence JSONB/NULL round-trip and idempotent evidence refs.
func TestExecutionStepAssertionEvidenceAndRefs(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	batches := postgres.NewBatchRepository(pool)
	steps := postgres.NewStepRepository(pool)
	assertions := postgres.NewAssertionRepository(pool)
	evidenceRepo := postgres.NewEvidenceRepository(pool)
	refs := postgres.NewEvidenceRefRepository(pool)

	run := executionRun(9201, 9601, 501, "P0")
	created := createExecutionBatch(t, batches, executionBatch(9601, 1, domain.BatchStatusRunning), []domain.CaseRun{run})

	step := domain.StepResult{
		ID: 9701, BatchID: created.ID, CaseRunID: run.ID, Seq: 1,
		Action: "open", Expected: "shown", Actual: "opened",
		Status: domain.StepStatusPassed, EvidenceCount: 1,
		CreatedAt: executionTestTime, UpdatedAt: executionTestTime,
	}
	storedStep, err := steps.Upsert(ctx, step)
	if err != nil {
		t.Fatalf("Upsert step: %v", err)
	}
	if storedStep.ID != step.ID || storedStep.Status != domain.StepStatusPassed {
		t.Fatalf("stored step = %+v", storedStep)
	}
	// A second upsert for the same (case_run_id, seq) updates in place and keeps
	// the original id even when the caller supplies another one.
	step.ID = 9799
	step.Actual = "opened again"
	step.Status = domain.StepStatusFailed
	step.EvidenceCount = 2
	updatedStep, err := steps.Upsert(ctx, step)
	if err != nil {
		t.Fatalf("Upsert step update: %v", err)
	}
	if updatedStep.ID != 9701 || updatedStep.Actual != "opened again" ||
		updatedStep.Status != domain.StepStatusFailed || updatedStep.EvidenceCount != 2 {
		t.Fatalf("updated step = %+v", updatedStep)
	}
	listedSteps, err := steps.ListByCaseRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListByCaseRun steps: %v", err)
	}
	if len(listedSteps) != 1 || listedSteps[0].ID != 9701 {
		t.Fatalf("listed steps = %+v", listedSteps)
	}

	assertion := domain.AssertionResult{
		ID: 9801, BatchID: created.ID, CaseRunID: run.ID, StepResultID: &storedStep.ID,
		AssertionID: 5001, Kind: "ui", Target: "#submit", Expected: "true",
		Actual: "false", Verdict: "failed", FailureReason: "missing",
		CreatedAt: executionTestTime, UpdatedAt: executionTestTime,
	}
	storedAssertion, err := assertions.Upsert(ctx, assertion)
	if err != nil {
		t.Fatalf("Upsert assertion: %v", err)
	}
	if storedAssertion.ID != assertion.ID || storedAssertion.StepResultID == nil ||
		*storedAssertion.StepResultID != storedStep.ID || storedAssertion.Verdict != "failed" {
		t.Fatalf("stored assertion = %+v", storedAssertion)
	}
	assertion.ID = 9899
	assertion.Verdict = "passed"
	assertion.FailureReason = ""
	updatedAssertion, err := assertions.Upsert(ctx, assertion)
	if err != nil {
		t.Fatalf("Upsert assertion update: %v", err)
	}
	if updatedAssertion.ID != 9801 || updatedAssertion.Verdict != "passed" {
		t.Fatalf("updated assertion = %+v", updatedAssertion)
	}
	listedAssertions, err := assertions.ListByCaseRun(ctx, run.ID)
	if err != nil || len(listedAssertions) != 1 || listedAssertions[0].ID != 9801 {
		t.Fatalf("listed assertions = %+v err=%v", listedAssertions, err)
	}

	evidence := domain.Evidence{
		ID: 9901, BatchID: created.ID, CaseRunID: &run.ID, StepResultID: &storedStep.ID,
		Kind: "screenshot", Path: "versions/1/9901.png",
		SHA256: strings.Repeat("c", 64), SizeBytes: 1024, Mime: "image/png",
		Metadata: map[string]any{"width": float64(1440)},
	}
	storedEvidence, err := evidenceRepo.Create(ctx, evidence)
	if err != nil {
		t.Fatalf("Create evidence: %v", err)
	}
	if storedEvidence.CreatedAt.IsZero() || storedEvidence.Metadata["width"] != float64(1440) {
		t.Fatalf("stored evidence = %+v", storedEvidence)
	}
	loadedEvidence, err := evidenceRepo.Get(ctx, evidence.ID)
	if err != nil {
		t.Fatalf("Get evidence: %v", err)
	}
	if loadedEvidence.CaseRunID == nil || *loadedEvidence.CaseRunID != run.ID ||
		loadedEvidence.SHA256 != evidence.SHA256 {
		t.Fatalf("loaded evidence = %+v", loadedEvidence)
	}
	_, err = evidenceRepo.Get(ctx, 999999)
	requireExecutionErrorIs(t, err, application.ErrEvidenceNotFound)

	// A batch-level artifact has NULL case_run_id and no metadata document.
	evidence2 := domain.Evidence{
		ID: 9902, BatchID: created.ID, Kind: "console_log", Path: "versions/1/9902.log",
		SHA256: strings.Repeat("d", 64), SizeBytes: 64, Mime: "text/plain",
	}
	storedEvidence2, err := evidenceRepo.Create(ctx, evidence2)
	if err != nil {
		t.Fatalf("Create evidence2: %v", err)
	}
	if storedEvidence2.CaseRunID != nil || storedEvidence2.Metadata == nil {
		t.Fatalf("batch evidence = %+v", storedEvidence2)
	}
	listedEvidence, err := evidenceRepo.ListByCaseRun(ctx, run.ID)
	if err != nil || len(listedEvidence) != 1 || listedEvidence[0].ID != 9901 {
		t.Fatalf("listed evidence = %+v err=%v", listedEvidence, err)
	}

	ref := domain.EvidenceRef{EvidenceID: 9901, RefKind: "assertion_result", RefID: 9801}
	storedRef, err := refs.Create(ctx, ref)
	if err != nil {
		t.Fatalf("Create evidence ref: %v", err)
	}
	if storedRef.CreatedAt.IsZero() || storedRef.RefKind != "assertion_result" {
		t.Fatalf("stored ref = %+v", storedRef)
	}
	// Creating the same link again is idempotent and returns the stored row.
	again, err := refs.Create(ctx, ref)
	if err != nil {
		t.Fatalf("Create duplicate evidence ref: %v", err)
	}
	if !again.CreatedAt.Equal(storedRef.CreatedAt) {
		t.Fatalf("duplicate ref created_at = %v, want %v", again.CreatedAt, storedRef.CreatedAt)
	}
	var linkCount int
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM evidence_ref WHERE evidence_id = $1`, 9901).Scan(&linkCount); err != nil {
		t.Fatalf("count evidence refs: %v", err)
	}
	if linkCount != 1 {
		t.Fatalf("evidence ref count = %d, want 1", linkCount)
	}
}

// batchIDs and caseRunIDs project ids for readable failures.
func batchIDs(batches []domain.RunBatch) []int64 {
	ids := make([]int64, len(batches))
	for i, batch := range batches {
		ids[i] = batch.ID
	}
	return ids
}

func caseRunIDs(runs []domain.CaseRun) []int64 {
	ids := make([]int64, len(runs))
	for i, run := range runs {
		ids[i] = run.ID
	}
	return ids
}

func ptrInt64(value int64) *int64 { return &value }
