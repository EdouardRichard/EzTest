package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executorinfra "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/executor"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// errNotConfigured is the internal wiring error of an unset adapter dependency.
func errNotConfigured(name string) error {
	return fmt.Errorf("%s is not configured", name)
}

// BrowserAdapter adapts the executor protocol client to the execution browser
// port. One client call maps one action; context lifecycle is owned by the
// CaseRunner.
type BrowserAdapter struct {
	client executorinfra.Client
	router *ExecutorRouter
}

// NewBrowserAdapter wires the browser executor adapter with a single client
// (P3 single-endpoint mode).
func NewBrowserAdapter(client executorinfra.Client) *BrowserAdapter {
	return &BrowserAdapter{client: client}
}

// NewBrowserAdapterWithRouter wires the browser executor adapter with the P8
// per-executor router. The router takes precedence over client.
func NewBrowserAdapterWithRouter(router *ExecutorRouter) *BrowserAdapter {
	return &BrowserAdapter{router: router}
}

var _ executionapp.BrowserExecutor = (*BrowserAdapter)(nil)

func (a *BrowserAdapter) clientFor(ctx context.Context, executorID int64) (executorinfra.Client, error) {
	if a.router != nil {
		return a.router.clientFor(ctx, executorID)
	}
	if a.client == nil {
		return nil, errNotConfigured("executor client")
	}
	return a.client, nil
}

// CreateContext creates one executor browser context and returns its id.
func (a *BrowserAdapter) CreateContext(ctx context.Context, spec executionapp.BrowserContextSpec) (string, error) {
	client, err := a.clientFor(ctx, spec.ExecutorID)
	if err != nil {
		return "", err
	}
	info, err := client.CreateContext(ctx, executorinfra.CreateContextInput{
		Key:          spec.Key,
		AllowedHosts: spec.AllowedHosts,
		BaseURL:      spec.BaseURL,
		Metadata:     spec.Metadata,
	})
	if err != nil {
		return "", err
	}
	return info.ID, nil
}

// Action runs one executor action and maps its envelope.
func (a *BrowserAdapter) Action(ctx context.Context, executorID int64, contextID, action string, args json.RawMessage) (executionapp.BrowserActionResult, error) {
	client, err := a.clientFor(ctx, executorID)
	if err != nil {
		return executionapp.BrowserActionResult{}, err
	}
	result, err := client.Action(ctx, contextID, action, args)
	if err != nil {
		return executionapp.BrowserActionResult{}, err
	}
	artifacts := make([]executionapp.BrowserArtifact, 0, len(result.Artifacts))
	for _, artifact := range result.Artifacts {
		artifacts = append(artifacts, executionapp.BrowserArtifact{
			Kind:     artifact.Kind,
			MIME:     artifact.MIME,
			Base64:   artifact.Base64,
			SHA256:   artifact.SHA256,
			Filename: artifact.Filename,
			Bytes:    artifact.Bytes,
		})
	}
	mapped := executionapp.BrowserActionResult{
		OK:        result.OK,
		Result:    result.Result,
		Artifacts: artifacts,
	}
	if result.Error != nil {
		mapped.Error = &executionapp.BrowserActionError{Code: result.Error.Code, Message: result.Error.Message}
	}
	return mapped, nil
}

// Cancel aborts the in-flight action of one context (best-effort).
func (a *BrowserAdapter) Cancel(ctx context.Context, executorID int64, contextID string) error {
	client, err := a.clientFor(ctx, executorID)
	if err != nil {
		return err
	}
	_, err = client.Cancel(ctx, contextID)
	if isContextNotFound(err) {
		return nil
	}
	return err
}

// Close deletes one context (best-effort: a missing context is already gone).
func (a *BrowserAdapter) Close(ctx context.Context, executorID int64, contextID string) error {
	client, err := a.clientFor(ctx, executorID)
	if err != nil {
		return err
	}
	err = client.Close(ctx, contextID)
	if isContextNotFound(err) {
		return nil
	}
	return err
}

// ListContexts lists the executor contexts (orphan reconciliation).
func (a *BrowserAdapter) ListContexts(ctx context.Context, executorID int64) ([]executionapp.BrowserContextRef, error) {
	client, err := a.clientFor(ctx, executorID)
	if err != nil {
		return nil, err
	}
	contexts, err := client.ListContexts(ctx)
	if err != nil {
		return nil, err
	}
	refs := make([]executionapp.BrowserContextRef, 0, len(contexts))
	for _, contextInfo := range contexts {
		refs = append(refs, executionapp.BrowserContextRef{ExecutorID: executorID, ID: contextInfo.ID, Key: contextInfo.Key})
	}
	return refs, nil
}

// ExecutorProbeAdapter adapts the executor client health/inventory surface to
// the registry probe port.
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
		Status:         health.Status,
		Version:        health.Version,
		Contexts:       health.Contexts,
		MaxContexts:    health.MaxContexts,
		BrowserVersion: health.BrowserVersion,
		Error:          health.Error,
	}, nil
}

// ListContexts lists executor contexts for the pause/cleanup services.
func (a *ExecutorProbeAdapter) ListContexts(ctx context.Context) ([]executionapp.ExecutorContextRef, error) {
	contexts, err := a.client.ListContexts(ctx)
	if err != nil {
		return nil, err
	}
	refs := make([]executionapp.ExecutorContextRef, 0, len(contexts))
	for _, contextInfo := range contexts {
		refs = append(refs, executionapp.ExecutorContextRef{ID: contextInfo.ID, Key: contextInfo.Key})
	}
	return refs, nil
}

// ExecutorControlAdapter adapts the executor client to the ExecutorControl port
// used by pause/cancel/context-TTL cleanup.
type ExecutorControlAdapter struct {
	client executorinfra.Client
	router *ExecutorRouter
}

// NewExecutorControl wires the executor control adapter with a single client.
func NewExecutorControl(client executorinfra.Client) *ExecutorControlAdapter {
	return &ExecutorControlAdapter{client: client}
}

// NewExecutorControlWithRouter wires the P8 per-executor control adapter.
func NewExecutorControlWithRouter(router *ExecutorRouter) *ExecutorControlAdapter {
	return &ExecutorControlAdapter{router: router}
}

var _ executionapp.ExecutorControl = (*ExecutorControlAdapter)(nil)

func (a *ExecutorControlAdapter) clientFor(ctx context.Context, executorID int64) (executorinfra.Client, error) {
	if a.router != nil {
		return a.router.clientFor(ctx, executorID)
	}
	if a.client == nil {
		return nil, errNotConfigured("executor client")
	}
	return a.client, nil
}

// CancelContext aborts an in-flight action; a missing context is a no-op.
func (a *ExecutorControlAdapter) CancelContext(ctx context.Context, executorID int64, contextID string) error {
	client, err := a.clientFor(ctx, executorID)
	if err != nil {
		return err
	}
	_, err = client.Cancel(ctx, contextID)
	if isContextNotFound(err) {
		return nil
	}
	return err
}

// DeleteContext closes one executor context; a missing context is a no-op.
func (a *ExecutorControlAdapter) DeleteContext(ctx context.Context, executorID int64, contextID string) error {
	client, err := a.clientFor(ctx, executorID)
	if err != nil {
		return err
	}
	err = client.Close(ctx, contextID)
	if isContextNotFound(err) {
		return nil
	}
	return err
}

// ListContexts lists every executor-side context (orphan reconciliation).
func (a *ExecutorControlAdapter) ListContexts(ctx context.Context) ([]executionapp.ExecutorContext, error) {
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
	refs := make([]executionapp.ExecutorContext, 0)
	for _, routed := range clients {
		contexts, err := routed.Client.ListContexts(ctx)
		if err != nil {
			return nil, err
		}
		for _, contextInfo := range contexts {
			refs = append(refs, executionapp.ExecutorContext{
				ExecutorID: routed.ExecutorID,
				ID:         contextInfo.ID,
				Key:        contextInfo.Key,
			})
		}
	}
	return refs, nil
}

func isContextNotFound(err error) bool {
	var apiErr *platform.Error
	return errors.As(err, &apiErr) && apiErr.Code == "context_not_found"
}
