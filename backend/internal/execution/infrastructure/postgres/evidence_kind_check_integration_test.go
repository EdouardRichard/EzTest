//go:build integration

package executionpg

// H-D1 (constitution 五.1/五.4): the evidence.kind DB CHECK constraint must equal
// the single execution-domain evidence-kind registry. The expected value set is
// DERIVED from execution.EvidenceKinds() — not a hand-copied list — so a new kind
// added to the registry without a matching append-only migration (or a kind
// dropped from the CHECK) makes the sets differ and fails here.
//
// This derived assertion lives in the execution context's OWN integration test,
// not in db/migrate_integration_test.go: db_test is platform facility and must not
// reverse-import a context domain (review M1). The former hand-copied 11-value
// requireCheckSet there is replaced by a pointer to this test.

import (
	"context"
	"regexp"
	"sort"
	"testing"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// checkConstraintValuePattern extracts the single-quoted literals of a CHECK
// constraint definition. PostgreSQL renders `kind IN ('a','b',...)` as
// `kind = ANY (ARRAY['a'::text, 'b'::text, ...])`; the quoted literals are exactly
// the allowed values (the column name and ::text casts carry no quotes).
var checkConstraintValuePattern = regexp.MustCompile(`'([^']*)'`)

// checkConstraintValues returns the sorted, deduplicated allowed values parsed
// from a pg_get_constraintdef CHECK definition.
func checkConstraintValues(def string) []string {
	seen := make(map[string]bool)
	var values []string
	for _, m := range checkConstraintValuePattern.FindAllStringSubmatch(def, -1) {
		v := m[1]
		if !seen[v] {
			seen[v] = true
			values = append(values, v)
		}
	}
	sort.Strings(values)
	return values
}

// TestEvidenceKindCheckDerivesFromDomain pins chk_evidence_kind EQUAL to the
// derived registry set. Mutation: appending a 12th kind to EvidenceKinds() leaves
// the DB CHECK at 11 values, so the equality fails (RED); restoring the registry
// returns GREEN. A hand-copied expected list could not catch a registry addition.
func TestEvidenceKindCheckDerivesFromDomain(t *testing.T) {
	_, pool := newTestRepository(t)
	ctx := context.Background()

	var def string
	if err := pool.QueryRow(ctx, `
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conname = 'chk_evidence_kind'
		  AND contype = 'c'
		  AND connamespace = current_schema()::regnamespace
	`).Scan(&def); err != nil {
		t.Fatalf("read chk_evidence_kind constraint: %v", err)
	}

	// The expected set is the domain registry itself (the single authority).
	kinds := execution.EvidenceKinds()
	if len(kinds) == 0 {
		t.Fatal("execution.EvidenceKinds() is empty; the derivation would go vacuous")
	}
	want := make([]string, 0, len(kinds))
	for _, k := range kinds {
		want = append(want, string(k))
	}
	sort.Strings(want)

	got := checkConstraintValues(def)
	if len(got) != len(want) {
		t.Fatalf("chk_evidence_kind allows %d values %v, but EvidenceKinds() declares %d %v -- the DB CHECK must equal the domain registry; a new kind needs an append-only migration",
			len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("chk_evidence_kind value set %v != EvidenceKinds() %v -- the DB CHECK must equal the domain registry", got, want)
		}
	}
}

// TestCheckConstraintValuesExtraction is permanent deterministic proof of the
// value extractor (no DB needed): it parses the PostgreSQL ARRAY rendering of an
// IN list, deduplicates, and sorts. A broken extractor would make the derived
// equality above vacuous or wrong.
func TestCheckConstraintValuesExtraction(t *testing.T) {
	def := `CHECK ((kind = ANY (ARRAY['screenshot'::text, 'snapshot'::text, 'command'::text, 'screenshot'::text])))`
	got := checkConstraintValues(def)
	want := []string{"command", "screenshot", "snapshot"}
	if len(got) != len(want) {
		t.Fatalf("extracted %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("extracted %v, want %v", got, want)
		}
	}
}
