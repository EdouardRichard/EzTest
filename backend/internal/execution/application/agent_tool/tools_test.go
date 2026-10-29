package agent_tool

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
	"net/http/httptest"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/testutil"
)

// fakeRecorder is an in-memory execution Recorder. Every call records the
// submission and returns the scripted row or error.
type fakeRecorder struct {
	view    application.CaseContextView
	viewErr error

	stepIn  application.StepRecordInput
	step    executiondomain.StepResult
	stepErr error

	assertionIn  application.AssertionRecordInput
	assertion    executiondomain.AssertionResult
	assertionErr error

	finishIn  application.FinishInput
	finish    executiondomain.CaseRun
	finishErr error

	dbAssertionID int64
	dbLimits      application.DBAssertLimits
	dbOutcome     application.DBAssertOutcome
	dbErr         error

	visualAssertionID int64
	visualOptions     application.VisualAssertOptions
	visualOutcome     application.VisualAssertOutcome
	visualErr         error
}

func (f *fakeRecorder) CaseContext(_ context.Context, _ int64) (application.CaseContextView, error) {
	if f.viewErr != nil {
		return application.CaseContextView{}, f.viewErr
	}
	return f.view, nil
}

func (f *fakeRecorder) RecordStep(_ context.Context, _ int64, in application.StepRecordInput) (executiondomain.StepResult, error) {
	f.stepIn = in
	return f.step, f.stepErr
}

func (f *fakeRecorder) RecordAssertion(_ context.Context, _ int64, in application.AssertionRecordInput) (executiondomain.AssertionResult, error) {
	f.assertionIn = in
	return f.assertion, f.assertionErr
}

func (f *fakeRecorder) FinishCase(_ context.Context, _ int64, in application.FinishInput) (executiondomain.CaseRun, error) {
	f.finishIn = in
	return f.finish, f.finishErr
}

func (f *fakeRecorder) DBAssert(_ context.Context, _ int64, assertionID int64, limits application.DBAssertLimits) (application.DBAssertOutcome, error) {
	f.dbAssertionID = assertionID
	f.dbLimits = limits
	return f.dbOutcome, f.dbErr
}

func (f *fakeRecorder) VisualAssert(_ context.Context, _ int64, assertionID int64, opts application.VisualAssertOptions) (application.VisualAssertOutcome, error) {
	f.visualAssertionID = assertionID
	f.visualOptions = opts
	return f.visualOutcome, f.visualErr
}

// fakeEvidence records captured artifacts and returns a deterministic row.
type fakeEvidence struct {
	inputs []application.CaptureInput
	err    error
	nextID int64
}

func (f *fakeEvidence) Capture(_ context.Context, in application.CaptureInput) (executiondomain.Evidence, error) {
	f.inputs = append(f.inputs, in)
	if f.err != nil {
		return executiondomain.Evidence{}, f.err
	}
	f.nextID++
	return executiondomain.Evidence{
		ID:        f.nextID,
		BatchID:   in.BatchID,
		CaseRunID: &in.CaseRunID,
		Kind:      in.Kind,
		SHA256:    sha256Hex(in.Data),
		SizeBytes: int64(len(in.Data)),
		Mime:      in.Mime,
		Metadata:  in.Metadata,
	}, nil
}

// testInvocation is the run scope the P1 tool executor attaches to every call.
var testInvocation = agentapp.Invocation{
	SessionID:  40,
	RunID:      41,
	ProfileKey: "execution-agent",
	BatchID:    11,
	CaseRunID:  22,
}

// defaultView is a runnable case context: one passed step, one assertion and a
// target allowlist containing the base URL host.
func defaultView() application.CaseContextView {
	return application.CaseContextView{
		CaseRun: executiondomain.CaseRun{
			ID:         22,
			BatchID:    11,
			VersionID:  7,
			CaseNodeID: 33,
			Attempt:    1,
			Status:     executiondomain.CaseRunStatusRunning,
			Snapshot: executiondomain.CaseSnapshot{
				Node: executiondomain.CaseSnapshotNode{ID: 33, Title: "登录", Priority: "P0"},
				Steps: []executiondomain.CaseSnapshotStep{
					{Seq: 1, Action: "打开登录页", Expected: "显示登录表单"},
					{Seq: 2, Action: "输入账号密码", Expected: "登录成功"},
				},
				Assertions: []executiondomain.CaseSnapshotAssertion{
					{ID: 77, StepSeq: 2, Kind: "ui", Target: "首页", Expected: "显示欢迎语"},
				},
			},
		},
		CompletedSeqs: []int{1},
		BaseURL:       "https://app.example.test",
		AllowedHosts:  []string{"app.example.test"},
	}
}

// callTool executes a tool under testInvocation.
func callTool(t *testing.T, tools []agentdomain.Tool, name, args string) (agentdomain.ToolResult, error) {
	t.Helper()
	return callToolWithInvocation(t, tools, name, args, testInvocation)
}

// callToolWithInvocation executes a tool under the given invocation.
func callToolWithInvocation(t *testing.T, tools []agentdomain.Tool, name, args string, invocation agentapp.Invocation) (agentdomain.ToolResult, error) {
	t.Helper()
	for _, tool := range tools {
		if tool.Name() != name {
			continue
		}
		ctx := agentapp.WithToolInvocation(context.Background(), invocation)
		return tool.Execute(ctx, json.RawMessage(args))
	}
	t.Fatalf("tool %q is not registered", name)
	return agentdomain.ToolResult{}, nil
}

// requireToolError asserts that err is a *domain.ToolError with code.
func requireToolError(t *testing.T, err error, code string) *agentdomain.ToolError {
	t.Helper()
	var toolErr *agentdomain.ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("error=%v want *domain.ToolError with code %q", err, code)
	}
	if toolErr.Code != code {
		t.Fatalf("tool error code=%q want %q (%v)", toolErr.Code, code, err)
	}
	return toolErr
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// int64Pointer returns a pointer to a copy of value.
func int64Pointer(value int64) *int64 { return &value }

// TestToolsMatchExecutionProfileAllowlist is the registry contract: the tool
// names of spec §7.1 exist exactly once, every schema is a JSON object, and the
// execution-agent allowlist names only registered tools.
func TestToolsMatchExecutionProfileAllowlist(t *testing.T) {
	tools := New(&fakeRecorder{}, testutil.NewFakeBrowserExecutor(), &fakeEvidence{}, nil, Limits{})

	want := []string{
		"execution.case_context",
		"browser.goto",
		"browser.snapshot",
		"browser.click",
		"browser.fill",
		"browser.press",
		"browser.wait_for",
		"browser.screenshot",
		"browser.console",
		"browser.network",
		"browser.download",
		"http.request",
		"execution.step_record",
		"execution.assertion_record",
		"execution.case_finish",
		"execution.db_assert",
		"execution.visual_assert",
	}
	if len(tools) != len(want) {
		t.Fatalf("registered %d tools, want %d: %v", len(tools), len(want), toolNames(tools))
	}
	registered := make(map[string]struct{}, len(tools))
	for index, tool := range tools {
		if tool.Name() != want[index] {
			t.Fatalf("tools[%d]=%q want %q", index, tool.Name(), want[index])
		}
		if tool.Description() == "" {
			t.Fatalf("tool %q has an empty description", tool.Name())
		}
		if !json.Valid(tool.InputSchema()) {
			t.Fatalf("tool %q schema is not valid JSON: %s", tool.Name(), tool.InputSchema())
		}
		var schema map[string]any
		if err := json.Unmarshal(tool.InputSchema(), &schema); err != nil {
			t.Fatalf("tool %q schema is not an object: %v", tool.Name(), err)
		}
		if schema["type"] != "object" {
			t.Fatalf("tool %q schema type=%v want object", tool.Name(), schema["type"])
		}
		registered[tool.Name()] = struct{}{}
	}

	profile := executiondomain.ExecutionProfile()
	if len(profile.AllowedTools) != len(want) {
		t.Fatalf("profile allows %d tools, want %d", len(profile.AllowedTools), len(want))
	}
	for _, name := range profile.AllowedTools {
		if _, ok := registered[name]; !ok {
			t.Fatalf("profile allows %q, which is not registered", name)
		}
	}
}

func toolNames(tools []agentdomain.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name())
	}
	return names
}

// TestBrowserScreenshotReturnsEvidenceMetadataOnly pins the out-of-band artifact
// contract: the model sees the evidence id/hash/size, never the artifact bytes.
func TestBrowserScreenshotReturnsEvidenceMetadataOnly(t *testing.T) {
	recorder := &fakeRecorder{view: defaultView()}
	browser := testutil.NewFakeBrowserExecutor()
	data := []byte("fake-png-bytes")
	browser.ScriptAction(application.ActionResult{
		Result: json.RawMessage(`{"ok":true}`),
		Artifacts: []application.Artifact{{
			Kind: "screenshot", Mime: "image/png", Data: data, SHA256: sha256Hex(data),
		}},
	})
	evidence := &fakeEvidence{}
	tools := New(recorder, browser, evidence, nil, Limits{})

	out, err := callTool(t, tools, "browser.screenshot", `{}`)
	if err != nil {
		t.Fatalf("browser.screenshot: %v", err)
	}
	if strings.Contains(out.Content, "base64") || strings.Contains(out.Content, string(data)) {
		t.Fatalf("artifact bytes must stay out of the tool result: %s", out.Content)
	}
	var payload struct {
		EvidenceID string `json:"evidence_id"`
		SHA256     string `json:"sha256"`
		Size       int64  `json:"size"`
	}
	if err := json.Unmarshal([]byte(out.Content), &payload); err != nil {
		t.Fatalf("tool result is not JSON: %v (%s)", err, out.Content)
	}
	if payload.EvidenceID != "1" {
		t.Fatalf("evidence_id=%q want 1", payload.EvidenceID)
	}
	if payload.SHA256 != sha256Hex(data) || payload.Size != int64(len(data)) {
		t.Fatalf("hash/size=%q/%d want the captured artifact", payload.SHA256, payload.Size)
	}
	if len(evidence.inputs) != 1 {
		t.Fatalf("captured %d artifacts, want 1", len(evidence.inputs))
	}
	captured := evidence.inputs[0]
	if captured.Kind != "screenshot" || captured.Mime != "image/png" ||
		captured.BatchID != 11 || captured.CaseRunID != 22 || !bytes.Equal(captured.Data, data) {
		t.Fatalf("capture input=%+v", captured)
	}

	contexts := browser.Contexts()
	if len(contexts) != 1 {
		t.Fatalf("created %d executor contexts, want 1", len(contexts))
	}
	for _, spec := range contexts {
		if spec.Key != "case_run:22" || spec.BaseURL != "https://app.example.test" ||
			len(spec.AllowedHosts) != 1 || spec.AllowedHosts[0] != "app.example.test" {
			t.Fatalf("executor context spec=%+v", spec)
		}
	}
}

// TestBrowserDownloadCapturesDownloadEvidence pins the download artifact path.
func TestBrowserDownloadCapturesDownloadEvidence(t *testing.T) {
	recorder := &fakeRecorder{view: defaultView()}
	browser := testutil.NewFakeBrowserExecutor()
	data := []byte("file-bytes")
	browser.ScriptAction(application.ActionResult{Artifacts: []application.Artifact{{
		Kind: "download", Mime: "application/octet-stream", Filename: "export.csv", Data: data, SHA256: sha256Hex(data),
	}}})
	evidence := &fakeEvidence{}
	tools := New(recorder, browser, evidence, nil, Limits{})

	out, err := callTool(t, tools, "browser.download", `{"target":{"text":"导出"}}`)
	if err != nil {
		t.Fatalf("browser.download: %v", err)
	}
	if strings.Contains(out.Content, "file-bytes") {
		t.Fatalf("download bytes must stay out of the tool result: %s", out.Content)
	}
	if len(evidence.inputs) != 1 || evidence.inputs[0].Kind != "download" {
		t.Fatalf("capture input=%+v", evidence.inputs)
	}
	if evidence.inputs[0].Mime != "application/octet-stream" || evidence.inputs[0].Metadata["filename"] != "export.csv" {
		t.Fatalf("download metadata=%+v", evidence.inputs[0])
	}
}

// TestBrowserContextReusesOneContextAndInjectsSession pins the per-case context
// cache and the storage-state/sessionStorage injection from the leased session.
func TestBrowserContextReusesOneContextAndInjectsSession(t *testing.T) {
	recorder := &fakeRecorder{view: defaultView()}
	browser := testutil.NewFakeBrowserExecutor()
	session := application.SessionRef{
		SessionID:      5,
		StorageState:   json.RawMessage(`{"cookies":[{"name":"c","value":"v"}],"origins":[]}`),
		SessionStorage: json.RawMessage(`{"origins":[{"origin":"https://app.example.test","sessionStorage":[{"name":"sid","value":"abc"}]}]}`),
		Headers:        map[string]string{"Authorization": "Bearer token"},
	}
	limits := Limits{Session: func(context.Context, int64) (application.SessionRef, error) {
		return session, nil
	}}
	tools := New(recorder, browser, &fakeEvidence{}, nil, limits)

	if _, err := callTool(t, tools, "browser.goto", `{"url":"https://app.example.test/login"}`); err != nil {
		t.Fatalf("first goto: %v", err)
	}
	if _, err := callTool(t, tools, "browser.goto", `{"url":"https://app.example.test/home"}`); err != nil {
		t.Fatalf("second goto: %v", err)
	}

	contexts := browser.Contexts()
	if len(contexts) != 1 {
		t.Fatalf("created %d contexts, want one reused context", len(contexts))
	}
	for _, spec := range contexts {
		if !strings.Contains(string(spec.StorageState), `"name":"c"`) {
			t.Fatalf("context storage state=%s", spec.StorageState)
		}
		raw := spec.SessionStorage["https://app.example.test"]
		if len(raw) == 0 || !strings.Contains(string(raw), `"sid"`) {
			t.Fatalf("context session storage=%v", spec.SessionStorage)
		}
	}
	actions := 0
	for _, call := range browser.Calls() {
		if call.Kind == testutil.FakeCallAction {
			actions++
		}
	}
	if actions != 2 {
		t.Fatalf("executed %d actions, want 2", actions)
	}
}

// TestBrowserContextUsesVisualViewportAndDeviceScaleFactor pins the §9.3
// environment contract: Playwright fixes the viewport and device scale factor at
// context creation, so a case with a visual assertion must create its context
// with that assertion's configuration (not the executor default). Without a
// visual assertion the executor default is kept.
func TestBrowserContextUsesVisualViewportAndDeviceScaleFactor(t *testing.T) {
	view := visualView(int64Pointer(501))
	visual := view.CaseRun.Snapshot.Assertions[len(view.CaseRun.Snapshot.Assertions)-1].Visual
	visual.ViewportWidth, visual.ViewportHeight = 1440, 900
	visual.DeviceScaleFactor = 2
	browser := testutil.NewFakeBrowserExecutor()
	tools := New(&fakeRecorder{view: view}, browser, &fakeEvidence{}, nil, Limits{})

	if _, err := callTool(t, tools, "browser.goto", `{"url":"https://app.example.test/login"}`); err != nil {
		t.Fatalf("browser.goto: %v", err)
	}
	contexts := browser.Contexts()
	if len(contexts) != 1 {
		t.Fatalf("created %d contexts, want 1", len(contexts))
	}
	for _, spec := range contexts {
		if spec.Viewport.Width != 1440 || spec.Viewport.Height != 900 {
			t.Fatalf("context viewport = %+v, want 1440x900", spec.Viewport)
		}
		if spec.DeviceScaleFactor != 2 {
			t.Fatalf("context device scale factor = %v, want 2", spec.DeviceScaleFactor)
		}
	}

	// A case without a visual assertion keeps the executor defaults.
	plainBrowser := testutil.NewFakeBrowserExecutor()
	plain := New(&fakeRecorder{view: defaultView()}, plainBrowser, &fakeEvidence{}, nil, Limits{})
	if _, err := callTool(t, plain, "browser.goto", `{"url":"https://app.example.test/login"}`); err != nil {
		t.Fatalf("browser.goto (no visual): %v", err)
	}
	for _, spec := range plainBrowser.Contexts() {
		if spec.Viewport.Width != 0 || spec.Viewport.Height != 0 || spec.DeviceScaleFactor != 0 {
			t.Fatalf("context without visual assertion overrode the environment: %+v", spec)
		}
	}
}

// TestCaseContextRedactsSensitiveDataAndIncludesCheckpoint pins the redaction of
// test_data/preconditions and the resume checkpoint fields of the read model.
func TestCaseContextRedactsSensitiveDataAndIncludesCheckpoint(t *testing.T) {
	view := defaultView()
	view.CaseRun.Snapshot.Node.TestData = map[string]any{
		"username": "alice",
		"password": "p@ss",
		"nested":   map[string]any{"api_key": "api-secret", "note": "visible"},
	}
	view.CaseRun.Snapshot.Node.Preconditions = "password=hunter2; token: tok-abc; " +
		"api_key=key-abc; x-api-key: xkey-abc; apikey: akey-abc; apiKey=ckey-abc; note=ok"
	view.CleanupRequired = true
	interrupted := 2
	view.InterruptedStepSeq = &interrupted
	tools := New(&fakeRecorder{view: view}, testutil.NewFakeBrowserExecutor(), &fakeEvidence{}, nil, Limits{})

	out, err := callTool(t, tools, "execution.case_context", `{}`)
	if err != nil {
		t.Fatalf("execution.case_context: %v", err)
	}
	for _, secret := range []string{"p@ss", "api-secret", "hunter2", "tok-abc", "key-abc", "xkey-abc", "akey-abc", "ckey-abc"} {
		if strings.Contains(out.Content, secret) {
			t.Fatalf("case context leaked %q: %s", secret, out.Content)
		}
	}
	if !strings.Contains(out.Content, "alice") || !strings.Contains(out.Content, "visible") {
		t.Fatalf("case context dropped non-sensitive data: %s", out.Content)
	}
	if !strings.Contains(out.Content, `"cleanup_required":true`) {
		t.Fatalf("case context missing cleanup flag: %s", out.Content)
	}
	if !strings.Contains(out.Content, `"base_url":"https://app.example.test"`) {
		t.Fatalf("case context missing base URL: %s", out.Content)
	}
	if !strings.Contains(out.Content, `"allowed_hosts":["app.example.test"]`) {
		t.Fatalf("case context missing allowed hosts: %s", out.Content)
	}
	var payload struct {
		CompletedSeqs      []int `json:"completed_seqs"`
		InterruptedStepSeq *int  `json:"interrupted_step_seq"`
	}
	if err := json.Unmarshal([]byte(out.Content), &payload); err != nil {
		t.Fatalf("case context is not JSON: %v", err)
	}
	if len(payload.CompletedSeqs) != 1 || payload.CompletedSeqs[0] != 1 {
		t.Fatalf("completed seqs=%v want [1]", payload.CompletedSeqs)
	}
	if payload.InterruptedStepSeq == nil || *payload.InterruptedStepSeq != 2 {
		t.Fatalf("interrupted step seq=%v want 2", payload.InterruptedStepSeq)
	}
}

// TestCaseContextIncludesResumeActualsAndRemainingSteps pins the resume
// checkpoint of spec §5.5: the read model carries each completed step's actual
// result and evidence count plus the remaining steps.
func TestCaseContextIncludesResumeActualsAndRemainingSteps(t *testing.T) {
	view := defaultView()
	view.CompletedSteps = []application.CaseContextCompletedStep{{
		Seq: 1, Status: executiondomain.StepStatusPassed, Actual: "登录页已打开", EvidenceCount: 2,
	}}
	view.RemainingSteps = []executiondomain.CaseSnapshotStep{
		{Seq: 2, Action: "输入账号密码", Expected: "登录成功"},
	}
	tools := New(&fakeRecorder{view: view}, testutil.NewFakeBrowserExecutor(), &fakeEvidence{}, nil, Limits{})

	out, err := callTool(t, tools, "execution.case_context", `{}`)
	if err != nil {
		t.Fatalf("execution.case_context: %v", err)
	}
	var payload struct {
		CompletedSteps []struct {
			Seq           int    `json:"seq"`
			Status        string `json:"status"`
			Actual        string `json:"actual"`
			EvidenceCount int    `json:"evidence_count"`
		} `json:"completed_steps"`
		RemainingSteps []struct {
			Seq    int    `json:"seq"`
			Action string `json:"action"`
		} `json:"remaining_steps"`
	}
	if err := json.Unmarshal([]byte(out.Content), &payload); err != nil {
		t.Fatalf("case context is not JSON: %v (%s)", err, out.Content)
	}
	if len(payload.CompletedSteps) != 1 {
		t.Fatalf("completed_steps=%+v want one entry", payload.CompletedSteps)
	}
	completed := payload.CompletedSteps[0]
	if completed.Seq != 1 || completed.Status != "passed" ||
		completed.Actual != "登录页已打开" || completed.EvidenceCount != 2 {
		t.Fatalf("completed step=%+v want its actual and evidence count", completed)
	}
	if len(payload.RemainingSteps) != 1 || payload.RemainingSteps[0].Seq != 2 ||
		payload.RemainingSteps[0].Action != "输入账号密码" {
		t.Fatalf("remaining_steps=%+v want step 2", payload.RemainingSteps)
	}
}

// TestHTTPRequestInjectsHeadersAndRedactsEvidence pins the allowlist check,
// session header injection and the redacted persisted exchange.
func TestHTTPRequestInjectsHeadersAndRedactsEvidence(t *testing.T) {
	var gotAuthorization, gotAPIKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("X-Api-Key")
		w.Header().Set("Set-Cookie", "session=secret-cookie")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok","password":"p@ss","token":"tok-123"}`)
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")

	view := defaultView()
	view.AllowedHosts = []string{host}
	recorder := &fakeRecorder{view: view}
	evidence := &fakeEvidence{}
	limits := Limits{Session: func(context.Context, int64) (application.SessionRef, error) {
		return application.SessionRef{Headers: map[string]string{
			"Authorization": "Bearer sess-token",
			"X-Api-Key":     "api-key-123",
		}}, nil
	}}
	// A nil http client must fall back to a bounded default client.
	tools := New(recorder, nil, evidence, nil, limits)

	args := fmt.Sprintf(`{"method":"GET","url":%q}`, server.URL)
	out, err := callTool(t, tools, "http.request", args)
	if err != nil {
		t.Fatalf("http.request: %v", err)
	}
	if gotAuthorization != "Bearer sess-token" || gotAPIKey != "api-key-123" {
		t.Fatalf("injected headers=%q/%q want the session headers", gotAuthorization, gotAPIKey)
	}
	for _, secret := range []string{"Bearer sess-token", "api-key-123", "secret-cookie", "p@ss", "tok-123"} {
		if strings.Contains(out.Content, secret) {
			t.Fatalf("http tool result leaked %q: %s", secret, out.Content)
		}
	}
	var payload struct {
		Status     int    `json:"status"`
		Body       string `json:"body"`
		EvidenceID string `json:"evidence_id"`
		Truncated  bool   `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(out.Content), &payload); err != nil {
		t.Fatalf("http tool result is not JSON: %v (%s)", err, out.Content)
	}
	if payload.Status != http.StatusOK || payload.EvidenceID == "" || payload.Truncated {
		t.Fatalf("http result=%+v", payload)
	}
	if !strings.Contains(payload.Body, "***") || !strings.Contains(payload.Body, `"status":"ok"`) {
		t.Fatalf("response body was not redacted: %s", payload.Body)
	}

	if len(evidence.inputs) != 1 {
		t.Fatalf("captured %d artifacts, want 1", len(evidence.inputs))
	}
	captured := evidence.inputs[0]
	if captured.Kind != "http_exchange" || captured.Mime != "application/json" {
		t.Fatalf("capture kind/mime=%q/%q", captured.Kind, captured.Mime)
	}
	stored := string(captured.Data)
	for _, secret := range []string{"Bearer sess-token", "api-key-123", "secret-cookie", "p@ss", "tok-123"} {
		if strings.Contains(stored, secret) {
			t.Fatalf("evidence leaked %q: %s", secret, stored)
		}
	}
	if !strings.Contains(stored, "***") {
		t.Fatalf("evidence was not redacted: %s", stored)
	}
}

// TestHTTPRequestRejectsHostOutsideAllowlist proves the client-side allowlist
// check runs before any request or evidence capture.
func TestHTTPRequestRejectsHostOutsideAllowlist(t *testing.T) {
	evidence := &fakeEvidence{}
	tools := New(&fakeRecorder{view: defaultView()}, nil, evidence, nil, Limits{})

	_, err := callTool(t, tools, "http.request", `{"url":"https://evil.invalid/x"}`)
	requireToolError(t, err, "host_not_allowed")
	if len(evidence.inputs) != 0 {
		t.Fatalf("captured %d artifacts for a denied request", len(evidence.inputs))
	}

	_, err = callTool(t, tools, "http.request", `{"url":"file:///etc/passwd"}`)
	requireToolError(t, err, "invalid_arguments")
}

// TestBrowserGotoRejectsHostOutsideAllowlist proves the same allowlist guard on
// browser navigation happens before an executor context is created.
func TestBrowserGotoRejectsHostOutsideAllowlist(t *testing.T) {
	browser := testutil.NewFakeBrowserExecutor()
	tools := New(&fakeRecorder{view: defaultView()}, browser, &fakeEvidence{}, nil, Limits{})

	_, err := callTool(t, tools, "browser.goto", `{"url":"https://evil.invalid/"}`)
	requireToolError(t, err, "host_not_allowed")
	if contexts := browser.Contexts(); len(contexts) != 0 {
		t.Fatalf("created %d contexts for a denied navigation", len(contexts))
	}
}

// TestHostAllowed pins exact, base-URL and wildcard matching.
func TestHostAllowed(t *testing.T) {
	view := application.CaseContextView{
		BaseURL:      "https://app.example.test",
		AllowedHosts: []string{"*.example.test", "localhost:13336", "127.0.0.1"},
	}
	tests := []struct {
		url  string
		want bool
	}{
		{"https://app.example.test/login", true},
		{"https://api.example.test/x", true},
		{"https://example.test/", false},
		{"http://localhost:13336/healthz", true},
		{"http://localhost:13337/healthz", false},
		{"http://127.0.0.1:9/x", true},
		{"https://evil.invalid/", false},
		{"file:///etc/passwd", false},
	}
	for _, test := range tests {
		if got := hostAllowed(view, test.url); got != test.want {
			t.Fatalf("hostAllowed(%q)=%v want %v", test.url, got, test.want)
		}
	}
}

// TestFillSecretValueNeverReturned pins that a secret fill value never reaches
// the model even if the executor echoes it.
func TestFillSecretValueNeverReturned(t *testing.T) {
	browser := testutil.NewFakeBrowserExecutor()
	browser.ScriptAction(application.ActionResult{Result: json.RawMessage(`{"ok":true,"echo":"s3cret-value"}`)})
	tools := New(&fakeRecorder{view: defaultView()}, browser, &fakeEvidence{}, nil, Limits{})

	out, err := callTool(t, tools, "browser.fill",
		`{"target":{"selector":"#password"},"value":"s3cret-value","secret":true}`)
	if err != nil {
		t.Fatalf("browser.fill: %v", err)
	}
	if strings.Contains(out.Content, "s3cret-value") {
		t.Fatalf("secret fill value leaked: %s", out.Content)
	}
	// The executor still receives the real value; only the model result is safe.
	var found bool
	for _, call := range browser.Calls() {
		if call.Kind == testutil.FakeCallAction && call.Action.Name == "fill" {
			found = strings.Contains(string(call.Action.Args), "s3cret-value")
		}
	}
	if !found {
		t.Fatal("the executor did not receive the fill value")
	}
}

// TestBrowserActionErrorMapsToToolError pins executor action-failure mapping.
func TestBrowserActionErrorMapsToToolError(t *testing.T) {
	browser := testutil.NewFakeBrowserExecutor()
	browser.ScriptAction(application.ActionResult{ErrorCode: "target_not_found", ErrorMessage: "no button"})
	tools := New(&fakeRecorder{view: defaultView()}, browser, &fakeEvidence{}, nil, Limits{})

	_, err := callTool(t, tools, "browser.click", `{"target":{"role":"button","name":"登录"}}`)
	toolErr := requireToolError(t, err, "target_not_found")
	if !strings.Contains(toolErr.Message, "no button") {
		t.Fatalf("tool error message=%q", toolErr.Message)
	}
}

// TestSubmitToolsCallRecorder pins the exact inputs and decimal-string id
// decoding of the three submission tools.
func TestSubmitToolsCallRecorder(t *testing.T) {
	recorder := &fakeRecorder{
		view:      defaultView(),
		step:      executiondomain.StepResult{ID: 1, BatchID: 11, CaseRunID: 22, Seq: 2, Status: executiondomain.StepStatusPassed},
		assertion: executiondomain.AssertionResult{ID: 2, BatchID: 11, CaseRunID: 22, AssertionID: 77, Verdict: "passed"},
		finish:    executiondomain.CaseRun{ID: 22, BatchID: 11, Status: executiondomain.CaseRunStatusPassed},
	}
	tools := New(recorder, nil, nil, nil, Limits{})

	out, err := callTool(t, tools, "execution.step_record",
		`{"seq":2,"status":"passed","actual":"登录成功","evidence_ids":["31","32"]}`)
	if err != nil {
		t.Fatalf("execution.step_record: %v", err)
	}
	if recorder.stepIn.Seq != 2 || recorder.stepIn.Status != executiondomain.StepStatusPassed ||
		recorder.stepIn.Actual != "登录成功" || len(recorder.stepIn.EvidenceIDs) != 2 ||
		recorder.stepIn.EvidenceIDs[1] != 32 {
		t.Fatalf("step input=%+v", recorder.stepIn)
	}
	if !strings.Contains(out.Content, `"seq":2`) {
		t.Fatalf("step result=%s", out.Content)
	}

	out, err = callTool(t, tools, "execution.assertion_record",
		`{"assertion_id":"77","verdict":"passed","actual":"见到欢迎语","evidence_ids":["31"]}`)
	if err != nil {
		t.Fatalf("execution.assertion_record: %v", err)
	}
	if recorder.assertionIn.AssertionID != 77 || recorder.assertionIn.Verdict != "passed" ||
		recorder.assertionIn.Actual != "见到欢迎语" || len(recorder.assertionIn.EvidenceIDs) != 1 ||
		recorder.assertionIn.EvidenceIDs[0] != 31 {
		t.Fatalf("assertion input=%+v", recorder.assertionIn)
	}
	if !strings.Contains(out.Content, `"assertion_id":"77"`) {
		t.Fatalf("assertion result=%s", out.Content)
	}

	_, err = callTool(t, tools, "execution.case_finish",
		`{"verdict":"passed","evidence_ids":["31"]}`)
	if err != nil {
		t.Fatalf("execution.case_finish: %v", err)
	}
	if recorder.finishIn.Decision.Verdict != executiondomain.VerdictPassed ||
		recorder.finishIn.Decision.FailureClass != nil ||
		len(recorder.finishIn.EvidenceIDs) != 1 || recorder.finishIn.EvidenceIDs[0] != 31 {
		t.Fatalf("finish input=%+v", recorder.finishIn)
	}
}

// TestSubmitToolsReturnValidationErrors covers the correctable submission
// failures the model must see as tool errors.
func TestSubmitToolsReturnValidationErrors(t *testing.T) {
	recorder := &fakeRecorder{view: defaultView()}
	tools := New(recorder, nil, nil, nil, Limits{})
	tests := []struct {
		name string
		args string
		inv  *agentapp.Invocation
	}{
		{name: "execution.step_record", args: `{"seq":0,"status":"passed"}`},
		{name: "execution.step_record", args: `{"seq":1,"status":"running"}`},
		{name: "execution.step_record", args: `{"seq":1,"status":"failed"}`},
		{name: "execution.step_record", args: `{"seq":1,"status":"passed","unknown":true}`},
		{name: "execution.step_record", args: `{"seq":1,"status":"passed"}`, inv: &agentapp.Invocation{BatchID: 11}},
		{name: "execution.assertion_record", args: `{"verdict":"passed","evidence_ids":["31"]}`},
		{name: "execution.assertion_record", args: `{"assertion_id":"77","verdict":"passed"}`},
		{name: "execution.assertion_record", args: `{"assertion_id":"77","verdict":"blocked","evidence_ids":["31"]}`},
		{name: "execution.assertion_record", args: `{"assertion_id":"x","verdict":"passed","evidence_ids":["31"]}`},
		{name: "execution.case_finish", args: `{"verdict":"success"}`},
		{name: "execution.case_finish", args: `{"verdict":"failed","failure_class":"flaky"}`},
		{name: "execution.case_finish", args: `{"verdict":"passed","unknown":true}`},
	}
	for _, test := range tests {
		t.Run(test.name+" "+test.args, func(t *testing.T) {
			invocation := testInvocation
			if test.inv != nil {
				invocation = *test.inv
			}
			_, err := callToolWithInvocation(t, tools, test.name, test.args, invocation)
			requireToolError(t, err, "invalid_arguments")
		})
	}
}

// TestSubmitToolMapsRecorderValidationError pins ReasonError -> ToolError.
func TestSubmitToolMapsRecorderValidationError(t *testing.T) {
	recorder := &fakeRecorder{
		view:    defaultView(),
		stepErr: executiondomain.NewReasonError("invalid_argument", "step_out_of_order", "step 2 is not the next unfinished step", false, nil),
	}
	tools := New(recorder, nil, nil, nil, Limits{})

	_, err := callTool(t, tools, "execution.step_record", `{"seq":2,"status":"passed"}`)
	toolErr := requireToolError(t, err, "invalid_arguments")
	if !strings.Contains(toolErr.Message, "step 2 is not the next unfinished step") {
		t.Fatalf("tool error message=%q", toolErr.Message)
	}

	// The T15 Recorder already returns typed tool errors for correctable
	// submissions; the tool layer must preserve their code.
	recorder.stepErr = &agentdomain.ToolError{Code: "invalid_arguments", Message: "evidence 9 does not belong to this case run"}
	_, err = callTool(t, tools, "execution.step_record", `{"seq":2,"status":"passed"}`)
	toolErr = requireToolError(t, err, "invalid_arguments")
	if !strings.Contains(toolErr.Message, "evidence 9") {
		t.Fatalf("tool error message=%q", toolErr.Message)
	}
}

// TestDBAssertToolCallsRecorderWithAssertionID pins the P7 tool contract (spec
// §9.5): the agent supplies only assertion_id; the SQL/expected/target are read
// server-side from the snapshot by the recorder, and the result carries the
// verdict/preview fields of the spec.
func TestDBAssertToolCallsRecorderWithAssertionID(t *testing.T) {
	recorder := &fakeRecorder{
		view: defaultView(),
		dbOutcome: application.DBAssertOutcome{
			AssertionID: 77, Verdict: "passed", RowCount: 1, ElapsedMS: 4,
			ActualPreview: `{"columns":["count"],"rows":[["3"]]}`,
		},
	}
	tools := New(recorder, nil, nil, nil, Limits{})

	out, err := callTool(t, tools, "execution.db_assert", `{"assertionId":"77"}`)
	if err != nil {
		t.Fatalf("execution.db_assert: %v", err)
	}
	if recorder.dbAssertionID != 77 {
		t.Fatalf("assertion id = %d, want 77", recorder.dbAssertionID)
	}
	if recorder.dbLimits.MaxResultBytes != DefaultMaxDBResultBytes ||
		recorder.dbLimits.MaxSQLBytes != DefaultMaxDBSQLBytes {
		t.Fatalf("db limits = %+v, want the spec defaults", recorder.dbLimits)
	}
	var payload struct {
		AssertionID   string `json:"assertionId"`
		Verdict       string `json:"verdict"`
		RowCount      int    `json:"rowCount"`
		Truncated     bool   `json:"truncated"`
		ElapsedMS     int64  `json:"elapsedMs"`
		ActualPreview string `json:"actualPreview"`
	}
	if err := json.Unmarshal([]byte(out.Content), &payload); err != nil {
		t.Fatalf("db_assert result is not JSON: %v (%s)", err, out.Content)
	}
	if payload.AssertionID != "77" || payload.Verdict != "passed" ||
		payload.RowCount != 1 || payload.Truncated || payload.ElapsedMS != 4 ||
		payload.ActualPreview != recorder.dbOutcome.ActualPreview {
		t.Fatalf("db_assert payload = %+v", payload)
	}
	if strings.Contains(out.Content, "SELECT") || strings.Contains(out.Content, "password") {
		t.Fatalf("db_assert result leaked server-side state: %s", out.Content)
	}
}

// TestDBAssertToolRejectsAgentSuppliedQueryArguments pins that the tool accepts
// no SQL, expected value, target or credential argument: only assertion_id is
// part of the schema.
func TestDBAssertToolRejectsAgentSuppliedQueryArguments(t *testing.T) {
	recorder := &fakeRecorder{view: defaultView()}
	tools := New(recorder, nil, nil, nil, Limits{})

	for _, args := range []string{
		`{"assertionId":"77","sql":"SELECT 1"}`,
		`{"assertionId":"77","expected":"[[1]]"}`,
		`{"assertionId":"77","connection":"postgres://user:pass@db/app"}`,
		`{"assertionId":"77","target":"orders"}`,
		`{"assertionId":"77","verdict":"passed"}`,
		`{"assertionId":"0"}`,
		`{}`,
	} {
		_, err := callTool(t, tools, "execution.db_assert", args)
		requireToolError(t, err, "invalid_arguments")
	}
	if recorder.dbAssertionID != 0 {
		t.Fatalf("a rejected db_assert reached the recorder (assertion %d)", recorder.dbAssertionID)
	}
}

// TestDBAssertToolPreservesRecorderToolError pins that a recorded not_run
// failure reaches the model as the stable reason with the prescribed finish
// hint, so the agent can finish blocked(environment) or inconclusive(case).
func TestDBAssertToolPreservesRecorderToolError(t *testing.T) {
	recorder := &fakeRecorder{
		view: defaultView(),
		dbErr: &agentdomain.ToolError{Code: "tool_error",
			Message: "db_connection_failed: dial tcp: refused; finish blocked(environment)"},
	}
	tools := New(recorder, nil, nil, nil, Limits{})
	_, err := callTool(t, tools, "execution.db_assert", `{"assertionId":"77"}`)
	toolErr := requireToolError(t, err, "tool_error")
	if !strings.Contains(toolErr.Message, "db_connection_failed") ||
		!strings.Contains(toolErr.Message, "blocked(environment)") {
		t.Fatalf("tool error message = %q", toolErr.Message)
	}
}

// TestCaseFinishClosesTheBrowserContext pins the per-case cleanup after a
// successful terminal submission.
func TestCaseFinishClosesTheBrowserContext(t *testing.T) {
	recorder := &fakeRecorder{
		view:   defaultView(),
		finish: executiondomain.CaseRun{ID: 22, Status: executiondomain.CaseRunStatusPassed},
	}
	browser := testutil.NewFakeBrowserExecutor()
	browser.ScriptAction(application.ActionResult{Artifacts: []application.Artifact{{
		Kind: "screenshot", Mime: "image/png", Data: []byte("png"), SHA256: sha256Hex([]byte("png")),
	}}})
	tools := New(recorder, browser, &fakeEvidence{}, nil, Limits{})

	if _, err := callTool(t, tools, "browser.screenshot", `{}`); err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if len(browser.Contexts()) != 1 {
		t.Fatalf("contexts after screenshot=%v", browser.Contexts())
	}
	if _, err := callTool(t, tools, "execution.case_finish", `{"verdict":"passed"}`); err != nil {
		t.Fatalf("case_finish: %v", err)
	}
	if closed := browser.Closed(); len(closed) != 1 {
		t.Fatalf("closed contexts=%v want the case context", closed)
	}
}

// TestToolsRequireCaseRunScope pins that no tool acts without the P6 invocation
// scope the executor injects.
func TestToolsRequireCaseRunScope(t *testing.T) {
	tools := New(&fakeRecorder{view: defaultView()}, testutil.NewFakeBrowserExecutor(), &fakeEvidence{}, nil, Limits{})
	for _, name := range []string{
		"execution.case_context",
		"browser.goto",
		"browser.screenshot",
		"execution.step_record",
		"execution.assertion_record",
		"execution.case_finish",
		"execution.db_assert",
	} {
		_, err := callToolWithInvocation(t, tools, name, `{"url":"https://app.example.test/","seq":1,"status":"passed","assertion_id":"1","verdict":"passed","evidence_ids":["1"]}`, agentapp.Invocation{})
		requireToolError(t, err, "invalid_arguments")
	}
}

// TestToolSetCancelCaseCancelsOnlyTheCachedCaseContext pins that the controller
// arms of the tool set act on the per-case registry: no context means no
// executor call, and a live context is canceled in place.
func TestToolSetCancelCaseCancelsOnlyTheCachedCaseContext(t *testing.T) {
	browser := testutil.NewFakeBrowserExecutor()
	set := NewToolSet(&fakeRecorder{view: defaultView()}, browser, &fakeEvidence{}, nil, Limits{})

	if err := set.CancelCase(context.Background(), 22); err != nil {
		t.Fatalf("cancel without a context: %v", err)
	}
	if canceled := browser.Canceled(); len(canceled) != 0 {
		t.Fatalf("canceled %d contexts before any was created", len(canceled))
	}

	if _, err := callTool(t, set.Tools(), "browser.goto", `{"url":"https://app.example.test/login"}`); err != nil {
		t.Fatalf("browser.goto: %v", err)
	}
	if err := set.CancelCase(context.Background(), 22); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if canceled := browser.Canceled(); len(canceled) != 1 {
		t.Fatalf("canceled contexts = %v, want the case context", canceled)
	}
	if contexts := browser.Contexts(); len(contexts) != 1 {
		t.Fatalf("cancel must keep the context alive, live contexts=%v", contexts)
	}
}

// TestToolSetCloseCaseClosesAndForgetsOnlyItsContext pins that CloseCase
// disposes and forgets the cached context, is idempotent, and lets a later
// action open one fresh context for the same case.
func TestToolSetCloseCaseClosesAndForgetsOnlyItsContext(t *testing.T) {
	browser := testutil.NewFakeBrowserExecutor()
	set := NewToolSet(&fakeRecorder{view: defaultView()}, browser, &fakeEvidence{}, nil, Limits{})

	if err := set.CloseCase(context.Background(), 22); err != nil {
		t.Fatalf("close without a context: %v", err)
	}
	if closed := browser.Closed(); len(closed) != 0 {
		t.Fatalf("closed %d contexts before any was created", len(closed))
	}

	if _, err := callTool(t, set.Tools(), "browser.goto", `{"url":"https://app.example.test/login"}`); err != nil {
		t.Fatalf("browser.goto: %v", err)
	}
	if err := set.CloseCase(context.Background(), 22); err != nil {
		t.Fatalf("close: %v", err)
	}
	if closed := browser.Closed(); len(closed) != 1 {
		t.Fatalf("closed contexts = %v, want the case context", closed)
	}
	// A second close is a no-op: the registry already forgot the context.
	if err := set.CloseCase(context.Background(), 22); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if closed := browser.Closed(); len(closed) != 1 {
		t.Fatalf("second close disposed %d contexts, want 1", len(closed))
	}

	if _, err := callTool(t, set.Tools(), "browser.goto", `{"url":"https://app.example.test/home"}`); err != nil {
		t.Fatalf("browser.goto after close: %v", err)
	}
	if contexts := browser.Contexts(); len(contexts) != 1 {
		t.Fatalf("live contexts after reopening = %d, want 1", len(contexts))
	}
}

// TestConsoleAndNetworkCaptureEvidence pins the optional console/network
// evidence capture and that the tool result exposes the evidence metadata the
// agent must cite (spec §5.7).
func TestConsoleAndNetworkCaptureEvidence(t *testing.T) {
	recorder := &fakeRecorder{view: defaultView()}
	browser := testutil.NewFakeBrowserExecutor()
	result := json.RawMessage(`{"items":[]}`)
	browser.ScriptAction(application.ActionResult{Result: result})
	browser.ScriptAction(application.ActionResult{Result: result})
	evidence := &fakeEvidence{}
	tools := New(recorder, browser, evidence, nil, Limits{})

	consoleOut, err := callTool(t, tools, "browser.console", `{"capture_evidence":true}`)
	if err != nil {
		t.Fatalf("browser.console: %v", err)
	}
	networkOut, err := callTool(t, tools, "browser.network", `{"capture_evidence":true}`)
	if err != nil {
		t.Fatalf("browser.network: %v", err)
	}
	if len(evidence.inputs) != 2 {
		t.Fatalf("captured %d artifacts, want console+network", len(evidence.inputs))
	}
	if evidence.inputs[0].Kind != "console_log" || evidence.inputs[1].Kind != "network_log" {
		t.Fatalf("capture kinds=%q/%q", evidence.inputs[0].Kind, evidence.inputs[1].Kind)
	}
	hash := sha256Hex(result)
	for index, out := range []agentdomain.ToolResult{consoleOut, networkOut} {
		var payload struct {
			Items      []any  `json:"items"`
			EvidenceID string `json:"evidence_id"`
			SHA256     string `json:"sha256"`
			Size       int64  `json:"size"`
		}
		if err := json.Unmarshal([]byte(out.Content), &payload); err != nil {
			t.Fatalf("log tool result is not JSON: %v (%s)", err, out.Content)
		}
		if payload.EvidenceID != fmt.Sprintf("%d", index+1) || payload.SHA256 != hash || payload.Size <= 0 {
			t.Fatalf("log result %d missing evidence metadata: %s", index, out.Content)
		}
		if !strings.Contains(out.Content, `"items"`) {
			t.Fatalf("log result %d dropped the console/network tail: %s", index, out.Content)
		}
	}
}

// TestHTTPRequestMergesSessionHeadersCaseInsensitively pins that a session
// header wins over a model header with different capitalization, sending only
// one value (design spec §5.9: the model cannot duplicate or override auth).
func TestHTTPRequestMergesSessionHeadersCaseInsensitively(t *testing.T) {
	var authorization, apiKey, custom []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Values("Authorization")
		apiKey = r.Header.Values("X-Api-Key")
		custom = r.Header.Values("X-Custom")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")

	view := defaultView()
	view.AllowedHosts = []string{host}
	limits := Limits{Session: func(context.Context, int64) (application.SessionRef, error) {
		return application.SessionRef{Headers: map[string]string{
			"Authorization": "Bearer session-token",
			"X-Api-Key":     "session-key",
		}}, nil
	}}
	tools := New(&fakeRecorder{view: view}, nil, &fakeEvidence{}, nil, limits)

	args := fmt.Sprintf(`{"url":%q,"headers":{"authorization":"Bearer model-token","X-API-KEY":"model-key","X-Custom":"kept"}}`, server.URL)
	if _, err := callTool(t, tools, "http.request", args); err != nil {
		t.Fatalf("http.request: %v", err)
	}
	if len(authorization) != 1 || authorization[0] != "Bearer session-token" {
		t.Fatalf("Authorization values=%v want only the session value", authorization)
	}
	if len(apiKey) != 1 || apiKey[0] != "session-key" {
		t.Fatalf("X-Api-Key values=%v want only the session value", apiKey)
	}
	if len(custom) != 1 || custom[0] != "kept" {
		t.Fatalf("X-Custom values=%v want the model value", custom)
	}
}

// TestHTTPRequestRejectsRedirectToDisallowedHost pins that a redirect leaving
// the allowlist is aborted before the request and produces no evidence.
func TestHTTPRequestRejectsRedirectToDisallowedHost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://evil.invalid/steal", http.StatusFound)
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")

	view := defaultView()
	view.AllowedHosts = []string{host}
	evidence := &fakeEvidence{}
	tools := New(&fakeRecorder{view: view}, nil, evidence, nil, Limits{})

	_, err := callTool(t, tools, "http.request", fmt.Sprintf(`{"url":%q}`, server.URL))
	requireToolError(t, err, "host_not_allowed")
	if len(evidence.inputs) != 0 {
		t.Fatalf("captured %d artifacts for a denied redirect", len(evidence.inputs))
	}
}

// TestHTTPRequestCaptures401AsSessionUnauthorized pins the §5.9 session-invalid
// signal: the exchange is persisted as evidence and the tool reports the
// correctable session_unauthorized code.
func TestHTTPRequestCaptures401AsSessionUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"login required"}`)
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")

	view := defaultView()
	view.AllowedHosts = []string{host}
	evidence := &fakeEvidence{}
	tools := New(&fakeRecorder{view: view}, nil, evidence, nil, Limits{})

	_, err := callTool(t, tools, "http.request", fmt.Sprintf(`{"url":%q}`, server.URL))
	requireToolError(t, err, "session_unauthorized")
	if len(evidence.inputs) != 1 || evidence.inputs[0].Kind != "http_exchange" {
		t.Fatalf("evidence=%+v want the captured 401 exchange", evidence.inputs)
	}
}

// TestHTTPRequestTruncatesResponseAtBodyCap pins the response-body cap: the
// stored exchange and the model result carry the same truncated bytes.
func TestHTTPRequestTruncatesResponseAtBodyCap(t *testing.T) {
	const body = "0123456789"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")

	view := defaultView()
	view.AllowedHosts = []string{host}
	evidence := &fakeEvidence{}
	tools := New(&fakeRecorder{view: view}, nil, evidence, nil, Limits{MaxHTTPBodyBytes: 5})

	out, err := callTool(t, tools, "http.request", fmt.Sprintf(`{"url":%q}`, server.URL))
	if err != nil {
		t.Fatalf("http.request: %v", err)
	}
	var payload struct {
		Body      string `json:"body"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(out.Content), &payload); err != nil {
		t.Fatalf("http result is not JSON: %v (%s)", err, out.Content)
	}
	if !payload.Truncated || payload.Body != body[:5] {
		t.Fatalf("body/truncated=%q/%v want %q/true", payload.Body, payload.Truncated, body[:5])
	}
	if len(evidence.inputs) != 1 || !strings.Contains(string(evidence.inputs[0].Data), `"truncated":true`) {
		t.Fatalf("evidence did not record the truncation: %+v", evidence.inputs)
	}
}

// TestScreenshotRejectsArtifactAboveLimit pins the per-artifact cap: an
// oversized artifact is an internal error and is never persisted.
func TestScreenshotRejectsArtifactAboveLimit(t *testing.T) {
	browser := testutil.NewFakeBrowserExecutor()
	browser.ScriptAction(application.ActionResult{Artifacts: []application.Artifact{{
		Kind: "screenshot", Mime: "image/png", Data: []byte("12345"), SHA256: sha256Hex([]byte("12345")),
	}}})
	evidence := &fakeEvidence{}
	tools := New(&fakeRecorder{view: defaultView()}, browser, evidence, nil, Limits{MaxArtifactBytes: 4})

	_, err := callTool(t, tools, "browser.screenshot", `{}`)
	requireToolError(t, err, "internal")
	if len(evidence.inputs) != 0 {
		t.Fatalf("captured %d oversized artifacts", len(evidence.inputs))
	}
}

// TestValidateToolsRequiresEveryProfileTool pins the §7.1 startup guard the
// composition root calls before seeding: every profile tool must be registered.
func TestValidateToolsRequiresEveryProfileTool(t *testing.T) {
	tools := New(&fakeRecorder{}, testutil.NewFakeBrowserExecutor(), &fakeEvidence{}, nil, Limits{})
	if err := ValidateTools(agentapp.NewToolRegistry(tools...)); err != nil {
		t.Fatalf("ValidateTools(live registry): %v", err)
	}

	withoutHTTP := make([]agentdomain.Tool, 0, len(tools))
	for _, tool := range tools {
		if tool.Name() != "http.request" {
			withoutHTTP = append(withoutHTTP, tool)
		}
	}
	err := ValidateTools(agentapp.NewToolRegistry(withoutHTTP...))
	if err == nil || !strings.Contains(err.Error(), `"http.request"`) {
		t.Fatalf("ValidateTools(missing tool)=%v want an error naming http.request", err)
	}

	if err := ValidateTools(nil); err == nil {
		t.Fatal("ValidateTools(nil registry) must fail")
	}
}

// visualView is the default case read model plus one kind=visual assertion.
func visualView(baseline *int64) application.CaseContextView {
	view := defaultView()
	view.CaseRun.Snapshot.Assertions = append(view.CaseRun.Snapshot.Assertions, executiondomain.CaseSnapshotAssertion{
		ID: 88, StepSeq: 2, Kind: "visual", Target: "#chart", Expected: "match",
		Visual: &executiondomain.CaseSnapshotVisual{
			Selector: "#chart", Threshold: 0.01, BaselineEvidenceID: baseline,
			ViewportWidth: 1280, ViewportHeight: 720, DeviceScaleFactor: 1,
		},
	})
	return view
}

// visualBrowserFake is a browser executor that also implements the optional
// VisualComparator slice, mirroring the production client.
type visualBrowserFake struct {
	*testutil.FakeBrowserExecutor
	visualCalls int
	result      application.VisualCompareResult
	err         error
}

func (f *visualBrowserFake) CompareVisual(_ context.Context, _ application.ContextRef, _ application.VisualCompareRequest) (application.VisualCompareResult, error) {
	f.visualCalls++
	return f.result, f.err
}

// fakeVisualBaselineReader is the approved-baseline read port wired through the
// tool set setter.
type fakeVisualBaselineReader struct{}

func (fakeVisualBaselineReader) ReadVisualBaseline(context.Context, int64, int64) (application.VisualBaseline, error) {
	return application.VisualBaseline{EvidenceID: 501, Kind: "visual_baseline", Mime: "image/png", Data: []byte("png")}, nil
}

// TestVisualAssertToolRejectsExtraArguments pins the frozen argument shape: the
// agent supplies only assertionId; a model-supplied baseline/config is rejected.
func TestVisualAssertToolRejectsExtraArguments(t *testing.T) {
	tools := New(&fakeRecorder{view: visualView(int64Pointer(501))}, testutil.NewFakeBrowserExecutor(), &fakeEvidence{}, nil, Limits{})
	_, err := callTool(t, tools, "execution.visual_assert",
		`{"assertionId":"88","baseline_base64":"Zm9v","threshold":0.5}`)
	requireToolError(t, err, "invalid_arguments")
}

// TestVisualAssertToolRequiresAVisualSnapshotAssertion rejects an unknown or
// non-visual assertion id before any browser or recorder work.
func TestVisualAssertToolRequiresAVisualSnapshotAssertion(t *testing.T) {
	recorder := &fakeRecorder{view: visualView(int64Pointer(501))}
	tools := New(recorder, testutil.NewFakeBrowserExecutor(), &fakeEvidence{}, nil, Limits{})

	_, err := callTool(t, tools, "execution.visual_assert", `{"assertionId":"999"}`)
	requireToolError(t, err, "invalid_arguments")

	uiTools := New(&fakeRecorder{view: defaultView()}, testutil.NewFakeBrowserExecutor(), &fakeEvidence{}, nil, Limits{})
	_, err = callTool(t, uiTools, "execution.visual_assert", `{"assertionId":"77"}`)
	requireToolError(t, err, "invalid_arguments")
	if recorder.visualAssertionID != 0 {
		t.Fatalf("a rejected assertion reached the recorder (%d)", recorder.visualAssertionID)
	}
}

// TestVisualAssertToolSkipsBrowserForUnapprovedBaseline pins the ordering: an
// assertion without an approved baseline is classified before any executor
// context is created.
func TestVisualAssertToolSkipsBrowserForUnapprovedBaseline(t *testing.T) {
	browser := testutil.NewFakeBrowserExecutor()
	recorder := &fakeRecorder{
		view:      visualView(nil),
		visualErr: &agentdomain.ToolError{Code: "missing_visual_baseline", Message: "no approved visual baseline"},
	}
	tools := New(recorder, browser, &fakeEvidence{}, nil, Limits{})

	_, err := callTool(t, tools, "execution.visual_assert", `{"assertionId":"88"}`)
	requireToolError(t, err, "missing_visual_baseline")
	if recorder.visualAssertionID != 88 {
		t.Fatalf("recorder assertion id = %d, want 88", recorder.visualAssertionID)
	}
	if recorder.visualOptions.Comparator != nil || recorder.visualOptions.Context.ID != "" {
		t.Fatalf("missing baseline opened a comparator/context: %+v", recorder.visualOptions)
	}
	for _, call := range browser.Calls() {
		if call.Kind == testutil.FakeCallNewContext {
			t.Fatal("a missing baseline created an executor context")
		}
	}
}

// TestVisualAssertToolCreatesContextAndDelegates covers the approved-baseline
// path: the tool creates the case context, hands the comparator and the
// configured caps to the recorder and projects the recorded outcome.
func TestVisualAssertToolCreatesContextAndDelegates(t *testing.T) {
	browser := &visualBrowserFake{FakeBrowserExecutor: testutil.NewFakeBrowserExecutor()}
	recorder := &fakeRecorder{
		view: visualView(int64Pointer(501)),
		visualOutcome: application.VisualAssertOutcome{
			AssertionID: 88, Verdict: "passed", Classification: "passed",
			Reason: "visual_match", Threshold: 0.01, HasBaseline: true, EnvironmentMatch: true,
			ActualEvidenceID: 900, DiffEvidenceID: 901,
		},
	}
	limits := Limits{MaxArtifactBytes: 2048, VisualBaselineMaxBytes: 4096}
	set := NewToolSet(recorder, browser, &fakeEvidence{}, nil, limits)
	set.SetVisualBaseline(fakeVisualBaselineReader{})
	tools := set.Tools()

	out, err := callTool(t, tools, "execution.visual_assert", `{"assertionId":"88"}`)
	if err != nil {
		t.Fatalf("execution.visual_assert: %v", err)
	}
	if recorder.visualAssertionID != 88 {
		t.Fatalf("recorder assertion id = %d, want 88", recorder.visualAssertionID)
	}
	opts := recorder.visualOptions
	if opts.Comparator == nil || opts.Context.ID != "fake-ctx-1" {
		t.Fatalf("comparator/context = %v/%+v", opts.Comparator, opts.Context)
	}
	if opts.MaxArtifactBytes != 2048 || opts.MaxBaselineBytes != 4096 {
		t.Fatalf("caps = %d/%d, want 2048/4096", opts.MaxArtifactBytes, opts.MaxBaselineBytes)
	}
	if opts.Capture == nil || opts.Baselines == nil {
		t.Fatalf("capture/baselines = %v/%v, want both wired", opts.Capture, opts.Baselines)
	}
	var payload struct {
		AssertionID      string `json:"assertionId"`
		Verdict          string `json:"verdict"`
		Classification   string `json:"classification"`
		ActualEvidenceID string `json:"actualEvidenceId"`
		DiffEvidenceID   string `json:"diffEvidenceId"`
	}
	if err := json.Unmarshal([]byte(out.Content), &payload); err != nil {
		t.Fatalf("result is not JSON: %v (%s)", err, out.Content)
	}
	if payload.AssertionID != "88" || payload.Verdict != "passed" ||
		payload.Classification != "passed" || payload.ActualEvidenceID != "900" ||
		payload.DiffEvidenceID != "901" {
		t.Fatalf("result = %+v", payload)
	}
}

// TestVisualAssertToolUnsupportedComparatorFailsClosed pins that a browser
// executor without CompareVisual never fakes a pass: the recorder is told there
// is no comparator and no context is created.
func TestVisualAssertToolUnsupportedComparatorFailsClosed(t *testing.T) {
	browser := testutil.NewFakeBrowserExecutor()
	recorder := &fakeRecorder{
		view:      visualView(int64Pointer(501)),
		visualErr: &agentdomain.ToolError{Code: "unsupported_assertion_kind", Message: "no visual comparator"},
	}
	tools := New(recorder, browser, &fakeEvidence{}, nil, Limits{})

	_, err := callTool(t, tools, "execution.visual_assert", `{"assertionId":"88"}`)
	requireToolError(t, err, "unsupported_assertion_kind")
	if recorder.visualOptions.Comparator != nil || recorder.visualOptions.Context.ID != "" {
		t.Fatalf("unsupported comparator created a context: %+v", recorder.visualOptions)
	}
	for _, call := range browser.Calls() {
		if call.Kind == testutil.FakeCallNewContext {
			t.Fatal("an unsupported comparator created an executor context")
		}
	}
}

// TestVisualAssertToolIsRegistered proves the frozen tool list carries the P8
// visual assertion tool (the execution-agent profile allowlist validates it).
func TestVisualAssertToolIsRegistered(t *testing.T) {
	tools := New(&fakeRecorder{}, testutil.NewFakeBrowserExecutor(), &fakeEvidence{}, nil, Limits{})
	names := make(map[string]bool, len(tools))
	for _, tool := range tools {
		names[tool.Name()] = true
	}
	if !names["execution.visual_assert"] {
		t.Fatal("execution.visual_assert is not registered")
	}
}

// TestToolCallBudgetDeniesOverLimitCalls pins the P8 tool-call budget
// (EZTEST_EXEC_MAX_TOOL_CALLS): the first N calls of one case run execute, the
// (N+1)th is denied with the stable code and the run is reported as budget
// exhausted to the case runner.
func TestToolCallBudgetDeniesOverLimitCalls(t *testing.T) {
	set := NewToolSet(&fakeRecorder{view: defaultView()}, nil, nil, nil, Limits{MaxToolCalls: 2})
	tools := set.Tools()

	for call := 1; call <= 2; call++ {
		if _, err := callTool(t, tools, "execution.case_context", `{}`); err != nil {
			t.Fatalf("call %d within the budget: %v", call, err)
		}
	}
	if code, ok := set.CaseRunFailure(testInvocation.CaseRunID); ok {
		t.Fatalf("a run within its budget reported a failure %q", code)
	}

	_, err := callTool(t, tools, "execution.case_context", `{}`)
	requireToolError(t, err, "tool_call_budget_exhausted")
	code, ok := set.CaseRunFailure(testInvocation.CaseRunID)
	if !ok || code != "tool_call_budget_exhausted" {
		t.Fatalf("case run failure = %q/%v, want tool_call_budget_exhausted", code, ok)
	}

	// A later call of the same run stays denied and the counter is not reset.
	_, err = callTool(t, tools, "execution.case_context", `{}`)
	requireToolError(t, err, "tool_call_budget_exhausted")

	// Another case run has its own independent budget.
	other := testInvocation
	other.CaseRunID = 23
	if _, err := callToolWithInvocation(t, tools, "execution.case_context", `{}`, other); err != nil {
		t.Fatalf("another case run was charged for the first run's calls: %v", err)
	}
}

// TestExecutorTimeoutMarksCaseRunFailure pins the P8 executor timeout contract:
// an action that timed out in the executor surfaces the stable executor_timeout
// code and reports it to the case runner as a terminal model failure.
func TestExecutorTimeoutMarksCaseRunFailure(t *testing.T) {
	browser := testutil.NewFakeBrowserExecutor()
	browser.ScriptAction(application.ActionResult{
		ErrorCode:    "executor_timeout",
		ErrorMessage: "action timed out and the browser context was recovered",
	})
	set := NewToolSet(&fakeRecorder{view: defaultView()}, browser, &fakeEvidence{}, nil,
		Limits{MaxToolCalls: 5})

	_, err := callTool(t, set.Tools(), "browser.snapshot", `{}`)
	requireToolError(t, err, "executor_timeout")
	code, ok := set.CaseRunFailure(testInvocation.CaseRunID)
	if !ok || code != "executor_timeout" {
		t.Fatalf("case run failure = %q/%v, want executor_timeout", code, ok)
	}

	// Once the executor timeout is recorded every further tool call is denied:
	// the run must not be able to finish as passed afterwards.
	_, err = callTool(t, set.Tools(), "execution.case_context", `{}`)
	requireToolError(t, err, "executor_timeout")

	// Closing the case context forgets the signal so a re-dispatch is clean.
	if err := set.CloseCase(context.Background(), testInvocation.CaseRunID); err != nil {
		t.Fatalf("CloseCase: %v", err)
	}
	if code, ok := set.CaseRunFailure(testInvocation.CaseRunID); ok {
		t.Fatalf("case run failure %q survived CloseCase", code)
	}
}
