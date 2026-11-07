package versionapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// CodeLLMCapabilityMismatch is the stable, non-retryable code returned when the
// effective conversation model cannot serve a draft (an image without vision,
// Task 75). It mirrors the LLM application's code; the version context must not
// import the LLM context (Agent.md §6.9.7).
const CodeLLMCapabilityMismatch = "llm_capability_mismatch"

// Single-writer conversation error codes (design §4.10 R-B2-3/R-B2-4, B §5.1).
// The HTTP edge maps them to 409/403; the application returns the typed
// BusyError/NotWriterError so the mapping never has to parse strings.
const (
	CodeConversationBusy = "conversation_busy"
	CodeNotWriter        = "not_writer"
)

// NotWriterError reasons.
const (
	// NotWriterReasonNoLease means the call did not carry a Host writer
	// identity (a background job has no conversation.append capability).
	NotWriterReasonNoLease = "no_writer_lease"
	// NotWriterReasonLeaseLost means the caller's turn no longer owns a live
	// lease (expired or taken over).
	NotWriterReasonLeaseLost = "lease_lost"
	// NotWriterReasonServerWriter means a non-Host server path tried to append
	// assistant text while a Host turn owned the conversation.
	NotWriterReasonServerWriter = "background_writer"
)

// Writer identifies the single active Host turn that owns a conversation's
// writer lease. TurnID is the Host turn identifier; Agent is the author_agent
// profile key (for example clarification_host/design_host).
type Writer struct {
	TurnID int64
	Agent  string
}

// WriterLease is a granted single-writer lease.
type WriterLease struct {
	ConversationID int64
	TurnID         int64
	Agent          string
	ExpiresAt      time.Time
}

// BusyError is the typed 409 conversation_busy failure: another live Host turn
// holds the conversation's writer lease. It unwraps to
// version.ErrConversationBusy and to the platform error carrying the HTTP
// status/code/details.
type BusyError struct {
	ConversationID int64
	ActiveTurnID   *int64
	ActiveAgent    *string
	LeaseUntil     *time.Time
	api            *platform.Error
}

func (e *BusyError) Error() string {
	return CodeConversationBusy + ": conversation is being written by another host turn"
}

// Unwrap exposes the domain sentinel and the API error to errors.Is/As.
func (e *BusyError) Unwrap() []error {
	errs := []error{version.ErrConversationBusy}
	if e.api != nil {
		errs = append(errs, e.api)
	}
	return errs
}

func newBusyError(conversation version.Conversation) *BusyError {
	details := map[string]any{"conversation_id": platformid.Format(conversation.ID)}
	if conversation.ActiveTurnID != nil {
		details["active_turn_id"] = platformid.Format(*conversation.ActiveTurnID)
	}
	if conversation.ActiveAgent != nil && *conversation.ActiveAgent != "" {
		details["active_agent"] = *conversation.ActiveAgent
	}
	if conversation.LeaseUntil != nil {
		details["lease_until"] = conversation.LeaseUntil.UTC().Format(time.RFC3339)
	}
	apiErr := platform.NewError(http.StatusConflict, CodeConversationBusy,
		"conversation is being written by another host turn")
	apiErr.Details = details
	return &BusyError{
		ConversationID: conversation.ID,
		ActiveTurnID:   conversation.ActiveTurnID,
		ActiveAgent:    conversation.ActiveAgent,
		LeaseUntil:     conversation.LeaseUntil,
		api:            apiErr,
	}
}

// NotWriterError is the typed 403 not_writer failure: the caller is not the
// active writer of the conversation (no writer identity, a lost/expired lease,
// or a background path while a Host owns the conversation). It unwraps to
// version.ErrNotConversationWriter and to the platform error.
type NotWriterError struct {
	ConversationID int64
	TurnID         int64
	Agent          string
	Reason         string
	api            *platform.Error
}

func (e *NotWriterError) Error() string {
	return CodeNotWriter + ": not the active writer of the conversation"
}

// Unwrap exposes the domain sentinel and the API error to errors.Is/As.
func (e *NotWriterError) Unwrap() []error {
	errs := []error{version.ErrNotConversationWriter}
	if e.api != nil {
		errs = append(errs, e.api)
	}
	return errs
}

func newNotWriterError(conversationID, turnID int64, agent, reason string) *NotWriterError {
	details := map[string]any{
		"conversation_id": platformid.Format(conversationID),
		"reason":          reason,
	}
	if turnID > 0 {
		details["turn_id"] = platformid.Format(turnID)
	}
	if agent != "" {
		details["agent"] = agent
	}
	apiErr := platform.NewError(http.StatusForbidden, CodeNotWriter,
		"not the active writer of the conversation")
	apiErr.Details = details
	return &NotWriterError{
		ConversationID: conversationID, TurnID: turnID, Agent: agent, Reason: reason, api: apiErr,
	}
}

// ConversationService implements clarification conversations, message
// posting and the thread summary. Task enqueue and attachment registration are
// best-effort side effects; the reconciler is the fallback.
type ConversationService struct {
	repo        version.ConversationRepository
	queue       TaskEnqueuer
	attachments MessageAttachmentRegistrar
	versions    VersionReader
	models      ConversationModelCatalog
	clarified   ClarifiedRequirementCounter
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

// SetClarifiedRequirementCounter wires the clarified-requirement gate of design
// conversations. A nil counter keeps conversation creation working in degraded
// assemblies and unit tests.
func (s *ConversationService) SetClarifiedRequirementCounter(counter ClarifiedRequirementCounter) {
	s.clarified = counter
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
	// A design conversation is only meaningful when at least one requirement is
	// clarified. The empty-state warning was not enough: the "new conversation"
	// action still created a round, queued AI work and burned a model run before
	// the agent refused. Fail with a typed conflict instead (Task 77).
	if kind == version.ConversationDesign && s.clarified != nil {
		count, err := s.clarified.CountClarifiedRequirements(ctx, versionID)
		if err != nil {
			return ConversationView{}, err
		}
		if count == 0 {
			apiErr := platform.NewError(http.StatusConflict, "design_requirements_missing",
				"design requires at least one clarified requirement; confirm a requirement before starting the design conversation")
			apiErr.Details = map[string]any{
				"next_action": "confirm at least one requirement at the requirement gate, then start the design conversation",
			}
			return ConversationView{}, apiErr
		}
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

	// Task 75 / admin B1: an image draft pinned to a model without vision is
	// rejected with a typed 400 before the message row and the reply/curate jobs
	// exist. Dispatch would fail these jobs at zero steps, and the accept-then-
	// fail behavior is exactly the opaque 201 ghost send observed in the trial.
	// The gate runs after attachment preparation because the uploader submits an
	// image as an attachment that the registrar converts into an image part.
	if containsImageMessagePart(parts) {
		if err := s.validateConversationImageModel(ctx, conversation.ModelOverride); err != nil {
			return MessageView{}, err
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
		Kind:           version.MessageKindText,
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
//
// A background/server writer has no conversation.append capability while a
// Host turn owns the conversation (R-B2-4). This legacy path carries no writer
// identity, so it writes through the repository's fenced background append:
// the live-lease check and the insert are one locked transaction. The read
// below is only for the version projection; it must not be used as the fence,
// because a Host can acquire between that read and the write.
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
	streamParts, err := assistantStreamParts(parts, now)
	if err != nil {
		return MessageView{}, err
	}
	message := version.Message{
		ID:               s.ids.Next(),
		ConversationID:   conversationID,
		Role:             version.MessageRoleAssistant,
		Parts:            append([]version.MessagePart{}, parts...),
		StreamParts:      streamParts,
		Refs:             []version.MessageRef{},
		TokenUsage:       map[string]any{},
		ReplyToMessageID: replyToMessageID,
		Kind:             version.MessageKindText,
		CreatedAt:        now,
	}
	if runID > 0 {
		value := runID
		message.AgentRunID = &value
	}
	if err := s.repo.AppendAssistantMessageAsBackground(ctx, conversationID, message, now); err != nil {
		if errors.Is(err, version.ErrNotConversationWriter) {
			return MessageView{}, newNotWriterError(conversationID, 0, "", NotWriterReasonServerWriter)
		}
		return MessageView{}, mapConversationRepositoryError(err)
	}
	s.publishMessageEvent(ctx, conversation.VersionID, message)
	s.markRunning(ctx, conversation.VersionID)
	return toMessageView(message), nil
}

// ---------- single-writer lease (design §4.10, B §5.1) ----------

// Acquire claims the conversation's single-writer lease for one Host turn. A
// second live turn is a typed 409 conversation_busy; re-acquiring as the same
// turn/agent is an idempotent retry that renews the lease.
func (s *ConversationService) Acquire(ctx context.Context, conversationID int64, agent string, turnID int64) (WriterLease, error) {
	if conversationID <= 0 {
		return WriterLease{}, platform.Invalid("cid", "must be a positive decimal ID")
	}
	agent = strings.TrimSpace(agent)
	if turnID <= 0 {
		return WriterLease{}, platform.Invalid("turn_id", "must be a positive decimal ID")
	}
	if agent == "" {
		return WriterLease{}, platform.Invalid("agent", "must not be empty")
	}
	now := s.clock.Now()
	leaseUntil := now.Add(version.ConversationWriterLeaseDuration)
	if err := s.repo.AcquireWriter(ctx, conversationID, turnID, agent, now, leaseUntil); err != nil {
		if errors.Is(err, version.ErrConversationBusy) {
			return WriterLease{}, s.writerBusyError(ctx, conversationID)
		}
		return WriterLease{}, mapConversationRepositoryError(err)
	}
	return WriterLease{ConversationID: conversationID, TurnID: turnID, Agent: agent, ExpiresAt: leaseUntil}, nil
}

// RenewLease is the Host heartbeat: it extends the caller's lease by 90s from
// the injected clock. A caller that no longer owns a live lease is a typed 403
// not_writer (the turn has been fenced).
func (s *ConversationService) RenewLease(ctx context.Context, conversationID int64, agent string, turnID int64) (WriterLease, error) {
	if conversationID <= 0 {
		return WriterLease{}, platform.Invalid("cid", "must be a positive decimal ID")
	}
	agent = strings.TrimSpace(agent)
	if turnID <= 0 {
		return WriterLease{}, platform.Invalid("turn_id", "must be a positive decimal ID")
	}
	if agent == "" {
		return WriterLease{}, platform.Invalid("agent", "must not be empty")
	}
	now := s.clock.Now()
	leaseUntil := now.Add(version.ConversationWriterLeaseDuration)
	if err := s.repo.RenewWriterLease(ctx, conversationID, turnID, agent, now, leaseUntil); err != nil {
		if errors.Is(err, version.ErrNotConversationWriter) {
			return WriterLease{}, newNotWriterError(conversationID, turnID, agent, NotWriterReasonLeaseLost)
		}
		return WriterLease{}, mapConversationRepositoryError(err)
	}
	return WriterLease{ConversationID: conversationID, TurnID: turnID, Agent: agent, ExpiresAt: leaseUntil}, nil
}

// Release clears the lease at a clean turn end. It is idempotent for an
// already-released conversation and never clears another turn's lease.
func (s *ConversationService) Release(ctx context.Context, conversationID int64, agent string, turnID int64) error {
	if conversationID <= 0 {
		return platform.Invalid("cid", "must be a positive decimal ID")
	}
	agent = strings.TrimSpace(agent)
	if turnID <= 0 {
		return platform.Invalid("turn_id", "must be a positive decimal ID")
	}
	if agent == "" {
		return platform.Invalid("agent", "must not be empty")
	}
	if err := s.repo.ReleaseWriter(ctx, conversationID, turnID, agent); err != nil {
		if errors.Is(err, version.ErrNotConversationWriter) {
			return newNotWriterError(conversationID, turnID, agent, NotWriterReasonLeaseLost)
		}
		return mapConversationRepositoryError(err)
	}
	return nil
}

// AppendAssistant is the only Host assistant-text write path (R-B2-3). The
// write is accepted only when writer owns the live conversation lease; an idle
// conversation or an expired lease is taken over atomically (crash recovery),
// while a different live turn is a typed 409 conversation_busy. Appends are
// idempotent per turn: a retried turn returns the stored message instead of
// appending a duplicate.
func (s *ConversationService) AppendAssistant(ctx context.Context, conversationID int64, writer Writer, parts []version.MessagePart, runID int64, replyToMessageID *int64) (MessageView, error) {
	if conversationID <= 0 {
		return MessageView{}, platform.Invalid("cid", "must be a positive decimal ID")
	}
	writer.Agent = strings.TrimSpace(writer.Agent)
	if writer.TurnID <= 0 || writer.Agent == "" {
		return MessageView{}, newNotWriterError(conversationID, writer.TurnID, writer.Agent, NotWriterReasonNoLease)
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
	streamParts, err := assistantStreamParts(parts, now)
	if err != nil {
		return MessageView{}, err
	}
	message := version.Message{
		ID:               s.ids.Next(),
		ConversationID:   conversationID,
		Role:             version.MessageRoleAssistant,
		Parts:            append([]version.MessagePart{}, parts...),
		StreamParts:      streamParts,
		Refs:             []version.MessageRef{},
		TokenUsage:       map[string]any{},
		ReplyToMessageID: replyToMessageID,
		AuthorAgent:      writer.Agent,
		Kind:             version.MessageKindText,
		CreatedAt:        now,
	}
	if runID > 0 {
		value := runID
		message.AgentRunID = &value
	}
	turnKey := version.HostTurnClientMessageID(writer.TurnID)
	message.ClientMessageID = &turnKey

	leaseUntil := now.Add(version.ConversationWriterLeaseDuration)
	existing, created, err := s.repo.AppendAssistantMessageAsWriter(
		ctx, conversationID, message, writer.TurnID, writer.Agent, now, leaseUntil)
	if err != nil {
		switch {
		case errors.Is(err, version.ErrConversationBusy):
			return MessageView{}, s.writerBusyError(ctx, conversationID)
		case errors.Is(err, version.ErrNotConversationWriter):
			return MessageView{}, newNotWriterError(conversationID, writer.TurnID, writer.Agent, NotWriterReasonLeaseLost)
		default:
			return MessageView{}, mapConversationRepositoryError(err)
		}
	}
	if !created && existing != nil {
		return toMessageView(*existing), nil
	}
	s.publishMessageEvent(ctx, conversation.VersionID, message)
	s.markRunning(ctx, conversation.VersionID)
	return toMessageView(message), nil
}

// writerBusyError builds the typed 409 from the stored lease holder. The extra
// read is best effort: the busy classification itself already came from the
// atomic SQL compare-and-set.
func (s *ConversationService) writerBusyError(ctx context.Context, conversationID int64) *BusyError {
	conversation, err := s.repo.GetByID(ctx, conversationID)
	if err != nil {
		return newBusyError(version.Conversation{ID: conversationID})
	}
	return newBusyError(conversation)
}

// StructuredMessageInput is the v0.22 structured-message write request (Task
// 23/36): a question card (kind question, role assistant, written by the live
// Host writer) or its answer echo (kind answer, role user, written by the
// answering actor). ClientMessageID is the idempotency key; the writer API
// returns the stored message with created=false on a retried write.
type StructuredMessageInput struct {
	VersionID       int64
	ConversationID  int64
	Kind            string
	Parts           []version.MessagePart
	ClientMessageID string
	ActorID         int64
	// AgentRunID is the owning Host run of a question card. It must match the
	// conversation's active writer lease; the author_agent of the stored
	// message is the lease holder, so a background job can never author a card.
	AgentRunID int64
}

// AppendStructuredMessage is the only structured-question conversation writer.
// It replaces the pre-fix direct SQL INSERT, which bypassed the conversation
// service and wrote a part type the version domain rejected on read.
//
//   - kind question: assistant message gated by the live writer lease
//     (conversation.active_turn_id == AgentRunID); author_agent is the lease
//     holder and client_message_id makes a retried question.ask idempotent.
//   - kind answer: user message written through the idempotent user-message
//     path; it never enqueues a chat.turn task (the answer is not a prompt).
//
// The returned bool reports whether the message was created.
func (s *ConversationService) AppendStructuredMessage(ctx context.Context, in StructuredMessageInput) (MessageView, bool, error) {
	if err := validateConversationIDs(in.VersionID, in.ConversationID); err != nil {
		return MessageView{}, false, err
	}
	kind := strings.TrimSpace(in.Kind)
	switch kind {
	case version.MessageKindQuestion, version.MessageKindAnswer:
	default:
		return MessageView{}, false, platform.Invalid("kind", "must be question or answer")
	}
	in.ClientMessageID = strings.TrimSpace(in.ClientMessageID)
	if in.ClientMessageID != "" {
		if err := version.ValidateClientMessageID(in.ClientMessageID); err != nil {
			return MessageView{}, false, err
		}
	}
	conversation, _, err := s.repo.Get(ctx, in.VersionID, in.ConversationID)
	if err != nil {
		return MessageView{}, false, mapConversationRepositoryError(err)
	}

	// Idempotency first: a retried question.ask/answer must resolve the stored
	// message even if the writer lease changed in between.
	if in.ClientMessageID != "" {
		existing, lookupErr := s.repo.FindMessageByClientID(ctx, in.ConversationID, in.ClientMessageID)
		switch {
		case lookupErr == nil && existing != nil:
			return toMessageView(*existing), false, nil
		case lookupErr != nil && !errors.Is(lookupErr, version.ErrMessageNotFound):
			return MessageView{}, false, mapConversationRepositoryError(lookupErr)
		}
	}

	now := s.clock.Now()
	switch kind {
	case version.MessageKindQuestion:
		if err := version.ValidateAssistantMessageParts(in.Parts); err != nil {
			return MessageView{}, false, err
		}
		if in.AgentRunID <= 0 {
			return MessageView{}, false, platform.Invalid("agent_run_id",
				"is required to author a structured question card")
		}
		// The card is authored by the live Host writer only. The lease holder
		// identity comes from the stored conversation, never from the caller,
		// so the author_agent is truthful.
		if conversation.ActiveTurnID == nil || *conversation.ActiveTurnID != in.AgentRunID ||
			conversation.ActiveAgent == nil || strings.TrimSpace(*conversation.ActiveAgent) == "" {
			return MessageView{}, false, newNotWriterError(in.ConversationID, in.AgentRunID, "", NotWriterReasonLeaseLost)
		}
		agent := strings.TrimSpace(*conversation.ActiveAgent)
		message := version.Message{
			ID:             s.ids.Next(),
			ConversationID: in.ConversationID,
			Role:           version.MessageRoleAssistant,
			Parts:          append([]version.MessagePart{}, in.Parts...),
			Refs:           []version.MessageRef{},
			TokenUsage:     map[string]any{},
			AuthorAgent:    agent,
			Kind:           kind,
			CreatedAt:      now,
		}
		runID := in.AgentRunID
		message.AgentRunID = &runID
		if in.ClientMessageID != "" {
			clientID := in.ClientMessageID
			message.ClientMessageID = &clientID
		}
		leaseUntil := now.Add(version.ConversationWriterLeaseDuration)
		existing, created, err := s.repo.AppendAssistantMessageAsWriter(
			ctx, in.ConversationID, message, in.AgentRunID, agent, now, leaseUntil)
		if err != nil {
			switch {
			case errors.Is(err, version.ErrConversationBusy):
				return MessageView{}, false, s.writerBusyError(ctx, in.ConversationID)
			case errors.Is(err, version.ErrNotConversationWriter):
				return MessageView{}, false, newNotWriterError(in.ConversationID, in.AgentRunID, agent, NotWriterReasonLeaseLost)
			default:
				return MessageView{}, false, mapConversationRepositoryError(err)
			}
		}
		if !created && existing != nil {
			return toMessageView(*existing), false, nil
		}
		s.publishMessageEvent(ctx, conversation.VersionID, message)
		s.markRunning(ctx, conversation.VersionID)
		return toMessageView(message), true, nil

	case version.MessageKindAnswer:
		if err := version.ValidateUserMessageParts(in.Parts); err != nil {
			return MessageView{}, false, err
		}
		message := version.Message{
			ID:             s.ids.Next(),
			ConversationID: in.ConversationID,
			Role:           version.MessageRoleUser,
			Parts:          append([]version.MessagePart{}, in.Parts...),
			Refs:           []version.MessageRef{},
			TokenUsage:     map[string]any{},
			ActorID:        optionalActor(in.ActorID),
			Kind:           kind,
			CreatedAt:      now,
		}
		if in.ClientMessageID != "" {
			clientID := in.ClientMessageID
			message.ClientMessageID = &clientID
		}
		existing, created, err := s.repo.PostUserMessage(ctx, in.ConversationID, message)
		if err != nil {
			return MessageView{}, false, mapConversationRepositoryError(err)
		}
		if !created && existing != nil {
			return toMessageView(*existing), false, nil
		}
		s.publishMessageEvent(ctx, conversation.VersionID, message)
		s.markRunning(ctx, conversation.VersionID)
		return toMessageView(message), true, nil
	}
	return MessageView{}, false, platform.Invalid("kind", "must be question or answer")
}

// UpsertJobCard stores or refreshes the single job_card part of one background
// job in a conversation. The first snapshot appends one assistant message whose
// only part is the card (with its conversation_message_part row); every later
// start/progress/terminal snapshot rewrites that same message and row in place,
// so a job never owns two cards. The returned bool reports whether the card
// message was created.
func (s *ConversationService) UpsertJobCard(ctx context.Context, versionID, conversationID int64, card version.MessagePart) (MessageView, bool, error) {
	if err := validateConversationIDs(versionID, conversationID); err != nil {
		return MessageView{}, false, err
	}
	if err := version.ValidateJobCardMessage(card); err != nil {
		return MessageView{}, false, err
	}
	// Ownership is version-scoped: a conversation of another version is a 404,
	// never a cross-version card write.
	if _, _, err := s.repo.Get(ctx, versionID, conversationID); err != nil {
		return MessageView{}, false, mapConversationRepositoryError(err)
	}

	existing, err := s.repo.FindJobCardMessage(ctx, versionID, conversationID, card.JobID)
	switch {
	case err == nil && existing != nil:
		// A cancelled card is terminal and sticky. The worker notices a cancel
		// only on its next heartbeat, so an in-flight turn may still project
		// running/progress or its terminal done after the cancel already
		// rewrote the card: ignore that late snapshot (no write, no republish)
		// and keep the stored cancelled card. The repository re-checks the
		// stored status under its row lock, so a cancel committing between this
		// lookup and the rewrite cannot lose the race either.
		if stored, ok := findJobCardPart(existing.Parts, card.JobID); ok &&
			!version.JobCardRewriteAllowed(stored.Status, card.Status) {
			return toMessageView(*existing), false, nil
		}
		if err := s.repo.UpdateJobCardMessage(ctx, versionID, conversationID, existing.ID, card.JobID, card); err != nil {
			if errors.Is(err, version.ErrJobCardCancelled) {
				// The concurrent cancel won the row-lock re-check after this
				// lookup. Its cancelled card is the truth; the late running/
				// done projection has nothing left to write back.
				return MessageView{}, false, nil
			}
			return MessageView{}, false, mapConversationRepositoryError(err)
		}
		existing.Parts = replaceJobCardPart(existing.Parts, card)
		s.publishMessageEvent(ctx, versionID, *existing)
		return toMessageView(*existing), false, nil
	case err != nil && !errors.Is(err, version.ErrMessageNotFound):
		return MessageView{}, false, mapConversationRepositoryError(err)
	}

	now := s.clock.Now()
	message := version.Message{
		ID:             s.ids.Next(),
		ConversationID: conversationID,
		Role:           version.MessageRoleAssistant,
		Parts:          []version.MessagePart{card},
		Refs:           []version.MessageRef{},
		TokenUsage:     map[string]any{},
		Kind:           version.MessageKindArtifact,
		CreatedAt:      now,
	}
	streamParts, err := assistantStreamParts(message.Parts, now)
	if err != nil {
		return MessageView{}, false, err
	}
	message.StreamParts = streamParts
	if err := s.repo.AppendAssistantMessage(ctx, conversationID, message); err != nil {
		return MessageView{}, false, mapConversationRepositoryError(err)
	}
	s.publishMessageEvent(ctx, versionID, message)
	return toMessageView(message), true, nil
}

// replaceJobCardPart returns a copy of parts with jobID's card replaced by the
// new snapshot. The card is the only mutable part of the conversation and the
// caller only reaches this after the repository found and updated that exact
// message, so there is no append fallback: a missing part leaves the snapshot
// unchanged.
func replaceJobCardPart(parts []version.MessagePart, card version.MessagePart) []version.MessagePart {
	updated := append([]version.MessagePart(nil), parts...)
	for index := range updated {
		if updated[index].Type == version.MessagePartJobCard && updated[index].JobID == card.JobID {
			updated[index] = card
			break
		}
	}
	return updated
}

// CancelJobCard rewrites the persisted conversation card of one canceled job to
// the terminal cancelled snapshot, in place (Task 63, P2-T10). It is the
// persisted-side counterpart of the live job.finished{status:"cancelled"}
// event: after a reload the thread shows the same terminal card the live UI
// already received instead of a stale "running" snapshot. The card is located
// by job id inside the addressed conversation and its original kind/current/
// total are preserved; only status and cancelable become terminal, and the
// usual conversation.message frame is republished.
//
// It is a no-op when no card was ever projected for the job (legacy thread or a
// job that never ran through the conversation projection). The caller treats
// the whole operation as best effort: the cancel state is already committed, so
// an error must never roll it back.
func (s *ConversationService) CancelJobCard(ctx context.Context, versionID, conversationID, jobID int64) error {
	if err := validateConversationIDs(versionID, conversationID); err != nil {
		return err
	}
	if jobID <= 0 {
		return platform.Invalid("job_id", "must be a positive decimal ID")
	}
	existing, err := s.repo.FindJobCardMessage(ctx, versionID, conversationID, jobID)
	if err != nil {
		if errors.Is(err, version.ErrMessageNotFound) {
			return nil
		}
		return mapConversationRepositoryError(err)
	}
	if existing == nil {
		return nil
	}
	card, ok := findJobCardPart(existing.Parts, jobID)
	if !ok {
		return nil
	}
	card.Status = eventingapp.JobStatusCancelled
	cancelable := false
	card.Cancelable = &cancelable
	if err := s.repo.UpdateJobCardMessage(ctx, versionID, conversationID, existing.ID, jobID, card); err != nil {
		if errors.Is(err, version.ErrMessageNotFound) {
			// The card disappeared between the lookup and the rewrite; there is
			// nothing left to update and the cancel already stands.
			return nil
		}
		return mapConversationRepositoryError(err)
	}
	existing.Parts = replaceJobCardPart(existing.Parts, card)
	s.publishMessageEvent(ctx, versionID, *existing)
	return nil
}

// findJobCardPart returns jobID's job_card part from a message snapshot.
func findJobCardPart(parts []version.MessagePart, jobID int64) (version.MessagePart, bool) {
	for _, part := range parts {
		if part.Type == version.MessagePartJobCard && part.JobID == jobID {
			return part, true
		}
	}
	return version.MessagePart{}, false
}

// ReopenJobCard rewrites the persisted card of a retried job from the terminal
// cancelled snapshot back to the non-terminal open snapshot, in place (Task 63
// fix round 2). It is the explicit bypass of the sticky cancelled card: a
// retried job starts a new attempt, and the runner's next start/progress
// projection must be able to update this same card again. kind/current/total are
// preserved; only status and cancelable become open. A job without a card is a
// silent no-op. The caller treats the operation as best effort: the retry state
// is already committed, so an error must never roll it back.
func (s *ConversationService) ReopenJobCard(ctx context.Context, versionID, conversationID, jobID int64) error {
	if err := validateConversationIDs(versionID, conversationID); err != nil {
		return err
	}
	if jobID <= 0 {
		return platform.Invalid("job_id", "must be a positive decimal ID")
	}
	existing, err := s.repo.FindJobCardMessage(ctx, versionID, conversationID, jobID)
	if err != nil {
		if errors.Is(err, version.ErrMessageNotFound) {
			return nil
		}
		return mapConversationRepositoryError(err)
	}
	if existing == nil {
		return nil
	}
	card, ok := findJobCardPart(existing.Parts, jobID)
	if !ok {
		return nil
	}
	card.Status = eventingapp.JobStatusOpen
	cancelable := true
	card.Cancelable = &cancelable
	if err := s.repo.ReopenJobCardMessage(ctx, versionID, conversationID, existing.ID, jobID, card); err != nil {
		if errors.Is(err, version.ErrMessageNotFound) {
			// The card disappeared between the lookup and the rewrite; there is
			// nothing left to reopen and the retry already stands.
			return nil
		}
		return mapConversationRepositoryError(err)
	}
	existing.Parts = replaceJobCardPart(existing.Parts, card)
	s.publishMessageEvent(ctx, versionID, *existing)
	return nil
}

// ---------- tasks and events ----------

// ensureMessageTasks enqueues the single Host reply for one user message
// (chat.turn). v0.22 D-B1 removed the requirement.curate background job:
// requirement drafting is a Host tool now, so no curation task is enqueued.
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
	// Assistant streamed parts carry their 1-based conversation_message_part seq
	// so the UI can dedupe repeated frames. The seq assignment mirrors
	// assistantStreamParts; parts outside the streaming enum stay unsequenced.
	seq := 0
	for _, part := range message.Parts {
		payload := partPayload(part)
		if message.Role == version.MessageRoleAssistant && version.IsStreamingMessagePartType(part.Type) {
			seq++
			payload["seq"] = seq
		}
		parts = append(parts, payload)
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

// assistantStreamParts turns an assistant message's parts into the append-only
// conversation_message_part rows written in the same transaction as the
// message head. Seq is 1-based and strictly increasing in part order; only the
// eight DB-checked part types are streamed (rollback_proposal/image remain
// snapshot-only).
func assistantStreamParts(parts []version.MessagePart, now time.Time) ([]version.ConversationMessagePart, error) {
	rows := make([]version.ConversationMessagePart, 0, len(parts))
	seq := 0
	for _, part := range parts {
		if !version.IsStreamingMessagePartType(part.Type) {
			continue
		}
		payload, err := json.Marshal(part)
		if err != nil {
			return nil, fmt.Errorf("encode message part %q: %w", part.Type, err)
		}
		seq++
		rows = append(rows, version.ConversationMessagePart{
			Seq:       seq,
			Type:      part.Type,
			Payload:   payload,
			CreatedAt: now,
		})
	}
	return rows, nil
}

// partPayload renders the canonical part map from the part's contract JSON
// shape; unknown types fall back to just the type tag.
func partPayload(part version.MessagePart) map[string]any {
	raw, err := json.Marshal(part)
	if err != nil {
		// MessagePart.MarshalJSON only fails for an unknown type, which the
		// validators reject before a message is ever built.
		return map[string]any{"type": part.Type}
	}
	payload := map[string]any{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return map[string]any{"type": part.Type}
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
	model, err := s.models.ResolveConversationModel(ctx, trimmed)
	if err != nil {
		return nil, nil, err
	}
	name := strings.TrimSpace(model.Model)
	if name == "" {
		return nil, nil, platform.Invalid("model_override", "must name a model of the default llm configuration")
	}
	return &name, &name, nil
}

// validateConversationImageModel enforces the Task 75 capability gate: an image
// draft may only be stored and queued when the conversation's effective model
// accepts images. The typed 400 names the model and the missing capability so
// the UI can tell the user to switch models or remove the image; it never
// switches the model automatically (D24).
func (s *ConversationService) validateConversationImageModel(ctx context.Context, modelOverride *string) error {
	if s.models == nil {
		// Fail closed: without a catalog the runtime cannot verify vision, and
		// accepting the draft would recreate the opaque zero-step failure.
		return platform.Unavailable("llm model catalog is not configured")
	}
	requested := ""
	if modelOverride != nil {
		requested = strings.TrimSpace(*modelOverride)
	}
	model, err := s.models.ResolveConversationModel(ctx, requested)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(model.Model)
	if name == "" {
		return platform.Invalid("model_override", "must name a model of the default llm configuration")
	}
	if model.SupportsVision {
		return nil
	}
	return &platform.Error{
		Status: http.StatusBadRequest,
		Code:   CodeLLMCapabilityMismatch,
		Message: fmt.Sprintf(
			"llm model %q does not support images; choose a model with vision or remove the image before sending",
			name),
		Details: map[string]any{
			"field":                "model_override",
			"model":                name,
			"missing_capabilities": []string{"vision"},
		},
	}
}

// containsImageMessagePart reports whether a validated user draft carries an
// image part (either sent directly or converted from an uploaded attachment).
func containsImageMessagePart(parts []version.MessagePart) bool {
	for _, part := range parts {
		if part.Type == version.MessagePartImage {
			return true
		}
	}
	return false
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
	model, err := s.models.ResolveConversationModel(ctx, requested)
	if err != nil {
		return nil
	}
	name := strings.TrimSpace(model.Model)
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
