package storage

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound reports a missing content object or reference.
var ErrNotFound = errors.New("storage: not found")

// ErrCorrupt reports stored bytes that no longer match their SHA-256 object
// key. It is an internal integrity failure, never a client error.
var ErrCorrupt = errors.New("storage: content hash mismatch")

// Ref is one reference from an owning entity to a content object. DeletedAt is
// set by soft deletion; refs are never hard-deleted in P0.
type Ref struct {
	ID        int64
	ObjectID  string
	OwnerKind string
	OwnerID   int64
	Role      string
	CreatedAt time.Time
	DeletedAt *time.Time
}

// Repository persists content objects and references.
type Repository interface {
	// PutObject inserts an object unless the SHA-256 already exists and returns
	// the stored object plus whether this call created it.
	PutObject(ctx context.Context, object Object) (Object, bool, error)
	// GetObject returns the object for a SHA-256 or ErrNotFound.
	GetObject(ctx context.Context, sha256 string) (Object, error)
	// AddRef inserts a reference with the caller-supplied snowflake ID.
	AddRef(ctx context.Context, ref Ref) (Ref, error)
	// ReplaceRef makes ref the single active reference for
	// (owner_kind, owner_id, role): an existing active row is updated in place
	// (or reused when it already points at the same object), and any other
	// active duplicates are soft-deleted. Concurrent calls serialize on a
	// transaction advisory lock.
	ReplaceRef(ctx context.Context, ref Ref) (Ref, error)
	// ListRefs returns the active (not deleted) references of one owner.
	ListRefs(ctx context.Context, ownerKind string, ownerID int64) ([]Ref, error)
	// SoftDeleteRef marks a reference deleted; a missing/already-deleted ref is
	// ErrNotFound.
	SoftDeleteRef(ctx context.Context, id int64) error
}
