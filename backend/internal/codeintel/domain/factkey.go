package codeintel

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"
)

// NormalizeRepoPath validates one repo-relative Git path and returns its
// slash-normalized form (spec §5.1/§6.1). Absolute paths, drive-letter paths,
// backslashes, ".." segments, control characters and symlink display markers
// ("path -> target") are rejected so a caller can never escape the repository.
// "." segments and repeated separators are cleaned; the path must stay
// non-empty and at most MaxPathChars characters.
func NormalizeRepoPath(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("%w: path is empty", ErrInvalid)
	}
	if len([]rune(value)) > MaxPathChars {
		return "", fmt.Errorf("%w: path exceeds %d characters", ErrInvalid, MaxPathChars)
	}
	if strings.ContainsRune(value, '\\') {
		return "", fmt.Errorf("%w: path must use forward slashes", ErrInvalid)
	}
	if strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("%w: path must be repo-relative", ErrInvalid)
	}
	if len(value) >= 2 && value[1] == ':' && isASCIIAlpha(value[0]) {
		return "", fmt.Errorf("%w: drive-letter path is not repo-relative", ErrInvalid)
	}
	if strings.Contains(value, " -> ") {
		return "", fmt.Errorf("%w: symlink display path is not supported", ErrInvalid)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("%w: path contains control characters", ErrInvalid)
		}
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return "", fmt.Errorf("%w: path escapes the repository root", ErrInvalid)
		}
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == "" || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", fmt.Errorf("%w: path is empty after normalization", ErrInvalid)
	}
	if len([]rune(cleaned)) > MaxPathChars {
		return "", fmt.Errorf("%w: path exceeds %d characters", ErrInvalid, MaxPathChars)
	}
	return cleaned, nil
}

// NormalizeAPIPath canonicalizes an HTTP API path for fact identity (spec
// §5.2): a scheme/host prefix is dropped, then the query and fragment are
// removed, repeated and trailing slashes are folded, and numeric, UUID and
// long-hex path segments are replaced with "{id}". It returns "" when the input
// cannot be normalized (empty, traversal or invalid characters).
func NormalizeAPIPath(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if scheme := strings.Index(value, "://"); scheme >= 0 {
		rest := value[scheme+3:]
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			value = rest[slash:]
		} else {
			value = "/"
		}
	}
	if cut := strings.IndexAny(value, "?#"); cut >= 0 {
		value = value[:cut]
	}
	segments := strings.Split(value, "/")
	normalized := make([]string, 0, len(segments))
	for _, segment := range segments {
		switch segment {
		case "", ".":
			continue
		case "..":
			return ""
		}
		if isIDPathSegment(segment) {
			normalized = append(normalized, "{id}")
			continue
		}
		for _, r := range segment {
			if r <= 0x20 || r == 0x7f || r == '\\' {
				return ""
			}
		}
		normalized = append(normalized, segment)
	}
	return "/" + strings.Join(normalized, "/")
}

// FactKey builds the normalized fact identity of one extracted fact (spec
// §5.2). It returns false for unsupported kinds, empty/invalid names or paths,
// invalid HTTP methods and keys over MaxFactKeyChars. API/client paths are
// always normalized with NormalizeAPIPath; when normalizedPath is empty the
// path is taken from name ("<METHOD> <path>" or "<path>").
func FactKey(kind EntityKind, repoPath, name, method, normalizedPath string) (string, bool) {
	var key string
	switch kind {
	case EntityRoute, EntityPage:
		route := strings.TrimSpace(name)
		if route == "" || !validRouteName(route) {
			return "", false
		}
		if !strings.HasPrefix(route, "/") {
			route = "/" + route
		}
		key = factKeyPrefix(kind) + ":" + route
	case EntityAPI, EntityClientCall:
		httpMethod, methodOK := normalizeHTTPMethod(method)
		candidate := strings.TrimSpace(normalizedPath)
		if candidate == "" {
			fields := strings.Fields(strings.TrimSpace(name))
			switch len(fields) {
			case 2:
				nameMethod, nameMethodOK := normalizeHTTPMethod(fields[0])
				if !nameMethodOK || (methodOK && nameMethod != httpMethod) {
					return "", false
				}
				httpMethod, methodOK = nameMethod, true
				candidate = fields[1]
			case 1:
				candidate = fields[0]
			default:
				return "", false
			}
		}
		if !methodOK {
			return "", false
		}
		normalized := NormalizeAPIPath(candidate)
		if normalized == "" {
			return "", false
		}
		key = factKeyPrefix(kind) + ":" + httpMethod + " " + normalized
	case EntityComponent, EntitySymbol:
		cleaned, err := NormalizeRepoPath(repoPath)
		if err != nil {
			return "", false
		}
		symbolName := strings.TrimSpace(name)
		if symbolName == "" || strings.ContainsAny(symbolName, "\x00\r\n") {
			return "", false
		}
		key = factKeyPrefix(kind) + ":" + cleaned + "#" + symbolName
	default:
		return "", false
	}
	if len([]rune(key)) > MaxFactKeyChars {
		return "", false
	}
	return key, true
}

// ExcerptHash hashes one selected evidence excerpt (spec §5.2): each line loses
// a trailing carriage return, lines are joined with a single LF and no trailing
// newline is added. Callers pass the lines of the 1-based closed interval
// [startLine, endLine] (line N is lines[N-1]).
func ExcerptHash(lines []string) string {
	normalized := make([]string, len(lines))
	for i, line := range lines {
		normalized[i] = strings.TrimSuffix(line, "\r")
	}
	sum := sha256.Sum256([]byte(strings.Join(normalized, "\n")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// factKeyPrefix returns the persisted fact-key prefix of one entity kind. The
// client_call entity kind uses the shorter "client" fact prefix (spec §5.2).
func factKeyPrefix(kind EntityKind) string {
	if kind == EntityClientCall {
		return "client"
	}
	return string(kind)
}

// validRouteName rejects route/page names that cannot be a stable fact identity.
func validRouteName(value string) bool {
	for _, r := range value {
		if r <= 0x20 || r == 0x7f || r == '\\' {
			return false
		}
	}
	return true
}

// normalizeHTTPMethod uppercases an HTTP method and validates its shape.
func normalizeHTTPMethod(method string) (string, bool) {
	value := strings.ToUpper(strings.TrimSpace(method))
	if value == "" {
		return "", false
	}
	for _, r := range value {
		if (r < 'A' || r > 'Z') && r != '-' {
			return "", false
		}
	}
	return value, true
}

// isIDPathSegment reports whether one API path segment is a concrete identifier
// that must collapse to "{id}": all digits, a UUID, or a long hex string.
func isIDPathSegment(segment string) bool {
	if segment == "" {
		return false
	}
	numeric := true
	for _, r := range segment {
		if r < '0' || r > '9' {
			numeric = false
			break
		}
	}
	if numeric {
		return true
	}
	if len(segment) == 36 {
		uuid := true
		for i, r := range segment {
			switch i {
			case 8, 13, 18, 23:
				if r != '-' {
					uuid = false
				}
			default:
				if !isHexRune(r) {
					uuid = false
				}
			}
			if !uuid {
				break
			}
		}
		if uuid {
			return true
		}
	}
	if len(segment) < 16 {
		return false
	}
	for _, r := range segment {
		if !isHexRune(r) {
			return false
		}
	}
	return true
}

func isHexRune(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

func isASCIIAlpha(r byte) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}
