package platform

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	// DefaultLimit is the page size used when a list endpoint gets no limit.
	DefaultLimit = 20
	// MaxLimit is the largest page size a list endpoint accepts.
	MaxLimit = 100
)

var (
	// ErrInvalidLimit is returned for a limit outside 1–100 or not an integer.
	ErrInvalidLimit = errors.New("platform: invalid limit")
	// ErrInvalidCursor is returned for a cursor that is not a base64url id.
	ErrInvalidCursor = errors.New("platform: invalid cursor")
)

// ParseLimit parses the limit query parameter. An empty value means
// DefaultLimit; integers outside 1–MaxLimit are rejected.
func ParseLimit(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultLimit, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: %q is not an integer", ErrInvalidLimit, raw)
	}
	if limit < 1 || limit > MaxLimit {
		return 0, fmt.Errorf("%w: %d is outside 1-%d", ErrInvalidLimit, limit, MaxLimit)
	}
	return limit, nil
}

// EncodeCursor encodes an id as an opaque, URL-safe pagination cursor.
func EncodeCursor(id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(id, 10)))
}

// DecodeCursor decodes an opaque cursor into the id it refers to. An empty
// cursor means the first page and returns 0. Malformed cursors and negative ids
// are rejected.
func DecodeCursor(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		// Tolerate padded base64url too: cursors are opaque and copying one from
		// a URL should not turn into a 400.
		decoded, err = base64.URLEncoding.DecodeString(raw)
		if err != nil {
			return 0, fmt.Errorf("%w: not base64url", ErrInvalidCursor)
		}
	}
	id, err := strconv.ParseInt(string(decoded), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: not a decimal id", ErrInvalidCursor)
	}
	if id < 0 {
		return 0, fmt.Errorf("%w: negative id", ErrInvalidCursor)
	}
	return id, nil
}
