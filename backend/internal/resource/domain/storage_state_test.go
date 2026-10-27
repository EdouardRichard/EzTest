package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseStorageStateAcceptsValidState(t *testing.T) {
	raw := json.RawMessage(`{
		"kind": "storage_state",
		"cookies": [{"name":"sid","value":"abc","domain":"fixture.local","path":"/",
			"expires":1760000000,"httpOnly":true,"secure":false,"sameSite":"Lax"}],
		"origins": [{"origin":"http://localhost:13335",
			"localStorage":[{"name":"k","value":"v"}]}]
	}`)
	got, err := ParseStorageState("storage_state", raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "storage_state" {
		t.Fatalf("kind=%q", got.Kind)
	}
	if len(got.Cookies) != 1 {
		t.Fatalf("cookies=%d", len(got.Cookies))
	}
	cookie := got.Cookies[0]
	if cookie.Name != "sid" || cookie.Value != "abc" || cookie.Domain != "fixture.local" ||
		cookie.Path != "/" || cookie.Expires != 1760000000 ||
		!cookie.HTTPOnly || cookie.Secure || cookie.SameSite != "Lax" {
		t.Fatalf("cookie=%+v", cookie)
	}
	if len(got.Origins) != 1 || got.Origins[0].Origin != "http://localhost:13335" {
		t.Fatalf("origins=%+v", got.Origins)
	}
	if len(got.Origins[0].LocalStorage) != 1 || got.Origins[0].LocalStorage[0].Name != "k" {
		t.Fatalf("localStorage=%+v", got.Origins[0].LocalStorage)
	}
}

func TestParseStorageStateRejectsOversizeCookieValue(t *testing.T) {
	raw := json.RawMessage(`{"cookies":[{"name":"c","value":"` + strings.Repeat("x", 4097) + `","domain":"d","path":"/"}]}`)
	if _, err := ParseStorageState("storage_state", raw); err == nil {
		t.Fatal("want error")
	}
}

func TestParseStorageStateRejectsTooManyCookies(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"cookies":[`)
	for i := 0; i < 2001; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"name":"c","value":"v"}`)
	}
	b.WriteString(`]}`)
	if _, err := ParseStorageState("storage_state", json.RawMessage(b.String())); err == nil {
		t.Fatal("want error for more than 2000 cookies")
	}
}

func TestParseStorageStateRejectsEmptyCookieName(t *testing.T) {
	raw := json.RawMessage(`{"cookies":[{"name":"","value":"v"}]}`)
	if _, err := ParseStorageState("storage_state", raw); err == nil {
		t.Fatal("want error for an empty cookie name")
	}
}

func TestParseStorageStateRejectsNonHTTPOrigin(t *testing.T) {
	raw := json.RawMessage(`{"origins":[{"origin":"ftp://example.com"}]}`)
	if _, err := ParseStorageState("storage_state", raw); err == nil {
		t.Fatal("want error for a non-http(s) origin")
	}
}

func TestParseStorageStateRejectsOversizeLocalStorageValue(t *testing.T) {
	raw := json.RawMessage(`{"origins":[{"origin":"http://localhost:13335","localStorage":[{"name":"k","value":"` +
		strings.Repeat("x", 8193) + `"}]}]}`)
	if _, err := ParseStorageState("storage_state", raw); err == nil {
		t.Fatal("want error for an oversize localStorage value")
	}
}

func TestParseStorageStateRejectsMalformedJSON(t *testing.T) {
	if _, err := ParseStorageState("storage_state", json.RawMessage(`{"cookies":`)); err == nil {
		t.Fatal("want error for malformed JSON")
	}
}

func TestParseStorageStateAcceptsTokenHeaders(t *testing.T) {
	got, err := ParseStorageState("token", json.RawMessage(`{"headers":{"Authorization":"Bearer fixture-token"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "token" || got.Headers["Authorization"] != "Bearer fixture-token" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseStorageStateAcceptsBareTokenHeaders(t *testing.T) {
	got, err := ParseStorageState("token", json.RawMessage(`{"Authorization":"Bearer fixture-token"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Headers["Authorization"] != "Bearer fixture-token" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseStorageStateRejectsForbiddenTokenHeader(t *testing.T) {
	for _, header := range []string{"Host", "Content-Length"} {
		raw := json.RawMessage(`{"headers":{"` + header + `":"x"}}`)
		if _, err := ParseStorageState("token", raw); err == nil {
			t.Fatalf("want error for forbidden header %q", header)
		}
	}
}

func TestParseStorageStateRejectsTokenHeaderWithNewline(t *testing.T) {
	raw := json.RawMessage(`{"headers":{"Authorization":"Bearer a\r\nX-Evil: 1"}}`)
	if _, err := ParseStorageState("token", raw); err == nil {
		t.Fatal("want error for a header value containing CR/LF")
	}
}

func TestParseStorageStateRejectsBadTokenHeaderName(t *testing.T) {
	raw := json.RawMessage(`{"headers":{"Bad Header":"x"}}`)
	if _, err := ParseStorageState("token", raw); err == nil {
		t.Fatal("want error for an invalid header name")
	}
}

func TestParseStorageStateRejectsUnknownKind(t *testing.T) {
	if _, err := ParseStorageState("cookie_jar", json.RawMessage(`{}`)); err == nil {
		t.Fatal("want error for an unknown kind")
	}
}

func TestEarliestExpiry(t *testing.T) {
	now := time.Unix(1760000000, 0).UTC()
	state := &StorageState{Cookies: []Cookie{
		{Name: "session", Expires: 0},
		{Name: "session-neg", Expires: -1},
		{Name: "later", Expires: 1760000500},
		{Name: "earliest", Expires: 1760000300},
	}}
	got, ok := state.EarliestExpiry(now)
	if !ok {
		t.Fatal("want an expiry")
	}
	if want := time.Unix(1760000300, 0).UTC(); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// An already-expired cookie is still returned so the caller can reject the
	// whole storage state with storage_state_expired (spec §6.2).
	past := &StorageState{Cookies: []Cookie{{Name: "gone", Expires: 100}}}
	if got, ok := past.EarliestExpiry(now); !ok || !got.Before(now) {
		t.Fatalf("got=%v ok=%v, want a past expiry", got, ok)
	}
	if _, ok := (&StorageState{}).EarliestExpiry(now); ok {
		t.Fatal("want no expiry for a state without positive cookie expiries")
	}
}

func TestHashJSONStableAndHex(t *testing.T) {
	first, err := HashJSON(map[string]string{"b": "2", "a": "1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashJSON(map[string]string{"a": "1", "b": "2"})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("hash is not canonical: %q != %q", first, second)
	}
	if len(first) != 64 {
		t.Fatalf("hash %q is not a lowercase hex sha256", first)
	}
	other, err := HashJSON(map[string]string{"a": "1", "b": "3"})
	if err != nil {
		t.Fatal(err)
	}
	if other == first {
		t.Fatal("different payloads must hash differently")
	}
}
