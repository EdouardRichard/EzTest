package deliveryapp

import (
	"context"
	"errors"
	"fmt"
	"testing"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// pinTaskEnsurer pins the revision-aware TaskEnsurer method set. The
// compile-time assertion below fails if a method is renamed, loses a
// parameter, or the old wave/version ensure variants are reintroduced.
type pinTaskEnsurer struct {
	calls []string
}

func (p *pinTaskEnsurer) EnsureIssueTriage(_ context.Context, issueID, versionID int64) error {
	p.calls = append(p.calls, fmt.Sprintf("triage:%d:%d", issueID, versionID))
	return nil
}

func (p *pinTaskEnsurer) EnsureReportTask(_ context.Context, reportID int64, revision int32) error {
	p.calls = append(p.calls, fmt.Sprintf("report:%d:%d", reportID, revision))
	return nil
}

func (p *pinTaskEnsurer) EnsureRequirementClose(_ context.Context, requirementID, requirementVersionID int64, closeRound int32) error {
	p.calls = append(p.calls, fmt.Sprintf("requirement:%d:%d:%d", requirementID, requirementVersionID, closeRound))
	return nil
}

func (p *pinTaskEnsurer) EnsureVersionClose(_ context.Context, versionID, reportRevisionID int64, predicateFingerprint string) error {
	p.calls = append(p.calls, fmt.Sprintf("version:%d:%d:%s", versionID, reportRevisionID, predicateFingerprint))
	return nil
}

var _ TaskEnsurer = (*pinTaskEnsurer)(nil)

func TestTaskEnsurerRevisionAwareContract(t *testing.T) {
	recorder := &pinTaskEnsurer{}
	var ensurer TaskEnsurer = recorder

	ctx := context.Background()
	issueID, issueVersionID := int64(101), int64(202)
	reportID, revision := int64(303), int32(4)
	requirementID, requirementVersionID, closeRound := int64(404), int64(707), int32(2)
	versionID, reportRevisionID := int64(505), int64(606)

	if err := ensurer.EnsureIssueTriage(ctx, issueID, issueVersionID); err != nil {
		t.Fatalf("EnsureIssueTriage() error = %v", err)
	}
	if err := ensurer.EnsureReportTask(ctx, reportID, revision); err != nil {
		t.Fatalf("EnsureReportTask() error = %v", err)
	}
	if err := ensurer.EnsureRequirementClose(ctx, requirementID, requirementVersionID, closeRound); err != nil {
		t.Fatalf("EnsureRequirementClose() error = %v", err)
	}
	if err := ensurer.EnsureVersionClose(ctx, versionID, reportRevisionID, "f00d"); err != nil {
		t.Fatalf("EnsureVersionClose() error = %v", err)
	}

	want := []string{
		"triage:101:202",
		"report:303:4",
		"requirement:404:707:2",
		"version:505:606:f00d",
	}
	if len(recorder.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", recorder.calls, want)
	}
	for i := range want {
		if recorder.calls[i] != want[i] {
			t.Errorf("calls[%d] = %q, want %q", i, recorder.calls[i], want[i])
		}
	}
}

// pinIssueRepository pins IssueRepository.GetByID. Embedding the interface
// keeps the fake focused while the var assertion still checks the whole port.
type pinIssueRepository struct {
	IssueRepository
	issue delivery.Issue
	err   error
}

func (r pinIssueRepository) GetByID(context.Context, int64) (delivery.Issue, error) {
	return r.issue, r.err
}

var _ IssueRepository = pinIssueRepository{}

func TestIssueRepositoryGetByID(t *testing.T) {
	want := delivery.Issue{ID: 42, VersionID: 7, Title: "boom"}
	var repo IssueRepository = pinIssueRepository{issue: want}

	got, err := repo.GetByID(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if got != want {
		t.Fatalf("GetByID() = %+v, want %+v", got, want)
	}

	sentinel := errors.New("missing issue")
	repo = pinIssueRepository{err: sentinel}
	if _, err := repo.GetByID(context.Background(), 43); !errors.Is(err, sentinel) {
		t.Fatalf("GetByID() error = %v, want %v", err, sentinel)
	}
}

// pinReportRepository pins ReportRepository.GetReportByID.
type pinReportRepository struct {
	ReportRepository
	report delivery.Report
	err    error
}

func (r pinReportRepository) GetReportByID(context.Context, int64) (delivery.Report, error) {
	return r.report, r.err
}

var _ ReportRepository = pinReportRepository{}

func TestReportRepositoryGetReportByID(t *testing.T) {
	want := delivery.Report{ID: 42, VersionID: 7, Kind: delivery.ReportKindVersion}
	var repo ReportRepository = pinReportRepository{report: want}

	got, err := repo.GetReportByID(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetReportByID() error = %v", err)
	}
	if got != want {
		t.Fatalf("GetReportByID() = %+v, want %+v", got, want)
	}

	sentinel := errors.New("missing report")
	repo = pinReportRepository{err: sentinel}
	if _, err := repo.GetReportByID(context.Background(), 43); !errors.Is(err, sentinel) {
		t.Fatalf("GetReportByID() error = %v, want %v", err, sentinel)
	}
}

// TestCaseRunSnapshotTriageFields pins the §5.1 candidate fields: the minimal
// failed assertion pair (0/0 when the case_run has no failed assertion), the
// failed result IDs, the synthesis inputs and the P3 dedupe hint.
func TestCaseRunSnapshotTriageFields(t *testing.T) {
	requirementID := int64(501)
	failedStepSeq := 3
	failedAssertionSeq := 2
	failedStepResultID := int64(601)
	failedAssertionResultIDs := []int64{701, 702}
	failedExpected := "HTTP 200"
	failedActual := "HTTP 500"
	failedReason := "unexpected status"
	dedupeHint := "0123456789abcdef0123456789abcdef"
	caseTitle := "login fails"
	evidenceIDs := []int64{801, 802}

	snapshot := CaseRunSnapshot{
		ID:                       1,
		CaseVersionID:            2,
		CaseTitle:                caseTitle,
		RequirementID:            requirementID,
		FailedStepSeq:            failedStepSeq,
		FailedAssertionSeq:       failedAssertionSeq,
		FailedStepResultID:       failedStepResultID,
		FailedAssertionResultIDs: failedAssertionResultIDs,
		FailedExpected:           failedExpected,
		FailedActual:             failedActual,
		FailedReason:             failedReason,
		DedupeHint:               dedupeHint,
		EvidenceIDs:              evidenceIDs,
	}

	if snapshot.RequirementID != requirementID {
		t.Errorf("RequirementID = %d, want %d", snapshot.RequirementID, requirementID)
	}
	if snapshot.CaseTitle != caseTitle {
		t.Errorf("CaseTitle = %q, want %q", snapshot.CaseTitle, caseTitle)
	}
	if snapshot.FailedStepSeq != failedStepSeq || snapshot.FailedAssertionSeq != failedAssertionSeq {
		t.Errorf("failed assertion pair = (%d, %d), want (%d, %d)",
			snapshot.FailedStepSeq, snapshot.FailedAssertionSeq, failedStepSeq, failedAssertionSeq)
	}
	if snapshot.FailedStepResultID != failedStepResultID {
		t.Errorf("FailedStepResultID = %d, want %d", snapshot.FailedStepResultID, failedStepResultID)
	}
	if len(snapshot.FailedAssertionResultIDs) != 2 ||
		snapshot.FailedAssertionResultIDs[0] != 701 ||
		snapshot.FailedAssertionResultIDs[1] != 702 {
		t.Errorf("FailedAssertionResultIDs = %v, want [701 702]", snapshot.FailedAssertionResultIDs)
	}
	if snapshot.FailedExpected != failedExpected ||
		snapshot.FailedActual != failedActual ||
		snapshot.FailedReason != failedReason {
		t.Errorf("failed detail = (%q, %q, %q), want (%q, %q, %q)",
			snapshot.FailedExpected, snapshot.FailedActual, snapshot.FailedReason,
			failedExpected, failedActual, failedReason)
	}
	if snapshot.DedupeHint != dedupeHint {
		t.Errorf("DedupeHint = %q, want %q", snapshot.DedupeHint, dedupeHint)
	}
	if len(snapshot.EvidenceIDs) != 2 || snapshot.EvidenceIDs[0] != 801 || snapshot.EvidenceIDs[1] != 802 {
		t.Errorf("EvidenceIDs = %v, want [801 802]", snapshot.EvidenceIDs)
	}

	// No failed assertion: the minimal pair is the documented 0/0 sentinel.
	var noFailure CaseRunSnapshot
	if noFailure.FailedStepSeq != 0 || noFailure.FailedAssertionSeq != 0 {
		t.Errorf("zero snapshot failed pair = (%d, %d), want (0, 0)",
			noFailure.FailedStepSeq, noFailure.FailedAssertionSeq)
	}
}
