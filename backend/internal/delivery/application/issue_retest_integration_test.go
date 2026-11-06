//go:build integration

package deliveryapp_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// integrationRetestExecutor is the in-memory W2-5 execution port of the DB
// integration test: the delivery repository is real, the retest intents/wave are
// deterministic fakes.
type integrationRetestExecutor struct {
	wave         deliveryapp.RetestWave
	active       *deliveryapp.RetestWave
	createCalls  []int64
	canceled     []int64
	startVersion []int64
}

func (e *integrationRetestExecutor) CreateRetestIntent(_ context.Context, versionID, caseVersionID, _ int64) (int64, error) {
	e.createCalls = append(e.createCalls, caseVersionID)
	return int64(1000 + len(e.createCalls)), nil
}

func (e *integrationRetestExecutor) CancelRetestIntent(_ context.Context, intentID int64) error {
	e.canceled = append(e.canceled, intentID)
	return nil
}

func (e *integrationRetestExecutor) ActiveWave(context.Context, int64) (deliveryapp.RetestWave, bool, error) {
	if e.active == nil {
		return deliveryapp.RetestWave{}, false, nil
	}
	return *e.active, true, nil
}

func (e *integrationRetestExecutor) StartRetestWave(_ context.Context, versionID int64) (deliveryapp.RetestWave, error) {
	e.startVersion = append(e.startVersion, versionID)
	return e.wave, nil
}

var _ deliveryapp.RetestExecutor = (*integrationRetestExecutor)(nil)

// TestIssueRetestIntegrationPersistsWaveAndResult proves the W2-5 retest model
// against the real delivery_0002 columns/refs: StartRetest persists
// retest_status/last_retest_wave_id and the role='retest' refs, then a passed
// terminal callback writes retest_status/last_retest_case_run_id and is
// idempotent.
func TestIssueRetestIntegrationPersistsWaveAndResult(t *testing.T) {
	fixture := newIntegrationIssueFixture(t)
	ctx := context.Background()
	const versionID, caseRunID = int64(100), int64(5001)
	fixture.exec.snapshots[caseRunID] = integrationIssueSnapshot(versionID, caseRunID, 300)

	issue, err := fixture.svc.CreateManual(ctx, deliveryapp.CreateManualIssueInput{
		VersionID: versionID,
		Title:     "manual checkout failure",
		Category:  "api",
		Severity:  "major",
		CaseRunID: caseRunID,
	}, 9)
	if err != nil {
		t.Fatalf("CreateManual: %v", err)
	}
	confirmed, err := fixture.svc.Confirm(ctx, versionID, issue.ID, 9, "", nil)
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if confirmed.RetestStatus != delivery.IssueRetestStatusNotRetested {
		t.Fatalf("initial retest_status = %q, want not_retested", confirmed.RetestStatus)
	}

	retestExec := &integrationRetestExecutor{
		wave: deliveryapp.RetestWave{
			ID: 900, Status: "running",
			CaseRuns: []deliveryapp.RetestCaseRun{
				{ID: 901, IntentID: 1001, CaseVersionID: 300, Status: "pending"},
			},
		},
	}
	fixture.svc.WithRetestExecutor(retestExec)

	result, err := fixture.svc.StartRetest(ctx, versionID, issue.ID, 9)
	if err != nil {
		t.Fatalf("StartRetest: %v", err)
	}
	if result.Wave.ID != 900 || result.Issue.RetestStatus != delivery.IssueRetestStatusRetesting {
		t.Fatalf("retest result = %+v", result)
	}
	if len(retestExec.createCalls) != 1 || retestExec.createCalls[0] != 300 {
		t.Fatalf("retest intents = %v, want case_version [300]", retestExec.createCalls)
	}

	stored, err := fixture.repo.Get(ctx, versionID, issue.ID)
	if err != nil {
		t.Fatalf("Get retesting issue: %v", err)
	}
	if stored.RetestStatus != delivery.IssueRetestStatusRetesting {
		t.Fatalf("persisted retest_status = %q, want retesting", stored.RetestStatus)
	}
	if stored.LastRetestWaveID == nil || *stored.LastRetestWaveID != 900 {
		t.Fatalf("persisted last_retest_wave_id = %v, want 900", stored.LastRetestWaveID)
	}
	refs, err := fixture.repo.ListRefs(ctx, issue.ID)
	if err != nil {
		t.Fatalf("ListRefs: %v", err)
	}
	foundWave, foundRun := false, false
	for _, ref := range refs {
		if ref.Kind == delivery.IssueRefKindWave && ref.RefID == 900 && ref.Role == delivery.IssueRefRoleRetest {
			foundWave = true
		}
		if ref.Kind == delivery.IssueRefKindCaseRun && ref.RefID == 901 && ref.Role == delivery.IssueRefRoleRetest {
			foundRun = true
		}
	}
	if !foundWave || !foundRun {
		t.Fatalf("retest refs = %+v, want wave/900 and case_run/901 with role=retest", refs)
	}
	t.Logf("retest wave linked: issue=%d wave=%d status=%s retest_status=%s",
		issue.ID, result.Wave.ID, result.Wave.Status, stored.RetestStatus)
	for _, ref := range refs {
		if ref.Role == delivery.IssueRefRoleRetest {
			t.Logf("retest ref: kind=%s ref_id=%d role=%s", ref.Kind, ref.RefID, ref.Role)
		}
	}

	updated, err := fixture.svc.OnRetestTerminal(ctx, 900, 901, "passed")
	if err != nil {
		t.Fatalf("OnRetestTerminal passed: %v", err)
	}
	if updated.RetestStatus != delivery.IssueRetestStatusPassed {
		t.Fatalf("retest_status = %q, want passed", updated.RetestStatus)
	}
	if updated.LastRetestCaseRunID == nil || *updated.LastRetestCaseRunID != 901 {
		t.Fatalf("last_retest_case_run_id = %v, want 901", updated.LastRetestCaseRunID)
	}
	if got := countIntegrationEvent(fixture.events, "issue.retest_completed"); got != 1 {
		t.Fatalf("issue.retest_completed events = %d, want 1", got)
	}
	t.Logf("retest writeback: retest_status=%s last_wave=%v last_case_run=%v events=%d",
		updated.RetestStatus, *updated.LastRetestWaveID, *updated.LastRetestCaseRunID,
		countIntegrationEvent(fixture.events, "issue.retest_completed"))

	if _, err := fixture.svc.OnRetestTerminal(ctx, 900, 901, "passed"); err != nil {
		t.Fatalf("duplicate OnRetestTerminal: %v", err)
	}
	if got := countIntegrationEvent(fixture.events, "issue.retest_completed"); got != 1 {
		t.Fatalf("duplicate issue.retest_completed events = %d, want 1", got)
	}

	// A second retest after a passed result is allowed and writes failed back.
	retestExec.wave = deliveryapp.RetestWave{
		ID: 901, Status: "running",
		CaseRuns: []deliveryapp.RetestCaseRun{{ID: 902, IntentID: 1002, CaseVersionID: 300, Status: "pending"}},
	}
	if _, err := fixture.svc.StartRetest(ctx, versionID, issue.ID, 9); err != nil {
		t.Fatalf("second StartRetest: %v", err)
	}
	failed, err := fixture.svc.OnRetestTerminal(ctx, 901, 902, "failed")
	if err != nil {
		t.Fatalf("OnRetestTerminal failed: %v", err)
	}
	if failed.RetestStatus != delivery.IssueRetestStatusFailed {
		t.Fatalf("retest_status = %q, want failed", failed.RetestStatus)
	}
	if failed.LastRetestWaveID == nil || *failed.LastRetestWaveID != 901 {
		t.Fatalf("last_retest_wave_id = %v, want 901", failed.LastRetestWaveID)
	}
	if got := countIntegrationEvent(fixture.events, "issue.retest_failed"); got != 1 {
		t.Fatalf("issue.retest_failed events = %d, want 1", got)
	}
}

func countIntegrationEvent(events *integrationIssueEvents, eventType string) int {
	count := 0
	for _, record := range events.records {
		if record.eventType == eventType {
			count++
		}
	}
	return count
}

// TestIssueRetestIntegrationStartCASRejectsStaleObservation proves the R-14 CAS:
// two requests that observed the same pre-retest projection cannot both write.
// The second start (a different case_run selection) is a state conflict and the
// first wave/ref persists untouched.
func TestIssueRetestIntegrationStartCASRejectsStaleObservation(t *testing.T) {
	fixture := newIntegrationIssueFixture(t)
	ctx := context.Background()
	const versionID = int64(100)

	issue, err := fixture.svc.CreateManual(ctx, deliveryapp.CreateManualIssueInput{
		VersionID: versionID,
		Title:     "manual retest CAS",
		Category:  "api",
		Severity:  "major",
	}, 9)
	if err != nil {
		t.Fatalf("CreateManual: %v", err)
	}
	if _, err := fixture.svc.Confirm(ctx, versionID, issue.ID, 9, "", nil); err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	firstRefs := []delivery.IssueRef{
		{Kind: delivery.IssueRefKindWave, RefID: 900, Role: delivery.IssueRefRoleRetest},
		{Kind: delivery.IssueRefKindCaseRun, RefID: 901, Role: delivery.IssueRefRoleRetest},
	}
	if _, err := fixture.repo.StartRetest(ctx, deliveryapp.IssueRetestStart{
		VersionID: versionID, IssueID: issue.ID, WaveID: 900,
		ExpectedRetestStatus: delivery.IssueRetestStatusNotRetested,
		Refs:                 firstRefs,
	}); err != nil {
		t.Fatalf("first StartRetest: %v", err)
	}

	secondRefs := []delivery.IssueRef{
		{Kind: delivery.IssueRefKindWave, RefID: 901, Role: delivery.IssueRefRoleRetest},
		{Kind: delivery.IssueRefKindCaseRun, RefID: 902, Role: delivery.IssueRefRoleRetest},
	}
	_, err = fixture.repo.StartRetest(ctx, deliveryapp.IssueRetestStart{
		VersionID: versionID, IssueID: issue.ID, WaveID: 901,
		ExpectedRetestStatus: delivery.IssueRetestStatusNotRetested,
		Refs:                 secondRefs,
	})
	if !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("second StartRetest error = %v, want ErrStateConflict", err)
	}

	stored, err := fixture.repo.Get(ctx, versionID, issue.ID)
	if err != nil {
		t.Fatalf("Get issue: %v", err)
	}
	if stored.RetestStatus != delivery.IssueRetestStatusRetesting ||
		stored.LastRetestWaveID == nil || *stored.LastRetestWaveID != 900 {
		t.Fatalf("stored retest = %q/%v, want retesting/900", stored.RetestStatus, stored.LastRetestWaveID)
	}
	refs, err := fixture.repo.ListRefs(ctx, issue.ID)
	if err != nil {
		t.Fatalf("ListRefs: %v", err)
	}
	for _, ref := range refs {
		if ref.Kind == delivery.IssueRefKindCaseRun && ref.RefID == 902 {
			t.Fatalf("losing selection ref 902 was persisted: %+v", refs)
		}
	}
}

// TestIssueRetestIntegrationOtherStatusHistory proves an "other" terminal status
// keeps retesting and records the reason in the append-only status history with
// the system actor.
func TestIssueRetestIntegrationOtherStatusHistory(t *testing.T) {
	fixture := newIntegrationIssueFixture(t)
	ctx := context.Background()
	const versionID, caseRunID = int64(100), int64(5001)
	fixture.exec.snapshots[caseRunID] = integrationIssueSnapshot(versionID, caseRunID, 300)

	issue, err := fixture.svc.CreateManual(ctx, deliveryapp.CreateManualIssueInput{
		VersionID: versionID,
		Title:     "manual blocked retest",
		Category:  "api",
		Severity:  "major",
		CaseRunID: caseRunID,
	}, 9)
	if err != nil {
		t.Fatalf("CreateManual: %v", err)
	}
	if _, err := fixture.svc.Confirm(ctx, versionID, issue.ID, 9, "", nil); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	fixture.svc.WithRetestExecutor(&integrationRetestExecutor{
		wave: deliveryapp.RetestWave{
			ID: 900, Status: "running",
			CaseRuns: []deliveryapp.RetestCaseRun{
				{ID: 901, IntentID: 1001, CaseVersionID: 300, Status: "pending"},
			},
		},
	})
	if _, err := fixture.svc.StartRetest(ctx, versionID, issue.ID, 9); err != nil {
		t.Fatalf("StartRetest: %v", err)
	}
	updated, err := fixture.svc.OnRetestTerminal(ctx, 900, 901, "blocked")
	if err != nil {
		t.Fatalf("OnRetestTerminal blocked: %v", err)
	}
	if updated.RetestStatus != delivery.IssueRetestStatusRetesting {
		t.Fatalf("retest_status = %q, want retesting", updated.RetestStatus)
	}
	history, err := fixture.repo.ListHistory(ctx, issue.ID)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	found := false
	for _, row := range history {
		if row.Reason != "blocked" {
			continue
		}
		if row.ActorKind != "system" || row.From == nil || *row.From != delivery.IssueStatusConfirmed ||
			row.To != delivery.IssueStatusConfirmed {
			t.Fatalf("blocked history row = %+v, want system confirmed->confirmed", row)
		}
		found = true
	}
	if !found {
		t.Fatalf("no blocked history row: %+v", history)
	}
	if got := countIntegrationEvent(fixture.events, "issue.retest_failed"); got != 0 {
		t.Fatalf("issue.retest_failed events = %d, want 0", got)
	}
}

// TestIssueRetestIntegrationStaleWaveResultConflict proves a terminal callback
// for an older wave cannot overwrite a newer retest (state conflict).
func TestIssueRetestIntegrationStaleWaveResultConflict(t *testing.T) {
	fixture := newIntegrationIssueFixture(t)
	ctx := context.Background()
	const versionID = int64(100)

	issue, err := fixture.svc.CreateManual(ctx, deliveryapp.CreateManualIssueInput{
		VersionID: versionID,
		Title:     "manual stale wave",
		Category:  "api",
		Severity:  "major",
	}, 9)
	if err != nil {
		t.Fatalf("CreateManual: %v", err)
	}
	if _, err := fixture.svc.Confirm(ctx, versionID, issue.ID, 9, "", nil); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if _, err := fixture.repo.StartRetest(ctx, deliveryapp.IssueRetestStart{
		VersionID: versionID, IssueID: issue.ID, WaveID: 900,
		ExpectedRetestStatus: delivery.IssueRetestStatusNotRetested,
	}); err != nil {
		t.Fatalf("StartRetest: %v", err)
	}
	_, _, err = fixture.repo.ApplyRetestResult(ctx, deliveryapp.IssueRetestResult{
		VersionID: versionID, IssueID: issue.ID, WaveID: 899,
		CaseRunID: 901, TargetStatus: delivery.IssueRetestStatusPassed,
	})
	if !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("stale wave result error = %v, want ErrStateConflict", err)
	}
}

// TestIssueRetestIntegrationNoLinkedRefsDoesNotWriteback proves F2 against the
// real repository: a retesting issue with no role='retest' case_run refs keeps
// retesting even when the wave has terminal runs.
func TestIssueRetestIntegrationNoLinkedRefsDoesNotWriteback(t *testing.T) {
	fixture := newIntegrationIssueFixture(t)
	ctx := context.Background()
	const versionID = int64(100)

	issue, err := fixture.svc.CreateManual(ctx, deliveryapp.CreateManualIssueInput{
		VersionID: versionID,
		Title:     "manual empty linked retest",
		Category:  "api",
		Severity:  "major",
	}, 9)
	if err != nil {
		t.Fatalf("CreateManual: %v", err)
	}
	if _, err := fixture.svc.Confirm(ctx, versionID, issue.ID, 9, "", nil); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if _, err := fixture.repo.StartRetest(ctx, deliveryapp.IssueRetestStart{
		VersionID: versionID, IssueID: issue.ID, WaveID: 900,
		ExpectedRetestStatus: delivery.IssueRetestStatusNotRetested,
	}); err != nil {
		t.Fatalf("StartRetest: %v", err)
	}
	fixture.exec.waveSnapshots[900] = deliveryapp.WaveSnapshot{
		ID: 900, VersionID: versionID,
		CaseRuns: []deliveryapp.CaseRunSnapshot{{ID: 901, CaseVersionID: 300, Status: "failed"}},
	}
	if err := fixture.svc.OnRetestWaveTerminal(ctx, 900); err != nil {
		t.Fatalf("OnRetestWaveTerminal: %v", err)
	}
	stored, err := fixture.repo.Get(ctx, versionID, issue.ID)
	if err != nil {
		t.Fatalf("Get issue: %v", err)
	}
	if stored.RetestStatus != delivery.IssueRetestStatusRetesting {
		t.Fatalf("retest_status = %q, want retesting", stored.RetestStatus)
	}
	if got := countIntegrationEvent(fixture.events, "issue.retest_failed"); got != 0 {
		t.Fatalf("issue.retest_failed events = %d, want 0", got)
	}
}

// concurrentIntegrationRetestExecutor fences both concurrent StartRetest calls
// at the wave-planning step so both have observed the pre-retest issue state and
// created their own intent before either repository CAS runs.
type concurrentIntegrationRetestExecutor struct {
	arrivals chan struct{}
	release  chan struct{}
	wave     deliveryapp.RetestWave
}

func (e *concurrentIntegrationRetestExecutor) CreateRetestIntent(_ context.Context, _, caseVersionID, _ int64) (int64, error) {
	switch caseVersionID {
	case 300:
		return 1001, nil
	case 301:
		return 1002, nil
	default:
		return 0, errors.New("unexpected case version")
	}
}

func (e *concurrentIntegrationRetestExecutor) CancelRetestIntent(context.Context, int64) error {
	return nil
}

func (e *concurrentIntegrationRetestExecutor) ActiveWave(context.Context, int64) (deliveryapp.RetestWave, bool, error) {
	return deliveryapp.RetestWave{}, false, nil
}

func (e *concurrentIntegrationRetestExecutor) StartRetestWave(context.Context, int64) (deliveryapp.RetestWave, error) {
	e.arrivals <- struct{}{}
	<-e.release
	return e.wave, nil
}

// TestIssueRetestIntegrationConcurrentSelectionsOneWinner proves R-14 against
// the real repository: two requests for the same issue with different case_run
// selections both observe the pre-retest state, but only one may persist; the
// other receives the stable retest_active 409.
func TestIssueRetestIntegrationConcurrentSelectionsOneWinner(t *testing.T) {
	fixture := newIntegrationIssueFixture(t)
	ctx := context.Background()
	const versionID, firstRunID, secondRunID = int64(100), int64(5001), int64(5002)
	fixture.exec.snapshots[firstRunID] = integrationIssueSnapshot(versionID, firstRunID, 300)
	fixture.exec.snapshots[secondRunID] = integrationIssueSnapshot(versionID, secondRunID, 301)

	issue, err := fixture.svc.CreateManual(ctx, deliveryapp.CreateManualIssueInput{
		VersionID: versionID,
		Title:     "manual concurrent retest",
		Category:  "api",
		Severity:  "major",
		CaseRunID: firstRunID,
	}, 9)
	if err != nil {
		t.Fatalf("CreateManual: %v", err)
	}
	confirmed, err := fixture.svc.Confirm(ctx, versionID, issue.ID, 9, "", nil)
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if _, err := fixture.svc.UpdateManual(ctx, versionID, issue.ID, deliveryapp.UpdateManualIssueInput{
		CaseRunID:        secondRunID,
		ExpectedRevision: confirmed.ExpectedRevision,
	}, 9); err != nil {
		t.Fatalf("link second case run: %v", err)
	}

	executor := &concurrentIntegrationRetestExecutor{
		arrivals: make(chan struct{}, 2),
		release:  make(chan struct{}),
		wave: deliveryapp.RetestWave{
			ID: 900, Status: "running",
			CaseRuns: []deliveryapp.RetestCaseRun{
				{ID: 901, IntentID: 1001, CaseVersionID: 300, Status: "pending"},
				{ID: 902, IntentID: 1002, CaseVersionID: 301, Status: "pending"},
			},
		},
	}
	fixture.svc.WithRetestExecutor(executor)

	type outcome struct {
		result deliveryapp.RetestResult
		err    error
	}
	results := make(chan outcome, 2)
	var wait sync.WaitGroup
	for _, selection := range []int64{firstRunID, secondRunID} {
		wait.Add(1)
		go func(caseRunID int64) {
			defer wait.Done()
			result, err := fixture.svc.StartRetest(ctx, versionID, issue.ID, 9, caseRunID)
			results <- outcome{result: result, err: err}
		}(selection)
	}
	for arrived := 0; arrived < 2; arrived++ {
		select {
		case <-executor.arrivals:
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent StartRetest calls did not reach the wave barrier")
		}
	}
	close(executor.release)
	wait.Wait()
	close(results)

	successes, conflicts := 0, 0
	for result := range results {
		if result.err == nil {
			successes++
			continue
		}
		var conflict *deliveryapp.IssueStateConflictError
		if !errors.As(result.err, &conflict) || conflict.Reason != deliveryapp.IssueConflictRetestActive {
			t.Fatalf("loser error = %v, want retest_active state conflict", result.err)
		}
		conflicts++
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes/conflicts = %d/%d, want 1/1", successes, conflicts)
	}

	stored, err := fixture.repo.Get(ctx, versionID, issue.ID)
	if err != nil {
		t.Fatalf("Get issue: %v", err)
	}
	if stored.RetestStatus != delivery.IssueRetestStatusRetesting ||
		stored.LastRetestWaveID == nil || *stored.LastRetestWaveID != 900 {
		t.Fatalf("stored retest = %q/%v, want retesting/900", stored.RetestStatus, stored.LastRetestWaveID)
	}
}
