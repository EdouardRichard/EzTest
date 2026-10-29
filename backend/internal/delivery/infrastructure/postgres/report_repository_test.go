package postgres

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// reportTestTime is a lossless TIMESTAMPTZ reading.
var reportTestTime = time.Date(2026, 10, 28, 20, 30, 0, 0, time.UTC)

// reportScanRow assigns fixed values to the scan destinations, pinning the
// report column order without a database.
type reportScanRow struct{ values []any }

// Scan implements reportRowScanner.
func (r reportScanRow) Scan(dest ...any) error {
	if len(dest) != len(r.values) {
		return fmt.Errorf("report scan row: %d destinations for %d values", len(dest), len(r.values))
	}
	for index := range dest {
		if err := reportAssignScanValue(dest[index], r.values[index]); err != nil {
			return fmt.Errorf("report scan row column %d: %w", index+1, err)
		}
	}
	return nil
}

// reportAssignScanValue mimics the pgx codecs: nil clears the destination and
// an optional column (pointer destination) is allocated with the value.
func reportAssignScanValue(dest, value any) error {
	target := reflect.ValueOf(dest)
	if target.Kind() != reflect.Pointer || target.IsNil() {
		return fmt.Errorf("destination %T is not a non-nil pointer", dest)
	}
	elem := target.Elem()
	if value == nil {
		elem.Set(reflect.Zero(elem.Type()))
		return nil
	}
	source := reflect.ValueOf(value)
	if source.Type().AssignableTo(elem.Type()) {
		elem.Set(source)
		return nil
	}
	if source.Type().ConvertibleTo(elem.Type()) {
		elem.Set(source.Convert(elem.Type()))
		return nil
	}
	if elem.Kind() == reflect.Pointer && source.Type().AssignableTo(elem.Type().Elem()) {
		pointer := reflect.New(elem.Type().Elem())
		pointer.Elem().Set(source)
		elem.Set(pointer)
		return nil
	}
	return fmt.Errorf("cannot assign %T to %s", value, elem.Type())
}

// TestScanReportMapsEveryColumn pins the report column order and the nullable
// mapping of markdown_sha256, generated_by and published_at.
func TestScanReportMapsEveryColumn(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	generatedBy := int64(7)
	publishedAt := reportTestTime.Add(time.Minute)
	content := domain.ReportContent{Schema: domain.ReportSchemaV1, Summary: domain.ReportSummary{CaseCount: 2}}
	encoded, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("marshal content: %v", err)
	}
	row := reportScanRow{values: []any{
		int64(100), int64(10), int64(20), 2, domain.ReportStatusPublished, "报告",
		encoded, sha, "versions/10/reports/100-r2.md", sha, int64(len(encoded)),
		"", generatedBy, publishedAt, reportTestTime, reportTestTime,
	}}
	report, err := scanReport(row)
	if err != nil {
		t.Fatalf("scan report: %v", err)
	}
	if report.ID != 100 || report.VersionID != 10 || report.BatchID != 20 || report.Revision != 2 {
		t.Fatalf("ids/revision = %+v", report)
	}
	if report.Status != domain.ReportStatusPublished || report.Title != "报告" {
		t.Fatalf("status/title = %q/%q", report.Status, report.Title)
	}
	if report.Content.Schema != domain.ReportSchemaV1 || report.Content.Summary.CaseCount != 2 {
		t.Fatalf("content = %+v", report.Content)
	}
	if report.InputFingerprint != sha || report.MarkdownSHA256 != sha || report.MarkdownSize != int64(len(encoded)) {
		t.Fatalf("fingerprint/markdown metadata = %+v", report)
	}
	if report.GeneratedBy == nil || *report.GeneratedBy != generatedBy {
		t.Fatalf("generatedBy = %v", report.GeneratedBy)
	}
	if report.PublishedAt == nil || !report.PublishedAt.Equal(publishedAt) {
		t.Fatalf("publishedAt = %v", report.PublishedAt)
	}
	if !report.CreatedAt.Equal(reportTestTime) || !report.UpdatedAt.Equal(reportTestTime) {
		t.Fatalf("timestamps = %v / %v", report.CreatedAt, report.UpdatedAt)
	}

	nullable := reportScanRow{values: []any{
		int64(101), int64(10), int64(20), 1, domain.ReportStatusDraft, "",
		[]byte(nil), sha, "", nil, int64(0), "", nil, nil, reportTestTime, reportTestTime,
	}}
	draft, err := scanReport(nullable)
	if err != nil {
		t.Fatalf("scan draft: %v", err)
	}
	if draft.MarkdownSHA256 != "" || draft.GeneratedBy != nil || draft.PublishedAt != nil {
		t.Fatalf("nullable columns = %+v", draft)
	}
	if draft.Content.Schema != "" {
		t.Fatalf("list projection content = %+v, want zero", draft.Content)
	}
}

// TestScanReportPropagatesErrors pins that a broken row is not silently turned
// into an empty report.
func TestScanReportPropagatesErrors(t *testing.T) {
	if _, err := scanReport(reportScanRow{values: []any{int64(1)}}); err == nil {
		t.Fatal("a row with the wrong number of columns must fail")
	}
	if _, err := scanReport(reportScanErrorRow{}); !errors.Is(err, errReportScan) {
		t.Fatalf("scan error = %v, want %v", err, errReportScan)
	}
}

// errReportScan is the sentinel of reportScanErrorRow.
var errReportScan = errors.New("scan failed")

// reportScanErrorRow always fails.
type reportScanErrorRow struct{}

// Scan implements reportRowScanner.
func (reportScanErrorRow) Scan(...any) error { return errReportScan }

// TestReportRefsFromContent pins the report_ref derivation of spec §4.2.2:
// batch/case_run/issue/requirement plus 40-hex commits only.
func TestReportRefsFromContent(t *testing.T) {
	baseCommit := "1111111111111111111111111111111111111111"
	headCommit := "2222222222222222222222222222222222222222"
	content := domain.ReportContent{
		Batch: domain.ReportBatch{ID: "20", CodeBaseCommit: baseCommit, HeadCommit: headCommit},
		Cases: []domain.ReportCase{
			{CaseRunID: "101", CaseNodeID: "11"},
			{CaseRunID: "102", CaseNodeID: "12"},
		},
		Issues: []domain.ReportIssue{{IssueID: "41"}},
		Requirements: []domain.ReportRequirement{
			{RequirementID: "31"},
		},
	}
	refs := reportRefsFromContent(content)
	want := []reportRef{
		{Kind: "batch", ID: "20"},
		{Kind: "case_run", ID: "101"},
		{Kind: "case_run", ID: "102"},
		{Kind: "issue", ID: "41"},
		{Kind: "requirement", ID: "31"},
		{Kind: "commit", ID: baseCommit},
		{Kind: "commit", ID: headCommit},
	}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = %+v, want %+v", refs, want)
	}

	empty := reportRefsFromContent(domain.ReportContent{Batch: domain.ReportBatch{CodeBaseCommit: "not-a-commit"}})
	if len(empty) != 0 {
		t.Fatalf("empty content refs = %+v", empty)
	}
}

// TestReportCommitHex pins the commit ref format guard.
func TestReportCommitHex(t *testing.T) {
	valid := "0123456789abcdef0123456789abcdef01234567"
	if !reportCommitHex(valid) {
		t.Fatalf("%q rejected as a commit", valid)
	}
	for _, invalid := range []string{"", "0123456789ABCDEF0123456789abcdef01234567", valid + "0", "z123456789abcdef0123456789abcdef01234567"} {
		if reportCommitHex(invalid) {
			t.Fatalf("%q accepted as a commit", invalid)
		}
	}
}
