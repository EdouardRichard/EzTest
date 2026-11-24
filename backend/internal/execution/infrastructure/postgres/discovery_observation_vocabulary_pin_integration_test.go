//go:build integration

package executionpg

// R-H10/T11e-4 schema pins for the discovery-observation KIND and DECISION
// vocabularies (DB-29).
//
// T11e-4 created execution.ObservationKind*/ObservationDecision* as the Go
// authority these two enums never had (the DB CHECK plus raw literals in the
// producers, the HTTP handler and the repository SQL were the only definitions;
// T11e-3 §12.1 routed the observation-kind half here). The DB constraint stays
// the FIRST truth: these pins hold the new constants against the live schema in
// BOTH directions, so a constant rename, a missing member or a CHECK change
// fails loud instead of silently diverging.
//
// Expectations come from the domain registries (the contract), never from
// observed output (方法纪律 ⑩). The CHECK-set comparison reuses this package's
// existing checkConstraintValues parser (evidence_kind_check_integration_test.go)
// — no second copy of the constraint-definition machinery.
//
// Discriminating: changing a constant's VALUE breaks both the set equality and
// the accept probe (23514); narrowing/widening a CHECK breaks the set equality
// and the named-reject probes. The reject probes assert the CONSTRAINT NAME so a
// rejection by some other constraint cannot masquerade as this CHECK.

import (
	"context"
	"errors"
	"sort"
	"testing"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestDiscoveryObservationVocabularyMatchesSchema(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()
	mustCreateWave(t, repo, 9301, 100)

	// 1. Both CHECK sets EQUAL the derived domain registries (catches a missing
	//    member as well as an extra one, in either direction).
	for _, c := range []struct {
		constraint string
		want       []string
	}{
		{"chk_execution_discovery_observation_kind", execution.ObservationKinds()},
		{"chk_execution_discovery_observation_decision", execution.ObservationDecisions()},
	} {
		if len(c.want) == 0 {
			t.Fatalf("%s: the domain registry is empty; the pin would go vacuous", c.constraint)
		}
		var def string
		if err := pool.QueryRow(ctx, `
			SELECT pg_get_constraintdef(oid) FROM pg_constraint
			WHERE conname = $1 AND contype = 'c'
			  AND connamespace = current_schema()::regnamespace
		`, c.constraint).Scan(&def); err != nil {
			t.Fatalf("read %s: %v", c.constraint, err)
		}
		want := append([]string(nil), c.want...)
		sort.Strings(want)
		got := checkConstraintValues(def)
		if len(got) != len(want) {
			t.Fatalf("%s allows %d values %v but the domain registry declares %d %v",
				c.constraint, len(got), got, len(want), want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s allows %v but the domain registry declares %v", c.constraint, got, want)
			}
		}
	}

	// 2. The parameterized upsert binds the domain DEFAULT: a freshly captured
	//    observation is unclassified, and the bound value must be the constant.
	observation := executionapp.DiscoveryObservation{
		Kind:             execution.ObservationKindAPI,
		Method:           "GET",
		Host:             "pin.example.com",
		Path:             "/api/orders/42",
		ResourceTemplate: "/api/orders/{id}",
		MIME:             "application/json",
		SUTID:            5301,
		WaveID:           9301,
		Occurrences:      1,
	}
	if err := repo.Observations.UpsertObservations(ctx, []executionapp.DiscoveryObservation{observation}); err != nil {
		t.Fatalf("upsert observation: %v", err)
	}
	var observationID int64
	var storedDecision, storedKind string
	if err := pool.QueryRow(ctx, `
		SELECT id, kind, decision FROM execution_discovery_observation
		WHERE resource_template = $1 AND host = $2
	`, observation.ResourceTemplate, observation.Host).Scan(&observationID, &storedKind, &storedDecision); err != nil {
		t.Fatalf("read back observation: %v", err)
	}
	if storedKind != execution.ObservationKindAPI {
		t.Fatalf("stored kind %q != execution.ObservationKindAPI %q", storedKind, execution.ObservationKindAPI)
	}
	if storedDecision != execution.ObservationDecisionNew {
		t.Fatalf("the upsert's bound decision %q != execution.ObservationDecisionNew %q (discovery_observation_repo.go binds it as $14)",
			storedDecision, execution.ObservationDecisionNew)
	}
	// The column DEFAULT itself must be the same constant (a row inserted without
	// the column, e.g. by a future writer, lands in the same state).
	var columnDefault string
	if err := pool.QueryRow(ctx, `
		SELECT pg_get_expr(adbin, adrelid) FROM pg_attrdef
		WHERE adrelid = 'execution_discovery_observation'::regclass
		  AND adnum = (SELECT attnum FROM pg_attribute
		               WHERE attrelid = 'execution_discovery_observation'::regclass AND attname = 'decision')
	`).Scan(&columnDefault); err != nil {
		t.Fatalf("read decision column default: %v", err)
	}
	if values := checkConstraintValues(columnDefault); len(values) != 1 || values[0] != execution.ObservationDecisionNew {
		t.Fatalf("decision column DEFAULT %q != execution.ObservationDecisionNew %q", columnDefault, execution.ObservationDecisionNew)
	}

	// 2b. Generic-plan probe (Ruling T11d-R4's born-green trap): the upsert's
	//     decision default is now a PARAMETER ($14) inside a statement whose
	//     ON CONFLICT infers uk_execution_discovery_observation. PostgreSQL
	//     switches a cached statement to a generic plan after five executions,
	//     which is where a parameterized index predicate would start failing
	//     42P10. Six executions of the same natural key must keep resolving the
	//     same row and accumulating occurrences.
	for i := 0; i < 6; i++ {
		if err := repo.Observations.UpsertObservations(ctx, []executionapp.DiscoveryObservation{observation}); err != nil {
			t.Fatalf("upsert execution %d after parameterizing the decision default: %v", i+2, err)
		}
	}
	var rows, occurrences int
	if err := pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(sum(occurrences), 0) FROM execution_discovery_observation
		WHERE resource_template = $1 AND host = $2
	`, observation.ResourceTemplate, observation.Host).Scan(&rows, &occurrences); err != nil {
		t.Fatalf("count observations after the generic-plan probe: %v", err)
	}
	if rows != 1 || occurrences != 7 {
		t.Fatalf("after 7 upserts of one natural key: rows=%d occurrences=%d, want 1/7 (the ON CONFLICT inference must survive parameterization past the generic-plan threshold)",
			rows, occurrences)
	}

	// 3. Every domain constant is ACCEPTED by the live CHECK.
	for _, kind := range execution.ObservationKinds() {
		if _, err := pool.Exec(ctx, `UPDATE execution_discovery_observation SET kind = $1 WHERE id = $2`, kind, observationID); err != nil {
			t.Fatalf("chk_execution_discovery_observation_kind must accept the domain kind %q: %v", kind, err)
		}
	}
	for _, decision := range execution.ObservationDecisions() {
		if _, err := pool.Exec(ctx, `UPDATE execution_discovery_observation SET decision = $1 WHERE id = $2`, decision, observationID); err != nil {
			t.Fatalf("chk_execution_discovery_observation_decision must accept the domain decision %q: %v", decision, err)
		}
	}

	// 4. Out-of-set values are rejected by the NAMED constraint. 'db' is the
	//    asset_registration kind that the narrower observation set excludes, so
	//    this probe also proves the two vocabularies are not interchangeable.
	for _, kind := range []string{execution.AssetKindDB, "not_a_kind"} {
		_, err := pool.Exec(ctx, `UPDATE execution_discovery_observation SET kind = $1 WHERE id = $2`, kind, observationID)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "chk_execution_discovery_observation_kind" {
			t.Fatalf("observation kind %q must be rejected by chk_execution_discovery_observation_kind (23514), got %v", kind, err)
		}
	}
	for _, decision := range []string{"settled", "not_a_decision"} {
		_, err := pool.Exec(ctx, `UPDATE execution_discovery_observation SET decision = $1 WHERE id = $2`, decision, observationID)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "chk_execution_discovery_observation_decision" {
			t.Fatalf("observation decision %q must be rejected by chk_execution_discovery_observation_decision (23514), got %v", decision, err)
		}
	}
}
