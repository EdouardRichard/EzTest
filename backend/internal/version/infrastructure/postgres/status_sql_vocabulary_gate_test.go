package versionpg_test

// V-0030 / V-0017 / H-A17 class (H T11e-2) vocabulary gate for the VERSION context.
//
// Every version infrastructure production file must read a status/kind/stage
// vocabulary through its domain constant — never through a hand-written literal,
// neither a Go comparison/assignment literal nor a SQL-inline quoted literal.
// Constitution 五.1/五.3/五.4 (single authority, no hand-written second truth, no
// bypass); B's downstream invariant (19 号 §4 invariant 1) forbids SQL-inline
// status predicates even when they are equal today: a constant rename or a new
// status value would leave the SQL and its literal-seeded tests green while the
// results silently diverge.
//
// # History (扩面不弱化)
//
// T3 parameterized the InUse asset-status region of ../postgres.go; T11b closed
// the version-status transitions (MarkRunning / Close / ReopenIfCompleted), the
// initial-version projection read and the extraction cursor; T11c (Ruling
// T11b-R5) extended the same surface to ../attachment.go and ../closure_repo.go.
// Those guards scanned a HAND-PICKED four-file list against a NARROW vocabulary
// (version/requirement/attachment/extraction statuses). T11e-2 (R-H10 full-class
// closure, Ruling T11d-R1/R2) widens BOTH axes without weakening any assertion:
//
//   - the scan surface is now DERIVED from every non-test .go file under
//     backend/internal/version/infrastructure (postgres/, attachments/, adapters/,
//     contentstore/ and the top-level repositories, plus any future subdirectory);
//   - the protected value set is now the full R3 derivation over EVERY bounded
//     context's domain layer, so a version repository reading ANOTHER context's
//     vocabulary (e.g. conversation_message.role, a resource-kind selector) is
//     held to that context's authority too.
//
// The former narrow coverage assertions are preserved verbatim in
// TestVersionStatusSQLGuardCoversItsVocabularies (now checked against the R3
// superset). The handoff gate (handoff_repo_lint_test.go) keeps its own scope and
// adjudicated collisions; where this gate re-scans handoff_repo.go it reuses the
// SAME fragments and reasons (task-11d-report.md §4.3), never a second wording.
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

	"github.com/EdouardRichard/EzTest/backend/internal/platform/govocab"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil/vocabscope"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// versionStatusLiteralAllowlist adjudicates the genuine collisions between a
// domain value and an unrelated use of the same string in the version
// infrastructure. Each entry is fragment-scoped (occurrence coverage) so it can
// never exempt a live predicate that merely shares the line. Every fragment is the
// exact text of ONE source line (whitespace-normalized, so gofmt alignment does not
// matter). Repaid in the same change set (T11e-2): every SQL status predicate is now
// bound as a query parameter and every Go status read references its domain
// constant, so NO status predicate is exempted here. What remains is non-status
// vocabulary: extraction element kinds, resource-kind selectors, fact-payload keys,
// an XML tag name, import paths, an LLM wire role and a display placeholder.
//
// The handoff_repo.go entries reuse the SAME fragments and reasons as the handoff
// gate (handoffLiteralAllowlist in handoff_repo_lint_test.go), never a second
// wording (task-11d-report.md §4.3).
func versionStatusLiteralAllowlist() map[string]vocablint.AllowEntry {
	return vocabscope.VersionGateAllowlist()
}

// versionGateRepoRoot walks up from this test's source file to the repository root (the
// directory containing backend/go.mod), so the gate works regardless of the test's
// working directory. It returns an error rather than taking *testing.T so the
// cached derivation can call it too.
func versionGateRepoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("cannot locate the version infrastructure vocabulary gate source")
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

// versionGateRepoRootT is the test-facing wrapper.
func versionGateRepoRootT(t *testing.T) string {
	t.Helper()
	root, err := versionGateRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// versionGuardedFiles derives this gate's scan surface through the SHARED scope authority
// (vocabscope.GateContexts + vocabscope.GuardedFiles): the context list and the
// per-context anti-vacuity floors are no longer copied per gate (T11e-6 closed
// that second truth -- the orchestration gate's codeintel floor had drifted to 13
// while the tree holds 15). The file set inside each context is always derived.
func versionGuardedFiles(t *testing.T) []string {
	t.Helper()
	files, err := vocabscope.GuardedFiles(versionGateRepoRootT(t), vocabscope.GateContexts()["version"])
	if err != nil {
		t.Fatalf("V-0030/T11e-2: %v", err)
	}
	return files
}

// versionVocabularyOnce derives the protected value set ONCE per test binary (H-A22④). The whole
// pipeline -- resolver, domain-file walk, R3 + R3-E + R3-R + the residual
// registration, the enum authorities, the additive application-layer source and the
// per-type anti-vacuity floors -- is the SHARED authority in
// internal/testutil/vocabscope (T11e-6: four hand-wired copies collapsed into one).
// This gate keeps only its SCAN SURFACE and its assertions.
var versionVocabularyOnce = sync.OnceValue(func() vocablint.R3Vocabulary {
	root, err := versionGateRepoRoot()
	if err != nil {
		return vocablint.R3Vocabulary{Problems: []string{err.Error()}}
	}
	return vocabscope.Derive(root)
})

// deriveVersionVocabulary reports the cached derivation's problems through the test and hands
// back the shared vocabulary.
func deriveVersionVocabulary(t *testing.T) vocablint.R3Vocabulary {
	t.Helper()
	v := versionVocabularyOnce()
	for _, problem := range v.Problems {
		t.Errorf("V-0030/T11e-5: %s", problem)
	}
	if len(v.Problems) > 0 {
		t.Fatalf("%d vocabulary derivation problem(s)", len(v.Problems))
	}
	return v
}

// TestVersionStatusSQLNoHardcodedVocabulary is the guard.
func TestVersionStatusSQLNoHardcodedVocabulary(t *testing.T) {
	vocabulary := deriveVersionVocabulary(t)
	scan := vocablint.ScanSurface(versionGateRepoRootT(t), versionGuardedFiles(t), vocabulary.Values, versionStatusLiteralAllowlist())
	for _, problem := range scan.Problems {
		t.Errorf("V-0030/T11e-5: %s", problem)
	}
	if len(scan.Findings) > 0 {
		for _, f := range scan.Findings {
			t.Errorf("V-0030/T11e-5: version infrastructure reads %q as a hand-written literal at %s:%d (authorities: %s) -- use the domain constant (Go) or bind it as a query parameter (SQL)",
				f.Value, f.File, f.Line, f.Name)
		}
		t.Fatalf("%d hand-written status literal(s) in the version infrastructure", len(scan.Findings))
	}
}

// TestVersionStatusSQLGuardCoversItsVocabularies keeps the scan honest: the
// derivation must actually protect every status the guarded files read, so a
// vocabulary file that moves out of the set cannot silently unprotect a value (the
// T3 minor② failure mode). The expectations come from the authorities themselves,
// not from a hand-copied value table. Preserved verbatim from the pre-T11e-2 narrow
// gate; now checked against the R3 superset (扩面不弱化).
func TestVersionStatusSQLGuardCoversItsVocabularies(t *testing.T) {
	vocabulary := deriveVersionVocabulary(t)
	want := append([]string{}, version.Statuses()...)
	want = append(want,
		string(version.RequirementDraft), string(version.RequirementClarified),
		string(version.RequirementClosed), string(version.RequirementObsolete))
	want = append(want,
		string(versionapp.ExtractionRunPending), string(versionapp.ExtractionRunRunning),
		string(versionapp.ExtractionRunPaused), string(versionapp.ExtractionRunDone),
		string(versionapp.ExtractionRunFailed),
		string(versionapp.ExtractionSegmentPending), string(versionapp.ExtractionSegmentRunning),
		string(versionapp.ExtractionSegmentDone), string(versionapp.ExtractionSegmentFailed),
		string(versionapp.ExtractionSegmentSkipped))
	want = append(want,
		version.AttachmentPending, version.AttachmentProcessing, version.AttachmentDone,
		version.AttachmentFailed, version.AttachmentSkipped,
		version.AttachmentImageExtracted, version.AttachmentImageAnalyzing,
		version.AttachmentImageDone, version.AttachmentImageFailed, version.AttachmentImageSkipped)
	for _, value := range want {
		if _, ok := vocabulary.Values[value]; !ok {
			t.Errorf("status vocabulary value %q is not protected by the version gate; a re-hardcoded literal would stay green", value)
		}
	}
}

// TestVersionVocabularyIsSupersetOfNarrowGate is the T11e-5 non-weakening proof
// for deleting the T11e-2 union: EVERY string value the pre-T11e-2 narrow gate
// derived from its four vocabulary files must still be protected. The union existed
// because R3 could not see the untyped attachment status / image-status enums
// (H-A21①); R3-E now selects them natively, so the union's three DOMAIN files are
// gone — and this test derives the old narrow set independently and compares, so a
// future rule change that drops one of those values fails here rather than silently
// unprotecting a live predicate.
func TestVersionVocabularyIsSupersetOfNarrowGate(t *testing.T) {
	vocabulary := deriveVersionVocabulary(t)
	root := versionGateRepoRootT(t)
	resolver, err := govocab.New(filepath.Join(root, "backend"))
	if err != nil {
		t.Fatalf("narrow-gate resolver: %v", err)
	}
	narrowFiles := append(vocabscope.AdditiveVocabularyFiles(root),
		filepath.Join(root, "backend", "internal", "version", "domain", "version.go"),
		filepath.Join(root, "backend", "internal", "version", "domain", "requirement.go"),
		filepath.Join(root, "backend", "internal", "version", "domain", "attachment.go"))
	narrow := vocablint.DeriveVocabulary(resolver, narrowFiles, vocabscope.NonLiteralVocabulary())
	for _, problem := range narrow.Problems {
		t.Fatalf("narrow-gate derivation: %s", problem)
	}
	if len(narrow.Values) == 0 {
		t.Fatal("the narrow-gate comparison set is empty; this assertion would be vacuous")
	}
	for value := range narrow.Values {
		if _, ok := vocabulary.Values[value]; !ok {
			t.Errorf("value %q was protected by the pre-T11e-2 narrow gate and is no longer protected; "+
				"deleting the union weakened the guard", value)
		}
	}
}

// TestVersionVocabularySelectionIsR3 is permanent deterministic proof of the R3 selection on synthetic
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
func TestVersionVocabularySelectionIsR3(t *testing.T) {
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

// TestVersionVocabularyScanSurfaceIsDerived proves a NEW production file joins the
// scan surface without anyone editing a list (T4 minor① failure class).
func TestVersionVocabularyScanSurfaceIsDerived(t *testing.T) {
	files := versionGuardedFiles(t)
	for _, want := range []string{
		"backend/internal/version/infrastructure/postgres.go",
		"backend/internal/version/infrastructure/conversation.go",
		"backend/internal/version/infrastructure/query_reader.go",
		"backend/internal/version/infrastructure/requirement.go",
		"backend/internal/version/infrastructure/attachments/ooxml.go",
		"backend/internal/version/infrastructure/adapters/vision.go",
		"backend/internal/version/infrastructure/postgres/handoff_repo.go",
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
