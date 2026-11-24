//go:build integration

package executionapp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	executionadapters "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/adapters"
	executionpg "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/postgres"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	storagepg "github.com/EdouardRichard/EzTest/backend/internal/storage/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// v0.2.4-F T12 (SC-F13, design §6.5/§6.7): data preparation is an
// EXECUTION-LOOP acceptance, not just a tool-layer unlock. The loop is
//
//	Prepare (NL snapshot with preconditions) → establish the Given
//	preconditions via db.query general SQL (seed) → execute the step → record
//	the assertion (expected server-overwritten) → cleanup the seeded data →
//	case_finish candidate → deterministic server verdict.
//
// The failure path is part of the contract: a failed seed means the
// precondition is NOT established; the run must record that observably
// (failed step + error, deterministic terminal) and never silently skip the
// precondition and pass.

// dataPrepTargetDSN points the REAL database adapter at the test pool's own
// schema, so the seed/cleanup writes are verifiable through the same pool.
func dataPrepTargetDSN(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var schema string
	if err := pool.QueryRow(context.Background(), "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatalf("current_schema: %v", err)
	}
	base := os.Getenv("EZTEST_TEST_DATABASE_URL")
	if strings.Contains(base, "?") {
		return base + "&search_path=" + schema
	}
	return base + "?search_path=" + schema
}

// dataPrepSnapshot is the confirmed NL case: a Given precondition that must be
// established by data preparation, one prose-target step and one db assertion.
func dataPrepSnapshot() execution.CaseSnapshot {
	return execution.CaseSnapshot{
		CaseID: 51, CaseVersionID: 41, RequirementID: 501, RequirementVersionID: 601,
		Title:          "查询种子账号余额",
		Description:    "运营人员查询指定账号的余额",
		SUTDescription: "订单中心 Web 端，面向内部运营人员",
		Preconditions:  "数据库中已存在账号 seed_user 且余额为 100",
		CoveredACs:     []string{"AC-1"},
		Priority:       "P1",
		Steps: []execution.CaseStep{{
			Seq: 1, Action: "查询账号余额", Expected: "返回账号余额记录",
			TargetDescription: "运营后台的账号余额查询页", AccountRole: "admin",
		}},
		Assertions: []execution.CaseAssertion{{
			StepSeq: 1, AssertionSeq: 1, Kind: "db",
			Expected: "seed_user 的余额为 100", SUTID: 7,
		}},
	}
}

type dataPrepFixture struct {
	runner  *executionapp.CaseRunner
	repo    *executionpg.Repository
	pool    *pgxpool.Pool
	ids     *platformid.Snowflake
	scope   tools.Scope
	item    agent.WorkItem
	table   string
	caseRun int64
}

// newDataPrepFixture wires the REAL settlement chain (postgres repositories,
// content-addressed evidence store, the REAL read-write database adapter) with
// only the credential resolution faked to hand out the test DSN. Each fixture
// owns one intent/wave/case_run so success and failure runs never share state.
func newDataPrepFixture(t *testing.T, pool *pgxpool.Pool, ids *platformid.Snowflake, label string) *dataPrepFixture {
	t.Helper()
	ctx := context.Background()
	repo := executionpg.NewRepository(pool, ids)
	dsn := dataPrepTargetDSN(t, pool)

	settings := &integrationSettings{
		ints: map[string]int64{
			"execution.evidence_max_bytes":            5 << 20,
			"execution.evidence_max_per_case_run":     0,
			"execution.executor_context_ttl_seconds":  300,
			"execution.case_timeout_seconds":          0,
			"execution.human_request_timeout_seconds": 600,
		},
		strings: map[string]string{"paths.data_root": t.TempDir()},
	}
	storageSvc := storageapp.New(storagepg.New(pool), storagepg.NewFileStore(), settings, ids, nil, nil)
	content := executionadapters.NewContentStore(storageSvc)

	const sutID, caseVersionID = int64(7), int64(41)
	// uk_run_wave_active allows a single active wave per version, so fixtures
	// sharing one pool must each own a version id.
	versionID := map[string]int64{"ok": 10, "fail": 11, "fail_noev": 12}[label]
	if versionID == 0 {
		t.Fatalf("unknown data-prep fixture label %q", label)
	}
	intentID, waveID, caseRunID := ids.Next(), ids.Next(), ids.Next()
	// The intent idempotency key is (case_version_id, environment_fingerprint,
	// code_baseline, purpose): fixtures sharing one pool must differ in the
	// fingerprint or the second Create collides with the first intent.
	fingerprintChar := map[string]string{"ok": "a", "fail": "b", "fail_noev": "c"}[label]
	if fingerprintChar == "" {
		t.Fatalf("unknown data-prep fixture label %q", label)
	}
	if err := repo.Intents.Create(ctx, execution.ExecutionIntent{
		ID: intentID, VersionID: versionID, CaseVersionID: caseVersionID,
		SUTIDs: []int64{sutID}, EnvironmentFingerprint: strings.Repeat(fingerprintChar, 64),
		Purpose: execution.PurposeInitial, Status: execution.IntentRunning,
		DedupeKey: "exec:data-prep-" + label, AttemptNo: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create intent: %v", err)
	}
	if err := repo.Waves.CreateWithCaseRuns(ctx, execution.RunWave{
		ID: waveID, VersionID: versionID, Status: execution.WaveRunning,
		Trigger: execution.WaveTriggerAuto, Counts: execution.WaveCounts{},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}, nil, nil); err != nil {
		t.Fatalf("create wave: %v", err)
	}
	if err := repo.CaseRuns.Create(ctx, execution.CaseRun{
		ID: caseRunID, WaveID: waveID, IntentID: intentID, CaseVersionID: caseVersionID,
		Status: execution.CaseRunPending, Attempts: 1,
		DispatchEpoch: 3, DispatchToken: "dispatch-token",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create case run: %v", err)
	}

	runner := executionapp.NewCaseRunner(executionapp.CaseRunnerDeps{
		Intents:     repo.Intents,
		CaseRuns:    repo.CaseRuns,
		SUTContexts: integrationSUTContexts{repo: repo.SUTContexts},
		Results:     repo.Results,
		Evidence:    repo.Evidence,
		Cases:       &fakeExecutionCaseReader{snapshot: dataPrepSnapshot()},
		Bindings: &fakeBindingResolver{bindings: []execution.SUTBinding{{
			SUTID: sutID, SUTName: "orders", Intro: "订单中心：下单、查询与审核",
			EnvironmentID: 3, EnvironmentName: "test",
			BaseURL: "http://127.0.0.1:13335", AccountRoles: []string{"admin"},
		}}},
		Execution:   &fakeExecutionExecutor{},
		Credentials: &fakeCredentialReader{database: executionapp.DatabaseCredential{DatabaseID: 81, DSN: dsn}},
		Database:    executionadapters.NewDatabaseAdapter(),
		Content:     content,
		Settings:    settings,
		Clock:       integrationClock{},
		IDs:         ids,
		InstanceID:  "api-data-prep",
	})
	payload, _ := json.Marshal(map[string]any{
		"intent_id":       fmt.Sprintf("%d", intentID),
		"wave_id":         fmt.Sprintf("%d", waveID),
		"case_run_id":     fmt.Sprintf("%d", caseRunID),
		"case_version_id": fmt.Sprintf("%d", caseVersionID),
		"dispatch_epoch":  3,
		"dispatch_token":  "dispatch-token",
	})
	return &dataPrepFixture{
		runner: runner, repo: repo, pool: pool, ids: ids,
		scope: tools.Scope{
			VersionID: versionID, WaveID: waveID, CaseRunID: caseRunID, IntentID: intentID,
			CaseVersionID: caseVersionID, DispatchEpoch: 3, DispatchToken: "dispatch-token",
			AgentRunID: ids.Next(),
		},
		item:    agent.WorkItem{ID: ids.Next(), VersionID: versionID, Type: "execute.case", Payload: payload},
		table:   "data_prep_seed_" + label,
		caseRun: caseRunID,
	}
}

func (f *dataPrepFixture) dbQuery(t *testing.T, sql string) (map[string]any, error) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"sql": sql})
	out, err := f.runner.DBQuery(context.Background(), f.scope, raw)
	if err != nil {
		return nil, err
	}
	result, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("db.query result = %#v, want an object", out)
	}
	return result, nil
}

// TestDataPrepSeedCleanup is the SC-F13 happy path: the Given precondition is
// established by a real SQL seed through the execution db.query tool, verified,
// exercised by the step/assertion, cleaned up, and the server settles passed.
func TestDataPrepSeedCleanup(t *testing.T) {
	pool := testutil.NewDB(t)
	ids, err := platformid.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	f := newDataPrepFixture(t, pool, ids, "ok")
	ctx := context.Background()

	// Prepare hands the executor the NL snapshot: preconditions travel with the
	// case (the data-prep obligation), and the environment id is stamped.
	snapshot, err := f.runner.Prepare(ctx, f.item)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if snapshot.Case.Preconditions != dataPrepSnapshot().Preconditions {
		t.Fatalf("snapshot preconditions = %q, want the Given prose", snapshot.Case.Preconditions)
	}
	if snapshot.Case.SUTDescription == "" || len(snapshot.Case.CoveredACs) == 0 ||
		snapshot.Case.Steps[0].TargetDescription == "" {
		t.Fatalf("snapshot lost the NL contract: %+v", snapshot.Case)
	}
	if snapshot.EnvironmentID != 3 {
		t.Fatalf("snapshot environment id = %d, want the bound environment 3", snapshot.EnvironmentID)
	}
	if len(snapshot.SUTs) != 1 || snapshot.SUTs[0].Intro == "" || snapshot.SUTs[0].EnvironmentID != 3 {
		t.Fatalf("snapshot SUT catalog = %+v, want intro and environment_id", snapshot.SUTs)
	}

	// The target table exists but the Given row does NOT: the precondition is
	// genuinely unestablished before data prep (the fixture is non-vacuous).
	if _, err := pool.Exec(ctx, fmt.Sprintf("CREATE TABLE %s (username text PRIMARY KEY, balance integer)", f.table)); err != nil {
		t.Fatalf("create target table: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+f.table)
	})
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+f.table).Scan(&count); err != nil || count != 0 {
		t.Fatalf("pre-seed count = %d (err %v), want 0", count, err)
	}

	// Seed the Given precondition through the execution-loop db.query tool.
	seeded, err := f.dbQuery(t, fmt.Sprintf("INSERT INTO %s (username, balance) VALUES ('seed_user', 100)", f.table))
	if err != nil {
		t.Fatalf("seed INSERT through db.query: %v", err)
	}
	if seeded["rows_affected"] != int64(1) {
		t.Fatalf("seed rows_affected = %#v, want 1", seeded["rows_affected"])
	}
	if seeded["evidence_id"] == nil || seeded["evidence_id"] == "" {
		t.Fatalf("the seed produced no db evidence: %#v", seeded)
	}
	// The precondition is established in the REAL target database.
	if err := pool.QueryRow(ctx, "SELECT balance FROM "+f.table+" WHERE username = 'seed_user'").Scan(&count); err != nil || count != 100 {
		t.Fatalf("seeded balance = %d (err %v), want 100", count, err)
	}

	// Verify the Given via the same tool, then execute the step and assertion.
	verified, err := f.dbQuery(t, "SELECT balance FROM "+f.table+" WHERE username = 'seed_user'")
	if err != nil {
		t.Fatalf("verify SELECT: %v", err)
	}
	rows, _ := verified["rows"].([][]string)
	if len(rows) != 1 || rows[0][0] != "100" {
		t.Fatalf("verify rows = %#v, want [[100]]", verified["rows"])
	}
	if _, err := f.runner.StepRecord(ctx, f.scope, tools.StepRecordInput{
		Seq: 1, Status: "passed", Observed: "余额查询返回 seed_user=100",
	}); err != nil {
		t.Fatalf("StepRecord: %v", err)
	}
	if _, err := f.runner.AssertionRecord(ctx, f.scope, tools.AssertionRecordInput{
		StepSeq: 1, AssertionSeq: 1, Verdict: "passed", Actual: "seed_user 的余额为 100",
	}); err != nil {
		t.Fatalf("AssertionRecord: %v", err)
	}

	// Cleanup the seeded data through the same loop (design §6.5: seed + cleanup).
	cleaned, err := f.dbQuery(t, "DELETE FROM "+f.table+" WHERE username = 'seed_user'")
	if err != nil {
		t.Fatalf("cleanup DELETE: %v", err)
	}
	if cleaned["rows_affected"] != int64(1) {
		t.Fatalf("cleanup rows_affected = %#v, want 1", cleaned["rows_affected"])
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+f.table).Scan(&count); err != nil || count != 0 {
		t.Fatalf("post-cleanup count = %d (err %v), want 0", count, err)
	}

	// The model candidate is settled by the server: passed, with db evidence.
	out, err := f.runner.CaseFinish(ctx, f.scope, tools.CaseFinishInput{Verdict: "passed"})
	if err != nil {
		t.Fatalf("CaseFinish: %v", err)
	}
	finished := out.(map[string]any)
	if finished["status"] != "passed" || finished["verdict_reason"] != execution.VerdictReasonPassed {
		t.Fatalf("finish = %#v, want passed/passed", finished)
	}
	evidence, err := f.repo.Evidence.ListByCaseRun(ctx, f.caseRun, 50)
	if err != nil {
		t.Fatalf("ListByCaseRun: %v", err)
	}
	dbEvidence := 0
	for _, row := range evidence {
		if row.Kind == execution.EvidenceDB {
			dbEvidence++
		}
	}
	if dbEvidence < 3 {
		t.Fatalf("db evidence rows = %d, want >= 3 (seed + verify + cleanup)", dbEvidence)
	}
}

// TestDataPrepSeedFailureObservable is the SC-F13 failure path: the seed SQL
// fails, so the Given precondition is NOT established. The run must surface
// that (typed tool error, failed step with the error, deterministic terminal)
// — never silently skip the precondition and report passed. With evidence the
// terminal is failed(environment); without any evidence the no-evidence gate
// degrades it to inconclusive(no_evidence) — still observable, never passed.
func TestDataPrepSeedFailureObservable(t *testing.T) {
	pool := testutil.NewDB(t)
	ids, err := platformid.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	ctx := context.Background()

	t.Run("failed seed with evidence settles failed(environment)", func(t *testing.T) {
		f := newDataPrepFixture(t, pool, ids, "fail")
		if _, err := f.runner.Prepare(ctx, f.item); err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		// The seed targets a table that does not exist: the precondition cannot
		// be established and the tool must return a typed error, not success.
		_, seedErr := f.dbQuery(t, "INSERT INTO data_prep_missing_table (username) VALUES ('seed_user')")
		if seedErr == nil {
			t.Fatal("a failing seed must return an error, not silent success")
		}
		if !strings.Contains(seedErr.Error(), "data_prep_missing_table") &&
			!strings.Contains(seedErr.Error(), "SQL statement failed") {
			t.Fatalf("seed error does not name the failure: %v", seedErr)
		}
		// The precondition failure is recorded as a failed step with the error.
		if _, err := f.runner.StepRecord(ctx, f.scope, tools.StepRecordInput{
			Seq: 1, Status: "failed",
			Error: "seed failed, precondition not established: " + seedErr.Error(),
		}); err != nil {
			t.Fatalf("StepRecord: %v", err)
		}
		if _, err := f.runner.AddEvidence(ctx, f.scope, executionapp.EvidenceInput{
			Kind:     execution.EvidenceText,
			Data:     []byte("data preparation failed: " + seedErr.Error()),
			Filename: "data-prep-failure.txt",
		}); err != nil {
			t.Fatalf("AddEvidence: %v", err)
		}
		out, err := f.runner.CaseFinish(ctx, f.scope, tools.CaseFinishInput{
			Verdict: "failed", FailureClass: "environment",
		})
		if err != nil {
			t.Fatalf("CaseFinish: %v", err)
		}
		finished := out.(map[string]any)
		if finished["status"] != "failed" || finished["failure_class"] != "environment" ||
			finished["verdict_reason"] != execution.VerdictReasonStepFailed {
			t.Fatalf("finish = %#v, want failed/environment/step_failed (precondition failure must not pass)", finished)
		}
		// The failed step and its error are persisted facts, not just a verdict.
		steps, err := f.repo.Results.ListSteps(ctx, f.caseRun)
		if err != nil || len(steps) != 1 {
			t.Fatalf("ListSteps = %+v (err %v), want the one failed step", steps, err)
		}
		if steps[0].Status != execution.StepFailed || !strings.Contains(steps[0].Error, "precondition not established") {
			t.Fatalf("persisted step = %+v, want failed with the seed error", steps[0])
		}
	})

	t.Run("failed seed without evidence degrades inconclusive, never passed", func(t *testing.T) {
		f := newDataPrepFixture(t, pool, ids, "fail_noev")
		if _, err := f.runner.Prepare(ctx, f.item); err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		if _, seedErr := f.dbQuery(t, "INSERT INTO data_prep_missing_table (username) VALUES ('seed_user')"); seedErr == nil {
			t.Fatal("a failing seed must return an error")
		}
		if _, err := f.runner.StepRecord(ctx, f.scope, tools.StepRecordInput{
			Seq: 1, Status: "failed", Error: "seed failed, precondition not established",
		}); err != nil {
			t.Fatalf("StepRecord: %v", err)
		}
		out, err := f.runner.CaseFinish(ctx, f.scope, tools.CaseFinishInput{
			Verdict: "failed", FailureClass: "environment",
		})
		if err != nil {
			t.Fatalf("CaseFinish: %v", err)
		}
		finished := out.(map[string]any)
		// Constitution art.1.2: a failed conclusion with zero evidence is not a
		// trustworthy defect; it degrades to inconclusive(no_evidence). The
		// silent-skip failure mode this guards against is status=passed.
		if finished["status"] != "inconclusive" || finished["verdict_reason"] != execution.VerdictReasonNoEvidence {
			t.Fatalf("finish = %#v, want inconclusive/no_evidence", finished)
		}
	})
}
