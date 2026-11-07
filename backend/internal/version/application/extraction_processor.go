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

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

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

// deterministicDeclaredFRPattern is the same declared-id syntax the
// reconciliation report uses (R-A7-8), so the extracted side and the declared
// side agree on what a requirement id is.
var deterministicDeclaredFRPattern = regexp.MustCompile(`FR-\d{3,}`)

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

// deterministicRequirementTitle turns one source line into a bounded title: the
// FR id is removed and a leading separator is trimmed; the heading path is the
// fallback because a line may contain only the id.
func deterministicRequirementTitle(line, externalID, headingPath string) string {
	title := strings.TrimSpace(strings.ReplaceAll(line, externalID, ""))
	title = strings.TrimLeft(title, " \t:：-—、.．,)）]】")
	if title == "" {
		title = strings.TrimSpace(headingPath)
	}
	if title == "" {
		title = externalID
	}
	return truncateExtractionText(title, 200)
}

func truncateExtractionText(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}

// deterministicSegmentRequirements emits one requirement per declared id of the
// segment, in document order. Two declaration schemes are recognized: every
// FR-<number> occurrence (the original W0 behavior) and every R<2 digits>
// Markdown heading (the FA-5 typical input). An R heading owns its section body
// and AC-Rxx-yy sub-items, so it yields exactly one requirement with a verbatim
// section quote; the AC items are acceptance criteria, never requirements.
func deterministicSegmentRequirements(segment Segment) []ExtractionRequirement {
	requirements := make([]ExtractionRequirement, 0)
	seen := make(map[string]struct{})
	lines := strings.Split(segment.Text, "\n")
	for index, line := range lines {
		for _, externalID := range deterministicDeclaredFRPattern.FindAllString(line, -1) {
			if _, duplicate := seen[externalID]; duplicate {
				continue
			}
			seen[externalID] = struct{}{}
			requirements = append(requirements, ExtractionRequirement{
				ExternalID:         externalID,
				Title:              deterministicRequirementTitle(line, externalID, segment.HeadingPath),
				Description:        truncateExtractionText(strings.TrimSpace(line), 2000),
				AcceptanceCriteria: []string{},
				Priority:           "P2",
				Category:           "functional",
				SourceQuote:        line,
				Confidence:         0.8,
				ConstraintsJSON:    json.RawMessage(`{}`),
			})
		}
		externalID, isHeading := deterministicRHeadingID(line)
		if !isHeading {
			continue
		}
		if _, duplicate := seen[externalID]; duplicate {
			continue
		}
		seen[externalID] = struct{}{}
		quote, body, bodyLines := deterministicRHeadingSection(lines, index)
		requirements = append(requirements, deterministicRHeadingRequirement(
			segment, line, externalID, quote, body, bodyLines))
	}
	return requirements
}

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
