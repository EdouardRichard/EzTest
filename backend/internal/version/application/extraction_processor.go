package versionapp

// v0.22 A-7 deterministic W0 segment processor (Task 37 fix round 1).
//
// The production worker needs an ExtractionSegmentProcessor to turn a claimed
// segment into requirement drafts. A real LLM-backed extractor is not part of
// W0 (Task 36's requirement.extraction_emit tool is the long-term contract), so
// the composition root wires this deterministic extractor under Rule C5: it
// reads the segment text the deterministic chunker produced and emits every
// declared FR-<number> requirement with a verbatim source quote. The processor
// keeps the exact same contract as an LLM processor (sink writes, skipped
// image-only segments, retryable errors), so replacing it later is a one-line
// composition-root change.
//
// Segment text is never persisted by the worker package (the DB only stores
// positions and text_sha256), so the processor re-derives the plan from the
// run's own input document through ExtractionSegmentsSource and verifies the
// stored TextSHA256 before processing. A mismatch means the run and the
// document disagree; the segment is failed and retried rather than silently
// extracting the wrong text.
//
// v0.2.3 C-03 (docs/superpowers/specs/2026-11-10-eztest-v0.23-defectfix-design.md
// §3.6): the W0 processor is column aware. An FR id only declares a requirement
// at the start of a line/cell (so NFR-001 and AC-FR-001-01 never alias FR-001),
// catalog rows are parsed by header alias (never written as raw Markdown rows)
// and FR body declarations expose their BDD 场景 blocks as acceptance criteria.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
)

// DeterministicExtractionProcessor extracts declared FR ids from one
// deterministic segment. It is the W0 production processor (see file header).
type DeterministicExtractionProcessor struct {
	source ExtractionSegmentsSource
	sink   tools.ExtractionSink

	mu    sync.Mutex
	plans map[int64]extractionPlanCache
	order []int64
}

// extractionPlanCache is the re-derived segment plan of one run. The worker is
// single-process per run (segment claims are exclusive), and the cache is
// bounded, so a long-lived process cannot leak every document it ever parsed.
type extractionPlanCache struct {
	segments []Segment
}

const deterministicPlanCacheLimit = 8

// NewDeterministicExtractionProcessor builds the W0 processor around the
// attachment-side segment source and the extraction sink (normally the
// ExtractionService itself).
func NewDeterministicExtractionProcessor(source ExtractionSegmentsSource, sink tools.ExtractionSink) *DeterministicExtractionProcessor {
	return &DeterministicExtractionProcessor{
		source: source,
		sink:   sink,
		plans:  map[int64]extractionPlanCache{},
	}
}

var _ ExtractionSegmentProcessor = (*DeterministicExtractionProcessor)(nil)

// SetProcessor wires/replaces the segment processor. The composition root uses
// it after constructing the sink (the extraction service itself is the sink),
// which would otherwise create a constructor cycle.
func (s *ExtractionService) SetProcessor(processor ExtractionSegmentProcessor) {
	if s == nil {
		return
	}
	s.processor = processor
}

// SetSink wires/replaces the requirement sink (nil-safe composition helper).
func (p *DeterministicExtractionProcessor) SetSink(sink tools.ExtractionSink) {
	if p == nil {
		return
	}
	p.sink = sink
}

// ProcessSegment extracts the declared FR ids of one claimed segment. A segment
// that only carries image markers is skipped; a normal text segment with no
// declared FR id is done with zero requirements.
func (p *DeterministicExtractionProcessor) ProcessSegment(ctx context.Context, run ExtractionRun, segment ExtractionSegment) (ExtractionSegmentResult, error) {
	if p == nil || p.source == nil {
		return ExtractionSegmentResult{}, fmt.Errorf("deterministic extraction processor has no segment source")
	}
	plan, err := p.planSegment(ctx, run, segment.Seq)
	if err != nil {
		return ExtractionSegmentResult{}, err
	}
	if segment.TextSHA256 != "" && !strings.EqualFold(strings.TrimSpace(plan.TextSHA256), strings.TrimSpace(segment.TextSHA256)) {
		return ExtractionSegmentResult{}, fmt.Errorf(
			"extraction segment %d text changed (run %d): re-run the extraction", segment.Seq, run.ID)
	}
	if plan.Kind == SegmentKindImage {
		return ExtractionSegmentResult{
			Skipped:    true,
			SkipReason: "segment contains only embedded image references",
		}, nil
	}
	if p.sink == nil {
		return ExtractionSegmentResult{}, fmt.Errorf("deterministic extraction processor has no requirement sink")
	}
	requirements := deterministicSegmentRequirements(plan)
	if len(requirements) == 0 {
		return ExtractionSegmentResult{ExtractedCount: 0}, nil
	}
	counts, err := p.sink.EmitExtractedRequirements(ctx, ExtractionSource{
		VersionID:    run.VersionID,
		AttachmentID: run.AttachmentID,
		RunID:        run.ID,
		SegmentSeq:   segment.Seq,
	}, requirements)
	if err != nil {
		return ExtractionSegmentResult{}, err
	}
	return ExtractionSegmentResult{ExtractedCount: counts.Created + counts.Updated}, nil
}

// planSegment re-derives (or reuses) the run's segment plan and returns the
// claimed seq. A missing seq means the run cursor and the document plan
// disagree; the segment stays retryable instead of being marked done.
func (p *DeterministicExtractionProcessor) planSegment(ctx context.Context, run ExtractionRun, seq int) (Segment, error) {
	segments, err := p.segments(ctx, run)
	if err != nil {
		return Segment{}, err
	}
	for _, segment := range segments {
		if segment.Seq == seq {
			return segment, nil
		}
	}
	return Segment{}, fmt.Errorf("extraction run %d has no segment %d in its input document", run.ID, seq)
}

// segments caches the re-derived plan per run so a multi-segment run parses the
// document once (the parser is deterministic; the cache is bounded FIFO).
func (p *DeterministicExtractionProcessor) segments(ctx context.Context, run ExtractionRun) ([]Segment, error) {
	p.mu.Lock()
	if cached, ok := p.plans[run.ID]; ok {
		segments := cached.segments
		p.mu.Unlock()
		return segments, nil
	}
	p.mu.Unlock()

	segments, err := p.source.ExtractionSegments(ctx, run.VersionID, run.AttachmentID, run.InputSHA256)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.plans == nil {
		p.plans = map[int64]extractionPlanCache{}
	}
	if _, exists := p.plans[run.ID]; !exists {
		p.plans[run.ID] = extractionPlanCache{segments: segments}
		p.order = append(p.order, run.ID)
		if len(p.order) > deterministicPlanCacheLimit {
			oldest := p.order[0]
			p.order = p.order[1:]
			delete(p.plans, oldest)
		}
	}
	return segments, nil
}

// deterministicDeclaredFRPattern is the declared-id syntax used when scanning a
// whole document for reconciliation. It is word anchored and excludes the
// AC-FR-… sub-item prefix, so NFR-001 can never alias FR-001 and AC-FR-001-01
// can never alias FR-001.
var deterministicDeclaredFRPattern = regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])(FR-\d{3,})\b`)

// deterministicFRDeclarationPattern recognises a declaration line: the id must
// start the line (an optional Markdown heading marker is allowed), never appear
// in the middle of prose.
var deterministicFRDeclarationPattern = regexp.MustCompile(`^(?:#{1,6}\s+)?(FR-\d{3,})\b`)

// deterministicFRCellPattern recognises a declaration at the start of a table
// cell.
var deterministicFRCellPattern = regexp.MustCompile(`^(FR-\d{3,})\b`)

// deterministicDeclaredRHeadingPattern is the second declared-id scheme: a
// Markdown ATX heading whose text starts with R<2 digits> ("## R01 账号密码登录").
// The deterministic chunker strips the leading '#' markers into Segment.Text, so
// the pattern accepts both the raw heading line and the bare heading text.
// Anchoring at the line start keeps body references such as "见 R12" (or an
// AC-R01-01 sub-item) from being treated as a declaration; a bare R\d{2} match
// anywhere in the text would not be safe.
var deterministicDeclaredRHeadingPattern = regexp.MustCompile(`^(?:#{1,6}\s+)?(R\d{2})\b`)

// deterministicACIDPattern finds the AC-Rxx-yy sub-items of one R requirement.
var deterministicACIDPattern = regexp.MustCompile(`\bAC-(R\d{2})-\d{2}\b`)

// deterministicPriorityPattern finds the inline priority metadata of an FR body
// declaration (the labels may be concatenated: "…所属功能：账号优先级：P1").
var deterministicPriorityPattern = regexp.MustCompile(`优先级\s*[：:]\s*(P[0-3])`)

// deterministicTablePriorityPattern validates a catalog priority cell.
var deterministicTablePriorityPattern = regexp.MustCompile(`(?i)^P[0-3]$`)

// deterministicScenePattern recognises a BDD scene heading ("场景 1：…").
var deterministicScenePattern = regexp.MustCompile(`场景\s*\d+`)

// deterministicACFRPattern recognises an FR acceptance-criterion heading
// ("AC-FR-101-01 …").
var deterministicACFRPattern = regexp.MustCompile(`^AC-FR-[\d-]+\b`)

// extractionMaxDeclarationRunes bounds the declaration scan: a pathological
// single-line document must not be treated as a requirement title.
const extractionMaxDeclarationRunes = 300

var extractionFRMetadataLabels = []string{
	"需求编号", "所属功能", "需求类型", "需求状态", "优先级",
	"需求描述", "用户故事", "验收标准", "关联功能与页面", "验收场景",
}

var extractionFRDescriptionStopLabels = []string{"用户故事", "验收标准", "关联功能与页面"}

// deterministicRequirementTitle turns one source line into a bounded title: the
// FR id is removed and a leading separator is trimmed; the heading path is the
// fallback because a line may contain only the id. A trailing Word annotation
// such as "账号密码登录【P1】" is dropped (C-03).
func deterministicRequirementTitle(line, externalID, headingPath string) string {
	title := strings.TrimSpace(strings.ReplaceAll(line, externalID, ""))
	title = strings.TrimLeft(title, " \t:：-—、.．,)）]】")
	title = deterministicTrimTrailingAnnotation(title)
	if title == "" {
		title = strings.TrimSpace(headingPath)
	}
	if title == "" {
		title = externalID
	}
	return truncateExtractionText(title, 200)
}

var deterministicTrailingAnnotationPattern = regexp.MustCompile(`\s*【[^】]*】\s*$`)

// deterministicTrimTrailingAnnotation removes a trailing 【...】 annotation.
func deterministicTrimTrailingAnnotation(value string) string {
	return strings.TrimSpace(deterministicTrailingAnnotationPattern.ReplaceAllString(value, ""))
}

func truncateExtractionText(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}

// deterministicSegmentRequirements emits one requirement per declared id of the
// segment. It first parses every Markdown table block with its header/columns,
// then scans the remaining lines for FR body declarations. Tables are parsed
// first and merged with fill_empty so a later appendix catalog row can never
// downgrade a body declaration, regardless of the document order.
func deterministicSegmentRequirements(segment Segment) []ExtractionRequirement {
	requirements := make([]ExtractionRequirement, 0)
	indexByID := make(map[string]int)
	add := func(requirement ExtractionRequirement) {
		if at, exists := indexByID[requirement.ExternalID]; exists {
			requirements[at] = MergeExtractionRequirement(requirements[at], requirement)
			return
		}
		indexByID[requirement.ExternalID] = len(requirements)
		requirements = append(requirements, requirement)
	}

	lines := strings.Split(strings.ReplaceAll(segment.Text, "\r\n", "\n"), "\n")
	tableLines := make([]bool, len(lines))

	// Pass 1: column-aware Markdown tables. A table is only recognized with a
	// header row followed by a separator; a stray "| FR-777 |" line without a
	// header is not a requirement (guardrail E).
	for index := 0; index+1 < len(lines); index++ {
		if !isMarkdownTableRow(lines[index]) || !isMarkdownTableSeparator(lines[index+1]) {
			continue
		}
		headers := splitMarkdownTableRow(lines[index])
		tableLines[index] = true
		tableLines[index+1] = true
		for row := index + 2; row < len(lines); row++ {
			if !isMarkdownTableRow(lines[row]) || isMarkdownTableSeparator(lines[row]) {
				break
			}
			tableLines[row] = true
			cells := splitMarkdownTableRow(lines[row])
			if requirement, ok := deterministicTableRequirement(headers, cells, lines[row]); ok {
				add(requirement)
			}
		}
	}

	// Pass 2: FR body declarations (the id must start the line; table rows and
	// over-long lines are never declarations).
	for index, line := range lines {
		if tableLines[index] {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.ContainsRune(trimmed, '|') {
			continue
		}
		if utf8.RuneCountInString(trimmed) > extractionMaxDeclarationRunes {
			continue
		}
		externalID, isDeclaration := deterministicFRDeclaration(trimmed)
		if isDeclaration {
			end := deterministicFRBodyEnd(lines, tableLines, index)
			bodyLines := lines[index+1 : end]
			add(deterministicFRBodyRequirement(segment, trimmed, externalID, lines[index:end], bodyLines))
			continue
		}
		if rID, isHeading := deterministicRHeadingID(trimmed); isHeading {
			quote, body, bodyLines := deterministicRHeadingSection(lines, index)
			add(deterministicRHeadingRequirement(segment, trimmed, rID, quote, body, bodyLines))
		}
	}
	return requirements
}

// deterministicFRDeclaration reports the declared id when the line starts with
// one. An optional Markdown heading marker is accepted (the chunker may strip
// it) and an AC- prefix is never a declaration because the id must match at the
// line start.
func deterministicFRDeclaration(line string) (string, bool) {
	matches := deterministicFRDeclarationPattern.FindStringSubmatch(strings.TrimSpace(line))
	if len(matches) != 2 {
		return "", false
	}
	return matches[1], true
}

// deterministicFRBodyEnd returns the exclusive end of one FR body section: the
// next FR declaration or the next Markdown heading. Tables are kept inside the
// section so an AC sub-table can be read.
func deterministicFRBodyEnd(lines []string, tableLines []bool, start int) int {
	for index := start + 1; index < len(lines); index++ {
		if tableLines[index] {
			continue
		}
		trimmed := strings.TrimSpace(lines[index])
		if _, isDeclaration := deterministicFRDeclaration(trimmed); isDeclaration {
			return index
		}
		if _, isHeading := markdownHeadingText(trimmed); isHeading {
			return index
		}
	}
	return len(lines)
}

// deterministicFRBodyRequirement renders one FR body declaration: title from
// the heading line, description between 需求描述 and 用户故事/验收标准, and
// the BDD 场景 blocks as acceptance criteria (C-03).
func deterministicFRBodyRequirement(segment Segment, headingLine, externalID string, sectionLines, bodyLines []string) ExtractionRequirement {
	title := deterministicRequirementTitle(headingLine, externalID, segment.HeadingPath)
	metadata := deterministicFRMetadata(append([]string{headingLine}, bodyLines...))
	description := deterministicFRDescription(bodyLines, title)
	criteria := deterministicFRAcceptanceCriteria(bodyLines)
	constraints := map[string]any{}
	if metadata.module != "" {
		constraints["module"] = metadata.module
	}
	confidence := 0.8
	if len(criteria) == 0 {
		confidence = 0.55
		constraints["needs_review"] = true
		constraints["reason"] = "acceptance criteria not found in the FR body"
	}
	constraintsJSON := marshalExtractionConstraints(constraints)
	category := extractionCategory(metadata.kind, metadata.status)
	priority := metadata.priority
	if priority == "" {
		priority = "P2"
	}
	return ExtractionRequirement{
		ExternalID:         externalID,
		Title:              sanitizeExtractionField(title),
		Description:        sanitizeExtractionField(truncateExtractionText(description, 2000)),
		AcceptanceCriteria: criteria,
		Priority:           priority,
		Category:           category,
		SourceQuote:        truncateExtractionText(strings.Join(sectionLines, "\n"), 20000),
		Confidence:         confidence,
		ConstraintsJSON:    constraintsJSON,
		MergeMode:          ExtractionMergeReplace,
	}
}

// extractionFRMetadata is the inline metadata of one FR body declaration.
type extractionFRMetadata struct {
	module   string
	kind     string
	status   string
	priority string
}

// deterministicFRMetadata parses the concatenated labels of an FR body section.
func deterministicFRMetadata(lines []string) extractionFRMetadata {
	metadata := extractionFRMetadata{}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if matches := deterministicPriorityPattern.FindStringSubmatch(trimmed); len(matches) == 2 {
			metadata.priority = matches[1]
		}
		if value := extractionLabeledValue(trimmed, "所属功能"); value != "" {
			metadata.module = value
		}
		if value := extractionLabeledValue(trimmed, "需求类型"); value != "" {
			metadata.kind = value
		}
		if value := extractionLabeledValue(trimmed, "需求状态"); value != "" {
			metadata.status = value
		}
	}
	return metadata
}

// extractionLabeledValue reads "标签：值" where the value ends at the next known
// label (the document concatenates labels without separators). Go's regexp has
// no lookahead, so this is a manual non-greedy scan.
func extractionLabeledValue(line, label string) string {
	index := strings.Index(line, label)
	if index < 0 {
		return ""
	}
	rest := line[index+len(label):]
	rest = strings.TrimLeft(rest, " \t：:")
	end := len(rest)
	for _, stop := range extractionFRMetadataLabels {
		if stop == label {
			continue
		}
		if at := strings.Index(rest, stop); at >= 0 && at < end {
			end = at
		}
	}
	return strings.TrimSpace(rest[:end])
}

// deterministicFRDescription returns the text between 需求描述 and the
// 用户故事/验收标准/关联功能与页面 labels. Without a 需求描述 label the body
// text (without metadata) is used; the title is the final fallback.
func deterministicFRDescription(bodyLines []string, title string) string {
	acceptance := len(bodyLines)
	for index, line := range bodyLines {
		if strings.Contains(line, "验收标准") {
			acceptance = index
			break
		}
	}
	before := bodyLines[:acceptance]
	start := -1
	parts := make([]string, 0, len(before))
	for index, line := range before {
		at := strings.Index(line, "需求描述")
		if at < 0 {
			continue
		}
		start = index
		first := strings.TrimLeft(line[at+len("需求描述"):], " \t：:")
		parts = append(parts, cutExtractionAtLabels(first, extractionFRDescriptionStopLabels))
		break
	}
	if start >= 0 {
		for index := start + 1; index < len(before); index++ {
			line := strings.TrimSpace(before[index])
			if line == "" || isMarkdownTableRow(line) {
				continue
			}
			if containsExtractionLabel(line, extractionFRDescriptionStopLabels) {
				break
			}
			if isExtractionMetadataLine(line) {
				continue
			}
			parts = append(parts, line)
		}
	} else {
		for _, line := range before {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || isMarkdownTableRow(trimmed) || isExtractionMetadataLine(trimmed) {
				continue
			}
			parts = append(parts, trimmed)
		}
	}
	description := strings.TrimSpace(strings.Join(parts, "\n"))
	description = strings.TrimSpace(cutExtractionAtLabels(description, extractionFRDescriptionStopLabels))
	if description == "" {
		description = title
	}
	return sanitizeExtractionField(description)
}

// cutExtractionAtLabels truncates value at the first known label occurrence.
func cutExtractionAtLabels(value string, labels []string) string {
	end := len(value)
	for _, label := range labels {
		if at := strings.Index(value, label); at >= 0 && at < end {
			end = at
		}
	}
	return strings.TrimSpace(value[:end])
}

// containsExtractionLabel reports whether line contains one of the labels.
func containsExtractionLabel(line string, labels []string) bool {
	for _, label := range labels {
		if strings.Contains(line, label) {
			return true
		}
	}
	return false
}

// isExtractionMetadataLine reports a line made of the inline metadata labels.
func isExtractionMetadataLine(line string) bool {
	for _, label := range []string{"需求编号", "所属功能", "需求类型", "需求状态", "优先级"} {
		if strings.Contains(line, label) {
			return true
		}
	}
	return false
}

// deterministicFRAcceptanceCriteria collects the AC prose of one FR body:
// 场景 N blocks, AC-FR-… headings, bare Given/When/Then blocks and AC
// sub-table rows after 验收标准. Pure numbers are not acceptance criteria.
func deterministicFRAcceptanceCriteria(bodyLines []string) []string {
	criteria := make([]string, 0)
	start := 0
	for index, line := range bodyLines {
		if strings.Contains(line, "验收标准") {
			start = index + 1
			break
		}
	}
	current := make([]string, 0)
	inBlock := false
	flush := func() {
		if len(current) == 0 {
			inBlock = false
			return
		}
		if value := strings.TrimSpace(strings.Join(current, "\n")); value != "" {
			criteria = append(criteria, truncateExtractionText(value, 2000))
		}
		current = current[:0]
		inBlock = false
	}
	for index := start; index < len(bodyLines); index++ {
		line := strings.TrimSpace(bodyLines[index])
		if line == "" {
			// Blank lines inside a scene block separate the Given/When/Then
			// paragraphs; they must not split one scene into several criteria.
			if inBlock {
				current = append(current, "")
			}
			continue
		}
		if containsExtractionLabel(line, []string{"用户故事", "关联功能与页面"}) {
			flush()
			break
		}
		if deterministicScenePattern.MatchString(line) || deterministicACFRPattern.MatchString(strings.TrimLeft(line, "-* ")) {
			flush()
			inBlock = true
			current = append(current, line)
			continue
		}
		if !inBlock && isGivenWhenThenLine(line) {
			inBlock = true
			current = append(current, line)
			continue
		}
		if isMarkdownTableRow(line) {
			if isMarkdownTableSeparator(line) {
				continue
			}
			if index+1 < len(bodyLines) && isMarkdownTableSeparator(strings.TrimSpace(bodyLines[index+1])) {
				// Header row of an AC sub-table.
				continue
			}
			flush()
			if value := extractionProseTableRow(line); value != "" {
				criteria = append(criteria, truncateExtractionText(value, 2000))
			}
			continue
		}
		if !inBlock {
			continue
		}
		current = append(current, line)
	}
	flush()
	return criteria
}

// isGivenWhenThenLine recognizes a bare Given/When/Then step opening an AC
// block when no 场景 heading was written.
func isGivenWhenThenLine(line string) bool {
	lower := strings.ToLower(strings.TrimLeft(line, "-* "))
	for _, prefix := range []string{"given ", "when ", "then ", "假如", "当", "那么"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// extractionProseTableRow renders one AC sub-table row as prose, skipping pure
// numbers and rows that repeat an FR id.
func extractionProseTableRow(line string) string {
	cells := splitMarkdownTableRow(line)
	values := make([]string, 0, len(cells))
	for _, cell := range cells {
		value := strings.TrimSpace(cell)
		if value == "" || isPureNumeric(value) {
			continue
		}
		if len(deterministicDeclaredFRIDs(value)) > 0 {
			continue
		}
		values = append(values, value)
	}
	return strings.TrimSpace(strings.Join(values, " "))
}

// deterministicDeclaredFRIDs returns the declared FR ids of one cell/value,
// word anchored and without the AC- prefix.
func deterministicDeclaredFRIDs(value string) []string {
	matches := deterministicDeclaredFRPattern.FindAllStringSubmatch(value, -1)
	ids := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) == 2 {
			ids = append(ids, match[1])
		}
	}
	return ids
}

// ---------- column-aware Markdown tables ----------

// extractionTableColumns is the alias-mapped column layout of one table. -1
// means the column is absent.
type extractionTableColumns struct {
	id, title, description, priority, kind, status, module, ac, acCount int
}

func newExtractionTableColumns(headers []string) extractionTableColumns {
	columns := extractionTableColumns{
		id: -1, title: -1, description: -1, priority: -1,
		kind: -1, status: -1, module: -1, ac: -1, acCount: -1,
	}
	for index, header := range headers {
		switch normalizeExtractionHeader(header) {
		case "需求编号", "编号", "id", "需求号", "需求id":
			if columns.id < 0 {
				columns.id = index
			}
		case "需求名称", "名称", "需求标题", "标题", "功能名称", "需求名":
			if columns.title < 0 {
				columns.title = index
			}
		case "需求描述", "描述", "说明", "需求说明":
			if columns.description < 0 {
				columns.description = index
			}
		case "优先级", "优先级别":
			if columns.priority < 0 {
				columns.priority = index
			}
		case "需求类型", "类型", "分类", "需求分类":
			if columns.kind < 0 {
				columns.kind = index
			}
		case "需求状态", "状态", "当前系统状态", "系统状态":
			if columns.status < 0 {
				columns.status = index
			}
		case "所属功能", "模块", "功能域", "所属模块", "功能":
			if columns.module < 0 {
				columns.module = index
			}
		case "验收场景数", "场景数", "ac数", "ac数量", "验收标准数":
			if columns.acCount < 0 {
				columns.acCount = index
			}
		case "验收标准", "验收场景", "验收条件", "ac":
			if columns.ac < 0 {
				columns.ac = index
			}
		}
	}
	return columns
}

// normalizeExtractionHeader lowercases and strips the decoration Word adds to a
// header cell (bold markers, spaces, trailing colon).
func normalizeExtractionHeader(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.NewReplacer("*", "", "_", "", "`", "", " ", "", "\u00a0", "").Replace(value)
	return strings.TrimRight(value, "：:")
}

// deterministicTableRequirement turns one catalog row into a requirement. The
// title must come from the 需求名称 column (or the second column with a low
// confidence fallback); a raw Markdown row is never written.
func deterministicTableRequirement(headers, cells []string, line string) (ExtractionRequirement, bool) {
	columns := newExtractionTableColumns(headers)
	externalID, _ := deterministicTableID(columns, cells)
	if externalID == "" {
		return ExtractionRequirement{}, false
	}
	title, titleFallbackReason := deterministicTableTitle(columns, cells, externalID)
	if title == "" {
		return ExtractionRequirement{}, false
	}
	description := deterministicTableDescription(columns, cells, title)
	priority := "P2"
	if columns.priority >= 0 && columns.priority < len(cells) {
		if matches := deterministicTablePriorityPattern.FindString(strings.TrimSpace(cells[columns.priority])); matches != "" {
			priority = strings.ToUpper(matches)
		}
	}
	kind := extractionTableCell(columns.kind, cells)
	status := extractionTableCell(columns.status, cells)
	category := extractionCategory(kind, status)
	criteria := deterministicTableAcceptanceCriteria(columns, cells)
	constraints := map[string]any{}
	if module := extractionTableCell(columns.module, cells); module != "" {
		constraints["module"] = sanitizeExtractionField(module)
	}
	acCount := 0
	if columns.acCount >= 0 && columns.acCount < len(cells) {
		if parsed, err := strconv.Atoi(strings.TrimSpace(cells[columns.acCount])); err == nil && parsed >= 0 {
			acCount = parsed
			constraints["ac_count"] = parsed
		}
	}
	if acCount == 0 && columns.ac >= 0 && columns.ac < len(cells) && isPureNumeric(strings.TrimSpace(cells[columns.ac])) {
		if parsed, err := strconv.Atoi(strings.TrimSpace(cells[columns.ac])); err == nil && parsed >= 0 {
			acCount = parsed
			constraints["ac_count"] = parsed
		}
	}
	// Unmapped extra columns are preserved as open constraints (never a raw
	// row: each value is a trimmed cell).
	mapped := map[int]bool{}
	for _, index := range []int{columns.id, columns.title, columns.description, columns.priority, columns.kind, columns.status, columns.module, columns.acCount} {
		if index >= 0 {
			mapped[index] = true
		}
	}
	for index, cell := range cells {
		if mapped[index] || index >= len(headers) {
			continue
		}
		// The AC column is prose, not a constraint; index values may repeat.
		if index == columns.ac {
			continue
		}
		value := sanitizeExtractionField(cell)
		if value == "" {
			continue
		}
		key := normalizeExtractionHeader(headers[index])
		if key == "" {
			key = fmt.Sprintf("column_%d", index+1)
		}
		if _, exists := constraints[key]; !exists {
			constraints[key] = value
		}
	}
	confidence := 0.8
	if titleFallbackReason != "" {
		confidence = 0.55
		constraints["needs_review"] = true
		constraints["reason"] = titleFallbackReason
	}
	if description == "" {
		description = title
	}
	return ExtractionRequirement{
		ExternalID:         externalID,
		Title:              truncateExtractionText(sanitizeExtractionField(title), 200),
		Description:        sanitizeExtractionField(truncateExtractionText(description, 2000)),
		AcceptanceCriteria: criteria,
		Priority:           priority,
		Category:           category,
		SourceQuote:        truncateExtractionText(strings.TrimSpace(line), 20000),
		Confidence:         confidence,
		ConstraintsJSON:    marshalExtractionConstraints(constraints),
		MergeMode:          ExtractionMergeFillEmpty,
	}, true
}

// deterministicTableID reads the id from the mapped id column, then from any
// cell that starts with an FR id. A table row without a declaration is skipped.
func deterministicTableID(columns extractionTableColumns, cells []string) (string, int) {
	if columns.id >= 0 && columns.id < len(cells) {
		if matches := deterministicFRCellPattern.FindStringSubmatch(strings.TrimSpace(cells[columns.id])); len(matches) == 2 {
			return matches[1], columns.id
		}
	}
	for index, cell := range cells {
		if matches := deterministicFRCellPattern.FindStringSubmatch(strings.TrimSpace(cell)); len(matches) == 2 {
			return matches[1], index
		}
	}
	for index, cell := range cells {
		if ids := deterministicDeclaredFRIDs(cell); len(ids) > 0 {
			return ids[0], index
		}
	}
	return "", -1
}

// deterministicTableTitle returns the 需求名称 cell, or the second column with
// a low-confidence fallback. The second return value is the accurate fallback
// reason ("" when the mapped title column was used): an unmappable header and an
// empty title cell are different document defects and must not share one text.
// The title is never the raw Markdown row.
func deterministicTableTitle(columns extractionTableColumns, cells []string, externalID string) (string, string) {
	titleColumnMapped := columns.title >= 0 && columns.title < len(cells)
	if titleColumnMapped {
		if title := sanitizeExtractionField(cells[columns.title]); title != "" && title != externalID {
			return title, ""
		}
	}
	fallbackIndex := 1
	if columns.id == 1 && len(cells) > 2 {
		fallbackIndex = 2
	}
	if fallbackIndex < len(cells) {
		if title := sanitizeExtractionField(cells[fallbackIndex]); title != "" && title != externalID {
			switch {
			case !titleColumnMapped:
				return title, "table header could not be mapped; used the second column as the title"
			case fallbackIndex == columns.title:
				return title, "title column has no usable value; used the first non-empty cell as the title"
			default:
				return title, "title column is empty; used the second column as the title"
			}
		}
	}
	for index, cell := range cells {
		if index == columns.id {
			continue
		}
		if title := sanitizeExtractionField(cell); title != "" && title != externalID && !isPureNumeric(title) {
			if !titleColumnMapped {
				return title, "table header could not be mapped; used the first non-empty cell as the title"
			}
			return title, "title column is empty; used the first non-empty cell as the title"
		}
	}
	return "", ""
}

// deterministicTableDescription returns the 需求描述 cell when present, and a
// labeled summary otherwise (never the raw row).
func deterministicTableDescription(columns extractionTableColumns, cells []string, title string) string {
	if columns.description >= 0 && columns.description < len(cells) {
		if value := sanitizeExtractionField(cells[columns.description]); value != "" {
			return value
		}
	}
	parts := make([]string, 0, 5)
	if value := sanitizeExtractionField(extractionTableCell(columns.module, cells)); value != "" {
		parts = append(parts, "所属功能："+value)
	}
	if value := sanitizeExtractionField(extractionTableCell(columns.kind, cells)); value != "" {
		parts = append(parts, "需求类型："+value)
	}
	if value := sanitizeExtractionField(extractionTableCell(columns.priority, cells)); value != "" {
		parts = append(parts, "优先级："+strings.ToUpper(value))
	}
	if value := sanitizeExtractionField(extractionTableCell(columns.status, cells)); value != "" {
		parts = append(parts, "需求状态："+value)
	}
	if columns.acCount >= 0 && columns.acCount < len(cells) {
		if value := sanitizeExtractionField(cells[columns.acCount]); value != "" {
			parts = append(parts, "验收场景数："+value)
		}
	}
	if len(parts) == 0 {
		return title
	}
	return strings.Join(parts, "；")
}

// deterministicTableAcceptanceCriteria takes only prose AC cells: a pure number
// is a scene count, not an acceptance criterion.
func deterministicTableAcceptanceCriteria(columns extractionTableColumns, cells []string) []string {
	if columns.ac < 0 || columns.ac >= len(cells) {
		return []string{}
	}
	criteria := make([]string, 0)
	for _, part := range strings.FieldsFunc(cells[columns.ac], func(r rune) bool {
		return r == ';' || r == '；' || r == '\n'
	}) {
		value := sanitizeExtractionField(part)
		if value == "" || isPureNumeric(value) {
			continue
		}
		criteria = append(criteria, truncateExtractionText(value, 2000))
	}
	return criteria
}

// extractionTableCell returns the trimmed cell of one mapped column.
func extractionTableCell(column int, cells []string) string {
	if column < 0 || column >= len(cells) {
		return ""
	}
	return strings.TrimSpace(cells[column])
}

// extractionCategory derives planned/non_functional/functional from the type and
// status columns (C-03): V2/规划/延展/不存在 means the capability is planned.
func extractionCategory(kind, status string) string {
	signal := strings.ToLower(kind + " " + status)
	switch {
	case containsAnyExtractionMarker(signal, []string{"v2", "规划", "延展", "不存在", "后续", "未来", "planned"}):
		return "planned"
	case containsAnyExtractionMarker(signal, []string{"非功能", "non-functional", "nonfunctional", "non_functional", "性能", "安全"}):
		return "non_functional"
	default:
		return "functional"
	}
}

func containsAnyExtractionMarker(value string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

// isPureNumeric reports whether value is only digits (an AC count, not prose).
func isPureNumeric(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// marshalExtractionConstraints renders a constraints object; an empty map is
// stored as {} (the fixed core schema always expects an object).
func marshalExtractionConstraints(value map[string]any) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

// sanitizeExtractionField removes Markdown table pipes (guardrail E) and trims.
// The text keeps its other content; pipes become full-width slashes so a legit
// escaped cell still reaches the user without ever looking like a table row.
func sanitizeExtractionField(value string) string {
	value = strings.ReplaceAll(value, "|", "／")
	return strings.TrimSpace(value)
}

// isMarkdownTableRow reports a line carrying Markdown table pipes.
func isMarkdownTableRow(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	return unescapedPipeCount(trimmed) >= 2
}

// isMarkdownTableSeparator reports the | --- | --- | divider row.
func isMarkdownTableSeparator(line string) bool {
	cells := splitMarkdownTableRow(line)
	if len(cells) == 0 {
		return false
	}
	for _, cell := range cells {
		value := strings.TrimSpace(cell)
		value = strings.TrimLeft(value, ":")
		value = strings.TrimRight(value, ":")
		if len(value) < 3 {
			return false
		}
		for _, r := range value {
			if r != '-' {
				return false
			}
		}
	}
	return true
}

// splitMarkdownTableRow splits a row on unescaped pipes and unescapes `\|`.
func splitMarkdownTableRow(line string) []string {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimPrefix(trimmed, "|")
	trimmed = strings.TrimSuffix(trimmed, "|")
	cells := make([]string, 0, 4)
	var builder strings.Builder
	runes := []rune(trimmed)
	for index := 0; index < len(runes); index++ {
		if runes[index] == '\\' && index+1 < len(runes) && runes[index+1] == '|' {
			builder.WriteRune('|')
			index++
			continue
		}
		if runes[index] == '|' {
			cells = append(cells, strings.TrimSpace(builder.String()))
			builder.Reset()
			continue
		}
		builder.WriteRune(runes[index])
	}
	cells = append(cells, strings.TrimSpace(builder.String()))
	return cells
}

// unescapedPipeCount counts the unescaped table separators of one line.
func unescapedPipeCount(line string) int {
	count := 0
	runes := []rune(line)
	for index := 0; index < len(runes); index++ {
		if runes[index] == '\\' && index+1 < len(runes) && runes[index+1] == '|' {
			index++
			continue
		}
		if runes[index] == '|' {
			count++
		}
	}
	return count
}

// ---------- FA-5 R heading scheme (unchanged path) ----------

// deterministicRHeadingID reports the declared id of one source line when that
// line is an R heading.
func deterministicRHeadingID(line string) (string, bool) {
	matches := deterministicDeclaredRHeadingPattern.FindStringSubmatch(strings.TrimSpace(line))
	if len(matches) != 2 {
		return "", false
	}
	return matches[1], true
}

// deterministicRHeadingSection returns the verbatim section text of one R
// heading (the heading line plus the following body/AC lines up to the next R
// heading), its trimmed body, and the body lines.
func deterministicRHeadingSection(lines []string, index int) (quote, body string, bodyLines []string) {
	end := len(lines)
	for scan := index + 1; scan < len(lines); scan++ {
		if _, isHeading := deterministicRHeadingID(lines[scan]); isHeading {
			end = scan
			break
		}
	}
	quote = strings.Join(lines[index:end], "\n")
	bodyLines = lines[index+1 : end]
	body = strings.TrimSpace(strings.Join(bodyLines, "\n"))
	return quote, body, bodyLines
}

// deterministicRHeadingRequirement renders one R-heading requirement. The source
// quote is the verbatim heading + section body (including the AC text), so the
// row can always be traced back to the segment (R-A7-4).
func deterministicRHeadingRequirement(segment Segment, headingLine, externalID, quote, body string, bodyLines []string) ExtractionRequirement {
	title := deterministicRequirementTitle(headingLine, externalID, segment.HeadingPath)
	description := truncateExtractionText(body, 2000)
	if description == "" {
		description = title
	}
	return ExtractionRequirement{
		ExternalID:         externalID,
		Title:              title,
		Description:        description,
		AcceptanceCriteria: deterministicAcceptanceCriteria(externalID, bodyLines),
		Priority:           "P2",
		Category:           "functional",
		SourceQuote:        truncateExtractionText(quote, 20000),
		Confidence:         0.8,
		ConstraintsJSON:    json.RawMessage(`{}`),
		MergeMode:          ExtractionMergeReplace,
	}
}

// deterministicAcceptanceCriteria groups the AC-Rxx-yy blocks of one
// requirement from the section body. A block starts at its AC heading line and
// keeps the following Given/When/Then lines; AC items of other requirements are
// ignored, so they can never leak into this requirement.
func deterministicAcceptanceCriteria(externalID string, lines []string) []string {
	criteria := make([]string, 0)
	current := make([]string, 0)
	flush := func() {
		if len(current) == 0 {
			return
		}
		if value := strings.TrimSpace(strings.Join(current, "\n")); value != "" {
			criteria = append(criteria, truncateExtractionText(value, 2000))
		}
		current = current[:0]
	}
	for _, line := range lines {
		matches := deterministicACIDPattern.FindAllStringSubmatch(line, -1)
		if len(matches) == 0 {
			if len(current) > 0 {
				current = append(current, line)
			}
			continue
		}
		flush()
		for _, match := range matches {
			if match[1] == externalID {
				current = append(current, line)
				break
			}
		}
	}
	flush()
	return criteria
}
