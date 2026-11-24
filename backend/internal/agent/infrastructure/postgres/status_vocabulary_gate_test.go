package agentpg

// V-0017 (H-B2) vocabulary gate: the agent work-queue repositories must read
// every status vocabulary through the agent domain constants (single source,
// constitution 五.1/五.4), never through hand-written literals -- neither Go
// comparison/assignment literals nor SQL-inline quoted literals. B's downstream
// invariant (19 号 §4 invariant 1) forbids SQL-inline status predicates even when
// they are equal today: a constant rename or a new status value would leave the
// SQL and its literal-seeded tests green while the results silently diverge.
//
// Scope: EVERY non-test Go file of this package, derived (T4 minor①) — the H-B2
// debt anchors workqueue.go/job_control.go/agent_center.go/query_reader.go plus
// runtime.go and any future repository file. A new file joins the scan automatically.
//
// The guarded value set is DERIVED, not hand-copied (方法纪律 ④/⑤/⑧): it is
// parsed from the agent/domain const blocks by DECLARED TYPE (WorkItemStatus,
// AttemptStatus, AgentRunStatus -- the three status vocabularies the four files
// read), honoring type-elided and value-elided const specs so a NEW status
// constant joins the guarded set automatically. The API lifecycle value
// JobStateCancelled (the query_reader DTO state filter, whose authority is the
// application constant agentapp.JobStateCancelled, not a work_item status) joins
// the set by reference so a rogue inline 'cancelled' is caught too.
//
// A guarded-type constant whose value is NOT a string literal (an alias, a
// conversion, a cross-package reference) cannot be resolved to a string by the
// AST walk. Instead of silently skipping it -- which would blind the gate to an
// alias-defined status -- the derivation fails LOUD unless the constant is
// explicitly classified in nonLiteralAgentStatusConstants with a reason (B T28
// handoffConstantNeedsExplicitClassification / T3 minor④ shape). A NEW
// non-literal constant fires until it is classified; a stale allowlist entry is
// reported so a repaid debt forces lockstep cleanup.
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
// file keeps the agent SCOPE, its guarded types and its adjudicated exemptions.
// The gate-local copies of agentOccurrenceSanction/agentAllIndices and of the
// type-filtered AST value walk are gone (constitution 五.1/五.3 存量违例清偿).
//
// Mutation evidence: this gate was written BEFORE the H-B2 literal->constant
// repayment and failed on the then-live SQL literals (workqueue claim/complete/
// fail/recover predicates, job_control cancel/retry, agent_center run-stat
// FILTERs, query_reader dead/failed/cancelled filters) and the Go assignment
// literals (workqueue.go attemptStatus/status). Re-injecting any literal
// reproduces the failure; the synthetic occurrence-scoped, type-elided and
// non-literal-classification unit tests below are the permanent deterministic
// proof.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"
)

// After H-B2 every work-queue status SQL mirror is parameterized (domain
// constant bound as a query argument) and every Go status literal reads the
// domain constant, so no occurrence in the guarded files needs sanctioning. The
// authority DEFINITIONS live in agent/domain, which this gate does not scan. A
// future legitimate collision (a non-status string that happens to equal a
// guarded value) must be added here with a fragment that carries exactly its own
// occurrences.
//
// Each fragment MUST be the exact text of ONE source line: the sanction
// algorithm evaluates one whitespace-normalized line at a time (T2 minor③).
var agentStatusExemptions = []vocablint.Exemption{}

// agentStatusGuardedFiles derives the guarded file set: EVERY non-test Go file
// of this package (H-B2 debt anchors workqueue/job_control/agent_center/
// query_reader plus runtime.go and any future repository file). A closed list
// would let a new repository file silently escape the gate (T4 minor①); the
// derivation covers it automatically. The count floor is a derivation sanity
// check, not a coverage list.
func agentStatusGuardedFiles(t *testing.T) []string {
	t.Helper()
	files, err := vocablint.GoFilesInDir(".")
	if err != nil {
		t.Fatalf("read agentpg package dir: %v", err)
	}
	if len(files) < 4 {
		t.Fatalf("only %d guarded files derived (%v); the derivation is broken and the gate would go vacuous", len(files), files)
	}
	return files
}

// agentStatusTypes are the agent-domain status vocabularies the guarded files
// read. This is a list of AUTHORITIES, not a closed value table: every constant
// of each type joins the guarded set automatically, including new ones. The
// numbers are sanity floors so a broken derivation fails loud instead of going
// vacuous.
var agentStatusTypes = map[string]int{
	"WorkItemStatus": 7,
	"AttemptStatus":  6,
	"AgentRunStatus": 5,
}

// nonLiteralAgentStatusConstants allowlists guarded-type constants whose value
// is NOT a string literal (a conversion, an alias, a cross-package reference)
// and which are consciously known NOT to be a status the gate must derive. The
// AST walk cannot resolve a non-literal value to a string, so instead of
// silently skipping it the derivation fails loud unless the constant is listed
// here with a reason. Empty today: every WorkItemStatus/AttemptStatus/
// AgentRunStatus constant is a plain string literal.
func nonLiteralAgentStatusConstants() map[string]string {
	return map[string]string{}
}

// agentStatusNeedsExplicitClassification reports whether a guarded-type constant
// value must be explicitly classified: it is a non-literal expression (the gate
// cannot derive its string value) AND it is not in the non-literal allowlist.
// This is the exact condition that turns a silent skip into a loud failure,
// extracted so the decision can be proven on synthetic AST
// (TestWorkQueueStatusNonLiteralClassification).
func agentStatusNeedsExplicitClassification(name string, value ast.Expr, allowlist map[string]string) bool {
	if _, ok := value.(*ast.BasicLit); ok {
		return false
	}
	_, allowed := allowlist[name]
	return !allowed
}

func TestWorkQueueStatusUsesDomainConstant(t *testing.T) {
	values := deriveAgentStatusValues(t)
	if len(values) == 0 {
		t.Fatal("no agent status values derived; the gate would go vacuous")
	}

	type finding struct{ site, line, value string }
	var findings []finding
	exemptionHits := make(map[int]int)

	for _, name := range agentStatusGuardedFiles(t) {
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
			remaining, hits := vocablint.OccurrenceSanction(lines[s.Line], s.Token, agentStatusExemptions)
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
		t.Errorf("V-0017/H-B2: agent work queue reads status value %s as a hand-written literal at %s: %s -- use the domain constant (Go predicate) or pass it as a query parameter (SQL)", f.value, f.site, f.line)
	}
	for _, problem := range vocablint.ExemptionShapeProblems(agentStatusExemptions) {
		t.Errorf("V-0017/H-B2: %s", problem)
	}
	for _, problem := range vocablint.StaleExemptionProblems(agentStatusExemptions, exemptionHits) {
		t.Errorf("V-0017/H-B2: %s", problem)
	}
}

// deriveAgentStatusValues parses every agent/domain source file and collects the
// string values of the guarded status types, deduplicated, plus the API
// lifecycle value JobStateCancelled by reference. Each type must contribute at
// least its sanity floor, and every non-literal guarded-type constant must be
// explicitly classified, so a broken derivation fails loud instead of going
// vacuous.
func deriveAgentStatusValues(t *testing.T) []string {
	t.Helper()
	root := agentStatusGateRepoRoot(t)
	domainDir := filepath.Join(root, "backend", "internal", "agent", "domain")
	entries, err := os.ReadDir(domainDir)
	if err != nil {
		t.Fatalf("read agent domain dir: %v", err)
	}
	allowlist := nonLiteralAgentStatusConstants()
	seenNonLiteral := make(map[string]bool)
	perType := map[string][]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), filepath.Join(domainDir, e.Name()), nil, 0)
		if parseErr != nil {
			t.Fatalf("parse agent domain %s: %v", e.Name(), parseErr)
		}
		for typeName := range agentStatusTypes {
			values, nonLiteral := deriveAgentStatusesByType(t, file, typeName)
			perType[typeName] = append(perType[typeName], values...)
			for name, expr := range nonLiteral {
				if agentStatusNeedsExplicitClassification(name, expr, allowlist) {
					t.Errorf("V-0017/H-B2: guarded-type constant %s (%s) has a non-literal value (%T) the status gate cannot resolve; "+
						"if it is a status, define it as a string literal so the vocabulary can derive it, "+
						"otherwise classify it in nonLiteralAgentStatusConstants with a reason", name, typeName, expr)
				}
				seenNonLiteral[name] = true
			}
		}
	}
	for name := range allowlist {
		if !seenNonLiteral[name] {
			t.Errorf("V-0017/H-B2: non-literal allowlist entry %s is unused (constant removed/renamed or now a string literal); remove it", name)
		}
	}
	for _, problem := range vocablint.TypeFloorProblems(perType, agentStatusTypes) {
		t.Fatalf("V-0017/H-B2: %s", problem)
	}
	values := vocablint.DedupValues(perType, vocablint.SortedKeys(agentStatusTypes))
	// The query_reader DTO state filter reads the API lifecycle value, whose
	// authority is the application constant (not a work_item status). Guard it by
	// reference so a rogue inline 'cancelled' is caught without a hand-copied value.
	if !slices.Contains(values, agentapp.JobStateCancelled) {
		values = append(values, agentapp.JobStateCancelled)
	}
	return values
}

// deriveAgentStatusesByType collects the string values of constants whose
// declared type is typeName through the shared elision-aware walk: a spec with no
// type inherits the previous spec's type within the same const group, and a spec
// with no values repeats the previous expression list. H-F2 shape: a status
// written with an elided type still joins the set. Guarded-type constants whose
// value is not a string literal are returned in nonLiteral (name -> value
// expression) so the caller can fail loud unless classified.
func deriveAgentStatusesByType(t *testing.T, file *ast.File, typeName string) (values []string, nonLiteral map[string]ast.Expr) {
	t.Helper()
	nonLiteral = map[string]ast.Expr{}
	for _, spec := range vocablint.ConstSpecs(file, token.CONST) {
		if spec.Type != typeName {
			continue
		}
		for i, name := range spec.Names {
			if i >= len(spec.Exprs) {
				continue
			}
			text, isString, err := vocablint.StringValue(spec.Exprs[i])
			if err != nil {
				t.Fatalf("unquote %s literal (%s): %v (derivation broken; the gate would go vacuous)", typeName, name, err)
			}
			if isString {
				values = append(values, text)
			} else {
				nonLiteral[name] = spec.Exprs[i]
			}
		}
	}
	return values, nonLiteral
}

// TestWorkQueueStatusExemptionIsOccurrenceScoped is permanent deterministic proof
// of the occurrence-scoped semantics: a line carrying a sanctioned fragment AND a
// rogue occurrence of the same guarded value flags the rogue; overlapping
// fragments cannot double-count a sanctioned occurrence and clamp the rogue
// away; a legitimately repeated fragment sanctions both occurrences.
func TestWorkQueueStatusExemptionIsOccurrenceScoped(t *testing.T) {
	exemptions := []vocablint.Exemption{
		{Fragment: `WHERE status = 'running'`, Reason: "sanctioned work-item predicate"},
	}
	if remaining, _ := vocablint.OccurrenceSanction(`WHERE status = 'running'`, `'running'`, exemptions); remaining != 0 {
		t.Fatalf("sanctioned-only line: remaining = %d, want 0", remaining)
	}
	rogue := `WHERE status = 'running' AND attempt.status = 'running'`
	if remaining, _ := vocablint.OccurrenceSanction(rogue, `'running'`, exemptions); remaining != 1 {
		t.Fatalf("sanctioned+rogue line: remaining = %d, want 1 (rogue hidden by line-level exemption)", remaining)
	}
	if remaining, _ := vocablint.OccurrenceSanction(`WHERE attempt.status = 'running'`, `'running'`, exemptions); remaining != 1 {
		t.Fatalf("rogue-only line: remaining = %d, want 1", remaining)
	}
	overlap := []vocablint.Exemption{
		{Fragment: `status = 'running'`, Reason: "sub-string fragment"},
		{Fragment: `WHERE status = 'running'`, Reason: "super-string fragment overlapping the first"},
	}
	overlapRogue := `WHERE status = 'running' AND label = 'running'`
	if remaining, _ := vocablint.OccurrenceSanction(overlapRogue, `'running'`, overlap); remaining != 1 {
		t.Fatalf("overlapping fragments: remaining = %d, want 1 (rogue hidden by double-count + clamp)", remaining)
	}
	repeatedEx := []vocablint.Exemption{
		{Fragment: `status = 'running'`, Reason: "sanctioned work-item predicate"},
	}
	repeated := `WHERE status = 'running' OR status = 'running'`
	if remaining, _ := vocablint.OccurrenceSanction(repeated, `'running'`, repeatedEx); remaining != 0 {
		t.Fatalf("repeated sanctioned fragment: remaining = %d, want 0 (false positive)", remaining)
	}
}

// TestWorkQueueStatusDerivationHandlesTypeElidedConst is permanent deterministic
// proof that a status const written with an elided (inherited) type, and one
// with an elided value, still join the derived guarded set.
func TestWorkQueueStatusDerivationHandlesTypeElidedConst(t *testing.T) {
	src := `package p
type WorkItemStatus string
const (
	WorkItemOpen    WorkItemStatus = "open"
	WorkItemClaimed                = "claimed"
	WorkItemRunning                = "running"
)
const (
	WorkItemDone WorkItemStatus = "done"
	WorkItemFailed
)
`
	file, err := parser.ParseFile(token.NewFileSet(), "synthetic.go", src, 0)
	if err != nil {
		t.Fatalf("parse synthetic: %v", err)
	}
	got, nonLiteral := deriveAgentStatusesByType(t, file, "WorkItemStatus")
	want := []string{"open", "claimed", "running", "done", "done"}
	if len(got) != len(want) {
		t.Fatalf("type-elided derivation = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("type-elided derivation = %v, want %v", got, want)
		}
	}
	if len(nonLiteral) != 0 {
		t.Fatalf("type-elided derivation reported non-literal constants %v, want none", nonLiteral)
	}
}

// TestWorkQueueStatusNonLiteralClassification is permanent deterministic proof of
// the loud-classification rule (T3 minor④): a guarded-type constant whose value
// is an alias/conversion (not a string literal) fires unless explicitly
// classified; an allowlisted one is accepted; the predicate is the exact
// decision the derivation applies.
func TestWorkQueueStatusNonLiteralClassification(t *testing.T) {
	src := `package p
type AgentRunStatus string
const (
	AgentRunRunning AgentRunStatus = "running"
	AgentRunAborted                = someAlias
)
`
	file, err := parser.ParseFile(token.NewFileSet(), "synthetic.go", src, 0)
	if err != nil {
		t.Fatalf("parse synthetic: %v", err)
	}
	values, nonLiteral := deriveAgentStatusesByType(t, file, "AgentRunStatus")
	if len(values) != 1 || values[0] != "running" {
		t.Fatalf("literal derivation = %v, want [running]", values)
	}
	expr, ok := nonLiteral["AgentRunAborted"]
	if !ok {
		t.Fatal("alias-defined AgentRunAborted was silently skipped; the gate must see it as non-literal")
	}
	// Unclassified: fires loud.
	if !agentStatusNeedsExplicitClassification("AgentRunAborted", expr, map[string]string{}) {
		t.Fatal("unclassified non-literal guarded constant did not fire; the gate would go blind to an alias-defined status")
	}
	// Classified: accepted.
	allow := map[string]string{"AgentRunAborted": "alias of a non-status value; consciously classified"}
	if agentStatusNeedsExplicitClassification("AgentRunAborted", expr, allow) {
		t.Fatal("allowlisted non-literal constant still fires; classification is not honored")
	}
	// A plain string literal never needs classification.
	if agentStatusNeedsExplicitClassification("AgentRunRunning", &ast.BasicLit{Kind: token.STRING, Value: `"running"`}, map[string]string{}) {
		t.Fatal("string-literal constant flagged as needing classification")
	}
}

// agentStatusGateRepoRoot walks up from this test's source file to the repository
// root (the directory containing backend/go.mod), so the gate works regardless
// of the test's working directory.
func agentStatusGateRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate agent status vocabulary gate source")
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
