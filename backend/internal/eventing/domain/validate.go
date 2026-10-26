package eventing

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"
)

// Envelope limits from design §7.2. Character limits are counted in runes, not
// bytes, so a multi-byte summary is not rejected early.
const (
	maxChannelRunes = 64
	maxTypeRunes    = 100
	maxSummaryRunes = 500
	maxDetailBytes  = 64 << 10
)

// ErrInvalidEvent means an event does not satisfy the frozen V1 envelope
// contract. HTTP callers map it to 400 invalid_argument rather than a 500.
var ErrInvalidEvent = errors.New("eventing: invalid event")

// Validate enforces the §7.2 envelope limits: channel, type and summary are
// bounded in runes and detail must be a valid JSON object of at most 64 KiB. An
// empty detail is valid because the store defaults it to the empty object the
// envelope requires.
func Validate(event NewEvent) error {
	if runes := utf8.RuneCountInString(event.Channel); runes > maxChannelRunes {
		return fmt.Errorf("%w: channel is %d characters, limit is %d", ErrInvalidEvent, runes, maxChannelRunes)
	}
	if runes := utf8.RuneCountInString(event.Type); runes > maxTypeRunes {
		return fmt.Errorf("%w: type is %d characters, limit is %d", ErrInvalidEvent, runes, maxTypeRunes)
	}
	if runes := utf8.RuneCountInString(event.Summary); runes > maxSummaryRunes {
		return fmt.Errorf("%w: summary is %d characters, limit is %d", ErrInvalidEvent, runes, maxSummaryRunes)
	}

	detail := bytes.TrimSpace(event.Detail)
	if len(detail) == 0 {
		return nil
	}
	if len(detail) > maxDetailBytes {
		return fmt.Errorf("%w: detail is %d bytes, limit is %d", ErrInvalidEvent, len(detail), maxDetailBytes)
	}
	if detail[0] != '{' || !json.Valid(detail) {
		return fmt.Errorf("%w: detail must be a valid JSON object", ErrInvalidEvent)
	}
	return nil
}
