//go:build integration

package integration

// This file wires the real T0 index service (gitx + PostgreSQL repositories +
// resource index-state adapter) against a temporary Git repository to prove the
// §6.1 repository index run end to end: run persistence, file entities,
// truncated stats and last_indexed_at. The unit-level service tests live in
// codeintel/application; this test only covers the DB/Git integration.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/gitx"
	codeintelpg "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/postgres"
	codeintelresource "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/resource"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	resourcepg "github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// indexIntegrationQueue captures the enqueued index jobs without starting a worker;
// the test invokes IndexService.Run synchronously instead.
type indexIntegrationQueue struct {
	mu   sync.Mutex
	jobs []codeintelapp.Job
}

func (q *indexIntegrationQueue) Enqueue(job codeintelapp.Job) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.jobs = append(q.jobs, job)
	return nil
}

func (q *indexIntegrationQueue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.jobs)
}

// indexIntegrationClock is the wall clock used by the index service fixture.
type indexIntegrationClock struct{}

func (indexIntegrationClock) Now() time.Time { return time.Now().UTC() }

// newIndexGitRepo creates a deterministic single-commit repository and returns
// its directory and head commit. Author/committer dates are pinned so the
// fixture only differs by content.
func newIndexGitRepo(t *testing.T, files map[string]string) (dir, head string) {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not available")
	}
	dir = t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(gitPath, append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_OPTIONAL_LOCKS=0",
			"GIT_AUTHOR_NAME=EzTest Index",
			"GIT_AUTHOR_EMAIL=index@example.com",
			"GIT_COMMITTER_NAME=EzTest Index",
			"GIT_COMMITTER_EMAIL=index@example.com",
			"GIT_AUTHOR_DATE=2026-01-01T00:00:00+0000",
			"GIT_COMMITTER_DATE=2026-01-01T00:00:00+0000",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "index@example.com")
	run("config", "user.name", "EzTest Index")
	run("config", "commit.gpgsign", "false")
	run("config", "core.autocrlf", "false")

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		absolute := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(absolute, []byte(files[name]), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	run("add", "-A")
	run("commit", "-q", "-m", "fixture")
	head = run("rev-parse", "HEAD")
	if len(head) != 40 {
		t.Fatalf("head = %q, want a 40-hex commit", head)
	}
	return dir, head
}

// seedIndexRepo inserts one P2 code repository pointing at localPath with the
// P2 defaults (fixed branch main, active).
func seedIndexRepo(t *testing.T, db *pgxpool.Pool, localPath string) int64 {
	t.Helper()
	ctx := context.Background()
	key := fmt.Sprintf("p4-index-%d", nextSeedID())
	var repoID int64
	if err := db.QueryRow(ctx,
		`INSERT INTO code_repository (key, name, local_path) VALUES ($1, $2, $3) RETURNING id`,
		key, "P4 index repository", localPath,
	).Scan(&repoID); err != nil {
		t.Fatalf("seed code repository: %v", err)
	}
	return repoID
}

// newIndexServiceForIntegration assembles the production index service over the
// real gitx and PostgreSQL adapters and the real resource MarkIndexed adapter.
func newIndexServiceForIntegration(t *testing.T, pool *pgxpool.Pool, maxIndexFiles int) (*codeintelapp.IndexService, *codeintelpg.IndexRunRepo, *indexIntegrationQueue) {
	t.Helper()
	repoStore := resourcepg.NewCodeRepository(pool)
	repoService := resourceapp.NewCodeRepoService(repoStore, nil, nil)
	generator, err := id.NewSnowflake(7)
	if err != nil {
		t.Fatalf("new snowflake generator: %v", err)
	}
	queue := &indexIntegrationQueue{}
	service := codeintelapp.NewIndexService(codeintelapp.IndexServiceDeps{
		Git:           gitx.NewGitReader(gitx.GitConfig{CommandTimeout: 10 * time.Second}),
		Repos:         codeintelresource.NewCatalog(repoService),
		IndexRuns:     codeintelpg.NewIndexRunRepository(pool),
		Entities:      codeintelpg.NewEntityRepository(pool),
		ChangeSets:    codeintelpg.NewChangeSetRepository(pool),
		State:         codeintelresource.NewIndexStateWriter(repoService),
		Queue:         queue,
		IDs:           generator,
		Clock:         indexIntegrationClock{},
		MaxIndexFiles: maxIndexFiles,
	})
	return service, codeintelpg.NewIndexRunRepository(pool), queue
}

// indexRunStats is the JSONB shape the index run writes (design spec §6.1).
type indexRunStatsJSON struct {
	Files          int  `json:"files"`
	Truncated      bool `json:"truncated"`
	TruncatedFiles int  `json:"truncatedFiles"`
}

// TestIndexIntegrationRunsIndexAndMarksIndexed proves the full T0 index run:
// a queued run is persisted, the tree is indexed into file entities, the P2
// last_indexed_at is written through the resource adapter and the run ends
// done.
func TestIndexIntegrationRunsIndexAndMarksIndexed(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	dir, head := newIndexGitRepo(t, map[string]string{
		"src/a.ts":       "export const a = 1;\n",
		"src/b.ts":       "export const b = 2;\n",
		"docs/guide.md":  "# guide\n",
		"vendor/skip.ts": "// the default exclude globs skip vendor/**\n",
	})
	repoID := seedIndexRepo(t, pool, dir)
	service, runs, queue := newIndexServiceForIntegration(t, pool, 1000)

	run, err := service.Start(ctx, repoID, 1)
	if err != nil {
		t.Fatalf("Start error = %v", err)
	}
	if run.Status != domain.RunQueued || run.CommitSHA != head {
		t.Fatalf("run = %+v, want queued at %s", run, head)
	}
	if queue.len() != 1 {
		t.Fatalf("enqueued jobs = %d, want 1", queue.len())
	}

	// A second trigger while the run is queued is an index_in_progress conflict.
	if _, err := service.Start(ctx, repoID, 1); !errors.Is(err, domain.ErrConflict) || domain.Reason(err) != "index_in_progress" {
		t.Fatalf("duplicate Start error = %v, want index_in_progress conflict", err)
	}

	if err := service.Run(ctx, run.ID); err != nil {
		t.Fatalf("Run error = %v", err)
	}

	stored, err := runs.GetByID(ctx, repoID, run.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.Status != domain.RunDone {
		t.Fatalf("stored status = %q, want done", stored.Status)
	}
	if stored.StartedAt == nil || stored.FinishedAt == nil {
		t.Fatalf("stored timestamps = %v/%v, want both set", stored.StartedAt, stored.FinishedAt)
	}
	var stats indexRunStatsJSON
	if err := json.Unmarshal(stored.Stats, &stats); err != nil {
		t.Fatalf("decode stats %s: %v", string(stored.Stats), err)
	}
	if stats.Files != 3 || stats.Truncated || stats.TruncatedFiles != 0 {
		t.Fatalf("stats = %+v, want files=3 truncated=false", stats)
	}

	// Only regular blobs become file entities; the default exclude globs keep
	// vendor/** out of the index.
	rows, err := pool.Query(ctx,
		`SELECT path, blob_hash, confidence
		 FROM code_entity
		 WHERE repo_id = $1 AND commit_sha = $2 AND kind = 'file' AND source = 'git'
		 ORDER BY path`, repoID, head)
	if err != nil {
		t.Fatalf("query file entities: %v", err)
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var path, blobHash, confidence string
		if err := rows.Scan(&path, &blobHash, &confidence); err != nil {
			t.Fatalf("scan file entity: %v", err)
		}
		paths = append(paths, path)
		if !strings.HasPrefix(blobHash, "git-sha1:") || len(blobHash) != len("git-sha1:")+40 {
			t.Fatalf("blob_hash %q for %s, want git-sha1:<40hex>", blobHash, path)
		}
		if confidence != domain.ConfidenceHigh {
			t.Fatalf("confidence %q for %s, want high", confidence, path)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate file entities: %v", err)
	}
	if want := []string{"docs/guide.md", "src/a.ts", "src/b.ts"}; fmt.Sprint(paths) != fmt.Sprint(want) {
		t.Fatalf("indexed paths = %v, want %v", paths, want)
	}

	// last_indexed_at is written through the resource application adapter and
	// uses the run finished_at; last_commit stays untouched.
	var lastIndexedAt *time.Time
	var lastCommit string
	if err := pool.QueryRow(ctx,
		`SELECT last_indexed_at, last_commit FROM code_repository WHERE id = $1`, repoID,
	).Scan(&lastIndexedAt, &lastCommit); err != nil {
		t.Fatalf("read repository index state: %v", err)
	}
	if lastIndexedAt == nil {
		t.Fatal("last_indexed_at = NULL, want the run finished_at")
	}
	if !lastIndexedAt.Equal(*stored.FinishedAt) {
		t.Fatalf("last_indexed_at = %v, want run finished_at %v", *lastIndexedAt, *stored.FinishedAt)
	}
	if lastCommit != "" {
		t.Fatalf("last_commit = %q, want untouched empty value", lastCommit)
	}
}

// TestIndexIntegrationTruncatesAtMaxFiles pins the configurable file cap against
// the real Git tree: stats.truncated is set and only MaxIndexFiles entities are
// written.
func TestIndexIntegrationTruncatesAtMaxFiles(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	dir, head := newIndexGitRepo(t, map[string]string{
		"a.ts": "const a = 1;\n",
		"b.ts": "const b = 2;\n",
		"c.ts": "const c = 3;\n",
	})
	repoID := seedIndexRepo(t, pool, dir)
	service, runs, _ := newIndexServiceForIntegration(t, pool, 2)

	run, err := service.Start(ctx, repoID, 1)
	if err != nil {
		t.Fatalf("Start error = %v", err)
	}
	if err := service.Run(ctx, run.ID); err != nil {
		t.Fatalf("Run error = %v", err)
	}

	stored, err := runs.GetByID(ctx, repoID, run.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.Status != domain.RunDone {
		t.Fatalf("stored status = %q, want done", stored.Status)
	}
	var stats indexRunStatsJSON
	if err := json.Unmarshal(stored.Stats, &stats); err != nil {
		t.Fatalf("decode stats %s: %v", string(stored.Stats), err)
	}
	if stats.Files != 2 || !stats.Truncated || stats.TruncatedFiles <= 0 {
		t.Fatalf("stats = %+v, want files=2 truncated=true truncatedFiles>0", stats)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*)::int FROM code_entity
		 WHERE repo_id = $1 AND commit_sha = $2 AND kind = 'file' AND source = 'git'`,
		repoID, head).Scan(&count); err != nil {
		t.Fatalf("count file entities: %v", err)
	}
	if count != 2 {
		t.Fatalf("file entities = %d, want the 2-file cap", count)
	}

	var lastIndexedAt *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT last_indexed_at FROM code_repository WHERE id = $1`, repoID,
	).Scan(&lastIndexedAt); err != nil {
		t.Fatalf("read last_indexed_at: %v", err)
	}
	if lastIndexedAt == nil {
		t.Fatal("last_indexed_at = NULL, want the run finished_at even when truncated")
	}
}
