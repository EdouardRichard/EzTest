package execution

// Evidence-kind single-source gate (final-review finding 7, the acute frontend
// copy). EvidenceKinds() is the ONE authority; the frontend keeps a hand-written
// TypeScript union (EvidenceKind in features/execution/types.ts). This gate
// validates that union against the authority so it is a checked projection,
// never a second independent truth — the same gate-validate pattern as
// version/domain/enum_authority_test.go (the worktree has no TS codegen
// pipeline). A kind added/removed on EITHER side fails until the other is
// updated. The sibling single-source gates over the same authority are now
// implemented (19号 §4.1e, v0.2.4-H): the DB CHECK derives from EvidenceKinds()
// in infrastructure/postgres/evidence_kind_check_integration_test.go (H-D1), and
// the executor ARTIFACT_KINDS cross-language SUBSET pin (H-D2, T1-R4) derives the
// fixture docs/contracts/evidence-kinds.json via evidence_kinds_fixture_test.go,
// consumed by executor/test/artifact_kinds_authority.test.mjs.

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// TestEvidenceKindFrontendUnion fails when the frontend EvidenceKind union
// drifts from EvidenceKinds(). The expected set is DERIVED from the Go
// authority, never hand-copied.
func TestEvidenceKindFrontendUnion(t *testing.T) {
	src := readEvidenceFrontendTypes(t)
	got := evidenceUnionMembers(t, src, "EvidenceKind")
	want := make([]string, 0, len(EvidenceKinds()))
	for _, kind := range EvidenceKinds() {
		want = append(want, string(kind))
	}
	gotSet := make(map[string]bool, len(got))
	for _, g := range got {
		gotSet[g] = true
	}
	wantSet := make(map[string]bool, len(want))
	for _, w := range want {
		wantSet[w] = true
	}
	for _, w := range want {
		if !gotSet[w] {
			t.Errorf("EvidenceKind: frontend union is missing domain kind %q", w)
		}
	}
	for _, g := range got {
		if !wantSet[g] {
			t.Errorf("EvidenceKind: frontend union has extra kind %q absent from EvidenceKinds()", g)
		}
	}
	if len(got) != len(want) {
		t.Errorf("EvidenceKind: frontend union has %d members %v, EvidenceKinds() has %d %v", len(got), got, len(want), want)
	}
}

// readEvidenceFrontendTypes walks up from this test's source file to the repo
// root containing frontend/, so the gate works regardless of the working dir.
func readEvidenceFrontendTypes(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate evidence kind authority test source")
	}
	rel := filepath.FromSlash("frontend/src/features/execution/types.ts")
	dir := filepath.Dir(file)
	for i := 0; i < 12; i++ {
		if data, err := os.ReadFile(filepath.Join(dir, rel)); err == nil {
			// Normalize CRLF so union boundary detection is line-ending agnostic.
			return strings.ReplaceAll(string(data), "\r\n", "\n")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("frontend/src/features/execution/types.ts not found from " + file)
	return ""
}

var evidenceUnionLiteral = regexp.MustCompile(`"([^"]+)"`)

// evidenceUnionMembers extracts the quoted string literals of
// `export type <name> = ...` up to the next blank line or export.
func evidenceUnionMembers(t *testing.T, src, name string) []string {
	t.Helper()
	marker := "export type " + name + " ="
	idx := strings.Index(src, marker)
	if idx < 0 {
		t.Fatalf("frontend union %s not found", name)
	}
	rest := src[idx+len(marker):]
	end := len(rest)
	if i := strings.Index(rest, "\n\n"); i >= 0 && i < end {
		end = i
	}
	if i := strings.Index(rest, "\nexport "); i >= 0 && i < end {
		end = i
	}
	matches := evidenceUnionLiteral.FindAllStringSubmatch(rest[:end], -1)
	members := make([]string, 0, len(matches))
	for _, m := range matches {
		members = append(members, m[1])
	}
	if len(members) == 0 {
		t.Fatalf("frontend union %s has no string members", name)
	}
	return members
}
