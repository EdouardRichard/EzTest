package domain

import (
	"encoding/json"
	"regexp"
	"time"
)

// App model node kinds, mirrored from the app_model_node.kind CHECK
// constraint.
const (
	AppModelKindPage      = "page"
	AppModelKindRoute     = "route"
	AppModelKindAPI       = "api"
	AppModelKindComponent = "component"
)

// maxAppModelKeyLength bounds app model keys (spec 搂6.6).
const maxAppModelKeyLength = 512

// appModelAPIKeyRE matches the "api:<METHOD> <path>" key convention.
var appModelAPIKeyRE = regexp.MustCompile(`^[A-Z]+ \S.*$`)

// AppModelNode is one element of an application model. Keys and kinds are
// immutable after creation; ParentID is nil for a root node. Meta is opaque
// JSON stored as JSONB.
type AppModelNode struct {
	ID        int64
	SystemID  int64
	ParentID  *int64
	Kind      string
	Key       string
	Name      string
	Path      string
	Method    string
	Source    string
	Meta      json.RawMessage
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AppModelFilter selects a page of nodes ordered by ID ascending. An empty Kind
// or ParentKey means "any".
type AppModelFilter struct {
	SystemID  int64
	Kind      string
	ParentKey string
	Page      Page
}

// ValidateAppModelKey enforces the key/kind convention of spec 搂6.6:
// page:<path>, route:<path>, api:<METHOD> <path>, component:<Name>.
func ValidateAppModelKey(kind, key string) error {
	if len(key) == 0 || len(key) > maxAppModelKeyLength {
		return NewInvalid("key", "invalid_app_model_key",
			"app model key must be between 1 and 512 bytes")
	}
	switch kind {
	case AppModelKindPage:
		if !hasKeyPrefix(key, "page:") {
			return NewInvalid("key", "invalid_app_model_key", "page keys must start with page:")
		}
	case AppModelKindRoute:
		if !hasKeyPrefix(key, "route:") {
			return NewInvalid("key", "invalid_app_model_key", "route keys must start with route:")
		}
	case AppModelKindComponent:
		if !hasKeyPrefix(key, "component:") {
			return NewInvalid("key", "invalid_app_model_key", "component keys must start with component:")
		}
	case AppModelKindAPI:
		suffix, ok := cutKeyPrefix(key, "api:")
		if !ok || !appModelAPIKeyRE.MatchString(suffix) {
			return NewInvalid("key", "invalid_app_model_key", "api keys must be api:<METHOD> <path>")
		}
	default:
		return NewInvalid("kind", "invalid_app_model_key",
			"app model kind must be page, route, api or component")
	}
	return nil
}

// hasKeyPrefix reports whether key starts with prefix and has a non-empty
// suffix.
func hasKeyPrefix(key, prefix string) bool {
	_, ok := cutKeyPrefix(key, prefix)
	return ok
}

// cutKeyPrefix returns key without prefix when it starts with prefix and the
// remaining value is non-empty.
func cutKeyPrefix(key, prefix string) (string, bool) {
	if len(key) <= len(prefix) || key[:len(prefix)] != prefix {
		return "", false
	}
	return key[len(prefix):], true
}
