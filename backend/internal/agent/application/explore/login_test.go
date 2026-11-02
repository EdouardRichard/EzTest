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

// fakeReply is one scripted executor response: a successful result or an error.
type fakeReply struct {
	result map[string]any
	err    error
}

// fakeExecutor scripts explore action responses (one queue per action) and
// records every call, so the tests assert on the real interaction sequence.
type fakeExecutor struct {
	mu     sync.Mutex
	queues map[string][]fakeReply
	calls  []executorCall
}

func newFakeExecutor() *fakeExecutor {
	return &fakeExecutor{queues: map[string][]fakeReply{}}
}

func (f *fakeExecutor) queue(action string, results ...map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, result := range results {
		f.queues[action] = append(f.queues[action], fakeReply{result: result})
	}
}

func (f *fakeExecutor) queueError(action string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queues[action] = append(f.queues[action], fakeReply{err: err})
}

func (f *fakeExecutor) Action(_ context.Context, action string, args map[string]any) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, executorCall{action: action, args: args})
	queue := f.queues[action]
	if len(queue) == 0 {
		return nil, errors.New("fake executor: unexpected action " + action)
	}
	reply := queue[0]
	f.queues[action] = queue[1:]
	return reply.result, reply.err
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
		"visible":  true,
	}
}

// hiddenElement is a DOM node present in the inventory but not rendered (e.g. a
// display:none login modal or a duplicate legacy form).
func hiddenElement(index int, tag, selector, role, name, typ string) map[string]any {
	node := element(index, tag, selector, role, name, typ)
	node["visible"] = false
	return node
}

func loggedInState() map[string]any {
	return map[string]any{
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
		"storage_state":     loggedInState(),
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

// loginSnapshotWithHiddenDuplicate keeps a stale, display:none copy of the form
// in the DOM ahead of the visible one. Detection must use the visible form.
func loginSnapshotWithHiddenDuplicate() map[string]any {
	snapshot := loginSnapshot()
	snapshot["dom_snapshot"].(map[string]any)["elements"] = []any{
		hiddenElement(0, "input", "#legacy-username", "textbox", "Username", "text"),
		hiddenElement(1, "input", "#legacy-password", "textbox", "Password", "password"),
		hiddenElement(2, "button", "#legacy-submit", "button", "Log in", ""),
		element(3, "input", "#username", "textbox", "Username", "text"),
		element(4, "input", "#password", "textbox", "Password", "password"),
		element(5, "button", "#submit", "button", "Log in", ""),
	}
	return snapshot
}

// hiddenLoginSnapshotOnly has a login form that is entirely display:none, as an
// SPA leaves it after a successful login.
func hiddenLoginSnapshotOnly() map[string]any {
	return map[string]any{
		"page_id": "page_1",
		"route":   "/home",
		"dom_snapshot": map[string]any{
			"title": "Home",
			"aria":  "heading \"Home\"",
			"route": "/home",
			"elements": []any{
				hiddenElement(0, "input", "#login-username", "textbox", "Username", "text"),
				hiddenElement(1, "input", "#login-password", "textbox", "Password", "password"),
				hiddenElement(2, "button", "#login-submit", "button", "Log in", ""),
			},
		},
	}
}

// hiddenModalLoggedInSnapshot is the post-submit page of an SPA that hides the
// login modal instead of unmounting it: the password input stays in the DOM.
func hiddenModalLoggedInSnapshot() map[string]any {
	return map[string]any{
		"page_id":           "page_1",
		"route":             "/home",
		"storage_state_ref": "ss_hidden_modal",
		"storage_state":     loggedInState(),
		"dom_snapshot": map[string]any{
			"title": "EzTest Home",
			"aria":  "heading \"Home\"\n  button \"Logout\"",
			"route": "/home",
			"elements": []any{
				hiddenElement(0, "input", "#login-username", "textbox", "Username", "text"),
				hiddenElement(1, "input", "#login-password", "textbox", "Password", "password"),
				element(2, "button", "#logout", "button", "Logout", ""),
			},
		},
	}
}

// unrelatedErrorSnapshot is a logged-in page whose unrelated widgets failed;
// generic error wording must not be mistaken for a rejected login.
func unrelatedErrorSnapshot() map[string]any {
	return map[string]any{
		"page_id":           "page_1",
		"route":             "/home",
		"storage_state_ref": "ss_after_widget_error",
		"storage_state":     loggedInState(),
		"dom_snapshot": map[string]any{
			"title": "Home",
			"aria":  "heading \"Home\"\n  alert \"Request failed\"",
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
	exec.queue("explore.open", loggedInSnapshot())
	accounts := &fakeAccounts{username: "alice", password: "s3cret-pw"}
	sessions := resourceapp.NewSessionStore(time.Hour)
	if err := sessions.Save(context.Background(), resourceapp.LoginSession{
		VersionID:       10,
		VersionSUTID:    7,
		EnvironmentID:   8,
		AccountRole:     "admin",
		StorageStateRef: "ss_reused",
		StorageState:    json.RawMessage(`{"cookies":[{"name":"sid","value":"t"}],"origins":[]}`),
		LoginURL:        "http://127.0.0.1:13335/login",
		Route:           "/login",
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
	// Reuse re-seeds the executor from the cached ref (a stale ref must be
	// detectable), but it must never fill or submit the login form.
	opens := exec.callsOf("explore.open")
	if len(opens) != 1 {
		t.Fatalf("reuse must verify the cached ref with one open, calls = %+v", exec.recorded())
	}
	if opens[0].args["storage_state_ref"] != "ss_reused" {
		t.Fatalf("reuse open args = %+v, want storage_state_ref ss_reused", opens[0].args)
	}
	if len(exec.callsOf("explore.fill")) != 0 || len(exec.callsOf("explore.click")) != 0 {
		t.Fatalf("reusing a stored session must not drive the form, calls = %+v", exec.recorded())
	}
	if len(accounts.calls) != 0 {
		t.Fatalf("reusing a stored session must not read credentials, calls = %v", accounts.calls)
	}
	if len(facts.recorded()) != 1 {
		t.Fatalf("reuse must still record a login_session fact, got %d", len(facts.recorded()))
	}
}

// TestLoginUnknownStorageStateRefFallsBackToFreshLogin covers an executor restart:
// the ref is still in the API cache but the executor no longer knows it, so the
// verification open returns invalid_arguments. That is a cache miss, not a
// failure: invalidate the entry and log in again.
func TestLoginUnknownStorageStateRefFallsBackToFreshLogin(t *testing.T) {
	exec := newFakeExecutor()
	exec.queueError("explore.open", &exploreapp.ExecutorActionError{
		Code:    "invalid_arguments",
		Message: "unknown storage_state_ref: ss_stale",
	})
	exec.queue("explore.open", loginSnapshot())
	exec.queue("explore.fill", map[string]any{})
	exec.queue("explore.fill", map[string]any{})
	exec.queue("explore.click", map[string]any{})
	exec.queue("explore.snapshot", loggedInSnapshot())

	accounts := &fakeAccounts{username: "alice", password: "s3cret-pw"}
	sessions := resourceapp.NewSessionStore(time.Hour)
	if err := sessions.Save(context.Background(), resourceapp.LoginSession{
		VersionID:       10,
		VersionSUTID:    7,
		EnvironmentID:   8,
		AccountRole:     "admin",
		StorageStateRef: "ss_stale",
		StorageState:    json.RawMessage(`{"cookies":[{"name":"sid","value":"expired"}],"origins":[]}`),
		LoginURL:        "http://127.0.0.1:13335/login",
		Route:           "/login",
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
	if session.Status != exploreapp.SessionActive || session.Reused {
		t.Fatalf("session = %+v, want a fresh active login", session)
	}
	if session.StorageStateRef != "ss_abc123" {
		t.Fatalf("storage_state_ref = %q, want the freshly captured ref", session.StorageStateRef)
	}
	opens := exec.callsOf("explore.open")
	if len(opens) != 2 {
		t.Fatalf("open calls = %d, want stale-ref verification then a fresh login", len(opens))
	}
	if opens[0].args["storage_state_ref"] != "ss_stale" {
		t.Fatalf("first open args = %+v, want the cached ref", opens[0].args)
	}
	if _, present := opens[1].args["storage_state_ref"]; present {
		t.Fatalf("fresh login open args = %+v, must not reuse the stale ref", opens[1].args)
	}
	if len(accounts.calls) != 1 || accounts.calls[0] != 81 {
		t.Fatalf("account store calls = %v, want [81]", accounts.calls)
	}
	stored, ok := sessions.Find(context.Background(), 7, 8, "admin")
	if !ok || stored.StorageStateRef != "ss_abc123" {
		t.Fatalf("stale session was not replaced: %+v (ok=%v)", stored, ok)
	}
	if len(facts.recorded()) != 1 {
		t.Fatalf("a fresh login must record one fact, got %d", len(facts.recorded()))
	}
}

// TestLoginExecuteWithoutDetectOpensConfiguredLoginPage covers the caller that
// skips Detect: Execute opens the configured login URL itself.
func TestLoginExecuteWithoutDetectOpensConfiguredLoginPage(t *testing.T) {
	exec := newFakeExecutor()
	exec.queue("explore.open", loginSnapshot())
	exec.queue("explore.fill", map[string]any{})
	exec.queue("explore.fill", map[string]any{})
	exec.queue("explore.click", map[string]any{})
	exec.queue("explore.snapshot", loggedInSnapshot())
	facts := &fakeFacts{}
	flow := newFlow(t, exec, &fakeAccounts{username: "alice", password: "s3cret-pw"}, resourceapp.NewSessionStore(time.Hour), facts)

	session, err := flow.Execute(context.Background(), exploreapp.Account{ID: 81, Role: "admin"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if session.Status != exploreapp.SessionActive || session.Reused {
		t.Fatalf("session = %+v, want a fresh active login", session)
	}
	opens := exec.callsOf("explore.open")
	if len(opens) != 1 {
		t.Fatalf("open calls = %d, want 1", len(opens))
	}
	if opens[0].args["url"] != "http://127.0.0.1:13335/login" {
		t.Fatalf("open args = %+v, want the configured login URL", opens[0].args)
	}
	fills := exec.callsOf("explore.fill")
	if len(fills) != 2 || fills[0].args["selector"] != "#username" || fills[1].args["selector"] != "#password" {
		t.Fatalf("fill calls = %+v, want the detected form selectors", fills)
	}
	if len(facts.recorded()) != 1 {
		t.Fatalf("a fresh login must record one fact, got %d", len(facts.recorded()))
	}
}

// TestLoginNonRetryableExecutorErrorBlocksManualLogin proves a deterministic
// executor error (a selector that cannot match) asks for a human instead of
// scheduling another blind autonomous attempt.
func TestLoginNonRetryableExecutorErrorBlocksManualLogin(t *testing.T) {
	exec := newFakeExecutor()
	exec.queue("explore.snapshot", loginSnapshot())
	exec.queueError("explore.fill", &exploreapp.ExecutorActionError{
		Code:    "target_not_found",
		Message: "no element matched selector #username",
	})
	facts := &fakeFacts{}
	flow := newFlow(t, exec, &fakeAccounts{username: "alice", password: "s3cret-pw"}, resourceapp.NewSessionStore(time.Hour), facts)
	detect(t, flow)

	session, err := flow.Execute(context.Background(), exploreapp.Account{ID: 81, Role: "admin"})
	if err != nil {
		t.Fatalf("a non-retryable executor failure is a blocked outcome: %v", err)
	}
	if session.Status != exploreapp.SessionBlocked {
		t.Fatalf("session status = %q, want blocked", session.Status)
	}
	if session.Action != exploreapp.ActionManualLogin {
		t.Fatalf("blocked action = %q, want %q", session.Action, exploreapp.ActionManualLogin)
	}
	if session.Reason != "login_fill_failed" {
		t.Fatalf("blocked reason = %q", session.Reason)
	}
	if len(facts.recorded()) != 0 {
		t.Fatalf("blocked login must not record a fact")
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

func TestLoginDetectIgnoresHiddenDuplicateForm(t *testing.T) {
	exec := newFakeExecutor()
	exec.queue("explore.snapshot", loginSnapshotWithHiddenDuplicate())
	flow := newFlow(t, exec, &fakeAccounts{}, resourceapp.NewSessionStore(time.Hour), &fakeFacts{})

	form, err := flow.Detect(context.Background(), exploreapp.Page{
		ID:  "page_1",
		URL: "http://127.0.0.1:13335/login",
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if form.UsernameSelector != "#username" {
		t.Fatalf("username selector = %q, want the visible #username (not the display:none duplicate)", form.UsernameSelector)
	}
	if form.PasswordSelector != "#password" {
		t.Fatalf("password selector = %q, want the visible #password (not the display:none duplicate)", form.PasswordSelector)
	}
	if form.SubmitSelector != "#submit" {
		t.Fatalf("submit selector = %q, want the visible #submit", form.SubmitSelector)
	}
}

func TestLoginDetectHiddenFormOnlyIsBlocked(t *testing.T) {
	exec := newFakeExecutor()
	exec.queue("explore.snapshot", hiddenLoginSnapshotOnly())
	flow := newFlow(t, exec, &fakeAccounts{}, resourceapp.NewSessionStore(time.Hour), &fakeFacts{})

	_, err := flow.Detect(context.Background(), exploreapp.Page{ID: "page_1", URL: "http://127.0.0.1:13335/home"})
	if err == nil {
		t.Fatal("a display:none login form must not be detected")
	}
	var blocked *exploreapp.BlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("Detect error = %T, want *BlockedError", err)
	}
	if blocked.Reason != "login_form_not_found" {
		t.Fatalf("blocked reason = %q", blocked.Reason)
	}
}

// TestLoginDetectWithPageIDOnlyUsesSnapshotURL covers the snapshot-only Detect
// path: the caller knows the page id but not the URL, so the URL must come from
// the snapshot itself (dom_snapshot.url).
func TestLoginDetectWithPageIDOnlyUsesSnapshotURL(t *testing.T) {
	snapshot := loginSnapshot()
	snapshot["dom_snapshot"].(map[string]any)["url"] = "http://127.0.0.1:13335/login"
	exec := newFakeExecutor()
	exec.queue("explore.snapshot", snapshot)
	flow := newFlow(t, exec, &fakeAccounts{}, resourceapp.NewSessionStore(time.Hour), &fakeFacts{})

	form, err := flow.Detect(context.Background(), exploreapp.Page{ID: "page_1"})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if form.LoginURL != "http://127.0.0.1:13335/login" {
		t.Fatalf("login URL = %q, want the URL from dom_snapshot.url", form.LoginURL)
	}
	if form.Route != "/login" {
		t.Fatalf("route = %q", form.Route)
	}
}

// TestLoginHiddenModalAfterSubmitIsSuccess covers an SPA that hides the login
// modal instead of unmounting it: the password input stays in the DOM but is
// not rendered, so the login must be treated as successful.
func TestLoginHiddenModalAfterSubmitIsSuccess(t *testing.T) {
	exec := newFakeExecutor()
	exec.queue("explore.snapshot", loginSnapshot())
	exec.queue("explore.fill", map[string]any{})
	exec.queue("explore.fill", map[string]any{})
	exec.queue("explore.click", map[string]any{})
	exec.queue("explore.snapshot", hiddenModalLoggedInSnapshot())
	sessions := resourceapp.NewSessionStore(time.Hour)
	facts := &fakeFacts{}
	flow := newFlow(t, exec, &fakeAccounts{username: "alice", password: "s3cret-pw"}, sessions, facts)
	detect(t, flow)

	session, err := flow.Execute(context.Background(), exploreapp.Account{ID: 81, Role: "admin"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if session.Status != exploreapp.SessionActive || session.Reused {
		t.Fatalf("session = %+v, want a fresh active login (hidden modal must not count as still visible)", session)
	}
	if session.StorageStateRef != "ss_hidden_modal" {
		t.Fatalf("storage_state_ref = %q", session.StorageStateRef)
	}
	if _, ok := sessions.Find(context.Background(), 7, 8, "admin"); !ok {
		t.Fatal("successful login must cache the hidden-modal session")
	}
	if len(facts.recorded()) != 1 {
		t.Fatalf("recorded facts = %d, want 1", len(facts.recorded()))
	}
}

// TestLoginFlowIgnoresUnrelatedPageErrorsAfterSubmit proves generic page errors
// ("Request failed") are not treated as a credential rejection.
func TestLoginFlowIgnoresUnrelatedPageErrorsAfterSubmit(t *testing.T) {
	exec := newFakeExecutor()
	exec.queue("explore.snapshot", loginSnapshot())
	exec.queue("explore.fill", map[string]any{})
	exec.queue("explore.fill", map[string]any{})
	exec.queue("explore.click", map[string]any{})
	exec.queue("explore.snapshot", unrelatedErrorSnapshot())
	facts := &fakeFacts{}
	flow := newFlow(t, exec, &fakeAccounts{username: "alice", password: "s3cret-pw"}, resourceapp.NewSessionStore(time.Hour), facts)
	detect(t, flow)

	session, err := flow.Execute(context.Background(), exploreapp.Account{ID: 81, Role: "admin"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if session.Status != exploreapp.SessionActive {
		t.Fatalf("session = %+v, an unrelated page error must not block the login", session)
	}
	if session.StorageStateRef != "ss_after_widget_error" {
		t.Fatalf("storage_state_ref = %q", session.StorageStateRef)
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
