package executionpg

// V-0014 H residual (H-A10, V-0030-class) vocabulary gate: the wave/case_run
// repositories must read their OWN status vocabularies through the execution
// domain constants (single source, constitution 五.1/五.4), never through
// hand-written literals -- neither Go comparison literals nor SQL-inline quoted
// literals. B's downstream invariant (19 号 §4 invariant 1) forbids SQL-inline
// status predicates even when they are equal today: a constant rename or a new
// status value would leave the SQL and its literal-seeded tests green while the
// results silently diverge.
//
// Scope: EVERY non-test Go file of this package, DERIVED (T11e-0). It used to be
// the hand-written list {wave.go, case_run.go, human.go, query_reader.go} — the
// H-A10 debt anchors plus the H-A17 residual T3 left outside its write scope (the
// human_request status/kind mirrors and the case_run terminal FILTER/IN mirrors).
// A closed list lets a new repository file silently escape the gate (the T4
// minor① failure class, already fixed in the agent gate); the derivation covers
// all 15 files of this package today and any future one automatically. The
// widening exposed NO new literal: the finding set stayed empty (measured before
// and after, task-11e-0-report.md), and injecting a status literal into a
// newly-covered file (repo.go) makes the gate fire.
// The version-status reads of the whole execution context are guarded by
// TestExecutionStatusReadsUseDomainConstant (status_vocabulary_gate_test.go);
// this gate covers the execution-OWN vocabularies those files read.
//
// The guarded value set is DERIVED, not hand-copied (方法纪律 ④/⑤/⑧): it is
// parsed from the execution/domain const blocks by DECLARED TYPE (WaveStatus,
// CaseRunStatus, IntentStatus, SUTContextStatus, HumanRequestStatus, HumanKind --
// the status AND kind vocabularies the guarded files read), honoring type-elided
// and value-elided const specs so a NEW status/kind constant joins the guarded set
// automatically. HumanKind is a vocabulary for the same reason a status is: the
// SQL `kind IN (...)` lists re-stated the enum, so a new decision kind would have
// left the sweep and the todo read silently incomplete (the kind sets are
// additionally pinned against HumanKind.IsDecision by
// TestHumanKindSetsMatchDomain).
//
// Exemptions are OCCURRENCE-scoped with a reason (方法纪律 ⑥, H-F1 shape): a
// fragment sanctions only the guarded-token occurrences it actually carries
// (position coverage), so a rogue predicate sharing the same value on the same
// line is still flagged. Every exemption must sanction at least one real
// occurrence (non-vacuous): a repaid debt forces the stale exemption to be
// removed in the same change set (lockstep).
//
// T11e-0: the ALGORITHM (occurrence sanction, AST site collection, elision-aware
// const walk, scan-surface derivation, per-type anti-vacuity floors) now lives in
// platform/vocablint as the single authority every context's gate calls; this
// file keeps the execution-own SCOPE, its guarded types and its adjudicated
// exemptions. The gate-local copies of ownOccurrenceSanction/ownAllIndices and
// of the type-filtered AST value walk are gone (constitution 五.1/五.3 存量违例清偿).
//
// Mutation evidence: this gate was written BEFORE the H-A10 literal->constant
// repayment and failed on the then-live SQL literals in wave.go (intent claim,
// active-wave IN lists, the Counts FILTER mirrors) and case_run.go (running
// listings, lease guard, superseded filter, stale-context sweep). H-A17 extended
// the scan set to human.go/query_reader.go and the guarded types to
// HumanRequestStatus/HumanKind BEFORE their repayment: it failed on 43 live
// occurrences (human_request status/kind predicates, the resolver CASE, both
// expiry sweeps, the case_run verdict FILTER/IN denominators and the todo kind
// CASE/IN/ORDER BY). Re-injecting any literal reproduces the failure; the
// synthetic occurrence-scoped and type-elided unit tests below are the permanent
// deterministic proof.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"
)

// After H-A10/H-A17 every wave/case_run/human_request status and kind SQL mirror
// is parameterized (domain constant bound as a query argument), so no occurrence
// in the guarded files needs sanctioning. The authority DEFINITIONS live in
// execution/domain, which this gate does not scan. A future legitimate collision
// (a non-status string that happens to equal a guarded value) must be added here
// with a fragment that carries exactly its own occurrences.
var ownStatusExemptions = []vocablint.Exemption{}

// ownStatusGuardedFiles derives the guarded file set: EVERY non-test Go file of
// this package, so a new repository file cannot silently escape the gate.
func ownStatusGuardedFiles(t *testing.T) []string {
	t.Helper()
	files, err := vocablint.GoFilesInDir(".")
	if err != nil {
		t.Fatalf("read executionpg package dir: %v", err)
	}
	if problem := vocablint.CountFloorProblem("guarded files", len(files), 4); problem != "" {
		t.Fatalf("V-0014/H-A10+H-A17: %s", problem)
	}
	return files
}

// ownStatusTypes are the execution-domain status/kind vocabularies the guarded
// files read. This is a list of AUTHORITIES, not a closed value table: every
// constant of each type joins the guarded set automatically, including new ones.
var ownStatusTypes = map[string]int{
	"WaveStatus":         6, // sanity floor: the derivation must find at least the known constants
	"CaseRunStatus":      10,
	"IntentStatus":       7,
	"SUTContextStatus":   4,
	"HumanRequestStatus": 4,
	"HumanKind":          5,
}

func TestExecutionOwnStatusSQLUsesDomainConstant(t *testing.T) {
	values := deriveExecutionOwnStatuses(t)
	if len(values) == 0 {
		t.Fatal("no execution-own status values derived; the gate would go vacuous")
	}

	type finding struct{ site, line, value string }
	var findings []finding
	exemptionHits := make(map[int]int)

	for _, name := range ownStatusGuardedFiles(t) {
		path := name
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		lines := vocablint.SourceLines(string(src))
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", path, parseErr)
		}
		sites, problems := vocablint.ASTSites(path, file, fset, lines, values)
		for _, problem := range problems {
			t.Fatalf("%s", problem)
		}
		for _, s := range sites {
			remaining, hits := vocablint.OccurrenceSanction(lines[s.Line], s.Token, ownStatusExemptions)
			for _, h := range hits {
				exemptionHits[h]++
			}
			if remaining > 0 {
				findings = append(findings, finding{
					site:  name + ":" + strconv.Itoa(s.Line+1),
					line:  strings.TrimSpace(lines[s.Line]),
					value: s.Token,
				})
			}
		}
	}

	for _, f := range findings {
		t.Errorf("V-0014/H-A10+H-A17: execution reads its own status/kind value %s as a hand-written literal at %s: %s -- use the domain constant (Go predicate) or pass it as a query parameter (SQL)", f.value, f.site, f.line)
	}
	for _, problem := range vocablint.ExemptionShapeProblems(ownStatusExemptions) {
		t.Errorf("V-0014/H-A10+H-A17: %s", problem)
	}
	for _, problem := range vocablint.StaleExemptionProblems(ownStatusExemptions, exemptionHits) {
		t.Errorf("V-0014/H-A10+H-A17: %s", problem)
	}
}

// TestWaveActiveSetMatchesDomain pins the active-wave query set against the
// domain authority: WaveStatus.Active() decides which statuses occupy the
// single-active-wave partial unique slot, and activeWaveStatuses (the query
// parameter of GetActive/ListActive) must contain EXACTLY the active statuses.
// The WaveStatus constants are enumerated from the domain source via go/ast
// (方法纪律 ⑧: a hand-copied closed table cannot catch a NEW status constant),
// so adding a status that Active() accepts without joining the query set fails
// this gate. Same shape as TestIntentDedupActiveSetMatchesDomain.
func TestWaveActiveSetMatchesDomain(t *testing.T) {
	root := executionGateRepoRoot(t)
	path := filepath.Join(root, "backend", "internal", "execution", "domain", "wave.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse wave status authority: %v", err)
	}
	inSet := make(map[string]bool, len(activeWaveStatuses))
	for _, value := range activeWaveStatuses {
		inSet[value] = true
	}
	found := 0
	activeCount := 0
	for _, v := range deriveStatusValuesByType(t, file, "WaveStatus") {
		status := execution.WaveStatus(v)
		if status.Active() {
			activeCount++
		}
		if inSet[v] != status.Active() {
			t.Errorf("wave status %q: in active query set = %v, but WaveStatus.Active() = %v -- GetActive/ListActive must mirror the partial-unique active set exactly", v, inSet[v], status.Active())
		}
		found++
	}
	if found == 0 {
		t.Fatal("no WaveStatus constants found in execution/domain/wave.go; the derivation is broken and the pin would go vacuous")
	}
	if len(activeWaveStatuses) != activeCount {
		t.Errorf("active wave query set has %d values, but the domain declares %d active WaveStatus constants -- the sets must be equal", len(activeWaveStatuses), activeCount)
	}
}

// TestHumanKindSetsMatchDomain pins the two SQL kind sets of the human_request
// read/sweep surfaces against the domain authority: HumanKind.IsDecision()
// decides which kinds are v0.22 version-level decisions, the question read
// surface is {clarification_question} + decisions and the execution-todo/expiry
// set is {login_takeover} + decisions. The HumanKind constants are enumerated
// from the domain source via go/ast (方法纪律 ⑧: a hand-copied closed table cannot
// catch a NEW kind), so adding a decision kind without joining the query sets
// fails this gate — exactly the silent-incomplete-sweep failure the SQL `kind IN
// (...)` literals used to allow. Same shape as TestWaveActiveSetMatchesDomain.
func TestHumanKindSetsMatchDomain(t *testing.T) {
	values := deriveExecutionDomainValuesOfType(t, "HumanKind")
	inDecision := make(map[string]bool, len(humanDecisionKinds))
	for _, kind := range humanDecisionKinds {
		inDecision[string(kind)] = true
	}
	inQuestion := make(map[string]bool, len(humanQuestionKinds))
	for _, kind := range humanQuestionKinds {
		inQuestion[string(kind)] = true
	}
	inTodo := make(map[string]bool, len(humanTodoKinds))
	for _, kind := range humanTodoKinds {
		inTodo[string(kind)] = true
	}
	decisionCount := 0
	for _, value := range values {
		kind := execution.HumanKind(value)
		if kind.IsDecision() {
			decisionCount++
		}
		if inDecision[value] != kind.IsDecision() {
			t.Errorf("human kind %q: in decision query set = %v, but HumanKind.IsDecision() = %v", value, inDecision[value], kind.IsDecision())
		}
		wantQuestion := kind.IsDecision() || kind == execution.HumanClarificationQuestion
		if inQuestion[value] != wantQuestion {
			t.Errorf("human kind %q: in question read set = %v, want %v (decisions + clarification_question)", value, inQuestion[value], wantQuestion)
		}
		wantTodo := kind.IsDecision() || kind == execution.HumanLoginTakeover
		if inTodo[value] != wantTodo {
			t.Errorf("human kind %q: in execution-todo/expiry set = %v, want %v (decisions + login_takeover)", value, inTodo[value], wantTodo)
		}
	}
	if len(humanDecisionKinds) != decisionCount {
		t.Errorf("decision kind query set has %d values, but the domain declares %d decision kinds -- the sets must be equal", len(humanDecisionKinds), decisionCount)
	}
	if len(humanQuestionKinds) != decisionCount+1 || len(humanTodoKinds) != decisionCount+1 {
		t.Errorf("question/todo query sets have %d/%d values, want %d each (decisions + one own kind)", len(humanQuestionKinds), len(humanTodoKinds), decisionCount+1)
	}
}

// TestHumanResolverStatusSetMatchesDomain pins the Transition resolver CASE set
// against HumanRequestStatus.StoresResolver(): the SQL clears the resolver on
// pending and stores it exactly for the statuses the domain says store one. A
// status that starts storing a resolver cannot join the domain without joining
// the query set (and the Go branch reads the same predicate).
func TestHumanResolverStatusSetMatchesDomain(t *testing.T) {
	values := deriveExecutionDomainValuesOfType(t, "HumanRequestStatus")
	inSet := make(map[string]bool, len(humanResolverStatuses))
	for _, status := range humanResolverStatuses {
		inSet[string(status)] = true
	}
	want := 0
	for _, value := range values {
		status := execution.HumanRequestStatus(value)
		if status.StoresResolver() {
			want++
		}
		if inSet[value] != status.StoresResolver() {
			t.Errorf("human request status %q: in resolver query set = %v, but StoresResolver() = %v", value, inSet[value], status.StoresResolver())
		}
	}
	if len(humanResolverStatuses) != want {
		t.Errorf("resolver query set has %d values, but the domain declares %d resolver-storing statuses", len(humanResolverStatuses), want)
	}
}

// TestFourStateCaseRunSetMatchesDomain pins the execution quality-ratio
// denominator against CaseRunStatus.FourState(). Using the whole terminal set
// instead would silently dilute the pass rate with skipped/superseded process
// states; using a hand-copied four-value table would miss a NEW verdict state.
func TestFourStateCaseRunSetMatchesDomain(t *testing.T) {
	values := deriveExecutionDomainValuesOfType(t, "CaseRunStatus")
	inSet := make(map[string]bool, len(fourStateCaseRunStatuses))
	for _, status := range fourStateCaseRunStatuses {
		inSet[status] = true
	}
	want := 0
	for _, value := range values {
		status := execution.CaseRunStatus(value)
		if status.FourState() {
			want++
		}
		if inSet[value] != status.FourState() {
			t.Errorf("case_run status %q: in verdict denominator = %v, but CaseRunStatus.FourState() = %v", value, inSet[value], status.FourState())
		}
	}
	if len(fourStateCaseRunStatuses) != want {
		t.Errorf("verdict denominator has %d values, but the domain declares %d four-state verdicts", len(fourStateCaseRunStatuses), want)
	}
}

// deriveExecutionDomainValuesOfType enumerates the string values of one
// execution-domain status/kind type across every non-test domain source file. It
// fails loud when nothing is derived, so a moved/renamed authority cannot make a
// pin vacuous.
func deriveExecutionDomainValuesOfType(t *testing.T, typeName string) []string {
	t.Helper()
	root := executionGateRepoRoot(t)
	domainDir := filepath.Join(root, "backend", "internal", "execution", "domain")
	entries, err := os.ReadDir(domainDir)
	if err != nil {
		t.Fatalf("read execution domain dir: %v", err)
	}
	var values []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), filepath.Join(domainDir, e.Name()), nil, 0)
		if parseErr != nil {
			t.Fatalf("parse execution domain %s: %v", e.Name(), parseErr)
		}
		values = append(values, deriveStatusValuesByType(t, file, typeName)...)
	}
	if len(values) == 0 {
		t.Fatalf("no %s constants derived from execution/domain; the derivation is broken and the pin would go vacuous", typeName)
	}
	return values
}

// deriveExecutionOwnStatuses parses every execution/domain source file and
// collects the string values of the guarded status types, deduplicated. Each
// type must contribute at least its sanity floor, so a broken derivation fails
// loud instead of going vacuous.
func deriveExecutionOwnStatuses(t *testing.T) []string {
	t.Helper()
	root := executionGateRepoRoot(t)
	domainDir := filepath.Join(root, "backend", "internal", "execution", "domain")
	entries, err := os.ReadDir(domainDir)
	if err != nil {
		t.Fatalf("read execution domain dir: %v", err)
	}
	perType := map[string][]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), filepath.Join(domainDir, e.Name()), nil, 0)
		if parseErr != nil {
			t.Fatalf("parse execution domain %s: %v", e.Name(), parseErr)
		}
		for typeName := range ownStatusTypes {
			perType[typeName] = append(perType[typeName], deriveStatusValuesByType(t, file, typeName)...)
		}
	}
	for _, problem := range vocablint.TypeFloorProblems(perType, ownStatusTypes) {
		t.Fatalf("V-0014/H-A10+H-A17: %s", problem)
	}
	return vocablint.DedupValues(perType, vocablint.SortedKeys(ownStatusTypes))
}

// deriveStatusValuesByType collects the string values of constants whose declared
// type is typeName through the shared elision-aware walk: a spec with no type
// inherits the previous spec's type within the same const group, and a spec with
// no values repeats the previous expression list. H-F2 shape: a status written
// with an elided type still joins the set.
func deriveStatusValuesByType(t *testing.T, file *ast.File, typeName string) []string {
	t.Helper()
	var values []string
	for _, spec := range vocablint.ConstSpecs(file, token.CONST) {
		if spec.Type != typeName {
			continue
		}
		for _, expr := range spec.Exprs {
			text, isString, err := vocablint.StringValue(expr)
			if err != nil {
				t.Fatalf("unquote %s literal: %v (derivation broken; the gate would go vacuous)", typeName, err)
			}
			if isString {
				values = append(values, text)
			}
		}
	}
	return values
}

// TestOwnStatusExemptionIsOccurrenceScoped is permanent deterministic proof of
// the occurrence-scoped semantics: a line carrying a sanctioned fragment AND a
// rogue occurrence of the same guarded value flags the rogue; overlapping
// fragments cannot double-count a sanctioned occurrence and clamp the rogue
// away; a legitimately repeated fragment sanctions both occurrences.
func TestOwnStatusExemptionIsOccurrenceScoped(t *testing.T) {
	exemptions := []vocablint.Exemption{
		{Fragment: `WHERE status = 'running'`, Reason: "sanctioned wave predicate"},
	}
	if remaining, _ := vocablint.OccurrenceSanction(`WHERE status = 'running'`, `'running'`, exemptions); remaining != 0 {
		t.Fatalf("sanctioned-only line: remaining = %d, want 0", remaining)
	}
	rogue := `WHERE status = 'running' AND run.status = 'running'`
	if remaining, _ := vocablint.OccurrenceSanction(rogue, `'running'`, exemptions); remaining != 1 {
		t.Fatalf("sanctioned+rogue line: remaining = %d, want 1 (rogue hidden by line-level exemption)", remaining)
	}
	if remaining, _ := vocablint.OccurrenceSanction(`WHERE run.status = 'running'`, `'running'`, exemptions); remaining != 1 {
		t.Fatalf("rogue-only line: remaining = %d, want 1", remaining)
	}
	overlap := []vocablint.Exemption{
		{Fragment: `status = 'running'`, Reason: "sub-string fragment"},
		{Fragment: `WHERE status = 'running'`, Reason: "super-string fragment overlapping the first"},
	}
	// The rogue occurrence must NOT contain the sub-fragment itself (a fragment
	// that genuinely matches the rogue sanctions it by design); label = 'running'
	// shares only the guarded token.
	overlapRogue := `WHERE status = 'running' AND label = 'running'`
	if remaining, _ := vocablint.OccurrenceSanction(overlapRogue, `'running'`, overlap); remaining != 1 {
		t.Fatalf("overlapping fragments: remaining = %d, want 1 (rogue hidden by double-count + clamp)", remaining)
	}
	// A fragment that legitimately appears twice on a line sanctions both
	// occurrences (no false positive). The fragment here is the predicate without
	// the WHERE prefix, so it matches both sides of the OR.
	repeatedEx := []vocablint.Exemption{
		{Fragment: `status = 'running'`, Reason: "sanctioned wave predicate"},
	}
	repeated := `WHERE status = 'running' OR status = 'running'`
	if remaining, _ := vocablint.OccurrenceSanction(repeated, `'running'`, repeatedEx); remaining != 0 {
		t.Fatalf("repeated sanctioned fragment: remaining = %d, want 0 (false positive)", remaining)
	}
}

// TestOwnStatusDerivationHandlesTypeElidedConst is permanent deterministic
// proof that a status const written with an elided (inherited) type, and one
// with an elided value, still join the derived guarded set.
func TestOwnStatusDerivationHandlesTypeElidedConst(t *testing.T) {
	src := `package p
type WaveStatus string
const (
	WavePlanned   WaveStatus = "planned"
	WaveRunning              = "running"
	WavePaused               = "paused"
)
const (
	WaveDone WaveStatus = "done"
	WaveFailed
)
`
	file, err := parser.ParseFile(token.NewFileSet(), "synthetic.go", src, 0)
	if err != nil {
		t.Fatalf("parse synthetic: %v", err)
	}
	got := deriveStatusValuesByType(t, file, "WaveStatus")
	want := []string{"planned", "running", "paused", "done", "done"}
	if len(got) != len(want) {
		t.Fatalf("type-elided derivation = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("type-elided derivation = %v, want %v", got, want)
		}
	}
}
