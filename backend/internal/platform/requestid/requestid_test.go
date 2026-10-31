package requestid

import (
	"context"
	"strings"
	"testing"
)

func TestNewIsPrefixedAndValid(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := New()
		if !strings.HasPrefix(id, "req_") {
			t.Fatalf("id = %q, want req_ prefix", id)
		}
		if !Valid(id) {
			t.Fatalf("id = %q is not valid", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestValid(t *testing.T) {
	valid := []string{"req_12345678", "client-request_id_1234", strings.Repeat("a", 128)}
	for _, id := range valid {
		if !Valid(id) {
			t.Fatalf("Valid(%q) = false", id)
		}
	}
	invalid := []string{"", "short", strings.Repeat("a", 129), "bad id", "bad/id"}
	for _, id := range invalid {
		if Valid(id) {
			t.Fatalf("Valid(%q) = true", id)
		}
	}
}

func TestContextRoundTrip(t *testing.T) {
	ctx := With(context.Background(), "req_12345678")
	if got := From(ctx); got != "req_12345678" {
		t.Fatalf("From = %q", got)
	}
	if got := From(context.Background()); got != "" {
		t.Fatalf("empty context From = %q", got)
	}
}
