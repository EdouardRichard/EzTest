package executionapp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	executorexec "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/executor"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
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
	visual     *fakeVisualPort
	settings   *fakeCaseRunnerSettings
	clock      *caseRunnerClock
	ids        *caseRunnerIDs
	events     *caseRunnerEvents
	audit      *caseRunnerAudit
	knowledge  *fakeCaseSuccessHook
	// knowledgeFailure receives failed-case observations. It is a nil-safe
	// optional hook like KnowledgeHook; the failure lifecycle wiring test uses it.
	knowledgeFailure *fakeCaseFailureHook
	// discovery records the terminal-state discovery observation batches. It is
	// nil in the default fixture (the port is optional in W1-10).
	discovery *fakeDiscoveryWriter
	runCtx    context.Context
	runCancel context.CancelFunc
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
	return newCaseRunnerFixtureWithDiscoveryWriter(t, nil)
}

// newCaseRunnerFixtureWithDiscovery is the default fixture plus a recording
// discovery repository port so terminal captures are observable.
func newCaseRunnerFixtureWithDiscovery(t *testing.T) *caseRunnerFixture {
	t.Helper()
	writer := &fakeDiscoveryWriter{}
	fixture := newCaseRunnerFixtureWithDiscoveryWriter(t, writer)
	fixture.discovery = writer
	return fixture
}

func newCaseRunnerFixtureWithDiscoveryWriter(t *testing.T, discovery executionapp.DiscoveryObservationWriter) *caseRunnerFixture {
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
		visual:   &fakeVisualPort{},
		settings: &fakeCaseRunnerSettings{ints: map[string]int64{
			"execution.case_timeout_seconds":          900,
			"execution.evidence_max_bytes":            5 << 20,
			"execution.lease_seconds":                 30,
			"execution.human_request_timeout_seconds": 600,
			"execution.case_max_wait_seconds":         300,
		}},
		clock:            &caseRunnerClock{now: time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)},
		ids:              &caseRunnerIDs{next: 5000},
		events:           &caseRunnerEvents{},
		audit:            &caseRunnerAudit{},
		knowledge:        &fakeCaseSuccessHook{},
		knowledgeFailure: &fakeCaseFailureHook{},
	}
	fixture.runCtx, fixture.runCancel = context.WithCancel(context.Background())
	t.Cleanup(fixture.runCancel)
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
		Intents:              fixture.intents,
		CaseRuns:             fixture.caseRuns,
		SUTContexts:          fixture.sutContext,
		Results:              fixture.results,
		Evidence:             fixture.evidence,
		Humans:               fixture.humans,
		Cases:                fixture.cases,
		Bindings:             fixture.bindings,
		Browser:              fixture.browser,
		Credentials:          fixture.creds,
		Content:              fixture.content,
		HTTPClient:           fixture.http,
		Database:             fixture.db,
		Gaps:                 fixture.gaps,
		Coverage:             fixture.coverage,
		TeamRoom:             fixture.room,
		Visual:               fixture.visual,
		Settings:             fixture.settings,
		Clock:                fixture.clock,
		IDs:                  fixture.ids,
		Events:               fixture.events,
		Audit:                fixture.audit,
		KnowledgeHook:        fixture.knowledge,
		KnowledgeFailureHook: fixture.knowledgeFailure,
		Discovery:            discovery,
		InstanceID:           "api-1",
	})
	return fixture
}

func p3WorkItem() agent.WorkItem {
	return p3WorkItemFor(3, "dispatch-token")
}

func p3WorkItemFor(dispatchEpoch int64, dispatchToken string) agent.WorkItem {
	payload, _ := json.Marshal(map[string]any{
		"intent_id":       "1001",
		"wave_id":         "2001",
		"case_run_id":     "3001",
		"case_version_id": "41",
		"dispatch_epoch":  dispatchEpoch,
		"dispatch_token":  dispatchToken,
	})
	return agent.WorkItem{ID: 9001, VersionID: 10, Type: "execute.case", Payload: payload}
}

func p3Scope() tools.Scope {
	return tools.Scope{
		VersionID: 10, WaveID: 2001, CaseRunID: 3001, IntentID: 1001,
		CaseVersionID: 41, DispatchEpoch: 3, DispatchToken: "dispatch-token", AgentRunID: 7001,
	}
}

// fakeCaseSuccessHook records passed-case observations. A configured error or
// panic proves the executor treats the hook as best effort.
type fakeCaseSuccessHook struct {
	facts []executionapp.CaseSuccessFact
	err   error
	panic bool
}

func (h *fakeCaseSuccessHook) OnCaseRunPassed(_ context.Context, fact executionapp.CaseSuccessFact) error {
	h.facts = append(h.facts, fact)
	if h.panic {
		panic("knowledge hook exploded")
	}
	return h.err
}

func prepareCaseRun(t *testing.T, fixture *caseRunnerFixture) execution.TaskSnapshot {
	t.Helper()
	snapshot, err := fixture.runner.Prepare(fixture.runCtx, p3WorkItem())
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
	if row.LeaseUntil == nil || !row.LeaseUntil.After(fixture.clock.Now()) {
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

func TestFinishPassedNotifiesKnowledgeHookBestEffort(t *testing.T) {
	finishPassed := func(t *testing.T, fixture *caseRunnerFixture) (execution.CaseRun, error) {
		t.Helper()
		prepareCaseRun(t, fixture)
		if _, err := fixture.runner.StepRecord(context.Background(), p3Scope(), tools.StepRecordInput{Seq: 1, Status: "passed"}); err != nil {
			t.Fatalf("StepRecord: %v", err)
		}
		if _, err := fixture.runner.AssertionRecord(context.Background(), p3Scope(), tools.AssertionRecordInput{
			StepSeq: 1, AssertionSeq: 1, Verdict: "passed", Actual: "Login 按钮可见",
		}); err != nil {
			t.Fatalf("AssertionRecord: %v", err)
		}
		return fixture.runner.Finish(context.Background(), p3Scope(), execution.FinishCandidate{Verdict: execution.CaseRunPassed})
	}

	t.Run("notifies once with the run fact", func(t *testing.T) {
		fixture := newCaseRunnerFixture(t)
		finished, err := finishPassed(t, fixture)
		if err != nil {
			t.Fatalf("Finish: %v", err)
		}
		if finished.Status != execution.CaseRunPassed {
			t.Fatalf("status = %q", finished.Status)
		}
		if len(fixture.knowledge.facts) != 1 {
			t.Fatalf("hook calls = %d, want 1", len(fixture.knowledge.facts))
		}
		fact := fixture.knowledge.facts[0]
		if fact.VersionID != 10 || fact.CaseRunID != 3001 || fact.CaseVersionID != 41 {
			t.Fatalf("fact = %+v", fact)
		}
		if fact.Title != "登录流程" {
			t.Fatalf("fact title = %q", fact.Title)
		}
		if len(fact.AccountRoles) != 1 || fact.AccountRoles[0] != "admin" {
			t.Fatalf("fact account roles = %v", fact.AccountRoles)
		}
		if len(fact.PageIDs) != 1 || fact.PageIDs[0] != 71 {
			t.Fatalf("fact page ids = %v", fact.PageIDs)
		}
		if len(fact.APIIDs) != 0 {
			t.Fatalf("fact api ids = %v", fact.APIIDs)
		}
	})

	t.Run("another owner's terminal write does not notify", func(t *testing.T) {
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
		// Another owner persisted the terminal row just before this owner's CAS:
		// this Finish reads the passed truth but must not sediment it a second
		// time.
		fixture.caseRuns.beforeUpdateCAS = func() {
			row := fixture.caseRuns.rows[3001]
			row.Status = execution.CaseRunPassed
			fixture.caseRuns.rows[3001] = row
		}
		finished, err := fixture.runner.Finish(context.Background(), p3Scope(), execution.FinishCandidate{Verdict: execution.CaseRunPassed})
		if err != nil {
			t.Fatalf("Finish: %v", err)
		}
		if finished.Status != execution.CaseRunPassed {
			t.Fatalf("status = %q, want the stored passed truth", finished.Status)
		}
		if len(fixture.knowledge.facts) != 0 {
			t.Fatalf("hook calls = %d, want 0 when another owner won the terminal CAS", len(fixture.knowledge.facts))
		}
	})

	t.Run("hook error does not fail the case run", func(t *testing.T) {
		fixture := newCaseRunnerFixture(t)
		fixture.knowledge.err = errors.New("knowledge service down")
		finished, err := finishPassed(t, fixture)
		if err != nil {
			t.Fatalf("Finish must ignore a hook failure, got %v", err)
		}
		if finished.Status != execution.CaseRunPassed {
			t.Fatalf("status = %q", finished.Status)
		}
	})

	t.Run("hook panic does not fail the case run", func(t *testing.T) {
		fixture := newCaseRunnerFixture(t)
		fixture.knowledge.panic = true
		finished, err := finishPassed(t, fixture)
		if err != nil {
			t.Fatalf("Finish must recover a hook panic, got %v", err)
		}
		if finished.Status != execution.CaseRunPassed {
			t.Fatalf("status = %q", finished.Status)
		}
	})

	t.Run("non passed runs do not notify", func(t *testing.T) {
		fixture := newCaseRunnerFixture(t)
		prepareCaseRun(t, fixture)
		if _, err := fixture.runner.StepRecord(context.Background(), p3Scope(), tools.StepRecordInput{Seq: 1, Status: "passed"}); err != nil {
			t.Fatalf("StepRecord: %v", err)
		}
		if _, err := fixture.runner.AssertionRecord(context.Background(), p3Scope(), tools.AssertionRecordInput{
			StepSeq: 1, AssertionSeq: 1, Verdict: "failed", Actual: "Login 按钮不存在",
		}); err != nil {
			t.Fatalf("AssertionRecord: %v", err)
		}
		finished, err := fixture.runner.Finish(context.Background(), p3Scope(), execution.FinishCandidate{Verdict: execution.CaseRunPassed})
		if err != nil {
			t.Fatalf("Finish: %v", err)
		}
		if finished.Status != execution.CaseRunFailed {
			t.Fatalf("status = %q", finished.Status)
		}
		if len(fixture.knowledge.facts) != 0 {
			t.Fatalf("hook calls = %d, want 0 for a failed run", len(fixture.knowledge.facts))
		}
	})
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
	owner := int64(7)
	fixture.caseRuns.mu.Lock()
	caseRow := fixture.caseRuns.rows[3001]
	caseRow.OwnerExecutorID = &owner
	fixture.caseRuns.rows[3001] = caseRow
	fixture.caseRuns.mu.Unlock()

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
	if fixture.browser.lastSpec.ExecutorID != 7 {
		t.Fatalf("context spec executor = %d, want 7", fixture.browser.lastSpec.ExecutorID)
	}
	// X-01 review: the create metadata is the only producer that correlates a
	// browser context with its dispatch attempt, so it must stamp both ids.
	if got, _ := fixture.browser.lastSpec.Metadata["case_run_id"].(string); got != platformid.Format(caseRow.ID) {
		t.Fatalf("context metadata case_run_id = %v, want %s", fixture.browser.lastSpec.Metadata["case_run_id"], platformid.Format(caseRow.ID))
	}
	if got, ok := fixture.browser.lastSpec.Metadata["attempt"].(int); !ok || got != caseRow.Attempts {
		t.Fatalf("context metadata attempt = %v, want %d", fixture.browser.lastSpec.Metadata["attempt"], caseRow.Attempts)
	}
	row, ok := fixture.sutContext.row(7, "admin")
	if !ok || row.Status != execution.SUTContextActive || row.BrowserContextID != "ctx_1" {
		t.Fatalf("sut context = %+v", row)
	}
	if row.ExecutorID == nil || *row.ExecutorID != 7 {
		t.Fatalf("sut context executor = %v, want 7", row.ExecutorID)
	}
	if fixture.browser.actions[0] != "goto" || fixture.browser.lastExecutorID != 7 {
		t.Fatalf("actions = %v executor = %d", fixture.browser.actions, fixture.browser.lastExecutorID)
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

func TestHTTPLargeBodySpillsHeadAndContentRef(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	body := append([]byte("P8-LARGE-BEGIN"), bytes.Repeat([]byte("x"), 20_000)...)
	fixture.http.response = executionapp.HTTPResponseData{Status: 200, Body: body}
	result, err := fixture.runner.HTTP(context.Background(), p3Scope(), json.RawMessage(`{
		"version_sut_id":"7","path":"/p8/large?bytes=20000","method":"GET"
	}`))
	if err != nil {
		t.Fatalf("HTTP large: %v", err)
	}
	payload, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T", result)
	}
	if payload["truncated"] != true {
		t.Fatalf("truncated = %v, want true", payload["truncated"])
	}
	head, _ := payload["head"].(string)
	if !strings.Contains(head, "P8-LARGE-BEGIN") {
		t.Fatalf("head = %q", head)
	}
	ref, _ := payload["content_ref"].(string)
	if strings.TrimSpace(ref) == "" {
		t.Fatalf("content_ref missing: %#v", payload)
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

// ---------- Task 9: lease, heartbeat and dispatch health ----------

// manualClock is a goroutine-safe clock so the lease heartbeat goroutine and the
// test can advance time without a data race.
type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func newManualClock(now time.Time) *manualClock { return &manualClock{now: now} }

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) Advance(delta time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(delta)
}

// newHeartbeatFixture rebuilds the runner with an injectable clock and a fast
// heartbeat interval so a long action can be simulated quickly.
func newHeartbeatFixture(t *testing.T, clock executionapp.Clock, interval time.Duration) *caseRunnerFixture {
	t.Helper()
	fixture := newCaseRunnerFixture(t)
	fixture.runner = executionapp.NewCaseRunner(executionapp.CaseRunnerDeps{
		Intents:           fixture.intents,
		CaseRuns:          fixture.caseRuns,
		SUTContexts:       fixture.sutContext,
		Results:           fixture.results,
		Evidence:          fixture.evidence,
		Humans:            fixture.humans,
		Cases:             fixture.cases,
		Bindings:          fixture.bindings,
		Browser:           fixture.browser,
		Credentials:       fixture.creds,
		Content:           fixture.content,
		HTTPClient:        fixture.http,
		Database:          fixture.db,
		Gaps:              fixture.gaps,
		Coverage:          fixture.coverage,
		TeamRoom:          fixture.room,
		Visual:            fixture.visual,
		Settings:          fixture.settings,
		Clock:             clock,
		IDs:               fixture.ids,
		Events:            fixture.events,
		Audit:             fixture.audit,
		InstanceID:        "api-1",
		LeaseTTL:          90 * time.Second,
		HeartbeatInterval: interval,
	})
	return fixture
}

// waitForLeaseRenewal blocks until the independent heartbeat has renewed the
// lease to at least wantRemaining past the (already advanced) clock.
func waitForLeaseRenewal(t *testing.T, fixture *caseRunnerFixture, clock *manualClock, wantRemaining time.Duration) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		row := fixture.caseRuns.get(t, 3001)
		if row.LeaseUntil != nil && row.LeaseUntil.Sub(clock.Now()) >= wantRemaining {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("independent lease heartbeat did not renew the case_run lease: lease_until=%v now=%v want remaining >= %v",
				row.LeaseUntil, clock.Now(), wantRemaining)
		}
		time.Sleep(time.Millisecond)
	}
}

func eventPayloads(events *caseRunnerEvents, eventType string) []map[string]any {
	events.mu.Lock()
	defer events.mu.Unlock()
	payloads := make([]map[string]any, 0)
	for _, event := range events.events {
		if event.Type == eventType {
			payloads = append(payloads, event.Payload)
		}
	}
	return payloads
}

func countEvents(events *caseRunnerEvents, eventType string) int {
	return len(eventPayloads(events, eventType))
}

// TestLeaseNotExpiredDuringSlowAction: a single executor action may take 70s
// (executor cap 60s, client bound 75s). The case_run lease must be longer than
// that bound, and the independent heartbeat must keep renewing while no step or
// assertion result is written. Recovery must therefore never emit executor_lost.
func TestLeaseNotExpiredDuringSlowAction(t *testing.T) {
	clock := newManualClock(time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC))
	fixture := newHeartbeatFixture(t, clock, 2*time.Millisecond)
	prepareCaseRun(t, fixture)
	defer fixture.runner.ClearRun(3001)

	row := fixture.caseRuns.get(t, 3001)
	if row.LeaseUntil == nil {
		t.Fatal("Prepare did not set a case_run lease")
	}
	lease := row.LeaseUntil.Sub(clock.Now())
	if lease < 75*time.Second {
		t.Fatalf("case_run lease = %v, want at least the 75s executor action bound", lease)
	}
	if lease <= executorexec.DefaultTimeout {
		t.Fatalf("case_run lease = %v, want longer than the executor client bound %v", lease, executorexec.DefaultTimeout)
	}

	// One 70s action with no execution.step_record/execution.assertion_record.
	clock.Advance(70 * time.Second)
	waitForLeaseRenewal(t, fixture, clock, 75*time.Second)
	// Extend past the original lease to prove the heartbeat (not just the
	// initial 90s lease) is what kept the run alive.
	clock.Advance(30 * time.Second)

	recovery := executionapp.NewRecovery(executionapp.RecoveryDeps{
		Intents:  fixture.intents,
		Waves:    &fakeSchedulerWaveRepo{wave: &execution.RunWave{ID: 2001, VersionID: 10, Status: execution.WaveRunning}},
		CaseRuns: fixture.caseRuns,
		Clock:    clock,
		Events:   fixture.events,
	})
	if err := recovery.Reconcile(context.Background()); err != nil {
		t.Fatalf("Recovery.Reconcile: %v", err)
	}

	row = fixture.caseRuns.get(t, 3001)
	if row.Status != execution.CaseRunRunning {
		t.Fatalf("slow action was marked %q (executor_lost misclassification)", row.Status)
	}
	if row.FailureClass != "" {
		t.Fatalf("slow action produced failure class %q, want none", row.FailureClass)
	}
	if got := countEvents(fixture.events, executionapp.EventCaseRunFinished); got != 0 {
		t.Fatalf("slow action emitted %d case_run.finished event(s), want 0", got)
	}
}

// TestRecoveryExhaustedAttemptsKeepsExecutorLost proves an expired lease at the
// dispatch budget is failed as executor_lost, not as environment (frontend used
// to show “环境异常” while the reason said executor_lost).
func TestRecoveryExhaustedAttemptsKeepsExecutorLost(t *testing.T) {
	clock := newManualClock(time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC))
	fixture := newCaseRunnerFixture(t)
	expired := clock.Now().Add(-time.Second)
	fixture.caseRuns.mu.Lock()
	row := fixture.caseRuns.rows[3001]
	row.Status = execution.CaseRunRunning
	row.Attempts = 3
	row.LeaseUntil = &expired
	fixture.caseRuns.rows[3001] = row
	fixture.caseRuns.mu.Unlock()

	recovery := executionapp.NewRecovery(executionapp.RecoveryDeps{
		Intents:     fixture.intents,
		Waves:       &fakeSchedulerWaveRepo{wave: &execution.RunWave{ID: 2001, VersionID: 10, Status: execution.WaveRunning}},
		CaseRuns:    fixture.caseRuns,
		Clock:       clock,
		Events:      fixture.events,
		MaxAttempts: 3,
	})
	if err := recovery.Reconcile(context.Background()); err != nil {
		t.Fatalf("Recovery.Reconcile: %v", err)
	}

	stored := fixture.caseRuns.get(t, 3001)
	if stored.Status != execution.CaseRunFailed {
		t.Fatalf("exhausted lease status = %q, want failed", stored.Status)
	}
	if stored.FailureClass != execution.FailureClassExecutorLost {
		t.Fatalf("exhausted lease failure class = %q, want executor_lost (not environment)", stored.FailureClass)
	}
	payloads := eventPayloads(fixture.events, executionapp.EventCaseRunFinished)
	if len(payloads) != 1 {
		t.Fatalf("case_run.finished events = %d, want 1", len(payloads))
	}
	if got, _ := payloads[0]["failure_class"].(string); got != string(execution.FailureClassExecutorLost) {
		t.Fatalf("event failure_class = %q, want executor_lost", got)
	}
}

// ---------- Task 9: dispatch health ----------

// fakeSchedulerWaveRepo is the minimal WaveRepository used by the scheduler and
// recovery tests here.
type fakeSchedulerWaveRepo struct {
	wave *execution.RunWave
}

func (r *fakeSchedulerWaveRepo) CreateWithCaseRuns(context.Context, execution.RunWave, []execution.ExecutionIntent, []execution.CaseRun) error {
	return nil
}

func (r *fakeSchedulerWaveRepo) Get(_ context.Context, id int64) (execution.RunWave, error) {
	if r.wave == nil || r.wave.ID != id {
		return execution.RunWave{}, execution.ErrNotFound
	}
	return *r.wave, nil
}

func (r *fakeSchedulerWaveRepo) GetActive(_ context.Context, versionID int64) (*execution.RunWave, error) {
	if r.wave == nil || r.wave.VersionID != versionID {
		return nil, nil
	}
	return r.wave, nil
}

func (r *fakeSchedulerWaveRepo) List(context.Context, int64, int, *int64) ([]execution.RunWave, bool, error) {
	if r.wave == nil {
		return nil, false, nil
	}
	return []execution.RunWave{*r.wave}, false, nil
}

func (r *fakeSchedulerWaveRepo) Update(context.Context, execution.RunWave) (bool, error) {
	return true, nil
}

func (r *fakeSchedulerWaveRepo) Transition(context.Context, int64, []execution.WaveStatus, execution.WaveStatus, execution.RunWave) (bool, error) {
	return false, nil
}

func (r *fakeSchedulerWaveRepo) Counts(context.Context, int64) (execution.WaveCounts, error) {
	return execution.WaveCounts{}, nil
}

func (r *fakeSchedulerWaveRepo) ListActive(context.Context) ([]execution.RunWave, error) {
	if r.wave == nil {
		return nil, nil
	}
	return []execution.RunWave{*r.wave}, nil
}

type fakeDispatchRegistry struct {
	executor *execution.ExecutorInstance
}

func (r *fakeDispatchRegistry) Select(context.Context) (*execution.ExecutorInstance, error) {
	return r.executor, nil
}

type fakeDispatchQueue struct {
	mu    sync.Mutex
	items []executionapp.WorkItemInput
}

func (q *fakeDispatchQueue) Ensure(_ context.Context, in executionapp.WorkItemInput) (int64, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append(q.items, in)
	return int64(len(q.items)), true, nil
}

func (q *fakeDispatchQueue) MarkWaitingHuman(context.Context, int64, int64) error { return nil }
func (q *fakeDispatchQueue) Finish(context.Context, int64, string) error          { return nil }

func (q *fakeDispatchQueue) count() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

func newDispatchScheduler(fixture *caseRunnerFixture, executor *execution.ExecutorInstance) (*executionapp.Scheduler, *fakeDispatchQueue) {
	queue := &fakeDispatchQueue{}
	scheduler := executionapp.NewScheduler(executionapp.SchedulerDeps{
		Intents:   fixture.intents,
		Waves:     &fakeSchedulerWaveRepo{wave: &execution.RunWave{ID: 2001, VersionID: 10, Status: execution.WaveRunning}},
		CaseRuns:  fixture.caseRuns,
		Cases:     fixture.cases,
		Executors: &fakeDispatchRegistry{executor: executor},
		WorkQueue: queue,
		IDs:       fixture.ids,
		Clock:     fixture.clock,
		Events:    fixture.events,
		Instance:  "api-1",
		// Mirrors the production wiring: even an explicit short scheduler lease
		// must be clamped above the agent-side case lease.
		LeaseTTL: 30 * time.Second,
	})
	return scheduler, queue
}

// TestDegradedExecutorNotDispatched proves a degraded executor (active row with
// the degraded marker) cannot start a pending case_run; the run stays pending
// and a blocked event explains why instead of a silent no-op.
func TestDegradedExecutorNotDispatched(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	executor := &execution.ExecutorInstance{
		ID: 7, Endpoint: "http://127.0.0.1:13336", Capacity: 8,
		Status: execution.ExecutorActive,
		Labels: map[string]any{"eztest.degraded": true},
	}
	scheduler, queue := newDispatchScheduler(fixture, executor)

	if err := scheduler.ReconcileVersion(context.Background(), 10); err != nil {
		t.Fatalf("ReconcileVersion: %v", err)
	}
	row := fixture.caseRuns.get(t, 3001)
	if row.Status != execution.CaseRunPending || row.Attempts != 1 || row.DispatchEpoch != 3 {
		t.Fatalf("degraded executor started the run: %+v", row)
	}
	if queue.count() != 0 {
		t.Fatalf("degraded executor ensured %d work item(s)", queue.count())
	}
	if got := countEvents(fixture.events, executionapp.EventCaseRunStarted); got != 0 {
		t.Fatalf("degraded executor emitted %d case_run.started event(s)", got)
	}
	payloads := eventPayloads(fixture.events, executionapp.EventCaseRunBlocked)
	if len(payloads) != 1 {
		t.Fatalf("blocked events = %d, want 1: %v", len(payloads), fixture.events.types())
	}
	if got, _ := payloads[0]["reason"].(string); got != "executor_degraded" {
		t.Fatalf("blocked reason = %q, want executor_degraded", got)
	}

	// The same blocked state is not re-published every scheduler tick.
	if err := scheduler.ReconcileVersion(context.Background(), 10); err != nil {
		t.Fatalf("second ReconcileVersion: %v", err)
	}
	if got := len(eventPayloads(fixture.events, executionapp.EventCaseRunBlocked)); got != 1 {
		t.Fatalf("blocked events after second tick = %d, want 1", got)
	}
}

// TestNoExecutorEmitsBlocked proves a missing executor is a visible blocked
// state, not a silent pending run.
func TestNoExecutorEmitsBlocked(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	scheduler, queue := newDispatchScheduler(fixture, nil)

	if err := scheduler.ReconcileVersion(context.Background(), 10); err != nil {
		t.Fatalf("ReconcileVersion: %v", err)
	}
	row := fixture.caseRuns.get(t, 3001)
	if row.Status != execution.CaseRunPending {
		t.Fatalf("no-executor run status = %q, want pending", row.Status)
	}
	if queue.count() != 0 {
		t.Fatalf("no executor ensured %d work item(s)", queue.count())
	}
	payloads := eventPayloads(fixture.events, executionapp.EventCaseRunBlocked)
	if len(payloads) != 1 {
		t.Fatalf("blocked events = %d, want 1: %v", len(payloads), fixture.events.types())
	}
	if got, _ := payloads[0]["reason"].(string); got != "no_executor" {
		t.Fatalf("blocked reason = %q, want no_executor", got)
	}
	if got, _ := payloads[0]["case_run_id"].(string); got != "3001" {
		t.Fatalf("blocked case_run_id = %q, want 3001", got)
	}
}

// TestDispatchPersistsExecutorSnapshotInSummary pins v0.2.3 X-01: every dispatch
// stamps the chosen executor (owner/endpoint/status/heartbeat/version/route) in
// case_run.summary["dispatch"] so a later failure can be attributed without a
// new table or migration.
func TestDispatchPersistsExecutorSnapshotInSummary(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	heartbeat := fixture.clock.Now().Add(-2 * time.Second)
	executor := &execution.ExecutorInstance{
		ID: 7, Endpoint: "http://127.0.0.1:13336", Capacity: 8,
		Status: execution.ExecutorActive, Version: "0.2.0-p3",
		LastHeartbeat: &heartbeat,
	}
	scheduler, queue := newDispatchScheduler(fixture, executor)

	if err := scheduler.ReconcileVersion(context.Background(), 10); err != nil {
		t.Fatalf("ReconcileVersion: %v", err)
	}
	row := fixture.caseRuns.get(t, 3001)
	if row.Status != execution.CaseRunRunning {
		t.Fatalf("run status = %q, want running", row.Status)
	}
	if queue.count() != 1 {
		t.Fatalf("work items = %d, want 1", queue.count())
	}
	dispatch := row.Summary.Dispatch
	if dispatch == nil {
		t.Fatalf("case_run.summary.dispatch is nil: %+v", row.Summary)
	}
	if dispatch.OwnerExecutorID != 7 || dispatch.Endpoint != "http://127.0.0.1:13336" {
		t.Fatalf("dispatch owner/endpoint = %+v", dispatch)
	}
	if dispatch.ExecutorStatus != string(execution.ExecutorActive) || dispatch.ExecutorVersion != "0.2.0-p3" {
		t.Fatalf("dispatch executor status/version = %+v", dispatch)
	}
	if dispatch.LastHeartbeat != heartbeat.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("dispatch last_heartbeat = %q, want %q", dispatch.LastHeartbeat, heartbeat.UTC().Format(time.RFC3339Nano))
	}
	if !dispatch.RegistryHit {
		t.Fatalf("dispatch registry_hit = false, want true for a selected row")
	}
	if dispatch.DispatchAt == "" {
		t.Fatalf("dispatch_at is empty: %+v", dispatch)
	}
	if dispatch.Attempt != row.Attempts {
		t.Fatalf("dispatch attempt = %d, want %d", dispatch.Attempt, row.Attempts)
	}
}

// TestTerminalSummaryPreservesDispatchSnapshot proves the terminal verdict write
// does not drop the dispatch snapshot assembled at dispatch time.
func TestTerminalSummaryPreservesDispatchSnapshot(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	snapshot := &execution.CaseRunDispatchDiagnostic{
		OwnerExecutorID: 7,
		Endpoint:        "http://127.0.0.1:13336",
		ExecutorStatus:  string(execution.ExecutorActive),
		RegistryHit:     true,
		DispatchAt:      "2026-11-01T01:00:00Z",
	}
	run := fixture.caseRuns.rows[3001]
	run.Summary.Dispatch = snapshot
	fixture.caseRuns.rows[3001] = run
	prepareCaseRun(t, fixture)
	if _, err := fixture.runner.StepRecord(context.Background(), p3Scope(), tools.StepRecordInput{
		Seq: 1, Status: "passed", Observed: "ok",
	}); err != nil {
		t.Fatalf("StepRecord: %v", err)
	}
	if _, err := fixture.runner.Finish(context.Background(), p3Scope(), execution.FinishCandidate{
		Verdict: execution.CaseRunPassed,
	}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	stored := fixture.caseRuns.get(t, 3001)
	if stored.Summary.Dispatch == nil || stored.Summary.Dispatch.OwnerExecutorID != 7 {
		t.Fatalf("terminal summary dropped the dispatch snapshot: %+v", stored.Summary)
	}
}

// TestLeaseHeartbeatRetriesTransientStoreError: one transient ClaimLease error
// must not permanently disable renewal — the timer retries while the run context
// is alive, so a later slow action cannot be misread as executor_lost.
func TestLeaseHeartbeatRetriesTransientStoreError(t *testing.T) {
	clock := newManualClock(time.Date(2026, 11, 2, 2, 0, 0, 0, time.UTC))
	fixture := newHeartbeatFixture(t, clock, 2*time.Millisecond)
	fixture.caseRuns.failNextClaims(1)
	prepareCaseRun(t, fixture)

	deadline := time.Now().Add(3 * time.Second)
	for fixture.caseRuns.claimCount() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("lease heartbeat stopped after a transient ClaimLease error: renewal attempts=%d (want a retry)",
				fixture.caseRuns.claimCount())
		}
		time.Sleep(time.Millisecond)
	}

	// The retried heartbeat keeps the lease alive across a long action.
	clock.Advance(70 * time.Second)
	waitForLeaseRenewal(t, fixture, clock, 75*time.Second)
}

// TestDispatchLeaseSurvivesDelayedClaim: the scheduler stamps the case_run lease
// at dispatch, but the agent only claims it when a worker is free. The dispatch
// lease must not expire while the work item waits in the queue, otherwise
// recovery interrupts and redispatches a run that never started.
func TestDispatchLeaseSurvivesDelayedClaim(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	executor := &execution.ExecutorInstance{
		ID: 7, Endpoint: "http://127.0.0.1:13336", Capacity: 8,
		Status: execution.ExecutorActive,
	}
	scheduler, queue := newDispatchScheduler(fixture, executor)
	if err := scheduler.ReconcileVersion(context.Background(), 10); err != nil {
		t.Fatalf("ReconcileVersion: %v", err)
	}
	dispatched := fixture.caseRuns.get(t, 3001)
	if dispatched.Status != execution.CaseRunRunning {
		t.Fatalf("dispatched status = %q, want running", dispatched.Status)
	}
	if dispatched.LeaseUntil == nil {
		t.Fatal("dispatch did not stamp a case_run lease")
	}
	if lease := dispatched.LeaseUntil.Sub(fixture.clock.Now()); lease < 90*time.Second {
		t.Fatalf("dispatch lease = %v, want at least the 90s agent lease", lease)
	}

	// The work item waits 45s behind a busy worker before any agent claims it.
	fixture.clock.Advance(45 * time.Second)
	recovery := executionapp.NewRecovery(executionapp.RecoveryDeps{
		Intents:     fixture.intents,
		Waves:       &fakeSchedulerWaveRepo{wave: &execution.RunWave{ID: 2001, VersionID: 10, Status: execution.WaveRunning}},
		CaseRuns:    fixture.caseRuns,
		Clock:       fixture.clock,
		Events:      fixture.events,
		MaxAttempts: 3,
	})
	if err := recovery.Reconcile(context.Background()); err != nil {
		t.Fatalf("Recovery.Reconcile: %v", err)
	}
	stored := fixture.caseRuns.get(t, 3001)
	if stored.Status != execution.CaseRunRunning {
		t.Fatalf("dispatch lease expired while the work item waited: status=%q attempts=%d", stored.Status, stored.Attempts)
	}
	if got := countEvents(fixture.events, executionapp.EventCaseRunFinished); got != 0 {
		t.Fatalf("delayed dispatch emitted %d case_run.finished event(s), want 0", got)
	}

	// The delayed claim still owns the dispatch fence and starts normally.
	item := queuedWorkItem(t, queue)
	if _, err := fixture.runner.Prepare(fixture.runCtx, item); err != nil {
		t.Fatalf("delayed Prepare after 45s wait: %v", err)
	}
	claimed := fixture.caseRuns.get(t, 3001)
	if claimed.Status != execution.CaseRunRunning || claimed.LeaseUntil == nil ||
		!claimed.LeaseUntil.After(fixture.clock.Now()) {
		t.Fatalf("delayed claim did not refresh the lease: %+v", claimed)
	}
}

// queuedWorkItem rebuilds the agent work item the scheduler enqueued.
func queuedWorkItem(t *testing.T, queue *fakeDispatchQueue) agent.WorkItem {
	t.Helper()
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.items) != 1 {
		t.Fatalf("ensured work items = %d, want 1", len(queue.items))
	}
	queued := queue.items[0]
	payload, err := json.Marshal(queued.Payload)
	if err != nil {
		t.Fatalf("marshal work item payload: %v", err)
	}
	return agent.WorkItem{ID: 9001, VersionID: queued.VersionID, Type: queued.Type, Payload: payload}
}

// TestFinalizeMapsExecutorUnavailableToExecutorLost: an unreachable executor is
// an executor failure, not an environment failure; the frontend maps it to
// “执行器失联”.
func TestFinalizeMapsExecutorUnavailableToExecutorLost(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	executorUnavailable := platform.NewError(503, "executor_unavailable", "executor is offline")
	executorUnavailable.Retryable = true
	runErr := &agentapp.RunError{Reason: "tool_failure", Retryable: true, Cause: executorUnavailable}
	if err := fixture.runner.Finalize(fixture.runCtx, 3001, 3, agentapp.RunResult{}, runErr, 1, 1); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	stored := fixture.caseRuns.get(t, 3001)
	if stored.Status != execution.CaseRunFailed {
		t.Fatalf("status = %q, want failed", stored.Status)
	}
	if stored.FailureClass != execution.FailureClassExecutorLost {
		t.Fatalf("failure class = %q, want executor_lost (not environment)", stored.FailureClass)
	}
}

// TestStaleFinalizeDoesNotStopSupersedingHeartbeat: a late Finalize from an old
// dispatch epoch must neither finalize nor stop the renewal timer of the new
// dispatch that superseded it.
func TestStaleFinalizeDoesNotStopSupersedingHeartbeat(t *testing.T) {
	clock := newManualClock(time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC))
	fixture := newHeartbeatFixture(t, clock, 2*time.Millisecond)
	prepareCaseRun(t, fixture) // old dispatch epoch 3

	fixture.caseRuns.mu.Lock()
	row := fixture.caseRuns.rows[3001]
	row.Status = execution.CaseRunPending
	row.DispatchEpoch = 4
	row.DispatchToken = "dispatch-token-4"
	row.LeaseUntil = nil
	fixture.caseRuns.rows[3001] = row
	fixture.caseRuns.mu.Unlock()
	if _, err := fixture.runner.Prepare(fixture.runCtx, p3WorkItemFor(4, "dispatch-token-4")); err != nil {
		t.Fatalf("Prepare superseding dispatch: %v", err)
	}

	if err := fixture.runner.Finalize(fixture.runCtx, 3001, 3, agentapp.RunResult{}, nil, 1, 3); err != nil {
		t.Fatalf("stale Finalize: %v", err)
	}
	stored := fixture.caseRuns.get(t, 3001)
	if stored.Status != execution.CaseRunRunning || stored.DispatchEpoch != 4 {
		t.Fatalf("stale Finalize touched the superseding run: %+v", stored)
	}

	clock.Advance(70 * time.Second)
	waitForLeaseRenewal(t, fixture, clock, 75*time.Second)
}
