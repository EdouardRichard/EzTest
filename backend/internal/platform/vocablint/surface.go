package vocablint

// Scan-surface derivation and anti-vacuity floors. A gate's guarded file set must
// be DERIVED from the directory it guards: a hand-written file list lets a new
// production file silently escape the gate (the T4 minor① failure class). The
// floors below are derivation sanity checks, not coverage lists — they make a
// broken derivation fail loud instead of going vacuous.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// GoFilesInDir lists the non-test .go files directly inside dir (no recursion),
// sorted by name. The returned names are relative to dir, so a gate running in
// its own package directory can read them directly.
func GoFilesInDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, name)
	}
	sort.Strings(files)
	return files, nil
}

// GoFilesUnder lists every non-test .go file under root, recursively, sorted
// lexically. The returned paths follow root: an absolute root yields absolute
// paths, a relative root yields paths relative to the caller's directory
// (filepath.WalkDir joins onto root). A gate reports sites relative to the
// repository root, so it converts these with filepath.Rel itself.
func GoFilesUnder(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// CountFloorProblem returns a non-empty problem when got < floor, naming what was
// being derived. It is the anti-vacuity check for a single derived count.
func CountFloorProblem(what string, got, floor int) string {
	if got >= floor {
		return ""
	}
	return fmt.Sprintf("only %d %s derived (want >= %d); the derivation is broken and the gate would go vacuous", got, what, floor)
}

// TypeFloorProblems returns one problem per guarded type whose derived count is
// below its sanity floor, plus one problem per derived type that has NO floor
// entry at all, in type-name order (deterministic, unlike ranging the floor map).
// perType holds the values derived for each type; floors is the per-type minimum.
//
// The counts are per-NAME accumulations, not distinct-value counts: a type whose
// two constants share one value contributes 2, and one value defined by several
// types is counted under each. So a floor is a bound on the DERIVATION WALK, never
// a claim about the size of the protected value set (H-A22③: "(untyped)" 229 while
// the whole set held 221 distinct values).
//
// The unregistered-type problem closes the second half of H-A22③: iterating only
// the floors keys meant a NEWLY declared guarded type with no floor entry had no
// anti-vacuity protection whatsoever — its whole bucket could vanish silently.
func TypeFloorProblems(perType map[string][]string, floors map[string]int) []string {
	counts := make(map[string]int, len(perType))
	for name, values := range perType {
		counts[name] = len(values)
	}
	return FloorProblems(counts, floors, FloorSubject{
		Kind:           "declared type",
		Unit:           "vocabulary values",
		BelowNoun:      "constants",
		RegisterSuffix: "register a floor so a broken derivation of this type cannot go vacuous",
	})
}

// SurfaceFloorProblems is the same check for a derived SCAN SURFACE: one problem per
// context whose derived production-file count is below its floor, plus one per
// context that has no floor entry at all. It is what makes a repository-level gate
// fail loud when a new bounded context grows an infrastructure layer (H-A25).
func SurfaceFloorProblems(counts map[string]int, floors map[string]int) []string {
	return FloorProblems(counts, floors, FloorSubject{
		Kind:           "bounded context",
		Unit:           "infrastructure production files",
		BelowNoun:      "infrastructure production files",
		RegisterSuffix: "register a scan-surface floor so a new context cannot join the repository unprotected",
	})
}

// FloorSubject names what a per-key floor table bounds, so ONE algorithm reports
// both declared-type value floors and scan-surface file floors. Two copies of this
// loop would be a second truth about what "anti-vacuity" means (constitution 五.3).
type FloorSubject struct {
	Kind           string // what a key is: "declared type", "bounded context"
	Unit           string // what is counted, in the unregistered-key diagnostic
	BelowNoun      string // what is counted, in the below-floor diagnostic
	RegisterSuffix string // what registering a floor protects against
}

// FloorProblems reports every key whose count is below its floor and every counted
// key with no floor entry, in key order (deterministic, unlike ranging a map).
func FloorProblems(counts map[string]int, floors map[string]int, subject FloorSubject) []string {
	names := SortedKeys(floors)
	for _, name := range SortedKeys(counts) {
		if _, ok := floors[name]; !ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var problems []string
	for _, name := range names {
		floor, registered := floors[name]
		if !registered {
			problems = append(problems, fmt.Sprintf("%s %q contributes %d %s but has no anti-vacuity floor entry; %s",
				subject.Kind, name, counts[name], subject.Unit, subject.RegisterSuffix))
			continue
		}
		if counts[name] < floor {
			problems = append(problems, fmt.Sprintf("only %d %s %s derived (want >= %d); the derivation is broken and the gate would go vacuous",
				counts[name], name, subject.BelowNoun, floor))
		}
	}
	return problems
}

// DedupValues appends the values of each type to one deduplicated slice, walking
// the types in the order the CALLER supplies (pass SortedKeys(floors) for
// type-name order) and the values of each type in declaration order. First
// occurrence wins, so the guarded value set is stable across runs.
func DedupValues(perType map[string][]string, order []string) []string {
	set := map[string]bool{}
	var values []string
	for _, name := range order {
		for _, v := range perType[name] {
			if !set[v] {
				set[v] = true
				values = append(values, v)
			}
		}
	}
	return values
}

// SortedKeys returns the keys of a set map in lexical order.
func SortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sqlQuotedValuePattern matches one single-quoted SQL literal.
var sqlQuotedValuePattern = regexp.MustCompile(`'([^']*)'`)

// SQLQuotedValues extracts every single-quoted literal of a SQL fragment (a CHECK
// definition from pg_get_constraintdef, an index predicate, an IN list), sorted and
// deduplicated, so a schema pin compares SETS rather than order or repetition. It
// is the single authority for that extraction: delivery's schema pins owned it
// first and design's trigger pin needed the same comparison (T11e-5 Part E).
func SQLQuotedValues(fragment string) []string {
	seen := map[string]bool{}
	var out []string
	for _, match := range sqlQuotedValuePattern.FindAllStringSubmatch(fragment, -1) {
		if !seen[match[1]] {
			seen[match[1]] = true
			out = append(out, match[1])
		}
	}
	sort.Strings(out)
	return out
}

// SortedSet copies values into lexical order, leaving the caller's slice untouched.
func SortedSet(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
