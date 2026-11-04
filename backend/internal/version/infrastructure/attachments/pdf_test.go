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

// TestPDFImageOnlyReturnsImages covers spec X-1: a PDF with no text layer but
// with parseable embedded images is not a failed extraction. The images must be
// surfaced for the vision chain (no OCR is performed, so there is no text), and
// the text-less page is recorded as a reasoned skip so the missing text layer
// stays visible in coverage.
func TestPDFImageOnlyReturnsImages(t *testing.T) {
	data := readPDFFixture(t, "image-only.pdf")
	result, err := extractResult(t, "image-only.pdf", "application/pdf", data)
	if err != nil {
		t.Fatalf("image-only PDF must surface its embedded image, got error: %v", err)
	}
	if strings.TrimSpace(result.Text) != "" {
		t.Fatalf("image-only PDF produced text %q", result.Text)
	}
	if len(result.Images) != 1 {
		t.Fatalf("image-only PDF images = %+v", result.Images)
	}
	if image := result.Images[0]; image.MIME != "image/png" || len(image.Data) == 0 || image.Width != 240 || image.Height != 120 {
		t.Fatalf("image-only PDF image = %+v", image)
	}
	if result.Coverage.ImagesFound != 1 || result.Coverage.ImagesParsed != 1 {
		t.Fatalf("image coverage = %+v", result.Coverage)
	}
	pageSkip := false
	for _, skipped := range result.Skipped {
		if skipped.Kind == "page" && skipped.Source == "pdf:page:1" &&
			strings.Contains(skipped.Reason, "no extractable text layer") {
			pageSkip = true
		}
	}
	if !pageSkip {
		t.Fatalf("text-less page skip missing: %+v", result.Skipped)
	}
	coverage := result.Coverage
	if coverage.Found != coverage.Parsed+coverage.Skipped || coverage.Parsed != len(result.Elements)+len(result.Images) {
		t.Fatalf("coverage = %+v elements=%d images=%d", coverage, len(result.Elements), len(result.Images))
	}
}

// TestPDFDecompressedImageLimitFailsExplicitly covers R-A7-3a: an embedded
// image whose Flate stream inflates past the decompressed-part limit is a hard
// failure, not a skip (a partial extraction must never be reported as done).
func TestPDFDecompressedImageLimitFailsExplicitly(t *testing.T) {
	_, err := extractResult(t, "oversized-image.pdf", "application/pdf", pdfOversizedImageFixture())
	if !errors.Is(err, attachments.ErrTooLarge) {
		t.Fatalf("oversized decompressed PDF image error = %v, want ErrTooLarge", err)
	}
}

// TestPDFScannedPageReportedAsSkipped covers R-A7-3/3b: a page without a text
// layer inside an otherwise readable PDF is a reasoned skip, not a silent gap.
func TestPDFScannedPageReportedAsSkipped(t *testing.T) {
	result, err := extractResult(t, "mixed.pdf", "application/pdf", pdfMixedScanFixture())
	if err != nil {
		t.Fatalf("mixed PDF: %v", err)
	}
	if !strings.Contains(result.Text, "Mixed PDF first page text") {
		t.Fatalf("text page lost: %q", result.Text)
	}
	skippedPage := false
	for _, skipped := range result.Skipped {
		if skipped.Kind == "page" && skipped.Source == "pdf:page:2" &&
			skipped.Reason == "page has no extractable text layer" {
			skippedPage = true
		}
	}
	if !skippedPage {
		t.Fatalf("scanned page skip missing: %+v", result.Skipped)
	}
	coverage := result.Coverage
	if coverage.Skipped != 1 || coverage.Found != coverage.Parsed+coverage.Skipped {
		t.Fatalf("coverage = %+v", coverage)
	}
}

// TestPDFImageObjectLimitFailsExplicitly covers the MaxPDFImageObjects limit:
// a document with more image XObjects than the scan limit fails explicitly
// instead of being partially parsed.
func TestPDFImageObjectLimitFailsExplicitly(t *testing.T) {
	fixture := pdfManyImagesFixture(attachments.MaxPDFImageObjects + 1)
	_, err := extractResult(t, "many-images.pdf", "application/pdf", fixture)
	if !errors.Is(err, attachments.ErrTooLarge) {
		t.Fatalf("PDF over the image-object limit error = %v, want ErrTooLarge", err)
	}
}
