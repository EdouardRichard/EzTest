package server

import (
	"context"
	"log"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/config"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/load"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
)

// Authenticator resolves a bearer token to an actor.
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (platform.Actor, error)
}

// RequestID accepts a valid inbound X-Request-ID or creates a new one and
// propagates it through the context and response header.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestid.Header)
		if !requestid.Valid(id) {
			id = requestid.New()
		}
		w.Header().Set(requestid.Header, id)
		next.ServeHTTP(w, r.WithContext(requestid.With(r.Context(), id)))
	})
}

// Recover turns a panic into the shared 500 envelope and logs the stack.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("panic: %v request_id=%s\n%s", recovered, requestid.From(r.Context()), debug.Stack())
				httpx.Error(w, r, platform.NewError(http.StatusInternalServerError, "internal", "internal server error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Logging records method, path, status, duration and request ID.
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		writer := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(writer, r)
		log.Printf("method=%s path=%s status=%d duration=%s request_id=%s",
			r.Method, r.URL.Path, writer.status, time.Since(started).Round(time.Millisecond), requestid.From(r.Context()))
	})
}

// TrackInflight counts API requests currently being served. Health probes and
// the long-lived SSE stream are excluded: they do not represent work that a
// rolling upgrade must wait for. A nil counter is a no-op.
func TrackInflight(counter *load.Counter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if counter == nil || skipInflight(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			counter.Inc()
			defer counter.Dec()
			next.ServeHTTP(w, r)
		})
	}
}

func skipInflight(path string) bool {
	switch {
	case path == "/healthz", path == "/api/health", path == "/api/health/ready":
		return true
	case path == "/api/stream" || strings.HasPrefix(path, "/api/stream/"):
		return true
	default:
		return false
	}
}

// CORS allows exactly the two origins derived from EZTEST_WEB_PORT.
func CORS(cfg config.Config) func(http.Handler) http.Handler {
	allowed := make(map[string]struct{}, 2)
	for _, origin := range cfg.WebOrigins() {
		allowed[origin] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			_, ok := allowed[origin]
			if ok {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Add("Vary", "Origin")
				w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID, X-Snapshot-Cursor, X-Content-SHA256, WWW-Authenticate")
			}
			if r.Method == http.MethodOptions {
				if ok {
					w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS")
					w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID, Last-Event-ID, Idempotency-Key")
					w.Header().Set("Access-Control-Max-Age", "600")
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireAuth rejects missing/invalid bearers and injects platform.Actor.
func RequireAuth(auth Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := bearerToken(r)
			if !ok {
				httpx.Error(w, r, platform.Unauthenticated())
				return
			}
			actor, err := auth.Authenticate(r.Context(), token)
			if err != nil {
				httpx.Error(w, r, err)
				return
			}
			if actor.ID == 0 {
				httpx.Error(w, r, platform.Unauthenticated())
				return
			}
			next.ServeHTTP(w, r.WithContext(platform.WithActor(r.Context(), actor)))
		})
	}
}

// RequireAdmin requires a valid bearer with the admin role.
func RequireAdmin(auth Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		protected := RequireAuth(auth)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor, ok := platform.ActorFrom(r.Context())
			if !ok || !actor.IsAdmin() {
				httpx.Error(w, r, platform.Forbidden())
				return
			}
			next.ServeHTTP(w, r)
		}))
		return protected
	}
}

func bearerToken(r *http.Request) (string, bool) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}
