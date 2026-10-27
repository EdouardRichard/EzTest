package application

import (
	"context"
	"log/slog"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

const (
	// segmentFlushRunes is the maximum number of runes accumulated before a
	// thinking/text segment is published mid-stream (design spec §9.3).
	segmentFlushRunes = 1024
	// segmentFlushInterval bounds how long a segment may stay unpublished.
	segmentFlushInterval = 1500 * time.Millisecond

	// activitySummaryLimit is the envelope summary hard limit (spec §9.1).
	activitySummaryLimit = 500
	// activitySegmentSummaryLimit is the preview length of thinking/text
	// activities: the summary shows the head of the published chunk.
	activitySegmentSummaryLimit = 200
	// activitySegmentTextLimit bounds one thinking/text detail chunk (spec §9.2).
	activitySegmentTextLimit = 4096
	// activityDetailStringLimit is the generic safety cap applied to every
	// string value inside detail. Callers truncate specific fields tighter
	// (tool args/results at 2048) before emitting.
	activityDetailStringLimit = 4096
)

// segmentBuffer coalesces one continuous thinking or text segment into
// activities. Chunks are published every segmentFlushInterval or once
// segmentFlushRunes runes accumulated, and the segment end always closes with a
// final=true activity. Part indexes are per buffer so the frontend can splice
// chunks of one message back together by (message_id, part).
type segmentBuffer struct {
	kind      string
	runID     int64
	messageID int64
	part      int
	buf       []rune
	lastFlush time.Time

	// emitted records whether the buffer already published a chunk; a final
	// flush of an empty buffer only emits when the segment had visible output.
	emitted bool
	// finalized closes the buffer after the final=true flush.
	finalized bool
}

// newSegmentBuffer starts a segment for one model turn.
func newSegmentBuffer(kind string, runID, messageID int64, now time.Time) *segmentBuffer {
	return &segmentBuffer{
		kind:      kind,
		runID:     runID,
		messageID: messageID,
		lastFlush: now,
	}
}

// Append adds one streamed delta to the segment.
func (b *segmentBuffer) Append(delta string) {
	if b == nil || delta == "" || b.finalized {
		return
	}
	b.buf = append(b.buf, []rune(delta)...)
}

// ShouldFlush reports whether the segment reached the size or interval limit.
// An empty buffer may report true on the interval rule; Flush decides whether
// there is anything worth publishing.
func (b *segmentBuffer) ShouldFlush(now time.Time) bool {
	if b == nil || b.finalized {
		return false
	}
	if len(b.buf) >= segmentFlushRunes {
		return true
	}
	return now.Sub(b.lastFlush) >= segmentFlushInterval
}

// Flush publishes the buffered runes. final marks the last chunk of the
// segment: the remainder is published with final=true even when it is empty,
// provided the segment already had visible chunks. ok is false when there is
// nothing to publish.
func (b *segmentBuffer) Flush(now time.Time, final bool) (text string, part int, ok bool) {
	if b == nil || b.finalized {
		return "", 0, false
	}
	if len(b.buf) == 0 && (!final || !b.emitted) {
		return "", 0, false
	}
	text = string(b.buf)
	part = b.part
	b.part++
	b.buf = b.buf[:0]
	b.lastFlush = now
	b.emitted = true
	if final {
		b.finalized = true
	}
	return text, part, true
}

// activityEmitter publishes one session's activity events with the frozen
// envelope: channel agent:{session_id}, agent_role = profile key, summary
// bounded to 500 runes and every detail string bounded as a safety net.
type activityEmitter struct {
	sink      EventSink
	sessionID int64
	role      string
}

// newActivityEmitter binds an emitter to one session and agent role.
func newActivityEmitter(sink EventSink, sessionID int64, role string) *activityEmitter {
	return &activityEmitter{sink: sink, sessionID: sessionID, role: role}
}

// emit publishes one activity. run_id and kind are part of every detail object
// (the contract freezes kind as the discriminator). Publishing failures never
// fail a run: they are logged and returned for optional caller logging.
func (e *activityEmitter) emit(ctx context.Context, runID int64, kind, summary string, detail map[string]any) error {
	if e == nil || e.sink == nil {
		return nil
	}
	envelope := activityCloneDetail(detail)
	envelope["kind"] = kind
	envelope["run_id"] = runID

	event := ActivityEvent{
		Channel:   domain.ChannelForSession(e.sessionID),
		AgentRole: e.role,
		Summary:   activityTruncateString(summary, activitySummaryLimit),
		SessionID: e.sessionID,
		Detail:    envelope,
	}
	if err := e.sink.PublishActivity(ctx, event); err != nil {
		slog.ErrorContext(ctx, "agent: publish activity event",
			"kind", kind, "run_id", runID, "session_id", e.sessionID, "error", err)
		return err
	}
	return nil
}

// emitSegment publishes one thinking/text chunk.
func (e *activityEmitter) emitSegment(ctx context.Context, buf *segmentBuffer, text string, part int, final bool) error {
	if buf == nil {
		return nil
	}
	chunk, truncated := activityTruncateRunes(text, activitySegmentTextLimit)
	detail := map[string]any{
		"message_id": buf.messageID,
		"part":       part,
		"final":      final,
		"text":       chunk,
		"truncated":  truncated,
	}
	return e.emit(ctx, buf.runID, buf.kind, activitySummarize(chunk, activitySegmentSummaryLimit), detail)
}

// activityCloneDetail copies a detail object while bounding every string value.
// Copying keeps the caller's map untouched.
func activityCloneDetail(detail map[string]any) map[string]any {
	if detail == nil {
		return make(map[string]any, 2)
	}
	cloned := make(map[string]any, len(detail)+2)
	for key, value := range detail {
		cloned[key] = activityTruncateValue(value, activityDetailStringLimit)
	}
	return cloned
}

// activityTruncateValue walks the nested values a detail object may carry
// (usage maps, counts) and bounds their strings.
func activityTruncateValue(value any, limit int) any {
	switch typed := value.(type) {
	case string:
		return activityTruncateString(typed, limit)
	case map[string]any:
		cloned := make(map[string]any, len(typed))
		for key, nested := range typed {
			cloned[key] = activityTruncateValue(nested, limit)
		}
		return cloned
	case []any:
		cloned := make([]any, len(typed))
		for i, nested := range typed {
			cloned[i] = activityTruncateValue(nested, limit)
		}
		return cloned
	default:
		return value
	}
}

// activityTruncateString cuts s to at most limit runes, marking a cut with an
// ellipsis so consumers can see the value is incomplete.
func activityTruncateString(s string, limit int) string {
	truncated, _ := activityTruncateRunes(s, limit)
	return truncated
}

// activityTruncateRunes cuts s to at most limit runes (ellipsis included) and
// reports whether it cut.
func activityTruncateRunes(s string, limit int) (string, bool) {
	if limit <= 0 {
		return "", s != ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s, false
	}
	if limit == 1 {
		return string(runes[:1]), true
	}
	return string(runes[:limit-1]) + "…", true
}

// activitySummarize returns the first limit runes of s without a truncation
// marker; the summary is a preview, not an authoritative payload.
func activitySummarize(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}

// activityTruncateBytes cuts s to at most limit bytes (ellipsis included),
// keeping the result valid UTF-8. It is used for the tool args/result previews,
// whose contract is expressed in KB rather than characters.
func activityTruncateBytes(s string, limit int) (string, bool) {
	if limit <= 0 {
		return "", s != ""
	}
	if len(s) <= limit {
		return s, false
	}
	const ellipsis = "…"
	budget := limit - len(ellipsis)
	if budget < 0 {
		budget = 0
	}
	return runSafeBytePrefix(s, budget) + ellipsis, true
}
