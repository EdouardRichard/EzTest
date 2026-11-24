package deliverypg

// Value pins for the ONE delivery SQL shape that cannot bind its status values as
// query parameters (controller Ruling T11d-R4), plus the schema pins for the two
// vocabularies T11e-1 made reachable.
//
// insertAnalysisRunSQL's ON CONFLICT index predicate keeps its literals under a
// fragment exemption in the delivery vocabulary gate. An exemption alone would
// let a domain-value rename drift silently: the gate would still see the same
// exempted fragment while the SQL no longer matched the domain (or the index).
// These pins are the REQUIRED compensating control — they compare the literal
// text against the domain-derived set, so changing delivery.AnalysisStatusQueued
// or ...Running fails here even though the gate stays green.

import (
	"regexp"
	"strings"
	"testing"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"
)

// conflictPredicatePattern captures the status list of the ON CONFLICT index
// inference predicate. It is anchored on ON CONFLICT so an ordinary WHERE clause
// can never satisfy it.
var conflictPredicatePattern = regexp.MustCompile(`ON CONFLICT \([^)]*\) WHERE status IN \(([^)]*)\) DO NOTHING`)

// quotedValues / sortedSet delegate to the SHARED extraction authority
// (platform/vocablint, T11e-5 Part E): design's trigger-schema pin needs the same
// sorted-set comparison, and two copies of it would be a second truth.
func quotedValues(fragment string) []string { return vocablint.SQLQuotedValues(fragment) }

func sortedSet(values []string) []string { return vocablint.SortedSet(values) }

// TestAnalysisConflictPredicateMatchesDomain pins the non-parameterizable ON
// CONFLICT literals to the domain constants. It is the compensating control the
// R4 exemption requires: a domain-value change fails HERE.
func TestAnalysisConflictPredicateMatchesDomain(t *testing.T) {
	match := conflictPredicatePattern.FindStringSubmatch(insertAnalysisRunSQL)
	if match == nil {
		t.Fatalf("insertAnalysisRunSQL no longer carries an ON CONFLICT ... WHERE status IN (...) index predicate; "+
			"the R4 exemption and this pin must be re-adjudicated in the same change set\nSQL: %s", insertAnalysisRunSQL)
	}
	got := quotedValues(match[1])
	want := sortedSet(analysisActiveStatuses())
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ON CONFLICT index predicate statuses %v != domain-derived active statuses %v "+
			"(delivery.AnalysisStatusQueued/Running); the partial unique index and this predicate must agree",
			got, want)
	}
	// The predicate must stay LITERAL. Parameterizing it is the born-green trap:
	// PostgreSQL cannot prove partial-index implication under a generic plan and
	// fails with SQLSTATE 42P10 from the fifth execution (task-11d-report.md §6).
	if strings.Contains(match[1], "$") {
		t.Fatalf("the ON CONFLICT index predicate was parameterized (%q); PostgreSQL rejects it with "+
			"SQLSTATE 42P10 once pgx's cached statement uses a generic plan — see Ruling T11d-R4", match[1])
	}
	// The domain authority itself must still be the two non-terminal statuses.
	if len(analysisActiveStatuses()) != 2 {
		t.Fatalf("analysisActiveStatuses() = %v, want exactly the two non-terminal analysis statuses",
			analysisActiveStatuses())
	}
	for _, status := range analysisActiveStatuses() {
		if !delivery.AnalysisStatus(status).Valid() {
			t.Fatalf("active analysis status %q is not a valid domain status", status)
		}
	}
}

// TestAnalysisConflictPinIsDiscriminative proves the pin above is not vacuous: a
// predicate whose literals drift from the domain set must be rejected by the same
// comparison the pin performs.
func TestAnalysisConflictPinIsDiscriminative(t *testing.T) {
	drifted := `ON CONFLICT (version_id, kind, dedupe_key) WHERE status IN ('queued', 'in_flight') DO NOTHING`
	match := conflictPredicatePattern.FindStringSubmatch(drifted)
	if match == nil {
		t.Fatal("the pin's pattern did not match a drifted predicate; the pin would go vacuous")
	}
	if strings.Join(quotedValues(match[1]), ",") == strings.Join(sortedSet(analysisActiveStatuses()), ",") {
		t.Fatal("a drifted predicate compared equal to the domain set; the pin cannot detect drift")
	}
	parameterized := `ON CONFLICT (version_id, kind, dedupe_key) WHERE status IN ($12, $13) DO NOTHING`
	if match := conflictPredicatePattern.FindStringSubmatch(parameterized); match == nil ||
		!strings.Contains(match[1], "$") {
		t.Fatal("the pin does not detect a parameterized index predicate (the 42P10 born-green trap)")
	}
}
