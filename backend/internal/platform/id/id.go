// Package id generates opaque identifiers and request IDs.
package id

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// New returns a 32-byte cryptographically random identifier encoded as raw
// base64url (43 characters, no padding).
func New() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("id: crypto/rand failed: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// RequestID returns a random RFC 4122 version 4 UUID for request tracing.
func RequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("id: crypto/rand failed: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
