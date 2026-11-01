//go:build integration

package deliverypg

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// pdfRepoFixture creates one published wave report revision and returns the
// report/revision together with the repository.
type pdfRepoFixture struct {
	repo     *ReportRepository
	report   delivery.Report
	revision delivery.ReportRevision
	htmlRef  int64
}

func newPDFRepoFixture(t *testing.T, versionID, waveID int64, revision int32, htmlRef int64) *pdfRepoFixture {
	t.Helper()
	repo, _ := newReportRepoForTest(t)
	wave := waveID
	report := reportRepoCreateReport(t, repo, delivery.Report{
		VersionID: versionID, WaveID: &wave, Kind: delivery.ReportKindWave,
	})
	created := reportRepoCreateRevision(t, repo, report.ID, revision)
	fingerprint := reportRepoFingerprint("a")
	if err := repo.FinalizePublished(context.Background(), report.ID, revision, htmlRef, fingerprint); err != nil {
		t.Fatalf("FinalizePublished: %v", err)
	}
	published, err := repo.GetRevision(context.Background(), report.ID, revision)
	if err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	if created.ID != published.ID {
		t.Fatalf("created revision ID = %d, published = %d", created.ID, published.ID)
	}
	return &pdfRepoFixture{repo: repo, report: report, revision: published, htmlRef: htmlRef}
}

func (f *pdfRepoFixture) current(t *testing.T) delivery.ReportRevision {
	t.Helper()
	revision, err := f.repo.GetRevision(context.Background(), f.report.ID, f.revision.Revision)
	if err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	return revision
}

// assertPDFImmutableColumns pins that PDF state changes never touch the
// authoritative revision metadata (spec #36).
func assertPDFImmutableColumns(t *testing.T, before, after delivery.ReportRevision) {
	t.Helper()
	if !after.AsOf.Equal(before.AsOf) {
		t.Fatalf("as_of changed: %v -> %v", before.AsOf, after.AsOf)
	}
	if !equalInt64Ptr(before.HTMLRef, after.HTMLRef) {
		t.Fatalf("html_ref changed: %v -> %v", before.HTMLRef, after.HTMLRef)
	}
	if after.InputFingerprint != before.InputFingerprint {
		t.Fatalf("input_fingerprint changed: %q -> %q", before.InputFingerprint, after.InputFingerprint)
	}
	if after.TemplateVersion != before.TemplateVersion {
		t.Fatalf("template_version changed: %q -> %q", before.TemplateVersion, after.TemplateVersion)
	}
	if after.Status != delivery.ReportStatusPublished || after.RenderStatus != "done" {
		t.Fatalf("published/render state changed: %+v", after)
	}
}

func equalInt64Ptr(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// TestReportRepoPDFLifecycleFencesAndImmutability covers the P7 PDF state
// machine: pending -> running -> done/failed/disabled with every transition
// fenced, and as_of/html_ref/input_fingerprint/template_version untouched.
func TestReportRepoPDFLifecycleFencesAndImmutability(t *testing.T) {
	fixture := newPDFRepoFixture(t, 7, 12, 1, 9101)
	ctx := context.Background()
	before := fixture.current(t)
	if before.PDFStatus != delivery.PDFStatusPending || before.PDFAttempts != 0 || before.PDFRef != nil {
		t.Fatalf("new revision PDF state = %+v", before)
	}

	renderable, err := fixture.repo.ListRenderableRevisions(ctx, 100)
	if err != nil {
		t.Fatalf("ListRenderableRevisions: %v", err)
	}
	if len(renderable) != 1 {
		t.Fatalf("renderable = %+v, want one", renderable)
	}
	item := renderable[0]
	if item.RevisionID != before.ID || item.ReportID != fixture.report.ID ||
		item.VersionID != 7 || item.Revision != 1 ||
		item.PDFStatus != delivery.PDFStatusPending || item.PDFAttempts != 0 {
		t.Fatalf("renderable revision = %+v", item)
	}

	if err := fixture.repo.MarkPDFRunning(ctx, before.ID); err != nil {
		t.Fatalf("MarkPDFRunning: %v", err)
	}
	running := fixture.current(t)
	if running.PDFStatus != delivery.PDFStatusRunning || running.PDFAttempts != 1 {
		t.Fatalf("running state = %+v", running)
	}
	assertPDFImmutableColumns(t, before, running)
	// A retried attempt (pending/failed/running) increments attempts again.
	if err := fixture.repo.MarkPDFRunning(ctx, before.ID); err != nil {
		t.Fatalf("retry MarkPDFRunning: %v", err)
	}
	if retried := fixture.current(t); retried.PDFAttempts != 2 || retried.PDFStatus != delivery.PDFStatusRunning {
		t.Fatalf("retried state = %+v", retried)
	}
	renderable, err = fixture.repo.ListRenderableRevisions(ctx, 100)
	if err != nil || len(renderable) != 0 {
		t.Fatalf("renderable while running = %+v err=%v, want none", renderable, err)
	}

	ref := deliveryapp.ReportContentRef{RefID: 9201, SHA256: strings.Repeat("b", 64), Size: 1234}
	renderedAt := time.Date(2026, 11, 2, 3, 4, 5, 0, time.UTC)
	if err := fixture.repo.MarkPDFRendered(ctx, before.ID, ref, renderedAt); err != nil {
		t.Fatalf("MarkPDFRendered: %v", err)
	}
	done := fixture.current(t)
	if done.PDFStatus != delivery.PDFStatusDone || done.PDFRef == nil || *done.PDFRef != 9201 ||
		done.PDFRenderedAt == nil || !done.PDFRenderedAt.Equal(renderedAt) || done.PDFError != "" {
		t.Fatalf("done state = %+v", done)
	}
	if done.PDFAttempts != 2 {
		t.Fatalf("done pdf_attempts = %d, want 2", done.PDFAttempts)
	}
	assertPDFImmutableColumns(t, before, done)

	// A completed PDF can never be re-rendered or failed again.
	if err := fixture.repo.MarkPDFRendered(ctx, before.ID, ref, renderedAt); !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("duplicate MarkPDFRendered error = %v, want ErrStateConflict", err)
	}
	if err := fixture.repo.MarkPDFFailed(ctx, before.ID, "late failure", 2, renderedAt); !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("MarkPDFFailed after done error = %v, want ErrStateConflict", err)
	}
	afterConflicts := fixture.current(t)
	if afterConflicts.PDFStatus != delivery.PDFStatusDone || !equalInt64Ptr(afterConflicts.PDFRef, done.PDFRef) {
		t.Fatalf("conflicting writes changed the done revision: %+v", afterConflicts)
	}

	// Failed path: fenced metadata, bounded error, renderable again for retry.
	failedFixture := newPDFRepoFixture(t, 8, 13, 1, 9102)
	failedBefore := failedFixture.current(t)
	if err := failedFixture.repo.MarkPDFRunning(ctx, failedBefore.ID); err != nil {
		t.Fatalf("MarkPDFRunning failed path: %v", err)
	}
	longError := strings.Repeat("x", 2500)
	if err := failedFixture.repo.MarkPDFFailed(ctx, failedBefore.ID, longError, 2, renderedAt); err != nil {
		t.Fatalf("MarkPDFFailed: %v", err)
	}
	failedAfter := failedFixture.current(t)
	if failedAfter.PDFStatus != delivery.PDFStatusFailed || failedAfter.PDFRef != nil ||
		failedAfter.PDFAttempts != 2 || len([]rune(failedAfter.PDFError)) != 2000 {
		t.Fatalf("failed state = %+v (pdf_error chars %d)", failedAfter, len([]rune(failedAfter.PDFError)))
	}
	assertPDFImmutableColumns(t, failedBefore, failedAfter)
	if err := failedFixture.repo.MarkPDFRendered(ctx, failedBefore.ID, ref, renderedAt); !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("MarkPDFRendered after failed error = %v, want ErrStateConflict", err)
	}
	renderable, err = failedFixture.repo.ListRenderableRevisions(ctx, 100)
	if err != nil || len(renderable) != 1 || renderable[0].PDFStatus != delivery.PDFStatusFailed ||
		renderable[0].PDFAttempts != 2 {
		t.Fatalf("renderable after failure = %+v err=%v", renderable, err)
	}

	// Disabled path: only pending revisions are disabled and the operation is
	// idempotent.
	disabledFixture := newPDFRepoFixture(t, 9, 14, 1, 9103)
	disabledBefore := disabledFixture.current(t)
	if err := disabledFixture.repo.MarkPDFDisabled(ctx, disabledBefore.ID); err != nil {
		t.Fatalf("MarkPDFDisabled: %v", err)
	}
	disabledAfter := disabledFixture.current(t)
	if disabledAfter.PDFStatus != delivery.PDFStatusDisabled {
		t.Fatalf("disabled state = %+v", disabledAfter)
	}
	assertPDFImmutableColumns(t, disabledBefore, disabledAfter)
	if err := disabledFixture.repo.MarkPDFDisabled(ctx, disabledBefore.ID); err != nil {
		t.Fatalf("idempotent MarkPDFDisabled: %v", err)
	}
	if err := disabledFixture.repo.MarkPDFRunning(ctx, disabledBefore.ID); !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("MarkPDFRunning on disabled error = %v, want ErrStateConflict", err)
	}
	renderable, err = disabledFixture.repo.ListRenderableRevisions(ctx, 100)
	if err != nil || len(renderable) != 0 {
		t.Fatalf("renderable after disable = %+v err=%v", renderable, err)
	}

	if err := fixture.repo.MarkPDFRunning(ctx, 999999); !errors.Is(err, deliveryapp.ErrReportNotFound) {
		t.Fatalf("MarkPDFRunning missing revision error = %v, want ErrReportNotFound", err)
	}
}

// TestReportRepoListRenderableFiltersAndLimits covers the reconciler scan:
// only published+rendered revisions whose PDF is pending/failed are returned.
func TestReportRepoListRenderableFiltersAndLimits(t *testing.T) {
	repo, pool := newReportRepoForTest(t)
	ctx := context.Background()
	waveID := int64(20)
	report := reportRepoCreateReport(t, repo, delivery.Report{
		VersionID: 100, WaveID: &waveID, Kind: delivery.ReportKindWave,
	})

	// A pending revision that was never published is not renderable.
	draft := reportRepoCreateRevision(t, repo, report.ID, 1)
	if err := repo.MarkRenderMetadata(ctx, draft.ID, "done", ""); err != nil {
		t.Fatalf("MarkRenderMetadata: %v", err)
	}

	// Two published renderable revisions (pending + failed) and one done.
	for revision := int32(2); revision <= 4; revision++ {
		reportRepoCreateRevision(t, repo, report.ID, revision)
		if err := repo.FinalizePublished(ctx, report.ID, revision, int64(9100+revision), reportRepoFingerprint("a")); err != nil {
			t.Fatalf("FinalizePublished r%d: %v", revision, err)
		}
	}
	rows, err := repo.ListRenderableRevisions(ctx, 100)
	if err != nil {
		t.Fatalf("ListRenderableRevisions: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("renderable = %+v, want revisions 2,3,4", rows)
	}
	for index, wantRevision := range []int32{2, 3, 4} {
		if rows[index].Revision != wantRevision {
			t.Fatalf("renderable[%d] = %+v, want revision %d", index, rows[index], wantRevision)
		}
	}

	// Mark the released one running (not renderable) and fail another.
	for _, row := range rows {
		switch row.Revision {
		case 3:
			if err := repo.MarkPDFRunning(ctx, row.RevisionID); err != nil {
				t.Fatalf("MarkPDFRunning: %v", err)
			}
			if err := repo.MarkPDFFailed(ctx, row.RevisionID, "boom", 1, time.Now().UTC()); err != nil {
				t.Fatalf("MarkPDFFailed: %v", err)
			}
		case 4:
			if err := repo.MarkPDFRunning(ctx, row.RevisionID); err != nil {
				t.Fatalf("MarkPDFRunning: %v", err)
			}
			if err := repo.MarkPDFRendered(ctx, row.RevisionID, deliveryapp.ReportContentRef{RefID: 9999, SHA256: strings.Repeat("c", 64), Size: 1}, time.Now().UTC()); err != nil {
				t.Fatalf("MarkPDFRendered: %v", err)
			}
		}
	}
	// A legacy published revision with a non-done render_status is skipped.
	if _, err := pool.Exec(ctx, `
		UPDATE delivery_report_revision SET render_status = 'failed'
		WHERE report_id = $1 AND revision = 2
	`, report.ID); err != nil {
		t.Fatalf("mark legacy render failed: %v", err)
	}
	rows, err = repo.ListRenderableRevisions(ctx, 100)
	if err != nil || len(rows) != 1 || rows[0].Revision != 3 || rows[0].PDFStatus != delivery.PDFStatusFailed {
		t.Fatalf("renderable after transitions = %+v err=%v, want only failed revision 3", rows, err)
	}

	// Limit is applied.
	rows, err = repo.ListRenderableRevisions(ctx, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("default limit rows = %+v err=%v", rows, err)
	}
}

// TestReportRepoEnsureRenderJobIdempotent covers the frozen work item contract
// of report.render_pdf.
func TestReportRepoEnsureRenderJobIdempotent(t *testing.T) {
	fixture := newPDFRepoFixture(t, 7, 12, 1, 9101)
	ctx := context.Background()
	revision := fixture.current(t)
	dedupeKey := "report:1:rev:1"

	if err := fixture.repo.EnsureRenderJob(ctx, revision.ID, fixture.report.ID, 7, dedupeKey, 3); err != nil {
		t.Fatalf("EnsureRenderJob: %v", err)
	}
	if err := fixture.repo.EnsureRenderJob(ctx, revision.ID, fixture.report.ID, 7, dedupeKey, 3); err != nil {
		t.Fatalf("idempotent EnsureRenderJob: %v", err)
	}
	var (
		count       int
		team, wtype string
		status      string
		versionID   int64
		maxAttempts int
		payload     []byte
	)
	if err := fixture.repo.pool.QueryRow(ctx, `
		SELECT count(*) FROM work_item WHERE dedupe_key = $1
	`, dedupeKey).Scan(&count); err != nil {
		t.Fatalf("count work items: %v", err)
	}
	if count != 1 {
		t.Fatalf("work items = %d, want 1", count)
	}
	if err := fixture.repo.pool.QueryRow(ctx, `
		SELECT version_id, team, type, status, max_attempts, payload
		FROM work_item WHERE dedupe_key = $1
	`, dedupeKey).Scan(&versionID, &team, &wtype, &status, &maxAttempts, &payload); err != nil {
		t.Fatalf("read work item: %v", err)
	}
	if versionID != 7 || team != "delivery" || wtype != "report.render_pdf" ||
		status != "open" || maxAttempts != 3 {
		t.Fatalf("work item = version:%d team:%s type:%s status:%s max:%d", versionID, team, wtype, status, maxAttempts)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode payload %s: %v", payload, err)
	}
	if decoded["report_id"] != strconv.FormatInt(fixture.report.ID, 10) || decoded["report_revision_id"] != strconv.FormatInt(revision.ID, 10) {
		t.Fatalf("payload = %v", decoded)
	}
}

// TestReportRepoCreateRevisionTemplateVersions covers the P7-D-03 template
// version gate: "1" and "2" are built in, anything else is rejected.
func TestReportRepoCreateRevisionTemplateVersions(t *testing.T) {
	repo, _ := newReportRepoForTest(t)
	ctx := context.Background()
	waveID := int64(21)
	report := reportRepoCreateReport(t, repo, delivery.Report{
		VersionID: 101, WaveID: &waveID, Kind: delivery.ReportKindWave,
	})
	for index, version := range []string{"1", "2"} {
		created, err := repo.CreateRevision(ctx, delivery.ReportRevision{
			ReportID: report.ID, Revision: int32(index + 1),
			InputFingerprint: reportRepoFingerprint("a"), TemplateVersion: version,
		})
		if err != nil {
			t.Fatalf("CreateRevision(%q): %v", version, err)
		}
		if created.TemplateVersion != version {
			t.Fatalf("created template_version = %q, want %q", created.TemplateVersion, version)
		}
	}
	if _, err := repo.CreateRevision(ctx, delivery.ReportRevision{
		ReportID: report.ID, Revision: 3,
		InputFingerprint: reportRepoFingerprint("b"), TemplateVersion: "3",
	}); !errors.Is(err, delivery.ErrInvalidArgument) {
		t.Fatalf("unsupported template version error = %v, want ErrInvalidArgument", err)
	}
}
