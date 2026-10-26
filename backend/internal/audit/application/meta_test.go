package application_test

import (
	"context"
	"testing"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
)

// TestRequestMetaContext pins the request-metadata carrier the HTTP handlers
// use so the identity services can stamp RequestID, IP and User-Agent on the
// audit entries they already record, without changing their signatures.
func TestRequestMetaContext(t *testing.T) {
	meta := auditapp.RequestMeta{
		RequestID: "req_meta_1234",
		IP:        "203.0.113.7",
		UserAgent: "eztest/1.0",
	}
	ctx := auditapp.WithMeta(context.Background(), meta)
	if got := auditapp.MetaFromContext(ctx); got != meta {
		t.Fatalf("MetaFromContext=%+v want %+v", got, meta)
	}

	// A context without metadata carries none: the helpers must not invent a
	// request ID or an address.
	if got := auditapp.MetaFromContext(context.Background()); got != (auditapp.RequestMeta{}) {
		t.Fatalf("MetaFromContext(background)=%+v want the zero value", got)
	}
	if got := auditapp.MetaFromContext(nil); got != (auditapp.RequestMeta{}) {
		t.Fatalf("MetaFromContext(nil)=%+v want the zero value", got)
	}
}

// TestActorContext pins the authenticated-actor carrier. It is separate from
// RequestMeta because a failed login has request metadata but no actor.
func TestActorContext(t *testing.T) {
	base := context.Background()
	if _, ok := auditapp.ActorIDFromContext(base); ok {
		t.Fatal("ActorIDFromContext(background) reported an actor")
	}
	if _, ok := auditapp.ActorIDFromContext(nil); ok {
		t.Fatal("ActorIDFromContext(nil) reported an actor")
	}

	ctx := auditapp.WithActorID(base, 42)
	id, ok := auditapp.ActorIDFromContext(ctx)
	if !ok || id != 42 {
		t.Fatalf("ActorIDFromContext=%d,%v want 42,true", id, ok)
	}

	// A zero ID is still an explicit actor (id 0 is never assigned, but the
	// presence bit must not be confused with a zero value).
	zeroCtx := auditapp.WithActorID(base, 0)
	if id, ok := auditapp.ActorIDFromContext(zeroCtx); !ok || id != 0 {
		t.Fatalf("ActorIDFromContext(zero)=%d,%v want 0,true", id, ok)
	}
}
