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
// message is stored, guards pending references, and persists the
// requirement_attachment runtime row. The attachment service implements it;
// conversation messages never block on extraction.
type MessageAttachmentRegistrar interface {
	// PrepareUserMessageParts converts image attachment parts into image parts
	// and returns the typed 409 attachment_pending when the message references
	// an attachment that is still parsing.
	PrepareUserMessageParts(ctx context.Context, versionID, conversationID int64, parts []version.MessagePart) ([]version.MessagePart, error)
	RegisterFromMessage(ctx context.Context, versionID, conversationID, messageID int64, part version.MessagePart, actorID int64) (AttachmentView, error)
}
