// Package browser implements the Go client for the executor protocol (design
// spec §5.6). It speaks JSON over HTTP to the standalone Node/Playwright
// executor, authenticates with a bearer token and returns the application
// port's types.
//
// The package has no logger: the token, request bodies and response bodies are
// never logged. Errors only carry the method, path, HTTP status and the
// executor's own error message, never the token or a body.
package browser

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/application"
)

const (
	// defaultTimeout bounds one call when the caller's context has no deadline.
	defaultTimeout = 30 * time.Second
	// maxResponseBytes caps a decoded executor response. The executor limits one
	// artifact to 10MB, which is roughly 13.4MB once base64 encoded.
	maxResponseBytes = 64 << 20

	healthPath   = "/healthz"
	contextsPath = "/v1/contexts"
)

// Type aliases let callers depend on the browser client package alone; the
// canonical definitions live in application/browser_port.go.
type (
	ContextSpec     = application.ContextSpec
	Viewport        = application.Viewport
	ContextRef      = application.ContextRef
	Action          = application.Action
	ActionResult    = application.ActionResult
	Artifact        = application.Artifact
	BrowserExecutor = application.BrowserExecutor
	BrowserError    = application.BrowserError
)

// Client is the production BrowserExecutor backed by the executor's HTTP/JSON
// protocol.
type Client struct {
	baseURL    string
	token      string
	timeout    time.Duration
	httpClient *http.Client
}

var _ application.BrowserExecutor = (*Client)(nil)
var _ application.VisualComparator = (*Client)(nil)

// New returns a client for the executor at baseURL. token is sent as the bearer
// token on every request. timeout bounds one call when ctx carries no deadline;
// a non-positive timeout uses defaultTimeout. The HTTP client deliberately has
// no transport-level retries: action calls are not idempotent.
func New(baseURL, token string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Client{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:      token,
		timeout:    timeout,
		httpClient: &http.Client{},
	}
}

// Health implements application.BrowserExecutor. It validates the executor's
// status, version and context capacity so a half-broken executor is reported as
// unavailable instead of silently accepting batches.
func (c *Client) Health(ctx context.Context) error {
	status, body, err := c.do(ctx, http.MethodGet, healthPath, nil)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return errorFromResponse(status, body)
	}
	var payload struct {
		Status      string `json:"status"`
		Version     string `json:"version"`
		Contexts    int    `json:"contexts"`
		MaxContexts int    `json:"maxContexts"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return &application.BrowserError{
			Code:       "internal",
			Message:    "executor health response is not valid JSON",
			StatusCode: status,
		}
	}
	if payload.Status != "ok" || payload.Contexts < 0 || payload.MaxContexts <= 0 || payload.Version == "" {
		return &application.BrowserError{
			Code:       "internal",
			Message:    "executor health response is missing status, version or capacity",
			StatusCode: status,
		}
	}
	return nil
}

// NewContext implements application.BrowserExecutor.
func (c *Client) NewContext(ctx context.Context, spec application.ContextSpec) (application.ContextRef, error) {
	payload := createContextPayload{
		Key:          spec.Key,
		AllowedHosts: spec.AllowedHosts,
		BaseURL:      spec.BaseURL,
	}
	if len(spec.StorageState) > 0 {
		payload.StorageState = spec.StorageState
	}
	if len(spec.SessionStorage) > 0 {
		payload.SessionStorage = spec.SessionStorage
	}
	if spec.Viewport.Width > 0 && spec.Viewport.Height > 0 {
		payload.Viewport = &viewportPayload{Width: spec.Viewport.Width, Height: spec.Viewport.Height}
	}

	status, body, err := c.do(ctx, http.MethodPost, contextsPath, payload)
	if err != nil {
		return application.ContextRef{}, err
	}
	if status < 200 || status >= 300 {
		return application.ContextRef{}, errorFromResponse(status, body)
	}
	var created struct {
		ContextID      string `json:"contextId"`
		BrowserVersion string `json:"browserVersion"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		return application.ContextRef{}, &application.BrowserError{
			Code:       "internal",
			Message:    "executor context response is not valid JSON",
			StatusCode: status,
		}
	}
	if created.ContextID == "" {
		return application.ContextRef{}, &application.BrowserError{
			Code:       "internal",
			Message:    "executor context response is missing contextId",
			StatusCode: status,
		}
	}
	return application.ContextRef{ID: created.ContextID}, nil
}

// Action implements application.BrowserExecutor. A protocol action failure is
// returned in ActionResult.ErrorCode with a nil error; transport failures and
// authentication failures return a typed *application.BrowserError.
func (c *Client) Action(ctx context.Context, ref application.ContextRef, action application.Action) (application.ActionResult, error) {
	if ref.ID == "" {
		return application.ActionResult{}, application.NewBrowserError("invalid_request", "executor context id is required")
	}
	if action.Name == "" {
		return application.ActionResult{}, application.NewBrowserError("invalid_request", "executor action name is required")
	}
	payload := actionPayload{Action: action.Name, Args: action.Args}
	status, body, err := c.do(ctx, http.MethodPost, contextPath(ref)+"/actions", payload)
	if err != nil {
		return application.ActionResult{}, err
	}
	// A rejected bearer token is a client failure, not an action outcome.
	if status == http.StatusUnauthorized {
		return application.ActionResult{}, errorFromResponse(status, body)
	}
	var envelope actionEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return application.ActionResult{}, &application.BrowserError{
			Code:       "internal",
			Message:    "executor action response is not valid JSON",
			StatusCode: status,
		}
	}
	if envelope.Error != nil {
		if envelope.Error.Code == "" {
			return application.ActionResult{}, &application.BrowserError{
				Code:       "internal",
				Message:    "executor action error response is missing a code",
				StatusCode: status,
			}
		}
		return application.ActionResult{
			ErrorCode:    envelope.Error.Code,
			ErrorMessage: envelope.Error.Message,
		}, nil
	}
	if status < 200 || status >= 300 {
		return application.ActionResult{}, errorFromResponse(status, body)
	}
	if !envelope.OK {
		return application.ActionResult{}, &application.BrowserError{
			Code:       "internal",
			Message:    "executor action response is missing ok",
			StatusCode: status,
		}
	}
	artifacts, err := decodeArtifacts(envelope.Artifacts)
	if err != nil {
		return application.ActionResult{}, err
	}
	return application.ActionResult{Result: envelope.Result, Artifacts: artifacts}, nil
}

// CompareVisual implements application.VisualComparator. The executor captures
// the actual screenshot itself; only the approved baseline copy and the
// comparison configuration are sent. Protocol failures are returned as typed
// *application.BrowserError values so the caller's classification never treats
// an invalid request as a visual mismatch.
func (c *Client) CompareVisual(ctx context.Context, ref application.ContextRef, req application.VisualCompareRequest) (application.VisualCompareResult, error) {
	if ref.ID == "" {
		return application.VisualCompareResult{}, application.NewBrowserError("invalid_request", "executor context id is required")
	}
	if len(req.Baseline) == 0 {
		return application.VisualCompareResult{}, application.NewBrowserError("invalid_request", "a visual baseline PNG is required")
	}
	maxBaseline := req.MaxBaselineBytes
	if maxBaseline <= 0 {
		maxBaseline = application.DefaultVisualBaselineMaxBytes
	}
	if int64(len(req.Baseline)) > maxBaseline {
		return application.VisualCompareResult{}, application.NewBrowserError("invalid_request",
			fmt.Sprintf("visual baseline is %d bytes, above the %d byte limit", len(req.Baseline), maxBaseline))
	}
	if req.Threshold < 0 || req.Threshold > 0.1 {
		return application.VisualCompareResult{}, application.NewBrowserError("invalid_request", "visual threshold must be between 0 and 0.1")
	}
	if req.Viewport.Width <= 0 || req.Viewport.Height <= 0 {
		return application.VisualCompareResult{}, application.NewBrowserError("invalid_request", "visual viewport width and height must be positive")
	}
	if req.DeviceScaleFactor <= 0 {
		return application.VisualCompareResult{}, application.NewBrowserError("invalid_request", "visual device scale factor must be positive")
	}

	args := visualComparePayload{
		BaselineBase64:    base64.StdEncoding.EncodeToString(req.Baseline),
		Selector:          req.Selector,
		Threshold:         req.Threshold,
		Viewport:          viewportPayload{Width: req.Viewport.Width, Height: req.Viewport.Height},
		DeviceScaleFactor: req.DeviceScaleFactor,
	}
	if req.Region != nil {
		args.Region = &visualRegionPayload{
			X: req.Region.X, Y: req.Region.Y,
			Width: req.Region.Width, Height: req.Region.Height,
		}
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return application.VisualCompareResult{}, application.NewBrowserError("internal", "visual compare request could not be encoded")
	}
	outcome, err := c.Action(ctx, ref, application.Action{Name: "visual.compare", Args: encoded})
	if err != nil {
		return application.VisualCompareResult{}, err
	}
	if outcome.ErrorCode != "" {
		return application.VisualCompareResult{}, &application.BrowserError{
			Code:    outcome.ErrorCode,
			Message: outcome.ErrorMessage,
		}
	}
	var payload visualCompareResultPayload
	if len(outcome.Result) == 0 || json.Unmarshal(outcome.Result, &payload) != nil {
		return application.VisualCompareResult{}, application.NewBrowserError("internal",
			"executor visual compare response is not valid JSON")
	}
	if payload.DiffRatio < 0 || payload.DiffRatio > 1 {
		return application.VisualCompareResult{}, application.NewBrowserError("internal",
			"executor visual compare response carries an invalid diff ratio")
	}
	if payload.Width < 0 || payload.Height < 0 || payload.ActualWidth < 0 || payload.ActualHeight < 0 {
		return application.VisualCompareResult{}, application.NewBrowserError("internal",
			"executor visual compare response carries a negative image size")
	}
	result := application.VisualCompareResult{
		DiffRatio:               payload.DiffRatio,
		Width:                   payload.Width,
		Height:                  payload.Height,
		EnvironmentMatch:        payload.EnvironmentMatch,
		ActualWidth:             payload.ActualWidth,
		ActualHeight:            payload.ActualHeight,
		ActualDeviceScaleFactor: payload.ActualDeviceScaleFactor,
	}
	for _, box := range payload.MismatchBoxes {
		result.MismatchBoxes = append(result.MismatchBoxes, application.VisualMismatchBox{
			X: box.X, Y: box.Y, Width: box.Width, Height: box.Height,
		})
	}
	for _, artifact := range outcome.Artifacts {
		switch artifact.Kind {
		case "visual_actual":
			result.ActualPNG = artifact.Data
		case "visual_diff":
			result.DiffPNG = artifact.Data
		}
	}
	return result, nil
}

// Cancel implements application.BrowserExecutor.
func (c *Client) Cancel(ctx context.Context, ref application.ContextRef) error {
	if ref.ID == "" {
		return application.NewBrowserError("invalid_request", "executor context id is required")
	}
	return c.callOK(ctx, http.MethodPost, contextPath(ref)+"/cancel")
}

// Close implements application.BrowserExecutor.
func (c *Client) Close(ctx context.Context, ref application.ContextRef) error {
	if ref.ID == "" {
		return application.NewBrowserError("invalid_request", "executor context id is required")
	}
	return c.callOK(ctx, http.MethodDelete, contextPath(ref))
}

// callOK performs a control request whose success response must carry ok:true.
func (c *Client) callOK(ctx context.Context, method, path string) error {
	status, body, err := c.do(ctx, method, path, nil)
	if err != nil {
		return err
	}
	var envelope struct {
		OK    bool           `json:"ok"`
		Error *protocolError `json:"error"`
	}
	decodeErr := json.Unmarshal(body, &envelope)
	if decodeErr == nil && envelope.Error != nil && envelope.Error.Code != "" {
		return &application.BrowserError{
			Code:       envelope.Error.Code,
			Message:    envelope.Error.Message,
			StatusCode: status,
		}
	}
	if status < 200 || status >= 300 {
		return errorFromResponse(status, body)
	}
	if decodeErr != nil {
		return &application.BrowserError{
			Code:       "internal",
			Message:    "executor response is not valid JSON",
			StatusCode: status,
		}
	}
	if !envelope.OK {
		return &application.BrowserError{
			Code:       "internal",
			Message:    "executor response is missing ok",
			StatusCode: status,
		}
	}
	return nil
}

// do sends one protocol request and returns the HTTP status and body. payload is
// marshaled as JSON when non-nil. The call uses ctx's deadline when it has one
// and otherwise the client's configured timeout; it never retries.
func (c *Client) do(ctx context.Context, method, path string, payload any) (int, []byte, error) {
	callCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, application.NewBrowserError("invalid_request", "executor request body could not be encoded")
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(callCtx, method, c.baseURL+path, body)
	if err != nil {
		return 0, nil, application.NewBrowserError("internal", fmt.Sprintf("executor request could not be built: %v", err))
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return 0, nil, transportError(method, path, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return 0, nil, application.NewBrowserError("internal", "executor response body could not be read")
	}
	if len(data) > maxResponseBytes {
		return 0, nil, application.NewBrowserError("internal", "executor response body exceeds the client limit")
	}
	return response.StatusCode, data, nil
}

// transportError maps a net/http failure to a typed BrowserError. The wrapped
// error only contains the method, path and URL; it never contains the token or a
// request body.
func transportError(method, path string, err error) *application.BrowserError {
	code := "executor_unavailable"
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		code = "timeout"
	case errors.Is(err, context.Canceled):
		code = "canceled"
	}
	return application.NewBrowserError(code, fmt.Sprintf("executor %s %s failed: %v", method, path, err))
}

// errorFromResponse maps a non-2xx response to a typed BrowserError, preferring
// the executor's error code when the body carries an error envelope.
func errorFromResponse(status int, body []byte) *application.BrowserError {
	var envelope struct {
		Error *protocolError `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error != nil && envelope.Error.Code != "" {
		return &application.BrowserError{
			Code:       envelope.Error.Code,
			Message:    envelope.Error.Message,
			StatusCode: status,
		}
	}
	code := "internal"
	if status == http.StatusUnauthorized {
		code = "unauthorized"
	}
	return &application.BrowserError{
		Code:       code,
		Message:    fmt.Sprintf("executor returned HTTP %d", status),
		StatusCode: status,
	}
}

// decodeArtifacts decodes and verifies every artifact. A decode failure or a
// sha256 mismatch is a typed internal error: the bytes must never be trusted.
func decodeArtifacts(payloads []artifactPayload) ([]application.Artifact, error) {
	if len(payloads) == 0 {
		return nil, nil
	}
	artifacts := make([]application.Artifact, 0, len(payloads))
	for _, payload := range payloads {
		data, err := base64.StdEncoding.DecodeString(payload.Base64)
		if err != nil {
			return nil, application.NewBrowserError("internal", fmt.Sprintf("executor artifact %q is not valid base64", payload.Kind))
		}
		digest := sha256.Sum256(data)
		sum := hex.EncodeToString(digest[:])
		if !strings.EqualFold(sum, payload.SHA256) {
			return nil, application.NewBrowserError("internal", fmt.Sprintf("executor artifact %q sha256 does not match its bytes", payload.Kind))
		}
		artifacts = append(artifacts, application.Artifact{
			Kind:     payload.Kind,
			Mime:     payload.Mime,
			Filename: payload.Filename,
			Data:     data,
			SHA256:   sum,
		})
	}
	return artifacts, nil
}

// contextPath returns the URL path of one context.
func contextPath(ref application.ContextRef) string {
	return contextsPath + "/" + url.PathEscape(ref.ID)
}

type createContextPayload struct {
	Key            string                     `json:"key,omitempty"`
	AllowedHosts   []string                   `json:"allowedHosts,omitempty"`
	BaseURL        string                     `json:"baseUrl,omitempty"`
	StorageState   json.RawMessage            `json:"storageState,omitempty"`
	SessionStorage map[string]json.RawMessage `json:"sessionStorage,omitempty"`
	Viewport       *viewportPayload           `json:"viewport,omitempty"`
}

type viewportPayload struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type actionPayload struct {
	Action string          `json:"action"`
	Args   json.RawMessage `json:"args,omitempty"`
}

type artifactPayload struct {
	Kind     string `json:"kind"`
	Mime     string `json:"mime"`
	Filename string `json:"filename"`
	Base64   string `json:"base64"`
	SHA256   string `json:"sha256"`
}

type protocolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type actionEnvelope struct {
	OK        bool              `json:"ok"`
	Result    json.RawMessage   `json:"result"`
	Artifacts []artifactPayload `json:"artifacts"`
	Error     *protocolError    `json:"error"`
}

// visualComparePayload is the visual.compare action argument object. The
// baseline copy is the only image the API sends (design spec §9.3).
type visualComparePayload struct {
	BaselineBase64    string               `json:"baseline_base64"`
	Selector          string               `json:"selector,omitempty"`
	Region            *visualRegionPayload `json:"region,omitempty"`
	Threshold         float64              `json:"threshold"`
	Viewport          viewportPayload      `json:"viewport"`
	DeviceScaleFactor float64              `json:"device_scale_factor"`
}

type visualRegionPayload struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// visualCompareResultPayload is the executor's visual.compare result object.
type visualCompareResultPayload struct {
	DiffRatio               float64             `json:"diff_ratio"`
	Width                   int                 `json:"width"`
	Height                  int                 `json:"height"`
	EnvironmentMatch        bool                `json:"environment_match"`
	ActualWidth             int                 `json:"actual_width"`
	ActualHeight            int                 `json:"actual_height"`
	ActualDeviceScaleFactor float64             `json:"actual_device_scale_factor"`
	MismatchBoxes           []visualMismatchBox `json:"mismatch_boxes"`
}

type visualMismatchBox struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}
