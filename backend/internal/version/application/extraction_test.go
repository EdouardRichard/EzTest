package versionapp_test

// TDD contract for v0.22 A-7 structural segmentation (R-A7-2/4):
//   - split parsed ExtractionResult by source structure (heading/slide/sheet/
//     page) and keep heading_path + source positions;
//   - target 4,000-8,000 runes, oversize paragraphs split again;
//   - never merge across headings;
//   - prefix = source path + previous segment tail (200 runes), never copied
//     into text;
//   - tables become Markdown text, embedded images become image_refs[] plus a
//     position marker, and no parsed element is dropped.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

const testDocxSource = "requirements.docx"

// testDocxElements is a DOCX-like parse result: two heading sections with
// Chinese text, where the second section must never be merged into the first.
func testDocxElements() []versionapp.ExtractionElement {
	return []versionapp.ExtractionElement{
		{Kind: "heading", Text: "3.1 概述", Level: 1, HeadingPath: "3.1 概述", Source: "word/document.xml"},
		{Kind: "paragraph", Text: strings.Repeat("概述内容", 700), HeadingPath: "3.1 概述", Source: "word/document.xml"},
		{Kind: "heading", Text: "3.2 用户登录", Level: 2, HeadingPath: "3.2 用户登录", Source: "word/document.xml"},
		{Kind: "paragraph", Text: "手机号登录：用户输入 11 位手机号与 6 位验证码后登录。", HeadingPath: "3.2 用户登录", Source: "word/document.xml"},
		{Kind: "list_item", Text: "验证码 60 秒内有效。", HeadingPath: "3.2 用户登录", Source: "word/document.xml"},
	}
}

func runeCount(value string) int { return utf8.RuneCountInString(value) }

// lastRunes returns the last n runes of value (the documented prefix tail).
func lastRunes(value string, n int) string {
	runes := []rune(value)
	if len(runes) <= n {
		return string(runes)
	}
	return string(runes[len(runes)-n:])
}

// joinedSegmentTexts concatenates segment texts without separators, which is
// how the "no content dropped" assertion walks the parsed elements.
func joinedSegmentTexts(segments []versionapp.Segment) string {
	var builder strings.Builder
	for _, segment := range segments {
		builder.WriteString(segment.Text)
	}
	return builder.String()
}

func segmentTextList(segments []versionapp.Segment) []string {
	texts := make([]string, 0, len(segments))
	for _, segment := range segments {
		texts = append(texts, segment.Text)
	}
	return texts
}

func TestSegmentationByHeading(t *testing.T) {
	elements := testDocxElements()
	texts := make([]string, 0, len(elements))
	for _, element := range elements {
		texts = append(texts, element.Text)
	}
	result := versionapp.ExtractionResult{
		Text:     strings.Join(texts, "\n"),
		Elements: elements,
	}
	segments, err := versionapp.SegmentExtraction(result, versionapp.SegmentOptions{SourcePath: testDocxSource})
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 2 {
		t.Fatalf("segments = %d (%+v), want 2 (one per heading section)", len(segments), segments)
	}
	if segments[1].HeadingPath != "3.2 用户登录" {
		t.Fatalf("second heading_path = %q, want %q", segments[1].HeadingPath, "3.2 用户登录")
	}
	if segments[0].Seq != 1 || segments[1].Seq != 2 {
		t.Fatalf("seq = %d/%d, want 1/2", segments[0].Seq, segments[1].Seq)
	}
	// The second segment must not absorb the first heading and vice versa.
	if strings.Contains(segments[0].Text, "用户登录") {
		t.Fatalf("first segment merged the second heading: %q", segments[0].Text)
	}
	if strings.Contains(segments[1].Text, "3.1 概述") {
		t.Fatalf("second segment merged the first heading: %q", segments[1].Text)
	}
	// Prefix = source path + previous segment tail (200 runes), never in text.
	tail := lastRunes(segments[0].Text, 200)
	if !strings.Contains(segments[1].Prefix, testDocxSource) {
		t.Fatalf("prefix %q does not carry the source path %q", segments[1].Prefix, testDocxSource)
	}
	if !strings.HasSuffix(segments[1].Prefix, tail) {
		t.Fatalf("prefix %q does not end with the previous 200-rune tail %q", segments[1].Prefix, tail)
	}
	if !strings.Contains(segments[0].Prefix, testDocxSource) {
		t.Fatalf("first prefix %q does not carry the source path", segments[0].Prefix)
	}
	if strings.Contains(segments[1].Text, tail) {
		t.Fatalf("previous tail was duplicated into the segment text")
	}
	// Every parsed content element must be accounted for.
	all := joinedSegmentTexts(segments)
	for _, element := range elements {
		if !strings.Contains(all, element.Text) {
			t.Fatalf("element %q was dropped from the segments", element.Text)
		}
	}
	// Positions and hash are deterministic and coherent.
	if segments[0].CharStart != 0 {
		t.Fatalf("first char_start = %d, want 0", segments[0].CharStart)
	}
	if segments[1].CharStart <= segments[0].CharEnd {
		t.Fatalf("char ranges overlap: [%d,%d) then [%d,%d)",
			segments[0].CharStart, segments[0].CharEnd, segments[1].CharStart, segments[1].CharEnd)
	}
	sum := sha256.Sum256([]byte(segments[1].Text))
	if want := hex.EncodeToString(sum[:]); segments[1].TextSHA256 != want {
		t.Fatalf("text_sha256 = %q, want %q", segments[1].TextSHA256, want)
	}
	for _, segment := range segments {
		if segment.CharEnd-segment.CharStart != runeCount(segment.Text) {
			t.Fatalf("segment %d range [%d,%d) does not match its %d runes",
				segment.Seq, segment.CharStart, segment.CharEnd, runeCount(segment.Text))
		}
	}
	again, err := versionapp.SegmentExtraction(result, versionapp.SegmentOptions{SourcePath: testDocxSource})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(segments, again) {
		t.Fatalf("segmentation is not deterministic:\n%+v\n%+v", segments, again)
	}
}

func TestSegmentationSplitsOversizeParagraph(t *testing.T) {
	paragraph := strings.Repeat("超长段落内容", 2500) // 15,000 runes
	result := versionapp.ExtractionResult{
		Text: "1 概述\n" + paragraph,
		Elements: []versionapp.ExtractionElement{
			{Kind: "heading", Text: "1 概述", Level: 1, HeadingPath: "1 概述", Source: "word/document.xml"},
			{Kind: "paragraph", Text: paragraph, HeadingPath: "1 概述", Source: "word/document.xml"},
		},
	}
	segments, err := versionapp.SegmentExtraction(result, versionapp.SegmentOptions{SourcePath: testDocxSource})
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) < 2 {
		t.Fatalf("segments = %d, want the oversize paragraph split again", len(segments))
	}
	for _, segment := range segments {
		if got := runeCount(segment.Text); got > versionapp.DefaultSegmentMaxChars {
			t.Fatalf("segment %d has %d runes, over the %d-rune maximum", segment.Seq, got, versionapp.DefaultSegmentMaxChars)
		}
	}
	if !strings.Contains(joinedSegmentTexts(segments), paragraph) {
		t.Fatalf("oversize paragraph content was dropped when splitting")
	}
	if !strings.Contains(joinedSegmentTexts(segments), "1 概述") {
		t.Fatalf("heading was dropped when splitting the paragraph")
	}
	for index := 1; index < len(segments); index++ {
		if segments[index].CharStart < segments[index-1].CharEnd {
			t.Fatalf("segment %d char range overlaps segment %d", index+1, index)
		}
	}
}

func TestSegmentationPreservesTablesAndImages(t *testing.T) {
	elements := []versionapp.ExtractionElement{
		{Kind: "heading", Text: "2.1 登录规则", HeadingPath: "2.1 登录规则", Source: "word/document.xml"},
		{Kind: "paragraph", Text: "登录规则如下。", HeadingPath: "2.1 登录规则", Source: "word/document.xml"},
		{Kind: "heading", Text: "2.2 校验规则", HeadingPath: "2.2 校验规则", Source: "word/document.xml"},
		{Kind: "table_row", Text: "场景 | 期望", Row: []string{"场景", "期望"}, HeadingPath: "2.2 校验规则", Source: "word/document.xml"},
		{Kind: "table_row", Text: "手机号为空 | 提示手机号必填", Row: []string{"手机号为空", "提示手机号必填"}, HeadingPath: "2.2 校验规则", Source: "word/document.xml"},
		{Kind: "heading", Text: "2.3 原型", HeadingPath: "2.3 原型", Source: "word/document.xml"},
		{Kind: "paragraph", Text: "登录页原型见下图。", HeadingPath: "2.3 原型", Source: "word/document.xml"},
	}
	result := versionapp.ExtractionResult{
		Elements: elements,
		Images: []versionapp.ExtractionImage{
			{Seq: 1, Name: "login-page.png", Source: "word/media/image1.png", MIME: "image/png"},
		},
	}
	segments, err := versionapp.SegmentExtraction(result, versionapp.SegmentOptions{SourcePath: testDocxSource})
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 3 {
		t.Fatalf("segments = %d (%+v), want 3", len(segments), segments)
	}
	tableSegment := segments[1]
	if tableSegment.Kind != versionapp.SegmentKindTable {
		t.Fatalf("table segment kind = %q, want %q", tableSegment.Kind, versionapp.SegmentKindTable)
	}
	if !strings.Contains(tableSegment.Text, "| 场景 | 期望 |") {
		t.Fatalf("table header marker missing from segment text: %q", tableSegment.Text)
	}
	for _, cell := range []string{"手机号为空", "提示手机号必填"} {
		if !strings.Contains(tableSegment.Text, cell) {
			t.Fatalf("table cell %q was dropped: %q", cell, tableSegment.Text)
		}
	}
	if len(tableSegment.ImageRefs) != 0 {
		t.Fatalf("table segment has unexpected image refs: %v", tableSegment.ImageRefs)
	}
	imageSegment := segments[2]
	if !reflect.DeepEqual(imageSegment.ImageRefs, []string{"login-page.png"}) {
		t.Fatalf("image refs = %v, want [login-page.png]", imageSegment.ImageRefs)
	}
	if !strings.Contains(imageSegment.Text, "[图片: login-page.png]") {
		t.Fatalf("image position marker missing from segment text: %q", imageSegment.Text)
	}
	if len(segments[0].ImageRefs) != 0 {
		t.Fatalf("image marker landed in the wrong segment: %v", segments[0].ImageRefs)
	}
}

func TestSegmentationPPTXSlideAndXLSXSheetStructure(t *testing.T) {
	pptx := versionapp.ExtractionResult{
		Elements: []versionapp.ExtractionElement{
			{Kind: "slide", Text: "Slide 1", Slide: 1, Source: "ppt/slides/slide1.xml"},
			{Kind: "paragraph", Text: "登录页", Slide: 1, Source: "ppt/slides/slide1.xml"},
			{Kind: "slide", Text: "Slide 2", Slide: 2, Source: "ppt/slides/slide2.xml"},
			{Kind: "paragraph", Text: "支付页", Slide: 2, Source: "ppt/slides/slide2.xml"},
		},
	}
	segments, err := versionapp.SegmentExtraction(pptx, versionapp.SegmentOptions{SourcePath: "design.pptx"})
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 2 {
		t.Fatalf("pptx segments = %d (%+v), want one per slide", len(segments), segments)
	}
	if segments[0].HeadingPath != "Slide 1" || segments[1].HeadingPath != "Slide 2" {
		t.Fatalf("pptx heading paths = %q/%q, want Slide 1/Slide 2", segments[0].HeadingPath, segments[1].HeadingPath)
	}
	if !strings.Contains(segments[0].Text, "登录页") || !strings.Contains(segments[1].Text, "支付页") {
		t.Fatalf("pptx slide order/content lost: %+v", segmentTextList(segments))
	}

	xlsx := versionapp.ExtractionResult{
		Elements: []versionapp.ExtractionElement{
			{Kind: "sheet", Text: "登录", Sheet: "登录", Source: "xl/worksheets/sheet1.xml"},
			{Kind: "sheet_row", Text: "手机号\t验证码", Row: []string{"手机号", "验证码"}, Sheet: "登录", Source: "xl/worksheets/sheet1.xml"},
			{Kind: "sheet_row", Text: "13800000000\t123456", Row: []string{"13800000000", "123456"}, Sheet: "登录", Source: "xl/worksheets/sheet1.xml"},
			{Kind: "sheet", Text: "支付", Sheet: "支付", Source: "xl/worksheets/sheet2.xml"},
			{Kind: "sheet_row", Text: "订单号\t金额", Row: []string{"订单号", "金额"}, Sheet: "支付", Source: "xl/worksheets/sheet2.xml"},
		},
	}
	segments, err = versionapp.SegmentExtraction(xlsx, versionapp.SegmentOptions{SourcePath: "cases.xlsx"})
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 2 {
		t.Fatalf("xlsx segments = %d (%+v), want one per sheet", len(segments), segments)
	}
	if segments[0].HeadingPath != "Sheet: 登录" || segments[1].HeadingPath != "Sheet: 支付" {
		t.Fatalf("xlsx heading paths = %q/%q", segments[0].HeadingPath, segments[1].HeadingPath)
	}
	for _, value := range []string{"登录", "13800000000", "支付", "订单号"} {
		if !strings.Contains(joinedSegmentTexts(segments), value) {
			t.Fatalf("xlsx value %q was dropped", value)
		}
	}
	// XLSX rows are table rows: they must render as Markdown tables (not the
	// parser's tab-separated Text) and the row-only sheet segment is table kind.
	for _, segment := range segments {
		if segment.Kind != versionapp.SegmentKindTable {
			t.Fatalf("xlsx segment %d kind = %q, want %q (rows are a table)", segment.Seq, segment.Kind, versionapp.SegmentKindTable)
		}
	}
	if !strings.Contains(segments[0].Text, "| 手机号 | 验证码 |") {
		t.Fatalf("xlsx rows were not rendered as a Markdown table: %q", segments[0].Text)
	}
	if !strings.Contains(segments[0].Text, "13800000000") {
		t.Fatalf("xlsx row cell dropped: %q", segments[0].Text)
	}
}

// TestSegmentationXLSXOversizedRowBlockIsTableChunks covers R-A7-2 for XLSX:
// a sheet row block over the segment maximum becomes standalone kind=table
// chunks that repeat the Markdown header, and no row content is dropped.
func TestSegmentationXLSXOversizedRowBlockIsTableChunks(t *testing.T) {
	const sheetFile = "xl/worksheets/sheet1.xml"
	row := func(index int) versionapp.ExtractionElement {
		cells := []string{
			fmt.Sprintf("TC-%03d", index),
			strings.Repeat("输入数据", 100),
			strings.Repeat("预期结果", 100),
		}
		return versionapp.ExtractionElement{
			Kind: "sheet_row", Text: strings.Join(cells, "\t"), Row: cells,
			Sheet: "用例", Source: sheetFile,
		}
	}
	elements := []versionapp.ExtractionElement{
		{Kind: "sheet", Text: "用例", Sheet: "用例", Source: sheetFile},
		{Kind: "sheet_row", Text: "用例编号\t输入\t预期", Row: []string{"用例编号", "输入", "预期"}, Sheet: "用例", Source: sheetFile},
	}
	for index := 0; index < 30; index++ {
		elements = append(elements, row(index))
	}
	segments, err := versionapp.SegmentExtraction(
		versionapp.ExtractionResult{Elements: elements},
		versionapp.SegmentOptions{SourcePath: "cases.xlsx"},
	)
	if err != nil {
		t.Fatal(err)
	}
	tableSegments := 0
	joined := joinedSegmentTexts(segments)
	for _, segment := range segments {
		if segment.Kind != versionapp.SegmentKindTable {
			continue
		}
		tableSegments++
		if got := runeCount(segment.Text); got > versionapp.DefaultSegmentMaxChars {
			t.Fatalf("table chunk %d has %d runes, over the %d-rune maximum", segment.Seq, got, versionapp.DefaultSegmentMaxChars)
		}
		if !strings.HasPrefix(segment.Text, "| 用例编号 | 输入 | 预期 |") {
			t.Fatalf("table chunk %d does not repeat the Markdown header: %.60q", segment.Seq, segment.Text)
		}
	}
	if tableSegments < 2 {
		t.Fatalf("oversized xlsx row block produced %d kind=table chunks, want at least 2", tableSegments)
	}
	for index := 0; index < 30; index++ {
		if token := fmt.Sprintf("TC-%03d", index); !strings.Contains(joined, token) {
			t.Fatalf("xlsx row %q was dropped from the table chunks", token)
		}
	}
}

func TestSegmentationPDFPageHeadingHeuristic(t *testing.T) {
	page := "3.1 登录\n用户使用手机号登录。\n3.2 退出\n用户点击退出按钮。"
	result := versionapp.ExtractionResult{
		Elements: []versionapp.ExtractionElement{
			{Kind: "page", Text: page, Page: 1, Source: "pdf:page:1"},
		},
	}
	segments, err := versionapp.SegmentExtraction(result, versionapp.SegmentOptions{SourcePath: "requirements.pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 2 {
		t.Fatalf("segments = %d (%+v), want the two heading heuristics split", len(segments), segments)
	}
	if segments[0].HeadingPath != "Page 1 > 3.1 登录" {
		t.Fatalf("first heading path = %q", segments[0].HeadingPath)
	}
	if segments[1].HeadingPath != "Page 1 > 3.2 退出" {
		t.Fatalf("second heading path = %q", segments[1].HeadingPath)
	}
	if !strings.Contains(segments[0].Text, "用户使用手机号登录。") ||
		!strings.Contains(segments[1].Text, "用户点击退出按钮。") {
		t.Fatalf("page heading heuristic lost body text: %+v", segmentTextList(segments))
	}
	if !strings.Contains(segments[0].Text, "Page 1") {
		t.Fatalf("page position marker missing: %q", segments[0].Text)
	}
}

func TestSegmentationFillsTargetRange(t *testing.T) {
	// Two 3,000-rune paragraphs under one heading must combine into one
	// 4,000-8,000-rune segment instead of two undersized ones, and the next
	// heading must still start a new segment.
	result := versionapp.ExtractionResult{
		Elements: []versionapp.ExtractionElement{
			{Kind: "heading", Text: "1 概述", HeadingPath: "1 概述", Source: "word/document.xml"},
			{Kind: "paragraph", Text: strings.Repeat("甲", 3000), HeadingPath: "1 概述", Source: "word/document.xml"},
			{Kind: "paragraph", Text: strings.Repeat("乙", 3000), HeadingPath: "1 概述", Source: "word/document.xml"},
			{Kind: "heading", Text: "2 详情", HeadingPath: "2 详情", Source: "word/document.xml"},
			{Kind: "paragraph", Text: strings.Repeat("丙", 3000), HeadingPath: "2 详情", Source: "word/document.xml"},
			{Kind: "paragraph", Text: strings.Repeat("丁", 3000), HeadingPath: "2 详情", Source: "word/document.xml"},
		},
	}
	segments, err := versionapp.SegmentExtraction(result, versionapp.SegmentOptions{SourcePath: testDocxSource})
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 2 {
		t.Fatalf("segments = %d (%+v), want 2 target-range segments", len(segments), segments)
	}
	for _, segment := range segments {
		if runes := runeCount(segment.Text); runes < versionapp.DefaultSegmentMinChars || runes > versionapp.DefaultSegmentMaxChars {
			t.Fatalf("segment %d has %d runes, want within [%d,%d]",
				segment.Seq, runes, versionapp.DefaultSegmentMinChars, versionapp.DefaultSegmentMaxChars)
		}
	}
	if segments[0].HeadingPath != "1 概述" || segments[1].HeadingPath != "2 详情" {
		t.Fatalf("heading paths = %q/%q", segments[0].HeadingPath, segments[1].HeadingPath)
	}
}

func TestSegmentationImageOnlyResult(t *testing.T) {
	result := versionapp.ExtractionResult{
		Images: []versionapp.ExtractionImage{
			{Seq: 1, Name: "scan.png", Source: "pdf:object:1", MIME: "image/png"},
		},
	}
	segments, err := versionapp.SegmentExtraction(result, versionapp.SegmentOptions{SourcePath: "scan.pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 1 {
		t.Fatalf("segments = %d (%+v), want one image-only segment", len(segments), segments)
	}
	if segments[0].Kind != versionapp.SegmentKindImage {
		t.Fatalf("kind = %q, want %q", segments[0].Kind, versionapp.SegmentKindImage)
	}
	if !reflect.DeepEqual(segments[0].ImageRefs, []string{"scan.png"}) {
		t.Fatalf("image_refs = %v", segments[0].ImageRefs)
	}
	if !strings.Contains(segments[0].Text, "[图片: scan.png]") {
		t.Fatalf("image marker = %q", segments[0].Text)
	}
}

func TestSegmentationFallsBackToPlainText(t *testing.T) {
	text := "# 1 概述\n" + strings.Repeat("概述内容。", 600) + "\n# 2 用户登录\n用户输入手机号与验证码。"
	segments, err := versionapp.SegmentExtraction(
		versionapp.ExtractionResult{Text: text},
		versionapp.SegmentOptions{SourcePath: "requirements.md"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 2 {
		t.Fatalf("segments = %d (%+v), want the two Markdown headings split", len(segments), segments)
	}
	if segments[1].HeadingPath != "2 用户登录" {
		t.Fatalf("heading path = %q, want %q", segments[1].HeadingPath, "2 用户登录")
	}
	joined := joinedSegmentTexts(segments)
	for _, want := range []string{"1 概述", "2 用户登录", "用户输入手机号与验证码。"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("plain-text content %q dropped: %q", want, joined)
		}
	}
}

// TestSegmentationFallsBackToTextWhenElementsYieldNoBlocks is the defensive
// no-loss fallback: an element list that renders no blocks (blank/unknown
// elements) must not hide the parser's concatenated Text.
func TestSegmentationFallsBackToTextWhenElementsYieldNoBlocks(t *testing.T) {
	result := versionapp.ExtractionResult{
		Text: "# 1 概述\n用户使用手机号登录。",
		Elements: []versionapp.ExtractionElement{
			{Kind: "paragraph", Text: "   ", Source: "word/document.xml"},
			{Kind: "unknown_future_kind", Source: "word/document.xml"},
		},
	}
	segments, err := versionapp.SegmentExtraction(result, versionapp.SegmentOptions{SourcePath: "req.docx"})
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) == 0 {
		t.Fatal("elements with no renderable text hid the Text fallback: no segments")
	}
	joined := joinedSegmentTexts(segments)
	for _, want := range []string{"1 概述", "用户使用手机号登录。"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("fallback text content %q dropped: %q", want, joined)
		}
	}
}

// TestSegmentationSplitsOversizedTable covers the row-group split of a table
// over the maximum: each piece is a standalone kind=table chunk that repeats the
// Markdown header, every piece stays within the maximum and no row is lost.
func TestSegmentationSplitsOversizedTable(t *testing.T) {
	const source = "word/document.xml"
	elements := []versionapp.ExtractionElement{
		{Kind: "table_row", Text: "用例编号 | 步骤 | 预期", Row: []string{"用例编号", "步骤", "预期"}, Source: source},
	}
	for index := 0; index < 20; index++ {
		cells := []string{fmt.Sprintf("CASE-%02d", index), strings.Repeat("步骤", 400), strings.Repeat("预期", 400)}
		elements = append(elements, versionapp.ExtractionElement{
			Kind: "table_row", Text: strings.Join(cells, " | "), Row: cells, Source: source,
		})
	}
	segments, err := versionapp.SegmentExtraction(
		versionapp.ExtractionResult{Elements: elements},
		versionapp.SegmentOptions{SourcePath: "req.docx"},
	)
	if err != nil {
		t.Fatal(err)
	}
	tableSegments := 0
	joined := joinedSegmentTexts(segments)
	for _, segment := range segments {
		if segment.Kind != versionapp.SegmentKindTable {
			t.Fatalf("segment %d kind = %q, want %q", segment.Seq, segment.Kind, versionapp.SegmentKindTable)
		}
		tableSegments++
		if got := runeCount(segment.Text); got > versionapp.DefaultSegmentMaxChars {
			t.Fatalf("table chunk %d has %d runes, over the %d-rune maximum", segment.Seq, got, versionapp.DefaultSegmentMaxChars)
		}
		if !strings.HasPrefix(segment.Text, "| 用例编号 | 步骤 | 预期 |") {
			t.Fatalf("table chunk %d does not repeat the Markdown header: %.60q", segment.Seq, segment.Text)
		}
	}
	if tableSegments < 2 {
		t.Fatalf("oversized table produced %d kind=table chunks, want at least 2", tableSegments)
	}
	for index := 0; index < 20; index++ {
		if token := fmt.Sprintf("CASE-%02d", index); !strings.Contains(joined, token) {
			t.Fatalf("table row %q was dropped when splitting", token)
		}
	}
}

// TestSegmentationSplitsOversizedSingleRowTable covers the no-row-group edge
// case: a table whose only row already exceeds the maximum is split into
// standalone table text chunks that each stay within the maximum, and every
// character of the row is preserved.
func TestSegmentationSplitsOversizedSingleRowTable(t *testing.T) {
	cells := []string{strings.Repeat("甲", 5000), strings.Repeat("乙", 5000)}
	segments, err := versionapp.SegmentExtraction(
		versionapp.ExtractionResult{Elements: []versionapp.ExtractionElement{{
			Kind: "table_row", Text: strings.Join(cells, " | "), Row: cells, Source: "word/document.xml",
		}}},
		versionapp.SegmentOptions{SourcePath: "req.docx"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) < 2 {
		t.Fatalf("oversized single-row table produced %d segments, want it split", len(segments))
	}
	joined := joinedSegmentTexts(segments)
	for _, segment := range segments {
		if segment.Kind != versionapp.SegmentKindTable {
			t.Fatalf("segment %d kind = %q, want %q", segment.Seq, segment.Kind, versionapp.SegmentKindTable)
		}
		if got := runeCount(segment.Text); got > versionapp.DefaultSegmentMaxChars {
			t.Fatalf("table chunk %d has %d runes, over the %d-rune maximum", segment.Seq, got, versionapp.DefaultSegmentMaxChars)
		}
	}
	if got := strings.Count(joined, "甲"); got != 5000 {
		t.Fatalf("first cell content lost when splitting: %d of 5000 %q runes kept", got, "甲")
	}
	if got := strings.Count(joined, "乙"); got != 5000 {
		t.Fatalf("second cell content lost when splitting: %d of 5000 %q runes kept", got, "乙")
	}
}
