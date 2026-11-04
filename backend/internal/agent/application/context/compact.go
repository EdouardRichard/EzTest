package agentcontext

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// SegmentSummary is the stable name of the rolling conversation summary that a
// boundary compaction appends to the packet. It belongs to the append-only
// suffix and is a derived segment: it is never part of the stable prefix and is
// superseded by the next boundary compaction.
const SegmentSummary = "summary"

// CompactionKeepRecent is the default number of compactable suffix segments
// replayed verbatim when a boundary compacts. Older history/tool-result
// segments are folded into the rolling summary (spec §6.4: summary + 近 N 条).
// The default mirrors the P1 conversation-window budget so both context paths
// keep the same amount of recent transcript. Callers that honor
// context.keep_recent_messages override it with NewCompactor(..., WithKeepRecent(n)).
const CompactionKeepRecent = 12

const (
	// maxCompactionSummaryRunes bounds the deterministic fallback summary.
	maxCompactionSummaryRunes = 2000
	// maxCompactionMessageRunes bounds one transcript line inside that summary.
	maxCompactionMessageRunes = 120
)

// ErrContextPacketNotFound reports that a version has no persisted context
// packet. Read adapters wrap it exactly once; the HTTP layer maps it to 404.
var ErrContextPacketNotFound = errors.New("agentcontext: context packet not found")

// Summarizer is the narrow compaction port: it folds older transcript messages
// into one rolling summary. agentapp.LLMSummarizer satisfies it structurally
// (agentapp.Message is an alias of Message), and the context package never
// imports agentapp, so the dependency stays one-way and cycle-free.
type Summarizer interface {
	Summarize(ctx context.Context, messages []Message) (string, error)
}

// CompactorOption customizes a Compactor built by NewCompactor.
type CompactorOption func(*Compactor)

// WithKeepRecent overrides the number of compactable suffix segments replayed
// verbatim at a boundary. It mirrors context.keep_recent_messages (default 12,
// min 0); a negative value is ignored so the default stays in effect. Zero is
// valid: every compactable segment folds into the rolling summary.
func WithKeepRecent(n int) CompactorOption {
	return func(c *Compactor) {
		if c == nil || n < 0 {
			return
		}
		c.keepRecent = n
		c.keepRecentSet = true
	}
}

// WithLogger sets the logger used to report compaction degradation. A
// summarizer failure falls back to the deterministic summary and is logged;
// nil keeps slog.Default().
func WithLogger(logger *slog.Logger) CompactorOption {
	return func(c *Compactor) {
		if c == nil || logger == nil {
			return
		}
		c.logger = logger
	}
}

// Compactor folds the compactable suffix of a packet into a rolling summary
// when the packet was assembled at a declared cache-invalidation boundary.
// The zero value is useful for deterministic summaries and keeps the default
// recent window; a nil summarizer also falls back to the deterministic summary.
type Compactor struct {
	summarizer    Summarizer
	keepRecent    int
	keepRecentSet bool
	logger        *slog.Logger
}

// NewCompactor wires the compaction summarizer. A nil summarizer is allowed;
// boundary compaction then uses the deterministic fallback. The recent window
// defaults to CompactionKeepRecent and can be configured with WithKeepRecent.
func NewCompactor(summarizer Summarizer, options ...CompactorOption) *Compactor {
	compactor := &Compactor{
		summarizer:    summarizer,
		keepRecent:    CompactionKeepRecent,
		keepRecentSet: true,
	}
	for _, option := range options {
		if option != nil {
			option(compactor)
		}
	}
	return compactor
}

// recentWindow returns the configured number of recent compactable segments.
// The zero-value Compactor has no explicit setting and falls back to the
// package default.
func (c *Compactor) recentWindow() int {
	if c == nil || !c.keepRecentSet {
		return CompactionKeepRecent
	}
	return c.keepRecent
}

// logWarn reports compaction degradation through the configured logger.
func (c *Compactor) logWarn(ctx context.Context, message string, args ...any) {
	logger := (*slog.Logger)(nil)
	if c != nil {
		logger = c.logger
	}
	if logger == nil {
		logger = slog.Default()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	logger.WarnContext(ctx, message, args...)
}

// Compact is the frozen package-level entry point from the P3-T4 contract. It
// applies boundary compaction with the deterministic summary fallback; callers
// that have a configured LLM gateway use NewCompactor(summarizer).Compact
// instead so the summary is model-generated.
//
// Compaction never fails a normal round: packets without a declared boundary
// are returned unchanged, and a summarizer failure falls back to the
// deterministic summary.
func Compact(ctx context.Context, packet ContextPacket) (ContextPacket, error) {
	return NewCompactor(nil).Compact(ctx, packet)
}

// Compact compacts one assembled packet at a declared boundary (spec §6.3).
//
//   - A normal round (empty InvalidationReason) is returned unchanged: prefix
//     bytes and the append-only suffix must not change.
//   - Only stage_boundary/session_reset/window_full trigger compaction; an
//     unknown non-empty reason fails closed.
//   - The stable prefix, the dynamic briefing and the rendered version facts are
//     pinned: they are never passed to the summarizer and never rewritten.
//   - The oldest history/tool-result segments beyond the recent window are
//     folded into a new summary segment that is appended to the packet; the
//     retained recent transcript stays byte-identical. History is never
//     rewritten and pinned facts always survive.
//   - Compacting the same boundary twice is a no-op, so one stage transition
//     compacts exactly once.
//
// The returned packet keeps the frozen ContextPacket shape (prefix_hash,
// segments, cached_tokens, cache_hit, invalidation_reason, briefing_hash); the
// invalidation_reason of the triggering boundary is preserved. A caller that
// continues the prompt chain after compaction passes the returned packet as
// AssemblyRequest.Previous: Build carries the summary into the next assembly
// before the newly replayed transcript.
func (c *Compactor) Compact(ctx context.Context, packet ContextPacket) (ContextPacket, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return ContextPacket{}, err
		}
	}
	invalidation := Invalidation(packet.InvalidationReason)
	if !invalidation.valid() {
		return ContextPacket{}, platform.Invalid("invalidation_reason",
			"must be one of stage_boundary, session_reset, window_full")
	}
	if invalidation == InvalidationNone {
		// Normal append-only round: compaction is forbidden, so the packet is
		// returned exactly as assembled.
		return packet, nil
	}

	var summarizer Summarizer
	keepRecent := CompactionKeepRecent
	if c != nil {
		summarizer = c.summarizer
		keepRecent = c.recentWindow()
	}
	compacted, summary := compactSegments(ctx, packet.Segments, summarizer, keepRecent, c)
	if summary == nil {
		// Nothing compactable (or the boundary arrived before the transcript
		// outgrew the verbatim window): keep the packet unchanged.
		return packet, nil
	}
	packet.Segments = compacted
	return packet, nil
}

// CompactForDecision applies one policy-aware trigger decision to an assembled
// packet (R-A5-2/R-A5-3). Unlike Compact it does not require the packet to
// carry a prefix-cache Invalidation boundary: pressure and overflow compact the
// append-only suffix while the stable prefix stays byte-identical, and
// stage_boundary is handled the same way once the caller has rebuilt the prefix.
// This method neither records nor applies the decision's retain budget; the
// caller carries it into the compaction transaction, and the token-based retain
// selection belongs to Task 13's summary wiring.
//
// The segment-window retention rule (CompactionKeepRecent / WithKeepRecent) is
// unchanged, and a repeated call on the same packet is a no-op. A packet with
// nothing compactable is returned unchanged.
func (c *Compactor) CompactForDecision(ctx context.Context, packet ContextPacket, decision CompactionDecision) (ContextPacket, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return ContextPacket{}, err
		}
	}
	if err := decision.Trigger.Validate(); err != nil {
		return ContextPacket{}, err
	}
	var summarizer Summarizer
	keepRecent := CompactionKeepRecent
	if c != nil {
		summarizer = c.summarizer
		keepRecent = c.recentWindow()
	}
	compacted, summary := compactSegments(ctx, packet.Segments, summarizer, keepRecent, c)
	if summary == nil {
		return packet, nil
	}
	packet.Segments = compacted
	return packet, nil
}

// compactableSegment reports whether a segment may be folded into the summary.
// The stable prefix, the briefing and the rendered facts are pinned. Summary
// segments are derived and are handled separately (superseded, never folded as
// plain history). Unknown segment names are treated as pinned: losing data the
// compactor does not understand is worse than keeping it.
func compactableSegment(segment Segment) bool {
	if segment.Prefix {
		return false
	}
	switch segment.Name {
	case SegmentHistory, SegmentToolResult:
		return true
	default:
		return false
	}
}

// compactSegments returns the compacted segment list and the appended summary
// segment, or (nil, nil) when the packet does not need compaction. keepRecent
// bounds how many compactable segments stay verbatim (0 is allowed). It is a
// pure function: the input slice is never mutated, so callers can keep the
// original packet for audit.
func compactSegments(ctx context.Context, segments []Segment, summarizer Summarizer, keepRecent int, compactor *Compactor) ([]Segment, *Segment) {
	compactable := make([]int, 0, len(segments))
	priorSummaries := make([]Segment, 0, 1)
	for index, segment := range segments {
		switch {
		case segment.Name == SegmentSummary:
			priorSummaries = append(priorSummaries, segment)
		case compactableSegment(segment):
			compactable = append(compactable, index)
		}
	}
	if len(compactable) <= keepRecent {
		// The newest transcript already fits the verbatim window. This also
		// makes a repeated Compact on the same boundary a no-op: compaction
		// keeps exactly keepRecent compactable segments.
		return nil, nil
	}
	foldCount := len(compactable) - keepRecent
	folded := make([]int, 0, foldCount)
	for _, index := range compactable[:foldCount] {
		folded = append(folded, index)
	}

	// The new rolling summary covers the previously folded summaries plus the
	// newly folded transcript, oldest material first.
	messages := make([]Message, 0, len(priorSummaries)+len(folded))
	for _, segment := range priorSummaries {
		messages = append(messages, Message{Role: segment.Role, Content: segment.Content})
	}
	for _, index := range folded {
		segment := segments[index]
		messages = append(messages, Message{
			Role:       segment.Role,
			Content:    segment.Content,
			ToolCallID: segment.ToolCallID,
		})
	}
	if len(messages) == 0 {
		return nil, nil
	}

	summaryText := ""
	if summarizer != nil {
		generated, err := summarizer.Summarize(ctx, messages)
		if err != nil {
			compactor.logWarn(ctx, "context compaction: summarizer failed; using deterministic summary", "error", err)
		} else {
			summaryText = strings.TrimSpace(generated)
		}
	}
	if summaryText != "" {
		// Model-generated summaries are untrusted input: bound them to the same
		// budget as the deterministic fallback so one bad response cannot blow
		// up the suffix.
		summaryText = truncateCompactionRunes(summaryText, maxCompactionSummaryRunes)
	}
	if summaryText == "" {
		summaryText = deterministicCompactionSummary(messages)
	}
	if strings.TrimSpace(summaryText) == "" {
		return nil, nil
	}

	out := make([]Segment, 0, len(segments)-len(folded)+1)
	foldedSet := make(map[int]struct{}, len(folded))
	for _, index := range folded {
		foldedSet[index] = struct{}{}
	}
	summarySegment := withTokens(Segment{
		Name:    SegmentSummary,
		Role:    roleUser,
		Content: summaryText,
	})
	// The rolling summary sits at the head of the transcript (spec §6.4:
	// briefing, conversation summary, 近 N 条): it is a new segment appended to
	// the packet before the retained recent messages, and it supersedes any
	// previous summary without touching history or the pinned segments.
	firstRetained := compactable[0]
	if foldCount < len(compactable) {
		firstRetained = compactable[foldCount]
	}
	inserted := false
	for index, segment := range segments {
		if index == firstRetained {
			out = append(out, summarySegment)
			inserted = true
		}
		if _, drop := foldedSet[index]; drop {
			continue
		}
		// Old summaries are superseded by the new rolling summary; history and
		// every pinned segment stay verbatim.
		if segment.Name == SegmentSummary {
			continue
		}
		out = append(out, segment)
	}
	if !inserted {
		// Defensive: every compactable segment was folded and the insertion
		// index was not reached; still append the summary as a new segment.
		out = append(out, summarySegment)
	}
	return out, &summarySegment
}

// deterministicCompactionSummary renders the fallback summary from the folded
// messages. It is deterministic (no clock, no randomness), never includes
// pinned material (callers only pass the compactable suffix) and is bounded by
// maxCompactionSummaryRunes.
func deterministicCompactionSummary(messages []Message) string {
	if len(messages) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("历史消息摘要:")
	for _, message := range messages {
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		builder.WriteString("\n- [")
		builder.WriteString(message.Role)
		builder.WriteString("] ")
		builder.WriteString(truncateCompactionRunes(content, maxCompactionMessageRunes))
	}
	return truncateCompactionRunes(strings.TrimSpace(builder.String()), maxCompactionSummaryRunes)
}

// truncateCompactionRunes cuts text to at most maxRunes runes while staying
// UTF-8 safe and reserving the ellipsis inside the limit.
func truncateCompactionRunes(text string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	if maxRunes == 1 {
		return truncationMarker
	}
	prefix := strings.TrimRight(string(runes[:maxRunes-1]), " \t\r\n")
	if prefix == "" {
		return truncationMarker
	}
	return prefix + truncationMarker
}
