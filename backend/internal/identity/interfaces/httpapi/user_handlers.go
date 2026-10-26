package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

const (
	// maxUserBodyBytes caps the user request bodies. A username (<=64 chars) and
	// a password (<=128 chars) are tiny, so 4 KiB is ample.
	maxUserBodyBytes = 4 << 10
)

// UserService is the identity use-case port consumed by the admin user routes.
// The application UserService satisfies it.
type UserService interface {
	Create(ctx context.Context, username, password string) (identity.User, error)
	List(ctx context.Context, limit int, afterID int64) ([]identity.User, bool, error)
	Get(ctx context.Context, id int64) (identity.User, error)
	SetStatus(ctx context.Context, actorID, userID int64, status identity.Status) (identity.User, error)
	ResetPassword(ctx context.Context, userID int64, password string) error
}

// UserHandler serves the admin user-management endpoints. Every route is
// registered behind RequireAdmin; the server's global AuthMiddleware has
// already authenticated the caller and stored the user in the context.
type UserHandler struct {
	svc UserService
}

// NewUserHandler returns handlers for svc.
func NewUserHandler(svc UserService) *UserHandler {
	return &UserHandler{svc: svc}
}

// RegisterUserRoutes mounts the admin user endpoints on mux. Authentication is
// handled by the server's global middleware chain; each route additionally
// requires the admin role.
func RegisterUserRoutes(mux *http.ServeMux, svc UserService) {
	handler := NewUserHandler(svc)
	mux.Handle("GET /api/users", RequireAdmin(http.HandlerFunc(handler.List)))
	mux.Handle("POST /api/users", RequireAdmin(http.HandlerFunc(handler.Create)))
	mux.Handle("GET /api/users/{id}", RequireAdmin(http.HandlerFunc(handler.Get)))
	mux.Handle("PATCH /api/users/{id}", RequireAdmin(http.HandlerFunc(handler.Update)))
	mux.Handle("POST /api/users/{id}/reset-password", RequireAdmin(http.HandlerFunc(handler.ResetPassword)))
}

// List returns one page of users ordered by ID ascending.
func (h *UserHandler) List(w http.ResponseWriter, r *http.Request) {
	limit, err := platform.ParseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid limit")
		return
	}
	afterID, err := platform.DecodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid cursor")
		return
	}
	users, more, err := h.svc.List(r.Context(), limit, afterID)
	if err != nil {
		platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal, "internal server error")
		return
	}
	items := make([]userPayload, len(users))
	for i, user := range users {
		items[i] = newUserPayload(user)
	}
	response := userListEnvelope{Data: items, HasMore: more}
	if more && len(users) > 0 {
		response.NextCursor = platform.EncodeCursor(users[len(users)-1].ID)
	}
	writeJSON(w, http.StatusOK, response)
}

// Create creates a regular user. The body only accepts username and password:
// P0 creates role=user exclusively, so an unknown field such as "role" is
// rejected by the strict JSON decoder.
func (h *UserHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := platform.DecodeJSON(r, &req, maxUserBodyBytes); err != nil {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid JSON body")
		return
	}
	user, err := h.svc.Create(requestContext(r), req.Username, req.Password)
	if err != nil {
		writeUserError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, userEnvelope{Data: newUserPayload(user)})
}

// Get returns one user by ID.
func (h *UserHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := userIDFromPath(w, r)
	if !ok {
		return
	}
	user, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeUserError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, userEnvelope{Data: newUserPayload(user)})
}

// Update changes a user's status. PATCH only accepts status; disabling the
// acting admin is rejected by the service (P0 has no recovery path).
func (h *UserHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := userIDFromPath(w, r)
	if !ok {
		return
	}
	actor, ok := UserFromContext(r.Context())
	if !ok {
		writeUnauthenticated(w, r)
		return
	}
	var req updateUserRequest
	if err := platform.DecodeJSON(r, &req, maxUserBodyBytes); err != nil {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid JSON body")
		return
	}
	if !req.Status.Valid() {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid user status")
		return
	}
	user, err := h.svc.SetStatus(requestContext(r), actor.ID, id, req.Status)
	if err != nil {
		writeUserError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, userEnvelope{Data: newUserPayload(user)})
}

// ResetPassword replaces a user's password. The service revokes every token of
// the target user, so old sessions stop working immediately.
func (h *UserHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := userIDFromPath(w, r)
	if !ok {
		return
	}
	var req resetPasswordRequest
	if err := platform.DecodeJSON(r, &req, maxUserBodyBytes); err != nil {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid JSON body")
		return
	}
	if err := h.svc.ResetPassword(requestContext(r), id, req.NewPassword); err != nil {
		writeUserError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// createUserRequest is the POST /api/users body. There is deliberately no role
// field: P0 only creates regular users.
type createUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// updateUserRequest is the PATCH /api/users/{id} body. PATCH only changes the
// status.
type updateUserRequest struct {
	Status identity.Status `json:"status"`
}

// resetPasswordRequest is the POST /api/users/{id}/reset-password body.
type resetPasswordRequest struct {
	NewPassword string `json:"new_password"`
}

// userListEnvelope is the generic list shape: data plus the opaque cursor and
// the has_more flag.
type userListEnvelope struct {
	Data       []userPayload `json:"data"`
	NextCursor string        `json:"next_cursor"`
	HasMore    bool          `json:"has_more"`
}

// userIDFromPath parses the positive integer {id} path value. On failure it
// writes the 400 response and reports false.
func userIDFromPath(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid user id")
		return 0, false
	}
	return id, true
}

// requestContext attaches the HTTP request metadata and the authenticated user
// to the context so the identity services stamp them on the audit entries they
// record, without widening every service signature.
func requestContext(r *http.Request) context.Context {
	ctx := auditapp.WithMeta(r.Context(), auditapp.RequestMeta{
		RequestID: platform.RequestID(r.Context()),
		IP:        clientIP(r),
		UserAgent: r.UserAgent(),
	})
	if user, ok := UserFromContext(ctx); ok {
		ctx = auditapp.WithActorID(ctx, user.ID)
	}
	return ctx
}

// writeUserError maps the identity domain errors to the shared error envelope.
func writeUserError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, identity.ErrUserNotFound):
		platform.WriteError(w, r, http.StatusNotFound, platform.CodeNotFound, "user not found")
	case errors.Is(err, identity.ErrUsernameTaken):
		platform.WriteError(w, r, http.StatusConflict, platform.CodeConflict, "username already taken")
	case errors.Is(err, identity.ErrLastAdmin):
		platform.WriteError(w, r, http.StatusConflict, platform.CodeConflict, "cannot disable the administrator account")
	case errors.Is(err, identity.ErrInvalidUsername):
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid username")
	case errors.Is(err, identity.ErrWeakPassword):
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "password must be between 10 and 128 characters")
	case errors.Is(err, identity.ErrInvalidStatus):
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid user status")
	default:
		platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal, "internal server error")
	}
}
