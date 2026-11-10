package adapters

// v0.2.3 X-02: an executor capability limitation recorded as a structured step
// marker must not reach the issue-candidate scan, even when the run was stored
// as failed(application) and carries a failed assertion.

import (
	"context"
	"testing"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// TestTerminalCauseExecutorCapabilityBlocksIssueCandidate: legacy rows carry the
// capability conclusion only in summary.terminal_cause (no structured step
// marker); the read model must still keep them out of the issue-candidate scan.
func TestTerminalCauseExecutorCapabilityBlocksIssueCandidate(t *testing.T) {
	deps, _ := newReadModelFixture()
	runs := deps.CaseRuns.(*fakeCaseRuns)
	runs.runs[0].Summary.TerminalCause = execution.TerminalCauseExecutorCapability

	reader := NewExecutionReader(deps)
	snapshots, err := reader.ListFailedApplicationCaseRuns(context.Background(), 7)
	if err != nil {
		t.Fatalf("ListFailedApplicationCaseRuns: %v", err)
	}
	for _, snapshot := range snapshots {
		if snapshot.ID == runs.runs[0].ID {
			t.Fatalf("terminal-cause capability run %d reached the issue candidate scan", snapshot.ID)
		}
	}
}

// TestWaveAndVersionSnapshotsCopyExecutorCapability: the application wave/version
// projections must carry the capability flag and stable cause, otherwise triage
// and the report silently lose the block.
func TestWaveAndVersionSnapshotsCopyExecutorCapability(t *testing.T) {
	deps, _ := newReadModelFixture()
	runs := deps.CaseRuns.(*fakeCaseRuns)
	runs.runs[0].Status = execution.CaseRunBlocked
	runs.runs[0].FailureClass = execution.FailureClassBlocked
	runs.runs[0].Summary.TerminalCause = execution.TerminalCauseExecutorCapability
	reader := NewExecutionReader(deps)

	assertCopied := func(t *testing.T, label string, snapshots []deliveryapp.CaseRunSnapshot) {
		t.Helper()
		for _, run := range snapshots {
			if run.ID != runs.runs[0].ID {
				continue
			}
			if !run.ExecutorCapability {
				t.Fatalf("%s snapshot lost executor_capability", label)
			}
			if run.TerminalCause != execution.TerminalCauseExecutorCapability {
				t.Fatalf("%s snapshot terminal cause = %q, want %q", label, run.TerminalCause, execution.TerminalCauseExecutorCapability)
			}
			return
		}
		t.Fatalf("%s snapshot has no case run %d", label, runs.runs[0].ID)
	}

	wave, err := reader.WaveSnapshot(context.Background(), 7, 11)
	if err != nil {
		t.Fatalf("WaveSnapshot: %v", err)
	}
	assertCopied(t, "wave", wave.CaseRuns)

	version, err := reader.VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	assertCopied(t, "version", version.CaseRuns)
}

func TestExecutorCapabilityLimitationNotIssueCandidate(t *testing.T) {
	deps, _ := newReadModelFixture()
	sutRef := int64(70)
	const (
		caseRunID   = int64(5008)
		caseVersion = int64(3008)
	)
	deps.CaseRuns.(*fakeCaseRuns).runs = append(deps.CaseRuns.(*fakeCaseRuns).runs, execution.CaseRun{
		ID: caseRunID, WaveID: 11, CaseVersionID: caseVersion,
		Status:       execution.CaseRunFailed,
		FailureClass: execution.FailureClassApplication,
		Summary:      execution.CaseRunSummary{StepTotal: 2, StepFailed: 1, AssertionTotal: 1},
	})
	deps.Design.(*fakeDesign).cases[7] = append(deps.Design.(*fakeDesign).cases[7], designapp.ExecutableCaseView{
		CaseID: 208, CaseVersionID: caseVersion, RequirementID: 108, RequirementVersionID: 1208,
		Title: "正文图片上传并插入", Priority: "P1", VersionSUTIDs: []int64{sutRef},
	})
	deps.Results.(*fakeResults).steps[caseRunID] = []execution.StepResult{
		{ID: 6008, CaseRunID: caseRunID, Seq: 1, Status: execution.StepPassed},
		{
			ID: 6009, CaseRunID: caseRunID, Seq: 2, Status: execution.StepBlocked,
			TargetVersionSUTID: &sutRef,
			Observed: map[string]any{
				"summary":                       "无法选择本地文件（工具不支持文件选择）",
				execution.ExecutorLimitationKey: map[string]any{"code": "file_chooser_unavailable"},
			},
		},
	}
	// A failed assertion would normally be enough application evidence; the
	// structured limitation marker must still keep the run out of the scan.
	deps.Results.(*fakeResults).assertions[caseRunID] = []execution.AssertionResult{
		{ID: 6508, CaseRunID: caseRunID, StepSeq: 2, AssertionSeq: 1, Kind: "ui", Verdict: execution.AssertionFailed},
	}

	reader := NewExecutionReader(deps)
	snapshots, err := reader.ListFailedApplicationCaseRuns(context.Background(), 7)
	if err != nil {
		t.Fatalf("ListFailedApplicationCaseRuns: %v", err)
	}
	for _, snapshot := range snapshots {
		if snapshot.ID == caseRunID {
			t.Fatalf("executor capability run %d reached the issue candidate scan: %+v", caseRunID, snapshot)
		}
	}

	service := deliveryapp.NewIssueService(&recordingIssueRepo{}, reader, nil, nil, nil, recordingTaskEnsurer{})
	if _, err := service.ScanFailures(context.Background(), 7); err != nil {
		t.Fatalf("ScanFailures: %v", err)
	}
}
