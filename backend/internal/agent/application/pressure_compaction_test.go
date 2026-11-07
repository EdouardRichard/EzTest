package agentapp_test

// Fix round 2 (Task 36 review): the proactive pressure-compaction path is a
// mid-run boundary. A compaction that folds history must keep the run's
// in-flight assistant/tool messages after the compacted packet prefix (never
// replay them through the conversation snapshot, which predates the run), and a
// no-op decision must not rewrite the live transcript, persist a new summary
// revision or publish a before==after compaction event.

import (
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// pressureCompactionSettings forces the R-A5-2 pressure trigger on the second
// model turn while leaving a real retain budget for compaction. The trigger is
// small (0.1% of usable) and the retain budget smaller still (0.05%), so
// Validate keeps retain < trigger < usable.
func pressureCompactionSettings(fixture *runnerFixture) {
	fixture.settings.ints["context.keep_recent_messages"] = 1
	fixture.settings.floats = map[string]float64{
		"compaction.threshold_ratio": 0.001,
		"compaction.retain_ratio":    0.0005,
	}
}

// pressureToolCallResponse is the first model turn of the pressure fixtures: an
// allowed Host tool call whose provider prompt size anchors the pressure check.
func pressureToolCallResponse() llmapp.ChatResponse {
	response := toolCall(tools.ToolRequirementRead, `{"requirement_id":"501"}`)
	response.Usage = map[string]int{"prompt_tokens": 1000}
	return response
}

// pressureHistory is a four-message conversation snapshot: with a one-message
// recent window the pressure compaction folds the three oldest turns and keeps
// the newest verbatim.
func pressureHistory(firstID int64) []agentapp.ConversationMessage {
	return []agentapp.ConversationMessage{
		{ID: firstID, Role: "user", Text: "较早需求一"},
		{ID: firstID + 1, Role: "user", Text: "较早需求二"},
		{ID: firstID + 2, Role: "user", Text: "较早需求三"},
		{ID: firstID + 3, Role: "user", Text: "最新需求"},
	}
}

// assertInflightToolPair asserts the prompt still carries the in-flight
// assistant tool call and its matching tool result, in order, and returns the
// tool result message.
func assertInflightToolPair(t *testing.T, request llmapp.ChatRequest) llmapp.ChatMessage {
	t.Helper()
	assistantIndex := -1
	toolIndex := -1
	for index, message := range request.Messages {
		if message.Role == "assistant" && len(message.ToolCalls) > 0 {
			assistantIndex = index
		}
		if message.Role == "tool" {
			toolIndex = index
		}
	}
	if assistantIndex < 0 {
		t.Fatalf("prompt lost the in-flight assistant tool call: %#v", request.Messages)
	}
	if toolIndex < 0 {
		t.Fatalf("prompt lost the in-flight tool result: %#v", request.Messages)
	}
	if assistantIndex > toolIndex {
		t.Fatalf("assistant tool call at %d must precede its tool result at %d", assistantIndex, toolIndex)
	}
	result := request.Messages[toolIndex]
	if callID := request.Messages[assistantIndex].ToolCalls[0].ID; result.ToolCallID != callID {
		t.Fatalf("tool call/result pairing lost: call %q, result %q", callID, result.ToolCallID)
	}
	if !strings.Contains(result.Content, "登录功能") {
		t.Fatalf("in-flight tool result content = %q", result.Content)
	}
	return result
}

// TestRunnerPressureCompactionPreservesInFlightHistory pins the mid-run pressure
// compaction contract: the compacted packet replaces only the conversation
// history it folded, so the current turn's assistant tool call and tool result
// stay in the next prompt after the compacted prefix.
func TestRunnerPressureCompactionPreservesInFlightHistory(t *testing.T) {
	fixture := newBriefingRunnerFixture(t)
	pressureCompactionSettings(fixture)
	fixture.conv.snapshot.Summary = ""
	fixture.conv.snapshot.PinnedFacts = nil
	fixture.conv.snapshot.Messages = pressureHistory(811)
	fixture.requirement.detail = tools.RequirementDetail{
		RequirementSummary: tools.RequirementSummary{ID: 501, Title: "登录功能"},
		Description:        "支持手机号登录",
	}
	fixture.chat.responses = []llmapp.ChatResponse{
		pressureToolCallResponse(),
		{Content: "压力压缩摘要"},
		{Content: "已完成。"},
	}

	if _, err := mustRun(t, fixture, hostItem()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(fixture.chat.requests) != 3 {
		t.Fatalf("chat requests = %d, want tool turn, summary, final turn", len(fixture.chat.requests))
	}
	if len(fixture.repo.packets) != 2 {
		t.Fatalf("persisted packets = %d, want prepare + pressure-compacted", len(fixture.repo.packets))
	}
	compacted := decodeStoredPacket(t, fixture.repo.packets[1])
	history := segmentContents(compacted, agentcontext.SegmentHistory)
	if len(history) != 1 || history[0] != "最新需求" {
		t.Fatalf("pressure-compacted history = %#v, want the newest message verbatim", history)
	}

	retry := fixture.chat.request(2)
	assertInflightToolPair(t, retry)
	if last := retry.Messages[len(retry.Messages)-1]; last.Role != "tool" {
		t.Fatalf("pressure-compacted prompt last message = %q, want the in-flight tool result", last.Role)
	}
	if !strings.Contains(requestPrompt(retry), "最新需求") {
		t.Fatalf("pressure-compacted prompt lost the retained recent history:\n%s", requestPrompt(retry))
	}
	// The folded range may appear inside the rolling summary only; it must
	// never be replayed as a transcript message again.
	for _, message := range retry.Messages {
		if strings.Contains(message.Content, agentapp.SummarySectionHeadings()[0]) {
			continue
		}
		for _, folded := range []string{"较早需求一", "较早需求二", "较早需求三"} {
			if strings.Contains(message.Content, folded) {
				t.Fatalf("pressure compaction replayed folded message %q outside the summary:\n%s",
					folded, message.Content)
			}
		}
	}

	if len(fixture.conv.summaries) != 1 {
		t.Fatalf("conversation summaries = %#v, want exactly one pressure revision", fixture.conv.summaries)
	}
	payload := compactedEventPayload(t, fixture.events)
	if trigger, _ := payload["trigger"].(string); trigger != string(agentcontext.Pressure) {
		t.Fatalf("context.compacted trigger = %#v, want pressure", payload["trigger"])
	}
	if covered, _ := payload["covered_message_id"].(int64); covered != 813 {
		t.Fatalf("context.compacted covered_message_id = %#v, want the folded watermark 813", payload["covered_message_id"])
	}
	if version, _ := payload["summary_version"].(int); version != 1 {
		t.Fatalf("context.compacted summary_version = %#v, want 1", payload["summary_version"])
	}
}

// TestRunnerPressureCompactionNoOpKeepsTranscriptAndSummary pins the no-op half
// of the pressure path: when the rebuilt packet has nothing left to fold, the
// decision must leave the live transcript, the persisted packet, the summary
// revision and the event stream untouched. The snapshot already carries a
// persisted rolling summary, so a naive "packet carries a summary" check would
// write a duplicate revision even though nothing was folded.
func TestRunnerPressureCompactionNoOpKeepsTranscriptAndSummary(t *testing.T) {
	fixture := newBriefingRunnerFixture(t)
	pressureCompactionSettings(fixture)
	// The recent window keeps the whole conversation, so the rebuilt packet has
	// nothing to fold; the persisted summary is merely carried into the prompt.
	fixture.settings.ints["context.keep_recent_messages"] = 12
	fixture.conv.snapshot.Summary = "此前持久化摘要"
	fixture.conv.snapshot.PinnedFacts = nil
	fixture.conv.snapshot.Messages = []agentapp.ConversationMessage{
		{ID: 831, Role: "user", Text: "最新需求"},
	}
	fixture.requirement.detail = tools.RequirementDetail{
		RequirementSummary: tools.RequirementSummary{ID: 501, Title: "登录功能"},
		Description:        "支持手机号登录",
	}
	fixture.chat.responses = []llmapp.ChatResponse{
		pressureToolCallResponse(),
		{Content: "已完成。"},
	}

	if _, err := mustRun(t, fixture, hostItem()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(fixture.chat.requests) != 2 {
		t.Fatalf("chat requests = %d, want tool turn + final turn", len(fixture.chat.requests))
	}
	if len(fixture.repo.packets) != 1 {
		t.Fatalf("persisted packets = %d, want only the prepare packet (the pressure attempt is a no-op)",
			len(fixture.repo.packets))
	}
	if len(fixture.conv.summaries) != 0 {
		t.Fatalf("conversation summaries = %#v, want no revision for a no-op compaction", fixture.conv.summaries)
	}
	if payloads := compactedEventPayloads(t, fixture.events); len(payloads) != 0 {
		t.Fatalf("context.compacted events = %d, want no before==after event", len(payloads))
	}

	// The retry prompt is exactly the live transcript plus the in-flight turn:
	// the no-op must not reset state.messages to the packet transcript.
	before := fixture.chat.request(0)
	after := fixture.chat.request(1)
	if len(after.Messages) != len(before.Messages)+2 {
		t.Fatalf("no-op pressure prompt messages = %d, want %d (live transcript + assistant + tool)",
			len(after.Messages), len(before.Messages)+2)
	}
	assertInflightToolPair(t, after)
}

// compactedEventPayloads returns every context.compacted payload of a run.
func compactedEventPayloads(t *testing.T, publisher *fakePublisher) []map[string]any {
	t.Helper()
	payloads := make([]map[string]any, 0)
	for _, event := range publisher.events {
		if event.Type == "context.compacted" {
			payloads = append(payloads, event.Payload)
		}
	}
	return payloads
}
