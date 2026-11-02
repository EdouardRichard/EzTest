// Package pagination implements the shared limit/cursor rules.
package pagination

import (
	"strconv"
)

const (
	// DefaultLimit is used when the caller omits a limit.
	DefaultLimit = 20
	// MaxLimit caps every list endpoint.
	MaxLimit = 100
)

// Limit parses a limit, falling back to def and capping at max.
func Limit(raw string, def, max int) int {
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return def
	}
	if max > 0 && value > max {
		return max
	}
	return value
}

// Cursor parses a decimal ID cursor. An empty cursor means "from the start".
func Cursor(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, err
	}
	if value < 0 {
		return 0, strconv.ErrRange
	}
	return value, nil
}

// NextCursor returns the cursor for the next page, or "" when exhausted.
func NextCursor(hasMore bool, lastID int64) string {
	if !hasMore {
		return ""
	}
	return strconv.FormatInt(lastID, 10)
}
