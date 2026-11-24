package executionpg

// V-0014 (G share, v0.2.4-G T3) vocabulary gate: the execution context must
// read VERSION status through the version domain constants (single source,
// constitution 五.1/五.4), never through hand-written literals -- neither Go
// comparison literals nor SQL-inline quoted literals. B's downstream invariant
// (19 号 §4 invariant 1) forbids SQL-inline status predicates even when they are
// equal today: a constant rename or a new status value would leave the SQL and
// its literal-seeded tests green while the results silently diverge.
//
// The guarded value set is DERIVED, not hand-copied (方法纪律 ④/⑤/⑧): it comes
// from version.Statuses(), the V-0017 single authority for the version status
// enum, so a new version status constant joins the guarded set automatically.
//
// A literal is flagged when it equals a guarded value exactly (Go predicate
// literal) or contains the SQL single-quoted form 'value' (SQL-inline
// predicate). Struct tags are stripped mechanically (json:"running" is a wire
// name, not a status predicate). Exemptions are OCCURRENCE-scoped with a reason
// (方法纪律 ⑥, H-F1 execution share): a fragment sanctions only the guarded-token
// occurrences it actually carries (position coverage), so a rogue version-status
// predicate sharing a line with a sanctioned authority definition is still
// flagged. Every exemption must match at least one real occurrence (non-vacuous):
// when an exempted debt is repaid the stale exemption fails the gate and must be
// removed in the same change set.
//
// Scope note: the execution context owns its OWN status vocabularies that
// share values with the version status enum -- wave/intent/case_run/step
// "running" and intent purpose "initial". Their authority definitions
// (execution/domain) legitimately spell the value once. The SQL mirrors of
// those execution-own statuses in wave.go/case_run.go were the V-0030-class
// debt routed H (H-A10); they are now parameterized (domain constants bound as
// query arguments) and guarded by TestExecutionOwnStatusSQLUsesDomainConstant
// (own_status_vocabulary_gate_test.go). The SQL-mirror fragment exemptions that
// used to live here were removed in the same change set as the parameterization
// (the non-vacuous check below forces the lockstep). The stage gate constants in
// execution/application are DERIVED from version.StageExecuting/StageCompleted
// (stage vocabulary, a different enum that happens to share the string
// "completed"), so they need no exemption.
//
// T11e-0: the ALGORITHM (occurrence sanction, AST site collection, scan-surface
// derivation) now lives in platform/vocablint as the single authority every
// context's gate calls; this file keeps the execution SCOPE and its adjudicated
// exemptions. The gate-local copies of executionOccurrenceSanction /
// executionAllIndices are gone (constitution 五.1/五.3 存量违例清偿).
//
// Mutation evidence: this gate was written BEFORE the T3 literal->constant
// repayment and failed on the then-live literals (application/intent.go
// completed/running reads + stage literal, supersede.go completed read,
// postgres/intent.go completed read + active-status/purpose SQL literals).
// Re-injecting any of those literals reproduces the failure.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// The execution-own authority definitions legitimately spell the shared value
// once each; the fragments below carry exactly those definition lines
// (occurrence-scoped, H-F1 execution share — a rogue predicate sharing a line
// with a definition is still flagged, unlike the old line-level Contains).
//
// Each fragment MUST be the exact text of ONE source line: the sanction
// algorithm evaluates one whitespace-normalized line at a time (T2 minor③).
var executionStatusExemptions = []vocablint.Exemption{
	{Fragment: `WaveRunning WaveStatus = "running"`, Reason: "wave-status authority definition (execution/domain/wave.go), not a version status"},
	{Fragment: `IntentRunning IntentStatus = "running"`, Reason: "intent-status authority definition (execution/domain/intent.go), not a version status"},
	{Fragment: `PurposeInitial Purpose = "initial"`, Reason: "intent-purpose authority definition (execution/domain/intent.go), not a version status"},
	{Fragment: `StepRunning StepStatus = "running"`, Reason: "step-status authority definition (execution/domain/result.go), not a version status"},
	{Fragment: `CaseRunRunning CaseRunStatus = "running"`, Reason: "case_run-status authority definition (execution/domain/case_run.go), not a version status"},
}

func TestExecutionStatusReadsUseDomainConstant(t *testing.T) {
	root := executionGateRepoRoot(t)
	values := version.Statuses()
	if len(values) < 3 {
		t.Fatalf("guarded value set suspiciously small: %v (derivation broken?)", values)
	}

	type finding struct{ site, line, value string }
	var findings []finding
	exemptionHits := make(map[int]int)

	executionDir := filepath.Join(root, "backend", "internal", "execution")
	files, err := vocablint.GoFilesUnder(executionDir)
	if err != nil {
		t.Fatalf("scan execution context: %v", err)
	}
	for _, path := range files {
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			t.Fatalf("relative path for %s: %v", path, relErr)
		}
		rel = filepath.ToSlash(rel)
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s: %v", rel, readErr)
		}
		lines := vocablint.SourceLines(string(src))
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", rel, parseErr)
		}
		sites, problems := vocablint.ASTSites(rel, file, fset, lines, values)
		for _, problem := range problems {
			t.Fatalf("%s", problem)
		}
		for _, s := range sites {
			remaining, hits := vocablint.OccurrenceSanction(lines[s.Line], s.Token, executionStatusExemptions)
			for _, h := range hits {
				exemptionHits[h]++
			}
			if remaining > 0 {
				findings = append(findings, finding{
					site:  rel + ":" + strconv.Itoa(s.Line+1),
					line:  strings.TrimSpace(lines[s.Line]),
					value: s.Token,
				})
			}
		}
	}

	for _, f := range findings {
		t.Errorf("V-0014: execution reads version status value %q as a hand-written literal at %s: %s -- use the version domain constant (Go predicate) or pass it as a query parameter (SQL)", f.value, f.site, f.line)
	}
	// Non-vacuous, well-formed exemptions: a stale exemption means the exempted
	// debt was repaid (or moved) and the registry must be updated in the same
	// change set. Hits are keyed by exemption INDEX (T4 minor③ shape).
	for _, problem := range vocablint.ExemptionShapeProblems(executionStatusExemptions) {
		t.Errorf("V-0014: %s", problem)
	}
	for _, problem := range vocablint.StaleExemptionProblems(executionStatusExemptions, exemptionHits) {
		t.Errorf("V-0014: %s", problem)
	}
}

// TestExecutionStatusExemptionIsOccurrenceScoped is permanent deterministic proof
// of the H-F1 execution-share hardening: a line carrying a sanctioned authority
// definition AND a rogue occurrence of the same shared value flags the rogue
// (the old line-level Contains exempted the whole line).
func TestExecutionStatusExemptionIsOccurrenceScoped(t *testing.T) {
	definition := `WaveRunning WaveStatus = "running"`
	if remaining, _ := vocablint.OccurrenceSanction(definition, `"running"`, executionStatusExemptions); remaining != 0 {
		t.Fatalf("sanctioned definition line: remaining = %d, want 0", remaining)
	}
	rogue := `WaveRunning WaveStatus = "running"; var rogue = "running"`
	if remaining, _ := vocablint.OccurrenceSanction(rogue, `"running"`, executionStatusExemptions); remaining != 1 {
		t.Fatalf("definition+rogue line: remaining = %d, want 1 (rogue hidden by line-level exemption)", remaining)
	}
	if remaining, _ := vocablint.OccurrenceSanction(`var rogue = "running"`, `"running"`, executionStatusExemptions); remaining != 1 {
		t.Fatalf("rogue-only line: remaining = %d, want 1", remaining)
	}
}

// TestIntentDedupActiveSetMatchesDomain pins the dedup pre-check's active set
// against the domain authority: IntentStatus.Active() decides which statuses
// occupy the partial-unique slot, and intentActiveStatuses (the query parameter
// of the under-lock dedup pre-check and ListActive) must contain EXACTLY the
// active statuses. The IntentStatus constants are enumerated from the domain
// source via go/ast (方法纪律 ⑧: a hand-copied closed table cannot catch a NEW
// status constant; parsing the const block can), so adding a status that
// Active() accepts without joining the query set fails this gate.
func TestIntentDedupActiveSetMatchesDomain(t *testing.T) {
	root := executionGateRepoRoot(t)
	path := filepath.Join(root, "backend", "internal", "execution", "domain", "intent.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse intent status authority: %v", err)
	}
	inSet := make(map[string]bool, len(intentActiveStatuses))
	for _, value := range intentActiveStatuses {
		inSet[value] = true
	}
	found := 0
	activeCount := 0
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			ident, ok := vs.Type.(*ast.Ident)
			if !ok || ident.Name != "IntentStatus" {
				continue
			}
			for _, value := range vs.Values {
				lit, ok := value.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				text, unquoteErr := strconv.Unquote(lit.Value)
				if unquoteErr != nil {
					t.Fatalf("unquote intent status: %v", unquoteErr)
				}
				status := execution.IntentStatus(text)
				if status.Active() {
					activeCount++
				}
				if inSet[text] != status.Active() {
					t.Errorf("intent status %q: in dedup active set = %v, but IntentStatus.Active() = %v -- the under-lock dedup pre-check must mirror the partial-unique active set exactly", text, inSet[text], status.Active())
				}
				found++
			}
		}
	}
	if found == 0 {
		t.Fatal("no IntentStatus constants found in execution/domain/intent.go; the derivation is broken and the pin would go vacuous")
	}
	// Reverse direction: an extra/typo value in the query set that is not an
	// active domain constant also breaks the mirror.
	if len(intentActiveStatuses) != activeCount {
		t.Errorf("dedup active set has %d values, but the domain declares %d active IntentStatus constants -- the sets must be equal", len(intentActiveStatuses), activeCount)
	}
}

// executionGateRepoRoot walks up from this test's source file to the repository
// root (the directory containing backend/go.mod), so the gate works regardless
// of the test's working directory.
func executionGateRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate execution status vocabulary gate source")
	}
	dir := filepath.Dir(file)
	for i := 0; i < 12; i++ {
		if _, err := os.Stat(filepath.Join(dir, "backend", "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("repo root containing backend/go.mod not found from " + file)
	return ""
}
