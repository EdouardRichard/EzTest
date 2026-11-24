package versionapp_test

import (
	"errors"
	"strings"
	"testing"

	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// v0.2.4-B: a version binds an environment directly. The editable binding
// surface is therefore the environment plus the version account subset; the SUT
// catalog, the assets and the code pool are all derived, and the scope_exclude
// document and the version repository overrides no longer exist.

// bindingChangedAudit returns the "changed" map of the last
// version.binding.changed audit entry. It fails the test when no such entry was
// recorded.
func bindingChangedAudit(t *testing.T, audit *fakeAudit) map[string]any {
	t.Helper()
	for index := len(audit.events) - 1; index >= 0; index-- {
		if audit.events[index].Action != "version.binding.changed" {
			continue
		}
		changed, ok := audit.events[index].Metadata["changed"].(map[string]any)
		if !ok {
			t.Fatalf("changed payload = %#v", audit.events[index].Metadata)
		}
		return changed
	}
	t.Fatal("version.binding.changed audit entry missing")
	return nil
}

func noBindingChangedAudit(t *testing.T, audit *fakeAudit) {
	t.Helper()
	for _, event := range audit.events {
		if event.Action == "version.binding.changed" {
			t.Fatalf("unexpected version.binding.changed audit entry: %#v", event.Metadata)
		}
	}
}

func changedRecord(t *testing.T, changed map[string]any, field string) (any, any) {
	t.Helper()
	record, ok := changed[field].(map[string]any)
	if !ok {
		t.Fatalf("changed[%q] = %#v, want an old/new record", field, changed[field])
	}
	return record["old"], record["new"]
}

// TestUpdateBindingAuditDiffReportsOnlyChangedFields pins R-C4-3: the
// version.binding.changed diff carries only the fields the patch actually
// changed, so an account update never stamps the environment and vice versa.
func TestUpdateBindingAuditDiffReportsOnlyChangedFields(t *testing.T) {
	t.Run("environment change", func(t *testing.T) {
		env := newTestEnv(t)
		created := createVersion(t, env, "Orders Q4")
		env.audit.events = nil

		if _, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
			EnvironmentID: int64Ptr(1102),
		}, 42); err != nil {
			t.Fatalf("UpdateBinding: %v", err)
		}
		changed := bindingChangedAudit(t, env.audit)
		oldValue, newValue := changedRecord(t, changed, "environment_id")
		if oldValue != "1101" || newValue != "1102" {
			t.Fatalf("environment_id diff = %#v -> %#v", oldValue, newValue)
		}
		for _, unexpected := range []string{"scope_exclude", "repository_ids", "account_ids"} {
			if _, present := changed[unexpected]; present {
				t.Fatalf("unchanged field %q present in diff: %#v", unexpected, changed)
			}
		}
	})

	t.Run("account change", func(t *testing.T) {
		env := newTestEnv(t)
		created := createVersion(t, env, "Orders Q4")
		resolverCalls := len(env.resolver.calls)
		env.audit.events = nil

		if _, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
			AccountIDs: int64SlicePtr([]int64{1602}),
		}, 42); err != nil {
			t.Fatalf("UpdateBinding: %v", err)
		}
		changed := bindingChangedAudit(t, env.audit)
		oldValue, newValue := changedRecord(t, changed, "account_ids")
		if got := oldValue.([]string); len(got) != 3 {
			t.Fatalf("old accounts = %#v, want the three active defaults", got)
		}
		if got := newValue.([]string); len(got) != 1 || got[0] != "1602" {
			t.Fatalf("new accounts = %#v", got)
		}
		if _, present := changed["environment_id"]; present {
			t.Fatalf("unchanged environment present in diff: %#v", changed)
		}
		if len(env.resolver.calls) != resolverCalls {
			t.Fatalf("account update re-resolved the code baseline")
		}
	})
}

// TestUpdateBindingEmptyPatchIsNoop pins the no-op contract: an empty patch (or a
// patch that repeats the current binding state) writes no rows, publishes no
// event, records no audit entry and never re-resolves the code baseline.
func TestUpdateBindingEmptyPatchIsNoop(t *testing.T) {
	cases := []struct {
		name  string
		patch versionapp.BindingPatch
	}{
		{"empty patch", versionapp.BindingPatch{}},
		{"same environment", versionapp.BindingPatch{EnvironmentID: int64Ptr(1101)}},
		{"same accounts", versionapp.BindingPatch{AccountIDs: int64SlicePtr([]int64{1601, 1602, 1604})}},
		{"same accounts reordered", versionapp.BindingPatch{AccountIDs: int64SlicePtr([]int64{1604, 1601, 1602})}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			created := createVersion(t, env, "Orders Q4")
			env.audit.events = nil
			env.publisher.events = nil
			resolverCalls := len(env.resolver.calls)
			updateCalls := env.repo.updateCalls

			overview, err := env.svc.UpdateBinding(requestContext(), created.ID, tc.patch, 42)
			if err != nil {
				t.Fatalf("UpdateBinding: %v", err)
			}
			if overview.Environment.ID != "1101" {
				t.Fatalf("overview environment = %#v", overview.Environment)
			}
			if env.repo.updateCalls != updateCalls {
				t.Fatalf("no-op patch wrote rows: updateCalls %d -> %d", updateCalls, env.repo.updateCalls)
			}
			if len(env.resolver.calls) != resolverCalls {
				t.Fatalf("no-op patch resolved the code baseline")
			}
			noBindingChangedAudit(t, env.audit)
			if len(env.publisher.events) != 0 {
				t.Fatalf("no-op patch published events: %#v", env.publisher.events)
			}
			stored := env.repo.versions[created.ID]
			if !stored.UpdatedAt.Equal(testTime) {
				t.Fatalf("no-op patch touched version.updated_at: %v", stored.UpdatedAt)
			}
		})
	}
}

// TestUpdateBindingBaselineStability pins the code-baseline stability contract
// after the move to sut_repository: an account update never re-resolves, an
// environment switch resolves only the new pool's unpinned baseline, and a
// resolver failure never clears an already pinned commit.
func TestUpdateBindingBaselineStability(t *testing.T) {
	t.Run("account updates keep the pinned baseline", func(t *testing.T) {
		env := newTestEnv(t)
		created := createVersion(t, env, "Orders Q4")
		if len(env.resolver.calls) != 1 {
			t.Fatalf("create resolver calls = %d", len(env.resolver.calls))
		}
		env.catalog.baselineWrites = nil

		overview, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
			AccountIDs: int64SlicePtr([]int64{1602}),
		}, 42)
		if err != nil {
			t.Fatalf("UpdateBinding: %v", err)
		}
		if overview.CodeBaselineCommit != codeBaselineCommit {
			t.Fatalf("code baseline = %q, want the pinned %q", overview.CodeBaselineCommit, codeBaselineCommit)
		}
		if len(env.resolver.calls) != 1 {
			t.Fatalf("account update re-resolved the baseline: %#v", env.resolver.calls)
		}
		if len(env.catalog.baselineWrites) != 0 {
			t.Fatalf("account update rewrote the baseline: %#v", env.catalog.baselineWrites)
		}
	})

	t.Run("an environment switch resolves the new pool baseline", func(t *testing.T) {
		env := newTestEnv(t)
		created := createVersion(t, env, "Orders Q4")
		// Give the staging SUT its own repository so the switch has a pool to pin.
		env.catalog.sutRepos[1003] = []resourceapp.SUTRepositoryView{
			{ID: "9", SUTID: "1003", Repository: env.catalog.repositories[1203], Role: "infra", Enabled: true},
		}
		env.resolver.commit = "fedcba9876543210fedcba9876543210fedcba98"
		env.resolver.calls = nil
		env.catalog.baselineWrites = nil

		overview, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
			EnvironmentID: int64Ptr(1102),
		}, 42)
		if err != nil {
			t.Fatalf("UpdateBinding: %v", err)
		}
		if overview.CodeBaselineCommit != env.resolver.commit {
			t.Fatalf("code baseline = %q, want the recomputed %q", overview.CodeBaselineCommit, env.resolver.commit)
		}
		if len(env.resolver.calls) != 1 || env.resolver.calls[0].ID != "1203" {
			t.Fatalf("resolver calls = %#v", env.resolver.calls)
		}
		if len(env.catalog.baselineWrites) != 1 ||
			env.catalog.baselineWrites[0] != "1003:1203="+env.resolver.commit {
			t.Fatalf("baseline writes = %#v", env.catalog.baselineWrites)
		}
		// The prod pool's pinned baseline is untouched by the switch.
		associations := env.catalog.sutRepos[1001]
		if associations[1].CodeBaselineCommit != codeBaselineCommit {
			t.Fatalf("the previous environment baseline was disturbed: %#v", associations[1])
		}
	})

	t.Run("resolver failure never clears a pinned baseline", func(t *testing.T) {
		env := newTestEnv(t)
		created := createVersion(t, env, "Orders Q4")
		env.catalog.sutRepos[1003] = []resourceapp.SUTRepositoryView{
			{ID: "9", SUTID: "1003", Repository: env.catalog.repositories[1203], Role: "infra", Enabled: true},
		}
		env.resolver.err = errors.New("git rev-parse failed")
		env.audit.events = nil
		env.catalog.baselineWrites = nil

		overview, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
			EnvironmentID: int64Ptr(1102),
		}, 42)
		if err != nil {
			t.Fatalf("UpdateBinding must not fail on resolver errors: %v", err)
		}
		if overview.CodeBaselineCommit != "" {
			t.Fatalf("code baseline = %q, want empty when unresolved", overview.CodeBaselineCommit)
		}
		if len(env.catalog.baselineWrites) != 0 {
			t.Fatalf("a failed resolution wrote a baseline: %#v", env.catalog.baselineWrites)
		}
		warnings := 0
		for _, event := range env.audit.events {
			if event.Action == "version.code_baseline_unresolved" {
				warnings++
			}
		}
		if warnings != 1 {
			t.Fatalf("baseline warnings = %d, want 1: %#v", warnings, env.audit.events)
		}
		// The prod baseline pinned at create time survives the failed switch.
		associations := env.catalog.sutRepos[1001]
		if associations[1].CodeBaselineCommit != codeBaselineCommit {
			t.Fatalf("a resolver failure cleared a pinned baseline: %#v", associations[1])
		}
	})

	// v0.2.4-B review round 1, Important 2: the baseline lives in the RESOURCE
	// context (sut_repository.code_baseline_commit), outside the optimistic-lock
	// protected version write, and a pinned commit is never overwritten. Resolving
	// it BEFORE repo.UpdateBinding therefore left a permanent, unaudited pin on an
	// environment the version never actually bound whenever the rebind lost the
	// race (409) or the write failed.
	t.Run("a conflicting rebind leaves the baseline untouched", func(t *testing.T) {
		env := newTestEnv(t)
		created := createVersion(t, env, "Orders Q4")
		env.catalog.sutRepos[1003] = []resourceapp.SUTRepositoryView{
			{ID: "9", SUTID: "1003", Repository: env.catalog.repositories[1203], Role: "infra", Enabled: true},
		}
		env.resolver.calls = nil
		env.catalog.baselineWrites = nil
		env.audit.events = nil
		env.repo.updateErr = version.ErrBindingConflict

		_, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
			EnvironmentID: int64Ptr(1102),
		}, 42)
		if err == nil {
			t.Fatal("UpdateBinding must surface the optimistic-lock conflict")
		}
		if apiErr := apiError(t, err); apiErr.Status != 409 {
			t.Fatalf("status = %d, want 409", apiErr.Status)
		}
		if len(env.catalog.baselineWrites) != 0 {
			t.Fatalf("a FAILED rebind pinned the new environment's baseline: %#v", env.catalog.baselineWrites)
		}
		if len(env.resolver.calls) != 0 {
			t.Fatalf("a FAILED rebind resolved the new pool's baseline: %#v", env.resolver.calls)
		}
		if got := env.repo.versions[created.ID].EnvironmentID; got != 1101 {
			t.Fatalf("environment_id = %d, want the unchanged 1101", got)
		}
		for _, event := range env.audit.events {
			if event.Action == "version.binding.changed" {
				t.Fatalf("a failed rebind recorded a binding change: %#v", event.Metadata)
			}
		}
	})
}

// TestBindingOverviewSnapshotReference pins R-C4-1: the binding read carries a
// snapshot reference. There is no frozen wave snapshot in the version context
// (R-C5 is execution-owned), so the reference is a deterministic digest of the
// current effective scope and frozen_wave_id is empty; it changes when the scope
// changes so a client can detect a stale frozen wave.
func TestBindingOverviewSnapshotReference(t *testing.T) {
	env := newTestEnv(t)
	created := createVersion(t, env, "Orders Q4")

	first, err := env.svc.GetBinding(requestContext(), created.ID)
	if err != nil {
		t.Fatalf("GetBinding: %v", err)
	}
	if !strings.HasPrefix(first.Snapshot.ScopeVersion, "scope-v2:") {
		t.Fatalf("snapshot scope version = %q", first.Snapshot.ScopeVersion)
	}
	if first.Snapshot.FrozenWaveID != nil {
		t.Fatalf("frozen wave id = %v, want nil before an execution wave freezes the scope", first.Snapshot.FrozenWaveID)
	}
	again, err := env.svc.GetBinding(requestContext(), created.ID)
	if err != nil {
		t.Fatalf("GetBinding again: %v", err)
	}
	if again.Snapshot.ScopeVersion != first.Snapshot.ScopeVersion {
		t.Fatalf("snapshot digest is not deterministic: %q != %q",
			again.Snapshot.ScopeVersion, first.Snapshot.ScopeVersion)
	}

	// Rebinding the environment moves the whole derived scope, so the digest
	// must move with it.
	if _, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
		EnvironmentID: int64Ptr(1102),
	}, 42); err != nil {
		t.Fatalf("UpdateBinding: %v", err)
	}
	changed, err := env.svc.GetBinding(requestContext(), created.ID)
	if err != nil {
		t.Fatalf("GetBinding after update: %v", err)
	}
	if changed.Snapshot.ScopeVersion == first.Snapshot.ScopeVersion {
		t.Fatal("snapshot digest did not change with the effective scope")
	}

	// Narrowing the account subset also moves the digest.
	if _, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
		AccountIDs: int64SlicePtr([]int64{1602}),
	}, 42); err != nil {
		t.Fatalf("UpdateBinding accounts: %v", err)
	}
	narrowed, err := env.svc.GetBinding(requestContext(), created.ID)
	if err != nil {
		t.Fatalf("GetBinding after account update: %v", err)
	}
	if narrowed.Snapshot.ScopeVersion == changed.Snapshot.ScopeVersion {
		t.Fatal("snapshot digest did not change with the account subset")
	}
}

// TestBindingCompletedVersionStaysReadOnly pins the completed/lifecycle guard:
// even when the stage projection lags behind the version status, a completed
// version reports stage=completed, editable=false and rejects binding writes.
func TestBindingCompletedVersionStaysReadOnly(t *testing.T) {
	env := newTestEnv(t)
	created := createVersion(t, env, "Orders Q4")
	// A stale stage row (designing) must not make a completed version editable.
	env.repo.stages[created.ID] = version.StageState{VersionID: created.ID, Stage: version.StageDesigning}
	stored := env.repo.versions[created.ID]
	stored.Status = version.StatusCompleted
	env.repo.versions[created.ID] = stored

	overview, err := env.svc.GetBinding(requestContext(), created.ID)
	if err != nil {
		t.Fatalf("GetBinding: %v", err)
	}
	if overview.Stage != version.StageCompleted || overview.Editable {
		t.Fatalf("completed binding overview = %s/%v", overview.Stage, overview.Editable)
	}
	if _, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
		EnvironmentID: int64Ptr(1102),
	}, 42); err == nil {
		t.Fatal("completed version accepted a binding update")
	}
}

// TestBindingAccountsAreTheBoundSubset pins the account threshold: the binding
// projection carries the version's own subset, never the global test-account
// pool, and the environment catalog is not polluted with it.
func TestBindingAccountsAreTheBoundSubset(t *testing.T) {
	env := newTestEnv(t)
	created := createVersion(t, env, "Orders Q4")

	if _, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
		AccountIDs: int64SlicePtr([]int64{1602}),
	}, 42); err != nil {
		t.Fatalf("UpdateBinding: %v", err)
	}
	overview, err := env.svc.GetBinding(requestContext(), created.ID)
	if err != nil {
		t.Fatalf("GetBinding: %v", err)
	}
	if len(overview.Accounts) != 1 || overview.Accounts[0].ID != "1602" {
		t.Fatalf("binding accounts = %#v, want the bound subset", overview.Accounts)
	}
	detail, err := env.svc.Get(requestContext(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(detail.Binding.Accounts) != 1 || detail.Binding.Accounts[0].ID != "1602" {
		t.Fatalf("detail accounts = %#v", detail.Binding.Accounts)
	}
	// The global pool still has four rows; only the disabled one is filtered out
	// of the default selection, so the bound subset is a real choice.
	pool, err := env.catalog.ListAllAccounts(requestContext())
	if err != nil {
		t.Fatalf("ListAllAccounts: %v", err)
	}
	if len(pool) != 4 {
		t.Fatalf("global pool = %#v", pool)
	}
}

// TestBindingExcludesInactiveAndProposedAssets pins the list/count consistency of
// the derived scope: disabled target databases and proposed pages/APIs are not
// part of the version range, so they are absent from the effective lists while
// the counters still report the proposed rows.
func TestBindingExcludesInactiveAndProposedAssets(t *testing.T) {
	env := newTestEnv(t)
	created := createVersion(t, env, "Orders Q4")

	for _, database := range created.Binding.Databases {
		if database.Status != "active" {
			t.Fatalf("binding carries an inactive database: %#v", database)
		}
	}
	if len(created.Binding.Databases) != 2 {
		t.Fatalf("binding databases = %#v, want the two active rows", created.Binding.Databases)
	}
	for _, page := range created.Binding.Pages {
		if page.Status != "approved" {
			t.Fatalf("binding carries a non-approved page: %#v", page)
		}
	}

	overview, err := env.svc.GetBinding(requestContext(), created.ID)
	if err != nil {
		t.Fatalf("GetBinding: %v", err)
	}
	if overview.Counts.Databases.Approved != 2 {
		t.Fatalf("database counts = %#v", overview.Counts.Databases)
	}
	if overview.Counts.Pages.Approved != 3 || overview.Counts.Pages.Proposed != 1 {
		t.Fatalf("page counts = %#v", overview.Counts.Pages)
	}
	if len(overview.Pages) != 3 || len(overview.Databases) != 2 {
		t.Fatalf("overview lists = pages %d databases %d", len(overview.Pages), len(overview.Databases))
	}
	// The nested catalog still exposes the proposed/disabled rows: they are
	// catalog state, just not version scope.
	var nestedProposed int
	for _, sut := range overview.Environment.SUTs {
		for _, page := range sut.Pages {
			if page.Status == "proposed" {
				nestedProposed++
			}
		}
	}
	if nestedProposed != 1 {
		t.Fatalf("nested proposed pages = %d, want 1", nestedProposed)
	}

	// v0.2.4-B review round 1 (Important 1): the PER-SUT effective projection the
	// adapters consume must agree with the flat lists — same rule, same rows — so
	// an adapter cannot widen the scope by reaching for the nested catalog instead.
	effective := overview.EffectiveSUTs()
	if len(effective) != len(overview.Environment.SUTs) {
		t.Fatalf("effective SUTs = %d, want one row per catalog SUT (%d)", len(effective), len(overview.Environment.SUTs))
	}
	var effectivePages, effectiveAPIs, effectiveDatabases int
	for _, sut := range effective {
		for _, page := range sut.Pages {
			if page.Status != "approved" {
				t.Fatalf("effective SUT %d carries a non-approved page: %#v", sut.SUTID, page)
			}
		}
		for _, api := range sut.APIs {
			if api.Status != "approved" {
				t.Fatalf("effective SUT %d carries a non-approved api: %#v", sut.SUTID, api)
			}
		}
		for _, database := range sut.Databases {
			if database.Status != "active" {
				t.Fatalf("effective SUT %d carries an inactive database: %#v", sut.SUTID, database)
			}
		}
		effectivePages += len(sut.Pages)
		effectiveAPIs += len(sut.APIs)
		effectiveDatabases += len(sut.Databases)
		if sut.SUTID <= 0 {
			t.Fatalf("effective SUT id = %d, want the parsed catalog id", sut.SUTID)
		}
	}
	if effectivePages != len(overview.Pages) ||
		effectiveAPIs != len(overview.APIs) ||
		effectiveDatabases != len(overview.Databases) {
		t.Fatalf("per-SUT effective union = %d pages / %d apis / %d databases, want the flat %d / %d / %d",
			effectivePages, effectiveAPIs, effectiveDatabases,
			len(overview.Pages), len(overview.APIs), len(overview.Databases))
	}
}
