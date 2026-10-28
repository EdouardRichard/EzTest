package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestParseStorageStateKeepsSessionStorage proves the additive spec §3.4
// extension: a Playwright state document that carries sessionStorage keeps
// every per-origin entry.
func TestParseStorageStateKeepsSessionStorage(t *testing.T) {
	raw := []byte(`{"cookies":[],"origins":[{"origin":"https://x.test","localStorage":[{"name":"a","value":"1"}],"sessionStorage":[{"name":"b","value":"2"}]}]}`)
	st, err := ParseStorageState("storage_state", raw)
	if err != nil || len(st.Origins) != 1 || len(st.Origins[0].SessionStorage) != 1 {
		t.Fatalf("%+v %v", st, err)
	}
	item := st.Origins[0].SessionStorage[0]
	if item.Name != "b" || item.Value != "2" {
		t.Fatalf("sessionStorage item=%+v", item)
	}
}

// TestStorageStateSessionStorageRoundTrip proves normalization does not drop or
// reorder sessionStorage.
func TestStorageStateSessionStorageRoundTrip(t *testing.T) {
	raw := []byte(`{"cookies":[],"origins":[{"origin":"https://x.test","localStorage":[],"sessionStorage":[{"name":"b","value":"2"},{"name":"c","value":"3"}]}]}`)
	st, err := ParseStorageState("storage_state", raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"sessionStorage"`) {
		t.Fatalf("encoded state drops sessionStorage: %s", encoded)
	}
	again, err := ParseStorageState("storage_state", encoded)
	if err != nil {
		t.Fatalf("re-parse normalized state: %v", err)
	}
	if len(again.Origins) != 1 || len(again.Origins[0].SessionStorage) != 2 {
		t.Fatalf("origins=%+v", again.Origins)
	}
	if again.Origins[0].SessionStorage[1].Name != "c" {
		t.Fatalf("sessionStorage=%+v", again.Origins[0].SessionStorage)
	}
}

// TestStorageStateOldStateHasNoSessionStorage proves the extension is additive:
// a pre-P6 state has no sessionStorage field and re-encodes without one.
func TestStorageStateOldStateHasNoSessionStorage(t *testing.T) {
	raw := []byte(`{"cookies":[],"origins":[{"origin":"https://x.test","localStorage":[{"name":"a","value":"1"}]}]}`)
	st, err := ParseStorageState("storage_state", raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Origins) != 1 || st.Origins[0].SessionStorage != nil {
		t.Fatalf("origins=%+v", st.Origins)
	}
	encoded, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "sessionStorage") {
		t.Fatalf("old state gained a sessionStorage field: %s", encoded)
	}
}

// TestParseStorageStateValidatesSessionStorageLikeLocalStorage proves the same
// per-entry limits apply to the new field.
func TestParseStorageStateValidatesSessionStorageLikeLocalStorage(t *testing.T) {
	t.Run("oversize value", func(t *testing.T) {
		raw := json.RawMessage(`{"origins":[{"origin":"https://x.test","sessionStorage":[{"name":"k","value":"` +
			strings.Repeat("x", maxLocalStorageValue+1) + `"}]}]}`)
		assertInvalidStorageState(t, raw, "storageState.origins[0].sessionStorage[0].value")
	})
	t.Run("too many items", func(t *testing.T) {
		var b strings.Builder
		b.WriteString(`{"origins":[{"origin":"https://x.test","sessionStorage":[`)
		for i := 0; i < maxLocalStorageItems+1; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`{"name":"k","value":"v"}`)
		}
		b.WriteString(`]}]}`)
		assertInvalidStorageState(t, json.RawMessage(b.String()), "storageState.origins[0].sessionStorage")
	})
}

// assertInvalidStorageState asserts the shared invalid_storage_state 422 error
// carries wantField.
func assertInvalidStorageState(t *testing.T, raw json.RawMessage, wantField string) {
	t.Helper()
	_, err := ParseStorageState("storage_state", raw)
	if err == nil {
		t.Fatal("want an invalid_storage_state error")
	}
	domainErr, ok := err.(*Error)
	if !ok || domainErr.Reason != "invalid_storage_state" {
		t.Fatalf("error=%v want invalid_storage_state", err)
	}
	if domainErr.Field != wantField {
		t.Fatalf("error field=%q want %q", domainErr.Field, wantField)
	}
}
