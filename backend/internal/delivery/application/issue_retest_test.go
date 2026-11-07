package deliveryapp

import (
	"context"
	"errors"
	"reflect"
	"testing"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// retestFixture seeds a confirmed issue with one linked source case_run and a
// retest wave carrying one pending case_run.
func retestFixture(t *testing.T) (*issueServiceFixture, int64, int64) {
	t.Helper()
	f := newIssueServiceFixture()
	const issueID, caseRunID = int64(11), int64(501)
	f.repo.getIssues[issueID] = delivery.Issue{
		ID:               issueID,
		VersionID:        7,
		Title:            "checkout fails",
		Status:           delivery.IssueStatusConfirmed,
		Category:         "api",
		Severity:         "major",
		Source:           delivery.IssueManualSource,
		ExpectedRevision: 2,
		RetestStatus:     delivery.IssueRetestStatusNotRetested,
	}
	f.repo.refs = []delivery.IssueRef{{
		ID: 1, IssueID: issueID, Kind: delivery.IssueRefKindCaseRun,
		RefID: caseRunID, Role: delivery.IssueRefRoleSource,
	}}
	f.exec.snapshots[caseRunID] = CaseRunSnapshot{
		ID: caseRunID, VersionID: 7, CaseVersionID: 71, Status: "failed",
	}
	f.retest.wave = RetestWave{
		ID:     900,
		Status: "running",
		CaseRuns: []RetestCaseRun{
			{ID: 901, IntentID: 1001, CaseVersionID: 71, Status: "pending"},
		},
	}
	return f, issueID, caseRunID
}

// retestFixtureAddSecondCaseRun links a second source case_run (case version
// 72) so a multi-case retest wave can be built.
func retestFixtureAddSecondCaseRun(f *issueServiceFixture, issueID int64) {
	f.repo.refs = append(f.repo.refs, delivery.IssueRef{
		ID: 2, IssueID: issueID, Kind: delivery.IssueRefKindCaseRun,
		RefID: 502, Role: delivery.IssueRefRoleSource,
	})
	f.exec.snapshots[502] = CaseRunSnapshot{ID: 502, VersionID: 7, CaseVersionID: 72}
}

func (f *issueServiceFixture) retestEventCount(eventType string) int {
	count := 0
	for _, record := range f.events.records {
		if record.eventType == eventType {
			count++
		}
	}
	return count
}

func (f *issueServiceFixture) hasRetestRef(issueID int64, kind string, refID int64) bool {
	for _, ref := range f.repo.refs {
		if (ref.IssueID == 0 || ref.IssueID == issueID) && ref.Kind == kind && ref.RefID == refID &&
			ref.Role == delivery.IssueRefRoleRetest {
			return true
		}
	}
	return false
}

// TestStartRetestAndWriteback is the W2-5 acceptance path: StartRetest creates a
// PurposeRetest intent, starts the wave, links the retest wave/case_run refs and
// marks the issue retesting; a passed retest then writes passed back and emits
// issue.retest_completed exactly once.
func TestStartRetestAndWriteback(t *testing.T) {
	f, issueID, _ := retestFixture(t)
	ctx := context.Background()

	result, err := f.svc.StartRetest(ctx, 7, issueID, 9)
	if err != nil {
		t.Fatalf("StartRetest: %v", err)
	}
	if result.Wave.ID != 900 || result.Wave.Status != "running" {
		t.Fatalf("retest wave = %+v, want 900/running", result.Wave)
	}
	if !reflect.DeepEqual(result.CaseRunIDs, []int64{901}) {
		t.Fatalf("retest result case_run_ids = %v, want [901]", result.CaseRunIDs)
	}
	if len(f.retest.createCalls) != 1 {
		t.Fatalf("CreateRetestIntent calls = %d, want 1", len(f.retest.createCalls))
	}
	if call := f.retest.createCalls[0]; call.versionID != 7 || call.caseVersionID != 71 || call.actorID != 9 {
		t.Fatalf("CreateRetestIntent = %+v, want version 7 case_version 71 actor 9", call)
	}
	if len(f.retest.startCalls) != 1 || f.retest.startCalls[0] != 7 {
		t.Fatalf("StartRetestWave calls = %v, want [7]", f.retest.startCalls)
	}

	stored := f.repo.getIssues[issueID]
	if stored.RetestStatus != delivery.IssueRetestStatusRetesting {
		t.Fatalf("retest_status = %q, want retesting", stored.RetestStatus)
	}
	if stored.LastRetestWaveID == nil || *stored.LastRetestWaveID != 900 {
		t.Fatalf("last_retest_wave_id = %v, want 900", stored.LastRetestWaveID)
	}
	if !f.hasRetestRef(issueID, delivery.IssueRefKindWave, 900) {
		t.Fatalf("missing wave/900 role=retest ref: %+v", f.repo.refs)
	}
	if !f.hasRetestRef(issueID, delivery.IssueRefKindCaseRun, 901) {
		t.Fatalf("missing case_run/901 role=retest ref: %+v", f.repo.refs)
	}

	started := f.events.records[0]
	if started.eventType != issueEventRetestStarted || started.versionID != 7 {
		t.Fatalf("started event = %+v", started)
	}
	wantStarted := map[string]any{
		"issue_id":      "11",
		"version_id":    "7",
		"wave_id":       "900",
		"case_run_ids":  []string{"901"},
		"retest_status": "retesting",
		"error":         "",
	}
	if !reflect.DeepEqual(started.payload, wantStarted) {
		t.Fatalf("started payload = %#v, want %#v", started.payload, wantStarted)
	}

	updated, err := f.svc.OnRetestTerminal(ctx, 900, 901, "passed")
	if err != nil {
		t.Fatalf("OnRetestTerminal: %v", err)
	}
	if updated.RetestStatus != delivery.IssueRetestStatusPassed {
		t.Fatalf("retest_status = %q, want passed", updated.RetestStatus)
	}
	if updated.LastRetestWaveID == nil || *updated.LastRetestWaveID != 900 {
		t.Fatalf("last_retest_wave_id = %v, want 900", updated.LastRetestWaveID)
	}
	if updated.LastRetestCaseRunID == nil || *updated.LastRetestCaseRunID != 901 {
		t.Fatalf("last_retest_case_run_id = %v, want 901", updated.LastRetestCaseRunID)
	}
	if got := f.retestEventCount(issueEventRetestCompleted); got != 1 {
		t.Fatalf("issue.retest_completed events = %d, want 1", got)
	}
	if got := f.retestEventCount(issueEventRetestFailed); got != 0 {
		t.Fatalf("issue.retest_failed events = %d, want 0", got)
	}
	completed := f.events.records[len(f.events.records)-1]
	if completed.eventType != issueEventRetestCompleted {
		t.Fatalf("last event = %q, want %q", completed.eventType, issueEventRetestCompleted)
	}
	if completed.payload["retest_status"] != "passed" {
		t.Fatalf("completed payload = %#v, want retest_status passed", completed.payload)
	}
	if completed.payload["error"] != "" {
		t.Fatalf("completed payload error = %#v, want empty", completed.payload["error"])
	}

	// Idempotent terminal callback: no duplicate event, same status.
	again, err := f.svc.OnRetestTerminal(ctx, 900, 901, "passed")
	if err != nil {
		t.Fatalf("duplicate OnRetestTerminal: %v", err)
	}
	if again.RetestStatus != delivery.IssueRetestStatusPassed {
		t.Fatalf("duplicate retest_status = %q, want passed", again.RetestStatus)
	}
	if got := f.retestEventCount(issueEventRetestCompleted); got != 1 {
		t.Fatalf("duplicate issue.retest_completed events = %d, want 1", got)
	}
}

// TestStartRetestRejectsActiveRetest proves the 409 guard: while a wave is
// active no second retest may be started and no intent is created.
func TestStartRetestRejectsActiveRetest(t *testing.T) {
	f, issueID, _ := retestFixture(t)
	f.repo.getIssues[issueID] = func() delivery.Issue {
		issue := f.repo.getIssues[issueID]
		issue.RetestStatus = delivery.IssueRetestStatusRetesting
		return issue
	}()
	f.retest.active = &RetestWave{ID: 899, Status: "running"}

	_, err := f.svc.StartRetest(context.Background(), 7, issueID, 9)
	var conflict *IssueStateConflictError
	if !errors.As(err, &conflict) || conflict.Reason != IssueConflictRetestActive {
		t.Fatalf("error = %v, want retest_active state conflict", err)
	}
	if len(f.retest.createCalls) != 0 {
		t.Fatalf("CreateRetestIntent calls = %d, want 0", len(f.retest.createCalls))
	}
	if len(f.retest.startCalls) != 0 {
		t.Fatalf("StartRetestWave calls = %d, want 0", len(f.retest.startCalls))
	}
}

// TestStartRetestRejectsClosedIssue proves dismissed/merged issues are not
// retestable.
func TestStartRetestRejectsClosedIssue(t *testing.T) {
	for _, status := range []delivery.IssueStatus{
		delivery.IssueStatusDismissed,
		delivery.IssueStatusMerged,
	} {
		f, issueID, _ := retestFixture(t)
		issue := f.repo.getIssues[issueID]
		issue.Status = status
		f.repo.getIssues[issueID] = issue

		_, err := f.svc.StartRetest(context.Background(), 7, issueID, 9)
		var conflict *IssueStateConflictError
		if !errors.As(err, &conflict) || conflict.Reason != IssueConflictNotRetestable {
			t.Fatalf("%s error = %v, want issue_not_retestable", status, err)
		}
	}
}

// TestStartRetestRequiresLinkedCaseRun proves an issue without a linked
// case_run cannot be retested.
func TestStartRetestRequiresLinkedCaseRun(t *testing.T) {
	f, issueID, _ := retestFixture(t)
	f.repo.refs = nil

	_, err := f.svc.StartRetest(context.Background(), 7, issueID, 9)
	var conflict *IssueStateConflictError
	if !errors.As(err, &conflict) || conflict.Reason != IssueConflictNotRetestable {
		t.Fatalf("error = %v, want issue_not_retestable", err)
	}
}

// TestStartRetestSelectionRestrictsCaseVersions proves the optional case_run
// selection narrows the created retest intents.
func TestStartRetestSelectionRestrictsCaseVersions(t *testing.T) {
	f, issueID, caseRunID := retestFixture(t)
	// A second source case_run linked to a distinct case version.
	f.repo.refs = append(f.repo.refs, delivery.IssueRef{
		ID: 2, IssueID: issueID, Kind: delivery.IssueRefKindCaseRun,
		RefID: 502, Role: delivery.IssueRefRoleSource,
	})
	f.exec.snapshots[502] = CaseRunSnapshot{ID: 502, VersionID: 7, CaseVersionID: 72}
	f.retest.wave = RetestWave{
		ID: 900, Status: "running",
		CaseRuns: []RetestCaseRun{
			{ID: 901, IntentID: 1001, CaseVersionID: 71, Status: "pending"},
			{ID: 902, IntentID: 1002, CaseVersionID: 72, Status: "pending"},
		},
	}

	if _, err := f.svc.StartRetest(context.Background(), 7, issueID, 9, caseRunID); err != nil {
		t.Fatalf("StartRetest selection: %v", err)
	}
	if len(f.retest.createCalls) != 1 || f.retest.createCalls[0].caseVersionID != 71 {
		t.Fatalf("CreateRetestIntent calls = %+v, want only case_version 71", f.retest.createCalls)
	}
	if f.hasRetestRef(issueID, delivery.IssueRefKindCaseRun, 902) {
		t.Fatalf("unselected case_run 902 was linked: %+v", f.repo.refs)
	}
}

// TestStartRetestRollsBackIntentsOnWaveFailure proves a partial retest batch is
// cancelled when the wave cannot be started.
func TestStartRetestRollsBackIntentsOnWaveFailure(t *testing.T) {
	f, issueID, _ := retestFixture(t)
	f.retest.waveErr = errors.New("planner unavailable")

	_, err := f.svc.StartRetest(context.Background(), 7, issueID, 9)
	if err == nil {
		t.Fatal("StartRetest with a failing wave start succeeded")
	}
	if len(f.retest.canceled) != 1 || f.retest.canceled[0] != 1001 {
		t.Fatalf("cancelled intents = %v, want [1001]", f.retest.canceled)
	}
}

// TestOnRetestTerminalFailedAndOther pins the two non-passed writebacks:
// failed => issue.retest_failed, any other status => keep retesting + reason.
func TestOnRetestTerminalFailedAndOther(t *testing.T) {
	t.Run("failed", func(t *testing.T) {
		f, issueID, _ := retestFixture(t)
		ctx := context.Background()
		if _, err := f.svc.StartRetest(ctx, 7, issueID, 9); err != nil {
			t.Fatalf("StartRetest: %v", err)
		}
		updated, err := f.svc.OnRetestTerminal(ctx, 900, 901, "failed")
		if err != nil {
			t.Fatalf("OnRetestTerminal: %v", err)
		}
		if updated.RetestStatus != delivery.IssueRetestStatusFailed {
			t.Fatalf("retest_status = %q, want failed", updated.RetestStatus)
		}
		if got := f.retestEventCount(issueEventRetestFailed); got != 1 {
			t.Fatalf("issue.retest_failed events = %d, want 1", got)
		}
		failedEvent := f.events.records[len(f.events.records)-1]
		if failedEvent.eventType != issueEventRetestFailed {
			t.Fatalf("last event = %q, want %q", failedEvent.eventType, issueEventRetestFailed)
		}
		if failedEvent.payload["error"] != "failed" {
			t.Fatalf("failed payload error = %#v, want %q", failedEvent.payload["error"], "failed")
		}
	})

	t.Run("other keeps retesting", func(t *testing.T) {
		f, issueID, _ := retestFixture(t)
		ctx := context.Background()
		if _, err := f.svc.StartRetest(ctx, 7, issueID, 9); err != nil {
			t.Fatalf("StartRetest: %v", err)
		}
		updated, err := f.svc.OnRetestTerminal(ctx, 900, 901, "blocked")
		if err != nil {
			t.Fatalf("OnRetestTerminal: %v", err)
		}
		if updated.RetestStatus != delivery.IssueRetestStatusRetesting {
			t.Fatalf("retest_status = %q, want retesting", updated.RetestStatus)
		}
		if updated.LastRetestCaseRunID == nil || *updated.LastRetestCaseRunID != 901 {
			t.Fatalf("last_retest_case_run_id = %v, want 901", updated.LastRetestCaseRunID)
		}
		if len(f.repo.retestResultCalls) != 1 {
			t.Fatalf("ApplyRetestResult calls = %d, want 1", len(f.repo.retestResultCalls))
		}
		if reason := f.repo.retestResultCalls[0].Reason; reason != "blocked" {
			t.Fatalf("result reason = %q, want blocked", reason)
		}
		if got := f.retestEventCount(issueEventRetestCompleted) + f.retestEventCount(issueEventRetestFailed); got != 0 {
			t.Fatalf("terminal events for other status = %d, want 0", got)
		}
	})
}

// TestOnRetestWaveTerminalAggregates proves the wave-level writeback: any failed
// case_run fails the retest, all passed passes it, other statuses keep
// retesting; a repeated terminal notification is idempotent.
func TestOnRetestWaveTerminalAggregates(t *testing.T) {
	t.Run("any failed", func(t *testing.T) {
		f, issueID, _ := retestFixture(t)
		ctx := context.Background()
		retestFixtureAddSecondCaseRun(f, issueID)
		f.retest.wave.CaseRuns = []RetestCaseRun{
			{ID: 901, IntentID: 1001, CaseVersionID: 71, Status: "passed"},
			{ID: 902, IntentID: 1002, CaseVersionID: 72, Status: "failed"},
		}
		f.exec.waveSnapshots[900] = WaveSnapshot{ID: 900, VersionID: 7, CaseRuns: []CaseRunSnapshot{
			{ID: 901, CaseVersionID: 71, Status: "passed"},
			{ID: 902, CaseVersionID: 72, Status: "failed"},
		}}
		if _, err := f.svc.StartRetest(ctx, 7, issueID, 9); err != nil {
			t.Fatalf("StartRetest: %v", err)
		}
		if err := f.svc.OnRetestWaveTerminal(ctx, 900); err != nil {
			t.Fatalf("OnRetestWaveTerminal: %v", err)
		}
		stored := f.repo.getIssues[issueID]
		if stored.RetestStatus != delivery.IssueRetestStatusFailed {
			t.Fatalf("retest_status = %q, want failed", stored.RetestStatus)
		}
		if stored.LastRetestCaseRunID == nil || *stored.LastRetestCaseRunID != 902 {
			t.Fatalf("last_retest_case_run_id = %v, want the failed 902", stored.LastRetestCaseRunID)
		}
		if got := f.retestEventCount(issueEventRetestFailed); got != 1 {
			t.Fatalf("issue.retest_failed events = %d, want 1", got)
		}
		if err := f.svc.OnRetestWaveTerminal(ctx, 900); err != nil {
			t.Fatalf("duplicate OnRetestWaveTerminal: %v", err)
		}
		if got := f.retestEventCount(issueEventRetestFailed); got != 1 {
			t.Fatalf("duplicate issue.retest_failed events = %d, want 1", got)
		}
	})

	t.Run("all passed", func(t *testing.T) {
		f, issueID, _ := retestFixture(t)
		ctx := context.Background()
		retestFixtureAddSecondCaseRun(f, issueID)
		f.retest.wave.CaseRuns = []RetestCaseRun{
			{ID: 901, IntentID: 1001, CaseVersionID: 71, Status: "passed"},
			{ID: 902, IntentID: 1002, CaseVersionID: 72, Status: "passed"},
		}
		f.exec.waveSnapshots[900] = WaveSnapshot{ID: 900, VersionID: 7, CaseRuns: []CaseRunSnapshot{
			{ID: 901, CaseVersionID: 71, Status: "passed"},
			{ID: 902, CaseVersionID: 72, Status: "passed"},
		}}
		if _, err := f.svc.StartRetest(ctx, 7, issueID, 9); err != nil {
			t.Fatalf("StartRetest: %v", err)
		}
		if err := f.svc.OnRetestWaveTerminal(ctx, 900); err != nil {
			t.Fatalf("OnRetestWaveTerminal: %v", err)
		}
		stored := f.repo.getIssues[issueID]
		if stored.RetestStatus != delivery.IssueRetestStatusPassed {
			t.Fatalf("retest_status = %q, want passed", stored.RetestStatus)
		}
		if got := f.retestEventCount(issueEventRetestCompleted); got != 1 {
			t.Fatalf("issue.retest_completed events = %d, want 1", got)
		}
	})

	t.Run("other keeps retesting", func(t *testing.T) {
		f, issueID, _ := retestFixture(t)
		ctx := context.Background()
		f.retest.wave.CaseRuns = []RetestCaseRun{{ID: 901, IntentID: 1001, CaseVersionID: 71, Status: "blocked"}}
		f.exec.waveSnapshots[900] = WaveSnapshot{ID: 900, VersionID: 7, CaseRuns: []CaseRunSnapshot{
			{ID: 901, CaseVersionID: 71, Status: "blocked"},
		}}
		if _, err := f.svc.StartRetest(ctx, 7, issueID, 9); err != nil {
			t.Fatalf("StartRetest: %v", err)
		}
		if err := f.svc.OnRetestWaveTerminal(ctx, 900); err != nil {
			t.Fatalf("OnRetestWaveTerminal: %v", err)
		}
		stored := f.repo.getIssues[issueID]
		if stored.RetestStatus != delivery.IssueRetestStatusRetesting {
			t.Fatalf("retest_status = %q, want retesting", stored.RetestStatus)
		}
		if reason := f.repo.retestResultCalls[0].Reason; reason != "blocked" {
			t.Fatalf("result reason = %q, want blocked", reason)
		}
	})
}

// TestOnRetestWaveTerminalIgnoresRegularWave proves a non-retest wave terminal
// notification is a no-op instead of an error.
func TestOnRetestWaveTerminalIgnoresRegularWave(t *testing.T) {
	f := newIssueServiceFixture()
	if err := f.svc.OnRetestWaveTerminal(context.Background(), 999); err != nil {
		t.Fatalf("OnRetestWaveTerminal for a regular wave: %v", err)
	}
	if len(f.repo.retestResultCalls) != 0 {
		t.Fatalf("retest result writes = %d, want 0", len(f.repo.retestResultCalls))
	}
}

// TestStartRetestLinksOnlyCreatedIntentRuns proves the issue links only the
// case_runs produced by the retest intents it created. The deterministic wave
// may also carry an unrelated open intent for the same case version; its run
// must never be linked or returned for deep links (F2/F5).
func TestStartRetestLinksOnlyCreatedIntentRuns(t *testing.T) {
	f, issueID, _ := retestFixture(t)
	f.retest.wave.CaseRuns = []RetestCaseRun{
		{ID: 901, IntentID: 1001, CaseVersionID: 71, Status: "pending"},
		{ID: 903, IntentID: 999, CaseVersionID: 71, Status: "pending"},
	}

	result, err := f.svc.StartRetest(context.Background(), 7, issueID, 9)
	if err != nil {
		t.Fatalf("StartRetest: %v", err)
	}
	if !reflect.DeepEqual(result.CaseRunIDs, []int64{901}) {
		t.Fatalf("case_run_ids = %v, want only the retest intent run [901]", result.CaseRunIDs)
	}
	if f.hasRetestRef(issueID, delivery.IssueRefKindCaseRun, 903) {
		t.Fatalf("unrelated open-intent run 903 was linked: %+v", f.repo.refs)
	}
}

// TestStartRetestFailsClosedWhenWaveMissesIntent proves a started wave that
// lacks a created retest intent is rejected and the orphan intent is cancelled:
// the issue is never marked retesting without the matching linked run (F2).
func TestStartRetestFailsClosedWhenWaveMissesIntent(t *testing.T) {
	f, issueID, _ := retestFixture(t)
	f.retest.wave.CaseRuns = []RetestCaseRun{
		{ID: 902, IntentID: 999, CaseVersionID: 72, Status: "pending"},
	}

	_, err := f.svc.StartRetest(context.Background(), 7, issueID, 9)
	var conflict *IssueStateConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %v, want a retest state conflict", err)
	}
	if len(f.repo.retestStartCalls) != 0 {
		t.Fatalf("StartRetest repo calls = %d, want 0 after a missing intent", len(f.repo.retestStartCalls))
	}
	if !reflect.DeepEqual(f.retest.canceled, []int64{1001}) {
		t.Fatalf("cancelled intents = %v, want [1001]", f.retest.canceled)
	}
	if stored := f.repo.getIssues[issueID]; stored.RetestStatus != delivery.IssueRetestStatusNotRetested {
		t.Fatalf("retest_status = %q, want not_retested", stored.RetestStatus)
	}
}

// TestStartRetestMapsRepositoryCASConflict proves the repository CAS guard is
// surfaced as the stable 409 retest_active conflict (R-14).
func TestStartRetestMapsRepositoryCASConflict(t *testing.T) {
	f, issueID, _ := retestFixture(t)
	f.repo.retestStartErr = delivery.ErrStateConflict

	_, err := f.svc.StartRetest(context.Background(), 7, issueID, 9)
	var conflict *IssueStateConflictError
	if !errors.As(err, &conflict) || conflict.Reason != IssueConflictRetestActive {
		t.Fatalf("error = %v, want retest_active state conflict", err)
	}
}

// TestOnRetestWaveTerminalNoLinkedRefsDoesNotWriteback proves F2: when the issue
// has no role='retest' case_run refs, a non-empty wave must not fall back to all
// of its runs. The issue keeps retesting and no result is applied.
func TestOnRetestWaveTerminalNoLinkedRefsDoesNotWriteback(t *testing.T) {
	f, issueID, _ := retestFixture(t)
	waveID := int64(900)
	issue := f.repo.getIssues[issueID]
	issue.RetestStatus = delivery.IssueRetestStatusRetesting
	issue.LastRetestWaveID = &waveID
	f.repo.getIssues[issueID] = issue
	f.repo.refs = nil
	f.exec.waveSnapshots[900] = WaveSnapshot{ID: 900, VersionID: 7, CaseRuns: []CaseRunSnapshot{
		{ID: 901, CaseVersionID: 71, Status: "failed"},
	}}

	if err := f.svc.OnRetestWaveTerminal(context.Background(), 900); err != nil {
		t.Fatalf("OnRetestWaveTerminal: %v", err)
	}
	if len(f.repo.retestResultCalls) != 0 {
		t.Fatalf("retest result writes = %d, want 0 without linked refs", len(f.repo.retestResultCalls))
	}
	stored := f.repo.getIssues[issueID]
	if stored.RetestStatus != delivery.IssueRetestStatusRetesting {
		t.Fatalf("retest_status = %q, want retesting", stored.RetestStatus)
	}
	if got := f.retestEventCount(issueEventRetestFailed); got != 0 {
		t.Fatalf("issue.retest_failed events = %d, want 0", got)
	}
}

// TestOnRetestTerminalPublishesOnlyWhenApplied proves F6: when the repository
// reports the projection did not change (a duplicate terminal callback already
// applied by a concurrent writer), no duplicate event is published.
func TestOnRetestTerminalPublishesOnlyWhenApplied(t *testing.T) {
	f, issueID, _ := retestFixture(t)
	ctx := context.Background()
	if _, err := f.svc.StartRetest(ctx, 7, issueID, 9); err != nil {
		t.Fatalf("StartRetest: %v", err)
	}
	f.repo.retestResultNoop = true

	if _, err := f.svc.OnRetestTerminal(ctx, 900, 901, "passed"); err != nil {
		t.Fatalf("OnRetestTerminal: %v", err)
	}
	if got := f.retestEventCount(issueEventRetestCompleted); got != 0 {
		t.Fatalf("issue.retest_completed events = %d, want 0 when not applied", got)
	}
	if len(f.repo.retestResultCalls) != 1 {
		t.Fatalf("ApplyRetestResult calls = %d, want 1", len(f.repo.retestResultCalls))
	}
}
