package application

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// testNow is the fixed clock of every batch-service test.
var testNow = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

const testBaselineHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// batchTestDeps is the fake world of one BatchService test.
type batchTestDeps struct {
	version *fakeVersionPort
	mindmap *fakeMindmapPort
	repo    *fakeBatchRepository
	runs    *fakeCaseRunRepository
	events  *fakeEventPublisher
	ids     *fakeIDGenerator
	clock   *fakeClock
}

// newBatchServiceTest returns a service wired to fakes with one executing
// version and a frozen two-case baseline (cases 11 and 12, snapshot graph hash
// equal to the baseline hash).
func newBatchServiceTest(t *testing.T) (*BatchService, *batchTestDeps) {
	t.Helper()
	return newBatchServiceTestWithMax(t, 10)
}

// newBatchServiceTestWithMax is newBatchServiceTest with an explicit per-batch
// case cap.
func newBatchServiceTestWithMax(t *testing.T, maxCases int) (*BatchService, *batchTestDeps) {
	t.Helper()
	deps := &batchTestDeps{
		version: &fakeVersionPort{
			status:        versionStatusExecuting,
			systemID:      int64Ptr(3),
			environmentID: int64Ptr(4),
		},
		mindmap: &fakeMindmapPort{
			baseline:  BaselineRef{Hash: testBaselineHash, CaseIDs: []int64{11, 12}},
			snapshots: map[int64]CaseSnapshotSourceView{},
		},
		repo:   newFakeBatchRepository(),
		runs:   newFakeCaseRunRepository(),
		events: &fakeEventPublisher{},
		ids:    &fakeIDGenerator{},
		clock:  &fakeClock{now: testNow},
	}
	deps.repo.caseRuns = deps.runs
	deps.seedSnapshot(11)
	deps.seedSnapshot(12)
	service := NewBatchService(BatchServiceDeps{
		Versions: deps.version,
		Mindmap:  deps.mindmap,
		Batches:  deps.repo,
		CaseRuns: deps.runs,
		Events:   deps.events,
		Clock:    deps.clock,
		IDs:      deps.ids,
		MaxCases: maxCases,
	})
	return service, deps
}

// seedSnapshot seeds the frozen snapshot of one baseline case.
func (d *batchTestDeps) seedSnapshot(caseNodeID int64) {
	d.mindmap.snapshots[caseNodeID] = testSnapshotView(caseNodeID, d.mindmap.baseline.Hash)
}

// seedBatch stores one batch in the fake repository and returns it.
func (d *batchTestDeps) seedBatch(status domain.BatchStatus, paused bool) domain.RunBatch {
	batch := domain.RunBatch{
		ID:           55,
		VersionID:    7,
		BatchNo:      1,
		Status:       status,
		Paused:       paused,
		PauseMode:    domain.PauseModeGraceful,
		BaselineHash: testBaselineHash,
		CreatedBy:    1,
	}
	d.repo.batches[batch.ID] = batch
	return batch
}

// seedRun stores one case run in the fake case-run repository.
func (d *batchTestDeps) seedRun(run domain.CaseRun) {
	d.runs.runs[run.ID] = run
}

// testSnapshotView is the frozen mindmap projection of one case.
func testSnapshotView(caseNodeID int64, graphHash string) CaseSnapshotSourceView {
	return CaseSnapshotSourceView{
		CaseNodeID: caseNodeID,
		Revision:   2,
		GraphHash:  graphHash,
		Node: domain.CaseSnapshotNode{
			ID:            caseNodeID,
			Title:         fmt.Sprintf("case %d", caseNodeID),
			Priority:      "P1",
			Preconditions: "signed in",
			TestData:      map[string]any{"key": "value", "nested": map[string]any{"n": 1}},
		},
		Steps: []domain.CaseSnapshotStep{
			{Seq: 1, Action: "open the list", Expected: "list visible", Data: map[string]any{"page": 1}},
		},
		Assertions: []domain.CaseSnapshotAssertion{
			{ID: caseNodeID * 10, StepSeq: 1, Kind: "ui", Target: "body", Expression: "count > 0", Expected: "rows"},
		},
		RequirementIDs: []int64{100 + caseNodeID},
	}
}

// wantReason asserts that err is a ReasonError carrying reason.
func wantReason(t *testing.T, err error, reason string) {
	t.Helper()
	var reasonErr *domain.ReasonError
	if !errors.As(err, &reasonErr) {
		t.Fatalf("error %v is not a *domain.ReasonError", err)
	}
	if reasonErr.Reason != reason {
		t.Fatalf("reason = %q, want %q (error %v)", reasonErr.Reason, reason, err)
	}
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

func TestCreatePinsBaselineAndSnapshots(t *testing.T) {
	service, deps := newBatchServiceTest(t)
	batch, err := service.Create(context.Background(), 7, 1, nil)
	if err != nil {
		t.Fatal(err)
	}

	if batch.BatchNo != 1 || batch.VersionID != 7 || batch.CreatedBy != 1 {
		t.Fatalf("batch identity = %+v", batch)
	}
	if batch.BaselineHash != testBaselineHash || batch.CaseCount != 2 {
		t.Fatalf("batch baseline/count = %+v", batch)
	}
	if batch.Status != domain.BatchStatusRunning {
		t.Fatalf("status = %s, want running", batch.Status)
	}
	if batch.Paused {
		t.Fatal("a new batch must not be paused")
	}
	if batch.StartedAt == nil || !batch.StartedAt.Equal(testNow) {
		t.Fatalf("started_at = %v, want %v", batch.StartedAt, testNow)
	}

	if len(deps.repo.createdRuns) != 2 {
		t.Fatalf("created runs = %d, want 2", len(deps.repo.createdRuns))
	}
	if deps.repo.createdRuns[0].ID != 2 || deps.repo.createdRuns[1].ID != 3 {
		t.Fatalf("run ids = %d,%d, want minted 2,3",
			deps.repo.createdRuns[0].ID, deps.repo.createdRuns[1].ID)
	}
	for i, run := range deps.repo.createdRuns {
		wantCaseID := int64(11 + i)
		if run.BatchID != batch.ID || run.VersionID != 7 || run.CaseNodeID != wantCaseID {
			t.Fatalf("run identity = %+v", run)
		}
		if run.Attempt != 1 || run.Status != domain.CaseRunStatusPending {
			t.Fatalf("run attempt/status = %d/%s", run.Attempt, run.Status)
		}
		if run.CaseRevision != 2 || run.CaseHash != domain.CanonicalCaseHash(run.Snapshot) {
			t.Fatalf("run revision/hash = %d/%q", run.CaseRevision, run.CaseHash)
		}
		if len(run.CaseHash) != 64 {
			t.Fatalf("case hash %q is not 64 hex chars", run.CaseHash)
		}
		if run.Snapshot.GraphHash != testBaselineHash || !run.Snapshot.CapturedAt.Equal(testNow) {
			t.Fatalf("snapshot graph hash/captured at = %+v", run.Snapshot)
		}
		if run.Snapshot.Node.ID != wantCaseID || len(run.Snapshot.Steps) != 1 ||
			len(run.Snapshot.Assertions) != 1 || run.Snapshot.Sources[0].RequirementID != 100+wantCaseID {
			t.Fatalf("pinned snapshot = %+v", run.Snapshot)
		}
		if run.CleanupRequired || run.StartedAt != nil || run.FinishedAt != nil {
			t.Fatalf("a fresh run must be clean and unstarted: %+v", run)
		}
	}

	if !reflect.DeepEqual(deps.version.allocateCalls, []int64{7}) {
		t.Fatalf("AllocateBatchNo calls = %v, want [7]", deps.version.allocateCalls)
	}
	if len(deps.version.setBatchNoCalls) != 0 {
		t.Fatalf("create must record batch_no through allocation, not SetBatchNo: %+v", deps.version.setBatchNoCalls)
	}
	if len(deps.version.releaseCalls) != 0 {
		t.Fatalf("a successful create must not release its batch_no: %+v", deps.version.releaseCalls)
	}
	if deps.version.batchNo != 1 {
		t.Fatalf("version batch_no = %d, want 1", deps.version.batchNo)
	}
	if !reflect.DeepEqual(deps.events.types, []string{"batch.created", "batch.started"}) {
		t.Fatalf("event types = %v", deps.events.types)
	}
	for _, event := range deps.events.events {
		if event.VersionID != 7 || event.BatchID != batch.ID {
			t.Fatalf("event addressing = %+v", event)
		}
	}
}

func TestCreateRejectsVersionNotExecutable(t *testing.T) {
	service, deps := newBatchServiceTest(t)
	deps.version.status = "draft"
	_, err := service.Create(context.Background(), 7, 1, nil)
	wantReason(t, err, "version_not_executable")
	if deps.repo.createCalls != 0 {
		t.Fatalf("a rejected create must not write a batch (calls=%d)", deps.repo.createCalls)
	}
}

func TestCreateRejectsMissingEnvironment(t *testing.T) {
	for name, mutate := range map[string]func(*fakeVersionPort){
		"no system":      func(v *fakeVersionPort) { v.systemID = nil },
		"no environment": func(v *fakeVersionPort) { v.environmentID = nil },
	} {
		t.Run(name, func(t *testing.T) {
			service, deps := newBatchServiceTest(t)
			mutate(deps.version)
			_, err := service.Create(context.Background(), 7, 1, nil)
			wantReason(t, err, "environment_required")
		})
	}
}

func TestCreateRejectsMissingBaseline(t *testing.T) {
	t.Run("empty baseline ref", func(t *testing.T) {
		service, deps := newBatchServiceTest(t)
		deps.mindmap.baseline = BaselineRef{}
		_, err := service.Create(context.Background(), 7, 1, nil)
		wantReason(t, err, "baseline_required")
	})
	t.Run("adapter reports missing baseline", func(t *testing.T) {
		service, deps := newBatchServiceTest(t)
		deps.mindmap.baselineErr = ErrBaselineRequired
		_, err := service.Create(context.Background(), 7, 1, nil)
		wantReason(t, err, "baseline_required")
	})
}

func TestCreateRejectsActiveBatch(t *testing.T) {
	service, deps := newBatchServiceTest(t)
	deps.repo.createErr = domain.ErrBatchActive
	_, err := service.Create(context.Background(), 7, 1, nil)
	wantReason(t, err, "batch_active")
	if !reflect.DeepEqual(deps.version.allocateCalls, []int64{7}) {
		t.Fatalf("the number must be allocated before the create attempt: %v", deps.version.allocateCalls)
	}
	// The lost create must hand the number back so a later batch is not stuck.
	if !reflect.DeepEqual(deps.version.releaseCalls, []releaseBatchNoCall{{versionID: 7, allocated: 1}}) {
		t.Fatalf("ReleaseBatchNo calls = %+v, want one for 1", deps.version.releaseCalls)
	}
	if deps.version.batchNo != 0 {
		t.Fatalf("version batch_no = %d after a lost create, want 0", deps.version.batchNo)
	}
	if len(deps.repo.createdRuns) != 0 {
		t.Fatalf("a rejected create must not persist runs: %+v", deps.repo.createdRuns)
	}
	if len(deps.events.types) != 0 {
		t.Fatalf("a rejected create must not publish events: %v", deps.events.types)
	}
}

func TestCreateFailsBeforeWritingWhenAllocationFails(t *testing.T) {
	service, deps := newBatchServiceTest(t)
	allocateErr := errors.New("version store unavailable")
	deps.version.allocateErr = allocateErr

	_, err := service.Create(context.Background(), 7, 1, nil)
	if !errors.Is(err, allocateErr) {
		t.Fatalf("error = %v, want %v", err, allocateErr)
	}
	if deps.repo.createCalls != 0 {
		t.Fatalf("an allocation failure must not create a batch (calls=%d)", deps.repo.createCalls)
	}
	if len(deps.repo.createdRuns) != 0 {
		t.Fatalf("an allocation failure must not create runs: %+v", deps.repo.createdRuns)
	}
	if len(deps.events.types) != 0 {
		t.Fatalf("an allocation failure must not publish events: %v", deps.events.types)
	}
}

func TestCreateCompensatesFailedCreateSoTheNextCreateSucceeds(t *testing.T) {
	service, deps := newBatchServiceTest(t)
	createErr := errors.New("insert batch failed")
	deps.repo.createErr = createErr

	if _, err := service.Create(context.Background(), 7, 1, nil); !errors.Is(err, createErr) {
		t.Fatalf("error = %v, want the original %v", err, createErr)
	}
	if !reflect.DeepEqual(deps.version.releaseCalls, []releaseBatchNoCall{{versionID: 7, allocated: 1}}) {
		t.Fatalf("ReleaseBatchNo calls = %+v, want one for 1", deps.version.releaseCalls)
	}
	if deps.version.batchNo != 0 {
		t.Fatalf("version batch_no = %d after compensation, want 0", deps.version.batchNo)
	}
	if len(deps.events.types) != 0 {
		t.Fatalf("a failed create must not publish events: %v", deps.events.types)
	}

	deps.repo.createErr = nil
	batch, err := service.Create(context.Background(), 7, 1, nil)
	if err != nil {
		t.Fatalf("the next create must succeed, got %v", err)
	}
	if batch.BatchNo != 1 {
		t.Fatalf("next batch_no = %d, want the compensated 1", batch.BatchNo)
	}
}

func TestCreateRejectsNoCases(t *testing.T) {
	service, deps := newBatchServiceTest(t)
	deps.mindmap.baseline = BaselineRef{Hash: testBaselineHash}
	_, err := service.Create(context.Background(), 7, 1, nil)
	wantReason(t, err, "no_cases")
}

func TestCreateDoesNotMislabelRepositoryConflictAsBatchActive(t *testing.T) {
	service, deps := newBatchServiceTest(t)
	conflict := fmt.Errorf("%w: duplicate primary key", ErrConflict)
	deps.repo.createErr = conflict

	_, err := service.Create(context.Background(), 7, 1, nil)
	if !errors.Is(err, conflict) {
		t.Fatalf("error = %v, want the repository conflict unchanged", err)
	}
	var reasonErr *domain.ReasonError
	if errors.As(err, &reasonErr) && reasonErr.Reason == "batch_active" {
		t.Fatalf("a repository conflict must not be mislabelled batch_active: %v", err)
	}
	if !reflect.DeepEqual(deps.version.releaseCalls, []releaseBatchNoCall{{versionID: 7, allocated: 1}}) {
		t.Fatalf("a conflicting create must still release its number: %+v", deps.version.releaseCalls)
	}
}

func TestCreateRejectsTooManyCases(t *testing.T) {
	service, _ := newBatchServiceTestWithMax(t, 1)
	_, err := service.Create(context.Background(), 7, 1, nil)
	wantReason(t, err, "too_many_cases")
}

func TestCreateRejectsInvalidCaseIDs(t *testing.T) {
	service, deps := newBatchServiceTest(t)
	_, err := service.Create(context.Background(), 7, 1, []int64{11, 99})
	wantReason(t, err, "invalid_case_ids")
	if len(deps.mindmap.snapshotCalls) != 0 {
		t.Fatalf("invalid ids must be rejected before snapshotting: %v", deps.mindmap.snapshotCalls)
	}
}

func TestCreateRejectsBaselineDrift(t *testing.T) {
	service, deps := newBatchServiceTest(t)
	drifted := deps.mindmap.snapshots[11]
	drifted.GraphHash = strings.Repeat("b", 64)
	deps.mindmap.snapshots[11] = drifted
	_, err := service.Create(context.Background(), 7, 1, nil)
	wantReason(t, err, "baseline_changed")
	if deps.repo.createCalls != 0 {
		t.Fatalf("baseline drift must not create a partial batch")
	}
	if len(deps.events.types) != 0 {
		t.Fatalf("baseline drift must not publish events: %v", deps.events.types)
	}
}

func TestCreateSelectsRequestedCasesAndDeduplicates(t *testing.T) {
	service, deps := newBatchServiceTest(t)
	batch, err := service.Create(context.Background(), 7, 1, []int64{12, 12})
	if err != nil {
		t.Fatal(err)
	}
	if batch.CaseCount != 1 || len(deps.repo.createdRuns) != 1 {
		t.Fatalf("selected = %+v", deps.repo.createdRuns)
	}
	if deps.repo.createdRuns[0].CaseNodeID != 12 {
		t.Fatalf("case node = %d, want 12", deps.repo.createdRuns[0].CaseNodeID)
	}
	if !reflect.DeepEqual(deps.mindmap.snapshotCalls, [][]int64{{12}}) {
		t.Fatalf("snapshot calls = %v, want [[12]]", deps.mindmap.snapshotCalls)
	}
}

// ---------------------------------------------------------------------------
// Get / List
// ---------------------------------------------------------------------------

func TestGetAndListDelegateToRepository(t *testing.T) {
	service, deps := newBatchServiceTest(t)
	batch := deps.seedBatch(domain.BatchStatusRunning, false)
	deps.repo.list = []domain.RunBatch{batch}
	deps.repo.listMore = true

	got, err := service.Get(context.Background(), 7, 55)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 55 {
		t.Fatalf("Get = %+v", got)
	}
	list, more, err := service.List(context.Background(), 7, 99, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !more {
		t.Fatalf("List = %v more=%v", list, more)
	}
	wantCall := listBatchesCall{versionID: 7, beforeID: 99, limit: 10}
	if !reflect.DeepEqual(deps.repo.listCalls, []listBatchesCall{wantCall}) {
		t.Fatalf("List calls = %+v, want %+v", deps.repo.listCalls, wantCall)
	}
}

// ---------------------------------------------------------------------------
// Pause / Resume
// ---------------------------------------------------------------------------

func TestPauseSetsBatchAndVersion(t *testing.T) {
	service, deps := newBatchServiceTest(t)
	deps.seedBatch(domain.BatchStatusRunning, false)

	batch, err := service.Pause(context.Background(), 7, 55, domain.PauseModeImmediate, 9)
	if err != nil {
		t.Fatal(err)
	}
	if !batch.Paused || batch.PauseMode != domain.PauseModeImmediate {
		t.Fatalf("paused batch = %+v", batch)
	}
	wantPause := []pauseCall{{batchID: 55, paused: true, mode: domain.PauseModeImmediate, reason: "user"}}
	if !reflect.DeepEqual(deps.repo.pauseCalls, wantPause) {
		t.Fatalf("SetPaused calls = %+v, want %+v", deps.repo.pauseCalls, wantPause)
	}
	wantVersion := []markPausedCall{{versionID: 7, paused: true, mode: "immediate", reason: "user", actor: 9}}
	if !reflect.DeepEqual(deps.version.markPausedCalls, wantVersion) {
		t.Fatalf("MarkPaused calls = %+v, want %+v", deps.version.markPausedCalls, wantVersion)
	}
	if !reflect.DeepEqual(deps.events.types, []string{"batch.paused"}) {
		t.Fatalf("event types = %v", deps.events.types)
	}
}

func TestPauseDefaultsToGraceful(t *testing.T) {
	service, deps := newBatchServiceTest(t)
	deps.seedBatch(domain.BatchStatusPlanned, false)
	batch, err := service.Pause(context.Background(), 7, 55, "", 9)
	if err != nil {
		t.Fatal(err)
	}
	if batch.PauseMode != domain.PauseModeGraceful {
		t.Fatalf("pause mode = %q, want graceful", batch.PauseMode)
	}
}

func TestPauseRejectsInvalidModeAndState(t *testing.T) {
	t.Run("invalid mode", func(t *testing.T) {
		service, deps := newBatchServiceTest(t)
		deps.seedBatch(domain.BatchStatusRunning, false)
		_, err := service.Pause(context.Background(), 7, 55, "later", 9)
		wantReason(t, err, "invalid_pause_mode")
		if len(deps.repo.pauseCalls) != 0 {
			t.Fatalf("invalid mode must not write: %+v", deps.repo.pauseCalls)
		}
	})
	t.Run("already paused", func(t *testing.T) {
		service, deps := newBatchServiceTest(t)
		deps.seedBatch(domain.BatchStatusRunning, true)
		_, err := service.Pause(context.Background(), 7, 55, domain.PauseModeGraceful, 9)
		wantReason(t, err, "already_paused")
	})
	t.Run("finished", func(t *testing.T) {
		service, deps := newBatchServiceTest(t)
		deps.seedBatch(domain.BatchStatusDone, false)
		_, err := service.Pause(context.Background(), 7, 55, domain.PauseModeGraceful, 9)
		wantReason(t, err, "batch_finished")
	})
	t.Run("finished while still paused", func(t *testing.T) {
		service, deps := newBatchServiceTest(t)
		deps.seedBatch(domain.BatchStatusCancelled, true)
		_, err := service.Pause(context.Background(), 7, 55, domain.PauseModeGraceful, 9)
		// A terminal batch is finished even when its pause flag was never
		// cleared; already_paused would hide the terminal state.
		wantReason(t, err, "batch_finished")
		if len(deps.repo.pauseCalls) != 0 {
			t.Fatalf("a terminal batch must not be paused: %+v", deps.repo.pauseCalls)
		}
	})
}

func TestResumeOnlyPausedBatch(t *testing.T) {
	t.Run("resumes", func(t *testing.T) {
		service, deps := newBatchServiceTest(t)
		deps.seedBatch(domain.BatchStatusRunning, true)
		batch, err := service.Resume(context.Background(), 7, 55, 9)
		if err != nil {
			t.Fatal(err)
		}
		if batch.Paused {
			t.Fatalf("batch still paused: %+v", batch)
		}
		wantPause := []pauseCall{{batchID: 55, paused: false, mode: domain.PauseModeGraceful, reason: ""}}
		if !reflect.DeepEqual(deps.repo.pauseCalls, wantPause) {
			t.Fatalf("SetPaused calls = %+v, want %+v", deps.repo.pauseCalls, wantPause)
		}
		wantVersion := []markPausedCall{{versionID: 7, paused: false, mode: "graceful", reason: "", actor: 9}}
		if !reflect.DeepEqual(deps.version.markPausedCalls, wantVersion) {
			t.Fatalf("MarkPaused calls = %+v, want %+v", deps.version.markPausedCalls, wantVersion)
		}
		if !reflect.DeepEqual(deps.events.types, []string{"batch.resumed"}) {
			t.Fatalf("event types = %v", deps.events.types)
		}
	})
	t.Run("not paused", func(t *testing.T) {
		service, deps := newBatchServiceTest(t)
		deps.seedBatch(domain.BatchStatusRunning, false)
		_, err := service.Resume(context.Background(), 7, 55, 9)
		wantReason(t, err, "not_paused")
	})
	t.Run("finished", func(t *testing.T) {
		service, deps := newBatchServiceTest(t)
		deps.seedBatch(domain.BatchStatusCancelled, true)
		_, err := service.Resume(context.Background(), 7, 55, 9)
		wantReason(t, err, "batch_finished")
	})
}

// ---------------------------------------------------------------------------
// Cancel
// ---------------------------------------------------------------------------

func TestCancelSkipsPendingAndInterruptsRunning(t *testing.T) {
	service, deps := newBatchServiceTest(t)
	deps.seedBatch(domain.BatchStatusRunning, false)
	deps.seedRun(domain.CaseRun{ID: 101, BatchID: 55, VersionID: 7, CaseNodeID: 11, Attempt: 1, Status: domain.CaseRunStatusPending})
	deps.seedRun(domain.CaseRun{ID: 102, BatchID: 55, VersionID: 7, CaseNodeID: 12, Attempt: 1, Status: domain.CaseRunStatusRunning})
	deps.seedRun(domain.CaseRun{ID: 103, BatchID: 55, VersionID: 7, CaseNodeID: 13, Attempt: 1, Status: domain.CaseRunStatusPassed})

	batch, err := service.Cancel(context.Background(), 7, 55, 9)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Status != domain.BatchStatusCancelled {
		t.Fatalf("batch status = %s, want cancelled", batch.Status)
	}
	if batch.FinishedAt == nil || !batch.FinishedAt.Equal(testNow) {
		t.Fatalf("finished_at = %v, want %v", batch.FinishedAt, testNow)
	}

	wantStatuses := map[int64]domain.CaseRunStatus{
		101: domain.CaseRunStatusSkipped,
		102: domain.CaseRunStatusInterrupted,
		103: domain.CaseRunStatusPassed,
	}
	for id, want := range wantStatuses {
		if got := deps.runs.runs[id].Status; got != want {
			t.Fatalf("run %d status = %s, want %s", id, got, want)
		}
	}
	// An interrupted run left browser state behind, so the same-attempt resume
	// must run cleanup first (spec §5.4, invariant 5).
	if !deps.runs.runs[102].CleanupRequired {
		t.Fatalf("interrupted run 102 must require cleanup: %+v", deps.runs.runs[102])
	}
	if deps.runs.runs[101].CleanupRequired || deps.runs.runs[103].CleanupRequired {
		t.Fatalf("skipped/passed runs must not require cleanup: %+v", deps.runs.runs)
	}
	if len(deps.runs.statusCalls) != 2 {
		t.Fatalf("SetStatus calls = %+v, want 2", deps.runs.statusCalls)
	}
	if deps.repo.countsCalls != 1 {
		t.Fatalf("UpdateCounts calls = %d, want 1", deps.repo.countsCalls)
	}
	wantCounts := domain.BatchCounts{CaseCount: 3, PassedCount: 1, SkippedCount: 1, InterruptedCount: 1}
	if deps.repo.counts != wantCounts {
		t.Fatalf("counts = %+v, want %+v", deps.repo.counts, wantCounts)
	}
	if !reflect.DeepEqual(deps.events.types, []string{"batch.finished"}) {
		t.Fatalf("event types = %v", deps.events.types)
	}
	detailCounts, ok := deps.events.events[0].Detail["counts"].(map[string]int)
	if !ok {
		t.Fatalf("event detail counts = %#v", deps.events.events[0].Detail["counts"])
	}
	if detailCounts["total"] != 3 || detailCounts["passed"] != 1 ||
		detailCounts["skipped"] != 1 || detailCounts["interrupted"] != 1 {
		t.Fatalf("event detail counts = %v", detailCounts)
	}
}

func TestCancelRejectsFinishedBatch(t *testing.T) {
	service, deps := newBatchServiceTest(t)
	deps.seedBatch(domain.BatchStatusFailed, false)
	_, err := service.Cancel(context.Background(), 7, 55, 9)
	wantReason(t, err, "batch_finished")
	if len(deps.version.markPausedCalls) != 0 {
		t.Fatalf("a finished batch must not touch the version mirror: %+v", deps.version.markPausedCalls)
	}
}

func TestCancelClearsPausedBatchAndVersionMirror(t *testing.T) {
	service, deps := newBatchServiceTest(t)
	deps.seedBatch(domain.BatchStatusRunning, true)
	deps.version.paused = true
	deps.seedRun(domain.CaseRun{ID: 101, BatchID: 55, VersionID: 7, CaseNodeID: 11, Attempt: 1, Status: domain.CaseRunStatusPending})
	deps.seedRun(domain.CaseRun{ID: 102, BatchID: 55, VersionID: 7, CaseNodeID: 12, Attempt: 1, Status: domain.CaseRunStatusRunning})

	batch, err := service.Cancel(context.Background(), 7, 55, 9)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Status != domain.BatchStatusCancelled {
		t.Fatalf("batch status = %s, want cancelled", batch.Status)
	}
	if batch.Paused {
		t.Fatalf("a cancelled batch must not stay paused: %+v", batch)
	}
	wantPause := []pauseCall{{batchID: 55, paused: false, mode: domain.PauseModeGraceful, reason: ""}}
	if !reflect.DeepEqual(deps.repo.pauseCalls, wantPause) {
		t.Fatalf("SetPaused calls = %+v, want %+v", deps.repo.pauseCalls, wantPause)
	}
	// The version mirror must be cleared too, otherwise the version stays
	// paused forever while the batch that owned the flag is terminal.
	wantVersion := []markPausedCall{{versionID: 7, paused: false, mode: "graceful", reason: "", actor: 9}}
	if !reflect.DeepEqual(deps.version.markPausedCalls, wantVersion) {
		t.Fatalf("MarkPaused calls = %+v, want %+v", deps.version.markPausedCalls, wantVersion)
	}
	if !deps.runs.runs[102].CleanupRequired {
		t.Fatalf("interrupted run must require cleanup: %+v", deps.runs.runs[102])
	}
	if !reflect.DeepEqual(deps.events.types, []string{"batch.finished"}) {
		t.Fatalf("event types = %v, want batch.finished", deps.events.types)
	}
}

func TestCancelRepairsInterruptedRunCleanupFlag(t *testing.T) {
	service, deps := newBatchServiceTest(t)
	deps.seedBatch(domain.BatchStatusRunning, false)
	// An earlier partial cancel left the run interrupted without cleanup.
	deps.seedRun(domain.CaseRun{ID: 101, BatchID: 55, VersionID: 7, CaseNodeID: 11, Attempt: 1,
		Status: domain.CaseRunStatusInterrupted, CleanupRequired: false})

	if _, err := service.Cancel(context.Background(), 7, 55, 9); err != nil {
		t.Fatal(err)
	}
	if !deps.runs.runs[101].CleanupRequired {
		t.Fatalf("an interrupted run must not stay without cleanup: %+v", deps.runs.runs[101])
	}
	// The run was already interrupted, so no status transition is needed.
	if len(deps.runs.statusCalls) != 0 {
		t.Fatalf("SetStatus calls = %+v, want none", deps.runs.statusCalls)
	}
}

// ---------------------------------------------------------------------------
// Rerun
// ---------------------------------------------------------------------------

func TestRerunCreatesNextAttempt(t *testing.T) {
	service, deps := newBatchServiceTest(t)
	deps.seedBatch(domain.BatchStatusRunning, false)
	failed := domain.CaseRun{
		ID:           101,
		BatchID:      55,
		VersionID:    7,
		CaseNodeID:   12,
		CaseRevision: 2,
		CaseHash:     domain.CanonicalCaseHash(testSnapshotView(12, testBaselineHash).asSnapshot(testNow)),
		Snapshot:     testSnapshotView(12, testBaselineHash).asSnapshot(testNow),
		Attempt:      1,
		Status:       domain.CaseRunStatusFailed,
	}
	deps.seedRun(failed)

	rerun, err := service.Rerun(context.Background(), 7, 55, 101, 9)
	if err != nil {
		t.Fatal(err)
	}
	if rerun.ID == failed.ID || rerun.ID != 1 {
		t.Fatalf("rerun id = %d, want a freshly minted id", rerun.ID)
	}
	if rerun.Attempt != 2 {
		t.Fatalf("attempt = %d, want 2", rerun.Attempt)
	}
	if rerun.RetryOfCaseRunID == nil || *rerun.RetryOfCaseRunID != failed.ID {
		t.Fatalf("retry_of = %v, want %d", rerun.RetryOfCaseRunID, failed.ID)
	}
	if rerun.Status != domain.CaseRunStatusPending || rerun.BatchID != 55 || rerun.VersionID != 7 {
		t.Fatalf("rerun row = %+v", rerun)
	}
	if rerun.CaseNodeID != failed.CaseNodeID || rerun.CaseRevision != failed.CaseRevision ||
		rerun.CaseHash != failed.CaseHash {
		t.Fatalf("rerun identity = %+v", rerun)
	}
	if !reflect.DeepEqual(rerun.Snapshot, failed.Snapshot) {
		t.Fatalf("rerun snapshot = %+v, want %+v", rerun.Snapshot, failed.Snapshot)
	}
	if rerun.SessionID != nil || rerun.AgentRunID != nil || rerun.CleanupRequired ||
		rerun.StartedAt != nil || rerun.FinishedAt != nil {
		t.Fatalf("a fresh attempt must start clean: %+v", rerun)
	}
	if len(deps.runs.createCalls) != 1 || deps.runs.createCalls[0].ID != rerun.ID {
		t.Fatalf("CreateCaseRun calls = %+v", deps.runs.createCalls)
	}
	// Rerun only creates a pending attempt; the scheduler owns run.started and
	// emits it when it dispatches that attempt. Publishing here would double
	// the event for one dispatch.
	if len(deps.events.types) != 0 {
		t.Fatalf("rerun must not publish an event, got %v", deps.events.types)
	}
}

func TestRerunDeepCopiesSnapshotMaps(t *testing.T) {
	service, deps := newBatchServiceTest(t)
	deps.seedBatch(domain.BatchStatusRunning, false)
	view := testSnapshotView(12, testBaselineHash)
	failed := domain.CaseRun{
		ID:           101,
		BatchID:      55,
		VersionID:    7,
		CaseNodeID:   12,
		CaseRevision: 2,
		CaseHash:     domain.CanonicalCaseHash(view.asSnapshot(testNow)),
		Snapshot:     view.asSnapshot(testNow),
		Attempt:      1,
		Status:       domain.CaseRunStatusFailed,
	}
	deps.seedRun(failed)

	rerun, err := service.Rerun(context.Background(), 7, 55, 101, 9)
	if err != nil {
		t.Fatal(err)
	}

	// Mutating the original run's snapshot must not leak into the new attempt,
	// including the nested test_data and step data maps.
	original := deps.runs.runs[101]
	original.Snapshot.Node.TestData["key"] = "changed"
	original.Snapshot.Node.TestData["nested"].(map[string]any)["n"] = 99
	original.Snapshot.Steps[0].Data["page"] = 42
	deps.runs.runs[101] = original

	copied := deps.runs.runs[rerun.ID]
	if got := copied.Snapshot.Node.TestData["key"]; got != "value" {
		t.Fatalf("rerun test_data aliased the original: key = %v", got)
	}
	if got := copied.Snapshot.Node.TestData["nested"].(map[string]any)["n"]; got != 1 {
		t.Fatalf("rerun nested test_data aliased the original: n = %v", got)
	}
	if got := copied.Snapshot.Steps[0].Data["page"]; got != 1 {
		t.Fatalf("rerun step data aliased the original: page = %v", got)
	}

	// The reverse direction must hold as well.
	copied.Snapshot.Node.TestData["key"] = "rerun-changed"
	deps.runs.runs[rerun.ID] = copied
	if got := deps.runs.runs[101].Snapshot.Node.TestData["key"]; got != "changed" {
		t.Fatalf("original test_data aliased the rerun: key = %v", got)
	}
}

func TestRerunRejectsNonTerminalCaseRun(t *testing.T) {
	service, deps := newBatchServiceTest(t)
	deps.seedBatch(domain.BatchStatusRunning, false)
	deps.seedRun(domain.CaseRun{ID: 101, BatchID: 55, VersionID: 7, CaseNodeID: 11, Attempt: 1, Status: domain.CaseRunStatusRunning})
	_, err := service.Rerun(context.Background(), 7, 55, 101, 9)
	wantReason(t, err, "case_run_terminal")
	if len(deps.runs.createCalls) != 0 {
		t.Fatalf("a non-terminal rerun must not create a run")
	}
}

// TestRerunRejectsFinishedBatch pins that a rerun of a terminal batch is
// rejected as batch_finished instead of creating an attempt the scheduler can
// never dispatch (it only scans planned/running batches).
func TestRerunRejectsFinishedBatch(t *testing.T) {
	for _, status := range []domain.BatchStatus{
		domain.BatchStatusDone, domain.BatchStatusFailed, domain.BatchStatusCancelled,
	} {
		t.Run(string(status), func(t *testing.T) {
			service, deps := newBatchServiceTest(t)
			deps.seedBatch(status, false)
			deps.seedRun(domain.CaseRun{
				ID: 101, BatchID: 55, VersionID: 7, CaseNodeID: 11, Attempt: 1,
				Status: domain.CaseRunStatusFailed,
			})

			_, err := service.Rerun(context.Background(), 7, 55, 101, 9)
			wantReason(t, err, "batch_finished")
			if len(deps.runs.createCalls) != 0 {
				t.Fatal("a finished-batch rerun must not create an attempt")
			}
		})
	}
}

func TestRerunRejectsUnknownCaseRun(t *testing.T) {
	service, deps := newBatchServiceTest(t)
	deps.seedBatch(domain.BatchStatusRunning, false)
	_, err := service.Rerun(context.Background(), 7, 55, 999, 9)
	if !errors.Is(err, ErrCaseRunNotFound) {
		t.Fatalf("error = %v, want ErrCaseRunNotFound", err)
	}
}

// asSnapshot projects the fake mindmap view into a domain snapshot exactly like
// the service does.
func (v CaseSnapshotSourceView) asSnapshot(capturedAt time.Time) domain.CaseSnapshot {
	sources := make([]domain.CaseSnapshotSource, 0, len(v.RequirementIDs))
	for _, id := range v.RequirementIDs {
		sources = append(sources, domain.CaseSnapshotSource{RequirementID: id})
	}
	return domain.CaseSnapshot{
		Node:       v.Node,
		Steps:      v.Steps,
		Assertions: v.Assertions,
		Sources:    sources,
		GraphHash:  v.GraphHash,
		CapturedAt: capturedAt,
	}
}

// ---------------------------------------------------------------------------
// Fakes: ports
// ---------------------------------------------------------------------------

type setBatchNoCall struct {
	versionID int64
	batchNo   int
	actor     int64
}

type releaseBatchNoCall struct {
	versionID int64
	allocated int
}

// AllocateBatchNo and ReleaseBatchNo keep recovery_test.go's older fake
// satisfying VersionPort now that batch-number allocation is part of the port.
// Recovery never creates a batch, so the stubs fail closed instead of inventing
// a number.
func (f *recoveryVersionPort) AllocateBatchNo(context.Context, int64) (int, error) {
	return 0, errors.New("execution: recovery must not allocate a batch number")
}

func (f *recoveryVersionPort) ReleaseBatchNo(context.Context, int64, int) error { return nil }

type markPausedCall struct {
	versionID int64
	paused    bool
	mode      string
	reason    string
	actor     int64
}

type fakeVersionPort struct {
	status        string
	paused        bool
	batchNo       int
	systemID      *int64
	environmentID *int64
	codeCommit    *string
	getErr        error

	markPausedCalls []markPausedCall
	markPausedErr   error
	allocateCalls   []int64
	allocateErr     error
	releaseCalls    []releaseBatchNoCall
	releaseErr      error
	setBatchNoCalls []setBatchNoCall
	setBatchNoErr   error
	completeCalls   int
}

func (f *fakeVersionPort) GetVersion(ctx context.Context, versionID int64) (VersionRef, error) {
	if f.getErr != nil {
		return VersionRef{}, f.getErr
	}
	return VersionRef{
		ID:                 versionID,
		Status:             f.status,
		Paused:             f.paused,
		BatchNo:            f.batchNo,
		SystemID:           f.systemID,
		EnvironmentID:      f.environmentID,
		CodeBaselineCommit: f.codeCommit,
	}, nil
}

func (f *fakeVersionPort) MarkPaused(ctx context.Context, versionID int64, paused bool, mode, reason string, actor int64) error {
	f.markPausedCalls = append(f.markPausedCalls, markPausedCall{versionID, paused, mode, reason, actor})
	return f.markPausedErr
}

func (f *fakeVersionPort) AllocateBatchNo(ctx context.Context, versionID int64) (int, error) {
	f.allocateCalls = append(f.allocateCalls, versionID)
	if f.allocateErr != nil {
		return 0, f.allocateErr
	}
	f.batchNo++
	return f.batchNo, nil
}

func (f *fakeVersionPort) ReleaseBatchNo(ctx context.Context, versionID int64, allocated int) error {
	f.releaseCalls = append(f.releaseCalls, releaseBatchNoCall{versionID: versionID, allocated: allocated})
	if f.releaseErr != nil {
		return f.releaseErr
	}
	// The production adapter only rolls a number back while it is still the
	// newest one; mirror that so tests catch a service that reuses stale ids.
	if f.batchNo == allocated {
		f.batchNo = allocated - 1
	}
	return nil
}

func (f *fakeVersionPort) SetBatchNo(ctx context.Context, versionID int64, batchNo int, actor int64) error {
	f.setBatchNoCalls = append(f.setBatchNoCalls, setBatchNoCall{versionID, batchNo, actor})
	if f.setBatchNoErr == nil {
		f.batchNo = batchNo
	}
	return f.setBatchNoErr
}

func (f *fakeVersionPort) CompleteExecution(ctx context.Context, versionID, actor int64) error {
	f.completeCalls++
	return nil
}

type fakeMindmapPort struct {
	baseline      BaselineRef
	baselineErr   error
	snapshots     map[int64]CaseSnapshotSourceView
	snapshotErr   error
	snapshotCalls [][]int64

	markExecutedCalls [][]int64
	markExecutedErr   error
}

func (f *fakeMindmapPort) Baseline(ctx context.Context, versionID int64) (BaselineRef, error) {
	if f.baselineErr != nil {
		return BaselineRef{}, f.baselineErr
	}
	return f.baseline, nil
}

func (f *fakeMindmapPort) SnapshotCases(ctx context.Context, versionID int64, caseIDs []int64) ([]CaseSnapshotSourceView, error) {
	f.snapshotCalls = append(f.snapshotCalls, append([]int64(nil), caseIDs...))
	if f.snapshotErr != nil {
		return nil, f.snapshotErr
	}
	views := make([]CaseSnapshotSourceView, 0, len(caseIDs))
	for _, id := range caseIDs {
		view, ok := f.snapshots[id]
		if !ok {
			return nil, fmt.Errorf("unexpected snapshot request for case %d", id)
		}
		views = append(views, view)
	}
	return views, nil
}

func (f *fakeMindmapPort) MarkExecuted(ctx context.Context, versionID int64, caseNodeIDs []int64) error {
	f.markExecutedCalls = append(f.markExecutedCalls, append([]int64(nil), caseNodeIDs...))
	return f.markExecutedErr
}

type fakeEventPublisher struct {
	types  []string
	events []Event
	err    error
}

func (f *fakeEventPublisher) Publish(ctx context.Context, event Event) error {
	if f.err != nil {
		return f.err
	}
	f.types = append(f.types, event.Type)
	f.events = append(f.events, event)
	return nil
}

type fakeIDGenerator struct {
	next int64
}

func (f *fakeIDGenerator) Next() int64 {
	f.next++
	return f.next
}

type fakeClock struct {
	now time.Time
}

func (f *fakeClock) Now() time.Time { return f.now }

// ---------------------------------------------------------------------------
// Fakes: repositories
// ---------------------------------------------------------------------------

type listBatchesCall struct {
	versionID int64
	beforeID  int64
	limit     int
}

type pauseCall struct {
	batchID int64
	paused  bool
	mode    domain.PauseMode
	reason  string
}

type updateStatusCall struct {
	batchID    int64
	from, to   domain.BatchStatus
	finishedAt *time.Time
}

type fakeBatchRepository struct {
	batches       map[int64]domain.RunBatch
	createCalls   int
	createdBatch  domain.RunBatch
	createdRuns   []domain.CaseRun
	createErr     error
	getErr        error
	list          []domain.RunBatch
	listMore      bool
	listErr       error
	listCalls     []listBatchesCall
	pauseCalls    []pauseCall
	pauseErr      error
	statusCalls   []updateStatusCall
	statusErr     error
	caseRuns      *fakeCaseRunRepository
	countsCalls   int
	counts        domain.BatchCounts
	countsErr     error
	failureErr    error
	failureReason string
}

func newFakeBatchRepository() *fakeBatchRepository {
	return &fakeBatchRepository{batches: map[int64]domain.RunBatch{}}
}

func (f *fakeBatchRepository) CreateBatchWithRuns(ctx context.Context, batch domain.RunBatch, runs []domain.CaseRun) (domain.RunBatch, error) {
	f.createCalls++
	if f.createErr != nil {
		return domain.RunBatch{}, f.createErr
	}
	stored := batch
	stored.CaseCount = len(runs)
	f.createdBatch = stored
	f.createdRuns = append([]domain.CaseRun(nil), runs...)
	f.batches[stored.ID] = stored
	return stored, nil
}

func (f *fakeBatchRepository) GetBatch(ctx context.Context, versionID, batchID int64) (domain.RunBatch, error) {
	if f.getErr != nil {
		return domain.RunBatch{}, f.getErr
	}
	batch, ok := f.batches[batchID]
	if !ok || batch.VersionID != versionID {
		return domain.RunBatch{}, ErrBatchNotFound
	}
	return batch, nil
}

func (f *fakeBatchRepository) ListBatches(ctx context.Context, versionID int64, beforeID int64, limit int) ([]domain.RunBatch, bool, error) {
	f.listCalls = append(f.listCalls, listBatchesCall{versionID, beforeID, limit})
	if f.listErr != nil {
		return nil, false, f.listErr
	}
	return f.list, f.listMore, nil
}

func (f *fakeBatchRepository) UpdateBatchStatus(ctx context.Context, batchID int64, from, to domain.BatchStatus, finishedAt *time.Time) (domain.RunBatch, error) {
	f.statusCalls = append(f.statusCalls, updateStatusCall{batchID, from, to, finishedAt})
	if f.statusErr != nil {
		return domain.RunBatch{}, f.statusErr
	}
	batch, ok := f.batches[batchID]
	if !ok {
		return domain.RunBatch{}, ErrBatchNotFound
	}
	batch.Status = to
	batch.FinishedAt = finishedAt
	f.batches[batchID] = batch
	return batch, nil
}

func (f *fakeBatchRepository) SetPaused(ctx context.Context, batchID int64, paused bool, mode domain.PauseMode, reason string) (domain.RunBatch, error) {
	f.pauseCalls = append(f.pauseCalls, pauseCall{batchID, paused, mode, reason})
	if f.pauseErr != nil {
		return domain.RunBatch{}, f.pauseErr
	}
	batch, ok := f.batches[batchID]
	if !ok {
		return domain.RunBatch{}, ErrBatchNotFound
	}
	batch.Paused = paused
	batch.PauseMode = mode
	batch.PauseReason = reason
	f.batches[batchID] = batch
	return batch, nil
}

func (f *fakeBatchRepository) UpdateCounts(ctx context.Context, batchID int64) (domain.BatchCounts, error) {
	f.countsCalls++
	if f.countsErr != nil {
		return domain.BatchCounts{}, f.countsErr
	}
	runs := make([]domain.CaseRun, 0)
	if f.caseRuns != nil {
		for _, run := range f.caseRuns.runs {
			if run.BatchID == batchID {
				runs = append(runs, run)
			}
		}
	}
	counts := domain.Aggregate(runs)
	f.counts = counts
	return counts, nil
}

func (f *fakeBatchRepository) UpdateBatchFailure(ctx context.Context, batchID int64, reason string) (domain.RunBatch, error) {
	f.failureReason = reason
	if f.failureErr != nil {
		return domain.RunBatch{}, f.failureErr
	}
	return f.batches[batchID], nil
}

type fakeCaseRunSetStatusCall struct {
	caseRunID int64
	from, to  domain.CaseRunStatus
}

type fakeCaseRunRepository struct {
	runs        map[int64]domain.CaseRun
	createCalls []domain.CaseRun
	createErr   error
	getErr      error
	listErr     error
	statusCalls []fakeCaseRunSetStatusCall
	statusErr   error
}

func newFakeCaseRunRepository() *fakeCaseRunRepository {
	return &fakeCaseRunRepository{runs: map[int64]domain.CaseRun{}}
}

func (f *fakeCaseRunRepository) CreateCaseRun(ctx context.Context, run domain.CaseRun) (domain.CaseRun, error) {
	f.createCalls = append(f.createCalls, run)
	if f.createErr != nil {
		return domain.CaseRun{}, f.createErr
	}
	f.runs[run.ID] = run
	return run, nil
}

func (f *fakeCaseRunRepository) GetCaseRun(ctx context.Context, batchID, caseRunID int64) (domain.CaseRun, error) {
	if f.getErr != nil {
		return domain.CaseRun{}, f.getErr
	}
	run, ok := f.runs[caseRunID]
	if !ok || run.BatchID != batchID {
		return domain.CaseRun{}, ErrCaseRunNotFound
	}
	return run, nil
}

func (f *fakeCaseRunRepository) GetCaseRunForUpdate(ctx context.Context, batchID, caseRunID int64) (domain.CaseRun, error) {
	return f.GetCaseRun(ctx, batchID, caseRunID)
}

func (f *fakeCaseRunRepository) ListCaseRuns(ctx context.Context, batchID int64, status domain.CaseRunStatus, beforeID int64, limit int) ([]domain.CaseRun, bool, error) {
	if f.listErr != nil {
		return nil, false, f.listErr
	}
	runs := make([]domain.CaseRun, 0, len(f.runs))
	for _, run := range f.runs {
		if run.BatchID != batchID {
			continue
		}
		if status != "" && run.Status != status {
			continue
		}
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID < runs[j].ID })
	return runs, false, nil
}

func (f *fakeCaseRunRepository) NextPending(ctx context.Context, batchID int64) (domain.CaseRun, error) {
	return domain.CaseRun{}, ErrNoPendingCaseRun
}

func (f *fakeCaseRunRepository) UpdateCaseRun(ctx context.Context, run domain.CaseRun) (domain.CaseRun, error) {
	f.runs[run.ID] = run
	return run, nil
}

func (f *fakeCaseRunRepository) SetStatus(ctx context.Context, caseRunID int64, from, to domain.CaseRunStatus, at time.Time) (domain.CaseRun, error) {
	f.statusCalls = append(f.statusCalls, fakeCaseRunSetStatusCall{caseRunID, from, to})
	if f.statusErr != nil {
		return domain.CaseRun{}, f.statusErr
	}
	run, ok := f.runs[caseRunID]
	if !ok {
		return domain.CaseRun{}, ErrCaseRunNotFound
	}
	if run.Status != from {
		return domain.CaseRun{}, domain.ErrCaseRunTerminal
	}
	run.Status = to
	f.runs[caseRunID] = run
	return run, nil
}

func (f *fakeCaseRunRepository) FinishCaseRun(ctx context.Context, run domain.CaseRun) (domain.CaseRun, error) {
	return f.UpdateCaseRun(ctx, run)
}

func int64Ptr(value int64) *int64 { return &value }
