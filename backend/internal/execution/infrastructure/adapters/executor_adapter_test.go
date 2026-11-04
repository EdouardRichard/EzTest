package adapters

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestExecutorSelfCheck pins the startup Bearer self-check contract: the
// executor's /healthz is unauthenticated, so only a protected route can prove
// that the configured token works. Authentication failures and an unreachable
// executor must stay distinguishable for the UI.
func TestExecutorSelfCheck(t *testing.T) {
	t.Run("authenticated", func(t *testing.T) {
		var method, path, authorization string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			method, path = r.Method, r.URL.Path
			authorization = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"contexts":[]}`)
		}))
		defer server.Close()

		if err := NewExecutorSelfCheck(server.URL, "test").SelfCheck(context.Background()); err != nil {
			t.Fatalf("SelfCheck() = %v, want nil", err)
		}
		if method != http.MethodGet || path != "/v1/contexts" {
			t.Fatalf("request = %s %s, want GET /v1/contexts", method, path)
		}
		if authorization != "Bearer test" {
			t.Fatalf("Authorization = %q, want %q", authorization, "Bearer test")
		}
	})

	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		status := status
		t.Run("unauthorized_"+http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/contexts" {
					t.Errorf("path = %q, want /v1/contexts", r.URL.Path)
				}
				w.WriteHeader(status)
			}))
			defer server.Close()

			err := NewExecutorSelfCheck(server.URL, "wrong-token").SelfCheck(context.Background())
			if !errors.Is(err, ErrExecutorUnauthorized) {
				t.Fatalf("SelfCheck() = %v, want ErrExecutorUnauthorized", err)
			}
		})
	}

	t.Run("offline", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		endpoint := server.URL
		server.Close()

		err := NewExecutorSelfCheck(endpoint, "test").SelfCheck(context.Background())
		if !errors.Is(err, ErrExecutorOffline) {
			t.Fatalf("SelfCheck() = %v, want ErrExecutorOffline", err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))
		defer server.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		err := NewExecutorSelfCheck(server.URL, "test").SelfCheck(ctx)
		if !errors.Is(err, ErrExecutorOffline) {
			t.Fatalf("SelfCheck() = %v, want ErrExecutorOffline", err)
		}
	})
}
