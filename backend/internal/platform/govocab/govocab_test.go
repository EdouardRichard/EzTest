package govocab_test

// Derivation properties of the vocabulary resolver, proven on real source
// samples (禁 born-green). Each case is a shape the former guards were blind to:
// a var-defined value, a var SET, a same-package alias, a cross-package constant
// reference, a string conversion, a concatenation and a value-elided const.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/govocab"
)

// writePackage writes a synthetic package into a temporary module-shaped tree
// and returns the resolver plus the path of the file to derive from.
//
//	<tmp>/go.mod                      module example.test/mod
//	<tmp>/internal/<pkg>/<file>.go    the given sources
func writePackage(t *testing.T, files map[string]string, target string) (*govocab.Resolver, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/mod\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	for name, src := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	resolver, err := govocab.New(root)
	if err != nil {
		t.Fatalf("govocab.New: %v", err)
	}
	return resolver, filepath.Join(root, filepath.FromSlash(target))
}

func declByName(t *testing.T, decls []govocab.Decl, name string) govocab.Decl {
	t.Helper()
	for _, decl := range decls {
		if decl.Name == name {
			return decl
		}
	}
	t.Fatalf("declaration %q not derived (got %d declarations)", name, len(decls))
	return govocab.Decl{}
}

func TestDerivesConstAndVarStringValues(t *testing.T) {
	resolver, file := writePackage(t, map[string]string{
		"internal/v/v.go": `package v

const ConstStatus = "approved"

var VarStatus = "proposed"

var StatusSet = []string{"done", "failed"}

var StatusMap = map[string]struct{}{"pending": {}, "in_progress": {}}

const Numeric = 64 << 10

var ErrThing = errors.New("not a vocabulary value")
`,
	}, "internal/v/v.go")
	decls, err := resolver.Decls(file, false)
	if err != nil {
		t.Fatalf("Decls: %v", err)
	}
	if got := declByName(t, decls, "ConstStatus"); !got.IsString || len(got.Values) != 1 || got.Values[0] != "approved" {
		t.Fatalf("const value = %+v", got)
	}
	// The var blind spot: a var-defined status value is a vocabulary value too.
	if got := declByName(t, decls, "VarStatus"); !got.IsString || len(got.Values) != 1 || got.Values[0] != "proposed" {
		t.Fatalf("var value = %+v (the former const-only scan never saw it)", got)
	}
	// The var SET blind spot: every member is protected, not just the first.
	set := declByName(t, decls, "StatusSet")
	if !set.IsString || strings.Join(set.Values, ",") != "done,failed" {
		t.Fatalf("var set values = %+v", set)
	}
	keys := declByName(t, decls, "StatusMap")
	if !keys.IsString || strings.Join(keys.Values, ",") != "pending,in_progress" {
		t.Fatalf("map key set values = %+v", keys)
	}
	// A numeric const and an error value are not compile-time strings: they are
	// skipped WITHOUT needing an allowlist entry, and are not "unresolved".
	for _, name := range []string{"Numeric", "ErrThing"} {
		got := declByName(t, decls, name)
		if got.IsString || got.Unresolved {
			t.Fatalf("%s must be classified non-string, not unresolved: %+v", name, got)
		}
	}
}

func TestResolvesAliasesConversionsAndConcatenation(t *testing.T) {
	resolver, file := writePackage(t, map[string]string{
		"internal/v/v.go": `package v

const base = "aborted"
const Alias = base
const Converted = string(base)
const Joined = base + "_final"
const Paren = (base)

const (
	First = "done"
	Elided
)
`,
		"internal/v/other.go": "package v\n\nconst CrossFile = base\n",
	}, "internal/v/v.go")
	decls, err := resolver.Decls(file, false)
	if err != nil {
		t.Fatalf("Decls: %v", err)
	}
	for name, want := range map[string]string{
		"Alias": "aborted", "Converted": "aborted", "Joined": "aborted_final",
		"Paren": "aborted", "Elided": "done",
	} {
		got := declByName(t, decls, name)
		if !got.IsString || len(got.Values) != 1 || got.Values[0] != want {
			t.Errorf("%s = %+v, want %q (the former BasicLit-only scan dropped it)", name, got, want)
		}
	}
}

func TestResolvesCrossPackageConstant(t *testing.T) {
	resolver, file := writePackage(t, map[string]string{
		"internal/other/other.go": "package other\n\nconst FactKindProgress = \"progress\"\n",
		"internal/v/v.go": `package v

import other "example.test/mod/internal/other"

const Alias = other.FactKindProgress
`,
	}, "internal/v/v.go")
	decls, err := resolver.Decls(file, false)
	if err != nil {
		t.Fatalf("Decls: %v", err)
	}
	got := declByName(t, decls, "Alias")
	if !got.IsString || len(got.Values) != 1 || got.Values[0] != "progress" {
		t.Fatalf("cross-package constant = %+v, want progress", got)
	}
}

func TestUnresolvedStringShapeIsLoudNotSilent(t *testing.T) {
	resolver, file := writePackage(t, map[string]string{
		"internal/v/v.go": `package v

import "time"

const ExternalAlias = time.Second
const Dangling = missingIdent
const CycleA = CycleB
const CycleB = CycleA
`,
	}, "internal/v/v.go")
	decls, err := resolver.Decls(file, false)
	if err != nil {
		t.Fatalf("Decls: %v", err)
	}
	// A string-SHAPED expression the resolver cannot evaluate must be reported
	// as unresolved so the caller fails loud (or classifies it explicitly). It
	// must never be silently dropped: that was the alias blind spot.
	for _, name := range []string{"ExternalAlias", "Dangling", "CycleA", "CycleB"} {
		got := declByName(t, decls, name)
		if got.IsString || !got.Unresolved {
			t.Errorf("%s must be unresolved (loud), got %+v", name, got)
		}
	}
}

func TestResolvesNamedTypeConversionsAndSkipsPredeclared(t *testing.T) {
	resolver, file := writePackage(t, map[string]string{
		"internal/other/other.go": "package other\n\ntype Kind string\n",
		"internal/v/v.go": `package v

import other "example.test/mod/internal/other"

type Status string

const Local = Status("aborted")
const Cross = other.Kind("progress")

const (
	First = iota
	Second
)

func makeStatus(s string) Status { return Status(s) }

var Computed = makeStatus("aborted")
`,
	}, "internal/v/v.go")
	decls, err := resolver.Decls(file, false)
	if err != nil {
		t.Fatalf("Decls: %v", err)
	}
	// A conversion to a NAMED string type preserves the value: the callee is
	// recognised as a type from the package's type declarations, not guessed.
	for name, want := range map[string]string{"Local": "aborted", "Cross": "progress"} {
		got := declByName(t, decls, name)
		if !got.IsString || len(got.Values) != 1 || got.Values[0] != want {
			t.Errorf("%s = %+v, want %q (named-type conversion blind spot)", name, got, want)
		}
	}
	// iota is a predeclared integer: non-string, and NOT "unresolved" (it must not
	// demand an allowlist entry).
	for _, name := range []string{"First", "Second"} {
		got := declByName(t, decls, name)
		if got.IsString || got.Unresolved {
			t.Errorf("%s (iota) must be non-string and resolved: %+v", name, got)
		}
	}
	// A real function call is NOT a conversion: its value is computed at runtime
	// and is out of the guard's declared coverage.
	computed := declByName(t, decls, "Computed")
	if computed.IsString || computed.Unresolved {
		t.Errorf("a function call must not be treated as a conversion: %+v", computed)
	}
}

// TestPartialSetKeepsUnresolvedLoud is the I1 regression: a set with ONE literal
// member and ONE unresolvable member must still report Unresolved. Dropping the
// flag because some values resolved is the alias blind spot in set form — the
// unresolvable member would never reach the caller's classification rule.
func TestPartialSetKeepsUnresolvedLoud(t *testing.T) {
	resolver, file := writePackage(t, map[string]string{
		"internal/v/v.go": `package v

var PartialSet = []string{"done", missingIdent}
var PartialMap = map[string]struct{}{"pending": {}, missingIdent: {}}
var AllUnresolved = []string{missingIdent}
`,
	}, "internal/v/v.go")
	decls, err := resolver.Decls(file, false)
	if err != nil {
		t.Fatalf("Decls: %v", err)
	}
	partial := declByName(t, decls, "PartialSet")
	if !partial.IsString || strings.Join(partial.Values, ",") != "done" {
		t.Fatalf("the resolvable member must still be derived: %+v", partial)
	}
	if !partial.Unresolved {
		t.Fatal("a partially unresolvable set reported Unresolved=false: the unresolvable member is silently dropped")
	}
	partialMap := declByName(t, decls, "PartialMap")
	if !partialMap.IsString || strings.Join(partialMap.Values, ",") != "pending" || !partialMap.Unresolved {
		t.Fatalf("a partially unresolvable map key set must keep both signals: %+v", partialMap)
	}
	all := declByName(t, decls, "AllUnresolved")
	if all.IsString || !all.Unresolved {
		t.Fatalf("a wholly unresolvable set = %+v", all)
	}
}

// TestStringShapedExpressionsAreLoud covers the expression forms that CAN yield a
// string but cannot be evaluated here: they must be reported unresolved (loud),
// never silently classified as "provably non-string".
func TestStringShapedExpressionsAreLoud(t *testing.T) {
	resolver, file := writePackage(t, map[string]string{
		"internal/v/v.go": `package v

var Set = []string{"done"}

var Indexed = Set[0]
var Sliced = Set[0:1]
var Received = <-ch
var Deref = *ptr
var Asserted = any(value).(string)

var Negated = -1
var Notted = !true
var Addressed = &Set
`,
	}, "internal/v/v.go")
	decls, err := resolver.Decls(file, false)
	if err != nil {
		t.Fatalf("Decls: %v", err)
	}
	for _, name := range []string{"Indexed", "Sliced", "Received", "Deref", "Asserted"} {
		got := declByName(t, decls, name)
		if got.IsString || !got.Unresolved {
			t.Errorf("%s can yield a string but is not evaluable here: must be unresolved, got %+v", name, got)
		}
	}
	for _, name := range []string{"Negated", "Notted", "Addressed"} {
		got := declByName(t, decls, name)
		if got.IsString || got.Unresolved {
			t.Errorf("%s is provably non-string: must not demand classification, got %+v", name, got)
		}
	}
}

func TestExportedFilterAndDeclarationMetadata(t *testing.T) {
	resolver, file := writePackage(t, map[string]string{
		"internal/v/v.go": "package v\n\nconst Exported = \"yes\"\nconst unexported = \"no\"\nvar AlsoExported = \"var\"\n",
	}, "internal/v/v.go")
	all, err := resolver.Decls(file, false)
	if err != nil {
		t.Fatalf("Decls: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all declarations = %d, want 3", len(all))
	}
	exported, err := resolver.Decls(file, true)
	if err != nil {
		t.Fatalf("Decls(exported): %v", err)
	}
	if len(exported) != 2 {
		t.Fatalf("exported declarations = %d, want 2", len(exported))
	}
	for _, decl := range exported {
		if decl.Pkg != "v" || decl.File != file || decl.Line == 0 {
			t.Fatalf("declaration metadata missing: %+v", decl)
		}
		if decl.Tok.String() != "const" && decl.Tok.String() != "var" {
			t.Fatalf("declaration token = %v", decl.Tok)
		}
	}
}
