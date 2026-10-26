// Package httpapi is the identity context's HTTP interface: authentication
// handlers, the bearer-token middleware and the role guards. It only calls the
// application layer; request/response DTOs stay separate from domain types.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
)

const (
	// maxLoginBodyBytes caps the login request body so a client cannot make the
	// server buffer an unbounded amount of JSON. A valid login body is tiny (a
	// bounded username plus a <=128 character password), so 4 KiB is ample.
	maxLoginBodyBytes = 4 << 10
	// loginMaxEntries bounds the in-memory limiter map so unauthenticated
	// requests cannot grow it without bound.
	loginMaxEntries = 10_000
	// loginMaxFailures is how many failed logins are tolerated per window.
	loginMaxFailures = 5
	// loginWindow is the rolling window the failures are counted in.
	loginWindow = time.Minute
	// loginLockDuration is how long an identity stays locked out.
	loginLockDuration = time.Minute
)

// Authenticator resolves a raw bearer token to its user. It is the only
// dependency of AuthMiddleware.
type Authenticator interface {
	Authenticate(ctx context.Context, rawToken string) (identity.User, error)
}

// AuthService is the auth use-case port consumed by the HTTP handlers. The
// application AuthService satisfies it.
type AuthService interface {
	Authenticator
	Login(ctx context.Context, username, password string) (application.LoginResult, error)
	Logout(ctx context.Context, rawToken string) error
}

// AuthHandler serves the login, logout and me endpoints.
type AuthHandler struct {
	svc     AuthService
	limiter *LoginLimiter
}

// NewAuthHandler returns handlers for svc. A nil limiter means a limiter with
// the real clock.
func NewAuthHandler(svc AuthService, limiter *LoginLimiter) *AuthHandler {
	if limiter == nil {
		limiter = NewLoginLimiter(time.Now)
	}
	return &AuthHandler{svc: svc, limiter: limiter}
}

// RegisterAuthRoutes mounts the auth endpoints on mux. Login is public;
// logout and me require a valid bearer token.
func RegisterAuthRoutes(mux *http.ServeMux, svc AuthService, limiter *LoginLimiter) {
	handler := NewAuthHandler(svc, limiter)
	mux.HandleFunc("POST /api/auth/login", handler.Login)
	authenticated := AuthMiddleware(svc)
	mux.Handle("GET /api/auth/me", authenticated(http.HandlerFunc(handler.Me)))
	mux.Handle("POST /api/auth/logout", authenticated(http.HandlerFunc(handler.Logout)))
}

// Login verifies credentials and returns the raw bearer token exactly once.
// Locked identities get 429 without reaching the auth service.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLoginBodyBytes))
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid JSON body")
		return
	}

	// Reject usernames that cannot name an account before they become limiter
	// keys or reach the (comparatively expensive) auth service. The 401 is
	// deliberately indistinguishable from wrong credentials.
	if err := identity.ValidateUsername(req.Username); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "invalid username or password")
		return
	}

	key := loginKey(clientIP(r), req.Username)
	if h.limiter.Locked(key) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many failed login attempts, try again later")
		return
	}

	result, err := h.svc.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		if errors.Is(err, identity.ErrUnauthenticated) {
			h.limiter.Fail(key)
			writeError(w, http.StatusUnauthorized, "unauthenticated", "invalid username or password")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "internal server error")
		return
	}
	h.limiter.Reset(key)
	writeJSON(w, http.StatusOK, loginEnvelope{Data: loginData{
		Token:     result.Token,
		ExpiresAt: result.ExpiresAt,
		User:      newUserPayload(result.User),
	}})
}

// Me returns the user resolved by AuthMiddleware.
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		writeUnauthenticated(w)
		return
	}
	writeJSON(w, http.StatusOK, userEnvelope{Data: newUserPayload(user)})
}

// Logout revokes the bearer token of the current request.
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	rawToken, ok := bearerToken(r)
	if !ok {
		writeUnauthenticated(w)
		return
	}
	if err := h.svc.Logout(r.Context(), rawToken); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "internal server error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// LoginLimiter throttles failed logins in memory, keyed by client IP and
// username. P0 runs a single instance, so a process-local map is sufficient.
// The map is bounded and expired entries are swept opportunistically, so an
// unauthenticated client cannot grow it without bound.
type LoginLimiter struct {
	mu       sync.Mutex
	now      func() time.Time
	attempts map[string]loginAttempt
}

type loginAttempt struct {
	failures    int
	windowStart time.Time
	lockedUntil time.Time
}

// NewLoginLimiter returns a limiter that reads time from now. A nil clock means
// time.Now; tests inject a fake clock.
func NewLoginLimiter(now func() time.Time) *LoginLimiter {
	if now == nil {
		now = time.Now
	}
	return &LoginLimiter{now: now, attempts: make(map[string]loginAttempt)}
}

// Locked reports whether key is currently locked out. An entry whose counting
// window has elapsed and whose lock has passed is forgotten here, so lookups
// also reclaim abandoned identities.
func (l *LoginLimiter) Locked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	attempt, ok := l.attempts[key]
	if !ok {
		return false
	}
	now := l.now()
	if now.Before(attempt.lockedUntil) {
		return true
	}
	if now.Sub(attempt.windowStart) >= loginWindow {
		delete(l.attempts, key)
	}
	return false
}

// Fail records one failed login. The fifth failure inside a one-minute window
// locks the key for one minute. Inserting a new identity first sweeps expired
// entries (so abandoned keys cannot accumulate) and then, when the map is at
// loginMaxEntries, evicts the oldest remaining entry so the map stays bounded.
func (l *LoginLimiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	attempt, ok := l.attempts[key]
	if !ok || now.Sub(attempt.windowStart) >= loginWindow {
		l.evictExpired(now)
		if !ok && len(l.attempts) >= loginMaxEntries {
			l.evictOldest()
		}
		attempt = loginAttempt{windowStart: now}
	}
	attempt.failures++
	if attempt.failures >= loginMaxFailures {
		attempt.lockedUntil = now.Add(loginLockDuration)
	}
	l.attempts[key] = attempt
}

// evictExpired removes entries whose counting window has elapsed and whose lock
// has passed. Callers must hold l.mu.
func (l *LoginLimiter) evictExpired(now time.Time) {
	for key, attempt := range l.attempts {
		if now.Sub(attempt.windowStart) >= loginWindow && !now.Before(attempt.lockedUntil) {
			delete(l.attempts, key)
		}
	}
}

// evictOldest removes the entry whose counting window started earliest. Callers
// must hold l.mu and the map must be non-empty.
func (l *LoginLimiter) evictOldest() {
	var oldestKey string
	var oldestStart time.Time
	for key, attempt := range l.attempts {
		if oldestKey == "" || attempt.windowStart.Before(oldestStart) {
			oldestKey = key
			oldestStart = attempt.windowStart
		}
	}
	delete(l.attempts, oldestKey)
}

// Reset forgets the failures of a key after a successful login.
func (l *LoginLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}

// loginRequest is the POST /api/auth/login body.
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// userPayload is the public representation of an account. It deliberately has
// no password hash field.
type userPayload struct {
	ID       int64           `json:"id"`
	Username string          `json:"username"`
	Role     identity.Role   `json:"role"`
	Status   identity.Status `json:"status"`
}

func newUserPayload(user identity.User) userPayload {
	return userPayload{
		ID:       user.ID,
		Username: user.Username,
		Role:     user.Role,
		Status:   user.Status,
	}
}

type loginData struct {
	Token     string      `json:"token"`
	ExpiresAt time.Time   `json:"expires_at"`
	User      userPayload `json:"user"`
}

type loginEnvelope struct {
	Data loginData `json:"data"`
}

type userEnvelope struct {
	Data userPayload `json:"data"`
}

// errorEnvelope is the local error shape until Task 6 adds the shared API
// error envelope.
type errorEnvelope struct {
	Error errorPayload `json:"error"`
}

type errorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorEnvelope{Error: errorPayload{Code: code, Message: message}})
}

// clientIP extracts the peer address of the request. Forwarding headers are
// deliberately ignored: they are client-controlled and this limit is keyed on
// the direct peer.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// loginKey scopes failed-login accounting to a client IP and username pair.
func loginKey(ip, username string) string {
	return ip + "|" + strings.TrimSpace(username)
}
