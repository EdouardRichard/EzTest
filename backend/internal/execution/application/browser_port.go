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

// BrowserError is the typed failure of the executor client. Code is a stable// protocol or transport code: the design spec §5.6 executor codes (for example
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

// DefaultVisualBaselineMaxBytes is the design spec §9.3 baseline size cap (5
// MiB) applied when the caller does not configure a limit.
const DefaultVisualBaselineMaxBytes int64 = 5 << 20

// VisualRegion is a normalized screenshot crop (design spec §9.3): all values
// are in the 0..1 range and (X+Width, Y+Height) stays inside the unit square.
type VisualRegion struct {
	X      float64
	Y      float64
	Width  float64
	Height float64
}

// VisualCompareRequest is one visual.compare invocation. Baseline is the
// approved baseline PNG; the actual screenshot is captured by the executor in
// the target page, never sent by the API. Selector is empty for the whole page
// viewport; Region is nil for no crop.
type VisualCompareRequest struct {
	Baseline          []byte
	Selector          string
	Region            *VisualRegion
	Threshold         float64
	Viewport          Viewport
	DeviceScaleFactor float64
	// MaxBaselineBytes caps the request-side baseline copy (0 = the 5 MiB
	// spec default enforced by the caller).
	MaxBaselineBytes int64
}

// VisualMismatchBox is one device-pixel rectangle that contains differing
// pixels. The coordinates are relative to the compared image.
type VisualMismatchBox struct {
	X      int
	Y      int
	Width  int
	Height int
}

// VisualCompareResult is the executor's decoded visual comparison. ActualPNG and
// DiffPNG are the hash-verified artifacts of the visual_actual and visual_diff
// kinds. EnvironmentMatch is false when the live viewport or device scale factor
// differs from the baseline configuration; the caller must then classify
// inconclusive(environment) instead of comparing pixels.
type VisualCompareResult struct {
	DiffRatio               float64
	Width                   int
	Height                  int
	MismatchBoxes           []VisualMismatchBox
	EnvironmentMatch        bool
	ActualWidth             int
	ActualHeight            int
	ActualDeviceScaleFactor float64
	ActualPNG               []byte
	DiffPNG                 []byte
}

// VisualComparator is the visual comparison slice of the executor protocol. It
// is separate from BrowserExecutor so existing fakes and adapters keep
// compiling; the production *browser.Client implements both.
type VisualComparator interface {
	CompareVisual(ctx context.Context, ref ContextRef, req VisualCompareRequest) (VisualCompareResult, error)
}
