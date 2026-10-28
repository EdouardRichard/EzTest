//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/filestore"
	executionpostgres "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/postgres"
	executionresource "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/resource"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	resourcedomain "github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure/pgreadonly"
	resourcepostgres "github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// p7xVersions is the execution VersionPort slice the recorder needs for the
// db_assert scenarios: every version uses the same configured environment.
type p7xVersions struct{ environmentID int64 }

// GetVersion returns an executing version ref with the configured environment.
func (v p7xVersions) GetVersion(_ context.Context, versionID int64) (executionapp.VersionRef, error) {
	return executionapp.VersionRef{ID: versionID, Status: "executing", EnvironmentID: &v.environmentID}, nil
}

// MarkPaused implements execution/application.VersionPort.
func (p7xVersions) MarkPaused(context.Context, int64, bool, string, string, int64) error { return nil }

// AllocateBatchNo implements execution/application.VersionPort.
func (p7xVersions) AllocateBatchNo(context.Context, int64) (int, error) { return 0, nil }

// ReleaseBatchNo implements execution/application.VersionPort.
func (p7xVersions) ReleaseBatchNo(context.Context, int64, int) error { return nil }

// SetBatchNo implements execution/application.VersionPort.
func (p7xVersions) SetBatchNo(context.Context, int64, int, int64) error { return nil }

// CompleteExecution implements execution/application.VersionPort.
func (p7xVersions) CompleteExecution(context.Context, int64, int64) error { return nil }

// p7xRealDBTarget builds the resource db target of the dedicated test database:
// the db_assert scenarios run the real read-only querier against real
// PostgreSQL (the querier opens its own connection and a read-only transaction).
func p7xRealDBTarget(t *testing.T, environmentID int64) resourcedomain.DBTarget {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("EZTEST_TEST_DATABASE_URL"))
	if raw == "" {
		t.Skip("EZTEST_TEST_DATABASE_URL is not set")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse EZTEST_TEST_DATABASE_URL: %v", err)
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
		ID:                 880123,
		EnvironmentID:      environmentID,
		Name:               "p7x-target",
		Host:               parsed.Hostname(),
		Port:               port,
		DatabaseName:       strings.TrimPrefix(parsed.Path, "/"),
		Username:           parsed.User.Username(),
		Password:           password,
		SSLMode:            sslMode,
		ConnectTimeoutMS:   3000,
		StatementTimeoutMS: 5000,
		MaxRows:            100,
		Enabled:            true,
		CreatedBy:          1,
		UpdatedBy:          1,
	}
}

// p7xDBAssertRun builds a pending case run carrying one db assertion.
func p7xDBAssertRun(runID, batchID, versionID, caseNodeID, assertionID int64, expression, expected string) executiondomain.CaseRun {
	snapshot := executiondomain.CaseSnapshot{
		Node: executiondomain.CaseSnapshotNode{
			ID: caseNodeID, Title: "db checkout", Priority: "P0",
		},
		Steps: []executiondomain.CaseSnapshotStep{
			{Seq: 1, Action: "query the orders table", Expected: "one row"},
		},
		Assertions: []executiondomain.CaseSnapshotAssertion{{
			ID: assertionID, StepSeq: 1, Kind: "db", Target: "p7x-target",
			Expression: expression, Expected: expected,
		}},
		GraphHash:  p7xValidHash('a'),
		CapturedAt: p7xTestTime,
	}
	return executiondomain.CaseRun{
		ID: runID, BatchID: batchID, VersionID: versionID, CaseNodeID: caseNodeID,
		CaseRevision: 1, CaseHash: executiondomain.CanonicalCaseHash(snapshot), Snapshot: snapshot,
		Attempt: 1, Status: executiondomain.CaseRunStatusPending,
		CreatedAt: p7xTestTime, UpdatedAt: p7xTestTime,
	}
}

// p7xDBAssertHarness groups the real recorder and repositories of one db_assert
// scenario.
type p7xDBAssertHarness struct {
	recorder   *executionapp.CaseRecorder
	batches    executionapp.BatchRepository
	caseRuns   executionapp.CaseRunRepository
	assertions executionapp.AssertionRepository
	evidence   executionapp.EvidenceRepository
	evidenceIO *executionapp.EvidenceService
}

// newP7xDBAssertHarness wires the recorder against the real execution
// repositories, the real resource db target service and the real read-only
// querier. The db target row is upserted through the real resource repository.
func newP7xDBAssertHarness(t *testing.T, pool *pgxpool.Pool, idStart int64) *p7xDBAssertHarness {
	t.Helper()
	ctx := context.Background()
	batches := executionpostgres.NewBatchRepository(pool)
	caseRuns := executionpostgres.NewCaseRunRepository(pool)
	steps := executionpostgres.NewStepRepository(pool)
	assertions := executionpostgres.NewAssertionRepository(pool)
	evidenceRepo := executionpostgres.NewEvidenceRepository(pool)
	refs := executionpostgres.NewEvidenceRefRepository(pool)
	queries := executionpostgres.NewExecutionQueries(pool)
	store, err := filestore.New(t.TempDir())
	if err != nil {
		t.Fatalf("execution filestore: %v", err)
	}
	ids := p7xNewIDs(idStart)

	targetRepo := resourcepostgres.NewDBTargetRepository(pool)
	target := p7xRealDBTarget(t, 44)
	if err := targetRepo.UpsertDBTarget(ctx, &target); err != nil {
		t.Fatalf("store db target: %v", err)
	}
	targetService := resourceapp.NewDBTargetService(targetRepo, nil, nil, nil, nil)
	queryPort := executionresource.NewDBQuery(targetService, pgreadonly.NewQuerier())

	evidenceService := executionapp.NewEvidenceService(evidenceRepo, refs, batches, store, ids)
	recorder := executionapp.NewRecorder(executionapp.RecorderDeps{
		Locator:    queries,
		CaseRuns:   caseRuns,
		Steps:      steps,
		Assertions: assertions,
		Evidence:   evidenceRepo,
		Refs:       refs,
		Batches:    batches,
		Versions:   p7xVersions{environmentID: 44},
		DBQuery:    queryPort,
		DBCapture:  evidenceService,
		Clock:      p7xClock{now: p7xTestTime},
		IDs:        ids,
	})
	return &p7xDBAssertHarness{
		recorder: recorder, batches: batches, caseRuns: caseRuns,
		assertions: assertions, evidence: evidenceRepo, evidenceIO: evidenceService,
	}
}

// start seeds one running batch with the supplied db case run and starts it.
func (h *p7xDBAssertHarness) start(t *testing.T, batch executiondomain.RunBatch, run executiondomain.CaseRun) {
	t.Helper()
	p7xSeedRunBatch(t, h.batches, batch, []executiondomain.CaseRun{run})
	if _, err := h.caseRuns.SetStatus(context.Background(), run.ID,
		executiondomain.CaseRunStatusPending, executiondomain.CaseRunStatusRunning, p7xTestTime); err != nil {
		t.Fatalf("start case run %d: %v", run.ID, err)
	}
	if _, err := h.recorder.RecordStep(context.Background(), run.ID, executionapp.StepRecordInput{
		Seq: 1, Status: executiondomain.StepStatusPassed, Actual: "query executed",
	}); err != nil {
		t.Fatalf("record step of case run %d: %v", run.ID, err)
	}
}

// TestP7DBAssertRealTargetIntegration runs execution.db_assert through the real
// resource db target service and the real read-only PostgreSQL querier:
//
//   - a passing assertion persists the db assertion_result and the db_result
//     evidence (with its sha256) and the finish evidence gate accepts it;
//   - a value mismatch records verdict=failed/assertion_mismatch and the
//     failed(application) finish is accepted;
//   - a write is rejected by the read-only SQL whitelist/transaction and the
//     not_run/db_readonly_violation result is persisted.
func TestP7DBAssertRealTargetIntegration(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()

	scenarios := []struct {
		name         string
		versionID    int64
		batchID      int64
		caseRunID    int64
		caseNodeID   int64
		assertionID  int64
		expression   string
		expected     string
		wantVerdict  string
		wantReason   string
		wantEvidence int
		wantError    bool
	}{
		{
			name: "passed", versionID: 5501, batchID: 55011, caseRunID: 550101,
			caseNodeID: 550201, assertionID: 550301,
			expression: "SELECT 1 AS n", expected: "[[1]]",
			wantVerdict: "passed", wantEvidence: 1,
		},
		{
			name: "value mismatch", versionID: 5502, batchID: 55021, caseRunID: 550102,
			caseNodeID: 550202, assertionID: 550302,
			expression: "SELECT 2 AS n", expected: "[[1]]",
			wantVerdict: "failed", wantReason: "assertion_mismatch", wantEvidence: 1,
		},
		{
			name: "read-only write rejected", versionID: 5503, batchID: 55031, caseRunID: 550103,
			caseNodeID: 550203, assertionID: 550303,
			expression: "UPDATE db_target SET name = 'x'", expected: "[[1]]",
			wantVerdict: "not_run", wantReason: executionapp.DBReasonReadonlyViolation, wantError: true,
		},
	}
	for i, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			h := newP7xDBAssertHarness(t, pool, int64(950000+i*1000))
			batch := executiondomain.RunBatch{
				ID: scenario.batchID, VersionID: scenario.versionID, BatchNo: 1,
				Status: executiondomain.BatchStatusRunning, Kind: executiondomain.BatchKindBaseline,
				BaselineHash: p7xValidHash('a'), GraphHash: p7xValidHash('a'),
				PauseMode: executiondomain.PauseModeGraceful, CreatedBy: 1,
				CreatedAt: p7xTestTime, UpdatedAt: p7xTestTime,
			}
			run := p7xDBAssertRun(scenario.caseRunID, scenario.batchID, scenario.versionID,
				scenario.caseNodeID, scenario.assertionID, scenario.expression, scenario.expected)
			h.start(t, batch, run)

			outcome, err := h.recorder.DBAssert(ctx, run.ID, scenario.assertionID, executionapp.DBAssertLimits{})
			if scenario.wantError {
				if err == nil {
					t.Fatal("DBAssert succeeded despite the classified failure")
				}
				if !strings.Contains(err.Error(), scenario.wantReason) {
					t.Fatalf("error = %v, want reason %q", err, scenario.wantReason)
				}
			} else {
				if err != nil {
					t.Fatalf("DBAssert: %v", err)
				}
				if outcome.Verdict != scenario.wantVerdict {
					t.Fatalf("verdict = %q, want %q (%+v)", outcome.Verdict, scenario.wantVerdict, outcome)
				}
				if scenario.wantVerdict == "passed" && outcome.RowCount != 1 {
					t.Fatalf("row count = %d, want 1", outcome.RowCount)
				}
				if scenario.wantVerdict == "failed" && outcome.FailureReason != scenario.wantReason {
					t.Fatalf("failure reason = %q, want %q", outcome.FailureReason, scenario.wantReason)
				}
			}

			results, err := h.assertions.ListByCaseRun(ctx, run.ID)
			if err != nil {
				t.Fatalf("list assertion results: %v", err)
			}
			if len(results) != 1 || results[0].Kind != "db" || results[0].Verdict != scenario.wantVerdict ||
				results[0].Target != "p7x-target" || results[0].FailureReason != scenario.wantReason {
				t.Fatalf("assertion results = %+v", results)
			}
			// A query that never reached PostgreSQL (the read-only whitelist
			// rejection) has no result preview to decode.
			if scenario.wantVerdict != "not_run" {
				var actual struct {
					RowCount  int    `json:"rowCount"`
					Truncated bool   `json:"truncated"`
					ElapsedMS *int64 `json:"elapsedMs"`
				}
				if err := json.Unmarshal([]byte(results[0].Actual), &actual); err != nil {
					t.Fatalf("decode persisted actual preview: %v (%q)", err, results[0].Actual)
				}
				if actual.ElapsedMS == nil {
					t.Fatalf("persisted actual preview has no elapsedMs: %q", results[0].Actual)
				}
			}

			evidenceRows, err := h.evidence.ListByCaseRun(ctx, run.ID)
			if err != nil {
				t.Fatalf("list evidence: %v", err)
			}
			if len(evidenceRows) != scenario.wantEvidence {
				t.Fatalf("evidence rows = %d, want %d (%+v)", len(evidenceRows), scenario.wantEvidence, evidenceRows)
			}
			if scenario.wantEvidence == 1 {
				if evidenceRows[0].Kind != "db_result" || evidenceRows[0].Mime != "application/json" {
					t.Fatalf("evidence row = %+v, want db_result/application/json", evidenceRows[0])
				}
				reader, stored, err := h.evidenceIO.Open(ctx, scenario.versionID, evidenceRows[0].ID)
				if err != nil {
					t.Fatalf("open db evidence: %v", err)
				}
				defer reader.Close()
				var document struct {
					SQL      string     `json:"sql"`
					Columns  []string   `json:"columns"`
					Rows     [][]string `json:"rows"`
					RowCount int        `json:"row_count"`
				}
				if err := json.NewDecoder(reader).Decode(&document); err != nil {
					t.Fatalf("decode db evidence: %v", err)
				}
				if stored.SHA256 != evidenceRows[0].SHA256 || stored.SHA256 == "" ||
					document.SQL != scenario.expression || document.RowCount < 1 {
					t.Fatalf("db evidence document = %+v stored = %+v", document, stored)
				}
			}

			finish := executionapp.FinishInput{Decision: executiondomain.FinishDecision{Verdict: executiondomain.VerdictPassed}}
			if scenario.wantVerdict == "failed" {
				failureClass := executiondomain.FailureClassApplication
				finish.Decision = executiondomain.FinishDecision{
					Verdict: executiondomain.VerdictFailed, FailureClass: &failureClass,
				}
			}
			if scenario.wantReason == executionapp.DBReasonReadonlyViolation {
				failureClass := executiondomain.FailureClassCase
				finish.Decision = executiondomain.FinishDecision{
					Verdict: executiondomain.VerdictInconclusive, FailureClass: &failureClass,
				}
			}
			finished, err := h.recorder.FinishCase(ctx, run.ID, finish)
			if err != nil {
				t.Fatalf("FinishCase %s: %v", finish.Decision.Verdict, err)
			}
			if scenario.wantVerdict == "passed" && finished.Status != executiondomain.CaseRunStatusPassed {
				t.Fatalf("finished status = %s, want passed", finished.Status)
			}
			if scenario.wantVerdict == "failed" && finished.Status != executiondomain.CaseRunStatusFailed {
				t.Fatalf("finished status = %s, want failed", finished.Status)
			}
			if scenario.wantReason == executionapp.DBReasonReadonlyViolation &&
				finished.Status != executiondomain.CaseRunStatusInconclusive {
				t.Fatalf("finished status = %s, want inconclusive", finished.Status)
			}
		})
	}
}
