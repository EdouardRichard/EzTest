package version_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/govocab"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// TestPlanItemTerminal pins the single classification authority over every status
// the writers produce plus the finished-state synonyms the free-form plan.update
// tool may write. The terminal set is derived from the writers (see plan_item.go):
// the extraction milestone writes done/failed as final run states and in_progress
// as the open-run state; the planner writes pending. done/failed/completed/
// cancelled/canceled/skipped/closed denote finished items; pending/in_progress and
// other ongoing values denote open work.
func TestPlanItemTerminal(t *testing.T) {
	tests := []struct {
		status   string
		terminal bool
	}{
		// Writer-produced open states (planner pending; milestone in_progress).
		{"pending", false},
		{"in_progress", false},
		// Writer-produced final states (extraction milestone done/failed).
		{"done", true},
		{"failed", true},
		// Finished-state synonyms the model may write (free-form plan.update).
		{"completed", true},
		{"cancelled", true},
		{"canceled", true},
		{"skipped", true},
		{"closed", true},
		// Normalization: trim + lower-case (matches the legacy handoff classifier).
		{"Done", true},
		{"FAILED", true},
		{" done ", true},
		{"In_Progress", false},
		{"  PENDING  ", false},
		// Unknown / ongoing values default to open.
		{"blocked", false},
		{"running", false},
		{"", false},
		{"unknown", false},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			if got := version.PlanItemTerminal(tt.status); got != tt.terminal {
				t.Fatalf("PlanItemTerminal(%q) = %v, want %v", tt.status, got, tt.terminal)
			}
		})
	}
}

// TestPlanItemTerminalStatusesMatchPredicate pins that the SQL bound set and the Go
// predicate derive from one authority: every status in PlanItemTerminalStatuses()
// classifies terminal, and the writer's open states are absent from it. A future
// edit that changes one without the other fails here.
func TestPlanItemTerminalStatusesMatchPredicate(t *testing.T) {
	for _, status := range version.PlanItemTerminalStatuses() {
		if !version.PlanItemTerminal(status) {
			t.Fatalf("terminal set member %q does not classify terminal", status)
		}
	}
	for _, open := range []string{"pending", "in_progress"} {
		if version.PlanItemTerminal(open) {
			t.Fatalf("writer open state %q classified terminal", open)
		}
		for _, status := range version.PlanItemTerminalStatuses() {
			if status == open {
				t.Fatalf("writer open state %q leaked into the terminal set", open)
			}
		}
	}
	// The returned slice is a copy: mutating it must not affect the authority.
	set := version.PlanItemTerminalStatuses()
	if len(set) > 0 {
		set[0] = "mutated"
	}
	if version.PlanItemTerminal("mutated") {
		t.Fatal("PlanItemTerminalStatuses() returned a mutable alias of the authority")
	}
}

// planStatusCarrierTypes are the plan-write carrier types whose status field maps
// to version_plan_item.status: the version-domain row (PlanItem) and partial write
// (PlanPatch), and the agent completion/settlement write set (CompletionWrite,
// whose PlanStatus/Status the completion store persists into the same column).
// This is the plan-write API surface (a stable contract), NOT a hand-picked file
// list: any application file that constructs one of these carriers with its status
// field set is a writer, wherever it lives.
var planStatusCarrierTypes = map[string]bool{
	"PlanItem":        true,
	"PlanPatch":       true,
	"CompletionWrite": true,
}

// planStatusFieldKeys are the carrier fields that carry the plan-item status value.
var planStatusFieldKeys = map[string]bool{"Status": true, "PlanStatus": true}

// isPlanStatusCarrier reports whether a composite-literal type expression names a
// plan-write carrier, matching the final identifier so a qualified type
// (version.PlanItem / versiondomain.PlanPatch) and a bare type (CompletionWrite)
// are treated alike.
func isPlanStatusCarrier(t ast.Expr) bool {
	switch tt := t.(type) {
	case *ast.Ident:
		return planStatusCarrierTypes[tt.Name]
	case *ast.SelectorExpr:
		return planStatusCarrierTypes[tt.Sel.Name]
	default:
		return false
	}
}

// fileWritesPlanStatus is the DERIVED writer predicate (H-D3, review M2 + T5
// review Important #1): a file is a plan-status writer when it produces a
// plan-item status value through a plan-write carrier. It matches the brief's
// "assigns/UPDATE the plan_item status column" by three content signals, any one
// of which selects the file:
//
//  1. a carrier composite literal that sets the status field by KEY
//     (PlanPatch{Status: ...} / CompletionWrite{PlanStatus: ...});
//  2. a POSITIONAL carrier composite literal (PlanPatch{title, status, ...}) —
//     positional elements name no fields, so any non-empty positional carrier
//     literal conservatively sets status;
//  3. the file mentions a carrier type AND assigns to a .Status/.PlanStatus
//     SELECTOR (item.Status = NewConst) — the selector-assignment write shape the
//     free-form plan.update tool uses today (tools/plan.go). Without this a new
//     writer file could define a status constant and write it via selector,
//     escaping constant enumeration entirely.
//
// It is a content predicate over the AST, not a closed file table: a NEW writer
// file in any of these shapes is selected automatically, so its status constants
// cannot escape the terminal-classification guard below. Over-selection is
// FAIL-LOUD by construction (every exported string constant of a selected file
// must classify terminal/known-open or be explicitly allowlisted as non-plan), so
// the conservative carrier-mention + selector signal is safe and cheap. Scan
// scope stays the context application trees (the inherited boundary of the former
// closed list); interfaces/infrastructure persist whatever status they are given
// and define no status vocabulary.
func fileWritesPlanStatus(f *ast.File) bool {
	mentionsCarrier := false
	statusSelectorAssign := false
	carrierStatusLiteral := false
	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.Ident:
			if planStatusCarrierTypes[node.Name] {
				mentionsCarrier = true
			}
		case *ast.SelectorExpr:
			if planStatusCarrierTypes[node.Sel.Name] {
				mentionsCarrier = true
			}
		case *ast.CompositeLit:
			if isPlanStatusCarrier(node.Type) && len(node.Elts) > 0 {
				keyed, hasStatusKey := false, false
				for _, elt := range node.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						keyed = true
						if key, ok := kv.Key.(*ast.Ident); ok && planStatusFieldKeys[key.Name] {
							hasStatusKey = true
						}
					}
				}
				// A keyed literal selects only when it sets status; a positional
				// literal (no keys) conservatively sets every field incl. status.
				if hasStatusKey || !keyed {
					carrierStatusLiteral = true
				}
			}
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				if sel, ok := lhs.(*ast.SelectorExpr); ok && planStatusFieldKeys[sel.Sel.Name] {
					statusSelectorAssign = true
				}
			}
		}
		return true
	})
	return carrierStatusLiteral || (mentionsCarrier && statusSelectorAssign)
}

// planStatusWriterFiles DERIVES the writer source-file set (H-D3): it walks the
// context application trees under internal/ and selects every non-test Go file
// that writes a plan-item status (fileWritesPlanStatus). Paths are relative to this
// package dir (internal/version/domain); ../../ is internal. The set is sorted for
// determinism. It replaces the former hand-written two-file list, which a new
// writer file could silently escape (review M2: a closed table swapped for another
// closed table is no derivation). The constant-derivation shape below still reuses
// T17's go/ast + go/constant approach (handoff_repo_lint_test.go).
func planStatusWriterFiles(t *testing.T) []string {
	t.Helper()
	const root = "../.." // internal/
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		if !strings.Contains(filepath.ToSlash(path), "/application/") {
			return nil
		}
		f, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		if fileWritesPlanStatus(f) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("derive plan-status writer files: %v", err)
	}
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("derived no plan-status writer files; the writer derivation is broken (would go vacuous)")
	}
	return files
}

// TestPlanStatusWritersASTDerived proves the writer-file set is DERIVED by a content
// predicate over the application trees, not a closed hand-list (H-D3, review M2).
// The known writers must be discovered, every selected file must satisfy the
// predicate, and the predicate must distinguish a real plan-status write from a
// non-carrier Status field and a carrier with no status key — so a NEW writer file
// is selected automatically (the mutation a closed two-file list could never see).
func TestPlanStatusWritersASTDerived(t *testing.T) {
	files := planStatusWriterFiles(t)
	for _, want := range []string{"extraction_milestone.go", "completion.go"} {
		found := false
		for _, f := range files {
			if filepath.Base(f) == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("derived writer set %v is missing known writer %q", files, want)
		}
	}
	// Every selected file must actually write a plan status (sanity, non-vacuous).
	for _, f := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		if err != nil {
			t.Fatalf("re-parse writer %s: %v", f, err)
		}
		if !fileWritesPlanStatus(parsed) {
			t.Fatalf("selected writer %s does not satisfy the writer predicate", f)
		}
	}
	// The predicate is a real derivation: it selects a plan-carrier status write
	// and rejects look-alikes, proven on synthetic AST (a closed file list cannot
	// express this content distinction).
	writers := []string{
		"package p\nimport v \"x/version/domain\"\nfunc f() v.PlanPatch { return v.PlanPatch{Status: S} }\n",
		"package p\nfunc f() CompletionWrite { return CompletionWrite{PlanStatus: S} }\n",
		"package p\nimport v \"x/version/domain\"\nfunc f() v.PlanItem { return v.PlanItem{Status: S} }\n",
		// Selector-assignment writer (T5 review Important #1): item.Status = NewConst.
		// The free-form plan.update tool writes this way; a new constant written via
		// selector must not escape enumeration.
		"package p\nimport v \"x/version/domain\"\nfunc f(item v.PlanItem) { item.Status = NewConst }\n",
		// Positional carrier literal (no keys) conservatively sets every field incl. status.
		"package p\nimport v \"x/version/domain\"\nfunc f() v.PlanPatch { return v.PlanPatch{A, B, C, D} }\n",
	}
	for _, src := range writers {
		f, err := parser.ParseFile(token.NewFileSet(), "synthetic.go", src, 0)
		if err != nil {
			t.Fatalf("parse synthetic writer: %v", err)
		}
		if !fileWritesPlanStatus(f) {
			t.Fatalf("predicate missed a plan-status write: %s", src)
		}
	}
	nonWriters := []string{
		"package p\nfunc f() Other { return Other{Status: S} }\n",                                           // non-carrier Status field
		"package p\nimport v \"x/version/domain\"\nfunc f() v.PlanPatch { return v.PlanPatch{Title: S} }\n", // carrier, no status key
		"package p\nimport v \"x/version/domain\"\nfunc f() v.PlanItem { return v.PlanItem{} }\n",           // empty carrier
	}
	for _, src := range nonWriters {
		f, err := parser.ParseFile(token.NewFileSet(), "synthetic.go", src, 0)
		if err != nil {
			t.Fatalf("parse synthetic non-writer: %v", err)
		}
		if fileWritesPlanStatus(f) {
			t.Fatalf("predicate falsely selected a non-writer: %s", src)
		}
	}
	// Precision: a .Status selector assignment with NO carrier mention is not a
	// plan writer (the carrier-mention conjunct guards against unrelated structs).
	noCarrier, err := parser.ParseFile(token.NewFileSet(), "synthetic.go", "package p\nfunc f(x Other) { x.Status = S }\n", 0)
	if err != nil {
		t.Fatalf("parse synthetic no-carrier: %v", err)
	}
	if fileWritesPlanStatus(noCarrier) {
		t.Fatal("predicate selected a .Status assignment with no carrier mention")
	}
}

// nonPlanStatusConstants is the explicit allowlist of exported string values in
// the writer files that are NOT plan-item statuses (fact kinds/sources, title/marker
// prefixes, phase labels), each with a reason. Every OTHER exported string value is
// treated as a plan status and must classify correctly. A NEW value — plan status or
// not — fails until it is either classified by the authority or explicitly
// allowlisted here. That is the derivation property a closed hand-list cannot
// provide: Go cannot enumerate package constants at runtime, so a closed table never
// sees an added one.
func nonPlanStatusConstants() map[string]string {
	return map[string]string{
		"ExtractionPlanTitlePrefix":           "milestone title prefix, not a status",
		"ExtractionPlanDecisionMarkerPrefix":  "milestone decision marker, not a status",
		"ExtractionFactKindAttachmentParse":   "fact kind, not a plan status",
		"ExtractionFactKindRequirementSource": "fact kind, not a plan status",
		"ExtractionFactSource":                "fact source, not a plan status",
		"CompletionFactSource":                "fact source, not a plan status",
		"SettlementFactSource":                "fact source, not a plan status",
		// tools/plan.go is the free-form plan.update writer (selector-assignment
		// shape, selected by the broadened predicate, T5 review Important #1). Its
		// exported string constants are the tool name and the stale-revision error
		// code, not plan statuses; the status values it writes are model-supplied
		// and covered by the terminal authority's finished-state synonyms.
		"ToolPlanUpdate": "plan.update tool name (free-form plan writer), not a plan status",
		"CodeConflict":   "plan write stale-revision error code, not a plan status",
		// H-B1: these three used to sit in the NON-LITERAL allowlist, which hid
		// their values from the guard entirely. The resolver now evaluates the
		// conversion and the cross-package aliases, so their values are derived
		// and must be classified here by name instead (the escape face is closed).
		"ExtractionPlanPhase":        "string(StageClarifying) conversion; a plan phase label, not a status",
		"SettlementFactKind":         "alias of CompletionFactKindProgress (\"progress\"); a fact kind, not a plan status",
		"CompletionFactKindProgress": "alias of version.FactKindProgress (\"progress\") so the producer and the judge briefing's agent-narrative exclusion share one authority; a fact kind, not a plan status",
	}
}

// knownOpenPlanStatuses are the plan-status values that are legitimately non-terminal
// (the open states the writers produce). A plan-status constant whose value is
// neither terminal per PlanItemTerminal nor in this set is unclassified and fails.
func knownOpenPlanStatuses() map[string]bool {
	return map[string]bool{"in_progress": true, "pending": true}
}

// nonLiteralWriterConstants classifies exported writer declarations whose value the
// resolver cannot evaluate and which are consciously known NOT to be plan-item
// statuses. An entry whose declaration DOES resolve to a string is rejected by the
// derivation (its value must be derived and classified, not hidden here), and an
// entry whose declaration disappeared is reported stale.
//
// It is EMPTY today: every exported writer value resolves (H-B1 closed the alias /
// conversion / cross-package blind spots). The mechanism stays because a future
// unresolvable declaration must fail loud rather than vanish.
func nonLiteralWriterConstants() map[string]string {
	return map[string]string{}
}

// planStatusNeedsExplicitClassification reports whether a writer declaration must
// be explicitly classified: the resolver could not evaluate its string-shaped value
// (or a MEMBER of its value set) AND it is not in the non-literal allowlist.
// IsString is deliberately NOT a short-circuit: a partially resolvable set carries
// both signals, and its unresolvable member must still be classified (I1) — it could
// be a new terminal status. A provably non-string value (a numeric expression,
// errors.New(...), a regexp) cannot be a plan status either.
func planStatusNeedsExplicitClassification(name string, decl govocab.Decl, allowlist map[string]string) bool {
	if !decl.Unresolved {
		return false
	}
	_, allowed := allowlist[name]
	return !allowed
}

// deriveWriterStringConstants derives every exported string value of the writer
// files (const AND var, including var sets, aliases, conversions, concatenation and
// cross-package references) through the shared resolver. A declaration whose
// string-shaped value cannot be resolved fails loud unless explicitly allowlisted;
// an allowlist entry that resolves to a string is rejected. Unused allowlist entries
// are reported stale.
func deriveWriterStringConstants(t *testing.T) map[string][]string {
	t.Helper()
	out, problems := deriveWriterValues(newPlanStatusVocabResolver(t), planStatusWriterFiles(t), nonLiteralWriterConstants())
	for _, problem := range problems {
		t.Errorf("%s", problem)
	}
	if len(problems) > 0 {
		t.Fatalf("%d writer vocabulary derivation problem(s)", len(problems))
	}
	return out
}

// newPlanStatusVocabResolver roots the derivation at the backend module directory
// (this package is internal/version/domain).
func newPlanStatusVocabResolver(t *testing.T) *govocab.Resolver {
	t.Helper()
	resolver, err := govocab.New("../../..")
	if err != nil {
		t.Fatalf("vocabulary resolver: %v", err)
	}
	return resolver
}

// deriveWriterValues is the derivation core, returning problems instead of failing
// so the guard's properties can be proven on real synthetic writer files.
func deriveWriterValues(resolver *govocab.Resolver, files []string, allowlist map[string]string) (map[string][]string, []string) {
	out := make(map[string][]string)
	var problems []string
	seenNonLiteral := make(map[string]bool)
	for _, file := range files {
		decls, err := resolver.Decls(file, true)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		for _, decl := range decls {
			_, allowed := allowlist[decl.Name]
			if decl.IsString {
				// An entry may only classify what could NOT be evaluated; a fully
				// resolved value must be derived and classified, never hidden.
				if allowed && !decl.Unresolved {
					problems = append(problems, "non-literal allowlist entry "+decl.Name+
						" resolves to the string value(s) "+strings.Join(decl.Values, ",")+
						"; remove the entry so the terminal authority classifies the value instead of hiding it")
					continue
				}
				out[decl.Name] = append(out[decl.Name], decl.Values...)
				// I1: a partially resolvable set keeps its resolved members in the
				// derived set AND demands classification of the rest.
				if planStatusNeedsExplicitClassification(decl.Name, decl, allowlist) {
					problems = append(problems, "writer declaration "+decl.Name+
						" has an unresolvable string-shaped value or set member; "+
						"define it as a string literal so the terminal authority can classify it, "+
						"otherwise allowlist it in nonLiteralWriterConstants with a reason")
					continue
				}
				if allowed {
					seenNonLiteral[decl.Name] = true
				}
				continue
			}
			if planStatusNeedsExplicitClassification(decl.Name, decl, allowlist) {
				problems = append(problems, "writer declaration "+decl.Name+
					" has a string-shaped value the plan-status guard cannot resolve; "+
					"if it is a plan status, define it as a string literal so the terminal authority can classify it, "+
					"otherwise allowlist it in nonLiteralWriterConstants with a reason")
				continue
			}
			if allowed {
				seenNonLiteral[decl.Name] = true
			}
		}
	}
	for name := range allowlist {
		if !seenNonLiteral[name] {
			problems = append(problems, "non-literal allowlist entry "+name+
				" is unused (declaration removed/renamed or now a string value); remove it")
		}
	}
	if len(out) == 0 {
		problems = append(problems, "derived no writer string values; parsing is broken")
	}
	return out, problems
}

// findUnclassifiedPlanStatuses applies the derivation rule to a derived value map:
// every declaration NOT in the non-plan allowlist must classify terminal
// (PlanItemTerminal) or be a known-open value, else it is unclassified. It also
// returns stale allowlist entries (names absent from consts). Extracted so the
// guard's properties can be proven on synthetic inputs (TestWriterPlanStatusGuardFires),
// since Go cannot enumerate an added package constant at runtime.
func findUnclassifiedPlanStatuses(consts map[string][]string, allowlist map[string]string, open map[string]bool) (unclassified, stale []string) {
	used := make(map[string]bool)
	for name, values := range consts {
		if _, nonPlan := allowlist[name]; nonPlan {
			used[name] = true
			continue
		}
		for _, value := range values {
			if version.PlanItemTerminal(value) || open[value] {
				continue
			}
			unclassified = append(unclassified, name+"="+value)
		}
	}
	for name := range allowlist {
		if !used[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(unclassified)
	sort.Strings(stale)
	return unclassified, stale
}

// TestWriterPlanStatusConstantsClassify is the real derivation guard (I-2). It
// ENUMERATES the writer files' exported string constants via AST — not a closed
// hand-list — so a NEW final-state constant (e.g. ExtractionPlanStatusAborted =
// "aborted") fails here until the terminal authority is extended (if terminal) or
// the value is added to knownOpenPlanStatuses (if open) or the constant is explicitly
// allowlisted as non-plan. A closed table cannot catch an added constant; this can.
// It also pins that the known plan-status constants are still present, and that no
// allowlist entry is stale.
func TestWriterPlanStatusConstantsClassify(t *testing.T) {
	consts := deriveWriterStringConstants(t)

	// The known plan-status constants must be present: a renamed/removed writer
	// constant or broken parsing must not pass silently.
	for _, name := range []string{
		"ExtractionPlanStatusRunning", "ExtractionPlanStatusDone", "ExtractionPlanStatusFailed",
		"CompletionPlanStatusDone", "SettlementPlanStatus",
	} {
		if _, ok := consts[name]; !ok {
			t.Fatalf("expected writer plan-status constant %s not found (renamed/removed?)", name)
		}
	}

	unclassified, stale := findUnclassifiedPlanStatuses(consts, nonPlanStatusConstants(), knownOpenPlanStatuses())
	for _, u := range unclassified {
		t.Errorf("writer constant %s is an unclassified plan status: extend planItemTerminalStatuses (if terminal) or knownOpenPlanStatuses (if open), or allowlist it as non-plan", u)
	}
	for _, s := range stale {
		t.Errorf("non-plan allowlist entry %s is unused (constant removed/renamed); remove it", s)
	}
}

// TestWriterPlanStatusGuardFires proves the derivation property in its strong form:
// a NEW final-state constant — which a closed hand-list could never see — DOES fire
// here, and the conscious escape hatches (classify it, or allowlist it as non-plan)
// resolve it. This is what makes the guard a derivation rather than a transcription.
func TestWriterPlanStatusGuardFires(t *testing.T) {
	allowlist := nonPlanStatusConstants()
	open := knownOpenPlanStatuses()

	// A new terminal-valued plan status ("aborted") is unclassified -> fires.
	withNew := map[string][]string{
		"ExtractionPlanStatusDone":    {"done"},
		"ExtractionPlanStatusAborted": {"aborted"},
	}
	unclassified, _ := findUnclassifiedPlanStatuses(withNew, allowlist, open)
	if !containsPrefix(unclassified, "ExtractionPlanStatusAborted=") {
		t.Fatalf("guard did not fire on a new final-state constant: %v", unclassified)
	}

	// A new OPEN-valued plan status with a novel value ("queued") also fires until
	// it is consciously classified as open.
	withNewOpen := map[string][]string{"SomePlanStatusQueued": {"queued"}}
	unclassified, _ = findUnclassifiedPlanStatuses(withNewOpen, allowlist, open)
	if !containsPrefix(unclassified, "SomePlanStatusQueued=") {
		t.Fatalf("guard did not fire on a new open-valued constant: %v", unclassified)
	}
	// Classifying it as open resolves it.
	openExtended := map[string]bool{"in_progress": true, "pending": true, "queued": true}
	unclassified, _ = findUnclassifiedPlanStatuses(withNewOpen, allowlist, openExtended)
	if len(unclassified) != 0 {
		t.Fatalf("guard still fired after classifying the new open value: %v", unclassified)
	}

	// Allowlisting the new constant as non-plan is the other conscious escape hatch.
	allowExtended := map[string]string{"ExtractionPlanStatusAborted": "hypothetical non-plan use"}
	unclassified, _ = findUnclassifiedPlanStatuses(withNew, allowExtended, open)
	if containsPrefix(unclassified, "ExtractionPlanStatusAborted=") {
		t.Fatalf("guard fired despite an explicit non-plan allowlist entry: %v", unclassified)
	}

	// A stale allowlist entry (constant absent) is reported.
	_, stale := findUnclassifiedPlanStatuses(map[string][]string{"ExtractionPlanStatusDone": {"done"}}, allowlist, open)
	if len(stale) == 0 {
		t.Fatal("guard did not report stale allowlist entries")
	}

	// A var SET contributes EVERY member: one unclassified member of a set fires
	// even when the other members are known (the const-only scan saw none of them).
	withSet := map[string][]string{"PlanStatusSet": {"done", "aborted"}}
	unclassified, _ = findUnclassifiedPlanStatuses(withSet, allowlist, open)
	if !containsPrefix(unclassified, "PlanStatusSet=aborted") {
		t.Fatalf("guard did not fire on an unclassified member of a var set: %v", unclassified)
	}
}

// TestWriterPlanStatusNonLiteralGuardFires proves the classification rule in its
// strong form: an unresolvable string-shaped declaration needs explicit
// classification, while a resolved string, a provably non-string value and an
// allowlisted non-literal do not.
func TestWriterPlanStatusNonLiteralGuardFires(t *testing.T) {
	allowlist := map[string]string{"SomeExternalLabel": "not a plan status"}

	unresolved := govocab.Decl{Name: "ExtractionPlanStatusAborted", Unresolved: true}
	if !planStatusNeedsExplicitClassification("ExtractionPlanStatusAborted", unresolved, allowlist) {
		t.Fatal("an unresolvable alias-defined terminal status did not require explicit classification (guard is blind)")
	}
	resolved := govocab.Decl{Name: "ExtractionPlanStatusAborted", IsString: true, Values: []string{"aborted"}}
	if planStatusNeedsExplicitClassification("ExtractionPlanStatusAborted", resolved, allowlist) {
		t.Fatal("a resolved string value should be derived, not require classification")
	}
	numeric := govocab.Decl{Name: "SomeNewSize"}
	if planStatusNeedsExplicitClassification("SomeNewSize", numeric, allowlist) {
		t.Fatal("a provably non-string declaration should not require classification")
	}
	allowed := govocab.Decl{Name: "SomeExternalLabel", Unresolved: true}
	if planStatusNeedsExplicitClassification("SomeExternalLabel", allowed, allowlist) {
		t.Fatal("an allowlisted non-literal should not require classification")
	}
	// I1: a PARTIALLY resolved set (IsString AND Unresolved) still needs
	// classification — the unresolvable member could be a new terminal status.
	partial := govocab.Decl{Name: "PlanStatusSet", IsString: true, Values: []string{"done"}, Unresolved: true}
	if !planStatusNeedsExplicitClassification("PlanStatusSet", partial, allowlist) {
		t.Fatal("a partially resolved writer set did not require classification: its unresolvable member escapes")
	}
}

// TestWriterPlanStatusDerivationFires proves the H-B1 derivation hardening end to
// end on real synthetic writer files: a var-defined status and every member of a
// var SET are derived, an alias and a cross-package reference resolve, an
// unresolvable alias fails loud, and an allowlist entry that resolves to a string
// is rejected instead of hiding the value.
func TestWriterPlanStatusDerivationFires(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatalf("write writer sample: %v", err)
		}
		return path
	}
	resolver := newPlanStatusVocabResolver(t)

	const sample = `package sample

const Done = "done"
const Aliased = Done
var VarStatus = "in_progress"
var StatusSet = []string{"done", "aborted"}
`
	values, problems := deriveWriterValues(resolver, []string{write("sample.go", sample)}, nil)
	if len(problems) != 0 {
		t.Fatalf("clean writer sample reported problems: %v", problems)
	}
	for name, want := range map[string][]string{
		"Done": {"done"}, "Aliased": {"done"}, "VarStatus": {"in_progress"}, "StatusSet": {"done", "aborted"},
	} {
		got := values[name]
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s = %v, want %v (var/alias blind spot)", name, got, want)
		}
	}
	// The derived set feeds the terminal authority: the new "aborted" member of
	// the var set is unclassified, so the guard fires on it.
	unclassified, _ := findUnclassifiedPlanStatuses(values, nonPlanStatusConstants(), knownOpenPlanStatuses())
	if !containsPrefix(unclassified, "StatusSet=aborted") {
		t.Fatalf("a var-set status member escaped terminal classification: %v", unclassified)
	}

	// An unresolvable string-shaped value fails loud instead of vanishing.
	const loudSample = "package sample\n\nconst Done = \"done\"\nconst Dangling = missingIdent\n"
	if _, problems := deriveWriterValues(resolver, []string{write("loud.go", loudSample)}, nil); len(problems) == 0 {
		t.Fatal("an unresolvable writer alias was silently dropped")
	}
	// Explicit classification is the conscious escape hatch...
	if _, problems := deriveWriterValues(resolver, []string{write("loud.go", loudSample)},
		map[string]string{"Dangling": "not a plan status"}); len(problems) != 0 {
		t.Fatalf("an explicitly classified non-literal still failed: %v", problems)
	}
	// ...but a resolvable string value must not be hideable behind the allowlist.
	if _, problems := deriveWriterValues(resolver, []string{write("escape.go", "package sample\n\nconst Done = \"done\"\nconst Hidden = Done\n")},
		map[string]string{"Hidden": "pretend this is not a string"}); len(problems) == 0 {
		t.Fatal("the allowlist hid a resolvable writer value from the guard")
	}

	// I1: a var SET with one literal member and one unresolvable member must fail
	// loud (the unresolvable member could be a new terminal status), while the
	// resolvable member stays in the derived set and reaches classification.
	const partialSample = "package sample\n\nvar StatusSet = []string{\"done\", missingIdent}\n"
	partialValues, partialProblems := deriveWriterValues(resolver, []string{write("partial.go", partialSample)}, nil)
	if len(partialProblems) == 0 {
		t.Fatal("a partially unresolvable writer set was silently accepted: its unresolvable member escapes terminal classification")
	}
	if strings.Join(partialValues["StatusSet"], ",") != "done" {
		t.Fatalf("the resolvable member of a partial set must stay derived: %v", partialValues)
	}
	// Explicit classification is the escape hatch; the resolved member is still
	// classified by the terminal authority (nothing is hidden).
	if _, problems := deriveWriterValues(resolver, []string{write("partial.go", partialSample)},
		map[string]string{"StatusSet": "the unresolvable member is not a plan status"}); len(problems) != 0 {
		t.Fatalf("an explicitly classified partial set still failed: %v", problems)
	}
}

func containsPrefix(items []string, prefix string) bool {
	for _, item := range items {
		if strings.HasPrefix(item, prefix) {
			return true
		}
	}
	return false
}

// TestPlanItemTerminalDomainOpenStates pins the domain's own open plan-item constants
// as non-terminal (these live in plan_item.go, not the writer files the AST guard scans).
func TestPlanItemTerminalDomainOpenStates(t *testing.T) {
	for _, status := range []string{version.PlanItemStatusPending, version.PlanItemStatusInProgress} {
		if version.PlanItemTerminal(status) {
			t.Fatalf("domain open plan-item constant %q classified terminal", status)
		}
	}
}
