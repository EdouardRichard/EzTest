package versionapp

import (
	"encoding/json"
	"time"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ConversationView is the conversation detail. Summary is the latest summary
// text (nil until the runtime generates one); summaries are also exposed via
// the service port for the agent runtime. ModelOverride is the stored 0.2.1
// conversation-level model (nil = config default) and EffectiveModel is the
// model a new assistant turn would use today.
type ConversationView struct {
	ID             int64
	VersionID      int64
	Kind           string
	Title          string
	Status         string
	MessageCount   int
	LastMessageAt  *time.Time
	ModelOverride  *string
	EffectiveModel *string
	CreatedBy      *int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Summary        *string
}

// MessageView is the UI-thread message projection. ActualModel is set for
// assistant messages from the linked agent_run (`actual_model`). Kind is the
// DB-3 conversation_message kind (text/question/answer); question/answer cards
// use it to render the structured batch, and empty keeps the legacy text row.
type MessageView struct {
	ID             int64
	ConversationID int64
	Role           string
	Kind           string
	Parts          []version.MessagePart
	Refs           []version.MessageRef
	ActualModel    *string
	CreatedAt      time.Time
}

// MessagePartView is one append-only streaming part row of a message. Payload
// is forwarded verbatim: the read API does not interpret the part kind.
type MessagePartView struct {
	Seq       int
	Type      string
	Payload   json.RawMessage
	CreatedAt time.Time
}

// SummaryView is the latest thread memory of one conversation.
type SummaryView struct {
	ConversationID   int64
	Summary          string
	PinnedFacts      []string
	TokenEstimate    int
	Version          int
	CoveredMessageID int64
	CompactionID     string
	SummaryMeta      map[string]any
	UpdatedAt        time.Time
}

func toConversationView(conversation version.Conversation, summary *version.ConversationSummary) ConversationView {
	view := ConversationView{
		ID:            conversation.ID,
		VersionID:     conversation.VersionID,
		Kind:          conversation.Kind,
		Title:         conversation.Title,
		Status:        conversation.Status,
		MessageCount:  conversation.MessageCount,
		LastMessageAt: conversation.LastMessageAt,
		ModelOverride: conversation.ModelOverride,
		CreatedBy:     conversation.CreatedBy,
		CreatedAt:     conversation.CreatedAt,
		UpdatedAt:     conversation.UpdatedAt,
	}
	if summary != nil {
		text := summary.Summary
		view.Summary = &text
	}
	return view
}

func toMessageView(message version.Message) MessageView {
	view := MessageView{
		ID:             message.ID,
		ConversationID: message.ConversationID,
		Role:           message.Role,
		Kind:           message.Kind,
		Parts:          nonNilParts(message.Parts),
		Refs:           nonNilRefs(message.Refs),
		CreatedAt:      message.CreatedAt,
	}
	if message.ActualModel != "" {
		actualModel := message.ActualModel
		view.ActualModel = &actualModel
	}
	return view
}

func nonNilParts(parts []version.MessagePart) []version.MessagePart {
	if parts == nil {
		return []version.MessagePart{}
	}
	return parts
}

func toMessagePartView(part version.ConversationMessagePart) MessagePartView {
	return MessagePartView{
		Seq:       part.Seq,
		Type:      part.Type,
		Payload:   part.Payload,
		CreatedAt: part.CreatedAt,
	}
}

func nonNilRefs(refs []version.MessageRef) []version.MessageRef {
	if refs == nil {
		return []version.MessageRef{}
	}
	return refs
}
