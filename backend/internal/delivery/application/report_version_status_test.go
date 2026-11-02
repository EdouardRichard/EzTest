package deliveryapp

import (
	"context"
	"testing"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// TestReportServiceVersionReportStatusPublished pins the version-close
// projection: the effective status is published only when the report and its
// current revision are published with rendered HTML, and the adapter returns
// the stored and recomputed fingerprints separately so the close predicate can
// detect stale input.
func TestReportServiceVersionReportStatusPublished(t *testing.T) {
	fixture := newReportFixture(t)
	report, revision := fixture.seedPublishedVersionReport(7, reportTestFingerprint("a"))
	snapshot := fixture.data.version[7]
	wantCurrent := delivery.Fingerprint(delivery.CapReportSnapshot(snapshot))

	status, err := fixture.svc.VersionReportStatus(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionReportStatus: %v", err)
	}
	if status.Status != delivery.ReportStatusPublished || status.RevisionID != revision.ID ||
		status.Fingerprint != reportTestFingerprint("a") || status.CurrentFingerprint != wantCurrent {
		t.Fatalf("status = %+v, want published revision %d stored a*64 current %s", status, revision.ID, wantCurrent)
	}
	if status.RevisionID == 0 || report.CurrentRevisionID == nil || status.RevisionID != *report.CurrentRevisionID {
		t.Fatalf("revision id = %d, want current revision %v", status.RevisionID, report.CurrentRevisionID)
	}
}

func TestReportServiceVersionReportStatusEffectiveStatus(t *testing.T) {
	t.Run("no version report", func(t *testing.T) {
		fixture := newReportFixture(t)
		status, err := fixture.svc.VersionReportStatus(context.Background(), 7)
		if err != nil {
			t.Fatalf("VersionReportStatus: %v", err)
		}
		if status.Status != "" || status.RevisionID != 0 || status.Fingerprint != "" {
			t.Fatalf("empty status = %+v", status)
		}
	})

	t.Run("published report with a still-draft current revision", func(t *testing.T) {
		fixture := newReportFixture(t)
		fixture.data.version[7] = reportTestVersionSnapshot(7)
		report := fixture.repo.seedReport(delivery.Report{ID: 200, VersionID: 7, Kind: delivery.ReportKindVersion})
		revision := fixture.repo.seedRevision(delivery.ReportRevision{
			ReportID: report.ID, Revision: 1, InputFingerprint: reportTestFingerprint("b"),
		})
		// Publish only the report header; the revision stays a draft.
		report.Status = delivery.ReportStatusPublished
		report.CurrentRevisionID = &revision.ID
		report.InputFingerprint = reportTestFingerprint("b")
		fixture.repo.seedReport(report)

		status, err := fixture.svc.VersionReportStatus(context.Background(), 7)
		if err != nil {
			t.Fatalf("VersionReportStatus: %v", err)
		}
		if status.Status == delivery.ReportStatusPublished {
			t.Fatalf("status = %q, a draft revision must never count as published", status.Status)
		}
	})

	t.Run("stale report keeps its literal status", func(t *testing.T) {
		fixture := newReportFixture(t)
		published, _ := fixture.seedPublishedVersionReport(7, reportTestFingerprint("c"))
		if changed, err := fixture.repo.MarkStale(context.Background(), published.ID); err != nil || !changed {
			t.Fatalf("MarkStale = %v, %v", changed, err)
		}
		status, err := fixture.svc.VersionReportStatus(context.Background(), 7)
		if err != nil {
			t.Fatalf("VersionReportStatus: %v", err)
		}
		if status.Status != delivery.ReportStatusStale {
			t.Fatalf("status = %q, want stale", status.Status)
		}
	})
}

func TestReportServiceVersionReportStatusDetectsStaleInput(t *testing.T) {
	fixture := newReportFixture(t)
	// The stored fingerprint is deliberately not the current input fingerprint.
	fixture.seedPublishedVersionReport(7, reportTestFingerprint("d"))
	status, err := fixture.svc.VersionReportStatus(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionReportStatus: %v", err)
	}
	if status.Fingerprint == status.CurrentFingerprint || status.CurrentFingerprint == "" {
		t.Fatalf("fingerprints = stored %q current %q, want different non-empty", status.Fingerprint, status.CurrentFingerprint)
	}
}

func TestReportServiceMarkVersionReportStale(t *testing.T) {
	fixture := newReportFixture(t)
	published, _ := fixture.seedPublishedVersionReport(7, reportTestFingerprint("e"))

	if err := fixture.svc.MarkVersionReportStale(context.Background(), 7, "requirement created"); err != nil {
		t.Fatalf("MarkVersionReportStale: %v", err)
	}
	if fixture.events.count(reportEventStale) != 1 {
		t.Fatalf("report.stale events = %d, want 1", fixture.events.count(reportEventStale))
	}
	stored, err := fixture.repo.GetReport(context.Background(), 7, published.ID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if stored.Status != delivery.ReportStatusStale {
		t.Fatalf("report status = %q, want stale", stored.Status)
	}
	// A second call is idempotent and never emits a duplicate event.
	if err := fixture.svc.MarkVersionReportStale(context.Background(), 7, "again"); err != nil {
		t.Fatalf("second MarkVersionReportStale: %v", err)
	}
	if fixture.events.count(reportEventStale) != 1 {
		t.Fatalf("report.stale events after repeat = %d, want 1", fixture.events.count(reportEventStale))
	}
}

func TestReportServiceMarkVersionReportStaleWithoutReport(t *testing.T) {
	fixture := newReportFixture(t)
	if err := fixture.svc.MarkVersionReportStale(context.Background(), 7, "no report"); err != nil {
		t.Fatalf("MarkVersionReportStale: %v", err)
	}
	if fixture.events.count(reportEventStale) != 0 {
		t.Fatal("unexpected report.stale event without a report")
	}
}

func TestReportServiceVersionReportStatusRejectsInvalidVersion(t *testing.T) {
	fixture := newReportFixture(t)
	if _, err := fixture.svc.VersionReportStatus(context.Background(), 0); err == nil {
		t.Fatal("VersionReportStatus(0) = nil error")
	}
	if err := fixture.svc.MarkVersionReportStale(context.Background(), 0, "x"); err == nil {
		t.Fatal("MarkVersionReportStale(0) = nil error")
	}
}
