package domain

import (
	"strings"
	"testing"
	"time"
)

// TestReportValidate pins the report row invariants of spec §4.2.2/§5.5.
func TestReportValidate(t *testing.T) {
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	actor := int64(7)
	report := Report{
		ID:               100,
		VersionID:        10,
		BatchID:          20,
		Revision:         1,
		Status:           ReportStatusPublished,
		Title:            "报告",
		Content:          reportTestContent(),
		InputFingerprint: reportTestHash,
		MarkdownPath:     "versions/10/reports/100-r1.md",
		MarkdownSHA256:   reportTestHash,
		MarkdownSize:     128,
		GeneratedBy:      &actor,
		PublishedAt:      &now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := report.Validate(); err != nil {
		t.Fatalf("valid report rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Report)
	}{
		{"id", func(r *Report) { r.ID = 0 }},
		{"version id", func(r *Report) { r.VersionID = 0 }},
		{"batch id", func(r *Report) { r.BatchID = 0 }},
		{"revision", func(r *Report) { r.Revision = 0 }},
		{"status", func(r *Report) { r.Status = "published-ish" }},
		{"fingerprint", func(r *Report) { r.InputFingerprint = "ABC" }},
		{"markdown sha", func(r *Report) { r.MarkdownSHA256 = "short" }},
		{"markdown size", func(r *Report) { r.MarkdownSize = -1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			broken := report
			test.mutate(&broken)
			if err := broken.Validate(); err == nil {
				t.Fatalf("report with invalid %s was accepted", test.name)
			}
		})
	}

	// A list projection carries no content and must still validate.
	listItem := report
	listItem.Content = ReportContent{}
	listItem.MarkdownPath = ""
	listItem.MarkdownSHA256 = ""
	listItem.MarkdownSize = 0
	if err := listItem.Validate(); err != nil {
		t.Fatalf("list projection rejected: %v", err)
	}
}

// TestReportStatusPredicates pins the small status helpers used by the service.
func TestReportStatusPredicates(t *testing.T) {
	if !IsValidReportStatus(ReportStatusPublished) || !IsValidReportStatus(ReportStatusStale) || !IsValidReportStatus(ReportStatusDraft) {
		t.Fatal("one of the report statuses is not valid")
	}
	if IsValidReportStatus("other") {
		t.Fatal("unknown report status accepted")
	}
	if !strings.HasPrefix(ReportReasonExportFailed, "report_") {
		t.Fatal("report reason constants must stay stable")
	}
}
