package executor_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		_, _ = w.Write([]byte(`{"status":"ok","version":"2.0.0","sessions":2,"maxSessions":8,"draining":false,"reclaimedSessions":1}`))
	})

	health, err := client.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if health.Status != "ok" || health.Version != "2.0.0" || health.Sessions != 2 ||
		health.MaxSessions != 8 {
		t.Fatalf("Health = %+v", health)
	}
	if health.Draining || health.ReclaimedSessions != 1 {
		t.Fatalf("Health lifecycle fields = %+v", health)
	}
}

func TestHealthDegraded503ReturnsHealth(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"degraded","version":"2.0.0","sessions":0,"maxSessions":8,"draining":true,"error":"executor is draining"}`))
	})

	health, err := client.Health(context.Background())
	if err != nil {
		t.Fatalf("Health should parse a 503 degraded response, got %v", err)
	}
	if health.Status != "degraded" || !health.Draining || health.Error != "executor is draining" {
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

func TestCreateSessionSendsAuthWorkspaceAndLease(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/sessions" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		requireBearer(t, r)
		body := decodeJSONBody(t, r)
		if body["key"] != "run:42:sut:7:role:admin" {
			t.Errorf("key = %v", body["key"])
		}
		if body["workspace"] != "/data/workspaces/run-42/sut-7-admin" {
			t.Errorf("workspace = %v", body["workspace"])
		}
		if body["base_url"] != "http://app.example.test:13335" {
			t.Errorf("base_url = %v", body["base_url"])
		}
		metadata, _ := body["metadata"].(map[string]any)
		if metadata["account_role"] != "admin" || metadata["case_run_id"] != json.Number("42") {
			t.Errorf("metadata = %v", body["metadata"])
		}
		lease, _ := body["lease"].(map[string]any)
		if lease["lease_epoch"] != json.Number("3") || lease["ttl_ms"] != json.Number("300000") {
			t.Errorf("lease = %v", body["lease"])
		}
		// The v1 allow-list field is gone from the protocol (GD-01).
		if _, ok := body["allowedHosts"]; ok {
			t.Errorf("allowedHosts must not be sent: %v", body)
		}
		_, _ = w.Write([]byte(`{"sessionId":"sess_1","key":"run:42:sut:7:role:admin","workspace":"/data/workspaces/run-42/sut-7-admin","lease_epoch":3,"lease_ttl_ms":300000,"lease_expires_at":"2026-11-16T00:05:00Z"}`))
	})

	info, err := client.CreateSession(context.Background(), executor.CreateSessionInput{
		Key:       "run:42:sut:7:role:admin",
		Workspace: "/data/workspaces/run-42/sut-7-admin",
		BaseURL:   "http://app.example.test:13335",
		Metadata:  map[string]any{"case_run_id": 42, "account_role": "admin"},
		Lease:     &executor.Lease{Epoch: 3, TTL: 300 * time.Second},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if info.ID != "sess_1" || info.Key != "run:42:sut:7:role:admin" || info.LeaseEpoch != 3 || info.LeaseTTLMS != 300000 {
		t.Fatalf("SessionInfo = %+v", info)
	}
}

func TestCreateSessionOmitsOptionalFields(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		body := decodeJSONBody(t, r)
		for _, field := range []string{"metadata", "base_url", "key", "lease"} {
			if _, ok := body[field]; ok {
				t.Errorf("expected %q to be omitted, body = %v", field, body)
			}
		}
		if body["workspace"] != "/tmp/ws" {
			t.Errorf("workspace = %v", body["workspace"])
		}
		_, _ = w.Write([]byte(`{"sessionId":"sess_2"}`))
	})

	if _, err := client.CreateSession(context.Background(), executor.CreateSessionInput{
		Workspace: "/tmp/ws",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
}

func TestListSessions(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/sessions" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		requireBearer(t, r)
		_, _ = w.Write([]byte(`{"sessions":[{"sessionId":"sess_1","key":"run:42","metadata":{"case_run_id":"42"},"createdAt":"2026-11-01T00:00:00Z"}]}`))
	})

	sessions, err := client.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %+v", sessions)
	}
	got := sessions[0]
	if got.ID != "sess_1" || got.Key != "run:42" || got.Metadata["case_run_id"] != "42" ||
		got.CreatedAt != "2026-11-01T00:00:00Z" {
		t.Fatalf("SessionInfo = %+v", got)
	}
}

func TestListSessionsNullIsEmpty(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"sessions":null}`))
	})
	sessions, err := client.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("sessions = %+v", sessions)
	}
}

func TestGetSession(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/sessions/sess_1" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		requireBearer(t, r)
		_, _ = w.Write([]byte(`{"sessionId":"sess_1","commandCount":2,"artifactCount":3,"artifact_base":7}`))
	})
	info, err := client.GetSession(context.Background(), "sess_1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if info.CommandCount != 2 || info.ArtifactCount != 3 || info.ArtifactBase != 7 {
		t.Fatalf("SessionInfo = %+v", info)
	}
}

// TestExecSendsLeaseEpochAndParsesResult pins the fencing contract: every write
// carries lease_epoch, and the settled command result is decoded verbatim.
func TestExecSendsLeaseEpochAndParsesResult(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/sessions/sess_1/exec" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		requireBearer(t, r)
		body := decodeJSONBody(t, r)
		if body["lease_epoch"] != json.Number("9") {
			t.Errorf("lease_epoch = %v, want 9", body["lease_epoch"])
		}
		argv, _ := body["argv"].([]any)
		if len(argv) != 1 || argv[0] != "echo hello" {
			t.Errorf("argv = %v", body["argv"])
		}
		if body["shell"] != true {
			t.Errorf("shell = %v", body["shell"])
		}
		_, _ = w.Write([]byte(`{"command_id":"cmd_1","pid":4242,"exit":0,"outcome":"exited","duration":12,"stdout":"hello\n","stderr":"","stdout_bytes":6,"stdout_sha256":"abc","artifacts":[{"kind":"command","command_id":"cmd_1","filename":"command-cmd_1.json","base64":"e30="}]}`))
	})

	result, err := client.Exec(context.Background(), "sess_1", executor.ExecInput{
		Argv: []string{"echo hello"}, Shell: true, LeaseEpoch: 9,
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if result.CommandID != "cmd_1" || result.Exit == nil || *result.Exit != 0 || result.Stdout != "hello\n" {
		t.Fatalf("ExecResult = %+v", result)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].Kind != "command" {
		t.Fatalf("artifacts = %+v", result.Artifacts)
	}
}

func TestExecRejectsEmptyArgv(t *testing.T) {
	client := executor.NewClient("http://127.0.0.1:1", testToken, time.Second)
	if _, err := client.Exec(context.Background(), "sess_1", executor.ExecInput{}); err == nil {
		t.Fatal("an empty argv must be rejected before dialing")
	}
}

// TestExecTimeoutKeepsRecordedResult: a timed-out command still carries its
// recording in error.details so the settlement path can persist the evidence.
func TestExecTimeoutKeepsRecordedResult(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
		_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"executor_timeout","message":"command timed out","details":{"command_id":"cmd_slow","outcome":"timeout","timed_out":true,"stdout":"partial"}}}`))
	})
	result, err := client.Exec(context.Background(), "sess_1", executor.ExecInput{Argv: []string{"sleep"}})
	perr := asPlatformError(t, err)
	if perr.Code != "executor_timeout" || !perr.Retryable {
		t.Fatalf("error = %+v, want retryable executor_timeout", perr)
	}
	if result.CommandID != "cmd_slow" || !result.TimedOut || result.Stdout != "partial" {
		t.Fatalf("ExecResult = %+v, want the recorded result of the timed-out command", result)
	}
}

// TestLeaseFencingCodesAreTyped pins the four v2 conflict codes the API must be
// able to tell apart (they drive re-claim vs. abandonment).
func TestLeaseFencingCodesAreTyped(t *testing.T) {
	for _, code := range []string{
		executor.CodeLeaseEpochRequired,
		executor.CodeLeaseLost,
		executor.CodeLeaseExpired,
		executor.CodeSessionReclaiming,
	} {
		code := code
		t.Run(code, func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"` + code + `","message":"fenced"}}`))
			})
			_, err := client.Exec(context.Background(), "sess_1", executor.ExecInput{Argv: []string{"true"}, LeaseEpoch: 1})
			perr := asPlatformError(t, err)
			if perr.Status != http.StatusConflict || perr.Code != code {
				t.Fatalf("error = %+v, want 409 %s", perr, code)
			}
		})
	}
}

// TestSessionLimitIsRetryableWithRetryAfter: capacity backpressure is 429 with
// the executor's hint, not a hard conflict.
func TestSessionLimitIsRetryableWithRetryAfter(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"session_limit","message":"capacity reached","details":{"retry_after_seconds":42}}}`))
	})
	_, err := client.CreateSession(context.Background(), executor.CreateSessionInput{Workspace: "/tmp/ws"})
	perr := asPlatformError(t, err)
	if perr.Status != http.StatusTooManyRequests || perr.Code != executor.CodeSessionLimit || !perr.Retryable {
		t.Fatalf("error = %+v, want retryable 429 session_limit", perr)
	}
	if got := detailInt64(t, perr.Details, "retry_after_seconds"); got != 42 {
		t.Fatalf("retry_after_seconds = %d, want 42", got)
	}
}

// TestDrainingIsTypedBackpressure: a draining executor's 503 keeps its own code
// so the scheduler can pick another endpoint instead of reporting an outage.
func TestDrainingIsTypedBackpressure(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"draining","message":"executor is draining"}}`))
	})
	_, err := client.CreateSession(context.Background(), executor.CreateSessionInput{Workspace: "/tmp/ws"})
	perr := asPlatformError(t, err)
	if perr.Code != executor.CodeDraining || !perr.Retryable {
		t.Fatalf("error = %+v, want retryable draining", perr)
	}
}

// TestBrowserCliUnavailableKeepsCodeAndMessage: the executor's typed 503
// browser_cli_unavailable (the official playwright CLI runtime is not installed,
// executor/src/browser_cli.mjs) must keep its code AND its install guidance.
// Collapsing it into a generic executor_unavailable("executor returned HTTP 503")
// discarded both, leaving the operator without the cause and the X-02 capability
// attribution without its code token (final-review finding 2).
func TestBrowserCliUnavailableKeepsCodeAndMessage(t *testing.T) {
	message := "playwright-cli is not installed; install @playwright/cli@latest (npm install -g @playwright/cli@latest) or set EZTEST_PLAYWRIGHT_CLI_JS"
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"browser_cli_unavailable","message":"` + message + `"}}`))
	})
	_, err := client.Exec(context.Background(), "sess_1", executor.ExecInput{Argv: []string{"playwright-cli", "snapshot"}})
	perr := asPlatformError(t, err)
	if perr.Code != executor.CodeBrowserCliUnavailable || !perr.Retryable {
		t.Fatalf("error = %+v, want retryable browser_cli_unavailable", perr)
	}
	if perr.Message != message {
		t.Fatalf("message = %q, want the executor's install guidance preserved verbatim: %q", perr.Message, message)
	}
}

// TestArtifactsCursorAndGap pins the absolute cursor contract and the explicit
// gap signal (a client that fell behind the bounded buffer must be told).
func TestArtifactsCursorAndGap(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/sessions/sess_1/artifacts" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("since") != "5" {
			t.Errorf("since = %q, want 5", r.URL.Query().Get("since"))
		}
		requireBearer(t, r)
		_, _ = w.Write([]byte(`{"artifacts":[{"kind":"file","path":"out.txt","sha256":"aa","size":3,"mime":"text/plain","filename":"out.txt","base64":"YWJj"}],"next_cursor":9,"total":9,"base_cursor":7,"evicted":7,"gap":true,"missed":2}`))
	})
	page, err := client.Artifacts(context.Background(), "sess_1", 5)
	if err != nil {
		t.Fatalf("Artifacts: %v", err)
	}
	if page.NextCursor != 9 || page.BaseCursor != 7 || page.Evicted != 7 || !page.Gap || page.Missed != 2 {
		t.Fatalf("ArtifactPage = %+v", page)
	}
	if len(page.Artifacts) != 1 || page.Artifacts[0].Kind != "file" || page.Artifacts[0].Base64 != "YWJj" {
		t.Fatalf("artifacts = %+v", page.Artifacts)
	}
}

func TestArtifactsRejectsNegativeCursor(t *testing.T) {
	client := executor.NewClient("http://127.0.0.1:1", testToken, time.Second)
	if _, err := client.Artifacts(context.Background(), "sess_1", -1); err == nil {
		t.Fatal("a negative cursor must be rejected before dialing")
	}
}

func TestShellOperationsRouteAndFence(t *testing.T) {
	var paths []string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		paths = append(paths, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		if r.Method == http.MethodPost {
			body := decodeJSONBody(t, r)
			if body["lease_epoch"] != json.Number("4") {
				t.Errorf("lease_epoch = %v, want 4", body["lease_epoch"])
			}
		}
		_, _ = w.Write([]byte(`{"shell_id":"sh_1","pid":10,"status":"running","data":"out","next_cursor":3,"total_bytes":3}`))
	})
	ctx := context.Background()
	if _, err := client.Shell(ctx, "sess_1", executor.ShellRequest{Op: executor.ShellCreate, Argv: []string{"bash"}, LeaseEpoch: 4}); err != nil {
		t.Fatalf("Shell create: %v", err)
	}
	if _, err := client.Shell(ctx, "sess_1", executor.ShellRequest{Op: executor.ShellWrite, ShellID: "sh_1", Data: "ls\n", LeaseEpoch: 4}); err != nil {
		t.Fatalf("Shell write: %v", err)
	}
	read, err := client.Shell(ctx, "sess_1", executor.ShellRequest{Op: executor.ShellRead, ShellID: "sh_1", Since: 2})
	if err != nil {
		t.Fatalf("Shell read: %v", err)
	}
	if read.Data != "out" || read.NextCursor != 3 {
		t.Fatalf("ShellResult = %+v", read)
	}
	if _, err := client.Shell(ctx, "sess_1", executor.ShellRequest{Op: executor.ShellWait, ShellID: "sh_1", TimeoutMS: 1000, LeaseEpoch: 4}); err != nil {
		t.Fatalf("Shell wait: %v", err)
	}
	if _, err := client.Shell(ctx, "sess_1", executor.ShellRequest{Op: executor.ShellClose, ShellID: "sh_1", LeaseEpoch: 4}); err != nil {
		t.Fatalf("Shell close: %v", err)
	}
	want := []string{
		"POST /v1/sessions/sess_1/shell?",
		"POST /v1/sessions/sess_1/shell/sh_1/write?",
		"GET /v1/sessions/sess_1/shell/sh_1/read?since=2",
		"POST /v1/sessions/sess_1/shell/sh_1/wait?",
		"POST /v1/sessions/sess_1/shell/sh_1/close?",
	}
	if strings.Join(paths, "|") != strings.Join(want, "|") {
		t.Fatalf("shell routes = %v, want %v", paths, want)
	}
	if _, err := client.Shell(ctx, "sess_1", executor.ShellRequest{Op: "teleport"}); err == nil {
		t.Fatal("an unknown shell op must be rejected")
	}
}

func TestFilesOperationsRouteAndFence(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		switch r.Method {
		case http.MethodPost:
			body := decodeJSONBody(t, r)
			if body["path"] != "in.txt" || body["base64"] != "YWJj" || body["lease_epoch"] != json.Number("6") {
				t.Errorf("upload body = %v", body)
			}
			_, _ = w.Write([]byte(`{"path":"in.txt","bytes":3,"sha256":"aa"}`))
		default:
			if r.URL.Query().Get("path") != "in.txt" {
				t.Errorf("path = %q", r.URL.Query().Get("path"))
			}
			_, _ = w.Write([]byte(`{"path":"in.txt","bytes":3,"sha256":"aa","mime":"text/plain","base64":"YWJj"}`))
		}
	})
	ctx := context.Background()
	if _, err := client.Files(ctx, "sess_1", executor.FileRequest{Op: executor.FileUpload, Path: "in.txt", Base64: "YWJj", LeaseEpoch: 6}); err != nil {
		t.Fatalf("Files upload: %v", err)
	}
	downloaded, err := client.Files(ctx, "sess_1", executor.FileRequest{Op: executor.FileDownload, Path: "in.txt"})
	if err != nil {
		t.Fatalf("Files download: %v", err)
	}
	if downloaded.Base64 != "YWJj" || downloaded.SHA256 != "aa" {
		t.Fatalf("FileResult = %+v", downloaded)
	}
	if _, err := client.Files(ctx, "sess_1", executor.FileRequest{Op: "teleport"}); err == nil {
		t.Fatal("an unknown files op must be rejected")
	}
}

func TestTraceAndRenewLease(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		switch r.URL.Path {
		case "/v1/sessions/sess_1/trace":
			_, _ = w.Write([]byte(`{"available":true,"path":"trace.zip","bytes":10,"sha256":"bb","mime":"application/zip"}`))
		case "/v1/sessions/sess_1/lease":
			body := decodeJSONBody(t, r)
			if body["lease_epoch"] != json.Number("11") {
				t.Errorf("lease_epoch = %v, want 11", body["lease_epoch"])
			}
			_, _ = w.Write([]byte(`{"sessionId":"sess_1","lease_epoch":11,"lease_expires_at":"2026-11-16T00:05:00Z","lease_ttl_ms":300000}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	ref, err := client.Trace(context.Background(), "sess_1")
	if err != nil {
		t.Fatalf("Trace: %v", err)
	}
	if !ref.Available || ref.Path != "trace.zip" || ref.SHA256 != "bb" {
		t.Fatalf("TraceRef = %+v", ref)
	}
	lease, err := client.RenewLease(context.Background(), "sess_1", 11)
	if err != nil {
		t.Fatalf("RenewLease: %v", err)
	}
	if lease.SessionID != "sess_1" || lease.LeaseEpoch != 11 {
		t.Fatalf("LeaseInfo = %+v", lease)
	}
}

func TestNetworkLogRoundTripAndTypedErrors(t *testing.T) {
	call := 0
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/sessions/sess_1/network" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("tail") != "200" {
			t.Errorf("tail = %q, want 200", r.URL.Query().Get("tail"))
		}
		requireBearer(t, r)
		call++
		switch call {
		case 1:
			_, _ = w.Write([]byte(`{"recording":true,"degraded":false,"items":[{"method":"GET","url":"http://app.example.test/home","status":200,"resource_type":"document"}]}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"internal","message":"boom"}}`))
		}
	})

	payload, err := client.NetworkLog(context.Background(), "sess_1", 200)
	if err != nil {
		t.Fatalf("NetworkLog: %v", err)
	}
	if !strings.Contains(string(payload), `"resource_type":"document"`) || !strings.Contains(string(payload), `"recording":true`) {
		t.Fatalf("NetworkLog payload = %s", payload)
	}

	if _, err := client.NetworkLog(context.Background(), "sess_1", 200); err == nil {
		t.Fatalf("expected a server failure to be returned as an error")
	} else if perr := asPlatformError(t, err); perr.Code != "executor_unavailable" {
		t.Fatalf("error = %+v", perr)
	}
}

func TestNon2xxReturnsTypedError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sessions" {
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

	_, err := client.ListSessions(context.Background())
	perr := asPlatformError(t, err)
	if perr.Status != http.StatusServiceUnavailable || perr.Code != "executor_unavailable" || !perr.Retryable {
		t.Fatalf("5xx error = %+v", perr)
	}
	if strings.Contains(perr.Message, testToken) {
		t.Fatalf("error message leaks the token: %q", perr.Message)
	}

	_, err = client.CreateSession(context.Background(), executor.CreateSessionInput{Workspace: "/tmp/ws"})
	perr = asPlatformError(t, err)
	if perr.Status != http.StatusUnauthorized || perr.Code != "unauthorized" || perr.Retryable {
		t.Fatalf("4xx error = %+v", perr)
	}
}

func TestCancelAndCloseSession(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions/sess_1/cancel":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"cancelled":["cmd_1"]}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions/sess_idle/cancel":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"cancelled":[]}}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/sessions/sess_1":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"sessionId":"sess_1"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"session_not_found","message":"unknown session"}}`))
		}
	})

	cancelled, err := client.Cancel(context.Background(), "sess_1")
	if err != nil || !cancelled {
		t.Fatalf("Cancel = %v, %v", cancelled, err)
	}
	idle, err := client.Cancel(context.Background(), "sess_idle")
	if err != nil || idle {
		t.Fatalf("Cancel of an idle session = %v, %v, want false", idle, err)
	}
	if err := client.CloseSession(context.Background(), "sess_1"); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	err = client.CloseSession(context.Background(), "sess_missing")
	perr := asPlatformError(t, err)
	if perr.Status != http.StatusNotFound || perr.Code != "session_not_found" {
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

// TestSessionCreateTimeoutIsTypedExecutorTimeout pins v0.2.3 X-01: a transport
// timeout is its own retryable executor_timeout code with structured diagnostics
// (endpoint/phase/request id/timeout) instead of a generic executor_unavailable.
func TestSessionCreateTimeoutIsTypedExecutorTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write([]byte(`{"sessionId":"sess_1"}`))
	}))
	t.Cleanup(server.Close)
	// Deliberately shorter than the handler's sleep.
	client := executor.NewClientWithHTTPClient(server.URL, testToken, &http.Client{Timeout: 50 * time.Millisecond})

	_, err := client.CreateSession(context.Background(), executor.CreateSessionInput{Workspace: "/tmp/ws"})
	perr := asPlatformError(t, err)
	if perr.Code != "executor_timeout" || !perr.Retryable {
		t.Fatalf("error = %+v, want retryable executor_timeout", perr)
	}
	requireDiagnosticDetails(t, perr, server.URL, http.MethodPost, "/v1/sessions")
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

	_, err := client.ListSessions(ctx)
	perr := asPlatformError(t, err)
	if perr.Code != "executor_timeout" {
		t.Fatalf("error = %+v, want executor_timeout", perr)
	}
	if phase, _ := perr.Details["phase"].(string); phase == "" {
		t.Fatalf("details.phase is empty: %#v", perr.Details)
	}
}

// TestCanceledRequestIsTypedExecutorTimeout keeps cancellations out of the
// generic executor_unavailable bucket (transport timeouts and cancellations
// share the executor_timeout code).
func TestCanceledRequestIsTypedExecutorTimeout(t *testing.T) {
	client := executor.NewClient("http://127.0.0.1:1", testToken, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.ListSessions(ctx)
	perr := asPlatformError(t, err)
	if perr.Code != "executor_timeout" {
		t.Fatalf("error = %+v, want executor_timeout", perr)
	}
}

// TestExecRaisesTheRequestBound: the command channel must not inherit the 75s
// control-plane bound, or a legitimate long command would be aborted client-side.
func TestExecRaisesTheRequestBound(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"command_id":"cmd_1","exit":0,"outcome":"exited"}`))
	})
	if _, err := client.Exec(context.Background(), "sess_1", executor.ExecInput{Argv: []string{"true"}}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	// A caller-supplied shorter bound still wins and is reported in diagnostics.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{"command_id":"cmd_1"}`))
	}))
	t.Cleanup(server.Close)
	bounded := executor.NewClient(server.URL, testToken, 5*time.Second)
	ctx, cancel := context.WithTimeout(executor.WithRequestTimeout(context.Background(), 40*time.Millisecond), time.Second)
	defer cancel()
	_, err := bounded.Exec(ctx, "sess_1", executor.ExecInput{Argv: []string{"sleep"}})
	perr := asPlatformError(t, err)
	if perr.Code != "executor_timeout" {
		t.Fatalf("error = %+v, want executor_timeout", perr)
	}
	if got := detailInt64(t, perr.Details, "timeout_ms"); got > 60 {
		t.Fatalf("details.timeout_ms = %d, want the explicit 40ms override to win", got)
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
			_, _ = w.Write([]byte(`{"status":"ok","version":"2.0.0"}`))
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
	_, err := client.ListSessions(context.Background())
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
	requireDiagnosticDetails(t, perr, server.URL, http.MethodGet, "/v1/sessions")
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
		_, _ = w.Write([]byte(`{"error":{"code":"executor_timeout","message":"session creation exceeded its hard deadline"}}`))
	})

	_, err := client.CreateSession(context.Background(), executor.CreateSessionInput{Workspace: "/tmp/ws"})
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
		if r.URL.Path == "/v1/sessions" {
			http.Redirect(w, r, "/leaked", http.StatusFound)
			return
		}
		atomic.AddInt32(&leaked, 1)
		_, _ = w.Write([]byte(`{"sessions":[]}`))
	}
	server := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(server.Close)
	client := executor.NewClient(server.URL, testToken, time.Second)

	_, err := client.ListSessions(context.Background())
	if err == nil {
		t.Fatalf("a redirect response must not be followed")
	}
	if atomic.LoadInt32(&leaked) != 0 {
		t.Fatalf("client followed the redirect, which would leak the bearer token")
	}
}

// TestSlowBodyReadTimeoutIsTypedExecutorTimeout pins the review finding that a
// deadline also bounds reading the response body: a body-read timeout must
// surface as executor_timeout with the body phase instead of a generic
// executor_unavailable. Locally detected timeouts use the same 504 status as the
// executor's own envelope.
func TestSlowBodyReadTimeoutIsTypedExecutorTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		// Send the headers and a partial body, then stall well past the client
		// timeout so io.ReadAll fails while reading the body.
		_, _ = w.Write([]byte(`{"sessionId":"sess_slow"`))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	client := executor.NewClientWithHTTPClient(server.URL, testToken, &http.Client{Timeout: 80 * time.Millisecond})

	_, err := client.CreateSession(context.Background(), executor.CreateSessionInput{Workspace: "/tmp/ws"})
	perr := asPlatformError(t, err)
	if perr.Code != "executor_timeout" || !perr.Retryable {
		t.Fatalf("error = %+v, want retryable executor_timeout for a slow body read", perr)
	}
	if perr.Status != http.StatusGatewayTimeout {
		t.Fatalf("error status = %d, want 504 (executor_timeout is always 504)", perr.Status)
	}
	requireDiagnosticDetails(t, perr, server.URL, http.MethodPost, "/v1/sessions")
	if phase, _ := perr.Details["phase"].(string); phase != "body" {
		t.Fatalf("details.phase = %q, want body", phase)
	}
	if got := detailInt64(t, perr.Details, "timeout_ms"); got != 80 {
		t.Fatalf("details.timeout_ms = %d, want 80", got)
	}
	if status := detailInt64(t, perr.Details, "status_code"); status != http.StatusOK {
		t.Fatalf("details.status_code = %d, want 200 (the headers were received)", status)
	}
}

// Test5xxTypedEnvelopeStaysExecutorUnavailable pins the documented contract: a
// 5xx maps to executor_unavailable unless it is an explicit executor_timeout or
// the operational draining signal, so an arbitrary executor code cannot leak
// into the API's failure taxonomy.
func Test5xxTypedEnvelopeStaysExecutorUnavailable(t *testing.T) {
	for _, status := range []int{
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
	} {
		status := status
		for _, code := range []string{"canceled", "page_not_found", "internal", ""} {
			code := code
			t.Run(fmt.Sprintf("status_%d_code_%q", status, code), func(t *testing.T) {
				client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"` + code + `","message":"boom"}}`))
				})
				_, err := client.ListSessions(context.Background())
				perr := asPlatformError(t, err)
				if perr.Code != "executor_unavailable" || !perr.Retryable {
					t.Fatalf("error = %+v, want retryable executor_unavailable for a %d %q envelope", perr, status, code)
				}
				if code != "" && perr.Code == code {
					t.Fatalf("executor code %q leaked into the platform error", code)
				}
				if got := detailInt64(t, perr.Details, "status_code"); got != int64(status) {
					t.Fatalf("details.status_code = %d, want %d", got, status)
				}
			})
		}
	}
}

// TestNetworkLog5xxTypedEnvelopeStaysExecutorUnavailable covers the traffic read
// endpoint: its 5xx path must apply the same taxonomy instead of forwarding the
// envelope code.
func TestNetworkLog5xxTypedEnvelopeStaysExecutorUnavailable(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"canceled","message":"boom"}}`))
	})
	_, err := client.NetworkLog(context.Background(), "sess_1", 0)
	perr := asPlatformError(t, err)
	if perr.Code != "executor_unavailable" || !perr.Retryable {
		t.Fatalf("error = %+v, want retryable executor_unavailable", perr)
	}
	if phase, _ := perr.Details["phase"].(string); phase != "response" {
		t.Fatalf("details.phase = %q, want response", phase)
	}
}

// TestSessionKey pins the executor session key format.
//
// This literal is duplicated in application/case_run.go
// executorSessionKeyFormat and the two MUST stay identical: orphan
// reconciliation matches executor sessions by this exact string, so a drift
// silently stops reclaiming sessions. TestRecoveryCleansStaleAndOrphanContexts
// is the cross-package pin (it feeds a `sut:` key to the application-side
// matcher and asserts the session is recognised, not deleted as an orphan).
func TestSessionKey(t *testing.T) {
	if got, want := executor.SessionKey(42, 7, "admin"), "run:42:sut:7:role:admin"; got != want {
		t.Fatalf("SessionKey = %q, want %q", got, want)
	}
	if strings.Contains(executor.SessionKey(42, 7, "admin"), ":vs:") {
		t.Fatal("session key still carries the pre-v0.2.4 `vs:` label")
	}
}
