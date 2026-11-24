// Package executor is the HTTP client for the executor service (protocol v2:
// sessions + exec/shell/files/artifacts/trace). The API owns the bearer token
// and never exposes the executor directly to the agent: every execution call is
// proxied through this client by the execution use cases.
//
// Protocol v2 is the only published surface (v0.2.4-C R-C2): the whole v1
// context family was deleted on both sides in one change set.
package executor

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
)

// maxRequestDurationBase is the baseline bound for one control-plane executor
// request. The general command channel has its own (much longer) operational
// deadline below. (The former per-browser-action cap retired with the T16
// bridge removal; the value is retained as the control-plane sizing basis.)
const maxRequestDurationBase = 60 * time.Second

// DefaultTimeout bounds one control-plane executor request (health, session
// create/close, artifacts, network read, cancel). A slightly larger client bound
// than the executor's own cap lets the executor return its typed timeout error
// instead of the client aborting first. The case_run lease (75s+) must stay above
// this bound so a slow request is never mistaken for a lost executor by lease
// recovery.
const DefaultTimeout = maxRequestDurationBase + 15*time.Second

// DefaultCommandTimeout mirrors the executor's operational command deadline
// (EZTEST_EXECUTOR_COMMAND_TIMEOUT_MS, default 15min). ExecRequestTimeout adds
// transport overhead so the executor's own typed executor_timeout wins instead
// of the client aborting a command that is still inside its deadline. The
// case_run lease is renewed by an independent heartbeat while a command runs.
const (
	DefaultCommandTimeout = 15 * time.Minute
	ExecRequestTimeout    = DefaultCommandTimeout + 30*time.Second
)

const (
	healthPath   = "/healthz"
	sessionsPath = "/v1/sessions"
	// maxResponseBytes bounds one executor response. An artifact page can carry
	// base64 content up to the executor's artifact limit, so the bound is well
	// above the request cap; exceeding it is an EXPLICIT typed error, never a
	// silent truncation (constitution art.7.6).
	maxResponseBytes = 256 << 20

	// RequestIDHeader correlates one API-side executor call with the executor's
	// own request start/finish log lines (v0.2.3 X-01).
	RequestIDHeader = "X-EzTest-Request-Id"

	// CodeExecutorTimeout is the typed platform code for a transport timeout or
	// cancellation. Non-timeout transport failures keep CodeExecutorUnavailable.
	CodeExecutorTimeout = "executor_timeout"
	// CodeExecutorUnavailable is the typed platform code this client produces for
	// an unreachable executor or a protocol failure (unavailable/protocolError).
	// It is the WIRE code of the executor protocol, not the persisted
	// case_run.summary.terminal_cause: execution/application.classifyExecutorError
	// maps it onto execution.TerminalCauseExecutorUnavailable, onto
	// TerminalCauseExecutorContextCreateFailed for a session create, or onto the
	// executor_lost failure class for a dial failure -- a non-identity mapping, so
	// the two vocabularies are distinct even where they share a spelling.
	CodeExecutorUnavailable = "executor_unavailable"
)

// Typed executor conflict codes (protocol v2 lease fencing). The API maps them
// onto its own lease/session taxonomy instead of forwarding arbitrary codes.
const (
	CodeSessionNotFound    = "session_not_found"
	CodeLeaseEpochRequired = "lease_epoch_required"
	CodeLeaseLost          = "lease_lost"
	CodeLeaseExpired       = "lease_expired"
	CodeSessionReclaiming  = "session_reclaiming"
	CodeSessionLimit       = "session_limit"
	CodeDraining           = "draining"
	// CodeBrowserCliUnavailable is the executor's typed 503 for a missing
	// official playwright CLI runtime (executor/src/browser_cli.mjs). It is a
	// concrete executor capability limitation, not a transport outage.
	CodeBrowserCliUnavailable = "browser_cli_unavailable"
)

// CallDiagnostics is the dispatch provenance attached to one executor call. The
// router/adapter layers know the owner and session; every field is optional and
// is copied into the structured error details/log line when known. It never
// carries credentials. CreateSession is the only call that receives
// case_run_id/attempt (from the create metadata); the other calls attach only
// session_id (plus owner/endpoint/registry_hit).
type CallDiagnostics struct {
	OwnerExecutorID int64
	CaseRunID       int64
	Attempt         int
	SessionID       string
	// RegistryHit reports whether the owner was resolved from the executor
	// registry (false means primary-endpoint fallback).
	RegistryHit *bool
}

type callDiagnosticsKey struct{}

// WithCallDiagnostics attaches dispatch provenance to one executor call.
func WithCallDiagnostics(ctx context.Context, diagnostics CallDiagnostics) context.Context {
	return context.WithValue(ctx, callDiagnosticsKey{}, diagnostics)
}

func callDiagnosticsFrom(ctx context.Context) (CallDiagnostics, bool) {
	diagnostics, ok := ctx.Value(callDiagnosticsKey{}).(CallDiagnostics)
	return diagnostics, ok
}

type requestTimeoutKey struct{}

// WithRequestTimeout overrides the per-request bound of one executor call. The
// command channel uses it: a 15-minute command must not be aborted by the
// control-plane bound. A shorter caller deadline still wins.
func WithRequestTimeout(ctx context.Context, timeout time.Duration) context.Context {
	return context.WithValue(ctx, requestTimeoutKey{}, timeout)
}

func requestTimeoutFrom(ctx context.Context) (time.Duration, bool) {
	timeout, ok := ctx.Value(requestTimeoutKey{}).(time.Duration)
	return timeout, ok && timeout > 0
}

// Health is the executor /healthz payload (v2 session surface). A draining
// executor answers 503 and keeps its bearer-protected routes available for
// cleanup. The probe never launches a browser: exec/shell/files do not need one.
type Health struct {
	Status            string `json:"status"`
	Version           string `json:"version"`
	Sessions          int    `json:"sessions"`
	MaxSessions       int    `json:"maxSessions"`
	Draining          bool   `json:"draining"`
	ReclaimedSessions int    `json:"reclaimedSessions"`
	Error             string `json:"error,omitempty"`
}

// Lease is the session lease the API stamps at creation. The epoch mirrors the
// case_run dispatch epoch: every write carries it so a stale owner is fenced.
type Lease struct {
	Epoch int64
	TTL   time.Duration
}

// CreateSessionInput is one POST /v1/sessions request. Metadata is opaque to the
// executor (≤2KB) and is echoed by GET /v1/sessions. BaseURL is an informational
// hint consumed only by the retained browser bridge.
type CreateSessionInput struct {
	Key       string
	Workspace string
	BaseURL   string
	Metadata  map[string]any
	Lease     *Lease
}

// SessionInfo is one executor session summary.
type SessionInfo struct {
	ID               string         `json:"sessionId"`
	Key              string         `json:"key"`
	Workspace        string         `json:"workspace"`
	Metadata         map[string]any `json:"metadata"`
	CreatedAt        string         `json:"createdAt"`
	LeaseEpoch       int64          `json:"lease_epoch"`
	LeaseExpiresAt   string         `json:"lease_expires_at"`
	LeaseTTLMS       int64          `json:"lease_ttl_ms"`
	CommandCount     int            `json:"commandCount"`
	RunningCommands  int            `json:"runningCommands"`
	ArtifactCount    int            `json:"artifactCount"`
	ArtifactBase     int64          `json:"artifact_base"`
	ArtifactsEvicted int64          `json:"artifacts_evicted"`
}

// LeaseInfo is the answer of an explicit renewal/takeover.
type LeaseInfo struct {
	SessionID      string `json:"sessionId"`
	LeaseEpoch     int64  `json:"lease_epoch"`
	LeaseExpiresAt string `json:"lease_expires_at"`
	LeaseTTLMS     int64  `json:"lease_ttl_ms"`
}

// ExecInput is one command. Argv is the default form; Shell=true asks the
// executor to run the single argv entry through the platform shell.
type ExecInput struct {
	Argv       []string
	Shell      bool
	Stdin      string
	TimeoutMS  int
	Background bool
	Env        map[string]string
	LeaseEpoch int64
}

// ExecResult is the settled command result. Stdout/Stderr are the inline head;
// *Truncated/*Ref expose the full spilled content explicitly.
type ExecResult struct {
	CommandID       string     `json:"command_id"`
	PID             int        `json:"pid"`
	Exit            *int       `json:"exit"`
	Signal          string     `json:"signal"`
	Outcome         string     `json:"outcome"`
	DurationMS      int64      `json:"duration"`
	Stdout          string     `json:"stdout"`
	Stderr          string     `json:"stderr"`
	StdoutTruncated bool       `json:"stdout_truncated"`
	StderrTruncated bool       `json:"stderr_truncated"`
	StdoutRef       string     `json:"stdout_ref"`
	StderrRef       string     `json:"stderr_ref"`
	StdoutBytes     int64      `json:"stdout_bytes"`
	StderrBytes     int64      `json:"stderr_bytes"`
	StdoutSHA256    string     `json:"stdout_sha256"`
	StderrSHA256    string     `json:"stderr_sha256"`
	Background      bool       `json:"background"`
	TimedOut        bool       `json:"timed_out"`
	Cancelled       bool       `json:"cancelled"`
	SpawnError      string     `json:"spawn_error,omitempty"`
	Artifacts       []Artifact `json:"artifacts"`
}

// Shell operations (protocol v2 PTY channel).
const (
	ShellCreate = "create"
	ShellWrite  = "write"
	ShellRead   = "read"
	ShellClose  = "close"
	ShellWait   = "wait"
)

// ShellRequest is one PTY shell operation. Op selects the sub-route; the unused
// fields of another op are omitted from the wire payload.
type ShellRequest struct {
	Op         string
	ShellID    string
	Argv       []string
	Cols       int
	Rows       int
	Env        map[string]string
	Data       string
	Since      int64
	TimeoutMS  int
	LeaseEpoch int64
}

// ShellResult is the union answer of the shell operations. Which fields are
// populated depends on the requested Op.
type ShellResult struct {
	ShellID    string `json:"shell_id"`
	PID        int    `json:"pid"`
	Status     string `json:"status"`
	Data       string `json:"data"`
	NextCursor int64  `json:"next_cursor"`
	TotalBytes int64  `json:"total_bytes"`
	Truncated  bool   `json:"truncated"`
	Exit       *int   `json:"exit"`
	Outcome    string `json:"outcome"`
	OK         bool   `json:"ok"`
}

// File operations (protocol v2 workspace files channel).
const (
	FileUpload   = "upload"
	FileDownload = "download"
	FileList     = "list"
)

// FileRequest is one workspace files operation.
type FileRequest struct {
	Op         string
	Path       string
	Base64     string
	LeaseEpoch int64
}

// FileEntry is one workspace directory entry.
type FileEntry struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
	MIME  string `json:"mime"`
	Dir   bool   `json:"dir"`
}

// FileResult is the union answer of the files operations.
type FileResult struct {
	Path      string      `json:"path"`
	Bytes     int64       `json:"bytes"`
	SHA256    string      `json:"sha256"`
	MIME      string      `json:"mime"`
	Base64    string      `json:"base64"`
	Files     []FileEntry `json:"files"`
	Truncated bool        `json:"truncated"`
}

// Artifact is one recording-layer artifact of the session ledger. Command
// artifacts carry the full command record as base64 JSON; file/trace artifacts
// carry the file content, or an explicit content_omitted flag when the content
// exceeds the transfer limit (never a silent drop).
type Artifact struct {
	Kind            string `json:"kind"`
	SessionID       string `json:"session_id"`
	CommandID       string `json:"command_id"`
	Path            string `json:"path"`
	SHA256          string `json:"sha256"`
	Size            int64  `json:"size"`
	MIME            string `json:"mime"`
	Filename        string `json:"filename"`
	Base64          string `json:"base64"`
	ContentOmitted  bool   `json:"content_omitted"`
	ContentReason   string `json:"content_reason"`
	RecordingFailed bool   `json:"recording_failed"`
	Outcome         string `json:"outcome"`
	Error           string `json:"error"`
}

// ArtifactPage is one incremental pull of the session recording ledger. The
// cursor is an ABSOLUTE sequence: Gap/Missed report that the client fell behind
// the executor's bounded buffer, so the caller can degrade honestly instead of
// silently missing evidence (constitution art.4.5 / art.7.6).
type ArtifactPage struct {
	Artifacts      []Artifact `json:"artifacts"`
	NextCursor     int64      `json:"next_cursor"`
	Total          int64      `json:"total"`
	BaseCursor     int64      `json:"base_cursor"`
	Evicted        int64      `json:"evicted"`
	Gap            bool       `json:"gap"`
	Missed         int64      `json:"missed"`
	EvictionFailed int64      `json:"artifact_eviction_failed"`
}

// TraceRef describes the playwright trace file of a session, when one exists.
type TraceRef struct {
	Available bool   `json:"available"`
	Path      string `json:"path"`
	Bytes     int64  `json:"bytes"`
	SHA256    string `json:"sha256"`
	MIME      string `json:"mime"`
}

// Client is the executor protocol v2 surface used by the execution runtime.
//
// The transitional Playwright-library browser bridge was removed in v0.2.4-C
// T16: the browser capability domain runs the official playwright-cli over Exec
// (T15), and browser traffic is PURE-RECORDED by a passive CDP observer read
// through NetworkLog (design §4.3). There is no v1 dual track.
type Client interface {
	Health(ctx context.Context) (Health, error)
	CreateSession(ctx context.Context, in CreateSessionInput) (SessionInfo, error)
	GetSession(ctx context.Context, sessionID string) (SessionInfo, error)
	ListSessions(ctx context.Context) ([]SessionInfo, error)
	Exec(ctx context.Context, sessionID string, in ExecInput) (ExecResult, error)
	Shell(ctx context.Context, sessionID string, in ShellRequest) (ShellResult, error)
	Files(ctx context.Context, sessionID string, in FileRequest) (FileResult, error)
	Artifacts(ctx context.Context, sessionID string, since int64) (ArtifactPage, error)
	Trace(ctx context.Context, sessionID string) (TraceRef, error)
	// NetworkLog reads the session's recorded browser traffic log (pure CDP
	// recording, T16). tail bounds the item count (0 = executor default). The
	// raw payload is {items:[...], recording, degraded[, reason]}.
	NetworkLog(ctx context.Context, sessionID string, tail int) (json.RawMessage, error)
	Cancel(ctx context.Context, sessionID string) (bool, error)
	RenewLease(ctx context.Context, sessionID string, leaseEpoch int64) (LeaseInfo, error)
	CloseSession(ctx context.Context, sessionID string) error
}

// HTTPClient is the production Client. It never follows redirects so the bearer
// token cannot be replayed to another host/route, and it never logs bodies or
// the token.
type HTTPClient struct {
	endpoint string
	token    string
	http     *http.Client
	timeout  time.Duration
}

var _ Client = (*HTTPClient)(nil)

// NewClient returns a client for endpoint with a default per-request timeout. A
// non-positive timeout falls back to DefaultTimeout. The bound is applied per
// request (not on http.Client) so the command channel can raise it for one call.
func NewClient(endpoint, token string, timeout time.Duration) *HTTPClient {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	client := NewClientWithHTTPClient(endpoint, token, &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	})
	client.timeout = timeout
	return client
}

// NewClientWithHTTPClient injects the underlying HTTP client (tests use it to
// exercise timeouts and redirect handling). Its Timeout, when set, remains the
// transport bound.
func NewClientWithHTTPClient(endpoint, token string, httpClient *http.Client) *HTTPClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultTimeout}
	}
	return &HTTPClient{
		endpoint: strings.TrimRight(endpoint, "/"),
		token:    token,
		http:     httpClient,
		timeout:  httpClient.Timeout,
	}
}

// SessionKey is the executor session key of one (case_run, SUT, role) binding.
// It is what orphan reconciliation matches on.
//
// This literal is duplicated in application/case_run.go
// executorSessionKeyFormat — the two MUST change in the same commit or
// reconciliation silently stops matching.
func SessionKey(caseRunID, sutID int64, role string) string {
	return fmt.Sprintf("run:%d:sut:%d:role:%s", caseRunID, sutID, role)
}

// Health probes GET /healthz. It does not send the bearer token; a 503 degraded
// reply is a valid Health (the registry still records the heartbeat).
func (c *HTTPClient) Health(ctx context.Context) (Health, error) {
	result, err := c.request(ctx, http.MethodGet, healthPath, nil, false)
	if err != nil {
		return Health{}, err
	}
	if result.status != http.StatusOK && result.status != http.StatusServiceUnavailable {
		return Health{}, result.failure("response", executorError(result))
	}
	var health Health
	if err := json.Unmarshal(result.body, &health); err != nil {
		return Health{}, result.failure("body", protocolError("invalid executor health response"))
	}
	return health, nil
}

// CreateSession calls POST /v1/sessions.
func (c *HTTPClient) CreateSession(ctx context.Context, in CreateSessionInput) (SessionInfo, error) {
	payload := struct {
		Key       string         `json:"key,omitempty"`
		Workspace string         `json:"workspace"`
		BaseURL   string         `json:"base_url,omitempty"`
		Metadata  map[string]any `json:"metadata,omitempty"`
		Lease     *struct {
			Epoch int64 `json:"lease_epoch"`
			TTLMS int64 `json:"ttl_ms,omitempty"`
		} `json:"lease,omitempty"`
	}{
		Key:       in.Key,
		Workspace: in.Workspace,
		BaseURL:   in.BaseURL,
		Metadata:  in.Metadata,
	}
	if in.Lease != nil {
		payload.Lease = &struct {
			Epoch int64 `json:"lease_epoch"`
			TTLMS int64 `json:"ttl_ms,omitempty"`
		}{Epoch: in.Lease.Epoch}
		if in.Lease.TTL > 0 {
			payload.Lease.TTLMS = in.Lease.TTL.Milliseconds()
		}
	}
	result, err := c.request(ctx, http.MethodPost, sessionsPath, payload, true)
	if err != nil {
		return SessionInfo{}, err
	}
	if err := ensureOK(result); err != nil {
		return SessionInfo{}, err
	}
	info, err := decodeSession(result)
	if err != nil {
		return SessionInfo{}, err
	}
	if info.Key == "" {
		info.Key = in.Key
	}
	if info.Metadata == nil {
		info.Metadata = in.Metadata
	}
	return info, nil
}

// GetSession calls GET /v1/sessions/{id}.
func (c *HTTPClient) GetSession(ctx context.Context, sessionID string) (SessionInfo, error) {
	result, err := c.request(ctx, http.MethodGet, sessionPath(sessionID), nil, true)
	if err != nil {
		return SessionInfo{}, err
	}
	if err := ensureOK(result); err != nil {
		return SessionInfo{}, err
	}
	return decodeSession(result)
}

// ListSessions calls GET /v1/sessions (orphan reconciliation / capacity).
func (c *HTTPClient) ListSessions(ctx context.Context) ([]SessionInfo, error) {
	result, err := c.request(ctx, http.MethodGet, sessionsPath, nil, true)
	if err != nil {
		return nil, err
	}
	if err := ensureOK(result); err != nil {
		return nil, err
	}
	var payload struct {
		Sessions []SessionInfo `json:"sessions"`
	}
	if err := json.Unmarshal(result.body, &payload); err != nil {
		return nil, result.failure("body", protocolError("invalid executor list-sessions response"))
	}
	sessions := make([]SessionInfo, 0, len(payload.Sessions))
	sessions = append(sessions, payload.Sessions...)
	return sessions, nil
}

// Exec calls POST /v1/sessions/{id}/exec. The request bound is raised to the
// command deadline so a long command is not aborted by the control-plane bound.
func (c *HTTPClient) Exec(ctx context.Context, sessionID string, in ExecInput) (ExecResult, error) {
	if len(in.Argv) == 0 {
		return ExecResult{}, platform.Invalid("argv", "argv must not be empty")
	}
	payload := struct {
		Argv       []string          `json:"argv"`
		Shell      bool              `json:"shell,omitempty"`
		Stdin      string            `json:"stdin,omitempty"`
		TimeoutMS  int               `json:"timeout_ms,omitempty"`
		Background bool              `json:"background,omitempty"`
		Env        map[string]string `json:"env,omitempty"`
		LeaseEpoch int64             `json:"lease_epoch"`
	}{
		Argv:       in.Argv,
		Shell:      in.Shell,
		Stdin:      in.Stdin,
		TimeoutMS:  in.TimeoutMS,
		Background: in.Background,
		Env:        in.Env,
		LeaseEpoch: in.LeaseEpoch,
	}
	if _, ok := requestTimeoutFrom(ctx); !ok {
		bound := ExecRequestTimeout
		if in.TimeoutMS > 0 {
			bound = time.Duration(in.TimeoutMS)*time.Millisecond + 30*time.Second
		}
		ctx = WithRequestTimeout(ctx, bound)
	}
	result, err := c.request(ctx, http.MethodPost, sessionPath(sessionID)+"/exec", payload, true)
	if err != nil {
		return ExecResult{}, err
	}
	// A timed-out command still carries its recorded result in error.details so
	// the settlement path can persist the evidence of what actually ran.
	if result.status == http.StatusGatewayTimeout {
		var envelope struct {
			Error struct {
				Code    string          `json:"code"`
				Message string          `json:"message"`
				Details json.RawMessage `json:"details"`
			} `json:"error"`
		}
		if json.Unmarshal(result.body, &envelope) == nil && len(envelope.Error.Details) > 0 {
			var timedOut ExecResult
			if json.Unmarshal(envelope.Error.Details, &timedOut) == nil && timedOut.CommandID != "" {
				return timedOut, result.failure("response", executorError(result))
			}
		}
	}
	if err := ensureOK(result); err != nil {
		return ExecResult{}, err
	}
	var decoded ExecResult
	if err := json.Unmarshal(result.body, &decoded); err != nil || decoded.CommandID == "" {
		return ExecResult{}, result.failure("body", protocolError("invalid executor exec response"))
	}
	return decoded, nil
}

// Shell calls the PTY shell sub-route selected by in.Op.
func (c *HTTPClient) Shell(ctx context.Context, sessionID string, in ShellRequest) (ShellResult, error) {
	base := sessionPath(sessionID) + "/shell"
	switch in.Op {
	case ShellCreate:
		payload := struct {
			Argv       []string          `json:"argv,omitempty"`
			Cols       int               `json:"cols,omitempty"`
			Rows       int               `json:"rows,omitempty"`
			Env        map[string]string `json:"env,omitempty"`
			LeaseEpoch int64             `json:"lease_epoch"`
		}{Argv: in.Argv, Cols: in.Cols, Rows: in.Rows, Env: in.Env, LeaseEpoch: in.LeaseEpoch}
		return c.shellCall(ctx, http.MethodPost, base, payload)
	case ShellWrite, ShellClose, ShellWait:
		if in.ShellID == "" {
			return ShellResult{}, platform.Invalid("shell_id", "shell_id is required")
		}
		payload := struct {
			Data       string `json:"data,omitempty"`
			TimeoutMS  int    `json:"timeout_ms,omitempty"`
			LeaseEpoch int64  `json:"lease_epoch"`
		}{Data: in.Data, TimeoutMS: in.TimeoutMS, LeaseEpoch: in.LeaseEpoch}
		path := base + "/" + url.PathEscape(in.ShellID) + "/" + in.Op
		return c.shellCall(ctx, http.MethodPost, path, payload)
	case ShellRead:
		if in.ShellID == "" {
			return ShellResult{}, platform.Invalid("shell_id", "shell_id is required")
		}
		path := base + "/" + url.PathEscape(in.ShellID) + "/read?since=" + fmt.Sprintf("%d", in.Since)
		return c.shellCall(ctx, http.MethodGet, path, nil)
	default:
		return ShellResult{}, platform.Invalid("op", "unsupported shell operation")
	}
}

func (c *HTTPClient) shellCall(ctx context.Context, method, path string, payload any) (ShellResult, error) {
	result, err := c.request(ctx, method, path, payload, true)
	if err != nil {
		return ShellResult{}, err
	}
	if err := ensureOK(result); err != nil {
		return ShellResult{}, err
	}
	var decoded ShellResult
	if err := json.Unmarshal(result.body, &decoded); err != nil {
		return ShellResult{}, result.failure("body", protocolError("invalid executor shell response"))
	}
	return decoded, nil
}

// Files calls the workspace files sub-route selected by in.Op.
func (c *HTTPClient) Files(ctx context.Context, sessionID string, in FileRequest) (FileResult, error) {
	base := sessionPath(sessionID) + "/files"
	var result *httpResult
	var err error
	switch in.Op {
	case FileUpload:
		payload := struct {
			Path       string `json:"path"`
			Base64     string `json:"base64"`
			LeaseEpoch int64  `json:"lease_epoch"`
		}{Path: in.Path, Base64: in.Base64, LeaseEpoch: in.LeaseEpoch}
		result, err = c.request(ctx, http.MethodPost, base, payload, true)
	case FileDownload:
		result, err = c.request(ctx, http.MethodGet, base+"?path="+url.QueryEscape(in.Path), nil, true)
	case FileList:
		path := base
		if in.Path != "" {
			path += "?path=" + url.QueryEscape(in.Path)
		}
		result, err = c.request(ctx, http.MethodGet, path, nil, true)
	default:
		return FileResult{}, platform.Invalid("op", "unsupported files operation")
	}
	if err != nil {
		return FileResult{}, err
	}
	if err := ensureOK(result); err != nil {
		return FileResult{}, err
	}
	var decoded FileResult
	if err := json.Unmarshal(result.body, &decoded); err != nil {
		return FileResult{}, result.failure("body", protocolError("invalid executor files response"))
	}
	return decoded, nil
}

// Artifacts calls GET /v1/sessions/{id}/artifacts?since=<absolute cursor>.
func (c *HTTPClient) Artifacts(ctx context.Context, sessionID string, since int64) (ArtifactPage, error) {
	if since < 0 {
		return ArtifactPage{}, platform.Invalid("since", "since must be a non-negative cursor")
	}
	path := sessionPath(sessionID) + "/artifacts?since=" + fmt.Sprintf("%d", since)
	result, err := c.request(ctx, http.MethodGet, path, nil, true)
	if err != nil {
		return ArtifactPage{}, err
	}
	if err := ensureOK(result); err != nil {
		return ArtifactPage{}, err
	}
	var page ArtifactPage
	if err := json.Unmarshal(result.body, &page); err != nil {
		return ArtifactPage{}, result.failure("body", protocolError("invalid executor artifacts response"))
	}
	if page.Artifacts == nil {
		page.Artifacts = []Artifact{}
	}
	return page, nil
}

// Trace calls GET /v1/sessions/{id}/trace.
func (c *HTTPClient) Trace(ctx context.Context, sessionID string) (TraceRef, error) {
	result, err := c.request(ctx, http.MethodGet, sessionPath(sessionID)+"/trace", nil, true)
	if err != nil {
		return TraceRef{}, err
	}
	if err := ensureOK(result); err != nil {
		return TraceRef{}, err
	}
	var ref TraceRef
	if err := json.Unmarshal(result.body, &ref); err != nil {
		return TraceRef{}, result.failure("body", protocolError("invalid executor trace response"))
	}
	return ref, nil
}

// Cancel calls POST /v1/sessions/{id}/cancel and reports whether an in-flight
// command was actually canceled. Cleanup is never fenced. The executor answers
// with the cancelled command ids; a non-empty list means work was stopped.
func (c *HTTPClient) Cancel(ctx context.Context, sessionID string) (bool, error) {
	result, err := c.request(ctx, http.MethodPost, sessionPath(sessionID)+"/cancel", nil, true)
	if err != nil {
		return false, err
	}
	if err := ensureOK(result); err != nil {
		return false, err
	}
	var payload struct {
		OK     bool `json:"ok"`
		Result struct {
			Cancelled []string `json:"cancelled"`
		} `json:"result"`
	}
	if err := json.Unmarshal(result.body, &payload); err != nil {
		return false, result.failure("body", protocolError("invalid executor cancel response"))
	}
	return len(payload.Result.Cancelled) > 0, nil
}

// NetworkLog calls GET /v1/sessions/{id}/network?tail=N: the session's recorded
// browser traffic log (pure CDP recording, T16). The raw payload is returned
// verbatim ({items:[...], recording, degraded[, reason]}) so the discovery
// capture can parse the items and store the log as network evidence. A read is
// best effort and never lease-fenced (terminal-state capture).
func (c *HTTPClient) NetworkLog(ctx context.Context, sessionID string, tail int) (json.RawMessage, error) {
	path := sessionPath(sessionID) + "/network"
	if tail > 0 {
		path += "?tail=" + fmt.Sprintf("%d", tail)
	}
	result, err := c.request(ctx, http.MethodGet, path, nil, true)
	if err != nil {
		return nil, err
	}
	if err := ensureOK(result); err != nil {
		return nil, err
	}
	return json.RawMessage(result.body), nil
}

// RenewLease calls POST /v1/sessions/{id}/lease. A higher epoch may take over an
// expired lease before the sweeper reclaims it (re-claim semantics).
func (c *HTTPClient) RenewLease(ctx context.Context, sessionID string, leaseEpoch int64) (LeaseInfo, error) {
	payload := struct {
		LeaseEpoch int64 `json:"lease_epoch"`
	}{LeaseEpoch: leaseEpoch}
	result, err := c.request(ctx, http.MethodPost, sessionPath(sessionID)+"/lease", payload, true)
	if err != nil {
		return LeaseInfo{}, err
	}
	if err := ensureOK(result); err != nil {
		return LeaseInfo{}, err
	}
	var info LeaseInfo
	if err := json.Unmarshal(result.body, &info); err != nil || info.SessionID == "" {
		return LeaseInfo{}, result.failure("body", protocolError("invalid executor lease response"))
	}
	return info, nil
}

// CloseSession calls DELETE /v1/sessions/{id}. A missing session is not an error
// for best-effort terminal cleanup.
func (c *HTTPClient) CloseSession(ctx context.Context, sessionID string) error {
	result, err := c.request(ctx, http.MethodDelete, sessionPath(sessionID), nil, true)
	if err != nil {
		return err
	}
	return ensureOK(result)
}

func sessionPath(sessionID string) string {
	return sessionsPath + "/" + url.PathEscape(sessionID)
}

func decodeSession(result *httpResult) (SessionInfo, error) {
	var info SessionInfo
	if err := json.Unmarshal(result.body, &info); err != nil || info.ID == "" {
		return SessionInfo{}, result.failure("body", protocolError("invalid executor session response"))
	}
	return info, nil
}

// httpResult is one completed executor HTTP exchange. It carries the
// sanitized request identity so every failure path can attach the same
// structured, request-scoped diagnostics.
type httpResult struct {
	ctx       context.Context
	status    int
	body      []byte
	method    string
	path      string
	endpoint  string
	requestID string
	duration  time.Duration
	timeout   time.Duration
}

// failure attaches structured details and logs one sanitized diagnostic line.
// It returns the same error so callers can `return ..., result.failure(...)`.
func (r *httpResult) failure(phase string, err *platform.Error) error {
	// Typed details already carried by the error (for example the executor's
	// retry_after_seconds hint) are preserved; the request diagnostics are merged
	// in around them.
	details := r.details(phase)
	for key, value := range err.Details {
		if _, exists := details[key]; !exists {
			details[key] = value
		}
	}
	err.Details = details
	attrs := []any{
		slog.String("executor.endpoint", r.endpoint),
		slog.String("executor.method", r.method),
		slog.String("executor.path", r.path),
		slog.String("executor.phase", phase),
		slog.String("executor.request_id", r.requestID),
		slog.Int64("executor.duration_ms", r.duration.Milliseconds()),
		slog.Int64("executor.timeout_ms", r.timeout.Milliseconds()),
		slog.Int("executor.status_code", r.status),
		slog.String("executor.code", err.Code),
		slog.Bool("executor.retryable", err.Retryable),
		slog.String("error", err.Message),
	}
	attrs = append(attrs, r.dispatchLogAttrs()...)
	slog.Default().WarnContext(r.ctx, "executor request failed", attrs...)
	return err
}

func (r *httpResult) dispatchLogAttrs() []any {
	details := r.details("")
	attrs := make([]any, 0, 8)
	if value, ok := details["owner_executor_id"]; ok {
		attrs = append(attrs, slog.Any("executor.owner_executor_id", value))
	}
	if value, ok := details["case_run_id"]; ok {
		attrs = append(attrs, slog.Any("executor.case_run_id", value))
	}
	if value, ok := details["attempt"]; ok {
		attrs = append(attrs, slog.Any("executor.attempt", value))
	}
	if value, ok := details["session_id"]; ok {
		attrs = append(attrs, slog.Any("executor.session_id", value))
	}
	if value, ok := details["registry_hit"]; ok {
		attrs = append(attrs, slog.Any("executor.registry_hit", value))
	}
	return attrs
}

// details builds the sanitized request-scoped diagnostics. It never includes
// the bearer token or the request body.
func (r *httpResult) details(phase string) map[string]any {
	details := map[string]any{
		"endpoint":   r.endpoint,
		"method":     r.method,
		"path":       r.path,
		"request_id": r.requestID,
	}
	if phase != "" {
		details["phase"] = phase
	}
	if r.status != 0 {
		details["status_code"] = r.status
	}
	if r.timeout > 0 {
		details["timeout_ms"] = r.timeout.Milliseconds()
	}
	details["duration_ms"] = r.duration.Milliseconds()
	if diagnostics, ok := callDiagnosticsFrom(r.ctx); ok {
		details["owner_executor_id"] = diagnostics.OwnerExecutorID
		if diagnostics.CaseRunID > 0 {
			details["case_run_id"] = diagnostics.CaseRunID
		}
		if diagnostics.Attempt > 0 {
			details["attempt"] = diagnostics.Attempt
		}
		if diagnostics.SessionID != "" {
			details["session_id"] = diagnostics.SessionID
		}
		if diagnostics.RegistryHit != nil {
			details["registry_hit"] = *diagnostics.RegistryHit
		}
	}
	return details
}

// request performs one executor HTTP call. Transport failures are typed as
// executor_timeout (timeout/cancellation) or executor_unavailable (everything
// else) and always carry the structured request diagnostics above.
func (c *HTTPClient) request(ctx context.Context, method, path string, payload any, auth bool) (*httpResult, error) {
	if c.endpoint == "" {
		return nil, protocolError("executor endpoint is not configured")
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, platform.Invalid("executor_request", "executor request could not be encoded")
		}
		body = bytes.NewReader(encoded)
	}
	bound := c.timeout
	if override, ok := requestTimeoutFrom(ctx); ok {
		bound = override
	}
	// The reported bound is the CONFIGURED per-request budget (or the caller's
	// shorter deadline), measured before this call derives its own deadline —
	// otherwise the diagnostic would drift by the microseconds already spent.
	reported := bound
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && (reported <= 0 || remaining < reported) {
			reported = remaining
		}
	}
	if bound > 0 {
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > bound {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, bound)
			defer cancel()
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, body)
	if err != nil {
		return nil, protocolError("executor endpoint is invalid")
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if auth {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	requestID := requestid.New()
	request.Header.Set(RequestIDHeader, requestID)

	result := &httpResult{
		ctx:       ctx,
		method:    method,
		path:      path,
		endpoint:  sanitizedEndpoint(c.endpoint),
		requestID: requestID,
		timeout:   reported,
	}
	started := time.Now()
	response, err := c.http.Do(request)
	result.duration = time.Since(started)
	if err != nil {
		return nil, result.failure(transportPhase(err), transportError(ctx, err))
	}
	defer func() { _ = response.Body.Close() }()
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	result.duration = time.Since(started)
	result.status, result.body = response.StatusCode, raw
	if readErr != nil {
		// io.ReadAll can fail after the headers because a deadline also bounds
		// the body. A timeout or cancellation there is the same transport
		// failure as one before the headers, so it keeps the executor_timeout
		// taxonomy instead of being flattened into executor_unavailable.
		if transportTimedOutOrCanceled(ctx, readErr) {
			return nil, result.failure("body", transportError(ctx, readErr))
		}
		return nil, result.failure("body", unavailable("executor response could not be read"))
	}
	if len(raw) > maxResponseBytes {
		// Explicit oversize failure instead of a silently truncated body.
		return nil, result.failure("body", &platform.Error{
			Status:  http.StatusBadGateway,
			Code:    "executor_response_too_large",
			Message: fmt.Sprintf("executor response exceeds the %d byte client limit", maxResponseBytes),
		})
	}
	return result, nil
}

// transportError maps a failed exchange onto the typed transport taxonomy.
func transportError(ctx context.Context, err error) *platform.Error {
	if !transportTimedOutOrCanceled(ctx, err) {
		return unavailable("executor request failed: " + sanitizeTransportError(err))
	}
	canceled := errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled)
	apiErr := &platform.Error{
		// Every executor_timeout uses the same 504 status as the executor's own
		// typed envelope, whether the deadline was detected locally or remotely.
		Status:    http.StatusGatewayTimeout,
		Code:      CodeExecutorTimeout,
		Retryable: !canceled,
	}
	if canceled {
		apiErr.Message = "executor request was canceled"
	} else {
		apiErr.Message = "executor request timed out: " + sanitizeTransportError(err)
	}
	return apiErr
}

// transportTimedOutOrCanceled reports whether err (or the caller's context) is a
// deadline or cancellation, including a response-body read that was interrupted
// by a deadline.
func transportTimedOutOrCanceled(ctx context.Context, err error) bool {
	return errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(ctx.Err(), context.DeadlineExceeded) ||
		isTimeoutError(err) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(ctx.Err(), context.Canceled)
}

func isTimeoutError(err error) bool {
	var timeoutErr interface{ Timeout() bool }
	return errors.As(err, &timeoutErr) && timeoutErr.Timeout()
}

// transportPhase classifies where a transport failure happened: dial, tls,
// request, response headers or body.
func transportPhase(err error) string {
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return "dial"
	}
	var certificateErr *tls.CertificateVerificationError
	if errors.As(err, &certificateErr) {
		return "tls"
	}
	var recordErr tls.RecordHeaderError
	if errors.As(err, &recordErr) {
		return "tls"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "tls:") || strings.Contains(message, "x509:"):
		return "tls"
	case strings.Contains(message, "awaiting headers"):
		return "response"
	case strings.Contains(message, "reading response body"):
		return "body"
	}
	// A deadline that is neither a dial nor a TLS failure was reached while the
	// exchange was in flight: the request had been written and the response had
	// not arrived. Report it as the response phase (the same classification the
	// transport-level timeout produced before the bound moved to the context).
	if isTimeoutError(err) || errors.Is(err, context.DeadlineExceeded) {
		return "response"
	}
	return "request"
}

// ensureOK maps a non-2xx response to a typed error.
func ensureOK(result *httpResult) error {
	if result.status >= http.StatusOK && result.status < http.StatusMultipleChoices {
		return nil
	}
	return result.failure("response", executorError(result))
}

// executorError converts an executor error envelope into a *platform.Error.
// Every 5xx is the retryable executor_unavailable environment failure, except
// the explicit executor_timeout reply, the operational draining signal and the
// typed browser_cli_unavailable capability gap (code + install guidance kept).
// Forwarding an arbitrary executor code would leak it into the API failure
// taxonomy.
func executorError(result *httpResult) *platform.Error {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details struct {
				RetryAfterSeconds int64 `json:"retry_after_seconds"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(result.body, &envelope)
	code := envelope.Error.Code
	if result.status >= http.StatusInternalServerError {
		switch code {
		case CodeExecutorTimeout:
			return &platform.Error{
				Status:    http.StatusGatewayTimeout,
				Code:      CodeExecutorTimeout,
				Message:   envelope.Error.Message,
				Retryable: true,
			}
		case CodeDraining:
			// Drain is operational backpressure, not an environment failure:
			// the caller retries another endpoint or later.
			return &platform.Error{
				Status:    http.StatusServiceUnavailable,
				Code:      CodeDraining,
				Message:   envelope.Error.Message,
				Retryable: true,
			}
		case CodeBrowserCliUnavailable:
			// The official playwright CLI runtime is not installed on this
			// executor. Keep the typed code and the install guidance: the code
			// token feeds the X-02 executor-capability attribution (blocked, no
			// SUT issue) and the message tells the operator how to fix the host.
			// Retryable: another endpoint (or this one after install) can serve.
			return &platform.Error{
				Status:    http.StatusServiceUnavailable,
				Code:      CodeBrowserCliUnavailable,
				Message:   envelope.Error.Message,
				Retryable: true,
			}
		}
		return unavailable(fmt.Sprintf("executor returned HTTP %d", result.status))
	}
	if code == "" {
		return unavailable(fmt.Sprintf("executor returned HTTP %d", result.status))
	}
	apiErr := &platform.Error{
		Status:  result.status,
		Code:    code,
		Message: envelope.Error.Message,
	}
	if code == CodeSessionLimit {
		// Capacity backpressure is retryable after the executor's hint.
		apiErr.Retryable = true
		if envelope.Error.Details.RetryAfterSeconds > 0 {
			apiErr.Details = map[string]any{
				"retry_after_seconds": envelope.Error.Details.RetryAfterSeconds,
			}
		}
	}
	return apiErr
}

func unavailable(message string) *platform.Error {
	return &platform.Error{
		Status:    http.StatusServiceUnavailable,
		Code:      CodeExecutorUnavailable,
		Message:   message,
		Retryable: true,
	}
}

func protocolError(message string) *platform.Error {
	return &platform.Error{
		Status:  http.StatusServiceUnavailable,
		Code:    CodeExecutorUnavailable,
		Message: message,
	}
}

// sanitizedEndpoint drops userinfo, query and path so diagnostics never leak
// credentials; executor endpoints are scheme://host[:port] by contract.
func sanitizedEndpoint(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

// sanitizeTransportError keeps the useful part of a transport failure without
// the request URL (which may embed credentials) or the Authorization header.
func sanitizeTransportError(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err.Error()
	}
	return "transport error"
}
