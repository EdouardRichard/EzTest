package application

import (
	"context"
	"encoding/json"
)

// ContextSpec is the executor context creation request (design spec §5.6). Key
// is the caller's correlation key (for example "case_run:{id}"); AllowedHosts
// bounds every request the context may issue; BaseURL is injected as the page's
// base URL and its host is implicitly allowed; StorageState is the serialized
// Playwright storage state (cookies/localStorage) used as the context's initial
// state; SessionStorage maps an origin to the JSON array of {name,value} items
// injected through an init script; Viewport overrides the browser viewport when
// both dimensions are positive.
type ContextSpec struct {
	Key            string
	AllowedHosts   []string
	BaseURL        string
	StorageState   json.RawMessage
	SessionStorage map[string]json.RawMessage
	Viewport       Viewport
}

// Viewport is a positive browser viewport. A zero value asks the executor for
// its default viewport.
type Viewport struct {
	Width  int
	Height int
}

// ContextRef identifies one live executor context. The ID is opaque and only
// meaningful to the executor that created it.
type ContextRef struct {
	ID string
}

// Action is one executor action invocation (design spec §5.6). Name is the
// protocol action name ("goto", "click", "screenshot", ...); Args is the raw
// JSON argument object forwarded to the executor unchanged.
type Action struct {
	Name string
	Args json.RawMessage
}

// ActionResult is the decoded response of one action. Result is the raw JSON
// result object. Artifacts are the decoded, hash-verified binary outputs.
// ErrorCode/ErrorMessage carry a protocol-level action failure: such a failure
// is a normal action outcome, not a transport error, so the Go client reports it
// here with a nil error. ErrorCode is one of the design spec §5.6 error codes
// (for example "target_not_found", "timeout" or "host_not_allowed").
type ActionResult struct {
	Result       json.RawMessage
	Artifacts    []Artifact
	ErrorCode    string
	ErrorMessage string
}

// Artifact is one binary executor output (screenshot, download). Data holds the
// decoded bytes and SHA256 their hex-encoded digest; the client recomputes the
// digest from Data and rejects a mismatched artifact.
type Artifact struct {
	Kind     string
	Mime     string
	Filename string
	Data     []byte
	SHA256   string
}

// BrowserExecutor is the browser execution port (design spec §5.6 / §8). The
// production adapter talks to the standalone Node/Playwright executor; tests use
// an in-memory implementation. Health reports whether the executor is reachable
// and healthy. Every call honors the context deadline; implementations apply
// their configured default only when the context has none.
type BrowserExecutor interface {
	// Health probes GET /healthz and validates the reported status, version and
	// context capacity.
	Health(ctx context.Context) error
	// NewContext creates one isolated browser context and returns its reference.
	NewContext(ctx context.Context, spec ContextSpec) (ContextRef, error)
	// Action runs one action on ref. Protocol-level failures are returned in
	// ActionResult.ErrorCode with a nil error; only transport/client failures
	// return an error.
	Action(ctx context.Context, ref ContextRef, action Action) (ActionResult, error)
	// Cancel aborts the in-flight action of ref, if any.
	Cancel(ctx context.Context, ref ContextRef) error
	// Close disposes ref and its browser context.
	Close(ctx context.Context, ref ContextRef) error
}

// BrowserError is the typed failure of the executor client. Code is a stable
// protocol or transport code: the design spec §5.6 executor codes (for example
// "unauthorized", "context_not_found", "internal") plus "timeout", "canceled"
// and "executor_unavailable" for transport failures. StatusCode is the HTTP
// status when the failure came from a response and zero otherwise. Message never
// contains the bearer token or a request body.
type BrowserError struct {
	Code       string
	Message    string
	StatusCode int
}

// NewBrowserError builds a BrowserError with the given stable code and message.
func NewBrowserError(code, message string) *BrowserError {
	return &BrowserError{Code: code, Message: message}
}

// Error implements error.
func (e *BrowserError) Error() string {
	if e == nil {
		return "execution: browser executor error"
	}
	if e.Code == "" {
		return e.Message
	}
	if e.Message == "" {
		return "execution: browser executor error: " + e.Code
	}
	return "execution: browser executor error: " + e.Code + ": " + e.Message
}
