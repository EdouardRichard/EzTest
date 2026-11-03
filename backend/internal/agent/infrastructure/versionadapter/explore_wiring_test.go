package versionadapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	exploreapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/explore"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/versionadapter"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

/* ------------------------------- fixtures -------------------------------- */

type wiringVersions struct {
	view  versionapp.VersionView
	err   error
	calls []int64
}

func (v *wiringVersions) Get(_ context.Context, id int64) (versionapp.VersionView, error) {
	v.calls = append(v.calls, id)
	return v.view, v.err
}

type wiringAccounts struct {
	username string
	password string
	calls    []int64
}

func (a *wiringAccounts) GetAccountSecret(_ context.Context, accountID int64) (string, string, error) {
	a.calls = append(a.calls, accountID)
	return a.username, a.password, nil
}

type wiringFacts struct {
	mu    sync.Mutex
	facts []version.Fact
}

func (f *wiringFacts) Record(_ context.Context, fact version.Fact) (version.Fact, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fact.ID = int64(900 + len(f.facts))
	f.facts = append(f.facts, fact)
	return fact, false, nil
}

func (f *wiringFacts) recorded() []version.Fact {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]version.Fact(nil), f.facts...)
}

// actionOnlyExecutor implements the action port but not the page-cleanup
// capability, mimicking a stale executor adapter.
type actionOnlyExecutor struct{}

func (actionOnlyExecutor) Action(context.Context, string, map[string]any) (map[string]any, error) {
	return nil, nil
}

type scriptedReply struct {
	result  map[string]any
	errCode string
	errMsg  string
}

type exploreServerCall struct {
	method string
	path   string
	action string
	args   map[string]any
	auth   string
}

// wiringExecutorServer is the fake executor HTTP endpoint: it scripts replies per
// explore action and records every request, so the wiring test asserts the real
// HTTP client path plus the action sequence. Close requests are split by scope:
// a page_id query closes one page, no query keeps the close-all meaning.
type wiringExecutorServer struct {
	mu            sync.Mutex
	replies       map[string][]scriptedReply
	calls         []exploreServerCall
	closes        int
	closeAll      int
	closedPageIDs []string
}

func newWiringExecutorServer() *wiringExecutorServer {
	return &wiringExecutorServer{replies: map[string][]scriptedReply{}}
}

func (s *wiringExecutorServer) queue(action string, results ...map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, result := range results {
		s.replies[action] = append(s.replies[action], scriptedReply{result: result})
	}
}

func (s *wiringExecutorServer) queueError(action, code, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies[action] = append(s.replies[action], scriptedReply{errCode: code, errMsg: message})
}

func (s *wiringExecutorServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("content-type", "application/json")
	if r.Method == http.MethodDelete && r.URL.Path == exploreapp.ExplorePagesPath {
		s.mu.Lock()
		s.closes++
		if pageID := r.URL.Query().Get("page_id"); pageID != "" {
			s.closedPageIDs = append(s.closedPageIDs, pageID)
		} else {
			s.closeAll++
		}
		s.calls = append(s.calls, exploreServerCall{method: r.Method, path: r.URL.Path, auth: r.Header.Get("authorization")})
		s.mu.Unlock()
		_, _ = io.WriteString(w, `{"ok":true,"result":{"closed":true}}`)
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != exploreapp.ExploreActionsPath {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Action string         `json:"action"`
		Args   map[string]any `json:"args"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	s.calls = append(s.calls, exploreServerCall{
		method: r.Method, path: r.URL.Path, action: body.Action,
		args: body.Args, auth: r.Header.Get("authorization"),
	})
	queue := s.replies[body.Action]
	if len(queue) > 0 {
		s.replies[body.Action] = queue[1:]
	}
	s.mu.Unlock()
	if len(queue) == 0 {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"ok":false,"error":{"code":"internal","message":"no scripted reply"}}`)
		return
	}
	reply := queue[0]
	if reply.errCode != "" {
		w.WriteHeader(http.StatusBadRequest)
		payload, _ := json.Marshal(map[string]any{
			"ok":    false,
			"error": map[string]any{"code": reply.errCode, "message": reply.errMsg},
		})
		_, _ = w.Write(payload)
		return
	}
	payload, _ := json.Marshal(map[string]any{"ok": true, "result": reply.result})
	_, _ = w.Write(payload)
}

func (s *wiringExecutorServer) callsOf(action string) []exploreServerCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]exploreServerCall, 0)
	for _, call := range s.calls {
		if call.action == action {
			out = append(out, call)
		}
	}
	return out
}

type wiringFixture struct {
	server   *wiringExecutorServer
	wiring   *versionadapter.ExploreWiring
	sessions *resourceapp.SessionStore
	facts    *wiringFacts
	accounts *wiringAccounts
	versions *wiringVersions
}

func newWiringFixture(t *testing.T) *wiringFixture {
	t.Helper()
	server := newWiringExecutorServer()
	endpoint := httptest.NewServer(server)
	t.Cleanup(endpoint.Close)
	accounts := &wiringAccounts{username: "alice", password: "s3cret-pw"}
	versions := &wiringVersions{view: versionapp.VersionView{
		ID: 10,
		Bindings: []versionapp.BindingView{{
			ID:       7,
			Accounts: []resourceapp.AccountView{{ID: "81", Roles: []string{"admin"}}},
		}},
	}}
	sessions := resourceapp.NewSessionStore(time.Hour)
	facts := &wiringFacts{}
	wiring, err := versionadapter.NewExploreWiring(versionadapter.ExploreWiringDeps{
		Executor: exploreapp.NewHTTPExecutor(endpoint.URL, "wiring-token", time.Second),
		Accounts: accounts,
		Sessions: sessions,
		Facts:    facts,
		Versions: versions,
	})
	if err != nil {
		t.Fatalf("NewExploreWiring: %v", err)
	}
	return &wiringFixture{server: server, wiring: wiring, sessions: sessions, facts: facts, accounts: accounts, versions: versions}
}

func wiringConfig() exploreapp.LoginFlowConfig {
	return exploreapp.LoginFlowConfig{
		VersionID:     10,
		VersionSUTID:  7,
		EnvironmentID: 8,
		Role:          "admin",
		LoginURL:      "http://127.0.0.1:13335/login",
	}
}

func wiringElement(index int, tag, selector, role, name, typ string) map[string]any {
	return map[string]any{
		"index": index, "tag": tag, "selector": selector, "role": role,
		"name": name, "type": typ, "text": name, "value": "", "disabled": false, "visible": true,
	}
}

func wiringLoginSnapshot() map[string]any {
	return map[string]any{
		"page_id": "page_1",
		"url":     "http://127.0.0.1:13335/login",
		"route":   "/login",
		"dom_snapshot": map[string]any{
			"url":   "http://127.0.0.1:13335/login",
			"title": "EzTest Login",
			"aria":  "dialog \"Sign in\"\n  textbox \"Username\"\n  textbox \"Password\"\n  button \"Log in\"",
			"route": "/login",
			"elements": []any{
				wiringElement(0, "input", "#username", "textbox", "Username", "text"),
				wiringElement(1, "input", "#password", "textbox", "Password", "password"),
				wiringElement(2, "button", "#submit", "button", "Log in", ""),
			},
		},
	}
}

func wiringLoggedInSnapshot() map[string]any {
	return map[string]any{
		"page_id":           "page_verify",
		"url":               "http://127.0.0.1:13335/home",
		"route":             "/home",
		"storage_state_ref": "ss_abc123",
		"storage_state": map[string]any{
			"cookies": []any{map[string]any{"name": "sid", "value": "session-token", "domain": "127.0.0.1", "path": "/"}},
			"origins": []any{},
		},
		"dom_snapshot": map[string]any{
			"url":   "http://127.0.0.1:13335/home",
			"title": "EzTest Home",
			"aria":  "heading \"Home\"\n  button \"Logout\"",
			"route": "/home",
			"elements": []any{
				wiringElement(0, "button", "#logout", "button", "Logout", ""),
			},
		},
	}
}

func wiringTargetSnapshot(pageID, url, route string) map[string]any {
	return map[string]any{
		"page_id": pageID,
		"url":     url,
		"route":   route,
		"dom_snapshot": map[string]any{
			"url":   url,
			"title": "Orders",
			"aria":  "heading \"Orders\"",
			"route": route,
			"elements": []any{
				wiringElement(0, "button", "#logout", "button", "Logout", ""),
			},
		},
	}
}

func seedWiringSession(t *testing.T, sessions *resourceapp.SessionStore, ref, loginURL string) {
	t.Helper()
	if err := sessions.Save(context.Background(), resourceapp.LoginSession{
		VersionID:       10,
		VersionSUTID:    7,
		EnvironmentID:   8,
		AccountRole:     "admin",
		StorageStateRef: ref,
		StorageState:    json.RawMessage(`{"cookies":[{"name":"sid","value":"seeded"}],"origins":[]}`),
		LoginURL:        loginURL,
		Route:           "/login",
		VerifiedAt:      time.Now(),
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
}

/* --------------------------------- tests --------------------------------- */

// TestExploreWiringRequiresPageCloser fails closed: the production wiring must
// not accept an executor that cannot close the throwaway reuse-verification
// page, otherwise repeated logins can exhaust the executor's 8-page cap.
func TestExploreWiringRequiresPageCloser(t *testing.T) {
	_, err := versionadapter.NewExploreWiring(versionadapter.ExploreWiringDeps{
		Executor: actionOnlyExecutor{},
		Accounts: &wiringAccounts{},
		Sessions: resourceapp.NewSessionStore(time.Hour),
		Facts:    &wiringFacts{},
		Versions: &wiringVersions{},
	})
	if err == nil {
		t.Fatal("wiring must reject an executor that cannot close explore pages")
	}
}

// TestExploreWiringResolveAccountDedupesRoleEntries covers a binding where the
// same account is listed more than once (duplicate role entries or a repeated
// account row). Resolution must dedupe by account, not count entries.
func TestExploreWiringResolveAccountDedupesRoleEntries(t *testing.T) {
	fixture := newWiringFixture(t)
	fixture.versions.view = versionapp.VersionView{
		ID: 10,
		Bindings: []versionapp.BindingView{{
			ID: 7,
			Accounts: []resourceapp.AccountView{
				{ID: "81", Roles: []string{"admin", "admin"}},
				{ID: "81", Roles: []string{"admin"}},
				{ID: "82", Roles: []string{"viewer"}},
			},
		}},
	}
	account, err := fixture.wiring.ResolveAccount(context.Background(), 10, 7, "admin")
	if err != nil {
		t.Fatalf("ResolveAccount: %v (duplicate entries for the same account must not conflict)", err)
	}
	if account.ID != 81 || account.Role != "admin" {
		t.Fatalf("account = %+v, want account 81 with role admin", account)
	}
}

// TestExploreWiringResolveAccountNotFound fails closed when no account is bound
// for the requested role.
func TestExploreWiringResolveAccountNotFound(t *testing.T) {
	fixture := newWiringFixture(t)
	_, err := fixture.wiring.ResolveAccount(context.Background(), 10, 7, "qa")
	var platformErr *platform.Error
	if !errors.As(err, &platformErr) {
		t.Fatalf("ResolveAccount error = %T, want *platform.Error", err)
	}
	if platformErr.Code != "not_found" {
		t.Fatalf("error code = %q, want not_found", platformErr.Code)
	}
}

// TestExploreWiringResolveAccountConflict reports a state conflict when two
// different accounts are bound for the requested role.
func TestExploreWiringResolveAccountConflict(t *testing.T) {
	fixture := newWiringFixture(t)
	fixture.versions.view = versionapp.VersionView{
		ID: 10,
		Bindings: []versionapp.BindingView{{
			ID: 7,
			Accounts: []resourceapp.AccountView{
				{ID: "81", Roles: []string{"admin"}},
				{ID: "82", Roles: []string{"admin"}},
			},
		}},
	}
	_, err := fixture.wiring.ResolveAccount(context.Background(), 10, 7, "admin")
	var platformErr *platform.Error
	if !errors.As(err, &platformErr) {
		t.Fatalf("ResolveAccount error = %T, want *platform.Error", err)
	}
	if platformErr.Code != "state_conflict" {
		t.Fatalf("error code = %q, want state_conflict", platformErr.Code)
	}
}

// TestExploreWiringLoginFlow wires the real account store, session store, fact
// recorder and HTTP executor: a fresh login resolves the bound account, fills
// and submits through the executor, records one login fact and caches the state.
func TestExploreWiringLoginFlow(t *testing.T) {
	fixture := newWiringFixture(t)
	fixture.server.queue("explore.open", wiringLoginSnapshot())
	fixture.server.queue("explore.fill", map[string]any{})
	fixture.server.queue("explore.fill", map[string]any{})
	fixture.server.queue("explore.click", map[string]any{})
	fixture.server.queue("explore.snapshot", wiringLoggedInSnapshot())

	fact, err := fixture.wiring.Login(context.Background(), wiringConfig())
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if fact.Status != exploreapp.SessionActive || fact.Reused {
		t.Fatalf("fact = %+v, want a fresh active login", fact)
	}
	if fact.StorageStateRef != "ss_abc123" || fact.FactID == 0 {
		t.Fatalf("fact = %+v, want the captured ref and a recorded fact", fact)
	}
	if len(fixture.accounts.calls) != 1 || fixture.accounts.calls[0] != 81 {
		t.Fatalf("account store calls = %v, want [81]", fixture.accounts.calls)
	}
	if len(fixture.versions.calls) != 1 || fixture.versions.calls[0] != 10 {
		t.Fatalf("version binding reads = %v, want [10]", fixture.versions.calls)
	}
	stored, ok := fixture.sessions.Find(context.Background(), 10, 7, 8, "admin")
	if !ok || stored.StorageStateRef != "ss_abc123" {
		t.Fatalf("stored session = %+v (ok=%v)", stored, ok)
	}
	facts := fixture.facts.recorded()
	if len(facts) != 1 || facts[0].Kind != exploreapp.KindLoginSession {
		t.Fatalf("facts = %+v, want one login_session fact", facts)
	}
	encoded, err := json.Marshal(facts)
	if err != nil {
		t.Fatalf("marshal facts: %v", err)
	}
	for _, secret := range []string{"alice", "s3cret-pw"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("login fact leaked credential %q: %s", secret, encoded)
		}
	}

	fills := fixture.server.callsOf("explore.fill")
	if len(fills) != 2 || fills[1].args["secret"] != true {
		t.Fatalf("fill calls = %+v, want the password fill flagged secret", fills)
	}
	if fills[1].args["value"] != "s3cret-pw" {
		t.Fatalf("password fill value = %v", fills[1].args["value"])
	}
	opens := fixture.server.callsOf("explore.open")
	if len(opens) != 1 || opens[0].args["url"] != "http://127.0.0.1:13335/login" {
		t.Fatalf("open calls = %+v, want the configured login URL", opens)
	}
	if opens[0].auth != "Bearer wiring-token" {
		t.Fatalf("executor authorization = %q", opens[0].auth)
	}
	if fixture.server.closes != 0 {
		t.Fatalf("a fresh login page must not be closed, close calls = %d", fixture.server.closes)
	}
}

// TestExploreWiringClosesPages proves the throwaway page opened to verify a
// cached storage ref is closed through DELETE /v1/explore?page_id=<id>, so
// repeated reuse cannot exhaust the executor's 8-page cap and concurrent pages
// on the shared executor runtime are left open.
func TestExploreWiringClosesPages(t *testing.T) {
	fixture := newWiringFixture(t)
	seedWiringSession(t, fixture.sessions, "ss_reused", "http://127.0.0.1:13335/login")
	fixture.server.queue("explore.open", wiringLoggedInSnapshot())

	fact, err := fixture.wiring.Login(context.Background(), wiringConfig())
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !fact.Reused || fact.Status != exploreapp.SessionActive {
		t.Fatalf("fact = %+v, want an active reused session", fact)
	}
	if len(fixture.accounts.calls) != 0 {
		t.Fatalf("reuse must not read credentials, calls = %v", fixture.accounts.calls)
	}
	if fixture.server.closes != 1 {
		t.Fatalf("reuse verification page close calls = %d, want 1", fixture.server.closes)
	}
	if fixture.server.closeAll != 0 {
		t.Fatalf("reuse must not close every page, close-all calls = %d", fixture.server.closeAll)
	}
	if closed := fixture.server.closedPageIDs; len(closed) != 1 || closed[0] != "page_verify" {
		t.Fatalf("closed page ids = %v, want only the opened verification page page_verify", closed)
	}
	if len(fixture.server.callsOf("explore.fill")) != 0 {
		t.Fatalf("reuse must not drive the login form, calls = %+v", fixture.server.callsOf("explore.fill"))
	}
}

// TestExploreWiringUnknownRefDirect covers the direct-ref caller path: a target
// open with a cached ref fails as unknown after an executor restart, so the
// wiring invalidates the entry, logs in again and serves the target URL from
// the freshly authenticated page.
func TestExploreWiringUnknownRefDirect(t *testing.T) {
	const targetURL = "http://127.0.0.1:13335/orders"
	fixture := newWiringFixture(t)
	seedWiringSession(t, fixture.sessions, "ss_stale", "http://127.0.0.1:13335/login")
	fixture.server.queueError("explore.open", "invalid_arguments", "unknown storage_state_ref: ss_stale")
	fixture.server.queue("explore.open", wiringLoginSnapshot())
	fixture.server.queue("explore.fill", map[string]any{})
	fixture.server.queue("explore.fill", map[string]any{})
	fixture.server.queue("explore.click", map[string]any{})
	fixture.server.queue("explore.snapshot", wiringLoggedInSnapshot())
	fixture.server.queue("explore.open", wiringTargetSnapshot("page_1", targetURL, "/orders"))

	page, fact, err := fixture.wiring.OpenWithSession(context.Background(), wiringConfig(), targetURL)
	if err != nil {
		t.Fatalf("OpenWithSession: %v", err)
	}
	if fact.Status != exploreapp.SessionActive || fact.Reused {
		t.Fatalf("fact = %+v, want a fresh active login", fact)
	}
	if page.ID != "page_1" || page.URL != targetURL {
		t.Fatalf("page = %+v, want the target URL on the freshly authenticated page", page)
	}
	if len(fixture.accounts.calls) != 1 || fixture.accounts.calls[0] != 81 {
		t.Fatalf("account store calls = %v, want [81]", fixture.accounts.calls)
	}
	stored, ok := fixture.sessions.Find(context.Background(), 10, 7, 8, "admin")
	if !ok || stored.StorageStateRef != "ss_abc123" {
		t.Fatalf("stale session was not replaced: %+v (ok=%v)", stored, ok)
	}
	opens := fixture.server.callsOf("explore.open")
	if len(opens) != 3 {
		t.Fatalf("open calls = %+v, want stale direct open, fresh login open, target navigation", opens)
	}
	if opens[0].args["url"] != targetURL || opens[0].args["storage_state_ref"] != "ss_stale" {
		t.Fatalf("stale direct open args = %+v", opens[0].args)
	}
	if _, present := opens[1].args["storage_state_ref"]; present {
		t.Fatalf("fresh login open args = %+v, must not reuse the stale ref", opens[1].args)
	}
	if opens[2].args["page_id"] != "page_1" || opens[2].args["url"] != targetURL {
		t.Fatalf("target navigation args = %+v, want the fresh login page", opens[2].args)
	}
	if len(fixture.facts.recorded()) != 1 {
		t.Fatalf("a fresh login must record one login fact, got %d", len(fixture.facts.recorded()))
	}
}
