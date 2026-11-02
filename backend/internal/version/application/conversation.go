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
	models      ConversationModelCatalog
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

// SetModelCatalog wires the default LLM configuration's selectable model
// catalog. It is set by the composition root after construction; a nil catalog
// keeps conversation creation working but rejects model overrides, which is the
// safe behavior when no LLM context is configured.
func (s *ConversationService) SetModelCatalog(catalog ConversationModelCatalog) {
	s.models = catalog
}

// Create creates a clarification or design conversation and ensures the
// opening chat task. Creating an empty conversation does not project the
// version to running.
func (s *ConversationService) Create(ctx context.Context, versionID int64, kind, title string, actorID int64) (ConversationView, error) {
	return s.CreateWithModel(ctx, versionID, kind, title, nil, actorID)
}

// CreateWithModel is Create plus the optional 0.2.1 conversation-level model
// override. A non-empty override must name a selectable model of the default
// LLM configuration; omitting it (nil) uses the configuration's
// default_model_id and never switches models automatically (D24).
func (s *ConversationService) CreateWithModel(ctx context.Context, versionID int64, kind, title string, modelOverride *string, actorID int64) (ConversationView, error) {
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
	if err := version.ValidateConversationKind(kind); err != nil {
		return ConversationView{}, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		if kind == version.ConversationDesign {
			title = version.DefaultDesignConversationTitle(s.clock.Now())
		} else {
			title = version.DefaultConversationTitle(s.clock.Now())
		}
	}
	if err := version.ValidateConversationTitle(title); err != nil {
		return ConversationView{}, err
	}
	normalizedOverride, effectiveModel, err := s.resolveModelOverride(ctx, modelOverride)
	if err != nil {
		return ConversationView{}, err
	}

	now := s.clock.Now()
	conversation := version.Conversation{
		ID:            s.ids.Next(),
		VersionID:     versionID,
		Kind:          kind,
		Title:         title,
		Status:        version.ConversationActive,
		ModelOverride: normalizedOverride,
		CreatedBy:     optionalActor(actorID),
		CreatedAt:     now,
		UpdatedAt:     now,
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
		Team:      teamForConversationKind(kind),
		Type:      "chat.turn",
		DedupeKey: "opening:" + conversationIDString,
		Payload: map[string]any{
			"conversation_id": conversationIDString,
			"trigger":         "opening",
		},
	})
	view := toConversationView(conversation, nil)
	view.EffectiveModel = effectiveModel
	return view, nil
}

// SetModelOverride sets or clears one conversation's model override. A nil
// override clears it and falls back to the LLM configuration's default model;
// an explicit model must be selectable (the switch is user-initiated, never
// automatic, D24).
func (s *ConversationService) SetModelOverride(ctx context.Context, versionID, conversationID int64, modelOverride *string) (ConversationView, error) {
	if err := validateConversationIDs(versionID, conversationID); err != nil {
		return ConversationView{}, err
	}
	_, summary, err := s.repo.Get(ctx, versionID, conversationID)
	if err != nil {
		return ConversationView{}, mapConversationRepositoryError(err)
	}
	normalizedOverride, effectiveModel, err := s.resolveModelOverride(ctx, modelOverride)
	if err != nil {
		return ConversationView{}, err
	}
	conversation, err := s.repo.UpdateModelOverride(ctx, versionID, conversationID, normalizedOverride, s.clock.Now())
	if err != nil {
		return ConversationView{}, mapConversationRepositoryError(err)
	}
	view := toConversationView(conversation, summary)
	view.EffectiveModel = effectiveModel
	return view, nil
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
	resolveEffectiveModel := s.effectiveModelResolver(ctx)
	views := make([]ConversationView, 0, len(conversations))
	for _, conversation := range conversations {
		view := toConversationView(conversation, nil)
		view.EffectiveModel = resolveEffectiveModel(conversation.ModelOverride)
		views = append(views, view)
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
	view := toConversationView(conversation, summary)
	view.EffectiveModel = s.effectiveModel(ctx, conversation.ModelOverride)
	return view, nil
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

// MessageParts returns the append-only streaming parts of one UI message in
// seq order. The message must belong to the conversation and version; a
// foreign message is reported as a 404, never leaked across versions.
func (s *ConversationService) MessageParts(ctx context.Context, versionID, conversationID, messageID int64) ([]MessagePartView, error) {
	if err := validateConversationIDs(versionID, conversationID); err != nil {
		return nil, err
	}
	if messageID <= 0 {
		return nil, platform.Invalid("mid", "must be a positive decimal ID")
	}
	parts, err := s.repo.MessageParts(ctx, versionID, conversationID, messageID)
	if err != nil {
		return nil, mapConversationRepositoryError(err)
	}
	views := make([]MessagePartView, 0, len(parts))
	for _, part := range parts {
		views = append(views, toMessagePartView(part))
	}
	return views, nil
}

// PostUserMessage validates and stores one user message. A repeated
// client_message_id returns the already-stored message without new side
// effects. Attachment rows are pre-registered before the insert so the §8.5
// gate can observe a brand-new upload's extraction status; a rejected post may
// therefore leave an unlinked row, which the retry reuses (it can never leave a
// message row for a failed attachment).
func (s *ConversationService) PostUserMessage(ctx context.Context, versionID, conversationID int64, parts []version.MessagePart, clientMessageID string, actorID int64) (MessageView, error) {
	if err := validateConversationIDs(versionID, conversationID); err != nil {
		return MessageView{}, err
	}
	conversation, _, err := s.repo.Get(ctx, versionID, conversationID)
	if err != nil {
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
			s.ensureMessageTasks(ctx, versionID, conversation.Kind, conversationID, existing.ID)
			return toMessageView(*existing), nil
		}
		if err != nil && !errors.Is(err, version.ErrMessageNotFound) {
			return MessageView{}, mapConversationRepositoryError(err)
		}
	}

	// Attachment rows are pre-registered before the message is stored so a
	// brand-new upload can be gated on its real extraction status (spec §8.5):
	// if any is still parsing the typed 409 is returned here and no message row
	// is written. Image parts are already converted, and the returned attachment
	// IDs are the pre-registered rows linked after a successful insert.
	attachmentIDs := []int64{}
	if s.attachments != nil {
		prepared, ids, err := s.attachments.PrepareUserMessageParts(ctx, versionID, conversationID, parts, actorID)
		if err != nil {
			return MessageView{}, err
		}
		if err := version.ValidateUserMessageParts(prepared); err != nil {
			return MessageView{}, err
		}
		parts = prepared
		attachmentIDs = ids
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

	existing, created, err := s.repo.PostUserMessage(ctx, conversationID, message)
	if err != nil {
		return MessageView{}, mapConversationRepositoryError(err)
	}
	if !created && existing != nil {
		s.ensureMessageTasks(ctx, versionID, conversation.Kind, conversationID, existing.ID)
		return toMessageView(*existing), nil
	}

	if s.attachments != nil && len(attachmentIDs) > 0 {
		// The rows already exist (pre-registered during preflight with content
		// refs and ingest tasks); only the message linkage is missing. Linking is
		// best effort, like the previous post-insert registration, and avoids
		// opening the content or extracting it a second time.
		_ = s.attachments.LinkFromMessage(ctx, versionID, message.ID, attachmentIDs)
	}

	s.publishMessageEvent(ctx, versionID, message)
	s.markRunning(ctx, versionID)
	s.ensureMessageTasks(ctx, versionID, conversation.Kind, conversationID, message.ID)
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

func (s *ConversationService) ensureMessageTasks(ctx context.Context, versionID int64, kind string, conversationID, messageID int64) {
	conversationIDString := platformid.Format(conversationID)
	messageIDString := platformid.Format(messageID)
	team := teamForConversationKind(kind)
	s.enqueue(ctx, TaskInput{
		VersionID: versionID,
		Team:      team,
		Type:      "chat.turn",
		DedupeKey: "reply:" + messageIDString,
		Payload: map[string]any{
			"conversation_id": conversationIDString,
			"message_id":      messageIDString,
			"trigger":         "reply",
		},
	})
	// Design conversations are handled by the design team; requirement
	// curation belongs to clarification conversations only (P2 spec §9.1).
	if kind == version.ConversationDesign {
		return
	}
	s.enqueue(ctx, TaskInput{
		VersionID: versionID,
		Team:      team,
		Type:      "requirement.curate",
		DedupeKey: "curate:" + messageIDString,
		Payload: map[string]any{
			"conversation_id": conversationIDString,
			"message_id":      messageIDString,
		},
	})
}

// teamForConversationKind selects the work-item team for chat tasks.
func teamForConversationKind(kind string) string {
	if kind == version.ConversationDesign {
		return "design"
	}
	return "clarification"
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
	case version.MessagePartImage:
		payload["mime"] = part.MIME
		payload["data_ref"] = part.DataRef
		payload["width"] = part.Width
		payload["height"] = part.Height
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

// ---------- model override resolution ----------

// resolveModelOverride normalizes a requested override and returns both the
// value to persist and the effective model name. nil/blank clears the override
// and resolves the configuration default best effort; an explicit model must
// resolve through the catalog, whose typed error is returned unchanged.
func (s *ConversationService) resolveModelOverride(ctx context.Context, modelOverride *string) (*string, *string, error) {
	if modelOverride == nil || strings.TrimSpace(*modelOverride) == "" {
		return nil, s.effectiveModel(ctx, nil), nil
	}
	trimmed := strings.TrimSpace(*modelOverride)
	if len(trimmed) > 128 {
		return nil, nil, platform.Invalid("model_override", "must be at most 128 characters")
	}
	if s.models == nil {
		return nil, nil, platform.Unavailable("llm model catalog is not configured")
	}
	name, err := s.models.ResolveConversationModel(ctx, trimmed)
	if err != nil {
		return nil, nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil, platform.Invalid("model_override", "must name a model of the default llm configuration")
	}
	return &name, &name, nil
}

// effectiveModel resolves the model a conversation currently uses, best effort:
// read paths must render a conversation even when the LLM configuration is
// temporarily unusable, so a resolution failure yields null (contract nullable).
func (s *ConversationService) effectiveModel(ctx context.Context, override *string) *string {
	if s.models == nil {
		return nil
	}
	requested := ""
	if override != nil {
		requested = strings.TrimSpace(*override)
	}
	name, err := s.models.ResolveConversationModel(ctx, requested)
	if err != nil {
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	return &name
}

// effectiveModelResolver memoizes effectiveModel per requested override so a
// conversation page resolves the shared configuration default only once.
func (s *ConversationService) effectiveModelResolver(ctx context.Context) func(*string) *string {
	if s.models == nil {
		return func(*string) *string { return nil }
	}
	cache := make(map[string]*string)
	return func(override *string) *string {
		key := ""
		if override != nil {
			key = strings.TrimSpace(*override)
		}
		if value, ok := cache[key]; ok {
			return value
		}
		value := s.effectiveModel(ctx, override)
		cache[key] = value
		return value
	}
}
