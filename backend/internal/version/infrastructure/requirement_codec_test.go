package versionpg

import (
	"testing"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// TestRequirementCodecsFailClosedOnCorruptDocument pins U1h: a corrupt JSONB
// document is an error, not a silent empty value. The design context's
// decodeACIDs already fails closed on a corrupt acceptance_criteria document;
// these codecs match it so the same corruption class has one semantics across
// contexts (constitution 七.6). Empty/absent bytes and JSON null stay a valid
// empty value -- that is a stored state, not corruption.
func TestRequirementCodecsFailClosedOnCorruptDocument(t *testing.T) {
	if _, err := decodeAcceptanceCriteria([]byte(`{"id":"AC-1"}`)); err == nil {
		t.Fatal("decodeAcceptanceCriteria accepted a corrupt (non-array) document; want an error, not a silent empty set")
	}
	if _, err := decodeUserStory([]byte(`[1,2,3]`)); err == nil {
		t.Fatal("decodeUserStory accepted a corrupt (non-object) document; want an error")
	}
	if _, err := decodeStringSlice([]byte(`{"a":1}`)); err == nil {
		t.Fatal("decodeStringSlice accepted a corrupt (non-array) document; want an error")
	}

	for _, raw := range [][]byte{nil, {}, []byte("null")} {
		if ac, err := decodeAcceptanceCriteria(raw); err != nil || len(ac) != 0 {
			t.Fatalf("decodeAcceptanceCriteria(%q) = %v, %v; want empty, nil", raw, ac, err)
		}
		if s, err := decodeStringSlice(raw); err != nil || len(s) != 0 {
			t.Fatalf("decodeStringSlice(%q) = %v, %v; want empty, nil", raw, s, err)
		}
		if story, err := decodeUserStory(raw); err != nil || !story.Empty() {
			t.Fatalf("decodeUserStory(%q) = %+v, %v; want empty, nil", raw, story, err)
		}
	}
}

// TestRequirementCodecsRoundTrip proves the encode/decode pair preserves content
// (the write path stores what the read path returns) and that encode emits valid
// JSON. The old encoders returned a silent `{}`/`[]` on marshal error (U1i, a
// silent-data-loss pattern forbidden by 七.6); these return an error instead. The
// marshaled types are plain string structs/slices, so the error path is
// unreachable for them today -- the fix is defensive hardening that matches the
// extraction pipeline's insertExtractionRevision, which already propagates.
func TestRequirementCodecsRoundTrip(t *testing.T) {
	story := version.UserStory{AsA: "a shopper", IWant: "to check out", SoThat: "I pay"}
	rawStory, err := encodeUserStory(story)
	if err != nil {
		t.Fatalf("encodeUserStory: %v", err)
	}
	gotStory, err := decodeUserStory([]byte(rawStory))
	if err != nil || gotStory != story {
		t.Fatalf("user story round trip = %+v, %v; want %+v", gotStory, err, story)
	}

	criteria := []version.AcceptanceCriterion{{ID: "AC-1", Given: "g", When: "w", Then: "t"}}
	rawAC, err := encodeAcceptanceCriteria(criteria)
	if err != nil {
		t.Fatalf("encodeAcceptanceCriteria: %v", err)
	}
	gotAC, err := decodeAcceptanceCriteria([]byte(rawAC))
	if err != nil || len(gotAC) != 1 || gotAC[0] != criteria[0] {
		t.Fatalf("criteria round trip = %+v, %v; want %+v", gotAC, err, criteria)
	}

	roles := []string{"shopper", "support"}
	rawRoles, err := encodeStringSlice(roles)
	if err != nil {
		t.Fatalf("encodeStringSlice: %v", err)
	}
	gotRoles, err := decodeStringSlice([]byte(rawRoles))
	if err != nil || len(gotRoles) != 2 || gotRoles[0] != "shopper" || gotRoles[1] != "support" {
		t.Fatalf("roles round trip = %+v, %v; want %+v", gotRoles, err, roles)
	}

	// nil encodes to an empty JSON array (never null), so a read-back is empty.
	if emptyAC, err := encodeAcceptanceCriteria(nil); err != nil || emptyAC != "[]" {
		t.Fatalf("encodeAcceptanceCriteria(nil) = %q, %v; want []", emptyAC, err)
	}
	if emptyRoles, err := encodeStringSlice(nil); err != nil || emptyRoles != "[]" {
		t.Fatalf("encodeStringSlice(nil) = %q, %v; want []", emptyRoles, err)
	}
}
