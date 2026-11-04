// Package attachments implements requirement attachment text extraction for
// Markdown/plain text/HTML, OOXML docx/pptx/xlsx and PDFs (CID/ToUnicode text
// layer only). Parsed text is returned to the application service, which
// stores it in the content ledger; nothing is written to disk here.
package attachments

import (
	"context"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
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
// cells) and .pdf (text layer decoded through CID/ToUnicode font maps). Images
// return ErrUnsupported; legacy .doc/.ppt return ErrUnsupportedLegacy with a
// save-as hint.
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
