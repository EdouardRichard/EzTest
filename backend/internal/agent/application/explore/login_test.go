package explore_test

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
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

/* -------------------------------- fakes ---------------------------------- */

type executorCall struct {
	action string
	args   map[string]any
}

// fakeExecutor scripts explore action responses (one queue per action) and
// records every call, so the tests assert on the real interaction sequence.
type fakeExecutor struct {
	mu     sync.Mutex
	queues map[string][]map[string]any
	errs   map[string]error
	calls  []executorCall
}

func newFakeExecutor() *fakeExecutor {
	return &fakeExecutor{queues: map[string][]map[string]any{}, errs: map[string]error{}}
}

func (f *fakeExecutor) queue(action string, results ...map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queues[action] = append(f.queues[action], results...)
}

func (f *fakeExecutor) Action(_ context.Context, action string, args map[string]any) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, executorCall{action: action, args: args})
	if err, ok := f.errs[action]; ok {
		return nil, err
	}
	queue := f.queues[action]
	if len(queue) == 0 {
		return nil, errors.New("fake executor: unexpected action " + action)
	}
	f.queues[action] = queue[1:]
	return queue[0], nil
}

func (f *fakeExecutor) recorded() []executorCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]executorCall(nil), f.calls...)
}

func (f *fakeExecutor) callsOf(action string) []executorCall {
	var out []executorCall
	for _, call := range f.recorded() {
		if call.action == action {
			out = append(out, call)
		}
	}
	return out
}

type fakeAccounts struct {
	username string
	password string
	err      error
	calls    []int64
}

func (f *fakeAccounts) AccountSecret(_ context.Context, accountID int64) (string, string, error) {
	f.calls = append(f.calls, accountID)
	return f.username, f.password, f.err
}

type fakeFacts struct {
	mu    sync.Mutex
	facts []version.Fact
	err   error
}

func (f *fakeFacts) Record(_ context.Context, fact version.Fact) (version.Fact, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return version.Fact{}, false, f.err
	}
	fact.ID = int64(900 + len(f.facts))
	f.facts = append(f.facts, fact)
	return fact, false, nil
}

func (f *fakeFacts) recorded() []version.Fact {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]version.Fact(nil), f.facts...)
}

/* ------------------------------ page fixtures ---------------------------- */

func element(index int, tag, selector, role, name, typ string) map[string]any {
	return map[string]any{
		"index":    index,
		"tag":      tag,
		"selector": selector,
		"role":     role,
		"name":     name,
		"type":     typ,
		"text":     name,
		"value":    "",
		"disabled": false,
	}
}

func loginSnapshot() map[string]any {
	return map[string]any{
		"page_id": "page_1",
		"route":   "/login",
		"dom_snapshot": map[string]any{
			"title": "EzTest Login",
			"aria":  "dialog \"Sign in\"\n  textbox \"Username\"\n  textbox \"Password\"\n  button \"Log in\"",
			"route": "/login",
			"elements": []any{
				element(0, "input", "#username", "textbox", "Username", "text"),
				element(1, "input", "#password", "textbox", "Password", "password"),
				element(2, "button", "#submit", "button", "Log in", ""),
			},
		},
	}
}

func rejectedLoginSnapshot() map[string]any {
	snapshot := loginSnapshot()
	snapshot["dom_snapshot"].(map[string]any)["aria"] = "dialog \"Sign in\"\n  alert \"Invalid credentials\"\n  textbox \"Username\""
	return snapshot
}

func loggedInSnapshot() map[string]any {
	return map[string]any{
		"page_id":           "page_1",
		"route":             "/home",
		"storage_state_ref": "ss_abc123",
		"storage_state": map[string]any{
			"cookies": []any{
				map[string]any{"name": "sid", "value": "session-token", "domain": "127.0.0.1", "path": "/"},
			},
			"origins": []any{
				map[string]any{
					"origin":         "http://127.0.0.1:13335",
					"localStorage":   []any{map[string]any{"name": "token", "value": "session-token"}},
					"sessionStorage": []any{map[string]any{"name": "flag", "value": "logged-in"}},
				},
			},
		},
		"dom_snapshot": map[string]any{
			"title": "EzTest Home",
			"aria":  "heading \"Home\"\n  button \"Logout\"",
			"route": "/home",
			"elements": []any{
				element(0, "button", "#logout", "button", "Logout", ""),
			},
		},
	}
}

/* -------------------------------- helpers -------------------------------- */

func newFlow(t *testing.T, exec *fakeExecutor, accounts *fakeAccounts, sessions exploreapp.SessionStore, facts *fakeFacts) *exploreapp.LoginFlow {
	t.Helper()
	return exploreapp.NewLoginFlow(
		exploreapp.LoginFlowConfig{
			VersionID:     10,
			VersionSUTID:  7,
			EnvironmentID: 8,
			Role:          "admin",
			LoginURL:      "http://127.0.0.1:13335/login",
		},
		exploreapp.LoginFlowDeps{
			Executor: exec,
			Accounts: accounts,
			Sessions: sessions,
			Facts:    facts,
		},
	)
}

func detect(t *testing.T, flow *exploreapp.LoginFlow) exploreapp.LoginForm {
	t.Helper()
	form, err := flow.Detect(context.Background(), exploreapp.Page{
		ID:  "page_1",
		URL: "http://127.0.0.1:13335/login",
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	return form
}

/* --------------------------------- tests --------------------------------- */

func TestLoginFlowDetectAndExecute(t *testing.T) {
	exec := newFakeExecutor()
	exec.queue("explore.snapshot", loginSnapshot())
	exec.queue("explore.fill", map[string]any{})
	exec.queue("explore.fill", map[string]any{})
	exec.queue("explore.click", map[string]any{})
	exec.queue("explore.snapshot", loggedInSnapshot())
	accounts := &fakeAccounts{username: "alice", password: "s3cret-pw"}
	sessions := resourceapp.NewSessionStore(30 * time.Minute)
	facts := &fakeFacts{}
	flow := newFlow(t, exec, accounts, sessions, facts)

	form := detect(t, flow)
	if form.Mode != "modal" {
		t.Fatalf("form mode = %q, want modal (the login is a dialog)", form.Mode)
	}
	if form.UsernameSelector != "#username" || form.PasswordSelector != "#password" || form.SubmitSelector != "#submit" {
		t.Fatalf("form selectors = %+v", form)
	}

	session, err := flow.Execute(context.Background(), exploreapp.Account{ID: 81, Role: "admin"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if session.Status != exploreapp.SessionActive {
		t.Fatalf("session status = %q, want active (%+v)", session.Status, session)
	}
	if session.Reused {
		t.Fatal("a fresh login must not be reported as a reused session")
	}
	if session.StorageStateRef != "ss_abc123" {
		t.Fatalf("storage_state_ref = %q", session.StorageStateRef)
	}
	if session.FactID == 0 {
		t.Fatal("successful login must record a login_session fact")
	}
	if session.AccountRole != "admin" || session.VersionID != 10 || session.VersionSUTID != 7 {
		t.Fatalf("session binding = %+v", session)
	}

	fills := exec.callsOf("explore.fill")
	if len(fills) != 2 {
		t.Fatalf("fill calls = %d, want 2", len(fills))
	}
	if fills[0].args["selector"] != "#username" || fills[0].args["value"] != "alice" {
		t.Fatalf("username fill args = %+v", fills[0].args)
	}
	if fills[1].args["selector"] != "#password" || fills[1].args["value"] != "s3cret-pw" {
		t.Fatalf("password fill args = %+v", fills[1].args)
	}
	if fills[1].args["secret"] != true {
		t.Fatalf("password fill must be secret, args = %+v", fills[1].args)
	}
	clicks := exec.callsOf("explore.click")
	if len(clicks) != 1 || clicks[0].args["selector"] != "#submit" {
		t.Fatalf("click calls = %+v", clicks)
	}
	snapshots := exec.callsOf("explore.snapshot")
	last := snapshots[len(snapshots)-1]
	if last.args["include_storage_state"] != true {
		t.Fatalf("final snapshot must request storage state, args = %+v", last.args)
	}

	if len(accounts.calls) != 1 || accounts.calls[0] != 81 {
		t.Fatalf("account store calls = %v, want [81]", accounts.calls)
	}

	stored, ok := sessions.Find(context.Background(), 7, 8, "admin")
	if !ok {
		t.Fatal("successful login must save the storage state for reuse")
	}
	if stored.StorageStateRef != "ss_abc123" || len(stored.StorageState) == 0 {
		t.Fatalf("stored session = %+v", stored)
	}

	recorded := facts.recorded()
	if len(recorded) != 1 {
		t.Fatalf("recorded facts = %d, want 1", len(recorded))
	}
	fact := recorded[0]
	if fact.Kind != exploreapp.KindLoginSession {
		t.Fatalf("fact kind = %q, want %q", fact.Kind, exploreapp.KindLoginSession)
	}
	if fact.VersionID != 10 || fact.Source != "explorer" {
		t.Fatalf("fact identity = %+v", fact)
	}
	if fact.Payload["storage_state_ref"] != "ss_abc123" {
		t.Fatalf("fact payload storage_state_ref = %v", fact.Payload["storage_state_ref"])
	}
}

func TestLoginFlowReusesStorageStateWithoutLoggingIn(t *testing.T) {
	exec := newFakeExecutor()
	accounts := &fakeAccounts{username: "alice", password: "s3cret-pw"}
	sessions := resourceapp.NewSessionStore(time.Hour)
	if err := sessions.Save(context.Background(), resourceapp.LoginSession{
		VersionID:       10,
		VersionSUTID:    7,
		EnvironmentID:   8,
		AccountRole:     "admin",
		StorageStateRef: "ss_reused",
		StorageState:    json.RawMessage(`{"cookies":[{"name":"sid","value":"t"}],"origins":[]}`),
		VerifiedAt:      time.Now(),
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	facts := &fakeFacts{}
	flow := newFlow(t, exec, accounts, sessions, facts)

	session, err := flow.Execute(context.Background(), exploreapp.Account{ID: 81, Role: "admin"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !session.Reused || session.Status != exploreapp.SessionActive {
		t.Fatalf("session = %+v, want active reused", session)
	}
	if session.StorageStateRef != "ss_reused" {
		t.Fatalf("storage_state_ref = %q", session.StorageStateRef)
	}
	if calls := exec.recorded(); len(calls) != 0 {
		t.Fatalf("reusing a stored session must not touch the executor, calls = %+v", calls)
	}
	if len(accounts.calls) != 0 {
		t.Fatalf("reusing a stored session must not read credentials, calls = %v", accounts.calls)
	}
	if len(facts.recorded()) != 1 {
		t.Fatalf("reuse must still record a login_session fact, got %d", len(facts.recorded()))
	}
}

func TestLoginFlowWrongPasswordBlockedWithAction(t *testing.T) {
	exec := newFakeExecutor()
	exec.queue("explore.snapshot", loginSnapshot())
	exec.queue("explore.fill", map[string]any{})
	exec.queue("explore.fill", map[string]any{})
	exec.queue("explore.click", map[string]any{})
	exec.queue("explore.snapshot", rejectedLoginSnapshot())
	accounts := &fakeAccounts{username: "alice", password: "wrong-pw"}
	sessions := resourceapp.NewSessionStore(time.Hour)
	facts := &fakeFacts{}
	flow := newFlow(t, exec, accounts, sessions, facts)
	detect(t, flow)

	session, err := flow.Execute(context.Background(), exploreapp.Account{ID: 81, Role: "admin"})
	if err != nil {
		t.Fatalf("a rejected login is a blocked outcome, not an error: %v", err)
	}
	if session.Status != exploreapp.SessionBlocked {
		t.Fatalf("session status = %q, want blocked", session.Status)
	}
	if session.Action != exploreapp.ActionFixCredentials {
		t.Fatalf("blocked action = %q, want %q", session.Action, exploreapp.ActionFixCredentials)
	}
	if session.Reason != "invalid_credentials" {
		t.Fatalf("blocked reason = %q", session.Reason)
	}
	if session.StorageStateRef != "" {
		t.Fatalf("blocked login must not produce a storage state, got %q", session.StorageStateRef)
	}
	if len(facts.recorded()) != 0 {
		t.Fatalf("blocked login must not record a login_session fact")
	}
	if _, ok := sessions.Find(context.Background(), 7, 8, "admin"); ok {
		t.Fatal("blocked login must invalidate the cached session")
	}
}

func TestLoginDetectWithoutFormIsBlockedWithAction(t *testing.T) {
	exec := newFakeExecutor()
	exec.queue("explore.snapshot", map[string]any{
		"page_id": "page_1",
		"route":   "/home",
		"dom_snapshot": map[string]any{
			"title":    "Home",
			"aria":     "heading \"Home\"",
			"route":    "/home",
			"elements": []any{element(0, "a", "#next", "link", "Next", "")},
		},
	})
	flow := newFlow(t, exec, &fakeAccounts{}, resourceapp.NewSessionStore(time.Hour), &fakeFacts{})

	_, err := flow.Detect(context.Background(), exploreapp.Page{ID: "page_1", URL: "http://127.0.0.1:13335/home"})
	if err == nil {
		t.Fatal("Detect without a login form must fail")
	}
	var blocked *exploreapp.BlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("Detect error = %T, want *BlockedError", err)
	}
	if blocked.Action != exploreapp.ActionManualLogin {
		t.Fatalf("blocked action = %q, want %q", blocked.Action, exploreapp.ActionManualLogin)
	}
	if blocked.Reason != "login_form_not_found" {
		t.Fatalf("blocked reason = %q", blocked.Reason)
	}
}

func TestLoginCredentialsNeverEnterFactsOrRecipes(t *testing.T) {
	exec := newFakeExecutor()
	exec.queue("explore.snapshot", loginSnapshot())
	exec.queue("explore.fill", map[string]any{})
	exec.queue("explore.fill", map[string]any{})
	exec.queue("explore.click", map[string]any{})
	exec.queue("explore.snapshot", loggedInSnapshot())
	const username = "alice"
	const password = "s3cret-pw"
	sessions := resourceapp.NewSessionStore(time.Hour)
	facts := &fakeFacts{}
	flow := newFlow(t, exec, &fakeAccounts{username: username, password: password}, sessions, facts)
	detect(t, flow)
	if _, err := flow.Execute(context.Background(), exploreapp.Account{ID: 81, Role: "admin"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	recorded := facts.recorded()
	if len(recorded) != 1 {
		t.Fatalf("recorded facts = %d, want 1", len(recorded))
	}
	encoded, err := json.Marshal(recorded[0])
	if err != nil {
		t.Fatalf("marshal fact: %v", err)
	}
	for _, secret := range []string{username, password} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("version fact leaked credential %q: %s", secret, encoded)
		}
	}

	// The recipe carried by the fact is what a knowledge hook would sediment:
	// it must describe selectors only, never a filled value.
	recipe, ok := recorded[0].Payload["recipe"].(map[string]any)
	if !ok {
		t.Fatalf("fact payload has no structured recipe: %v", recorded[0].Payload)
	}
	encodedRecipe, err := json.Marshal(recipe)
	if err != nil {
		t.Fatalf("marshal recipe: %v", err)
	}
	for _, secret := range []string{username, password} {
		if strings.Contains(string(encodedRecipe), secret) {
			t.Fatalf("knowledge recipe leaked credential %q: %s", secret, encodedRecipe)
		}
	}

	// The only place a credential may travel is the executor fill, and the
	// password fill must be flagged secret so outputs are redacted.
	passwordFill := exec.callsOf("explore.fill")[1]
	if passwordFill.args["secret"] != true {
		t.Fatalf("password fill must carry secret=true, args = %+v", passwordFill.args)
	}
}

func TestLoginHTTPExecutorTargetsExploreActionsRoute(t *testing.T) {
	var (
		gotPath  string
		gotAuth  string
		gotBody  map[string]any
		response = `{"ok":true,"result":{"page_id":"page_9","route":"/login"}}`
		status   = http.StatusOK
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	defer server.Close()

	exec := exploreapp.NewHTTPExecutor(server.URL+"/", "executor-token", time.Second)
	result, err := exec.Action(context.Background(), "explore.snapshot", map[string]any{"page_id": "page_9"})
	if err != nil {
		t.Fatalf("Action: %v", err)
	}
	if exploreapp.ExploreActionsPath != "/v1/explore/actions" {
		t.Fatalf("ExploreActionsPath = %q", exploreapp.ExploreActionsPath)
	}
	if gotPath != exploreapp.ExploreActionsPath {
		t.Fatalf("explore action path = %q, want %q", gotPath, exploreapp.ExploreActionsPath)
	}
	if gotAuth != "Bearer executor-token" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if gotBody["action"] != "explore.snapshot" {
		t.Fatalf("action body = %v", gotBody)
	}
	args, ok := gotBody["args"].(map[string]any)
	if !ok || args["page_id"] != "page_9" {
		t.Fatalf("action args = %v", gotBody["args"])
	}
	if result["page_id"] != "page_9" {
		t.Fatalf("result = %v", result)
	}

	// A typed action failure is surfaced as an ExecutorActionError, not as a
	// transport error, so the login flow can turn it into a blocked outcome.
	status = http.StatusBadRequest
	response = `{"ok":false,"error":{"code":"invalid_arguments","message":"unknown page"}}`
	_, err = exec.Action(context.Background(), "explore.snapshot", map[string]any{"page_id": "missing"})
	var actionErr *exploreapp.ExecutorActionError
	if !errors.As(err, &actionErr) {
		t.Fatalf("Action error = %T, want *ExecutorActionError", err)
	}
	if actionErr.Code != "invalid_arguments" {
		t.Fatalf("action error code = %q", actionErr.Code)
	}
}
