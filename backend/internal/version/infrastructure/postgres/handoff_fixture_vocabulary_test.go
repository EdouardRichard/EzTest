package versionpg_test

// H-B15 fixture vocabulary gate (T17 fail-closed hygiene): the handoff
// integration fixtures must seed their status/kind vocabulary through the domain
// constants, not hand-written literals. A literal-seeded fixture stays green
// when a constant is renamed or a value changes, while the production reads it
// seeds drift silently -- the same second-source-of-truth class the production
// guard (TestHandoffNoHardcodedDomainLiterals, handoff_repo_lint_test.go) closes
// for the handoff production files. The fixtures were outside that guard's scan
// set; this gate extends the SAME derived value set and the SAME fragment-scoped
// exemption machinery to them (single derivation authority, no second table).
//
// The guard reads the fixture SOURCE from disk, so it runs in the unit gate
// (no integration build tag needed) even though the fixtures themselves are
// integration-tagged.
//
// Mutation evidence: this gate was written BEFORE the H-B15 literal->constant
// fixture repayment and failed on the then-live seeded literals (environment
// 'active', page 'proposed', question 'done'/'pending', ...). Re-injecting any
// literal reproduces the failure.

import (
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"
)

// handoffFixtureGuardedFiles are the H-B15 fixture files this gate scans
// (relative to this package directory).
func handoffFixtureGuardedFiles() []string {
	return []string{
		"backend/internal/version/infrastructure/postgres/handoff_repo_integration_test.go",
		"backend/internal/version/infrastructure/postgres/handoff_repo_fix_round_integration_test.go",
	}
}

// handoffFixtureAllowlist adjudicates genuine collisions between a derived
// domain constant value and an unrelated fixture-local use of the same string.
// Entries are fragment-scoped with a reason (方法纪律 ⑥) and must be non-vacuous.
// After the H-B15 repayment every seeded vocabulary value references its domain
// constant; the remaining entries are cross-vocabulary collisions, not seeds:
//   - "admin" is a free-form test-account ROLE label (test_account.roles /
//     account_roles_hint), not resource.SUTSourceAdmin/PageSourceAdmin (a SUT/page
//     provenance vocabulary that happens to share the value).
//   - "question"/"requirement" are the handoff-local open-item Kind (the same
//     handoff-local vocabulary the production allowlist adjudicates in
//     handoffLiteralAllowlist), not the message/question kind or the requirement
//     confirmation scope.
func handoffFixtureAllowlist() map[string]vocablint.AllowEntry {
	return map[string]vocablint.AllowEntry{
		"admin": {
			Reason: "free-form test-account role label (roles JSON / account_roles_hint assertion), not the SUT/page source vocabulary",
			Fragments: vocablint.FileFragments("backend/internal/version/infrastructure/postgres/handoff_repo_integration_test.go",
				`'["admin"]'::jsonb`,
				`'["admin"]'::jsonb`,
				`clarified.AccountRolesHint[0] != "admin"`,
			),
		},
		"question": {
			Reason: "handoff-local open-item Kind assertion (mirrors the production handoffLiteralAllowlist adjudication), not the message/question kind vocabulary",
			Fragments: vocablint.JoinFragments(
				vocablint.FileFragments("backend/internal/version/infrastructure/postgres/handoff_repo_integration_test.go", `openKinds["question"]`),
				vocablint.FileFragments("backend/internal/version/infrastructure/postgres/handoff_repo_fix_round_integration_test.go", `item.Kind == "question"`),
			),
		},
		"requirement": {
			Reason:    "handoff-local open-item Kind assertion (mirrors the production handoffLiteralAllowlist adjudication), not the requirement confirmation scope / message ref kind",
			Fragments: vocablint.FileFragments("backend/internal/version/infrastructure/postgres/handoff_repo_integration_test.go", `openKinds["requirement"]`),
		},
		"P1": {
			Reason:    "requirement priority label seeded by the fixture; the version domain exposes only DefaultPriority (P2) as a named constant, the other valid priorities exist as validPriorities map keys (H-B1 var-set derivation)",
			Fragments: vocablint.FileFragments("backend/internal/version/infrastructure/postgres/handoff_repo_integration_test.go", `'P1', '订单中心退款流程'`),
		},
	}
}

func TestHandoffFixturesNoHardcodedDomainLiterals(t *testing.T) {
	values := deriveDomainConstantValues(t)
	// Fragment-level non-vacuity (H-B1 minor ③) and the file-bound fragment shape
	// (T11e-5 Part A1) are both checked by the shared surface scanner.
	scan := vocablint.ScanSurface(versionGateRepoRootT(t), handoffFixtureGuardedFiles(), values, handoffFixtureAllowlist())
	for _, problem := range scan.Problems {
		t.Errorf("%s", problem)
	}
	all := scan.Findings
	if len(all) > 0 {
		for _, f := range all {
			t.Errorf("%s:%d hardcodes %q (%s); seed it from the domain constant instead",
				f.File, f.Line, f.Value, f.Name)
		}
		t.Fatalf("%d hardcoded domain literal(s) found in the handoff fixtures", len(all))
	}
}
