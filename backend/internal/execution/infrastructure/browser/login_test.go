package browser_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/browser"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/testutil"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// loginPassword is deliberately distinctive so leak assertions can detect it.
const loginPassword = "s3cret-login-password"

// loginState is a canonical executor storage_state result including the P6
// sessionStorage extension.
const loginState = `{"cookies":[{"name":"sid","value":"cookie-value","domain":"app.test","path":"/","expires":-1,"httpOnly":true,"secure":true,"sameSite":"Lax"}],"origins":[{"origin":"https://app.test","localStorage":[{"name":"theme","value":"dark"}],"sessionStorage":[{"name":"csrf","value":"token-123"}]}]}`

// waitArgs is the subset of a wait_for action argument the fake inspects.
type waitArgs struct {
	Target *struct {
		Selector string `json:"selector"`
	} `json:"target"`
	URLContains string `json:"url_contains"`
	TimeoutMS   int64  `json:"timeout_ms"`
}

// fillArgs is the subset of a fill action argument the tests inspect.
type fillArgs struct {
	Target struct {
		Selector string `json:"selector"`
	} `json:"target"`
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
}

// loginHarness scripts one successful or failing login behind the fake executor
// and records every action the adapter issues.
type loginHarness struct {
	fake *testutil.FakeBrowserExecutor

	mu             sync.Mutex
	actions        []application.Action
	successMatched bool
	failures       map[string]bool
	actionErrs     map[string]error
}

// newLoginHarness installs the scripted action handler.
func newLoginHarness(t *testing.T) *loginHarness {
	t.Helper()
	h := &loginHarness{
		fake:     testutil.NewFakeBrowserExecutor(),
		failures: map[string]bool{},
		actionErrs: map[string]error{
			"goto":          nil,
			"storage_state": nil,
		},
	}
	h.fake.SetActionHandler(func(_ context.Context, _ application.ContextRef, action application.Action) (application.ActionResult, error) {
		h.mu.Lock()
		h.actions = append(h.actions, action)
		injected := h.actionErrs[action.Name]
		matched := h.successMatched
		failures := make(map[string]bool, len(h.failures))
		for selector, present := range h.failures {
			failures[selector] = present
		}
		h.mu.Unlock()
		if injected != nil {
			return application.ActionResult{}, injected
		}
		switch action.Name {
		case "storage_state":
			return application.ActionResult{Result: json.RawMessage(loginState)}, nil
		case "wait_for":
			var args waitArgs
			if err := json.Unmarshal(action.Args, &args); err != nil {
				t.Errorf("wait_for args are not valid JSON: %v", err)
				return application.ActionResult{ErrorCode: "invalid_arguments"}, nil
			}
			if args.Target != nil {
				if present, isFailure := failures[args.Target.Selector]; isFailure {
					if present {
						return application.ActionResult{}, nil
					}
					return application.ActionResult{ErrorCode: "timeout", ErrorMessage: "timed out"}, nil
				}
			}
			if matched {
				return application.ActionResult{}, nil
			}
			return application.ActionResult{ErrorCode: "timeout", ErrorMessage: "timed out"}, nil
		}
		return application.ActionResult{}, nil
	})
	return h
}

// failureFlow returns a recipe with every failure selector configured and a URL
// success rule.
func failureFlow() domain.FormDefinition {
	return domain.FormDefinition{
		LoginURL:         "https://login.test/login",
		UsernameSelector: "#username",
		PasswordSelector: "#password",
		SubmitSelector:   "#submit",
		Success:          domain.SuccessRule{URLContains: "https://app.test/home"},
		Failure: domain.FailureSelectors{
			MFASelector:     "#mfa",
			CaptchaSelector: "#captcha",
			InvalidSelector: "#invalid-credentials",
		},
		TimeoutMS: 15000,
	}
}

// loginRequest builds a request that exercises the access login-URL override.
func loginRequest(flow domain.FormDefinition) resourceapp.LoginRequest {
	return resourceapp.LoginRequest{
		EnvironmentID: 7,
		SystemID:      11,
		AccountID:     13,
		AccessID:      17,
		Flow:          flow,
		LoginURL:      "https://override.test/sign-in",
		Username:      "fixture-user@example.test",
		Password:      loginPassword,
		Timeout:       15 * time.Second,
	}
}

// actionsNamed returns the recorded actions with the given name.
func (h *loginHarness) actionsNamed(name string) []application.Action {
	h.mu.Lock()
	defer h.mu.Unlock()
	var matched []application.Action
	for _, action := range h.actions {
		if action.Name == name {
			matched = append(matched, action)
		}
	}
	return matched
}

// actionNames returns the recorded action names in order.
func (h *loginHarness) actionNames() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	names := make([]string, len(h.actions))
	for i, action := range h.actions {
		names[i] = action.Name
	}
	return names
}

// setFailure marks one failure selector as present or absent.
func (h *loginHarness) setFailure(selector string, present bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failures[selector] = present
}

// setActionError injects a transport error for one action name.
func (h *loginHarness) setActionError(name string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.actionErrs[name] = err
}

// assertClosedExactlyOnce asserts the adapter disposed its browser context.
func assertClosedExactlyOnce(t *testing.T, fake *testutil.FakeBrowserExecutor) {
	t.Helper()
	if closed := fake.Closed(); len(closed) != 1 {
		t.Fatalf("closed contexts = %d, want exactly 1", len(closed))
	}
}

// decodeObject decodes raw JSON into a generic object for order-insensitive
// comparison.
func decodeObject(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("decode JSON object: %v (raw=%s)", err, raw)
	}
	return object
}

func TestLoginSuccessURLCapturesStateAndClosesContext(t *testing.T) {
	h := newLoginHarness(t)
	h.successMatched = true
	adapter := browser.NewLoginBrowser(h.fake)

	flow := failureFlow()
	result, err := adapter.Login(context.Background(), loginRequest(flow))
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if result.Outcome != domain.LoginOutcomeValid {
		t.Fatalf("outcome = %q, want %q", result.Outcome, domain.LoginOutcomeValid)
	}
	if got, want := decodeObject(t, result.StorageState), decodeObject(t, json.RawMessage(loginState)); !reflect.DeepEqual(got, want) {
		t.Fatalf("storage state = %s, want %s", result.StorageState, loginState)
	}
	wantSession := map[string]any{
		"https://app.test": []any{
			map[string]any{"name": "csrf", "value": "token-123"},
		},
	}
	if got := decodeObject(t, result.SessionStorage); !reflect.DeepEqual(got, wantSession) {
		t.Fatalf("session storage = %s, want %v", result.SessionStorage, wantSession)
	}
	assertClosedExactlyOnce(t, h.fake)

	calls := h.fake.Calls()
	if len(calls) == 0 || calls[0].Kind != testutil.FakeCallNewContext {
		t.Fatalf("first call = %+v, want NewContext", calls)
	}
	spec := calls[0].Spec
	if spec.BaseURL != "https://override.test/sign-in" {
		t.Fatalf("base url = %q, want the effective login url", spec.BaseURL)
	}
	for _, host := range []string{"login.test", "override.test", "app.test"} {
		if !containsString(spec.AllowedHosts, host) {
			t.Fatalf("allowed hosts = %v, want %q", spec.AllowedHosts, host)
		}
	}
	if len(spec.StorageState) != 0 || len(spec.SessionStorage) != 0 {
		t.Fatalf("login context must start without storage state: %+v", spec)
	}
	if !regexp.MustCompile(`^login:11:13:[0-9a-f]+$`).MatchString(spec.Key) {
		t.Fatalf("context key = %q, want login:{system}:{account}:{nonce}", spec.Key)
	}

	wantOrder := []string{"goto", "fill", "fill", "click", "wait_for", "storage_state"}
	if got := h.actionNames(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("actions = %v, want %v", got, wantOrder)
	}

	fills := h.actionsNamed("fill")
	if len(fills) != 2 {
		t.Fatalf("fill actions = %d, want 2", len(fills))
	}
	var username, password fillArgs
	if err := json.Unmarshal(fills[0].Args, &username); err != nil {
		t.Fatalf("decode username fill: %v", err)
	}
	if err := json.Unmarshal(fills[1].Args, &password); err != nil {
		t.Fatalf("decode password fill: %v", err)
	}
	if username.Target.Selector != "#username" || username.Value != "fixture-user@example.test" || username.Secret {
		t.Fatalf("username fill = %+v", username)
	}
	if password.Target.Selector != "#password" || password.Value != loginPassword || !password.Secret {
		t.Fatalf("password fill = %+v, want secret=true and the credential value", password)
	}

	waits := h.actionsNamed("wait_for")
	if len(waits) != 1 {
		t.Fatalf("wait_for actions = %d, want 1 before success", len(waits))
	}
	var wait waitArgs
	if err := json.Unmarshal(waits[0].Args, &wait); err != nil {
		t.Fatalf("decode wait_for: %v", err)
	}
	if wait.URLContains != "https://app.test/home" {
		t.Fatalf("success wait = %+v, want url_contains", wait)
	}
	if wait.TimeoutMS != int64(flow.TimeoutMS) {
		t.Fatalf("success wait timeout = %d, want %d", wait.TimeoutMS, flow.TimeoutMS)
	}
}

func TestLoginSuccessSelectorWaitsForSelector(t *testing.T) {
	h := newLoginHarness(t)
	h.successMatched = true
	adapter := browser.NewLoginBrowser(h.fake)

	flow := failureFlow()
	flow.Success = domain.SuccessRule{Selector: "#welcome"}
	result, err := adapter.Login(context.Background(), loginRequest(flow))
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if result.Outcome != domain.LoginOutcomeValid {
		t.Fatalf("outcome = %q, want valid", result.Outcome)
	}
	assertClosedExactlyOnce(t, h.fake)

	waits := h.actionsNamed("wait_for")
	if len(waits) != 1 {
		t.Fatalf("wait_for actions = %d, want 1", len(waits))
	}
	var wait waitArgs
	if err := json.Unmarshal(waits[0].Args, &wait); err != nil {
		t.Fatalf("decode wait_for: %v", err)
	}
	if wait.Target == nil || wait.Target.Selector != "#welcome" {
		t.Fatalf("success wait = %+v, want target #welcome", wait)
	}
	if wait.URLContains != "" {
		t.Fatalf("success wait = %+v, want no url_contains", wait)
	}
}

func TestLoginFailureSelectorsMapToOutcomes(t *testing.T) {
	tests := []struct {
		name     string
		selector string
		outcome  string
	}{
		{name: "mfa", selector: "#mfa", outcome: domain.LoginOutcomeMFA},
		{name: "captcha", selector: "#captcha", outcome: domain.LoginOutcomeCaptcha},
		{name: "invalid credentials", selector: "#invalid-credentials", outcome: domain.LoginOutcomeInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newLoginHarness(t)
			h.successMatched = false
			h.setFailure(test.selector, true)
			adapter := browser.NewLoginBrowser(h.fake)

			result, err := adapter.Login(context.Background(), loginRequest(failureFlow()))
			if err != nil {
				t.Fatalf("Login: %v", err)
			}
			if result.Outcome != test.outcome {
				t.Fatalf("outcome = %q, want %q", result.Outcome, test.outcome)
			}
			if len(result.StorageState) != 0 {
				t.Fatalf("failed login returned a storage state")
			}
			assertClosedExactlyOnce(t, h.fake)
			if calls := h.actionsNamed("storage_state"); len(calls) != 0 {
				t.Fatalf("failed login captured state %d times", len(calls))
			}
		})
	}
}

func TestLoginSuccessTimeoutWithoutGateIsUnreachable(t *testing.T) {
	h := newLoginHarness(t)
	h.successMatched = false
	adapter := browser.NewLoginBrowser(h.fake)

	result, err := adapter.Login(context.Background(), loginRequest(failureFlow()))
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if result.Outcome != domain.LoginOutcomeUnreachable {
		t.Fatalf("outcome = %q, want unreachable", result.Outcome)
	}
	assertClosedExactlyOnce(t, h.fake)
}

func TestLoginClosesContextWhenExecutorFails(t *testing.T) {
	h := newLoginHarness(t)
	h.successMatched = true
	h.setActionError("goto", errors.New("connection refused"))
	adapter := browser.NewLoginBrowser(h.fake)

	result, err := adapter.Login(context.Background(), loginRequest(failureFlow()))
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if result.Outcome != domain.LoginOutcomeUnreachable {
		t.Fatalf("outcome = %q, want unreachable", result.Outcome)
	}
	assertClosedExactlyOnce(t, h.fake)
}

func TestLoginNewContextFailureHasNoContextToClose(t *testing.T) {
	h := newLoginHarness(t)
	h.successMatched = true
	h.fake.SetNewContextError(errors.New("executor unavailable"))
	adapter := browser.NewLoginBrowser(h.fake)

	result, err := adapter.Login(context.Background(), loginRequest(failureFlow()))
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if result.Outcome != domain.LoginOutcomeUnreachable {
		t.Fatalf("outcome = %q, want unreachable", result.Outcome)
	}
	if closed := h.fake.Closed(); len(closed) != 0 {
		t.Fatalf("closed contexts = %d, want 0", len(closed))
	}
}

func TestLoginCaptureFailureIsSanitizedAndClosesContext(t *testing.T) {
	h := newLoginHarness(t)
	h.successMatched = true
	h.setActionError("storage_state", fmt.Errorf("executor rejected value %s", loginPassword))
	adapter := browser.NewLoginBrowser(h.fake)

	_, err := adapter.Login(context.Background(), loginRequest(failureFlow()))
	if err == nil {
		t.Fatal("Login returned no error for a failed capture")
	}
	if strings.Contains(err.Error(), loginPassword) {
		t.Fatalf("error leaked the password: %v", err)
	}
	assertClosedExactlyOnce(t, h.fake)
}

func TestLoginContextKeyIsUniquePerAttempt(t *testing.T) {
	h := newLoginHarness(t)
	h.successMatched = true
	adapter := browser.NewLoginBrowser(h.fake)

	for i := 0; i < 2; i++ {
		if _, err := adapter.Login(context.Background(), loginRequest(failureFlow())); err != nil {
			t.Fatalf("Login %d: %v", i, err)
		}
	}
	var keys []string
	for _, call := range h.fake.Calls() {
		if call.Kind == testutil.FakeCallNewContext {
			keys = append(keys, call.Spec.Key)
		}
	}
	if len(keys) != 2 {
		t.Fatalf("context keys = %v, want two", keys)
	}
	if keys[0] == keys[1] {
		t.Fatalf("context keys are not unique: %v", keys)
	}
	for _, key := range keys {
		if !strings.HasPrefix(key, "login:11:13:") {
			t.Fatalf("context key = %q, want the login:{system}:{account}:{nonce} shape", key)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
