//go:build integration

package deliveryapp_test

// v0.2.4-G T11 (SC-G8, R-G10): real-PostgreSQL proof of the complete sweep's scan
// source and healing. The lister returns EVERY version with a published report
// (no version-status join — completed versions included), paginated and filtered
// by the published domain constant; the sweep heals a lost-event input change.

import (
	"context"
	"testing"
	"time"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	deliveryadapters "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/adapters"
	contentstore "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/content"
	deliverypg "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/render"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	eventingpg "github.com/EdouardRichard/EzTest/backend/internal/eventing/infrastructure"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	storagepg "github.com/EdouardRichard/EzTest/backend/internal/storage/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// TestPublishedReportVersionListerIntegration pins the sweep scan SQL: distinct
// published-report versions ascending, draft-only versions excluded, pagination
// by afterVersionID, and NO join on version status (versions 8/9 have no version
// row at all — the logical FK is not enforced — yet are still scanned).
func TestPublishedReportVersionListerIntegration(t *testing.T) {
	fixture := newIntegrationReportFixture(t)
	ctx := context.Background()
	// Published report headers for versions 7, 8, 9; a draft-only version 10.
	for _, row := range []struct {
		id        int64
		versionID int64
		status    string
	}{
		{9001, 7, string(delivery.ReportStatusPublished)},
		{9002, 8, string(delivery.ReportStatusPublished)},
		{9003, 9, string(delivery.ReportStatusPublished)},
		{9004, 10, string(delivery.ReportStatusDraft)},
	} {
		if _, err := fixture.pool.Exec(ctx, `
			INSERT INTO delivery_report (id, version_id, wave_id, kind, status, input_fingerprint, created_at, updated_at)
			VALUES ($1, $2, NULL, 'version', $3, '', now(), now())
		`, row.id, row.versionID, row.status); err != nil {
			t.Fatalf("insert report header %d: %v", row.id, err)
		}
	}

	ids, err := fixture.repo.ListVersionsWithPublishedReports(ctx, 0, 100)
	if err != nil {
		t.Fatalf("ListVersionsWithPublishedReports: %v", err)
	}
	want := []int64{7, 8, 9}
	if len(ids) != len(want) {
		t.Fatalf("published versions = %v, want %v (draft-only 10 excluded)", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("published versions = %v, want %v (ascending)", ids, want)
		}
	}

	// Pagination: after 7 returns 8, 9.
	page, err := fixture.repo.ListVersionsWithPublishedReports(ctx, 7, 100)
	if err != nil || len(page) != 2 || page[0] != 8 || page[1] != 9 {
		t.Fatalf("page after 7 = %v err=%v, want [8 9]", page, err)
	}
	// A small limit truncates the page (the sweep keeps paging until a short page).
	short, err := fixture.repo.ListVersionsWithPublishedReports(ctx, 0, 2)
	if err != nil || len(short) != 2 || short[0] != 7 || short[1] != 8 {
		t.Fatalf("limit-2 page = %v err=%v, want [7 8]", short, err)
	}
}

// TestPublishedReportVersionListerIncludesStale pins final fix wave #5 (SC-G8
// "扫所有已发布报告"): the sweep scan source covers STALE reports too, not only
// published ones. A stale report still has a published current revision
// (reportHasPublishedRevision accepts published OR stale), so the display healer
// (RefreshDisplayIfChanged) must still reach it — otherwise a completed version
// whose report went stale and whose judge event was lost would never heal its
// display. The canonical MarkStaleIfChanged no-ops on an already-stale report, so
// including stale versions is safe (no duplicate stale events).
func TestPublishedReportVersionListerIncludesStale(t *testing.T) {
	fixture := newIntegrationReportFixture(t)
	ctx := context.Background()
	for _, row := range []struct {
		id        int64
		versionID int64
		status    string
	}{
		{9101, 7, string(delivery.ReportStatusPublished)},
		{9102, 8, string(delivery.ReportStatusStale)},
		{9103, 9, string(delivery.ReportStatusDraft)},
	} {
		if _, err := fixture.pool.Exec(ctx, `
			INSERT INTO delivery_report (id, version_id, wave_id, kind, status, input_fingerprint, created_at, updated_at)
			VALUES ($1, $2, NULL, 'version', $3, '', now(), now())
		`, row.id, row.versionID, row.status); err != nil {
			t.Fatalf("insert report header %d: %v", row.id, err)
		}
	}
	ids, err := fixture.repo.ListVersionsWithPublishedReports(ctx, 0, 100)
	if err != nil {
		t.Fatalf("ListVersionsWithPublishedReports: %v", err)
	}
	want := []int64{7, 8}
	if len(ids) != len(want) {
		t.Fatalf("sweep scan versions = %v, want %v (published 7 + stale 8; draft-only 9 excluded)", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("sweep scan versions = %v, want %v (ascending)", ids, want)
		}
	}
}

// TestSweepHealsLostEventIntegration: a published wave report whose input changed
// with NO event (lost) is marked stale by the complete sweep over the real repo.
func TestSweepHealsLostEventIntegration(t *testing.T) {
	fixture := newIntegrationReportFixture(t)
	ctx := context.Background()
	report, err := fixture.svc.EnsureWaveReport(ctx, 7, 12)
	if err != nil {
		t.Fatalf("EnsureWaveReport: %v", err)
	}
	if _, err := fixture.svc.Publish(ctx, report.ID, 1, ""); err != nil {
		t.Fatalf("Publish r1: %v", err)
	}
	// Conclusion input changes; no event is delivered (lost).
	fixture.data.wave.Issues[0].Status = delivery.IssueStatusConfirmed

	sweeper := deliveryapp.NewReportStaleSweeper(fixture.repo, fixture.svc, nil)
	stats, err := sweeper.Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if stats.Staled != 1 {
		t.Fatalf("sweep staled = %d, want 1 (lost event healed)", stats.Staled)
	}
	stored, err := fixture.repo.GetReport(ctx, 7, report.ID)
	if err != nil || stored.Status != delivery.ReportStatusStale {
		t.Fatalf("report after sweep = %+v err=%v, want stale", stored, err)
	}
	// Idempotent: a second sweep marks nothing new.
	stats2, err := sweeper.Sweep(ctx)
	if err != nil || stats2.Staled != 0 {
		t.Fatalf("second sweep = %+v err=%v, want staled=0", stats2, err)
	}
}

// TestCausalStaleRealProducerIntegration (fix round 1 minor #5, discipline ⑨):
// a REAL producer (IssueService.Confirm) publishes issue.updated through the REAL
// eventing service; the watcher observer registered on that service enqueues the
// version and the drain marks the changed published report stale. No hand-built
// event Scope — the event flows through the production publish→observe path.
func TestCausalStaleRealProducerIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO run_wave (id, version_id, status, trigger) VALUES (12, 7, 'done', 'manual')`); err != nil {
		t.Fatalf("insert run wave: %v", err)
	}
	ids, err := platformid.NewSnowflake(0)
	if err != nil {
		t.Fatalf("new snowflake: %v", err)
	}
	eventSvc := eventingapp.New(eventingpg.New(pool), eventingapp.NewHub(eventingapp.DefaultHubBuffer))
	storageSvc := storageapp.New(storagepg.New(pool), storagepg.NewFileStore(), integrationReportSettings{root: t.TempDir()}, ids, nil, nil)
	renderer, err := render.NewHTMLRenderer(render.TemplateVersionV1)
	if err != nil {
		t.Fatalf("new renderer: %v", err)
	}
	data := &integrationReportData{wave: integrationWaveSnapshot()}
	clock := integrationReportClock{now: time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)}
	reportRepo := deliverypg.NewReportRepository(pool, ids)
	reportSvc := deliveryapp.NewReportService(
		reportRepo, renderer,
		contentstore.New(storageSvc, deliverypg.NewContentRefReader(pool)),
		data, deliveryadapters.NewEventPublisher(eventSvc), &integrationReportAudit{}, clock, &integrationReportTasks{},
		delivery.SectionOptions{Summary: true, Results: true, Issues: true, Coverage: true, Evidence: true},
	)
	watcher := deliveryapp.NewReportStaleWatcher(reportSvc, reportSvc, nil, nil)
	eventSvc.Observe(watcher.Observe)

	report, err := reportSvc.EnsureWaveReport(ctx, 7, 12)
	if err != nil {
		t.Fatalf("EnsureWaveReport: %v", err)
	}
	if _, err := reportSvc.Publish(ctx, report.ID, 1, ""); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Real issue producer wired to the SAME real eventing service.
	issueSvc := deliveryapp.NewIssueService(
		deliverypg.NewIssueRepository(pool, ids),
		&integrationIssueExecution{snapshots: map[int64]deliveryapp.CaseRunSnapshot{}, waveSnapshots: map[int64]deliveryapp.WaveSnapshot{}},
		deliveryadapters.NewEventPublisher(eventSvc), &integrationIssueAudit{},
		integrationIssueClock{now: clock.now}, integrationIssueTasks{},
	)
	issue, err := issueSvc.CreateManual(ctx, deliveryapp.CreateManualIssueInput{VersionID: 7, Title: "结算按钮", Summary: "缺陷", Category: "ui", Severity: "major"}, 1)
	if err != nil {
		t.Fatalf("CreateManual: %v", err)
	}

	// The report's conclusion input changes (issue status in the wave snapshot).
	data.wave.Issues[0].Status = delivery.IssueStatusConfirmed
	// REAL producer publishes issue.updated -> real eventing -> watcher.Observe.
	if _, err := issueSvc.Confirm(ctx, 7, issue.ID, 1, "确认", nil); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if err := watcher.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	stored, err := reportRepo.GetReport(ctx, 7, report.ID)
	if err != nil || stored.Status != delivery.ReportStatusStale {
		t.Fatalf("report after real producer event = %+v err=%v, want stale", stored, err)
	}
}
