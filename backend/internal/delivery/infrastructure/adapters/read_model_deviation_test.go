package adapters

// QA Q1 reproduction at the issue-candidate boundary: a failed(application)
// case_run whose only evidence is a failed step with an observed requirement
// deviation must reach IssueService.ScanFailures and create one traceable issue.
// The pre-fix reader silently dropped every run without a failed assertion, so
// no issue could be created.

import (
	"context"
	"strings"
	"testing"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// recordingIssueRepo embeds the repository port so the test only implements the
// methods ScanFailures uses; an unexpected call panics instead of silently
// passing.
type recordingIssueRepo struct {
	deliveryapp.IssueRepository
	created     delivery.Issue
	refs        []delivery.IssueRef
	createCalls int
}

func (r *recordingIssueRepo) FindByCaseRun(context.Context, int64, int64) (delivery.Issue, bool, error) {
	return delivery.Issue{}, false, nil
}

func (r *recordingIssueRepo) CreateCandidate(_ context.Context, issue delivery.Issue, refs []delivery.IssueRef) (delivery.Issue, bool, error) {
	r.createCalls++
	r.created = issue
	r.refs = append([]delivery.IssueRef(nil), refs...)
	issue.ID = 900
	return issue, true, nil
}

type recordingTaskEnsurer struct{}

func (recordingTaskEnsurer) EnsureIssueTriage(context.Context, int64, int64) error { return nil }
func (recordingTaskEnsurer) EnsureReportTask(context.Context, int64, int32) error  { return nil }
func (recordingTaskEnsurer) EnsureRequirementClose(context.Context, int64, int64, int32) error {
	return nil
}
func (recordingTaskEnsurer) EnsureVersionClose(context.Context, int64, int64, string) error {
	return nil
}

func TestStepDeviationCreatesApplicationIssue(t *testing.T) {
	deps, _ := newReadModelFixture()
	sutRef := int64(70)
	const (
		caseRunID   = int64(5004)
		caseVersion = int64(3004)
		stepID      = int64(6004)
		evidenceID  = int64(7004)
	)
	deps.CaseRuns.(*fakeCaseRuns).runs = append(deps.CaseRuns.(*fakeCaseRuns).runs, execution.CaseRun{
		ID: caseRunID, WaveID: 11, CaseVersionID: caseVersion,
		Status: execution.CaseRunFailed, FailureClass: execution.FailureClassApplication,
		Summary: execution.CaseRunSummary{StepTotal: 2, StepFailed: 1, AssertionTotal: 1},
	})
	deps.Design.(*fakeDesign).cases[7] = append(deps.Design.(*fakeDesign).cases[7], designapp.ExecutableCaseView{
		CaseID: 204, CaseVersionID: caseVersion, RequirementID: 102, RequirementVersionID: 1002,
		Title: "AC-FR-011-02 登录失败提示（负向）", Priority: "P0", VersionSUTIDs: []int64{sutRef},
	})
	deps.Results.(*fakeResults).steps[caseRunID] = []execution.StepResult{
		{ID: 6003, CaseRunID: caseRunID, Seq: 1, Status: execution.StepPassed},
		{
			ID: stepID, CaseRunID: caseRunID, Seq: 2, Status: execution.StepFailed,
			TargetVersionSUTID: &sutRef,
			Observed: map[string]any{
				"summary": "弹窗内展示的提示文案为「登录已过期，请重新登录」，与需求要求的统一提示「账号或密码错误，请重试」不一致。",
			},
		},
	}
	deps.Results.(*fakeResults).assertions[caseRunID] = []execution.AssertionResult{
		{ID: 6504, CaseRunID: caseRunID, StepSeq: 1, AssertionSeq: 1, Kind: "ui", Verdict: execution.AssertionPassed},
	}
	deps.Evidence.(*fakeEvidence).rows[caseRunID] = []execution.Evidence{
		{ID: evidenceID, CaseRunID: caseRunID, VersionSUTID: &sutRef, Kind: execution.EvidenceScreenshot, SHA256: "ccdd", SizeBytes: 64},
	}
	// An application claim with no failed assertion and no observed requirement
	// deviation must stay out of the issue scan (no gate widening).
	const emptyClaimID = int64(5005)
	deps.CaseRuns.(*fakeCaseRuns).runs = append(deps.CaseRuns.(*fakeCaseRuns).runs, execution.CaseRun{
		ID: emptyClaimID, WaveID: 11, CaseVersionID: caseVersion,
		Status: execution.CaseRunFailed, FailureClass: execution.FailureClassApplication,
		Summary: execution.CaseRunSummary{StepTotal: 1, StepFailed: 1},
	})
	deps.Results.(*fakeResults).steps[emptyClaimID] = []execution.StepResult{
		{ID: 6005, CaseRunID: emptyClaimID, Seq: 1, Status: execution.StepFailed},
	}

	reader := NewExecutionReader(deps)

	// The scan input must no longer drop the assertion-less step deviation.
	snapshots, err := reader.ListFailedApplicationCaseRuns(context.Background(), 7)
	if err != nil {
		t.Fatalf("ListFailedApplicationCaseRuns: %v", err)
	}
	var snapshot *deliveryapp.CaseRunSnapshot
	for index := range snapshots {
		if snapshots[index].ID == caseRunID {
			snapshot = &snapshots[index]
		}
		if snapshots[index].ID == emptyClaimID {
			t.Fatalf("evidence-less application claim %d must not become an issue candidate", emptyClaimID)
		}
	}
	if snapshot == nil {
		t.Fatalf("step-deviation run %d was dropped: %+v", caseRunID, snapshots)
	}
	if snapshot.FailureClass != "application" || snapshot.CaseTitle != "AC-FR-011-02 登录失败提示（负向）" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.RequirementID != 102 {
		t.Fatalf("requirement id = %d, want 102", snapshot.RequirementID)
	}
	if snapshot.FailedStepSeq != 2 || snapshot.FailedStepResultID != stepID {
		t.Fatalf("failed step = %d/%d, want 2/%d", snapshot.FailedStepSeq, snapshot.FailedStepResultID, stepID)
	}
	if snapshot.Category != "ui" || snapshot.Severity != "major" {
		t.Fatalf("category/severity = %q/%q, want ui/major", snapshot.Category, snapshot.Severity)
	}
	if snapshot.Summary != "弹窗内展示的提示文案为「登录已过期，请重新登录」，与需求要求的统一提示「账号或密码错误，请重试」不一致。" {
		t.Fatalf("summary = %q, want the failed step observation", snapshot.Summary)
	}
	if len(snapshot.EvidenceIDs) != 1 || snapshot.EvidenceIDs[0] != evidenceID {
		t.Fatalf("evidence ids = %v, want [%d]", snapshot.EvidenceIDs, evidenceID)
	}

	// And the real issue service must create the issue with the traceable refs.
	repo := &recordingIssueRepo{}
	service := deliveryapp.NewIssueService(repo, reader, nil, nil, nil, recordingTaskEnsurer{})
	created, err := service.ScanFailures(context.Background(), 7)
	if err != nil {
		t.Fatalf("ScanFailures: %v", err)
	}
	// The fixture already carries one failed(application) run (5001); the
	// regression is that the step-deviation run 5004 is ingested too.
	if created != 2 {
		t.Fatalf("ScanFailures created = %d, want 2 (fixture 5001 + step deviation 5004)", created)
	}
	if repo.createCalls != 2 {
		t.Fatalf("CreateCandidate calls = %d, want 2", repo.createCalls)
	}
	if repo.created.Category != "ui" || repo.created.Severity != "major" {
		t.Fatalf("issue = %+v, want ui/major", repo.created)
	}
	if !strings.Contains(repo.created.Summary, "登录已过期，请重新登录") ||
		!strings.Contains(repo.created.Summary, "账号或密码错误，请重试") {
		t.Fatalf("issue summary lost the requirement evidence: %q", repo.created.Summary)
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
