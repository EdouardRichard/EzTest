package deliveryapp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// Report event types (spec §4.3).
const (
	reportEventPublished = "report.published"
	reportEventStale     = "report.stale"
)

// Report lifecycle constants. template_version falls back to "1" (the frozen
// P4 built-in template) when the renderer does not report a version; renderers
// implementing delivery.ReportTemplateVersioner supply the actual version.
const (
	reportTemplateVersion = "1"
	reportRenderPending   = "pending"
	reportRenderDone      = "done"
	reportRenderFailed    = "failed"
)

// ReportService owns the report lifecycle: ensuring report rows and generation
// tasks, publishing rendered HTML into the content ledger, stale detection and
// verified reads (spec §6.3–§6.5).
type ReportService struct {
	repo     ReportRepository
	renderer delivery.ReportRenderer
	content  ContentStore
	data     ReportDataReader
	events   EventPublisher
	audit    AuditSink
	clock    Clock
	tasks    TaskEnsurer
	sections delivery.SectionOptions

	// P6 cross-version read ports. They stay optional so the P4 constructor
	// and its tests keep compiling; WithGlobalReader attaches them.
	global GlobalReportReader
	titles VersionTitleReader

	// P7 PDF pipeline (spec §4.2). Attached with WithRenderJobs; nil keeps the
	// P4 HTML-only lifecycle so older wirings keep compiling.
	renderJobs     RenderJobRepository
	renderPDF      bool
	renderAttempts int
}

// NewReportService wires the report use cases. events, audit and tasks may be
// nil in unit tests; the clock defaults to the system clock.
func NewReportService(
	repo ReportRepository,
	renderer delivery.ReportRenderer,
	content ContentStore,
	data ReportDataReader,
	events EventPublisher,
	audit AuditSink,
	clock Clock,
	tasks TaskEnsurer,
	opts delivery.SectionOptions,
) *ReportService {
	if clock == nil {
		clock = systemClock{}
	}
	return &ReportService{
		repo:     repo,
		renderer: renderer,
		content:  content,
		data:     data,
		events:   events,
		audit:    audit,
		clock:    clock,
		tasks:    tasks,
		sections: opts,
	}
}

// systemClock is the default Clock.
type systemClock struct{}

// Now implements Clock.
func (systemClock) Now() time.Time { return time.Now().UTC() }

// WithRenderJobs attaches the P7 PDF render-job pipeline. renderEnabled mirrors
// report.render_pdf (when false, Publish marks new revisions disabled instead of
// booking a render job) and maxAttempts mirrors report.render_max_attempts. It
// returns the service so the composition root can chain it.
func (s *ReportService) WithRenderJobs(jobs RenderJobRepository, renderEnabled bool, maxAttempts int) *ReportService {
	s.renderJobs = jobs
	s.renderPDF = renderEnabled
	if maxAttempts < MinRenderAttempts || maxAttempts > MaxRenderAttempts {
		maxAttempts = DefaultRenderMaxAttempts
	}
	s.renderAttempts = maxAttempts
	return s
}

// EnsureWaveReport makes sure a wave report exists for a terminal wave and has
// an in-flight draft revision with a report.wave work item. It is idempotent:
// a published report is returned untouched; a pending draft re-ensures the
// same task; a failed draft gets the next revision (spec §6.3/§6.5).
func (s *ReportService) EnsureWaveReport(ctx context.Context, versionID, waveID int64) (delivery.Report, error) {
	if versionID <= 0 {
		return delivery.Report{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if waveID <= 0 {
		return delivery.Report{}, platform.Invalid("wave_id", "must be a positive decimal ID")
	}
	kind := delivery.ReportKindWave
	reports, err := s.repo.ListReports(ctx, versionID, ReportFilter{Kind: &kind, Limit: 100})
	if err != nil {
		return delivery.Report{}, mapReportError(err)
	}
	for _, report := range reports {
		if report.WaveID != nil && *report.WaveID == waveID {
			return s.ensureGeneration(ctx, report)
		}
	}
	report, err := s.repo.CreateReport(ctx, delivery.Report{
		VersionID: versionID,
		WaveID:    &waveID,
		Kind:      delivery.ReportKindWave,
		Status:    delivery.ReportStatusDraft,
	})
	if err != nil {
		return delivery.Report{}, mapReportError(err)
	}
	return s.ensureGeneration(ctx, report)
}

// EnsureVersionReport makes sure one version report exists and has an
// in-flight draft revision with a report.version work item. The reconciler
// only calls it when the §7.4 conditions hold (all requirements closed or
// obsolete, no active wave, latest report missing or stale).
func (s *ReportService) EnsureVersionReport(ctx context.Context, versionID int64) (delivery.Report, error) {
	if versionID <= 0 {
		return delivery.Report{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	kind := delivery.ReportKindVersion
	reports, err := s.repo.ListReports(ctx, versionID, ReportFilter{Kind: &kind, Limit: 100})
	if err != nil {
		return delivery.Report{}, mapReportError(err)
	}
	if len(reports) > 0 {
		return s.ensureGeneration(ctx, reports[0])
	}
	report, err := s.repo.CreateReport(ctx, delivery.Report{
		VersionID: versionID,
		Kind:      delivery.ReportKindVersion,
		Status:    delivery.ReportStatusDraft,
	})
	if err != nil {
		return delivery.Report{}, mapReportError(err)
	}
	return s.ensureGeneration(ctx, report)
}

// EnsureReports is the reconciler entry point (spec §4.4/§7.4): it creates the
// missing terminal-wave reports and, when every requirement is closed or
// obsolete and no wave is active, marks changed reports stale and ensures the
// version report (regenerating it when stale). Active-intent checks are owned
// by the reader/wiring layer; this method consumes the version snapshot.
func (s *ReportService) EnsureReports(ctx context.Context, versionID int64) error {
	if versionID <= 0 {
		return platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if s.data == nil {
		return reportServiceNotConfigured("report data reader")
	}
	snapshot, err := s.data.VersionSnapshot(ctx, versionID)
	if err != nil {
		return err
	}
	existing, err := s.repo.ListReports(ctx, versionID, ReportFilter{Limit: 100})
	if err != nil {
		return mapReportError(err)
	}
	waveReports := make(map[int64]delivery.Report)
	for _, report := range existing {
		if report.Kind == delivery.ReportKindWave && report.WaveID != nil {
			waveReports[*report.WaveID] = report
		}
	}

	for _, wave := range snapshot.Waves {
		if !terminalWaveStatus(wave.Status) {
			continue
		}
		if report, ok := waveReports[wave.ID]; ok && reportHasPublishedRevision(report) {
			continue
		}
		if _, err := s.EnsureWaveReport(ctx, versionID, wave.ID); err != nil {
			return err
		}
	}
	// The §7.4 condition also excludes active execution intents and open human
	// requests. They are outside the report snapshot, so a reader that can
	// observe them implements ReportTriggerReader.
	if trigger, ok := s.data.(ReportTriggerReader); ok {
		ready, err := trigger.VersionReportReady(ctx, versionID)
		if err != nil {
			return err
		}
		if !ready {
			return nil
		}
	}
	if !versionReportDue(snapshot) {
		return nil
	}
	if _, err := s.MarkStaleIfChanged(ctx, versionID); err != nil {
		return err
	}
	refreshKind := delivery.ReportKindVersion
	refreshed, err := s.repo.ListReports(ctx, versionID, ReportFilter{Kind: &refreshKind, Limit: 1})
	if err != nil {
		return mapReportError(err)
	}
	if len(refreshed) == 0 {
		_, err := s.EnsureVersionReport(ctx, versionID)
		return err
	}
	current := refreshed[0]
	switch current.Status {
	case delivery.ReportStatusStale:
		_, err := s.Regenerate(ctx, versionID, current.ID)
		return err
	case delivery.ReportStatusDraft:
		_, err := s.EnsureVersionReport(ctx, versionID)
		return err
	default:
		return nil
	}
}

// Regenerate allocates the next revision of one report and ensures its
// report.wave/report.version task. An in-flight pending draft is idempotent
// (the same revision/task is returned); a published or failed revision gets
// the next number. Old revisions are never deleted (spec §6.5).
func (s *ReportService) Regenerate(ctx context.Context, versionID, reportID int64) (int32, error) {
	if versionID <= 0 || reportID <= 0 {
		return 0, platform.Invalid("report_id", "must be a positive decimal ID")
	}
	report, err := s.repo.GetReport(ctx, versionID, reportID)
	if err != nil {
		return 0, mapReportError(err)
	}
	revisions, err := s.repo.ListRevisions(ctx, reportID)
	if err != nil {
		return 0, mapReportError(err)
	}
	if len(revisions) > 0 {
		latest := revisions[0]
		if latest.Status == delivery.ReportStatusDraft && latest.RenderStatus != reportRenderFailed {
			if err := s.ensureTask(ctx, reportID, latest.Revision); err != nil {
				return 0, err
			}
			return latest.Revision, nil
		}
	}
	revision, err := s.createNextDraft(ctx, report)
	if err != nil {
		return 0, err
	}
	if err := s.ensureTask(ctx, reportID, revision.Revision); err != nil {
		return 0, err
	}
	return revision.Revision, nil
}

// Publish renders one draft revision from the current deterministic snapshot,
// writes the HTML into the content ledger, then finalizes the revision/report
// atomically and appends report.published + the audit entry. A failed render
// or content write leaves the revision visible as render_status='failed' and
// never publishes. An already-published revision is returned idempotently
// (the wave.report_id backfill is still healed). Old revisions are immutable.
func (s *ReportService) Publish(ctx context.Context, reportID int64, revision int32, narrative string) (delivery.ReportRevision, error) {
	if reportID <= 0 {
		return delivery.ReportRevision{}, platform.Invalid("report_id", "must be a positive decimal ID")
	}
	if revision <= 0 {
		return delivery.ReportRevision{}, platform.Invalid("revision", "must be a positive integer")
	}
	report, err := s.repo.GetReportByID(ctx, reportID)
	if err != nil {
		return delivery.ReportRevision{}, mapReportError(err)
	}
	current, err := s.repo.GetRevision(ctx, reportID, revision)
	if err != nil {
		return delivery.ReportRevision{}, mapReportError(err)
	}
	if current.Status == delivery.ReportStatusPublished {
		if err := s.ensureWaveLink(ctx, report); err != nil {
			return current, err
		}
		return current, nil
	}
	if current.Status != delivery.ReportStatusDraft {
		return delivery.ReportRevision{}, platform.StateConflict("report revision is not a draft")
	}
	if s.renderer == nil || s.content == nil {
		return delivery.ReportRevision{}, reportServiceNotConfigured("report renderer or content store")
	}

	asOf := s.clock.Now().UTC()
	snapshot, err := s.snapshotFor(ctx, report)
	if err != nil {
		s.failRevision(ctx, current.ID, err)
		return delivery.ReportRevision{}, err
	}
	snapshot.GeneratedAt = asOf
	fingerprint := delivery.Fingerprint(snapshot)
	if fingerprint == "" {
		err := fmt.Errorf("%w: report input fingerprint is empty", delivery.ErrInvalidArgument)
		s.failRevision(ctx, current.ID, err)
		return delivery.ReportRevision{}, err
	}
	// Stamp the v2 appendix metadata from the authoritative report/revision
	// rows. These fields never participate in the fingerprint.
	snapshot.ReportID = report.ID
	snapshot.ReportRevision = revision
	snapshot.InputFingerprint = fingerprint

	html, err := s.renderer.Render(ctx, snapshot, narrative, s.sections)
	if err != nil {
		err = fmt.Errorf("render report %d revision %d: %w", reportID, revision, err)
		s.failRevision(ctx, current.ID, err)
		return delivery.ReportRevision{}, err
	}
	contentRefID, sha, size, err := s.content.PutReportHTML(ctx, current.ID, html)
	if err != nil {
		s.failRevision(ctx, current.ID, err)
		return delivery.ReportRevision{}, err
	}
	if !validReportSHA(sha) || size != int64(len(html)) {
		err := fmt.Errorf("%w: content store returned an inconsistent sha/size", ErrReportContentCorrupt)
		s.failRevision(ctx, current.ID, err)
		return delivery.ReportRevision{}, err
	}
	if err := s.repo.InsertRefs(ctx, current.ID, reportRefsForSnapshot(report, snapshot)); err != nil {
		s.failRevision(ctx, current.ID, err)
		return delivery.ReportRevision{}, mapReportError(err)
	}
	if err := s.repo.FinalizePublished(ctx, reportID, revision, contentRefID, fingerprint); err != nil {
		s.failRevision(ctx, current.ID, err)
		return delivery.ReportRevision{}, mapReportError(err)
	}
	published, err := s.repo.GetRevision(ctx, reportID, revision)
	if err != nil {
		return delivery.ReportRevision{}, mapReportError(err)
	}
	if err := s.ensureWaveLink(ctx, report); err != nil {
		return published, err
	}
	s.bookPDF(ctx, report, published)
	s.publishPublishedEvent(ctx, report, published)
	s.recordPublishedAudit(ctx, report, published)
	return published, nil
}

// bookPDF books the P7 PDF work for one published revision (spec §4.2). It is
// best-effort: HTML publishing never waits for PDF, and the reconciler backfills
// any ensure that failed here. With report.render_pdf off the revision is marked
// disabled instead of booking a task.
func (s *ReportService) bookPDF(ctx context.Context, report delivery.Report, revision delivery.ReportRevision) {
	if s == nil || s.renderJobs == nil {
		return
	}
	if !s.renderPDF {
		_ = s.renderJobs.MarkPDFDisabled(ctx, revision.ID)
		return
	}
	attempts := s.renderAttempts
	if attempts < MinRenderAttempts || attempts > MaxRenderAttempts {
		attempts = DefaultRenderMaxAttempts
	}
	dedupeKey := fmt.Sprintf("report:%d:rev:%d", report.ID, revision.Revision)
	_ = s.renderJobs.EnsureRenderJob(ctx, revision.ID, report.ID, report.VersionID, dedupeKey, attempts)
}

// MarkStaleIfChanged recomputes the current input fingerprint of every
// published report of a version and marks the report stale when it differs.
// Exactly one report.stale event is emitted per report while it stays stale
// (spec §6.5). It reports whether any report changed.
func (s *ReportService) MarkStaleIfChanged(ctx context.Context, versionID int64) (bool, error) {
	if versionID <= 0 {
		return false, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if s.data == nil {
		return false, reportServiceNotConfigured("report data reader")
	}
	reports, err := s.repo.ListReports(ctx, versionID, ReportFilter{Limit: 100})
	if err != nil {
		return false, mapReportError(err)
	}
	changedAny := false
	for _, report := range reports {
		if report.Status != delivery.ReportStatusPublished {
			continue
		}
		snapshot, err := s.snapshotFor(ctx, report)
		if err != nil {
			return changedAny, err
		}
		fingerprint := delivery.Fingerprint(snapshot)
		if fingerprint == "" {
			return changedAny, fmt.Errorf("%w: report input fingerprint is empty", delivery.ErrInvalidArgument)
		}
		if fingerprint == report.InputFingerprint {
			continue
		}
		changed, err := s.repo.MarkStale(ctx, report.ID)
		if err != nil {
			return changedAny, mapReportError(err)
		}
		if !changed {
			continue
		}
		changedAny = true
		s.publishStaleEvent(ctx, report)
	}
	return changedAny, nil
}

// List returns one descending-ID page of a version's reports, optionally
// filtered by kind.
func (s *ReportService) List(ctx context.Context, versionID int64, kind *delivery.ReportKind) ([]delivery.Report, error) {
	if versionID <= 0 {
		return nil, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if kind != nil && !kind.Valid() {
		return nil, platform.Invalid("kind", "must be wave or version")
	}
	reports, err := s.repo.ListReports(ctx, versionID, ReportFilter{Kind: kind, Limit: 100})
	if err != nil {
		return nil, mapReportError(err)
	}
	return reports, nil
}

// Get returns one report with its revisions (newest first) and logical refs.
func (s *ReportService) Get(ctx context.Context, versionID, reportID int64) (ReportDetail, error) {
	if versionID <= 0 || reportID <= 0 {
		return ReportDetail{}, platform.Invalid("report_id", "must be a positive decimal ID")
	}
	report, err := s.repo.GetReport(ctx, versionID, reportID)
	if err != nil {
		return ReportDetail{}, mapReportError(err)
	}
	revisions, err := s.repo.ListRevisions(ctx, reportID)
	if err != nil {
		return ReportDetail{}, mapReportError(err)
	}
	refs, err := s.repo.ListRefs(ctx, reportID)
	if err != nil {
		return ReportDetail{}, mapReportError(err)
	}
	return ReportDetail{Report: report, Revisions: revisions, Refs: refs}, nil
}

// OpenHTML returns the verified HTML bytes of one published revision. The
// report must belong to the path version and the revision to the report; the
// revision must be published with render_status='done'. The content adapter
// re-validates owner/role/sha/size. Missing rows are 404; an integrity failure
// is 409 state_conflict with details.reason=report_content_corrupt (spec §6.4).
func (s *ReportService) OpenHTML(ctx context.Context, versionID, reportID int64, revision int32) ([]byte, delivery.ReportRevision, error) {
	if versionID <= 0 || reportID <= 0 || revision <= 0 {
		return nil, delivery.ReportRevision{}, platform.Invalid("report_id", "must be a positive decimal ID and revision a positive integer")
	}
	if _, err := s.repo.GetReport(ctx, versionID, reportID); err != nil {
		return nil, delivery.ReportRevision{}, mapReportError(err)
	}
	stored, err := s.repo.GetRevision(ctx, reportID, revision)
	if err != nil {
		return nil, delivery.ReportRevision{}, mapReportError(err)
	}
	if stored.Status != delivery.ReportStatusPublished || stored.RenderStatus != reportRenderDone || stored.HTMLRef == nil {
		return nil, stored, platform.NotFound("report revision")
	}
	if s.content == nil {
		return nil, stored, reportServiceNotConfigured("content store")
	}
	html, sha, size, err := s.content.OpenReportHTML(ctx, *stored.HTMLRef)
	if err != nil {
		switch {
		case errors.Is(err, ErrReportContentNotFound):
			return nil, stored, platform.NotFound("report content")
		case errors.Is(err, ErrReportContentCorrupt):
			return nil, stored, reportContentCorrupt()
		default:
			if apiErr := platform.AsError(err); apiErr.Status == 404 {
				return nil, stored, platform.NotFound("report content")
			}
			return nil, stored, err
		}
	}
	if !validReportSHA(sha) || size != int64(len(html)) {
		return nil, stored, reportContentCorrupt()
	}
	return html, stored, nil
}

// OpenPDF returns the verified PDF bytes of one published revision (spec §6.4).
// The report must belong to the path version and the revision to the report. A
// revision whose PDF is pending/running is 409 pdf_not_ready; a failed render is
// 409 pdf_failed; a disabled or missing PDF is 404. The content adapter
// re-validates owner/role/sha/size and the service additionally checks the
// %PDF- magic.
func (s *ReportService) OpenPDF(ctx context.Context, versionID, reportID int64, revision int32) ([]byte, delivery.ReportRevision, error) {
	if versionID <= 0 || reportID <= 0 || revision <= 0 {
		return nil, delivery.ReportRevision{}, platform.Invalid("report_id", "must be a positive decimal ID and revision a positive integer")
	}
	if _, err := s.repo.GetReport(ctx, versionID, reportID); err != nil {
		return nil, delivery.ReportRevision{}, mapReportError(err)
	}
	stored, err := s.repo.GetRevision(ctx, reportID, revision)
	if err != nil {
		return nil, delivery.ReportRevision{}, mapReportError(err)
	}
	if stored.Status != delivery.ReportStatusPublished || stored.RenderStatus != reportRenderDone {
		return nil, stored, platform.NotFound("report revision")
	}
	switch stored.PDFStatus {
	case delivery.PDFStatusDone:
		if stored.PDFRef == nil {
			return nil, stored, platform.NotFound("report PDF")
		}
	case delivery.PDFStatusPending, delivery.PDFStatusRunning:
		return nil, stored, pdfNotReady()
	case delivery.PDFStatusFailed:
		return nil, stored, pdfFailed(stored.PDFError)
	default:
		return nil, stored, platform.NotFound("report PDF")
	}
	if s.content == nil {
		return nil, stored, reportServiceNotConfigured("content store")
	}
	pdf, sha, size, err := s.content.OpenReportPDF(ctx, *stored.PDFRef)
	if err != nil {
		switch {
		case errors.Is(err, ErrReportContentNotFound):
			return nil, stored, platform.NotFound("report PDF content")
		case errors.Is(err, ErrReportContentCorrupt):
			return nil, stored, reportContentCorrupt()
		default:
			if apiErr := platform.AsError(err); apiErr.Status == 404 {
				return nil, stored, platform.NotFound("report PDF content")
			}
			return nil, stored, err
		}
	}
	if !validReportSHA(sha) || size != int64(len(pdf)) || !validPDFHeader(pdf) {
		return nil, stored, reportContentCorrupt()
	}
	return pdf, stored, nil
}

// ensureGeneration returns a report with an in-flight draft revision: it
// creates the first revision when none exists, re-ensures the task of a
// pending draft, and allocates the next revision after a failed one.
func (s *ReportService) ensureGeneration(ctx context.Context, report delivery.Report) (delivery.Report, error) {
	revisions, err := s.repo.ListRevisions(ctx, report.ID)
	if err != nil {
		return delivery.Report{}, mapReportError(err)
	}
	if len(revisions) == 0 {
		revision, err := s.createNextDraft(ctx, report)
		if err != nil {
			return delivery.Report{}, err
		}
		if err := s.ensureTask(ctx, report.ID, revision.Revision); err != nil {
			return delivery.Report{}, err
		}
		return report, nil
	}
	latest := revisions[0]
	switch {
	case latest.Status == delivery.ReportStatusPublished:
		return report, nil
	case latest.Status == delivery.ReportStatusDraft && latest.RenderStatus != reportRenderFailed:
		if err := s.ensureTask(ctx, report.ID, latest.Revision); err != nil {
			return delivery.Report{}, err
		}
		return report, nil
	default:
		revision, err := s.createNextDraft(ctx, report)
		if err != nil {
			return delivery.Report{}, err
		}
		if err := s.ensureTask(ctx, report.ID, revision.Revision); err != nil {
			return delivery.Report{}, err
		}
		return report, nil
	}
}

// createNextDraft computes the current snapshot fingerprint and inserts the
// next draft revision (revision = max+1) with as_of=now.
func (s *ReportService) createNextDraft(ctx context.Context, report delivery.Report) (delivery.ReportRevision, error) {
	snapshot, err := s.snapshotFor(ctx, report)
	if err != nil {
		return delivery.ReportRevision{}, err
	}
	asOf := s.clock.Now().UTC()
	snapshot.GeneratedAt = asOf
	fingerprint := delivery.Fingerprint(snapshot)
	if fingerprint == "" {
		return delivery.ReportRevision{}, fmt.Errorf("%w: report input fingerprint is empty", delivery.ErrInvalidArgument)
	}
	next, err := s.repo.NextRevision(ctx, report.ID)
	if err != nil {
		return delivery.ReportRevision{}, mapReportError(err)
	}
	revision, err := s.repo.CreateRevision(ctx, delivery.ReportRevision{
		ReportID:         report.ID,
		Revision:         next,
		Status:           delivery.ReportStatusDraft,
		InputFingerprint: fingerprint,
		AsOf:             asOf,
		TemplateVersion:  s.templateVersion(),
		RenderStatus:     reportRenderPending,
	})
	if err != nil {
		return delivery.ReportRevision{}, mapReportError(err)
	}
	return revision, nil
}

// templateVersion resolves the built-in template version new revisions record:
// the renderer is the source of truth (P7-D-03); a renderer that does not
// report one keeps the frozen P4 version.
func (s *ReportService) templateVersion() string {
	if versioner, ok := s.renderer.(delivery.ReportTemplateVersioner); ok {
		if version := versioner.TemplateVersion(); version != "" {
			return version
		}
	}
	return reportTemplateVersion
}

// snapshotFor reads and caps the deterministic report input for one report and
// stamps the report's authoritative scope onto it (a mis-scoped reader can
// never cross versions or waves).
func (s *ReportService) snapshotFor(ctx context.Context, report delivery.Report) (delivery.ReportSnapshot, error) {
	if s.data == nil {
		return delivery.ReportSnapshot{}, reportServiceNotConfigured("report data reader")
	}
	var snapshot delivery.ReportSnapshot
	var err error
	switch report.Kind {
	case delivery.ReportKindWave:
		if report.WaveID == nil || *report.WaveID <= 0 {
			return delivery.ReportSnapshot{}, fmt.Errorf("%w: wave report has no wave", delivery.ErrInvalidArgument)
		}
		snapshot, err = s.data.WaveSnapshot(ctx, report.VersionID, *report.WaveID)
	case delivery.ReportKindVersion:
		snapshot, err = s.data.VersionSnapshot(ctx, report.VersionID)
	default:
		return delivery.ReportSnapshot{}, fmt.Errorf("%w: unknown report kind %q", delivery.ErrInvalidArgument, report.Kind)
	}
	if err != nil {
		return delivery.ReportSnapshot{}, err
	}
	snapshot = delivery.CapReportSnapshot(snapshot)
	snapshot.Kind = report.Kind
	snapshot.VersionID = report.VersionID
	if report.Kind == delivery.ReportKindWave {
		snapshot.WaveID = report.WaveID
	} else {
		snapshot.WaveID = nil
	}
	return snapshot, nil
}

// failRevision best-effort records a failed render. The revision stays a draft
// for audit and the next retry allocates a new revision (spec §6.3).
func (s *ReportService) failRevision(ctx context.Context, revisionID int64, cause error) {
	if s.repo == nil {
		return
	}
	_ = s.repo.MarkRenderMetadata(ctx, revisionID, reportRenderFailed, cause.Error())
}

// ensureWaveLink backfills run_wave.report_id for a wave report.
func (s *ReportService) ensureWaveLink(ctx context.Context, report delivery.Report) error {
	if report.Kind != delivery.ReportKindWave {
		return nil
	}
	if report.WaveID == nil || *report.WaveID <= 0 {
		return fmt.Errorf("%w: wave report has no wave", delivery.ErrInvalidArgument)
	}
	if err := s.repo.SetWaveReportID(ctx, report.VersionID, *report.WaveID, report.ID); err != nil {
		return mapReportError(err)
	}
	return nil
}

// ensureTask ensures the report.wave/report.version work item for a revision.
func (s *ReportService) ensureTask(ctx context.Context, reportID int64, revision int32) error {
	if s.tasks == nil {
		return reportServiceNotConfigured("task ensurer")
	}
	return s.tasks.EnsureReportTask(ctx, reportID, revision)
}

// publishPublishedEvent emits report.published on the version channel. Event
// failures are best-effort and never roll back the publish (spec §4.3).
func (s *ReportService) publishPublishedEvent(ctx context.Context, report delivery.Report, revision delivery.ReportRevision) {
	if s.events == nil {
		return
	}
	payload := map[string]any{
		"report_id":          platformid.Format(report.ID),
		"report_revision_id": platformid.Format(revision.ID),
		"kind":               string(report.Kind),
		"revision":           revision.Revision,
		"as_of":              revision.AsOf.UTC().Format(time.RFC3339Nano),
	}
	if report.WaveID != nil {
		payload["wave_id"] = platformid.Format(*report.WaveID)
	}
	if revision.HTMLRef != nil {
		payload["html_ref"] = platformid.Format(*revision.HTMLRef)
	}
	_ = s.events.Publish(ctx, reportEventPublished, report.VersionID, payload)
}

// recordPublishedAudit writes the best-effort publish audit entry.
func (s *ReportService) recordPublishedAudit(ctx context.Context, report delivery.Report, revision delivery.ReportRevision) {
	if s.audit == nil {
		return
	}
	metadata := map[string]any{
		"report_revision_id": platformid.Format(revision.ID),
		"revision":           revision.Revision,
		"kind":               string(report.Kind),
	}
	if revision.HTMLRef != nil {
		metadata["html_ref"] = platformid.Format(*revision.HTMLRef)
	}
	_ = s.audit.Write(ctx, 0, reportEventPublished, "report", platformid.Format(report.ID), metadata)
}

// publishStaleEvent emits report.stale for one report with the current
// published revision number.
func (s *ReportService) publishStaleEvent(ctx context.Context, report delivery.Report) {
	if s.events == nil {
		return
	}
	currentRevision := int32(0)
	if report.CurrentRevisionID != nil {
		revisions, err := s.repo.ListRevisions(ctx, report.ID)
		if err == nil {
			for _, revision := range revisions {
				if revision.ID == *report.CurrentRevisionID {
					currentRevision = revision.Revision
					break
				}
			}
		}
	}
	payload := map[string]any{
		"report_id":        platformid.Format(report.ID),
		"kind":             string(report.Kind),
		"current_revision": currentRevision,
		"reason":           "input_changed",
	}
	if report.WaveID != nil {
		payload["wave_id"] = platformid.Format(*report.WaveID)
	}
	_ = s.events.Publish(ctx, reportEventStale, report.VersionID, payload)
}

// reportRefsForSnapshot derives the append-only logical refs of one revision:
// requirements, waves, case versions, case runs, issues and version SUTs. The
// list is deduplicated and sorted by (kind, ref_id) for replay determinism.
func reportRefsForSnapshot(report delivery.Report, snapshot delivery.ReportSnapshot) []delivery.ReportRef {
	seen := make(map[string]bool)
	refs := make([]delivery.ReportRef, 0)
	add := func(kind string, refID int64) {
		if refID <= 0 {
			return
		}
		key := fmt.Sprintf("%s:%d", kind, refID)
		if seen[key] {
			return
		}
		seen[key] = true
		refs = append(refs, delivery.ReportRef{Kind: kind, RefID: refID})
	}
	if report.Kind == delivery.ReportKindWave && report.WaveID != nil {
		add("wave", *report.WaveID)
	}
	for _, requirement := range snapshot.Requirements {
		add("requirement", requirement.ID)
	}
	for _, wave := range snapshot.Waves {
		add("wave", wave.ID)
	}
	for _, run := range snapshot.CaseRuns {
		add("case_version", run.CaseVersionID)
		add("case_run", run.ID)
		if run.WaveID > 0 {
			add("wave", run.WaveID)
		}
		if run.VersionSUTID != nil {
			add("version_sut", *run.VersionSUTID)
		}
		for _, versionSUTID := range run.VersionSUTIDs {
			add("version_sut", versionSUTID)
		}
	}
	for _, issue := range snapshot.Issues {
		add("issue", issue.ID)
	}
	for _, versionSUT := range snapshot.VersionSUTs {
		add("version_sut", versionSUT.ID)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Kind != refs[j].Kind {
			return refs[i].Kind < refs[j].Kind
		}
		return refs[i].RefID < refs[j].RefID
	})
	return refs
}

// terminalWaveStatus reports a run_wave terminal status (done/failed/cancelled).
func terminalWaveStatus(status string) bool {
	switch status {
	case "done", "failed", "cancelled":
		return true
	default:
		return false
	}
}

// versionReportDue applies the §7.4 version-report condition that is visible
// from the version snapshot: every requirement is closed or obsolete and no
// wave is active. The active-intent/human-request checks belong to the close
// predicate and the reader/wiring layer.
func versionReportDue(snapshot delivery.ReportSnapshot) bool {
	for _, requirement := range snapshot.Requirements {
		if requirement.Status != "closed" && requirement.Status != "obsolete" {
			return false
		}
	}
	for _, wave := range snapshot.Waves {
		switch wave.Status {
		case "planned", "running", "paused":
			return false
		}
	}
	return true
}

// reportHasPublishedRevision reports whether the report has a published
// current revision (a stale report still has one and is only regenerated
// explicitly or through the stale path).
func reportHasPublishedRevision(report delivery.Report) bool {
	return report.Status == delivery.ReportStatusPublished || report.Status == delivery.ReportStatusStale
}

// validReportSHA reports a lowercase hex sha256.
func validReportSHA(value string) bool {
	if len(value) != 64 {
		return false
	}
	for index := 0; index < len(value); index++ {
		char := value[index]
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

// validPDFHeader reports the PDF file signature of stored bytes.
func validPDFHeader(data []byte) bool {
	return len(data) >= 5 && string(data[:5]) == "%PDF-"
}

// pdfNotReady is the stable 409 of a revision whose PDF is still rendering
// (spec §6.4).
func pdfNotReady() error {
	apiErr := platform.NewError(409, "pdf_not_ready", "report PDF is not ready")
	apiErr.Details = map[string]any{"reason": "pdf_not_ready"}
	return apiErr
}

// pdfFailed is the stable 409 of a revision whose PDF rendering failed. The
// readable failure reason travels in details.error (already bounded on write).
func pdfFailed(message string) error {
	apiErr := platform.NewError(409, "pdf_failed", "report PDF rendering failed")
	details := map[string]any{"reason": "pdf_failed"}
	if message != "" {
		details["error"] = message
	}
	apiErr.Details = details
	return apiErr
}

// reportContentCorrupt is the stable 409 for a failed report content integrity
// check (spec §6.4).
func reportContentCorrupt() error {
	apiErr := platform.StateConflict("report content failed its integrity check")
	apiErr.Details = map[string]any{"reason": "report_content_corrupt"}
	return apiErr
}

// reportServiceNotConfigured is the internal error of a missing wired port.
func reportServiceNotConfigured(kind string) error {
	return platform.NewError(500, "internal", kind+" is not configured")
}

// mapReportError maps the report repository/content sentinels onto the stable
// API envelope.
func mapReportError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrReportNotFound):
		return platform.NotFound("report")
	case errors.Is(err, delivery.ErrInvalidArgument):
		return platform.Invalid("report", "report input is not valid")
	case errors.Is(err, delivery.ErrStateConflict):
		return platform.StateConflict("report is not in a valid state")
	default:
		return err
	}
}
