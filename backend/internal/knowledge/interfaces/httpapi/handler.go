// Package httpapi exposes the P7 knowledge REST API (frozen contract):
//
//	GET   /api/knowledge/items                          login  list/search
//	POST  /api/knowledge/items                          login  create draft
//	GET   /api/knowledge/items/{knowledgeId}            login  detail + revisions
//	PATCH /api/knowledge/items/{knowledgeId}            login  edit draft/rejected
//	POST  /api/knowledge/items/{knowledgeId}/approve    admin  draft|revoked -> approved
//	POST  /api/knowledge/items/{knowledgeId}/reject     admin  draft -> rejected
//	POST  /api/knowledge/items/{knowledgeId}/revoke     admin  approved -> revoked
//	GET   /api/knowledge/items/{knowledgeId}/revisions  login  append-only history
//
// Handlers only parse/validate transport input, call the knowledge application
// service and render views; permissions, validation and the state machine stay
// in the service/domain. The assembly root mounts these routes (Task 19), so
// this package never imports server.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	knowledgeapp "github.com/EdouardRichard/EzTest/backend/internal/knowledge/application"
	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/pagination"
)

// Service is the subset of *knowledgeapp.Service used by the HTTP layer.
type Service interface {
	List(ctx context.Context, filter knowledgeapp.ListFilter) (knowledgeapp.Page, error)
	Get(ctx context.Context, id int64) (knowledge.Item, error)
	Create(ctx context.Context, actor platform.Actor, input knowledgeapp.CreateInput) (knowledge.Item, error)
	Update(ctx context.Context, actor platform.Actor, id int64, input knowledgeapp.UpdateInput) (knowledge.Item, error)
	Approve(ctx context.Context, actor platform.Actor, id int64, reason string) (knowledge.Item, error)
	Reject(ctx context.Context, actor platform.Actor, id int64, reason string) (knowledge.Item, error)
	Revoke(ctx context.Context, actor platform.Actor, id int64, reason string) (knowledge.Item, error)
	Revisions(ctx context.Context, id int64) ([]knowledge.Revision, error)
}

// Handler serves the P7 knowledge endpoints.
type Handler struct {
	service Service
}

// New builds the knowledge HTTP handler.
func New(service Service) *Handler { return &Handler{service: service} }

// *knowledgeapp.Service must satisfy the handler's local Service port; the
// assembly root relies on this when wiring Task 19.
var _ Service = (*knowledgeapp.Service)(nil)

// ListItems GET /api/knowledge/items (login).
func (h *Handler) ListItems(w http.ResponseWriter, r *http.Request) {
	if _, err := requireActor(r); err != nil {
		httpx.Error(w, r, err)
		return
	}
	query := r.URL.Query()
	scope := knowledge.Scope(strings.TrimSpace(query.Get("scope")))
	if scope != "" && !scope.Valid() {
		httpx.Error(w, r, platform.Invalid("scope", "scope must be global or system"))
		return
	}
	status := knowledge.Status(strings.TrimSpace(query.Get("status")))
	if status != "" && !status.Valid() {
		httpx.Error(w, r, platform.Invalid("status", "status must be draft, approved, rejected or revoked"))
		return
	}
	systemKey := strings.TrimSpace(query.Get("system_key"))
	if scope == knowledge.ScopeGlobal && systemKey != "" {
		httpx.Error(w, r, platform.Invalid("system_key", "must be empty when scope is global"))
		return
	}
	cursor, err := parseCursor(query.Get("cursor"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	limit, err := parseLimit(query.Get("limit"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.service == nil {
		httpx.Error(w, r, platform.Unavailable("knowledge service is not configured"))
		return
	}
	page, err := h.service.List(r.Context(), knowledgeapp.ListFilter{
		Scope:     scope,
		Status:    status,
		SystemKey: systemKey,
		Tag:       strings.TrimSpace(query.Get("tag")),
		Query:     strings.TrimSpace(query.Get("q")),
		Cursor:    cursor,
		Limit:     limit,
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	views := make([]knowledgeItemView, 0, len(page.Items))
	for _, item := range page.Items {
		views = append(views, toItemView(item))
	}
	nextCursor := ""
	if page.HasMore {
		nextCursor = pagination.NextCursor(true, page.NextCursor)
	}
	httpx.List(w, http.StatusOK, views, nextCursor, page.HasMore)
}

// CreateItem POST /api/knowledge/items (login). The server always creates a
// draft unless an admin creates it while knowledge.review_required is false.
func (h *Handler) CreateItem(w http.ResponseWriter, r *http.Request) {
	actor, err := requireActor(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body createItemRequest
	if err := decodeBody(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}
	input, err := body.toInput()
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.service == nil {
		httpx.Error(w, r, platform.Unavailable("knowledge service is not configured"))
		return
	}
	item, err := h.service.Create(r.Context(), actor, input)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toItemView(item))
}

// GetItem GET /api/knowledge/items/{knowledgeId} (login).
func (h *Handler) GetItem(w http.ResponseWriter, r *http.Request) {
	if _, err := requireActor(r); err != nil {
		httpx.Error(w, r, err)
		return
	}
	id, err := parsePathID(r, "knowledgeId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.service == nil {
		httpx.Error(w, r, platform.Unavailable("knowledge service is not configured"))
		return
	}
	item, err := h.service.Get(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	revisions, err := h.service.Revisions(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toItemDetailView(item, revisions))
}

// UpdateItem PATCH /api/knowledge/items/{knowledgeId} (author or admin). The
// service enforces the author/admin rule and the draft/rejected precondition.
func (h *Handler) UpdateItem(w http.ResponseWriter, r *http.Request) {
	actor, err := requireActor(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	id, err := parsePathID(r, "knowledgeId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body updateItemRequest
	if err := decodeBody(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.service == nil {
		httpx.Error(w, r, platform.Unavailable("knowledge service is not configured"))
		return
	}
	item, err := h.service.Update(r.Context(), actor, id, body.toInput())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toItemView(item))
}

// ApproveItem POST /api/knowledge/items/{knowledgeId}/approve (admin).
func (h *Handler) ApproveItem(w http.ResponseWriter, r *http.Request) {
	h.review(w, r, "approve")
}

// RejectItem POST /api/knowledge/items/{knowledgeId}/reject (admin).
func (h *Handler) RejectItem(w http.ResponseWriter, r *http.Request) {
	h.review(w, r, "reject")
}

// RevokeItem POST /api/knowledge/items/{knowledgeId}/revoke (admin).
func (h *Handler) RevokeItem(w http.ResponseWriter, r *http.Request) {
	h.review(w, r, "revoke")
}

// ListRevisions GET /api/knowledge/items/{knowledgeId}/revisions (login).
func (h *Handler) ListRevisions(w http.ResponseWriter, r *http.Request) {
	if _, err := requireActor(r); err != nil {
		httpx.Error(w, r, err)
		return
	}
	id, err := parsePathID(r, "knowledgeId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.service == nil {
		httpx.Error(w, r, platform.Unavailable("knowledge service is not configured"))
		return
	}
	revisions, err := h.service.Revisions(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	views := make([]knowledgeRevisionView, 0, len(revisions))
	for _, revision := range revisions {
		views = append(views, toRevisionView(revision))
	}
	httpx.List(w, http.StatusOK, views, "", false)
}

/* --------------------------------- helpers -------------------------------- */

func (h *Handler) review(w http.ResponseWriter, r *http.Request, action string) {
	actor, err := requireAdmin(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	id, err := parsePathID(r, "knowledgeId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	reason, err := decodeReason(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if action != "approve" {
		if _, err := knowledge.ValidateReason(reason); err != nil {
			httpx.Error(w, r, err)
			return
		}
	}
	if h.service == nil {
		httpx.Error(w, r, platform.Unavailable("knowledge service is not configured"))
		return
	}
	var item knowledge.Item
	switch action {
	case "approve":
		item, err = h.service.Approve(r.Context(), actor, id, reason)
	case "reject":
		item, err = h.service.Reject(r.Context(), actor, id, reason)
	case "revoke":
		item, err = h.service.Revoke(r.Context(), actor, id, reason)
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toItemView(item))
}

func requireActor(r *http.Request) (platform.Actor, error) {
	actor, ok := platform.ActorFrom(r.Context())
	if !ok {
		return platform.Actor{}, platform.Unauthenticated()
	}
	return actor, nil
}

// requireAdmin is defense in depth: the assembly root also mounts the review
// routes behind the admin middleware, but a direct handler call must never let
// a non-admin review knowledge.
func requireAdmin(r *http.Request) (platform.Actor, error) {
	actor, err := requireActor(r)
	if err != nil {
		return platform.Actor{}, err
	}
	if !actor.IsAdmin() {
		return platform.Actor{}, platform.Forbidden()
	}
	return actor, nil
}

func parsePathID(r *http.Request, field string) (int64, error) {
	raw := strings.TrimSpace(r.PathValue(field))
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, platform.Invalid(field, "must be a positive decimal ID")
	}
	return parsed, nil
}

func parseCursor(raw string) (int64, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, nil
	}
	cursor, err := pagination.Cursor(trimmed)
	if err != nil {
		return 0, platform.Invalid("cursor", "must be a non-negative decimal ID")
	}
	return cursor, nil
}

func parseLimit(raw string) (int, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return knowledgeapp.DefaultListLimit, nil
	}
	value, err := strconv.Atoi(trimmed)
	if err != nil || value < 1 || value > knowledgeapp.MaxListLimit {
		return 0, platform.Invalid("limit", "must be between 1 and 100")
	}
	return value, nil
}

func decodeBody(r *http.Request, target any) error {
	if r.Body == nil {
		return platform.Invalid("body", "request body is required")
	}
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		return platform.Invalid("body", "invalid JSON body: "+err.Error())
	}
	return nil
}

// decodeReason reads an optional review body: an empty body means no reason
// (only approve accepts that), while malformed JSON is a 400.
func decodeReason(r *http.Request) (string, error) {
	if r.Body == nil {
		return "", nil
	}
	var body reasonRequest
	err := json.NewDecoder(r.Body).Decode(&body)
	if errors.Is(err, io.EOF) {
		return "", nil
	}
	if err != nil {
		return "", platform.Invalid("body", "invalid JSON body: "+err.Error())
	}
	return body.Reason, nil
}
