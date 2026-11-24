package adapters

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

const (
	// httpToolTimeout is the DEFAULT whole-request budget of http.request when
	// the caller does not pass timeout_ms (v0.2.4-C T12/P8: configurable per call).
	httpToolTimeout = 15 * time.Second

	// executorSelfCheckTimeout bounds the startup authentication probe. The
	// executor answers the session collection immediately, so startup must not wait
	// for the regular action timeout when the executor is unreachable.
	executorSelfCheckTimeout = 5 * time.Second
	executorSessionsPath     = "/v1/sessions"
)

// HTTPClientAdapter performs the target HTTP requests of http.request. It never
// follows redirects and reads the FULL response body — v0.2.4-C T12 (P8) lifted
// the old silent 1MiB LimitReader cap; any size bound is applied explicitly
// downstream (spill + truncated flag), never silently here.
type HTTPClientAdapter struct {
	client *http.Client
}

// NewHTTPClient wires the target HTTP adapter. The whole-request budget is
// applied per request (default 15s, or the caller's timeout_ms) rather than as a
// fixed client timeout, so a longer per-call timeout is not capped at 15s.
func NewHTTPClient() *HTTPClientAdapter {
	return &HTTPClientAdapter{client: &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

var _ executionapp.HTTPDoer = (*HTTPClientAdapter)(nil)

// Do performs one HTTP request and returns status/headers/body/duration. The
// FULL body is read (no silent cap); the request budget is timeout_ms or 15s.
func (a *HTTPClientAdapter) Do(ctx context.Context, request executionapp.HTTPRequestData) (executionapp.HTTPResponseData, error) {
	started := time.Now()
	timeout := httpToolTimeout
	if request.TimeoutMS > 0 {
		timeout = time.Duration(request.TimeoutMS) * time.Millisecond
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var body io.Reader
	if len(request.Body) > 0 {
		body = strings.NewReader(string(request.Body))
	}
	httpRequest, err := http.NewRequestWithContext(requestCtx, request.Method, request.URL, body)
	if err != nil {
		return executionapp.HTTPResponseData{}, platform.Invalid("path", "could not build the HTTP request")
	}
	for name, value := range request.Headers {
		httpRequest.Header.Set(name, value)
	}
	response, err := a.client.Do(httpRequest)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil || requestCtx.Err() != nil {
			return executionapp.HTTPResponseData{}, platform.NewError(http.StatusServiceUnavailable, "unavailable", "the target SUT request timed out")
		}
		return executionapp.HTTPResponseData{}, platform.NewError(http.StatusServiceUnavailable, "unavailable", "the target SUT is unreachable")
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		if requestCtx.Err() != nil {
			return executionapp.HTTPResponseData{}, platform.NewError(http.StatusServiceUnavailable, "unavailable", "the target SUT request timed out")
		}
		return executionapp.HTTPResponseData{}, platform.NewError(http.StatusServiceUnavailable, "unavailable", "could not read the target SUT response")
	}
	return executionapp.HTTPResponseData{
		Status:     response.StatusCode,
		Headers:    response.Header,
		Body:       data,
		DurationMS: time.Since(started).Milliseconds(),
	}, nil
}

// ExecutorSelfCheck performs the startup authentication probe of the executor. The executor's /healthz is unauthenticated and therefore cannot
// prove that the configured bearer token is accepted: only a protected route
// does. SelfCheck calls the session collection with the token and reports the three
// outcomes the acceptance criteria ask for (nil / unauthorized / offline).
type ExecutorSelfCheck struct {
	endpoint string
	token    string
	client   *http.Client
}

// NewExecutorSelfCheck wires the executor authentication self-check.
func NewExecutorSelfCheck(endpoint, token string) *ExecutorSelfCheck {
	return &ExecutorSelfCheck{
		endpoint: strings.TrimRight(endpoint, "/"),
		token:    token,
		client: &http.Client{
			Timeout: executorSelfCheckTimeout,
			// Never replay the bearer token to a redirect target.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// SelfCheck calls GET /v1/sessions with the bearer token. A 200 is a success
// (the body is irrelevant to the probe); 401/403 wrap ErrExecutorUnauthorized;
// transport failures, timeouts and unexpected statuses wrap ErrExecutorOffline.
func (c *ExecutorSelfCheck) SelfCheck(ctx context.Context) error {
	if c == nil || c.client == nil {
		return fmt.Errorf("%w: executor self-check is not configured", ErrExecutorOffline)
	}
	if c.endpoint == "" {
		return fmt.Errorf("%w: executor endpoint is not configured", ErrExecutorOffline)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+executorSessionsPath, nil)
	if err != nil {
		return fmt.Errorf("%w: executor endpoint is invalid", ErrExecutorOffline)
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrExecutorOffline, executorSelfCheckTransportError(err))
	}
	defer func() { _ = response.Body.Close() }()
	// Drain a bounded prefix so the connection can be reused; the probe body
	// carries no useful data.
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	switch response.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: executor returned HTTP %d", ErrExecutorUnauthorized, response.StatusCode)
	default:
		// A reachable but unusable executor (5xx, redirect, ...) belongs to the
		// offline bucket: it is not an authentication failure.
		return fmt.Errorf("%w: executor returned HTTP %d", ErrExecutorOffline, response.StatusCode)
	}
}

// executorSelfCheckTransportError keeps the useful part of a transport failure
// without the request URL (which may embed credentials) or the Authorization
// header.
func executorSelfCheckTransportError(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err.Error()
	}
	return "transport error"
}
