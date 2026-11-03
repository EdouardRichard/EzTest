package executionapp

// Task 107 (P8-T14) reproduction: the PO trap E2E ran FR-113-01 with a real
// missing-feature observation (no like control/count, no like fields in
// GET /posts/{id}, POST /posts/{id}/like 404, one failed ui assertion, 14
// evidence) but the wave analysis fallback rewrote the run to blocked and no
// issue candidate was emitted. The trap case FR-106 (scheduled publish /
// publish calendar) must be surfaced the same way when the observation records
// the missing feature. The deterministic fallback must never downgrade recorded
// application evidence to blocked; a blocked run with no such evidence stays
// blocked.

import (
	"context"
	"testing"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

const missingFeatureCaseTitle = "AC-FR-113-01 重复点赞不重复计数"

// missedLikeFeatureRun reproduces the stored end state of the PO trap E2E run:
// status=blocked / failure_class=blocked, step 3 failed with a missing-control
// observation, step 4 blocked because the control does not exist, one failed ui
// assertion and 14 evidence rows.
func (f *analysisFixture) addBlockedMissingFeatureRun(runID int64) execution.CaseRun {
	run := f.addRun(runID, 1, 901, execution.CaseRunBlocked, execution.FailureClassBlocked)
	run.Summary = execution.CaseRunSummary{EvidenceCount: 14}
	f.runs.byID[runID] = run
	f.results.steps[runID] = []execution.StepResult{
		{CaseRunID: runID, Seq: 1, Status: execution.StepPassed, Observed: map[string]any{"summary": "已登录并打开文章详情页"}},
		{CaseRunID: runID, Seq: 2, Status: execution.StepPassed, Observed: map[string]any{"summary": "文章已发布并可通过 /posts/{id} 访问"}},
		{
			CaseRunID: runID, Seq: 3, Status: execution.StepFailed,
			Observed: map[string]any{"summary": "详情页不存在任何点赞控件或点赞计数展示；GET /api/v1/posts/{id} 响应无 like_count/likes 字段；POST /api/v1/posts/{id}/like 返回 404 page not found。"},
		},
		{
			CaseRunID: runID, Seq: 4, Status: execution.StepBlocked,
			Observed: map[string]any{"summary": "详情页没有任何点赞按钮/点赞计数元素，点赞功能不存在，无法执行点击操作。"},
		},
	}
	f.results.assertions[runID] = []execution.AssertionResult{
		{CaseRunID: runID, StepSeq: 1, AssertionSeq: 1, Kind: execution.AssertionKindAPI, Verdict: execution.AssertionPassed},
		{
			CaseRunID: runID, StepSeq: 3, AssertionSeq: 1, Kind: execution.AssertionKindUI, Verdict: execution.AssertionFailed,
			Expected: "详情页展示点赞控件与点赞计数 N0=0", Actual: "详情页无点赞控件、无点赞计数展示",
			Reason: "页面缺失点赞控件与点赞计数，无法建立基线 N=0，按用例判定失败",
		},
		{CaseRunID: runID, StepSeq: 3, AssertionSeq: 2, Kind: execution.AssertionKindAPI, Verdict: execution.AssertionInconclusive},
	}
	return run
}

// TestMissingFeatureBlockedRunIsApplication is the FR-113 trap regression: a
// recorded failed ui assertion plus the observed missing control/404/fields must
// stay failed(application) even though the run (and a later step) is blocked.
func TestMissingFeatureBlockedRunIsApplication(t *testing.T) {
	f := newAnalysisFixture()
	ctx := context.Background()
	const caseRunID int64 = 5001
	f.addCase(901, 900, 901, "P2")
	f.cases.cases[901] = execution.CaseSnapshot{
		CaseID: 1901, CaseVersionID: 901, RequirementID: 900, RequirementVersionID: 901,
		Priority: "P2", Title: missingFeatureCaseTitle,
	}
	f.addBlockedMissingFeatureRun(caseRunID)

	if err := f.analysis.AnalyzeWave(ctx, f.wave.ID); err != nil {
		t.Fatalf("AnalyzeWave: %v", err)
	}
	run, err := f.runs.Get(ctx, caseRunID)
	if err != nil {
		t.Fatalf("get analyzed run: %v", err)
	}
	if run.Status != execution.CaseRunFailed || run.FailureClass != execution.FailureClassApplication {
		t.Fatalf("run = %s/%q, want failed/application (missing-feature evidence must not be dismissed as blocked)",
			run.Status, run.FailureClass)
	}
	if run.Summary.VerdictReason != execution.VerdictReasonAssertionFailed {
		t.Fatalf("verdict reason = %q, want %q", run.Summary.VerdictReason, execution.VerdictReasonAssertionFailed)
	}
	if run.Summary.StepFailed != 1 || run.Summary.AssertionFailed != 1 {
		t.Fatalf("summary = %+v, want 1 failed step / 1 failed assertion", run.Summary)
	}
	if run.Summary.EvidenceCount != 14 {
		t.Fatalf("evidence count = %d, want the observed 14 evidence rows", run.Summary.EvidenceCount)
	}
	if f.team.count() != 1 {
		t.Fatalf("team finding posts = %d, want 1", f.team.count())
	}
}

// TestMissingFeatureCreatesIssue asserts the issue-candidate handoff for an
// observed missing feature that has no failed assertion record: the FR-106 trap
// (scheduled publish / publish calendar absent) must emit exactly one
// failed(application) candidate with severity/category and keep the evidence.
func TestMissingFeatureCreatesIssue(t *testing.T) {
	f := newAnalysisFixture()
	ctx := context.Background()
	const caseRunID int64 = 5002
	f.addCase(902, 900, 901, "P1")
	f.cases.cases[902] = execution.CaseSnapshot{
		CaseID: 1902, CaseVersionID: 902, RequirementID: 900, RequirementVersionID: 901,
		Priority: "P1", Title: "AC-FR-106-01 预约未来发布且预约中不公开",
	}
	run := f.addRun(caseRunID, 2, 902, execution.CaseRunBlocked, execution.FailureClassBlocked)
	run.Summary = execution.CaseRunSummary{EvidenceCount: 10}
	f.runs.byID[caseRunID] = run
	f.results.steps[caseRunID] = []execution.StepResult{
		{CaseRunID: caseRunID, Seq: 1, Status: execution.StepPassed},
		{
			CaseRunID: caseRunID, Seq: 2, Status: execution.StepFailed,
			Observed: map[string]any{"summary": "编辑页保存/发布区域不存在「预约发布」时间设置控件，管理后台 /admin 也不存在发布日历入口；定时发布与发布日历功能不存在，无法执行预约操作。"},
		},
		{CaseRunID: caseRunID, Seq: 3, Status: execution.StepBlocked, Observed: map[string]any{"summary": "预约发布控件不存在，后续到点自动发布无法验证。"}},
	}

	for attempt := 0; attempt < 2; attempt++ {
		if err := f.analysis.AnalyzeWave(ctx, f.wave.ID); err != nil {
			t.Fatalf("AnalyzeWave %d: %v", attempt, err)
		}
	}
	stored, err := f.runs.Get(ctx, caseRunID)
	if err != nil {
		t.Fatalf("get analyzed run: %v", err)
	}
	if stored.Status != execution.CaseRunFailed || stored.FailureClass != execution.FailureClassApplication {
		t.Fatalf("run = %s/%q, want failed/application", stored.Status, stored.FailureClass)
	}
	if stored.Summary.VerdictReason != execution.VerdictReasonStepFailed {
		t.Fatalf("verdict reason = %q, want %q", stored.Summary.VerdictReason, execution.VerdictReasonStepFailed)
	}
	if stored.Summary.EvidenceCount != 10 {
		t.Fatalf("evidence count = %d, want 10", stored.Summary.EvidenceCount)
	}
	events := f.caseRunEvents(EventFailureCandidate)
	if len(events) != 1 {
		t.Fatalf("failure_candidate events = %d, want exactly 1 after repeated analysis", len(events))
	}
	payload := events[0].Payload
	if payload["failure_class"] != string(execution.FailureClassApplication) {
		t.Fatalf("failure_class = %v, want application", payload["failure_class"])
	}
	if payload["category"] != "ui" {
		t.Fatalf("category = %v, want ui for the observed missing UI control", payload["category"])
	}
	if payload["severity"] != "major" {
		t.Fatalf("severity = %v, want major for a P1 case", payload["severity"])
	}
	if hint, _ := payload["dedupe_hint"].(string); len(hint) != 32 {
		t.Fatalf("dedupe_hint = %q, want a 32-character hint", hint)
	}
	if f.team.count() != 1 {
		t.Fatalf("team finding posts = %d, want 1 after repeated analysis", f.team.count())
	}
}

// TestBlockedWithoutEvidenceStaysBlocked is the no-gate-widening guard: a run
// that was blocked before any product behavior could be observed (for example a
// budget stop), and a blocked run whose only failure is a tool/automation error,
// carry no application evidence and must stay blocked with no issue candidate.
func TestBlockedWithoutEvidenceStaysBlocked(t *testing.T) {
	f := newAnalysisFixture()
	ctx := context.Background()
	const budgetRunID int64 = 5003
	const automationRunID int64 = 5004
	f.addCase(903, 900, 901, "P0")
	f.addCase(904, 900, 901, "P0")
	f.addRun(budgetRunID, 3, 903, execution.CaseRunBlocked, execution.FailureClassBlocked)
	f.results.steps[budgetRunID] = []execution.StepResult{
		{CaseRunID: budgetRunID, Seq: 1, Status: execution.StepPassed},
		{
			CaseRunID: budgetRunID, Seq: 2, Status: execution.StepBlocked,
			Observed: map[string]any{"summary": "执行在该步中断（token 预算耗尽），未提交表单也未抓取网络响应。"},
		},
	}
	f.results.assertions[budgetRunID] = []execution.AssertionResult{
		{CaseRunID: budgetRunID, StepSeq: 1, AssertionSeq: 1, Kind: execution.AssertionKindUI, Verdict: execution.AssertionPassed},
	}
	// A tool error is not product evidence: the blocked run must not be
	// upgraded to an application failure by the analysis fallback.
	f.addRun(automationRunID, 4, 904, execution.CaseRunBlocked, execution.FailureClassBlocked)
	f.results.steps[automationRunID] = []execution.StepResult{
		{CaseRunID: automationRunID, Seq: 1, Status: execution.StepFailed, Error: "target_not_found: selector #like-button"},
		{CaseRunID: automationRunID, Seq: 2, Status: execution.StepBlocked, Observed: map[string]any{"summary": "登录前置不可用，剩余步骤未执行。"}},
	}

	if err := f.analysis.AnalyzeWave(ctx, f.wave.ID); err != nil {
		t.Fatalf("AnalyzeWave: %v", err)
	}
	for _, caseRunID := range []int64{budgetRunID, automationRunID} {
		run, err := f.runs.Get(ctx, caseRunID)
		if err != nil {
			t.Fatalf("get analyzed run %d: %v", caseRunID, err)
		}
		if run.Status != execution.CaseRunBlocked || run.FailureClass != execution.FailureClassBlocked {
			t.Fatalf("run %d = %s/%q, want blocked/blocked", caseRunID, run.Status, run.FailureClass)
		}
		if run.Summary.VerdictReason != execution.VerdictReasonBlocked {
			t.Fatalf("run %d verdict reason = %q, want %q", caseRunID, run.Summary.VerdictReason, execution.VerdictReasonBlocked)
		}
	}
	if events := f.caseRunEvents(EventFailureCandidate); len(events) != 0 {
		t.Fatalf("failure_candidate events = %d, want 0 without application evidence", len(events))
	}
	if f.team.count() != 0 {
		t.Fatalf("team finding posts = %d, want 0 without application evidence", f.team.count())
	}
}
