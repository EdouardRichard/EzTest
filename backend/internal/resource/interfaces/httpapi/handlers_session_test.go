package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// sessionHTTPTestNow is the fixed instant the session handler tests use.
var sessionHTTPTestNow = time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC)

// sessionHTTPTestStorageState is the seeded browser state; its cookie value is
// distinctive so responses can be checked for leaks.
const sessionHTTPTestStorageState = `{"cookies":[{"name":"sid","value":"fixture-secret-cookie",` +
	`"domain":"fixture.local","path":"/","expires":0,"httpOnly":true,"secure":false,` +
	`"sameSite":"Lax"}],"origins":[]}`

// sessionHTTPTestChecker is an in-memory application.LoginChecker.
type sessionHTTPTestChecker struct {
	result domain.LoginCheckResult
	err    error
	calls  int
}

// Check records the request and returns the configured result.
func (c *sessionHTTPTestChecker) Check(_ context.Context, _ domain.Environment, _ domain.StorageState) (domain.LoginCheckResult, error) {
	c.calls++
	if c.err != nil {
		return domain.LoginCheckResult{}, c.err
	}
	return c.result, nil
}

// sessionHTTPTestRepo is an in-memory domain.SessionRepository for the HTTP
// tests. It mirrors the production CAS semantics, lease reclaim and takeover
// defaults.
type sessionHTTPTestRepo struct {
	now func() time.Time

	sessions  map[int64]domain.Session
	snapshots map[int64][]domain.Snapshot
	leases    map[int64][]domain.Lease
	takeovers map[int64]domain.Takeover

	nextSessionID  int64
	nextSnapshotID int64
	nextLeaseID    int64
	nextTakeoverID int64
}

// newSessionHTTPTestRepo returns an empty repository.
func newSessionHTTPTestRepo(now func() time.Time) *sessionHTTPTestRepo {
	return &sessionHTTPTestRepo{
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
func (r *sessionHTTPTestRepo) CreateSession(_ context.Context, s *domain.Session) error {
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

// GetSession returns one session by id.
func (r *sessionHTTPTestRepo) GetSession(_ context.Context, id int64) (*domain.Session, error) {
	session, ok := r.sessions[id]
	if !ok {
		return nil, domain.NewNotFound("session", id)
	}
	return &session, nil
}

// ListSessions returns one page ordered by id ascending.
func (r *sessionHTTPTestRepo) ListSessions(_ context.Context, f domain.SessionFilter) ([]domain.Session, bool, error) {
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
func (r *sessionHTTPTestRepo) UpdateSession(_ context.Context, s *domain.Session, expectedState string) error {
	current, ok := r.sessions[s.ID]
	if !ok || current.State != expectedState {
		return domain.NewStateConflict("session state conflict")
	}
	s.UpdatedAt = r.now()
	r.sessions[s.ID] = *s
	return nil
}

// CreateSnapshot appends an immutable snapshot.
func (r *sessionHTTPTestRepo) CreateSnapshot(_ context.Context, s *domain.Snapshot) error {
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
func (r *sessionHTTPTestRepo) GetLatestSnapshot(_ context.Context, sessionID int64) (*domain.Snapshot, error) {
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
func (r *sessionHTTPTestRepo) ListSnapshots(_ context.Context, sessionID int64, p domain.Page) ([]domain.Snapshot, bool, error) {
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

// CreateLease reclaims expired leases and enforces one active lease per
// session.
func (r *sessionHTTPTestRepo) CreateLease(_ context.Context, l *domain.Lease) error {
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
func (r *sessionHTTPTestRepo) GetActiveLease(_ context.Context, sessionID int64) (*domain.Lease, error) {
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
func (r *sessionHTTPTestRepo) ReleaseLease(_ context.Context, sessionID int64, holder, reason string) error {
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
func (r *sessionHTTPTestRepo) ReleaseAllLeases(_ context.Context, sessionID int64, reason string) error {
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
func (r *sessionHTTPTestRepo) CreateTakeover(_ context.Context, t *domain.Takeover) error {
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

// GetTakeover returns one takeover by id.
func (r *sessionHTTPTestRepo) GetTakeover(_ context.Context, id int64) (*domain.Takeover, error) {
	takeover, ok := r.takeovers[id]
	if !ok {
		return nil, domain.NewNotFound("takeover", id)
	}
	return &takeover, nil
}

// GetWaitingTakeover returns the newest waiting takeover of a session.
func (r *sessionHTTPTestRepo) GetWaitingTakeover(_ context.Context, sessionID int64) (*domain.Takeover, error) {
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
func (r *sessionHTTPTestRepo) GetLatestTakeover(_ context.Context, sessionID int64) (*domain.Takeover, error) {
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
func (r *sessionHTTPTestRepo) ListTakeovers(_ context.Context, f domain.TakeoverFilter) ([]domain.Takeover, bool, error) {
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
func (r *sessionHTTPTestRepo) UpdateTakeover(_ context.Context, t *domain.Takeover, expectedStatus string) error {
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

// sessionHTTPTestEnv bundles the session handler fakes.
type sessionHTTPTestEnv struct {
	sessions *sessionHTTPTestRepo
	accesses *accessHTTPTestAccessRepo
	envs     *catalogHTTPTestEnvironmentRepo
	systems  *catalogHTTPTestSystemRepo
	checker  *sessionHTTPTestChecker
}

// newSessionHandlerTestServer mounts the session routes behind the real auth
// middleware and RequireAdmin and seeds the fixture system/environment/access.
func newSessionHandlerTestServer(t *testing.T) (*http.ServeMux, *sessionHTTPTestEnv) {
	t.Helper()
	env := &sessionHTTPTestEnv{
		accesses: newAccessHTTPTestAccessRepo(),
		envs:     newCatalogHTTPTestEnvironmentRepo(),
		systems:  newCatalogHTTPTestSystemRepo(),
		checker:  &sessionHTTPTestChecker{},
	}
	env.sessions = newSessionHTTPTestRepo(func() time.Time { return sessionHTTPTestNow })
	system := env.systems.seed("fixture-web")
	env.envs.environments[1] = domain.Environment{
		ID: 1, SystemID: system.ID, Key: "dev", Name: "Dev",
		BaseURL: "http://localhost:13335", Kind: domain.EnvironmentKindDev,
	}
	env.envs.nextID = 2
	env.accesses.seed(domain.Access{
		AccountID: 3, SystemID: system.ID, AuthType: domain.AuthTypeManual,
		Enabled: true, ConcurrencyLimit: 1,
	})
	service := application.NewSessionService(env.sessions, env.accesses, env.envs, env.systems,
		env.checker, catalogHTTPTestClock{now: sessionHTTPTestNow})
	mux := http.NewServeMux()
	guard := func(next http.Handler) http.Handler {
		return identityhttp.AuthMiddleware(catalogHTTPTestAuthenticator{})(identityhttp.RequireAdmin(next))
	}
	NewSessionHandler(service).RegisterRoutes(mux, guard)
	return mux, env
}

// seedSession inserts a fixture session in state.
func (e *sessionHTTPTestEnv) seedSession(state string) domain.Session {
	session := domain.Session{
		ID:               e.sessions.nextSessionID,
		AccountID:        3,
		SystemID:         1,
		EnvironmentID:    1,
		AccessID:         1,
		State:            state,
		Acquisition:      domain.AcquisitionImported,
		StorageState:     json.RawMessage(sessionHTTPTestStorageState),
		StorageStateKind: domain.StorageStateKindStorageState,
		CookieCount:      1,
		OriginCount:      0,
		ExpiresAt:        sessionHTTPTimePtr(sessionHTTPTestNow.Add(time.Hour)),
		CreatedAt:        sessionHTTPTestNow.Add(-time.Hour),
		UpdatedAt:        sessionHTTPTestNow,
	}
	parsedState, err := domain.ParseStorageState(session.StorageStateKind, session.StorageState)
	if err != nil {
		panic(err)
	}
	session.StorageStateHash, _ = domain.HashJSON(parsedState)
	e.sessions.nextSessionID++
	e.sessions.sessions[session.ID] = session
	return session
}

// seedSnapshot appends a fixture snapshot.
func (e *sessionHTTPTestEnv) seedSnapshot(sessionID int64, degraded bool) domain.Snapshot {
	snapshot := domain.Snapshot{
		ID: e.sessions.nextSnapshotID, SessionID: sessionID, Kind: "login",
		Items:      json.RawMessage(`{}`),
		Summary:    json.RawMessage(`{"total":1,"allowed":1,"denied":0,"error":0,"degraded":false}`),
		CapturedAt: sessionHTTPTestNow.Add(-time.Minute),
	}
	if degraded {
		snapshot.Degraded = true
		snapshot.Summary = json.RawMessage(`{"degraded":true,"reason":"manual_confirmation"}`)
	}
	e.sessions.nextSnapshotID++
	e.sessions.snapshots[sessionID] = append(e.sessions.snapshots[sessionID], snapshot)
	return snapshot
}

// seedLease inserts an active fixture lease.
func (e *sessionHTTPTestEnv) seedLease(sessionID int64, holder string) domain.Lease {
	lease := domain.Lease{
		ID: e.sessions.nextLeaseID, SessionID: sessionID, Holder: holder,
		AcquiredAt: sessionHTTPTestNow.Add(-time.Minute),
		ExpiresAt:  sessionHTTPTestNow.Add(30 * time.Minute),
	}
	e.sessions.nextLeaseID++
	e.sessions.leases[sessionID] = append(e.sessions.leases[sessionID], lease)
	return lease
}

// seedTakeover inserts a fixture takeover linked to sessionID.
func (e *sessionHTTPTestEnv) seedTakeover(sessionID int64, status string) domain.Takeover {
	takeover := domain.Takeover{
		ID: e.sessions.nextTakeoverID, SessionID: &sessionID, AccountID: 3,
		SystemID: 1, EnvironmentID: 1, Reason: domain.TakeoverReasonMFA,
		Status: status, Instructions: json.RawMessage(`{"text":"seeded"}`),
		RequestedAt: sessionHTTPTestNow.Add(-time.Minute),
		ExpiresAt:   sessionHTTPTimePtr(sessionHTTPTestNow.Add(30 * time.Minute)),
	}
	e.sessions.nextTakeoverID++
	e.sessions.takeovers[takeover.ID] = takeover
	return takeover
}

// sessionHTTPTimePtr returns a pointer to value.
func sessionHTTPTimePtr(value time.Time) *time.Time { return &value }

// sessionRequest performs one request against the session test mux.
func sessionRequest(mux *http.ServeMux, method, path, token, body string) *httptest.ResponseRecorder {
	var bodyReader *strings.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	var requestBody interface{ Read([]byte) (int, error) }
	if bodyReader != nil {
		requestBody = bodyReader
	}
	req := httptest.NewRequest(method, path, requestBody)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// assertNoSessionSecretLeak fails when a response carries the stored login
// state document, a credential or the fixture secret cookie value. The
// storageStateKind/value pair is a public DTO field and is not a leak.
func assertNoSessionSecretLeak(t *testing.T, rr *httptest.ResponseRecorder, secrets ...string) {
	t.Helper()
	body := rr.Body.String()
	for _, forbidden := range []string{`"storageState":`, `"credential":`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaks %s: %s", forbidden, body)
		}
	}
	for _, secret := range append(secrets, "fixture-secret-cookie") {
		if strings.Contains(body, secret) {
			t.Fatalf("response leaks the secret %q: %s", secret, body)
		}
	}
}

// sessionJSONMap reads a nested JSON object from a decoded DTO.
func sessionJSONMap(t *testing.T, object map[string]any, field string) map[string]any {
	t.Helper()
	value, ok := object[field]
	if !ok {
		t.Fatalf("missing %q in %v", field, object)
	}
	nested, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%q=%v is not an object", field, value)
	}
	return nested
}

func TestSessionHandlerImport(t *testing.T) {
	mux, env := newSessionHandlerTestServer(t)
	env.checker.result = domain.LoginCheckResult{Observed: domain.ObservedAllowed, Status: 200}

	body := `{"environmentId":1,"kind":"storage_state",` +
		`"storageState":{"cookies":[{"name":"sid","value":"fixture-secret-cookie",` +
		`"domain":"fixture.local","path":"/","expires":0}],"origins":[]},"note":"fixture import"}`
	rr := sessionRequest(mux, http.MethodPost, "/api/resources/accesses/1/sessions", catalogAdminToken, body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 (body=%s)", rr.Code, rr.Body.String())
	}
	data := decodeAccessData[map[string]any](t, rr)
	if data["state"] != domain.StateValid || data["environmentId"] != float64(1) ||
		data["accessId"] != float64(1) || data["accountId"] != float64(3) {
		t.Fatalf("data=%v", data)
	}
	if data["storageStateKind"] != domain.StorageStateKindStorageState ||
		data["cookieCount"] != float64(1) || data["originCount"] != float64(0) {
		t.Fatalf("data=%v", data)
	}
	if _, ok := data["expiring"].(bool); !ok {
		t.Fatalf("expiring=%v want a boolean", data["expiring"])
	}
	snapshot := sessionJSONMap(t, data, "latestSnapshot")
	if snapshot["degraded"] != false {
		t.Fatalf("latestSnapshot=%v", snapshot)
	}
	summary := sessionJSONMap(t, snapshot, "summary")
	if summary["allowed"] != float64(1) || summary["degraded"] != false {
		t.Fatalf("summary=%v", summary)
	}
	assertNoSessionSecretLeak(t, rr)
	if len(env.sessions.sessions) != 1 {
		t.Fatalf("stored %d sessions", len(env.sessions.sessions))
	}

	// A token import uses the separate headers field and keeps the token kind.
	env.checker.result = domain.LoginCheckResult{Observed: domain.ObservedAllowed, Status: 200}
	rr = sessionRequest(mux, http.MethodPost, "/api/resources/accesses/1/sessions", catalogAdminToken,
		`{"environmentId":1,"kind":"token","headers":{"Authorization":"Bearer fixture-token"}}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("token import status=%d want 201 (body=%s)", rr.Code, rr.Body.String())
	}
	token := decodeAccessData[map[string]any](t, rr)
	if token["storageStateKind"] != domain.StorageStateKindToken || token["state"] != domain.StateValid {
		t.Fatalf("token data=%v", token)
	}
	assertNoSessionSecretLeak(t, rr, "Bearer fixture-token")

	// A skipped login_check keeps the session importable but needs manual work.
	env.checker.result = domain.LoginCheckResult{Degraded: true, Reason: "login_check_not_configured"}
	rr = sessionRequest(mux, http.MethodPost, "/api/resources/accesses/1/sessions", catalogAdminToken, body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("degraded import status=%d want 201 (body=%s)", rr.Code, rr.Body.String())
	}
	degraded := decodeAccessData[map[string]any](t, rr)
	if degraded["state"] != domain.StateNeedsManual ||
		degraded["failureReason"] != "login_check_not_configured" {
		t.Fatalf("degraded data=%v", degraded)
	}
	if sessionJSONMap(t, degraded, "latestSnapshot")["degraded"] != true {
		t.Fatalf("degraded snapshot=%v", degraded["latestSnapshot"])
	}
}

func TestSessionHandlerImportRejectsInvalidInput(t *testing.T) {
	mux, env := newSessionHandlerTestServer(t)

	cases := []struct {
		name   string
		body   string
		status int
		reason string
		field  string
	}{
		{
			name: "malformed storage state", status: http.StatusUnprocessableEntity,
			reason: "invalid_storage_state", field: "storageState",
			body: `{"environmentId":1,"kind":"storage_state","storageState":{"cookies":"nope","origins":[]}}`,
		},
		{
			name: "environment of another system", status: http.StatusUnprocessableEntity,
			reason: "invalid_environment", field: "environmentId",
			body: `{"environmentId":99,"kind":"storage_state","storageState":{"cookies":[],"origins":[]}}`,
		},
		{
			name: "unknown kind", status: http.StatusUnprocessableEntity,
			reason: "invalid_storage_state", field: "kind",
			body: `{"environmentId":1,"kind":"cookie","storageState":{}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := sessionRequest(mux, http.MethodPost, "/api/resources/accesses/1/sessions", catalogAdminToken, tc.body)
			apiErr := assertAccessErrorEnvelope(t, rr, tc.status, "invalid_argument", tc.reason)
			if apiErr.Details["field"] != tc.field {
				t.Fatalf("details.field=%v want %q", apiErr.Details["field"], tc.field)
			}
		})
	}
	if len(env.sessions.sessions) != 0 {
		t.Fatalf("invalid imports created %d sessions", len(env.sessions.sessions))
	}

	rr := sessionRequest(mux, http.MethodPost, "/api/resources/accesses/999/sessions", catalogAdminToken,
		`{"environmentId":1,"kind":"storage_state","storageState":{"cookies":[],"origins":[]}}`)
	assertAccessErrorEnvelope(t, rr, http.StatusNotFound, "not_found", "not_found")
}

func TestSessionHandlerImportBodyCap(t *testing.T) {
	mux, env := newSessionHandlerTestServer(t)
	env.checker.result = domain.LoginCheckResult{Observed: domain.ObservedAllowed, Status: 200}

	// A body above the 64 KiB action cap but below the 6 MiB import cap reaches
	// the import service, proving the import-specific limit.
	origins := make([]string, 0, 200)
	for i := 0; i < 200; i++ {
		origins = append(origins, fmt.Sprintf(
			`{"origin":"http://localhost:%d","localStorage":[{"name":"k","value":"%s"}]}`,
			13400+i, strings.Repeat("a", 300)))
	}
	storage := `{"cookies":[],"origins":[` + strings.Join(origins, ",") + `]}`
	body := `{"environmentId":1,"kind":"storage_state","storageState":` + storage + `}`
	if len(body) <= 64<<10 || len(body) >= 6<<20 {
		t.Fatalf("fixture body size %d outside the expected 64KiB..6MiB window", len(body))
	}
	rr := sessionRequest(mux, http.MethodPost, "/api/resources/accesses/1/sessions", catalogAdminToken, body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 for a body above 64 KiB (body=%s)", rr.Code, rr.Body.String())
	}

	oversized := `{"environmentId":1,"kind":"storage_state","storageState":{"cookies":[],"origins":[]},"note":"` +
		strings.Repeat("a", 6<<20) + `"}`
	rr = sessionRequest(mux, http.MethodPost, "/api/resources/accesses/1/sessions", catalogAdminToken, oversized)
	assertAccessErrorEnvelope(t, rr, http.StatusUnprocessableEntity, "invalid_argument", "body_too_large")
	if len(env.sessions.sessions) != 1 {
		t.Fatalf("oversized import created a session: %d stored", len(env.sessions.sessions))
	}
}

func TestSessionHandlerActions(t *testing.T) {
	mux, env := newSessionHandlerTestServer(t)
	env.checker.result = domain.LoginCheckResult{Observed: domain.ObservedAllowed, Status: 200}
	pending := env.seedSession(domain.StatePending)
	manual := env.seedSession(domain.StateNeedsManual)
	valid := env.seedSession(domain.StateValid)

	rr := sessionRequest(mux, http.MethodPost,
		fmt.Sprintf("/api/resources/sessions/%d/validate", pending.ID), catalogAdminToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("validate status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	validated := decodeAccessData[map[string]any](t, rr)
	if validated["state"] != domain.StateValid || validated["lastValidatedAt"] == nil {
		t.Fatalf("validate data=%v", validated)
	}
	if _, ok := validated["latestSnapshot"]; !ok {
		t.Fatal("validate response has no latestSnapshot")
	}

	rr = sessionRequest(mux, http.MethodPost,
		fmt.Sprintf("/api/resources/sessions/%d/confirm", manual.ID), catalogAdminToken,
		`{"note":"manual confirmation"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("confirm status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	confirmed := decodeAccessData[map[string]any](t, rr)
	if confirmed["state"] != domain.StateValid {
		t.Fatalf("confirm data=%v", confirmed)
	}
	if sessionJSONMap(t, confirmed, "latestSnapshot")["degraded"] != true {
		t.Fatalf("confirmed snapshot=%v", confirmed["latestSnapshot"])
	}
	assertNoSessionSecretLeak(t, rr)

	rr = sessionRequest(mux, http.MethodPost,
		fmt.Sprintf("/api/resources/sessions/%d/invalidate", valid.ID), catalogAdminToken,
		`{"reason":"expired cookies"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("invalidate status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	invalidated := decodeAccessData[map[string]any](t, rr)
	if invalidated["state"] != domain.StateInvalid || invalidated["invalidatedReason"] != "expired cookies" {
		t.Fatalf("invalidate data=%v", invalidated)
	}

	rr = sessionRequest(mux, http.MethodPost,
		fmt.Sprintf("/api/resources/sessions/%d/revoke", valid.ID), catalogAdminToken, `{"reason":"leaked"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("revoke status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	revoked := decodeAccessData[map[string]any](t, rr)
	if revoked["state"] != domain.StateRevoked || revoked["invalidatedReason"] != "leaked" {
		t.Fatalf("revoke data=%v", revoked)
	}
	assertNoSessionSecretLeak(t, rr)

	// The snapshot written by validate is readable.
	rr = sessionRequest(mux, http.MethodGet,
		fmt.Sprintf("/api/resources/sessions/%d/snapshots", pending.ID), catalogUserToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("snapshots status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	items, _, hasMore := decodeAccessPage(t, rr)
	if len(items) != 1 || hasMore {
		t.Fatalf("snapshots=%v hasMore=%v", items, hasMore)
	}
	if _, ok := items[0]["permissionHash"]; !ok {
		t.Fatalf("snapshot item=%v", items[0])
	}
	assertNoSessionSecretLeak(t, rr)

	// A state transition that is not allowed is a 409 state conflict.
	rr = sessionRequest(mux, http.MethodPost,
		fmt.Sprintf("/api/resources/sessions/%d/confirm", valid.ID), catalogAdminToken, `{"note":"x"}`)
	assertAccessErrorEnvelope(t, rr, http.StatusConflict, "conflict", "session_state_conflict")

	rr = sessionRequest(mux, http.MethodPost, "/api/resources/sessions/999/validate", catalogAdminToken, "")
	assertAccessErrorEnvelope(t, rr, http.StatusNotFound, "not_found", "not_found")
}

func TestSessionHandlerActionBodyCap(t *testing.T) {
	mux, env := newSessionHandlerTestServer(t)
	env.seedSession(domain.StateNeedsManual)

	oversized := `{"note":"` + strings.Repeat("a", 65<<10) + `"}`
	rr := sessionRequest(mux, http.MethodPost, "/api/resources/sessions/1/confirm", catalogAdminToken, oversized)
	assertAccessErrorEnvelope(t, rr, http.StatusUnprocessableEntity, "invalid_argument", "body_too_large")

	rr = sessionRequest(mux, http.MethodPost, "/api/resources/sessions/1/invalidate", catalogAdminToken, oversized)
	assertAccessErrorEnvelope(t, rr, http.StatusUnprocessableEntity, "invalid_argument", "body_too_large")
}

func TestSessionHandlerDetailAndList(t *testing.T) {
	mux, env := newSessionHandlerTestServer(t)
	withRelations := env.seedSession(domain.StateValid)
	env.seedSnapshot(withRelations.ID, false)
	env.seedLease(withRelations.ID, "worker-1")
	env.seedTakeover(withRelations.ID, domain.TakeoverStatusWaiting)
	second := env.seedSession(domain.StateValid)
	env.seedSession(domain.StateNeedsManual)

	rr := sessionRequest(mux, http.MethodGet,
		fmt.Sprintf("/api/resources/sessions/%d", withRelations.ID), catalogUserToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("detail status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	data := decodeAccessData[map[string]any](t, rr)
	if data["state"] != domain.StateValid || data["id"] != float64(withRelations.ID) {
		t.Fatalf("detail data=%v", data)
	}
	if _, ok := data["storageStateHash"].(string); !ok {
		t.Fatalf("detail has no storageStateHash: %v", data)
	}
	if sessionJSONMap(t, data, "latestSnapshot")["degraded"] != false {
		t.Fatalf("latestSnapshot=%v", data["latestSnapshot"])
	}
	if sessionJSONMap(t, data, "activeLease")["holder"] != "worker-1" {
		t.Fatalf("activeLease=%v", data["activeLease"])
	}
	if sessionJSONMap(t, data, "latestTakeover")["status"] != domain.TakeoverStatusWaiting {
		t.Fatalf("latestTakeover=%v", data["latestTakeover"])
	}
	if _, ok := data["expiring"].(bool); !ok {
		t.Fatalf("expiring=%v want a boolean", data["expiring"])
	}
	assertNoSessionSecretLeak(t, rr)

	rr = sessionRequest(mux, http.MethodGet, fmt.Sprintf("/api/resources/sessions/%d", second.ID), catalogUserToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("detail without relations status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	plain := decodeAccessData[map[string]any](t, rr)
	if _, ok := plain["latestSnapshot"]; ok {
		t.Fatalf("detail invented a snapshot: %v", plain["latestSnapshot"])
	}
	if _, ok := plain["activeLease"]; ok {
		t.Fatalf("detail invented a lease: %v", plain["activeLease"])
	}

	rr = sessionRequest(mux, http.MethodGet, "/api/resources/sessions/999", catalogUserToken, "")
	assertAccessErrorEnvelope(t, rr, http.StatusNotFound, "not_found", "not_found")

	rr = sessionRequest(mux, http.MethodGet, "/api/resources/sessions?limit=2", catalogUserToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("list status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	items, next, hasMore := decodeAccessPage(t, rr)
	if len(items) != 2 || !hasMore || next == "" {
		t.Fatalf("items=%d next=%q hasMore=%v", len(items), next, hasMore)
	}
	assertNoSessionSecretLeak(t, rr)

	rr = sessionRequest(mux, http.MethodGet, "/api/resources/sessions?state=needs_manual", catalogUserToken, "")
	items, _, _ = decodeAccessPage(t, rr)
	if len(items) != 1 || items[0]["state"] != domain.StateNeedsManual {
		t.Fatalf("filtered items=%v", items)
	}

	rr = sessionRequest(mux, http.MethodGet, "/api/resources/sessions?accountId=99", catalogUserToken, "")
	items, _, _ = decodeAccessPage(t, rr)
	if len(items) != 0 {
		t.Fatalf("account filter returned %v", items)
	}

	rr = sessionRequest(mux, http.MethodGet, "/api/resources/sessions?limit=101", catalogUserToken, "")
	assertAccessErrorEnvelope(t, rr, http.StatusUnprocessableEntity, "invalid_argument", "invalid_limit")

	rr = sessionRequest(mux, http.MethodGet, "/api/resources/sessions?accountId=nope", catalogUserToken, "")
	assertAccessErrorEnvelope(t, rr, http.StatusUnprocessableEntity, "invalid_argument", "invalid_account_id")
}

func TestSessionHandlerTakeovers(t *testing.T) {
	mux, env := newSessionHandlerTestServer(t)
	env.checker.result = domain.LoginCheckResult{Observed: domain.ObservedAllowed, Status: 200}
	session := env.seedSession(domain.StateValid)

	rr := sessionRequest(mux, http.MethodPost,
		fmt.Sprintf("/api/resources/sessions/%d/takeovers", session.ID), catalogAdminToken,
		`{"reason":"mfa","instructions":{"text":"enter the one-time code"},"expiresInSeconds":900}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("request takeover status=%d want 201 (body=%s)", rr.Code, rr.Body.String())
	}
	requested := decodeAccessData[map[string]any](t, rr)
	takeover := sessionJSONMap(t, requested, "takeover")
	if takeover["status"] != domain.TakeoverStatusWaiting {
		t.Fatalf("takeover=%v", takeover)
	}
	sessionData := sessionJSONMap(t, requested, "session")
	if sessionData["state"] != domain.StateNeedsManual {
		t.Fatalf("session=%v", sessionData)
	}

	// A session may have at most one waiting takeover.
	rr = sessionRequest(mux, http.MethodPost,
		fmt.Sprintf("/api/resources/sessions/%d/takeovers", session.ID), catalogAdminToken,
		`{"reason":"captcha"}`)
	assertAccessErrorEnvelope(t, rr, http.StatusConflict, "conflict", "takeover_not_waiting")

	// Completing replaces the state, re-runs the check and returns the session.
	takeoverID := takeover["id"].(float64)
	rr = sessionRequest(mux, http.MethodPost,
		fmt.Sprintf("/api/resources/takeovers/%d/complete", int64(takeoverID)), catalogAdminToken,
		`{"kind":"storage_state","storageState":{"cookies":[{"name":"sid","value":"rotated",`+
			`"expires":0}],"origins":[]}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("complete takeover status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	completed := decodeAccessData[map[string]any](t, rr)
	if sessionJSONMap(t, completed, "takeover")["status"] != domain.TakeoverStatusCompleted {
		t.Fatalf("completed takeover=%v", completed["takeover"])
	}
	completedSession := sessionJSONMap(t, completed, "session")
	if completedSession["state"] != domain.StateValid || completedSession["storageStateKind"] != "storage_state" {
		t.Fatalf("completed session=%v", completedSession)
	}
	if _, ok := completedSession["latestSnapshot"]; !ok {
		t.Fatal("completed response has no latestSnapshot")
	}
	assertNoSessionSecretLeak(t, rr, "rotated")

	// A completed takeover cannot be completed or cancelled again.
	rr = sessionRequest(mux, http.MethodPost,
		fmt.Sprintf("/api/resources/takeovers/%d/complete", int64(takeoverID)), catalogAdminToken,
		`{"kind":"storage_state","storageState":{"cookies":[],"origins":[]}}`)
	assertAccessErrorEnvelope(t, rr, http.StatusConflict, "conflict", "takeover_not_waiting")
	rr = sessionRequest(mux, http.MethodPost,
		fmt.Sprintf("/api/resources/takeovers/%d/cancel", int64(takeoverID)), catalogAdminToken, "")
	assertAccessErrorEnvelope(t, rr, http.StatusConflict, "conflict", "takeover_not_waiting")

	// Cancel a fresh waiting takeover; the session stays needs_manual.
	rr = sessionRequest(mux, http.MethodPost,
		fmt.Sprintf("/api/resources/sessions/%d/takeovers", session.ID), catalogAdminToken,
		`{"reason":"approval"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("second request status=%d want 201 (body=%s)", rr.Code, rr.Body.String())
	}
	second := sessionJSONMap(t, decodeAccessData[map[string]any](t, rr), "takeover")
	rr = sessionRequest(mux, http.MethodPost,
		fmt.Sprintf("/api/resources/takeovers/%d/cancel", int64(second["id"].(float64))), catalogAdminToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("cancel status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	cancelled := decodeAccessData[map[string]any](t, rr)
	if cancelled["status"] != domain.TakeoverStatusCancelled {
		t.Fatalf("cancelled=%v", cancelled)
	}
	if env.sessions.sessions[session.ID].State != domain.StateNeedsManual {
		t.Fatalf("cancel changed the session state to %q", env.sessions.sessions[session.ID].State)
	}

	// Missing takeover is a 404.
	rr = sessionRequest(mux, http.MethodPost, "/api/resources/takeovers/999/cancel", catalogAdminToken, "")
	assertAccessErrorEnvelope(t, rr, http.StatusNotFound, "not_found", "not_found")

	// The waiting list is readable by regular users.
	rr = sessionRequest(mux, http.MethodGet, "/api/resources/takeovers?status=waiting", catalogUserToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("list takeovers status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	items, _, hasMore := decodeAccessPage(t, rr)
	if len(items) != 0 || hasMore {
		t.Fatalf("waiting takeovers=%v hasMore=%v", items, hasMore)
	}
}

func TestSessionHandlerWriteRoutesRequireAdmin(t *testing.T) {
	mux, env := newSessionHandlerTestServer(t)
	session := env.seedSession(domain.StateNeedsManual)
	env.seedTakeover(session.ID, domain.TakeoverStatusWaiting)

	writes := []struct{ method, path, body string }{
		{http.MethodPost, "/api/resources/accesses/1/sessions",
			`{"environmentId":1,"kind":"storage_state","storageState":{"cookies":[],"origins":[]}}`},
		{http.MethodPost, fmt.Sprintf("/api/resources/sessions/%d/validate", session.ID), ""},
		{http.MethodPost, fmt.Sprintf("/api/resources/sessions/%d/confirm", session.ID), `{"note":"x"}`},
		{http.MethodPost, fmt.Sprintf("/api/resources/sessions/%d/invalidate", session.ID), `{"reason":"x"}`},
		{http.MethodPost, fmt.Sprintf("/api/resources/sessions/%d/revoke", session.ID), `{"reason":"x"}`},
		{http.MethodPost, fmt.Sprintf("/api/resources/sessions/%d/takeovers", session.ID), `{"reason":"mfa"}`},
		{http.MethodPost, "/api/resources/takeovers/1/complete",
			`{"kind":"storage_state","storageState":{"cookies":[],"origins":[]}}`},
		{http.MethodPost, "/api/resources/takeovers/1/cancel", ""},
	}
	for _, write := range writes {
		t.Run(write.method+" "+write.path, func(t *testing.T) {
			rr := sessionRequest(mux, write.method, write.path, catalogUserToken, write.body)
			assertAccessErrorEnvelope(t, rr, http.StatusForbidden, "forbidden", "")

			rr = sessionRequest(mux, write.method, write.path, "", write.body)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d want 401 without a token", rr.Code)
			}
		})
	}
	if len(env.sessions.takeovers) != 1 {
		t.Fatalf("forbidden takeover writes reached the service: %d takeovers", len(env.sessions.takeovers))
	}

	reads := []string{
		"/api/resources/sessions",
		fmt.Sprintf("/api/resources/sessions/%d", session.ID),
		fmt.Sprintf("/api/resources/sessions/%d/snapshots", session.ID),
		"/api/resources/takeovers",
	}
	for _, path := range reads {
		rr := sessionRequest(mux, http.MethodGet, path, catalogUserToken, "")
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s: status=%d want 200 (body=%s)", path, rr.Code, rr.Body.String())
		}
		assertNoSessionSecretLeak(t, rr)
	}
}
