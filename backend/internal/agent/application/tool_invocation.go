package application

import "context"

// Invocation is the run-scoped identity of one tool call. The tool executor
// attaches it to the context handed to Tool.Execute, so P3 tool adapters read
// the actor, session and profile from the runtime instead of trusting
// model-supplied arguments (design spec §8.2 item 3, §8.7).
type Invocation struct {
	SessionID   int64
	RunID       int64
	ActorUserID *int64
	ProfileKey  string
}

// toolInvocationContextKey is the private context key carrying an Invocation.
type toolInvocationContextKey struct{}

// WithToolInvocation returns a context carrying inv. Tool implementations read
// it with InvocationFromContext. The executor calls it before Tool.Execute, so
// callers normally do not need to.
func WithToolInvocation(ctx context.Context, inv Invocation) context.Context {
	return context.WithValue(ctx, toolInvocationContextKey{}, inv)
}

// InvocationFromContext returns the invocation stored by WithToolInvocation.
// A nil context or a context without an invocation returns the zero value and
// false. The pointer field is shared with the stored value; callers treat an
// invocation as read-only.
func InvocationFromContext(ctx context.Context) (Invocation, bool) {
	if ctx == nil {
		return Invocation{}, false
	}
	invocation, ok := ctx.Value(toolInvocationContextKey{}).(Invocation)
	return invocation, ok
}
