package audit

import "context"

// Filter narrows one page of audit entries. Zero fields are ignored: a nil
// ActorID and an empty Action mean "no narrowing".
type Filter struct {
	// Action selects one exact action name. Empty means any.
	Action string
	// ActorID selects the entries performed by one user. Nil means any.
	ActorID *int64
}

// Repository is the append-only persistence port for audit entries. There is
// deliberately no update or delete operation.
type Repository interface {
	// Insert appends one entry. The application assigns ID and CreatedAt.
	Insert(ctx context.Context, entry Entry) error
	// List returns up to limit entries with ID less than beforeID, ordered by
	// ID descending (newest first). A nil beforeID starts at the newest entry.
	List(ctx context.Context, filter Filter, limit int, beforeID *int64) ([]Entry, error)
}
