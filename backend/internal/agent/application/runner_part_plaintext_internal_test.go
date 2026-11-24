package agentapp

// GD-01 (Q05=B, constitution Constraints A.2): credentials are plaintext by
// design and may enter prompt, evidence and logs. The conversation "parts" are
// the produced trace of a run (thinking / tool_call / error) persisted before
// they reach the thread or its SSE event — a log carrier, not a REST credential
// view. The legacy part-secret scrubbing (assignment/bearer/JWT regexes and
// credential-keyed field masking) is removed; credentials stay verbatim. Only
// the size/depth bounds remain (operational bounds, not redaction).

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSanitizePartResultKeepsCredentialsPlaintext(t *testing.T) {
	raw, ok := sanitizePartResult(`password=hunter2 Authorization: Bearer abc.def.ghi`)
	if !ok {
		t.Fatal("sanitizePartResult returned ok=false")
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	value, _ := obj["value"].(string)
	if !strings.Contains(value, "password=hunter2") {
		t.Fatalf("assignment credential must stay verbatim (GD-01): %q", value)
	}
	if !strings.Contains(value, "Bearer abc.def.ghi") {
		t.Fatalf("bearer credential must stay verbatim (GD-01): %q", value)
	}
	if strings.Contains(value, "[redacted]") {
		t.Fatalf("legacy redaction marker present: %q", value)
	}
}

func TestSanitizePartJSONObjectKeepsCredentialKeysPlaintext(t *testing.T) {
	raw, ok := sanitizePartJSONObject(`{"api_key":"sk-live-secret","password":"hunter2","nested":{"token":"tok-123"},"note":"ok"}`)
	if !ok {
		t.Fatal("sanitizePartJSONObject returned ok=false")
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if obj["api_key"] != "sk-live-secret" {
		t.Fatalf("api_key must stay verbatim (GD-01): %#v", obj["api_key"])
	}
	if obj["password"] != "hunter2" {
		t.Fatalf("password must stay verbatim (GD-01): %#v", obj["password"])
	}
	nested, _ := obj["nested"].(map[string]any)
	if nested["token"] != "tok-123" {
		t.Fatalf("nested token must stay verbatim (GD-01): %#v", nested["token"])
	}
	if strings.Contains(string(raw), "[redacted]") {
		t.Fatalf("legacy redaction marker present: %s", raw)
	}
}

func TestSanitizePartResultKeepsJWTPlaintext(t *testing.T) {
	const jwt = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"
	raw, ok := sanitizePartResult(`{"session":"` + jwt + `"}`)
	if !ok {
		t.Fatal("sanitizePartResult returned ok=false")
	}
	if !strings.Contains(string(raw), jwt) {
		t.Fatalf("JWT must stay verbatim (GD-01): %s", raw)
	}
	if strings.Contains(string(raw), "[redacted]") {
		t.Fatalf("legacy redaction marker present: %s", raw)
	}
}

// TestSanitizePartResultStillBoundsSize guards the operational size bound: the
// scrub removal must not drop the rune cap that keeps a part from bloating a
// message (a size bound, not a credential restriction).
func TestSanitizePartResultStillBoundsSize(t *testing.T) {
	long := strings.Repeat("x", producedPartStringMax+100)
	raw, ok := sanitizePartResult(long)
	if !ok {
		t.Fatal("sanitizePartResult returned ok=false")
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	value, _ := obj["value"].(string)
	if got := len([]rune(value)); got != producedPartStringMax {
		t.Fatalf("value rune len = %d, want bounded to %d", got, producedPartStringMax)
	}
}
