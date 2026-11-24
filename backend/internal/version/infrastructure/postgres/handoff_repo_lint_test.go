package versionpg_test

// Value-derived source lint for the handoff (T17 fix round 5, hardened by
// v0.2.4-H H-B1 + T3 carried minors ②/③). It closes the hand-written-literal
// class so it cannot return:
//
//   - The protected value set is DERIVED from the vocabulary-defining domain
//     files (not hand-listed), so a constant the handoff forgets to reference is
//     still protected — forgetting to name it IS the defect a hand-picked list
//     cannot catch. The derivation covers const AND var declarations, the members
//     of a var SET, same-package aliases, string conversions/concatenation,
//     value-elided const specs and CROSS-PACKAGE constant references
//     (platform/govocab). The former BasicLit-only, const-only scan was blind to
//     every one of those shapes.
//   - A value the resolver cannot evaluate is a LOUD failure unless it is
//     explicitly classified, and an allowlist entry that DOES resolve to a string
//     is rejected: the allowlist can no longer be used to hide a vocabulary value
//     from the guard (the escape face H-B1 closes).
//   - It fails if any derived value appears as a quoted literal ('x' or "x") in the
//     handoff production files, outside a FRAGMENT-SCOPED, OCCURRENCE-SCOPED
//     allowlist: each entry carries a reason plus exact source fragments, and only
//     the occurrences a fragment actually spans are exempt. A rogue occurrence
//     sharing a line with a sanctioned one still fires (minor ③; the shape T2
//     established for design).
//   - Comments AND struct tags are stripped first (a backtick literal whose content
//     matches \w+:"), so json:"..." tags and doc counter-examples never collide.
//   - It SELF-TRACKS renames: the values come from the declarations themselves.
//   - Unused allowlist FRAGMENTS fail (minor ③): an exemption whose fragment no
//     longer matches any occurrence is stale and must be removed, even when its
//     value still appears elsewhere.
//
// T11e-0: the ALGORITHM now lives in platform/vocablint (single authority shared
// by every context's vocabulary gate); this file keeps only the handoff's SCOPE
// (vocabulary files, guarded files) and its adjudicated collisions. The guard's
// own discriminative properties — TestHandoffLiteralGuardFires,
// TestHandoffNonLiteralGuardFires, TestHandoffVocabularyDerivationFires — moved
// with the algorithm to platform/vocablint (scan_test.go / derive_test.go), with
// every assertion preserved.

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/govocab"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"
)

// handoffVocabularyFiles are the domain files whose string declarations define the
// vocabularies the handoff must source from. Paths are relative to this package dir
// (internal/version/infrastructure/postgres).
//
// plan_item.go joined the set in H-B1 (T3 minor ②): without it the plan-item
// statuses ('in_progress' and the terminal set) were unprotected in the handoff
// production files AND in the integration fixtures, so re-hardcoding them stayed
// green.
func handoffVocabularyFiles() []string {
	return []string{
		"../../domain/fact.go",
		"../../domain/requirement.go",
		"../../domain/conversation.go",
		"../../domain/plan_item.go",
		"../../../execution/domain/human.go",
		"../../../execution/domain/intent.go",
		"../../../resource/domain/sut.go",
		"../../../resource/domain/page.go",
		"../../../resource/domain/api.go",
	}
}

// handoffGuardedFiles are the handoff production files the guard scans.
func handoffGuardedFiles() []string {
	return []string{
		"backend/internal/version/infrastructure/postgres/handoff_repo.go",
		"backend/internal/version/application/handoff.go",
		"backend/internal/version/application/handoff_resources.go",
		"backend/internal/version/interfaces/httpapi/handoff_handler.go",
	}
}

// nonLiteralHandoffConstants classifies vocabulary declarations whose value the
// resolver cannot evaluate and which are consciously known to carry no string
// value the handoff must source. An entry whose declaration DOES resolve to a
// string is rejected by the derivation (it must be derived and protected, not
// hidden here), and an entry whose declaration disappeared is reported stale.
func nonLiteralHandoffConstants() map[string]string {
	return map[string]string{
		"ConversationWriterLeaseDuration": "numeric duration (90 * time.Second), not a string value",
		"QuestionPartPayloadMax":          "numeric size (64 << 10), not a string value",
		"DefaultQuestionTimeout":          "numeric duration (24 * time.Hour), not a string value",
	}
}

// newHandoffVocabResolver roots the derivation at the backend module directory
// (this package is internal/version/infrastructure/postgres).
func newHandoffVocabResolver(t *testing.T) *govocab.Resolver {
	t.Helper()
	resolver, err := govocab.New("../../../..")
	if err != nil {
		t.Fatalf("vocabulary resolver: %v", err)
	}
	return resolver
}

// deriveDomainConstantValues is the guard's derivation entry point.
func deriveDomainConstantValues(t *testing.T) map[string][]string {
	t.Helper()
	derived := vocablint.DeriveVocabulary(newHandoffVocabResolver(t), handoffVocabularyFiles(), nonLiteralHandoffConstants())
	for _, problem := range derived.Problems {
		t.Errorf("%s", problem)
	}
	if len(derived.Problems) > 0 {
		t.Fatalf("%d vocabulary derivation problem(s)", len(derived.Problems))
	}
	return derived.Values
}

// handoffLiteralAllowlist adjudicates the genuine collisions between a domain
// value and an unrelated wire-format / handoff-local use of the same string. Each
// entry is fragment-scoped so it cannot exempt a live predicate.
func handoffLiteralAllowlist() map[string]vocablint.AllowEntry {
	return map[string]vocablint.AllowEntry{
		"decision": {
			Reason:    "fact-payload summary key list (handoffFactSummary), not a version_fact.kind filter",
			Fragments: vocablint.FileFragments("backend/internal/version/infrastructure/postgres/handoff_repo.go", `"summary", "text", "value", "decision", "title"`),
		},
		"text": {
			Reason:    "fact-payload summary key list (handoffFactSummary), not the MessagePartText discriminator",
			Fragments: vocablint.FileFragments("backend/internal/version/infrastructure/postgres/handoff_repo.go", `"summary", "text", "value", "decision", "title"`),
		},
		"question": {
			Reason:    "handoff open-item Kind (a question gap), not the message/question kind vocabulary",
			Fragments: vocablint.FileFragments("backend/internal/version/application/handoff.go", `Kind: "question"`),
		},
		"requirement": {
			Reason: "the handoff open-item Kind (handoff-local) and the fact-payload key (an unowned cross-context convention). The MessageRef.Kind ref predicate now sources version.MessageRefKindRequirement, so it is NOT exempted here. Of the nine constants whose value is 'requirement' (ConfirmationScopeRequirement, chain.NodeRequirement, delivery.IssueRefKindRequirement, design.SourceRequirement, ...), none owns the fact-payload-key vocabulary.",
			Fragments: vocablint.JoinFragments(
				vocablint.FileFragments("backend/internal/version/application/handoff.go", `Kind: "requirement"`),
				vocablint.FileFragments("backend/internal/version/infrastructure/postgres/handoff_repo.go", `"requirement_id", "requirement"`, `v.payload->>'requirement'`, `v.payload->>'requirement'`),
			),
		},
		"open": {
			Reason:    "handoff-local vocabulary, not an execution intent status: the open-item Status (an unresolved gap) and the 'open' block name in the handoff.updated event sections list",
			Fragments: vocablint.FileFragments("backend/internal/version/application/handoff.go", `Status: "open"`, `"requirements", "qa", "decisions", "open", "plan", "resources"`),
		},
	}
}

// TestHandoffNoHardcodedDomainLiterals is the guard: no handoff production file may
// re-hardcode a derived domain value as a quoted literal outside a
// fragment-scoped, occurrence-scoped, justified allowlist entry; and every
// allowlist fragment must be used.
func TestHandoffNoHardcodedDomainLiterals(t *testing.T) {
	values := deriveDomainConstantValues(t)
	// Stale exemptions are reported BEFORE the findings Fatalf below, so a run
	// that both has a hardcoded literal AND a stale exemption reports both.
	scan := vocablint.ScanSurface(versionGateRepoRootT(t), handoffGuardedFiles(), values, handoffLiteralAllowlist())
	for _, problem := range scan.Problems {
		t.Errorf("%s", problem)
	}
	all := scan.Findings
	if len(all) > 0 {
		for _, f := range all {
			t.Errorf("%s:%d hardcodes %q (%s); source it from the domain constant instead",
				f.File, f.Line, f.Value, f.Name)
		}
		t.Fatalf("%d hardcoded domain literal(s) found in the handoff", len(all))
	}
}

// TestHandoffGuardedFilesExist keeps the scan set honest: every guarded production
// file must exist and parse (a renamed file must not silently drop out of scope).
func TestHandoffGuardedFilesExist(t *testing.T) {
	root := versionGateRepoRootT(t)
	for _, file := range handoffGuardedFiles() {
		if _, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, filepath.FromSlash(file)), nil, 0); err != nil {
			t.Fatalf("guarded handoff file %s does not parse: %v", file, err)
		}
	}
	for _, file := range handoffVocabularyFiles() {
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatalf("vocabulary file %s does not parse: %v", file, err)
		}
		if len(f.Decls) == 0 {
			t.Fatalf("vocabulary file %s has no declarations", file)
		}
	}
	// The AST parse above is also the guard against a vocabulary file that moved:
	// plan_item.go must be in the set (T3 minor ②).
	found := false
	for _, file := range handoffVocabularyFiles() {
		if strings.HasSuffix(file, "plan_item.go") {
			found = true
		}
	}
	if !found {
		t.Fatal("version/domain/plan_item.go is not in the handoff vocabulary set; plan-item statuses would be unprotected")
	}
}
