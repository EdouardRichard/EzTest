package adapters

// Task 110 (P8-T16): after wave analysis classifies FR-106-02 as
// failed(application), the issue scanner must ingest the run even though it has
// no failed assertion and no failed-step observation: the recorded missing
// publish-time/schedule control sits on a blocked step. The resulting issue must
// carry severity, the case expectation, the observed absence and the evidence
// refs. A run without such an observation is still skipped.

import (
	"context"
	"strings"
	"testing"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

func TestBlockedMissingControlIssueCarriesExpectedObservedEvidence(t *testing.T) {
	deps, _ := newReadModelFixture()
	sutRef := int64(70)
	const (
		caseRunID   = int64(5006)
		caseVersion = int64(3006)
		stepID      = int64(6006)
		evidenceID  = int64(7006)
		stepExpect  = "保存成功，文章状态显示「预约发布」并展示预约时间"
		observed    = "编辑页没有「发布时间/预约发布」控件，无法设置未来时间 T。"
		lastError   = "编辑页无「发布时间/预约发布」入口（状态仅草稿/已发布/已下架），无法设置未来时间 T，期望不可达"
	)
	deps.CaseRuns.(*fakeCaseRuns).runs = append(deps.CaseRuns.(*fakeCaseRuns).runs, execution.CaseRun{
		ID: caseRunID, WaveID: 11, CaseVersionID: caseVersion,
		Status: execution.CaseRunFailed, FailureClass: execution.FailureClassApplication,
		Summary: execution.CaseRunSummary{
			StepTotal: 2, StepFailed: 0, AssertionTotal: 1, AssertionFailed: 0,
			EvidenceCount: 1, LastError: lastError,
		},
	})
	deps.Design.(*fakeDesign).cases[7] = append(deps.Design.(*fakeDesign).cases[7], designapp.ExecutableCaseView{
		CaseID: 206, CaseVersionID: caseVersion, RequirementID: 102, RequirementVersionID: 1002,
		Title: "AC-FR-106-02 到点自动发布", Priority: "P2", VersionSUTIDs: []int64{sutRef},
		Steps: []designapp.ExecutableStepView{{Seq: 1, Action: "在编辑页设置未来发布时间 T", Expected: stepExpect}},
	})
	deps.Results.(*fakeResults).steps[caseRunID] = []execution.StepResult{
		{
			ID: stepID, CaseRunID: caseRunID, Seq: 1, Status: execution.StepBlocked,
			TargetVersionSUTID: &sutRef,
			Observed:           map[string]any{"summary": observed},
		},
	}
	deps.Results.(*fakeResults).assertions[caseRunID] = []execution.AssertionResult{
		{
			ID: 6506, CaseRunID: caseRunID, StepSeq: 1, AssertionSeq: 1, Kind: execution.AssertionKindUI,
			Verdict: execution.AssertionSkipped, Expected: stepExpect,
			Actual: "未执行：step1 无法设置预约时间 T", Reason: "前置条件不成立",
		},
	}
	deps.Evidence.(*fakeEvidence).rows[caseRunID] = []execution.Evidence{
		{ID: evidenceID, CaseRunID: caseRunID, VersionSUTID: &sutRef, Kind: execution.EvidenceScreenshot, SHA256: "eeff", SizeBytes: 32},
	}

	reader := NewExecutionReader(deps)
	snapshots, err := reader.ListFailedApplicationCaseRuns(context.Background(), 7)
	if err != nil {
		t.Fatalf("ListFailedApplicationCaseRuns: %v", err)
	}
	var snapshot *deliveryapp.CaseRunSnapshot
	for index := range snapshots {
		if snapshots[index].ID == caseRunID {
			snapshot = &snapshots[index]
		}
	}
	if snapshot == nil {
		t.Fatalf("blocked-step missing-control run %d was dropped: %+v", caseRunID, snapshots)
	}
	if snapshot.FailureClass != "application" {
		t.Fatalf("failure class = %q, want application", snapshot.FailureClass)
	}
	if snapshot.FailedStepSeq != 1 || snapshot.FailedStepResultID != stepID {
		t.Fatalf("failed step = %d/%d, want 1/%d", snapshot.FailedStepSeq, snapshot.FailedStepResultID, stepID)
	}
	if snapshot.Category != "ui" || snapshot.Severity != "minor" {
		t.Fatalf("category/severity = %q/%q, want ui/minor", snapshot.Category, snapshot.Severity)
	}
	if snapshot.Summary != lastError {
		t.Fatalf("summary = %q, want the observed missing-entry explanation", snapshot.Summary)
	}
	if snapshot.FailedExpected != stepExpect {
		t.Fatalf("failed expected = %q, want the case step expectation %q", snapshot.FailedExpected, stepExpect)
	}
	if snapshot.FailedActual != observed {
		t.Fatalf("failed actual = %q, want the step observation %q", snapshot.FailedActual, observed)
	}
	if len(snapshot.EvidenceIDs) != 1 || snapshot.EvidenceIDs[0] != evidenceID {
		t.Fatalf("evidence ids = %v, want [%d]", snapshot.EvidenceIDs, evidenceID)
	}

	repo := &recordingIssueRepo{}
	service := deliveryapp.NewIssueService(repo, reader, nil, nil, nil, recordingTaskEnsurer{})
	created, err := service.ScanFailures(context.Background(), 7)
	if err != nil {
		t.Fatalf("ScanFailures: %v", err)
	}
	// The fixture already carries one failed(application) run (5001); the
	// regression is that the blocked-step missing-control run is ingested too.
	if created != 2 {
		t.Fatalf("ScanFailures created = %d, want 2 (fixture 5001 + missing-control 5006)", created)
	}
	if repo.createCalls != 2 {
		t.Fatalf("CreateCandidate calls = %d, want 2", repo.createCalls)
	}
	if !strings.Contains(repo.created.Summary, "发布时间/预约发布") {
		t.Fatalf("issue summary lost the observed missing control: %q", repo.created.Summary)
	}
	wantKinds := []string{
		delivery.IssueRefKindCaseRun,
		delivery.IssueRefKindCaseVersion,
		delivery.IssueRefKindWave,
		delivery.IssueRefKindStepResult,
		delivery.IssueRefKindEvidence,
		delivery.IssueRefKindRequirement,
	}
	if len(repo.refs) != len(wantKinds) {
		t.Fatalf("issue refs = %d, want %d (%v)", len(repo.refs), len(wantKinds), wantKinds)
	}
	for index, kind := range wantKinds {
		if repo.refs[index].Kind != kind {
			t.Fatalf("ref[%d].kind = %q, want %q", index, repo.refs[index].Kind, kind)
		}
	}
}
