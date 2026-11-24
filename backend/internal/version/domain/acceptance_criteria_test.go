package version_test

import (
	"testing"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ac builds a criterion with no id, which is how a caller submits one: ids are
// server-assigned.
func ac(given, when, then string) version.AcceptanceCriterion {
	return version.AcceptanceCriterion{Given: given, When: when, Then: then}
}

func ids(criteria []version.AcceptanceCriterion) []string {
	got := make([]string, 0, len(criteria))
	for _, criterion := range criteria {
		got = append(got, criterion.ID)
	}
	return got
}

func wantIDs(t *testing.T, got []version.AcceptanceCriterion, want ...string) {
	t.Helper()
	actual := ids(got)
	if len(actual) != len(want) {
		t.Fatalf("ids = %v, want %v", actual, want)
	}
	for i := range want {
		if actual[i] != want[i] {
			t.Fatalf("ids = %v, want %v", actual, want)
		}
	}
}

// TestACIDInheritance is the §3.6 rule "same content ⇒ same id": a criterion
// whose normalized given/when/then matches one the previous revision already had
// keeps that id.
func TestACIDInheritance(t *testing.T) {
	prev := []version.AcceptanceCriterion{
		{ID: "AC-1", Given: "a shopper", When: "they apply a coupon", Then: "the total drops"},
		{ID: "AC-2", Given: "a shopper", When: "they apply an expired coupon", Then: "it is rejected"},
	}
	next := []version.AcceptanceCriterion{
		ac("a shopper", "they apply a coupon", "the total drops"),
		ac("a shopper", "they apply an expired coupon", "it is rejected"),
	}

	wantIDs(t, version.AssignACIDs(prev, next), "AC-1", "AC-2")
}

// TestACIDInheritanceIgnoresWhitespace proves the match runs on the same
// normalized text the ContentHash uses, so a re-wrap of the same sentence is an
// inheritance and not a new criterion.
func TestACIDInheritanceIgnoresWhitespace(t *testing.T) {
	prev := []version.AcceptanceCriterion{
		{ID: "AC-4", Given: "a shopper", When: "they apply a coupon", Then: "the total drops"},
	}
	next := []version.AcceptanceCriterion{
		ac("  a   shopper ", "\nthey apply\ta coupon", "the total  drops\n"),
	}

	got := version.AssignACIDs(prev, next)
	wantIDs(t, got, "AC-4")
	if got[0].Given != "  a   shopper " {
		t.Fatalf("AssignACIDs rewrote the content: given = %q, want the caller's text unchanged", got[0].Given)
	}
}

// TestACIDReorderIsInvariant is the §3.6 rule "reordering does not change the
// hash": each criterion still matches its own content, so the id follows the
// criterion rather than the position -- and because the hash sorts by id, the
// hash is unchanged too.
func TestACIDReorderIsInvariant(t *testing.T) {
	prev := []version.AcceptanceCriterion{
		{ID: "AC-1", Given: "g1", When: "w1", Then: "t1"},
		{ID: "AC-2", Given: "g2", When: "w2", Then: "t2"},
		{ID: "AC-3", Given: "g3", When: "w3", Then: "t3"},
	}
	reordered := []version.AcceptanceCriterion{
		ac("g3", "w3", "t3"),
		ac("g1", "w1", "t1"),
		ac("g2", "w2", "t2"),
	}

	got := version.AssignACIDs(prev, reordered)
	wantIDs(t, got, "AC-3", "AC-1", "AC-2")

	// The revision keeps its own order; only the hash canonicalizes.
	before := revisionWith(prev)
	after := revisionWith(got)
	beforeHash, err := before.ComputeContentHash()
	if err != nil {
		t.Fatalf("hash prev: %v", err)
	}
	afterHash, err := after.ComputeContentHash()
	if err != nil {
		t.Fatalf("hash reordered: %v", err)
	}
	if beforeHash != afterHash {
		t.Fatalf("reordering changed the hash: %q vs %q", beforeHash, afterHash)
	}
}

// TestACIDAppendIsNextSequence is the §3.6 rule "new ACs append the next id": a
// criterion with no twin in prev takes max(prev)+1, then ++.
func TestACIDAppendIsNextSequence(t *testing.T) {
	prev := []version.AcceptanceCriterion{
		{ID: "AC-1", Given: "g1", When: "w1", Then: "t1"},
		{ID: "AC-2", Given: "g2", When: "w2", Then: "t2"},
	}
	next := []version.AcceptanceCriterion{
		ac("g1", "w1", "t1"),
		ac("g3", "w3", "t3"),
		ac("g4", "w4", "t4"),
	}

	wantIDs(t, version.AssignACIDs(prev, next), "AC-1", "AC-3", "AC-4")
}

// TestACIDFirstRevisionStartsAtOne covers a brand new requirement, which has no
// previous revision to inherit from.
func TestACIDFirstRevisionStartsAtOne(t *testing.T) {
	next := []version.AcceptanceCriterion{
		ac("g1", "w1", "t1"),
		ac("g2", "w2", "t2"),
	}

	wantIDs(t, version.AssignACIDs(nil, next), "AC-1", "AC-2")
}

// TestACIDDeletedLeavesPermanentHole is the §3.6 rule "deleted ACs leave a hole
// -- ids are never reused". AC-2 is dropped and a later append must not take it,
// even though it is now the lowest free number.
func TestACIDDeletedLeavesPermanentHole(t *testing.T) {
	revision1 := []version.AcceptanceCriterion{
		{ID: "AC-1", Given: "g1", When: "w1", Then: "t1"},
		{ID: "AC-2", Given: "g2", When: "w2", Then: "t2"},
		{ID: "AC-3", Given: "g3", When: "w3", Then: "t3"},
	}

	// Revision 2 deletes AC-2.
	revision2 := version.AssignACIDs(revision1, []version.AcceptanceCriterion{
		ac("g1", "w1", "t1"),
		ac("g3", "w3", "t3"),
	})
	wantIDs(t, revision2, "AC-1", "AC-3")

	// Revision 3 adds two new criteria. They must append above AC-3 and must not
	// resurrect AC-2.
	revision3 := version.AssignACIDs(revision2, []version.AcceptanceCriterion{
		ac("g1", "w1", "t1"),
		ac("g3", "w3", "t3"),
		ac("g4", "w4", "t4"),
		ac("g5", "w5", "t5"),
	})
	wantIDs(t, revision3, "AC-1", "AC-3", "AC-4", "AC-5")

	for _, criterion := range revision3 {
		if criterion.ID == "AC-2" {
			t.Fatal("the deleted id AC-2 was reused")
		}
	}
}

// TestACIDEditedContentRetiresTheOldID pins the case the design leaves implicit:
// §3.6 says "same content ⇒ same id", so a criterion whose content CHANGED is
// not the same criterion. The old id retires into a hole (never reused) and the
// edit appends a new id. This is what makes an edit always change the
// ContentHash and always signal stale design.
func TestACIDEditedContentRetiresTheOldID(t *testing.T) {
	prev := []version.AcceptanceCriterion{
		{ID: "AC-1", Given: "g1", When: "w1", Then: "t1"},
		{ID: "AC-2", Given: "g2", When: "w2", Then: "t2"},
	}
	// Same position, same shape, changed Then: an edit, not a match.
	next := []version.AcceptanceCriterion{
		ac("g1", "w1", "t1"),
		ac("g2", "w2", "t2 CHANGED"),
	}

	got := version.AssignACIDs(prev, next)
	wantIDs(t, got, "AC-1", "AC-3")

	// AC-2 stays retired: a later revision that restores the original text gets a
	// fresh id, not AC-2 back.
	restored := version.AssignACIDs(got, []version.AcceptanceCriterion{
		ac("g1", "w1", "t1"),
		ac("g2", "w2", "t2"),
	})
	wantIDs(t, restored, "AC-1", "AC-4")
}

// TestACIDDuplicateContentInheritsOnce covers a next list that repeats one
// content key: the first occurrence inherits, the second appends, so the result
// never holds the same id twice.
func TestACIDDuplicateContentInheritsOnce(t *testing.T) {
	prev := []version.AcceptanceCriterion{
		{ID: "AC-1", Given: "g1", When: "w1", Then: "t1"},
	}
	next := []version.AcceptanceCriterion{
		ac("g1", "w1", "t1"),
		ac("g1", "w1", "t1"),
	}

	got := version.AssignACIDs(prev, next)
	wantIDs(t, got, "AC-1", "AC-2")
}

// TestACIDIgnoresNonCanonicalPreviousIDs proves a malformed id in the previous
// revision cannot corrupt the sequence: it is skipped for both the max and the
// inheritance map, so its content is treated as new.
func TestACIDIgnoresNonCanonicalPreviousIDs(t *testing.T) {
	prev := []version.AcceptanceCriterion{
		{ID: "", Given: "g1", When: "w1", Then: "t1"},
		{ID: "AC-007", Given: "g2", When: "w2", Then: "t2"},
		{ID: "nonsense", Given: "g3", When: "w3", Then: "t3"},
		{ID: "AC-3", Given: "g4", When: "w4", Then: "t4"},
	}
	next := []version.AcceptanceCriterion{
		ac("g1", "w1", "t1"),
		ac("g2", "w2", "t2"),
		ac("g4", "w4", "t4"),
	}

	// AC-007 is not canonical, so g2 does not inherit it; the sequence still
	// starts above AC-3, the highest canonical id in prev.
	wantIDs(t, version.AssignACIDs(prev, next), "AC-4", "AC-5", "AC-3")
}

// TestACIDAssignmentIsIdempotent proves that re-running the assignment over its
// own output is a no-op, which is what makes a re-emit of unchanged content keep
// the same ids -- and therefore the same ContentHash.
func TestACIDAssignmentIsIdempotent(t *testing.T) {
	next := []version.AcceptanceCriterion{
		ac("g1", "w1", "t1"),
		ac("g2", "w2", "t2"),
	}
	first := version.AssignACIDs(nil, next)
	second := version.AssignACIDs(first, first)

	wantIDs(t, second, ids(first)...)
}

// TestAssignACIDsDoesNotMutateInputs guards the callers that keep the previous
// revision around after assigning (the application service hashes both).
func TestAssignACIDsDoesNotMutateInputs(t *testing.T) {
	prev := []version.AcceptanceCriterion{
		{ID: "AC-1", Given: "g1", When: "w1", Then: "t1"},
	}
	next := []version.AcceptanceCriterion{ac("g1", "w1", "t1"), ac("g2", "w2", "t2")}

	_ = version.AssignACIDs(prev, next)

	if prev[0].ID != "AC-1" || prev[0].Given != "g1" {
		t.Fatalf("prev was mutated: %#v", prev[0])
	}
	if len(prev) != 1 {
		t.Fatalf("prev grew to %d items", len(prev))
	}
	for _, criterion := range next {
		if criterion.ID != "" {
			t.Fatalf("next was mutated: %#v", criterion)
		}
	}
}

func TestNormalizeAcceptanceCriteriaDropsEmptyAndCollapsesWhitespace(t *testing.T) {
	got := version.NormalizeAcceptanceCriteria([]version.AcceptanceCriterion{
		{ID: " AC-1 ", Given: "  a   shopper ", When: "\nthey apply\tit ", Then: " it works  "},
		{Given: "   ", When: "", Then: "  "},
	})
	if len(got) != 1 {
		t.Fatalf("normalized = %#v, want the empty criterion dropped", got)
	}
	if got[0].ID != "AC-1" {
		t.Fatalf("id = %q, want the trimmed %q", got[0].ID, "AC-1")
	}
	if got[0].Given != "a shopper" || got[0].When != "they apply it" || got[0].Then != "it works" {
		t.Fatalf("parts were not whitespace-collapsed: %#v", got[0])
	}
}

func TestValidateAcceptanceCriteria(t *testing.T) {
	if err := version.ValidateAcceptanceCriteria([]version.AcceptanceCriterion{
		{ID: "AC-1", Given: "g", When: "w", Then: "t"},
	}); err != nil {
		t.Fatalf("valid criteria rejected: %v", err)
	}

	if err := version.ValidateAcceptanceCriteria(nil); err == nil {
		t.Fatal("an empty criterion set was accepted")
	}

	tooMany := make([]version.AcceptanceCriterion, 0, version.MaxAcceptanceCriteria+1)
	for i := 0; i <= version.MaxAcceptanceCriteria; i++ {
		tooMany = append(tooMany, version.AcceptanceCriterion{
			ID: version.FormatACID(i + 1), Given: "g", When: "w", Then: "t",
		})
	}
	if err := version.ValidateAcceptanceCriteria(tooMany); err == nil {
		t.Fatalf("%d criteria were accepted, want at most %d", len(tooMany), version.MaxAcceptanceCriteria)
	}
}

// TestACIDDuplicateContentIsStableAcrossRevisions is the review-round-1
// regression. When the same content appears more than once, the ids must be
// stable across arbitrarily many revisions: an unchanged re-submit is a no-op,
// so it must neither mint new ids nor move the ContentHash.
//
// The old first-match-wins map handed the first copy the inherited id and
// re-minted every later copy above maxSequence, so each revision retired one id
// and created another -- a no-op Update changed the hash, and any design case's
// covered_acs link to that criterion dangled after the next update. The per-key
// FIFO fixes it at the domain level; the write path also de-duplicates, so
// duplicates should not normally get this far.
func TestACIDDuplicateContentIsStableAcrossRevisions(t *testing.T) {
	cases := []struct {
		name string
		// prev is the revision the churn starts from; next is re-submitted
		// unchanged over and over.
		prev      []version.AcceptanceCriterion
		next      []version.AcceptanceCriterion
		wantIDs   []string
		revisions int
	}{
		{
			// The reviewer's exact case: prev already holds two ids for one
			// content key, and next repeats that content twice.
			name: "two prev ids for one content key",
			prev: []version.AcceptanceCriterion{
				{ID: "AC-1", Given: "g", When: "w", Then: "t"},
				{ID: "AC-3", Given: "g", When: "w", Then: "t"},
			},
			next:      []version.AcceptanceCriterion{ac("g", "w", "t"), ac("g", "w", "t")},
			wantIDs:   []string{"AC-1", "AC-3"},
			revisions: 5,
		},
		{
			// The reachable path: one prev id, next submits the content twice,
			// so the second copy appends once and must then keep that id.
			name: "duplicate appended once then held",
			prev: []version.AcceptanceCriterion{
				{ID: "AC-1", Given: "g", When: "w", Then: "t"},
			},
			next:      []version.AcceptanceCriterion{ac("g", "w", "t"), ac("g", "w", "t")},
			wantIDs:   []string{"AC-1", "AC-2"},
			revisions: 5,
		},
		{
			// Three copies, with a distinct criterion interleaved, over a hole.
			name: "three copies interleaved with a distinct criterion",
			prev: []version.AcceptanceCriterion{
				{ID: "AC-2", Given: "g", When: "w", Then: "t"},
				{ID: "AC-5", Given: "other", When: "w", Then: "t"},
				{ID: "AC-7", Given: "g", When: "w", Then: "t"},
				{ID: "AC-9", Given: "g", When: "w", Then: "t"},
			},
			next: []version.AcceptanceCriterion{
				ac("g", "w", "t"), ac("other", "w", "t"), ac("g", "w", "t"), ac("g", "w", "t"),
			},
			wantIDs:   []string{"AC-2", "AC-5", "AC-7", "AC-9"},
			revisions: 4,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			current := tc.prev
			wantHash := revisionHash(t, mustAssign(t, current, tc.next, tc.wantIDs))
			// Re-submit the identical content for `revisions` more generations.
			// Every one must produce the same ids and the same ContentHash.
			for generation := 2; generation <= tc.revisions; generation++ {
				current = mustAssign(t, current, tc.next, tc.wantIDs)
				gotHash := revisionHash(t, current)
				if gotHash != wantHash {
					t.Fatalf("generation %d moved the ContentHash for unchanged content: %q -> %q",
						generation, wantHash, gotHash)
				}
			}
		})
	}
}

// mustAssign runs AssignACIDs and asserts the resulting ids, so a churn case
// fails on the generation where the ids first move rather than later.
func mustAssign(t *testing.T, prev, next []version.AcceptanceCriterion, wantIDs []string) []version.AcceptanceCriterion {
	t.Helper()
	got := version.AssignACIDs(prev, next)
	actual := ids(got)
	if len(actual) != len(wantIDs) {
		t.Fatalf("AssignACIDs = %v, want %v", actual, wantIDs)
	}
	for i := range wantIDs {
		if actual[i] != wantIDs[i] {
			t.Fatalf("AssignACIDs = %v, want %v", actual, wantIDs)
		}
	}
	return got
}

func revisionHash(t *testing.T, criteria []version.AcceptanceCriterion) string {
	t.Helper()
	hash, err := revisionWith(criteria).ComputeContentHash()
	if err != nil {
		t.Fatalf("ComputeContentHash: %v", err)
	}
	return hash
}

// TestDedupeAcceptanceCriteria pins the write-path rule that keeps duplicate
// content out of a revision in the first place.
func TestDedupeAcceptanceCriteria(t *testing.T) {
	got := version.DedupeAcceptanceCriteria([]version.AcceptanceCriterion{
		{Given: "g1", When: "w1", Then: "t1"},
		{Given: " g1 ", When: "w1", Then: " t1"}, // same after normalization
		{Given: "g2", When: "w2", Then: "t2"},
		{Given: "g1", When: "w1", Then: "t1"}, // exact repeat
	})
	if len(got) != 2 {
		t.Fatalf("deduped = %#v, want the first occurrence of each distinct criterion", got)
	}
	if got[0].Given != "g1" || got[1].Given != "g2" {
		t.Fatalf("deduped order/content = %#v, want input order preserved", got)
	}
	if len(version.DedupeAcceptanceCriteria(nil)) != 0 {
		t.Fatal("deduping nil must yield an empty result")
	}
}

// revisionWith builds the smallest revision carrying the given criteria, so the
// hash can be compared across two orderings.
func revisionWith(criteria []version.AcceptanceCriterion) version.RequirementVersion {
	return version.RequirementVersion{
		Title:              "Checkout supports coupons",
		UserStory:          version.UserStory{AsA: "a shopper", IWant: "to pay less", SoThat: "I save money"},
		AcceptanceCriteria: criteria,
		Priority:           "P1",
		SutDescription:     "The web storefront checkout.",
	}
}
