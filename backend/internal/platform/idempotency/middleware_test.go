package idempotency_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/idempotency"
)

// fakeStore is an in-memory idempotency.Store. It is mutex-protected because the
// middleware tests exercise concurrent requests.
type fakeStore struct {
	mu      sync.Mutex
	entries map[string]fakeEntry
	getErr  error
	saveErr error
	gets    int
	saves   int
}

type fakeEntry struct {
	status int
	body   []byte
	hash   string
}

func newFakeIdempotencyStore() *fakeStore {
	return &fakeStore{entries: make(map[string]fakeEntry)}
}

func storeKey(userID int64, key, method, path string) string {
	return fmt.Sprintf("%d|%s|%s|%s", userID, key, method, path)
}

func (s *fakeStore) Get(_ context.Context, userID int64, key, method, path string) (*idempotency.StoredResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	if s.getErr != nil {
		return nil, s.getErr
	}
	entry, ok := s.entries[storeKey(userID, key, method, path)]
	if !ok {
		return nil, nil
	}
	return &idempotency.StoredResponse{
		Status:      entry.status,
		Body:        append([]byte(nil), entry.body...),
		RequestHash: entry.hash,
	}, nil
}

func (s *fakeStore) Save(_ context.Context, userID int64, key, method, path, requestHash string, status int, body []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	if s.saveErr != nil {
		return s.saveErr
	}
	s.entries[storeKey(userID, key, method, path)] = fakeEntry{
		status: status,
		body:   append([]byte(nil), body...),
		hash:   requestHash,
	}
	return nil
}

func (s *fakeStore) counts() (gets, saves int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets, s.saves
}

func (s *fakeStore) entry(userID int64, key, method, path string) (fakeEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[storeKey(userID, key, method, path)]
	return entry, ok
}

const testKey = "key_12345678"

func withUser(ctx context.Context, userID int64) context.Context {
	return context.WithValue(ctx, userIDContextKey{}, userID)
}

type userIDContextKey struct{}

func userIDFromContext(ctx context.Context) (int64, bool) {
	userID, ok := ctx.Value(userIDContextKey{}).(int64)
	return userID, ok
}

func postRequest(path, body, key string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	return req
}

func writeJSONHandler(status int, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

func TestIdempotencyReplayAndConflict(t *testing.T) {
	store := newFakeIdempotencyStore()
	var calls int32
	h := idempotency.Middleware(store, func(context.Context) (int64, bool) { return 1, true })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"data":{"id":1}}`))
		}))
	newReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/api/users", strings.NewReader(`{"username":"qa01"}`))
		req.Header.Set("Idempotency-Key", testKey)
		return req
	}

	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, newReq())
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first code=%d body=%s", rec1.Code, rec1.Body)
	}
	if rec1.Header().Get("Idempotent-Replayed") != "" {
		t.Fatal("first response must not be marked as replayed")
	}

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, newReq())
	if rec2.Code != http.StatusCreated {
		t.Fatalf("replay code=%d body=%s", rec2.Code, rec2.Body)
	}
	if rec2.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatal("not replayed")
	}
	if got := rec2.Body.String(); got != `{"data":{"id":1}}` {
		t.Fatalf("replayed body=%q", got)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("handler calls=%d, want 1", got)
	}

	// Same key, different body: the key is already bound to another request.
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, postRequest("/api/users", `{"username":"qa02"}`, testKey))
	if rec3.Code != http.StatusConflict {
		t.Fatalf("conflict code=%d body=%s", rec3.Code, rec3.Body)
	}
	if got := errorCode(t, rec3.Body.Bytes()); got != platform.CodeIdempotencyConflict {
		t.Fatalf("conflict code=%q, want %s", got, platform.CodeIdempotencyConflict)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("handler calls=%d after conflict, want 1", got)
	}
}

func TestIdempotencyConcurrentSameKey(t *testing.T) {
	store := newFakeIdempotencyStore()
	var calls int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(20 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":1}}`))
	})
	h := idempotency.Middleware(store, func(context.Context) (int64, bool) { return 1, true })(handler)

	const requestBody = `{"username":"qa01"}`
	start := make(chan struct{})
	results := make([]*httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, postRequest("/api/users", requestBody, testKey))
			results[i] = rec
		}(i)
	}
	close(start)
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("handler calls=%d, want 1 (same-key requests must serialize)", got)
	}
	replayed := 0
	for _, rec := range results {
		if rec.Code != http.StatusCreated {
			t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
		}
		if rec.Header().Get("Idempotent-Replayed") == "true" {
			replayed++
		}
	}
	if replayed != 1 {
		t.Fatalf("replayed responses=%d, want exactly 1", replayed)
	}
}

func TestIdempotencyWithoutKeyPassesThrough(t *testing.T) {
	store := newFakeIdempotencyStore()
	var calls int32
	h := idempotency.Middleware(store, func(context.Context) (int64, bool) { return 1, true })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.WriteHeader(http.StatusNoContent)
		}))

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, postRequest("/api/users", "", ""))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("code=%d", rec.Code)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("handler calls=%d, want 2 without an idempotency key", got)
	}
	if gets, saves := store.counts(); gets != 0 || saves != 0 {
		t.Fatalf("store used without a key: gets=%d saves=%d", gets, saves)
	}
}

func TestIdempotencySkipsNonWriteAndStreamingPaths(t *testing.T) {
	store := newFakeIdempotencyStore()
	var calls int32
	h := idempotency.Middleware(store, func(context.Context) (int64, bool) { return 1, true })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{}}`))
		}))

	cases := []struct {
		name   string
		method string
		path   string
	}{
		{name: "GET is never idempotent-cached", method: http.MethodGet, path: "/api/users"},
		{name: "health", method: http.MethodGet, path: "/healthz"},
		{name: "auth login", method: http.MethodPost, path: "/api/auth/login"},
		{name: "auth logout", method: http.MethodPost, path: "/api/auth/logout"},
		{name: "stream", method: http.MethodPost, path: "/api/stream/events"},
		{name: "streamed llm test", method: http.MethodPost, path: "/api/admin/llm/configs/7/test?stream=true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := atomic.LoadInt32(&calls)
			for i := 0; i < 2; i++ {
				req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"n":1}`))
				req.Header.Set("Idempotency-Key", testKey)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Header().Get("Idempotent-Replayed") != "" {
					t.Fatalf("skipped path %s %s was replayed", tc.method, tc.path)
				}
			}
			if got := atomic.LoadInt32(&calls) - before; got != 2 {
				t.Fatalf("handler calls=%d, want 2 for a skipped path", got)
			}
		})
	}
}

func TestIdempotencyNonStreamingLLMTestIsCached(t *testing.T) {
	store := newFakeIdempotencyStore()
	var calls int32
	h := idempotency.Middleware(store, func(context.Context) (int64, bool) { return 1, true })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"text":"ok"}}`))
		}))
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, postRequest("/api/admin/llm/configs/7/test", `{"prompt":"hi"}`, testKey))
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d", rec.Code)
		}
		if i == 1 && rec.Header().Get("Idempotent-Replayed") != "true" {
			t.Fatal("non-streaming LLM test must be cached")
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("handler calls=%d, want 1", got)
	}
}

// TestIdempotencyStreamingLLMTestBodySkips proves the LLM test endpoint is also
// skipped when stream=true is in the JSON body, which is the documented request
// shape: the middleware must not buffer the SSE response or replay it.
func TestIdempotencyStreamingLLMTestBodySkips(t *testing.T) {
	store := newFakeIdempotencyStore()
	var calls int32
	h := idempotency.Middleware(store, func(context.Context) (int64, bool) { return 1, true })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Fatal("streaming handler lost http.Flusher behind the idempotency middleware")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "event: llm.done\ndata: {\"usage\":{}}\n\n")
			flusher.Flush()
		}))
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, postRequest("/api/admin/llm/configs/7/test", `{"stream":true}`, testKey))
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d: code=%d body=%s", i, rec.Code, rec.Body)
		}
		if rec.Header().Get("Idempotent-Replayed") != "" {
			t.Fatalf("call %d: streaming LLM test was replayed", i)
		}
		if !rec.Flushed {
			t.Fatalf("call %d: SSE response was buffered by the idempotency middleware", i)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("handler calls=%d want 2", got)
	}
}

func TestIdempotencyInvalidKeyRejected(t *testing.T) {
	store := newFakeIdempotencyStore()
	var calls int32
	h := idempotency.Middleware(store, func(context.Context) (int64, bool) { return 1, true })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
		}))

	for _, key := range []string{"short", "bad key!", strings.Repeat("a", 129)} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, postRequest("/api/users", `{"username":"qa01"}`, key))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("key %q: code=%d, want 400", key, rec.Code)
		}
		if got := errorCode(t, rec.Body.Bytes()); got != platform.CodeInvalidArgument {
			t.Fatalf("key %q: code=%q, want %s", key, got, platform.CodeInvalidArgument)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("handler calls=%d, want 0 for invalid keys", got)
	}
}

func TestIdempotencyUnauthenticatedPassesThrough(t *testing.T) {
	store := newFakeIdempotencyStore()
	var calls int32
	h := idempotency.Middleware(store, func(context.Context) (int64, bool) { return 0, false })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{}}`))
		}))
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, postRequest("/api/users", `{"username":"qa01"}`, testKey))
		if rec.Header().Get("Idempotent-Replayed") != "" {
			t.Fatal("unauthenticated request must not be replayed")
		}
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("handler calls=%d, want 2", got)
	}
}

func TestIdempotencyUsersAreIsolated(t *testing.T) {
	store := newFakeIdempotencyStore()
	var calls int32
	h := idempotency.Middleware(store, userIDFromContext)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{}}`))
		}))
	for _, userID := range []int64{1, 2, 1, 2} {
		req := postRequest("/api/users", `{"username":"qa01"}`, testKey)
		req = req.WithContext(withUser(req.Context(), userID))
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("handler calls=%d, want 2 (one per user)", got)
	}
}

func TestIdempotencyRequestHashFormula(t *testing.T) {
	store := newFakeIdempotencyStore()
	const body = `{"username":"qa01"}`
	h := idempotency.Middleware(store, func(context.Context) (int64, bool) { return 1, true })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The middleware buffers the body to hash it and must restore it for
			// the handler.
			got, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read request body: %v", err)
			}
			if string(got) != body {
				t.Errorf("handler body=%q, want %q", got, body)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":1}}`))
		}))
	h.ServeHTTP(httptest.NewRecorder(), postRequest("/api/users?ignored=1", body, testKey))

	entry, ok := store.entry(1, testKey, http.MethodPost, "/api/users")
	if !ok {
		t.Fatal("store has no entry")
	}
	sum := sha256.Sum256([]byte(http.MethodPost + "\n" + "/api/users" + "\n" + body))
	want := hex.EncodeToString(sum[:])
	if entry.hash != want {
		t.Fatalf("request hash=%q, want %q (SHA-256(method+\\n+path+\\n+body))", entry.hash, want)
	}
}

func TestIdempotencyNonJSONResponseNotCached(t *testing.T) {
	store := newFakeIdempotencyStore()
	var calls int32
	h := idempotency.Middleware(store, func(context.Context) (int64, bool) { return 1, true })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		}))
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, postRequest("/api/upload", "", testKey))
		if rec.Header().Get("Idempotent-Replayed") != "" {
			t.Fatal("non-JSON response was replayed")
		}
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("handler calls=%d, want 2", got)
	}
	if _, saves := store.counts(); saves != 0 {
		t.Fatalf("non-JSON response was saved %d times", saves)
	}
}

func TestIdempotencyLargeJSONResponseNotCached(t *testing.T) {
	store := newFakeIdempotencyStore()
	// A valid JSON body of exactly the cache limit: the contract only caches
	// responses strictly smaller than 1 MiB.
	body := `{"data":"` + strings.Repeat("a", int(idempotency.MaxCachedResponseBytes)-len(`{"data":""}`)) + `"}`
	if int64(len(body)) != idempotency.MaxCachedResponseBytes {
		t.Fatalf("test body size=%d, want %d", len(body), idempotency.MaxCachedResponseBytes)
	}
	var calls int32
	h := idempotency.Middleware(store, func(context.Context) (int64, bool) { return 1, true })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(body))
		}))
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, postRequest("/api/users", `{"username":"qa01"}`, testKey))
		if rec.Header().Get("Idempotent-Replayed") != "" {
			t.Fatal("oversized response was replayed")
		}
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("handler calls=%d, want 2", got)
	}
	if _, saves := store.counts(); saves != 0 {
		t.Fatalf("oversized response was saved %d times", saves)
	}
}

func TestIdempotencyStoreErrors(t *testing.T) {
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	t.Run("get error fails closed", func(t *testing.T) {
		store := newFakeIdempotencyStore()
		store.getErr = fmt.Errorf("database down")
		var calls int32
		h := idempotency.Middleware(store, func(context.Context) (int64, bool) { return 1, true })(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&calls, 1)
			}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, postRequest("/api/users", `{"username":"qa01"}`, testKey))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("code=%d, want 500", rec.Code)
		}
		if got := errorCode(t, rec.Body.Bytes()); got != platform.CodeInternal {
			t.Fatalf("code=%q, want %s", got, platform.CodeInternal)
		}
		if got := atomic.LoadInt32(&calls); got != 0 {
			t.Fatalf("handler calls=%d, want 0 when the store lookup failed", got)
		}
	})

	t.Run("save error still returns the handler response", func(t *testing.T) {
		store := newFakeIdempotencyStore()
		store.saveErr = fmt.Errorf("database down")
		h := idempotency.Middleware(store, func(context.Context) (int64, bool) { return 1, true })(
			writeJSONHandler(http.StatusCreated, `{"data":{"id":1}}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, postRequest("/api/users", `{"username":"qa01"}`, testKey))
		if rec.Code != http.StatusCreated {
			t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
		}
	})
}

func TestIdempotencyOversizedRequestBodyBypassesCache(t *testing.T) {
	store := newFakeIdempotencyStore()
	// The largest documented P0 write body (storage upload) is 10 MiB. A body
	// above that must not be buffered by the idempotency middleware; it is
	// handed to the endpoint, whose own limit rejects it, and is not cached.
	const oversizedBody = 10<<20 + 1
	body := strings.Repeat("a", oversizedBody)

	var calls int32
	var bytesSeen int64
	h := idempotency.Middleware(store, func(context.Context) (int64, bool) { return 1, true })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			n, err := io.Copy(io.Discard, r.Body)
			if err != nil {
				t.Errorf("read body: %v", err)
			}
			atomic.StoreInt64(&bytesSeen, n)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{}}`))
		}))

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, postRequest("/api/upload", body, testKey))
		if rec.Code != http.StatusCreated {
			t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
		}
		if rec.Header().Get("Idempotent-Replayed") != "" {
			t.Fatal("oversized request body was replayed")
		}
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("handler calls=%d, want 2 for an oversized body", got)
	}
	if got := atomic.LoadInt64(&bytesSeen); got != oversizedBody {
		t.Fatalf("handler saw %d body bytes, want %d", got, oversizedBody)
	}
	if gets, saves := store.counts(); gets != 0 || saves != 0 {
		t.Fatalf("oversized body touched the store: gets=%d saves=%d", gets, saves)
	}
}

func TestIdempotencyHandlerCanWriteHeadersWithoutWriteHeader(t *testing.T) {
	store := newFakeIdempotencyStore()
	h := idempotency.Middleware(store, func(context.Context) (int64, bool) { return 1, true })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"id":1}}`))
		}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postRequest("/api/users", `{"username":"qa01"}`, testKey))
	if rec.Code != http.StatusOK {
		t.Fatalf("implicit status code=%d, want 200", rec.Code)
	}
}

func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode error response %q: %v", body, err)
	}
	return payload.Error.Code
}

// TestMiddlewareDoesNotLeakBufferedBodyToReplay proves the replay writes the
// stored body, not the current request body.
func TestMiddlewareDoesNotLeakBufferedBodyToReplay(t *testing.T) {
	store := newFakeIdempotencyStore()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Username string `json:"username"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode handler body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"username":"` + payload.Username + `"}}`))
	})
	h := idempotency.Middleware(store, func(context.Context) (int64, bool) { return 1, true })(handler)

	first := httptest.NewRecorder()
	h.ServeHTTP(first, postRequest("/api/users", `{"username":"qa01"}`, testKey))
	second := httptest.NewRecorder()
	h.ServeHTTP(second, postRequest("/api/users", `{"username":"qa01"}`, testKey))
	if !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		t.Fatalf("replayed body=%s, first body=%s", second.Body, first.Body)
	}
	if second.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatal("second response was not a replay")
	}
}
