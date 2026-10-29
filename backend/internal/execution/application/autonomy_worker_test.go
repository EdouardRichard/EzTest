package application

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// Fixed identifiers of the autonomy worker tests.
const (
	autoBatchID    int64 = 9001
	autoVersionID  int64 = 9002
	autoParentID   int64 = 9003
	autoParentHead       = "0123456789abcdef0123456789abcdef01234567"

	// autoVersionStatusCompleted mirrors the version status the worker skips.
	autoVersionStatusCompleted = "completed"
)

// autoWorld is the in-memory world of one autonomy worker test.
type autoWorld struct {
	store       *autoStore
	versions    *autoVersionPort
	analysis    *autoAnalysis
	reports     *autoReports
	supplements *autoSupplements
	impact      *autoImpact
	finalizer   *autoFinalizer
	delivery    *autoDelivery
	deps        AutonomyWorkerDeps
	worker      *AutonomyWorker
}

func newAutoWorld(t *testing.T) *autoWorld {
	t.Helper()
	world := &autoWorld{
		store:       newAutoStore(),
		versions:    newAutoVersionPort(),
		analysis:    &autoAnalysis{views: map[string]*AnalysisView{}, getErrs: map[string]error{}},
		reports:     &autoReports{},
		supplements: &autoSupplements{},
		impact:      &autoImpact{},
		finalizer:   &autoFinalizer{},
		delivery:    &autoDelivery{},
	}
	world.store.seed(domain.RunBatch{
		ID:         autoBatchID,
		VersionID:  autoVersionID,
		BatchNo:    1,
		Status:     domain.BatchStatusDone,
		Kind:       domain.BatchKindBaseline,
		CaseCount:  1,
		CreatedAt:  testNow,
		UpdatedAt:  testNow,
		FinishedAt: timePointer(testNow),
	})
	world.deps = AutonomyWorkerDeps{
		Store:          world.store,
		Versions:       world.versions,
		Analysis:       world.analysis,
		Reports:        world.reports,
		Supplements:    world.supplements,
		Impact:         world.impact,
		Finalizer:      world.finalizer,
		Delivery:       world.delivery,
		Actor:          7,
		BatchesPerTick: 4,
	}
	world.worker = NewAutonomyWorker(world.deps)
	return world
}

// runOnce performs one worker tick and fails the test on an infrastructure
// error.
func (w *autoWorld) runOnce(t *testing.T) {
	t.Helper()
	if err := w.worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
}

// batch returns the stored batch of autoBatchID (a value copy).
func (w *autoWorld) batch() domain.RunBatch {
	w.store.mu.Lock()
	defer w.store.mu.Unlock()
	return w.store.batches[0]
}

// mutateBatch applies one change to the stored batch.
func (w *autoWorld) mutateBatch(mutate func(*domain.RunBatch)) {
	w.store.mu.Lock()
	defer w.store.mu.Unlock()
	mutate(&w.store.batches[0])
}

// seedReadyChain prepares the state in which analysis and report already
// settled, so the next tick starts the post-batch supplement.
func (w *autoWorld) seedReadyChain() {
	w.analysis.startFailureErr = ErrAutonomyAnalysisNotApplicable
	w.reports.latest = &ReportStateView{ID: 700, Status: AutonomyReportStatusPublished, Revision: 1}
}

// seedDesigning makes the version continuing+designing with one terminal parent
// batch and no pending batch chain.
func (w *autoWorld) seedDesigning() *domain.RunBatch {
	w.store.mu.Lock()
	w.store.batches = nil
	w.store.mu.Unlock()

	w.versions.status = versionStatusContinuing
	w.versions.continuePhase = continuePhaseDesigning
	parent := &domain.RunBatch{
		ID:         autoParentID,
		VersionID:  autoVersionID,
		BatchNo:    1,
		Status:     domain.BatchStatusDone,
		Kind:       domain.BatchKindBaseline,
		HeadCommit: autoStringPointer(autoParentHead),
		CreatedAt:  testNow,
		UpdatedAt:  testNow,
	}
	w.store.design = []DesigningVersion{{
		Version: VersionRef{
			ID:            autoVersionID,
			Status:        versionStatusContinuing,
			ContinuePhase: continuePhaseDesigning,
		},
		Parent: parent,
	}}
	return parent
}

// autoStringPointer returns a pointer to a copy of value.
func autoStringPointer(value string) *string { return &value }

// ---------------------------------------------------------------------------
// Worker dependency fakes
// ---------------------------------------------------------------------------

// autoStore is an in-memory AutonomyStore. CAS flags are persisted so a second
// RunOnce observes the stored state, exactly like the postgres adapter.
type autoStore struct {
	mu       sync.Mutex
	batches  []domain.RunBatch
	design   []DesigningVersion
	child    map[int64]*domain.RunBatch
	awaiting []int64

	markDoneCalls   int
	markSuppCalls   int
	pendingCalls    int
	resetCalls      int
	markDoneResult  bool
	markSuppResult  bool
	resetResult     bool
	markDoneErr     error
	markSuppErr     error
	resetErr        error
	pendingErr      error
	designingErr    error
	awaitingErr     error
	supplementChild error
	limitCalls      []int
}

func newAutoStore() *autoStore {
	return &autoStore{
		child:          map[int64]*domain.RunBatch{},
		markDoneResult: true,
		markSuppResult: true,
		resetResult:    true,
	}
}

// setAwaitingFinalize seeds the DB-derived finalize sweep projection.
func (s *autoStore) setAwaitingFinalize(versionIDs ...int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.awaiting = append([]int64(nil), versionIDs...)
}

// pendingCallCount reports how many times the batch scan ran (lifecycle tests).
func (s *autoStore) pendingCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingCalls
}

// VersionsAwaitingFinalize is the DB-derived finalize sweep projection.
func (s *autoStore) VersionsAwaitingFinalize(ctx context.Context, limit int) ([]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.awaitingErr != nil {
		return nil, s.awaitingErr
	}
	out := append([]int64(nil), s.awaiting...)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ResetAutonomyDone mirrors the true -> false manual-analysis CAS.
func (s *autoStore) ResetAutonomyDone(ctx context.Context, batchID int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetCalls++
	if s.resetErr != nil {
		return false, s.resetErr
	}
	if !s.resetResult {
		return false, nil
	}
	for i := range s.batches {
		if s.batches[i].ID == batchID && s.batches[i].AutonomyDone {
			s.batches[i].AutonomyDone = false
			return true, nil
		}
	}
	return false, nil
}

func (s *autoStore) seed(batch domain.RunBatch) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches = append(s.batches, batch)
}

func (s *autoStore) PendingAutonomyBatches(ctx context.Context, limit int) ([]domain.RunBatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limitCalls = append(s.limitCalls, limit)
	s.pendingCalls++
	if s.pendingErr != nil {
		return nil, s.pendingErr
	}
	out := make([]domain.RunBatch, 0, len(s.batches))
	for _, batch := range s.batches {
		out = append(out, batch)
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *autoStore) DesigningVersions(ctx context.Context, limit int) ([]DesigningVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.designingErr != nil {
		return nil, s.designingErr
	}
	out := make([]DesigningVersion, 0, len(s.design))
	out = append(out, s.design...)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *autoStore) MarkAutonomyDone(ctx context.Context, batchID int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markDoneCalls++
	if s.markDoneErr != nil {
		return false, s.markDoneErr
	}
	if !s.markDoneResult {
		return false, nil
	}
	for i := range s.batches {
		if s.batches[i].ID == batchID {
			s.batches[i].AutonomyDone = true
		}
	}
	return true, nil
}

func (s *autoStore) MarkSupplementAttempted(ctx context.Context, batchID int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markSuppCalls++
	if s.markSuppErr != nil {
		return false, s.markSuppErr
	}
	if !s.markSuppResult {
		return false, nil
	}
	for i := range s.batches {
		if s.batches[i].ID == batchID && !s.batches[i].SupplementAttempted {
			s.batches[i].SupplementAttempted = true
			return true, nil
		}
	}
	return false, nil
}

func (s *autoStore) SupplementChild(ctx context.Context, parentBatchID int64) (*domain.RunBatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.supplementChild != nil {
		return nil, s.supplementChild
	}
	return s.child[parentBatchID], nil
}

func (s *autoStore) setChild(parentID int64, child *domain.RunBatch) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.child[parentID] = child
}

// autoVersionPort is the execution VersionPort of the worker. It embeds the
// scheduler fake for the methods the worker never calls.
type autoVersionPort struct {
	*schedVersionPort
	continuePhase string
}

func newAutoVersionPort() *autoVersionPort {
	return &autoVersionPort{schedVersionPort: &schedVersionPort{
		status:        versionStatusExecuting,
		systemID:      int64Pointer(3),
		environmentID: int64Pointer(4),
	}}
}

func (v *autoVersionPort) GetVersion(ctx context.Context, versionID int64) (VersionRef, error) {
	return VersionRef{
		ID:            versionID,
		Status:        v.status,
		Paused:        v.paused,
		ContinuePhase: v.continuePhase,
		BatchNo:       v.batchNo,
		SystemID:      v.systemID,
		EnvironmentID: v.environmentID,
	}, nil
}

// autoAnalysis is an in-memory AnalysisPort. GetAnalysis returns nil when no run
// exists, as the delivery adapter does for a not-found latest run.
type autoAnalysis struct {
	views             map[string]*AnalysisView
	getErrs           map[string]error
	startFailureCalls int
	startTriageCalls  int
	getCalls          int
	startFailureErr   error
	startTriageErr    error
	failureConflict   bool
	triageConflict    bool

	lastFailureVersionID int64
	lastFailureBatchID   int64
	lastFailureActor     int64
	lastTriageVersionID  int64
	lastTriageBatchID    int64
	lastTriageActor      int64
}

func (a *autoAnalysis) set(kind, status string, candidates int) {
	view := &AnalysisView{ID: 500, Kind: kind, Status: status, CandidateCount: candidates}
	if kind == AutonomyAnalysisKindIssueTriage {
		view.ID = 501
	}
	a.views[kind] = view
}

// setRun seeds one run with an explicit id and created_at (manual re-analysis
// ordering tests).
func (a *autoAnalysis) setRun(kind string, id int64, status string, candidates int, createdAt time.Time) {
	a.views[kind] = &AnalysisView{
		ID: id, Kind: kind, Status: status, CandidateCount: candidates, CreatedAt: createdAt,
	}
}

func (a *autoAnalysis) StartFailureAnalysis(ctx context.Context, versionID, batchID, actor int64) (int64, error) {
	a.startFailureCalls++
	a.lastFailureVersionID, a.lastFailureBatchID, a.lastFailureActor = versionID, batchID, actor
	if a.failureConflict {
		a.views[AutonomyAnalysisKindFailure] = &AnalysisView{
			ID: 500, Kind: AutonomyAnalysisKindFailure, Status: AutonomyStatusQueued,
		}
		return 500, ErrAutonomyAnalysisActive
	}
	if a.startFailureErr != nil {
		return 0, a.startFailureErr
	}
	a.views[AutonomyAnalysisKindFailure] = &AnalysisView{
		ID: 500, Kind: AutonomyAnalysisKindFailure, Status: AutonomyStatusQueued,
	}
	return 500, nil
}

func (a *autoAnalysis) StartIssueTriage(ctx context.Context, versionID, batchID, actor int64) (int64, error) {
	a.startTriageCalls++
	a.lastTriageVersionID, a.lastTriageBatchID, a.lastTriageActor = versionID, batchID, actor
	if a.triageConflict {
		a.views[AutonomyAnalysisKindIssueTriage] = &AnalysisView{
			ID: 501, Kind: AutonomyAnalysisKindIssueTriage, Status: AutonomyStatusQueued,
		}
		return 501, ErrAutonomyAnalysisActive
	}
	if a.startTriageErr != nil {
		return 0, a.startTriageErr
	}
	a.views[AutonomyAnalysisKindIssueTriage] = &AnalysisView{
		ID: 501, Kind: AutonomyAnalysisKindIssueTriage, Status: AutonomyStatusQueued,
	}
	return 501, nil
}

func (a *autoAnalysis) GetAnalysis(ctx context.Context, batchID int64, kind string) (*AnalysisView, error) {
	a.getCalls++
	if err := a.getErrs[kind]; err != nil {
		return nil, err
	}
	return a.views[kind], nil
}

// autoReports is an in-memory ReportPort.
type autoReports struct {
	latest          *ReportStateView
	ensureCalls     int
	regenerateCalls int
	ensureErr       error
	regenerateErr   error
	// keepStale makes a successful regeneration leave the stored status stale,
	// simulating a fingerprint that did not clear.
	keepStale bool

	lastEnsureVersionID int64
	lastEnsureBatchID   int64
	lastEnsureActor     int64
	lastRegenerateID    int64
	lastRegenerateActor int64
}

func (r *autoReports) EnsureReport(ctx context.Context, versionID, batchID, actor int64) (int64, bool, error) {
	r.ensureCalls++
	r.lastEnsureVersionID, r.lastEnsureBatchID, r.lastEnsureActor = versionID, batchID, actor
	if r.ensureErr != nil {
		return 0, false, r.ensureErr
	}
	r.latest = &ReportStateView{ID: 700, Status: AutonomyReportStatusPublished, Revision: 1}
	return 700, true, nil
}

func (r *autoReports) RegenerateReport(ctx context.Context, versionID, reportID, actor int64) (int64, error) {
	r.regenerateCalls++
	r.lastRegenerateID, r.lastRegenerateActor = reportID, actor
	if r.regenerateErr != nil {
		return 0, r.regenerateErr
	}
	if r.keepStale {
		return reportID, nil
	}
	r.latest = &ReportStateView{ID: 701, Status: AutonomyReportStatusPublished, Revision: 2}
	return 701, nil
}

func (r *autoReports) LatestReport(ctx context.Context, versionID, batchID int64) (*ReportStateView, error) {
	return r.latest, nil
}

// autoSupplements is an in-memory SupplementPort.
type autoSupplements struct {
	run             *SupplementRunView
	startCalls      int
	designCalls     int
	createCalls     int
	markMergedCalls int
	startErr        error
	designErr       error
	createErr       error
	markMergedErr   error
	startConflict   bool
	lastCaseIDs     []int64
	lastParentID    int64
	lastVersionID   int64
	lastActor       int64
}

func (s *autoSupplements) StartSupplement(ctx context.Context, versionID, batchID, actor int64) (int64, error) {
	s.startCalls++
	s.lastVersionID, s.lastParentID, s.lastActor = versionID, batchID, actor
	if s.startConflict {
		s.run = &SupplementRunView{ID: 55, Kind: AutonomyRunKindSupplement, Status: AutonomyStatusQueued}
		return 55, ErrAutonomyRunInProgress
	}
	if s.startErr != nil {
		return 0, s.startErr
	}
	s.run = &SupplementRunView{ID: 55, Kind: AutonomyRunKindSupplement, Status: AutonomyStatusQueued}
	return 55, nil
}

func (s *autoSupplements) StartIncrementalDesign(ctx context.Context, versionID, batchID, actor int64) (int64, error) {
	s.designCalls++
	s.lastVersionID, s.lastParentID, s.lastActor = versionID, batchID, actor
	if s.designErr != nil {
		return 0, s.designErr
	}
	s.run = &SupplementRunView{ID: 56, Kind: AutonomyRunKindIncrementalDesign, Status: AutonomyStatusQueued}
	return 56, nil
}

func (s *autoSupplements) GetSupplementRun(ctx context.Context, batchID int64) (*SupplementRunView, error) {
	return s.run, nil
}

func (s *autoSupplements) CreateSupplementBatch(ctx context.Context, versionID, parentBatchID int64, caseIDs []int64, actor int64) (*domain.RunBatch, error) {
	s.createCalls++
	s.lastVersionID, s.lastParentID, s.lastActor = versionID, parentBatchID, actor
	s.lastCaseIDs = append([]int64(nil), caseIDs...)
	if s.createErr != nil {
		return nil, s.createErr
	}
	child := &domain.RunBatch{
		ID:            78,
		VersionID:     versionID,
		BatchNo:       2,
		Status:        domain.BatchStatusRunning,
		Kind:          domain.BatchKindSupplement,
		ParentBatchID: int64Pointer(parentBatchID),
		CreatedAt:     testNow,
		UpdatedAt:     testNow,
	}
	return child, nil
}

func (s *autoSupplements) MarkNodesMerged(ctx context.Context, versionID int64, caseIDs []int64, actor int64) error {
	s.markMergedCalls++
	return s.markMergedErr
}

// autoDelivery is an in-memory DeliveryPort: it records the requirement-gap
// escalation materialization and can fail it to prove the worker degrades.
type autoDelivery struct {
	calls           int
	err             error
	lastVersionID   int64
	lastBatchID     int64
	lastActor       int64
	lastEscalations []RequirementGapEscalation
}

func (d *autoDelivery) CreateRequirementGapIssues(_ context.Context, versionID, batchID int64,
	escalations []RequirementGapEscalation, actor int64) (int, error) {
	d.calls++
	d.lastVersionID, d.lastBatchID, d.lastActor = versionID, batchID, actor
	d.lastEscalations = append([]RequirementGapEscalation(nil), escalations...)
	if d.err != nil {
		return 0, d.err
	}
	return len(escalations), nil
}

// autoImpact is an in-memory CodeImpactPort.
type autoImpact struct {
	latest     *ImpactView
	getView    *ImpactView
	startCalls int
	getCalls   int
	startErr   error
	getErr     error
	lastBase   string
}

func (i *autoImpact) StartImpact(ctx context.Context, versionID int64, baseCommit string) (int64, error) {
	i.startCalls++
	i.lastBase = baseCommit
	if i.startErr != nil {
		return 0, i.startErr
	}
	i.latest = &ImpactView{ID: 66, Status: AutonomyStatusQueued, BaseCommit: baseCommit, HeadCommit: "head"}
	return 66, nil
}

func (i *autoImpact) GetImpact(ctx context.Context, versionID, analysisID int64) (*ImpactView, error) {
	i.getCalls++
	if i.getErr != nil {
		return nil, i.getErr
	}
	if i.getView != nil {
		return i.getView, nil
	}
	return i.latest, nil
}

func (i *autoImpact) LatestImpact(ctx context.Context, versionID int64) (*ImpactView, error) {
	return i.latest, nil
}

// autoFinalizer is an in-memory VersionFinalizer. The mutex makes the counts
// safe to read from the worker lifecycle tests.
type autoFinalizer struct {
	mu         sync.Mutex
	tryCalls   int
	phaseCalls int
	versionIDs []int64
	result     bool
	err        error
	lastPhase  string
	lastCtxErr error
}

func (f *autoFinalizer) TryFinalize(ctx context.Context, versionID int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tryCalls++
	f.versionIDs = append(f.versionIDs, versionID)
	f.lastCtxErr = ctx.Err()
	return f.result, f.err
}

func (f *autoFinalizer) tryCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tryCalls
}

func (f *autoFinalizer) SetContinuePhase(ctx context.Context, versionID int64, phase string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.phaseCalls++
	f.lastPhase = phase
	return nil
}

// ---------------------------------------------------------------------------
// Batch chain tests
// ---------------------------------------------------------------------------

func TestAutonomyNoFailuresStartsReport(t *testing.T) {
	world := newAutoWorld(t)
	world.analysis.startFailureErr = ErrAutonomyAnalysisNotApplicable

	world.runOnce(t)

	if world.analysis.startFailureCalls != 1 {
		t.Fatalf("StartFailureAnalysis calls = %d, want 1", world.analysis.startFailureCalls)
	}
	if world.reports.ensureCalls != 1 {
		t.Fatalf("EnsureReport calls = %d, want 1", world.reports.ensureCalls)
	}
	if world.analysis.startTriageCalls != 0 {
		t.Fatalf("StartIssueTriage calls = %d, want 0", world.analysis.startTriageCalls)
	}
	if world.analysis.lastFailureVersionID != autoVersionID || world.analysis.lastFailureBatchID != autoBatchID {
		t.Fatalf("failure analysis scope = version %d batch %d",
			world.analysis.lastFailureVersionID, world.analysis.lastFailureBatchID)
	}
	if world.reports.lastEnsureVersionID != autoVersionID || world.reports.lastEnsureBatchID != autoBatchID {
		t.Fatalf("report scope = version %d batch %d",
			world.reports.lastEnsureVersionID, world.reports.lastEnsureBatchID)
	}
	if world.batch().AutonomyDone {
		t.Fatal("the report step must not mark the chain done")
	}
}

func TestAutonomyFailureRunsAnalysisThenTriageThenReport(t *testing.T) {
	world := newAutoWorld(t)

	world.runOnce(t)
	if world.analysis.startFailureCalls != 1 {
		t.Fatalf("StartFailureAnalysis calls = %d, want 1", world.analysis.startFailureCalls)
	}
	if world.reports.ensureCalls != 0 {
		t.Fatal("a queued failure analysis must delay the report")
	}

	world.analysis.set(AutonomyAnalysisKindFailure, AutonomyStatusDone, 2)
	world.runOnce(t)
	if world.analysis.startTriageCalls != 1 {
		t.Fatalf("StartIssueTriage calls = %d, want 1", world.analysis.startTriageCalls)
	}
	if world.reports.ensureCalls != 0 {
		t.Fatal("a queued issue triage must delay the report")
	}

	world.analysis.set(AutonomyAnalysisKindIssueTriage, AutonomyStatusDone, 0)
	world.runOnce(t)
	if world.reports.ensureCalls != 1 {
		t.Fatalf("EnsureReport calls = %d, want 1", world.reports.ensureCalls)
	}
}

func TestAutonomyFailureAnalysisFailureSkipsTriage(t *testing.T) {
	world := newAutoWorld(t)
	world.analysis.set(AutonomyAnalysisKindFailure, AutonomyStatusFailed, 2)

	world.runOnce(t)

	if world.analysis.startTriageCalls != 0 {
		t.Fatalf("StartIssueTriage calls = %d, want 0", world.analysis.startTriageCalls)
	}
	if world.reports.ensureCalls != 1 {
		t.Fatalf("EnsureReport calls = %d, want 1", world.reports.ensureCalls)
	}
}

func TestAutonomyTriageFailureDoesNotDuplicateFallback(t *testing.T) {
	world := newAutoWorld(t)
	world.analysis.set(AutonomyAnalysisKindFailure, AutonomyStatusDone, 2)
	world.analysis.set(AutonomyAnalysisKindIssueTriage, AutonomyStatusFailed, 0)

	// The triage fallback belongs to the delivery service; the worker only
	// continues to the report and never starts triage again.
	world.runOnce(t)
	if world.analysis.startTriageCalls != 0 {
		t.Fatalf("StartIssueTriage calls = %d, want 0", world.analysis.startTriageCalls)
	}
	if world.reports.ensureCalls != 1 {
		t.Fatalf("EnsureReport calls = %d, want 1", world.reports.ensureCalls)
	}

	world.runOnce(t)
	if world.analysis.startTriageCalls != 0 {
		t.Fatalf("a failed triage must not be restarted, calls = %d", world.analysis.startTriageCalls)
	}
	if world.reports.ensureCalls != 1 {
		t.Fatalf("the report was regenerated after a failed triage: calls = %d", world.reports.ensureCalls)
	}
}

func TestAutonomyAnalysisStartDegradesToReport(t *testing.T) {
	world := newAutoWorld(t)
	world.analysis.startFailureErr = errors.New("ai unavailable")

	world.runOnce(t)

	if world.reports.ensureCalls != 1 {
		t.Fatalf("an unavailable analysis start must degrade to the report, EnsureReport calls = %d",
			world.reports.ensureCalls)
	}
}

func TestAutonomyAnalysisConflictUsesExistingRun(t *testing.T) {
	world := newAutoWorld(t)
	world.analysis.failureConflict = true

	world.runOnce(t)
	if world.analysis.startFailureCalls != 1 {
		t.Fatalf("StartFailureAnalysis calls = %d, want 1", world.analysis.startFailureCalls)
	}
	if world.reports.ensureCalls != 0 {
		t.Fatal("an existing active analysis must delay the report")
	}

	world.analysis.set(AutonomyAnalysisKindFailure, AutonomyStatusDone, 1)
	world.runOnce(t)
	if world.analysis.startTriageCalls != 1 {
		t.Fatalf("the existing done analysis must drive triage, calls = %d", world.analysis.startTriageCalls)
	}
}

func TestAutonomyStaleReportRegenerates(t *testing.T) {
	world := newAutoWorld(t)
	world.seedReadyChain()
	world.reports.latest = &ReportStateView{ID: 15, Status: AutonomyReportStatusStale, Revision: 1}

	world.runOnce(t)

	if world.reports.regenerateCalls != 1 {
		t.Fatalf("RegenerateReport calls = %d, want 1", world.reports.regenerateCalls)
	}
	if world.reports.ensureCalls != 0 {
		t.Fatalf("EnsureReport calls = %d, want 0 for a stale report", world.reports.ensureCalls)
	}
	if world.supplements.startCalls != 0 {
		t.Fatal("a stale report must delay the supplement step")
	}
}

func TestAutonomyReportEnsureFailureRetries(t *testing.T) {
	world := newAutoWorld(t)
	world.analysis.startFailureErr = ErrAutonomyAnalysisNotApplicable
	world.reports.ensureErr = errors.New("report store unavailable")

	if err := world.worker.RunOnce(context.Background()); err == nil {
		t.Fatal("a failed report generation must be reported for the next tick")
	}
	if world.batch().AutonomyDone {
		t.Fatal("a failed report generation must not finish the chain")
	}
}

func TestAutonomyBaselineStartsSupplementOnce(t *testing.T) {
	world := newAutoWorld(t)
	world.seedReadyChain()

	world.runOnce(t)
	if world.store.markSuppCalls != 1 {
		t.Fatalf("MarkSupplementAttempted calls = %d, want 1", world.store.markSuppCalls)
	}
	if world.supplements.startCalls != 1 {
		t.Fatalf("StartSupplement calls = %d, want 1", world.supplements.startCalls)
	}
	if world.supplements.run == nil || world.supplements.run.Kind != AutonomyRunKindSupplement {
		t.Fatalf("started run = %+v, want a supplement run", world.supplements.run)
	}
	if world.supplements.lastVersionID != autoVersionID || world.supplements.lastParentID != autoBatchID {
		t.Fatalf("supplement run scope = version %d batch %d",
			world.supplements.lastVersionID, world.supplements.lastParentID)
	}
	if world.batch().AutonomyDone {
		t.Fatal("a queued supplement run must not finish the chain")
	}

	world.runOnce(t)
	if world.supplements.startCalls != 1 {
		t.Fatalf("supplement run started twice: %d", world.supplements.startCalls)
	}
}

func TestAutonomySupplementConflictDoesNotDuplicateRun(t *testing.T) {
	world := newAutoWorld(t)
	world.seedReadyChain()
	world.supplements.startConflict = true

	world.runOnce(t)
	if world.supplements.startCalls != 1 {
		t.Fatalf("StartSupplement calls = %d, want 1", world.supplements.startCalls)
	}
	if world.store.markDoneCalls != 0 {
		t.Fatal("an existing active supplement run must delay the finish")
	}

	world.runOnce(t)
	if world.supplements.startCalls != 1 {
		t.Fatalf("StartSupplement was retried despite the existing run: %d", world.supplements.startCalls)
	}
}

func TestAutonomySupplementStartFailureDegradesAndFinishes(t *testing.T) {
	world := newAutoWorld(t)
	world.seedReadyChain()
	world.supplements.startErr = errors.New("mindmap unavailable")

	world.runOnce(t)

	if world.store.markDoneCalls != 1 {
		t.Fatalf("MarkAutonomyDone calls = %d, want 1", world.store.markDoneCalls)
	}
	if world.finalizer.tryCalls != 1 {
		t.Fatalf("TryFinalize calls = %d, want 1", world.finalizer.tryCalls)
	}
	if world.supplements.createCalls != 0 {
		t.Fatal("a failed supplement start must not create a batch")
	}
}

func TestAutonomySupplementZeroCasesFinishesAndFinalizes(t *testing.T) {
	world := newAutoWorld(t)
	world.seedReadyChain()
	world.mutateBatch(func(batch *domain.RunBatch) { batch.SupplementAttempted = true })
	world.supplements.run = &SupplementRunView{
		ID: 55, Kind: AutonomyRunKindSupplement, Status: AutonomyStatusDone,
	}

	world.runOnce(t)

	if world.store.markDoneCalls != 1 {
		t.Fatalf("MarkAutonomyDone calls = %d, want 1", world.store.markDoneCalls)
	}
	if world.finalizer.tryCalls != 1 {
		t.Fatalf("TryFinalize calls = %d, want 1", world.finalizer.tryCalls)
	}
	if len(world.finalizer.versionIDs) != 1 || world.finalizer.versionIDs[0] != autoVersionID {
		t.Fatalf("finalized versions = %v, want [%d]", world.finalizer.versionIDs, autoVersionID)
	}
	if world.supplements.createCalls != 0 {
		t.Fatal("zero new cases must not create a supplement batch")
	}

	// Reentry: the stored autonomy_done flag stops a second run.
	world.runOnce(t)
	if world.store.markDoneCalls != 1 || world.finalizer.tryCalls != 1 {
		t.Fatalf("reentry repeated the finish: markDone=%d finalize=%d",
			world.store.markDoneCalls, world.finalizer.tryCalls)
	}
}

func TestAutonomySupplementNewCasesCreatesChildBatch(t *testing.T) {
	world := newAutoWorld(t)
	world.seedReadyChain()
	world.mutateBatch(func(batch *domain.RunBatch) { batch.SupplementAttempted = true })
	world.supplements.run = &SupplementRunView{
		ID: 55, Kind: AutonomyRunKindSupplement, Status: AutonomyStatusDone,
		NewCaseIDs: []int64{31, 32},
	}

	world.runOnce(t)

	if world.supplements.createCalls != 1 {
		t.Fatalf("CreateSupplementBatch calls = %d, want 1", world.supplements.createCalls)
	}
	if world.supplements.lastParentID != autoBatchID || world.supplements.lastVersionID != autoVersionID {
		t.Fatalf("supplement batch scope = version %d parent %d",
			world.supplements.lastVersionID, world.supplements.lastParentID)
	}
	if !reflect.DeepEqual(world.supplements.lastCaseIDs, []int64{31, 32}) {
		t.Fatalf("supplement case ids = %v", world.supplements.lastCaseIDs)
	}
	if world.supplements.markMergedCalls != 1 {
		t.Fatalf("MarkNodesMerged calls = %d, want 1", world.supplements.markMergedCalls)
	}
	if world.store.markDoneCalls != 0 {
		t.Fatal("the chain must wait for the supplement batch before finishing")
	}

	world.store.setChild(autoBatchID, &domain.RunBatch{
		ID: 78, VersionID: autoVersionID, Kind: domain.BatchKindSupplement,
		Status: domain.BatchStatusRunning, ParentBatchID: int64Pointer(autoBatchID),
	})
	world.runOnce(t)
	if world.store.markDoneCalls != 1 {
		t.Fatalf("MarkAutonomyDone calls = %d, want 1", world.store.markDoneCalls)
	}
	if world.supplements.createCalls != 1 {
		t.Fatalf("a second supplement batch was created: %d", world.supplements.createCalls)
	}
}

func TestAutonomySupplementExistingChildFinishes(t *testing.T) {
	world := newAutoWorld(t)
	world.seedReadyChain()
	world.mutateBatch(func(batch *domain.RunBatch) { batch.SupplementAttempted = true })
	world.supplements.run = &SupplementRunView{
		ID: 55, Kind: AutonomyRunKindSupplement, Status: AutonomyStatusDone,
		NewCaseIDs: []int64{31},
	}
	world.store.setChild(autoBatchID, &domain.RunBatch{
		ID: 78, Kind: domain.BatchKindSupplement, Status: domain.BatchStatusRunning,
	})

	world.runOnce(t)

	if world.supplements.createCalls != 0 {
		t.Fatalf("CreateSupplementBatch calls = %d, want 0", world.supplements.createCalls)
	}
	if world.store.markDoneCalls != 1 {
		t.Fatalf("MarkAutonomyDone calls = %d, want 1", world.store.markDoneCalls)
	}
}

func TestAutonomySupplementFailureFinishes(t *testing.T) {
	world := newAutoWorld(t)
	world.seedReadyChain()
	world.mutateBatch(func(batch *domain.RunBatch) { batch.SupplementAttempted = true })
	world.supplements.run = &SupplementRunView{
		ID: 55, Kind: AutonomyRunKindSupplement, Status: AutonomyStatusDegraded,
		NewCaseIDs: []int64{31},
	}

	world.runOnce(t)

	if world.supplements.createCalls != 0 {
		t.Fatalf("a degraded supplement run must not create a batch, calls = %d", world.supplements.createCalls)
	}
	if world.store.markDoneCalls != 1 {
		t.Fatalf("MarkAutonomyDone calls = %d, want 1", world.store.markDoneCalls)
	}
}

func TestAutonomySupplementEscalationsCreateIssues(t *testing.T) {
	world := newAutoWorld(t)
	world.seedReadyChain()
	world.mutateBatch(func(batch *domain.RunBatch) {
		batch.SupplementAttempted = true
		batch.CreatedBy = 42
	})
	world.supplements.run = &SupplementRunView{
		ID: 55, Kind: AutonomyRunKindSupplement, Status: AutonomyStatusDone,
		Escalations: []RequirementGapEscalation{{
			Title:          "Missing refund requirement",
			Detail:         "no requirement covers partial refunds",
			RequirementIDs: []int64{21, 22},
		}},
	}

	world.runOnce(t)

	if world.delivery.calls != 1 {
		t.Fatalf("CreateRequirementGapIssues calls = %d, want 1", world.delivery.calls)
	}
	if world.delivery.lastVersionID != autoVersionID || world.delivery.lastBatchID != autoBatchID ||
		world.delivery.lastActor != 42 {
		t.Fatalf("escalation scope = version %d batch %d actor %d, want %d/%d/42",
			world.delivery.lastVersionID, world.delivery.lastBatchID, world.delivery.lastActor,
			autoVersionID, autoBatchID)
	}
	if len(world.delivery.lastEscalations) != 1 ||
		world.delivery.lastEscalations[0].Title != "Missing refund requirement" ||
		len(world.delivery.lastEscalations[0].RequirementIDs) != 2 {
		t.Fatalf("escalations=%+v want the stored requirement gap", world.delivery.lastEscalations)
	}
	if world.store.markDoneCalls != 1 || world.finalizer.tryCalls != 1 {
		t.Fatalf("markDone=%d finalize=%d want the chain settled after materializing",
			world.store.markDoneCalls, world.finalizer.tryCalls)
	}
}

func TestAutonomySupplementEscalationFailureDoesNotWedge(t *testing.T) {
	world := newAutoWorld(t)
	world.seedReadyChain()
	world.mutateBatch(func(batch *domain.RunBatch) { batch.SupplementAttempted = true })
	world.supplements.run = &SupplementRunView{
		ID: 55, Kind: AutonomyRunKindSupplement, Status: AutonomyStatusDone,
		Escalations: []RequirementGapEscalation{{Title: "gap", RequirementIDs: []int64{21}}},
	}
	world.delivery.err = errors.New("delivery store unavailable")

	if err := world.worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("a failed escalation materialization must degrade, not wedge: %v", err)
	}
	if world.delivery.calls != 1 {
		t.Fatalf("CreateRequirementGapIssues calls = %d, want 1", world.delivery.calls)
	}
	if world.store.markDoneCalls != 1 || world.finalizer.tryCalls != 1 {
		t.Fatalf("markDone=%d finalize=%d want the chain settled despite the delivery error",
			world.store.markDoneCalls, world.finalizer.tryCalls)
	}
}

func TestAutonomyIncrementalDesignEscalationsCreateIssuesOnce(t *testing.T) {
	world := newAutoWorld(t)
	parent := world.seedDesigning()
	parent.CreatedBy = 42
	world.supplements.run = &SupplementRunView{
		ID: 56, Kind: AutonomyRunKindIncrementalDesign, Status: AutonomyStatusDone,
		Escalations: []RequirementGapEscalation{{Title: "gap", RequirementIDs: []int64{21}}},
	}

	world.runOnce(t)
	if world.delivery.calls != 1 || world.delivery.lastBatchID != autoParentID ||
		world.delivery.lastActor != 42 {
		t.Fatalf("design escalation calls=%d scope=%d/%d want one call for parent %d by 42",
			world.delivery.calls, world.delivery.lastVersionID, world.delivery.lastBatchID, autoParentID)
	}
	if world.finalizer.tryCalls != 0 || world.supplements.createCalls != 0 {
		t.Fatal("the designing segment must wait for the user's incremental batch")
	}

	// The design run stays terminal while the user confirms the plan; the
	// in-memory marker makes the issue materialization a one-shot.
	world.runOnce(t)
	if world.delivery.calls != 1 {
		t.Fatalf("CreateRequirementGapIssues calls = %d, want the one-shot marker to hold", world.delivery.calls)
	}
}

func TestAutonomyIncrementalSkipsPostSupplement(t *testing.T) {
	world := newAutoWorld(t)
	world.seedReadyChain()
	world.mutateBatch(func(batch *domain.RunBatch) { batch.Kind = domain.BatchKindIncremental })

	world.runOnce(t)

	if world.store.markSuppCalls != 0 {
		t.Fatalf("MarkSupplementAttempted calls = %d, want 0", world.store.markSuppCalls)
	}
	if world.supplements.startCalls != 0 {
		t.Fatalf("StartSupplement calls = %d, want 0", world.supplements.startCalls)
	}
	if world.store.markDoneCalls != 1 {
		t.Fatalf("MarkAutonomyDone calls = %d, want 1", world.store.markDoneCalls)
	}
}

func TestAutonomySupplementKindSkipsPostSupplement(t *testing.T) {
	world := newAutoWorld(t)
	world.seedReadyChain()
	world.mutateBatch(func(batch *domain.RunBatch) { batch.Kind = domain.BatchKindSupplement })

	world.runOnce(t)

	if world.store.markSuppCalls != 0 || world.supplements.startCalls != 0 {
		t.Fatalf("a supplement batch triggered another supplement cycle: attempt=%d start=%d",
			world.store.markSuppCalls, world.supplements.startCalls)
	}
	if world.store.markDoneCalls != 1 {
		t.Fatalf("MarkAutonomyDone calls = %d, want 1", world.store.markDoneCalls)
	}
}

func TestAutonomySkipsPausedBatch(t *testing.T) {
	world := newAutoWorld(t)
	world.mutateBatch(func(batch *domain.RunBatch) { batch.Paused = true })

	world.runOnce(t)

	if world.analysis.startFailureCalls != 0 || world.reports.ensureCalls != 0 ||
		world.store.markDoneCalls != 0 {
		t.Fatal("a paused batch was advanced")
	}
}

func TestAutonomyMarkDoneCAS(t *testing.T) {
	world := newAutoWorld(t)
	world.seedReadyChain()
	world.mutateBatch(func(batch *domain.RunBatch) { batch.SupplementAttempted = true })
	world.store.markDoneResult = false

	world.runOnce(t)

	if world.store.markDoneCalls != 1 {
		t.Fatalf("MarkAutonomyDone calls = %d, want 1", world.store.markDoneCalls)
	}
	if world.finalizer.tryCalls != 0 {
		t.Fatalf("a lost autonomy_done CAS must not finalize, calls = %d", world.finalizer.tryCalls)
	}
}

func TestAutonomySkipsPausedAndCompletedVersions(t *testing.T) {
	t.Run("paused", func(t *testing.T) {
		world := newAutoWorld(t)
		world.versions.paused = true

		world.runOnce(t)

		if world.analysis.startFailureCalls != 0 || world.reports.ensureCalls != 0 ||
			world.store.markDoneCalls != 0 {
			t.Fatalf("paused version was processed: failure=%d report=%d done=%d",
				world.analysis.startFailureCalls, world.reports.ensureCalls, world.store.markDoneCalls)
		}
	})

	t.Run("completed", func(t *testing.T) {
		world := newAutoWorld(t)
		world.versions.status = autoVersionStatusCompleted

		world.runOnce(t)

		if world.analysis.startFailureCalls != 0 || world.store.markDoneCalls != 0 {
			t.Fatal("a completed version was processed")
		}
	})
}

// TestConfiguredAutonomyBatchesPerTickClamps pins spec §16.1: the worker budget
// falls back to the default for an unset/invalid value and is clamped to the
// 1–20 range, so a typo cannot disable the chain or let one tick scan the whole
// database.
func TestConfiguredAutonomyBatchesPerTickClamps(t *testing.T) {
	cases := map[string]int{
		"":     DefaultAutonomyBatchesPerTick,
		"0":    DefaultAutonomyBatchesPerTick,
		"-3":   DefaultAutonomyBatchesPerTick,
		"abc":  DefaultAutonomyBatchesPerTick,
		"1":    1,
		"7":    7,
		"20":   20,
		"21":   20,
		"9999": 20,
	}
	for raw, want := range cases {
		t.Setenv(envAutonomyBatchesPerTick, raw)
		if got := configuredAutonomyBatchesPerTick(); got != want {
			t.Fatalf("configuredAutonomyBatchesPerTick(%q)=%d want %d", raw, got, want)
		}
	}

	t.Setenv(envAutonomyBatchesPerTick, "40")
	worker := NewAutonomyWorker(AutonomyWorkerDeps{})
	if worker.batchesPerTick != 20 {
		t.Fatalf("worker budget=%d want the clamped 20", worker.batchesPerTick)
	}
}

func TestAutonomyProcessesAtMostBatchesPerTick(t *testing.T) {
	world := newAutoWorld(t)
	world.store.mu.Lock()
	world.store.batches = nil
	for i := 0; i < 3; i++ {
		world.store.batches = append(world.store.batches, domain.RunBatch{
			ID: autoBatchID + int64(i), VersionID: autoVersionID, BatchNo: i + 1,
			Status: domain.BatchStatusDone, Kind: domain.BatchKindBaseline,
			CreatedAt: testNow, UpdatedAt: testNow,
		})
	}
	world.store.mu.Unlock()
	world.seedReadyChain()
	world.deps.BatchesPerTick = 2
	world.worker = NewAutonomyWorker(world.deps)

	world.runOnce(t)

	if world.supplements.startCalls != 2 {
		t.Fatalf("supplement starts = %d, want 2 (the per-tick cap)", world.supplements.startCalls)
	}
	if got := world.store.limitCalls; len(got) == 0 || got[0] != 2 {
		t.Fatalf("scan limits = %v, want the per-tick cap", got)
	}
}

func TestAutonomyEmptyChainFromNoFailuresToFinalize(t *testing.T) {
	world := newAutoWorld(t)
	world.seedReadyChain()
	world.mutateBatch(func(batch *domain.RunBatch) { batch.SupplementAttempted = true })
	world.supplements.run = &SupplementRunView{ID: 55, Kind: AutonomyRunKindSupplement, Status: AutonomyStatusDone}

	// The report is the last blocking step; the finish only happens once the
	// previous steps settled, never in the same tick.
	world.reports.latest = nil
	world.runOnce(t)
	if world.reports.ensureCalls != 1 || world.finalizer.tryCalls != 0 {
		t.Fatalf("report step = %d, finalize = %d", world.reports.ensureCalls, world.finalizer.tryCalls)
	}
	world.runOnce(t)
	if world.finalizer.tryCalls != 1 {
		t.Fatalf("finalize calls = %d, want 1", world.finalizer.tryCalls)
	}
}

// ---------------------------------------------------------------------------
// Continuing + designing tests
// ---------------------------------------------------------------------------

func TestAutonomyContinuingDesignRunsImpactThenStartsDesignRun(t *testing.T) {
	world := newAutoWorld(t)
	world.seedDesigning()

	world.runOnce(t)
	if world.impact.startCalls != 1 {
		t.Fatalf("StartImpact calls = %d, want 1", world.impact.startCalls)
	}
	if world.impact.lastBase != autoParentHead {
		t.Fatalf("impact base = %q, want the parent head", world.impact.lastBase)
	}
	if world.supplements.designCalls != 0 {
		t.Fatal("the design run must wait for the impact analysis")
	}

	world.impact.latest = &ImpactView{
		ID: 66, Status: AutonomyStatusDone, BaseCommit: autoParentHead, HeadCommit: "head",
	}
	world.runOnce(t)
	if world.supplements.designCalls != 1 {
		t.Fatalf("StartIncrementalDesign calls = %d, want 1", world.supplements.designCalls)
	}
	if world.supplements.lastVersionID != autoVersionID || world.supplements.lastParentID != autoParentID {
		t.Fatalf("design run scope = version %d parent %d",
			world.supplements.lastVersionID, world.supplements.lastParentID)
	}
	if world.impact.startCalls != 1 {
		t.Fatalf("a done impact analysis was requested again: %d", world.impact.startCalls)
	}

	// The design run is active; the worker only waits and never creates the
	// incremental batch itself.
	world.runOnce(t)
	if world.supplements.createCalls != 0 {
		t.Fatalf("the worker created an incremental batch: %d", world.supplements.createCalls)
	}
	if world.finalizer.tryCalls != 0 {
		t.Fatal("the worker finalized a continuing version without an incremental batch")
	}
	if world.finalizer.phaseCalls != 0 {
		t.Fatalf("the worker changed the continue phase: %d", world.finalizer.phaseCalls)
	}
}

func TestAutonomyContinuingDesignWithoutImpactStartsDesignRun(t *testing.T) {
	world := newAutoWorld(t)
	world.seedDesigning()
	world.deps.Impact = nil
	world.worker = NewAutonomyWorker(world.deps)

	world.runOnce(t)

	if world.supplements.designCalls != 1 {
		t.Fatalf("StartIncrementalDesign calls = %d, want 1", world.supplements.designCalls)
	}
	if world.supplements.createCalls != 0 {
		t.Fatal("the worker created an incremental batch")
	}
}

func TestAutonomyContinuingDesignImpactFailureDegrades(t *testing.T) {
	world := newAutoWorld(t)
	world.seedDesigning()
	world.impact.latest = &ImpactView{
		ID: 66, Status: AutonomyStatusFailed, BaseCommit: autoParentHead, Degraded: true,
	}

	world.runOnce(t)

	if world.impact.startCalls != 0 {
		t.Fatalf("a terminal impact analysis was retried: %d", world.impact.startCalls)
	}
	if world.supplements.designCalls != 1 {
		t.Fatalf("StartIncrementalDesign calls = %d, want 1", world.supplements.designCalls)
	}
}

func TestAutonomyContinuingDesignStartDegrades(t *testing.T) {
	world := newAutoWorld(t)
	world.seedDesigning()
	world.deps.Impact = nil
	world.supplements.designErr = errors.New("ai unavailable")
	world.worker = NewAutonomyWorker(world.deps)

	if err := world.worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("an unavailable design start must degrade, got %v", err)
	}
	if world.supplements.createCalls != 0 || world.finalizer.tryCalls != 0 {
		t.Fatal("a degraded design start must not create a batch or finalize")
	}
}

func TestAutonomyContinuingDesignSkipsNonDesigningPhase(t *testing.T) {
	world := newAutoWorld(t)
	world.seedDesigning()
	world.versions.continuePhase = continuePhaseExecuting

	world.runOnce(t)

	if world.impact.startCalls != 0 || world.supplements.designCalls != 0 {
		t.Fatalf("a non-designing version was processed: impact=%d design=%d",
			world.impact.startCalls, world.supplements.designCalls)
	}
}

func TestAutonomyContinuingDesignExistingRunWaits(t *testing.T) {
	world := newAutoWorld(t)
	world.seedDesigning()
	world.supplements.run = &SupplementRunView{
		ID: 56, Kind: AutonomyRunKindIncrementalDesign, Status: AutonomyStatusQueued,
	}

	world.runOnce(t)

	if world.impact.startCalls != 0 {
		t.Fatalf("the impact analysis started despite an existing design run: %d", world.impact.startCalls)
	}
	if world.supplements.designCalls != 0 {
		t.Fatalf("a second design run was started: %d", world.supplements.designCalls)
	}
}

// ---------------------------------------------------------------------------
// Finalize sweep (I1), actor attribution (I2), read errors (I4) and guards
// ---------------------------------------------------------------------------

func TestAutonomyFinalizeSweepFinalizesSettledChain(t *testing.T) {
	world := newAutoWorld(t)
	world.mutateBatch(func(batch *domain.RunBatch) { batch.AutonomyDone = true })
	world.store.setAwaitingFinalize(autoVersionID)

	world.runOnce(t)

	if world.finalizer.tryCalls != 1 {
		t.Fatalf("TryFinalize calls = %d, want 1 for a settled chain", world.finalizer.tryCalls)
	}
	if len(world.finalizer.versionIDs) != 1 || world.finalizer.versionIDs[0] != autoVersionID {
		t.Fatalf("finalized versions = %v, want [%d]", world.finalizer.versionIDs, autoVersionID)
	}
	if world.store.markDoneCalls != 0 {
		t.Fatalf("an already settled batch was advanced again: markDone=%d", world.store.markDoneCalls)
	}
}

func TestAutonomyFinalizeSweepRetriesAfterError(t *testing.T) {
	world := newAutoWorld(t)
	world.mutateBatch(func(batch *domain.RunBatch) { batch.AutonomyDone = true })
	world.store.setAwaitingFinalize(autoVersionID)
	world.finalizer.err = errors.New("version store unavailable")

	if err := world.worker.RunOnce(context.Background()); err == nil {
		t.Fatal("a failed finalize sweep must be reported for the next tick")
	}
	if world.finalizer.tryCalls != 1 {
		t.Fatalf("TryFinalize calls = %d, want 1", world.finalizer.tryCalls)
	}

	world.finalizer.err = nil
	if err := world.worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if world.finalizer.tryCalls != 2 {
		t.Fatalf("TryFinalize was not retried: calls = %d, want 2", world.finalizer.tryCalls)
	}
}

func TestAutonomyFinalizeSweepIsBounded(t *testing.T) {
	world := newAutoWorld(t)
	world.mutateBatch(func(batch *domain.RunBatch) { batch.AutonomyDone = true })
	world.store.setAwaitingFinalize(autoVersionID+1, autoVersionID+2, autoVersionID+3)
	world.deps.BatchesPerTick = 2
	world.worker = NewAutonomyWorker(world.deps)

	world.runOnce(t)

	if world.finalizer.tryCalls != 2 {
		t.Fatalf("TryFinalize calls = %d, want the per-tick cap 2", world.finalizer.tryCalls)
	}
	if !reflect.DeepEqual(world.finalizer.versionIDs, []int64{autoVersionID + 1, autoVersionID + 2}) {
		t.Fatalf("finalized versions = %v, want the bounded scan order", world.finalizer.versionIDs)
	}
}

func TestAutonomyChainActorUsesBatchCreator(t *testing.T) {
	world := newAutoWorld(t)
	world.mutateBatch(func(batch *domain.RunBatch) { batch.CreatedBy = 42 })

	// Failure analysis start.
	world.runOnce(t)
	if world.analysis.lastFailureActor != 42 {
		t.Fatalf("failure analysis actor = %d, want the batch creator 42", world.analysis.lastFailureActor)
	}

	// Issue-triage start.
	world.analysis.set(AutonomyAnalysisKindFailure, AutonomyStatusDone, 2)
	world.runOnce(t)
	if world.analysis.lastTriageActor != 42 {
		t.Fatalf("issue triage actor = %d, want the batch creator 42", world.analysis.lastTriageActor)
	}

	// Report ensure and stale regeneration.
	world.analysis.set(AutonomyAnalysisKindIssueTriage, AutonomyStatusDone, 0)
	world.runOnce(t)
	if world.reports.lastEnsureActor != 42 {
		t.Fatalf("ensure report actor = %d, want the batch creator 42", world.reports.lastEnsureActor)
	}
	world.reports.latest = &ReportStateView{ID: 15, Status: AutonomyReportStatusStale, Revision: 1}
	world.runOnce(t)
	if world.reports.lastRegenerateActor != 42 {
		t.Fatalf("regenerate report actor = %d, want the batch creator 42", world.reports.lastRegenerateActor)
	}
}

func TestAutonomyChainActorFallsBackToDepsActor(t *testing.T) {
	world := newAutoWorld(t)
	world.mutateBatch(func(batch *domain.RunBatch) { batch.CreatedBy = 0 })
	world.analysis.startFailureErr = ErrAutonomyAnalysisNotApplicable

	world.runOnce(t)

	if world.analysis.lastFailureActor != 7 {
		t.Fatalf("failure analysis actor = %d, want the configured fallback 7", world.analysis.lastFailureActor)
	}
	if world.reports.lastEnsureActor != 7 {
		t.Fatalf("report actor = %d, want the configured fallback 7", world.reports.lastEnsureActor)
	}
}

func TestAutonomySupplementActorUsesBatchCreator(t *testing.T) {
	t.Run("start supplement", func(t *testing.T) {
		world := newAutoWorld(t)
		world.seedReadyChain()
		world.mutateBatch(func(batch *domain.RunBatch) { batch.CreatedBy = 42 })

		world.runOnce(t)

		if world.supplements.startCalls != 1 || world.supplements.lastActor != 42 {
			t.Fatalf("supplement start actor = %d (calls=%d), want 42",
				world.supplements.lastActor, world.supplements.startCalls)
		}
	})

	t.Run("create supplement child", func(t *testing.T) {
		world := newAutoWorld(t)
		world.seedReadyChain()
		world.mutateBatch(func(batch *domain.RunBatch) {
			batch.CreatedBy = 42
			batch.SupplementAttempted = true
		})
		world.supplements.run = &SupplementRunView{
			ID: 55, Kind: AutonomyRunKindSupplement, Status: AutonomyStatusDone,
			NewCaseIDs: []int64{31},
		}

		world.runOnce(t)

		if world.supplements.createCalls != 1 || world.supplements.lastActor != 42 {
			t.Fatalf("supplement child actor = %d (calls=%d), want 42",
				world.supplements.lastActor, world.supplements.createCalls)
		}
	})
}

func TestAutonomyDesignActorUsesParentCreator(t *testing.T) {
	world := newAutoWorld(t)
	parent := world.seedDesigning()
	parent.CreatedBy = 42
	world.deps.Impact = nil
	world.worker = NewAutonomyWorker(world.deps)

	world.runOnce(t)

	if world.supplements.designCalls != 1 || world.supplements.lastActor != 42 {
		t.Fatalf("incremental design actor = %d (calls=%d), want the parent creator 42",
			world.supplements.lastActor, world.supplements.designCalls)
	}
}

func TestAutonomyRetriagesWhenFailureAnalysisIsNewer(t *testing.T) {
	world := newAutoWorld(t)
	// The failure analysis was re-run manually after the stored triage: the
	// snowflake id ordering is the fallback when timestamps are not projected.
	world.analysis.views[AutonomyAnalysisKindFailure] = &AnalysisView{
		ID: 900, Kind: AutonomyAnalysisKindFailure, Status: AutonomyStatusDone, CandidateCount: 2,
	}
	world.analysis.views[AutonomyAnalysisKindIssueTriage] = &AnalysisView{
		ID: 700, Kind: AutonomyAnalysisKindIssueTriage, Status: AutonomyStatusDone,
	}

	world.runOnce(t)

	if world.analysis.startTriageCalls != 1 {
		t.Fatalf("StartIssueTriage calls = %d, want a re-triage for the newer failure analysis",
			world.analysis.startTriageCalls)
	}
	if world.reports.ensureCalls != 0 {
		t.Fatal("the report ran before the re-triage settled")
	}
}

func TestAutonomyRetriageUsesTimestampsWhenIdsDisagree(t *testing.T) {
	t.Run("newer failure by timestamp", func(t *testing.T) {
		world := newAutoWorld(t)
		// The failure run has a lower id but a newer created_at (the adapter's
		// projection is authoritative when timestamps exist).
		world.analysis.setRun(AutonomyAnalysisKindFailure, 400, AutonomyStatusDone, 2, testNow)
		world.analysis.setRun(AutonomyAnalysisKindIssueTriage, 500, AutonomyStatusDone, 0, testNow.Add(-time.Minute))

		world.runOnce(t)

		if world.analysis.startTriageCalls != 1 {
			t.Fatalf("StartIssueTriage calls = %d, want the timestamp-ordered re-triage",
				world.analysis.startTriageCalls)
		}
	})

	t.Run("older failure by timestamp", func(t *testing.T) {
		world := newAutoWorld(t)
		world.analysis.setRun(AutonomyAnalysisKindFailure, 900, AutonomyStatusDone, 2, testNow.Add(-time.Minute))
		world.analysis.setRun(AutonomyAnalysisKindIssueTriage, 500, AutonomyStatusDone, 0, testNow)

		world.runOnce(t)

		if world.analysis.startTriageCalls != 0 {
			t.Fatalf("StartIssueTriage calls = %d, want 0 for a triage newer than the failure run",
				world.analysis.startTriageCalls)
		}
		if world.reports.ensureCalls != 1 {
			t.Fatalf("EnsureReport calls = %d, want 1", world.reports.ensureCalls)
		}
	})
}

func TestAutonomyRetriageWaitsForActiveTriage(t *testing.T) {
	world := newAutoWorld(t)
	world.analysis.setRun(AutonomyAnalysisKindFailure, 900, AutonomyStatusDone, 2, testNow)
	world.analysis.setRun(AutonomyAnalysisKindIssueTriage, 700, AutonomyStatusQueued, 0, testNow.Add(-time.Minute))

	world.runOnce(t)
	if world.analysis.startTriageCalls != 0 {
		t.Fatalf("StartIssueTriage calls = %d, want to await the active triage first",
			world.analysis.startTriageCalls)
	}

	world.analysis.setRun(AutonomyAnalysisKindIssueTriage, 700, AutonomyStatusDone, 0, testNow.Add(-time.Minute))
	world.runOnce(t)
	if world.analysis.startTriageCalls != 1 {
		t.Fatalf("StartIssueTriage calls = %d, want the re-triage after the active run settled",
			world.analysis.startTriageCalls)
	}
}

func TestAutonomyResetAutonomyDone(t *testing.T) {
	world := newAutoWorld(t)
	world.mutateBatch(func(batch *domain.RunBatch) { batch.AutonomyDone = true })

	won, err := world.worker.ResetAutonomyDone(context.Background(), autoBatchID)
	if err != nil {
		t.Fatalf("ResetAutonomyDone: %v", err)
	}
	if !won {
		t.Fatal("ResetAutonomyDone did not report the transition")
	}
	if world.store.resetCalls != 1 {
		t.Fatalf("store ResetAutonomyDone calls = %d, want 1", world.store.resetCalls)
	}
	if world.batch().AutonomyDone {
		t.Fatal("ResetAutonomyDone left the chain settled")
	}

	// A second reset is a lost CAS: the chain is already open.
	won, err = world.worker.ResetAutonomyDone(context.Background(), autoBatchID)
	if err != nil || won {
		t.Fatalf("second ResetAutonomyDone = (%v, %v), want (false, nil)", won, err)
	}

	var nilWorker *AutonomyWorker
	if _, err := nilWorker.ResetAutonomyDone(context.Background(), autoBatchID); err == nil {
		t.Fatal("a nil worker must report the missing store")
	}
}

func TestAutonomySupplementPermanentCreateErrorSettles(t *testing.T) {
	world := newAutoWorld(t)
	world.seedReadyChain()
	world.mutateBatch(func(batch *domain.RunBatch) { batch.SupplementAttempted = true })
	world.supplements.run = &SupplementRunView{
		ID: 55, Kind: AutonomyRunKindSupplement, Status: AutonomyStatusDone,
		NewCaseIDs: []int64{31},
	}
	world.supplements.createErr = ErrAutonomySupplementRejected

	if err := world.worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("a permanent supplement rejection must degrade and settle, got %v", err)
	}
	if world.supplements.createCalls != 1 {
		t.Fatalf("CreateSupplementBatch calls = %d, want 1", world.supplements.createCalls)
	}
	if world.store.markDoneCalls != 1 {
		t.Fatalf("MarkAutonomyDone calls = %d, want 1 instead of an infinite retry",
			world.store.markDoneCalls)
	}
}

func TestAutonomySupplementTransientCreateErrorRetries(t *testing.T) {
	world := newAutoWorld(t)
	world.seedReadyChain()
	world.mutateBatch(func(batch *domain.RunBatch) { batch.SupplementAttempted = true })
	world.supplements.run = &SupplementRunView{
		ID: 55, Kind: AutonomyRunKindSupplement, Status: AutonomyStatusDone,
		NewCaseIDs: []int64{31},
	}
	world.supplements.createErr = errors.New("batch store unavailable")

	if err := world.worker.RunOnce(context.Background()); err == nil {
		t.Fatal("a transient supplement create error must be returned for retry")
	}
	if world.store.markDoneCalls != 0 {
		t.Fatal("a transient supplement create error settled the chain")
	}

	if err := world.worker.RunOnce(context.Background()); err == nil {
		t.Fatal("the transient error was not retried")
	}
	if world.supplements.createCalls != 2 {
		t.Fatalf("CreateSupplementBatch calls = %d, want the retry", world.supplements.createCalls)
	}
}

func TestAutonomyPostCASFinalizeIgnoresCancelledContext(t *testing.T) {
	world := newAutoWorld(t)
	world.seedReadyChain()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	version, err := world.versions.GetVersion(ctx, autoVersionID)
	if err != nil {
		t.Fatalf("GetVersion: %v", err)
	}
	if err := world.worker.finishChain(ctx, world.batch(), version); err != nil {
		t.Fatalf("finishChain: %v", err)
	}
	if world.finalizer.tryCalls != 1 {
		t.Fatalf("TryFinalize calls = %d, want 1", world.finalizer.tryCalls)
	}
	if world.finalizer.lastCtxErr != nil {
		t.Fatalf("the post-CAS finalize inherited the cancelled context: %v", world.finalizer.lastCtxErr)
	}
}
func TestAutonomyAnalysisReadFailureRetries(t *testing.T) {
	t.Run("failure analysis read", func(t *testing.T) {
		world := newAutoWorld(t)
		world.analysis.getErrs[AutonomyAnalysisKindFailure] = errors.New("analysis store unavailable")

		if err := world.worker.RunOnce(context.Background()); err == nil {
			t.Fatal("a failed analysis read must be returned for retry")
		}
		if world.reports.ensureCalls != 0 {
			t.Fatalf("a failed analysis read degraded to the report: ensure=%d", world.reports.ensureCalls)
		}
		if world.batch().AutonomyDone {
			t.Fatal("a failed analysis read settled the chain")
		}
	})

	t.Run("issue triage read", func(t *testing.T) {
		world := newAutoWorld(t)
		world.analysis.set(AutonomyAnalysisKindFailure, AutonomyStatusDone, 1)
		world.analysis.getErrs[AutonomyAnalysisKindIssueTriage] = errors.New("analysis store unavailable")

		if err := world.worker.RunOnce(context.Background()); err == nil {
			t.Fatal("a failed triage read must be returned for retry")
		}
		if world.reports.ensureCalls != 0 {
			t.Fatalf("a failed triage read degraded to the report: ensure=%d", world.reports.ensureCalls)
		}
	})
}

func TestAutonomyStaleReportRegeneratesAtMostOnce(t *testing.T) {
	world := newAutoWorld(t)
	world.seedReadyChain()
	world.reports.latest = &ReportStateView{ID: 15, Status: AutonomyReportStatusStale, Revision: 1}
	world.reports.keepStale = true
	world.mutateBatch(func(batch *domain.RunBatch) { batch.SupplementAttempted = true })
	world.supplements.run = &SupplementRunView{
		ID: 55, Kind: AutonomyRunKindSupplement, Status: AutonomyStatusDone,
	}

	world.runOnce(t)
	if world.reports.regenerateCalls != 1 {
		t.Fatalf("RegenerateReport calls = %d, want 1", world.reports.regenerateCalls)
	}

	// The status did not clear: the marker must stop a second regeneration and
	// let the chain settle instead of looping forever.
	world.runOnce(t)
	if world.reports.regenerateCalls != 1 {
		t.Fatalf("the stale report was regenerated again: calls = %d", world.reports.regenerateCalls)
	}
	if world.store.markDoneCalls != 1 {
		t.Fatalf("MarkAutonomyDone calls = %d, want 1 after the bounded regeneration",
			world.store.markDoneCalls)
	}
}

func TestAutonomyContinuingDesignImpactStartFailures(t *testing.T) {
	cases := []struct {
		name       string
		startErr   error
		wantDesign int
	}{
		{"in progress waits", ErrAutonomyImpactInProgress, 0},
		{"unavailable continues", ErrAutonomyImpactUnavailable, 1},
		{"unknown error degrades", errors.New("codeintel unavailable"), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			world := newAutoWorld(t)
			world.seedDesigning()
			world.impact.startErr = tc.startErr

			world.runOnce(t)

			if world.supplements.designCalls != tc.wantDesign {
				t.Fatalf("StartIncrementalDesign calls = %d, want %d",
					world.supplements.designCalls, tc.wantDesign)
			}
		})
	}
}

func TestAutonomyContinuingDesignImpactGetFailureWaits(t *testing.T) {
	world := newAutoWorld(t)
	world.seedDesigning()
	world.impact.getErr = errors.New("codeintel read failed")

	world.runOnce(t)

	if world.impact.startCalls != 1 || world.impact.getCalls != 1 {
		t.Fatalf("impact calls = start %d get %d, want 1/1", world.impact.startCalls, world.impact.getCalls)
	}
	if world.supplements.designCalls != 0 {
		t.Fatalf("the design run started before the impact analysis was readable: %d",
			world.supplements.designCalls)
	}
}

func TestAutonomyContinuingDesignOtherBaseRetriggersImpact(t *testing.T) {
	world := newAutoWorld(t)
	world.seedDesigning()
	world.impact.latest = &ImpactView{
		ID: 66, Status: AutonomyStatusDone, BaseCommit: "another-base",
	}

	world.runOnce(t)

	if world.impact.startCalls != 1 {
		t.Fatalf("StartImpact calls = %d, want a new analysis for the parent head", world.impact.startCalls)
	}
}

func TestAutonomyContinuingDesignWithoutHeadStartsDesignRun(t *testing.T) {
	world := newAutoWorld(t)
	parent := world.seedDesigning()
	parent.HeadCommit = nil

	world.runOnce(t)

	if world.impact.startCalls != 0 {
		t.Fatalf("an impact analysis started without a parent head: %d", world.impact.startCalls)
	}
	if world.supplements.designCalls != 1 {
		t.Fatalf("StartIncrementalDesign calls = %d, want 1 without analysable code",
			world.supplements.designCalls)
	}
}

func TestAutonomyWorkerStartStopRunsFinalizeSweepOnStartup(t *testing.T) {
	world := newAutoWorld(t)
	world.mutateBatch(func(batch *domain.RunBatch) { batch.AutonomyDone = true })
	world.store.setAwaitingFinalize(autoVersionID)
	world.worker.tick = time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	world.worker.Start(ctx)
	world.worker.Start(ctx) // Starting twice is a documented no-op.

	waitFor(t, "startup finalize sweep", func() bool {
		return world.finalizer.tryCount() >= 1
	})
	world.worker.Stop()
	calls := world.finalizer.tryCount()
	time.Sleep(5 * time.Millisecond)
	if got := world.finalizer.tryCount(); got != calls {
		t.Fatalf("the worker kept ticking after Stop: %d -> %d", calls, got)
	}
	world.worker.Stop() // Stop is idempotent.
}

func TestAutonomyOnBatchTerminalWakesWorker(t *testing.T) {
	world := newAutoWorld(t)
	world.mutateBatch(func(batch *domain.RunBatch) { batch.AutonomyDone = true })
	world.worker.tick = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	world.worker.Start(ctx)
	waitFor(t, "startup pass", func() bool {
		return world.store.pendingCallCount() >= 1
	})

	world.store.setAwaitingFinalize(autoVersionID)
	world.worker.OnBatchTerminal(ctx, domain.RunBatch{ID: autoBatchID})
	waitFor(t, "woken pass", func() bool {
		return world.finalizer.tryCount() >= 1
	})
	world.worker.Stop()
}

func TestAutonomyRunOnceRequiresDeps(t *testing.T) {
	ctx := context.Background()
	if err := (*AutonomyWorker)(nil).RunOnce(ctx); err == nil {
		t.Fatal("a nil worker must report the missing store")
	}

	world := newAutoWorld(t)
	world.deps.Store = nil
	if err := NewAutonomyWorker(world.deps).RunOnce(ctx); err == nil {
		t.Fatal("a worker without a store must report the missing store")
	}

	world.deps.Store = newAutoStore()
	world.deps.Versions = nil
	if err := NewAutonomyWorker(world.deps).RunOnce(ctx); err == nil {
		t.Fatal("a worker without a version port must report the missing version port")
	}

	world.deps.Versions = newAutoVersionPort()
	if err := NewAutonomyWorker(world.deps).RunOnce(nil); err != nil {
		t.Fatalf("a nil context must fall back to the background context: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Compile-time interface guards
// ---------------------------------------------------------------------------

var (
	_ AutonomyTrigger    = (*AutonomyWorker)(nil)
	_ AnalysisPort       = (*autoAnalysis)(nil)
	_ ReportPort         = (*autoReports)(nil)
	_ SupplementPort     = (*autoSupplements)(nil)
	_ CodeImpactPort     = (*autoImpact)(nil)
	_ VersionFinalizer   = (*autoFinalizer)(nil)
	_ AutonomyBatchStore = (*autoStore)(nil)
)
