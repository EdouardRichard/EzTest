package design

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
)

// OptionalID maps the design context's zero-id "absent" convention onto a nil
// pointer. It belongs to the DOMAIN because the convention is a property of the
// value objects (Judgment.AgentRunID, Override.JudgmentID, export CreatedBy):
// zero means "no such entity", never "entity 0". The application (optional wire
// IDs) and the PostgreSQL store (SQL NULL) both project that one convention
// through this function instead of keeping their own copies (H-F10 T6).
func OptionalID(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}

// formatInt64 renders an ID without importing the snowflake package at every
// call site.
func formatInt64(value int64) string {
	return strconv.FormatInt(value, 10)
}

// randomHex returns n random bytes as lowercase hex.
func randomHex(n int) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("design: random byte count must be positive")
	}
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
