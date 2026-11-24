package tools

// H-B6 (constitution 五.1/五.4): the sut_id wire-type contract — a positive decimal
// STRING — has ONE authority, idStringSchema(), the shape case.graph_propose
// requires of assertions[].sut_id (parsed as *string). Every producer surface
// (sut.list, sut.database_list, the task_pack DatabaseSummary row) must emit sut_id
// in that shape, so a model that copies a sut_id verbatim into a db assertion never
// hits a json.UnmarshalTypeError. Before this pin the contract was maintained by
// several independent surface guards each re-hardcoding "a string", with no single
// source tying them to the consumer's required type. requireSUTIDWireType derives
// the expectation from idStringSchema() and the surface guards call it; the pin
// below additionally anchors the DatabaseSummary `,string` encoding mechanism and
// the tool filter schemas to the same authority.

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// requireSUTIDWireType asserts value satisfies the single sut_id wire-type
// authority (idStringSchema): a Go string matching the authority's decimal pattern.
// The surface guards derive their type expectation here instead of each hardcoding
// "want the STRING ...", so the contract cannot drift between surfaces.
func requireSUTIDWireType(t *testing.T, value any) {
	t.Helper()
	authority := idStringSchema()
	if authority["type"] != "string" {
		t.Fatalf("sut_id wire-type authority idStringSchema() type = %v, want string", authority["type"])
	}
	s, ok := value.(string)
	if !ok {
		t.Fatalf("sut_id = %#v (%T), want a decimal STRING per idStringSchema() -- a number breaks case.graph_propose, which parses sut_id as *string", value, value)
	}
	pattern, _ := authority["pattern"].(string)
	if pattern == "" {
		// A missing pattern would silently skip the shape check (T5 minor②):
		// the authority is incomplete, fail loud instead of passing vacuously.
		t.Fatal("sut_id wire-type authority idStringSchema() has no pattern; the shape check would be silently skipped")
	}
	if !regexp.MustCompile(pattern).MatchString(s) {
		t.Fatalf("sut_id = %q does not match the authority pattern %q", s, pattern)
	}
}

// jsonTagHasOption reports whether a json struct tag carries option as an EXACT
// comma-separated option (T5 minor③): strings.Contains(tag, "string") would also
// match a field NAME containing "string" or a different option like "stringish",
// silently passing a tag that does not actually encode the value as a string.
func jsonTagHasOption(tag, option string) bool {
	parts := strings.Split(tag, ",")
	for _, p := range parts[1:] {
		if p == option {
			return true
		}
	}
	return false
}

// TestJSONTagHasOptionPrecision is permanent deterministic proof of the exact
// option match: a substring of the name or of another option never passes.
func TestJSONTagHasOptionPrecision(t *testing.T) {
	for _, tag := range []string{"sut_id,string", "sut_id,omitempty,string", "sut_id,string,omitempty"} {
		if !jsonTagHasOption(tag, "string") {
			t.Fatalf("tag %q carries ,string but was rejected", tag)
		}
	}
	for _, tag := range []string{"sut_id", "sut_id,omitempty", "string_id", "sut_id,stringish", "sut_id"} {
		if jsonTagHasOption(tag, "string") {
			t.Fatalf("tag %q does NOT carry the ,string option but passed (substring blind spot)", tag)
		}
	}
}

// TestSUTIDWireTypeSingleAuthority pins the producer surfaces to the single
// idStringSchema() authority. Mutation: dropping `,string` from DatabaseSummary.SUTID
// makes the pack surface emit a bare number, so the structural tag pin and the
// marshal probe fail (RED); restoring the tag returns GREEN. A hand-copied "string"
// expectation in each surface test could not anchor the type to the consumer schema.
func TestSUTIDWireTypeSingleAuthority(t *testing.T) {
	authority := idStringSchema()
	if authority["type"] != "string" {
		t.Fatalf("idStringSchema() type = %v, want string (the sut_id wire-type authority)", authority["type"])
	}
	if p, _ := authority["pattern"].(string); p == "" {
		t.Fatal("idStringSchema() has no pattern; the sut_id wire-type authority is incomplete")
	}

	// DatabaseSummary.SUTID is int64 with a load-bearing `,string` json tag: the tag
	// is the mechanism that makes the task_pack surface emit a string like every
	// other surface. Pin the mechanism structurally so a regression to a bare number
	// fails at the type, not only in a downstream surface test.
	field, ok := reflect.TypeOf(DatabaseSummary{}).FieldByName("SUTID")
	if !ok {
		t.Fatal("DatabaseSummary has no SUTID field")
	}
	if tag := field.Tag.Get("json"); !jsonTagHasOption(tag, "string") {
		t.Fatalf("DatabaseSummary.SUTID json tag = %q, want ,string so sut_id encodes as a decimal string (authority idStringSchema)", tag)
	}
	encoded, err := json.Marshal(DatabaseSummary{ID: "1501", SUTID: 9001, Name: "order_db", Kind: "postgres", ReadOnly: true})
	if err != nil {
		t.Fatalf("marshal DatabaseSummary: %v", err)
	}
	var row map[string]any
	if err := json.Unmarshal(encoded, &row); err != nil {
		t.Fatalf("unmarshal DatabaseSummary: %v", err)
	}
	requireSUTIDWireType(t, row["sut_id"])

	// The tool filter schemas that accept a sut_id must declare the authority's
	// type, so the consumer-facing contract and the producer encoding agree.
	for _, tool := range []Tool{&sutPageList{}, &sutDatabaseList{}} {
		var schema struct {
			Properties map[string]struct {
				Type string `json:"type"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(tool.Parameters(), &schema); err != nil {
			t.Fatalf("parse %T parameters: %v", tool, err)
		}
		if got := schema.Properties["sut_id"].Type; got != authority["type"] {
			t.Fatalf("%T sut_id schema type = %q, want %v (idStringSchema authority)", tool, got, authority["type"])
		}
	}
}
