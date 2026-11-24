//go:build integration

package versionpg_test

// H-B1 / T3 carried minor ①: the resource delete-guard predicates in this package
// (SUTInUse / PageInUse / APIInUse / DatabaseInUse / RepositoryInUse and
// ReferencingVersions) mirror resourceapp.SUTView.IsApproved as SQL —
// `(s.status = $n OR s.status = '')`, eleven occurrences. T3 parameterized the
// status VALUE from the resource domain constant, but a value pin cannot catch a
// RULE drift: change the predicate shape (drop the empty-status branch, invert it,
// gate on the wrong column) and every value stays correct while the behaviour
// diverges from the authority.
//
// This pin closes that gap the only way that actually tests the rule: for EVERY
// SUT status the resource domain declares (derived from the source, so a new
// status joins automatically), it seeds a real version binding and asserts the
// SQL guards' behaviour equals resourceapp.SUTView.IsApproved for that status —
// per status, per guard, against PostgreSQL. Same shape as
// TestWaveActiveSetMatchesDomain, but exercised through the database rather than
// through a Go set, because the mirror under test IS SQL.
//
// The empty-status branch is provably unreachable from the database
// (chk_sut_status, resource_0005); the last sub-test pins that claim instead of
// pretending to cover it.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/govocab"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	resource "github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure"
	"github.com/jackc/pgx/v5/pgxpool"
)

// deriveSUTStatusValues derives every SUT registration status from the resource
// domain source (SUTStatus* declarations), so a NEW status constant is covered
// without editing a list here.
func deriveSUTStatusValues(t *testing.T) []string {
	t.Helper()
	resolver, err := govocab.New("../../..")
	if err != nil {
		t.Fatalf("vocabulary resolver: %v", err)
	}
	decls, err := resolver.Decls("../../resource/domain/sut.go", false)
	if err != nil {
		t.Fatalf("derive SUT statuses: %v", err)
	}
	values, err := collectSUTStatusValues(decls)
	if err != nil {
		t.Fatalf("derive SUT statuses: %v", err)
	}
	if len(values) < 3 {
		t.Fatalf("derived SUT status set %v is suspiciously small; the derivation is broken and the pin would go vacuous", values)
	}
	return values
}

// collectSUTStatusValues filters the SUTStatus* declarations to their string
// values. A declaration the resolver could not fully resolve (Unresolved — an
// alias, a cross-package reference, a string-shaped index/conversion, or a
// partially resolvable set) is a LOUD error, never a silent skip: silently
// dropping it would blind the pin to a status defined by reference (H-B1c, the
// govocab I1 shape — IsString alone is not "fully covered"). Non-string
// declarations (no IsString and no Unresolved) are not statuses and are skipped.
func collectSUTStatusValues(decls []govocab.Decl) ([]string, error) {
	var values []string
	for _, decl := range decls {
		if !strings.HasPrefix(decl.Name, "SUTStatus") {
			continue
		}
		if decl.Unresolved {
			return nil, fmt.Errorf("SUT status declaration %s has an unresolved string-shaped value the pin cannot derive; define it as a string literal or classify it explicitly (govocab I1 shape)", decl.Name)
		}
		if !decl.IsString {
			continue
		}
		values = append(values, decl.Values...)
	}
	return values, nil
}

// TestCollectSUTStatusValuesUnresolvedIsLoud is permanent deterministic proof of
// the H-B1c hardening: a partially resolvable / alias SUTStatus declaration fails
// loud instead of being silently dropped from the pin's status set.
func TestCollectSUTStatusValuesUnresolvedIsLoud(t *testing.T) {
	clean := []govocab.Decl{
		{Name: "SUTStatusProposed", IsString: true, Values: []string{"proposed"}},
		{Name: "SUTStatusApproved", IsString: true, Values: []string{"approved"}},
		{Name: "SUTStatusRejected", IsString: true, Values: []string{"rejected"}},
	}
	values, err := collectSUTStatusValues(clean)
	if err != nil || len(values) != 3 {
		t.Fatalf("clean declarations: values=%v err=%v, want 3 values", values, err)
	}
	// IsString=true AND Unresolved=true (a partially resolvable set): the resolved
	// members must NOT be accepted as full coverage.
	partial := []govocab.Decl{
		{Name: "SUTStatusProposed", IsString: true, Values: []string{"proposed"}},
		{Name: "SUTStatusAlias", IsString: true, Unresolved: true, Values: []string{"approved"}},
	}
	if _, err := collectSUTStatusValues(partial); err == nil {
		t.Fatal("partially resolved SUTStatus declaration was silently accepted; the pin would go blind to the unresolved member")
	}
	// A pure unresolved alias (no resolved values) also fails loud.
	alias := []govocab.Decl{{Name: "SUTStatusAlias", Unresolved: true}}
	if _, err := collectSUTStatusValues(alias); err == nil {
		t.Fatal("unresolved SUTStatus alias was silently skipped")
	}
}

// seedSUTGateFixture seeds one environment, one version bound to it and one SUT
// with the given registration status owning an approved page, an approved API, an
// active target database and an enabled association to an active repository.
// Every asset is IN SCOPE by its own status, so the only variable under test is
// the SUT's registration status.
func seedSUTGateFixture(t *testing.T, pool *pgxpool.Pool, base int64, status string) (versionID, sutID, pageID, apiID, databaseID, repositoryID int64) {
	t.Helper()
	ctx := context.Background()
	environmentID := base + 1
	versionID, sutID = base+2, base+3
	pageID, apiID, databaseID, repositoryID = base+4, base+5, base+6, base+7
	associationID := base + 8
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("seed SUT gate fixture (%s): %v", status, err)
		}
	}
	exec(`INSERT INTO environment (id, name, status) VALUES ($1, $2, $3)`, environmentID, "gate env "+status, resource.StatusActive)
	exec(`INSERT INTO version (id, title, created_by, environment_id) VALUES ($1, 'gate version', 1, $2)`, versionID, environmentID)
	exec(`INSERT INTO sut (id, environment_id, name, base_url, created_by, status)
		VALUES ($1, $2, 'gate sut', 'https://gate.example.test', 1, $3)`, sutID, environmentID, status)
	exec(`INSERT INTO sut_page (id, sut_id, name, url, route_template, status)
		VALUES ($1, $2, 'gate page', 'https://gate.example.test/p', '/p', $3)`, pageID, sutID, resource.PageStatusApproved)
	exec(`INSERT INTO sut_api (id, sut_id, method, path_template, name, status)
		VALUES ($1, $2, 'GET', '/api/gate', 'gate api', $3)`, apiID, sutID, resource.APIStatusApproved)
	exec(`INSERT INTO target_database (id, sut_id, name, dsn, username, read_only, status)
		VALUES ($1, $2, 'gate db', 'postgres://u:p@db/gate', 'u', true, $3)`, databaseID, sutID, resource.StatusActive)
	exec(`INSERT INTO code_repository (id, name, path, created_by, status)
		VALUES ($1, $2, $3, 1, $4)`, repositoryID, "gate repo "+status, "/repos/gate-"+status, resource.StatusActive)
	exec(`INSERT INTO sut_repository (id, sut_id, repo_id, role, enabled)
		VALUES ($1, $2, $3, 'backend', true)`, associationID, sutID, repositoryID)
	return versionID, sutID, pageID, apiID, databaseID, repositoryID
}

func TestVersionSUTGateMirrorsIsApproved(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := versionpg.New(pool, newIDs(t))
	ctx := context.Background()
	statuses := deriveSUTStatusValues(t)

	for index, status := range statuses {
		t.Run(status, func(t *testing.T) {
			base := int64(700000 + 100*index)
			versionID, sutID, pageID, apiID, databaseID, repositoryID := seedSUTGateFixture(t, pool, base, status)
			// The expectation comes from the AUTHORITY, not from the SQL's output.
			want := resourceapp.SUTView{Status: status}.IsApproved()

			guards := []struct {
				kind string
				id   int64
				call func(context.Context, int64) (bool, error)
			}{
				{"sut", sutID, repo.SUTInUse},
				{"page", pageID, repo.PageInUse},
				{"api", apiID, repo.APIInUse},
				{"database", databaseID, repo.DatabaseInUse},
				{"repository", repositoryID, repo.RepositoryInUse},
			}
			for _, guard := range guards {
				got, err := guard.call(ctx, guard.id)
				if err != nil {
					t.Fatalf("%s in-use: %v", guard.kind, err)
				}
				if got != want {
					t.Errorf("SUT status %q: %s in-use = %v, but resourceapp.SUTView.IsApproved = %v -- the SQL gate must mirror the authority for every status",
						status, guard.kind, got, want)
				}
				refs, err := repo.ReferencingVersions(ctx, guard.kind, guard.id)
				if err != nil {
					t.Fatalf("%s referencing versions: %v", guard.kind, err)
				}
				referenced := false
				for _, ref := range refs {
					if ref.ID == versionID {
						referenced = true
					}
				}
				if referenced != want {
					t.Errorf("SUT status %q: ReferencingVersions(%s) reports the version = %v, but IsApproved = %v -- the explanation must agree with the in-use predicate",
						status, guard.kind, referenced, want)
				}
			}
		})
	}

	// The empty-status branch of the SQL mirror is UNREACHABLE: the column is
	// NOT NULL DEFAULT 'approved' with chk_sut_status (resource_0005). Pin that
	// claim rather than asserting a behaviour the database cannot produce.
	t.Run("empty_status_is_db_unreachable", func(t *testing.T) {
		_, err := pool.Exec(ctx, `
			INSERT INTO sut (id, environment_id, name, base_url, created_by, status)
			VALUES (799001, 700001, 'empty status sut', 'https://empty.example.test', 1, '')`)
		if err == nil {
			t.Fatal("an empty sut.status was accepted; the mirror's empty-status branch is reachable and needs its own behaviour pin")
		}
		if !strings.Contains(err.Error(), "chk_sut_status") {
			t.Fatalf("empty sut.status was rejected by something other than chk_sut_status: %v", err)
		}
		if !(resourceapp.SUTView{Status: ""}).IsApproved() {
			t.Fatal("IsApproved no longer treats the legacy empty status as approved; the SQL mirror's empty branch must be revisited")
		}
	})
}
