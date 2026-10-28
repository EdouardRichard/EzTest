package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// ---------------------------------------------------------------------------
// fakes
// ---------------------------------------------------------------------------

type reportFakeClock struct{ now time.Time }

func (c reportFakeClock) Now() time.Time { return c.now }

type reportFakeIDs struct{ next int64 }

func (f *reportFakeIDs) Next() int64 {
	id := f.next
	f.next++
	return id
}

type reportFakeVersionReader struct {
	version ReportVersionSource
	err     error
}

func (f *reportFakeVersionReader) GetReportVersion(context.Context, int64) (ReportVersionSource, error) {
	return f.version, f.err
}

// reportFakeBatchLookup records one PreviousBatchID call.
type reportFakeBatchLookup struct {
	VersionID int64
	BatchID   int64
}

type reportFakeExecutionReader struct {
	batch ReportBatchSource
	cases []ReportCaseRunSource
	err   error

	previousBatchID  int64
	hasPreviousBatch bool
	previousBatchErr error
	previousCalls    []reportFakeBatchLookup
}

func (f *reportFakeExecutionReader) GetReportBatch(context.Context, int64, int64) (ReportBatchSource, error) {
	return f.batch, f.err
}

func (f *reportFakeExecutionReader) ListReportCaseRuns(context.Context, int64) ([]ReportCaseRunSource, error) {
	out := make([]ReportCaseRunSource, len(f.cases))
	copy(out, f.cases)
	return out, f.err
}

// PreviousBatchID implements the batch_no-1 lookup independently of any report
// creation order.
func (f *reportFakeExecutionReader) PreviousBatchID(_ context.Context, versionID, batchID int64) (int64, bool, error) {
	f.previousCalls = append(f.previousCalls, reportFakeBatchLookup{VersionID: versionID, BatchID: batchID})
	if f.previousBatchErr != nil {
		return 0, false, f.previousBatchErr
	}
	return f.previousBatchID, f.hasPreviousBatch, nil
}

type reportFakeGraphReader struct {
	requirements []ReportRequirementSource
	err          error
}

func (f *reportFakeGraphReader) ListReportRequirements(context.Context, int64) ([]ReportRequirementSource, error) {
	out := make([]ReportRequirementSource, len(f.requirements))
	copy(out, f.requirements)
	return out, f.err
}

type reportFakeIssueReader struct {
	issues []ReportIssueSource
	err    error
}

func (f *reportFakeIssueReader) ListReportIssues(context.Context, int64, int64) ([]ReportIssueSource, error) {
	out := make([]ReportIssueSource, len(f.issues))
	copy(out, f.issues)
	return out, f.err
}

type reportFakeEvents struct {
	events []ReportEvent
	err    error
}

func (f *reportFakeEvents) PublishReportEvent(_ context.Context, event ReportEvent) error {
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, event)
	return nil
}

func (f *reportFakeEvents) types() []string {
	out := make([]string, 0, len(f.events))
	for _, event := range f.events {
		out = append(out, event.Type)
	}
	return out
}

func (f *reportFakeEvents) countType(eventType string) int {
	total := 0
	for _, event := range f.events {
		if event.Type == eventType {
			total++
		}
	}
	return total
}

type reportFakeAudit struct {
	events []ReportAuditEvent
	err    error
}

func (f *reportFakeAudit) RecordReportAudit(_ context.Context, event ReportAuditEvent) error {
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, event)
	return nil
}

func (f *reportFakeAudit) actions() []string {
	out := make([]string, 0, len(f.events))
	for _, event := range f.events {
		out = append(out, event.Action)
	}
	return out
}

type reportFakeFiles struct {
	files    map[string][]byte
	writeErr error
	openErr  error
}

func newReportFakeFiles() *reportFakeFiles {
	return &reportFakeFiles{files: make(map[string][]byte)}
}

func (f *reportFakeFiles) Write(_ context.Context, rel string, data []byte) (string, string, int64, error) {
	if f.writeErr != nil {
		return "", "", 0, f.writeErr
	}
	stored := append([]byte(nil), data...)
	f.files[rel] = stored
	sum := sha256.Sum256(stored)
	return rel, hex.EncodeToString(sum[:]), int64(len(stored)), nil
}

func (f *reportFakeFiles) Open(_ context.Context, rel string) (io.ReadCloser, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	data, ok := f.files[rel]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (f *reportFakeFiles) corrupt(rel string) {
	data := f.files[rel]
	if len(data) == 0 {
		f.files[rel] = []byte("corrupted")
		return
	}
	mutated := append([]byte(nil), data...)
	mutated[len(mutated)-1] ^= 0xff
	f.files[rel] = mutated
}

type reportFakeStore struct {
	reports     map[int64]domain.Report
	createErr   error
	markedStale int
	deleted     []int64
}

func newReportFakeStore() *reportFakeStore {
	return &reportFakeStore{reports: make(map[int64]domain.Report)}
}

func (s *reportFakeStore) Create(_ context.Context, report domain.Report) (domain.Report, error) {
	if s.createErr != nil {
		return domain.Report{}, s.createErr
	}
	for _, existing := range s.reports {
		if existing.BatchID == report.BatchID && existing.Revision == report.Revision {
			return domain.Report{}, fmt.Errorf("%w: batch %d revision %d",
				domain.ErrReportRevisionConflict, report.BatchID, report.Revision)
		}
	}
	s.reports[report.ID] = report
	return report, nil
}

func (s *reportFakeStore) Get(_ context.Context, versionID, reportID int64) (domain.Report, error) {
	report, ok := s.reports[reportID]
	if !ok || report.VersionID != versionID {
		return domain.Report{}, fmt.Errorf("%w: report %d", domain.ErrReportNotFound, reportID)
	}
	return report, nil
}

func (s *reportFakeStore) LatestByBatch(_ context.Context, versionID, batchID int64) (domain.Report, error) {
	var latest domain.Report
	found := false
	for _, report := range s.reports {
		if report.VersionID != versionID || report.BatchID != batchID {
			continue
		}
		if !found || report.Revision > latest.Revision {
			latest, found = report, true
		}
	}
	if !found {
		return domain.Report{}, fmt.Errorf("%w: batch %d", domain.ErrReportNotFound, batchID)
	}
	return latest, nil
}

func (s *reportFakeStore) List(_ context.Context, versionID int64, filter ReportFilter) ([]domain.Report, string, bool, error) {
	limit := filter.Limit
	if limit <= 0 || limit > platform.MaxLimit {
		limit = platform.DefaultLimit
	}
	matched := make([]domain.Report, 0, len(s.reports))
	for _, report := range s.reports {
		if report.VersionID != versionID {
			continue
		}
		if filter.BatchID != nil && report.BatchID != *filter.BatchID {
			continue
		}
		if filter.Status != "" && report.Status != filter.Status {
			continue
		}
		matched = append(matched, report)
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if !matched[i].CreatedAt.Equal(matched[j].CreatedAt) {
			return matched[i].CreatedAt.After(matched[j].CreatedAt)
		}
		return matched[i].ID > matched[j].ID
	})
	if filter.Cursor != "" {
		cursorID, err := platform.DecodeCursor(filter.Cursor)
		if err != nil {
			return nil, "", false, err
		}
		cursorIndex := -1
		for index, report := range matched {
			if report.ID == cursorID {
				cursorIndex = index
				break
			}
		}
		if cursorIndex >= 0 {
			matched = matched[cursorIndex+1:]
		}
	}
	hasMore := len(matched) > limit
	if hasMore {
		matched = matched[:limit]
	}
	nextCursor := ""
	if hasMore && len(matched) > 0 {
		nextCursor = platform.EncodeCursor(matched[len(matched)-1].ID)
	}
	return matched, nextCursor, hasMore, nil
}

func (s *reportFakeStore) NextRevision(_ context.Context, _ int64, batchID int64) (int, error) {
	maxRevision := 0
	for _, report := range s.reports {
		if report.BatchID == batchID && report.Revision > maxRevision {
			maxRevision = report.Revision
		}
	}
	return maxRevision + 1, nil
}

func (s *reportFakeStore) MarkStale(_ context.Context, versionID, reportID int64, fingerprint, reason string, at time.Time) (bool, error) {
	report, ok := s.reports[reportID]
	if !ok || report.VersionID != versionID || report.Status != domain.ReportStatusPublished {
		return false, nil
	}
	if report.InputFingerprint != fingerprint {
		return false, nil
	}
	report.Status = domain.ReportStatusStale
	report.StaleReason = reason
	report.UpdatedAt = at
	s.reports[reportID] = report
	s.markedStale++
	return true, nil
}

func (s *reportFakeStore) MarkPublished(_ context.Context, versionID, reportID int64, at time.Time) (bool, error) {
	report, ok := s.reports[reportID]
	if !ok || report.VersionID != versionID || report.Status != domain.ReportStatusDraft {
		return false, nil
	}
	report.Status = domain.ReportStatusPublished
	report.PublishedAt = &at
	report.UpdatedAt = at
	s.reports[reportID] = report
	return true, nil
}

func (s *reportFakeStore) Delete(_ context.Context, versionID, reportID int64) error {
	if report, ok := s.reports[reportID]; ok && report.VersionID == versionID {
		delete(s.reports, reportID)
	}
	s.deleted = append(s.deleted, reportID)
	return nil
}

// ---------------------------------------------------------------------------
// setup
// ---------------------------------------------------------------------------

type reportTestSetup struct {
	versions  *reportFakeVersionReader
	execution *reportFakeExecutionReader
	graph     *reportFakeGraphReader
	issues    *reportFakeIssueReader
	reports   *reportFakeStore
	files     *reportFakeFiles
	events    *reportFakeEvents
	audit     *reportFakeAudit
	ids       *reportFakeIDs
	clock     reportFakeClock
	service   *ReportService
}

func reportTestTimePointer(value time.Time) *time.Time { return &value }

func newReportTestSetup(t *testing.T) *reportTestSetup {
	t.Helper()
	started := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	finished := started.Add(30 * time.Minute)
	updatedRun := finished.Add(-5 * time.Minute)
	updatedIssue := finished.Add(-10 * time.Minute)
	setup := &reportTestSetup{
		versions: &reportFakeVersionReader{version: ReportVersionSource{
			ID: 10, Name: "结算系统", Label: "v1.2", Status: "executing",
			CodeBaselineCommit: strings.Repeat("c", 40),
		}},
		execution: &reportFakeExecutionReader{
			previousBatchID:  19,
			hasPreviousBatch: true,
			batch: ReportBatchSource{
				ID: 20, VersionID: 10, BatchNo: 2, Kind: "incremental", Status: "done",
				BaselineHash: strings.Repeat("a", 64), GraphHash: strings.Repeat("b", 64),
				CodeBaseCommit: strings.Repeat("c", 40), HeadCommit: strings.Repeat("d", 40),
				StartedAt: reportTestTimePointer(started), FinishedAt: reportTestTimePointer(finished),
				Environment: "staging", System: "payment",
				CaseCount: 3, PassedCount: 1, FailedCount: 1, BlockedCount: 1,
				FlakyCaseIDs: []int64{12},
			},
			cases: []ReportCaseRunSource{
				{
					CaseRunID: 102, CaseNodeID: 12, Title: "结算失败", Source: "baseline",
					Status: "failed", FailureClass: "application", Attempt: 1,
					StepCount: 3, AssertionCount: 2, EvidenceCount: 4, UpdatedAt: updatedRun,
				},
				{
					CaseRunID: 101, CaseNodeID: 11, Title: "结算成功", Source: "ai_supplement",
					Status: "passed", Attempt: 1, StepCount: 2, AssertionCount: 1,
					EvidenceCount: 1, UpdatedAt: updatedRun,
				},
				{
					CaseRunID: 103, CaseNodeID: 13, Title: "退款阻塞", Source: "baseline",
					Status: "blocked", FailureClass: "environment", Attempt: 1,
					StepCount: 1, AssertionCount: 1, EvidenceCount: 2, UpdatedAt: updatedRun,
				},
			},
		},
		graph: &reportFakeGraphReader{requirements: []ReportRequirementSource{
			{RequirementID: 32, Title: "退款需求", Status: "clarified", CaseNodeIDs: []int64{11}},
			{RequirementID: 31, Title: "结算需求", Status: "clarified", CaseNodeIDs: []int64{13, 12}},
		}},
		issues: &reportFakeIssueReader{issues: []ReportIssueSource{
			{
				IssueID: 41, Title: "轻微问题", Severity: "minor", Status: "open",
				Confidence: "medium", Category: "defect",
				CodeRefs: []domain.ReportCodeRef{
					{Path: "src/b.ts", Line: 20},
					{Path: "src/a.ts", Line: 10},
				},
				Revision: 1, UpdatedAt: updatedIssue,
			},
			{
				IssueID: 43, Title: "另一个轻微问题", Severity: "minor", Status: "open",
				Confidence: "low", Category: "defect", Revision: 1, UpdatedAt: updatedIssue,
			},
			{
				IssueID: 42, Title: "阻塞问题", Severity: "blocker", Status: "confirmed",
				Confidence: "high", Category: "defect", CaseNodeID: reportTestInt64(12),
				Revision: 2, UpdatedAt: updatedIssue,
			},
		}},
		reports: newReportFakeStore(),
		files:   newReportFakeFiles(),
		events:  &reportFakeEvents{},
		audit:   &reportFakeAudit{},
		ids:     &reportFakeIDs{next: 1000},
		clock:   reportFakeClock{now: time.Date(2026, 10, 28, 11, 0, 0, 0, time.UTC)},
	}
	setup.service = NewReportService(ReportServiceDeps{
		Versions:  setup.versions,
		Execution: setup.execution,
		Graph:     setup.graph,
		Issues:    setup.issues,
		Reports:   setup.reports,
		Files:     setup.files,
		Events:    setup.events,
		Audit:     setup.audit,
		IDs:       setup.ids,
		Clock:     setup.clock,
	})
	return setup
}

func reportTestInt64(value int64) *int64 { return &value }

func (s *reportTestSetup) ensure(t *testing.T) (domain.Report, bool) {
	t.Helper()
	report, created, err := s.service.Ensure(context.Background(), 10, 20, 7)
	if err != nil {
		t.Fatalf("ensure report: %v", err)
	}
	return report, created
}

func reportTestSHA(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func reportTestContentJSON(t *testing.T, report domain.Report) []byte {
	t.Helper()
	encoded, err := report.Content.CanonicalJSON()
	if err != nil {
		t.Fatalf("canonical content json: %v", err)
	}
	return encoded
}

// ---------------------------------------------------------------------------
// Ensure / determinism
// ---------------------------------------------------------------------------

// TestReportEnsurePublishesDeterministicRevisionOne pins the §5.5/§6.5 first
// generation: revision 1, published, deterministic snapshot/fingerprint/Markdown
// and a file whose sha256/size match the row.
func TestReportEnsurePublishesDeterministicRevisionOne(t *testing.T) {
	setup := newReportTestSetup(t)
	report, created := setup.ensure(t)
	if !created {
		t.Fatal("first Ensure must create the report")
	}
	if report.Revision != 1 {
		t.Fatalf("revision = %d, want 1", report.Revision)
	}
	if report.Status != domain.ReportStatusPublished {
		t.Fatalf("status = %q, want published", report.Status)
	}
	if report.GeneratedBy == nil || *report.GeneratedBy != 7 {
		t.Fatalf("generatedBy = %v, want 7", report.GeneratedBy)
	}
	if report.PublishedAt == nil {
		t.Fatal("publishedAt must be set")
	}
	if report.MarkdownPath == "" {
		t.Fatal("markdown path must be set")
	}
	if len(report.InputFingerprint) != 64 {
		t.Fatalf("inputFingerprint = %q, want 64 hex chars", report.InputFingerprint)
	}
	markdown := setup.files.files[report.MarkdownPath]
	if len(markdown) == 0 {
		t.Fatalf("markdown file %q was not written", report.MarkdownPath)
	}
	if got := reportTestSHA(markdown); got != report.MarkdownSHA256 {
		t.Fatalf("markdown sha256 = %q, row has %q", got, report.MarkdownSHA256)
	}
	if int64(len(markdown)) != report.MarkdownSize {
		t.Fatalf("markdown size = %d, row has %d", len(markdown), report.MarkdownSize)
	}

	content := report.Content
	wantCases := []string{"11", "12", "13"}
	for index, want := range wantCases {
		if content.Cases[index].CaseNodeID != want {
			t.Fatalf("cases[%d].caseNodeId = %q, want %q", index, content.Cases[index].CaseNodeID, want)
		}
	}
	wantIssues := []string{"42", "43", "41"}
	for index, want := range wantIssues {
		if content.Issues[index].IssueID != want {
			t.Fatalf("issues[%d].issueId = %q, want %q", index, content.Issues[index].IssueID, want)
		}
	}
	if got, want := content.Requirements[0].RequirementID, "31"; got != want {
		t.Fatalf("requirements[0].requirementId = %q, want %q", got, want)
	}
	if got := content.Summary; got.CaseCount != 3 || got.Passed != 1 || got.Failed != 1 ||
		got.Blocked != 1 || got.FlakyCount != 1 {
		t.Fatalf("summary = %+v, want 3 cases / 1 passed / 1 failed / 1 blocked / 1 flaky", got)
	}
	if got := content.Requirements[0]; got.CaseCount != 2 || got.Passed != 0 || got.Failed != 1 {
		t.Fatalf("requirement 31 coverage = %+v, want 2 cases / 0 passed / 1 failed", got)
	}
	if got := content.Appendix; got.Environment != "staging" || got.System != "payment" ||
		got.GeneratedAt != "2026-10-28T10:30:00Z" {
		t.Fatalf("appendix = %+v", got)
	}

	body := string(markdown)
	for _, needle := range []string{
		"# 结算系统 — Batch 2 报告（incremental，revision 1）",
		"## 1. 执行摘要",
		"## 2. 需求覆盖",
		"## 3. 用例结果",
		"## 4. 问题",
		"## 5. 风险与结论",
		"## 6. 附录",
		"## 7. 覆盖关系图",
		"```mermaid",
		"graph LR",
		"- 报告指纹：" + report.InputFingerprint,
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("markdown missing %q:\n%s", needle, body)
		}
	}

	contentJSON := reportTestContentJSON(t, report)
	lowerJSON := strings.ToLower(string(contentJSON))
	lowerMarkdown := strings.ToLower(body)
	for _, needle := range []string{
		"password", "storagestate", "authorization", "cookie", "llm_prompt",
		"evidencebody", "sqlresult", "connectionstring", "markdownpath",
	} {
		if strings.Contains(lowerJSON, needle) {
			t.Fatalf("content JSON leaked sensitive key %q: %s", needle, contentJSON)
		}
		if strings.Contains(lowerMarkdown, needle) {
			t.Fatalf("markdown leaked sensitive key %q", needle)
		}
	}

	// A second, independent generation of the same input must produce identical
	// content bytes, fingerprint and Markdown.
	other := newReportTestSetup(t)
	otherReport, otherCreated := other.ensure(t)
	if !otherCreated {
		t.Fatal("second setup must create the report")
	}
	if !bytes.Equal(contentJSON, reportTestContentJSON(t, otherReport)) {
		t.Fatal("content bytes differ for identical inputs")
	}
	if report.InputFingerprint != otherReport.InputFingerprint {
		t.Fatal("input fingerprint differs for identical inputs")
	}
	if !bytes.Equal(markdown, other.files.files[otherReport.MarkdownPath]) {
		t.Fatal("markdown bytes differ for identical inputs")
	}
	if got := setup.events.countType(ReportEventPublished); got != 1 {
		t.Fatalf("report.published events = %d, want 1", got)
	}
	if got := setup.audit.actions(); len(got) != 1 || got[0] != ReportActionPublished {
		t.Fatalf("audit actions = %v, want [%s]", got, ReportActionPublished)
	}
}

// TestReportEnsureReturnsExistingReport pins that Ensure only creates revision 1
// once and returns the existing report afterwards.
func TestReportEnsureReturnsExistingReport(t *testing.T) {
	setup := newReportTestSetup(t)
	first, created := setup.ensure(t)
	if !created {
		t.Fatal("first Ensure must create")
	}
	second, created := setup.ensure(t)
	if created {
		t.Fatal("second Ensure must not create a report")
	}
	if second.ID != first.ID || second.Revision != first.Revision || second.Status != domain.ReportStatusPublished {
		t.Fatalf("second Ensure returned %+v, want report %d revision 1", second, first.ID)
	}
	if len(setup.reports.reports) != 1 {
		t.Fatalf("stored reports = %d, want 1", len(setup.reports.reports))
	}
	if got := setup.events.countType(ReportEventPublished); got != 1 {
		t.Fatalf("report.published events = %d, want 1", got)
	}
}

// TestReportEnsureRejectsForeignBatch pins the version-scoping guard: a batch
// that belongs to another version must never leak into a report snapshot.
func TestReportEnsureRejectsForeignBatch(t *testing.T) {
	setup := newReportTestSetup(t)
	setup.execution.batch.VersionID = 99
	_, _, err := setup.service.Ensure(context.Background(), 10, 20, 7)
	if err == nil {
		t.Fatal("a batch of another version must be rejected")
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Kind != domain.KindInvalid {
		t.Fatalf("error = %v, want a domain invalid error", err)
	}
	if len(setup.reports.reports) != 0 {
		t.Fatal("no report may be created for a foreign batch")
	}
}

// TestReportEnsureCleansRowWhenMarkdownWriteFails pins the spec §6.5 retry
// contract: the row is removed (and no event/audit is emitted) when the
// Markdown file cannot be written, so the next tick starts clean.
func TestReportEnsureCleansRowWhenMarkdownWriteFails(t *testing.T) {
	setup := newReportTestSetup(t)
	setup.files.writeErr = errors.New("disk full")
	if _, _, err := setup.service.Ensure(context.Background(), 10, 20, 7); err == nil {
		t.Fatal("Ensure must report the file write failure")
	}
	if len(setup.reports.reports) != 0 {
		t.Fatalf("stored reports = %d, want 0 after cleanup", len(setup.reports.reports))
	}
	if len(setup.reports.deleted) != 1 {
		t.Fatalf("deleted rows = %v, want one compensating delete", setup.reports.deleted)
	}
	if got := setup.events.countType(ReportEventPublished); got != 0 {
		t.Fatalf("report.published events = %d, want 0", got)
	}
	if got := len(setup.audit.actions()); got != 0 {
		t.Fatalf("audit actions = %d, want 0", got)
	}
}

// TestReportStaleMarksOnceAndEnsureRegenerates pins the stale rule of spec
// §5.5/§6.5: a changed fingerprint flips published→stale exactly once, and the
// next Ensure appends revision 2 while keeping revision 1.
func TestReportStaleMarksOnceAndEnsureRegenerates(t *testing.T) {
	setup := newReportTestSetup(t)
	first, _ := setup.ensure(t)

	// A rerun changed the latest case run: the fingerprint must change now.
	setup.execution.cases[0].Status = "passed"
	setup.execution.cases[0].UpdatedAt = setup.clock.now
	setup.execution.batch.PassedCount = 2
	setup.execution.batch.FailedCount = 0

	stale, err := setup.service.Get(context.Background(), 10, first.ID)
	if err != nil {
		t.Fatalf("get report: %v", err)
	}
	if stale.Status != domain.ReportStatusStale {
		t.Fatalf("status = %q, want stale", stale.Status)
	}
	if stale.StaleReason == "" {
		t.Fatal("stale reason must be set")
	}
	if got := setup.events.countType(ReportEventStale); got != 1 {
		t.Fatalf("report.stale events = %d, want 1", got)
	}
	if _, err := setup.service.Get(context.Background(), 10, first.ID); err != nil {
		t.Fatalf("get stale report again: %v", err)
	}
	if got := setup.events.countType(ReportEventStale); got != 1 {
		t.Fatalf("report.stale events after second read = %d, want 1", got)
	}

	regenerated, created := setup.ensure(t)
	if !created {
		t.Fatal("Ensure on a stale report must create a new revision")
	}
	if regenerated.Revision != 2 {
		t.Fatalf("revision = %d, want 2", regenerated.Revision)
	}
	if regenerated.ID == first.ID {
		t.Fatal("regenerated report reused the old id")
	}
	if got := setup.reports.reports[first.ID].Status; got != domain.ReportStatusStale {
		t.Fatalf("old revision status = %q, want stale", got)
	}
	if got := setup.events.countType(ReportEventPublished); got != 2 {
		t.Fatalf("report.published events = %d, want 2", got)
	}
}

// TestReportRegenerateCreatesNextRevision pins POST regenerate semantics:
// revision max+1, old revision retained, and a newer revision returned as-is.
func TestReportRegenerateCreatesNextRevision(t *testing.T) {
	setup := newReportTestSetup(t)
	first, _ := setup.ensure(t)

	regenerated, err := setup.service.Regenerate(context.Background(), 10, first.ID, 8)
	if err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	if regenerated.Revision != 2 || regenerated.ID == first.ID {
		t.Fatalf("regenerated = id %d revision %d, want a new id at revision 2", regenerated.ID, regenerated.Revision)
	}
	if _, ok := setup.reports.reports[first.ID]; !ok {
		t.Fatal("old revision must be retained")
	}
	if got := setup.reports.reports[regenerated.ID].MarkdownPath; got == setup.reports.reports[first.ID].MarkdownPath {
		t.Fatal("each revision must write its own Markdown file")
	}

	latest, err := setup.service.Regenerate(context.Background(), 10, first.ID, 9)
	if err != nil {
		t.Fatalf("regenerate old id: %v", err)
	}
	if latest.ID != regenerated.ID || latest.Revision != 2 {
		t.Fatalf("regenerating an old revision returned id %d revision %d, want latest report %d", latest.ID, latest.Revision, regenerated.ID)
	}
}

// TestReportMarkdownDegradedWarning pins the degraded warning of §6.3.
func TestReportMarkdownDegradedWarning(t *testing.T) {
	setup := newReportTestSetup(t)
	setup.execution.batch.Degraded = true
	report, _ := setup.ensure(t)
	body := string(setup.files.files[report.MarkdownPath])
	if !strings.Contains(body, "降级") {
		t.Fatalf("markdown missing the degraded warning:\n%s", body)
	}
	if !report.Content.Degraded {
		t.Fatal("content degraded flag must be true")
	}
}

// ---------------------------------------------------------------------------
// diff / export / publish / list
// ---------------------------------------------------------------------------

func reportTestStoredReport(id, versionID, batchID int64, status string, content domain.ReportContent, createdAt time.Time) domain.Report {
	report := domain.Report{
		ID:               id,
		VersionID:        versionID,
		BatchID:          batchID,
		Revision:         1,
		Status:           status,
		Title:            "报告",
		Content:          content,
		InputFingerprint: strings.Repeat("e", 64),
		CreatedAt:        createdAt,
		UpdatedAt:        createdAt,
	}
	if status == domain.ReportStatusPublished {
		report.PublishedAt = &createdAt
	}
	return report
}

// TestReportDiffUsesExplicitAndDefaultBase pins the diff wiring: explicit base,
// default previous report, batch numbers and the classified counts.
func TestReportDiffUsesExplicitAndDefaultBase(t *testing.T) {
	setup := newReportTestSetup(t)
	baseContent := domain.ReportContent{
		Schema: domain.ReportSchemaV1,
		Batch:  domain.ReportBatch{ID: "19", BatchNo: 1, Kind: "baseline", Status: "done"},
		Cases: []domain.ReportCase{
			{CaseRunID: "201", CaseNodeID: "101", Title: "A", Source: "baseline", Status: "passed", Attempt: 1},
			{CaseRunID: "202", CaseNodeID: "102", Title: "B", Source: "baseline", Status: "failed", Attempt: 1},
			{CaseRunID: "203", CaseNodeID: "103", Title: "C", Source: "baseline", Status: "passed", Attempt: 1},
		},
		Issues: []domain.ReportIssue{
			{IssueID: "201", Title: "旧问题", Severity: "major", Status: "open"},
		},
	}
	targetContent := domain.ReportContent{
		Schema: domain.ReportSchemaV1,
		Batch:  domain.ReportBatch{ID: "20", BatchNo: 2, Kind: "incremental", Status: "done"},
		Cases: []domain.ReportCase{
			{CaseRunID: "301", CaseNodeID: "101", Title: "A", Source: "baseline", Status: "passed", Attempt: 1},
			{CaseRunID: "302", CaseNodeID: "102", Title: "B", Source: "baseline", Status: "passed", Attempt: 1},
			{CaseRunID: "303", CaseNodeID: "103", Title: "C", Source: "baseline", Status: "failed", Attempt: 1},
			{CaseRunID: "304", CaseNodeID: "104", Title: "D", Source: "ai_supplement", Status: "passed", Attempt: 1},
		},
		Issues: []domain.ReportIssue{
			{IssueID: "202", Title: "新问题", Severity: "blocker", Status: "open"},
		},
	}
	baseTime := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	targetTime := baseTime.Add(time.Hour)
	base := reportTestStoredReport(500, 10, 19, domain.ReportStatusPublished, baseContent, baseTime)
	target := reportTestStoredReport(501, 10, 20, domain.ReportStatusPublished, targetContent, targetTime)
	setup.reports.reports[base.ID] = base
	setup.reports.reports[target.ID] = target

	explicit, err := setup.service.Diff(context.Background(), 10, target.ID, &base.ID)
	if err != nil {
		t.Fatalf("diff with explicit base: %v", err)
	}
	if explicit.BaseReportID != base.ID || explicit.TargetReportID != target.ID {
		t.Fatalf("diff ids = (%d,%d), want (%d,%d)", explicit.BaseReportID, explicit.TargetReportID, base.ID, target.ID)
	}
	if explicit.BaseBatchNo != 1 || explicit.TargetBatchNo != 2 {
		t.Fatalf("batch numbers = (%d,%d), want (1,2)", explicit.BaseBatchNo, explicit.TargetBatchNo)
	}
	want := domain.ReportDiffSummary{
		AddedIssues: 1, FixedIssues: 1, Regressed: 1, Recovered: 1, NewCases: 1,
	}
	if explicit.Summary != want {
		t.Fatalf("summary = %+v, want %+v", explicit.Summary, want)
	}
	if len(explicit.RegressedCases) != 1 || explicit.RegressedCases[0].CaseNodeID != 103 {
		t.Fatalf("regressed cases = %+v", explicit.RegressedCases)
	}
	if len(explicit.RecoveredCases) != 1 || explicit.RecoveredCases[0].CaseNodeID != 102 {
		t.Fatalf("recovered cases = %+v", explicit.RecoveredCases)
	}

	byDefault, err := setup.service.Diff(context.Background(), 10, target.ID, nil)
	if err != nil {
		t.Fatalf("diff with default base: %v", err)
	}
	if byDefault.BaseReportID != base.ID {
		t.Fatalf("default base report = %d, want %d", byDefault.BaseReportID, base.ID)
	}
	if byDefault.Summary != explicit.Summary {
		t.Fatalf("default diff summary = %+v, want %+v", byDefault.Summary, explicit.Summary)
	}
}

// TestReportDiffDefaultBaseIsPreviousBatchLatest pins spec §6.5/§7.4: the
// default diff base is the latest report of the previous batch (batch_no-1),
// even when that report was regenerated after the target report was created.
func TestReportDiffDefaultBaseIsPreviousBatchLatest(t *testing.T) {
	setup := newReportTestSetup(t)
	baseContent := domain.ReportContent{
		Schema: domain.ReportSchemaV1,
		Batch:  domain.ReportBatch{ID: "19", BatchNo: 1, Kind: "baseline", Status: "done"},
		Cases: []domain.ReportCase{
			{CaseRunID: "201", CaseNodeID: "101", Title: "A", Source: "baseline", Status: "failed", Attempt: 1},
		},
	}
	targetContent := domain.ReportContent{
		Schema: domain.ReportSchemaV1,
		Batch:  domain.ReportBatch{ID: "20", BatchNo: 2, Kind: "incremental", Status: "done"},
		Cases: []domain.ReportCase{
			{CaseRunID: "301", CaseNodeID: "101", Title: "A", Source: "baseline", Status: "passed", Attempt: 1},
		},
	}
	created := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	target := reportTestStoredReport(501, 10, 20, domain.ReportStatusPublished, targetContent, created)
	// Revision 1 of the previous batch predates the target; revision 2, the
	// latest, was regenerated afterwards. The default base must be revision 2.
	oldBase := reportTestStoredReport(500, 10, 19, domain.ReportStatusPublished, baseContent, created.Add(-time.Hour))
	regeneratedBase := reportTestStoredReport(502, 10, 19, domain.ReportStatusPublished, baseContent, created.Add(2*time.Hour))
	regeneratedBase.Revision = 2
	setup.reports.reports[target.ID] = target
	setup.reports.reports[oldBase.ID] = oldBase
	setup.reports.reports[regeneratedBase.ID] = regeneratedBase

	diff, err := setup.service.Diff(context.Background(), 10, target.ID, nil)
	if err != nil {
		t.Fatalf("diff with default base: %v", err)
	}
	if diff.BaseReportID != regeneratedBase.ID {
		t.Fatalf("default base = report %d, want previous batch's latest report %d",
			diff.BaseReportID, regeneratedBase.ID)
	}
	if diff.BaseBatchNo != 1 || diff.TargetBatchNo != 2 {
		t.Fatalf("batch numbers = (%d,%d), want (1,2)", diff.BaseBatchNo, diff.TargetBatchNo)
	}
	if want := (domain.ReportDiffSummary{Recovered: 1}); diff.Summary != want {
		t.Fatalf("summary = %+v, want %+v", diff.Summary, want)
	}
	wantLookup := reportFakeBatchLookup{VersionID: 10, BatchID: 20}
	if len(setup.execution.previousCalls) != 1 || setup.execution.previousCalls[0] != wantLookup {
		t.Fatalf("PreviousBatchID calls = %+v, want [%+v]", setup.execution.previousCalls, wantLookup)
	}
}

// TestReportDiffWithoutPreviousReportIsClearNotFound pins the missing default
// base contract: no previous batch, or a previous batch without a report, is a
// clear 404 that names no target id. Another batch's report is never borrowed
// as the base.
func TestReportDiffWithoutPreviousReportIsClearNotFound(t *testing.T) {
	setup := newReportTestSetup(t)
	content := domain.ReportContent{
		Schema: domain.ReportSchemaV1,
		Batch:  domain.ReportBatch{ID: "20", BatchNo: 2, Kind: "incremental", Status: "done"},
	}
	created := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	target := reportTestStoredReport(501, 10, 20, domain.ReportStatusPublished, content, created)
	// Created before the target, but not the previous batch: it must not be used.
	older := reportTestStoredReport(500, 10, 18, domain.ReportStatusPublished, content, created.Add(-2*time.Hour))
	setup.reports.reports[target.ID] = target
	setup.reports.reports[older.ID] = older

	cases := []struct {
		name            string
		hasPrevious     bool
		previousBatchID int64
	}{
		{name: "no previous batch", hasPrevious: false},
		{name: "previous batch has no report", hasPrevious: true, previousBatchID: 19},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			setup.execution.hasPreviousBatch = testCase.hasPrevious
			setup.execution.previousBatchID = testCase.previousBatchID
			_, err := setup.service.Diff(context.Background(), 10, target.ID, nil)
			reportTestWantReason(t, err, domain.KindNotFound, domain.ReportReasonNotFound)
			if strings.Contains(err.Error(), fmt.Sprintf("%d", target.ID)) {
				t.Fatalf("error message mentions the target id: %v", err)
			}
		})
	}
}

// TestReportExportFailureHidesFilesystemPath pins that report_export_failed
// carries a fixed message: a cause that embeds an OS path stays in the log.
func TestReportExportFailureHidesFilesystemPath(t *testing.T) {
	setup := newReportTestSetup(t)
	report, _ := setup.ensure(t)
	setup.files.openErr = &fs.PathError{
		Op:   "open",
		Path: `C:\private\reports\secret.md`,
		Err:  fs.ErrPermission,
	}
	_, err := setup.service.Export(context.Background(), 10, report.ID)
	reportTestWantReason(t, err, "internal", domain.ReportReasonExportFailed)
	if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), `C:\`) {
		t.Fatalf("export error message leaks the filesystem path: %v", err)
	}
}

// TestReportExportVerifiesSHA pins the export contract: bytes are verified
// against sha256/size, and a missing or corrupted file is report_export_failed.
func TestReportExportVerifiesSHA(t *testing.T) {
	setup := newReportTestSetup(t)
	report, _ := setup.ensure(t)

	exported, err := setup.service.Export(context.Background(), 10, report.ID)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	stored := setup.files.files[report.MarkdownPath]
	if !bytes.Equal(exported.Content, stored) {
		t.Fatal("export content does not match the stored file")
	}
	if exported.SHA256 != report.MarkdownSHA256 || exported.Size != report.MarkdownSize {
		t.Fatalf("export sha/size = (%s,%d), row has (%s,%d)",
			exported.SHA256, exported.Size, report.MarkdownSHA256, report.MarkdownSize)
	}
	if exported.ReportID != report.ID || exported.VersionID != report.VersionID ||
		exported.BatchID != report.BatchID || exported.Revision != report.Revision {
		t.Fatalf("export metadata = %+v", exported)
	}
	if got := setup.audit.actions(); !reportTestContains(got, ReportActionExported) {
		t.Fatalf("audit actions = %v, want %s", got, ReportActionExported)
	}

	setup.files.corrupt(report.MarkdownPath)
	_, err = setup.service.Export(context.Background(), 10, report.ID)
	reportTestWantReason(t, err, "internal", domain.ReportReasonExportFailed)

	delete(setup.files.files, report.MarkdownPath)
	_, err = setup.service.Export(context.Background(), 10, report.ID)
	reportTestWantReason(t, err, "internal", domain.ReportReasonExportFailed)
}

// TestReportPublishDraft pins the manual publish transition of §7.4.
func TestReportPublishDraft(t *testing.T) {
	setup := newReportTestSetup(t)
	draft := reportTestStoredReport(600, 10, 20, domain.ReportStatusDraft,
		domain.ReportContent{Schema: domain.ReportSchemaV1}, setup.clock.now)
	setup.reports.reports[draft.ID] = draft

	published, err := setup.service.Publish(context.Background(), 10, draft.ID, 3)
	if err != nil {
		t.Fatalf("publish draft: %v", err)
	}
	if published.Status != domain.ReportStatusPublished || published.PublishedAt == nil {
		t.Fatalf("published report = %+v", published)
	}
	if _, err := setup.service.Publish(context.Background(), 10, draft.ID, 3); err == nil {
		t.Fatal("publishing an already published report must fail")
	} else {
		reportTestWantReason(t, err, domain.KindStateConflict, domain.ReportReasonNotPublishable)
	}
}

// TestReportListFiltersAndValidates pins list filtering, pagination and status
// validation.
func TestReportListFiltersAndValidates(t *testing.T) {
	setup := newReportTestSetup(t)
	base := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	for index, batchID := range []int64{20, 21} {
		report := reportTestStoredReport(int64(700+index), 10, batchID, domain.ReportStatusPublished,
			domain.ReportContent{Schema: domain.ReportSchemaV1}, base.Add(time.Duration(index)*time.Hour))
		setup.reports.reports[report.ID] = report
	}

	all, hasMore, err := setup.service.List(context.Background(), 10, ReportFilter{})
	if err != nil {
		t.Fatalf("list reports: %v", err)
	}
	if len(all) != 2 || hasMore {
		t.Fatalf("list = %d reports, hasMore=%v; want 2, false", len(all), hasMore)
	}
	batchID := int64(21)
	filtered, _, _, err := setup.service.ListPage(context.Background(), 10, ReportFilter{BatchID: &batchID})
	if err != nil {
		t.Fatalf("list filtered: %v", err)
	}
	if len(filtered) != 1 || filtered[0].BatchID != 21 {
		t.Fatalf("filtered list = %+v", filtered)
	}
	firstPage, cursor, hasMore, err := setup.service.ListPage(context.Background(), 10, ReportFilter{Limit: 1})
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if len(firstPage) != 1 || !hasMore || cursor == "" {
		t.Fatalf("first page = %d reports, cursor=%q, hasMore=%v", len(firstPage), cursor, hasMore)
	}
	secondPage, _, _, err := setup.service.ListPage(context.Background(), 10, ReportFilter{Limit: 1, Cursor: cursor})
	if err != nil {
		t.Fatalf("list second page: %v", err)
	}
	if len(secondPage) != 1 || secondPage[0].ID == firstPage[0].ID {
		t.Fatalf("second page = %+v", secondPage)
	}

	_, _, err = setup.service.List(context.Background(), 10, ReportFilter{Status: "exploded"})
	reportTestWantReason(t, err, domain.KindInvalid, domain.ReportReasonInvalidFilter)
}

// TestReportListRefreshesStale pins the list-read stale check of §5.5.
func TestReportListRefreshesStale(t *testing.T) {
	setup := newReportTestSetup(t)
	staleCandidate := reportTestStoredReport(800, 10, 20, domain.ReportStatusPublished,
		domain.ReportContent{Schema: domain.ReportSchemaV1}, setup.clock.now)
	staleCandidate.InputFingerprint = strings.Repeat("f", 64)
	setup.reports.reports[staleCandidate.ID] = staleCandidate

	listed, _, err := setup.service.List(context.Background(), 10, ReportFilter{})
	if err != nil {
		t.Fatalf("list reports: %v", err)
	}
	if len(listed) != 1 || listed[0].Status != domain.ReportStatusStale {
		t.Fatalf("listed = %+v, want one stale report", listed)
	}
	if got := setup.events.countType(ReportEventStale); got != 1 {
		t.Fatalf("report.stale events = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func reportTestContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func reportTestWantReason(t *testing.T, err error, kind, reason string) {
	t.Helper()
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("error = %v, want *domain.Error", err)
	}
	if domainErr.Kind != kind || domainErr.Reason != reason {
		t.Fatalf("error = %+v, want kind %q reason %q", domainErr, kind, reason)
	}
}
