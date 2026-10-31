//go:build integration

package deliverypg

import (
	"context"
	"errors"
	"strings"
	"testing"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// newReportRepoForTest opens an isolated migrated schema and wires the report
// repository against it.
func newReportRepoForTest(t *testing.T) (*ReportRepository, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.NewDB(t)
	return NewReportRepository(pool), pool
}

func reportRepoFingerprint(char string) string { return strings.Repeat(char, 64) }

func reportRepoInt64(value int64) *int64 { return &value }

// reportRepoCreateReport creates one report through the repository and fails
// the test on error or on a non-draft result.
func reportRepoCreateReport(t *testing.T, repo *ReportRepository, report delivery.Report) delivery.Report {
	t.Helper()
	created, err := repo.CreateReport(context.Background(), report)
	if err != nil {
		t.Fatalf("CreateReport: %v", err)
	}
	if created.ID <= 0 {
		t.Fatalf("created report ID = %d", created.ID)
	}
	return created
}

// reportRepoCreateRevision inserts one numbered revision with a valid
// fingerprint.
func reportRepoCreateRevision(t *testing.T, repo *ReportRepository, reportID int64, revision int32) delivery.ReportRevision {
	t.Helper()
	created, err := repo.CreateRevision(context.Background(), delivery.ReportRevision{
		ReportID:         reportID,
		Revision:         revision,
		InputFingerprint: reportRepoFingerprint("a"),
	})
	if err != nil {
		t.Fatalf("CreateRevision(%d, %d): %v", reportID, revision, err)
	}
	return created
}

// TestReportRepoCreateAndPartialUnique covers the wave/version partial unique
// keys, idempotent creation, id-scoped reads and descending list pagination.
func TestReportRepoCreateAndPartialUnique(t *testing.T) {
	repo, pool := newReportRepoForTest(t)
	ctx := context.Background()

	waveID := int64(700)
	wave, err := repo.CreateReport(ctx, delivery.Report{
		VersionID: 100, WaveID: &waveID, Kind: delivery.ReportKindWave,
	})
	if err != nil {
		t.Fatalf("CreateReport wave: %v", err)
	}
	if wave.ID <= 0 || wave.Status != delivery.ReportStatusDraft || wave.Kind != delivery.ReportKindWave ||
		wave.WaveID == nil || *wave.WaveID != waveID || wave.CurrentRevisionID != nil ||
		wave.InputFingerprint != "" || wave.CreatedAt.IsZero() || wave.UpdatedAt.IsZero() {
		t.Fatalf("created wave report = %+v", wave)
	}

	duplicateWave, err := repo.CreateReport(ctx, delivery.Report{
		VersionID: 100, WaveID: &waveID, Kind: delivery.ReportKindWave,
	})
	if err != nil {
		t.Fatalf("duplicate wave report: %v", err)
	}
	if duplicateWave.ID != wave.ID {
		t.Fatalf("duplicate wave report ID = %d, want %d", duplicateWave.ID, wave.ID)
	}
	var waveCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM delivery_report WHERE version_id = 100 AND kind = 'wave'`).Scan(&waveCount); err != nil {
		t.Fatalf("count wave reports: %v", err)
	}
	if waveCount != 1 {
		t.Fatalf("wave report rows = %d, want 1 (partial unique)", waveCount)
	}

	otherWaveID := int64(701)
	otherWave := reportRepoCreateReport(t, repo, delivery.Report{
		VersionID: 100, WaveID: &otherWaveID, Kind: delivery.ReportKindWave,
	})
	if otherWave.ID == wave.ID {
		t.Fatal("two different waves reused one report ID")
	}

	versionReport, err := repo.CreateReport(ctx, delivery.Report{
		VersionID: 100, Kind: delivery.ReportKindVersion,
	})
	if err != nil {
		t.Fatalf("CreateReport version: %v", err)
	}
	if versionReport.WaveID != nil || versionReport.Kind != delivery.ReportKindVersion {
		t.Fatalf("version report = %+v", versionReport)
	}
	duplicateVersion, err := repo.CreateReport(ctx, delivery.Report{
		VersionID: 100, Kind: delivery.ReportKindVersion,
	})
	if err != nil || duplicateVersion.ID != versionReport.ID {
		t.Fatalf("duplicate version report = %+v err=%v, want ID %d", duplicateVersion, err, versionReport.ID)
	}

	// Invalid shapes are rejected before SQL.
	for _, invalid := range []delivery.Report{
		{VersionID: 100, Kind: delivery.ReportKind("bogus")},
		{VersionID: 100, Kind: delivery.ReportKindWave},
		{VersionID: 100, Kind: delivery.ReportKindVersion, WaveID: reportRepoInt64(1)},
		{VersionID: 0, Kind: delivery.ReportKindVersion},
	} {
		if _, err := repo.CreateReport(ctx, invalid); !errors.Is(err, delivery.ErrInvalidArgument) {
			t.Fatalf("CreateReport(%+v) error = %v, want ErrInvalidArgument", invalid, err)
		}
	}

	// GetReport is version scoped while GetReportByID is not.
	got, err := repo.GetReport(ctx, 100, wave.ID)
	if err != nil || got.ID != wave.ID {
		t.Fatalf("GetReport = %+v err=%v", got, err)
	}
	if _, err := repo.GetReport(ctx, 200, wave.ID); !errors.Is(err, deliveryapp.ErrReportNotFound) {
		t.Fatalf("GetReport wrong version error = %v, want ErrReportNotFound", err)
	}
	byID, err := repo.GetReportByID(ctx, wave.ID)
	if err != nil || byID.VersionID != 100 || byID.ID != wave.ID {
		t.Fatalf("GetReportByID = %+v err=%v", byID, err)
	}
	if _, err := repo.GetReportByID(ctx, 999999); !errors.Is(err, deliveryapp.ErrReportNotFound) {
		t.Fatalf("GetReportByID missing error = %v, want ErrReportNotFound", err)
	}

	// ListReports: kind filter, descending ID, exclusive Before cursor.
	waveKind := delivery.ReportKindWave
	allWaves, err := repo.ListReports(ctx, 100, deliveryapp.ReportFilter{Kind: &waveKind, Limit: 10})
	if err != nil {
		t.Fatalf("ListReports waves: %v", err)
	}
	if len(allWaves) != 2 || allWaves[0].ID != otherWave.ID || allWaves[1].ID != wave.ID {
		t.Fatalf("ListReports waves = %+v, want descending [%d %d]", allWaves, otherWave.ID, wave.ID)
	}
	paged, err := repo.ListReports(ctx, 100, deliveryapp.ReportFilter{Kind: &waveKind, Before: otherWave.ID, Limit: 10})
	if err != nil || len(paged) != 1 || paged[0].ID != wave.ID {
		t.Fatalf("ListReports Before = %+v err=%v", paged, err)
	}
	versionKind := delivery.ReportKindVersion
	versions, err := repo.ListReports(ctx, 100, deliveryapp.ReportFilter{Kind: &versionKind})
	if err != nil || len(versions) != 1 || versions[0].ID != versionReport.ID {
		t.Fatalf("ListReports versions = %+v err=%v", versions, err)
	}
	limited, err := repo.ListReports(ctx, 100, deliveryapp.ReportFilter{Limit: 1})
	if err != nil || len(limited) != 1 || limited[0].ID != versionReport.ID {
		t.Fatalf("ListReports limit 1 = %+v err=%v", limited, err)
	}
	otherVersion, err := repo.ListReports(ctx, 200, deliveryapp.ReportFilter{})
	if err != nil || len(otherVersion) != 0 {
		t.Fatalf("ListReports other version = %+v err=%v", otherVersion, err)
	}
}

// TestReportRepoRevisionAndFinalize covers revision numbering, the unique
// (report_id, revision) key, the atomic publish finalization and the
// append-only content fields of published revisions.
func TestReportRepoRevisionAndFinalize(t *testing.T) {
	repo, _ := newReportRepoForTest(t)
	ctx := context.Background()
	waveID := int64(700)
	report := reportRepoCreateReport(t, repo, delivery.Report{
		VersionID: 100, WaveID: &waveID, Kind: delivery.ReportKindWave,
	})

	next, err := repo.NextRevision(ctx, report.ID)
	if err != nil || next != 1 {
		t.Fatalf("NextRevision empty = %d err=%v, want 1", next, err)
	}
	first := reportRepoCreateRevision(t, repo, report.ID, 1)
	if first.ID <= 0 || first.Status != delivery.ReportStatusDraft ||
		first.RenderStatus != "pending" || first.TemplateVersion != "1" ||
		first.HTMLRef != nil || first.PDFRef != nil || first.AsOf.IsZero() {
		t.Fatalf("first revision = %+v", first)
	}
	if next, err = repo.NextRevision(ctx, report.ID); err != nil || next != 2 {
		t.Fatalf("NextRevision after r1 = %d err=%v, want 2", next, err)
	}
	if _, err := repo.CreateRevision(ctx, delivery.ReportRevision{
		ReportID: report.ID, Revision: 1, InputFingerprint: reportRepoFingerprint("b"),
	}); !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("duplicate revision error = %v, want ErrStateConflict", err)
	}
	if _, err := repo.CreateRevision(ctx, delivery.ReportRevision{
		ReportID: report.ID, Revision: 2, InputFingerprint: "",
	}); !errors.Is(err, delivery.ErrInvalidArgument) {
		t.Fatalf("empty fingerprint revision error = %v, want ErrInvalidArgument", err)
	}
	if _, err := repo.CreateRevision(ctx, delivery.ReportRevision{
		ReportID: 999999, Revision: 1, InputFingerprint: reportRepoFingerprint("b"),
	}); !errors.Is(err, deliveryapp.ErrReportNotFound) {
		t.Fatalf("missing report revision error = %v, want ErrReportNotFound", err)
	}

	fingerprint := reportRepoFingerprint("c")
	if err := repo.FinalizePublished(ctx, report.ID, 1, 9001, ""); !errors.Is(err, delivery.ErrInvalidArgument) {
		t.Fatalf("FinalizePublished empty fingerprint error = %v, want ErrInvalidArgument", err)
	}
	if err := repo.FinalizePublished(ctx, report.ID, 1, 9001, fingerprint); err != nil {
		t.Fatalf("FinalizePublished r1: %v", err)
	}
	published, err := repo.GetReport(ctx, 100, report.ID)
	if err != nil {
		t.Fatalf("GetReport after finalize: %v", err)
	}
	if published.Status != delivery.ReportStatusPublished || published.CurrentRevisionID == nil ||
		*published.CurrentRevisionID != first.ID || published.InputFingerprint != fingerprint {
		t.Fatalf("published report = %+v", published)
	}
	publishedRevision, err := repo.GetRevision(ctx, report.ID, 1)
	if err != nil {
		t.Fatalf("GetRevision r1: %v", err)
	}
	if publishedRevision.Status != delivery.ReportStatusPublished || publishedRevision.RenderStatus != "done" ||
		publishedRevision.HTMLRef == nil || *publishedRevision.HTMLRef != 9001 ||
		publishedRevision.InputFingerprint != fingerprint {
		t.Fatalf("published revision = %+v", publishedRevision)
	}
	if err := repo.FinalizePublished(ctx, report.ID, 1, 9999, reportRepoFingerprint("d")); !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("second finalize error = %v, want ErrStateConflict", err)
	}
	if err := repo.MarkRenderMetadata(ctx, first.ID, "failed", "boom"); !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("render metadata on published revision error = %v, want ErrStateConflict", err)
	}
	if err := repo.FinalizePublished(ctx, report.ID, 9, 1, fingerprint); !errors.Is(err, deliveryapp.ErrReportNotFound) {
		t.Fatalf("finalize missing revision error = %v, want ErrReportNotFound", err)
	}
	if err := repo.FinalizePublished(ctx, 999999, 1, 1, fingerprint); !errors.Is(err, deliveryapp.ErrReportNotFound) {
		t.Fatalf("finalize missing report error = %v, want ErrReportNotFound", err)
	}

	// A second published revision leaves the first one untouched.
	second := reportRepoCreateRevision(t, repo, report.ID, 2)
	secondFingerprint := reportRepoFingerprint("e")
	if err := repo.FinalizePublished(ctx, report.ID, 2, 9002, secondFingerprint); err != nil {
		t.Fatalf("FinalizePublished r2: %v", err)
	}
	oldRevision, err := repo.GetRevision(ctx, report.ID, 1)
	if err != nil {
		t.Fatalf("GetRevision old: %v", err)
	}
	if oldRevision.Status != delivery.ReportStatusPublished || oldRevision.HTMLRef == nil ||
		*oldRevision.HTMLRef != 9001 || oldRevision.InputFingerprint != fingerprint {
		t.Fatalf("old revision changed after r2 publish: %+v", oldRevision)
	}
	current, err := repo.GetReport(ctx, 100, report.ID)
	if err != nil {
		t.Fatalf("GetReport after r2: %v", err)
	}
	if current.CurrentRevisionID == nil || *current.CurrentRevisionID != second.ID || current.InputFingerprint != secondFingerprint {
		t.Fatalf("current revision after r2 = %+v", current)
	}
	revisions, err := repo.ListRevisions(ctx, report.ID)
	if err != nil || len(revisions) != 2 || revisions[0].Revision != 2 || revisions[1].Revision != 1 {
		t.Fatalf("ListRevisions = %+v err=%v", revisions, err)
	}

	// Draft revisions accept render metadata; missing revisions are not found.
	third := reportRepoCreateRevision(t, repo, report.ID, 3)
	if err := repo.MarkRenderMetadata(ctx, third.ID, "running", ""); err != nil {
		t.Fatalf("MarkRenderMetadata running: %v", err)
	}
	if err := repo.MarkRenderMetadata(ctx, third.ID, "failed", "render failed"); err != nil {
		t.Fatalf("MarkRenderMetadata failed: %v", err)
	}
	failedRevision, err := repo.GetRevision(ctx, report.ID, 3)
	if err != nil || failedRevision.RenderStatus != "failed" {
		t.Fatalf("failed revision = %+v err=%v", failedRevision, err)
	}
	if err := repo.MarkRenderMetadata(ctx, 999999, "failed", "x"); !errors.Is(err, deliveryapp.ErrReportNotFound) {
		t.Fatalf("render metadata missing revision error = %v, want ErrReportNotFound", err)
	}

	// Stale is only applied to a published report and only once.
	stale, err := repo.MarkStale(ctx, report.ID)
	if err != nil || !stale {
		t.Fatalf("MarkStale = %v err=%v, want true", stale, err)
	}
	stale, err = repo.MarkStale(ctx, report.ID)
	if err != nil || stale {
		t.Fatalf("second MarkStale = %v err=%v, want false", stale, err)
	}
	staled, err := repo.GetReport(ctx, 100, report.ID)
	if err != nil || staled.Status != delivery.ReportStatusStale {
		t.Fatalf("stale report = %+v err=%v", staled, err)
	}
	if _, err := repo.MarkStale(ctx, 999999); !errors.Is(err, deliveryapp.ErrReportNotFound) {
		t.Fatalf("MarkStale missing error = %v, want ErrReportNotFound", err)
	}
	draftReport := reportRepoCreateReport(t, repo, delivery.Report{
		VersionID: 200, Kind: delivery.ReportKindVersion,
	})
	if stale, err := repo.MarkStale(ctx, draftReport.ID); err != nil || stale {
		t.Fatalf("MarkStale draft = %v err=%v, want false", stale, err)
	}
}

// TestReportRepoFinalizeMonotonic covers the no-backwards-move guard of publish
// finalization: a revision lower than the report's current revision or lower
// than the highest published revision is a state conflict that leaves both the
// report header and the rejected revision untouched.
func TestReportRepoFinalizeMonotonic(t *testing.T) {
	repo, pool := newReportRepoForTest(t)
	ctx := context.Background()

	// Publishing r1 then r2 keeps r2 current; finalizing the older revision
	// again is a conflict.
	waveID := int64(700)
	report := reportRepoCreateReport(t, repo, delivery.Report{
		VersionID: 100, WaveID: &waveID, Kind: delivery.ReportKindWave,
	})
	reportRepoCreateRevision(t, repo, report.ID, 1)
	second := reportRepoCreateRevision(t, repo, report.ID, 2)
	firstFingerprint := reportRepoFingerprint("c")
	secondFingerprint := reportRepoFingerprint("d")
	if err := repo.FinalizePublished(ctx, report.ID, 1, 9001, firstFingerprint); err != nil {
		t.Fatalf("FinalizePublished r1: %v", err)
	}
	if err := repo.FinalizePublished(ctx, report.ID, 2, 9002, secondFingerprint); err != nil {
		t.Fatalf("FinalizePublished r2: %v", err)
	}
	if err := repo.FinalizePublished(ctx, report.ID, 1, 9003, reportRepoFingerprint("e")); !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("finalize published r1 after r2 error = %v, want ErrStateConflict", err)
	}
	current, err := repo.GetReport(ctx, 100, report.ID)
	if err != nil {
		t.Fatalf("GetReport after rejected finalize: %v", err)
	}
	if current.CurrentRevisionID == nil || *current.CurrentRevisionID != second.ID ||
		current.InputFingerprint != secondFingerprint || current.Status != delivery.ReportStatusPublished {
		t.Fatalf("rejected finalize moved current backwards: %+v", current)
	}
	firstStored, err := repo.GetRevision(ctx, report.ID, 1)
	if err != nil || firstStored.Status != delivery.ReportStatusPublished ||
		firstStored.HTMLRef == nil || *firstStored.HTMLRef != 9001 ||
		firstStored.InputFingerprint != firstFingerprint {
		t.Fatalf("rejected finalize changed r1: %+v err=%v", firstStored, err)
	}

	// A draft revision below the current one must not move the pointer
	// backwards; this is the finalize race the row lock has to serialize.
	otherWaveID := int64(701)
	draftReport := reportRepoCreateReport(t, repo, delivery.Report{
		VersionID: 100, WaveID: &otherWaveID, Kind: delivery.ReportKindWave,
	})
	reportRepoCreateRevision(t, repo, draftReport.ID, 1)
	higher := reportRepoCreateRevision(t, repo, draftReport.ID, 2)
	higherFingerprint := reportRepoFingerprint("f")
	if err := repo.FinalizePublished(ctx, draftReport.ID, 2, 9102, higherFingerprint); err != nil {
		t.Fatalf("FinalizePublished higher draft: %v", err)
	}
	currentBefore, err := repo.GetReport(ctx, 100, draftReport.ID)
	if err != nil {
		t.Fatalf("GetReport before backwards attempt: %v", err)
	}
	lowerBefore, err := repo.GetRevision(ctx, draftReport.ID, 1)
	if err != nil {
		t.Fatalf("GetRevision before backwards attempt: %v", err)
	}
	if err := repo.FinalizePublished(ctx, draftReport.ID, 1, 9101, reportRepoFingerprint("1")); !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("finalize lower draft revision error = %v, want ErrStateConflict", err)
	}
	currentAfter, err := repo.GetReport(ctx, 100, draftReport.ID)
	if err != nil {
		t.Fatalf("GetReport after backwards attempt: %v", err)
	}
	if currentAfter.CurrentRevisionID == nil || *currentAfter.CurrentRevisionID != higher.ID ||
		currentAfter.InputFingerprint != higherFingerprint || currentAfter.Status != delivery.ReportStatusPublished ||
		!currentAfter.UpdatedAt.Equal(currentBefore.UpdatedAt) {
		t.Fatalf("rejected lower draft changed report: before=%+v after=%+v", currentBefore, currentAfter)
	}
	lowerAfter, err := repo.GetRevision(ctx, draftReport.ID, 1)
	if err != nil {
		t.Fatalf("GetRevision after backwards attempt: %v", err)
	}
	if lowerAfter.Status != delivery.ReportStatusDraft || lowerAfter.RenderStatus != "pending" ||
		lowerAfter.HTMLRef != nil || lowerAfter.InputFingerprint != lowerBefore.InputFingerprint ||
		!lowerAfter.UpdatedAt.Equal(lowerBefore.UpdatedAt) {
		t.Fatalf("rejected lower draft changed revision: before=%+v after=%+v", lowerBefore, lowerAfter)
	}
	higherAfter, err := repo.GetRevision(ctx, draftReport.ID, 2)
	if err != nil || higherAfter.Status != delivery.ReportStatusPublished ||
		higherAfter.HTMLRef == nil || *higherAfter.HTMLRef != 9102 ||
		higherAfter.InputFingerprint != higherFingerprint {
		t.Fatalf("current revision changed by rejected lower draft: %+v err=%v", higherAfter, err)
	}

	// The highest published revision bounds finalization even when the current
	// pointer is missing (defense in depth against a partial header write).
	lastWaveID := int64(702)
	lastReport := reportRepoCreateReport(t, repo, delivery.Report{
		VersionID: 100, WaveID: &lastWaveID, Kind: delivery.ReportKindWave,
	})
	reportRepoCreateRevision(t, repo, lastReport.ID, 1)
	reportRepoCreateRevision(t, repo, lastReport.ID, 2)
	if err := repo.FinalizePublished(ctx, lastReport.ID, 2, 9202, reportRepoFingerprint("2")); err != nil {
		t.Fatalf("FinalizePublished max published revision: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE delivery_report SET current_revision_id = NULL WHERE id = $1`, lastReport.ID); err != nil {
		t.Fatalf("clear current revision: %v", err)
	}
	if err := repo.FinalizePublished(ctx, lastReport.ID, 1, 9201, reportRepoFingerprint("3")); !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("finalize below max published error = %v, want ErrStateConflict", err)
	}
	lastCurrent, err := repo.GetReport(ctx, 100, lastReport.ID)
	if err != nil || lastCurrent.CurrentRevisionID != nil {
		t.Fatalf("rejected finalize set a current revision: %+v err=%v", lastCurrent, err)
	}
	lastLower, err := repo.GetRevision(ctx, lastReport.ID, 1)
	if err != nil || lastLower.Status != delivery.ReportStatusDraft || lastLower.HTMLRef != nil {
		t.Fatalf("rejected finalize below max published changed r1: %+v err=%v", lastLower, err)
	}
}

// TestReportRepoRefsAndWaveReportID covers append-only refs, their idempotent
// insert and the run_wave.report_id backfill.
func TestReportRepoRefsAndWaveReportID(t *testing.T) {
	repo, pool := newReportRepoForTest(t)
	ctx := context.Background()
	waveID := int64(700)
	report := reportRepoCreateReport(t, repo, delivery.Report{
		VersionID: 100, WaveID: &waveID, Kind: delivery.ReportKindWave,
	})
	revision := reportRepoCreateRevision(t, repo, report.ID, 1)

	refs := []delivery.ReportRef{
		{Kind: "wave", RefID: waveID},
		{Kind: "case_run", RefID: 3001},
		{Kind: "case_run", RefID: 3002},
		{Kind: "issue", RefID: 4001},
		{Kind: "requirement", RefID: 5001},
		{Kind: "version_sut", RefID: 6001},
	}
	if err := repo.InsertRefs(ctx, revision.ID, refs); err != nil {
		t.Fatalf("InsertRefs: %v", err)
	}
	stored, err := repo.ListRefs(ctx, report.ID)
	if err != nil {
		t.Fatalf("ListRefs: %v", err)
	}
	if len(stored) != len(refs) {
		t.Fatalf("refs = %d, want %d", len(stored), len(refs))
	}
	for index, ref := range stored {
		if ref.ID <= 0 || ref.ReportID != report.ID || ref.ReportRevisionID != revision.ID ||
			ref.Kind != refs[index].Kind || ref.RefID != refs[index].RefID || ref.CreatedAt.IsZero() {
			t.Fatalf("ref[%d] = %+v", index, ref)
		}
	}
	// Re-inserting the same logical refs is idempotent; a new ref appends.
	duplicate := append(append([]delivery.ReportRef(nil), refs...), delivery.ReportRef{Kind: "issue", RefID: 4002})
	if err := repo.InsertRefs(ctx, revision.ID, duplicate); err != nil {
		t.Fatalf("InsertRefs duplicate: %v", err)
	}
	stored, err = repo.ListRefs(ctx, report.ID)
	if err != nil || len(stored) != len(refs)+1 {
		t.Fatalf("refs after duplicate = %d err=%v, want %d", len(stored), err, len(refs)+1)
	}
	if err := repo.InsertRefs(ctx, revision.ID, []delivery.ReportRef{{Kind: "bogus", RefID: 1}}); err == nil {
		t.Fatal("InsertRefs with an invalid kind must fail")
	}
	if err := repo.InsertRefs(ctx, 999999, refs); !errors.Is(err, deliveryapp.ErrReportNotFound) {
		t.Fatalf("InsertRefs missing revision error = %v, want ErrReportNotFound", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO run_wave (id, version_id, status, trigger)
		VALUES (700, 100, 'done', 'manual')
	`); err != nil {
		t.Fatalf("insert run wave: %v", err)
	}
	if err := repo.SetWaveReportID(ctx, 100, waveID, report.ID); err != nil {
		t.Fatalf("SetWaveReportID: %v", err)
	}
	var reportID *int64
	if err := pool.QueryRow(ctx, `SELECT report_id FROM run_wave WHERE id = $1`, waveID).Scan(&reportID); err != nil {
		t.Fatalf("read run wave: %v", err)
	}
	if reportID == nil || *reportID != report.ID {
		t.Fatalf("run_wave.report_id = %v, want %d", reportID, report.ID)
	}
	if err := repo.SetWaveReportID(ctx, 100, waveID, report.ID); err != nil {
		t.Fatalf("SetWaveReportID idempotent: %v", err)
	}
	if err := repo.SetWaveReportID(ctx, 100, waveID, report.ID+1); !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("SetWaveReportID different report error = %v, want ErrStateConflict", err)
	}
	if err := repo.SetWaveReportID(ctx, 100, 999999, report.ID); !errors.Is(err, deliveryapp.ErrReportNotFound) {
		t.Fatalf("SetWaveReportID missing wave error = %v, want ErrReportNotFound", err)
	}

	// Deleting the report cascades to revisions and refs.
	if _, err := pool.Exec(ctx, `DELETE FROM delivery_report WHERE id = $1`, report.ID); err != nil {
		t.Fatalf("delete report: %v", err)
	}
	var revisionCount, refCount int
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM delivery_report_revision WHERE report_id = $1),
		       (SELECT count(*) FROM delivery_report_ref WHERE report_id = $1)
	`, report.ID).Scan(&revisionCount, &refCount); err != nil {
		t.Fatalf("count cascade rows: %v", err)
	}
	if revisionCount != 0 || refCount != 0 {
		t.Fatalf("cascade left revisions=%d refs=%d", revisionCount, refCount)
	}
	if _, err := repo.GetReportByID(ctx, report.ID); !errors.Is(err, deliveryapp.ErrReportNotFound) {
		t.Fatalf("deleted report lookup error = %v, want ErrReportNotFound", err)
	}
}
