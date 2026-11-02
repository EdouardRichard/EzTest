package versionadapter

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

const (
	messagePageLimit = 200
	messageMaxRows   = 2000
)

// Snapshot loads conversation memory: latest rolling summary, pinned facts and
// the conversation messages up to the safety cap. The context builder owns the
// keep-recent split so older messages can be folded into the rolling summary
// instead of being silently dropped by the adapter.
func (a *Adapter) Snapshot(ctx context.Context, versionID, conversationID int64, _ int) (agentapp.ConversationSnapshot, error) {
	if a.conversations == nil {
		return agentapp.ConversationSnapshot{}, a.missing("conversations")
	}
	conversation, err := a.conversations.Get(ctx, versionID, conversationID)
	if err != nil {
		return agentapp.ConversationSnapshot{}, err
	}
	summary, err := a.conversations.Summary(ctx, conversationID)
	if err != nil {
		return agentapp.ConversationSnapshot{}, err
	}
	messages, err := a.allMessages(ctx, versionID, conversationID)
	if err != nil {
		return agentapp.ConversationSnapshot{}, err
	}
	portMessages := make([]agentapp.ConversationMessage, 0, len(messages))
	for _, message := range messages {
		portMessages = append(portMessages, conversationMessage(message, versionID))
	}
	summaryText := summary.Summary
	if summaryText == "" && conversation.Summary != nil {
		summaryText = *conversation.Summary
	}
	pinned := summary.PinnedFacts
	if pinned == nil {
		pinned = []string{}
	}
	return agentapp.ConversationSnapshot{
		ID:          conversation.ID,
		VersionID:   versionID,
		Kind:        conversation.Kind,
		Summary:     summaryText,
		PinnedFacts: pinned,
		Messages:    portMessages,
	}, nil
}

// ConversationKind resolves the conversation kind for chat.turn Host selection
// (clarification vs design). Ownership is enforced by the scoped Get.
func (a *Adapter) ConversationKind(ctx context.Context, versionID, conversationID int64) (string, error) {
	if a.conversations == nil {
		return "", a.missing("conversations")
	}
	conversation, err := a.conversations.Get(ctx, versionID, conversationID)
	if err != nil {
		return "", err
	}
	return conversation.Kind, nil
}

// GetMessage returns one UI-thread message, scoped to its conversation/version.
func (a *Adapter) GetMessage(ctx context.Context, versionID, conversationID, messageID int64) (agentapp.ConversationMessage, error) {
	if a.conversations == nil {
		return agentapp.ConversationMessage{}, a.missing("conversations")
	}
	if messageID <= 0 {
		return agentapp.ConversationMessage{}, platform.Invalid("message_id", "must be a positive decimal ID")
	}
	views, _, err := a.conversations.Messages(ctx, versionID, conversationID, messageID-1, 1)
	if err != nil {
		return agentapp.ConversationMessage{}, err
	}
	if len(views) == 0 || views[0].ID != messageID {
		return agentapp.ConversationMessage{}, platform.NotFound("message")
	}
	return conversationMessage(views[0], versionID), nil
}

// AppendAssistantMessage stores a runtime-authored assistant message, including
// the reply-to pointer when the runtime replies to a user message.
func (a *Adapter) AppendAssistantMessage(ctx context.Context, in agentapp.AssistantMessageInput) (agentapp.ConversationMessage, error) {
	if a.conversations == nil {
		return agentapp.ConversationMessage{}, a.missing("conversations")
	}
	if in.VersionID <= 0 || in.ConversationID <= 0 {
		return agentapp.ConversationMessage{}, platform.Invalid("conversation_id", "must be positive decimal IDs")
	}
	if err := a.conversationOwned(ctx, in.VersionID, in.ConversationID); err != nil {
		return agentapp.ConversationMessage{}, err
	}
	parts, err := versionParts(in.Parts)
	if err != nil {
		return agentapp.ConversationMessage{}, err
	}
	view, err := a.conversations.AppendAssistantMessageWithReply(
		ctx, in.ConversationID, parts, in.AgentRunID, in.ReplyToMessageID)
	if err != nil {
		return agentapp.ConversationMessage{}, err
	}
	message := conversationMessage(view, in.VersionID)
	message.ReplyToMessageID = in.ReplyToMessageID
	if in.AgentRunID > 0 {
		runID := in.AgentRunID
		message.AgentRunID = &runID
	}
	return message, nil
}

// UpdateSummary writes a new conversation summary revision and returns the
// stored version.
func (a *Adapter) UpdateSummary(ctx context.Context, versionID, conversationID int64, summary string, pinnedFacts []string) (agentapp.ConversationSummary, error) {
	if a.conversations == nil {
		return agentapp.ConversationSummary{}, a.missing("conversations")
	}
	if err := a.conversationOwned(ctx, versionID, conversationID); err != nil {
		return agentapp.ConversationSummary{}, err
	}
	if err := a.conversations.UpdateSummary(ctx, conversationID, summary, pinnedFacts); err != nil {
		return agentapp.ConversationSummary{}, err
	}
	stored, err := a.conversations.Summary(ctx, conversationID)
	if err != nil {
		return agentapp.ConversationSummary{}, err
	}
	pinned := stored.PinnedFacts
	if pinned == nil {
		pinned = []string{}
	}
	return agentapp.ConversationSummary{
		Version:     stored.Version,
		Summary:     stored.Summary,
		PinnedFacts: pinned,
		UpdatedAt:   stored.UpdatedAt,
	}, nil
}

func (a *Adapter) allMessages(ctx context.Context, versionID, conversationID int64) ([]versionapp.MessageView, error) {
	all := make([]versionapp.MessageView, 0)
	after := int64(0)
	for len(all) < messageMaxRows {
		views, hasMore, err := a.conversations.Messages(ctx, versionID, conversationID, after, messagePageLimit)
		if err != nil {
			return nil, err
		}
		all = append(all, views...)
		if !hasMore || len(views) == 0 {
			break
		}
		after = views[len(views)-1].ID
	}
	return all, nil
}

func conversationMessage(view versionapp.MessageView, versionID int64) agentapp.ConversationMessage {
	texts := make([]string, 0)
	parts := make([]agentapp.ConversationPart, 0, len(view.Parts))
	for _, part := range view.Parts {
		mapped := conversationPart(part)
		parts = append(parts, mapped)
		if mapped.Type == version.MessagePartText && mapped.Text != "" {
			texts = append(texts, mapped.Text)
		}
	}
	return agentapp.ConversationMessage{
		ID:             view.ID,
		VersionID:      versionID,
		ConversationID: view.ConversationID,
		Role:           view.Role,
		Text:           strings.Join(texts, "\n"),
		Parts:          parts,
		CreatedAt:      view.CreatedAt,
	}
}

func conversationPart(part version.MessagePart) agentapp.ConversationPart {
	mapped := agentapp.ConversationPart{Type: part.Type, Text: part.Text}
	switch part.Type {
	case version.MessagePartAttachment:
		mapped.Data = map[string]any{
			"sha256": part.SHA256,
			"name":   part.Name,
			"mime":   part.MIME,
			"size":   part.Size,
		}
	case version.MessagePartRequirementCard:
		mapped.Data = map[string]any{
			"requirement_id": platformid.Format(part.RequirementID),
			"title":          part.Title,
			"status":         part.Status,
			"revision":       part.Revision,
		}
	case version.MessagePartRollbackProposal:
		mapped.Data = map[string]any{
			"requirement_id": platformid.Format(part.RequirementID),
			"reason":         part.Reason,
		}
	}
	return mapped
}

func versionParts(parts []agentapp.ConversationPart) ([]version.MessagePart, error) {
	out := make([]version.MessagePart, 0, len(parts))
	for index, part := range parts {
		mapped, err := versionPart(part)
		if err != nil {
			return nil, platform.Invalid("parts["+strconv.Itoa(index)+"]", err.Error())
		}
		out = append(out, mapped)
	}
	return out, nil
}

func versionPart(part agentapp.ConversationPart) (version.MessagePart, error) {
	switch part.Type {
	case version.MessagePartText:
		return version.MessagePart{Type: part.Type, Text: part.Text}, nil
	case version.MessagePartAttachment:
		sha256, _ := dataString(part.Data, "sha256")
		name, _ := dataString(part.Data, "name")
		mime, _ := dataString(part.Data, "mime")
		size, _ := dataInt(part.Data, "size")
		return version.MessagePart{
			Type: part.Type, SHA256: sha256, Name: name, MIME: mime, Size: size,
		}, nil
	case version.MessagePartRequirementCard:
		requirementID, ok := dataInt(part.Data, "requirement_id")
		if !ok || requirementID <= 0 {
			return version.MessagePart{}, platform.Invalid("requirement_id", "must be a positive decimal ID")
		}
		title, _ := dataString(part.Data, "title")
		status, _ := dataString(part.Data, "status")
		revision, _ := dataInt(part.Data, "revision")
		return version.MessagePart{
			Type: part.Type, RequirementID: requirementID, Title: title,
			Status: status, Revision: int(revision),
		}, nil
	case version.MessagePartRollbackProposal:
		requirementID, ok := dataInt(part.Data, "requirement_id")
		if !ok || requirementID <= 0 {
			return version.MessagePart{}, platform.Invalid("requirement_id", "must be a positive decimal ID")
		}
		reason, _ := dataString(part.Data, "reason")
		return version.MessagePart{
			Type: part.Type, RequirementID: requirementID, Reason: reason,
		}, nil
	default:
		return version.MessagePart{}, platform.Invalid("type", "must be text, attachment, requirement_card or rollback_proposal")
	}
}

func dataString(data map[string]any, key string) (string, bool) {
	if data == nil {
		return "", false
	}
	value, ok := data[key]
	if !ok || value == nil {
		return "", false
	}
	switch typed := value.(type) {
	case string:
		return typed, true
	case json.Number:
		return typed.String(), true
	case float64:
		return strconv.FormatInt(int64(typed), 10), true
	case int:
		return strconv.Itoa(typed), true
	case int64:
		return strconv.FormatInt(typed, 10), true
	default:
		return "", false
	}
}

func dataInt(data map[string]any, key string) (int64, bool) {
	text, ok := dataString(data, key)
	if !ok {
		return 0, false
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}
