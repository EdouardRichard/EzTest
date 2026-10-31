package versionapp

import (
	"context"
	"errors"
	"strings"
	"time"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ConversationService implements clarification conversations, message
// posting and the thread summary. Task enqueue and attachment registration are
// best-effort side effects; the reconciler is the fallback.
type ConversationService struct {
	repo        version.ConversationRepository
	queue       TaskEnqueuer
	attachments MessageAttachmentRegistrar
	versions    VersionReader
	ids         platformid.Generator
	clock       clock.Clock
	events      eventingapp.Publisher
}

// NewConversationService wires the conversation use cases.
func NewConversationService(
	repo version.ConversationRepository,
	queue TaskEnqueuer,
	attachments MessageAttachmentRegistrar,
	versions VersionReader,
	clk clock.Clock,
	ids platformid.Generator,
	events eventingapp.Publisher,
) *ConversationService {
	return &ConversationService{
		repo: repo, queue: queue, attachments: attachments, versions: versions,
		clock: clk, ids: ids, events: events,
	}
}

// Create creates a clarification conversation and ensures the opening chat
// task. Creating an empty conversation does not project the version to
// running.
func (s *ConversationService) Create(ctx context.Context, versionID int64, kind, title string, actorID int64) (ConversationView, error) {
	if versionID <= 0 {
		return ConversationView{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	if _, err := s.versions.Get(ctx, versionID); err != nil {
		return ConversationView{}, err
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = version.ConversationClarification
	}
	if kind != version.ConversationClarification {
		return ConversationView{}, platform.Invalid("kind", "P1 only supports clarification conversations")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = version.DefaultConversationTitle(s.clock.Now())
	}
	if err := version.ValidateConversationTitle(title); err != nil {
		return ConversationView{}, err
	}

	now := s.clock.Now()
	conversation := version.Conversation{
		ID:        s.ids.Next(),
		VersionID: versionID,
		Kind:      kind,
		Title:     title,
		Status:    version.ConversationActive,
		CreatedBy: optionalActor(actorID),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.repo.Create(ctx, conversation); err != nil {
		return ConversationView{}, mapConversationRepositoryError(err)
	}

	conversationIDString := platformid.Format(conversation.ID)
	s.publishEvent(ctx, versionID, "conversation.created", map[string]any{
		"conversation_id": conversationIDString,
		"title":           conversation.Title,
		"kind":            conversation.Kind,
	}, map[string]any{"conversation_id": conversationIDString})
	s.enqueue(ctx, TaskInput{
		VersionID: versionID,
		Team:      "clarification",
		Type:      "chat.turn",
		DedupeKey: "opening:" + conversationIDString,
		Payload: map[string]any{
			"conversation_id": conversationIDString,
			"trigger":         "opening",
		},
	})
	return toConversationView(conversation, nil), nil
}

// List returns one id-descending page of conversations.
func (s *ConversationService) List(ctx context.Context, versionID int64, kind string, limit int, beforeID *int64) ([]ConversationView, bool, error) {
	if versionID <= 0 {
		return nil, false, platform.Invalid("id", "must be a positive decimal ID")
	}
	if _, err := s.versions.Get(ctx, versionID); err != nil {
		return nil, false, err
	}
	kind = strings.TrimSpace(kind)
	if kind != "" {
		if err := version.ValidateConversationKind(kind); err != nil {
			return nil, false, err
		}
	}
	limit = normalizeLimit(limit)
	conversations, err := s.repo.List(ctx, versionID, kind, limit+1, beforeID)
	if err != nil {
		return nil, false, mapConversationRepositoryError(err)
	}
	hasMore := len(conversations) > limit
	if hasMore {
		conversations = conversations[:limit]
	}
	views := make([]ConversationView, 0, len(conversations))
	for _, conversation := range conversations {
		views = append(views, toConversationView(conversation, nil))
	}
	return views, hasMore, nil
}

// Get returns one conversation detail with its latest summary text.
func (s *ConversationService) Get(ctx context.Context, versionID, conversationID int64) (ConversationView, error) {
	if err := validateConversationIDs(versionID, conversationID); err != nil {
		return ConversationView{}, err
	}
	conversation, summary, err := s.repo.Get(ctx, versionID, conversationID)
	if err != nil {
		return ConversationView{}, mapConversationRepositoryError(err)
	}
	return toConversationView(conversation, summary), nil
}

// Messages returns messages with id > after in ascending order. The default
// page size is 100 and the maximum is 200.
func (s *ConversationService) Messages(ctx context.Context, versionID, conversationID int64, after int64, limit int) ([]MessageView, bool, error) {
	if err := validateConversationIDs(versionID, conversationID); err != nil {
		return nil, false, err
	}
	if after < 0 {
		return nil, false, platform.Invalid("after", "must be a zero or positive decimal ID")
	}
	if _, _, err := s.repo.Get(ctx, versionID, conversationID); err != nil {
		return nil, false, mapConversationRepositoryError(err)
	}
	if limit <= 0 {
		limit = version.MessagesDefaultLimit
	}
	if limit > version.MessagesMaxLimit {
		limit = version.MessagesMaxLimit
	}
	messages, err := s.repo.Messages(ctx, conversationID, after, limit+1)
	if err != nil {
		return nil, false, mapConversationRepositoryError(err)
	}
	hasMore := len(messages) > limit
	if hasMore {
		messages = messages[:limit]
	}
	views := make([]MessageView, 0, len(messages))
	for _, message := range messages {
		views = append(views, toMessageView(message))
	}
	return views, hasMore, nil
}

// PostUserMessage validates and stores one user message. A repeated
// client_message_id returns the already-stored message without new side
// effects; attachment registration happens before the insert so message parts
// stay self-describing.
func (s *ConversationService) PostUserMessage(ctx context.Context, versionID, conversationID int64, parts []version.MessagePart, clientMessageID string, actorID int64) (MessageView, error) {
	if err := validateConversationIDs(versionID, conversationID); err != nil {
		return MessageView{}, err
	}
	if _, _, err := s.repo.Get(ctx, versionID, conversationID); err != nil {
		return MessageView{}, mapConversationRepositoryError(err)
	}
	clientMessageID = strings.TrimSpace(clientMessageID)
	if err := version.ValidateClientMessageID(clientMessageID); err != nil {
		return MessageView{}, err
	}
	if err := version.ValidateUserMessageParts(parts); err != nil {
		return MessageView{}, err
	}

	if clientMessageID != "" {
		existing, err := s.repo.FindMessageByClientID(ctx, conversationID, clientMessageID)
		if err == nil && existing != nil {
			s.ensureMessageTasks(ctx, versionID, conversationID, existing.ID)
			return toMessageView(*existing), nil
		}
		if err != nil && !errors.Is(err, version.ErrMessageNotFound) {
			return MessageView{}, mapConversationRepositoryError(err)
		}
	}

	now := s.clock.Now()
	message := version.Message{
		ID:             s.ids.Next(),
		ConversationID: conversationID,
		Role:           version.MessageRoleUser,
		Parts:          append([]version.MessagePart{}, parts...),
		Refs:           []version.MessageRef{},
		TokenUsage:     map[string]any{},
		ActorID:        optionalActor(actorID),
		CreatedAt:      now,
	}
	if clientMessageID != "" {
		value := clientMessageID
		message.ClientMessageID = &value
	}

	if s.attachments != nil {
		for _, part := range parts {
			if part.Type != version.MessagePartAttachment {
				continue
			}
			if _, err := s.attachments.RegisterFromMessage(ctx, versionID, message.ID, part, actorID); err != nil {
				return MessageView{}, err
			}
		}
	}

	existing, created, err := s.repo.PostUserMessage(ctx, conversationID, message)
	if err != nil {
		return MessageView{}, mapConversationRepositoryError(err)
	}
	if !created && existing != nil {
		s.ensureMessageTasks(ctx, versionID, conversationID, existing.ID)
		return toMessageView(*existing), nil
	}

	s.publishMessageEvent(ctx, versionID, message)
	s.markRunning(ctx, versionID)
	s.ensureMessageTasks(ctx, versionID, conversationID, message.ID)
	return toMessageView(message), nil
}

// AppendAssistantMessage stores a runtime-authored assistant message and
// projects the version to running (the first assistant message counts as
// conversation activity).
func (s *ConversationService) AppendAssistantMessage(ctx context.Context, conversationID int64, parts []version.MessagePart, runID int64) (MessageView, error) {
	return s.AppendAssistantMessageWithReply(ctx, conversationID, parts, runID, nil)
}

// AppendAssistantMessageWithReply is AppendAssistantMessage plus the optional
// UI reply target (the Host assistant answers a specific user message).
func (s *ConversationService) AppendAssistantMessageWithReply(ctx context.Context, conversationID int64, parts []version.MessagePart, runID int64, replyToMessageID *int64) (MessageView, error) {
	if conversationID <= 0 {
		return MessageView{}, platform.Invalid("cid", "must be a positive decimal ID")
	}
	if replyToMessageID != nil && *replyToMessageID <= 0 {
		return MessageView{}, platform.Invalid("reply_to_message_id", "must be a positive decimal ID")
	}
	conversation, err := s.repo.GetByID(ctx, conversationID)
	if err != nil {
		return MessageView{}, mapConversationRepositoryError(err)
	}
	if err := version.ValidateAssistantMessageParts(parts); err != nil {
		return MessageView{}, err
	}

	now := s.clock.Now()
	message := version.Message{
		ID:               s.ids.Next(),
		ConversationID:   conversationID,
		Role:             version.MessageRoleAssistant,
		Parts:            append([]version.MessagePart{}, parts...),
		Refs:             []version.MessageRef{},
		TokenUsage:       map[string]any{},
		ReplyToMessageID: replyToMessageID,
		CreatedAt:        now,
	}
	if runID > 0 {
		value := runID
		message.AgentRunID = &value
	}
	if err := s.repo.AppendAssistantMessage(ctx, conversationID, message); err != nil {
		return MessageView{}, mapConversationRepositoryError(err)
	}
	s.publishMessageEvent(ctx, conversation.VersionID, message)
	s.markRunning(ctx, conversation.VersionID)
	return toMessageView(message), nil
}

// ---------- tasks and events ----------

func (s *ConversationService) ensureMessageTasks(ctx context.Context, versionID, conversationID, messageID int64) {
	conversationIDString := platformid.Format(conversationID)
	messageIDString := platformid.Format(messageID)
	s.enqueue(ctx, TaskInput{
		VersionID: versionID,
		Team:      "clarification",
		Type:      "chat.turn",
		DedupeKey: "reply:" + messageIDString,
		Payload: map[string]any{
			"conversation_id": conversationIDString,
			"message_id":      messageIDString,
			"trigger":         "reply",
		},
	})
	s.enqueue(ctx, TaskInput{
		VersionID: versionID,
		Team:      "clarification",
		Type:      "requirement.curate",
		DedupeKey: "curate:" + messageIDString,
		Payload: map[string]any{
			"conversation_id": conversationIDString,
			"message_id":      messageIDString,
		},
	})
}

func (s *ConversationService) enqueue(ctx context.Context, task TaskInput) {
	if s.queue == nil {
		return
	}
	_ = s.queue.Ensure(ctx, task)
}

func (s *ConversationService) markRunning(ctx context.Context, versionID int64) {
	_ = s.versions.MarkRunning(ctx, versionID)
}

func (s *ConversationService) publishMessageEvent(ctx context.Context, versionID int64, message version.Message) {
	if s.events == nil {
		return
	}
	parts := make([]any, 0, len(message.Parts))
	for _, part := range message.Parts {
		parts = append(parts, partPayload(part))
	}
	refs := make([]map[string]any, 0, len(message.Refs))
	for _, ref := range message.Refs {
		refs = append(refs, map[string]any{"kind": ref.Kind, "id": ref.ID})
	}
	payload := map[string]any{
		"id":              platformid.Format(message.ID),
		"conversation_id": platformid.Format(message.ConversationID),
		"role":            message.Role,
		"parts":           parts,
		"refs":            refs,
		"created_at":      message.CreatedAt.UTC().Format(time.RFC3339),
	}
	_ = s.events.Publish(ctx, eventingapp.Event{
		Channel: "version:" + platformid.Format(versionID),
		Type:    "conversation.message",
		Scope: map[string]any{
			"version_id":      platformid.Format(versionID),
			"conversation_id": platformid.Format(message.ConversationID),
		},
		Payload:   payload,
		RequestID: requestid.From(ctx),
	})
}

// partPayload renders the canonical part map without a JSON round trip.
func partPayload(part version.MessagePart) map[string]any {
	payload := map[string]any{"type": part.Type}
	switch part.Type {
	case version.MessagePartText:
		payload["text"] = part.Text
	case version.MessagePartAttachment:
		payload["sha256"] = part.SHA256
		payload["name"] = part.Name
		payload["mime"] = part.MIME
		payload["size"] = part.Size
	case version.MessagePartRequirementCard:
		payload["requirement_id"] = platformid.Format(part.RequirementID)
		payload["title"] = part.Title
		payload["status"] = part.Status
		payload["revision"] = part.Revision
	case version.MessagePartRollbackProposal:
		payload["requirement_id"] = platformid.Format(part.RequirementID)
		payload["reason"] = part.Reason
	}
	return payload
}

func (s *ConversationService) publishEvent(ctx context.Context, versionID int64, eventType string, payload, scope map[string]any) {
	if s.events == nil {
		return
	}
	mergedScope := map[string]any{"version_id": platformid.Format(versionID)}
	for key, value := range scope {
		mergedScope[key] = value
	}
	_ = s.events.Publish(ctx, eventingapp.Event{
		Channel:   "version:" + platformid.Format(versionID),
		Type:      eventType,
		Scope:     mergedScope,
		Payload:   payload,
		RequestID: requestid.From(ctx),
	})
}

// ---------- errors and helpers ----------

func mapConversationRepositoryError(err error) error {
	switch {
	case errors.Is(err, version.ErrConversationNotFound):
		return platform.NotFound("conversation")
	case errors.Is(err, version.ErrMessageNotFound):
		return platform.NotFound("message")
	default:
		return err
	}
}

func validateConversationIDs(versionID, conversationID int64) error {
	if versionID <= 0 {
		return platform.Invalid("id", "must be a positive decimal ID")
	}
	if conversationID <= 0 {
		return platform.Invalid("cid", "must be a positive decimal ID")
	}
	return nil
}
