package adapters

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	executorinfra "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/executor"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// TestBrowserAdapterAttachesExecutorRouteDiagnostics pins the v0.2.3 X-01
// requirement that a failed browser call names the routed owner, endpoint and
// whether the registry supplied it, on top of the client's request diagnostics.
func TestBrowserAdapterAttachesExecutorRouteDiagnostics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	defer server.Close()

	resolver := executorinfra.NewResolver("token", 30*time.Millisecond)
	lookup := &fakeExecutorLookup{rows: map[int64]execution.ExecutorInstance{
		4: {ID: 4, Endpoint: server.URL, Status: execution.ExecutorActive},
	}}
	router := NewExecutorRouter(resolver, lookup, "http://127.0.0.1:1")
	adapter := NewBrowserAdapterWithRouter(router)

	_, err := adapter.Action(context.Background(), 4, "ctx_1", "goto", nil)
	perr := platform.AsError(err)
	if perr.Code != "executor_timeout" {
		t.Fatalf("error = %+v, want executor_timeout", perr)
	}
	details := perr.Details
	if got, _ := details["owner_executor_id"].(int64); got != 4 {
		t.Fatalf("details.owner_executor_id = %#v, want 4", details["owner_executor_id"])
	}
	if got, _ := details["registry_hit"].(bool); !got {
		t.Fatalf("details.registry_hit = %#v, want true", details["registry_hit"])
	}
	if got, _ := details["endpoint"].(string); got != server.URL {
		t.Fatalf("details.endpoint = %q, want %q", got, server.URL)
	}
	if got, _ := details["context_id"].(string); got != "ctx_1" {
		t.Fatalf("details.context_id = %q, want ctx_1", got)
	}
	if phase, _ := details["phase"].(string); phase == "" {
		t.Fatalf("details.phase is empty: %#v", details)
	}
	if requestID, _ := details["request_id"].(string); requestID == "" {
		t.Fatalf("details.request_id is empty: %#v", details)
	}

	// An unknown owner falls back to the primary endpoint with registry_hit=false
	// and keeps the case_run correlation parsed from the create metadata.
	fallback := NewBrowserAdapterWithRouter(NewExecutorRouter(resolver, lookup, server.URL))
	_, err = fallback.CreateContext(context.Background(), executionapp.BrowserContextSpec{
		ExecutorID:   9,
		Key:          "run:3001:vs:7:role:admin",
		AllowedHosts: []string{"127.0.0.1"},
		Metadata:     map[string]any{"case_run_id": "3001", "attempt": 2},
	})
	perr = platform.AsError(err)
	if perr.Code != "executor_timeout" {
		t.Fatalf("fallback error = %+v, want executor_timeout", perr)
	}
	details = perr.Details
	if got, _ := details["owner_executor_id"].(int64); got != 9 {
		t.Fatalf("fallback owner_executor_id = %#v, want 9", details["owner_executor_id"])
	}
	if got, _ := details["registry_hit"].(bool); got {
		t.Fatalf("fallback registry_hit = %#v, want false", details["registry_hit"])
	}
	if got, _ := details["case_run_id"].(int64); got != 3001 {
		t.Fatalf("details.case_run_id = %#v, want 3001", details["case_run_id"])
	}
	if got, _ := details["attempt"].(int); got != 2 {
		t.Fatalf("details.attempt = %#v, want 2", details["attempt"])
	}
}

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
