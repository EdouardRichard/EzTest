package agentapp

// v0.2.4-D T6: the runner's Host incremental write path (SC-D2/SC-D4).
//
// A Host conversation turn (clarification_host/design_host) writes ONE assistant
// message incrementally: the placeholder head is opened at the first model
// delta through the single-writer lease, the produced parts are pushed to the
// head by seq while the model streams, and the run's terminal state seals it
// (complete on success, interrupted on any failure/cancel).
//
// Single source of the part list (constitution V): the head mirrors
// state.streamParts positionally — seq i is streamParts[i-1] — so the batch
// renderer (assistantConversationParts) and the incremental head can never
// disagree about what the reply contains. The terminal seal writes that same
// list, which also heals any update that failed mid-stream.
//
// All head writes happen on the run's single stream-consumption goroutine (the
// ChatGateway adapters call onDelta synchronously), as the T5 pipeline requires.

import (
	"context"
	"reflect"
	"strings"
	"time"

	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// streamInterruptedCode is the stable error-part code of an unfinished Host
// reply. Both the in-process interruption and the crash-recovery sweep use it,
// so the thread has one vocabulary for "this reply did not finish".
const streamInterruptedCode = "stream_interrupted"

// streamPartsTruncatedCode is the error-part code of the produced-part-cap
// truncation signal rendered by assistantConversationParts. It is META about the
// trace (the reply text is unaffected), not reply content. Single source shared
// with the render site (H-E16 review Minor 1).
const streamPartsTruncatedCode = "stream_parts_truncated"

// streamInterruptedRecoveryMessage is the recovery explanation of a head whose
// owning run died before it could seal it (SC-D4 "连接中断").
const streamInterruptedRecoveryMessage = "连接中断，本条回复未完成；已保留中断前生成的内容。"

// streamSealSalvageMessage explains a salvage seal whose stored prefix is genuinely
// incomplete: the full snapshot was refused (e.g. an over-long reply) and the head
// only holds a prefix, so the remainder really was not written.
const streamSealSalvageMessage = "回复未能完整保存，已保留中断前成功保存的内容；其余内容未能写入。"

// streamSealStoredCompleteMessage explains a salvage seal whose stored prefix already
// holds the WHOLE reply: the terminal snapshot was refused (e.g. a transient store
// failure) but nothing the model produced is missing. It must not claim content loss
// (H-E16, constitution VII.6 honest attribution) — only that the reply is sealed as
// interrupted because its completion could not be confirmed.
const streamSealStoredCompleteMessage = "回复内容已完整保存，但完成状态写入被拒绝；本条回复按中断封存。"

// User-facing explanations of the two context-driven stops. The job card
// deliberately suppresses an aborted run's raw error; the conversation thread
// must not reintroduce the Go string ("context canceled") either.
const (
	streamAbortedMessage    = "运行已取消，本条回复未完成；已保留取消前生成的内容。"
	streamDeadlineMessage   = "运行超时中断，本条回复未完成；已保留中断前生成的内容。"
	streamNoReplyMessage    = "本次运行没有产出可展示的回复内容。"
	streamUnfinishedMessage = "回复未完成；已保留中断前生成的内容。"
)

// hostStreamFailureMessage renders the user-facing explanation of a failed run
// for the interrupted head. The two context reasons get a sentence in the same
// register as the recovery text; every other reason reuses the job card's
// extractor so there is one source of failure wording.
func hostStreamFailureMessage(reason string, err error) string {
	switch reason {
	case "aborted":
		return streamAbortedMessage
	case "interrupted":
		return streamDeadlineMessage
	default:
		return jobCardFailureDetails(err).Reason
	}
}

// streamUnfinishedFallbackMessage renders the terminal interrupted seal's
// explanation when no concrete cause was recorded (hostStreamCause empty). This is
// a near-unreachable backstop — fail() normally records hostStreamCause — but it
// must stay in the user's register: the raw taxonomy reason code (model_error /
// budget / tool_failure / …) is a development label and never belongs in the
// conversation thread (H-E17, P31 runtime hygiene). The two context reasons reuse
// their specific sentences (single source with hostStreamFailureMessage); every
// other reason gets the generic unfinished explanation, with NO raw code appended.
func streamUnfinishedFallbackMessage(reason string) string {
	switch reason {
	case "aborted":
		return streamAbortedMessage
	case "interrupted":
		return streamDeadlineMessage
	default:
		return streamUnfinishedMessage
	}
}

// streamInterruptedPart is the ONE producer of the interruption error part.
// message explains the concrete cause; the code and shape are shared.
func streamInterruptedPart(message string) ConversationPart {
	message = strings.TrimSpace(message)
	if message == "" {
		message = streamInterruptedRecoveryMessage
	}
	return ConversationPart{Type: "error", Data: map[string]any{
		"code":      streamInterruptedCode,
		"message":   boundPartText(message, version.MessageErrorMessageMax),
		"retryable": false,
	}}
}

// hostStreamState tracks the incremental head of one Host run. messageID is 0
// until the first delta opens it; pushed/dirty track which seqs the head already
// carries so an unchanged part is never republished. stored is the snapshot the
// head actually holds (only successfully written parts), which is what a salvage
// seal falls back to. broken records that an in-place head write failed: only the
// HEAD stops tracking, the run keeps accumulating its parts and the terminal seal
// writes the full snapshot (logged, never silent — constitution VII.6). liveCap
// records that the produced-part cap refused a part, which the render site
// surfaces explicitly.
type hostStreamState struct {
	messageID   int64
	pushed      int
	dirty       map[int]bool
	stored      []ConversationPart
	sealed      bool
	broken      bool
	liveCap     bool
	textSeq     int
	thinkingSeq int
	toolCalls   map[string]int
}

// allowRunReplay applies the SC-D4/R-D7 invariant to a work-item retry decision:
// once this run has opened a Host reply head, the reply is (or is about to be)
// visible, so replaying the item would run the model again and write a SECOND
// message with the same content next to the first one. Every retry decision of
// a run goes through here, so the rule has one definition.
func (state *runState) allowRunReplay(retryable bool) bool {
	if state != nil && state.hostStream != nil && state.hostStream.messageID > 0 {
		return false
	}
	return retryable
}

// terminalSealContext detaches a seal write from the run's cancellation while
// keeping its values (request id / agent-run attribution), bounded so a stalled
// store cannot hang the terminal funnel.
func terminalSealContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
}

// unstreamedReplyText returns the final reply text the head does NOT already
// carry. Normally the last turn's live text part holds it. When the produced-part
// cap refused that part the reply must still be written: the cap bounds the
// TRACE, never the model reply (appendStreamPart's own contract), and the
// truncation signal promises "最终回复不受影响".
func (state *runState) unstreamedReplyText() string {
	if state.finalText == "" || state.hostStream == nil {
		return ""
	}
	if seq := state.hostStream.textSeq; seq > 0 && seq <= len(state.streamParts) {
		return ""
	}
	return state.finalText
}

// salvageCoversContent reports whether the head's stored prefix already contains
// every CONTENT part of the attempted terminal snapshot, so a salvage seal can
// honestly say nothing was lost (H-E16). Only trailing META error parts — the
// interruption explanation (streamInterruptedCode) and the trace-truncation signal
// (streamPartsTruncatedCode) — are excluded from the comparison; a real content
// error card (e.g. a tool_failure result) stays in the comparison, so one that was
// refused and never stored is conservatively reported as a loss (review Minor 1:
// stripping by Type=="error" alone would mask such a loss). stored mirrors the
// successfully-pushed parts positionally, so it covers the content exactly when it
// is at least as long and positionally equal. A strict prefix (a refused mid-stream
// update, an over-long part, an unstored content error card, or auxiliary cards the
// head never carried) returns false → the salvage wording reports the loss.
func salvageCoversContent(stored, attempted []ConversationPart) bool {
	content := attempted
	for len(content) > 0 && isSalvageMetaError(content[len(content)-1]) {
		content = content[:len(content)-1]
	}
	if len(stored) < len(content) {
		return false
	}
	return reflect.DeepEqual(stored[:len(content)], content)
}

// isSalvageMetaError reports whether an error part is seal/trace META (the
// interruption explanation or the produced-part-cap truncation signal) rather than
// reply CONTENT. Only meta error parts are excluded from the salvage coverage
// comparison; every other error part is treated as content (review Minor 1).
func isSalvageMetaError(part ConversationPart) bool {
	if part.Type != "error" {
		return false
	}
	code, _ := part.Data["code"].(string)
	return code == streamInterruptedCode || code == streamPartsTruncatedCode
}

// hostStreamPort returns the incremental surface for this run, or false when
// the profile is not a Host conversation profile, the writer lease was not
// acquired, or the adapter predates the incremental pipeline.
func (r *Runner) hostStreamPort(state *runState) (conversationStreamPort, bool) {
	if r == nil || r.conversation == nil || state == nil {
		return nil, false
	}
	if !isHostConversationProfile(state.profile.Key) || !state.writerLeased || state.payload.ConversationID <= 0 {
		return nil, false
	}
	port, ok := r.conversation.(conversationStreamPort)
	return port, ok
}

// hostStreamActive reports whether this run owns an unsealed incremental head.
func (state *runState) hostStreamActive() bool {
	return state != nil && state.hostStream != nil && state.hostStream.messageID > 0 && !state.hostStream.sealed
}

// hostReplyExtraParts is the profile-specific card set appended to the Host
// reply. The clarification Host owns requirement drafting, so its tools' cards
// ride the same assistant message; the design Host has none. Both the batch
// renderer and the incremental seal read it from here.
func hostReplyExtraParts(state *runState) []ConversationPart {
	if state != nil && state.profile.Key == ProfileKeyClarificationHost {
		return state.cards
	}
	return nil
}

// recoverOrphanHostStreams seals the heads a crashed Host turn left streaming in
// this conversation (SC-D4 crash path). It runs right after the writer lease is
// acquired: the lease takeover makes every other streaming head an orphan by
// construction, so no run-status guess is needed. Best effort — the previous
// run's head must never fail this one — but never silent.
func (r *Runner) recoverOrphanHostStreams(ctx context.Context, state *runState) {
	port, ok := r.hostStreamPort(state)
	if !ok {
		return
	}
	recoverCtx, cancel := context.WithTimeout(ctx, finishTimeout)
	defer cancel()
	sealed, err := port.InterruptOrphanStreams(recoverCtx, state.payload.ConversationID,
		state.profile.Key, state.run.ID, streamInterruptedPart(streamInterruptedRecoveryMessage))
	if err != nil {
		// H-E2: the sweep is per-head tolerant, so a non-nil error can accompany
		// heads that WERE sealed. Both numbers are reported: the heads left
		// streaming are a real, named degradation, never a silent partial success.
		if r.logger != nil {
			r.logger.Warn("orphaned conversation stream heads could not all be sealed",
				"conversation_id", state.payload.ConversationID, "run_id", state.run.ID,
				"sealed", sealed, "error", err)
		}
		return
	}
	if sealed > 0 && r.logger != nil {
		r.logger.Info("sealed orphaned conversation stream heads as interrupted",
			"conversation_id", state.payload.ConversationID, "run_id", state.run.ID, "heads", sealed)
	}
}

// beginHostStream opens the placeholder head at the first delta of the run.
// A failure is logged and leaves the run on the batch reply path: the model's
// work is not thrown away, and the batch write surfaces a real lease failure.
func (r *Runner) beginHostStream(ctx context.Context, state *runState) {
	if state.hostStream != nil {
		return
	}
	port, ok := r.hostStreamPort(state)
	if !ok {
		return
	}
	messageID, err := port.BeginAssistantStream(ctx, state.payload.ConversationID,
		state.profile.Key, state.run.ID, state.run.ID)
	if err != nil || messageID <= 0 {
		if r.logger != nil {
			r.logger.Warn("conversation incremental head could not be opened; writing the reply as one batch message",
				"conversation_id", state.payload.ConversationID, "run_id", state.run.ID, "error", err)
		}
		return
	}
	state.hostStream = &hostStreamState{messageID: messageID, dirty: map[int]bool{}, toolCalls: map[string]int{}}
}

// startHostStreamTurn resets the per-turn live part slots so a new model turn
// appends its own thinking/text parts instead of overwriting the previous
// turn's (already shown) content.
func (state *runState) startHostStreamTurn() {
	if state == nil || state.hostStream == nil {
		return
	}
	state.hostStream.textSeq = 0
	state.hostStream.thinkingSeq = 0
	state.hostStream.toolCalls = map[string]int{}
}

// applyHostStreamDelta pushes the accumulated reasoning/text snapshots of the
// turn being streamed. Each logical part keeps its seq for the whole turn; the
// update carries the full snapshot (R-D2), never the raw fragment.
func (r *Runner) applyHostStreamDelta(ctx context.Context, state *runState, turn *streamTurn, turnStarted time.Time) {
	stream := state.hostStream
	if stream == nil || stream.messageID == 0 {
		return
	}
	if reasoning := strings.TrimSpace(turn.reasoning.String()); reasoning != "" {
		stream.thinkingSeq = r.upsertHostLivePart(state, stream.thinkingSeq, ConversationPart{
			Type: "thinking",
			Text: boundPartText(reasoning, producedPartThinkingMax),
			Data: map[string]any{"duration_ms": partDurationMS(turnStarted, r.now())},
		})
	}
	// H-E13: trim the accumulated snapshot's edges so the incremental head carries
	// the same first/last whitespace as the batch loop's final text (which is
	// strings.TrimSpace'd). Trimming only the edges keeps the snapshot prefix-growing
	// (R-D2): internal whitespace is preserved and a later fragment still extends it.
	if content := strings.TrimSpace(turn.content.String()); content != "" {
		stream.textSeq = r.upsertHostLivePart(state, stream.textSeq, ConversationPart{Type: "text", Text: content})
	}
	r.syncHostStream(ctx, state)
}

// upsertHostLivePart replaces the turn's live part in place, or appends it on
// first arrival. It returns the part's 1-based seq (0 when the produced-part cap
// refused it; the render site then surfaces the truncation explicitly).
func (r *Runner) upsertHostLivePart(state *runState, seq int, part ConversationPart) int {
	stream := state.hostStream
	if seq <= 0 && stream.liveCap {
		return 0
	}
	if seq > 0 && seq <= len(state.streamParts) {
		previous := state.streamParts[seq-1]
		if previous.Type == part.Type && previous.Text == part.Text {
			return seq
		}
		state.streamParts[seq-1] = part
		stream.dirty[seq] = true
		return seq
	}
	before := len(state.streamParts)
	r.appendStreamPart(state, part)
	if len(state.streamParts) == before {
		// The produced-part cap refused the live part; the render site adds the
		// explicit truncation signal. Stop retrying the append so the drop is
		// counted once, not once per delta.
		stream.liveCap = true
		return 0
	}
	return len(state.streamParts)
}

// emitHostToolCallParts marks the turn's assembled tool calls as running before
// they execute, so the thread shows the call while it is in flight. Arguments
// are already assembled by index (streamTurn), so no raw JSON fragment ever
// reaches a part; appendToolParts rewrites the same seq with the final result.
// A call whose execution never reaches appendToolParts (the tool loop aborted)
// keeps its running marker: that is the truthful state of the call, and the
// interrupted seal's error part explains why the turn stopped.
func (r *Runner) emitHostToolCallParts(ctx context.Context, state *runState, calls []llmapp.ToolCall) {
	stream := state.hostStream
	if stream == nil || stream.messageID == 0 || stream.liveCap {
		return
	}
	for _, call := range calls {
		callID := strings.TrimSpace(call.ID)
		name := strings.TrimSpace(call.Name)
		if callID == "" || name == "" {
			continue
		}
		if _, exists := stream.toolCalls[callID]; exists {
			continue
		}
		data := map[string]any{
			"call_id":     truncateRunes(callID, version.ToolCallIDMax),
			"name":        truncateRunes(name, version.ToolCallNameMax),
			"status":      "running",
			"duration_ms": int64(0),
		}
		if arguments, ok := sanitizePartJSONObject(call.Arguments); ok {
			data["arguments"] = arguments
		}
		before := len(state.streamParts)
		r.appendStreamPart(state, ConversationPart{Type: "tool_call", Data: data})
		if len(state.streamParts) == before {
			stream.liveCap = true
			return
		}
		stream.toolCalls[callID] = len(state.streamParts)
	}
	r.syncHostStream(ctx, state)
}

// recordToolCallPart writes the finished tool_call part into the seq announced
// as running, or appends it when this run has no incremental head.
func (r *Runner) recordToolCallPart(state *runState, callID string, data map[string]any) {
	part := ConversationPart{Type: "tool_call", Data: data}
	if stream := state.hostStream; stream != nil {
		if seq, ok := stream.toolCalls[callID]; ok {
			delete(stream.toolCalls, callID)
			if seq > 0 && seq <= len(state.streamParts) {
				state.streamParts[seq-1] = part
				stream.dirty[seq] = true
				return
			}
		}
	}
	r.appendStreamPart(state, part)
}

// syncHostStream pushes every new or rewritten part to the head, in seq order,
// on the caller's single goroutine. A failed update is logged and stops further
// HEAD writes only — the run keeps accumulating its parts and the terminal seal
// still writes the full snapshot, so a transient part-row failure never loses
// content.
func (r *Runner) syncHostStream(ctx context.Context, state *runState) {
	stream := state.hostStream
	if stream == nil || stream.messageID == 0 || stream.broken || stream.sealed {
		return
	}
	port, ok := r.hostStreamPort(state)
	if !ok {
		return
	}
	for seq := 1; seq <= len(state.streamParts); seq++ {
		if seq <= stream.pushed && !stream.dirty[seq] {
			continue
		}
		if err := port.UpdateStreamPart(ctx, state.payload.ConversationID, stream.messageID, seq, state.streamParts[seq-1]); err != nil {
			stream.broken = true
			if r.logger != nil {
				r.logger.Warn("conversation stream part update failed; the terminal frame will carry the full reply",
					"conversation_id", state.payload.ConversationID, "run_id", state.run.ID,
					"message_id", stream.messageID, "seq", seq, "error", err)
			}
			return
		}
		// Record exactly what the head now stores: a salvage seal may only use
		// snapshots the store already accepted.
		part := state.streamParts[seq-1]
		switch {
		case seq == len(stream.stored)+1:
			stream.stored = append(stream.stored, part)
		case seq <= len(stream.stored):
			stream.stored[seq-1] = part
		}
		if seq > stream.pushed {
			stream.pushed = seq
		}
		delete(stream.dirty, seq)
	}
}

// writeHostReply writes the Host's final reply: the incremental head is sealed
// complete when this run streamed one, otherwise the batch writer appends the
// whole message (adapters without the T5 pipeline, or a head that could not be
// opened). The streamed text parts already carry the reply, so the incremental
// seal only appends finalText when the head does not hold it.
func (r *Runner) writeHostReply(ctx context.Context, state *runState, finalText string) error {
	if state.hostStreamActive() {
		if err := r.finishHostStream(ctx, state); err != nil {
			// The reply is already visible: replaying the item would regenerate
			// it into a second head (allowRunReplay is the single rule).
			return &RunError{Reason: "tool_failure", Retryable: state.allowRunReplay(true), Cause: err}
		}
		return nil
	}
	if err := r.appendAssistantReply(ctx, state, r.assistantConversationParts(state, finalText, hostReplyExtraParts(state))); err != nil {
		return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
	}
	return nil
}

// finishHostStream seals the incremental reply as complete with the same parts
// the batch renderer would have written, plus the reply text when the produced
// part cap kept it out of the live stream.
func (r *Runner) finishHostStream(ctx context.Context, state *runState) error {
	if !state.hostStreamActive() {
		return nil
	}
	parts := r.assistantConversationParts(state, state.unstreamedReplyText(), hostReplyExtraParts(state))
	if len(parts) == 0 {
		// Nothing was produced: an empty assistant message is not a valid
		// contract payload. Seal honestly as interrupted instead of faking a
		// complete empty reply.
		r.interruptHostStream(ctx, state, streamNoReplyMessage)
		return nil
	}
	return r.writeHostStreamSeal(ctx, state, parts, false)
}

// interruptHostStream seals the incremental reply as interrupted, preserving
// every accumulated part and appending the error part that explains the stop.
// It runs from the run's terminal funnel, where the run context may already be
// cancelled, so the write is detached from cancellation but keeps its values
// (constitution VII.6: cancellation must not discard what the user already saw).
func (r *Runner) interruptHostStream(ctx context.Context, state *runState, message string) {
	if !state.hostStreamActive() {
		return
	}
	parts := append(r.assistantConversationParts(state, state.unstreamedReplyText(), hostReplyExtraParts(state)),
		streamInterruptedPart(message))
	if err := r.writeHostStreamSeal(ctx, state, parts, true); err != nil && r.logger != nil {
		r.logger.Warn("conversation stream head could not be sealed as interrupted",
			"conversation_id", state.payload.ConversationID, "run_id", state.run.ID,
			"message_id", state.hostStream.messageID, "error", err)
	}
}

// writeHostStreamSeal writes one terminal snapshot. When the full snapshot is
// refused (for example a reply that outgrew the message contract), it falls back
// to exactly the prefix the head already stores plus an explicit error part:
// those parts were accepted by the store, nothing the user already saw is
// deleted, and a seal failure can never leave a frozen streaming bubble. The
// original failure is still returned so the run reports it honestly.
func (r *Runner) writeHostStreamSeal(ctx context.Context, state *runState, parts []ConversationPart, interrupted bool) error {
	port, ok := r.hostStreamPort(state)
	if !ok {
		return nil
	}
	sealCtx, cancel := terminalSealContext(ctx)
	defer cancel()
	write := port.FinishAssistantStream
	if interrupted {
		write = port.InterruptAssistantStream
	}
	err := write(sealCtx, state.payload.ConversationID, state.hostStream.messageID, parts)
	if err == nil {
		state.hostStream.sealed = true
		return nil
	}
	salvageMessage := streamSealSalvageMessage
	if salvageCoversContent(state.hostStream.stored, parts) {
		salvageMessage = streamSealStoredCompleteMessage
	}
	salvage := append(append([]ConversationPart(nil), state.hostStream.stored...),
		streamInterruptedPart(salvageMessage))
	salvageCtx, salvageCancel := terminalSealContext(ctx)
	defer salvageCancel()
	salvageErr := port.InterruptAssistantStream(salvageCtx, state.payload.ConversationID, state.hostStream.messageID, salvage)
	if r.logger != nil {
		r.logger.Warn("conversation stream terminal snapshot was refused; sealing the stored prefix as interrupted",
			"conversation_id", state.payload.ConversationID, "run_id", state.run.ID,
			"message_id", state.hostStream.messageID, "stored_parts", len(state.hostStream.stored),
			"error", err, "salvage_error", salvageErr)
	}
	if salvageErr == nil {
		state.hostStream.sealed = true
	}
	return err
}

// sealHostStreamAtTerminal is the run's terminal guarantee: a run that reached a
// terminal state never leaves its conversation head streaming. A done run seals
// complete (writeOutputs normally already did); every other outcome seals
// interrupted with the failure explanation.
func (r *Runner) sealHostStreamAtTerminal(ctx context.Context, state *runState, done bool, reason string) {
	if !state.hostStreamActive() {
		return
	}
	if !done {
		message := state.hostStreamCause
		if message == "" {
			message = streamUnfinishedFallbackMessage(reason)
		}
		r.interruptHostStream(ctx, state, message)
		return
	}
	if err := r.finishHostStream(ctx, state); err != nil && r.logger != nil {
		r.logger.Warn("conversation stream head could not be sealed as complete",
			"conversation_id", state.payload.ConversationID, "run_id", state.run.ID, "error", err)
	}
}
