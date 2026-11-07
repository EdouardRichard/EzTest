package adapters

import (
	"context"
	"errors"
	"strings"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
)

const executorAuthDetailLimit = 300

// ExecutorAuthCheckerAdapter probes an executor's bearer-protected route with
// the token from execution.executor_token settings (D8). The executor's
// /healthz is unauthenticated, so an offline-looking heartbeat can also mean a
// rejected token; this admin-only probe makes the two causes distinguishable.
type ExecutorAuthCheckerAdapter struct {
	settings executionapp.SettingsReader
}

var _ executionapp.ExecutorAuthChecker = (*ExecutorAuthCheckerAdapter)(nil)

// NewExecutorAuthChecker wires the executor authentication checker.
func NewExecutorAuthChecker(settings executionapp.SettingsReader) *ExecutorAuthCheckerAdapter {
	return &ExecutorAuthCheckerAdapter{settings: settings}
}

// Check probes endpoint with GET /v1/contexts and the configured bearer token.
// An empty endpoint is unreachable; an empty/unreadable token is
// unauthenticated without issuing a request (an unauthenticated probe proves
// nothing about the token).
func (c *ExecutorAuthCheckerAdapter) Check(ctx context.Context, endpoint string) (executionapp.ExecutorAuthStatus, string) {
	if strings.TrimSpace(endpoint) == "" {
		return executionapp.ExecutorAuthUnreachable, "endpoint 未配置"
	}
	token := ""
	if c != nil && c.settings != nil {
		if value, err := c.settings.String(ctx, executionapp.SettingExecutorToken); err == nil {
			token = strings.TrimSpace(value)
		}
	}
	if token == "" {
		return executionapp.ExecutorAuthUnauthenticated, "Bearer token 未配置"
	}
	err := NewExecutorSelfCheck(endpoint, token).SelfCheck(ctx)
	if err == nil {
		return executionapp.ExecutorAuthOK, ""
	}
	if errors.Is(err, ErrExecutorUnauthorized) {
		return executionapp.ExecutorAuthUnauthenticated, truncateExecutorAuthDetail(err.Error())
	}
	return executionapp.ExecutorAuthUnreachable, truncateExecutorAuthDetail(err.Error())
}

// truncateExecutorAuthDetail bounds the diagnostic text without splitting a
// multi-byte character. It never receives or echoes the bearer token.
func truncateExecutorAuthDetail(detail string) string {
	runes := []rune(detail)
	if len(runes) <= executorAuthDetailLimit {
		return detail
	}
	return string(runes[:executorAuthDetailLimit])
}
