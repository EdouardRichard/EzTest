package versionapp

import (
	"time"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ConversationView is the conversation detail. Summary is the latest summary
// text (nil until the runtime generates one); summaries are also exposed via
// the service port for the agent runtime.
type ConversationView struct {
	ID            int64
	VersionID     int64
	Kind          string
	Title         string
	Status        string
	MessageCount  int
	LastMessageAt *time.Time
	CreatedBy     *int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
	Summary       *string
}

// MessageView is the UI-thread message projection.
type MessageView struct {
	ID             int64
	ConversationID int64
	Role           string
	Parts          []version.MessagePart
	Refs           []version.MessageRef
	CreatedAt      time.Time
}

// SummaryView is the latest thread memory of one conversation.
type SummaryView struct {
	ConversationID int64
	Summary        string
	PinnedFacts    []string
	TokenEstimate  int
	Version        int
	UpdatedAt      time.Time
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
	return MessageView{
		ID:             message.ID,
		ConversationID: message.ConversationID,
		Role:           message.Role,
		Parts:          nonNilParts(message.Parts),
		Refs:           nonNilRefs(message.Refs),
		CreatedAt:      message.CreatedAt,
	}
}

func nonNilParts(parts []version.MessagePart) []version.MessagePart {
	if parts == nil {
		return []version.MessagePart{}
	}
	return parts
}

func nonNilRefs(refs []version.MessageRef) []version.MessageRef {
	if refs == nil {
		return []version.MessageRef{}
	}
	return refs
}
