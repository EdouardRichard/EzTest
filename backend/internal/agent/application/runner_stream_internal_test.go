package agentapp

// Streaming turn assembly and the explicit produced-part cap (v0.2.4-D T3).
// The internal tests drive the unexported assembler and part producer directly
// so the per-index tool-call assembly and the explicit overflow signal are
// pinned without a full run.

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

func TestStreamTurnAssemblesInterleavedToolCallsByIndex(t *testing.T) {
	turn := newStreamTurn()
	turn.apply(llmapp.Delta{ReasoningContent: "think "})
	turn.apply(llmapp.Delta{ReasoningContent: "more"})
	turn.apply(llmapp.Delta{Content: "Hello "})
	// Index 1 is announced first; argument fragments interleave across indices.
	turn.apply(llmapp.Delta{ToolCall: &llmapp.ToolCallDelta{Index: 1, ID: "call_second", Name: "tool.b"}})
	turn.apply(llmapp.Delta{ToolCall: &llmapp.ToolCallDelta{Index: 0, ID: "call_first", Name: "tool.a"}})
	turn.apply(llmapp.Delta{ToolCall: &llmapp.ToolCallDelta{Index: 1, Arguments: `{"b":`}})
	turn.apply(llmapp.Delta{Content: "world"})
	turn.apply(llmapp.Delta{ToolCall: &llmapp.ToolCallDelta{Index: 0, Arguments: `{"a":`}})
	turn.apply(llmapp.Delta{ToolCall: &llmapp.ToolCallDelta{Index: 1, Arguments: `2}`}})
	turn.apply(llmapp.Delta{ToolCall: &llmapp.ToolCallDelta{Index: 0, Arguments: `1}`}})
	items := []json.RawMessage{json.RawMessage(`{"type":"reasoning","id":"rs_1"}`)}
	turn.apply(llmapp.Delta{
		FinishReason:   "tool_calls",
		Usage:          map[string]int{"prompt_tokens": 13, "completion_tokens": 8, "total_tokens": 21},
		ReasoningItems: items,
	})

	response := turn.response()
	if response.Content != "Hello world" {
		t.Fatalf("content = %q, want the concatenated fragments", response.Content)
	}
	if response.ReasoningContent != "think more" {
		t.Fatalf("reasoning = %q", response.ReasoningContent)
	}
	if len(response.ToolCalls) != 2 {
		t.Fatalf("tool calls = %#v, want 2 distinct calls (index-0 hardcode merges them)", response.ToolCalls)
	}
	first, second := response.ToolCalls[0], response.ToolCalls[1]
	if first.ID != "call_first" || first.Name != "tool.a" || first.Arguments != `{"a":1}` {
		t.Fatalf("first call = %#v, want call_first/tool.a/{\"a\":1}", first)
	}
	if second.ID != "call_second" || second.Name != "tool.b" || second.Arguments != `{"b":2}` {
		t.Fatalf("second call = %#v, want call_second/tool.b/{\"b\":2}", second)
	}
	if response.FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %q", response.FinishReason)
	}
	if response.Usage["prompt_tokens"] != 13 || response.Usage["total_tokens"] != 21 {
		t.Fatalf("usage = %#v, want the terminal usage snapshot", response.Usage)
	}
	if len(response.ReasoningItems) != 1 || string(response.ReasoningItems[0]) != string(items[0]) {
		t.Fatalf("reasoning items = %#v, want the terminal items verbatim", response.ReasoningItems)
	}
}

func TestStreamTurnUsageIsLastWinsSnapshot(t *testing.T) {
	turn := newStreamTurn()
	turn.apply(llmapp.Delta{Usage: map[string]int{"prompt_tokens": 5, "total_tokens": 5}})
	turn.apply(llmapp.Delta{FinishReason: "stop", Usage: map[string]int{"prompt_tokens": 7, "completion_tokens": 3, "total_tokens": 10}})
	response := turn.response()
	// Usage snapshots are cumulative provider reports, not additive fragments:
	// the terminal snapshot replaces the earlier one (parity with Chat, which
	// returns exactly one usage object).
	if response.Usage["prompt_tokens"] != 7 || response.Usage["completion_tokens"] != 3 || response.Usage["total_tokens"] != 10 {
		t.Fatalf("usage = %#v, want the terminal snapshot (last wins)", response.Usage)
	}
}

// capturedLogs returns a runner whose logger writes into a buffer, so the
// observability of a drop can be asserted instead of discarded (constitution
// VII.7: observability is a feature and must be tested).
func capturedLogs() (*Runner, *bytes.Buffer) {
	logs := &bytes.Buffer{}
	return &Runner{logger: slog.New(slog.NewTextHandler(logs, nil))}, logs
}

func TestAppendStreamPartOverflowIsCountedAndSurfaced(t *testing.T) {
	runner, logs := capturedLogs()
	state := &runState{profile: agent.AgentProfile{Key: ProfileKeyClarificationHost}}
	for index := 0; index < maxProducedStreamParts+3; index++ {
		runner.appendStreamPart(state, ConversationPart{Type: "tool_call", Data: map[string]any{"call_id": idText(int64(index))}})
	}
	if len(state.streamParts) != maxProducedStreamParts {
		t.Fatalf("stored parts = %d, want the cap %d", len(state.streamParts), maxProducedStreamParts)
	}
	if state.droppedStreamParts != 3 {
		t.Fatalf("dropped parts = %d, want 3 (drops must be counted, not silent)", state.droppedStreamParts)
	}
	// The drop point logs the onset once (not once per dropped part).
	if got := strings.Count(logs.String(), "dropping further trace parts"); got != 1 {
		t.Fatalf("drop-point log count = %d, want exactly 1: %s", got, logs.String())
	}

	parts := runner.assistantConversationParts(state, "final reply", nil)
	var signal *ConversationPart
	for index := range parts {
		if parts[index].Type == "error" && parts[index].Data["code"] == "stream_parts_truncated" {
			signal = &parts[index]
		}
	}
	if signal == nil {
		t.Fatalf("overflow produced no explicit error part: %#v", parts)
	}
	if signal.Data["dropped_parts"] != 3 {
		t.Fatalf("signal = %#v, want the dropped count", signal.Data)
	}
	if !strings.Contains(logs.String(), "rendered trace is truncated") || !strings.Contains(logs.String(), "dropped_parts=3") {
		t.Fatalf("render-site log missing the final tally: %s", logs.String())
	}
	var text *ConversationPart
	for index := range parts {
		if parts[index].Type == "text" {
			text = &parts[index]
		}
	}
	if text == nil || text.Text != "final reply" {
		t.Fatalf("final text missing after the overflow signal: %#v", parts)
	}
}

// TestAppendStreamPartDropIsObservableWithoutRenderSite pins the drop signal on
// a profile that never renders the trace: the case executor returns from
// writeOutputs before assistantConversationParts is ever reached, so a counter
// read only at the render site would be write-only here. The real producer
// (appendToolParts) drives the cap, and the drop must still be observable.
func TestAppendStreamPartDropIsObservableWithoutRenderSite(t *testing.T) {
	runner, logs := capturedLogs()
	state := &runState{
		profile: agent.AgentProfile{Key: ProfileKeyCaseExecutor},
		run:     agent.AgentRun{ID: 77},
	}
	for index := 0; index < maxProducedStreamParts+2; index++ {
		runner.appendToolParts(state,
			llmapp.ToolCall{ID: "call_" + idText(int64(index)), Name: "exec.run", Arguments: `{}`},
			`{"ok":true}`, nil, 1)
	}
	if state.droppedStreamParts != 2 {
		t.Fatalf("dropped parts = %d, want 2", state.droppedStreamParts)
	}
	// The case executor never renders the trace: writeOutputs returns before
	// the render site, so the log at the drop point is the only signal.
	if err := runner.writeOutputs(context.Background(), state, "final reply"); err != nil {
		t.Fatalf("writeOutputs: %v", err)
	}
	if strings.Contains(logs.String(), "rendered trace is truncated") {
		t.Fatalf("case executor unexpectedly reached the render site: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "dropping further trace parts") {
		t.Fatalf("drop is silent on a non-rendering profile: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "profile="+ProfileKeyCaseExecutor) || !strings.Contains(logs.String(), "run_id=77") {
		t.Fatalf("drop log missing attribution (profile/run_id): %s", logs.String())
	}
}

func TestAppendStreamPartUnderCapProducesNoSignal(t *testing.T) {
	runner, logs := capturedLogs()
	state := &runState{}
	runner.appendStreamPart(state, ConversationPart{Type: "thinking", Text: "thought"})
	parts := runner.assistantConversationParts(state, "reply", nil)
	for _, part := range parts {
		if part.Type == "error" {
			t.Fatalf("unexpected error part under the cap: %#v", part)
		}
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %#v, want thinking + text", parts)
	}
	if logs.Len() != 0 {
		t.Fatalf("under-cap run logged a truncation: %s", logs.String())
	}
}

// stubClock is the fixed time source of the internal producer tests.
type stubClock struct{ at time.Time }

func (c stubClock) Now() time.Time { return c.at }

// TestAppendThinkingPartIsTheSingleProducer pins the carried T3 review #3: the
// successful turn and the preserved partial turn share ONE thinking-part
// producer, and a Host streaming turn that already owns a live thinking part
// finalizes it in place instead of appending a second copy.
func TestAppendThinkingPartIsTheSingleProducer(t *testing.T) {
	runner, _ := capturedLogs()
	started := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	runner.clock = stubClock{at: started.Add(1500 * time.Millisecond)}

	// No live part: the producer appends exactly one bounded thinking part.
	plain := &runState{}
	runner.appendThinkingPart(plain, "  完整思考  ", started)
	if len(plain.streamParts) != 1 || plain.streamParts[0].Type != "thinking" ||
		plain.streamParts[0].Text != "完整思考" || plain.streamParts[0].Data["duration_ms"] != int64(1500) {
		t.Fatalf("appended thinking part = %#v", plain.streamParts)
	}
	runner.appendThinkingPart(plain, "   ", started)
	if len(plain.streamParts) != 1 {
		t.Fatalf("empty reasoning appended a part: %#v", plain.streamParts)
	}

	// A live Host thinking part is finalized in place (no duplicate).
	live := &runState{
		streamParts: []ConversationPart{{Type: "thinking", Text: "部分", Data: map[string]any{"duration_ms": int64(0)}}},
		hostStream:  &hostStreamState{messageID: 9, thinkingSeq: 1, dirty: map[int]bool{}},
	}
	runner.appendThinkingPart(live, "完整思考", started)
	if len(live.streamParts) != 1 {
		t.Fatalf("live thinking part was duplicated: %#v", live.streamParts)
	}
	if live.streamParts[0].Text != "完整思考" || live.streamParts[0].Data["duration_ms"] != int64(1500) {
		t.Fatalf("live thinking part was not finalized: %#v", live.streamParts[0])
	}
	if !live.hostStream.dirty[1] {
		t.Fatal("the finalized thinking part was not marked for a head update")
	}
}

// TestStreamInterruptedPartHasOneShape pins the interruption error part: both
// the in-process seal and the crash-recovery sweep use the same stable code and
// a non-empty bounded message (constitution VII.6, no silent interruption).
func TestStreamInterruptedPartHasOneShape(t *testing.T) {
	for _, message := range []string{"模型中断", "", "   "} {
		part := streamInterruptedPart(message)
		if part.Type != "error" || part.Data["code"] != streamInterruptedCode {
			t.Fatalf("interruption part = %#v, want the shared error code", part)
		}
		text, _ := part.Data["message"].(string)
		if strings.TrimSpace(text) == "" {
			t.Fatalf("interruption part has an empty message: %#v", part)
		}
		if part.Data["retryable"] != false {
			t.Fatalf("interruption part retryable = %#v, want false", part.Data["retryable"])
		}
	}
	if got := streamInterruptedPart("").Data["message"]; got != streamInterruptedRecoveryMessage {
		t.Fatalf("default interruption message = %#v, want the recovery text", got)
	}
}
