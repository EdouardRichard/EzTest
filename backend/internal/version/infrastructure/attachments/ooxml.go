package attachments

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
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

func findArchiveEntry(archive *zip.Reader, name string) (*zip.File, bool) {
	for _, file := range archive.File {
		if file.Name == name {
			return file, true
		}
	}
	return nil, false
}

func readZipEntry(file *zip.File) ([]byte, error) {
	return readZipEntryLimited(file, version.MaxOOXMLBytes)
}

func readZipEntryLimited(file *zip.File, limit int64) ([]byte, error) {
	if file.UncompressedSize64 > uint64(limit) {
		return nil, fmt.Errorf("%w: archive entry %s exceeds the limit", ErrTooLarge, file.Name)
	}
	reader, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: open archive entry: %v", ErrCorrupt, err)
	}
	defer reader.Close()
	raw, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read archive entry: %v", ErrCorrupt, err)
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("%w: archive entry %s exceeds the limit", ErrTooLarge, file.Name)
	}
	return raw, nil
}

// partRelsPath maps word/document.xml to word/_rels/document.xml.rels.
func partRelsPath(part string) string {
	dir, base := path.Split(part)
	return path.Join(dir, "_rels", base+".rels")
}

// resolvePartPath resolves an OPC relationship target against the directory of
// the part that owns the relationship. Absolute targets (leading /) are package
// absolute; "../media/x" is normalized.
func resolvePartPath(baseDir, target string) string {
	target = strings.TrimSpace(target)
	if strings.HasPrefix(target, "/") {
		return path.Clean(strings.TrimPrefix(target, "/"))
	}
	return path.Clean(path.Join(baseDir, target))
}

// readRelationships parses one OPC .rels part. A missing rels part is normal
// (a package without images/notes) and yields an empty map.
func readRelationships(archive *zip.Reader, name string) (map[string]string, error) {
	relations := make(map[string]string)
	file, ok := findArchiveEntry(archive, name)
	if !ok {
		return relations, nil
	}
	raw, err := readZipEntry(file)
	if err != nil {
		return nil, err
	}
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: decode relationships: %v", ErrCorrupt, err)
		}
		if start, ok := token.(xml.StartElement); ok && start.Name.Local == "Relationship" {
			id := xmlAttribute(start, "Id")
			target := xmlAttribute(start, "Target")
			if id != "" && target != "" {
				relations[id] = target
			}
		}
	}
	return relations, nil
}

func sortedRelationshipTargets(relations map[string]string) []string {
	targets := make([]string, 0, len(relations))
	for _, target := range relations {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	return targets
}

func xmlAttribute(element xml.StartElement, name string) string {
	for _, attribute := range element.Attr {
		if attribute.Name.Local == name {
			return attribute.Value
		}
	}
	return ""
}

// ---------- DOCX ----------

type wordParagraph struct {
	Text         string
	Style        string
	OutlineLevel int // -1 when the paragraph has no w:outlineLvl
	List         bool
	ImageIDs     []string
}

type wordBlock struct {
	Kind          string // "paragraph" or "table"
	Paragraph     wordParagraph
	Rows          [][]string
	ImageIDs      []string // table-cell images (paragraph images live in Paragraph)
	ContainerKind string   // footnote/endnote/comment
	ContainerID   string
	ContainerType string
	Author        string
}

type wordImageRef struct {
	Part string
	ID   string
}

// parseWordPart walks one wordprocessingml part. Paragraphs, table rows/cells,
// headers/footers, notes and comments all share this parser; notes/comments are
// tagged with their container so the caller can render them as separate
// sections.
func parseWordPart(raw []byte) ([]wordBlock, error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	var (
		blocks     []wordBlock
		para       *wordParagraph
		paraText   strings.Builder
		table      *wordTable
		tableImage []string
		row        []string
		cell       *strings.Builder
		inText     bool
		deleted    int
		container  wordContainerTag
	)
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: decode wordprocessingml: %v", ErrCorrupt, err)
		}
		switch element := token.(type) {
		case xml.StartElement:
			switch element.Name.Local {
			case "p":
				if para == nil {
					para = &wordParagraph{OutlineLevel: -1}
					paraText.Reset()
				}
			case "pStyle":
				if para != nil {
					para.Style = xmlAttribute(element, "val")
				}
			case "outlineLvl":
				if para != nil {
					if level, err := strconv.Atoi(xmlAttribute(element, "val")); err == nil {
						para.OutlineLevel = level
					}
				}
			case "numPr":
				if para != nil {
					para.List = true
				}
			case "t":
				inText = true
			case "tab":
				if para != nil {
					paraText.WriteString("\t")
				}
			case "br", "cr":
				if para != nil {
					paraText.WriteString("\n")
				}
			case "del":
				deleted++
			case "blip":
				if para != nil {
					if id := xmlAttribute(element, "embed"); id != "" {
						para.ImageIDs = append(para.ImageIDs, id)
					}
				}
			case "imagedata":
				if para != nil {
					if id := xmlAttribute(element, "id"); id != "" {
						para.ImageIDs = append(para.ImageIDs, id)
					}
				}
			case "tbl":
				table = &wordTable{}
				tableImage = nil
			case "tr":
				row = nil
			case "tc":
				cell = &strings.Builder{}
			case "footnote", "endnote":
				container = wordContainerTag{
					Kind: element.Name.Local, ID: xmlAttribute(element, "id"), Type: xmlAttribute(element, "type"),
				}
			case "comment":
				container = wordContainerTag{
					Kind: "comment", ID: xmlAttribute(element, "id"), Author: xmlAttribute(element, "author"),
				}
			}
		case xml.CharData:
			if inText && deleted == 0 && para != nil {
				paraText.Write([]byte(element))
			}
		case xml.EndElement:
			switch element.Name.Local {
			case "t":
				inText = false
			case "del":
				if deleted > 0 {
					deleted--
				}
			case "p":
				if para != nil {
					text := cleanWordText(paraText.String())
					para.Text = text
					switch {
					case cell != nil:
						if text != "" {
							if cell.Len() > 0 {
								cell.WriteString(" ")
							}
							cell.WriteString(text)
						}
						tableImage = append(tableImage, para.ImageIDs...)
					case table != nil:
						// A paragraph directly inside a table (invalid OOXML)
						// has no cell to live in; ignore it.
					default:
						if text != "" || len(para.ImageIDs) > 0 {
							blocks = append(blocks, wordBlock{
								Kind: "paragraph", Paragraph: *para,
								ContainerKind: container.Kind, ContainerID: container.ID,
								ContainerType: container.Type, Author: container.Author,
							})
						}
					}
					para = nil
				}
			case "tc":
				if cell != nil {
					row = append(row, strings.TrimSpace(cell.String()))
					cell = nil
				}
			case "tr":
				if table != nil {
					table.Rows = append(table.Rows, row)
					row = nil
				}
			case "tbl":
				if table != nil {
					blocks = append(blocks, wordBlock{
						Kind: "table", Rows: table.Rows, ImageIDs: tableImage,
						ContainerKind: container.Kind, ContainerID: container.ID,
						ContainerType: container.Type, Author: container.Author,
					})
					table = nil
					tableImage = nil
				}
			case "footnote", "endnote", "comment":
				container = wordContainerTag{}
			}
		}
	}
	return blocks, nil
}

type wordTable struct {
	Rows [][]string
}

type wordContainerTag struct {
	Kind   string
	ID     string
	Type   string
	Author string
}

func cleanWordText(text string) string {
	text = strings.TrimSpace(text)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return text
}

// wordHeadingLevel maps a Word paragraph style or outline level to a Markdown
// heading level. Word writes both "Heading 2"/"标题 2" styles and numeric
// style ids ("2") depending on locale, so all shapes are accepted.
func wordHeadingLevel(style string, outlineLevel int) int {
	if outlineLevel >= 0 && outlineLevel <= 8 {
		return outlineLevel + 1
	}
	normalized := strings.ToLower(strings.TrimSpace(style))
	normalized = strings.ReplaceAll(normalized, " ", "")
	normalized = strings.ReplaceAll(normalized, "-", "")
	normalized = strings.ReplaceAll(normalized, "_", "")
	switch normalized {
	case "heading1", "标题1", "1":
		return 1
	case "heading2", "标题2", "2":
		return 2
	case "heading3", "标题3", "3":
		return 3
	case "heading4", "标题4", "4":
		return 4
	case "heading5", "标题5", "5":
		return 5
	case "heading6", "标题6", "6":
		return 6
	case "heading7", "标题7", "7":
		return 7
	case "heading8", "标题8", "8":
		return 8
	case "heading9", "标题9", "9":
		return 9
	default:
		return 0
	}
}

// appendWordBlocks renders parsed wordprocessingml blocks into the extraction
// result. headings switches on Markdown heading rendering and heading_path
// tracking for the document body.
func appendWordBlocks(builder *resultBuilder, blocks []wordBlock, source string, headings bool, imageRefs *[]wordImageRef) error {
	headingStack := make([]string, 0, 4)
	for _, block := range blocks {
		if block.Kind == "table" {
			if len(block.Rows) == 0 {
				continue
			}
			for _, id := range block.ImageIDs {
				*imageRefs = append(*imageRefs, wordImageRef{Part: source, ID: id})
			}
			headingPath := renderHeadingPath(headingStack)
			for _, row := range block.Rows {
				cells := append([]string(nil), row...)
				if err := builder.element(versionapp.ExtractionElement{
					Kind: "table_row", Text: strings.Join(cells, " | "), HeadingPath: headingPath,
					Source: source, Row: cells,
				}); err != nil {
					return err
				}
			}
			if err := builder.addText(renderMarkdownTable(block.Rows) + "\n"); err != nil {
				return err
			}
			continue
		}
		paragraph := block.Paragraph
		text := strings.TrimSpace(paragraph.Text)
		if len(paragraph.ImageIDs) > 0 {
			for _, id := range paragraph.ImageIDs {
				*imageRefs = append(*imageRefs, wordImageRef{Part: source, ID: id})
			}
		}
		if text == "" {
			continue
		}
		level := 0
		if headings {
			level = wordHeadingLevel(paragraph.Style, paragraph.OutlineLevel)
		}
		kind := "paragraph"
		prefix := ""
		headingPath := renderHeadingPath(headingStack)
		if level > 0 {
			kind = "heading"
			if level > len(headingStack) {
				for len(headingStack) < level {
					headingStack = append(headingStack, "")
				}
			} else {
				headingStack = headingStack[:level]
			}
			headingStack[level-1] = text
			headingPath = renderHeadingPath(headingStack)
			prefix = strings.Repeat("#", level) + " "
		} else if paragraph.List {
			kind = "list_item"
			prefix = "- "
		}
		if err := builder.element(versionapp.ExtractionElement{
			Kind: kind, Text: text, Level: level, HeadingPath: headingPath, Source: source,
		}); err != nil {
			return err
		}
		if err := builder.addText(prefix + text + "\n"); err != nil {
			return err
		}
	}
	return nil
}

func renderMarkdownTable(rows [][]string) string {
	width := 0
	for _, row := range rows {
		if len(row) > width {
			width = len(row)
		}
	}
	var builder strings.Builder
	builder.WriteString("\n")
	for index, row := range rows {
		builder.WriteString("|")
		for column := 0; column < width; column++ {
			cell := ""
			if column < len(row) {
				cell = escapeMarkdownCell(row[column])
			}
			builder.WriteString(" ")
			builder.WriteString(cell)
			builder.WriteString(" |")
		}
		builder.WriteString("\n")
		if index == 0 {
			builder.WriteString("|")
			for column := 0; column < width; column++ {
				builder.WriteString(" --- |")
			}
			builder.WriteString("\n")
		}
	}
	return builder.String()
}

func escapeMarkdownCell(cell string) string {
	cell = strings.ReplaceAll(cell, "|", "\\|")
	cell = strings.ReplaceAll(cell, "\n", "<br>")
	return strings.TrimSpace(cell)
}

// extractDOCXResult extracts the document body plus every auxiliary Word part:
// headers/footers, footnotes/endnotes, comments and word/media images.
func extractDOCXResult(data []byte) (versionapp.ExtractionResult, error) {
	archive, err := openOOXML(data, "docx")
	if err != nil {
		return versionapp.ExtractionResult{}, err
	}
	document, ok := findArchiveEntry(archive, "word/document.xml")
	if !ok {
		return versionapp.ExtractionResult{}, fmt.Errorf("%w: word/document.xml is missing", ErrCorrupt)
	}
	raw, err := readZipEntry(document)
	if err != nil {
		return versionapp.ExtractionResult{}, err
	}
	blocks, err := parseWordPart(raw)
	if err != nil {
		return versionapp.ExtractionResult{}, err
	}
	builder := newResultBuilder()
	imageRefs := make([]wordImageRef, 0)
	if err := appendWordBlocks(builder, blocks, document.Name, true, &imageRefs); err != nil {
		return versionapp.ExtractionResult{}, err
	}
	for _, header := range numberedXMLFiles(archive, "word/header", ".xml") {
		if err := appendWordSectionPart(builder, archive, header.file, "header", "页眉", &imageRefs); err != nil {
			return versionapp.ExtractionResult{}, err
		}
	}
	for _, footer := range numberedXMLFiles(archive, "word/footer", ".xml") {
		if err := appendWordSectionPart(builder, archive, footer.file, "footer", "页脚", &imageRefs); err != nil {
			return versionapp.ExtractionResult{}, err
		}
	}
	if err := appendWordNotesPart(builder, archive, "word/footnotes.xml", "footnote", "脚注", &imageRefs); err != nil {
		return versionapp.ExtractionResult{}, err
	}
	if err := appendWordNotesPart(builder, archive, "word/endnotes.xml", "endnote", "尾注", &imageRefs); err != nil {
		return versionapp.ExtractionResult{}, err
	}
	if err := appendWordNotesPart(builder, archive, "word/comments.xml", "comment", "批注", &imageRefs); err != nil {
		return versionapp.ExtractionResult{}, err
	}
	if err := appendWordImages(builder, archive, imageRefs); err != nil {
		return versionapp.ExtractionResult{}, err
	}
	return builder.result(), nil
}

func appendWordSectionPart(builder *resultBuilder, archive *zip.Reader, file *zip.File, kind, marker string, imageRefs *[]wordImageRef) error {
	raw, err := readZipEntry(file)
	if err != nil {
		return err
	}
	blocks, err := parseWordPart(raw)
	if err != nil {
		return err
	}
	for _, block := range blocks {
		if block.Kind == "table" {
			if len(block.Rows) == 0 {
				continue
			}
			for _, id := range block.ImageIDs {
				*imageRefs = append(*imageRefs, wordImageRef{Part: file.Name, ID: id})
			}
			if err := builder.addText(renderMarkdownTable(block.Rows)); err != nil {
				return err
			}
			continue
		}
		for _, id := range block.Paragraph.ImageIDs {
			*imageRefs = append(*imageRefs, wordImageRef{Part: file.Name, ID: id})
		}
		text := strings.TrimSpace(block.Paragraph.Text)
		if text == "" {
			continue
		}
		if err := builder.element(versionapp.ExtractionElement{Kind: kind, Text: text, Source: file.Name}); err != nil {
			return err
		}
		if err := builder.addText("[" + marker + "] " + text + "\n"); err != nil {
			return err
		}
	}
	return nil
}

func appendWordNotesPart(builder *resultBuilder, archive *zip.Reader, name, kind, marker string, imageRefs *[]wordImageRef) error {
	file, ok := findArchiveEntry(archive, name)
	if !ok {
		return nil
	}
	raw, err := readZipEntry(file)
	if err != nil {
		return err
	}
	blocks, err := parseWordPart(raw)
	if err != nil {
		return err
	}
	type entry struct {
		id     string
		author string
		texts  []string
	}
	ordered := make([]*entry, 0)
	byID := make(map[string]*entry)
	for _, block := range blocks {
		switch block.ContainerType {
		case "separator", "continuationSeparator", "continuationNotice":
			continue
		}
		if block.ContainerID == "" {
			continue
		}
		var text string
		switch block.Kind {
		case "paragraph":
			text = strings.TrimSpace(block.Paragraph.Text)
			for _, id := range block.Paragraph.ImageIDs {
				*imageRefs = append(*imageRefs, wordImageRef{Part: name, ID: id})
			}
		case "table":
			lines := make([]string, 0, len(block.Rows))
			for _, row := range block.Rows {
				lines = append(lines, strings.Join(row, " | "))
			}
			text = strings.TrimSpace(strings.Join(lines, "\n"))
			for _, id := range block.ImageIDs {
				*imageRefs = append(*imageRefs, wordImageRef{Part: name, ID: id})
			}
		}
		if text == "" {
			continue
		}
		item := byID[block.ContainerID]
		if item == nil {
			item = &entry{id: block.ContainerID, author: block.Author}
			byID[block.ContainerID] = item
			ordered = append(ordered, item)
		}
		item.texts = append(item.texts, text)
	}
	for _, item := range ordered {
		text := strings.Join(item.texts, " ")
		display := text
		if kind == "comment" && item.author != "" {
			display = item.author + ": " + text
		}
		if err := builder.element(versionapp.ExtractionElement{Kind: kind, Text: text, Source: name}); err != nil {
			return err
		}
		if err := builder.addText("[" + marker + " " + item.id + "] " + display + "\n"); err != nil {
			return err
		}
	}
	return nil
}

func appendWordImages(builder *resultBuilder, archive *zip.Reader, refs []wordImageRef) error {
	seen := make(map[string]bool)
	relsCache := make(map[string]map[string]string)
	for _, reference := range refs {
		relsPath := partRelsPath(reference.Part)
		relations, ok := relsCache[relsPath]
		if !ok {
			loaded, err := readRelationships(archive, relsPath)
			if err != nil {
				return err
			}
			relations = loaded
			relsCache[relsPath] = relations
		}
		target := relations[reference.ID]
		if target == "" {
			if err := builder.skip("image", reference.Part+"#"+reference.ID, "image relationship has no target"); err != nil {
				return err
			}
			continue
		}
		mediaPath := resolvePartPath(path.Dir(reference.Part), target)
		// The same media part may be referenced from several paragraphs,
		// table cells or sections; it is one document image, so extract it
		// once instead of duplicating the element and the image bytes.
		if seen[mediaPath] {
			continue
		}
		file, ok := findArchiveEntry(archive, mediaPath)
		if !ok {
			if err := builder.skip("image", mediaPath, "referenced image is missing from the package"); err != nil {
				return err
			}
			seen[mediaPath] = true
			continue
		}
		raw, err := readZipEntryLimited(file, version.MaxAttachmentBytes)
		if err != nil {
			return err
		}
		if err := builder.addEmbeddedImage(mediaPath, path.Base(mediaPath), raw); err != nil {
			return err
		}
		seen[mediaPath] = true
	}
	for _, file := range archive.File {
		if !strings.HasPrefix(file.Name, "word/media/") || seen[file.Name] {
			continue
		}
		raw, err := readZipEntryLimited(file, version.MaxAttachmentBytes)
		if err != nil {
			return err
		}
		if err := builder.addEmbeddedImage(file.Name, path.Base(file.Name), raw); err != nil {
			return err
		}
	}
	return nil
}

// ---------- PPTX ----------

type drawingPart struct {
	Paragraphs []string
	Tables     [][][]string
	ImageIDs   []string
}

// parseDrawingPart extracts text paragraphs, table rows and image relationship
// ids from one DrawingML part (slide or notes slide).
func parseDrawingPart(raw []byte) (drawingPart, error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	part := drawingPart{}
	var (
		paragraph    strings.Builder
		inText       bool
		inTable      bool
		tables       [][][]string
		currentTable [][]string
		row          []string
		cell         *strings.Builder
	)
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return drawingPart{}, fmt.Errorf("%w: decode presentationml: %v", ErrCorrupt, err)
		}
		switch element := token.(type) {
		case xml.StartElement:
			switch element.Name.Local {
			case "tbl":
				inTable = true
				currentTable = nil
			case "tr":
				row = nil
			case "tc":
				cell = &strings.Builder{}
			case "p":
				paragraph.Reset()
			case "t":
				inText = true
			case "br":
				paragraph.WriteString("\n")
			case "blip":
				if id := xmlAttribute(element, "embed"); id != "" {
					part.ImageIDs = append(part.ImageIDs, id)
				}
			}
		case xml.CharData:
			if inText {
				paragraph.Write([]byte(element))
			}
		case xml.EndElement:
			switch element.Name.Local {
			case "t":
				inText = false
			case "p":
				text := strings.TrimSpace(paragraph.String())
				if text != "" {
					if inTable && cell != nil {
						if cell.Len() > 0 {
							cell.WriteString(" ")
						}
						cell.WriteString(text)
					} else {
						part.Paragraphs = append(part.Paragraphs, text)
					}
				}
			case "tc":
				if cell != nil {
					row = append(row, strings.TrimSpace(cell.String()))
					cell = nil
				}
			case "tr":
				if inTable {
					currentTable = append(currentTable, row)
					row = nil
				}
			case "tbl":
				if inTable && len(currentTable) > 0 {
					tables = append(tables, currentTable)
				}
				currentTable = nil
				inTable = false
			}
		}
	}
	part.Tables = tables
	return part, nil
}

func extractPPTXResult(data []byte) (versionapp.ExtractionResult, error) {
	archive, err := openOOXML(data, "pptx")
	if err != nil {
		return versionapp.ExtractionResult{}, err
	}
	slides := numberedXMLFiles(archive, "ppt/slides/slide", ".xml")
	if len(slides) == 0 {
		return versionapp.ExtractionResult{}, fmt.Errorf("%w: no presentation slides found", ErrCorrupt)
	}
	builder := newResultBuilder()
	seenMedia := make(map[string]bool)
	for _, slide := range slides {
		raw, err := readZipEntry(slide.file)
		if err != nil {
			return versionapp.ExtractionResult{}, err
		}
		part, err := parseDrawingPart(raw)
		if err != nil {
			return versionapp.ExtractionResult{}, err
		}
		if err := builder.element(versionapp.ExtractionElement{
			Kind: "slide", Text: fmt.Sprintf("Slide %d", slide.number), Slide: slide.number, Source: slide.file.Name,
		}); err != nil {
			return versionapp.ExtractionResult{}, err
		}
		if err := builder.addText(fmt.Sprintf("## Slide %d\n", slide.number)); err != nil {
			return versionapp.ExtractionResult{}, err
		}
		for _, paragraph := range part.Paragraphs {
			if err := builder.element(versionapp.ExtractionElement{
				Kind: "paragraph", Text: paragraph, Slide: slide.number, Source: slide.file.Name,
			}); err != nil {
				return versionapp.ExtractionResult{}, err
			}
			if err := builder.addText(paragraph + "\n"); err != nil {
				return versionapp.ExtractionResult{}, err
			}
		}
		for _, table := range part.Tables {
			for _, row := range table {
				cells := append([]string(nil), row...)
				if err := builder.element(versionapp.ExtractionElement{
					Kind: "table_row", Text: strings.Join(cells, " | "), Row: cells,
					Slide: slide.number, Source: slide.file.Name,
				}); err != nil {
					return versionapp.ExtractionResult{}, err
				}
			}
			if err := builder.addText(renderMarkdownTable(table) + "\n"); err != nil {
				return versionapp.ExtractionResult{}, err
			}
		}
		relations, err := readRelationships(archive, partRelsPath(slide.file.Name))
		if err != nil {
			return versionapp.ExtractionResult{}, err
		}
		for _, id := range part.ImageIDs {
			target := relations[id]
			if target == "" {
				if err := builder.skip("image", slide.file.Name+"#"+id, "image relationship has no target"); err != nil {
					return versionapp.ExtractionResult{}, err
				}
				continue
			}
			mediaPath := resolvePartPath(path.Dir(slide.file.Name), target)
			if seenMedia[mediaPath] {
				continue
			}
			if err := appendMediaImage(builder, archive, mediaPath); err != nil {
				return versionapp.ExtractionResult{}, err
			}
			seenMedia[mediaPath] = true
		}
		// Speaker notes are related to their slide by a notesSlide
		// relationship; numbers alone are not reliable, so resolve the rels
		// target instead of guessing notesSlideN.xml.
		for _, target := range sortedRelationshipTargets(relations) {
			if !strings.Contains(strings.ToLower(target), "notesslide") {
				continue
			}
			notesPath := resolvePartPath(path.Dir(slide.file.Name), target)
			notesFile, ok := findArchiveEntry(archive, notesPath)
			if !ok {
				if err := builder.skip("note", notesPath, "speaker notes part is missing"); err != nil {
					return versionapp.ExtractionResult{}, err
				}
				continue
			}
			notesRaw, err := readZipEntry(notesFile)
			if err != nil {
				return versionapp.ExtractionResult{}, err
			}
			notes, err := parseDrawingPart(notesRaw)
			if err != nil {
				return versionapp.ExtractionResult{}, err
			}
			notesRelations, err := readRelationships(archive, partRelsPath(notesPath))
			if err != nil {
				return versionapp.ExtractionResult{}, err
			}
			for _, id := range notes.ImageIDs {
				target := notesRelations[id]
				if target == "" {
					if err := builder.skip("image", notesPath+"#"+id, "image relationship has no target"); err != nil {
						return versionapp.ExtractionResult{}, err
					}
					continue
				}
				mediaPath := resolvePartPath(path.Dir(notesPath), target)
				if seenMedia[mediaPath] {
					continue
				}
				if err := appendMediaImage(builder, archive, mediaPath); err != nil {
					return versionapp.ExtractionResult{}, err
				}
				seenMedia[mediaPath] = true
			}
			text := strings.Join(notes.Paragraphs, " ")
			if text == "" {
				continue
			}
			if err := builder.element(versionapp.ExtractionElement{
				Kind: "note", Text: text, Slide: slide.number, Source: notesPath,
			}); err != nil {
				return versionapp.ExtractionResult{}, err
			}
			if err := builder.addText("[备注] " + text + "\n"); err != nil {
				return versionapp.ExtractionResult{}, err
			}
		}
	}
	for _, file := range archive.File {
		if !strings.HasPrefix(file.Name, "ppt/media/") || seenMedia[file.Name] {
			continue
		}
		if err := appendMediaImage(builder, archive, file.Name); err != nil {
			return versionapp.ExtractionResult{}, err
		}
	}
	return builder.result(), nil
}

func appendMediaImage(builder *resultBuilder, archive *zip.Reader, name string) error {
	file, ok := findArchiveEntry(archive, name)
	if !ok {
		return builder.skip("image", name, "media part is missing")
	}
	raw, err := readZipEntryLimited(file, version.MaxAttachmentBytes)
	if err != nil {
		return err
	}
	return builder.addEmbeddedImage(name, path.Base(name), raw)
}

// ---------- XLSX ----------

type workbookSheet struct {
	Name   string
	File   string
	Hidden bool
}

// readWorkbookSheets reads the sheet list (name/visibility) and resolves each
// relationship to its worksheet part. A workbook without this metadata falls
// back to the numbered worksheets in the caller.
func readWorkbookSheets(archive *zip.Reader) ([]workbookSheet, error) {
	file, ok := findArchiveEntry(archive, "xl/workbook.xml")
	if !ok {
		return nil, nil
	}
	raw, err := readZipEntry(file)
	if err != nil {
		return nil, err
	}
	relations, err := readRelationships(archive, "xl/_rels/workbook.xml.rels")
	if err != nil {
		return nil, err
	}
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	sheets := make([]workbookSheet, 0)
	index := 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: decode workbook: %v", ErrCorrupt, err)
		}
		if start, ok := token.(xml.StartElement); ok && start.Name.Local == "sheet" {
			index++
			name := xmlAttribute(start, "name")
			if name == "" {
				name = fmt.Sprintf("Sheet%d", index)
			}
			state := strings.ToLower(xmlAttribute(start, "state"))
			sheet := workbookSheet{
				Name:   name,
				Hidden: state == "hidden" || state == "veryhidden",
			}
			target := relations[xmlAttribute(start, "id")]
			if target == "" {
				target = fmt.Sprintf("worksheets/sheet%d.xml", index)
			}
			sheet.File = resolvePartPath("xl", target)
			sheets = append(sheets, sheet)
		}
	}
	return sheets, nil
}

// parseSheetRows renders cell display values row by row. Column references are
// honoured so empty cells keep the table shape. A formula without a cached
// value is returned as its formula text marked with [formula].
func parseSheetRows(raw []byte, shared []string) ([][]string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	rows := make([][]string, 0)
	var (
		cells       []string
		cellValue   strings.Builder
		cellFormula strings.Builder
		cellInline  strings.Builder
		cellType    string
		cellColumn  int
		hasCell     bool
		inValue     bool
		inFormula   bool
		inInline    bool
		inText      bool
	)
	appendCell := func(value string) {
		if cellColumn >= 0 {
			for len(cells) < cellColumn {
				cells = append(cells, "")
			}
			cells = append(cells, value)
			return
		}
		cells = append(cells, value)
	}
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: decode xlsx worksheet: %v", ErrCorrupt, err)
		}
		switch element := token.(type) {
		case xml.StartElement:
			switch element.Name.Local {
			case "c":
				cellValue.Reset()
				cellFormula.Reset()
				cellInline.Reset()
				cellType = xmlAttribute(element, "t")
				cellColumn = -1
				if reference := xmlAttribute(element, "r"); reference != "" {
					cellColumn = spreadsheetColumn(reference)
				}
				hasCell = true
			case "v":
				inValue = true
			case "f":
				inFormula = true
			case "is":
				inInline = true
			case "t":
				if inInline {
					inText = true
				}
			}
		case xml.CharData:
			switch {
			case inValue:
				cellValue.Write([]byte(element))
			case inFormula:
				cellFormula.Write([]byte(element))
			case inText:
				cellInline.Write([]byte(element))
			}
		case xml.EndElement:
			switch element.Name.Local {
			case "v":
				inValue = false
			case "f":
				inFormula = false
			case "t":
				inText = false
			case "is":
				inInline = false
			case "c":
				if hasCell {
					appendCell(resolveXLSXCell(cellValue.String(), cellInline.String(), cellFormula.String(), cellType, shared))
				}
				hasCell = false
			case "row":
				cells = trimTrailingEmpty(cells)
				if len(cells) > 0 {
					rows = append(rows, cells)
					if len(rows) > MaxSheetRows {
						return nil, fmt.Errorf("%w: worksheet has more than %d rows", ErrTooLarge, MaxSheetRows)
					}
				}
				cells = nil
			}
		}
	}
	return rows, nil
}

func spreadsheetColumn(reference string) int {
	column := 0
	seen := false
	for _, character := range reference {
		if character < 'A' || character > 'Z' {
			break
		}
		column = column*26 + int(character-'A'+1)
		seen = true
	}
	if !seen {
		return -1
	}
	return column - 1
}

func resolveXLSXCell(value, inline, formula, cellType string, shared []string) string {
	value = strings.TrimSpace(value)
	inline = strings.TrimSpace(inline)
	formula = strings.TrimSpace(formula)
	switch cellType {
	case "s":
		index, err := strconv.Atoi(value)
		if err != nil || index < 0 || index >= len(shared) {
			return ""
		}
		return shared[index]
	case "inlineStr":
		return inline
	case "b":
		if value == "1" {
			return "TRUE"
		}
		return "FALSE"
	case "str", "e", "d":
		return value
	}
	if formula != "" && value == "" {
		return "[formula] " + formula
	}
	return value
}

func trimTrailingEmpty(cells []string) []string {
	end := len(cells)
	for end > 0 && strings.TrimSpace(cells[end-1]) == "" {
		end--
	}
	return cells[:end]
}

type sheetComment struct {
	Ref    string
	Author string
	Text   string
}

// sheetCommentSkip records a comment part the XLSX parser found but cannot
// parse (currently threaded comments), so it is reported with a reason instead
// of silently disappearing from coverage (R-A7-3a).
type sheetCommentSkip struct {
	Source string
	Reason string
}

// readSheetComments resolves the comment parts related to one worksheet (if
// any). Legacy comments are parsed; threaded comments are a different XML part
// that this parser does not decode, so they are returned as reasoned skips.
func readSheetComments(archive *zip.Reader, sheetFile string) ([]sheetComment, []sheetCommentSkip, error) {
	relations, err := readRelationships(archive, partRelsPath(sheetFile))
	if err != nil {
		return nil, nil, err
	}
	comments := make([]sheetComment, 0)
	skips := make([]sheetCommentSkip, 0)
	for _, target := range sortedRelationshipTargets(relations) {
		lowered := strings.ToLower(target)
		commentsPath := resolvePartPath(path.Dir(sheetFile), target)
		if strings.Contains(lowered, "threadedcomment") {
			skips = append(skips, sheetCommentSkip{Source: commentsPath, Reason: "threaded comments are not parsed"})
			continue
		}
		if !strings.Contains(lowered, "comments") {
			continue
		}
		file, ok := findArchiveEntry(archive, commentsPath)
		if !ok {
			continue
		}
		raw, err := readZipEntry(file)
		if err != nil {
			return nil, nil, err
		}
		parsed, err := parseComments(raw)
		if err != nil {
			return nil, nil, err
		}
		comments = append(comments, parsed...)
	}
	return comments, skips, nil
}

func parseComments(raw []byte) ([]sheetComment, error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	authors := make([]string, 0)
	comments := make([]sheetComment, 0)
	var (
		current    *sheetComment
		text       strings.Builder
		authorText strings.Builder
		authorID   string
		inAuthor   bool
		inText     bool
	)
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: decode xlsx comments: %v", ErrCorrupt, err)
		}
		switch element := token.(type) {
		case xml.StartElement:
			switch element.Name.Local {
			case "author":
				inAuthor = true
				authorText.Reset()
			case "comment":
				current = &sheetComment{Ref: xmlAttribute(element, "ref")}
				authorID = xmlAttribute(element, "authorId")
				text.Reset()
			case "text":
				if current != nil {
					inText = true
				}
			}
		case xml.CharData:
			switch {
			case inAuthor:
				authorText.Write([]byte(element))
			case inText && current != nil:
				text.Write([]byte(element))
			}
		case xml.EndElement:
			switch element.Name.Local {
			case "author":
				authors = append(authors, strings.TrimSpace(authorText.String()))
				inAuthor = false
			case "comment":
				if current != nil {
					current.Text = strings.TrimSpace(text.String())
					if index, err := strconv.Atoi(authorID); err == nil && index >= 0 && index < len(authors) {
						current.Author = authors[index]
					}
					if current.Ref != "" && current.Text != "" {
						comments = append(comments, *current)
					}
					current = nil
				}
			case "text":
				inText = false
			}
		}
	}
	return comments, nil
}

// extractXLSXResult renders every sheet (hidden ones are marked), cached
// formula values, comments and xl/media images.
func extractXLSXResult(data []byte) (versionapp.ExtractionResult, error) {
	archive, err := openOOXML(data, "xlsx")
	if err != nil {
		return versionapp.ExtractionResult{}, err
	}
	shared, err := readSharedStrings(archive)
	if err != nil {
		return versionapp.ExtractionResult{}, err
	}
	sheets, err := readWorkbookSheets(archive)
	if err != nil {
		return versionapp.ExtractionResult{}, err
	}
	if len(sheets) == 0 {
		for _, sheet := range numberedXMLFiles(archive, "xl/worksheets/sheet", ".xml") {
			sheets = append(sheets, workbookSheet{Name: fmt.Sprintf("Sheet%d", sheet.number), File: sheet.file.Name})
		}
	}
	if len(sheets) == 0 {
		return versionapp.ExtractionResult{}, fmt.Errorf("%w: no worksheet files found", ErrCorrupt)
	}
	builder := newResultBuilder()
	seenFiles := make(map[string]bool)
	for _, sheet := range sheets {
		file, ok := findArchiveEntry(archive, sheet.File)
		if !ok {
			if err := builder.skip("sheet", sheet.File, "worksheet part is missing"); err != nil {
				return versionapp.ExtractionResult{}, err
			}
			continue
		}
		seenFiles[sheet.File] = true
		raw, err := readZipEntry(file)
		if err != nil {
			return versionapp.ExtractionResult{}, err
		}
		rows, err := parseSheetRows(raw, shared)
		if err != nil {
			return versionapp.ExtractionResult{}, err
		}
		header := "## Sheet: " + sheet.Name
		if sheet.Hidden {
			header += " (hidden)"
		}
		if err := builder.element(versionapp.ExtractionElement{
			Kind: "sheet", Text: sheet.Name, Source: sheet.File, Sheet: sheet.Name,
		}); err != nil {
			return versionapp.ExtractionResult{}, err
		}
		if err := builder.addText(header + "\n"); err != nil {
			return versionapp.ExtractionResult{}, err
		}
		for _, row := range rows {
			line := strings.Join(row, "\t")
			if err := builder.element(versionapp.ExtractionElement{
				Kind: "sheet_row", Text: line, Row: row, Source: sheet.File, Sheet: sheet.Name,
			}); err != nil {
				return versionapp.ExtractionResult{}, err
			}
			if err := builder.addText(line + "\n"); err != nil {
				return versionapp.ExtractionResult{}, err
			}
		}
		comments, commentSkips, err := readSheetComments(archive, sheet.File)
		if err != nil {
			return versionapp.ExtractionResult{}, err
		}
		for _, comment := range comments {
			if err := builder.element(versionapp.ExtractionElement{
				Kind: "comment", Text: comment.Text, Source: sheet.File, Sheet: sheet.Name,
			}); err != nil {
				return versionapp.ExtractionResult{}, err
			}
			author := comment.Author
			if author == "" {
				author = "unknown"
			}
			if err := builder.addText("[批注 " + comment.Ref + "] " + author + ": " + comment.Text + "\n"); err != nil {
				return versionapp.ExtractionResult{}, err
			}
		}
		for _, skip := range commentSkips {
			if err := builder.skip("comment", skip.Source, skip.Reason); err != nil {
				return versionapp.ExtractionResult{}, err
			}
		}
	}
	// A workbook without workbook.xml still gets its loose worksheets, and
	// worksheets absent from the sheet list must not be dropped.
	for _, sheet := range numberedXMLFiles(archive, "xl/worksheets/sheet", ".xml") {
		if seenFiles[sheet.file.Name] {
			continue
		}
		builder.skip("sheet", sheet.file.Name, "worksheet is not listed in workbook.xml")
	}
	for _, file := range archive.File {
		if !strings.HasPrefix(file.Name, "xl/media/") {
			continue
		}
		if err := appendMediaImage(builder, archive, file.Name); err != nil {
			return versionapp.ExtractionResult{}, err
		}
	}
	return builder.result(), nil
}

// ---------- shared string table ----------

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
				current.Write([]byte(element))
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
