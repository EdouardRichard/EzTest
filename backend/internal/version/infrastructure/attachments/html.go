package attachments

import (
	htmlpkg "html"
	"strings"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// htmlTokenKind classifies one token of the dependency-free HTML tokenizer.
type htmlTokenKind int

const (
	htmlTextToken htmlTokenKind = iota
	htmlStartTagToken
	htmlEndTagToken
)

type htmlToken struct {
	kind        htmlTokenKind
	name        string
	attrs       map[string]string
	text        string
	selfClosing bool
}

// extractHTMLResult extracts visible HTML content: heading hierarchy, lists,
// markdown tables and image alt/src references. Raw tags are never treated as
// body text, and script/style/head content is excluded (R-A7-3).
func extractHTMLResult(data []byte) (versionapp.ExtractionResult, error) {
	document := strings.TrimPrefix(string(data), "\uFEFF")
	tokens := tokenizeHTML(document)
	builder := newResultBuilder()
	if err := walkHTML(tokens, builder); err != nil {
		return versionapp.ExtractionResult{}, err
	}
	return builder.result(), nil
}

type htmlTable struct {
	rows [][]string
}

func walkHTML(tokens []htmlToken, builder *resultBuilder) error {
	var (
		block        strings.Builder
		blockKind    string
		blockLevel   int
		headingStack []string
		skipDepth    int
		inTable      bool
		table        *htmlTable
		row          []string
		cell         strings.Builder
		cellActive   bool
	)
	flushBlock := func() error {
		text := collapseHTMLText(block.String())
		kind := blockKind
		level := blockLevel
		block.Reset()
		blockKind = ""
		blockLevel = 0
		if text == "" {
			return nil
		}
		headingPath := renderHeadingPath(headingStack)
		prefix := ""
		switch kind {
		case "heading":
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
			if err := builder.element(versionapp.ExtractionElement{
				Kind: "heading", Text: text, Level: level, HeadingPath: headingPath,
			}); err != nil {
				return err
			}
		case "list_item":
			prefix = "- "
			if err := builder.element(versionapp.ExtractionElement{
				Kind: "list_item", Text: text, HeadingPath: headingPath,
			}); err != nil {
				return err
			}
		default:
			if err := builder.element(versionapp.ExtractionElement{
				Kind: "paragraph", Text: text, HeadingPath: headingPath,
			}); err != nil {
				return err
			}
		}
		return builder.addText(prefix + text + "\n")
	}
	flushTable := func() error {
		if table == nil || len(table.rows) == 0 {
			return nil
		}
		headingPath := renderHeadingPath(headingStack)
		for _, tableRow := range table.rows {
			cells := append([]string(nil), tableRow...)
			if err := builder.element(versionapp.ExtractionElement{
				Kind: "table_row", Text: strings.Join(cells, " | "), HeadingPath: headingPath, Row: cells,
			}); err != nil {
				return err
			}
		}
		return builder.addText(renderMarkdownTable(table.rows))
	}
	for _, token := range tokens {
		switch token.kind {
		case htmlTextToken:
			if skipDepth > 0 {
				continue
			}
			text := htmlpkg.UnescapeString(token.text)
			if inTable {
				if cellActive {
					cell.WriteString(text)
				}
				continue
			}
			block.WriteString(text)
		case htmlStartTagToken:
			if isHTMLSkippedTag(token.name) {
				skipDepth++
				continue
			}
			if skipDepth > 0 {
				continue
			}
			switch {
			case isHTMLHeading(token.name):
				if err := flushBlock(); err != nil {
					return err
				}
				blockKind = "heading"
				blockLevel = int(token.name[1] - '0')
			case isHTMLBlockTag(token.name):
				if err := flushBlock(); err != nil {
					return err
				}
			case token.name == "br":
				if inTable && cellActive {
					cell.WriteString(" ")
				} else {
					block.WriteString(" ")
				}
			case token.name == "li":
				if err := flushBlock(); err != nil {
					return err
				}
				blockKind = "list_item"
			case token.name == "table":
				if err := flushBlock(); err != nil {
					return err
				}
				inTable = true
				table = &htmlTable{}
			case token.name == "tr":
				row = nil
			case token.name == "th" || token.name == "td":
				cell.Reset()
				cellActive = true
			case token.name == "img":
				alt := strings.TrimSpace(htmlpkg.UnescapeString(token.attrs["alt"]))
				src := strings.TrimSpace(htmlpkg.UnescapeString(token.attrs["src"]))
				marker := "[图片"
				if alt != "" {
					marker += " " + alt
				}
				if src != "" {
					marker += "](" + src + ")"
				} else {
					marker += "]"
				}
				if inTable && cellActive {
					cell.WriteString(marker)
				} else {
					block.WriteString(marker)
				}
				if err := builder.element(versionapp.ExtractionElement{
					Kind: "image", Text: marker, Source: src, HeadingPath: renderHeadingPath(headingStack),
				}); err != nil {
					return err
				}
			}
		case htmlEndTagToken:
			if isHTMLSkippedTag(token.name) {
				if skipDepth > 0 {
					skipDepth--
				}
				continue
			}
			if skipDepth > 0 {
				continue
			}
			switch {
			case isHTMLHeading(token.name), isHTMLBlockTag(token.name), token.name == "li":
				if err := flushBlock(); err != nil {
					return err
				}
			case token.name == "th" || token.name == "td":
				if inTable {
					row = append(row, collapseHTMLText(cell.String()))
					cell.Reset()
					cellActive = false
				}
			case token.name == "tr":
				if inTable && table != nil {
					table.rows = append(table.rows, row)
					row = nil
				}
			case token.name == "table":
				if err := flushTable(); err != nil {
					return err
				}
				inTable = false
				table = nil
			}
		}
	}
	if skipDepth == 0 {
		if err := flushBlock(); err != nil {
			return err
		}
	}
	return nil
}

func isHTMLHeading(name string) bool {
	return len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6'
}

func isHTMLBlockTag(name string) bool {
	switch name {
	case "p", "div", "section", "article", "header", "footer", "main", "aside",
		"ul", "ol", "dl", "dt", "dd", "blockquote", "pre", "figure", "figcaption", "form":
		return true
	default:
		return false
	}
}

func isHTMLSkippedTag(name string) bool {
	switch name {
	case "script", "style", "head", "title", "template", "noscript", "svg", "math", "iframe":
		return true
	default:
		return false
	}
}

func collapseHTMLText(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// tokenizeHTML is a small, dependency-free tokenizer. It understands comments,
// doctypes, quoted/unquoted attributes and raw-text elements (script/style/
// title/textarea) so '<' inside them cannot be mistaken for markup.
func tokenizeHTML(document string) []htmlToken {
	tokens := make([]htmlToken, 0, len(document)/8)
	lower := strings.ToLower(document)
	rawTag := ""
	for index := 0; index < len(document); {
		if rawTag != "" {
			closing := "</" + rawTag
			end := strings.Index(lower[index:], closing)
			if end < 0 {
				tokens = append(tokens, htmlToken{kind: htmlTextToken, text: document[index:]})
				break
			}
			end += index
			if end > index {
				tokens = append(tokens, htmlToken{kind: htmlTextToken, text: document[index:end]})
			}
			index = end
			rawTag = ""
			continue
		}
		next := strings.IndexByte(document[index:], '<')
		if next < 0 {
			if index < len(document) {
				tokens = append(tokens, htmlToken{kind: htmlTextToken, text: document[index:]})
			}
			break
		}
		if next > 0 {
			tokens = append(tokens, htmlToken{kind: htmlTextToken, text: document[index : index+next]})
		}
		index += next
		remaining := document[index:]
		if strings.HasPrefix(remaining, "<!--") {
			end := strings.Index(remaining[4:], "-->")
			if end < 0 {
				break
			}
			index += 4 + end + 3
			continue
		}
		if strings.HasPrefix(remaining, "<!") || strings.HasPrefix(remaining, "<?") {
			end := strings.IndexByte(remaining, '>')
			if end < 0 {
				break
			}
			index += end + 1
			continue
		}
		closing := strings.HasPrefix(remaining, "</")
		nameStart := index + 1
		if closing {
			nameStart = index + 2
		}
		nameEnd := nameStart
		for nameEnd < len(document) && isHTMLNameChar(document[nameEnd]) {
			nameEnd++
		}
		name := strings.ToLower(document[nameStart:nameEnd])
		if name == "" {
			index = nameEnd + 1
			continue
		}
		attrs := make(map[string]string)
		selfClosing := false
		cursor := nameEnd
		for cursor < len(document) {
			for cursor < len(document) && isHTMLSpace(document[cursor]) {
				cursor++
			}
			if cursor >= len(document) {
				break
			}
			if document[cursor] == '>' {
				cursor++
				break
			}
			if document[cursor] == '/' {
				selfClosing = true
				cursor++
				continue
			}
			attrStart := cursor
			for cursor < len(document) && !isHTMLSpace(document[cursor]) &&
				document[cursor] != '=' && document[cursor] != '>' && document[cursor] != '/' {
				cursor++
			}
			attrName := strings.ToLower(document[attrStart:cursor])
			for cursor < len(document) && isHTMLSpace(document[cursor]) {
				cursor++
			}
			attrValue := ""
			if cursor < len(document) && document[cursor] == '=' {
				cursor++
				for cursor < len(document) && isHTMLSpace(document[cursor]) {
					cursor++
				}
				if cursor < len(document) && (document[cursor] == '"' || document[cursor] == '\'') {
					quote := document[cursor]
					cursor++
					valueStart := cursor
					for cursor < len(document) && document[cursor] != quote {
						cursor++
					}
					attrValue = document[valueStart:cursor]
					if cursor < len(document) {
						cursor++
					}
				} else {
					valueStart := cursor
					for cursor < len(document) && !isHTMLSpace(document[cursor]) && document[cursor] != '>' {
						cursor++
					}
					attrValue = document[valueStart:cursor]
				}
			}
			if attrName != "" {
				if _, exists := attrs[attrName]; !exists {
					attrs[attrName] = attrValue
				}
			}
		}
		index = cursor
		if closing {
			tokens = append(tokens, htmlToken{kind: htmlEndTagToken, name: name})
			continue
		}
		tokens = append(tokens, htmlToken{kind: htmlStartTagToken, name: name, attrs: attrs, selfClosing: selfClosing})
		if !selfClosing && isHTMLRawTextTag(name) {
			rawTag = name
		}
	}
	return tokens
}

func isHTMLRawTextTag(name string) bool {
	switch name {
	case "script", "style", "title", "textarea", "xmp":
		return true
	default:
		return false
	}
}

func isHTMLNameChar(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
		character >= '0' && character <= '9' || character == ':' || character == '-' || character == '_'
}

func isHTMLSpace(character byte) bool {
	switch character {
	case ' ', '\t', '\n', '\r', '\f':
		return true
	default:
		return false
	}
}
