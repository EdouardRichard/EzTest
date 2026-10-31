package versionadapter

import (
	"context"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
)

// ListConversations implements agentapp.ConversationReader for the P1
// reconciler. The version service selects the newest conversations with their
// messages in ID order; the adapter copies them into the agent-context types
// (they are deliberately not type aliases so the agent API stays decoupled).
func (a *Adapter) ListConversations(ctx context.Context, limit int) ([]agentapp.ReconcilerConversation, error) {
	if a.conversations == nil {
		return nil, a.missing("conversations")
	}
	items, err := a.conversations.ListReconcilable(ctx, limit)
	if err != nil {
		return nil, err
	}
	conversations := make([]agentapp.ReconcilerConversation, 0, len(items))
	for _, item := range items {
		messages := make([]agentapp.ReconcilerMessage, 0, len(item.Messages))
		for _, message := range item.Messages {
			messages = append(messages, agentapp.ReconcilerMessage{
				ID:              message.ID,
				Role:            message.Role,
				AgentProfileKey: message.AgentProfileKey,
			})
		}
		conversations = append(conversations, agentapp.ReconcilerConversation{
			ID:        item.ID,
			VersionID: item.VersionID,
			Kind:      item.Kind,
			Messages:  messages,
		})
	}
	return conversations, nil
}

// ListPendingAttachments implements agentapp.PendingAttachmentReader for the
// P1 reconciler. Unlinked attachments keep zero conversation/message IDs.
func (a *Adapter) ListPendingAttachments(ctx context.Context, limit int) ([]agentapp.PendingAttachment, error) {
	if a.attachments == nil {
		return nil, a.missing("attachments")
	}
	items, err := a.attachments.ListPendingForIngest(ctx, limit)
	if err != nil {
		return nil, err
	}
	pending := make([]agentapp.PendingAttachment, 0, len(items))
	for _, item := range items {
		pending = append(pending, agentapp.PendingAttachment{
			ID:             item.ID,
			VersionID:      item.VersionID,
			ConversationID: item.ConversationID,
			MessageID:      item.MessageID,
		})
	}
	return pending, nil
}
