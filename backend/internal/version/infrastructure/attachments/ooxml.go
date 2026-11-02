package attachments

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

const maxZipEntries = 2000

// numberedFile is one OOXML part whose numeric suffix defines its order.
type numberedFile struct {
	number int
	file   *zip.File
}

// numberedXMLFiles returns archive entries named <prefix><number><suffix> in
// numeric order. OOXML part names are not zero padded, so lexical sorting
// would place slide10 before slide2.
func numberedXMLFiles(archive *zip.Reader, prefix, suffix string) []numberedFile {
	files := make([]numberedFile, 0)
	for _, file := range archive.File {
		if !strings.HasPrefix(file.Name, prefix) || !strings.HasSuffix(file.Name, suffix) {
			continue
		}
		numberPart := strings.TrimSuffix(strings.TrimPrefix(file.Name, prefix), suffix)
		number, err := strconv.Atoi(numberPart)
		if err != nil {
			continue
		}
		files = append(files, numberedFile{number: number, file: file})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].number < files[j].number })
	return files
}

func openOOXML(data []byte, format string) (*zip.Reader, error) {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("%w: %s is not a zip archive", ErrCorrupt, format)
	}
	if len(archive.File) > maxZipEntries {
		return nil, fmt.Errorf("%w: too many archive entries", ErrTooLarge)
	}
	return archive, nil
}

func extractDOCX(data []byte) (string, error) {
	archive, err := openOOXML(data, "docx")
	if err != nil {
		return "", err
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
	archive, err := openOOXML(data, "pptx")
	if err != nil {
		return "", err
	}
	slides := numberedXMLFiles(archive, "ppt/slides/slide", ".xml")
	if len(slides) == 0 {
		return "", fmt.Errorf("%w: no presentation slides found", ErrCorrupt)
	}
	var builder strings.Builder
	for _, slide := range slides {
		raw, err := readZipEntry(slide.file)
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

// extractXLSX resolves the shared string table and renders cell values sheet by
// sheet. Cells are tab separated and rows newline separated so the extracted
// text keeps the table shape a reader sees.
func extractXLSX(data []byte) (string, error) {
	archive, err := openOOXML(data, "xlsx")
	if err != nil {
		return "", err
	}
	shared, err := readSharedStrings(archive)
	if err != nil {
		return "", err
	}
	sheets := numberedXMLFiles(archive, "xl/worksheets/sheet", ".xml")
	if len(sheets) == 0 {
		return "", fmt.Errorf("%w: no worksheet files found", ErrCorrupt)
	}
	var builder strings.Builder
	for _, sheet := range sheets {
		raw, err := readZipEntry(sheet.file)
		if err != nil {
			return "", err
		}
		text, err := extractSheetText(raw, shared)
		if err != nil {
			return "", err
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString(text)
	}
	return strings.TrimSpace(builder.String()), nil
}

// readSharedStrings decodes xl/sharedStrings.xml when present. A spreadsheet
// using only inline strings legitimately has no shared string table.
func readSharedStrings(archive *zip.Reader) ([]string, error) {
	for _, file := range archive.File {
		if file.Name != "xl/sharedStrings.xml" {
			continue
		}
		raw, err := readZipEntry(file)
		if err != nil {
			return nil, err
		}
		return decodeSharedStrings(raw)
	}
	return nil, nil
}

func decodeSharedStrings(raw []byte) ([]string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	values := make([]string, 0)
	var current strings.Builder
	inItem, inText := false, false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: decode xlsx shared strings: %v", ErrCorrupt, err)
		}
		switch element := token.(type) {
		case xml.StartElement:
			switch element.Name.Local {
			case "si":
				inItem = true
				current.Reset()
			case "t":
				// Rich text runs nest their <t> inside <r>; all of them
				// belong to the same string item.
				if inItem {
					inText = true
				}
			}
		case xml.CharData:
			if inItem && inText {
				current.Write(element)
			}
		case xml.EndElement:
			switch element.Name.Local {
			case "t":
				inText = false
			case "si":
				if inItem {
					values = append(values, current.String())
				}
				inItem = false
			}
		}
	}
	return values, nil
}

func extractSheetText(raw []byte, shared []string) (string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	var rows strings.Builder
	cells := make([]string, 0)
	var cell strings.Builder
	cellType := ""
	hasCell := false
	inValue, inInline, inText := false, false, false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("%w: decode xlsx worksheet: %v", ErrCorrupt, err)
		}
		switch element := token.(type) {
		case xml.StartElement:
			switch element.Name.Local {
			case "c":
				cell.Reset()
				cellType = ""
				hasCell = true
				for _, attribute := range element.Attr {
					if attribute.Name.Local == "t" {
						cellType = attribute.Value
						break
					}
				}
			case "v":
				inValue = true
			case "is":
				inInline = true
			case "t":
				if inInline {
					inText = true
				}
			}
		case xml.CharData:
			// Formula text lives in <f>, which neither flag matches.
			if inValue || inText {
				cell.Write(element)
			}
		case xml.EndElement:
			switch element.Name.Local {
			case "v":
				inValue = false
			case "t":
				inText = false
			case "is":
				inInline = false
			case "c":
				if hasCell {
					cells = append(cells, resolveCell(cell.String(), cellType, shared))
				}
				hasCell = false
			case "row":
				line := strings.Join(cells, "\t")
				cells = cells[:0]
				if strings.TrimSpace(line) == "" {
					continue
				}
				if rows.Len() > 0 {
					rows.WriteString("\n")
				}
				rows.WriteString(line)
			}
		}
	}
	return rows.String(), nil
}

func resolveCell(value, cellType string, shared []string) string {
	switch cellType {
	case "s":
		index, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || index < 0 || index >= len(shared) {
			return ""
		}
		return shared[index]
	case "b":
		if strings.TrimSpace(value) == "1" {
			return "TRUE"
		}
		return "FALSE"
	default:
		return value
	}
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
