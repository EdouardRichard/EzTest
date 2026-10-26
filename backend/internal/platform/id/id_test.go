package id

import (
	"encoding/base64"
	"regexp"
	"testing"
)

func TestNewReturnsRandom32ByteBase64URL(t *testing.T) {
	seen := make(map[string]struct{}, 64)
	for i := 0; i < 64; i++ {
		got := New()
		if _, ok := seen[got]; ok {
			t.Fatalf("New() returned duplicate %q", got)
		}
		seen[got] = struct{}{}
		raw, err := base64.RawURLEncoding.DecodeString(got)
		if err != nil {
			t.Fatalf("New()=%q is not raw base64url: %v", got, err)
		}
		if len(raw) != 32 {
			t.Fatalf("New()=%q decodes to %d bytes, want 32", got, len(raw))
		}
	}
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestRequestIDIsUUID(t *testing.T) {
	seen := make(map[string]struct{}, 64)
	for i := 0; i < 64; i++ {
		got := RequestID()
		if !uuidPattern.MatchString(got) {
			t.Fatalf("RequestID()=%q is not a UUID", got)
		}
		if _, ok := seen[got]; ok {
			t.Fatalf("RequestID() returned duplicate %q", got)
		}
		seen[got] = struct{}{}
	}
}
