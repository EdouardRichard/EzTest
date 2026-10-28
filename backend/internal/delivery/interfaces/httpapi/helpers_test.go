package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// testAuth resolves the two bearer tokens used by every test.
type testAuth map[string]identity.User

func (a testAuth) Authenticate(_ context.Context, rawToken string) (identity.User, error) {
	user, ok := a[rawToken]
	if !ok {
		return identity.User{}, identity.ErrUnauthenticated
	}
	return user, nil
}

var testTokens = testAuth{
	"ezt_user":  {ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive},
	"ezt_admin": {ID: 1, Username: "admin", Role: identity.RoleAdmin, Status: identity.StatusActive},
}

// newTestServer mounts the delivery handler groups behind the identity
// middleware, mirroring the server's global authentication chain.
func newTestServer(t *testing.T, deps HandlerDeps) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	NewHandlers(deps).RegisterRoutes(mux)
	return identityhttp.AuthMiddleware(testTokens)(mux)
}

// doRequest sends one request with an optional bearer token and JSON body.
func doRequest(handler http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	var reader io.Reader = http.NoBody
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// requireStatus asserts the HTTP status code.
func requireStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status=%d body=%s want %d", rec.Code, rec.Body, want)
	}
}

// errorEnvelope is the shared error shape.
type errorEnvelope struct {
	Error struct {
		Code      string         `json:"code"`
		Message   string         `json:"message"`
		RequestID string         `json:"request_id"`
		Retryable bool           `json:"retryable"`
		Details   map[string]any `json:"details"`
	} `json:"error"`
}

// decodeError parses the shared error envelope.
func decodeError(t *testing.T, rec *httptest.ResponseRecorder) errorEnvelope {
	t.Helper()
	var envelope errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error envelope: %v body=%s", err, rec.Body)
	}
	return envelope
}

// requireError asserts the status, envelope code, details.reason and retryable.
func requireError(t *testing.T, rec *httptest.ResponseRecorder, status int, code, reason string, retryable bool) {
	t.Helper()
	envelope := decodeError(t, rec)
	gotReason, _ := envelope.Error.Details["reason"].(string)
	if rec.Code != status || envelope.Error.Code != code || gotReason != reason ||
		envelope.Error.Retryable != retryable {
		t.Fatalf("error=%d code=%q reason=%q retryable=%v body=%s want %d %q reason=%q retryable=%v",
			rec.Code, envelope.Error.Code, gotReason, envelope.Error.Retryable, rec.Body,
			status, code, reason, retryable)
	}
}

// requireErrorField asserts the stable details.field of a validation error.
func requireErrorField(t *testing.T, rec *httptest.ResponseRecorder, field string) {
	t.Helper()
	envelope := decodeError(t, rec)
	got, _ := envelope.Error.Details["field"].(string)
	if got != field {
		t.Fatalf("details.field=%q body=%s want %q", got, rec.Body, field)
	}
}

// decodeData parses a single-resource success envelope.
func decodeData(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode data envelope: %v body=%s", err, rec.Body)
	}
	if envelope.Data == nil {
		t.Fatalf("response has no data object: %s", rec.Body)
	}
	return envelope.Data
}

// decodePage parses a cursor page envelope.
func decodePage(t *testing.T, rec *httptest.ResponseRecorder) ([]map[string]any, string, bool) {
	t.Helper()
	var envelope struct {
		Data       []map[string]any `json:"data"`
		NextCursor string           `json:"next_cursor"`
		HasMore    bool             `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode page envelope: %v body=%s", err, rec.Body)
	}
	return envelope.Data, envelope.NextCursor, envelope.HasMore
}

// decodeJSONBody decodes any response body into dst.
func decodeJSONBody(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode body: %v body=%s", err, rec.Body)
	}
}

// requireStringField asserts m[key] is a decimal string and returns it.
func requireStringField(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	value, ok := m[key]
	if !ok {
		t.Fatalf("field %q is missing from %v", key, m)
	}
	text, ok := value.(string)
	if !ok {
		t.Fatalf("field %q is %T, want a decimal string (D29): %v", key, value, value)
	}
	if _, err := strconv.ParseInt(text, 10, 64); err != nil {
		t.Fatalf("field %q=%q is not a decimal id", key, text)
	}
	return text
}

// requireNoJSONField asserts key is absent from m.
func requireNoJSONField(t *testing.T, m map[string]any, key string) {
	t.Helper()
	if _, ok := m[key]; ok {
		t.Fatalf("field %q must not be exposed: %v", key, m)
	}
}

// sha256Hex returns the lowercase hexadecimal sha256 of data.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// Delivery service fakes
// ---------------------------------------------------------------------------

type fakeIssueService struct {
	createHuman func(ctx context.Context, versionID, actor int64, cmd deliveryapp.CreateIssueCommand) (domain.Issue, error)
	applyDrafts func(ctx context.Context, versionID, batchID, actor int64, drafts []domain.IssueDraft) ([]domain.Issue, error)
	confirm     func(ctx context.Context, versionID, issueID, actor int64, expectedRevision int, comment string) (domain.Issue, error)
	dismiss     func(ctx context.Context, versionID, issueID, actor int64, expectedRevision int, reason string) (domain.Issue, error)
	get         func(ctx context.Context, versionID, issueID int64) (domain.Issue, error)
	list        func(ctx context.Context, versionID int64, filter deliveryapp.IssueFilter) ([]domain.Issue, bool, error)
}

func (f *fakeIssueService) CreateHuman(ctx context.Context, versionID, actor int64, cmd deliveryapp.CreateIssueCommand) (domain.Issue, error) {
	if f.createHuman == nil {
		return domain.Issue{}, errors.New("createHuman not stubbed")
	}
	return f.createHuman(ctx, versionID, actor, cmd)
}

func (f *fakeIssueService) ApplyDrafts(ctx context.Context, versionID, batchID, actor int64, drafts []domain.IssueDraft) ([]domain.Issue, error) {
	if f.applyDrafts == nil {
		return nil, errors.New("applyDrafts not stubbed")
	}
	return f.applyDrafts(ctx, versionID, batchID, actor, drafts)
}

func (f *fakeIssueService) Confirm(ctx context.Context, versionID, issueID, actor int64, expectedRevision int, comment string) (domain.Issue, error) {
	if f.confirm == nil {
		return domain.Issue{}, errors.New("confirm not stubbed")
	}
	return f.confirm(ctx, versionID, issueID, actor, expectedRevision, comment)
}

func (f *fakeIssueService) Dismiss(ctx context.Context, versionID, issueID, actor int64, expectedRevision int, reason string) (domain.Issue, error) {
	if f.dismiss == nil {
		return domain.Issue{}, errors.New("dismiss not stubbed")
	}
	return f.dismiss(ctx, versionID, issueID, actor, expectedRevision, reason)
}

func (f *fakeIssueService) Get(ctx context.Context, versionID, issueID int64) (domain.Issue, error) {
	if f.get == nil {
		return domain.Issue{}, errors.New("get not stubbed")
	}
	return f.get(ctx, versionID, issueID)
}

func (f *fakeIssueService) List(ctx context.Context, versionID int64, filter deliveryapp.IssueFilter) ([]domain.Issue, bool, error) {
	if f.list == nil {
		return nil, false, errors.New("list not stubbed")
	}
	return f.list(ctx, versionID, filter)
}

type fakeIssueRefs struct {
	refs map[int64][]domain.IssueRef
	err  error
}

func (f *fakeIssueRefs) ListRefs(_ context.Context, _ int64, issueID int64) ([]domain.IssueRef, error) {
	if f == nil {
		return nil, nil
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.refs[issueID], nil
}

// fakeIssueCodeRefs is the optional codeintel anchor reader seam (T17 wires the
// production adapter). It records the addressed issue so the tests prove the
// lookup is version+issue scoped.
type fakeIssueCodeRefs struct {
	refs       map[int64][]IssueCodeRef
	err        error
	calls      int
	gotVersion int64
	gotIssue   int64
}

func (f *fakeIssueCodeRefs) ListIssueCodeRefs(_ context.Context, versionID, issueID int64) ([]IssueCodeRef, error) {
	if f == nil {
		return nil, nil
	}
	f.calls++
	f.gotVersion, f.gotIssue = versionID, issueID
	if f.err != nil {
		return nil, f.err
	}
	return f.refs[issueID], nil
}

type fakeReportService struct {
	ensure     func(ctx context.Context, versionID, batchID, actor int64) (domain.Report, bool, error)
	regenerate func(ctx context.Context, versionID, reportID, actor int64) (domain.Report, error)
	get        func(ctx context.Context, versionID, reportID int64) (domain.Report, error)
	listPage   func(ctx context.Context, versionID int64, filter deliveryapp.ReportFilter) ([]domain.Report, string, bool, error)
	diff       func(ctx context.Context, versionID, reportID int64, baseReportID *int64) (domain.ReportDiff, error)
	export     func(ctx context.Context, versionID, reportID int64) (domain.ReportExport, error)
	publish    func(ctx context.Context, versionID, reportID, actor int64) (domain.Report, error)
}

func (f *fakeReportService) Ensure(ctx context.Context, versionID, batchID, actor int64) (domain.Report, bool, error) {
	if f.ensure == nil {
		return domain.Report{}, false, errors.New("ensure not stubbed")
	}
	return f.ensure(ctx, versionID, batchID, actor)
}

func (f *fakeReportService) Regenerate(ctx context.Context, versionID, reportID, actor int64) (domain.Report, error) {
	if f.regenerate == nil {
		return domain.Report{}, errors.New("regenerate not stubbed")
	}
	return f.regenerate(ctx, versionID, reportID, actor)
}

func (f *fakeReportService) Get(ctx context.Context, versionID, reportID int64) (domain.Report, error) {
	if f.get == nil {
		return domain.Report{}, errors.New("get not stubbed")
	}
	return f.get(ctx, versionID, reportID)
}

func (f *fakeReportService) ListPage(ctx context.Context, versionID int64, filter deliveryapp.ReportFilter) ([]domain.Report, string, bool, error) {
	if f.listPage == nil {
		return nil, "", false, errors.New("listPage not stubbed")
	}
	return f.listPage(ctx, versionID, filter)
}

func (f *fakeReportService) Diff(ctx context.Context, versionID, reportID int64, baseReportID *int64) (domain.ReportDiff, error) {
	if f.diff == nil {
		return domain.ReportDiff{}, errors.New("diff not stubbed")
	}
	return f.diff(ctx, versionID, reportID, baseReportID)
}

func (f *fakeReportService) Export(ctx context.Context, versionID, reportID int64) (domain.ReportExport, error) {
	if f.export == nil {
		return domain.ReportExport{}, errors.New("export not stubbed")
	}
	return f.export(ctx, versionID, reportID)
}

func (f *fakeReportService) Publish(ctx context.Context, versionID, reportID, actor int64) (domain.Report, error) {
	if f.publish == nil {
		return domain.Report{}, errors.New("publish not stubbed")
	}
	return f.publish(ctx, versionID, reportID, actor)
}

type fakeAnalysisService struct {
	startFailure func(ctx context.Context, versionID, batchID, actor int64) (domain.AnalysisRun, error)
	get          func(ctx context.Context, versionID, batchID, analysisID int64) (domain.AnalysisRun, error)
	list         func(ctx context.Context, versionID, batchID int64) ([]domain.AnalysisRun, error)
}

func (f *fakeAnalysisService) StartFailure(ctx context.Context, versionID, batchID, actor int64) (domain.AnalysisRun, error) {
	if f.startFailure == nil {
		return domain.AnalysisRun{}, errors.New("startFailure not stubbed")
	}
	return f.startFailure(ctx, versionID, batchID, actor)
}

func (f *fakeAnalysisService) Get(ctx context.Context, versionID, batchID, analysisID int64) (domain.AnalysisRun, error) {
	if f.get == nil {
		return domain.AnalysisRun{}, errors.New("get not stubbed")
	}
	return f.get(ctx, versionID, batchID, analysisID)
}

func (f *fakeAnalysisService) List(ctx context.Context, versionID, batchID int64) ([]domain.AnalysisRun, error) {
	if f.list == nil {
		return nil, errors.New("list not stubbed")
	}
	return f.list(ctx, versionID, batchID)
}

type fakeBatchReader struct {
	batch *executiondomain.RunBatch
	err   error
	calls []string
}

func (f *fakeBatchReader) Get(_ context.Context, versionID, batchID int64) (*executiondomain.RunBatch, error) {
	f.calls = append(f.calls, strconv.FormatInt(versionID, 10)+"/"+strconv.FormatInt(batchID, 10))
	if f.err != nil {
		return nil, f.err
	}
	return f.batch, nil
}

type fakeGlobalIssues struct {
	rows      []GlobalIssueRow
	next      string
	hasMore   bool
	err       error
	gotFilter *GlobalIssueFilter
}

func (f *fakeGlobalIssues) ListGlobalIssues(_ context.Context, filter GlobalIssueFilter) ([]GlobalIssueRow, string, bool, error) {
	f.gotFilter = &filter
	if f.err != nil {
		return nil, "", false, f.err
	}
	return f.rows, f.next, f.hasMore, nil
}

type fakeGlobalReports struct {
	rows      []GlobalReportRow
	next      string
	hasMore   bool
	err       error
	gotFilter *GlobalReportFilter
}

func (f *fakeGlobalReports) ListGlobalReports(_ context.Context, filter GlobalReportFilter) ([]GlobalReportRow, string, bool, error) {
	f.gotFilter = &filter
	if f.err != nil {
		return nil, "", false, f.err
	}
	return f.rows, f.next, f.hasMore, nil
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const (
	testVersionID  int64 = 101
	testIssueID    int64 = 9001
	testReportID   int64 = 9101
	testBatchID    int64 = 7001
	testAnalysisID int64 = 9201
	testActorID    int64 = 2
)

func sampleIssue() domain.Issue {
	created := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	batchID := testBatchID
	caseRunID := int64(7101)
	caseNodeID := int64(7102)
	return domain.Issue{
		ID:              testIssueID,
		VersionID:       testVersionID,
		BatchID:         &batchID,
		CaseRunID:       &caseRunID,
		CaseNodeID:      &caseNodeID,
		Source:          domain.SourceAI,
		Category:        domain.CategoryDefect,
		Severity:        domain.SeverityMajor,
		Status:          domain.StatusOpen,
		Confidence:      domain.ConfidenceHigh,
		FailureClass:    domain.FailureClassApplication,
		DedupeKey:       strings.Repeat("a", 64),
		Title:           "checkout fails",
		Description:     "checkout request returns 500",
		ReproSteps:      "open cart, submit",
		Expected:        "order created",
		Actual:          "500",
		RecurrenceCount: 2,
		Revision:        3,
		CreatedBy:       1,
		UpdatedBy:       1,
		CreatedAt:       created,
		UpdatedAt:       created,
	}
}

func sampleIssueRefs(issueID int64) []domain.IssueRef {
	return []domain.IssueRef{
		{IssueID: issueID, RefKind: domain.RefKindCaseNode, RefID: "7102"},
		{IssueID: issueID, RefKind: domain.RefKindEvidence, RefID: "7201"},
	}
}

func sampleReportContent() domain.ReportContent {
	return domain.ReportContent{
		Schema: domain.ReportSchemaV1,
		Version: domain.ReportVersion{
			ID: strconv.FormatInt(testVersionID, 10), Name: "v1", Status: "executing",
		},
		Batch: domain.ReportBatch{
			ID: strconv.FormatInt(testBatchID, 10), BatchNo: 2, Kind: "baseline", Status: "done",
		},
		Summary:      domain.ReportSummary{CaseCount: 1, Passed: 1},
		Cases:        []domain.ReportCase{},
		Requirements: []domain.ReportRequirement{},
		Issues:       []domain.ReportIssue{},
		Risks:        []string{},
	}
}

func sampleReport() domain.Report {
	created := time.Date(2026, 10, 28, 11, 0, 0, 0, time.UTC)
	published := created
	return domain.Report{
		ID:               testReportID,
		VersionID:        testVersionID,
		BatchID:          testBatchID,
		Revision:         1,
		Status:           domain.ReportStatusPublished,
		Title:            "v1 batch 2 report",
		Content:          sampleReportContent(),
		InputFingerprint: strings.Repeat("b", 64),
		MarkdownPath:     "versions/101/reports/9101-r1.md",
		MarkdownSHA256:   strings.Repeat("c", 64),
		MarkdownSize:     128,
		PublishedAt:      &published,
		CreatedAt:        created,
		UpdatedAt:        created,
	}
}

func sampleAnalysisRun() domain.AnalysisRun {
	created := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	return domain.AnalysisRun{
		ID:            testAnalysisID,
		VersionID:     testVersionID,
		BatchID:       testBatchID,
		Kind:          domain.AnalysisKindFailure,
		Status:        domain.AnalysisDone,
		Model:         "fake-model",
		PromptVersion: domain.AnalysisPromptVersionFailure,
		Result:        json.RawMessage(`{"candidates":[]}`),
		Stats:         json.RawMessage(`{"truncated":false}`),
		Errors:        json.RawMessage(`{}`),
		CreatedBy:     1,
		CreatedAt:     created,
		UpdatedAt:     created,
	}
}

func terminalBatch(versionID, batchID int64) *executiondomain.RunBatch {
	return &executiondomain.RunBatch{
		ID: batchID, VersionID: versionID, BatchNo: 1, Status: executiondomain.BatchStatusDone,
	}
}

// notFoundBatch returns the execution not-found sentinel.
func notFoundBatch() error { return executionapp.ErrBatchNotFound }

// encodeCursor is the opaque cursor helper used by pagination assertions.
func encodeCursor(id int64) string { return platform.EncodeCursor(id) }
