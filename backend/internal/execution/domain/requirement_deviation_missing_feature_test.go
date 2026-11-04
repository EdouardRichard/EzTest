package execution

// Task 110 (P8-T16): the FR-106-02 trap. The executor recorded step 1 as failed
// with the observed absence of the publish-time/schedule control *and* an
// explanatory error at the same time, then blocked the remaining steps. The QA
// Q1 discriminator rejected every failed step carrying an error, so the recorded
// missing-control observation never became failed(application). A blocked step
// can carry the same observation when the executor could not start the action.
// Pure budget/tool/precondition stoppages must not be upgraded.

import "testing"

func TestStepObservesMissingFeature(t *testing.T) {
	cases := []struct {
		name string
		step StepResult
		want bool
	}{
		{
			name: "failed step with missing publish-time control and an error note",
			step: StepResult{
				CaseRunID: 1, Seq: 1, Status: StepFailed,
				Observed: map[string]any{
					"summary": `author(openClaw) 登录后打开 http://localhost:5173/editor；文章设置面板字段为 标题/Slug/分类/摘要/封面图/标签/首页精选/状态(草稿、已发布、已下架)，操作按钮为 保存文章设置/保存草稿/发布文章；页面无发布时间或定时发布控件（text="预约" 未找到、input[type=datetime-local] 未找到）；前端表单模型无 publishedAt/scheduledAt，后端忽略 publishedAt。因此无法设置 T，也无「预约发布」状态与预约时间展示。`,
				},
				Error: "编辑页无「发布时间/预约发布」入口（状态仅草稿/已发布/已下架），无法设置未来时间 T，期望不可达",
			},
			want: true,
		},
		{
			name: "blocked step observes the missing schedule control",
			step: StepResult{
				CaseRunID: 1, Seq: 1, Status: StepBlocked,
				Observed: map[string]any{"summary": "编辑页没有发布时间/预约发布控件，无法设置未来时间 T。"},
			},
			want: true,
		},
		{
			name: "blocked step observes the missing publish calendar entry",
			step: StepResult{
				CaseRunID: 1, Seq: 1, Status: StepBlocked,
				Observed: map[string]any{"summary": "管理后台不存在发布日历入口，无法查看未来 30 天排期。"},
			},
			want: true,
		},
		{
			name: "blocked step observes the scheduled control as absent",
			step: StepResult{
				CaseRunID: 1, Seq: 1, Status: StepBlocked,
				Observed: map[string]any{"summary": "预约发布控件不存在，后续到点自动发布无法验证。"},
			},
			want: true,
		},
		{
			name: "missing like control, fields and endpoint",
			step: StepResult{
				CaseRunID: 1, Seq: 1, Status: StepFailed,
				Observed: map[string]any{"summary": "详情页不存在任何点赞控件或点赞计数展示；GET /api/v1/posts/{id} 无 like_count/likes 字段；POST /api/v1/posts/{id}/like 返回 404 page not found。"},
			},
			want: true,
		},
		{
			name: "budget stop observation is not product evidence",
			step: StepResult{
				CaseRunID: 1, Seq: 1, Status: StepBlocked,
				Observed: map[string]any{"summary": "执行在该步中断（token 预算耗尽），未提交表单也未抓取网络响应。"},
			},
			want: false,
		},
		{
			name: "missing login precondition is not product evidence",
			step: StepResult{
				CaseRunID: 1, Seq: 1, Status: StepBlocked,
				Observed: map[string]any{"summary": "登录前置不可用，剩余步骤未执行。"},
			},
			want: false,
		},
		{
			name: "blocked consequence of the missing control is not product evidence",
			step: StepResult{
				CaseRunID: 1, Seq: 1, Status: StepBlocked,
				Observed: map[string]any{"summary": "step1 失败导致不存在处于「预约发布」且预约时间为 T 的文章。GET /api/v1/admin/posts?status=scheduled 返回空数组；作者名下仅 1 篇文章「openClaw 自动生成文章6」status=published、publishedAt=2026-06-30T17:05:39+08:00，并非本用例创建的预约文章，不能用于验证状态流转。无法记录 T 到达后的 published 时间戳。"},
			},
			want: false,
		},
		{
			name: "blocked empty-result observation is not product evidence",
			step: StepResult{
				CaseRunID: 1, Seq: 1, Status: StepBlocked,
				Observed: map[string]any{"summary": "无本用例创建的预约文章，无法在再等一个检查周期后核对 GET /admin/posts 与 GET /api/v1/posts/{id} 的唯一性与 publishedAt 稳定性。"},
			},
			want: false,
		},
		{
			name: "absence marker on an unrelated subject is not product evidence",
			step: StepResult{
				CaseRunID: 1, Seq: 1, Status: StepFailed,
				Observed: map[string]any{"summary": "将账号输入框改为不存在的账号 auth_ghost_01，提交后提示「登录已过期，请重新登录」。"},
			},
			want: false,
		},
		{
			name: "cannot-open observation is not a missing page",
			step: StepResult{
				CaseRunID: 1, Seq: 1, Status: StepBlocked,
				Observed: map[string]any{"summary": "无法打开后台页面：browser.goto 超时，网络连接被重置。"},
			},
			want: false,
		},
		{
			name: "no-error observation is not a missing feature",
			step: StepResult{
				CaseRunID: 1, Seq: 1, Status: StepBlocked,
				Observed: map[string]any{"summary": "页面无错误提示，控制台无异常，接口均返回 200。"},
			},
			want: false,
		},
		{
			name: "tool error without an observation is not product evidence",
			step: StepResult{CaseRunID: 1, Seq: 1, Status: StepFailed, Error: "target_not_found: selector #scheduled-at"},
			want: false,
		},
		{
			name: "tool error observation is not product evidence",
			step: StepResult{
				CaseRunID: 1, Seq: 1, Status: StepFailed,
				Observed: map[string]any{"summary": "element not found: selector #scheduled-at"},
				Error:    "target_not_found: selector #scheduled-at",
			},
			want: false,
		},
		{
			name: "passed step cannot be missing-feature evidence",
			step: StepResult{
				CaseRunID: 1, Seq: 1, Status: StepPassed,
				Observed: map[string]any{"summary": "页面无发布时间控件。"},
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StepObservesMissingFeature(tc.step); got != tc.want {
				t.Fatalf("StepObservesMissingFeature = %v, want %v (observed %q, error %q)",
					got, tc.want, StepObservedSummary(tc.step), tc.step.Error)
			}
		})
	}
}

// TestFailedStepMissingFeatureWithErrorIsApplication pins the FR-106-02 stored
// shape at the deterministic verdict: step 1 failed with the missing-control
// observation and an explanatory (non-tool) error. It must be an application
// failure, not automation.
func TestFailedStepMissingFeatureWithErrorIsApplication(t *testing.T) {
	in := VerdictInput{
		Case: caseSnapshotForVerdict(1),
		Steps: []StepResult{{
			CaseRunID: 1, Seq: 1, Status: StepFailed,
			Observed: map[string]any{"summary": "页面无发布时间或定时发布控件，无法设置未来时间 T。"},
			Error:    "编辑页无「发布时间/预约发布」入口（状态仅草稿、已发布、已下架），无法设置未来时间 T，期望不可达",
		}},
	}
	status, class, summary, err := Aggregate(in)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if status != CaseRunFailed || class != FailureClassApplication {
		t.Fatalf("verdict = %s/%q, want failed/application", status, class)
	}
	if summary.VerdictReason != VerdictReasonStepFailed {
		t.Fatalf("verdict reason = %q, want %q", summary.VerdictReason, VerdictReasonStepFailed)
	}
}

func TestStepShowsApplicationDeviation(t *testing.T) {
	blockedMissing := StepResult{
		CaseRunID: 1, Seq: 1, Status: StepBlocked,
		Observed: map[string]any{"summary": "编辑页没有「发布时间/预约发布」控件。"},
	}
	if !StepShowsApplicationDeviation(blockedMissing) {
		t.Fatal("a blocked step observing a missing control must be application deviation evidence")
	}
	budget := StepResult{
		CaseRunID: 1, Seq: 1, Status: StepBlocked,
		Observed: map[string]any{"summary": "执行在该步中断（token 预算耗尽），未提交表单也未抓取网络响应。"},
	}
	if StepShowsApplicationDeviation(budget) {
		t.Fatal("a budget stop must not be application deviation evidence")
	}
	failedTool := StepResult{CaseRunID: 1, Seq: 1, Status: StepFailed, Error: "target_not_found: selector #submit"}
	if StepShowsApplicationDeviation(failedTool) {
		t.Fatal("a tool error without an observation must not be application deviation evidence")
	}
}
