//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	deliverydomain "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	deliveryfilestore "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/filestore"
	deliverypostgres "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/postgres"
	deliveryreport "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/report"
	deliveryversion "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/version"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	executiondelivery "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/delivery"
	executionpostgres "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/postgres"
	executionversion "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/version"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionpostgres "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// p7xSupplementPort is the deterministic mindmap supplement port double of the
// autonomy chain test. The AI-driven supplement run itself cannot be executed
// without a model, so the port models the run lifecycle (queued -> done with
// new cases) while creating the child supplement batch through the real
// execution BatchRepository, which is the durable state the worker reads back.
type p7xSupplementPort struct {
	mu       sync.Mutex
	pool     *pgxpool.Pool
	batches  executionapp.BatchRepository
	ids      *p7xIDs
	runs     map[int64]*executionapp.SupplementRunView
	children map[int64]int64
	starts   int
}

// newP7xSupplementPort returns an empty port.
func newP7xSupplementPort(pool *pgxpool.Pool, batches executionapp.BatchRepository, ids *p7xIDs) *p7xSupplementPort {
	return &p7xSupplementPort{
		pool: pool, batches: batches, ids: ids,
		runs:     map[int64]*executionapp.SupplementRunView{},
		children: map[int64]int64{},
	}
}

// StartSupplement implements execution/application.SupplementPort.
func (p *p7xSupplementPort) StartSupplement(_ context.Context, _, batchID, _ int64) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.starts++
	run := &executionapp.SupplementRunView{
		ID: p.ids.Next(), Kind: executionapp.AutonomyRunKindSupplement,
		Status: executionapp.AutonomyStatusQueued,
	}
	p.runs[batchID] = run
	return run.ID, nil
}

// StartIncrementalDesign implements execution/application.SupplementPort. The
// autonomy chain test does not exercise the P7 continuing-design segment.
func (p *p7xSupplementPort) StartIncrementalDesign(context.Context, int64, int64, int64) (int64, error) {
	return 0, errors.New("p7x: incremental design is not used by this test")
}

// GetSupplementRun implements execution/application.SupplementPort.
func (p *p7xSupplementPort) GetSupplementRun(_ context.Context, batchID int64) (*executionapp.SupplementRunView, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	run, ok := p.runs[batchID]
	if !ok {
		return nil, nil
	}
	copied := *run
	copied.NewCaseIDs = append([]int64(nil), run.NewCaseIDs...)
	return &copied, nil
}

// CreateSupplementBatch implements execution/application.SupplementPort. It
// inserts the child batch through the real repository so SupplementChild (the
// real autonomy store) finds it on the next tick.
func (p *p7xSupplementPort) CreateSupplementBatch(ctx context.Context, versionID, parentBatchID int64, _ []int64, actor int64) (*executiondomain.RunBatch, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var batchNo int
	if err := p.pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(batch_no), 0) + 1 FROM run_batch WHERE version_id = $1`,
		versionID).Scan(&batchNo); err != nil {
		return nil, err
	}
	parent := parentBatchID
	batch := executiondomain.RunBatch{
		ID: p.ids.Next(), VersionID: versionID, BatchNo: batchNo,
		Status: executiondomain.BatchStatusPlanned, Kind: executiondomain.BatchKindSupplement,
		ParentBatchID: &parent, BaselineHash: p7xValidHash('a'), GraphHash: p7xValidHash('a'),
		PauseMode: executiondomain.PauseModeGraceful,
		CreatedBy: actor, CreatedAt: p7xTestTime, UpdatedAt: p7xTestTime,
	}
	created, err := p.batches.CreateBatchWithRuns(ctx, batch, nil)
	if err != nil {
		return nil, err
	}
	p.children[parentBatchID] = created.ID
	return &created, nil
}

// MarkNodesMerged implements execution/application.SupplementPort. The real
// batch service already marks selected nodes; the double only verifies the call
// does not fail the chain.
func (p *p7xSupplementPort) MarkNodesMerged(context.Context, int64, []int64, int64) error {
	return nil
}

// completeRun moves the pending supplement run of batchID to done with the
// supplied new case ids, simulating the agent finishing its execution.
func (p *p7xSupplementPort) completeRun(batchID int64, newCaseIDs []int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	run, ok := p.runs[batchID]
	if !ok {
		run = &executionapp.SupplementRunView{ID: p.ids.Next(), Kind: executionapp.AutonomyRunKindSupplement}
		p.runs[batchID] = run
	}
	run.Status = executionapp.AutonomyStatusDone
	run.NewCaseIDs = append([]int64(nil), newCaseIDs...)
}

// runStatus returns the current supplement run status of batchID.
func (p *p7xSupplementPort) runStatus(batchID int64) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if run, ok := p.runs[batchID]; ok {
		return run.Status
	}
	return ""
}

// startCount returns how many supplement runs were started.
func (p *p7xSupplementPort) startCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.starts
}

// p7xCompleteAnalysisRun advances the latest active run of (batch, kind) to
// done with result through the real analysis repository.
func p7xCompleteAnalysisRun(t *testing.T, runs deliveryapp.AnalysisStore, batchID int64, kind string, result json.RawMessage) {
	t.Helper()
	ctx := context.Background()
	run, err := runs.Latest(ctx, batchID, kind)
	if err != nil {
		t.Fatalf("load latest %s analysis of batch %d: %v", kind, batchID, err)
	}
	finished := time.Now().UTC()
	if _, err := runs.UpdateStatus(ctx, run.ID, run.Status, deliverydomain.AnalysisDone,
		deliveryapp.AnalysisRunPatch{Result: result, FinishedAt: &finished}); err != nil {
		t.Fatalf("complete %s analysis %d: %v", kind, run.ID, err)
	}
}

// p7xAutonomyFixture groups the real services and the single deterministic fake
// port of the autonomous chain test.
type p7xAutonomyFixture struct {
	worker     *executionapp.AutonomyWorker
	versionSvc *versionapp.VersionService
	versionID  int64
	batchID    int64
	caseRunID  int64
	caseNodeID int64
	ids        *p7xIDs
	analysis   *deliveryapp.AnalysisService
	analysisDB deliveryapp.AnalysisStore
	reports    *deliveryapp.ReportService
	batches    executionapp.BatchRepository
	supplement *p7xSupplementPort
}

// newP7xAutonomyFixture builds the real version/analysis/report services over
// the test pool with one terminal baseline batch containing a
// failed(application) case, and wires the real AutonomyWorker. Only the agent
// surfaces (analysis start/completion and the supplement run) are deterministic
// doubles.
func newP7xAutonomyFixture(t *testing.T, pool *pgxpool.Pool) *p7xAutonomyFixture {
	t.Helper()
	ids := p7xNewIDs(930000)
	clock := p7xClock{now: p7xTestTime}

	versionRepo := versionpostgres.NewVersionRepository(pool)
	versionSvc := versionapp.NewVersionService(versionRepo, nil, nil, nil,
		versionapp.WithFinalizeBatchReader(versionRepo))
	version := p7xCreateVersion(t, versionRepo, 5301, versiondomain.VersionExecuting, "")

	batches := executionpostgres.NewBatchRepository(pool)
	finished := p7xTestTime.Add(5 * time.Minute)
	terminal := executiondomain.RunBatch{
		ID: 53011, VersionID: version.ID, BatchNo: 1, Status: executiondomain.BatchStatusDone,
		Kind: executiondomain.BatchKindBaseline, BaselineHash: p7xValidHash('a'),
		GraphHash: p7xValidHash('a'), CreatedBy: 7, CreatedAt: p7xTestTime, UpdatedAt: p7xTestTime,
		FinishedAt: &finished,
	}
	p7xSeedRunBatch(t, batches, terminal, nil)
	p7xSeedFailedApplicationRun(t, pool, 6201, terminal.ID, version.ID, 6301, "Checkout")

	issueSvc := deliveryapp.NewIssueService(deliveryapp.IssueServiceDeps{
		Store:    deliverypostgres.NewIssueRepository(pool),
		Versions: deliveryversion.NewIssueVersionReader(versionSvc),
		IDs:      ids,
		Clock:    clock,
	})
	reads := executiondelivery.NewReadStore(pool)
	analysisDB := deliverypostgres.NewAnalysisRepository(pool)
	analysisSvc := deliveryapp.NewAnalysisService(deliveryapp.AnalysisDeps{
		Runs:      analysisDB,
		Execution: executiondelivery.NewAnalysisExecution(reads),
		Issues:    issueSvc,
		Agents:    p7xAnalysisAgent{ids: ids},
		IDs:       ids,
		Clock:     p7xWallClock{},
	})
	reportFiles, err := deliveryfilestore.New(t.TempDir())
	if err != nil {
		t.Fatalf("report filestore: %v", err)
	}
	reportSvc := deliveryapp.NewReportService(deliveryapp.ReportServiceDeps{
		Versions:  deliveryversion.NewReportVersionReader(versionSvc),
		Execution: executiondelivery.NewReportExecution(reads, nil, nil),
		Graph:     p7xReportGraph{},
		Issues:    deliveryreport.NewIssueSources(deliverypostgres.NewIssueRepository(pool), nil),
		Reports:   deliverypostgres.NewReportRepository(pool),
		Files:     reportFiles,
		IDs:       ids,
		Clock:     clock,
	})
	supplement := newP7xSupplementPort(pool, batches, ids)
	worker := executionapp.NewAutonomyWorker(executionapp.AutonomyWorkerDeps{
		Store:          executionpostgres.NewAutonomyStore(pool),
		Versions:       executionversion.New(versionSvc),
		Analysis:       executiondelivery.NewAnalysisPort(analysisSvc),
		Reports:        executiondelivery.NewReportPort(reportSvc),
		Supplements:    supplement,
		Finalizer:      executionversion.NewFinalizer(versionSvc),
		Actor:          0,
		BatchesPerTick: 10,
	})
	return &p7xAutonomyFixture{
		worker: worker, versionSvc: versionSvc, versionID: version.ID,
		batchID: terminal.ID, caseRunID: 6201, caseNodeID: 6301, ids: ids,
		analysis: analysisSvc, analysisDB: analysisDB, reports: reportSvc,
		batches: batches, supplement: supplement,
	}
}

// newWorker returns a second worker instance over the same durable state,
// simulating a process restart that must resume from the database.
func (f *p7xAutonomyFixture) newWorker(pool *pgxpool.Pool) *executionapp.AutonomyWorker {
	return executionapp.NewAutonomyWorker(executionapp.AutonomyWorkerDeps{
		Store:          executionpostgres.NewAutonomyStore(pool),
		Versions:       executionversion.New(f.versionSvc),
		Analysis:       executiondelivery.NewAnalysisPort(f.analysis),
		Reports:        executiondelivery.NewReportPort(f.reports),
		Supplements:    f.supplement,
		Finalizer:      executionversion.NewFinalizer(f.versionSvc),
		Actor:          0,
		BatchesPerTick: 10,
	})
}

// tick runs one worker pass and fails on an infrastructure error.
func p7xTick(t *testing.T, worker *executionapp.AutonomyWorker) {
	t.Helper()
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("autonomy RunOnce: %v", err)
	}
}

// TestP7AutonomyChainIntegration drives the full baseline chain against
// PostgreSQL with deterministic agent ports:
//
//	terminal baseline -> failure analysis -> triage -> report -> supplement
//	-> child supplement batch -> autonomy_done -> finalize
//
// plus ResetAutonomyDone re-entry, a process restart mid-chain and the
// idempotent settled state.
func TestP7AutonomyChainIntegration(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	fixture := newP7xAutonomyFixture(t, pool)
	versionRepo := versionpostgres.NewVersionRepository(pool)
	store := executionpostgres.NewAutonomyStore(pool)

	// 1. First tick starts the failure analysis of the terminal batch.
	p7xTick(t, fixture.worker)
	failure, err := fixture.analysisDB.Latest(ctx, fixture.batchID, deliverydomain.AnalysisKindFailure)
	if err != nil {
		t.Fatalf("load started failure analysis: %v", err)
	}
	if failure.Status != deliverydomain.AnalysisRunning {
		t.Fatalf("failure analysis status = %q, want running", failure.Status)
	}

	// 2. Restart: a fresh worker reads the stored running run and does not start
	// a duplicate analysis.
	restarted := fixture.newWorker(pool)
	p7xTick(t, restarted)
	var failureCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM analysis_run WHERE batch_id = $1 AND kind = 'failure'`,
		fixture.batchID).Scan(&failureCount); err != nil {
		t.Fatalf("count failure analyses: %v", err)
	}
	if failureCount != 1 {
		t.Fatalf("failure analyses after restart = %d, want 1", failureCount)
	}

	// 3. Completing the failure run with one candidate starts issue triage.
	p7xCompleteAnalysisRun(t, fixture.analysisDB, fixture.batchID,
		deliverydomain.AnalysisKindFailure, p7xAnalysisJSONResult(t, fixture.caseRunID, fixture.caseNodeID))
	p7xTick(t, restarted)
	triage, err := fixture.analysisDB.Latest(ctx, fixture.batchID, deliverydomain.AnalysisKindIssueTriage)
	if err != nil {
		t.Fatalf("load started triage: %v", err)
	}
	if triage.Status != deliverydomain.AnalysisRunning {
		t.Fatalf("triage status = %q, want running", triage.Status)
	}

	// 4. Completing triage drives report generation on the next tick.
	p7xCompleteAnalysisRun(t, fixture.analysisDB, fixture.batchID,
		deliverydomain.AnalysisKindIssueTriage, json.RawMessage(`{"issues":[]}`))
	p7xTick(t, restarted)
	var reportID int64
	var reportStatus string
	if err := pool.QueryRow(ctx,
		`SELECT id, status FROM report WHERE batch_id = $1 ORDER BY revision DESC LIMIT 1`,
		fixture.batchID).Scan(&reportID, &reportStatus); err != nil {
		t.Fatalf("read generated report: %v", err)
	}
	if reportStatus != deliverydomain.ReportStatusPublished {
		t.Fatalf("report status = %q, want published", reportStatus)
	}

	// 5. The next tick starts the post-batch supplement exactly once.
	p7xTick(t, restarted)
	if status := fixture.supplement.runStatus(fixture.batchID); status != executionapp.AutonomyStatusQueued {
		t.Fatalf("supplement run status = %q, want queued", status)
	}
	if starts := fixture.supplement.startCount(); starts != 1 {
		t.Fatalf("supplement starts = %d, want 1", starts)
	}
	var supplementAttempted bool
	if err := pool.QueryRow(ctx,
		`SELECT supplement_attempted FROM run_batch WHERE id = $1`, fixture.batchID).Scan(&supplementAttempted); err != nil {
		t.Fatalf("read supplement_attempted: %v", err)
	}
	if !supplementAttempted {
		t.Fatal("supplement_attempted was not persisted")
	}

	// 6. An active supplement run is awaited, then its new cases create the
	// child supplement batch through the real batch repository.
	p7xTick(t, restarted)
	fixture.supplement.completeRun(fixture.batchID, []int64{fixture.caseNodeID})
	p7xTick(t, restarted)
	var childID int64
	var childParent int64
	var childKind, childStatus string
	if err := pool.QueryRow(ctx,
		`SELECT id, parent_batch_id, kind, status FROM run_batch WHERE parent_batch_id = $1`,
		fixture.batchID).Scan(&childID, &childParent, &childKind, &childStatus); err != nil {
		t.Fatalf("read supplement child: %v", err)
	}
	if childParent != fixture.batchID || childKind != string(executiondomain.BatchKindSupplement) ||
		childStatus != string(executiondomain.BatchStatusPlanned) {
		t.Fatalf("child batch = %d parent %d kind %s status %s", childID, childParent, childKind, childStatus)
	}

	// 7. The parent chain settles (autonomy_done) but the active child blocks the
	// version finalize.
	p7xTick(t, restarted)
	parentSettled, err := store.MarkAutonomyDone(ctx, fixture.batchID)
	if err != nil {
		t.Fatalf("verify parent autonomy_done: %v", err)
	}
	if parentSettled {
		t.Fatal("the worker did not settle the parent autonomy chain")
	}
	version, err := versionRepo.Get(ctx, fixture.versionID)
	if err != nil {
		t.Fatalf("get version after parent settle: %v", err)
	}
	if version.Status != versiondomain.VersionExecuting {
		t.Fatalf("version status = %q, want executing while the child runs", version.Status)
	}

	// 8. Reset reopens the settled chain; a newer failure run re-enters triage,
	// then the chain settles again without duplicating the supplement child.
	if won, err := restarted.ResetAutonomyDone(ctx, fixture.batchID); err != nil || !won {
		t.Fatalf("ResetAutonomyDone = %v/%v, want won", won, err)
	}
	newFailure := deliverydomain.AnalysisRun{
		ID: fixture.ids.Next(), VersionID: fixture.versionID,
		BatchID: fixture.batchID, Kind: deliverydomain.AnalysisKindFailure,
		Status: deliverydomain.AnalysisDone, Result: p7xAnalysisJSONResult(t, fixture.caseRunID, fixture.caseNodeID),
		CreatedBy: 7, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if _, err := fixture.analysisDB.Create(ctx, newFailure); err != nil {
		t.Fatalf("create newer failure analysis: %v", err)
	}
	p7xTick(t, restarted)
	var triageCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM analysis_run WHERE batch_id = $1 AND kind = 'issue_triage'`,
		fixture.batchID).Scan(&triageCount); err != nil {
		t.Fatalf("count triage runs: %v", err)
	}
	if triageCount != 2 {
		t.Fatalf("triage runs after reset = %d, want 2", triageCount)
	}
	p7xCompleteAnalysisRun(t, fixture.analysisDB, fixture.batchID,
		deliverydomain.AnalysisKindIssueTriage, json.RawMessage(`{"issues":[]}`))
	p7xTick(t, restarted)
	parentSettled, err = store.MarkAutonomyDone(ctx, fixture.batchID)
	if err != nil {
		t.Fatalf("verify re-settled parent: %v", err)
	}
	if parentSettled {
		t.Fatal("the reset chain did not settle again")
	}
	// The supplement cycle is not repeated.
	if starts := fixture.supplement.startCount(); starts != 1 {
		t.Fatalf("supplement starts after reset = %d, want 1", starts)
	}

	// 9. The child supplement batch advances through its own (report-only) chain.
	p7xMarkBatchDone(t, fixture.batches, childID, executiondomain.BatchStatusPlanned)
	p7xTick(t, restarted)
	var childReport int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM report WHERE batch_id = $1`, childID).Scan(&childReport); err != nil {
		t.Fatalf("count child reports: %v", err)
	}
	if childReport != 1 {
		t.Fatalf("child reports = %d, want 1", childReport)
	}
	p7xTick(t, restarted)

	// 10. Every batch is settled: the worker finalized the version.
	version, err = versionRepo.Get(ctx, fixture.versionID)
	if err != nil {
		t.Fatalf("get finalized version: %v", err)
	}
	if version.Status != versiondomain.VersionCompleted {
		t.Fatalf("version status = %q, want completed", version.Status)
	}
	if won, err := store.MarkAutonomyDone(ctx, childID); err != nil || won {
		t.Fatalf("child autonomy_done = %v/%v, want already settled", won, err)
	}

	// 11. A further tick is idempotent: no new analysis/report/batch/child.
	var before struct{ analyses, reports, batches int }
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM analysis_run WHERE batch_id IN ($1, $2)),
			(SELECT count(*) FROM report WHERE version_id = $3),
			(SELECT count(*) FROM run_batch WHERE version_id = $3)`,
		fixture.batchID, childID, fixture.versionID).
		Scan(&before.analyses, &before.reports, &before.batches); err != nil {
		t.Fatalf("count settled state: %v", err)
	}
	p7xTick(t, restarted)
	var after struct{ analyses, reports, batches int }
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM analysis_run WHERE batch_id IN ($1, $2)),
			(SELECT count(*) FROM report WHERE version_id = $3),
			(SELECT count(*) FROM run_batch WHERE version_id = $3)`,
		fixture.batchID, childID, fixture.versionID).
		Scan(&after.analyses, &after.reports, &after.batches); err != nil {
		t.Fatalf("count settled state after tick: %v", err)
	}
	if before != after {
		t.Fatalf("settled state changed (%+v -> %+v), want idempotent", before, after)
	}
}

// TestP7AnalysisRestartSweepIntegration covers the boot-time stale-analysis
// recovery over real PostgreSQL: an active run left by a previous process is
// failed with stale_run_recovered, the single-flight slot is released and the
// partial unique index rejects a second active run while the first is alive.
func TestP7AnalysisRestartSweepIntegration(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	ids := p7xNewIDs(940000)
	versionRepo := versionpostgres.NewVersionRepository(pool)
	version := p7xCreateVersion(t, versionRepo, 5401, versiondomain.VersionExecuting, "")
	batches := executionpostgres.NewBatchRepository(pool)
	terminal := executiondomain.RunBatch{
		ID: 54011, VersionID: version.ID, BatchNo: 1, Status: executiondomain.BatchStatusDone,
		Kind: executiondomain.BatchKindBaseline, BaselineHash: p7xValidHash('a'),
		GraphHash: p7xValidHash('a'), CreatedBy: 7, CreatedAt: p7xTestTime, UpdatedAt: p7xTestTime,
	}
	p7xSeedRunBatch(t, batches, terminal, nil)
	p7xSeedFailedApplicationRun(t, pool, 6401, terminal.ID, version.ID, 6501, "Login")

	store := deliverypostgres.NewAnalysisRepository(pool)
	service := deliveryapp.NewAnalysisService(deliveryapp.AnalysisDeps{
		Runs:      store,
		Execution: executiondelivery.NewAnalysisExecution(executiondelivery.NewReadStore(pool)),
		Issues: deliveryapp.NewIssueService(deliveryapp.IssueServiceDeps{
			Store:    deliverypostgres.NewIssueRepository(pool),
			Versions: deliveryversion.NewIssueVersionReader(versionRepo),
			IDs:      ids,
		}),
		Agents: p7xAnalysisAgent{ids: ids},
		IDs:    ids,
		Clock:  p7xClock{now: p7xTestTime},
	})
	// A manual retry produces a queued->running run; simulating a crash, no
	// worker polls it and the boot recovery must fail it.
	started, err := service.StartFailure(ctx, version.ID, terminal.ID, 7)
	if err != nil {
		t.Fatalf("start failure analysis: %v", err)
	}
	if started.Status != deliverydomain.AnalysisRunning {
		t.Fatalf("started run status = %q, want running", started.Status)
	}
	// The real unique index keeps one active run per (batch, kind).
	second := deliverydomain.AnalysisRun{
		ID: ids.Next(), VersionID: version.ID, BatchID: terminal.ID,
		Kind: deliverydomain.AnalysisKindFailure, Status: deliverydomain.AnalysisQueued, CreatedBy: 7,
		CreatedAt: p7xTestTime, UpdatedAt: p7xTestTime,
	}
	if _, err := store.Create(ctx, second); err == nil {
		t.Fatal("the unique active analysis index accepted a second active run")
	} else {
		var domainErr *deliverydomain.Error
		if !errors.As(err, &domainErr) || domainErr.Reason != deliverydomain.ReasonAnalysisActive {
			t.Fatalf("second active analysis create error = %v, want analysis_active", err)
		}
	}
	recovered, err := service.RecoverStale(ctx)
	if err != nil || recovered != 1 {
		t.Fatalf("RecoverStale = %d/%v, want 1/nil", recovered, err)
	}
	recoveredRun, err := store.GetByID(ctx, started.ID)
	if err != nil {
		t.Fatalf("read recovered run: %v", err)
	}
	if recoveredRun.Status != deliverydomain.AnalysisFailed || recoveredRun.FinishedAt == nil {
		t.Fatalf("recovered run = %+v, want failed with a finish time", recoveredRun)
	}
	var failures map[string]any
	if err := json.Unmarshal(recoveredRun.Errors, &failures); err != nil ||
		failures["reason"] != deliverydomain.ReasonStaleRunRecovered {
		t.Fatalf("recovered errors = %s (%v), want %s", recoveredRun.Errors, err, deliverydomain.ReasonStaleRunRecovered)
	}
	// The active slot is free again: the retry can start a fresh run.
	retried, err := service.StartFailure(ctx, version.ID, terminal.ID, 7)
	if err != nil {
		t.Fatalf("retry after recovery: %v", err)
	}
	if retried.ID == started.ID {
		t.Fatalf("retry reused the recovered run %d", retried.ID)
	}
}
