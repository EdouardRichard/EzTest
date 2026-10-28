// Package agent_tool implements the execution agent's tool set (design spec
// §7.1): one read-only case-context tool, the browser tools backed by the
// executor, http.request and the three structured submission tools backed by
// the Recorder. Every tool reads the batch and case-run scope from the P1
// Invocation context, never from model-supplied arguments.
package agent_tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

const (
	// DefaultMaxArtifactBytes is the per-artifact cap of design spec §5.7
	// (10 MiB) applied when Limits.MaxArtifactBytes is not positive.
	DefaultMaxArtifactBytes int64 = 10 << 20
	// DefaultMaxHTTPBodyBytes bounds the HTTP exchange body kept in evidence.
	DefaultMaxHTTPBodyBytes int64 = 1 << 20
	// DefaultHTTPTimeout bounds one http.request when the caller context has no
	// deadline and Limits.HTTPTimeout is not positive.
	DefaultHTTPTimeout = 30 * time.Second
	// maxActionTimeoutMS is the executor's per-action ceiling (design spec §5.6).
	maxActionTimeoutMS = 60000
)

// EvidenceCapturer is the narrow slice of the P1 evidence use case the browser
// and HTTP tools need: persist one artifact and return its immutable metadata.
// *application.EvidenceService satisfies it.
type EvidenceCapturer interface {
	Capture(ctx context.Context, in application.CaptureInput) (executiondomain.Evidence, error)
}

// Limits are the execution tool bounds and the optional session resolver.
type Limits struct {
	// MaxArtifactBytes caps one persisted artifact (default 10 MiB).
	MaxArtifactBytes int64
	// MaxHTTPBodyBytes caps the response body stored as evidence and returned to
	// the model (default 1 MiB).
	MaxHTTPBodyBytes int64
	// HTTPTimeout bounds one http.request when the invocation context has no
	// deadline (default 30s).
	HTTPTimeout time.Duration
	// MaxDBResultBytes caps one db_result evidence artifact of execution.db_assert
	// (EZTEST_DB_RESULT_MAX_BYTES, default 1 MiB).
	MaxDBResultBytes int64
	// MaxDBSQLBytes caps the snapshot SQL of one db assertion
	// (EZTEST_DB_SQL_MAX_BYTES, default 16 KiB).
	MaxDBSQLBytes int64
	// Session resolves the leased session of a case run. The browser tools use
	// its StorageState/SessionStorage as the executor context's initial state and
	// http.request injects its authentication Headers. It is optional: without a
	// resolver the tools run unauthenticated. The composition root wires it to
	// the execution AgentRuntime's SessionForCaseRun bridge.
	Session func(ctx context.Context, caseRunID int64) (application.SessionRef, error)
}

// withDefaults fills the non-positive limits with the spec defaults.
func (l Limits) withDefaults() Limits {
	if l.MaxArtifactBytes <= 0 {
		l.MaxArtifactBytes = DefaultMaxArtifactBytes
	}
	if l.MaxHTTPBodyBytes <= 0 {
		l.MaxHTTPBodyBytes = DefaultMaxHTTPBodyBytes
	}
	if l.HTTPTimeout <= 0 {
		l.HTTPTimeout = DefaultHTTPTimeout
	}
	if l.MaxDBResultBytes <= 0 {
		l.MaxDBResultBytes = DefaultMaxDBResultBytes
	}
	if l.MaxDBSQLBytes <= 0 {
		l.MaxDBSQLBytes = DefaultMaxDBSQLBytes
	}
	return l
}

// caseBrowser is one lazily created executor context plus the case read model
// that produced it. The context is created on the first browser action of a
// case run and closed after a successful case_finish.
type caseBrowser struct {
	ref  application.ContextRef
	view application.CaseContextView
}

// toolSet is the shared state of one registered tool set. The P1 registry is
// process-wide, so the set serves concurrent case runs and guards its
// per-case context cache.
type toolSet struct {
	recorder   application.Recorder
	browser    application.BrowserExecutor
	evidence   EvidenceCapturer
	httpClient *http.Client
	limits     Limits

	mu       sync.Mutex
	contexts map[int64]caseBrowser
}

// toolDefinition is one tool's frozen metadata and its execution function.
type toolDefinition struct {
	name        string
	description string
	schema      string
	run         func(ctx context.Context, set *toolSet, args json.RawMessage) (agentdomain.ToolResult, error)
}

// executionTool implements agent/domain.Tool over one definition.
type executionTool struct {
	set *toolSet
	def toolDefinition
}

// Name implements domain.Tool.
func (t executionTool) Name() string { return t.def.name }

// Description implements domain.Tool.
func (t executionTool) Description() string { return t.def.description }

// InputSchema implements domain.Tool.
func (t executionTool) InputSchema() json.RawMessage { return json.RawMessage(t.def.schema) }

// Execute implements domain.Tool. The case-run scope is read from the P1
// invocation context the tool executor injects.
func (t executionTool) Execute(ctx context.Context, args json.RawMessage) (agentdomain.ToolResult, error) {
	return t.def.run(ctx, t.set, args)
}

// ToolSet is the exported handle of one shared execution tool set. It owns the
// per-case browser-context registry, so the case runner can cancel and close
// exactly the context the tools created for a case run (see
// application.CaseBrowserController).
type ToolSet = toolSet

// NewToolSet returns the shared execution tool set. Tools returns the
// registered tools; every tool of the set shares the same per-case context
// registry and the controller methods act on that registry.
func NewToolSet(recorder application.Recorder, browser application.BrowserExecutor, evidence EvidenceCapturer, httpClient *http.Client, limits Limits) *ToolSet {
	return &toolSet{
		recorder:   recorder,
		browser:    browser,
		evidence:   evidence,
		httpClient: httpClient,
		limits:     limits.withDefaults(),
		contexts:   make(map[int64]caseBrowser),
	}
}

// New returns the execution agent tools of design spec §7.1 in their frozen
// order. recorder, browser and evidence may be nil in tests that never exercise
// the corresponding tool; the affected tool then returns an internal tool
// error. httpClient is the shared HTTP client for http.request (nil uses a
// bounded default client).
func New(recorder application.Recorder, browser application.BrowserExecutor, evidence EvidenceCapturer, httpClient *http.Client, limits Limits) []agentdomain.Tool {
	return NewToolSet(recorder, browser, evidence, httpClient, limits).Tools()
}

// Tools returns the execution agent tools of design spec §7.1 in their frozen
// order.
func (s *toolSet) Tools() []agentdomain.Tool {
	definitions := append([]toolDefinition{
		{
			name:        "execution.case_context",
			description: "Read the pinned case snapshot, the resume checkpoint (completed steps, interrupted step, cleanup flag) and the target base URL/allowlist. Sensitive test data and preconditions are masked.",
			schema:      schemaCaseContext,
			run:         runCaseContext,
		},
	}, browserDefinitions()...)
	definitions = append(definitions, httpDefinitions()...)
	definitions = append(definitions, submitDefinitions()...)
	definitions = append(definitions, dbAssertDefinitions()...)

	tools := make([]agentdomain.Tool, 0, len(definitions))
	for _, definition := range definitions {
		tools = append(tools, executionTool{set: s, def: definition})
	}
	return tools
}

// ValidateTools verifies that every tool the execution-agent profile allows is
// present in the live registry (design spec §7.1). The composition root must
// call it before seeding and treat a failure as fatal: a missing tool would
// silently offer the agent fewer tools than the profile declares. T19 owns the
// startup wiring.
func ValidateTools(registry *agentapp.ToolRegistry) error {
	for _, name := range executiondomain.ExecutionProfile().AllowedTools {
		if tool, ok := registry.Get(name); !ok || tool == nil {
			return fmt.Errorf("execution: agent profile allows tool %q, which is not registered", name)
		}
	}
	return nil
}

// caseScope reads the execution run scope from the invocation context.
func caseScope(ctx context.Context) (agentapp.Invocation, error) {
	invocation, ok := agentapp.InvocationFromContext(ctx)
	if !ok || invocation.CaseRunID <= 0 {
		return agentapp.Invocation{}, invalidArguments("execution tools require a case run scope")
	}
	return invocation, nil
}

// loadCaseContext reads the case read model through the Recorder.
func loadCaseContext(ctx context.Context, recorder application.Recorder, caseRunID int64) (application.CaseContextView, error) {
	if recorder == nil {
		return application.CaseContextView{}, internalError("the execution recorder is not configured")
	}
	view, err := recorder.CaseContext(ctx, caseRunID)
	if err != nil {
		return application.CaseContextView{}, mapExecutionError(err)
	}
	return view, nil
}

// invalidArguments builds the correctable invalid_arguments tool error.
func invalidArguments(message string) error {
	return &agentdomain.ToolError{Code: "invalid_arguments", Message: message}
}

// internalError builds an internal tool error.
func internalError(message string) error {
	return &agentdomain.ToolError{Code: "internal", Message: message}
}

// toolError builds a typed tool error.
func toolError(code, message string) error {
	return &agentdomain.ToolError{Code: code, Message: message}
}

// mapExecutionError translates a Recorder/ReasonError failure into a tool
// error. Context cancellation and deadlines are returned unchanged so the P1
// tool executor reports them as cancelled/timeout.
func mapExecutionError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	// The Recorder already reports correctable submissions as typed tool
	// errors; preserve their code instead of flattening them to internal.
	var toolErr *agentdomain.ToolError
	if errors.As(err, &toolErr) {
		return err
	}
	var reason *executiondomain.ReasonError
	if errors.As(err, &reason) {
		switch reason.Code {
		case "invalid_argument":
			return toolError("invalid_arguments", reason.Error())
		case "not_found":
			return toolError("not_found", reason.Error())
		default:
			return toolError("internal", reason.Error())
		}
	}
	switch {
	case errors.Is(err, executiondomain.ErrCaseRunTerminal),
		errors.Is(err, executiondomain.ErrBatchFinished):
		return toolError("invalid_arguments", err.Error())
	}
	return toolError("internal", err.Error())
}

// mapBrowserError translates a BrowserExecutor transport/auth failure into a
// tool error. Action-level failures are mapped separately by the action code.
func mapBrowserError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var browserErr *application.BrowserError
	if errors.As(err, &browserErr) {
		return toolError(normalizeBrowserCode(browserErr.Code), browserErr.Error())
	}
	return toolError("internal", err.Error())
}

// normalizeBrowserCode maps the executor's protocol/transport codes onto the
// tool error codes of design spec §5.5.
func normalizeBrowserCode(code string) string {
	switch code {
	case "timeout":
		return "timeout"
	case "canceled", "cancelled":
		return "cancelled"
	case "host_not_allowed", "target_not_found", "target_ambiguous":
		return code
	case "invalid_arguments", "invalid_request":
		return "invalid_arguments"
	case "not_found":
		return "not_found"
	case "context_not_found", "context_limit", "executor_unavailable":
		return "executor_unavailable"
	case "dns_failed", "connection_failed", "navigation_failed":
		// Target reachability failures are environment failures (§5.8); the
		// tool error vocabulary has no dedicated target code.
		return "executor_unavailable"
	default:
		return "internal"
	}
}

// decodeArguments strictly decodes one tool argument object. Unknown fields are
// rejected because every schema sets additionalProperties=false.
func decodeArguments[T any](args json.RawMessage) (T, error) {
	var value T
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) == 0 {
		trimmed = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, invalidArguments("invalid arguments: " + err.Error())
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return value, invalidArguments("invalid arguments: unexpected trailing data")
	}
	return value, nil
}

// jsonResult marshals a successful tool result.
func jsonResult(value any) (agentdomain.ToolResult, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return agentdomain.ToolResult{}, internalError("marshal tool result: " + err.Error())
	}
	return agentdomain.ToolResult{Content: string(encoded)}, nil
}

// rawResult returns the executor's result object unchanged, defaulting to the
// empty object when the executor returned nothing.
func rawResult(result application.ActionResult) (agentdomain.ToolResult, error) {
	content := strings.TrimSpace(string(result.Result))
	if content == "" || content == "null" || !json.Valid([]byte(content)) {
		content = "{}"
	}
	return agentdomain.ToolResult{Content: content}, nil
}

// decimalID formats one id as the decimal string execution JSON uses.
func decimalID(id int64) string {
	return strconv.FormatInt(id, 10)
}

// parseDecimalID parses a positive decimal id.
func parseDecimalID(raw string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0, invalidArguments("id must be a positive decimal string")
	}
	return id, nil
}

// parseEvidenceIDs parses the model-supplied evidence id list.
func parseEvidenceIDs(raw []string) ([]int64, error) {
	ids := make([]int64, 0, len(raw))
	for _, item := range raw {
		id, err := parseDecimalID(item)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// hostAllowed reports whether rawURL targets the environment base URL host or a
// host in the allowlist. Allowlist entries are exact hosts (optionally with a
// port) or a "*.suffix" wildcard, matching the executor's security rule.
func hostAllowed(view application.CaseContextView, rawURL string) bool {
	parsed, err := parseHTTPURL(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return false
	}
	port := parsed.Port()
	if base, err := parseHTTPURL(view.BaseURL); err == nil && strings.EqualFold(base.Hostname(), host) {
		return true
	}
	for _, allowed := range view.AllowedHosts {
		if hostMatches(strings.ToLower(strings.TrimSpace(allowed)), host, port) {
			return true
		}
	}
	return false
}

// hostMatches matches one allowlist entry against a request hostname and port.
// An entry without a port matches any port; an entry with a port must match it.
func hostMatches(allowed, host, port string) bool {
	if allowed == "" {
		return false
	}
	allowedHost, allowedPort := splitHostPort(allowed)
	if allowedHost == "" {
		return false
	}
	if allowedPort != "" && allowedPort != port {
		return false
	}
	if strings.HasPrefix(allowedHost, "*.") {
		suffix := strings.TrimPrefix(allowedHost, "*.")
		return strings.HasSuffix(host, "."+suffix) && host != suffix
	}
	return host == allowedHost
}

// splitHostPort splits an allowlist entry that may carry a port.
func splitHostPort(entry string) (host, port string) {
	if strings.HasPrefix(entry, "[") {
		if index := strings.LastIndex(entry, "]"); index >= 0 {
			host = entry[1:index]
			if len(entry) > index+1 && entry[index+1] == ':' {
				port = entry[index+2:]
			}
			return host, port
		}
	}
	if index := strings.LastIndex(entry, ":"); index > 0 && !strings.Contains(entry[index+1:], ":") {
		return entry[:index], entry[index+1:]
	}
	return entry, ""
}

// mapActionFailure maps an executor action-level error code.
func mapActionFailure(result application.ActionResult) error {
	return toolError(normalizeBrowserCode(result.ErrorCode), result.ErrorMessage)
}

// caseContextValue is the case_context JSON read model.
type caseContextValue struct {
	CaseRunID          string              `json:"case_run_id"`
	BatchID            string              `json:"batch_id"`
	VersionID          string              `json:"version_id"`
	CaseNodeID         string              `json:"case_node_id"`
	Title              string              `json:"title"`
	Status             string              `json:"status"`
	Attempt            int                 `json:"attempt"`
	Steps              []caseStep          `json:"steps"`
	RemainingSteps     []caseStep          `json:"remaining_steps"`
	Assertions         []caseAssert        `json:"assertions"`
	CompletedSeqs      []int               `json:"completed_seqs"`
	CompletedSteps     []caseCompletedStep `json:"completed_steps"`
	InterruptedStepSeq *int                `json:"interrupted_step_seq,omitempty"`
	CleanupRequired    bool                `json:"cleanup_required"`
	BaseURL            string              `json:"base_url"`
	AllowedHosts       []string            `json:"allowed_hosts"`
	TestData           map[string]any      `json:"test_data,omitempty"`
	Preconditions      string              `json:"preconditions,omitempty"`
}

// caseCompletedStep is one recorded resume-checkpoint step: its actual result
// and evidence count let the agent continue without re-running it.
type caseCompletedStep struct {
	Seq           int    `json:"seq"`
	Status        string `json:"status"`
	Actual        string `json:"actual,omitempty"`
	EvidenceCount int    `json:"evidence_count"`
}

// caseStep is one snapshot step plus its redacted data.
type caseStep struct {
	Seq      int            `json:"seq"`
	Action   string         `json:"action"`
	Expected string         `json:"expected"`
	Data     map[string]any `json:"data,omitempty"`
}

// caseAssert is one snapshot assertion. Expression is included so the agent can
// judge non-UI assertions without reading the mindmap.
type caseAssert struct {
	AssertionID string `json:"assertion_id"`
	StepSeq     int    `json:"step_seq"`
	Kind        string `json:"kind"`
	Target      string `json:"target"`
	Expression  string `json:"expression,omitempty"`
	Expected    string `json:"expected"`
}

// evidenceOutput is the only artifact information returned to the model.
type evidenceOutput struct {
	EvidenceID string `json:"evidence_id"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
}

// batchIDFor returns the authoritative batch id of a case run.
func batchIDFor(invocation agentapp.Invocation, view application.CaseContextView) (int64, error) {
	if view.CaseRun.BatchID > 0 {
		return view.CaseRun.BatchID, nil
	}
	if invocation.BatchID > 0 {
		return invocation.BatchID, nil
	}
	return 0, invalidArguments("the case run scope has no batch id")
}

// captureArtifact persists one executor artifact and returns only its evidence
// metadata (design spec §5.7: evidence bytes never reach the model).
func (s *toolSet) captureArtifact(ctx context.Context, invocation agentapp.Invocation, view application.CaseContextView, artifact application.Artifact, kind, mimeType string, metadata map[string]any) (evidenceOutput, error) {
	if s.evidence == nil {
		return evidenceOutput{}, internalError("the evidence service is not configured")
	}
	if len(artifact.Data) == 0 {
		return evidenceOutput{}, internalError("the executor returned an empty artifact")
	}
	if int64(len(artifact.Data)) > s.limits.MaxArtifactBytes {
		return evidenceOutput{}, internalError(fmt.Sprintf("artifact exceeds the %d byte evidence limit", s.limits.MaxArtifactBytes))
	}
	batchID, err := batchIDFor(invocation, view)
	if err != nil {
		return evidenceOutput{}, err
	}
	if strings.TrimSpace(mimeType) == "" {
		mimeType = artifact.Mime
	}
	if strings.TrimSpace(mimeType) == "" {
		mimeType = "application/octet-stream"
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	if artifact.Filename != "" {
		metadata["filename"] = artifact.Filename
	}
	if artifact.SHA256 != "" {
		metadata["executor_sha256"] = artifact.SHA256
	}
	evidence, err := s.evidence.Capture(ctx, application.CaptureInput{
		BatchID:   batchID,
		CaseRunID: invocation.CaseRunID,
		Kind:      kind,
		Mime:      mimeType,
		Data:      artifact.Data,
		Metadata:  metadata,
	})
	if err != nil {
		return evidenceOutput{}, mapExecutionError(err)
	}
	return evidenceOutput{
		EvidenceID: decimalID(evidence.ID),
		SHA256:     evidence.SHA256,
		Size:       evidence.SizeBytes,
	}, nil
}

// decodeSessionStorage extracts the per-origin sessionStorage documents from a
// SessionRef.SessionStorage payload for the executor context init script.
func decodeSessionStorage(raw json.RawMessage) map[string]json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var document struct {
		Origins []struct {
			Origin         string          `json:"origin"`
			SessionStorage json.RawMessage `json:"sessionStorage"`
		} `json:"origins"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil
	}
	storage := make(map[string]json.RawMessage, len(document.Origins))
	for _, origin := range document.Origins {
		if origin.Origin == "" || len(bytes.TrimSpace(origin.SessionStorage)) == 0 {
			continue
		}
		storage[origin.Origin] = origin.SessionStorage
	}
	if len(storage) == 0 {
		return nil
	}
	return storage
}

// parseHTTPURL parses an http(s) URL. Any other scheme is invalid.
func parseHTTPURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("unsupported URL scheme %q", parsed.Scheme)
	}
	if parsed.Hostname() == "" {
		return nil, errors.New("URL must have a host")
	}
	return parsed, nil
}
