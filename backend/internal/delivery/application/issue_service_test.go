package deliveryapp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

var errIssueTestBoom = errors.New("issue test boom")

var issueServiceTestNow = time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)

// issueGetCall records one version-scoped Get.
type issueGetCall struct {
	versionID int64
	issueID   int64
}

// issueListCall records one List request.
type issueListCall struct {
	versionID int64
	filter    IssueFilter
}

type issueTriageCall struct {
	issueID   int64
	versionID int64
}

// fakeIssueRepository is the in-memory IssueRepository of the issue service
// tests. Every behavior is injectable so a test can force one port outcome
// without touching SQL.
type fakeIssueRepository struct {
	createResult     delivery.Issue
	createCreated    bool
	createErr        error
	createCalls      int
	createdCandidate delivery.Issue
	createdRefs      []delivery.IssueRef

	getIssues map[int64]delivery.Issue
	getCalls  []issueGetCall
	getErr    error

	refs       []delivery.IssueRef
	refsErr    error
	history    []delivery.IssueStatusChange
	historyErr error

	findByCaseRun map[int64]delivery.Issue
	findErrByCase map[int64]error
	findCalls     []int64

	updateResult delivery.Issue
	updateErr    error
	updateCalls  []IssueStatusUpdate

	mergeResult delivery.Issue
	mergeErr    error
	mergeCalls  []IssueMerge

	listResult []delivery.Issue
	listErr    error
	listCalls  []issueListCall
}

func (r *fakeIssueRepository) CreateCandidate(_ context.Context, issue delivery.Issue, refs []delivery.IssueRef) (delivery.Issue, bool, error) {
	r.createCalls++
	r.createdCandidate = issue
	r.createdRefs = append([]delivery.IssueRef(nil), refs...)
	if r.createErr != nil {
		return delivery.Issue{}, false, r.createErr
	}
	return r.createResult, r.createCreated, nil
}

func (r *fakeIssueRepository) Get(_ context.Context, versionID, issueID int64) (delivery.Issue, error) {
	r.getCalls = append(r.getCalls, issueGetCall{versionID: versionID, issueID: issueID})
	if r.getErr != nil {
		return delivery.Issue{}, r.getErr
	}
	issue, ok := r.getIssues[issueID]
	if !ok || (issue.VersionID != 0 && issue.VersionID != versionID) {
		return delivery.Issue{}, delivery.ErrNotFound
	}
	return issue, nil
}

func (r *fakeIssueRepository) GetByID(_ context.Context, issueID int64) (delivery.Issue, error) {
	issue, ok := r.getIssues[issueID]
	if !ok {
		return delivery.Issue{}, delivery.ErrNotFound
	}
	return issue, nil
}

func (r *fakeIssueRepository) List(_ context.Context, versionID int64, f IssueFilter) ([]delivery.Issue, error) {
	r.listCalls = append(r.listCalls, issueListCall{versionID: versionID, filter: f})
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.listResult, nil
}

func (r *fakeIssueRepository) UpdateStatus(_ context.Context, u IssueStatusUpdate) (delivery.Issue, error) {
	r.updateCalls = append(r.updateCalls, u)
	if r.updateErr != nil {
		return delivery.Issue{}, r.updateErr
	}
	return r.updateResult, nil
}

func (r *fakeIssueRepository) Merge(_ context.Context, m IssueMerge) (delivery.Issue, error) {
	r.mergeCalls = append(r.mergeCalls, m)
	if r.mergeErr != nil {
		return delivery.Issue{}, r.mergeErr
	}
	return r.mergeResult, nil
}

func (r *fakeIssueRepository) AppendHistory(context.Context, delivery.IssueStatusChange) error {
	return nil
}

func (r *fakeIssueRepository) ListRefs(context.Context, int64) ([]delivery.IssueRef, error) {
	if r.refsErr != nil {
		return nil, r.refsErr
	}
	return r.refs, nil
}

func (r *fakeIssueRepository) ListHistory(context.Context, int64) ([]delivery.IssueStatusChange, error) {
	if r.historyErr != nil {
		return nil, r.historyErr
	}
	return r.history, nil
}

func (r *fakeIssueRepository) FindByCaseRun(_ context.Context, _, caseRunID int64) (delivery.Issue, bool, error) {
	r.findCalls = append(r.findCalls, caseRunID)
	if err := r.findErrByCase[caseRunID]; err != nil {
		return delivery.Issue{}, false, err
	}
	issue, ok := r.findByCaseRun[caseRunID]
	return issue, ok, nil
}

var _ IssueRepository = (*fakeIssueRepository)(nil)

// fakeIssueExecution is the in-memory ExecutionReader of the scan/ingest tests.
type fakeIssueExecution struct {
	failed      []CaseRunSnapshot
	failedErr   error
	failedCalls []int64

	snapshots         map[int64]CaseRunSnapshot
	snapshotErrByCase map[int64]error
	snapshotCalls     []int64
}

func (e *fakeIssueExecution) ListFailedApplicationCaseRuns(_ context.Context, versionID int64) ([]CaseRunSnapshot, error) {
	e.failedCalls = append(e.failedCalls, versionID)
	if e.failedErr != nil {
		return nil, e.failedErr
	}
	return e.failed, nil
}

func (e *fakeIssueExecution) CaseRunSnapshot(_ context.Context, caseRunID int64) (CaseRunSnapshot, error) {
	e.snapshotCalls = append(e.snapshotCalls, caseRunID)
	if err := e.snapshotErrByCase[caseRunID]; err != nil {
		return CaseRunSnapshot{}, err
	}
	snapshot, ok := e.snapshots[caseRunID]
	if !ok {
		return CaseRunSnapshot{}, errIssueTestBoom
	}
	return snapshot, nil
}

func (e *fakeIssueExecution) WaveSnapshot(context.Context, int64, int64) (WaveSnapshot, error) {
	return WaveSnapshot{}, nil
}

func (e *fakeIssueExecution) VersionSnapshot(context.Context, int64) (VersionSnapshot, error) {
	return VersionSnapshot{}, nil
}

var _ ExecutionReader = (*fakeIssueExecution)(nil)

type recordedIssueEvent struct {
	eventType string
	versionID int64
	payload   map[string]any
}

type fakeIssueEvents struct {
	records []recordedIssueEvent
	err     error
}

func (p *fakeIssueEvents) Publish(_ context.Context, eventType string, versionID int64, payload map[string]any) error {
	copied := make(map[string]any, len(payload))
	for key, value := range payload {
		copied[key] = value
	}
	p.records = append(p.records, recordedIssueEvent{eventType: eventType, versionID: versionID, payload: copied})
	return p.err
}

var _ EventPublisher = (*fakeIssueEvents)(nil)

type recordedIssueAudit struct {
	actorID    int64
	action     string
	targetKind string
	targetID   string
	metadata   map[string]any
}

type fakeIssueAudit struct {
	records []recordedIssueAudit
	err     error
}

func (a *fakeIssueAudit) Write(_ context.Context, actorID int64, action, targetKind, targetID string, metadata map[string]any) error {
	copied := make(map[string]any, len(metadata))
	for key, value := range metadata {
		copied[key] = value
	}
	a.records = append(a.records, recordedIssueAudit{
		actorID:    actorID,
		action:     action,
		targetKind: targetKind,
		targetID:   targetID,
		metadata:   copied,
	})
	return a.err
}

var _ AuditSink = (*fakeIssueAudit)(nil)

type fakeIssueClock struct{ now time.Time }

func (c fakeIssueClock) Now() time.Time { return c.now }

type fakeIssueTasks struct {
	calls []issueTriageCall
	err   error
}

func (t *fakeIssueTasks) EnsureIssueTriage(_ context.Context, issueID, versionID int64) error {
	t.calls = append(t.calls, issueTriageCall{issueID: issueID, versionID: versionID})
	return t.err
}

func (t *fakeIssueTasks) EnsureReportTask(context.Context, int64, int32) error { return nil }

func (t *fakeIssueTasks) EnsureRequirementClose(context.Context, int64, int64, int32) error {
	return nil
}

func (t *fakeIssueTasks) EnsureVersionClose(context.Context, int64, int64, string) error {
	return nil
}

var _ TaskEnsurer = (*fakeIssueTasks)(nil)

type fakeIssueTriageReader struct {
	run   delivery.AnalysisRun
	found bool
	err   error
	calls []int64
}

func (r *fakeIssueTriageReader) LatestByIssue(_ context.Context, issueID int64) (delivery.AnalysisRun, bool, error) {
	r.calls = append(r.calls, issueID)
	if r.err != nil {
		return delivery.AnalysisRun{}, false, r.err
	}
	return r.run, r.found, nil
}

var _ IssueTriageReader = (*fakeIssueTriageReader)(nil)

type issueServiceFixture struct {
	repo   *fakeIssueRepository
	exec   *fakeIssueExecution
	events *fakeIssueEvents
	audit  *fakeIssueAudit
	clock  *fakeIssueClock
	tasks  *fakeIssueTasks
	svc    *IssueService
}

func newIssueServiceFixture() *issueServiceFixture {
	fixture := &issueServiceFixture{
		repo: &fakeIssueRepository{
			getIssues:     map[int64]delivery.Issue{},
			findByCaseRun: map[int64]delivery.Issue{},
			findErrByCase: map[int64]error{},
		},
		exec: &fakeIssueExecution{
			snapshots:         map[int64]CaseRunSnapshot{},
			snapshotErrByCase: map[int64]error{},
		},
		events: &fakeIssueEvents{},
		audit:  &fakeIssueAudit{},
		clock:  &fakeIssueClock{now: issueServiceTestNow},
		tasks:  &fakeIssueTasks{},
	}
	fixture.svc = NewIssueService(fixture.repo, fixture.exec, fixture.events, fixture.audit, fixture.clock, fixture.tasks)
	return fixture
}

func (f *issueServiceFixture) requireSingleEvent(t *testing.T, eventType string) recordedIssueEvent {
	t.Helper()
	if len(f.events.records) != 1 {
		t.Fatalf("publish calls = %d, want 1", len(f.events.records))
	}
	record := f.events.records[0]
	if record.eventType != eventType {
		t.Fatalf("event type = %q, want %q", record.eventType, eventType)
	}
	return record
}

func (f *issueServiceFixture) requireSingleAudit(t *testing.T, action string) recordedIssueAudit {
	t.Helper()
	if len(f.audit.records) != 1 {
		t.Fatalf("audit writes = %d, want 1", len(f.audit.records))
	}
	record := f.audit.records[0]
	if record.action != action {
		t.Fatalf("audit action = %q, want %q", record.action, action)
	}
	return record
}

func (f *issueServiceFixture) requireSingleTriageCall(t *testing.T) issueTriageCall {
	t.Helper()
	if len(f.tasks.calls) != 1 {
		t.Fatalf("EnsureIssueTriage calls = %d, want 1", len(f.tasks.calls))
	}
	return f.tasks.calls[0]
}

func issueServiceTestSnapshot(caseRunID int64) CaseRunSnapshot {
	return CaseRunSnapshot{
		ID:                       caseRunID,
		VersionID:                7,
		WaveID:                   11,
		CaseVersionID:            21,
		RequirementID:            31,
		CaseTitle:                "login fails",
		Status:                   "failed",
		FailureClass:             "application",
		DedupeHint:               "0123456789abcdef0123456789abcdef",
		Summary:                  "",
		FailedStepSeq:            2,
		FailedAssertionSeq:       1,
		FailedStepResultID:       41,
		FailedAssertionResultIDs: []int64{51, 52},
		FailedExpected:           "HTTP 200",
		FailedActual:             "HTTP 500",
		FailedReason:             "unexpected status",
		Category:                 "api",
		Severity:                 "major",
		EvidenceIDs:              []int64{61, 62},
	}
}

func issueServiceTestInt32(value int32) *int32 { return &value }
func issueServiceTestInt64(value int64) *int64 { return &value }

func requireIssueError(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want errors.Is(%v)", err, want)
	}
}

func requireIssueConflictReason(t *testing.T, err error, want string) {
	t.Helper()
	if !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("error = %v, want errors.Is(ErrStateConflict)", err)
	}
	var conflict *IssueStateConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %v, want *IssueStateConflictError", err)
	}
	if conflict.Reason != want {
		t.Fatalf("conflict reason = %q, want %q", conflict.Reason, want)
	}
}

func requireIssueMergeReason(t *testing.T, err error, want string) {
	t.Helper()
	if !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("error = %v, want errors.Is(ErrStateConflict)", err)
	}
	var mergeErr *delivery.MergeError
	if !errors.As(err, &mergeErr) {
		t.Fatalf("error = %v, want *delivery.MergeError", err)
	}
	if mergeErr.Reason != want {
		t.Fatalf("merge reason = %q, want %q", mergeErr.Reason, want)
	}
}

func TestScanFailuresCreatesIssuesAndSkips(t *testing.T) {
	fixture := newIssueServiceFixture()
	fixture.repo.createResult = delivery.Issue{
		ID:        900,
		VersionID: 7,
		Status:    delivery.IssueStatusOpen,
		Category:  "api",
		Severity:  "major",
		Source:    delivery.IssueCandidateSource,
	}
	fixture.repo.createCreated = true
	fixture.exec.failed = []CaseRunSnapshot{
		issueServiceTestSnapshot(101),
		{ID: 102, VersionID: 7, Status: "failed", FailureClass: "infrastructure", Category: "api", Severity: "major"},
		issueServiceTestSnapshot(103),
	}
	fixture.repo.findByCaseRun[103] = delivery.Issue{ID: 903, VersionID: 7}

	count, err := fixture.svc.ScanFailures(context.Background(), 7)
	if err != nil {
		t.Fatalf("ScanFailures() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("ScanFailures() count = %d, want 1", count)
	}
	if len(fixture.exec.failedCalls) != 1 || fixture.exec.failedCalls[0] != 7 {
		t.Fatalf("ListFailedApplicationCaseRuns calls = %v, want [7]", fixture.exec.failedCalls)
	}
	if len(fixture.repo.findCalls) != 2 || fixture.repo.findCalls[0] != 101 || fixture.repo.findCalls[1] != 103 {
		t.Fatalf("FindByCaseRun calls = %v, want [101 103]", fixture.repo.findCalls)
	}
	if fixture.repo.createCalls != 1 {
		t.Fatalf("CreateCandidate calls = %d, want 1", fixture.repo.createCalls)
	}
	if fixture.repo.createdCandidate.VersionID != 7 {
		t.Errorf("candidate version = %d, want 7", fixture.repo.createdCandidate.VersionID)
	}
	if fixture.repo.createdCandidate.Status != delivery.IssueStatusOpen {
		t.Errorf("candidate status = %q, want open", fixture.repo.createdCandidate.Status)
	}
	if fixture.repo.createdCandidate.DedupeKey != "0123456789abcdef0123456789abcdef" {
		t.Errorf("candidate dedupe key = %q, want the P3 hint", fixture.repo.createdCandidate.DedupeKey)
	}
	if !fixture.repo.createdCandidate.CreatedAt.Equal(issueServiceTestNow) {
		t.Errorf("candidate created_at = %v, want the injected clock %v",
			fixture.repo.createdCandidate.CreatedAt, issueServiceTestNow)
	}
	wantKinds := []string{
		delivery.IssueRefKindCaseRun,
		delivery.IssueRefKindCaseVersion,
		delivery.IssueRefKindWave,
		delivery.IssueRefKindStepResult,
		delivery.IssueRefKindAssertionResult,
		delivery.IssueRefKindAssertionResult,
		delivery.IssueRefKindEvidence,
		delivery.IssueRefKindEvidence,
		delivery.IssueRefKindRequirement,
	}
	if len(fixture.repo.createdRefs) != len(wantKinds) {
		t.Fatalf("candidate refs = %d, want %d (%v)", len(fixture.repo.createdRefs), len(wantKinds), wantKinds)
	}
	for i, kind := range wantKinds {
		if fixture.repo.createdRefs[i].Kind != kind {
			t.Errorf("ref[%d].kind = %q, want %q", i, fixture.repo.createdRefs[i].Kind, kind)
		}
	}

	event := fixture.requireSingleEvent(t, "issue.created")
	if event.versionID != 7 {
		t.Errorf("event version = %d, want 7", event.versionID)
	}
	if event.payload["issue_id"] != "900" || event.payload["status"] != "open" ||
		event.payload["category"] != "api" || event.payload["severity"] != "major" {
		t.Errorf("issue.created payload = %v", event.payload)
	}
	if event.payload["wave_id"] != "11" || event.payload["case_run_id"] != "101" {
		t.Errorf("issue.created refs payload = %v, want wave_id=11 case_run_id=101", event.payload)
	}
	audit := fixture.requireSingleAudit(t, "delivery.issue.created")
	if audit.targetKind != "issue" || audit.targetID != "900" {
		t.Errorf("audit target = %s/%s, want issue/900", audit.targetKind, audit.targetID)
	}
	if fixture.requireSingleTriageCall(t) != (issueTriageCall{issueID: 900, versionID: 7}) {
		t.Fatalf("EnsureIssueTriage call = %+v, want {900 7}", fixture.tasks.calls[0])
	}
}

func TestScanFailuresAggregatesPerItemFailures(t *testing.T) {
	fixture := newIssueServiceFixture()
	fixture.repo.createResult = delivery.Issue{ID: 900, VersionID: 7, Status: delivery.IssueStatusOpen}
	fixture.repo.createCreated = true
	fixture.exec.failed = []CaseRunSnapshot{
		issueServiceTestSnapshot(101),
		issueServiceTestSnapshot(102),
	}
	fixture.repo.findErrByCase[101] = errIssueTestBoom

	count, err := fixture.svc.ScanFailures(context.Background(), 7)
	if count != 1 {
		t.Fatalf("ScanFailures() count = %d, want 1", count)
	}
	requireIssueError(t, err, errIssueTestBoom)
	if len(fixture.repo.findCalls) != 2 {
		t.Fatalf("FindByCaseRun calls = %v, want both case runs", fixture.repo.findCalls)
	}
	if fixture.repo.createCalls != 1 {
		t.Fatalf("CreateCandidate calls = %d, want 1", fixture.repo.createCalls)
	}
}

func TestScanFailuresRejectsInvalidVersion(t *testing.T) {
	fixture := newIssueServiceFixture()
	count, err := fixture.svc.ScanFailures(context.Background(), 0)
	if count != 0 {
		t.Fatalf("ScanFailures() count = %d, want 0", count)
	}
	requireIssueError(t, err, ErrInvalidArgument)
	if len(fixture.exec.failedCalls) != 0 {
		t.Fatalf("ListFailedApplicationCaseRuns calls = %v, want none", fixture.exec.failedCalls)
	}
}

func TestIngestCandidateCreatesIssue(t *testing.T) {
	fixture := newIssueServiceFixture()
	fixture.exec.snapshots[50] = issueServiceTestSnapshot(50)
	fixture.repo.createResult = delivery.Issue{ID: 901, VersionID: 7, Status: delivery.IssueStatusOpen}
	fixture.repo.createCreated = true

	issue, created, err := fixture.svc.IngestCandidate(context.Background(), 50)
	if err != nil {
		t.Fatalf("IngestCandidate() error = %v", err)
	}
	if !created {
		t.Fatal("IngestCandidate() created = false, want true")
	}
	if issue.ID != 901 {
		t.Fatalf("issue.ID = %d, want 901", issue.ID)
	}
	if len(fixture.exec.snapshotCalls) != 1 || fixture.exec.snapshotCalls[0] != 50 {
		t.Fatalf("CaseRunSnapshot calls = %v, want [50]", fixture.exec.snapshotCalls)
	}

	candidate := fixture.repo.createdCandidate
	if candidate.VersionID != 7 {
		t.Errorf("candidate version = %d, want 7", candidate.VersionID)
	}
	if candidate.Title != "login fails · step 2: api failure" {
		t.Errorf("candidate title = %q", candidate.Title)
	}
	wantSummary := "expected: HTTP 200\nactual: HTTP 500\nreason: unexpected status"
	if candidate.Summary != wantSummary {
		t.Errorf("candidate summary = %q, want %q", candidate.Summary, wantSummary)
	}
	if candidate.Status != delivery.IssueStatusOpen || candidate.Category != "api" || candidate.Severity != "major" {
		t.Errorf("candidate = %+v, want open/api/major", candidate)
	}
	if candidate.Source != delivery.IssueCandidateSource || candidate.ExpectedRevision != 1 {
		t.Errorf("candidate source/revision = %q/%d", candidate.Source, candidate.ExpectedRevision)
	}
	if len(candidate.Fingerprint) != 64 {
		t.Errorf("candidate fingerprint = %q, want 64 hex chars", candidate.Fingerprint)
	}
	if !candidate.CreatedAt.Equal(issueServiceTestNow) || !candidate.UpdatedAt.Equal(issueServiceTestNow) {
		t.Errorf("candidate timestamps = %v/%v, want %v", candidate.CreatedAt, candidate.UpdatedAt, issueServiceTestNow)
	}
	if len(fixture.repo.createdRefs) != 9 {
		t.Fatalf("candidate refs = %d, want 9", len(fixture.repo.createdRefs))
	}
	if fixture.repo.createdRefs[0].Kind != delivery.IssueRefKindCaseRun || fixture.repo.createdRefs[0].RefID != 50 {
		t.Errorf("case_run ref = %+v, want case_run/50", fixture.repo.createdRefs[0])
	}
	if fixture.repo.createdRefs[8].Kind != delivery.IssueRefKindRequirement || fixture.repo.createdRefs[8].RefID != 31 {
		t.Errorf("requirement ref = %+v, want requirement/31", fixture.repo.createdRefs[8])
	}

	event := fixture.requireSingleEvent(t, "issue.created")
	if event.payload["case_run_id"] != "50" {
		t.Errorf("issue.created case_run_id = %v, want 50", event.payload["case_run_id"])
	}
	fixture.requireSingleAudit(t, "delivery.issue.created")
	if call := fixture.requireSingleTriageCall(t); call != (issueTriageCall{issueID: 901, versionID: 7}) {
		t.Fatalf("EnsureIssueTriage call = %+v, want {901 7}", call)
	}
}

func TestIngestCandidateIsIdempotentOnDedupe(t *testing.T) {
	fixture := newIssueServiceFixture()
	fixture.exec.snapshots[51] = issueServiceTestSnapshot(51)
	fixture.repo.createResult = delivery.Issue{ID: 902, VersionID: 7, Status: delivery.IssueStatusConfirmed}
	fixture.repo.createCreated = false

	issue, created, err := fixture.svc.IngestCandidate(context.Background(), 51)
	if err != nil {
		t.Fatalf("IngestCandidate() error = %v", err)
	}
	if created {
		t.Fatal("IngestCandidate() created = true on a dedupe hit, want false")
	}
	if issue.ID != 902 {
		t.Fatalf("issue.ID = %d, want the existing 902", issue.ID)
	}
	if len(fixture.events.records) != 0 {
		t.Errorf("dedupe hit published %d events, want none", len(fixture.events.records))
	}
	if len(fixture.audit.records) != 0 {
		t.Errorf("dedupe hit wrote %d audits, want none", len(fixture.audit.records))
	}
	if len(fixture.tasks.calls) != 0 {
		t.Errorf("dedupe hit ensured %d triage tasks, want none", len(fixture.tasks.calls))
	}
}

func TestIngestCandidateRejectsInvalidSnapshots(t *testing.T) {
	t.Run("non-positive case run id", func(t *testing.T) {
		fixture := newIssueServiceFixture()
		_, _, err := fixture.svc.IngestCandidate(context.Background(), 0)
		requireIssueError(t, err, ErrInvalidArgument)
		if len(fixture.exec.snapshotCalls) != 0 {
			t.Fatalf("CaseRunSnapshot calls = %v, want none", fixture.exec.snapshotCalls)
		}
	})

	t.Run("non-application failure class", func(t *testing.T) {
		fixture := newIssueServiceFixture()
		snapshot := issueServiceTestSnapshot(50)
		snapshot.FailureClass = "infrastructure"
		fixture.exec.snapshots[50] = snapshot
		_, _, err := fixture.svc.IngestCandidate(context.Background(), 50)
		requireIssueError(t, err, ErrInvalidArgument)
		if fixture.repo.createCalls != 0 {
			t.Fatalf("CreateCandidate calls = %d, want 0", fixture.repo.createCalls)
		}
	})

	t.Run("invalid category", func(t *testing.T) {
		fixture := newIssueServiceFixture()
		snapshot := issueServiceTestSnapshot(50)
		snapshot.Category = "network"
		fixture.exec.snapshots[50] = snapshot
		_, _, err := fixture.svc.IngestCandidate(context.Background(), 50)
		requireIssueError(t, err, ErrInvalidArgument)
		if fixture.repo.createCalls != 0 {
			t.Fatalf("CreateCandidate calls = %d, want 0", fixture.repo.createCalls)
		}
	})

	t.Run("execution read error", func(t *testing.T) {
		fixture := newIssueServiceFixture()
		fixture.exec.snapshotErrByCase[50] = errIssueTestBoom
		_, _, err := fixture.svc.IngestCandidate(context.Background(), 50)
		requireIssueError(t, err, errIssueTestBoom)
		if fixture.repo.createCalls != 0 {
			t.Fatalf("CreateCandidate calls = %d, want 0", fixture.repo.createCalls)
		}
	})
}

func TestIngestCandidateReportsTriageEnsureFailure(t *testing.T) {
	fixture := newIssueServiceFixture()
	fixture.exec.snapshots[50] = issueServiceTestSnapshot(50)
	fixture.repo.createResult = delivery.Issue{ID: 901, VersionID: 7, Status: delivery.IssueStatusOpen}
	fixture.repo.createCreated = true
	fixture.tasks.err = errIssueTestBoom

	issue, created, err := fixture.svc.IngestCandidate(context.Background(), 50)
	requireIssueError(t, err, errIssueTestBoom)
	if !created || issue.ID != 901 {
		t.Fatalf("IngestCandidate() = (%d, %v), want the created issue and true", issue.ID, created)
	}
	if len(fixture.events.records) != 1 || len(fixture.audit.records) != 1 {
		t.Errorf("side effects = %d events/%d audits, want 1/1 before the ensure failure",
			len(fixture.events.records), len(fixture.audit.records))
	}
}

func TestIssueServiceListPassesFilterThrough(t *testing.T) {
	fixture := newIssueServiceFixture()
	fixture.repo.listResult = []delivery.Issue{
		{ID: 9, VersionID: 7, Status: delivery.IssueStatusOpen},
		{ID: 8, VersionID: 7, Status: delivery.IssueStatusOpen},
	}
	filter := IssueFilter{
		Status:       "open",
		Category:     "api",
		Severity:     "minor",
		VersionSUTID: issueServiceTestInt64(5),
		Before:       100,
		Limit:        10,
	}
	issues, err := fixture.svc.List(context.Background(), 7, filter)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(issues) != 2 || issues[0].ID != 9 || issues[1].ID != 8 {
		t.Fatalf("List() = %+v, want the repository page", issues)
	}
	if len(fixture.repo.listCalls) != 1 {
		t.Fatalf("List calls = %d, want 1", len(fixture.repo.listCalls))
	}
	if call := fixture.repo.listCalls[0]; call.versionID != 7 || call.filter != filter {
		t.Fatalf("List call = %+v, want version 7 and the caller filter", call)
	}
}

func TestIssueServiceListRejectsInvalidArguments(t *testing.T) {
	cases := []struct {
		name    string
		version int64
		filter  IssueFilter
	}{
		{name: "version", version: 0},
		{name: "status", version: 7, filter: IssueFilter{Status: "closed"}},
		{name: "category", version: 7, filter: IssueFilter{Category: "network"}},
		{name: "severity", version: 7, filter: IssueFilter{Severity: "critical"}},
		{name: "version_sut", version: 7, filter: IssueFilter{VersionSUTID: issueServiceTestInt64(0)}},
		{name: "before", version: 7, filter: IssueFilter{Before: -1}},
		{name: "limit", version: 7, filter: IssueFilter{Limit: -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newIssueServiceFixture()
			_, err := fixture.svc.List(context.Background(), tc.version, tc.filter)
			requireIssueError(t, err, ErrInvalidArgument)
			if len(fixture.repo.listCalls) != 0 {
				t.Fatalf("List calls = %d, want none", len(fixture.repo.listCalls))
			}
		})
	}
}

func TestIssueServiceGetReturnsRefsHistoryAndTriage(t *testing.T) {
	fixture := newIssueServiceFixture()
	fixture.repo.getIssues[42] = delivery.Issue{ID: 42, VersionID: 7, Title: "boom", Status: delivery.IssueStatusOpen}
	fixture.repo.refs = []delivery.IssueRef{{ID: 1, IssueID: 42, Kind: delivery.IssueRefKindCaseRun, RefID: 50}}
	from := delivery.IssueStatusOpen
	fixture.repo.history = []delivery.IssueStatusChange{
		{ID: 1, IssueID: 42, From: &from, To: delivery.IssueStatusConfirmed},
	}
	run := delivery.AnalysisRun{ID: 77, VersionID: 7, Kind: "triage", Status: delivery.AnalysisStatusDone}
	reader := &fakeIssueTriageReader{run: run, found: true}
	fixture.svc.WithTriageReader(reader)

	detail, err := fixture.svc.Get(context.Background(), 7, 42)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if detail.Issue.ID != 42 || detail.Issue.Title != "boom" {
		t.Fatalf("detail issue = %+v", detail.Issue)
	}
	if len(detail.Refs) != 1 || detail.Refs[0].RefID != 50 {
		t.Fatalf("detail refs = %+v", detail.Refs)
	}
	if len(detail.History) != 1 || detail.History[0].To != delivery.IssueStatusConfirmed {
		t.Fatalf("detail history = %+v", detail.History)
	}
	if detail.Triage == nil || detail.Triage.ID != 77 {
		t.Fatalf("detail triage = %+v, want the latest analysis run", detail.Triage)
	}
	if len(reader.calls) != 1 || reader.calls[0] != 42 {
		t.Fatalf("LatestByIssue calls = %v, want [42]", reader.calls)
	}
}

func TestIssueServiceGetWithoutTriageReader(t *testing.T) {
	fixture := newIssueServiceFixture()
	fixture.repo.getIssues[42] = delivery.Issue{ID: 42, VersionID: 7, Status: delivery.IssueStatusOpen}

	detail, err := fixture.svc.Get(context.Background(), 7, 42)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if detail.Triage != nil {
		t.Fatalf("detail triage = %+v, want nil without a reader", detail.Triage)
	}
}

func TestIssueServiceGetErrors(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		fixture := newIssueServiceFixture()
		_, err := fixture.svc.Get(context.Background(), 7, 42)
		requireIssueError(t, err, ErrNotFound)
	})

	t.Run("invalid ids", func(t *testing.T) {
		for _, tc := range []struct {
			name      string
			versionID int64
			issueID   int64
		}{
			{name: "version", versionID: 0, issueID: 42},
			{name: "issue", versionID: 7, issueID: 0},
		} {
			t.Run(tc.name, func(t *testing.T) {
				fixture := newIssueServiceFixture()
				_, err := fixture.svc.Get(context.Background(), tc.versionID, tc.issueID)
				requireIssueError(t, err, ErrInvalidArgument)
			})
		}
	})

	t.Run("triage read error", func(t *testing.T) {
		fixture := newIssueServiceFixture()
		fixture.repo.getIssues[42] = delivery.Issue{ID: 42, VersionID: 7, Status: delivery.IssueStatusOpen}
		fixture.svc.WithTriageReader(&fakeIssueTriageReader{err: errIssueTestBoom})
		_, err := fixture.svc.Get(context.Background(), 7, 42)
		requireIssueError(t, err, errIssueTestBoom)
	})
}

func TestIssueServiceConfirm(t *testing.T) {
	fixture := newIssueServiceFixture()
	fixture.repo.getIssues[42] = delivery.Issue{ID: 42, VersionID: 7, Status: delivery.IssueStatusOpen, ExpectedRevision: 1}
	fixture.repo.updateResult = delivery.Issue{ID: 42, VersionID: 7, Status: delivery.IssueStatusConfirmed, ExpectedRevision: 2}
	expected := issueServiceTestInt32(1)

	updated, err := fixture.svc.Confirm(context.Background(), 7, 42, 99, "  needs fix  ", expected)
	if err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	if updated.Status != delivery.IssueStatusConfirmed {
		t.Fatalf("Confirm() status = %q, want confirmed", updated.Status)
	}
	if len(fixture.repo.updateCalls) != 1 {
		t.Fatalf("UpdateStatus calls = %d, want 1", len(fixture.repo.updateCalls))
	}
	update := fixture.repo.updateCalls[0]
	if update.VersionID != 7 || update.IssueID != 42 || update.To != delivery.IssueStatusConfirmed {
		t.Errorf("UpdateStatus = %+v", update)
	}
	if update.Reason != "needs fix" {
		t.Errorf("UpdateStatus reason = %q, want trimmed reason", update.Reason)
	}
	if update.ActorID != 99 || update.ActorKind != "user" {
		t.Errorf("UpdateStatus actor = %d/%q, want 99/user", update.ActorID, update.ActorKind)
	}
	if update.ExpectedRevision == nil || *update.ExpectedRevision != 1 {
		t.Errorf("UpdateStatus expected revision = %v, want 1", update.ExpectedRevision)
	}

	event := fixture.requireSingleEvent(t, "issue.updated")
	if event.payload["issue_id"] != "42" || event.payload["from_status"] != "open" ||
		event.payload["to_status"] != "confirmed" || event.payload["reason"] != "needs fix" {
		t.Errorf("issue.updated payload = %v", event.payload)
	}
	audit := fixture.requireSingleAudit(t, "delivery.issue.confirmed")
	if audit.actorID != 99 || audit.targetKind != "issue" || audit.targetID != "42" {
		t.Errorf("audit = %+v", audit)
	}
}

func TestIssueServiceConfirmRejectsInvalidTransition(t *testing.T) {
	fixture := newIssueServiceFixture()
	fixture.repo.getIssues[42] = delivery.Issue{ID: 42, VersionID: 7, Status: delivery.IssueStatusConfirmed}

	_, err := fixture.svc.Confirm(context.Background(), 7, 42, 99, "", nil)
	requireIssueConflictReason(t, err, "issue_not_open")
	if len(fixture.repo.updateCalls) != 0 {
		t.Fatalf("UpdateStatus calls = %d, want none", len(fixture.repo.updateCalls))
	}
	if len(fixture.events.records) != 0 {
		t.Fatalf("events = %d, want none", len(fixture.events.records))
	}
}

func TestIssueServiceConfirmArgumentValidation(t *testing.T) {
	t.Run("stale expected revision", func(t *testing.T) {
		fixture := newIssueServiceFixture()
		expected := issueServiceTestInt32(0)
		_, err := fixture.svc.Confirm(context.Background(), 7, 42, 99, "", expected)
		requireIssueError(t, err, ErrInvalidArgument)
	})

	t.Run("reason too long", func(t *testing.T) {
		fixture := newIssueServiceFixture()
		_, err := fixture.svc.Confirm(context.Background(), 7, 42, 99, strings.Repeat("x", 2001), nil)
		requireIssueError(t, err, ErrInvalidArgument)
	})

	t.Run("not found", func(t *testing.T) {
		fixture := newIssueServiceFixture()
		_, err := fixture.svc.Confirm(context.Background(), 7, 42, 99, "", nil)
		requireIssueError(t, err, ErrNotFound)
	})

	t.Run("repository state conflict", func(t *testing.T) {
		fixture := newIssueServiceFixture()
		fixture.repo.getIssues[42] = delivery.Issue{ID: 42, VersionID: 7, Status: delivery.IssueStatusOpen}
		fixture.repo.updateErr = delivery.ErrStateConflict
		_, err := fixture.svc.Confirm(context.Background(), 7, 42, 99, "", nil)
		requireIssueError(t, err, delivery.ErrStateConflict)
		if len(fixture.events.records) != 0 {
			t.Fatalf("events = %d, want none on a failed write", len(fixture.events.records))
		}
	})
}

func TestIssueServiceDismiss(t *testing.T) {
	fixture := newIssueServiceFixture()
	fixture.repo.getIssues[42] = delivery.Issue{ID: 42, VersionID: 7, Status: delivery.IssueStatusOpen}
	fixture.repo.updateResult = delivery.Issue{ID: 42, VersionID: 7, Status: delivery.IssueStatusDismissed}

	updated, err := fixture.svc.Dismiss(context.Background(), 7, 42, 99, "not a product bug", nil)
	if err != nil {
		t.Fatalf("Dismiss() error = %v", err)
	}
	if updated.Status != delivery.IssueStatusDismissed {
		t.Fatalf("Dismiss() status = %q, want dismissed", updated.Status)
	}
	if len(fixture.repo.updateCalls) != 1 || fixture.repo.updateCalls[0].To != delivery.IssueStatusDismissed {
		t.Fatalf("UpdateStatus = %+v, want dismissed", fixture.repo.updateCalls)
	}
	event := fixture.requireSingleEvent(t, "issue.updated")
	if event.payload["to_status"] != "dismissed" || event.payload["reason"] != "not a product bug" {
		t.Errorf("issue.updated payload = %v", event.payload)
	}
	fixture.requireSingleAudit(t, "delivery.issue.dismissed")
}

func TestIssueServiceDismissRejectsNonOpenIssue(t *testing.T) {
	fixture := newIssueServiceFixture()
	fixture.repo.getIssues[42] = delivery.Issue{ID: 42, VersionID: 7, Status: delivery.IssueStatusDismissed}

	_, err := fixture.svc.Dismiss(context.Background(), 7, 42, 99, "", nil)
	requireIssueConflictReason(t, err, "issue_not_open")
}

func TestIssueServiceReopen(t *testing.T) {
	fixture := newIssueServiceFixture()
	fixture.repo.getIssues[42] = delivery.Issue{ID: 42, VersionID: 7, Status: delivery.IssueStatusConfirmed, ReopenCount: 0}
	fixture.repo.updateResult = delivery.Issue{ID: 42, VersionID: 7, Status: delivery.IssueStatusOpen, ReopenCount: 1}

	updated, err := fixture.svc.Reopen(context.Background(), 7, 42, 99, "  regression seen  ", nil)
	if err != nil {
		t.Fatalf("Reopen() error = %v", err)
	}
	if updated.Status != delivery.IssueStatusOpen || updated.ReopenCount != 1 {
		t.Fatalf("Reopen() = %+v, want open with reopen_count 1", updated)
	}
	if len(fixture.repo.updateCalls) != 1 || fixture.repo.updateCalls[0].To != delivery.IssueStatusOpen {
		t.Fatalf("UpdateStatus = %+v, want open", fixture.repo.updateCalls)
	}
	if reason := fixture.repo.updateCalls[0].Reason; reason != "regression seen" {
		t.Errorf("UpdateStatus reason = %q, want trimmed reason", reason)
	}
	event := fixture.requireSingleEvent(t, "issue.reopened")
	if event.payload["issue_id"] != "42" || event.payload["reason"] != "regression seen" {
		t.Errorf("issue.reopened payload = %v", event.payload)
	}
	if event.payload["reopen_count"] != 1 {
		t.Errorf("issue.reopened reopen_count = %v, want 1", event.payload["reopen_count"])
	}
	fixture.requireSingleAudit(t, "delivery.issue.reopened")
}

func TestIssueServiceReopenRequiresReason(t *testing.T) {
	fixture := newIssueServiceFixture()
	for _, reason := range []string{"", "   "} {
		_, err := fixture.svc.Reopen(context.Background(), 7, 42, 99, reason, nil)
		requireIssueError(t, err, ErrInvalidArgument)
	}
	if len(fixture.repo.getCalls) != 0 {
		t.Fatalf("Get calls = %d, want none for an invalid reason", len(fixture.repo.getCalls))
	}
	if len(fixture.repo.updateCalls) != 0 {
		t.Fatalf("UpdateStatus calls = %d, want none", len(fixture.repo.updateCalls))
	}
}

func TestIssueServiceReopenRejectsNonReopenableIssue(t *testing.T) {
	fixture := newIssueServiceFixture()
	fixture.repo.getIssues[42] = delivery.Issue{ID: 42, VersionID: 7, Status: delivery.IssueStatusOpen}

	_, err := fixture.svc.Reopen(context.Background(), 7, 42, 99, "still broken", nil)
	requireIssueConflictReason(t, err, "issue_not_reopenable")
	if len(fixture.repo.updateCalls) != 0 {
		t.Fatalf("UpdateStatus calls = %d, want none", len(fixture.repo.updateCalls))
	}
}

func TestIssueServiceMerge(t *testing.T) {
	fixture := newIssueServiceFixture()
	fixture.repo.getIssues[10] = delivery.Issue{ID: 10, VersionID: 7, Status: delivery.IssueStatusOpen}
	fixture.repo.getIssues[20] = delivery.Issue{ID: 20, VersionID: 7, Status: delivery.IssueStatusConfirmed}
	fixture.repo.mergeResult = delivery.Issue{
		ID:           10,
		VersionID:    7,
		Status:       delivery.IssueStatusMerged,
		MergedIntoID: issueServiceTestInt64(20),
	}
	expected := issueServiceTestInt32(1)

	merged, err := fixture.svc.Merge(context.Background(), 7, 10, 20, 99, "duplicate", expected)
	if err != nil {
		t.Fatalf("Merge() error = %v", err)
	}
	if merged.Status != delivery.IssueStatusMerged || merged.MergedIntoID == nil || *merged.MergedIntoID != 20 {
		t.Fatalf("Merge() = %+v, want merged into 20", merged)
	}
	if len(fixture.repo.mergeCalls) != 1 {
		t.Fatalf("Merge calls = %d, want 1", len(fixture.repo.mergeCalls))
	}
	call := fixture.repo.mergeCalls[0]
	if call.VersionID != 7 || call.SourceID != 10 || call.TargetID != 20 {
		t.Errorf("Merge call = %+v", call)
	}
	if call.ActorID != 99 || call.Reason != "duplicate" {
		t.Errorf("Merge actor/reason = %d/%q", call.ActorID, call.Reason)
	}
	if call.ExpectedRevision == nil || *call.ExpectedRevision != 1 {
		t.Errorf("Merge expected revision = %v, want 1", call.ExpectedRevision)
	}
	event := fixture.requireSingleEvent(t, "issue.merged")
	if event.payload["issue_id"] != "10" || event.payload["merged_into_id"] != "20" ||
		event.payload["reason"] != "duplicate" {
		t.Errorf("issue.merged payload = %v", event.payload)
	}
	audit := fixture.requireSingleAudit(t, "delivery.issue.merged")
	if audit.targetID != "10" || audit.metadata["merged_into_id"] != "20" {
		t.Errorf("audit = %+v", audit)
	}
}

func TestIssueServiceMergeRejections(t *testing.T) {
	t.Run("self merge", func(t *testing.T) {
		fixture := newIssueServiceFixture()
		_, err := fixture.svc.Merge(context.Background(), 7, 10, 10, 99, "", nil)
		requireIssueMergeReason(t, err, "merge_target_invalid")
		if len(fixture.repo.mergeCalls) != 0 {
			t.Fatalf("Merge calls = %d, want none", len(fixture.repo.mergeCalls))
		}
	})

	t.Run("missing target is a state conflict", func(t *testing.T) {
		fixture := newIssueServiceFixture()
		fixture.repo.getIssues[10] = delivery.Issue{ID: 10, VersionID: 7, Status: delivery.IssueStatusOpen}
		_, err := fixture.svc.Merge(context.Background(), 7, 10, 20, 99, "", nil)
		requireIssueMergeReason(t, err, "merge_target_invalid")
		if len(fixture.repo.mergeCalls) != 0 {
			t.Fatalf("Merge calls = %d, want none", len(fixture.repo.mergeCalls))
		}
	})

	t.Run("cross-version target", func(t *testing.T) {
		fixture := newIssueServiceFixture()
		fixture.repo.getIssues[10] = delivery.Issue{ID: 10, VersionID: 7, Status: delivery.IssueStatusOpen}
		fixture.repo.getIssues[20] = delivery.Issue{ID: 20, VersionID: 8, Status: delivery.IssueStatusConfirmed}
		_, err := fixture.svc.Merge(context.Background(), 7, 10, 20, 99, "", nil)
		requireIssueMergeReason(t, err, "merge_target_invalid")
	})

	t.Run("merged source is terminal", func(t *testing.T) {
		fixture := newIssueServiceFixture()
		fixture.repo.getIssues[10] = delivery.Issue{ID: 10, VersionID: 7, Status: delivery.IssueStatusMerged}
		fixture.repo.getIssues[20] = delivery.Issue{ID: 20, VersionID: 7, Status: delivery.IssueStatusOpen}
		_, err := fixture.svc.Merge(context.Background(), 7, 10, 20, 99, "", nil)
		requireIssueError(t, err, delivery.ErrStateConflict)
	})

	t.Run("merge cycle", func(t *testing.T) {
		fixture := newIssueServiceFixture()
		fixture.repo.getIssues[10] = delivery.Issue{ID: 10, VersionID: 7, Status: delivery.IssueStatusOpen}
		fixture.repo.getIssues[20] = delivery.Issue{
			ID:           20,
			VersionID:    7,
			Status:       delivery.IssueStatusConfirmed,
			MergedIntoID: issueServiceTestInt64(10),
		}
		_, err := fixture.svc.Merge(context.Background(), 7, 10, 20, 99, "", nil)
		requireIssueMergeReason(t, err, "merge_cycle")
		if len(fixture.repo.mergeCalls) != 0 {
			t.Fatalf("Merge calls = %d, want none", len(fixture.repo.mergeCalls))
		}
	})

	t.Run("source not found", func(t *testing.T) {
		fixture := newIssueServiceFixture()
		_, err := fixture.svc.Merge(context.Background(), 7, 10, 20, 99, "", nil)
		requireIssueError(t, err, ErrNotFound)
	})

	t.Run("repository CAS conflict", func(t *testing.T) {
		fixture := newIssueServiceFixture()
		fixture.repo.getIssues[10] = delivery.Issue{ID: 10, VersionID: 7, Status: delivery.IssueStatusOpen}
		fixture.repo.getIssues[20] = delivery.Issue{ID: 20, VersionID: 7, Status: delivery.IssueStatusOpen}
		fixture.repo.mergeErr = delivery.ErrStateConflict
		_, err := fixture.svc.Merge(context.Background(), 7, 10, 20, 99, "", issueServiceTestInt32(9))
		requireIssueError(t, err, delivery.ErrStateConflict)
		if len(fixture.events.records) != 0 || len(fixture.audit.records) != 0 {
			t.Fatalf("side effects = %d events/%d audits, want none on a failed write",
				len(fixture.events.records), len(fixture.audit.records))
		}
	})

	t.Run("invalid ids", func(t *testing.T) {
		cases := []struct {
			name      string
			versionID int64
			sourceID  int64
			targetID  int64
			expected  *int32
		}{
			{name: "version", versionID: 0, sourceID: 10, targetID: 20},
			{name: "source", versionID: 7, sourceID: 0, targetID: 20},
			{name: "target", versionID: 7, sourceID: 10, targetID: 0},
			{name: "expected revision", versionID: 7, sourceID: 10, targetID: 20, expected: issueServiceTestInt32(0)},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				fixture := newIssueServiceFixture()
				_, err := fixture.svc.Merge(context.Background(), tc.versionID, tc.sourceID, tc.targetID, 99, "", tc.expected)
				requireIssueError(t, err, ErrInvalidArgument)
				if len(fixture.repo.mergeCalls) != 0 {
					t.Fatalf("Merge calls = %d, want none", len(fixture.repo.mergeCalls))
				}
			})
		}
	})
}

func TestIssueServiceProjectionsAreBestEffort(t *testing.T) {
	fixture := newIssueServiceFixture()
	fixture.repo.getIssues[42] = delivery.Issue{ID: 42, VersionID: 7, Status: delivery.IssueStatusOpen}
	fixture.repo.updateResult = delivery.Issue{ID: 42, VersionID: 7, Status: delivery.IssueStatusConfirmed}
	fixture.events.err = errIssueTestBoom
	fixture.audit.err = errIssueTestBoom

	updated, err := fixture.svc.Confirm(context.Background(), 7, 42, 99, "", nil)
	if err != nil {
		t.Fatalf("Confirm() error = %v, projection failures must not roll back the write", err)
	}
	if updated.Status != delivery.IssueStatusConfirmed {
		t.Fatalf("Confirm() status = %q, want confirmed", updated.Status)
	}
	if len(fixture.events.records) != 1 || len(fixture.audit.records) != 1 {
		t.Fatalf("projection attempts = %d events/%d audits, want 1/1",
			len(fixture.events.records), len(fixture.audit.records))
	}
}
