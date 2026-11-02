package attachments

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/ledongthuc/pdf"
)

// PDFPageSeparator separates the text extracted from consecutive PDF pages. It
// is a visible marker rather than a bare newline so readers (and the LLM
// context built from the attachment text) can tell where one page ends.
const PDFPageSeparator = "\n\n=== page break ===\n\n"

// ErrPDFNoTextLayer reports a PDF without any extractable text layer, e.g. a
// scanned or image-only document. Extracting embedded images or running OCR is
// out of scope (spec §8.2), so such attachments fail instead of yielding
// empty text that looks like a successful extraction.
var ErrPDFNoTextLayer = errors.New("pdf has no text layer")

// extractPDF extracts positioned text from at most MaxPDFPages pages. Text
// runs are decoded through each font's ToUnicode CMap, which covers the
// Type0/Identity-H CID fonts Chinese documents use. Embedded images are never
// read and no OCR is performed; a PDF with no text layer at all returns
// ErrPDFNoTextLayer.
func extractPDF(data []byte) (text string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			text = ""
			err = fmt.Errorf("%w: malformed PDF", ErrCorrupt)
		}
	}()
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	pages := reader.NumPage()
	if pages > version.MaxPDFPages {
		return "", fmt.Errorf("%w: PDF has %d pages (limit %d)", ErrTooLarge, pages, version.MaxPDFPages)
	}
	if pages <= 0 {
		return "", fmt.Errorf("%w: PDF has no pages", ErrCorrupt)
	}
	pageTexts := make([]string, 0, pages)
	for pageNumber := 1; pageNumber <= pages; pageNumber++ {
		page := reader.Page(pageNumber)
		if page.V.IsNull() {
			continue
		}
		// Passing nil builds the font cache per page: PDF resource names
		// such as /F1 are page scoped, so sharing one map across pages could
		// decode a run with another page's font.
		pageText, pageErr := page.GetPlainText(nil)
		if pageErr != nil {
			return "", fmt.Errorf("%w: PDF page %d: %v", ErrCorrupt, pageNumber, pageErr)
		}
		if pageText = strings.TrimSpace(pageText); pageText == "" {
			continue
		}
		pageTexts = append(pageTexts, pageText)
	}
	if len(pageTexts) == 0 {
		return "", fmt.Errorf("%w: scanned or image-only PDFs are not supported", ErrPDFNoTextLayer)
	}
	return strings.Join(pageTexts, PDFPageSeparator), nil
}
