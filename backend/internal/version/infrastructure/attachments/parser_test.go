package attachments_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/attachments"
)

func extract(t *testing.T, name, mime string, data []byte) (string, error) {
	t.Helper()
	result, err := extractResult(t, name, mime, data)
	return result.Text, err
}

func extractResult(t *testing.T, name, mime string, data []byte) (versionapp.ExtractionResult, error) {
	t.Helper()
	return attachments.NewParser().Extract(context.Background(), name, mime, bytes.NewReader(data))
}

func TestExtractMarkdownAndText(t *testing.T) {
	text, err := extract(t, "notes.md", "text/markdown", []byte("# Title\n\nSome *content*."))
	if err != nil {
		t.Fatalf("markdown: %v", err)
	}
	if !strings.Contains(text, "# Title") || !strings.Contains(text, "Some *content*.") {
		t.Fatalf("markdown = %q", text)
	}

	text, err = extract(t, "notes.txt", "text/plain", []byte("\ufeffplain text"))
	if err != nil {
		t.Fatalf("text: %v", err)
	}
	if text != "plain text" {
		t.Fatalf("text = %q", text)
	}

	text, err = extract(t, "names.txt", "text/plain", []byte("中文内容"))
	if err != nil || !strings.Contains(text, "中文内容") {
		t.Fatalf("utf8 text = %q, %v", text, err)
	}
}

func TestExtractTextRejectsInvalidUTF8(t *testing.T) {
	_, err := extract(t, "bad.txt", "text/plain", []byte{0xff, 0xfe, 0x00})
	if err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	if !errors.Is(err, attachments.ErrCorrupt) {
		t.Fatalf("error = %v", err)
	}
}

func TestExtractTextEnforcesSizeLimit(t *testing.T) {
	tooLarge := make([]byte, 2<<20+1)
	for index := range tooLarge {
		tooLarge[index] = 'a'
	}
	_, err := extract(t, "big.txt", "text/plain", tooLarge)
	if err == nil {
		t.Fatal("oversized text accepted")
	}
	if !errors.Is(err, attachments.ErrTooLarge) {
		t.Fatalf("error = %v", err)
	}
}

func TestSizeLimit(t *testing.T) {
	oversized := bytes.Repeat([]byte("a"), 10<<20+1)
	for _, name := range []string{"big.txt", "big.docx", "big.xlsx"} {
		if _, err := extract(t, name, "", oversized); !errors.Is(err, attachments.ErrTooLarge) {
			t.Fatalf("%s over 10 MiB error = %v, want ErrTooLarge", name, err)
		}
	}
	// The extracted-text cap is separate from the upload cap.
	tooMuchText := bytes.Repeat([]byte("a"), 2<<20+1)
	if _, err := extract(t, "big.txt", "text/plain", tooMuchText); !errors.Is(err, attachments.ErrTooLarge) {
		t.Fatalf("text over 2 MiB error = %v, want ErrTooLarge", err)
	}
}

func TestExtractOfficeFormats(t *testing.T) {
	cases := []struct {
		name  string
		file  string
		mime  string
		data  []byte
		wants []string
	}{
		{
			name: "docx",
			file: "requirements.docx",
			mime: "application/octet-stream",
			data: docxFixture(
				[]string{"Attachment matrix paragraph"},
				[][]string{{"Cell A", "Cell B"}},
			),
			wants: []string{"Attachment matrix paragraph", "Cell A", "Cell B", "| Cell A | Cell B |"},
		},
		{
			name:  "pptx",
			file:  "slides.pptx",
			mime:  "application/octet-stream",
			data:  pptxFixture([]string{"First slide", "Second slide"}),
			wants: []string{"First slide", "Second slide"},
		},
		{
			name:  "xlsx",
			file:  "matrix.xlsx",
			mime:  "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			data:  xlsxFixture(),
			wants: []string{"Requirement\tExpected", "Login works\tinline value", "TRUE\t2\tFALSE", "42\tExpected result"},
		},
		{
			name:  "xlsx by MIME",
			file:  "spreadsheet",
			mime:  "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			data:  xlsxFixture(),
			wants: []string{"Requirement\tExpected", "Login works\tinline value", "TRUE\t2\tFALSE", "42\tExpected result"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, err := extract(t, tc.file, tc.mime, tc.data)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			for _, want := range tc.wants {
				if !strings.Contains(text, want) {
					t.Fatalf("%s extraction missing %q: %q", tc.name, want, text)
				}
			}
			for _, markup := range []string{"<w:", "<a:", "<?xml"} {
				if strings.Contains(text, markup) {
					t.Fatalf("%s extraction leaked %q: %q", tc.name, markup, text)
				}
			}
		})
	}
}

func TestLegacyFormatsRejected(t *testing.T) {
	for _, name := range []string{"requirements.doc", "deck.ppt"} {
		_, err := extract(t, name, "application/octet-stream", []byte("legacy binary"))
		if err == nil {
			t.Fatalf("%s was accepted", name)
		}
		if !errors.Is(err, attachments.ErrUnsupportedLegacy) {
			t.Fatalf("%s error = %v, want ErrUnsupportedLegacy", name, err)
		}
		if !errors.Is(err, attachments.ErrUnsupported) {
			t.Fatalf("%s error = %v, want it to also match ErrUnsupported", name, err)
		}
		message := strings.ToLower(err.Error())
		if !strings.Contains(message, "docx") || !strings.Contains(message, "pptx") {
			t.Fatalf("%s message lacks a save-as hint: %v", name, err)
		}
	}
}

func TestExtractHTML(t *testing.T) {
	document := []byte("\ufeff<!doctype html><html><body><h1>标题</h1><p>body text</p></body></html>")
	for _, tc := range []struct{ name, mime string }{
		{"page.html", "text/html"},
		{"page.htm", "text/html"},
		{"page", "text/html"},
	} {
		text, err := extract(t, tc.name, tc.mime, document)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !strings.Contains(text, "标题") || !strings.Contains(text, "body text") {
			t.Fatalf("%s = %q, want visible text", tc.name, text)
		}
		if strings.Contains(text, "<h1>") || strings.Contains(text, "<p>") {
			t.Fatalf("%s leaked raw HTML tags: %q", tc.name, text)
		}
	}
}

func TestExtractDOCXParagraphsAndTable(t *testing.T) {
	fixture := docxFixture(
		[]string{"First paragraph", "Second paragraph"},
		[][]string{{"Header A", "Header B"}, {"Value 1", "Value 2"}},
	)
	text, err := extract(t, "requirements.docx", "application/octet-stream", fixture)
	if err != nil {
		t.Fatalf("docx: %v", err)
	}
	for _, want := range []string{"First paragraph", "Second paragraph", "Header A", "Header B", "Value 1", "Value 2"} {
		if !strings.Contains(text, want) {
			t.Fatalf("docx extraction missing %q: %q", want, text)
		}
	}
	if strings.Index(text, "First paragraph") > strings.Index(text, "Second paragraph") {
		t.Fatalf("paragraph order lost: %q", text)
	}
}

func TestExtractPPTXSlidesInNumericOrder(t *testing.T) {
	slides := []string{"Slide one", "Slide two", "Slide three", "Slide four", "Slide five",
		"Slide six", "Slide seven", "Slide eight", "Slide nine", "Slide ten", "Slide eleven"}
	text, err := extract(t, "deck.pptx", "application/octet-stream", pptxFixture(slides))
	if err != nil {
		t.Fatalf("pptx: %v", err)
	}
	previous := -1
	for index, slide := range slides {
		position := strings.Index(text, slide)
		if position < 0 {
			t.Fatalf("slide %d missing: %q", index+1, text)
		}
		if position <= previous {
			t.Fatalf("slide order wrong for %q: %q", slide, text)
		}
		previous = position
	}
}

func TestExtractPDFText(t *testing.T) {
	text, err := extract(t, "report.pdf", "application/pdf", pdfFixture([]string{"Hello PDF", "Second page"}))
	if err != nil {
		t.Fatalf("pdf: %v", err)
	}
	if !strings.Contains(text, "Hello PDF") || !strings.Contains(text, "Second page") {
		t.Fatalf("pdf = %q", text)
	}
}

func TestExtractPDFEnforcesPageLimit(t *testing.T) {
	pages := make([]string, 51)
	for index := range pages {
		pages[index] = "Page"
	}
	_, err := extract(t, "big.pdf", "application/pdf", pdfFixture(pages))
	if err == nil {
		t.Fatal("PDF over the page limit accepted")
	}
	if !errors.Is(err, attachments.ErrTooLarge) {
		t.Fatalf("error = %v", err)
	}
}

func TestExtractRejectsCorruptZipAndPDF(t *testing.T) {
	if _, err := extract(t, "broken.docx", "application/octet-stream", []byte("not a zip")); err == nil {
		t.Fatal("corrupt docx accepted")
	} else if !errors.Is(err, attachments.ErrCorrupt) {
		t.Fatalf("docx error = %v", err)
	}
	if _, err := extract(t, "broken.pdf", "application/pdf", []byte("%PDF-1.4 garbage")); err == nil {
		t.Fatal("corrupt pdf accepted")
	} else if !errors.Is(err, attachments.ErrCorrupt) {
		t.Fatalf("pdf error = %v", err)
	}
}

func TestExtractDOCXRejectsOversizedXML(t *testing.T) {
	// Declared uncompressed size alone must reject the entry (zip bomb guard).
	fixture := zipFixture(map[string]string{
		"word/document.xml": strings.Repeat("<w:p><w:r><w:t>x</w:t></w:r></w:p>", 1) + strings.Repeat(" ", 21<<20),
	})
	_, err := extract(t, "bomb.docx", "application/octet-stream", fixture)
	if err == nil {
		t.Fatal("oversized OOXML accepted")
	}
	if !errors.Is(err, attachments.ErrTooLarge) {
		t.Fatalf("error = %v", err)
	}
}

func TestExtractRejectsLegacyAndArchiveFormats(t *testing.T) {
	for _, tc := range []struct{ name, mime string }{
		{"legacy.doc", "application/msword"},
		{"legacy.ppt", "application/vnd.ms-powerpoint"},
		{"archive.zip", "application/zip"},
	} {
		if _, err := extract(t, tc.name, tc.mime, []byte("data")); err == nil {
			t.Fatalf("%s accepted", tc.name)
		} else if !errors.Is(err, attachments.ErrUnsupported) {
			t.Fatalf("%s error = %v", tc.name, err)
		}
	}
}

// TestExtractStandaloneImage covers R-E3-1: a standalone image is returned as
// an image reference (bytes + MIME + dimensions) for the vision chain, not as
// text and not as an unsupported format.
func TestExtractStandaloneImage(t *testing.T) {
	result, err := extractResult(t, "screenshot.png", "image/png", fixturePNG(4, 3))
	if err != nil {
		t.Fatalf("standalone image: %v", err)
	}
	if len(result.Images) != 1 {
		t.Fatalf("images = %+v", result.Images)
	}
	image := result.Images[0]
	if image.MIME != "image/png" || image.Width != 4 || image.Height != 3 || len(image.Data) == 0 {
		t.Fatalf("image = %+v", image)
	}
	if strings.TrimSpace(result.Text) != "" {
		t.Fatalf("standalone image produced text: %q", result.Text)
	}
	if result.Coverage.Parsed != 1 || result.Coverage.Skipped != 0 {
		t.Fatalf("coverage = %+v", result.Coverage)
	}
}

func findElement(elements []versionapp.ExtractionElement, kind, text string) bool {
	for _, element := range elements {
		if element.Kind == kind && strings.Contains(element.Text, text) {
			return true
		}
	}
	return false
}

func TestDOCXFullyExtracts(t *testing.T) {
	result, err := extractResult(t, "req.docx", "", docxFullyFixture())
	if err != nil {
		t.Fatalf("docx: %v", err)
	}
	for _, want := range []string{
		"3.2 用户登录", "手机号", "表格单元格", "页眉", "页脚", "脚注", "尾注", "批注",
	} {
		if !strings.Contains(result.Text, want) {
			t.Fatalf("missing %q in extracted text:\n%s", want, result.Text)
		}
	}
	if !strings.Contains(result.Text, "| 表格单元格 | 预期结果 |") {
		t.Fatalf("table row structure lost:\n%s", result.Text)
	}
	if len(result.Images) != 1 {
		t.Fatalf("embedded image not extracted: %+v", result.Images)
	}
	if result.Images[0].MIME != "image/png" || len(result.Images[0].Data) == 0 ||
		result.Images[0].Width != 8 || result.Images[0].Height != 6 {
		t.Fatalf("embedded image metadata = %+v", result.Images[0])
	}
	if result.Coverage.Skipped != 0 {
		t.Fatalf("skipped = %+v", result.Skipped)
	}
	if !findElement(result.Elements, "heading", "3.2 用户登录") {
		t.Fatalf("heading element missing: %+v", result.Elements)
	}
	for _, element := range result.Elements {
		if element.Kind == "heading" && element.Text == "3.2 用户登录" {
			if element.Level != 2 || element.HeadingPath != "3.2 用户登录" {
				t.Fatalf("heading metadata = %+v", element)
			}
		}
	}
	if !findElement(result.Elements, "list_item", "手机号") {
		t.Fatalf("list item missing: %+v", result.Elements)
	}
	for _, kind := range []string{"header", "footer", "footnote", "endnote", "comment", "table_row"} {
		found := false
		for _, element := range result.Elements {
			if element.Kind == kind {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("element kind %q missing: %+v", kind, result.Elements)
		}
	}
	if result.Coverage.Found == 0 || result.Coverage.Found != result.Coverage.Parsed {
		t.Fatalf("coverage = %+v", result.Coverage)
	}
}

// TestDOCXTableImageExtracted covers images whose drawing lives in a table
// cell paragraph instead of the document body.
func TestDOCXTableImageExtracted(t *testing.T) {
	result, err := extractResult(t, "req.docx", "", docxTableImageFixture())
	if err != nil {
		t.Fatalf("docx: %v", err)
	}
	if !strings.Contains(result.Text, "表格中的图片") {
		t.Fatalf("table text missing: %q", result.Text)
	}
	if len(result.Images) != 1 || result.Images[0].MIME != "image/png" {
		t.Fatalf("table-cell image = %+v", result.Images)
	}
	if result.Coverage.Skipped != 0 {
		t.Fatalf("skipped = %+v", result.Skipped)
	}
}

func TestPPTXFullyExtracts(t *testing.T) {
	result, err := extractResult(t, "deck.pptx", "", pptxFullyFixture())
	if err != nil {
		t.Fatalf("pptx: %v", err)
	}
	for _, want := range []string{"3.2 用户登录", "表格单元格", "演讲者备注", "第二页"} {
		if !strings.Contains(result.Text, want) {
			t.Fatalf("missing %q in extracted text:\n%s", want, result.Text)
		}
	}
	if strings.Index(result.Text, "3.2 用户登录") > strings.Index(result.Text, "第二页") {
		t.Fatalf("slide order lost:\n%s", result.Text)
	}
	if len(result.Images) != 1 || result.Images[0].MIME != "image/png" || len(result.Images[0].Data) == 0 {
		t.Fatalf("embedded image = %+v", result.Images)
	}
	if !findElement(result.Elements, "table_row", "表格单元格") ||
		!findElement(result.Elements, "note", "演讲者备注") {
		t.Fatalf("structured elements missing: %+v", result.Elements)
	}
	if result.Coverage.Skipped != 0 || result.Coverage.Found != result.Coverage.Parsed {
		t.Fatalf("coverage = %+v", result.Coverage)
	}
}

func TestXLSXFullyExtracts(t *testing.T) {
	result, err := extractResult(t, "matrix.xlsx", "", xlsxFullyFixture())
	if err != nil {
		t.Fatalf("xlsx: %v", err)
	}
	for _, want := range []string{
		"可见表", "隐藏表", "(hidden)", "隐藏 sheet 内容", "缓存值",
		"内联字符串", "[formula]", "CONCAT(A1,A2)", "批注：需要补充边界条件",
	} {
		if !strings.Contains(result.Text, want) {
			t.Fatalf("missing %q in extracted text:\n%s", want, result.Text)
		}
	}
	if !strings.Contains(result.Text, "## Sheet: 隐藏表 (hidden)") {
		t.Fatalf("hidden sheet not marked:\n%s", result.Text)
	}
	if len(result.Images) != 1 || result.Images[0].MIME != "image/png" {
		t.Fatalf("embedded image = %+v", result.Images)
	}
	cachedFormula := false
	formulaOnly := false
	for _, element := range result.Elements {
		if element.Kind != "sheet_row" {
			continue
		}
		for _, cell := range element.Row {
			if cell == "2" {
				cachedFormula = true
			}
			if strings.Contains(cell, "[formula] CONCAT(A1,A2)") {
				formulaOnly = true
			}
		}
	}
	if !cachedFormula || !formulaOnly {
		t.Fatalf("formula cells = %+v", result.Elements)
	}
	if !findElement(result.Elements, "comment", "批注：需要补充边界条件") {
		t.Fatalf("comment element missing: %+v", result.Elements)
	}
	if result.Coverage.Skipped != 0 || result.Coverage.Found != result.Coverage.Parsed {
		t.Fatalf("coverage = %+v", result.Coverage)
	}
}

func TestPDFFullyExtracts(t *testing.T) {
	result, err := extractResult(t, "report.pdf", "application/pdf", pdfFullyFixture())
	if err != nil {
		t.Fatalf("pdf: %v", err)
	}
	if !strings.Contains(result.Text, "PDF image page") {
		t.Fatalf("pdf text = %q", result.Text)
	}
	if len(result.Images) != 1 {
		t.Fatalf("embedded pdf image not extracted: %+v", result.Images)
	}
	image := result.Images[0]
	if image.MIME != "image/png" || image.Width != 2 || image.Height != 2 || len(image.Data) == 0 {
		t.Fatalf("pdf image = %+v", image)
	}
	if result.Coverage.Skipped != 0 {
		t.Fatalf("skipped = %+v", result.Skipped)
	}
}

func TestPDFSkippedImageReported(t *testing.T) {
	result, err := extractResult(t, "report.pdf", "application/pdf", pdfSkippedImageFixture())
	if err != nil {
		t.Fatalf("pdf: %v", err)
	}
	if !strings.Contains(result.Text, "PDF image page") {
		t.Fatalf("pdf text = %q", result.Text)
	}
	if len(result.Images) != 0 {
		t.Fatalf("unsupported image decoded: %+v", result.Images)
	}
	if len(result.Skipped) != 1 || result.Coverage.Skipped != 1 {
		t.Fatalf("skipped = %+v coverage=%+v", result.Skipped, result.Coverage)
	}
	reason := result.Skipped[0].Reason
	if !strings.Contains(reason, "JPXDecode") {
		t.Fatalf("skip reason = %q, want the unsupported filter", reason)
	}
}

func TestCoverageSkippedImageHasReason(t *testing.T) {
	result, err := extractResult(t, "req.docx", "", docxSkippedImageFixture())
	if err != nil {
		t.Fatalf("docx: %v", err)
	}
	if !strings.Contains(result.Text, "正文保留") {
		t.Fatalf("body text lost: %q", result.Text)
	}
	if len(result.Images) != 0 {
		t.Fatalf("unparseable image returned: %+v", result.Images)
	}
	if result.Coverage.Skipped != 1 || len(result.Skipped) != 1 {
		t.Fatalf("coverage = %+v skipped=%+v", result.Coverage, result.Skipped)
	}
	if !strings.Contains(result.Skipped[0].Reason, "image") {
		t.Fatalf("skip reason = %q", result.Skipped[0].Reason)
	}
	if result.Coverage.Found != result.Coverage.Parsed+result.Coverage.Skipped {
		t.Fatalf("coverage mismatch = %+v", result.Coverage)
	}
}

func TestCoverageReport(t *testing.T) {
	cases := []struct {
		name string
		file string
		mime string
		data []byte
	}{
		{"docx", "req.docx", "", docxFullyFixture()},
		{"pptx", "deck.pptx", "", pptxFullyFixture()},
		{"xlsx", "matrix.xlsx", "", xlsxFullyFixture()},
		{"html", "page.html", "text/html", []byte(htmlFullyFixture)},
		{"pdf", "report.pdf", "application/pdf", pdfFullyFixture()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := extractResult(t, tc.file, tc.mime, tc.data)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			coverage := result.Coverage
			if coverage.Found == 0 {
				t.Fatalf("empty coverage: %+v", coverage)
			}
			if coverage.Found != coverage.Parsed+coverage.Skipped {
				t.Fatalf("found != parsed+skipped: %+v", coverage)
			}
			if coverage.Parsed != len(result.Elements)+len(result.Images) {
				t.Fatalf("parsed = %d, elements+images = %d", coverage.Parsed, len(result.Elements)+len(result.Images))
			}
			if coverage.Skipped != len(result.Skipped) || coverage.Skipped != 0 {
				t.Fatalf("unexpected skips: %+v", coverage)
			}
			if len(coverage.SkipReasons) != 0 {
				t.Fatalf("skip reasons for a clean fixture: %+v", coverage.SkipReasons)
			}
			if coverage.ImagesFound != len(result.Images) {
				t.Fatalf("image coverage = %+v images=%d", coverage, len(result.Images))
			}
			t.Logf("%s coverage: %+v", tc.name, coverage)
		})
	}
}

func TestLimitFailsExplicitly(t *testing.T) {
	pages := make([]string, 51)
	for index := range pages {
		pages[index] = "Page"
	}
	if _, err := extractResult(t, "big.pdf", "application/pdf", pdfFixture(pages)); !errors.Is(err, attachments.ErrTooLarge) {
		t.Fatalf("PDF over the page limit error = %v, want ErrTooLarge", err)
	}

	paragraphs := make([]string, attachments.MaxDocumentElements+1)
	for index := range paragraphs {
		paragraphs[index] = "p"
	}
	if _, err := extractResult(t, "big.docx", "", docxFixture(paragraphs, nil)); !errors.Is(err, attachments.ErrTooLarge) {
		t.Fatalf("DOCX over the element limit error = %v, want ErrTooLarge", err)
	}
}

// TestDOCXDuplicateImageExtractedOnce covers embedded-image dedupe: two
// paragraphs referencing the same r:embed relationship describe one document
// image, so it must be extracted exactly once.
func TestDOCXDuplicateImageExtractedOnce(t *testing.T) {
	result, err := extractResult(t, "duplicate.docx", "", docxDuplicateImageFixture())
	if err != nil {
		t.Fatalf("docx: %v", err)
	}
	for _, want := range []string{"第一处图片引用", "第二处图片引用"} {
		if !strings.Contains(result.Text, want) {
			t.Fatalf("missing %q in extracted text: %q", want, result.Text)
		}
	}
	if len(result.Images) != 1 {
		t.Fatalf("duplicate embedded image extracted %d times: %+v", len(result.Images), result.Images)
	}
	if result.Coverage.ImagesFound != 1 || result.Coverage.ImagesParsed != 1 {
		t.Fatalf("image coverage = %+v", result.Coverage)
	}
	if coverage := result.Coverage; coverage.Found != coverage.Parsed+coverage.Skipped {
		t.Fatalf("coverage = %+v", coverage)
	}
}

// TestOOXMLImageRelationshipWithoutTargetReported covers R-A7-3a/3b for OOXML:
// a dangling image relationship (no target) cannot be resolved and must be a
// reasoned skip instead of a silent drop in DOCX and PPTX.
func TestOOXMLImageRelationshipWithoutTargetReported(t *testing.T) {
	cases := []struct {
		name string
		file string
		mime string
		data []byte
	}{
		{"docx", "dangling.docx", "", docxDanglingImageFixture()},
		{"pptx", "dangling.pptx", "", pptxDanglingImageFixture()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := extractResult(t, tc.file, tc.mime, tc.data)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if len(result.Images) != 0 {
				t.Fatalf("unresolvable image extracted: %+v", result.Images)
			}
			reported := false
			for _, skipped := range result.Skipped {
				reason := strings.ToLower(skipped.Reason)
				if skipped.Kind == "image" && strings.Contains(reason, "relationship") && strings.Contains(reason, "target") {
					reported = true
				}
			}
			if !reported {
				t.Fatalf("dangling image relationship not reported: %+v", result.Skipped)
			}
			if result.Coverage.Skipped != 1 {
				t.Fatalf("coverage = %+v", result.Coverage)
			}
			if coverage := result.Coverage; coverage.Found != coverage.Parsed+coverage.Skipped {
				t.Fatalf("coverage = %+v", coverage)
			}
		})
	}
}

// TestXLSXThreadedCommentReportedAsSkipped covers R-A7-3a: the legacy comment
// parser still reads legacy comments, while a threaded comment (a different
// XML part) must be reported as skipped with a reason, not silently dropped.
func TestXLSXThreadedCommentReportedAsSkipped(t *testing.T) {
	result, err := extractResult(t, "threaded.xlsx", "", xlsxThreadedCommentFixture())
	if err != nil {
		t.Fatalf("xlsx: %v", err)
	}
	if !strings.Contains(result.Text, "传统批注：需要补充边界条件") {
		t.Fatalf("legacy comment lost: %q", result.Text)
	}
	if strings.Contains(result.Text, "线程批注：现代 Excel 讨论串") {
		t.Fatalf("unparsed threaded comment leaked into text: %q", result.Text)
	}
	reported := false
	for _, skipped := range result.Skipped {
		if skipped.Kind == "comment" && strings.Contains(skipped.Source, "threadedComment1.xml") &&
			strings.Contains(strings.ToLower(skipped.Reason), "threaded") {
			reported = true
		}
	}
	if !reported {
		t.Fatalf("threaded comment skip missing: %+v", result.Skipped)
	}
	if coverage := result.Coverage; coverage.Skipped != 1 || coverage.Found != coverage.Parsed+coverage.Skipped {
		t.Fatalf("coverage = %+v", coverage)
	}
}

// xlsxFixture builds a minimal spreadsheet with two worksheets: shared strings
// (including a rich-text run), an inline string, numeric, boolean and formula
// cells on the first sheet, and a numeric cell on the second. Worksheet files
// are not zero padded, and the sheet order is what the extractor must preserve.
func xlsxFixture() []byte {
	const sharedStrings = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="4" uniqueCount="4">
<si><t>Requirement</t></si>
<si><t>Expected</t></si>
<si><r><t>Login </t></r><r><t>works</t></r></si>
<si><t>Expected result</t></si>
</sst>`
	const sheet1 = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row>
<row r="2"><c r="A2" t="s"><v>2</v></c><c r="B2" t="inlineStr"><is><t>inline value</t></is></c></row>
<row r="3"><c r="A3" t="b"><v>1</v></c><c r="B3"><f>1+1</f><v>2</v></c><c r="C3" t="b"><v>0</v></c></row>
</sheetData></worksheet>`
	const sheet2 = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
<row r="1"><c r="A1"><v>42</v></c><c r="B1" t="s"><v>3</v></c></row>
</sheetData></worksheet>`
	return zipFixture(map[string]string{
		"xl/sharedStrings.xml":     sharedStrings,
		"xl/worksheets/sheet1.xml": sheet1,
		"xl/worksheets/sheet2.xml": sheet2,
	})
}
