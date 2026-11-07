// Package attachments implements requirement attachment content extraction for
// Markdown/plain text/HTML, OOXML docx/pptx/xlsx, PDFs (CID/ToUnicode text
// layer plus embedded image objects) and standalone images. Parsed text is
// returned to the application service, which stores it in the content ledger;
// nothing is written to disk here. OCR and PDF page rendering are never
// performed (spec N-7, R-A7-3).
package attachments

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode/utf8"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// Sentinel errors re-exported for parser callers/tests.
var (
	ErrUnsupported       = version.ErrUnsupportedAttachmentFormat
	ErrUnsupportedLegacy = version.ErrUnsupportedLegacyAttachment
	ErrTooLarge          = version.ErrAttachmentTooLarge
	ErrCorrupt           = version.ErrAttachmentCorrupt
)

// Parser limits. Any of them being hit fails the whole parse explicitly with
// ErrTooLarge instead of returning a partial extraction that looks done
// (R-A7-3a). The upload/XML/page limits live in the version domain next to the
// other attachment limits.
const (
	// MaxDocumentElements bounds the number of content elements kept in one
	// extraction (paragraphs, table rows, slides, sheet rows, notes, images).
	MaxDocumentElements = 20000
	// MaxEmbeddedImages bounds how many embedded images one attachment may
	// contribute to the vision chain.
	MaxEmbeddedImages = 200
	// MaxPDFImageObjects bounds image XObjects scanned in one PDF.
	MaxPDFImageObjects = 500
	// MaxPDFObjects bounds raw PDF objects inspected while looking for images.
	MaxPDFObjects = 20000
	// MaxSheetRows bounds rows parsed from one workbook.
	MaxSheetRows = 50000
)

// Parser extracts text, structured elements, embedded images and a coverage
// report from supported attachments.
type Parser struct{}

// NewParser builds the attachment parser.
func NewParser() *Parser { return &Parser{} }

// Extract reads one attachment and returns its human-visible content. Supported
// formats: .md/.markdown/.txt (full text), .html (visible text, headings,
// lists, tables and image references), .docx (body, heading hierarchy, lists,
// tables, headers/footers, footnotes/endnotes, comments and word/media
// images), .pptx (slides in order, tables, speaker notes and ppt/media
// images), .xlsx (all sheets, cached formula values, comments, hidden-sheet
// markers and xl/media images), .pdf (text layer plus parseable embedded image
// objects) and standalone images (returned as an image reference). Legacy
// .doc/.ppt return ErrUnsupportedLegacy with a save-as hint. A limit hit is an
// explicit ErrTooLarge; skips are reported with a reason instead of silently
// dropping content.
func (p *Parser) Extract(ctx context.Context, name, mime string, r io.Reader) (versionapp.ExtractionResult, error) {
	if r == nil {
		return versionapp.ExtractionResult{}, fmt.Errorf("%w: file content is required", ErrCorrupt)
	}
	data, err := readLimited(r, version.MaxAttachmentBytes)
	if err != nil {
		return versionapp.ExtractionResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return versionapp.ExtractionResult{}, err
	}

	kind := version.DetectAttachmentKind(name, mime)
	switch kind {
	case version.AttachmentMarkdown, version.AttachmentText:
		return extractPlainText(data)
	case version.AttachmentHTML:
		return extractHTMLResult(data)
	case version.AttachmentDOCX:
		return extractDOCXResult(data)
	case version.AttachmentPPTX:
		return extractPPTXResult(data)
	case version.AttachmentXLSX:
		return extractXLSXResult(data)
	case version.AttachmentPDF:
		return extractPDFResult(data)
	case version.AttachmentImage:
		return extractStandaloneImage(name, data)
	default:
		if version.IsLegacyOfficeFormat(name) {
			return versionapp.ExtractionResult{}, fmt.Errorf("%w: %s", ErrUnsupportedLegacy, name)
		}
		return versionapp.ExtractionResult{}, fmt.Errorf("%w: %s", ErrUnsupported, name)
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

func extractPlainText(data []byte) (versionapp.ExtractionResult, error) {
	if !utf8.Valid(data) {
		return versionapp.ExtractionResult{}, fmt.Errorf("%w: text is not valid UTF-8", ErrCorrupt)
	}
	if int64(len(data)) > version.MaxExtractedTextBytes {
		return versionapp.ExtractionResult{}, fmt.Errorf("%w: text exceeds the 2 MiB limit", ErrTooLarge)
	}
	text := strings.TrimPrefix(string(data), "\uFEFF")
	builder := newResultBuilder()
	if err := builder.addText(text); err != nil {
		return versionapp.ExtractionResult{}, err
	}
	if err := builder.element(versionapp.ExtractionElement{Kind: "document", Text: text}); err != nil {
		return versionapp.ExtractionResult{}, err
	}
	return builder.result(), nil
}

// extractStandaloneImage returns the image object itself as an image reference
// for the vision chain (R-E3-1); it produces no text and never runs vision.
func extractStandaloneImage(name string, data []byte) (versionapp.ExtractionResult, error) {
	builder := newResultBuilder()
	if err := builder.addEmbeddedImage(name, path.Base(name), data); err != nil {
		return versionapp.ExtractionResult{}, err
	}
	return builder.result(), nil
}

// resultBuilder accumulates text, elements, image refs and the coverage report
// of one extraction, enforcing the element/text/image limits while building.
type resultBuilder struct {
	text          strings.Builder
	elements      []versionapp.ExtractionElement
	skipped       []versionapp.SkippedElement
	images        []versionapp.ExtractionImage
	found         int
	parsed        int
	skippedCount  int
	imagesFound   int
	imagesParsed  int
	skipReasons   []string
	elementCounts map[string]int
}

func newResultBuilder() *resultBuilder {
	return &resultBuilder{elementCounts: make(map[string]int)}
}

func (b *resultBuilder) addText(text string) error {
	if text == "" {
		return nil
	}
	if b.found > MaxDocumentElements {
		return fmt.Errorf("%w: document has more than %d elements", ErrTooLarge, MaxDocumentElements)
	}
	b.text.WriteString(text)
	if int64(b.text.Len()) > version.MaxExtractedTextBytes {
		return fmt.Errorf("%w: extracted text exceeds the 2 MiB limit", ErrTooLarge)
	}
	return nil
}

// element records one parsed structured element and counts it in coverage.
func (b *resultBuilder) element(element versionapp.ExtractionElement) error {
	b.found++
	if b.found > MaxDocumentElements {
		return fmt.Errorf("%w: document has more than %d elements", ErrTooLarge, MaxDocumentElements)
	}
	b.parsed++
	b.elements = append(b.elements, element)
	b.elementCounts[element.Kind]++
	return nil
}

// skip records one found-but-unparsed element together with its reason.
func (b *resultBuilder) skip(kind, source, reason string) error {
	b.found++
	if b.found > MaxDocumentElements {
		return fmt.Errorf("%w: document has more than %d elements", ErrTooLarge, MaxDocumentElements)
	}
	b.skippedCount++
	if kind == "image" {
		b.imagesFound++
	}
	if strings.TrimSpace(reason) == "" {
		reason = "unsupported content"
	}
	b.skipped = append(b.skipped, versionapp.SkippedElement{Kind: kind, Source: source, Reason: reason})
	for _, existing := range b.skipReasons {
		if existing == reason {
			return nil
		}
	}
	b.skipReasons = append(b.skipReasons, reason)
	return nil
}

// image records one parsed embedded image reference. Data is kept so the
// application can hand the original object to the vision chain.
func (b *resultBuilder) image(image versionapp.ExtractionImage) error {
	b.found++
	b.imagesFound++
	if b.found > MaxDocumentElements {
		return fmt.Errorf("%w: document has more than %d elements", ErrTooLarge, MaxDocumentElements)
	}
	if b.imagesFound > MaxEmbeddedImages {
		return fmt.Errorf("%w: document has more than %d embedded images", ErrTooLarge, MaxEmbeddedImages)
	}
	if int64(len(image.Data)) > version.MaxAttachmentBytes {
		return fmt.Errorf("%w: embedded image exceeds the upload limit", ErrTooLarge)
	}
	b.parsed++
	b.imagesParsed++
	image.Seq = len(b.images) + 1
	image.Size = len(image.Data)
	b.images = append(b.images, image)
	b.elementCounts["image"]++
	return nil
}

// addEmbeddedImage probes the bytes and either records a usable image reference
// or reports an explicit skip reason (Emf/Wmf/tiff and corrupt objects cannot
// be sent to the vision chain).
func (b *resultBuilder) addEmbeddedImage(source, name string, data []byte) error {
	if len(data) == 0 {
		return b.skip("image", source, "embedded image object is empty")
	}
	mime, width, height, err := ImageDimensions(data)
	if err != nil {
		return b.skip("image", source, "embedded image is not png/jpeg/webp: "+err.Error())
	}
	return b.image(versionapp.ExtractionImage{
		Name:   name,
		Source: source,
		MIME:   mime,
		Width:  width,
		Height: height,
		Data:   data,
	})
}

// renderHeadingPath renders the ancestor heading chain. Levels without a
// heading (for example a document starting at Heading 2) are skipped instead of
// producing leading separators.
func renderHeadingPath(stack []string) string {
	parts := make([]string, 0, len(stack))
	for _, part := range stack {
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, " > ")
}

func (b *resultBuilder) result() versionapp.ExtractionResult {
	counts := make(map[string]int, len(b.elementCounts))
	for kind, count := range b.elementCounts {
		counts[kind] = count
	}
	reasons := append([]string(nil), b.skipReasons...)
	return versionapp.ExtractionResult{
		Text:     strings.TrimSpace(b.text.String()),
		Elements: b.elements,
		Skipped:  b.skipped,
		Images:   b.images,
		Coverage: versionapp.ExtractionCoverage{
			Found:         b.found,
			Parsed:        b.parsed,
			Skipped:       b.skippedCount,
			ImagesFound:   b.imagesFound,
			ImagesParsed:  b.imagesParsed,
			SkipReasons:   reasons,
			ElementCounts: counts,
		},
	}
}
