//go:build integration

package designpg_test

// R-H10/T11e-3 schema pins for the case version/confirmation TRIGGER vocabulary.
//
// T11e-3 parameterized the two SQL trigger defaults (confirm.go's 'manual',
// propose.go's 'normal') and added design.CaseTriggerManual so the confirmation
// default has a Go authority (the constant block previously carried only the
// three case_version triggers while chk_case_confirmation_trigger has always
// allowed 'manual'). These pins hold the domain constants against the LIVE
// contract — the CHECK sets and column defaults of design_0002/0003 — so a
// drift on either side fails loud. Expectations come from the domain constants
// (the contract), never from observed output (方法纪律 ⑩).
//
// Discriminating in both directions: renaming a constant's VALUE makes the
// accept-case fail with 23514 (the DB rejects the new value); widening/narrowing
// a CHECK makes the accept or the named-constraint reject case fail. The
// reject cases assert the CONSTRAINT NAME, so a rejection caused by some other
// constraint cannot masquerade as the trigger CHECK.

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/govocab"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// triggerConstraintValues reads a live CHECK definition and returns its sorted set
// of single-quoted literals, through the SHARED extraction authority delivery's
// schema pins use (platform/vocablint, T11e-5 Part E).
func triggerConstraintValues(t *testing.T, pool *pgxpool.Pool, table, constraint string) []string {
	t.Helper()
	var def string
	if err := pool.QueryRow(context.Background(), `
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conrelid = $1::regclass AND conname = $2
	`, table, constraint).Scan(&def); err != nil {
		t.Fatalf("load %s.%s definition: %v", table, constraint, err)
	}
	values := vocablint.SQLQuotedValues(def)
	if len(values) == 0 {
		t.Fatalf("%s.%s carries no quoted literals: %s", table, constraint, def)
	}
	return values
}

// caseTriggerConstants derives EVERY CaseTrigger* value from the domain source, so
// a newly declared trigger joins this pin's expectation without anyone editing a
// list (H-A21⑧: the hand-written member lists could not see a new constant).
func caseTriggerConstants(t *testing.T) []string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the trigger schema pin source")
	}
	dir := filepath.Dir(file)
	backend := ""
	for i := 0; i < 12; i++ {
		if strings.HasSuffix(filepath.ToSlash(dir), "/backend") {
			backend = dir
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if backend == "" {
		t.Fatal("backend module directory not found from " + file)
	}
	resolver, err := govocab.New(backend)
	if err != nil {
		t.Fatalf("trigger vocabulary resolver: %v", err)
	}
	decls, err := resolver.Decls(filepath.Join(backend, "internal", "design", "domain", "case.go"), false)
	if err != nil {
		t.Fatalf("derive design trigger constants: %v", err)
	}
	var out []string
	for _, decl := range decls {
		if !strings.HasPrefix(decl.Name, "CaseTrigger") || !decl.IsString {
			continue
		}
		out = append(out, decl.Values...)
	}
	if len(out) < 4 {
		t.Fatalf("only %d CaseTrigger* values derived; the completeness assertion would be vacuous", len(out))
	}
	return vocablint.SortedSet(out)
}

// TestTriggerVocabularySetsMatchSchema compares the two live CHECK sets as WHOLE
// SETS (delivery's precedent, vocabulary_schema_pin_integration_test.go) instead of
// only accepting/rejecting a hand-written member list, and pins their UNION against
// every CaseTrigger* constant derived from the domain source. A new trigger
// constant that no CHECK accepts, or a CHECK widened with a value Go does not
// define, fails here (H-A21⑧).
func TestTriggerVocabularySetsMatchSchema(t *testing.T) {
	_, pool := newRepo(t)
	versionSet := triggerConstraintValues(t, pool, "case_version", "chk_case_version_trigger")
	confirmationSet := triggerConstraintValues(t, pool, "case_confirmation", "chk_case_confirmation_trigger")

	wantVersion := vocablint.SortedSet([]string{
		design.CaseTriggerNormal, design.CaseTriggerDiscoveryDelta, design.CaseTriggerRollback,
	})
	wantConfirmation := vocablint.SortedSet([]string{
		design.CaseTriggerManual, design.CaseTriggerDiscoveryDelta, design.CaseTriggerRollback,
	})
	if strings.Join(versionSet, ",") != strings.Join(wantVersion, ",") {
		t.Fatalf("chk_case_version_trigger %v != the version-side domain triggers %v", versionSet, wantVersion)
	}
	if strings.Join(confirmationSet, ",") != strings.Join(wantConfirmation, ",") {
		t.Fatalf("chk_case_confirmation_trigger %v != the confirmation-side domain triggers %v", confirmationSet, wantConfirmation)
	}

	var union []string
	for _, value := range append(append([]string(nil), versionSet...), confirmationSet...) {
		if !slices.Contains(union, value) {
			union = append(union, value)
		}
	}
	union = vocablint.SortedSet(union)
	all := caseTriggerConstants(t)
	if strings.Join(union, ",") != strings.Join(all, ",") {
		t.Fatalf("the union of the two live trigger CHECKs %v != every design.CaseTrigger* constant %v "+
			"(a new trigger must join a CHECK in the same change set)", union, all)
	}
}

func TestTriggerVocabularyMatchesSchema(t *testing.T) {
	_, pool := newRepo(t)
	seedVersion(t, pool, testVersionID, true)
	seedRequirement(t, pool, testVersionID, testReqID, testReqVersion, "clarified", "trigger pin", 1)
	seedScenario(t, pool, testVersionID, testReqID, testReqVersion, testScenarioID, "trigger", 1)
	seedCase(t, pool, testVersionID, testReqID, testReqVersion, testScenarioID, testCaseID, testCaseVersion, 1, "draft", "trigger pin")
	ctx := context.Background()

	// The seeded case_version omitted the trigger column: its stored value is
	// the DB DEFAULT and must equal the constant the propose path binds.
	var versionDefault string
	if err := pool.QueryRow(ctx, `SELECT trigger FROM case_version WHERE id = $1`, testCaseVersion).Scan(&versionDefault); err != nil {
		t.Fatalf("read case_version trigger default: %v", err)
	}
	if versionDefault != design.CaseTriggerNormal {
		t.Fatalf("case_version trigger DEFAULT %q != design.CaseTriggerNormal %q (propose.go binds the constant as the COALESCE default)",
			versionDefault, design.CaseTriggerNormal)
	}

	// chk_case_version_trigger accepts exactly the version-side domain triggers.
	for _, trigger := range []string{design.CaseTriggerNormal, design.CaseTriggerDiscoveryDelta, design.CaseTriggerRollback} {
		if _, err := pool.Exec(ctx, `UPDATE case_version SET trigger = $1 WHERE id = $2`, trigger, testCaseVersion); err != nil {
			t.Fatalf("chk_case_version_trigger must accept the domain trigger %q: %v", trigger, err)
		}
	}
	// The confirmation-only trigger and a bogus value are rejected by the NAMED check.
	for _, trigger := range []string{design.CaseTriggerManual, "not_a_trigger"} {
		_, err := pool.Exec(ctx, `UPDATE case_version SET trigger = $1 WHERE id = $2`, trigger, testCaseVersion)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "chk_case_version_trigger" {
			t.Fatalf("case_version trigger %q must be rejected by chk_case_version_trigger (23514), got %v", trigger, err)
		}
	}

	// case_confirmation has UNIQUE(case_version_id), so every probe runs in its
	// own rolled-back transaction against the same seeded version.
	tryConfirmation := func(trigger string, withTrigger bool) (string, error) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin confirmation probe: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		stored := ""
		if withTrigger {
			_, err = tx.Exec(ctx, `
				INSERT INTO case_confirmation (id, case_version_id, scope, target_scope_id, batch_id, confirmed_by, trigger)
				VALUES ($1, $2, 'case', $2, 'trigger-pin', 1, $3)
			`, testCaseVersion+300001, testCaseVersion, trigger)
		} else {
			_, err = tx.Exec(ctx, `
				INSERT INTO case_confirmation (id, case_version_id, scope, target_scope_id, batch_id, confirmed_by)
				VALUES ($1, $2, 'case', $2, 'trigger-pin', 1)
			`, testCaseVersion+300001, testCaseVersion)
			if err == nil {
				err = tx.QueryRow(ctx, `SELECT trigger FROM case_confirmation WHERE id = $1`, testCaseVersion+300001).Scan(&stored)
			}
		}
		return stored, err
	}

	// chk_case_confirmation_trigger accepts exactly the confirmation-side set.
	for _, trigger := range []string{design.CaseTriggerManual, design.CaseTriggerDiscoveryDelta, design.CaseTriggerRollback} {
		if _, err := tryConfirmation(trigger, true); err != nil {
			t.Fatalf("chk_case_confirmation_trigger must accept the domain trigger %q: %v", trigger, err)
		}
	}
	// The version-only trigger and a bogus value are rejected by the NAMED check.
	for _, trigger := range []string{design.CaseTriggerNormal, "not_a_trigger"} {
		_, err := tryConfirmation(trigger, true)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "chk_case_confirmation_trigger" {
			t.Fatalf("case_confirmation trigger %q must be rejected by chk_case_confirmation_trigger (23514), got %v", trigger, err)
		}
	}
	// The DB DEFAULT equals the constant confirm.go binds as its COALESCE default.
	storedDefault, err := tryConfirmation("", false)
	if err != nil {
		t.Fatalf("insert confirmation without trigger: %v", err)
	}
	if storedDefault != design.CaseTriggerManual {
		t.Fatalf("case_confirmation trigger DEFAULT %q != design.CaseTriggerManual %q (confirm.go binds the constant as the COALESCE default)",
			storedDefault, design.CaseTriggerManual)
	}
}
