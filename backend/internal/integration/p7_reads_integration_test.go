//go:build integration

package integration_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	deliverypostgres "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/postgres"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executiondelivery "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/delivery"
	executionpostgres "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// p7SeedVersion inserts one version row with the columns the P7 reads need.
func p7SeedVersion(t *testing.T, pool *pgxpool.Pool, versionID int64, status, phase string, paused bool) {
	t.Helper()
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
		INSERT INTO version (id, name, status, revision, created_by, updated_by, paused, continue_phase)
		VALUES ($1, $2, $3, 1, 1, 1, $4, NULLIF($5, ''))`,
		versionID, "p7-wiring-version", status, paused, phase)
	if err != nil {
		t.Fatalf("seed version %d: %v", versionID, err)
	}
}

// p7SeedBatch inserts one run_batch row with a valid hash pair.
func p7SeedBatch(t *testing.T, pool *pgxpool.Pool, batchID, versionID int64, batchNo int, status, kind string, parent *int64, autonomyDone bool) {
	t.Helper()
	hash := strings.Repeat("a", 64)
	_, err := pool.Exec(context.Background(), `
		INSERT INTO run_batch
			(id, version_id, batch_no, status, baseline_hash, graph_hash, kind,
			 parent_batch_id, autonomy_done, created_by)
		VALUES ($1, $2, $3, $4, $5, $5, $6, $7, $8, 1)`,
		batchID, versionID, batchNo, status, hash, kind, parent, autonomyDone)
	if err != nil {
		t.Fatalf("seed batch %d: %v", batchID, err)
	}
}

// p7SeedCaseRun inserts one case run with its immutable snapshot.
func p7SeedCaseRun(t *testing.T, pool *pgxpool.Pool, caseRunID, batchID, versionID, caseNodeID int64, attempt int, status, failureClass, title string) {
	t.Helper()
	hash := strings.Repeat("b", 64)
	var class any
	if failureClass != "" {
		class = failureClass
	}
	_, err := pool.Exec(context.Background(), `
		INSERT INTO case_run
			(id, batch_id, version_id, case_node_id, case_revision, case_hash,
			 case_snapshot, attempt, status, failure_class, failure_reason, source)
		VALUES ($1, $2, $3, $4, 1, $5, $6::jsonb, $7, $8, $9, $10, 'baseline')`,
		caseRunID, batchID, versionID, caseNodeID, hash,
		`{"node":{"title":"`+title+`"}}`, attempt, status, class, "short reason")
	if err != nil {
		t.Fatalf("seed case run %d: %v", caseRunID, err)
	}
}

// p7SeedStep inserts one step result.
func p7SeedStep(t *testing.T, pool *pgxpool.Pool, stepID, batchID, caseRunID int64, seq int) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO step_result (id, batch_id, case_run_id, seq, action, status, evidence_count)
		VALUES ($1, $2, $3, $4, 'goto /', 'passed', 0)`, stepID, batchID, caseRunID, seq)
	if err != nil {
		t.Fatalf("seed step %d: %v", stepID, err)
	}
}

// p7SeedAssertion inserts one assertion result.
func p7SeedAssertion(t *testing.T, pool *pgxpool.Pool, assertionResultID, batchID, caseRunID, assertionID int64) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO assertion_result
			(id, batch_id, case_run_id, assertion_id, kind, target, expected, actual, verdict)
		VALUES ($1, $2, $3, $4, 'ui', '#ok', 'visible', 'hidden', 'failed')`,
		assertionResultID, batchID, caseRunID, assertionID)
	if err != nil {
		t.Fatalf("seed assertion %d: %v", assertionResultID, err)
	}
}

// p7SeedEvidence inserts one evidence row.
func p7SeedEvidence(t *testing.T, pool *pgxpool.Pool, evidenceID, batchID, caseRunID int64) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO evidence (id, batch_id, case_run_id, kind, path, sha256, size_bytes)
		VALUES ($1, $2, $3, 'screenshot', 'shots/a.png', $4, 10)`,
		evidenceID, batchID, caseRunID, strings.Repeat("c", 64))
	if err != nil {
		t.Fatalf("seed evidence %d: %v", evidenceID, err)
	}
}

// TestP7ExecutionReads covers the execution-side read store/adapters used by the
// delivery report, failure analysis and ref validation: the application filter
// runs in SQL before LIMIT, the previous batch resolves by batch_no, the latest
// attempt per case node is projected with its counts and the flaky rule matches
// the P6 report.
func TestP7ExecutionReads(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	const (
		versionID = int64(701)
		batch1    = int64(7001)
		batch2    = int64(7002)
	)
	p7SeedVersion(t, pool, versionID, "executing", "", false)
	p7SeedBatch(t, pool, batch1, versionID, 1, "done", "baseline", nil, true)
	p7SeedBatch(t, pool, batch2, versionID, 2, "done", "incremental", nil, false)

	// Case 11 failed(application) then passed (flaky); case 12 failed(environment).
	p7SeedCaseRun(t, pool, 801, batch2, versionID, 11, 1, "failed", "application", "Checkout")
	p7SeedCaseRun(t, pool, 802, batch2, versionID, 11, 2, "passed", "", "Checkout")
	p7SeedCaseRun(t, pool, 803, batch2, versionID, 12, 1, "failed", "environment", "Login")
	p7SeedCaseRun(t, pool, 804, batch1, versionID, 11, 1, "passed", "", "Checkout")
	p7SeedStep(t, pool, 901, batch2, 801, 1)
	p7SeedAssertion(t, pool, 902, batch2, 801, 55)
	p7SeedEvidence(t, pool, 903, batch2, 801)
	p7SeedEvidence(t, pool, 904, batch2, 801)
	// The latest attempt (802) carries its own step/assertion/evidence rows: the
	// report counts the projected attempt, not the whole case history.
	p7SeedStep(t, pool, 905, batch2, 802, 1)
	p7SeedAssertion(t, pool, 906, batch2, 802, 56)
	p7SeedEvidence(t, pool, 907, batch2, 802)

	ctx := context.Background()
	reads := executiondelivery.NewReadStore(pool)

	failed, err := executiondelivery.NewAnalysisExecution(reads).
		ListFailedCases(ctx, versionID, batch2, 20)
	if err != nil {
		t.Fatalf("ListFailedCases: %v", err)
	}
	if len(failed) != 1 || failed[0].CaseRunID != 801 || failed[0].CaseNodeID != 11 {
		t.Fatalf("failed cases=%+v want only the application failure 801", failed)
	}
	if failed[0].Title != "Checkout" || failed[0].FailureClass != "application" {
		t.Fatalf("failed case=%+v want title/failure class", failed[0])
	}
	if len(failed[0].EvidenceIDs) != 2 || failed[0].EvidenceIDs[0] != 903 || failed[0].EvidenceIDs[1] != 904 {
		t.Fatalf("evidence ids=%v want [903 904]", failed[0].EvidenceIDs)
	}

	previous, ok, err := reads.PreviousBatchID(ctx, versionID, batch2)
	if err != nil || !ok || previous != batch1 {
		t.Fatalf("previous=%d ok=%v err=%v want %d", previous, ok, err, batch1)
	}
	if _, ok, err := reads.PreviousBatchID(ctx, versionID, batch1); err != nil || ok {
		t.Fatalf("previous of the first batch ok=%v err=%v want false,nil", ok, err)
	}
	if _, _, err := reads.PreviousBatchID(ctx, versionID, 9999); !errors.Is(err, executiondelivery.ErrBatchNotFound) {
		t.Fatalf("previous of a missing batch err=%v want ErrBatchNotFound", err)
	}

	batch, err := reads.GetReportBatch(ctx, versionID, batch2)
	if err != nil {
		t.Fatalf("GetReportBatch: %v", err)
	}
	if batch.BatchNo != 2 || batch.Kind != "incremental" || batch.Status != "done" || batch.VersionID != versionID {
		t.Fatalf("batch=%+v want version/batch_no/kind/status", batch)
	}
	flaky, err := reads.FlakyCaseIDs(ctx, batch2)
	if err != nil {
		t.Fatalf("FlakyCaseIDs: %v", err)
	}
	if len(flaky) != 1 || flaky[0] != 11 {
		t.Fatalf("flaky=%v want [11]", flaky)
	}

	runs, err := reads.LatestReportCaseRuns(ctx, batch2)
	if err != nil {
		t.Fatalf("LatestReportCaseRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("latest runs=%+v want one row per case node", runs)
	}
	if runs[0].CaseNodeID != 11 || runs[0].CaseRunID != 802 || runs[0].Status != "passed" ||
		runs[0].Attempt != 2 || runs[0].StepCount != 1 || runs[0].AssertionCount != 1 ||
		runs[0].EvidenceCount != 1 || runs[0].Title != "Checkout" || runs[0].Source != "baseline" {
		t.Fatalf("latest case 11=%+v want attempt 2 with counts", runs[0])
	}
	if runs[1].CaseNodeID != 12 || runs[1].FailureClass != "environment" {
		t.Fatalf("latest case 12=%+v want the environment failure", runs[1])
	}

	// The supplement agent's recent-result summary must classify the latest
	// attempt per case before filtering failed/skipped: case 11 failed then
	// passed and is therefore not a current failure; only case 12 remains.
	batchResult, err := reads.LatestBatchResult(ctx, versionID, batch2)
	if err != nil {
		t.Fatalf("LatestBatchResult: %v", err)
	}
	if len(batchResult.Cases) != 1 || batchResult.Cases[0].CaseNodeID != 12 ||
		batchResult.Cases[0].Status != "failed" || batchResult.Cases[0].FailureClass != "environment" {
		t.Fatalf("batch result cases=%+v want only the latest failed environment case 12",
			batchResult.Cases)
	}

	detail, found, err := reads.CaseDetail(ctx, versionID, batch2, 801)
	if err != nil || !found {
		t.Fatalf("CaseDetail found=%v err=%v", found, err)
	}
	if detail.Title != "Checkout" || detail.FailureClass != "application" {
		t.Fatalf("detail=%+v want title/failure class", detail)
	}
	steps, err := reads.StepResults(ctx, 801)
	if err != nil || len(steps) != 1 || steps[0].Seq != 1 {
		t.Fatalf("steps=%+v err=%v want one step", steps, err)
	}
	assertions, err := reads.AssertionResults(ctx, 801)
	if err != nil || len(assertions) != 1 || assertions[0].AssertionID != 55 {
		t.Fatalf("assertions=%+v err=%v want assertion 55", assertions, err)
	}
	evidenceIDs, err := reads.CaseEvidenceIDs(ctx, 801)
	if err != nil || len(evidenceIDs) != 2 {
		t.Fatalf("evidence ids=%v err=%v want 2", evidenceIDs, err)
	}
	metas, err := reads.EvidenceMetadata(ctx, versionID, batch2, []int64{903, 904, 999})
	if err != nil {
		t.Fatalf("EvidenceMetadata: %v", err)
	}
	if len(metas) != 2 || metas[0].EvidenceID != 903 || metas[0].Kind != "screenshot" ||
		metas[0].SHA256 != strings.Repeat("c", 64) || metas[0].SizeBytes != 10 {
		t.Fatalf("evidence metas=%+v want the two batch artifacts", metas)
	}
	if _, found, err := reads.CaseDetail(ctx, versionID, batch2, 803); err != nil || found {
		t.Fatalf("environment failure must not be a case detail: found=%v err=%v", found, err)
	}

	owner, found, err := reads.StepResultOwner(ctx, 901)
	if err != nil || !found || owner != 801 {
		t.Fatalf("step owner=%d found=%v err=%v want 801", owner, found, err)
	}
	if _, found, err := reads.StepResultOwner(ctx, 9999); err != nil || found {
		t.Fatalf("missing step found=%v err=%v want false,nil", found, err)
	}

	getBatch, err := reads.GetBatchByID(ctx, batch2)
	if err != nil || getBatch.ID != batch2 || getBatch.VersionID != versionID {
		t.Fatalf("batch by id=%+v err=%v", getBatch, err)
	}
	latest, err := reads.LatestTerminalBatch(ctx, versionID)
	if err != nil || latest.ID != batch2 {
		t.Fatalf("latest terminal=%+v err=%v want %d", latest, err, batch2)
	}
}

// TestP7AutonomyStore covers the durable autonomous-chain store used by the
// execution worker: the pending/designing scans, the two CAS flags, the child
// lookup and the finalize sweep.
func TestP7AutonomyStore(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	const (
		versionID = int64(801)
		batch1    = int64(8001)
		planned   = int64(8002)
		child     = int64(8003)
	)
	p7SeedVersion(t, pool, versionID, "continuing", "designing", false)
	p7SeedBatch(t, pool, batch1, versionID, 1, "done", "baseline", nil, false)
	plannedParent := batch1
	p7SeedBatch(t, pool, planned, versionID, 2, "planned", "supplement", &plannedParent, false)
	childParent := batch1
	p7SeedBatch(t, pool, child, versionID, 3, "done", "supplement", &childParent, true)

	ctx := context.Background()
	store := executionpostgres.NewAutonomyStore(pool)

	pending, err := store.PendingAutonomyBatches(ctx, 10)
	if err != nil {
		t.Fatalf("PendingAutonomyBatches: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != batch1 {
		t.Fatalf("pending=%+v want only the unsettled terminal batch %d", pending, batch1)
	}

	designing, err := store.DesigningVersions(ctx, 10)
	if err != nil {
		t.Fatalf("DesigningVersions: %v", err)
	}
	if len(designing) != 1 {
		t.Fatalf("designing=%+v want one version", designing)
	}
	item := designing[0]
	if item.Version.ID != versionID || item.Version.Status != "continuing" ||
		item.Version.ContinuePhase != "designing" || item.Version.Paused {
		t.Fatalf("designing version=%+v want the continuing/designing version", item.Version)
	}
	if item.Parent == nil || item.Parent.ID != child {
		// The newest terminal batch is the supplement child (batch_no 3).
		t.Fatalf("designing parent=%+v want the newest terminal batch %d", item.Parent, child)
	}

	won, err := store.MarkSupplementAttempted(ctx, batch1)
	if err != nil || !won {
		t.Fatalf("MarkSupplementAttempted=%v/%v want won", won, err)
	}
	if won, err := store.MarkSupplementAttempted(ctx, batch1); err != nil || won {
		t.Fatalf("second MarkSupplementAttempted=%v/%v want a lost CAS", won, err)
	}

	won, err = store.MarkAutonomyDone(ctx, batch1)
	if err != nil || !won {
		t.Fatalf("MarkAutonomyDone=%v/%v want won", won, err)
	}
	if won, err := store.MarkAutonomyDone(ctx, batch1); err != nil || won {
		t.Fatalf("second MarkAutonomyDone=%v/%v want a lost CAS", won, err)
	}
	if won, err := store.ResetAutonomyDone(ctx, batch1); err != nil || !won {
		t.Fatalf("ResetAutonomyDone=%v/%v want won", won, err)
	}
	if won, err := store.ResetAutonomyDone(ctx, batch1); err != nil || won {
		t.Fatalf("second ResetAutonomyDone=%v/%v want a lost CAS", won, err)
	}

	supplementChild, err := store.SupplementChild(ctx, batch1)
	if err != nil {
		t.Fatalf("SupplementChild: %v", err)
	}
	if supplementChild == nil || supplementChild.ID != child {
		t.Fatalf("supplement child=%+v want %d", supplementChild, child)
	}
	if missing, err := store.SupplementChild(ctx, 9999); err != nil || missing != nil {
		t.Fatalf("missing supplement child=%+v/%v want nil,nil", missing, err)
	}

	// The finalize sweep only reports versions with every batch settled and no
	// active batch: the planned batch blocks it until it is cancelled.
	awaiting, err := store.VersionsAwaitingFinalize(ctx, 10)
	if err != nil {
		t.Fatalf("VersionsAwaitingFinalize: %v", err)
	}
	if len(awaiting) != 0 {
		t.Fatalf("awaiting=%v want none while a planned batch exists", awaiting)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE run_batch SET status = 'cancelled', autonomy_done = true WHERE id = $1`, planned); err != nil {
		t.Fatalf("cancel planned batch: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE run_batch SET autonomy_done = true WHERE id = $1`, batch1); err != nil {
		t.Fatalf("settle batch: %v", err)
	}
	awaiting, err = store.VersionsAwaitingFinalize(ctx, 10)
	if err != nil {
		t.Fatalf("VersionsAwaitingFinalize after settle: %v", err)
	}
	if len(awaiting) != 1 || awaiting[0] != versionID {
		t.Fatalf("awaiting=%v want [%d]", awaiting, versionID)
	}

	// A paused version is skipped by both scans.
	if _, err := pool.Exec(ctx,
		`UPDATE version SET paused = true WHERE id = $1`, versionID); err != nil {
		t.Fatalf("pause version: %v", err)
	}
	pending, err = store.PendingAutonomyBatches(ctx, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending of a paused version=%+v/%v want none", pending, err)
	}
	designing, err = store.DesigningVersions(ctx, 10)
	if err != nil || len(designing) != 0 {
		t.Fatalf("designing of a paused version=%+v/%v want none", designing, err)
	}
	awaiting, err = store.VersionsAwaitingFinalize(ctx, 10)
	if err != nil || len(awaiting) != 0 {
		t.Fatalf("awaiting of a paused version=%v/%v want none", awaiting, err)
	}
}

// TestP7ExecutionAnalysisPortMissingRun proves the execution AnalysisPort
// adapter through the real delivery AnalysisService (not a raw repository
// sentinel): LatestByKind answers the delivery domain analysis_not_found error
// and the adapter must project it to nil,nil so the worker proceeds.
func TestP7ExecutionAnalysisPortMissingRun(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	const versionID = int64(901)
	p7SeedVersion(t, pool, versionID, "executing", "", false)
	p7SeedBatch(t, pool, 9001, versionID, 1, "done", "baseline", nil, true)

	service := deliveryapp.NewAnalysisService(deliveryapp.AnalysisDeps{
		Runs: deliverypostgres.NewAnalysisRepository(pool),
	})
	port := executiondelivery.NewAnalysisPort(service)
	view, err := port.GetAnalysis(context.Background(), 9001, executionapp.AutonomyAnalysisKindFailure)
	if err != nil || view != nil {
		t.Fatalf("view=%+v err=%v want nil,nil for a batch without analysis", view, err)
	}
}
