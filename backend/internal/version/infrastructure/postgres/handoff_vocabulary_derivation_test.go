package versionpg_test

// H-B1 guard hardening (T3 carried minors ② and ③).
//
//   - minor ②: the derived protected value set must cover the plan-item status
//     vocabulary. handoffVocabularyFiles() did not include version/domain/
//     plan_item.go, so 'in_progress' / 'pending' and the whole terminal set were
//     unprotected in the handoff production files AND in the integration
//     fixtures: re-hardcoding them stayed green (方法纪律 ⑧ — a value the
//     derivation never sees cannot be protected).
//   - minor ③: the exemption machinery inherited from T17 was LINE-scoped
//     (strings.Contains(line, fragment) exempted every occurrence on the line)
//     and the non-vacuous check was VALUE-level (seen[value]), so a fragment
//     could go stale while its value still appeared elsewhere. Both are upgraded
//     to occurrence-scoped position coverage + fragment-level hit tracking, the
//     shape T2 established in design/application/status_vocabulary_gate_test.go.

import (
	"sort"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// TestHandoffVocabularyCoversPlanItemStatuses pins minor ②: every plan-item status
// the domain defines — the two open-state constants AND every member of the
// terminal set (which lives in a var, not a const block) — is in the protected
// value set the handoff production guard and the fixture guard share.
func TestHandoffVocabularyCoversPlanItemStatuses(t *testing.T) {
	values := deriveDomainConstantValues(t)
	want := []string{version.PlanItemStatusPending, version.PlanItemStatusInProgress}
	want = append(want, version.PlanItemTerminalStatuses()...)
	for _, status := range want {
		if _, ok := values[status]; !ok {
			t.Errorf("plan-item status %q is not in the handoff protected value set; a re-hardcoded literal would stay green", status)
		}
	}
}

// TestHandoffExemptionIsOccurrenceScoped pins minor ③ in its strong form: a line
// carrying a sanctioned fragment AND a rogue occurrence of the same value flags
// the rogue. The line-scoped exemption this replaces hid it.
func TestHandoffExemptionIsOccurrenceScoped(t *testing.T) {
	values := map[string][]string{"question": {"version.MessagePartQuestion"}}
	allowlist := map[string]vocablint.AllowEntry{
		"question": {
			Reason:    "handoff-local open-item Kind",
			Fragments: vocablint.FileFragments("s.go", `Kind: "question"`),
		},
	}

	// The sanctioned occurrence alone is clean.
	sanctioned := "package x\nfunc f() { item := Item{Kind: \"question\"} }\n"
	if got := vocablint.ScanLiterals("s.go", sanctioned, values, allowlist).Findings; len(got) != 0 {
		t.Fatalf("sanctioned occurrence flagged: %+v", got)
	}

	// A rogue occurrence of the SAME value on the SAME line must still fire.
	rogue := "package x\nfunc f() { item := Item{Kind: \"question\"}; if kind == \"question\" { return } }\n"
	rogueAllow := map[string]vocablint.AllowEntry{
		"question": {Reason: "handoff-local open-item Kind", Fragments: vocablint.FileFragments("r.go", `Kind: "question"`)},
	}
	got := vocablint.ScanLiterals("r.go", rogue, values, rogueAllow).Findings
	if len(got) != 1 {
		t.Fatalf("line-scoped exemption hid a rogue occurrence sharing the line: %+v", got)
	}

	// A fragment that legitimately appears twice sanctions both occurrences.
	twice := "package x\nfunc f() { a := Item{Kind: \"question\"}; b := Item{Kind: \"question\"} }\n"
	twiceAllow := map[string]vocablint.AllowEntry{
		"question": {Reason: "handoff-local open-item Kind", Fragments: vocablint.FileFragments("t.go", `Kind: "question"`)},
	}
	if got := vocablint.ScanLiterals("t.go", twice, values, twiceAllow).Findings; len(got) != 0 {
		t.Fatalf("repeated sanctioned fragment produced a false positive: %+v", got)
	}

	// Overlapping fragments (one a sub-string of the other) covering the SAME
	// occurrence must not clamp a rogue occurrence away.
	narrow := map[string]vocablint.AllowEntry{
		"question": {Reason: "overlap", Fragments: vocablint.FileFragments("n.go", `Kind: "question"`, `Item{Kind: "question"}`)},
	}
	if got := vocablint.ScanLiterals("n.go", rogue, values, narrow).Findings; len(got) != 1 {
		t.Fatalf("overlapping fragments clamped the rogue occurrence away: %+v", got)
	}
}

// TestHandoffAllowlistTracksFragments pins the fragment-level non-vacuous check:
// an entry whose value still appears, but whose listed fragment no longer
// matches any occurrence, is stale and must be reported. A value-level check
// (seen[value]) passed it silently.
func TestHandoffAllowlistTracksFragments(t *testing.T) {
	values := map[string][]string{"question": {"version.MessagePartQuestion"}}
	allowlist := map[string]vocablint.AllowEntry{
		"question": {
			Reason:    "two adjudicated contexts",
			Fragments: vocablint.FileFragments("s.go", `Item{Kind: "question"}`, `Other{Kind: "question"}`),
		},
	}
	// Only the first fragment's context is present; the second is stale even
	// though the value "question" is seen.
	src := "package x\nfunc f() { item := Item{Kind: \"question\"} }\n"
	scan := vocablint.ScanLiterals("s.go", src, values, allowlist)
	if !scan.SeenValues["question"] {
		t.Fatal("the value-level check would already pass here: the sample must still see the value")
	}
	if scan.FragmentHits[vocablint.FragmentKey{Value: "question", Index: 0}] == 0 {
		t.Fatalf("live fragment was not tracked: %v", scan.FragmentHits)
	}
	if scan.FragmentHits[vocablint.FragmentKey{Value: "question", Index: 1}] != 0 {
		t.Fatalf("stale fragment reported as hit: %v", scan.FragmentHits)
	}
	if !strings.Contains(strings.Join(sortedHitFragments(allowlist, scan.FragmentHits), ","), `Item{Kind: "question"}`) {
		t.Fatalf("hit map is not readable: %v", scan.FragmentHits)
	}
}

func sortedHitFragments(allowlist map[string]vocablint.AllowEntry, hits map[vocablint.FragmentKey]int) []string {
	var out []string
	for key, count := range hits {
		if count > 0 {
			out = append(out, allowlist[key.Value].Fragments[key.Index].Text)
		}
	}
	sort.Strings(out)
	return out
}
