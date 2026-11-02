// Package attachments implements the P1 requirement attachment text
// extraction for Markdown/plain text, OOXML docx/pptx and simple PDFs. Parsed
// text is returned to the application service, which stores it in the content
// ledger; nothing is written to disk here.
package attachments

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"rsc.io/pdf"
)

// Sentinel errors re-exported for parser callers/tests.
var (
	ErrUnsupported = version.ErrUnsupportedAttachmentFormat
	ErrTooLarge    = version.ErrAttachmentTooLarge
	ErrCorrupt     = version.ErrAttachmentCorrupt
)

const maxZipEntries = 2000

// Parser extracts plain text from supported attachments.
type Parser struct{}

// NewParser builds the attachment text parser.
func NewParser() *Parser { return &Parser{} }

// Extract reads one attachment and returns its plain text. Supported formats:
// .md/.markdown/.txt (UTF-8), .docx (word/document.xml), .pptx (slide XML in
// numeric slide order) and .pdf (simple text operators). Images and legacy
// .doc/.ppt return ErrUnsupportedAttachmentFormat.
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
	case version.AttachmentMarkdown, version.AttachmentText:
		return extractPlainText(data)
	case version.AttachmentDOCX:
		return extractDOCX(data)
	case version.AttachmentPPTX:
		return extractPPTX(data)
	case version.AttachmentPDF:
		return extractPDF(data)
	default:
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

func extractDOCX(data []byte) (string, error) {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("%w: docx is not a zip archive", ErrCorrupt)
	}
	if len(archive.File) > maxZipEntries {
		return "", fmt.Errorf("%w: too many archive entries", ErrTooLarge)
	}
	for _, file := range archive.File {
		if file.Name != "word/document.xml" {
			continue
		}
		raw, err := readZipEntry(file)
		if err != nil {
			return "", err
		}
		return extractXMLText(raw)
	}
	return "", fmt.Errorf("%w: word/document.xml is missing", ErrCorrupt)
}

func extractPPTX(data []byte) (string, error) {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("%w: pptx is not a zip archive", ErrCorrupt)
	}
	if len(archive.File) > maxZipEntries {
		return "", fmt.Errorf("%w: too many archive entries", ErrTooLarge)
	}
	type slide struct {
		number int
		file   *zip.File
	}
	slides := make([]slide, 0)
	for _, file := range archive.File {
		if !strings.HasPrefix(file.Name, "ppt/slides/slide") || !strings.HasSuffix(file.Name, ".xml") {
			continue
		}
		numberPart := strings.TrimSuffix(strings.TrimPrefix(file.Name, "ppt/slides/slide"), ".xml")
		number, err := strconv.Atoi(numberPart)
		if err != nil {
			continue
		}
		slides = append(slides, slide{number: number, file: file})
	}
	if len(slides) == 0 {
		return "", fmt.Errorf("%w: no presentation slides found", ErrCorrupt)
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].number < slides[j].number })

	var builder strings.Builder
	for _, item := range slides {
		raw, err := readZipEntry(item.file)
		if err != nil {
			return "", err
		}
		text, err := extractXMLText(raw)
		if err != nil {
			return "", err
		}
		builder.WriteString(strings.TrimSpace(text))
		builder.WriteString("\n")
	}
	return strings.TrimSpace(builder.String()), nil
}

func readZipEntry(file *zip.File) ([]byte, error) {
	if file.UncompressedSize64 > uint64(version.MaxOOXMLBytes) {
		return nil, fmt.Errorf("%w: XML entry exceeds the 20 MiB limit", ErrTooLarge)
	}
	reader, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: open archive entry: %v", ErrCorrupt, err)
	}
	defer reader.Close()
	raw, err := io.ReadAll(io.LimitReader(reader, version.MaxOOXMLBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read archive entry: %v", ErrCorrupt, err)
	}
	if int64(len(raw)) > version.MaxOOXMLBytes {
		return nil, fmt.Errorf("%w: XML entry exceeds the 20 MiB limit", ErrTooLarge)
	}
	return raw, nil
}

// extractXMLText walks OOXML and collects drawing/wordprocessing text runs.
// Paragraph ends become newlines so tables and runs keep a readable order.
func extractXMLText(raw []byte) (string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	var builder strings.Builder
	inText := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("%w: decode XML: %v", ErrCorrupt, err)
		}
		switch element := token.(type) {
		case xml.StartElement:
			switch element.Name.Local {
			case "t":
				inText = true
			case "tab":
				builder.WriteString("\t")
			case "br":
				builder.WriteString("\n")
			}
		case xml.CharData:
			if inText {
				builder.Write(raw)
			}
		case xml.EndElement:
			switch element.Name.Local {
			case "t":
				inText = false
			case "p":
				builder.WriteString("\n")
			}
		}
	}
	return builder.String(), nil
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
