package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// HTTP tool limits and defaults (design spec §8.5).
const (
	defaultHTTPTimeout   = 5 * time.Second
	maxHTTPBodyBytes     = 1 << 20
	maxHTTPRedirects     = 3
	httpTruncationMarker = "\n...[truncated]"
)

// httpGetSchema is the JSON Schema of http.get's arguments.
const httpGetSchema = `{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}`

// httpGet is the read-only, allowlisted HTTP GET tool. It never forwards
// headers or credentials: only the URL supplied by the model reaches the
// network.
type httpGet struct {
	client *http.Client
	// hosts holds the normalised allowlist entries. An entry may be a bare
	// host (any port) or host:port (exact port).
	hosts []string
}

// NewHTTPGet returns the http.get tool. The allowlist decides which hosts may
// be reached; an empty allowlist denies every host (the configuration layer
// applies the spec default of 127.0.0.1 and localhost). A non-positive timeout
// selects the 5 s default.
func NewHTTPGet(allowlist []string, timeout time.Duration) domain.Tool {
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	tool := &httpGet{
		client: &http.Client{Timeout: timeout},
		hosts:  normalizeHTTPAllowlist(allowlist),
	}
	tool.client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		// len(via) is the number of requests already made; allow three
		// redirect hops and refuse the fourth. Every hop is re-checked.
		if len(via) > maxHTTPRedirects {
			return newToolError(toolErrorCodeDenied, fmt.Sprintf("http.get: stopped after %d redirects", maxHTTPRedirects))
		}
		return tool.checkURL(req.URL)
	}
	return tool
}

// normalizeHTTPAllowlist trims and lower-cases the configured entries.
func normalizeHTTPAllowlist(allowlist []string) []string {
	hosts := make([]string, 0, len(allowlist))
	for _, entry := range allowlist {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry != "" {
			hosts = append(hosts, entry)
		}
	}
	return hosts
}

// Name implements domain.Tool.
func (*httpGet) Name() string { return "http.get" }

// Description implements domain.Tool.
func (*httpGet) Description() string {
	return "Fetch a URL with a plain GET request and return its text body. " +
		"Only http/https URLs on the configured host allowlist are allowed; URLs containing credentials are refused. " +
		"Redirects are limited to 3 hops and re-checked. Responses must be text-like and are truncated at 1 MB."
}

// InputSchema implements domain.Tool.
func (*httpGet) InputSchema() json.RawMessage { return json.RawMessage(httpGetSchema) }

// Execute implements domain.Tool.
func (t *httpGet) Execute(ctx context.Context, args json.RawMessage) (domain.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.ToolResult{}, err
	}

	var in struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(args, &in); err != nil || in.URL == "" {
		return domain.ToolResult{}, newToolError(toolErrorCodeInvalidArguments,
			"http.get: arguments must be an object with a non-empty url")
	}

	parsed, err := url.Parse(in.URL)
	if err != nil {
		return domain.ToolResult{}, newToolError(toolErrorCodeInvalidArguments,
			fmt.Sprintf("http.get: invalid URL: %v", err))
	}
	if err := t.checkURL(parsed); err != nil {
		return domain.ToolResult{}, err
	}

	// No headers are set: no Authorization, Cookie or other local credentials
	// are ever forwarded, and the model cannot add request headers.
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return domain.ToolResult{}, newToolError(toolErrorCodeInvalidArguments,
			fmt.Sprintf("http.get: cannot build request: %v", err))
	}

	response, err := t.client.Do(request)
	if err != nil {
		return domain.ToolResult{}, t.responseError(ctx, err)
	}
	defer response.Body.Close()

	if !textContentType(response.Header.Get("Content-Type")) {
		return domain.ToolResult{}, newToolError(toolErrorCodeUnsupportedContent,
			fmt.Sprintf("http.get: unsupported content type %q", response.Header.Get("Content-Type")))
	}

	// Read one byte past the cap so an oversized body is detected and
	// truncated instead of being silently accepted.
	body, err := io.ReadAll(io.LimitReader(response.Body, maxHTTPBodyBytes+1))
	if err != nil {
		return domain.ToolResult{}, t.responseError(ctx, err)
	}
	content := string(body)
	if len(body) > maxHTTPBodyBytes {
		content = string(body[:maxHTTPBodyBytes]) + httpTruncationMarker
	}
	return domain.ToolResult{Content: content}, nil
}

// checkURL enforces the scheme, credential and allowlist rules for one URL.
// It runs for the initial request and for every redirect hop.
func (t *httpGet) checkURL(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return newToolError(toolErrorCodeInvalidArguments,
			fmt.Sprintf("http.get: unsupported URL scheme %q", u.Scheme))
	}
	if u.User != nil {
		return newToolError(toolErrorCodeInvalidArguments, "http.get: URLs with credentials are not allowed")
	}
	if u.Hostname() == "" {
		return newToolError(toolErrorCodeInvalidArguments, "http.get: URL must include a host")
	}
	if !t.hostAllowed(u) {
		return newToolError(toolErrorCodeDenied,
			fmt.Sprintf("http.get: host %q is not in the allowlist", u.Host))
	}
	return nil
}

// hostAllowed reports whether the URL host matches an allowlist entry. A
// host:port entry must match the host and port exactly; a bare host entry
// matches the host on any port.
func (t *httpGet) hostAllowed(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	for _, entry := range t.hosts {
		if entryHost, entryPort, err := net.SplitHostPort(entry); err == nil {
			if host == entryHost && port == entryPort {
				return true
			}
			continue
		}
		if host == strings.Trim(entry, "[]") {
			return true
		}
	}
	return false
}

// responseError maps a client/transport failure to a tool error. Context
// cancellation is returned verbatim so the executor reports cancelled/timeout.
func (t *httpGet) responseError(ctx context.Context, err error) error {
	var toolErr *domain.ToolError
	if errors.As(err, &toolErr) {
		return toolErr
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return newToolError(toolErrorCodeTimeout, "http.get: request timed out")
	}
	return newToolError(toolErrorCodeInternal, fmt.Sprintf("http.get: request failed: %v", err))
}

// textContentType reports whether a response Content-Type is text-like enough
// to hand to the model. Images, archives and other binary payloads are refused.
func textContentType(raw string) bool {
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		mediaType = strings.TrimSpace(strings.Split(raw, ";")[0])
	}
	mediaType = strings.ToLower(mediaType)
	if strings.HasPrefix(mediaType, "text/") {
		return true
	}
	switch mediaType {
	case "application/json", "application/xml", "application/yaml":
		return true
	}
	return false
}
