package agentapp_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

func TestLLMSummarizerBuildsPromptAndTrimsOutput(t *testing.T) {
	chat := &scriptedChat{responses: []llmapp.ChatResponse{{Content: "  用户要登录功能，未决：登录方式。  "}}}
	summarizer := agentapp.NewLLMSummarizer(chat)

	summary, err := summarizer.Summarize(context.Background(), []agentapp.Message{
		{ID: 1, Role: "user", Content: "我要一个登录功能"},
		{ID: 2, Role: "assistant", Content: "请问登录方式？"},
	})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if summary != "用户要登录功能，未决：登录方式。" {
		t.Fatalf("summary = %q", summary)
	}
	request := chat.request(0)
	if len(request.Messages) != 2 || request.Messages[0].Role != "system" || request.Messages[1].Role != "user" {
		t.Fatalf("messages = %#v", request.Messages)
	}
	if !strings.Contains(request.Messages[1].Content, "[user] 我要一个登录功能") {
		t.Fatalf("transcript = %q", request.Messages[1].Content)
	}
	if request.MaxOutputTokens <= 0 {
		t.Fatalf("max output tokens = %d", request.MaxOutputTokens)
	}
}

func TestLLMSummarizerReturnsEmptyForNoMessagesWithoutCallingChat(t *testing.T) {
	chat := &scriptedChat{}
	summary, err := agentapp.NewLLMSummarizer(chat).Summarize(context.Background(), nil)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if summary != "" {
		t.Fatalf("summary = %q", summary)
	}
	if chat.requestCount() != 0 {
		t.Fatalf("chat calls = %d", chat.requestCount())
	}
}

func TestLLMSummarizerTruncatesLongOutput(t *testing.T) {
	long := strings.Repeat("字", 3000)
	chat := &scriptedChat{responses: []llmapp.ChatResponse{{Content: long}}}
	summary, err := agentapp.NewLLMSummarizer(chat).Summarize(context.Background(), []agentapp.Message{{ID: 1, Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if runes := []rune(summary); len(runes) > 2000 {
		t.Fatalf("summary length = %d, want <= 2000", len(runes))
	}
}

func TestLLMSummarizerPropagatesGatewayFailure(t *testing.T) {
	chat := &scriptedChat{errors: []error{errors.New("model down")}}
	_, err := agentapp.NewLLMSummarizer(chat).Summarize(context.Background(), []agentapp.Message{{ID: 1, Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestLLMSummarizerWithoutGatewayFails(t *testing.T) {
	_, err := agentapp.NewLLMSummarizer(nil).Summarize(context.Background(), []agentapp.Message{{ID: 1, Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected an error")
	}
}

// summaryPolicy is the reference R-A5-1 policy used by the summary fixtures
// (128K window / 8K output: summary_max = 4800).
func summaryPolicy() agentcontext.CompactionPolicy {
	return agentcontext.NewCompactionPolicy(128000, 8000)
}

// testSummaryInput is the rich fixture of the summary tests. It carries every
// material the nine-section builder must account for: pinned task context,
// requirement/case ids, plan and fact snapshots, pinned facts, a transcript with
// exact ids/paths/commands and a stable prefix with tools for R-A5-8.
func testSummaryInput() agentapp.SummaryInput {
	confidence := 0.9
	return agentapp.SummaryInput{
		ConversationID: 20,
		VersionID:      10,
		CompactionID:   "compaction-20-1700000000-1",
		Trigger:        agentcontext.Pressure,
		StablePrefix:   "[profile:clarification_host] 稳定前缀\n\n# 版本不变量\n- 版本目标: 订单系统",
		Tools: []llmapp.ToolDefinition{{
			Name:        "plan.update",
			Description: "更新计划项",
			Parameters:  []byte(`{"type":"object"}`),
		}},
		Task: agentapp.TaskContext{
			Type:               "design.requirement",
			Goal:               "实现登录功能",
			Constraints:        []string{"不得修改已确认需求"},
			AcceptanceCriteria: []string{"登录成功返回 200", "错误密码返回 401"},
			Scope:              []string{"work_item_id=42", "requirement_id=501"},
		},
		WorkItemID:     42,
		RequirementIDs: []int64{501},
		CaseIDs:        []int64{7001},
		Messages: []agentapp.SummaryReplayMessage{
			{ID: 1, Role: "user", Content: "早前：请实现 /api/login，登录代码在 src/auth/login.go，先阅读并运行 go test ./internal/auth。"},
			{
				ID: 2, Role: "assistant", Content: "已读取 src/auth/login.go。",
				ReasoningContent: "先阅读登录实现",
				ToolCalls:        []llmapp.ToolCall{{ID: "call-1", Name: "file.read", Arguments: `{"path":"src/auth/login.go"}`}},
			},
			{ID: 3, Role: "tool", ToolCallID: "call-1", Content: "file.read 返回 package auth\nfunc Login(ctx context.Context) error\n运行 go test ./internal/auth 通过，耗时 0.42s"},
			{ID: 4, Role: "user", Content: "请实现 /api/login；错误码 FR-001。"},
		},
		Plan: []agentcontext.PlanItemSnapshot{
			{ID: 7, Title: "实现登录接口", Status: "in_progress", DependsOn: []string{"6"}},
		},
		Facts: []agentcontext.FactSnapshot{
			{ID: 9, Kind: "decision", Key: "auth.method", Summary: "使用 JWT", Confidence: &confidence},
		},
		PinnedFacts:      []string{"JWT 有效期 15 分钟"},
		CoveredMessageID: 4,
		Policy:           summaryPolicy(),
	}
}

// TestSummaryNineSections pins the R-A5-7 contract: the summary is exactly the
// nine structured Chinese sections from the spec, in order, even when some
// sections carry no material.
func TestSummaryNineSections(t *testing.T) {
	out := agentapp.BuildSummary(testSummaryInput())
	for _, heading := range []string{
		"## 用户目标与意图", "## 关键概念与约束", "## 需求 / 用例 / 产物",
		"## 文件与代码", "## 错误与修复", "## 待办作业", "## 当前进展",
		"## 下一步", "## 关键上下文",
	} {
		if !strings.Contains(out, heading) {
			t.Fatalf("missing %q in:\n%s", heading, out)
		}
		if count := strings.Count(out, heading); count != 1 {
			t.Fatalf("heading %q occurs %d times, want once:\n%s", heading, count, out)
		}
	}
	for _, heading := range agentapp.SummarySectionHeadings() {
		if !strings.Contains(out, "## "+heading) {
			t.Fatalf("exported heading %q is missing from the render", heading)
		}
	}
}

// TestSummaryEmptySectionsUseNone pins "空段写 (none)，不得删段": an empty input
// still renders all nine headings and every section body is the literal
// placeholder.
func TestSummaryEmptySectionsUseNone(t *testing.T) {
	out := agentapp.BuildSummary(agentapp.SummaryInput{Policy: summaryPolicy()})
	for _, heading := range agentapp.SummarySectionHeadings() {
		if !strings.Contains(out, "## "+heading+"\n(none)") {
			t.Fatalf("empty section %q does not render (none):\n%s", heading, out)
		}
	}
	if count := strings.Count(out, "(none)"); count != 9 {
		t.Fatalf("(none) occurrences = %d, want exactly one per section:\n%s", count, out)
	}
}

// TestSummaryMergesPriorSummaryAndPreservesIdentifiers pins the prior-summary
// merge contract and the exact-ID/path/command preservation rule: a still-true
// prior fact is kept once (no verbatim duplication) and the new range keeps its
// ids, file paths and commands verbatim.
func TestSummaryMergesPriorSummaryAndPreservesIdentifiers(t *testing.T) {
	in := testSummaryInput()
	in.PriorSummary = "## 需求 / 用例 / 产物\n- FR-001 登录需求（已确认）\n\n## 关键上下文\n- 附件 attachment_id=9001 已解析"
	in.Messages = append(in.Messages, agentapp.SummaryReplayMessage{
		ID: 5, Role: "tool",
		Content: "error: timeout after 30s in Login(ctx)",
	})
	out := agentapp.BuildSummary(in)

	for _, want := range []string{
		"FR-001 登录需求（已确认）",
		"attachment_id=9001",
		"/api/login",
		"src/auth/login.go",
		"go test ./internal/auth",
		"error: timeout after 30s in Login(ctx)",
		"30s",
		"Login(ctx)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("merged summary lost %q:\n%s", want, out)
		}
	}
	if count := strings.Count(out, "FR-001 登录需求（已确认）"); count != 1 {
		t.Fatalf("prior fact duplicated %d times, want once:\n%s", count, out)
	}
}

// TestSummaryMaxTruncatesWithFlag pins the summary_max cap: content that does
// not fit is truncated, every section heading survives the truncation and the
// result reports the truncation for the summary_truncated event.
func TestSummaryMaxTruncatesWithFlag(t *testing.T) {
	in := testSummaryInput()
	in.SummaryMax = 240
	confidence := 0.5
	for index := 0; index < 40; index++ {
		in.Facts = append(in.Facts, agentcontext.FactSnapshot{
			ID:         int64(100 + index),
			Kind:       "finding",
			Key:        fmt.Sprintf("key.%02d", index),
			Summary:    strings.Repeat("细节", 20),
			Confidence: &confidence,
		})
	}

	result := agentapp.BuildSummaryResult(in)
	if !result.Truncated {
		t.Fatalf("summary_max=%d did not report a truncation: %+v", in.SummaryMax, result)
	}
	if result.Tokens > in.SummaryMax {
		t.Fatalf("summary tokens = %d, want <= %d", result.Tokens, in.SummaryMax)
	}
	if got := agentapp.EstimateSummaryTokens(result.Text); got > in.SummaryMax {
		t.Fatalf("estimate = %d, want <= %d", got, in.SummaryMax)
	}
	for _, heading := range agentapp.SummarySectionHeadings() {
		if !strings.Contains(result.Text, "## "+heading) {
			t.Fatalf("truncation dropped section %q:\n%s", heading, result.Text)
		}
	}
	if strings.Contains(result.Text, "key.39") {
		t.Fatalf("truncation kept the tail fact that cannot fit:\n%s", result.Text)
	}
}

// TestSummaryRendersPlanAndFactSnapshots pins R-A5-13: the summary's todo,
// progress and next sections reference real plan item ids/statuses and fact
// kind/key/confidence from the snapshots.
func TestSummaryRendersPlanAndFactSnapshots(t *testing.T) {
	confidence := 0.9
	in := testSummaryInput()
	in.Plan = []agentcontext.PlanItemSnapshot{
		{ID: 6, Title: "澄清需求", Status: "done"},
		{ID: 7, Title: "实现登录接口", Status: "in_progress", DependsOn: []string{"6"}},
		{ID: 8, Title: "执行登录用例", Status: "pending", DependsOn: []string{"7"}},
	}
	in.Facts = []agentcontext.FactSnapshot{
		{ID: 9, Kind: "decision", Key: "auth.method", Summary: "使用 JWT", Confidence: &confidence},
	}
	out := agentapp.BuildSummary(in)
	for _, want := range []string{
		"plan_item id=7 status=in_progress", "depends_on=6",
		"plan_item id=8 status=pending",
		"plan_item id=6 status=done",
		"fact kind=decision", "key=auth.method", "confidence=0.90",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("plan/fact snapshot material %q missing:\n%s", want, out)
		}
	}
}

// TestSummaryImagesNeverEnterSummary pins R-A5-5/R-A5-7: an image part is
// replaced by the attachment marker and its bytes never reach the summary.
func TestSummaryImagesNeverEnterSummary(t *testing.T) {
	messages := agentapp.SummaryMessages([]agentapp.ConversationMessage{{
		ID:   1,
		Role: "user",
		Text: "请参考这张截图",
		Parts: []agentapp.ConversationPart{
			{Type: "text", Text: "请参考这张截图"},
			{Type: "image", Data: map[string]any{
				"attachment_id": "9001",
				"mime":          "image/png",
				"data_ref":      "sha256-abc",
				"content":       "BASE64_SECRET_IMAGE_BYTES",
			}},
		},
	}})
	if len(messages) != 1 {
		t.Fatalf("summary messages = %#v", messages)
	}
	if !strings.Contains(messages[0].Content, "[image skipped: attachment_id=9001]") {
		t.Fatalf("image marker missing: %q", messages[0].Content)
	}

	in := testSummaryInput()
	in.Messages = messages
	out := agentapp.BuildSummary(in)
	if strings.Contains(out, "BASE64_SECRET_IMAGE_BYTES") || strings.Contains(out, "sha256-abc") {
		t.Fatalf("image payload leaked into the summary:\n%s", out)
	}
}

// TestSummaryIncrementalWatermark pins the R-A5-12 covered_message_id delta: the
// next summary only folds messages after the watermark, and a watermark never
// moves backwards.
func TestSummaryIncrementalWatermark(t *testing.T) {
	messages := []agentapp.SummaryReplayMessage{
		{ID: 1, Role: "user", Content: "旧消息一"},
		{ID: 5, Role: "assistant", Content: "旧消息五"},
		{ID: 9, Role: "user", Content: "新消息九"},
	}
	selected := agentapp.SelectSummaryMessages(messages, 5)
	if len(selected) != 1 || selected[0].ID != 9 {
		t.Fatalf("selected messages = %#v, want only id 9", selected)
	}
	if all := agentapp.SelectSummaryMessages(messages, 0); len(all) != 3 {
		t.Fatalf("zero watermark selected %d messages, want all", len(all))
	}
	covered, err := agentapp.NewCoveredMessageID(5, 9)
	if err != nil || covered != 9 {
		t.Fatalf("NewCoveredMessageID(5,9) = %d/%v", covered, err)
	}
	if same, err := agentapp.NewCoveredMessageID(9, 9); err != nil || same != 9 {
		t.Fatalf("NewCoveredMessageID(9,9) = %d/%v", same, err)
	}
	if _, err := agentapp.NewCoveredMessageID(9, 4); !errors.Is(err, agentapp.ErrSummaryWatermarkStale) {
		t.Fatalf("backwards watermark error = %v, want ErrSummaryWatermarkStale", err)
	}
}

// TestSummaryLedgerVersionIncrements pins the persistence hook contract the DB
// adapter (Task 36) must implement: every compaction appends one revision, the
// version increments, covered_message_id stays monotonic and summary_meta is
// carried through.
func TestSummaryLedgerVersionIncrements(t *testing.T) {
	ledger := agentapp.NewSummaryLedger()
	ctx := context.Background()

	first, err := ledger.AppendSummary(ctx, agentapp.SummaryCommit{
		VersionID: 10, ConversationID: 20, CompactionID: "c1", Summary: "第一版",
		PinnedFacts: []string{"f1"}, CoveredMessageID: 10,
		SummaryMeta: map[string]any{"truncated": false}, TokenEstimate: 120,
	})
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	if first.Version != 1 || first.CoveredMessageID != 10 || first.CompactionID != "c1" {
		t.Fatalf("first record = %+v", first)
	}

	second, err := ledger.AppendSummary(ctx, agentapp.SummaryCommit{
		VersionID: 10, ConversationID: 20, CompactionID: "c2", Summary: "第二版",
		CoveredMessageID: 20, SummaryMeta: map[string]any{"truncated": true}, TokenEstimate: 90,
	})
	if err != nil {
		t.Fatalf("second append: %v", err)
	}
	if second.Version != 2 || second.CoveredMessageID != 20 {
		t.Fatalf("second record = %+v", second)
	}

	if _, err := ledger.AppendSummary(ctx, agentapp.SummaryCommit{
		VersionID: 10, ConversationID: 20, Summary: "回退水位", CoveredMessageID: 15,
	}); !errors.Is(err, agentapp.ErrSummaryWatermarkStale) {
		t.Fatalf("backwards append error = %v, want ErrSummaryWatermarkStale", err)
	}

	third, err := ledger.AppendSummary(ctx, agentapp.SummaryCommit{
		VersionID: 10, ConversationID: 20, CompactionID: "c3", Summary: "第三版",
		CoveredMessageID: 20,
	})
	if err != nil {
		t.Fatalf("third append: %v", err)
	}
	if third.Version != 3 {
		t.Fatalf("third version = %d, want 3", third.Version)
	}
	latest, ok := ledger.LatestSummary(20)
	if !ok || latest.Version != 3 || latest.Summary != "第三版" {
		t.Fatalf("latest = %+v/%v", latest, ok)
	}
	if latest.SummaryMeta["truncated"] != nil {
		t.Fatalf("third summary_meta = %#v", latest.SummaryMeta)
	}
}

// TestSummaryPrefixReuseRequest pins R-A5-8: the summary request reuses the
// current conversation's stable prefix and tools, replays only the summary
// range after the covered watermark and appends the compaction instruction as
// the last user message.
func TestSummaryPrefixReuseRequest(t *testing.T) {
	in := testSummaryInput()
	in.PreviousCoveredMessageID = 1
	in.CoveredMessageID = 4
	request := agentapp.BuildSummaryRequest(in)
	if len(request.Messages) < 2 {
		t.Fatalf("summary request messages = %#v", request.Messages)
	}
	if request.Messages[0].Role != "system" || request.Messages[0].Content != in.StablePrefix {
		t.Fatalf("summary request does not reuse the stable prefix: %#v", request.Messages[0])
	}
	if len(request.Tools) != 1 || request.Tools[0].Name != "plan.update" {
		t.Fatalf("summary request tools = %#v, want the conversation tools", request.Tools)
	}
	last := request.Messages[len(request.Messages)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "压缩指令") {
		t.Fatalf("last summary message is not the compaction instruction: %#v", last)
	}
	for _, want := range []string{"plan_item id=7", "auth.method", "## 用户目标与意图"} {
		if !strings.Contains(last.Content, want) {
			t.Fatalf("compaction instruction misses %q:\n%s", want, last.Content)
		}
	}
	if request.MaxOutputTokens <= 0 {
		t.Fatalf("max output tokens = %d", request.MaxOutputTokens)
	}

	replayed := false
	for _, message := range request.Messages[1 : len(request.Messages)-1] {
		if strings.Contains(message.Content, "src/auth/login.go") {
			replayed = true
		}
		if strings.Contains(message.Content, "早前：请实现 /api/login") {
			t.Fatalf("summary request replayed a message at/below the covered watermark:\n%s", message.Content)
		}
	}
	if !replayed {
		t.Fatalf("summary request did not replay the summary range: %#v", request.Messages)
	}
}

// fakeSummaryEventPublisher records summary_truncated events.
type fakeSummaryEventPublisher struct {
	events []agentapp.SummaryEvent
}

func (f *fakeSummaryEventPublisher) PublishSummaryEvent(_ context.Context, event agentapp.SummaryEvent) error {
	f.events = append(f.events, event)
	return nil
}

// TestCompactionSummarizerReusesPrefixAndRecordsCachedTokens pins the R-A5-8
// provider contract: the summary call goes through the reused prefix request and
// the provider-reported cached_tokens is returned for the context.compacted
// payload.
func TestCompactionSummarizerReusesPrefixAndRecordsCachedTokens(t *testing.T) {
	chat := &scriptedChat{responses: []llmapp.ChatResponse{{
		Content: "## 用户目标与意图\n实现登录功能。",
		Usage:   map[string]int{"cached_tokens": 640, "cache_hit": 1},
	}}}
	in := testSummaryInput()
	result := agentapp.NewCompactionSummarizer(chat).Summarize(context.Background(), in)
	if result.UsedFallback {
		t.Fatalf("summarizer fell back without a gateway failure: %+v", result)
	}
	if result.CachedTokens != 640 || !result.CacheHit {
		t.Fatalf("cached tokens = %d/%v, want the provider 640/true", result.CachedTokens, result.CacheHit)
	}
	if result.CoveredMessageID != in.CoveredMessageID || result.CompactionID != in.CompactionID {
		t.Fatalf("summary identity = %+v", result)
	}
	for _, heading := range agentapp.SummarySectionHeadings() {
		if !strings.Contains(result.Text, "## "+heading) {
			t.Fatalf("summary normalisation dropped %q:\n%s", heading, result.Text)
		}
	}
	if chat.requestCount() != 1 {
		t.Fatalf("chat requests = %d, want one summary request", chat.requestCount())
	}
	request := chat.request(0)
	if request.Messages[0].Content != in.StablePrefix {
		t.Fatalf("summary call did not reuse the stable prefix:\n%s", request.Messages[0].Content)
	}
	if len(request.Tools) != 1 || request.Tools[0].Name != "plan.update" {
		t.Fatalf("summary call tools = %#v", request.Tools)
	}
}

// TestCompactionSummarizerPublishesTruncation pins the summary_truncated
// flag/event (R-A5-7): an over-budget summary reports the truncation and emits
// one event carrying the policy limit.
func TestCompactionSummarizerPublishesTruncation(t *testing.T) {
	publisher := &fakeSummaryEventPublisher{}
	in := testSummaryInput()
	in.SummaryMax = 240
	confidence := 0.5
	for index := 0; index < 40; index++ {
		in.Facts = append(in.Facts, agentcontext.FactSnapshot{
			ID: int64(100 + index), Kind: "finding",
			Key: fmt.Sprintf("key.%02d", index), Summary: strings.Repeat("细节", 20),
			Confidence: &confidence,
		})
	}
	chat := &scriptedChat{responses: []llmapp.ChatResponse{{Content: strings.Repeat("很长的摘要内容。", 1000)}}}
	result := agentapp.NewCompactionSummarizer(chat, agentapp.WithSummaryEventPublisher(publisher)).
		Summarize(context.Background(), in)
	if !result.Truncated {
		t.Fatalf("summary did not report truncation: %+v", result)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("summary events = %#v, want exactly one", publisher.events)
	}
	event := publisher.events[0]
	if event.Type != agentapp.EventSummaryTruncated {
		t.Fatalf("event type = %q, want %q", event.Type, agentapp.EventSummaryTruncated)
	}
	if event.Payload["summary_max"] != 240 {
		t.Fatalf("event summary_max = %#v, want 240", event.Payload["summary_max"])
	}
}

// TestCompactionSummarizerFallsBackWithoutGateway pins the deterministic
// fallback: a missing/failed summary call still returns the nine-section
// summary instead of failing the compaction.
func TestCompactionSummarizerFallsBackWithoutGateway(t *testing.T) {
	in := testSummaryInput()
	result := agentapp.NewCompactionSummarizer(nil).Summarize(context.Background(), in)
	if !result.UsedFallback {
		t.Fatalf("missing gateway did not fall back: %+v", result)
	}
	for _, heading := range agentapp.SummarySectionHeadings() {
		if !strings.Contains(result.Text, "## "+heading) {
			t.Fatalf("fallback summary dropped %q:\n%s", heading, result.Text)
		}
	}
}

// TestNormalizeSummaryDropsPreamble pins "只输出摘要文本": a model preamble is
// dropped and the nine canonical headings are restored.
func TestNormalizeSummaryDropsPreamble(t *testing.T) {
	raw := "好的，以下是压缩摘要：\n\n## 用户目标与意图\n实现登录功能。\n\n## 下一步\n继续 plan_item id=7。"
	normalized := agentapp.NormalizeSummary(raw)
	if strings.Contains(normalized, "好的") {
		t.Fatalf("preamble was not dropped:\n%s", normalized)
	}
	for _, heading := range agentapp.SummarySectionHeadings() {
		if !strings.Contains(normalized, "## "+heading) {
			t.Fatalf("normalised summary misses %q:\n%s", heading, normalized)
		}
	}
}

// TestSummaryResultCommitCarriesPersistenceFields pins the R-A5-12 commit
// contract: the result renders every field the conversation_summary hook needs
// (compaction id, covered watermark, version ids, summary_meta, pinned facts)
// and the ledger assigns it the next version.
func TestSummaryResultCommitCarriesPersistenceFields(t *testing.T) {
	in := testSummaryInput()
	result := agentapp.BuildSummaryResult(in)
	commit := result.Commit(in)
	if commit.VersionID != 10 || commit.ConversationID != 20 ||
		commit.CompactionID != in.CompactionID {
		t.Fatalf("commit identity = %+v", commit)
	}
	if commit.CoveredMessageID != in.CoveredMessageID || commit.TokenEstimate != result.Tokens {
		t.Fatalf("commit watermark/tokens = %+v, want %d/%d",
			commit, in.CoveredMessageID, result.Tokens)
	}
	if len(commit.PinnedFacts) != 1 || commit.PinnedFacts[0] != "JWT 有效期 15 分钟" {
		t.Fatalf("commit pinned facts = %#v", commit.PinnedFacts)
	}
	if commit.SummaryMeta["trigger"] != string(agentcontext.Pressure) {
		t.Fatalf("commit trigger meta = %#v", commit.SummaryMeta["trigger"])
	}
	if commit.SummaryMeta["summary_max"] != 4800 {
		t.Fatalf("commit summary_max meta = %#v, want the policy cap 4800", commit.SummaryMeta["summary_max"])
	}
	if commit.SummaryMeta["truncated"] != false {
		t.Fatalf("commit truncated meta = %#v", commit.SummaryMeta["truncated"])
	}
	ledger := agentapp.NewSummaryLedger()
	record, err := ledger.AppendSummary(context.Background(), commit)
	if err != nil {
		t.Fatalf("append commit: %v", err)
	}
	if record.Version != 1 || record.Summary != result.Text {
		t.Fatalf("ledger record = %+v", record)
	}
}

// the overflow compaction rebuild inserts the rebuilt pinned task context into
// the compacted packet's pinned facts segment, ahead of the summary. The folded
// history stays out of it.
// TestCompactionReinjectsIntoCompactedPacket pins the runner wiring of R-A5-9:
// the overflow compaction rebuild inserts the rebuilt pinned task context into
// the compacted packet's pinned facts segment, ahead of the summary. The folded
// history stays out of it.
func TestCompactionReinjectsIntoCompactedPacket(t *testing.T) {
	fixture := newBriefingRunnerFixture(t)
	fixture.settings.ints["context.keep_recent_messages"] = 1
	fixture.conv.snapshot.Summary = ""
	fixture.conv.snapshot.PinnedFacts = nil
	fixture.conv.snapshot.Messages = []agentapp.ConversationMessage{
		{ID: 701, Role: "user", Text: "消息一：折叠目标"},
		{ID: 702, Role: "assistant", Text: "消息二：折叠回复"},
		{ID: 703, Role: "user", Text: "消息三：最新回复"},
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
	compacted := decodeStoredPacket(t, fixture.repo.packets[1])
	facts := strings.Join(segmentContents(compacted, agentcontext.SegmentFacts), "\n")
	for _, want := range []string{"压缩后重注入", "work_item: 1001", "acceptance", "goal: 回复用户最新消息"} {
		if !strings.Contains(facts, want) {
			t.Fatalf("compacted pinned facts miss the reinjected %q:\n%s", want, facts)
		}
	}
	for _, folded := range []string{"消息一：折叠目标", "消息二：折叠回复"} {
		if strings.Contains(facts, folded) {
			t.Fatalf("folded history %q leaked into the reinjection:\n%s", folded, facts)
		}
	}
}

// TestSummaryExcludesPinnedTaskContextAndLatestUser pins R-A5-4: the summary is
// derived from the folded transcript only. Pinned task context, pinned facts,
// work item/requirement/case ids and the latest (trailing) user message must
// never be rendered into the summary text, while still-true folded transcript
// content survives.
func TestSummaryExcludesPinnedTaskContextAndLatestUser(t *testing.T) {
	in := testSummaryInput()
	out := agentapp.BuildSummary(in)

	forbidden := []string{
		"task_type:",
		"work_item:",
		"pinned_fact:",
		"requirement_id=501",
		"case_id=7001",
		"goal: 实现登录功能",
		in.Task.Goal,
		in.PinnedFacts[0],
		"请实现 /api/login；错误码 FR-001。", // latest (trailing) user message
	}
	forbidden = append(forbidden, in.Task.Constraints...)
	forbidden = append(forbidden, in.Task.AcceptanceCriteria...)
	forbidden = append(forbidden, in.Task.Scope...)
	for _, text := range forbidden {
		if strings.Contains(out, text) {
			t.Fatalf("summary re-introduced pinned R-A5-4 material %q:\n%s", text, out)
		}
	}

	for _, want := range []string{
		"早前：请实现 /api/login",
		"src/auth/login.go",
		"go test ./internal/auth",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary lost folded transcript content %q:\n%s", want, out)
		}
	}
}

// TestSummaryWatermarkAppliesToDeterministicFallback pins the single watermark
// contract (R-A5-12): the deterministic builder folds only messages after
// PreviousCoveredMessageID, so an already-covered message is never re-summarized
// on the fallback path.
func TestSummaryWatermarkAppliesToDeterministicFallback(t *testing.T) {
	in := testSummaryInput()
	in.PreviousCoveredMessageID = 2
	out := agentapp.BuildSummary(in)
	for _, covered := range []string{"早前：请实现 /api/login", "src/auth/login.go"} {
		if strings.Contains(out, covered) {
			t.Fatalf("deterministic summary re-folded covered message material %q:\n%s", covered, out)
		}
	}
	if !strings.Contains(out, "go test ./internal/auth") {
		t.Fatalf("deterministic summary dropped the message after the watermark:\n%s", out)
	}
}

// TestSummaryDropsPinnedAndStalePriorLines pins the prior-summary merge: lines
// that re-introduce pinned R-A5-4 material are dropped, a fact whose kind/key is
// no longer current is stale and dropped, a still-current fact line survives and
// a plan item missing from the DB snapshot is dropped.
func TestSummaryDropsPinnedAndStalePriorLines(t *testing.T) {
	in := testSummaryInput()
	in.PriorSummary = `## 关键概念与约束
- goal: 实现登录功能
- 约束: 不得修改已确认需求

## 关键上下文
- fact kind=decision key=auth.method confidence=0.90 (prior merge marker)
- fact kind=decision key=auth.legacy confidence=0.90
- pinned_fact: JWT 有效期 15 分钟
- pinned_fact: 旧凭证 30 天
- plan_item id=99 status=in_progress
- 仍然有效的旧结论 attachment_id=9001`
	out := agentapp.BuildSummary(in)

	for _, stale := range []string{
		"goal: 实现登录功能",
		"不得修改已确认需求",
		"pinned_fact:",
		"旧凭证 30 天",
		"auth.legacy",
		"plan_item id=99",
	} {
		if strings.Contains(out, stale) {
			t.Fatalf("summary kept stale/pinned prior line %q:\n%s", stale, out)
		}
	}
	for _, want := range []string{
		"(prior merge marker)",
		"仍然有效的旧结论 attachment_id=9001",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary dropped still-current prior material %q:\n%s", want, out)
		}
	}
}

// TestSummaryPreservesBareNumbers pins R-A5-7's exact-value rule for bare
// numbers: HTTP statuses/counts from the folded range are preserved, while
// numbers that are part of an id field are not duplicated as bare noise.
func TestSummaryPreservesBareNumbers(t *testing.T) {
	in := testSummaryInput()
	in.Messages = append(in.Messages, agentapp.SummaryReplayMessage{
		ID: 5, Role: "tool",
		Content: "release check: HTTP 401 after 12 retries; attachment_id=9001",
	})
	out := agentapp.BuildSummary(in)
	for _, want := range []string{"401", "12"} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary lost bare number %q:\n%s", want, out)
		}
	}
	if count := strings.Count(out, "9001"); count != 1 {
		t.Fatalf("attachment id 9001 rendered %d times, want the id once and no bare duplicate:\n%s", count, out)
	}
}

// TestSummaryExcludesLatestUserEvenWhenFollowedByToolOutput pins the R-A5-4
// scope of the latest-user exclusion: the newest user message is pinned even
// when later assistant/tool material follows it in the folded range, and the
// folded tool output still survives in the summary.
func TestSummaryExcludesLatestUserEvenWhenFollowedByToolOutput(t *testing.T) {
	in := testSummaryInput()
	in.Messages = append(in.Messages, agentapp.SummaryReplayMessage{
		ID: 5, Role: "tool",
		Content: "post-turn tool output: attachment_id=9002",
	})
	out := agentapp.BuildSummary(in)
	if strings.Contains(out, "请实现 /api/login；错误码 FR-001。") {
		t.Fatalf("summary folded the pinned latest user message:\n%s", out)
	}
	if !strings.Contains(out, "attachment_id=9002") {
		t.Fatalf("summary dropped the folded post-turn tool output:\n%s", out)
	}
}

// TestSummaryCommitCapsPinnedFacts pins the R-A5-4 ≤20 cap on the persistence
// hook: the commit carries only the newest MaxPinnedFacts facts.
func TestSummaryCommitCapsPinnedFacts(t *testing.T) {
	in := testSummaryInput()
	in.PinnedFacts = make([]string, 0, 25)
	for index := 0; index < 25; index++ {
		in.PinnedFacts = append(in.PinnedFacts, fmt.Sprintf("钉住事实%02d", index))
	}
	commit := agentapp.BuildSummaryResult(in).Commit(in)
	if len(commit.PinnedFacts) != agentapp.MaxPinnedFacts {
		t.Fatalf("commit pinned facts = %d, want the R-A5-4 cap %d", len(commit.PinnedFacts), agentapp.MaxPinnedFacts)
	}
	if commit.PinnedFacts[0] != "钉住事实05" || commit.PinnedFacts[len(commit.PinnedFacts)-1] != "钉住事实24" {
		t.Fatalf("commit kept the wrong pinned-fact window: %#v", commit.PinnedFacts)
	}
}

// TestCompactionSummarizerFallbackPublishesTruncation pins the summary_truncated
// flag/event on the deterministic fallback path too: a missing gateway must not
// hide an over-budget summary.
func TestCompactionSummarizerFallbackPublishesTruncation(t *testing.T) {
	publisher := &fakeSummaryEventPublisher{}
	in := testSummaryInput()
	in.SummaryMax = 240
	confidence := 0.5
	for index := 0; index < 40; index++ {
		in.Facts = append(in.Facts, agentcontext.FactSnapshot{
			ID: int64(100 + index), Kind: "finding",
			Key: fmt.Sprintf("key.%02d", index), Summary: strings.Repeat("细节", 20),
			Confidence: &confidence,
		})
	}
	result := agentapp.NewCompactionSummarizer(nil, agentapp.WithSummaryEventPublisher(publisher)).
		Summarize(context.Background(), in)
	if !result.UsedFallback || !result.Truncated {
		t.Fatalf("fallback summary = %+v, want a truncated fallback", result)
	}
	if len(publisher.events) != 1 || publisher.events[0].Type != agentapp.EventSummaryTruncated {
		t.Fatalf("fallback summary events = %#v, want one %s", publisher.events, agentapp.EventSummaryTruncated)
	}
}

// TestCompactionReinjectionDoesNotDuplicatePinnedFacts pins R-A5-4/R-A5-9: the
// pinned facts already live in the pinned facts segment, so appending the
// post-compaction reinjection block must not render them a second time.
func TestCompactionReinjectionDoesNotDuplicatePinnedFacts(t *testing.T) {
	fixture := newBriefingRunnerFixture(t)
	fixture.settings.ints["context.keep_recent_messages"] = 1
	fixture.conv.snapshot.Summary = ""
	fixture.conv.snapshot.PinnedFacts = []string{"JWT 有效期 15 分钟"}
	fixture.conv.snapshot.Messages = []agentapp.ConversationMessage{
		{ID: 701, Role: "user", Text: "消息一：折叠目标"},
		{ID: 702, Role: "assistant", Text: "消息二：折叠回复"},
		{ID: 703, Role: "user", Text: "消息三：最新回复"},
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
	compacted := decodeStoredPacket(t, fixture.repo.packets[1])
	facts := strings.Join(segmentContents(compacted, agentcontext.SegmentFacts), "\n")
	if count := strings.Count(facts, "JWT 有效期 15 分钟"); count != 1 {
		t.Fatalf("pinned fact rendered %d times in the facts segment, want once:\n%s", count, facts)
	}
	if !strings.Contains(facts, "work_item: 1001") || !strings.Contains(facts, "acceptance") {
		t.Fatalf("compacted pinned facts lost the reinjected task context:\n%s", facts)
	}
}

// TestSummaryRequestReusesStablePrefixByteForByte pins R-A5-8's cache-alignment
// byte rule: the system message of the summary request is the live stable prefix
// unchanged, including any leading/trailing whitespace.
func TestSummaryRequestReusesStablePrefixByteForByte(t *testing.T) {
	in := testSummaryInput()
	in.StablePrefix = "  [profile:clarification_host] 稳定前缀  \n\n# 版本不变量\n- 版本目标: 订单系统  \n"
	request := agentapp.BuildSummaryRequest(in)
	if request.Messages[0].Role != "system" || request.Messages[0].Content != in.StablePrefix {
		t.Fatalf("summary request rewrote the stable prefix:\nwant %q\ngot  %q", in.StablePrefix, request.Messages[0].Content)
	}
}

// TestSummaryRequestIsCacheAlignedPrefix pins R-A5-8's prefix-reuse contract:
// the request replays the live prefix messages and the watermark-filtered folded
// range verbatim, so its message sequence (before the compaction instruction) is
// a prefix of the live provider transcript with no skipped middle messages.
// Tool calls and reasoning content survive the replay.
func TestSummaryRequestIsCacheAlignedPrefix(t *testing.T) {
	live := []agentapp.SummaryReplayMessage{
		{Role: "user", Content: "版本 briefing：登录需求"},
		{Role: "user", Content: "版本 facts：订单系统"},
		{Role: "user", Content: "已有摘要：早期上下文"},
	}
	folded := []agentapp.SummaryReplayMessage{
		{ID: 11, Role: "user", Content: "请阅读登录实现"},
		{
			ID: 12, Role: "assistant", Content: "",
			ReasoningContent: "先阅读登录实现",
			ToolCalls:        []llmapp.ToolCall{{ID: "call-1", Name: "file.read", Arguments: `{"path":"src/auth/login.go"}`}},
		},
		{ID: 13, Role: "tool", ToolCallID: "call-1", Content: "package auth"},
	}
	in := testSummaryInput()
	in.StablePrefix = "  [profile:clarification_host] 稳定前缀  \n"
	in.LiveMessages = live
	in.Messages = folded
	in.PreviousCoveredMessageID = 0
	in.CoveredMessageID = 13

	request := agentapp.BuildSummaryRequest(in)
	expected := make([]llmapp.ChatMessage, 0, 1+len(live)+len(folded))
	expected = append(expected, llmapp.ChatMessage{Role: "system", Content: in.StablePrefix})
	for _, message := range live {
		expected = append(expected, summaryReplayChatMessage(message))
	}
	for _, message := range folded {
		expected = append(expected, summaryReplayChatMessage(message))
	}
	if len(request.Messages) != len(expected)+1 {
		t.Fatalf("summary request has %d messages, want the %d prefix messages plus the instruction",
			len(request.Messages), len(expected))
	}
	for index, want := range expected {
		if !reflect.DeepEqual(request.Messages[index], want) {
			t.Fatalf("summary request message %d is not the live transcript message:\nwant %#v\ngot  %#v",
				index, want, request.Messages[index])
		}
	}
	last := request.Messages[len(request.Messages)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "压缩指令") {
		t.Fatalf("last summary message is not the compaction instruction: %#v", last)
	}

	var toolCall, reasoning bool
	for _, message := range request.Messages {
		if len(message.ToolCalls) == 1 && message.ToolCalls[0].ID == "call-1" {
			toolCall = true
		}
		if message.ReasoningContent == "先阅读登录实现" {
			reasoning = true
		}
	}
	if !toolCall || !reasoning {
		t.Fatalf("summary replay lost tool calls/reasoning: toolCall=%v reasoning=%v", toolCall, reasoning)
	}
}

// summaryReplayChatMessage mirrors the production replay conversion for the
// expected-prefix assertion.
func summaryReplayChatMessage(message agentapp.SummaryReplayMessage) llmapp.ChatMessage {
	return llmapp.ChatMessage{
		Role:             message.Role,
		Content:          message.Content,
		Images:           message.Images,
		ToolCalls:        message.ToolCalls,
		ToolCallID:       message.ToolCallID,
		ReasoningContent: message.ReasoningContent,
	}
}

// TestSummaryMessagesPreservesStructuredParts pins the transcript contract of
// SummaryMessages: structured UI parts are rendered as bounded structured text
// (never dropped) while image payload bytes are replaced by the attachment
// marker.
func TestSummaryMessagesPreservesStructuredParts(t *testing.T) {
	messages := agentapp.SummaryMessages([]agentapp.ConversationMessage{{
		ID:   1,
		Role: "assistant",
		Text: "已生成需求卡",
		Parts: []agentapp.ConversationPart{
			{Type: "requirement_card", Data: map[string]any{"requirement_id": "501", "title": "登录"}},
			{Type: "image", Data: map[string]any{"attachment_id": 9001, "content": "SECRET_IMAGE_BYTES"}},
		},
	}})
	if len(messages) != 1 {
		t.Fatalf("summary messages = %#v", messages)
	}
	if !strings.Contains(messages[0].Content, "requirement_card") || !strings.Contains(messages[0].Content, "501") {
		t.Fatalf("structured requirement_card part was dropped: %q", messages[0].Content)
	}
	if strings.Contains(messages[0].Content, "SECRET_IMAGE_BYTES") {
		t.Fatalf("image payload leaked into the summary transcript: %q", messages[0].Content)
	}
}

// TestSummaryImageTokensChargedInRequestEstimate pins R-A5-5's 1500-tokens-per-
// image charge: the summary request estimate and the result bookkeeping both
// account for skipped images.
func TestSummaryImageTokensChargedInRequestEstimate(t *testing.T) {
	in := testSummaryInput()
	in.ImageCount = 2
	if estimate := agentapp.EstimateSummaryRequestTokens(in); estimate < 2*agentapp.SummaryImageTokens {
		t.Fatalf("summary request estimate = %d, want at least the %d image charge",
			estimate, 2*agentapp.SummaryImageTokens)
	}
	result := agentapp.BuildSummaryResult(in)
	if result.InputTokens < 2*agentapp.SummaryImageTokens {
		t.Fatalf("summary result input tokens = %d, want the %d image charge",
			result.InputTokens, 2*agentapp.SummaryImageTokens)
	}
}

// TestCompactionSummarizerFiltersPinnedLeak is the H5 guard (v0.22 fix round
// 1): a model draft that reproduces explicitly pinned material (session facts or
// the pinned task context) must not leak it into the persisted rolling summary.
func TestCompactionSummarizerFiltersPinnedLeak(t *testing.T) {
	in := testSummaryInput()
	chat := &scriptedChat{responses: []llmapp.ChatResponse{{
		Content: "## 用户目标与意图\n- 回顾：JWT 有效期 15 分钟\n- 实现登录功能\n" +
			"## 关键上下文\n- 不得修改已确认需求\n",
	}}}
	result := agentapp.NewCompactionSummarizer(chat).Summarize(context.Background(), in)
	if !result.PinnedLeakFiltered {
		t.Fatalf("pinned leak was not detected: %+v", result)
	}
	for _, leaked := range []string{"JWT 有效期 15 分钟", "不得修改已确认需求"} {
		if strings.Contains(result.Text, leaked) {
			t.Fatalf("summary leaked pinned material %q:\n%s", leaked, result.Text)
		}
	}
	if result.Tokens != agentapp.EstimateSummaryTokens(result.Text) {
		t.Fatalf("token bookkeeping not refreshed after filtering: %d", result.Tokens)
	}

	// A draft without pinned material is unchanged by the filter.
	clean := testSummaryInput()
	cleanChat := &scriptedChat{responses: []llmapp.ChatResponse{{
		Content: "## 用户目标与意图\n- 实现登录接口\n",
	}}}
	cleanResult := agentapp.NewCompactionSummarizer(cleanChat).Summarize(context.Background(), clean)
	if cleanResult.PinnedLeakFiltered {
		t.Fatalf("clean summary was filtered: %+v", cleanResult)
	}
}
