package versionapi

import (
	"context"
	"net/http"
	"sort"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// MessagePartsService is the application surface the parts history handler
// needs. The service owns the version/conversation/message ownership check, so
// a message of another version is a 404, never a leaked row.
type MessagePartsService interface {
	MessageParts(ctx context.Context, versionID, conversationID, messageID int64) ([]versionapp.MessagePartView, error)
}

var _ MessagePartsService = (*versionapp.ConversationService)(nil)

// MessagePartsHandler serves the 0.2.1 append-only message parts history
// (spec §9.1/§12.3, OpenAPI listMessageParts).
type MessagePartsHandler struct {
	svc MessagePartsService
}

// NewMessagePartsHandler builds the parts history handler.
func NewMessagePartsHandler(svc MessagePartsService) *MessagePartsHandler {
	return &MessagePartsHandler{svc: svc}
}

// List GET /api/versions/{id}/conversations/{cid}/messages/{mid}/parts.
// {mid} is conversation_message.id (the UI-thread message, not an
// agent_message.id); parts are returned in ascending seq order.
func (h *MessagePartsHandler) List(w http.ResponseWriter, r *http.Request) {
	versionID, err := validation.NonEmptyID("id", r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	conversationID, err := validation.NonEmptyID("cid", r.PathValue("cid"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	messageID, err := validation.NonEmptyID("mid", r.PathValue("mid"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	views, err := h.svc.MessageParts(r.Context(), versionID, conversationID, messageID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	// The repository reads ORDER BY seq; sorting again at the API edge keeps
	// the contract (seq 去重/排序) true for every implementation.
	sort.SliceStable(views, func(i, j int) bool { return views[i].Seq < views[j].Seq })
	items := make([]MessagePart, 0, len(views))
	for _, view := range views {
		items = append(items, toMessagePart(view))
	}
	httpx.JSON(w, http.StatusOK, items)
}

// RegisterMessageParts mounts the 0.2.1 parts history route behind the
// server's bearer-auth middleware. R7 keeps server assembly Lead-owned, so this
// helper is the single mount point for the route.
func RegisterMessageParts(mux *http.ServeMux, authenticated func(http.Handler) http.Handler, h *MessagePartsHandler) {
	if h == nil {
		return
	}
	mux.Handle("GET /api/versions/{id}/conversations/{cid}/messages/{mid}/parts",
		authenticated(http.HandlerFunc(h.List)))
}
