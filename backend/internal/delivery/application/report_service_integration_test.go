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
	storage *storageapp.Service
	exports *storageapp.ExportService
	ids     platformid.Generator
}

// integrationArtifactStore adapts the real storage export service to the
// delivery ReportArtifactStore port, mirroring the production main.go wiring.
type integrationArtifactStore struct {
	exports *storageapp.ExportService
}

func (a integrationArtifactStore) CreateReportArtifact(ctx context.Context, in deliveryapp.ReportArtifactStoreInput) (deliveryapp.ReportArtifactRecord, error) {
	artifact, err := a.exports.Create(ctx, storageapp.CreateExportInput{
		ID:        in.ID,
		VersionID: in.VersionID,
		OwnerID:   in.OwnerID,
		OwnerKind: "report",
		Format:    in.Format,
		MIME:      in.MIME,
		Filename:  in.Filename,
		Data:      in.Data,
	}, in.ActorID)
	if err != nil {
		return deliveryapp.ReportArtifactRecord{}, err
	}
	return deliveryapp.ReportArtifactRecord{
		ID: artifact.ID, Format: artifact.Format, ContentObjectID: artifact.ContentObjectID,
		Size: artifact.Size, Status: artifact.Status,
	}, nil
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
	exports := storageapp.NewExportService(storagepg.NewExportRepository(pool), storageSvc, ids, clock)
	service := deliveryapp.NewReportService(
		deliverypg.NewReportRepository(pool, ids),
		renderer,
		contentstore.New(storageSvc, deliverypg.NewContentRefReader(pool)),
		data,
		events,
		audit,
		clock,
		tasks,
		delivery.SectionOptions{Summary: true, Results: true, Issues: true, Coverage: true, Evidence: true},
	)
	service.WithArtifactStore(integrationArtifactStore{exports: exports}, ids)
	return &integrationReportFixture{
		svc:     service,
		repo:    deliverypg.NewReportRepository(pool, ids),
		pool:    pool,
		data:    data,
		events:  events,
		audit:   audit,
		tasks:   tasks,
		clock:   clock,
		storage: storageSvc,
		exports: exports,
		ids:     ids,
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

// TestReportServiceIntegrationCreateArtifact wires the real PostgreSQL report
// repository, content ledger and storage export service. It proves three
// formats produce export_artifact rows whose content_object_id is the sha256 of
// ledger-backed bytes (no binary in the DB), the content refs exist, the
// export.* events are published, and Markdown is byte-deterministic.
func TestReportServiceIntegrationCreateArtifact(t *testing.T) {
	fixture := newIntegrationReportFixture(t)
	ctx := context.Background()

	report, err := fixture.svc.EnsureWaveReport(ctx, 7, 12)
	if err != nil {
		t.Fatalf("EnsureWaveReport: %v", err)
	}
	revision, err := fixture.svc.Publish(ctx, report.ID, 1, "artifact report")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Attach a ready PDF exactly like the render worker would.
	pdfData := []byte("%PDF-1.4\n% report artifact\n%%EOF\n")
	object, err := fixture.storage.Upload(ctx, "report.pdf", "application/pdf", bytes.NewReader(pdfData), storageapp.MaxUploadBytes, 1)
	if err != nil {
		t.Fatalf("upload PDF: %v", err)
	}
	pdfRef, err := fixture.storage.AddRef(ctx, object.SHA256, storageapp.OwnerKindReportRevision, revision.ID, storageapp.RoleReportPDF)
	if err != nil {
		t.Fatalf("add PDF ref: %v", err)
	}
	if err := fixture.repo.MarkPDFRunning(ctx, revision.ID); err != nil {
		t.Fatalf("MarkPDFRunning: %v", err)
	}
	if err := fixture.repo.MarkPDFRendered(ctx, revision.ID, deliveryapp.ReportContentRef{
		RefID: pdfRef.ID, SHA256: object.SHA256, Size: object.Size,
	}, 1, fixture.clock.now); err != nil {
		t.Fatalf("MarkPDFRendered: %v", err)
	}

	markdownSHA := ""
	artifactIDs := make([]int64, 0, 3)
	formats := make(map[string]string, 3)
	for _, format := range []string{"html", "pdf", "markdown"} {
		artifact, err := fixture.svc.CreateArtifact(ctx, 7, report.ID, format, 9)
		if err != nil {
			t.Fatalf("CreateArtifact %s: %v", format, err)
		}
		if artifact.ID <= 0 || artifact.Status != "ready" || len(artifact.ContentObjectID) != 64 || artifact.Size <= 0 {
			t.Fatalf("%s artifact = %+v", format, artifact)
		}
		artifactIDs = append(artifactIDs, artifact.ID)
		formats[format] = artifact.ContentObjectID
		t.Logf("created artifact format=%s id=%d sha256=%s size=%d status=%s",
			format, artifact.ID, artifact.ContentObjectID, artifact.Size, artifact.Status)
		if format == "markdown" {
			markdownSHA = artifact.ContentObjectID
		}
	}
	again, err := fixture.svc.CreateArtifact(ctx, 7, report.ID, "markdown", 9)
	if err != nil {
		t.Fatalf("CreateArtifact markdown again: %v", err)
	}
	if again.ContentObjectID != markdownSHA {
		t.Fatalf("markdown artifact changed: %s != %s", again.ContentObjectID, markdownSHA)
	}

	rows, err := fixture.pool.Query(ctx, `
		SELECT id, owner_kind, owner_id, revision, file_format, content_object_id, size_bytes, status
		FROM export_artifact
		WHERE version_id = 7
		ORDER BY id
	`)
	if err != nil {
		t.Fatalf("query export_artifact: %v", err)
	}
	defer rows.Close()
	count := 0
	seenFormat := make(map[string]bool, 3)
	for rows.Next() {
		var id, ownerID int64
		var ownerKind, format, sha, status string
		var artifactRevision int
		var size int64
		if err := rows.Scan(&id, &ownerKind, &ownerID, &artifactRevision, &format, &sha, &size, &status); err != nil {
			t.Fatalf("scan export_artifact: %v", err)
		}
		if ownerKind != "report" || ownerID != report.ID || status != "ready" || size <= 0 || len(sha) != 64 {
			t.Fatalf("artifact row = id=%d owner=%s/%d revision=%d format=%s sha=%s size=%d status=%s",
				id, ownerKind, ownerID, artifactRevision, format, sha, size, status)
		}
		if format != "markdown" && artifactRevision != 1 {
			t.Fatalf("%s revision = %d, want 1", format, artifactRevision)
		}
		if format == "markdown" && (artifactRevision < 1 || artifactRevision > 2) {
			t.Fatalf("markdown revision = %d, want 1 or 2", artifactRevision)
		}
		t.Logf("db export_artifact id=%d owner_kind=%s owner_id=%d revision=%d format=%s sha256=%s size=%d status=%s",
			id, ownerKind, ownerID, artifactRevision, format, sha, size, status)
		seenFormat[format] = true
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate export_artifact: %v", err)
	}
	if count != 4 || !seenFormat["html"] || !seenFormat["pdf"] || !seenFormat["markdown"] {
		t.Fatalf("artifact rows = %d formats=%v, want 4 rows / three formats", count, seenFormat)
	}

	for _, artifactID := range artifactIDs {
		var refs int
		if err := fixture.pool.QueryRow(ctx, `
			SELECT count(*) FROM content_ref
			WHERE owner_kind = 'export' AND owner_id = $1 AND role = 'content' AND deleted_at IS NULL
		`, artifactID).Scan(&refs); err != nil {
			t.Fatalf("query content_ref %d: %v", artifactID, err)
		}
		if refs != 1 {
			t.Fatalf("content_ref rows for artifact %d = %d, want 1", artifactID, refs)
		}
		t.Logf("content ledger ref export_artifact=%d owner_kind=export role=content rows=%d", artifactID, refs)
	}

	var binaryColumns int
	if err := fixture.pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name IN ('export_artifact', 'content_object')
		  AND data_type IN ('bytea', 'blob')
	`).Scan(&binaryColumns); err != nil {
		t.Fatalf("query binary columns: %v", err)
	}
	if binaryColumns != 0 {
		t.Fatalf("binary columns in export tables = %d, want 0", binaryColumns)
	}

	started, completed, failed := 0, 0, 0
	for index, eventType := range fixture.events.types {
		switch eventType {
		case "export.started":
			started++
		case "export.completed":
			completed++
			payload := fixture.events.payload[index]
			if payload["status"] != "completed" || payload["report_id"] == "" || payload["format"] == "" ||
				payload["sha256"] == "" || payload["version_id"] == "" {
				t.Fatalf("export.completed payload = %v", payload)
			}
		case "export.failed":
			failed++
		}
	}
	if started != 4 || completed != 4 || failed != 0 {
		t.Fatalf("export events started=%d completed=%d failed=%d, want 4/4/0", started, completed, failed)
	}
	t.Logf("export events started=%d completed=%d failed=%d", started, completed, failed)
}
