// Package renderclient is the delivery context's HTTP client of the standalone
// PDF renderer service (P7 D59). The API never installs Chromium: it posts one
// frozen HTML document to `POST /v1/pdf` and validates the returned bytes.
package renderclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
)

const (
	// MaxPDFBytes is the hard cap of one rendered PDF (spec plan Task 8).
	MaxPDFBytes = 20 << 20
	// DefaultTimeout is used when the configured timeout is not positive.
	DefaultTimeout = 60 * time.Second
	// maxErrorBody bounds the renderer error response read into the message.
	maxErrorBody = 4 << 10
)

// Client calls the renderer service. URL is the renderer base URL
// (report.render_worker_url), Token is the Bearer secret
// (report.render_token) and Timeout is the per-request deadline.
type Client struct {
	URL     string
	Token   string
	Timeout time.Duration
}

var _ deliveryapp.RenderPDFRenderer = (*Client)(nil)

// New builds a renderer client. A non-positive timeout falls back to the 60s
// frozen default; the URL is trimmed of a trailing slash.
func New(baseURL, token string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{
		URL:     strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		Token:   token,
		Timeout: timeout,
	}
}

// RenderPDF posts one HTML document and returns the verified PDF bytes. It
// validates the %PDF- signature and the 20 MiB cap. Transport/timeout/5xx/busy
// failures wrap deliveryapp.ErrRenderUnavailable (retryable); 4xx and invalid
// responses wrap deliveryapp.ErrRenderRejected (terminal).
func (c *Client) RenderPDF(ctx context.Context, html []byte) ([]byte, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: renderer client is not configured", deliveryapp.ErrRenderRejected)
	}
	if len(html) == 0 {
		return nil, fmt.Errorf("%w: html is required", deliveryapp.ErrRenderRejected)
	}
	endpoint, err := rendererEndpoint(c.URL)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", deliveryapp.ErrRenderRejected, err)
	}
	payload, err := json.Marshal(map[string]string{"html": string(html)})
	if err != nil {
		return nil, fmt.Errorf("%w: encode renderer request: %v", deliveryapp.ErrRenderRejected, err)
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%w: build renderer request: %v", deliveryapp.ErrRenderRejected, err)
	}
	request.Header.Set("Authorization", "Bearer "+c.Token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/pdf")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", deliveryapp.ErrRenderUnavailable, err)
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		summary := readErrorSummary(response.Body)
		message := fmt.Sprintf("renderer returned %d", response.StatusCode)
		if summary != "" {
			message += ": " + summary
		}
		if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
			return nil, fmt.Errorf("%w: %s", deliveryapp.ErrRenderUnavailable, message)
		}
		return nil, fmt.Errorf("%w: %s", deliveryapp.ErrRenderRejected, message)
	}

	pdf, err := io.ReadAll(io.LimitReader(response.Body, MaxPDFBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read renderer response: %v", deliveryapp.ErrRenderUnavailable, err)
	}
	if len(pdf) > MaxPDFBytes {
		return nil, fmt.Errorf("%w: rendered PDF exceeds the %d-byte limit", deliveryapp.ErrRenderRejected, MaxPDFBytes)
	}
	if len(pdf) < 5 || string(pdf[:5]) != "%PDF-" {
		return nil, fmt.Errorf("%w: renderer response is not a PDF", deliveryapp.ErrRenderRejected)
	}
	return pdf, nil
}

// rendererEndpoint validates the base URL and appends the frozen /v1/pdf path.
func rendererEndpoint(baseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("renderer URL must be an absolute http(s) URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("renderer URL must use http or https")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/v1/pdf"
	return parsed.String(), nil
}

// readErrorSummary extracts the shared error envelope message when present and
// otherwise returns a trimmed, bounded body preview.
func readErrorSummary(body io.Reader) string {
	data, err := io.ReadAll(io.LimitReader(body, maxErrorBody))
	if err != nil {
		return ""
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err == nil && envelope.Error.Message != "" {
		if envelope.Error.Code != "" {
			return envelope.Error.Code + ": " + envelope.Error.Message
		}
		return envelope.Error.Message
	}
	text := strings.Join(strings.Fields(string(data)), " ")
	if len(text) > 512 {
		text = text[:512]
	}
	return text
}
