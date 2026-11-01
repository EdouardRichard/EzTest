//go:build integration

package deliveryapp_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	contentstore "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/content"
	deliverypg "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/render"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	storagepg "github.com/EdouardRichard/EzTest/backend/internal/storage/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

type integrationReportSettings struct{ root string }

func (s integrationReportSettings) String(context.Context, string) (string, error) {
	return s.root, nil
}

type integrationReportData struct {
	wave delivery.ReportSnapshot
}

func (d *integrationReportData) WaveSnapshot(context.Context, int64, int64) (delivery.ReportSnapshot, error) {
	return d.wave, nil
}

func (d *integrationReportData) VersionSnapshot(context.Context, int64) (delivery.ReportSnapshot, error) {
	return delivery.ReportSnapshot{Kind: delivery.ReportKindVersion, VersionID: 7}, nil
}

type integrationReportClock struct{ now time.Time }

func (c integrationReportClock) Now() time.Time { return c.now }

type integrationReportEvents struct {
	types   []string
	payload []map[string]any
}

func (e *integrationReportEvents) Publish(_ context.Context, eventType string, _ int64, payload map[string]any) error {
	e.types = append(e.types, eventType)
	e.payload = append(e.payload, payload)
	return nil
}

type integrationReportAudit struct{ actions []string }

func (a *integrationReportAudit) Write(_ context.Context, _ int64, action, _, _ string, _ map[string]any) error {
	a.actions = append(a.actions, action)
	return nil
}

type integrationReportTasks struct{ calls []string }

func (t *integrationReportTasks) EnsureReportTask(_ context.Context, reportID int64, revision int32) error {
	t.calls = append(t.calls, "report")
	return nil
}
func (t *integrationReportTasks) EnsureIssueTriage(context.Context, int64, int64) error { return nil }
func (t *integrationReportTasks) EnsureRequirementClose(context.Context, int64, int64, int32) error {
	return nil
}
func (t *integrationReportTasks) EnsureVersionClose(context.Context, int64, int64, string) error {
	return nil
}

type integrationReportFixture struct {
	svc     *deliveryapp.ReportService
	repo    *deliverypg.ReportRepository
	pool    *pgxpool.Pool
	data    *integrationReportData
	events  *integrationReportEvents
	audit   *integrationReportAudit
	tasks   *integrationReportTasks
	clock   integrationReportClock
	report  delivery.Report
	htmlRef int64
}

func newIntegrationReportFixture(t *testing.T) *integrationReportFixture {
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
	storageSvc := storageapp.New(
		storagepg.New(pool),
		storagepg.NewFileStore(),
		integrationReportSettings{root: t.TempDir()},
		ids,
		nil,
		nil,
	)
	renderer, err := render.NewHTMLRenderer(render.TemplateVersionV1)
	if err != nil {
		t.Fatalf("new renderer: %v", err)
	}
	data := &integrationReportData{wave: integrationWaveSnapshot()}
	events := &integrationReportEvents{}
	audit := &integrationReportAudit{}
	tasks := &integrationReportTasks{}
	clock := integrationReportClock{now: time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)}
	return &integrationReportFixture{
		svc: deliveryapp.NewReportService(
			deliverypg.NewReportRepository(pool, ids),
			renderer,
			contentstore.New(storageSvc, deliverypg.NewContentRefReader(pool)),
			data,
			events,
			audit,
			clock,
			tasks,
			delivery.SectionOptions{Summary: true, Results: true, Issues: true, Coverage: true, Evidence: true},
		),
		repo:   deliverypg.NewReportRepository(pool, ids),
		pool:   pool,
		data:   data,
		events: events,
		audit:  audit,
		tasks:  tasks,
		clock:  clock,
	}
}

func integrationWaveSnapshot() delivery.ReportSnapshot {
	waveID := int64(12)
	return delivery.ReportSnapshot{
		Kind:      delivery.ReportKindWave,
		VersionID: 7,
		WaveID:    &waveID,
		Waves: []delivery.WaveSnapshot{{
			ID: 12, VersionID: 7, Status: "done", Trigger: "manual",
			FinishedAt: func() *time.Time { value := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC); return &value }(),
		}},
		CaseRuns: []delivery.CaseRunSnapshot{{
			ID: 3001, VersionID: 7, WaveID: 12, CaseVersionID: 4001,
			CaseTitle: "结算提交", Status: "failed", Category: "ui", Severity: "major",
			CreatedAt: time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC),
		}},
		Issues: []delivery.IssueSnapshot{{
			ID: 5001, VersionID: 7, Title: "结算按钮", Status: delivery.IssueStatusOpen,
			Category: "ui", Severity: "major", Source: "analysis",
		}},
	}
}

// TestReportServiceIntegrationPublishRegenerateStale wires the real PostgreSQL
// report repository, content ledger and renderer: publish writes html_ref and
// report refs + run_wave.report_id, HTML download verifies the ledger, and
// regenerating/staling keeps the old revision readable (spec §6.3–§6.5).
func TestReportServiceIntegrationPublishRegenerateStale(t *testing.T) {
	fixture := newIntegrationReportFixture(t)
	ctx := context.Background()

	report, err := fixture.svc.EnsureWaveReport(ctx, 7, 12)
	if err != nil {
		t.Fatalf("EnsureWaveReport: %v", err)
	}
	first, err := fixture.svc.Publish(ctx, report.ID, 1, "第一版叙述")
	if err != nil {
		t.Fatalf("Publish r1: %v", err)
	}
	if first.Status != delivery.ReportStatusPublished || first.HTMLRef == nil {
		t.Fatalf("published r1 = %+v", first)
	}
	stored, err := fixture.repo.GetReport(ctx, 7, report.ID)
	if err != nil || stored.Status != delivery.ReportStatusPublished ||
		stored.CurrentRevisionID == nil || *stored.CurrentRevisionID != first.ID ||
		stored.InputFingerprint != first.InputFingerprint {
		t.Fatalf("stored report = %+v err=%v", stored, err)
	}
	var waveReportID *int64
	if err := fixture.pool.QueryRow(ctx, `SELECT report_id FROM run_wave WHERE id = 12`).Scan(&waveReportID); err != nil {
		t.Fatalf("read run wave: %v", err)
	}
	if waveReportID == nil || *waveReportID != report.ID {
		t.Fatalf("run_wave.report_id = %v, want %d", waveReportID, report.ID)
	}
	refs, err := fixture.repo.ListRefs(ctx, report.ID)
	if err != nil || len(refs) == 0 {
		t.Fatalf("refs = %+v err=%v", refs, err)
	}
	if len(fixture.events.types) != 1 || fixture.events.types[0] != "report.published" {
		t.Fatalf("events = %v", fixture.events.types)
	}
	if len(fixture.audit.actions) != 1 || fixture.audit.actions[0] != "report.published" {
		t.Fatalf("audit = %v", fixture.audit.actions)
	}
	if len(fixture.tasks.calls) != 1 {
		t.Fatalf("report task calls = %v", fixture.tasks.calls)
	}

	firstHTML, firstRevision, err := fixture.svc.OpenHTML(ctx, 7, report.ID, 1)
	if err != nil {
		t.Fatalf("OpenHTML r1: %v", err)
	}
	if firstRevision.ID != first.ID || !bytes.Contains(firstHTML, []byte(`data-issue-id="5001"`)) ||
		!bytes.Contains(firstHTML, []byte("第一版叙述")) {
		t.Fatalf("downloaded HTML is not the published report (%d bytes)", len(firstHTML))
	}

	// Regenerate and publish r2; the old revision and its bytes stay untouched.
	next, err := fixture.svc.Regenerate(ctx, 7, report.ID)
	if err != nil || next != 2 {
		t.Fatalf("Regenerate = %d err=%v, want 2", next, err)
	}
	fixture.data.wave.Issues[0].Title = "结算按钮（新）"
	second, err := fixture.svc.Publish(ctx, report.ID, next, "第二版叙述")
	if err != nil {
		t.Fatalf("Publish r2: %v", err)
	}
	if second.Revision != 2 || second.Status != delivery.ReportStatusPublished {
		t.Fatalf("published r2 = %+v", second)
	}
	current, err := fixture.repo.GetReport(ctx, 7, report.ID)
	if err != nil || current.CurrentRevisionID == nil || *current.CurrentRevisionID != second.ID ||
		current.InputFingerprint == stored.InputFingerprint {
		t.Fatalf("current after r2 = %+v err=%v", current, err)
	}
	oldHTML, _, err := fixture.svc.OpenHTML(ctx, 7, report.ID, 1)
	if err != nil || !bytes.Equal(oldHTML, firstHTML) {
		t.Fatalf("old revision changed after r2: err=%v equal=%v", err, bytes.Equal(oldHTML, firstHTML))
	}
	newHTML, _, err := fixture.svc.OpenHTML(ctx, 7, report.ID, 2)
	if err != nil || !bytes.Contains(newHTML, []byte("第二版叙述")) {
		t.Fatalf("new revision HTML err=%v", err)
	}

	// Download auth: crossed version is 404 and a content row with a broken
	// owner link is 409 (the content adapter re-validates owner/role).
	if _, _, err := fixture.svc.OpenHTML(ctx, 8, report.ID, 1); deliveryappStatus(err) != 404 {
		t.Fatalf("cross-version download error = %v, want 404", err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE content_ref SET role = 'attachment' WHERE id = $1
	`, *second.HTMLRef); err != nil {
		t.Fatalf("tamper content ref: %v", err)
	}
	if _, _, err := fixture.svc.OpenHTML(ctx, 7, report.ID, 2); deliveryappStatus(err) != 409 {
		t.Fatalf("corrupt content error = %v, want 409", err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE content_ref SET role = 'report_html' WHERE id = $1
	`, *second.HTMLRef); err != nil {
		t.Fatalf("restore content ref: %v", err)
	}

	// A changed input marks only the current published report stale, once.
	fixture.data.wave.Issues[0].Status = delivery.IssueStatusConfirmed
	changed, err := fixture.svc.MarkStaleIfChanged(ctx, 7)
	if err != nil || !changed {
		t.Fatalf("MarkStaleIfChanged = %v err=%v, want true", changed, err)
	}
	staleReport, err := fixture.repo.GetReport(ctx, 7, report.ID)
	if err != nil || staleReport.Status != delivery.ReportStatusStale {
		t.Fatalf("stale report = %+v err=%v", staleReport, err)
	}
	changed, err = fixture.svc.MarkStaleIfChanged(ctx, 7)
	if err != nil || changed {
		t.Fatalf("second MarkStaleIfChanged = %v err=%v, want false", changed, err)
	}
	staleEvents := 0
	for _, eventType := range fixture.events.types {
		if eventType == "report.stale" {
			staleEvents++
		}
	}
	if staleEvents != 1 {
		t.Fatalf("report.stale events = %d, want 1", staleEvents)
	}
	// The published revisions themselves remain published and readable.
	revision, err := fixture.repo.GetRevision(ctx, report.ID, 2)
	if err != nil || revision.Status != delivery.ReportStatusPublished {
		t.Fatalf("revision after stale = %+v err=%v", revision, err)
	}
	if _, _, err := fixture.svc.OpenHTML(ctx, 7, report.ID, 2); err != nil {
		t.Fatalf("stale report revision is not readable: %v", err)
	}

	// The service maps the missing-row sentinel; the repository sentinel is
	// still wrapped for direct infrastructure callers.
	if _, err := fixture.repo.GetReportByID(ctx, 999999); !errors.Is(err, deliveryapp.ErrReportNotFound) {
		t.Fatalf("missing report sentinel = %v", err)
	}
}

// deliveryappStatus returns the platform status of an error.
func deliveryappStatus(err error) int {
	return platform.AsError(err).Status
}
