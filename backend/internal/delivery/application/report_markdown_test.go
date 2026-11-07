package deliveryapp

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// seedArtifactReport seeds one published version report revision with a
// deterministic snapshot, HTML content, and (when pdfReady) a ready PDF.
func (f *reportFixture) seedArtifactReport(versionID int64, pdfReady bool) (delivery.Report, delivery.ReportRevision) {
	evidenceSHA := strings.Repeat("e", 64)
	snapshot := reportTestVersionSnapshot(versionID)
	snapshot.Requirements = append(snapshot.Requirements, delivery.RequirementSnapshot{
		ID: 100, VersionID: versionID, Title: "第二条需求", Status: "closed",
	})
	snapshot.CaseRuns = append(snapshot.CaseRuns, delivery.CaseRunSnapshot{
		ID: 3000, VersionID: versionID, WaveID: 12, CaseVersionID: 4000,
		CaseTitle: "登录", Status: "passed",
		Evidence: []delivery.EvidenceSnapshot{{
			ID: 7000, CaseRunID: 3000, Kind: "log", SHA256: evidenceSHA, SizeBytes: 42,
		}},
	})
	snapshot.CaseRuns[0].Evidence = []delivery.EvidenceSnapshot{{
		ID: 7001, CaseRunID: 3001, Kind: "screenshot", SHA256: evidenceSHA, SizeBytes: 1234,
	}}
	f.data.version[versionID] = snapshot

	report := f.repo.seedReport(delivery.Report{
		ID: 100, VersionID: versionID, Kind: delivery.ReportKindVersion,
	})
	htmlRef := int64(5001)
	pdfRef := int64(5002)
	revision := delivery.ReportRevision{
		ReportID:         report.ID,
		Revision:         1,
		Status:           delivery.ReportStatusPublished,
		RenderStatus:     "done",
		HTMLRef:          &htmlRef,
		InputFingerprint: reportTestFingerprint("a"),
		AsOf:             time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC),
		TemplateVersion:  "1",
	}
	if pdfReady {
		revision.PDFRef = &pdfRef
		revision.PDFStatus = delivery.PDFStatusDone
	} else {
		revision.PDFStatus = delivery.PDFStatusPending
	}
	seeded := f.repo.seedRevision(revision)
	f.content.seed(htmlRef, seeded.ID, []byte("<!doctype html><html><body>report</body></html>"))
	if pdfReady {
		f.content.seedPDF(pdfRef, []byte("%PDF-1.4\n% test report\n%%EOF\n"))
	}
	report.CurrentRevisionID = &seeded.ID
	f.repo.reports[report.ID] = report
	return report, seeded
}

func TestReportMarkdownDeterministicAndReferences(t *testing.T) {
	f := newReportFixture(t)
	report, revision := f.seedArtifactReport(7, true)
	ctx := context.Background()

	first, gotRevision, err := f.svc.RenderMarkdown(ctx, 7, report.ID)
	if err != nil {
		t.Fatalf("RenderMarkdown first: %v", err)
	}
	if gotRevision.ID != revision.ID || gotRevision.Revision != 1 {
		t.Fatalf("revision = %+v, want id=%d revision=1", gotRevision, revision.ID)
	}
	second, _, err := f.svc.RenderMarkdown(ctx, 7, report.ID)
	if err != nil {
		t.Fatalf("RenderMarkdown second: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("markdown is not byte-stable:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	for _, needle := range []string{
		"# Report 100",
		"Revision: 1",
		"Generated at: 2026-11-01T06:00:00Z",
		"Evidence 7001",
		strings.Repeat("e", 64),
	} {
		if !bytes.Contains(first, []byte(needle)) {
			t.Errorf("markdown missing %q:\n%s", needle, first)
		}
	}

	// Fixed ordering: reversing the snapshot slices must not change the bytes.
	snapshot := f.data.version[7]
	snapshot.Requirements[0], snapshot.Requirements[1] = snapshot.Requirements[1], snapshot.Requirements[0]
	snapshot.CaseRuns[0], snapshot.CaseRuns[1] = snapshot.CaseRuns[1], snapshot.CaseRuns[0]
	f.data.version[7] = snapshot
	third, _, err := f.svc.RenderMarkdown(ctx, 7, report.ID)
	if err != nil {
		t.Fatalf("RenderMarkdown third: %v", err)
	}
	if !bytes.Equal(first, third) {
		t.Fatalf("markdown depends on snapshot order:\nfirst:\n%s\nthird:\n%s", first, third)
	}
}
