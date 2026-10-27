package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// ActionSessionDeleted is the audit action recorded when a session is deleted.
const ActionSessionDeleted = "agent.session.deleted"

const (
	// sessionTitleMaxRunes is the API's title bound (design spec §10.3).
	sessionTitleMaxRunes = 120
	// sessionDeleteAbortTimeout bounds how long DELETE waits for the aborted run
	// to become idle before it gives up with domain.ErrRunActive.
	sessionDeleteAbortTimeout = 5 * time.Second
)

// RunControl is the subset of the run service the session lifecycle needs. The
// concrete *RunService satisfies it; the interface lives here so the session use
// case does not depend on the execution loop's implementation.
type RunControl interface {
	Start(ctx context.Context, sessionID int64, message string, actor *int64) (domain.StartResult, error)
	Abort(ctx context.Context, sessionID int64, actor *int64) (domain.AbortResult, error)
	WaitIdle(ctx context.Context, sessionID int64, timeout time.Duration) bool
}

// SessionService is the session use case: cursor-paged listing, creation on the
// default or an explicit profile, detail assembly (running run plus newest
// terminal run), title updates, abort-then-delete and session-scoped message
// reads.
type SessionService struct {
	profiles domain.ProfileRepository
	sessions domain.SessionRepository
	runs     domain.RunRepository
	messages domain.MessageRepository
	control  RunControl
	audit    AuditSink
}

// NewSessionService returns the session service. control may be nil only in
// tests that never delete a session; a nil audit sink disables auditing.
func NewSessionService(
	profiles domain.ProfileRepository,
	sessions domain.SessionRepository,
	runRepo domain.RunRepository,
	messages domain.MessageRepository,
	control RunControl,
	audit AuditSink,
) *SessionService {
	return &SessionService{
		profiles: profiles,
		sessions: sessions,
		runs:     runRepo,
		messages: messages,
		control:  control,
		audit:    audit,
	}
}

// List returns one page of session summaries plus the repository's has-more
// flag. The cursor and limit are passed through unchanged: the HTTP layer is
// responsible for decoding and bounding them.
func (s *SessionService) List(ctx context.Context, cursor int64, limit int, profileID *int64) ([]domain.SessionSummary, bool, error) {
	return s.sessions.List(ctx, cursor, limit, profileID)
}

// Create creates a session on profileID, or on the builtin profile when
// profileID is 0. A missing profile is domain.ErrNotFound; a disabled profile is
// domain.ErrProfileDisabled. The title must be at most 120 characters.
func (s *SessionService) Create(ctx context.Context, profileID int64, title string, actor *int64) (domain.Session, error) {
	if err := validateSessionTitle(title); err != nil {
		return domain.Session{}, err
	}
	profile, err := s.profileForSession(ctx, profileID)
	if err != nil {
		return domain.Session{}, err
	}
	if !profile.Enabled {
		return domain.Session{}, domain.ErrProfileDisabled
	}
	return s.sessions.Create(ctx, profile.ID, title, actor)
}

// profileForSession resolves the profile a new session is created on. A zero id
// means "the builtin profile", which is discovered through List because the
// repository exposes no builtin lookup.
func (s *SessionService) profileForSession(ctx context.Context, profileID int64) (domain.AgentProfile, error) {
	if profileID != 0 {
		return s.profiles.Get(ctx, profileID)
	}
	profiles, err := s.profiles.List(ctx)
	if err != nil {
		return domain.AgentProfile{}, err
	}
	for _, profile := range profiles {
		if profile.Builtin {
			return profile, nil
		}
	}
	return domain.AgentProfile{}, domain.ErrNotFound
}

// Get returns the session together with its running run (if any) and its newest
// terminal run (if any). The two run lookups are independent: a session that has
// only completed runs, or only a running one, still resolves.
func (s *SessionService) Get(ctx context.Context, id int64) (domain.SessionDetail, error) {
	session, err := s.sessions.Get(ctx, id)
	if err != nil {
		return domain.SessionDetail{}, err
	}
	detail := domain.SessionDetail{Session: session}
	active, err := s.runs.Active(ctx, id)
	switch {
	case err == nil:
		detail.ActiveRun = &active
		detail.Running = true
	case errors.Is(err, domain.ErrNotFound):
	default:
		return domain.SessionDetail{}, err
	}
	latest, err := s.runs.Latest(ctx, id)
	switch {
	case err == nil:
		detail.LastRun = &latest
	case errors.Is(err, domain.ErrNotFound):
	default:
		return domain.SessionDetail{}, err
	}
	return detail, nil
}

// UpdateTitle changes a session title after checking the API length bound.
func (s *SessionService) UpdateTitle(ctx context.Context, id int64, title string) (domain.Session, error) {
	if err := validateSessionTitle(title); err != nil {
		return domain.Session{}, err
	}
	return s.sessions.UpdateTitle(ctx, id, title)
}

// Delete removes a session. A running session is aborted first and the service
// waits up to five seconds for it to become idle; a run that is still active
// afterwards keeps the session and returns domain.ErrRunActive. A deleted
// session records agent.session.deleted.
func (s *SessionService) Delete(ctx context.Context, id int64, actor *int64) error {
	if _, err := s.sessions.Get(ctx, id); err != nil {
		return err
	}
	if s.control != nil {
		if _, err := s.control.Abort(ctx, id, actor); err != nil {
			return err
		}
		if !s.control.WaitIdle(ctx, id, sessionDeleteAbortTimeout) {
			return domain.ErrRunActive
		}
	}
	if err := s.sessions.Delete(ctx, id); err != nil {
		return err
	}
	s.recordSessionDeleted(ctx, id, actor)
	return nil
}

// Message returns a message only when it belongs to sessionID. A mismatch is
// reported as domain.ErrNotFound so a session id from the path can never expose
// another session's message.
func (s *SessionService) Message(ctx context.Context, sessionID, messageID int64) (domain.Message, error) {
	message, err := s.messages.Get(ctx, messageID)
	if err != nil {
		return domain.Message{}, err
	}
	if message.SessionID != sessionID {
		return domain.Message{}, domain.ErrNotFound
	}
	return message, nil
}

// recordSessionDeleted appends the agent.session.deleted entry. Auditing must
// never fail the request, so a sink error is only logged.
func (s *SessionService) recordSessionDeleted(ctx context.Context, id int64, actor *int64) {
	if s.audit == nil {
		return
	}
	event := AuditEvent{
		Action:      ActionSessionDeleted,
		ActorUserID: actor,
		TargetType:  "agent_session",
		TargetID:    strconv.FormatInt(id, 10),
		Metadata:    map[string]any{"session_id": id},
	}
	if err := s.audit.Record(ctx, event); err != nil {
		slog.ErrorContext(ctx, "agent: record session deletion audit entry",
			"session_id", id, "error", err)
	}
}

// validateSessionTitle enforces the 0–120 character title bound. The count is in
// runes, matching the design spec's "字符" wording rather than bytes.
func validateSessionTitle(title string) error {
	if n := utf8.RuneCountInString(title); n > sessionTitleMaxRunes {
		return fmt.Errorf("agent: title has %d characters, must be at most %d", n, sessionTitleMaxRunes)
	}
	return nil
}
