package deliveryapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// renderEventPDFReady/renderEventPDFFailed are the frozen P7 PDF events
// (spec §7). They are published on the revision's version channel.
const (
	renderEventPDFReady  = "report.pdf_ready"
	renderEventPDFFailed = "report.pdf_failed"

	renderAuditPDFRendered = "delivery.report.pdf_rendered"
	renderAuditPDFFailed   = "delivery.report.pdf_failed"

	// renderPollInterval is the idle poll cadence of the worker.
	renderPollInterval = 2 * time.Second
)

// errRenderSettled means the revision is already done/disabled (or was settled
// by a competing write). The claimed work item is completed without rendering.
var errRenderSettled = errors.New("delivery: report PDF is already settled")

// RenderJobEnsurer is the P7 reconciler rule (agentapp.P7Ensurer): it ensures a
// report.render_pdf work item for every published revision whose PDF is still
// pending/failed, and marks pending revisions disabled when the
// report.render_pdf setting is off (spec §4.2).
type RenderJobEnsurer struct {
	jobs     RenderJobRepository
	settings SettingsReader
	limit    int
}

// NewRenderJobEnsurer wires the P7 PDF ensure rule. settings may be nil in
// tests; the frozen defaults then apply (PDF enabled, 3 attempts).
func NewRenderJobEnsurer(jobs RenderJobRepository, settings SettingsReader) *RenderJobEnsurer {
	return &RenderJobEnsurer{jobs: jobs, settings: settings, limit: renderEnsureLimit}
}

// ReconcileP7 implements the P7 reconciler hook. It is idempotent: every
// ensure is deduplicated by report:{reportID}:rev:{revision}.
func (e *RenderJobEnsurer) ReconcileP7(ctx context.Context) error {
	if e == nil || e.jobs == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	limit := e.limit
	if limit <= 0 {
		limit = renderEnsureLimit
	}
	revisions, err := e.jobs.ListRenderableRevisions(ctx, limit)
	if err != nil {
		return fmt.Errorf("list renderable report revisions: %w", err)
	}
	if !renderPDFEnabled(ctx, e.settings) {
		var errs []error
		for _, revision := range revisions {
			if revision.PDFStatus != delivery.PDFStatusPending {
				continue
			}
			if err := e.jobs.MarkPDFDisabled(ctx, revision.RevisionID); err != nil {
				errs = append(errs, fmt.Errorf("disable report PDF for revision %d: %w", revision.RevisionID, err))
			}
		}
		return errors.Join(errs...)
	}
	maxAttempts := renderMaxAttempts(ctx, e.settings)
	var errs []error
	for _, revision := range revisions {
		if revision.PDFStatus != delivery.PDFStatusPending && revision.PDFStatus != delivery.PDFStatusFailed {
			continue
		}
		if int(revision.PDFAttempts) >= maxAttempts {
			continue
		}
		dedupeKey := fmt.Sprintf("report:%d:rev:%d", revision.ReportID, revision.Revision)
		if err := e.jobs.EnsureRenderJob(ctx, revision.RevisionID, revision.ReportID, revision.VersionID, dedupeKey, maxAttempts); err != nil {
			errs = append(errs, fmt.Errorf("ensure report PDF job for revision %d: %w", revision.RevisionID, err))
		}
	}
	return errors.Join(errs...)
}

// RenderWorkerDeps wires the P7 report.render_pdf worker. Queue/Jobs/Content/
// Client are required; Events/Audit/Settings are optional (nil keeps them off
// or falls back to defaults); Clock defaults to the system clock.
type RenderWorkerDeps struct {
	Queue      RenderQueue
	Jobs       RenderJobRepository
	Content    RenderContent
	Client     RenderPDFRenderer
	Settings   SettingsReader
	Events     EventPublisher
	Audit      AuditSink
	Clock      Clock
	InstanceID string
	Logger     *slog.Logger
	// PollInterval/HeartbeatInterval are optional test/ops tuning knobs. The
	// default poll is 2s and the default heartbeat is lease/3 (at least 1s).
	PollInterval      time.Duration
	HeartbeatInterval time.Duration
}

// RenderWorker renders published report HTML to PDF through the standalone
// renderer service and writes the result into the content ledger. It owns one
// work item type only (report.render_pdf), so it can never claim P1-P4 work.
type RenderWorker struct {
	queue    RenderQueue
	jobs     RenderJobRepository
	content  RenderContent
	client   RenderPDFRenderer
	settings SettingsReader
	events   EventPublisher
	audit    AuditSink
	clock    Clock
	claimant string
	logger   *slog.Logger

	pollInterval      time.Duration
	heartbeatInterval time.Duration
}

// NewRenderWorker wires the P7 PDF worker.
func NewRenderWorker(deps RenderWorkerDeps) *RenderWorker {
	clock := deps.Clock
	if clock == nil {
		clock = systemClock{}
	}
	claimant := strings.TrimSpace(deps.InstanceID)
	if claimant == "" {
		claimant = "render-worker"
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	poll := deps.PollInterval
	if poll <= 0 {
		poll = renderPollInterval
	}
	return &RenderWorker{
		queue: deps.Queue, jobs: deps.Jobs, content: deps.Content, client: deps.Client,
		settings: deps.Settings, events: deps.Events, audit: deps.Audit, clock: clock,
		claimant: claimant, logger: logger,
		pollInterval: poll, heartbeatInterval: deps.HeartbeatInterval,
	}
}

// Run polls the queue until ctx is canceled. Transient pass errors are logged
// and retried; the work item's own retry policy owns failure backoff.
func (w *RenderWorker) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := w.RunOnce(ctx); err != nil && ctx.Err() == nil {
			w.logger.Warn("report pdf worker pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(w.pollInterval):
		}
	}
}

// RunOnce claims at most one report.render_pdf item and processes it. The bool
// reports whether an item was claimed.
func (w *RenderWorker) RunOnce(ctx context.Context) (bool, error) {
	if w == nil || w.queue == nil || w.jobs == nil || w.content == nil || w.client == nil {
		return false, errors.New("delivery: render worker is not configured")
	}
	lease := renderLease(ctx, w.settings)
	job, ok, err := w.queue.ClaimForTypes(ctx, w.claimant, []string{RenderTaskType}, lease)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}

	revisionID, reportID, payloadErr := renderPayloadIDs(job.Payload)
	if payloadErr != nil {
		return true, w.failJobWithoutRevision(ctx, job, fmt.Errorf("invalid report.render_pdf payload: %w", payloadErr))
	}

	var revision delivery.ReportRevision
	var execErr error
	execErr = w.withHeartbeat(ctx, job, func() error {
		revision, execErr = w.render(ctx, job, revisionID, reportID)
		return execErr
	})
	if execErr == nil {
		return true, w.queue.Complete(ctx, job.ID, job.Epoch)
	}
	if errors.Is(execErr, errRenderSettled) {
		w.logger.Debug("report pdf job already settled", "revision_id", revisionID)
		return true, w.queue.Complete(ctx, job.ID, job.Epoch)
	}

	failure := &renderFailure{err: execErr, retryable: true}
	var typed *renderFailure
	if errors.As(execErr, &typed) {
		failure = typed
	}
	maxAttempts := job.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = renderMaxAttempts(ctx, w.settings)
	}
	if !failure.retryable || job.Attempts >= maxAttempts {
		return true, w.failJob(ctx, job, revisionID, revision, failure)
	}
	w.logger.Warn("report pdf render failed; retrying",
		"revision_id", revisionID, "attempt", job.Attempts, "max_attempts", maxAttempts, "error", failure.err)
	return true, w.queue.Fail(ctx, job.ID, job.Epoch, failure.err, true)
}

// render moves one revision through the PDF pipeline. A returned error is
// either a *renderFailure (classified retryable/terminal), errRenderSettled or a
// transient queue/heartbeat error (treated as retryable by RunOnce).
func (w *RenderWorker) render(ctx context.Context, job RenderJob, revisionID, reportID int64) (delivery.ReportRevision, error) {
	revision, err := w.jobs.GetRevisionForRender(ctx, revisionID)
	if err != nil {
		return delivery.ReportRevision{}, &renderFailure{
			err:       fmt.Errorf("load report revision %d: %w", revisionID, err),
			retryable: false,
		}
	}
	if reportID > 0 && revision.ReportID > 0 && revision.ReportID != reportID {
		return revision, &renderFailure{
			err:       fmt.Errorf("report revision %d belongs to report %d, not %d", revisionID, revision.ReportID, reportID),
			retryable: false,
		}
	}
	switch revision.PDFStatus {
	case delivery.PDFStatusDone, delivery.PDFStatusDisabled:
		return revision, errRenderSettled
	}
	if revision.Status != delivery.ReportStatusPublished || revision.RenderStatus != "done" {
		return revision, &renderFailure{
			err:       fmt.Errorf("report revision %d is not published with rendered HTML", revisionID),
			retryable: false,
		}
	}
	if revision.HTMLRef == nil || *revision.HTMLRef <= 0 {
		return revision, &renderFailure{
			err:       fmt.Errorf("report revision %d has no HTML content ref", revisionID),
			retryable: false,
		}
	}
	if err := w.jobs.MarkPDFRunning(ctx, revisionID); err != nil {
		if errors.Is(err, delivery.ErrStateConflict) {
			return revision, errRenderSettled
		}
		if errors.Is(err, ErrReportNotFound) {
			return revision, &renderFailure{
				err:       fmt.Errorf("report revision %d no longer exists: %w", revisionID, err),
				retryable: false,
			}
		}
		return revision, fmt.Errorf("mark report PDF running: %w", err)
	}
	html, err := w.content.GetReportHTML(ctx, *revision.HTMLRef)
	if err != nil {
		return revision, &renderFailure{
			err:       fmt.Errorf("load frozen report HTML: %w", err),
			retryable: !isTerminalRenderContentError(err),
		}
	}
	pdf, err := w.client.RenderPDF(ctx, html)
	if err != nil {
		return revision, &renderFailure{
			err:       fmt.Errorf("render report PDF: %w", err),
			retryable: errors.Is(err, ErrRenderUnavailable),
		}
	}
	if len(pdf) == 0 {
		return revision, &renderFailure{err: fmt.Errorf("renderer returned an empty PDF"), retryable: false}
	}
	ref, err := w.content.PutReportPDF(ctx, revisionID, pdf)
	if err != nil {
		return revision, &renderFailure{
			err:       fmt.Errorf("store report PDF: %w", err),
			retryable: !isTerminalRenderContentError(err),
		}
	}
	if err := w.jobs.MarkPDFRendered(ctx, revisionID, ref, w.clock.Now()); err != nil {
		if errors.Is(err, delivery.ErrStateConflict) {
			return revision, errRenderSettled
		}
		return revision, fmt.Errorf("mark report PDF rendered: %w", err)
	}
	w.publishPDFReady(ctx, job, revision, ref)
	return revision, nil
}

// withHeartbeat renews the job lease while run executes and stops the renewal
// before returning.
func (w *RenderWorker) withHeartbeat(ctx context.Context, job RenderJob, run func() error) error {
	interval := w.heartbeatInterval
	if interval <= 0 {
		interval = renderLease(ctx, w.settings) / 3
		if interval < time.Second {
			interval = time.Second
		}
	}
	hbCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-ticker.C:
				if err := w.queue.Heartbeat(hbCtx, job.ID, job.Epoch); err != nil {
					w.logger.Warn("report pdf work item heartbeat failed", "job_id", job.ID, "error", err)
				}
			}
		}
	}()
	err := run()
	cancel()
	wg.Wait()
	return err
}

// failJob records a terminal PDF failure and fails the work item
// non-retryably. The revision event/audit is emitted only when the metadata
// write actually moved the revision into failed.
func (w *RenderWorker) failJob(ctx context.Context, job RenderJob, revisionID int64, revision delivery.ReportRevision, failure *renderFailure) error {
	queueErr := w.queue.Fail(ctx, job.ID, job.Epoch, failure.err, false)
	if revisionID <= 0 {
		return queueErr
	}
	message := boundedPDFError(failure.err)
	if err := w.jobs.MarkPDFFailed(ctx, revisionID, message, int32(job.Attempts), w.clock.Now()); err != nil {
		if errors.Is(err, delivery.ErrStateConflict) || errors.Is(err, ErrReportNotFound) {
			// A completed/disabled revision is never overwritten and emits no
			// failure event.
			return queueErr
		}
		return errors.Join(queueErr, fmt.Errorf("mark report PDF failed: %w", err))
	}
	if revision.ID == 0 {
		if loaded, err := w.jobs.GetRevisionForRender(ctx, revisionID); err == nil {
			revision = loaded
		}
	}
	w.publishPDFFailed(ctx, job, revision, job.Attempts, failure.err)
	return queueErr
}

// failJobWithoutRevision settles a job whose payload cannot identify a
// revision: there is no row to fence, so only the queue item fails.
func (w *RenderWorker) failJobWithoutRevision(ctx context.Context, job RenderJob, cause error) error {
	w.logger.Warn("report pdf job has an invalid payload", "job_id", job.ID, "error", cause)
	return w.queue.Fail(ctx, job.ID, job.Epoch, cause, false)
}

func (w *RenderWorker) publishPDFReady(ctx context.Context, job RenderJob, revision delivery.ReportRevision, ref ReportContentRef) {
	payload := map[string]any{
		"report_id":          platformid.Format(revision.ReportID),
		"report_revision_id": platformid.Format(revision.ID),
		"revision":           revision.Revision,
		"pdf_ref":            platformid.Format(ref.RefID),
		"size_bytes":         ref.Size,
	}
	if w.events != nil {
		_ = w.events.Publish(ctx, renderEventPDFReady, job.VersionID, payload)
	}
	if w.audit != nil {
		_ = w.audit.Write(ctx, 0, renderAuditPDFRendered, "report_revision", platformid.Format(revision.ID), map[string]any{
			"report_id":  platformid.Format(revision.ReportID),
			"revision":   revision.Revision,
			"pdf_ref":    platformid.Format(ref.RefID),
			"size_bytes": ref.Size,
		})
	}
}

func (w *RenderWorker) publishPDFFailed(ctx context.Context, job RenderJob, revision delivery.ReportRevision, attempts int, cause error) {
	message := boundedPDFError(cause)
	payload := map[string]any{
		"report_id":          platformid.Format(revision.ReportID),
		"report_revision_id": platformid.Format(revision.ID),
		"revision":           revision.Revision,
		"attempts":           int32(attempts),
		"error":              message,
	}
	if w.events != nil {
		_ = w.events.Publish(ctx, renderEventPDFFailed, job.VersionID, payload)
	}
	if w.audit != nil {
		_ = w.audit.Write(ctx, 0, renderAuditPDFFailed, "report_revision", platformid.Format(revision.ID), map[string]any{
			"report_id": platformid.Format(revision.ReportID),
			"revision":  revision.Revision,
			"attempts":  int32(attempts),
			"error":     message,
		})
	}
}

// renderFailure is one classified render error.
type renderFailure struct {
	err       error
	retryable bool
}

func (f *renderFailure) Error() string { return f.err.Error() }
func (f *renderFailure) Unwrap() error { return f.err }

// renderPayloadIDs parses the frozen {report_id, report_revision_id} payload of
// a report.render_pdf work item.
func renderPayloadIDs(payload json.RawMessage) (revisionID, reportID int64, err error) {
	var raw struct {
		ReportID         string `json:"report_id"`
		ReportRevisionID string `json:"report_revision_id"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return 0, 0, fmt.Errorf("payload must be a JSON object: %w", err)
	}
	revisionID, err = platformid.Parse(strings.TrimSpace(raw.ReportRevisionID))
	if err != nil {
		return 0, 0, fmt.Errorf("report_revision_id must be a positive decimal ID: %w", err)
	}
	reportID, err = platformid.Parse(strings.TrimSpace(raw.ReportID))
	if err != nil {
		return 0, 0, fmt.Errorf("report_id must be a positive decimal ID: %w", err)
	}
	return revisionID, reportID, nil
}

// boundedPDFError keeps the persisted pdf_error within the schema CHECK.
func boundedPDFError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	runes := []rune(message)
	if len(runes) > renderMaxPDFError {
		return string(runes[:renderMaxPDFError])
	}
	return message
}

// isTerminalRenderContentError reports a content-ledger error that a retry
// cannot heal (missing/corrupt content or a rejected argument). Any other error
// (database/network) is treated as transient.
func isTerminalRenderContentError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrReportContentNotFound) || errors.Is(err, ErrReportContentCorrupt) {
		return true
	}
	apiErr := platform.AsError(err)
	switch apiErr.Status {
	case 400, 404, 409, 422:
		return true
	default:
		return false
	}
}
