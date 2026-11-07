package agentcontext_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
)

// fakeBriefingReader is the in-memory read port used by the briefing tests. It
// proves the builder only depends on the narrow port and needs no database.
type fakeBriefingReader struct {
	source agentcontext.Source
	err    error
	calls  int
	lastID int64
}

func (f *fakeBriefingReader) Load(_ context.Context, versionID int64) (agentcontext.Source, error) {
	f.calls++
	f.lastID = versionID
	if f.err != nil {
		return agentcontext.Source{}, f.err
	}
	return f.source, nil
}

func floatPtr(value float64) *float64 { return &value }

func richSource() agentcontext.Source {
	base := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	return agentcontext.Source{
		Version: agentcontext.VersionSnapshot{Goal: "验证优惠券结算", Stage: "executing"},
		Requirements: agentcontext.RequirementsSnapshot{
			Confirmed: []agentcontext.RequirementSnapshot{
				{Title: "优惠券抵扣订单总额", Acceptance: "总额按折扣减少"},
				{Title: "过期券不可使用", Acceptance: "提交时提示过期"},
			},
			Gaps: []string{"缺少过期券的边界路径"},
		},
		Cases: agentcontext.CasesSnapshot{
			Draft:        2,
			Confirmed:    3,
			Stale:        1,
			CoverageGaps: []string{"缺少并发下单场景"},
		},
		Plan: []agentcontext.PlanItemSnapshot{
			{ID: 1, Title: "生成用例草稿", Status: "pending", DependsOn: []string{"确认需求"}},
			{ID: 2, Title: "评审用例草稿", Status: "in_progress", DependsOn: []string{"生成用例草稿"}},
		},
		Findings: []agentcontext.FactSnapshot{
			{ID: 3, Kind: "page", Key: "login", Summary: "登录页可达", Confidence: floatPtr(0.9), CreatedAt: base.Add(2 * time.Minute)},
			{ID: 1, Kind: "api", Key: "auth", Summary: "鉴权接口可用", Confidence: floatPtr(0.7), CreatedAt: base.Add(time.Minute)},
			{ID: 2, Kind: "risk", Key: "coupon", Summary: "过期券未覆盖", Confidence: nil, CreatedAt: base.Add(3 * time.Minute)},
		},
		Execution: agentcontext.ExecutionSnapshot{WaveStatus: "done", Passed: 8, Failed: 1, Blocked: 0, Skipped: 2},
		Knowledge: []agentcontext.KnowledgeSnapshot{
			{ID: 1, Title: "登录失败重试", Summary: "先刷新再重试", Confidence: floatPtr(0.8)},
			{ID: 2, Title: "优惠券边界值", Summary: "测试 0/1/最大值", Confidence: floatPtr(0.6)},
		},
		OpenQuestions: agentcontext.OpenQuestionsSnapshot{
			Questions:     []string{"过期券是否可用"},
			HumanRequests: []string{"需要人工登录 admin 账号"},
		},
	}
}

func reverseFindings(items []agentcontext.FactSnapshot) {
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
}

func reverseKnowledge(items []agentcontext.KnowledgeSnapshot) {
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
}

// TestBriefingDeterministic pins the two determinism guarantees of spec §6.2:
// identical data must render byte-identically, and the ranked sections must not
// depend on the row order the read port happens to return.
func TestBriefingDeterministic(t *testing.T) {
	reader := &fakeBriefingReader{source: richSource()}
	builder := agentcontext.NewBriefingBuilder(reader)

	first, err := builder.BuildBriefing(context.Background(), 101)
	if err != nil {
		t.Fatalf("BuildBriefing: %v", err)
	}
	second, err := builder.BuildBriefing(context.Background(), 101)
	if err != nil {
		t.Fatalf("BuildBriefing (second): %v", err)
	}
	if reader.calls != 2 || reader.lastID != 101 {
		t.Fatalf("read port calls = %d, last version = %d, want 2 and 101", reader.calls, reader.lastID)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("two builds from identical data differ:\n%#v\n%#v", first, second)
	}
	firstText := first.Render()
	if firstText != second.Render() {
		t.Fatalf("two renders from identical data differ:\n%s\n---\n%s", firstText, second.Render())
	}

	// Reverse the order of the ranked inputs: the briefing ranks facts and
	// knowledge itself, so the rendered bytes must not change.
	shuffled := richSource()
	reverseFindings(shuffled.Findings)
	reverseKnowledge(shuffled.Knowledge)
	other, err := agentcontext.NewBriefingBuilder(&fakeBriefingReader{source: shuffled}).BuildBriefing(context.Background(), 101)
	if err != nil {
		t.Fatalf("BuildBriefing (shuffled): %v", err)
	}
	if other.Render() != firstText {
		t.Fatalf("row order changed the briefing:\n%s\n---\n%s", firstText, other.Render())
	}

	// Fixed section order (spec §6.2): head first, then the seven groups.
	headings := []string{
		"## 目标/阶段",
		"## 需求",
		"## 用例",
		"## 计划",
		"## 发现",
		"## 执行",
		"## 知识",
		"## 未决",
	}
	previous := -1
	for _, heading := range headings {
		index := strings.Index(firstText, heading)
		if index < 0 {
			t.Fatalf("missing heading %q in:\n%s", heading, firstText)
		}
		if index <= previous {
			t.Fatalf("heading %q is out of order in:\n%s", heading, firstText)
		}
		previous = index
	}

	for _, want := range []string{
		"目标：验证优惠券结算",
		"阶段：executing",
		"已确认需求（2）：",
		"- 优惠券抵扣订单总额（验收：总额按折扣减少）",
		"未决缺口（1）：",
		"- 缺少过期券的边界路径",
		"草稿 2 / 已确认 3 / 失效 1",
		"覆盖缺口（1）：",
		"当前：[pending] 生成用例草稿 [id=1 revision=0]（依赖：确认需求）",
		"下一步：[in_progress] 评审用例草稿 [id=2 revision=0]（依赖：生成用例草稿）",
		"- [page] login：登录页可达（置信 0.90）",
		"最近 wave：done（通过 8 / 失败 1 / 阻塞 0 / 跳过 2）",
		"- 登录失败重试：先刷新再重试（置信 0.80）",
		"问题：",
		"- 过期券是否可用",
		"人工请求：",
		"- 需要人工登录 admin 账号",
	} {
		if !strings.Contains(firstText, want) {
			t.Fatalf("rendered briefing is missing %q:\n%s", want, firstText)
		}
	}
	if strings.Contains(firstText, "…") {
		t.Fatalf("small briefing must not truncate:\n%s", firstText)
	}
	if got := first.Requirements; len(got) != 1 || got[0].Name != agentcontext.SectionRequirements {
		t.Fatalf("requirements sections = %#v, want one %q section", got, agentcontext.SectionRequirements)
	}
}

// TestBriefingEmptyVersion pins the empty/malformed-input behaviour (Review
// Focus 3): every group is present with "无" content, nothing is nil and
// nothing panics.
func TestBriefingEmptyVersion(t *testing.T) {
	t.Run("empty source", func(t *testing.T) {
		reader := &fakeBriefingReader{source: agentcontext.Source{
			Version: agentcontext.VersionSnapshot{Goal: "空版本", Stage: "created"},
		}}
		briefing, err := agentcontext.NewBriefingBuilder(reader).BuildBriefing(context.Background(), 7)
		if err != nil {
			t.Fatalf("BuildBriefing: %v", err)
		}
		groups := []struct {
			name     string
			sections []agentcontext.Section
		}{
			{agentcontext.SectionRequirements, briefing.Requirements},
			{agentcontext.SectionCases, briefing.Cases},
			{agentcontext.SectionPlan, briefing.Plan},
			{agentcontext.SectionFindings, briefing.Findings},
			{agentcontext.SectionExecution, briefing.Execution},
			{agentcontext.SectionKnowledge, briefing.Knowledge},
			{agentcontext.SectionOpenQuestions, briefing.OpenQuestions},
		}
		for _, group := range groups {
			if len(group.sections) != 1 {
				t.Fatalf("%s: got %d sections, want 1", group.name, len(group.sections))
			}
			if group.sections[0].Name != group.name {
				t.Fatalf("%s: section name = %q", group.name, group.sections[0].Name)
			}
			if group.sections[0].Content != "无" {
				t.Fatalf("%s: content = %q, want 无", group.name, group.sections[0].Content)
			}
		}
		rendered := briefing.Render()
		if !strings.Contains(rendered, "目标：空版本") || !strings.Contains(rendered, "阶段：created") {
			t.Fatalf("head missing from:\n%s", rendered)
		}
		for _, title := range []string{"需求", "用例", "计划", "发现", "执行", "知识", "未决"} {
			if !strings.Contains(rendered, "## "+title+"\n无") {
				t.Fatalf("section %q is not 无 in:\n%s", title, rendered)
			}
		}
	})

	t.Run("zero briefing", func(t *testing.T) {
		rendered := (agentcontext.Briefing{}).Render()
		if !strings.Contains(rendered, "目标：无") || !strings.Contains(rendered, "阶段：无") {
			t.Fatalf("empty briefing head is not 无:\n%s", rendered)
		}
		for _, title := range []string{"需求", "用例", "计划", "发现", "执行", "知识", "未决"} {
			if !strings.Contains(rendered, "## "+title+"\n无") {
				t.Fatalf("empty briefing section %q is not 无 in:\n%s", title, rendered)
			}
		}
	})

	t.Run("invalid version id", func(t *testing.T) {
		for _, versionID := range []int64{0, -1} {
			if _, err := agentcontext.NewBriefingBuilder(&fakeBriefingReader{}).BuildBriefing(context.Background(), versionID); err == nil {
				t.Fatalf("BuildBriefing(%d) = nil error, want invalid version error", versionID)
			}
		}
	})

	t.Run("reader error propagates", func(t *testing.T) {
		boom := errors.New("database unavailable")
		builder := agentcontext.NewBriefingBuilder(&fakeBriefingReader{err: boom})
		if _, err := builder.BuildBriefing(context.Background(), 7); !errors.Is(err, boom) {
			t.Fatalf("BuildBriefing error = %v, want wrapped %v", err, boom)
		}
	})

	t.Run("nil reader", func(t *testing.T) {
		if _, err := agentcontext.NewBriefingBuilder(nil).BuildBriefing(context.Background(), 7); err == nil {
			t.Fatal("BuildBriefing with a nil reader = nil error, want configuration error")
		}
	})

	t.Run("malformed entries do not panic", func(t *testing.T) {
		source := agentcontext.Source{
			Requirements: agentcontext.RequirementsSnapshot{
				Confirmed: []agentcontext.RequirementSnapshot{{}, {Title: "  ", Acceptance: " "}},
				Gaps:      []string{"", "   "},
			},
			Cases:         agentcontext.CasesSnapshot{Draft: -1, CoverageGaps: []string{""}},
			Plan:          []agentcontext.PlanItemSnapshot{{}, {Title: ""}},
			Findings:      []agentcontext.FactSnapshot{{}},
			Knowledge:     []agentcontext.KnowledgeSnapshot{{}},
			OpenQuestions: agentcontext.OpenQuestionsSnapshot{Questions: []string{" "}},
		}
		briefing, err := agentcontext.NewBriefingBuilder(&fakeBriefingReader{source: source}).BuildBriefing(context.Background(), 7)
		if err != nil {
			t.Fatalf("BuildBriefing: %v", err)
		}
		rendered := briefing.Render()
		for _, title := range []string{"需求", "用例", "计划", "发现", "执行", "知识", "未决"} {
			if !strings.Contains(rendered, "## "+title+"\n无") {
				t.Fatalf("malformed %q content did not collapse to 无:\n%s", title, rendered)
			}
		}
	})
}

// TestBriefingBudget pins R-A8-5: the render stays inside the 3k token
// envelope, over-budget sections are truncated per section at their own budget
// (plan 600, findings 800), and the goal, stage and open questions are never
// truncated.
func TestBriefingBudget(t *testing.T) {
	builder := agentcontext.NewBriefingBuilder(&fakeBriefingReader{source: oversizedSource()})
	briefing, err := builder.BuildBriefing(context.Background(), 55)
	if err != nil {
		t.Fatalf("BuildBriefing: %v", err)
	}
	rendered := briefing.Render()

	if got := briefingTokens(rendered); got > agentcontext.BriefingTokenBudget {
		t.Fatalf("rendered briefing uses %d tokens, want <= %d", got, agentcontext.BriefingTokenBudget)
	}

	// The protected head and open questions survive verbatim.
	for _, want := range []string{
		"预算测试目标",
		"blocked（阻塞：等待人工登录）",
		"OPENQ-END-MARKER",
		"需要人工登录 admin 账号",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("protected content %q was dropped:\n%s", want, rendered)
		}
	}

	// The oversized regular sections are cut; the tail of the requirements is
	// not rendered at all.
	if strings.Contains(rendered, "REQ-END-MARKER") {
		t.Fatalf("oversized requirements were not truncated:\n%s", rendered)
	}
	caps := []struct {
		title  string
		budget int
	}{
		{"需求", 600},
		{"用例", 300},
		{"计划", 600},
		{"发现", 800},
		{"执行", 200},
		{"知识", 200},
	}
	for _, section := range caps {
		body := renderedSection(rendered, section.title)
		if body == "" {
			t.Fatalf("section %q is missing:\n%s", section.title, rendered)
		}
		if got := briefingTokens(body); got > section.budget {
			t.Fatalf("section %q uses %d tokens, want <= %d:\n%s", section.title, got, section.budget, body)
		}
		if !strings.Contains(body, "…") {
			t.Fatalf("oversized section %q was not truncated:\n%s", section.title, body)
		}
	}

	// Open questions are exempt from the 200-token envelope in §6.2 and are
	// never truncated, so the fixture deliberately exceeds it.
	openQuestions := renderedSection(rendered, "未决")
	if got := briefingTokens(openQuestions); got <= 200 {
		t.Fatalf("open questions fixture is too small (%d tokens); it must prove the exemption", got)
	}

	// The plan cap is applied before rendering: only five items are listed even
	// though the source carries forty.
	if body := briefing.Plan[0].Content; !strings.Contains(body, "（另有 35 项未列出）") {
		t.Fatalf("plan cap marker is missing from the derived section:\n%s", body)
	}

	// Truncation itself is deterministic.
	if rendered != briefing.Render() {
		t.Fatal("budgeted render is not deterministic")
	}
	again, err := builder.BuildBriefing(context.Background(), 55)
	if err != nil {
		t.Fatalf("BuildBriefing (again): %v", err)
	}
	if again.Render() != rendered {
		t.Fatal("budgeted render changed between builds")
	}
}

// budgetProbeSource pushes only the plan and findings groups over their section
// caps. Every other group is empty, so the render stays inside the total
// envelope and the rendered proportions pin the R-A8-5 section budgets.
func budgetProbeSource() agentcontext.Source {
	source := agentcontext.Source{
		Version: agentcontext.VersionSnapshot{Goal: "预算探针", Stage: "executing"},
	}
	for index := 0; index < 6; index++ {
		source.Plan = append(source.Plan, agentcontext.PlanItemSnapshot{
			ID:     int64(index + 1),
			Title:  fmt.Sprintf("%s%d", strings.Repeat("计划项", 80), index),
			Status: "pending",
		})
	}
	base := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	for index := 0; index < 8; index++ {
		source.Findings = append(source.Findings, agentcontext.FactSnapshot{
			ID:         int64(index + 1),
			Kind:       "page",
			Key:        fmt.Sprintf("probe-%d", index+1),
			Summary:    strings.Repeat("事实摘要", 80),
			Confidence: floatPtr(0.90 - float64(index)*0.05),
			CreatedAt:  base.Add(time.Duration(index) * time.Minute),
		})
	}
	return source
}

// TestBriefingSectionBudgets pins the R-A8-5 budget constants: the total
// envelope is 3k tokens, the plan section keeps 600 tokens and the findings
// section 800. Both probe sections are truncated right at their cap, so the
// rendered token counts prove the values instead of only bounding them.
func TestBriefingSectionBudgets(t *testing.T) {
	if got := agentcontext.BriefingTokenBudget; got != 3000 {
		t.Fatalf("BriefingTokenBudget = %d, want 3000 (R-A8-5)", got)
	}
	rendered := buildBriefing(t, budgetProbeSource(), 140).Render()
	if got := briefingTokens(rendered); got > 3000 {
		t.Fatalf("probe briefing uses %d tokens, want <= 3000", got)
	}
	plan := renderedSection(rendered, "计划")
	if got := briefingTokens(plan); got != 600 {
		t.Fatalf("plan section uses %d tokens, want exactly 600 (600*5/3 = 1000 truncated runes):\n%s", got, plan)
	}
	findings := renderedSection(rendered, "发现")
	if got := briefingTokens(findings); got != 800 {
		t.Fatalf("findings section uses %d tokens, want exactly 800 (800*5/3 = 1333 truncated runes):\n%s", got, findings)
	}
}

// currentNextSource is the R-A8-5 current/next fixture: the first non-terminal
// item is 生成用例草稿 (in_progress) and the following non-terminal item is
// 补充边界用例 (pending); the terminal items in between must never be promoted.
func currentNextSource() agentcontext.Source {
	return agentcontext.Source{
		Version: agentcontext.VersionSnapshot{Goal: "验证当前与下一步", Stage: "executing"},
		Plan: []agentcontext.PlanItemSnapshot{
			{ID: 1, Title: "完成需求梳理", Status: "done"},
			{ID: 2, Title: "生成用例草稿", Status: "in_progress"},
			{ID: 3, Title: "评审旧草稿", Status: "completed"},
			{ID: 4, Title: "补充边界用例", Status: "pending"},
			{ID: 5, Title: "执行回归测试", Status: "pending"},
			{ID: 6, Title: "归档旧结果", Status: "skipped"},
		},
	}
}

// TestBriefingKeepsCurrentAndNextPlanItems pins R-A8-5: the current plan item
// is the first non-terminal item in plan order, the next one is the following
// non-terminal item, both carry an explicit label, and the 3k total envelope
// still holds.
func TestBriefingKeepsCurrentAndNextPlanItems(t *testing.T) {
	text := buildBriefing(t, currentNextSource(), 120).Render()
	for _, want := range []string{
		"当前：[in_progress] 生成用例草稿",
		"下一步：[pending] 补充边界用例",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered briefing is missing %q:\n%s", want, text)
		}
	}
	if got := briefingTokens(text); got > 3000 {
		t.Fatalf("briefing uses %d tokens, want <= 3000:\n%s", got, text)
	}
	for _, banned := range []string{"下一步：[done]", "下一步：[completed]", "下一步：[skipped]"} {
		if strings.Contains(text, banned) {
			t.Fatalf("terminal item rendered as next (%q):\n%s", banned, text)
		}
	}

	t.Run("single open item has no fabricated next", func(t *testing.T) {
		source := agentcontext.Source{
			Plan: []agentcontext.PlanItemSnapshot{
				{ID: 1, Title: "唯一待办", Status: "todo"},
				{ID: 2, Title: "已经完成", Status: "done"},
			},
		}
		rendered := buildBriefing(t, source, 121).Render()
		if !strings.Contains(rendered, "当前：[todo] 唯一待办") {
			t.Fatalf("the single open item is not current:\n%s", rendered)
		}
		if strings.Contains(rendered, "下一步：") {
			t.Fatalf("briefing fabricated a next item:\n%s", rendered)
		}
	})

	t.Run("all terminal items have neither current nor next", func(t *testing.T) {
		source := agentcontext.Source{
			Plan: []agentcontext.PlanItemSnapshot{
				{ID: 1, Title: "已完成一", Status: "done"},
				{ID: 2, Title: "已取消二", Status: "cancelled"},
			},
		}
		rendered := buildBriefing(t, source, 123).Render()
		if strings.Contains(rendered, "当前：") || strings.Contains(rendered, "下一步：") {
			t.Fatalf("fully terminal plan invented current/next:\n%s", rendered)
		}
		if !strings.Contains(rendered, "已完成一") {
			t.Fatalf("terminal plan items were dropped:\n%s", rendered)
		}
	})

	t.Run("current and next survive the total budget squeeze", func(t *testing.T) {
		source := currentNextSource()
		source.OpenQuestions = agentcontext.OpenQuestionsSnapshot{
			Questions: []string{strings.Repeat("压缩压力", 1150)},
		}
		rendered := buildBriefing(t, source, 122).Render()
		if got := briefingTokens(rendered); got > 3000 {
			t.Fatalf("squeezed briefing uses %d tokens, want <= 3000", got)
		}
		for _, want := range []string{"当前：", "下一步：", "生成用例草稿", "补充边界用例"} {
			if !strings.Contains(rendered, want) {
				t.Fatalf("squeezed briefing dropped %q:\n%s", want, rendered)
			}
		}
	})
}

// TestBriefingPlanTerminalStatuses pins which plan statuses are terminal for
// the current/next selection (R-A8-5). The set must match the production read
// port filter (done/completed/cancelled/skipped/failed/closed) plus the
// cancelled spelling alias, matched case-insensitively and after oneLine
// normalization; an empty or unknown status still counts as open work. A
// terminal item can never become current while an open item is available.
func TestBriefingPlanTerminalStatuses(t *testing.T) {
	cases := []struct {
		name     string
		status   string
		terminal bool
	}{
		{"done", "done", true},
		{"completed", "completed", true},
		{"cancelled", "cancelled", true},
		{"canceled alias", "canceled", true},
		{"skipped", "skipped", true},
		{"failed", "failed", true},
		{"closed", "closed", true},
		{"uppercase done", "DONE", true},
		{"mixed case completed", "Completed", true},
		{"uppercase canceled", "CANCELED", true},
		{"uppercase failed", "FAILED", true},
		{"uppercase closed", "CLOSED", true},
		{"whitespace normalized", "  closed  ", true},
		{"empty is open", "", false},
		{"pending is open", "pending", false},
		{"in progress is open", "in_progress", false},
		{"unknown is open", "mystery", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			source := agentcontext.Source{
				Plan: []agentcontext.PlanItemSnapshot{
					{ID: 1, Title: "候选状态项", Status: testCase.status},
					{ID: 2, Title: "兜底待办", Status: "pending"},
				},
			}
			rendered := buildBriefing(t, source, 124).Render()
			plan := renderedSection(rendered, "计划")
			lines := strings.Split(plan, "\n")
			if len(lines) == 0 || lines[0] == "" {
				t.Fatalf("plan section is empty:\n%s", rendered)
			}
			want := "当前：[pending] 兜底待办 [id=2 revision=0]"
			if !testCase.terminal {
				want = "当前：候选状态项 [id=1 revision=0]"
				if status := strings.Join(strings.Fields(testCase.status), " "); status != "" {
					want = "当前：[" + status + "] 候选状态项 [id=1 revision=0]"
				}
			}
			if lines[0] != want {
				t.Fatalf("status %q: first plan line = %q, want %q:\n%s", testCase.status, lines[0], want, plan)
			}
		})
	}
}

// competingSqueezeSource is the discriminating mandatory-prefix fixture. The
// four other regular groups carry enough content to compete for the total
// budget, and the protected open questions are large enough that the six
// regular groups are squeezed almost to their markers. The plan/findings
// proportional share falls below the protected current/next and top-3 prefixes,
// so their visibility can only come from the mandatory reservation.
func competingSqueezeSource() agentcontext.Source {
	base := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	source := agentcontext.Source{
		Version: agentcontext.VersionSnapshot{Goal: "验证竞争压缩", Stage: "executing"},
	}
	for index := 0; index < 20; index++ {
		source.Requirements.Confirmed = append(source.Requirements.Confirmed, agentcontext.RequirementSnapshot{
			Title:      fmt.Sprintf("需求标题%d", index),
			Acceptance: strings.Repeat("验收标准", 20),
		})
	}
	for index := 0; index < 8; index++ {
		source.Cases.CoverageGaps = append(source.Cases.CoverageGaps, strings.Repeat("覆盖缺口", 20))
	}
	source.Plan = []agentcontext.PlanItemSnapshot{
		{ID: 1, Title: "生成用例草稿", Status: "in_progress", DependsOn: []string{"完成需求梳理"}},
		{ID: 2, Title: "补充边界用例", Status: "pending", DependsOn: []string{"生成用例草稿"}},
		{ID: 3, Title: "执行回归测试", Status: "pending", DependsOn: []string{"补充边界用例"}},
		{ID: 4, Title: "归档旧结果", Status: "done"},
	}
	for index := 0; index < 8; index++ {
		source.Findings = append(source.Findings, agentcontext.FactSnapshot{
			ID:         int64(index + 1),
			Kind:       "page",
			Key:        fmt.Sprintf("top-%d", index+1),
			Summary:    strings.Repeat("事实摘要", 8),
			Confidence: floatPtr(0.95 - float64(index)*0.05),
			CreatedAt:  base.Add(time.Duration(index) * time.Minute),
		})
	}
	source.Execution = agentcontext.ExecutionSnapshot{
		WaveStatus: "failed", Passed: 5, Failed: 9, Blocked: 2, Skipped: 1,
		Blocker: strings.Repeat("执行阻塞原因", 50),
	}
	for index := 0; index < 5; index++ {
		source.Knowledge = append(source.Knowledge, agentcontext.KnowledgeSnapshot{
			ID:         int64(index + 1),
			Title:      fmt.Sprintf("知识标题%d", index),
			Summary:    strings.Repeat("知识摘要", 30),
			Confidence: floatPtr(0.8),
		})
	}
	// 4,600 protected open-question runes: the render lands just over the 3k
	// envelope after the per-section truncation, so Render must squeeze the
	// regular groups instead of falling into the documented protected-content
	// exception. (The plan labels carry their id+revision optimistic-lock
	// identity, which is protected content.)
	source.OpenQuestions = agentcontext.OpenQuestionsSnapshot{
		Questions: []string{strings.Repeat("压缩", 2300)},
	}
	return source
}

// TestBriefingMandatoryPrefixesSurviveCompetingSqueeze is the R-A8-5
// discrimination test: unlike the small current/next and top-3 fixtures, the
// plan and findings groups must compete for the total envelope with large
// unprotected sections. The squeeze allowance left for the plan and findings is
// zero runes (both bodies collapse to their mandatory prefix plus the
// truncation marker), so only the protected-prefix mechanism can keep the
// tail of the second plan label (its dependency) and the tail of the third fact
// (its confidence) visible. If that mechanism is removed these exact substrings
// disappear.
func TestBriefingMandatoryPrefixesSurviveCompetingSqueeze(t *testing.T) {
	rendered := buildBriefing(t, competingSqueezeSource(), 125).Render()
	if got := briefingTokens(rendered); got > agentcontext.BriefingTokenBudget {
		t.Fatalf("squeezed briefing uses %d tokens, want <= %d", got, agentcontext.BriefingTokenBudget)
	}

	plan := renderedSection(rendered, "计划")
	for _, want := range []string{
		"当前：[in_progress] 生成用例草稿 [id=1 revision=0]（依赖：完成需求梳理）",
		"下一步：[pending] 补充边界用例 [id=2 revision=0]（依赖：生成用例草稿）",
	} {
		if !strings.Contains(plan, want) {
			t.Fatalf("squeezed plan dropped the mandatory label tail %q:\n%s", want, plan)
		}
	}
	if !strings.Contains(plan, "…") {
		t.Fatalf("plan section was not squeezed:\n%s", plan)
	}
	if strings.Contains(plan, "执行回归测试") {
		t.Fatalf("plan optional tail survived the squeeze fixture:\n%s", plan)
	}

	findings := renderedSection(rendered, "发现")
	summary := strings.Repeat("事实摘要", 8)
	for _, want := range []string{
		"- [page] top-1：" + summary + "（置信 0.95）",
		"- [page] top-2：" + summary + "（置信 0.90）",
		"- [page] top-3：" + summary + "（置信 0.85）",
	} {
		if !strings.Contains(findings, want) {
			t.Fatalf("squeezed findings dropped the mandatory fact tail %q:\n%s", want, findings)
		}
	}
	if !strings.Contains(findings, "…") {
		t.Fatalf("findings section was not squeezed:\n%s", findings)
	}
	if strings.Contains(findings, "top-4") {
		t.Fatalf("findings optional tail survived the squeeze fixture:\n%s", findings)
	}

	// The competing unprotected groups sit far below their section caps (600 /
	// 300 / 200 / 200 tokens): the squeeze is severe, not incidental.
	for _, title := range []string{"需求", "用例", "执行", "知识"} {
		body := renderedSection(rendered, title)
		if got := briefingTokens(body); got > 10 {
			t.Fatalf("section %q uses %d tokens, want <= 10 after the competing squeeze:\n%s", title, got, body)
		}
	}
}

// TestBriefingMandatoryPlanPrefixExceedsSectionCap pins the raise-to-protected
// branch of the plan truncation: the current/next labels (long titles and
// dependencies) alone exceed the 600-token plan section budget (600*5/3 = 1000
// runes). Render must still emit the whole mandatory prefix, letting the plan
// section body exceed its own budget rather than cut current/next.
func TestBriefingMandatoryPlanPrefixExceedsSectionCap(t *testing.T) {
	title := strings.Repeat("超长计划标题", 65)
	nextTitle := title + "（副本）"
	currentDependency := strings.Repeat("前置依赖甲", 25)
	nextDependency := strings.Repeat("前置依赖乙", 25)
	source := agentcontext.Source{
		Version: agentcontext.VersionSnapshot{Goal: "验证必显前缀超出分节预算", Stage: "executing"},
		Plan: []agentcontext.PlanItemSnapshot{
			{ID: 1, Title: title, Status: "in_progress", DependsOn: []string{currentDependency}},
			{ID: 2, Title: nextTitle, Status: "pending", DependsOn: []string{nextDependency}},
			{ID: 3, Title: "可选计划项", Status: "pending"},
		},
	}
	rendered := buildBriefing(t, source, 126).Render()
	plan := renderedSection(rendered, "计划")
	for _, want := range []string{
		"当前：[in_progress] " + title + " [id=1 revision=0]（依赖：" + currentDependency + "）",
		"下一步：[pending] " + nextTitle + " [id=2 revision=0]（依赖：" + nextDependency + "）",
	} {
		if !strings.Contains(plan, want) {
			t.Fatalf("over-cap plan dropped the mandatory label tail %q:\n%s", want, plan)
		}
	}
	if got := briefingTokens(plan); got <= 600 {
		t.Fatalf("plan section uses %d tokens, want > 600: the mandatory prefix must raise the section truncation limit:\n%s", got, plan)
	}
	if !strings.Contains(plan, "…") {
		t.Fatalf("plan optional tail was not truncated:\n%s", plan)
	}
	if strings.Contains(plan, "可选计划项") {
		t.Fatalf("plan optional item survived the over-cap truncation:\n%s", plan)
	}
	if got := briefingTokens(rendered); got > agentcontext.BriefingTokenBudget {
		t.Fatalf("briefing uses %d tokens, want <= %d", got, agentcontext.BriefingTokenBudget)
	}
}

// topFactsSource is the R-A8-5 top-3 fixture: eight findings with a descending
// confidence spread, so the ranking is unambiguous.
func topFactsSource() agentcontext.Source {
	base := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	confidences := []float64{0.95, 0.90, 0.85, 0.60, 0.55, 0.50, 0.45, 0.40}
	source := agentcontext.Source{
		Version: agentcontext.VersionSnapshot{Goal: "验证发现排序", Stage: "executing"},
	}
	for index, confidence := range confidences {
		source.Findings = append(source.Findings, agentcontext.FactSnapshot{
			ID:         int64(index + 1),
			Kind:       "page",
			Key:        fmt.Sprintf("top-%d", index+1),
			Summary:    strings.Repeat("事实摘要", 75),
			Confidence: floatPtr(confidence),
			CreatedAt:  base.Add(time.Duration(index) * time.Minute),
		})
	}
	return source
}

// TestBriefingKeepsTop3Facts pins R-A8-5: the three highest-confidence facts
// stay visible even when the findings section is truncated or the total budget
// squeezes the section body down to its mandatory tail.
func TestBriefingKeepsTop3Facts(t *testing.T) {
	assertTop3 := func(t *testing.T, rendered string) {
		t.Helper()
		for _, key := range []string{"top-1", "top-2", "top-3"} {
			if !strings.Contains(rendered, key) {
				t.Fatalf("briefing dropped top-3 fact %q:\n%s", key, rendered)
			}
		}
		first := strings.Index(rendered, "top-1")
		second := strings.Index(rendered, "top-2")
		third := strings.Index(rendered, "top-3")
		if first > second || second > third {
			t.Fatalf("facts are not ranked by confidence:\n%s", rendered)
		}
	}

	t.Run("top-3 survive section truncation", func(t *testing.T) {
		assertTop3(t, buildBriefing(t, topFactsSource(), 130).Render())
	})

	t.Run("top-3 survive the total budget squeeze", func(t *testing.T) {
		source := topFactsSource()
		source.OpenQuestions = agentcontext.OpenQuestionsSnapshot{
			Questions: []string{strings.Repeat("压缩压力", 925)},
		}
		rendered := buildBriefing(t, source, 131).Render()
		if got := briefingTokens(rendered); got > 3000 {
			t.Fatalf("squeezed briefing uses %d tokens, want <= 3000", got)
		}
		assertTop3(t, rendered)
	})
}

func oversizedSource() agentcontext.Source {
	base := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	source := agentcontext.Source{
		Version: agentcontext.VersionSnapshot{Goal: "预算测试目标", Stage: "blocked", Blocker: "等待人工登录"},
		Execution: agentcontext.ExecutionSnapshot{
			WaveStatus: "failed",
			Passed:     5,
			Failed:     9,
			Blocked:    2,
			Skipped:    1,
			Blocker:    strings.Repeat("执行阻塞原因", 100),
		},
	}
	for index := 0; index < 60; index++ {
		source.Requirements.Confirmed = append(source.Requirements.Confirmed, agentcontext.RequirementSnapshot{
			Title:      fmt.Sprintf("需求标题%d", index),
			Acceptance: strings.Repeat("验收标准", 8),
		})
		source.Requirements.Gaps = append(source.Requirements.Gaps, strings.Repeat("未决缺口", 6))
	}
	source.Requirements.Gaps = append(source.Requirements.Gaps, "REQ-END-MARKER")
	source.Cases = agentcontext.CasesSnapshot{Draft: 12, Confirmed: 34, Stale: 56}
	for index := 0; index < 40; index++ {
		source.Cases.CoverageGaps = append(source.Cases.CoverageGaps, strings.Repeat("覆盖缺口", 6))
		source.Plan = append(source.Plan, agentcontext.PlanItemSnapshot{
			ID:        int64(index + 1),
			Title:     strings.Repeat("计划项", 100),
			Status:    "pending",
			DependsOn: []string{"确认需求"},
		})
		source.Findings = append(source.Findings, agentcontext.FactSnapshot{
			ID:         int64(index + 1),
			Kind:       "page",
			Key:        fmt.Sprintf("key-%d", index),
			Summary:    strings.Repeat("发现事实", 80),
			Confidence: floatPtr(float64(index%10) / 10),
			CreatedAt:  base.Add(time.Duration(index) * time.Minute),
		})
		source.Knowledge = append(source.Knowledge, agentcontext.KnowledgeSnapshot{
			ID:         int64(index + 1),
			Title:      strings.Repeat("知识标题", 6),
			Summary:    strings.Repeat("知识摘要", 20),
			Confidence: floatPtr(0.8),
		})
	}
	source.OpenQuestions = agentcontext.OpenQuestionsSnapshot{
		Questions: []string{
			strings.Repeat("未决问题", 20),
			strings.Repeat("待确认问题", 20),
			strings.Repeat("未决问题", 20) + " OPENQ-END-MARKER",
		},
		HumanRequests: []string{
			"需要人工登录 admin 账号",
			strings.Repeat("人工请求", 15),
		},
	}
	return source
}

// boundarySource is the protected-budget boundary fixture: the six regular
// groups are filled by oversizedSource, but the plan and findings groups are
// deliberately empty so the fixture isolates the goal/stage and open-questions
// protection. Its mandatory current/next and top-3 prefixes are exercised by
// their own tests.
func boundarySource() agentcontext.Source {
	source := oversizedSource()
	source.Plan = nil
	source.Findings = nil
	return source
}

// briefingTokens is the independent test-side token estimate from spec §6.3:
// at least 0.6 tokens per rune (rounded up), the conservative coefficient. It
// mirrors the production estimateBriefingTokens helper exactly (ceil(0.6×runes)
// is computed as (runes×3+4)/5 in integer arithmetic, so runes → tokens is the
// documented conversion).
func briefingTokens(text string) int {
	return (utf8.RuneCountInString(text)*3 + 4) / 5
}

// renderedSection returns the body rendered between the "## <title>" heading
// and the next group heading.
func renderedSection(rendered, title string) string {
	marker := "## " + title + "\n"
	index := strings.Index(rendered, marker)
	if index < 0 {
		return ""
	}
	rest := rendered[index+len(marker):]
	if next := strings.Index(rest, "\n\n## "); next >= 0 {
		rest = rest[:next]
	}
	return strings.TrimSuffix(rest, "\n")
}

type briefingSectionCap struct {
	title  string
	budget int
}

// briefingSectionCaps mirrors the per-section budgets from spec §6.2.
func briefingSectionCaps() []briefingSectionCap {
	return []briefingSectionCap{
		{"需求", 600},
		{"用例", 300},
		{"计划", 600},
		{"发现", 800},
		{"执行", 200},
		{"知识", 200},
	}
}

// totalSectionBudgetTokens is the most the six regular sections may use when
// each sits exactly at its own cap.
func totalSectionBudgetTokens() int {
	total := 0
	for _, section := range briefingSectionCaps() {
		total += section.budget
	}
	return total
}

func buildBriefing(t *testing.T, source agentcontext.Source, versionID int64) agentcontext.Briefing {
	t.Helper()
	briefing, err := agentcontext.NewBriefingBuilder(&fakeBriefingReader{source: source}).BuildBriefing(context.Background(), versionID)
	if err != nil {
		t.Fatalf("BuildBriefing: %v", err)
	}
	return briefing
}

// protectedHeavySource is the total-budget fixture: the six regular groups are
// pushed to their standalone caps (through oversizedSource), while the open
// questions are large enough to break the 3k total on their own when combined
// with those caps, but not large enough to exceed the envelope by themselves.
func protectedHeavySource() agentcontext.Source {
	source := oversizedSource()
	source.OpenQuestions = agentcontext.OpenQuestionsSnapshot{
		Questions: []string{
			strings.Repeat("大规模未决问题描述", 38) + " OPENQ-END-MARKER",
			strings.Repeat("另一个未决问题", 40),
			strings.Repeat("待补充的边界问题", 40),
		},
		HumanRequests: []string{
			strings.Repeat("人工确认请求", 40),
			strings.Repeat("需要补充资料", 40),
		},
	}
	return source
}

// TestBriefingTotalBudget pins the total envelope from R-A8-5. The per-section
// caps alone (600+300+600+800+200+200 = 2700) plus a large protected
// open-questions group can exceed 3000, so Render must squeeze the six regular
// groups until the whole text fits, while never cutting the goal, stage or open
// questions. If the protected content plus the fixed headings and the one-rune
// marker each regular group needs already exceeds the envelope, the render is
// returned unchanged: that is the documented, unavoidable exception.
func TestBriefingTotalBudget(t *testing.T) {
	t.Run("large open questions shrink the regular sections", func(t *testing.T) {
		briefing := buildBriefing(t, protectedHeavySource(), 77)

		// Fixture precondition: the protected content is large but does not
		// exceed the envelope on its own.
		protectedOnly := agentcontext.Briefing{
			Goal:          briefing.Goal,
			Stage:         briefing.Stage,
			OpenQuestions: briefing.OpenQuestions,
		}
		if got := briefingTokens(protectedOnly.Render()); got > agentcontext.BriefingTokenBudget {
			t.Fatalf("fixture precondition: protected content alone uses %d tokens, want <= %d", got, agentcontext.BriefingTokenBudget)
		}

		rendered := briefing.Render()
		if got := briefingTokens(rendered); got > agentcontext.BriefingTokenBudget {
			t.Fatalf("rendered briefing uses %d tokens, want <= %d", got, agentcontext.BriefingTokenBudget)
		}

		// The protected group must survive verbatim: the total-budget pass may
		// only touch the regular groups.
		if got, want := renderedSection(rendered, "未决"), renderedSection(protectedOnly.Render(), "未决"); got != want {
			t.Fatalf("protected open questions changed:\n%s\n---\n%s", got, want)
		}
		for _, want := range []string{"目标：预算测试目标", "OPENQ-END-MARKER", "人工请求："} {
			if !strings.Contains(rendered, want) {
				t.Fatalf("protected content %q was dropped:\n%s", want, rendered)
			}
		}

		// Each regular group still respects its own cap, but the combined usage
		// is now below the sum of those caps: the total-budget pass squeezed it.
		regularTokens := 0
		for _, section := range briefingSectionCaps() {
			body := renderedSection(rendered, section.title)
			if body == "" {
				t.Fatalf("section %q is missing:\n%s", section.title, rendered)
			}
			if got := briefingTokens(body); got > section.budget {
				t.Fatalf("section %q uses %d tokens, want <= %d", section.title, got, section.budget)
			}
			regularTokens += briefingTokens(body)
		}
		if regularTokens >= totalSectionBudgetTokens() {
			t.Fatalf("regular sections still use %d tokens; the total-budget pass did not squeeze them", regularTokens)
		}

		// The total-budget pass is deterministic.
		if rendered != briefing.Render() {
			t.Fatal("total-budget render is not deterministic")
		}
		if again := buildBriefing(t, protectedHeavySource(), 77); again.Render() != rendered {
			t.Fatal("total-budget render changed between builds")
		}
	})

	t.Run("protected content alone over the envelope is returned unchanged", func(t *testing.T) {
		source := oversizedSource()
		huge := strings.Repeat("超出总预算的保护内容", 700)
		source.OpenQuestions = agentcontext.OpenQuestionsSnapshot{
			Questions:     []string{huge},
			HumanRequests: []string{"需要人工确认"},
		}
		briefing := buildBriefing(t, source, 78)
		rendered := briefing.Render()

		if got := briefingTokens(rendered); got <= agentcontext.BriefingTokenBudget {
			t.Fatalf("documented exception not exercised: render uses %d tokens, want > %d", got, agentcontext.BriefingTokenBudget)
		}
		if open := renderedSection(rendered, "未决"); !strings.Contains(open, huge) {
			t.Fatal("protected open questions were truncated in the exception render")
		}
		// In the exception the six regular groups are left at their standalone
		// per-section caps instead of being squeezed.
		for _, section := range briefingSectionCaps() {
			body := renderedSection(rendered, section.title)
			if body == "" {
				t.Fatalf("section %q is missing in the exception render:\n%s", section.title, rendered)
			}
			if got := briefingTokens(body); got > section.budget {
				t.Fatalf("section %q uses %d tokens, want <= %d", section.title, got, section.budget)
			}
		}
		if rendered != briefing.Render() {
			t.Fatal("exception render is not deterministic")
		}
	})
}

// TestBriefingProtectedBudgetBoundary pins the exact boundary of the documented
// exception from both sides. While the protected content still leaves a rune
// for every regular section marker, Render squeezes the regular groups and the
// total stays inside the envelope; one rune more of protected content pushes
// the protected content alone over the envelope, and Render returns the
// per-section-truncated text unchanged. The fixture deliberately has no plan or
// findings items: their mandatory current/next and top-3 prefixes are covered
// by TestBriefingKeepsCurrentAndNextPlanItems and TestBriefingKeepsTop3Facts,
// so this test isolates the goal/stage and open-questions protection.
func TestBriefingProtectedBudgetBoundary(t *testing.T) {
	build := func(openRunes int) agentcontext.Briefing {
		t.Helper()
		source := boundarySource()
		source.OpenQuestions = agentcontext.OpenQuestionsSnapshot{
			Questions: []string{strings.Repeat("边", openRunes)},
		}
		return buildBriefing(t, source, 79)
	}
	renderedTokens := func(openRunes int) int {
		return briefingTokens(build(openRunes).Render())
	}

	// The boundary is found by binary search because the rendered size is
	// monotone non-decreasing in the protected content: Render either fits the
	// envelope exactly or, once the protected content alone is too large,
	// returns text that only grows.
	const upper = 8192
	if got := renderedTokens(upper); got <= agentcontext.BriefingTokenBudget {
		t.Fatalf("fixture precondition: protected content of %d runes uses only %d tokens", upper, got)
	}
	low, high := 0, upper
	for low < high {
		middle := low + (high-low)/2
		if renderedTokens(middle) > agentcontext.BriefingTokenBudget {
			high = middle
		} else {
			low = middle + 1
		}
	}
	over := low
	if over <= 0 {
		t.Fatalf("no protected-content boundary found below %d runes", upper)
	}

	// Just inside the boundary the regular groups are squeezed to their
	// one-rune markers so the total still fits.
	preBoundary := build(over - 1)
	inside := preBoundary.Render()
	if got := briefingTokens(inside); got > agentcontext.BriefingTokenBudget {
		t.Fatalf("render at the last in-budget protected size uses %d tokens, want <= %d", got, agentcontext.BriefingTokenBudget)
	}
	regularTokens := 0
	for _, section := range briefingSectionCaps() {
		regularTokens += briefingTokens(renderedSection(inside, section.title))
	}
	if regularTokens >= totalSectionBudgetTokens() {
		t.Fatalf("regular sections use %d tokens just inside the boundary; they were not squeezed", regularTokens)
	}

	// One rune past the boundary only the protected content can be responsible:
	// Render returns the per-section-truncated text unchanged.
	atBoundary := build(over)
	rendered := atBoundary.Render()
	if got := briefingTokens(rendered); got <= agentcontext.BriefingTokenBudget {
		t.Fatalf("render at the boundary uses %d tokens, want > %d", got, agentcontext.BriefingTokenBudget)
	}
	protectedOnly := agentcontext.Briefing{
		Goal:          atBoundary.Goal,
		Stage:         atBoundary.Stage,
		OpenQuestions: atBoundary.OpenQuestions,
	}
	if got := briefingTokens(protectedOnly.Render()); got <= agentcontext.BriefingTokenBudget {
		t.Fatalf("exception triggered although the protected content alone uses only %d tokens", got)
	}
	if got, want := renderedSection(rendered, "未决"), renderedSection(protectedOnly.Render(), "未决"); got != want {
		t.Fatalf("protected open questions changed at the exception boundary:\n%s\n---\n%s", got, want)
	}
}

// TestBriefingPlanItemsCarryIDAndRevision pins the plan.update input contract
// (R-A8-2): every rendered plan item exposes the id and revision the Host must
// send back, so the model never has to guess an item id or present a stale
// revision.
func TestBriefingPlanItemsCarryIDAndRevision(t *testing.T) {
	source := agentcontext.Source{
		Plan: []agentcontext.PlanItemSnapshot{
			{ID: 42, Title: "生成用例草稿", Status: "in_progress", Revision: 3},
			{ID: 43, Title: "补充边界用例", Status: "pending", Revision: 1},
		},
	}
	rendered := buildBriefing(t, source, 127).Render()
	plan := renderedSection(rendered, "计划")
	for _, want := range []string{
		"当前：[in_progress] 生成用例草稿 [id=42 revision=3]",
		"下一步：[pending] 补充边界用例 [id=43 revision=1]",
	} {
		if !strings.Contains(plan, want) {
			t.Fatalf("plan section missing %q:\n%s", want, plan)
		}
	}
}

// TestBriefingInjectsClarificationHandoff pins the clarification→design handoff
// pack injection (R-B5-2): the Design Host briefing carries the artifact
// revision and its document, and a version without a handoff renders no empty
// handoff group (older briefings stay unchanged).
func TestBriefingInjectsClarificationHandoff(t *testing.T) {
	document := `{"requirements":[{"id":501,"title":"登录"}],"open":[{"title":"短信通道"}]}`
	source := agentcontext.Source{
		Version: agentcontext.VersionSnapshot{Goal: "设计登录版本", Stage: "designing"},
		Handoff: &agentcontext.HandoffSnapshot{
			Revision:    4,
			GeneratedAt: "2026-11-04T09:00:00Z",
			Document:    document,
		},
	}
	rendered := buildBriefing(t, source, 128).Render()
	handoff := renderedSection(rendered, "澄清交接包")
	for _, want := range []string{"revision=4", "2026-11-04T09:00:00Z", "短信通道"} {
		if !strings.Contains(handoff, want) {
			t.Fatalf("handoff group missing %q:\n%s", want, rendered)
		}
	}
	without := buildBriefing(t, agentcontext.Source{
		Version: agentcontext.VersionSnapshot{Goal: "设计登录版本", Stage: "designing"},
	}, 129).Render()
	if strings.Contains(without, "澄清交接包") {
		t.Fatalf("a version without a handoff rendered the handoff group:\n%s", without)
	}
}
