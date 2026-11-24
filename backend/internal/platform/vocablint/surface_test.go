package vocablint_test

// Scan-surface derivation and anti-vacuity floor properties. The discriminative
// case is the T4 minor① failure class: a NEW production file must join a derived
// scan surface automatically, and a test file / subdirectory must not.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, src := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

func TestGoFilesInDirDerivesNonTestFilesOnly(t *testing.T) {
	root := writeTree(t, map[string]string{
		"wave.go":            "package p\n",
		"case_run.go":        "package p\n",
		"wave_test.go":       "package p\n",
		"notes.md":           "not go\n",
		"sub/nested.go":      "package sub\n",
		"sub/nested_test.go": "package sub\n",
	})
	got, err := vocablint.GoFilesInDir(root)
	if err != nil {
		t.Fatalf("GoFilesInDir: %v", err)
	}
	if len(got) != 2 || got[0] != "case_run.go" || got[1] != "wave.go" {
		t.Fatalf("derived surface = %v, want [case_run.go wave.go] (sorted, non-test, no recursion)", got)
	}
	// A NEW repository file joins the surface without editing any list.
	if err := os.WriteFile(filepath.Join(root, "human.go"), []byte("package p\n"), 0o600); err != nil {
		t.Fatalf("write new file: %v", err)
	}
	got, err = vocablint.GoFilesInDir(root)
	if err != nil {
		t.Fatalf("GoFilesInDir after add: %v", err)
	}
	if len(got) != 3 || got[1] != "human.go" {
		t.Fatalf("a new production file did not join the derived surface: %v", got)
	}
}

func TestGoFilesUnderRecursesAndSkipsTests(t *testing.T) {
	root := writeTree(t, map[string]string{
		"domain/a.go":        "package domain\n",
		"domain/a_test.go":   "package domain\n",
		"postgres/b.go":      "package pg\n",
		"postgres/b_test.go": "package pg\n",
		"postgres/deep/c.go": "package deep\n",
	})
	got, err := vocablint.GoFilesUnder(root)
	if err != nil {
		t.Fatalf("GoFilesUnder: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("derived surface = %v, want 3 non-test files across subdirectories", got)
	}
	for i, path := range got {
		if strings.HasSuffix(path, "_test.go") {
			t.Fatalf("test file in derived surface: %s", path)
		}
		if !filepath.IsAbs(path) {
			t.Fatalf("path %s is not absolute; the gate's repo-relative reporting would break", path)
		}
		if i > 0 && got[i-1] > path {
			t.Fatalf("derived surface is not sorted: %v", got)
		}
	}
}

func TestGoFilesMissingDirIsLoud(t *testing.T) {
	if _, err := vocablint.GoFilesInDir(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a missing package directory was silently treated as an empty scan surface")
	}
	if _, err := vocablint.GoFilesUnder(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a missing context directory was silently treated as an empty scan surface")
	}
}

func TestFloorHelpersAreDiscriminative(t *testing.T) {
	if p := vocablint.CountFloorProblem("guarded files", 4, 4); p != "" {
		t.Fatalf("a satisfied floor reported a problem: %q", p)
	}
	p := vocablint.CountFloorProblem("guarded files", 3, 4)
	if p == "" || !strings.Contains(p, "only 3 guarded files derived (want >= 4)") {
		t.Fatalf("floor problem = %q, want the shortfall named", p)
	}

	floors := map[string]int{"WaveStatus": 6, "CaseRunStatus": 10, "HumanKind": 5}
	perType := map[string][]string{
		"WaveStatus":    {"planned", "running", "paused", "done", "failed", "cancelled"},
		"CaseRunStatus": {"pending", "running"},
		"HumanKind":     {"approval", "conflict", "clarification_question", "login_takeover", "blocked_recovery"},
	}
	problems := vocablint.TypeFloorProblems(perType, floors)
	if len(problems) != 1 || !strings.Contains(problems[0], "only 2 CaseRunStatus constants derived (want >= 10)") {
		t.Fatalf("type floor problems = %v, want exactly the CaseRunStatus shortfall", problems)
	}
	// A type that derived NOTHING is below its floor too (a moved authority).
	problems = vocablint.TypeFloorProblems(map[string][]string{}, floors)
	if len(problems) != 3 {
		t.Fatalf("empty derivation problems = %v, want one per guarded type", problems)
	}
	if !strings.HasPrefix(problems[0], "only 0 CaseRunStatus") {
		t.Fatalf("type floor problems are not in type-name order: %v", problems)
	}
}

func TestDedupValuesIsStableAndDeduplicated(t *testing.T) {
	perType := map[string][]string{
		"B": {"running", "done"},
		"A": {"open", "running"},
	}
	got := vocablint.DedupValues(perType, vocablint.SortedKeys(perType))
	want := []string{"open", "running", "done"}
	if len(got) != len(want) {
		t.Fatalf("dedup = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dedup = %v, want %v (type-name order, first occurrence wins)", got, want)
		}
	}
}
