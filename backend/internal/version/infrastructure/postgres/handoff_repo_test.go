package versionpg

// Fix round 1 unit coverage for the handoff/search SQL adapter helpers: payload
// ids must be decoded without float64 precision loss (snowflake ids exceed
// 2^53), and requirement refs must resolve both numeric and external forms.

import (
	"encoding/json"
	"testing"
)

// TestHandoffFactIDsKeepSnowflakePrecision pins json.Decoder.UseNumber for
// payload ids: a snowflake id above 2^53 must survive the payload decode
// exactly. Plain json.Unmarshal into any loses the low bits.
func TestHandoffFactIDsKeepSnowflakePrecision(t *testing.T) {
	const snowflake = int64(9007199254740993) // 2^53 + 1
	payload := []byte(`{"summary":"大 ID 事实","requirement_id":9007199254740993}`)

	if got := handoffFactRequirementID(payload); got != snowflake {
		t.Fatalf("fact requirement id = %d, want %d (float64 precision loss)", got, snowflake)
	}
	if got := handoffFactSummary("fact", []byte(`{"summary":9007199254740993}`)); got != "9007199254740993" {
		t.Fatalf("fact summary = %q, want the exact JSON number", got)
	}
}

// TestHandoffMessageRequirementIDsParsesNumericRefs pins the existing message
// ref projection used by the search adapter.
func TestHandoffMessageRequirementIDsParsesNumericRefs(t *testing.T) {
	refs, err := json.Marshal([]map[string]string{
		{"kind": "requirement", "id": "101"},
		{"kind": "attachment", "id": "7"},
		{"kind": "requirement", "id": "102"},
		{"kind": "requirement", "id": "101"},
	})
	if err != nil {
		t.Fatalf("marshal refs: %v", err)
	}
	ids := handoffMessageRequirementIDs(refs)
	if len(ids) != 2 || ids[0] != 101 || ids[1] != 102 {
		t.Fatalf("message requirement ids = %v, want [101 102]", ids)
	}
}
