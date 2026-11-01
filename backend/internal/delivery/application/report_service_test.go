package deliveryapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// ---------------------------------------------------------------------------
// Fake repository
// ---------------------------------------------------------------------------

type fakeReportRepo struct {
	mu        sync.Mutex
	reports   map[int64]delivery.Report
	revisions map[int64]map[int32]delivery.ReportRevision
	refs      map[int64][]delivery.ReportRef
	nextID    int64
	log       *[]string
	failNext  error
}

func newFakeReportRepo(log *[]string) *fakeReportRepo {
	return &fakeReportRepo{
		reports:   make(map[int64]delivery.Report),
		revisions: make(map[int64]map[int32]delivery.ReportRevision),
		refs:      make(map[int64][]delivery.ReportRef),
		nextID:    1000,
		log:       log,
	}
}

func (r *fakeReportRepo) record(label string) {
	if r.log != nil {
		*r.log = append(*r.log, label)
	}
}

func (r *fakeReportRepo) newID() int64 {
	r.nextID++
	return r.nextID
}

func (r *fakeReportRepo) seedReport(report delivery.Report) delivery.Report {
	r.mu.Lock()
	defer r.mu.Unlock()
	if report.ID == 0 {
		report.ID = r.newID()
	}
	if report.Status == "" {
		report.Status = delivery.ReportStatusDraft
	}
	if report.CreatedAt.IsZero() {
		report.CreatedAt = time.Unix(1000, 0).UTC()
	}
	if report.UpdatedAt.IsZero() {
		report.UpdatedAt = report.CreatedAt
	}
	r.reports[report.ID] = report
	return report
}

func (r *fakeReportRepo) seedRevision(revision delivery.ReportRevision) delivery.ReportRevision {
	r.mu.Lock()
	defer r.mu.Unlock()
	if revision.ID == 0 {
		revision.ID = r.newID()
	}
	if revision.Status == "" {
		revision.Status = delivery.ReportStatusDraft
	}
	if revision.RenderStatus == "" {
		revision.RenderStatus = "pending"
	}
	if revision.TemplateVersion == "" {
		revision.TemplateVersion = "1"
	}
	if revision.InputFingerprint == "" {
		revision.InputFingerprint = strings.Repeat("a", 64)
	}
	if revision.AsOf.IsZero() {
		revision.AsOf = time.Unix(2000, 0).UTC()
	}
	if revision.CreatedAt.IsZero() {
		revision.CreatedAt = time.Unix(2000, 0).UTC()
	}
	if revision.UpdatedAt.IsZero() {
		revision.UpdatedAt = revision.CreatedAt
	}
	if r.revisions[revision.ReportID] == nil {
		r.revisions[revision.ReportID] = make(map[int32]delivery.ReportRevision)
	}
	r.revisions[revision.ReportID][revision.Revision] = revision
	return revision
}

func (r *fakeReportRepo) seedPublished(report delivery.Report, revision delivery.ReportRevision, htmlRef int64, fingerprint string) (delivery.Report, delivery.ReportRevision) {
	report.Status = delivery.ReportStatusPublished
	report.InputFingerprint = fingerprint
	seeded := r.seedReport(report)
	revision.Status = delivery.ReportStatusPublished
	revision.RenderStatus = "done"
	revision.HTMLRef = &htmlRef
	revision.InputFingerprint = fingerprint
	seededRevision := r.seedRevision(revision)
	seeded.CurrentRevisionID = &seededRevision.ID
	r.reports[seeded.ID] = seeded
	return seeded, seededRevision
}

func (r *fakeReportRepo) CreateReport(_ context.Context, report delivery.Report) (delivery.Report, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failNext != nil {
		err := r.failNext
		r.failNext = nil
		return delivery.Report{}, err
	}
	for _, existing := range r.reports {
		if existing.VersionID != report.VersionID || existing.Kind != report.Kind {
			continue
		}
		if report.Kind == delivery.ReportKindVersion ||
			(existing.WaveID != nil && report.WaveID != nil && *existing.WaveID == *report.WaveID) {
			return existing, nil
		}
	}
	if report.ID == 0 {
		report.ID = r.newID()
	}
	if report.Status == "" {
		report.Status = delivery.ReportStatusDraft
	}
	if report.CreatedAt.IsZero() {
		report.CreatedAt = time.Unix(1000, 0).UTC()
	}
	if report.UpdatedAt.IsZero() {
		report.UpdatedAt = report.CreatedAt
	}
	r.reports[report.ID] = report
	r.record("repo.create-report")
	return report, nil
}

func (r *fakeReportRepo) GetReport(_ context.Context, versionID, reportID int64) (delivery.Report, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	report, ok := r.reports[reportID]
	if !ok || report.VersionID != versionID {
		return delivery.Report{}, ErrReportNotFound
	}
	return report, nil
}

func (r *fakeReportRepo) GetReportByID(_ context.Context, reportID int64) (delivery.Report, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	report, ok := r.reports[reportID]
	if !ok {
		return delivery.Report{}, ErrReportNotFound
	}
	return report, nil
}

func (r *fakeReportRepo) ListReports(_ context.Context, versionID int64, f ReportFilter) ([]delivery.Report, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]delivery.Report, 0)
	for _, report := range r.reports {
		if report.VersionID != versionID {
			continue
		}
		if f.Kind != nil && report.Kind != *f.Kind {
			continue
		}
		if f.Before > 0 && report.ID >= f.Before {
			continue
		}
		result = append(result, report)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID > result[j].ID })
	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (r *fakeReportRepo) NextRevision(_ context.Context, reportID int64) (int32, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.reports[reportID]; !ok {
		return 0, ErrReportNotFound
	}
	var max int32
	for revision := range r.revisions[reportID] {
		if revision > max {
			max = revision
		}
	}
	return max + 1, nil
}

func (r *fakeReportRepo) CreateRevision(_ context.Context, revision delivery.ReportRevision) (delivery.ReportRevision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.reports[revision.ReportID]; !ok {
		return delivery.ReportRevision{}, ErrReportNotFound
	}
	if revision.Revision <= 0 {
		var max int32
		for number := range r.revisions[revision.ReportID] {
			if number > max {
				max = number
			}
		}
		revision.Revision = max + 1
	}
	if _, exists := r.revisions[revision.ReportID][revision.Revision]; exists {
		return delivery.ReportRevision{}, delivery.ErrStateConflict
	}
	if revision.ID == 0 {
		revision.ID = r.newID()
	}
	if revision.Status == "" {
		revision.Status = delivery.ReportStatusDraft
	}
	if revision.RenderStatus == "" {
		revision.RenderStatus = "pending"
	}
	if revision.TemplateVersion == "" {
		revision.TemplateVersion = "1"
	}
	if revision.AsOf.IsZero() {
		revision.AsOf = time.Unix(2000, 0).UTC()
	}
	if revision.CreatedAt.IsZero() {
		revision.CreatedAt = time.Unix(2000, 0).UTC()
	}
	if revision.UpdatedAt.IsZero() {
		revision.UpdatedAt = revision.CreatedAt
	}
	if r.revisions[revision.ReportID] == nil {
		r.revisions[revision.ReportID] = make(map[int32]delivery.ReportRevision)
	}
	r.revisions[revision.ReportID][revision.Revision] = revision
	r.record("repo.create-revision")
	return revision, nil
}

func (r *fakeReportRepo) FinalizePublished(_ context.Context, reportID int64, revision int32, htmlRef int64, fingerprint string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	report, ok := r.reports[reportID]
	if !ok {
		return ErrReportNotFound
	}
	row, ok := r.revisions[reportID][revision]
	if !ok {
		return ErrReportNotFound
	}
	if row.Status == delivery.ReportStatusPublished {
		return delivery.ErrStateConflict
	}
	row.Status = delivery.ReportStatusPublished
	row.RenderStatus = "done"
	row.HTMLRef = &htmlRef
	row.InputFingerprint = fingerprint
	r.revisions[reportID][revision] = row
	report.CurrentRevisionID = &row.ID
	report.Status = delivery.ReportStatusPublished
	report.InputFingerprint = fingerprint
	r.reports[reportID] = report
	r.record("repo.finalize")
	return nil
}

func (r *fakeReportRepo) MarkRenderMetadata(_ context.Context, revisionID int64, status, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for reportID, revisions := range r.revisions {
		for number, revision := range revisions {
			if revision.ID != revisionID {
				continue
			}
			if revision.Status == delivery.ReportStatusPublished {
				return delivery.ErrStateConflict
			}
			revision.RenderStatus = status
			r.revisions[reportID][number] = revision
			r.record("repo.render-" + status)
			return nil
		}
	}
	return ErrReportNotFound
}

func (r *fakeReportRepo) MarkStale(_ context.Context, reportID int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	report, ok := r.reports[reportID]
	if !ok {
		return false, ErrReportNotFound
	}
	if report.Status != delivery.ReportStatusPublished {
		return false, nil
	}
	report.Status = delivery.ReportStatusStale
	r.reports[reportID] = report
	r.record("repo.mark-stale")
	return true, nil
}

func (r *fakeReportRepo) GetRevision(_ context.Context, reportID int64, revision int32) (delivery.ReportRevision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.revisions[reportID][revision]
	if !ok {
		return delivery.ReportRevision{}, ErrReportNotFound
	}
	return row, nil
}

func (r *fakeReportRepo) ListRevisions(_ context.Context, reportID int64) ([]delivery.ReportRevision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]delivery.ReportRevision, 0, len(r.revisions[reportID]))
	for _, revision := range r.revisions[reportID] {
		result = append(result, revision)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Revision > result[j].Revision })
	return result, nil
}

func (r *fakeReportRepo) InsertRefs(_ context.Context, revisionID int64, refs []delivery.ReportRef) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var reportID int64
	for candidateReportID, revisions := range r.revisions {
		for _, revision := range revisions {
			if revision.ID == revisionID {
				reportID = candidateReportID
			}
		}
	}
	if reportID == 0 {
		return ErrReportNotFound
	}
	existing := make(map[string]bool, len(r.refs[reportID]))
	for _, ref := range r.refs[reportID] {
		existing[fmt.Sprintf("%s:%d", ref.Kind, ref.RefID)] = true
	}
	for _, ref := range refs {
		key := fmt.Sprintf("%s:%d", ref.Kind, ref.RefID)
		if existing[key] {
			continue
		}
		existing[key] = true
		ref.ID = r.newID()
		ref.ReportID = reportID
		ref.ReportRevisionID = revisionID
		r.refs[reportID] = append(r.refs[reportID], ref)
	}
	r.record("repo.insert-refs")
	return nil
}

func (r *fakeReportRepo) ListRefs(_ context.Context, reportID int64) ([]delivery.ReportRef, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]delivery.ReportRef(nil), r.refs[reportID]...), nil
}

func (r *fakeReportRepo) SetWaveReportID(_ context.Context, _, waveID, reportID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failNext != nil {
		err := r.failNext
		r.failNext = nil
		return err
	}
	r.record(fmt.Sprintf("repo.set-wave:%d:%d", waveID, reportID))
	return nil
}

// ---------------------------------------------------------------------------
// Other fakes
// ---------------------------------------------------------------------------

type fakeReportRenderer struct {
	log           *[]string
	output        []byte
	err           error
	lastSnapshot  delivery.ReportSnapshot
	lastNarrative string
	lastOptions   delivery.SectionOptions
	templateVer   string
	calls         int
}

// TemplateVersion implements delivery.ReportTemplateVersioner so a test can
// prove the service records the renderer's built-in version (P7-D-03).
func (r *fakeReportRenderer) TemplateVersion() string { return r.templateVer }

func (r *fakeReportRenderer) Render(_ context.Context, snapshot delivery.ReportSnapshot, narrative string, opts delivery.SectionOptions) ([]byte, error) {
	r.calls++
	r.lastSnapshot = snapshot
	r.lastNarrative = narrative
	r.lastOptions = opts
	if r.log != nil {
		*r.log = append(*r.log, "render")
	}
	if r.err != nil {
		return nil, r.err
	}
	if r.output != nil {
		return append([]byte(nil), r.output...), nil
	}
	return []byte("<!doctype html><html><body>report</body></html>"), nil
}

type fakeReportContent struct {
	mu          sync.Mutex
	log         *[]string
	puts        map[int64][]byte
	putErr      error
	openErr     error
	openSHA     string
	entries     map[int64]fakeReportContentEntry
	pdfs        map[int64]fakeReportContentEntry
	pdfRefs     map[int64]ReportContentRef
	pdfErr      error
	putPDFErr   error
	putPDFCalls int
	nextRef     int64
	putCalls    int
	openCalls   int
	lastOpenID  int64
}

type fakeReportContentEntry struct {
	data []byte
	sha  string
	size int64
}

func newFakeReportContent(log *[]string) *fakeReportContent {
	return &fakeReportContent{
		log: log, puts: make(map[int64][]byte), entries: make(map[int64]fakeReportContentEntry),
		pdfs: make(map[int64]fakeReportContentEntry), pdfRefs: make(map[int64]ReportContentRef), nextRef: 5000,
	}
}

func reportContentSHA(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (c *fakeReportContent) PutReportHTML(_ context.Context, revisionID int64, html []byte) (int64, string, int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.putCalls++
	if c.log != nil {
		*c.log = append(*c.log, "content.put")
	}
	if c.putErr != nil {
		return 0, "", 0, c.putErr
	}
	refID := c.nextRef
	c.nextRef++
	sha := reportContentSHA(html)
	size := int64(len(html))
	c.puts[revisionID] = append([]byte(nil), html...)
	c.entries[refID] = fakeReportContentEntry{data: append([]byte(nil), html...), sha: sha, size: size}
	return refID, sha, size, nil
}

func (c *fakeReportContent) OpenReportHTML(_ context.Context, contentRefID int64) ([]byte, string, int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.openCalls++
	c.lastOpenID = contentRefID
	if c.log != nil {
		*c.log = append(*c.log, "content.open")
	}
	if c.openErr != nil {
		return nil, "", 0, c.openErr
	}
	entry, ok := c.entries[contentRefID]
	if !ok {
		return nil, "", 0, ErrReportContentNotFound
	}
	sha := entry.sha
	if c.openSHA != "" {
		sha = c.openSHA
	}
	return append([]byte(nil), entry.data...), sha, entry.size, nil
}

func (c *fakeReportContent) seed(refID, revisionID int64, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sha := reportContentSHA(data)
	c.entries[refID] = fakeReportContentEntry{data: append([]byte(nil), data...), sha: sha, size: int64(len(data))}
	c.puts[revisionID] = append([]byte(nil), data...)
}

// GetReportHTML implements RenderContent.
func (c *fakeReportContent) GetReportHTML(_ context.Context, contentRefID int64) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.openErr != nil {
		return nil, c.openErr
	}
	entry, ok := c.entries[contentRefID]
	if !ok {
		return nil, ErrReportContentNotFound
	}
	return append([]byte(nil), entry.data...), nil
}

// PutReportPDF implements RenderContent.
func (c *fakeReportContent) PutReportPDF(_ context.Context, revisionID int64, data []byte) (ReportContentRef, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.putPDFCalls++
	if c.putPDFErr != nil {
		return ReportContentRef{}, c.putPDFErr
	}
	refID := c.nextRef
	c.nextRef++
	sha := reportContentSHA(data)
	size := int64(len(data))
	c.pdfs[refID] = fakeReportContentEntry{data: append([]byte(nil), data...), sha: sha, size: size}
	ref := ReportContentRef{RefID: refID, SHA256: sha, Size: size}
	c.pdfRefs[revisionID] = ref
	return ref, nil
}

// OpenReportPDF implements ContentStore.
func (c *fakeReportContent) OpenReportPDF(_ context.Context, contentRefID int64) ([]byte, string, int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pdfErr != nil {
		return nil, "", 0, c.pdfErr
	}
	entry, ok := c.pdfs[contentRefID]
	if !ok {
		return nil, "", 0, ErrReportContentNotFound
	}
	return append([]byte(nil), entry.data...), entry.sha, entry.size, nil
}

// seedPDF stores one PDF content entry for the OpenPDF read path.
func (c *fakeReportContent) seedPDF(refID int64, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pdfs[refID] = fakeReportContentEntry{data: append([]byte(nil), data...), sha: reportContentSHA(data), size: int64(len(data))}
}

type fakeReportData struct {
	mu          sync.Mutex
	log         *[]string
	wave        map[string]delivery.ReportSnapshot
	version     map[int64]delivery.ReportSnapshot
	waveErr     error
	versionErr  error
	waveCalls   int
	versionCall int
	// versionReady optionally gates version report generation (the optional
	// ReportTriggerReader input for active intents/human requests).
	versionReady    *bool
	versionReadyErr error
}

func newFakeReportData(log *[]string) *fakeReportData {
	return &fakeReportData{log: log, wave: make(map[string]delivery.ReportSnapshot), version: make(map[int64]delivery.ReportSnapshot)}
}

func reportDataWaveKey(versionID, waveID int64) string {
	return fmt.Sprintf("%d:%d", versionID, waveID)
}

func (d *fakeReportData) WaveSnapshot(_ context.Context, versionID, waveID int64) (delivery.ReportSnapshot, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.waveCalls++
	if d.log != nil {
		*d.log = append(*d.log, "data.wave")
	}
	if d.waveErr != nil {
		return delivery.ReportSnapshot{}, d.waveErr
	}
	snapshot, ok := d.wave[reportDataWaveKey(versionID, waveID)]
	if !ok {
		return delivery.ReportSnapshot{}, fmt.Errorf("no wave snapshot for %d/%d", versionID, waveID)
	}
	return snapshot, nil
}

func (d *fakeReportData) VersionSnapshot(_ context.Context, versionID int64) (delivery.ReportSnapshot, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.versionCall++
	if d.log != nil {
		*d.log = append(*d.log, "data.version")
	}
	if d.versionErr != nil {
		return delivery.ReportSnapshot{}, d.versionErr
	}
	snapshot, ok := d.version[versionID]
	if !ok {
		return delivery.ReportSnapshot{}, fmt.Errorf("no version snapshot for %d", versionID)
	}
	return snapshot, nil
}

func (d *fakeReportData) VersionReportReady(_ context.Context, _ int64) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.versionReadyErr != nil {
		return false, d.versionReadyErr
	}
	if d.versionReady == nil {
		return true, nil
	}
	return *d.versionReady, nil
}

type fakeReportEvents struct {
	mu        sync.Mutex
	log       *[]string
	published []fakeReportEvent
	err       error
}

type fakeReportEvent struct {
	eventType string
	versionID int64
	payload   map[string]any
}

func (e *fakeReportEvents) Publish(_ context.Context, eventType string, versionID int64, payload map[string]any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.log != nil {
		*e.log = append(*e.log, "event:"+eventType)
	}
	if e.err != nil {
		return e.err
	}
	e.published = append(e.published, fakeReportEvent{eventType: eventType, versionID: versionID, payload: payload})
	return nil
}

func (e *fakeReportEvents) count(eventType string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	count := 0
	for _, event := range e.published {
		if event.eventType == eventType {
			count++
		}
	}
	return count
}

type fakeReportAudit struct {
	mu    sync.Mutex
	log   *[]string
	calls []string
	err   error
}

func (a *fakeReportAudit) Write(_ context.Context, _ int64, action, _, _ string, _ map[string]any) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.log != nil {
		*a.log = append(*a.log, "audit:"+action)
	}
	if a.err != nil {
		return a.err
	}
	a.calls = append(a.calls, action)
	return nil
}

type fakeReportClock struct{ now time.Time }

func (c fakeReportClock) Now() time.Time { return c.now }

type fakeReportTasks struct {
	mu    sync.Mutex
	log   *[]string
	calls []string
	err   error
}

func (t *fakeReportTasks) EnsureReportTask(_ context.Context, reportID int64, revision int32) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	call := fmt.Sprintf("task:%d:%d", reportID, revision)
	t.calls = append(t.calls, call)
	if t.log != nil {
		*t.log = append(*t.log, call)
	}
	return t.err
}

func (t *fakeReportTasks) EnsureIssueTriage(context.Context, int64, int64) error { return nil }
func (t *fakeReportTasks) EnsureRequirementClose(context.Context, int64, int64, int32) error {
	return nil
}
func (t *fakeReportTasks) EnsureVersionClose(context.Context, int64, int64, string) error {
	return nil
}

type reportFixture struct {
	repo     *fakeReportRepo
	renderer *fakeReportRenderer
	content  *fakeReportContent
	data     *fakeReportData
	events   *fakeReportEvents
	audit    *fakeReportAudit
	tasks    *fakeReportTasks
	clock    fakeReportClock
	svc      *ReportService
	log      *[]string
}

func newReportFixture(t *testing.T) *reportFixture {
	t.Helper()
	log := make([]string, 0, 64)
	fixture := &reportFixture{
		repo:     newFakeReportRepo(&log),
		renderer: &fakeReportRenderer{log: &log},
		content:  newFakeReportContent(&log),
		data:     newFakeReportData(&log),
		events:   &fakeReportEvents{log: &log},
		audit:    &fakeReportAudit{log: &log},
		tasks:    &fakeReportTasks{log: &log},
		clock:    fakeReportClock{now: time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)},
		log:      &log,
	}
	fixture.svc = NewReportService(
		fixture.repo,
		fixture.renderer,
		fixture.content,
		fixture.data,
		fixture.events,
		fixture.audit,
		fixture.clock,
		fixture.tasks,
		delivery.SectionOptions{Summary: true, Results: true, Issues: true, Coverage: true, Evidence: true},
	)
	return fixture
}

func reportTestFingerprint(char string) string { return strings.Repeat(char, 64) }

func reportTestWaveSnapshot(versionID, waveID int64) delivery.ReportSnapshot {
	return delivery.ReportSnapshot{
		Kind:      delivery.ReportKindWave,
		VersionID: versionID,
		WaveID:    &waveID,
		Waves:     []delivery.WaveSnapshot{{ID: waveID, VersionID: versionID, Status: "done", Trigger: "manual"}},
		CaseRuns: []delivery.CaseRunSnapshot{{
			ID: 3001, VersionID: versionID, WaveID: waveID, CaseVersionID: 4001,
			CaseTitle: "用例", Status: "failed", Category: "api", Severity: "major",
		}},
		Issues: []delivery.IssueSnapshot{{
			ID: 5001, VersionID: versionID, Title: "问题", Status: delivery.IssueStatusOpen,
			Category: "api", Severity: "major",
		}},
		VersionSUTs: []delivery.VersionSUTSnapshot{{ID: 6001, SUTID: 7001, SUTName: "API"}},
	}
}

func reportTestVersionSnapshot(versionID int64) delivery.ReportSnapshot {
	return delivery.ReportSnapshot{
		Kind:      delivery.ReportKindVersion,
		VersionID: versionID,
		Requirements: []delivery.RequirementSnapshot{
			{ID: 101, VersionID: versionID, Title: "需求", Status: "closed"},
		},
		Waves:    []delivery.WaveSnapshot{{ID: 12, VersionID: versionID, Status: "done"}},
		CaseRuns: []delivery.CaseRunSnapshot{{ID: 3001, VersionID: versionID, WaveID: 12, CaseVersionID: 4001, Status: "passed"}},
		Issues:   []delivery.IssueSnapshot{{ID: 5001, VersionID: versionID, Title: "问题", Status: delivery.IssueStatusConfirmed}},
		Coverage: delivery.CoverageSnapshot{RequirementsTotal: 1, RequirementsCovered: 1},
	}
}

func (f *reportFixture) seedWaveReport(versionID, waveID int64) (delivery.Report, delivery.ReportRevision) {
	wave := waveID
	f.data.wave[reportDataWaveKey(versionID, waveID)] = reportTestWaveSnapshot(versionID, waveID)
	report := f.repo.seedReport(delivery.Report{
		ID: 100, VersionID: versionID, WaveID: &wave, Kind: delivery.ReportKindWave,
	})
	revision := f.repo.seedRevision(delivery.ReportRevision{ReportID: report.ID, Revision: 1})
	return report, revision
}

func (f *reportFixture) seedPublishedWaveReport(versionID, waveID int64, fingerprint string) (delivery.Report, delivery.ReportRevision) {
	wave := waveID
	report := f.repo.seedReport(delivery.Report{
		ID: 100, VersionID: versionID, WaveID: &wave, Kind: delivery.ReportKindWave,
	})
	revision := f.repo.seedRevision(delivery.ReportRevision{ReportID: report.ID, Revision: 1})
	f.data.wave[reportDataWaveKey(versionID, waveID)] = reportTestWaveSnapshot(versionID, waveID)
	published, publishedRevision := f.repo.seedPublished(report, revision, 5001, fingerprint)
	return published, publishedRevision
}

func (f *reportFixture) seedPublishedVersionReport(versionID int64, fingerprint string) (delivery.Report, delivery.ReportRevision) {
	report := f.repo.seedReport(delivery.Report{ID: 200, VersionID: versionID, Kind: delivery.ReportKindVersion})
	revision := f.repo.seedRevision(delivery.ReportRevision{ReportID: report.ID, Revision: 1})
	f.data.version[versionID] = reportTestVersionSnapshot(versionID)
	published, publishedRevision := f.repo.seedPublished(report, revision, 5002, fingerprint)
	return published, publishedRevision
}

func (f *reportFixture) repoRevision(t *testing.T, reportID int64, revision int32) delivery.ReportRevision {
	t.Helper()
	row, err := f.repo.GetRevision(context.Background(), reportID, revision)
	if err != nil {
		t.Fatalf("GetRevision(%d, %d): %v", reportID, revision, err)
	}
	return row
}

func assertLogOrder(t *testing.T, log []string, wanted ...string) {
	t.Helper()
	position := 0
	for _, entry := range log {
		if position < len(wanted) && entry == wanted[position] {
			position++
		}
	}
	if position != len(wanted) {
		t.Fatalf("log order = %v, missing or out of order: %v", log, wanted[position:])
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestReportServicePublishWritesContentThenPublishes(t *testing.T) {
	fixture := newReportFixture(t)
	report, _ := fixture.seedWaveReport(7, 12)

	published, err := fixture.svc.Publish(context.Background(), report.ID, 1, "叙述文本")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if published.Status != delivery.ReportStatusPublished || published.RenderStatus != "done" ||
		published.HTMLRef == nil || *published.HTMLRef != 5000 || published.InputFingerprint == "" {
		t.Fatalf("published revision = %+v", published)
	}
	if published.AsOf.IsZero() {
		t.Fatal("published revision as_of is zero")
	}
	stored, err := fixture.repo.GetReport(context.Background(), 7, report.ID)
	if err != nil {
		t.Fatalf("GetReport after publish: %v", err)
	}
	if stored.Status != delivery.ReportStatusPublished || stored.CurrentRevisionID == nil ||
		*stored.CurrentRevisionID != published.ID || stored.InputFingerprint != published.InputFingerprint {
		t.Fatalf("stored report = %+v", stored)
	}
	if fixture.renderer.lastNarrative != "叙述文本" || fixture.renderer.lastOptions.Summary != true {
		t.Fatalf("renderer narrative/options = %q/%+v", fixture.renderer.lastNarrative, fixture.renderer.lastOptions)
	}
	if fixture.renderer.lastSnapshot.GeneratedAt != fixture.clock.now {
		t.Fatalf("snapshot generated_at = %v", fixture.renderer.lastSnapshot.GeneratedAt)
	}
	// The v2 appendix metadata is stamped by the service, never trusted from a
	// reader: report id, revision number and the final input fingerprint.
	if fixture.renderer.lastSnapshot.ReportID != report.ID ||
		fixture.renderer.lastSnapshot.ReportRevision != published.Revision ||
		fixture.renderer.lastSnapshot.InputFingerprint != published.InputFingerprint {
		t.Fatalf("renderer appendix metadata = id:%d revision:%d fingerprint:%q",
			fixture.renderer.lastSnapshot.ReportID, fixture.renderer.lastSnapshot.ReportRevision,
			fixture.renderer.lastSnapshot.InputFingerprint)
	}
	if len(fixture.renderer.lastSnapshot.Issues) != 1 || len(fixture.renderer.lastSnapshot.CaseRuns) != 1 {
		t.Fatalf("renderer snapshot = %+v", fixture.renderer.lastSnapshot)
	}
	if *published.HTMLRef != 5000 {
		t.Fatalf("html_ref = %d", *published.HTMLRef)
	}
	// The snapshot kind/wave are stamped from the report, not trusted from the
	// reader (defends a mis-scoped reader).
	if fixture.renderer.lastSnapshot.Kind != delivery.ReportKindWave ||
		fixture.renderer.lastSnapshot.WaveID == nil || *fixture.renderer.lastSnapshot.WaveID != 12 {
		t.Fatalf("renderer snapshot scope = %+v", fixture.renderer.lastSnapshot)
	}

	assertLogOrder(t, *fixture.log,
		"data.wave",
		"render",
		"content.put",
		"repo.insert-refs",
		"repo.finalize",
		"repo.set-wave:12:"+fmt.Sprint(report.ID),
		"event:report.published",
		"audit:report.published",
	)

	if got := fixture.events.count("report.published"); got != 1 {
		t.Fatalf("report.published events = %d, want 1", got)
	}
	event := fixture.events.published[0]
	if event.versionID != 7 || event.payload["kind"] != "wave" ||
		event.payload["report_id"] != fmt.Sprint(report.ID) ||
		event.payload["report_revision_id"] != fmt.Sprint(published.ID) ||
		event.payload["wave_id"] != "12" ||
		event.payload["html_ref"] != "5000" {
		t.Fatalf("report.published event = %+v", event)
	}
	if fixture.audit.calls[0] != "report.published" {
		t.Fatalf("audit = %v", fixture.audit.calls)
	}
	refs, err := fixture.repo.ListRefs(context.Background(), report.ID)
	if err != nil {
		t.Fatalf("ListRefs: %v", err)
	}
	refKinds := make(map[string][]int64, len(refs))
	for _, ref := range refs {
		refKinds[ref.Kind] = append(refKinds[ref.Kind], ref.RefID)
	}
	if len(refKinds["wave"]) != 1 || refKinds["wave"][0] != 12 {
		t.Fatalf("wave refs = %v", refKinds["wave"])
	}
	if len(refKinds["case_run"]) != 1 || refKinds["case_run"][0] != 3001 {
		t.Fatalf("case_run refs = %v", refKinds["case_run"])
	}
	if len(refKinds["issue"]) != 1 || refKinds["issue"][0] != 5001 {
		t.Fatalf("issue refs = %v", refKinds["issue"])
	}
	if len(fixture.tasks.calls) != 0 {
		t.Fatalf("Publish ensured tasks: %v", fixture.tasks.calls)
	}
}

// TestReportServiceRecordsRendererTemplateVersion pins P7-D-03: every new
// revision records the template version of the renderer that will render it,
// not a stale hard-coded constant.
func TestReportServiceRecordsRendererTemplateVersion(t *testing.T) {
	fixture := newReportFixture(t)
	fixture.renderer.templateVer = "2"
	fixture.data.wave[reportDataWaveKey(7, 12)] = reportTestWaveSnapshot(7, 12)

	report, err := fixture.svc.EnsureWaveReport(context.Background(), 7, 12)
	if err != nil {
		t.Fatalf("EnsureWaveReport: %v", err)
	}
	revision := fixture.repoRevision(t, report.ID, 1)
	if revision.TemplateVersion != "2" {
		t.Fatalf("draft template_version = %q, want the renderer version 2", revision.TemplateVersion)
	}
	published, err := fixture.svc.Publish(context.Background(), report.ID, 1, "")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if published.TemplateVersion != "2" {
		t.Fatalf("published template_version = %q, want 2", published.TemplateVersion)
	}

	// A renderer that does not report a version keeps the frozen P4 version.
	fallback := newReportFixture(t)
	fallback.data.wave[reportDataWaveKey(7, 12)] = reportTestWaveSnapshot(7, 12)
	fallbackReport, err := fallback.svc.EnsureWaveReport(context.Background(), 7, 12)
	if err != nil {
		t.Fatalf("EnsureWaveReport fallback: %v", err)
	}
	if got := fallback.repoRevision(t, fallbackReport.ID, 1).TemplateVersion; got != "1" {
		t.Fatalf("fallback template_version = %q, want 1", got)
	}
}

// TestReportServiceGetReturnsOnlyCurrentRevisionRefs pins P7-E2E-BUG-01: report
// detail must project the current revision's refs only (deduplicated by
// kind/ref_id), never the append-only history of every revision.
func TestReportServiceGetReturnsOnlyCurrentRevisionRefs(t *testing.T) {
	fixture := newReportFixture(t)
	report, first := fixture.seedPublishedWaveReport(7, 12, reportTestFingerprint("a"))
	second := fixture.repo.seedRevision(delivery.ReportRevision{ReportID: report.ID, Revision: 2})
	third := fixture.repo.seedRevision(delivery.ReportRevision{ReportID: report.ID, Revision: 3})
	report.CurrentRevisionID = &third.ID
	fixture.repo.reports[report.ID] = report
	fixture.repo.refs[report.ID] = []delivery.ReportRef{
		{ID: 1, ReportID: report.ID, ReportRevisionID: first.ID, Kind: "case_run", RefID: 3001},
		{ID: 2, ReportID: report.ID, ReportRevisionID: second.ID, Kind: "case_run", RefID: 3001},
		{ID: 3, ReportID: report.ID, ReportRevisionID: third.ID, Kind: "case_run", RefID: 3001},
		{ID: 4, ReportID: report.ID, ReportRevisionID: third.ID, Kind: "issue", RefID: 5001},
		// A duplicate of the same logical ref inside the current revision is
		// collapsed too (the schema allows append-only duplicates).
		{ID: 5, ReportID: report.ID, ReportRevisionID: third.ID, Kind: "case_run", RefID: 3001},
	}

	detail, err := fixture.svc.Get(context.Background(), 7, report.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(detail.Refs) != 2 {
		t.Fatalf("detail refs = %+v, want 2 unique current-revision refs", detail.Refs)
	}
	if detail.Refs[0].ID != 3 || detail.Refs[0].Kind != "case_run" ||
		detail.Refs[1].ID != 4 || detail.Refs[1].Kind != "issue" {
		t.Fatalf("detail refs = %+v, want current revision refs [3 4]", detail.Refs)
	}
	for _, ref := range detail.Refs {
		if ref.ReportRevisionID != third.ID {
			t.Fatalf("detail ref %+v is not from the current revision %d", ref, third.ID)
		}
	}
	if len(detail.Revisions) != 3 {
		t.Fatalf("detail revisions = %d, want the full history (3)", len(detail.Revisions))
	}
}

// TestReportServiceGetWithoutCurrentRevisionDedupes covers a draft report (no
// current revision): the detail falls back to the deduplicated union instead of
// repeating append-only history rows.
func TestReportServiceGetWithoutCurrentRevisionDedupes(t *testing.T) {
	fixture := newReportFixture(t)
	report, revision := fixture.seedWaveReport(7, 12)
	second := fixture.repo.seedRevision(delivery.ReportRevision{ReportID: report.ID, Revision: 2})
	fixture.repo.refs[report.ID] = []delivery.ReportRef{
		{ID: 1, ReportID: report.ID, ReportRevisionID: revision.ID, Kind: "case_run", RefID: 3001},
		{ID: 2, ReportID: report.ID, ReportRevisionID: second.ID, Kind: "case_run", RefID: 3001},
	}
	detail, err := fixture.svc.Get(context.Background(), 7, report.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(detail.Refs) != 1 || detail.Refs[0].Kind != "case_run" || detail.Refs[0].RefID != 3001 {
		t.Fatalf("detail refs = %+v, want one deduplicated ref", detail.Refs)
	}
}

func TestReportServicePublishIsIdempotentForPublishedRevision(t *testing.T) {
	fixture := newReportFixture(t)
	fingerprint := reportTestFingerprint("a")
	report, revision := fixture.seedPublishedWaveReport(7, 12, fingerprint)

	published, err := fixture.svc.Publish(context.Background(), report.ID, revision.Revision, "ignored")
	if err != nil {
		t.Fatalf("Publish published revision: %v", err)
	}
	if published.ID != revision.ID || published.Status != delivery.ReportStatusPublished {
		t.Fatalf("idempotent publish = %+v", published)
	}
	if fixture.renderer.calls != 0 || fixture.content.putCalls != 0 {
		t.Fatalf("idempotent publish re-rendered/wrote: render=%d put=%d", fixture.renderer.calls, fixture.content.putCalls)
	}
	if fixture.events.count("report.published") != 0 {
		t.Fatal("idempotent publish re-emitted report.published")
	}
	if got := fixture.events.count("report.published"); got != 0 {
		t.Fatalf("events = %d", got)
	}
	// The wave backfill is still ensured on retry (crash between finalize and
	// the run_wave update is healed).
	assertLogOrder(t, *fixture.log, "repo.set-wave:12:"+fmt.Sprint(report.ID))
}

func TestReportServicePublishRenderFailureMarksFailed(t *testing.T) {
	fixture := newReportFixture(t)
	report, revision := fixture.seedWaveReport(7, 12)
	fixture.renderer.err = errors.New("template exploded")

	if _, err := fixture.svc.Publish(context.Background(), report.ID, revision.Revision, ""); err == nil {
		t.Fatal("Publish with a renderer error must fail")
	}
	failed := fixture.repoRevision(t, report.ID, revision.Revision)
	if failed.RenderStatus != "failed" || failed.Status != delivery.ReportStatusDraft {
		t.Fatalf("revision after render failure = %+v", failed)
	}
	stored, err := fixture.repo.GetReport(context.Background(), 7, report.ID)
	if err != nil || stored.Status != delivery.ReportStatusDraft || stored.CurrentRevisionID != nil {
		t.Fatalf("report after render failure = %+v err=%v", stored, err)
	}
	if fixture.content.putCalls != 0 {
		t.Fatalf("content written after render failure: %d", fixture.content.putCalls)
	}
	if fixture.events.count("report.published") != 0 || len(fixture.audit.calls) != 0 {
		t.Fatal("render failure still emitted event/audit")
	}
}

func TestReportServicePublishContentFailureMarksFailed(t *testing.T) {
	fixture := newReportFixture(t)
	report, revision := fixture.seedWaveReport(7, 12)
	fixture.content.putErr = errors.New("content ledger down")

	if _, err := fixture.svc.Publish(context.Background(), report.ID, revision.Revision, ""); err == nil {
		t.Fatal("Publish with a content error must fail")
	}
	failed := fixture.repoRevision(t, report.ID, revision.Revision)
	if failed.RenderStatus != "failed" {
		t.Fatalf("revision after content failure = %+v", failed)
	}
	stored, _ := fixture.repo.GetReport(context.Background(), 7, report.ID)
	if stored.Status != delivery.ReportStatusDraft {
		t.Fatalf("report published after content failure: %+v", stored)
	}
}

func TestReportServicePublishRejectsInvalidArguments(t *testing.T) {
	fixture := newReportFixture(t)
	report, _ := fixture.seedWaveReport(7, 12)

	if _, err := fixture.svc.Publish(context.Background(), 0, 1, ""); platform.AsError(err).Status != 400 {
		t.Fatalf("report_id 0 error = %v", err)
	}
	if _, err := fixture.svc.Publish(context.Background(), report.ID, 0, ""); platform.AsError(err).Status != 400 {
		t.Fatalf("revision 0 error = %v", err)
	}
	if _, err := fixture.svc.Publish(context.Background(), 999999, 1, ""); platform.AsError(err).Status != 404 {
		t.Fatalf("missing report error = %v", err)
	}
	if _, err := fixture.svc.Publish(context.Background(), report.ID, 99, ""); platform.AsError(err).Status != 404 {
		t.Fatalf("missing revision error = %v", err)
	}
}

func TestReportServiceEnsureWaveReportCreatesDraftAndTask(t *testing.T) {
	fixture := newReportFixture(t)
	fixture.data.wave[reportDataWaveKey(7, 12)] = reportTestWaveSnapshot(7, 12)

	first, err := fixture.svc.EnsureWaveReport(context.Background(), 7, 12)
	if err != nil {
		t.Fatalf("EnsureWaveReport: %v", err)
	}
	if first.ID == 0 || first.Kind != delivery.ReportKindWave || first.Status != delivery.ReportStatusDraft {
		t.Fatalf("ensured report = %+v", first)
	}
	revisions, err := fixture.repo.ListRevisions(context.Background(), first.ID)
	if err != nil || len(revisions) != 1 {
		t.Fatalf("revisions = %+v err=%v", revisions, err)
	}
	if revisions[0].Revision != 1 || revisions[0].Status != delivery.ReportStatusDraft ||
		revisions[0].RenderStatus != "pending" || revisions[0].InputFingerprint == "" {
		t.Fatalf("draft revision = %+v", revisions[0])
	}
	if len(fixture.tasks.calls) != 1 || fixture.tasks.calls[0] != fmt.Sprintf("task:%d:1", first.ID) {
		t.Fatalf("tasks = %v", fixture.tasks.calls)
	}

	// A second ensure while the draft is pending reuses the revision and
	// ensures the same task (idempotent reconciler call).
	second, err := fixture.svc.EnsureWaveReport(context.Background(), 7, 12)
	if err != nil || second.ID != first.ID {
		t.Fatalf("second EnsureWaveReport = %+v err=%v", second, err)
	}
	revisions, _ = fixture.repo.ListRevisions(context.Background(), first.ID)
	if len(revisions) != 1 {
		t.Fatalf("second ensure created revisions: %d", len(revisions))
	}
	if len(fixture.tasks.calls) != 2 || fixture.tasks.calls[1] != fixture.tasks.calls[0] {
		t.Fatalf("tasks after second ensure = %v", fixture.tasks.calls)
	}

	// A failed draft is left for audit; the next ensure allocates a new
	// revision and task.
	if err := fixture.repo.MarkRenderMetadata(context.Background(), revisions[0].ID, "failed", "boom"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	third, err := fixture.svc.EnsureWaveReport(context.Background(), 7, 12)
	if err != nil || third.ID != first.ID {
		t.Fatalf("third EnsureWaveReport = %+v err=%v", third, err)
	}
	revisions, _ = fixture.repo.ListRevisions(context.Background(), first.ID)
	if len(revisions) != 2 || revisions[0].Revision != 2 || revisions[1].Status != delivery.ReportStatusDraft {
		t.Fatalf("revisions after failed draft = %+v", revisions)
	}
	if fixture.tasks.calls[len(fixture.tasks.calls)-1] != fmt.Sprintf("task:%d:2", first.ID) {
		t.Fatalf("tasks after failed draft = %v", fixture.tasks.calls)
	}
}

func TestReportServiceEnsureVersionReportCreatesDraftAndTask(t *testing.T) {
	fixture := newReportFixture(t)
	fixture.data.version[7] = reportTestVersionSnapshot(7)

	report, err := fixture.svc.EnsureVersionReport(context.Background(), 7)
	if err != nil {
		t.Fatalf("EnsureVersionReport: %v", err)
	}
	if report.Kind != delivery.ReportKindVersion || report.WaveID != nil || report.Status != delivery.ReportStatusDraft {
		t.Fatalf("ensured version report = %+v", report)
	}
	revisions, err := fixture.repo.ListRevisions(context.Background(), report.ID)
	if err != nil || len(revisions) != 1 || revisions[0].Revision != 1 {
		t.Fatalf("version revisions = %+v err=%v", revisions, err)
	}
	if len(fixture.tasks.calls) != 1 || fixture.tasks.calls[0] != fmt.Sprintf("task:%d:1", report.ID) {
		t.Fatalf("tasks = %v", fixture.tasks.calls)
	}
}

func TestReportServiceRegenerateCreatesNextRevision(t *testing.T) {
	fixture := newReportFixture(t)
	fingerprint := reportTestFingerprint("a")
	report, _ := fixture.seedPublishedWaveReport(7, 12, fingerprint)

	next, err := fixture.svc.Regenerate(context.Background(), 7, report.ID)
	if err != nil || next != 2 {
		t.Fatalf("Regenerate = %d err=%v, want 2", next, err)
	}
	revision := fixture.repoRevision(t, report.ID, 2)
	if revision.Status != delivery.ReportStatusDraft || revision.RenderStatus != "pending" || revision.InputFingerprint == "" {
		t.Fatalf("regenerated revision = %+v", revision)
	}
	if fixture.tasks.calls[len(fixture.tasks.calls)-1] != fmt.Sprintf("task:%d:2", report.ID) {
		t.Fatalf("tasks = %v", fixture.tasks.calls)
	}
	// Old published revision stays published and readable.
	old := fixture.repoRevision(t, report.ID, 1)
	if old.Status != delivery.ReportStatusPublished || old.HTMLRef == nil || *old.HTMLRef != 5001 {
		t.Fatalf("old revision changed: %+v", old)
	}

	// A pending next revision is idempotent: the same revision/task is reused.
	again, err := fixture.svc.Regenerate(context.Background(), 7, report.ID)
	if err != nil || again != 2 {
		t.Fatalf("second Regenerate = %d err=%v, want 2", again, err)
	}
	revisions, _ := fixture.repo.ListRevisions(context.Background(), report.ID)
	if len(revisions) != 2 {
		t.Fatalf("second regenerate created revisions: %d", len(revisions))
	}
	if got := fixture.tasks.calls[len(fixture.tasks.calls)-1]; got != fmt.Sprintf("task:%d:2", report.ID) {
		t.Fatalf("task after second regenerate = %q", got)
	}

	// A failed draft is skipped and the next number is used.
	if err := fixture.repo.MarkRenderMetadata(context.Background(), revision.ID, "failed", "boom"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	third, err := fixture.svc.Regenerate(context.Background(), 7, report.ID)
	if err != nil || third != 3 {
		t.Fatalf("third Regenerate = %d err=%v, want 3", third, err)
	}
}

func TestReportServiceMarkStaleIfChangedOnlyCurrentOnce(t *testing.T) {
	fixture := newReportFixture(t)
	// The wave report's stored fingerprint matches the recomputed one.
	waveReport, _ := fixture.seedPublishedWaveReport(7, 12, "")
	waveSnapshot := reportTestWaveSnapshot(7, 12)
	waveReport.InputFingerprint = delivery.Fingerprint(waveSnapshot)
	fixture.repo.reports[waveReport.ID] = waveReport
	// The version report is stale (stored fingerprint all "a").
	versionReport, versionRevision := fixture.seedPublishedVersionReport(7, reportTestFingerprint("a"))

	changed, err := fixture.svc.MarkStaleIfChanged(context.Background(), 7)
	if err != nil || !changed {
		t.Fatalf("MarkStaleIfChanged = %v err=%v, want true", changed, err)
	}
	waveAfter, _ := fixture.repo.GetReportByID(context.Background(), waveReport.ID)
	if waveAfter.Status != delivery.ReportStatusPublished {
		t.Fatalf("matching wave report was staled: %+v", waveAfter)
	}
	versionAfter, _ := fixture.repo.GetReportByID(context.Background(), versionReport.ID)
	if versionAfter.Status != delivery.ReportStatusStale {
		t.Fatalf("changed version report = %+v, want stale", versionAfter)
	}
	// The published revision itself remains published (content immutable).
	revisionAfter := fixture.repoRevision(t, versionReport.ID, versionRevision.Revision)
	if revisionAfter.Status != delivery.ReportStatusPublished {
		t.Fatalf("stale marking changed the revision: %+v", revisionAfter)
	}
	if got := fixture.events.count("report.stale"); got != 1 {
		t.Fatalf("report.stale events = %d, want 1", got)
	}
	if event := fixture.events.published[0]; event.payload["reason"] != "input_changed" ||
		event.payload["kind"] != "version" || event.payload["current_revision"] != int32(1) {
		t.Fatalf("report.stale event = %+v", event)
	}

	// Second pass: already stale, so no change and no duplicate event.
	changed, err = fixture.svc.MarkStaleIfChanged(context.Background(), 7)
	if err != nil || changed {
		t.Fatalf("second MarkStaleIfChanged = %v err=%v, want false", changed, err)
	}
	if got := fixture.events.count("report.stale"); got != 1 {
		t.Fatalf("report.stale events after second pass = %d, want 1", got)
	}
}

func TestReportServiceOpenHTMLValidatesOwnership(t *testing.T) {
	fixture := newReportFixture(t)
	fingerprint := reportTestFingerprint("a")
	report, revision := fixture.seedPublishedWaveReport(7, 12, fingerprint)
	html := []byte("<!doctype html><html><body>report</body></html>")
	fixture.content.seed(5001, revision.ID, html)

	got, gotRevision, err := fixture.svc.OpenHTML(context.Background(), 7, report.ID, revision.Revision)
	if err != nil {
		t.Fatalf("OpenHTML: %v", err)
	}
	if string(got) != string(html) || gotRevision.ID != revision.ID {
		t.Fatalf("OpenHTML = %q revision=%+v", got, gotRevision)
	}
	if fixture.content.lastOpenID != 5001 {
		t.Fatalf("opened content ref = %d, want 5001", fixture.content.lastOpenID)
	}

	// Version ownership: a report from another version is not found.
	if _, _, err := fixture.svc.OpenHTML(context.Background(), 8, report.ID, revision.Revision); platform.AsError(err).Status != 404 {
		t.Fatalf("wrong version error = %v, want 404", err)
	}
	// A draft revision is not readable.
	draft := fixture.repo.seedRevision(delivery.ReportRevision{ReportID: report.ID, Revision: 2})
	if _, _, err := fixture.svc.OpenHTML(context.Background(), 7, report.ID, draft.Revision); platform.AsError(err).Status != 404 {
		t.Fatalf("draft revision error = %v, want 404", err)
	}
	// Missing revision is not found.
	if _, _, err := fixture.svc.OpenHTML(context.Background(), 7, report.ID, 99); platform.AsError(err).Status != 404 {
		t.Fatalf("missing revision error = %v, want 404", err)
	}
}

func TestReportServiceOpenHTMLMapsContentFailures(t *testing.T) {
	fixture := newReportFixture(t)
	report, revision := fixture.seedPublishedWaveReport(7, 12, reportTestFingerprint("a"))

	fixture.content.openErr = ErrReportContentNotFound
	if _, _, err := fixture.svc.OpenHTML(context.Background(), 7, report.ID, revision.Revision); platform.AsError(err).Status != 404 {
		t.Fatalf("missing content error = %v, want 404", err)
	}
	fixture.content.openErr = ErrReportContentCorrupt
	_, _, err := fixture.svc.OpenHTML(context.Background(), 7, report.ID, revision.Revision)
	if platform.AsError(err).Status != 409 || platform.AsError(err).Details["reason"] != "report_content_corrupt" {
		t.Fatalf("corrupt content error = %#v", platform.AsError(err))
	}
	// A returned SHA that is not a lowercase hex sha256 is also a 409.
	fixture.content.openErr = nil
	html := []byte("bytes")
	fixture.content.seed(5001, revision.ID, html)
	fixture.content.openSHA = "not-a-sha"
	if _, _, err := fixture.svc.OpenHTML(context.Background(), 7, report.ID, revision.Revision); platform.AsError(err).Status != 409 {
		t.Fatalf("sha mismatch error = %v, want 409", err)
	}
}

func TestReportServiceOpenHTMLOldRevisionStaysReadable(t *testing.T) {
	fixture := newReportFixture(t)
	report, first := fixture.seedPublishedWaveReport(7, 12, reportTestFingerprint("a"))
	second := fixture.repo.seedRevision(delivery.ReportRevision{
		ReportID: report.ID, Revision: 2, Status: delivery.ReportStatusPublished,
		RenderStatus: "done", HTMLRef: func() *int64 { value := int64(5002); return &value }(),
		InputFingerprint: reportTestFingerprint("b"),
	})
	fixture.content.seed(5001, first.ID, []byte("first"))
	fixture.content.seed(5002, second.ID, []byte("second"))

	firstBody, _, err := fixture.svc.OpenHTML(context.Background(), 7, report.ID, 1)
	if err != nil || string(firstBody) != "first" {
		t.Fatalf("old revision read = %q err=%v", firstBody, err)
	}
	secondBody, _, err := fixture.svc.OpenHTML(context.Background(), 7, report.ID, 2)
	if err != nil || string(secondBody) != "second" {
		t.Fatalf("new revision read = %q err=%v", secondBody, err)
	}
}

func TestReportServicePublishesEmptySnapshot(t *testing.T) {
	fixture := newReportFixture(t)
	fixture.data.wave[reportDataWaveKey(7, 12)] = delivery.ReportSnapshot{Kind: delivery.ReportKindWave, VersionID: 7}
	report, err := fixture.svc.EnsureWaveReport(context.Background(), 7, 12)
	if err != nil {
		t.Fatalf("EnsureWaveReport empty: %v", err)
	}
	revision := fixture.repoRevision(t, report.ID, 1)
	published, err := fixture.svc.Publish(context.Background(), report.ID, revision.Revision, "")
	if err != nil {
		t.Fatalf("Publish empty snapshot: %v", err)
	}
	if published.InputFingerprint == "" || len(published.InputFingerprint) != 64 {
		t.Fatalf("empty snapshot fingerprint = %q", published.InputFingerprint)
	}
	if fixture.renderer.lastSnapshot.Issues == nil && fixture.renderer.lastSnapshot.Waves == nil {
		// nil slices are acceptable; the renderer normalizes them to empty
		// sections. This assertion only documents that empty data is valid.
	}
	if fixture.content.putCalls != 1 {
		t.Fatalf("empty snapshot content writes = %d", fixture.content.putCalls)
	}
}

func TestReportServiceCapsOversizedSnapshot(t *testing.T) {
	fixture := newReportFixture(t)
	snapshot := delivery.ReportSnapshot{Kind: delivery.ReportKindWave, VersionID: 7}
	waveID := int64(12)
	snapshot.WaveID = &waveID
	for id := int64(delivery.MaxReportIssues + 2); id >= 1; id-- {
		snapshot.Issues = append(snapshot.Issues, delivery.IssueSnapshot{ID: id, VersionID: 7, Title: "issue"})
	}
	for id := int64(delivery.MaxReportWaves + 1); id >= 1; id-- {
		snapshot.Waves = append(snapshot.Waves, delivery.WaveSnapshot{ID: id, VersionID: 7, Status: "done"})
	}
	for id := int64(delivery.MaxReportCaseRuns + 3); id >= 1; id-- {
		snapshot.CaseRuns = append(snapshot.CaseRuns, delivery.CaseRunSnapshot{ID: id, VersionID: 7, WaveID: 12})
	}
	fixture.data.wave[reportDataWaveKey(7, 12)] = snapshot
	report, err := fixture.svc.EnsureWaveReport(context.Background(), 7, 12)
	if err != nil {
		t.Fatalf("EnsureWaveReport: %v", err)
	}
	if _, err := fixture.svc.Publish(context.Background(), report.ID, 1, ""); err != nil {
		t.Fatalf("Publish oversized: %v", err)
	}
	captured := fixture.renderer.lastSnapshot
	if len(captured.Issues) != delivery.MaxReportIssues ||
		len(captured.Waves) != delivery.MaxReportWaves ||
		len(captured.CaseRuns) != delivery.MaxReportCaseRuns {
		t.Fatalf("caps not applied: issues=%d waves=%d case_runs=%d",
			len(captured.Issues), len(captured.Waves), len(captured.CaseRuns))
	}
	want := []string{delivery.ReportTruncationIssues, delivery.ReportTruncationWaves, delivery.ReportTruncationCaseRuns}
	if len(captured.TruncatedSections) != len(want) {
		t.Fatalf("TruncatedSections = %v, want %v", captured.TruncatedSections, want)
	}
	for index := range want {
		if captured.TruncatedSections[index] != want[index] {
			t.Fatalf("TruncatedSections = %v, want %v", captured.TruncatedSections, want)
		}
	}
	if captured.Issues[0].ID != 1 || captured.Issues[len(captured.Issues)-1].ID != delivery.MaxReportIssues {
		t.Fatalf("issues are not the first N by id: first=%d last=%d",
			captured.Issues[0].ID, captured.Issues[len(captured.Issues)-1].ID)
	}
	stored, err := fixture.repo.GetReport(context.Background(), 7, report.ID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if stored.InputFingerprint != delivery.Fingerprint(captured) {
		t.Fatal("stored fingerprint is not the fingerprint of the capped snapshot")
	}
}

func TestReportServiceEnsureReports(t *testing.T) {
	t.Run("creates terminal wave reports only", func(t *testing.T) {
		fixture := newReportFixture(t)
		versionID := int64(7)
		terminalA, terminalB := int64(11), int64(12)
		active := int64(13)
		fixture.data.version[versionID] = delivery.ReportSnapshot{
			Kind: delivery.ReportKindVersion, VersionID: versionID,
			Requirements: []delivery.RequirementSnapshot{{ID: 101, VersionID: versionID, Status: "draft"}},
			Waves: []delivery.WaveSnapshot{
				{ID: terminalA, VersionID: versionID, Status: "done"},
				{ID: terminalB, VersionID: versionID, Status: "failed"},
				{ID: active, VersionID: versionID, Status: "running"},
			},
		}
		fixture.data.wave[reportDataWaveKey(versionID, terminalA)] = reportTestWaveSnapshot(versionID, terminalA)
		fixture.data.wave[reportDataWaveKey(versionID, terminalB)] = reportTestWaveSnapshot(versionID, terminalB)

		if err := fixture.svc.EnsureReports(context.Background(), versionID); err != nil {
			t.Fatalf("EnsureReports: %v", err)
		}
		reports, err := fixture.repo.ListReports(context.Background(), versionID, ReportFilter{Limit: 100})
		if err != nil {
			t.Fatalf("ListReports: %v", err)
		}
		if len(reports) != 2 {
			t.Fatalf("reports = %d, want 2 terminal wave reports", len(reports))
		}
		for _, report := range reports {
			if report.Kind != delivery.ReportKindWave {
				t.Fatalf("unexpected report kind: %+v", report)
			}
		}
		if len(fixture.tasks.calls) != 2 {
			t.Fatalf("tasks = %v, want 2", fixture.tasks.calls)
		}
	})

	t.Run("creates version report when all requirements closed and no active wave", func(t *testing.T) {
		fixture := newReportFixture(t)
		versionID := int64(7)
		fixture.data.version[versionID] = reportTestVersionSnapshot(versionID)
		fixture.data.wave[reportDataWaveKey(versionID, 12)] = reportTestWaveSnapshot(versionID, 12)

		if err := fixture.svc.EnsureReports(context.Background(), versionID); err != nil {
			t.Fatalf("EnsureReports: %v", err)
		}
		versionKind := delivery.ReportKindVersion
		versions, err := fixture.repo.ListReports(context.Background(), versionID, ReportFilter{Kind: &versionKind, Limit: 100})
		if err != nil || len(versions) != 1 {
			t.Fatalf("version reports = %+v err=%v", versions, err)
		}
		if len(fixture.tasks.calls) < 1 {
			t.Fatal("version report task not ensured")
		}
	})

	t.Run("skips the version report while an active intent blocks generation", func(t *testing.T) {
		fixture := newReportFixture(t)
		versionID := int64(7)
		fixture.data.version[versionID] = reportTestVersionSnapshot(versionID)
		fixture.data.wave[reportDataWaveKey(versionID, 12)] = reportTestWaveSnapshot(versionID, 12)
		notReady := false
		fixture.data.versionReady = &notReady

		if err := fixture.svc.EnsureReports(context.Background(), versionID); err != nil {
			t.Fatalf("EnsureReports: %v", err)
		}
		versionKind := delivery.ReportKindVersion
		versions, err := fixture.repo.ListReports(context.Background(), versionID, ReportFilter{Kind: &versionKind, Limit: 100})
		if err != nil {
			t.Fatalf("ListReports: %v", err)
		}
		if len(versions) != 0 {
			t.Fatalf("version report created while an active intent blocks generation: %+v", versions)
		}
	})

	t.Run("regenerates a stale version report", func(t *testing.T) {
		fixture := newReportFixture(t)
		versionID := int64(7)
		report, _ := fixture.seedPublishedVersionReport(versionID, reportTestFingerprint("a"))
		// The reader returns a different fingerprint than the stored one.
		fixture.data.version[versionID] = reportTestVersionSnapshot(versionID)
		fixture.data.wave[reportDataWaveKey(versionID, 12)] = reportTestWaveSnapshot(versionID, 12)

		if err := fixture.svc.EnsureReports(context.Background(), versionID); err != nil {
			t.Fatalf("EnsureReports: %v", err)
		}
		revisions, err := fixture.repo.ListRevisions(context.Background(), report.ID)
		if err != nil || len(revisions) != 2 {
			t.Fatalf("revisions = %+v err=%v, want a regeneration revision", revisions, err)
		}
		if fixture.tasks.calls[len(fixture.tasks.calls)-1] != fmt.Sprintf("task:%d:2", report.ID) {
			t.Fatalf("tasks = %v", fixture.tasks.calls)
		}
	})
}

func TestReportServiceListAndGet(t *testing.T) {
	fixture := newReportFixture(t)
	waveReport, waveRevision := fixture.seedWaveReport(7, 12)
	versionReport := fixture.repo.seedReport(delivery.Report{ID: 200, VersionID: 7, Kind: delivery.ReportKindVersion})
	if err := fixture.repo.InsertRefs(context.Background(), waveRevision.ID, []delivery.ReportRef{{Kind: "wave", RefID: 12}}); err != nil {
		t.Fatalf("InsertRefs: %v", err)
	}

	waveKind := delivery.ReportKindWave
	waves, err := fixture.svc.List(context.Background(), 7, &waveKind)
	if err != nil || len(waves) != 1 || waves[0].ID != waveReport.ID {
		t.Fatalf("List waves = %+v err=%v", waves, err)
	}
	all, err := fixture.svc.List(context.Background(), 7, nil)
	if err != nil || len(all) != 2 {
		t.Fatalf("List all = %+v err=%v", all, err)
	}
	if _, err := fixture.svc.List(context.Background(), 0, nil); platform.AsError(err).Status != 400 {
		t.Fatalf("List version 0 error = %v", err)
	}

	detail, err := fixture.svc.Get(context.Background(), 7, waveReport.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if detail.Report.ID != waveReport.ID || len(detail.Revisions) != 1 || len(detail.Refs) != 1 {
		t.Fatalf("detail = %+v", detail)
	}
	if _, err := fixture.svc.Get(context.Background(), 8, waveReport.ID); platform.AsError(err).Status != 404 {
		t.Fatalf("Get wrong version error = %v", err)
	}
	if _, err := fixture.svc.Get(context.Background(), 7, versionReport.ID); err != nil {
		t.Fatalf("Get version report: %v", err)
	}
}
