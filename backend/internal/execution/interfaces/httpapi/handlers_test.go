package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/idempotency"
)

// ---------------------------------------------------------------------------
// Test harness
// ---------------------------------------------------------------------------

// executionAuth resolves the two bearer tokens the tests use.
type executionAuth map[string]identity.User

func (a executionAuth) Authenticate(_ context.Context, rawToken string) (identity.User, error) {
	user, ok := a[rawToken]
	if !ok {
		return identity.User{}, identity.ErrUnauthenticated
	}
	return user, nil
}

var executionTokens = executionAuth{
	"ezt_user":  {ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive},
	"ezt_admin": {ID: 1, Username: "admin", Role: identity.RoleAdmin, Status: identity.StatusActive},
}

// newExecutionServer mounts the handler groups behind the identity middleware.
func newExecutionServer(t *testing.T, deps HandlerDeps) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	NewHandlers(deps).RegisterRoutes(mux)
	return identityhttp.AuthMiddleware(executionTokens)(mux)
}

// doExecutionRequest sends one JSON request with an optional bearer token.
func doExecutionRequest(handler http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
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

// decodeErrorEnvelope returns the shared error envelope.
func decodeErrorEnvelope(t *testing.T, rec *httptest.ResponseRecorder) (int, string, string) {
	t.Helper()
	var envelope struct {
		Error struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error envelope: %v body=%s", err, rec.Body)
	}
	reason, _ := envelope.Error.Details["reason"].(string)
	return rec.Code, envelope.Error.Code, reason
}

// requireEnvelopeError asserts a status and details.reason.
func requireEnvelopeError(t *testing.T, rec *httptest.ResponseRecorder, status int, code, reason string) {
	t.Helper()
	gotStatus, gotCode, gotReason := decodeErrorEnvelope(t, rec)
	if gotStatus != status || gotCode != code || gotReason != reason {
		t.Fatalf("error=%d %q reason=%q body=%s want %d %q reason=%q",
			gotStatus, gotCode, gotReason, rec.Body, status, code, reason)
	}
}

// ---------------------------------------------------------------------------
// Batch service fake
// ---------------------------------------------------------------------------

type fakeBatchService struct {
	create func(ctx context.Context, versionID, actor int64, caseIDs []int64) (*domain.RunBatch, error)
	get    func(ctx context.Context, versionID, batchID int64) (*domain.RunBatch, error)
	list   func(ctx context.Context, versionID int64, beforeSeq int64, limit int) ([]domain.RunBatch, bool, error)
	pause  func(ctx context.Context, versionID, batchID int64, mode domain.PauseMode, actor int64) (*domain.RunBatch, error)
	resume func(ctx context.Context, versionID, batchID, actor int64) (*domain.RunBatch, error)
	cancel func(ctx context.Context, versionID, batchID, actor int64) (*domain.RunBatch, error)
	rerun  func(ctx context.Context, versionID, batchID, caseRunID, actor int64) (*domain.CaseRun, error)
}

func (f *fakeBatchService) Create(ctx context.Context, versionID, actor int64, caseIDs []int64) (*domain.RunBatch, error) {
	if f.create == nil {
		return nil, errors.New("create not stubbed")
	}
	return f.create(ctx, versionID, actor, caseIDs)
}

func (f *fakeBatchService) Get(ctx context.Context, versionID, batchID int64) (*domain.RunBatch, error) {
	if f.get == nil {
		return nil, executionapp.ErrBatchNotFound
	}
	return f.get(ctx, versionID, batchID)
}

func (f *fakeBatchService) List(ctx context.Context, versionID int64, beforeSeq int64, limit int) ([]domain.RunBatch, bool, error) {
	if f.list == nil {
		return nil, false, nil
	}
	return f.list(ctx, versionID, beforeSeq, limit)
}

func (f *fakeBatchService) Pause(ctx context.Context, versionID, batchID int64, mode domain.PauseMode, actor int64) (*domain.RunBatch, error) {
	if f.pause == nil {
		return nil, errors.New("pause not stubbed")
	}
	return f.pause(ctx, versionID, batchID, mode, actor)
}

func (f *fakeBatchService) Resume(ctx context.Context, versionID, batchID, actor int64) (*domain.RunBatch, error) {
	if f.resume == nil {
		return nil, errors.New("resume not stubbed")
	}
	return f.resume(ctx, versionID, batchID, actor)
}

func (f *fakeBatchService) Cancel(ctx context.Context, versionID, batchID, actor int64) (*domain.RunBatch, error) {
	if f.cancel == nil {
		return nil, errors.New("cancel not stubbed")
	}
	return f.cancel(ctx, versionID, batchID, actor)
}

func (f *fakeBatchService) Rerun(ctx context.Context, versionID, batchID, caseRunID, actor int64) (*domain.CaseRun, error) {
	if f.rerun == nil {
		return nil, errors.New("rerun not stubbed")
	}
	return f.rerun(ctx, versionID, batchID, caseRunID, actor)
}

// ---------------------------------------------------------------------------
// Case-run / evidence / report / session fakes
// ---------------------------------------------------------------------------

type fakeCaseRunReader struct {
	get  func(ctx context.Context, batchID, caseRunID int64) (domain.CaseRun, error)
	list func(ctx context.Context, batchID int64, status domain.CaseRunStatus, beforeID int64, limit int) ([]domain.CaseRun, bool, error)
}

func (f *fakeCaseRunReader) GetCaseRun(ctx context.Context, batchID, caseRunID int64) (domain.CaseRun, error) {
	if f.get == nil {
		return domain.CaseRun{}, executionapp.ErrCaseRunNotFound
	}
	return f.get(ctx, batchID, caseRunID)
}

func (f *fakeCaseRunReader) ListCaseRuns(ctx context.Context, batchID int64, status domain.CaseRunStatus, beforeID int64, limit int) ([]domain.CaseRun, bool, error) {
	if f.list == nil {
		return nil, false, nil
	}
	return f.list(ctx, batchID, status, beforeID, limit)
}

type fakeStepReader struct{ steps []domain.StepResult }

func (f fakeStepReader) ListByCaseRun(context.Context, int64) ([]domain.StepResult, error) {
	return f.steps, nil
}

type fakeAssertionReader struct{ assertions []domain.AssertionResult }

func (f fakeAssertionReader) ListByCaseRun(context.Context, int64) ([]domain.AssertionResult, error) {
	return f.assertions, nil
}

type fakeEvidenceReader struct{ evidence []domain.Evidence }

func (f fakeEvidenceReader) ListByCaseRun(context.Context, int64) ([]domain.Evidence, error) {
	return f.evidence, nil
}

type fakeEvidenceService struct {
	open func(ctx context.Context, versionID, evidenceID int64) (io.ReadCloser, domain.Evidence, error)
}

func (f *fakeEvidenceService) Open(ctx context.Context, versionID, evidenceID int64) (io.ReadCloser, domain.Evidence, error) {
	if f.open == nil {
		return nil, domain.Evidence{}, errors.New("open not stubbed")
	}
	return f.open(ctx, versionID, evidenceID)
}

type fakeReportService struct {
	report   func(ctx context.Context, versionID, batchID int64) (executionapp.ReportView, error)
	markdown func(ctx context.Context, versionID, batchID int64) ([]byte, string, error)
}

func (f *fakeReportService) BatchReport(ctx context.Context, versionID, batchID int64) (executionapp.ReportView, error) {
	if f.report == nil {
		return executionapp.ReportView{}, errors.New("report not stubbed")
	}
	return f.report(ctx, versionID, batchID)
}

func (f *fakeReportService) Markdown(ctx context.Context, versionID, batchID int64) ([]byte, string, error) {
	if f.markdown == nil {
		return nil, "", errors.New("markdown not stubbed")
	}
	return f.markdown(ctx, versionID, batchID)
}

type fakeSessionGate struct {
	takeover func(ctx context.Context, sessionID int64) (*executionapp.TakeoverRef, error)
}

func (f *fakeSessionGate) PendingTakeover(ctx context.Context, sessionID int64) (*executionapp.TakeoverRef, error) {
	if f.takeover == nil {
		return nil, nil
	}
	return f.takeover(ctx, sessionID)
}

type fakeAgentSessionLister struct {
	sessions []executionapp.AgentSessionRef
}

func (f fakeAgentSessionLister) ListSessions(context.Context, int64) ([]executionapp.AgentSessionRef, error) {
	return f.sessions, nil
}

type fakeBrowserHealth struct{ err error }

func (f fakeBrowserHealth) Health(context.Context) error { return f.err }

type fakeAuditSink struct {
	mu        sync.Mutex
	cancelled []int64
	paused    []bool
	created   []int64
}

func (f *fakeAuditSink) BatchCreated(_ context.Context, batch domain.RunBatch, _ int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, batch.ID)
	return nil
}

func (f *fakeAuditSink) BatchPaused(_ context.Context, batch domain.RunBatch, paused bool, _ int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paused = append(f.paused, paused)
	return nil
}

func (f *fakeAuditSink) BatchCancelled(_ context.Context, batchID, _ int64, _ int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, batchID)
	return nil
}

type fakeSessionInfoReader struct {
	session *CaseRunSession
	err     error
}

func (f fakeSessionInfoReader) CaseRunSession(context.Context, int64) (CaseRunSession, error) {
	if f.err != nil {
		return CaseRunSession{}, f.err
	}
	if f.session == nil {
		return CaseRunSession{}, executionapp.ErrCaseRunNotFound
	}
	return *f.session, nil
}

type fakeEvidenceRefReader struct {
	byKind map[string]map[int64][]int64
	err    error
}

func (f fakeEvidenceRefReader) ListByRef(_ context.Context, refKind string, _ []int64) (map[int64][]int64, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byKind[refKind], nil
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

func sampleBatch() domain.RunBatch {
	return domain.RunBatch{
		ID: 1842, VersionID: 7, BatchNo: 1, Status: domain.BatchStatusRunning,
		PauseMode: domain.PauseModeGraceful, BaselineHash: strings.Repeat("a", 64),
		CaseCount: 2, PassedCount: 1, CreatedBy: 2,
		StartedAt: timePtr(time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC)),
		CreatedAt: time.Date(2026, 10, 28, 7, 59, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC),
	}
}

func sampleCaseRun() domain.CaseRun {
	return domain.CaseRun{
		ID: 1843, BatchID: 1842, VersionID: 7, CaseNodeID: 9001,
		CaseRevision: 3, Attempt: 1, Status: domain.CaseRunStatusFailed,
		FailureClass:  failureClassPtr(domain.FailureClassApplication),
		FailureReason: "assertion_failed", Summary: "login assertion failed",
		DurationMS: 12345, CleanupRequired: true,
		SessionID: int64Ptr(501), SessionSnapshotID: int64Ptr(601),
		AgentSessionID: int64Ptr(701), AgentRunID: int64Ptr(702),
		Snapshot: domain.CaseSnapshot{
			Node:  domain.CaseSnapshotNode{ID: 9001, Title: "登录用例", Priority: "P1"},
			Steps: []domain.CaseSnapshotStep{{Seq: 1, Action: "打开首页", Expected: "显示登录"}},
			Assertions: []domain.CaseSnapshotAssertion{{
				ID: 9101, StepSeq: 1, Kind: "ui", Target: "登录按钮", Expected: "可见",
			}},
		},
		StartedAt:  timePtr(time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC)),
		FinishedAt: timePtr(time.Date(2026, 10, 28, 8, 0, 12, 0, time.UTC)),
		CreatedAt:  time.Date(2026, 10, 28, 7, 59, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 10, 28, 8, 0, 12, 0, time.UTC),
	}
}

func sampleEvidence() domain.Evidence {
	return domain.Evidence{
		ID: 7001, BatchID: 1842, CaseRunID: int64Ptr(1843),
		Kind: "screenshot", Mime: "image/png", SizeBytes: 5,
		SHA256: strings.Repeat("b", 64), CreatedAt: time.Date(2026, 10, 28, 8, 0, 1, 0, time.UTC),
	}
}

func sampleReport() executionapp.ReportView {
	return executionapp.ReportView{
		Batch: executionapp.ReportBatchView{
			ID: 1842, BatchNo: 1, Status: domain.BatchStatusDone,
			BaselineHash: strings.Repeat("a", 64), CodeBaselineCommit: "abc",
		},
		Summary: executionapp.ReportSummary{
			CaseCount: 1, ByStatus: map[string]int{"failed": 1},
			ByFailureClass: map[string]int{"application": 1}, FlakyCaseIDs: []int64{9001},
			DurationMS: 12345, EvidenceCount: 1,
		},
		Cases: []executionapp.ReportCase{{
			CaseRunID: 1843, CaseNodeID: 9001, Title: "登录用例",
			Status: domain.CaseRunStatusFailed, FailureClass: "application",
			Steps: 1, Assertions: 1, Evidence: 1, DurationMS: 12345,
		}},
		Environment: map[string]string{"system": "shop", "environment": "dev", "base_url": "http://127.0.0.1:13335"},
		GeneratedAt: time.Date(2026, 10, 28, 8, 1, 0, 0, time.UTC),
	}
}

func timePtr(value time.Time) *time.Time { return &value }
func int64Ptr(value int64) *int64        { return &value }
func failureClassPtr(value domain.FailureClass) *domain.FailureClass {
	return &value
}

// happyDeps is a fully wired dependency set with stubs that answer every route.
func happyDeps() HandlerDeps {
	batch := sampleBatch()
	run := sampleCaseRun()
	return HandlerDeps{
		Batches: &fakeBatchService{
			create: func(_ context.Context, versionID, _ int64, _ []int64) (*domain.RunBatch, error) {
				created := batch
				created.VersionID = versionID
				return &created, nil
			},
			get: func(_ context.Context, versionID, batchID int64) (*domain.RunBatch, error) {
				if versionID != batch.VersionID || batchID != batch.ID {
					return nil, executionapp.ErrBatchNotFound
				}
				return &batch, nil
			},
			list: func(_ context.Context, _ int64, _ int64, _ int) ([]domain.RunBatch, bool, error) {
				return []domain.RunBatch{batch}, false, nil
			},
			pause: func(_ context.Context, _, _ int64, mode domain.PauseMode, _ int64) (*domain.RunBatch, error) {
				paused := batch
				paused.Paused = true
				paused.PauseMode = mode
				return &paused, nil
			},
			resume: func(_ context.Context, _, _, _ int64) (*domain.RunBatch, error) { return &batch, nil },
			cancel: func(_ context.Context, _, _, _ int64) (*domain.RunBatch, error) {
				cancelled := batch
				cancelled.Status = domain.BatchStatusCancelled
				return &cancelled, nil
			},
			rerun: func(_ context.Context, _, _, _, _ int64) (*domain.CaseRun, error) {
				rerun := run
				rerun.ID = 1844
				rerun.Attempt = 2
				rerun.Status = domain.CaseRunStatusPending
				return &rerun, nil
			},
		},
		CaseRuns: &fakeCaseRunReader{
			get: func(_ context.Context, batchID, caseRunID int64) (domain.CaseRun, error) {
				if batchID != run.BatchID || caseRunID != run.ID {
					return domain.CaseRun{}, executionapp.ErrCaseRunNotFound
				}
				return run, nil
			},
			list: func(_ context.Context, _ int64, _ domain.CaseRunStatus, _ int64, _ int) ([]domain.CaseRun, bool, error) {
				return []domain.CaseRun{run}, false, nil
			},
		},
		Steps:      fakeStepReader{steps: []domain.StepResult{{ID: 8001, CaseRunID: 1843, Seq: 1, Action: "打开首页", Expected: "显示登录", Actual: "显示登录", Status: domain.StepStatusPassed, EvidenceCount: 1}}},
		Assertions: fakeAssertionReader{assertions: []domain.AssertionResult{{ID: 8101, CaseRunID: 1843, AssertionID: 9101, Kind: "ui", Target: "登录按钮", Expected: "可见", Actual: "可见", Verdict: "failed"}}},
		Evidence:   fakeEvidenceReader{evidence: []domain.Evidence{sampleEvidence()}},
		EvidenceService: &fakeEvidenceService{
			open: func(_ context.Context, versionID, evidenceID int64) (io.ReadCloser, domain.Evidence, error) {
				if versionID != 7 || evidenceID != 7001 {
					return nil, domain.Evidence{}, domain.NewReasonError("not_found", "not_found", "evidence not found", false, nil)
				}
				return io.NopCloser(strings.NewReader("PNGDATA")), sampleEvidence(), nil
			},
		},
		EvidenceRefs: fakeEvidenceRefReader{byKind: map[string]map[int64][]int64{
			"step_result":      {8001: {7001}},
			"assertion_result": {8101: {7001}},
		}},
		Reports: &fakeReportService{
			report: func(context.Context, int64, int64) (executionapp.ReportView, error) {
				return sampleReport(), nil
			},
			markdown: func(context.Context, int64, int64) ([]byte, string, error) {
				return []byte("# report\n"), strings.Repeat("c", 64), nil
			},
		},
		Sessions: &fakeSessionGate{takeover: func(context.Context, int64) (*executionapp.TakeoverRef, error) {
			return &executionapp.TakeoverRef{ID: 9801, SessionID: 501, Reason: "mfa", Instructions: json.RawMessage(`{"hint":"scan"}`)}, nil
		}},
		AgentSessions: fakeAgentSessionLister{sessions: []executionapp.AgentSessionRef{{SessionID: 701, Title: "用例 9001：登录用例", Status: "running", CreatedAt: time.Unix(0, 0).UTC(), UpdatedAt: time.Unix(1, 0).UTC()}}},
		Executor:      fakeBrowserHealth{},
		Audit:         &fakeAuditSink{},
		SessionInfo:   fakeSessionInfoReader{session: &CaseRunSession{ID: 501, Account: "qa", State: "valid"}},
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestBatchRoutesRequireAuthAndUseStringIDs(t *testing.T) {
	handler := newExecutionServer(t, happyDeps())

	rec := doExecutionRequest(handler, http.MethodGet, "/api/versions/7/batches", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous=%d body=%s want 401", rec.Code, rec.Body)
	}

	rec = doExecutionRequest(handler, http.MethodGet, "/api/versions/7/batches", "ezt_user", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list=%d body=%s want 200", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"id":"1842"`) {
		t.Fatalf("body=%s want the decimal string batch id", rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"version_id":"7"`) {
		t.Fatalf("body=%s want the decimal string version id", rec.Body)
	}
}

func TestCreateBatchReturns503WhenExecutorIsUnhealthy(t *testing.T) {
	deps := happyDeps()
	createCalls := 0
	deps.Batches.(*fakeBatchService).create = func(context.Context, int64, int64, []int64) (*domain.RunBatch, error) {
		createCalls++
		return nil, errors.New("must not be called")
	}
	deps.Executor = fakeBrowserHealth{err: errors.New("connection refused")}
	handler := newExecutionServer(t, deps)

	rec := doExecutionRequest(handler, http.MethodPost, "/api/versions/7/batches", "ezt_user", `{}`)
	requireEnvelopeError(t, rec, http.StatusServiceUnavailable, "unavailable", "executor_unavailable")
	if createCalls != 0 {
		t.Fatalf("create ran %d times despite an unhealthy executor", createCalls)
	}
}

func TestCreateBatchReturns409Reason(t *testing.T) {
	deps := happyDeps()
	deps.Batches.(*fakeBatchService).create = func(context.Context, int64, int64, []int64) (*domain.RunBatch, error) {
		return nil, domain.NewReasonError("version_not_executable", "version_not_executable", "version is designing", false, nil)
	}
	handler := newExecutionServer(t, deps)

	rec := doExecutionRequest(handler, http.MethodPost, "/api/versions/7/batches", "ezt_user", `{}`)
	requireEnvelopeError(t, rec, http.StatusConflict, "conflict", "version_not_executable")
}

func TestCreateBatchRejectsMalformedCaseIDs(t *testing.T) {
	handler := newExecutionServer(t, happyDeps())
	rec := doExecutionRequest(handler, http.MethodPost, "/api/versions/7/batches", "ezt_user", `{"caseIds":["abc"]}`)
	requireEnvelopeError(t, rec, http.StatusUnprocessableEntity, "invalid_argument", "invalid_case_ids")
}

func TestCreateBatchParsesStringCaseIDs(t *testing.T) {
	deps := happyDeps()
	var got []int64
	deps.Batches.(*fakeBatchService).create = func(_ context.Context, _, _ int64, caseIDs []int64) (*domain.RunBatch, error) {
		got = caseIDs
		created := sampleBatch()
		return &created, nil
	}
	handler := newExecutionServer(t, deps)

	rec := doExecutionRequest(handler, http.MethodPost, "/api/versions/7/batches", "ezt_user", `{"caseIds":["9001","9002"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create=%d body=%s want 201", rec.Code, rec.Body)
	}
	if len(got) != 2 || got[0] != 9001 || got[1] != 9002 {
		t.Fatalf("caseIDs=%v want [9001 9002]", got)
	}
}

func TestBatchMutationsRequireIdempotencyReplay(t *testing.T) {
	store := newMemoryIdempotencyStore()
	base := happyDeps()
	pauseCalls := 0
	base.Batches.(*fakeBatchService).pause = func(_ context.Context, _, _ int64, mode domain.PauseMode, _ int64) (*domain.RunBatch, error) {
		pauseCalls++
		paused := sampleBatch()
		paused.Paused = true
		paused.PauseMode = mode
		return &paused, nil
	}
	mux := http.NewServeMux()
	NewHandlers(base).RegisterRoutes(mux)
	handler := identityhttp.AuthMiddleware(executionTokens)(idempotency.Middleware(store, executionUserID)(mux))

	first := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/versions/7/batches/1842/pause", strings.NewReader(`{"mode":"immediate"}`))
	req.Header.Set("Authorization", "Bearer ezt_user")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "pause-key-1")
	handler.ServeHTTP(first, req)
	if first.Code != http.StatusOK {
		t.Fatalf("first pause=%d body=%s", first.Code, first.Body)
	}

	second := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/versions/7/batches/1842/pause", strings.NewReader(`{"mode":"immediate"}`))
	req.Header.Set("Authorization", "Bearer ezt_user")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "pause-key-1")
	handler.ServeHTTP(second, req)
	if second.Code != http.StatusOK {
		t.Fatalf("replay pause=%d body=%s", second.Code, second.Body)
	}
	if second.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay header=%q want true", second.Header().Get("Idempotent-Replayed"))
	}
	if pauseCalls != 1 {
		t.Fatalf("pause ran %d times, want one replay", pauseCalls)
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("replayed body=%s want %s", second.Body, first.Body)
	}
}

func TestCancelBatchAuditsCancellation(t *testing.T) {
	audit := &fakeAuditSink{}
	deps := happyDeps()
	deps.Audit = audit
	handler := newExecutionServer(t, deps)

	rec := doExecutionRequest(handler, http.MethodPost, "/api/versions/7/batches/1842/cancel", "ezt_user", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel=%d body=%s want 200", rec.Code, rec.Body)
	}
	if len(audit.cancelled) != 1 || audit.cancelled[0] != 1842 {
		t.Fatalf("audited cancellations=%v want [1842]", audit.cancelled)
	}
}

func TestCaseRunDetailUsesStringIDsAndEvidence(t *testing.T) {
	handler := newExecutionServer(t, happyDeps())
	rec := doExecutionRequest(handler, http.MethodGet,
		"/api/versions/7/batches/1842/case-runs/1843", "ezt_user", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("detail=%d body=%s want 200", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`"id":"1843"`, `"batch_id":"1842"`, `"case_node_id":"9001"`,
		`"session":{"id":"501"`, `"completed_seqs"`, `"cleanup_required":true`,
		`"evidence_ids":["7001"]`, `"pending_takeover":{"id":"9801"`,
		`"agent_run_id":"702"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body=%s misses %s", body, want)
		}
	}
	if strings.Contains(body, `"path"`) {
		t.Fatalf("body=%s must not expose the evidence path", body)
	}
}

func TestCaseRunDetailCrossVersionIs404(t *testing.T) {
	handler := newExecutionServer(t, happyDeps())
	rec := doExecutionRequest(handler, http.MethodGet,
		"/api/versions/7/batches/9999/case-runs/1843", "ezt_user", "")
	requireEnvelopeError(t, rec, http.StatusNotFound, "not_found", "not_found")
}

func TestCaseRunListFiltersAndPages(t *testing.T) {
	var gotStatus domain.CaseRunStatus
	var gotLimit int
	deps := happyDeps()
	deps.CaseRuns.(*fakeCaseRunReader).list = func(_ context.Context, _ int64, status domain.CaseRunStatus, _ int64, limit int) ([]domain.CaseRun, bool, error) {
		gotStatus, gotLimit = status, limit
		return []domain.CaseRun{sampleCaseRun()}, false, nil
	}
	handler := newExecutionServer(t, deps)

	rec := doExecutionRequest(handler, http.MethodGet,
		"/api/versions/7/batches/1842/case-runs?status=failed&limit=10", "ezt_user", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list=%d body=%s", rec.Code, rec.Body)
	}
	if gotStatus != domain.CaseRunStatusFailed || gotLimit != 10 {
		t.Fatalf("status=%q limit=%d want failed/10", gotStatus, gotLimit)
	}
	if !strings.Contains(rec.Body.String(), `"id":"1843"`) {
		t.Fatalf("body=%s want string case run id", rec.Body)
	}
}

func TestEvidenceDownloadSetsHeadersAndRequiresAuth(t *testing.T) {
	handler := newExecutionServer(t, happyDeps())

	rec := doExecutionRequest(handler, http.MethodGet, "/api/versions/7/evidence/7001", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous evidence=%d want 401", rec.Code)
	}

	rec = doExecutionRequest(handler, http.MethodGet, "/api/versions/7/evidence/7001", "ezt_user", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("evidence=%d body=%s want 200", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("Content-Type=%q want image/png", got)
	}
	if got := rec.Header().Get("X-Content-SHA256"); got != strings.Repeat("b", 64) {
		t.Fatalf("X-Content-SHA256=%q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); got != "attachment" {
		t.Fatalf("Content-Disposition=%q want attachment", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options=%q want nosniff", got)
	}
	if rec.Body.String() != "PNGDATA" {
		t.Fatalf("body=%q want the artifact bytes", rec.Body)
	}
}

func TestEvidenceCrossVersionIs404(t *testing.T) {
	handler := newExecutionServer(t, happyDeps())
	rec := doExecutionRequest(handler, http.MethodGet, "/api/versions/8/evidence/7001", "ezt_user", "")
	requireEnvelopeError(t, rec, http.StatusNotFound, "not_found", "not_found")
}

func TestEvidenceMetaNeverExposesPath(t *testing.T) {
	handler := newExecutionServer(t, happyDeps())
	rec := doExecutionRequest(handler, http.MethodGet, "/api/versions/7/evidence/7001/meta", "ezt_user", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("meta=%d body=%s want 200", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"id":"7001"`) || !strings.Contains(body, `"sha256":"`+strings.Repeat("b", 64)+`"`) {
		t.Fatalf("body=%s want the evidence metadata", body)
	}
	if strings.Contains(body, `"path"`) {
		t.Fatalf("body=%s must not expose the evidence path", body)
	}
}

func TestReportExportSetsHeaders(t *testing.T) {
	handler := newExecutionServer(t, happyDeps())
	rec := doExecutionRequest(handler, http.MethodGet,
		"/api/versions/7/batches/1842/report/export?format=markdown", "ezt_user", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("export=%d body=%s want 200", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/markdown") {
		t.Fatalf("Content-Type=%q want text/markdown", got)
	}
	disposition := rec.Header().Get("Content-Disposition")
	if !strings.Contains(disposition, "attachment") || !strings.Contains(disposition, "execution-report-1842.md") {
		t.Fatalf("Content-Disposition=%q", disposition)
	}
	if got := rec.Header().Get("X-Content-SHA256"); got != strings.Repeat("c", 64) {
		t.Fatalf("X-Content-SHA256=%q", got)
	}
	if rec.Body.String() != "# report\n" {
		t.Fatalf("body=%q", rec.Body)
	}
}

func TestReportExportRejectsUnknownFormat(t *testing.T) {
	handler := newExecutionServer(t, happyDeps())
	rec := doExecutionRequest(handler, http.MethodGet,
		"/api/versions/7/batches/1842/report/export?format=pdf", "ezt_user", "")
	requireEnvelopeError(t, rec, http.StatusUnprocessableEntity, "invalid_argument", "export_failed")
}

func TestReportJSONUsesStringIDs(t *testing.T) {
	handler := newExecutionServer(t, happyDeps())
	rec := doExecutionRequest(handler, http.MethodGet, "/api/versions/7/batches/1842/report", "ezt_user", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("report=%d body=%s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, want := range []string{`"case_run_id":"1843"`, `"case_node_id":"9001"`, `"flaky_case_ids":["9001"]`, `"failure_class":"application"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body=%s misses %s", body, want)
		}
	}
}

func TestExecutionSessionsUseStringIDs(t *testing.T) {
	handler := newExecutionServer(t, happyDeps())
	rec := doExecutionRequest(handler, http.MethodGet, "/api/versions/7/execution/sessions", "ezt_user", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("sessions=%d body=%s want 200", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"id":"701"`) || !strings.Contains(body, `"running":true`) {
		t.Fatalf("body=%s want the execution session projection", body)
	}
}

func TestExecutionHandlersNilSafe(t *testing.T) {
	for name, handlers := range map[string]*Handlers{
		"nil":           nil,
		"empty":         NewHandlers(HandlerDeps{}),
		"nil-deps":      {},
		"without-batch": NewHandlers(HandlerDeps{CaseRuns: &fakeCaseRunReader{}}),
	} {
		mux := http.NewServeMux()
		if handlers != nil {
			handlers.RegisterRoutes(mux)
		}
		rec := doExecutionRequest(identityhttp.AuthMiddleware(executionTokens)(mux),
			http.MethodGet, "/api/versions/7/batches", "ezt_user", "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: GET batches=%d want 404", name, rec.Code)
		}
	}
}

func TestInvalidPathIDIs422(t *testing.T) {
	handler := newExecutionServer(t, happyDeps())
	rec := doExecutionRequest(handler, http.MethodGet, "/api/versions/7/batches/not-a-number", "ezt_user", "")
	requireEnvelopeError(t, rec, http.StatusUnprocessableEntity, "invalid_argument", "invalid_id")
}

func TestInvalidStatusFilterIs422(t *testing.T) {
	handler := newExecutionServer(t, happyDeps())
	rec := doExecutionRequest(handler, http.MethodGet,
		"/api/versions/7/batches/1842/case-runs?status=bogus", "ezt_user", "")
	requireEnvelopeError(t, rec, http.StatusUnprocessableEntity, "invalid_argument", "invalid_status")
}

func TestBatchDetailNotFound(t *testing.T) {
	handler := newExecutionServer(t, happyDeps())
	rec := doExecutionRequest(handler, http.MethodGet, "/api/versions/7/batches/9999", "ezt_user", "")
	requireEnvelopeError(t, rec, http.StatusNotFound, "not_found", "not_found")
}

// ---------------------------------------------------------------------------
// Idempotency store
// ---------------------------------------------------------------------------

type memoryIdempotencyEntry struct {
	status int
	body   []byte
	hash   string
}

type memoryIdempotencyStore struct {
	mu      sync.Mutex
	entries map[string]memoryIdempotencyEntry
}

func newMemoryIdempotencyStore() *memoryIdempotencyStore {
	return &memoryIdempotencyStore{entries: make(map[string]memoryIdempotencyEntry)}
}

func memoryIdempotencyKey(userID int64, key, method, path string) string {
	return fmt.Sprintf("%d|%s|%s|%s", userID, key, method, path)
}

func (s *memoryIdempotencyStore) Get(_ context.Context, userID int64, key, method, path string) (*idempotency.StoredResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[memoryIdempotencyKey(userID, key, method, path)]
	if !ok {
		return nil, nil
	}
	return &idempotency.StoredResponse{Status: entry.status, Body: append([]byte(nil), entry.body...), RequestHash: entry.hash}, nil
}

func (s *memoryIdempotencyStore) Save(_ context.Context, userID int64, key, method, path, requestHash string, status int, body []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[memoryIdempotencyKey(userID, key, method, path)] = memoryIdempotencyEntry{
		status: status, body: append([]byte(nil), body...), hash: requestHash,
	}
	return nil
}

// executionUserID mirrors the server's idempotency user lookup.
func executionUserID(ctx context.Context) (int64, bool) {
	user, ok := identityhttp.UserFromContext(ctx)
	if !ok {
		return 0, false
	}
	return user.ID, true
}
