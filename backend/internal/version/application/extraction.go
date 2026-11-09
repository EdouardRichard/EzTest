package versionapp

// v0.22 A-7 structural chunking (spec R-A7-2, docs/version-V0.22/A §7.4).
//
// SegmentExtraction turns the structured parse result of one attachment into
// deterministic text chunks for per-chunk requirement extraction. It preserves
// source structure (DOCX heading hierarchy, PPTX slide order, XLSX sheet + row
// blocks, PDF page + heading heuristic, HTML headings / plain-text paragraphs),
// keeps tables as Markdown text and embedded images as image_refs[] with an
// in-text position marker, and never merges content across a heading/slide/
// sheet/page boundary.
//
// Chunk sizing: a segment grows until it reaches the 4,000-rune target minimum
// or until adding the next block would exceed the 8,000-rune maximum. A block
// larger than the maximum is split again at a line/space/sentence boundary (the
// tail may keep filling the same heading section). A table larger than the
// maximum becomes standalone table segments, one per row group, because a
// re-rendered Markdown header is required for every piece.
//
// Prefix: every segment carries "source path" plus the previous segment's last
// 200 runes in Prefix (never duplicated into Text), so a model that only sees
// one segment keeps the source and the cross-chunk context (A §7.4). Heading
// paths stay in their own HeadingPath field.
//
// The canonical document text is the concatenation of the parsed content blocks
// joined by "\n"; CharStart/CharEnd are rune offsets into it and
// TextSHA256 is the SHA-256 (hex) of Text, so segment identity is stable for an
// unchanged input (R-A7-6/R-A7-7 segment key).

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Segment kinds (spec R-A7-2). A segment containing any ordinary text is
// "text"; a segment made only of table rows (plus its heading marker) is
// "table"; a segment made only of image markers is "image".
const (
	SegmentKindText  = "text"
	SegmentKindTable = "table"
	SegmentKindImage = "image"
)

// Chunking defaults (docs/version-V0.22/A §7.4).
const (
	// DefaultSegmentMaxChars is the upper bound of one segment in runes.
	DefaultSegmentMaxChars = 8000
	// DefaultSegmentMinChars is the soft lower target: a segment is flushed as
	// soon as it reaches this size (unless a structure boundary comes first).
	DefaultSegmentMinChars = 4000
	// DefaultSegmentPrefixTailRunes is the number of previous-segment runes
	// copied into Prefix.
	DefaultSegmentPrefixTailRunes = 200
)

// Segment is one deterministic extraction chunk. Seq is 1-based and stable for
// the same input. CharStart/CharEnd are rune offsets into the canonical
// segmented document text ([start, end)). Prefix carries the source path and the
// previous segment's tail and is never part of Text.
type Segment struct {
	Seq         int      `json:"seq"`
	HeadingPath string   `json:"heading_path"`
	Kind        string   `json:"kind"`
	CharStart   int      `json:"char_start"`
	CharEnd     int      `json:"char_end"`
	Text        string   `json:"text"`
	Prefix      string   `json:"prefix"`
	ImageRefs   []string `json:"image_refs,omitempty"`
	TextSHA256  string   `json:"text_sha256"`
}

// SegmentOptions tunes SegmentExtraction. Zero values select the spec defaults.
type SegmentOptions struct {
	// SourcePath is the attachment name/path shown to the model in Prefix
	// (falls back to result.Attachment.Name).
	SourcePath string
	// MaxChars is the segment upper bound in runes (default 8,000).
	MaxChars int
	// MinChars is the soft flush target in runes (default 4,000).
	MinChars int
	// PrefixTailRunes is the previous-segment tail length in Prefix (default 200).
	PrefixTailRunes int
}

// SegmentExtraction chunks one parsed extraction result. It is deterministic:
// the same result and options always produce the same segments, hashes and
// offsets. It never drops a parsed element: headings, paragraphs, list items,
// table rows, slides, sheets, pages, notes and images all contribute text (or a
// marker) to exactly one segment.
func SegmentExtraction(result ExtractionResult, options SegmentOptions) ([]Segment, error) {
	maxChars := options.MaxChars
	if maxChars <= 0 {
		maxChars = DefaultSegmentMaxChars
	}
	minChars := options.MinChars
	if minChars <= 0 {
		minChars = DefaultSegmentMinChars
	}
	if minChars > maxChars {
		minChars = maxChars
	}
	tailRunes := options.PrefixTailRunes
	if tailRunes <= 0 {
		tailRunes = DefaultSegmentPrefixTailRunes
	}
	sourcePath := strings.TrimSpace(options.SourcePath)
	if sourcePath == "" {
		sourcePath = strings.TrimSpace(result.Attachment.Name)
	}

	blocks := buildSegmentBlocks(result)
	blocks = insertImageBlocks(blocks, result.Images)
	blocks = expandOversizedTables(blocks, maxChars)
	if len(blocks) == 0 {
		return nil, nil
	}

	segments := make([]Segment, 0, len(blocks))
	current := make([]segmentPiece, 0, 4)
	currentLength := 0
	currentSection := 0
	flush := func() {
		if len(current) == 0 {
			return
		}
		var text strings.Builder
		for index, piece := range current {
			if index > 0 && piece.gapBefore {
				text.WriteString("\n")
			}
			text.WriteString(piece.text)
		}
		segmentText := text.String()
		prefix := sourcePath
		if len(segments) > 0 {
			if tail := lastSegmentRunes(segments[len(segments)-1].Text, tailRunes); tail != "" {
				if prefix != "" {
					prefix += "\n"
				}
				prefix += tail
			}
		}
		hasText, hasTable, hasImage := false, false, false
		var refs []string
		for _, piece := range current {
			if !piece.heading {
				switch piece.kind {
				case SegmentKindTable:
					hasTable = true
				case SegmentKindImage:
					hasImage = true
				default:
					hasText = true
				}
			}
			refs = appendUniqueSegmentStrings(refs, piece.imageRefs...)
		}
		kind := SegmentKindText
		if !hasText {
			switch {
			case hasTable && !hasImage:
				kind = SegmentKindTable
			case hasImage && !hasTable:
				kind = SegmentKindImage
			}
		}
		hash := sha256.Sum256([]byte(segmentText))
		segments = append(segments, Segment{
			Seq:         len(segments) + 1,
			HeadingPath: current[len(current)-1].headingPath,
			Kind:        kind,
			CharStart:   current[0].absStart,
			CharEnd:     current[len(current)-1].absEnd,
			Text:        segmentText,
			Prefix:      prefix,
			ImageRefs:   refs,
			TextSHA256:  hex.EncodeToString(hash[:]),
		})
		current = current[:0]
		currentLength = 0
	}
	addPiece := func(piece segmentPiece) {
		if piece.text == "" {
			return
		}
		if len(current) > 0 && piece.absStart > current[len(current)-1].absEnd {
			piece.gapBefore = true
		}
		if piece.gapBefore && len(current) > 0 {
			currentLength++
		}
		current = append(current, piece)
		currentLength += runeLen(piece.text)
	}

	cursor := 0
	for _, block := range blocks {
		if block.text == "" {
			continue
		}
		if len(current) > 0 && block.section != currentSection {
			flush()
		}
		currentSection = block.section
		blockStart := cursor
		cursor += runeLen(block.text) + 1 // canonical block separator
		if block.standalone {
			flush()
			addPiece(segmentPiece{
				text: block.text, kind: block.kind, heading: block.heading,
				headingPath: block.headingPath, imageRefs: block.imageRefs,
				absStart: blockStart, absEnd: blockStart + runeLen(block.text),
			})
			flush()
			continue
		}
		remaining := block.text
		consumed := 0
		for remaining != "" {
			space := maxChars - currentLength
			if len(current) > 0 {
				space-- // separator before the next block
			}
			if space <= 0 {
				flush()
				continue
			}
			if runeLen(remaining) <= space {
				addPiece(segmentPiece{
					text: remaining, kind: block.kind, heading: block.heading,
					headingPath: block.headingPath, imageRefs: block.imageRefs,
					absStart: blockStart + consumed, absEnd: blockStart + consumed + runeLen(remaining),
				})
				consumed += runeLen(remaining)
				remaining = ""
				if currentLength >= minChars {
					flush()
				}
				break
			}
			splitAt := extractionSplitIndex(remaining, space)
			addPiece(segmentPiece{
				text: string([]rune(remaining)[:splitAt]), kind: block.kind, heading: block.heading,
				headingPath: block.headingPath, imageRefs: block.imageRefs,
				absStart: blockStart + consumed, absEnd: blockStart + consumed + splitAt,
			})
			consumed += splitAt
			remaining = string([]rune(remaining)[splitAt:])
			flush()
		}
	}
	flush()
	return segments, nil
}

// segmentPiece is one rendered text range of the canonical document.
type segmentPiece struct {
	text        string
	kind        string
	heading     bool // structural marker (heading/slide/sheet/page), not content
	headingPath string
	imageRefs   []string
	absStart    int
	absEnd      int
	gapBefore   bool
}

// segmentBlock is one source-structure block (a heading, paragraph, table,
// image marker, ...). rows keeps the structured table so an oversized table can
// be split again by row groups.
type segmentBlock struct {
	kind        string
	heading     bool
	headingPath string
	section     int
	source      string
	text        string
	rows        [][]string
	imageRefs   []string
	// standalone marks a table piece that must occupy its own segment
	// (spec R-A7-2: an oversized table becomes standalone kind=table chunks).
	standalone bool
}

type segmentBlockBuilder struct {
	blocks      []segmentBlock
	section     int
	headingPath string
	hasTable    bool
	tableRows   [][]string
	tableMeta   segmentBlock
}

func (b *segmentBlockBuilder) flushTable() {
	if !b.hasTable {
		return
	}
	rows := b.tableRows
	b.tableRows = nil
	b.hasTable = false
	if len(rows) == 0 {
		return
	}
	block := b.tableMeta
	block.kind = SegmentKindTable
	block.text = renderSegmentTable(rows)
	block.rows = rows
	b.blocks = append(b.blocks, block)
}

// addTableRow groups contiguous table_row/sheet_row elements into one Markdown
// table block. DOCX/PPTX emit table_row and XLSX emits sheet_row for the same
// structured row shape (Row holds the cells).
func (b *segmentBlockBuilder) addTableRow(element ExtractionElement) {
	if !b.hasTable {
		b.hasTable = true
		headingPath := b.headingPath
		if strings.TrimSpace(element.HeadingPath) != "" {
			headingPath = element.HeadingPath
		}
		b.tableMeta = segmentBlock{headingPath: headingPath, section: b.section, source: element.Source}
	}
	row := append([]string(nil), element.Row...)
	if len(row) == 0 {
		row = []string{element.Text}
	}
	b.tableRows = append(b.tableRows, row)
}

// addText appends one ordinary text block, flushing a pending table first.
func (b *segmentBlockBuilder) addText(text, headingPath, source string, heading bool) {
	if strings.TrimSpace(text) == "" {
		return
	}
	b.flushTable()
	if b.headingPath == "" && strings.TrimSpace(headingPath) != "" {
		b.headingPath = headingPath
	}
	b.blocks = append(b.blocks, segmentBlock{
		kind: SegmentKindText, heading: heading, headingPath: b.headingPath,
		section: b.section, source: source, text: text,
	})
}

// startSection opens a new structural section (heading/slide/sheet) so later
// blocks can never merge into the previous heading's segment.
func (b *segmentBlockBuilder) startSection(label, source, text string) {
	b.flushTable()
	b.section++
	if strings.TrimSpace(label) == "" {
		label = strings.TrimSpace(text)
	}
	b.headingPath = label
	b.blocks = append(b.blocks, segmentBlock{
		kind: SegmentKindText, heading: true, headingPath: label,
		section: b.section, source: source, text: text,
	})
}

// buildSegmentBlocks renders all parsed elements into ordered content blocks.
func buildSegmentBlocks(result ExtractionResult) []segmentBlock {
	builder := &segmentBlockBuilder{}
	if len(result.Elements) == 0 {
		builder.addPlainText(result.Text, strings.TrimSpace(result.Attachment.Name))
		return builder.blocks
	}
	for _, element := range result.Elements {
		switch element.Kind {
		case "table_row", "sheet_row":
			builder.addTableRow(element)
			continue
		}
		builder.flushTable()
		switch element.Kind {
		case "heading":
			label := element.HeadingPath
			if strings.TrimSpace(label) == "" {
				label = element.Text
			}
			builder.startSection(label, element.Source, element.Text)
		case "slide":
			label := fmt.Sprintf("Slide %d", element.Slide)
			if element.Slide <= 0 {
				label = element.Text
			}
			builder.startSection(label, element.Source, element.Text)
		case "sheet":
			name := element.Sheet
			if strings.TrimSpace(name) == "" {
				name = element.Text
			}
			label := "Sheet: " + name
			builder.startSection(label, element.Source, label)
		case "page":
			builder.addPage(element)
		case "document":
			builder.addPlainText(element.Text, element.Source)
		case "note", "header", "footer", "footnote", "endnote", "comment":
			builder.addText(extractionAuxiliaryMarker(element.Kind)+element.Text, element.HeadingPath, element.Source, false)
		default:
			builder.addText(element.Text, element.HeadingPath, element.Source, false)
		}
	}
	builder.flushTable()
	if len(builder.blocks) == 0 && strings.TrimSpace(result.Text) != "" {
		// Defensive no-loss fallback: an element list that renders no blocks
		// (blank/unknown elements, replayed results) must not hide the parser's
		// concatenated Text.
		builder.addPlainText(result.Text, strings.TrimSpace(result.Attachment.Name))
	}
	return builder.blocks
}

// addPage renders one PDF page: a page marker block plus heading-heuristic
// sub-blocks. Each detected heading opens a new section (path
// "Page N > <heading>"); the page marker joins the first heading section.
func (b *segmentBlockBuilder) addPage(element ExtractionElement) {
	b.flushTable()
	pagePath := "Page"
	if element.Page > 0 {
		pagePath = fmt.Sprintf("Page %d", element.Page)
	}
	parts := splitPDFPageParts(element.Text)
	if len(parts) == 0 {
		b.startSection(pagePath, element.Source, pagePath)
		return
	}
	if parts[0].heading {
		b.section++
		b.headingPath = pagePath + " > " + parts[0].text
		b.blocks = append(b.blocks, segmentBlock{
			kind: SegmentKindText, heading: true, headingPath: b.headingPath,
			section: b.section, source: element.Source, text: pagePath,
		}, segmentBlock{
			kind: SegmentKindText, heading: true, headingPath: b.headingPath,
			section: b.section, source: element.Source, text: parts[0].text,
		})
		parts = parts[1:]
	} else {
		b.section++
		b.headingPath = pagePath
		b.blocks = append(b.blocks, segmentBlock{
			kind: SegmentKindText, heading: true, headingPath: b.headingPath,
			section: b.section, source: element.Source, text: pagePath,
		})
	}
	for _, part := range parts {
		if part.heading {
			b.section++
			b.headingPath = pagePath + " > " + part.text
			b.blocks = append(b.blocks, segmentBlock{
				kind: SegmentKindText, heading: true, headingPath: b.headingPath,
				section: b.section, source: element.Source, text: part.text,
			})
			continue
		}
		b.blocks = append(b.blocks, segmentBlock{
			kind: SegmentKindText, headingPath: b.headingPath,
			section: b.section, source: element.Source, text: part.text,
		})
	}
}

// addPlainText chunks a raw text fallback (md/txt, or a replayed extraction that
// only kept Text): blank lines separate paragraphs and Markdown ATX headings
// open new sections.
func (b *segmentBlockBuilder) addPlainText(text, source string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	b.flushTable()
	var paragraph strings.Builder
	flushParagraph := func() {
		value := strings.TrimSpace(paragraph.String())
		paragraph.Reset()
		if value == "" {
			return
		}
		b.blocks = append(b.blocks, segmentBlock{
			kind: SegmentKindText, headingPath: b.headingPath,
			section: b.section, source: source, text: value,
		})
	}
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	for _, line := range strings.Split(normalized, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			flushParagraph()
			continue
		}
		if heading, ok := markdownHeadingText(trimmed); ok {
			flushParagraph()
			b.section++
			b.headingPath = heading
			b.blocks = append(b.blocks, segmentBlock{
				kind: SegmentKindText, heading: true, headingPath: heading,
				section: b.section, source: source, text: heading,
			})
			continue
		}
		if paragraph.Len() > 0 {
			paragraph.WriteString("\n")
		}
		paragraph.WriteString(trimmed)
	}
	flushParagraph()
}

// insertImageBlocks turns every parsed embedded image into a position marker
// block. Anchoring is deterministic: an image whose Source matches a parsed
// element's Source is placed after the last such element; an OOXML media part
// (word/media, xl/media, ppt/media) is placed after the last element of that
// package part; a PDF object image after the last page. Unanchored images become
// a trailing "图片" image section. The parser does not preserve per-paragraph
// image positions, so the marker is container-level, never invented.
func insertImageBlocks(blocks []segmentBlock, images []ExtractionImage) []segmentBlock {
	if len(images) == 0 {
		return blocks
	}
	maxSection := 0
	for _, block := range blocks {
		if block.section > maxSection {
			maxSection = block.section
		}
	}
	inserts := make(map[int][]segmentBlock, len(images))
	for _, image := range images {
		ref := extractionImageRef(image)
		marker := segmentBlock{
			kind: SegmentKindImage, headingPath: "图片", source: image.Source,
			text: "[图片: " + ref + "]", imageRefs: []string{ref},
		}
		anchor := -1
		for index := range blocks {
			if imageAnchorsTo(blocks[index].source, image.Source) {
				anchor = index
			}
		}
		if anchor >= 0 {
			marker.section = blocks[anchor].section
			marker.headingPath = blocks[anchor].headingPath
			inserts[anchor+1] = append(inserts[anchor+1], marker)
			continue
		}
		marker.section = maxSection + 1
		inserts[len(blocks)] = append(inserts[len(blocks)], marker)
	}
	expanded := make([]segmentBlock, 0, len(blocks)+len(images))
	for index := 0; index <= len(blocks); index++ {
		expanded = append(expanded, inserts[index]...)
		if index < len(blocks) {
			expanded = append(expanded, blocks[index])
		}
	}
	return expanded
}

// imageAnchorsTo reports whether an image belongs to the container of the given
// element source (see insertImageBlocks).
func imageAnchorsTo(elementSource, imageSource string) bool {
	elementSource = strings.TrimSpace(elementSource)
	imageSource = strings.TrimSpace(imageSource)
	if elementSource == "" || imageSource == "" {
		return false
	}
	if elementSource == imageSource {
		return true
	}
	if index := strings.Index(imageSource, "/media/"); index >= 0 {
		return strings.HasPrefix(elementSource, imageSource[:index+1])
	}
	if strings.HasPrefix(imageSource, "pdf:object:") {
		return strings.HasPrefix(elementSource, "pdf:page:")
	}
	return path.Base(elementSource) == path.Base(imageSource)
}

// extractionImageRef is the stable image reference recorded in image_refs[]:
// the embedded image name, falling back to its source base name.
func extractionImageRef(image ExtractionImage) string {
	if name := strings.TrimSpace(image.Name); name != "" {
		return name
	}
	if base := strings.TrimSpace(path.Base(image.Source)); base != "" && base != "." {
		return base
	}
	return fmt.Sprintf("image-%d", image.Seq)
}

// expandOversizedTables splits tables over the segment maximum into standalone
// kind=table blocks (spec R-A7-2). Row groups repeat the Markdown header; a
// group that still cannot fit (a single row or header wider than the maximum)
// is split again at a text boundary so no segment ever exceeds the maximum.
//
// This path only runs for structured elements (DOCX/PPTX/XLSX table_row /
// sheet_row): plain-text and Markdown input carries no structured rows, so an
// oversized Markdown table there is split by the generic text splitter and its
// continuation pieces do not repeat the header. That is a documented limitation
// of the text path; the deterministic processor still recognizes the header
// piece, and the DOCX path (the UAT document) is safe.
func expandOversizedTables(blocks []segmentBlock, maxChars int) []segmentBlock {
	expanded := make([]segmentBlock, 0, len(blocks))
	for _, block := range blocks {
		if block.kind != SegmentKindTable || runeLen(block.text) <= maxChars {
			expanded = append(expanded, block)
			continue
		}
		for _, rows := range splitSegmentTableRows(block.rows, maxChars) {
			rendered := renderSegmentTable(rows)
			if runeLen(rendered) <= maxChars {
				piece := block
				piece.rows = rows
				piece.text = rendered
				piece.standalone = true
				expanded = append(expanded, piece)
				continue
			}
			for _, text := range splitOversizedTableText(rows, rendered, maxChars) {
				piece := block
				piece.rows = nil
				piece.text = text
				piece.standalone = true
				expanded = append(expanded, piece)
			}
		}
	}
	return expanded
}

// splitSegmentTableRows groups rows so each rendered Markdown table fits in
// maxChars. The header row (and separator) repeats in every group. A group that
// still does not fit (for example one oversized row) is returned as-is and is
// split again by splitOversizedTableText.
func splitSegmentTableRows(rows [][]string, maxChars int) [][][]string {
	if len(rows) == 0 {
		return nil
	}
	header := rows[0]
	data := rows[1:]
	groups := make([][][]string, 0, 1)
	current := [][]string{header}
	for _, row := range data {
		candidate := append(append([][]string(nil), current...), row)
		if len(current) > 1 && runeLen(renderSegmentTable(candidate)) > maxChars {
			groups = append(groups, current)
			current = [][]string{header, row}
			continue
		}
		current = candidate
	}
	if len(current) > 0 {
		groups = append(groups, current)
	}
	return groups
}

// splitOversizedTableText splits one oversized rendered table into pieces that
// each fit maxChars. When the group still has data rows, continuation pieces
// repeat the Markdown header so they keep column context; if the header alone
// leaves no usable budget it is not repeated. This is the fallback for a single
// row (or a header) wider than the maximum: there is no smaller row group, so
// the rendered text is split at a newline/sentence/space boundary.
func splitOversizedTableText(rows [][]string, rendered string, maxChars int) []string {
	header := ""
	if len(rows) > 1 {
		header = renderSegmentTable(rows[:1])
	}
	pieces := make([]string, 0, 2)
	remaining := []rune(rendered)
	first := true
	for len(remaining) > 0 {
		budget := maxChars
		prefix := ""
		if !first && header != "" {
			candidate := header + "\n"
			if runeLen(candidate) < maxChars {
				prefix = candidate
				budget = maxChars - runeLen(prefix)
			}
		}
		if len(remaining) <= budget {
			pieces = append(pieces, prefix+string(remaining))
			break
		}
		splitAt := extractionSplitIndex(string(remaining), budget)
		pieces = append(pieces, prefix+string(remaining[:splitAt]))
		remaining = remaining[splitAt:]
		first = false
	}
	return pieces
}

// renderSegmentTable renders rows as a Markdown table, the portable textual
// form the requirement extraction consumes (spec R-A7-2).
func renderSegmentTable(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}
	width := 0
	for _, row := range rows {
		if len(row) > width {
			width = len(row)
		}
	}
	var builder strings.Builder
	for index, row := range rows {
		builder.WriteString("|")
		for column := 0; column < width; column++ {
			cell := ""
			if column < len(row) {
				cell = escapeSegmentTableCell(row[column])
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
	return strings.TrimRight(builder.String(), "\n")
}

func escapeSegmentTableCell(value string) string {
	value = strings.ReplaceAll(value, "|", `\|`)
	value = strings.ReplaceAll(value, "\r", " ")
	return strings.ReplaceAll(value, "\n", " ")
}

// extractionAuxiliaryMarker preserves the content class of non-body DOCX
// elements (the parser records them as separate elements).
func extractionAuxiliaryMarker(kind string) string {
	switch kind {
	case "header":
		return "[页眉] "
	case "footer":
		return "[页脚] "
	case "footnote":
		return "[脚注] "
	case "endnote":
		return "[尾注] "
	case "comment":
		return "[批注] "
	case "note":
		return "[备注] "
	default:
		return ""
	}
}

// pagePart is one PDF page block produced by the heading heuristic.
type pagePart struct {
	heading bool
	text    string
}

// splitPDFPageParts splits a PDF page text layer into heading-heuristic blocks
// (spec R-A7-2: PDF by page + heading heuristic). Detected headings are short
// numbered/chapter lines; everything else is body text.
func splitPDFPageParts(text string) []pagePart {
	parts := make([]pagePart, 0)
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if heading, ok := markdownHeadingText(trimmed); ok {
			parts = append(parts, pagePart{heading: true, text: heading})
			continue
		}
		if runeLen(trimmed) <= pdfHeadingMaxRunes && pdfHeadingPattern.MatchString(trimmed) {
			parts = append(parts, pagePart{heading: true, text: trimmed})
			continue
		}
		parts = append(parts, pagePart{text: trimmed})
	}
	return parts
}

var (
	markdownHeadingPattern = regexp.MustCompile(`^#{1,6}\s+(.+)$`)
	// pdfHeadingPattern is the deterministic heading heuristic for PDF text:
	// numbered section titles ("3.2 用户登录"), chapter titles ("第三章 ...")
	// and Chinese enumeration headings ("一、..."). Long lines are body text.
	pdfHeadingPattern = regexp.MustCompile(`^([0-9]+(\.(?:[0-9]+))*[.、．)）]?\s+\S|第[0-9一二三四五六七八九十百]+[章节篇部分]|[一二三四五六七八九十]+、|（[一二三四五六七八九十]+）)`)
)

const pdfHeadingMaxRunes = 80

// markdownHeadingText recognizes an ATX Markdown heading and returns its text.
func markdownHeadingText(line string) (string, bool) {
	matches := markdownHeadingPattern.FindStringSubmatch(line)
	if len(matches) != 2 {
		return "", false
	}
	heading := strings.TrimSpace(matches[1])
	if heading == "" {
		return "", false
	}
	return heading, true
}

// extractionSplitIndex chooses the rune index at which to split an oversized
// block within limit runes. It prefers a newline, then a sentence/word
// boundary in the last 20% of the window, and falls back to a hard split.
func extractionSplitIndex(text string, limit int) int {
	runes := []rune(text)
	if limit >= len(runes) {
		return len(runes)
	}
	if limit <= 0 {
		return 1
	}
	lowest := limit - limit/5
	if lowest < 1 {
		lowest = 1
	}
	breakRunes := func(candidate rune) bool {
		switch candidate {
		case '\n', '。', '！', '？', '；', '.', '!', '?', ';', '，', ',', '、', ' ':
			return true
		default:
			return false
		}
	}
	for index := limit; index > lowest; index-- {
		if breakRunes(runes[index-1]) {
			return index
		}
	}
	return limit
}

// lastSegmentRunes returns the last n runes of value.
func lastSegmentRunes(value string, n int) string {
	runes := []rune(value)
	if len(runes) <= n {
		return string(runes)
	}
	return string(runes[len(runes)-n:])
}

func runeLen(value string) int {
	return utf8.RuneCountInString(value)
}

func appendUniqueSegmentStrings(values []string, additions ...string) []string {
	for _, addition := range additions {
		if addition == "" {
			continue
		}
		exists := false
		for _, value := range values {
			if value == addition {
				exists = true
				break
			}
		}
		if !exists {
			values = append(values, addition)
		}
	}
	return values
}

// ---------- v0.22 A-7 reconciliation (R-A7-8, docs A §7.7) ----------

// DefaultLowConfidenceThreshold is the UI "低置信待复核" cutoff: extracted
// requirements below this confidence are counted in the report and need review.
const DefaultLowConfidenceThreshold = 0.6

// ReconciliationInput carries the extracted side of the reconciliation. The
// declared side is parsed from the document text itself.
type ReconciliationInput struct {
	// ExtractedIDs are the external ids actually persisted for the run.
	ExtractedIDs []string
	// LowConfidence is the number of persisted requirements below
	// DefaultLowConfidenceThreshold (supplied by the requirement query).
	LowConfidence int
	// SkippedImages is the parser coverage count of human-visible images that
	// were not analyzed (A §7.9: v0.22 keeps the reference and marks skipped).
	SkippedImages int
	// SkippedIDs are the declared ids the sink declined to overwrite because
	// the requirement is already non-draft (spec D). They are reported as
	// skipped, never as missing, and excluded from MissingIDs.
	SkippedIDs []string
}

// ReconciliationReport is the R-A7-8 audit report. Expected/Extracted/Missing
// and MissingIDs cover the V1 current requirements only. The V2 planned
// requirements are a separate partition (PlannedCount/PlannedIDs) and are never
// reported missing: the live document declares 58 current + 32 planned ids, so
// summing them would over-count the A-9 58/58 target. Skipped/SkippedIDs list
// the current ids the sink skipped as already non-draft (spec D), so expected =
// extracted + missing + skipped. The whole report is persisted as
// requirement_extraction_run.report for the progress UI/API.
type ReconciliationReport struct {
	Expected      int      `json:"expected"`
	Extracted     int      `json:"extracted"`
	Missing       int      `json:"missing"`
	MissingIDs    []string `json:"missing_ids"`
	Skipped       int      `json:"skipped"`
	SkippedIDs    []string `json:"skipped_ids"`
	LowConfidence int      `json:"low_confidence"`
	SkippedImages int      `json:"skipped_images"`
	PlannedCount  int      `json:"planned_count"`
	PlannedIDs    []string `json:"planned_ids"`
	// CurrentIDs is the declared V1 partition. It is kept in-process for
	// diagnostics/tests and is not part of the persisted report contract.
	CurrentIDs []string `json:"-"`
}

var (
	// extractionFRIDPattern is the declared-requirement id syntax (R-A7-8).
	// It is word anchored and excludes the AC-FR-… prefix so NFR-001 and
	// AC-FR-001-01 never alias FR-001 (v0.2.3 C-03).
	extractionFRIDPattern = regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])(FR-\d{3,})\b`)
	// extractionRHeadingIDPattern is the second declared-requirement id scheme
	// (FA-5 typical input): a Markdown heading whose text starts with R<2
	// digits> ("## R01 账号密码登录"). It is applied to heading text only, so
	// AC-R01-01 sub-items and body references never inflate the declared count.
	extractionRHeadingIDPattern = regexp.MustCompile(`^(R\d{2})\b`)
	// extractionFRHeadingPattern finds the heading sections that must be
	// scanned a second time ("功能需求" / FR 清单).
	extractionFRHeadingPattern = regexp.MustCompile(`(?i)功能需求|\bfr\b`)
	// extractionPlannedHeadingPattern marks a V2 / planned region.
	extractionPlannedHeadingPattern = regexp.MustCompile(`(?i)v2|规划|后续|未来|planned`)
	// extractionCurrentHeadingPattern marks a V1 / current region.
	extractionCurrentHeadingPattern = regexp.MustCompile(`(?i)v1|当前|本期|current`)
)

// ReconcileDocument counts the requirement ids declared by a requirements
// document and compares them with the ids the run actually extracted. Two id
// schemes are declared: FR-\d{3,} occurrences and R<2 digits> Markdown headings
// ("## R01 账号密码登录") whose AC-Rxx-yy sub-items are not separate ids.
//
// The first pass applies FR-\d{3,} to the whole document (deduplicated in
// appearance order) and adds the id of every R heading. The heading second pass
// re-scans every Markdown heading whose text contains "功能需求"/"FR" plus its
// following lines, so ids that only occur in a functional-requirements section
// are never lost. Headings marking V2/规划 split the declared ids into
// PlannedIDs; V1/当前 sections fill CurrentIDs. Expected/Extracted/Missing and
// MissingIDs are computed over the V1 current partition only; planned ids are
// counted separately and are never missing. An id the sink skipped because the
// requirement is already non-draft (input.SkippedIDs) is reported in
// SkippedIDs/Skipped and is never missing. The report never invents an id and
// never rewrites extracted ids: missing_ids is the set difference, in document
// order.
func ReconcileDocument(documentText string, input ReconciliationInput) ReconciliationReport {
	report := ReconciliationReport{
		LowConfidence: input.LowConfidence,
		SkippedImages: input.SkippedImages,
		MissingIDs:    []string{},
		SkippedIDs:    []string{},
		CurrentIDs:    []string{},
		PlannedIDs:    []string{},
	}
	lines := strings.Split(strings.ReplaceAll(documentText, "\r\n", "\n"), "\n")
	headings := make([]bool, len(lines))
	headingTexts := make([]string, len(lines))
	for index, line := range lines {
		if heading, ok := markdownHeadingText(strings.TrimSpace(line)); ok {
			headings[index] = true
			headingTexts[index] = heading
		}
	}

	declared := make([]string, 0)
	seen := make(map[string]struct{})
	planned := make(map[string]bool)
	regionPlanned := false
	add := func(id string) {
		if _, duplicate := seen[id]; duplicate {
			return
		}
		seen[id] = struct{}{}
		declared = append(declared, id)
		planned[id] = regionPlanned
	}

	// First pass: every declared id in document order, tagged with the V1/V2
	// region of its first occurrence.
	for index, line := range lines {
		if headings[index] {
			switch {
			case extractionPlannedHeadingPattern.MatchString(headingTexts[index]):
				regionPlanned = true
			case extractionCurrentHeadingPattern.MatchString(headingTexts[index]):
				regionPlanned = false
			}
			if matches := extractionRHeadingIDPattern.FindStringSubmatch(headingTexts[index]); len(matches) == 2 {
				add(matches[1])
			}
		}
		for _, match := range extractionFRIDPattern.FindAllStringSubmatch(line, -1) {
			if len(match) == 2 {
				add(match[1])
			}
		}
	}

	// Second pass: functional-requirements / FR headings and their sections.
	for index := 0; index < len(lines); index++ {
		if !headings[index] || !extractionFRHeadingPattern.MatchString(headingTexts[index]) {
			continue
		}
		for section := index; section < len(lines); section++ {
			if section > index && headings[section] {
				break
			}
			for _, match := range extractionFRIDPattern.FindAllStringSubmatch(lines[section], -1) {
				if len(match) == 2 {
					add(match[1])
				}
			}
		}
	}

	extracted := make(map[string]struct{}, len(input.ExtractedIDs))
	for _, id := range input.ExtractedIDs {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		extracted[trimmed] = struct{}{}
	}
	skipped := make(map[string]struct{}, len(input.SkippedIDs))
	for _, id := range input.SkippedIDs {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		skipped[trimmed] = struct{}{}
	}
	for _, id := range declared {
		if planned[id] {
			report.PlannedIDs = append(report.PlannedIDs, id)
			continue
		}
		report.CurrentIDs = append(report.CurrentIDs, id)
		if _, ok := extracted[id]; ok {
			report.Extracted++
			continue
		}
		if _, ok := skipped[id]; ok {
			report.SkippedIDs = append(report.SkippedIDs, id)
			continue
		}
		report.MissingIDs = append(report.MissingIDs, id)
	}
	report.Expected = len(report.CurrentIDs)
	report.PlannedCount = len(report.PlannedIDs)
	report.Missing = len(report.MissingIDs)
	report.Skipped = len(report.SkippedIDs)
	return report
}
