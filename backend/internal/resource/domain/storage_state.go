package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Storage state validation bounds from spec §6.2.
const (
	maxCookies           = 2000
	maxCookieName        = 256
	maxCookieValue       = 4096
	maxCookieDomain      = 253
	maxCookiePath        = 1024
	maxOrigins           = 500
	maxOriginLength      = 2048
	maxLocalStorageItems = 5000
	maxLocalStorageValue = 8192
	maxTokenHeaders      = 50
	maxTokenHeaderValue  = 8192
)

// tokenHeaderNameRE is the documented token header-name format (spec §6.2).
var tokenHeaderNameRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// forbiddenTokenHeaders may never be supplied by a client: they are set by the
// HTTP transport and smuggling them would break request framing.
var forbiddenTokenHeaders = map[string]struct{}{
	"host":           {},
	"content-length": {},
}

// StorageState is a parsed and structurally validated session login state. For
// kind "storage_state" Cookies/Origins are used; for kind "token" Headers is
// used. The state is stored in plaintext by decision D28.
type StorageState struct {
	Kind    string            `json:"kind,omitempty"`
	Cookies []Cookie          `json:"cookies,omitempty"`
	Origins []Origin          `json:"origins,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Cookie is one browser cookie. Expires is Unix seconds; -1 or 0 means a
// session cookie.
type Cookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Domain   string `json:"domain"`
	Path     string `json:"path"`
	Expires  int64  `json:"expires"`
	HTTPOnly bool   `json:"httpOnly"`
	Secure   bool   `json:"secure"`
	SameSite string `json:"sameSite"`
}

// Origin is one browser origin with its localStorage entries.
type Origin struct {
	Origin       string             `json:"origin"`
	LocalStorage []LocalStorageItem `json:"localStorage"`
}

// LocalStorageItem is one localStorage key/value pair.
type LocalStorageItem struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ParseStorageState validates raw against the import format of spec §6.2 and
// returns the normalized state. kind is either "storage_state" or "token". The
// caller passes the error to WriteServiceError: it is a 422
// invalid_storage_state with the offending field.
func ParseStorageState(kind string, raw json.RawMessage) (*StorageState, error) {
	switch kind {
	case StorageStateKindStorageState:
		return parseStorageStateJSON(raw)
	case StorageStateKindToken:
		return parseTokenJSON(raw)
	default:
		return nil, NewInvalid("kind", "invalid_storage_state",
			`kind must be "storage_state" or "token"`)
	}
}

// parseStorageStateJSON validates a browser storage_state document.
func parseStorageStateJSON(raw json.RawMessage) (*StorageState, error) {
	if !isJSONObject(raw) {
		return nil, invalidStorageState("storageState", "storageState must be a JSON object")
	}
	var doc struct {
		Cookies []Cookie `json:"cookies"`
		Origins []Origin `json:"origins"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, invalidStorageState("storageState", "storageState is not valid JSON")
	}
	if len(doc.Cookies) > maxCookies {
		return nil, invalidStorageState("storageState.cookies",
			fmt.Sprintf("at most %d cookies are allowed", maxCookies))
	}
	for i, cookie := range doc.Cookies {
		field := fmt.Sprintf("storageState.cookies[%d]", i)
		switch {
		case cookie.Name == "":
			return nil, invalidStorageState(field+".name", "cookie name must not be empty")
		case len(cookie.Name) > maxCookieName:
			return nil, invalidStorageState(field+".name",
				fmt.Sprintf("cookie name must be at most %d bytes", maxCookieName))
		case len(cookie.Value) > maxCookieValue:
			return nil, invalidStorageState(field+".value",
				fmt.Sprintf("cookie value must be at most %d bytes", maxCookieValue))
		case len(cookie.Domain) > maxCookieDomain:
			return nil, invalidStorageState(field+".domain",
				fmt.Sprintf("cookie domain must be at most %d bytes", maxCookieDomain))
		case len(cookie.Path) > maxCookiePath:
			return nil, invalidStorageState(field+".path",
				fmt.Sprintf("cookie path must be at most %d bytes", maxCookiePath))
		}
	}
	if len(doc.Origins) > maxOrigins {
		return nil, invalidStorageState("storageState.origins",
			fmt.Sprintf("at most %d origins are allowed", maxOrigins))
	}
	for i, origin := range doc.Origins {
		field := fmt.Sprintf("storageState.origins[%d]", i)
		if err := validateOriginURL(origin.Origin); err != nil {
			return nil, invalidStorageState(field+".origin", err.Error())
		}
		if len(origin.LocalStorage) > maxLocalStorageItems {
			return nil, invalidStorageState(field+".localStorage",
				fmt.Sprintf("at most %d localStorage items are allowed", maxLocalStorageItems))
		}
		for j, item := range origin.LocalStorage {
			itemField := fmt.Sprintf("%s.localStorage[%d]", field, j)
			if len(item.Name) > maxLocalStorageValue {
				return nil, invalidStorageState(itemField+".name",
					fmt.Sprintf("localStorage name must be at most %d bytes", maxLocalStorageValue))
			}
			if len(item.Value) > maxLocalStorageValue {
				return nil, invalidStorageState(itemField+".value",
					fmt.Sprintf("localStorage value must be at most %d bytes", maxLocalStorageValue))
			}
		}
	}
	if doc.Cookies == nil {
		doc.Cookies = []Cookie{}
	}
	if doc.Origins == nil {
		doc.Origins = []Origin{}
	}
	return &StorageState{Kind: StorageStateKindStorageState, Cookies: doc.Cookies, Origins: doc.Origins}, nil
}

// parseTokenJSON validates a token document. The HTTP API sends the headers
// next to kind, so both {"headers":{...}} and a bare {header: value} object are
// accepted.
func parseTokenJSON(raw json.RawMessage) (*StorageState, error) {
	if !isJSONObject(raw) {
		return nil, invalidStorageState("storageState", "token state must be a JSON object")
	}
	var wrapper struct {
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil, invalidStorageState("storageState.headers", "token headers must be a JSON object of strings")
	}
	headers := wrapper.Headers
	if headers == nil {
		var bare map[string]string
		if err := json.Unmarshal(raw, &bare); err != nil {
			return nil, invalidStorageState("storageState.headers", "token headers must be a JSON object of strings")
		}
		headers = bare
	}
	if len(headers) > maxTokenHeaders {
		return nil, invalidStorageState("storageState.headers",
			fmt.Sprintf("at most %d headers are allowed", maxTokenHeaders))
	}
	for name, value := range headers {
		field := "storageState.headers." + name
		if !tokenHeaderNameRE.MatchString(name) {
			return nil, invalidStorageState(field,
				"header names must be 1-64 characters of letters, digits or '-'")
		}
		if _, forbidden := forbiddenTokenHeaders[strings.ToLower(name)]; forbidden {
			return nil, invalidStorageState(field, "header may not be set by the client")
		}
		if len(value) > maxTokenHeaderValue {
			return nil, invalidStorageState(field,
				fmt.Sprintf("header value must be at most %d bytes", maxTokenHeaderValue))
		}
		if strings.ContainsAny(value, "\r\n") {
			return nil, invalidStorageState(field, "header value must not contain CR or LF")
		}
	}
	return &StorageState{Kind: StorageStateKindToken, Headers: headers}, nil
}

// validateOriginURL checks that raw is an absolute http(s) URL of at most
// maxOriginLength bytes.
func validateOriginURL(raw string) error {
	if len(raw) > maxOriginLength {
		return fmt.Errorf("origin must be at most %d bytes", maxOriginLength)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("origin is not a valid URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("origin must use the http or https scheme")
	}
	if parsed.Host == "" {
		return fmt.Errorf("origin must include a host")
	}
	return nil
}

// EarliestExpiry returns the earliest positive cookie expiry and reports
// whether one exists. Expiries at or before now are returned unchanged so the
// caller can reject the storage state with storage_state_expired (spec §6.2);
// now is the reference time for that comparison.
func (s *StorageState) EarliestExpiry(now time.Time) (time.Time, bool) {
	if s == nil {
		return time.Time{}, false
	}
	var earliest time.Time
	found := false
	for _, cookie := range s.Cookies {
		if cookie.Expires <= 0 {
			continue
		}
		expires := time.Unix(cookie.Expires, 0).UTC()
		if !found || expires.Before(earliest) {
			earliest = expires
			found = true
		}
	}
	if !found {
		return time.Time{}, false
	}
	// Keep the earliest value even when it is already in the past: the caller
	// compares it with now and rejects an expired import.
	return earliest, true
}

// HashJSON returns the lowercase hex SHA-256 of v's canonical JSON encoding
// (encoding/json sorts map keys and uses struct field order). It is used for
// storage_state_hash and permission_hash; it is not encryption.
func HashJSON(v any) (string, error) {
	encoded, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// isJSONObject reports whether raw is a JSON object (rather than null, an
// array or a scalar).
func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

// invalidStorageState returns the shared 422 invalid_storage_state error.
func invalidStorageState(field, message string) *Error {
	return NewInvalid(field, "invalid_storage_state", message)
}
