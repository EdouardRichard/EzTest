package executionapp_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

type caseRunnerFixture struct {
	runner     *executionapp.CaseRunner
	intents    *fakeIntentRepo
	caseRuns   *fakeCaseRunRepo
	sutContext *fakeSUTContextRepo
	results    *fakeResultRepo
	evidence   *fakeEvidenceRepo
	humans     *fakeHumanRepo
	cases      *fakeExecutionCaseReader
	bindings   *fakeBindingResolver
	browser    *fakeBrowserExecutor
	creds      *fakeCredentialReader
	content    *fakeContentStore
	http       *fakeHTTPDoer
	db         *fakeDatabaseQuerier
	gaps       *fakeGapEnsurer
	coverage   *fakeCoverageReader
	room       *fakeTeamRoom
	settings   *fakeCaseRunnerSettings
	clock      *caseRunnerClock
	ids        *caseRunnerIDs
	events     *caseRunnerEvents
	audit      *caseRunnerAudit
}

func int64ptr(value int64) *int64 { return &value }
func intptr(value int) *int       { return &value }

func caseRunSnapshot() execution.CaseSnapshot {
	return execution.CaseSnapshot{
		CaseID: 51, CaseVersionID: 41, RequirementID: 501, RequirementVersionID: 601,
		DefaultVersionSUTID: 7, Title: "登录流程", Description: "用户使用账号密码登录",
		Preconditions: "已创建测试账号", Priority: "P0",
		VersionSUTIDs: []int64{7},
		Steps: []execution.CaseStep{{
			Seq: 1, Action: "打开登录页", Expected: "出现登录表单",
			TargetVersionSUTID: 7, AccountRole: "admin", TargetPageID: int64ptr(71),
		}},
		Assertions: []execution.CaseAssertion{{
			StepSeq: 1, AssertionSeq: 1, Kind: "ui", Expected: "Login 按钮可见", TargetVersionSUTID: 7,
		}},
	}
}

func newCaseRunnerFixture(t *testing.T) *caseRunnerFixture {
	t.Helper()
	fixture := &caseRunnerFixture{
		intents:    newFakeIntentRepo(),
		caseRuns:   newFakeCaseRunRepo(),
		sutContext: newFakeSUTContextRepo(),
		results:    newFakeResultRepo(),
		evidence:   newFakeEvidenceRepo(),
		humans:     newFakeHumanRepo(),
		cases:      &fakeExecutionCaseReader{snapshot: caseRunSnapshot()},
		bindings: &fakeBindingResolver{bindings: []execution.SUTBinding{{
			VersionSUTID: 7, SUTName: "orders", EnvironmentName: "test",
			BaseURL:      "http://127.0.0.1:13335",
			PageURLs:     []string{"http://127.0.0.1:13335/login"},
			AccountRoles: []string{"admin"},
		}}},
		browser: &fakeBrowserExecutor{result: executionapp.BrowserActionResult{
			OK: true, Result: json.RawMessage(`{"status":200}`),
		}},
		creds: &fakeCredentialReader{account: executionapp.AccountCredential{
			AccountID: 81, Username: "alice", Password: "pw",
		}},
		content:  &fakeContentStore{},
		http:     &fakeHTTPDoer{response: executionapp.HTTPResponseData{Status: 200, Body: []byte(`{"ok":true}`)}},
		db:       &fakeDatabaseQuerier{queryResult: executionapp.DBQueryResult{Columns: []string{"n"}, Rows: [][]string{{"1"}}, RowCount: 1}},
		gaps:     &fakeGapEnsurer{},
		coverage: &fakeCoverageReader{requirements: []executionapp.RequirementRef{{RequirementID: 501, Status: "clarified"}}},
		room:     &fakeTeamRoom{},
		settings: &fakeCaseRunnerSettings{ints: map[string]int64{
			"execution.case_timeout_seconds":          900,
			"execution.evidence_max_bytes":            5 << 20,
			"execution.lease_seconds":                 30,
			"execution.human_request_timeout_seconds": 600,
			"execution.case_max_wait_seconds":         300,
		}},
		clock:  &caseRunnerClock{now: time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)},
		ids:    &caseRunnerIDs{next: 5000},
		events: &caseRunnerEvents{},
		audit:  &caseRunnerAudit{},
	}
	fixture.intents.rows[1001] = execution.ExecutionIntent{
		ID: 1001, VersionID: 10, CaseVersionID: 41, Status: execution.IntentClaimed,
		VersionSUTIDs: []int64{7}, Purpose: execution.PurposeInitial, AttemptNo: 1,
	}
	fixture.caseRuns.rows[3001] = execution.CaseRun{
		ID: 3001, WaveID: 2001, IntentID: 1001, CaseVersionID: 41,
		Status: execution.CaseRunPending, DispatchEpoch: 3, DispatchToken: "dispatch-token",
		Attempts: 1, OwnerInstance: "api-1",
	}
	fixture.runner = executionapp.NewCaseRunner(executionapp.CaseRunnerDeps{
		Intents:     fixture.intents,
		CaseRuns:    fixture.caseRuns,
		SUTContexts: fixture.sutContext,
		Results:     fixture.results,
		Evidence:    fixture.evidence,
		Humans:      fixture.humans,
		Cases:       fixture.cases,
		Bindings:    fixture.bindings,
		Browser:     fixture.browser,
		Credentials: fixture.creds,
		Content:     fixture.content,
		HTTPClient:  fixture.http,
		Database:    fixture.db,
		Gaps:        fixture.gaps,
		Coverage:    fixture.coverage,
		TeamRoom:    fixture.room,
		Settings:    fixture.settings,
		Clock:       fixture.clock,
		IDs:         fixture.ids,
		Events:      fixture.events,
		Audit:       fixture.audit,
		InstanceID:  "api-1",
	})
	return fixture
}

func p3WorkItem() agent.WorkItem {
	payload, _ := json.Marshal(map[string]any{
		"intent_id":       "1001",
		"wave_id":         "2001",
		"case_run_id":     "3001",
		"case_version_id": "41",
		"dispatch_epoch":  "3",
		"dispatch_token":  "dispatch-token",
	})
	return agent.WorkItem{ID: 9001, VersionID: 10, Type: "execute.case", Payload: payload}
}

func p3Scope() tools.Scope {
	return tools.Scope{
		VersionID: 10, WaveID: 2001, CaseRunID: 3001, IntentID: 1001,
		CaseVersionID: 41, DispatchEpoch: 3, DispatchToken: "dispatch-token", AgentRunID: 7001,
	}
}

func prepareCaseRun(t *testing.T, fixture *caseRunnerFixture) execution.TaskSnapshot {
	t.Helper()
	snapshot, err := fixture.runner.Prepare(context.Background(), p3WorkItem())
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	return snapshot
}

func platformError(t *testing.T, err error) *platform.Error {
	t.Helper()
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v is not a *platform.Error", err)
	}
	return apiErr
}

func TestPrepareSetsRunningLeaseAndSnapshot(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	snapshot := prepareCaseRun(t, fixture)

	row := fixture.caseRuns.get(t, 3001)
	if row.Status != execution.CaseRunRunning {
		t.Fatalf("status = %q", row.Status)
	}
	if row.LeaseUntil == nil || !row.LeaseUntil.After(fixture.clock.now) {
		t.Fatalf("lease = %v", row.LeaseUntil)
	}
	if row.DispatchEpoch != 3 || row.DispatchToken != "dispatch-token" {
		t.Fatalf("dispatch fence changed: %+v", row)
	}
	if intent, _ := fixture.intents.Get(context.Background(), 1001); intent.Status != execution.IntentRunning {
		t.Fatalf("intent status = %q", intent.Status)
	}
	if _, ok := fixture.sutContext.row(7, "admin"); !ok {
		t.Fatalf("case_run_sut_context row for (7, admin) was not created")
	}
	if fixture.browser.contexts != 0 {
		t.Fatalf("Prepare must not create browser contexts lazily, created %d", fixture.browser.contexts)
	}
	if snapshot.Case.Title != "登录流程" || snapshot.CaseRun.ID != 3001 ||
		len(snapshot.SUTs) != 1 || snapshot.SUTs[0].BaseURL != "http://127.0.0.1:13335" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	started := false
	for _, eventType := range fixture.events.types() {
		if eventType == executionapp.EventCaseRunStarted {
			started = true
		}
	}
	if !started {
		t.Fatalf("expected a case_run.started event, got %v", fixture.events.types())
	}
}

func TestPrepareRejectsNonCurrentCase(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	fixture.cases.err = execution.ErrNotFound
	_, err := fixture.runner.Prepare(context.Background(), p3WorkItem())
	if got := platformError(t, err); got.Status != 409 || got.Code != "state_conflict" {
		t.Fatalf("error = %+v", got)
	}
}

func TestPrepareRejectsStaleDispatch(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	fixture.caseRuns.rows[3001] = execution.CaseRun{
		ID: 3001, WaveID: 2001, IntentID: 1001, CaseVersionID: 41,
		Status: execution.CaseRunPending, DispatchEpoch: 4, DispatchToken: "new-token",
	}
	_, err := fixture.runner.Prepare(context.Background(), p3WorkItem())
	if got := platformError(t, err); got.Code != "lease_lost" {
		t.Fatalf("error = %+v", got)
	}
}

func TestRecordStepRejectsStaleEpoch(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)

	stale := p3Scope()
	stale.DispatchToken = "old-token"
	_, err := fixture.runner.StepRecord(context.Background(), stale, tools.StepRecordInput{
		Seq: 1, Status: "passed", Observed: "ok",
	})
	if got := platformError(t, err); got.Code != "lease_lost" {
		t.Fatalf("stale write error = %+v", got)
	}
	if steps, _ := fixture.results.ListSteps(context.Background(), 3001); len(steps) != 0 {
		t.Fatalf("stale write changed rows: %+v", steps)
	}

	if _, err := fixture.runner.StepRecord(context.Background(), p3Scope(), tools.StepRecordInput{
		Seq: 1, Status: "passed", Observed: "登录表单可见",
	}); err != nil {
		t.Fatalf("StepRecord: %v", err)
	}
	steps, _ := fixture.results.ListSteps(context.Background(), 3001)
	if len(steps) != 1 || steps[0].Status != execution.StepPassed {
		t.Fatalf("steps = %+v", steps)
	}
	if steps[0].Observed["summary"] != "登录表单可见" {
		t.Fatalf("observed = %+v", steps[0].Observed)
	}
}

func TestRecordAssertionExpectedOverridden(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)

	if _, err := fixture.runner.AssertionRecord(context.Background(), p3Scope(), tools.AssertionRecordInput{
		StepSeq: 1, AssertionSeq: 1, Verdict: "failed",
		Expected: "模型伪造的期望", Actual: "Login 按钮不存在", Reason: "not found",
	}); err != nil {
		t.Fatalf("AssertionRecord: %v", err)
	}
	assertions, _ := fixture.results.ListAssertions(context.Background(), 3001)
	if len(assertions) != 1 {
		t.Fatalf("assertions = %+v", assertions)
	}
	got := assertions[0]
	if got.Expected != "Login 按钮可见" {
		t.Fatalf("expected was not overwritten by the P2 truth: %q", got.Expected)
	}
	if got.Verdict != execution.AssertionFailed || got.Actual != "Login 按钮不存在" || got.Kind != "ui" {
		t.Fatalf("assertion = %+v", got)
	}

	_, err := fixture.runner.AssertionRecord(context.Background(), p3Scope(), tools.AssertionRecordInput{
		StepSeq: 1, AssertionSeq: 9, Verdict: "passed",
	})
	if got := platformError(t, err); got.Code != "invalid_argument" {
		t.Fatalf("out-of-range assertion error = %+v", got)
	}
}

func TestFinishAggregatesDeterministicVerdict(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	if _, err := fixture.runner.StepRecord(context.Background(), p3Scope(), tools.StepRecordInput{
		Seq: 1, Status: "passed", Observed: "ok",
	}); err != nil {
		t.Fatalf("StepRecord: %v", err)
	}
	if _, err := fixture.runner.AssertionRecord(context.Background(), p3Scope(), tools.AssertionRecordInput{
		StepSeq: 1, AssertionSeq: 1, Verdict: "failed", Actual: "Login 按钮不存在",
	}); err != nil {
		t.Fatalf("AssertionRecord: %v", err)
	}
	finished, err := fixture.runner.Finish(context.Background(), p3Scope(), execution.FinishCandidate{
		Verdict: execution.CaseRunPassed,
	})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.Status != execution.CaseRunFailed || finished.FailureClass != execution.FailureClassApplication {
		t.Fatalf("aggregated case_run = %+v", finished)
	}
	if finished.Summary.AssertionFailed != 1 || finished.Summary.AssertionTotal != 1 {
		t.Fatalf("summary = %+v", finished.Summary)
	}
	if finished.FinishedAt == nil {
		t.Fatalf("finished_at was not set")
	}
}

func TestFinishAllPassed(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	if _, err := fixture.runner.StepRecord(context.Background(), p3Scope(), tools.StepRecordInput{Seq: 1, Status: "passed"}); err != nil {
		t.Fatalf("StepRecord: %v", err)
	}
	if _, err := fixture.runner.AssertionRecord(context.Background(), p3Scope(), tools.AssertionRecordInput{
		StepSeq: 1, AssertionSeq: 1, Verdict: "passed", Actual: "Login 按钮可见",
	}); err != nil {
		t.Fatalf("AssertionRecord: %v", err)
	}
	finished, err := fixture.runner.Finish(context.Background(), p3Scope(), execution.FinishCandidate{Verdict: execution.CaseRunPassed})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.Status != execution.CaseRunPassed || finished.FailureClass != "" {
		t.Fatalf("case_run = %+v", finished)
	}
	if finished.Summary.VerdictReason != execution.VerdictReasonPassed {
		t.Fatalf("verdict reason = %q", finished.Summary.VerdictReason)
	}
}

func TestFinishMissingResultsIsInconclusive(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	finished, err := fixture.runner.Finish(context.Background(), p3Scope(), execution.FinishCandidate{Verdict: execution.CaseRunPassed})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.Status != execution.CaseRunInconclusive {
		t.Fatalf("case_run status = %q", finished.Status)
	}
	if finished.Summary.VerdictReason != execution.VerdictReasonIncomplete {
		t.Fatalf("verdict reason = %q", finished.Summary.VerdictReason)
	}
}

func TestWaitBoundedAndInterruptible(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)

	if _, err := fixture.runner.Wait(context.Background(), p3Scope(), tools.WaitInput{Seconds: 301}); err == nil {
		t.Fatalf("Wait accepted 301 seconds")
	} else if got := platformError(t, err); got.Status != 400 {
		t.Fatalf("error = %+v", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := fixture.runner.Wait(ctx, p3Scope(), tools.WaitInput{Seconds: 30})
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("canceled wait returned no error")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Wait did not return after cancellation")
	}
}

func TestWaitInterruptedByRunCancellation(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	cancelCtx, cancel := context.WithCancel(context.Background())
	fixture.runner.RegisterRun(3001, cancel)
	go func() {
		time.Sleep(50 * time.Millisecond)
		fixture.runner.CancelRun(3001)
	}()
	if _, err := fixture.runner.Wait(cancelCtx, p3Scope(), tools.WaitInput{Seconds: 30}); err == nil {
		t.Fatalf("paused wait returned no error")
	}
	fixture.runner.ClearRun(3001)
}

func TestEvidenceImmutableAndMaxBytes(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)

	oversized := make([]byte, (5<<20)+1)
	_, err := fixture.runner.AddEvidence(context.Background(), p3Scope(), executionapp.EvidenceInput{
		Kind: execution.EvidenceText, Data: oversized,
	})
	got := platformError(t, err)
	if got.Code != "evidence_too_large" || got.Status != 413 {
		t.Fatalf("oversize error = %+v", got)
	}

	first, err := fixture.runner.AddEvidence(context.Background(), p3Scope(), executionapp.EvidenceInput{
		Kind: execution.EvidenceText, Data: []byte("hello"), Filename: "note.txt",
	})
	if err != nil {
		t.Fatalf("AddEvidence: %v", err)
	}
	if first.CaseRunID != 3001 || first.Kind != execution.EvidenceText || first.SizeBytes != 5 {
		t.Fatalf("evidence = %+v", first)
	}
	if first.VersionSUTID == nil || *first.VersionSUTID != 7 {
		t.Fatalf("single-SUT evidence version_sut_id = %v, want 7", first.VersionSUTID)
	}
	second, err := fixture.runner.AddEvidence(context.Background(), p3Scope(), executionapp.EvidenceInput{
		Kind: execution.EvidenceText, Data: []byte("hello again"),
	})
	if err != nil {
		t.Fatalf("AddEvidence: %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("evidence rows must be immutable (new row per upload)")
	}
	rows, _ := fixture.evidence.ListByCaseRun(context.Background(), 3001, 0)
	if len(rows) != 2 {
		t.Fatalf("evidence rows = %d", len(rows))
	}
	if len(fixture.evidence.refs) != 2 {
		t.Fatalf("evidence refs = %d", len(fixture.evidence.refs))
	}

	// The per-case_run cap drops further evidence without failing.
	for i := 0; i < execution.EvidenceMaxPerCaseRun; i++ {
		if _, err := fixture.runner.AddEvidence(context.Background(), p3Scope(), executionapp.EvidenceInput{
			Kind: execution.EvidenceText, Data: []byte("filler"),
		}); err != nil {
			t.Fatalf("filler AddEvidence: %v", err)
		}
	}
	if _, err := fixture.runner.AddEvidence(context.Background(), p3Scope(), executionapp.EvidenceInput{
		Kind: execution.EvidenceText, Data: []byte("overflow"),
	}); err != nil {
		t.Fatalf("overflow AddEvidence must not fail the case: %v", err)
	}
	rows, _ = fixture.evidence.ListByCaseRun(context.Background(), 3001, 0)
	if len(rows) != execution.EvidenceMaxPerCaseRun {
		t.Fatalf("evidence rows = %d, want cap %d", len(rows), execution.EvidenceMaxPerCaseRun)
	}
}

func TestBrowserToolCreatesContextLazilyAndStoresEvidence(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)

	// goto: creates one context for (7, admin) and runs the action.
	if _, err := fixture.runner.Browser(context.Background(), p3Scope(), "goto",
		json.RawMessage(`{"version_sut_id":"7","account_role":"admin","url":"http://127.0.0.1:13335/login"}`)); err != nil {
		t.Fatalf("Browser goto: %v", err)
	}
	if fixture.browser.contexts != 1 {
		t.Fatalf("contexts = %d", fixture.browser.contexts)
	}
	if fixture.browser.lastSpec.BaseURL != "http://127.0.0.1:13335" {
		t.Fatalf("context spec = %+v", fixture.browser.lastSpec)
	}
	row, ok := fixture.sutContext.row(7, "admin")
	if !ok || row.Status != execution.SUTContextActive || row.BrowserContextID != "ctx_1" {
		t.Fatalf("sut context = %+v", row)
	}
	if fixture.browser.actions[0] != "goto" {
		t.Fatalf("actions = %v", fixture.browser.actions)
	}

	// A second action reuses the same context.
	if _, err := fixture.runner.Browser(context.Background(), p3Scope(), "snapshot", json.RawMessage(`{"account_role":"admin"}`)); err != nil {
		t.Fatalf("Browser snapshot: %v", err)
	}
	if fixture.browser.contexts != 1 {
		t.Fatalf("contexts after reuse = %d", fixture.browser.contexts)
	}

	// A screenshot artifact is stored as immutable evidence.
	fixture.browser.result = executionapp.BrowserActionResult{
		OK:     true,
		Result: json.RawMessage(`{"bytes":3}`),
		Artifacts: []executionapp.BrowserArtifact{{
			Kind: "screenshot", MIME: "image/png", Base64: "AAAA", SHA256: "abc", Bytes: 3,
		}},
	}
	if _, err := fixture.runner.Browser(context.Background(), p3Scope(), "screenshot", json.RawMessage(`{"account_role":"admin"}`)); err != nil {
		t.Fatalf("Browser screenshot: %v", err)
	}
	rows, _ := fixture.evidence.ListByCaseRun(context.Background(), 3001, 0)
	if len(rows) != 1 || rows[0].Kind != execution.EvidenceScreenshot {
		t.Fatalf("evidence = %+v", rows)
	}
}

func TestAccountCredentialsDelegatesWithRole(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	result, err := fixture.runner.AccountCredentials(context.Background(), p3Scope(), tools.AccountCredentialsInput{
		VersionSUTID: int64ptr(7), Role: "admin",
	})
	if err != nil {
		t.Fatalf("AccountCredentials: %v", err)
	}
	payload := result.(map[string]any)
	if payload["username"] != "alice" || payload["password"] != "pw" {
		t.Fatalf("payload = %+v", payload)
	}
	if len(fixture.creds.roles) != 1 || fixture.creds.roles[0] != "admin" {
		t.Fatalf("roles = %v", fixture.creds.roles)
	}
	if len(fixture.audit.events) == 0 {
		t.Fatalf("expected an execution.credentials_read audit event")
	}
}

func TestHTTPRequestEvidenceRedactsCredentials(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	result, err := fixture.runner.HTTP(context.Background(), p3Scope(), json.RawMessage(`{
		"version_sut_id":"7","path":"/api/orders","method":"GET",
		"headers":{"Authorization":"Bearer super-secret","X-Trace":"1"}
	}`))
	if err != nil {
		t.Fatalf("HTTP: %v", err)
	}
	payload := result.(map[string]any)
	if payload["status"] != 200 {
		t.Fatalf("payload = %+v", payload)
	}
	if fixture.http.lastReq.URL != "http://127.0.0.1:13335/api/orders" {
		t.Fatalf("request URL = %q", fixture.http.lastReq.URL)
	}
	rows, _ := fixture.evidence.ListByCaseRun(context.Background(), 3001, 0)
	if len(rows) != 1 || rows[0].Kind != execution.EvidenceHTTP {
		t.Fatalf("evidence = %+v", rows)
	}
	for _, put := range fixture.content.puts {
		if string(put) != "" && containsSecret(string(put), "super-secret") {
			t.Fatalf("http evidence leaked credentials: %s", put)
		}
	}
}

func containsSecret(haystack, secret string) bool {
	return len(secret) > 0 && len(haystack) >= len(secret) && (func() bool {
		for i := 0; i+len(secret) <= len(haystack); i++ {
			if haystack[i:i+len(secret)] == secret {
				return true
			}
		}
		return false
	})()
}

func TestDBQueryRejectsNonReadOnlySQL(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	for _, sqlText := range []string{
		"DELETE FROM users",
		"SELECT 1; DROP TABLE users",
		"UPDATE users SET admin = true",
	} {
		_, err := fixture.runner.DBQuery(context.Background(), p3Scope(), json.RawMessage(`{"version_sut_id":"7","sql":`+jsonString(sqlText)+`}`))
		if err == nil {
			t.Fatalf("db.query accepted %q", sqlText)
		}
		if got := platformError(t, err); got.Status != 400 {
			t.Fatalf("error for %q = %+v", sqlText, got)
		}
	}
	if _, err := fixture.runner.DBQuery(context.Background(), p3Scope(), json.RawMessage(`{
		"version_sut_id":"7","sql":"SELECT count(*) FROM orders","max_rows":10
	}`)); err != nil {
		t.Fatalf("valid SELECT rejected: %v", err)
	}
	if !containsSecret(fixture.db.lastSQL, "SELECT count(*)") {
		t.Fatalf("querier SQL = %q", fixture.db.lastSQL)
	}
	rows, _ := fixture.evidence.ListByCaseRun(context.Background(), 3001, 0)
	if len(rows) != 1 || rows[0].Kind != execution.EvidenceDB {
		t.Fatalf("db evidence = %+v", rows)
	}
}

func jsonString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func TestFailureSubmitDedupeHintMatchesSpec(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	if _, err := fixture.runner.StepRecord(context.Background(), p3Scope(), tools.StepRecordInput{Seq: 1, Status: "passed"}); err != nil {
		t.Fatalf("StepRecord: %v", err)
	}
	if _, err := fixture.runner.AssertionRecord(context.Background(), p3Scope(), tools.AssertionRecordInput{
		StepSeq: 1, AssertionSeq: 1, Verdict: "failed", Actual: "mismatch",
	}); err != nil {
		t.Fatalf("AssertionRecord: %v", err)
	}
	if _, err := fixture.runner.Finish(context.Background(), p3Scope(), execution.FinishCandidate{Verdict: execution.CaseRunFailed}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if _, err := fixture.runner.FailureSubmit(context.Background(), p3Scope(), tools.FailureSubmitInput{
		CaseRunID: 3001, FailureClass: "application", Category: "ui", Severity: "major", Summary: "按钮文案不符",
	}); err != nil {
		t.Fatalf("FailureSubmit: %v", err)
	}
	// sha256(version_id|case_version_id|step_seq|assertion_seq|failure_class)[:32]
	payload := "10|41|1|1|application"
	sum := sha256.Sum256([]byte(payload))
	want := hex.EncodeToString(sum[:])[:32]
	found := ""
	fixture.events.mu.Lock()
	for _, event := range fixture.events.events {
		if event.Type == "execution.failure_candidate" {
			found, _ = event.Payload["dedupe_hint"].(string)
		}
	}
	fixture.events.mu.Unlock()
	if found != want {
		t.Fatalf("dedupe_hint = %q, want %q", found, want)
	}
}
