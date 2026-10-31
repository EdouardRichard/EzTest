package design

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
)

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
