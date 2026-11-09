package executor_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/executor"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

const testToken = "test-executor-token"

// newTestClient starts an httptest server and returns a client pointed at it.
func newTestClient(t *testing.T, handler http.HandlerFunc) *executor.HTTPClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return executor.NewClient(server.URL, testToken, 5*time.Second)
}

func decodeJSONBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	return body
}

func requireBearer(t *testing.T, r *http.Request) {
	t.Helper()
	if got, want := r.Header.Get("Authorization"), "Bearer "+testToken; got != want {
		t.Fatalf("Authorization = %q, want %q", got, want)
	}
}

func asPlatformError(t *testing.T, err error) *platform.Error {
	t.Helper()
	var target *platform.Error
	if !errors.As(err, &target) {
		t.Fatalf("error %v is not a *platform.Error", err)
	}
	return target
}

func TestHealthParsesWithoutAuth(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/healthz" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("healthz must not send a bearer token, got %q", got)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","version":"0.2.0-p3","contexts":2,"maxContexts":8,"browserVersion":"Chromium 140"}`))
	})

	health, err := client.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if health.Status != "ok" || health.Version != "0.2.0-p3" || health.Contexts != 2 ||
		health.MaxContexts != 8 || health.BrowserVersion != "Chromium 140" {
		t.Fatalf("Health = %+v", health)
	}
}

func TestHealthDegraded503ReturnsHealth(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"degraded","version":"0.2.0-p3","contexts":0,"maxContexts":8,"error":"browser unavailable"}`))
	})

	health, err := client.Health(context.Background())
	if err != nil {
		t.Fatalf("Health should parse a 503 degraded response, got %v", err)
	}
	if health.Status != "degraded" || health.Error != "browser unavailable" {
		t.Fatalf("Health = %+v", health)
	}
}

func TestHealthTransportFailureIsExecutorUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()

	client := executor.NewClient(url, testToken, 200*time.Millisecond)
	_, err := client.Health(context.Background())
	perr := asPlatformError(t, err)
	if perr.Status != http.StatusServiceUnavailable || perr.Code != "executor_unavailable" || !perr.Retryable {
		t.Fatalf("error = %+v", perr)
	}
	// A refused dial is not a timeout: it keeps executor_unavailable and reports
	// the dial phase so the caller can tell it apart from executor_timeout.
	if phase, _ := perr.Details["phase"].(string); phase != "dial" {
		t.Fatalf("details.phase = %q, want dial (details: %#v)", phase, perr.Details)
	}
	if got, _ := perr.Details["endpoint"].(string); got != url {
		t.Fatalf("details.endpoint = %q, want %q", got, url)
	}
	if requestID, _ := perr.Details["request_id"].(string); requestID == "" {
		t.Fatalf("details.request_id is empty: %#v", perr.Details)
	}
}

func TestCreateContextSendsAuthAndParsesInfo(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/contexts" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		requireBearer(t, r)
		body := decodeJSONBody(t, r)
		if body["key"] != "run:42:vs:7:role:admin" {
			t.Errorf("key = %v", body["key"])
		}
		hosts, _ := body["allowedHosts"].([]any)
		if len(hosts) != 1 || hosts[0] != "app.example.test" {
			t.Errorf("allowedHosts = %v", body["allowedHosts"])
		}
		if body["baseUrl"] != "http://app.example.test:13335" {
			t.Errorf("baseUrl = %v", body["baseUrl"])
		}
		metadata, _ := body["metadata"].(map[string]any)
		if metadata["account_role"] != "admin" || metadata["case_run_id"] != json.Number("42") {
			t.Errorf("metadata = %v", body["metadata"])
		}
		viewport, _ := body["viewport"].(map[string]any)
		if viewport["width"] != json.Number("1440") || viewport["height"] != json.Number("900") {
			t.Errorf("viewport = %v", body["viewport"])
		}
		_, _ = w.Write([]byte(`{"contextId":"ctx_1","browserVersion":"Chromium 140"}`))
	})

	info, err := client.CreateContext(context.Background(), executor.CreateContextInput{
		Key:          "run:42:vs:7:role:admin",
		AllowedHosts: []string{"app.example.test"},
		BaseURL:      "http://app.example.test:13335",
		Metadata:     map[string]any{"case_run_id": 42, "account_role": "admin"},
		Viewport:     &executor.Viewport{Width: 1440, Height: 900},
	})
	if err != nil {
		t.Fatalf("CreateContext: %v", err)
	}
	if info.ID != "ctx_1" || info.Key != "run:42:vs:7:role:admin" || info.Metadata["account_role"] != "admin" {
		t.Fatalf("ContextInfo = %+v", info)
	}
}

func TestCreateContextOmitsOptionalFields(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		body := decodeJSONBody(t, r)
		for _, field := range []string{"metadata", "viewport", "storageState", "baseUrl", "key"} {
			if _, ok := body[field]; ok {
				t.Errorf("expected %q to be omitted, body = %v", field, body)
			}
		}
		_, _ = w.Write([]byte(`{"contextId":"ctx_2"}`))
	})

	if _, err := client.CreateContext(context.Background(), executor.CreateContextInput{
		AllowedHosts: []string{"127.0.0.1"},
	}); err != nil {
		t.Fatalf("CreateContext: %v", err)
	}
}

func TestListContexts(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/contexts" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		requireBearer(t, r)
		_, _ = w.Write([]byte(`{"contexts":[{"id":"ctx_1","key":"run:42","metadata":{"case_run_id":"42"},"createdAt":"2026-11-01T00:00:00Z"}]}`))
	})

	contexts, err := client.ListContexts(context.Background())
	if err != nil {
		t.Fatalf("ListContexts: %v", err)
	}
	if len(contexts) != 1 {
		t.Fatalf("contexts = %+v", contexts)
	}
	got := contexts[0]
	if got.ID != "ctx_1" || got.Key != "run:42" || got.Metadata["case_run_id"] != "42" ||
		got.CreatedAt != "2026-11-01T00:00:00Z" {
		t.Fatalf("ContextInfo = %+v", got)
	}
}

func TestListContextsNullIsEmpty(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"contexts":null}`))
	})
	contexts, err := client.ListContexts(context.Background())
	if err != nil {
		t.Fatalf("ListContexts: %v", err)
	}
	if len(contexts) != 0 {
		t.Fatalf("contexts = %+v", contexts)
	}
}

func TestActionRoundTripAndEnvelopeErrors(t *testing.T) {
	call := 0
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/contexts/ctx_1/actions" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		requireBearer(t, r)
		body := decodeJSONBody(t, r)
		if body["action"] != "goto" {
			t.Errorf("action = %v", body["action"])
		}
		args, _ := body["args"].(map[string]any)
		if args["url"] != "http://app.example.test/home" {
			t.Errorf("args = %v", body["args"])
		}
		call++
		switch call {
		case 1:
			_, _ = w.Write([]byte(`{"ok":true,"result":{"status":200},"artifacts":[{"kind":"screenshot","mime":"image/png","base64":"AAAA","sha256":"abc123","bytes":3,"filename":"shot.png"}]}`))
		case 2:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"target_not_found","message":"no target"}}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"internal","message":"boom"}}`))
		}
	})

	result, err := client.Action(context.Background(), "ctx_1", "goto", json.RawMessage(`{"url":"http://app.example.test/home"}`))
	if err != nil {
		t.Fatalf("Action: %v", err)
	}
	if !result.OK || string(result.Result) != `{"status":200}` || len(result.Artifacts) != 1 {
		t.Fatalf("ActionResult = %+v", result)
	}
	artifact := result.Artifacts[0]
	if artifact.Kind != "screenshot" || artifact.MIME != "image/png" || artifact.Base64 != "AAAA" ||
		artifact.SHA256 != "abc123" || artifact.Bytes != 3 || artifact.Filename != "shot.png" {
		t.Fatalf("Artifact = %+v", artifact)
	}

	result, err = client.Action(context.Background(), "ctx_1", "goto", json.RawMessage(`{"url":"http://app.example.test/home"}`))
	if err != nil {
		t.Fatalf("Action target_not_found should be an envelope, got %v", err)
	}
	if result.OK || result.Error == nil || result.Error.Code != "target_not_found" || result.Error.Message != "no target" {
		t.Fatalf("ActionResult = %+v", result)
	}

	if _, err := client.Action(context.Background(), "ctx_1", "goto", json.RawMessage(`{"url":"http://app.example.test/home"}`)); err == nil {
		t.Fatalf("expected a server failure to be returned as an error")
	} else if perr := asPlatformError(t, err); perr.Code != "executor_unavailable" {
		t.Fatalf("error = %+v", perr)
	}
}

func TestNon2xxReturnsTypedError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/contexts" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"boom"}}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"missing token"}}`))
	})

	_, err := client.ListContexts(context.Background())
	perr := asPlatformError(t, err)
	if perr.Status != http.StatusServiceUnavailable || perr.Code != "executor_unavailable" || !perr.Retryable {
		t.Fatalf("5xx error = %+v", perr)
	}
	if strings.Contains(perr.Message, testToken) {
		t.Fatalf("error message leaks the token: %q", perr.Message)
	}

	_, err = client.CreateContext(context.Background(), executor.CreateContextInput{AllowedHosts: []string{"127.0.0.1"}})
	perr = asPlatformError(t, err)
	if perr.Status != http.StatusUnauthorized || perr.Code != "unauthorized" || perr.Retryable {
		t.Fatalf("4xx error = %+v", perr)
	}
}

func TestCancelAndClose(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contexts/ctx_1/cancel":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"canceled":true}}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/contexts/ctx_1":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"closed":true}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"context_not_found","message":"unknown context"}}`))
		}
	})

	canceled, err := client.Cancel(context.Background(), "ctx_1")
	if err != nil || !canceled {
		t.Fatalf("Cancel = %v, %v", canceled, err)
	}
	if err := client.Close(context.Background(), "ctx_1"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	err = client.Close(context.Background(), "ctx_missing")
	perr := asPlatformError(t, err)
	if perr.Status != http.StatusNotFound || perr.Code != "context_not_found" {
		t.Fatalf("error = %+v", perr)
	}
}

// detailInt64 reads a numeric diagnostic detail regardless of the concrete
// integer width used when the map was built.
func detailInt64(t *testing.T, details map[string]any, key string) int64 {
	t.Helper()
	switch value := details[key].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	default:
		t.Fatalf("details[%q] = %#v, want a number (details: %#v)", key, details[key], details)
		return 0
	}
}

func requireDiagnosticDetails(t *testing.T, perr *platform.Error, endpoint, method, path string) {
	t.Helper()
	if perr.Details == nil {
		t.Fatalf("error has no details: %+v", perr)
	}
	details := perr.Details
	if got, _ := details["endpoint"].(string); got != endpoint {
		t.Fatalf("details.endpoint = %q, want %q (details: %#v)", got, endpoint, details)
	}
	if got, _ := details["method"].(string); got != method {
		t.Fatalf("details.method = %q, want %q", got, method)
	}
	if got, _ := details["path"].(string); got != path {
		t.Fatalf("details.path = %q, want %q", got, path)
	}
	if phase, _ := details["phase"].(string); phase == "" {
		t.Fatalf("details.phase is empty: %#v", details)
	}
	if requestID, _ := details["request_id"].(string); requestID == "" {
		t.Fatalf("details.request_id is empty: %#v", details)
	}
	if duration := detailInt64(t, details, "duration_ms"); duration < 0 {
		t.Fatalf("details.duration_ms = %d, want >= 0", duration)
	}
	if detailInt64(t, details, "timeout_ms") <= 0 {
		t.Fatalf("details.timeout_ms is missing: %#v", details)
	}
}

// TestContextCreateTimeoutIsTypedExecutorTimeout pins v0.2.3 X-01: a transport
// timeout is its own retryable executor_timeout code with structured diagnostics
// (endpoint/phase/request id/timeout) instead of a generic executor_unavailable.
func TestContextCreateTimeoutIsTypedExecutorTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write([]byte(`{"contextId":"ctx_1"}`))
	}))
	t.Cleanup(server.Close)
	// Deliberately shorter than the handler's sleep.
	client := executor.NewClientWithHTTPClient(server.URL, testToken, &http.Client{Timeout: 50 * time.Millisecond})

	_, err := client.CreateContext(context.Background(), executor.CreateContextInput{
		AllowedHosts: []string{"127.0.0.1"},
	})
	perr := asPlatformError(t, err)
	if perr.Code != "executor_timeout" || !perr.Retryable {
		t.Fatalf("error = %+v, want retryable executor_timeout", perr)
	}
	requireDiagnosticDetails(t, perr, server.URL, http.MethodPost, "/v1/contexts")
	if got := detailInt64(t, perr.Details, "timeout_ms"); got != 50 {
		t.Fatalf("details.timeout_ms = %d, want 50", got)
	}
	if phase, _ := perr.Details["phase"].(string); phase != "response" {
		t.Fatalf("details.phase = %q, want response for a header timeout", phase)
	}
	if strings.Contains(perr.Message, testToken) ||
		strings.Contains(perr.Details["endpoint"].(string), testToken) {
		t.Fatalf("diagnostics leak the token: %+v", perr)
	}
}

// TestContextDeadlineExceededIsTypedExecutorTimeout covers a caller-supplied
// deadline: it must be typed exactly like the per-client timeout.
func TestContextDeadlineExceededIsTypedExecutorTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	client := executor.NewClient(server.URL, testToken, 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := client.ListContexts(ctx)
	perr := asPlatformError(t, err)
	if perr.Code != "executor_timeout" {
		t.Fatalf("error = %+v, want executor_timeout", perr)
	}
	if phase, _ := perr.Details["phase"].(string); phase == "" {
		t.Fatalf("details.phase is empty: %#v", perr.Details)
	}
}

// TestCanceledRequestIsTypedExecutorTimeout keeps cancellations out of the
// generic executor_unavailable bucket (task contract: transport timeouts and
// cancellations share the executor_timeout code).
func TestCanceledRequestIsTypedExecutorTimeout(t *testing.T) {
	client := executor.NewClient("http://127.0.0.1:1", testToken, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.ListContexts(ctx)
	perr := asPlatformError(t, err)
	if perr.Code != "executor_timeout" {
		t.Fatalf("error = %+v, want executor_timeout", perr)
	}
}

// TestExecutorRequestsCarryRequestIDHeader proves every executor call carries
// the correlation header and that an HTTP failure reports the same id plus its
// response phase in structured details.
func TestExecutorRequestsCarryRequestIDHeader(t *testing.T) {
	var healthHeader, listHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-EzTest-Request-Id")
		if r.URL.Path == "/healthz" {
			healthHeader = requestID
			_, _ = w.Write([]byte(`{"status":"ok","version":"0.2.0-p3"}`))
			return
		}
		listHeader = requestID
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"boom"}}`))
	}))
	t.Cleanup(server.Close)
	client := executor.NewClient(server.URL, testToken, 5*time.Second)

	if _, err := client.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	_, err := client.ListContexts(context.Background())
	perr := asPlatformError(t, err)
	if perr.Code != "executor_unavailable" {
		t.Fatalf("error = %+v, want executor_unavailable", perr)
	}
	if healthHeader == "" || listHeader == "" {
		t.Fatalf("request ids were not sent: health=%q list=%q", healthHeader, listHeader)
	}
	if !strings.HasPrefix(listHeader, "req_") {
		t.Fatalf("request id = %q, want a req_ id", listHeader)
	}
	requireDiagnosticDetails(t, perr, server.URL, http.MethodGet, "/v1/contexts")
	if got := perr.Details["request_id"]; got != listHeader {
		t.Fatalf("details.request_id = %v, want the sent header %q", got, listHeader)
	}
	if phase, _ := perr.Details["phase"].(string); phase != "response" {
		t.Fatalf("details.phase = %q, want response", phase)
	}
	if status := detailInt64(t, perr.Details, "status_code"); status != http.StatusInternalServerError {
		t.Fatalf("details.status_code = %d, want 500", status)
	}
}

// TestExecutorTimeoutEnvelopeIsPreserved: a typed 504 executor_timeout reply is
// not flattened into executor_unavailable.
func TestExecutorTimeoutEnvelopeIsPreserved(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
		_, _ = w.Write([]byte(`{"error":{"code":"executor_timeout","message":"context creation exceeded its hard deadline"}}`))
	})

	_, err := client.CreateContext(context.Background(), executor.CreateContextInput{
		AllowedHosts: []string{"127.0.0.1"},
	})
	perr := asPlatformError(t, err)
	if perr.Code != "executor_timeout" || !perr.Retryable {
		t.Fatalf("error = %+v, want retryable executor_timeout", perr)
	}
	if !strings.Contains(perr.Message, "hard deadline") {
		t.Fatalf("message = %q, want the executor diagnostic", perr.Message)
	}
	if phase, _ := perr.Details["phase"].(string); phase != "response" {
		t.Fatalf("details.phase = %q, want response", phase)
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	var leaked int32
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/contexts" {
			http.Redirect(w, r, "/leaked", http.StatusFound)
			return
		}
		atomic.AddInt32(&leaked, 1)
		_, _ = w.Write([]byte(`{"contexts":[]}`))
	}
	server := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(server.Close)
	client := executor.NewClient(server.URL, testToken, time.Second)

	_, err := client.ListContexts(context.Background())
	if err == nil {
		t.Fatalf("a redirect response must not be followed")
	}
	if atomic.LoadInt32(&leaked) != 0 {
		t.Fatalf("client followed the redirect, which would leak the bearer token")
	}
}

func TestContextKey(t *testing.T) {
	if got, want := executor.ContextKey(42, 7, "admin"), "run:42:vs:7:role:admin"; got != want {
		t.Fatalf("ContextKey = %q, want %q", got, want)
	}
}
