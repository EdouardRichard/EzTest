//go:build integration

package agentpg_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	skillapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/skill"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	agentpg "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newSkillRepo(t *testing.T) (*agentpg.SkillRepository, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.NewDB(t)
	return agentpg.NewSkillRepository(pool, newIDs(t)), pool
}

func skillAdmin() platform.Actor { return platform.Actor{ID: 77, Role: "admin"} }

func seedSkillFixture(t *testing.T, dir, name, description, body string) {
	t.Helper()
	pkg := filepath.Join(dir, name)
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(pkg, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSkillCRUDAndVersionAppendOnly(t *testing.T) {
	repo, pool := newSkillRepo(t)
	svc := skillapp.NewService(repo, nil)
	ctx := context.Background()

	created, err := svc.Create(ctx, skillAdmin(), agent.Skill{
		Name: "api-recon", Description: "d", Body: "v1 body", Enabled: true,
	}, "initial")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Version != 1 || created.ID == 0 {
		t.Fatalf("created = %#v", created)
	}

	// Duplicate active name is a conflict.
	if _, err := svc.Create(ctx, skillAdmin(), agent.Skill{
		Name: "api-recon", Description: "d", Body: "other", Enabled: true,
	}, ""); err == nil {
		t.Fatal("duplicate create succeeded")
	} else {
		var apiErr *platform.Error
		if !errors.As(err, &apiErr) || apiErr.Status != 409 {
			t.Fatalf("duplicate create error = %v, want 409", err)
		}
	}

	// Update appends v2; v1 stays byte-identical (append-only).
	body := "v2 body"
	updated, changed, err := svc.Update(ctx, skillAdmin(), "api-recon", agent.SkillUpdate{Body: &body}, "edit")
	if err != nil || !changed || updated.Version != 2 {
		t.Fatalf("Update = %#v changed=%v err=%v", updated, changed, err)
	}
	versions, _, err := svc.Versions(ctx, "api-recon", 20, nil)
	if err != nil || len(versions) != 2 {
		t.Fatalf("Versions = %#v err=%v", versions, err)
	}
	if versions[0].Version != 2 || versions[1].Version != 1 {
		t.Fatalf("versions not DESC: %#v", versions)
	}
	if versions[1].Body != "v1 body" || versions[1].Note != "initial" {
		t.Fatalf("v1 mutated: %#v", versions[1])
	}
	if versions[0].ActorID == nil || *versions[0].ActorID != 77 {
		t.Fatalf("v2 actor = %v", versions[0].ActorID)
	}

	// Third update: v1 and v2 rows are still untouched.
	body3 := "v3 body"
	if _, _, err := svc.Update(ctx, skillAdmin(), "api-recon", agent.SkillUpdate{Body: &body3}, ""); err != nil {
		t.Fatalf("Update 3: %v", err)
	}
	versions, _, err = svc.Versions(ctx, "api-recon", 20, nil)
	if err != nil || len(versions) != 3 {
		t.Fatalf("Versions after v3 = %#v err=%v", versions, err)
	}
	if versions[2].Body != "v1 body" || versions[1].Body != "v2 body" {
		t.Fatalf("history mutated: %#v", versions)
	}

	// No-op update does not append.
	same := "v3 body"
	_, changed, err = svc.Update(ctx, skillAdmin(), "api-recon", agent.SkillUpdate{Body: &same}, "")
	if err != nil || changed {
		t.Fatalf("no-op update changed=%v err=%v", changed, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM skill_version WHERE skill_id = $1`, created.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("skill_version rows = %d, want 3 (no-op must not append)", count)
	}

	// expected_version CAS.
	stale := 1
	body4 := "v4 body"
	if _, _, err := svc.Update(ctx, skillAdmin(), "api-recon",
		agent.SkillUpdate{Body: &body4, ExpectedVersion: &stale}, ""); err == nil {
		t.Fatal("stale expected_version accepted")
	} else {
		var apiErr *platform.Error
		if !errors.As(err, &apiErr) || apiErr.Status != 409 {
			t.Fatalf("CAS error = %v, want 409", err)
		}
	}

	// Delete hides the skill but keeps the version history rows.
	if err := svc.Delete(ctx, skillAdmin(), "api-recon"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Get(ctx, "api-recon"); err == nil {
		t.Fatal("Get after delete succeeded")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM skill_version WHERE skill_id = $1`, created.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("delete removed version history: %d rows", count)
	}

	// Re-create after delete starts a fresh aggregate at v1.
	recreated, err := svc.Create(ctx, skillAdmin(), agent.Skill{
		Name: "api-recon", Description: "d2", Body: "reborn", Enabled: true,
	}, "")
	if err != nil {
		t.Fatalf("re-create after delete: %v", err)
	}
	if recreated.ID == created.ID || recreated.Version != 1 {
		t.Fatalf("recreated = %#v, want new id and version 1", recreated)
	}
}

func TestSkillSeedIdempotentAcrossRestart(t *testing.T) {
	repo, _ := newSkillRepo(t)
	ctx := context.Background()
	dir := t.TempDir()
	seedSkillFixture(t, dir, "playwright-cli", "CLI usage", "seed body v1")
	seedSkillFixture(t, dir, "api-recon", "API workflow", "recon seed body")

	first := skillapp.NewService(repo, nil)
	result, err := first.Seed(ctx, dir)
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if len(result.Seeded) != 2 || len(result.Skipped) != 0 {
		t.Fatalf("first seed = %#v", result)
	}

	// Admin edits one seeded skill at runtime.
	edited := "admin runtime edit"
	svc := first
	if _, _, err := svc.Update(ctx, skillAdmin(), "playwright-cli", agent.SkillUpdate{Body: &edited}, "tune"); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Simulated restart: a brand-new service over the same DB re-seeds.
	restarted := skillapp.NewService(repo, nil)
	result, err = restarted.Seed(ctx, dir)
	if err != nil {
		t.Fatalf("Seed after restart: %v", err)
	}
	if len(result.Seeded) != 0 || len(result.Skipped) != 2 {
		t.Fatalf("restart seed = %#v, want all skipped", result)
	}
	got, err := restarted.Get(ctx, "playwright-cli")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Body != edited || got.Version != 2 {
		t.Fatalf("restart overwrote the admin edit: %#v", got)
	}
	versions, _, err := restarted.Versions(ctx, "playwright-cli", 20, nil)
	if err != nil || len(versions) != 2 {
		t.Fatalf("versions = %#v err=%v", versions, err)
	}
	if versions[1].Source != agent.SkillSourceSeed || versions[0].Source != agent.SkillSourceAdmin {
		t.Fatalf("sources = %#v", versions)
	}
}

func TestSkillSeedDoesNotResurrectDeletedSkill(t *testing.T) {
	repo, _ := newSkillRepo(t)
	svc := skillapp.NewService(repo, nil)
	ctx := context.Background()
	dir := t.TempDir()
	seedSkillFixture(t, dir, "api-recon", "d", "seed body")
	if _, err := svc.Seed(ctx, dir); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if err := svc.Delete(ctx, skillAdmin(), "api-recon"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Restart re-seed: the admin's deletion is an edit too; it must survive.
	result, err := skillapp.NewService(repo, nil).Seed(ctx, dir)
	if err != nil {
		t.Fatalf("Seed after delete: %v", err)
	}
	if len(result.Seeded) != 0 || len(result.Skipped) != 1 {
		t.Fatalf("seed resurrected a deleted skill: %#v", result)
	}
	if _, err := svc.Get(ctx, "api-recon"); err == nil {
		t.Fatal("deleted skill is visible again")
	}
}

func TestSkillSeedFilesAreNotAuthoritativeAfterFirstSeed(t *testing.T) {
	repo, _ := newSkillRepo(t)
	svc := skillapp.NewService(repo, nil)
	ctx := context.Background()
	dir := t.TempDir()
	seedSkillFixture(t, dir, "api-recon", "d", "original file body")
	if _, err := svc.Seed(ctx, dir); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	// The seed file changes on disk; the DB stays authoritative.
	seedSkillFixture(t, dir, "api-recon", "d", "changed file body")
	if _, err := svc.Seed(ctx, dir); err != nil {
		t.Fatalf("Seed again: %v", err)
	}
	got, err := svc.Get(ctx, "api-recon")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Body != "original file body" {
		t.Fatalf("file change leaked into the DB: %q", got.Body)
	}
}

// TestSkillRuntimeEditNoRestart is the R-C11 acceptance: one long-lived
// service instance seeds, serves, accepts an admin edit and immediately
// serves the new content — no rebuild, no restart, no re-seed.
func TestSkillRuntimeEditNoRestart(t *testing.T) {
	repo, _ := newSkillRepo(t)
	svc := skillapp.NewService(repo, nil)
	ctx := context.Background()
	dir := t.TempDir()
	seedSkillFixture(t, dir, "api-recon", "d", "before edit")
	if _, err := svc.Seed(ctx, dir); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	before, err := svc.Get(ctx, "api-recon")
	if err != nil || before.Body != "before edit" {
		t.Fatalf("Get before = %#v err=%v", before, err)
	}
	after := "after edit — served without restart"
	if _, _, err := svc.Update(ctx, skillAdmin(), "api-recon", agent.SkillUpdate{Body: &after}, "runtime"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := svc.Get(ctx, "api-recon")
	if err != nil {
		t.Fatalf("Get after: %v", err)
	}
	if got.Body != after {
		t.Fatalf("runtime edit not visible: %q", got.Body)
	}
	// Enabled toggle is also a runtime edit.
	disabled := false
	if _, _, err := svc.Update(ctx, skillAdmin(), "api-recon", agent.SkillUpdate{Enabled: &disabled}, ""); err != nil {
		t.Fatalf("disable: %v", err)
	}
	got, err = svc.Get(ctx, "api-recon")
	if err != nil || got.Enabled {
		t.Fatalf("enabled toggle not visible: %#v err=%v", got, err)
	}
}

func TestSkillMetadataRoundTrip(t *testing.T) {
	repo, _ := newSkillRepo(t)
	svc := skillapp.NewService(repo, nil)
	ctx := context.Background()
	created, err := svc.Create(ctx, skillAdmin(), agent.Skill{
		Name: "meta-skill", Description: "d", Body: "b", Enabled: true,
		Metadata: []byte(`{"tags":["http","recon"],"weight":2}`),
	}, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := svc.Get(ctx, "meta-skill")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got.Metadata) == "" || string(got.Metadata) == "{}" {
		t.Fatalf("metadata lost: %s", got.Metadata)
	}
	// Semantically identical metadata (different key order) is a no-op.
	reordered := []byte(`{"weight":2,"tags":["http","recon"]}`)
	_, changed, err := svc.Update(ctx, skillAdmin(), "meta-skill", agent.SkillUpdate{Metadata: reordered}, "")
	if err != nil {
		t.Fatalf("metadata update: %v", err)
	}
	if changed {
		t.Fatal("reordered metadata reported as a change")
	}
	if created.Version != 1 {
		t.Fatalf("created version = %d", created.Version)
	}
}
