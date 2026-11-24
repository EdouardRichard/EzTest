package agentapp

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// streamTurn assembles the normalized deltas of one streaming model turn into
// the ChatResponse equivalent the runner loop consumes. Content/reasoning text
// concatenate in arrival order; tool-call fragments assemble per index (the
// identity announced on the first fragment, arguments concatenated across
// fragments); the terminal delta's finish reason, usage snapshot and provider
// reasoning items are kept verbatim. Non-Host profiles keep the batch write: the
// assembled turn is persisted once, exactly like the former Chat response. The
// Host profiles additionally mirror the accumulating snapshots into their
// conversation head while the turn streams (v0.2.4-D T6).
type streamTurn struct {
	content        strings.Builder
	reasoning      strings.Builder
	reasoningItems []json.RawMessage
	calls          map[int]*streamedToolCall
	finishReason   string
	usage          map[string]int
}

// streamedToolCall is one function call being assembled from fragments.
type streamedToolCall struct {
	id        string
	name      string
	arguments strings.Builder
}

func newStreamTurn() *streamTurn {
	return &streamTurn{calls: make(map[int]*streamedToolCall)}
}

// apply folds one delta into the turn. Usage is a cumulative provider snapshot
// (last wins), not an additive fragment; reasoning items arrive on the terminal
// delta (Responses protocol) and are kept for the next turn's replay.
func (t *streamTurn) apply(delta llmapp.Delta) {
	if delta.Content != "" {
		t.content.WriteString(delta.Content)
	}
	if delta.ReasoningContent != "" {
		t.reasoning.WriteString(delta.ReasoningContent)
	}
	if delta.ToolCall != nil {
		call, ok := t.calls[delta.ToolCall.Index]
		if !ok {
			call = &streamedToolCall{}
			t.calls[delta.ToolCall.Index] = call
		}
		if delta.ToolCall.ID != "" {
			call.id = delta.ToolCall.ID
		}
		if delta.ToolCall.Name != "" {
			call.name = delta.ToolCall.Name
		}
		call.arguments.WriteString(delta.ToolCall.Arguments)
	}
	if delta.FinishReason != "" {
		t.finishReason = delta.FinishReason
	}
	if delta.Usage != nil {
		t.usage = delta.Usage
	}
	if len(delta.ReasoningItems) > 0 {
		t.reasoningItems = delta.ReasoningItems
	}
}

// response renders the assembled turn. Tool calls are emitted in index order
// so parallel calls keep a deterministic order regardless of fragment arrival.
func (t *streamTurn) response() llmapp.ChatResponse {
	response := llmapp.ChatResponse{
		Content:          t.content.String(),
		ReasoningContent: t.reasoning.String(),
		ReasoningItems:   t.reasoningItems,
		FinishReason:     t.finishReason,
		Usage:            t.usage,
	}
	indices := make([]int, 0, len(t.calls))
	for index := range t.calls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		call := t.calls[index]
		response.ToolCalls = append(response.ToolCalls, llmapp.ToolCall{
			ID:        call.id,
			Name:      call.name,
			Arguments: call.arguments.String(),
		})
	}
	return response
}

// streamChatTurn consumes one streaming model turn through the ChatGateway
// port. firstDeltaSeen records the R-D7 retry boundary: before the first delta
// the loop keeps its overflow compaction rescue; after it, any error ends the
// turn without retrying (a retry would duplicate content the caller already
// saw). The partial turn is returned alongside the error so the caller can
// preserve what was seen. No failure falls back to the non-streaming Chat path.
//
// v0.2.4-D T6: the first delta also opens the Host conversation's incremental
// head, and every delta pushes the accumulated snapshots into it. onDelta is
// invoked synchronously by the adapter, so all head writes for one message stay
// on this single goroutine (the T5 publish-ordering requirement).
func (r *Runner) streamChatTurn(ctx context.Context, state *runState, request llmapp.ChatRequest, turnStarted time.Time) (llmapp.ChatResponse, error) {
	turn := newStreamTurn()
	state.firstDeltaSeen = false
	state.startHostStreamTurn()
	state.startTranscriptTurn()
	err := r.chat.Stream(ctx, request, func(delta llmapp.Delta) {
		if !state.firstDeltaSeen {
			state.firstDeltaSeen = true
			r.beginHostStream(ctx, state)
			r.beginTranscriptStream(ctx, state)
		}
		turn.apply(delta)
		r.applyHostStreamDelta(ctx, state, turn, turnStarted)
		r.applyTranscriptDelta(ctx, state, turn)
	})
	return turn.response(), err
}

// preservePartialTurn keeps the content a failed streaming turn already
// produced (R-D7, constitution VII.6): the partial reasoning/text is persisted
// instead of silently dropped and the interruption is logged. The visible
// error part and message-head state belong to the Host incremental write path.
//
// Cancellation is the most common mid-stream break, so the write must NOT ride
// the run context: a cancelled context would guarantee the partial turn is
// lost. context.WithoutCancel keeps the values (agent run/item attribution)
// while a bounded timeout keeps the write from hanging.
func (r *Runner) preservePartialTurn(ctx context.Context, state *runState, response llmapp.ChatResponse, turnStarted time.Time, cause error) {
	if r.logger != nil {
		r.logger.Warn("llm stream failed after the first delta; keeping the partial turn",
			"run_id", state.run.ID, "turns", state.turns, "error", cause)
	}
	r.appendThinkingPart(state, response.ReasoningContent, turnStarted)
	if strings.TrimSpace(response.Content) == "" && len(response.ToolCalls) == 0 {
		return
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	// The turn stopped after content was already produced: the transcript row
	// is sealed interrupted, never complete, and keeps what was generated.
	if err := r.writeAssistantTurn(persistCtx, state, response, agent.MessageStreamStateInterrupted); err != nil {
		if r.logger != nil {
			r.logger.Warn("partial assistant turn could not be persisted",
				"run_id", state.run.ID, "error", err)
		}
	}
}

// appendThinkingPart records one model turn's reasoning as the collapsed
// thinking part ("思考了 Ns"). It is the SINGLE producer of that part shape
// (constitution V.3): the successful turn and the preserved partial turn both
// go through it. A Host streaming turn already owns a live thinking part for
// this turn, so it is finalized in place instead of appended a second time.
func (r *Runner) appendThinkingPart(state *runState, reasoning string, turnStarted time.Time) {
	text := boundPartText(strings.TrimSpace(reasoning), producedPartThinkingMax)
	if text == "" {
		return
	}
	part := ConversationPart{
		Type: "thinking",
		Text: text,
		Data: map[string]any{"duration_ms": partDurationMS(turnStarted, r.now())},
	}
	if stream := state.hostStream; stream != nil && stream.thinkingSeq > 0 && stream.thinkingSeq <= len(state.streamParts) {
		state.streamParts[stream.thinkingSeq-1] = part
		stream.dirty[stream.thinkingSeq] = true
		return
	}
	r.appendStreamPart(state, part)
}
