//go:build integration

package deliveryapp_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentpg "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	contentstore "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/content"
	deliverypg "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/render"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	storagepg "github.com/EdouardRichard/EzTest/backend/internal/storage/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// integrationPDFSettings is the storage data-root + P7 PDF settings surface
// shared by the storage service and the render ensurer/worker.
type integrationPDFSettings struct {
	root         string
	renderPDF    bool
	leaseSeconds int64
	maxAttempts  int64
}

func (s integrationPDFSettings) String(context.Context, string) (string, error) {
	return s.root, nil
}

func (s integrationPDFSettings) Int(_ context.Context, key string) (int64, error) {
	switch key {
	case "report.render_lease_seconds":
		if s.leaseSeconds > 0 {
			return s.leaseSeconds, nil
		}
		return 120, nil
	case "report.render_max_attempts":
		if s.maxAttempts > 0 {
			return s.maxAttempts, nil
		}
		return 3, nil
	default:
		return 0, fmt.Errorf("unexpected setting %q", key)
	}
}

func (s integrationPDFSettings) Bool(_ context.Context, key string) (bool, error) {
	if key != "report.render_pdf" {
		return false, fmt.Errorf("unexpected setting %q", key)
	}
	return s.renderPDF, nil
}

// integrationRenderQueue adapts the real agent work queue onto the delivery
// RenderQueue port (the same shape the cmd wiring uses at Task 19).
type integrationRenderQueue struct {
	queue *agentapp.WorkQueue
}

func (q integrationRenderQueue) ClaimForTypes(ctx context.Context, claimant string, types []string, lease time.Duration) (deliveryapp.RenderJob, bool, error) {
	item, ok, err := q.queue.ClaimForTypes(ctx, claimant, types, lease)
	if err != nil || !ok {
		return deliveryapp.RenderJob{}, ok, err
	}
	return deliveryapp.RenderJob{
		ID: item.ID, Epoch: item.LeaseEpoch, VersionID: item.VersionID,
		Attempts: item.Attempts, MaxAttempts: item.MaxAttempts, Payload: item.Payload,
	}, true, nil
}

func (q integrationRenderQueue) Heartbeat(ctx context.Context, jobID, epoch int64, lease time.Duration) error {
	return q.queue.Heartbeat(ctx, jobID, epoch, lease)
}

func (q integrationRenderQueue) Complete(ctx context.Context, jobID, epoch int64) error {
	return q.queue.Complete(ctx, jobID, epoch)
}

func (q integrationRenderQueue) Fail(ctx context.Context, jobID, epoch int64, cause error, retryable bool) error {
	return q.queue.Fail(ctx, jobID, epoch, cause, retryable)
}

// integrationStaticRenderer returns fixed PDF bytes and records the HTML it was
// asked to render.
type integrationStaticRenderer struct {
	pdf  []byte
	err  error
	html []byte
}

func (r *integrationStaticRenderer) RenderPDF(_ context.Context, html []byte) ([]byte, error) {
	r.html = append([]byte(nil), html...)
	if r.err != nil {
		return nil, r.err
	}
	return append([]byte(nil), r.pdf...), nil
}

type integrationPDFFixture struct {
	svc      *deliveryapp.ReportService
	repo     *deliverypg.ReportRepository
	pool     *pgxpool.Pool
	worker   *deliveryapp.RenderWorker
	ensurer  *deliveryapp.RenderJobEnsurer
	queue    integrationRenderQueue
	renderer *integrationStaticRenderer
	data     *integrationReportData
	clock    integrationReportClock
	events   *integrationReportEvents
	audit    *integrationReportAudit
}

func newIntegrationPDFFixture(t *testing.T) *integrationPDFFixture {
	t.Helper()
	pool := testutil.NewDB(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO run_wave (id, version_id, status, trigger)
		VALUES (12, 7, 'done', 'manual')
	`); err != nil {
		t.Fatalf("insert run wave: %v", err)
	}
	ids, err := platformid.NewSnowflake(0)
	if err != nil {
		t.Fatalf("new snowflake: %v", err)
	}
	settings := integrationPDFSettings{root: t.TempDir(), renderPDF: true, leaseSeconds: 120, maxAttempts: 3}
	storageSvc := storageapp.New(
		storagepg.New(pool), storagepg.NewFileStore(), settings, ids, nil, nil,
	)
	content := contentstore.New(storageSvc, deliverypg.NewContentRefReader(pool))
	htmlRenderer, err := render.NewHTMLRenderer(render.TemplateVersionV2)
	if err != nil {
		t.Fatalf("new HTML renderer: %v", err)
	}
	data := &integrationReportData{wave: integrationWaveSnapshot()}
	events := &integrationReportEvents{}
	audit := &integrationReportAudit{}
	tasks := &integrationReportTasks{}
	clk := integrationReportClock{now: time.Date(2026, 11, 2, 7, 0, 0, 0, time.UTC)}
	repo := deliverypg.NewReportRepository(pool, ids)
	svc := deliveryapp.NewReportService(
		repo, htmlRenderer, content, data, events, audit, clk, tasks,
		delivery.SectionOptions{Summary: true, Results: true, Issues: true, Coverage: true, Evidence: true, Appendix: true},
	).WithRenderJobs(repo, true, 3)
	workQueue := agentapp.NewWorkQueue(
		agentpg.NewWorkItemRepository(pool, ids), clock.System{}, 30*time.Second, 3,
	)
	queue := integrationRenderQueue{queue: workQueue}
	renderer := &integrationStaticRenderer{pdf: []byte("%PDF-1.7\n" + "integration pdf\n")}
	worker := deliveryapp.NewRenderWorker(deliveryapp.RenderWorkerDeps{
		Queue: queue, Jobs: repo, Content: content, Client: renderer,
		Settings: settings, Events: events, Audit: audit, Clock: clk,
		InstanceID:        "integration-render-worker",
		HeartbeatInterval: time.Hour,
	})
	return &integrationPDFFixture{
		svc: svc, repo: repo, pool: pool, worker: worker,
		ensurer: deliveryapp.NewRenderJobEnsurer(repo, settings),
		queue:   queue, renderer: renderer, data: data, clock: clk, events: events, audit: audit,
	}
}

// count returns how many events of one type were published.
func (e *integrationReportEvents) count(eventType string) int {
	count := 0
	for _, candidate := range e.types {
		if candidate == eventType {
			count++
		}
	}
	return count
}

// publishWaveRevision ensures + publishes one wave report revision.
func (f *integrationPDFFixture) publishWaveRevision(t *testing.T) delivery.ReportRevision {
	t.Helper()
	report, err := f.svc.EnsureWaveReport(context.Background(), 7, 12)
	if err != nil {
		t.Fatalf("EnsureWaveReport: %v", err)
	}
	published, err := f.svc.Publish(context.Background(), report.ID, 1, "叙述")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	return published
}

// TestReportPDFPipelineIntegration wires the real PostgreSQL report repository,
// real content ledger, real work queue and renderer over the fake renderer
// client: publish -> ensure -> claim -> render -> done, then asserts the
// revision metadata and HTML bytes are immutable (spec §4.2 / review focus 2).
func TestReportPDFPipelineIntegration(t *testing.T) {
	fixture := newIntegrationPDFFixture(t)
	ctx := context.Background()
	revision := fixture.publishWaveRevision(t)
	if revision.TemplateVersion != render.TemplateVersionV2 {
		t.Fatalf("published template_version = %q, want 2", revision.TemplateVersion)
	}
	if revision.PDFStatus != delivery.PDFStatusPending {
		t.Fatalf("published pdf_status = %q, want pending", revision.PDFStatus)
	}

	htmlBefore, _, err := fixture.svc.OpenHTML(ctx, 7, revision.ReportID, revision.Revision)
	if err != nil {
		t.Fatalf("OpenHTML before PDF: %v", err)
	}
	immutableBefore := revision

	if err := fixture.ensurer.ReconcileP7(ctx); err != nil {
		t.Fatalf("ReconcileP7: %v", err)
	}
	dedupeKey := fmt.Sprintf("report:%d:rev:%d", revision.ReportID, revision.Revision)
	var (
		workType, team string
		maxAttempts    int
		workCount      int
	)
	if err := fixture.pool.QueryRow(ctx, `
		SELECT count(*) FROM work_item WHERE dedupe_key = $1
	`, dedupeKey).Scan(&workCount); err != nil {
		t.Fatalf("count render work items: %v", err)
	}
	if workCount != 1 {
		t.Fatalf("render work items = %d, want 1", workCount)
	}
	if err := fixture.pool.QueryRow(ctx, `
		SELECT team, type, max_attempts FROM work_item WHERE dedupe_key = $1
	`, dedupeKey).Scan(&team, &workType, &maxAttempts); err != nil {
		t.Fatalf("read render work item: %v", err)
	}
	if team != "ops" || workType != "report.render_pdf" || maxAttempts != 3 {
		t.Fatalf("render work item = team:%s type:%s max:%d", team, workType, maxAttempts)
	}

	claimed, err := fixture.worker.RunOnce(ctx)
	if err != nil || !claimed {
		t.Fatalf("worker RunOnce = %v, %v", claimed, err)
	}
	if !bytes.Contains(fixture.renderer.html, []byte(`data-template-version="2"`)) {
		t.Fatalf("renderer did not receive the v2 HTML (%d bytes)", len(fixture.renderer.html))
	}

	stored, err := fixture.repo.GetRevision(ctx, revision.ReportID, revision.Revision)
	if err != nil {
		t.Fatalf("GetRevision after render: %v", err)
	}
	if stored.PDFStatus != delivery.PDFStatusDone || stored.PDFRef == nil ||
		stored.PDFRenderedAt == nil || stored.PDFAttempts != 1 || stored.PDFError != "" {
		t.Fatalf("rendered revision = %+v", stored)
	}
	// Immutability: the authoritative metadata is byte-for-byte unchanged.
	if !stored.AsOf.Equal(immutableBefore.AsOf) ||
		stored.HTMLRef == nil || immutableBefore.HTMLRef == nil || *stored.HTMLRef != *immutableBefore.HTMLRef ||
		stored.InputFingerprint != immutableBefore.InputFingerprint ||
		stored.TemplateVersion != render.TemplateVersionV2 ||
		stored.Status != delivery.ReportStatusPublished || stored.RenderStatus != "done" {
		t.Fatalf("PDF render changed immutable revision metadata: before=%+v after=%+v", immutableBefore, stored)
	}
	htmlAfter, _, err := fixture.svc.OpenHTML(ctx, 7, revision.ReportID, revision.Revision)
	if err != nil {
		t.Fatalf("OpenHTML after PDF: %v", err)
	}
	if !bytes.Equal(htmlBefore, htmlAfter) {
		t.Fatal("PDF render changed the authoritative HTML bytes")
	}

	// The PDF lives in the ledger under owner_kind=report_revision/report_pdf.
	var ownerKind, role, mime string
	var ownerID, pdfSize int64
	if err := fixture.pool.QueryRow(ctx, `
		SELECT r.owner_kind, r.owner_id, r.role, o.mime, o.size
		FROM content_ref r
		JOIN content_object o ON o.sha256 = r.object_id
		WHERE r.id = $1
	`, *stored.PDFRef).Scan(&ownerKind, &ownerID, &role, &mime, &pdfSize); err != nil {
		t.Fatalf("read PDF ledger row: %v", err)
	}
	if ownerKind != "report_revision" || ownerID != stored.ID || role != "report_pdf" ||
		mime != "application/pdf" || pdfSize != int64(len(fixture.renderer.pdf)) {
		t.Fatalf("PDF ledger = owner:%s/%d role:%s mime:%s size:%d", ownerKind, ownerID, role, mime, pdfSize)
	}
	pdf, opened, err := fixture.svc.OpenPDF(ctx, 7, revision.ReportID, revision.Revision)
	if err != nil {
		t.Fatalf("OpenPDF: %v", err)
	}
	if !bytes.Equal(pdf, fixture.renderer.pdf) || opened.ID != stored.ID {
		t.Fatalf("OpenPDF = %d bytes (revision %d)", len(pdf), opened.ID)
	}
	if fixture.events.count("report.pdf_ready") != 1 {
		t.Fatalf("report.pdf_ready events = %v", fixture.events.types)
	}
	if len(fixture.audit.actions) == 0 || fixture.audit.actions[len(fixture.audit.actions)-1] != "delivery.report.pdf_rendered" {
		t.Fatalf("audit actions = %v", fixture.audit.actions)
	}
	var workStatus string
	var attempts int
	if err := fixture.pool.QueryRow(ctx, `
		SELECT status, attempts FROM work_item WHERE dedupe_key = $1
	`, dedupeKey).Scan(&workStatus, &attempts); err != nil {
		t.Fatalf("read finished work item: %v", err)
	}
	if workStatus != "done" || attempts != 1 {
		t.Fatalf("finished work item = %s/%d", workStatus, attempts)
	}
}

// TestReportPDFRetryIntegration covers the retryable failure path end to end:
// a transient renderer error reopens the work item and leaves the revision in
// the running state; the retried attempt increments pdf_attempts and completes.
func TestReportPDFRetryIntegration(t *testing.T) {
	fixture := newIntegrationPDFFixture(t)
	ctx := context.Background()
	revision := fixture.publishWaveRevision(t)
	dedupeKey := fmt.Sprintf("report:%d:rev:%d", revision.ReportID, revision.Revision)

	fixture.renderer.err = fmt.Errorf("%w: connection refused", deliveryapp.ErrRenderUnavailable)
	if claimed, err := fixture.worker.RunOnce(ctx); err != nil || !claimed {
		t.Fatalf("first RunOnce = %v, %v", claimed, err)
	}
	afterFailure, err := fixture.repo.GetRevision(ctx, revision.ReportID, revision.Revision)
	if err != nil {
		t.Fatalf("GetRevision after retryable failure: %v", err)
	}
	if afterFailure.PDFStatus != delivery.PDFStatusRunning || afterFailure.PDFAttempts != 1 {
		t.Fatalf("revision after retryable failure = %+v", afterFailure)
	}
	var workStatus string
	if err := fixture.pool.QueryRow(ctx, `SELECT status FROM work_item WHERE dedupe_key = $1`, dedupeKey).Scan(&workStatus); err != nil {
		t.Fatalf("read retried work item: %v", err)
	}
	if workStatus != "open" {
		t.Fatalf("work item status after retryable failure = %s, want open", workStatus)
	}
	// The agent work queue applies an exponential backoff; make the retry
	// available immediately so the test does not sleep.
	if _, err := fixture.pool.Exec(ctx, `UPDATE work_item SET available_at = now() WHERE dedupe_key = $1`, dedupeKey); err != nil {
		t.Fatalf("release retried work item: %v", err)
	}

	fixture.renderer.err = nil
	if claimed, err := fixture.worker.RunOnce(ctx); err != nil || !claimed {
		t.Fatalf("retry RunOnce = %v, %v", claimed, err)
	}
	done, err := fixture.repo.GetRevision(ctx, revision.ReportID, revision.Revision)
	if err != nil {
		t.Fatalf("GetRevision after retry: %v", err)
	}
	if done.PDFStatus != delivery.PDFStatusDone || done.PDFRef == nil || done.PDFAttempts != 2 {
		t.Fatalf("revision after retry = %+v", done)
	}
	if fixture.events.count("report.pdf_ready") != 1 || fixture.events.count("report.pdf_failed") != 0 {
		t.Fatalf("retry events = %v", fixture.events.types)
	}
	// A re-claim (attempt 2) must leave exactly one live report_pdf ref for the
	// revision: the ledger write is idempotent per revision.
	var livePDFRefs int
	if err := fixture.pool.QueryRow(ctx, `
		SELECT count(*) FROM content_ref
		WHERE owner_kind = 'report_revision' AND owner_id = $1
		  AND role = 'report_pdf' AND deleted_at IS NULL
	`, revision.ID).Scan(&livePDFRefs); err != nil {
		t.Fatalf("count live PDF refs: %v", err)
	}
	if livePDFRefs != 1 {
		t.Fatalf("live report_pdf refs after re-claim = %d, want exactly 1", livePDFRefs)
	}
}

// TestReportPDFFailureIntegration covers the terminal failure path: the
// revision becomes failed with a bounded error, the work item dies and the
// authoritative HTML/metadata stay untouched.
func TestReportPDFFailureIntegration(t *testing.T) {
	fixture := newIntegrationPDFFixture(t)
	ctx := context.Background()
	revision := fixture.publishWaveRevision(t)
	htmlBefore, _, err := fixture.svc.OpenHTML(ctx, 7, revision.ReportID, revision.Revision)
	if err != nil {
		t.Fatalf("OpenHTML before failure: %v", err)
	}
	fixture.renderer.err = fmt.Errorf("%w: renderer rejected the document", deliveryapp.ErrRenderRejected)

	if claimed, err := fixture.worker.RunOnce(ctx); err != nil || !claimed {
		t.Fatalf("worker RunOnce = %v, %v", claimed, err)
	}
	stored, err := fixture.repo.GetRevision(ctx, revision.ReportID, revision.Revision)
	if err != nil {
		t.Fatalf("GetRevision after failure: %v", err)
	}
	if stored.PDFStatus != delivery.PDFStatusFailed || stored.PDFRef != nil ||
		stored.PDFError == "" || stored.PDFAttempts != 1 {
		t.Fatalf("failed revision = %+v", stored)
	}
	if !stored.AsOf.Equal(revision.AsOf) ||
		stored.HTMLRef == nil || revision.HTMLRef == nil || *stored.HTMLRef != *revision.HTMLRef ||
		stored.InputFingerprint != revision.InputFingerprint ||
		stored.TemplateVersion != revision.TemplateVersion {
		t.Fatalf("PDF failure changed immutable revision metadata: before=%+v after=%+v", revision, stored)
	}
	htmlAfter, _, err := fixture.svc.OpenHTML(ctx, 7, revision.ReportID, revision.Revision)
	if err != nil || !bytes.Equal(htmlBefore, htmlAfter) {
		t.Fatalf("PDF failure changed the HTML bytes: err=%v", err)
	}
	if _, _, err := fixture.svc.OpenPDF(ctx, 7, revision.ReportID, revision.Revision); platform.AsError(err).Code != "pdf_failed" {
		t.Fatalf("OpenPDF failed revision error = %v", err)
	}
	if fixture.events.count("report.pdf_failed") != 1 {
		t.Fatalf("report.pdf_failed events = %v", fixture.events.types)
	}
	dedupeKey := fmt.Sprintf("report:%d:rev:%d", revision.ReportID, revision.Revision)
	var status string
	if err := fixture.pool.QueryRow(ctx, `
		SELECT status FROM work_item WHERE dedupe_key = $1
	`, dedupeKey).Scan(&status); err != nil {
		t.Fatalf("read failed work item: %v", err)
	}
	if status != "dead" {
		t.Fatalf("work item status = %s, want dead", status)
	}
	// The ensurer must not silently revive a revision whose attempts are spent
	// or that is terminally failed (defensive list filter + attempts bound).
	if err := fixture.ensurer.ReconcileP7(ctx); err != nil {
		t.Fatalf("ReconcileP7 after failure: %v", err)
	}
	var workCount int
	if err := fixture.pool.QueryRow(ctx, `
		SELECT count(*) FROM work_item WHERE dedupe_key = $1
	`, dedupeKey).Scan(&workCount); err != nil {
		t.Fatalf("count work items: %v", err)
	}
	if workCount != 1 {
		t.Fatalf("ensurer created %d render work items, want the original 1", workCount)
	}
}

// TestReportPDFDisabledIntegration covers report.render_pdf=false: publishing
// books no task and the ensurer marks pending revisions disabled.
func TestReportPDFDisabledIntegration(t *testing.T) {
	fixture := newIntegrationPDFFixture(t)
	ctx := context.Background()
	disabledSvc := *fixture.svc
	disabledSvc.WithRenderJobs(fixture.repo, false, 3)
	// Republish through a service wired with render_pdf=false.
	report, err := disabledSvc.EnsureWaveReport(ctx, 7, 12)
	if err != nil {
		t.Fatalf("EnsureWaveReport: %v", err)
	}
	published, err := disabledSvc.Publish(ctx, report.ID, 1, "")
	if err != nil {
		t.Fatalf("Publish with render_pdf=false: %v", err)
	}
	stored, err := fixture.repo.GetRevision(ctx, published.ReportID, published.Revision)
	if err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	if stored.PDFStatus != delivery.PDFStatusDisabled {
		t.Fatalf("pdf_status = %q, want disabled", stored.PDFStatus)
	}
	var workCount int
	dedupeKey := fmt.Sprintf("report:%d:rev:%d", published.ReportID, published.Revision)
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM work_item WHERE dedupe_key = $1`, dedupeKey).Scan(&workCount); err != nil {
		t.Fatalf("count work items: %v", err)
	}
	if workCount != 0 {
		t.Fatalf("render_pdf=false booked %d work items", workCount)
	}
	if _, _, err := fixture.svc.OpenPDF(ctx, 7, published.ReportID, published.Revision); platform.AsError(err).Status != 404 {
		t.Fatalf("OpenPDF disabled error = %v, want 404", err)
	}
}
