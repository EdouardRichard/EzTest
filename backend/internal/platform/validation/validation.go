// Package validation holds dependency-free field validators.
package validation

import (
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

var httpMethods = map[string]struct{}{
	"GET": {}, "POST": {}, "PUT": {}, "PATCH": {}, "DELETE": {}, "HEAD": {}, "OPTIONS": {},
}

// String validates rune length and rejects blank values when min > 0.
func String(field, value string, min, max int) error {
	if min > 0 && strings.TrimSpace(value) == "" {
		return platform.Invalid(field, "must not be empty")
	}
	length := utf8.RuneCountInString(value)
	if length < min {
		return platform.Invalid(field, "must be at least "+strconv.Itoa(min)+" characters")
	}
	if max > 0 && length > max {
		return platform.Invalid(field, "must be at most "+strconv.Itoa(max)+" characters")
	}
	return nil
}

// URL validates an absolute http(s) URL with a host.
func URL(field, value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return platform.Invalid(field, "must be an absolute http(s) URL")
	}
	return nil
}

// HTTPMethod validates an uppercase HTTP method supported by the resource API.
func HTTPMethod(value string) error {
	if _, ok := httpMethods[value]; !ok {
		return platform.Invalid("method", "unsupported HTTP method")
	}
	return nil
}

// StringList validates a list of non-empty strings with an optional cap.
func StringList(field string, values []string, max int) error {
	if max > 0 && len(values) > max {
		return platform.Invalid(field, "must contain at most "+strconv.Itoa(max)+" items")
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return platform.Invalid(field, "must not contain empty items")
		}
	}
	return nil
}

// NonEmptyID parses a positive decimal integer ID.
func NonEmptyID(field, raw string) (int64, error) {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value <= 0 {
		return 0, platform.Invalid(field, "must be a positive decimal ID")
	}
	return value, nil
}
