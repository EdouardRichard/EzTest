//go:build integration

package server_test

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// seedWaiverFixture provisions the waiver scenario: one clarified requirement
// whose single confirmed case has a BLOCKED latest run (terminal but verified
// nothing, so the requirement can never close on its own), a terminal wave and
// intent, and NO published report. Execution is settled; closure completeness
// and check 8 are not. It mirrors the GD-17 fixture shape with a blocked run.
func seedWaiverFixture(t *testing.T, api *testAPI, versionID int64, ids gd17FixtureIDs) {
	t.Helper()
	ctx := context.Background()
	execSQL := func(statement string, args ...any) {
		t.Helper()
		if _, err := api.pool.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed waiver fixture: %v\n%s", err, statement)
		}
	}
	execSQL(`UPDATE version SET status = 'running' WHERE id = $1`, versionID)
	execSQL(`INSERT INTO requirement (id, version_id, current_version_id, status)
		VALUES ($1, $2, $3, 'clarified')`, ids.requirementID, versionID, ids.requirementVersionID)
	execSQL(`INSERT INTO requirement_version (id, requirement_id, revision, title, content_hash,
		user_story, sut_description, acceptance_criteria)
		VALUES ($1, $2, 1, 'waiver requirement', $3,
		$4::jsonb, 'the order center', $5::jsonb)`,
		ids.requirementVersionID, ids.requirementID, strings.Repeat("a", 64),
		`{"as_a":"tester","i_want":"waive the unverified remainder","so_that":"the version can ship"}`,
		`[{"id":"AC-1","given":"a blocked case","when":"the admin waives","then":"the version closes unverified"}]`)
	execSQL(`INSERT INTO case_scenario (id, version_id, requirement_id, requirement_version_id, name)
		VALUES ($1, $2, $3, $4, 'waiver scenario')`,
		ids.scenarioID, versionID, ids.requirementID, ids.requirementVersionID)
	execSQL(`INSERT INTO case_entity (id, scenario_id, current_version_id, status)
		VALUES ($1, $2, $3, 'confirmed')`, ids.caseID, ids.scenarioID, ids.caseVersionID)
	execSQL(`INSERT INTO case_version (id, case_id, revision, title, description, priority,
		sut_description, covered_acs, status, content_hash)
		VALUES ($1, $2, 1, 'waiver blocked case', '', 'P2', 'the order center', $3::jsonb, 'confirmed', $4)`,
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
		"waiver-seed-intent-"+strconv.FormatInt(ids.intentID, 10))
	execSQL(`INSERT INTO case_run (id, wave_id, intent_id, case_version_id, status, attempts,
		failure_class, summary, started_at, finished_at)
		VALUES ($1, $2, $3, $4, 'blocked', 1, 'environment', '{}'::jsonb, now(), now())`,
		ids.caseRunID, ids.waveID, ids.intentID, ids.caseVersionID)
}

func waiverFixtureIDs(base int64) gd17FixtureIDs {
	return gd17FixtureIDs{
		requirementID: base + 1, requirementVersionID: base + 2, scenarioID: base + 3,
		caseID: base + 10, caseVersionID: base + 11, waveID: base + 101, intentID: base + 102, caseRunID: base + 103,
	}
}

func waiverVersionProjection(t *testing.T, api *testAPI, versionID int64) (status, closureKind string, unverified bool) {
	t.Helper()
	if err := api.pool.QueryRow(context.Background(),
		`SELECT status, closure_kind, unverified FROM version WHERE id = $1`, versionID).
		Scan(&status, &closureKind, &unverified); err != nil {
		t.Fatalf("read version waiver projection: %v", err)
	}
	return status, closureKind, unverified
}

func waiverRequirementWaived(t *testing.T, api *testAPI, requirementID int64) bool {
	t.Helper()
	var waived bool
	if err := api.pool.QueryRow(context.Background(),
		`SELECT waived FROM requirement WHERE id = $1`, requirementID).Scan(&waived); err != nil {
		t.Fatalf("read requirement waived: %v", err)
	}
	return waived
}

// seedWaiverRequirementWithRun adds one more clarified requirement with a single
// confirmed case whose latest run has the given status, on the fixture's terminal
// wave with its own terminal intent (case_run UNIQUE(wave_id,intent_id)). It
// returns the requirement ID. A passed run makes the requirement normally
// closable; a blocked run keeps it open without a waiver.
func seedWaiverRequirementWithRun(t *testing.T, api *testAPI, versionID, waveID, base int64, runStatus string) int64 {
	t.Helper()
	ctx := context.Background()
	rid, rvid, sid, cid, cvid := base+1, base+2, base+3, base+10, base+11
	intentID, runID := base+102, base+103
	execSQL := func(statement string, args ...any) {
		t.Helper()
		if _, err := api.pool.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed requirement with run: %v\n%s", err, statement)
		}
	}
	execSQL(`INSERT INTO requirement (id, version_id, current_version_id, status)
		VALUES ($1, $2, $3, 'clarified')`, rid, versionID, rvid)
	execSQL(`INSERT INTO requirement_version (id, requirement_id, revision, title, content_hash,
		user_story, sut_description, acceptance_criteria)
		VALUES ($1, $2, 1, 'extra requirement', $3,
		$4::jsonb, 'the order center', $5::jsonb)`,
		rvid, rid, strings.Repeat("a", 64),
		`{"as_a":"tester","i_want":"an extra requirement","so_that":"the lifecycle is pinned"}`,
		`[{"id":"AC-1","given":"a case","when":"the reconciler ticks","then":"closure is evaluated"}]`)
	execSQL(`INSERT INTO case_scenario (id, version_id, requirement_id, requirement_version_id, name)
		VALUES ($1, $2, $3, $4, 'extra scenario')`, sid, versionID, rid, rvid)
	execSQL(`INSERT INTO case_entity (id, scenario_id, current_version_id, status)
		VALUES ($1, $2, $3, 'confirmed')`, cid, sid, cvid)
	execSQL(`INSERT INTO case_version (id, case_id, revision, title, description, priority,
		sut_description, covered_acs, status, content_hash)
		VALUES ($1, $2, 1, 'extra case', '', 'P2', 'the order center', '["AC-1"]'::jsonb, 'confirmed', $3)`,
		cvid, cid, strings.Repeat("b", 64))
	execSQL(`INSERT INTO case_step (id, case_version_id, seq, action, target_description)
		VALUES ($1, $2, 1, 'open the page', 'the result page')`, cvid+100000, cvid)
	execSQL(`INSERT INTO case_assertion (id, case_version_id, step_seq, kind, target, expected)
		VALUES ($1, $2, 1, 'ui', '#result', 'ok')`, cvid+200000, cvid)
	execSQL(`INSERT INTO execution_intent (id, version_id, case_version_id, sut_ids,
		environment_fingerprint, purpose, status, dedupe_key)
		VALUES ($1, $2, $3, '[]'::jsonb, $4, 'initial', 'done', $5)`,
		intentID, versionID, cvid, strings.Repeat("c", 64),
		"waiver-extra-intent-"+strconv.FormatInt(intentID, 10))
	execSQL(`INSERT INTO case_run (id, wave_id, intent_id, case_version_id, status, attempts,
		failure_class, summary, started_at, finished_at)
		VALUES ($1, $2, $3, $4, $5, 1, '', '{}'::jsonb, now(), now())`,
		runID, waveID, intentID, cvid, runStatus)
	return rid
}

// TestWaiveCloseOverridesCheck8AndRegeneratesUnverified is the SC-G11/R-G8
// reachability integration: with requirements open and NO current report the
// full close predicate rejects, but the admin waive closes through the single
// Close terminal entry (closure_kind=waived + unverified) AND synchronously
// regenerates + publishes the version report — breaking the dead chain where a
// completed version is excluded from the P4 ensure pass and its report would
// never regenerate.
func TestWaiveCloseOverridesCheck8AndRegeneratesUnverified(t *testing.T) {
	api := newTestAPI(t)
	api.login("admin", testSeedAdminPassword)
	versionIDStr, versionID := api.createTestVersion("G Waive Close")
	ids := waiverFixtureIDs(880000)
	seedWaiverFixture(t, api, versionID, ids)
	ctx := context.Background()

	// Discriminating: the deterministic full-predicate close must NOT close this
	// version (requirements open + no published report).
	if err := api.versionCloseSvc.EnsureVersionClose(ctx, versionID); err != nil {
		t.Fatalf("EnsureVersionClose: %v", err)
	}
	if status, _, _ := waiverVersionProjection(t, api, versionID); status != version.StatusRunning {
		t.Fatalf("version closed without waiver (status=%q); the fixture does not reproduce the blocked close", status)
	}

	result := api.data(api.do(http.MethodPost, "/api/versions/"+versionIDStr+"/waive-close",
		map[string]any{"reason": "剩余验收标准经所有者批准降级，本版本豁免关闭"}, http.StatusOK))
	if result["closure_kind"] != version.ClosureKindWaived || result["unverified"] != true {
		t.Fatalf("waive-close response = %#v, want closure_kind waived + unverified", result)
	}
	if result["status"] != version.StatusCompleted {
		t.Fatalf("waive-close response status = %#v, want completed", result["status"])
	}
	reportIDStr, _ := result["report_id"].(string)
	if reportIDStr == "" || result["report_revision"] == nil {
		t.Fatalf("waive-close response = %#v, want a synchronously published report id + revision", result)
	}

	status, closureKind, unverified := waiverVersionProjection(t, api, versionID)
	if status != version.StatusCompleted || closureKind != version.ClosureKindWaived || !unverified {
		t.Fatalf("version projection = %q/%q/%v, want completed/waived/true", status, closureKind, unverified)
	}
	var closeReason string
	if err := api.pool.QueryRow(ctx, `SELECT close_reason FROM version WHERE id = $1`, versionID).Scan(&closeReason); err != nil {
		t.Fatalf("read close reason: %v", err)
	}
	if closeReason != "剩余验收标准经所有者批准降级，本版本豁免关闭" {
		t.Fatalf("close_reason = %q, want the waiver reason", closeReason)
	}

	// The requirement stays clarified: waive_close overrides the version-level
	// RequirementsClosed check; it does not close requirements.
	if got := gd17RequirementStatus(t, api, ids.requirementID); got != string(version.RequirementClarified) {
		t.Fatalf("requirement status = %q, want clarified (waive_close does not close requirements)", got)
	}

	// The unverified report EXISTS and is published immediately after the waive,
	// without any reconciler ensure pass (the version is completed and excluded
	// from P4). This is the R-G8 dead-chain break.
	kind := delivery.ReportKindVersion
	reports, err := api.reportSvc.List(ctx, versionID, &kind)
	if err != nil {
		t.Fatalf("list reports: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("version reports = %d, want 1 synchronously regenerated report", len(reports))
	}
	report := reports[0]
	if report.Status != delivery.ReportStatusPublished || report.CurrentRevisionID == nil {
		t.Fatalf("report = %+v, want published with a current revision", report)
	}
	reportID, _ := strconv.ParseInt(reportIDStr, 10, 64)
	if report.ID != reportID {
		t.Fatalf("response report_id = %d, actual report = %d", reportID, report.ID)
	}

	// Waiver data persisted: the append-only waiver fact carries reason +
	// operator for the W3 report read model (T8/T9/T10 render it; T14 verifies
	// the rendered content).
	facts, err := api.factSvc.Current(ctx, versionID, version.FactKindWaiver)
	if err != nil {
		t.Fatalf("read waiver facts: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("waiver facts = %d, want 1", len(facts))
	}
	if facts[0].Payload["reason"] != closeReason {
		t.Fatalf("waiver fact payload = %#v, want the close reason", facts[0].Payload)
	}
	var adminID int64
	if err := api.pool.QueryRow(ctx, `SELECT id FROM app_user WHERE username = 'admin'`).Scan(&adminID); err != nil {
		t.Fatalf("read admin id: %v", err)
	}
	if facts[0].Payload["actor_id"] != strconv.FormatInt(adminID, 10) {
		t.Fatalf("waiver fact operator = %#v, want admin %d", facts[0].Payload["actor_id"], adminID)
	}

	// Non-silent: version.waived event + audit, and the version.closed event
	// itself discloses closure_kind/unverified.
	var waivedEvents, closedDisclosures int
	if err := api.pool.QueryRow(ctx, `
		SELECT count(*) FROM event WHERE channel = $1 AND type = 'version.waived'`,
		"version:"+versionIDStr).Scan(&waivedEvents); err != nil {
		t.Fatalf("count waiver events: %v", err)
	}
	if waivedEvents == 0 {
		t.Fatal("no version.waived event published")
	}
	if err := api.pool.QueryRow(ctx, `
		SELECT count(*) FROM event
		WHERE channel = $1 AND type = 'version.closed'
		  AND envelope->'payload'->>'closure_kind' = $2
		  AND (envelope->'payload'->>'unverified')::boolean = true`,
		"version:"+versionIDStr, version.ClosureKindWaived).Scan(&closedDisclosures); err != nil {
		t.Fatalf("count closed disclosures: %v", err)
	}
	if closedDisclosures == 0 {
		t.Fatal("version.closed event does not disclose closure_kind=waived + unverified")
	}
	var auditCount int
	if err := api.pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_log WHERE action = 'version.waived' AND target_id = $1 AND actor_id = $2`,
		versionIDStr, adminID).Scan(&auditCount); err != nil {
		t.Fatalf("count waiver audit: %v", err)
	}
	if auditCount == 0 {
		t.Fatal("no version.waived audit entry with the operator")
	}
}

// TestWaiveCloseRequiresExecutionSettled pins the R-G6 negative space over the
// real readers: an active wave, an active intent, an in-flight case run or an
// open human takeover each reject the waive with 409 and leave the version
// running; once every blocker settles the same waive succeeds.
func TestWaiveCloseRequiresExecutionSettled(t *testing.T) {
	api := newTestAPI(t)
	api.login("admin", testSeedAdminPassword)
	versionIDStr, versionID := api.createTestVersion("G Waive Settled")
	ids := waiverFixtureIDs(881000)
	seedWaiverFixture(t, api, versionID, ids)
	ctx := context.Background()

	// A terminal intent dedicated to the in-flight-run blocker (case_run has a
	// UNIQUE(wave_id, intent_id), so the extra run needs its own intent). It is
	// terminal, so it never blocks on its own — only the running run does.
	if _, err := api.pool.Exec(ctx, `
		INSERT INTO execution_intent (id, version_id, case_version_id, sut_ids,
			environment_fingerprint, purpose, status, dedupe_key)
		VALUES (881205, $1, $2, '[]'::jsonb, $3, 'manual', 'done', 'waiver-blocker-run-intent')`,
		versionID, ids.caseVersionID, strings.Repeat("d", 64)); err != nil {
		t.Fatalf("seed in-flight-run intent: %v", err)
	}

	blockers := []struct {
		name    string
		seed    string
		args    []any
		cleanup string
	}{
		{
			name: "active wave",
			seed: `INSERT INTO run_wave (id, version_id, status, trigger, counts, started_at)
				VALUES (881201, $1, 'running', 'auto', '{}'::jsonb, now())`,
			args:    []any{versionID},
			cleanup: `DELETE FROM run_wave WHERE id = 881201`,
		},
		{
			name: "active intent",
			seed: `INSERT INTO execution_intent (id, version_id, case_version_id, sut_ids,
				environment_fingerprint, purpose, status, dedupe_key)
				VALUES (881202, $1, $2, '[]'::jsonb, $3, 'manual', 'running', 'waiver-blocker-intent')`,
			args:    []any{versionID, ids.caseVersionID, strings.Repeat("d", 64)},
			cleanup: `DELETE FROM execution_intent WHERE id = 881202`,
		},
		{
			name: "in-flight case run",
			seed: `INSERT INTO case_run (id, wave_id, intent_id, case_version_id, status, attempts, summary, started_at)
				VALUES (881203, $1, 881205, $2, 'running', 1, '{}'::jsonb, now())`,
			args:    []any{ids.waveID, ids.caseVersionID},
			cleanup: `DELETE FROM case_run WHERE id = 881203`,
		},
		{
			name: "open human takeover",
			seed: `INSERT INTO human_request (id, version_id, case_run_id, kind, payload, status, expires_at)
				VALUES (881204, $1, $2, 'login_takeover', '{}'::jsonb, 'pending', now() + interval '30 minutes')`,
			args:    []any{versionID, ids.caseRunID},
			cleanup: `DELETE FROM human_request WHERE id = 881204`,
		},
		{
			name: "blocking work item",
			seed: `INSERT INTO work_item (id, version_id, team, type, status, priority, dedupe_key, payload, max_attempts)
				VALUES (881206, $1, 'delivery', 'report.version', 'open', 0, 'waiver-blocker-work', '{}'::jsonb, 3)`,
			args:    []any{versionID},
			cleanup: `DELETE FROM work_item WHERE id = 881206`,
		},
	}
	for _, blocker := range blockers {
		t.Run(blocker.name, func(t *testing.T) {
			if _, err := api.pool.Exec(ctx, blocker.seed, blocker.args...); err != nil {
				t.Fatalf("seed blocker: %v", err)
			}
			api.do(http.MethodPost, "/api/versions/"+versionIDStr+"/waive-close",
				map[string]any{"reason": "ship it"}, http.StatusConflict)
			status, closureKind, unverified := waiverVersionProjection(t, api, versionID)
			if status != version.StatusRunning || closureKind != version.ClosureKindNormal || unverified {
				t.Fatalf("version after rejected waive = %q/%q/%v, want running/normal/false", status, closureKind, unverified)
			}
			if _, err := api.pool.Exec(ctx, blocker.cleanup); err != nil {
				t.Fatalf("cleanup blocker: %v", err)
			}
		})
	}

	// Running guard against the real Close CAS: a non-running version is a 409
	// and the waive changes nothing.
	t.Run("not running", func(t *testing.T) {
		if _, err := api.pool.Exec(ctx, `UPDATE version SET status = 'initial' WHERE id = $1`, versionID); err != nil {
			t.Fatalf("seed non-running version: %v", err)
		}
		api.do(http.MethodPost, "/api/versions/"+versionIDStr+"/waive-close",
			map[string]any{"reason": "ship it"}, http.StatusConflict)
		status, closureKind, unverified := waiverVersionProjection(t, api, versionID)
		if status != version.StatusInitial || closureKind != version.ClosureKindNormal || unverified {
			t.Fatalf("version after rejected waive = %q/%q/%v, want initial/normal/false", status, closureKind, unverified)
		}
		if _, err := api.pool.Exec(ctx, `UPDATE version SET status = 'running' WHERE id = $1`, versionID); err != nil {
			t.Fatalf("restore running version: %v", err)
		}
	})

	// Every blocker settled: the same waive now succeeds (reachability).
	api.do(http.MethodPost, "/api/versions/"+versionIDStr+"/waive-close",
		map[string]any{"reason": "ship it"}, http.StatusOK)
	if status, closureKind, _ := waiverVersionProjection(t, api, versionID); status != version.StatusCompleted || closureKind != version.ClosureKindWaived {
		t.Fatalf("version after settled waive = %q/%q, want completed/waived", status, closureKind)
	}
}

// TestWaiveCloseAdminOnly pins the Q7=A functional gate at the HTTP surface:
// unauthenticated is 401, a normal user is 403, and only the admin can waive.
func TestWaiveCloseAdminOnly(t *testing.T) {
	api := newTestAPI(t)
	api.login("admin", testSeedAdminPassword)
	versionIDStr, versionID := api.createTestVersion("G Waive Admin")
	ids := waiverFixtureIDs(882000)
	seedWaiverFixture(t, api, versionID, ids)

	api.data(api.do(http.MethodPost, "/api/admin/users",
		map[string]any{"username": "waiver-user", "display_name": "Waiver User", "password": "waiver-user-pass"}, http.StatusCreated))

	api.token = ""
	api.do(http.MethodPost, "/api/versions/"+versionIDStr+"/waive-close",
		map[string]any{"reason": "ship it"}, http.StatusUnauthorized)

	api.login("waiver-user", "waiver-user-pass")
	api.do(http.MethodPost, "/api/versions/"+versionIDStr+"/waive-close",
		map[string]any{"reason": "ship it"}, http.StatusForbidden)
	if status, _, _ := waiverVersionProjection(t, api, versionID); status != version.StatusRunning {
		t.Fatalf("non-admin waive changed the version (status=%q)", status)
	}

	api.login("admin", testSeedAdminPassword)
	api.do(http.MethodPost, "/api/versions/"+versionIDStr+"/waive-close",
		map[string]any{"reason": "ship it"}, http.StatusOK)
}

// TestReopenClearsWaiver pins the R-G8 lifecycle: reopen resets
// closure_kind/unverified AND clears every requirement waiver, so the
// reconciler cannot re-close the version through a stale waiver.
func TestReopenClearsWaiver(t *testing.T) {
	api := newTestAPI(t)
	api.login("admin", testSeedAdminPassword)
	versionIDStr, versionID := api.createTestVersion("G Waive Reopen")
	ids := waiverFixtureIDs(883000)
	seedWaiverFixture(t, api, versionID, ids)
	ctx := context.Background()

	// Mark the requirement waived but do NOT run the closure reconciler before
	// the version waive: at close time the requirement is clarified+waived —
	// exactly the state that would re-close after reopen if the waiver survived.
	api.do(http.MethodPost, "/api/versions/"+versionIDStr+"/requirements/"+strconv.FormatInt(ids.requirementID, 10)+"/waive",
		map[string]any{"reason": "AC-1 descoped"}, http.StatusOK)
	if !waiverRequirementWaived(t, api, ids.requirementID) {
		t.Fatal("requirement not marked waived")
	}
	api.do(http.MethodPost, "/api/versions/"+versionIDStr+"/waive-close",
		map[string]any{"reason": "ship it"}, http.StatusOK)

	api.do(http.MethodPost, "/api/versions/"+versionIDStr+"/reopen",
		map[string]any{"reason": "new evidence arrived"}, http.StatusOK)
	status, closureKind, unverified := waiverVersionProjection(t, api, versionID)
	if status != version.StatusRunning || closureKind != version.ClosureKindNormal || unverified {
		t.Fatalf("version after reopen = %q/%q/%v, want running/normal/false", status, closureKind, unverified)
	}
	if waiverRequirementWaived(t, api, ids.requirementID) {
		t.Fatal("requirement waiver survived the version reopen (stale waiver would re-close)")
	}

	// The reconciler must not re-close via the stale waiver: the blocked run
	// keeps the requirement open now that the waiver is gone.
	if err := api.closureSvc.EnsureClosures(ctx, versionID); err != nil {
		t.Fatalf("EnsureClosures: %v", err)
	}
	if got := gd17RequirementStatus(t, api, ids.requirementID); got != string(version.RequirementClarified) {
		t.Fatalf("requirement re-closed via stale waiver (status=%q)", got)
	}

	// Even with a freshly regenerated current report, the version stays running
	// because RequirementsClosed fails again.
	kind := delivery.ReportKindVersion
	reports, err := api.reportSvc.List(ctx, versionID, &kind)
	if err != nil || len(reports) == 0 {
		t.Fatalf("list reports: %v (n=%d)", err, len(reports))
	}
	revision, err := api.reportSvc.Regenerate(ctx, versionID, reports[0].ID)
	if err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
	if _, err := api.reportSvc.Publish(ctx, reports[0].ID, revision, "reopen verification"); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := api.versionCloseSvc.EnsureVersionClose(ctx, versionID); err != nil {
		t.Fatalf("EnsureVersionClose: %v", err)
	}
	if status, _, _ := waiverVersionProjection(t, api, versionID); status != version.StatusRunning {
		t.Fatalf("version re-closed after reopen (status=%q); the stale-waiver lifecycle is broken", status)
	}
}

// TestWaiveRequirement pins the requirement-level waiver: admin marks the
// requirement Waived (fact + event + audit), the deterministic reconciler then
// closes it through the normal Close entry with check_result.waived=true, and a
// requirement whose execution is still in flight is NOT closed by the waiver.
func TestWaiveRequirement(t *testing.T) {
	api := newTestAPI(t)
	api.login("admin", testSeedAdminPassword)
	versionIDStr, versionID := api.createTestVersion("G Waive Requirement")
	ids := waiverFixtureIDs(884000)
	seedWaiverFixture(t, api, versionID, ids)
	ctx := context.Background()

	// Before the waiver the blocked run keeps the requirement open.
	if err := api.closureSvc.EnsureClosures(ctx, versionID); err != nil {
		t.Fatalf("EnsureClosures before waiver: %v", err)
	}
	if got := gd17RequirementStatus(t, api, ids.requirementID); got != string(version.RequirementClarified) {
		t.Fatalf("requirement closed without waiver (status=%q)", got)
	}

	// Non-admin cannot waive.
	api.data(api.do(http.MethodPost, "/api/admin/users",
		map[string]any{"username": "waiver-user2", "display_name": "Waiver User2", "password": "waiver-user2-pass"}, http.StatusCreated))
	api.login("waiver-user2", "waiver-user2-pass")
	api.do(http.MethodPost, "/api/versions/"+versionIDStr+"/requirements/"+strconv.FormatInt(ids.requirementID, 10)+"/waive",
		map[string]any{"reason": "AC-1 descoped"}, http.StatusForbidden)
	api.login("admin", testSeedAdminPassword)

	result := api.data(api.do(http.MethodPost, "/api/versions/"+versionIDStr+"/requirements/"+strconv.FormatInt(ids.requirementID, 10)+"/waive",
		map[string]any{"reason": "AC-1 descoped by owner"}, http.StatusOK))
	if result["waived"] != true {
		t.Fatalf("waive response = %#v, want waived true", result)
	}
	if !waiverRequirementWaived(t, api, ids.requirementID) {
		t.Fatal("requirement.waived not persisted")
	}

	// The deterministic reconciler closes the waived requirement through the
	// normal terminal entry; the closure round discloses the waiver.
	if err := api.closureSvc.EnsureClosures(ctx, versionID); err != nil {
		t.Fatalf("EnsureClosures after waiver: %v", err)
	}
	if got := gd17RequirementStatus(t, api, ids.requirementID); got != string(version.RequirementClosed) {
		t.Fatalf("waived requirement status = %q, want closed", got)
	}
	var checkWaived bool
	if err := api.pool.QueryRow(ctx, `
		SELECT (check_result->>'waived')::boolean FROM requirement_closure
		WHERE requirement_id = $1 ORDER BY close_round DESC LIMIT 1`, ids.requirementID).Scan(&checkWaived); err != nil {
		t.Fatalf("read closure check_result: %v", err)
	}
	if !checkWaived {
		t.Fatal("closure check_result.waived = false, want true")
	}

	// Waiver fact + event + audit.
	facts, err := api.factSvc.Current(ctx, versionID, version.FactKindWaiver)
	if err != nil {
		t.Fatalf("read waiver facts: %v", err)
	}
	if len(facts) != 1 || facts[0].Key != "requirement:"+strconv.FormatInt(ids.requirementID, 10) {
		t.Fatalf("waiver facts = %#v, want one requirement-scoped fact", facts)
	}
	if facts[0].Payload["reason"] != "AC-1 descoped by owner" {
		t.Fatalf("waiver fact payload = %#v", facts[0].Payload)
	}
	var eventCount, auditCount int
	if err := api.pool.QueryRow(ctx, `
		SELECT count(*) FROM event WHERE channel = $1 AND type = 'requirement.waived'`,
		"version:"+versionIDStr).Scan(&eventCount); err != nil {
		t.Fatalf("count waiver events: %v", err)
	}
	if eventCount == 0 {
		t.Fatal("no requirement.waived event")
	}
	if err := api.pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_log WHERE action = 'requirement.waived' AND target_id = $1`,
		strconv.FormatInt(ids.requirementID, 10)).Scan(&auditCount); err != nil {
		t.Fatalf("count waiver audit: %v", err)
	}
	if auditCount == 0 {
		t.Fatal("no requirement.waived audit entry")
	}

	// R-G8 lifecycle at the requirement level: reopening the closed waived
	// requirement (closed -> draft) clears its waiver, so a later re-confirm can
	// never be auto-closed through a stale waiver.
	api.do(http.MethodPost, "/api/versions/"+versionIDStr+"/requirements/"+strconv.FormatInt(ids.requirementID, 10)+"/reopen",
		map[string]any{"reason": "re-scope the requirement"}, http.StatusOK)
	if got := gd17RequirementStatus(t, api, ids.requirementID); got != string(version.RequirementDraft) {
		t.Fatalf("reopened requirement status = %q, want draft", got)
	}
	if waiverRequirementWaived(t, api, ids.requirementID) {
		t.Fatal("requirement waiver survived the requirement reopen (stale waiver would re-close)")
	}

	// A waived requirement whose execution is still in flight stays open: the
	// waiver never overrides the execution-settled checks.
	{
		rid, rvid, sid, cid, cvid := int64(884101), int64(884102), int64(884103), int64(884110), int64(884111)
		execSQL := func(statement string, args ...any) {
			t.Helper()
			if _, err := api.pool.Exec(ctx, statement, args...); err != nil {
				t.Fatalf("seed second requirement: %v\n%s", err, statement)
			}
		}
		execSQL(`INSERT INTO requirement (id, version_id, current_version_id, status, waived)
			VALUES ($1, $2, $3, 'clarified', true)`, rid, versionID, rvid)
		execSQL(`INSERT INTO requirement_version (id, requirement_id, revision, title, content_hash,
			user_story, sut_description, acceptance_criteria)
			VALUES ($1, $2, 1, 'in-flight waived requirement', $3,
			$4::jsonb, 'the order center', $5::jsonb)`,
			rvid, rid, strings.Repeat("e", 64),
			`{"as_a":"tester","i_want":"a second requirement","so_that":"the waiver boundary is pinned"}`,
			`[{"id":"AC-1","given":"a running case","when":"the reconciler ticks","then":"the requirement stays open"}]`)
		execSQL(`INSERT INTO case_scenario (id, version_id, requirement_id, requirement_version_id, name)
			VALUES ($1, $2, $3, $4, 'in-flight scenario')`, sid, versionID, rid, rvid)
		execSQL(`INSERT INTO case_entity (id, scenario_id, current_version_id, status)
			VALUES ($1, $2, $3, 'confirmed')`, cid, sid, cvid)
		execSQL(`INSERT INTO case_version (id, case_id, revision, title, description, priority,
			sut_description, covered_acs, status, content_hash)
			VALUES ($1, $2, 1, 'in-flight case', '', 'P2', 'the order center', '["AC-1"]'::jsonb, 'confirmed', $3)`,
			cvid, cid, strings.Repeat("f", 64))
		execSQL(`INSERT INTO case_step (id, case_version_id, seq, action, target_description)
			VALUES ($1, $2, 1, 'open the page', 'the result page')`, cvid+100000, cvid)
		execSQL(`INSERT INTO case_assertion (id, case_version_id, step_seq, kind, target, expected)
			VALUES ($1, $2, 1, 'ui', '#result', 'ok')`, cvid+200000, cvid)
		// Dedicated terminal intent: case_run UNIQUE(wave_id, intent_id) forbids
		// reusing the fixture's intent for a second run.
		execSQL(`INSERT INTO execution_intent (id, version_id, case_version_id, sut_ids,
			environment_fingerprint, purpose, status, dedupe_key)
			VALUES (884205, $1, $2, '[]'::jsonb, $3, 'manual', 'done', 'waiver-req2-run-intent')`,
			versionID, cvid, strings.Repeat("9", 64))
		execSQL(`INSERT INTO case_run (id, wave_id, intent_id, case_version_id, status, attempts, summary, started_at)
			VALUES ($1, $2, 884205, $3, 'running', 1, '{}'::jsonb, now())`,
			cvid+300000, ids.waveID, cvid)
	}
	if err := api.closureSvc.EnsureClosures(ctx, versionID); err != nil {
		t.Fatalf("EnsureClosures with in-flight waived requirement: %v", err)
	}
	if got := gd17RequirementStatus(t, api, 884101); got != string(version.RequirementClarified) {
		t.Fatalf("in-flight waived requirement closed (status=%q); the waiver overrode execution settlement", got)
	}
}

// TestWaivedRequirementClosesViaReconciler pins the reconciler leg independently
// of the waive API: a requirement seeded waived=true with a blocked latest run
// closes through EnsureClosures (the waiver override is read from the persisted
// column, not from an in-memory flag).
func TestWaivedRequirementClosesViaReconciler(t *testing.T) {
	api := newTestAPI(t)
	api.login("admin", testSeedAdminPassword)
	_, versionID := api.createTestVersion("G Waived Reconciler")
	ids := waiverFixtureIDs(885000)
	seedWaiverFixture(t, api, versionID, ids)
	ctx := context.Background()
	if _, err := api.pool.Exec(ctx, `UPDATE requirement SET waived = true WHERE id = $1`, ids.requirementID); err != nil {
		t.Fatalf("seed waived flag: %v", err)
	}
	if err := api.closureSvc.EnsureClosures(ctx, versionID); err != nil {
		t.Fatalf("EnsureClosures: %v", err)
	}
	if got := gd17RequirementStatus(t, api, ids.requirementID); got != string(version.RequirementClosed) {
		t.Fatalf("waived requirement status = %q, want closed (persisted waiver must drive the reconciler)", got)
	}
}

// TestReopenUndoesWaivedClosureNoLaundering is the R-G8a discriminating
// lifecycle test: a requirement closed VIA WAIVER is reopened closed->clarified
// when the VERSION reopens (the waived closure is undone, not just the flag
// cleared), so the reconciler cannot re-close the version as a fully-verified
// normal close. A requirement closed NORMALLY stays closed (its closure was
// legitimate). Without the undo, the waived closure survives the version reopen
// and EnsureVersionClose re-closes closure_kind=normal/unverified=false —
// laundering the waiver.
func TestReopenUndoesWaivedClosureNoLaundering(t *testing.T) {
	api := newTestAPI(t)
	api.login("admin", testSeedAdminPassword)
	versionIDStr, versionID := api.createTestVersion("G Waive Laundering")
	ids := waiverFixtureIDs(886000)
	seedWaiverFixture(t, api, versionID, ids)
	// A second requirement with a PASSED run closes NORMALLY (control: it must
	// survive the version reopen untouched).
	normalReq := seedWaiverRequirementWithRun(t, api, versionID, ids.waveID, 886100, "passed")
	ctx := context.Background()

	// Waive the blocked requirement, then let the reconciler close BOTH
	// requirements (the waived one via override, the passed one normally).
	api.do(http.MethodPost, "/api/versions/"+versionIDStr+"/requirements/"+strconv.FormatInt(ids.requirementID, 10)+"/waive",
		map[string]any{"reason": "AC-1 descoped"}, http.StatusOK)
	if err := api.closureSvc.EnsureClosures(ctx, versionID); err != nil {
		t.Fatalf("EnsureClosures: %v", err)
	}
	if got := gd17RequirementStatus(t, api, ids.requirementID); got != string(version.RequirementClosed) {
		t.Fatalf("waived requirement status = %q, want closed", got)
	}
	if got := gd17RequirementStatus(t, api, normalReq); got != string(version.RequirementClosed) {
		t.Fatalf("normal requirement status = %q, want closed", got)
	}

	// Waive-close the version (no report yet, so the normal close is blocked).
	api.do(http.MethodPost, "/api/versions/"+versionIDStr+"/waive-close",
		map[string]any{"reason": "ship it"}, http.StatusOK)

	// Reopen the version: the waived closure must be UNDONE (closed->clarified),
	// the normal closure must survive.
	api.do(http.MethodPost, "/api/versions/"+versionIDStr+"/reopen",
		map[string]any{"reason": "re-verify the waived scope"}, http.StatusOK)
	if got := gd17RequirementStatus(t, api, ids.requirementID); got != string(version.RequirementClarified) {
		t.Fatalf("waiver-closed requirement status after version reopen = %q, want clarified (R-G8a undo); the waiver would be laundered", got)
	}
	if waiverRequirementWaived(t, api, ids.requirementID) {
		t.Fatal("waiver flag survived the version reopen")
	}
	if got := gd17RequirementStatus(t, api, normalReq); got != string(version.RequirementClosed) {
		t.Fatalf("normally-closed requirement status after version reopen = %q, want closed (legitimate closure must survive)", got)
	}

	// The reconciler cannot re-close the undone requirement (blocked run, no
	// waiver) and cannot re-close the version even with a current report.
	if err := api.closureSvc.EnsureClosures(ctx, versionID); err != nil {
		t.Fatalf("EnsureClosures after reopen: %v", err)
	}
	if got := gd17RequirementStatus(t, api, ids.requirementID); got != string(version.RequirementClarified) {
		t.Fatalf("undone requirement re-closed via stale waiver (status=%q)", got)
	}
	kind := delivery.ReportKindVersion
	reports, err := api.reportSvc.List(ctx, versionID, &kind)
	if err != nil || len(reports) == 0 {
		t.Fatalf("list reports: %v (n=%d)", err, len(reports))
	}
	revision, err := api.reportSvc.Regenerate(ctx, versionID, reports[0].ID)
	if err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
	if _, err := api.reportSvc.Publish(ctx, reports[0].ID, revision, "re-verification"); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := api.versionCloseSvc.EnsureVersionClose(ctx, versionID); err != nil {
		t.Fatalf("EnsureVersionClose: %v", err)
	}
	status, closureKind, unverified := waiverVersionProjection(t, api, versionID)
	if status != version.StatusRunning || closureKind != version.ClosureKindNormal || unverified {
		t.Fatalf("version after reopen+reconcile = %q/%q/%v, want running/normal/false (the waived close was laundered into a normal re-close)", status, closureKind, unverified)
	}
}

// TestRepeatWaiverEmitsEventAndAudit pins GD-06 accountability over the real
// fact store: a repeat waiver with an IDENTICAL reason (whose fact may dedupe on
// content hash) still emits its event + audit row — fact-log idempotency must
// never suppress the audit of a real admin transition.
func TestRepeatWaiverEmitsEventAndAudit(t *testing.T) {
	api := newTestAPI(t)
	api.login("admin", testSeedAdminPassword)
	versionIDStr, versionID := api.createTestVersion("G Repeat Waiver")
	ids := waiverFixtureIDs(887000)
	seedWaiverFixture(t, api, versionID, ids)
	ctx := context.Background()
	reqPath := "/api/versions/" + versionIDStr + "/requirements/" + strconv.FormatInt(ids.requirementID, 10)

	// Requirement level: waive -> reconciler closes -> reopen -> re-confirm ->
	// re-waive with the SAME reason.
	api.do(http.MethodPost, reqPath+"/waive", map[string]any{"reason": "same reason"}, http.StatusOK)
	if err := api.closureSvc.EnsureClosures(ctx, versionID); err != nil {
		t.Fatalf("EnsureClosures: %v", err)
	}
	api.do(http.MethodPost, reqPath+"/reopen", map[string]any{"reason": "re-scope"}, http.StatusOK)
	if _, err := api.pool.Exec(ctx, `UPDATE requirement SET status = 'clarified' WHERE id = $1`, ids.requirementID); err != nil {
		t.Fatalf("re-confirm requirement: %v", err)
	}
	api.do(http.MethodPost, reqPath+"/waive", map[string]any{"reason": "same reason"}, http.StatusOK)

	var reqEvents, reqAudits int
	if err := api.pool.QueryRow(ctx, `
		SELECT count(*) FROM event WHERE channel = $1 AND type = 'requirement.waived'`,
		"version:"+versionIDStr).Scan(&reqEvents); err != nil {
		t.Fatalf("count requirement waiver events: %v", err)
	}
	if err := api.pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_log WHERE action = 'requirement.waived' AND target_id = $1`,
		strconv.FormatInt(ids.requirementID, 10)).Scan(&reqAudits); err != nil {
		t.Fatalf("count requirement waiver audits: %v", err)
	}
	if reqEvents != 2 || reqAudits != 2 {
		t.Fatalf("repeat requirement waiver events/audits = %d/%d, want 2/2 (fact dedupe suppressed the second admin action)", reqEvents, reqAudits)
	}

	// Version level: waive-close -> reopen -> waive-close with the SAME reason.
	api.do(http.MethodPost, "/api/versions/"+versionIDStr+"/waive-close", map[string]any{"reason": "same version reason"}, http.StatusOK)
	api.do(http.MethodPost, "/api/versions/"+versionIDStr+"/reopen", map[string]any{"reason": "re-verify"}, http.StatusOK)
	api.do(http.MethodPost, "/api/versions/"+versionIDStr+"/waive-close", map[string]any{"reason": "same version reason"}, http.StatusOK)

	var verEvents, verAudits int
	if err := api.pool.QueryRow(ctx, `
		SELECT count(*) FROM event WHERE channel = $1 AND type = 'version.waived'`,
		"version:"+versionIDStr).Scan(&verEvents); err != nil {
		t.Fatalf("count version waiver events: %v", err)
	}
	if err := api.pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_log WHERE action = 'version.waived' AND target_id = $1`,
		versionIDStr).Scan(&verAudits); err != nil {
		t.Fatalf("count version waiver audits: %v", err)
	}
	if verEvents != 2 || verAudits != 2 {
		t.Fatalf("repeat version waiver events/audits = %d/%d, want 2/2 (fact dedupe suppressed the second admin action)", verEvents, verAudits)
	}
}

// TestWaivedReportListsScopeReasonActor is the deferred SC-G11 end-to-end leg
// (final fix wave #1, T6 controller ruling → T14): waive a requirement → the
// deterministic reconciler closes it → the version closes NORMALLY (all
// requirements closed) but unverified=true because a requirement closed via
// waiver → the ACTUAL published v3 HTML names the waived requirement, its waiver
// reason and its operator. Before the fix the requirement-level waiver was
// invisible (the waived section gated on closure_kind=waived) and the normal
// close was labeled fully-verified — the human could not tell the admin overrode
// completeness.
func TestWaivedReportListsScopeReasonActor(t *testing.T) {
	api := newTestAPI(t)
	api.login("admin", testSeedAdminPassword)
	versionIDStr, versionID := api.createTestVersion("G Waived Report Content")
	ids := waiverFixtureIDs(888000)
	seedWaiverFixture(t, api, versionID, ids)
	ctx := context.Background()

	// Admin waives the clarified requirement; the deterministic reconciler closes
	// it through the normal terminal entry with check_result.waived=true.
	api.do(http.MethodPost, "/api/versions/"+versionIDStr+"/requirements/"+strconv.FormatInt(ids.requirementID, 10)+"/waive",
		map[string]any{"reason": "AC-1 descoped by owner"}, http.StatusOK)
	if err := api.closureSvc.EnsureClosures(ctx, versionID); err != nil {
		t.Fatalf("EnsureClosures: %v", err)
	}
	if got := gd17RequirementStatus(t, api, ids.requirementID); got != string(version.RequirementClosed) {
		t.Fatalf("waived requirement status = %q, want closed", got)
	}

	// Publish the version report (the closed requirement opens the due gate), then
	// the deterministic version close runs. It is a NORMAL close (closure_kind=
	// normal) but unverified=true: a requirement closed via waiver (#1).
	publishGD17VersionReport(t, api, versionID)
	if err := api.versionCloseSvc.EnsureVersionClose(ctx, versionID); err != nil {
		t.Fatalf("EnsureVersionClose: %v", err)
	}
	status, closureKind, unverified := waiverVersionProjection(t, api, versionID)
	if status != version.StatusCompleted || closureKind != version.ClosureKindNormal || !unverified {
		t.Fatalf("version projection = %q/%q/%v, want completed/normal/true (waived requirement → unverified normal close)", status, closureKind, unverified)
	}

	// Regenerate + publish the post-close report so the HTML reflects the closed
	// unverified classification (the close changed the fingerprint).
	kind := delivery.ReportKindVersion
	reports, err := api.reportSvc.List(ctx, versionID, &kind)
	if err != nil || len(reports) == 0 {
		t.Fatalf("list reports: %v (n=%d)", err, len(reports))
	}
	report := reports[0]
	revision, err := api.reportSvc.Regenerate(ctx, versionID, report.ID)
	if err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
	if _, err := api.reportSvc.Publish(ctx, report.ID, revision, "waived requirement report"); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	var adminID int64
	if err := api.pool.QueryRow(ctx, `SELECT id FROM app_user WHERE username = 'admin'`).Scan(&adminID); err != nil {
		t.Fatalf("read admin id: %v", err)
	}

	// The ACTUAL published HTML (served by the real content endpoint) names the
	// waived requirement, its reason and its operator.
	path := "/api/versions/" + versionIDStr + "/reports/" + strconv.FormatInt(report.ID, 10) +
		"/revisions/" + strconv.Itoa(int(revision)) + "/html"
	statusCode, _, html := api.getRaw(path, true)
	if statusCode != http.StatusOK {
		t.Fatalf("report HTML status = %d: %s", statusCode, html)
	}
	body := string(html)
	for _, want := range []string{
		`data-section="waived"`,
		"豁免说明",
		"waiver requirement",
		"AC-1 descoped by owner",
		"#" + strconv.FormatInt(adminID, 10),
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("published waived report HTML missing %q\n%s", want, body)
		}
	}
	// The conclusion mentions the waiver and never reads as a clean verified pass.
	if strings.Contains(body, "一切正常") {
		t.Fatalf("a waived-requirement version concluded 一切正常:\n%s", body)
	}
	if !strings.Contains(body, "豁免") {
		t.Fatalf("the report does not mention the waiver:\n%s", body)
	}
}
