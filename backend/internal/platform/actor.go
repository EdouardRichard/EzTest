package platform

import "context"

// Actor is the authenticated caller injected by the HTTP middleware.
type Actor struct {
	ID   int64
	Role string
}

// IsAdmin reports whether the actor has the admin role.
func (a Actor) IsAdmin() bool { return a.Role == "admin" }

type actorKey struct{}

// WithActor stores the authenticated actor in the context.
func WithActor(ctx context.Context, actor Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

// ActorFrom reads the authenticated actor from the context.
func ActorFrom(ctx context.Context) (Actor, bool) {
	actor, ok := ctx.Value(actorKey{}).(Actor)
	return actor, ok
}
