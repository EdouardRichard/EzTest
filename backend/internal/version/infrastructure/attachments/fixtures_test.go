package attachments_test

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
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

// docxBlock is one ordered block of the richer in-memory DOCX fixture builder:
// either a heading/paragraph or a table.
type docxBlock struct {
	Style string
	Text  string
	Table [][]string
}

// docxBlockFixture builds a minimal OOXML document from ordered heading,
// paragraph and table blocks. The extraction-layout fixture needs body headings
// and multiple tables interleaved, which docxFixture cannot express. A Heading3
// body requirement plus the 6.1/6.4 catalog tables is the C-03 UAT shape.
func docxBlockFixture(blocks []docxBlock) []byte {
	var body bytes.Buffer
	for _, block := range blocks {
		if len(block.Table) > 0 {
			body.WriteString(`<w:tbl>`)
			for _, row := range block.Table {
				body.WriteString(`<w:tr>`)
				for _, cell := range row {
					fmt.Fprintf(&body, `<w:tc><w:p><w:r><w:t xml:space="preserve">%s</w:t></w:r></w:p></w:tc>`, docxEscape(cell))
				}
				body.WriteString(`</w:tr>`)
			}
			body.WriteString(`</w:tbl>`)
			continue
		}
		if block.Style != "" {
			fmt.Fprintf(&body, `<w:p><w:pPr><w:pStyle w:val="%s"/></w:pPr><w:r><w:t xml:space="preserve">%s</w:t></w:r></w:p>`,
				block.Style, docxEscape(block.Text))
			continue
		}
		fmt.Fprintf(&body, `<w:p><w:r><w:t xml:space="preserve">%s</w:t></w:r></w:p>`, docxEscape(block.Text))
	}
	document := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:body>` + body.String() + `</w:body></w:document>`
	return zipFixture(map[string]string{
		"[Content_Types].xml": `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`,
		"word/document.xml":   document,
	})
}

// docxEscape escapes the XML metacharacters a table cell or paragraph may
// contain; the fixtures use Chinese prose and Markdown pipes.
func docxEscape(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(value)
}

// docxExtractionLayoutFixture reproduces the C-03 UAT layout in memory: an FR
// Heading3 body with 需求描述 + 验收标准/场景 BDD text, then the 7-column 6.1
// requirement list and the 6-column 6.4 catalog that previously overwrote the
// body row.
func docxExtractionLayoutFixture() []byte {
	return docxBlockFixture([]docxBlock{
		{Style: "Heading3", Text: "FR-101 账号密码登录"},
		{Text: "需求编号：FR-101所属功能：账号优先级：P1"},
		{Text: "需求描述：用户可以使用账号密码登录系统。"},
		{Text: "验收标准："},
		{Text: "场景 1：正确密码登录"},
		{Text: "Given 已注册用户 When 输入正确密码 Then 登录成功"},
		{Text: "场景 2：错误密码登录"},
		{Text: "Given 已注册用户 When 输入错误密码 Then 提示密码错误"},
		{Style: "Heading3", Text: "6.1 需求清单总表"},
		{Table: [][]string{
			{"需求编号", "需求名称", "需求描述", "优先级", "需求类型", "当前系统状态", "所属功能"},
			{"FR-001", "账号密码登录", "用户使用账号密码登录", "P1", "功能需求", "已实现", "账号"},
		}},
		{Style: "Heading3", Text: "6.4 延展功能需求清单"},
		{Table: [][]string{
			{"需求编号", "需求名称", "所属功能", "优先级", "验收场景数", "当前系统状态"},
			{"FR-131", "内容排期与工作量看板", "内容管理", "P2", "2", "不存在"},
		}},
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

	return assembledPDF(objects)
}

// assembledPDF renders objects as a valid PDF 1.4 file with an xref table so
// the Go PDF reader can open it.
func assembledPDF(objects []string) []byte {
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

// fixturePNG builds a deterministic PNG image for embedded-image fixtures.
func fixturePNG(width, height int) []byte {
	picture := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			picture.Set(x, y, color.RGBA{R: uint8(20 + x), G: uint8(80 + y), B: 160, A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, picture); err != nil {
		panic(err)
	}
	return buffer.Bytes()
}

const (
	wordNamespace        = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	officeRelNamespace   = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	drawingNamespace     = "http://schemas.openxmlformats.org/drawingml/2006/main"
	presentationNS       = "http://schemas.openxmlformats.org/presentationml/2006/main"
	packageRelNamespace  = "http://schemas.openxmlformats.org/package/2006/relationships"
	spreadsheetNamespace = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
	drawingSpreadsheetNS = "http://schemas.openxmlformats.org/drawingml/2006/spreadsheetDrawing"
)

const relationshipsHeader = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="` + packageRelNamespace + `">`

func relationshipsXML(entries string) string {
	return relationshipsHeader + entries + `</Relationships>`
}

// docxFullyFixture builds a DOCX covering every human-visible element the
// extractor must keep: heading style, body paragraphs, a numbered list, a
// table, an embedded image, header/footer, footnote, endnote and a comment.
func docxFullyFixture() []byte {
	document := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="` + wordNamespace + `" xmlns:r="` + officeRelNamespace + `" xmlns:a="` + drawingNamespace + `">
<w:body>
<w:p><w:pPr><w:pStyle w:val="Heading2"/></w:pPr><w:r><w:t>3.2 用户登录</w:t></w:r></w:p>
<w:p><w:r><w:t>手机号登录方式：输入手机号与验证码。</w:t></w:r></w:p>
<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>手机号</w:t></w:r></w:p>
<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>验证码</w:t></w:r></w:p>
<w:tbl>
<w:tr><w:tc><w:p><w:r><w:t>表格单元格</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>预期结果</w:t></w:r></w:p></w:tc></w:tr>
<w:tr><w:tc><w:p><w:r><w:t>手机号必填</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>展示错误提示</w:t></w:r></w:p></w:tc></w:tr>
</w:tbl>
<w:p><w:r><w:t>登录界面示意：</w:t></w:r><w:r><w:drawing><a:blip r:embed="rId10"/></w:drawing></w:r></w:p>
</w:body></w:document>`
	header := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:hdr xmlns:w="` + wordNamespace + `"><w:p><w:r><w:t>页眉：EzTest 需求文档</w:t></w:r></w:p></w:hdr>`
	footer := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:ftr xmlns:w="` + wordNamespace + `"><w:p><w:r><w:t>页脚：第 1 页</w:t></w:r></w:p></w:ftr>`
	footnotes := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:footnotes xmlns:w="` + wordNamespace + `">
<w:footnote w:type="separator" w:id="-1"><w:p><w:r><w:t>separator</w:t></w:r></w:p></w:footnote>
<w:footnote w:id="1"><w:p><w:r><w:t>脚注：登录必须支持手机号</w:t></w:r></w:p></w:footnote>
</w:footnotes>`
	endnotes := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:endnotes xmlns:w="` + wordNamespace + `">
<w:endnote w:type="separator" w:id="-1"><w:p><w:r><w:t>separator</w:t></w:r></w:p></w:endnote>
<w:endnote w:id="1"><w:p><w:r><w:t>尾注：验收以本表为准</w:t></w:r></w:p></w:endnote>
</w:endnotes>`
	comments := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:comments xmlns:w="` + wordNamespace + `"><w:comment w:id="0" w:author="张三"><w:p><w:r><w:t>批注：补充验证码登录用例</w:t></w:r></w:p></w:comment></w:comments>`
	return zipFixture(map[string]string{
		"[Content_Types].xml":   `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`,
		"word/document.xml":     document,
		"word/header1.xml":      header,
		"word/footer1.xml":      footer,
		"word/footnotes.xml":    footnotes,
		"word/endnotes.xml":     endnotes,
		"word/comments.xml":     comments,
		"word/media/image1.png": string(fixturePNG(8, 6)),
		"word/_rels/document.xml.rels": relationshipsXML(
			`<Relationship Id="rId10" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/image1.png"/>`),
	})
}

// docxAuxiliaryTableFixture has tables in the header and footer parts plus a
// comment with an author. The structured elements must keep this content too:
// element-driven chunking (Task 16) never reads the concatenated Text.
func docxAuxiliaryTableFixture() []byte {
	document := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="` + wordNamespace + `"><w:body>
<w:p><w:r><w:t>正文：登录需求。</w:t></w:r></w:p>
</w:body></w:document>`
	header := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:hdr xmlns:w="` + wordNamespace + `">
<w:p><w:r><w:t>页眉：规格</w:t></w:r></w:p>
<w:tbl><w:tr><w:tc><w:p><w:r><w:t>页眉字段</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>页眉说明</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
</w:hdr>`
	footer := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:ftr xmlns:w="` + wordNamespace + `">
<w:tbl><w:tr><w:tc><w:p><w:r><w:t>页脚字段</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>页脚说明</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
</w:ftr>`
	comments := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:comments xmlns:w="` + wordNamespace + `"><w:comment w:id="0" w:author="张三"><w:p><w:r><w:t>批注：补充边界条件</w:t></w:r></w:p></w:comment></w:comments>`
	return zipFixture(map[string]string{
		"word/document.xml": document,
		"word/header1.xml":  header,
		"word/footer1.xml":  footer,
		"word/comments.xml": comments,
	})
}

// docxSkippedImageFixture embeds an EMF image the parser cannot turn into a
// vision-compatible image reference, so it must be reported with a reason
// instead of being silently dropped.
func docxSkippedImageFixture() []byte {
	document := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="` + wordNamespace + `" xmlns:r="` + officeRelNamespace + `" xmlns:a="` + drawingNamespace + `">
<w:body>
<w:p><w:r><w:t>正文保留</w:t></w:r></w:p>
<w:p><w:r><w:drawing><a:blip r:embed="rId20"/></w:drawing></w:r></w:p>
</w:body></w:document>`
	return zipFixture(map[string]string{
		"word/document.xml":     document,
		"word/media/image1.emf": "\x01\x00\x00\x00EMF",
		"word/_rels/document.xml.rels": relationshipsXML(
			`<Relationship Id="rId20" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/image1.emf"/>`),
	})
}

// docxTableImageFixture embeds an image inside a table cell. Word stores the
// drawing in a cell paragraph, so the image must be collected from the table
// block rather than only from body paragraphs.
func docxTableImageFixture() []byte {
	document := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="` + wordNamespace + `" xmlns:r="` + officeRelNamespace + `" xmlns:a="` + drawingNamespace + `">
<w:body><w:tbl><w:tr>
<w:tc><w:p><w:r><w:t>图标</w:t></w:r><w:r><w:drawing><a:blip r:embed="rId30"/></w:drawing></w:r></w:p></w:tc>
<w:tc><w:p><w:r><w:t>表格中的图片</w:t></w:r></w:p></w:tc>
</w:tr></w:tbl></w:body></w:document>`
	return zipFixture(map[string]string{
		"word/document.xml":     document,
		"word/media/image1.png": string(fixturePNG(5, 5)),
		"word/_rels/document.xml.rels": relationshipsXML(
			`<Relationship Id="rId30" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/image1.png"/>`),
	})
}

// pptxFullyFixture builds a presentation with two slides, a table, a picture
// on slide one and speaker notes for slide one.
func pptxFullyFixture() []byte {
	slideOne := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sld xmlns:p="` + presentationNS + `" xmlns:a="` + drawingNamespace + `" xmlns:r="` + officeRelNamespace + `"><p:cSld><p:spTree>
<p:sp><p:txBody><a:p><a:r><a:t>3.2 用户登录</a:t></a:r></a:p><a:p><a:r><a:t>手机号登录流程</a:t></a:r></a:p></p:txBody></p:sp>
<p:graphicFrame><a:graphic><a:graphicData><a:tbl>
<a:tr><a:tc><a:txBody><a:p><a:r><a:t>表格单元格</a:t></a:r></a:p></a:txBody></a:tc><a:tc><a:txBody><a:p><a:r><a:t>说明</a:t></a:r></a:p></a:txBody></a:tc></a:tr>
</a:tbl></a:graphicData></a:graphic></p:graphicFrame>
<p:pic><p:blipFill><a:blip r:embed="rId2"/></p:blipFill></p:pic>
</p:spTree></p:cSld></p:sld>`
	slideTwo := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sld xmlns:p="` + presentationNS + `" xmlns:a="` + drawingNamespace + `"><p:cSld><p:spTree>
<p:sp><p:txBody><a:p><a:r><a:t>第二页：验收标准</a:t></a:r></a:p></p:txBody></p:sp>
</p:spTree></p:cSld></p:sld>`
	notes := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:notes xmlns:p="` + presentationNS + `" xmlns:a="` + drawingNamespace + `"><p:cSld><p:spTree>
<p:sp><p:txBody><a:p><a:r><a:t>演讲者备注：讲解登录流程</a:t></a:r></a:p></p:txBody></p:sp>
</p:spTree></p:cSld></p:notes>`
	return zipFixture(map[string]string{
		"ppt/slides/slide1.xml":           slideOne,
		"ppt/slides/slide2.xml":           slideTwo,
		"ppt/notesSlides/notesSlide1.xml": notes,
		"ppt/media/image1.png":            string(fixturePNG(10, 5)),
		"ppt/slides/_rels/slide1.xml.rels": relationshipsXML(
			`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="../media/image1.png"/>` +
				`<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesSlide" Target="../notesSlides/notesSlide1.xml"/>`),
	})
}

// xlsxFullyFixture builds a workbook with a visible and a hidden sheet, shared
// and inline strings, cached and uncached formulas, a comment and an embedded
// image.
func xlsxFullyFixture() []byte {
	workbook := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="` + spreadsheetNamespace + `" xmlns:r="` + officeRelNamespace + `"><sheets>
<sheet name="可见表" sheetId="1" r:id="rId1"/>
<sheet name="隐藏表" sheetId="2" state="hidden" r:id="rId2"/>
</sheets></workbook>`
	workbookRels := relationshipsXML(
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>` +
			`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/>`)
	sharedStrings := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<sst xmlns="` + spreadsheetNamespace + `" count="4" uniqueCount="4">
<si><t>需求</t></si><si><t>结果</t></si><si><r><t>缓存</t></r><r><t>值</t></r></si><si><t>说明</t></si>
</sst>`
	sheetOne := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="` + spreadsheetNamespace + `" xmlns:r="` + officeRelNamespace + `"><sheetData>
<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row>
<row r="2"><c r="A2" t="s"><v>2</v></c><c r="B2" t="inlineStr"><is><t>内联字符串</t></is></c></row>
<row r="3"><c r="A3"><f>SUM(A1:A2)</f><v>2</v></c><c r="B3"><f>CONCAT(A1,A2)</f></c></row>
<row r="4"><c r="A4" t="b"><v>1</v></c><c r="B4"><v>42</v></c></row>
</sheetData><drawing r:id="rId2"/></worksheet>`
	sheetOneRels := relationshipsXML(
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/comments" Target="../comments1.xml"/>` +
			`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/drawing" Target="../drawings/drawing1.xml"/>`)
	sheetTwo := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="` + spreadsheetNamespace + `"><sheetData>
<row r="1"><c r="A1" t="inlineStr"><is><t>隐藏 sheet 内容</t></is></c></row>
</sheetData></worksheet>`
	comments := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<comments xmlns="` + spreadsheetNamespace + `"><authors><author>李四</author></authors><commentList>
<comment ref="B2" authorId="0"><text><r><t>批注：需要补充边界条件</t></r></text></comment>
</commentList></comments>`
	drawing := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<xdr:wsDr xmlns:xdr="` + drawingSpreadsheetNS + `" xmlns:a="` + drawingNamespace + `" xmlns:r="` + officeRelNamespace + `">
<xdr:twoCellAnchor><xdr:pic><xdr:blipFill><a:blip r:embed="rId1"/></xdr:blipFill></xdr:pic></xdr:twoCellAnchor>
</xdr:wsDr>`
	drawingRels := relationshipsXML(
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="../media/image1.png"/>`)
	return zipFixture(map[string]string{
		"xl/workbook.xml":                     workbook,
		"xl/_rels/workbook.xml.rels":          workbookRels,
		"xl/sharedStrings.xml":                sharedStrings,
		"xl/worksheets/sheet1.xml":            sheetOne,
		"xl/worksheets/_rels/sheet1.xml.rels": sheetOneRels,
		"xl/worksheets/sheet2.xml":            sheetTwo,
		"xl/comments1.xml":                    comments,
		"xl/drawings/drawing1.xml":            drawing,
		"xl/drawings/_rels/drawing1.xml.rels": drawingRels,
		"xl/media/image1.png":                 string(fixturePNG(7, 4)),
	})
}

const htmlFullyFixture = `<!doctype html>
<html><head><title>不可见标题</title><style>.hidden{color:red}</style></head>
<body>
<h1>需求概述</h1>
<p>这是 <strong>可见</strong> 文本 &amp; 实体。</p>
<h2>3.2 用户登录</h2>
<ul><li>手机号</li><li>验证码</li></ul>
<table><tr><th>字段</th><th>说明</th></tr><tr><td>手机号</td><td>表格单元格</td></tr></table>
<p><img src="/assets/login.png" alt="登录流程图"></p>
<script>var secret = "脚本不可见";</script>
</body></html>`

// pdfFullyFixture builds a one-page PDF with a text layer and a FlateDecode
// RGB image XObject.
func pdfFullyFixture() []byte {
	return pdfWithImageObject("FlateDecode", deflateBytes([]byte{
		255, 0, 0, 0, 255, 0, 0, 0, 255, 255, 255, 255,
	}), "/ColorSpace /DeviceRGB")
}

// pdfSkippedImageFixture embeds an image object with a filter the parser does
// not decode (JPEG 2000). It must surface as skipped with that reason.
func pdfSkippedImageFixture() []byte {
	return pdfWithImageObject("JPXDecode", []byte("JPX!"), "/ColorSpace /DeviceRGB")
}

func pdfWithImageObject(filter string, stream []byte, extraDict string) []byte {
	image := fmt.Sprintf(`<< /Type /XObject /Subtype /Image /Width 2 /Height 2 /BitsPerComponent 8 %s /Filter /%s /Length %d >>
stream
%s
endstream`, extraDict, filter, len(stream), string(stream))
	content := "BT /F1 12 Tf 72 720 Td (PDF image page) Tj ET"
	objects := []string{
		`<< /Type /Catalog /Pages 2 0 R >>`,
		`<< /Type /Pages /Kids [3 0 R] /Count 1 >>`,
		`<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> /XObject << /Im1 6 0 R >> >> >>`,
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		fmt.Sprintf(`<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /FirstChar 32 /LastChar 126 /Widths [%s] >>`, helveticaWidths()),
		image,
	}
	return assembledPDF(objects)
}

func deflateBytes(data []byte) []byte {
	var buffer bytes.Buffer
	writer := zlib.NewWriter(&buffer)
	if _, err := writer.Write(data); err != nil {
		panic(err)
	}
	if err := writer.Close(); err != nil {
		panic(err)
	}
	return buffer.Bytes()
}

// docxDuplicateImageFixture references the same media part twice (two
// paragraphs share one r:embed id). The image is one document element, so it
// must be extracted exactly once.
func docxDuplicateImageFixture() []byte {
	document := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="` + wordNamespace + `" xmlns:r="` + officeRelNamespace + `" xmlns:a="` + drawingNamespace + `">
<w:body>
<w:p><w:r><w:t>第一处图片引用</w:t></w:r><w:r><w:drawing><a:blip r:embed="rId40"/></w:drawing></w:r></w:p>
<w:p><w:r><w:t>第二处图片引用</w:t></w:r><w:r><w:drawing><a:blip r:embed="rId40"/></w:drawing></w:r></w:p>
</w:body></w:document>`
	return zipFixture(map[string]string{
		"word/document.xml":     document,
		"word/media/image1.png": string(fixturePNG(6, 4)),
		"word/_rels/document.xml.rels": relationshipsXML(
			`<Relationship Id="rId40" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/image1.png"/>`),
	})
}

// docxDanglingImageFixture references an image through a relationship whose
// Target is empty, so the image cannot be resolved and must be reported as a
// skip with a reason instead of being silently dropped.
func docxDanglingImageFixture() []byte {
	document := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="` + wordNamespace + `" xmlns:r="` + officeRelNamespace + `" xmlns:a="` + drawingNamespace + `">
<w:body>
<w:p><w:r><w:t>正文保留</w:t></w:r><w:r><w:drawing><a:blip r:embed="rId99"/></w:drawing></w:r></w:p>
</w:body></w:document>`
	return zipFixture(map[string]string{
		"word/document.xml": document,
		"word/_rels/document.xml.rels": relationshipsXML(
			`<Relationship Id="rId99" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target=""/>`),
	})
}

// pptxDanglingImageFixture is the presentation equivalent: the slide points at
// a relationship without a target, which must surface as a reasoned skip.
func pptxDanglingImageFixture() []byte {
	slide := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sld xmlns:p="` + presentationNS + `" xmlns:a="` + drawingNamespace + `" xmlns:r="` + officeRelNamespace + `"><p:cSld><p:spTree>
<p:sp><p:txBody><a:p><a:r><a:t>幻灯片正文</a:t></a:r></a:p></p:txBody></p:sp>
<p:pic><p:blipFill><a:blip r:embed="rId9"/></p:blipFill></p:pic>
</p:spTree></p:cSld></p:sld>`
	return zipFixture(map[string]string{
		"ppt/slides/slide1.xml": slide,
		"ppt/slides/_rels/slide1.xml.rels": relationshipsXML(
			`<Relationship Id="rId9" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target=""/>`),
	})
}

// xlsxThreadedCommentFixture has both a legacy comment (parsed) and a threaded
// comment (not parsed by the legacy comment parser). The threaded part must be
// reported as a skip with a reason instead of vanishing silently.
func xlsxThreadedCommentFixture() []byte {
	const sheet = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="` + spreadsheetNamespace + `"><sheetData>
<row r="1"><c r="A1" t="inlineStr"><is><t>线程批注表</t></is></c></row>
</sheetData></worksheet>`
	sheetRels := relationshipsXML(
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/comments" Target="../comments1.xml"/>` +
			`<Relationship Id="rId2" Type="http://schemas.microsoft.com/office/2017/10/relationships/threadedComment" Target="../threadedComments/threadedComment1.xml"/>`)
	const comments = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<comments xmlns="` + spreadsheetNamespace + `"><authors><author>王五</author></authors><commentList>
<comment ref="A1" authorId="0"><text><r><t>传统批注：需要补充边界条件</t></r></text></comment>
</commentList></comments>`
	const threaded = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<ThreadedComments xmlns="http://schemas.microsoft.com/office/spreadsheetml/2018/threadedcomments">
<threadedComment ref="A1" id="{11111111-1111-1111-1111-111111111111}" personId="{22222222-2222-2222-2222-222222222222}">
<text>线程批注：现代 Excel 讨论串</text>
</threadedComment></ThreadedComments>`
	return zipFixture(map[string]string{
		"xl/worksheets/sheet1.xml":                 sheet,
		"xl/worksheets/_rels/sheet1.xml.rels":      sheetRels,
		"xl/comments1.xml":                         comments,
		"xl/threadedComments/threadedComment1.xml": threaded,
	})
}

// pdfMixedScanFixture has a text page followed by a page without a text layer
// (a scanned page), so the second page must be reported as skipped.
func pdfMixedScanFixture() []byte {
	return pdfFixture([]string{"Mixed PDF first page text", ""})
}

// pdfOversizedImageFixture embeds a FlateDecode image whose decoded payload is
// larger than the decompressed-entry limit. The compressed object itself is
// small, so this cannot be caught by the upload limit.
func pdfOversizedImageFixture() []byte {
	payload := make([]byte, int(version.MaxOOXMLBytes)+1)
	return pdfWithImageObject("FlateDecode", deflateBytes(payload), "/ColorSpace /DeviceRGB")
}

// pdfManyImagesFixture embeds count image XObjects on one text page so the
// MaxPDFImageObjects scan limit can be exercised.
func pdfManyImagesFixture(count int) []byte {
	content := "BT /F1 12 Tf 72 720 Td (many image objects) Tj ET"
	objects := []string{
		`<< /Type /Catalog /Pages 2 0 R >>`,
		`<< /Type /Pages /Kids [3 0 R] /Count 1 >>`,
		`<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>`,
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		fmt.Sprintf(`<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /FirstChar 32 /LastChar 126 /Widths [%s] >>`, helveticaWidths()),
	}
	for index := 0; index < count; index++ {
		stream := "JPX!"
		objects = append(objects, fmt.Sprintf(`<< /Type /XObject /Subtype /Image /Width 1 /Height 1 /BitsPerComponent 8 /ColorSpace /DeviceRGB /Filter /JPXDecode /Length %d >>
stream
%s
endstream`, len(stream), stream))
	}
	return assembledPDF(objects)
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
