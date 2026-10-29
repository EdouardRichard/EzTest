package agent_tool

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/application"
)

// schemaHTTPRequest is the argument object of http.request.
const schemaHTTPRequest = `{"type":"object","properties":{"method":{"type":"string","enum":["GET","POST","PUT","PATCH","DELETE","HEAD","OPTIONS"]},"url":{"type":"string"},"headers":{"type":"object","additionalProperties":{"type":"string"}},"body":{"type":"string"},` + timeoutSchema + `},"required":["url"],"additionalProperties":false}`

// errRedirectHostNotAllowed aborts a redirect outside the environment allowlist.
var errRedirectHostNotAllowed = errors.New("execution: redirect host is not allowed")

// httpAllowedMethods is the frozen method list of http.request.
var httpAllowedMethods = map[string]struct{}{
	"GET": {}, "POST": {}, "PUT": {}, "PATCH": {}, "DELETE": {}, "HEAD": {}, "OPTIONS": {},
}

// httpDefinitions returns the single HTTP tool of design spec §7.1.
func httpDefinitions() []toolDefinition {
	return []toolDefinition{{
		name:        "http.request",
		description: "Send one HTTP request to the environment base URL or an allowed host. Session authentication headers are injected automatically and the exchange is stored as redacted evidence.",
		schema:      schemaHTTPRequest,
		run:         runHTTPRequest,
	}}
}

// httpRequestInput is the validated argument shape of http.request.
type httpRequestInput struct {
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body"`
	TimeoutMS int               `json:"timeout_ms"`
}

// httpRequestOutput is the model-facing HTTP result. The exchange body is
// redacted and the raw bytes live only in evidence.
type httpRequestOutput struct {
	Status     int                 `json:"status"`
	Headers    map[string][]string `json:"headers"`
	Body       string              `json:"body,omitempty"`
	BodyBase64 string              `json:"body_base64,omitempty"`
	Truncated  bool                `json:"truncated"`
	EvidenceID string              `json:"evidence_id"`
	SHA256     string              `json:"sha256"`
	Size       int64               `json:"size"`
}

// httpExchangeEvidence is the persisted evidence envelope.
type httpExchangeEvidence struct {
	Request  httpExchangeRequest  `json:"request"`
	Response httpExchangeResponse `json:"response"`
}

// httpExchangeRequest is the redacted request half of the evidence envelope.
type httpExchangeRequest struct {
	Method     string              `json:"method"`
	URL        string              `json:"url"`
	Headers    map[string][]string `json:"headers"`
	Body       string              `json:"body,omitempty"`
	BodyBase64 string              `json:"body_base64,omitempty"`
}

// httpExchangeResponse is the redacted response half of the evidence envelope.
type httpExchangeResponse struct {
	Status     int                 `json:"status"`
	Headers    map[string][]string `json:"headers"`
	Body       string              `json:"body,omitempty"`
	BodyBase64 string              `json:"body_base64,omitempty"`
	Truncated  bool                `json:"truncated"`
}

// runHTTPRequest executes http.request: allowlist check, session header
// injection, redacted evidence capture and a redacted model result.
func runHTTPRequest(ctx context.Context, set *toolSet, args json.RawMessage) (agentdomain.ToolResult, error) {
	invocation, err := caseScope(ctx)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	input, err := decodeArguments[httpRequestInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	method := strings.ToUpper(strings.TrimSpace(input.Method))
	if method == "" {
		method = http.MethodGet
	}
	if _, ok := httpAllowedMethods[method]; !ok {
		return agentdomain.ToolResult{}, invalidArguments("method must be one of GET, POST, PUT, PATCH, DELETE, HEAD or OPTIONS")
	}
	if _, err := parseHTTPURL(input.URL); err != nil {
		return agentdomain.ToolResult{}, invalidArguments("url must be an absolute http(s) URL: " + err.Error())
	}
	if input.TimeoutMS < 0 || input.TimeoutMS > maxActionTimeoutMS {
		return agentdomain.ToolResult{}, invalidArguments("timeout_ms must be between 1 and 60000")
	}
	for name := range input.Headers {
		if strings.TrimSpace(name) == "" {
			return agentdomain.ToolResult{}, invalidArguments("header names must not be empty")
		}
	}

	view, err := loadCaseContext(ctx, set.recorder, invocation.CaseRunID)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if !hostAllowed(view, input.URL) {
		return agentdomain.ToolResult{}, toolError("host_not_allowed",
			"request host is outside the environment base URL and allowed hosts")
	}

	// http.Header keys are canonicalized, so a model-supplied "authorization"
	// and a session-supplied "Authorization" are the same entry: setting the
	// session value last removes the model duplicate instead of sending both
	// (design spec §5.9: the model cannot override or strip the session auth).
	headers := make(http.Header, len(input.Headers)+2)
	for name, value := range input.Headers {
		headers.Set(name, value)
	}
	if set.limits.Session != nil {
		session, err := set.limits.Session(ctx, invocation.CaseRunID)
		if err != nil {
			return agentdomain.ToolResult{}, mapExecutionError(err)
		}
		for name, value := range session.Headers {
			headers.Set(name, value)
		}
	}
	if len(input.Body) > 0 && headers.Get("Content-Type") == "" {
		headers.Set("Content-Type", "application/json")
	}

	timeout := set.limits.HTTPTimeout
	if input.TimeoutMS > 0 {
		timeout = time.Duration(input.TimeoutMS) * time.Millisecond
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, method, input.URL, strings.NewReader(input.Body))
	if err != nil {
		return agentdomain.ToolResult{}, invalidArguments("invalid request: " + err.Error())
	}
	request.Header = headers

	client := http.Client{}
	if set.httpClient != nil {
		client = *set.httpClient
	}
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if !hostAllowed(view, next.URL.String()) {
			return errRedirectHostNotAllowed
		}
		if set.httpClient != nil && set.httpClient.CheckRedirect != nil {
			return set.httpClient.CheckRedirect(next, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}

	response, err := client.Do(request)
	if err != nil {
		return agentdomain.ToolResult{}, mapHTTPError(err)
	}
	defer response.Body.Close()

	data, err := io.ReadAll(io.LimitReader(response.Body, set.limits.MaxHTTPBodyBytes+1))
	if err != nil {
		return agentdomain.ToolResult{}, toolError("internal", "read response body: "+err.Error())
	}
	truncated := int64(len(data)) > set.limits.MaxHTTPBodyBytes
	if truncated {
		data = data[:set.limits.MaxHTTPBodyBytes]
	}

	redactedRequestHeaders, redactedRequestBody := application.RedactHTTP(request.Header, []byte(input.Body))
	redactedResponseHeaders, redactedResponseBody := application.RedactHTTP(response.Header, data)
	requestText, requestBase64 := encodeEvidenceBody(redactedRequestBody)
	responseText, responseBase64 := encodeEvidenceBody(redactedResponseBody)
	envelope, err := json.Marshal(httpExchangeEvidence{
		Request: httpExchangeRequest{
			Method:     method,
			URL:        input.URL,
			Headers:    redactedRequestHeaders,
			Body:       requestText,
			BodyBase64: requestBase64,
		},
		Response: httpExchangeResponse{
			Status:     response.StatusCode,
			Headers:    redactedResponseHeaders,
			Body:       responseText,
			BodyBase64: responseBase64,
			Truncated:  truncated,
		},
	})
	if err != nil {
		return agentdomain.ToolResult{}, toolError("internal", "marshal http evidence: "+err.Error())
	}
	evidence, err := set.captureArtifact(ctx, invocation, view, application.Artifact{
		Kind: "http_exchange", Mime: "application/json", Data: envelope,
	}, "http_exchange", "application/json", map[string]any{
		"method":    method,
		"status":    response.StatusCode,
		"truncated": truncated,
	})
	if err != nil {
		return agentdomain.ToolResult{}, err
	}

	output := httpRequestOutput{
		Status:     response.StatusCode,
		Headers:    redactedResponseHeaders,
		Truncated:  truncated,
		EvidenceID: evidence.EvidenceID,
		SHA256:     evidence.SHA256,
		Size:       evidence.Size,
	}
	if utf8.Valid(redactedResponseBody) {
		output.Body = string(redactedResponseBody)
	} else {
		output.BodyBase64 = base64.StdEncoding.EncodeToString(redactedResponseBody)
	}
	if response.StatusCode == http.StatusUnauthorized {
		// A 401 is the session-invalid signal of design spec §5.9; the exchange
		// is still captured first so the failure stays auditable.
		return agentdomain.ToolResult{}, toolError("session_unauthorized",
			fmt.Sprintf("target responded %d; the session may be invalid", response.StatusCode))
	}
	return jsonResult(output)
}

// mapHTTPError maps transport failures onto the tool error vocabulary.
func mapHTTPError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return toolError("timeout", "http request timed out")
	}
	if errors.Is(err, errRedirectHostNotAllowed) {
		return toolError("host_not_allowed", "redirect target is outside the allowed hosts")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return toolError("timeout", "http request timed out")
	}
	// A connection/DNS failure is an environment failure (design spec §5.8).
	return toolError("executor_unavailable", "http request failed: "+err.Error())
}

// encodeEvidenceBody renders body as UTF-8 text when possible, otherwise as
// base64, so evidence stays lossless without leaking invalid bytes into JSON.
func encodeEvidenceBody(body []byte) (text, encoded string) {
	if len(body) == 0 {
		return "", ""
	}
	if utf8.Valid(body) {
		return string(body), ""
	}
	return "", base64.StdEncoding.EncodeToString(body)
}
