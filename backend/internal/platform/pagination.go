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
	// ErrInvalidLimit is the sentinel wrapped by every LimitError.
	ErrInvalidLimit = errors.New("platform: invalid limit")
	// ErrInvalidCursor is returned for a cursor that is not a base64url id.
	ErrInvalidCursor = errors.New("platform: invalid cursor")
)

// LimitError reasons distinguish the two rejection classes of the limit
// contract (spec §5.6 C6).
const (
	// LimitReasonNotInteger marks a value that is not a decimal integer.
	LimitReasonNotInteger = "not_integer"
	// LimitReasonOutOfRange marks an integer outside the accepted range.
	LimitReasonOutOfRange = "out_of_range"
)

// LimitError is returned by ParseLimit for a value that is not an integer or
// that is outside the accepted range. Its message is the user-facing limit
// contract wording, and Min/Max let the HTTP layers render the shared
// details{field,min,max} of the error envelope without hard-coding the bounds.
type LimitError struct {
	Raw    string
	Value  int
	Reason string
	Min    int
	Max    int
}

// Error returns the user-facing message: a non-integer and an out-of-range
// value must stay distinguishable (spec §5.6 C6).
func (e *LimitError) Error() string {
	if e == nil {
		return ErrInvalidLimit.Error()
	}
	if e.Reason == LimitReasonNotInteger {
		return "limit must be an integer"
	}
	return fmt.Sprintf("limit must be between %d and %d", e.Min, e.Max)
}

// Unwrap reports ErrInvalidLimit so errors.Is keeps matching every limit
// rejection.
func (e *LimitError) Unwrap() error { return ErrInvalidLimit }

// Details returns the structured envelope details shared by the HTTP layers: the
// field and its inclusive bounds. The stable details.reason stays owned by each
// HTTP context because version limits stay 400 invalid_limit while delivery
// stays 422 invalid_argument.
func (e *LimitError) Details() map[string]any {
	if e == nil {
		return map[string]any{}
	}
	return map[string]any{"field": "limit", "min": e.Min, "max": e.Max}
}

// ParseLimit parses the limit query parameter. An empty value means
// DefaultLimit; non-integers and integers outside 1–MaxLimit are rejected with
// a *LimitError carrying the contract bounds.
func ParseLimit(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultLimit, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil {
		return 0, &LimitError{Raw: raw, Reason: LimitReasonNotInteger, Min: 1, Max: MaxLimit}
	}
	if limit < 1 || limit > MaxLimit {
		return 0, &LimitError{
			Raw: raw, Value: limit, Reason: LimitReasonOutOfRange, Min: 1, Max: MaxLimit,
		}
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
