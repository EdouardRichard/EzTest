package vocablint_test

// T11e-6 M6 / M8: the two selection boundaries T11e-5 left open.
//
//   - M6: R3-R resolved a registry's references while it was still collecting
//     groups, so a FORWARD reference (the registry declared before the group it
//     enumerates) marked only the referenced name Selected and left the rest of
//     that group unprotected and its per-type bucket empty. Selection now runs
//     after the whole index exists; this test pins both directions.
//   - M8: R3-E admits every EXPORTED domain const group, which includes the id
//     PREFIX constants (identity.TokenPrefix, version.ACIDPrefix). They are not
//     status enums, so their class membership needed an explicit adjudication
//     and a permanent guard rather than an accident of the rule.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/govocab"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil/vocabscope"
)

// r3ProbeModule writes one real domain source file into a temporary module and
// returns the resolver plus the file list, so the samples are parsed and resolved
// exactly like production source (no hand-built declaration maps).
func r3ProbeModule(t *testing.T, src string) (*govocab.Resolver, []string) {
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

// TestR3RBackfillsForwardReferencedGroup is the M6 discriminating test. The
// registry is declared FIRST and enumerates a group declared LATER whose names
// carry no semantic pattern and are all unexported, so only R3-R can reach them.
// The group also carries a FOURTH member the registry does not enumerate: group
// adoption must protect it too, which is exactly what a per-NAME selection (the
// pre-T11e-6 shape) missed. Every member must be Selected, every value protected,
// and the group's per-type bucket must carry them. The backward-reference control
// proves the fix did not break the direction that already worked.
func TestR3RBackfillsForwardReferencedGroup(t *testing.T) {
	const members = `const (
	countAlpha = "alpha"
	countBeta  = "beta"
	countGamma = "gamma"
	countDelta = "delta"
)
`
	cases := []struct {
		name string
		src  string
	}{
		{"forward", "package x\nvar countKeys = []string{countAlpha, countBeta, countGamma}\n" + members},
		{"backward", "package x\n" + members + "var countKeys = []string{countAlpha, countBeta, countGamma}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolver, files := r3ProbeModule(t, tc.src)
			// The synthetic module carries no residual registration, so the
			// selector is the pattern alone (the repository registration names a
			// declaration that only exists in the real tree).
			selector := vocablint.R3Selector{Pattern: vocabscope.Pattern()}
			v := vocablint.DeriveR3(resolver, files, selector, map[string]string{})
			if len(v.Problems) != 0 {
				t.Fatalf("derivation problems: %v", v.Problems)
			}
			for _, name := range []string{"countAlpha", "countBeta", "countGamma", "countDelta"} {
				if !v.Selected["x."+name] {
					t.Fatalf("%s reference: x.%s was not selected; the referenced group's membership was not backfilled", tc.name, name)
				}
			}
			for _, value := range []string{"alpha", "beta", "gamma", "delta"} {
				if _, ok := v.Values[value]; !ok {
					t.Fatalf("%s reference: value %q is not protected", tc.name, value)
				}
			}
			// The per-type bucket is the other half T11e-5 left empty on a forward
			// reference: the values were protected but never counted, so the
			// anti-vacuity floor for that bucket saw nothing.
			if got := len(v.PerType["(untyped)"]); got < 4 {
				t.Fatalf("%s reference: (untyped) bucket holds %d values, want >= 4 (per-type backfill missing)", tc.name, got)
			}
		})
	}
}

// TestR3RResolvesChainedRegistries is the review Minor 4 pair: a registry ADOPTED
// by an earlier registry must still enumerate its OWN references, or a chain
// A -> B -> C stops at B. Both orders are exercised, because the pre-fix code
// skipped any group already marked selected and so only one order worked.
func TestR3RResolvesChainedRegistries(t *testing.T) {
	const leaf = `const (
	leafOne = "leaf_one"
	leafTwo = "leaf_two"
)
`
	const middle = "var middleKeys = []string{leafOne, leafTwo}\n"
	const top = "var topKeys = []string{middleKeys}\n"
	// topKeys references middleKeys, which is itself a registry: a set whose only
	// member is a bare identifier. Both must be resolved regardless of order.
	for _, tc := range []struct {
		name string
		src  string
	}{
		{"adopter-first", "package x\n" + top + middle + leaf},
		{"adopter-last", "package x\n" + leaf + middle + top},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolver, files := r3ProbeModule(t, tc.src)
			selector := vocablint.R3Selector{Pattern: vocabscope.Pattern()}
			v := vocablint.DeriveR3(resolver, files, selector, map[string]string{})
			if len(v.Problems) != 0 {
				t.Fatalf("derivation problems: %v", v.Problems)
			}
			for _, name := range []string{"leafOne", "leafTwo"} {
				if !v.Selected["x."+name] {
					t.Fatalf("%s: x.%s not selected; the chained registry's own references were skipped", tc.name, name)
				}
			}
			for _, value := range []string{"leaf_one", "leaf_two"} {
				if _, ok := v.Values[value]; !ok {
					t.Fatalf("%s: value %q not protected", tc.name, value)
				}
			}
		})
	}
}

// TestR3EProtectsExportedIDPrefixes pins the M8 adjudication: id PREFIX constants
// are IN the protected vocabulary. An adapter that hard-codes "ezt_" or "AC-"
// instead of sourcing identity.TokenPrefix / version.ACIDPrefix is exactly the
// second-truth defect this class exists to catch, so excluding them by name would
// open a blind spot (and a name-based exclusion list would itself be a second
// truth). The unexported host-turn prefix stays out: it is implementation data of
// one domain function, the same shape as the NLP word lists Ruling T11d-R1
// forbids. The scanner positive control proves the decision has teeth.
func TestR3EProtectsExportedIDPrefixes(t *testing.T) {
	root := r3TestRepoRoot(t)
	resolver, err := govocab.New(filepath.Join(root, "backend"))
	if err != nil {
		t.Fatal(err)
	}
	v := vocablint.DeriveR3(resolver, r3TestDomainFiles(t, root), vocabscope.Selector(), map[string]string{})
	if len(v.Problems) != 0 {
		t.Fatalf("derivation problems: %v", v.Problems)
	}
	for _, tc := range []struct {
		label string
		value string
		want  bool
	}{
		{"identity.TokenPrefix", "ezt_", true},
		{"version.ACIDPrefix", "AC-", true},
		{"version.hostTurnClientMessageIDPrefix", "host-turn:", false},
	} {
		if got := v.Selected[tc.label]; got != tc.want {
			t.Fatalf("%s selected = %v, want %v (M8 adjudication drifted)", tc.label, got, tc.want)
		}
		if _, ok := v.Values[tc.value]; ok != tc.want {
			t.Fatalf("value %q protected = %v, want %v", tc.value, ok, tc.want)
		}
	}
	// Positive control: an infrastructure file that hard-codes an exported
	// prefix instead of sourcing the constant IS a finding.
	rogue := "package p\nfunc token(raw string) string { return \"ezt_\" + raw }\n"
	if got := vocablint.ScanLiterals("backend/internal/identity/infrastructure/token.go", rogue, v.Values, nil).Findings; len(got) != 1 {
		t.Fatalf("hard-coded id prefix produced %d findings, want 1: %+v", len(got), got)
	}
	// Negative control: the unexported prefix is not vocabulary, so the same
	// shape carrying it is not a finding.
	unexported := "package p\nfunc id(turn int64) string { return \"host-turn:\" + itoa(turn) }\n"
	if got := vocablint.ScanLiterals("backend/internal/version/infrastructure/conversation.go", unexported, v.Values, nil).Findings; len(got) != 0 {
		t.Fatalf("the unexported host-turn prefix was treated as vocabulary: %+v", got)
	}
	// The exclusion must be real: the declaration exists in the source, so the
	// negative control above cannot pass because the sample was never there.
	src, err := os.ReadFile(filepath.Join(root, "backend", "internal", "version", "domain", "conversation.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `hostTurnClientMessageIDPrefix = "host-turn:"`) {
		t.Fatal("the unexported prefix declaration moved; the M8 negative control would be vacuous")
	}
}
