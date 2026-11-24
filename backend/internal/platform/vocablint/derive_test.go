package vocablint_test

// Derivation properties migrated verbatim from TestHandoffNonLiteralGuardFires and
// TestHandoffVocabularyDerivationFires
// (version/infrastructure/postgres/handoff_repo_lint_test.go). They run on REAL
// synthetic vocabulary files, not a hand-built map (禁 born-green). No assertion
// is weakened.

import (
	"os"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/govocab"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"
)

// newVocabResolver roots the derivation at the backend module directory (this
// package is internal/platform/vocablint).
func newVocabResolver(t *testing.T) *govocab.Resolver {
	t.Helper()
	resolver, err := govocab.New("../../..")
	if err != nil {
		t.Fatalf("vocabulary resolver: %v", err)
	}
	return resolver
}

// TestNeedsExplicitClassification proves the classification rule in its strong
// form: an unresolvable string-shaped declaration needs explicit classification,
// while a resolved string, a provably non-string value and an allowlisted
// non-literal do not.
func TestNeedsExplicitClassification(t *testing.T) {
	// SYNTHETIC allowlist, deliberately NOT a copy of any gate's real
	// non-literal registry (a second copy of those names/reasons would be a
	// second small truth, constitution 五.1/五.3). The predicate only tests
	// map membership, so synthetic names prove exactly the same rule.
	allowlist := map[string]string{
		"SyntheticLeaseDuration": "numeric duration, not a string value",
		"SyntheticPayloadMax":    "numeric size, not a string value",
		"SyntheticTimeout":       "numeric duration, not a string value",
	}

	unresolved := govocab.Decl{Name: "MessagePartText", Unresolved: true}
	if !vocablint.NeedsExplicitClassification("MessagePartText", unresolved, allowlist) {
		t.Fatal("an unresolvable string-shaped declaration did not require explicit classification (guard is blind)")
	}
	resolved := govocab.Decl{Name: "MessagePartText", IsString: true, Values: []string{"text"}}
	if vocablint.NeedsExplicitClassification("MessagePartText", resolved, allowlist) {
		t.Fatal("a resolved string value should be derived, not require classification")
	}
	numeric := govocab.Decl{Name: "SomeNewSize"}
	if vocablint.NeedsExplicitClassification("SomeNewSize", numeric, allowlist) {
		t.Fatal("a provably non-string declaration should not require classification")
	}
	allowed := govocab.Decl{Name: "SyntheticLeaseDuration", Unresolved: true}
	if vocablint.NeedsExplicitClassification("SyntheticLeaseDuration", allowed, allowlist) {
		t.Fatal("an allowlisted non-literal should not require classification")
	}
	// I1: a PARTIALLY resolved set (IsString AND Unresolved) still needs
	// classification — IsString must not short-circuit the rule.
	partial := govocab.Decl{Name: "StatusSet", IsString: true, Values: []string{"done"}, Unresolved: true}
	if !vocablint.NeedsExplicitClassification("StatusSet", partial, allowlist) {
		t.Fatal("a partially resolved set did not require classification: its unresolvable member escapes")
	}
}

// TestDeriveVocabularyFires proves the DERIVATION hardening end to end on real
// synthetic vocabulary files: a var-defined value and a var SET member are
// protected, an alias and a cross-package reference resolve, an unresolvable alias
// fails loud, and an allowlist entry that resolves to a string is rejected (the
// escape face).
func TestDeriveVocabularyFires(t *testing.T) {
	dir := t.TempDir()
	writeVocab := func(name, src string) string {
		t.Helper()
		path := dir + string(os.PathSeparator) + name
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatalf("write vocabulary sample: %v", err)
		}
		return path
	}
	// The resolver only needs a module root for CROSS-PACKAGE references; these
	// samples are same-package, so the real backend root is fine.
	resolver := newVocabResolver(t)

	const sample = `package sample

const Base = "approved"
const Alias = Base
var VarStatus = "proposed"
var StatusSet = []string{"done", "queued"}
`
	derived := vocablint.DeriveVocabulary(resolver, []string{writeVocab("sample.go", sample)}, nil)
	if len(derived.Problems) != 0 {
		t.Fatalf("clean vocabulary reported problems: %v", derived.Problems)
	}
	for _, want := range []string{"approved", "proposed", "done", "queued"} {
		if _, ok := derived.Values[want]; !ok {
			t.Fatalf("derived set is missing %q (var/alias blind spot): %v", want, derived.Values)
		}
	}

	// An unresolvable string-shaped value must fail loud, not vanish.
	const loudSample = "package sample\n\nconst Base = \"approved\"\nconst Dangling = missingIdent\n"
	loud := vocablint.DeriveVocabulary(resolver, []string{writeVocab("loud.go", loudSample)}, nil)
	if len(loud.Problems) == 0 {
		t.Fatal("an unresolvable alias was silently dropped from the protected set")
	}

	// Allowlisting it is the conscious escape hatch...
	classified := vocablint.DeriveVocabulary(resolver, []string{writeVocab("loud.go", loudSample)},
		map[string]string{"Dangling": "not a vocabulary value"})
	if len(classified.Problems) != 0 {
		t.Fatalf("an explicitly classified non-literal still failed: %v", classified.Problems)
	}

	// ...but allowlisting a value that DOES resolve to a string is rejected: the
	// allowlist must not be usable to hide a vocabulary value from the guard.
	escaped := vocablint.DeriveVocabulary(resolver, []string{writeVocab("escape.go", "package sample\n\nconst Base = \"approved\"\nconst Hidden = Base\n")},
		map[string]string{"Hidden": "pretend this is not a string"})
	if len(escaped.Problems) == 0 {
		t.Fatal("the allowlist hid a resolvable string value from the guard")
	}

	// A stale allowlist entry (declaration gone) is reported.
	stale := vocablint.DeriveVocabulary(resolver, []string{writeVocab("sample.go", sample)},
		map[string]string{"Removed": "gone"})
	if len(stale.Problems) == 0 {
		t.Fatal("a stale non-literal allowlist entry was not reported")
	}

	// I1: a set with one literal member and one unresolvable member must NOT go
	// quiet just because part of it resolved. The resolvable member stays
	// protected AND the declaration is loudly classified.
	const partialSample = "package sample\n\nvar StatusSet = []string{\"done\", missingIdent}\n"
	partial := vocablint.DeriveVocabulary(resolver, []string{writeVocab("partial.go", partialSample)}, nil)
	if len(partial.Problems) == 0 {
		t.Fatal("a partially unresolvable vocabulary set was silently accepted: its unresolvable member escapes the guard")
	}
	if _, ok := partial.Values["done"]; !ok {
		t.Fatalf("the resolvable member of a partial set must stay protected: %v", partial.Values)
	}
	// Classifying it explicitly is the conscious escape hatch, and the resolved
	// member is still derived (the entry does not hide the whole declaration).
	partialClassified := vocablint.DeriveVocabulary(resolver, []string{writeVocab("partial.go", partialSample)},
		map[string]string{"StatusSet": "the unresolvable member is not a vocabulary value"})
	if len(partialClassified.Problems) != 0 {
		t.Fatalf("an explicitly classified partial set still failed: %v", partialClassified.Problems)
	}
	if _, ok := partialClassified.Values["done"]; !ok {
		t.Fatalf("classifying a partial set hid its resolvable member: %v", partialClassified.Values)
	}
}

// TestDeriveVocabularyEmptyIsLoud pins the anti-vacuity rule: deriving nothing is
// a broken derivation, not an empty protected set.
func TestDeriveVocabularyEmptyIsLoud(t *testing.T) {
	dir := t.TempDir()
	path := dir + string(os.PathSeparator) + "empty.go"
	if err := os.WriteFile(path, []byte("package sample\n\nconst Numeric = 1\n"), 0o600); err != nil {
		t.Fatalf("write sample: %v", err)
	}
	derived := vocablint.DeriveVocabulary(newVocabResolver(t), []string{path}, nil)
	if len(derived.Problems) == 0 {
		t.Fatal("a vocabulary file with no string value derived no problem; the gate would go vacuous")
	}
}

// TestDeriveVocabularyMissingFileIsLoud pins that an unreadable vocabulary file is
// a problem, never a silently smaller protected set.
func TestDeriveVocabularyMissingFileIsLoud(t *testing.T) {
	derived := vocablint.DeriveVocabulary(newVocabResolver(t), []string{"does-not-exist.go"}, nil)
	if len(derived.Problems) == 0 {
		t.Fatal("a missing vocabulary file was silently skipped")
	}
}
