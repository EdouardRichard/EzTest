package adapters

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
)

// authCheckerSettings is the execution settings view read by the auth checker.
// Only execution.executor_token matters here; any other key is a test failure.
type authCheckerSettings struct {
	token string
	err   error
}

func (s authCheckerSettings) String(_ context.Context, key string) (string, error) {
	if key != executionapp.SettingExecutorToken {
		return "", errors.New("unexpected setting " + key)
	}
	if s.err != nil {
		return "", s.err
	}
	return s.token, nil
}

func (authCheckerSettings) Int(context.Context, string) (int64, error) { return 0, nil }

func (authCheckerSettings) Bool(context.Context, string) (bool, error) { return false, nil }

// TestExecutorAuthCheckerMapsProbeOutcomes is the D8 contract: the admin action
// must distinguish a valid bearer token (ok) from a rejected/missing token
// (unauthenticated) and from an executor that cannot be reached at all
// (unreachable). The probe reuses the token-bearing GET /v1/contexts self-check;
// /healthz is unauthenticated and cannot prove the token works.
func TestExecutorAuthCheckerMapsProbeOutcomes(t *testing.T) {
	const token = "super-secret-executor-token"

	t.Run("ok", func(t *testing.T) {
		var authorization string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/v1/contexts" {
				t.Errorf("request = %s %s, want GET /v1/contexts", r.Method, r.URL.Path)
			}
			authorization = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"contexts":[]}`))
		}))
		defer server.Close()

		status, detail := NewExecutorAuthChecker(authCheckerSettings{token: token}).Check(context.Background(), server.URL)
		if status != executionapp.ExecutorAuthOK {
			t.Fatalf("Check() status = %q, want %q (detail %q)", status, executionapp.ExecutorAuthOK, detail)
		}
		if detail != "" {
			t.Fatalf("Check() detail = %q, want empty on success", detail)
		}
		if authorization != "Bearer "+token {
			t.Fatalf("Authorization = %q, want the configured bearer token", authorization)
		}
	})

	t.Run("unauthorized", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		status, detail := NewExecutorAuthChecker(authCheckerSettings{token: token}).Check(context.Background(), server.URL)
		if status != executionapp.ExecutorAuthUnauthenticated {
			t.Fatalf("Check() status = %q, want %q (detail %q)", status, executionapp.ExecutorAuthUnauthenticated, detail)
		}
		if !strings.Contains(detail, "401") {
			t.Fatalf("Check() detail = %q, want it to name HTTP 401", detail)
		}
		if strings.Contains(detail, token) {
			t.Fatalf("Check() detail leaks the bearer token: %q", detail)
		}
	})

	t.Run("server error is unreachable", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		status, detail := NewExecutorAuthChecker(authCheckerSettings{token: token}).Check(context.Background(), server.URL)
		if status != executionapp.ExecutorAuthUnreachable {
			t.Fatalf("Check() status = %q, want %q (detail %q)", status, executionapp.ExecutorAuthUnreachable, detail)
		}
		if !strings.Contains(detail, "500") {
			t.Fatalf("Check() detail = %q, want it to name HTTP 500", detail)
		}
	})

	t.Run("transport failure is unreachable", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		endpoint := server.URL
		server.Close()

		status, detail := NewExecutorAuthChecker(authCheckerSettings{token: token}).Check(context.Background(), endpoint)
		if status != executionapp.ExecutorAuthUnreachable {
			t.Fatalf("Check() status = %q, want %q (detail %q)", status, executionapp.ExecutorAuthUnreachable, detail)
		}
		// A dead endpoint has no bearer token to echo; detail still must not
		// carry the configured value.
		if strings.Contains(detail, token) {
			t.Fatalf("Check() detail leaks the bearer token: %q", detail)
		}
	})
}

// TestExecutorAuthCheckerMissingConfiguration pins the two no-request outcomes:
// an empty endpoint is unreachable, and an empty/unreadable token is
// unauthenticated without ever probing the executor.
func TestExecutorAuthCheckerMissingConfiguration(t *testing.T) {
	t.Run("empty endpoint", func(t *testing.T) {
		status, detail := NewExecutorAuthChecker(authCheckerSettings{token: "token"}).Check(context.Background(), "  ")
		if status != executionapp.ExecutorAuthUnreachable {
			t.Fatalf("Check() status = %q, want %q", status, executionapp.ExecutorAuthUnreachable)
		}
		if detail != "endpoint 未配置" {
			t.Fatalf("Check() detail = %q, want %q", detail, "endpoint 未配置")
		}
	})

	for _, tc := range []struct {
		name     string
		settings authCheckerSettings
	}{
		{name: "empty token", settings: authCheckerSettings{token: "   "}},
		{name: "settings read failure", settings: authCheckerSettings{err: errors.New("settings unavailable")}},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var probes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				probes.Add(1)
			}))
			defer server.Close()

			status, detail := NewExecutorAuthChecker(tc.settings).Check(context.Background(), server.URL)
			if status != executionapp.ExecutorAuthUnauthenticated {
				t.Fatalf("Check() status = %q, want %q (detail %q)", status, executionapp.ExecutorAuthUnauthenticated, detail)
			}
			if detail != "Bearer token 未配置" {
				t.Fatalf("Check() detail = %q, want %q", detail, "Bearer token 未配置")
			}
			if got := probes.Load(); got != 0 {
				t.Fatalf("Check() probed the executor %d time(s) without a token, want 0", got)
			}
		})
	}
}

// TestTruncateExecutorAuthDetail bounds the diagnostic detail and never splits
// a multi-byte character.
func TestTruncateExecutorAuthDetail(t *testing.T) {
	unchanged := "executor returned HTTP 401"
	if got := truncateExecutorAuthDetail(unchanged); got != unchanged {
		t.Fatalf("truncateExecutorAuthDetail() = %q, want %q", got, unchanged)
	}
	long := strings.Repeat("认证失败", 100)
	got := truncateExecutorAuthDetail(long)
	if runes := []rune(got); len(runes) != 300 {
		t.Fatalf("truncated detail has %d runes, want 300", len(runes))
	}
	if !strings.HasPrefix(got, "认证失败") {
		t.Fatalf("truncated detail = %q, want the original prefix", got)
	}
}
