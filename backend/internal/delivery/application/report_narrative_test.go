package deliveryapp

// PO E2E Report B said FR-011/FR-014 "本轮没有任何用例版本获得有效执行结果"
// while the deterministic wave results showed AC-FR-011-01 and AC-FR-014-01
// passed (6/6 and 4/4 assertions, 13 and 8 evidence). The AI narrative also
// invented coverage numbers (2/11 and 7 execution_not_run gaps) that do not
// match the deterministic coverage (2/10, 6 not-run + 2 failed-coverage gaps).
// The tests below pin the fix: the deterministic snapshot is authoritative, a
// contradictory proposal is rejected so the writer can regenerate, and the
// publish step falls back to a server-generated deterministic narrative.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agenttools "github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

const reportBContradictoryNarrative = `### 覆盖情况
- 用例版本覆盖：2 / 11；需求覆盖：2 / 4。
- 未覆盖 7 个用例版本，原因均为 execution_not_run：FR-011 账号密码登录 ×2、FR-014 页面访问控制与权限跳转 ×2、FR-106 ×2、FR-113 ×1。
- 未有效覆盖 2 个用例版本，原因为 execution_failed_coverage：FR-106、FR-113 各 1。
- 需要注意：两个 P0 需求 FR-011、FR-014 本轮没有任何用例版本获得有效执行结果，版本级风险尚未得到验证。`

const reportBConsistentNarrative = `### 覆盖情况
- 用例版本覆盖：2 / 10；需求覆盖：2 / 4。
- 未覆盖 6 个用例版本（execution_not_run）。
- 未有效覆盖 2 个用例版本（execution_failed_coverage）。
- FR-011、FR-014 已各有 1 条 passed 结果，失败与阻塞项按结果表呈现。`

func reportBTrapEvidence(count int) []delivery.EvidenceSnapshot {
	rows := make([]delivery.EvidenceSnapshot, count)
	for index := range rows {
		rows[index] = delivery.EvidenceSnapshot{ID: int64(9000000 + index), Kind: "http"}
	}
	return rows
}

// reportBTrapSnapshot is the deterministic wave snapshot of Report B: four
// requirements, four case runs (FR-011 passed, FR-014 passed, FR-106 failed,
// FR-113 blocked) and 10 executable case versions of which 2 passed.
func reportBTrapSnapshot() delivery.ReportSnapshot {
	waveID := int64(12)
	return delivery.ReportSnapshot{
		Kind:      delivery.ReportKindWave,
		VersionID: 7,
		WaveID:    &waveID,
		Requirements: []delivery.RequirementSnapshot{
			{ID: 101, VersionID: 7, Title: "FR-011 账号密码登录", Status: "closed"},
			{ID: 102, VersionID: 7, Title: "FR-014 页面访问控制与权限跳转", Status: "closed"},
			{ID: 103, VersionID: 7, Title: "FR-106 定时发布与发布日历", Status: "closed"},
			{ID: 104, VersionID: 7, Title: "FR-113 收藏、点赞与阅读进度", Status: "closed"},
		},
		Waves: []delivery.WaveSnapshot{{
			ID: waveID, VersionID: 7, Status: "done", Trigger: "auto",
			Counts: delivery.WaveCountsSnapshot{Total: 4, Passed: 2, Failed: 1, Blocked: 1, Evidence: 45},
		}},
		CaseRuns: []delivery.CaseRunSnapshot{
			{
				ID: 2001, VersionID: 7, WaveID: waveID, CaseVersionID: 1101, RequirementID: 101,
				CaseTitle: "AC-FR-011-01 登录成功（正向）", Status: "passed",
				StepCount: 6, AssertionCount: 6, Evidence: reportBTrapEvidence(13),
			},
			{
				ID: 2002, VersionID: 7, WaveID: waveID, CaseVersionID: 1401, RequirementID: 102,
				CaseTitle: "AC-FR-014-01 未登录访问后台并登录回跳（负向→正向）", Status: "passed",
				StepCount: 3, AssertionCount: 4, Evidence: reportBTrapEvidence(8),
			},
			{
				ID: 2003, VersionID: 7, WaveID: waveID, CaseVersionID: 10601, RequirementID: 103,
				CaseTitle: "AC-FR-106-01 预约未来发布且预约中不公开", Status: "failed",
				FailureClass: "application", StepCount: 1, StepFailedCount: 1,
				Evidence: reportBTrapEvidence(10),
			},
			{
				ID: 2004, VersionID: 7, WaveID: waveID, CaseVersionID: 11301, RequirementID: 104,
				CaseTitle: "AC-FR-113-01 重复点赞不重复计数", Status: "blocked",
				FailureClass: "blocked", StepCount: 4, StepFailedCount: 1,
				AssertionCount: 3, AssertionFailedCount: 1, Evidence: reportBTrapEvidence(14),
			},
		},
		Issues: []delivery.IssueSnapshot{{
			ID: 3001, VersionID: 7, Title: "AC-FR-106-01 预约未来发布且预约中不公开 · step 1: ui failure",
			Status: delivery.IssueStatusOpen, Category: "ui", Severity: "minor", Source: "failure_candidate",
		}},
		Coverage: delivery.CoverageSnapshot{
			RequirementsTotal: 4, RequirementsCovered: 2,
			CaseVersionsTotal: 10, CaseVersionsCovered: 2,
			Gaps: []delivery.CoverageGapSnapshot{
				{RequirementID: 101, CaseVersionID: 1102, Reason: "execution_not_run"},
				{RequirementID: 101, CaseVersionID: 1103, Reason: "execution_not_run"},
				{RequirementID: 102, CaseVersionID: 1402, Reason: "execution_not_run"},
				{RequirementID: 102, CaseVersionID: 1403, Reason: "execution_not_run"},
				{RequirementID: 103, CaseVersionID: 10601, Reason: "execution_failed_coverage"},
				{RequirementID: 103, CaseVersionID: 10602, Reason: "execution_not_run"},
				{RequirementID: 104, CaseVersionID: 11301, Reason: "execution_failed_coverage"},
				{RequirementID: 104, CaseVersionID: 11302, Reason: "execution_not_run"},
			},
			ByRequirement: []delivery.RequirementCoverageSnapshot{
				{RequirementID: 101, CaseVersionsTotal: 3, CaseVersionsCovered: 1, CaseVersionsNotRun: 2},
				{RequirementID: 102, CaseVersionsTotal: 3, CaseVersionsCovered: 1, CaseVersionsNotRun: 2},
				{RequirementID: 103, CaseVersionsTotal: 2, CaseVersionsNotRun: 1, CaseVersionsFailedCoverage: 1},
				{RequirementID: 104, CaseVersionsTotal: 2, CaseVersionsNotRun: 1, CaseVersionsFailedCoverage: 1},
			},
		},
	}
}

// seedReportBTrapReport seeds one draft wave report whose snapshot is the
// Report B deterministic input.
func seedReportBTrapReport(t *testing.T) (*reportFixture, delivery.Report) {
	t.Helper()
	fixture := newReportFixture(t)
	report, _ := fixture.seedWaveReport(7, 12)
	fixture.data.wave[reportDataWaveKey(7, 12)] = reportBTrapSnapshot()
	return fixture, report
}

func TestReportNarrativeMatchesDeterministicResults(t *testing.T) {
	ctx := context.Background()

	t.Run("contradictory claim about a covered requirement is rejected", func(t *testing.T) {
		snapshot := reportBTrapSnapshot()
		err := validateReportNarrative(snapshot, "- FR-011、FR-014 本轮没有任何用例版本获得有效执行结果。")
		if err == nil {
			t.Fatal("contradictory requirement claim was accepted")
		}
		if !strings.Contains(err.Error(), "FR-011") && !strings.Contains(err.Error(), "FR-014") {
			t.Fatalf("contradiction error does not name the requirement: %v", err)
		}
		// A requirement without a passing case may be described as not
		// effectively executed: the guard must not over-block truthful text.
		if err := validateReportNarrative(snapshot, "- FR-106 本轮没有任何用例版本获得有效执行结果。"); err != nil {
			t.Fatalf("truthful uncovered-requirement claim was rejected: %v", err)
		}
		if err := validateReportNarrative(snapshot, "- FR-011 已通过，不得表述为无有效执行。"); err != nil {
			t.Fatalf("negated phrase was treated as a claim: %v", err)
		}
		// An unscoped absolute claim is false while any case version passed.
		if err := validateReportNarrative(snapshot, "本轮没有任何用例版本获得有效执行。"); err == nil {
			t.Fatal("absolute no-execution claim was accepted although coverage covered > 0")
		}
	})

	t.Run("coverage counts come from the deterministic snapshot", func(t *testing.T) {
		snapshot := reportBTrapSnapshot()
		for _, contradictory := range []string{
			reportBContradictoryNarrative,
			"- 用例版本覆盖：2 / 11；需求覆盖：2 / 4。",
			"- 需求覆盖：1 / 4。",
			"- 未覆盖 7 个用例版本（execution_not_run）。",
			"- 未有效覆盖 1 个用例版本（execution_failed_coverage）。",
		} {
			err := validateReportNarrative(snapshot, contradictory)
			if err == nil {
				t.Fatalf("contradictory coverage narrative was accepted: %s", contradictory)
			}
			if !strings.Contains(err.Error(), "确定性") && !strings.Contains(err.Error(), "execution") {
				t.Fatalf("coverage contradiction does not quote the deterministic facts: %v", err)
			}
		}
		if err := validateReportNarrative(snapshot, reportBConsistentNarrative); err != nil {
			t.Fatalf("consistent coverage narrative was rejected: %v", err)
		}
	})

	t.Run("per-requirement coverage resolves FR codes from requirement titles", func(t *testing.T) {
		snapshot := reportBTrapSnapshot()
		snapshot.Kind = delivery.ReportKindVersion
		snapshot.WaveID = nil
		// Drop the requirement code from the case title: only the requirement
		// title and the per-requirement coverage fact can prove the claim wrong.
		for index := range snapshot.CaseRuns {
			if snapshot.CaseRuns[index].CaseVersionID == 1101 {
				snapshot.CaseRuns[index].CaseTitle = "登录成功（正向）"
			}
		}
		if err := validateReportNarrative(snapshot, "- FR-011 本轮没有任何用例版本获得有效执行结果。"); err == nil {
			t.Fatal("per-requirement coverage did not contradict the no-execution claim")
		}

		// The current coverage is authoritative over a historical passed run: a
		// failed latest attempt leaves the requirement uncovered, so the claim is
		// not a contradiction of the deterministic coverage.
		for index := range snapshot.Coverage.ByRequirement {
			if snapshot.Coverage.ByRequirement[index].RequirementID == 101 {
				snapshot.Coverage.ByRequirement[index].CaseVersionsCovered = 0
				snapshot.Coverage.ByRequirement[index].CaseVersionsFailedCoverage = 1
				snapshot.Coverage.ByRequirement[index].CaseVersionsNotRun = 2
			}
		}
		if err := validateReportNarrative(snapshot, "- FR-011 本轮没有任何用例版本获得有效执行结果。"); err != nil {
			t.Fatalf("current per-requirement coverage lost to a historical passed run: %v", err)
		}
	})

	t.Run("propose tool rejects contradictory narrative without staging", func(t *testing.T) {
		fixture, report := seedReportBTrapReport(t)
		propose := reportTool(t, ReportTools(fixture.svc), agenttools.ToolReportNarrativePropose)
		raw, err := json.Marshal(map[string]any{"summary_markdown": reportBContradictoryNarrative})
		if err != nil {
			t.Fatalf("marshal narrative: %v", err)
		}
		_, err = propose.Execute(ctx, agenttools.Scope{
			VersionID: 7, ReportID: report.ID, ReportRevision: 1, WaveID: 12,
		}, raw)
		toolErr := requireReportToolError(t, err, agenttools.CodeInvalidArgument)
		if !strings.Contains(toolErr.Message, "2 / 10") {
			t.Fatalf("rejection message does not quote the deterministic coverage: %s", toolErr.Message)
		}
		if _, ok := TakeReportNarrative(report.ID, 1); ok {
			t.Fatal("contradictory narrative was staged for publish")
		}
	})

	t.Run("publish falls back to deterministic text for a contradictory candidate", func(t *testing.T) {
		fixture, report := seedReportBTrapReport(t)
		stageReportNarrative(ReportNarrativeDraft{
			ReportID: report.ID, Revision: 1, SummaryMarkdown: reportBContradictoryNarrative,
		})
		if err := fixture.svc.FinalizeRevision(ctx, report.ID, 1); err != nil {
			t.Fatalf("FinalizeRevision: %v", err)
		}
		narrative := fixture.renderer.lastNarrative
		for _, want := range []string{
			"确定性", "用例版本覆盖：2 / 10", "需求覆盖：2 / 4",
			"AC-FR-011-01", "AC-FR-014-01", "passed",
			"execution_not_run 6", "execution_failed_coverage 2",
		} {
			if !strings.Contains(narrative, want) {
				t.Fatalf("deterministic fallback narrative is missing %q:\n%s", want, narrative)
			}
		}
		for _, forbidden := range []string{"2 / 11", "未覆盖 7 个", "没有任何用例版本获得有效执行"} {
			if strings.Contains(narrative, forbidden) {
				t.Fatalf("published narrative still contains the contradiction %q:\n%s", forbidden, narrative)
			}
		}
		// The fallback must itself be consistent: it never contradicts the
		// deterministic facts it quotes.
		if err := validateReportNarrative(reportBTrapSnapshot(), narrative); err != nil {
			t.Fatalf("deterministic fallback contradicts the snapshot: %v", err)
		}
		if revision := fixture.repoRevision(t, report.ID, 1); revision.Status != delivery.ReportStatusPublished {
			t.Fatalf("revision after fallback = %+v, want published", revision)
		}
	})

	t.Run("consistent candidate is published verbatim", func(t *testing.T) {
		fixture, report := seedReportBTrapReport(t)
		stageReportNarrative(ReportNarrativeDraft{
			ReportID: report.ID, Revision: 1, SummaryMarkdown: reportBConsistentNarrative,
		})
		if err := fixture.svc.FinalizeRevision(ctx, report.ID, 1); err != nil {
			t.Fatalf("FinalizeRevision: %v", err)
		}
		if got := fixture.renderer.lastNarrative; got != reportBConsistentNarrative {
			t.Fatalf("consistent narrative was rewritten:\n%s", got)
		}
	})
}

// TestReportDataReadExposesDeterministicResults pins the report writer's
// projection: the model must be able to quote per-case step/assertion/evidence
// counts and the exact coverage facts instead of inventing them.
func TestReportDataReadExposesDeterministicResults(t *testing.T) {
	fixture, report := seedReportBTrapReport(t)
	read := reportTool(t, ReportTools(fixture.svc), agenttools.ToolReportDataRead)
	out, err := read.Execute(context.Background(), agenttools.Scope{
		VersionID: 7, ReportID: report.ID, ReportRevision: 1, WaveID: 12,
	}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("report.data_read: %v", err)
	}
	payload := out.(map[string]any)

	caseResults, ok := payload["case_results"].([]map[string]any)
	if !ok || len(caseResults) != 4 {
		t.Fatalf("case_results = %#v, want four deterministic run rows", payload["case_results"])
	}
	byCase := map[string]map[string]any{}
	for _, row := range caseResults {
		byCase[row["case_version_id"].(string)] = row
	}
	passed, ok := byCase["1101"]
	if !ok {
		t.Fatalf("case_results is missing case version 1101: %#v", caseResults)
	}
	if passed["status"] != "passed" || passed["step_count"] != 6 || passed["assertion_count"] != 6 ||
		passed["evidence_count"] != 13 || passed["requirement_id"] != "101" {
		t.Fatalf("passed case result = %#v, want 6 steps / 6 assertions / 13 evidence", passed)
	}
	if blocked := byCase["11301"]; blocked["status"] != "blocked" {
		t.Fatalf("blocked case result = %#v", blocked)
	}

	coverage := payload["coverage"].(map[string]any)
	if coverage["case_versions_covered"] != 2 || coverage["case_versions_total"] != 10 ||
		coverage["requirements_covered"] != 2 || coverage["requirements_total"] != 4 {
		t.Fatalf("coverage = %#v", coverage)
	}
	gapsByReason := coverage["gaps_by_reason"].(map[string]int)
	if gapsByReason["execution_not_run"] != 6 || gapsByReason["execution_failed_coverage"] != 2 {
		t.Fatalf("gaps_by_reason = %#v", gapsByReason)
	}
	byRequirement, ok := coverage["by_requirement"].([]map[string]any)
	if !ok || len(byRequirement) != 4 {
		t.Fatalf("coverage by_requirement = %#v", coverage["by_requirement"])
	}
	coveredFR011 := false
	for _, row := range byRequirement {
		if row["requirement_id"] == "101" {
			coveredFR011 = row["case_versions_covered"] == 1 && row["case_versions_total"] == 3 && row["case_versions_not_run"] == 2
		}
	}
	if !coveredFR011 {
		t.Fatalf("coverage by_requirement does not show FR-011 covered 1/3: %#v", byRequirement)
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal report.data_read: %v", err)
	}
	for _, leaked := range []string{"AC-FR-011-01", "登录成功", "预约未来发布"} {
		if strings.Contains(string(encoded), leaked) {
			t.Fatalf("report.data_read leaked the case title %q: %s", leaked, encoded)
		}
	}
}
