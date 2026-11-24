package versionapp

// v0.2.4-D T5: the version conversation incremental stream pipeline (SC-D2,
// R-D2). An incremental Host reply is written as a placeholder message head
// created at stream start (BeginAssistantStream, through the single-writer
// lease), its parts updated in place by seq during streaming (UpdateStreamPart,
// one conversation.message.delta increment event per update) and sealed at the
// terminal state (FinishAssistantStream / InterruptAssistantStream, one full
// conversation.message frame compatible with the existing frontend).
//
// R-D2 increment granularity: the DB stores the FULL snapshot per seq (the part
// row payload and the head parts array); the SSE delta carries only the
// increment, so a long reply's event bodies never grow with its length. The
// batch AppendAssistant path is unchanged; this pipeline is additive.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/textunit"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// CodeMessageStreamClosed is the stable 409 code returned when an in-place
// stream update targets a head whose stream is already terminal.
const CodeMessageStreamClosed = "message_stream_closed"

// MessageDelta is the conversation.message.delta event payload — the ONE
// authoritative schema of the increment event (R-D2). It carries the increment
// of one streaming part, never the full growing snapshot. Exactly one of two
// modes is present:
//
//   - Append mode (text/thinking growth): Increment is the new text suffix and
//     Offset is the UTF-16 code-unit length of the accumulated text BEFORE the
//     suffix (matching JavaScript String.length, so the frontend can dedupe a
//     replayed frame by offset). Offset is omitted when 0; Increment is always
//     non-empty in this mode.
//   - Replace mode (every other part type, and any text/thinking rewrite that is
//     not a pure extension): Part is the full part snapshot (the contract
//     MessagePart JSON shape) and replaces the part at Seq wholesale. Increment
//     and Offset are absent.
//
// Seq is the 1-based conversation_message_part seq; PartType is the streaming
// enum value. message_id/conversation_id are decimal string IDs (the platform
// wire convention). The terminal head state is carried by the full
// conversation.message frame (stream_state), not by each delta.
type MessageDelta struct {
	MessageID      string          `json:"message_id"`
	ConversationID string          `json:"conversation_id"`
	Seq            int             `json:"seq"`
	PartType       string          `json:"part_type"`
	Offset         int             `json:"offset,omitempty"`
	Increment      string          `json:"increment,omitempty"`
	Part           json.RawMessage `json:"part,omitempty"`
}

// BeginAssistantStream creates the placeholder head of one incremental Host
// reply through the single-writer lease (non-writer -> 403, a different live
// turn -> 409). The head is written with stream_state=streaming and empty parts
// so conversation_message_part.message_id has a referent before the first part
// update. A retried turn returns the stored head's ID without a duplicate or a
// republished frame (turn-level idempotency, mirroring AppendAssistant).
func (s *ConversationService) BeginAssistantStream(ctx context.Context, conversationID int64, writer Writer, runID int64) (int64, error) {
	if conversationID <= 0 {
		return 0, platform.Invalid("cid", "must be a positive decimal ID")
	}
	writer.Agent = strings.TrimSpace(writer.Agent)
	if writer.TurnID <= 0 || writer.Agent == "" {
		return 0, newNotWriterError(conversationID, writer.TurnID, writer.Agent, NotWriterReasonNoLease)
	}
	// The read is only for the version projection; the lease fence is the
	// repository's locked compare-and-set, never this pre-read.
	conversation, err := s.repo.GetByID(ctx, conversationID)
	if err != nil {
		return 0, mapConversationRepositoryError(err)
	}
	now := s.clock.Now()
	message := version.Message{
		ID:             s.ids.Next(),
		ConversationID: conversationID,
		Role:           version.MessageRoleAssistant,
		Parts:          []version.MessagePart{},
		Refs:           []version.MessageRef{},
		TokenUsage:     map[string]any{},
		AuthorAgent:    writer.Agent,
		Kind:           version.MessageKindText,
		StreamState:    version.MessageStreamStateStreaming,
		CreatedAt:      now,
	}
	if runID > 0 {
		value := runID
		message.AgentRunID = &value
	}
	turnKey := version.HostTurnClientMessageID(writer.TurnID)
	message.ClientMessageID = &turnKey

	leaseUntil := now.Add(version.ConversationWriterLeaseDuration)
	existing, created, err := s.repo.BeginAssistantStream(
		ctx, conversationID, message, writer.TurnID, writer.Agent, now, leaseUntil)
	if err != nil {
		return 0, s.mapStreamWriteError(ctx, conversationID, writer.TurnID, writer.Agent, err)
	}
	if !created && existing != nil {
		return existing.ID, nil
	}
	// The placeholder publishes a full frame so the existing frontend renders an
	// empty streaming bubble; stream_state marks it unfinished.
	s.publishMessageEvent(ctx, conversation.VersionID, message)
	s.markRunning(ctx, conversation.VersionID)
	return message.ID, nil
}

// UpdateStreamPart rewrites the full snapshot of one streaming part (seq) in
// place and publishes a conversation.message.delta carrying only the increment.
// The same seq is idempotent (no row growth); concurrent updates are serialized
// by the repository row lock. The live lease must still belong to the message's
// owning Host turn; a terminal head rejects late deltas (409).
func (s *ConversationService) UpdateStreamPart(ctx context.Context, conversationID, messageID int64, seq int, part version.MessagePart) error {
	if conversationID <= 0 {
		return platform.Invalid("cid", "must be a positive decimal ID")
	}
	if messageID <= 0 {
		return platform.Invalid("mid", "must be a positive decimal ID")
	}
	if seq <= 0 {
		return platform.Invalid("seq", "must be a positive integer")
	}
	if !version.IsStreamingMessagePartType(part.Type) {
		return platform.Invalid("part_type", "must be a streaming conversation_message_part type")
	}
	if err := version.ValidateAssistantMessageParts([]version.MessagePart{part}); err != nil {
		return err
	}
	conversation, err := s.repo.GetByID(ctx, conversationID)
	if err != nil {
		return mapConversationRepositoryError(err)
	}
	now := s.clock.Now()
	previous, err := s.repo.UpdateStreamPart(ctx, conversationID, messageID, seq, part, now)
	if err != nil {
		return s.mapStreamWriteError(ctx, conversationID, 0, "", err)
	}
	s.publishMessageDelta(ctx, conversation.VersionID, conversationID, messageID, seq, part, previous)
	return nil
}

// FinishAssistantStream seals one incremental reply as complete and publishes
// the full conversation.message frame (final parts + stream_state=complete) so
// the existing frontend renders the final message unchanged. A retried finish is
// an idempotent no-op (no republish).
func (s *ConversationService) FinishAssistantStream(ctx context.Context, conversationID, messageID int64, finalParts []version.MessagePart) error {
	return s.finishStream(ctx, conversationID, messageID, finalParts, version.MessageStreamStateComplete)
}

// InterruptAssistantStream seals one incremental reply as interrupted (error /
// cancel / lease loss after the first delta). The accumulated content is
// preserved in finalParts (never deleted) and the head is marked unfinished; the
// full frame carries stream_state=interrupted.
func (s *ConversationService) InterruptAssistantStream(ctx context.Context, conversationID, messageID int64, finalParts []version.MessagePart) error {
	return s.finishStream(ctx, conversationID, messageID, finalParts, version.MessageStreamStateInterrupted)
}

// InterruptOrphanStreams seals the incremental heads a crashed (or fenced-out)
// Host turn left streaming in this conversation (SC-D4 crash recovery). The
// caller must own the live single-writer lease — acquiring it takes over from
// the dead turn, so every other streaming head is an orphan by construction.
// Each orphan keeps its accumulated parts and gains errorPart; the head is
// marked interrupted, never complete, and its terminal full frame is published
// so a live thread stops rendering an unfinished bubble. It returns the number
// of heads sealed; a repeated call is a no-op (0).
//
// H-E2: the sweep is per-head tolerant. When one head cannot be sealed the
// repository returns the heads it DID seal plus a *version.OrphanSealError; those
// frames are still published and the count still reports them, and the error is
// passed through unchanged (it is a recovery outcome, not a lease rejection, so
// the 403/409 writer mapping must not relabel it).
func (s *ConversationService) InterruptOrphanStreams(ctx context.Context, conversationID int64, writer Writer, errorPart version.MessagePart) (int, error) {
	if conversationID <= 0 {
		return 0, platform.Invalid("cid", "must be a positive decimal ID")
	}
	writer.Agent = strings.TrimSpace(writer.Agent)
	if writer.TurnID <= 0 || writer.Agent == "" {
		return 0, newNotWriterError(conversationID, writer.TurnID, writer.Agent, NotWriterReasonNoLease)
	}
	if errorPart.Type != version.MessagePartError {
		return 0, platform.Invalid("error_part", "must be an error part")
	}
	if err := version.ValidateAssistantMessageParts([]version.MessagePart{errorPart}); err != nil {
		return 0, err
	}
	conversation, err := s.repo.GetByID(ctx, conversationID)
	if err != nil {
		return 0, mapConversationRepositoryError(err)
	}
	sealed, err := s.repo.InterruptOrphanStreams(ctx, conversationID, writer.TurnID, writer.Agent, errorPart, s.clock.Now())
	return s.publishOrphanSeal(ctx, conversation.VersionID, sealed, err, func(err error) error {
		return s.mapStreamWriteError(ctx, conversationID, writer.TurnID, writer.Agent, err)
	})
}

// InterruptRunOrphanStreams seals the streaming heads of ONE terminal agent run
// in this conversation (H-E2). It is the recovery entry the agent crash sweep
// uses when the conversation gets no further turn: no lease holder exists to run
// InterruptOrphanStreams, and the run's terminal truth belongs to the agent
// context. Heads of other runs are never touched; the same per-head tolerance and
// terminal-frame republish apply.
func (s *ConversationService) InterruptRunOrphanStreams(ctx context.Context, conversationID, agentRunID int64, errorPart version.MessagePart) (int, error) {
	if conversationID <= 0 {
		return 0, platform.Invalid("cid", "must be a positive decimal ID")
	}
	if agentRunID <= 0 {
		return 0, platform.Invalid("agent_run_id", "must be a positive decimal ID")
	}
	if errorPart.Type != version.MessagePartError {
		return 0, platform.Invalid("error_part", "must be an error part")
	}
	if err := version.ValidateAssistantMessageParts([]version.MessagePart{errorPart}); err != nil {
		return 0, err
	}
	conversation, err := s.repo.GetByID(ctx, conversationID)
	if err != nil {
		return 0, mapConversationRepositoryError(err)
	}
	sealed, err := s.repo.InterruptRunOrphanStreams(ctx, conversationID, agentRunID, errorPart, s.clock.Now())
	return s.publishOrphanSeal(ctx, conversation.VersionID, sealed, err, func(err error) error {
		return mapConversationRepositoryError(err)
	})
}

// publishOrphanSeal is the ONE outcome handler of both recovery sweeps: every
// sealed head's terminal frame is published even when the sweep also reports a
// head it could not seal, and a partial-recovery report is passed through
// untouched while every other failure goes through the caller's error mapping.
func (s *ConversationService) publishOrphanSeal(ctx context.Context, versionID int64, sealed []version.Message, err error, mapErr func(error) error) (int, error) {
	var partial *version.OrphanSealError
	if err != nil && !errors.As(err, &partial) {
		return 0, mapErr(err)
	}
	for _, head := range sealed {
		s.publishMessageEvent(ctx, versionID, head)
	}
	if partial != nil {
		return len(sealed), partial
	}
	return len(sealed), nil
}

func (s *ConversationService) finishStream(ctx context.Context, conversationID, messageID int64, finalParts []version.MessagePart, streamState string) error {
	if conversationID <= 0 {
		return platform.Invalid("cid", "must be a positive decimal ID")
	}
	if messageID <= 0 {
		return platform.Invalid("mid", "must be a positive decimal ID")
	}
	if err := version.ValidateAssistantMessageParts(finalParts); err != nil {
		return err
	}
	conversation, err := s.repo.GetByID(ctx, conversationID)
	if err != nil {
		return mapConversationRepositoryError(err)
	}
	now := s.clock.Now()
	head, finished, err := s.repo.FinishAssistantStream(ctx, conversationID, messageID, finalParts, streamState, now)
	if err != nil {
		return s.mapStreamWriteError(ctx, conversationID, 0, "", err)
	}
	if !finished {
		return nil
	}
	s.publishMessageEvent(ctx, conversation.VersionID, head)
	return nil
}

// mapStreamWriteError maps the repository's stream write sentinels to the typed
// API errors shared with the batch writer path (409 busy / 403 not_writer / 409
// closed), so the HTTP edge never parses strings.
func (s *ConversationService) mapStreamWriteError(ctx context.Context, conversationID, turnID int64, agent string, err error) error {
	switch {
	case errors.Is(err, version.ErrConversationBusy):
		return s.writerBusyError(ctx, conversationID)
	case errors.Is(err, version.ErrNotConversationWriter):
		return newNotWriterError(conversationID, turnID, agent, NotWriterReasonLeaseLost)
	case errors.Is(err, version.ErrMessageStreamClosed):
		return platform.NewError(http.StatusConflict, CodeMessageStreamClosed,
			"conversation message stream is closed")
	default:
		return mapConversationRepositoryError(err)
	}
}

// publishMessageDelta derives the increment from the previous stored snapshot
// and publishes conversation.message.delta on the version channel. The DB already
// holds the full snapshot (the repository wrote it); the event carries only the
// increment so long replies never grow the event body.
func (s *ConversationService) publishMessageDelta(ctx context.Context, versionID, conversationID, messageID int64, seq int, part version.MessagePart, previous json.RawMessage) {
	if s.events == nil {
		return
	}
	offset, increment, replacePart := streamDelta(previous, part)
	delta := MessageDelta{
		MessageID:      platformid.Format(messageID),
		ConversationID: platformid.Format(conversationID),
		Seq:            seq,
		PartType:       part.Type,
		Offset:         offset,
		Increment:      increment,
		Part:           replacePart,
	}
	// The payload is the JSON round-trip of MessageDelta so the wire shape is
	// exactly the struct's json tags (single source). Both errors are
	// constructionally unreachable, mirroring streamDelta's fallback rationale:
	// MessageDelta holds only string/int/json.RawMessage fields, and Part is
	// either nil or a already-valid MessagePart JSON document, so json.Marshal
	// cannot fail; unmarshaling that valid JSON object into a map cannot fail
	// either. No reachable path publishes an empty payload (constitution 七).
	payload := map[string]any{}
	if raw, err := json.Marshal(delta); err == nil {
		_ = json.Unmarshal(raw, &payload)
	}
	_ = s.events.Publish(ctx, eventingapp.Event{
		Channel: "version:" + platformid.Format(versionID),
		Type:    "conversation.message.delta",
		Scope: map[string]any{
			"version_id":      platformid.Format(versionID),
			"conversation_id": platformid.Format(conversationID),
		},
		Payload:   payload,
		RequestID: requestid.From(ctx),
	})
}

// streamDelta derives the delta mode from the previous stored part snapshot and
// the new full snapshot. text/thinking that purely extends the previous text OF
// THE SAME TYPE is an append increment (offset = UTF-16 length of the previous
// text); every other case is an explicit full-snapshot replacement — never a
// silently corrupted suffix. In particular a same-seq part-type change (the
// repository accepts it via ON CONFLICT SET part_type) or a previous snapshot
// that fails to decode is a replacement, NOT an append-from-empty: the frontend
// accumulates by (message_id, seq) regardless of type, so an append frame against
// a stale different-type text would be skipped or land in an undefined offset
// band (constitution 七, no silently unappliable delta on an accepted path).
func streamDelta(previous json.RawMessage, part version.MessagePart) (offset int, increment string, replacePart json.RawMessage) {
	if part.Type == version.MessagePartText || part.Type == version.MessagePartThinking {
		previousText := ""
		// No prior row: the first delta appends from empty. A prior row only
		// qualifies for append when it decodes to the SAME part type.
		sameType := len(previous) == 0
		if len(previous) > 0 {
			var old version.MessagePart
			if err := json.Unmarshal(previous, &old); err == nil && old.Type == part.Type {
				previousText = old.Text
				sameType = true
			}
		}
		if sameType && part.Text != previousText && strings.HasPrefix(part.Text, previousText) {
			return textunit.UTF16Length(previousText), part.Text[len(previousText):], nil
		}
	}
	raw, err := json.Marshal(part)
	if err != nil {
		// MessagePart.MarshalJSON only fails for an unknown type, which the
		// validator rejects before the update reaches here.
		return 0, "", json.RawMessage(`{"type":"` + part.Type + `"}`)
	}
	return 0, "", raw
}
