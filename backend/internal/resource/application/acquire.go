package application

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// Session acquisition defaults and failure reasons (spec §5.3, §5.9).
const (
	// credentialInvalid is the failure_reason of a rejected credential.
	credentialInvalid = "credential_invalid"
	// storageStateInvalid is the failure_reason of a capture the service could
	// not parse or encode.
	storageStateInvalid = "storage_state_invalid"
	// releasedAfterFailure is the released_reason of the best-effort lease
	// release when a read after the lease failed.
	releasedAfterFailure = "released"
	// sessionReasonLoginCheckFailed is the failure_reason of an acquisition whose
	// configured login_check denied the captured state.
	sessionReasonLoginCheckFailed = "login_check_failed"
	// defaultLoginTimeout is the login timeout used when a recipe omits it.
	defaultLoginTimeout = 20 * time.Second
	// acquisitionPageSize bounds the internal scans of sessions and accounts.
	acquisitionPageSize = 100
)

// ErrNoSession is the sentinel for "no valid session could be leased and no
// eligible access could produce one". Callers keep using errors.Is; every
// failure returned by AcquireForExecution is a *NoSessionError carrying one of
// the machine-readable reasons below for blocked(session, reason) classification
// (spec §5.3, §5.9).
var ErrNoSession = errors.New("resource: no session available")

// Machine-readable no-session reasons. They are the stable classification input
// execution consumes; the persisted session row keeps its own, more specific
// failure_reason (for example storage_state_invalid).
const (
	// NoSessionReasonCredentialInvalid: the submitted credential was rejected.
	NoSessionReasonCredentialInvalid = "credential_invalid"
	// NoSessionReasonEnvUnreachable: the environment or the login transport
	// could not be reached.
	NoSessionReasonEnvUnreachable = "env_unreachable"
	// NoSessionReasonLoginFailed: the login produced no usable session for any
	// other reason (for example an invalid or expired capture).
	NoSessionReasonLoginFailed = "login_failed"
	// NoSessionReasonNoEligibleAccess: no access is eligible for auto login.
	NoSessionReasonNoEligibleAccess = "no_eligible_access"
	// NoSessionReasonNoValidSession: eligible attempts ran but no valid session
	// is available right now (saturated, mismatched or unconfigured login port).
	NoSessionReasonNoValidSession = "no_valid_session"
)

// NoSessionError is the reasoned no-session failure. It unwraps to the
// ErrNoSession sentinel, so errors.Is(err, ErrNoSession) keeps working while a
// caller can read Reason through errors.As.
type NoSessionError struct {
	Reason string
}

// Error implements error without ever including a credential.
func (e *NoSessionError) Error() string {
	return "resource: no session available (" + e.Reason + ")"
}

// Unwrap exposes the ErrNoSession sentinel.
func (e *NoSessionError) Unwrap() error { return ErrNoSession }

// isLeaseConflict reports whether err is an active-lease conflict, which means
// another holder won the session while this caller was scanning.
func isLeaseConflict(err error) bool {
	var domainErr *domain.Error
	return errors.As(err, &domainErr) && domainErr.Kind == domain.KindLeaseConflict
}

// ErrNeedsManual reports a login that cannot complete without a human. It is
// returned for MFA, captcha and prod-approval takeovers (spec §5.9). TakeoverID
// is the waiting takeover created for the login browser outcome.
type ErrNeedsManual struct {
	TakeoverID int64
	Reason     string
}

// Error implements error without ever including a credential.
func (e *ErrNeedsManual) Error() string {
	return "resource: session needs manual takeover (" + e.Reason + ")"
}

// ErrSessionUnavailable reports an acquisition that failed for a reason a human
// takeover cannot fix, so no takeover card exists for it (for example a
// login_check that denied the captured state). Reason is the persisted
// failure_reason of the failed session.
type ErrSessionUnavailable struct {
	Reason string
}

// Error implements error without ever including a credential.
func (e *ErrSessionUnavailable) Error() string {
	return "resource: session unavailable (" + e.Reason + ")"
}

// AcquiredSession is one leased session ready for an execution context. The
// storage state is the canonical document stored on the session; SessionStorage
// repeats the per-origin sessionStorage map so an adapter can inject it without
// re-parsing. StorageStateKind tells execution whether the session is a browser
// document or an HTTP header bundle; Headers carries that bundle for
// token/apikey/basic sessions so callers never parse or sniff the state. Nothing
// here is logged.
type AcquiredSession struct {
	SessionID        int64
	SnapshotID       int64
	StorageState     json.RawMessage
	SessionStorage   json.RawMessage
	StorageStateKind string
	Headers          map[string]string
	ExpiresAt        *time.Time
}

// AcquisitionDeps wires the optional auto-login dependencies (spec §5.9). The
// repositories already held by SessionService cover sessions and accesses;
// these three add login recipes, the browser login port and the account
// directory the eligibility scan walks.
type AcquisitionDeps struct {
	Flows    domain.AuthFlowRepository
	Login    LoginBrowser
	Accounts domain.AccountRepository
}

// ConfigureAcquisition attaches the optional auto-login dependencies and
// returns the receiver so wiring code can chain. All three are required for
// acquisition: AcquireForExecution fails closed with
// ErrNoSession(no_valid_session) when Login is missing instead of silently
// reusing sessions and hiding the wiring error.
func (s *SessionService) ConfigureAcquisition(deps AcquisitionDeps) *SessionService {
	s.flows = deps.Flows
	s.login = deps.Login
	s.accounts = deps.Accounts
	return s
}

// acquisitionKey is the single-flight key of spec §5.9.
type acquisitionKey struct {
	accountID     int64
	systemID      int64
	environmentID int64
}

// acquisitionFlight is one in-progress acquisition. token is a one-slot
// semaphore; refs counts the goroutines holding or waiting for it so the map
// entry can be reclaimed once nobody is left.
type acquisitionFlight struct {
	token chan struct{}
	refs  int
}

// acquisitionFlights serialises auto-login per (account, system, environment).
// The zero value is ready to use.
type acquisitionFlights struct {
	mu    sync.Mutex
	locks map[acquisitionKey]*acquisitionFlight
}

// enter blocks until the key's flight is free or ctx is cancelled. It returns
// the release function the caller must invoke exactly once.
func (f *acquisitionFlights) enter(ctx context.Context, key acquisitionKey) (func(), error) {
	f.mu.Lock()
	if f.locks == nil {
		f.locks = map[acquisitionKey]*acquisitionFlight{}
	}
	flight := f.locks[key]
	if flight == nil {
		flight = &acquisitionFlight{token: make(chan struct{}, 1)}
		flight.token <- struct{}{}
		f.locks[key] = flight
	}
	flight.refs++
	f.mu.Unlock()

	select {
	case <-flight.token:
		return func() {
			// Hand the token back before dropping the reference so a waiter that
			// arrives between the two steps still observes the same flight.
			flight.token <- struct{}{}
			f.mu.Lock()
			flight.refs--
			if flight.refs == 0 {
				delete(f.locks, key)
			}
			f.mu.Unlock()
		}, nil
	case <-ctx.Done():
		f.mu.Lock()
		flight.refs--
		if flight.refs == 0 {
			delete(f.locks, key)
		}
		f.mu.Unlock()
		return nil, ctx.Err()
	}
}

// AcquireForExecution leases a session for one execution holder (spec §5.3,
// §5.9). It first reuses a valid, unexpired session whose access still has a
// free concurrency slot; otherwise it auto-logs in for the first eligible
// access of the environment. MFA/captcha/approval produce a waiting takeover and
// ErrNeedsManual; no usable access produces ErrNoSession.
func (s *SessionService) AcquireForExecution(ctx context.Context, envID, systemID int64, holder string, ttl time.Duration) (AcquiredSession, error) {
	// Fail closed when the login browser was never wired: silently reusing
	// sessions would hide a wiring error and later panic on auto-login.
	if s.login == nil {
		return AcquiredSession{}, &NoSessionError{Reason: NoSessionReasonNoValidSession}
	}
	environment, err := s.environments.GetEnvironment(ctx, envID)
	if err != nil {
		return AcquiredSession{}, err
	}
	if environment.SystemID != systemID {
		// The environment cannot supply a session of another system; this is a
		// caller wiring error, so it is reported as "no session" and never
		// silently crosses systems.
		return AcquiredSession{}, &NoSessionError{Reason: NoSessionReasonNoValidSession}
	}
	if ttl <= 0 {
		ttl = defaultLeaseTTL
	}

	if acquired, ok, err := s.acquireReusableSession(ctx, systemID, environment.ID, holder, ttl); err != nil {
		return AcquiredSession{}, err
	} else if ok {
		return acquired, nil
	}
	return s.autoLogin(ctx, environment, holder, ttl)
}

// acquireReusableSession leases the least recently used valid session with a
// free lease and a free access concurrency slot. It reports ok=false when no
// candidate is usable.
func (s *SessionService) acquireReusableSession(ctx context.Context, systemID, environmentID int64, holder string, ttl time.Duration) (AcquiredSession, bool, error) {
	now := s.now()
	sessions, err := s.listSessions(ctx, domain.SessionFilter{
		SystemID: systemID, EnvironmentID: environmentID, State: domain.StateValid,
	})
	if err != nil {
		return AcquiredSession{}, false, err
	}
	sortLeastRecentlyUsed(sessions)
	for i := range sessions {
		session := sessions[i]
		if session.IsExpired(now) {
			continue
		}
		access, err := s.accesses.GetAccess(ctx, session.AccessID)
		if err != nil {
			if isNotFound(err) {
				continue
			}
			return AcquiredSession{}, false, err
		}
		if !access.Enabled {
			continue
		}
		free, err := s.accessHasFreeSlotAmong(ctx, access, sessions)
		if err != nil {
			return AcquiredSession{}, false, err
		}
		if !free {
			continue
		}
		if _, err := s.AcquireLease(ctx, session.ID, holder, ttl); err != nil {
			if isLeaseConflict(err) || isStateConflict(err) || isNotFound(err) {
				continue
			}
			return AcquiredSession{}, false, err
		}
		s.touchLastUsed(ctx, &session, now)
		snapshotID, err := s.latestSnapshotID(ctx, session.ID)
		if err != nil {
			// The lease was granted for a row we can no longer serve; release it
			// rather than leaving a holder-less active lease behind.
			_ = s.sessions.ReleaseLease(ctx, session.ID, holder, releasedAfterFailure)
			return AcquiredSession{}, false, err
		}
		return acquiredSession(session, snapshotID), true, nil
	}
	return AcquiredSession{}, false, nil
}

// autoLogin walks the eligible accesses of the environment and logs in for the
// first one that has a free concurrency slot. The per-(account,system,
// environment) single flight collapses concurrent attempts.
func (s *SessionService) autoLogin(ctx context.Context, environment *domain.Environment, holder string, ttl time.Duration) (AcquiredSession, error) {
	targets, err := s.eligibleAcquisitionTargets(ctx, environment)
	if err != nil {
		return AcquiredSession{}, err
	}
	if len(targets) == 0 {
		return AcquiredSession{}, &NoSessionError{Reason: NoSessionReasonNoEligibleAccess}
	}
	for _, target := range targets {
		key := acquisitionKey{
			accountID:     target.access.AccountID,
			systemID:      environment.SystemID,
			environmentID: environment.ID,
		}
		release, err := s.flights.enter(ctx, key)
		if err != nil {
			return AcquiredSession{}, err
		}
		acquired, final, err := s.attemptAutoLogin(ctx, environment, target, holder, ttl)
		release()
		if err != nil {
			return AcquiredSession{}, err
		}
		if final {
			return acquired, nil
		}
	}
	return AcquiredSession{}, &NoSessionError{Reason: NoSessionReasonNoValidSession}
}

// acquisitionTarget is one eligible access plus its resolved recipe and login
// username.
type acquisitionTarget struct {
	access   domain.Access
	flow     domain.FormDefinition
	username string
}

// attemptAutoLogin performs one login attempt. final=false means this access
// could not be used (for example its concurrency slot is full) and the caller
// should try the next eligible access.
func (s *SessionService) attemptAutoLogin(ctx context.Context, environment *domain.Environment, target acquisitionTarget, holder string, ttl time.Duration) (AcquiredSession, bool, error) {
	now := s.now()
	// While this goroutine waited for the single flight another attempt may have
	// completed a login; prefer the now-valid session.
	if acquired, ok, err := s.acquireReusableSession(ctx, environment.SystemID, environment.ID, holder, ttl); err != nil {
		return AcquiredSession{}, true, err
	} else if ok {
		return acquired, true, nil
	}

	if environment.IsProd && environment.RequireApproval {
		session, err := s.beginAcquiringSession(ctx, &target.access, environment.ID)
		if err != nil {
			return AcquiredSession{}, true, err
		}
		takeover, err := s.finishNeedsManual(ctx, session, domain.TakeoverReasonApproval, now)
		if err != nil {
			return AcquiredSession{}, true, err
		}
		return AcquiredSession{}, true, &ErrNeedsManual{TakeoverID: takeover.ID, Reason: domain.TakeoverReasonApproval}
	}

	free, err := s.accessHasFreeSlot(ctx, &target.access, environment.ID)
	if err != nil {
		return AcquiredSession{}, true, err
	}
	if !free {
		return AcquiredSession{}, false, nil
	}

	session, err := s.beginAcquiringSession(ctx, &target.access, environment.ID)
	if err != nil {
		return AcquiredSession{}, true, err
	}

	timeout := defaultLoginTimeout
	if target.flow.TimeoutMS > 0 {
		timeout = time.Duration(target.flow.TimeoutMS) * time.Millisecond
	}
	loginURL := target.access.LoginURL
	if loginURL == "" {
		loginURL = target.flow.LoginURL
	}
	result, err := s.login.Login(ctx, LoginRequest{
		EnvironmentID: environment.ID,
		SystemID:      environment.SystemID,
		AccountID:     target.access.AccountID,
		AccessID:      target.access.ID,
		Flow:          target.flow,
		LoginURL:      loginURL,
		Username:      target.username,
		Password:      target.access.Credential,
		Timeout:       timeout,
	})
	if err != nil {
		// The adapter failed before it could classify the page; treat it as an
		// unreachable environment. The adapter error text is not propagated so a
		// credential can never leak through it.
		if finishErr := s.finishFailed(ctx, session, envUnreachable); finishErr != nil {
			return AcquiredSession{}, true, finishErr
		}
		return AcquiredSession{}, true, &NoSessionError{Reason: NoSessionReasonEnvUnreachable}
	}

	switch result.Outcome {
	case domain.LoginOutcomeValid:
		acquired, err := s.finishValid(ctx, environment, &target.access, session, result, holder, ttl, now)
		return acquired, true, err
	case domain.LoginOutcomeMFA, domain.LoginOutcomeCaptcha:
		takeover, err := s.finishNeedsManual(ctx, session, result.Outcome, now)
		if err != nil {
			return AcquiredSession{}, true, err
		}
		return AcquiredSession{}, true, &ErrNeedsManual{TakeoverID: takeover.ID, Reason: result.Outcome}
	case domain.LoginOutcomeInvalid:
		if err := s.finishFailed(ctx, session, credentialInvalid); err != nil {
			return AcquiredSession{}, true, err
		}
		return AcquiredSession{}, true, &NoSessionError{Reason: NoSessionReasonCredentialInvalid}
	case domain.LoginOutcomeUnreachable:
		if err := s.finishFailed(ctx, session, envUnreachable); err != nil {
			return AcquiredSession{}, true, err
		}
		return AcquiredSession{}, true, &NoSessionError{Reason: NoSessionReasonEnvUnreachable}
	default:
		if err := s.finishFailed(ctx, session, loginFailed); err != nil {
			return AcquiredSession{}, true, err
		}
		return AcquiredSession{}, true, &NoSessionError{Reason: NoSessionReasonLoginFailed}
	}
}

// eligibleAcquisitionTargets applies the auto-login eligibility rules of spec
// §5.9: environment allows auto login, access enabled with a password
// credential, and an enabled form recipe of the same system. Results are
// ordered by access id for determinism.
func (s *SessionService) eligibleAcquisitionTargets(ctx context.Context, environment *domain.Environment) ([]acquisitionTarget, error) {
	if !environment.AllowAutoLogin || s.accounts == nil || s.flows == nil {
		return nil, nil
	}
	targets := make([]acquisitionTarget, 0)
	afterID := int64(0)
	for {
		accounts, more, err := s.accounts.ListAccounts(ctx, domain.AccountFilter{
			Page: domain.Page{AfterID: afterID, Limit: acquisitionPageSize},
		})
		if err != nil {
			return nil, err
		}
		for _, account := range accounts {
			accesses, err := s.accesses.ListAccesses(ctx, account.ID)
			if err != nil {
				return nil, err
			}
			for _, access := range accesses {
				if access.SystemID != environment.SystemID || !eligibleAccess(access) {
					continue
				}
				flow, err := s.flows.GetAuthFlow(ctx, *access.LoginFlowID)
				if err != nil {
					if isNotFound(err) {
						continue
					}
					return nil, err
				}
				if flow.SystemID != environment.SystemID || !flow.Enabled ||
					flow.Kind != domain.AuthFlowKindForm {
					continue
				}
				if err := flow.Definition.Validate(); err != nil {
					// A recipe that cannot execute is not eligible; it must not
					// turn into a login attempt against a half-configured page.
					continue
				}
				username := access.UsernameOverride
				if username == "" {
					username = account.Username
				}
				targets = append(targets, acquisitionTarget{
					access: access, flow: flow.Definition, username: username,
				})
			}
		}
		if !more || len(accounts) == 0 {
			break
		}
		afterID = accounts[len(accounts)-1].ID
	}
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].access.ID < targets[j].access.ID
	})
	return targets, nil
}

// eligibleAccess reports whether an access may log in automatically.
func eligibleAccess(access domain.Access) bool {
	return access.Enabled &&
		access.AuthType == domain.AuthTypePassword &&
		access.Credential != "" &&
		access.LoginFlowID != nil
}

// beginAcquiringSession creates a pending session row or reuses the latest
// failed/expired row of the same access and environment, then moves it to
// acquiring (spec §5.9 step 1). The caller always owns the row from that point.
func (s *SessionService) beginAcquiringSession(ctx context.Context, access *domain.Access, environmentID int64) (*domain.Session, error) {
	rows, err := s.listSessions(ctx, domain.SessionFilter{
		AccessID: access.ID, EnvironmentID: environmentID,
	})
	if err != nil {
		return nil, err
	}
	var reusable *domain.Session
	for i := range rows {
		session := rows[i]
		if session.State != domain.StateFailed && session.State != domain.StateExpired {
			continue
		}
		if reusable == nil || session.ID > reusable.ID {
			reusable = &rows[i]
		}
	}
	if reusable != nil {
		expected := reusable.State
		updated := *reusable
		updated.State = domain.StateAcquiring
		updated.Acquisition = domain.AcquisitionAuto
		updated.FailureReason = ""
		updated.InvalidatedReason = ""
		if err := s.sessions.UpdateSession(ctx, &updated, expected); err != nil {
			if !isStateConflict(err) {
				return nil, err
			}
		} else {
			return &updated, nil
		}
	}

	session := &domain.Session{
		AccountID:        access.AccountID,
		SystemID:         access.SystemID,
		EnvironmentID:    environmentID,
		AccessID:         access.ID,
		State:            domain.StatePending,
		Acquisition:      domain.AcquisitionAuto,
		StorageStateKind: domain.StorageStateKindStorageState,
	}
	if err := s.sessions.CreateSession(ctx, session); err != nil {
		return nil, err
	}
	session.State = domain.StateAcquiring
	if err := s.sessions.UpdateSession(ctx, session, domain.StatePending); err != nil {
		return nil, err
	}
	return session, nil
}

// finishValid persists the captured login state, applies the optional
// login_check and, when the session is valid, adopts the lease and snapshot
// (spec §5.9 step 3).
func (s *SessionService) finishValid(ctx context.Context, environment *domain.Environment, access *domain.Access, session *domain.Session, result LoginResult, holder string, ttl time.Duration, now time.Time) (AcquiredSession, error) {
	state, err := domain.ParseStorageState(domain.StorageStateKindStorageState, result.StorageState)
	if err != nil {
		if finishErr := s.finishFailed(ctx, session, storageStateInvalid); finishErr != nil {
			return AcquiredSession{}, finishErr
		}
		return AcquiredSession{}, &NoSessionError{Reason: NoSessionReasonLoginFailed}
	}
	if err := mergeSessionStorage(state, result.SessionStorage); err != nil {
		if finishErr := s.finishFailed(ctx, session, storageStateInvalid); finishErr != nil {
			return AcquiredSession{}, finishErr
		}
		return AcquiredSession{}, &NoSessionError{Reason: NoSessionReasonLoginFailed}
	}
	encoded, err := encodeStorageState(state)
	if err != nil {
		return AcquiredSession{}, err
	}
	hash, err := domain.HashJSON(state)
	if err != nil {
		return AcquiredSession{}, err
	}
	system, err := s.systems.GetSystem(ctx, access.SystemID)
	if err != nil {
		return AcquiredSession{}, err
	}
	expiresAt, err := sessionExpiry(now, access, system, state, result.ExpiresAt)
	if err != nil {
		if finishErr := s.finishFailed(ctx, session, "storage_state_expired"); finishErr != nil {
			return AcquiredSession{}, finishErr
		}
		return AcquiredSession{}, &NoSessionError{Reason: NoSessionReasonLoginFailed}
	}

	session.StorageState = encoded
	session.StorageStateHash = hash
	session.StorageStateKind = state.Kind
	session.CookieCount = len(state.Cookies)
	session.OriginCount = len(state.Origins)
	session.ExpiresAt = &expiresAt
	session.Acquisition = domain.AcquisitionAuto
	session.FailureReason = ""
	session.InvalidatedReason = ""

	checked := environment.LoginCheck != nil && environment.LoginCheck.URL != ""
	checkResult := domain.LoginCheckResult{Degraded: true, Reason: loginCheckNotConfigured}
	if checked {
		checkResult = s.runLoginCheck(ctx, environment, state)
		applySessionCheckResult(session, checkResult, now)
		if session.State != domain.StateValid {
			// A configured check that does not allow the captured state fails
			// the acquisition. It is not a browser gate, so it must never
			// become a needs_manual with a zero takeover id.
			if checkResult.Observed == domain.ObservedDenied {
				session.FailureReason = sessionReasonLoginCheckFailed
			}
			session.State = domain.StateFailed
		}
	} else {
		session.State = domain.StateValid
		// A successful credential submission is the validation when the
		// environment has no login_check.
		session.LastValidatedAt = &now
	}

	if err := s.sessions.UpdateSession(ctx, session, domain.StateAcquiring); err != nil {
		return AcquiredSession{}, err
	}
	snapshot, err := s.createSnapshot(ctx, buildCheckSnapshot(session.ID, checkResult, now))
	if err != nil {
		return AcquiredSession{}, err
	}
	if session.State == domain.StateFailed {
		// No lease was acquired for this row, so there is nothing to release;
		// the distinct error keeps a caller from looking for a takeover card.
		return AcquiredSession{}, &ErrSessionUnavailable{Reason: session.FailureReason}
	}

	if _, err := s.AcquireLease(ctx, session.ID, holder, ttl); err != nil {
		return AcquiredSession{}, err
	}
	s.touchLastUsed(ctx, session, now)
	return acquiredSession(*session, snapshot.ID), nil
}

// finishNeedsManual moves the session to needs_manual and records one waiting
// takeover for a browser gate (spec §5.9 step 4). No lease is ever held here, so
// the human can take as long as the takeover allows.
func (s *SessionService) finishNeedsManual(ctx context.Context, session *domain.Session, reason string, now time.Time) (*domain.Takeover, error) {
	if !validTakeoverReason(reason) {
		return nil, invalidTakeoverReasonError()
	}
	session.State = domain.StateNeedsManual
	session.FailureReason = reason
	session.Acquisition = domain.AcquisitionAuto
	if err := s.sessions.UpdateSession(ctx, session, domain.StateAcquiring); err != nil {
		return nil, err
	}
	expiresAt := now.Add(defaultTakeoverTTL)
	takeover := &domain.Takeover{
		SessionID:     &session.ID,
		AccountID:     session.AccountID,
		SystemID:      session.SystemID,
		EnvironmentID: session.EnvironmentID,
		Reason:        reason,
		Status:        domain.TakeoverStatusWaiting,
		Instructions:  json.RawMessage(`{}`),
		RequestedAt:   now,
		ExpiresAt:     &expiresAt,
	}
	if err := s.sessions.CreateTakeover(ctx, takeover); err != nil {
		return nil, err
	}
	return takeover, nil
}

// finishFailed moves the session to failed with reason. Nothing is leased.
func (s *SessionService) finishFailed(ctx context.Context, session *domain.Session, reason string) error {
	session.State = domain.StateFailed
	session.FailureReason = reason
	session.Acquisition = domain.AcquisitionAuto
	return s.sessions.UpdateSession(ctx, session, domain.StateAcquiring)
}

// MarkInvalid marks a session invalid through the state CAS and releases every
// lease it held (spec §5.9). A non-invalidatable state is a state conflict; a
// lost CAS is returned unchanged and releases nothing.
func (s *SessionService) MarkInvalid(ctx context.Context, sessionID int64, reason string) error {
	session, err := s.sessions.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}
	switch session.State {
	case domain.StateValid, domain.StateNeedsManual, domain.StateExpired:
	default:
		return stateConflictError("session state " + session.State + " does not allow marking invalid")
	}
	expected := session.State
	session.State = domain.StateInvalid
	session.InvalidatedReason = reason
	if err := s.sessions.UpdateSession(ctx, session, expected); err != nil {
		return err
	}
	return s.sessions.ReleaseAllLeases(ctx, session.ID, invalidatedLeaseReason)
}

// accessHasFreeSlot reports whether the access may start one more concurrent
// session in the environment (spec §5.3).
func (s *SessionService) accessHasFreeSlot(ctx context.Context, access *domain.Access, environmentID int64) (bool, error) {
	sessions, err := s.listSessions(ctx, domain.SessionFilter{
		SystemID: access.SystemID, EnvironmentID: environmentID, State: domain.StateValid,
	})
	if err != nil {
		return false, err
	}
	return s.accessHasFreeSlotAmong(ctx, access, sessions)
}

// accessHasFreeSlotAmong counts the active leases of the access among candidates
// and compares them with access.concurrency_limit (the DDL default, 1, when the
// stored value is absent).
func (s *SessionService) accessHasFreeSlotAmong(ctx context.Context, access *domain.Access, candidates []domain.Session) (bool, error) {
	limit := access.ConcurrencyLimit
	if limit <= 0 {
		limit = 1
	}
	active := 0
	for _, candidate := range candidates {
		if candidate.AccessID != access.ID {
			continue
		}
		lease, err := s.sessions.GetActiveLease(ctx, candidate.ID)
		if err != nil {
			return false, err
		}
		if lease != nil {
			active++
		}
	}
	return active < limit, nil
}

// listSessions scans every page of a session filter.
func (s *SessionService) listSessions(ctx context.Context, filter domain.SessionFilter) ([]domain.Session, error) {
	afterID := filter.Page.AfterID
	sessions := make([]domain.Session, 0)
	for {
		filter.Page = domain.Page{AfterID: afterID, Limit: acquisitionPageSize}
		page, more, err := s.sessions.ListSessions(ctx, filter)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, page...)
		if !more || len(page) == 0 {
			return sessions, nil
		}
		afterID = page[len(page)-1].ID
	}
}

// latestSnapshotID returns the id of the session's latest snapshot, or 0.
func (s *SessionService) latestSnapshotID(ctx context.Context, sessionID int64) (int64, error) {
	snapshot, err := s.sessions.GetLatestSnapshot(ctx, sessionID)
	if err != nil || snapshot == nil {
		return 0, err
	}
	return snapshot.ID, nil
}

// sessionLastUsedToucher is the narrow persistence port of the LRU touch. It is
// separate from SessionRepository.UpdateSession so a lease acquisition updates
// one column instead of rewriting the stored login state from a stale copy.
type sessionLastUsedToucher interface {
	TouchLastUsed(ctx context.Context, id int64, at time.Time) error
}

// touchLastUsed records the lease acquisition time so the selection order stays
// least-recently-used. The write is a single-column update; a failure is ignored
// because the lease is already valid and the touch only affects ordering.
func (s *SessionService) touchLastUsed(ctx context.Context, session *domain.Session, now time.Time) {
	if session.LastUsedAt != nil && !session.LastUsedAt.Before(now) {
		return
	}
	toucher, ok := s.sessions.(sessionLastUsedToucher)
	if !ok {
		// Never fall back to UpdateSession: that would rewrite the full row.
		return
	}
	if err := toucher.TouchLastUsed(ctx, session.ID, now); err != nil {
		return
	}
	session.LastUsedAt = &now
}

// sortLeastRecentlyUsed orders sessions by last_used_at ascending (never used
// first), then by id ascending for a deterministic tie-break (spec §5.3).
func sortLeastRecentlyUsed(sessions []domain.Session) {
	sort.SliceStable(sessions, func(i, j int) bool {
		left, right := sessions[i], sessions[j]
		switch {
		case left.LastUsedAt == nil && right.LastUsedAt == nil:
		case left.LastUsedAt == nil:
			return true
		case right.LastUsedAt == nil:
			return false
		case !left.LastUsedAt.Equal(*right.LastUsedAt):
			return left.LastUsedAt.Before(*right.LastUsedAt)
		}
		return left.ID < right.ID
	})
}

// acquiredSession projects a stored session into the acquisition result. The
// state kind and, for a token state, its header bundle are derived here so
// execution never has to parse or sniff the storage document.
func acquiredSession(session domain.Session, snapshotID int64) AcquiredSession {
	kind := session.StorageStateKind
	if kind == "" {
		kind = domain.StorageStateKindStorageState
	}
	return AcquiredSession{
		SessionID:        session.ID,
		SnapshotID:       snapshotID,
		StorageState:     session.StorageState,
		SessionStorage:   sessionStorageDocument(session),
		StorageStateKind: kind,
		Headers:          storageHeaders(kind, session.StorageState),
		ExpiresAt:        session.ExpiresAt,
	}
}

// storageHeaders returns the HTTP header bundle of a token/apikey/basic session,
// or nil for a browser storage_state. The persisted document is the single
// source; callers receive the parsed headers instead of inspecting the state.
func storageHeaders(kind string, raw json.RawMessage) map[string]string {
	if kind != domain.StorageStateKindToken {
		return nil
	}
	state, err := domain.ParseStorageState(kind, raw)
	if err != nil || len(state.Headers) == 0 {
		return nil
	}
	return state.Headers
}

// sessionStorageDocument extracts the per-origin sessionStorage map from a
// stored session, as a deterministic JSON object keyed by origin.
func sessionStorageDocument(session domain.Session) json.RawMessage {
	kind := session.StorageStateKind
	if kind == "" {
		kind = domain.StorageStateKindStorageState
	}
	state, err := domain.ParseStorageState(kind, session.StorageState)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return sessionStorageOf(state)
}

// sessionStorageOf renders the per-origin sessionStorage map of a parsed state.
func sessionStorageOf(state *domain.StorageState) json.RawMessage {
	document := make(map[string][]domain.LocalStorageItem, len(state.Origins))
	for _, origin := range state.Origins {
		if len(origin.SessionStorage) == 0 {
			continue
		}
		document[origin.Origin] = origin.SessionStorage
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

// mergeSessionStorage folds a separately reported per-origin sessionStorage map
// into the parsed state so the stored document is canonical (spec §3.4). Origins
// that are not part of the captured state are ignored.
func mergeSessionStorage(state *domain.StorageState, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var document map[string][]domain.LocalStorageItem
	if err := json.Unmarshal(raw, &document); err != nil {
		return err
	}
	for origin, items := range document {
		for i := range state.Origins {
			if state.Origins[i].Origin == origin {
				state.Origins[i].SessionStorage = items
				break
			}
		}
	}
	return nil
}
