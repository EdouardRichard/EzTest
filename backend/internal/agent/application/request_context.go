package application

import "context"

// requestIDContextKey is the private context key that carries the ID of the
// HTTP request that started a run into the run's own context tree.
type requestIDContextKey struct{}

// WithRequestID stores id in ctx as the originating request's ID. RunService
// attaches it to the context the runner and tool executor inherit, so audit
// entries recorded after the HTTP request returns stay attributable to the
// request that started the run. Only the request ID is propagated: the peer IP
// and User-Agent belong to the request, not to the run.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDContextKey{}, id)
}

// RequestIDFromContext returns the request ID stored by WithRequestID, or ""
// when the context carries none. A nil context is safe. The audit adapter uses
// it to stamp run-scoped entries that have no HTTP request metadata.
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	return requestID
}
