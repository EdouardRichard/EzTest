package platform

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// DefaultMaxBodyBytes is the request body limit when an endpoint does not pass
// its own limit to DecodeJSON.
const DefaultMaxBodyBytes int64 = 1 << 20

var (
	// ErrBodyTooLarge is returned when the request body exceeds the limit.
	ErrBodyTooLarge = errors.New("platform: request body too large")
	// ErrInvalidJSON is returned when the body is not a single JSON object
	// (malformed input, unknown fields or trailing data).
	ErrInvalidJSON = errors.New("platform: invalid JSON body")
)

// DecodeJSON decodes a single JSON object from r.Body into dst. It caps the
// body at maxBytes (or DefaultMaxBodyBytes when maxBytes <= 0) and rejects
// unknown fields and trailing data, so a client cannot smuggle values past the
// endpoint's DTO. The body must be a JSON object: a null, array or scalar is
// rejected because every API request body is an object.
func DecodeJSON(r *http.Request, dst any, maxBytes int64) error {
	if r == nil || r.Body == nil {
		return fmt.Errorf("%w: empty body", ErrInvalidJSON)
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBodyBytes
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBytes+1))
	if err != nil {
		return fmt.Errorf("%w: read body: %v", ErrInvalidJSON, err)
	}
	if int64(len(body)) > maxBytes {
		return fmt.Errorf("%w: limit is %d bytes", ErrBodyTooLarge, maxBytes)
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("%w: body must be a JSON object", ErrInvalidJSON)
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidJSON, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: unexpected trailing data", ErrInvalidJSON)
	}
	return nil
}
