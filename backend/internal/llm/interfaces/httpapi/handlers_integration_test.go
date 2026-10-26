//go:build integration

package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditpostgres "github.com/EdouardRichard/EzTest/backend/internal/audit/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/config"
	identityapp "github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/infrastructure/password"
	identitypostgres "github.com/EdouardRichard/EzTest/backend/internal/identity/infrastructure/postgres"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	llm "github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/infrastructure/deepseek"
	llmpostgres "github.com/EdouardRichard/EzTest/backend/internal/llm/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/crypto"
	"github.com/EdouardRichard/EzTest/backend/internal/server"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// mockUpstream simulates a DeepSeek-compatible upstream. The first non-streaming
// chat request is rate limited so the test also proves the configured retry
// policy is applied end to end.
type mockUpstream struct {
	server    *httptest.Server
	chatCalls int32

	mu       sync.Mutex
	chatBody map[string]any
}

func newMockUpstream(t *testing.T) *mockUpstream {
	t.Helper()
	mock := &mockUpstream{}
	mock.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"deepseek-flash","context_window":1048576,"max_output_tokens":8192,"input_modalities":["text","image"]}]}`)
		case "/v1/chat/completions":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mock.mu.Lock()
			mock.chatBody = body
			mock.mu.Unlock()
			stream, _ := body["stream"].(bool)
			if stream {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				for _, chunk := range []string{
					`{"choices":[{"delta":{"content":"integration-"}}]}`,
					`{"choices":[{"delta":{"content":"stream"}}]}`,
					`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`,
					`[DONE]`,
				} {
					_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
					w.(http.Flusher).Flush()
				}
				return
			}
			if atomic.AddInt32(&mock.chatCalls, 1) == 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"error":{"message":"slow down"}}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"integration-ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`)
		case "/v1/responses":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"output":[{"type":"message","content":[{"type":"output_text","text":"responses-ok"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(mock.server.Close)
	return mock
}

func (m *mockUpstream) lastChatBody() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.chatBody
}

// newLLMServer wires the production composition root against the test database
// and the mock upstream.
func newLLMServer(t *testing.T, pool *pgxpool.Pool, mock *mockUpstream) (http.Handler, *llmapp.ConfigService) {
	t.Helper()
	ctx := context.Background()
	keyring, err := crypto.NewKeyring(map[int][]byte{1: bytes.Repeat([]byte{7}, 32)}, 1)
	if err != nil {
		t.Fatal(err)
	}
	userRepo := identitypostgres.NewUserRepository(pool)
	tokenRepo := identitypostgres.NewTokenRepository(pool)
	hasher := password.NewHasher(12)
	auditor := auditapp.NewService(auditpostgres.NewRepository(pool))
	userSvc := identityapp.NewUserService(userRepo, hasher, auditor, tokenRepo)
	if err := userSvc.EnsureAdmin(ctx, "admin", "admin-pass-123"); err != nil {
		t.Fatal(err)
	}
	authSvc := identityapp.NewAuthService(userRepo, tokenRepo, hasher, time.Now, auditor)
	llmSvc := llmapp.NewConfigService(llmpostgres.NewConfigRepository(pool, keyring), auditor)
	factory := func(cfg llm.Config) llmapp.Client {
		timeout := time.Duration(cfg.TimeoutMS) * time.Millisecond
		base := deepseek.NewDeepSeekClient(cfg, &http.Client{Timeout: timeout})
		return llmapp.NewRetryClient(base, cfg.RetryCount, time.Duration(cfg.RetryIntervalMS)*time.Millisecond, timeout)
	}
	h := server.New(config.Config{Env: "test"}, testLogger(), server.Dependencies{
		Pinger:           pool,
		Auth:             authSvc,
		Users:            userSvc,
		Audit:            auditor,
		LLMConfigs:       llmSvc,
		LLMClientFactory: factory,
	})
	return h, llmSvc
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func doRequest(t *testing.T, h http.Handler, method, path, token, body, requestID string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if requestID != "" {
		req.Header.Set("X-Request-ID", requestID)
	}
	req.Header.Set("User-Agent", "eztest-llm-integration/1.0")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func login(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := doRequest(t, h, http.MethodPost, "/api/auth/login", "", `{"username":"admin","password":"admin-pass-123"}`, "req_llm_login_1")
	if rec.Code != http.StatusOK {
		t.Fatalf("login=%d body=%s", rec.Code, rec.Body)
	}
	var payload struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	return payload.Data.Token
}

// auditEntry is the audit API entry shape used by the integration assertions.
type auditEntry struct {
	ActorUserID *int64         `json:"actor_user_id"`
	Action      string         `json:"action"`
	TargetID    string         `json:"target_id"`
	RequestID   string         `json:"request_id"`
	Metadata    map[string]any `json:"metadata"`
}

// TestLLMAdminAPIsAgainstDatabase is the Task 10 end-to-end check: encrypted
// configuration CRUD, catalog refresh, both test modes (with retry and an
// image), the normalized SSE stream, persisted sanitized audits, admin-only
// access and the health flag derived from the decryptable default key.
func TestLLMAdminAPIsAgainstDatabase(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	mock := newMockUpstream(t)
	h, llmSvc := newLLMServer(t, pool, mock)
	adminToken := login(t, h)

	// Create the default configuration. The base URL points at the mock.
	createBody := fmt.Sprintf(`{"name":"default","protocol":"chat_completions","base_url":%q,"model":"deepseek-flash","api_key":"sk-integration-secret","headers":{"X-Custom":"secret-header-value"},"retry_count":2,"retry_interval_ms":100,"timeout_ms":5000,"is_default":true}`, mock.server.URL)
	rec := doRequest(t, h, http.MethodPost, "/api/admin/llm/configs", adminToken, createBody, "req_llm_create_1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create=%d body=%s", rec.Code, rec.Body)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("sk-integration-secret")) || bytes.Contains(rec.Body.Bytes(), []byte("secret-header-value")) {
		t.Fatalf("create response leaked secrets: %s", rec.Body)
	}
	var created struct {
		Data struct {
			ID          int64    `json:"id"`
			HasAPIKey   bool     `json:"has_api_key"`
			KeyVersion  int      `json:"key_version"`
			HeaderNames []string `json:"header_names"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.Data.ID == 0 || !created.Data.HasAPIKey || created.Data.KeyVersion != 1 ||
		len(created.Data.HeaderNames) != 1 || created.Data.HeaderNames[0] != "X-Custom" {
		t.Fatalf("created=%+v", created.Data)
	}

	// Secrets are encrypted at rest.
	var apiKeyEnc, headersEnc []byte
	if err := pool.QueryRow(ctx, `SELECT api_key_encrypted, headers_encrypted FROM llm_configs WHERE id=$1`, created.Data.ID).
		Scan(&apiKeyEnc, &headersEnc); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(apiKeyEnc, []byte("sk-integration-secret")) ||
		bytes.Contains(headersEnc, []byte("secret-header-value")) {
		t.Fatal("llm secrets were persisted in plaintext")
	}

	// The admin list never returns the secrets.
	rec = doRequest(t, h, http.MethodGet, "/api/admin/llm/configs", adminToken, "", "req_llm_list_1")
	if rec.Code != http.StatusOK {
		t.Fatalf("list=%d body=%s", rec.Code, rec.Body)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("sk-integration-secret")) || bytes.Contains(rec.Body.Bytes(), []byte("secret-header-value")) {
		t.Fatalf("list leaked secrets: %s", rec.Body)
	}

	// Catalog refresh persists context_window.
	rec = doRequest(t, h, http.MethodPost, fmt.Sprintf("/api/admin/llm/configs/%d/catalog/refresh", created.Data.ID), adminToken, "", "req_llm_catalog_1")
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog refresh=%d body=%s", rec.Code, rec.Body)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"context_window":1048576`)) {
		t.Fatalf("catalog body=%s", rec.Body)
	}
	var catalogJSON []byte
	var fetchedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT model_catalog, catalog_fetched_at FROM llm_configs WHERE id=$1`, created.Data.ID).
		Scan(&catalogJSON, &fetchedAt); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(catalogJSON, []byte("1048576")) || fetchedAt == nil {
		t.Fatalf("catalog not persisted: catalog=%s fetched_at=%v", catalogJSON, fetchedAt)
	}

	// A non-streaming image test: the first upstream call is 429, the retry
	// succeeds. The audit entry is persisted with the admin actor.
	rec = doRequest(t, h, http.MethodPost, fmt.Sprintf("/api/admin/llm/configs/%d/test", created.Data.ID), adminToken, `{"image":true,"prompt":"describe"}`, "req_llm_test_1")
	if rec.Code != http.StatusOK {
		t.Fatalf("image test=%d body=%s", rec.Code, rec.Body)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"text":"integration-ok"`)) {
		t.Fatalf("image test body=%s", rec.Body)
	}
	if got := atomic.LoadInt32(&mock.chatCalls); got != 2 {
		t.Fatalf("upstream chat calls=%d want 2 (429 then retry)", got)
	}
	chatBody := mock.lastChatBody()
	messages, _ := chatBody["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("upstream messages=%v", chatBody["messages"])
	}
	content, _ := messages[0].(map[string]any)["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("upstream content=%v", content)
	}
	imagePart, _ := content[1].(map[string]any)
	imageURL, _ := imagePart["image_url"].(map[string]any)
	if imagePart["type"] != "image_url" || imageURL["url"] == "" {
		t.Fatalf("upstream image part=%v", content[1])
	}

	// The streaming test returns normalized SSE frames.
	rec = doRequest(t, h, http.MethodPost, fmt.Sprintf("/api/admin/llm/configs/%d/test", created.Data.ID), adminToken, `{"stream":true}`, "req_llm_test_stream_1")
	if rec.Code != http.StatusOK {
		t.Fatalf("stream test=%d body=%s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("stream Content-Type=%q", ct)
	}
	for _, want := range []string{"event: llm.delta", `"delta":"integration-"`, `"delta":"stream"`, "event: llm.done", `"total_tokens":3`} {
		if !bytes.Contains(rec.Body.Bytes(), []byte(want)) {
			t.Fatalf("stream body missing %q: %s", want, rec.Body)
		}
	}

	// A responses-protocol configuration also parses its non-streaming text.
	responsesBody := fmt.Sprintf(`{"name":"responses","protocol":"responses","base_url":%q,"model":"deepseek-flash","api_key":"sk-responses","is_default":false}`, mock.server.URL)
	rec = doRequest(t, h, http.MethodPost, "/api/admin/llm/configs", adminToken, responsesBody, "req_llm_create_responses_1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create responses=%d body=%s", rec.Code, rec.Body)
	}
	var responsesCfg struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &responsesCfg); err != nil {
		t.Fatal(err)
	}
	rec = doRequest(t, h, http.MethodPost, fmt.Sprintf("/api/admin/llm/configs/%d/test", responsesCfg.Data.ID), adminToken, `{}`, "req_llm_test_responses_1")
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"text":"responses-ok"`)) {
		t.Fatalf("responses test=%d body=%s", rec.Code, rec.Body)
	}

	// PATCH keeps omitted secrets and replaces provided ones.
	rec = doRequest(t, h, http.MethodPatch, fmt.Sprintf("/api/admin/llm/configs/%d", created.Data.ID), adminToken, `{"model":"deepseek-pro"}`, "req_llm_patch_1")
	if rec.Code != http.StatusOK {
		t.Fatalf("patch=%d body=%s", rec.Code, rec.Body)
	}
	stored, err := llmSvc.Get(ctx, created.Data.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.APIKey != "sk-integration-secret" || stored.Headers["X-Custom"] != "secret-header-value" || stored.Model != "deepseek-pro" {
		t.Fatalf("PATCH lost or failed to update fields: %+v", stored)
	}
	rec = doRequest(t, h, http.MethodPatch, fmt.Sprintf("/api/admin/llm/configs/%d", created.Data.ID), adminToken, `{"api_key":"sk-integration-new","headers":{"X-New":"new-value"}}`, "req_llm_patch_2")
	if rec.Code != http.StatusOK {
		t.Fatalf("replace patch=%d body=%s", rec.Code, rec.Body)
	}
	stored, err = llmSvc.Get(ctx, created.Data.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.APIKey != "sk-integration-new" || len(stored.Headers) != 1 || stored.Headers["X-New"] != "new-value" {
		t.Fatalf("PATCH did not replace secrets: %+v", stored)
	}

	// The test audit entries are persisted, sanitized and actor-stamped.
	rec = doRequest(t, h, http.MethodGet, "/api/admin/audit?limit=100", adminToken, "", "req_llm_audit_1")
	if rec.Code != http.StatusOK {
		t.Fatalf("audit=%d body=%s", rec.Code, rec.Body)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("sk-integration")) || bytes.Contains(rec.Body.Bytes(), []byte("secret-header-value")) {
		t.Fatalf("audit leaked secrets: %s", rec.Body)
	}
	var auditPage struct {
		Data []auditEntry `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &auditPage); err != nil {
		t.Fatal(err)
	}
	var tested *auditEntry
	for i := range auditPage.Data {
		if auditPage.Data[i].Action == auditapp.ActionLLMTestSucceeded && auditPage.Data[i].RequestID == "req_llm_test_1" {
			tested = &auditPage.Data[i]
			break
		}
	}
	if tested == nil {
		t.Fatalf("audit log has no llm.test_succeeded entry: %+v", auditPage.Data)
	}
	if tested.ActorUserID == nil || *tested.ActorUserID != 1 {
		t.Fatalf("test audit actor=%v want 1", tested.ActorUserID)
	}
	if tested.Metadata["image"] != true {
		t.Fatalf("test audit metadata=%v", tested.Metadata)
	}

	// Health reports the configured default, and admin-only access is enforced.
	rec = doRequest(t, h, http.MethodGet, "/api/health", "", "", "req_llm_health_1")
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"llm_configured":true`)) {
		t.Fatalf("health=%d body=%s", rec.Code, rec.Body)
	}
	rec = doRequest(t, h, http.MethodPost, "/api/users", adminToken, `{"username":"qa01","password":"qa-pass-1234"}`, "req_llm_user_1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create user=%d body=%s", rec.Code, rec.Body)
	}
	rec = doRequest(t, h, http.MethodPost, "/api/auth/login", "", `{"username":"qa01","password":"qa-pass-1234"}`, "req_llm_login_2")
	if rec.Code != http.StatusOK {
		t.Fatalf("user login=%d body=%s", rec.Code, rec.Body)
	}
	var userLogin struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &userLogin); err != nil {
		t.Fatal(err)
	}
	rec = doRequest(t, h, http.MethodGet, "/api/admin/llm/configs", userLogin.Data.Token, "", "req_llm_forbidden_1")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("regular user list=%d want 403 body=%s", rec.Code, rec.Body)
	}

	// Deleting the default flips llm_configured back to false.
	rec = doRequest(t, h, http.MethodDelete, fmt.Sprintf("/api/admin/llm/configs/%d", created.Data.ID), adminToken, "", "req_llm_delete_1")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete=%d body=%s", rec.Code, rec.Body)
	}
	rec = doRequest(t, h, http.MethodGet, "/api/health", "", "", "req_llm_health_2")
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"llm_configured":false`)) {
		t.Fatalf("health after delete=%d body=%s", rec.Code, rec.Body)
	}
}

// TestSeedSkipsPreexistingDefaultNameAgainstDatabase covers the Task 9 review
// minor on the real repository: a non-default row named "default" must not abort
// startup seeding, and persisted strings are trimmed.
func TestSeedSkipsPreexistingDefaultNameAgainstDatabase(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	keyring, err := crypto.NewKeyring(map[int][]byte{1: bytes.Repeat([]byte{9}, 32)}, 1)
	if err != nil {
		t.Fatal(err)
	}
	repo := llmpostgres.NewConfigRepository(pool, keyring)
	svc := llmapp.NewConfigService(repo, auditapp.NopRecorder{})

	existing, err := repo.Create(ctx, llm.CreateParams{
		Name:       "  default  ",
		Protocol:   llm.ProtocolChat,
		BaseURL:    "  https://api.deepseek.com  ",
		Model:      "  deepseek-flash  ",
		APIKey:     "sk-existing",
		Headers:    map[string]string{"  X-Existing  ": "v"},
		IsDefault:  false,
		RetryCount: 2, RetryIntervalMS: 500, TimeoutMS: 60000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if existing.Name != "default" || existing.BaseURL != "https://api.deepseek.com" || existing.Model != "deepseek-flash" {
		t.Fatalf("repository did not trim persisted strings: %+v", existing)
	}
	if len(existing.Headers) != 1 || existing.Headers["X-Existing"] != "v" {
		t.Fatalf("repository did not trim header names: %+v", existing.Headers)
	}

	seeded, err := svc.SeedFromEnv(ctx, "https://api.deepseek.com", "sk-seed", "deepseek-flash")
	if err != nil {
		t.Fatalf("seed aborted startup over a name conflict: %v", err)
	}
	if seeded {
		t.Fatal("seed unexpectedly created a configuration")
	}
	configs, err := svc.List(ctx)
	if err != nil || len(configs) != 1 {
		t.Fatalf("configs=%d err=%v", len(configs), err)
	}
	if configs[0].IsDefault || configs[0].APIKey != "sk-existing" {
		t.Fatalf("existing config was modified: %+v", configs[0])
	}
}
