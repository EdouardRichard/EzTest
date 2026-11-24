package deliveryapp

// v0.2.4-G T11 (SC-G8, R-G10): the periodic COMPLETE sweep is the stale fallback.
// It scans EVERY version that has a published report (not just running versions,
// replacing the old narrow "running + first 2000 case_runs" polling), recomputes
// the conclusion-input fingerprint and marks changed reports stale. It heals
// lost events (best-effort publish) and the no-event reclassification path
// (F terminal.go is untouched). The sweep is bounded per page and idempotent.

import (
	"context"
	"errors"
	"sort"
	"testing"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// fakePublishedVersionLister mirrors the production SQL lister over the fake
// report repo: distinct version IDs with at least one report whose status is
// published OR stale (a stale report still has a published current revision the
// display healer must reach — final fix wave #5), ascending, paginated by
// afterVersionID. It deliberately does NOT filter by version status (the sweep
// covers completed versions too).
type fakePublishedVersionLister struct {
	repo *fakeReportRepo
	err  error
}

func newFakePublishedVersionLister(repo *fakeReportRepo) *fakePublishedVersionLister {
	return &fakePublishedVersionLister{repo: repo}
}

func (l *fakePublishedVersionLister) ListVersionsWithPublishedReports(_ context.Context, afterVersionID int64, limit int) ([]int64, error) {
	if l.err != nil {
		return nil, l.err
	}
	l.repo.mu.Lock()
	defer l.repo.mu.Unlock()
	seen := make(map[int64]struct{})
	for _, report := range l.repo.reports {
		if (report.Status == delivery.ReportStatusPublished || report.Status == delivery.ReportStatusStale) && report.VersionID > afterVersionID {
			seen[report.VersionID] = struct{}{}
		}
	}
	ids := make([]int64, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}

// TestSweepHealsLostEvent: a conclusion input changed but the change event was
// lost (best-effort publish). No causal trigger fires; the complete sweep
// recomputes the fingerprint and marks the report stale.
func TestSweepHealsLostEvent(t *testing.T) {
	fixture := newReportFixture(t)
	versionID := int64(7)
	report := seedCurrentVersionReport(t, fixture, versionID)

	snapshot := fixture.data.version[versionID]
	snapshot.Issues[0].Status = delivery.IssueStatusOpen
	fixture.data.version[versionID] = snapshot

	// No event is fired (lost). Only the sweep runs.
	sweeper := NewReportStaleSweeper(newFakePublishedVersionLister(fixture.repo), fixture.svc, nil)
	stats, err := sweeper.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if stats.Scanned != 1 || stats.Staled != 1 {
		t.Fatalf("sweep stats = %+v, want scanned=1 staled=1", stats)
	}
	after, _ := fixture.repo.GetReportByID(context.Background(), report.ID)
	if after.Status != delivery.ReportStatusStale {
		t.Fatalf("report after lost-event sweep = %+v, want stale", after)
	}
}

// TestReclassificationStaleViaSweep: a terminal->terminal reclassification
// changes failure_class/terminal_cause but emits NO event (F terminal.go is
// read-only for G). The sweep heals it.
func TestReclassificationStaleViaSweep(t *testing.T) {
	fixture := newReportFixture(t)
	versionID := int64(7)
	report := seedCurrentVersionReport(t, fixture, versionID)

	snapshot := fixture.data.version[versionID]
	snapshot.CaseRuns[0].Status = "inconclusive"
	snapshot.CaseRuns[0].FailureClass = "environment"
	snapshot.CaseRuns[0].TerminalCause = "lease_expired"
	fixture.data.version[versionID] = snapshot

	sweeper := NewReportStaleSweeper(newFakePublishedVersionLister(fixture.repo), fixture.svc, nil)
	stats, err := sweeper.Sweep(context.Background())
	if err != nil || stats.Staled != 1 {
		t.Fatalf("Sweep = %+v err=%v, want staled=1 (reclassification healed)", stats, err)
	}
	after, _ := fixture.repo.GetReportByID(context.Background(), report.ID)
	if after.Status != delivery.ReportStatusStale {
		t.Fatalf("report after reclassification sweep = %+v, want stale", after)
	}
}

// TestSweepScansAllPublishedVersions: the sweep covers every version with a
// published report, including completed ones (the old narrow polling only
// looked at running versions).
func TestSweepScansAllPublishedVersions(t *testing.T) {
	fixture := newReportFixture(t)
	for _, versionID := range []int64{7, 8, 9} {
		report := seedCurrentVersionReport(t, fixture, versionID)
		snapshot := fixture.data.version[versionID]
		snapshot.Issues[0].Severity = "minor"
		fixture.data.version[versionID] = snapshot
		_ = report
	}
	sweeper := NewReportStaleSweeper(newFakePublishedVersionLister(fixture.repo), fixture.svc, nil)
	stats, err := sweeper.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if stats.Scanned != 3 || stats.Staled != 3 {
		t.Fatalf("sweep stats = %+v, want scanned=3 staled=3 (all published versions)", stats)
	}
}

// TestSweepBoundedAndIdempotent: pagination covers more versions than one page,
// and a second sweep marks nothing new (MarkStale only on fingerprint change).
func TestSweepBoundedAndIdempotent(t *testing.T) {
	fixture := newReportFixture(t)
	for versionID := int64(1); versionID <= 5; versionID++ {
		seedCurrentVersionReport(t, fixture, versionID)
		snapshot := fixture.data.version[versionID]
		snapshot.Issues[0].Severity = "minor"
		fixture.data.version[versionID] = snapshot
	}
	sweeper := NewReportStaleSweeper(newFakePublishedVersionLister(fixture.repo), fixture.svc, nil)
	sweeper.pageSize = 2 // force multiple pages
	stats, err := sweeper.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if stats.Scanned != 5 || stats.Staled != 5 {
		t.Fatalf("first sweep = %+v, want scanned=5 staled=5 across pages", stats)
	}
	// Add a still-PUBLISHED, UNCHANGED version before the second pass so the
	// idempotency claim is genuinely pinned: the sweep must scan it and NOT re-stale
	// it. The first five are stale now but STAY in the scan source (published OR
	// stale — final fix wave #5); MarkStaleIfChanged no-ops on an already-stale
	// report, so the second pass scans all six and stales none.
	unchanged := seedCurrentVersionReport(t, fixture, 6)
	stats2, err := sweeper.Sweep(context.Background())
	if err != nil {
		t.Fatalf("second Sweep: %v", err)
	}
	if stats2.Scanned != 6 || stats2.Staled != 0 {
		t.Fatalf("second sweep = %+v, want scanned=6 (5 stale + 1 published) staled=0 (idempotent)", stats2)
	}
	after, _ := fixture.repo.GetReportByID(context.Background(), unchanged.ID)
	if after.Status != delivery.ReportStatusPublished {
		t.Fatalf("unchanged published report was staled by the second sweep: %+v", after)
	}
}

// TestSweepSharesVersionSnapshotAcrossLegs pins the H-G4 #27 perf hardening: one
// sweep pass over an unchanged published version reads the version snapshot ONCE
// and shares it between the canonical stale leg and the display-heal leg, instead
// of loading the full snapshot twice. The display output and stats are unchanged;
// only the duplicated read drops. (A diverged display still refreshes through
// syncRefreshVersionReport, which legitimately re-reads — that path is pinned by
// TestSweepHealsLostJudgeEventOnStaticVersion.)
func TestSweepSharesVersionSnapshotAcrossLegs(t *testing.T) {
	fixture := newReportFixture(t)
	publishCurrentVersionReport(t, fixture, 7)
	fixture.data.mu.Lock()
	fixture.data.versionCall = 0
	fixture.data.mu.Unlock()

	sweeper := NewReportStaleSweeper(newFakePublishedVersionLister(fixture.repo), fixture.svc, nil).WithDisplayHealer(fixture.svc)
	stats, err := sweeper.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if stats.Scanned != 1 || stats.Staled != 0 || stats.DisplayRefreshed != 0 {
		t.Fatalf("sweep stats = %+v, want scanned=1 staled=0 displayRefreshed=0 (unchanged version)", stats)
	}
	fixture.data.mu.Lock()
	calls := fixture.data.versionCall
	fixture.data.mu.Unlock()
	if calls != 1 {
		t.Fatalf("version snapshot read %d times in one sweep pass, want 1 (shared between the stale and display legs)", calls)
	}
}

// failVersionMarker wraps a ReportStaleMarker and fails one specific version,
// delegating the rest. It proves the sweep isolates a per-version failure.
type failVersionMarker struct {
	inner       ReportStaleMarker
	failVersion int64
}

func (m failVersionMarker) MarkStaleIfChanged(ctx context.Context, versionID int64) (bool, error) {
	if versionID == m.failVersion {
		return false, errors.New("snapshot read failed")
	}
	return m.inner.MarkStaleIfChanged(ctx, versionID)
}

// TestSweepErrorIsolation: a read failure for one version must not stop the
// sweep from processing the remaining versions; the error is aggregated.
func TestSweepErrorIsolation(t *testing.T) {
	fixture := newReportFixture(t)
	good := seedCurrentVersionReport(t, fixture, 7)
	snapshot := fixture.data.version[7]
	snapshot.Issues[0].Severity = "minor"
	fixture.data.version[7] = snapshot
	// A second published version whose stale check fails.
	seedCurrentVersionReport(t, fixture, 8)

	marker := failVersionMarker{inner: fixture.svc, failVersion: 8}
	sweeper := NewReportStaleSweeper(newFakePublishedVersionLister(fixture.repo), marker, nil)
	stats, err := sweeper.Sweep(context.Background())
	if err == nil {
		t.Fatal("Sweep error = nil, want the aggregated read failure")
	}
	// The healthy version was still staled despite the other version's failure.
	after, _ := fixture.repo.GetReportByID(context.Background(), good.ID)
	if after.Status != delivery.ReportStatusStale {
		t.Fatalf("healthy version not staled after isolated failure: %+v", after)
	}
	if stats.Staled != 1 {
		t.Fatalf("sweep staled = %d, want 1 (healthy version only)", stats.Staled)
	}
}
