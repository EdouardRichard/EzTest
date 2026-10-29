package browser_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/browser"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/testutil"
)

const testToken = "tok"

func newTestClient(server *httptest.Server) *browser.Client {
	return browser.New(server.URL, testToken, time.Second)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func authorized(r *http.Request) bool {
	return r.Header.Get("Authorization") == "Bearer "+testToken
}

func assertBrowserErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var browserErr *application.BrowserError
	if !errors.As(err, &browserErr) {
		t.Fatalf("error = %T %v, want *application.BrowserError", err, err)
	}
	if browserErr.Code != code {
		t.Fatalf("error code = %q (%s), want %q", browserErr.Code, browserErr.Message, code)
	}
}

func jsonEqual(a, b []byte) bool {
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}

func TestHealthParsesVersionAndCapacity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/healthz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if !authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":      "ok",
			"version":     "1.2.3",
			"contexts":    2,
			"maxContexts": 8,
		})
	}))
	defer server.Close()

	if err := newTestClient(server).Health(context.Background()); err != nil {
		t.Fatalf("Health() error = %v", err)
	}
}

func TestHealthRejectsMalformedPayload(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"not json", "not-json"},
		{"missing status", `{"version":"1.2.3","contexts":0,"maxContexts":8}`},
		{"missing version", `{"status":"ok","contexts":0,"maxContexts":8}`},
		{"missing capacity", `{"status":"ok","version":"1.2.3","contexts":0}`},
		{"not ok", `{"status":"degraded","version":"1.2.3","contexts":0,"maxContexts":8}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			err := newTestClient(server).Health(context.Background())
			assertBrowserErrorCode(t, err, "internal")
		})
	}
}

func TestNewContextSendsSpec(t *testing.T) {
	spec := browser.ContextSpec{
		Key:          "case_run:42",
		AllowedHosts: []string{"127.0.0.1", "localhost"},
		BaseURL:      "http://127.0.0.1:13335",
		StorageState: json.RawMessage(`{"cookies":[{"name":"sid","value":"v"}]}`),
		SessionStorage: map[string]json.RawMessage{
			"http://127.0.0.1:13335": json.RawMessage(`[{"name":"k","value":"v"}]`),
		},
		Viewport: browser.Viewport{Width: 1440, Height: 900},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/contexts" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if !authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body struct {
			Key            string                     `json:"key"`
			AllowedHosts   []string                   `json:"allowedHosts"`
			BaseURL        string                     `json:"baseUrl"`
			StorageState   json.RawMessage            `json:"storageState"`
			SessionStorage map[string]json.RawMessage `json:"sessionStorage"`
			Viewport       struct {
				Width  int `json:"width"`
				Height int `json:"height"`
			} `json:"viewport"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode context request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if body.Key != spec.Key {
			t.Errorf("key = %q, want %q", body.Key, spec.Key)
		}
		if !reflect.DeepEqual(body.AllowedHosts, spec.AllowedHosts) {
			t.Errorf("allowedHosts = %#v, want %#v", body.AllowedHosts, spec.AllowedHosts)
		}
		if body.BaseURL != spec.BaseURL {
			t.Errorf("baseUrl = %q, want %q", body.BaseURL, spec.BaseURL)
		}
		if !jsonEqual(body.StorageState, spec.StorageState) {
			t.Errorf("storageState = %s, want %s", body.StorageState, spec.StorageState)
		}
		if len(body.SessionStorage) != 1 || !jsonEqual(body.SessionStorage["http://127.0.0.1:13335"], spec.SessionStorage["http://127.0.0.1:13335"]) {
			t.Errorf("sessionStorage = %s", body.SessionStorage)
		}
		if body.Viewport.Width != spec.Viewport.Width || body.Viewport.Height != spec.Viewport.Height {
			t.Errorf("viewport = %dx%d, want %dx%d", body.Viewport.Width, body.Viewport.Height, spec.Viewport.Width, spec.Viewport.Height)
		}
		writeJSON(w, http.StatusOK, map[string]any{"contextId": "ctx_7", "browserVersion": "999.0.0"})
	}))
	defer server.Close()

	ref, err := newTestClient(server).NewContext(context.Background(), spec)
	if err != nil {
		t.Fatalf("NewContext() error = %v", err)
	}
	if ref.ID != "ctx_7" {
		t.Fatalf("context ref = %q, want ctx_7", ref.ID)
	}
}

func TestNewContextMapsProtocolError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"ok":    false,
			"error": map[string]any{"code": "context_limit", "message": "too many contexts"},
		})
	}))
	defer server.Close()

	_, err := newTestClient(server).NewContext(context.Background(), browser.ContextSpec{Key: "case_run:1"})
	assertBrowserErrorCode(t, err, "context_limit")
}

type artifactBody struct {
	Kind     string `json:"kind"`
	Mime     string `json:"mime"`
	Filename string `json:"filename,omitempty"`
	Base64   string `json:"base64"`
	SHA256   string `json:"sha256"`
	Bytes    int    `json:"bytes,omitempty"`
}

func TestActionVerifiesArtifactSHA256(t *testing.T) {
	data := []byte("fake-png-bytes")
	digest := sha256.Sum256(data)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/contexts/ctx_1/actions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if !authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body struct {
			Action string          `json:"action"`
			Args   json.RawMessage `json:"args"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode action request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if body.Action != "screenshot" {
			t.Errorf("action = %q, want screenshot", body.Action)
		}
		if !jsonEqual(body.Args, json.RawMessage(`{"full_page":true}`)) {
			t.Errorf("args = %s", body.Args)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"result": map[string]any{"bytes": len(data)},
			"artifacts": []artifactBody{{
				Kind:     "screenshot",
				Mime:     "image/png",
				Filename: "shot.png",
				Base64:   base64.StdEncoding.EncodeToString(data),
				SHA256:   hex.EncodeToString(digest[:]),
				Bytes:    len(data),
			}},
		})
	}))
	defer server.Close()

	result, err := newTestClient(server).Action(context.Background(), browser.ContextRef{ID: "ctx_1"}, browser.Action{
		Name: "screenshot",
		Args: json.RawMessage(`{"full_page":true}`),
	})
	if err != nil {
		t.Fatalf("Action() error = %v", err)
	}
	if result.ErrorCode != "" {
		t.Fatalf("ErrorCode = %q, want empty", result.ErrorCode)
	}
	var payload struct {
		Bytes int `json:"bytes"`
	}
	if err := json.Unmarshal(result.Result, &payload); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if payload.Bytes != len(data) {
		t.Fatalf("result bytes = %d, want %d", payload.Bytes, len(data))
	}
	if len(result.Artifacts) != 1 {
		t.Fatalf("artifacts = %d, want 1", len(result.Artifacts))
	}
	artifact := result.Artifacts[0]
	if artifact.Kind != "screenshot" || artifact.Mime != "image/png" || artifact.Filename != "shot.png" {
		t.Fatalf("artifact metadata = %+v", artifact)
	}
	if !reflect.DeepEqual(artifact.Data, data) {
		t.Fatalf("artifact data = %q, want %q", artifact.Data, data)
	}
	if artifact.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("artifact sha256 = %q", artifact.SHA256)
	}
}

func TestActionRejectsBadArtifacts(t *testing.T) {
	data := []byte("fake-png-bytes")
	digest := sha256.Sum256(data)
	validBase64 := base64.StdEncoding.EncodeToString(data)
	tests := []struct {
		name     string
		artifact artifactBody
	}{
		{
			name: "sha256 mismatch",
			artifact: artifactBody{
				Kind: "screenshot", Mime: "image/png",
				Base64: validBase64, SHA256: strings.Repeat("0", 64),
			},
		},
		{
			name: "invalid base64",
			artifact: artifactBody{
				Kind: "screenshot", Mime: "image/png",
				Base64: "%%%not-base64%%%", SHA256: hex.EncodeToString(digest[:]),
			},
		},
		{
			name: "missing sha256",
			artifact: artifactBody{
				Kind: "screenshot", Mime: "image/png", Base64: validBase64,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, http.StatusOK, map[string]any{
					"ok":        true,
					"result":    map[string]any{"ok": true},
					"artifacts": []artifactBody{tc.artifact},
				})
			}))
			defer server.Close()

			result, err := newTestClient(server).Action(context.Background(), browser.ContextRef{ID: "ctx_1"}, browser.Action{Name: "screenshot"})
			assertBrowserErrorCode(t, err, "internal")
			if len(result.Artifacts) != 0 {
				t.Fatalf("artifacts returned on verification failure: %d", len(result.Artifacts))
			}
		})
	}
}

func TestActionSurfacesTypedError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": map[string]any{"code": "target_not_found", "message": "none"},
		})
	}))
	defer server.Close()

	result, err := newTestClient(server).Action(context.Background(), browser.ContextRef{ID: "ctx_1"}, browser.Action{Name: "click"})
	if err != nil {
		t.Fatalf("Action() error = %v, want nil action error", err)
	}
	if result.ErrorCode != "target_not_found" {
		t.Fatalf("ErrorCode = %q, want target_not_found", result.ErrorCode)
	}
	if result.ErrorMessage != "none" {
		t.Fatalf("ErrorMessage = %q, want none", result.ErrorMessage)
	}
}

func TestActionMapsProtocolErrorOnNon2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": map[string]any{"code": "invalid_arguments", "message": "bad args"},
		})
	}))
	defer server.Close()

	result, err := newTestClient(server).Action(context.Background(), browser.ContextRef{ID: "ctx_1"}, browser.Action{Name: "teleport"})
	if err != nil {
		t.Fatalf("Action() error = %v, want nil action error", err)
	}
	if result.ErrorCode != "invalid_arguments" {
		t.Fatalf("ErrorCode = %q, want invalid_arguments", result.ErrorCode)
	}
}

func TestActionUnauthorizedIsTypedError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"ok":    false,
			"error": map[string]any{"code": "unauthorized", "message": "bad token"},
		})
	}))
	defer server.Close()

	_, err := newTestClient(server).Action(context.Background(), browser.ContextRef{ID: "ctx_1"}, browser.Action{Name: "click"})
	assertBrowserErrorCode(t, err, "unauthorized")
}

func TestActionIsNotRetriedOnServerError(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer server.Close()

	_, err := newTestClient(server).Action(context.Background(), browser.ContextRef{ID: "ctx_1"}, browser.Action{Name: "click"})
	assertBrowserErrorCode(t, err, "internal")
	if got := requests.Load(); got != 1 {
		t.Fatalf("action requests = %d, want exactly 1 (non-idempotent actions must not be retried)", got)
	}
}

func TestActionTimeout(t *testing.T) {
	run := func(t *testing.T, clientTimeout time.Duration, useContextDeadline bool) {
		t.Helper()
		release := make(chan struct{})
		started := make(chan struct{})
		var once sync.Once
		var requests atomic.Int32
		var cancels atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/cancel") {
				cancels.Add(1)
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": map[string]any{"canceled": true}})
				return
			}
			requests.Add(1)
			once.Do(func() { close(started) })
			<-release
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()
		defer close(release)

		client := browser.New(server.URL, testToken, clientTimeout)
		ctx := context.Background()
		if useContextDeadline {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, 30*time.Millisecond)
			defer cancel()
		}
		_, err := client.Action(ctx, browser.ContextRef{ID: "ctx_1"}, browser.Action{Name: "click"})
		// A timeout of the executor call itself stays distinguishable from an
		// action-level timeout: it is the model-classified executor_timeout of
		// design spec §10.5, and the abandoned executor context is canceled.
		assertBrowserErrorCode(t, err, "executor_timeout")
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("executor handler never received the action request")
		}
		if got := requests.Load(); got != 1 {
			t.Fatalf("action requests = %d, want exactly 1", got)
		}
		if got := cancels.Load(); got != 1 {
			t.Fatalf("executor cancel requests = %d, want exactly 1 (the timed-out context must be canceled)", got)
		}
	}

	t.Run("configured default timeout", func(t *testing.T) {
		run(t, 30*time.Millisecond, false)
	})
	t.Run("caller context deadline", func(t *testing.T) {
		run(t, 5*time.Second, true)
	})
}

func TestCancelAndClose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contexts/ctx_1/cancel":
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": map[string]any{"canceled": true}})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/contexts/ctx_1":
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": map[string]any{"closed": true}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := newTestClient(server)
	ref := browser.ContextRef{ID: "ctx_1"}
	if err := client.Cancel(context.Background(), ref); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if err := client.Close(context.Background(), ref); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestCancelMapsProtocolError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"ok":    false,
			"error": map[string]any{"code": "context_not_found", "message": "unknown context"},
		})
	}))
	defer server.Close()

	err := newTestClient(server).Cancel(context.Background(), browser.ContextRef{ID: "ctx_missing"})
	assertBrowserErrorCode(t, err, "context_not_found")
}

func TestRequestsCarryBearerToken(t *testing.T) {
	const token = "secret-token"
	var mu sync.Mutex
	headers := make([]string, 0, 5)
	record := func(r *http.Request) {
		mu.Lock()
		headers = append(headers, r.Header.Get("Authorization"))
		mu.Unlock()
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/healthz":
			record(r)
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": "1.2.3", "contexts": 0, "maxContexts": 8})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contexts":
			record(r)
			writeJSON(w, http.StatusOK, map[string]any{"contextId": "ctx_1", "browserVersion": "999.0.0"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contexts/ctx_1/actions":
			record(r)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": map[string]any{"ok": true}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contexts/ctx_1/cancel":
			record(r)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": map[string]any{"canceled": true}})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/contexts/ctx_1":
			record(r)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": map[string]any{"closed": true}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := browser.New(server.URL, token, time.Second)
	ctx := context.Background()
	if err := client.Health(ctx); err != nil {
		t.Fatalf("Health() error = %v", err)
	}
	ref, err := client.NewContext(ctx, browser.ContextSpec{Key: "case_run:1"})
	if err != nil {
		t.Fatalf("NewContext() error = %v", err)
	}
	if _, err := client.Action(ctx, ref, browser.Action{Name: "snapshot"}); err != nil {
		t.Fatalf("Action() error = %v", err)
	}
	if err := client.Cancel(ctx, ref); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if err := client.Close(ctx, ref); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(headers) != 5 {
		t.Fatalf("recorded %d requests, want 5", len(headers))
	}
	for index, header := range headers {
		if header != "Bearer "+token {
			t.Fatalf("request %d Authorization = %q, want bearer token", index, header)
		}
	}
}

func TestFakeBrowserExecutorScriptsAndRecords(t *testing.T) {
	fake := testutil.NewFakeBrowserExecutor()
	ctx := context.Background()
	if err := fake.Health(ctx); err != nil {
		t.Fatalf("fake Health() error = %v", err)
	}
	spec := browser.ContextSpec{
		Key:          "case_run:1",
		AllowedHosts: []string{"127.0.0.1"},
		Viewport:     browser.Viewport{Width: 1280, Height: 720},
	}
	ref, err := fake.NewContext(ctx, spec)
	if err != nil {
		t.Fatalf("fake NewContext() error = %v", err)
	}
	want := browser.ActionResult{
		Result: json.RawMessage(`{"ok":true}`),
		Artifacts: []browser.Artifact{{
			Kind: "screenshot", Mime: "image/png", Filename: "shot.png",
			Data: []byte("png"), SHA256: "abc",
		}},
	}
	fake.ScriptActionFor(ref, want)
	got, err := fake.Action(ctx, ref, browser.Action{Name: "screenshot", Args: json.RawMessage(`{"full_page":true}`)})
	if err != nil {
		t.Fatalf("fake Action() error = %v", err)
	}
	if string(got.Result) != string(want.Result) || len(got.Artifacts) != 1 || got.Artifacts[0].Filename != "shot.png" {
		t.Fatalf("scripted result = %+v, want %+v", got, want)
	}

	fake.ScriptActionErrorFor(ref, &application.BrowserError{Code: "target_not_found", Message: "none"})
	if _, err := fake.Action(ctx, ref, browser.Action{Name: "click"}); err != nil {
		assertBrowserErrorCode(t, err, "target_not_found")
	} else {
		t.Fatal("scripted action error was not returned")
	}

	calls := fake.Calls()
	wantKinds := []testutil.FakeCallKind{
		testutil.FakeCallHealth,
		testutil.FakeCallNewContext,
		testutil.FakeCallAction,
		testutil.FakeCallAction,
	}
	if len(calls) != len(wantKinds) {
		t.Fatalf("recorded %d calls, want %d", len(calls), len(wantKinds))
	}
	for index, kind := range wantKinds {
		if calls[index].Kind != kind {
			t.Fatalf("call %d kind = %q, want %q", index, calls[index].Kind, kind)
		}
	}
	if calls[1].Spec.Key != spec.Key {
		t.Fatalf("recorded context spec = %+v", calls[1].Spec)
	}
	if calls[2].Action.Name != "screenshot" {
		t.Fatalf("recorded action = %+v", calls[2].Action)
	}
	if recorded, ok := fake.Contexts()[ref.ID]; !ok || recorded.Key != spec.Key {
		t.Fatalf("recorded contexts = %+v", fake.Contexts())
	}
}

func TestFakeBrowserExecutorInjectedErrors(t *testing.T) {
	fake := testutil.NewFakeBrowserExecutor()
	ctx := context.Background()
	ref := browser.ContextRef{ID: "ctx_1"}

	fake.SetHealthError(&application.BrowserError{Code: "executor_unavailable", Message: "down"})
	assertBrowserErrorCode(t, fake.Health(ctx), "executor_unavailable")

	fake.SetNewContextError(&application.BrowserError{Code: "context_limit", Message: "full"})
	if _, err := fake.NewContext(ctx, browser.ContextSpec{Key: "x"}); err != nil {
		assertBrowserErrorCode(t, err, "context_limit")
	} else {
		t.Fatal("injected NewContext error was not returned")
	}

	fake.SetActionError(&application.BrowserError{Code: "internal", Message: "boom"})
	if _, err := fake.Action(ctx, ref, browser.Action{Name: "click"}); err != nil {
		assertBrowserErrorCode(t, err, "internal")
	} else {
		t.Fatal("injected action error was not returned")
	}

	fake.SetCancelError(&application.BrowserError{Code: "context_not_found", Message: "missing"})
	assertBrowserErrorCode(t, fake.Cancel(ctx, ref), "context_not_found")

	fake.SetCloseError(&application.BrowserError{Code: "context_not_found", Message: "missing"})
	assertBrowserErrorCode(t, fake.Close(ctx, ref), "context_not_found")
}

func TestFakeBrowserExecutorCancelAndClose(t *testing.T) {
	fake := testutil.NewFakeBrowserExecutor()
	ctx := context.Background()
	ref, err := fake.NewContext(ctx, browser.ContextSpec{Key: "case_run:1"})
	if err != nil {
		t.Fatalf("fake NewContext() error = %v", err)
	}
	if err := fake.Cancel(ctx, ref); err != nil {
		t.Fatalf("fake Cancel() error = %v", err)
	}
	if err := fake.Close(ctx, ref); err != nil {
		t.Fatalf("fake Close() error = %v", err)
	}
	canceled := fake.Canceled()
	if len(canceled) != 1 || canceled[0] != ref {
		t.Fatalf("canceled = %+v, want [%+v]", canceled, ref)
	}
	closed := fake.Closed()
	if len(closed) != 1 || closed[0] != ref {
		t.Fatalf("closed = %+v, want [%+v]", closed, ref)
	}
	if _, ok := fake.Contexts()[ref.ID]; ok {
		t.Fatal("closed context is still tracked")
	}
}

func TestFakeBrowserExecutorGlobalScriptAndHandler(t *testing.T) {
	var fake testutil.FakeBrowserExecutor
	ctx := context.Background()
	ref := browser.ContextRef{ID: "ctx_1"}

	want := browser.ActionResult{Result: json.RawMessage(`{"ok":true}`)}
	fake.ScriptAction(want)
	got, err := fake.Action(ctx, ref, browser.Action{Name: "snapshot"})
	if err != nil || string(got.Result) != string(want.Result) {
		t.Fatalf("global scripted action = %+v, %v; want %+v", got, err, want)
	}

	fake.ScriptActionError(&application.BrowserError{Code: "timeout", Message: "slow"})
	if _, err := fake.Action(ctx, ref, browser.Action{Name: "click"}); err != nil {
		assertBrowserErrorCode(t, err, "timeout")
	} else {
		t.Fatal("global scripted action error was not returned")
	}

	fake.SetActionHandler(func(ctx context.Context, ref browser.ContextRef, action browser.Action) (browser.ActionResult, error) {
		return browser.ActionResult{Result: json.RawMessage(`{"handled":"` + action.Name + `"}`)}, nil
	})
	got, err = fake.Action(ctx, ref, browser.Action{Name: "press"})
	if err != nil {
		t.Fatalf("handler-backed Action() error = %v", err)
	}
	if string(got.Result) != `{"handled":"press"}` {
		t.Fatalf("handler-backed result = %s", got.Result)
	}
}

func TestFakeBrowserExecutorIsSafeForConcurrentUse(t *testing.T) {
	fake := testutil.NewFakeBrowserExecutor()
	ctx := context.Background()
	ref, err := fake.NewContext(ctx, browser.ContextSpec{Key: "case_run:1"})
	if err != nil {
		t.Fatalf("fake NewContext() error = %v", err)
	}
	const workers = 20
	var wg sync.WaitGroup
	wg.Add(workers)
	for index := 0; index < workers; index++ {
		go func() {
			defer wg.Done()
			_, _ = fake.Action(ctx, ref, browser.Action{Name: "snapshot"})
			_ = fake.Cancel(ctx, ref)
		}()
	}
	wg.Wait()
	if got, want := len(fake.Calls()), 1+2*workers; got != want {
		t.Fatalf("recorded %d calls, want %d", got, want)
	}
}
