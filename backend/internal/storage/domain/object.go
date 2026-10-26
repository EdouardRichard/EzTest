// Package storage is the object-storage bounded context's domain model. An
// object is immutable and content-addressed: its identity is the lowercase hex
// SHA-256 of its bytes.
package storage

import (
	"crypto/sha256"
	"errors"
)

// Domain errors. The HTTP layer maps them to the shared error envelope:
// ErrInvalidSHA256 and ErrInvalidContentType are invalid_argument,
// ErrObjectNotFound is not_found and ErrCorrupted is storage_corrupted.
var (
	// ErrObjectNotFound is returned when no object is stored for a hash.
	ErrObjectNotFound = errors.New("storage: object not found")
	// ErrCorrupted is returned when stored bytes no longer hash to their
	// content-addressed id.
	ErrCorrupted = errors.New("storage: object content does not match its sha256")
	// ErrInvalidSHA256 is returned for ids that are not 64 lowercase hex
	// characters. Validating the id before touching the filesystem also
	// prevents path traversal.
	ErrInvalidSHA256 = errors.New("storage: invalid sha256")
	// ErrInvalidContentType is returned when an upload does not declare the
	// Content-Type the object is stored with.
	ErrInvalidContentType = errors.New("storage: content type is required")
)

// Object is one immutable, content-addressed stored object. SHA256 is the
// lowercase hex digest and doubles as the object id. RelativePath is the
// slash-separated path below the store root (for example "ab/cd/<sha256>").
// ContentType is the media type declared when the object was uploaded.
type Object struct {
	SHA256       string
	Size         int64
	ContentType  string
	RelativePath string
}

// ValidateSHA256 reports whether id is exactly 64 lowercase hexadecimal
// characters, the shape the store uses for object ids and paths.
func ValidateSHA256(id string) error {
	if len(id) != sha256.Size*2 {
		return ErrInvalidSHA256
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ErrInvalidSHA256
		}
	}
	return nil
}
