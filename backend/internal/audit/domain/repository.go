package audit

import (
	"context"
	"time"
)

// ListFilter narrows one page of audit entries. Zero fields are ignored: nil
// ActorUserID, nil From/To and an empty Action mean "no narrowing", so the
// unfiltered viewer request keeps its documented newest-first page.
type ListFilter struct {
	// ActorUserID selects the entries performed by one user. Nil means any.
	ActorUserID *int64
	// Action selects one exact action name. Empty means any.
	Action string
	// From and To bound CreatedAt inclusively. Nil means unbounded.
	From *time.Time
	To   *time.Time
}

// IsZero reports whether the filter narrows nothing.
func (f ListFilter) IsZero() bool {
	return f.ActorUserID == nil && f.Action == "" && f.From == nil && f.To == nil
}

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
	// ListFiltered is List narrowed by filter. A zero filter is equivalent to
	// List, so callers may always use the filtered form.
	ListFiltered(ctx context.Context, filter ListFilter, limit int, afterID int64) ([]Entry, bool, error)
}
