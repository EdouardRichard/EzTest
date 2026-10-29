package delivery

import (
	"context"
	"errors"
	"testing"
	"time"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	deliverydomain "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// fakeAnalysisStarter drives the execution AnalysisPort adapter.
type fakeAnalysisStarter struct {
	run       deliverydomain.AnalysisRun
	startErr  error
	latest    deliverydomain.AnalysisRun
	latestErr error
}

func (f fakeAnalysisStarter) StartFailure(context.Context, int64, int64, int64) (deliverydomain.AnalysisRun, error) {
	return f.run, f.startErr
}

func (f fakeAnalysisStarter) StartTriage(context.Context, int64, int64, int64) (deliverydomain.AnalysisRun, error) {
	return f.run, f.startErr
}

func (f fakeAnalysisStarter) LatestByKind(context.Context, int64, string) (deliverydomain.AnalysisRun, error) {
	return f.latest, f.latestErr
}

// TestAnalysisPortMapsActiveToWaitSentinel pins the T16 handoff: a duplicate
// start (409 analysis_active) keeps the existing run id and answers the worker's
// wait sentinel so the chain continues that run instead of degrading.
func TestAnalysisPortMapsActiveToWaitSentinel(t *testing.T) {
	existing := deliverydomain.AnalysisRun{ID: 71, Kind: deliverydomain.AnalysisKindFailure, Status: deliverydomain.AnalysisRunning}
	port := NewAnalysisPort(fakeAnalysisStarter{
		run:       existing,
		startErr:  deliverydomain.NewAnalysisConflict(deliverydomain.ReasonAnalysisActive, "active"),
		latest:    existing,
		latestErr: nil,
	})
	runID, err := port.StartFailureAnalysis(context.Background(), 7, 41, 0)
	if !errors.Is(err, executionapp.ErrAutonomyAnalysisActive) {
		t.Fatalf("err=%v want ErrAutonomyAnalysisActive", err)
	}
	if runID != 71 {
		t.Fatalf("run id=%d want the existing run 71", runID)
	}
}

// TestAnalysisPortMapsNotApplicable pins the no-failure skip sentinel.
func TestAnalysisPortMapsNotApplicable(t *testing.T) {
	port := NewAnalysisPort(fakeAnalysisStarter{
		startErr: deliverydomain.NewAnalysisNotApplicable("no failed application case"),
	})
	if _, err := port.StartIssueTriage(context.Background(), 7, 41, 0); !errors.Is(err, executionapp.ErrAutonomyAnalysisNotApplicable) {
		t.Fatalf("err=%v want ErrAutonomyAnalysisNotApplicable", err)
	}
}

// TestAnalysisPortGetAnalysis covers the missing-run and candidate-count
// projections.
func TestAnalysisPortGetAnalysis(t *testing.T) {
	missing := NewAnalysisPort(fakeAnalysisStarter{latestErr: deliveryapp.ErrAnalysisRunNotFound})
	view, err := missing.GetAnalysis(context.Background(), 41, executionapp.AutonomyAnalysisKindFailure)
	if err != nil || view != nil {
		t.Fatalf("view=%+v err=%v want nil,nil for a missing run", view, err)
	}

	result := deliverydomain.EncodeFailureResult(
		[]deliverydomain.FailureCandidate{{CaseRunID: 1, Title: "t", Severity: "major", Confidence: "high"}},
		nil,
	)
	found := NewAnalysisPort(fakeAnalysisStarter{latest: deliverydomain.AnalysisRun{
		ID: 5, Kind: deliverydomain.AnalysisKindFailure, Status: deliverydomain.AnalysisDegraded, Result: result,
	}})
	view, err = found.GetAnalysis(context.Background(), 41, executionapp.AutonomyAnalysisKindFailure)
	if err != nil {
		t.Fatalf("GetAnalysis: %v", err)
	}
	if view == nil || view.ID != 5 || view.CandidateCount != 1 || !view.Degraded {
		t.Fatalf("view=%+v want run 5 with one candidate, degraded", view)
	}
}

// fakeAnalysisStore is the minimal delivery AnalysisStore needed to drive the
// real AnalysisService: only Active and Latest are used by the missing-run path.
type fakeAnalysisStore struct{}

func (fakeAnalysisStore) Create(context.Context, deliverydomain.AnalysisRun) (deliverydomain.AnalysisRun, error) {
	return deliverydomain.AnalysisRun{}, deliveryapp.ErrAnalysisRunNotFound
}

func (fakeAnalysisStore) Get(context.Context, int64, int64, int64) (deliverydomain.AnalysisRun, error) {
	return deliverydomain.AnalysisRun{}, deliveryapp.ErrAnalysisRunNotFound
}

func (fakeAnalysisStore) GetByID(context.Context, int64) (deliverydomain.AnalysisRun, error) {
	return deliverydomain.AnalysisRun{}, deliveryapp.ErrAnalysisRunNotFound
}

func (fakeAnalysisStore) List(context.Context, int64, int64) ([]deliverydomain.AnalysisRun, error) {
	return nil, nil
}

func (fakeAnalysisStore) Active(context.Context, int64, string) (deliverydomain.AnalysisRun, error) {
	return deliverydomain.AnalysisRun{}, deliveryapp.ErrAnalysisRunNotFound
}

func (fakeAnalysisStore) Latest(context.Context, int64, string) (deliverydomain.AnalysisRun, error) {
	return deliverydomain.AnalysisRun{}, deliveryapp.ErrAnalysisRunNotFound
}

func (fakeAnalysisStore) ListActive(context.Context, int) ([]deliverydomain.AnalysisRun, error) {
	return nil, nil
}

func (fakeAnalysisStore) UpdateStatus(context.Context, int64, string, string, deliveryapp.AnalysisRunPatch) (deliverydomain.AnalysisRun, error) {
	return deliverydomain.AnalysisRun{}, deliveryapp.ErrAnalysisRunNotFound
}

func (fakeAnalysisStore) RecoverStale(context.Context) ([]deliverydomain.AnalysisRun, error) {
	return nil, nil
}

// fakeFailedCaseReader answers the failure-analysis execution port with no
// failed case, which is the analysis_not_applicable skip path.
type fakeFailedCaseReader struct{}

func (fakeFailedCaseReader) ListFailedCases(context.Context, int64, int64, int) ([]deliveryapp.AnalysisFailedCase, error) {
	return nil, nil
}

// fakeAnalysisAgentRuntime satisfies the real AnalysisService port gate; the
// not-applicable path never starts a session.
type fakeAnalysisAgentRuntime struct{}

func (fakeAnalysisAgentRuntime) CreateSession(context.Context, string, string, int64, string, string) (int64, error) {
	return 0, errors.New("not used")
}

func (fakeAnalysisAgentRuntime) Start(context.Context, int64, string, int64, deliveryapp.AnalysisAgentRunScope) (int64, error) {
	return 0, errors.New("not used")
}

func (fakeAnalysisAgentRuntime) GetSession(context.Context, int64) (deliveryapp.AnalysisAgentSessionStatus, error) {
	return deliveryapp.AnalysisAgentSessionStatus{}, errors.New("not used")
}

func (fakeAnalysisAgentRuntime) Abort(context.Context, int64, int64) error { return nil }

// fakeWorkerBatchStore is the minimal AutonomyBatchStore of the worker-proceeds
// test: one pending terminal batch and the autonomy_done CAS.
type fakeWorkerBatchStore struct {
	batch *executiondomain.RunBatch
	done  bool
}

func (s *fakeWorkerBatchStore) PendingAutonomyBatches(context.Context, int) ([]executiondomain.RunBatch, error) {
	return []executiondomain.RunBatch{*s.batch}, nil
}

func (s *fakeWorkerBatchStore) DesigningVersions(context.Context, int) ([]executionapp.DesigningVersion, error) {
	return nil, nil
}

func (s *fakeWorkerBatchStore) VersionsAwaitingFinalize(context.Context, int) ([]int64, error) {
	return nil, nil
}

func (s *fakeWorkerBatchStore) MarkSupplementAttempted(context.Context, int64) (bool, error) {
	return false, nil
}

func (s *fakeWorkerBatchStore) MarkAutonomyDone(context.Context, int64) (bool, error) {
	if s.done {
		return false, nil
	}
	s.done = true
	return true, nil
}

func (s *fakeWorkerBatchStore) ResetAutonomyDone(context.Context, int64) (bool, error) {
	s.done = false
	return true, nil
}

func (s *fakeWorkerBatchStore) SupplementChild(context.Context, int64) (*executiondomain.RunBatch, error) {
	return nil, nil
}

// fakeWorkerVersionPort is the minimal executing/version projection of the
// worker-proceeds test.
type fakeWorkerVersionPort struct{}

func (fakeWorkerVersionPort) GetVersion(_ context.Context, versionID int64) (executionapp.VersionRef, error) {
	return executionapp.VersionRef{ID: versionID, Status: "executing"}, nil
}

func (fakeWorkerVersionPort) MarkPaused(context.Context, int64, bool, string, string, int64) error {
	return nil
}

func (fakeWorkerVersionPort) AllocateBatchNo(context.Context, int64) (int, error) { return 0, nil }

func (fakeWorkerVersionPort) ReleaseBatchNo(context.Context, int64, int) error { return nil }

func (fakeWorkerVersionPort) SetBatchNo(context.Context, int64, int, int64) error { return nil }

func (fakeWorkerVersionPort) CompleteExecution(context.Context, int64, int64) error { return nil }

// recordingFinalizer records the version finalize of the worker-proceeds test.
type recordingFinalizer struct {
	called     bool
	versionIDs []int64
}

func (f *recordingFinalizer) TryFinalize(_ context.Context, versionID int64) (bool, error) {
	f.called = true
	f.versionIDs = append(f.versionIDs, versionID)
	return true, nil
}

func (f *recordingFinalizer) SetContinuePhase(context.Context, int64, string) error { return nil }

// TestAnalysisPortMissingRunThroughRealService pins the CRITICAL T17a mapping:
// the real AnalysisService.LatestByKind answers a delivery domain
// analysis_not_found error, and the execution adapter must translate it to
// nil,nil instead of surfacing it as an infrastructure failure. The worker half
// of the same test proves the chain advances (the failure step skips and the
// batch settles) rather than retrying forever.
func TestAnalysisPortMissingRunThroughRealService(t *testing.T) {
	service := deliveryapp.NewAnalysisService(deliveryapp.AnalysisDeps{
		Runs:      fakeAnalysisStore{},
		Execution: fakeFailedCaseReader{},
		Agents:    fakeAnalysisAgentRuntime{},
	})
	port := NewAnalysisPort(service)

	view, err := port.GetAnalysis(context.Background(), 41, executionapp.AutonomyAnalysisKindFailure)
	if err != nil || view != nil {
		t.Fatalf("GetAnalysis view=%+v err=%v want nil,nil for a missing run", view, err)
	}

	store := &fakeWorkerBatchStore{batch: &executiondomain.RunBatch{
		ID:        41,
		VersionID: 7,
		BatchNo:   1,
		Status:    executiondomain.BatchStatusDone,
		Kind:      executiondomain.BatchKindBaseline,
		CreatedAt: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC),
	}}
	finalizer := &recordingFinalizer{}
	worker := executionapp.NewAutonomyWorker(executionapp.AutonomyWorkerDeps{
		Store:          store,
		Versions:       fakeWorkerVersionPort{},
		Analysis:       port,
		Finalizer:      finalizer,
		Actor:          7,
		BatchesPerTick: 4,
	})
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("the worker did not proceed past the missing analysis run: %v", err)
	}
	if !store.done {
		t.Fatal("the worker did not settle the batch")
	}
	if !finalizer.called || len(finalizer.versionIDs) != 1 || finalizer.versionIDs[0] != 7 {
		t.Fatalf("finalizer calls=%v want version 7", finalizer.versionIDs)
	}
}

// fakeReportEnsurer drives the execution ReportPort adapter.
type fakeReportEnsurer struct {
	report  deliverydomain.Report
	created bool
	ensure  error
	list    []deliverydomain.Report
	listErr error
}

func (f fakeReportEnsurer) Ensure(context.Context, int64, int64, int64) (deliverydomain.Report, bool, error) {
	return f.report, f.created, f.ensure
}

func (f fakeReportEnsurer) Regenerate(context.Context, int64, int64, int64) (deliverydomain.Report, error) {
	return f.report, f.ensure
}

func (f fakeReportEnsurer) List(context.Context, int64, deliveryapp.ReportFilter) ([]deliverydomain.Report, bool, error) {
	return f.list, false, f.listErr
}

// TestReportPortEnsureAndLatest covers the ensure projection and the
// latest-report read (a batch without a report is nil, nil).
func TestReportPortEnsureAndLatest(t *testing.T) {
	port := NewReportPort(fakeReportEnsurer{
		report:  deliverydomain.Report{ID: 88, Status: deliverydomain.ReportStatusPublished, Revision: 2},
		created: true,
	})
	reportID, created, err := port.EnsureReport(context.Background(), 7, 41, 0)
	if err != nil || reportID != 88 || !created {
		t.Fatalf("ensure=%d/%v/%v want 88/true/nil", reportID, created, err)
	}

	empty := NewReportPort(fakeReportEnsurer{list: nil})
	latest, err := empty.LatestReport(context.Background(), 7, 41)
	if err != nil || latest != nil {
		t.Fatalf("latest=%+v err=%v want nil,nil", latest, err)
	}

	withReport := NewReportPort(fakeReportEnsurer{list: []deliverydomain.Report{{
		ID: 88, Status: deliverydomain.ReportStatusStale, Revision: 2,
	}}})
	latest, err = withReport.LatestReport(context.Background(), 7, 41)
	if err != nil {
		t.Fatalf("LatestReport: %v", err)
	}
	if latest == nil || latest.ID != 88 || latest.Status != deliverydomain.ReportStatusStale || latest.Revision != 2 {
		t.Fatalf("latest=%+v want report 88 stale revision 2", latest)
	}
}
