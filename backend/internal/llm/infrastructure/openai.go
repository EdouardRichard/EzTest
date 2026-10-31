package llminfra

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
)

// DefaultTimeout bounds every upstream request made by the client. The
// per-config timeout_seconds govern execution in P1; P0 only needs a small
// connectivity probe.
const DefaultTimeout = 10 * time.Second

// probeModel is only used by Test when the upstream has no /models endpoint.
// Models discovered through GET {base}/models take precedence.
const probeModel = "gpt-4o-mini"

const (
	maxResponseBytes = 4 << 20
	maxErrorBytes    = 64 << 10
)

// Client is an OpenAI-compatible upstream client. It implements
// llmapp.Client.
type Client struct {
	http *http.Client
}

var _ llmapp.Client = (*Client)(nil)

// NewClient returns a client with the default 10s timeout.
func NewClient() *Client {
	return NewClientWithHTTPClient(&http.Client{Timeout: DefaultTimeout})
}

// NewClientWithHTTPClient injects the underlying HTTP client (used by tests to
// exercise timeouts and httptest servers).
func NewClientWithHTTPClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultTimeout}
	}
	return &Client{http: httpClient}
}

// ListModels calls GET {base}/models and maps the OpenAI-compatible payload.
// Errors include the upstream status code but never the configured API key or
// response body.
func (c *Client) ListModels(ctx context.Context, cfg llm.Config) ([]llmapp.RemoteModel, error) {
	raw, err := c.do(ctx, cfg, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Data []remoteModelPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("decode llm models response failed: %w", err)
	}
	models := make([]llmapp.RemoteModel, 0, len(payload.Data))
	for _, item := range payload.Data {
		name := strings.TrimSpace(item.ID)
		if name == "" {
			continue
		}
		models = append(models, item.remoteModel())
	}
	return models, nil
}

// Test probes the upstream: a successful GET {base}/models passes; otherwise a
// minimal chat completion is attempted. Any failure is returned as an error
// that is safe to surface (status code only, no key).
func (c *Client) Test(ctx context.Context, cfg llm.Config) (llmapp.TestResult, error) {
	start := time.Now()
	models, modelsErr := c.ListModels(ctx, cfg)
	if modelsErr == nil {
		return llmapp.TestResult{
			OK:        true,
			LatencyMS: time.Since(start).Milliseconds(),
			Models:    models,
		}, nil
	}

	chatErr := c.chatProbe(ctx, cfg)
	if chatErr == nil {
		return llmapp.TestResult{
			OK:        true,
			LatencyMS: time.Since(start).Milliseconds(),
		}, nil
	}

	return llmapp.TestResult{
			OK:        false,
			LatencyMS: time.Since(start).Milliseconds(),
			Error:     redact(chatErr.Error(), cfg.APIKey),
		}, fmt.Errorf("llm connectivity test failed: models: %v; chat: %v",
			modelsErr, redact(chatErr.Error(), cfg.APIKey))
}

func (c *Client) chatProbe(ctx context.Context, cfg llm.Config) error {
	payload := map[string]any{
		"model": probeModel,
		"messages": []map[string]string{
			{"role": "user", "content": "ping"},
		},
		"max_tokens": 1,
		"stream":     false,
	}
	_, err := c.do(ctx, cfg, http.MethodPost, "/chat/completions", payload)
	return err
}

// do performs one upstream request. The Authorization header is controlled
// exclusively by cfg.APIKey; custom headers cannot override it.
func (c *Client) do(ctx context.Context, cfg llm.Config, method, suffix string, payload any) ([]byte, error) {
	request, err := c.newRequest(ctx, cfg, method, suffix, payload)
	if err != nil {
		return nil, err
	}

	response, err := c.http.Do(request)
	if err != nil {
		if isTimeout(err) {
			return nil, errors.New("llm provider request timed out")
		}
		var unknownHost *url.Error
		if errors.As(err, &unknownHost) {
			return nil, errors.New("llm provider request failed: " + unknownHost.Err.Error())
		}
		return nil, errors.New("llm provider request failed")
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxErrorBytes))
		return nil, fmt.Errorf("llm provider returned HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return nil, errors.New("read llm provider response failed")
	}
	return raw, nil
}

// newRequest builds one upstream HTTP request with the configured headers.
func (c *Client) newRequest(ctx context.Context, cfg llm.Config, method, suffix string, payload any) (*http.Request, error) {
	endpoint, err := endpointURL(cfg.BaseURL, suffix)
	if err != nil {
		return nil, errors.New("llm base url is invalid")
	}

	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, errors.New("encode llm request failed")
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, errors.New("build llm request failed")
	}
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range cfg.Headers {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" || strings.EqualFold(trimmed, "authorization") {
			continue
		}
		request.Header.Set(trimmed, value)
	}
	if key := strings.TrimSpace(cfg.APIKey); key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	return request, nil
}

// endpointURL appends suffix to the configured base URL. Base URLs may or may
// not carry a trailing slash and often already include /v1.
func endpointURL(base, suffix string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return "", err
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", errors.New("unsupported llm base url")
	}
	if parsed.User != nil {
		return "", errors.New("llm base url must not contain credentials")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + suffix
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

type remoteModelPayload struct {
	ID              string          `json:"id"`
	ContextWindow   int             `json:"context_window"`
	MaxOutputTokens int             `json:"max_output_tokens"`
	MaxOutput       int             `json:"max_output"`
	SupportsTools   bool            `json:"supports_tools"`
	SupportsVision  bool            `json:"supports_vision"`
	InputModalities []string        `json:"input_modalities"`
	Capabilities    json.RawMessage `json:"capabilities"`
}

func (p remoteModelPayload) remoteModel() llmapp.RemoteModel {
	maxOutput := p.MaxOutputTokens
	if maxOutput <= 0 {
		maxOutput = p.MaxOutput
	}
	vision := p.SupportsVision
	for _, modality := range p.InputModalities {
		if strings.EqualFold(strings.TrimSpace(modality), "image") {
			vision = true
		}
	}
	tools := p.SupportsTools
	if len(p.Capabilities) > 0 {
		var capabilities struct {
			FunctionCalling bool `json:"function_calling"`
		}
		if err := json.Unmarshal(p.Capabilities, &capabilities); err == nil && capabilities.FunctionCalling {
			tools = true
		}
	}
	return llmapp.RemoteModel{
		ID:             strings.TrimSpace(p.ID),
		ContextWindow:  p.ContextWindow,
		MaxOutput:      maxOutput,
		SupportsTools:  tools,
		SupportsVision: vision,
	}
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// redact removes the configured secret from a message if it somehow leaked.
func redact(message, secret string) string {
	if secret == "" || message == "" {
		return message
	}
	return strings.ReplaceAll(message, secret, "[redacted]")
}
