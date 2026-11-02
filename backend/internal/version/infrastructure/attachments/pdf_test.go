package attachments_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/attachments"
)

// readPDFFixture loads a committed binary fixture from testdata.
func readPDFFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

// TestPDFChineseText extracts a two-page PDF whose Chinese text is encoded via
// a Type0 CID font (/Identity-H) and a /ToUnicode CMap. rsc.io/pdf returned
// unusable text for this shape, so readable Chinese here proves the CID map is
// applied. The two pages must also stay separated by the page marker.
func TestPDFChineseText(t *testing.T) {
	data := readPDFFixture(t, "cn-cid.pdf")
	text, err := extract(t, "cn-cid.pdf", "application/pdf", data)
	if err != nil {
		t.Fatalf("extract cn-cid.pdf: %v", err)
	}

	pageOne := "验收标准：附件中的中文必须可读\n第一页：需求描述与测试步骤"
	pageTwo := "第二页：分页文本仍需保留\n结束语：PDF 文本抽取检查完成"
	want := pageOne + attachments.PDFPageSeparator + pageTwo
	if text != want {
		t.Fatalf("extracted text = %q, want %q", text, want)
	}
	if strings.ContainsRune(text, '\uFFFD') {
		t.Fatalf("extracted text contains Unicode replacement characters: %q", text)
	}
}

// TestPDFImageOnlyRejected covers the review focus: a PDF with an embedded
// image but no text layer must fail with ErrPDFNoTextLayer instead of
// returning empty text (which callers would store as a successful extraction).
// No OCR is performed, so the image must not produce any text.
func TestPDFImageOnlyRejected(t *testing.T) {
	data := readPDFFixture(t, "image-only.pdf")
	text, err := extract(t, "image-only.pdf", "application/pdf", data)
	if err == nil {
		t.Fatalf("image-only PDF accepted as text: %q", text)
	}
	if !errors.Is(err, attachments.ErrPDFNoTextLayer) {
		t.Fatalf("error = %v, want ErrPDFNoTextLayer", err)
	}
	if text != "" {
		t.Fatalf("image-only PDF returned text %q", text)
	}
}
