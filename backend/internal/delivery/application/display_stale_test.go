package deliveryapp

// v0.2.4-G T11 fix round 1 (IMPORTANT #1, R-G10a / art.7.6): display-only judge
// state must heal even when the judge event is LOST on a static version. The
// canonical fingerprint does not include review_state/verdict (T9 display-only),
// so MarkStaleIfChanged never fires; the complete sweep compares the independent
// DISPLAY digest and refreshes on divergence. A failed refresh is re-queued, not
// silently dropped.

import (
	"context"
	"errors"
	"testing"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// publishCurrentVersionReport publishes a version report through the real
// Publish path so BOTH the canonical fingerprint and the display digest are
// recorded on the header (as production does).
func publishCurrentVersionReport(t *testing.T, f *reportFixture, versionID int64) delivery.Report {
	t.Helper()
	snapshot := reportTestVersionSnapshot(versionID)
	snapshot.Judgments = []delivery.JudgmentSnapshot{{CaseID: 1, CaseVersionID: 4001, ReviewState: "pending"}}
	f.data.version[versionID] = snapshot
	report := f.repo.seedReport(delivery.Report{VersionID: versionID, Kind: delivery.ReportKindVersion})
	f.repo.seedRevision(delivery.ReportRevision{ReportID: report.ID, Revision: 1})
	if _, err := f.svc.Publish(context.Background(), report.ID, 1, ""); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	stored, err := f.repo.GetReportByID(context.Background(), report.ID)
	if err != nil {
		t.Fatalf("GetReportByID: %v", err)
	}
	if stored.DisplayFingerprint == "" {
		t.Fatal("publish did not record a display digest")
	}
	return stored
}

// TestSweepHealsLostJudgeEventOnStaticVersion: the judge review_state changes but
// the event is lost and the version is static (no canonical input change). The
// report stays "published" (canonical unchanged) yet the sweep's display-digest
// comparison detects the divergence and refreshes the display.
func TestSweepHealsLostJudgeEventOnStaticVersion(t *testing.T) {
	fixture := newReportFixture(t)
	versionID := int64(7)
	report := publishCurrentVersionReport(t, fixture, versionID)
	beforeDisplay := report.DisplayFingerprint

	// Only the display-only judge projection changes (canonical fingerprint same).
	snapshot := fixture.data.version[versionID]
	snapshot.Judgments[0].ReviewState = "pass"
	snapshot.Judgments[0].Verdict = "pass"
	fixture.data.version[versionID] = snapshot
	if delivery.Fingerprint(snapshot) != report.InputFingerprint {
		t.Fatal("test setup: judge change must NOT alter the canonical fingerprint")
	}

	// No event (lost). Only the complete sweep with the display healer runs.
	sweeper := NewReportStaleSweeper(newFakePublishedVersionLister(fixture.repo), fixture.svc, nil).WithDisplayHealer(fixture.svc)
	stats, err := sweeper.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if stats.Staled != 0 || stats.DisplayRefreshed != 1 {
		t.Fatalf("sweep stats = %+v, want staled=0 displayRefreshed=1", stats)
	}
	after, _ := fixture.repo.GetReportByID(context.Background(), report.ID)
	if after.Status != delivery.ReportStatusPublished {
		t.Fatalf("report status = %q, want still published (canonical unchanged)", after.Status)
	}
	if after.DisplayFingerprint == beforeDisplay {
		t.Fatal("display digest not refreshed after lost judge event")
	}
	revisions, _ := fixture.repo.ListRevisions(context.Background(), report.ID)
	if len(revisions) != 2 || revisions[0].Status != delivery.ReportStatusPublished {
		t.Fatalf("revisions = %+v, want a published display-refresh revision", revisions)
	}
	// Idempotent: a second sweep refreshes nothing (display now current).
	stats2, err := sweeper.Sweep(context.Background())
	if err != nil || stats2.DisplayRefreshed != 0 {
		t.Fatalf("second sweep = %+v err=%v, want displayRefreshed=0", stats2, err)
	}
}

// TestSweepHealsLostJudgeEventOnStaleReport pins final fix wave #5 (SC-G8): a
// completed version's report went STALE (a canonical input changed earlier) and
// then a judge event is LOST on the static stale report. The sweep scan source
// must still reach the stale report (published OR stale) so the display healer
// converges — with a published-only lister the stale report drops out of the scan
// and its judge display stays outdated forever. The canonical MarkStaleIfChanged
// no-ops on the already-stale report (no duplicate stale event).
func TestSweepHealsLostJudgeEventOnStaleReport(t *testing.T) {
	fixture := newReportFixture(t)
	versionID := int64(7)
	report := publishCurrentVersionReport(t, fixture, versionID)

	// A canonical input changes and the report is marked stale (the earlier
	// conclusion-input change the reviewer's scenario starts from).
	snapshot := fixture.data.version[versionID]
	snapshot.Issues[0].Severity = "minor"
	fixture.data.version[versionID] = snapshot
	if _, err := fixture.svc.MarkStaleIfChanged(context.Background(), versionID); err != nil {
		t.Fatalf("MarkStaleIfChanged: %v", err)
	}
	stale, _ := fixture.repo.GetReportByID(context.Background(), report.ID)
	if stale.Status != delivery.ReportStatusStale {
		t.Fatalf("report status = %q, want stale (test setup)", stale.Status)
	}
	beforeDisplay := stale.DisplayFingerprint

	// Now ONLY the display-only judge projection changes; the judge event is lost.
	snapshot = fixture.data.version[versionID]
	snapshot.Judgments[0].ReviewState = "pass"
	snapshot.Judgments[0].Verdict = "pass"
	fixture.data.version[versionID] = snapshot

	// The complete sweep with the display healer must reach the STALE report.
	sweeper := NewReportStaleSweeper(newFakePublishedVersionLister(fixture.repo), fixture.svc, nil).WithDisplayHealer(fixture.svc)
	stats, err := sweeper.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if stats.Scanned != 1 || stats.DisplayRefreshed != 1 {
		t.Fatalf("sweep stats = %+v, want scanned=1 displayRefreshed=1 (stale report reached and healed)", stats)
	}
	after, _ := fixture.repo.GetReportByID(context.Background(), report.ID)
	if after.DisplayFingerprint == beforeDisplay {
		t.Fatal("stale report display digest not refreshed after lost judge event (published-only scan dropped it?)")
	}
	revisions, _ := fixture.repo.ListRevisions(context.Background(), report.ID)
	if len(revisions) != 2 || revisions[0].Status != delivery.ReportStatusPublished {
		t.Fatalf("revisions = %+v, want a published display-refresh revision", revisions)
	}
}

// TestJudgeRefreshFailureRequeuedThenRetries: a transient refresh failure must
// re-queue the version (not silently drop it); a later drain retries and succeeds.
func TestJudgeRefreshFailureRequeuedThenRetries(t *testing.T) {
	fixture := newReportFixture(t)
	versionID := int64(7)
	report := publishCurrentVersionReport(t, fixture, versionID)

	watcher := newCausalWatcher(fixture)
	// Transient failure: the snapshot read fails during the refresh.
	fixture.data.versionErr = errors.New("transient db error")
	fireStaleEvent(t, watcher, "design.judgment_submitted", versionID)
	if err := watcher.Drain(context.Background()); err == nil {
		t.Fatal("Drain error = nil, want the transient refresh failure surfaced")
	}
	// The version was re-queued for retry (not dropped).
	watcher.mu.Lock()
	_, requeued := watcher.judgePending[versionID]
	watcher.mu.Unlock()
	if !requeued {
		t.Fatal("failed refresh was not re-queued to judgePending")
	}

	// Transient error clears; the next drain retries and succeeds.
	fixture.data.versionErr = nil
	if err := watcher.Drain(context.Background()); err != nil {
		t.Fatalf("retry Drain: %v", err)
	}
	revisions, _ := fixture.repo.ListRevisions(context.Background(), report.ID)
	if len(revisions) != 2 || revisions[0].Status != delivery.ReportStatusPublished {
		t.Fatalf("revisions after retry = %+v, want a published refresh", revisions)
	}
	watcher.mu.Lock()
	_, still := watcher.judgePending[versionID]
	watcher.mu.Unlock()
	if still {
		t.Fatal("version still pending after a successful retry")
	}
}
