package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// Session rules and defaults from spec §6.1-§6.5.
const (
	// loginCheckNotConfigured is the failure_reason written when the
	// environment has no login_check; the session goes to needs_manual.
	loginCheckNotConfigured = "login_check_not_configured"
	// loginFailed is the failure_reason of a denied login_check.
	loginFailed = "login_failed"
	// envUnreachable is the failure_reason of a transport-level check failure.
	envUnreachable = "env_unreachable"

	// snapshotKindLogin is the only session_snapshot.kind the DDL allows.
	snapshotKindLogin = "login"
	// manualSnapshotReason is the summary.reason of a manual confirmation.
	manualSnapshotReason = "manual_confirmation"

	// sessionTTLFallbackSeconds is the final TTL fallback of spec §6.2.
	sessionTTLFallbackSeconds = 3600

	// defaultTakeoverTTL is the default takeover lifetime (spec §6.4).
	defaultTakeoverTTL = 30 * time.Minute
	// defaultLeaseTTL is the default lease lifetime (spec §6.5).
	defaultLeaseTTL = 30 * time.Minute

	// invalidatedLeaseReason and revokedLeaseReason are the released_reason
	// values of the state-machine side effects. An expired lease is reclaimed by
	// the repository on the next acquisition.
	invalidatedLeaseReason = "invalidated"
	revokedLeaseReason     = "revoked"
)

// SessionService implements the session, manual takeover and lease use cases
// (spec §6.1-§6.5). Every state change is a compare-and-swap on the current
// state; a lost CAS surfaces as a domain state conflict.
type SessionService struct {
	sessions     domain.SessionRepository
	accesses     domain.AccessRepository
	environments domain.EnvironmentRepository
	systems      domain.SystemRepository
	checker      LoginChecker
	clock        Clock

	// Optional auto-login dependencies wired by ConfigureAcquisition (spec
	// §5.9). A service without them only reuses valid sessions.
	flows    domain.AuthFlowRepository
	login    LoginBrowser
	accounts domain.AccountRepository
	flights  acquisitionFlights

	// audit is the optional write-path audit sink (spec §10.4). A nil sink
	// keeps the service silent (tests and partially wired processes).
	audit AuditSink
}

// SessionServiceOption customises the session service dependencies.
type SessionServiceOption func(*SessionService)

// WithSessionAuditSink injects the resource write-path audit sink. Entries
// carry ids and states only: the stored storage state and takeover instructions
// are never recorded.
func WithSessionAuditSink(sink AuditSink) SessionServiceOption {
	return func(s *SessionService) {
		if sink != nil {
			s.audit = sink
		}
	}
}

// ImportSessionInput is the create-session request. Headers carries the token
// document of the HTTP DTO; StorageState carries the browser document.
type ImportSessionInput struct {
	EnvironmentID int64
	Kind          string
	StorageState  json.RawMessage
	Headers       map[string]string
	ExpiresAt     *time.Time
	Note          string
	ActorID       *int64
}

// ReplaceSessionStateInput is the complete-takeover request; it has the import
// shape without the environment and expiry overrides.
type ReplaceSessionStateInput struct {
	Kind         string
	StorageState json.RawMessage
	Headers      map[string]string
}

// NewSessionService wires the session use cases to their repositories, the
// read-only login checker and the injectable clock.
func NewSessionService(sessions domain.SessionRepository, accesses domain.AccessRepository, environments domain.EnvironmentRepository, systems domain.SystemRepository, checker LoginChecker, clock Clock, opts ...SessionServiceOption) *SessionService {
	service := &SessionService{
		sessions:     sessions,
		accesses:     accesses,
		environments: environments,
		systems:      systems,
		checker:      checker,
		clock:        clock,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(service)
		}
	}
	return service
}

// recordSessionAudit records one Session lifecycle audit entry. The entry
// carries ids and the resulting state only; a recorder failure is logged and
// never fails the committed business write.
func (s *SessionService) recordSessionAudit(ctx context.Context, event AuditEvent) {
	if s.audit == nil {
		return
	}
	if err := s.audit.RecordResourceAudit(ctx, event); err != nil {
		slog.WarnContext(ctx, "resource audit record failed",
			"action", event.Action,
			"target_type", event.TargetType,
			"target_id", event.TargetID,
			"error", err,
		)
	}
}

// sessionAuditEvent builds the session-target entry with the resulting state.
func sessionAuditEvent(action string, session *domain.Session, actor *int64) AuditEvent {
	event := AuditEvent{
		Action:     action,
		TargetType: AuditTargetSession,
		TargetID:   formatID(session.ID),
		Metadata:   map[string]any{"state": session.State},
	}
	if actor != nil {
		event.ActorID = *actor
	}
	return event
}

// takeoverAuditEvent builds the takeover-target entry. The metadata names the
// takeover and its session and carries the takeover status only; the requested
// instructions are never included.
func takeoverAuditEvent(action string, takeover *domain.Takeover, actor *int64) AuditEvent {
	metadata := map[string]any{
		"state":       takeover.Status,
		"takeover_id": takeover.ID,
	}
	if takeover.SessionID != nil {
		metadata["session_id"] = *takeover.SessionID
	}
	event := AuditEvent{
		Action:     action,
		TargetType: AuditTargetTakeover,
		TargetID:   formatID(takeover.ID),
		Metadata:   metadata,
	}
	if actor != nil {
		event.ActorID = *actor
	}
	return event
}

// now returns the injected clock in UTC; the database stores timestamptz.
func (s *SessionService) now() time.Time {
	return s.clock.Now().UTC()
}

// isStateConflict reports whether err is a failed state CAS.
func isStateConflict(err error) bool {
	var domainErr *domain.Error
	return errors.As(err, &domainErr) && domainErr.Kind == domain.KindStateConflict
}

// invalidEnvironmentError is the documented 422 for an environmentId that does
// not belong to the access's system.
func invalidEnvironmentError() *domain.Error {
	return domain.NewInvalid("environmentId", "invalid_environment",
		"environmentId must reference an environment of the access's system")
}

// storageStateExpiredError is the documented 422 for a computed expiration at
// or before now. Import uses it and so does every transition that would persist
// valid; the contract maps storage_state_expired to 422 invalid_argument.
func storageStateExpiredError() *domain.Error {
	return domain.NewInvalid("storageState", "storage_state_expired",
		"storage state expires at or before the current time")
}

// invalidTakeoverReasonError is the 422 for a reason outside the DDL enum.
func invalidTakeoverReasonError() *domain.Error {
	return domain.NewInvalid("reason", "invalid_takeover_reason",
		"reason must be mfa, captcha, approval, login_unknown or other")
}

// takeoverNotWaitingError is the documented 409 for a takeover lifecycle action
// that is only legal while the takeover is waiting.
func takeoverNotWaitingError() *domain.Error {
	return domain.NewConflict("takeover_not_waiting", "takeover is not waiting")
}

// sessionNotValidError is the state conflict returned when an action requires a
// valid, unexpired session.
func sessionNotValidError() *domain.Error {
	return domain.NewStateConflict("session is not valid and unexpired")
}

// stateConflictError returns a session state conflict with a caller message.
func stateConflictError(message string) *domain.Error {
	return domain.NewStateConflict(message)
}

// validTakeoverReason enforces the manual_takeover.reason CHECK constraint.
func validTakeoverReason(reason string) bool {
	switch reason {
	case domain.TakeoverReasonMFA, domain.TakeoverReasonCaptcha,
		domain.TakeoverReasonApproval, domain.TakeoverReasonLoginUnknown,
		domain.TakeoverReasonOther:
		return true
	default:
		return false
	}
}

// parseImportStorageState validates the incoming document. A token request may
// carry its headers separately, matching the HTTP DTO; both representations are
// accepted by the domain parser.
func parseImportStorageState(kind string, raw json.RawMessage, headers map[string]string) (*domain.StorageState, error) {
	if kind == "" {
		if len(raw) == 0 && len(headers) > 0 {
			kind = domain.StorageStateKindToken
		} else {
			kind = domain.StorageStateKindStorageState
		}
	}
	if kind == domain.StorageStateKindToken && len(raw) == 0 {
		if headers == nil {
			headers = map[string]string{}
		}
		encoded, err := json.Marshal(struct {
			Headers map[string]string `json:"headers"`
		}{Headers: headers})
		if err != nil {
			return nil, err
		}
		raw = encoded
	}
	return domain.ParseStorageState(kind, raw)
}

// parseStoredStorageState re-parses a stored session document for a re-check.
func parseStoredStorageState(session *domain.Session) (*domain.StorageState, error) {
	kind := session.StorageStateKind
	if kind == "" {
		kind = domain.StorageStateKindStorageState
	}
	return domain.ParseStorageState(kind, session.StorageState)
}

// encodeStorageState returns the canonical document stored in
// session.storage_state (the hash is computed from the same value).
func encodeStorageState(state *domain.StorageState) (json.RawMessage, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(encoded), nil
}

// effectiveSessionTTL returns access.session_ttl_seconds, falling back to
// system.default_session_ttl_seconds and then 3600 (spec §6.2).
func effectiveSessionTTL(access *domain.Access, system *domain.System) int {
	ttl := sessionTTLFallbackSeconds
	if system != nil && system.DefaultSessionTTLSeconds > 0 {
		ttl = system.DefaultSessionTTLSeconds
	}
	if access != nil && access.SessionTTLSeconds != nil && *access.SessionTTLSeconds > 0 {
		ttl = *access.SessionTTLSeconds
	}
	return ttl
}

// sessionExpiry is the single expiry computation shared by import, validate,
// confirm and complete-takeover: now plus the effective access/system TTL, an
// explicit import override winning, capped by the state's earliest positive
// cookie expiry (a token has none). A result at or before now is rejected with
// the documented 422 invalid_argument / storage_state_expired; callers that
// would persist valid must propagate the error instead of writing a row that
// Get/List would immediately lazy-expire.
func sessionExpiry(now time.Time, access *domain.Access, system *domain.System, state *domain.StorageState, explicit *time.Time) (time.Time, error) {
	expiresAt := now.Add(time.Duration(effectiveSessionTTL(access, system)) * time.Second)
	if explicit != nil {
		expiresAt = explicit.UTC()
	}
	if earliest, ok := state.EarliestExpiry(now); ok && earliest.Before(expiresAt) {
		expiresAt = earliest
	}
	if !expiresAt.After(now) {
		return time.Time{}, storageStateExpiredError()
	}
	return expiresAt, nil
}

// refreshSessionExpiry resolves the TTL sources and stores the expiry implied by
// state on session. It is the transition-side entry point to sessionExpiry, used
// by validate, confirm and complete-takeover. A rejected expiry leaves the
// session's old expires_at untouched, so the caller cannot persist an
// already-expired valid row.
func (s *SessionService) refreshSessionExpiry(ctx context.Context, session *domain.Session, state *domain.StorageState, now time.Time) error {
	access, err := s.accesses.GetAccess(ctx, session.AccessID)
	if err != nil {
		return err
	}
	system, err := s.systems.GetSystem(ctx, session.SystemID)
	if err != nil {
		return err
	}
	expiresAt, err := sessionExpiry(now, access, system, state, nil)
	if err != nil {
		return err
	}
	session.ExpiresAt = &expiresAt
	return nil
}

// permissionHash hashes the sorted items[key,observed] pairs. Manual
// confirmation hashes the single literal "manual" as required by spec §6.3.
func permissionHash(pairs ...string) string {
	sorted := make([]string, len(pairs))
	copy(sorted, pairs)
	sort.Strings(sorted)
	digest := sha256.Sum256([]byte(strings.Join(sorted, ",")))
	return hex.EncodeToString(digest[:])
}

// mustMarshalJSON renders a snapshot document; a plain map always marshals.
func mustMarshalJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(encoded)
}

// buildCheckSnapshot records one login_check outcome per spec §6.3.
func buildCheckSnapshot(sessionID int64, result domain.LoginCheckResult, capturedAt time.Time) *domain.Snapshot {
	snapshot := &domain.Snapshot{
		SessionID:  sessionID,
		Kind:       snapshotKindLogin,
		CapturedAt: capturedAt,
	}
	if result.Degraded || result.Observed == "" {
		snapshot.Degraded = true
		snapshot.Items = json.RawMessage(`{}`)
		snapshot.Summary = mustMarshalJSON(map[string]any{"degraded": true})
		snapshot.PermissionHash = permissionHash("")
		return snapshot
	}
	allowed, denied, failed := 0, 0, 0
	switch result.Observed {
	case domain.ObservedAllowed:
		allowed = 1
	case domain.ObservedDenied:
		denied = 1
	default:
		failed = 1
	}
	snapshot.Items = mustMarshalJSON(map[string]any{
		"login": map[string]any{
			"kind":     "page",
			"observed": result.Observed,
			"status":   result.Status,
		},
	})
	snapshot.Summary = mustMarshalJSON(map[string]any{
		"total":    1,
		"allowed":  allowed,
		"denied":   denied,
		"error":    failed,
		"degraded": false,
	})
	snapshot.PermissionHash = permissionHash("login=" + result.Observed)
	return snapshot
}

// buildManualSnapshot records a manual confirmation per spec §6.3.
func buildManualSnapshot(sessionID int64, capturedAt time.Time) *domain.Snapshot {
	return &domain.Snapshot{
		SessionID:      sessionID,
		Kind:           snapshotKindLogin,
		Items:          json.RawMessage(`{}`),
		Summary:        mustMarshalJSON(map[string]any{"degraded": true, "reason": manualSnapshotReason}),
		PermissionHash: permissionHash("manual"),
		Degraded:       true,
		CapturedAt:     capturedAt,
	}
}

// runLoginCheck performs the environment's check, turning an adapter error into
// the documented env_unreachable outcome instead of failing the write.
func (s *SessionService) runLoginCheck(ctx context.Context, environment *domain.Environment, state *domain.StorageState) domain.LoginCheckResult {
	if s.checker == nil {
		return domain.LoginCheckResult{Degraded: true, Reason: loginCheckNotConfigured}
	}
	result, err := s.checker.Check(ctx, *environment, *state)
	if err != nil {
		return domain.LoginCheckResult{Observed: domain.ObservedError, Reason: envUnreachable}
	}
	if result.Degraded && result.Reason == "" {
		result.Reason = loginCheckNotConfigured
	}
	return result
}

// applySessionCheckResult applies the state machine outcome of a check to the
// session (spec §6.1/§6.3). A performed check never leaves invalidated_reason
// behind, because the session is no longer in the invalid state.
func applySessionCheckResult(session *domain.Session, result domain.LoginCheckResult, now time.Time) {
	session.InvalidatedReason = ""
	switch {
	case result.Degraded || result.Observed == "":
		session.State = domain.StateNeedsManual
		session.FailureReason = result.Reason
		if session.FailureReason == "" {
			session.FailureReason = loginCheckNotConfigured
		}
	case result.Observed == domain.ObservedAllowed:
		session.State = domain.StateValid
		session.FailureReason = ""
		validatedAt := now
		session.LastValidatedAt = &validatedAt
	case result.Observed == domain.ObservedDenied:
		session.State = domain.StateNeedsManual
		session.FailureReason = loginFailed
	default:
		session.State = domain.StateNeedsManual
		session.FailureReason = envUnreachable
	}
}

// createSnapshot appends one immutable snapshot to the session history.
func (s *SessionService) createSnapshot(ctx context.Context, snapshot *domain.Snapshot) (*domain.Snapshot, error) {
	if err := s.sessions.CreateSnapshot(ctx, snapshot); err != nil {
		return nil, err
	}
	return snapshot, nil
}

// lazyExpireSession moves a valid session whose expires_at has passed to
// expired (spec §6.1). A lost CAS means another writer already changed the row,
// so the latest row is read back instead of failing the read path. A failed
// re-read is returned: serving the stale value would silently report a state the
// row no longer has.
func (s *SessionService) lazyExpireSession(ctx context.Context, session *domain.Session, now time.Time) error {
	if session.State != domain.StateValid || !session.IsExpired(now) {
		return nil
	}
	updated := *session
	updated.State = domain.StateExpired
	if err := s.sessions.UpdateSession(ctx, &updated, domain.StateValid); err != nil {
		if !isStateConflict(err) {
			return err
		}
		latest, getErr := s.sessions.GetSession(ctx, session.ID)
		if getErr != nil {
			return getErr
		}
		if latest != nil {
			*session = *latest
		}
		return nil
	}
	*session = updated
	return nil
}

// lazyExpireTakeover moves an expired waiting takeover to expired (spec §6.4).
// As above a lost CAS is resolved by reading the latest row, and a failed
// re-read is returned instead of swallowed.
func (s *SessionService) lazyExpireTakeover(ctx context.Context, takeover *domain.Takeover, now time.Time) error {
	if takeover == nil || takeover.Status != domain.TakeoverStatusWaiting ||
		takeover.ExpiresAt == nil || now.Before(*takeover.ExpiresAt) {
		return nil
	}
	updated := *takeover
	updated.Status = domain.TakeoverStatusExpired
	if err := s.sessions.UpdateTakeover(ctx, &updated, domain.TakeoverStatusWaiting); err != nil {
		if !isStateConflict(err) {
			return err
		}
		latest, getErr := s.sessions.GetTakeover(ctx, takeover.ID)
		if getErr != nil {
			return getErr
		}
		if latest != nil {
			*takeover = *latest
		}
		return nil
	}
	*takeover = updated
	return nil
}

// Get returns one session with its latest snapshot, active lease and latest
// takeover. Absent optional relations are nil, not errors (spec §7.5).
func (s *SessionService) Get(ctx context.Context, id int64) (*domain.Session, *domain.Snapshot, *domain.Lease, *domain.Takeover, error) {
	session, err := s.sessions.GetSession(ctx, id)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	now := s.now()
	if err := s.lazyExpireSession(ctx, session, now); err != nil {
		return nil, nil, nil, nil, err
	}
	snapshot, err := s.sessions.GetLatestSnapshot(ctx, id)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	lease, err := s.sessions.GetActiveLease(ctx, id)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	takeover, err := s.sessions.GetLatestTakeover(ctx, id)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if err := s.lazyExpireTakeover(ctx, takeover, now); err != nil {
		return nil, nil, nil, nil, err
	}
	return session, snapshot, lease, takeover, nil
}

// List returns one page of sessions, lazily expiring the valid rows whose
// expires_at has passed (spec §6.1).
func (s *SessionService) List(ctx context.Context, f domain.SessionFilter) ([]domain.Session, bool, error) {
	sessions, more, err := s.sessions.ListSessions(ctx, f)
	if err != nil {
		return nil, false, err
	}
	now := s.now()
	for i := range sessions {
		if err := s.lazyExpireSession(ctx, &sessions[i], now); err != nil {
			return nil, false, err
		}
	}
	return sessions, more, nil
}

// Import creates a pending session from an access's storage state, runs the
// environment's login_check and applies the resulting valid/needs_manual state
// (spec §6.1-§6.3).
func (s *SessionService) Import(ctx context.Context, accessID int64, in ImportSessionInput) (*domain.Session, *domain.Snapshot, error) {
	if in.EnvironmentID <= 0 {
		return nil, nil, invalidEnvironmentError()
	}
	access, err := s.accesses.GetAccess(ctx, accessID)
	if err != nil {
		return nil, nil, err
	}
	environment, err := s.environments.GetEnvironment(ctx, in.EnvironmentID)
	if err != nil {
		if isNotFound(err) {
			return nil, nil, invalidEnvironmentError()
		}
		return nil, nil, err
	}
	if environment.SystemID != access.SystemID {
		return nil, nil, invalidEnvironmentError()
	}
	state, err := parseImportStorageState(in.Kind, in.StorageState, in.Headers)
	if err != nil {
		return nil, nil, err
	}
	storedState, err := encodeStorageState(state)
	if err != nil {
		return nil, nil, err
	}
	storageHash, err := domain.HashJSON(state)
	if err != nil {
		return nil, nil, err
	}
	system, err := s.systems.GetSystem(ctx, access.SystemID)
	if err != nil {
		return nil, nil, err
	}
	now := s.now()
	expiresAt, err := sessionExpiry(now, access, system, state, in.ExpiresAt)
	if err != nil {
		return nil, nil, err
	}

	session := &domain.Session{
		AccountID:        access.AccountID,
		SystemID:         access.SystemID,
		EnvironmentID:    environment.ID,
		AccessID:         access.ID,
		State:            domain.StatePending,
		Acquisition:      domain.AcquisitionImported,
		StorageState:     storedState,
		StorageStateHash: storageHash,
		StorageStateKind: state.Kind,
		CookieCount:      len(state.Cookies),
		OriginCount:      len(state.Origins),
		ExpiresAt:        &expiresAt,
		CreatedBy:        in.ActorID,
	}
	if err := s.sessions.CreateSession(ctx, session); err != nil {
		return nil, nil, err
	}

	result := s.runLoginCheck(ctx, environment, state)
	applySessionCheckResult(session, result, now)
	if err := s.sessions.UpdateSession(ctx, session, domain.StatePending); err != nil {
		return nil, nil, err
	}
	snapshot, err := s.createSnapshot(ctx, buildCheckSnapshot(session.ID, result, now))
	if err != nil {
		return nil, nil, err
	}
	s.recordSessionAudit(ctx, sessionAuditEvent(ActionSessionImported, session, in.ActorID))
	return session, snapshot, nil
}

// Validate re-runs the login_check for a session in any non-terminal state and
// applies the resulting state (spec §6.1/§7.5).
func (s *SessionService) Validate(ctx context.Context, id int64) (*domain.Session, *domain.Snapshot, error) {
	session, err := s.sessions.GetSession(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	now := s.now()
	if err := s.lazyExpireSession(ctx, session, now); err != nil {
		return nil, nil, err
	}
	switch session.State {
	case domain.StatePending, domain.StateNeedsManual, domain.StateValid,
		domain.StateExpired, domain.StateInvalid:
	default:
		return nil, nil, stateConflictError("session state " + session.State + " does not allow validate")
	}
	environment, err := s.environments.GetEnvironment(ctx, session.EnvironmentID)
	if err != nil {
		return nil, nil, err
	}
	state, err := parseStoredStorageState(session)
	if err != nil {
		return nil, nil, err
	}
	result := s.runLoginCheck(ctx, environment, state)
	expected := session.State
	applySessionCheckResult(session, result, now)
	if session.State == domain.StateValid {
		// A successful check refreshes the expiry from the state that produced
		// it. The old expires_at may already be in the past (validate is legal
		// from expired) and must not survive into the valid row.
		if err := s.refreshSessionExpiry(ctx, session, state, now); err != nil {
			return nil, nil, err
		}
	}
	if err := s.sessions.UpdateSession(ctx, session, expected); err != nil {
		return nil, nil, err
	}
	snapshot, err := s.createSnapshot(ctx, buildCheckSnapshot(session.ID, result, now))
	if err != nil {
		return nil, nil, err
	}
	s.recordSessionAudit(ctx, sessionAuditEvent(ActionSessionValidated, session, nil))
	return session, snapshot, nil
}

// Confirm marks a pending or needs_manual session valid from a human
// confirmation and records the degraded manual snapshot (spec §6.3). The
// confirmation refreshes expires_at from the still-live stored state; an already
// expired session is rejected as a state conflict (409 session_state_conflict,
// matching a lazily expired row) instead of being resurrected as valid.
func (s *SessionService) Confirm(ctx context.Context, id int64, note string, actorID *int64) (*domain.Session, *domain.Snapshot, error) {
	_ = note
	session, err := s.sessions.GetSession(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	switch session.State {
	case domain.StatePending, domain.StateNeedsManual:
	default:
		return nil, nil, stateConflictError("session state " + session.State + " does not allow confirm")
	}
	now := s.now()
	if session.IsExpired(now) {
		return nil, nil, stateConflictError("session is expired and cannot be confirmed")
	}
	state, err := parseStoredStorageState(session)
	if err != nil {
		return nil, nil, err
	}
	// The stored state is still live (its computed expiry is after now), so the
	// refreshed candidate may extend or shorten the old expiry but stays within
	// the cookie lifetime. A stale state is rejected with 422
	// storage_state_expired before any write.
	if err := s.refreshSessionExpiry(ctx, session, state, now); err != nil {
		return nil, nil, err
	}
	expected := session.State
	session.State = domain.StateValid
	session.FailureReason = ""
	session.InvalidatedReason = ""
	if err := s.sessions.UpdateSession(ctx, session, expected); err != nil {
		return nil, nil, err
	}
	snapshot, err := s.createSnapshot(ctx, buildManualSnapshot(session.ID, now))
	if err != nil {
		return nil, nil, err
	}
	s.recordSessionAudit(ctx, sessionAuditEvent(ActionSessionConfirmed, session, actorID))
	return session, snapshot, nil
}

// Invalidate marks a valid, needs_manual or expired session invalid and releases
// all of its leases (spec §6.1).
func (s *SessionService) Invalidate(ctx context.Context, id int64, reason string) (*domain.Session, error) {
	session, err := s.sessions.GetSession(ctx, id)
	if err != nil {
		return nil, err
	}
	switch session.State {
	case domain.StateValid, domain.StateNeedsManual, domain.StateExpired:
	default:
		return nil, stateConflictError("session state " + session.State + " does not allow invalidate")
	}
	expected := session.State
	session.State = domain.StateInvalid
	session.InvalidatedReason = reason
	if err := s.sessions.UpdateSession(ctx, session, expected); err != nil {
		return nil, err
	}
	if err := s.sessions.ReleaseAllLeases(ctx, session.ID, invalidatedLeaseReason); err != nil {
		return nil, err
	}
	s.recordSessionAudit(ctx, sessionAuditEvent(ActionSessionInvalidated, session, nil))
	return session, nil
}

// Revoke marks any non-revoked session revoked and releases all of its leases
// (spec §6.1).
func (s *SessionService) Revoke(ctx context.Context, id int64, reason string) (*domain.Session, error) {
	session, err := s.sessions.GetSession(ctx, id)
	if err != nil {
		return nil, err
	}
	if session.State == domain.StateRevoked {
		return nil, stateConflictError("session is already revoked")
	}
	expected := session.State
	session.State = domain.StateRevoked
	session.InvalidatedReason = reason
	if err := s.sessions.UpdateSession(ctx, session, expected); err != nil {
		return nil, err
	}
	if err := s.sessions.ReleaseAllLeases(ctx, session.ID, revokedLeaseReason); err != nil {
		return nil, err
	}
	s.recordSessionAudit(ctx, sessionAuditEvent(ActionSessionRevoked, session, nil))
	return session, nil
}

// ListSnapshots returns one page of a session's immutable snapshots.
func (s *SessionService) ListSnapshots(ctx context.Context, sessionID int64, page domain.Page) ([]domain.Snapshot, bool, error) {
	return s.sessions.ListSnapshots(ctx, sessionID, page)
}

// ListTakeovers returns one page of takeovers, lazily expiring the waiting rows
// whose expires_at has passed (spec §6.4).
func (s *SessionService) ListTakeovers(ctx context.Context, f domain.TakeoverFilter) ([]domain.Takeover, bool, error) {
	takeovers, more, err := s.sessions.ListTakeovers(ctx, f)
	if err != nil {
		return nil, false, err
	}
	now := s.now()
	for i := range takeovers {
		if err := s.lazyExpireTakeover(ctx, &takeovers[i], now); err != nil {
			return nil, false, err
		}
	}
	return takeovers, more, nil
}

// RequestTakeover creates a waiting manual takeover for a non-revoked session
// and moves the session to needs_manual (spec §6.4). A session may have at most
// one waiting takeover.
func (s *SessionService) RequestTakeover(ctx context.Context, sessionID int64, reason string, instructions json.RawMessage, expiresIn time.Duration, actorID *int64) (*domain.Takeover, *domain.Session, error) {
	session, err := s.sessions.GetSession(ctx, sessionID)
	if err != nil {
		return nil, nil, err
	}
	if session.State == domain.StateRevoked {
		return nil, nil, stateConflictError("session is revoked")
	}
	if !validTakeoverReason(reason) {
		return nil, nil, invalidTakeoverReasonError()
	}
	now := s.now()
	// One-waiting invariant: spec §6.4 allows at most one waiting takeover per
	// session. There is no partial unique index and this check-then-insert is
	// not atomic against a second concurrent admin request, so two truly
	// simultaneous requests can both pass. Accepted for single-admin P2: the
	// admin surface serialises these writes in practice. Revisit with a partial
	// unique index on (session_id) WHERE status = 'waiting' if admin concurrency
	// is introduced.
	waiting, err := s.sessions.GetWaitingTakeover(ctx, sessionID)
	if err != nil {
		return nil, nil, err
	}
	if waiting != nil {
		if err := s.lazyExpireTakeover(ctx, waiting, now); err != nil {
			return nil, nil, err
		}
		if waiting.Status == domain.TakeoverStatusWaiting {
			return nil, nil, takeoverNotWaitingError()
		}
	}
	if expiresIn <= 0 {
		expiresIn = defaultTakeoverTTL
	}
	expiresAt := now.Add(expiresIn)
	takeover := &domain.Takeover{
		SessionID:     &session.ID,
		AccountID:     session.AccountID,
		SystemID:      session.SystemID,
		EnvironmentID: session.EnvironmentID,
		Reason:        reason,
		Status:        domain.TakeoverStatusWaiting,
		Instructions:  instructions,
		RequestedBy:   actorID,
		RequestedAt:   now,
		ExpiresAt:     &expiresAt,
	}
	if err := s.sessions.CreateTakeover(ctx, takeover); err != nil {
		return nil, nil, err
	}
	expected := session.State
	session.State = domain.StateNeedsManual
	if err := s.sessions.UpdateSession(ctx, session, expected); err != nil {
		// The request cannot be honoured unless the session moves to
		// needs_manual, so the takeover just recorded must not stay waiting: it
		// would block every later request and advertise work that can never be
		// completed. Cancel it best effort; the row is ours (created above), so
		// a lost cancel CAS means another writer already moved it, and the
		// session conflict remains the error the caller needs. A transaction is
		// deliberately out of scope for the single-admin P2 surface.
		cancelled := *takeover
		cancelled.Status = domain.TakeoverStatusCancelled
		_ = s.sessions.UpdateTakeover(ctx, &cancelled, domain.TakeoverStatusWaiting)
		return nil, nil, err
	}
	s.recordSessionAudit(ctx, takeoverAuditEvent(ActionTakeoverRequested, takeover, actorID))
	return takeover, session, nil
}

// CompleteTakeover replaces the session's stored login state from a human
// completion, re-runs the login_check and marks the takeover completed
// (spec §6.4). expires_at is recomputed from the uploaded state's TTL/cookie
// expiry; the session's previous expiry is never reused.
func (s *SessionService) CompleteTakeover(ctx context.Context, takeoverID int64, in ReplaceSessionStateInput, actorID *int64) (*domain.Takeover, *domain.Session, *domain.Snapshot, error) {
	takeover, err := s.sessions.GetTakeover(ctx, takeoverID)
	if err != nil {
		return nil, nil, nil, err
	}
	now := s.now()
	if err := s.lazyExpireTakeover(ctx, takeover, now); err != nil {
		return nil, nil, nil, err
	}
	if takeover.Status != domain.TakeoverStatusWaiting {
		return nil, nil, nil, takeoverNotWaitingError()
	}
	if takeover.SessionID == nil {
		return nil, nil, nil, takeoverNotWaitingError()
	}
	session, err := s.sessions.GetSession(ctx, *takeover.SessionID)
	if err != nil {
		return nil, nil, nil, err
	}
	if session.State == domain.StateRevoked {
		return nil, nil, nil, stateConflictError("session is revoked")
	}
	environment, err := s.environments.GetEnvironment(ctx, session.EnvironmentID)
	if err != nil {
		return nil, nil, nil, err
	}
	state, err := parseImportStorageState(in.Kind, in.StorageState, in.Headers)
	if err != nil {
		return nil, nil, nil, err
	}
	storedState, err := encodeStorageState(state)
	if err != nil {
		return nil, nil, nil, err
	}
	storageHash, err := domain.HashJSON(state)
	if err != nil {
		return nil, nil, nil, err
	}

	// The uploaded state defines the new lifetime. Validate it before writing
	// anything: an already-expired upload is rejected with 422
	// storage_state_expired and leaves the session and takeover untouched, even
	// if the login_check would have put the session back in needs_manual.
	if err := s.refreshSessionExpiry(ctx, session, state, now); err != nil {
		return nil, nil, nil, err
	}

	// Ordering ruling (T13 fix round 1): the session update commits before the
	// takeover CAS. If the takeover CAS then loses, the session may already
	// carry the completed login state while the takeover still reads waiting;
	// the caller gets the conflict and the admin can re-import. This is accepted
	// for the single-admin P2 surface, and a cross-row transaction is
	// deliberately out of scope. The reverse order is worse: a failed session
	// CAS would leave a completed takeover attached to an unchanged session.
	expected := session.State
	session.StorageState = storedState
	session.StorageStateHash = storageHash
	session.StorageStateKind = state.Kind
	session.CookieCount = len(state.Cookies)
	session.OriginCount = len(state.Origins)
	result := s.runLoginCheck(ctx, environment, state)
	applySessionCheckResult(session, result, now)
	if err := s.sessions.UpdateSession(ctx, session, expected); err != nil {
		return nil, nil, nil, err
	}

	takeover.Status = domain.TakeoverStatusCompleted
	completedAt := now
	takeover.CompletedAt = &completedAt
	if err := s.sessions.UpdateTakeover(ctx, takeover, domain.TakeoverStatusWaiting); err != nil {
		return nil, nil, nil, err
	}
	snapshot, err := s.createSnapshot(ctx, buildCheckSnapshot(session.ID, result, now))
	if err != nil {
		return nil, nil, nil, err
	}
	s.recordSessionAudit(ctx, takeoverAuditEvent(ActionTakeoverCompleted, takeover, actorID))
	return takeover, session, snapshot, nil
}

// CancelTakeover cancels a waiting takeover; the session stays needs_manual
// (spec §6.4).
func (s *SessionService) CancelTakeover(ctx context.Context, takeoverID int64) (*domain.Takeover, error) {
	takeover, err := s.sessions.GetTakeover(ctx, takeoverID)
	if err != nil {
		return nil, err
	}
	if err := s.lazyExpireTakeover(ctx, takeover, s.now()); err != nil {
		return nil, err
	}
	if takeover.Status != domain.TakeoverStatusWaiting {
		return nil, takeoverNotWaitingError()
	}
	takeover.Status = domain.TakeoverStatusCancelled
	if err := s.sessions.UpdateTakeover(ctx, takeover, domain.TakeoverStatusWaiting); err != nil {
		return nil, err
	}
	s.recordSessionAudit(ctx, takeoverAuditEvent(ActionTakeoverCancelled, takeover, nil))
	return takeover, nil
}

// AcquireLease grants an exclusive, time-bounded lease on a valid, unexpired
// session. The expiration is capped by the session's own expiration
// (spec §6.5).
func (s *SessionService) AcquireLease(ctx context.Context, sessionID int64, holder string, ttl time.Duration) (*domain.Lease, error) {
	session, err := s.sessions.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	if session.State != domain.StateValid || session.IsExpired(now) {
		return nil, sessionNotValidError()
	}
	if ttl <= 0 {
		ttl = defaultLeaseTTL
	}
	expiresAt := now.Add(ttl)
	if session.ExpiresAt != nil && session.ExpiresAt.Before(expiresAt) {
		expiresAt = *session.ExpiresAt
	}
	lease := &domain.Lease{
		SessionID: sessionID,
		Holder:    holder,
		ExpiresAt: expiresAt,
	}
	if err := s.sessions.CreateLease(ctx, lease); err != nil {
		return nil, err
	}
	return lease, nil
}

// ReleaseLease releases the session's active lease when holder owns it.
func (s *SessionService) ReleaseLease(ctx context.Context, sessionID int64, holder, reason string) error {
	return s.sessions.ReleaseLease(ctx, sessionID, holder, reason)
}
