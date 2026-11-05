package executionapp

import (
	"net/url"
	pathpkg "path"
	"strings"
)

// staticDiscoveryExtensions is the explicit static-resource denylist of R-EX-2
// (M8). It is checked on the concrete path before the hash route so an SPA
// route such as /app#/reports/42 is never mistaken for a static asset.
var staticDiscoveryExtensions = map[string]struct{}{
	".js": {}, ".mjs": {}, ".cjs": {}, ".css": {}, ".map": {},
	".png": {}, ".jpg": {}, ".jpeg": {}, ".gif": {}, ".svg": {}, ".webp": {},
	".ico": {}, ".bmp": {}, ".avif": {},
	".woff": {}, ".woff2": {}, ".ttf": {}, ".otf": {}, ".eot": {},
	".mp4": {}, ".webm": {}, ".mp3": {}, ".wav": {}, ".ogg": {}, ".m4a": {},
	".pdf": {},
}

// NormalizeResourceTemplate is the pure R-EX-2 normalizer of one discovered
// request. It returns the lowercased host and the resource template:
//
//   - page: the path plus, when present, a hash-route path
//     (/app#/reports/42 -> /app#/reports/{id}); the method is not part of the
//     template.
//   - api: "METHOD " + the path template (/api/orders/42 -> GET /api/orders/{id}).
//
// Pure numeric path segments and UUID segments become {id}; the query, userinfo,
// headers, cookies and bodies are never part of the output. Static resources
// (script/stylesheet/image/font/media extensions) are filtered with ok=false.
// It is a pure function so tests and other callers can normalize without a DB.
func NormalizeResourceTemplate(kind, method, rawURL string) (host, template string, ok bool) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != "page" && kind != "api" {
		return "", "", false
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Hostname() == "" {
		return "", "", false
	}
	rawPath := parsed.EscapedPath()
	if rawPath == "" {
		rawPath = "/"
	}
	path := normalizeDiscoveryRoute(rawPath)
	staticPath := path
	if index := strings.IndexByte(staticPath, '#'); index >= 0 {
		staticPath = staticPath[:index]
	}
	if isStaticDiscoveryPath(staticPath) {
		return "", "", false
	}
	host = strings.ToLower(parsed.Hostname())
	if kind == "page" {
		template = path
		if fragment := normalizeDiscoveryHashRoute(parsed.Fragment); fragment != "" {
			template += "#" + fragment
		}
		return host, template, true
	}
	verb := strings.ToUpper(strings.TrimSpace(method))
	if verb == "" {
		verb = "GET"
	}
	return host, verb + " " + path, true
}

// normalizeDiscoveryHashRoute keeps only a path-shaped hash route and drops its
// query values. A fragment such as "#section" is not a route and is discarded.
func normalizeDiscoveryHashRoute(fragment string) string {
	if !strings.HasPrefix(fragment, "/") {
		return ""
	}
	if index := strings.IndexByte(fragment, '?'); index >= 0 {
		fragment = fragment[:index]
	}
	return normalizeDiscoveryRoute(fragment)
}

// normalizeDiscoveryRoute replaces identifier segments with {id}. Matrix
// parameters (;jsessionid=...) are dropped. Percent-encoded characters stay
// escaped so a %3F in a path can never be mistaken for a query separator.
func normalizeDiscoveryRoute(route string) string {
	segments := strings.Split(route, "/")
	for i, segment := range segments {
		if index := strings.IndexByte(segment, ';'); index >= 0 {
			segment = segment[:index]
		}
		if isDiscoveryIDSegment(segment) {
			segment = "{id}"
		}
		segments[i] = segment
	}
	return strings.Join(segments, "/")
}

// isDiscoveryIDSegment reports whether one path segment is a pure numeric or
// UUID identifier. Everything else (names, slugs, empty trailing segments) is
// kept verbatim.
func isDiscoveryIDSegment(segment string) bool {
	if segment == "" {
		return false
	}
	if isDiscoveryNumeric(segment) {
		return true
	}
	return isDiscoveryUUID(segment)
}

func isDiscoveryNumeric(segment string) bool {
	for i := 0; i < len(segment); i++ {
		if segment[i] < '0' || segment[i] > '9' {
			return false
		}
	}
	return true
}

// isDiscoveryUUID matches the canonical 8-4-4-4-12 hexadecimal UUID shape.
func isDiscoveryUUID(segment string) bool {
	if len(segment) != 36 {
		return false
	}
	for i := 0; i < len(segment); i++ {
		switch i {
		case 8, 13, 18, 23:
			if segment[i] != '-' {
				return false
			}
		default:
			char := segment[i]
			if (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') {
				return false
			}
		}
	}
	return true
}

func isStaticDiscoveryPath(path string) bool {
	extension := strings.ToLower(pathpkg.Ext(path))
	if extension == "" {
		return false
	}
	_, static := staticDiscoveryExtensions[extension]
	return static
}

// isStaticDiscoveryMIME is the MIME half of the static filter: image/font/
// audio/video families and stylesheet/javascript payloads are never page or api
// discoveries even when the URL has no recognizable extension.
func isStaticDiscoveryMIME(mime string) bool {
	value := strings.ToLower(strings.TrimSpace(mime))
	if index := strings.IndexByte(value, ';'); index >= 0 {
		value = strings.TrimSpace(value[:index])
	}
	if value == "" {
		return false
	}
	switch {
	case strings.HasPrefix(value, "image/"),
		strings.HasPrefix(value, "font/"),
		strings.HasPrefix(value, "audio/"),
		strings.HasPrefix(value, "video/"):
		return true
	}
	switch value {
	case "text/css", "application/javascript", "text/javascript",
		"application/x-javascript", "application/wasm",
		"application/font-woff", "application/vnd.ms-fontobject":
		return true
	}
	return false
}
