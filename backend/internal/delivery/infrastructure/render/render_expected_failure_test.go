package render

// Task 122 (Release-T10): the rendered report must not present an
// expected_failure-triaged run/issue as an application product defect. The
// frozen template keeps its columns; the renderer projects the triage-honoring
// classification (expected_failure) and keeps the stored issue status/severity
// visible. A real application failure keeps its application label.

import (
	"context"
	"strings"
	"testing"
	"time"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

func expectedFailureRenderSnapshot() delivery.ReportSnapshot {
	waveID := int64(12)
	return delivery.ReportSnapshot{
		Kind:      delivery.ReportKindWave,
		VersionID: 7,
		WaveID:    &waveID,
		Waves: []delivery.WaveSnapshot{{
			ID: waveID, VersionID: 7, Status: "done",
			Counts: delivery.WaveCountsSnapshot{Total: 2, Failed: 2},
		}},
		CaseRuns: []delivery.CaseRunSnapshot{
			{
				ID: 5001, VersionID: 7, WaveID: waveID, CaseVersionID: 3001,
				CaseTitle: "AC-FR-011-02 错误口令提示", Status: "failed",
				FailureClass: "application",
				StepCount:    4, StepFailedCount: 2, AssertionCount: 5, AssertionFailedCount: 2,
				CreatedAt: time.Date(2026, 11, 4, 0, 22, 0, 0, time.UTC),
			},
			{
				ID: 5002, VersionID: 7, WaveID: waveID, CaseVersionID: 3002,
				CaseTitle: "AC-FR-113-03 阅读进度恢复", Status: "failed",
				FailureClass:         "application",
				TriageClassification: delivery.TriageClassificationExpectedFailure,
				StepCount:            4, StepFailedCount: 0, AssertionCount: 1, AssertionFailedCount: 0,
				CreatedAt: time.Date(2026, 11, 4, 0, 22, 0, 0, time.UTC),
			},
		},
		Issues: []delivery.IssueSnapshot{
			{
				ID: 8001, VersionID: 7, Title: "AC-FR-011-02 · step 3: ui failure",
				Status: delivery.IssueStatusOpen, Category: "ui", Severity: "major", Source: "failure_candidate",
				TriageStatus: string(delivery.AnalysisStatusDone), TriageClassification: "ui",
			},
			{
				ID: 8002, VersionID: 7, Title: "AC-FR-113-03 阅读进度恢复 · step 4: ui failure",
				Status: delivery.IssueStatusOpen, Category: "ui", Severity: "minor", Source: "failure_candidate",
				CaseRunIDs:           []int64{5002},
				TriageStatus:         string(delivery.AnalysisStatusDone),
				TriageClassification: delivery.TriageClassificationExpectedFailure,
			},
		},
		Coverage: delivery.CoverageSnapshot{
			RequirementsTotal: 2, CaseVersionsTotal: 2,
			Gaps: []delivery.CoverageGapSnapshot{
				{RequirementID: 101, CaseVersionID: 3001, Reason: "execution_failed_coverage"},
				{RequirementID: 104, CaseVersionID: 3002, Reason: "execution_failed_coverage"},
			},
		},
		GeneratedAt: time.Date(2026, 11, 4, 0, 22, 0, 0, time.UTC),
	}
}

// renderRow returns the rendered table row carrying the given data attribute.
func renderRow(t *testing.T, output, attribute string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, attribute) {
			return line
		}
	}
	t.Fatalf("rendered report has no row %q", attribute)
	return ""
}

// TestRenderHonorsExpectedFailureTriage pins the visible projection: the
// expected-failure run shows failure class expected_failure (not application),
// its issue shows expected_failure as the classification while the stored open
// status and minor severity stay visible, and a real application failure is
// unchanged.
func TestRenderHonorsExpectedFailureTriage(t *testing.T) {
	output, err := newVersionedRenderer(t, TemplateVersionV1).Render(
		context.Background(), expectedFailureRenderSnapshot(), "", allSections())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	text := string(output)

	expectedRun := renderRow(t, text, `data-case-run-id="5002"`)
	if !strings.Contains(expectedRun, "<td>expected_failure</td>") {
		t.Fatalf("expected-failure run is not rendered as expected_failure:\n%s", expectedRun)
	}
	if strings.Contains(expectedRun, "<td>application</td>") {
		t.Fatalf("expected-failure run is still rendered as application:\n%s", expectedRun)
	}

	realRun := renderRow(t, text, `data-case-run-id="5001"`)
	if !strings.Contains(realRun, "<td>application</td>") {
		t.Fatalf("real application failure lost its application classification:\n%s", realRun)
	}

	expectedIssue := renderRow(t, text, `data-issue-id="8002"`)
	if !strings.Contains(expectedIssue, "<td>expected_failure</td>") {
		t.Fatalf("expected-failure issue is not rendered with its triage classification:\n%s", expectedIssue)
	}
	if !strings.Contains(expectedIssue, "<td>open</td>") || !strings.Contains(expectedIssue, "<td>minor</td>") {
		t.Fatalf("expected-failure issue lost its stored open/minor presentation:\n%s", expectedIssue)
	}
}
