package deliveryapp

import (
	"context"
	"errors"
	"testing"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// TestReportServiceFinalizeRevisionPublishesDraft pins the composition-root
// finalizer contract: a report.* run's staged narrative candidate is published
// with the deterministic snapshot, then the finalized revision is verified as
// the published current revision.
func TestReportServiceFinalizeRevisionPublishesDraft(t *testing.T) {
	fixture := newReportFixture(t)
	report, _ := fixture.seedWaveReport(7, 12)
	stageReportNarrative(ReportNarrativeDraft{
		ReportID: report.ID, Revision: 1, SummaryMarkdown: "finalizer narrative",
	})

	if err := fixture.svc.FinalizeRevision(context.Background(), report.ID, 1); err != nil {
		t.Fatalf("FinalizeRevision: %v", err)
	}
	stored, err := fixture.repo.GetReport(context.Background(), 7, report.ID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if stored.Status != delivery.ReportStatusPublished || stored.CurrentRevisionID == nil {
		t.Fatalf("report after finalize = %+v", stored)
	}
	revision := fixture.repoRevision(t, report.ID, 1)
	if revision.Status != delivery.ReportStatusPublished || revision.RenderStatus != "done" {
		t.Fatalf("revision after finalize = %+v", revision)
	}
	if fixture.renderer.lastNarrative != "finalizer narrative" {
		t.Fatalf("renderer narrative = %q, want the staged candidate", fixture.renderer.lastNarrative)
	}
	if fixture.events.count(reportEventPublished) != 1 {
		t.Fatalf("report.published events = %d, want 1", fixture.events.count(reportEventPublished))
	}
}

func TestReportServiceFinalizeRevisionAlreadyPublishedIsIdempotent(t *testing.T) {
	fixture := newReportFixture(t)
	report, _ := fixture.seedWaveReport(7, 12)
	if _, err := fixture.svc.Publish(context.Background(), report.ID, 1, "first"); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	renderCalls := fixture.renderer.calls
	if err := fixture.svc.FinalizeRevision(context.Background(), report.ID, 1); err != nil {
		t.Fatalf("FinalizeRevision(published): %v", err)
	}
	if fixture.renderer.calls != renderCalls {
		t.Fatalf("renderer calls = %d after finalize, want %d", fixture.renderer.calls, renderCalls)
	}
}

func TestReportServiceFinalizeRevisionReportsPublishFailure(t *testing.T) {
	fixture := newReportFixture(t)
	report, _ := fixture.seedWaveReport(7, 12)
	fixture.renderer.err = errors.New("renderer unavailable")
	if err := fixture.svc.FinalizeRevision(context.Background(), report.ID, 1); err == nil {
		t.Fatal("FinalizeRevision(render failure) = nil error")
	}
	if stored, err := fixture.repo.GetReport(context.Background(), 7, report.ID); err != nil || stored.Status == delivery.ReportStatusPublished {
		t.Fatalf("report after failed finalize = %+v, %v", stored, err)
	}
	if err := fixture.svc.FinalizeRevision(context.Background(), 0, 1); err == nil {
		t.Fatal("FinalizeRevision(0) = nil error")
	}
}
