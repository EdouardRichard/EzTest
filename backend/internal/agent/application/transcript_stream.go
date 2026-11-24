package agentapp

// v0.2.4-D T8: the agent transcript incremental write path (SC-D3/R-D5).
//
// agent_message is the LLM transcript of EVERY profile (Host conversations
// included — their conversation_message head is a separate surface, T6). One
// assistant row per model turn is opened at the FIRST delta, rewritten in place
// while the model streams, and sealed when the turn ends: complete on a finished
// turn, interrupted on a turn cut short after the first delta. A sealed row is
// frozen — the repository refuses every later in-place write.
//
// R-D2 granularity: the DB always holds the FULL row snapshot; the run:{id}
// event carries only the increment when the reply text purely grows, and a full
// row snapshot otherwise (placeholder, tool calls/usage, terminal seal). Long
// replies therefore never grow the event body.
//
// Bounded persistence (H-E11/R-H7): the EVENT stream is never coalesced — every
// reply delta still publishes exactly one frame with the same offset/increment
// semantics — but the DB rewrite of the row is merged. pushedText is the text
// already PUBLISHED, flushedText the text already STORED; the row is rewritten
// with the latest published snapshot at most once per transcriptFlushInterval
// (or per transcriptFlushMaxDeltas deltas, whichever comes first). Every seal
// and terminal boundary synchronously flushes the latest text first, so the
// final row is byte-identical to an uncoalesced run and a mid-reply reload only
// ever sees an already-published prefix.
//
// All writes happen on the run's single stream-consumption goroutine (the
// ChatGateway adapters call onDelta synchronously), the same discipline the T5
// pipeline and the T6 Host path require. The flush bookkeeping lives on the
// per-run transcriptStreamState, so concurrent runs never share it.

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/textunit"
)

// TranscriptDeltaEventType is the ONE name of the run transcript event. It is
// published on eventingapp.RunChannel(runID) and nowhere else.
const TranscriptDeltaEventType = "agent.transcript.delta"

// Bounded persistence cadence of the streaming transcript row (H-E11/R-H7).
//
// T28b measured a real 1437-delta turn rewriting the full row on EVERY delta
// (~130-142 rewrites/s, quadratic under four concurrent runs). These two bounds
// keep the stored snapshot fresh without touching the per-delta event stream:
//
//   - transcriptFlushInterval bounds how far a mid-reply reload (which reads the
//     DB) can lag the published stream: at most one interval, never more than
//     300ms by contract (see task28c-R2).
//   - transcriptFlushMaxDeltas bounds the writes of a burst that delivers many
//     deltas inside one interval, so a fast provider cannot turn the interval
//     into an unbounded write count.
//
// The first content delta always writes (lastFlush is zero), so a reload sees
// the opening of the reply immediately.
const (
	transcriptFlushInterval  = 250 * time.Millisecond
	transcriptFlushMaxDeltas = 32
)

// TranscriptMessage is the ONE wire projection of an agent_message row. Both
// the admin-only snapshot endpoint and the delta's full-snapshot mode emit it,
// so the frontend has a single row shape to render. IDs are decimal strings
// (the platform wire convention); the collections are always present.
type TranscriptMessage struct {
	ID             string              `json:"id"`
	RunID          string              `json:"run_id,omitempty"`
	ConversationID string              `json:"conversation_id,omitempty"`
	Role           string              `json:"role"`
	Parts          []agent.MessagePart `json:"parts"`
	ToolCalls      []agent.ToolCall    `json:"tool_calls"`
	ToolCallID     *string             `json:"tool_call_id,omitempty"`
	Usage          map[string]int      `json:"usage"`
	StreamState    string              `json:"stream_state"`
	CreatedAt      time.Time           `json:"created_at"`
}

// NewTranscriptMessage is the ONE producer of the wire row.
func NewTranscriptMessage(message agent.AgentMessage) TranscriptMessage {
	view := TranscriptMessage{
		ID:          platformid.Format(message.ID),
		Role:        string(message.Role),
		Parts:       message.Parts,
		ToolCalls:   message.ToolCalls,
		ToolCallID:  message.ToolCallID,
		Usage:       message.Usage,
		StreamState: agent.NormalizeMessageStreamState(message.StreamState),
		CreatedAt:   message.CreatedAt,
	}
	if view.Parts == nil {
		view.Parts = []agent.MessagePart{}
	}
	if view.ToolCalls == nil {
		view.ToolCalls = []agent.ToolCall{}
	}
	if view.Usage == nil {
		view.Usage = map[string]int{}
	}
	if message.RunID != nil && *message.RunID > 0 {
		view.RunID = platformid.Format(*message.RunID)
	}
	if message.ConversationID != nil && *message.ConversationID > 0 {
		view.ConversationID = platformid.Format(*message.ConversationID)
	}
	return view
}

// TranscriptDelta is the agent.transcript.delta payload — the ONE authoritative
// schema of the run:{id} transcript stream. Exactly one of two modes is present:
//
//   - Append mode (the reply text purely grows): Seq is the 1-based index of the
//     growing part in Message.Parts, PartType is its type, Increment is the new
//     text suffix and Offset is the UTF-16 code-unit length of the accumulated
//     text BEFORE the suffix (JavaScript String.length, so a replayed frame is
//     deduped by offset). Increment is always non-empty in this mode.
//   - Snapshot mode (placeholder, tool calls/usage, any non-extension rewrite,
//     and every terminal seal): Message is the full wire row and replaces the
//     row wholesale. Seq/PartType/Offset/Increment are absent.
//
// StreamState is always present and always a member of the shared stream-state
// authority (streaming/complete/interrupted); a terminal state means the row is
// frozen and no further delta will arrive for it.
type TranscriptDelta struct {
	RunID       string             `json:"run_id"`
	MessageID   string             `json:"message_id"`
	StreamState string             `json:"stream_state"`
	Seq         int                `json:"seq,omitempty"`
	PartType    string             `json:"part_type,omitempty"`
	Offset      int                `json:"offset,omitempty"`
	Increment   string             `json:"increment,omitempty"`
	Message     *TranscriptMessage `json:"message,omitempty"`
}

// transcriptStreamState tracks the streaming assistant row of the CURRENT model
// turn. row is the snapshot the store is believed to hold; pushedText is the
// text already published on the run channel (the append offset is derived from
// it); flushedText is the text already stored in the row. The two are decoupled
// because persistence is coalesced: every delta advances pushedText and
// publishes one event, while the row is rewritten on a bounded cadence.
// unflushed counts the coalesced deltas since the last rewrite and lastFlush is
// its time; broken records a failed in-place write, after which only the seal
// (which flushes the latest text first) still writes the full snapshot, so a
// transient failure never loses content (constitution VII.6).
type transcriptStreamState struct {
	row         agent.AgentMessage
	pushedText  string
	flushedText string
	unflushed   int
	lastFlush   time.Time
	sealed      bool
	broken      bool
}

// flushDue reports whether the next delta should rewrite the row: the first
// content write seeds it promptly, then at most one write per
// transcriptFlushInterval or per transcriptFlushMaxDeltas coalesced deltas.
func (stream *transcriptStreamState) flushDue(now time.Time) bool {
	if stream.lastFlush.IsZero() {
		return true
	}
	if stream.unflushed >= transcriptFlushMaxDeltas {
		return true
	}
	return now.Sub(stream.lastFlush) >= transcriptFlushInterval
}

// startTranscriptTurn drops the previous turn's head: each model turn owns its
// own transcript row, exactly as the batch path appended one row per turn.
func (state *runState) startTranscriptTurn() {
	if state != nil {
		state.transcript = nil
	}
}

// beginTranscriptStream opens the streaming row at the turn's first delta. A
// failure is logged and leaves the turn on the batch append path — the model's
// work is never thrown away.
func (r *Runner) beginTranscriptStream(ctx context.Context, state *runState) {
	if state.transcript != nil {
		return
	}
	head := agent.AgentMessage{
		RunID:          &state.run.ID,
		ConversationID: state.run.ConversationID,
		Role:           agent.MessageRoleAssistant,
		Parts:          []agent.MessagePart{},
		StreamState:    agent.MessageStreamStateStreaming,
	}
	stored, err := r.repo.AppendMessage(ctx, head)
	if err != nil || stored.ID <= 0 {
		if r.logger != nil {
			r.logger.Warn("agent transcript streaming row could not be opened; writing the turn as one batch row",
				"run_id", state.run.ID, "error", err)
		}
		state.transcript = &transcriptStreamState{broken: true}
		return
	}
	state.transcript = &transcriptStreamState{row: stored}
	r.publishTranscriptSnapshot(ctx, state, stored)
}

// applyTranscriptDelta mirrors the accumulated reply text into the streaming row
// and publishes the increment. The event is never coalesced (one frame per
// delta, unchanged offset/increment semantics); only the row rewrite is bounded
// by flushDue. The DB is written after the frame is published, so a stored
// snapshot is always an already-published prefix. Tool-call fragments are
// deliberately NOT written mid-flight: agent.ToolCall requires an id and a
// name, and a half-assembled arguments string is not a valid call record — the
// assembled calls land with the turn's seal (the same rule the Host path
// applies to its tool_call parts).
func (r *Runner) applyTranscriptDelta(ctx context.Context, state *runState, turn *streamTurn) {
	stream := state.transcript
	if stream == nil || stream.row.ID <= 0 || stream.broken || stream.sealed {
		return
	}
	content := turn.content.String()
	if content == "" || content == stream.pushedText {
		return
	}
	previous := stream.pushedText
	row := stream.row
	row.Parts = []agent.MessagePart{{Type: "text", Text: content}}
	stream.pushedText = content
	if strings.HasPrefix(content, previous) {
		r.publishTranscript(ctx, state, TranscriptDelta{
			RunID:       platformid.Format(state.run.ID),
			MessageID:   platformid.Format(row.ID),
			StreamState: agent.MessageStreamStateStreaming,
			Seq:         1,
			PartType:    "text",
			Offset:      textunit.UTF16Length(previous),
			Increment:   content[len(previous):],
		})
	} else {
		r.publishTranscriptSnapshot(ctx, state, row)
	}

	now := r.now()
	if !stream.flushDue(now) {
		stream.unflushed++
		return
	}
	if err := r.repo.UpdateMessage(ctx, row); err != nil {
		stream.broken = true
		if r.logger != nil {
			r.logger.Warn("agent transcript in-place update failed; the turn seal will carry the full row",
				"run_id", state.run.ID, "message_id", row.ID, "error", err)
		}
		return
	}
	stream.row = row
	stream.flushedText = content
	stream.lastFlush = now
	stream.unflushed = 0
}

// flushTranscript synchronously persists the latest PUBLISHED text before a
// seal or a terminal boundary (H-E11/R-H7: the coalesced cadence must never
// leave the final content unwritten). It is a no-op when the row already holds
// the latest text or a previous write failed (the seal still writes the full
// snapshot in that case).
func (r *Runner) flushTranscript(ctx context.Context, state *runState) {
	stream := state.transcript
	if stream == nil || stream.row.ID <= 0 || stream.broken || stream.sealed {
		return
	}
	if stream.pushedText == "" || stream.pushedText == stream.flushedText {
		return
	}
	row := stream.row
	row.Parts = []agent.MessagePart{{Type: "text", Text: stream.pushedText}}
	if err := r.repo.UpdateMessage(ctx, row); err != nil {
		stream.broken = true
		if r.logger != nil {
			r.logger.Warn("agent transcript flush before seal failed; the seal will carry the full row",
				"run_id", state.run.ID, "message_id", row.ID, "error", err)
		}
		return
	}
	stream.row = row
	stream.flushedText = stream.pushedText
	stream.lastFlush = r.now()
	stream.unflushed = 0
}

// writeAssistantTurn persists the turn's final assistant row: the streaming head
// is sealed in place when this turn opened one, otherwise the row is appended
// (batch path, or a head that could not be opened). Both paths publish one full
// snapshot so the admin transcript drawer sees the finished row.
func (r *Runner) writeAssistantTurn(ctx context.Context, state *runState, response llmapp.ChatResponse, streamState string) error {
	message := assistantTranscriptMessage(state, response, streamState)
	if stream := state.transcript; stream != nil && stream.row.ID > 0 && !stream.sealed {
		// A coalesced turn may hold deltas that never reached the DB: flush the
		// latest published text before the seal so the last mid-turn snapshot is
		// complete even if the seal write itself is transiently lost.
		r.flushTranscript(ctx, state)
		message.ID = stream.row.ID
		message.CreatedAt = stream.row.CreatedAt
		if err := r.repo.UpdateMessage(ctx, message); err != nil {
			if r.logger != nil {
				r.logger.Warn("agent transcript row could not be sealed",
					"run_id", state.run.ID, "message_id", message.ID, "stream_state", streamState, "error", err)
			}
			return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
		}
		stream.row = message
		stream.sealed = true
		r.publishTranscriptSnapshot(ctx, state, message)
		return nil
	}
	stored, err := r.repo.AppendMessage(ctx, message)
	if err != nil {
		return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
	}
	r.publishTranscriptSnapshot(ctx, state, stored)
	return nil
}

// assistantTranscriptMessage renders the persisted assistant row of one turn.
// It is the single producer of that row's content, shared by the streaming seal
// and the batch append.
func assistantTranscriptMessage(state *runState, response llmapp.ChatResponse, streamState string) agent.AgentMessage {
	parts := []agent.MessagePart{}
	if strings.TrimSpace(response.Content) != "" {
		parts = append(parts, agent.MessagePart{Type: "text", Text: response.Content})
	}
	message := agent.AgentMessage{
		RunID:          &state.run.ID,
		ConversationID: state.run.ConversationID,
		Role:           agent.MessageRoleAssistant,
		Parts:          parts,
		Usage:          response.Usage,
		StreamState:    streamState,
	}
	for _, call := range response.ToolCalls {
		message.ToolCalls = append(message.ToolCalls, agent.ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments})
	}
	return message
}

// sealTranscriptAtTerminal is the run's terminal guarantee for the transcript:
// a run that reached a terminal state never leaves a transcript row streaming.
// The turn paths normally seal already; this backstop covers a run that stopped
// between the last delta and its seal. The write rides a fresh bounded context
// because the run context may already be cancelled.
func (r *Runner) sealTranscriptAtTerminal(ctx context.Context, state *runState) {
	stream := state.transcript
	if stream == nil || stream.row.ID <= 0 || stream.sealed {
		return
	}
	sealCtx, cancel := terminalSealContext(ctx)
	defer cancel()
	row := stream.row
	// The turn stopped between deltas and its seal: the coalesced write may lag
	// the published stream, so the interrupted row carries the latest published
	// text, never a stale prefix (H-E11/R-H7).
	if stream.pushedText != "" {
		row.Parts = []agent.MessagePart{{Type: "text", Text: stream.pushedText}}
	}
	row.StreamState = agent.MessageStreamStateInterrupted
	if err := r.repo.UpdateMessage(sealCtx, row); err != nil {
		if r.logger != nil {
			r.logger.Warn("agent transcript row could not be sealed as interrupted at the run terminal",
				"run_id", state.run.ID, "message_id", row.ID, "error", err)
		}
		return
	}
	stream.row = row
	stream.sealed = true
	r.publishTranscriptSnapshot(sealCtx, state, row)
}

// publishTranscriptSnapshot publishes a full-row frame (placeholder, batch row,
// terminal seal). The payload is the JSON round-trip of TranscriptDelta so the
// wire shape is exactly the struct's json tags (single source).
func (r *Runner) publishTranscriptSnapshot(ctx context.Context, state *runState, message agent.AgentMessage) {
	view := NewTranscriptMessage(message)
	r.publishTranscript(ctx, state, TranscriptDelta{
		RunID:       platformid.Format(state.run.ID),
		MessageID:   view.ID,
		StreamState: view.StreamState,
		Message:     &view,
	})
}

// publishTranscript emits one transcript frame on the run's own channel. The
// publish deliberately does not ride the run context (a cancelled run must still
// report the seal) but keeps its request id so the frame stays attributable.
func (r *Runner) publishTranscript(ctx context.Context, state *runState, delta TranscriptDelta) {
	if r.events == nil {
		return
	}
	// TranscriptDelta holds only strings, ints and already-valid JSON-bearing
	// structs, so Marshal cannot fail; unmarshaling that object into a map
	// cannot fail either. No reachable path publishes an empty payload.
	payload := map[string]any{}
	if raw, err := json.Marshal(delta); err == nil {
		_ = json.Unmarshal(raw, &payload)
	}
	_ = r.events.Publish(context.Background(), eventingapp.Event{
		Channel: eventingapp.RunChannel(state.run.ID),
		Type:    TranscriptDeltaEventType,
		Scope: map[string]any{
			"version_id":   idText(state.item.VersionID),
			"agent_run_id": idText(state.run.ID),
		},
		Payload:   payload,
		RequestID: requestid.From(ctx),
	})
}
