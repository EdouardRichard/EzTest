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
		// A job card is a UI artifact of a background job, not a conversation
		// turn: the card message carries no text, so injecting it would add an
		// empty assistant turn to the Host/curator prompt (D25 keeps background
		// work out of the conversation context).
		if isJobCardOnlyMessage(message) {
			continue
		}
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

// ModelOverride returns the conversation-level 0.2.1 model override stored on
// the conversation ("" when unset). The runner reads it before model resolution
// so the user's last model switch applies to the next run. Ownership is
// enforced by the scoped Get.
func (a *Adapter) ModelOverride(ctx context.Context, versionID, conversationID int64) (string, error) {
	if a.conversations == nil {
		return "", a.missing("conversations")
	}
	conversation, err := a.conversations.Get(ctx, versionID, conversationID)
	if err != nil {
		return "", err
	}
	if conversation.ModelOverride == nil {
		return "", nil
	}
	return strings.TrimSpace(*conversation.ModelOverride), nil
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

// UpsertJobCard stores or refreshes the single job_card part of one background
// job in the conversation thread. The version context owns validation and the
// one-card-per-job idempotency; the adapter only maps the runtime part shape.
func (a *Adapter) UpsertJobCard(ctx context.Context, versionID, conversationID int64, part agentapp.ConversationPart) (agentapp.ConversationMessage, error) {
	if a.conversations == nil {
		return agentapp.ConversationMessage{}, a.missing("conversations")
	}
	if versionID <= 0 || conversationID <= 0 {
		return agentapp.ConversationMessage{}, platform.Invalid("conversation_id", "must be positive decimal IDs")
	}
	mapped, err := versionPart(part)
	if err != nil {
		return agentapp.ConversationMessage{}, err
	}
	if mapped.Type != version.MessagePartJobCard {
		return agentapp.ConversationMessage{}, platform.Invalid("type", "must be job_card")
	}
	view, _, err := a.conversations.UpsertJobCard(ctx, versionID, conversationID, mapped)
	if err != nil {
		return agentapp.ConversationMessage{}, err
	}
	return conversationMessage(view, versionID), nil
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

// isJobCardOnlyMessage reports whether a UI message exists only to carry a
// background job card. Those messages are UI artifacts, not conversation turns:
// the adapter keeps them out of the LLM history (an empty assistant turn would
// otherwise pollute the Host/curator prompt).
func isJobCardOnlyMessage(message versionapp.MessageView) bool {
	if message.Role != version.MessageRoleAssistant || len(message.Parts) != 1 {
		return false
	}
	return message.Parts[0].Type == version.MessagePartJobCard
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
	case version.MessagePartImage:
		mapped.Data = map[string]any{
			"mime":     part.MIME,
			"data_ref": part.DataRef,
			"width":    part.Width,
			"height":   part.Height,
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
	case version.MessagePartThinking:
		mapped.Data = map[string]any{"duration_ms": part.DurationMS}
	case version.MessagePartToolCall:
		mapped.Data = map[string]any{
			"call_id":     part.CallID,
			"name":        part.Name,
			"status":      part.Status,
			"arguments":   part.Arguments,
			"result":      part.Result,
			"duration_ms": part.DurationMS,
			"error":       part.Error,
		}
	case version.MessagePartJobCard:
		mapped.Data = map[string]any{
			"job_id":     platformid.Format(part.JobID),
			"kind":       part.JobKind,
			"status":     part.Status,
			"current":    part.Current,
			"total":      part.Total,
			"cancelable": part.Cancelable,
		}
	case version.MessagePartCaseCard:
		mapped.Data = map[string]any{
			"case_id":         platformid.Format(part.CaseID),
			"case_version_id": platformid.Format(part.CaseVersionID),
			"title":           part.Title,
			"status":          part.Status,
		}
	case version.MessagePartError:
		mapped.Data = map[string]any{
			"code":      part.Code,
			"message":   part.Message,
			"retryable": part.Retryable,
			"action":    part.Action,
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
	case version.MessagePartImage:
		mime, _ := dataString(part.Data, "mime")
		dataRef, _ := dataString(part.Data, "data_ref")
		width, _ := dataInt(part.Data, "width")
		height, _ := dataInt(part.Data, "height")
		return version.MessagePart{
			Type: part.Type, MIME: mime, DataRef: dataRef, Width: int(width), Height: int(height),
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
	case version.MessagePartThinking:
		duration, _ := dataInt(part.Data, "duration_ms")
		return version.MessagePart{Type: part.Type, Text: part.Text, DurationMS: duration}, nil
	case version.MessagePartToolCall:
		callID, _ := dataString(part.Data, "call_id")
		name, _ := dataString(part.Data, "name")
		status, _ := dataString(part.Data, "status")
		arguments, _ := dataRaw(part.Data, "arguments")
		result, _ := dataRaw(part.Data, "result")
		duration, _ := dataInt(part.Data, "duration_ms")
		errText, _ := dataString(part.Data, "error")
		return version.MessagePart{
			Type: part.Type, CallID: callID, Name: name, Status: status,
			Arguments: arguments, Result: result, DurationMS: duration, Error: errText,
		}, nil
	case version.MessagePartJobCard:
		jobID, _ := dataInt(part.Data, "job_id")
		kind, _ := dataString(part.Data, "kind")
		status, _ := dataString(part.Data, "status")
		current, _ := dataInt(part.Data, "current")
		total, _ := dataInt(part.Data, "total")
		return version.MessagePart{
			Type: part.Type, JobID: jobID, JobKind: kind, Status: status,
			Current: int(current), Total: int(total), Cancelable: dataBoolPtr(part.Data, "cancelable"),
		}, nil
	case version.MessagePartCaseCard:
		caseID, _ := dataInt(part.Data, "case_id")
		caseVersionID, _ := dataInt(part.Data, "case_version_id")
		title, _ := dataString(part.Data, "title")
		status, _ := dataString(part.Data, "status")
		return version.MessagePart{
			Type: part.Type, CaseID: caseID, CaseVersionID: caseVersionID, Title: title, Status: status,
		}, nil
	case version.MessagePartError:
		code, _ := dataString(part.Data, "code")
		message, _ := dataString(part.Data, "message")
		retryable, _ := dataBool(part.Data, "retryable")
		action, _ := dataString(part.Data, "action")
		return version.MessagePart{
			Type: part.Type, Code: code, Message: message, Retryable: retryable, Action: action,
		}, nil
	default:
		return version.MessagePart{}, platform.Invalid("type",
			"must be text, thinking, tool_call, job_card, attachment, image, requirement_card, case_card, error or rollback_proposal")
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

// dataRaw reads an object-shaped JSON field carried either as a decoded map or
// as raw JSON (the runner stores json.RawMessage for tool arguments/results).
func dataRaw(data map[string]any, key string) (json.RawMessage, bool) {
	if data == nil {
		return nil, false
	}
	value, ok := data[key]
	if !ok || value == nil {
		return nil, false
	}
	switch typed := value.(type) {
	case json.RawMessage:
		if len(typed) == 0 {
			return nil, false
		}
		return typed, true
	case []byte:
		if len(typed) == 0 {
			return nil, false
		}
		return json.RawMessage(typed), true
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return nil, false
		}
		return json.RawMessage(trimmed), true
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return nil, false
		}
		return json.RawMessage(encoded), true
	}
}

func dataBool(data map[string]any, key string) (bool, bool) {
	if data == nil {
		return false, false
	}
	value, ok := data[key]
	if !ok || value == nil {
		return false, false
	}
	typed, ok := value.(bool)
	return typed, ok
}

func dataBoolPtr(data map[string]any, key string) *bool {
	value, ok := dataBool(data, key)
	if !ok {
		return nil
	}
	return &value
}
