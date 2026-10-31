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
}

// NewBrowserAdapter wires the browser executor adapter.
func NewBrowserAdapter(client executorinfra.Client) *BrowserAdapter {
	return &BrowserAdapter{client: client}
}

var _ executionapp.BrowserExecutor = (*BrowserAdapter)(nil)

// CreateContext creates one executor browser context and returns its id.
func (a *BrowserAdapter) CreateContext(ctx context.Context, spec executionapp.BrowserContextSpec) (string, error) {
	if a.client == nil {
		return "", errNotConfigured("executor client")
	}
	info, err := a.client.CreateContext(ctx, executorinfra.CreateContextInput{
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
func (a *BrowserAdapter) Action(ctx context.Context, contextID, action string, args json.RawMessage) (executionapp.BrowserActionResult, error) {
	if a.client == nil {
		return executionapp.BrowserActionResult{}, errNotConfigured("executor client")
	}
	result, err := a.client.Action(ctx, contextID, action, args)
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
func (a *BrowserAdapter) Cancel(ctx context.Context, contextID string) error {
	if a.client == nil {
		return errNotConfigured("executor client")
	}
	_, err := a.client.Cancel(ctx, contextID)
	if isContextNotFound(err) {
		return nil
	}
	return err
}

// Close deletes one context (best-effort: a missing context is already gone).
func (a *BrowserAdapter) Close(ctx context.Context, contextID string) error {
	if a.client == nil {
		return errNotConfigured("executor client")
	}
	err := a.client.Close(ctx, contextID)
	if isContextNotFound(err) {
		return nil
	}
	return err
}

// ListContexts lists the executor contexts (orphan reconciliation).
func (a *BrowserAdapter) ListContexts(ctx context.Context) ([]executionapp.BrowserContextRef, error) {
	if a.client == nil {
		return nil, errNotConfigured("executor client")
	}
	contexts, err := a.client.ListContexts(ctx)
	if err != nil {
		return nil, err
	}
	refs := make([]executionapp.BrowserContextRef, 0, len(contexts))
	for _, contextInfo := range contexts {
		refs = append(refs, executionapp.BrowserContextRef{ID: contextInfo.ID, Key: contextInfo.Key})
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
}

// NewExecutorControl wires the executor control adapter.
func NewExecutorControl(client executorinfra.Client) *ExecutorControlAdapter {
	return &ExecutorControlAdapter{client: client}
}

var _ executionapp.ExecutorControl = (*ExecutorControlAdapter)(nil)

// CancelContext aborts an in-flight action; a missing context is a no-op.
func (a *ExecutorControlAdapter) CancelContext(ctx context.Context, contextID string) error {
	if a.client == nil {
		return errNotConfigured("executor client")
	}
	_, err := a.client.Cancel(ctx, contextID)
	if isContextNotFound(err) {
		return nil
	}
	return err
}

// DeleteContext closes one executor context; a missing context is a no-op.
func (a *ExecutorControlAdapter) DeleteContext(ctx context.Context, contextID string) error {
	if a.client == nil {
		return errNotConfigured("executor client")
	}
	err := a.client.Close(ctx, contextID)
	if isContextNotFound(err) {
		return nil
	}
	return err
}

// ListContexts lists every executor-side context (orphan reconciliation).
func (a *ExecutorControlAdapter) ListContexts(ctx context.Context) ([]executionapp.ExecutorContext, error) {
	if a.client == nil {
		return nil, errNotConfigured("executor client")
	}
	contexts, err := a.client.ListContexts(ctx)
	if err != nil {
		return nil, err
	}
	refs := make([]executionapp.ExecutorContext, 0, len(contexts))
	for _, contextInfo := range contexts {
		refs = append(refs, executionapp.ExecutorContext{ID: contextInfo.ID, Key: contextInfo.Key})
	}
	return refs, nil
}

func isContextNotFound(err error) bool {
	var apiErr *platform.Error
	return errors.As(err, &apiErr) && apiErr.Code == "context_not_found"
}
