// Package requestid creates, validates and propagates X-Request-ID values.
package requestid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"sync/atomic"
	"time"
)

// Header is the request correlation header.
const Header = "X-Request-ID"

var pattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

type contextKey struct{}

var fallbackCounter atomic.Uint64

// New returns a fresh request ID. IDs are opaque, URL-safe and 20 characters.
func New() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// crypto/rand failures are practically impossible; keep the API usable.
		return fmt.Sprintf("req_%08x%08x", uint32(time.Now().UnixNano()), uint32(fallbackCounter.Add(1)))
	}
	return "req_" + hex.EncodeToString(raw[:])
}

// Valid reports whether an inbound request ID is acceptable to echo.
func Valid(id string) bool {
	return pattern.MatchString(id)
}

// With stores a request ID in the context.
func With(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// From reads the request ID from the context.
func From(ctx context.Context) string {
	id, _ := ctx.Value(contextKey{}).(string)
	return id
}
