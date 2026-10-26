package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

const (
	// idempotencyKeyHeader is the request header that opts a write request into
	// idempotent replay.
	idempotencyKeyHeader = "Idempotency-Key"
	// replayedHeader marks a response served from the idempotency store.
	replayedHeader = "Idempotent-Replayed"
)

// idempotencyKeyPattern is the accepted key shape from the design's §9.1.
var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

// Middleware replays stored responses for write requests that carry an
// Idempotency-Key header. userID must return the authenticated user; requests
// without a user are passed through untouched (authentication is a separate
// middleware that must run first).
//
// The request body is buffered to compute SHA-256(method+"\n"+path+"\n"+body)
// and restored for the handler. Responses are buffered so a successful JSON
// response under 1 MiB can be stored after the handler completes. Same-key
// requests are serialised in-process, so two concurrent retries execute the
// handler once; the second is served from the store.
func Middleware(store Store, userID func(context.Context) (int64, bool)) func(http.Handler) http.Handler {
	locks := newKeyedLocks()
	return func(next http.Handler) http.Handler {
		if store == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isWriteMethod(r.Method) || skipIdempotency(r) {
				next.ServeHTTP(w, r)
				return
			}
			if userID == nil {
				next.ServeHTTP(w, r)
				return
			}
			authenticatedUserID, ok := userID(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}

			key := strings.TrimSpace(r.Header.Get(idempotencyKeyHeader))
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}
			if !idempotencyKeyPattern.MatchString(key) {
				platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument,
					"Idempotency-Key must match ^[A-Za-z0-9_-]{8,128}$")
				return
			}

			body, err := io.ReadAll(io.LimitReader(r.Body, MaxBufferedRequestBytes+1))
			if err != nil {
				platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "cannot read request body")
				return
			}
			if int64(len(body)) > MaxBufferedRequestBytes {
				// The body is larger than any P0 write endpoint accepts; hand
				// the untruncated body to the handler instead of buffering it.
				r.Body = multiReadCloser{
					Reader: io.MultiReader(bytes.NewReader(body), r.Body),
					Closer: r.Body,
				}
				next.ServeHTTP(w, r)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			// The LLM test endpoint carries stream in its JSON body, so the
			// stream flag can only be inspected after the body is buffered.
			if isStreamingLLMTestBody(r, body) {
				next.ServeHTTP(w, r)
				return
			}
			requestHash := hashRequest(r.Method, r.URL.Path, body)

			// Serialise concurrent requests that share the same idempotency
			// scope so the handler runs once and later callers replay it.
			unlock := locks.lock(lockKey(authenticatedUserID, key, r.Method, r.URL.Path))
			defer unlock()

			stored, err := store.Get(r.Context(), authenticatedUserID, key, r.Method, r.URL.Path)
			if err != nil {
				slog.ErrorContext(r.Context(), "idempotency: load stored response", "error", err)
				platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal, "internal server error")
				return
			}
			if stored != nil {
				if stored.RequestHash != requestHash {
					platform.WriteError(w, r, http.StatusConflict, platform.CodeIdempotencyConflict,
						"Idempotency-Key was already used with a different request body")
					return
				}
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.Header().Set(replayedHeader, "true")
				w.WriteHeader(stored.Status)
				_, _ = w.Write(stored.Body)
				return
			}

			recorder := &responseRecorder{ResponseWriter: w}
			next.ServeHTTP(recorder, r)
			if shouldCache(recorder) {
				if err := store.Save(r.Context(), authenticatedUserID, key, r.Method, r.URL.Path,
					requestHash, recorder.status, recorder.body.Bytes()); err != nil {
					// The side effect already happened; failing the response now
					// would be worse than losing the replay record.
					slog.ErrorContext(r.Context(), "idempotency: save response", "error", err)
				}
			}
			recorder.flush()
		})
	}
}

// isWriteMethod reports whether method may have side effects. GET, HEAD and
// OPTIONS are never cached.
func isWriteMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

// skipIdempotency reports whether the request must bypass idempotency: the SSE
// stream, all auth endpoints, and the stream=true LLM test endpoint.
func skipIdempotency(r *http.Request) bool {
	path := r.URL.Path
	if path == "/api/stream" || strings.HasPrefix(path, "/api/stream/") {
		return true
	}
	if strings.HasPrefix(path, "/api/auth/") {
		return true
	}
	return isStreamingLLMTest(r)
}

// isStreamingLLMTest matches POST /api/admin/llm/configs/{id}/test with
// stream=true in the query string.
func isStreamingLLMTest(r *http.Request) bool {
	if r.URL.Query().Get("stream") != "true" {
		return false
	}
	return isLLMTestPath(r.URL.Path)
}

// isStreamingLLMTestBody matches the same endpoint when the documented body
// carries {"stream":true}. The body flag must also bypass the middleware:
// buffering the SSE response would hide http.Flusher and break the stream.
func isStreamingLLMTestBody(r *http.Request, body []byte) bool {
	if !isLLMTestPath(r.URL.Path) {
		return false
	}
	var payload struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	return payload.Stream
}

// isLLMTestPath reports whether path is POST /api/admin/llm/configs/{id}/test.
func isLLMTestPath(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	return len(parts) == 6 &&
		parts[0] == "api" &&
		parts[1] == "admin" &&
		parts[2] == "llm" &&
		parts[3] == "configs" &&
		parts[4] != "" &&
		parts[5] == "test"
}

// hashRequest implements SHA-256(method + "\n" + path + "\n" + body) from the
// API contract.
func hashRequest(method, path string, body []byte) string {
	sum := sha256.Sum256([]byte(method + "\n" + path + "\n" + string(body)))
	return hex.EncodeToString(sum[:])
}

// shouldCache reports whether the completed response may be replayed. Only
// successful JSON responses strictly smaller than MaxCachedResponseBytes are
// cached; streaming and other content types are not.
func shouldCache(recorder *responseRecorder) bool {
	if recorder.status < http.StatusOK || recorder.status >= http.StatusMultipleChoices {
		return false
	}
	if recorder.body.Len() == 0 || int64(recorder.body.Len()) >= MaxCachedResponseBytes {
		return false
	}
	return isJSONContentType(recorder.Header().Get("Content-Type"))
}

func isJSONContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

// responseRecorder buffers a handler's status and body so the middleware can
// decide whether to store them. Headers are written through to the real
// response. Replaying handlers do not use this writer.
type responseRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	body        bytes.Buffer
}

// multiReadCloser restores a partially consumed request body for pass-through
// requests while preserving Close on the original body.
type multiReadCloser struct {
	io.Reader
	io.Closer
}

func (r *responseRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.wroteHeader = true
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.status = http.StatusOK
		r.wroteHeader = true
	}
	return r.body.Write(b)
}

// flush writes the buffered response to the underlying writer exactly once.
func (r *responseRecorder) flush() {
	if !r.wroteHeader {
		r.status = http.StatusOK
	}
	r.ResponseWriter.WriteHeader(r.status)
	if r.body.Len() > 0 {
		_, _ = r.ResponseWriter.Write(r.body.Bytes())
	}
}

// keyedLocks serialises requests per idempotency scope and reclaims idle locks
// so the map cannot grow without bound.
type keyedLocks struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

type keyedLock struct {
	mu   sync.Mutex
	refs int
}

func newKeyedLocks() *keyedLocks {
	return &keyedLocks{locks: make(map[string]*keyedLock)}
}

// lock blocks until the scope is free and returns its unlock function.
func (l *keyedLocks) lock(key string) func() {
	l.mu.Lock()
	entry := l.locks[key]
	if entry == nil {
		entry = &keyedLock{}
		l.locks[key] = entry
	}
	entry.refs++
	l.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		l.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(l.locks, key)
		}
		l.mu.Unlock()
	}
}

func lockKey(userID int64, key, method, path string) string {
	return method + "\x00" + path + "\x00" + key + "\x00" + strconv.FormatInt(userID, 10)
}
