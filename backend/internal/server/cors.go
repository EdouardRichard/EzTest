package server

import (
	"net/http"
	"strings"
)

const (
	// corsAllowMethods is the closed set of methods the API advertises.
	corsAllowMethods = "GET, POST, PUT, PATCH, DELETE, OPTIONS"
	// corsAllowHeaders is the closed set of request headers the web client may
	// send.
	corsAllowHeaders = "Authorization, Content-Type, X-Request-ID, Idempotency-Key, Last-Event-ID"
	// corsExposeHeaders is the set of response headers the web client may read.
	corsExposeHeaders = "X-Request-ID, X-Snapshot-Cursor, Idempotent-Replayed"
)

// CORS returns middleware that allows exactly the configured web origin. It
// answers preflight requests with 204 and never enables credentials, matching
// the single-origin, bearer-token design (the token travels in the
// Authorization header, not a cookie).
func CORS(origin string) func(http.Handler) http.Handler {
	allowedOrigin := strings.TrimSpace(origin)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			originAllowed := allowedOrigin != "" && r.Header.Get("Origin") == allowedOrigin
			if originAllowed {
				header := w.Header()
				header.Set("Access-Control-Allow-Origin", allowedOrigin)
				header.Add("Vary", "Origin")
				header.Set("Access-Control-Expose-Headers", corsExposeHeaders)
			}

			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				if originAllowed {
					header := w.Header()
					header.Set("Access-Control-Allow-Methods", corsAllowMethods)
					header.Set("Access-Control-Allow-Headers", corsAllowHeaders)
					header.Set("Access-Control-Max-Age", "600")
				}
				// A preflight from a different origin gets no CORS headers; the
				// browser then blocks the actual request.
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
