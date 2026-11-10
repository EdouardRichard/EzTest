package versionapp_test

// P0 FA-5 fix: the deterministic W0 extraction processor must also support the
// R/AC heading scheme used by the FA-5 typical input (R01..R12, with
// AC-R01-01 sub-items), not only the FR-### scheme. The real stack uploaded
// typical-section1.md, all 13 segments finished done, but 0 requirements were
// extracted because the stopgap only recognized FR-\d{3,}.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	agenttools "github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// rHeadingSectionTitles mirrors the 12 requirement headings of the FA-5
// typical-section1.md input.
var rHeadingSectionTitles = []string{
	"账号密码登录",
	"页面访问控制与权限跳转",
	"博客列表浏览与分页",
	"关键词搜索",
	"文章详情阅读与目录",
	"素材上传与光标插入",
	"保存草稿与发布",
	"后台文章管理与筛选",
	"标签筛选（单标签与多标签）",
	"定时发布与发布日历",
	"收藏、点赞与阅读进度",
	"搜索增强（建议、高亮、历史）",
}

// rHeadingACCounts sums to the 41 acceptance criteria of the FA-5 input.
var rHeadingACCounts = []int{3, 4, 3, 4, 4, 3, 3, 4, 3, 3, 3, 4}

// rHeadingRequirementDocument builds the FA-5 input shape: one Markdown
// document with R01..R12 headings (mixed ## / ### levels) and AC-Rxx-yy
// sub-items. Body lines only mention R12 as a cross reference, which must not
// become a requirement.
func rHeadingRequirementDocument() string {
	var builder strings.Builder
	builder.WriteString("## 1. 验收输入集\n\n")
	builder.WriteString("> 本次需求输入共 12 条（R01–R12），共 41 个验收标准（AC）；正文提到 R12 不代表新增需求。\n\n")
	for index, title := range rHeadingSectionTitles {
		id := fmt.Sprintf("R%02d", index+1)
		level := "###"
		if index == 0 {
			// FA-5 mixes heading levels; the declared id is the heading text.
			level = "##"
		}
		fmt.Fprintf(&builder, "%s %s %s\n\n", level, id, title)
		fmt.Fprintf(&builder, "**需求描述**：%s 的需求描述；正文提到 R12 只是交叉引用。\n\n", title)
		builder.WriteString("**验收标准**：\n\n")
		for ac := 1; ac <= rHeadingACCounts[index]; ac++ {
			fmt.Fprintf(&builder, "- **AC-%s-%02d 场景 %d（正向）**\n", id, ac, ac)
			builder.WriteString("  - Given（假如）：前置条件已满足\n")
			builder.WriteString("  - When（当）：执行被测操作\n")
			builder.WriteString("  - Then（那么）：得到预期结果\n")
		}
		builder.WriteString("\n")
	}
	builder.WriteString("**输入边界**：以上 12 条即完整输入。\n")
	return builder.String()
}

// recordingExtractionSink captures every requirement the deterministic
// processor emits, so the test asserts the real extraction contract instead of
// an internal helper.
type recordingExtractionSink struct {
	requirements []versionapp.ExtractionRequirement
	sources      []versionapp.ExtractionSource
}

var _ agenttools.ExtractionSink = (*recordingExtractionSink)(nil)

func (s *recordingExtractionSink) EmitExtractedRequirements(_ context.Context, source versionapp.ExtractionSource, requirements []versionapp.ExtractionRequirement) (versionapp.ExtractionEmitCounts, error) {
	s.sources = append(s.sources, source)
	s.requirements = append(s.requirements, requirements...)
	return versionapp.ExtractionEmitCounts{Created: len(requirements)}, nil
}

// TestDeterministicProcessorExtractsRHeadingRequirements is the RED test for
// the FA-5 defect: a document declared as R01..R12 yields exactly one
// requirement per R heading, with AC-Rxx-yy kept as sub-items (never separate
// requirements).
func TestDeterministicProcessorExtractsRHeadingRequirements(t *testing.T) {
	document := rHeadingRequirementDocument()
	segments, err := versionapp.SegmentExtraction(
		versionapp.ExtractionResult{Text: document},
		versionapp.SegmentOptions{SourcePath: "typical-section1.md"},
	)
	if err != nil {
		t.Fatalf("SegmentExtraction: %v", err)
	}
	if len(segments) < 12 {
		t.Fatalf("segments = %d, want at least one per R heading", len(segments))
	}

	source := &pipelineExtractionSource{
		input: versionapp.ExtractionStartInput{
			VersionID: 7, AttachmentID: 3, InputSHA256: testSHA(document),
			Segments: segments, DocumentText: document, AttachmentName: "typical-section1.md",
		},
		document: versionapp.ExtractionDocument{Name: "typical-section1.md", Text: document},
	}
	sink := &recordingExtractionSink{}
	processor := versionapp.NewDeterministicExtractionProcessor(source, sink)
	run := versionapp.ExtractionRun{ID: 9001, VersionID: 7, AttachmentID: 3, InputSHA256: source.input.InputSHA256}
	for _, segment := range segments {
		if _, err := processor.ProcessSegment(context.Background(), run, versionapp.ExtractionSegment{
			Seq: segment.Seq, TextSHA256: segment.TextSHA256,
		}); err != nil {
			t.Fatalf("ProcessSegment(%d): %v", segment.Seq, err)
		}
	}

	if len(sink.requirements) != 12 {
		t.Fatalf("extracted %d requirements, want 12: %#v", len(sink.requirements), sink.requirements)
	}
	for index, requirement := range sink.requirements {
		wantID := fmt.Sprintf("R%02d", index+1)
		if requirement.ExternalID != wantID {
			t.Fatalf("requirement[%d].ExternalID = %q, want %q", index, requirement.ExternalID, wantID)
		}
		if strings.HasPrefix(requirement.ExternalID, "AC-") {
			t.Fatalf("acceptance criterion %q was extracted as a standalone requirement", requirement.ExternalID)
		}
		if requirement.Title != rHeadingSectionTitles[index] {
			t.Fatalf("requirement[%d].Title = %q, want %q", index, requirement.Title, rHeadingSectionTitles[index])
		}
	}

	// R01 must carry its section body and AC text as the verbatim source quote
	// (R-A7-4), and its acceptance criteria must stay attached to R01.
	first := sink.requirements[0]
	if !strings.Contains(first.SourceQuote, "账号密码登录") || !strings.Contains(first.SourceQuote, "AC-R01-01") {
		t.Fatalf("R01 source_quote does not include the section body/AC text: %q", first.SourceQuote)
	}
	if len(first.AcceptanceCriteria) == 0 || !strings.Contains(strings.Join(first.AcceptanceCriteria, "\n"), "AC-R01-02") {
		t.Fatalf("R01 acceptance_criteria = %#v, want AC-R01-01/AC-R01-02/AC-R01-03", first.AcceptanceCriteria)
	}
}

// processDeterministicDocument chunks one parsed document text, runs the
// deterministic processor over every segment and returns what the real sink
// received. It keeps the table/BDD tests on the production contract instead of
// an internal helper.
func processDeterministicDocument(t *testing.T, document string) []versionapp.ExtractionRequirement {
	t.Helper()
	segments, err := versionapp.SegmentExtraction(
		versionapp.ExtractionResult{Text: document},
		versionapp.SegmentOptions{SourcePath: "requirements.md"},
	)
	if err != nil {
		t.Fatalf("SegmentExtraction: %v", err)
	}
	source := &pipelineExtractionSource{
		input: versionapp.ExtractionStartInput{
			VersionID: 7, AttachmentID: 3, InputSHA256: testSHA(document),
			Segments: segments, DocumentText: document, AttachmentName: "requirements.md",
		},
		document: versionapp.ExtractionDocument{Name: "requirements.md", Text: document},
	}
	sink := &recordingExtractionSink{}
	processor := versionapp.NewDeterministicExtractionProcessor(source, sink)
	run := versionapp.ExtractionRun{ID: 9002, VersionID: 7, AttachmentID: 3, InputSHA256: source.input.InputSHA256}
	for _, segment := range segments {
		if _, err := processor.ProcessSegment(context.Background(), run, versionapp.ExtractionSegment{
			Seq: segment.Seq, TextSHA256: segment.TextSHA256,
		}); err != nil {
			t.Fatalf("ProcessSegment(%d): %v", segment.Seq, err)
		}
	}
	return sink.requirements
}

// TestDeterministicProcessorIgnoresAliasedFRIDs is the C-03 regression: NFR
// ids and AC sub-items alias an FR id when the pattern is not word anchored,
// and a body line that merely references an FR id is not a declaration.
func TestDeterministicProcessorIgnoresAliasedFRIDs(t *testing.T) {
	document := `## 非功能需求

NFR-001 性能要求
AC-FR-001-01 场景
正文引用 FR-001 不是声明
| FR-777 | 没有表头的残片 |
`
	requirements := processDeterministicDocument(t, document)
	if len(requirements) != 0 {
		t.Fatalf("aliased/non-declaration ids produced requirements: %#v", requirements)
	}
}

// TestDeterministicProcessorTableColumns covers the 6.4 catalog shape: the
// title comes from the 需求名称 column (never the raw Markdown row), the
// description is a labeled summary (there is no 需求描述 column), a numeric
// 验收场景数 is not an acceptance criterion, and the unmapped columns land in
// constraints_json.
func TestDeterministicProcessorTableColumns(t *testing.T) {
	document := `# 6.4 延展功能需求清单

| 需求编号 | 需求名称 | 所属功能 | 优先级 | 验收场景数 | 当前系统状态 |
| --- | --- | --- | --- | --- | --- |
| FR-131 | 内容排期与工作量看板 | 内容管理 | P2 | 2 | 不存在 |
`
	requirements := processDeterministicDocument(t, document)
	if len(requirements) != 1 {
		t.Fatalf("table requirements = %#v, want exactly FR-131", requirements)
	}
	requirement := requirements[0]
	if requirement.ExternalID != "FR-131" {
		t.Fatalf("ExternalID = %q", requirement.ExternalID)
	}
	if requirement.Title != "内容排期与工作量看板" {
		t.Fatalf("Title = %q, want the 需求名称 cell", requirement.Title)
	}
	if strings.Contains(requirement.Title, "|") || strings.Contains(requirement.Description, "|") {
		t.Fatalf("table row leaked into title/description: %#v", requirement)
	}
	if strings.Contains(requirement.Description, "FR-131") || strings.Contains(requirement.Description, "内容管理 |") {
		t.Fatalf("description is the raw table row: %q", requirement.Description)
	}
	if !strings.Contains(requirement.Description, "所属功能") && !strings.Contains(requirement.Description, "内容管理") {
		t.Fatalf("description is not a labeled summary: %q", requirement.Description)
	}
	if len(requirement.AcceptanceCriteria) != 0 {
		t.Fatalf("numeric 验收场景数 became acceptance criteria: %#v", requirement.AcceptanceCriteria)
	}
	if requirement.Priority != "P2" {
		t.Fatalf("Priority = %q, want P2", requirement.Priority)
	}
	if requirement.Category != "planned" {
		t.Fatalf("Category = %q, want planned for 当前系统状态=不存在", requirement.Category)
	}
	var constraints map[string]any
	if err := json.Unmarshal(requirement.ConstraintsJSON, &constraints); err != nil {
		t.Fatalf("constraints_json = %q: %v", requirement.ConstraintsJSON, err)
	}
	if len(constraints) != 2 {
		t.Fatalf("constraints = %#v, want module + ac_count", constraints)
	}
	if constraints["module"] != "内容管理" || constraints["ac_count"] != float64(2) {
		t.Fatalf("constraints = %#v, want module=内容管理 ac_count=2", constraints)
	}
}

// TestDeterministicProcessorBodyBDD covers the FR body declaration: metadata
// labels are concatenated, the description stops at 用户故事/验收标准, and the
// BDD 场景 blocks become acceptance criteria.
func TestDeterministicProcessorBodyBDD(t *testing.T) {
	document := `### FR-101 账号密码登录

需求编号：FR-101所属功能：账号优先级：P1
需求描述：用户可以使用账号密码登录系统。
用户故事：作为已注册用户，我希望登录系统。
验收标准：

场景 1：正确密码登录
Given 已注册用户
When 输入正确密码
Then 登录成功

场景 2：错误密码登录
Given 已注册用户
When 输入错误密码
Then 提示密码错误
`
	requirements := processDeterministicDocument(t, document)
	if len(requirements) != 1 {
		t.Fatalf("body requirements = %#v, want FR-101", requirements)
	}
	requirement := requirements[0]
	if requirement.ExternalID != "FR-101" || requirement.Title != "账号密码登录" {
		t.Fatalf("declaration = %#v", requirement)
	}
	if requirement.Description != "用户可以使用账号密码登录系统。" {
		t.Fatalf("Description = %q", requirement.Description)
	}
	if requirement.Priority != "P1" {
		t.Fatalf("Priority = %q, want the inline metadata P1", requirement.Priority)
	}
	if len(requirement.AcceptanceCriteria) != 2 {
		t.Fatalf("AcceptanceCriteria = %#v, want two 场景 blocks", requirement.AcceptanceCriteria)
	}
	joined := strings.Join(requirement.AcceptanceCriteria, "\n")
	for _, want := range []string{"场景 1", "场景 2", "Given", "Then"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("AC missing %q: %#v", want, requirement.AcceptanceCriteria)
		}
	}
	if requirement.Confidence < 0.6 {
		t.Fatalf("confidence = %v despite extracted AC", requirement.Confidence)
	}
}

// TestDeterministicProcessorBodyMissingAC covers the low-confidence fallback:
// an FR body without 验收标准 is marked needs_review instead of silently
// looking complete.
func TestDeterministicProcessorBodyMissingAC(t *testing.T) {
	document := `### FR-102 退出登录

需求描述：用户可以退出登录。
`
	requirements := processDeterministicDocument(t, document)
	if len(requirements) != 1 {
		t.Fatalf("requirements = %#v", requirements)
	}
	requirement := requirements[0]
	if len(requirement.AcceptanceCriteria) != 0 {
		t.Fatalf("AcceptanceCriteria = %#v", requirement.AcceptanceCriteria)
	}
	if requirement.Confidence >= 0.6 {
		t.Fatalf("confidence = %v, want below 0.6", requirement.Confidence)
	}
	var constraints map[string]any
	if err := json.Unmarshal(requirement.ConstraintsJSON, &constraints); err != nil {
		t.Fatalf("constraints_json = %q: %v", requirement.ConstraintsJSON, err)
	}
	if constraints["needs_review"] != true || strings.TrimSpace(fmt.Sprint(constraints["reason"])) == "" {
		t.Fatalf("constraints = %#v, want needs_review + reason", constraints)
	}
}

// TestDeterministicProcessorTableTitleCapped pins the guardrail against an
// unbounded catalog title: a long 需求名称 cell is truncated to 200 runes so one
// row cannot blow the requirement title/validation limits.
func TestDeterministicProcessorTableTitleCapped(t *testing.T) {
	longTitle := strings.Repeat("长", 260)
	document := "# 6.4 延展功能需求清单\n\n" +
		"| 需求编号 | 需求名称 | 所属功能 | 优先级 | 验收场景数 | 当前系统状态 |\n" +
		"| --- | --- | --- | --- | --- | --- |\n" +
		"| FR-331 | " + longTitle + " | 内容管理 | P2 | 2 | 不存在 |\n"
	requirements := processDeterministicDocument(t, document)
	if len(requirements) != 1 {
		t.Fatalf("requirements = %#v, want FR-331", requirements)
	}
	if runes := []rune(requirements[0].Title); len(runes) != 200 {
		t.Fatalf("title runes = %d, want capped 200", len(runes))
	}
}

// TestDeterministicProcessorTableFallbackReasons pins the two low-confidence
// title fallbacks apart: an unmappable header and an empty title cell must each
// keep needs_review but explain the actual reason.
func TestDeterministicProcessorTableFallbackReasons(t *testing.T) {
	unmapped := `# 6.4 延展功能需求清单

| 甲列 | 乙列 | 丙列 |
| --- | --- | --- |
| FR-341 | 备用标题 | 内容管理 |
`
	requirements := processDeterministicDocument(t, unmapped)
	if len(requirements) != 1 {
		t.Fatalf("unmapped-header requirements = %#v", requirements)
	}
	requirement := requirements[0]
	if requirement.Title != "备用标题" {
		t.Fatalf("unmapped-header title = %q, want the second column", requirement.Title)
	}
	var constraints map[string]any
	if err := json.Unmarshal(requirement.ConstraintsJSON, &constraints); err != nil {
		t.Fatalf("constraints_json = %q: %v", requirement.ConstraintsJSON, err)
	}
	reason := fmt.Sprint(constraints["reason"])
	if constraints["needs_review"] != true || !strings.Contains(reason, "header could not be mapped") {
		t.Fatalf("unmapped-header constraints = %#v, want a header reason", constraints)
	}
	if requirement.Confidence >= 0.6 {
		t.Fatalf("unmapped-header confidence = %v, want low confidence", requirement.Confidence)
	}

	emptyTitle := `# 6.1 需求清单总表

| 需求编号 | 需求名称 | 所属功能 |
| --- | --- | --- |
| FR-342 |  | 内容管理 |
`
	requirements = processDeterministicDocument(t, emptyTitle)
	if len(requirements) != 1 {
		t.Fatalf("empty-title requirements = %#v", requirements)
	}
	requirement = requirements[0]
	if requirement.Title != "内容管理" {
		t.Fatalf("empty-title fallback = %q, want the first usable cell", requirement.Title)
	}
	if err := json.Unmarshal(requirement.ConstraintsJSON, &constraints); err != nil {
		t.Fatalf("constraints_json = %q: %v", requirement.ConstraintsJSON, err)
	}
	reason = fmt.Sprint(constraints["reason"])
	if constraints["needs_review"] != true || !strings.Contains(reason, "title column is empty") {
		t.Fatalf("empty-title constraints = %#v, want a title-column reason", constraints)
	}
}
