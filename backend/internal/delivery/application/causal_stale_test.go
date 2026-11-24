package deliveryapp

// v0.2.4-G T11 (SC-G8, R-G7/R-G10/R-G10a): causal stale completeness. A change
// to ANY conclusion input of a published report must mark that report stale,
// driven by the conclusion-input change EVENT (event-driven primary), for every
// version regardless of running/completed status. Judge state events refresh the
// report display (R-G10a). updated_at alone is NOT a conclusion input and must
// never mark a report stale (R-G3).

import (
	"context"
	"strconv"
	"testing"
	"time"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	eventingdomain "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
)

// fireStaleEvent delivers one conclusion-input change event to the watcher the
// same way the composition root does (eventSvc.Observe / Listener.Observe). The
// version scope mirrors execution.publishEvent (decimal string version_id).
func fireStaleEvent(t *testing.T, w *ReportStaleWatcher, eventType string, versionID int64) {
	t.Helper()
	ref := strconv.FormatInt(versionID, 10)
	err := w.Observe(context.Background(), eventingdomain.Event{
		Type:    eventType,
		Channel: "version:" + ref,
		Scope:   map[string]any{"version_id": ref},
	})
	if err != nil {
		t.Fatalf("Observe(%s): %v", eventType, err)
	}
}

// seedCurrentVersionReport seeds a published version report whose stored
// fingerprint equals the CURRENT reader snapshot, so a later conclusion-input
// mutation is the only thing that can make it stale. It uses an auto-assigned
// report ID so several versions can coexist (the shared fixture helper hardcodes
// ID 200, which would collide across versions).
func seedCurrentVersionReport(t *testing.T, f *reportFixture, versionID int64) delivery.Report {
	t.Helper()
	snapshot := reportTestVersionSnapshot(versionID)
	f.data.version[versionID] = snapshot
	report := f.repo.seedReport(delivery.Report{VersionID: versionID, Kind: delivery.ReportKindVersion})
	revision := f.repo.seedRevision(delivery.ReportRevision{ReportID: report.ID, Revision: 1})
	published, _ := f.repo.seedPublished(report, revision, 5002, delivery.Fingerprint(snapshot))
	return published
}

func newCausalWatcher(f *reportFixture) *ReportStaleWatcher {
	return NewReportStaleWatcher(f.svc, f.svc, nil, nil)
}

// TestTriageOnlyChangeMarksStale pins the Review Focus #5 case: a published
// report whose ONLY changed input is the issue triage projection must become
// stale. Triage analysis completion emits no issue field event today, so the
// complete sweep is the path that heals it (mutation: remove the sweep and this
// stays published -> RED).
func TestTriageOnlyChangeMarksStale(t *testing.T) {
	fixture := newReportFixture(t)
	versionID := int64(7)
	report := seedCurrentVersionReport(t, fixture, versionID)

	// Only the triage projection changes (no verdict/evidence/AC change).
	snapshot := fixture.data.version[versionID]
	snapshot.Issues[0].TriageStatus = "done"
	snapshot.Issues[0].TriageClassification = delivery.TriageClassificationFlaky
	fixture.data.version[versionID] = snapshot

	sweeper := NewReportStaleSweeper(newFakePublishedVersionLister(fixture.repo), fixture.svc, nil)
	stats, err := sweeper.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if stats.Staled != 1 {
		t.Fatalf("sweep staled = %d, want 1 (triage-only change)", stats.Staled)
	}
	after, err := fixture.repo.GetReportByID(context.Background(), report.ID)
	if err != nil || after.Status != delivery.ReportStatusStale {
		t.Fatalf("report after triage-only sweep = %+v err=%v, want stale", after, err)
	}
	if got := fixture.events.count("report.stale"); got != 1 {
		t.Fatalf("report.stale events = %d, want 1", got)
	}
}

// TestAllConclusionInputsTriggerStale pins R-G10: every conclusion-input change
// event drives the causal stale check of the affected version's published
// report, without any sweep. Each leg mutates the representative conclusion
// input and fires its event.
func TestAllConclusionInputsTriggerStale(t *testing.T) {
	cases := []struct {
		name      string
		eventType string
		mutate    func(*delivery.ReportSnapshot)
	}{
		{
			name:      "evidence settlement",
			eventType: "evidence.created",
			mutate: func(s *delivery.ReportSnapshot) {
				s.CaseRuns[0].EvidenceSHA256s = []string{"e1", "e2"}
			},
		},
		{
			name:      "evidence dropped",
			eventType: "evidence.skipped",
			mutate: func(s *delivery.ReportSnapshot) {
				s.CaseRuns[0].EvidenceDroppedCount++
			},
		},
		{
			name:      "case_run terminal",
			eventType: "case_run.finished",
			mutate: func(s *delivery.ReportSnapshot) {
				s.CaseRuns[0].Status = "failed"
				s.CaseRuns[0].TerminalCause = "assertion_failed"
			},
		},
		{
			name:      "issue triage/status/severity/category/dedupe",
			eventType: "issue.updated",
			mutate: func(s *delivery.ReportSnapshot) {
				s.Issues[0].Severity = "minor"
			},
		},
		{
			name:      "AC coverage (case confirmation)",
			eventType: "design.case_confirmed",
			mutate: func(s *delivery.ReportSnapshot) {
				s.Coverage.CaseVersionsCovered++
			},
		},
		{
			name:      "requirement closure",
			eventType: "requirement.closed",
			mutate: func(s *delivery.ReportSnapshot) {
				s.Requirements[0].Status = "obsolete"
			},
		},
		{
			name:      "waiver",
			eventType: "version.waived",
			mutate: func(s *delivery.ReportSnapshot) {
				s.ClosureKind = "waived"
				s.Unverified = true
			},
		},
		{
			name:      "reopen",
			eventType: "version.reopened",
			mutate: func(s *delivery.ReportSnapshot) {
				s.Requirements[0].ReopenCount++
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newReportFixture(t)
			versionID := int64(7)
			report := seedCurrentVersionReport(t, fixture, versionID)

			snapshot := fixture.data.version[versionID]
			tc.mutate(&snapshot)
			fixture.data.version[versionID] = snapshot

			watcher := newCausalWatcher(fixture)
			fireStaleEvent(t, watcher, tc.eventType, versionID)
			if err := watcher.Drain(context.Background()); err != nil {
				t.Fatalf("Drain: %v", err)
			}
			after, err := fixture.repo.GetReportByID(context.Background(), report.ID)
			if err != nil || after.Status != delivery.ReportStatusStale {
				t.Fatalf("report after %s = %+v err=%v, want stale", tc.eventType, after, err)
			}
			if got := fixture.events.count("report.stale"); got != 1 {
				t.Fatalf("report.stale events = %d, want 1", got)
			}
		})
	}
}

// TestJudgeEventRefreshesDisplay pins R-G10a: a judge state event regenerates the
// affected version's published report to refresh the judge display, even though
// review_state/verdict are display-only (not canonical fingerprint members), so
// MarkStaleIfChanged alone would never refresh it.
func TestJudgeEventRefreshesDisplay(t *testing.T) {
	for _, eventType := range []string{
		"design.judgment_submitted",
		"design.judgment_overridden",
		"design.rework_requested",
		"design.judge_exhausted",
	} {
		t.Run(eventType, func(t *testing.T) {
			fixture := newReportFixture(t)
			versionID := int64(7)
			report := seedCurrentVersionReport(t, fixture, versionID)
			fixture.data.wave[reportDataWaveKey(versionID, 12)] = reportTestWaveSnapshot(versionID, 12)

			watcher := newCausalWatcher(fixture)
			fireStaleEvent(t, watcher, eventType, versionID)
			if err := watcher.Drain(context.Background()); err != nil {
				t.Fatalf("Drain: %v", err)
			}
			revisions, err := fixture.repo.ListRevisions(context.Background(), report.ID)
			if err != nil {
				t.Fatalf("ListRevisions: %v", err)
			}
			if len(revisions) != 2 {
				t.Fatalf("revisions = %d, want a display-refresh revision after %s", len(revisions), eventType)
			}
			if revisions[0].Status != delivery.ReportStatusPublished {
				t.Fatalf("latest revision = %+v, want published display refresh", revisions[0])
			}
		})
	}
}

// TestJudgeEventDoesNotCreateFirstReport pins that a judge display refresh only
// acts on an already-published report: a judge event must not create the first
// version report (that is EnsureVersionReport's gated job).
func TestJudgeEventDoesNotCreateFirstReport(t *testing.T) {
	fixture := newReportFixture(t)
	versionID := int64(7)
	fixture.data.version[versionID] = reportTestVersionSnapshot(versionID) // execution data, but NO report yet

	watcher := newCausalWatcher(fixture)
	fireStaleEvent(t, watcher, "design.judgment_submitted", versionID)
	if err := watcher.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	kind := delivery.ReportKindVersion
	reports, err := fixture.repo.ListReports(context.Background(), versionID, ReportFilter{Kind: &kind, Limit: 10})
	if err != nil || len(reports) != 0 {
		t.Fatalf("judge event created a first report: %+v err=%v", reports, err)
	}
}

// TestUpdatedAtAloneNotStale pins R-G3: a row-modification timestamp change with
// no conclusion-input change must NOT mark the report stale (neither causal nor
// sweep).
func TestUpdatedAtAloneNotStale(t *testing.T) {
	fixture := newReportFixture(t)
	versionID := int64(7)
	report := seedCurrentVersionReport(t, fixture, versionID)

	snapshot := fixture.data.version[versionID]
	snapshot.Requirements[0].UpdatedAt = snapshot.Requirements[0].UpdatedAt.Add(time.Hour)
	snapshot.Issues[0].UpdatedAt = snapshot.Issues[0].UpdatedAt.Add(time.Hour)
	fixture.data.version[versionID] = snapshot

	// Causal path: an issue.updated event fires but the fingerprint is unchanged.
	watcher := newCausalWatcher(fixture)
	fireStaleEvent(t, watcher, "issue.updated", versionID)
	if err := watcher.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	// Sweep path too.
	sweeper := NewReportStaleSweeper(newFakePublishedVersionLister(fixture.repo), fixture.svc, nil)
	if _, err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	after, err := fixture.repo.GetReportByID(context.Background(), report.ID)
	if err != nil || after.Status != delivery.ReportStatusPublished {
		t.Fatalf("report after updated_at-only change = %+v err=%v, want still published", after, err)
	}
	if got := fixture.events.count("report.stale"); got != 0 {
		t.Fatalf("report.stale events = %d, want 0 (updated_at is not a conclusion input)", got)
	}
}

// TestNonTriggerEventIgnored pins that unrelated events do not enqueue a stale
// check (the trigger set is conclusion inputs, not every event).
func TestNonTriggerEventIgnored(t *testing.T) {
	fixture := newReportFixture(t)
	versionID := int64(7)
	report := seedCurrentVersionReport(t, fixture, versionID)
	snapshot := fixture.data.version[versionID]
	snapshot.Issues[0].Severity = "minor"
	fixture.data.version[versionID] = snapshot

	watcher := newCausalWatcher(fixture)
	fireStaleEvent(t, watcher, "report.published", versionID)
	fireStaleEvent(t, watcher, "conversation.message", versionID)
	if err := watcher.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	after, _ := fixture.repo.GetReportByID(context.Background(), report.ID)
	if after.Status != delivery.ReportStatusPublished {
		t.Fatalf("non-trigger event staled the report: %+v", after)
	}
}
