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
	CreateWithModel(ctx context.Context, versionID int64, kind, title string, modelOverride *string, actorID int64) (versionapp.ConversationView, error)
	List(ctx context.Context, versionID int64, kind string, limit int, beforeID *int64) ([]versionapp.ConversationView, bool, error)
	Get(ctx context.Context, versionID, conversationID int64) (versionapp.ConversationView, error)
	SetModelOverride(ctx context.Context, versionID, conversationID int64, modelOverride *string) (versionapp.ConversationView, error)
	Messages(ctx context.Context, versionID, conversationID int64, after int64, limit int) ([]versionapp.MessageView, bool, error)
	PostUserMessage(ctx context.Context, versionID, conversationID int64, parts []version.MessagePart, clientMessageID string, actorID int64) (versionapp.MessageView, error)
}

// ConversationHandler serves the /api/versions/{id}/conversations routes.
type ConversationHandler struct {
	svc    ConversationService
	design versionapp.DesignStarter
	stage  ConversationStageReader
}

// ConversationStageReader is the deterministic stage projection used to keep
// design actions off a clarifying version (Task 78). *versionapp.StageMachine
// satisfies it.
type ConversationStageReader interface {
	Stage(ctx context.Context, versionID int64) (versionapp.StageView, error)
}

// SetStageReader wires the stage projection. Without it the handler keeps the
// P1-only behavior (no stage gate); production always sets it.
func (h *ConversationHandler) SetStageReader(reader ConversationStageReader) {
	h.stage = reader
}

// NewConversationHandler builds the conversation handler. design may be
// omitted in P1-only tests; when provided it is called best effort after a
// design conversation is created (the agent reconciler is the fallback).
func NewConversationHandler(svc ConversationService, design ...versionapp.DesignStarter) *ConversationHandler {
	handler := &ConversationHandler{svc: svc}
	if len(design) > 0 {
		handler.design = design[0]
	}
	return handler
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
	// A design action on a clarifying (or otherwise pre-design) version must not
	// be silently accepted: it would generate cases the stage machine has not
	// gated yet (Task 78). Requirement confirmation advances the version to
	// requirement_review, and the explicit requirement gate starts designing.
	kind := strings.TrimSpace(request.Kind)
	if kind == string(version.ConversationDesign) && h.stage != nil {
		stage, err := h.stage.Stage(r.Context(), versionID)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		if !designConversationStageAllowed(stage.Stage) {
			httpx.Error(w, r, &platform.Error{
				Status:  http.StatusConflict,
				Code:    "invalid_stage_for_action",
				Message: "creating a design conversation requires the designing stage, but the version is at " + string(stage.Stage),
				Details: map[string]any{
					"action":         "create_design_conversation",
					"required_stage": "designing",
					"current_stage":  string(stage.Stage),
					"next_action":    "confirm the requirements and apply the requirement gate (start design) first",
				},
			})
			return
		}
	}
	view, err := h.svc.CreateWithModel(r.Context(), versionID, request.Kind, request.Title, request.ModelOverride, actorID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	// Design conversations start their first round through the Planner. The
	// call is best effort: a failure is repaired by the agent reconciler and
	// never fails the conversation creation.
	if view.Kind == version.ConversationDesign && h.design != nil {
		_ = h.design.StartRound(r.Context(), versionID, view.ID, actorID)
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

// UpdateModel PATCH /api/versions/{id}/conversations/{cid}.
//
// It sets or clears the 0.2.1 conversation-level model override. The model must
// be an enabled, non-deprecated model of the default LLM configuration; the
// switch is user-initiated only (D24). A null model_override clears the
// override and falls back to the configuration default_model_id.
func (h *ConversationHandler) UpdateModel(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireActor(w, r); !ok {
		return
	}
	versionID, conversationID, err := conversationPathIDs(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var request updateConversationRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	modelOverride, err := request.modelOverride()
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.SetModelOverride(r.Context(), versionID, conversationID, modelOverride)
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

// designConversationStageAllowed reports whether a design conversation may be
// created at stage. Designing is the product entry; case_review and the later
// workflow stages keep redesign/modification available.
func designConversationStageAllowed(stage version.Stage) bool {
	switch stage {
	case version.StageDesigning, version.StageCaseReview, version.StageExecuting, version.StageReporting:
		return true
	default:
		return false
	}
}
