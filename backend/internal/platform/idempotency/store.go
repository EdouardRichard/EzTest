// Package idempotency makes write requests replay-safe. A client attaches an
// Idempotency-Key header to a write request; the first successful response is
// stored for 24 hours and replayed for retries with the same key, method and
// path. A retry with the same key but a different body is rejected with 409.
package idempotency

import (
	"context"
)

const (
	// MaxCachedResponseBytes is the largest response body the middleware
	// caches. The API contract only caches JSON responses strictly smaller
	// than 1 MiB.
	MaxCachedResponseBytes int64 = 1 << 20
	// MaxBufferedRequestBytes bounds how much of a write request body the
	// middleware buffers for hashing. It matches the largest documented P0
	// write body (the 10 MiB storage upload); larger requests are passed
	// through without replay so an authenticated client cannot make the
	// middleware buffer unbounded memory before the endpoint's own limit runs.
	MaxBufferedRequestBytes int64 = 10 << 20
)

// StoredResponse is a cached write response.
type StoredResponse struct {
	Status      int
	Body        []byte
	RequestHash string
}

// Store persists idempotency records. The middleware deliberately depends on
// this small port so tests can use an in-memory fake and the server can use
// PostgreSQL.
type Store interface {
	// Get returns the stored response for the user, key, method and path, or
	// (nil, nil) when none exists. Expired records are not returned.
	Get(ctx context.Context, userID int64, key, method, path string) (*StoredResponse, error)
	// Save records the response for the user, key, method and path. It must not
	// overwrite a live record: the first response wins.
	Save(ctx context.Context, userID int64, key, method, path, requestHash string, status int, body []byte) error
}
