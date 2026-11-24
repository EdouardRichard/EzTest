// Package validation holds dependency-free field validators.
package validation

import (
	"net/url"
	"regexp"
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

// gitCommitSHAPattern matches a lowercase 40-hex Git commit SHA. It is the one
// authority for the shape stored by the sut_repository.code_baseline_commit
// CHECK constraint, so the application validators and the database cannot
// disagree.
var gitCommitSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// GitCommitSHA validates a resolved Git commit SHA. An empty value is accepted
// and means "not resolved yet", matching the nullable database column; any
// other value must be a lowercase 40-hex SHA.
func GitCommitSHA(field, value string) error {
	if value == "" {
		return nil
	}
	if !gitCommitSHAPattern.MatchString(value) {
		return platform.Invalid(field, "must be empty or a lowercase 40-hex commit SHA")
	}
	return nil
}
