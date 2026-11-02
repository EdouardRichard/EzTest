package adapters

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

const (
	httpToolTimeout      = 15 * time.Second
	httpToolMaxBodyBytes = 1 << 20
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
