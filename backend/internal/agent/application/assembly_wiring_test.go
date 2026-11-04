package agentapp_test

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// newBriefingRunnerFixture returns the standard runner fixture with the
// production briefing builder wired over the fake read port.
func newBriefingRunnerFixture(t *testing.T) *runnerFixture {
	t.Helper()
	return newRunnerFixture(t)
}

// fakeBriefingSource implements agentcontext.Reader for the runner assembly
// tests. The runner builds the version briefing through the production
// BriefingBuilder over this narrow read port.
type fakeBriefingSource struct {
	source    agentcontext.Source
	err       error
	calls     int
	versionID int64
}

func (f *fakeBriefingSource) Load(_ context.Context, versionID int64) (agentcontext.Source, error) {
	f.calls++
	f.versionID = versionID
	if f.err != nil {
		return agentcontext.Source{}, f.err
	}
	return f.source, nil
}

// defaultAssemblyBriefing is the deterministic briefing fixture of the runner
// tests: a stable goal/stage head so the cache-first prefix is reproducible.
func defaultAssemblyBriefing() *fakeBriefingSource {
	return &fakeBriefingSource{source: agentcontext.Source{
		Version: agentcontext.VersionSnapshot{Goal: "订单系统", Stage: "clarifying"},
		Requirements: agentcontext.RequirementsSnapshot{
			Confirmed: []agentcontext.RequirementSnapshot{{Title: "登录需求", Acceptance: "可登录"}},
		},
	}}
}

func decodeStoredPacket(t *testing.T, stored agent.ContextPacket) agentcontext.ContextPacket {
	t.Helper()
	packet, err := agentapp.DecodeContextPacket(stored)
	if err != nil {
		t.Fatalf("DecodeContextPacket: %v", err)
	}
	return packet
}

func assertSHA256Hex(t *testing.T, label, value string) {
	t.Helper()
	if len(value) != 64 {
		t.Fatalf("%s = %q, want a 64-hex digest", label, value)
	}
	if _, err := hex.DecodeString(value); err != nil {
		t.Fatalf("%s = %q is not hex: %v", label, value, err)
	}
}

func segmentContents(packet agentcontext.ContextPacket, name string) []string {
	contents := make([]string, 0)
	for _, segment := range packet.Segments {
		if segment.Name == name {
			contents = append(contents, segment.Content)
		}
	}
	return contents
}

// TestRunnerUsesAssemblerPrefixStable pins the cache-first wiring: the model
// prompt starts with the assembler's byte-stable prefix, dynamic data only ever
// lands in the append-only suffix, and a dynamic-only change across rounds does
// not rewrite the prefix.
func TestRunnerUsesAssemblerPrefixStable(t *testing.T) {
	fixture := newBriefingRunnerFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "第一轮回复"}}

	if _, err := mustRun(t, fixture, hostItem()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	firstRequest := fixture.chat.request(0)
	if len(firstRequest.Messages) < 2 {
		t.Fatalf("first prompt = %#v", firstRequest.Messages)
	}
	prefix := firstRequest.Messages[0]
	if prefix.Role != "system" || !strings.Contains(prefix.Content, "[profile:clarification_host]") {
		t.Fatalf("first prompt message is not the rendered profile prefix: %#v", prefix)
	}
	if strings.Contains(prefix.Content, "此前讨论了登录") {
		t.Fatalf("dynamic conversation summary leaked into the prefix: %s", prefix.Content)
	}
	firstPacket := decodeStoredPacket(t, fixture.repo.packets[0])
	if len(firstPacket.Segments) == 0 || firstPacket.Segments[0].Name != agentcontext.SegmentPrefix ||
		!firstPacket.Segments[0].Prefix {
		t.Fatalf("first packet is not prefix-first: %#v", firstPacket.Segments)
	}
	if firstPacket.Segments[0].Content != prefix.Content {
		t.Fatalf("prompt prefix and persisted prefix disagree")
	}
	assertSHA256Hex(t, "PrefixHash", firstPacket.PrefixHash)

	// Dynamic-only update: the rolling summary, transcript and briefing change;
	// the stable prefix must keep its exact bytes and digest.
	fixture.conv.snapshot.Summary = "动态摘要：改用手机号登录。"
	fixture.conv.snapshot.Messages = append(fixture.conv.snapshot.Messages,
		agentapp.ConversationMessage{ID: 502, Role: "assistant", Text: "动态追加的第二轮消息"})
	fixture.briefing.source.OpenQuestions = agentcontext.OpenQuestionsSnapshot{Questions: []string{"是否需要验证码？"}}
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "第二轮回复"}}

	if _, err := mustRun(t, fixture, hostItem()); err != nil {
		t.Fatalf("second run: %v", err)
	}
	secondRequest := fixture.chat.request(1)
	secondPrefix := secondRequest.Messages[0]
	if secondPrefix.Content != prefix.Content {
		t.Fatalf("stable prefix was rewritten by dynamic data:\nfirst: %s\nsecond: %s",
			prefix.Content, secondPrefix.Content)
	}
	secondPacket := decodeStoredPacket(t, fixture.repo.packets[1])
	if secondPacket.PrefixHash != firstPacket.PrefixHash {
		t.Fatalf("PrefixHash changed on a dynamic-only update: %s -> %s",
			firstPacket.PrefixHash, secondPacket.PrefixHash)
	}
	if secondPacket.BriefingHash == firstPacket.BriefingHash {
		t.Fatalf("BriefingHash did not change with the dynamic briefing")
	}

	var suffix strings.Builder
	for _, message := range secondRequest.Messages[1:] {
		suffix.WriteString(message.Content)
		suffix.WriteString("\n")
	}
	for _, want := range []string{"动态摘要：改用手机号登录。", "动态追加的第二轮消息", "是否需要验证码？"} {
		if !strings.Contains(suffix.String(), want) {
			t.Fatalf("dynamic change %q is missing from the suffix:\n%s", want, suffix.String())
		}
	}
	// History is replayed verbatim, never rewritten by the assembly.
	if contents := segmentContents(secondPacket, agentcontext.SegmentHistory); len(contents) == 0 ||
		contents[len(contents)-1] != "动态追加的第二轮消息" {
		t.Fatalf("history segments = %#v", contents)
	}
}

// TestRunnerPersistsContextPacket pins the cache-first persistence: the runner
// stores the assembler packet (ordered prefix/suffix segments plus the frozen
// cache bookkeeping) and not the P1 section shape, then points the run at it.
func TestRunnerPersistsContextPacket(t *testing.T) {
	fixture := newBriefingRunnerFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "已回复"}}

	result, err := mustRun(t, fixture, hostItem())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(fixture.repo.packets) != 1 {
		t.Fatalf("persisted packets = %d, want 1", len(fixture.repo.packets))
	}
	stored := fixture.repo.packets[0]
	if stored.RunID != result.AgentRunID || stored.VersionID != 10 {
		t.Fatalf("stored packet run/version = %d/%d, want %d/10",
			stored.RunID, stored.VersionID, result.AgentRunID)
	}
	packet := decodeStoredPacket(t, stored)
	assertSHA256Hex(t, "PrefixHash", packet.PrefixHash)
	assertSHA256Hex(t, "BriefingHash", packet.BriefingHash)
	if stored.Hash != packet.PrefixHash {
		t.Fatalf("stored hash = %q, want the prefix hash %q", stored.Hash, packet.PrefixHash)
	}
	if len(packet.Segments) < 2 || packet.Segments[0].Name != agentcontext.SegmentPrefix ||
		!packet.Segments[0].Prefix || packet.Segments[0].Role != "system" {
		t.Fatalf("segments = %#v", packet.Segments)
	}
	if packet.Segments[0].Content != fixture.chat.request(0).Messages[0].Content {
		t.Fatal("the model system message is not the persisted stable prefix")
	}
	if contents := segmentContents(packet, agentcontext.SegmentBriefing); len(contents) != 1 ||
		!strings.Contains(contents[0], "目标/阶段") {
		t.Fatalf("briefing segments = %#v", contents)
	}
	if contents := segmentContents(packet, agentcontext.SegmentHistory); len(contents) == 0 ||
		contents[len(contents)-1] != "我想做一个登录功能" {
		t.Fatalf("history segments = %#v", contents)
	}
	if packet.CachedTokens != 0 || packet.CacheHit {
		t.Fatalf("fresh chain cache bookkeeping = %d/%v, want 0/false",
			packet.CachedTokens, packet.CacheHit)
	}
	finished := fixture.repo.lastFinished(t)
	if finished.ContextPacketID == nil || *finished.ContextPacketID != stored.ID {
		t.Fatalf("run context_packet_id = %v, want %d", finished.ContextPacketID, stored.ID)
	}
}

// TestRunnerProviderCacheStatsOverwrite pins contract §18.2.6: the provider
// usage is authoritative for the prompt-cache bookkeeping and overwrites the
// assembler's local estimate; the overwrite is persisted and published once.
func TestRunnerProviderCacheStatsOverwrite(t *testing.T) {
	fixture := newBriefingRunnerFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{{
		Content: "已回复",
		Usage: map[string]int{
			"prompt_tokens":     1200,
			"completion_tokens": 40,
			"total_tokens":      1240,
			"cached_tokens":     640,
			"cache_hit":         1,
		},
	}}

	if _, err := mustRun(t, fixture, hostItem()); err != nil {
		t.Fatalf("run: %v", err)
	}
	packet := decodeStoredPacket(t, fixture.repo.packets[0])
	if packet.CachedTokens != 640 || !packet.CacheHit {
		t.Fatalf("cache bookkeeping = cached_tokens:%d cache_hit:%v, want the provider 640/true",
			packet.CachedTokens, packet.CacheHit)
	}
	if fixture.repo.packetCacheUpdates != 1 {
		t.Fatalf("repository cache updates = %d, want 1", fixture.repo.packetCacheUpdates)
	}
	finished := fixture.repo.lastFinished(t)
	if finished.Cache["cached_tokens"] != 640 || finished.Cache["cache_hit"] != 1 {
		t.Fatalf("agent_run.cache = %#v, want the provider cache accounting", finished.Cache)
	}

	var payload map[string]any
	for _, event := range fixture.events.events {
		if event.Type == "context.cache_stats" {
			payload = event.Payload
		}
	}
	if payload == nil {
		t.Fatalf("events = %v, want one context.cache_stats", eventTypes(fixture.events))
	}
	if payload["cached_tokens"] != 640 || payload["cache_hit"] != true {
		t.Fatalf("context.cache_stats payload = %#v", payload)
	}
	if payload["prefix_hash"] != packet.PrefixHash || payload["briefing_hash"] != packet.BriefingHash {
		t.Fatalf("context.cache_stats hashes = %#v", payload)
	}

	// A provider that reports no prompt-cache statistics leaves the local
	// estimate untouched and publishes no cache_stats event.
	plain := newBriefingRunnerFixture(t)
	plain.chat.responses = []llmapp.ChatResponse{{Content: "已回复", Usage: map[string]int{"prompt_tokens": 900}}}
	if _, err := mustRun(t, plain, hostItem()); err != nil {
		t.Fatalf("plain run: %v", err)
	}
	plainPacket := decodeStoredPacket(t, plain.repo.packets[0])
	if plainPacket.CachedTokens != 0 || plainPacket.CacheHit {
		t.Fatalf("plain cache bookkeeping = %d/%v, want the local 0/false",
			plainPacket.CachedTokens, plainPacket.CacheHit)
	}
	if plain.repo.packetCacheUpdates != 0 {
		t.Fatalf("plain repository cache updates = %d, want 0", plain.repo.packetCacheUpdates)
	}
	if containsEvent(plain.events, "context.cache_stats") {
		t.Fatalf("plain events = %v, want no context.cache_stats", eventTypes(plain.events))
	}
}

// TestRunnerPersistsContextCompactionAtWindowBoundary pins boundary-only
// compaction: an overflowing window folds the oldest transcript into the
// rolling summary, keeps exactly the configured recent window verbatim, renders
// the retry prompt from the compacted packet and never rewrites the prefix.
func TestRunnerPersistsContextCompactionAtWindowBoundary(t *testing.T) {
	fixture := newBriefingRunnerFixture(t)
	fixture.settings.ints["context.keep_recent_messages"] = 2
	fixture.conv.snapshot.Summary = "更早的滚动摘要"
	fixture.conv.snapshot.Messages = []agentapp.ConversationMessage{
		{ID: 601, Role: "user", Text: "消息一：需求背景"},
		{ID: 602, Role: "assistant", Text: "消息二：澄清回复"},
		{ID: 603, Role: "user", Text: "消息三：补充需求"},
		{ID: 604, Role: "assistant", Text: "消息四：继续澄清"},
		{ID: 605, Role: "user", Text: "消息五：确认范围"},
		{ID: 606, Role: "assistant", Text: "消息六：最新回复"},
	}
	fixture.chat.errors = []error{fmt.Errorf("upstream: %w", llmapp.ErrContextOverflow)}
	fixture.chat.responses = []llmapp.ChatResponse{
		{Content: "不应被采用的回复"},
		{Content: "压缩后的摘要"},
		{Content: "恢复后的回复"},
	}

	if _, err := mustRun(t, fixture, hostItem()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(fixture.repo.packets) != 2 {
		t.Fatalf("persisted packets = %d, want prepare + compacted", len(fixture.repo.packets))
	}
	prepared := decodeStoredPacket(t, fixture.repo.packets[0])
	compacted := decodeStoredPacket(t, fixture.repo.packets[1])
	if compacted.InvalidationReason != string(agentcontext.InvalidationWindowFull) {
		t.Fatalf("invalidation reason = %q, want window_full", compacted.InvalidationReason)
	}
	if compacted.PrefixHash != prepared.PrefixHash {
		t.Fatalf("compaction rewrote the prefix: %s -> %s", prepared.PrefixHash, compacted.PrefixHash)
	}
	summary := segmentContents(compacted, agentcontext.SegmentSummary)
	if len(summary) != 1 || !strings.Contains(summary[0], "压缩后的摘要") {
		t.Fatalf("summary segments = %#v", summary)
	}
	history := segmentContents(compacted, agentcontext.SegmentHistory)
	if len(history) != 2 || history[0] != "消息五：确认范围" || history[1] != "消息六：最新回复" {
		t.Fatalf("recent history = %#v, want the 2 newest messages verbatim", history)
	}
	retry := requestPrompt(fixture.chat.request(2))
	for _, folded := range []string{"消息一：需求背景", "消息二：澄清回复", "消息三：补充需求", "消息四：继续澄清"} {
		if strings.Contains(retry, folded) {
			t.Fatalf("compact retry replayed the folded message %q:\n%s", folded, retry)
		}
	}
	for _, kept := range []string{"消息五：确认范围", "消息六：最新回复", "压缩后的摘要"} {
		if !strings.Contains(retry, kept) {
			t.Fatalf("compact retry misses %q:\n%s", kept, retry)
		}
	}
	if len(fixture.conv.summaries) == 0 || !strings.Contains(fixture.conv.summaries[len(fixture.conv.summaries)-1], "压缩后的摘要") {
		t.Fatalf("conversation summaries = %#v", fixture.conv.summaries)
	}
	if !containsEvent(fixture.events, "context.compacted") {
		t.Fatalf("events = %v, want context.compacted", eventTypes(fixture.events))
	}
}

// compactedEventPayload returns the single context.compacted payload of a run.
func compactedEventPayload(t *testing.T, publisher *fakePublisher) map[string]any {
	t.Helper()
	var payload map[string]any
	count := 0
	for _, event := range publisher.events {
		if event.Type == "context.compacted" {
			count++
			payload = event.Payload
		}
	}
	if count != 1 || payload == nil {
		t.Fatalf("context.compacted events = %d, want exactly one", count)
	}
	return payload
}

// TestRunnerPersistsSingleFoldedSegmentAtWindowBoundary pins the explicit fold
// detection of a boundary compaction. With history = keepRecent + 1 and no prior
// summary, folding one transcript segment and appending the rolling summary
// keeps the packet segment count unchanged (one segment out, one in), so a
// count-only heuristic cannot see the fold. The summary is nevertheless part of
// the prompt and must be persisted to the conversation memory and published with
// its summary version.
func TestRunnerPersistsSingleFoldedSegmentAtWindowBoundary(t *testing.T) {
	fixture := newBriefingRunnerFixture(t)
	fixture.settings.ints["context.keep_recent_messages"] = 2
	fixture.conv.snapshot.Summary = ""
	fixture.conv.snapshot.PinnedFacts = nil
	fixture.conv.snapshot.Messages = []agentapp.ConversationMessage{
		{ID: 611, Role: "user", Text: "消息一：需求背景"},
		{ID: 612, Role: "assistant", Text: "消息二：澄清回复"},
		{ID: 613, Role: "user", Text: "消息三：最新补充"},
	}
	fixture.chat.errors = []error{fmt.Errorf("upstream: %w", llmapp.ErrContextOverflow)}
	fixture.chat.responses = []llmapp.ChatResponse{
		{Content: "不应被采用的回复"},
		{Content: "单条折叠摘要"},
		{Content: "恢复后的回复"},
	}

	if _, err := mustRun(t, fixture, hostItem()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(fixture.repo.packets) != 2 {
		t.Fatalf("persisted packets = %d, want prepare + compacted", len(fixture.repo.packets))
	}
	prepared := decodeStoredPacket(t, fixture.repo.packets[0])
	compacted := decodeStoredPacket(t, fixture.repo.packets[1])
	// The bug this pins: the folded segment is replaced by the summary segment,
	// so the two packets have the same number of segments.
	if len(compacted.Segments) != len(prepared.Segments) {
		t.Fatalf("segment counts = %d -> %d, want the foldCount==1 shape (equal counts)",
			len(prepared.Segments), len(compacted.Segments))
	}
	summary := segmentContents(compacted, agentcontext.SegmentSummary)
	if len(summary) != 1 || !strings.Contains(summary[0], "单条折叠摘要") {
		t.Fatalf("summary segments = %#v", summary)
	}
	history := segmentContents(compacted, agentcontext.SegmentHistory)
	if len(history) != 2 || history[0] != "消息二：澄清回复" || history[1] != "消息三：最新补充" {
		t.Fatalf("recent history = %#v, want the 2 newest messages verbatim", history)
	}

	// The summary carried by the prompt must be persisted even though the
	// segment count did not change.
	if len(fixture.conv.summaries) != 1 || !strings.Contains(fixture.conv.summaries[0], "单条折叠摘要") {
		t.Fatalf("conversation summaries = %#v, want the folded summary persisted", fixture.conv.summaries)
	}
	payload := compactedEventPayload(t, fixture.events)
	version, _ := payload["summary_version"].(int)
	if version < 1 {
		t.Fatalf("context.compacted summary_version = %#v, want the persisted summary version", payload["summary_version"])
	}
}

// TestRunnerPersistsSingleFoldedSegmentAtPrepareBoundary covers the proactive
// window_full boundary detected during prepare: a compaction that folds exactly
// one segment must still publish context.compacted and persist its summary even
// though the appended summary keeps the segment count unchanged.
func TestRunnerPersistsSingleFoldedSegmentAtPrepareBoundary(t *testing.T) {
	fixture := newBriefingRunnerFixture(t)
	fixture.settings.ints["context.keep_recent_messages"] = 2
	// Any assembled packet is "over threshold"; the round is a window_full
	// boundary detected during prepare (not by a provider overflow).
	fixture.settings.floats = map[string]float64{"context.compact_threshold": 0.000001}
	fixture.conv.snapshot.Summary = ""
	fixture.conv.snapshot.PinnedFacts = nil
	fixture.conv.snapshot.Messages = []agentapp.ConversationMessage{
		{ID: 621, Role: "user", Text: "消息一：需求背景"},
		{ID: 622, Role: "assistant", Text: "消息二：澄清回复"},
		{ID: 623, Role: "user", Text: "消息三：最新补充"},
	}
	fixture.chat.responses = []llmapp.ChatResponse{
		{Content: "prepare 单条折叠摘要"},
		{Content: "最终回复"},
	}

	if _, err := mustRun(t, fixture, hostItem()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(fixture.repo.packets) != 1 {
		t.Fatalf("persisted packets = %d, want the compacted prepare packet", len(fixture.repo.packets))
	}
	packet := decodeStoredPacket(t, fixture.repo.packets[0])
	summary := segmentContents(packet, agentcontext.SegmentSummary)
	if len(summary) != 1 || !strings.Contains(summary[0], "prepare 单条折叠摘要") {
		t.Fatalf("summary segments = %#v", summary)
	}
	if len(fixture.conv.summaries) != 1 || !strings.Contains(fixture.conv.summaries[0], "prepare 单条折叠摘要") {
		t.Fatalf("conversation summaries = %#v, want the folded summary persisted", fixture.conv.summaries)
	}
	payload := compactedEventPayload(t, fixture.events)
	if version, _ := payload["summary_version"].(int); version < 1 {
		t.Fatalf("context.compacted summary_version = %#v, want the persisted summary version", payload["summary_version"])
	}
}

// TestRunnerSecondOverflowFoldsMoreHistory pins the rescue path of a second
// provider overflow: rebuilding from the same history with the same recent
// window cannot shrink the prompt, so the last retry halves the configured
// window and folds strictly more transcript.
func TestRunnerSecondOverflowFoldsMoreHistory(t *testing.T) {
	fixture := newBriefingRunnerFixture(t)
	fixture.settings.ints["context.keep_recent_messages"] = 2
	fixture.conv.snapshot.Summary = ""
	fixture.conv.snapshot.PinnedFacts = nil
	fixture.conv.snapshot.Messages = []agentapp.ConversationMessage{
		{ID: 631, Role: "user", Text: "消息一：需求背景"},
		{ID: 632, Role: "assistant", Text: "消息二：澄清回复"},
		{ID: 633, Role: "user", Text: "消息三：补充需求"},
		{ID: 634, Role: "assistant", Text: "消息四：最新回复"},
	}
	overflow := fmt.Errorf("upstream: %w", llmapp.ErrContextOverflow)
	fixture.chat.errors = []error{overflow, nil, overflow, nil, nil}
	fixture.chat.responses = []llmapp.ChatResponse{
		{Content: "不应被采用的回复"},
		{Content: "第一次摘要"},
		{Content: "不应被采用的回复"},
		{Content: "第二次摘要"},
		{Content: "恢复后的回复"},
	}

	if _, err := mustRun(t, fixture, hostItem()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(fixture.repo.packets) != 3 {
		t.Fatalf("persisted packets = %d, want prepare + two compacted", len(fixture.repo.packets))
	}
	first := decodeStoredPacket(t, fixture.repo.packets[1])
	second := decodeStoredPacket(t, fixture.repo.packets[2])
	firstHistory := segmentContents(first, agentcontext.SegmentHistory)
	secondHistory := segmentContents(second, agentcontext.SegmentHistory)
	if len(firstHistory) != 2 {
		t.Fatalf("first compacted history = %#v, want the configured window of 2", firstHistory)
	}
	if len(secondHistory) != 1 {
		t.Fatalf("second compacted history = %#v, want the halved window of 1", secondHistory)
	}
	if summary := segmentContents(second, agentcontext.SegmentSummary); len(summary) != 1 ||
		!strings.Contains(summary[0], "第二次摘要") {
		t.Fatalf("second compacted summary = %#v", summary)
	}

	// The two retry prompts are comparable: the second one must be strictly
	// smaller than the first and must no longer replay the additionally folded
	// message.
	if len(fixture.chat.requests) != 5 {
		t.Fatalf("chat requests = %d, want overflow, summary, overflow, summary, final", len(fixture.chat.requests))
	}
	secondRetry := requestPrompt(fixture.chat.request(4))
	if strings.Contains(secondRetry, "消息三：补充需求") {
		t.Fatalf("second retry replayed the folded message:\n%s", secondRetry)
	}
	if !strings.Contains(secondRetry, "消息四：最新回复") || !strings.Contains(secondRetry, "第二次摘要") {
		t.Fatalf("second retry misses the retained recent window/summary:\n%s", secondRetry)
	}
	if len(fixture.chat.request(4).Messages) >= len(fixture.chat.request(2).Messages) {
		t.Fatalf("second retry messages = %d, want fewer than the first retry %d",
			len(fixture.chat.request(4).Messages), len(fixture.chat.request(2).Messages))
	}
}

// designRequirementWorkItem returns a design.requirement work item with the
// given per-work-item identity (work_item_id plus requirement ids). The
// cache-prefix tests run two of them against the same profile, version and
// stage — the shape of the 174 design work items that share one version.
func designRequirementWorkItem(workItemID, requirementID, requirementVersionID int64) agent.WorkItem {
	item := fakeWorkItem(agentapp.WorkTypeDesignRequirement, agentapp.WorkItemPayload{
		ConversationID: 20, RequirementID: requirementID,
		RequirementVersionID: requirementVersionID, Trigger: "manual",
	})
	item.ID = workItemID
	return item
}

// TestPrefixStableAcrossWorkItems pins the Task 80 KV-cache invariant: the
// stable prefix is a function of the profile, the version invariants and the
// stage only. Two different work items of the same profile/version/stage must
// render a byte-identical prefix (and the same PrefixHash) even though their
// task context — work_item_id, requirement ids, goal, constraints, acceptance
// and scope — differs. That per-work-item material must still reach the model
// through the append-only user suffix.
func TestPrefixStableAcrossWorkItems(t *testing.T) {
	fixture := newBriefingRunnerFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{
		{Content: "已为需求 501 提交场景与用例草案。"},
		{Content: "已为需求 502 提交场景与用例草案。"},
	}
	firstItem := designRequirementWorkItem(1001, 501, 700)
	secondItem := designRequirementWorkItem(1002, 502, 701)
	if _, err := mustRun(t, fixture, firstItem); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if _, err := mustRun(t, fixture, secondItem); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(fixture.chat.requests) != 2 {
		t.Fatalf("chat requests = %d, want one per run", len(fixture.chat.requests))
	}

	firstPrefix := fixture.chat.request(0).Messages[0]
	secondPrefix := fixture.chat.request(1).Messages[0]
	if firstPrefix.Role != "system" || secondPrefix.Role != "system" {
		t.Fatalf("first prompt message roles = %q/%q, want system", firstPrefix.Role, secondPrefix.Role)
	}
	if firstPrefix.Content != secondPrefix.Content {
		t.Fatalf("stable prefix changed across work items:\n%s\n---\n%s",
			firstPrefix.Content, secondPrefix.Content)
	}
	for _, unstable := range []string{
		"# 任务", "work_item_id=", "requirement_ids=",
		"requirement_id=501", "requirement_id=502",
	} {
		if strings.Contains(firstPrefix.Content, unstable) {
			t.Fatalf("per-work-item data %q leaked into the stable prefix:\n%s",
				unstable, firstPrefix.Content)
		}
	}
	for _, stable := range []string{
		"[profile:design_author]", "版本目标: 订单系统", "阶段: clarifying",
	} {
		if !strings.Contains(firstPrefix.Content, stable) {
			t.Fatalf("stable prefix is missing %q:\n%s", stable, firstPrefix.Content)
		}
	}

	if len(fixture.repo.packets) != 2 {
		t.Fatalf("persisted packets = %d, want one per run", len(fixture.repo.packets))
	}
	firstPacket := decodeStoredPacket(t, fixture.repo.packets[0])
	secondPacket := decodeStoredPacket(t, fixture.repo.packets[1])
	if firstPacket.PrefixHash != secondPacket.PrefixHash {
		t.Fatalf("PrefixHash changed across work items: %s != %s",
			firstPacket.PrefixHash, secondPacket.PrefixHash)
	}
	if firstPacket.Segments[0].Content != secondPacket.Segments[0].Content {
		t.Fatalf("persisted stable prefixes disagree across work items")
	}

	// The task context still reaches the model: the last user turn differs per
	// work item and carries its own scope/ids, so only the suffix changed.
	firstUser := fixture.chat.request(0).Messages[len(fixture.chat.request(0).Messages)-1]
	secondUser := fixture.chat.request(1).Messages[len(fixture.chat.request(1).Messages)-1]
	if firstUser.Role != "user" || secondUser.Role != "user" {
		t.Fatalf("last prompt message roles = %q/%q, want the current user turn",
			firstUser.Role, secondUser.Role)
	}
	if !strings.Contains(firstUser.Content, "work_item_id=1001") ||
		!strings.Contains(secondUser.Content, "work_item_id=1002") {
		t.Fatalf("per-work-item task context was lost from the user suffix:\n%s\n---\n%s",
			firstUser.Content, secondUser.Content)
	}
	if !strings.Contains(firstUser.Content, "requirement_id=501") ||
		!strings.Contains(secondUser.Content, "requirement_id=502") {
		t.Fatalf("requirement scope was lost from the user suffix:\n%s\n---\n%s",
			firstUser.Content, secondUser.Content)
	}
}

// TestTaskContextInUserSuffix pins the other half of Task 80: moving the task
// context out of the stable prefix must not lose it. The task goal,
// constraints, acceptance criteria and scope — and the P3 execution snapshot —
// must still reach the model through a user-role suffix message, while the
// system prefix keeps only the stable profile/version/stage material.
func TestTaskContextInUserSuffix(t *testing.T) {
	t.Run("design task", func(t *testing.T) {
		fixture := newBriefingRunnerFixture(t)
		fixture.chat.responses = []llmapp.ChatResponse{{Content: "已提交"}}
		if _, err := mustRun(t, fixture, designRequirementWorkItem(1001, 501, 700)); err != nil {
			t.Fatalf("run: %v", err)
		}
		request := fixture.chat.request(0)
		if len(request.Messages) < 2 {
			t.Fatalf("prompt = %#v", request.Messages)
		}
		prefix := request.Messages[0]
		for _, leaked := range []string{
			"# 任务", "# 执行任务", "work_item_id=1001", "requirement_id=501", "作用域:",
		} {
			if strings.Contains(prefix.Content, leaked) {
				t.Fatalf("per-work-item data %q leaked into the stable prefix:\n%s",
					leaked, prefix.Content)
			}
		}

		userContent := ""
		for _, message := range request.Messages[1:] {
			if message.Role == "user" && strings.Contains(message.Content, "# 任务") {
				userContent = message.Content
			}
		}
		if userContent == "" {
			t.Fatalf("task context block is missing from the user suffix: %#v", request.Messages)
		}
		for _, want := range []string{
			"类型: design.requirement",
			"目标: ", "约束:", "验收:", "作用域:",
			"work_item_id=1001", "requirement_id=501",
			"请为需求 501（需求版本 700）设计场景与用例草案。",
		} {
			if !strings.Contains(userContent, want) {
				t.Fatalf("user suffix misses task material %q:\n%s", want, userContent)
			}
		}

		packet := decodeStoredPacket(t, fixture.repo.packets[0])
		if strings.Contains(packet.Segments[0].Content, "# 任务") {
			t.Fatalf("persisted stable prefix carries the task block:\n%s", packet.Segments[0].Content)
		}
	})

	t.Run("execution task", func(t *testing.T) {
		provider := &fakeTaskProvider{task: executionTaskFixture()}
		fixture := newRunnerFixtureWithTaskProvider(t, provider)
		fixture.chat.responses = []llmapp.ChatResponse{{Content: "用例执行完成"}}
		if _, err := mustRun(t, fixture, p3CaseItem()); err != nil {
			t.Fatalf("run: %v", err)
		}
		request := fixture.chat.request(0)
		prefix := request.Messages[0]
		for _, leaked := range []string{
			"# 任务", "# 执行任务", "case_run_id=3001", "case_version_id=41", "步骤:",
		} {
			if strings.Contains(prefix.Content, leaked) {
				t.Fatalf("per-work-item data %q leaked into the stable prefix:\n%s",
					leaked, prefix.Content)
			}
		}

		userContent := ""
		for _, message := range request.Messages[1:] {
			if message.Role == "user" && strings.Contains(message.Content, "# 执行任务") {
				userContent = message.Content
			}
		}
		if userContent == "" {
			t.Fatalf("execution task block is missing from the user suffix: %#v", request.Messages)
		}
		for _, want := range []string{
			"# 任务", "# 执行任务", "登录流程",
			"打开登录页", "Login 按钮可见", "请逐步执行用例并记录结果",
		} {
			if !strings.Contains(userContent, want) {
				t.Fatalf("user suffix misses execution task material %q:\n%s", want, userContent)
			}
		}
	})
}

// TestTaskContextSurvivesWindowFull pins the Task 80 overflow survival
// invariant: the current user turn (task block + trigger) is stored once in
// state.userContent and handleOverflow re-appends that same turn after the
// compaction rebuild. A retry prompt that lost the task block, or re-rendered a
// different one, would leave the model without its goal/constraints/scope — and
// a retry that re-added the block while the fallback system text also carried
// it would send the block twice.
func TestTaskContextSurvivesWindowFull(t *testing.T) {
	t.Run("design task", func(t *testing.T) {
		fixture := newBriefingRunnerFixture(t)
		fixture.settings.ints["context.keep_recent_messages"] = 1
		fixture.conv.snapshot.Summary = ""
		fixture.conv.snapshot.PinnedFacts = nil
		fixture.conv.snapshot.Messages = []agentapp.ConversationMessage{
			{ID: 701, Role: "user", Text: "消息一：需求背景"},
			{ID: 702, Role: "assistant", Text: "消息二：澄清回复"},
			{ID: 703, Role: "user", Text: "消息三：最新回复"},
		}
		fixture.chat.errors = []error{fmt.Errorf("upstream: %w", llmapp.ErrContextOverflow)}
		fixture.chat.responses = []llmapp.ChatResponse{
			{Content: "不应被采用的回复"},
			{Content: "窗口压缩摘要"},
			{Content: "恢复后的回复"},
		}

		if _, err := mustRun(t, fixture, designRequirementWorkItem(1001, 501, 700)); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(fixture.repo.packets) != 2 {
			t.Fatalf("persisted packets = %d, want prepare + window_full rebuild", len(fixture.repo.packets))
		}
		compacted := decodeStoredPacket(t, fixture.repo.packets[1])
		if compacted.InvalidationReason != string(agentcontext.InvalidationWindowFull) {
			t.Fatalf("invalidation reason = %q, want window_full", compacted.InvalidationReason)
		}
		if len(fixture.chat.requests) != 3 {
			t.Fatalf("chat requests = %d, want overflow, summary, retry", len(fixture.chat.requests))
		}
		retry := fixture.chat.request(2)
		last := retry.Messages[len(retry.Messages)-1]
		if last.Role != "user" {
			t.Fatalf("retry last message role = %q, want the re-appended user turn", last.Role)
		}
		for _, want := range []string{
			"# 任务", "目标: ", "约束:", "验收:", "作用域:",
			"work_item_id=1001", "requirement_id=501",
			"请为需求 501（需求版本 700）设计场景与用例草案。",
		} {
			if !strings.Contains(last.Content, want) {
				t.Fatalf("window_full retry lost task material %q:\n%s", want, last.Content)
			}
		}
		if got := strings.Count(requestPrompt(retry), "# 任务"); got != 1 {
			t.Fatalf("task block occurrences in the window_full retry = %d, want exactly 1:\n%s",
				got, requestPrompt(retry))
		}
	})

	t.Run("execution task", func(t *testing.T) {
		provider := &fakeTaskProvider{task: executionTaskFixture()}
		fixture := newRunnerFixtureWithTaskProvider(t, provider)
		fixture.chat.errors = []error{fmt.Errorf("upstream: %w", llmapp.ErrContextOverflow)}
		fixture.chat.responses = []llmapp.ChatResponse{{Content: "恢复后的回复"}}

		if _, err := mustRun(t, fixture, p3CaseItem()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(fixture.repo.packets) != 2 {
			t.Fatalf("persisted packets = %d, want prepare + window_full rebuild", len(fixture.repo.packets))
		}
		compacted := decodeStoredPacket(t, fixture.repo.packets[1])
		if compacted.InvalidationReason != string(agentcontext.InvalidationWindowFull) {
			t.Fatalf("invalidation reason = %q, want window_full", compacted.InvalidationReason)
		}
		if len(fixture.chat.requests) != 2 {
			t.Fatalf("chat requests = %d, want overflow, retry", len(fixture.chat.requests))
		}
		retry := fixture.chat.request(1)
		last := retry.Messages[len(retry.Messages)-1]
		if last.Role != "user" {
			t.Fatalf("retry last message role = %q, want the re-appended user turn", last.Role)
		}
		for _, want := range []string{
			"# 任务", "# 执行任务", "目标: ", "约束:", "验收:", "作用域:",
			"case_run_id=3001", "登录流程", "打开登录页", "Login 按钮可见",
			"请逐步执行用例并记录结果",
		} {
			if !strings.Contains(last.Content, want) {
				t.Fatalf("window_full retry lost execution material %q:\n%s", want, last.Content)
			}
		}
		if got := strings.Count(requestPrompt(retry), "# 任务"); got != 1 {
			t.Fatalf("task block occurrences in the window_full retry = %d, want exactly 1:\n%s",
				got, requestPrompt(retry))
		}
	})
}

// TestTaskContextCountsTowardWindowBudget pins the Task 80 budget accounting:
// the per-work-item task block moved out of the assembler-counted System
// segment into the pending user turn that the runner appends after assembly.
// The window_full threshold must count that turn; otherwise a run whose packet
// fits the threshold but whose user turn pushes the prompt over the window
// would never trigger the proactive boundary. The probe run measures the real
// packet and user-turn token estimates, then the second fixture sets a
// threshold that only the packet + user-turn sum exceeds.
func TestTaskContextCountsTowardWindowBudget(t *testing.T) {
	item := designRequirementWorkItem(1001, 501, 700)

	probe := newBriefingRunnerFixture(t)
	probe.chat.responses = []llmapp.ChatResponse{{Content: "已提交"}}
	if _, err := mustRun(t, probe, item); err != nil {
		t.Fatalf("probe run: %v", err)
	}
	if len(probe.repo.packets) != 1 {
		t.Fatalf("probe persisted packets = %d, want one uncompacted packet", len(probe.repo.packets))
	}
	probePacket := decodeStoredPacket(t, probe.repo.packets[0])
	packetTokens := 0
	for _, segment := range probePacket.Segments {
		packetTokens += segment.Tokens
	}
	probeRequest := probe.chat.request(0)
	userContent := probeRequest.Messages[len(probeRequest.Messages)-1].Content
	if !strings.Contains(userContent, "# 任务") {
		t.Fatalf("probe last message is not the task-bearing user turn:\n%s", userContent)
	}
	userTokens := agentapp.EstimateTokens(userContent)
	if packetTokens <= 0 || userTokens <= tokenOverheadProbe {
		t.Fatalf("unexpected probe estimates: packet=%d user=%d", packetTokens, userTokens)
	}

	// Budget resolution for the fixture model: window 64000 - output reserve
	// 8000 - tool reserve 2048. A threshold just below (packet+user)/budget
	// triggers only when the user turn is counted, and the packet alone stays
	// below it.
	const budget = 64000 - 8000 - 2048
	threshold := float64(packetTokens+userTokens-1) / float64(budget)
	fixture := newBriefingRunnerFixture(t)
	fixture.settings.floats = map[string]float64{"context.compact_threshold": threshold}
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "已提交"}}
	if _, err := mustRun(t, fixture, item); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(fixture.repo.packets) != 1 {
		t.Fatalf("persisted packets = %d, want the proactively compacted prepare packet", len(fixture.repo.packets))
	}
	packet := decodeStoredPacket(t, fixture.repo.packets[0])
	if packet.InvalidationReason != string(agentcontext.InvalidationWindowFull) {
		t.Fatalf("invalidation reason = %q, want a proactive window_full boundary (packet %d + user %d > threshold %v×%d)",
			packet.InvalidationReason, packetTokens, userTokens, threshold, budget)
	}
	// The block is still in the user turn; budget accounting must not move it.
	last := fixture.chat.request(0).Messages[len(fixture.chat.request(0).Messages)-1]
	if !strings.Contains(last.Content, "# 任务") || !strings.Contains(last.Content, "work_item_id=1001") {
		t.Fatalf("task block lost from the budgeted user turn:\n%s", last.Content)
	}
}

// tokenOverheadProbe is agentapp.EstimateTokens("") — the fixed estimator
// overhead, used to prove the probe measured real user-turn content.
const tokenOverheadProbe = 16
