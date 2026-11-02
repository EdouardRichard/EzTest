// Package attachments implements requirement attachment text extraction for
// Markdown/plain text/HTML, OOXML docx/pptx/xlsx and simple PDFs. Parsed text
// is returned to the application service, which stores it in the content
// ledger; nothing is written to disk here.
package attachments

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"rsc.io/pdf"
)

// Sentinel errors re-exported for parser callers/tests.
var (
	ErrUnsupported       = version.ErrUnsupportedAttachmentFormat
	ErrUnsupportedLegacy = version.ErrUnsupportedLegacyAttachment
	ErrTooLarge          = version.ErrAttachmentTooLarge
	ErrCorrupt           = version.ErrAttachmentCorrupt
)

// Parser extracts plain text from supported attachments.
type Parser struct{}

// NewParser builds the attachment text parser.
func NewParser() *Parser { return &Parser{} }

// Extract reads one attachment and returns its plain text. Supported formats:
// .md/.markdown/.txt/.html (UTF-8 text), .docx (word/document.xml), .pptx
// (slide XML in numeric slide order), .xlsx (shared strings and worksheet
// cells) and .pdf (simple text operators). Images return ErrUnsupported; legacy
// .doc/.ppt return ErrUnsupportedLegacy with a save-as hint.
func (p *Parser) Extract(ctx context.Context, name, mime string, r io.Reader) (text string, err error) {
	if r == nil {
		return "", fmt.Errorf("%w: file content is required", ErrCorrupt)
	}
	data, err := readLimited(r, version.MaxAttachmentBytes)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	kind := version.DetectAttachmentKind(name, mime)
	switch kind {
	case version.AttachmentMarkdown, version.AttachmentText, version.AttachmentHTML:
		return extractPlainText(data)
	case version.AttachmentDOCX:
		return extractDOCX(data)
	case version.AttachmentPPTX:
		return extractPPTX(data)
	case version.AttachmentXLSX:
		return extractXLSX(data)
	case version.AttachmentPDF:
		return extractPDF(data)
	default:
		if version.IsLegacyOfficeFormat(name) {
			return "", fmt.Errorf("%w: %s", ErrUnsupportedLegacy, name)
		}
		return "", fmt.Errorf("%w: %s", ErrUnsupported, name)
	}
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read: %v", ErrCorrupt, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: file exceeds the upload limit", ErrTooLarge)
	}
	return data, nil
}

func extractPlainText(data []byte) (string, error) {
	if !utf8.Valid(data) {
		return "", fmt.Errorf("%w: text is not valid UTF-8", ErrCorrupt)
	}
	if int64(len(data)) > version.MaxExtractedTextBytes {
		return "", fmt.Errorf("%w: text exceeds the 2 MiB limit", ErrTooLarge)
	}
	return strings.TrimPrefix(string(data), "\uFEFF"), nil
}

// extractPDF extracts positioned text from at most 50 pages. Scanning/OCR is
// out of scope (P1-D13).
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
	var builder strings.Builder
	for pageNumber := 1; pageNumber <= pages; pageNumber++ {
		page := reader.Page(pageNumber)
		if page.V.IsNull() {
			continue
		}
		content := page.Content()
		texts := content.Text
		if len(texts) == 0 {
			continue
		}
		sort.SliceStable(texts, func(i, j int) bool {
			if math.Abs(texts[i].Y-texts[j].Y) > 1 {
				return texts[i].Y > texts[j].Y
			}
			return texts[i].X < texts[j].X
		})
		lastY := texts[0].Y
		lastEndX := texts[0].X + texts[0].W
		for index, item := range texts {
			if index > 0 {
				if math.Abs(item.Y-lastY) > 2 {
					builder.WriteString("\n")
				} else if item.X-lastEndX > 0.5 {
					builder.WriteString(" ")
				}
			}
			builder.WriteString(item.S)
			lastY = item.Y
			lastEndX = item.X + item.W
		}
		builder.WriteString("\n")
	}
	return strings.TrimSpace(builder.String()), nil
}
