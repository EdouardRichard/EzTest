package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// fakeLoginBrowser is an in-memory application.LoginBrowser. It records every
// request (including the plaintext password) so the tests can prove the service
// passes the access configuration through, and it can block to model a slow
// login for the single-flight test.
type fakeLoginBrowser struct {
	mu       sync.Mutex
	calls    int
	requests []LoginRequest
	result   LoginResult
	err      error

	started chan struct{}
	release chan struct{}
}

// Login records the request and returns the configured result.
func (b *fakeLoginBrowser) Login(_ context.Context, req LoginRequest) (LoginResult, error) {
	b.mu.Lock()
	b.calls++
	b.requests = append(b.requests, req)
	result, err := b.result, b.err
	started, release := b.started, b.release
	b.mu.Unlock()

	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if release != nil {
		<-release
	}
	return result, err
}

// callCount returns the number of Login invocations.
func (b *fakeLoginBrowser) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

// lastRequest returns the most recent Login request.
func (b *fakeLoginBrowser) lastRequest() LoginRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.requests) == 0 {
		return LoginRequest{}
	}
	return b.requests[len(b.requests)-1]
}

// acquireFixture bundles every fake behind one configured SessionService.
type acquireFixture struct {
	t        *testing.T
	sessions *fakeSessionRepo
	accesses *fakeAccessAccessRepo
	envs     *fakeCatalogEnvironmentRepo
	systems  *fakeCatalogSystemRepo
	accounts *fakeAccessAccountRepo
	flows    *fakeAuthFlowRepo
	checker  *fakeSessionChecker
	login    *fakeLoginBrowser
	clock    catalogTestClock
	svc      *SessionService

	accountID int64
	accessID  int64
	systemID  int64
	envID     int64
}

// acquireLoginURL and acquirePassword are the fixture credential values. The
// password is deliberately distinctive so leak assertions can detect it.
const (
	acquireLoginURL = "http://127.0.0.1:13335/p6/login"
	acquirePassword = "fixture-password"
	acquireUsername = "fixture-user"
)

// newAcquireFixture seeds one system, environment and account and wires the
// optional acquisition dependencies. The access and login recipe are seeded by
// seedEligibleAccess so a test can model "no eligible access" explicitly.
func newAcquireFixture(t *testing.T) *acquireFixture {
	t.Helper()
	f := &acquireFixture{
		t:         t,
		accesses:  newFakeAccessAccessRepo(),
		envs:      newFakeCatalogEnvironmentRepo(),
		systems:   newFakeCatalogSystemRepo(),
		accounts:  newFakeAccessAccountRepo(),
		flows:     newFakeAuthFlowRepo(),
		checker:   &fakeSessionChecker{},
		login:     &fakeLoginBrowser{},
		clock:     catalogTestClock{now: sessionTestNow},
		accountID: 1,
		accessID:  1,
		systemID:  1,
		envID:     1,
	}
	f.sessions = newFakeSessionRepo(f.clock.Now)
	f.systems.systems[f.systemID] = domain.System{
		ID: f.systemID, CompanyID: 1, Key: "fixture-web", Name: "Fixture",
		Kind: domain.SystemKindWeb, BaseURL: "http://127.0.0.1:13335",
		DefaultSessionTTLSeconds: 3600, Status: domain.StatusActive,
	}
	f.systems.nextID = f.systemID + 1
	f.envs.environments[f.envID] = domain.Environment{
		ID: f.envID, SystemID: f.systemID, Key: "dev", Name: "Dev",
		BaseURL: "http://127.0.0.1:13335", Kind: domain.EnvironmentKindDev,
		AllowAutoLogin: true,
	}
	f.envs.nextID = f.envID + 1
	f.accounts.accounts[f.accountID] = domain.Account{
		ID: f.accountID, CompanyID: 1, Key: "fixture-user", Label: "Fixture",
		Username: acquireUsername, Kind: domain.AccountKindService,
		Status: domain.StatusActive,
	}
	f.accounts.nextID = f.accountID + 1

	f.svc = NewSessionService(f.sessions, f.accesses, f.envs, f.systems, f.checker, f.clock)
	f.svc.ConfigureAcquisition(AcquisitionDeps{
		Flows:    f.flows,
		Login:    f.login,
		Accounts: f.accounts,
	})
	return f
}

// formDefinition returns a valid form recipe used by the eligible access.
func formDefinition() domain.FormDefinition {
	return domain.FormDefinition{
		LoginURL:         acquireLoginURL,
		UsernameSelector: "#username",
		PasswordSelector: "#password",
		SubmitSelector:   "button[type=submit]",
		Success:          domain.SuccessRule{URLContains: "/p6/home"},
		Failure: domain.FailureSelectors{
			MFASelector:     "#mfa",
			CaptchaSelector: "#captcha",
			InvalidSelector: ".login-error",
		},
		TimeoutMS: 20000,
	}
}

// seedEligibleAccess seeds the enabled password access and its enabled form
// recipe.
func (f *acquireFixture) seedEligibleAccess() domain.Access {
	f.t.Helper()
	flow := f.flows.seed(domain.AuthFlow{
		ID: 1, SystemID: f.systemID, Name: "fixture-login",
		Kind: domain.AuthFlowKindForm, Definition: formDefinition(), Enabled: true,
	})
	flowID := flow.ID
	access := domain.Access{
		ID: f.accessID, AccountID: f.accountID, SystemID: f.systemID,
		AuthType: domain.AuthTypePassword, Credential: acquirePassword,
		UsernameOverride: acquireUsername, LoginURL: acquireLoginURL,
		LoginFlowID: &flowID, Enabled: true, ConcurrencyLimit: 1,
	}
	f.accesses.accesses[access.ID] = access
	return access
}

// setAccess mutates the fixture access in place.
func (f *acquireFixture) setAccess(mutate func(*domain.Access)) {
	access := f.accesses.accesses[f.accessID]
	mutate(&access)
	f.accesses.accesses[f.accessID] = access
}

// setEnvironment mutates the fixture environment in place.
func (f *acquireFixture) setEnvironment(mutate func(*domain.Environment)) {
	environment := f.envs.environments[f.envID]
	mutate(&environment)
	f.envs.environments[f.envID] = environment
}

// validLoginResult is the default successful login capture. SessionStorage is
// carried separately as well, so the merge path is exercised.
func validLoginResult() LoginResult {
	return LoginResult{
		Outcome: domain.LoginOutcomeValid,
		StorageState: json.RawMessage(`{"cookies":[{"name":"sid","value":"fixture-cookie",` +
			`"domain":"127.0.0.1","path":"/","expires":0,"httpOnly":true,` +
			`"secure":false,"sameSite":"Lax"}],"origins":[{"origin":"http://127.0.0.1:13335",` +
			`"localStorage":[{"name":"k","value":"v"}],"sessionStorage":[{"name":"s","value":"v"}]}]}`),
		SessionStorage: json.RawMessage(`{"http://127.0.0.1:13335":[{"name":"s","value":"v"}]}`),
	}
}

// seedSession inserts a session with a fixed ID, advancing the fake's id
// sequence so a later create never reuses it.
func (f *acquireFixture) seedSession(id int64, state string) domain.Session {
	f.t.Helper()
	storageState := sessionStorageStateJSON()
	parsed, err := domain.ParseStorageState(domain.StorageStateKindStorageState, storageState)
	if err != nil {
		f.t.Fatalf("parse fixture storage state: %v", err)
	}
	hash, err := domain.HashJSON(parsed)
	if err != nil {
		f.t.Fatalf("hash fixture storage state: %v", err)
	}
	session := domain.Session{
		ID: id, AccountID: f.accountID, SystemID: f.systemID,
		EnvironmentID: f.envID, AccessID: f.accessID, State: state,
		Acquisition: domain.AcquisitionImported, StorageState: storageState,
		StorageStateHash: hash, StorageStateKind: domain.StorageStateKindStorageState,
		CookieCount: 1, OriginCount: 1, CreatedAt: sessionTestNow.Add(-time.Hour),
		UpdatedAt: sessionTestNow.Add(-time.Hour),
	}
	switch state {
	case domain.StateValid:
		expiresAt := sessionTestNow.Add(30 * time.Minute)
		session.ExpiresAt = &expiresAt
	case domain.StateExpired:
		expiresAt := sessionTestNow.Add(-time.Minute)
		session.ExpiresAt = &expiresAt
	}
	if id >= f.sessions.nextSessionID {
		f.sessions.nextSessionID = id + 1
	}
	f.sessions.sessions[id] = session
	return session
}

// seedTokenSession inserts a valid session whose stored state is a token
// headers document, as imported for token/apikey/basic accesses.
func (f *acquireFixture) seedTokenSession(id int64, headers map[string]string) domain.Session {
	f.t.Helper()
	raw, err := json.Marshal(struct {
		Headers map[string]string `json:"headers"`
	}{Headers: headers})
	if err != nil {
		f.t.Fatalf("encode fixture token state: %v", err)
	}
	parsed, err := domain.ParseStorageState(domain.StorageStateKindToken, raw)
	if err != nil {
		f.t.Fatalf("parse fixture token state: %v", err)
	}
	hash, err := domain.HashJSON(parsed)
	if err != nil {
		f.t.Fatalf("hash fixture token state: %v", err)
	}
	expiresAt := sessionTestNow.Add(30 * time.Minute)
	session := domain.Session{
		ID: id, AccountID: f.accountID, SystemID: f.systemID,
		EnvironmentID: f.envID, AccessID: f.accessID, State: domain.StateValid,
		Acquisition: domain.AcquisitionImported, StorageState: json.RawMessage(raw),
		StorageStateHash: hash, StorageStateKind: domain.StorageStateKindToken,
		ExpiresAt: &expiresAt, CreatedAt: sessionTestNow.Add(-time.Hour),
		UpdatedAt: sessionTestNow.Add(-time.Hour),
	}
	if id >= f.sessions.nextSessionID {
		f.sessions.nextSessionID = id + 1
	}
	f.sessions.sessions[id] = session
	return session
}

// seedSnapshot inserts a snapshot with a fixed ID.
func (f *acquireFixture) seedSnapshot(sessionID, id int64) domain.Snapshot {
	snapshot := domain.Snapshot{
		ID: id, SessionID: sessionID, Kind: "login", Items: json.RawMessage(`{}`),
		Summary: json.RawMessage(`{"degraded":false}`), PermissionHash: permissionHash("login=allowed"),
		CapturedAt: sessionTestNow.Add(-time.Minute),
	}
	if id >= f.sessions.nextSnapshotID {
		f.sessions.nextSnapshotID = id + 1
	}
	f.sessions.snapshots[sessionID] = append(f.sessions.snapshots[sessionID], snapshot)
	return snapshot
}

// acquire calls AcquireForExecution for the fixture's system and environment.
func (f *acquireFixture) acquire() (AcquiredSession, error) {
	return f.svc.AcquireForExecution(context.Background(), f.envID, f.systemID, "case_run:7", time.Minute)
}

// activeLease returns the session's active lease or nil.
func (f *acquireFixture) activeLease(sessionID int64) *domain.Lease {
	lease, err := f.sessions.GetActiveLease(context.Background(), sessionID)
	if err != nil {
		f.t.Fatalf("GetActiveLease: %v", err)
	}
	return lease
}

// TouchLastUsed mirrors the narrow production write: only last_used_at and
// updated_at change, so a lease acquisition never rewrites the full session row.
func (r *fakeSessionRepo) TouchLastUsed(_ context.Context, id int64, at time.Time) error {
	session, ok := r.sessions[id]
	if !ok {
		return domain.NewNotFound("session", id)
	}
	touched := at
	session.LastUsedAt = &touched
	session.UpdatedAt = r.now()
	r.sessions[id] = session
	return nil
}

// onlySession returns the single session row the fake holds.
func (f *acquireFixture) onlySession() domain.Session {
	f.t.Helper()
	if len(f.sessions.sessions) != 1 {
		f.t.Fatalf("sessions=%d want exactly 1", len(f.sessions.sessions))
	}
	for _, session := range f.sessions.sessions {
		return session
	}
	return domain.Session{}
}

// assertNoLeak fails when any error text or persisted session value contains
// the fixture password. D28 stores the credential plaintext, but it must never
// leak into errors, reasons or logs (spec §5.9).
func assertNoLeak(t *testing.T, err error, session domain.Session) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), acquirePassword) {
		t.Fatalf("error leaks the credential: %v", err)
	}
	if strings.Contains(session.FailureReason, acquirePassword) ||
		strings.Contains(session.InvalidatedReason, acquirePassword) {
		t.Fatalf("session reason leaks the credential: %+v", session)
	}
}

// assertNoSessionReason fails when err is not a no-session failure carrying the
// machine-readable reason execution needs for blocked(session, reason).
func assertNoSessionReason(t *testing.T, err error, want string) {
	t.Helper()
	if !errors.Is(err, ErrNoSession) {
		t.Fatalf("err=%v want ErrNoSession", err)
	}
	var noSession *NoSessionError
	if !errors.As(err, &noSession) {
		t.Fatalf("err=%v want a *NoSessionError carrying the reason", err)
	}
	if noSession.Reason != want {
		t.Fatalf("no-session reason=%q want %q", noSession.Reason, want)
	}
}

// assertSessionUnavailable fails when err is not the distinct unavailable-session
// failure carrying the machine-readable reason.
func assertSessionUnavailable(t *testing.T, err error, want string) {
	t.Helper()
	var unavailable *ErrSessionUnavailable
	if !errors.As(err, &unavailable) {
		t.Fatalf("err=%v want ErrSessionUnavailable", err)
	}
	if unavailable.Reason != want {
		t.Fatalf("unavailable reason=%q want %q", unavailable.Reason, want)
	}
}

// TestAcquireReusesValidSessionBeforeLogin proves the selection path: an
// unexpired valid session with a free lease is leased without any login call.
func TestAcquireReusesValidSessionBeforeLogin(t *testing.T) {
	f := newAcquireFixture(t)
	f.seedEligibleAccess()
	f.seedSession(10, domain.StateValid)
	f.seedSnapshot(10, 77)

	got, err := f.acquire()
	if err != nil {
		t.Fatalf("AcquireForExecution: %v", err)
	}
	if got.SessionID != 10 {
		t.Fatalf("session=%d want 10", got.SessionID)
	}
	if got.SnapshotID != 77 {
		t.Fatalf("snapshot=%d want 77", got.SnapshotID)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(sessionTestNow.Add(30*time.Minute)) {
		t.Fatalf("expiresAt=%v", got.ExpiresAt)
	}
	if f.login.callCount() != 0 {
		t.Fatal("must not login when a valid session with a free lease exists")
	}
	lease := f.activeLease(10)
	if lease == nil || lease.Holder != "case_run:7" {
		t.Fatalf("lease=%+v want holder case_run:7", lease)
	}
	if stored := f.sessions.sessions[10]; stored.LastUsedAt == nil || !stored.LastUsedAt.Equal(sessionTestNow) {
		t.Fatalf("last_used_at=%v want now", stored.LastUsedAt)
	}
	if f.sessions.updateSessionCalls != 0 {
		t.Fatalf("update calls=%d; lease acquisition must touch last_used_at narrowly, not rewrite the row",
			f.sessions.updateSessionCalls)
	}
	if got.StorageStateKind != domain.StorageStateKindStorageState {
		t.Fatalf("storageStateKind=%q want storage_state", got.StorageStateKind)
	}
	if len(got.Headers) != 0 {
		t.Fatalf("headers=%v want none for a browser session", got.Headers)
	}
}

// TestAcquireExposesTokenHeadersForHTTPAuth proves a reused token/apikey/basic
// session exposes its headers and state kind, so execution can fill
// SessionRef.Headers without parsing or sniffing the state itself.
func TestAcquireExposesTokenHeadersForHTTPAuth(t *testing.T) {
	for _, authType := range []string{domain.AuthTypeToken, domain.AuthTypeAPIKey, domain.AuthTypeBasic} {
		t.Run(authType, func(t *testing.T) {
			f := newAcquireFixture(t)
			f.seedEligibleAccess()
			f.setAccess(func(a *domain.Access) { a.AuthType = authType })
			f.seedTokenSession(10, map[string]string{
				"Authorization": "Bearer fixture-token",
				"X-Api-Key":     "fixture-api-key",
			})

			got, err := f.acquire()
			if err != nil {
				t.Fatalf("AcquireForExecution: %v", err)
			}
			if got.StorageStateKind != domain.StorageStateKindToken {
				t.Fatalf("storageStateKind=%q want token", got.StorageStateKind)
			}
			if got.Headers["Authorization"] != "Bearer fixture-token" ||
				got.Headers["X-Api-Key"] != "fixture-api-key" {
				t.Fatalf("headers=%v want the stored token headers", got.Headers)
			}
			if f.login.callCount() != 0 {
				t.Fatal("reusing a token session must not start a browser login")
			}
		})
	}
}

// TestAcquireSelectsLeastRecentlyUsedValidSession proves the LRU order among
// eligible sessions: the oldest last_used_at wins, then the lowest id.
func TestAcquireSelectsLeastRecentlyUsedValidSession(t *testing.T) {
	f := newAcquireFixture(t)
	f.seedEligibleAccess()
	f.setAccess(func(a *domain.Access) { a.ConcurrencyLimit = 2 })
	older := f.seedSession(11, domain.StateValid)
	older.LastUsedAt = sessionTimePtr(sessionTestNow.Add(-2 * time.Hour))
	f.sessions.sessions[11] = older
	newer := f.seedSession(12, domain.StateValid)
	newer.LastUsedAt = sessionTimePtr(sessionTestNow.Add(-time.Minute))
	f.sessions.sessions[12] = newer

	got, err := f.acquire()
	if err != nil {
		t.Fatalf("AcquireForExecution: %v", err)
	}
	if got.SessionID != 11 {
		t.Fatalf("session=%d want the least recently used 11", got.SessionID)
	}
}

// TestAcquireLogsInAndPersistsValidSession proves the happy auto-login path:
// the login port receives the recipe and credential, and the created session is
// valid, auto-acquired, expiry-computed, snapshotted and leased.
func TestAcquireLogsInAndPersistsValidSession(t *testing.T) {
	f := newAcquireFixture(t)
	access := f.seedEligibleAccess()
	f.login.result = validLoginResult()

	got, err := f.acquire()
	if err != nil {
		t.Fatalf("AcquireForExecution: %v", err)
	}
	if f.login.callCount() != 1 {
		t.Fatalf("login calls=%d want 1", f.login.callCount())
	}
	req := f.login.lastRequest()
	if req.LoginURL != access.LoginURL || req.Username != acquireUsername ||
		req.Password != acquirePassword || req.Timeout != 20*time.Second {
		t.Fatalf("login request=%+v", req)
	}
	if req.Flow.LoginURL != acquireLoginURL || req.Flow.UsernameSelector != "#username" {
		t.Fatalf("login flow=%+v", req.Flow)
	}
	if req.AccessID != access.ID || req.AccountID != f.accountID || req.SystemID != f.systemID {
		t.Fatalf("login ids=%+v", req)
	}

	if got.SessionID == 0 || got.SnapshotID == 0 {
		t.Fatalf("acquired=%+v", got)
	}
	session := f.sessions.sessions[got.SessionID]
	if session.State != domain.StateValid || session.Acquisition != domain.AcquisitionAuto {
		t.Fatalf("session=%+v", session)
	}
	if session.ExpiresAt == nil || !session.ExpiresAt.Equal(sessionTestNow.Add(time.Hour)) {
		t.Fatalf("expires_at=%v want now+1h", session.ExpiresAt)
	}
	if session.LastValidatedAt == nil || !session.LastValidatedAt.Equal(sessionTestNow) {
		t.Fatalf("last_validated_at=%v want now", session.LastValidatedAt)
	}
	if session.CookieCount != 1 || session.OriginCount != 1 ||
		session.StorageStateKind != domain.StorageStateKindStorageState {
		t.Fatalf("storage summary=%+v", session)
	}
	assertSessionStorageHashMatches(t, &session)
	assertNoLeak(t, err, session)

	stored, parseErr := domain.ParseStorageState(session.StorageStateKind, session.StorageState)
	if parseErr != nil {
		t.Fatalf("parse stored state: %v", parseErr)
	}
	if len(stored.Origins) != 1 || len(stored.Origins[0].SessionStorage) != 1 ||
		stored.Origins[0].SessionStorage[0].Name != "s" {
		t.Fatalf("stored origins=%+v want sessionStorage preserved", stored.Origins)
	}

	if got.SnapshotID == 0 {
		t.Fatal("acquired snapshot id is zero")
	}
	latest, err := f.sessions.GetLatestSnapshot(context.Background(), session.ID)
	if err != nil || latest == nil || latest.ID != got.SnapshotID {
		t.Fatalf("latest snapshot=%+v err=%v want id=%d", latest, err, got.SnapshotID)
	}
	if lease := f.activeLease(session.ID); lease == nil || lease.Holder != "case_run:7" {
		t.Fatalf("lease=%+v", lease)
	}
	if len(got.StorageState) == 0 || len(got.SessionStorage) == 0 {
		t.Fatalf("acquired state=%+v", got)
	}
	if got.StorageStateKind != domain.StorageStateKindStorageState || len(got.Headers) != 0 {
		t.Fatalf("acquired kind=%q headers=%v want browser storage_state without headers",
			got.StorageStateKind, got.Headers)
	}
	if f.checker.calls != 0 {
		t.Fatalf("login_check calls=%d want 0 when the environment has none", f.checker.calls)
	}
	var sessionStorage map[string][]domain.LocalStorageItem
	if err := json.Unmarshal(got.SessionStorage, &sessionStorage); err != nil {
		t.Fatalf("decode session storage: %v", err)
	}
	if len(sessionStorage["http://127.0.0.1:13335"]) != 1 {
		t.Fatalf("session storage=%v", sessionStorage)
	}
}

// TestAcquireMergesSessionStorageSeparately proves a LoginBrowser that reports
// the captured origins' sessionStorage out of band still lands inside the stored
// storage_state document.
func TestAcquireMergesSessionStorageSeparately(t *testing.T) {
	f := newAcquireFixture(t)
	f.seedEligibleAccess()
	f.login.result = LoginResult{
		Outcome: domain.LoginOutcomeValid,
		StorageState: json.RawMessage(`{"cookies":[{"name":"sid","value":"v","expires":0}],` +
			`"origins":[{"origin":"http://127.0.0.1:13335","localStorage":[]}]}`),
		SessionStorage: json.RawMessage(`{"http://127.0.0.1:13335":[{"name":"token","value":"t"}]}`),
	}

	got, err := f.acquire()
	if err != nil {
		t.Fatalf("AcquireForExecution: %v", err)
	}
	session := f.sessions.sessions[got.SessionID]
	stored, err := domain.ParseStorageState(session.StorageStateKind, session.StorageState)
	if err != nil {
		t.Fatalf("parse stored state: %v", err)
	}
	if len(stored.Origins) != 1 || len(stored.Origins[0].SessionStorage) != 1 ||
		stored.Origins[0].SessionStorage[0].Name != "token" {
		t.Fatalf("origins=%+v want merged sessionStorage", stored.Origins)
	}
}

// TestAcquireRunsLoginCheckWhenConfigured proves a configured login_check runs
// after a successful login and a denied check keeps the session out of valid.
func TestAcquireRunsLoginCheckWhenConfigured(t *testing.T) {
	f := newAcquireFixture(t)
	f.seedEligibleAccess()
	f.login.result = validLoginResult()
	f.setEnvironment(func(e *domain.Environment) {
		e.LoginCheck = &domain.LoginCheck{URL: "http://127.0.0.1:13335/p6/home", ExpectStatus: []int{200}}
	})
	f.checker.result = domain.LoginCheckResult{Observed: domain.ObservedAllowed, Status: 200}

	got, err := f.acquire()
	if err != nil {
		t.Fatalf("AcquireForExecution: %v", err)
	}
	if f.checker.calls != 1 {
		t.Fatalf("login_check calls=%d want 1", f.checker.calls)
	}
	if session := f.sessions.sessions[got.SessionID]; session.State != domain.StateValid {
		t.Fatalf("state=%q want valid", session.State)
	}

	f2 := newAcquireFixture(t)
	f2.seedEligibleAccess()
	f2.login.result = validLoginResult()
	f2.setEnvironment(func(e *domain.Environment) {
		e.LoginCheck = &domain.LoginCheck{URL: "http://127.0.0.1:13335/p6/home", ExpectStatus: []int{200}}
	})
	f2.checker.result = domain.LoginCheckResult{Observed: domain.ObservedDenied, Status: 403}

	_, err = f2.acquire()
	// A denied login_check cannot be fixed by a human takeover, so it is a
	// failed acquisition, not ErrNeedsManual with a zero takeover id.
	assertSessionUnavailable(t, err, "login_check_failed")
	if errors.Is(err, ErrNoSession) {
		t.Fatalf("err=%v; a login_check denial is a distinct unavailable-session failure", err)
	}
	session := f2.onlySession()
	if session.State != domain.StateFailed || session.FailureReason != "login_check_failed" {
		t.Fatalf("session=%+v want failed/login_check_failed", session)
	}
	if f2.sessions.createTakeoverCalls != 0 {
		t.Fatal("a login_check denial must not create a takeover card")
	}
	if lease := f2.activeLease(session.ID); lease != nil {
		t.Fatalf("lease=%+v must be released on a login_check failure", lease)
	}
}

// TestAcquireMFAAndCaptchaCreateWaitingTakeover proves the two human-gate
// outcomes: needs_manual plus one waiting takeover, and no held lease.
func TestAcquireMFAAndCaptchaCreateWaitingTakeover(t *testing.T) {
	for _, reason := range []string{domain.LoginOutcomeMFA, domain.LoginOutcomeCaptcha} {
		t.Run(reason, func(t *testing.T) {
			f := newAcquireFixture(t)
			f.seedEligibleAccess()
			f.login.result = LoginResult{Outcome: reason}

			_, err := f.acquire()
			var needsManual *ErrNeedsManual
			if !errors.As(err, &needsManual) {
				t.Fatalf("err=%v want ErrNeedsManual", err)
			}
			if needsManual.Reason != reason || needsManual.TakeoverID == 0 {
				t.Fatalf("needsManual=%+v", needsManual)
			}
			session := f.onlySession()
			if session.State != domain.StateNeedsManual {
				t.Fatalf("state=%q want needs_manual", session.State)
			}
			if lease := f.activeLease(session.ID); lease != nil {
				t.Fatalf("lease=%+v must be released while waiting for a human", lease)
			}
			takeover, err := f.sessions.GetTakeover(context.Background(), needsManual.TakeoverID)
			if err != nil {
				t.Fatalf("GetTakeover: %v", err)
			}
			if takeover.Status != domain.TakeoverStatusWaiting || takeover.Reason != reason ||
				takeover.SessionID == nil || *takeover.SessionID != session.ID {
				t.Fatalf("takeover=%+v", takeover)
			}
			if f.sessions.createTakeoverCalls != 1 {
				t.Fatalf("takeovers=%d want 1", f.sessions.createTakeoverCalls)
			}
		})
	}
}

// TestAcquireProdApprovalNeedsManualWithoutLogin proves the prod approval gate
// short-circuits before any credential is submitted.
func TestAcquireProdApprovalNeedsManualWithoutLogin(t *testing.T) {
	f := newAcquireFixture(t)
	f.seedEligibleAccess()
	f.setEnvironment(func(e *domain.Environment) {
		e.IsProd = true
		e.RequireApproval = true
	})
	f.login.result = validLoginResult()

	_, err := f.acquire()
	var needsManual *ErrNeedsManual
	if !errors.As(err, &needsManual) || needsManual.Reason != domain.TakeoverReasonApproval {
		t.Fatalf("err=%v want ErrNeedsManual(approval)", err)
	}
	if f.login.callCount() != 0 {
		t.Fatal("approval gate must not submit the credential")
	}
	session := f.onlySession()
	if session.State != domain.StateNeedsManual {
		t.Fatalf("state=%q want needs_manual", session.State)
	}
	if lease := f.activeLease(session.ID); lease != nil {
		t.Fatalf("lease=%+v must not be held while waiting for approval", lease)
	}
	takeover, err := f.sessions.GetTakeover(context.Background(), needsManual.TakeoverID)
	if err != nil || takeover.Reason != domain.TakeoverReasonApproval ||
		takeover.Status != domain.TakeoverStatusWaiting {
		t.Fatalf("takeover=%+v err=%v", takeover, err)
	}
}

// TestAcquireInvalidCredentialMarksSessionFailed proves a rejected credential
// persists failed(credential_invalid), creates no takeover and returns
// ErrNoSession without leaking the credential.
func TestAcquireInvalidCredentialMarksSessionFailed(t *testing.T) {
	f := newAcquireFixture(t)
	f.seedEligibleAccess()
	f.login.result = LoginResult{Outcome: domain.LoginOutcomeInvalid}

	_, err := f.acquire()
	assertNoSessionReason(t, err, NoSessionReasonCredentialInvalid)
	session := f.onlySession()
	if session.State != domain.StateFailed || session.FailureReason != "credential_invalid" {
		t.Fatalf("session=%+v want failed/credential_invalid", session)
	}
	assertNoLeak(t, err, session)
	if f.sessions.createTakeoverCalls != 0 {
		t.Fatal("an invalid credential must not create a takeover")
	}
	if lease := f.activeLease(session.ID); lease != nil {
		t.Fatalf("lease=%+v must not be held after a failed login", lease)
	}
}

// TestAcquireUnreachableMarksSessionFailed proves a transport-level failure
// persists failed(env_unreachable) and returns ErrNoSession.
func TestAcquireUnreachableMarksSessionFailed(t *testing.T) {
	f := newAcquireFixture(t)
	f.seedEligibleAccess()
	f.login.result = LoginResult{Outcome: domain.LoginOutcomeUnreachable}

	_, err := f.acquire()
	assertNoSessionReason(t, err, NoSessionReasonEnvUnreachable)
	session := f.onlySession()
	if session.State != domain.StateFailed || session.FailureReason != "env_unreachable" {
		t.Fatalf("session=%+v want failed/env_unreachable", session)
	}
	if lease := f.activeLease(session.ID); lease != nil {
		t.Fatalf("lease=%+v", lease)
	}
}

// TestAcquireLoginBrowserErrorMarksSessionFailed proves a transport-level port
// error persists failed(env_unreachable), returns the reasoned no-session
// failure, holds no lease and creates no takeover.
func TestAcquireLoginBrowserErrorMarksSessionFailed(t *testing.T) {
	f := newAcquireFixture(t)
	f.seedEligibleAccess()
	f.login.err = errors.New("fixture transport failure")

	_, err := f.acquire()
	assertNoSessionReason(t, err, NoSessionReasonEnvUnreachable)
	session := f.onlySession()
	if session.State != domain.StateFailed || session.FailureReason != "env_unreachable" {
		t.Fatalf("session=%+v want failed/env_unreachable", session)
	}
	assertNoLeak(t, err, session)
	if lease := f.activeLease(session.ID); lease != nil {
		t.Fatalf("lease=%+v must be released after a login port error", lease)
	}
	if f.sessions.createTakeoverCalls != 0 {
		t.Fatal("a login port error must not create a takeover")
	}
}

// TestAcquireInvalidStorageStateMarksSessionFailed proves an unparseable capture
// persists failed(storage_state_invalid) and never leases the session.
func TestAcquireInvalidStorageStateMarksSessionFailed(t *testing.T) {
	f := newAcquireFixture(t)
	f.seedEligibleAccess()
	f.login.result = LoginResult{
		Outcome:      domain.LoginOutcomeValid,
		StorageState: json.RawMessage(`{"cookies":`),
	}

	_, err := f.acquire()
	assertNoSessionReason(t, err, NoSessionReasonLoginFailed)
	session := f.onlySession()
	if session.State != domain.StateFailed || session.FailureReason != "storage_state_invalid" {
		t.Fatalf("session=%+v want failed/storage_state_invalid", session)
	}
	if lease := f.activeLease(session.ID); lease != nil {
		t.Fatalf("lease=%+v must not be held after an unusable capture", lease)
	}
	if f.sessions.createTakeoverCalls != 0 {
		t.Fatal("an unusable capture must not create a takeover")
	}
	assertNoLeak(t, err, session)
}

// TestAcquireExpiredStorageStateMarksSessionFailed proves a capture whose
// earliest cookie expiry is already past persists failed(storage_state_expired).
func TestAcquireExpiredStorageStateMarksSessionFailed(t *testing.T) {
	f := newAcquireFixture(t)
	f.seedEligibleAccess()
	expired := sessionTestNow.Add(-time.Hour).Unix()
	f.login.result = LoginResult{
		Outcome: domain.LoginOutcomeValid,
		StorageState: json.RawMessage(fmt.Sprintf(
			`{"cookies":[{"name":"sid","value":"v","expires":%d}],"origins":[]}`, expired)),
	}

	_, err := f.acquire()
	assertNoSessionReason(t, err, NoSessionReasonLoginFailed)
	session := f.onlySession()
	if session.State != domain.StateFailed || session.FailureReason != "storage_state_expired" {
		t.Fatalf("session=%+v want failed/storage_state_expired", session)
	}
	if lease := f.activeLease(session.ID); lease != nil {
		t.Fatalf("lease=%+v must not be held for an expired capture", lease)
	}
	if f.sessions.createTakeoverCalls != 0 {
		t.Fatal("an expired capture must not create a takeover")
	}
}

func TestAcquireNoEligibleAccessReturnsErrNoSession(t *testing.T) {
	f := newAcquireFixture(t)

	_, err := f.acquire()
	assertNoSessionReason(t, err, NoSessionReasonNoEligibleAccess)
	if f.login.callCount() != 0 {
		t.Fatalf("login calls=%d want 0", f.login.callCount())
	}
	if f.sessions.createSessionCalls != 0 {
		t.Fatalf("created sessions=%d want 0", f.sessions.createSessionCalls)
	}
}

// TestAcquireRejectsNilLoginDependency proves a service wired without the login
// browser fails closed instead of panicking, even when a valid session exists.
func TestAcquireRejectsNilLoginDependency(t *testing.T) {
	f := newAcquireFixture(t)
	f.seedEligibleAccess()
	f.seedSession(10, domain.StateValid)
	f.svc.ConfigureAcquisition(AcquisitionDeps{
		Flows:    f.flows,
		Accounts: f.accounts,
	})

	_, err := f.acquire()
	assertNoSessionReason(t, err, NoSessionReasonNoValidSession)
	if lease := f.activeLease(10); lease != nil {
		t.Fatalf("lease=%+v must not be acquired without the login dependency", lease)
	}
	if f.sessions.createSessionCalls != 0 {
		t.Fatalf("created sessions=%d want 0", f.sessions.createSessionCalls)
	}
}

// TestAcquireRejectsIneligibleAccesses proves each eligibility rule blocks the
// login instead of creating a session.
func TestAcquireRejectsIneligibleAccesses(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*acquireFixture)
	}{
		{
			name: "disabled access",
			mutate: func(f *acquireFixture) {
				f.setAccess(func(a *domain.Access) { a.Enabled = false })
			},
		},
		{
			name: "non-password auth",
			mutate: func(f *acquireFixture) {
				f.setAccess(func(a *domain.Access) { a.AuthType = domain.AuthTypeToken })
			},
		},
		{
			name: "empty credential",
			mutate: func(f *acquireFixture) {
				f.setAccess(func(a *domain.Access) { a.Credential = "" })
			},
		},
		{
			name: "no login flow",
			mutate: func(f *acquireFixture) {
				f.setAccess(func(a *domain.Access) { a.LoginFlowID = nil })
			},
		},
		{
			name: "disabled login flow",
			mutate: func(f *acquireFixture) {
				flow := f.flows.flows[1]
				flow.Enabled = false
				f.flows.flows[1] = flow
			},
		},
		{
			name: "auto login disabled on the environment",
			mutate: func(f *acquireFixture) {
				f.setEnvironment(func(e *domain.Environment) { e.AllowAutoLogin = false })
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAcquireFixture(t)
			f.seedEligibleAccess()
			f.login.result = validLoginResult()
			tc.mutate(f)

			_, err := f.acquire()
			assertNoSessionReason(t, err, NoSessionReasonNoEligibleAccess)
			if f.login.callCount() != 0 {
				t.Fatalf("login calls=%d want 0", f.login.callCount())
			}
			if f.sessions.createSessionCalls != 0 {
				t.Fatalf("created sessions=%d want 0", f.sessions.createSessionCalls)
			}
		})
	}
}

// TestAcquireSkipsExpiredAndInvalidSessions proves expired and invalid rows are
// never leased; the service logs in instead.
func TestAcquireSkipsExpiredAndInvalidSessions(t *testing.T) {
	f := newAcquireFixture(t)
	f.seedEligibleAccess()
	f.seedSession(10, domain.StateValid)
	expired := f.sessions.sessions[10]
	expired.ExpiresAt = sessionTimePtr(sessionTestNow.Add(-time.Minute))
	f.sessions.sessions[10] = expired
	f.seedSession(11, domain.StateInvalid)
	f.login.result = validLoginResult()

	got, err := f.acquire()
	if err != nil {
		t.Fatalf("AcquireForExecution: %v", err)
	}
	if got.SessionID == 10 || got.SessionID == 11 {
		t.Fatalf("session=%d leased an expired/invalid row", got.SessionID)
	}
	if f.login.callCount() != 1 {
		t.Fatalf("login calls=%d want 1", f.login.callCount())
	}
	if session := f.sessions.sessions[got.SessionID]; session.State != domain.StateValid {
		t.Fatalf("state=%q want valid", session.State)
	}
}

// TestAcquireConcurrencyLimitSaturatedReturnsErrNoSession proves a full access
// slot blocks both reuse and auto-login.
func TestAcquireConcurrencyLimitSaturatedReturnsErrNoSession(t *testing.T) {
	f := newAcquireFixture(t)
	f.seedEligibleAccess()
	f.seedSession(10, domain.StateValid)
	f.sessions.leases[10] = []domain.Lease{{
		ID: 1, SessionID: 10, Holder: "case_run:other",
		AcquiredAt: sessionTestNow.Add(-time.Minute), ExpiresAt: sessionTestNow.Add(10 * time.Minute),
	}}
	f.login.result = validLoginResult()

	_, err := f.acquire()
	assertNoSessionReason(t, err, NoSessionReasonNoValidSession)
	if f.login.callCount() != 0 {
		t.Fatalf("login calls=%d want 0 while the access is saturated", f.login.callCount())
	}
	if f.sessions.createSessionCalls != 0 {
		t.Fatalf("created sessions=%d want 0", f.sessions.createSessionCalls)
	}
}

// TestAcquireSingleFlightLogsInOnce proves two concurrent acquisitions for the
// same account/system/environment collapse into a single login.
func TestAcquireSingleFlightLogsInOnce(t *testing.T) {
	f := newAcquireFixture(t)
	f.seedEligibleAccess()
	f.login.result = validLoginResult()
	f.login.started = make(chan struct{}, 4)
	f.login.release = make(chan struct{})

	type result struct {
		acquired AcquiredSession
		err      error
	}
	results := make(chan result, 2)
	run := func() {
		acquired, err := f.acquire()
		results <- result{acquired: acquired, err: err}
	}

	go run()
	select {
	case <-f.login.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the first login never started")
	}
	go run()
	// Give the second goroutine time to enter the single-flight gate. It must
	// wait there instead of invoking the login port.
	time.Sleep(50 * time.Millisecond)
	if calls := f.login.callCount(); calls != 1 {
		t.Fatalf("login calls=%d before release, want 1", calls)
	}
	close(f.login.release)

	successes := 0
	for i := 0; i < 2; i++ {
		select {
		case got := <-results:
			switch {
			case got.err == nil && got.acquired.SessionID != 0:
				successes++
			case errors.Is(got.err, ErrNoSession):
			default:
				t.Fatalf("unexpected result: %+v err=%v", got.acquired, got.err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("an acquisition goroutine did not finish")
		}
	}
	if f.login.callCount() != 1 {
		t.Fatalf("login calls=%d want exactly 1", f.login.callCount())
	}
	if f.sessions.createSessionCalls != 1 {
		t.Fatalf("created sessions=%d want 1", f.sessions.createSessionCalls)
	}
	if successes != 1 {
		t.Fatalf("successes=%d want 1 (the single lease is exclusive)", successes)
	}
}

// TestAcquireSecondAccountNotBlockedByFirstLease proves the single-flight key is
// per account: a second account is not serialised behind the first's lease.
func TestAcquireSecondAccountNotBlockedByFirstLease(t *testing.T) {
	f := newAcquireFixture(t)
	f.seedEligibleAccess()
	f.login.result = validLoginResult()

	if _, err := f.acquire(); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	// A second account with its own access and no session must be able to log
	// in even though the first account's session lease is active.
	f.accounts.accounts[2] = domain.Account{
		ID: 2, CompanyID: 1, Key: "fixture-admin", Username: "fixture-admin",
		Kind: domain.AccountKindService, Status: domain.StatusActive,
	}
	flowID := int64(1)
	f.accesses.accesses[2] = domain.Access{
		ID: 2, AccountID: 2, SystemID: f.systemID, AuthType: domain.AuthTypePassword,
		Credential: "second-password", UsernameOverride: "fixture-admin",
		LoginURL: acquireLoginURL, LoginFlowID: &flowID, Enabled: true, ConcurrencyLimit: 1,
	}
	second := f.svc
	got, err := second.AcquireForExecution(context.Background(), f.envID, f.systemID, "case_run:8", time.Minute)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if got.SessionID == 0 {
		t.Fatalf("acquired=%+v", got)
	}
	if f.login.callCount() != 2 {
		t.Fatalf("login calls=%d want 2", f.login.callCount())
	}
}

// TestMarkInvalidFromNeedsManualReleasesLease proves invalidating a session that
// is waiting for a human still flips it through the CAS and releases its lease.
func TestMarkInvalidFromNeedsManualReleasesLease(t *testing.T) {
	f := newAcquireFixture(t)
	f.seedSession(10, domain.StateNeedsManual)
	f.sessions.leases[10] = []domain.Lease{{
		ID: 1, SessionID: 10, Holder: "worker:old",
		AcquiredAt: sessionTestNow.Add(-time.Minute), ExpiresAt: sessionTestNow.Add(10 * time.Minute),
	}}

	if err := f.svc.MarkInvalid(context.Background(), 10, "session_unauthorized"); err != nil {
		t.Fatalf("MarkInvalid: %v", err)
	}
	session := f.sessions.sessions[10]
	if session.State != domain.StateInvalid || session.InvalidatedReason != "session_unauthorized" {
		t.Fatalf("session=%+v", session)
	}
	if f.sessions.lastExpectedSessionState != domain.StateNeedsManual {
		t.Fatalf("CAS expected=%q want needs_manual", f.sessions.lastExpectedSessionState)
	}
	if lease := f.activeLease(10); lease != nil {
		t.Fatalf("lease=%+v after MarkInvalid from needs_manual", lease)
	}
}

// TestMarkInvalidCASReleasesLease proves MarkInvalid flips the session through
// the state CAS and releases every lease it held.
func TestMarkInvalidCASReleasesLease(t *testing.T) {
	f := newAcquireFixture(t)
	f.seedEligibleAccess()
	f.seedSession(10, domain.StateValid)
	f.sessions.leases[10] = []domain.Lease{{
		ID: 1, SessionID: 10, Holder: "case_run:7",
		AcquiredAt: sessionTestNow.Add(-time.Minute), ExpiresAt: sessionTestNow.Add(10 * time.Minute),
	}}

	if err := f.svc.MarkInvalid(context.Background(), 10, "session_unauthorized"); err != nil {
		t.Fatalf("MarkInvalid: %v", err)
	}
	session := f.sessions.sessions[10]
	if session.State != domain.StateInvalid || session.InvalidatedReason != "session_unauthorized" {
		t.Fatalf("session=%+v", session)
	}
	if f.sessions.lastExpectedSessionState != domain.StateValid {
		t.Fatalf("CAS expected=%q want valid", f.sessions.lastExpectedSessionState)
	}
	if f.sessions.releaseAllCalls != 1 || f.sessions.lastReleaseAllReason != invalidatedLeaseReason {
		t.Fatalf("releases=%d reason=%q", f.sessions.releaseAllCalls, f.sessions.lastReleaseAllReason)
	}
	if lease := f.activeLease(10); lease != nil {
		t.Fatalf("lease=%+v after MarkInvalid", lease)
	}
}

// TestMarkInvalidCASConflictReleasesNothing proves a lost CAS surfaces as the
// domain conflict and leaves the lease untouched.
func TestMarkInvalidCASConflictReleasesNothing(t *testing.T) {
	f := newAcquireFixture(t)
	f.seedSession(10, domain.StateValid)
	f.sessions.forceSessionConflict = true

	err := f.svc.MarkInvalid(context.Background(), 10, "session_unauthorized")
	assertCatalogServiceError(t, err, domain.KindStateConflict, "session_state_conflict")
	if f.sessions.releaseAllCalls != 0 {
		t.Fatal("a conflicting MarkInvalid released leases")
	}
}

// TestMarkInvalidRejectsNonInvalidatableStates proves MarkInvalid only acts on
// the states the invalidation state machine allows.
func TestMarkInvalidRejectsNonInvalidatableStates(t *testing.T) {
	for _, state := range []string{domain.StatePending, domain.StateAcquiring, domain.StateRevoked} {
		t.Run(state, func(t *testing.T) {
			f := newAcquireFixture(t)
			f.seedSession(10, state)
			err := f.svc.MarkInvalid(context.Background(), 10, "reason")
			assertCatalogServiceError(t, err, domain.KindStateConflict, "session_state_conflict")
			if f.sessions.releaseAllCalls != 0 {
				t.Fatal("a disallowed MarkInvalid released leases")
			}
		})
	}
}

// TestMarkInvalidNotFound proves a missing session surfaces as not found.
func TestMarkInvalidNotFound(t *testing.T) {
	f := newAcquireFixture(t)
	err := f.svc.MarkInvalid(context.Background(), 99, "reason")
	assertCatalogServiceError(t, err, domain.KindNotFound, "not_found")
}

// TestAcquireEnvironmentMustBelongToSystem proves a mismatched environment is
// rejected instead of selecting sessions of another system.
func TestAcquireEnvironmentMustBelongToSystem(t *testing.T) {
	f := newAcquireFixture(t)
	f.seedEligibleAccess()
	f.seedSession(10, domain.StateValid)

	_, err := f.svc.AcquireForExecution(context.Background(), f.envID, 99, "case_run:7", time.Minute)
	assertNoSessionReason(t, err, NoSessionReasonNoValidSession)
	if f.login.callCount() != 0 {
		t.Fatal("a mismatched environment logged in")
	}
}

// TestAcquireMissingEnvironmentSurfacesError proves repository failures are not
// swallowed as ErrNoSession.
func TestAcquireMissingEnvironmentSurfacesError(t *testing.T) {
	f := newAcquireFixture(t)
	f.seedEligibleAccess()

	_, err := f.svc.AcquireForExecution(context.Background(), 99, f.systemID, "case_run:7", time.Minute)
	if err == nil || errors.Is(err, ErrNoSession) {
		t.Fatalf("err=%v want the repository not-found error", err)
	}
	if !strings.Contains(fmt.Sprint(err), "not found") {
		t.Fatalf("err=%v want a not-found message", err)
	}
}
