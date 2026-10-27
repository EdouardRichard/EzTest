package domain

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// NormalizeMethodPath normalises a method/path pair for cross-stack contract
// matching (design spec §6.5):
//
//  1. the method is upper-cased;
//  2. an absolute URL contributes only its path, query and fragment are
//     dropped, duplicate slashes are merged and a trailing slash is removed
//     (except for the root);
//  3. dynamic segments ({id}, :id, <id>, pure digits and UUIDs) become "{}";
//  4. case is preserved and no gateway-prefix inference is attempted.
//
// When method is empty and path starts with a standard HTTP method (for example
// "post /api/refund"), the method is lifted out of path. This keeps fact names
// from AI output usable without a second parse.
func NormalizeMethodPath(method, path string) (string, string) {
	method = strings.TrimSpace(method)
	path = strings.TrimSpace(path)
	if method == "" {
		if embedded, rest, ok := splitLeadingMethod(path); ok {
			method, path = embedded, rest
		}
	}
	return strings.ToUpper(method), normalizeContractPath(path)
}

// standardMethods are the methods recognized when an unlabelled path carries
// its own method prefix.
var standardMethods = map[string]struct{}{
	"GET": {}, "HEAD": {}, "POST": {}, "PUT": {}, "PATCH": {},
	"DELETE": {}, "CONNECT": {}, "OPTIONS": {}, "TRACE": {},
}

// splitLeadingMethod splits "POST /api/x" into its method and path parts.
func splitLeadingMethod(path string) (string, string, bool) {
	idx := strings.IndexFunc(path, unicode.IsSpace)
	if idx <= 0 {
		return "", "", false
	}
	method := strings.ToUpper(path[:idx])
	if _, ok := standardMethods[method]; !ok {
		return "", "", false
	}
	rest := strings.TrimSpace(path[idx:])
	if rest == "" {
		return "", "", false
	}
	return method, rest, true
}

// uuidSegment matches a canonical UUID path segment.
var uuidSegment = regexp.MustCompile(
	`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// normalizeContractPath applies the path rules of design spec §6.5.
func normalizeContractPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		if parsed, err := url.Parse(raw); err == nil && parsed.Path != "" {
			raw = parsed.Path
		}
	}
	if cut := strings.IndexAny(raw, "?#"); cut >= 0 {
		raw = raw[:cut]
	}

	segments := strings.Split(raw, "/")
	normalized := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment == "" {
			continue
		}
		if isDynamicSegment(segment) {
			segment = "{}"
		}
		normalized = append(normalized, segment)
	}
	if len(normalized) == 0 {
		return "/"
	}
	return "/" + strings.Join(normalized, "/")
}

// isDynamicSegment reports whether one path segment is a route parameter.
func isDynamicSegment(segment string) bool {
	if len(segment) >= 2 &&
		strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
		return true
	}
	if len(segment) >= 2 && strings.HasPrefix(segment, ":") {
		return true
	}
	if len(segment) >= 3 &&
		strings.HasPrefix(segment, "<") && strings.HasSuffix(segment, ">") {
		return true
	}
	if isDigits(segment) {
		return true
	}
	return uuidSegment.MatchString(segment)
}

// isDigits reports whether s is a non-empty run of ASCII digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
