package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// userContextKey is the private context key carrying the authenticated user.
type userContextKey struct{}

// UserFromContext returns the user stored by AuthMiddleware.
func UserFromContext(ctx context.Context) (identity.User, bool) {
	user, ok := ctx.Value(userContextKey{}).(identity.User)
	return user, ok
}

// AuthMiddleware resolves the Authorization bearer token and stores the user in
// the request context. A missing, malformed, unknown, revoked or expired token
// gets 401 with a Bearer challenge. Any other Authenticate error (typically a
// database outage) is a server failure: it gets the shared 503 unavailable
// envelope without the challenge, so clients keep their credentials and retry
// instead of treating a valid session as invalid.
func AuthMiddleware(authenticator Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rawToken, ok := bearerToken(r)
			if !ok {
				writeUnauthenticated(w, r)
				return
			}
			user, err := authenticator.Authenticate(r.Context(), rawToken)
			if err != nil {
				if errors.Is(err, identity.ErrUnauthenticated) {
					writeUnauthenticated(w, r)
					return
				}
				// Never log the bearer token: only the request id and the
				// underlying server-side failure are recorded.
				slog.ErrorContext(r.Context(), "auth: authenticate request",
					"request_id", platform.RequestID(r.Context()), "error", err)
				platform.WriteError(w, r, http.StatusServiceUnavailable, platform.CodeUnavailable,
					"authentication is temporarily unavailable")
				return
			}
			ctx := context.WithValue(r.Context(), userContextKey{}, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireAdmin rejects authenticated users that are not administrators. It must
// run after AuthMiddleware.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := UserFromContext(r.Context())
		if !ok || user.Role != identity.RoleAdmin {
			platform.WriteError(w, r, http.StatusForbidden, platform.CodeForbidden, "admin role required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearerToken extracts a non-empty token from `Authorization: Bearer <token>`.
// The scheme is matched case-insensitively, as RFC 7235 requires.
func bearerToken(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}

// writeUnauthenticated writes the shared 401 response, including the Bearer
// challenge required by the API contract.
func writeUnauthenticated(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	platform.WriteError(w, r, http.StatusUnauthorized, platform.CodeUnauthenticated, "authentication required")
}
