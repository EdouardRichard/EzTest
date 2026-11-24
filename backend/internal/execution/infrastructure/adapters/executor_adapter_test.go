package adapters

import (
	"context"
	"encoding/json"
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

// TestExecutionAdapterAttachesExecutorRouteDiagnostics pins the v0.2.3 X-01
// requirement that a failed execution call names the routed owner, endpoint and
// whether the registry supplied it, on top of the client's request diagnostics.
func TestExecutionAdapterAttachesExecutorRouteDiagnostics(t *testing.T) {
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
	adapter := NewExecutionAdapterWithRouter(router)

	_, err := adapter.NetworkLog(context.Background(), 4, "sess_1", 200)
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
	if got, _ := details["session_id"].(string); got != "sess_1" {
		t.Fatalf("details.session_id = %q, want sess_1", got)
	}
	if phase, _ := details["phase"].(string); phase == "" {
		t.Fatalf("details.phase is empty: %#v", details)
	}
	if requestID, _ := details["request_id"].(string); requestID == "" {
		t.Fatalf("details.request_id is empty: %#v", details)
	}

	// An unknown owner falls back to the primary endpoint with registry_hit=false
	// and keeps the case_run correlation parsed from the create metadata.
	fallback := NewExecutionAdapterWithRouter(NewExecutorRouter(resolver, lookup, server.URL))
	_, err = fallback.CreateSession(context.Background(), executionapp.SessionSpec{
		ExecutorID: 9,
		Key:        "run:3001:sut:7:role:admin",
		Workspace:  "/data/workspaces/run-3001/sut-7-admin",
		Metadata:   map[string]any{"case_run_id": "3001", "attempt": 2},
		LeaseEpoch: 3,
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

// TestInt64FromMetadataNormalizesNumericIDs pins the diagnostic id validation:
// only positive integral values are accepted, so a fractional or negative
// metadata value cannot be truncated into a misleading correlation id.
func TestInt64FromMetadataNormalizesNumericIDs(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  int64
	}{
		{"json number", json.Number("42"), 42},
		{"numeric string", "42", 42},
		{"int64", int64(42), 42},
		{"int", 42, 42},
		{"integral float", float64(42), 42},
		{"integral float64 literal", 42.0, 42},
		{"fractional float", 42.5, 0},
		{"fractional json number", json.Number("42.5"), 0},
		{"negative float", float64(-3), 0},
		{"negative int64", int64(-3), 0},
		{"negative int", -3, 0},
		{"negative string", "-3", 0},
		{"non-numeric string", "abc", 0},
		{"nil value", nil, 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			metadata := map[string]any{"case_run_id": tc.value}
			if got := int64FromMetadata(metadata, "case_run_id"); got != tc.want {
				t.Fatalf("int64FromMetadata(%#v) = %d, want %d", tc.value, got, tc.want)
			}
		})
	}
	if got := int64FromMetadata(nil, "case_run_id"); got != 0 {
		t.Fatalf("int64FromMetadata(nil) = %d, want 0", got)
	}
}

// TestExecutorProbeMapsSessionLifecycle proves the application port carries the
// v2 /healthz session lifecycle fields (capacity/drain/reclaim observability).
func TestExecutorProbeMapsSessionLifecycle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"status":"degraded","sessions":2,"maxSessions":4,"draining":true,"reclaimedSessions":3,"error":"executor is draining"}`)
	}))
	defer server.Close()
	probe := NewExecutorProbe(executorinfra.NewClient(server.URL, "test", time.Second))

	health, err := probe.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if health.Sessions != 2 || health.MaxSessions != 4 || !health.Draining ||
		health.ReclaimedSessions != 3 {
		t.Fatalf("ExecutorHealth = %+v, want the v2 session lifecycle fields", health)
	}
	if health.Status != "degraded" || health.Error != "executor is draining" {
		t.Fatalf("ExecutorHealth = %+v, want the degraded drain diagnostic", health)
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
			_, _ = io.WriteString(w, `{"sessions":[]}`)
		}))
		defer server.Close()

		if err := NewExecutorSelfCheck(server.URL, "test").SelfCheck(context.Background()); err != nil {
			t.Fatalf("SelfCheck() = %v, want nil", err)
		}
		if method != http.MethodGet || path != "/v1/sessions" {
			t.Fatalf("request = %s %s, want GET /v1/sessions", method, path)
		}
		if authorization != "Bearer test" {
			t.Fatalf("Authorization = %q, want %q", authorization, "Bearer test")
		}
	})

	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		status := status
		t.Run("unauthorized_"+http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/sessions" {
					t.Errorf("path = %q, want /v1/sessions", r.URL.Path)
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
