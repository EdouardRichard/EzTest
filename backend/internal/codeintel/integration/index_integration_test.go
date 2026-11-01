//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/gitx"
	codeintelpg "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// --- minimal integration ports ---------------------------------------------------

type integrationVersions struct {
	bindings []codeintelapp.VersionSUTBinding
	setCalls []int64Commit
}

type int64Commit struct {
	versionSUTID int64
	commit       string
}

func (v *integrationVersions) VersionSUTBindings(_ context.Context, _ int64) ([]codeintelapp.VersionSUTBinding, error) {
	return v.bindings, nil
}

func (v *integrationVersions) SetCodeBaselineCommit(_ context.Context, versionSUTID int64, commit string) error {
	v.setCalls = append(v.setCalls, int64Commit{versionSUTID: versionSUTID, commit: commit})
	for index := range v.bindings {
		if v.bindings[index].VersionSUTID == versionSUTID {
			v.bindings[index].CodeBaselineCommit = commit
		}
	}
	return nil
}

type integrationResources struct {
	repo codeintelapp.RepoRef
}

func (r *integrationResources) CodeRepository(_ context.Context, repoID int64) (codeintelapp.RepoRef, error) {
	if repoID != r.repo.ID {
		return codeintelapp.RepoRef{}, codeintel.ErrNotFound
	}
	return r.repo, nil
}

func (r *integrationResources) SUTPages(_ context.Context, _ int64) ([]codeintelapp.PageRef, error) {
	return nil, nil
}

func (r *integrationResources) SUTAPIs(_ context.Context, _ int64) ([]codeintelapp.APIRef, error) {
	return nil, nil
}

type integrationQueue struct {
	mu      sync.Mutex
	ensured []codeintelapp.NewWorkItem
}

func (q *integrationQueue) Ensure(_ context.Context, in codeintelapp.NewWorkItem) (codeintelapp.WorkItem, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.ensured = append(q.ensured, in)
	return codeintelapp.WorkItem{ID: int64(len(q.ensured)), VersionID: in.VersionID, Type: in.Type}, true, nil
}

func (q *integrationQueue) ClaimForTeams(_ context.Context, _ string, _ []string) (codeintelapp.WorkItem, bool, error) {
	return codeintelapp.WorkItem{}, false, nil
}

func (q *integrationQueue) MarkRunning(_ context.Context, _, _ int64) error { return nil }
func (q *integrationQueue) Renew(_ context.Context, _, _ int64, _ time.Duration) error {
	return nil
}
func (q *integrationQueue) Complete(_ context.Context, _, _ int64) error { return nil }
func (q *integrationQueue) Fail(_ context.Context, _, _ int64, _ string, _ bool, _ time.Duration) error {
	return nil
}

type integrationEvents struct {
	mu     sync.Mutex
	events []codeintelapp.Event
}

func (e *integrationEvents) Publish(_ context.Context, event codeintelapp.Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
	return nil
}

func (e *integrationEvents) types() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	result := make([]string, 0, len(e.events))
	for _, event := range e.events {
		result = append(result, event.Type)
	}
	return result
}

type integrationAudit struct{}

func (integrationAudit) Record(_ context.Context, _ codeintelapp.AuditEntry) error { return nil }

type integrationSettings struct {
	ints    map[string]int64
	strings map[string]string
	raws    map[string]json.RawMessage
}

func (s *integrationSettings) String(_ context.Context, key string) (string, error) {
	return s.strings[key], nil
}
func (s *integrationSettings) Int(_ context.Context, key string) (int64, error) {
	return s.ints[key], nil
}
func (s *integrationSettings) Float(_ context.Context, key string) (float64, error) {
	return 0, nil
}
func (s *integrationSettings) Raw(_ context.Context, key string) (json.RawMessage, error) {
	return s.raws[key], nil
}

type integrationClock struct{ now time.Time }

func (c integrationClock) Now() time.Time { return c.now }

// --- real git repository fixture --------------------------------------------------

type integrationRepo struct {
	dir  string
	base string
	head string
}

func newIntegrationRepo(t *testing.T) *integrationRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is not available: %v", err)
	}
	dir := t.TempDir()
	runGitIntegration(t, dir, "init", "-b", "main")
	runGitIntegration(t, dir, "config", "user.name", "EzTest Integration")
	runGitIntegration(t, dir, "config", "user.email", "integration@example.invalid")
	runGitIntegration(t, dir, "config", "core.autocrlf", "false")
	runGitIntegration(t, dir, "config", "commit.gpgsign", "false")

	writeIntegrationFile(t, dir, "src/routes.ts", "import { post } from './client';\nexport const orders = '/orders';\nexport const refund = () => post('/api/refund', {});\n")
	writeIntegrationFile(t, dir, "src/client.ts", "export function post(path, body) { return fetch(path, { method: 'POST', body }); }\n")
	writeIntegrationFile(t, dir, "src/server.go", "package server\n\nfunc Refund() {}\n")
	writeIntegrationFile(t, dir, "src/old.ts", "export const old = true;\n")
	writeIntegrationFile(t, dir, "node_modules/pkg/index.ts", "ignored\n")
	writeIntegrationFile(t, dir, "src/assets/logo.bin", string([]byte{0x00, 0x01, 0x02, 0x03}))

	fixture := &integrationRepo{dir: dir}
	fixture.base = commitIntegration(t, dir, "commit A")

	writeIntegrationFile(t, dir, "src/routes.ts", "import { post } from './client';\nexport const orders = '/orders';\nexport const refund = () => post('/api/refund', {});\nexport const audit = '/api/audit';\n")
	writeIntegrationFile(t, dir, "src/new.ts", "export const added = '/orders/refund';\n")
	runGitIntegration(t, dir, "rm", "--quiet", "src/old.ts")
	writeIntegrationFile(t, dir, "src/assets/logo.bin", string([]byte{0x10, 0x11, 0x12, 0x13, 0x14}))
	fixture.head = commitIntegration(t, dir, "commit B")
	return fixture
}

func writeIntegrationFile(t *testing.T, dir, name, content string) {
	t.Helper()
	target := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", name, err)
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func runGitIntegration(t *testing.T, dir string, args ...string) {
	t.Helper()
	_ = runGitOutput(t, dir, args...)
}

func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=EzTest Integration",
		"GIT_AUTHOR_EMAIL=integration@example.invalid",
		"GIT_COMMITTER_NAME=EzTest Integration",
		"GIT_COMMITTER_EMAIL=integration@example.invalid",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
		"GIT_OPTIONAL_LOCKS=0",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func commitIntegration(t *testing.T, dir, message string) string {
	t.Helper()
	runGitIntegration(t, dir, "add", "-A")
	runGitIntegration(t, dir, "commit", "-m", message)
	return strings.TrimSpace(runGitOutput(t, dir, "rev-parse", "HEAD"))
}

// --- shared service wiring -------------------------------------------------------

type indexIntegrationFixture struct {
	service   *codeintelapp.IndexService
	entities  *codeintelpg.EntityRepository
	runs      *codeintelpg.IndexRunRepository
	versions  *integrationVersions
	resources *integrationResources
	queue     *integrationQueue
	events    *integrationEvents
}

func newIndexIntegrationFixture(t *testing.T, pool *pgxpool.Pool, repoPath string, maxFiles int64) *indexIntegrationFixture {
	t.Helper()
	ids, err := platformid.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	git := gitx.New(nil, 10*time.Second)
	runs := codeintelpg.NewIndexRunRepository(pool)
	entities := codeintelpg.NewEntityRepository(pool, ids)
	versions := &integrationVersions{bindings: []codeintelapp.VersionSUTBinding{{
		VersionSUTID: 11, VersionID: 1, RepoID: 10, Branch: "main",
	}}}
	resources := &integrationResources{repo: codeintelapp.RepoRef{ID: 10, Name: "fixture", Path: repoPath, Branch: "main"}}
	queue := &integrationQueue{}
	events := &integrationEvents{}
	settings := &integrationSettings{
		ints:    map[string]int64{"codeintel.max_index_files": maxFiles},
		strings: map[string]string{},
		raws:    map[string]json.RawMessage{"codeintel.exclude_globs": json.RawMessage(`["**/node_modules/**"]`)},
	}
	service := codeintelapp.NewIndexService(codeintelapp.IndexDeps{
		Runs: runs, Entities: entities, Git: git, Versions: versions, Resources: resources,
		Queue: queue, Events: events, Audit: integrationAudit{}, Settings: settings,
		Clock: integrationClock{now: time.Now()}, IDs: ids,
	})
	return &indexIntegrationFixture{
		service: service, entities: entities, runs: runs, versions: versions,
		resources: resources, queue: queue, events: events,
	}
}

func countEntities(t *testing.T, pool *pgxpool.Pool, repoID int64) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM code_entity WHERE repo_id = $1 AND kind = 'file'
	`, repoID).Scan(&count); err != nil {
		t.Fatalf("count entities: %v", err)
	}
	return count
}

func TestIndexIntegrationRealGitRepo(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := newIntegrationRepo(t)
	fixture := newIndexIntegrationFixture(t, pool, repo.dir, 5000)
	ctx := context.Background()

	run, created, err := fixture.service.Start(ctx, 1, 10, repo.base)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !created || run.Status != string(codeintel.IndexQueued) {
		t.Fatalf("run = %+v created=%v", run, created)
	}
	if len(fixture.queue.ensured) != 1 || fixture.queue.ensured[0].Type != "index.repo" || fixture.queue.ensured[0].Team != "code" {
		t.Fatalf("work items = %+v", fixture.queue.ensured)
	}

	// The active partial-unique run blocks a second commit until it completes.
	if _, _, err := fixture.service.Start(ctx, 1, 10, repo.head); platform.AsError(err).Code != "state_conflict" {
		t.Fatalf("active conflict = %v", err)
	}

	if err := fixture.service.Execute(ctx, run.ID); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	stored, err := fixture.runs.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if stored.Status != string(codeintel.IndexDone) || stored.FilesIndexed == 0 || stored.FilesTotal != stored.FilesIndexed {
		t.Fatalf("stored run = %+v", stored)
	}
	if count := countEntities(t, pool, 10); count != stored.FilesIndexed {
		t.Fatalf("entity count = %d, want %d", count, stored.FilesIndexed)
	}
	if len(fixture.versions.setCalls) != 1 || fixture.versions.setCalls[0].commit != repo.base {
		t.Fatalf("baseline backfill = %+v", fixture.versions.setCalls)
	}
	events := fixture.events.types()
	if len(events) != 2 || events[0] != "code.index_started" || events[1] != "code.index_finished" {
		t.Fatalf("events = %v", events)
	}

	// Replaying the same run is idempotent (file projection replaced, not
	// duplicated).
	if err := fixture.service.Execute(ctx, run.ID); err != nil {
		t.Fatalf("Execute replay: %v", err)
	}
	if count := countEntities(t, pool, 10); count != stored.FilesIndexed {
		t.Fatalf("entity count after replay = %d", count)
	}

	reused, created, err := fixture.service.Start(ctx, 1, 10, repo.base)
	if err != nil {
		t.Fatalf("Start reused: %v", err)
	}
	if created || reused.ID != run.ID {
		t.Fatalf("reuse = %+v created=%v", reused, created)
	}

	// A done run for the head commit can now be indexed separately.
	headRun, created, err := fixture.service.Start(ctx, 1, 10, repo.head)
	if err != nil || !created {
		t.Fatalf("Start head = %+v created=%v err=%v", headRun, created, err)
	}
	if err := fixture.service.Execute(ctx, headRun.ID); err != nil {
		t.Fatalf("Execute head: %v", err)
	}
	tree, _, _, err := fixture.service.Tree(ctx, 10, repo.head, "src", "", 100)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	if len(tree) == 0 {
		t.Fatal("tree is empty")
	}
	for _, entity := range tree {
		if !strings.HasPrefix(entity.Path, "src/") {
			t.Fatalf("tree entry outside prefix: %+v", entity)
		}
		if !strings.HasPrefix(entity.BlobHash, "git-sha1:") {
			t.Fatalf("file entity blob hash = %q", entity.BlobHash)
		}
	}
}

func TestIndexIntegrationTruncation(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := newIntegrationRepo(t)
	fixture := newIndexIntegrationFixture(t, pool, repo.dir, 1)
	ctx := context.Background()
	run, _, err := fixture.service.Start(ctx, 1, 10, repo.base)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := fixture.service.Execute(ctx, run.ID); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	stored, err := fixture.runs.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if !stored.Truncated || stored.FilesIndexed != 1 || stored.FilesTotal <= 1 {
		t.Fatalf("truncated run = %+v", stored)
	}
	if count := countEntities(t, pool, 10); count != 1 {
		t.Fatalf("truncated entity count = %d", count)
	}
}
