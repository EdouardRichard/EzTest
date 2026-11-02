package resourceapp

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// DefaultSessionTTL bounds how long a captured SUT login session may be reused
// before the explorer logs in again. Session reuse is an optimization: an
// expired or missing entry always falls back to a fresh login.
const DefaultSessionTTL = 30 * time.Minute

// LoginSession is one captured SUT browser session (storageState) owned by the
// resource context. It is keyed by the version SUT and account role that
// produced it. StorageState carries cookies/localStorage and is sensitive: it
// is never returned by an HTTP projection or written to a version fact; facts
// and knowledge only carry StorageStateRef.
type LoginSession struct {
	VersionID       int64
	VersionSUTID    int64
	EnvironmentID   int64
	AccountRole     string
	StorageStateRef string
	// StorageState is the executor storageState JSON ({cookies, origins}).
	StorageState json.RawMessage
	// LoginURL and Route locate the form that produced the session.
	LoginURL string
	Route    string
	// Recipe is the credential-free "how to log in" description (selectors and
	// steps). It is what a login_recipe knowledge draft can sediment.
	Recipe map[string]any
	// VerifiedAt is when the session last logged in successfully; ExpiresAt is
	// the derived reuse deadline.
	VerifiedAt time.Time
	ExpiresAt  time.Time
}

// Expired reports whether the session may no longer be reused at now.
func (s LoginSession) Expired(now time.Time) bool {
	return !s.ExpiresAt.IsZero() && !now.Before(s.ExpiresAt)
}

type sessionKey struct {
	versionSUTID  int64
	environmentID int64
	role          string
}

// SessionStore is the in-memory registry of reusable login sessions. The
// executor owns the storageState bytes; this store keeps the reference and the
// state needed to re-seed a restarted executor. It deliberately does not
// persist to the database (0.2.1 has no session migration): a process restart
// drops the cache and the explorer simply logs in again.
//
// A stored ref can still go stale while this process lives (executor restart),
// so reuse callers must verify the ref with the executor and treat an unknown
// ref as a cache miss: Invalidate and log in again.
type SessionStore struct {
	mu       sync.Mutex
	ttl      time.Duration
	now      func() time.Time
	sessions map[sessionKey]LoginSession
}

// NewSessionStore returns a session registry with the given reuse TTL; a
// non-positive TTL falls back to DefaultSessionTTL. The store is safe for
// concurrent use.
func NewSessionStore(ttl time.Duration) *SessionStore {
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	return &SessionStore{
		ttl:      ttl,
		now:      time.Now,
		sessions: make(map[sessionKey]LoginSession),
	}
}

// Find returns a non-expired session for the (version SUT, environment, role)
// binding. An expired entry is removed and reported as missing.
func (s *SessionStore) Find(_ context.Context, versionSUTID, environmentID int64, role string) (LoginSession, bool) {
	if s == nil {
		return LoginSession{}, false
	}
	key, ok := newSessionKey(versionSUTID, environmentID, role)
	if !ok {
		return LoginSession{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, found := s.sessions[key]
	if !found {
		return LoginSession{}, false
	}
	if session.Expired(s.now()) {
		delete(s.sessions, key)
		return LoginSession{}, false
	}
	return session, true
}

// Save records a successful login session. The zero VerifiedAt is set to now
// and a zero ExpiresAt becomes VerifiedAt + the store TTL.
func (s *SessionStore) Save(_ context.Context, session LoginSession) error {
	if s == nil {
		return platform.NewError(500, "internal", "login session store is not configured")
	}
	session.AccountRole = strings.TrimSpace(session.AccountRole)
	key, ok := newSessionKey(session.VersionSUTID, session.EnvironmentID, session.AccountRole)
	if !ok {
		return platform.Invalid("login_session", "requires a positive version SUT and environment and a non-empty role")
	}
	if session.VersionID <= 0 {
		return platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if strings.TrimSpace(session.StorageStateRef) == "" {
		return platform.Invalid("storage_state_ref", "must not be empty")
	}
	if len(session.StorageState) == 0 {
		return platform.Invalid("storage_state", "must not be empty")
	}
	now := s.now()
	if session.VerifiedAt.IsZero() {
		session.VerifiedAt = now
	}
	if session.ExpiresAt.IsZero() {
		session.ExpiresAt = session.VerifiedAt.Add(s.ttl)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[key] = session
	return nil
}

// Invalidate drops a cached session (e.g. after the SUT rejected the stored
// credentials or the app reported the session expired).
func (s *SessionStore) Invalidate(_ context.Context, versionSUTID, environmentID int64, role string) {
	if s == nil {
		return
	}
	key, ok := newSessionKey(versionSUTID, environmentID, role)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, key)
}

func newSessionKey(versionSUTID, environmentID int64, role string) (sessionKey, bool) {
	role = strings.TrimSpace(role)
	if versionSUTID <= 0 || environmentID <= 0 || role == "" {
		return sessionKey{}, false
	}
	return sessionKey{versionSUTID: versionSUTID, environmentID: environmentID, role: role}, true
}
