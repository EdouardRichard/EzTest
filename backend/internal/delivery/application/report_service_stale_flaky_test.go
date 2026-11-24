package deliveryapp

// Task 118 (Release-T6): a published wave report whose deterministic input
// changed (for example the issue triage finished after publication and turned
// the failure into flaky) must be detected as stale and regenerated on the next
// ensure pass. Otherwise the first publication keeps claiming a product defect.
//
// v0.2.4-G T11: stale DETECTION moved out of EnsureReports into the causal
// watcher + complete sweep (MarkStaleIfChanged). This test simulates that
// detection step, then asserts EnsureReports regenerates the stale wave report.

import (
	"context"
	"testing"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

func TestEnsureReportsRegeneratesStaleWaveReport(t *testing.T) {
	fixture := newReportFixture(t)
	versionID := int64(7)
	fixture.data.version[versionID] = reportTestVersionSnapshot(versionID)
	report, _ := fixture.seedPublishedWaveReport(versionID, 12, reportTestFingerprint("a"))

	// The causal watcher/sweep detects the changed input and marks it stale.
	if _, err := fixture.svc.MarkStaleIfChanged(context.Background(), versionID); err != nil {
		t.Fatalf("MarkStaleIfChanged: %v", err)
	}
	if err := fixture.svc.EnsureReports(context.Background(), versionID); err != nil {
		t.Fatalf("EnsureReports: %v", err)
	}
	revisions, err := fixture.repo.ListRevisions(context.Background(), report.ID)
	if err != nil {
		t.Fatalf("ListRevisions: %v", err)
	}
	if len(revisions) != 2 {
		t.Fatalf("revisions = %+v, want a regenerated revision after the input changed", revisions)
	}
	if revisions[0].Status != delivery.ReportStatusDraft || revisions[0].Revision != 2 {
		t.Fatalf("latest revision = %+v, want draft r2", revisions[0])
	}
	stored, err := fixture.repo.GetReport(context.Background(), versionID, report.ID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if stored.Status != delivery.ReportStatusStale {
		t.Fatalf("report status = %q, want stale while r2 is in flight", stored.Status)
	}
	taskEnqueued := false
	for _, call := range fixture.tasks.calls {
		if call == "task:100:2" {
			taskEnqueued = true
		}
	}
	if !taskEnqueued {
		t.Fatalf("tasks = %v, want the report r2 task", fixture.tasks.calls)
	}
}
