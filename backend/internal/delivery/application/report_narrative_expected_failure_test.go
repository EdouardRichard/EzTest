package deliveryapp

// Task 122 (Release-T10): the report summary must honor the platform issue
// triage classification=expected_failure. The release PO evidence shows a wave
// with 12 runs: 4 passed, 7 real failed(application) defects and 1 expected
// failure (AC-FR-113-03; the agent token budget really ran out before step 4, so
// assertion_failed=0 and step_failed=0 and the triage is expected_failure). The
// published report still said "8 条失败全部为 failure_class=application ... 全部
// 按缺陷结论呈现" and counted the expected-failure issue among the product
// issues. The deterministic summary and the report.data_read projection must
// count expected_failure separately (需求: 不构成产品缺陷, need rerun) while a
// real application defect keeps the product-defect conclusion.

import (
	"context"
	"strings"
	"testing"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// expectedFailureReportSnapshot is the release PO shape: 12 case runs
// (4 passed + 7 real application failures + 1 expected_failure) and 8 open
// issues (7 product ui issues + 1 expected_failure issue). Coverage keeps the
// expected failure as a genuine execution gap (no fake pass; Task 121).
func expectedFailureReportSnapshot() delivery.ReportSnapshot {
	waveID := int64(12)
	requirements := []delivery.RequirementSnapshot{
		{ID: 101, VersionID: 7, Title: "FR-011 账号密码登录", Status: "closed"},
		{ID: 102, VersionID: 7, Title: "FR-014 页面访问控制与权限跳转", Status: "closed"},
		{ID: 103, VersionID: 7, Title: "FR-106 定时发布与发布日历", Status: "closed"},
		{ID: 104, VersionID: 7, Title: "FR-113 收藏、点赞与阅读进度", Status: "closed"},
	}
	runs := []delivery.CaseRunSnapshot{
		{ID: 2001, VersionID: 7, WaveID: waveID, CaseVersionID: 1101, RequirementID: 101, CaseTitle: "AC-FR-011-01 登录成功", Status: "passed", StepCount: 4, AssertionCount: 6},
		{ID: 2002, VersionID: 7, WaveID: waveID, CaseVersionID: 1401, RequirementID: 102, CaseTitle: "AC-FR-014-01 未登录访问后台", Status: "passed", StepCount: 3, AssertionCount: 4},
		{ID: 2003, VersionID: 7, WaveID: waveID, CaseVersionID: 1103, RequirementID: 101, CaseTitle: "AC-FR-011-03 邮箱登录", Status: "passed", StepCount: 3, AssertionCount: 4},
		{ID: 2004, VersionID: 7, WaveID: waveID, CaseVersionID: 1403, RequirementID: 102, CaseTitle: "AC-FR-014-03 回跳后台", Status: "passed", StepCount: 2, AssertionCount: 3},
	}
	for index, id := range []int64{2005, 2006, 2007, 2008, 2009, 2010, 2011} {
		runs = append(runs, delivery.CaseRunSnapshot{
			ID: id, VersionID: 7, WaveID: waveID, CaseVersionID: int64(1100 + index),
			RequirementID: 103, CaseTitle: "AC-FR-106-0" + string(rune('1'+index%3)) + " 预约发布",
			Status: "failed", FailureClass: "application",
			StepCount: 4, StepFailedCount: 1, AssertionCount: 4, AssertionFailedCount: 1,
			Evidence: reportBTrapEvidence(10),
		})
	}
	runs = append(runs, delivery.CaseRunSnapshot{
		ID: 2012, VersionID: 7, WaveID: waveID, CaseVersionID: 11303, RequirementID: 104,
		CaseTitle: "AC-FR-113-03 阅读进度恢复", Status: "failed",
		FailureClass:         "application",
		TriageClassification: delivery.TriageClassificationExpectedFailure,
		StepCount:            4, StepFailedCount: 0,
		AssertionCount: 1, AssertionFailedCount: 0,
		Evidence: reportBTrapEvidence(16),
	})

	issues := make([]delivery.IssueSnapshot, 0, 8)
	for index := 0; index < 7; index++ {
		severity := "minor"
		if index < 2 {
			severity = "major"
		}
		issues = append(issues, delivery.IssueSnapshot{
			ID: int64(3001 + index), VersionID: 7, Title: "AC-FR-106-01 · step 1: ui failure",
			Status: delivery.IssueStatusOpen, Category: "ui", Severity: severity, Source: "failure_candidate",
			TriageStatus: string(delivery.AnalysisStatusDone), TriageClassification: "ui",
		})
	}
	issues = append(issues, delivery.IssueSnapshot{
		ID: 3008, VersionID: 7, Title: "AC-FR-113-03 阅读进度恢复 · step 4: ui failure",
		Status: delivery.IssueStatusOpen, Category: "ui", Severity: "minor", Source: "failure_candidate",
		CaseRunIDs:           []int64{2012},
		TriageStatus:         string(delivery.AnalysisStatusDone),
		TriageClassification: delivery.TriageClassificationExpectedFailure,
	})

	return delivery.ReportSnapshot{
		Kind:         delivery.ReportKindWave,
		VersionID:    7,
		WaveID:       &waveID,
		Requirements: requirements,
		Waves: []delivery.WaveSnapshot{{
			ID: waveID, VersionID: 7, Status: "done", Trigger: "auto",
			Counts: delivery.WaveCountsSnapshot{Total: 12, Passed: 4, Failed: 8, Evidence: 156},
		}},
		CaseRuns: runs,
		Issues:   issues,
		Coverage: delivery.CoverageSnapshot{
			RequirementsTotal: 4, RequirementsCovered: 2,
			CaseVersionsTotal: 12, CaseVersionsCovered: 4,
			Gaps: []delivery.CoverageGapSnapshot{
				{RequirementID: 101, CaseVersionID: 1102, Reason: "execution_failed_coverage"},
				{RequirementID: 102, CaseVersionID: 1402, Reason: "execution_failed_coverage"},
				{RequirementID: 103, CaseVersionID: 10601, Reason: "execution_failed_coverage"},
				{RequirementID: 103, CaseVersionID: 10602, Reason: "execution_failed_coverage"},
				{RequirementID: 103, CaseVersionID: 10603, Reason: "execution_failed_coverage"},
				{RequirementID: 104, CaseVersionID: 11301, Reason: "execution_failed_coverage"},
				{RequirementID: 104, CaseVersionID: 11302, Reason: "execution_failed_coverage"},
				{RequirementID: 104, CaseVersionID: 11303, Reason: "execution_failed_coverage"},
			},
			ByRequirement: []delivery.RequirementCoverageSnapshot{
				{RequirementID: 101, CaseVersionsTotal: 3, CaseVersionsCovered: 2, CaseVersionsFailedCoverage: 1},
				{RequirementID: 102, CaseVersionsTotal: 3, CaseVersionsCovered: 2, CaseVersionsFailedCoverage: 1},
				{RequirementID: 103, CaseVersionsTotal: 3, CaseVersionsFailedCoverage: 3},
				{RequirementID: 104, CaseVersionsTotal: 3, CaseVersionsFailedCoverage: 3},
			},
		},
	}
}

const reportExpectedFailureContradictoryNarrative = `## 版本结论
本版本执行 1 个 wave（状态 done），12 个用例版本全部执行：通过 4 / 失败 8 / 阻塞 0 / 跳过 0，累计证据 156 条。8 条失败全部为 failure_class=application，平台 triage 无 flaky（flaky 问题数 0），因此全部按「产品行为与需求偏差」的缺陷结论呈现，不以自动化脚本问题解释。已登记 8 个 open 问题：分类 ui 8 条，严重度 major 2 / minor 6，triage 分布 ui 7、expected_failure 1。`

const reportExpectedFailureConsistentNarrative = `## 失败与分诊结论
- 失败 8 条：7 条 failure_class=application 按产品缺陷呈现；1 条 expected_failure 分诊（运行预算耗尽、未取得 step 4 观测），不构成产品缺陷证据，需重跑。
- 未关闭问题 8 个：未关闭产品问题 7 个；expected_failure 分诊 1 个不计入产品缺陷。
- 用例版本覆盖：4 / 12；需求覆盖：2 / 4。
- 未覆盖 0 个用例版本（execution_not_run）；未有效覆盖 8 个用例版本（execution_failed_coverage）。`

// TestReportSummaryCountsExpectedFailureSeparately pins the report summary:
// expected_failure runs and issues are counted separately from product defects,
// the release-style "all 8 failures are application product defects" narrative
// is rejected, and a corrected narrative that names expected_failure is
// accepted. The deterministic report.data_read projection exposes the same
// split so the report writer can quote instead of reinterpreting.
func TestReportSummaryCountsExpectedFailureSeparately(t *testing.T) {
	snapshot := expectedFailureReportSnapshot()

	narrative := deterministicReportNarrative(snapshot)
	for _, want := range []string{
		"执行总数 12",
		"通过（passed）4、失败（failed）8",
		"expected_failure 分诊 1 条",
		"不计入产品缺陷",
		"需重跑",
		"产品缺陷 7 条",
		"问题 8 个（未关闭 8",
		"expected_failure 分诊 1 个",
		"未关闭产品问题 7 个",
	} {
		if !strings.Contains(narrative, want) {
			t.Fatalf("deterministic report summary is missing %q:\n%s", want, narrative)
		}
	}
	if strings.Contains(narrative, "8 条失败全部") {
		t.Fatalf("deterministic report summary still claims all 8 failures are one class:\n%s", narrative)
	}
	if err := validateReportNarrative(snapshot, narrative); err != nil {
		t.Fatalf("deterministic report summary contradicts its own snapshot: %v", err)
	}

	// The exact release-style narrative must be rejected so the publisher falls
	// back to the deterministic summary.
	if err := validateReportNarrative(snapshot, reportExpectedFailureContradictoryNarrative); err == nil {
		t.Fatal("narrative claiming all failures are application product defects was accepted despite an expected_failure run")
	}
	for _, claim := range []string{
		"8 条失败全部为 failure_class=application，全部按缺陷结论呈现。",
		"失败 8 条全部为 application 产品缺陷。",
		"全部 8 条失败均为 application 产品缺陷。",
	} {
		if err := validateReportNarrative(snapshot, claim); err == nil {
			t.Fatalf("all-failures product-defect claim was accepted: %q", claim)
		}
	}
	// A truthful subset statement is not an "all failures" claim.
	if err := validateReportNarrative(snapshot, "其余 7 条失败全部为 application 产品缺陷；1 条 expected_failure 不计入产品缺陷。"); err != nil {
		t.Fatalf("truthful subset product-defect statement was rejected: %v", err)
	}
	if err := validateReportNarrative(snapshot, reportExpectedFailureConsistentNarrative); err != nil {
		t.Fatalf("expected_failure-consistent narrative was rejected: %v", err)
	}
	if err := validateReportNarrative(snapshot, "- expected_failure 分诊 2 条，不计入产品缺陷。"); err == nil {
		t.Fatal("wrong expected_failure triage count was accepted")
	}

	waveID := int64(12)
	summary := reportSnapshotSummary(delivery.Report{ID: 70, VersionID: 7, WaveID: &waveID, Kind: delivery.ReportKindWave}, 1, snapshot)
	caseRuns := summary["case_runs"].(map[string]any)
	byClass := caseRuns["by_failure_class"].(map[string]int)
	if byClass["application"] != 7 || byClass[delivery.FailureClassExpectedFailure] != 1 {
		t.Fatalf("case_runs.by_failure_class = %#v, want 7 application + 1 expected_failure", byClass)
	}
	rawClass := caseRuns["by_run_failure_class"].(map[string]int)
	if rawClass["application"] != 8 {
		t.Fatalf("case_runs.by_run_failure_class = %#v, want the raw 8 application failures preserved", rawClass)
	}
	issues := summary["issues"].(map[string]any)
	byTriage := issues["by_triage"].(map[string]int)
	if byTriage["ui"] != 7 || byTriage[delivery.TriageClassificationExpectedFailure] != 1 {
		t.Fatalf("issues.by_triage = %#v, want 7 ui + 1 expected_failure", byTriage)
	}
	if issues["expected_failure"] != 1 || issues["open_product"] != 7 || issues["total"] != 8 {
		t.Fatalf("issues summary = %#v, want 8 total with 1 expected_failure and 7 open product issues", issues)
	}
}

// expectedFailureOnlyReportSnapshot removes the real application failures from
// the release shape: the deterministic fallback must still be self-consistent
// (and never present the remaining open expected_failure issue as a product
// defect).
func expectedFailureOnlyReportSnapshot() delivery.ReportSnapshot {
	snapshot := expectedFailureReportSnapshot()
	runs := make([]delivery.CaseRunSnapshot, 0, 5)
	for _, run := range snapshot.CaseRuns {
		if run.ExpectedFailureTriage() || run.Status == "passed" {
			runs = append(runs, run)
		}
	}
	snapshot.CaseRuns = runs
	snapshot.Waves[0].Counts = delivery.WaveCountsSnapshot{Total: len(runs), Passed: 4, Failed: 1, Evidence: 156}
	snapshot.Issues = snapshot.Issues[len(snapshot.Issues)-1:]
	return snapshot
}

// TestExpectedFailureOnlyFallbackIsConsistent pins that an expected-failure-only
// report (no real product defects) produces a deterministic summary that its
// own guard accepts: the expected failure is named, counted once, and never
// presented as a product defect.
func TestExpectedFailureOnlyFallbackIsConsistent(t *testing.T) {
	snapshot := expectedFailureOnlyReportSnapshot()
	narrative := deterministicReportNarrative(snapshot)
	if !strings.Contains(narrative, "expected_failure 分诊 1 条") {
		t.Fatalf("expected-failure-only summary is missing the separate count:\n%s", narrative)
	}
	if strings.Contains(narrative, "产品缺陷 1 条") || strings.Contains(narrative, "未关闭产品问题") {
		t.Fatalf("expected-failure-only summary presented it as a product defect:\n%s", narrative)
	}
	if err := validateReportNarrative(snapshot, narrative); err != nil {
		t.Fatalf("expected-failure-only deterministic summary contradicts its own snapshot: %v", err)
	}
}

// TestExpectedFailurePublishFallbackWhenNarrativeMissing pins the publish path:
// a report revision whose writer proposed no narrative still publishes the
// deterministic summary so the expected_failure split is visible.
func TestExpectedFailurePublishFallbackWhenNarrativeMissing(t *testing.T) {
	fixture := newReportFixture(t)
	report, _ := fixture.seedWaveReport(7, 12)
	fixture.data.wave[reportDataWaveKey(7, 12)] = expectedFailureReportSnapshot()
	if err := fixture.svc.FinalizeRevision(context.Background(), report.ID, 1); err != nil {
		t.Fatalf("FinalizeRevision: %v", err)
	}
	narrative := fixture.renderer.lastNarrative
	if !strings.Contains(narrative, "expected_failure 分诊 1 条") || !strings.Contains(narrative, "不计入产品缺陷") {
		t.Fatalf("published fallback narrative does not separate the expected failure:\n%s", narrative)
	}
}

// TestRealApplicationStillProductDefect pins that the new expected_failure
// handling never weakens real defects: without an expected_failure triage all 8
// failed runs stay application/product defects, the issue count stays 8 open
// product issues, and the product-defect narrative remains valid.
func TestRealApplicationStillProductDefect(t *testing.T) {
	snapshot := expectedFailureReportSnapshot()
	snapshot.CaseRuns[len(snapshot.CaseRuns)-1].TriageClassification = ""
	snapshot.Issues[len(snapshot.Issues)-1].TriageClassification = "ui"

	for index := range snapshot.CaseRuns {
		if snapshot.CaseRuns[index].ID == 2012 {
			if got := snapshot.CaseRuns[index].EffectiveFailureClass(); got != "application" {
				t.Fatalf("real application failure classified as %q, want application", got)
			}
		}
	}

	narrative := deterministicReportNarrative(snapshot)
	if strings.Contains(narrative, delivery.TriageClassificationExpectedFailure) {
		t.Fatalf("real-application fallback mentions expected_failure:\n%s", narrative)
	}
	if err := validateReportNarrative(snapshot, reportFlakyProductDefectNarrative); err != nil {
		t.Fatalf("real application defect narrative was rejected: %v", err)
	}

	waveID := int64(12)
	summary := reportSnapshotSummary(delivery.Report{ID: 70, VersionID: 7, WaveID: &waveID, Kind: delivery.ReportKindWave}, 1, snapshot)
	byClass := summary["case_runs"].(map[string]any)["by_failure_class"].(map[string]int)
	if byClass["application"] != 8 || byClass[delivery.FailureClassExpectedFailure] != 0 {
		t.Fatalf("case_runs.by_failure_class = %#v, want all 8 application", byClass)
	}
	issues := summary["issues"].(map[string]any)
	if issues["expected_failure"] != 0 || issues["open_product"] != 8 {
		t.Fatalf("issues summary = %#v, want 8 open product issues and no expected_failure", issues)
	}
}
