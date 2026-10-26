package application

import "context"

// RequestMeta is the HTTP request metadata attached to audit entries by the
// interface layer. It deliberately holds only non-sensitive fields: the request
// ID, the peer IP and the User-Agent. Request bodies, credentials and headers
// are never captured, so a handler can attach it without leaking secrets.
type RequestMeta struct {
	RequestID string
	IP        string
	UserAgent string
}

// requestMetaContextKey is the private context key carrying RequestMeta.
type requestMetaContextKey struct{}

// WithMeta attaches meta to ctx so the identity services stamp it on the audit
// entries they record for the request. Services keep their signatures
// unchanged: the context is the transport for cross-cutting request metadata.
func WithMeta(ctx context.Context, meta RequestMeta) context.Context {
	return context.WithValue(ctx, requestMetaContextKey{}, meta)
}

// MetaFromContext returns the request metadata stored by WithMeta. A nil
// context or a context without metadata returns the zero value.
func MetaFromContext(ctx context.Context) RequestMeta {
	if ctx == nil {
		return RequestMeta{}
	}
	meta, _ := ctx.Value(requestMetaContextKey{}).(RequestMeta)
	return meta
}

// actorContextKey is the private context key carrying the authenticated user.
type actorContextKey struct{}

// WithActorID attaches the authenticated user ID to ctx so a service records
// the actor on audit entries that do not name one explicitly.
func WithActorID(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, actorContextKey{}, id)
}

// ActorIDFromContext returns the authenticated user ID stored by WithActorID.
// The boolean reports presence, so id 0 is never confused with "no actor".
func ActorIDFromContext(ctx context.Context) (int64, bool) {
	if ctx == nil {
		return 0, false
	}
	id, ok := ctx.Value(actorContextKey{}).(int64)
	return id, ok
}
