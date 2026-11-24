package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executorinfra "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/executor"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// errNotConfigured is the internal wiring error of an unset adapter dependency.
func errNotConfigured(name string) error {
	return fmt.Errorf("%s is not configured", name)
}

// Executor self-check sentinels (spec §11.1 item 6). The startup probe has to
// distinguish a rejected bearer token from an unreachable executor so the UI
// can show "authentication failed" instead of a generic "offline".
var (
	// ErrExecutorUnauthorized reports that the executor rejected the configured
	// Bearer token (HTTP 401/403).
	ErrExecutorUnauthorized = errors.New("executor rejected the bearer token")
	// ErrExecutorOffline reports that the executor could not be reached or
	// answered with an unexpected status.
	ErrExecutorOffline = errors.New("executor is offline")
)

// ExecutionAdapter adapts the executor protocol v2 client to the execution port.
// One client call maps one protocol operation; the session lifecycle is owned by
// the CaseRunner. Every write carries the session lease epoch so the executor
// fences a stale dispatch.
type ExecutionAdapter struct {
	client executorinfra.Client
	router *ExecutorRouter
}

// NewExecutionAdapterWithRouter wires the execution adapter with the
// per-executor router. The router takes precedence over client.
func NewExecutionAdapterWithRouter(router *ExecutorRouter) *ExecutionAdapter {
	return &ExecutionAdapter{router: router}
}

var _ executionapp.ExecutionExecutor = (*ExecutionAdapter)(nil)

// clientForDiagnostics also returns the routing provenance merged into the
// client's structured error details/log line.
func (a *ExecutionAdapter) clientForDiagnostics(ctx context.Context, executorID int64) (executorinfra.Client, executorinfra.CallDiagnostics, error) {
	diagnostics := executorinfra.CallDiagnostics{OwnerExecutorID: executorID}
	if a.router != nil {
		client, route, err := a.router.clientForDiagnostics(ctx, executorID)
		if err != nil {
			return nil, diagnostics, err
		}
		registryHit := route.RegistryHit
		diagnostics.RegistryHit = &registryHit
		return client, diagnostics, nil
	}
	if a.client == nil {
		return nil, diagnostics, errNotConfigured("executor client")
	}
	return a.client, diagnostics, nil
}

// CreateSession creates one executor session and returns its id.
func (a *ExecutionAdapter) CreateSession(ctx context.Context, spec executionapp.SessionSpec) (string, error) {
	client, diagnostics, err := a.clientForDiagnostics(ctx, spec.ExecutorID)
	if err != nil {
		return "", err
	}
	// The create metadata is the only place the adapter can recover the
	// case_run correlation without adding a new port field.
	diagnostics.CaseRunID = caseRunIDFromMetadata(spec.Metadata)
	diagnostics.Attempt = attemptFromMetadata(spec.Metadata)
	ctx = executorinfra.WithCallDiagnostics(ctx, diagnostics)
	var lease *executorinfra.Lease
	if spec.LeaseEpoch > 0 || spec.LeaseTTL > 0 {
		lease = &executorinfra.Lease{Epoch: spec.LeaseEpoch, TTL: spec.LeaseTTL}
	}
	info, err := client.CreateSession(ctx, executorinfra.CreateSessionInput{
		Key:       spec.Key,
		Workspace: spec.Workspace,
		BaseURL:   spec.BaseURL,
		Metadata:  spec.Metadata,
		Lease:     lease,
	})
	if err != nil {
		return "", err
	}
	return info.ID, nil
}

// Exec runs one command in the session. A timed-out command still returns its
// recorded result alongside the typed error so the caller can settle the evidence
// of what actually ran.
func (a *ExecutionAdapter) Exec(ctx context.Context, executorID int64, sessionID string, command executionapp.ExecCommand, leaseEpoch int64) (executionapp.ExecOutcome, error) {
	client, diagnostics, err := a.clientForDiagnostics(ctx, executorID)
	if err != nil {
		return executionapp.ExecOutcome{}, err
	}
	diagnostics.SessionID = sessionID
	ctx = executorinfra.WithCallDiagnostics(ctx, diagnostics)
	result, err := client.Exec(ctx, sessionID, executorinfra.ExecInput{
		Argv:       command.Argv,
		Shell:      command.Shell,
		Stdin:      command.Stdin,
		TimeoutMS:  command.TimeoutMS,
		Background: command.Background,
		Env:        command.Env,
		LeaseEpoch: leaseEpoch,
	})
	outcome := executionapp.ExecOutcome{
		SessionID:       sessionID,
		CommandID:       result.CommandID,
		Exit:            result.Exit,
		Outcome:         result.Outcome,
		DurationMS:      result.DurationMS,
		Stdout:          result.Stdout,
		Stderr:          result.Stderr,
		StdoutTruncated: result.StdoutTruncated,
		StderrTruncated: result.StderrTruncated,
		StdoutRef:       result.StdoutRef,
		StderrRef:       result.StderrRef,
		Background:      result.Background,
		TimedOut:        result.TimedOut,
		Cancelled:       result.Cancelled,
	}
	return outcome, err
}

// Shell runs one PTY shell operation in the session (create/write/read/close/
// wait). The op vocabulary passes through verbatim: it is the protocol's own.
func (a *ExecutionAdapter) Shell(ctx context.Context, executorID int64, sessionID string, op executionapp.ShellOperation, leaseEpoch int64) (executionapp.ShellOutcome, error) {
	client, diagnostics, err := a.clientForDiagnostics(ctx, executorID)
	if err != nil {
		return executionapp.ShellOutcome{}, err
	}
	diagnostics.SessionID = sessionID
	ctx = executorinfra.WithCallDiagnostics(ctx, diagnostics)
	result, err := client.Shell(ctx, sessionID, executorinfra.ShellRequest{
		Op:         op.Op,
		ShellID:    op.ShellID,
		Argv:       op.Argv,
		Cols:       op.Cols,
		Rows:       op.Rows,
		Env:        op.Env,
		Data:       op.Data,
		Since:      op.Since,
		TimeoutMS:  op.TimeoutMS,
		LeaseEpoch: leaseEpoch,
	})
	if err != nil {
		return executionapp.ShellOutcome{}, err
	}
	return executionapp.ShellOutcome{
		ShellID:    result.ShellID,
		PID:        result.PID,
		Status:     result.Status,
		Data:       result.Data,
		NextCursor: result.NextCursor,
		TotalBytes: result.TotalBytes,
		Truncated:  result.Truncated,
		Exit:       result.Exit,
		Outcome:    result.Outcome,
		OK:         result.OK,
	}, nil
}

// Files runs one workspace files operation in the session (upload/download/list).
func (a *ExecutionAdapter) Files(ctx context.Context, executorID int64, sessionID string, op executionapp.FileOperation, leaseEpoch int64) (executionapp.FileOutcome, error) {
	client, diagnostics, err := a.clientForDiagnostics(ctx, executorID)
	if err != nil {
		return executionapp.FileOutcome{}, err
	}
	diagnostics.SessionID = sessionID
	ctx = executorinfra.WithCallDiagnostics(ctx, diagnostics)
	result, err := client.Files(ctx, sessionID, executorinfra.FileRequest{
		Op:         op.Op,
		Path:       op.Path,
		Base64:     op.Base64,
		LeaseEpoch: leaseEpoch,
	})
	if err != nil {
		return executionapp.FileOutcome{}, err
	}
	files := make([]executionapp.FileEntry, 0, len(result.Files))
	for _, entry := range result.Files {
		files = append(files, executionapp.FileEntry{
			Path:  entry.Path,
			Bytes: entry.Bytes,
			MIME:  entry.MIME,
			Dir:   entry.Dir,
		})
	}
	return executionapp.FileOutcome{
		Path:      result.Path,
		Bytes:     result.Bytes,
		SHA256:    result.SHA256,
		MIME:      result.MIME,
		Base64:    result.Base64,
		Files:     files,
		Truncated: result.Truncated,
	}, nil
}

// Artifacts pulls the session recording ledger from an absolute cursor.
func (a *ExecutionAdapter) Artifacts(ctx context.Context, executorID int64, sessionID string, since int64) (executionapp.ArtifactPage, error) {
	client, diagnostics, err := a.clientForDiagnostics(ctx, executorID)
	if err != nil {
		return executionapp.ArtifactPage{}, err
	}
	diagnostics.SessionID = sessionID
	ctx = executorinfra.WithCallDiagnostics(ctx, diagnostics)
	page, err := client.Artifacts(ctx, sessionID, since)
	if err != nil {
		return executionapp.ArtifactPage{}, err
	}
	artifacts := make([]executionapp.ExecutionArtifact, 0, len(page.Artifacts))
	for _, artifact := range page.Artifacts {
		artifacts = append(artifacts, executionapp.ExecutionArtifact{
			Kind:            artifact.Kind,
			CommandID:       artifact.CommandID,
			Path:            artifact.Path,
			SHA256:          artifact.SHA256,
			Size:            artifact.Size,
			MIME:            artifact.MIME,
			Filename:        artifact.Filename,
			Base64:          artifact.Base64,
			ContentOmitted:  artifact.ContentOmitted,
			ContentReason:   artifact.ContentReason,
			RecordingFailed: artifact.RecordingFailed,
			Error:           artifact.Error,
		})
	}
	return executionapp.ArtifactPage{
		Artifacts:      artifacts,
		NextCursor:     page.NextCursor,
		BaseCursor:     page.BaseCursor,
		Evicted:        page.Evicted,
		Gap:            page.Gap,
		Missed:         page.Missed,
		EvictionFailed: page.EvictionFailed,
	}, nil
}

// Trace reads the session's trace file reference.
func (a *ExecutionAdapter) Trace(ctx context.Context, executorID int64, sessionID string) (executionapp.TraceRef, error) {
	client, diagnostics, err := a.clientForDiagnostics(ctx, executorID)
	if err != nil {
		return executionapp.TraceRef{}, err
	}
	diagnostics.SessionID = sessionID
	ctx = executorinfra.WithCallDiagnostics(ctx, diagnostics)
	ref, err := client.Trace(ctx, sessionID)
	if err != nil {
		return executionapp.TraceRef{}, err
	}
	return executionapp.TraceRef{
		Available: ref.Available,
		Path:      ref.Path,
		Bytes:     ref.Bytes,
		SHA256:    ref.SHA256,
		MIME:      ref.MIME,
	}, nil
}

// RenewLease renews (or, with a higher epoch, takes over) one session lease.
func (a *ExecutionAdapter) RenewLease(ctx context.Context, executorID int64, sessionID string, leaseEpoch int64) error {
	client, diagnostics, err := a.clientForDiagnostics(ctx, executorID)
	if err != nil {
		return err
	}
	diagnostics.SessionID = sessionID
	ctx = executorinfra.WithCallDiagnostics(ctx, diagnostics)
	_, err = client.RenewLease(ctx, sessionID, leaseEpoch)
	return err
}

// NetworkLog reads the session's recorded browser traffic log (pure CDP
// recording, T16; design §4.3). The raw payload is returned verbatim for the
// discovery capture to parse and store as network evidence.
func (a *ExecutionAdapter) NetworkLog(ctx context.Context, executorID int64, sessionID string, tail int) (json.RawMessage, error) {
	client, diagnostics, err := a.clientForDiagnostics(ctx, executorID)
	if err != nil {
		return nil, err
	}
	diagnostics.SessionID = sessionID
	ctx = executorinfra.WithCallDiagnostics(ctx, diagnostics)
	return client.NetworkLog(ctx, sessionID, tail)
}

// Cancel aborts the in-flight command of one session (best-effort).
func (a *ExecutionAdapter) Cancel(ctx context.Context, executorID int64, sessionID string) error {
	client, diagnostics, err := a.clientForDiagnostics(ctx, executorID)
	if err != nil {
		return err
	}
	diagnostics.SessionID = sessionID
	ctx = executorinfra.WithCallDiagnostics(ctx, diagnostics)
	_, err = client.Cancel(ctx, sessionID)
	if isSessionNotFound(err) {
		return nil
	}
	return err
}

// Close deletes one session (best-effort: a missing session is already gone).
func (a *ExecutionAdapter) Close(ctx context.Context, executorID int64, sessionID string) error {
	client, diagnostics, err := a.clientForDiagnostics(ctx, executorID)
	if err != nil {
		return err
	}
	diagnostics.SessionID = sessionID
	ctx = executorinfra.WithCallDiagnostics(ctx, diagnostics)
	err = client.CloseSession(ctx, sessionID)
	if isSessionNotFound(err) {
		return nil
	}
	return err
}

// ExecutorProbeAdapter adapts the executor client health surface to the registry
// probe port.
type ExecutorProbeAdapter struct {
	client executorinfra.Client
}

// NewExecutorProbe wires the registry probe adapter.
func NewExecutorProbe(client executorinfra.Client) *ExecutorProbeAdapter {
	return &ExecutorProbeAdapter{client: client}
}

var _ executionapp.ExecutorProbe = (*ExecutorProbeAdapter)(nil)

// Health probes GET /healthz and converts the neutral shape.
func (a *ExecutorProbeAdapter) Health(ctx context.Context) (executionapp.ExecutorHealth, error) {
	if a.client == nil {
		return executionapp.ExecutorHealth{}, errNotConfigured("executor client")
	}
	health, err := a.client.Health(ctx)
	if err != nil {
		return executionapp.ExecutorHealth{}, err
	}
	return executionapp.ExecutorHealth{
		Status:            health.Status,
		Version:           health.Version,
		Sessions:          health.Sessions,
		MaxSessions:       health.MaxSessions,
		Draining:          health.Draining,
		ReclaimedSessions: health.ReclaimedSessions,
		Error:             health.Error,
	}, nil
}

// ExecutorControlAdapter adapts the executor client to the ExecutorControl port
// used by pause/cancel/session-TTL cleanup.
type ExecutorControlAdapter struct {
	client executorinfra.Client
	router *ExecutorRouter
}

// NewExecutorControlWithRouter wires the per-executor control adapter.
func NewExecutorControlWithRouter(router *ExecutorRouter) *ExecutorControlAdapter {
	return &ExecutorControlAdapter{router: router}
}

var _ executionapp.ExecutorControl = (*ExecutorControlAdapter)(nil)

func (a *ExecutorControlAdapter) clientFor(ctx context.Context, executorID int64) (executorinfra.Client, error) {
	client, _, err := a.clientForDiagnostics(ctx, executorID)
	return client, err
}

func (a *ExecutorControlAdapter) clientForDiagnostics(ctx context.Context, executorID int64) (executorinfra.Client, executorinfra.CallDiagnostics, error) {
	diagnostics := executorinfra.CallDiagnostics{OwnerExecutorID: executorID}
	if a.router != nil {
		client, route, err := a.router.clientForDiagnostics(ctx, executorID)
		if err != nil {
			return nil, diagnostics, err
		}
		registryHit := route.RegistryHit
		diagnostics.RegistryHit = &registryHit
		return client, diagnostics, nil
	}
	if a.client == nil {
		return nil, diagnostics, errNotConfigured("executor client")
	}
	return a.client, diagnostics, nil
}

// CancelSession aborts an in-flight command/action; a missing session is a no-op.
func (a *ExecutorControlAdapter) CancelSession(ctx context.Context, executorID int64, sessionID string) error {
	client, diagnostics, err := a.clientForDiagnostics(ctx, executorID)
	if err != nil {
		return err
	}
	diagnostics.SessionID = sessionID
	ctx = executorinfra.WithCallDiagnostics(ctx, diagnostics)
	_, err = client.Cancel(ctx, sessionID)
	if isSessionNotFound(err) {
		return nil
	}
	return err
}

// DeleteSession closes one executor session; a missing session is a no-op.
func (a *ExecutorControlAdapter) DeleteSession(ctx context.Context, executorID int64, sessionID string) error {
	client, diagnostics, err := a.clientForDiagnostics(ctx, executorID)
	if err != nil {
		return err
	}
	diagnostics.SessionID = sessionID
	ctx = executorinfra.WithCallDiagnostics(ctx, diagnostics)
	err = client.CloseSession(ctx, sessionID)
	if isSessionNotFound(err) {
		return nil
	}
	return err
}

// ListSessions lists every executor-side session (orphan reconciliation).
func (a *ExecutorControlAdapter) ListSessions(ctx context.Context) ([]executionapp.ExecutorSession, error) {
	clients := []routedClient{}
	if a.router != nil {
		routed, err := a.router.clients(ctx)
		if err != nil {
			return nil, err
		}
		clients = routed
	} else {
		client, err := a.clientFor(ctx, 0)
		if err != nil {
			return nil, err
		}
		clients = []routedClient{{Client: client}}
	}
	refs := make([]executionapp.ExecutorSession, 0)
	for _, routed := range clients {
		callCtx := executorinfra.WithCallDiagnostics(ctx, executorinfra.CallDiagnostics{OwnerExecutorID: routed.ExecutorID})
		sessions, err := routed.Client.ListSessions(callCtx)
		if err != nil {
			return nil, err
		}
		for _, session := range sessions {
			refs = append(refs, executionapp.ExecutorSession{
				ExecutorID: routed.ExecutorID,
				ID:         session.ID,
				Key:        session.Key,
			})
		}
	}
	return refs, nil
}

func isSessionNotFound(err error) bool {
	var apiErr *platform.Error
	return errors.As(err, &apiErr) && apiErr.Code == executorinfra.CodeSessionNotFound
}

// caseRunIDFromMetadata recovers the case_run correlation that the API stamps
// in every session metadata (identity format string, but a numeric value is
// accepted for direct callers/tests).
func caseRunIDFromMetadata(metadata map[string]any) int64 {
	return int64FromMetadata(metadata, "case_run_id")
}

// attemptFromMetadata reads the dispatch attempt when the caller stamps it in
// the session metadata.
func attemptFromMetadata(metadata map[string]any) int {
	return int(int64FromMetadata(metadata, "attempt"))
}

func int64FromMetadata(metadata map[string]any, key string) int64 {
	if metadata == nil {
		return 0
	}
	switch value := metadata[key].(type) {
	case string:
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err == nil && parsed > 0 {
			return parsed
		}
	case json.Number:
		parsed, err := value.Int64()
		if err == nil && parsed > 0 {
			return parsed
		}
	case int64:
		if value > 0 {
			return value
		}
	case int:
		if value > 0 {
			return int64(value)
		}
	case float64:
		// Only a positive integral value is a valid id: truncating 42.9 or a
		// negative/non-finite number would invent a misleading correlation.
		if value > 0 && value <= math.MaxInt64 && value == math.Trunc(value) {
			return int64(value)
		}
	}
	return 0
}
