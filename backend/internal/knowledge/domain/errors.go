// Package knowledge: domain-level errors shared by the application service and
// its storage adapters.
package knowledge

import "errors"

var (
	// ErrNotFound reports a missing knowledge item.
	ErrNotFound = errors.New("knowledge item not found")
	// ErrStateConflict reports a concurrent or illegal state change detected by
	// the storage adapter (expected revision/status no longer matches).
	ErrStateConflict = errors.New("knowledge item state conflict")
)
