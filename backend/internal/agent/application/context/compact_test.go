package agentcontext_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
	agentapi "github.com/EdouardRichard/EzTest/backend/internal/agent/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
)

/* -------------------------------- helpers -------------------------------- */

// longHistoryRequest returns a normal-round request whose transcript is long
// enough that a boundary compaction has something to fold.
func longHistoryRequest() agentcontext.AssemblyRequest {
	request := baseRequest()
	for index := 0; index < agentcontext.CompactionKeepRecent+3; index++ {
		request.History = append(request.History, agentcontext.Message{
			ID:      int64(100 + index),
			Role:    "assistant",
			Content: fmt.Sprintf("compactable-history-%02d", index),
		})
	}
	return request
}

func countSegments(packet agentcontext.ContextPacket, name string) int {
	count := 0
	for _, segment := range packet.Segments {
		if segment.Name == name {
			count++
		}
	}
	return count
}

func findSegment(packet agentcontext.ContextPacket, name string) (int, agentcontext.Segment, bool) {
	for index, segment := range packet.Segments {
		if segment.Name == name {
			return index, segment, true
		}
	}
	return -1, agentcontext.Segment{}, false
}

func compactableSegments(packet agentcontext.ContextPacket) []agentcontext.Segment {
	segments := make([]agentcontext.Segment, 0, len(packet.Segments))
	for _, segment := range packet.Segments {
		if segment.Name == agentcontext.SegmentHistory || segment.Name == agentcontext.SegmentToolResult {
			segments = append(segments, segment)
		}
	}
	return segments
}

func assertNoSummary(t *testing.T, packet agentcontext.ContextPacket) {
	t.Helper()
	if count := countSegments(packet, agentcontext.SegmentSummary); count != 0 {
		t.Fatalf("packet has %d summary segments, want 0", count)
	}
}

// summaryInputsContains reports whether any summarized message carries text.
func summaryInputsContains(messages []agentcontext.Message, text string) bool {
	for _, message := range messages {
		if strings.Contains(message.Content, text) {
			return true
		}
	}
	return false
}

// recordingSummarizer is the narrow injection port fake: it records the exact
// messages it was asked to compact and returns a fixed summary.
type recordingSummarizer struct {
	summary  string
	err      error
	calls    int
	messages []agentcontext.Message
}

func (s *recordingSummarizer) Summarize(_ context.Context, messages []agentcontext.Message) (string, error) {
	s.calls++
	s.messages = append([]agentcontext.Message(nil), messages...)
	if s.err != nil {
		return "", s.err
	}
	return s.summary, nil
}

/* ------------------------------- compaction ------------------------------ */

// TestCompactionOnlyAtBoundary pins the boundary contract: a normal round never
// compacts, a stage boundary compacts exactly once, the other two declared
// boundaries also compact, and an unknown reason fails closed.
func TestCompactionOnlyAtBoundary(t *testing.T) {
	assembler := agentcontext.NewAssembler()
	request := longHistoryRequest()

	normal, err := assembler.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("normal Build: %v", err)
	}
	if normal.Packet.InvalidationReason != "" {
		t.Fatalf("normal packet InvalidationReason = %q, want empty", normal.Packet.InvalidationReason)
	}
	unchanged, err := agentcontext.Compact(context.Background(), normal.Packet)
	if err != nil {
		t.Fatalf("compact normal round: %v", err)
	}
	if !reflect.DeepEqual(unchanged, normal.Packet) {
		t.Fatalf("normal round was compacted:\n%#v\nwant\n%#v", unchanged, normal.Packet)
	}
	assertNoSummary(t, unchanged)

	boundaryRequest := longHistoryRequest()
	boundaryRequest.Stage = "designing"
	boundaryRequest.Invalidation = agentcontext.InvalidationStageBoundary
	boundaryRequest.Previous = &normal.Packet
	boundary, err := assembler.Build(context.Background(), boundaryRequest)
	if err != nil {
		t.Fatalf("stage-boundary Build: %v", err)
	}
	if boundary.Packet.InvalidationReason != "stage_boundary" {
		t.Fatalf("stage-boundary InvalidationReason = %q", boundary.Packet.InvalidationReason)
	}

	compacted, err := agentcontext.Compact(context.Background(), boundary.Packet)
	if err != nil {
		t.Fatalf("stage-boundary Compact: %v", err)
	}
	if got := countSegments(compacted, agentcontext.SegmentSummary); got != 1 {
		t.Fatalf("summary segments = %d, want exactly 1", got)
	}
	if compacted.InvalidationReason != "stage_boundary" {
		t.Fatalf("compacted InvalidationReason = %q, want the recorded boundary", compacted.InvalidationReason)
	}
	summaryIndex, summary, ok := findSegment(compacted, agentcontext.SegmentSummary)
	if !ok {
		t.Fatalf("compacted packet has no summary segment: %#v", compacted.Segments)
	}
	if summary.Prefix {
		t.Fatal("summary segment was marked as part of the stable prefix")
	}
	if strings.TrimSpace(summary.Content) == "" || summary.Tokens <= 0 {
		t.Fatalf("summary segment has no content or token estimate: %#v", summary)
	}
	// The summary is a new segment after the pinned head and before the
	// retained recent transcript (spec §6.4). It never replaces the prefix,
	// briefing or facts, and it does not rewrite the retained history.
	if summaryIndex <= 2 {
		t.Fatalf("summary index = %d, want it after the pinned head", summaryIndex)
	}
	historyIndex, _, hasHistory := findSegment(compacted, agentcontext.SegmentHistory)
	if hasHistory && historyIndex < summaryIndex {
		t.Fatalf("summary index = %d is after the retained history index = %d", summaryIndex, historyIndex)
	}
	// The summary is appended: the pinned head and the retained recent
	// transcript keep their original order and content. Only the oldest
	// compactable segments are folded away.
	if got := len(compactableSegments(compacted)); got != agentcontext.CompactionKeepRecent {
		t.Fatalf("retained compactable segments = %d, want %d", got, agentcontext.CompactionKeepRecent)
	}
	if !reflect.DeepEqual(compacted.Segments[0], boundary.Packet.Segments[0]) {
		t.Fatal("compaction rewrote the stable prefix segment")
	}
	// The prefix hash and cache bookkeeping are carried through untouched.
	if compacted.PrefixHash != boundary.Packet.PrefixHash {
		t.Fatalf("PrefixHash changed: %s -> %s", boundary.Packet.PrefixHash, compacted.PrefixHash)
	}
	if compacted.BriefingHash != boundary.Packet.BriefingHash {
		t.Fatalf("BriefingHash changed: %s -> %s", boundary.Packet.BriefingHash, compacted.BriefingHash)
	}

	// A stage transition compacts once: compacting the result again is a no-op.
	again, err := agentcontext.Compact(context.Background(), compacted)
	if err != nil {
		t.Fatalf("second Compact: %v", err)
	}
	if !reflect.DeepEqual(again, compacted) {
		t.Fatalf("the same boundary compacted twice:\n%#v\nwant\n%#v", again, compacted)
	}
	if got := countSegments(again, agentcontext.SegmentSummary); got != 1 {
		t.Fatalf("summary segments after a repeated compact = %d, want 1", got)
	}

	// session_reset and window_full are the other two declared boundaries.
	for _, boundary := range []agentcontext.Invalidation{
		agentcontext.InvalidationSessionReset,
		agentcontext.InvalidationWindowFull,
	} {
		roundRequest := longHistoryRequest()
		roundRequest.Invalidation = boundary
		round, buildErr := assembler.Build(context.Background(), roundRequest)
		if buildErr != nil {
			t.Fatalf("%s Build: %v", boundary, buildErr)
		}
		result, compactErr := agentcontext.Compact(context.Background(), round.Packet)
		if compactErr != nil {
			t.Fatalf("%s Compact: %v", boundary, compactErr)
		}
		if got := countSegments(result, agentcontext.SegmentSummary); got != 1 {
			t.Fatalf("%s summary segments = %d, want 1", boundary, got)
		}
		if result.InvalidationReason != string(boundary) {
			t.Fatalf("%s InvalidationReason = %q", boundary, result.InvalidationReason)
		}
	}

	// An unknown reason must fail closed instead of silently compacting.
	unknown := normal.Packet
	unknown.InvalidationReason = "stage_slam"
	if _, err := agentcontext.Compact(context.Background(), unknown); err == nil {
		t.Fatal("unknown invalidation reason was compacted")
	}

	// A compacted packet re-enters the next assembly: Build carries the rolling
	// summary forward (Previous.Segments is consumed) and keeps the prefix
	// stable, so the next round is a cache hit.
	nextRequest := longHistoryRequest()
	nextRequest.Stage = "designing"
	nextRequest.Invalidation = agentcontext.InvalidationNone
	nextRequest.Previous = &compacted
	nextRequest.History = []agentcontext.Message{
		{ID: 900, Role: "user", Content: "stage-boundary-follow-up"},
	}
	nextRequest.Briefing = "## 目标/阶段\n目标：验证优惠券结算\n阶段：designing"
	next, err := assembler.Build(context.Background(), nextRequest)
	if err != nil {
		t.Fatalf("continuation Build: %v", err)
	}
	if next.Packet.PrefixHash != compacted.PrefixHash {
		t.Fatalf("continuation rewrote the prefix: %s -> %s", compacted.PrefixHash, next.Packet.PrefixHash)
	}
	if !next.Packet.CacheHit {
		t.Fatal("continuation with an unchanged prefix did not report a cache hit")
	}
	if got := countSegments(next.Packet, agentcontext.SegmentSummary); got != 1 {
		t.Fatalf("continuation summary segments = %d, want the carried summary", got)
	}
	carriedIndex, carriedSummary, carriedOK := findSegment(next.Packet, agentcontext.SegmentSummary)
	if !carriedOK || carriedSummary.Content != summary.Content || carriedSummary.Role != summary.Role {
		t.Fatalf("continuation summary = %#v, want the compacted summary %#v", carriedSummary, summary)
	}
	historyIndex = -1
	for index, segment := range next.Packet.Segments {
		if segment.Name == agentcontext.SegmentHistory && historyIndex < 0 {
			historyIndex = index
		}
	}
	if carriedIndex < 0 || historyIndex < 0 || carriedIndex > historyIndex {
		t.Fatalf("carried summary index = %d, first history index = %d; want the summary before new history",
			carriedIndex, historyIndex)
	}
	if !strings.Contains(suffixText(next), "stage-boundary-follow-up") {
		t.Fatalf("continuation lost the new transcript:\n%s", suffixText(next))
	}
}

// TestPinnedFactsSurviveCompaction proves compaction only touches the
// compactable history/tool-result material: the rendered facts (version truth)
// and the briefing are pinned, never sent to the summarizer, and never altered.
func TestPinnedFactsSurviveCompaction(t *testing.T) {
	assembler := agentcontext.NewAssembler()
	request := longHistoryRequest()
	request.Stage = "designing"
	request.Invalidation = agentcontext.InvalidationStageBoundary
	built, err := assembler.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	factsBefore := built.Packet.Segments[2]
	if factsBefore.Name != agentcontext.SegmentFacts {
		t.Fatalf("fixture facts segment index changed: %#v", factsBefore)
	}
	prefixBefore := built.Packet.Segments[0]
	if prefixBefore.Name != agentcontext.SegmentPrefix || !prefixBefore.Prefix {
		t.Fatalf("fixture prefix segment index changed: %#v", prefixBefore)
	}
	briefingBefore := built.Packet.Segments[1]
	if briefingBefore.Name != agentcontext.SegmentBriefing {
		t.Fatalf("fixture briefing segment index changed: %#v", briefingBefore)
	}
	original := append([]agentcontext.Segment(nil), built.Packet.Segments...)

	summarizer := &recordingSummarizer{summary: "滚动摘要：用户要求验证优惠券结算。"}
	compactor := agentcontext.NewCompactor(summarizer)
	compacted, err := compactor.Compact(context.Background(), built.Packet)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if summarizer.calls != 1 {
		t.Fatalf("summarizer calls = %d, want exactly 1", summarizer.calls)
	}
	if !reflect.DeepEqual(built.Packet.Segments, original) {
		t.Fatal("Compact mutated its input packet")
	}

	// Pinned version truth survives byte-for-byte at the same position.
	if got := compacted.Segments[0]; !reflect.DeepEqual(got, prefixBefore) {
		t.Fatalf("prefix segment survived as %#v, want %#v", got, prefixBefore)
	}
	if got := compacted.Segments[1]; got != briefingBefore {
		t.Fatalf("briefing segment survived as %#v, want %#v", got, briefingBefore)
	}
	if got := compacted.Segments[2]; !reflect.DeepEqual(got, factsBefore) {
		t.Fatalf("facts segment moved or changed: %#v", got)
	}
	if !strings.Contains(factsBefore.Content, "[api] auth") {
		t.Fatalf("fixture facts content = %q", factsBefore.Content)
	}

	// Pinned material is not part of the compactable input.
	if summaryInputsContains(summarizer.messages, "[api] auth") {
		t.Fatal("pinned facts were sent to the summarizer")
	}
	if summaryInputsContains(summarizer.messages, "版本不变量") || summaryInputsContains(summarizer.messages, "测试助手") {
		t.Fatal("the stable prefix was sent to the summarizer")
	}
	if summaryInputsContains(summarizer.messages, "## 目标/阶段") {
		t.Fatal("the briefing was sent to the summarizer")
	}
	// The folded old transcript is what the summary is generated from.
	if !summaryInputsContains(summarizer.messages, "compactable-history-00") {
		t.Fatal("old compactable history was not sent to the summarizer")
	}
	if len(summarizer.messages) == 0 {
		t.Fatal("summarizer received no messages")
	}

	// The summary is a new segment after the pinned head, not a replacement of
	// it, and the newest transcript is kept verbatim.
	summaryIndex, summary, ok := findSegment(compacted, agentcontext.SegmentSummary)
	if !ok {
		t.Fatalf("compacted packet has no summary segment: %#v", compacted.Segments)
	}
	if summaryIndex <= 2 {
		t.Fatalf("summary index = %d, want it after the pinned head", summaryIndex)
	}
	if summary.Content != summarizer.summary {
		t.Fatalf("summary content = %q, want the summarizer output", summary.Content)
	}
	if summary.Role != "user" {
		t.Fatalf("summary role = %q, want user", summary.Role)
	}
	retained := compactableSegments(compacted)
	if len(retained) != agentcontext.CompactionKeepRecent {
		t.Fatalf("retained compactable segments = %d, want %d", len(retained), agentcontext.CompactionKeepRecent)
	}
	if last := retained[len(retained)-1].Content; !strings.Contains(last, "compactable-history-14") {
		t.Fatalf("newest transcript was not retained verbatim: %q", last)
	}
	if got := compactableSegments(compacted); len(got) > 0 && strings.Contains(got[0].Content, "compactable-history-00") {
		t.Fatalf("oldest transcript was not folded: %q", got[0].Content)
	}

	// A failing summarizer falls back to the deterministic summary instead of
	// failing the run, and never duplicates pinned content. The failure is
	// logged; the discard logger keeps this test's output pristine.
	failing := &recordingSummarizer{err: errors.New("gateway down")}
	discardLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	fallback, err := agentcontext.NewCompactor(failing, agentcontext.WithLogger(discardLogger)).
		Compact(context.Background(), built.Packet)
	if err != nil {
		t.Fatalf("fallback Compact: %v", err)
	}
	fallbackSummaryIndex, fallbackSummary, ok := findSegment(fallback, agentcontext.SegmentSummary)
	if !ok || fallbackSummaryIndex <= 2 {
		t.Fatalf("fallback summary segment = %#v", fallbackSummary)
	}
	if strings.TrimSpace(fallbackSummary.Content) == "" {
		t.Fatal("deterministic fallback summary is empty")
	}
	if strings.Contains(fallbackSummary.Content, "[api] auth") {
		t.Fatalf("fallback summary includes pinned facts: %q", fallbackSummary.Content)
	}
	if !strings.Contains(fallbackSummary.Content, "compactable-history-00") {
		t.Fatalf("fallback summary does not cover the folded transcript: %q", fallbackSummary.Content)
	}

	// Boundary-only and once per boundary: even with a summarizer injected, a
	// normal round and a repeated compaction never call it.
	normal, err := assembler.Build(context.Background(), longHistoryRequest())
	if err != nil {
		t.Fatalf("normal Build: %v", err)
	}
	unused := &recordingSummarizer{summary: "must not be used"}
	unchanged, err := agentcontext.NewCompactor(unused).Compact(context.Background(), normal.Packet)
	if err != nil {
		t.Fatalf("normal Compact: %v", err)
	}
	if unused.calls != 0 {
		t.Fatalf("summarizer calls on a normal round = %d, want 0", unused.calls)
	}
	if !reflect.DeepEqual(unchanged, normal.Packet) {
		t.Fatal("normal round was compacted")
	}
	// compactor compacted built.Packet exactly once above; the stage transition
	// must not compact a second time.
	repeated, err := compactor.Compact(context.Background(), compacted)
	if err != nil {
		t.Fatalf("repeated Compact: %v", err)
	}
	if summarizer.calls != 1 {
		t.Fatalf("summarizer calls after a repeated compact = %d, want 1", summarizer.calls)
	}
	if !reflect.DeepEqual(repeated, compacted) {
		t.Fatal("the same boundary compacted twice")
	}
}

// TestBuildDropsSummaryOnSessionReset proves a fresh session cannot inherit a
// summary from a previous prompt chain: session_reset starts with no carried
// transcript, even when the caller still passes the compacted Previous packet.
func TestBuildDropsSummaryOnSessionReset(t *testing.T) {
	assembler := agentcontext.NewAssembler()
	boundary := longHistoryRequest()
	boundary.Stage = "designing"
	boundary.Invalidation = agentcontext.InvalidationStageBoundary
	built, err := assembler.Build(context.Background(), boundary)
	if err != nil {
		t.Fatalf("boundary Build: %v", err)
	}
	compacted, err := agentcontext.Compact(context.Background(), built.Packet)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	_, staleSummary, ok := findSegment(compacted, agentcontext.SegmentSummary)
	if !ok {
		t.Fatalf("fixture compacted packet has no summary: %#v", compacted.Segments)
	}

	resetRequest := longHistoryRequest()
	resetRequest.Stage = "clarifying"
	resetRequest.Invalidation = agentcontext.InvalidationSessionReset
	resetRequest.Previous = &compacted
	resetRequest.History = []agentcontext.Message{
		{ID: 900, Role: "user", Content: "fresh-session-question"},
	}
	fresh, err := assembler.Build(context.Background(), resetRequest)
	if err != nil {
		t.Fatalf("session_reset Build: %v", err)
	}
	if got := countSegments(fresh.Packet, agentcontext.SegmentSummary); got != 0 {
		t.Fatalf("session_reset carried %d stale summary segments into the fresh chain", got)
	}
	if strings.Contains(suffixText(fresh), staleSummary.Content) {
		t.Fatalf("session_reset leaked the previous summary into the fresh chain:\n%s", suffixText(fresh))
	}
	if !strings.Contains(suffixText(fresh), "fresh-session-question") {
		t.Fatalf("session_reset lost the fresh transcript:\n%s", suffixText(fresh))
	}
	if fresh.Packet.InvalidationReason != "session_reset" {
		t.Fatalf("session_reset InvalidationReason = %q", fresh.Packet.InvalidationReason)
	}
}

// TestCompactionKeepRecentOption pins the configurable recent window: Task 48
// injects context.keep_recent_messages (default 12, min 0) instead of relying
// on the package default, so the configured value must control how many recent
// segments stay verbatim and how much is folded into the rolling summary.
func TestCompactionKeepRecentOption(t *testing.T) {
	assembler := agentcontext.NewAssembler()
	request := longHistoryRequest()
	request.Stage = "designing"
	request.Invalidation = agentcontext.InvalidationStageBoundary
	built, err := assembler.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	const keepRecent = 3
	compacted, err := agentcontext.NewCompactor(nil, agentcontext.WithKeepRecent(keepRecent)).
		Compact(context.Background(), built.Packet)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	retained := compactableSegments(compacted)
	if len(retained) != keepRecent {
		t.Fatalf("retained compactable segments = %d, want the configured %d", len(retained), keepRecent)
	}
	if newest := retained[len(retained)-1].Content; !strings.Contains(newest, "compactable-history-14") {
		t.Fatalf("newest transcript was not retained verbatim: %q", newest)
	}
	_, summary, ok := findSegment(compacted, agentcontext.SegmentSummary)
	if !ok {
		t.Fatalf("compacted packet has no summary: %#v", compacted.Segments)
	}
	if !strings.Contains(summary.Content, "compactable-history-00") {
		t.Fatalf("summary does not cover the newly folded oldest transcript: %q", summary.Content)
	}
	if strings.Contains(summary.Content, "compactable-history-14") {
		t.Fatalf("summary includes the retained recent transcript: %q", summary.Content)
	}
}

// TestCompactionKeepRecentZero covers the configured minimum (0): every
// compactable segment folds and the summary still lands after the pinned head,
// without panicking on an empty retained window.
func TestCompactionKeepRecentZero(t *testing.T) {
	assembler := agentcontext.NewAssembler()
	request := longHistoryRequest()
	request.Stage = "designing"
	request.Invalidation = agentcontext.InvalidationStageBoundary
	built, err := assembler.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	compactor := agentcontext.NewCompactor(nil, agentcontext.WithKeepRecent(0))
	compacted, err := compactor.Compact(context.Background(), built.Packet)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if got := len(compactableSegments(compacted)); got != 0 {
		t.Fatalf("retained compactable segments = %d, want 0", got)
	}
	if got := countSegments(compacted, agentcontext.SegmentSummary); got != 1 {
		t.Fatalf("summary segments = %d, want 1", got)
	}
	if len(compacted.Segments) < 4 || compacted.Segments[3].Name != agentcontext.SegmentSummary {
		t.Fatalf("summary was not inserted right after the pinned head: %#v", compacted.Segments)
	}
	if compacted.Segments[0].Name != agentcontext.SegmentPrefix || compacted.Segments[2].Name != agentcontext.SegmentFacts {
		t.Fatalf("pinned head changed: %#v", compacted.Segments[:3])
	}

	again, err := compactor.Compact(context.Background(), compacted)
	if err != nil {
		t.Fatalf("repeated Compact: %v", err)
	}
	if !reflect.DeepEqual(again, compacted) {
		t.Fatal("the same boundary compacted twice with a zero recent window")
	}
}

// TestCompactionBoundsInjectedSummary proves model-generated summaries cannot
// enter the packet unbounded: the injected output is truncated to the same
// budget as the deterministic fallback.
func TestCompactionBoundsInjectedSummary(t *testing.T) {
	assembler := agentcontext.NewAssembler()
	request := longHistoryRequest()
	request.Stage = "designing"
	request.Invalidation = agentcontext.InvalidationStageBoundary
	built, err := assembler.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	longSummary := strings.Repeat("摘要", 3000) // 6000 runes, above the bound
	compactor := agentcontext.NewCompactor(&recordingSummarizer{summary: longSummary})
	compacted, err := compactor.Compact(context.Background(), built.Packet)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	_, summary, ok := findSegment(compacted, agentcontext.SegmentSummary)
	if !ok {
		t.Fatalf("compacted packet has no summary: %#v", compacted.Segments)
	}
	const maxSummaryRunes = 2000
	if got := utf8.RuneCountInString(summary.Content); got > maxSummaryRunes {
		t.Fatalf("injected summary has %d runes, want at most %d", got, maxSummaryRunes)
	}
	if !strings.HasSuffix(summary.Content, "…") {
		t.Fatalf("bounded summary is not marked as truncated: %q", summary.Content[len(summary.Content)-16:])
	}
}

// TestCompactionLogsSummarizerFailure pins observability for the fallback path:
// a summary failure is logged (without failing the run) so operators can see
// degraded rolling summaries.
func TestCompactionLogsSummarizerFailure(t *testing.T) {
	assembler := agentcontext.NewAssembler()
	request := longHistoryRequest()
	request.Stage = "designing"
	request.Invalidation = agentcontext.InvalidationStageBoundary
	built, err := assembler.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var logOutput bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logOutput, nil))
	failing := &recordingSummarizer{err: errors.New("gateway down")}
	compacted, err := agentcontext.NewCompactor(failing, agentcontext.WithLogger(logger)).
		Compact(context.Background(), built.Packet)
	if err != nil {
		t.Fatalf("Compact with a failing summarizer: %v", err)
	}
	if got := countSegments(compacted, agentcontext.SegmentSummary); got != 1 {
		t.Fatalf("summary segments after a summarizer failure = %d, want the deterministic fallback", got)
	}
	logged := logOutput.String()
	if !strings.Contains(logged, "summarizer") || !strings.Contains(logged, "gateway down") {
		t.Fatalf("summarizer failure was not logged: %q", logged)
	}
}

/* -------------------------------- read API ------------------------------- */

// fakeContextPacketReader is the narrow read port fake of the packet API.
type fakeContextPacketReader struct {
	packet    agentcontext.ContextPacket
	err       error
	calls     int
	versionID int64
}

func (f *fakeContextPacketReader) LatestContextPacket(_ context.Context, versionID int64) (agentcontext.ContextPacket, error) {
	f.calls++
	f.versionID = versionID
	if f.err != nil {
		return agentcontext.ContextPacket{}, f.err
	}
	return f.packet, nil
}

// contextPacketAuth mirrors the production middleware: it injects an actor
// with the requested role and defaults to admin so handler tests can exercise
// the admin-only route. X-Test-Anon simulates a missing session.
func contextPacketAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-Anon") != "" {
			httpx.Error(w, r, platform.Unauthenticated())
			return
		}
		role := r.Header.Get("X-Test-Role")
		if role == "" {
			role = "admin"
		}
		next.ServeHTTP(w, r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: 42, Role: role})))
	})
}

func newContextPacketServer(t *testing.T, reader agentapi.ContextPacketReader) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	agentapi.RegisterContextPacketRoutes(mux, contextPacketAuth, agentapi.NewContextPacketHandler(reader))
	return mux
}

func getContextPacket(t *testing.T, handler http.Handler, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// TestContextPacketReadAPI serves GET /api/versions/{id}/context-packets/latest
// and pins authentication, ID validation, not-found mapping and the frozen
// packet projection.
func TestContextPacketReadAPI(t *testing.T) {
	assembler := agentcontext.NewAssembler()
	request := baseRequest()
	request.Invalidation = agentcontext.InvalidationStageBoundary
	built, err := assembler.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	reader := &fakeContextPacketReader{packet: built.Packet}
	handler := newContextPacketServer(t, reader)

	recorder := getContextPacket(t, handler, "/api/versions/7/context-packets/latest", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if reader.calls != 1 || reader.versionID != 7 {
		t.Fatalf("reader calls = %d, version = %d; want 1 call for version 7", reader.calls, reader.versionID)
	}
	type decodedContextPacket struct {
		Data struct {
			PrefixHash         string `json:"prefix_hash"`
			CachedTokens       int    `json:"cached_tokens"`
			CacheHit           bool   `json:"cache_hit"`
			InvalidationReason string `json:"invalidation_reason"`
			BriefingHash       string `json:"briefing_hash"`
			Segments           []struct {
				Name       string `json:"name"`
				Role       string `json:"role"`
				Content    string `json:"content"`
				ToolCallID string `json:"tool_call_id"`
				Prefix     bool   `json:"prefix"`
				Tokens     int    `json:"tokens"`
			} `json:"segments"`
		} `json:"data"`
	}
	var payload decodedContextPacket
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode body %q: %v", recorder.Body.String(), err)
	}
	data := payload.Data
	if data.PrefixHash != built.Packet.PrefixHash || data.BriefingHash != built.Packet.BriefingHash {
		t.Fatalf("hashes = %#v", data)
	}
	if data.InvalidationReason != "stage_boundary" {
		t.Fatalf("invalidation_reason = %q", data.InvalidationReason)
	}
	if len(data.Segments) != len(built.Packet.Segments) {
		t.Fatalf("segments = %d, want %d", len(data.Segments), len(built.Packet.Segments))
	}
	if data.Segments[0].Name != agentcontext.SegmentPrefix || !data.Segments[0].Prefix {
		t.Fatalf("first segment = %#v", data.Segments[0])
	}
	if data.Segments[0].Content == "" || data.Segments[0].Tokens <= 0 {
		t.Fatalf("first segment = %#v", data.Segments[0])
	}
	if data.CachedTokens != built.Packet.CachedTokens || data.CacheHit != built.Packet.CacheHit {
		t.Fatalf("cache bookkeeping = cached_tokens:%d cache_hit:%v, want %d/%v",
			data.CachedTokens, data.CacheHit, built.Packet.CachedTokens, built.Packet.CacheHit)
	}

	// A later round with an unchanged prefix reports a cache hit, and a tool
	// result keeps its tool_call_id on the wire.
	firstRequest := baseRequest()
	firstBuilt, err := assembler.Build(context.Background(), firstRequest)
	if err != nil {
		t.Fatalf("cache fixture first Build: %v", err)
	}
	cachedRequest := baseRequest()
	cachedRequest.Previous = &firstBuilt.Packet
	cachedRequest.History = append(append([]agentcontext.Message(nil), firstRequest.History...),
		agentcontext.Message{ID: 13, Role: "assistant", Content: "第二轮回复"})
	cachedRequest.ToolResults = []agentcontext.ToolResult{
		{CallID: "call-7", Name: "read", Content: "工具原始结果"},
	}
	cachedBuilt, err := assembler.Build(context.Background(), cachedRequest)
	if err != nil {
		t.Fatalf("cache fixture second Build: %v", err)
	}
	if !cachedBuilt.Packet.CacheHit || cachedBuilt.Packet.CachedTokens <= 0 {
		t.Fatalf("cache fixture did not produce a cache hit: %#v", cachedBuilt.Packet)
	}
	cachedReader := &fakeContextPacketReader{packet: cachedBuilt.Packet}
	cachedHandler := newContextPacketServer(t, cachedReader)
	cachedRecorder := getContextPacket(t, cachedHandler, "/api/versions/7/context-packets/latest", nil)
	if cachedRecorder.Code != http.StatusOK {
		t.Fatalf("cached status = %d, body = %s", cachedRecorder.Code, cachedRecorder.Body.String())
	}
	var cachedPayload decodedContextPacket
	if err := json.Unmarshal(cachedRecorder.Body.Bytes(), &cachedPayload); err != nil {
		t.Fatalf("decode cached body %q: %v", cachedRecorder.Body.String(), err)
	}
	if cachedPayload.Data.CachedTokens != cachedBuilt.Packet.CachedTokens || !cachedPayload.Data.CacheHit {
		t.Fatalf("cache bookkeeping = cached_tokens:%d cache_hit:%v, want %d/true",
			cachedPayload.Data.CachedTokens, cachedPayload.Data.CacheHit, cachedBuilt.Packet.CachedTokens)
	}
	toolCallID := ""
	for _, segment := range cachedPayload.Data.Segments {
		if segment.Name == agentcontext.SegmentToolResult {
			toolCallID = segment.ToolCallID
		}
	}
	if toolCallID != "call-7" {
		t.Fatalf("tool_result tool_call_id = %q, want call-7 (segments %#v)", toolCallID, cachedPayload.Data.Segments)
	}

	// Authentication is mandatory and must not touch the reader.
	reader.calls = 0
	anon := getContextPacket(t, handler, "/api/versions/7/context-packets/latest",
		map[string]string{"X-Test-Anon": "1"})
	if anon.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", anon.Code)
	}
	if reader.calls != 0 {
		t.Fatalf("reader calls after an anonymous request = %d", reader.calls)
	}

	// Invalid and missing version IDs are rejected before the read.
	for _, path := range []string{
		"/api/versions/not-an-id/context-packets/latest",
		"/api/versions/0/context-packets/latest",
	} {
		reader.calls = 0
		bad := getContextPacket(t, handler, path, nil)
		if bad.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, body = %s", path, bad.Code, bad.Body.String())
		}
		if reader.calls != 0 {
			t.Fatalf("%s reached the reader", path)
		}
	}

	// No packet for the version maps to 404.
	notFound := newContextPacketServer(t, &fakeContextPacketReader{err: agentcontext.ErrContextPacketNotFound})
	missing := getContextPacket(t, notFound, "/api/versions/9/context-packets/latest", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing packet status = %d, body = %s", missing.Code, missing.Body.String())
	}

	// An unwired reader is unavailable rather than a panic.
	unwired := newContextPacketServer(t, nil)
	unavailable := getContextPacket(t, unwired, "/api/versions/9/context-packets/latest", nil)
	if unavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired reader status = %d", unavailable.Code)
	}

	// Other methods are not routed.
	postRequest := httptest.NewRequest(http.MethodPost, "/api/versions/7/context-packets/latest", nil)
	postRecorder := httptest.NewRecorder()
	handler.ServeHTTP(postRecorder, postRequest)
	if postRecorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", postRecorder.Code)
	}
}

// TestContextPacketReadAPIAdminOnly pins the permission decision for the packet
// read API: the payload contains the stable system-prompt prefix and the
// replayed transcript, so it is admin-only like the profile prompt read APIs.
// Ordinary logged-in users must not receive prompt content.
func TestContextPacketReadAPIAdminOnly(t *testing.T) {
	assembler := agentcontext.NewAssembler()
	built, err := assembler.Build(context.Background(), baseRequest())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	reader := &fakeContextPacketReader{packet: built.Packet}
	handler := newContextPacketServer(t, reader)

	for _, role := range []string{"member", "user"} {
		forbidden := getContextPacket(t, handler, "/api/versions/7/context-packets/latest",
			map[string]string{"X-Test-Role": role})
		if forbidden.Code != http.StatusForbidden {
			t.Fatalf("%s status = %d, body = %s", role, forbidden.Code, forbidden.Body.String())
		}
	}
	if reader.calls != 0 {
		t.Fatalf("non-admin requests reached the reader: %d calls", reader.calls)
	}

	admin := getContextPacket(t, handler, "/api/versions/7/context-packets/latest", nil)
	if admin.Code != http.StatusOK {
		t.Fatalf("admin status = %d, body = %s", admin.Code, admin.Body.String())
	}
}
