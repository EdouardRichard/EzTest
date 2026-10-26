package audit

import "context"

// Repository is the persistence port for audit entries. Append-only by design:
// there is no update or delete operation.
type Repository interface {
	// Record appends one entry. ID and CreatedAt are assigned by the
	// repository; the remaining fields are stored as given.
	Record(ctx context.Context, entry Entry) error
	// List returns up to limit entries with ID less than afterID, ordered by
	// ID descending (newest first). afterID <= 0 starts at the newest entry.
	// more reports whether at least one older entry exists.
	List(ctx context.Context, limit int, afterID int64) ([]Entry, bool, error)
}
