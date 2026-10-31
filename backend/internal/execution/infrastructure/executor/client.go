// Package executor is the HTTP client for the Playwright executor service
// (executor protocol A', P3 extension). The API owns the bearer token and never
// exposes the browser directly to the agent: every browser/db/http action is
// proxied through this client by the execution tools.
package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// DefaultTimeout bounds one executor request. The executor itself caps actions
// at 60s (goto 30s); a slightly larger client bound lets the executor return its
// own typed timeout error instead of the client aborting first.
const DefaultTimeout = 75 * time.Second

const (
	healthPath   = "/healthz"
	contextsPath = "/v1/contexts"
	// A screenshot or download artifact is capped at 10MB by the executor and
	// travels base64-encoded, so the response can exceed the request cap.
	maxResponseBytes = 32 << 20
)

// Health is the executor /healthz payload. A degraded executor still answers
// (503) and keeps its bearer-protected routes available for cleanup.
type Health struct {
	Status         string `json:"status"`
	Version        string `json:"version"`
	Contexts       int    `json:"contexts"`
	MaxContexts    int    `json:"maxContexts"`
	BrowserVersion string `json:"browserVersion,omitempty"`
	Error          string `json:"error,omitempty"`
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
	status, body, err := c.request(ctx, http.MethodGet, healthPath, nil, false)
	if err != nil {
		return Health{}, err
	}
	if status != http.StatusOK && status != http.StatusServiceUnavailable {
		return Health{}, executorError(status, body)
	}
	var health Health
	if err := json.Unmarshal(body, &health); err != nil {
		return Health{}, protocolError("invalid executor health response")
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
	status, body, err := c.request(ctx, http.MethodPost, contextsPath, payload, true)
	if err != nil {
		return ContextInfo{}, err
	}
	if err := ensureOK(status, body); err != nil {
		return ContextInfo{}, err
	}
	var created struct {
		ContextID      string `json:"contextId"`
		BrowserVersion string `json:"browserVersion"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.ContextID == "" {
		return ContextInfo{}, protocolError("invalid executor create-context response")
	}
	return ContextInfo{
		ID:       created.ContextID,
		Key:      in.Key,
		Metadata: in.Metadata,
	}, nil
}

// ListContexts calls GET /v1/contexts (P3 extension).
func (c *HTTPClient) ListContexts(ctx context.Context) ([]ContextInfo, error) {
	status, body, err := c.request(ctx, http.MethodGet, contextsPath, nil, true)
	if err != nil {
		return nil, err
	}
	if err := ensureOK(status, body); err != nil {
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
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, protocolError("invalid executor list-contexts response")
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
	status, body, err := c.request(ctx, http.MethodPost, path, payload, true)
	if err != nil {
		return ActionResult{}, err
	}
	var envelope struct {
		OK        bool            `json:"ok"`
		Result    json.RawMessage `json:"result"`
		Artifacts []Artifact      `json:"artifacts"`
		Error     *ActionError    `json:"error"`
	}
	decodeErr := json.Unmarshal(body, &envelope)
	if decodeErr != nil || (!envelope.OK && envelope.Error == nil) {
		if status >= http.StatusInternalServerError || (status >= 200 && status < 300) {
			return ActionResult{}, protocolError("invalid executor action response")
		}
		return ActionResult{}, executorError(status, body)
	}
	if status >= http.StatusInternalServerError {
		return ActionResult{}, executorError(status, body)
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
	status, body, err := c.request(ctx, http.MethodPost, path, nil, true)
	if err != nil {
		return false, err
	}
	if err := ensureOK(status, body); err != nil {
		return false, err
	}
	var payload struct {
		OK     bool `json:"ok"`
		Result struct {
			Canceled bool `json:"canceled"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false, protocolError("invalid executor cancel response")
	}
	return payload.Result.Canceled, nil
}

// Close calls DELETE /v1/contexts/{id}. A missing context is not an error for
// best-effort terminal cleanup.
func (c *HTTPClient) Close(ctx context.Context, contextID string) error {
	path := contextsPath + "/" + url.PathEscape(contextID)
	status, body, err := c.request(ctx, http.MethodDelete, path, nil, true)
	if err != nil {
		return err
	}
	return ensureOK(status, body)
}

// request performs one executor HTTP call. It returns the status and body; the
// caller decides whether the status is an error (health/action envelopes differ
// from the rest). Transport failures are mapped to executor_unavailable.
func (c *HTTPClient) request(ctx context.Context, method, path string, payload any, auth bool) (int, []byte, error) {
	if c.endpoint == "" {
		return 0, nil, protocolError("executor endpoint is not configured")
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, platform.Invalid("executor_request", "executor request could not be encoded")
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, body)
	if err != nil {
		return 0, nil, protocolError("executor endpoint is invalid")
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if auth {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, platform.NewError(http.StatusServiceUnavailable, "executor_unavailable", "executor request was canceled")
		}
		return 0, nil, unavailable("executor request failed: " + sanitizeTransportError(err))
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return 0, nil, unavailable("executor response could not be read")
	}
	return response.StatusCode, raw, nil
}

// ensureOK maps a non-2xx response to a typed error.
func ensureOK(status int, body []byte) error {
	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		return nil
	}
	return executorError(status, body)
}

// executorError converts an executor error envelope into a *platform.Error. 5xx
// responses always become the retryable executor_unavailable code so callers
// can classify them as environment failures.
func executorError(status int, body []byte) error {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &envelope)
	if status >= http.StatusInternalServerError || envelope.Error.Code == "" {
		return unavailable(fmt.Sprintf("executor returned HTTP %d", status))
	}
	return &platform.Error{
		Status:  status,
		Code:    envelope.Error.Code,
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

// sanitizeTransportError keeps the useful part of a transport failure without
// the request URL (which may embed credentials) or the Authorization header.
func sanitizeTransportError(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err.Error()
	}
	return "transport error"
}
