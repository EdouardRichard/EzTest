package deliveryapp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// RenderMarkdown renders the current published revision of one report as a
// deterministic Markdown snapshot (spec §4.29 R-W2-3). It reuses the report
// snapshot, the immutable revision metadata and the evidence references; the
// generated-at time is the revision's as_of, never the current clock, so two
// calls over the same data are byte-identical.
func (s *ReportService) RenderMarkdown(ctx context.Context, versionID, reportID int64) ([]byte, delivery.ReportRevision, error) {
	if versionID <= 0 || reportID <= 0 {
		return nil, delivery.ReportRevision{}, platform.Invalid("report_id", "must be a positive decimal ID")
	}
	report, err := s.repo.GetReport(ctx, versionID, reportID)
	if err != nil {
		return nil, delivery.ReportRevision{}, mapReportError(err)
	}
	revision, err := s.currentExportRevision(ctx, report)
	if err != nil {
		return nil, delivery.ReportRevision{}, err
	}
	snapshot, err := s.snapshotFor(ctx, report)
	if err != nil {
		return nil, delivery.ReportRevision{}, err
	}
	return renderReportMarkdown(stampReportSnapshot(snapshot, report, revision), revision), revision, nil
}

// stampReportSnapshot stamps the authoritative report/revision metadata onto a
// snapshot. GeneratedAt is the revision as_of so the Markdown bytes never
// depend on the wall clock.
func stampReportSnapshot(snapshot delivery.ReportSnapshot, report delivery.Report, revision delivery.ReportRevision) delivery.ReportSnapshot {
	snapshot.GeneratedAt = revision.AsOf
	snapshot.ReportID = report.ID
	snapshot.ReportRevision = revision.Revision
	snapshot.InputFingerprint = revision.InputFingerprint
	return snapshot
}

// renderReportMarkdown renders one deterministic Markdown document. Every list
// is sorted by its stable decimal ID before rendering; titles are escaped so a
// value can never break the table layout. No map iteration reaches the output.
func renderReportMarkdown(snapshot delivery.ReportSnapshot, revision delivery.ReportRevision) []byte {
	var builder strings.Builder
	builder.Grow(4096)

	fmt.Fprintf(&builder, "# Report %s\n\n", platformid.Format(snapshot.ReportID))
	fmt.Fprintf(&builder, "- Kind: %s\n", snapshot.Kind)
	fmt.Fprintf(&builder, "- Version: %s\n", platformid.Format(snapshot.VersionID))
	if snapshot.WaveID != nil {
		fmt.Fprintf(&builder, "- Wave: %s\n", platformid.Format(*snapshot.WaveID))
	} else {
		builder.WriteString("- Wave: -\n")
	}
	fmt.Fprintf(&builder, "- Revision: %d\n", revision.Revision)
	fmt.Fprintf(&builder, "- Generated at: %s\n", markdownTime(revision.AsOf))
	fmt.Fprintf(&builder, "- Input fingerprint: %s\n", revision.InputFingerprint)
	fmt.Fprintf(&builder, "- Status: %s\n\n", revision.Status)

	writeMarkdownSummary(&builder, snapshot)
	writeMarkdownRequirements(&builder, snapshot.Requirements)
	writeMarkdownWaves(&builder, snapshot.Waves)
	writeMarkdownCaseRuns(&builder, snapshot.CaseRuns)
	writeMarkdownIssues(&builder, snapshot.Issues)
	writeMarkdownEvidence(&builder, snapshot.CaseRuns)
	writeMarkdownCoverage(&builder, snapshot.Coverage)
	writeMarkdownVersionSUTs(&builder, snapshot.VersionSUTs)
	if len(snapshot.TruncatedSections) > 0 {
		fmt.Fprintf(&builder, "## Truncation\n\n- Sections capped at the report input limit: %s\n",
			strings.Join(snapshot.TruncatedSections, ", "))
	}

	return []byte(builder.String())
}

func writeMarkdownSummary(builder *strings.Builder, snapshot delivery.ReportSnapshot) {
	passed, failed, blocked, inconclusive, skipped, other := 0, 0, 0, 0, 0, 0
	for _, run := range snapshot.CaseRuns {
		switch run.Status {
		case "passed":
			passed++
		case "failed":
			failed++
		case "blocked":
			blocked++
		case "inconclusive":
			inconclusive++
		case "skipped":
			skipped++
		default:
			other++
		}
	}
	openIssues := 0
	for _, issue := range snapshot.Issues {
		if issue.Status == delivery.IssueStatusOpen {
			openIssues++
		}
	}
	builder.WriteString("## Summary\n\n")
	fmt.Fprintf(builder, "- Executions: %d (passed %d, failed %d, blocked %d, inconclusive %d, skipped %d, other %d).\n",
		len(snapshot.CaseRuns), passed, failed, blocked, inconclusive, skipped, other)
	fmt.Fprintf(builder, "- Coverage: case versions %d / %d; requirements %d / %d.\n",
		snapshot.Coverage.CaseVersionsCovered, snapshot.Coverage.CaseVersionsTotal,
		snapshot.Coverage.RequirementsCovered, snapshot.Coverage.RequirementsTotal)
	fmt.Fprintf(builder, "- Issues: %d (open %d).\n\n", len(snapshot.Issues), openIssues)
}

func writeMarkdownRequirements(builder *strings.Builder, rows []delivery.RequirementSnapshot) {
	if len(rows) == 0 {
		return
	}
	sorted := sortReportRows(rows, func(row delivery.RequirementSnapshot) int64 { return row.ID })
	builder.WriteString("## Requirements\n\n")
	builder.WriteString("| ID | Title | Status |\n| --- | --- | --- |\n")
	for _, row := range sorted {
		fmt.Fprintf(builder, "| %s | %s | %s |\n",
			platformid.Format(row.ID), markdownCell(row.Title), markdownCell(row.Status))
	}
	builder.WriteString("\n")
}

func writeMarkdownWaves(builder *strings.Builder, rows []delivery.WaveSnapshot) {
	if len(rows) == 0 {
		return
	}
	sorted := sortReportRows(rows, func(row delivery.WaveSnapshot) int64 { return row.ID })
	builder.WriteString("## Waves\n\n")
	builder.WriteString("| ID | Status | Trigger | Total | Passed | Failed | Blocked | Inconclusive | Skipped |\n")
	builder.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, row := range sorted {
		fmt.Fprintf(builder, "| %s | %s | %s | %d | %d | %d | %d | %d | %d |\n",
			platformid.Format(row.ID), markdownCell(row.Status), markdownCell(row.Trigger),
			row.Counts.Total, row.Counts.Passed, row.Counts.Failed, row.Counts.Blocked,
			row.Counts.Inconclusive, row.Counts.Skipped)
	}
	builder.WriteString("\n")
}

func writeMarkdownCaseRuns(builder *strings.Builder, rows []delivery.CaseRunSnapshot) {
	if len(rows) == 0 {
		return
	}
	sorted := sortReportRows(rows, func(row delivery.CaseRunSnapshot) int64 { return row.ID })
	builder.WriteString("## Case Runs\n\n")
	builder.WriteString("| Case run | Case version | Title | Status | Failure class | Steps failed | Assertions failed | Evidence |\n")
	builder.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, row := range sorted {
		fmt.Fprintf(builder, "| %s | %s | %s | %s | %s | %d / %d | %d / %d | %d |\n",
			platformid.Format(row.ID), platformid.Format(row.CaseVersionID), markdownCell(row.CaseTitle),
			markdownCell(row.Status), markdownCell(row.EffectiveFailureClass()),
			row.StepFailedCount, row.StepCount, row.AssertionFailedCount, row.AssertionCount,
			len(row.Evidence))
	}
	builder.WriteString("\n")
}

func writeMarkdownIssues(builder *strings.Builder, rows []delivery.IssueSnapshot) {
	if len(rows) == 0 {
		return
	}
	sorted := sortReportRows(rows, func(row delivery.IssueSnapshot) int64 { return row.ID })
	builder.WriteString("## Issues\n\n")
	builder.WriteString("| ID | Title | Status | Category | Severity | Source |\n")
	builder.WriteString("| --- | --- | --- | --- | --- | --- |\n")
	for _, row := range sorted {
		fmt.Fprintf(builder, "| %s | %s | %s | %s | %s | %s |\n",
			platformid.Format(row.ID), markdownCell(row.Title), markdownCell(string(row.Status)),
			markdownCell(row.Category), markdownCell(row.Severity), markdownCell(row.Source))
	}
	builder.WriteString("\n")
}

// writeMarkdownEvidence prints the report/revision evidence references. It
// never reads evidence bytes: only the decimal IDs, kind, content sha256 and
// size are exposed (spec §6.1).
func writeMarkdownEvidence(builder *strings.Builder, runs []delivery.CaseRunSnapshot) {
	builder.WriteString("## Evidence\n\n")
	sortedRuns := sortReportRows(runs, func(row delivery.CaseRunSnapshot) int64 { return row.ID })
	wrote := false
	for _, run := range sortedRuns {
		evidence := sortReportRows(run.Evidence, func(row delivery.EvidenceSnapshot) int64 { return row.ID })
		for _, row := range evidence {
			wrote = true
			fmt.Fprintf(builder, "- Evidence %s (case run %s, kind %s, sha256 %s, %d bytes)\n",
				platformid.Format(row.ID), platformid.Format(run.ID), markdownCell(row.Kind),
				markdownCell(row.SHA256), row.SizeBytes)
		}
	}
	if !wrote {
		builder.WriteString("- (none)\n")
	}
	builder.WriteString("\n")
}

func writeMarkdownCoverage(builder *strings.Builder, coverage delivery.CoverageSnapshot) {
	builder.WriteString("## Coverage\n\n")
	fmt.Fprintf(builder, "- Case versions: %d / %d covered.\n", coverage.CaseVersionsCovered, coverage.CaseVersionsTotal)
	fmt.Fprintf(builder, "- Requirements: %d / %d covered.\n", coverage.RequirementsCovered, coverage.RequirementsTotal)
	if len(coverage.Gaps) == 0 {
		builder.WriteString("- Gaps: none.\n\n")
		return
	}
	gaps := append([]delivery.CoverageGapSnapshot(nil), coverage.Gaps...)
	sort.SliceStable(gaps, func(i, j int) bool {
		if gaps[i].RequirementID != gaps[j].RequirementID {
			return gaps[i].RequirementID < gaps[j].RequirementID
		}
		return gaps[i].CaseVersionID < gaps[j].CaseVersionID
	})
	builder.WriteString("\n| Requirement | Case version | Reason |\n| --- | --- | --- |\n")
	for _, gap := range gaps {
		fmt.Fprintf(builder, "| %s | %s | %s |\n",
			platformid.Format(gap.RequirementID), platformid.Format(gap.CaseVersionID), markdownCell(gap.Reason))
	}
	builder.WriteString("\n")
}

func writeMarkdownVersionSUTs(builder *strings.Builder, rows []delivery.VersionSUTSnapshot) {
	if len(rows) == 0 {
		return
	}
	sorted := sortReportRows(rows, func(row delivery.VersionSUTSnapshot) int64 { return row.ID })
	builder.WriteString("## Environments\n\n")
	builder.WriteString("| Version SUT | SUT | Environment |\n| --- | --- | --- |\n")
	for _, row := range sorted {
		fmt.Fprintf(builder, "| %s | %s | %s |\n",
			platformid.Format(row.ID), markdownCell(row.SUTName), markdownCell(row.EnvironmentName))
	}
	builder.WriteString("\n")
}

// sortReportRows returns a stable ascending-ID copy. The snapshot reader may
// return rows in any order; the Markdown contract fixes the order by ID.
func sortReportRows[T any](rows []T, id func(T) int64) []T {
	sorted := append([]T(nil), rows...)
	sort.SliceStable(sorted, func(i, j int) bool { return id(sorted[i]) < id(sorted[j]) })
	return sorted
}

// markdownCell escapes a value used inside one Markdown table cell.
func markdownCell(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.ReplaceAll(value, "\n", "<br>")
	value = strings.ReplaceAll(value, "|", `\|`)
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}

// markdownTime renders the fixed revision timestamp. A zero time renders an
// empty value (never the current clock), keeping byte stability.
func markdownTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}
