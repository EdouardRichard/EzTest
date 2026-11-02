package gitx_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/gitx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// gitFixture is a real temporary git repository with two deterministic commits.
type gitFixture struct {
	dir string
	c1  string
	c2  string
}

const shortContent = "line1\nline2\nline3\nline4\nline5\n"

func newGitFixture(t *testing.T) *gitFixture {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.name", "EzTest Fixture")
	runGit(t, dir, "config", "user.email", "fixture@example.invalid")
	runGit(t, dir, "config", "core.autocrlf", "false")
	runGit(t, dir, "config", "commit.gpgsign", "false")

	writeFile(t, dir, "src/a.ts", shortContent)
	writeFile(t, dir, "src/old.ts", "old line\n")
	writeFile(t, dir, "src/weird name$().ts", "weird line\n")
	writeFile(t, dir, "node_modules/pkg/index.ts", "ignored\n")
	writeFile(t, dir, "src/assets/logo.bin", string([]byte{0x00, 0x01, 0x02, 0x03}))
	writeFile(t, dir, "link.txt", "src/a.ts")
	writeFile(t, dir, "src/link.ts", "a.ts")
	writeFile(t, dir, "src/big.txt", strings.Repeat("row\n", 450))

	// Symlinks are created through the index so the fixture works on Windows
	// hosts without symlink privileges. The entries are applied *after*
	// `git add -A` because a plain add replaces them with the regular worktree
	// file on hosts with symlink support (Linux CI).
	runGit(t, dir, "add", "-A")
	linkBlob := runGitInput(t, dir, "src/a.ts", "hash-object", "-w", "--stdin")
	fixture := &gitFixture{dir: dir}
	fixture.c1 = commitPreservingSymlinks(t, dir, "commit A", linkBlob)

	// Commit B: rename a.ts (pure rename to keep R100 deterministic), add a
	// file, delete a file, change the binary asset and the shell-metacharacter
	// name.
	runGit(t, dir, "mv", "src/a.ts", "src/renamed a.ts")
	writeFile(t, dir, "src/new.ts", "export const added = true;\n")
	runGit(t, dir, "rm", "--quiet", "src/old.ts")
	writeFile(t, dir, "src/assets/logo.bin", string([]byte{0x10, 0x11, 0x12, 0x13, 0x14}))
	writeFile(t, dir, "src/weird name$().ts", "weird changed line\n")
	fixture.c2 = commitPreservingSymlinks(t, dir, "commit B", linkBlob)
	return fixture
}

// commitPreservingSymlinks stages the worktree, re-applies the symlink index
// entries and then commits, so the fixture tree marks those paths as mode
// 120000 on every host.
func commitPreservingSymlinks(t *testing.T, dir, message, symlinkBlob string) string {
	t.Helper()
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "update-index", "--add", "--cacheinfo", "120000,"+symlinkBlob+",link.txt")
	runGit(t, dir, "update-index", "--add", "--cacheinfo", "120000,"+symlinkBlob+",src/link.ts")
	cmd := exec.Command("git", "commit", "-m", message)
	cmd.Dir = dir
	cmd.Env = fixtureEnv()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit %q: %v\n%s", message, err, output)
	}
	return strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	target := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", name, err)
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func commitAll(t *testing.T, dir, message string) string {
	t.Helper()
	runGit(t, dir, "add", "-A")
	cmd := exec.Command("git", "commit", "-m", message)
	cmd.Dir = dir
	cmd.Env = fixtureEnv()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit %q: %v\n%s", message, err, output)
	}
	return strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(runGitOutputBytes(t, dir, nil, args...))
}

func runGitInput(t *testing.T, dir, input string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(runGitOutputBytes(t, dir, strings.NewReader(input), args...))
}

func runGitOutputBytes(t *testing.T, dir string, stdin *strings.Reader, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = fixtureEnv()
	if stdin != nil {
		cmd.Stdin = stdin
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func fixtureEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=EzTest Fixture",
		"GIT_AUTHOR_EMAIL=fixture@example.invalid",
		"GIT_COMMITTER_NAME=EzTest Fixture",
		"GIT_COMMITTER_EMAIL=fixture@example.invalid",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
		"GIT_OPTIONAL_LOCKS=0",
	)
}

func newClient(t *testing.T) *gitx.Git {
	t.Helper()
	return gitx.New(nil, 10*time.Second)
}

func errorCode(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	return platform.AsError(err).Code
}

func TestResolveCommitAndAncestor(t *testing.T) {
	fixture := newGitFixture(t)
	client := newClient(t)
	ctx := context.Background()

	head, err := client.ResolveCommit(ctx, fixture.dir, "main")
	if err != nil {
		t.Fatalf("ResolveCommit(main): %v", err)
	}
	if head != fixture.c2 {
		t.Fatalf("ResolveCommit(main) = %q, want %q", head, fixture.c2)
	}
	sha, err := client.ResolveCommit(ctx, fixture.dir, fixture.c2)
	if err != nil || sha != fixture.c2 {
		t.Fatalf("ResolveCommit(sha) = %q, %v", sha, err)
	}
	if _, err := client.ResolveCommit(ctx, fixture.dir, "-n"); err == nil {
		t.Fatal("option-like ref accepted")
	}

	ancestor, err := client.IsAncestor(ctx, fixture.dir, fixture.c1, fixture.c2)
	if err != nil {
		t.Fatalf("IsAncestor(c1, c2): %v", err)
	}
	if !ancestor {
		t.Fatal("c1 must be an ancestor of c2")
	}
	ancestor, err = client.IsAncestor(ctx, fixture.dir, fixture.c2, fixture.c1)
	if err != nil {
		t.Fatalf("IsAncestor(c2, c1): %v", err)
	}
	if ancestor {
		t.Fatal("c2 must not be an ancestor of c1")
	}

	head, err = client.HeadCommit(ctx, fixture.dir, "")
	if err != nil || head != fixture.c2 {
		t.Fatalf("HeadCommit(default) = %q, %v", head, err)
	}
	head, err = client.HeadCommit(ctx, fixture.dir, "main")
	if err != nil || head != fixture.c2 {
		t.Fatalf("HeadCommit(main) = %q, %v", head, err)
	}
	if _, err := client.HeadCommit(ctx, fixture.dir, "missing-branch"); errorCode(t, err) != "invalid_head" {
		t.Fatalf("HeadCommit(missing) code = %q", errorCode(t, err))
	}

	if _, err := client.ResolveCommit(ctx, fixture.dir, "does-not-exist"); errorCode(t, err) != "commit_not_found" {
		t.Fatalf("ResolveCommit(missing) code = %q", errorCode(t, err))
	}

	missing := filepath.Join(t.TempDir(), "nope")
	if _, err := client.ResolveCommit(ctx, missing, "main"); errorCode(t, err) != "repo_not_found" {
		t.Fatalf("missing repo code = %q", errorCode(t, err))
	}
	plain := t.TempDir()
	if _, err := client.ResolveCommit(ctx, plain, "main"); errorCode(t, err) != "path_not_repo" {
		t.Fatalf("plain dir code = %q", errorCode(t, err))
	}
}

// blockingRunner proves the per-command 10s timeout path is configurable and
// maps to the retryable git_unavailable code.
type blockingRunner struct{}

func (blockingRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, []byte, error) {
	<-ctx.Done()
	return nil, nil, ctx.Err()
}

func TestGitCommandTimeoutMapsToRetryableUnavailable(t *testing.T) {
	client := gitx.New(blockingRunner{}, 20*time.Millisecond)
	_, err := client.ResolveCommit(context.Background(), t.TempDir(), "main")
	apiErr := platform.AsError(err)
	if apiErr.Code != "git_unavailable" {
		t.Fatalf("timeout code = %q, want git_unavailable", apiErr.Code)
	}
	if !apiErr.Retryable || apiErr.Status != 503 {
		t.Fatalf("timeout status/retryable = %d/%v", apiErr.Status, apiErr.Retryable)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error does not wrap context.DeadlineExceeded: %v", err)
	}
}

func TestListTreeParsesFilesAndFilters(t *testing.T) {
	fixture := newGitFixture(t)
	client := newClient(t)
	ctx := context.Background()

	result, err := client.ListTree(ctx, fixture.dir, fixture.c1, []string{"src/**"}, []string{"**/node_modules/**"}, 100)
	if err != nil {
		t.Fatalf("ListTree: %v", err)
	}
	paths := make([]string, 0, len(result.Entries))
	for _, entry := range result.Entries {
		paths = append(paths, entry.Path)
	}
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	if strings.Join(paths, "\n") != strings.Join(sorted, "\n") {
		t.Fatalf("entries are not sorted by path: %v", paths)
	}
	find := func(path string) (codeintel.TreeEntry, bool) {
		for _, entry := range result.Entries {
			if entry.Path == path {
				return entry, true
			}
		}
		return codeintel.TreeEntry{}, false
	}
	if _, ok := find("node_modules/pkg/index.ts"); ok {
		t.Fatal("excluded node_modules entry returned")
	}
	if entry, ok := find("src/a.ts"); !ok {
		t.Fatalf("src/a.ts missing from %v", paths)
	} else if entry.Mode != "100644" || entry.Type != "blob" || entry.Size != int64(len(shortContent)) || entry.BlobSHA == "" {
		t.Fatalf("src/a.ts entry = %+v", entry)
	}
	if entry, ok := find("src/assets/logo.bin"); !ok {
		t.Fatal("binary fixture missing")
	} else if !entry.Binary {
		t.Fatalf("binary file not marked: %+v", entry)
	}
	if entry, ok := find("src/link.ts"); !ok {
		t.Fatal("symlink fixture missing")
	} else if !entry.Unsupported {
		t.Fatalf("symlink not marked unsupported: %+v", entry)
	}

	truncated, err := client.ListTree(ctx, fixture.dir, fixture.c1, nil, nil, 2)
	if err != nil {
		t.Fatalf("ListTree truncated: %v", err)
	}
	if len(truncated.Entries) != 2 || !truncated.Truncated || truncated.Total <= 2 {
		t.Fatalf("truncated result = %d/%v/%d", len(truncated.Entries), truncated.Truncated, truncated.Total)
	}

	if _, err := client.ListTree(ctx, fixture.dir, "0000000000000000000000000000000000000000", nil, nil, 10); errorCode(t, err) != "commit_not_found" {
		t.Fatalf("missing commit code = %q", errorCode(t, err))
	}
}
