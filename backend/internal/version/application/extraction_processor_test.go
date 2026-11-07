package versionapp_test

// P0 FA-5 fix: the deterministic W0 extraction processor must also support the
// R/AC heading scheme used by the FA-5 typical input (R01..R12, with
// AC-R01-01 sub-items), not only the FR-### scheme. The real stack uploaded
// typical-section1.md, all 13 segments finished done, but 0 requirements were
// extracted because the stopgap only recognized FR-\d{3,}.

import (
	"context"
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
