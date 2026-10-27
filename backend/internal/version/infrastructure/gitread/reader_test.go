package gitread_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/gitread"
)

// runGit runs one fixed git command in dir and returns its trimmed standard
// output.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

// newTestRepo initialises a repository on branch main and returns its path.
func newTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "gitread@example.com")
	runGit(t, dir, "config", "user.name", "Git Read Test")
	return dir
}

// writeRepoFile writes one repository-relative file.
func writeRepoFile(t *testing.T, dir, relative, content string) {
	t.Helper()
	absolute := filepath.Join(dir, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", relative, err)
	}
	if err := os.WriteFile(absolute, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", relative, err)
	}
}

// commitRepo commits the work tree and returns the new commit id.
func commitRepo(t *testing.T, dir, message string) string {
	t.Helper()
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", message)
	return runGit(t, dir, "rev-parse", "HEAD")
}

// gitErrorReason returns the *application.GitError reason carried by err.
func gitErrorReason(t *testing.T, err error) string {
	t.Helper()
	var gitErr *application.GitError
	if !errors.As(err, &gitErr) {
		t.Fatalf("error %v is not *application.GitError", err)
	}
	return gitErr.Reason
}

// entryPaths returns the paths of a code tree listing.
func entryPaths(entries []application.CodeEntry) []string {
	paths := make([]string, len(entries))
	for i, entry := range entries {
		paths[i] = entry.Path
	}
	return paths
}

// TestGitReadAtCommit pins the read-only Git reader: commit-pinned tree/read,
// include/exclude filtering, Byte bounds and the rejection of path escalation.
func TestGitReadAtCommit(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)

	writeRepoFile(t, repo, "README.md", "hello\n")
	writeRepoFile(t, repo, "src/main.go", "package main\n")
	writeRepoFile(t, repo, "src/old.txt", "old\n")
	firstCommit := commitRepo(t, repo, "first")

	writeRepoFile(t, repo, "README.md", "hello v2\n")
	writeRepoFile(t, repo, "src/new.txt", "new\n")
	secondCommit := commitRepo(t, repo, "second")

	reader := gitread.NewGitReader(5 * time.Second)

	t.Run("ResolveCommit returns the branch head", func(t *testing.T) {
		commit, err := reader.ResolveCommit(ctx, repo, "main")
		if err != nil {
			t.Fatalf("ResolveCommit: %v", err)
		}
		if commit != secondCommit || len(commit) != 40 {
			t.Fatalf("ResolveCommit=%q want %q (40 hex)", commit, secondCommit)
		}
	})

	t.Run("Tree lists the pinned commit", func(t *testing.T) {
		entries, err := reader.Tree(ctx, repo, firstCommit, nil, nil, 200)
		if err != nil {
			t.Fatalf("Tree(first): %v", err)
		}
		paths := entryPaths(entries)
		for _, want := range []string{"README.md", "src/main.go", "src/old.txt"} {
			if !contains(paths, want) {
				t.Fatalf("Tree(first) paths=%v missing %q", paths, want)
			}
		}
		if contains(paths, "src/new.txt") {
			t.Fatalf("Tree(first) paths=%v leaked a file from a later commit", paths)
		}
		for _, entry := range entries {
			if entry.Path == "src/main.go" {
				if entry.Type != "blob" || entry.Size != int64(len("package main\n")) {
					t.Fatalf("Tree entry %+v want blob size %d", entry, len("package main\n"))
				}
			}
		}

		limited, err := reader.Tree(ctx, repo, secondCommit, nil, nil, 1)
		if err != nil {
			t.Fatalf("Tree(limit 1): %v", err)
		}
		if len(limited) != 1 {
			t.Fatalf("Tree(limit 1) returned %d entries, want 1", len(limited))
		}
	})

	t.Run("Tree applies include and exclude globs", func(t *testing.T) {
		included, err := reader.Tree(ctx, repo, secondCommit, []string{"src/**"}, nil, 200)
		if err != nil {
			t.Fatalf("Tree(include): %v", err)
		}
		paths := entryPaths(included)
		if !contains(paths, "src/main.go") || contains(paths, "README.md") {
			t.Fatalf("Tree(include src/**) paths=%v", paths)
		}

		excluded, err := reader.Tree(ctx, repo, secondCommit, []string{"src/**"}, []string{"src/old.txt"}, 200)
		if err != nil {
			t.Fatalf("Tree(exclude): %v", err)
		}
		paths = entryPaths(excluded)
		if contains(paths, "src/old.txt") || !contains(paths, "src/main.go") {
			t.Fatalf("Tree(exclude src/old.txt) paths=%v", paths)
		}

		star, err := reader.Tree(ctx, repo, secondCommit, []string{"**/*.txt"}, nil, 200)
		if err != nil {
			t.Fatalf("Tree(**/*.txt): %v", err)
		}
		paths = entryPaths(star)
		if !contains(paths, "src/new.txt") || !contains(paths, "src/old.txt") || contains(paths, "README.md") {
			t.Fatalf("Tree(**/*.txt) paths=%v", paths)
		}
	})

	t.Run("Read is pinned to the commit and bounded", func(t *testing.T) {
		content, err := reader.Read(ctx, repo, firstCommit, "README.md", 0)
		if err != nil {
			t.Fatalf("Read(first): %v", err)
		}
		if content != "hello\n" {
			t.Fatalf("Read(first)=%q want the first commit content", content)
		}
		latest, err := reader.Read(ctx, repo, secondCommit, "README.md", 0)
		if err != nil {
			t.Fatalf("Read(second): %v", err)
		}
		if latest != "hello v2\n" {
			t.Fatalf("Read(second)=%q want the second commit content", latest)
		}
		bounded, err := reader.Read(ctx, repo, secondCommit, "README.md", 3)
		if err != nil {
			t.Fatalf("Read(bounded): %v", err)
		}
		if bounded != "hel" {
			t.Fatalf("Read(bounded)=%q want %q", bounded, "hel")
		}
	})

	t.Run("ReadRange returns a window plus the blob size", func(t *testing.T) {
		content, size, err := reader.ReadRange(ctx, repo, secondCommit, "README.md", 1, 4)
		if err != nil {
			t.Fatalf("ReadRange: %v", err)
		}
		if content != "ello" || size != int64(len("hello v2\n")) {
			t.Fatalf("ReadRange=%q size=%d want %q size=%d", content, size, "ello", len("hello v2\n"))
		}
	})

	t.Run("Read rejects path escalation", func(t *testing.T) {
		for _, path := range []string{"../README.md", "src/../../README.md", "/etc/passwd", `C:\windows\win.ini`, `src\main.go`} {
			if _, err := reader.Read(ctx, repo, secondCommit, path, 0); err == nil {
				t.Fatalf("Read(%q) returned nil error, want a path rejection", path)
			}
		}
		if _, err := reader.Read(ctx, repo, secondCommit, "src/missing.go", 0); err == nil {
			t.Fatal("Read(missing) returned nil error, want path_not_found")
		} else if reason := gitErrorReason(t, err); reason != "path_not_found" {
			t.Fatalf("Read(missing) reason=%q want path_not_found", reason)
		}
	})

	t.Run("unknown branch, path and non-repo are typed", func(t *testing.T) {
		if _, err := reader.ResolveCommit(ctx, repo, "no-such-branch"); err == nil {
			t.Fatal("ResolveCommit(missing branch) returned nil error")
		} else if reason := gitErrorReason(t, err); reason != "branch_not_found" {
			t.Fatalf("ResolveCommit(missing branch) reason=%q want branch_not_found", reason)
		}

		missing := filepath.Join(t.TempDir(), "absent")
		if _, err := reader.ResolveCommit(ctx, missing, "main"); err == nil {
			t.Fatal("ResolveCommit(missing path) returned nil error")
		} else if reason := gitErrorReason(t, err); reason != "path_not_found" {
			t.Fatalf("ResolveCommit(missing path) reason=%q want path_not_found", reason)
		}

		notRepo := t.TempDir()
		if _, err := reader.ResolveCommit(ctx, notRepo, "main"); err == nil {
			t.Fatal("ResolveCommit(non-repo) returned nil error")
		} else if reason := gitErrorReason(t, err); reason != "path_not_git_repo" {
			t.Fatalf("ResolveCommit(non-repo) reason=%q want path_not_git_repo", reason)
		}
	})

	t.Run("an unknown pinned commit is resource_unavailable", func(t *testing.T) {
		unknown := strings.Repeat("a", 40)
		if _, err := reader.Tree(ctx, repo, unknown, nil, nil, 10); err == nil {
			t.Fatal("Tree(unknown commit) returned nil error")
		} else if reason := gitErrorReason(t, err); reason != "resource_unavailable" {
			t.Fatalf("Tree(unknown commit) reason=%q want resource_unavailable", reason)
		}
	})
}

// TestGitTreePrefixFiltersBeforeCap pins the prefix-aware listing: the prefix
// (and the include/exclude globs) must be applied before the 200-entry cap, so a
// subtree that sorts after more than 200 other entries is still listed.
func TestGitTreePrefixFiltersBeforeCap(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)

	for i := 0; i < 220; i++ {
		writeRepoFile(t, repo, fmt.Sprintf("aaa/file-%03d.txt", i), "x\n")
	}
	writeRepoFile(t, repo, "src/main.go", "package main\n")
	writeRepoFile(t, repo, "src/nested/util.go", "package nested\n")
	writeRepoFile(t, repo, "README.md", "readme\n")
	commit := commitRepo(t, repo, "many files")
	reader := gitread.NewGitReader(5 * time.Second)

	// The plain listing fills its whole cap with entries that sort before src.
	capped, err := reader.Tree(ctx, repo, commit, nil, nil, 200)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	if len(capped) != 200 {
		t.Fatalf("Tree returned %d entries, want the 200-entry cap", len(capped))
	}
	for _, entry := range capped {
		if entry.Path == "src/main.go" || entry.Path == "src/nested/util.go" {
			t.Fatalf("Tree returned %q inside the cap; the fixture is wrong", entry.Path)
		}
	}

	// TreePrefix filters the prefix before the cap.
	entries, err := reader.TreePrefix(ctx, repo, commit, "src", nil, nil, 200)
	if err != nil {
		t.Fatalf("TreePrefix: %v", err)
	}
	paths := entryPaths(entries)
	for _, want := range []string{"src/main.go", "src/nested", "src/nested/util.go"} {
		if !contains(paths, want) {
			t.Fatalf("TreePrefix(src) paths=%v missing %q", paths, want)
		}
	}
	if contains(paths, "README.md") {
		t.Fatalf("TreePrefix(src) paths=%v leaked an entry outside the prefix", paths)
	}

	// The include/exclude globs are applied before the cap too.
	filtered, err := reader.TreePrefix(ctx, repo, commit, "src", []string{"src/**"}, []string{"src/nested"}, 200)
	if err != nil {
		t.Fatalf("TreePrefix(globs): %v", err)
	}
	if paths := entryPaths(filtered); len(paths) != 1 || paths[0] != "src/main.go" {
		t.Fatalf("TreePrefix(src, include src/**, exclude src/nested) paths=%v want [src/main.go]", paths)
	}

	// The cap still bounds the already-filtered prefix.
	limited, err := reader.TreePrefix(ctx, repo, commit, "src", nil, nil, 1)
	if err != nil {
		t.Fatalf("TreePrefix(max 1): %v", err)
	}
	if len(limited) != 1 || !strings.HasPrefix(limited[0].Path, "src/") {
		t.Fatalf("TreePrefix(max 1)=%+v want one src entry", entryPaths(limited))
	}

	// A traversing prefix is rejected before it can reach git.
	if _, err := reader.TreePrefix(ctx, repo, commit, "../src", nil, nil, 200); err == nil {
		t.Fatal("TreePrefix(../src) returned nil error")
	} else if reason := gitErrorReason(t, err); reason != "path_not_found" {
		t.Fatalf("TreePrefix(../src) reason=%q want path_not_found", reason)
	}
}

// contains reports whether values contains want.
func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
