package executionapp

// V-0022 (F-share) consistency gate: the case_run TERMINAL VERDICT has exactly
// ONE sanctioned write entry per mode (constitution art.3.6 终态唯一入口):
//
//   - settlement      non-terminal -> terminal, via settleTerminalVerdict
//     (application/terminal.go) -> CaseRunRepository.UpdateCAS (dispatch fence,
//     CAS refuses already-terminal rows).
//   - reclassification terminal -> terminal (failed/blocked/inconclusive only),
//     via reclassifyTerminalVerdict (application/terminal.go) ->
//     CaseRunRepository.ReclassifyTerminal (guarded + fenced).
//
// Both verdicts are computed by the single domain authority execution.Aggregate;
// no production code may hand-write a terminal verdict status (assign a verdict
// constant to a Status field). Process states (skipped/superseded/interrupted)
// are written through the guarded, fenced Transition/CreateAndTransition
// primitives whose from-set guard is pinned at the SQL layer below; they are not
// verdict settlements. ONE sanctioned exception writes a verdict state through
// Transition: the scheduler capacity gate (Scheduler.terminateUnsatisfiableRun,
// scheduler.go) moves a pending run to blocked when its context requirement
// exceeds every reachable executor's capacity -- a deterministic, pending-only
// from-set guarded path with no model input (not an Aggregate verdict, not a
// hand-written bypass). It rides the same transitionCaseRun primitive labelled
// in the sanctioned-SQL map below.
//
// The gate has three layers, mirroring the version status_write_gate pattern:
//
//  1. SQL multiset: every production SQL literal that writes case_run.status
//     must be one of the sanctioned repository primitives, each appearing
//     EXACTLY once (sites are counted, never deduplicated). A new raw status
//     write anywhere fails; a vanished sanctioned write fails (no vacuous pass).
//     The generic Update primitive must carry the terminal-transition guard
//     (it can never move a row into or out of a terminal status); the shared
//     transition primitive must carry its from-set guard.
//  2. Verdict assignment scan: no production function in the execution
//     application layer may assign a TERMINAL VERDICT constant (passed/failed/
//     blocked/inconclusive, values derived from the domain constants) to a
//     case_run Status field. Verdicts flow as variables out of Aggregate into
//     the single entry; a hand-written constant is a bypass.
//  3. Entry call pins: the settlement entry must be called by the CaseRunner
//     write path, the human timeout/cancel path and the recovery budget-exhausted
//     path; the reclassification entry must be called by the deterministic
//     analysis fallback and the analyst refinement path. Removing an entry call
//     (re-introducing a bypass) fails the gate.
//
// Test fixtures are excluded: seeding state in _test.go is legitimate.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// terminalGateRoot walks up from this file to the repository root (the worktree).
func terminalGateRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve the terminal gate test source path")
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

// terminalVerdictValues derives the four terminal verdict status values from
// the domain constant block (execution/domain/case_run.go) instead of a
// hand-copied list: renaming a domain constant changes the scanned value set
// with it (method discipline: guards derive from the authority's values).
func terminalVerdictValues(t *testing.T, root string) map[string]string {
	t.Helper()
	path := filepath.Join(root, "backend", "internal", "execution", "domain", "case_run.go")
	file := parseTerminalGateFile(t, path)
	want := map[string]bool{
		"CaseRunPassed": true, "CaseRunFailed": true,
		"CaseRunBlocked": true, "CaseRunInconclusive": true,
	}
	values := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range spec.Names {
			if !want[name.Name] || i >= len(spec.Values) {
				continue
			}
			lit, ok := spec.Values[i].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatalf("unquote domain constant %s: %v", name.Name, err)
			}
			values[name.Name] = value
		}
		return true
	})
	if len(values) != len(want) {
		t.Fatalf("derived %d terminal verdict constants, want %d (domain constant block drifted?)", len(values), len(want))
	}
	return values
}

type terminalSQLSite struct {
	file string
	fn   string
	kind string
	sql  string
}

func (s terminalSQLSite) key() string { return s.file + "|" + s.fn + "|" + s.kind }

// sanctionedTerminalSQLWrites is the exact allowlist of production SQL sites
// that write case_run.status. The value documents what each primitive owns.
var sanctionedTerminalSQLWrites = map[string]string{
	"backend/internal/execution/infrastructure/postgres/case_run.go|insertCaseRun|insert":      "row creation (process status at insert)",
	"backend/internal/execution/infrastructure/postgres/case_run.go|Update|update":             "generic non-terminal mutation (terminal-transition guarded)",
	"backend/internal/execution/infrastructure/postgres/case_run.go|UpdateCAS|update":          "fenced settlement CAS (refuses terminal rows)",
	"backend/internal/execution/infrastructure/postgres/case_run.go|ReclassifyTerminal|update": "guarded fenced terminal reclassification CAS",
	"backend/internal/execution/infrastructure/postgres/case_run.go|transitionCaseRun|update":  "shared guarded from-set transition (process states + capacity-gate blocked)",
	"backend/internal/execution/infrastructure/postgres/wave.go|CreateWithCaseRuns|insert":     "wave planning inserts pending case_runs",
}

var (
	insertCaseRunRe       = regexp.MustCompile(`(?i)insert\s+into\s+case_run\b`)
	updateCaseRunRe       = regexp.MustCompile(`(?is)update\s+case_run\s+set\s+(.*?)(?:\s+where\b|\s+returning\b|$)`)
	caseRunStatusAssignRe = regexp.MustCompile(`(?i)(?:^|[,\s(])status\s*=`)
	caseRunFromGuardRe    = regexp.MustCompile(`(?is)where\b.*\bstatus\s*=\s*any`)
	// updateTerminalGuardRe pins the Update primitive's terminal-transition
	// guard: the target status must equal the stored one, or neither side may
	// be terminal (the terminal set arrives as a bound domain-derived array).
	updateTerminalGuardRe = regexp.MustCompile(`(?is)where\b.*status\s*=\s*\$2.*<>\s*all`)
)

func classifyCaseRunStatusWrite(sql string) (string, bool) {
	if insertCaseRunRe.MatchString(sql) {
		return "insert", true
	}
	if m := updateCaseRunRe.FindStringSubmatch(sql); m != nil {
		if caseRunStatusAssignRe.MatchString(m[1]) {
			return "update", true
		}
	}
	return "", false
}

func parseTerminalGateFile(t *testing.T, path string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return file
}

func terminalGateEnclosingFunc(file *ast.File, pos token.Pos) string {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if pos >= fn.Body.Pos() && pos <= fn.Body.End() {
			return fn.Name.Name
		}
	}
	return "<package>"
}

// TestTerminalSingleEntryGate is the V-0022 drift gate. A rogue direct terminal
// write (raw SQL, a hand-written verdict constant, or a removed entry call)
// fails it; the sanctioned entries must all be present and detected.
func TestTerminalSingleEntryGate(t *testing.T) {
	root := terminalGateRoot(t)

	// Layer 1: SQL multiset.
	var sites []terminalSQLSite
	for _, sub := range []string{"backend/internal", "backend/cmd"} {
		base := filepath.Join(root, filepath.FromSlash(sub))
		err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file := parseTerminalGateFile(t, path)
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				sql, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				kind, isWrite := classifyCaseRunStatusWrite(sql)
				if !isWrite {
					return true
				}
				sites = append(sites, terminalSQLSite{file: rel, fn: terminalGateEnclosingFunc(file, lit.Pos()), kind: kind, sql: sql})
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", sub, err)
		}
	}
	counts := map[string]int{}
	for _, site := range sites {
		counts[site.key()]++
	}
	var extra, duplicated, missing []string
	for key := range counts {
		if _, ok := sanctionedTerminalSQLWrites[key]; !ok {
			extra = append(extra, key)
		}
	}
	for key := range sanctionedTerminalSQLWrites {
		if counts[key] > 1 {
			duplicated = append(duplicated, key)
		}
		if counts[key] == 0 {
			missing = append(missing, key)
		}
	}
	sort.Strings(extra)
	sort.Strings(duplicated)
	sort.Strings(missing)
	for _, key := range extra {
		t.Errorf("V-0022: unsanctioned case_run.status SQL write at %s (%d site(s)); terminal verdicts may only be written through the single terminal entry", key, counts[key])
	}
	for _, key := range duplicated {
		t.Errorf("V-0022: sanctioned case_run.status write %q appears %d times; exactly one per primitive (co-located rogue write)", key, counts[key])
	}
	for _, key := range missing {
		t.Errorf("V-0022: sanctioned case_run.status write %q (%s) was not found; the gate cannot verify the single-entry invariant", key, sanctionedTerminalSQLWrites[key])
	}
	for _, site := range sites {
		switch site.key() {
		case "backend/internal/execution/infrastructure/postgres/case_run.go|Update|update":
			if !updateTerminalGuardRe.MatchString(site.sql) {
				t.Errorf("V-0022: the generic Update primitive lost its terminal-transition guard; a direct Update could write a terminal status")
			}
		case "backend/internal/execution/infrastructure/postgres/case_run.go|transitionCaseRun|update":
			if !caseRunFromGuardRe.MatchString(site.sql) {
				t.Errorf("V-0022: the shared transition primitive lost its from-set status guard")
			}
		case "backend/internal/execution/infrastructure/postgres/case_run.go|ReclassifyTerminal|update":
			if !caseRunFromGuardRe.MatchString(site.sql) {
				t.Errorf("V-0022: ReclassifyTerminal lost its reclassifiable-status WHERE guard")
			}
		}
	}

	// Layer 2: no hand-written terminal verdict constant assignment in the
	// execution application layer (verdicts flow as variables from Aggregate).
	values := terminalVerdictValues(t, root)
	appDir := filepath.Join(root, "backend", "internal", "execution", "application")
	entries, err := os.ReadDir(appDir)
	if err != nil {
		t.Fatalf("read application dir: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file := parseTerminalGateFile(t, filepath.Join(appDir, name))
		ast.Inspect(file, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok || assign.Tok != token.ASSIGN || len(assign.Lhs) != len(assign.Rhs) {
				return true
			}
			for i, lhs := range assign.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Status" {
					continue
				}
				rhs, ok := assign.Rhs[i].(*ast.SelectorExpr)
				if !ok {
					continue
				}
				pkg, ok := rhs.X.(*ast.Ident)
				if !ok || pkg.Name != "execution" {
					continue
				}
				if _, terminal := values[rhs.Sel.Name]; terminal {
					t.Errorf("V-0022: %s:%s assigns terminal verdict constant execution.%s to a Status field; terminal verdicts must flow from execution.Aggregate through the single terminal entry (settleTerminalVerdict/reclassifyTerminalVerdict)",
						name, terminalGateEnclosingFunc(file, assign.Pos()), rhs.Sel.Name)
				}
			}
			return true
		})
	}

	// Layer 3: entry call pins. Each sanctioned caller must invoke the entry;
	// removing the call (re-introducing a bypass) fails the gate.
	entryPins := map[string][]string{
		"settleTerminalVerdict":     {"writeTerminal", "failCaseRun", "interruptRun"},
		"reclassifyTerminalVerdict": {"classifyRun", "SubmitFailure"},
	}
	for entryName, callers := range entryPins {
		found := map[string]bool{}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			file := parseTerminalGateFile(t, filepath.Join(appDir, name))
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				for _, caller := range callers {
					if fn.Name.Name != caller {
						continue
					}
					ast.Inspect(fn.Body, func(n ast.Node) bool {
						call, ok := n.(*ast.CallExpr)
						if !ok {
							return true
						}
						if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == entryName {
							found[caller] = true
						}
						return true
					})
				}
			}
		}
		for _, caller := range callers {
			if !found[caller] {
				t.Errorf("V-0022: %s no longer calls the terminal entry %s; the write path bypasses the single terminal entry", caller, entryName)
			}
		}
	}
}
