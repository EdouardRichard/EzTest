package attachments_test

import (
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
