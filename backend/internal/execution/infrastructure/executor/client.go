// Package executor is the HTTP client for the Playwright executor service
// (executor protocol A', P3 extension). The API owns the bearer token and never
// exposes the browser directly to the agent: every browser/db/http action is
// proxied through this client by the execution tools.
package executor

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
)

// MaxActionDuration is the executor's per-action cap. The API-side case_run
// lease must be longer than the action plus transport overhead: the default
// case lease is 90s and the client bound below is 75s.
const MaxActionDuration = 60 * time.Second

// DefaultTimeout bounds one executor request. The executor itself caps actions
// at 60s (goto 30s); a slightly larger client bound lets the executor return its
// own typed timeout error instead of the client aborting first. The case_run
// lease (75s+) must stay above this bound so a slow action is never mistaken for
// a lost executor by lease recovery.
const DefaultTimeout = MaxActionDuration + 15*time.Second

const (
	healthPath   = "/healthz"
	contextsPath = "/v1/contexts"
	// A screenshot or download artifact is capped at 10MB by the executor and
	// travels base64-encoded, so the response can exceed the request cap.
	maxResponseBytes = 32 << 20

	// RequestIDHeader correlates one API-side executor call with the executor's
	// own request start/finish log lines (v0.2.3 X-01).
	RequestIDHeader = "X-EzTest-Request-Id"

	// CodeExecutorTimeout is the typed platform code for a transport timeout or
	// cancellation. Non-timeout transport failures keep executor_unavailable.
	CodeExecutorTimeout = "executor_timeout"
)

// CallDiagnostics is the dispatch provenance attached to one executor call. The
// router/adapter layers know the owner and context; every field is optional and
// is copied into the structured error details/log line when known. It never
// carries credentials. CreateContext is the only call that receives
// case_run_id/attempt (from the create metadata); Action/Cancel/Close attach
// only context_id (plus owner/endpoint/registry_hit) for now.
type CallDiagnostics struct {
	OwnerExecutorID int64
	CaseRunID       int64
	Attempt         int
	ContextID       string
	// RegistryHit reports whether the owner was resolved from the executor
	// registry (false means primary-endpoint fallback).
	RegistryHit *bool
}

type callDiagnosticsKey struct{}

// WithCallDiagnostics attaches dispatch provenance to one executor call.
func WithCallDiagnostics(ctx context.Context, diagnostics CallDiagnostics) context.Context {
	return context.WithValue(ctx, callDiagnosticsKey{}, diagnostics)
}

func callDiagnosticsFrom(ctx context.Context) (CallDiagnostics, bool) {
	diagnostics, ok := ctx.Value(callDiagnosticsKey{}).(CallDiagnostics)
	return diagnostics, ok
}

// Health is the executor /healthz payload. A degraded executor still answers
// (503) and keeps its bearer-protected routes available for cleanup. Creating
// and LastContextCreateError expose an in-flight/hung context creation (X-01).
type Health struct {
	Status                 string `json:"status"`
	Version                string `json:"version"`
	Contexts               int    `json:"contexts"`
	MaxContexts            int    `json:"maxContexts"`
	BrowserVersion         string `json:"browserVersion,omitempty"`
	Error                  string `json:"error,omitempty"`
	Creating               int    `json:"creating"`
	LastContextCreateError string `json:"lastContextCreateError,omitempty"`
}

// Viewport is the optional browser viewport of a new context.
type Viewport struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// CreateContextInput is one POST /v1/contexts request. Metadata is opaque to the
// executor (P3 extension, ≤2KB) and is echoed by GET /v1/contexts.
type CreateContextInput struct {
	Key          string
	AllowedHosts []string
	BaseURL      string
	StorageState json.RawMessage
	Metadata     map[string]any
	Viewport     *Viewport
}

// ContextInfo is one executor context. Key/Metadata/CreatedAt are returned by
// GET /v1/contexts; CreateContext fills them from the request because the
// executor's create response only carries the id.
type ContextInfo struct {
	ID        string
	Key       string
	Metadata  map[string]any
	CreatedAt string
}

// ActionError is the executor's typed action failure (target_not_found,
// target_ambiguous, host_not_allowed, ...). It is not a transport error: the
// browser tool maps it to a step failure/observed result.
type ActionError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Artifact is one binary result of an action, base64-encoded for transport.
// The API re-computes SHA256 and size before persisting it as evidence.
type Artifact struct {
	Kind     string `json:"kind"`
	MIME     string `json:"mime"`
	Base64   string `json:"base64"`
	SHA256   string `json:"sha256"`
	Filename string `json:"filename,omitempty"`
	Bytes    int    `json:"bytes"`
}

// ActionResult is one action envelope.
type ActionResult struct {
	OK        bool
	Result    json.RawMessage
	Artifacts []Artifact
	Error     *ActionError
}

// Client is the executor protocol surface used by the execution runtime.
type Client interface {
	Health(ctx context.Context) (Health, error)
	CreateContext(ctx context.Context, in CreateContextInput) (ContextInfo, error)
	Action(ctx context.Context, contextID, action string, args json.RawMessage) (ActionResult, error)
	Cancel(ctx context.Context, contextID string) (bool, error)
	Close(ctx context.Context, contextID string) error
	ListContexts(ctx context.Context) ([]ContextInfo, error)
}

// HTTPClient is the production Client. It never follows redirects so the bearer
// token cannot be replayed to another host/route, and it never logs bodies or
// the token.
type HTTPClient struct {
	endpoint string
	token    string
	http     *http.Client
}

var _ Client = (*HTTPClient)(nil)

// NewClient returns a client for endpoint with a per-request timeout. A
// non-positive timeout falls back to DefaultTimeout.
func NewClient(endpoint, token string, timeout time.Duration) *HTTPClient {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return NewClientWithHTTPClient(endpoint, token, &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	})
}

// NewClientWithHTTPClient injects the underlying HTTP client (tests use it to
// exercise timeouts and redirect handling).
func NewClientWithHTTPClient(endpoint, token string, httpClient *http.Client) *HTTPClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultTimeout}
	}
	return &HTTPClient{
		endpoint: strings.TrimRight(endpoint, "/"),
		token:    token,
		http:     httpClient,
	}
}

// ContextKey is the executor context key of one (case_run, version_sut, role)
// binding (spec §6.1). It is what orphan reconciliation matches on.
func ContextKey(caseRunID, versionSUTID int64, role string) string {
	return fmt.Sprintf("run:%d:vs:%d:role:%s", caseRunID, versionSUTID, role)
}

// Health probes GET /healthz. It does not send the bearer token; a 503 degraded
// reply is a valid Health (the registry still records the heartbeat).
func (c *HTTPClient) Health(ctx context.Context) (Health, error) {
	result, err := c.request(ctx, http.MethodGet, healthPath, nil, false)
	if err != nil {
		return Health{}, err
	}
	if result.status != http.StatusOK && result.status != http.StatusServiceUnavailable {
		return Health{}, result.failure("response", executorError(result))
	}
	var health Health
	if err := json.Unmarshal(result.body, &health); err != nil {
		return Health{}, result.failure("body", protocolError("invalid executor health response"))
	}
	return health, nil
}

// CreateContext calls POST /v1/contexts.
func (c *HTTPClient) CreateContext(ctx context.Context, in CreateContextInput) (ContextInfo, error) {
	payload := struct {
		Key          string          `json:"key,omitempty"`
		AllowedHosts []string        `json:"allowedHosts"`
		BaseURL      string          `json:"baseUrl,omitempty"`
		StorageState json.RawMessage `json:"storageState,omitempty"`
		Metadata     map[string]any  `json:"metadata,omitempty"`
		Viewport     *Viewport       `json:"viewport,omitempty"`
	}{
		Key:          in.Key,
		AllowedHosts: in.AllowedHosts,
		BaseURL:      in.BaseURL,
		StorageState: in.StorageState,
		Metadata:     in.Metadata,
		Viewport:     in.Viewport,
	}
	result, err := c.request(ctx, http.MethodPost, contextsPath, payload, true)
	if err != nil {
		return ContextInfo{}, err
	}
	if err := ensureOK(result); err != nil {
		return ContextInfo{}, err
	}
	var created struct {
		ContextID      string `json:"contextId"`
		BrowserVersion string `json:"browserVersion"`
	}
	if err := json.Unmarshal(result.body, &created); err != nil || created.ContextID == "" {
		return ContextInfo{}, result.failure("body", protocolError("invalid executor create-context response"))
	}
	return ContextInfo{
		ID:       created.ContextID,
		Key:      in.Key,
		Metadata: in.Metadata,
	}, nil
}

// ListContexts calls GET /v1/contexts (P3 extension).
func (c *HTTPClient) ListContexts(ctx context.Context) ([]ContextInfo, error) {
	result, err := c.request(ctx, http.MethodGet, contextsPath, nil, true)
	if err != nil {
		return nil, err
	}
	if err := ensureOK(result); err != nil {
		return nil, err
	}
	var payload struct {
		Contexts []struct {
			ID        string         `json:"id"`
			Key       string         `json:"key"`
			Metadata  map[string]any `json:"metadata"`
			CreatedAt string         `json:"createdAt"`
		} `json:"contexts"`
	}
	if err := json.Unmarshal(result.body, &payload); err != nil {
		return nil, result.failure("body", protocolError("invalid executor list-contexts response"))
	}
	contexts := make([]ContextInfo, 0, len(payload.Contexts))
	for _, item := range payload.Contexts {
		contexts = append(contexts, ContextInfo{
			ID:        item.ID,
			Key:       item.Key,
			Metadata:  item.Metadata,
			CreatedAt: item.CreatedAt,
		})
	}
	return contexts, nil
}

// Action calls POST /v1/contexts/{id}/actions. An action-level failure (even a
// 4xx one) is returned inside ActionResult so the tool layer can classify it;
// transport, 5xx and malformed responses are returned as errors.
func (c *HTTPClient) Action(ctx context.Context, contextID, action string, args json.RawMessage) (ActionResult, error) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	payload := struct {
		Action string          `json:"action"`
		Args   json.RawMessage `json:"args"`
	}{Action: action, Args: args}
	path := contextsPath + "/" + url.PathEscape(contextID) + "/actions"
	result, err := c.request(ctx, http.MethodPost, path, payload, true)
	if err != nil {
		return ActionResult{}, err
	}
	var envelope struct {
		OK        bool            `json:"ok"`
		Result    json.RawMessage `json:"result"`
		Artifacts []Artifact      `json:"artifacts"`
		Error     *ActionError    `json:"error"`
	}
	decodeErr := json.Unmarshal(result.body, &envelope)
	if decodeErr != nil || (!envelope.OK && envelope.Error == nil) {
		if result.status >= http.StatusInternalServerError || (result.status >= 200 && result.status < 300) {
			return ActionResult{}, result.failure("body", protocolError("invalid executor action response"))
		}
		return ActionResult{}, result.failure("response", executorError(result))
	}
	if result.status >= http.StatusInternalServerError {
		return ActionResult{}, result.failure("response", executorError(result))
	}
	return ActionResult{
		OK:        envelope.OK,
		Result:    envelope.Result,
		Artifacts: envelope.Artifacts,
		Error:     envelope.Error,
	}, nil
}

// Cancel calls POST /v1/contexts/{id}/cancel and reports whether an in-flight
// action was actually canceled.
func (c *HTTPClient) Cancel(ctx context.Context, contextID string) (bool, error) {
	path := contextsPath + "/" + url.PathEscape(contextID) + "/cancel"
	result, err := c.request(ctx, http.MethodPost, path, nil, true)
	if err != nil {
		return false, err
	}
	if err := ensureOK(result); err != nil {
		return false, err
	}
	var payload struct {
		OK     bool `json:"ok"`
		Result struct {
			Canceled bool `json:"canceled"`
		} `json:"result"`
	}
	if err := json.Unmarshal(result.body, &payload); err != nil {
		return false, result.failure("body", protocolError("invalid executor cancel response"))
	}
	return payload.Result.Canceled, nil
}

// Close calls DELETE /v1/contexts/{id}. A missing context is not an error for
// best-effort terminal cleanup.
func (c *HTTPClient) Close(ctx context.Context, contextID string) error {
	path := contextsPath + "/" + url.PathEscape(contextID)
	result, err := c.request(ctx, http.MethodDelete, path, nil, true)
	if err != nil {
		return err
	}
	return ensureOK(result)
}

// httpResult is one completed executor HTTP exchange. It carries the
// sanitized request identity so every failure path can attach the same
// structured, request-scoped diagnostics.
type httpResult struct {
	ctx       context.Context
	status    int
	body      []byte
	method    string
	path      string
	endpoint  string
	requestID string
	duration  time.Duration
	timeout   time.Duration
}

// failure attaches structured details and logs one sanitized diagnostic line.
// It returns the same error so callers can `return ..., result.failure(...)`.
func (r *httpResult) failure(phase string, err *platform.Error) error {
	err.Details = r.details(phase)
	attrs := []any{
		slog.String("executor.endpoint", r.endpoint),
		slog.String("executor.method", r.method),
		slog.String("executor.path", r.path),
		slog.String("executor.phase", phase),
		slog.String("executor.request_id", r.requestID),
		slog.Int64("executor.duration_ms", r.duration.Milliseconds()),
		slog.Int64("executor.timeout_ms", r.timeout.Milliseconds()),
		slog.Int("executor.status_code", r.status),
		slog.String("executor.code", err.Code),
		slog.Bool("executor.retryable", err.Retryable),
		slog.String("error", err.Message),
	}
	attrs = append(attrs, r.dispatchLogAttrs()...)
	slog.Default().WarnContext(r.ctx, "executor request failed", attrs...)
	return err
}

func (r *httpResult) dispatchLogAttrs() []any {
	details := r.details("")
	attrs := make([]any, 0, 8)
	if value, ok := details["owner_executor_id"]; ok {
		attrs = append(attrs, slog.Any("executor.owner_executor_id", value))
	}
	if value, ok := details["case_run_id"]; ok {
		attrs = append(attrs, slog.Any("executor.case_run_id", value))
	}
	if value, ok := details["attempt"]; ok {
		attrs = append(attrs, slog.Any("executor.attempt", value))
	}
	if value, ok := details["context_id"]; ok {
		attrs = append(attrs, slog.Any("executor.context_id", value))
	}
	if value, ok := details["registry_hit"]; ok {
		attrs = append(attrs, slog.Any("executor.registry_hit", value))
	}
	return attrs
}

// details builds the sanitized request-scoped diagnostics. It never includes
// the bearer token, the request body or the raw (credential-bearing) URL.
func (r *httpResult) details(phase string) map[string]any {
	details := map[string]any{
		"endpoint":   r.endpoint,
		"method":     r.method,
		"path":       r.path,
		"request_id": r.requestID,
	}
	if phase != "" {
		details["phase"] = phase
	}
	if r.status != 0 {
		details["status_code"] = r.status
	}
	if r.timeout > 0 {
		details["timeout_ms"] = r.timeout.Milliseconds()
	}
	details["duration_ms"] = r.duration.Milliseconds()
	if diagnostics, ok := callDiagnosticsFrom(r.ctx); ok {
		details["owner_executor_id"] = diagnostics.OwnerExecutorID
		if diagnostics.CaseRunID > 0 {
			details["case_run_id"] = diagnostics.CaseRunID
		}
		if diagnostics.Attempt > 0 {
			details["attempt"] = diagnostics.Attempt
		}
		if diagnostics.ContextID != "" {
			details["context_id"] = diagnostics.ContextID
		}
		if diagnostics.RegistryHit != nil {
			details["registry_hit"] = *diagnostics.RegistryHit
		}
	}
	return details
}

// request performs one executor HTTP call. Transport failures are typed as
// executor_timeout (timeout/cancellation) or executor_unavailable (everything
// else) and always carry the structured request diagnostics above.
func (c *HTTPClient) request(ctx context.Context, method, path string, payload any, auth bool) (*httpResult, error) {
	if c.endpoint == "" {
		return nil, protocolError("executor endpoint is not configured")
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, platform.Invalid("executor_request", "executor request could not be encoded")
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, body)
	if err != nil {
		return nil, protocolError("executor endpoint is invalid")
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if auth {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	requestID := requestid.New()
	request.Header.Set(RequestIDHeader, requestID)

	result := &httpResult{
		ctx:       ctx,
		method:    method,
		path:      path,
		endpoint:  sanitizedEndpoint(c.endpoint),
		requestID: requestID,
		timeout:   c.effectiveTimeout(ctx),
	}
	started := time.Now()
	response, err := c.http.Do(request)
	result.duration = time.Since(started)
	if err != nil {
		return nil, result.failure(transportPhase(err), transportError(ctx, err))
	}
	defer func() { _ = response.Body.Close() }()
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	result.duration = time.Since(started)
	result.status, result.body = response.StatusCode, raw
	if readErr != nil {
		// io.ReadAll can fail after the headers because http.Client.Timeout also
		// bounds the body (artifacts travel up to 32 MiB). A timeout or
		// cancellation there is the same transport failure as one before the
		// headers, so it keeps the executor_timeout taxonomy instead of being
		// flattened into executor_unavailable.
		if transportTimedOutOrCanceled(ctx, readErr) {
			return nil, result.failure("body", transportError(ctx, readErr))
		}
		return nil, result.failure("body", unavailable("executor response could not be read"))
	}
	return result, nil
}

// effectiveTimeout is the smaller of the HTTP client bound and the caller's
// remaining context budget, used only for diagnostics.
func (c *HTTPClient) effectiveTimeout(ctx context.Context) time.Duration {
	timeout := c.http.Timeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > 0 && (timeout <= 0 || remaining < timeout) {
			return remaining
		}
	}
	return timeout
}

// transportError maps a failed exchange onto the typed transport taxonomy.
func transportError(ctx context.Context, err error) *platform.Error {
	if !transportTimedOutOrCanceled(ctx, err) {
		return unavailable("executor request failed: " + sanitizeTransportError(err))
	}
	canceled := errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled)
	apiErr := &platform.Error{
		// Every executor_timeout uses the same 504 status as the executor's own
		// typed envelope, whether the deadline was detected locally or remotely.
		Status:    http.StatusGatewayTimeout,
		Code:      CodeExecutorTimeout,
		Retryable: !canceled,
	}
	if canceled {
		apiErr.Message = "executor request was canceled"
	} else {
		apiErr.Message = "executor request timed out: " + sanitizeTransportError(err)
	}
	return apiErr
}

// transportTimedOutOrCanceled reports whether err (or the caller's context) is a
// deadline or cancellation, including a response-body read that was interrupted
// by http.Client.Timeout.
func transportTimedOutOrCanceled(ctx context.Context, err error) bool {
	return errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(ctx.Err(), context.DeadlineExceeded) ||
		isTimeoutError(err) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(ctx.Err(), context.Canceled)
}

func isTimeoutError(err error) bool {
	var timeoutErr interface{ Timeout() bool }
	return errors.As(err, &timeoutErr) && timeoutErr.Timeout()
}

// transportPhase classifies where a transport failure happened: dial, tls,
// request, response headers or body.
func transportPhase(err error) string {
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return "dial"
	}
	var certificateErr *tls.CertificateVerificationError
	if errors.As(err, &certificateErr) {
		return "tls"
	}
	var recordErr tls.RecordHeaderError
	if errors.As(err, &recordErr) {
		return "tls"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "tls:") || strings.Contains(message, "x509:"):
		return "tls"
	case strings.Contains(message, "awaiting headers"):
		return "response"
	case strings.Contains(message, "reading response body"):
		return "body"
	default:
		return "request"
	}
}

// ensureOK maps a non-2xx response to a typed error.
func ensureOK(result *httpResult) error {
	if result.status >= http.StatusOK && result.status < http.StatusMultipleChoices {
		return nil
	}
	return result.failure("response", executorError(result))
}

// executorError converts an executor error envelope into a *platform.Error.
// Every 5xx is the retryable executor_unavailable environment failure, except
// an explicit executor_timeout reply. Forwarding an arbitrary executor code
// would leak it into the API failure taxonomy (Task 11 classifies these).
func executorError(result *httpResult) *platform.Error {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(result.body, &envelope)
	code := envelope.Error.Code
	if result.status >= http.StatusInternalServerError {
		if code == CodeExecutorTimeout {
			return &platform.Error{
				Status:    http.StatusGatewayTimeout,
				Code:      CodeExecutorTimeout,
				Message:   envelope.Error.Message,
				Retryable: true,
			}
		}
		return unavailable(fmt.Sprintf("executor returned HTTP %d", result.status))
	}
	if code == "" {
		return unavailable(fmt.Sprintf("executor returned HTTP %d", result.status))
	}
	return &platform.Error{
		Status:  result.status,
		Code:    code,
		Message: envelope.Error.Message,
	}
}

func unavailable(message string) *platform.Error {
	return &platform.Error{
		Status:    http.StatusServiceUnavailable,
		Code:      "executor_unavailable",
		Message:   message,
		Retryable: true,
	}
}

func protocolError(message string) *platform.Error {
	return &platform.Error{
		Status:  http.StatusServiceUnavailable,
		Code:    "executor_unavailable",
		Message: message,
	}
}

// sanitizedEndpoint drops userinfo, query and path so diagnostics never leak
// credentials; executor endpoints are scheme://host[:port] by contract.
func sanitizedEndpoint(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

// sanitizeTransportError keeps the useful part of a transport failure without
// the request URL (which may embed credentials) or the Authorization header.
func sanitizeTransportError(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err.Error()
	}
	return "transport error"
}
