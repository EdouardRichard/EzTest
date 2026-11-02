package attachments_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/attachments"
)

func extract(t *testing.T, name, mime string, data []byte) (string, error) {
	t.Helper()
	return attachments.NewParser().Extract(context.Background(), name, mime, strings.NewReader(string(data)))
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
		name    string
		file    string
		mime    string
		data    []byte
		wants   []string
		ordered []string
	}{
		{
			name: "docx",
			file: "requirements.docx",
			mime: "application/octet-stream",
			data: docxFixture(
				[]string{"Attachment matrix paragraph"},
				[][]string{{"Cell A", "Cell B"}},
			),
			wants: []string{"Attachment matrix paragraph", "Cell A", "Cell B"},
		},
		{
			name:  "pptx",
			file:  "slides.pptx",
			mime:  "application/octet-stream",
			data:  pptxFixture([]string{"First slide", "Second slide"}),
			wants: []string{"First slide", "Second slide"},
		},
		{
			name:    "xlsx",
			file:    "matrix.xlsx",
			mime:    "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			data:    xlsxFixture(),
			wants:   []string{"Requirement", "Expected", "Login works", "inline value", "42", "Expected result"},
			ordered: []string{"Requirement", "Login works", "42"},
		},
		{
			name:  "xlsx by MIME",
			file:  "spreadsheet",
			mime:  "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			data:  xlsxFixture(),
			wants: []string{"Requirement", "42"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, err := extract(t, tc.file, tc.mime, tc.data)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if strings.TrimSpace(text) == "" {
				t.Fatalf("%s extraction is empty", tc.name)
			}
			for _, want := range tc.wants {
				if !strings.Contains(text, want) {
					t.Fatalf("%s extraction missing %q: %q", tc.name, want, text)
				}
			}
			previous := -1
			for _, want := range tc.ordered {
				position := strings.Index(text, want)
				if position <= previous {
					t.Fatalf("%s extraction order wrong at %q: %q", tc.name, want, text)
				}
				previous = position
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
			t.Fatalf("%s = %q", tc.name, text)
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

func TestExtractRejectsLegacyAndImageFormats(t *testing.T) {
	for _, tc := range []struct{ name, mime string }{
		{"legacy.doc", "application/msword"},
		{"legacy.ppt", "application/vnd.ms-powerpoint"},
		{"screenshot.png", "image/png"},
		{"archive.zip", "application/zip"},
	} {
		if _, err := extract(t, tc.name, tc.mime, []byte("data")); err == nil {
			t.Fatalf("%s accepted", tc.name)
		} else if !errors.Is(err, attachments.ErrUnsupported) {
			t.Fatalf("%s error = %v", tc.name, err)
		}
	}
}

// xlsxFixture builds a minimal spreadsheet with two worksheets: shared strings
// (including a rich-text run) on the first sheet, and an inline string plus a
// numeric cell on the second. Worksheet files are not zero padded, and the
// sheet order is what the extractor must preserve.
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
