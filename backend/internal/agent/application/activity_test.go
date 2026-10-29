package application

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// runFakeActivitySink captures the activity events a test emitter publishes.
type runFakeActivitySink struct {
	mu     sync.Mutex
	events []ActivityEvent
}

// PublishActivity implements EventSink.
func (s *runFakeActivitySink) PublishActivity(_ context.Context, event ActivityEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	return nil
}

// published returns a copy of the captured events.
func (s *runFakeActivitySink) published() []ActivityEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ActivityEvent(nil), s.events...)
}

// runTestActivityInt64 normalises the numeric detail values to int64 so the
// assertions do not depend on whether the emitter stored an int or an int64.
func runTestActivityInt64(t *testing.T, value any) int64 {
	t.Helper()
	switch number := value.(type) {
	case int64:
		return number
	case int:
		return int64(number)
	default:
		t.Fatalf("detail value %v (%T) is not an integer", value, value)
		return 0
	}
}

func TestSegmentBufferFlushesOnIntervalOrSize(t *testing.T) {
	base := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	buf := newSegmentBuffer(domain.KindThinking, 9, 42, base)

	// 1024 runes flush immediately, with part numbering starting at zero.
	buf.Append(strings.Repeat("思", segmentFlushRunes))
	if !buf.ShouldFlush(base) {
		t.Fatalf("ShouldFlush=false for a %d-rune buffer", segmentFlushRunes)
	}
	text, part, ok := buf.Flush(base, false)
	if !ok {
		t.Fatal("Flush returned ok=false for a non-empty buffer")
	}
	if part != 0 {
		t.Fatalf("first part=%d want 0", part)
	}
	if text != strings.Repeat("思", segmentFlushRunes) {
		t.Fatalf("flushed text length=%d runes want %d", utf8.RuneCountInString(text), segmentFlushRunes)
	}

	// The interval rule: not before 1500ms, exactly at 1500ms.
	buf.Append("短")
	if buf.ShouldFlush(base.Add(segmentFlushInterval - time.Millisecond)) {
		t.Fatal("ShouldFlush=true before the 1500ms interval elapsed")
	}
	if !buf.ShouldFlush(base.Add(segmentFlushInterval)) {
		t.Fatal("ShouldFlush=false after the 1500ms interval elapsed")
	}
	text, part, ok = buf.Flush(base.Add(segmentFlushInterval), false)
	if !ok || part != 1 || text != "短" {
		t.Fatalf("interval flush text=%q part=%d ok=%v want %q part 1", text, part, ok, "短")
	}

	// The final flush uses final=true and closes the buffer.
	buf.Append("结束")
	text, part, ok = buf.Flush(base.Add(2*segmentFlushInterval), true)
	if !ok || part != 2 || text != "结束" {
		t.Fatalf("final flush text=%q part=%d ok=%v want %q part 2", text, part, ok, "结束")
	}
	if _, _, ok := buf.Flush(base.Add(3*segmentFlushInterval), true); ok {
		t.Fatal("a finalized buffer must not flush again")
	}
}

func TestSegmentBufferFinalFlushAfterIncrementalFlush(t *testing.T) {
	base := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	buf := newSegmentBuffer(domain.KindText, 9, 42, base)
	buf.Append(strings.Repeat("答", segmentFlushRunes))
	if _, _, ok := buf.Flush(base, false); !ok {
		t.Fatal("incremental flush returned ok=false")
	}

	// The remainder is empty but the segment still needs its final=true marker
	// (contract: the segment end always emits final=true after incremental
	// chunks).
	text, part, ok := buf.Flush(base.Add(time.Second), true)
	if !ok {
		t.Fatal("final flush after an incremental flush returned ok=false")
	}
	if text != "" {
		t.Fatalf("empty remainder flushed text=%q want empty", text)
	}
	if part != 1 {
		t.Fatalf("final marker part=%d want 1", part)
	}

	// A buffer that never emitted anything must not emit an empty event.
	empty := newSegmentBuffer(domain.KindThinking, 9, 43, base)
	if _, _, ok := empty.Flush(base, true); ok {
		t.Fatal("an empty segment must not emit a final event")
	}
}

// TestActivityEmitterFallsBackToAgentChannel pins the P1 default: a session
// without a stored channel publishes on agent:<session_id> and carries no
// version id.
func TestActivityEmitterFallsBackToAgentChannel(t *testing.T) {
	sink := &runFakeActivitySink{}
	emitter := newActivityEmitter(sink, 5, "runtime-diagnostic", "", nil)

	if err := emitter.emit(context.Background(), 9, domain.KindRunStarted, "开始运行", nil); err != nil {
		t.Fatalf("emit returned error: %v", err)
	}
	events := sink.published()
	if len(events) != 1 {
		t.Fatalf("published events=%d want 1", len(events))
	}
	if want := domain.ChannelForSession(5); events[0].Channel != want {
		t.Fatalf("Channel=%q want the derived %q", events[0].Channel, want)
	}
	if events[0].VersionID != nil {
		t.Fatalf("VersionID=%v want nil for a session without a version stamp", events[0].VersionID)
	}
}

// TestActivityEmitterUsesVersionChannel pins the P3 routing: a session stamped
// with a version channel publishes on it and forwards the version id.
func TestActivityEmitterUsesVersionChannel(t *testing.T) {
	versionID := "42"
	sink := &runFakeActivitySink{}
	emitter := newActivityEmitter(sink, 5, "runtime-diagnostic", "version:42", &versionID)

	if err := emitter.emit(context.Background(), 9, domain.KindRunStarted, "开始运行", nil); err != nil {
		t.Fatalf("emit returned error: %v", err)
	}
	events := sink.published()
	if len(events) != 1 {
		t.Fatalf("published events=%d want 1", len(events))
	}
	if events[0].Channel != "version:42" {
		t.Fatalf("Channel=%q want the persisted version:42", events[0].Channel)
	}
	if events[0].VersionID == nil || *events[0].VersionID != versionID {
		t.Fatalf("VersionID=%v want %q", events[0].VersionID, versionID)
	}
}

func TestActivityEmitterSetsSegmentDetailAndTruncates(t *testing.T) {
	sink := &runFakeActivitySink{}
	emitter := newActivityEmitter(sink, 5, "runtime-diagnostic", "", nil)
	base := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

	buf := newSegmentBuffer(domain.KindText, 9, 42, base)
	long := strings.Repeat("答", activitySegmentTextLimit+100)
	buf.Append(long)
	text, part, ok := buf.Flush(base.Add(2*time.Second), true)
	if !ok {
		t.Fatal("Flush returned ok=false")
	}
	if err := emitter.emitSegment(context.Background(), buf, text, part, true); err != nil {
		t.Fatalf("emitSegment returned error: %v", err)
	}

	events := sink.published()
	if len(events) != 1 {
		t.Fatalf("published events=%d want 1", len(events))
	}
	event := events[0]
	if want := domain.ChannelForSession(5); event.Channel != want {
		t.Fatalf("Channel=%q want %q", event.Channel, want)
	}
	if event.AgentRole != "runtime-diagnostic" {
		t.Fatalf("AgentRole=%q want runtime-diagnostic", event.AgentRole)
	}
	if event.SessionID != 5 {
		t.Fatalf("SessionID=%d want 5", event.SessionID)
	}
	if event.Summary != strings.Repeat("答", activitySegmentSummaryLimit) {
		t.Fatalf("Summary=%q want the first %d runes of the segment", event.Summary, activitySegmentSummaryLimit)
	}

	detail := event.Detail
	if got := detail["kind"]; got != domain.KindText {
		t.Fatalf("detail[kind]=%v want %q", got, domain.KindText)
	}
	if got := runTestActivityInt64(t, detail["run_id"]); got != 9 {
		t.Fatalf("detail[run_id]=%d want 9", got)
	}
	if got := runTestActivityInt64(t, detail["message_id"]); got != 42 {
		t.Fatalf("detail[message_id]=%d want 42", got)
	}
	if got := runTestActivityInt64(t, detail["part"]); got != 0 {
		t.Fatalf("detail[part]=%d want 0", got)
	}
	if got := detail["final"]; got != true {
		t.Fatalf("detail[final]=%v want true", got)
	}
	if got := detail["truncated"]; got != true {
		t.Fatalf("detail[truncated]=%v want true", got)
	}
	flushed, ok := detail["text"].(string)
	if !ok {
		t.Fatalf("detail[text]=%T want string", detail["text"])
	}
	if n := utf8.RuneCountInString(flushed); n > activitySegmentTextLimit {
		t.Fatalf("detail[text] length=%d runes exceeds the %d limit", n, activitySegmentTextLimit)
	}
	if !strings.HasPrefix(flushed, strings.Repeat("答", activitySegmentTextLimit-1)) {
		t.Fatalf("detail[text] does not keep the head of the segment")
	}

	// emit enforces the global caps: summary <=500 runes, detail strings
	// <=activityDetailStringLimit runes.
	longSummary := strings.Repeat("摘", activitySummaryLimit+100)
	if err := emitter.emit(context.Background(), 9, domain.KindDecision, longSummary, map[string]any{
		"decision": "budget_stop",
		"note":     strings.Repeat("细", activityDetailStringLimit+100),
	}); err != nil {
		t.Fatalf("emit returned error: %v", err)
	}
	capped := sink.published()[1]
	if n := utf8.RuneCountInString(capped.Summary); n > activitySummaryLimit {
		t.Fatalf("Summary length=%d runes exceeds %d", n, activitySummaryLimit)
	}
	note, ok := capped.Detail["note"].(string)
	if !ok {
		t.Fatalf("detail[note]=%T want string", capped.Detail["note"])
	}
	if n := utf8.RuneCountInString(note); n > activityDetailStringLimit {
		t.Fatalf("detail[note] length=%d runes exceeds %d", n, activityDetailStringLimit)
	}
	if got := capped.Detail["kind"]; got != domain.KindDecision {
		t.Fatalf("detail[kind]=%v want %q", got, domain.KindDecision)
	}
}
