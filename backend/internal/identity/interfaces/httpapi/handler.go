// Package identityapi exposes the identity HTTP API: public login, the
// authenticated session endpoints, and the admin user-management endpoints.
package identityapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"

	identityapp "github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/pagination"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// Service is the identity application surface the HTTP layer needs.
type Service interface {
	Login(ctx context.Context, username, password, requestID, ip, ua string) (identityapp.Session, error)
	Logout(ctx context.Context, token string) error
	Authenticate(ctx context.Context, token string) (identity.User, error)
	ChangePassword(ctx context.Context, userID int64, oldPassword, newPassword string) error
	CreateUser(ctx context.Context, actorID int64, username, displayName, password string) (identity.User, error)
	ListUsers(ctx context.Context, limit int, beforeID *int64) ([]identity.User, bool, error)
	UpdateUser(ctx context.Context, actorID, userID int64, displayName, status string) (identity.User, error)
	ResetPassword(ctx context.Context, actorID, userID int64, password string) error
}

// Handler serves the identity endpoints.
type Handler struct {
	svc Service
}

// New builds the handler.
func New(svc Service) *Handler {
	return &Handler{svc: svc}
}

// Register mounts the identity routes on mux. authenticated wraps the session
// routes and admin wraps the admin user routes; a nil wrapper means the caller
// mounts the route itself. Passing the server middleware keeps the identity
// package free of a dependency on the server package.
func (h *Handler) Register(mux *http.ServeMux, authenticated, admin func(http.Handler) http.Handler) {
	wrap := func(middleware func(http.Handler) http.Handler, handler http.HandlerFunc) http.Handler {
		if middleware == nil {
			return handler
		}
		return middleware(handler)
	}
	mux.Handle("POST /api/auth/login", http.HandlerFunc(h.Login))
	mux.Handle("POST /api/auth/logout", wrap(authenticated, h.Logout))
	mux.Handle("GET /api/auth/me", wrap(authenticated, h.Me))
	mux.Handle("POST /api/auth/password", wrap(authenticated, h.ChangePassword))
	mux.Handle("GET /api/admin/users", wrap(admin, h.ListUsers))
	mux.Handle("POST /api/admin/users", wrap(admin, h.CreateUser))
	mux.Handle("PATCH /api/admin/users/{id}", wrap(admin, h.UpdateUser))
	mux.Handle("POST /api/admin/users/{id}/reset-password", wrap(admin, h.ResetPassword))
}

// Login POST /api/auth/login.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var body loginRequest
	if !decodeBody(w, r, &body) {
		httpx.Error(w, r, platform.Invalid("body", "request body must be valid JSON"))
		return
	}
	username := strings.TrimSpace(body.Username)
	if username == "" {
		httpx.Error(w, r, platform.Invalid("username", "username is required"))
		return
	}
	if body.Password == "" {
		httpx.Error(w, r, platform.Invalid("password", "password is required"))
		return
	}

	session, err := h.svc.Login(r.Context(), username, body.Password, requestid.From(r.Context()), clientIP(r), r.UserAgent())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toSessionDTO(session))
}

// Logout POST /api/auth/logout.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(r)
	if !ok {
		httpx.Error(w, r, platform.Unauthenticated())
		return
	}
	if err := h.svc.Logout(r.Context(), token); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Me GET /api/auth/me.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(r)
	if !ok {
		httpx.Error(w, r, platform.Unauthenticated())
		return
	}
	user, err := h.svc.Authenticate(r.Context(), token)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toUserDTO(user))
}

// ChangePassword POST /api/auth/password.
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	actor, ok := platform.ActorFrom(r.Context())
	if !ok {
		httpx.Error(w, r, platform.Unauthenticated())
		return
	}
	var body changePasswordRequest
	if !decodeBody(w, r, &body) {
		httpx.Error(w, r, platform.Invalid("body", "request body must be valid JSON"))
		return
	}
	if err := h.svc.ChangePassword(r.Context(), actor.ID, body.OldPassword, body.NewPassword); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListUsers GET /api/admin/users?limit=&cursor=.
func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit := pagination.Limit(query.Get("limit"), identityapp.DefaultListLimit, identityapp.MaxListLimit)
	cursor, err := pagination.Cursor(query.Get("cursor"))
	if err != nil {
		httpx.Error(w, r, platform.Invalid("cursor", "cursor must be a decimal ID"))
		return
	}
	var beforeID *int64
	if cursor > 0 {
		beforeID = &cursor
	}

	users, hasMore, err := h.svc.ListUsers(r.Context(), limit, beforeID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]userDTO, 0, len(users))
	for _, user := range users {
		items = append(items, toUserDTO(user))
	}
	var nextCursor string
	if hasMore && len(users) > 0 {
		nextCursor = pagination.NextCursor(true, users[len(users)-1].ID)
	}
	httpx.List(w, http.StatusOK, items, nextCursor, hasMore)
}

// CreateUser POST /api/admin/users.
func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := platform.ActorFrom(r.Context())
	if !ok {
		httpx.Error(w, r, platform.Unauthenticated())
		return
	}
	var body createUserRequest
	if !decodeBody(w, r, &body) {
		httpx.Error(w, r, platform.Invalid("body", "request body must be valid JSON"))
		return
	}
	user, err := h.svc.CreateUser(r.Context(), actor.ID, body.Username, body.DisplayName, body.Password)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toUserDTO(user))
}

// UpdateUser PATCH /api/admin/users/{id}.
func (h *Handler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := platform.ActorFrom(r.Context())
	if !ok {
		httpx.Error(w, r, platform.Unauthenticated())
		return
	}
	userID, err := validation.NonEmptyID("id", r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body updateUserRequest
	if !decodeBody(w, r, &body) {
		httpx.Error(w, r, platform.Invalid("body", "request body must be valid JSON"))
		return
	}
	if body.DisplayName == nil && body.Status == nil {
		httpx.Error(w, r, platform.Invalid("body", "at least one of display_name or status is required"))
		return
	}
	var displayName, status string
	if body.DisplayName != nil {
		displayName = *body.DisplayName
	}
	if body.Status != nil {
		status = *body.Status
	}

	user, err := h.svc.UpdateUser(r.Context(), actor.ID, userID, displayName, status)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toUserDTO(user))
}

// ResetPassword POST /api/admin/users/{id}/reset-password.
func (h *Handler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	actor, ok := platform.ActorFrom(r.Context())
	if !ok {
		httpx.Error(w, r, platform.Unauthenticated())
		return
	}
	userID, err := validation.NonEmptyID("id", r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body resetPasswordRequest
	if !decodeBody(w, r, &body) {
		httpx.Error(w, r, platform.Invalid("body", "request body must be valid JSON"))
		return
	}
	if err := h.svc.ResetPassword(r.Context(), actor.ID, userID, body.NewPassword); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// decodeBody decodes one JSON request body, rejecting unknown fields, and
// reports whether decoding succeeded. The caller writes the 400 so each
// endpoint controls its own field-level message.
func decodeBody(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target) == nil
}

func bearerToken(r *http.Request) (string, bool) {
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
