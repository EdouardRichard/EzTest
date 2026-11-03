package executionapp

// Task 110 (P8-T16) reproduction: the PO trap E2E retry stored FR-106-02
// (AC "到点自动发布") as status=blocked / failure_class=blocked with no failed
// assertion: step 1 observed that the editor has no publish-time / scheduled
// publish control (and recorded the explanatory error), every later step was
// blocked because the control does not exist, and 16 evidence rows were kept.
// The wave analysis fallback called it blocked, so no failed(application)
// candidate and no issue reached the report.
//
// The deterministic fallback must treat that recorded missing-control
// observation as application evidence while leaving a genuinely blocked run
// (budget stop, tool error, missing precondition/consequence observations)
// blocked.

import (
	"context"
	"testing"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

const (
	fr10602CaseTitle = "AC-FR-106-02 到点自动发布"
	fr10602LastError = "编辑页无「发布时间/预约发布」入口（状态仅草稿/已发布/已下架），无法设置未来时间 T，期望不可达"
	fr10602Observed  = `author(openClaw) 登录后打开 http://localhost:5173/editor；文章设置面板字段为 标题/Slug/分类/摘要/封面图/标签/首页精选/状态(草稿、已发布、已下架)，操作按钮为 保存文章设置/保存草稿/发布文章；页面无发布时间或定时发布控件（text="预约" 未找到、input[type=datetime-local] 未找到）；前端表单模型无 publishedAt/scheduledAt，后端忽略 publishedAt。因此无法设置 T，也无「预约发布」状态与预约时间展示。`
)

// fr10602BlockedConsequences are the real observations of steps 2-5: they
// describe the missing article/precondition that follows from the absent
// control, never the control itself. They must not be read as new evidence.
var fr10602BlockedConsequences = []string{
	"step1 失败导致不存在处于「预约发布」且预约时间为 T 的文章。GET /api/v1/admin/posts?status=scheduled 返回空数组；作者名下仅 1 篇文章「openClaw 自动生成文章6」status=published、publishedAt=2026-06-30T17:05:39+08:00，并非本用例创建的预约文章，不能用于验证状态流转。无法记录 T 到达后的 published 时间戳。",
	"无本用例创建的预约文章，无法在 GET /admin/posts 中核对其首次发布时间与 T 是否一致。",
	"因不存在于 T 自动发布的文章，无法以访客身份访问对应的 /posts/{id} 验证标题与正文展示。",
	"无本用例创建的预约文章，无法在再等一个检查周期后核对 GET /admin/posts 与 GET /api/v1/posts/{id} 的唯一性与 publishedAt 稳定性。",
}

// addFR10602Run stores the trap E2E end state. When blockedFirst is false the
// executor recorded step 1 as failed with the missing-control observation and
// its explanatory error (the exact stored evidence); when true the same
// observation sits on a blocked step (the "cannot even start" shape).
func addFR10602Run(f *analysisFixture, caseRunID, caseVersionID int64, blockedFirst bool) {
	f.addCase(caseVersionID, 900, 901, "P2")
	f.cases.cases[caseVersionID] = execution.CaseSnapshot{
		CaseID: caseVersionID + 1000, CaseVersionID: caseVersionID,
		RequirementID: 900, RequirementVersionID: 901,
		Priority: "P2", Title: fr10602CaseTitle,
	}
	run := f.addRun(caseRunID, 2, caseVersionID, execution.CaseRunBlocked, execution.FailureClassBlocked)
	run.Summary = execution.CaseRunSummary{EvidenceCount: 16, LastError: fr10602LastError}
	f.runs.byID[caseRunID] = run

	steps := []execution.StepResult{
		{CaseRunID: caseRunID, Seq: 1, Status: execution.StepFailed, Observed: map[string]any{"summary": fr10602Observed}, Error: fr10602LastError},
	}
	if blockedFirst {
		steps[0].Status = execution.StepBlocked
		steps[0].Error = ""
	}
	for index, consequence := range fr10602BlockedConsequences {
		steps = append(steps, execution.StepResult{
			CaseRunID: caseRunID, Seq: index + 2, Status: execution.StepBlocked,
			Observed: map[string]any{"summary": consequence},
		})
	}
	f.results.steps[caseRunID] = steps
	f.results.assertions[caseRunID] = []execution.AssertionResult{
		{
			CaseRunID: caseRunID, StepSeq: 2, AssertionSeq: 1, Kind: execution.AssertionKindAPI,
			Verdict: execution.AssertionSkipped, Expected: "该文章 status 为已发布（published），记录 T 到达后变为已发布的实际时间戳",
			Actual: "未执行：step1 无法设置预约时间 T，系统中不存在「预约发布」状态且预约时间为 T 的文章",
			Reason: "前置条件不成立（编辑页无预约发布入口，后端未存储预约时间），断言无观测对象，未执行",
		},
		{
			CaseRunID: caseRunID, StepSeq: 3, AssertionSeq: 1, Kind: execution.AssertionKindAPI,
			Verdict: execution.AssertionSkipped, Expected: "published_at == T（与设置的预约时间完全一致）",
			Actual: "未执行：无预约文章的 publishedAt 可核对", Reason: "无预约文章记录，无法比对发布时间与 T",
		},
		{
			CaseRunID: caseRunID, StepSeq: 4, AssertionSeq: 1, Kind: execution.AssertionKindUI,
			Verdict: execution.AssertionSkipped, Expected: "页面展示文章标题与正文，无「内容不存在/尚未公开」提示",
			Actual: "未执行：不存在于 T 自动发布的文章", Reason: "无本用例产生的文章 ID，UI 断言无法执行",
		},
		{
			CaseRunID: caseRunID, StepSeq: 5, AssertionSeq: 1, Kind: execution.AssertionKindAPI,
			Verdict: execution.AssertionSkipped, Expected: "返回 200 且 published_at 仍为 T，仅返回一条该文章记录",
			Actual: "未执行：无本用例创建的已发布文章", Reason: "依赖 step2/step3 的文章，前置缺失导致无法执行",
		},
	}
}

// TestMissingControlBlockedStepIsApplication is the FR-106-02 trap regression:
// a blocked run whose recorded step observation states that the required
// publish-time/schedule control is missing is failed(application) with exactly
// one issue candidate, even when no assertion failed. It covers both the stored
// failed-step-with-error shape and the blocked-step observation shape.
func TestMissingControlBlockedStepIsApplication(t *testing.T) {
	for _, blockedFirst := range []bool{false, true} {
		name := "failed step observation with error"
		if blockedFirst {
			name = "blocked step observation"
		}
		t.Run(name, func(t *testing.T) {
			f := newAnalysisFixture()
			ctx := context.Background()
			const caseRunID int64 = 5001
			addFR10602Run(f, caseRunID, 901, blockedFirst)

			// The fallback must be idempotent: a second analysis neither
			// rewrites the verdict back nor duplicates the issue candidate.
			for attempt := 0; attempt < 2; attempt++ {
				if err := f.analysis.AnalyzeWave(ctx, f.wave.ID); err != nil {
					t.Fatalf("AnalyzeWave %d: %v", attempt, err)
				}
			}
			run, err := f.runs.Get(ctx, caseRunID)
			if err != nil {
				t.Fatalf("get analyzed run: %v", err)
			}
			if run.Status != execution.CaseRunFailed || run.FailureClass != execution.FailureClassApplication {
				t.Fatalf("run = %s/%q, want failed/application for the observed missing control (last_error %q)",
					run.Status, run.FailureClass, run.Summary.LastError)
			}
			if run.Summary.VerdictReason != execution.VerdictReasonStepFailed {
				t.Fatalf("verdict reason = %q, want %q", run.Summary.VerdictReason, execution.VerdictReasonStepFailed)
			}
			if run.Summary.AssertionFailed != 0 {
				t.Fatalf("assertion_failed = %d, want 0 (the trap has no failed assertion)", run.Summary.AssertionFailed)
			}
			if run.Summary.EvidenceCount != 16 {
				t.Fatalf("evidence count = %d, want the observed 16 evidence rows", run.Summary.EvidenceCount)
			}
			if run.Summary.LastError != fr10602LastError {
				t.Fatalf("last_error = %q, want the observed missing-entry explanation", run.Summary.LastError)
			}
			if !blockedFirst && run.Summary.StepFailed != 1 {
				t.Fatalf("step_failed = %d, want 1 for the failed step that observed the gap", run.Summary.StepFailed)
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
			if payload["severity"] != "minor" {
				t.Fatalf("severity = %v, want minor for a P2 case", payload["severity"])
			}
			if hint, _ := payload["dedupe_hint"].(string); len(hint) != 32 {
				t.Fatalf("dedupe_hint = %q, want a 32-character hint", hint)
			}
			if f.team.count() != 1 {
				t.Fatalf("team finding posts = %d, want 1 after repeated analysis", f.team.count())
			}
		})
	}
}

// TestAnalyzeWaveRefreshesBlockedWaveCounts keeps the wave report headline
// consistent with the reclassified run: completeWave snapshots the counts before
// analysis, so the FR-106-02 run would otherwise keep counting as blocked in
// the report while its case_run row says failed(application).
func TestAnalyzeWaveRefreshesBlockedWaveCounts(t *testing.T) {
	f := newAnalysisFixture()
	ctx := context.Background()
	f.waves.runsStore = f.runs
	const caseRunID int64 = 5201
	addFR10602Run(f, caseRunID, 906, false)

	if err := f.analysis.AnalyzeWave(ctx, f.wave.ID); err != nil {
		t.Fatalf("AnalyzeWave: %v", err)
	}
	wave, err := f.waves.Get(ctx, f.wave.ID)
	if err != nil {
		t.Fatalf("get wave: %v", err)
	}
	if wave.Counts.Failed != 1 || wave.Counts.Blocked != 0 {
		t.Fatalf("wave counts = failed %d / blocked %d, want failed 1 / blocked 0 after reclassification",
			wave.Counts.Failed, wave.Counts.Blocked)
	}
}

// TestPureBlockedStaysBlocked is the no-gate-widening guard: a budget stop, a
// run whose only failure is a tool/automation error, and a run that merely
// records the downstream consequences of a missing feature carry no
// missing-control observation and must stay blocked with no issue candidate.
func TestPureBlockedStaysBlocked(t *testing.T) {
	f := newAnalysisFixture()
	ctx := context.Background()
	const (
		budgetRunID      int64 = 5101
		automationRunID  int64 = 5102
		consequenceRunID int64 = 5103
	)
	f.addCase(902, 900, 901, "P0")
	f.addCase(903, 900, 901, "P0")
	f.addCase(904, 900, 901, "P2")

	f.addRun(budgetRunID, 3, 902, execution.CaseRunBlocked, execution.FailureClassBlocked)
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
	f.addRun(automationRunID, 4, 903, execution.CaseRunBlocked, execution.FailureClassBlocked)
	f.results.steps[automationRunID] = []execution.StepResult{
		{CaseRunID: automationRunID, Seq: 1, Status: execution.StepFailed, Error: "target_not_found: selector #scheduled-at"},
		{
			CaseRunID: automationRunID, Seq: 2, Status: execution.StepBlocked,
			Observed: map[string]any{"summary": "登录前置不可用，剩余步骤未执行。"},
		},
	}

	// The same downstream observations as FR-106-02 without the missing-control
	// observation itself: blocked consequences of a tool failure stay blocked.
	f.addRun(consequenceRunID, 5, 904, execution.CaseRunBlocked, execution.FailureClassBlocked)
	consequenceSteps := []execution.StepResult{
		{CaseRunID: consequenceRunID, Seq: 1, Status: execution.StepFailed, Error: "target_not_found: selector #scheduled-at"},
	}
	for index, consequence := range fr10602BlockedConsequences {
		consequenceSteps = append(consequenceSteps, execution.StepResult{
			CaseRunID: consequenceRunID, Seq: index + 2, Status: execution.StepBlocked,
			Observed: map[string]any{"summary": consequence},
		})
	}
	f.results.steps[consequenceRunID] = consequenceSteps

	if err := f.analysis.AnalyzeWave(ctx, f.wave.ID); err != nil {
		t.Fatalf("AnalyzeWave: %v", err)
	}
	for _, caseRunID := range []int64{budgetRunID, automationRunID, consequenceRunID} {
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
		t.Fatalf("failure_candidate events = %d, want 0 without missing-control evidence", len(events))
	}
	if f.team.count() != 0 {
		t.Fatalf("team finding posts = %d, want 0 without missing-control evidence", f.team.count())
	}
}
