package main

// The REPOSITORY-LEVEL status-vocabulary gate (v0.2.4-H T11e-6, H-A25).
//
// Every infrastructure production file of EVERY bounded context must read a
// status/kind/stage vocabulary through its domain constant -- never through a
// hand-written literal, neither a Go comparison/assignment literal nor a
// SQL-inline quoted literal. Constitution 五.1/五.3/五.4 (single authority, no
// hand-written second truth, no bypass).
//
// # Why a repository-level gate, and why here
//
// T11e-1..4 built four context gates covering 13 of the 19 contexts that have an
// infrastructure layer. Six were never covered by ANY gate: audit, identity,
// quality, resource, settings, workspace. Nothing proved they were clean -- they
// were simply never scanned (H-A25). A per-context gate cannot close that hole:
// the next context added to the repository would be unprotected until someone
// remembered to write a fifth gate. Only the composition root can see every
// context at once, and only since T11e-0 moved the machinery into
// platform/govocab + platform/vocablint is it importable from here (pinned by
// vocablint_reachability_test.go).
//
// This gate's scan surface is DERIVED by walking backend/internal and collecting
// every non-test .go file under an /infrastructure/ segment. There is no
// hand-written context list and no hand-written file list: a new context, a new
// subdirectory, or a new production file joins the net automatically. A context
// that appears with no anti-vacuity floor entry is itself a problem
// (vocablint.SurfaceFloorProblems), so a twentieth context cannot slip in
// unnoticed.
//
// # The six newly covered contexts were MEASURED, not assumed
//
// On the T11e-6 tree they contribute 12 production files and ZERO findings
// against the full 395-value protected set with an EMPTY exemption table
// (t11e6-uncovered.tsv). They therefore join the net with no exemptions at all.
//
// # Existing gates are NOT weakened (Ruling T11d-R4 / 裁定 4)
//
// The four context gates (delivery, version, orchestration-share, agent-share),
// agent's own work-queue gate, execution's two gates, design's application gate
// and the three handoff gates are untouched in their assertions and stay green.
// This gate is an ADDITIONAL global net over a strictly wider surface: it reuses
// the SAME shared derivation (internal/testutil/vocabscope.Derive) and the SAME
// adjudicated exemptions (vocabscope.RepositoryAllowlist merges the four tables
// without dropping a reason or a fragment). Two tests below pin that superset
// relationship permanently, so "the repository gate covers it" can never become
// a reason to relax a context gate.
//
// # Value set (derived, never a closed table)
//
// Every backend/internal/*/domain/*.go non-test file through platform/govocab +
// platform/vocablint under R3 (semantic name on the declared type, the
// declaration, or any name of its const/var group), R3-E (any EXPORTED name in
// the group), R3-R (a registry set enumerating declarations by reference) and the
// one fail-closed residual registration; reinforced by the enum authority
// functions and by version's application-layer extraction statuses. The
// per-declared-type anti-vacuity floors make a broken derivation fail loud, and a
// derived type with no floor entry is itself a problem.
//
// # Exemptions (fragment-scoped, FILE-BOUND, occurrence-scoped, ONE PER OCCURRENCE)
//
// A genuine collision (a wire/DTO field name, a chat wire role, a log key, an
// error-envelope entity name, a frozen template anchor, an executor protocol
// code) is exempted by the EXACT source fragment carrying it, bound to the file
// it lives in, with a specific reason traced to the definition it collides with.
// Only the occurrences a fragment spans are sanctioned; the same text in another
// file is a finding; every fragment must sanction at least one real occurrence
// (index-keyed stale check); and fragments sharing a file and a text must be
// declared once per OCCURRENCE they sanction -- a (line, occurrence-index) pair,
// not a line (T11e-6 M5 + review Minor 3) -- so neither repaying one of two
// identical lines nor changing one of two occurrences on a single line can hide
// behind its twin. Value-level exemptions are forbidden. The tables' value and
// fragment counts are measured from the tables by these tests, never restated in
// a comment (review M1: a restated number is a second truth that drifts).

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil/vocabscope"
)

// repoVocabContextsNeverCovered are the six bounded contexts no context gate had
// ever scanned before T11e-6 (H-A25). They are named here ONLY as a coverage
// assertion -- the scan surface itself is derived, never listed.
var repoVocabContextsNeverCovered = []string{"audit", "identity", "quality", "resource", "settings", "workspace"}

// repoVocabGateRepoRoot walks up from this test's source file to the repository
// root (the directory containing backend/go.mod), so the gate works regardless of
// the test's working directory. It returns an error rather than taking
// *testing.T so the cached derivation can call it too.
func repoVocabGateRepoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("cannot locate the repository-level status vocabulary gate source")
	}
	dir := filepath.Dir(file)
	for i := 0; i < 12; i++ {
		if _, err := os.Stat(filepath.Join(dir, "backend", "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", errors.New("repo root containing backend/go.mod not found from " + file)
}

func repoVocabGateRepoRootT(t *testing.T) string {
	t.Helper()
	root, err := repoVocabGateRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// repoVocabSurface derives the guarded surface: every infrastructure production
// file of every bounded context, repository-relative.
func repoVocabSurface(t *testing.T) vocabscope.Surface {
	t.Helper()
	surface, err := vocabscope.InfrastructureSurface(repoVocabGateRepoRootT(t))
	if err != nil {
		t.Fatalf("H-A25/T11e-6: derive the repository infrastructure scan surface: %v", err)
	}
	return surface
}

// repoVocabVocabularyOnce derives the protected value set ONCE per test binary
// through the SHARED pipeline (the same one every context gate uses).
var repoVocabVocabularyOnce = sync.OnceValue(func() vocablint.R3Vocabulary {
	root, err := repoVocabGateRepoRoot()
	if err != nil {
		return vocablint.R3Vocabulary{Problems: []string{err.Error()}}
	}
	return vocabscope.Derive(root)
})

func deriveRepoVocabVocabulary(t *testing.T) vocablint.R3Vocabulary {
	t.Helper()
	v := repoVocabVocabularyOnce()
	for _, problem := range v.Problems {
		t.Errorf("H-A25/T11e-6: %s", problem)
	}
	if len(v.Problems) > 0 {
		t.Fatalf("%d vocabulary derivation problem(s)", len(v.Problems))
	}
	return v
}

// TestRepositoryStatusVocabularyUsesDomainConstant is the guard.
func TestRepositoryStatusVocabularyUsesDomainConstant(t *testing.T) {
	vocabulary := deriveRepoVocabVocabulary(t)
	surface := repoVocabSurface(t)
	scan := vocablint.ScanSurface(repoVocabGateRepoRootT(t), surface.Files, vocabulary.Values, vocabscope.RepositoryAllowlist())
	for _, problem := range scan.Problems {
		t.Errorf("H-A25/T11e-6: %s", problem)
	}
	if len(scan.Findings) > 0 {
		for _, f := range scan.Findings {
			t.Errorf("H-A25/T11e-6: %s:%d reads %q as a hand-written literal (authorities: %s) -- use the domain constant (Go) or bind it as a query parameter (SQL)",
				f.File, f.Line, f.Value, f.Name)
		}
		t.Fatalf("%d hand-written status literal(s) in the repository infrastructure surface (%d files, %d contexts)",
			len(scan.Findings), len(surface.Files), len(surface.ByContext))
	}
}

// TestRepositoryScanSurfaceCoversEveryInfrastructureContext proves the surface is
// DERIVED and complete: every context with an infrastructure layer is present
// (including the six no context gate ever covered), every per-context floor holds,
// an unregistered context would be reported, and no test file is scanned.
func TestRepositoryScanSurfaceCoversEveryInfrastructureContext(t *testing.T) {
	root := repoVocabGateRepoRootT(t)
	surface := repoVocabSurface(t)
	floors := vocabscope.InfrastructureSurfaceFloors()
	for _, problem := range vocablint.SurfaceFloorProblems(surface.Counts(), floors) {
		t.Errorf("H-A25/T11e-6: %s", problem)
	}
	if problem := vocablint.CountFloorProblem("repository infrastructure production files", len(surface.Files), 172); problem != "" {
		t.Fatalf("H-A25/T11e-6: %s", problem)
	}
	if problem := vocablint.CountFloorProblem("bounded contexts with an infrastructure layer", len(surface.ByContext), 19); problem != "" {
		t.Fatalf("H-A25/T11e-6: %s", problem)
	}
	for _, name := range repoVocabContextsNeverCovered {
		files := surface.ByContext[name]
		if len(files) == 0 {
			t.Fatalf("H-A25/T11e-6: context %q was never covered by a context gate and is STILL missing from the derived repository surface", name)
		}
		for _, file := range files {
			if !strings.HasPrefix(file, "backend/internal/"+name+"/infrastructure/") {
				t.Fatalf("surface file %q does not belong to context %q", file, name)
			}
		}
	}
	// The walk must reach nested infrastructure subdirectories, not just the
	// postgres package a hand-written list would have named.
	for _, want := range []string{
		"backend/internal/audit/infrastructure/postgres.go",
		"backend/internal/identity/infrastructure/bcrypt.go",
		"backend/internal/quality/infrastructure/metrics/aggregator.go",
		"backend/internal/quality/infrastructure/adapters/eventing.go",
		"backend/internal/resource/infrastructure/postgres.go",
		"backend/internal/settings/infrastructure/postgres.go",
		"backend/internal/workspace/infrastructure/providers/version.go",
		"backend/internal/execution/infrastructure/executor/client.go",
		"backend/internal/version/infrastructure/attachments/ooxml.go",
	} {
		if !slices.Contains(surface.Files, want) {
			t.Fatalf("derived repository surface is missing %s (%d files derived)", want, len(surface.Files))
		}
	}
	for _, file := range surface.Files {
		if strings.HasSuffix(file, "_test.go") {
			t.Fatalf("scan surface includes a test file: %s", file)
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(file))); err != nil {
			t.Fatalf("derived surface file %s does not exist: %v", file, err)
		}
	}
}

// TestRepositorySurfaceIsSupersetOfEveryContextGate is the permanent
// non-weakening proof: every file each context gate guards is inside this gate's
// surface. The context gates' surfaces are re-derived here from the SHARED scope
// authority (vocabscope.GateContexts), independently of the repository walk.
func TestRepositorySurfaceIsSupersetOfEveryContextGate(t *testing.T) {
	root := repoVocabGateRepoRootT(t)
	surface := repoVocabSurface(t)
	covered := map[string]bool{}
	for _, file := range surface.Files {
		covered[file] = true
	}
	scopes := vocabscope.GateContexts()
	if len(scopes) == 0 {
		t.Fatal("no context gate scopes declared; the superset assertion would be vacuous")
	}
	for gate, contexts := range scopes {
		files, err := vocabscope.GuardedFiles(root, contexts)
		if err != nil {
			t.Fatalf("%s gate scope: %v", gate, err)
		}
		if len(files) == 0 {
			t.Fatalf("%s gate derived no infrastructure files; the superset assertion would be vacuous", gate)
		}
		for _, file := range files {
			if !covered[file] {
				t.Fatalf("%s gate file %s is OUTSIDE the repository gate surface; the global net would be narrower than the gate it is supposed to stack on", gate, file)
			}
		}
	}
}

// TestGateScopesPartitionRepositorySurface keeps the two scope authorities in
// lockstep without a hand-written context list: the contexts the four gates guard,
// plus the six no gate ever covered, must be EXACTLY the contexts the repository
// surface floors register. A gate that silently dropped a context, or a new
// context nobody claimed, breaks the partition.
func TestGateScopesPartitionRepositorySurface(t *testing.T) {
	claimed := map[string]string{}
	for gate, contexts := range vocabscope.GateContexts() {
		for _, name := range contexts {
			if other, dup := claimed[name]; dup {
				t.Fatalf("context %q is claimed by two gates (%s and %s); the scopes must partition", name, other, gate)
			}
			claimed[name] = gate
		}
	}
	for _, name := range repoVocabContextsNeverCovered {
		if gate, dup := claimed[name]; dup {
			t.Fatalf("context %q is listed as never-covered but gate %s claims it; update the coverage assertion", name, gate)
		}
		claimed[name] = "repository-only"
	}
	floors := vocabscope.InfrastructureSurfaceFloors()
	for _, name := range vocablint.SortedKeys(floors) {
		if _, ok := claimed[name]; !ok {
			t.Fatalf("context %q has a scan-surface floor but is claimed by NO gate and is not listed as repository-only", name)
		}
	}
	for _, name := range vocablint.SortedKeys(claimed) {
		if _, ok := floors[name]; !ok {
			t.Fatalf("context %q is claimed but has no scan-surface floor entry", name)
		}
	}
}

// TestRepositoryAllowlistKeepsEveryContextAdjudication proves the merge loses
// nothing: every fragment of every context gate's table is present in the
// repository table, file-bound and unchanged, and its reason survives. A dropped
// fragment would turn an adjudicated collision into a finding; a dropped reason
// would turn an auditable adjudication into an unexplained exemption.
func TestRepositoryAllowlistKeepsEveryContextAdjudication(t *testing.T) {
	merged := vocabscope.RepositoryAllowlist()
	tables := map[string]map[string]vocablint.AllowEntry{
		"delivery":      vocabscope.DeliveryGateAllowlist(),
		"version":       vocabscope.VersionGateAllowlist(),
		"orchestration": vocabscope.OrchestrationGateAllowlist(),
		"agent":         vocabscope.AgentGateAllowlist(),
	}
	total := 0
	for name, table := range tables {
		if len(table) == 0 {
			t.Fatalf("%s gate table is empty; the merge assertion would be vacuous", name)
		}
		for value, entry := range table {
			got, ok := merged[value]
			if !ok {
				t.Fatalf("%s gate adjudication of %q is missing from the repository allowlist", name, value)
			}
			if !strings.Contains(got.Reason, entry.Reason) {
				t.Fatalf("%s gate reason for %q was dropped by the merge", name, value)
			}
			for _, fragment := range entry.Fragments {
				if !slices.Contains(got.Fragments, fragment) {
					t.Fatalf("%s gate fragment %q (%s) for %q is missing from the repository allowlist",
						name, fragment.Text, fragment.File, value)
				}
				total++
			}
		}
	}
	if problem := vocablint.CountFloorProblem("merged adjudicated fragments", total, 160); problem != "" {
		t.Fatalf("H-A25/T11e-6: %s", problem)
	}
	for _, problem := range vocablint.AllowlistShapeProblems(merged) {
		t.Errorf("H-A25/T11e-6: %s", problem)
	}
}

// TestRepositoryVocabularyFloor pins the protected value set's size. The
// per-declared-type floors already run inside the shared derivation; this is the
// whole-set bound, so a derivation that silently loses a whole rule (R3-E off,
// the registration dropped) cannot pass by keeping each type above its floor.
// The expectation is a LOWER BOUND measured on the T11e-6 tree (395 values), not
// a closed value table.
func TestRepositoryVocabularyFloor(t *testing.T) {
	vocabulary := deriveRepoVocabVocabulary(t)
	if problem := vocablint.CountFloorProblem("protected vocabulary values", len(vocabulary.Values), 395); problem != "" {
		t.Fatalf("H-A25/T11e-6: %s", problem)
	}
	if problem := vocablint.CountFloorProblem("declared vocabulary type buckets", len(vocabulary.PerType), 49); problem != "" {
		t.Fatalf("H-A25/T11e-6: %s", problem)
	}
}
