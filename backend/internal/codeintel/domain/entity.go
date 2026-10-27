package domain

import (
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"
)

// Entity kinds from design spec §6.2.
const (
	KindFile       = "file"
	KindPage       = "page"
	KindRoute      = "route"
	KindAPI        = "api"
	KindClientCall = "client_call"
	KindComponent  = "component"
	KindModule     = "module"
	KindSymbol     = "symbol"
)

// Fact provenance from design spec §5.1.
const (
	SourceGit = "git"
	SourceAI  = "ai"
)

// Fact confidence levels from design spec §6.2.
const (
	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"
	ConfidenceLow    = "low"
)

// FactPrefixes maps an AI-produced entity kind to its fact_key prefix. file
// entities are T0 git facts and never carry a fact_key.
var factPrefixes = map[string]string{
	KindRoute:      "route:",
	KindPage:       "page:",
	KindAPI:        "api:",
	KindClientCall: "client:",
	KindComponent:  "component:",
	KindModule:     "symbol:",
	KindSymbol:     "symbol:",
}

// CodeEntity is one code entity snapshot at a commit: a T0 file fact or an
// AI-produced fact with its current version.
type CodeEntity struct {
	ID          int64
	RepoID      int64
	Commit      string
	Kind        string
	Path        string
	Name        string
	StartLine   *int
	EndLine     *int
	Signature   string
	BlobHash    string
	Source      string
	Confidence  string
	Evidence    []Evidence
	Meta        []byte // JSONB
	FactKey     *string
	FactVersion *int
	AnalysisID  *int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// FactKey returns the stable identity of one AI-produced fact (design spec
// §6.2):
//
//	route:<path>                      (leading slash added when missing)
//	page:<name>
//	api:<METHOD> <normalizedPath>
//	client:<METHOD> <normalizedPath>
//	component:<path>#<name>
//	symbol:<path>#<name>              (shared by module and symbol facts)
//
// Facts that cannot be normalised (empty name, missing method, invalid path or
// an over-long key) are rejected so the caller can drop them and count
// stats.invalidFacts.
func FactKey(kind, name, factPath string) (string, error) {
	kind = strings.TrimSpace(kind)
	prefix, ok := factPrefixes[kind]
	if !ok {
		return "", NewInvalid("kind", "invalid_fact",
			fmt.Sprintf("codeintel: kind %q has no fact identity", kind))
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", NewInvalid("name", "invalid_fact", "codeintel: fact name is required")
	}

	var key string
	switch kind {
	case KindRoute:
		key = prefix + ensureLeadingSlash(name)
	case KindPage:
		key = prefix + name
	case KindAPI, KindClientCall:
		method, rawPath, err := splitMethodPath(name)
		if err != nil {
			return "", err
		}
		method, rawPath = NormalizeMethodPath(method, rawPath)
		if rawPath == "" {
			return "", NewInvalid("name", "invalid_fact",
				fmt.Sprintf("codeintel: fact name %q has no path to normalize", name))
		}
		key = prefix + method + " " + rawPath
	case KindComponent, KindModule, KindSymbol:
		normalized, err := normalizeFactPath(factPath)
		if err != nil {
			return "", err
		}
		key = prefix + normalized + "#" + name
	}

	if len(key) > MaxFactKeyChars {
		return "", NewInvalid("name", "invalid_fact",
			fmt.Sprintf("codeintel: fact_key exceeds %d characters", MaxFactKeyChars))
	}
	return key, nil
}

// ensureLeadingSlash prefixes p with "/" unless it already starts with one.
func ensureLeadingSlash(p string) string {
	if strings.HasPrefix(p, "/") {
		return p
	}
	return "/" + p
}

// splitMethodPath splits an AI fact name such as "post /api/refund" into its
// method and path parts. Every api/client_call fact must name its method.
func splitMethodPath(name string) (string, string, error) {
	idx := strings.IndexFunc(name, unicode.IsSpace)
	if idx <= 0 {
		return "", "", NewInvalid("name", "invalid_fact",
			fmt.Sprintf("codeintel: fact name %q must start with an HTTP method", name))
	}
	method := name[:idx]
	rawPath := strings.TrimSpace(name[idx:])
	if rawPath == "" || !isMethodToken(method) {
		return "", "", NewInvalid("name", "invalid_fact",
			fmt.Sprintf("codeintel: fact name %q must start with an HTTP method", name))
	}
	return strings.ToUpper(method), rawPath, nil
}

// isMethodToken reports whether s looks like an HTTP method token.
func isMethodToken(s string) bool {
	if len(s) == 0 || len(s) > 16 {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return true
}

// normalizeFactPath turns a fact path into the canonical slash-separated
// repository-relative form and rejects absolute paths and escapes.
func normalizeFactPath(raw string) (string, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, `\`, "/"))
	if raw == "" {
		return "", NewInvalid("path", "invalid_fact", "codeintel: fact path is required")
	}
	if strings.HasPrefix(raw, "/") || path.IsAbs(raw) {
		return "", NewInvalid("path", "invalid_fact",
			fmt.Sprintf("codeintel: fact path %q must be repository-relative", raw))
	}
	cleaned := path.Clean(raw)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", NewInvalid("path", "invalid_fact",
			fmt.Sprintf("codeintel: fact path %q escapes the repository root", raw))
	}
	if strings.Contains(cleaned, "#") {
		return "", NewInvalid("path", "invalid_fact",
			fmt.Sprintf("codeintel: fact path %q must not contain '#'", raw))
	}
	return cleaned, nil
}
