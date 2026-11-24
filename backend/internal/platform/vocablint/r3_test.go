package vocablint_test

// T11e-5 machine-hardening and R3-extension properties.
//
// Every rule extension carries a POSITIVE case (a measured blind-spot group joins)
// and a NEGATIVE case (the shape Ruling T11d-R1 forbids — the NLP word lists of
// execution/domain/requirement_deviation.go — stays out). The negative cases are
// asserted on the REAL repository source as well as on synthetic AST, so a future
// widening that swallows a word list fails here rather than silently protecting
// nonsense.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/govocab"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil/vocabscope"
)

// r3TestRepoRoot walks up from this test's source file to the repository root.
func r3TestRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the vocablint test source")
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

// r3TestDomainFiles derives every domain vocabulary file of the repository.
func r3TestDomainFiles(t *testing.T, root string) []string {
	t.Helper()
	files, err := vocablint.GoFilesUnder(filepath.Join(root, "backend", "internal"))
	if err != nil {
		t.Fatalf("derive domain files: %v", err)
	}
	var out []string
	for _, file := range files {
		if strings.Contains(filepath.ToSlash(file), "/domain/") {
			out = append(out, file)
		}
	}
	if len(out) < 100 {
		t.Fatalf("only %d domain vocabulary files derived; the sample would be vacuous", len(out))
	}
	return out
}

// TestConstSpecGroupMatchesASTBoundaries is the Part B property: the Group index
// the shared walk carries reproduces the AST GenDecl boundaries exactly, and the
// two inheritances (elided TYPE, elided VALUE) are unchanged by carrying it. The
// reference slicing below is written independently of ConstGroups, so agreement is
// evidence rather than tautology.
func TestConstSpecGroupMatchesASTBoundaries(t *testing.T) {
	src := `package p
type Status string
const (
	A Status = "a"
	B        = "b"
	C
)
const single = "s"
var (
	Set   = []string{"a", "b"}
	Other = []string{"c"}
)
var lone = []string{"d"}
const (
	D = "d"
	E = "e"
)
`
	file, err := parser.ParseFile(token.NewFileSet(), "synthetic.go", src, 0)
	if err != nil {
		t.Fatalf("parse synthetic: %v", err)
	}
	for _, tok := range []token.Token{token.CONST, token.VAR} {
		specs := vocablint.ConstSpecs(file, tok)
		// Independent reference: walk the GenDecls and count value specs.
		var want [][]vocablint.ConstSpec
		next := 0
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != tok {
				continue
			}
			count := 0
			for _, spec := range gen.Specs {
				if _, ok := spec.(*ast.ValueSpec); ok {
					count++
				}
			}
			if count == 0 {
				continue
			}
			want = append(want, specs[next:next+count])
			next += count
		}
		got := vocablint.ConstGroups(file, tok)
		if len(got) != len(want) {
			t.Fatalf("%s groups = %d, want %d", tok, len(got), len(want))
		}
		for gi := range want {
			if len(got[gi]) != len(want[gi]) {
				t.Fatalf("%s group %d size = %d, want %d", tok, gi, len(got[gi]), len(want[gi]))
			}
			for si, spec := range got[gi] {
				if spec.Group != gi {
					t.Fatalf("%s group %d spec %d carries Group=%d", tok, gi, si, spec.Group)
				}
				if spec.Names[0] != want[gi][si].Names[0] || spec.Type != want[gi][si].Type {
					t.Fatalf("%s group %d spec %d disagrees with the AST slicing: %+v vs %+v",
						tok, gi, si, spec, want[gi][si])
				}
			}
		}
	}
	consts := vocablint.ConstGroups(file, token.CONST)
	if len(consts) != 3 {
		t.Fatalf("const groups = %d, want 3", len(consts))
	}
	// Type elision inherits within the group, and never across groups.
	if consts[0][1].Type != "Status" || consts[0][2].Type != "Status" {
		t.Fatalf("elided type not inherited: %+v", consts[0])
	}
	if consts[1][0].Type != "" || consts[2][0].Type != "" {
		t.Fatalf("type leaked across group boundaries: %+v %+v", consts[1], consts[2])
	}
	// Value elision repeats the previous spec's expression list.
	if len(consts[0][2].Exprs) != 1 || consts[0][2].Exprs[0] != consts[0][1].Exprs[0] {
		t.Fatalf("value-elided spec did not repeat the previous expression: %+v", consts[0][2])
	}
}

// TestR3ESelectsExportedGroupWithoutSemanticName is the R3-E positive case: the
// exact shape of the measured blind spots (chain.Layer*, execution.NextAction*,
// version.Conversation*): an untyped const group, no semantic name, no authority.
func TestR3ESelectsExportedGroupWithoutSemanticName(t *testing.T) {
	src := `package p
const (
	LayerRequirement = "requirement"
	LayerDesign      = "design"
)
`
	file, err := parser.ParseFile(token.NewFileSet(), "synthetic.go", src, 0)
	if err != nil {
		t.Fatalf("parse synthetic: %v", err)
	}
	groups := vocablint.ConstGroups(file, token.CONST)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}
	if groups[0][0].Type != "" {
		t.Fatalf("sample must be untyped to prove R3-E, got %q", groups[0][0].Type)
	}
	if !vocabscope.Selector().GroupIsVocabulary(groups[0]) {
		t.Fatal("R3-E did not select an exported untyped const group; chain.Layer* would stay unprotected")
	}
}

// TestR3RSelectsRegistrySetAndOnlyRegistrySet is the R3-R pair: a set enumerating
// constants BY REFERENCE selects them (chainCountKeys -> countRequirements...),
// while a set of literal strings (every NLP word list) is not a registry.
func TestR3RSelectsRegistrySetAndOnlyRegistrySet(t *testing.T) {
	src := `package p
const (
	countRequirements = "requirements"
	countScenarios    = "scenarios"
)
var chainCountKeys = []string{countRequirements, countScenarios}
var missingFeatureMarkers = []string{"missing", "not found"}
var mixed = []string{countRequirements, "literal"}
var byKey = map[string]struct{}{countRequirements: {}, countScenarios: {}}
var literalKeys = map[string]struct{}{"x": {}}
`
	file, err := parser.ParseFile(token.NewFileSet(), "synthetic.go", src, 0)
	if err != nil {
		t.Fatalf("parse synthetic: %v", err)
	}
	groups := vocablint.ConstGroups(file, token.VAR)
	if len(groups) != 5 {
		t.Fatalf("var groups = %d, want 5", len(groups))
	}
	refs := vocablint.RegistryReferences(groups[0])
	if !slices.Contains(refs, "countRequirements") || !slices.Contains(refs, "countScenarios") {
		t.Fatalf("registry references = %v, want both constants", refs)
	}
	for i, name := range []string{"missingFeatureMarkers", "mixed", "literalKeys"} {
		idx := map[string]int{"missingFeatureMarkers": 1, "mixed": 2, "literalKeys": 4}[name]
		if got := vocablint.RegistryReferences(groups[idx]); got != nil {
			t.Fatalf("%s (case %d) was treated as a registry: %v", name, i, got)
		}
	}
	if got := vocablint.RegistryReferences(groups[3]); len(got) != 2 {
		t.Fatalf("a map keyed by constants is a registry too, got %v", got)
	}
	// The referenced constants are unexported and carry no semantic name, so only
	// R3-R can reach them.
	consts := vocablint.ConstGroups(file, token.CONST)
	if vocabscope.Selector().GroupIsVocabulary(consts[0]) {
		t.Fatal("the sample constants must be unreachable by R3/R3-E for this to prove R3-R")
	}
	if vocabscope.Selector().GroupIsVocabulary(groups[1]) {
		t.Fatal("an NLP word list was selected by the group rules")
	}
}

// TestR3RulesExcludeRealNLPWordLists is the negative case on REAL source: none of
// the four NLP word lists Ruling T11d-R1 named may be selected, and the residual
// registration face may not be used to sneak them in.
func TestR3RulesExcludeRealNLPWordLists(t *testing.T) {
	root := r3TestRepoRoot(t)
	files := []string{
		filepath.Join(root, "backend", "internal", "execution", "domain", "requirement_deviation.go"),
		filepath.Join(root, "backend", "internal", "execution", "domain", "failure_summary.go"),
	}
	forbidden := []string{"missingFeatureMarkers", "missingFeatureNouns", "nonAbsenceWuPrefixes", "sentenceTerminators"}
	selector := vocabscope.Selector()
	seen := map[string]bool{}
	for _, path := range files {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, tok := range []token.Token{token.CONST, token.VAR} {
			for _, group := range vocablint.ConstGroups(file, tok) {
				for _, spec := range group {
					for _, name := range spec.Names {
						if slices.Contains(forbidden, name) {
							seen[name] = true
						}
					}
				}
				selected := selector.GroupIsVocabulary(group) || len(vocablint.RegistryReferences(group)) > 0
				if !selected {
					continue
				}
				for _, spec := range group {
					for _, name := range spec.Names {
						if slices.Contains(forbidden, name) {
							t.Errorf("NLP word list %s in %s was selected as vocabulary; Ruling T11d-R1 forbids it",
								name, filepath.Base(path))
						}
					}
				}
			}
		}
	}
	for _, name := range forbidden {
		if !seen[name] {
			t.Fatalf("the counter-example %s was not found in the source; this test would be vacuous", name)
		}
	}
	for _, registration := range vocabscope.Registrations() {
		if slices.Contains(forbidden, registration.Name) {
			t.Fatalf("NLP word list %s is registered as vocabulary", registration.Name)
		}
	}
}

// TestR3NativeCoversMeasuredBlindSpots is the Part C coverage proof on real source:
// one declaration per measured blind-spot group (H-A21①②④⑦⑪, task-11e-3 §12.3 and
// task-11e-4 §12.4) must now be SELECTED by the shared rules. Before T11e-5 every
// one of these labels was invisible to R3 and its live predicates were naked.
func TestR3NativeCoversMeasuredBlindSpots(t *testing.T) {
	root := r3TestRepoRoot(t)
	resolver, err := govocab.New(filepath.Join(root, "backend"))
	if err != nil {
		t.Fatalf("resolver: %v", err)
	}
	v := vocablint.DeriveR3(resolver, r3TestDomainFiles(t, root), vocabscope.Selector(), map[string]string{})
	for _, problem := range v.Problems {
		t.Errorf("derivation problem: %s", problem)
	}
	want := []string{
		"agent.ActivityTypeToolCall",             // #1 ActivityType
		"agent.PromptSourceSeed",                 // #2 PromptSource*
		"agent.PromptVariableStabilityRun",       // #2 stability
		"agent.MessageRoleTool",                  // #3 MessageRole
		"agent.SkillSourceSeed",                  // #4 SkillSource*
		"agent.TeamDesign",                       // #5 Team
		"chain.countRequirements",                // #6 count* via chainCountKeys (R3-R)
		"chain.chainCountKeys",                   // #6 the registry itself
		"chain.TruncationReasonNodeBudget",       // #7
		"chain.LayerDesign",                      // #8 Layer*
		"chain.DegradedLayerOrder",               // #8 the order set
		"chain.ReplayModeLive",                   // #9
		"execution.TerminalCauseTurnBudget",      // #10
		"execution.TerminalCauseExecutorTimeout", // #10 second group
		"execution.NextActionRerun",              // #11
		"execution.EvidenceOwnerHumanRequest",    // #12
		"execution.ExecutorLabelDegraded",        // #13
		"execution.PurposeRetest",                // #14
		"execution.WaveTriggerAuto",              // #15
		"execution.PauseModeGraceful",            // #15
		"execution.ReasonGlobalConcurrency",      // #16
		"execution.executorLimitationCodes",      // #17 residual registration
		"llm.ProtocolOpenAIResponses",            // #18
		"llm.ThinkingTypeAuto",                   // #19
		"llm.ReasoningEffortXHigh",               // #19
		"version.ConversationClarification",      // H-A21② conversation kind
		"version.MessagePartJobCard",             // H-A21② message part
		"version.AttachmentProcessing",           // H-A21① attachment status
		"version.AttachmentImageExtracted",       // H-A21① image status
		"version.AttachmentImageSourceEmbedded",  // attachment image source
		"codeintel.EdgeRouteToAPI",               // H-A21⑦ EdgeRel
		"codeintel.ConfidenceHigh",               // T11e-3 Confidence
		"codeintel.CandidateAI",                  // T11e-3 CandidateSource
		"codeintel.AnchorSubjectVersion",         // AnchorSubject
		"knowledge.ScopeGlobal",                  // T11e-3 Scope
		"design.CaseTriggerManual",               // T11e-3 CaseTrigger*
		"design.SourceRequirement",               // T11e-3 Source*
		"design.AssertionAPI",                    // T11e-3 Assertion*
		"design.SeverityMinor",                   // judgment severity
		"storage.FormatMarkdown",                 // T11e-3 Format*
		"storage.GCModeApply",                    // GCMode
		"settings.TypeString",                    // settings Type
		"identity.RoleAdmin",                     // identity Role
		"delivery.ACVerificationUnverified",      // AC verification
		"delivery.IssueRefRoleRecurrence",        // issue ref role
		"quality.ReasonOK",                       // eval reason
		"version.RelationDependsOn",              // requirement relation
		"execution.AssetSourceAI",                // asset source
		"design.ExportFormatMermaid",             // export format
		"resource.SUTResolutionAdminCreate",      // SUT resolution
		"version.VersionCloseCheckRunning",       // close checks
		"delivery.ReportTruncationIssues",        // report truncation
		"quality.MetricDesignCoverage",           // eval metric
		"quality.OperationalMetricOrder",         // operational metric order (exported var set)
		"design.ConfirmScopeCase",                // confirmation scope
		"design.ActivationManual",                // round activation
		"design.OverrideConditionFailExhausted",  // judgment override
		"version.CaseRunPending",                 // requirement-closure case-run status
		"version.ClosureEvidenceCaseRun",         // closure evidence kind
		"version.ConfirmationScopeBatch",         // confirmation scope
		"execution.EvidenceOwnerCaseRun",         // evidence owner
		"agent.PromptVariableSourceRuntime",      // prompt variable source
		"delivery.IssueCandidateSource",          // issue candidate source
		"delivery.MergeReasonCycle",              // merge reason
		"design.AutoConfirmationActor",           // auto-confirmation actor
		"design.PriorityP2",                      // priority
		"execution.PauseReasonWaitingHuman",      // pause reason
		"version.AttachmentErrorVisionRequired",  // attachment error
		"version.AttachmentImageRole",            // attachment image role
		"identity.TokenPrefix",                   // token prefix (adjudicated: exported domain constant)
		"version.ACIDPrefix",                     // acceptance-criteria id prefix (same adjudication)
		"quality.EvalModeRealLLM",                // eval mode
		"quality.SnapshotMetricOrder",            // snapshot metric order
		"storage.GCCandidateUnreferenced",        // GC candidate reason
	}
	for _, label := range want {
		if !v.Selected[label] {
			t.Errorf("blind-spot declaration %s is still not selected by the shared R3 rules", label)
		}
	}
	// The values those groups own must be protected, not merely selected.
	for _, value := range []string{"design", "clarification", "job_card", "processing", "extracted", "analyzing",
		"route_to_api", "turn_budget", "rerun", "requirements", "codeintel", "tool", "seed", "P2"} {
		if _, ok := v.Values[value]; !ok {
			t.Errorf("blind-spot value %q is not in the protected set", value)
		}
	}
}

// TestRegisteredGroupFaceIsFailClosed proves the residual registration cannot rot:
// a missing declaration, a non-string group, an empty reason and a group the
// derived rules already select are each reported. The samples are REAL source files
// in a temporary module, not hand-built maps.
func TestRegisteredGroupFaceIsFailClosed(t *testing.T) {
	write := func(t *testing.T, src string) (*govocab.Resolver, []string) {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/probe\n\ngo 1.24\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		domain := filepath.Join(dir, "internal", "x", "domain")
		if err := os.MkdirAll(domain, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(domain, "v.go")
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		resolver, err := govocab.New(dir)
		if err != nil {
			t.Fatal(err)
		}
		return resolver, []string{path}
	}
	src := `package x
const Exported = "kept"
const hiddenCodes = "hidden"
const numericHidden = 7
`
	cases := []struct {
		name         string
		registration vocablint.RegisteredGroup
		wantProblem  string
	}{
		{"missing", vocablint.RegisteredGroup{Pkg: "x", Name: "gone", Reason: "why"}, "does not exist"},
		{"nonstring", vocablint.RegisteredGroup{Pkg: "x", Name: "numericHidden", Reason: "why"}, "no derived string value"},
		{"noreason", vocablint.RegisteredGroup{Pkg: "x", Name: "hiddenCodes", Reason: "  "}, "empty reason"},
		{"redundant", vocablint.RegisteredGroup{Pkg: "x", Name: "Exported", Reason: "why"}, "already selected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolver, files := write(t, src)
			selector := vocablint.R3Selector{
				Pattern:    vocabscope.Pattern(),
				Registered: []vocablint.RegisteredGroup{tc.registration},
			}
			v := vocablint.DeriveR3(resolver, files, selector, map[string]string{})
			found := false
			for _, problem := range v.Problems {
				if strings.Contains(problem, tc.wantProblem) {
					found = true
				}
			}
			if !found {
				t.Fatalf("registration %s produced no %q problem: %v", tc.name, tc.wantProblem, v.Problems)
			}
		})
	}
	// The honest registration works and protects the value.
	resolver, files := write(t, src)
	selector := vocablint.R3Selector{
		Pattern: vocabscope.Pattern(),
		Registered: []vocablint.RegisteredGroup{
			{Pkg: "x", Name: "hiddenCodes", Reason: "a closed unexported code set"},
		},
	}
	v := vocablint.DeriveR3(resolver, files, selector, map[string]string{})
	if len(v.Problems) != 0 {
		t.Fatalf("honest registration reported problems: %v", v.Problems)
	}
	if _, ok := v.Values["hidden"]; !ok {
		t.Fatal("the registered group's value is not protected")
	}
}

// TestTypeFloorProblemsReportsUnregisteredType closes H-A22③'s second half: a
// derived type with no floor entry used to have NO anti-vacuity protection at all.
func TestTypeFloorProblemsReportsUnregisteredType(t *testing.T) {
	perType := map[string][]string{"Registered": {"a", "b"}, "BrandNew": {"c"}}
	floors := map[string]int{"Registered": 2}
	problems := vocablint.TypeFloorProblems(perType, floors)
	if len(problems) != 1 || !strings.Contains(problems[0], "BrandNew") ||
		!strings.Contains(problems[0], "no anti-vacuity floor entry") {
		t.Fatalf("unregistered type not reported: %v", problems)
	}
	floors["BrandNew"] = 1
	if problems := vocablint.TypeFloorProblems(perType, floors); len(problems) != 0 {
		t.Fatalf("registered types reported problems: %v", problems)
	}
	floors["BrandNew"] = 5
	if problems := vocablint.TypeFloorProblems(perType, floors); len(problems) != 1 {
		t.Fatalf("a broken derivation of the new type was not reported: %v", problems)
	}
}

// TestRepositoryTypeFloorsCoverEveryDerivedType keeps the shared floor table in
// lockstep with the real derivation: a newly declared guarded type must be
// registered, and no floor may exceed what the tree derives.
func TestRepositoryTypeFloorsCoverEveryDerivedType(t *testing.T) {
	root := r3TestRepoRoot(t)
	resolver, err := govocab.New(filepath.Join(root, "backend"))
	if err != nil {
		t.Fatalf("resolver: %v", err)
	}
	v := vocablint.DeriveR3(resolver, r3TestDomainFiles(t, root), vocabscope.Selector(), map[string]string{})
	for _, problem := range v.Problems {
		t.Errorf("shared vocabulary scope: %s", problem)
	}
	problems := vocablint.TypeFloorProblems(v.PerType, vocabscope.TypeFloors())
	for _, problem := range problems {
		t.Errorf("shared floor table: %s", problem)
	}
}
