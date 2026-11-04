package designapp

import (
	"context"
	"testing"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
)

// storedAssertionKinds returns the persisted assertion kinds of the current
// case version, in submission order.
func storedAssertionKinds(t *testing.T, repo *fakeRepo, caseID int64) []string {
	t.Helper()
	item, ok := repo.cases[caseID]
	if !ok || item.current == nil {
		t.Fatalf("case %d was not stored", caseID)
	}
	kinds := make([]string, 0, len(item.current.Assertions))
	for _, assertion := range item.current.Assertions {
		kinds = append(kinds, assertion.Kind)
	}
	return kinds
}

func assertNoVisualAssertion(t *testing.T, kinds []string) {
	t.Helper()
	for index, kind := range kinds {
		if kind == design.AssertionVisual {
			t.Fatalf("assertion %d kept kind=visual: %v", index, kinds)
		}
	}
}

// TestDesignDefaultsNonVisualAssertions pins Task 120: a normal functional
// acceptance criterion must generate text/ui/api/db assertions. The model's
// misplaced kind=visual is defaulted to a browser assertion while its concrete
// target and expected stay intact, so FR-025-style regression designs do not
// auto-create a visual assertion that has no visual_assertion configuration.
func TestDesignDefaultsNonVisualAssertions(t *testing.T) {
	service, repo, _, _ := newGraphFixture(t)
	repo.requirement.Title = "订单管理"
	repo.requirement.Description = "提交订单后订单出现在订单列表中"
	repo.requirement.AcceptanceCriteria = "提交合法订单后，订单出现在列表顶部且状态为待处理"

	in := validProposeInput()
	in.Cases[0].Title = "提交订单后出现在列表"
	in.Cases[0].Assertions = []AssertionInput{
		{StepSeq: 1, Kind: "ui", Target: "订单表单", Expected: "表单显示必填字段"},
		{StepSeq: 2, Kind: "visual", Target: "订单列表", Expected: "列表顶部出现新订单且状态为待处理"},
	}

	result, err := service.Propose(context.Background(), 1001, in, 1, 1)
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if len(result.CaseIDs) != 1 {
		t.Fatalf("case ids = %v", result.CaseIDs)
	}
	kinds := storedAssertionKinds(t, repo, result.CaseIDs[0])
	assertNoVisualAssertion(t, kinds)
	if len(kinds) != 2 {
		t.Fatalf("assertion kinds = %v, want both assertions preserved", kinds)
	}
	if kinds[1] != design.AssertionUI {
		t.Fatalf("defaulted kind = %q, want %q", kinds[1], design.AssertionUI)
	}
	assertion := repo.cases[result.CaseIDs[0]].current.Assertions[1]
	if assertion.Target != "订单列表" || assertion.Expected != "列表顶部出现新订单且状态为待处理" {
		t.Fatalf("defaulting weakened the assertion: %#v", assertion)
	}
}

// TestExplicitVisualRequestStillVisual pins the other half of Task 120: an
// acceptance criterion that explicitly requires a visual baseline comparison
// still produces kind=visual. The fallback must never disable the P5 visual
// capability when the requirement really asks for it.
func TestExplicitVisualRequestStillVisual(t *testing.T) {
	service, repo, _, _ := newGraphFixture(t)
	repo.requirement.Title = "首页展示"
	repo.requirement.AcceptanceCriteria = "首页首屏需与已批准的视觉基线做像素级比对，差异超过阈值即失败"

	in := validProposeInput()
	in.Cases[0].Title = "首页首屏视觉基线回归"
	in.Cases[0].Assertions = []AssertionInput{
		{StepSeq: 1, Kind: "ui", Target: "首屏标题", Expected: "标题可见"},
		{StepSeq: 2, Kind: "visual", Target: "首页首屏", Expected: "首屏与已批准基线一致（差异比例不超过阈值）"},
	}

	result, err := service.Propose(context.Background(), 1001, in, 1, 1)
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	kinds := storedAssertionKinds(t, repo, result.CaseIDs[0])
	if len(kinds) != 2 || kinds[1] != design.AssertionVisual {
		t.Fatalf("explicit visual request kinds = %v, want the visual assertion kept", kinds)
	}
}

// TestFR025NoVisualByDefault uses the real FR-025 acceptance text from the
// retry11 fresh design: "the published reading page does not show the
// preview/draft marker". That is a text/ui claim; the fresh design must not
// turn it into a visual assertion because S3/S4 do not configure a visual
// assertion and Task 115 would reject the confirmation with
// 409 visual_assertion_unresolved. The demoted assertion must stay concrete.
func TestFR025NoVisualByDefault(t *testing.T) {
	service, repo, _, _ := newGraphFixture(t)
	repo.requirement.Title = "保存草稿与发布"
	repo.requirement.Description = "作者保存草稿并发布文章，发布后进入公开阅读页。"
	repo.requirement.AcceptanceCriteria = "发布后阅读页 URL 为公开地址且页面不显示预览/草稿标识，访客可访问。"

	in := validProposeInput()
	in.Cases[0].Title = "AC-FR-025-01 发布文章（正向）"
	in.Cases[0].Description = "验证作者完成文章并发布后进入不带预览标识的公开阅读页，访客可直接访问。"
	in.Cases[0].Preconditions = "作者账号可正常登录；站点可通过 http://localhost:5173/ 访问。"
	in.Cases[0].Steps = []StepInput{
		{Seq: 1, Action: "以作者账号登录后进入编辑器，填写文章标题与正文", Expected: "编辑器内标题与正文已填入并可提交发布", TargetVersionSUTID: int64Ptr(9001)},
		{Seq: 2, Action: "点击「发布文章」按钮", Expected: "保存成功且页面跳转到该文章的公开阅读页", TargetVersionSUTID: int64Ptr(9001)},
		{Seq: 3, Action: "检查发布后阅读页的 URL 与页面标识", Expected: "URL 指向该文章且页面不出现预览/草稿标识", TargetVersionSUTID: int64Ptr(9001)},
	}
	in.Cases[0].Assertions = []AssertionInput{
		{StepSeq: 2, Kind: "api", Target: "POST /posts", Expected: "发布请求返回 2xx 且响应体含文章 id", TargetVersionSUTID: int64Ptr(9001)},
		{StepSeq: 3, Kind: "text", Target: "发布后阅读页 URL", Expected: "URL 为 http://localhost:5173/posts/{id} 且页面无预览标识", TargetVersionSUTID: int64Ptr(9001)},
		{StepSeq: 3, Kind: "visual", Target: "文章阅读页", Expected: "页面不显示「预览」/「草稿」等仅作者可见的标识", TargetVersionSUTID: int64Ptr(9001)},
		{StepSeq: 3, Kind: "api", Target: "GET /api/v1/posts/{id}", Expected: "未登录请求返回 200 且响应体包含该文章标题与正文", TargetVersionSUTID: int64Ptr(9001)},
	}

	result, err := service.Propose(context.Background(), 1001, in, 1, 1)
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	kinds := storedAssertionKinds(t, repo, result.CaseIDs[0])
	assertNoVisualAssertion(t, kinds)
	want := []string{design.AssertionAPI, design.AssertionText, design.AssertionUI, design.AssertionAPI}
	if len(kinds) != len(want) {
		t.Fatalf("assertion kinds = %v, want %v", kinds, want)
	}
	for index := range want {
		if kinds[index] != want[index] {
			t.Fatalf("assertion kinds = %v, want %v", kinds, want)
		}
	}
	demoted := repo.cases[result.CaseIDs[0]].current.Assertions[2]
	if demoted.Target != "文章阅读页" || demoted.Expected != "页面不显示「预览」/「草稿」等仅作者可见的标识" {
		t.Fatalf("FR-025 assertion lost its concrete criterion: %#v", demoted)
	}
}
