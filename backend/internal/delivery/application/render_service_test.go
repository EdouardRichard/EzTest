package deliveryapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// ---------------------------------------------------------------------------
// Render job repository fake
// ---------------------------------------------------------------------------

type renderEnsureCall struct {
	RevisionID  int64
	ReportID    int64
	VersionID   int64
	DedupeKey   string
	MaxAttempts int
}

type renderRenderedCall struct {
	RevisionID int64
	Ref        ReportContentRef
	Attempts   int32
	At         time.Time
}

type renderFailedCall struct {
	RevisionID int64
	Message    string
	Attempts   int32
	At         time.Time
}

type fakeRenderJobs struct {
	mu sync.Mutex

	renderable []delivery.RenderableRevision
	revisions  map[int64]delivery.ReportRevision
	listErr    error

	ensureCalls []renderEnsureCall
	ensureErr   error

	disabledCalls []int64
	disabledErr   error

	runningCalls []int64
	runningErr   error

	renderedCalls []renderRenderedCall
	renderedErr   error

	failedCalls []renderFailedCall
	failedErr   error

	getErr error
}

func newFakeRenderJobs() *fakeRenderJobs {
	return &fakeRenderJobs{revisions: make(map[int64]delivery.ReportRevision)}
}

func (f *fakeRenderJobs) ListRenderableRevisions(_ context.Context, limit, maxAttempts int) ([]delivery.RenderableRevision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	// Mirror the repository scan: only pending/failed rows whose attempt budget
	// is not exhausted are renderable (fix 2: exhausted failed rows must not
	// consume the bounded page).
	result := make([]delivery.RenderableRevision, 0, len(f.renderable))
	for _, revision := range f.renderable {
		switch revision.PDFStatus {
		case delivery.PDFStatusPending:
		case delivery.PDFStatusFailed:
			if maxAttempts > 0 && int(revision.PDFAttempts) >= maxAttempts {
				continue
			}
		default:
			continue
		}
		result = append(result, revision)
	}
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (f *fakeRenderJobs) MarkPDFRunning(_ context.Context, revisionID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.runningErr != nil {
		return f.runningErr
	}
	f.runningCalls = append(f.runningCalls, revisionID)
	return nil
}

func (f *fakeRenderJobs) MarkPDFRendered(_ context.Context, revisionID int64, ref ReportContentRef, attempts int32, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.renderedErr != nil {
		return f.renderedErr
	}
	f.renderedCalls = append(f.renderedCalls, renderRenderedCall{RevisionID: revisionID, Ref: ref, Attempts: attempts, At: at})
	return nil
}

func (f *fakeRenderJobs) MarkPDFFailed(_ context.Context, revisionID int64, message string, attempts int32, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failedErr != nil {
		return f.failedErr
	}
	f.failedCalls = append(f.failedCalls, renderFailedCall{RevisionID: revisionID, Message: message, Attempts: attempts, At: at})
	return nil
}

func (f *fakeRenderJobs) MarkPDFDisabled(_ context.Context, revisionID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.disabledErr != nil {
		return f.disabledErr
	}
	f.disabledCalls = append(f.disabledCalls, revisionID)
	return nil
}

func (f *fakeRenderJobs) GetRevisionForRender(_ context.Context, revisionID int64) (delivery.ReportRevision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return delivery.ReportRevision{}, f.getErr
	}
	revision, ok := f.revisions[revisionID]
	if !ok {
		return delivery.ReportRevision{}, ErrReportNotFound
	}
	return revision, nil
}

func (f *fakeRenderJobs) EnsureRenderJob(_ context.Context, revisionID, reportID, versionID int64, dedupeKey string, maxAttempts int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ensureErr != nil {
		return f.ensureErr
	}
	f.ensureCalls = append(f.ensureCalls, renderEnsureCall{
		RevisionID: revisionID, ReportID: reportID, VersionID: versionID,
		DedupeKey: dedupeKey, MaxAttempts: maxAttempts,
	})
	return nil
}

// ---------------------------------------------------------------------------
// Render queue fake
// ---------------------------------------------------------------------------

type renderFailCall struct {
	JobID     int64
	Epoch     int64
	Err       string
	Retryable bool
}

type fakeRenderQueue struct {
	mu sync.Mutex

	job      RenderJob
	claimOK  bool
	claimErr error

	claimTypes []string
	claimLease time.Duration

	heartbeats      int
	heartbeatLeases []time.Duration
	completed       []int64
	completeErr     error
	failed          []renderFailCall
	failErr         error
}

func (q *fakeRenderQueue) ClaimForTypes(_ context.Context, claimant string, types []string, lease time.Duration) (RenderJob, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.claimTypes = append([]string(nil), types...)
	q.claimLease = lease
	if q.claimErr != nil {
		return RenderJob{}, false, q.claimErr
	}
	if !q.claimOK {
		return RenderJob{}, false, nil
	}
	return q.job, true, nil
}

func (q *fakeRenderQueue) Heartbeat(_ context.Context, _ int64, _ int64, lease time.Duration) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.heartbeats++
	q.heartbeatLeases = append(q.heartbeatLeases, lease)
	return nil
}

func (q *fakeRenderQueue) Complete(_ context.Context, jobID, _ int64) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.completeErr != nil {
		return q.completeErr
	}
	q.completed = append(q.completed, jobID)
	return nil
}

func (q *fakeRenderQueue) Fail(_ context.Context, jobID, epoch int64, cause error, retryable bool) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.failErr != nil {
		return q.failErr
	}
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	q.failed = append(q.failed, renderFailCall{JobID: jobID, Epoch: epoch, Err: message, Retryable: retryable})
	return nil
}

// ---------------------------------------------------------------------------
// Renderer client / settings fakes
// ---------------------------------------------------------------------------

type fakeRenderClient struct {
	mu       sync.Mutex
	pdf      []byte
	err      error
	calls    int
	lastHTML []byte
}

func (c *fakeRenderClient) RenderPDF(_ context.Context, html []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.lastHTML = append([]byte(nil), html...)
	if c.err != nil {
		return nil, c.err
	}
	return append([]byte(nil), c.pdf...), nil
}

type fakeRenderSettings struct {
	renderPDF    bool
	maxAttempts  int64
	leaseSeconds int64
	err          error
}

func (s fakeRenderSettings) Int(_ context.Context, key string) (int64, error) {
	if s.err != nil {
		return 0, s.err
	}
	switch key {
	case "report.render_max_attempts":
		if s.maxAttempts > 0 {
			return s.maxAttempts, nil
		}
		return 3, nil
	case "report.render_lease_seconds":
		if s.leaseSeconds > 0 {
			return s.leaseSeconds, nil
		}
		return 120, nil
	default:
		return 0, fmt.Errorf("unexpected setting %q", key)
	}
}

func (s fakeRenderSettings) Bool(_ context.Context, key string) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	if key != "report.render_pdf" {
		return false, fmt.Errorf("unexpected setting %q", key)
	}
	return s.renderPDF, nil
}

// renderJob builds a claimed job whose payload points at revisionID/reportID.
func renderJob(t *testing.T, id, epoch, versionID int64, revisionID, reportID int64, attempts, maxAttempts int) RenderJob {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"report_id":          fmt.Sprint(reportID),
		"report_revision_id": fmt.Sprint(revisionID),
	})
	if err != nil {
		t.Fatalf("marshal render job payload: %v", err)
	}
	return RenderJob{
		ID: id, Epoch: epoch, VersionID: versionID,
		Attempts: attempts, MaxAttempts: maxAttempts, Payload: payload,
	}
}

// ---------------------------------------------------------------------------
// Ensurer tests
// ---------------------------------------------------------------------------

func TestRenderTaskContractIsDeterministicOps(t *testing.T) {
	if RenderTaskType != "report.render_pdf" {
		t.Fatalf("RenderTaskType = %q", RenderTaskType)
	}
	// The generic agent worker claims the five product teams only; deterministic
	// worker tasks must live outside them so report.render_pdf is never stolen.
	if RenderTaskTeam != "ops" {
		t.Fatalf("RenderTaskTeam = %q, want ops", RenderTaskTeam)
	}
}

func TestRenderJobEnsurerEnsuresPendingRevisions(t *testing.T) {
	jobs := newFakeRenderJobs()
	jobs.renderable = []delivery.RenderableRevision{
		{RevisionID: 9001, ReportID: 100, VersionID: 7, Revision: 1, PDFStatus: delivery.PDFStatusPending},
		{RevisionID: 9002, ReportID: 100, VersionID: 7, Revision: 2, PDFStatus: delivery.PDFStatusFailed, PDFAttempts: 1},
	}
	ensurer := NewRenderJobEnsurer(jobs, fakeRenderSettings{renderPDF: true, maxAttempts: 3})

	if err := ensurer.ReconcileP7(context.Background()); err != nil {
		t.Fatalf("ReconcileP7: %v", err)
	}
	if len(jobs.ensureCalls) != 2 {
		t.Fatalf("ensure calls = %+v, want 2", jobs.ensureCalls)
	}
	first := jobs.ensureCalls[0]
	if first.RevisionID != 9001 || first.ReportID != 100 || first.VersionID != 7 ||
		first.DedupeKey != "report:100:rev:1" || first.MaxAttempts != 3 {
		t.Fatalf("first ensure = %+v", first)
	}
	if second := jobs.ensureCalls[1]; second.DedupeKey != "report:100:rev:2" {
		t.Fatalf("second ensure = %+v", second)
	}
}

func TestRenderJobEnsurerDisablesPendingWhenRenderPDFOff(t *testing.T) {
	jobs := newFakeRenderJobs()
	jobs.renderable = []delivery.RenderableRevision{
		{RevisionID: 9001, ReportID: 100, VersionID: 7, Revision: 1, PDFStatus: delivery.PDFStatusPending},
		{RevisionID: 9002, ReportID: 100, VersionID: 7, Revision: 2, PDFStatus: delivery.PDFStatusFailed, PDFAttempts: 1},
	}
	ensurer := NewRenderJobEnsurer(jobs, fakeRenderSettings{renderPDF: false})

	if err := ensurer.ReconcileP7(context.Background()); err != nil {
		t.Fatalf("ReconcileP7: %v", err)
	}
	if len(jobs.ensureCalls) != 0 {
		t.Fatalf("render_pdf=false still ensured jobs: %+v", jobs.ensureCalls)
	}
	if len(jobs.disabledCalls) != 1 || jobs.disabledCalls[0] != 9001 {
		t.Fatalf("disabled calls = %+v, want only the pending revision 9001", jobs.disabledCalls)
	}
}

func TestRenderJobEnsurerSkipsExhaustedAndSettledRevisions(t *testing.T) {
	jobs := newFakeRenderJobs()
	jobs.renderable = []delivery.RenderableRevision{
		{RevisionID: 9001, ReportID: 100, VersionID: 7, Revision: 1, PDFStatus: delivery.PDFStatusPending, PDFAttempts: 3},
		{RevisionID: 9002, ReportID: 100, VersionID: 7, Revision: 2, PDFStatus: delivery.PDFStatusDone},
		{RevisionID: 9003, ReportID: 100, VersionID: 7, Revision: 3, PDFStatus: delivery.PDFStatusDisabled},
		{RevisionID: 9004, ReportID: 100, VersionID: 7, Revision: 4, PDFStatus: delivery.PDFStatusPending, PDFAttempts: 2},
	}
	ensurer := NewRenderJobEnsurer(jobs, fakeRenderSettings{renderPDF: true, maxAttempts: 3})

	if err := ensurer.ReconcileP7(context.Background()); err != nil {
		t.Fatalf("ReconcileP7: %v", err)
	}
	if len(jobs.ensureCalls) != 1 || jobs.ensureCalls[0].RevisionID != 9004 {
		t.Fatalf("ensure calls = %+v, want only revision 9004", jobs.ensureCalls)
	}
}

func TestRenderJobEnsurerDefaultsWhenSettingsUnreadable(t *testing.T) {
	jobs := newFakeRenderJobs()
	jobs.renderable = []delivery.RenderableRevision{
		{RevisionID: 9001, ReportID: 100, VersionID: 7, Revision: 1, PDFStatus: delivery.PDFStatusPending},
	}
	ensurer := NewRenderJobEnsurer(jobs, fakeRenderSettings{err: errors.New("settings down")})

	if err := ensurer.ReconcileP7(context.Background()); err != nil {
		t.Fatalf("ReconcileP7: %v", err)
	}
	if len(jobs.ensureCalls) != 1 || jobs.ensureCalls[0].MaxAttempts != 3 {
		t.Fatalf("ensure calls = %+v, want the default max attempts 3", jobs.ensureCalls)
	}
}

func TestRenderJobEnsurerPropagatesListAndEnsureErrors(t *testing.T) {
	listErr := errors.New("list failed")
	jobs := newFakeRenderJobs()
	jobs.listErr = listErr
	ensurer := NewRenderJobEnsurer(jobs, fakeRenderSettings{renderPDF: true})
	if err := ensurer.ReconcileP7(context.Background()); !errors.Is(err, listErr) {
		t.Fatalf("ReconcileP7 list error = %v, want %v", err, listErr)
	}

	ensureErr := errors.New("ensure failed")
	jobs = newFakeRenderJobs()
	jobs.renderable = []delivery.RenderableRevision{
		{RevisionID: 9001, ReportID: 100, VersionID: 7, Revision: 1, PDFStatus: delivery.PDFStatusPending},
		{RevisionID: 9002, ReportID: 100, VersionID: 7, Revision: 2, PDFStatus: delivery.PDFStatusPending},
	}
	jobs.ensureErr = ensureErr
	ensurer = NewRenderJobEnsurer(jobs, fakeRenderSettings{renderPDF: true})
	if err := ensurer.ReconcileP7(context.Background()); !errors.Is(err, ensureErr) {
		t.Fatalf("ReconcileP7 ensure error = %v, want %v", err, ensureErr)
	}
}

// ---------------------------------------------------------------------------
// Worker tests
// ---------------------------------------------------------------------------

func newRenderWorkerFixture(t *testing.T, job RenderJob) (*RenderWorker, *fakeRenderQueue, *fakeRenderJobs, *fakeReportContent, *fakeRenderClient, *fakeReportEvents, *fakeReportAudit) {
	t.Helper()
	queue := &fakeRenderQueue{job: job, claimOK: true}
	jobs := newFakeRenderJobs()
	log := make([]string, 0, 16)
	content := newFakeReportContent(&log)
	client := &fakeRenderClient{pdf: []byte("%PDF-1.7 rendered")}
	events := &fakeReportEvents{log: &log}
	audit := &fakeReportAudit{log: &log}
	worker := NewRenderWorker(RenderWorkerDeps{
		Queue:             queue,
		Jobs:              jobs,
		Content:           content,
		Client:            client,
		Settings:          fakeRenderSettings{renderPDF: true, maxAttempts: 3, leaseSeconds: 120},
		Events:            events,
		Audit:             audit,
		Clock:             fakeReportClock{now: time.Date(2026, 11, 2, 1, 2, 3, 0, time.UTC)},
		InstanceID:        "render-worker-test",
		HeartbeatInterval: time.Hour,
	})
	return worker, queue, jobs, content, client, events, audit
}

// seedRenderableRevision stores one published revision with a stored HTML ref.
func seedRenderableRevision(jobs *fakeRenderJobs, content *fakeReportContent, revisionID, reportID int64, revision int32) {
	jobs.mu.Lock()
	defer jobs.mu.Unlock()
	htmlRef := int64(5000) + revisionID
	content.seed(htmlRef, revisionID, []byte("<html>报告的 HTML</html>"))
	jobs.revisions[revisionID] = delivery.ReportRevision{
		ID: revisionID, ReportID: reportID, Revision: revision,
		Status: delivery.ReportStatusPublished, RenderStatus: "done",
		HTMLRef:   &htmlRef,
		PDFStatus: delivery.PDFStatusPending,
	}
}

func TestRenderWorkerRendersAndPublishesPDF(t *testing.T) {
	job := renderJob(t, 77, 3, 7, 9001, 100, 1, 3)
	worker, queue, jobs, content, client, events, audit := newRenderWorkerFixture(t, job)
	seedRenderableRevision(jobs, content, 9001, 100, 2)

	claimed, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if !claimed {
		t.Fatal("RunOnce reported no claimed job")
	}
	if len(queue.claimTypes) != 1 || queue.claimTypes[0] != "report.render_pdf" {
		t.Fatalf("claim types = %+v", queue.claimTypes)
	}
	if queue.claimLease != 120*time.Second {
		t.Fatalf("claim lease = %v, want 120s", queue.claimLease)
	}
	if len(jobs.runningCalls) != 1 || jobs.runningCalls[0] != 9001 {
		t.Fatalf("MarkPDFRunning calls = %+v", jobs.runningCalls)
	}
	if string(client.lastHTML) != "<html>报告的 HTML</html>" {
		t.Fatalf("renderer html = %q", client.lastHTML)
	}
	if len(jobs.renderedCalls) != 1 {
		t.Fatalf("MarkPDFRendered calls = %+v", jobs.renderedCalls)
	}
	rendered := jobs.renderedCalls[0]
	if rendered.RevisionID != 9001 || rendered.Ref.RefID <= 0 ||
		rendered.Ref.SHA256 != reportContentSHA(client.pdf) || rendered.Ref.Size != int64(len(client.pdf)) ||
		rendered.Attempts != 1 {
		t.Fatalf("MarkPDFRendered args = %+v", rendered)
	}
	if !rendered.At.Equal(time.Date(2026, 11, 2, 1, 2, 3, 0, time.UTC)) {
		t.Fatalf("pdf_rendered_at = %v, want the injected clock", rendered.At)
	}
	if len(queue.completed) != 1 || queue.completed[0] != 77 {
		t.Fatalf("completed = %+v", queue.completed)
	}
	if len(queue.failed) != 0 {
		t.Fatalf("failed = %+v", queue.failed)
	}
	if got := events.count("report.pdf_ready"); got != 1 {
		t.Fatalf("report.pdf_ready events = %d", got)
	}
	event := events.published[0]
	if event.versionID != 7 {
		t.Fatalf("event version = %d, want 7", event.versionID)
	}
	for key, want := range map[string]any{
		"report_id":          "100",
		"report_revision_id": "9001",
		"revision":           int32(2),
		"pdf_ref":            fmt.Sprint(rendered.Ref.RefID),
		"size_bytes":         int64(len(client.pdf)),
	} {
		if got := event.payload[key]; fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("event payload[%s] = %v, want %v", key, got, want)
		}
	}
	if len(audit.calls) != 1 || audit.calls[0] != "delivery.report.pdf_rendered" {
		t.Fatalf("audit = %+v", audit.calls)
	}
}

func TestRenderWorkerRetriesWhenRendererIsUnavailable(t *testing.T) {
	job := renderJob(t, 77, 3, 7, 9001, 100, 1, 3)
	worker, queue, jobs, content, client, events, audit := newRenderWorkerFixture(t, job)
	seedRenderableRevision(jobs, content, 9001, 100, 1)
	client.err = fmt.Errorf("%w: connection refused", ErrRenderUnavailable)

	if claimed, err := worker.RunOnce(context.Background()); err != nil || !claimed {
		t.Fatalf("RunOnce = %v, %v", claimed, err)
	}
	if len(queue.failed) != 1 || !queue.failed[0].Retryable {
		t.Fatalf("queue failure = %+v, want one retryable failure", queue.failed)
	}
	if len(queue.completed) != 0 || len(jobs.failedCalls) != 0 {
		t.Fatalf("retryable failure completed/finalized: completed=%+v final=%+v", queue.completed, jobs.failedCalls)
	}
	if events.count("report.pdf_ready") != 0 || events.count("report.pdf_failed") != 0 || len(audit.calls) != 0 {
		t.Fatal("retryable failure emitted a terminal event/audit")
	}
}

// TestRenderWorkerDoesNotFailRevisionAfterLeaseLoss pins the P7 fencing rule:
// once queue.Fail reports lease_lost, this worker no longer owns the revision
// and must not mark it failed (a newer attempt may be rendering it).
func TestRenderWorkerDoesNotFailRevisionAfterLeaseLoss(t *testing.T) {
	job := renderJob(t, 77, 3, 7, 9001, 100, 1, 3)
	worker, queue, jobs, content, client, events, audit := newRenderWorkerFixture(t, job)
	seedRenderableRevision(jobs, content, 9001, 100, 1)
	client.err = fmt.Errorf("%w: renderer returned 422", ErrRenderRejected)
	queue.failErr = platform.NewError(409, "lease_lost", "work item lease was taken over by another claimant")

	claimed, err := worker.RunOnce(context.Background())
	if !claimed {
		t.Fatal("RunOnce did not report the claimed lease-lost job")
	}
	// The queue's lease_lost conflict is propagated (the caller logs it), but it
	// must never be translated into a revision failure write.
	if !isRenderLeaseLost(err) {
		t.Fatalf("RunOnce error = %v, want the propagated lease_lost", err)
	}
	if len(jobs.failedCalls) != 0 {
		t.Fatalf("MarkPDFFailed calls = %+v, want none after lease loss", jobs.failedCalls)
	}
	if events.count("report.pdf_failed") != 0 || len(audit.calls) != 0 {
		t.Fatalf("lease-lost failure emitted events/audit: events=%v audit=%v", events.published, audit.calls)
	}
}

func TestRenderWorkerMarksRejectedRenderFailed(t *testing.T) {
	job := renderJob(t, 77, 3, 7, 9001, 100, 1, 3)
	worker, queue, jobs, content, client, events, audit := newRenderWorkerFixture(t, job)
	seedRenderableRevision(jobs, content, 9001, 100, 1)
	client.err = fmt.Errorf("%w: renderer returned 422", ErrRenderRejected)

	if claimed, err := worker.RunOnce(context.Background()); err != nil || !claimed {
		t.Fatalf("RunOnce = %v, %v", claimed, err)
	}
	if len(jobs.failedCalls) != 1 {
		t.Fatalf("MarkPDFFailed calls = %+v, want 1", jobs.failedCalls)
	}
	failed := jobs.failedCalls[0]
	if failed.RevisionID != 9001 || failed.Attempts != 1 ||
		!strings.Contains(failed.Message, "renderer returned 422") {
		t.Fatalf("MarkPDFFailed args = %+v", failed)
	}
	if len(queue.failed) != 1 || queue.failed[0].Retryable {
		t.Fatalf("queue failure = %+v, want one non-retryable failure", queue.failed)
	}
	if got := events.count("report.pdf_failed"); got != 1 {
		t.Fatalf("report.pdf_failed events = %d", got)
	}
	event := events.published[0]
	if event.versionID != 7 || fmt.Sprint(event.payload["attempts"]) != "1" ||
		!strings.Contains(fmt.Sprint(event.payload["error"]), "renderer returned 422") {
		t.Fatalf("report.pdf_failed event = %+v", event)
	}
	if len(audit.calls) != 1 || audit.calls[0] != "delivery.report.pdf_failed" {
		t.Fatalf("audit = %+v", audit.calls)
	}
}

func TestRenderWorkerMarksExhaustedAttemptsFailed(t *testing.T) {
	job := renderJob(t, 77, 3, 7, 9001, 100, 3, 3)
	worker, queue, jobs, content, client, events, _ := newRenderWorkerFixture(t, job)
	seedRenderableRevision(jobs, content, 9001, 100, 1)
	client.err = fmt.Errorf("%w: renderer timeout", ErrRenderUnavailable)

	if claimed, err := worker.RunOnce(context.Background()); err != nil || !claimed {
		t.Fatalf("RunOnce = %v, %v", claimed, err)
	}
	if len(jobs.failedCalls) != 1 || jobs.failedCalls[0].Attempts != 3 {
		t.Fatalf("MarkPDFFailed calls = %+v, want attempts 3", jobs.failedCalls)
	}
	if len(queue.failed) != 1 || queue.failed[0].Retryable {
		t.Fatalf("queue failure = %+v, want a terminal failure", queue.failed)
	}
	if got := events.count("report.pdf_failed"); got != 1 {
		t.Fatalf("report.pdf_failed events = %d", got)
	}
}

func TestRenderWorkerCompletesWhenRevisionAlreadySettled(t *testing.T) {
	job := renderJob(t, 77, 3, 7, 9001, 100, 1, 3)
	worker, queue, jobs, content, client, events, _ := newRenderWorkerFixture(t, job)
	seedRenderableRevision(jobs, content, 9001, 100, 1)
	jobs.runningErr = delivery.ErrStateConflict

	if claimed, err := worker.RunOnce(context.Background()); err != nil || !claimed {
		t.Fatalf("RunOnce = %v, %v", claimed, err)
	}
	if len(queue.completed) != 1 || len(queue.failed) != 0 {
		t.Fatalf("completed = %+v failed = %+v, want a clean completion", queue.completed, queue.failed)
	}
	if client.calls != 0 || events.count("report.pdf_ready") != 0 {
		t.Fatal("settled revision was rendered again")
	}
}

func TestRenderWorkerRejectsInvalidPayload(t *testing.T) {
	job := renderJob(t, 77, 3, 7, 9001, 100, 1, 3)
	job.Payload = json.RawMessage(`{"report_revision_id":"not-a-number"}`)
	worker, queue, jobs, content, _, events, _ := newRenderWorkerFixture(t, job)
	seedRenderableRevision(jobs, content, 9001, 100, 1)

	if claimed, err := worker.RunOnce(context.Background()); err != nil || !claimed {
		t.Fatalf("RunOnce = %v, %v", claimed, err)
	}
	if len(queue.failed) != 1 || queue.failed[0].Retryable {
		t.Fatalf("queue failure = %+v, want one non-retryable failure", queue.failed)
	}
	if len(queue.completed) != 0 || len(jobs.runningCalls) != 0 || events.count("report.pdf_ready") != 0 {
		t.Fatal("invalid payload touched the revision or emitted an event")
	}
}

func TestRenderWorkerNoJobIsANoop(t *testing.T) {
	worker, queue, jobs, content, client, _, _ := newRenderWorkerFixture(t, RenderJob{})
	seedRenderableRevision(jobs, content, 9001, 100, 1)
	queue.claimOK = false

	claimed, err := worker.RunOnce(context.Background())
	if err != nil || claimed {
		t.Fatalf("RunOnce = %v, %v, want (false, nil)", claimed, err)
	}
	if len(queue.completed) != 0 || len(queue.failed) != 0 || client.calls != 0 || len(jobs.runningCalls) != 0 {
		t.Fatal("no-job poll touched a revision or the queue")
	}
}

func TestRenderWorkerHeartbeatsWhileRendering(t *testing.T) {
	job := renderJob(t, 77, 3, 7, 9001, 100, 1, 3)
	worker, queue, jobs, content, _, _, _ := newRenderWorkerFixture(t, job)
	worker.heartbeatInterval = 5 * time.Millisecond
	started := make(chan struct{})
	release := make(chan struct{})
	worker.client = &blockingRenderClient{started: started, release: release, pdf: []byte("%PDF-1.7")}
	seedRenderableRevision(jobs, content, 9001, 100, 1)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = worker.RunOnce(context.Background())
	}()
	<-started
	time.Sleep(30 * time.Millisecond)
	close(release)
	<-done

	if queue.heartbeats == 0 {
		t.Fatal("worker never renewed the lease while rendering")
	}
	// Fix 1: the heartbeat must renew the lease the job was claimed with
	// (report.render_lease_seconds = 120s), not the agent queue default 30s;
	// otherwise the lease lapses between renewals.
	if queue.claimLease != 120*time.Second {
		t.Fatalf("claim lease = %v, want 120s", queue.claimLease)
	}
	for _, lease := range queue.heartbeatLeases {
		if lease != queue.claimLease {
			t.Fatalf("heartbeat lease = %v, want the claimed lease %v", lease, queue.claimLease)
		}
	}
}

// TestRenderHeartbeatIntervalKeepsRenewalBeforeExpiry proves the heartbeat
// cadence never lets the lease lapse: interval = max(lease/3, 5s) stays below
// the lease itself for every catalog-valid duration (30s..1800s).
func TestRenderHeartbeatIntervalKeepsRenewalBeforeExpiry(t *testing.T) {
	for _, lease := range []time.Duration{30 * time.Second, 60 * time.Second, 120 * time.Second, 1800 * time.Second} {
		interval := renderHeartbeatInterval(lease)
		if interval < 5*time.Second {
			t.Fatalf("lease %v: interval %v is below the 5s floor", lease, interval)
		}
		if interval >= lease {
			t.Fatalf("lease %v: interval %v does not renew before expiry", lease, interval)
		}
		if interval*2 >= lease {
			t.Fatalf("lease %v: interval %v leaves no renewal margin", lease, interval)
		}
	}
}

type blockingRenderClient struct {
	started chan struct{}
	release chan struct{}
	pdf     []byte
}

func (c *blockingRenderClient) RenderPDF(context.Context, []byte) ([]byte, error) {
	close(c.started)
	<-c.release
	return append([]byte(nil), c.pdf...), nil
}

func TestRenderWorkerRunStopsOnContextCancel(t *testing.T) {
	worker, queue, _, _, _, _, _ := newRenderWorkerFixture(t, RenderJob{})
	queue.claimOK = false
	worker.pollInterval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := worker.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
}

// TestRenderWorkerUsesConfiguredMaxAttempts covers the settings fallback when
// the work item carried no max_attempts (a defensive default).
func TestRenderWorkerUsesConfiguredMaxAttempts(t *testing.T) {
	job := renderJob(t, 77, 3, 7, 9001, 100, 1, 0)
	queue := &fakeRenderQueue{job: job, claimOK: true}
	jobs := newFakeRenderJobs()
	log := make([]string, 0, 8)
	content := newFakeReportContent(&log)
	client := &fakeRenderClient{err: fmt.Errorf("%w: down", ErrRenderUnavailable)}
	worker := NewRenderWorker(RenderWorkerDeps{
		Queue: queue, Jobs: jobs, Content: content, Client: client,
		Settings: fakeRenderSettings{renderPDF: true, maxAttempts: 1, leaseSeconds: 120},
		Clock:    fakeReportClock{now: time.Now().UTC()},
	})
	seedRenderableRevision(jobs, content, 9001, 100, 1)

	if claimed, err := worker.RunOnce(context.Background()); err != nil || !claimed {
		t.Fatalf("RunOnce = %v, %v", claimed, err)
	}
	if len(jobs.failedCalls) != 1 {
		t.Fatalf("MarkPDFFailed calls = %+v, want the configured max attempt to finalize", jobs.failedCalls)
	}
}

// TestRenderWorkerTruncatesPDFErrorToSchemaBound keeps pdf_error within the
// delivery_0004 CHECK (<= 2000 chars) even for a huge upstream message.
func TestRenderWorkerTruncatesPDFErrorToSchemaBound(t *testing.T) {
	job := renderJob(t, 77, 3, 7, 9001, 100, 1, 1)
	worker, _, jobs, content, client, _, _ := newRenderWorkerFixture(t, job)
	seedRenderableRevision(jobs, content, 9001, 100, 1)
	client.err = fmt.Errorf("%w: %s", ErrRenderRejected, strings.Repeat("x", 5000))

	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(jobs.failedCalls) != 1 {
		t.Fatalf("MarkPDFFailed calls = %+v", jobs.failedCalls)
	}
	if got := len([]rune(jobs.failedCalls[0].Message)); got > 2000 {
		t.Fatalf("pdf_error is %d chars, want <= 2000", got)
	}
}

/* --------------------------- ReportService OpenPDF ------------------------ */

// seedPDFRevision seeds one published revision with both HTML and PDF refs.
func seedPDFRevision(fixture *reportFixture, pdfStatus string, pdfRef *int64) (delivery.Report, delivery.ReportRevision) {
	report, revision := fixture.seedPublishedWaveReport(7, 12, reportTestFingerprint("a"))
	fixture.repo.mu.Lock()
	stored := fixture.repo.revisions[report.ID][revision.Revision]
	stored.PDFStatus = delivery.PDFStatus(pdfStatus)
	stored.PDFRef = pdfRef
	fixture.repo.revisions[report.ID][revision.Revision] = stored
	fixture.repo.mu.Unlock()
	return report, stored
}

/* ------------------------- ReportService PDF booking ---------------------- */

// TestReportServicePublishBooksPDFJob covers the publish hook: a published
// revision books exactly one idempotent report.render_pdf work item.
func TestReportServicePublishBooksPDFJob(t *testing.T) {
	fixture := newReportFixture(t)
	jobs := newFakeRenderJobs()
	fixture.svc.WithRenderJobs(jobs, true, 3)
	report, revision := fixture.seedWaveReport(7, 12)

	published, err := fixture.svc.Publish(context.Background(), report.ID, revision.Revision, "")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(jobs.ensureCalls) != 1 {
		t.Fatalf("ensure calls = %+v, want 1", jobs.ensureCalls)
	}
	ensure := jobs.ensureCalls[0]
	wantKey := fmt.Sprintf("report:%d:rev:%d", report.ID, published.Revision)
	if ensure.RevisionID != published.ID || ensure.ReportID != report.ID || ensure.VersionID != 7 ||
		ensure.DedupeKey != wantKey || ensure.MaxAttempts != 3 {
		t.Fatalf("ensure = %+v, want key %q", ensure, wantKey)
	}
	if len(jobs.disabledCalls) != 0 {
		t.Fatalf("disabled calls = %+v", jobs.disabledCalls)
	}
}

// TestReportServicePublishDisablesPDFWhenSettingOff covers report.render_pdf
// = false: no task is booked and the revision is marked disabled.
func TestReportServicePublishDisablesPDFWhenSettingOff(t *testing.T) {
	fixture := newReportFixture(t)
	jobs := newFakeRenderJobs()
	fixture.svc.WithRenderJobs(jobs, false, 3)
	report, revision := fixture.seedWaveReport(7, 12)

	published, err := fixture.svc.Publish(context.Background(), report.ID, revision.Revision, "")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(jobs.ensureCalls) != 0 {
		t.Fatalf("ensure calls = %+v, want none when render_pdf=false", jobs.ensureCalls)
	}
	if len(jobs.disabledCalls) != 1 || jobs.disabledCalls[0] != published.ID {
		t.Fatalf("disabled calls = %+v, want [%d]", jobs.disabledCalls, published.ID)
	}
}

// TestReportServicePublishKeepsPDFPendingWithoutRenderJobs covers a P4-era
// wiring (no render job repository): Publish keeps the HTML-only behavior.
func TestReportServicePublishKeepsPDFPendingWithoutRenderJobs(t *testing.T) {
	fixture := newReportFixture(t)
	report, revision := fixture.seedWaveReport(7, 12)
	if _, err := fixture.svc.Publish(context.Background(), report.ID, revision.Revision, ""); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	// The fake repo records no PDF booking calls; the DB default stays pending.
	if got := fixture.repoRevision(t, report.ID, revision.Revision).PDFStatus; got != "pending" && got != "" {
		t.Fatalf("pdf_status = %q, want the pending default", got)
	}
}

func TestReportServiceOpenPDFDone(t *testing.T) {
	fixture := newReportFixture(t)
	pdfRefID := int64(6001)
	report, revision := seedPDFRevision(fixture, "done", &pdfRefID)
	pdf := []byte("%PDF-1.7 report bytes")
	fixture.content.seedPDF(pdfRefID, pdf)

	payload, stored, err := fixture.svc.OpenPDF(context.Background(), 7, report.ID, revision.Revision)
	if err != nil {
		t.Fatalf("OpenPDF: %v", err)
	}
	if string(payload) != string(pdf) || stored.ID != revision.ID {
		t.Fatalf("OpenPDF payload = %q stored=%+v", payload, stored)
	}
}

func TestReportServiceOpenPDFStates(t *testing.T) {
	tests := []struct {
		name     string
		status   string
		ref      *int64
		code     string
		httpCode int
	}{
		{name: "pending", status: "pending", code: "pdf_not_ready", httpCode: 409},
		{name: "running", status: "running", code: "pdf_not_ready", httpCode: 409},
		{name: "failed", status: "failed", code: "pdf_failed", httpCode: 409},
		{name: "disabled", status: "disabled", httpCode: 404},
		{name: "done without ref", status: "done", httpCode: 404},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReportFixture(t)
			report, revision := seedPDFRevision(fixture, test.status, test.ref)
			_, _, err := fixture.svc.OpenPDF(context.Background(), 7, report.ID, revision.Revision)
			apiErr := platform.AsError(err)
			if apiErr.Status != test.httpCode {
				t.Fatalf("status = %d, want %d (err=%v)", apiErr.Status, test.httpCode, err)
			}
			if test.code != "" && apiErr.Code != test.code {
				t.Fatalf("code = %q, want %q", apiErr.Code, test.code)
			}
		})
	}
}

func TestReportServiceOpenPDFRejectsCorruptContent(t *testing.T) {
	fixture := newReportFixture(t)
	refID := int64(6001)
	report, revision := seedPDFRevision(fixture, "done", &refID)
	fixture.content.pdfErr = ErrReportContentCorrupt

	_, _, err := fixture.svc.OpenPDF(context.Background(), 7, report.ID, revision.Revision)
	apiErr := platform.AsError(err)
	if apiErr.Status != 409 || apiErr.Code != "state_conflict" {
		t.Fatalf("OpenPDF error = %v (status %d)", err, apiErr.Status)
	}
	if reason := apiErr.Details["reason"]; reason != "report_content_corrupt" {
		t.Fatalf("details.reason = %v", reason)
	}
}

func TestReportServiceOpenPDFValidatesScope(t *testing.T) {
	fixture := newReportFixture(t)
	refID := int64(6001)
	report, revision := seedPDFRevision(fixture, "done", &refID)
	fixture.content.seedPDF(refID, []byte("%PDF-1.7"))

	if _, _, err := fixture.svc.OpenPDF(context.Background(), 8, report.ID, revision.Revision); platform.AsError(err).Status != 404 {
		t.Fatalf("cross-version OpenPDF error = %v, want 404", err)
	}
	if _, _, err := fixture.svc.OpenPDF(context.Background(), 7, report.ID, 99); platform.AsError(err).Status != 404 {
		t.Fatalf("missing revision OpenPDF error = %v, want 404", err)
	}
}
