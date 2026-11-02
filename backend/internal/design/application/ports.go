package designapp

import (
	"errors"
	"time"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// mapDesignError maps repository/domain sentinel errors onto the stable API
// error envelope. Already-mapped platform errors pass through.
func mapDesignError(err error, kind string) error {
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	switch {
	case errors.Is(err, design.ErrNotFound):
		return platform.NotFound(kind)
	case errors.Is(err, design.ErrStateConflict):
		return platform.StateConflict("the design entity is not in a valid state for this operation")
	case errors.Is(err, design.ErrDuplicate):
		return platform.Conflict("design entity already exists")
	default:
		return err
	}
}

// requirePositiveID validates internal IDs parsed by the HTTP layer.
func requirePositiveID(field string, value int64) error {
	if value <= 0 {
		return platform.Invalid(field, "must be a positive decimal ID")
	}
	return nil
}

// asInt64Ptr returns nil for a zero value so optional IDs stay optional.
func asInt64Ptr(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}

// derefInt64 returns 0 for a nil pointer.
func derefInt64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

// utcNow normalizes a timestamp for wire output.
func utcNow(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.UTC()
}
