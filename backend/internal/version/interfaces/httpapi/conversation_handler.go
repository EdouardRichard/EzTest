package versionapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/pagination"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ConversationService is the application surface the conversation handlers
// need.
type ConversationService interface {
	Create(ctx context.Context, versionID int64, kind, title string, actorID int64) (versionapp.ConversationView, error)
	List(ctx context.Context, versionID int64, kind string, limit int, beforeID *int64) ([]versionapp.ConversationView, bool, error)
	Get(ctx context.Context, versionID, conversationID int64) (versionapp.ConversationView, error)
	Messages(ctx context.Context, versionID, conversationID int64, after int64, limit int) ([]versionapp.MessageView, bool, error)
	PostUserMessage(ctx context.Context, versionID, conversationID int64, parts []version.MessagePart, clientMessageID string, actorID int64) (versionapp.MessageView, error)
}

// ConversationHandler serves the /api/versions/{id}/conversations routes.
type ConversationHandler struct {
	svc ConversationService
}

// NewConversationHandler builds the conversation handler.
func NewConversationHandler(svc ConversationService) *ConversationHandler {
	return &ConversationHandler{svc: svc}
}

var _ ConversationService = (*versionapp.ConversationService)(nil)

// List GET /api/versions/{id}/conversations?kind=&limit=&before=.
func (h *ConversationHandler) List(w http.ResponseWriter, r *http.Request) {
	versionID, err := validation.NonEmptyID("id", r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	query := r.URL.Query()
	kind := strings.TrimSpace(query.Get("kind"))
	limit := pagination.Limit(query.Get("limit"), pagination.DefaultLimit, pagination.MaxLimit)
	before, err := parseBefore(query.Get("before"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	views, hasMore, err := h.svc.List(r.Context(), versionID, kind, limit, before)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]conversationSummaryDTO, 0, len(views))
	for _, view := range views {
		items = append(items, toConversationSummaryDTO(view))
	}
	nextCursor := ""
	if hasMore && len(items) > 0 {
		nextCursor = items[len(items)-1].ID
	}
	httpx.List(w, http.StatusOK, items, nextCursor, hasMore)
}

// Create POST /api/versions/{id}/conversations.
func (h *ConversationHandler) Create(w http.ResponseWriter, r *http.Request) {
	actorID, ok := requireActor(w, r)
	if !ok {
		return
	}
	versionID, err := validation.NonEmptyID("id", r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var request createConversationRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.Create(r.Context(), versionID, request.Kind, request.Title, actorID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toConversationDTO(view))
}

// Get GET /api/versions/{id}/conversations/{cid}.
func (h *ConversationHandler) Get(w http.ResponseWriter, r *http.Request) {
	versionID, conversationID, err := conversationPathIDs(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.Get(r.Context(), versionID, conversationID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toConversationDTO(view))
}

// Messages GET /api/versions/{id}/conversations/{cid}/messages?after=&limit=.
func (h *ConversationHandler) Messages(w http.ResponseWriter, r *http.Request) {
	versionID, conversationID, err := conversationPathIDs(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	query := r.URL.Query()
	after, err := pagination.Cursor(strings.TrimSpace(query.Get("after")))
	if err != nil || after < 0 {
		httpx.Error(w, r, platform.Invalid("after", "must be a zero or positive decimal ID"))
		return
	}
	limit := pagination.Limit(query.Get("limit"), version.MessagesDefaultLimit, version.MessagesMaxLimit)
	views, hasMore, err := h.svc.Messages(r.Context(), versionID, conversationID, after, limit)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]conversationMessageDTO, 0, len(views))
	for _, view := range views {
		items = append(items, toConversationMessageDTO(view))
	}
	nextCursor := ""
	if hasMore && len(items) > 0 {
		nextCursor = items[len(items)-1].ID
	}
	httpx.List(w, http.StatusOK, items, nextCursor, hasMore)
}

// PostMessage POST /api/versions/{id}/conversations/{cid}/messages.
func (h *ConversationHandler) PostMessage(w http.ResponseWriter, r *http.Request) {
	actorID, ok := requireActor(w, r)
	if !ok {
		return
	}
	versionID, conversationID, err := conversationPathIDs(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var request postMessageRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	parts, err := request.messageParts()
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.PostUserMessage(r.Context(), versionID, conversationID, parts, request.ClientMessageID, actorID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toConversationMessageDTO(view))
}

func conversationPathIDs(r *http.Request) (int64, int64, error) {
	versionID, err := validation.NonEmptyID("id", r.PathValue("id"))
	if err != nil {
		return 0, 0, err
	}
	conversationID, err := validation.NonEmptyID("cid", r.PathValue("cid"))
	if err != nil {
		return 0, 0, err
	}
	return versionID, conversationID, nil
}
