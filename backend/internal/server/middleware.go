package server

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

const requestIDHeader = platform.RequestIDHeader

// requestIDPattern is the shape accepted from clients. Anything else is
// replaced with a generated UUID so untrusted input cannot break logs.
var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

// requestIDMiddleware resolves the request ID, stores it in the request
// context for downstream handlers and middleware, and echoes it on the
// response. The context key is platform's, so platform.WriteError can build the
// error envelope without knowing about the server package.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get(requestIDHeader)
		if !requestIDPattern.MatchString(requestID) {
			requestID = id.RequestID()
		}
		w.Header().Set(requestIDHeader, requestID)
		ctx := platform.WithRequestID(r.Context(), requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requestIDFromContext returns the request ID set by requestIDMiddleware.
func requestIDFromContext(ctx context.Context) string {
	return platform.RequestID(ctx)
}

// recoverMiddleware converts a panicking handler into a logged 500 response
// instead of dropping the connection. It runs outside requestIDMiddleware, so
// r.Context() does not carry the ID; it reads the response header (which
// requestIDMiddleware set before dispatching downstream) and stores it in the
// context it hands to platform.WriteError, keeping the envelope and the log
// consistent.
func recoverMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}
				requestID := w.Header().Get(requestIDHeader)
				logger.Error("panic recovered",
					"panic", recovered,
					"request_id", requestID,
					"stack", string(debug.Stack()),
				)
				ctx := platform.WithRequestID(r.Context(), requestID)
				platform.WriteError(w, r.WithContext(ctx), http.StatusInternalServerError,
					platform.CodeInternal, "internal server error")
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// statusWriter records the response status while forwarding the response.
type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(status int) {
	if !w.wroteHeader {
		w.status = status
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.status = http.StatusOK
		w.wroteHeader = true
	}
	return w.ResponseWriter.Write(b)
}

// Flush forwards to the underlying writer so streaming handlers (SSE) keep
// working through the logging middleware. Flushing before an explicit
// WriteHeader implies a 200 status, matching net/http.
func (w *statusWriter) Flush() {
	if !w.wroteHeader {
		w.status = http.StatusOK
		w.wroteHeader = true
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// loggingMiddleware logs method, path, status, duration and request ID. It
// never logs headers or request bodies, which may contain credentials.
func loggingMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			logger.Info("http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"duration", time.Since(start),
				"request_id", requestIDFromContext(r.Context()),
			)
		})
	}
}
