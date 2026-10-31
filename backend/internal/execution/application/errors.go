package executionapp

import (
	"errors"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// mapExecutionError maps repository/domain sentinel errors onto the stable API
// error envelope. Already-mapped platform errors pass through.
func mapExecutionError(err error, kind string) error {
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	switch {
	case errors.Is(err, execution.ErrNotFound):
		return platform.NotFound(kind)
	case errors.Is(err, execution.ErrStateConflict):
		return platform.StateConflict("the execution entity is not in a valid state for this operation")
	case errors.Is(err, execution.ErrDuplicate):
		return platform.Conflict("the execution entity already exists")
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

// optionalID returns nil for a non-positive ID so optional columns stay NULL.
func optionalID(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}

// derefID returns 0 for a nil pointer.
func derefID(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

// uniqueSortedIDs normalizes a version_sut ID set: positive, unique, ascending.
func uniqueSortedIDs(ids []int64) ([]int64, error) {
	seen := make(map[int64]struct{}, len(ids))
	result := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, platform.Invalid("version_sut_ids", "must contain positive decimal IDs")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sortInt64(result)
	return result, nil
}

func sortInt64(values []int64) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
