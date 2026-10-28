package resource

import (
	"context"
	"errors"
	"fmt"
	"time"

	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	resourcedomain "github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// SessionService is the narrow slice of the P2 session service the adapter
// needs. *resource/application.SessionService satisfies it.
type SessionService interface {
	Get(ctx context.Context, id int64) (*resourcedomain.Session, *resourcedomain.Snapshot, *resourcedomain.Lease, *resourcedomain.Takeover, error)
}

// Clock returns the current time; injectable so the validity window can be
// tested without sleeping. A nil clock falls back to time.Now.
type Clock interface {
	Now() time.Time
}

// SessionAdapter implements application.SessionGateway on top of the P2 session
// service.
type SessionAdapter struct {
	sessions SessionService
	clock    Clock
}

// NewSessionAdapter wires the adapter to the P2 session service and the
// optional clock.
func NewSessionAdapter(sessions SessionService, clock Clock) *SessionAdapter {
	return &SessionAdapter{sessions: sessions, clock: clock}
}

var _ mindmapapp.SessionGateway = (*SessionAdapter)(nil)

// GetForExplore returns the cookies of a valid, unexpired P2 session for the
// read-only HTTP explorer (spec §6.9). Only the browser storage_state cookie jar
// is exposed: localStorage and token headers are never read, and the session is
// never written back.
func (a *SessionAdapter) GetForExplore(ctx context.Context, sessionID int64) (mindmapapp.SessionSnapshot, error) {
	if a == nil || a.sessions == nil {
		return mindmapapp.SessionSnapshot{}, errors.New("mindmap: session service is not configured")
	}
	session, _, _, _, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return mindmapapp.SessionSnapshot{}, mapResourceError(err, "session", sessionID)
	}
	if session == nil {
		return mindmapapp.SessionSnapshot{}, mindmapdomain.NewNotFound("session", sessionID)
	}
	if err := a.validateExploreSession(session); err != nil {
		return mindmapapp.SessionSnapshot{}, err
	}

	kind := session.StorageStateKind
	if kind == "" {
		kind = resourcedomain.StorageStateKindStorageState
	}
	if kind != resourcedomain.StorageStateKindStorageState {
		// A token session carries request headers, not a cookie jar, so the
		// explorer cannot use it; no token is ever injected (spec §6.9).
		return mindmapapp.SessionSnapshot{}, sessionNotValid(sessionID,
			fmt.Sprintf("session %d does not carry a browser storage state", sessionID))
	}
	state, err := resourcedomain.ParseStorageState(kind, session.StorageState)
	if err != nil {
		return mindmapapp.SessionSnapshot{}, mapResourceError(err, "session", sessionID)
	}
	cookies := make(map[string]string, len(state.Cookies))
	for _, cookie := range state.Cookies {
		cookies[cookie.Name] = cookie.Value
	}
	return mindmapapp.SessionSnapshot{
		ID:            session.ID,
		State:         session.State,
		EnvironmentID: session.EnvironmentID,
		Cookies:       cookies,
	}, nil
}

// validateExploreSession enforces the spec §6.9 state gate: exploration may only
// read a session whose state is valid and whose expires_at has not passed. The
// P2 get already lazily expires rows, so this is a fail-closed re-check.
func (a *SessionAdapter) validateExploreSession(session *resourcedomain.Session) error {
	if session.State != resourcedomain.StateValid {
		return sessionNotValid(session.ID,
			fmt.Sprintf("session %d is %s, exploration requires a valid session", session.ID, session.State))
	}
	if session.IsExpired(a.now()) {
		return sessionNotValid(session.ID,
			fmt.Sprintf("session %d expires before the current time", session.ID))
	}
	return nil
}

// sessionNotValid builds the mindmap state conflict of an unusable session.
func sessionNotValid(sessionID int64, message string) error {
	return mindmapdomain.NewStateConflict("session_not_valid", message)
}

// now returns the injected clock reading, or the wall clock when none is wired.
func (a *SessionAdapter) now() time.Time {
	if a.clock == nil {
		return time.Now()
	}
	return a.clock.Now()
}
