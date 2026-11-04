package render

// Task 118 (Release-T6): the rendered report (template v1 is frozen byte for
// byte) must not present a flaky-triaged failure as an application product
// defect. The renderer projects the triage-honoring classification into the
// existing columns without changing the template document.

import (
	"context"
	"strings"
	"testing"
	"time"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

func flakyRenderSnapshot() delivery.ReportSnapshot {
	waveID := int64(12)
	return delivery.ReportSnapshot{
		Kind:      delivery.ReportKindWave,
		VersionID: 7,
		WaveID:    &waveID,
		Waves: []delivery.WaveSnapshot{{
			ID: waveID, VersionID: 7, Status: "done",
			Counts: delivery.WaveCountsSnapshot{Total: 1, Failed: 1},
		}},
		CaseRuns: []delivery.CaseRunSnapshot{{
			ID: 5001, VersionID: 7, WaveID: waveID, CaseVersionID: 3001,
			CaseTitle: "AC-FR-025-01 发布文章", Status: "failed",
			FailureClass:         "application",
			TriageClassification: delivery.TriageClassificationFlaky,
			StepCount:            4, StepFailedCount: 1,
			AssertionCount: 4, AssertionFailedCount: 1,
			CreatedAt: time.Date(2026, 11, 4, 0, 22, 0, 0, time.UTC),
		}},
		Issues: []delivery.IssueSnapshot{{
			ID: 8001, VersionID: 7, Title: "AC-FR-025-01 发布文章 · step 3 failure",
			Status: delivery.IssueStatusOpen, Category: "ui", Severity: "major",
			Source:               "failure_candidate",
			CaseRunIDs:           []int64{5001},
			TriageStatus:         string(delivery.AnalysisStatusDone),
			TriageClassification: delivery.TriageClassificationFlaky,
			CreatedAt:            time.Date(2026, 11, 4, 0, 22, 0, 0, time.UTC),
			UpdatedAt:            time.Date(2026, 11, 4, 0, 22, 0, 0, time.UTC),
		}},
		Coverage: delivery.CoverageSnapshot{
			RequirementsTotal: 1, CaseVersionsTotal: 1,
			Gaps: []delivery.CoverageGapSnapshot{{RequirementID: 101, CaseVersionID: 3001, Reason: delivery.CoverageGapExecutionFlaky}},
		},
		GeneratedAt: time.Date(2026, 11, 4, 0, 22, 0, 0, time.UTC),
	}
}

func TestRenderHonorsFlakyTriage(t *testing.T) {
	output, err := newVersionedRenderer(t, TemplateVersionV1).Render(
		context.Background(), flakyRenderSnapshot(), "", allSections())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	text := string(output)
	if strings.Contains(text, ">application<") {
		t.Fatalf("rendered report still shows the failure as application: %s", text)
	}
	if !strings.Contains(text, ">automation<") {
		t.Fatalf("rendered report does not show the triage-honoring automation class")
	}
	if !strings.Contains(text, ">flaky<") || !strings.Contains(text, "execution_flaky_coverage") {
		t.Fatalf("rendered report does not show the flaky issue/coverage classification")
	}
}
