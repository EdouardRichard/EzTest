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
	httpToolTimeout      = 15 * time.Second
	httpToolMaxBodyBytes = 1 << 20

	// executorSelfCheckTimeout bounds the startup authentication probe. The
	// executor answers GET /v1/contexts immediately, so startup must not wait
	// for the regular action timeout when the executor is unreachable.
	executorSelfCheckTimeout = 5 * time.Second
	executorContextsPath     = "/v1/contexts"
)

// HTTPClientAdapter performs the target-SUT HTTP requests of http.request. It
// never follows redirects (the allow-list check already ran on the resolved SUT
// URL) and bounds the response body.
type HTTPClientAdapter struct {
	client *http.Client
}

// NewHTTPClient wires the target-SUT HTTP adapter.
func NewHTTPClient() *HTTPClientAdapter {
	return &HTTPClientAdapter{client: &http.Client{
		Timeout: httpToolTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

var _ executionapp.HTTPDoer = (*HTTPClientAdapter)(nil)

// Do performs one HTTP request and returns status/headers/body/duration.
func (a *HTTPClientAdapter) Do(ctx context.Context, request executionapp.HTTPRequestData) (executionapp.HTTPResponseData, error) {
	started := time.Now()
	var body io.Reader
	if len(request.Body) > 0 {
		body = strings.NewReader(string(request.Body))
	}
	httpRequest, err := http.NewRequestWithContext(ctx, request.Method, request.URL, body)
	if err != nil {
		return executionapp.HTTPResponseData{}, platform.Invalid("path", "could not build the HTTP request")
	}
	for name, value := range request.Headers {
		httpRequest.Header.Set(name, value)
	}
	response, err := a.client.Do(httpRequest)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return executionapp.HTTPResponseData{}, platform.NewError(http.StatusServiceUnavailable, "unavailable", "the target SUT request timed out")
		}
		return executionapp.HTTPResponseData{}, platform.NewError(http.StatusServiceUnavailable, "unavailable", "the target SUT is unreachable")
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, httpToolMaxBodyBytes))
	if err != nil {
		return executionapp.HTTPResponseData{}, platform.NewError(http.StatusServiceUnavailable, "unavailable", "could not read the target SUT response")
	}
	return executionapp.HTTPResponseData{
		Status:     response.StatusCode,
		Headers:    response.Header,
		Body:       data,
		DurationMS: time.Since(started).Milliseconds(),
	}, nil
}

// ExecutorSelfCheck performs the startup authentication probe of the Playwright
// executor. The executor's /healthz is unauthenticated and therefore cannot
// prove that the configured bearer token is accepted: only a protected route
// does. SelfCheck calls GET /v1/contexts with the token and reports the three
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

// SelfCheck calls GET /v1/contexts with the bearer token. A 200 is a success
// (the body is irrelevant to the probe); 401/403 wrap ErrExecutorUnauthorized;
// transport failures, timeouts and unexpected statuses wrap ErrExecutorOffline.
func (c *ExecutorSelfCheck) SelfCheck(ctx context.Context) error {
	if c == nil || c.client == nil {
		return fmt.Errorf("%w: executor self-check is not configured", ErrExecutorOffline)
	}
	if c.endpoint == "" {
		return fmt.Errorf("%w: executor endpoint is not configured", ErrExecutorOffline)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+executorContextsPath, nil)
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
