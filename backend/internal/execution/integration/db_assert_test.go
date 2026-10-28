//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/application/agent_tool"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/filestore"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/postgres"
	executionresource "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/resource"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	resourcedomain "github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure/pgreadonly"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// integrationClock is a fixed application.Clock.
type integrationClock struct{}

func (integrationClock) Now() time.Time { return executionTestTime }

// integrationIDs mints sequential ids for the recorder/evidence services.
type integrationIDs struct{ next int64 }

func (g *integrationIDs) Next() int64 {
	g.next++
	return g.next
}

// integrationVersions is the version port slice used by db_assert: it only
// answers GetVersion.
type integrationVersions struct {
	environmentID *int64
}

func (v integrationVersions) GetVersion(context.Context, int64) (application.VersionRef, error) {
	return application.VersionRef{ID: executionTestVersion, Status: "executing", EnvironmentID: v.environmentID}, nil
}

func (integrationVersions) MarkPaused(context.Context, int64, bool, string, string, int64) error {
	return nil
}

func (integrationVersions) AllocateBatchNo(context.Context, int64) (int, error) { return 0, nil }

func (integrationVersions) ReleaseBatchNo(context.Context, int64, int) error { return nil }

func (integrationVersions) SetBatchNo(context.Context, int64, int, int64) error { return nil }

func (integrationVersions) CompleteExecution(context.Context, int64, int64) error { return nil }

// integrationDBQuery is the DBQueryPort double of the integration test.
type integrationDBQuery struct {
	result application.DBQueryResult
	err    error
	calls  int
}

func (q *integrationDBQuery) Target(context.Context, int64) (application.DBTargetRef, error) {
	return application.DBTargetRef{
		EnvironmentID: 44, Name: "orders", MaxRows: 100, StatementTimeoutMS: 5000,
	}, nil
}

func (q *integrationDBQuery) Query(context.Context, int64, string, application.DBQueryLimits) (application.DBQueryResult, error) {
	q.calls++
	if q.err != nil {
		return application.DBQueryResult{}, q.err
	}
	return q.result, nil
}

// dbAssertBatch builds a running P7 batch; the shared executionBatch fixture
// predates the graph_hash NOT NULL constraint.
func dbAssertBatch(id int64, batchNo int) domain.RunBatch {
	return domain.RunBatch{
		ID: id, VersionID: executionTestVersion, BatchNo: batchNo,
		Status: domain.BatchStatusRunning, Kind: domain.BatchKindBaseline,
		BaselineHash: strings.Repeat("b", 64), GraphHash: strings.Repeat("a", 64),
		PauseMode: domain.PauseModeGraceful, CreatedBy: executionTestActor,
		CreatedAt: executionTestTime, UpdatedAt: executionTestTime,
	}
}

// dbAssertionRun builds a pending case run whose only assertion is a db one.
func dbAssertionRun(id, batchID, caseNodeID int64) domain.CaseRun {
	return dbAssertionRunWith(id, batchID, caseNodeID,
		"SELECT count(*) FROM orders", `[[1]]`)
}

// dbAssertionRunWith builds a pending db-only case run with the supplied
// server-side SQL and expected value.
func dbAssertionRunWith(id, batchID, caseNodeID int64, expression, expected string) domain.CaseRun {
	snapshot := domain.CaseSnapshot{
		Node: domain.CaseSnapshotNode{ID: caseNodeID, Title: "db case", Priority: "P0"},
		Steps: []domain.CaseSnapshotStep{
			{Seq: 1, Action: "query orders", Expected: "rows"},
		},
		Assertions: []domain.CaseSnapshotAssertion{{
			ID: id + 10000, StepSeq: 1, Kind: "db", Target: "orders",
			Expression: expression, Expected: expected,
		}},
		GraphHash:  strings.Repeat("a", 64),
		CapturedAt: executionTestTime,
	}
	return domain.CaseRun{
		ID: id, BatchID: batchID, VersionID: executionTestVersion, CaseNodeID: caseNodeID,
		CaseRevision: 1, CaseHash: domain.CanonicalCaseHash(snapshot), Snapshot: snapshot,
		Attempt: 1, Status: domain.CaseRunStatusPending,
		CreatedAt: executionTestTime, UpdatedAt: executionTestTime,
	}
}

// TestExecutionDBAssertIntegrationPassed proves the real persistence contract:
// the db_result evidence row/file (allowed by the P7 CHECK), the db
// assertion_result row, the evidence ref and the passed finish all commit
// against PostgreSQL, and the db evidence satisfies the finish evidence gate
// with zero step evidence.
func TestExecutionDBAssertIntegrationPassed(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	batches := postgres.NewBatchRepository(pool)
	caseRuns := postgres.NewCaseRunRepository(pool)
	steps := postgres.NewStepRepository(pool)
	assertions := postgres.NewAssertionRepository(pool)
	evidenceRepo := postgres.NewEvidenceRepository(pool)
	refs := postgres.NewEvidenceRefRepository(pool)
	queries := postgres.NewExecutionQueries(pool)

	store, err := filestore.New(t.TempDir())
	if err != nil {
		t.Fatalf("filestore.New: %v", err)
	}
	ids := &integrationIDs{}
	evidenceService := application.NewEvidenceService(evidenceRepo, refs, batches, store, ids)
	environmentID := int64(44)
	query := &integrationDBQuery{result: application.DBQueryResult{
		Columns: []string{"count"}, Rows: [][]string{{"1"}}, RowCount: 1, ElapsedMS: 2,
	}}
	recorder := application.NewRecorder(application.RecorderDeps{
		Locator:    queries,
		CaseRuns:   caseRuns,
		Steps:      steps,
		Assertions: assertions,
		Evidence:   evidenceRepo,
		Refs:       refs,
		Batches:    batches,
		Versions:   integrationVersions{environmentID: &environmentID},
		DBQuery:    query,
		DBCapture:  evidenceService,
		Clock:      integrationClock{},
		IDs:        ids,
	})

	run := dbAssertionRun(9801, 9811, 701)
	created := createExecutionBatch(t, batches, dbAssertBatch(9811, 1), []domain.CaseRun{run})
	if _, err := caseRuns.SetStatus(ctx, run.ID, domain.CaseRunStatusPending, domain.CaseRunStatusRunning, executionTestTime); err != nil {
		t.Fatalf("start case run: %v", err)
	}
	if _, err := recorder.RecordStep(ctx, run.ID, application.StepRecordInput{
		Seq: 1, Status: domain.StepStatusPassed, Actual: "done",
	}); err != nil {
		t.Fatalf("RecordStep: %v", err)
	}

	outcome, err := recorder.DBAssert(ctx, run.ID, run.Snapshot.Assertions[0].ID, application.DBAssertLimits{})
	if err != nil {
		t.Fatalf("DBAssert: %v", err)
	}
	if outcome.Verdict != "passed" {
		t.Fatalf("outcome = %+v", outcome)
	}

	results, err := assertions.ListByCaseRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListByCaseRun assertions: %v", err)
	}
	if len(results) != 1 || results[0].Kind != "db" || results[0].Verdict != "passed" ||
		results[0].Target != "orders" || results[0].FailureReason != "" {
		t.Fatalf("assertion results = %+v", results)
	}
	// The results tab reads the persisted actual preview, so it must carry the
	// elapsed time (T20 review Important #1), not just the db_result evidence.
	var actualPreview struct {
		RowCount  int    `json:"rowCount"`
		Truncated bool   `json:"truncated"`
		ElapsedMS *int64 `json:"elapsedMs"`
	}
	if err := json.Unmarshal([]byte(results[0].Actual), &actualPreview); err != nil {
		t.Fatalf("persisted db actual preview is not JSON: %v (%q)", err, results[0].Actual)
	}
	if actualPreview.RowCount != 1 || actualPreview.Truncated ||
		actualPreview.ElapsedMS == nil || *actualPreview.ElapsedMS != 2 {
		t.Fatalf("persisted db actual preview = %+v (%q), want rowCount 1, not truncated and elapsedMs 2",
			actualPreview, results[0].Actual)
	}
	evidenceRows, err := evidenceRepo.ListByCaseRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListByCaseRun evidence: %v", err)
	}
	if len(evidenceRows) != 1 || evidenceRows[0].Kind != "db_result" ||
		evidenceRows[0].Mime != "application/json" {
		t.Fatalf("evidence rows = %+v", evidenceRows)
	}
	reader, stored, err := evidenceService.Open(ctx, executionTestVersion, evidenceRows[0].ID)
	if err != nil {
		t.Fatalf("Open db evidence: %v", err)
	}
	defer reader.Close()
	var document struct {
		SQL       string     `json:"sql"`
		Columns   []string   `json:"columns"`
		Rows      [][]string `json:"rows"`
		RowCount  int        `json:"row_count"`
		Truncated bool       `json:"truncated"`
		ElapsedMS int64      `json:"elapsed_ms"`
	}
	if err := json.NewDecoder(reader).Decode(&document); err != nil {
		t.Fatalf("decode db evidence: %v", err)
	}
	if stored.SHA256 == "" || document.SQL != "SELECT count(*) FROM orders" ||
		document.RowCount != 1 || len(document.Rows) != 1 || document.Rows[0][0] != "1" {
		t.Fatalf("db evidence document = %+v (stored %+v)", document, stored)
	}

	finished, err := recorder.FinishCase(ctx, run.ID, application.FinishInput{
		Decision: domain.FinishDecision{Verdict: domain.VerdictPassed},
	})
	if err != nil {
		t.Fatalf("FinishCase passed with db evidence: %v", err)
	}
	if finished.Status != domain.CaseRunStatusPassed {
		t.Fatalf("status = %s, want passed", finished.Status)
	}
	if created.ID != run.BatchID {
		t.Fatalf("batch = %+v", created)
	}
}

// TestExecutionDBAssertIntegrationConnectionFailure proves the error path
// against PostgreSQL: the not_run result is persisted with the stable reason and
// the blocked(environment) finish is accepted (T8 review fix 2).
func TestExecutionDBAssertIntegrationConnectionFailure(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	batches := postgres.NewBatchRepository(pool)
	caseRuns := postgres.NewCaseRunRepository(pool)
	steps := postgres.NewStepRepository(pool)
	assertions := postgres.NewAssertionRepository(pool)
	evidenceRepo := postgres.NewEvidenceRepository(pool)
	refs := postgres.NewEvidenceRefRepository(pool)
	queries := postgres.NewExecutionQueries(pool)
	store, err := filestore.New(t.TempDir())
	if err != nil {
		t.Fatalf("filestore.New: %v", err)
	}
	ids := &integrationIDs{}
	environmentID := int64(44)
	query := &integrationDBQuery{err: &application.DBQueryError{
		Reason: application.DBReasonConnectionFailed, Message: "dial tcp: connection refused",
	}}
	recorder := application.NewRecorder(application.RecorderDeps{
		Locator:    queries,
		CaseRuns:   caseRuns,
		Steps:      steps,
		Assertions: assertions,
		Evidence:   evidenceRepo,
		Refs:       refs,
		Batches:    batches,
		Versions:   integrationVersions{environmentID: &environmentID},
		DBQuery:    query,
		DBCapture:  application.NewEvidenceService(evidenceRepo, refs, batches, store, ids),
		Clock:      integrationClock{},
		IDs:        ids,
	})

	run := dbAssertionRun(9901, 9911, 702)
	createExecutionBatch(t, batches, dbAssertBatch(9911, 1), []domain.CaseRun{run})
	if _, err := caseRuns.SetStatus(ctx, run.ID, domain.CaseRunStatusPending, domain.CaseRunStatusRunning, executionTestTime); err != nil {
		t.Fatalf("start case run: %v", err)
	}
	if _, err := recorder.RecordStep(ctx, run.ID, application.StepRecordInput{
		Seq: 1, Status: domain.StepStatusPassed, Actual: "done",
	}); err != nil {
		t.Fatalf("RecordStep: %v", err)
	}

	_, err = recorder.DBAssert(ctx, run.ID, run.Snapshot.Assertions[0].ID, application.DBAssertLimits{})
	if err == nil {
		t.Fatal("DBAssert succeeded despite the connection failure")
	}
	if !strings.Contains(err.Error(), application.DBReasonConnectionFailed) {
		t.Fatalf("error = %v, want reason %q", err, application.DBReasonConnectionFailed)
	}
	results, listErr := assertions.ListByCaseRun(ctx, run.ID)
	if listErr != nil {
		t.Fatalf("ListByCaseRun assertions: %v", listErr)
	}
	if len(results) != 1 || results[0].Verdict != "not_run" ||
		results[0].FailureReason != application.DBReasonConnectionFailed {
		t.Fatalf("assertion results = %+v", results)
	}

	finished, err := recorder.FinishCase(ctx, run.ID, application.FinishInput{
		Decision: domain.FinishDecision{
			Verdict: domain.VerdictBlocked, FailureClass: failureClassPtr(domain.FailureClassEnvironment),
		},
	})
	if err != nil {
		t.Fatalf("FinishCase blocked environment: %v", err)
	}
	if finished.Status != domain.CaseRunStatusBlocked {
		t.Fatalf("status = %s, want blocked", finished.Status)
	}
}

// dbAssertHarness is the real-PostgreSQL fixture of the db_assert failure-path
// tests: the recorder is wired to the real execution repositories and to the
// real resource read-only querier through the execution resource adapter.
type dbAssertHarness struct {
	recorder   *application.CaseRecorder
	batches    application.BatchRepository
	caseRuns   application.CaseRunRepository
	assertions application.AssertionRepository
	evidence   application.EvidenceRepository
}

// newDBAssertHarness wires one recorder against pool. idBase reserves the id
// ranges of previous subtests so their evidence/assertion ids never collide.
func newDBAssertHarness(t *testing.T, pool *pgxpool.Pool, query application.DBQueryPort, environmentID *int64, idBase int64) *dbAssertHarness {
	t.Helper()
	batches := postgres.NewBatchRepository(pool)
	caseRuns := postgres.NewCaseRunRepository(pool)
	steps := postgres.NewStepRepository(pool)
	assertions := postgres.NewAssertionRepository(pool)
	evidenceRepo := postgres.NewEvidenceRepository(pool)
	refs := postgres.NewEvidenceRefRepository(pool)
	queries := postgres.NewExecutionQueries(pool)
	store, err := filestore.New(t.TempDir())
	if err != nil {
		t.Fatalf("filestore.New: %v", err)
	}
	ids := &integrationIDs{next: idBase}
	evidenceService := application.NewEvidenceService(evidenceRepo, refs, batches, store, ids)
	recorder := application.NewRecorder(application.RecorderDeps{
		Locator:    queries,
		CaseRuns:   caseRuns,
		Steps:      steps,
		Assertions: assertions,
		Evidence:   evidenceRepo,
		Refs:       refs,
		Batches:    batches,
		Versions:   integrationVersions{environmentID: environmentID},
		DBQuery:    query,
		DBCapture:  evidenceService,
		Clock:      integrationClock{},
		IDs:        ids,
	})
	return &dbAssertHarness{
		recorder: recorder, batches: batches, caseRuns: caseRuns,
		assertions: assertions, evidence: evidenceRepo,
	}
}

// execDBTargets adapts one fixed stored target to the execution resource
// adapter's narrow target service.
type execDBTargets struct {
	target *resourcedomain.DBTarget
}

func (t execDBTargets) Get(context.Context, int64) (*resourcedomain.DBTarget, error) {
	return t.target, nil
}

// countingReadOnlyQuerier wraps the real resource querier and counts the
// execution-level query attempts: a rejected assertion must never reach SQL.
type countingReadOnlyQuerier struct {
	inner resourceapp.ReadOnlyQuerier
	calls int
}

func (q *countingReadOnlyQuerier) Query(ctx context.Context, target resourcedomain.DBTarget, sql string, limits resourcedomain.QueryLimits) (resourcedomain.QueryResult, error) {
	q.calls++
	return q.inner.Query(ctx, target, sql, limits)
}

// realDBTarget builds an enabled target pointing at the test database, so the
// db_assert paths run real PostgreSQL through the resource querier.
func realDBTarget(t *testing.T, maxRows, statementTimeoutMS int) resourcedomain.DBTarget {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("EZTEST_TEST_DATABASE_URL"))
	if raw == "" {
		t.Skip("EZTEST_TEST_DATABASE_URL is not set")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse test database URL: %v", err)
	}
	password, _ := parsed.User.Password()
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("test database URL has no valid port: %v", err)
	}
	sslMode := parsed.Query().Get("sslmode")
	switch sslMode {
	case resourcedomain.DBTargetSSLModeDisable, resourcedomain.DBTargetSSLModeRequire,
		resourcedomain.DBTargetSSLModeVerifyCA, resourcedomain.DBTargetSSLModeVerifyFull:
	default:
		sslMode = resourcedomain.DBTargetSSLModeDisable
	}
	return resourcedomain.DBTarget{
		EnvironmentID:      44,
		Name:               "orders",
		Host:               parsed.Hostname(),
		Port:               port,
		DatabaseName:       strings.TrimPrefix(parsed.Path, "/"),
		Username:           parsed.User.Username(),
		Password:           password,
		SSLMode:            sslMode,
		ConnectTimeoutMS:   3000,
		StatementTimeoutMS: statementTimeoutMS,
		MaxRows:            maxRows,
		Enabled:            true,
	}
}

// startDBAssertRun seeds the batch/run, starts the case and records the single
// snapshot step. The batch is closed in cleanup so the version's single active
// batch slot is free for the next subtest.
func startDBAssertRun(t *testing.T, h *dbAssertHarness, run domain.CaseRun, batchNo int) {
	t.Helper()
	createExecutionBatch(t, h.batches, dbAssertBatch(run.BatchID, batchNo), []domain.CaseRun{run})
	if _, err := h.caseRuns.SetStatus(context.Background(), run.ID,
		domain.CaseRunStatusPending, domain.CaseRunStatusRunning, executionTestTime); err != nil {
		t.Fatalf("start case run: %v", err)
	}
	if _, err := h.recorder.RecordStep(context.Background(), run.ID, application.StepRecordInput{
		Seq: 1, Status: domain.StepStatusPassed, Actual: "done",
	}); err != nil {
		t.Fatalf("RecordStep: %v", err)
	}
	t.Cleanup(func() {
		if _, err := h.batches.UpdateBatchStatus(context.Background(), run.BatchID,
			domain.BatchStatusRunning, domain.BatchStatusDone, nil); err != nil {
			t.Errorf("close batch %d: %v", run.BatchID, err)
		}
	})
}

// TestExecutionDBAssertIntegrationFailurePaths covers the five classified
// db_assert failure paths against real PostgreSQL: each persists the db
// assertion_result as not_run with the stable reason, and the finish the tool
// hint prescribes is accepted (blocked(environment) or inconclusive(case)).
func TestExecutionDBAssertIntegrationFailurePaths(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	target := realDBTarget(t, 100, 5000)
	truncatedTarget := realDBTarget(t, 2, 5000)
	environmentID := int64(44)
	blockedEnvironment := domain.FinishDecision{
		Verdict: domain.VerdictBlocked, FailureClass: failureClassPtr(domain.FailureClassEnvironment),
	}
	inconclusiveCase := domain.FinishDecision{
		Verdict: domain.VerdictInconclusive, FailureClass: failureClassPtr(domain.FailureClassCase),
	}

	tests := []struct {
		name         string
		expression   string
		expected     string
		target       *resourcedomain.DBTarget // nil means no configured target
		wantReason   string
		wantHint     string
		wantFinish   domain.FinishDecision
		wantStatus   domain.CaseRunStatus
		wantQueries  int
		wantEvidence int
	}{
		{
			name:       "missing target",
			expression: "SELECT 1 AS n",
			expected:   "[[1]]",
			wantReason: application.DBReasonTargetMissing,
			wantHint:   "finish blocked(environment)",
			wantFinish: blockedEnvironment,
			wantStatus: domain.CaseRunStatusBlocked,
		},
		{
			name:        "query error",
			expression:  "SELECT * FROM definitely_missing_table",
			expected:    "[[1]]",
			target:      &target,
			wantReason:  application.DBReasonQueryFailed,
			wantHint:    "finish inconclusive(case)",
			wantFinish:  inconclusiveCase,
			wantStatus:  domain.CaseRunStatusInconclusive,
			wantQueries: 1,
		},
		{
			name:        "read-only violation",
			expression:  "UPDATE db_target SET name='x'",
			expected:    "[[1]]",
			target:      &target,
			wantReason:  application.DBReasonReadonlyViolation,
			wantHint:    "finish inconclusive(case)",
			wantFinish:  inconclusiveCase,
			wantStatus:  domain.CaseRunStatusInconclusive,
			wantQueries: 1,
		},
		{
			name:         "truncated result",
			expression:   "SELECT generate_series(1, 5) AS n",
			expected:     "[[1],[2]]",
			target:       &truncatedTarget,
			wantReason:   application.DBReasonResultTruncated,
			wantHint:     "finish inconclusive(case)",
			wantFinish:   inconclusiveCase,
			wantStatus:   domain.CaseRunStatusInconclusive,
			wantQueries:  1,
			wantEvidence: 1,
		},
		{
			name:       "invalid expected executes nothing",
			expression: "SELECT 1 AS n",
			expected:   `{"rows":[`,
			target:     &target,
			wantReason: application.DBReasonExpectedInvalid,
			wantHint:   "finish inconclusive(case)",
			wantFinish: inconclusiveCase,
			wantStatus: domain.CaseRunStatusInconclusive,
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			querier := &countingReadOnlyQuerier{inner: pgreadonly.NewQuerier()}
			adapter := executionresource.NewDBQuery(execDBTargets{target: tt.target}, querier)
			h := newDBAssertHarness(t, pool, adapter, &environmentID, int64(i+1)*1000)
			run := dbAssertionRunWith(10101+int64(i), 10111+int64(i), 801+int64(i),
				tt.expression, tt.expected)
			startDBAssertRun(t, h, run, i+1)

			_, err := h.recorder.DBAssert(ctx, run.ID, run.Snapshot.Assertions[0].ID,
				application.DBAssertLimits{})
			if err == nil {
				t.Fatal("DBAssert succeeded despite a classified failure")
			}
			var toolErr *agentdomain.ToolError
			if !errors.As(err, &toolErr) || toolErr.Code != "tool_error" {
				t.Fatalf("error = %v (%T), want a tool_error", err, err)
			}
			if !strings.Contains(toolErr.Message, tt.wantReason) ||
				!strings.Contains(toolErr.Message, tt.wantHint) {
				t.Fatalf("tool error = %q, want reason %q and hint %q",
					toolErr.Message, tt.wantReason, tt.wantHint)
			}
			if querier.calls != tt.wantQueries {
				t.Fatalf("query calls = %d, want %d", querier.calls, tt.wantQueries)
			}

			results, listErr := h.assertions.ListByCaseRun(ctx, run.ID)
			if listErr != nil {
				t.Fatalf("ListByCaseRun assertions: %v", listErr)
			}
			if len(results) != 1 || results[0].Kind != "db" ||
				results[0].Verdict != "not_run" ||
				results[0].FailureReason != tt.wantReason {
				t.Fatalf("assertion results = %+v, want one not_run/%s db row",
					results, tt.wantReason)
			}
			evidenceRows, err := h.evidence.ListByCaseRun(ctx, run.ID)
			if err != nil {
				t.Fatalf("ListByCaseRun evidence: %v", err)
			}
			if len(evidenceRows) != tt.wantEvidence {
				t.Fatalf("db evidence rows = %d, want %d", len(evidenceRows), tt.wantEvidence)
			}
			if tt.wantEvidence == 1 && evidenceRows[0].Kind != "db_result" {
				t.Fatalf("evidence kind = %q, want db_result", evidenceRows[0].Kind)
			}

			finished, err := h.recorder.FinishCase(ctx, run.ID, application.FinishInput{
				Decision: tt.wantFinish,
			})
			if err != nil {
				t.Fatalf("FinishCase %s: %v", tt.wantFinish.Verdict, err)
			}
			if finished.Status != tt.wantStatus || finished.FailureClass == nil ||
				*finished.FailureClass != *tt.wantFinish.FailureClass {
				t.Fatalf("finished = %+v, want %s/%s", finished, tt.wantStatus, *tt.wantFinish.FailureClass)
			}
		})
	}
}

// TestExecutionDBAssertIntegrationTimeoutHint proves a real statement timeout
// (SQLSTATE 57014) is classified as db_connection_failed, and the real
// execution.db_assert tool (not only the recorder) prescribes
// blocked(environment) to the model.
func TestExecutionDBAssertIntegrationTimeoutHint(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	target := realDBTarget(t, 100, 500)
	querier := &countingReadOnlyQuerier{inner: pgreadonly.NewQuerier()}
	adapter := executionresource.NewDBQuery(execDBTargets{target: &target}, querier)
	environmentID := int64(44)
	h := newDBAssertHarness(t, pool, adapter, &environmentID, 0)
	run := dbAssertionRunWith(10201, 10211, 901, "SELECT pg_sleep(1)", `[[null]]`)
	startDBAssertRun(t, h, run, 1)

	tools := agent_tool.New(h.recorder, nil, nil, nil, agent_tool.Limits{})
	toolCtx := agentapp.WithToolInvocation(ctx, agentapp.Invocation{
		CaseRunID: run.ID, BatchID: run.BatchID, ProfileKey: "execution-agent",
	})
	args := json.RawMessage(`{"assertionId":"` +
		strconv.FormatInt(run.Snapshot.Assertions[0].ID, 10) + `"}`)
	var execErr error
	found := false
	for _, tool := range tools {
		if tool.Name() != "execution.db_assert" {
			continue
		}
		found = true
		_, execErr = tool.Execute(toolCtx, args)
		break
	}
	if !found {
		t.Fatal("execution.db_assert is not registered")
	}
	if execErr == nil {
		t.Fatal("the statement timeout did not surface as a tool error")
	}
	var toolErr *agentdomain.ToolError
	if !errors.As(execErr, &toolErr) || toolErr.Code != "tool_error" {
		t.Fatalf("tool error = %v (%T), want a tool_error", execErr, execErr)
	}
	if !strings.Contains(toolErr.Message, application.DBReasonConnectionFailed) ||
		!strings.Contains(toolErr.Message, "finish blocked(environment)") {
		t.Fatalf("tool error = %q, want db_connection_failed and blocked(environment)", toolErr.Message)
	}
	if querier.calls != 1 {
		t.Fatalf("query calls = %d, want 1", querier.calls)
	}

	results, err := h.assertions.ListByCaseRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListByCaseRun assertions: %v", err)
	}
	if len(results) != 1 || results[0].Kind != "db" || results[0].Verdict != "not_run" ||
		results[0].FailureReason != application.DBReasonConnectionFailed {
		t.Fatalf("assertion results = %+v, want not_run/db_connection_failed", results)
	}
	finished, err := h.recorder.FinishCase(ctx, run.ID, application.FinishInput{
		Decision: domain.FinishDecision{
			Verdict: domain.VerdictBlocked, FailureClass: failureClassPtr(domain.FailureClassEnvironment),
		},
	})
	if err != nil {
		t.Fatalf("FinishCase blocked environment: %v", err)
	}
	if finished.Status != domain.CaseRunStatusBlocked {
		t.Fatalf("status = %s, want blocked", finished.Status)
	}
}

// failureClassPtr returns a pointer to class for the finish decision.
func failureClassPtr(class domain.FailureClass) *domain.FailureClass {
	return &class
}
