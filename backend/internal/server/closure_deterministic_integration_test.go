//go:build integration

package server_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// seedGD17ClosableVersion provisions a version whose requirement closure and
// version close predicates are satisfiable: one clarified requirement with a
// current+confirmed case whose latest run passed, a terminal wave/intent, and no
// active intent/human blocker. It mirrors the design/execution fixture shape used
// by TestExecutionManualIntentForPassedRerun (real schema, real adapters).
func seedGD17ClosableVersion(t *testing.T, api *testAPI, versionID int64, ids gd17FixtureIDs) {
	t.Helper()
	ctx := context.Background()
	execSQL := func(statement string, args ...any) {
		t.Helper()
		if _, err := api.pool.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed GD-17 fixture: %v\n%s", err, statement)
		}
	}
	// The API creates the version as initial; the reconciler's projectVersions
	// rule marks it running once content exists. Seed that running state directly
	// (the close predicate requires running; initial->running is a separate rule).
	execSQL(`UPDATE version SET status = 'running' WHERE id = $1`, versionID)
	execSQL(`INSERT INTO requirement (id, version_id, current_version_id, status)
		VALUES ($1, $2, $3, 'clarified')`, ids.requirementID, versionID, ids.requirementVersionID)
	execSQL(`INSERT INTO requirement_version (id, requirement_id, revision, title, content_hash,
		user_story, sut_description, acceptance_criteria)
		VALUES ($1, $2, 1, 'GD-17 closure requirement', $3,
		$4::jsonb, 'the order center', $5::jsonb)`,
		ids.requirementVersionID, ids.requirementID, strings.Repeat("a", 64),
		`{"as_a":"tester","i_want":"deterministic closure","so_that":"no Steward is needed"}`,
		`[{"id":"AC-1","given":"a passed case","when":"the reconciler ticks","then":"the requirement closes"}]`)
	execSQL(`INSERT INTO case_scenario (id, version_id, requirement_id, requirement_version_id, name)
		VALUES ($1, $2, $3, $4, 'GD-17 closure scenario')`,
		ids.scenarioID, versionID, ids.requirementID, ids.requirementVersionID)
	execSQL(`INSERT INTO case_entity (id, scenario_id, current_version_id, status)
		VALUES ($1, $2, $3, 'confirmed')`, ids.caseID, ids.scenarioID, ids.caseVersionID)
	execSQL(`INSERT INTO case_version (id, case_id, revision, title, description, priority,
		sut_description, covered_acs, status, content_hash)
		VALUES ($1, $2, 1, 'GD-17 passed case', '', 'P2', 'the order center', $3::jsonb, 'confirmed', $4)`,
		ids.caseVersionID, ids.caseID, `["AC-1"]`, strings.Repeat("b", 64))
	execSQL(`INSERT INTO case_step (id, case_version_id, seq, action, target_description)
		VALUES ($1, $2, 1, 'open the page', 'the result page')`, ids.caseVersionID+100000, ids.caseVersionID)
	execSQL(`INSERT INTO case_assertion (id, case_version_id, step_seq, kind, target, expected)
		VALUES ($1, $2, 1, 'ui', '#result', 'ok')`, ids.caseVersionID+200000, ids.caseVersionID)
	execSQL(`INSERT INTO run_wave (id, version_id, status, trigger, counts, started_at, finished_at)
		VALUES ($1, $2, 'done', 'auto', '{}'::jsonb, now(), now())`, ids.waveID, versionID)
	execSQL(`INSERT INTO execution_intent (id, version_id, case_version_id, sut_ids,
		environment_fingerprint, purpose, status, dedupe_key)
		VALUES ($1, $2, $3, '[]'::jsonb, $4, 'initial', 'done', $5)`,
		ids.intentID, versionID, ids.caseVersionID, strings.Repeat("c", 64),
		"gd17-seed-intent-"+strconv.FormatInt(ids.intentID, 10))
	execSQL(`INSERT INTO case_run (id, wave_id, intent_id, case_version_id, status, attempts,
		failure_class, summary, started_at, finished_at)
		VALUES ($1, $2, $3, $4, 'passed', 1, '', '{}'::jsonb, now(), now())`,
		ids.caseRunID, ids.waveID, ids.intentID, ids.caseVersionID)
}

type gd17FixtureIDs struct {
	requirementID        int64
	requirementVersionID int64
	scenarioID           int64
	caseID               int64
	caseVersionID        int64
	waveID               int64
	intentID             int64
	caseRunID            int64
}

// seedGD17DeadCloseRows inserts pre-upgrade in-flight requirement.close and
// version.close work items (team 'steward', which the append-only DB CHECK still
// accepts). After GD-17 these types have no consumer; without disposal they would
// block closure forever (SC-G10 existing-row disposal).
func seedGD17DeadCloseRows(t *testing.T, api *testAPI, versionID, requirementID int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := api.pool.Exec(ctx, `
		INSERT INTO work_item (id, version_id, team, type, status, priority, dedupe_key, payload, max_attempts)
		VALUES ($1, $2, 'steward', 'requirement.close', 'open', 0, 'gd17-dead-req-close', $3::jsonb, 10)`,
		versionID*10+1, versionID, `{"requirement_id":"`+strconv.FormatInt(requirementID, 10)+`"}`); err != nil {
		t.Fatalf("seed dead requirement.close row: %v", err)
	}
	if _, err := api.pool.Exec(ctx, `
		INSERT INTO work_item (id, version_id, team, type, status, priority, dedupe_key, payload, max_attempts)
		VALUES ($1, $2, 'steward', 'version.close', 'open', 0, 'gd17-dead-ver-close', '{}'::jsonb, 3)`,
		versionID*10+2, versionID); err != nil {
		t.Fatalf("seed dead version.close row: %v", err)
	}
}

// publishGD17VersionReport ensures + publishes the version report through the real
// report service, then terminalizes the background report work items (in production
// the report_writer run completes them; the test isolates the closure work-item leg).
func publishGD17VersionReport(t *testing.T, api *testAPI, versionID int64) {
	t.Helper()
	ctx := context.Background()
	report, err := api.reportSvc.EnsureVersionReport(ctx, versionID)
	if err != nil {
		t.Fatalf("EnsureVersionReport: %v", err)
	}
	if _, err := api.reportSvc.Publish(ctx, report.ID, 1, "GD-17 确定性闭环集成报告"); err != nil {
		t.Fatalf("Publish version report: %v", err)
	}
	if _, err := api.pool.Exec(ctx, `
		UPDATE work_item SET status = 'done', attempts = max_attempts, updated_at = now()
		WHERE version_id = $1 AND type NOT IN ('requirement.close', 'version.close')
		  AND status IN ('open', 'claimed', 'running')`, versionID); err != nil {
		t.Fatalf("terminalize background report tasks: %v", err)
	}
}

func gd17RequirementStatus(t *testing.T, api *testAPI, requirementID int64) string {
	t.Helper()
	var status string
	if err := api.pool.QueryRow(context.Background(),
		`SELECT status FROM requirement WHERE id = $1`, requirementID).Scan(&status); err != nil {
		t.Fatalf("read requirement status: %v", err)
	}
	return status
}

func gd17VersionStatus(t *testing.T, api *testAPI, versionID int64) string {
	t.Helper()
	var status string
	if err := api.pool.QueryRow(context.Background(),
		`SELECT status FROM version WHERE id = $1`, versionID).Scan(&status); err != nil {
		t.Fatalf("read version status: %v", err)
	}
	return status
}

func gd17OpenRetiredCloseRows(t *testing.T, api *testAPI, versionID int64) int {
	t.Helper()
	var count int
	if err := api.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM work_item
		WHERE version_id = $1 AND type IN ('requirement.close', 'version.close')
		  AND status IN ('open', 'claimed', 'running', 'waiting_human')`, versionID).Scan(&count); err != nil {
		t.Fatalf("count open retired close rows: %v", err)
	}
	return count
}

// TestClosureDeterministicDirectClose is the SC-G10 closure-determinism integration:
// with the Steward removed, the reconciler hooks close the requirement and version
// DIRECTLY through the terminal CAS entries — no requirement.close/version.close
// work item is enqueued and no version_steward agent run exists. Pre-upgrade dead
// close rows are disposed first so they cannot block (existing-row disposal).
func TestClosureDeterministicDirectClose(t *testing.T) {
	api := newTestAPI(t)
	api.login("admin", testSeedAdminPassword)
	_, versionID := api.createTestVersion("G Deterministic Close")
	ctx := context.Background()
	ids := gd17FixtureIDs{
		requirementID: 860001, requirementVersionID: 860002, scenarioID: 860003,
		caseID: 860010, caseVersionID: 860011, waveID: 860101, intentID: 860102, caseRunID: 860103,
	}
	seedGD17ClosableVersion(t, api, versionID, ids)
	seedGD17DeadCloseRows(t, api, versionID, ids.requirementID)

	disposed, err := agentapp.DisposeRetiredCloseWorkItems(ctx, api.workItems)
	if err != nil {
		t.Fatalf("DisposeRetiredCloseWorkItems: %v", err)
	}
	if disposed < 2 {
		t.Fatalf("disposed rows = %d, want >= 2 (the seeded dead close rows)", disposed)
	}
	if open := gd17OpenRetiredCloseRows(t, api, versionID); open != 0 {
		t.Fatalf("open retired close rows after disposal = %d, want 0", open)
	}

	if err := api.closureSvc.EnsureClosures(ctx, versionID); err != nil {
		t.Fatalf("EnsureClosures: %v", err)
	}
	if status := gd17RequirementStatus(t, api, ids.requirementID); status != string(version.RequirementClosed) {
		t.Fatalf("requirement status = %q, want closed (direct reconciler close)", status)
	}

	publishGD17VersionReport(t, api, versionID)
	if err := api.versionCloseSvc.EnsureVersionClose(ctx, versionID); err != nil {
		t.Fatalf("EnsureVersionClose: %v", err)
	}
	if status := gd17VersionStatus(t, api, versionID); status != string(version.StatusCompleted) {
		t.Fatalf("version status = %q, want completed (direct reconciler close)", status)
	}

	// No close work item was enqueued by the direct-close path, and no Steward run exists.
	if open := gd17OpenRetiredCloseRows(t, api, versionID); open != 0 {
		t.Fatalf("direct close enqueued retired close work items = %d, want 0", open)
	}
	var stewardRuns int
	if err := api.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_run WHERE profile_key = 'version_steward'`).Scan(&stewardRuns); err != nil {
		t.Fatalf("count steward runs: %v", err)
	}
	if stewardRuns != 0 {
		t.Fatalf("version_steward agent runs = %d, want 0 (Steward removed)", stewardRuns)
	}

	// Idempotency: a second reconciler pass is a benign no-op.
	if err := api.closureSvc.EnsureClosures(ctx, versionID); err != nil {
		t.Fatalf("second EnsureClosures: %v", err)
	}
	if err := api.versionCloseSvc.EnsureVersionClose(ctx, versionID); err != nil {
		t.Fatalf("second EnsureVersionClose: %v", err)
	}
	if status := gd17VersionStatus(t, api, versionID); status != string(version.StatusCompleted) {
		t.Fatalf("version status after second pass = %q, want completed", status)
	}
}

// TestExistingCloseJobsDisposed is the reviewer-flagged permanent-pending proof
// (SC-G10 I1/M5): an in-flight requirement.close row left by an upgrade has no
// consumer after GD-17, so it counts toward NoBlockingWorkItems and blocks version
// close FOREVER unless disposed. The test shows the block before disposal and the
// clean close after startup disposal terminalizes the dead rows.
func TestExistingCloseJobsDisposed(t *testing.T) {
	api := newTestAPI(t)
	api.login("admin", testSeedAdminPassword)
	_, versionID := api.createTestVersion("G Existing Close Disposal")
	ctx := context.Background()
	ids := gd17FixtureIDs{
		requirementID: 870001, requirementVersionID: 870002, scenarioID: 870003,
		caseID: 870010, caseVersionID: 870011, waveID: 870101, intentID: 870102, caseRunID: 870103,
	}
	seedGD17ClosableVersion(t, api, versionID, ids)

	// Close the requirement and publish the report so the ONLY remaining blocker
	// is the dead close work row.
	if err := api.closureSvc.EnsureClosures(ctx, versionID); err != nil {
		t.Fatalf("EnsureClosures: %v", err)
	}
	if status := gd17RequirementStatus(t, api, ids.requirementID); status != string(version.RequirementClosed) {
		t.Fatalf("requirement status = %q, want closed", status)
	}
	publishGD17VersionReport(t, api, versionID)

	// Seed the pre-upgrade dead rows AFTER the report tasks are terminalized.
	seedGD17DeadCloseRows(t, api, versionID, ids.requirementID)

	// Before disposal the dead requirement.close row blocks version close permanently.
	if err := api.versionCloseSvc.EnsureVersionClose(ctx, versionID); err != nil {
		t.Fatalf("EnsureVersionClose before disposal: %v", err)
	}
	if status := gd17VersionStatus(t, api, versionID); status != string(version.StatusRunning) {
		t.Fatalf("version closed despite a dead blocking close row (status=%q); the permanent-pending risk is not reproduced", status)
	}

	// Startup disposal terminalizes the dead rows.
	disposed, err := agentapp.DisposeRetiredCloseWorkItems(ctx, api.workItems)
	if err != nil {
		t.Fatalf("DisposeRetiredCloseWorkItems: %v", err)
	}
	if disposed < 2 {
		t.Fatalf("disposed rows = %d, want >= 2", disposed)
	}
	var deadStatus string
	var deadAttempts, deadMax int
	if err := api.pool.QueryRow(ctx, `
		SELECT status, attempts, max_attempts FROM work_item
		WHERE version_id = $1 AND type = 'requirement.close'`, versionID).
		Scan(&deadStatus, &deadAttempts, &deadMax); err != nil {
		t.Fatalf("read disposed requirement.close row: %v", err)
	}
	if deadStatus != "failed" || deadAttempts < deadMax {
		t.Fatalf("disposed row = %q attempts=%d/%d, want failed at attempt cap (never re-claimable)",
			deadStatus, deadAttempts, deadMax)
	}

	// Idempotency: a second startup sweep finds no claimable rows and re-disposes
	// nothing (the WHERE guard is the guarantee; this pins it against a real DB).
	again, err := agentapp.DisposeRetiredCloseWorkItems(ctx, api.workItems)
	if err != nil {
		t.Fatalf("second DisposeRetiredCloseWorkItems: %v", err)
	}
	if again != 0 {
		t.Fatalf("second disposal re-disposed %d rows, want 0 (idempotent)", again)
	}

	// After disposal the version closes cleanly.
	if err := api.versionCloseSvc.EnsureVersionClose(ctx, versionID); err != nil {
		t.Fatalf("EnsureVersionClose after disposal: %v", err)
	}
	if status := gd17VersionStatus(t, api, versionID); status != string(version.StatusCompleted) {
		t.Fatalf("version status after disposal = %q, want completed (dead rows no longer block)", status)
	}
}
