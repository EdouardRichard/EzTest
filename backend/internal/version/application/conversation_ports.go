package versionapp

import (
	"context"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// TaskInput is the local work-queue contract consumed by the version context.
// It mirrors the P1 work_item dedupe semantics without importing the agent
// context: the Lead adapts it to the agent work-queue service in T13.
type TaskInput struct {
	VersionID int64
	Team      string
	Type      string
	DedupeKey string
	Payload   map[string]any
	Priority  int
}

// TaskEnqueuer ensures one work item exists. It must be idempotent on
// (version_id, type, dedupe_key): callers treat enqueue as best effort.
type TaskEnqueuer interface {
	Ensure(ctx context.Context, task TaskInput) error
}

// MessageAttachmentRegistrar validates a message attachment part against the
// content ledger, converts image attachments into image parts before the
// message is stored, pre-registers/reuses the requirement_attachment runtime
// rows, and enforces the §8.5 "done or parsing" submit gate. The attachment
// service implements it.
type MessageAttachmentRegistrar interface {
	// PrepareUserMessageParts converts image attachment parts into image parts,
	// pre-registers (or reuses) the attachment rows for the message's attachment
	// parts and returns the typed 409 attachment_pending when any referenced
	// attachment is still parsing. attachmentIDs are the unlinked rows that
	// must be linked to the message once the insert wins.
	PrepareUserMessageParts(ctx context.Context, versionID, conversationID int64, parts []version.MessagePart, actorID int64) ([]version.MessagePart, []int64, error)
	// LinkFromMessage binds pre-registered attachment rows to the inserted
	// message without re-reading the content or re-running extraction.
	LinkFromMessage(ctx context.Context, versionID, messageID int64, attachmentIDs []int64) error
}

// ConversationModelCatalog resolves a conversation-level model override against
// the default LLM configuration's selectable catalog. An empty override resolves
// the configuration's explicit default_model_id (models[0] is never used,
// D24). The LLM application service backs it in production.
type ConversationModelCatalog interface {
	ResolveConversationModel(ctx context.Context, modelOverride string) (string, error)
}
