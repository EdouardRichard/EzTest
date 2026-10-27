package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// sessionTestNow is the fixed instant every session test uses.
var sessionTestNow = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

// fakeSessionChecker is an in-memory application.LoginChecker.
type fakeSessionChecker struct {
	result    domain.LoginCheckResult
	err       error
	calls     int
	lastEnv   domain.Environment
	lastState domain.StorageState
}

// Check records the request and returns the configured result.
func (c *fakeSessionChecker) Check(_ context.Context, env domain.Environment, state domain.StorageState) (domain.LoginCheckResult, error) {
	c.calls++
	c.lastEnv = env
	c.lastState = state
	if c.err != nil {
		return domain.LoginCheckResult{}, c.err
	}
	return c.result, nil
}

// fakeSessionRepo is an in-memory domain.SessionRepository. It mirrors the
// production compare-and-swap semantics, lease reclaim and takeover defaults so
// the service tests exercise the real contract instead of mock behavior.
type fakeSessionRepo struct {
	now func() time.Time

	sessions  map[int64]domain.Session
	snapshots map[int64][]domain.Snapshot
	leases    map[int64][]domain.Lease
	takeovers map[int64]domain.Takeover

	nextSessionID  int64
	nextSnapshotID int64
	nextLeaseID    int64
	nextTakeoverID int64

	createSessionCalls  int
	updateSessionCalls  int
	createSnapshotCalls int
	createLeaseCalls    int
	createTakeoverCalls int
	updateTakeoverCalls int

	lastExpectedSessionState   string
	lastExpectedTakeoverStatus string
	releaseAllCalls            int
	lastReleaseAllReason       string

	forceSessionConflict  bool
	forceTakeoverConflict bool

	// getSessionErr/getTakeoverErr inject a read failure on the nth read so the
	// lazy-expiry tests can prove a failed post-conflict re-read is returned
	// instead of swallowed.
	getSessionErr        error
	getSessionErrOnCall  int
	getSessionReadCalls  int
	getTakeoverErr       error
	getTakeoverErrOnCall int
	getTakeoverReadCalls int
}

// newFakeSessionRepo returns an empty repository using now as its clock.
func newFakeSessionRepo(now func() time.Time) *fakeSessionRepo {
	return &fakeSessionRepo{
		now:            now,
		sessions:       map[int64]domain.Session{},
		snapshots:      map[int64][]domain.Snapshot{},
		leases:         map[int64][]domain.Lease{},
		takeovers:      map[int64]domain.Takeover{},
		nextSessionID:  1,
		nextSnapshotID: 1,
		nextLeaseID:    1,
		nextTakeoverID: 1,
	}
}

// CreateSession stores a session with a fresh id and server timestamps.
func (r *fakeSessionRepo) CreateSession(_ context.Context, s *domain.Session) error {
	r.createSessionCalls++
	s.ID = r.nextSessionID
	r.nextSessionID++
	now := r.now()
	s.CreatedAt = now
	s.UpdatedAt = now
	if s.Acquisition == "" {
		s.Acquisition = domain.AcquisitionImported
	}
	r.sessions[s.ID] = *s
	return nil
}

// GetSession returns one session by id, injecting the configured read failure.
func (r *fakeSessionRepo) GetSession(_ context.Context, id int64) (*domain.Session, error) {
	r.getSessionReadCalls++
	if r.getSessionErr != nil && r.getSessionReadCalls == r.getSessionErrOnCall {
		return nil, r.getSessionErr
	}
	session, ok := r.sessions[id]
	if !ok {
		return nil, domain.NewNotFound("session", id)
	}
	return &session, nil
}

// ListSessions returns one page ordered by id ascending.
func (r *fakeSessionRepo) ListSessions(_ context.Context, f domain.SessionFilter) ([]domain.Session, bool, error) {
	ids := make([]int64, 0, len(r.sessions))
	for id := range r.sessions {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	matches := make([]domain.Session, 0, len(ids))
	for _, id := range ids {
		session := r.sessions[id]
		switch {
		case session.ID <= f.Page.AfterID:
			continue
		case f.AccountID != 0 && session.AccountID != f.AccountID:
			continue
		case f.SystemID != 0 && session.SystemID != f.SystemID:
			continue
		case f.EnvironmentID != 0 && session.EnvironmentID != f.EnvironmentID:
			continue
		case f.AccessID != 0 && session.AccessID != f.AccessID:
			continue
		case f.State != "" && session.State != f.State:
			continue
		}
		matches = append(matches, session)
	}
	limit := f.Page.Limit
	if limit <= 0 {
		limit = 20
	}
	more := len(matches) > limit
	if more {
		matches = matches[:limit]
	}
	return matches, more, nil
}

// UpdateSession is the state compare-and-swap write.
func (r *fakeSessionRepo) UpdateSession(_ context.Context, s *domain.Session, expectedState string) error {
	r.updateSessionCalls++
	r.lastExpectedSessionState = expectedState
	if r.forceSessionConflict {
		return domain.NewStateConflict("forced session conflict")
	}
	current, ok := r.sessions[s.ID]
	if !ok || current.State != expectedState {
		return domain.NewStateConflict("session state conflict")
	}
	s.UpdatedAt = r.now()
	r.sessions[s.ID] = *s
	return nil
}

// CreateSnapshot appends an immutable snapshot.
func (r *fakeSessionRepo) CreateSnapshot(_ context.Context, s *domain.Snapshot) error {
	r.createSnapshotCalls++
	s.ID = r.nextSnapshotID
	r.nextSnapshotID++
	if s.Kind == "" {
		s.Kind = "login"
	}
	if len(s.Items) == 0 {
		s.Items = json.RawMessage(`{}`)
	}
	if len(s.Summary) == 0 {
		s.Summary = json.RawMessage(`{}`)
	}
	if s.CapturedAt.IsZero() {
		s.CapturedAt = r.now()
	}
	r.snapshots[s.SessionID] = append(r.snapshots[s.SessionID], *s)
	return nil
}

// GetLatestSnapshot returns the newest snapshot or (nil, nil).
func (r *fakeSessionRepo) GetLatestSnapshot(_ context.Context, sessionID int64) (*domain.Snapshot, error) {
	list := r.snapshots[sessionID]
	if len(list) == 0 {
		return nil, nil
	}
	latest := list[0]
	for _, snapshot := range list[1:] {
		if snapshot.CapturedAt.After(latest.CapturedAt) ||
			(snapshot.CapturedAt.Equal(latest.CapturedAt) && snapshot.ID > latest.ID) {
			latest = snapshot
		}
	}
	return &latest, nil
}

// ListSnapshots returns one page of a session's snapshots ordered by id.
func (r *fakeSessionRepo) ListSnapshots(_ context.Context, sessionID int64, p domain.Page) ([]domain.Snapshot, bool, error) {
	matches := make([]domain.Snapshot, 0, len(r.snapshots[sessionID]))
	for _, snapshot := range r.snapshots[sessionID] {
		if snapshot.ID > p.AfterID {
			matches = append(matches, snapshot)
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
	limit := p.Limit
	if limit <= 0 {
		limit = 20
	}
	more := len(matches) > limit
	if more {
		matches = matches[:limit]
	}
	return matches, more, nil
}

// CreateLease reclaims expired active leases and enforces the unique active
// lease, mirroring the production repository.
func (r *fakeSessionRepo) CreateLease(_ context.Context, l *domain.Lease) error {
	r.createLeaseCalls++
	now := r.now()
	list := r.leases[l.SessionID]
	for i := range list {
		if list[i].ReleasedAt == nil && !list[i].ExpiresAt.After(now) {
			releasedAt := now
			list[i].ReleasedAt = &releasedAt
			list[i].ReleasedReason = "expired"
		}
	}
	for i := range list {
		if list[i].ReleasedAt == nil {
			r.leases[l.SessionID] = list
			return domain.NewLeaseConflict("session already has an active lease")
		}
	}
	l.ID = r.nextLeaseID
	r.nextLeaseID++
	l.AcquiredAt = now
	list = append(list, *l)
	r.leases[l.SessionID] = list
	return nil
}

// GetActiveLease returns the unexpired, unreleased lease or (nil, nil).
func (r *fakeSessionRepo) GetActiveLease(_ context.Context, sessionID int64) (*domain.Lease, error) {
	now := r.now()
	var active *domain.Lease
	for _, lease := range r.leases[sessionID] {
		candidate := lease
		if candidate.ReleasedAt != nil || !candidate.ExpiresAt.After(now) {
			continue
		}
		if active == nil || candidate.ID > active.ID {
			active = &candidate
		}
	}
	return active, nil
}

// ReleaseLease releases the active lease held by holder.
func (r *fakeSessionRepo) ReleaseLease(_ context.Context, sessionID int64, holder, reason string) error {
	now := r.now()
	list := r.leases[sessionID]
	for i := range list {
		if list[i].ReleasedAt == nil && list[i].Holder == holder {
			releasedAt := now
			list[i].ReleasedAt = &releasedAt
			list[i].ReleasedReason = reason
		}
	}
	r.leases[sessionID] = list
	return nil
}

// ReleaseAllLeases releases every active lease of a session.
func (r *fakeSessionRepo) ReleaseAllLeases(_ context.Context, sessionID int64, reason string) error {
	r.releaseAllCalls++
	r.lastReleaseAllReason = reason
	now := r.now()
	list := r.leases[sessionID]
	for i := range list {
		if list[i].ReleasedAt == nil {
			releasedAt := now
			list[i].ReleasedAt = &releasedAt
			list[i].ReleasedReason = reason
		}
	}
	r.leases[sessionID] = list
	return nil
}

// CreateTakeover stores a takeover with the DDL defaults.
func (r *fakeSessionRepo) CreateTakeover(_ context.Context, t *domain.Takeover) error {
	r.createTakeoverCalls++
	t.ID = r.nextTakeoverID
	r.nextTakeoverID++
	if t.Status == "" {
		t.Status = domain.TakeoverStatusWaiting
	}
	if t.RequestedAt.IsZero() {
		t.RequestedAt = r.now()
	}
	if len(t.Instructions) == 0 {
		t.Instructions = json.RawMessage(`{}`)
	}
	r.takeovers[t.ID] = *t
	return nil
}

// GetTakeover returns one takeover by id, injecting the configured read failure.
func (r *fakeSessionRepo) GetTakeover(_ context.Context, id int64) (*domain.Takeover, error) {
	r.getTakeoverReadCalls++
	if r.getTakeoverErr != nil && r.getTakeoverReadCalls == r.getTakeoverErrOnCall {
		return nil, r.getTakeoverErr
	}
	takeover, ok := r.takeovers[id]
	if !ok {
		return nil, domain.NewNotFound("takeover", id)
	}
	return &takeover, nil
}

// GetWaitingTakeover returns the newest waiting takeover of a session.
func (r *fakeSessionRepo) GetWaitingTakeover(_ context.Context, sessionID int64) (*domain.Takeover, error) {
	var waiting *domain.Takeover
	for _, takeover := range r.takeovers {
		candidate := takeover
		if candidate.SessionID == nil || *candidate.SessionID != sessionID ||
			candidate.Status != domain.TakeoverStatusWaiting {
			continue
		}
		if waiting == nil || candidate.ID > waiting.ID {
			waiting = &candidate
		}
	}
	return waiting, nil
}

// GetLatestTakeover returns the newest takeover of a session in any status.
func (r *fakeSessionRepo) GetLatestTakeover(_ context.Context, sessionID int64) (*domain.Takeover, error) {
	var latest *domain.Takeover
	for _, takeover := range r.takeovers {
		candidate := takeover
		if candidate.SessionID == nil || *candidate.SessionID != sessionID {
			continue
		}
		if latest == nil || candidate.RequestedAt.After(latest.RequestedAt) ||
			(candidate.RequestedAt.Equal(latest.RequestedAt) && candidate.ID > latest.ID) {
			latest = &candidate
		}
	}
	return latest, nil
}

// ListTakeovers returns one page ordered by id ascending.
func (r *fakeSessionRepo) ListTakeovers(_ context.Context, f domain.TakeoverFilter) ([]domain.Takeover, bool, error) {
	ids := make([]int64, 0, len(r.takeovers))
	for id := range r.takeovers {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	matches := make([]domain.Takeover, 0, len(ids))
	for _, id := range ids {
		takeover := r.takeovers[id]
		if takeover.ID <= f.Page.AfterID {
			continue
		}
		if f.Status != "" && takeover.Status != f.Status {
			continue
		}
		matches = append(matches, takeover)
	}
	limit := f.Page.Limit
	if limit <= 0 {
		limit = 20
	}
	more := len(matches) > limit
	if more {
		matches = matches[:limit]
	}
	return matches, more, nil
}

// UpdateTakeover is the status compare-and-swap write.
func (r *fakeSessionRepo) UpdateTakeover(_ context.Context, t *domain.Takeover, expectedStatus string) error {
	r.updateTakeoverCalls++
	r.lastExpectedTakeoverStatus = expectedStatus
	if r.forceTakeoverConflict {
		return domain.NewStateConflict("forced takeover conflict")
	}
	current, ok := r.takeovers[t.ID]
	if !ok || current.Status != expectedStatus {
		return domain.NewStateConflict("takeover status conflict")
	}
	if t.Status == domain.TakeoverStatusCompleted && t.CompletedAt == nil {
		now := r.now()
		t.CompletedAt = &now
	}
	r.takeovers[t.ID] = *t
	return nil
}

// sessionServiceFakes bundles the fake repositories behind one SessionService.
type sessionServiceFakes struct {
	sessions  *fakeSessionRepo
	accesses  *fakeAccessAccessRepo
	envs      *fakeCatalogEnvironmentRepo
	systems   *fakeCatalogSystemRepo
	checker   *fakeSessionChecker
	clock     catalogTestClock
	service   *SessionService
	accessID  int64
	systemID  int64
	envID     int64
	accountID int64
}

// newSessionServiceWithFakes builds a SessionService wired to fresh fakes and a
// seeded system, environment and access. The fixture IDs are all 1, so the
// helper inputs address the same rows.
func newSessionServiceWithFakes(t *testing.T) (*SessionService, *sessionServiceFakes) {
	t.Helper()
	f := &sessionServiceFakes{
		accesses:  newFakeAccessAccessRepo(),
		envs:      newFakeCatalogEnvironmentRepo(),
		systems:   newFakeCatalogSystemRepo(),
		checker:   &fakeSessionChecker{},
		clock:     catalogTestClock{now: sessionTestNow},
		accessID:  1,
		systemID:  1,
		envID:     1,
		accountID: 1,
	}
	f.sessions = newFakeSessionRepo(f.clock.Now)
	f.systems.systems[f.systemID] = domain.System{
		ID: f.systemID, CompanyID: 1, Key: "fixture-web", Name: "Fixture",
		Kind: domain.SystemKindWeb, BaseURL: "http://localhost:13335",
		DefaultSessionTTLSeconds: 3600, Status: domain.StatusActive,
	}
	f.systems.nextID = f.systemID + 1
	f.envs.environments[f.envID] = domain.Environment{
		ID: f.envID, SystemID: f.systemID, Key: "dev", Name: "Dev",
		BaseURL: "http://localhost:13335", Kind: domain.EnvironmentKindDev,
	}
	f.envs.nextID = f.envID + 1
	f.accesses.accesses[f.accessID] = domain.Access{
		ID: f.accessID, AccountID: f.accountID, SystemID: f.systemID,
		AuthType: domain.AuthTypePassword, Enabled: true, ConcurrencyLimit: 1,
	}
	f.accesses.nextID = f.accessID + 1
	f.service = NewSessionService(f.sessions, f.accesses, f.envs, f.systems, f.checker, f.clock)
	return f.service, f
}

// seedSession inserts a session directly, filling the fixture links and the
// defaults a valid session needs.
func (f *sessionServiceFakes) seedSession(session domain.Session) domain.Session {
	if session.ID == 0 {
		session.ID = f.sessions.nextSessionID
		f.sessions.nextSessionID++
	}
	if session.AccountID == 0 {
		session.AccountID = f.accountID
	}
	if session.SystemID == 0 {
		session.SystemID = f.systemID
	}
	if session.EnvironmentID == 0 {
		session.EnvironmentID = f.envID
	}
	if session.AccessID == 0 {
		session.AccessID = f.accessID
	}
	if session.State == "" {
		session.State = domain.StateValid
	}
	if session.Acquisition == "" {
		session.Acquisition = domain.AcquisitionImported
	}
	if session.StorageStateKind == "" {
		session.StorageStateKind = domain.StorageStateKindStorageState
	}
	if len(session.StorageState) == 0 {
		if session.StorageStateKind == domain.StorageStateKindToken {
			session.StorageState = json.RawMessage(`{"headers":{"Authorization":"Bearer fixture-token"}}`)
		} else {
			session.StorageState = sessionStorageStateJSON()
		}
	}
	if session.StorageStateHash == "" {
		if state, err := domain.ParseStorageState(session.StorageStateKind, session.StorageState); err == nil {
			session.StorageStateHash, _ = domain.HashJSON(state)
		}
	}
	if session.CreatedAt.IsZero() {
		session.CreatedAt = sessionTestNow.Add(-time.Hour)
	}
	if session.UpdatedAt.IsZero() {
		session.UpdatedAt = session.CreatedAt
	}
	if session.ExpiresAt == nil && session.State == domain.StateValid {
		expiresAt := sessionTestNow.Add(time.Hour)
		session.ExpiresAt = &expiresAt
	}
	f.sessions.sessions[session.ID] = session
	return session
}

// seedSnapshot appends a snapshot directly to the fake store.
func (f *sessionServiceFakes) seedSnapshot(sessionID int64, summary string) domain.Snapshot {
	snapshot := domain.Snapshot{
		ID: f.sessions.nextSnapshotID, SessionID: sessionID, Kind: "login",
		Items: json.RawMessage(`{}`), Summary: json.RawMessage(summary),
		CapturedAt: sessionTestNow.Add(-time.Minute),
	}
	f.sessions.nextSnapshotID++
	f.sessions.snapshots[sessionID] = append(f.sessions.snapshots[sessionID], snapshot)
	return snapshot
}

// seedLease inserts a lease directly to model pre-existing holder state.
func (f *sessionServiceFakes) seedLease(sessionID int64, holder string, expiresAt time.Time) domain.Lease {
	lease := domain.Lease{
		ID: f.sessions.nextLeaseID, SessionID: sessionID, Holder: holder,
		AcquiredAt: sessionTestNow.Add(-time.Minute), ExpiresAt: expiresAt,
	}
	f.sessions.nextLeaseID++
	f.sessions.leases[sessionID] = append(f.sessions.leases[sessionID], lease)
	return lease
}

// seedTakeover inserts a takeover directly to model pre-existing requests.
func (f *sessionServiceFakes) seedTakeover(takeover domain.Takeover) domain.Takeover {
	if takeover.ID == 0 {
		takeover.ID = f.sessions.nextTakeoverID
		f.sessions.nextTakeoverID++
	}
	if takeover.Status == "" {
		takeover.Status = domain.TakeoverStatusWaiting
	}
	if takeover.RequestedAt.IsZero() {
		takeover.RequestedAt = sessionTestNow.Add(-time.Minute)
	}
	if len(takeover.Instructions) == 0 {
		takeover.Instructions = json.RawMessage(`{}`)
	}
	f.sessions.takeovers[takeover.ID] = takeover
	return takeover
}

// sessionStorageStateJSON returns a valid browser storage_state document with a
// distinctive cookie value so secret-leak assertions can detect it.
func sessionStorageStateJSON() json.RawMessage {
	return json.RawMessage(`{"cookies":[{"name":"sid","value":"fixture-secret-cookie",` +
		`"domain":"fixture.local","path":"/","expires":0,"httpOnly":true,` +
		`"secure":false,"sameSite":"Lax"}],"origins":[{"origin":"http://localhost:13335",` +
		`"localStorage":[{"name":"k","value":"v"}]}]}`)
}

// sessionStateWithCookieExpiry returns a storage_state whose only cookie expires
// at the given Unix second.
func sessionStateWithCookieExpiry(expires int64) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(
		`{"cookies":[{"name":"sid","value":"v","expires":%d}],"origins":[]}`, expires))
}

// validStorageStateInput is the default import body for the fixture access.
func validStorageStateInput() ImportSessionInput {
	return ImportSessionInput{
		EnvironmentID: 1,
		Kind:          domain.StorageStateKindStorageState,
		StorageState:  sessionStorageStateJSON(),
		Note:          "fixture import",
	}
}

// sessionJSONObject decodes a JSON document into a map for structural asserts.
func sessionJSONObject(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode JSON %q: %v", string(raw), err)
	}
	return out
}

// sessionJSONNumber reads a numeric JSON field as an int.
func sessionJSONNumber(t *testing.T, object map[string]any, field string) int {
	t.Helper()
	value, ok := object[field]
	if !ok {
		t.Fatalf("missing JSON field %q in %v", field, object)
	}
	number, ok := value.(float64)
	if !ok {
		t.Fatalf("JSON field %q=%v is not a number", field, value)
	}
	return int(number)
}

// sessionSHA256 returns the lowercase hex SHA-256 of raw.
func sessionSHA256(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// assertSessionStorageHashMatches verifies the session hash is the SHA-256 of
// its stored canonical storage state.
func assertSessionStorageHashMatches(t *testing.T, session *domain.Session) {
	t.Helper()
	state, err := domain.ParseStorageState(session.StorageStateKind, session.StorageState)
	if err != nil {
		t.Fatalf("parse stored storage state: %v", err)
	}
	want, err := domain.HashJSON(state)
	if err != nil {
		t.Fatalf("hash stored storage state: %v", err)
	}
	if session.StorageStateHash != want {
		t.Fatalf("storage_state_hash=%q want %q", session.StorageStateHash, want)
	}
}

// sessionTimePtr returns a pointer to value for the nullable timestamps.
func sessionTimePtr(value time.Time) *time.Time { return &value }

// sessionIntPtr returns a pointer to value.
func sessionIntPtr(value int) *int { return &value }

// sessionInt64Ptr returns a pointer to value.
func sessionInt64Ptr(value int64) *int64 { return &value }

func TestSessionServiceImportWithPassingCheckIsValid(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	f.checker.result = domain.LoginCheckResult{Observed: domain.ObservedAllowed, Status: 200}

	session, snapshot, err := svc.Import(context.Background(), f.accessID, validStorageStateInput())
	if err != nil || session.State != domain.StateValid || snapshot.Degraded {
		t.Fatalf("sess=%+v snap=%+v err=%v", session, snapshot, err)
	}
	if session.Acquisition != domain.AcquisitionImported {
		t.Fatalf("acquisition=%q want imported", session.Acquisition)
	}
	if session.AccountID != f.accountID || session.SystemID != f.systemID || session.EnvironmentID != f.envID {
		t.Fatalf("session links=%+v", session)
	}
	if session.StorageStateKind != domain.StorageStateKindStorageState ||
		session.CookieCount != 1 || session.OriginCount != 1 {
		t.Fatalf("storage state summary=%+v", session)
	}
	assertSessionStorageHashMatches(t, session)
	if session.ExpiresAt == nil || !session.ExpiresAt.Equal(sessionTestNow.Add(time.Hour)) {
		t.Fatalf("expires_at=%v want now+1h", session.ExpiresAt)
	}
	if session.LastValidatedAt == nil || !session.LastValidatedAt.Equal(sessionTestNow) {
		t.Fatalf("last_validated_at=%v want now", session.LastValidatedAt)
	}
	if session.FailureReason != "" || session.InvalidatedReason != "" {
		t.Fatalf("failure=%q invalidated=%q", session.FailureReason, session.InvalidatedReason)
	}
	if snapshot.SessionID != session.ID || snapshot.Kind != "login" ||
		!snapshot.CapturedAt.Equal(sessionTestNow) || snapshot.PermissionHash == "" {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	items := sessionJSONObject(t, snapshot.Items)
	login, ok := items["login"].(map[string]any)
	if !ok || login["observed"] != domain.ObservedAllowed || login["kind"] != "page" {
		t.Fatalf("snapshot items=%v", items)
	}
	if sessionJSONNumber(t, login, "status") != 200 {
		t.Fatalf("snapshot login=%v", login)
	}
	summary := sessionJSONObject(t, snapshot.Summary)
	if sessionJSONNumber(t, summary, "total") != 1 || sessionJSONNumber(t, summary, "allowed") != 1 ||
		sessionJSONNumber(t, summary, "denied") != 0 || sessionJSONNumber(t, summary, "error") != 0 ||
		summary["degraded"] != false {
		t.Fatalf("snapshot summary=%v", summary)
	}
	if f.checker.calls != 1 || f.checker.lastEnv.ID != f.envID {
		t.Fatalf("checker calls=%d env=%+v", f.checker.calls, f.checker.lastEnv)
	}
	if len(f.checker.lastState.Cookies) != 1 || f.checker.lastState.Cookies[0].Value != "fixture-secret-cookie" {
		t.Fatalf("checker state=%+v", f.checker.lastState)
	}
	if f.sessions.createSessionCalls != 1 || f.sessions.updateSessionCalls != 1 ||
		f.sessions.createSnapshotCalls != 1 {
		t.Fatalf("repo calls create=%d update=%d snapshot=%d",
			f.sessions.createSessionCalls, f.sessions.updateSessionCalls, f.sessions.createSnapshotCalls)
	}
	if f.sessions.lastExpectedSessionState != domain.StatePending {
		t.Fatalf("CAS expected=%q want pending", f.sessions.lastExpectedSessionState)
	}
	stored := f.sessions.sessions[session.ID]
	if stored.State != domain.StateValid || stored.StorageStateHash != session.StorageStateHash {
		t.Fatalf("stored session=%+v", stored)
	}
}

func TestSessionServiceImportNotConfiguredNeedsManualDegraded(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	f.checker.result = domain.LoginCheckResult{
		Degraded: true, Reason: "login_check_not_configured",
	}

	session, snapshot, err := svc.Import(context.Background(), f.accessID, validStorageStateInput())
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if session.State != domain.StateNeedsManual || session.FailureReason != "login_check_not_configured" {
		t.Fatalf("session=%+v", session)
	}
	if session.LastValidatedAt != nil {
		t.Fatalf("last_validated_at=%v want nil for a skipped check", session.LastValidatedAt)
	}
	if !snapshot.Degraded {
		t.Fatal("snapshot is not degraded")
	}
	if items := sessionJSONObject(t, snapshot.Items); len(items) != 0 {
		t.Fatalf("degraded snapshot items=%v want {}", items)
	}
	summary := sessionJSONObject(t, snapshot.Summary)
	if summary["degraded"] != true {
		t.Fatalf("degraded snapshot summary=%v", summary)
	}
}

func TestSessionServiceImportLoginCheckFailuresNeedManual(t *testing.T) {
	cases := []struct {
		name         string
		result       domain.LoginCheckResult
		checkErr     error
		wantReason   string
		wantObserved string
		wantStatus   int
		wantCount    string
	}{
		{
			name: "denied", result: domain.LoginCheckResult{Observed: domain.ObservedDenied, Status: 403},
			wantReason: "login_failed", wantObserved: "denied", wantStatus: 403, wantCount: "denied",
		},
		{
			name: "observed error", result: domain.LoginCheckResult{Observed: domain.ObservedError},
			wantReason: "env_unreachable", wantObserved: "error", wantStatus: 0, wantCount: "error",
		},
		{
			name: "adapter error", checkErr: errors.New("dial timeout"),
			wantReason: "env_unreachable", wantObserved: "error", wantStatus: 0, wantCount: "error",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, f := newSessionServiceWithFakes(t)
			f.checker.result = tc.result
			f.checker.err = tc.checkErr

			session, snapshot, err := svc.Import(context.Background(), f.accessID, validStorageStateInput())
			if err != nil {
				t.Fatalf("Import: %v", err)
			}
			if session.State != domain.StateNeedsManual || session.FailureReason != tc.wantReason {
				t.Fatalf("session=%+v want needs_manual/%s", session, tc.wantReason)
			}
			if snapshot.Degraded {
				t.Fatal("a performed check must not be degraded")
			}
			login, ok := sessionJSONObject(t, snapshot.Items)["login"].(map[string]any)
			if !ok || login["observed"] != tc.wantObserved {
				t.Fatalf("snapshot items=%v want observed=%s", snapshot.Items, tc.wantObserved)
			}
			if sessionJSONNumber(t, login, "status") != tc.wantStatus {
				t.Fatalf("snapshot status=%v want %d", login["status"], tc.wantStatus)
			}
			summary := sessionJSONObject(t, snapshot.Summary)
			if summary["degraded"] != false {
				t.Fatalf("summary=%v", summary)
			}
			if sessionJSONNumber(t, summary, tc.wantCount) != 1 {
				t.Fatalf("summary=%v want %s=1", summary, tc.wantCount)
			}
		})
	}
}

func TestSessionServiceImportRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*sessionServiceFakes, *ImportSessionInput)
		reason string
		field  string
	}{
		{
			name: "malformed storage state",
			mutate: func(_ *sessionServiceFakes, in *ImportSessionInput) {
				in.StorageState = json.RawMessage(`{"cookies":`)
			},
			reason: "invalid_storage_state", field: "storageState",
		},
		{
			name: "oversized cookie value",
			mutate: func(_ *sessionServiceFakes, in *ImportSessionInput) {
				in.StorageState = json.RawMessage(
					`{"cookies":[{"name":"sid","value":"` + strings.Repeat("a", 4097) + `"}],"origins":[]}`)
			},
			reason: "invalid_storage_state", field: "storageState.cookies[0].value",
		},
		{
			name: "unknown kind",
			mutate: func(_ *sessionServiceFakes, in *ImportSessionInput) {
				in.Kind = "cookie"
			},
			reason: "invalid_storage_state", field: "kind",
		},
		{
			name: "environment belongs to another system",
			mutate: func(f *sessionServiceFakes, _ *ImportSessionInput) {
				environment := f.envs.environments[1]
				environment.SystemID = 999
				f.envs.environments[1] = environment
			},
			reason: "invalid_environment", field: "environmentId",
		},
		{
			name: "environment does not exist",
			mutate: func(_ *sessionServiceFakes, in *ImportSessionInput) {
				in.EnvironmentID = 99
			},
			reason: "invalid_environment", field: "environmentId",
		},
		{
			name: "environment id is absent",
			mutate: func(_ *sessionServiceFakes, in *ImportSessionInput) {
				in.EnvironmentID = 0
			},
			reason: "invalid_environment", field: "environmentId",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, f := newSessionServiceWithFakes(t)
			input := validStorageStateInput()
			tc.mutate(f, &input)
			_, _, err := svc.Import(context.Background(), f.accessID, input)
			domainErr := assertCatalogServiceError(t, err, domain.KindInvalid, tc.reason)
			if domainErr.Field != tc.field {
				t.Fatalf("field=%q want %q", domainErr.Field, tc.field)
			}
			if f.sessions.createSessionCalls != 0 {
				t.Fatal("an invalid import created a session")
			}
		})
	}

	svc, f := newSessionServiceWithFakes(t)
	_, _, err := svc.Import(context.Background(), 999, validStorageStateInput())
	assertCatalogServiceError(t, err, domain.KindNotFound, "not_found")
	if f.sessions.createSessionCalls != 0 {
		t.Fatal("an import with a missing access created a session")
	}
}

func TestSessionServiceImportComputesEffectiveExpiry(t *testing.T) {
	cases := []struct {
		name         string
		accessTTL    *int
		systemTTL    int
		cookieExpiry int64
		explicit     *time.Time
		wantExpiry   time.Time
	}{
		{
			name: "inherits the system default", systemTTL: 3600,
			wantExpiry: sessionTestNow.Add(time.Hour),
		},
		{
			name: "access ttl overrides the system default", accessTTL: sessionIntPtr(120), systemTTL: 3600,
			wantExpiry: sessionTestNow.Add(2 * time.Minute),
		},
		{
			name: "falls back to one hour", systemTTL: 0,
			wantExpiry: sessionTestNow.Add(time.Hour),
		},
		{
			name: "earliest cookie expiry caps the ttl", systemTTL: 3600,
			cookieExpiry: sessionTestNow.Add(90 * time.Second).Unix(),
			wantExpiry:   sessionTestNow.Add(90 * time.Second),
		},
		{
			name:       "explicit expiresAt wins over the ttl",
			systemTTL:  3600,
			explicit:   sessionTimePtr(sessionTestNow.Add(2 * time.Hour)),
			wantExpiry: sessionTestNow.Add(2 * time.Hour),
		},
		{
			name:         "explicit expiresAt is still capped by the cookie",
			systemTTL:    3600,
			cookieExpiry: sessionTestNow.Add(90 * time.Second).Unix(),
			explicit:     sessionTimePtr(sessionTestNow.Add(2 * time.Hour)),
			wantExpiry:   sessionTestNow.Add(90 * time.Second),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, f := newSessionServiceWithFakes(t)
			system := f.systems.systems[f.systemID]
			system.DefaultSessionTTLSeconds = tc.systemTTL
			f.systems.systems[f.systemID] = system
			access := f.accesses.accesses[f.accessID]
			access.SessionTTLSeconds = tc.accessTTL
			f.accesses.accesses[f.accessID] = access

			input := validStorageStateInput()
			input.ExpiresAt = tc.explicit
			if tc.cookieExpiry != 0 {
				input.StorageState = sessionStateWithCookieExpiry(tc.cookieExpiry)
			}
			session, _, err := svc.Import(context.Background(), f.accessID, input)
			if err != nil {
				t.Fatalf("Import: %v", err)
			}
			if session.ExpiresAt == nil || !session.ExpiresAt.Equal(tc.wantExpiry) {
				t.Fatalf("expires_at=%v want %v", session.ExpiresAt, tc.wantExpiry)
			}
		})
	}
}

func TestSessionServiceImportRejectsExpiredStorageState(t *testing.T) {
	cases := []struct {
		name  string
		input func(*ImportSessionInput)
	}{
		{
			name: "expired cookie",
			input: func(in *ImportSessionInput) {
				in.StorageState = sessionStateWithCookieExpiry(sessionTestNow.Add(-time.Minute).Unix())
			},
		},
		{
			name: "explicit expiresAt in the past",
			input: func(in *ImportSessionInput) {
				in.ExpiresAt = sessionTimePtr(sessionTestNow.Add(-time.Minute))
			},
		},
		{
			name: "explicit expiresAt equal to now",
			input: func(in *ImportSessionInput) {
				in.ExpiresAt = sessionTimePtr(sessionTestNow)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, f := newSessionServiceWithFakes(t)
			input := validStorageStateInput()
			tc.input(&input)
			_, _, err := svc.Import(context.Background(), f.accessID, input)
			domainErr := assertCatalogServiceError(t, err, domain.KindInvalid, "storage_state_expired")
			if domainErr.Field != "storageState" {
				t.Fatalf("field=%q want storageState", domainErr.Field)
			}
			if f.sessions.createSessionCalls != 0 {
				t.Fatal("an expired import created a session")
			}
		})
	}
}

func TestSessionServiceImportAcceptsTokenHeaders(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	f.checker.result = domain.LoginCheckResult{Observed: domain.ObservedAllowed, Status: 200}

	session, _, err := svc.Import(context.Background(), f.accessID, ImportSessionInput{
		EnvironmentID: f.envID,
		Kind:          domain.StorageStateKindToken,
		Headers:       map[string]string{"Authorization": "Bearer fixture-token"},
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if session.StorageStateKind != domain.StorageStateKindToken || session.CookieCount != 0 || session.OriginCount != 0 {
		t.Fatalf("session=%+v", session)
	}
	assertSessionStorageHashMatches(t, session)
	if f.checker.lastState.Headers["Authorization"] != "Bearer fixture-token" {
		t.Fatalf("checker headers=%v", f.checker.lastState.Headers)
	}
}

func TestSessionServiceValidateTransitionsSessions(t *testing.T) {
	for _, state := range []string{
		domain.StatePending, domain.StateNeedsManual, domain.StateValid,
		domain.StateExpired, domain.StateInvalid,
	} {
		t.Run(state, func(t *testing.T) {
			svc, f := newSessionServiceWithFakes(t)
			f.checker.result = domain.LoginCheckResult{Observed: domain.ObservedAllowed, Status: 200}
			seeded := f.seedSession(domain.Session{State: state})

			session, snapshot, err := svc.Validate(context.Background(), seeded.ID)
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if session.State != domain.StateValid || snapshot.Degraded {
				t.Fatalf("session=%+v snapshot=%+v", session, snapshot)
			}
			if session.FailureReason != "" || session.InvalidatedReason != "" {
				t.Fatalf("session=%+v", session)
			}
			if session.LastValidatedAt == nil || !session.LastValidatedAt.Equal(sessionTestNow) {
				t.Fatalf("last_validated_at=%v want now", session.LastValidatedAt)
			}
			if f.sessions.lastExpectedSessionState != state {
				t.Fatalf("CAS expected=%q want %q", f.sessions.lastExpectedSessionState, state)
			}
			if f.sessions.sessions[seeded.ID].State != domain.StateValid {
				t.Fatalf("stored state=%q", f.sessions.sessions[seeded.ID].State)
			}
			if f.sessions.createSnapshotCalls != 1 || f.checker.calls != 1 {
				t.Fatalf("snapshots=%d checks=%d", f.sessions.createSnapshotCalls, f.checker.calls)
			}
		})
	}
}

func TestSessionServiceValidateFailureNeedsManual(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	f.checker.result = domain.LoginCheckResult{Observed: domain.ObservedDenied, Status: 403}
	seeded := f.seedSession(domain.Session{State: domain.StateNeedsManual})

	session, snapshot, err := svc.Validate(context.Background(), seeded.ID)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if session.State != domain.StateNeedsManual || session.FailureReason != "login_failed" {
		t.Fatalf("session=%+v", session)
	}
	if snapshot.Degraded || f.sessions.lastExpectedSessionState != domain.StateNeedsManual {
		t.Fatalf("snapshot=%+v expected=%q", snapshot, f.sessions.lastExpectedSessionState)
	}
}

func TestSessionServiceValidateParsesStoredTokenState(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	f.checker.result = domain.LoginCheckResult{Observed: domain.ObservedAllowed, Status: 200}
	seeded := f.seedSession(domain.Session{
		State:            domain.StateExpired,
		StorageStateKind: domain.StorageStateKindToken,
		StorageState:     json.RawMessage(`{"headers":{"Authorization":"Bearer stored-token"}}`),
	})

	if _, _, err := svc.Validate(context.Background(), seeded.ID); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if f.checker.lastState.Kind != domain.StorageStateKindToken ||
		f.checker.lastState.Headers["Authorization"] != "Bearer stored-token" {
		t.Fatalf("checker state=%+v", f.checker.lastState)
	}
}

func TestSessionServiceValidateRejectsDisallowedStates(t *testing.T) {
	for _, state := range []string{
		domain.StateAcquiring, domain.StateRefreshing, domain.StateRevoked, domain.StateFailed,
	} {
		t.Run(state, func(t *testing.T) {
			svc, f := newSessionServiceWithFakes(t)
			seeded := f.seedSession(domain.Session{State: state})
			_, _, err := svc.Validate(context.Background(), seeded.ID)
			assertCatalogServiceError(t, err, domain.KindStateConflict, "session_state_conflict")
			if f.checker.calls != 0 || f.sessions.updateSessionCalls != 0 {
				t.Fatalf("disallowed validate ran: checks=%d updates=%d",
					f.checker.calls, f.sessions.updateSessionCalls)
			}
		})
	}
}

func TestSessionServiceValidateCASConflict(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	f.checker.result = domain.LoginCheckResult{Observed: domain.ObservedAllowed, Status: 200}
	seeded := f.seedSession(domain.Session{State: domain.StateValid})
	f.sessions.forceSessionConflict = true

	_, _, err := svc.Validate(context.Background(), seeded.ID)
	assertCatalogServiceError(t, err, domain.KindStateConflict, "session_state_conflict")
	if f.sessions.sessions[seeded.ID].State != domain.StateValid {
		t.Fatal("a conflicting validate changed the stored state")
	}
}

func TestSessionServiceValidateMissingSession(t *testing.T) {
	svc, _ := newSessionServiceWithFakes(t)
	_, _, err := svc.Validate(context.Background(), 999)
	assertCatalogServiceError(t, err, domain.KindNotFound, "not_found")
}

func TestSessionServiceConfirmMarksManualValid(t *testing.T) {
	for _, state := range []string{domain.StatePending, domain.StateNeedsManual} {
		t.Run(state, func(t *testing.T) {
			svc, f := newSessionServiceWithFakes(t)
			seeded := f.seedSession(domain.Session{
				State: state, FailureReason: "login_check_not_configured",
				InvalidatedReason: "stale",
			})

			session, snapshot, err := svc.Confirm(context.Background(), seeded.ID, "manual confirmation", nil)
			if err != nil {
				t.Fatalf("Confirm: %v", err)
			}
			if session.State != domain.StateValid || session.FailureReason != "" || session.InvalidatedReason != "" {
				t.Fatalf("session=%+v", session)
			}
			if f.sessions.lastExpectedSessionState != state {
				t.Fatalf("CAS expected=%q want %q", f.sessions.lastExpectedSessionState, state)
			}
			if !snapshot.Degraded || snapshot.Kind != "login" {
				t.Fatalf("snapshot=%+v", snapshot)
			}
			if items := sessionJSONObject(t, snapshot.Items); len(items) != 0 {
				t.Fatalf("items=%v want {}", items)
			}
			summary := sessionJSONObject(t, snapshot.Summary)
			if summary["degraded"] != true || summary["reason"] != "manual_confirmation" {
				t.Fatalf("summary=%v", summary)
			}
			if snapshot.PermissionHash != sessionSHA256("manual") {
				t.Fatalf("permission_hash=%q want sha256(manual)", snapshot.PermissionHash)
			}
		})
	}
}

func TestSessionServiceConfirmRejectsDisallowedStates(t *testing.T) {
	for _, state := range []string{
		domain.StateValid, domain.StateExpired, domain.StateInvalid, domain.StateRevoked,
	} {
		t.Run(state, func(t *testing.T) {
			svc, f := newSessionServiceWithFakes(t)
			seeded := f.seedSession(domain.Session{State: state})
			_, _, err := svc.Confirm(context.Background(), seeded.ID, "", nil)
			assertCatalogServiceError(t, err, domain.KindStateConflict, "session_state_conflict")
			if f.sessions.updateSessionCalls != 0 || f.sessions.createSnapshotCalls != 0 {
				t.Fatalf("a disallowed confirm wrote state: updates=%d snapshots=%d",
					f.sessions.updateSessionCalls, f.sessions.createSnapshotCalls)
			}
		})
	}
}

func TestSessionServiceConfirmCASConflict(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{State: domain.StateNeedsManual})
	f.sessions.forceSessionConflict = true

	_, _, err := svc.Confirm(context.Background(), seeded.ID, "", nil)
	assertCatalogServiceError(t, err, domain.KindStateConflict, "session_state_conflict")
	if f.sessions.sessions[seeded.ID].State != domain.StateNeedsManual {
		t.Fatal("a conflicting confirm changed the stored state")
	}
}

func TestSessionServiceInvalidateTransitionsAndReleasesLeases(t *testing.T) {
	for _, state := range []string{domain.StateValid, domain.StateNeedsManual, domain.StateExpired} {
		t.Run(state, func(t *testing.T) {
			svc, f := newSessionServiceWithFakes(t)
			seeded := f.seedSession(domain.Session{State: state})
			f.seedLease(seeded.ID, "worker-1", sessionTestNow.Add(30*time.Minute))

			session, err := svc.Invalidate(context.Background(), seeded.ID, "admin invalidated")
			if err != nil {
				t.Fatalf("Invalidate: %v", err)
			}
			if session.State != domain.StateInvalid || session.InvalidatedReason != "admin invalidated" {
				t.Fatalf("session=%+v", session)
			}
			if f.sessions.lastExpectedSessionState != state {
				t.Fatalf("CAS expected=%q want %q", f.sessions.lastExpectedSessionState, state)
			}
			if f.sessions.releaseAllCalls != 1 || f.sessions.lastReleaseAllReason != "invalidated" {
				t.Fatalf("releases=%d reason=%q", f.sessions.releaseAllCalls, f.sessions.lastReleaseAllReason)
			}
			if active, _ := f.sessions.GetActiveLease(context.Background(), seeded.ID); active != nil {
				t.Fatalf("active lease=%+v after invalidation", active)
			}
		})
	}
}

func TestSessionServiceInvalidateRejectsDisallowedStates(t *testing.T) {
	for _, state := range []string{domain.StatePending, domain.StateRevoked, domain.StateAcquiring} {
		t.Run(state, func(t *testing.T) {
			svc, f := newSessionServiceWithFakes(t)
			seeded := f.seedSession(domain.Session{State: state})
			_, err := svc.Invalidate(context.Background(), seeded.ID, "reason")
			assertCatalogServiceError(t, err, domain.KindStateConflict, "session_state_conflict")
			if f.sessions.updateSessionCalls != 0 || f.sessions.releaseAllCalls != 0 {
				t.Fatal("a disallowed invalidate wrote state")
			}
		})
	}
}

func TestSessionServiceInvalidateCASConflict(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{State: domain.StateValid})
	f.sessions.forceSessionConflict = true

	_, err := svc.Invalidate(context.Background(), seeded.ID, "reason")
	assertCatalogServiceError(t, err, domain.KindStateConflict, "session_state_conflict")
	if f.sessions.releaseAllCalls != 0 {
		t.Fatal("a conflicting invalidate released leases")
	}
}

func TestSessionServiceRevokeTransitionsAndReleasesLeases(t *testing.T) {
	for _, state := range []string{
		domain.StatePending, domain.StateValid, domain.StateNeedsManual,
		domain.StateExpired, domain.StateInvalid,
	} {
		t.Run(state, func(t *testing.T) {
			svc, f := newSessionServiceWithFakes(t)
			seeded := f.seedSession(domain.Session{State: state})
			f.seedLease(seeded.ID, "worker-1", sessionTestNow.Add(30*time.Minute))

			session, err := svc.Revoke(context.Background(), seeded.ID, "leaked")
			if err != nil {
				t.Fatalf("Revoke: %v", err)
			}
			if session.State != domain.StateRevoked || session.InvalidatedReason != "leaked" {
				t.Fatalf("session=%+v", session)
			}
			if f.sessions.lastExpectedSessionState != state {
				t.Fatalf("CAS expected=%q want %q", f.sessions.lastExpectedSessionState, state)
			}
			if f.sessions.releaseAllCalls != 1 || f.sessions.lastReleaseAllReason != "revoked" {
				t.Fatalf("releases=%d reason=%q", f.sessions.releaseAllCalls, f.sessions.lastReleaseAllReason)
			}
		})
	}
}

func TestSessionServiceRevokeRejectsRevokedAndCASConflict(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{State: domain.StateRevoked})
	_, err := svc.Revoke(context.Background(), seeded.ID, "again")
	assertCatalogServiceError(t, err, domain.KindStateConflict, "session_state_conflict")
	if f.sessions.updateSessionCalls != 0 {
		t.Fatal("a revoked session was revoked again")
	}

	svc, f = newSessionServiceWithFakes(t)
	seeded = f.seedSession(domain.Session{State: domain.StateValid})
	f.sessions.forceSessionConflict = true
	_, err = svc.Revoke(context.Background(), seeded.ID, "leaked")
	assertCatalogServiceError(t, err, domain.KindStateConflict, "session_state_conflict")
}

func TestSessionServiceGetLazilyExpiresAndAttachesRelations(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	expiredAt := sessionTestNow.Add(-time.Minute)
	seeded := f.seedSession(domain.Session{State: domain.StateValid, ExpiresAt: &expiredAt})
	f.seedSnapshot(seeded.ID, `{"degraded":false}`)
	f.seedLease(seeded.ID, "worker-1", sessionTestNow.Add(30*time.Minute))
	f.seedTakeover(domain.Takeover{
		SessionID: &seeded.ID, Status: domain.TakeoverStatusWaiting,
		Reason: domain.TakeoverReasonMFA, ExpiresAt: &expiredAt,
	})

	session, snapshot, lease, takeover, err := svc.Get(context.Background(), seeded.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if session.State != domain.StateExpired {
		t.Fatalf("state=%q want expired", session.State)
	}
	if f.sessions.lastExpectedSessionState != domain.StateValid {
		t.Fatalf("session CAS expected=%q want valid", f.sessions.lastExpectedSessionState)
	}
	if f.sessions.sessions[seeded.ID].State != domain.StateExpired {
		t.Fatal("lazy expiry was not persisted")
	}
	if snapshot == nil || lease == nil || takeover == nil {
		t.Fatalf("relations snapshot=%v lease=%v takeover=%v", snapshot, lease, takeover)
	}
	if takeover.Status != domain.TakeoverStatusExpired {
		t.Fatalf("takeover status=%q want expired", takeover.Status)
	}
	if f.sessions.takeovers[takeover.ID].Status != domain.TakeoverStatusExpired {
		t.Fatal("lazy takeover expiry was not persisted")
	}
	if f.sessions.lastExpectedTakeoverStatus != domain.TakeoverStatusWaiting {
		t.Fatalf("takeover CAS expected=%q want waiting", f.sessions.lastExpectedTakeoverStatus)
	}
}

func TestSessionServiceGetHandlesAbsentOptionalRelations(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{State: domain.StateValid})

	session, snapshot, lease, takeover, err := svc.Get(context.Background(), seeded.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if session.ID != seeded.ID || session.State != domain.StateValid {
		t.Fatalf("session=%+v", session)
	}
	if snapshot != nil || lease != nil || takeover != nil {
		t.Fatalf("optional relations snapshot=%v lease=%v takeover=%v want nils", snapshot, lease, takeover)
	}

	_, _, _, _, err = svc.Get(context.Background(), 999)
	assertCatalogServiceError(t, err, domain.KindNotFound, "not_found")
}

func TestSessionServiceListLazilyExpiresSessions(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	expiredAt := sessionTestNow.Add(-time.Minute)
	first := f.seedSession(domain.Session{State: domain.StateValid, ExpiresAt: &expiredAt})
	second := f.seedSession(domain.Session{State: domain.StateNeedsManual})
	third := f.seedSession(domain.Session{State: domain.StateValid})

	sessions, more, err := svc.List(context.Background(), domain.SessionFilter{Page: domain.Page{Limit: 10}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 3 || more {
		t.Fatalf("sessions=%d more=%v", len(sessions), more)
	}
	if sessions[0].ID != first.ID || sessions[0].State != domain.StateExpired {
		t.Fatalf("first=%+v want it lazily expired", sessions[0])
	}
	if sessions[1].ID != second.ID || sessions[1].State != domain.StateNeedsManual {
		t.Fatalf("second=%+v", sessions[1])
	}
	if sessions[2].ID != third.ID || sessions[2].State != domain.StateValid {
		t.Fatalf("third=%+v", sessions[2])
	}
	if f.sessions.sessions[first.ID].State != domain.StateExpired {
		t.Fatal("list lazy expiry was not persisted")
	}
}

func TestSessionServiceListTakeoversLazilyExpiresWaitingRows(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{State: domain.StateNeedsManual})
	expiredAt := sessionTestNow.Add(-time.Minute)
	futureAt := sessionTestNow.Add(30 * time.Minute)
	expired := f.seedTakeover(domain.Takeover{
		SessionID: &seeded.ID, Status: domain.TakeoverStatusWaiting, ExpiresAt: &expiredAt,
	})
	waiting := f.seedTakeover(domain.Takeover{
		SessionID: &seeded.ID, Status: domain.TakeoverStatusWaiting, ExpiresAt: &futureAt,
	})
	completed := f.seedTakeover(domain.Takeover{
		SessionID: &seeded.ID, Status: domain.TakeoverStatusCompleted, ExpiresAt: &expiredAt,
	})

	takeovers, more, err := svc.ListTakeovers(context.Background(), domain.TakeoverFilter{Page: domain.Page{Limit: 10}})
	if err != nil {
		t.Fatalf("ListTakeovers: %v", err)
	}
	if len(takeovers) != 3 || more {
		t.Fatalf("takeovers=%d more=%v", len(takeovers), more)
	}
	if takeovers[0].ID != expired.ID || takeovers[0].Status != domain.TakeoverStatusExpired {
		t.Fatalf("expired=%+v", takeovers[0])
	}
	if takeovers[1].ID != waiting.ID || takeovers[1].Status != domain.TakeoverStatusWaiting {
		t.Fatalf("waiting=%+v", takeovers[1])
	}
	if takeovers[2].ID != completed.ID || takeovers[2].Status != domain.TakeoverStatusCompleted {
		t.Fatalf("completed=%+v", takeovers[2])
	}
	if f.sessions.takeovers[expired.ID].Status != domain.TakeoverStatusExpired {
		t.Fatal("list lazy takeover expiry was not persisted")
	}
}

func TestSessionServiceListReturnsLazyExpireSessionRereadError(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{State: domain.StateValid})
	// Make the valid session already past its expires_at.
	seeded.ExpiresAt = sessionTimePtr(sessionTestNow.Add(-time.Minute))
	f.sessions.sessions[seeded.ID] = seeded
	f.sessions.forceSessionConflict = true
	readErr := errors.New("forced session reread failure")
	f.sessions.getSessionErr = readErr
	f.sessions.getSessionErrOnCall = 1

	_, _, err := svc.List(context.Background(), domain.SessionFilter{})
	if !errors.Is(err, readErr) {
		t.Fatalf("List error=%v want the reread error %v", err, readErr)
	}
}

func TestSessionServiceListTakeoversReturnsLazyExpireTakeoverRereadError(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{State: domain.StateNeedsManual})
	f.seedTakeover(domain.Takeover{
		SessionID: &seeded.ID, Status: domain.TakeoverStatusWaiting,
		ExpiresAt: sessionTimePtr(sessionTestNow.Add(-time.Minute)),
	})
	f.sessions.forceTakeoverConflict = true
	readErr := errors.New("forced takeover reread failure")
	f.sessions.getTakeoverErr = readErr
	f.sessions.getTakeoverErrOnCall = 1

	_, _, err := svc.ListTakeovers(context.Background(), domain.TakeoverFilter{})
	if !errors.Is(err, readErr) {
		t.Fatalf("ListTakeovers error=%v want the reread error %v", err, readErr)
	}
}

func TestSessionServiceRequestTakeover(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{State: domain.StateValid})
	actor := int64(7)
	instructions := json.RawMessage(`{"text":"enter the code"}`)

	takeover, session, err := svc.RequestTakeover(context.Background(), seeded.ID,
		domain.TakeoverReasonMFA, instructions, 10*time.Minute, &actor)
	if err != nil {
		t.Fatalf("RequestTakeover: %v", err)
	}
	if takeover.Status != domain.TakeoverStatusWaiting || takeover.SessionID == nil ||
		*takeover.SessionID != seeded.ID {
		t.Fatalf("takeover=%+v", takeover)
	}
	if takeover.Reason != domain.TakeoverReasonMFA ||
		string(takeover.Instructions) != `{"text":"enter the code"}` ||
		takeover.RequestedBy == nil || *takeover.RequestedBy != actor {
		t.Fatalf("takeover=%+v", takeover)
	}
	if takeover.ExpiresAt == nil || !takeover.ExpiresAt.Equal(sessionTestNow.Add(10*time.Minute)) {
		t.Fatalf("takeover expires_at=%v", takeover.ExpiresAt)
	}
	if session.State != domain.StateNeedsManual {
		t.Fatalf("session state=%q want needs_manual", session.State)
	}
	if f.sessions.lastExpectedSessionState != domain.StateValid {
		t.Fatalf("session CAS expected=%q want valid", f.sessions.lastExpectedSessionState)
	}
	if f.sessions.sessions[seeded.ID].State != domain.StateNeedsManual {
		t.Fatal("takeover request did not persist needs_manual")
	}
}

func TestSessionServiceRequestTakeoverDefaultExpiry(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{State: domain.StateNeedsManual})

	takeover, _, err := svc.RequestTakeover(context.Background(), seeded.ID,
		domain.TakeoverReasonCaptcha, nil, 0, nil)
	if err != nil {
		t.Fatalf("RequestTakeover: %v", err)
	}
	if takeover.ExpiresAt == nil || !takeover.ExpiresAt.Equal(sessionTestNow.Add(30*time.Minute)) {
		t.Fatalf("takeover expires_at=%v want now+30m", takeover.ExpiresAt)
	}
	if len(takeover.Instructions) == 0 || string(takeover.Instructions) != `{}` {
		t.Fatalf("instructions=%q want {}", takeover.Instructions)
	}
}

func TestSessionServiceRequestTakeoverRejectsDuplicateWaiting(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{State: domain.StateNeedsManual})
	f.seedTakeover(domain.Takeover{
		SessionID: &seeded.ID, Status: domain.TakeoverStatusWaiting,
		ExpiresAt: sessionTimePtr(sessionTestNow.Add(30 * time.Minute)),
	})

	_, _, err := svc.RequestTakeover(context.Background(), seeded.ID,
		domain.TakeoverReasonMFA, nil, 0, nil)
	assertCatalogServiceError(t, err, domain.KindConflict, "takeover_not_waiting")
	if f.sessions.createTakeoverCalls != 0 {
		t.Fatalf("duplicate request created %d takeovers", f.sessions.createTakeoverCalls)
	}
}

func TestSessionServiceRequestTakeoverExpiresStaleWaiting(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{State: domain.StateNeedsManual})
	stale := f.seedTakeover(domain.Takeover{
		SessionID: &seeded.ID, Status: domain.TakeoverStatusWaiting,
		ExpiresAt: sessionTimePtr(sessionTestNow.Add(-time.Minute)),
	})

	takeover, _, err := svc.RequestTakeover(context.Background(), seeded.ID,
		domain.TakeoverReasonApproval, nil, 0, nil)
	if err != nil {
		t.Fatalf("RequestTakeover: %v", err)
	}
	if f.sessions.takeovers[stale.ID].Status != domain.TakeoverStatusExpired {
		t.Fatalf("stale takeover status=%q want expired", f.sessions.takeovers[stale.ID].Status)
	}
	if takeover.Status != domain.TakeoverStatusWaiting || takeover.ID == stale.ID {
		t.Fatalf("takeover=%+v", takeover)
	}
}

func TestSessionServiceRequestTakeoverRejectsRevokedAndBadReason(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	revoked := f.seedSession(domain.Session{State: domain.StateRevoked})
	_, _, err := svc.RequestTakeover(context.Background(), revoked.ID,
		domain.TakeoverReasonMFA, nil, 0, nil)
	assertCatalogServiceError(t, err, domain.KindStateConflict, "session_state_conflict")

	svc, f = newSessionServiceWithFakes(t)
	valid := f.seedSession(domain.Session{State: domain.StateValid})
	_, _, err = svc.RequestTakeover(context.Background(), valid.ID, "需要验证码", nil, 0, nil)
	domainErr := assertCatalogServiceError(t, err, domain.KindInvalid, "invalid_takeover_reason")
	if domainErr.Field != "reason" {
		t.Fatalf("field=%q want reason", domainErr.Field)
	}
	if f.sessions.createTakeoverCalls != 0 {
		t.Fatal("an invalid reason created a takeover")
	}
}

func TestSessionServiceRequestTakeoverCASConflictCancelsOrphan(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{State: domain.StateValid})
	f.sessions.forceSessionConflict = true

	_, _, err := svc.RequestTakeover(context.Background(), seeded.ID,
		domain.TakeoverReasonOther, nil, 0, nil)
	assertCatalogServiceError(t, err, domain.KindStateConflict, "session_state_conflict")
	if f.sessions.createTakeoverCalls != 1 {
		t.Fatalf("create takeover calls=%d want 1 (the request is recorded first)",
			f.sessions.createTakeoverCalls)
	}
	// The takeover created for a request whose session CAS lost must not stay
	// waiting: it would block every later takeover and advertise work that can
	// never be completed for a needs_manual session that was never moved.
	waiting, getErr := f.sessions.GetWaitingTakeover(context.Background(), seeded.ID)
	if getErr != nil {
		t.Fatalf("GetWaitingTakeover: %v", getErr)
	}
	if waiting != nil {
		t.Fatalf("session CAS failure left an orphan waiting takeover: %+v", waiting)
	}
	if len(f.sessions.takeovers) != 1 {
		t.Fatalf("takeovers=%d want the single attempted request", len(f.sessions.takeovers))
	}
	for _, takeover := range f.sessions.takeovers {
		if takeover.Status != domain.TakeoverStatusCancelled {
			t.Fatalf("orphan takeover status=%q want cancelled", takeover.Status)
		}
	}
	if f.sessions.lastExpectedTakeoverStatus != domain.TakeoverStatusWaiting {
		t.Fatalf("takeover CAS expected=%q want waiting", f.sessions.lastExpectedTakeoverStatus)
	}
	if f.sessions.sessions[seeded.ID].State != domain.StateValid {
		t.Fatalf("failed session CAS changed the state to %q",
			f.sessions.sessions[seeded.ID].State)
	}
}

func TestSessionServiceRequestTakeoverCASConflictCancelFailureStillReturnsConflict(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{State: domain.StateValid})
	f.sessions.forceSessionConflict = true
	f.sessions.forceTakeoverConflict = true

	_, _, err := svc.RequestTakeover(context.Background(), seeded.ID,
		domain.TakeoverReasonOther, nil, 0, nil)
	// Cancelling the orphan is best effort: a lost cancel CAS must not mask the
	// session conflict the caller needs.
	assertCatalogServiceError(t, err, domain.KindStateConflict, "session_state_conflict")
	if f.sessions.sessions[seeded.ID].State != domain.StateValid {
		t.Fatalf("failed session CAS changed the state to %q",
			f.sessions.sessions[seeded.ID].State)
	}
}

func TestSessionServiceCompleteTakeoverReplacesStateAndRechecks(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{
		State: domain.StateNeedsManual, FailureReason: "login_failed",
		ExpiresAt: sessionTimePtr(sessionTestNow.Add(time.Hour)),
	})
	oldHash := seeded.StorageStateHash
	takeover := f.seedTakeover(domain.Takeover{
		SessionID: &seeded.ID, Status: domain.TakeoverStatusWaiting,
		ExpiresAt: sessionTimePtr(sessionTestNow.Add(30 * time.Minute)),
	})
	f.checker.result = domain.LoginCheckResult{Observed: domain.ObservedAllowed, Status: 200}

	completed, session, snapshot, err := svc.CompleteTakeover(context.Background(), takeover.ID,
		ReplaceSessionStateInput{
			Kind:         domain.StorageStateKindToken,
			StorageState: json.RawMessage(`{"headers":{"Authorization":"Bearer rotated-token"}}`),
		}, nil)
	if err != nil {
		t.Fatalf("CompleteTakeover: %v", err)
	}
	if completed.Status != domain.TakeoverStatusCompleted || completed.CompletedAt == nil ||
		!completed.CompletedAt.Equal(sessionTestNow) {
		t.Fatalf("takeover=%+v", completed)
	}
	if f.sessions.lastExpectedTakeoverStatus != domain.TakeoverStatusWaiting {
		t.Fatalf("takeover CAS expected=%q want waiting", f.sessions.lastExpectedTakeoverStatus)
	}
	if session.State != domain.StateValid || session.FailureReason != "" {
		t.Fatalf("session=%+v", session)
	}
	if f.sessions.lastExpectedSessionState != domain.StateNeedsManual {
		t.Fatalf("session CAS expected=%q want needs_manual", f.sessions.lastExpectedSessionState)
	}
	if session.StorageStateKind != domain.StorageStateKindToken ||
		session.CookieCount != 0 || session.OriginCount != 0 {
		t.Fatalf("session storage state summary=%+v", session)
	}
	if session.StorageStateHash == oldHash {
		t.Fatal("storage_state_hash was not replaced")
	}
	assertSessionStorageHashMatches(t, session)
	if session.ExpiresAt == nil || !session.ExpiresAt.Equal(sessionTestNow.Add(time.Hour)) {
		t.Fatalf("expires_at=%v want it unchanged", session.ExpiresAt)
	}
	if snapshot.Degraded || snapshot.SessionID != seeded.ID {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if f.checker.lastState.Headers["Authorization"] != "Bearer rotated-token" {
		t.Fatalf("checker state=%+v", f.checker.lastState)
	}
	stored := f.sessions.sessions[seeded.ID]
	if stored.State != domain.StateValid || stored.StorageStateHash != session.StorageStateHash {
		t.Fatalf("stored session=%+v", stored)
	}
	if f.sessions.takeovers[takeover.ID].Status != domain.TakeoverStatusCompleted {
		t.Fatal("completed takeover was not persisted")
	}
}

func TestSessionServiceCompleteTakeoverKeepsNeedsManualOnFailedCheck(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{State: domain.StateNeedsManual})
	takeover := f.seedTakeover(domain.Takeover{
		SessionID: &seeded.ID, Status: domain.TakeoverStatusWaiting,
		ExpiresAt: sessionTimePtr(sessionTestNow.Add(30 * time.Minute)),
	})
	f.checker.result = domain.LoginCheckResult{Degraded: true, Reason: "login_check_not_configured"}

	completed, session, snapshot, err := svc.CompleteTakeover(context.Background(), takeover.ID,
		ReplaceSessionStateInput{
			Kind:         domain.StorageStateKindStorageState,
			StorageState: sessionStateWithCookieExpiry(sessionTestNow.Add(time.Hour).Unix()),
		}, nil)
	if err != nil {
		t.Fatalf("CompleteTakeover: %v", err)
	}
	if completed.Status != domain.TakeoverStatusCompleted {
		t.Fatalf("takeover=%+v", completed)
	}
	if session.State != domain.StateNeedsManual || session.FailureReason != "login_check_not_configured" {
		t.Fatalf("session=%+v", session)
	}
	if !snapshot.Degraded {
		t.Fatal("skipped check snapshot is not degraded")
	}
}

func TestSessionServiceCompleteTakeoverRejectsNonWaiting(t *testing.T) {
	for _, status := range []string{
		domain.TakeoverStatusCompleted, domain.TakeoverStatusCancelled,
	} {
		t.Run(status, func(t *testing.T) {
			svc, f := newSessionServiceWithFakes(t)
			seeded := f.seedSession(domain.Session{State: domain.StateNeedsManual})
			takeover := f.seedTakeover(domain.Takeover{SessionID: &seeded.ID, Status: status})

			_, _, _, err := svc.CompleteTakeover(context.Background(), takeover.ID,
				ReplaceSessionStateInput{Kind: "storage_state", StorageState: sessionStorageStateJSON()}, nil)
			assertCatalogServiceError(t, err, domain.KindConflict, "takeover_not_waiting")
			if f.sessions.updateSessionCalls != 0 || f.checker.calls != 0 {
				t.Fatal("a non-waiting takeover ran the completion path")
			}
		})
	}
}

func TestSessionServiceCompleteTakeoverExpiresStaleWaiting(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{State: domain.StateNeedsManual})
	takeover := f.seedTakeover(domain.Takeover{
		SessionID: &seeded.ID, Status: domain.TakeoverStatusWaiting,
		ExpiresAt: sessionTimePtr(sessionTestNow.Add(-time.Minute)),
	})

	_, _, _, err := svc.CompleteTakeover(context.Background(), takeover.ID,
		ReplaceSessionStateInput{Kind: "storage_state", StorageState: sessionStorageStateJSON()}, nil)
	assertCatalogServiceError(t, err, domain.KindConflict, "takeover_not_waiting")
	if f.sessions.takeovers[takeover.ID].Status != domain.TakeoverStatusExpired {
		t.Fatalf("stale takeover status=%q want expired", f.sessions.takeovers[takeover.ID].Status)
	}
	if f.sessions.updateSessionCalls != 0 {
		t.Fatal("an expired takeover updated the session")
	}
}

func TestSessionServiceCompleteTakeoverRejectsInvalidStorageState(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{State: domain.StateNeedsManual})
	takeover := f.seedTakeover(domain.Takeover{
		SessionID: &seeded.ID, Status: domain.TakeoverStatusWaiting,
		ExpiresAt: sessionTimePtr(sessionTestNow.Add(30 * time.Minute)),
	})

	_, _, _, err := svc.CompleteTakeover(context.Background(), takeover.ID,
		ReplaceSessionStateInput{Kind: "storage_state", StorageState: json.RawMessage(`{"cookies":`)}, nil)
	assertCatalogServiceError(t, err, domain.KindInvalid, "invalid_storage_state")
	if f.sessions.takeovers[takeover.ID].Status != domain.TakeoverStatusWaiting {
		t.Fatal("an invalid completion cancelled the takeover")
	}
	if f.sessions.updateSessionCalls != 0 || f.checker.calls != 0 {
		t.Fatal("an invalid completion reached the session")
	}
}

func TestSessionServiceCompleteTakeoverRejectsRevokedSession(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{State: domain.StateRevoked})
	takeover := f.seedTakeover(domain.Takeover{
		SessionID: &seeded.ID, Status: domain.TakeoverStatusWaiting,
		ExpiresAt: sessionTimePtr(sessionTestNow.Add(30 * time.Minute)),
	})

	_, _, _, err := svc.CompleteTakeover(context.Background(), takeover.ID,
		ReplaceSessionStateInput{Kind: "storage_state", StorageState: sessionStorageStateJSON()}, nil)
	assertCatalogServiceError(t, err, domain.KindStateConflict, "session_state_conflict")
	if f.sessions.updateSessionCalls != 0 {
		t.Fatal("a revoked session was completed")
	}
}

func TestSessionServiceCompleteTakeoverCASConflictLeavesTakeoverWaiting(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{State: domain.StateNeedsManual})
	takeover := f.seedTakeover(domain.Takeover{
		SessionID: &seeded.ID, Status: domain.TakeoverStatusWaiting,
		ExpiresAt: sessionTimePtr(sessionTestNow.Add(30 * time.Minute)),
	})
	f.checker.result = domain.LoginCheckResult{Observed: domain.ObservedAllowed, Status: 200}
	f.sessions.forceSessionConflict = true

	_, _, _, err := svc.CompleteTakeover(context.Background(), takeover.ID,
		ReplaceSessionStateInput{Kind: "storage_state", StorageState: sessionStorageStateJSON()}, nil)
	assertCatalogServiceError(t, err, domain.KindStateConflict, "session_state_conflict")
	if f.sessions.takeovers[takeover.ID].Status != domain.TakeoverStatusWaiting {
		t.Fatal("a failed session CAS completed the takeover")
	}
	if f.sessions.updateTakeoverCalls != 0 {
		t.Fatal("a failed session CAS updated the takeover")
	}
}

func TestSessionServiceCompleteTakeoverTakeoverCASConflictLeavesSessionUpdated(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{State: domain.StateNeedsManual})
	takeover := f.seedTakeover(domain.Takeover{
		SessionID: &seeded.ID, Status: domain.TakeoverStatusWaiting,
		ExpiresAt: sessionTimePtr(sessionTestNow.Add(30 * time.Minute)),
	})
	f.checker.result = domain.LoginCheckResult{Observed: domain.ObservedAllowed, Status: 200}
	f.sessions.forceTakeoverConflict = true

	_, _, snapshot, err := svc.CompleteTakeover(context.Background(), takeover.ID,
		ReplaceSessionStateInput{Kind: "storage_state", StorageState: sessionStorageStateJSON()}, nil)
	assertCatalogServiceError(t, err, domain.KindStateConflict, "session_state_conflict")
	if snapshot != nil {
		t.Fatalf("a lost takeover CAS returned a snapshot: %+v", snapshot)
	}
	// Accepted ruling (T13 fix round 1): the session is updated before the
	// takeover CAS. When the takeover CAS loses, the session may already carry
	// the completed login state; the caller gets the conflict and the admin can
	// re-import. A transaction is deliberately out of scope for single-admin P2.
	if got := f.sessions.sessions[seeded.ID]; got.State != domain.StateValid {
		t.Fatalf("session state=%q want valid (the session write precedes the takeover CAS)", got.State)
	}
	if f.sessions.updateSessionCalls != 1 {
		t.Fatalf("session update calls=%d want 1", f.sessions.updateSessionCalls)
	}
	if f.sessions.takeovers[takeover.ID].Status != domain.TakeoverStatusWaiting {
		t.Fatalf("takeover status=%q want waiting",
			f.sessions.takeovers[takeover.ID].Status)
	}
}

func TestSessionServiceCompleteTakeoverRequiresLinkedSession(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	takeover := f.seedTakeover(domain.Takeover{
		Status: domain.TakeoverStatusWaiting,
		Reason: domain.TakeoverReasonOther,
	})

	_, _, _, err := svc.CompleteTakeover(context.Background(), takeover.ID,
		ReplaceSessionStateInput{Kind: "storage_state", StorageState: sessionStorageStateJSON()}, nil)
	assertCatalogServiceError(t, err, domain.KindConflict, "takeover_not_waiting")
}

func TestSessionServiceCompleteTakeoverMissing(t *testing.T) {
	svc, _ := newSessionServiceWithFakes(t)
	_, _, _, err := svc.CompleteTakeover(context.Background(), 999,
		ReplaceSessionStateInput{Kind: "storage_state", StorageState: sessionStorageStateJSON()}, nil)
	assertCatalogServiceError(t, err, domain.KindNotFound, "not_found")
}

func TestSessionServiceCancelTakeover(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{State: domain.StateNeedsManual})
	takeover := f.seedTakeover(domain.Takeover{
		SessionID: &seeded.ID, Status: domain.TakeoverStatusWaiting,
		ExpiresAt: sessionTimePtr(sessionTestNow.Add(30 * time.Minute)),
	})

	cancelled, err := svc.CancelTakeover(context.Background(), takeover.ID)
	if err != nil {
		t.Fatalf("CancelTakeover: %v", err)
	}
	if cancelled.Status != domain.TakeoverStatusCancelled || cancelled.CompletedAt != nil {
		t.Fatalf("takeover=%+v", cancelled)
	}
	if f.sessions.lastExpectedTakeoverStatus != domain.TakeoverStatusWaiting {
		t.Fatalf("CAS expected=%q want waiting", f.sessions.lastExpectedTakeoverStatus)
	}
	if f.sessions.sessions[seeded.ID].State != domain.StateNeedsManual {
		t.Fatalf("cancel changed the session state to %q", f.sessions.sessions[seeded.ID].State)
	}
}

func TestSessionServiceCancelTakeoverRejectsNonWaiting(t *testing.T) {
	for _, status := range []string{
		domain.TakeoverStatusCompleted, domain.TakeoverStatusCancelled, domain.TakeoverStatusExpired,
	} {
		t.Run(status, func(t *testing.T) {
			svc, f := newSessionServiceWithFakes(t)
			seeded := f.seedSession(domain.Session{State: domain.StateNeedsManual})
			takeover := f.seedTakeover(domain.Takeover{SessionID: &seeded.ID, Status: status})

			_, err := svc.CancelTakeover(context.Background(), takeover.ID)
			assertCatalogServiceError(t, err, domain.KindConflict, "takeover_not_waiting")
			if f.sessions.updateTakeoverCalls != 0 {
				t.Fatal("a non-waiting takeover was cancelled")
			}
		})
	}
}

func TestSessionServiceCancelTakeoverLazilyExpires(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{State: domain.StateNeedsManual})
	takeover := f.seedTakeover(domain.Takeover{
		SessionID: &seeded.ID, Status: domain.TakeoverStatusWaiting,
		ExpiresAt: sessionTimePtr(sessionTestNow.Add(-time.Minute)),
	})

	_, err := svc.CancelTakeover(context.Background(), takeover.ID)
	assertCatalogServiceError(t, err, domain.KindConflict, "takeover_not_waiting")
	if f.sessions.takeovers[takeover.ID].Status != domain.TakeoverStatusExpired {
		t.Fatalf("stale takeover status=%q want expired", f.sessions.takeovers[takeover.ID].Status)
	}
}

func TestSessionServiceCancelTakeoverMissing(t *testing.T) {
	svc, _ := newSessionServiceWithFakes(t)
	_, err := svc.CancelTakeover(context.Background(), 999)
	assertCatalogServiceError(t, err, domain.KindNotFound, "not_found")
}

func TestSessionServiceAcquireLease(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{State: domain.StateValid})

	lease, err := svc.AcquireLease(context.Background(), seeded.ID, "worker-1", 0)
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	if lease.SessionID != seeded.ID || lease.Holder != "worker-1" || lease.ReleasedAt != nil {
		t.Fatalf("lease=%+v", lease)
	}
	if !lease.ExpiresAt.Equal(sessionTestNow.Add(30 * time.Minute)) {
		t.Fatalf("lease expires_at=%v want now+30m", lease.ExpiresAt)
	}
	if f.sessions.createLeaseCalls != 1 {
		t.Fatalf("create lease calls=%d", f.sessions.createLeaseCalls)
	}
}

func TestSessionServiceAcquireLeaseCapsAtSessionExpiry(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{
		State: domain.StateValid, ExpiresAt: sessionTimePtr(sessionTestNow.Add(10 * time.Minute)),
	})

	lease, err := svc.AcquireLease(context.Background(), seeded.ID, "worker-1", 0)
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	if !lease.ExpiresAt.Equal(sessionTestNow.Add(10 * time.Minute)) {
		t.Fatalf("lease expires_at=%v want the session expiry", lease.ExpiresAt)
	}

	svc, f = newSessionServiceWithFakes(t)
	seeded = f.seedSession(domain.Session{State: domain.StateValid})
	lease, err = svc.AcquireLease(context.Background(), seeded.ID, "worker-2", 5*time.Minute)
	if err != nil {
		t.Fatalf("AcquireLease with ttl: %v", err)
	}
	if !lease.ExpiresAt.Equal(sessionTestNow.Add(5 * time.Minute)) {
		t.Fatalf("lease expires_at=%v want now+5m", lease.ExpiresAt)
	}
}

func TestSessionServiceAcquireLeaseConflict(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{State: domain.StateValid})
	if _, err := svc.AcquireLease(context.Background(), seeded.ID, "worker-1", 0); err != nil {
		t.Fatalf("first AcquireLease: %v", err)
	}

	_, err := svc.AcquireLease(context.Background(), seeded.ID, "worker-2", 0)
	assertCatalogServiceError(t, err, domain.KindLeaseConflict, "lease_conflict")
}

func TestSessionServiceAcquireLeaseRequiresValidSession(t *testing.T) {
	for _, state := range []string{domain.StateNeedsManual, domain.StateExpired, domain.StateInvalid} {
		t.Run(state, func(t *testing.T) {
			svc, f := newSessionServiceWithFakes(t)
			seeded := f.seedSession(domain.Session{State: state})
			_, err := svc.AcquireLease(context.Background(), seeded.ID, "worker-1", 0)
			assertCatalogServiceError(t, err, domain.KindStateConflict, "session_state_conflict")
			if f.sessions.createLeaseCalls != 0 {
				t.Fatal("a non-valid session acquired a lease")
			}
		})
	}
}

func TestSessionServiceAcquireLeaseRejectsExpiredSession(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{
		State: domain.StateValid, ExpiresAt: sessionTimePtr(sessionTestNow.Add(-time.Minute)),
	})

	_, err := svc.AcquireLease(context.Background(), seeded.ID, "worker-1", 0)
	assertCatalogServiceError(t, err, domain.KindStateConflict, "session_state_conflict")
	if f.sessions.createLeaseCalls != 0 {
		t.Fatal("an expired session acquired a lease")
	}
}

func TestSessionServiceAcquireLeaseMissingSession(t *testing.T) {
	svc, _ := newSessionServiceWithFakes(t)
	_, err := svc.AcquireLease(context.Background(), 999, "worker-1", 0)
	assertCatalogServiceError(t, err, domain.KindNotFound, "not_found")
}

func TestSessionServiceReleaseLease(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{State: domain.StateValid})
	lease, err := svc.AcquireLease(context.Background(), seeded.ID, "worker-1", 0)
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}

	if err := svc.ReleaseLease(context.Background(), seeded.ID, "worker-2", "wrong holder"); err != nil {
		t.Fatalf("ReleaseLease wrong holder: %v", err)
	}
	if active, _ := f.sessions.GetActiveLease(context.Background(), seeded.ID); active == nil {
		t.Fatal("a foreign holder released the lease")
	}

	if err := svc.ReleaseLease(context.Background(), seeded.ID, "worker-1", "done"); err != nil {
		t.Fatalf("ReleaseLease: %v", err)
	}
	stored := f.sessions.leases[seeded.ID][len(f.sessions.leases[seeded.ID])-1]
	if stored.ReleasedAt == nil || stored.ReleasedReason != "done" || stored.ID != lease.ID {
		t.Fatalf("stored lease=%+v", stored)
	}
	reacquired, err := svc.AcquireLease(context.Background(), seeded.ID, "worker-2", 0)
	if err != nil {
		t.Fatalf("reacquire: %v", err)
	}
	if reacquired.Holder != "worker-2" || reacquired.ID == lease.ID {
		t.Fatalf("reacquired lease=%+v", reacquired)
	}
}

func TestSessionServiceAcquireLeaseReclaimsExpiredLease(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{State: domain.StateValid})
	stale := f.seedLease(seeded.ID, "worker-old", sessionTestNow.Add(-time.Minute))

	lease, err := svc.AcquireLease(context.Background(), seeded.ID, "worker-new", 0)
	if err != nil {
		t.Fatalf("AcquireLease after an expired lease: %v", err)
	}
	if lease.ID == stale.ID || lease.Holder != "worker-new" {
		t.Fatalf("lease=%+v", lease)
	}
	stored := f.sessions.leases[seeded.ID][0]
	if stored.ReleasedAt == nil || stored.ReleasedReason != "expired" {
		t.Fatalf("stale lease=%+v", stored)
	}
}

func TestSessionServiceListSnapshots(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	seeded := f.seedSession(domain.Session{State: domain.StateValid})
	first := f.seedSnapshot(seeded.ID, `{"degraded":false}`)
	second := f.seedSnapshot(seeded.ID, `{"degraded":true}`)

	page, more, err := svc.ListSnapshots(context.Background(), seeded.ID, domain.Page{Limit: 1})
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(page) != 1 || !more || page[0].ID != first.ID {
		t.Fatalf("page=%+v more=%v", page, more)
	}
	page, more, err = svc.ListSnapshots(context.Background(), seeded.ID, domain.Page{Limit: 1, AfterID: first.ID})
	if err != nil {
		t.Fatalf("ListSnapshots page 2: %v", err)
	}
	if len(page) != 1 || more || page[0].ID != second.ID {
		t.Fatalf("page=%+v more=%v", page, more)
	}
}
