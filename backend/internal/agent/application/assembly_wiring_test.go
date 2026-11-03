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
