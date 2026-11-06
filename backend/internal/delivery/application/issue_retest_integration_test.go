//go:build integration

package deliveryapp_test

import (
	"context"
	"testing"

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
				{ID: 901, CaseVersionID: 300, Status: "pending"},
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
		CaseRuns: []deliveryapp.RetestCaseRun{{ID: 902, CaseVersionID: 300, Status: "pending"}},
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
