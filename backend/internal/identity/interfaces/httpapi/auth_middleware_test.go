package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// stubAuthenticator adapts a function to the Authenticator port.
type stubAuthenticator struct {
	authenticate func(ctx context.Context, rawToken string) (identity.User, error)
}

func (s stubAuthenticator) Authenticate(ctx context.Context, rawToken string) (identity.User, error) {
	return s.authenticate(ctx, rawToken)
}

func rejectHandler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("authenticated handler must not run when authentication fails")
	})
}

// TestAuthMiddlewareMapsTokenRejectionsTo401 pins the client-error path: an
// expired/revoked/unknown token is still an authentication failure even when
// the use case wraps the sentinel error.
func TestAuthMiddlewareMapsTokenRejectionsTo401(t *testing.T) {
	h := AuthMiddleware(stubAuthenticator{authenticate: func(context.Context, string) (identity.User, error) {
		return identity.User{}, fmt.Errorf("identity: authenticate: %w", identity.ErrUnauthenticated)
	}})(rejectHandler(t))

	req := newTestRequest(http.MethodGet, "/api/admin/secret", nil)
	req.Header.Set("Authorization", "Bearer expired-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401 body=%s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Fatalf("WWW-Authenticate=%q want Bearer", got)
	}
	if apiErr := decodeAPIError(t, rec); apiErr.Code != platform.CodeUnauthenticated {
		t.Fatalf("error code=%q want %q", apiErr.Code, platform.CodeUnauthenticated)
	}
}

// TestAuthMiddlewareMapsBackendFailureTo503 proves a database or infrastructure
// outage is not reported as a bad token: the client must not discard its
// credentials, so the response is 503 unavailable without a Bearer challenge,
// and the server log records the failure without the presented token.
func TestAuthMiddlewareMapsBackendFailureTo503(t *testing.T) {
	var logBuf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	const token = "tok_do_not_log_0123456789"
	h := AuthMiddleware(stubAuthenticator{authenticate: func(context.Context, string) (identity.User, error) {
		return identity.User{}, fmt.Errorf("identity: authenticate: %w", errors.New("database connection refused"))
	}})(rejectHandler(t))

	req := newTestRequest(http.MethodGet, "/api/admin/secret", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 body=%s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != "" {
		t.Fatalf("WWW-Authenticate=%q on backend failure; only 401s carry the challenge", got)
	}
	apiErr := decodeAPIError(t, rec)
	if apiErr.Code != platform.CodeUnavailable {
		t.Fatalf("error code=%q want %q", apiErr.Code, platform.CodeUnavailable)
	}
	body := rec.Body.String()
	if strings.Contains(body, "database") || strings.Contains(body, "identity:") {
		t.Fatalf("503 response leaks the internal failure: %s", body)
	}
	if !strings.Contains(logBuf.String(), "database connection refused") {
		t.Fatalf("backend failure was not logged: %q", logBuf.String())
	}
	if strings.Contains(logBuf.String(), token) {
		t.Fatalf("auth log contains the bearer token: %q", logBuf.String())
	}
}
