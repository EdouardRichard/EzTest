package skill_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	skillapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/skill"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

/* --------------------------------- fakes ---------------------------------- */

// fakeRepo is an in-memory Repository. Methods record their calls; the
// admin-gate tests use panicRepo instead so a gate that leaks into the
// repository fails loudly (a zero-value stub would silently supply answers).
type fakeRepo struct {
	skills   map[string]agent.Skill
	versions map[string][]agent.SkillVersion

	created  []agent.Skill
	ensured  []agent.Skill
	updates  []agent.SkillUpdate
	deletes  []string
	listCall int

	createErr error
	updateErr error
	deleteErr error
	getErr    error

	// ensureCreated controls EnsureSkill's created flag for existing names.
	ensureCreated bool
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		skills:   map[string]agent.Skill{},
		versions: map[string][]agent.SkillVersion{},
	}
}

func (r *fakeRepo) ListSkills(context.Context) ([]agent.Skill, error) {
	r.listCall++
	out := make([]agent.Skill, 0, len(r.skills))
	for _, s := range r.skills {
		out = append(out, s)
	}
	return out, nil
}

func (r *fakeRepo) GetSkill(_ context.Context, name string) (agent.Skill, error) {
	if r.getErr != nil {
		return agent.Skill{}, r.getErr
	}
	s, ok := r.skills[name]
	if !ok {
		return agent.Skill{}, agent.ErrNotFound
	}
	return s, nil
}

func (r *fakeRepo) CreateSkill(_ context.Context, s agent.Skill, actorID *int64, note string) (agent.Skill, error) {
	if r.createErr != nil {
		return agent.Skill{}, r.createErr
	}
	s.ID = int64(len(r.created) + 1)
	s.Version = 1
	s.CreatedAt = time.Now().UTC()
	s.UpdatedAt = s.CreatedAt
	r.created = append(r.created, s)
	r.skills[s.Name] = s
	r.versions[s.Name] = []agent.SkillVersion{{
		SkillID: s.ID, Version: 1, Name: s.Name, Description: s.Description,
		Body: s.Body, Metadata: s.Metadata, Enabled: s.Enabled,
		Source: agent.SkillSourceAdmin, ActorID: actorID, Note: note,
	}}
	return s, nil
}

func (r *fakeRepo) EnsureSkill(_ context.Context, seed agent.Skill) (agent.Skill, bool, error) {
	r.ensured = append(r.ensured, seed)
	if existing, ok := r.skills[seed.Name]; ok {
		return existing, r.ensureCreated, nil
	}
	seed.ID = int64(len(r.skills) + 1)
	seed.Version = 1
	r.skills[seed.Name] = seed
	r.versions[seed.Name] = []agent.SkillVersion{{
		SkillID: seed.ID, Version: 1, Name: seed.Name, Description: seed.Description,
		Body: seed.Body, Metadata: seed.Metadata, Enabled: seed.Enabled,
		Source: agent.SkillSourceSeed,
	}}
	return seed, true, nil
}

func (r *fakeRepo) UpdateSkill(_ context.Context, name string, update agent.SkillUpdate, actorID *int64, note string) (agent.Skill, bool, error) {
	if r.updateErr != nil {
		return agent.Skill{}, false, r.updateErr
	}
	r.updates = append(r.updates, update)
	s, ok := r.skills[name]
	if !ok {
		return agent.Skill{}, false, agent.ErrNotFound
	}
	if update.Description != nil {
		s.Description = *update.Description
	}
	if update.Body != nil {
		s.Body = *update.Body
	}
	if update.Metadata != nil {
		s.Metadata = update.Metadata
	}
	if update.Enabled != nil {
		s.Enabled = *update.Enabled
	}
	s.Version++
	s.UpdatedAt = time.Now().UTC()
	r.skills[name] = s
	r.versions[name] = append(r.versions[name], agent.SkillVersion{
		SkillID: s.ID, Version: s.Version, Name: s.Name, Description: s.Description,
		Body: s.Body, Metadata: s.Metadata, Enabled: s.Enabled,
		Source: agent.SkillSourceAdmin, ActorID: actorID, Note: note,
	})
	return s, true, nil
}

func (r *fakeRepo) DeleteSkill(_ context.Context, name string) (agent.Skill, error) {
	if r.deleteErr != nil {
		return agent.Skill{}, r.deleteErr
	}
	r.deletes = append(r.deletes, name)
	s, ok := r.skills[name]
	if !ok {
		return agent.Skill{}, agent.ErrNotFound
	}
	delete(r.skills, name)
	return s, nil
}

func (r *fakeRepo) ListSkillVersions(_ context.Context, name string, limit int, beforeVersion *int) ([]agent.SkillVersion, bool, error) {
	all := r.versions[name]
	// newest first
	out := make([]agent.SkillVersion, 0, len(all))
	for i := len(all) - 1; i >= 0; i-- {
		v := all[i]
		if beforeVersion != nil && v.Version >= *beforeVersion {
			continue
		}
		out = append(out, v)
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}

// panicRepo fails loudly on any repository call: the admin gate must reject
// before the repository is ever consulted.
type panicRepo struct{}

func (panicRepo) ListSkills(context.Context) ([]agent.Skill, error) {
	panic("panicRepo.ListSkills called past the admin gate")
}
func (panicRepo) GetSkill(context.Context, string) (agent.Skill, error) {
	panic("panicRepo.GetSkill called past the admin gate")
}
func (panicRepo) CreateSkill(context.Context, agent.Skill, *int64, string) (agent.Skill, error) {
	panic("panicRepo.CreateSkill called past the admin gate")
}
func (panicRepo) EnsureSkill(context.Context, agent.Skill) (agent.Skill, bool, error) {
	panic("panicRepo.EnsureSkill called past the admin gate")
}
func (panicRepo) UpdateSkill(context.Context, string, agent.SkillUpdate, *int64, string) (agent.Skill, bool, error) {
	panic("panicRepo.UpdateSkill called past the admin gate")
}
func (panicRepo) DeleteSkill(context.Context, string) (agent.Skill, error) {
	panic("panicRepo.DeleteSkill called past the admin gate")
}
func (panicRepo) ListSkillVersions(context.Context, string, int, *int) ([]agent.SkillVersion, bool, error) {
	panic("panicRepo.ListSkillVersions called past the admin gate")
}

type recordingAudit struct{ events []auditapp.Event }

func (a *recordingAudit) Record(_ context.Context, event auditapp.Event) error {
	a.events = append(a.events, event)
	return nil
}

func adminActor() platform.Actor { return platform.Actor{ID: 7, Role: "admin"} }
func userActor() platform.Actor  { return platform.Actor{ID: 9, Role: "user"} }

func validSkill() agent.Skill {
	return agent.Skill{
		Name:        "api-recon",
		Description: "API reconnaissance workflow",
		Body:        "# API recon\n\nProbe the target API.",
		Metadata:    json.RawMessage(`{}`),
		Enabled:     true,
	}
}

func wantStatus(t *testing.T, err error, status int) {
	t.Helper()
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v (%T), want platform.Error with status %d", err, err, status)
	}
	if apiErr.Status != status {
		t.Fatalf("status = %d, want %d (err=%v)", apiErr.Status, status, err)
	}
}

/* ------------------------------ admin gate -------------------------------- */

func TestWritesRequireAdmin(t *testing.T) {
	svc := skillapp.NewService(panicRepo{}, nil)
	ctx := context.Background()
	body := "new body"
	for _, actor := range []platform.Actor{userActor(), {}} {
		if _, err := svc.Create(ctx, actor, validSkill(), ""); err == nil {
			t.Fatal("Create accepted a non-admin actor")
		} else {
			wantStatus(t, err, 403)
		}
		if _, _, err := svc.Update(ctx, actor, "api-recon", agent.SkillUpdate{Body: &body}, ""); err == nil {
			t.Fatal("Update accepted a non-admin actor")
		} else {
			wantStatus(t, err, 403)
		}
		if err := svc.Delete(ctx, actor, "api-recon"); err == nil {
			t.Fatal("Delete accepted a non-admin actor")
		} else {
			wantStatus(t, err, 403)
		}
	}
}

/* --------------------------------- CRUD ----------------------------------- */

func TestCreateValidatesAndAudits(t *testing.T) {
	repo := newFakeRepo()
	audit := &recordingAudit{}
	svc := skillapp.NewService(repo, audit)
	ctx := context.Background()

	stored, err := svc.Create(ctx, adminActor(), validSkill(), "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if stored.Version != 1 || stored.ID == 0 {
		t.Fatalf("stored = %#v, want id>0 version=1", stored)
	}
	if len(audit.events) != 1 || audit.events[0].Action != skillapp.ActionSkillCreated {
		t.Fatalf("audit events = %#v, want one %s", audit.events, skillapp.ActionSkillCreated)
	}
	meta := audit.events[0].Metadata
	if _, leaked := meta["body"]; leaked {
		t.Fatal("audit metadata must not carry the skill body")
	}
	if meta["name"] != "api-recon" {
		t.Fatalf("audit metadata name = %v", meta["name"])
	}
	if audit.events[0].ActorID == nil || *audit.events[0].ActorID != 7 {
		t.Fatalf("audit actor = %v", audit.events[0].ActorID)
	}

	// Invalid inputs are 400 before any write.
	bad := validSkill()
	bad.Name = "Bad Name"
	if _, err := svc.Create(ctx, adminActor(), bad, ""); err == nil {
		t.Fatal("Create accepted an invalid name")
	} else {
		wantStatus(t, err, 400)
	}
	bad = validSkill()
	bad.Body = "  "
	if _, err := svc.Create(ctx, adminActor(), bad, ""); err == nil {
		t.Fatal("Create accepted an empty body")
	} else {
		wantStatus(t, err, 400)
	}
	if len(repo.created) != 1 {
		t.Fatalf("repo.created = %d, want 1 (invalid inputs must not write)", len(repo.created))
	}
}

func TestCreateConflictMapsTo409(t *testing.T) {
	repo := newFakeRepo()
	repo.createErr = agent.ErrConflict
	svc := skillapp.NewService(repo, nil)
	_, err := svc.Create(context.Background(), adminActor(), validSkill(), "")
	wantStatus(t, err, 409)
}

func TestGetNotFoundMapsTo404(t *testing.T) {
	svc := skillapp.NewService(newFakeRepo(), nil)
	_, err := svc.Get(context.Background(), "missing-skill")
	wantStatus(t, err, 404)
}

func TestUpdateAppendsVersionAndAudits(t *testing.T) {
	repo := newFakeRepo()
	audit := &recordingAudit{}
	svc := skillapp.NewService(repo, audit)
	ctx := context.Background()
	if _, err := svc.Create(ctx, adminActor(), validSkill(), ""); err != nil {
		t.Fatalf("Create: %v", err)
	}

	newBody := "# API recon v2\n\nUpdated at runtime."
	stored, changed, err := svc.Update(ctx, adminActor(), "api-recon", agent.SkillUpdate{Body: &newBody}, "runtime edit")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !changed || stored.Version != 2 || stored.Body != newBody {
		t.Fatalf("stored = %#v changed=%v, want version 2 with the new body", stored, changed)
	}
	if len(audit.events) != 2 || audit.events[1].Action != skillapp.ActionSkillUpdated {
		t.Fatalf("audit events = %#v, want %s on update", audit.events, skillapp.ActionSkillUpdated)
	}
	fields, ok := audit.events[1].Metadata["fields"].([]string)
	if !ok || len(fields) != 1 || fields[0] != "body" {
		t.Fatalf("audit fields = %#v, want [body]", audit.events[1].Metadata["fields"])
	}

	// R-C11: the same service instance observes the edit immediately.
	got, err := svc.Get(ctx, "api-recon")
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got.Body != newBody {
		t.Fatalf("Get body = %q, want the runtime edit %q", got.Body, newBody)
	}
}

func TestUpdateNoOpDoesNotAppend(t *testing.T) {
	repo := newFakeRepo()
	audit := &recordingAudit{}
	svc := skillapp.NewService(repo, audit)
	ctx := context.Background()
	if _, err := svc.Create(ctx, adminActor(), validSkill(), ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	view, changed, err := svc.Update(ctx, adminActor(), "api-recon", agent.SkillUpdate{}, "")
	if err != nil {
		t.Fatalf("empty Update: %v", err)
	}
	if changed {
		t.Fatal("empty update reported changed")
	}
	if view.Version != 1 || len(repo.updates) != 0 {
		t.Fatalf("empty update touched the repo: view=%#v updates=%d", view, len(repo.updates))
	}
	if len(audit.events) != 1 {
		t.Fatalf("empty update audited: %#v", audit.events)
	}
}

func TestUpdateVersionConflictMapsTo409(t *testing.T) {
	repo := newFakeRepo()
	repo.updateErr = agent.ErrSkillVersionConflict
	svc := skillapp.NewService(repo, nil)
	body := "x"
	version := 1
	_, _, err := svc.Update(context.Background(), adminActor(), "api-recon",
		agent.SkillUpdate{Body: &body, ExpectedVersion: &version}, "")
	wantStatus(t, err, 409)
}

func TestUpdateRejectsInvalidFields(t *testing.T) {
	repo := newFakeRepo()
	svc := skillapp.NewService(repo, nil)
	ctx := context.Background()
	if _, err := svc.Create(ctx, adminActor(), validSkill(), ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	empty := ""
	if _, _, err := svc.Update(ctx, adminActor(), "api-recon", agent.SkillUpdate{Body: &empty}, ""); err == nil {
		t.Fatal("Update accepted an empty body")
	} else {
		wantStatus(t, err, 400)
	}
	zero := 0
	body := "ok"
	if _, _, err := svc.Update(ctx, adminActor(), "api-recon",
		agent.SkillUpdate{Body: &body, ExpectedVersion: &zero}, ""); err == nil {
		t.Fatal("Update accepted expected_version=0")
	} else {
		wantStatus(t, err, 400)
	}
	if len(repo.updates) != 0 {
		t.Fatalf("invalid updates reached the repo: %d", len(repo.updates))
	}
}

func TestDeleteAuditsAndMapsNotFound(t *testing.T) {
	repo := newFakeRepo()
	audit := &recordingAudit{}
	svc := skillapp.NewService(repo, audit)
	ctx := context.Background()
	if _, err := svc.Create(ctx, adminActor(), validSkill(), ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.Delete(ctx, adminActor(), "api-recon"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(audit.events) != 2 || audit.events[1].Action != skillapp.ActionSkillDeleted {
		t.Fatalf("audit events = %#v, want %s", audit.events, skillapp.ActionSkillDeleted)
	}
	if err := svc.Delete(ctx, adminActor(), "api-recon"); err == nil {
		t.Fatal("Delete of a missing skill succeeded")
	} else {
		wantStatus(t, err, 404)
	}
}

func TestVersionsNormalizesPagination(t *testing.T) {
	repo := newFakeRepo()
	svc := skillapp.NewService(repo, nil)
	ctx := context.Background()
	if _, err := svc.Create(ctx, adminActor(), validSkill(), ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	body := "v2"
	if _, _, err := svc.Update(ctx, adminActor(), "api-recon", agent.SkillUpdate{Body: &body}, ""); err != nil {
		t.Fatalf("Update: %v", err)
	}
	versions, _, err := svc.Versions(ctx, "api-recon", 0, nil)
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(versions) != 2 || versions[0].Version != 2 || versions[1].Version != 1 {
		t.Fatalf("versions = %#v, want DESC [2,1]", versions)
	}
	if versions[1].Source != agent.SkillSourceAdmin || versions[0].Source != agent.SkillSourceAdmin {
		t.Fatalf("admin writes must record source=admin: %#v", versions)
	}
	if _, _, err := svc.Versions(ctx, "missing-skill", 20, nil); err == nil {
		t.Fatal("Versions of a missing skill succeeded")
	} else {
		wantStatus(t, err, 404)
	}
	before := 0
	if _, _, err := svc.Versions(ctx, "api-recon", 20, &before); err == nil {
		t.Fatal("before_version=0 accepted")
	} else {
		wantStatus(t, err, 400)
	}
}

func TestNilRepositoryIsUnavailable(t *testing.T) {
	svc := skillapp.NewService(nil, nil)
	ctx := context.Background()
	if _, err := svc.List(ctx); err == nil {
		t.Fatal("List with nil repo succeeded")
	} else {
		wantStatus(t, err, 503)
	}
	if _, err := svc.Create(ctx, adminActor(), validSkill(), ""); err == nil {
		t.Fatal("Create with nil repo succeeded")
	} else {
		wantStatus(t, err, 503)
	}
}

/* --------------------------------- seeding -------------------------------- */

func TestSeedSkipsExistingNames(t *testing.T) {
	repo := newFakeRepo()
	svc := skillapp.NewService(repo, nil)
	ctx := context.Background()
	dir := t.TempDir()
	writeSkillFile(t, dir, "api-recon", "---\nname: api-recon\ndescription: seed description\n---\nseed body\n")

	first, err := svc.Seed(ctx, dir)
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if len(first.Seeded) != 1 || first.Seeded[0] != "api-recon" || len(first.Skipped) != 0 {
		t.Fatalf("first seed result = %#v", first)
	}

	// Admin edits the seeded skill, then a "restart" re-seeds from the same
	// files: the edit must survive (EnsureProfiles precedent).
	edited := "admin edited body"
	if _, _, err := svc.Update(ctx, adminActor(), "api-recon", agent.SkillUpdate{Body: &edited}, ""); err != nil {
		t.Fatalf("Update: %v", err)
	}
	second, err := svc.Seed(ctx, dir)
	if err != nil {
		t.Fatalf("Seed again: %v", err)
	}
	if len(second.Seeded) != 0 || len(second.Skipped) != 1 {
		t.Fatalf("second seed result = %#v, want skip", second)
	}
	got, err := svc.Get(ctx, "api-recon")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Body != edited {
		t.Fatalf("re-seed overwrote the admin edit: body=%q", got.Body)
	}
	if got.Version != 2 {
		t.Fatalf("re-seed bumped the version: %d", got.Version)
	}
	if len(repo.versions["api-recon"]) != 2 {
		t.Fatalf("re-seed appended a version: %d", len(repo.versions["api-recon"]))
	}
	// The seed source is recorded once, as source=seed.
	if repo.versions["api-recon"][0].Source != agent.SkillSourceSeed {
		t.Fatalf("first version source = %q, want seed", repo.versions["api-recon"][0].Source)
	}
}

func TestSeedMissingDirectoryFails(t *testing.T) {
	svc := skillapp.NewService(newFakeRepo(), nil)
	if _, err := svc.Seed(context.Background(), t.TempDir()+"/does-not-exist"); err == nil {
		t.Fatal("Seed of a missing directory succeeded silently")
	}
}

func TestSeedValidatesEveryFileBeforeWriting(t *testing.T) {
	repo := newFakeRepo()
	svc := skillapp.NewService(repo, nil)
	dir := t.TempDir()
	writeSkillFile(t, dir, "good-skill", "---\nname: good-skill\ndescription: ok\n---\nbody\n")
	writeSkillFile(t, dir, "bad-skill", "---\nname: mismatch\ndescription: ok\n---\nbody\n")
	if _, err := svc.Seed(context.Background(), dir); err == nil {
		t.Fatal("Seed accepted a file whose frontmatter name mismatches its directory")
	}
	if len(repo.ensured) != 0 {
		t.Fatalf("Seed wrote %d skills before validating all files", len(repo.ensured))
	}
}

func TestAuditMetadataCarriesDigestNotBody(t *testing.T) {
	repo := newFakeRepo()
	audit := &recordingAudit{}
	svc := skillapp.NewService(repo, audit)
	ctx := context.Background()
	input := validSkill()
	input.Body = "SECRET-SKILL-BODY-MARKER " + strings.Repeat("x", 32)
	if _, err := svc.Create(ctx, adminActor(), input, ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	raw, err := json.Marshal(audit.events[0].Metadata)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if strings.Contains(string(raw), "SECRET-SKILL-BODY-MARKER") {
		t.Fatalf("audit metadata leaked the body: %s", raw)
	}
	if audit.events[0].Metadata["body_sha256"] == nil {
		t.Fatal("audit metadata must carry body_sha256")
	}
}
