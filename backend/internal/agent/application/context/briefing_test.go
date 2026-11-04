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
		"- [pending] 生成用例草稿（依赖：确认需求）",
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

// TestBriefingBudget pins spec §6.2: the render stays inside the 2.5k token
// envelope, over-budget sections are truncated per section, and the goal,
// stage and open questions are never truncated.
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
		{"计划", 300},
		{"发现", 500},
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
			Title:     strings.Repeat("计划项", 40),
			Status:    "pending",
			DependsOn: []string{"确认需求"},
		})
		source.Findings = append(source.Findings, agentcontext.FactSnapshot{
			ID:         int64(index + 1),
			Kind:       "page",
			Key:        fmt.Sprintf("key-%d", index),
			Summary:    strings.Repeat("发现事实", 40),
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

// briefingTokens is the independent test-side token estimate from spec §6.3:
// at least 0.6 tokens per rune (rounded up), the conservative coefficient.
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
		{"计划", 300},
		{"发现", 500},
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
// questions are large enough to break the 2.5k total on their own when combined
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

// TestBriefingTotalBudget pins the total envelope from spec §6.2. The
// per-section caps alone (600+300+300+500+200+200 = 2100) plus a large
// protected open-questions group can exceed 2500, so Render must squeeze the six
// regular groups until the whole text fits, while never cutting the goal, stage
// or open questions. If the protected content plus the fixed headings and the
// one-rune marker each regular group needs already exceeds the envelope, the
// render is returned unchanged: that is the documented, unavoidable exception.
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
// per-section-truncated text unchanged.
func TestBriefingProtectedBudgetBoundary(t *testing.T) {
	build := func(openRunes int) agentcontext.Briefing {
		t.Helper()
		source := oversizedSource()
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
