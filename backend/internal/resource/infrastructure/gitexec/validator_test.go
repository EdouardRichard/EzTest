package gitexec_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure/gitexec"
)

// runGit executes git against dir and returns its trimmed standard output.
// Tests use it to build fixture repositories and to inspect them.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// initRepo creates a work tree with one commit on branch main and returns the
// commit ID.
func initRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "validator@example.test")
	runGit(t, dir, "config", "user.name", "Validator Test")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")
	return dir, runGit(t, dir, "rev-parse", "HEAD")
}

// assertInvalid asserts err is a domain invalid error for field and reason.
func assertInvalid(t *testing.T, err error, field, reason string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a %s error, got nil", reason)
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("err=%v (%T), want *domain.Error", err, err)
	}
	if domainErr.Kind != domain.KindInvalid || domainErr.Field != field || domainErr.Reason != reason {
		t.Fatalf("error kind=%q field=%q reason=%q, want invalid/%s/%s",
			domainErr.Kind, domainErr.Field, domainErr.Reason, field, reason)
	}
}

func TestValidateRejectsNonRepository(t *testing.T) {
	dir := t.TempDir()
	_, err := gitexec.NewValidator().Validate(context.Background(), dir, "main")
	assertInvalid(t, err, "localPath", "path_not_git_repo")
}

func TestValidateRejectsMissingPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")
	_, err := gitexec.NewValidator().Validate(context.Background(), path, "main")
	assertInvalid(t, err, "localPath", "path_not_found")
}

func TestValidateRejectsFileInsteadOfDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	_, err := gitexec.NewValidator().Validate(context.Background(), path, "main")
	assertInvalid(t, err, "localPath", "path_not_found")
}

func TestValidateRejectsBlankPath(t *testing.T) {
	_, err := gitexec.NewValidator().Validate(context.Background(), "   ", "main")
	assertInvalid(t, err, "localPath", "path_not_found")
}

func TestValidateRejectsMissingBranch(t *testing.T) {
	dir, _ := initRepo(t)
	_, err := gitexec.NewValidator().Validate(context.Background(), dir, "release")
	assertInvalid(t, err, "fixedBranch", "branch_not_found")
}

func TestValidateRejectsInvalidBranch(t *testing.T) {
	dir, _ := initRepo(t)
	for _, branch := range []string{"-main", "--upload-pack=evil", "bad branch", "main:evil", ""} {
		t.Run(branch, func(t *testing.T) {
			_, err := gitexec.NewValidator().Validate(context.Background(), dir, branch)
			assertInvalid(t, err, "fixedBranch", "invalid_branch")
		})
	}
}

func TestValidateReturnsCommit(t *testing.T) {
	dir, want := initRepo(t)
	got, err := gitexec.NewValidator().Validate(context.Background(), dir, "main")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got.Commit != want {
		t.Fatalf("commit=%q want %q", got.Commit, want)
	}
	if len(got.Commit) != 40 {
		t.Fatalf("commit=%q is %d characters, want 40", got.Commit, len(got.Commit))
	}
}

// TestValidateDoesNotModifyWorktree proves the validator is read-only: the
// status, HEAD and file contents are identical before and after, and no index
// lock is left behind.
func TestValidateDoesNotModifyWorktree(t *testing.T) {
	dir, wantCommit := initRepo(t)
	untracked := filepath.Join(dir, "untracked.txt")
	if err := os.WriteFile(untracked, []byte("keep me"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}
	before := runGit(t, dir, "status", "--porcelain")
	headBefore := runGit(t, dir, "rev-parse", "HEAD")

	if _, err := gitexec.NewValidator().Validate(context.Background(), dir, "main"); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	after := runGit(t, dir, "status", "--porcelain")
	if after != before {
		t.Fatalf("worktree status changed from %q to %q", before, after)
	}
	if head := runGit(t, dir, "rev-parse", "HEAD"); head != headBefore || head != wantCommit {
		t.Fatalf("HEAD changed from %q to %q (commit %q)", headBefore, head, wantCommit)
	}
	content, err := os.ReadFile(untracked)
	if err != nil || string(content) != "keep me" {
		t.Fatalf("untracked file=%q err=%v", content, err)
	}
	lockFiles, err := filepath.Glob(filepath.Join(dir, ".git", "index.lock"))
	if err != nil || len(lockFiles) != 0 {
		t.Fatalf("index lock left behind: %v err=%v", lockFiles, err)
	}
}

func TestValidateReturnsContextError(t *testing.T) {
	dir, _ := initRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := gitexec.NewValidator().Validate(ctx, dir, "main")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", err)
	}
}

// TestValidateReportsMissingGit proves an unavailable git binary is an
// operational failure, not a path/branch verdict the API would return as 422.
func TestValidateReportsMissingGit(t *testing.T) {
	dir, _ := initRepo(t)
	t.Setenv("PATH", t.TempDir())
	_, err := gitexec.NewValidator().Validate(context.Background(), dir, "main")
	if err == nil {
		t.Fatal("expected an error without a git binary")
	}
	var domainErr *domain.Error
	if errors.As(err, &domainErr) {
		t.Fatalf("missing git mapped to a validation verdict: %v", err)
	}
}
