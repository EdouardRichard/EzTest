package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// maxSessionImportBodyBytes caps the session import body. A storage_state may
// carry up to ~5 MiB of JSON, so the import endpoint needs more room than an
// ordinary action (spec §6.2/§7.5).
const maxSessionImportBodyBytes int64 = 6 << 20

// maxSessionBodyBytes caps ordinary session and takeover request bodies.
const maxSessionBodyBytes int64 = 64 << 10

// maxTakeoverCompleteBodyBytes caps the complete-takeover body. A completion
// replaces the session's storage state or token headers, so it needs the same
// 6 MiB room as an import (spec §7.6); the 64 KiB ordinary action cap would
// reject a valid storageState. confirm/invalidate/revoke keep the small cap.
const maxTakeoverCompleteBodyBytes int64 = 6 << 20

// maxTakeoverExpiresInSeconds bounds expiresInSeconds so the duration conversion
// cannot overflow. The API has no documented upper bound; a year is far beyond
// any human takeover.
const maxTakeoverExpiresInSeconds = int64(365 * 24 * 60 * 60)

// SessionHandler serves the session, snapshot and manual takeover endpoints.
// Session leases are application-level only and are never exposed over HTTP
// (spec §6.5). Read routes are mounted plain (the global auth middleware still
// requires a login); write and action routes are wrapped by the caller's admin
// guard.
type SessionHandler struct {
	svc *application.SessionService
}

// NewSessionHandler returns the session handlers for svc.
func NewSessionHandler(svc *application.SessionService) *SessionHandler {
	return &SessionHandler{svc: svc}
}

// RegisterRoutes mounts the session and takeover endpoints on mux. guard wraps
// every write/action route; the server passes an admin guard built from
// RequireAdmin.
func (h *SessionHandler) RegisterRoutes(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	if guard == nil {
		guard = func(next http.Handler) http.Handler { return next }
	}

	mux.HandleFunc("GET /api/resources/sessions", h.ListSessions)
	mux.HandleFunc("GET /api/resources/sessions/{id}", h.GetSession)
	mux.HandleFunc("GET /api/resources/sessions/{id}/snapshots", h.ListSnapshots)
	mux.HandleFunc("GET /api/resources/takeovers", h.ListTakeovers)

	mux.Handle("POST /api/resources/accesses/{id}/sessions", guard(http.HandlerFunc(h.ImportSession)))
	mux.Handle("POST /api/resources/sessions/{id}/validate", guard(http.HandlerFunc(h.ValidateSession)))
	mux.Handle("POST /api/resources/sessions/{id}/confirm", guard(http.HandlerFunc(h.ConfirmSession)))
	mux.Handle("POST /api/resources/sessions/{id}/invalidate", guard(http.HandlerFunc(h.InvalidateSession)))
	mux.Handle("POST /api/resources/sessions/{id}/revoke", guard(http.HandlerFunc(h.RevokeSession)))
	mux.Handle("POST /api/resources/sessions/{id}/takeovers", guard(http.HandlerFunc(h.RequestTakeover)))
	mux.Handle("POST /api/resources/takeovers/{id}/complete", guard(http.HandlerFunc(h.CompleteTakeover)))
	mux.Handle("POST /api/resources/takeovers/{id}/cancel", guard(http.HandlerFunc(h.CancelTakeover)))
}

// sessionDTO is the public Session representation (camelCase, spec §7.5). The
// stored storage_state is never part of it; only its hash and shape counts are.
// The detail-only relations are omitted on list responses.
type sessionDTO struct {
	ID                int64        `json:"id"`
	AccountID         int64        `json:"accountId"`
	SystemID          int64        `json:"systemId"`
	EnvironmentID     int64        `json:"environmentId"`
	AccessID          int64        `json:"accessId"`
	State             string       `json:"state"`
	Acquisition       string       `json:"acquisition"`
	StorageStateKind  string       `json:"storageStateKind"`
	CookieCount       int          `json:"cookieCount"`
	OriginCount       int          `json:"originCount"`
	StorageStateHash  string       `json:"storageStateHash"`
	ExpiresAt         *time.Time   `json:"expiresAt"`
	LastValidatedAt   *time.Time   `json:"lastValidatedAt"`
	LastUsedAt        *time.Time   `json:"lastUsedAt"`
	FailureReason     string       `json:"failureReason"`
	InvalidatedReason string       `json:"invalidatedReason"`
	Expiring          bool         `json:"expiring"`
	CreatedAt         time.Time    `json:"createdAt"`
	UpdatedAt         time.Time    `json:"updatedAt"`
	LatestSnapshot    *snapshotDTO `json:"latestSnapshot,omitempty"`
	ActiveLease       *leaseDTO    `json:"activeLease,omitempty"`
	LatestTakeover    *takeoverDTO `json:"latestTakeover,omitempty"`
}

// snapshotDTO is the public Snapshot representation (camelCase, spec §6.3).
type snapshotDTO struct {
	ID             int64           `json:"id"`
	SessionID      int64           `json:"sessionId"`
	Kind           string          `json:"kind"`
	Items          json.RawMessage `json:"items"`
	Summary        json.RawMessage `json:"summary"`
	PermissionHash string          `json:"permissionHash"`
	Degraded       bool            `json:"degraded"`
	CapturedAt     time.Time       `json:"capturedAt"`
}

// leaseDTO is the public active-lease representation embedded in a session
// detail. Leases themselves have no route (spec §6.5).
type leaseDTO struct {
	ID         int64     `json:"id"`
	Holder     string    `json:"holder"`
	AcquiredAt time.Time `json:"acquiredAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

// takeoverDTO is the public Takeover representation (camelCase, spec §7.6).
type takeoverDTO struct {
	ID            int64           `json:"id"`
	SessionID     *int64          `json:"sessionId"`
	AccountID     int64           `json:"accountId"`
	SystemID      int64           `json:"systemId"`
	EnvironmentID int64           `json:"environmentId"`
	Reason        string          `json:"reason"`
	Status        string          `json:"status"`
	Instructions  json.RawMessage `json:"instructions"`
	RequestedBy   *int64          `json:"requestedBy"`
	RequestedAt   time.Time       `json:"requestedAt"`
	ExpiresAt     *time.Time      `json:"expiresAt"`
	CompletedAt   *time.Time      `json:"completedAt"`
}

// newSessionDTO maps a session to its API shape; expiring is the derived value
// of spec §6.1 and is never stored.
func newSessionDTO(session domain.Session, now time.Time) sessionDTO {
	return sessionDTO{
		ID:                session.ID,
		AccountID:         session.AccountID,
		SystemID:          session.SystemID,
		EnvironmentID:     session.EnvironmentID,
		AccessID:          session.AccessID,
		State:             session.State,
		Acquisition:       session.Acquisition,
		StorageStateKind:  session.StorageStateKind,
		CookieCount:       session.CookieCount,
		OriginCount:       session.OriginCount,
		StorageStateHash:  session.StorageStateHash,
		ExpiresAt:         session.ExpiresAt,
		LastValidatedAt:   session.LastValidatedAt,
		LastUsedAt:        session.LastUsedAt,
		FailureReason:     session.FailureReason,
		InvalidatedReason: session.InvalidatedReason,
		Expiring:          session.Expiring(now),
		CreatedAt:         session.CreatedAt,
		UpdatedAt:         session.UpdatedAt,
	}
}

// sessionDetailDTO returns a session with its detail-only relations attached.
func sessionDetailDTO(session domain.Session, snapshot *domain.Snapshot, lease *domain.Lease, takeover *domain.Takeover, now time.Time) sessionDTO {
	dto := newSessionDTO(session, now)
	dto.LatestSnapshot = newSnapshotDTO(snapshot)
	dto.ActiveLease = newLeaseDTO(lease)
	dto.LatestTakeover = newTakeoverDTO(takeover)
	return dto
}

// newSnapshotDTO maps a snapshot to its API shape, or nil when absent.
func newSnapshotDTO(snapshot *domain.Snapshot) *snapshotDTO {
	if snapshot == nil {
		return nil
	}
	items := snapshot.Items
	if len(items) == 0 {
		items = json.RawMessage(`{}`)
	}
	summary := snapshot.Summary
	if len(summary) == 0 {
		summary = json.RawMessage(`{}`)
	}
	return &snapshotDTO{
		ID:             snapshot.ID,
		SessionID:      snapshot.SessionID,
		Kind:           snapshot.Kind,
		Items:          items,
		Summary:        summary,
		PermissionHash: snapshot.PermissionHash,
		Degraded:       snapshot.Degraded,
		CapturedAt:     snapshot.CapturedAt,
	}
}

// newLeaseDTO maps an active lease to its API shape, or nil when absent.
func newLeaseDTO(lease *domain.Lease) *leaseDTO {
	if lease == nil {
		return nil
	}
	return &leaseDTO{
		ID:         lease.ID,
		Holder:     lease.Holder,
		AcquiredAt: lease.AcquiredAt,
		ExpiresAt:  lease.ExpiresAt,
	}
}

// newTakeoverDTO maps a takeover to its API shape, or nil when absent.
func newTakeoverDTO(takeover *domain.Takeover) *takeoverDTO {
	if takeover == nil {
		return nil
	}
	instructions := takeover.Instructions
	if len(instructions) == 0 {
		instructions = json.RawMessage(`{}`)
	}
	return &takeoverDTO{
		ID:            takeover.ID,
		SessionID:     takeover.SessionID,
		AccountID:     takeover.AccountID,
		SystemID:      takeover.SystemID,
		EnvironmentID: takeover.EnvironmentID,
		Reason:        takeover.Reason,
		Status:        takeover.Status,
		Instructions:  instructions,
		RequestedBy:   takeover.RequestedBy,
		RequestedAt:   takeover.RequestedAt,
		ExpiresAt:     takeover.ExpiresAt,
		CompletedAt:   takeover.CompletedAt,
	}
}

// importSessionRequest is the POST /accesses/{id}/sessions body (spec §7.5).
type importSessionRequest struct {
	EnvironmentID int64             `json:"environmentId"`
	Kind          string            `json:"kind"`
	StorageState  json.RawMessage   `json:"storageState"`
	Headers       map[string]string `json:"headers"`
	ExpiresAt     *time.Time        `json:"expiresAt"`
	Note          string            `json:"note"`
}

// confirmSessionRequest is the POST /sessions/{id}/confirm body.
type confirmSessionRequest struct {
	Note string `json:"note"`
}

// reasonRequest is the shared invalidate/revoke body.
type reasonRequest struct {
	Reason string `json:"reason"`
}

// requestTakeoverRequest is the POST /sessions/{id}/takeovers body.
type requestTakeoverRequest struct {
	Reason           string          `json:"reason"`
	Instructions     json.RawMessage `json:"instructions"`
	ExpiresInSeconds int64           `json:"expiresInSeconds"`
}

// completeTakeoverRequest is the POST /takeovers/{id}/complete body; it has the
// import state shape (spec §7.6).
type completeTakeoverRequest struct {
	Kind         string            `json:"kind"`
	StorageState json.RawMessage   `json:"storageState"`
	Headers      map[string]string `json:"headers"`
}

// parseSessionQueryID parses an optional positive-int filter parameter.
func parseSessionQueryID(r *http.Request, name string) (int64, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		reason := map[string]string{
			"accountId":     "invalid_account_id",
			"systemId":      "invalid_system_id",
			"environmentId": "invalid_environment_id",
		}[name]
		if reason == "" {
			reason = "invalid_" + name
		}
		return 0, domain.NewInvalid(name, reason, name+" must be a positive integer")
	}
	return id, nil
}

// ListSessions returns one cursor page of sessions.
func (h *SessionHandler) ListSessions(w http.ResponseWriter, r *http.Request) {
	page, err := ParsePage(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	accountID, err := parseSessionQueryID(r, "accountId")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	systemID, err := parseSessionQueryID(r, "systemId")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	environmentID, err := parseSessionQueryID(r, "environmentId")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	sessions, hasMore, err := h.svc.List(r.Context(), domain.SessionFilter{
		AccountID:     accountID,
		SystemID:      systemID,
		EnvironmentID: environmentID,
		State:         r.URL.Query().Get("state"),
		Page:          page,
	})
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	now := time.Now()
	items := make([]sessionDTO, len(sessions))
	for i, session := range sessions {
		items[i] = newSessionDTO(session, now)
	}
	next := ""
	if hasMore && len(sessions) > 0 {
		next = platform.EncodeCursor(sessions[len(sessions)-1].ID)
	}
	WritePage(w, items, next, hasMore)
}

// GetSession returns one session with its latest snapshot, active lease and
// latest takeover.
func (h *SessionHandler) GetSession(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	session, snapshot, lease, takeover, err := h.svc.Get(r.Context(), id)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, sessionDetailDTO(*session, snapshot, lease, takeover, time.Now()))
}

// ListSnapshots returns one cursor page of a session's snapshots.
func (h *SessionHandler) ListSnapshots(w http.ResponseWriter, r *http.Request) {
	sessionID, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	page, err := ParsePage(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	snapshots, hasMore, err := h.svc.ListSnapshots(r.Context(), sessionID, page)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	items := make([]snapshotDTO, len(snapshots))
	for i, snapshot := range snapshots {
		items[i] = *newSnapshotDTO(&snapshot)
	}
	next := ""
	if hasMore && len(snapshots) > 0 {
		next = platform.EncodeCursor(snapshots[len(snapshots)-1].ID)
	}
	WritePage(w, items, next, hasMore)
}

// ImportSession creates a session from an access's imported login state.
func (h *SessionHandler) ImportSession(w http.ResponseWriter, r *http.Request) {
	accessID, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var req importSessionRequest
	if err := DecodeJSON(w, r, &req, maxSessionImportBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	session, snapshot, err := h.svc.Import(r.Context(), accessID, application.ImportSessionInput{
		EnvironmentID: req.EnvironmentID,
		Kind:          req.Kind,
		StorageState:  req.StorageState,
		Headers:       req.Headers,
		ExpiresAt:     req.ExpiresAt,
		Note:          req.Note,
		ActorID:       ActorID(r.Context()),
	})
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusCreated, sessionDetailDTO(*session, snapshot, nil, nil, time.Now()))
}

// ValidateSession re-runs the environment's login_check.
func (h *SessionHandler) ValidateSession(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	session, snapshot, err := h.svc.Validate(r.Context(), id)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, sessionDetailDTO(*session, snapshot, nil, nil, time.Now()))
}

// ConfirmSession marks a pending/needs_manual session valid by hand.
func (h *SessionHandler) ConfirmSession(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var req confirmSessionRequest
	if err := DecodeJSON(w, r, &req, maxSessionBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	session, snapshot, err := h.svc.Confirm(r.Context(), id, req.Note, ActorID(r.Context()))
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, sessionDetailDTO(*session, snapshot, nil, nil, time.Now()))
}

// InvalidateSession marks a session invalid and releases its leases.
func (h *SessionHandler) InvalidateSession(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var req reasonRequest
	if err := DecodeJSON(w, r, &req, maxSessionBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	session, err := h.svc.Invalidate(r.Context(), id, req.Reason)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newSessionDTO(*session, time.Now()))
}

// RevokeSession marks a session revoked and releases its leases.
func (h *SessionHandler) RevokeSession(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var req reasonRequest
	if err := DecodeJSON(w, r, &req, maxSessionBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	session, err := h.svc.Revoke(r.Context(), id, req.Reason)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newSessionDTO(*session, time.Now()))
}

// RequestTakeover creates a waiting manual takeover and returns it with the
// updated session (spec §7.6).
func (h *SessionHandler) RequestTakeover(w http.ResponseWriter, r *http.Request) {
	sessionID, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var req requestTakeoverRequest
	if err := DecodeJSON(w, r, &req, maxSessionBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	if req.ExpiresInSeconds > maxTakeoverExpiresInSeconds {
		WriteServiceError(w, r, domain.NewInvalid("expiresInSeconds", "invalid_expires_in_seconds",
			"expiresInSeconds must be at most one year"))
		return
	}
	expiresIn := time.Duration(req.ExpiresInSeconds) * time.Second
	takeover, session, err := h.svc.RequestTakeover(r.Context(), sessionID, req.Reason,
		req.Instructions, expiresIn, ActorID(r.Context()))
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusCreated, map[string]any{
		"takeover": newTakeoverDTO(takeover),
		"session":  sessionDetailDTO(*session, nil, nil, takeover, time.Now()),
	})
}

// ListTakeovers returns one cursor page of takeovers.
func (h *SessionHandler) ListTakeovers(w http.ResponseWriter, r *http.Request) {
	page, err := ParsePage(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	takeovers, hasMore, err := h.svc.ListTakeovers(r.Context(), domain.TakeoverFilter{
		Status: r.URL.Query().Get("status"),
		Page:   page,
	})
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	items := make([]takeoverDTO, len(takeovers))
	for i, takeover := range takeovers {
		items[i] = *newTakeoverDTO(&takeover)
	}
	next := ""
	if hasMore && len(takeovers) > 0 {
		next = platform.EncodeCursor(takeovers[len(takeovers)-1].ID)
	}
	WritePage(w, items, next, hasMore)
}

// CompleteTakeover replaces the session's login state and completes the
// takeover, returning both.
func (h *SessionHandler) CompleteTakeover(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var req completeTakeoverRequest
	if err := DecodeJSON(w, r, &req, maxTakeoverCompleteBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	takeover, session, snapshot, err := h.svc.CompleteTakeover(r.Context(), id,
		application.ReplaceSessionStateInput{
			Kind:         req.Kind,
			StorageState: req.StorageState,
			Headers:      req.Headers,
		}, ActorID(r.Context()))
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, map[string]any{
		"takeover": newTakeoverDTO(takeover),
		"session":  sessionDetailDTO(*session, snapshot, nil, takeover, time.Now()),
	})
}

// CancelTakeover cancels a waiting takeover; the session stays needs_manual.
func (h *SessionHandler) CancelTakeover(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	takeover, err := h.svc.CancelTakeover(r.Context(), id)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newTakeoverDTO(takeover))
}
