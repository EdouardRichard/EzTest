package executor

import (
	"testing"
	"time"
)

func TestResolverCachesByEndpoint(t *testing.T) {
	resolver := NewResolver("token", time.Second)
	first := resolver.Client("http://127.0.0.1:13336")
	second := resolver.Client("http://127.0.0.1:13336")
	if first == nil || first != second {
		t.Fatalf("same endpoint returned different clients: %p %p", first, second)
	}
	other := resolver.Client("http://127.0.0.1:13346")
	if other == nil || other == first {
		t.Fatalf("distinct endpoints share one client: %p %p", other, first)
	}
}

func TestResolverEmptyEndpoint(t *testing.T) {
	resolver := NewResolver("token", time.Second)
	if client := resolver.Client(""); client != nil {
		t.Fatalf("empty endpoint client = %v, want nil", client)
	}
	var nilResolver *Resolver
	if client := nilResolver.Client("http://127.0.0.1:13336"); client != nil {
		t.Fatalf("nil resolver client = %v, want nil", client)
	}
}
