// Package explore implements the autonomous exploration capabilities of the
// explorer role (spec §11). This file owns capability #1: autonomous login and
// session reuse.
//
// A LoginFlow detects a login form from the executor's page snapshot, fills the
// credentials through the account store (never through a prompt or knowledge
// payload), captures the executor storageState on success and records one
// append-only `login_session` version fact. A still-valid storage state is
// reused without touching the login form. A rejected login is a blocked
// outcome with a concrete recovery action, not an infrastructure error.
package explore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ExploreActionsPath is the executor protocol v1.3 route of the explore action
// family. It is deliberately separate from /v1/contexts/{id}/actions: explore
// pages are not execution contexts.
const ExploreActionsPath = "/v1/explore/actions"

// defaultExecutorTimeout mirrors the executor client bound: the executor caps a
// single action at 60s, so the API side waits slightly longer for its typed
// error envelope.
const defaultExecutorTimeout = 75 * time.Second

// KindLoginSession is the version_fact kind of a successful login.
const KindLoginSession = "login_session"

// Session status values returned by LoginFlow.Execute.
const (
	SessionActive  = "active"
	SessionBlocked = "blocked"
)

// Recovery actions attached to a blocked login. They tell the caller (or the
// version stage projection) what a human or the next run must do.
const (
	// ActionFixCredentials asks for a corrected account password/role.
	ActionFixCredentials = "fix_credentials"
	// ActionManualLogin asks for a human login (CAPTCHA/MFA/unknown form).
	ActionManualLogin = "manual_login"
	// ActionRetryLogin asks for another autonomous attempt (transient executor
	// failure or an SUT that was still starting).
	ActionRetryLogin = "retry_login"
	// ActionConfigureAccount asks for a bound test account.
	ActionConfigureAccount = "configure_account"
)

// Explore action names on the executor protocol v1.3 /v1/explore route.
const (
	actionOpen     = "explore.open"
	actionSnapshot = "explore.snapshot"
	actionFill     = "explore.fill"
	actionClick    = "explore.click"
)

// Page identifies one executor explore page.
type Page struct {
	ID  string
	URL string
}

// Account identifies the test account selected for the login. It carries no
// secret: credentials are resolved through AccountStore at execution time.
type Account struct {
	ID   int64
	Role string
}

// Element is one interactive element of an explore DOM snapshot.
type Element struct {
	Index    int    `json:"index"`
	Tag      string `json:"tag"`
	Selector string `json:"selector"`
	Role     string `json:"role"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Text     string `json:"text"`
	Value    string `json:"value"`
	Disabled bool   `json:"disabled"`
	Checked  *bool  `json:"checked"`
}

// LoginForm is one detected login form. It carries selectors and the page
// location only; no credential ever enters it.
type LoginForm struct {
	PageID           string
	Mode             string // "modal" when the form lives in a dialog
	LoginURL         string
	Route            string
	Title            string
	UsernameSelector string
	PasswordSelector string
	SubmitSelector   string
}

// SessionFact is the outcome of Execute. Status is SessionActive on success or
// SessionBlocked on failure; a blocked fact carries Action and Reason. Recipe is
// the credential-free login recipe (selectors/steps) that a knowledge hook may
// sediment as login_recipe.
type SessionFact struct {
	VersionID       int64
	VersionSUTID    int64
	EnvironmentID   int64
	AccountRole     string
	AccountID       int64
	Status          string
	StorageStateRef string
	LoginURL        string
	Route           string
	Reused          bool
	Action          string
	Reason          string
	Form            LoginForm
	Recipe          map[string]any
	FactID          int64
}

// BlockedError reports a login page that cannot be detected or driven. Detect
// returns it so the caller can surface the same reason/action pair as a blocked
// SessionFact.
type BlockedError struct {
	Reason string
	Action string
	PageID string
}

// Error implements error.
func (e *BlockedError) Error() string {
	return fmt.Sprintf("login blocked: %s (action %s)", e.Reason, e.Action)
}

// Executor runs one explore action and returns the executor's result object.
// The production adapter posts to /v1/explore/actions; tests use a fake.
type Executor interface {
	Action(ctx context.Context, action string, args map[string]any) (map[string]any, error)
}

// ExecutorActionError is a typed executor explore action failure (page not
// found, timeout, host not allowed, ...). It is not a transport error.
type ExecutorActionError struct {
	Code    string
	Message string
}

// Error implements error.
func (e *ExecutorActionError) Error() string {
	return fmt.Sprintf("executor action failed: %s: %s", e.Code, e.Message)
}

// HTTPExecutor is the production Executor adapter for the executor protocol
// v1.3 /v1/explore/actions route. The bearer token is only sent in the
// Authorization header; redirects are not followed and bodies are never logged.
type HTTPExecutor struct {
	endpoint string
	token    string
	client   *http.Client
}

// NewHTTPExecutor returns an explore action client. A non-positive timeout falls
// back to defaultExecutorTimeout.
func NewHTTPExecutor(endpoint, token string, timeout time.Duration) *HTTPExecutor {
	if timeout <= 0 {
		timeout = defaultExecutorTimeout
	}
	return &HTTPExecutor{
		endpoint: strings.TrimRight(endpoint, "/"),
		token:    token,
		client: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Action runs one explore action. An ok:false envelope becomes an
// *ExecutorActionError so callers can distinguish an SUT/browser failure from a
// transport failure.
func (e *HTTPExecutor) Action(ctx context.Context, action string, args map[string]any) (map[string]any, error) {
	if args == nil {
		args = map[string]any{}
	}
	payload, err := json.Marshal(map[string]any{"action": action, "args": args})
	if err != nil {
		return nil, fmt.Errorf("encode executor explore action %s: %w", action, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint+ExploreActionsPath, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build executor explore request: %w", err)
	}
	request.Header.Set("content-type", "application/json")
	if e.token != "" {
		request.Header.Set("authorization", "Bearer "+e.token)
	}
	response, err := e.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("executor explore action %s: %w", action, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("read executor explore response: %w", err)
	}
	var envelope struct {
		OK     bool           `json:"ok"`
		Result map[string]any `json:"result"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode executor explore response (HTTP %d): %w", response.StatusCode, err)
	}
	if !envelope.OK {
		if envelope.Error != nil && envelope.Error.Code != "" {
			return nil, &ExecutorActionError{Code: envelope.Error.Code, Message: envelope.Error.Message}
		}
		return nil, fmt.Errorf("executor explore action %s failed with HTTP %d", action, response.StatusCode)
	}
	return envelope.Result, nil
}

// AccountStore resolves account credentials. The resource Service implements
// AccountSecret; the password is handed only to fill(secret=true).
type AccountStore interface {
	AccountSecret(ctx context.Context, accountID int64) (username, password string, err error)
}

// FactRecorder appends version facts (versionapp.FactService implements it).
type FactRecorder interface {
	Record(ctx context.Context, fact version.Fact) (version.Fact, bool, error)
}

// SessionStore caches captured storage states by (version SUT, environment,
// role). resourceapp.SessionStore implements it.
type SessionStore interface {
	Find(ctx context.Context, versionSUTID, environmentID int64, role string) (resourceapp.LoginSession, bool)
	Save(ctx context.Context, session resourceapp.LoginSession) error
	Invalidate(ctx context.Context, versionSUTID, environmentID int64, role string)
}

// LoginFlowConfig binds one flow to a version SUT and account role. LoginURL is
// the fallback page opened when Execute runs without a preceding Detect.
type LoginFlowConfig struct {
	VersionID     int64
	VersionSUTID  int64
	EnvironmentID int64
	Role          string
	LoginURL      string
	AllowedHosts  []string
}

// LoginFlowDeps are the narrow ports consumed by the flow. Facts may be nil (a
// degraded assembly still logs in); Sessions must be set.
type LoginFlowDeps struct {
	Executor Executor
	Accounts AccountStore
	Sessions SessionStore
	Facts    FactRecorder
	Now      func() time.Time
}

// LoginFlow is one stateful autonomous login attempt. Detect records the
// detected form, Execute logs in (or reuses a session) and produces the fact.
type LoginFlow struct {
	cfg   LoginFlowConfig
	exec  Executor
	accts AccountStore
	sess  SessionStore
	facts FactRecorder
	now   func() time.Time

	form *LoginForm
	page Page
}

// NewLoginFlow wires one autonomous login flow.
func NewLoginFlow(cfg LoginFlowConfig, deps LoginFlowDeps) *LoginFlow {
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return &LoginFlow{
		cfg:   cfg,
		exec:  deps.Executor,
		accts: deps.Accounts,
		sess:  deps.Sessions,
		facts: deps.Facts,
		now:   now,
	}
}

/* ------------------------------- detection -------------------------------- */

var (
	usernameHints = regexp.MustCompile(`(?i)user|login|email|account|phone|账号|用户名|邮箱|手机号`)
	submitHints   = regexp.MustCompile(`(?i)\blog\s*-?\s*in\b|\bsign\s*-?\s*in\b|submit|登录|登陆|提交`)
	modalHints    = regexp.MustCompile(`(?i)\bdialog\b|\bmodal\b|弹窗`)
	loginHints    = regexp.MustCompile(`(?i)log\s*-?\s*in|sign\s*-?\s*in|登录|登陆`)
	credentialErr = regexp.MustCompile(`(?i)invalid|incorrect|wrong|denied|failed|error|失败|错误|不正确|密码错|账号或密码|锁定`)
	logoutHints   = regexp.MustCompile(`(?i)log\s*-?\s*out|sign\s*-?\s*out|logout|退出`)
)

// Detect reads the current page snapshot, finds the username/password/submit
// selectors and remembers the form for Execute. A page without a password field
// returns a *BlockedError with a recovery action.
func (f *LoginFlow) Detect(ctx context.Context, page Page) (LoginForm, error) {
	if f.exec == nil {
		return LoginForm{}, errors.New("login flow has no executor")
	}
	if strings.TrimSpace(page.ID) == "" {
		return LoginForm{}, errors.New("login flow requires a page id")
	}
	result, err := f.exec.Action(ctx, actionSnapshot, map[string]any{"page_id": page.ID})
	if err != nil {
		return LoginForm{}, fmt.Errorf("snapshot login page: %w", err)
	}
	snapshot, err := decodePageSnapshot(result)
	if err != nil {
		return LoginForm{}, err
	}
	if strings.TrimSpace(snapshot.PageID) != "" {
		page.ID = snapshot.PageID
	}
	if page.URL == "" {
		page.URL = snapshot.URL
	}
	form, blocked := detectLoginForm(snapshot, page)
	if blocked != nil {
		return LoginForm{}, blocked
	}
	f.form = &form
	f.page = page
	return form, nil
}

// Execute logs in with account and returns the resulting session fact. A valid
// cached storage state is reused without any executor call or credential read.
// A rejected login produces a blocked fact with a recovery action and a nil
// error; only infrastructure failures return an error.
func (f *LoginFlow) Execute(ctx context.Context, account Account) (SessionFact, error) {
	if f == nil || f.exec == nil {
		return SessionFact{}, errors.New("login flow has no executor")
	}
	if f.sess == nil {
		return SessionFact{}, errors.New("login flow has no session store")
	}
	if f.accts == nil {
		return SessionFact{}, errors.New("login flow has no account store")
	}
	role := strings.TrimSpace(account.Role)
	if role == "" {
		role = strings.TrimSpace(f.cfg.Role)
	}
	if role == "" {
		return SessionFact{}, errors.New("login flow requires an account role")
	}
	if account.ID <= 0 {
		return SessionFact{}, errors.New("login flow requires a positive account id")
	}

	// 1. Reuse a still-valid session: no login form, no credential read.
	if session, ok := f.sess.Find(ctx, f.cfg.VersionSUTID, f.cfg.EnvironmentID, role); ok && session.StorageStateRef != "" {
		fact := f.sessionFact(account, role, session.StorageStateRef, true)
		fact.LoginURL = session.LoginURL
		fact.Route = session.Route
		fact.Recipe = session.Recipe
		if err := f.record(ctx, &fact); err != nil {
			return SessionFact{}, err
		}
		return fact, nil
	}

	// 2. Credentials enter through the account store only.
	username, password, err := f.accts.AccountSecret(ctx, account.ID)
	if err != nil || username == "" || password == "" {
		return f.blocked(account, role, "account_secret_unavailable", ActionConfigureAccount), nil
	}

	form := f.form
	if form == nil {
		opened, openErr := f.openLoginPage(ctx)
		if openErr != nil {
			return f.blocked(account, role, "login_page_unavailable", ActionRetryLogin), nil
		}
		detected, blocked := detectLoginForm(opened, Page{ID: opened.PageID, URL: opened.URL})
		if blocked != nil {
			return f.blocked(account, role, blocked.Reason, blocked.Action), nil
		}
		form = &detected
		f.form = form
		f.page = Page{ID: detected.PageID, URL: detected.LoginURL}
	}
	if form.SubmitSelector == "" {
		return f.blocked(account, role, "login_submit_not_found", ActionManualLogin), nil
	}

	// 3. Drive the form. The password is flagged secret so every executor
	// output/error is redacted before it reaches the caller.
	pageID := form.PageID
	if pageID == "" {
		pageID = f.page.ID
	}
	if _, err := f.exec.Action(ctx, actionFill, map[string]any{
		"page_id": pageID, "selector": form.UsernameSelector, "value": username,
	}); err != nil {
		return f.blocked(account, role, "login_fill_failed", ActionRetryLogin), nil
	}
	if _, err := f.exec.Action(ctx, actionFill, map[string]any{
		"page_id": pageID, "selector": form.PasswordSelector, "value": password, "secret": true,
	}); err != nil {
		return f.blocked(account, role, "login_fill_failed", ActionRetryLogin), nil
	}
	if _, err := f.exec.Action(ctx, actionClick, map[string]any{
		"page_id": pageID, "selector": form.SubmitSelector,
	}); err != nil {
		return f.blocked(account, role, "login_submit_failed", ActionRetryLogin), nil
	}

	// 4. Verify and capture the session in one snapshot.
	result, err := f.exec.Action(ctx, actionSnapshot, map[string]any{
		"page_id": pageID, "include_storage_state": true,
	})
	if err != nil {
		return f.blocked(account, role, "login_verification_failed", ActionRetryLogin), nil
	}
	snapshot, err := decodePageSnapshot(result)
	if err != nil {
		return SessionFact{}, err
	}
	if loginStillVisible(snapshot) {
		f.sess.Invalidate(ctx, f.cfg.VersionSUTID, f.cfg.EnvironmentID, role)
		reason := "login_rejected"
		if credentialErr.MatchString(snapshot.DOM.ARIA) || credentialErr.MatchString(errorText(snapshot.DOM.Elements)) {
			reason = "invalid_credentials"
		}
		return f.blocked(account, role, reason, ActionFixCredentials), nil
	}

	ref := strings.TrimSpace(snapshot.StorageStateRef)
	state := snapshot.StorageState
	if ref == "" && len(state) > 0 {
		ref = storageStateRef(state)
	}
	if ref == "" {
		return f.blocked(account, role, "storage_state_unavailable", ActionManualLogin), nil
	}
	recipe := loginRecipe(*form)
	session := resourceapp.LoginSession{
		VersionID:       f.cfg.VersionID,
		VersionSUTID:    f.cfg.VersionSUTID,
		EnvironmentID:   f.cfg.EnvironmentID,
		AccountRole:     role,
		StorageStateRef: ref,
		StorageState:    state,
		LoginURL:        form.LoginURL,
		Route:           form.Route,
		Recipe:          recipe,
		VerifiedAt:      f.now(),
	}
	if err := f.sess.Save(ctx, session); err != nil {
		return SessionFact{}, fmt.Errorf("save login session: %w", err)
	}

	fact := f.sessionFact(account, role, ref, false)
	fact.Form = *form
	fact.Recipe = recipe
	if err := f.record(ctx, &fact); err != nil {
		return SessionFact{}, err
	}
	return fact, nil
}

/* ------------------------------- internals -------------------------------- */

type pageSnapshot struct {
	PageID          string          `json:"page_id"`
	URL             string          `json:"url"`
	Route           string          `json:"route"`
	StorageStateRef string          `json:"storage_state_ref"`
	StorageState    json.RawMessage `json:"storage_state"`
	DOM             domSnapshot     `json:"dom_snapshot"`
}

type domSnapshot struct {
	Title    string    `json:"title"`
	ARIA     string    `json:"aria"`
	Route    string    `json:"route"`
	Elements []Element `json:"elements"`
}

func decodePageSnapshot(result map[string]any) (pageSnapshot, error) {
	if result == nil {
		return pageSnapshot{}, errors.New("executor returned an empty snapshot result")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return pageSnapshot{}, fmt.Errorf("encode page snapshot: %w", err)
	}
	var snapshot pageSnapshot
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		return pageSnapshot{}, fmt.Errorf("decode page snapshot: %w", err)
	}
	return snapshot, nil
}

func detectLoginForm(snapshot pageSnapshot, page Page) (LoginForm, *BlockedError) {
	elements := snapshot.DOM.Elements
	passwordIndex := -1
	for i, element := range elements {
		if element.Disabled || strings.TrimSpace(element.Selector) == "" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(element.Type), "password") {
			passwordIndex = i
			break
		}
	}
	if passwordIndex < 0 {
		return LoginForm{}, &BlockedError{
			Reason: "login_form_not_found",
			Action: ActionManualLogin,
			PageID: page.ID,
		}
	}

	usernameIndex := -1
	bestScore := -1
	for i := 0; i < passwordIndex; i++ {
		element := elements[i]
		if !isTextInput(element) {
			continue
		}
		if score := fieldScore(element, usernameHints); score > bestScore {
			bestScore = score
			usernameIndex = i
		}
	}
	if usernameIndex < 0 {
		for i, element := range elements {
			if i == passwordIndex || !isTextInput(element) {
				continue
			}
			if score := fieldScore(element, usernameHints); score > bestScore {
				bestScore = score
				usernameIndex = i
			}
		}
	}
	if usernameIndex < 0 {
		return LoginForm{}, &BlockedError{
			Reason: "login_username_not_found",
			Action: ActionManualLogin,
			PageID: page.ID,
		}
	}

	submitSelector := ""
	submitScore := -1
	for i := passwordIndex + 1; i < len(elements); i++ {
		element := elements[i]
		if element.Disabled || strings.TrimSpace(element.Selector) == "" || !isSubmitControl(element) {
			continue
		}
		if score := fieldScore(element, submitHints); score > submitScore {
			submitScore = score
			submitSelector = element.Selector
		}
	}
	if submitSelector == "" {
		for _, element := range elements {
			if element.Disabled || strings.TrimSpace(element.Selector) == "" || !isSubmitControl(element) {
				continue
			}
			if score := fieldScore(element, submitHints); score > submitScore {
				submitScore = score
				submitSelector = element.Selector
			}
		}
	}

	route := snapshot.DOM.Route
	if route == "" {
		route = snapshot.Route
	}
	loginURL := page.URL
	if loginURL == "" {
		loginURL = snapshot.URL
	}
	mode := "page"
	if modalHints.MatchString(snapshot.DOM.ARIA + " " + snapshot.DOM.Title) {
		mode = "modal"
	}
	return LoginForm{
		PageID:           page.ID,
		Mode:             mode,
		LoginURL:         loginURL,
		Route:            route,
		Title:            snapshot.DOM.Title,
		UsernameSelector: elements[usernameIndex].Selector,
		PasswordSelector: elements[passwordIndex].Selector,
		SubmitSelector:   submitSelector,
	}, nil
}

func isTextInput(element Element) bool {
	if strings.TrimSpace(element.Selector) == "" || element.Disabled {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(element.Tag), "input") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(element.Type)) {
	case "password", "hidden", "checkbox", "radio", "submit", "button", "file", "image":
		return false
	}
	return true
}

func isSubmitControl(element Element) bool {
	tag := strings.ToLower(strings.TrimSpace(element.Tag))
	if tag == "button" {
		return true
	}
	if tag != "input" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(element.Type)) {
	case "submit", "button", "image":
		return true
	}
	return false
}

func fieldScore(element Element, hints *regexp.Regexp) int {
	switch {
	case hints.MatchString(element.Name):
		return 4
	case hints.MatchString(element.Text):
		return 3
	case hints.MatchString(element.Role):
		return 2
	case hints.MatchString(element.Type):
		return 1
	default:
		return 0
	}
}

// loginStillVisible reports whether the post-submit page still looks like the
// login form (a rejected attempt). A visible password field is the primary
// signal; an error banner without any signed-in marker is the fallback.
func loginStillVisible(snapshot pageSnapshot) bool {
	for _, element := range snapshot.DOM.Elements {
		if element.Disabled {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(element.Type), "password") {
			return true
		}
	}
	if credentialErr.MatchString(snapshot.DOM.ARIA) && !logoutHints.MatchString(snapshot.DOM.ARIA) {
		return true
	}
	if credentialErr.MatchString(snapshot.DOM.Title) {
		return true
	}
	return false
}

func errorText(elements []Element) string {
	var builder strings.Builder
	for _, element := range elements {
		builder.WriteString(element.Name)
		builder.WriteString(" ")
		builder.WriteString(element.Text)
		builder.WriteString(" ")
	}
	return builder.String()
}

func storageStateRef(state json.RawMessage) string {
	var canonical any
	if err := json.Unmarshal(state, &canonical); err != nil {
		canonical = string(state)
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		encoded = state
	}
	sum := sha256.Sum256(encoded)
	return "ss_" + hex.EncodeToString(sum[:])[:16]
}

func loginRecipe(form LoginForm) map[string]any {
	steps := make([]any, 0, 4)
	steps = append(steps, map[string]any{"action": "open", "url": form.LoginURL})
	if form.UsernameSelector != "" {
		steps = append(steps, map[string]any{
			"action": "fill", "field": "username", "selector": form.UsernameSelector,
		})
	}
	if form.PasswordSelector != "" {
		steps = append(steps, map[string]any{
			"action": "fill", "field": "password", "selector": form.PasswordSelector,
		})
	}
	if form.SubmitSelector != "" {
		steps = append(steps, map[string]any{"action": "click", "selector": form.SubmitSelector})
	}
	return map[string]any{
		"mode":           form.Mode,
		"login_url":      form.LoginURL,
		"route":          form.Route,
		"username_field": form.UsernameSelector,
		"password_field": form.PasswordSelector,
		"submit":         form.SubmitSelector,
		"steps":          steps,
	}
}

func (f *LoginFlow) openLoginPage(ctx context.Context) (pageSnapshot, error) {
	if strings.TrimSpace(f.cfg.LoginURL) == "" {
		return pageSnapshot{}, errors.New("no login page is configured")
	}
	args := map[string]any{"url": f.cfg.LoginURL}
	if len(f.cfg.AllowedHosts) > 0 {
		args["allowed_hosts"] = append([]string(nil), f.cfg.AllowedHosts...)
	}
	result, err := f.exec.Action(ctx, actionOpen, args)
	if err != nil {
		return pageSnapshot{}, err
	}
	return decodePageSnapshot(result)
}

func (f *LoginFlow) sessionFact(account Account, role, ref string, reused bool) SessionFact {
	fact := SessionFact{
		VersionID:       f.cfg.VersionID,
		VersionSUTID:    f.cfg.VersionSUTID,
		EnvironmentID:   f.cfg.EnvironmentID,
		AccountRole:     role,
		AccountID:       account.ID,
		Status:          SessionActive,
		StorageStateRef: ref,
		Reused:          reused,
	}
	if f.form != nil {
		fact.Form = *f.form
		fact.LoginURL = f.form.LoginURL
		fact.Route = f.form.Route
	}
	return fact
}

func (f *LoginFlow) blocked(account Account, role, reason, action string) SessionFact {
	fact := f.sessionFact(account, role, "", false)
	fact.Status = SessionBlocked
	fact.Reason = reason
	fact.Action = action
	return fact
}

// record appends the login_session fact. Recording is part of the successful
// outcome; a recorder error is returned so a retry can complete the finding.
func (f *LoginFlow) record(ctx context.Context, fact *SessionFact) error {
	if f.facts == nil {
		return nil
	}
	confidence := 1.0
	stored, _, err := f.facts.Record(ctx, version.Fact{
		VersionID:  fact.VersionID,
		Kind:       KindLoginSession,
		Key:        fmt.Sprintf("vs:%d:role:%s", fact.VersionSUTID, fact.AccountRole),
		Payload:    loginFactPayload(*fact),
		Confidence: &confidence,
		Source:     "explorer",
		SourceRef:  fact.LoginURL,
	})
	if err != nil {
		return fmt.Errorf("record %s fact: %w", KindLoginSession, err)
	}
	fact.FactID = stored.ID
	return nil
}

// loginFactPayload is deterministic for a given storage state so recording the
// same reused session again is an idempotent append. It never contains a
// credential or the storageState body.
func loginFactPayload(fact SessionFact) map[string]any {
	recipe := fact.Recipe
	if recipe == nil {
		recipe = map[string]any{}
	}
	return map[string]any{
		"storage_state_ref": fact.StorageStateRef,
		"login_url":         fact.LoginURL,
		"route":             fact.Route,
		"account_role":      fact.AccountRole,
		"recipe":            recipe,
	}
}
