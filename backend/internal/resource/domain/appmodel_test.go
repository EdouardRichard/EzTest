package domain

import (
	"strings"
	"testing"
)

func TestAppModelKeyMustMatchKind(t *testing.T) {
	if err := ValidateAppModelKey("api", "route:/orders"); err == nil {
		t.Fatal("want error")
	}
}

func TestValidateAppModelKeyAcceptsMatchingKinds(t *testing.T) {
	cases := map[string]string{
		"page":      "page:/orders",
		"route":     "route:/orders",
		"api":       "api:GET /api/orders",
		"component": "component:OrderList",
	}
	for kind, key := range cases {
		if err := ValidateAppModelKey(kind, key); err != nil {
			t.Fatalf("kind=%q key=%q: %v", kind, key, err)
		}
	}
}

func TestValidateAppModelKeyRejectsInvalidKeys(t *testing.T) {
	cases := []struct{ kind, key string }{
		{"page", "route:/orders"},
		{"route", "page:/orders"},
		{"component", "page:/orders"},
		{"api", "api:gEt /api/orders"},
		{"api", "api:GET"},
		{"page", "page:"},
		{"page", "orders"},
		{"page", "page:" + strings.Repeat("x", 512)},
		{"", "page:/orders"},
	}
	for _, tc := range cases {
		if err := ValidateAppModelKey(tc.kind, tc.key); err == nil {
			t.Fatalf("kind=%q key=%q: want error", tc.kind, tc.key)
		}
	}
}

func TestValidateResourceKey(t *testing.T) {
	for _, key := range []string{"shop", "shop-1", "shop_1", "a1", strings.Repeat("a", 64)} {
		if err := ValidateResourceKey(key); err != nil {
			t.Fatalf("key=%q: %v", key, err)
		}
	}
	for _, key := range []string{"", "a", "Shop", "shop key", "-shop", strings.Repeat("a", 65)} {
		if err := ValidateResourceKey(key); err == nil {
			t.Fatalf("key=%q: want error", key)
		}
	}
}
