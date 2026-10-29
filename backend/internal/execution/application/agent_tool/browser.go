package agent_tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/application"
)

// targetSchema is the executor target object of design spec §5.6. Exactly one
// of role/text/test_id/selector may be present; role may carry name/exact.
const targetSchema = `{"type":"object","properties":{"role":{"type":"string"},"name":{"type":"string"},"exact":{"type":"boolean"},"text":{"type":"string"},"test_id":{"type":"string"},"selector":{"type":"string"}},"additionalProperties":false,"minProperties":1}`

// timeoutSchema is the shared action timeout property.
const timeoutSchema = `"timeout_ms":{"type":"integer","minimum":1,"maximum":60000}`

// browserDefinitions returns the ten browser tools of design spec §7.1 in the
// frozen order.
func browserDefinitions() []toolDefinition {
	return []toolDefinition{
		{
			name:        "browser.goto",
			description: "Navigate the case browser to an http(s) URL inside the environment base URL or allowed hosts. Returns the final URL and HTTP status.",
			schema:      `{"type":"object","properties":{"url":{"type":"string"},"wait_until":{"type":"string","enum":["load","domcontentloaded","networkidle","commit"]},` + timeoutSchema + `},"required":["url"],"additionalProperties":false}`,
			run:         runBrowserGoto,
		},
		{
			name:        "browser.snapshot",
			description: "Return the page's ARIA snapshot (read-only, truncated, password values stripped).",
			schema:      `{"type":"object","properties":{"max_chars":{"type":"integer","minimum":1,"maximum":12000}},"additionalProperties":false}`,
			run:         runBrowserSnapshot,
		},
		{
			name:        "browser.click",
			description: "Click one element resolved by role/text/test id/selector.",
			schema:      `{"type":"object","properties":{"target":` + targetSchema + `,"button":{"type":"string","enum":["left","right","middle"]},"click_count":{"type":"integer","minimum":1,"maximum":3}},"required":["target"],"additionalProperties":false}`,
			run:         runBrowserClick,
		},
		{
			name:        "browser.fill",
			description: "Fill one input. Set secret=true for password/credential fields: the value is never logged, returned or captured as evidence.",
			schema:      `{"type":"object","properties":{"target":` + targetSchema + `,"value":{"type":"string"},"secret":{"type":"boolean"}},"required":["target","value"],"additionalProperties":false}`,
			run:         runBrowserFill,
		},
		{
			name:        "browser.press",
			description: "Press one keyboard key, optionally focused on a target element.",
			schema:      `{"type":"object","properties":{"key":{"type":"string"},"target":` + targetSchema + `},"required":["key"],"additionalProperties":false}`,
			run:         runBrowserPress,
		},
		{
			name:        "browser.wait_for",
			description: "Wait until a target is attached/visible, text appears, or the URL contains a substring.",
			schema:      `{"type":"object","properties":{"target":` + targetSchema + `,"text":{"type":"string"},"url_contains":{"type":"string"},"state":{"type":"string","enum":["attached","detached","visible","hidden"]},` + timeoutSchema + `},"anyOf":[{"required":["target"]},{"required":["text"]},{"required":["url_contains"]}],"additionalProperties":false}`,
			run:         runBrowserWaitFor,
		},
		{
			name:        "browser.screenshot",
			description: "Capture a PNG screenshot as evidence. Returns only evidence_id, sha256 and size; the bytes never reach the model.",
			schema:      `{"type":"object","properties":{"full_page":{"type":"boolean"},"target":` + targetSchema + `},"additionalProperties":false}`,
			run:         runBrowserScreenshot,
		},
		{
			name:        "browser.console",
			description: "Return the page console tail (at most 200 items). Set capture_evidence=true to also persist it as console_log evidence.",
			schema:      `{"type":"object","properties":{"tail":{"type":"integer","minimum":1,"maximum":200},"capture_evidence":{"type":"boolean"}},"additionalProperties":false}`,
			run:         runBrowserConsole,
		},
		{
			name:        "browser.network",
			description: "Return the page network tail (at most 200 entries). Set capture_evidence=true to also persist it as network_log evidence.",
			schema:      `{"type":"object","properties":{"tail":{"type":"integer","minimum":1,"maximum":200},"capture_evidence":{"type":"boolean"}},"additionalProperties":false}`,
			run:         runBrowserNetwork,
		},
		{
			name:        "browser.download",
			description: "Trigger a download and store it as evidence. Returns only evidence_id, sha256 and size.",
			schema:      `{"type":"object","properties":{"target":` + targetSchema + `,` + timeoutSchema + `},"required":["target"],"additionalProperties":false}`,
			run:         runBrowserDownload,
		},
	}
}

// gotoInput is the validated argument shape of browser.goto.
type gotoInput struct {
	URL       string `json:"url"`
	WaitUntil string `json:"wait_until"`
	TimeoutMS int    `json:"timeout_ms"`
}

// runBrowserGoto executes browser.goto.
func runBrowserGoto(ctx context.Context, set *toolSet, args json.RawMessage) (agentdomain.ToolResult, error) {
	invocation, err := caseScope(ctx)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	input, err := decodeArguments[gotoInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if _, err := parseHTTPURL(input.URL); err != nil {
		return agentdomain.ToolResult{}, invalidArguments("url must be an absolute http(s) URL: " + err.Error())
	}
	if input.WaitUntil != "" && !oneOf(input.WaitUntil, "load", "domcontentloaded", "networkidle", "commit") {
		return agentdomain.ToolResult{}, invalidArguments("wait_until must be load, domcontentloaded, networkidle or commit")
	}
	if input.TimeoutMS < 0 || input.TimeoutMS > maxActionTimeoutMS {
		return agentdomain.ToolResult{}, invalidArguments("timeout_ms must be between 1 and 60000")
	}
	view, err := set.caseView(ctx, invocation.CaseRunID)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if !hostAllowed(view, input.URL) {
		return agentdomain.ToolResult{}, toolError("host_not_allowed",
			"url host is outside the environment base URL and allowed hosts")
	}
	contextRef, err := set.ensureCaseBrowser(ctx, invocation.CaseRunID, view)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	result, err := set.browserAction(ctx, contextRef, "goto", args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	return rawResult(result)
}

// snapshotInput is the validated argument shape of browser.snapshot.
type snapshotInput struct {
	MaxChars int `json:"max_chars"`
}

// runBrowserSnapshot executes browser.snapshot.
func runBrowserSnapshot(ctx context.Context, set *toolSet, args json.RawMessage) (agentdomain.ToolResult, error) {
	invocation, err := caseScope(ctx)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	input, err := decodeArguments[snapshotInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if input.MaxChars < 0 || input.MaxChars > 12000 {
		return agentdomain.ToolResult{}, invalidArguments("max_chars must be between 1 and 12000")
	}
	return set.runSimpleBrowserAction(ctx, invocation, "snapshot", args)
}

// clickInput is the validated argument shape of browser.click.
type clickInput struct {
	Target     json.RawMessage `json:"target"`
	Button     string          `json:"button"`
	ClickCount int             `json:"click_count"`
}

// runBrowserClick executes browser.click.
func runBrowserClick(ctx context.Context, set *toolSet, args json.RawMessage) (agentdomain.ToolResult, error) {
	invocation, err := caseScope(ctx)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	input, err := decodeArguments[clickInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if err := validateTarget(input.Target); err != nil {
		return agentdomain.ToolResult{}, err
	}
	if input.Button != "" && !oneOf(input.Button, "left", "right", "middle") {
		return agentdomain.ToolResult{}, invalidArguments("button must be left, right or middle")
	}
	if input.ClickCount < 0 || input.ClickCount > 3 {
		return agentdomain.ToolResult{}, invalidArguments("click_count must be between 1 and 3")
	}
	return set.runSimpleBrowserAction(ctx, invocation, "click", args)
}

// fillInput is the validated argument shape of browser.fill.
type fillInput struct {
	Target json.RawMessage `json:"target"`
	Value  string          `json:"value"`
	Secret bool            `json:"secret"`
}

// runBrowserFill executes browser.fill. The result is always the bare
// {"ok":true} envelope: a secret value can never be echoed back by a buggy
// executor, and a non-secret value is not useful to the model either.
func runBrowserFill(ctx context.Context, set *toolSet, args json.RawMessage) (agentdomain.ToolResult, error) {
	invocation, err := caseScope(ctx)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	input, err := decodeArguments[fillInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if err := validateTarget(input.Target); err != nil {
		return agentdomain.ToolResult{}, err
	}
	if _, err := set.runSimpleBrowserAction(ctx, invocation, "fill", args); err != nil {
		return agentdomain.ToolResult{}, err
	}
	return agentdomain.ToolResult{Content: `{"ok":true}`}, nil
}

// pressInput is the validated argument shape of browser.press.
type pressInput struct {
	Key    string          `json:"key"`
	Target json.RawMessage `json:"target"`
}

// runBrowserPress executes browser.press.
func runBrowserPress(ctx context.Context, set *toolSet, args json.RawMessage) (agentdomain.ToolResult, error) {
	invocation, err := caseScope(ctx)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	input, err := decodeArguments[pressInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if strings.TrimSpace(input.Key) == "" {
		return agentdomain.ToolResult{}, invalidArguments("key must not be empty")
	}
	if len(bytes.TrimSpace(input.Target)) > 0 && string(bytes.TrimSpace(input.Target)) != "null" {
		if err := validateTarget(input.Target); err != nil {
			return agentdomain.ToolResult{}, err
		}
	}
	return set.runSimpleBrowserAction(ctx, invocation, "press", args)
}

// waitForInput is the validated argument shape of browser.wait_for.
type waitForInput struct {
	Target      json.RawMessage `json:"target"`
	Text        string          `json:"text"`
	URLContains string          `json:"url_contains"`
	State       string          `json:"state"`
	TimeoutMS   int             `json:"timeout_ms"`
}

// runBrowserWaitFor executes browser.wait_for.
func runBrowserWaitFor(ctx context.Context, set *toolSet, args json.RawMessage) (agentdomain.ToolResult, error) {
	invocation, err := caseScope(ctx)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	input, err := decodeArguments[waitForInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	hasTarget := len(bytes.TrimSpace(input.Target)) > 0 && string(bytes.TrimSpace(input.Target)) != "null"
	if !hasTarget && strings.TrimSpace(input.Text) == "" && strings.TrimSpace(input.URLContains) == "" {
		return agentdomain.ToolResult{}, invalidArguments("wait_for needs target, text or url_contains")
	}
	if hasTarget {
		if err := validateTarget(input.Target); err != nil {
			return agentdomain.ToolResult{}, err
		}
	}
	if input.State != "" && !oneOf(input.State, "attached", "detached", "visible", "hidden") {
		return agentdomain.ToolResult{}, invalidArguments("state must be attached, detached, visible or hidden")
	}
	if input.TimeoutMS < 0 || input.TimeoutMS > maxActionTimeoutMS {
		return agentdomain.ToolResult{}, invalidArguments("timeout_ms must be between 1 and 60000")
	}
	return set.runSimpleBrowserAction(ctx, invocation, "wait_for", args)
}

// screenshotInput is the validated argument shape of browser.screenshot.
type screenshotInput struct {
	FullPage bool            `json:"full_page"`
	Target   json.RawMessage `json:"target"`
}

// runBrowserScreenshot executes browser.screenshot and persists the artifact.
func runBrowserScreenshot(ctx context.Context, set *toolSet, args json.RawMessage) (agentdomain.ToolResult, error) {
	invocation, err := caseScope(ctx)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	input, err := decodeArguments[screenshotInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if len(bytes.TrimSpace(input.Target)) > 0 && string(bytes.TrimSpace(input.Target)) != "null" {
		if err := validateTarget(input.Target); err != nil {
			return agentdomain.ToolResult{}, err
		}
	}
	view, contextRef, err := set.browserFor(ctx, invocation.CaseRunID)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	result, err := set.browserAction(ctx, contextRef, "screenshot", args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	artifact, err := firstArtifact(result)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	output, err := set.captureArtifact(ctx, invocation, view, artifact, "screenshot", "image/png", nil)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	return jsonResult(output)
}

// consoleInput is the shared argument shape of browser.console/network.
type consoleInput struct {
	Tail            int  `json:"tail"`
	CaptureEvidence bool `json:"capture_evidence"`
}

// runBrowserConsole executes browser.console.
func runBrowserConsole(ctx context.Context, set *toolSet, args json.RawMessage) (agentdomain.ToolResult, error) {
	return runLogTool(ctx, set, "console", "console_log", args)
}

// runBrowserNetwork executes browser.network.
func runBrowserNetwork(ctx context.Context, set *toolSet, args json.RawMessage) (agentdomain.ToolResult, error) {
	return runLogTool(ctx, set, "network", "network_log", args)
}

// runLogTool executes browser.console/network and optionally persists the tail
// as evidence.
func runLogTool(ctx context.Context, set *toolSet, action, evidenceKind string, args json.RawMessage) (agentdomain.ToolResult, error) {
	invocation, err := caseScope(ctx)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	input, err := decodeArguments[consoleInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if input.Tail < 0 || input.Tail > 200 {
		return agentdomain.ToolResult{}, invalidArguments("tail must be between 1 and 200")
	}
	view, contextRef, err := set.browserFor(ctx, invocation.CaseRunID)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	result, err := set.browserAction(ctx, contextRef, action, args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if !input.CaptureEvidence {
		return rawResult(result)
	}
	data := bytes.TrimSpace(result.Result)
	if len(data) == 0 || !json.Valid(data) {
		data = []byte("{}")
	}
	evidence, err := set.captureArtifact(ctx, invocation, view, application.Artifact{
		Kind: evidenceKind, Mime: "application/json", Data: data,
	}, evidenceKind, "application/json", nil)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	// The agent must be able to cite the log evidence it just produced, so the
	// result carries the evidence metadata alongside the console/network tail.
	return logResultWithEvidence(result, evidence)
}

// logResultWithEvidence merges the captured log evidence metadata into the
// console/network result object.
func logResultWithEvidence(result application.ActionResult, evidence evidenceOutput) (agentdomain.ToolResult, error) {
	output := map[string]any{}
	raw := bytes.TrimSpace(result.Result)
	if len(raw) > 0 && json.Valid(raw) {
		if err := json.Unmarshal(raw, &output); err != nil {
			output = map[string]any{"result": json.RawMessage(append([]byte(nil), raw...))}
		}
	}
	output["evidence_id"] = evidence.EvidenceID
	output["sha256"] = evidence.SHA256
	output["size"] = evidence.Size
	return jsonResult(output)
}

// downloadInput is the validated argument shape of browser.download.
type downloadInput struct {
	Target    json.RawMessage `json:"target"`
	TimeoutMS int             `json:"timeout_ms"`
}

// runBrowserDownload executes browser.download and persists the artifact.
func runBrowserDownload(ctx context.Context, set *toolSet, args json.RawMessage) (agentdomain.ToolResult, error) {
	invocation, err := caseScope(ctx)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	input, err := decodeArguments[downloadInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if err := validateTarget(input.Target); err != nil {
		return agentdomain.ToolResult{}, err
	}
	if input.TimeoutMS < 0 || input.TimeoutMS > maxActionTimeoutMS {
		return agentdomain.ToolResult{}, invalidArguments("timeout_ms must be between 1 and 60000")
	}
	view, contextRef, err := set.browserFor(ctx, invocation.CaseRunID)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	result, err := set.browserAction(ctx, contextRef, "download", args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	artifact, err := firstArtifact(result)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	mimeType := artifact.Mime
	if strings.TrimSpace(mimeType) == "" {
		mimeType = "application/octet-stream"
	}
	output, err := set.captureArtifact(ctx, invocation, view, artifact, "download", mimeType, nil)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	return jsonResult(output)
}

// runSimpleBrowserAction runs one action whose result is returned verbatim.
func (s *toolSet) runSimpleBrowserAction(ctx context.Context, invocation agentapp.Invocation, action string, args json.RawMessage) (agentdomain.ToolResult, error) {
	_, contextRef, err := s.browserFor(ctx, invocation.CaseRunID)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	result, err := s.browserAction(ctx, contextRef, action, args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	return rawResult(result)
}

// browserFor returns the case read model and its executor context, creating the
// context lazily on first use.
func (s *toolSet) browserFor(ctx context.Context, caseRunID int64) (application.CaseContextView, application.ContextRef, error) {
	view, err := s.caseView(ctx, caseRunID)
	if err != nil {
		return application.CaseContextView{}, application.ContextRef{}, err
	}
	contextRef, err := s.ensureCaseBrowser(ctx, caseRunID, view)
	if err != nil {
		return application.CaseContextView{}, application.ContextRef{}, err
	}
	return view, contextRef, nil
}

// caseView returns the cached case read model or reads it through the Recorder.
func (s *toolSet) caseView(ctx context.Context, caseRunID int64) (application.CaseContextView, error) {
	s.mu.Lock()
	cached, ok := s.contexts[caseRunID]
	s.mu.Unlock()
	if ok {
		return cached.view, nil
	}
	return loadCaseContext(ctx, s.recorder, caseRunID)
}

// ensureCaseBrowser creates and caches the executor context of one case run.
// The context is initialized from the environment target and the leased
// session storage state/sessionStorage (design spec §5.9).
func (s *toolSet) ensureCaseBrowser(ctx context.Context, caseRunID int64, view application.CaseContextView) (application.ContextRef, error) {
	s.mu.Lock()
	cached, ok := s.contexts[caseRunID]
	s.mu.Unlock()
	if ok {
		return cached.ref, nil
	}
	if s.browser == nil {
		return application.ContextRef{}, internalError("the browser executor is not configured")
	}

	spec := application.ContextSpec{
		Key:          fmt.Sprintf("case_run:%d", caseRunID),
		BaseURL:      view.BaseURL,
		AllowedHosts: append([]string(nil), view.AllowedHosts...),
	}
	if s.limits.Session != nil {
		session, err := s.limits.Session(ctx, caseRunID)
		if err != nil {
			return application.ContextRef{}, mapExecutionError(err)
		}
		spec.StorageState = session.StorageState
		spec.SessionStorage = decodeSessionStorage(session.SessionStorage)
	}
	ref, err := s.browser.NewContext(ctx, spec)
	if err != nil {
		return application.ContextRef{}, mapBrowserError(err)
	}

	s.mu.Lock()
	if existing, ok := s.contexts[caseRunID]; ok {
		s.mu.Unlock()
		// A concurrent call won the creation race; discard the extra context.
		_ = s.browser.Close(ctx, ref)
		return existing.ref, nil
	}
	s.contexts[caseRunID] = caseBrowser{ref: ref, view: view}
	s.mu.Unlock()
	return ref, nil
}

// browserAction runs one executor action and maps protocol-level failures.
func (s *toolSet) browserAction(ctx context.Context, ref application.ContextRef, action string, args json.RawMessage) (application.ActionResult, error) {
	if s.browser == nil {
		return application.ActionResult{}, internalError("the browser executor is not configured")
	}
	argsCopy := append(json.RawMessage(nil), args...)
	result, err := s.browser.Action(ctx, ref, application.Action{Name: action, Args: argsCopy})
	if err != nil {
		return application.ActionResult{}, mapBrowserError(err)
	}
	if result.ErrorCode != "" {
		return application.ActionResult{}, mapActionFailure(result)
	}
	return result, nil
}

// ToolSet implements the case runner's browser-context controller: the tools
// own the per-case context, and the runner acts on the registry through it.
var _ application.CaseBrowserController = (*toolSet)(nil)

// CancelCase implements application.CaseBrowserController. It aborts the
// in-flight action of the case run's cached context, if one exists, and keeps
// the context alive so a later graceful step or CloseCase still owns disposal.
func (s *toolSet) CancelCase(ctx context.Context, caseRunID int64) error {
	s.mu.Lock()
	cached, ok := s.contexts[caseRunID]
	s.mu.Unlock()
	if !ok || s.browser == nil {
		return nil
	}
	if err := s.browser.Cancel(ctx, cached.ref); err != nil {
		return mapBrowserError(err)
	}
	return nil
}

// CloseCase implements application.CaseBrowserController. It disposes the case
// run's cached context and forgets it so a later action can lazily open a fresh
// one; a missing context is a no-op and closing twice is safe.
func (s *toolSet) CloseCase(ctx context.Context, caseRunID int64) error {
	s.mu.Lock()
	cached, ok := s.contexts[caseRunID]
	if ok {
		delete(s.contexts, caseRunID)
	}
	// The tool-call budget and any terminal signal are per attempt: a fresh
	// attempt of the same case must not inherit them.
	delete(s.toolCalls, caseRunID)
	delete(s.failures, caseRunID)
	s.mu.Unlock()
	if !ok || s.browser == nil {
		return nil
	}
	if err := s.browser.Close(ctx, cached.ref); err != nil {
		return mapBrowserError(err)
	}
	return nil
}

// closeCaseBrowser disposes the cached executor context of one case run
// best-effort; a successful case_finish must not fail because cleanup did.
func (s *toolSet) closeCaseBrowser(ctx context.Context, caseRunID int64) {
	_ = s.CloseCase(ctx, caseRunID)
}

// firstArtifact returns the single artifact an artifact-producing action must
// yield.
func firstArtifact(result application.ActionResult) (application.Artifact, error) {
	if len(result.Artifacts) == 0 {
		return application.Artifact{}, internalError("the executor returned no artifact")
	}
	return result.Artifacts[0], nil
}

// validateTarget enforces the executor target rule: exactly one of role, text,
// test_id or selector, and name/exact only with role.
func validateTarget(raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return invalidArguments("target is required")
	}
	var target struct {
		Role     *string `json:"role"`
		Name     *string `json:"name"`
		Exact    *bool   `json:"exact"`
		Text     *string `json:"text"`
		TestID   *string `json:"test_id"`
		Selector *string `json:"selector"`
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&target); err != nil {
		return invalidArguments("invalid target: " + err.Error())
	}
	families := 0
	if target.Role != nil {
		families++
		if strings.TrimSpace(*target.Role) == "" {
			return invalidArguments("target.role must not be empty")
		}
		if target.Name == nil || strings.TrimSpace(*target.Name) == "" {
			return invalidArguments("target.role requires target.name")
		}
	}
	if target.Text != nil {
		families++
		if strings.TrimSpace(*target.Text) == "" {
			return invalidArguments("target.text must not be empty")
		}
	}
	if target.TestID != nil {
		families++
		if strings.TrimSpace(*target.TestID) == "" {
			return invalidArguments("target.test_id must not be empty")
		}
	}
	if target.Selector != nil {
		families++
		if strings.TrimSpace(*target.Selector) == "" {
			return invalidArguments("target.selector must not be empty")
		}
	}
	if families != 1 {
		return invalidArguments("target must have exactly one of role, text, test_id or selector")
	}
	if target.Name != nil && target.Role == nil {
		return invalidArguments("target.name is only valid with target.role")
	}
	if target.Exact != nil && target.Role == nil {
		return invalidArguments("target.exact is only valid with target.role")
	}
	return nil
}

// oneOf reports whether value is one of the allowed strings.
func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
