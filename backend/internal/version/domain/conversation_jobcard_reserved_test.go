package version_test

// R-H6r / v0.2.4-H T18 pin: design.round is a PERMANENT historical read-only
// value, not a writable conversation job-card kind.
//
//   - It was REMOVED from the conversation job-card WRITE set
//     (conversationJobCardKinds now holds only chat.turn/requirement.curate), so
//     no new card can be written with it. It has zero producers since V-0004
//     removed the legacy design pipeline (the planner pins
//     byType("design.round")==0), so this narrowing changes no behavior.
//   - It was NOT deleted from the published OpenAPI JobKind enum: historical
//     conversation cards still render through that enum member plus the frontend
//     KIND_LABEL/invalid_work_type rules (JobCard.tsx). Keeping the enum member
//     means no contract-deletion notice period is triggered (constitution 6.6).
//   - The 0.3 due-version ruling (delete the enum member or promote it) is
//     recorded on conversationJobCardKinds (constitution III.7 third path).

import (
	"testing"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

func TestDesignRoundJobCardKindReservedForHistoricalCards(t *testing.T) {
	// The historical value is retained as a named domain constant (R-H6r), never
	// lost, but it is NOT writable.
	if version.DesignRoundJobCardKind != "design.round" {
		t.Fatalf("DesignRoundJobCardKind = %q, want design.round (the retained historical read-only value)", version.DesignRoundJobCardKind)
	}
	// Write set must not accept the historical value: IsConversationJobCardKind is
	// asserted on the CONSTANT itself (not a stray literal) so a future hand-edit
	// cannot slip design.round back into the write map without failing here.
	if version.IsConversationJobCardKind(version.DesignRoundJobCardKind) {
		t.Fatal("DesignRoundJobCardKind (historical design.round) must not be in the conversation job-card write set (R-H6r): it is a historical read-only value, not a writable conversation kind")
	}
	// It is NOT a frozen 0.2.1 contract job kind (work-type path, not kind path).
	if version.IsValidJobCardKind("design.round") {
		t.Fatal("design.round must not become a frozen 0.2.1 contract job kind")
	}
	// The surviving writable conversation work types keep their membership.
	for _, kind := range []string{"chat.turn", "requirement.curate"} {
		if !version.IsConversationJobCardKind(kind) {
			t.Fatalf("%s must remain a writable conversation job-card kind", kind)
		}
	}

	// The published OpenAPI JobKind enum MUST still carry design.round: it is the
	// historical-rendering contract, kept (not deleted) so no 6.6 notice period is
	// triggered (R-H6r). Reading api.yaml reuses the enum_authority_test.go helpers
	// (same package) so this is a real contract check, not a hand-copied second
	// truth. The enum is identified by the contract kinds it also carries, so a
	// stray design.round mention elsewhere cannot satisfy the assertion.
	root := repoRoot(t)
	src := readFrontendFile(t, root, "docs/openapi/api.yaml")
	enums, err := extractYAMLEnums(src)
	if err != nil {
		t.Fatalf("extract OpenAPI enums: %v", err)
	}
	found := false
	for _, e := range enums {
		members := stringSet(e.members)
		if members[version.DesignRoundJobCardKind] && members["design.cases"] && members["chat.turn"] && members["requirement.curate"] {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("docs/openapi/api.yaml JobKind enum must still contain design.round (R-H6r historical read-only value); deleting it triggers a 6.6 contract-deletion notice period")
	}
}
