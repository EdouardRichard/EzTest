package deliveryapp

// Task 118 (Release-T6): the report narrative must honor a platform triage
// classification=flaky. A report whose only application-class failure was
// triaged flaky may not be presented as a product defect, and the deterministic
// fallback must describe the flaky coverage gap instead. A real application
// failure keeps the product-defect conclusion.

import (
	"context"
	"strings"
	"testing"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

func flakyTriageNarrativeSnapshot() delivery.ReportSnapshot {
	waveID := int64(12)
	return delivery.ReportSnapshot{
		Kind:      delivery.ReportKindWave,
		VersionID: 7,
		WaveID:    &waveID,
		Requirements: []delivery.RequirementSnapshot{
			{ID: 101, VersionID: 7, Title: "FR-025 保存草稿与发布", Status: "draft"},
		},
		Waves: []delivery.WaveSnapshot{{
			ID: waveID, VersionID: 7, Status: "done", Trigger: "retest",
			Counts: delivery.WaveCountsSnapshot{Total: 1, Failed: 1},
		}},
		CaseRuns: []delivery.CaseRunSnapshot{{
			ID: 5001, VersionID: 7, WaveID: waveID, CaseVersionID: 3001, RequirementID: 101,
			CaseTitle: "AC-FR-025-01 发布文章（正向）", Status: "failed",
			FailureClass:         "application",
			TriageClassification: delivery.TriageClassificationFlaky,
			StepCount:            4, StepFailedCount: 1,
			AssertionCount: 4, AssertionFailedCount: 1,
		}},
		Issues: []delivery.IssueSnapshot{{
			ID: 8001, VersionID: 7, Title: "AC-FR-025-01 发布文章（正向） · step 3: ui failure",
			Status: delivery.IssueStatusOpen, Category: "ui", Severity: "major", Source: "failure_candidate",
			CaseRunIDs:           []int64{5001},
			TriageStatus:         string(delivery.AnalysisStatusDone),
			TriageClassification: delivery.TriageClassificationFlaky,
		}},
		Coverage: delivery.CoverageSnapshot{
			RequirementsTotal: 1, RequirementsCovered: 0,
			CaseVersionsTotal: 1, CaseVersionsCovered: 0,
			Gaps: []delivery.CoverageGapSnapshot{{
				RequirementID: 101, CaseVersionID: 3001, Reason: delivery.CoverageGapExecutionFlaky,
			}},
			ByRequirement: []delivery.RequirementCoverageSnapshot{{
				RequirementID: 101, CaseVersionsTotal: 1, CaseVersionsFlakyCoverage: 1,
			}},
		},
	}
}

// realApplicationNarrativeSnapshot is the same failure without a flaky triage:
// the report must keep the product-defect conclusion.
func realApplicationNarrativeSnapshot() delivery.ReportSnapshot {
	snapshot := flakyTriageNarrativeSnapshot()
	snapshot.CaseRuns[0].TriageClassification = ""
	snapshot.Issues[0].TriageStatus = string(delivery.AnalysisStatusDone)
	snapshot.Issues[0].TriageClassification = "ui"
	snapshot.Coverage.Gaps[0].Reason = "execution_failed_coverage"
	snapshot.Coverage.ByRequirement[0] = delivery.RequirementCoverageSnapshot{
		RequirementID: 101, CaseVersionsTotal: 1, CaseVersionsFailedCoverage: 1,
	}
	return snapshot
}

const reportFlakyProductDefectNarrative = `## 失败与缺陷结论
- 失败分类为 application，即产品行为与已澄清需求存在偏差，按缺陷结论呈现，不作为自动化脚本问题处理。
- 当前该缺陷已有 1 个 open 状态的 major（ui 分类）问题记录。`

// TestFlakyTriageNarrativeRejectsProductDefect pins the narrative guard: when
// the only application-class failure was triaged flaky, a narrative that
// presents it as a product defect is rejected so the writer can rewrite it (and
// the publish step falls back to deterministic text).
func TestFlakyTriageNarrativeRejectsProductDefect(t *testing.T) {
	err := validateReportNarrative(flakyTriageNarrativeSnapshot(), reportFlakyProductDefectNarrative)
	if err == nil {
		t.Fatal("flaky-triaged failure was accepted as an application/product defect")
	}
	if !strings.Contains(err.Error(), delivery.TriageClassificationFlaky) &&
		!strings.Contains(err.Error(), "缺陷") {
		t.Fatalf("rejection does not explain the triage contradiction: %v", err)
	}

	consistent := `## 失败与 flaky 结论
- 失败分类为 flaky（自动化不稳定），不计入产品缺陷。
- 用例版本覆盖：0 / 1；需求覆盖：0 / 1。
- 未有效覆盖 1 个用例版本（execution_flaky_coverage）。`
	if err := validateReportNarrative(flakyTriageNarrativeSnapshot(), consistent); err != nil {
		t.Fatalf("flaky-consistent narrative was rejected: %v", err)
	}

	mismatched := "- 未有效覆盖 2 个用例版本（execution_flaky_coverage）。"
	if err := validateReportNarrative(flakyTriageNarrativeSnapshot(), mismatched); err == nil {
		t.Fatal("wrong execution_flaky_coverage gap count was accepted")
	}
}

// TestDeterministicNarrativeHonorsFlakyTriage pins the server-generated
// fallback: it states the flaky classification and flaky coverage gap and never
// claims a product defect for a flaky-only failure.
func TestDeterministicNarrativeHonorsFlakyTriage(t *testing.T) {
	snapshot := flakyTriageNarrativeSnapshot()
	narrative := deterministicReportNarrative(snapshot)
	for _, want := range []string{
		"flaky 分诊 1 条", "execution_flaky_coverage 1", "不计入产品缺陷",
	} {
		if !strings.Contains(narrative, want) {
			t.Fatalf("deterministic narrative is missing %q:\n%s", want, narrative)
		}
	}
	for _, forbidden := range []string{"失败分类为 application", "产品行为"} {
		if strings.Contains(narrative, forbidden) {
			t.Fatalf("deterministic narrative still claims %q:\n%s", forbidden, narrative)
		}
	}
	if err := validateReportNarrative(snapshot, narrative); err != nil {
		t.Fatalf("deterministic fallback contradicts its own snapshot: %v", err)
	}
}

// TestRealApplicationNarrativeStillProductDefect pins that the new guard never
// weakens real application defects: without a flaky triage the product-defect
// conclusion stays valid.
func TestRealApplicationNarrativeStillProductDefect(t *testing.T) {
	snapshot := realApplicationNarrativeSnapshot()
	if err := validateReportNarrative(snapshot, reportFlakyProductDefectNarrative); err != nil {
		t.Fatalf("real application defect narrative was rejected: %v", err)
	}
	narrative := deterministicReportNarrative(snapshot)
	for _, want := range []string{"execution_failed_coverage 1"} {
		if !strings.Contains(narrative, want) {
			t.Fatalf("deterministic narrative is missing %q:\n%s", want, narrative)
		}
	}
	if strings.Contains(narrative, "flaky 分诊") {
		t.Fatalf("real application fallback mentions flaky triage:\n%s", narrative)
	}
}

// TestFlakyTriageFallbackPublish verifies the publish-time guard end to end:
// a contradictory candidate is replaced and the published revision carries the
// triage-honoring deterministic text.
func TestFlakyTriageFallbackPublish(t *testing.T) {
	fixture := newReportFixture(t)
	report, _ := fixture.seedWaveReport(7, 12)
	fixture.data.wave[reportDataWaveKey(7, 12)] = flakyTriageNarrativeSnapshot()
	stageReportNarrative(ReportNarrativeDraft{
		ReportID: report.ID, Revision: 1, SummaryMarkdown: reportFlakyProductDefectNarrative,
	})
	if err := fixture.svc.FinalizeRevision(context.Background(), report.ID, 1); err != nil {
		t.Fatalf("FinalizeRevision: %v", err)
	}
	narrative := fixture.renderer.lastNarrative
	if !strings.Contains(narrative, "flaky") || !strings.Contains(narrative, "execution_flaky_coverage") {
		t.Fatalf("published fallback narrative does not honor the flaky triage:\n%s", narrative)
	}
	if strings.Contains(narrative, "按缺陷结论呈现") {
		t.Fatalf("published fallback narrative still claims a product defect:\n%s", narrative)
	}
}
