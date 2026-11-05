package versionapp_test

import (
	"errors"
	"strings"
	"testing"

	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

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
// version.binding.changed diff carries the fields the patch actually changed.
// environment_id is no longer stamped on every update, and a patched
// scope_exclude is recorded with its old/new pages/apis/databases summary.
func TestUpdateBindingAuditDiffReportsOnlyChangedFields(t *testing.T) {
	t.Run("environment change", func(t *testing.T) {
		env := newTestEnv(t)
		created := createVersion(t, env, "Orders Q4")
		resolverCalls := len(env.resolver.calls)
		env.audit.events = nil

		if _, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
			EnvironmentID: int64Ptr(1103),
		}, 42); err != nil {
			t.Fatalf("UpdateBinding: %v", err)
		}
		changed := bindingChangedAudit(t, env.audit)
		oldValue, newValue := changedRecord(t, changed, "environment_id")
		if oldValue != "1101" || newValue != "1103" {
			t.Fatalf("environment_id diff = %#v -> %#v", oldValue, newValue)
		}
		for _, unexpected := range []string{"scope_exclude", "repository_ids", "account_ids"} {
			if _, present := changed[unexpected]; present {
				t.Fatalf("unchanged field %q present in diff: %#v", unexpected, changed)
			}
		}
		if len(env.resolver.calls) != resolverCalls {
			t.Fatalf("environment change recomputed the code baseline: %d -> %d",
				resolverCalls, len(env.resolver.calls))
		}
	})

	t.Run("scope_exclude change", func(t *testing.T) {
		env := newTestEnv(t)
		created := createVersion(t, env, "Orders Q4")
		env.audit.events = nil

		if _, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
			ScopeExclude: &version.ScopeExclude{Pages: []int64{1301}, Databases: []int64{1501}},
		}, 42); err != nil {
			t.Fatalf("UpdateBinding: %v", err)
		}
		changed := bindingChangedAudit(t, env.audit)
		oldValue, newValue := changedRecord(t, changed, "scope_exclude")
		oldScope, ok := oldValue.(map[string]any)
		if !ok {
			t.Fatalf("old scope summary = %#v", oldValue)
		}
		newScope, ok := newValue.(map[string]any)
		if !ok {
			t.Fatalf("new scope summary = %#v", newValue)
		}
		if pages := oldScope["pages"].([]string); len(pages) != 0 {
			t.Fatalf("old pages = %#v", pages)
		}
		if pages := newScope["pages"].([]string); len(pages) != 1 || pages[0] != "1301" {
			t.Fatalf("new pages = %#v", pages)
		}
		if databases := newScope["databases"].([]string); len(databases) != 1 || databases[0] != "1501" {
			t.Fatalf("new databases = %#v", databases)
		}
		if _, present := changed["environment_id"]; present {
			t.Fatalf("unchanged environment present in diff: %#v", changed)
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
			t.Fatalf("account update recomputed the code baseline")
		}
	})

	t.Run("repository change", func(t *testing.T) {
		env := newTestEnv(t)
		created := createVersion(t, env, "Orders Q4")
		env.audit.events = nil

		if _, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
			RepositoryIDs: int64SlicePtr([]int64{1203}),
		}, 42); err != nil {
			t.Fatalf("UpdateBinding: %v", err)
		}
		changed := bindingChangedAudit(t, env.audit)
		oldValue, newValue := changedRecord(t, changed, "repository_ids")
		if got := oldValue.([]string); len(got) != 2 || got[0] != "1201" || got[1] != "1202" {
			t.Fatalf("old repositories = %#v", got)
		}
		if got := newValue.([]string); len(got) != 1 || got[0] != "1203" {
			t.Fatalf("new repositories = %#v", got)
		}
	})
}

// TestUpdateBindingEmptyPatchIsNoop pins the no-op contract: an empty patch (or
// a patch that repeats the current binding state) writes no rows, publishes no
// event, records no audit entry and never re-resolves the code baseline.
func TestUpdateBindingEmptyPatchIsNoop(t *testing.T) {
	cases := []struct {
		name  string
		patch versionapp.BindingPatch
	}{
		{"empty patch", versionapp.BindingPatch{}},
		{"same environment", versionapp.BindingPatch{EnvironmentID: int64Ptr(1101)}},
		{"same scope", versionapp.BindingPatch{ScopeExclude: &version.ScopeExclude{}}},
		{"same repositories", versionapp.BindingPatch{RepositoryIDs: int64SlicePtr([]int64{1201, 1202})}},
		{"same accounts", versionapp.BindingPatch{AccountIDs: int64SlicePtr([]int64{1601, 1602, 1604})}},
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
			versionBefore := env.repo.versions[created.ID]
			if !versionBefore.UpdatedAt.Equal(testTime) {
				t.Fatalf("no-op patch touched version.updated_at: %v", versionBefore.UpdatedAt)
			}
		})
	}
}

// TestUpdateBindingBaselineStability pins the repository baseline stability
// contract: the pinned commit is kept for account/scope/environment updates,
// recomputed only when the effective repository set changes, and never cleared
// by a resolver failure.
func TestUpdateBindingBaselineStability(t *testing.T) {
	t.Run("account and scope updates keep the pinned baseline", func(t *testing.T) {
		env := newTestEnv(t)
		created := createVersion(t, env, "Orders Q4")
		if len(env.resolver.calls) != 1 {
			t.Fatalf("create resolver calls = %d", len(env.resolver.calls))
		}

		overview, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
			AccountIDs:   int64SlicePtr([]int64{1602}),
			ScopeExclude: &version.ScopeExclude{Pages: []int64{1301}},
		}, 42)
		if err != nil {
			t.Fatalf("UpdateBinding: %v", err)
		}
		if overview.CodeBaselineCommit != codeBaselineCommit {
			t.Fatalf("code baseline = %q, want the pinned %q", overview.CodeBaselineCommit, codeBaselineCommit)
		}
		if len(env.resolver.calls) != 1 {
			t.Fatalf("account/scope update re-resolved the baseline: %#v", env.resolver.calls)
		}
		if stored := storedBinding(t, env, created.ID); stored.CodeBaselineCommit != codeBaselineCommit {
			t.Fatalf("stored baseline = %q", stored.CodeBaselineCommit)
		}
	})

	t.Run("repository change recomputes the baseline", func(t *testing.T) {
		env := newTestEnv(t)
		created := createVersion(t, env, "Orders Q4")
		env.resolver.commit = "fedcba9876543210fedcba9876543210fedcba98"

		overview, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
			RepositoryIDs: int64SlicePtr([]int64{1203}),
		}, 42)
		if err != nil {
			t.Fatalf("UpdateBinding: %v", err)
		}
		if overview.CodeBaselineCommit != env.resolver.commit {
			t.Fatalf("code baseline = %q, want the recomputed %q", overview.CodeBaselineCommit, env.resolver.commit)
		}
		if len(env.resolver.calls) != 2 || env.resolver.calls[1].ID != "1203" {
			t.Fatalf("resolver calls = %#v", env.resolver.calls)
		}
	})

	t.Run("resolver failure keeps the previously pinned commit", func(t *testing.T) {
		env := newTestEnv(t)
		created := createVersion(t, env, "Orders Q4")
		env.resolver.err = errors.New("git rev-parse failed")
		env.audit.events = nil

		overview, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
			RepositoryIDs: int64SlicePtr([]int64{1203}),
		}, 42)
		if err != nil {
			t.Fatalf("UpdateBinding must not fail on resolver errors: %v", err)
		}
		if overview.CodeBaselineCommit != codeBaselineCommit {
			t.Fatalf("code baseline = %q, want the pinned %q", overview.CodeBaselineCommit, codeBaselineCommit)
		}
		if stored := storedBinding(t, env, created.ID); stored.CodeBaselineCommit != codeBaselineCommit {
			t.Fatalf("stored baseline = %q, want the pinned commit", stored.CodeBaselineCommit)
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
	})
}

// TestBindingOverviewSnapshotReference pins R-C4-1: the binding read carries a
// snapshot reference. W0 has no frozen wave snapshot in the version context
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
	if !strings.HasPrefix(first.Snapshot.ScopeVersion, "scope-v1:") {
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

	if _, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
		ScopeExclude: &version.ScopeExclude{Pages: []int64{1301}},
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
		ScopeExclude: &version.ScopeExclude{},
	}, 42); err == nil {
		t.Fatal("completed version accepted a binding update")
	}
}

// TestBindingEnvironmentAccountsAreBoundSubset pins the account threshold in the
// nested environment projection: environment.accounts is the version's bound
// subset, not the global test-account pool.
func TestBindingEnvironmentAccountsAreBoundSubset(t *testing.T) {
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
	if len(overview.Environment.Accounts) != 1 || overview.Environment.Accounts[0].ID != "1602" {
		t.Fatalf("environment accounts = %#v, want the bound subset", overview.Environment.Accounts)
	}
	detail, err := env.svc.Get(requestContext(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(detail.Bindings[0].Environment.Accounts) != 1 ||
		detail.Bindings[0].Environment.Accounts[0].ID != "1602" {
		t.Fatalf("detail environment accounts = %#v", detail.Bindings[0].Environment.Accounts)
	}
}

// TestBindingExcludesInactiveTargetDatabases pins the list/count consistency for
// disabled target databases: they are not part of the version range, so they are
// absent from both the approved count and the effective database list.
func TestBindingExcludesInactiveTargetDatabases(t *testing.T) {
	env := newTestEnv(t)
	disabled := resourceapp.DatabaseView{
		ID: "1504", SUTID: "1001", EnvironmentID: "1101", Name: "orders-offline",
		DSN: "postgres://offline", Username: "reader", Status: "disabled",
		CreatedAt: testTime, UpdatedAt: testTime,
	}
	env.catalog.databases[1504] = disabled
	environment := env.catalog.environments[1101]
	environment.Databases = append(environment.Databases, disabled)
	env.catalog.environments[1101] = environment

	created := createVersion(t, env, "Orders Q4")
	binding := created.Bindings[0]
	for _, database := range binding.Databases {
		if database.Status != "active" {
			t.Fatalf("binding carries an inactive database: %#v", database)
		}
	}
	if len(binding.Databases) != 2 {
		t.Fatalf("binding databases = %#v, want the two active rows", binding.Databases)
	}

	overview, err := env.svc.GetBinding(requestContext(), created.ID)
	if err != nil {
		t.Fatalf("GetBinding: %v", err)
	}
	if overview.Counts.Databases.Approved != 2 || overview.Counts.Databases.Included != 2 {
		t.Fatalf("database counts = %#v", overview.Counts.Databases)
	}
	if len(overview.Databases) != 2 {
		t.Fatalf("overview databases = %#v", overview.Databases)
	}
}

// TestBindingReadsLowestIDVersionSUTRow documents the v0.22 single-writer
// invariant: one environment-derived version_sut row per version. A database
// that still has several rows (legacy) is read deterministically through the
// lowest ID, and the binding update rewrites only that row's binding.
func TestBindingReadsLowestIDVersionSUTRow(t *testing.T) {
	env := newTestEnv(t)
	created := createVersion(t, env, "Orders Q4")
	first := storedBinding(t, env, created.ID)
	second := first
	second.ID = first.ID + 1
	second.EnvironmentID = 1103
	env.repo.bindings[created.ID] = []version.Binding{first, second}

	overview, err := env.svc.GetBinding(requestContext(), created.ID)
	if err != nil {
		t.Fatalf("GetBinding: %v", err)
	}
	if overview.Environment.ID != "1101" {
		t.Fatalf("binding read picked environment %q, want the lowest-ID row", overview.Environment.ID)
	}
	if _, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
		ScopeExclude: &version.ScopeExclude{Pages: []int64{1301}},
	}, 42); err != nil {
		t.Fatalf("UpdateBinding: %v", err)
	}
	if env.repo.lastBinding.ID != first.ID {
		t.Fatalf("updated binding id = %d, want the lowest-ID row %d", env.repo.lastBinding.ID, first.ID)
	}
}
