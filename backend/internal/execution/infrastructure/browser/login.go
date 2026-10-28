package browser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	resourcedomain "github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

const (
	// loginDefaultTimeout bounds one login attempt when the request carries no
	// timeout. The resource service normally passes the recipe timeout.
	loginDefaultTimeout = 20 * time.Second
	// loginFailureProbeTimeout bounds one failure-selector probe after the
	// success wait timed out. Probes run in sequence, so this is a per-selector
	// budget, not the whole failure detection budget.
	loginFailureProbeTimeout = 2 * time.Second
	// loginCloseTimeout bounds the best-effort context close. It deliberately
	// does not use the login context, which may already be canceled.
	loginCloseTimeout = 5 * time.Second
)

// Errors returned by the adapter carry no credential, URL query or executor
// response body. The resource service maps them to the unreachable outcome.
var (
	// errLoginExecutorNotConfigured reports a missing executor dependency.
	errLoginExecutorNotConfigured = errors.New("login: browser executor is not configured")
	// errLoginCapture reports a storage_state capture that returned no usable
	// document. It never includes the executor's response.
	errLoginCapture = errors.New("login: browser storage_state capture failed")
)

// LoginBrowser runs a configured form login through the executor protocol and
// captures the resulting browser state. It is the production implementation of
// resource/application.LoginBrowser and imports only the resource domain and
// application types, so the resource context never depends on execution.
//
// The plaintext password is passed to the executor as a secret fill value and is
// never included in an error, event or log line. The adapter itself has no
// logger: request bodies, action results and failure details are not emitted.
type LoginBrowser struct {
	executor BrowserExecutor
}

var _ resourceapp.LoginBrowser = (*LoginBrowser)(nil)

// NewLoginBrowser returns the login adapter backed by executor.
func NewLoginBrowser(executor BrowserExecutor) *LoginBrowser {
	return &LoginBrowser{executor: executor}
}

// targetArg is the selector form of an executor target (spec §5.6).
type targetArg struct {
	Selector string `json:"selector"`
}

// gotoArgs is the goto action argument.
type gotoArgs struct {
	URL       string `json:"url"`
	TimeoutMS int64  `json:"timeout_ms,omitempty"`
}

// fillArgs is the fill action argument. Secret marks the value for scrub in the
// executor's errors, console, network and snapshot output.
type fillArgs struct {
	Target targetArg `json:"target"`
	Value  string    `json:"value"`
	Secret bool      `json:"secret,omitempty"`
}

// clickArgs is the click action argument.
type clickArgs struct {
	Target targetArg `json:"target"`
}

// waitForArgs is the wait_for action argument. Exactly one of Target and
// URLContains is set.
type waitForArgs struct {
	Target      *targetArg `json:"target,omitempty"`
	URLContains string     `json:"url_contains,omitempty"`
	TimeoutMS   int64      `json:"timeout_ms,omitempty"`
}

// storageStateArgs is the storage_state action argument. The executor includes
// sessionStorage by default; it is requested explicitly so the captured state is
// canonical regardless of the executor's default.
type storageStateArgs struct {
	IncludeSessionStorage bool  `json:"include_session_storage"`
	TimeoutMS             int64 `json:"timeout_ms,omitempty"`
}

// Login implements resource/application.LoginBrowser. A browser-gate outcome
// (mfa/captcha/invalid) is returned as a LoginResult with no error; executor and
// internal failures are returned as unreachable or as a sanitized error, which
// the service also classifies as unreachable.
func (b *LoginBrowser) Login(ctx context.Context, req resourceapp.LoginRequest) (resourceapp.LoginResult, error) {
	if b == nil || b.executor == nil {
		return resourceapp.LoginResult{}, errLoginExecutorNotConfigured
	}
	loginURL := strings.TrimSpace(req.LoginURL)
	if loginURL == "" {
		loginURL = strings.TrimSpace(req.Flow.LoginURL)
	}
	if loginURL == "" {
		return unreachableLoginResult(), nil
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = loginDefaultTimeout
	}

	// A fresh context per attempt: no storage state, hosts limited to the login
	// page (effective and configured) plus the absolute success URL host when the
	// success rule names one. The unique key keeps concurrent attempts apart in
	// the executor's diagnostics.
	ref, err := b.executor.NewContext(ctx, ContextSpec{
		Key:          loginContextKey(req),
		AllowedHosts: loginAllowedHosts(loginURL, req.Flow),
		BaseURL:      loginURL,
	})
	if err != nil {
		// The executor may be unreachable; the error text is not propagated so a
		// request body can never leak through it.
		return unreachableLoginResult(), nil
	}
	// The context must always be disposed, including every failure path below.
	defer b.closeContext(ref)

	if err := b.runStep(ctx, ref, "goto", gotoArgs{
		URL:       loginURL,
		TimeoutMS: milliseconds(timeout),
	}); err != nil {
		return unreachableLoginResult(), nil
	}
	if err := b.runStep(ctx, ref, "fill", fillArgs{
		Target: targetArg{Selector: req.Flow.UsernameSelector},
		Value:  req.Username,
	}); err != nil {
		return unreachableLoginResult(), nil
	}
	if err := b.runStep(ctx, ref, "fill", fillArgs{
		Target: targetArg{Selector: req.Flow.PasswordSelector},
		Value:  req.Password,
		Secret: true,
	}); err != nil {
		return unreachableLoginResult(), nil
	}
	if err := b.runStep(ctx, ref, "click", clickArgs{
		Target: targetArg{Selector: req.Flow.SubmitSelector},
	}); err != nil {
		return unreachableLoginResult(), nil
	}

	// Wait for the configured success rule with the recipe timeout. A timeout is
	// not immediately unreachable: the page may have stopped at an MFA, captcha
	// or rejected-credential state, which is a manual takeover, not a broken
	// environment.
	wait := waitForArgs{TimeoutMS: milliseconds(timeout)}
	if strings.TrimSpace(req.Flow.Success.Selector) != "" {
		wait.Target = &targetArg{Selector: req.Flow.Success.Selector}
	} else {
		wait.URLContains = req.Flow.Success.URLContains
	}
	if err := b.runStep(ctx, ref, "wait_for", wait); err != nil {
		if outcome := b.detectFailure(ctx, ref, req.Flow.Failure); outcome != "" {
			return resourceapp.LoginResult{Outcome: outcome}, nil
		}
		return unreachableLoginResult(), nil
	}

	// Login succeeded: capture the canonical storage_state including
	// sessionStorage. A capture failure is a sanitized internal error; the
	// service classifies it as an unavailable environment and never leases a
	// half-captured session.
	result, err := b.runAction(ctx, ref, "storage_state", storageStateArgs{
		IncludeSessionStorage: true,
		TimeoutMS:             milliseconds(timeout),
	})
	if err != nil || result.ErrorCode != "" {
		return resourceapp.LoginResult{}, errLoginCapture
	}
	return captureResult(result.Result)
}

// runAction issues one executor action and returns its decoded result. Transport
// failures are returned as errors; protocol failures stay in result.ErrorCode.
func (b *LoginBrowser) runAction(ctx context.Context, ref ContextRef, name string, args any) (ActionResult, error) {
	payload, err := json.Marshal(args)
	if err != nil {
		return ActionResult{}, errLoginCapture
	}
	return b.executor.Action(ctx, ref, Action{Name: name, Args: payload})
}

// runStep runs one preparatory action and reports whether it succeeded. A
// transport failure or a protocol error is a non-success; neither error text is
// retained, because the executor's message could echo page content.
func (b *LoginBrowser) runStep(ctx context.Context, ref ContextRef, name string, args any) error {
	result, err := b.runAction(ctx, ref, name, args)
	if err != nil {
		return errLoginCapture
	}
	if result.ErrorCode != "" {
		return errLoginCapture
	}
	return nil
}

// detectFailure probes the configured MFA, captcha and invalid-credential
// selectors in that order and returns the matching login outcome, or "" when the
// page shows none of them. Every probe is a short wait_for, so a selector that
// is not present does not extend the failure path by the full recipe timeout.
func (b *LoginBrowser) detectFailure(ctx context.Context, ref ContextRef, failure resourcedomain.FailureSelectors) string {
	checks := []struct {
		selector string
		outcome  string
	}{
		{failure.MFASelector, resourcedomain.LoginOutcomeMFA},
		{failure.CaptchaSelector, resourcedomain.LoginOutcomeCaptcha},
		{failure.InvalidSelector, resourcedomain.LoginOutcomeInvalid},
	}
	for _, check := range checks {
		selector := strings.TrimSpace(check.selector)
		if selector == "" {
			continue
		}
		result, err := b.runAction(ctx, ref, "wait_for", waitForArgs{
			Target:    &targetArg{Selector: selector},
			TimeoutMS: milliseconds(loginFailureProbeTimeout),
		})
		if err != nil || result.ErrorCode != "" {
			continue
		}
		return check.outcome
	}
	return ""
}

// closeContext disposes the browser context with an independent short timeout so
// a canceled login context cannot skip cleanup. A close failure is ignored: the
// executor reaps abandoned contexts on its own.
func (b *LoginBrowser) closeContext(ref ContextRef) {
	ctx, cancel := context.WithTimeout(context.Background(), loginCloseTimeout)
	defer cancel()
	_ = b.executor.Close(ctx, ref)
}

// captureResult validates the captured document and repeats the per-origin
// sessionStorage map for consumers that inject it directly.
func captureResult(raw json.RawMessage) (resourceapp.LoginResult, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return resourceapp.LoginResult{}, errLoginCapture
	}
	sessionStorage, err := sessionStorageDocument(raw)
	if err != nil {
		return resourceapp.LoginResult{}, errLoginCapture
	}
	return resourceapp.LoginResult{
		Outcome:        resourcedomain.LoginOutcomeValid,
		StorageState:   raw,
		SessionStorage: sessionStorage,
	}, nil
}

// sessionStorageDocument renders the per-origin sessionStorage map of a captured
// storage_state document. It returns nil when no origin carries sessionStorage.
func sessionStorageDocument(raw json.RawMessage) (json.RawMessage, error) {
	var doc struct {
		Origins []struct {
			Origin         string                            `json:"origin"`
			SessionStorage []resourcedomain.LocalStorageItem `json:"sessionStorage"`
		} `json:"origins"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	document := make(map[string][]resourcedomain.LocalStorageItem)
	for _, origin := range doc.Origins {
		if origin.Origin == "" || len(origin.SessionStorage) == 0 {
			continue
		}
		document[origin.Origin] = origin.SessionStorage
	}
	if len(document) == 0 {
		return nil, nil
	}
	return json.Marshal(document)
}

// loginContextKey builds the unique executor context key
// "login:{system}:{account}:{nonce}".
func loginContextKey(req resourceapp.LoginRequest) string {
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Sprintf("login:%d:%d:%d", req.SystemID, req.AccountID, time.Now().UnixNano())
	}
	return fmt.Sprintf("login:%d:%d:%s", req.SystemID, req.AccountID, hex.EncodeToString(nonce))
}

// loginAllowedHosts returns the distinct hosts the login context may contact:
// the effective login URL, the configured recipe login URL and the host of an
// absolute success URL.
func loginAllowedHosts(loginURL string, flow resourcedomain.FormDefinition) []string {
	hosts := make([]string, 0, 3)
	seen := make(map[string]struct{}, 3)
	for _, raw := range []string{loginURL, flow.LoginURL, flow.Success.URLContains} {
		host := hostOfURL(raw)
		if host == "" {
			continue
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		hosts = append(hosts, host)
	}
	return hosts
}

// hostOfURL returns the lower-case hostname of an absolute URL, or "" when raw
// is not an absolute URL (for example the usual "/home" success fragment).
func hostOfURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

// milliseconds converts a duration to the executor's integer millisecond unit.
func milliseconds(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64(d / time.Millisecond)
}

// unreachableLoginResult is the fail-closed transport outcome.
func unreachableLoginResult() resourceapp.LoginResult {
	return resourceapp.LoginResult{Outcome: resourcedomain.LoginOutcomeUnreachable}
}
