package attachments_test

import (
	"archive/zip"
	"bytes"
	"fmt"
)

// docxFixture builds a minimal OOXML wordprocessing document. Paragraphs are
// emitted in order followed by the table rows; each table cell is its own
// paragraph, which is what Word itself does.
func docxFixture(paragraphs []string, table [][]string) []byte {
	var body bytes.Buffer
	for _, paragraph := range paragraphs {
		fmt.Fprintf(&body, `<w:p><w:r><w:t xml:space="preserve">%s</w:t></w:r></w:p>`, paragraph)
	}
	if len(table) > 0 {
		body.WriteString(`<w:tbl>`)
		for _, row := range table {
			body.WriteString(`<w:tr>`)
			for _, cell := range row {
				fmt.Fprintf(&body, `<w:tc><w:p><w:r><w:t>%s</w:t></w:r></w:p></w:tc>`, cell)
			}
			body.WriteString(`</w:tr>`)
		}
		body.WriteString(`</w:tbl>`)
	}
	document := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:body>` + body.String() + `</w:body></w:document>`
	return zipFixture(map[string]string{
		"[Content_Types].xml": `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`,
		"word/document.xml":   document,
	})
}

// pptxFixture builds a minimal presentation with one XML file per slide. The
// slide file names are intentionally not zero padded: slide10 must sort after
// slide2.
func pptxFixture(slides []string) []byte {
	files := map[string]string{}
	for index, slide := range slides {
		files[fmt.Sprintf("ppt/slides/slide%d.xml", index+1)] = fmt.Sprintf(
			`<?xml version="1.0" encoding="UTF-8"?><p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>%s</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>`,
			slide,
		)
	}
	return zipFixture(files)
}

func zipFixture(files map[string]string) []byte {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	// Deterministic file order keeps fixtures reproducible.
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if names[j] < names[i] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	for _, name := range names {
		entry, err := writer.Create(name)
		if err != nil {
			panic(err)
		}
		if _, err := entry.Write([]byte(files[name])); err != nil {
			panic(err)
		}
	}
	if err := writer.Close(); err != nil {
		panic(err)
	}
	return buffer.Bytes()
}

// pdfFixture builds a valid single-font PDF with one text run per page and a
// correct xref table so rsc.io/pdf can read it.
func pdfFixture(pages []string) []byte {
	objects := []string{
		`<< /Type /Catalog /Pages 2 0 R >>`,
	}
	pageObjectIDs := make([]int, 0, len(pages))
	for range pages {
		pageObjectIDs = append(pageObjectIDs, 0)
	}
	// Object layout: 1 catalog, 2 pages, then per page: page object, content
	// stream, and one shared font object at the end.
	objectCount := 2 + len(pages)*2 + 1
	kids := make([]string, 0, len(pages))
	nextID := 3
	for index := range pages {
		pageObjectIDs[index] = nextID
		kids = append(kids, fmt.Sprintf("%d 0 R", nextID))
		nextID += 2
	}
	objects = append(objects, fmt.Sprintf(`<< /Type /Pages /Kids [%s] /Count %d >>`,
		joinStrings(kids, " "), len(pages)))
	fontID := objectCount
	for index, page := range pages {
		pageID := pageObjectIDs[index]
		contentID := pageID + 1
		objects = append(objects, fmt.Sprintf(
			`<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents %d 0 R /Resources << /Font << /F1 %d 0 R >> >> >>`,
			contentID, fontID))
		stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", page)
		objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	}
	objects = append(objects, fmt.Sprintf(
		`<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /FirstChar 32 /LastChar 126 /Widths [%s] >>`,
		helveticaWidths()))

	var buffer bytes.Buffer
	buffer.WriteString("%PDF-1.4\n")
	offsets := make([]int, 0, len(objects))
	for index, object := range objects {
		offsets = append(offsets, buffer.Len())
		fmt.Fprintf(&buffer, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xrefOffset := buffer.Len()
	fmt.Fprintf(&buffer, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&buffer, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&buffer, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xrefOffset)
	return buffer.Bytes()
}

func joinStrings(values []string, separator string) string {
	var buffer bytes.Buffer
	for index, value := range values {
		if index > 0 {
			buffer.WriteString(separator)
		}
		buffer.WriteString(value)
	}
	return buffer.String()
}

// helveticaWidths is a synthetic but usable /Widths table for codes 32..126.
// The space glyph is narrower than the others so word gaps are observable.
func helveticaWidths() string {
	widths := make([]string, 0, 95)
	for code := 32; code <= 126; code++ {
		if code == 32 {
			widths = append(widths, "278")
			continue
		}
		widths = append(widths, "500")
	}
	return joinStrings(widths, " ")
}
