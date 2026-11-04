package agentcontext_test

import (
	"context"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
)

// baseRequest is the fixed prefix plus a representative dynamic suffix. The
// prefix inputs (system/invariants/stage) are the only fields that may affect
// PrefixHash; every other field is append-only dynamic material.
func baseRequest() agentcontext.AssemblyRequest {
	return agentcontext.AssemblyRequest{
		System: "# 角色\n测试助手：负责版本测试执行",
		Invariants: []string{
			"目标：验证优惠券结算",
			"验收：订单总额按折扣减少",
			"已确认需求：优惠券可抵扣订单总额",
		},
		Stage: "clarifying",
		Briefing: strings.Join([]string{
			"## 目标/阶段",
			"目标：验证优惠券结算",
			"阶段：clarifying",
		}, "\n"),
		Facts: []string{"[api] auth：鉴权接口可用"},
		History: []agentcontext.Message{
			{ID: 11, Role: "user", Content: "请开始验证"},
			{ID: 12, Role: "assistant", Content: "我会先检查登录"},
		},
	}
}

func assertSHA256Hex(t *testing.T, name, value string) {
	t.Helper()
	if len(value) != 64 {
		t.Fatalf("%s = %q (len %d), want a 64-character lowercase sha256 hex", name, value, len(value))
	}
	if strings.ToLower(value) != value {
		t.Fatalf("%s = %q, want lowercase hex", name, value)
	}
	if _, err := hex.DecodeString(value); err != nil {
		t.Fatalf("%s = %q, want a valid hex digest: %v", name, value, err)
	}
}

func prefixSegments(asm agentcontext.Assembly) []agentcontext.Segment {
	segments := make([]agentcontext.Segment, 0, 1)
	for _, segment := range asm.Packet.Segments {
		if segment.Prefix {
			segments = append(segments, segment)
		}
	}
	return segments
}

func suffixSegments(asm agentcontext.Assembly) []agentcontext.Segment {
	segments := make([]agentcontext.Segment, 0, len(asm.Packet.Segments))
	for _, segment := range asm.Packet.Segments {
		if !segment.Prefix {
			segments = append(segments, segment)
		}
	}
	return segments
}

func suffixText(asm agentcontext.Assembly) string {
	var builder strings.Builder
	for _, segment := range suffixSegments(asm) {
		builder.WriteString(segment.Content)
		builder.WriteString("\n")
	}
	return builder.String()
}

func prefixContent(t *testing.T, asm agentcontext.Assembly) string {
	t.Helper()
	segments := prefixSegments(asm)
	if len(segments) != 1 {
		t.Fatalf("prefix segments = %d, want exactly 1", len(segments))
	}
	if segments[0].Role != "system" {
		t.Fatalf("prefix role = %q, want system", segments[0].Role)
	}
	if len(asm.Messages) == 0 || asm.Messages[0].Content != segments[0].Content {
		t.Fatalf("first assembled message does not match the prefix segment")
	}
	return segments[0].Content
}

// assembledHistory returns the history messages reconstructed from the packet
// segments and checks the message/segment projection stays 1:1.
func assembledHistory(t *testing.T, asm agentcontext.Assembly) []agentcontext.Message {
	t.Helper()
	if len(asm.Messages) != len(asm.Packet.Segments) {
		t.Fatalf("messages = %d, segments = %d, want a 1:1 projection", len(asm.Messages), len(asm.Packet.Segments))
	}
	history := make([]agentcontext.Message, 0, len(asm.Messages))
	for index, segment := range asm.Packet.Segments {
		message := asm.Messages[index]
		if message.Role != segment.Role || message.Content != segment.Content || message.ToolCallID != segment.ToolCallID {
			t.Fatalf("segment %d %#v does not match message %#v", index, segment, message)
		}
		if segment.Name == agentcontext.SegmentHistory {
			history = append(history, message)
		}
	}
	return history
}

// TestPrefixStableAcrossDynamics is Review Focus 4: changing only the briefing,
// facts, history or tool results must leave the cached stable prefix untouched
// and grow only the append-only suffix.
func TestPrefixStableAcrossDynamics(t *testing.T) {
	assembler := agentcontext.NewAssembler()
	first, err := assembler.Build(context.Background(), baseRequest())
	if err != nil {
		t.Fatalf("first Build: %v", err)
	}
	if first.Packet.CacheHit {
		t.Fatalf("first build reports a cache hit; there is no previous prefix")
	}
	if first.Packet.CachedTokens != 0 {
		t.Fatalf("first build CachedTokens = %d, want 0", first.Packet.CachedTokens)
	}
	if first.Packet.InvalidationReason != "" {
		t.Fatalf("first build InvalidationReason = %q, want empty", first.Packet.InvalidationReason)
	}
	assertSHA256Hex(t, "PrefixHash", first.Packet.PrefixHash)
	assertSHA256Hex(t, "BriefingHash", first.Packet.BriefingHash)

	prefix := prefixContent(t, first)
	for _, want := range []string{
		agentcontext.PlaceholderLatestSituation,
		agentcontext.PlaceholderRecentActivity,
		"阶段: clarifying",
		"目标：验证优惠券结算",
	} {
		if !strings.Contains(prefix, want) {
			t.Fatalf("stable prefix missing %q:\n%s", want, prefix)
		}
	}
	for _, leaked := range []string{"## 目标/阶段", "[api] auth", "请开始验证"} {
		if strings.Contains(prefix, leaked) {
			t.Fatalf("dynamic content %q leaked into the stable prefix:\n%s", leaked, prefix)
		}
	}

	firstSuffix := suffixSegments(first)
	if len(firstSuffix) == 0 {
		t.Fatalf("first build has an empty append region")
	}

	secondRequest := baseRequest()
	secondRequest.Briefing = "## 目标/阶段\n目标：验证优惠券结算\n阶段：clarifying\n\n## 执行\n最近 wave：done"
	secondRequest.Facts = append(secondRequest.Facts, "[page] login：登录页可达")
	secondRequest.History = append(secondRequest.History,
		agentcontext.Message{ID: 13, Role: "assistant", Content: "开始执行用例"})
	secondRequest.ToolResults = []agentcontext.ToolResult{
		{CallID: "call-1", Name: "browser", Content: "navigation done", Ref: "tool-result:1"},
	}
	secondRequest.Previous = &first.Packet

	second, err := assembler.Build(context.Background(), secondRequest)
	if err != nil {
		t.Fatalf("second Build: %v", err)
	}
	if second.Packet.PrefixHash != first.Packet.PrefixHash {
		t.Fatalf("PrefixHash changed on a dynamic-only update: %s -> %s",
			first.Packet.PrefixHash, second.Packet.PrefixHash)
	}
	if !second.Packet.CacheHit {
		t.Fatalf("second build with an unchanged prefix did not report a cache hit")
	}
	if second.Packet.InvalidationReason != "" {
		t.Fatalf("dynamic-only update InvalidationReason = %q, want empty", second.Packet.InvalidationReason)
	}
	if second.Packet.CachedTokens <= 0 {
		t.Fatalf("cache-hit CachedTokens = %d, want a positive stable-prefix estimate", second.Packet.CachedTokens)
	}
	if second.Packet.BriefingHash == first.Packet.BriefingHash {
		t.Fatalf("BriefingHash did not change with the briefing")
	}
	if got, base := len(suffixSegments(second)), len(firstSuffix); got <= base {
		t.Fatalf("append region did not grow: %d -> %d segments", base, got)
	}
	if got := prefixContent(t, second); got != prefix {
		t.Fatalf("stable prefix was rewritten on a dynamic-only update:\n%s\n---\n%s", prefix, got)
	}
	added := suffixText(second)
	for _, want := range []string{"[page] login：登录页可达", "开始执行用例", "navigation done"} {
		if !strings.Contains(added, want) {
			t.Fatalf("append region missing %q:\n%s", want, added)
		}
	}
}

// TestPrefixChangesOnlyAtBoundary pins the invalidation contract: the prefix
// may only be rebuilt at a declared boundary, and a stage transition must
// report stage_boundary; an undeclared rewrite is rejected.
func TestPrefixChangesOnlyAtBoundary(t *testing.T) {
	assembler := agentcontext.NewAssembler()
	first, err := assembler.Build(context.Background(), baseRequest())
	if err != nil {
		t.Fatalf("first Build: %v", err)
	}

	transition := baseRequest()
	transition.Stage = "designing"
	transition.Invalidation = agentcontext.InvalidationStageBoundary
	transition.Previous = &first.Packet
	second, err := assembler.Build(context.Background(), transition)
	if err != nil {
		t.Fatalf("stage-boundary Build: %v", err)
	}
	if second.Packet.PrefixHash == first.Packet.PrefixHash {
		t.Fatalf("PrefixHash did not change on a stage transition")
	}
	if second.Packet.InvalidationReason != "stage_boundary" {
		t.Fatalf("InvalidationReason = %q, want stage_boundary", second.Packet.InvalidationReason)
	}
	if second.Packet.CacheHit {
		t.Fatalf("stage-boundary build reported a cache hit")
	}
	if second.Packet.CachedTokens != 0 {
		t.Fatalf("stage-boundary CachedTokens = %d, want 0", second.Packet.CachedTokens)
	}
	if prefix := prefixContent(t, second); !strings.Contains(prefix, "阶段: designing") {
		t.Fatalf("stage-boundary prefix does not carry the new stage:\n%s", prefix)
	}

	// A prefix rewrite without a declared boundary is a cache-correctness bug:
	// fail closed instead of silently invalidating the cache.
	undeclared := baseRequest()
	undeclared.Invariants = append(undeclared.Invariants, "边界：新增已确认需求")
	undeclared.Previous = &first.Packet
	if _, err := assembler.Build(context.Background(), undeclared); !errors.Is(err, agentcontext.ErrPrefixChanged) {
		t.Fatalf("undeclared prefix change error = %v, want ErrPrefixChanged", err)
	}

	// Unknown boundary values are rejected instead of being written into the
	// packet as a made-up invalidation reason.
	unknown := baseRequest()
	unknown.Invalidation = agentcontext.Invalidation("stage_slam")
	if _, err := assembler.Build(context.Background(), unknown); err == nil {
		t.Fatalf("unknown invalidation was accepted")
	}

	// A new prompt chain has no previous prefix to rewrite, so no boundary is
	// required.
	fresh := baseRequest()
	fresh.Stage = "executing"
	fresh.Invariants = append(fresh.Invariants, "边界：新会话")
	if _, err := assembler.Build(context.Background(), fresh); err != nil {
		t.Fatalf("fresh chain Build: %v", err)
	}
}

// TestNoHistoryRewrite proves a second Build never rewrites or reorders the
// already-appended transcript: the prefix bytes and every historical message
// stay identical.
func TestNoHistoryRewrite(t *testing.T) {
	assembler := agentcontext.NewAssembler()
	firstRequest := baseRequest()
	firstRequest.History = append(firstRequest.History,
		agentcontext.Message{ID: 13, Role: "tool", Content: "工具原始结果 A", ToolCallID: "call-7"},
	)
	first, err := assembler.Build(context.Background(), firstRequest)
	if err != nil {
		t.Fatalf("first Build: %v", err)
	}

	secondRequest := baseRequest()
	secondRequest.Briefing = "## 目标/阶段\n目标：第二轮简报"
	secondRequest.Facts = append(secondRequest.Facts, "[risk] coupon：过期券未覆盖")
	secondRequest.History = append(firstRequest.History,
		agentcontext.Message{ID: 14, Role: "assistant", Content: "第二轮回复"},
		agentcontext.Message{ID: 15, Role: "tool", Content: "工具原始结果 B", ToolCallID: "call-8"},
	)
	secondRequest.Previous = &first.Packet
	second, err := assembler.Build(context.Background(), secondRequest)
	if err != nil {
		t.Fatalf("second Build: %v", err)
	}

	if firstPrefix, secondPrefix := prefixSegments(first), prefixSegments(second); !reflect.DeepEqual(firstPrefix, secondPrefix) {
		t.Fatalf("prefix segments changed between builds:\n%#v\n%#v", firstPrefix, secondPrefix)
	}
	if first.Messages[0] != second.Messages[0] {
		t.Fatalf("first prefix message changed between builds:\n%#v\n%#v", first.Messages[0], second.Messages[0])
	}
	if got := assembledHistory(t, first); !reflect.DeepEqual(got, firstRequest.History) {
		t.Fatalf("first build rewrote history:\n%#v\nwant\n%#v", got, firstRequest.History)
	}
	if got := assembledHistory(t, second); !reflect.DeepEqual(got, secondRequest.History) {
		t.Fatalf("second build rewrote history:\n%#v\nwant\n%#v", got, secondRequest.History)
	}
	// The first build's whole history must still be a byte-identical prefix of
	// the second build's history.
	if got, want := assembledHistory(t, second)[:len(firstRequest.History)], firstRequest.History; !reflect.DeepEqual(got, want) {
		t.Fatalf("second build reordered/rewrote the earlier history:\n%#v\nwant\n%#v", got, want)
	}
}

// TestPrefixToolResultTruncation pins the large tool-result rule: >8KB is
// replaced by the byte head plus a lazy-load reference, never appended whole.
func TestPrefixToolResultTruncation(t *testing.T) {
	assembler := agentcontext.NewAssembler()
	largeContent := strings.Repeat("A", agentcontext.MaxToolResultBytes+700)
	request := baseRequest()
	request.ToolResults = []agentcontext.ToolResult{
		{CallID: "call-small", Name: "read", Content: "small result", Ref: "tool-result:small"},
		{CallID: "call-large", Name: "read", Content: largeContent, Ref: "tool-result:42"},
	}
	assembly, err := assembler.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var toolMessages []agentcontext.Message
	for index, segment := range assembly.Packet.Segments {
		if segment.Name == agentcontext.SegmentToolResult {
			if segment.Role != "tool" {
				t.Fatalf("tool result segment role = %q, want tool", segment.Role)
			}
			toolMessages = append(toolMessages, assembly.Messages[index])
		}
	}
	if len(toolMessages) != 2 {
		t.Fatalf("tool messages = %d, want 2", len(toolMessages))
	}
	if toolMessages[0].Content != "small result" {
		t.Fatalf("small tool result was altered: %q", toolMessages[0].Content)
	}
	if toolMessages[0].ToolCallID != "call-small" {
		t.Fatalf("small tool ToolCallID = %q", toolMessages[0].ToolCallID)
	}
	truncated := toolMessages[1].Content
	if truncated == largeContent {
		t.Fatalf("oversized tool result was appended whole")
	}
	if !strings.HasPrefix(truncated, strings.Repeat("A", agentcontext.MaxToolResultBytes)) {
		t.Fatalf("truncated tool result does not keep the byte head")
	}
	if !strings.Contains(truncated, "tool-result:42") {
		t.Fatalf("truncated tool result is missing its lazy-load ref:\n%s", truncated)
	}
	if !strings.Contains(truncated, "700") {
		t.Fatalf("truncated tool result does not report the omitted byte count:\n%s", truncated)
	}
	if len(truncated) >= len(largeContent) {
		t.Fatalf("truncated tool result is not smaller than the original")
	}
	if !utf8.ValidString(truncated) {
		t.Fatalf("truncated tool result is not valid UTF-8")
	}

	// Cutting at the byte limit must not split a multi-byte rune.
	multibyte := strings.Repeat("测试结果", 1000)
	multibyteRequest := baseRequest()
	multibyteRequest.ToolResults = []agentcontext.ToolResult{{CallID: "call-zh", Content: multibyte}}
	multibyteAssembly, err := assembler.Build(context.Background(), multibyteRequest)
	if err != nil {
		t.Fatalf("multibyte Build: %v", err)
	}
	multibyteMessages := make([]agentcontext.Message, 0, 1)
	for index, segment := range multibyteAssembly.Packet.Segments {
		if segment.Name == agentcontext.SegmentToolResult {
			multibyteMessages = append(multibyteMessages, multibyteAssembly.Messages[index])
		}
	}
	if len(multibyteMessages) != 1 {
		t.Fatalf("multibyte tool messages = %d, want 1", len(multibyteMessages))
	}
	if !utf8.ValidString(multibyteMessages[0].Content) {
		t.Fatalf("multi-byte truncation split a rune: %q", multibyteMessages[0].Content)
	}
	// A caller that supplies no ref still gets a deterministic handle naming
	// the tool result, so lazy loading stays addressable.
	if !strings.Contains(multibyteMessages[0].Content, "call-zh") {
		t.Fatalf("fallback lazy-load ref does not name the call id:\n%s", multibyteMessages[0].Content)
	}
}

// TestPrefixEmptyRequestCoversZeroValue keeps the assembler usable with
// minimal input: the stable prefix (including both placeholders) always exists.
func TestPrefixEmptyRequestCoversZeroValue(t *testing.T) {
	assembler := agentcontext.NewAssembler()
	assembly, err := assembler.Build(context.Background(), agentcontext.AssemblyRequest{})
	if err != nil {
		t.Fatalf("zero-value Build: %v", err)
	}
	prefix := prefixContent(t, assembly)
	for _, want := range []string{
		agentcontext.PlaceholderLatestSituation,
		agentcontext.PlaceholderRecentActivity,
		"阶段: 未知",
	} {
		if !strings.Contains(prefix, want) {
			t.Fatalf("zero-value prefix missing %q:\n%s", want, prefix)
		}
	}
	assertSHA256Hex(t, "PrefixHash", assembly.Packet.PrefixHash)
	assertSHA256Hex(t, "BriefingHash", assembly.Packet.BriefingHash)
	if len(suffixSegments(assembly)) != 0 {
		t.Fatalf("zero-value request has %d suffix segments, want 0", len(suffixSegments(assembly)))
	}
}
