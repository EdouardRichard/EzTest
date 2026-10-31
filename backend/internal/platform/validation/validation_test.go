package validation

import (
	"strings"
	"testing"
)

func TestString(t *testing.T) {
	if err := String("title", "hello", 1, 10); err != nil {
		t.Fatalf("String() error = %v", err)
	}
	if err := String("title", "", 1, 10); err == nil {
		t.Fatal("empty string accepted")
	}
	if err := String("title", strings.Repeat("界", 11), 1, 10); err == nil {
		t.Fatal("overlong rune string accepted")
	}
	if err := String("title", "  ", 1, 10); err == nil {
		t.Fatal("whitespace-only string accepted")
	}
	if err := String("title", "hello", 1, 10); err != nil {
		t.Fatalf("String() error = %v", err)
	}
}

func TestURL(t *testing.T) {
	for _, raw := range []string{"https://example.com/a", "http://localhost:8080/x"} {
		if err := URL("url", raw); err != nil {
			t.Fatalf("URL(%q) error = %v", raw, err)
		}
	}
	for _, raw := range []string{"", "ftp://example.com", "example.com", "https://"} {
		if err := URL("url", raw); err == nil {
			t.Fatalf("URL(%q) accepted", raw)
		}
	}
}

func TestHTTPMethod(t *testing.T) {
	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"} {
		if err := HTTPMethod(method); err != nil {
			t.Fatalf("HTTPMethod(%q) error = %v", method, err)
		}
	}
	for _, method := range []string{"", "get", "TRACE"} {
		if err := HTTPMethod(method); err == nil {
			t.Fatalf("HTTPMethod(%q) accepted", method)
		}
	}
}

func TestStringList(t *testing.T) {
	if err := StringList("roles", []string{"admin", "ops"}, 4); err != nil {
		t.Fatalf("StringList() error = %v", err)
	}
	if err := StringList("roles", []string{"admin", ""}, 4); err == nil {
		t.Fatal("empty element accepted")
	}
	if err := StringList("roles", []string{"a", "b", "c"}, 2); err == nil {
		t.Fatal("overlong list accepted")
	}
}

func TestNonEmptyID(t *testing.T) {
	if got, err := NonEmptyID("id", "123"); err != nil || got != 123 {
		t.Fatalf("NonEmptyID(123) = %d, %v", got, err)
	}
	for _, raw := range []string{"", "0", "-1", "abc"} {
		if _, err := NonEmptyID("id", raw); err == nil {
			t.Fatalf("NonEmptyID(%q) accepted", raw)
		}
	}
}
