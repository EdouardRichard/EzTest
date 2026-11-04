package adapters

// Task 112: FR-106-02 recorded the missing publish-time/schedule control on
// step 1, while a later step's API assertion also failed. The issue must anchor
// on the root-cause missing-feature step (ui/step 1) and carry the canonical
// severity derived from the case priority (P2 → minor). Its summary must not
// repeat the analyst's "severity upgraded to major" claim.

import (
	"context"
	"strings"
	"testing"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

func TestIssueAnchorPrefersEarlierMissingFeatureStep(t *testing.T) {
	deps, _ := newReadModelFixture()
	sutRef := int64(70)
	const (
		caseRunID  = int64(5010)
		caseVer    = int64(3010)
		step1ID    = int64(6010)
		evidenceID = int64(7010)
		observed   = "编辑页没有「发布时间/预约发布」控件，无法设置未来时间 T。"
		stepExpect = "保存成功，文章状态显示「预约发布」并展示预约时间"
	)
	deps.CaseRuns.(*fakeCaseRuns).runs = append(deps.CaseRuns.(*fakeCaseRuns).runs, execution.CaseRun{
		ID: caseRunID, WaveID: 11, CaseVersionID: caseVer,
		Status: execution.CaseRunFailed, FailureClass: execution.FailureClassApplication,
		Summary: execution.CaseRunSummary{
			StepTotal: 4, StepFailed: 4, AssertionTotal: 2, AssertionFailed: 2,
			LastError: "FR-106-02 失败：定时发布能力整体缺失，重跑无法修复。" +
				"severity 由 minor 上调为 major：整条 P2 能力完全缺失。",
		},
	})
	deps.Design.(*fakeDesign).cases[7] = append(deps.Design.(*fakeDesign).cases[7], designapp.ExecutableCaseView{
		CaseID: 210, CaseVersionID: caseVer, RequirementID: 102, RequirementVersionID: 1002,
		Title: "AC-FR-106-02 到点自动发布", Priority: "P2", VersionSUTIDs: []int64{sutRef},
		Steps: []designapp.ExecutableStepView{{Seq: 1, Action: "设置未来发布时间 T", Expected: stepExpect}},
	})
	deps.Results.(*fakeResults).steps[caseRunID] = []execution.StepResult{
		{
			ID: step1ID, CaseRunID: caseRunID, Seq: 1, Status: execution.StepFailed,
			TargetVersionSUTID: &sutRef,
			Observed:           map[string]any{"summary": observed},
		},
		{ID: 6011, CaseRunID: caseRunID, Seq: 2, Status: execution.StepFailed, TargetVersionSUTID: &sutRef},
		{ID: 6012, CaseRunID: caseRunID, Seq: 3, Status: execution.StepFailed, TargetVersionSUTID: &sutRef},
		{ID: 6013, CaseRunID: caseRunID, Seq: 4, Status: execution.StepFailed, TargetVersionSUTID: &sutRef},
	}
	deps.Results.(*fakeResults).assertions[caseRunID] = []execution.AssertionResult{
		{ID: 6510, CaseRunID: caseRunID, StepSeq: 2, AssertionSeq: 1, Kind: execution.AssertionKindAPI, Verdict: execution.AssertionFailed, Expected: "201", Actual: "500"},
		{ID: 6511, CaseRunID: caseRunID, StepSeq: 3, AssertionSeq: 1, Kind: execution.AssertionKindAPI, Verdict: execution.AssertionFailed, Expected: "published", Actual: "scheduled"},
	}
	deps.Evidence.(*fakeEvidence).rows[caseRunID] = []execution.Evidence{
		{ID: evidenceID, CaseRunID: caseRunID, VersionSUTID: &sutRef, Kind: execution.EvidenceScreenshot, SHA256: "eeff", SizeBytes: 40},
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
		t.Fatalf("run %d missing from the issue scan: %+v", caseRunID, snapshots)
	}
	if snapshot.FailedStepSeq != 1 || snapshot.FailedStepResultID != step1ID {
		t.Fatalf("anchor step = %d/%d, want 1/%d", snapshot.FailedStepSeq, snapshot.FailedStepResultID, step1ID)
	}
	if snapshot.Category != "ui" || snapshot.Severity != "minor" {
		t.Fatalf("category/severity = %q/%q, want ui/minor", snapshot.Category, snapshot.Severity)
	}
	if snapshot.FailedExpected != stepExpect || snapshot.FailedActual != observed {
		t.Fatalf("expected/actual = %q/%q", snapshot.FailedExpected, snapshot.FailedActual)
	}
	if strings.Contains(snapshot.Summary, "上调") || strings.Contains(strings.ToLower(snapshot.Summary), "severity") {
		t.Fatalf("issue summary repeats the severity claim: %q", snapshot.Summary)
	}
	if !strings.Contains(snapshot.Summary, "定时发布能力整体缺失") {
		t.Fatalf("issue summary lost the observed conclusion: %q", snapshot.Summary)
	}
	if len(snapshot.FailedAssertionResultIDs) != 2 {
		t.Fatalf("failed assertion refs = %v, want both later API assertions", snapshot.FailedAssertionResultIDs)
	}

	repo := &recordingIssueRepo{}
	service := deliveryapp.NewIssueService(repo, reader, nil, nil, nil, recordingTaskEnsurer{})
	if _, err := service.ScanFailures(context.Background(), 7); err != nil {
		t.Fatalf("ScanFailures: %v", err)
	}
	if want := "AC-FR-106-02 到点自动发布 · step 1: ui failure"; repo.created.Title != want {
		t.Fatalf("issue title = %q, want %q", repo.created.Title, want)
	}
	if repo.created.Severity != "minor" || repo.created.Category != "ui" {
		t.Fatalf("issue = %q/%q, want ui/minor", repo.created.Category, repo.created.Severity)
	}
}
