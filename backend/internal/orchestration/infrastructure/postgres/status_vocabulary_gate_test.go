package orchestrationpg_test

// R-H10 / T11e-3 vocabulary gate for the orchestration + codeintel + knowledge +
// storage + design contexts (the batch-3 share of the owner-approved full-class
// closure of hand-written status literals).
//
// Every infrastructure production file of these five contexts must read a
// status/kind/stage vocabulary through its domain constant — never through a
// hand-written literal, neither a Go comparison/assignment literal nor a
// SQL-inline quoted literal. Constitution 五.1/五.3/五.4 (single authority, no
// hand-written second truth, no bypass); B's downstream invariant (19 号 §4
// invariant 1) forbids SQL-inline status predicates even when they are equal
// today: a constant rename or a new status value would leave the SQL and its
// literal-seeded tests green while the results silently diverge.
//
// This gate lives in the orchestration postgres test package because that is
// one of the five guarded contexts. The composition-root repository-level gate
// (cmd/eztest/repo_status_vocabulary_gate_test.go, T11e-6/H-A25) now stacks a
// global net over EVERY context's infrastructure on top of this one; it does not
// replace it. The five scan surfaces below are each DERIVED with
// vocablint.GoFilesUnder (never a hand-written file list), so a new production
// file in any of the five contexts joins the scan automatically.
// The design context's own-status gate (design/application/
// status_vocabulary_gate_test.go, V-0014/V-0030) is untouched and stays green:
// this gate is a strict value-set superset over the infra surface, not a
// replacement, and neither gate's assertions are weakened.
//
// # Value set (derived, Ruling T11d-R1 == R3, extended by T11e-5)
//
// Every backend/internal/*/domain/*.go non-test file through platform/govocab (the
// single resolver) + platform/vocablint (the single elision-aware const walk, the
// single loud-classification rule and the single R3 SELECTION authority). A
// declaration joins when:
//
//   - R3: its DECLARED TYPE name, its OWN name, or ANY name of its const/var GROUP
//     matches the semantic pattern (group inheritance is load-bearing: execution's
//     HumanPayloadReason carries no semantic name and joins through its sibling);
//   - R3-E: its group declares an EXPORTED name -- an exported domain constant is
//     published vocabulary. This is what closes the 19+ measured blind-spot groups
//     (agent.Team, chain.Layer*, execution.TerminalCause*/NextAction*,
//     version.Conversation*/MessagePart*/Attachment*, codeintel.EdgeRel,
//     knowledge.Scope, design.CaseTrigger*, storage.Format*, llm.Protocol, ...);
//   - R3-R: it is enumerated BY REFERENCE by a registry set (chain's unexported
//     count* constants join through chainCountKeys);
//   - or it is one of the residual groups registered by name in
//     vocabscope.Registrations (execution.executorLimitationCodes).
//
// A "every domain string" rule (R2) is still forbidden: it would swallow
// execution/domain/requirement_deviation.go's NLP word lists, which are UNEXPORTED
// literal lists and are provably excluded by every rule above (pinned by
// vocablint's TestR3RulesExcludeRealNLPWordLists).
//
// The enum authority functions reinforce by reference; the SHARED per-type
// anti-vacuity floors (vocabscope.TypeFloors) make a broken derivation fail
// loud, and a derived type with no floor entry is itself a problem. The pattern,
// the floors, the residual registration, the authority list and the whole
// derivation pipeline live in internal/testutil/vocabscope: T11e-5 Part B closed
// the four copies of the policy, and T11e-6 (M7, Ruling T11e-5-R2) moved that
// REPOSITORY-SPECIFIC policy out of the platform shared kernel and collapsed the
// four hand-wired derivations into vocabscope.Derive. platform/vocablint keeps
// the ALGORITHM only and imports no bounded context.
// # Exemptions (fragment-scoped, FILE-BOUND AND occurrence-scoped, 方法纪律 ⑥)
//
// A genuine collision (a DTO/wire JSON field name, a chat wire role, a log field
// key, an error-envelope field name, a URL scheme, a named legacy value) is
// exempted by the EXACT source fragment carrying it, BOUND TO THE FILE it lives in,
// with a specific reason. Only the occurrences a fragment spans are sanctioned, so
// a rogue predicate sharing the line still fires -- and the same text in ANOTHER
// file is a finding, not a free pass (T11e-5 Part A1 closed that hole: a short
// fragment used to sanction the whole merged surface, and the stale check stayed
// quiet because the original site still matched). Every fragment must sanction at
// least one real occurrence, tracked by VALUE + FRAGMENT INDEX so two fragments
// with identical text never share a count (Part A2). Value-level exemptions are
// forbidden.
//
// T11e-6 SETTLED the exposure T11e-5's R3 native coverage measured: every live
// predicate was repaid (bound as a query parameter or sourced from its domain
// constant) and every genuine collision was adjudicated into the permanent table
// above with a specific reason. The temporary routed registry AND its route-marker
// check (vocablint.PendingExposureProblems) are DELETED: with every site settled
// that failure mode no longer exists structurally, and keeping an uncalled check
// would be unwired code (T11e-6 review Minor 5). A future measured exposure
// re-introduces both together, in the change set that needs them.
//
// ONE FRAGMENT PER OCCURRENCE (T11e-6 M5 + review Minor 3): fragments that share a
// file and a text must be declared once per OCCURRENCE they sanction -- identified
// by (source line, index among that value's occurrences on the line), not by line
// alone. Two identical lines, or one line carrying the value twice (a frozen
// <section id="issues" data-section="issues">), each need their own fragment, so
// repaying any single occurrence is detected instead of hiding behind a twin.
// # Non-parameterizable shapes (Ruling T11d-R4)
//
// None in these five contexts: the only ON CONFLICT ... WHERE predicate is
// codeintel entity_repo.go's `WHERE fact_key <> ''`, which carries no vocabulary
// value (verified by grep over all five infra trees). The 42P10 generic-plan
// trap therefore does not apply here and no R4 value pin is needed.

import (
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil/vocabscope"
)

// vocabGateAllowlist adjudicates the genuine collisions between a domain value
// and an unrelated wire-format / prompt / log use of the same string. Each entry
// is fragment-scoped so it cannot exempt a live predicate, and every fragment is
// the exact text of ONE source line (whitespace-normalized, so gofmt alignment
// does not matter).
//
// Repaid in the same change set (T11e-3): every SQL status/kind/scope/trigger
// predicate of the five contexts is now bound as a query parameter and the one
// Go source-kind read references its domain constant, so NO reachable status
// predicate is exempted here. What remains is non-status vocabulary or
// cross-context vocabulary with no Go authority (each reason names the
// producer/consumer evidence).
//
// The table itself is vocabscope.OrchestrationGateAllowlist; its value/fragment
// counts are documented THERE and measured by the gate, never restated here
// (T11e-6 review Minor 1: this comment still carried the pre-T11e-3 numbers after
// the table moved, which is the very second-truth class this task closes).
func vocabGateAllowlist() map[string]vocablint.AllowEntry {
	return vocabscope.OrchestrationGateAllowlist()
}

// vocabGateGuardedFiles derives this gate's scan surface through the SHARED scope authority
// (vocabscope.GateContexts + vocabscope.GuardedFiles): the context list and the
// per-context anti-vacuity floors are no longer copied per gate (T11e-6 closed
// that second truth -- the orchestration gate's codeintel floor had drifted to 13
// while the tree holds 15). The file set inside each context is always derived.
func vocabGateGuardedFiles(t *testing.T) []string {
	t.Helper()
	files, err := vocabscope.GuardedFiles(vocabGateRepoRootT(t), vocabscope.GateContexts()["orchestration"])
	if err != nil {
		t.Fatalf("R-H10/T11e-3: %v", err)
	}
	return files
}

// vocabGateVocabularyOnce derives the protected value set ONCE per test binary (H-A22④). The whole
// pipeline -- resolver, domain-file walk, R3 + R3-E + R3-R + the residual
// registration, the enum authorities, the additive application-layer source and the
// per-type anti-vacuity floors -- is the SHARED authority in
// internal/testutil/vocabscope (T11e-6: four hand-wired copies collapsed into one).
// This gate keeps only its SCAN SURFACE and its assertions.
var vocabGateVocabularyOnce = sync.OnceValue(func() vocablint.R3Vocabulary {
	root, err := vocabGateRepoRoot()
	if err != nil {
		return vocablint.R3Vocabulary{Problems: []string{err.Error()}}
	}
	return vocabscope.Derive(root)
})

// deriveVocabGateVocabulary reports the cached derivation's problems through the test and hands
// back the shared vocabulary.
func deriveVocabGateVocabulary(t *testing.T) vocablint.R3Vocabulary {
	t.Helper()
	v := vocabGateVocabularyOnce()
	for _, problem := range v.Problems {
		t.Errorf("R-H10/T11e-5: %s", problem)
	}
	if len(v.Problems) > 0 {
		t.Fatalf("%d vocabulary derivation problem(s)", len(v.Problems))
	}
	return v
}

// TestStatusVocabularyUsesDomainConstant is the guard.
func TestStatusVocabularyUsesDomainConstant(t *testing.T) {
	vocabulary := deriveVocabGateVocabulary(t)
	scan := vocablint.ScanSurface(vocabGateRepoRootT(t), vocabGateGuardedFiles(t), vocabulary.Values, vocabGateAllowlist())
	for _, problem := range scan.Problems {
		t.Errorf("R-H10/T11e-5: %s", problem)
	}
	if len(scan.Findings) > 0 {
		for _, f := range scan.Findings {
			t.Errorf("R-H10/T11e-5: batch-3 infrastructure surface reads %q as a hand-written literal at %s:%d (authorities: %s) -- use the domain constant (Go) or bind it as a query parameter (SQL)",
				f.Value, f.File, f.Line, f.Name)
		}
		t.Fatalf("%d hand-written status literal(s) in the batch-3 infrastructure surface", len(scan.Findings))
	}
}

// TestVocabularySelectionIsR3 is permanent deterministic proof of the R3 selection on synthetic
// AST, now exercised through the SHARED selector (platform/vocablint): a
// type-named enum whose constants carry no semantic name joins (the R1 blind
// spot), a group whose only semantic name sits on a SIBLING joins by group
// inheritance, and an UNEXPORTED unrelated string group / NLP word list does not
// (the R2 degeneration T11d forbade).
//
// T11e-5: the unrelated group is unexported because R3-E now admits every
// EXPORTED domain const group as published vocabulary. The anti-R2 assertion is
// preserved on the shape that matters — the unexported implementation word lists
// of execution/domain/requirement_deviation.go.
func TestVocabularySelectionIsR3(t *testing.T) {
	src := `package p
type RequirementStatus string
const (
	RequirementDraft     RequirementStatus = "draft"
	RequirementClarified                   = "clarified"
)
const (
	HumanPayloadReason        = "reason"
	HumanPayloadChallengeKind = "challenge_kind"
)
const (
	summaryHeading = "Overview"
	summaryFooter  = "End"
)
var knownNodeKinds = []string{"requirement", "wave"}
var missingFeatureMarkers = []string{"missing", "not found"}
`
	file, err := parser.ParseFile(token.NewFileSet(), "synthetic.go", src, 0)
	if err != nil {
		t.Fatalf("parse synthetic: %v", err)
	}
	selector := vocabscope.Selector()
	groups := vocablint.ConstGroups(file, token.CONST)
	if len(groups) != 3 {
		t.Fatalf("const groups = %d, want 3", len(groups))
	}
	if !selector.GroupIsVocabulary(groups[0]) {
		t.Fatal("type-named RequirementStatus group not selected; R3 would miss RequirementDraft (the R1 blind spot)")
	}
	if len(groups[0]) != 2 || groups[0][1].Type != "RequirementStatus" {
		t.Fatalf("elided type not inherited: %+v", groups[0][1])
	}
	if !selector.GroupIsVocabulary(groups[1]) {
		t.Fatal("group inheritance failed: HumanPayloadReason must join because HumanPayloadChallengeKind shares its group")
	}
	if selector.GroupIsVocabulary(groups[2]) {
		t.Fatal("an unexported unrelated string group was selected; R3 would degenerate into 'every domain string' (R2)")
	}
	varGroups := vocablint.ConstGroups(file, token.VAR)
	if len(varGroups) != 2 || !selector.GroupIsVocabulary(varGroups[0]) {
		t.Fatalf("var set knownNodeKinds not selected: %+v", varGroups)
	}
	if selector.GroupIsVocabulary(varGroups[1]) {
		t.Fatal("an NLP word list was selected; Ruling T11d-R1 forbids treating it as vocabulary")
	}
}

// TestVocabularyScanSurfaceIsDerived proves a NEW production file joins the scan
// surface without anyone editing a list (T4 minor① failure class), for each of
// the five guarded contexts including their non-postgres subdirectories.
func TestVocabularyScanSurfaceIsDerived(t *testing.T) {
	files := vocabGateGuardedFiles(t)
	for _, want := range []string{
		"backend/internal/orchestration/infrastructure/postgres/repo.go",
		"backend/internal/orchestration/infrastructure/probe.go",
		"backend/internal/codeintel/infrastructure/postgres/entity_repo.go",
		"backend/internal/codeintel/infrastructure/llm/prompt.go",
		"backend/internal/codeintel/infrastructure/gitx/git.go",
		"backend/internal/knowledge/infrastructure/postgres/repo.go",
		"backend/internal/storage/infrastructure/gc.go",
		"backend/internal/storage/infrastructure/filestore.go",
		"backend/internal/design/infrastructure/postgres/confirm.go",
		"backend/internal/design/infrastructure/storageexport/adapter.go",
	} {
		if !slices.Contains(files, want) {
			t.Fatalf("derived scan surface is missing %s (%d files derived)", want, len(files))
		}
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			t.Fatalf("scan surface includes a test file: %s", file)
		}
	}
}

// vocabGateRepoRoot walks up from this test's source file to the repository root (the
// directory containing backend/go.mod), so the gate works regardless of the test's
// working directory. It returns an error rather than taking *testing.T so the
// cached derivation can call it too.
func vocabGateRepoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("cannot locate the batch-3 infrastructure surface vocabulary gate source")
	}
	dir := filepath.Dir(file)
	for i := 0; i < 12; i++ {
		if _, err := os.Stat(filepath.Join(dir, "backend", "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", errors.New("repo root containing backend/go.mod not found from " + file)
}

// vocabGateRepoRootT is the test-facing wrapper.
func vocabGateRepoRootT(t *testing.T) string {
	t.Helper()
	root, err := vocabGateRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	return root
}
