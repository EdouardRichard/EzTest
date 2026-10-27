package gitx_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/gitx"
)

// TestRejectsEscapingPath pins the repository-relative path rule: absolute
// paths, traversal, Windows separators and escape-hatching prefixes are all
// rejected with path_escape before anything reaches git.
func TestRejectsEscapingPath(t *testing.T) {
	fx := newTestRepo(t)
	r := gitx.NewGitReader(gitx.GitConfig{CommandTimeout: 5 * time.Second})
	ctx := context.Background()

	escaping := []string{
		"../secret",
		"/etc/passwd",
		`a\b`,
		`src\api\refund.ts`,
		`C:\windows\win.ini`,
		"..",
		"src/../../etc/passwd",
		"src/../..",
		"./../secret",
	}
	for _, path := range escaping {
		_, err := r.File(ctx, fx.dir, fx.commit2, path, 0, 0)
		requireDomainInvalid(t, err, "path_escape")
		_, _, err = r.ReadAll(ctx, fx.dir, fx.commit2, path, 0)
		requireDomainInvalid(t, err, "path_escape")
	}
}

// TestFileUnsupportedPaths pins file reads for symlinks, submodules, binaries
// and non-UTF-8 content. Unsafe modes are never followed.
func TestFileUnsupportedPaths(t *testing.T) {
	fx := newTestRepo(t)
	r := gitx.NewGitReader(gitx.GitConfig{CommandTimeout: 5 * time.Second})
	ctx := context.Background()

	for _, path := range []string{"link.txt", "vendor/sub"} {
		_, err := r.File(ctx, fx.dir, fx.commit2, path, 0, 0)
		requireDomainInvalid(t, err, "path_not_file")
		_, _, err = r.ReadAll(ctx, fx.dir, fx.commit2, path, 0)
		requireDomainInvalid(t, err, "path_not_file")
	}

	_, err := r.File(ctx, fx.dir, fx.commit2, "src/api", 0, 0)
	requireDomainInvalid(t, err, "path_not_file")

	for _, path := range []string{"src/data/binary.bin", "src/data/invalid-utf8.txt"} {
		_, err := r.File(ctx, fx.dir, fx.commit2, path, 0, 0)
		requireDomainInvalid(t, err, "unsupported_content")
	}
}

// TestPathNormalization pins slash normalisation of safe relative paths.
func TestPathNormalization(t *testing.T) {
	fx := newTestRepo(t)
	r := gitx.NewGitReader(gitx.GitConfig{CommandTimeout: 5 * time.Second})
	ctx := context.Background()

	window, err := r.File(ctx, fx.dir, fx.commit2, "src//api/./refund.ts", 1, 1)
	if err != nil {
		t.Fatalf("File(normalised): %v", err)
	}
	if window.Path != "src/api/refund.ts" {
		t.Fatalf("File(normalised).Path=%q want src/api/refund.ts", window.Path)
	}
	if !strings.Contains(window.Content, "export function refund") {
		t.Fatalf("File(normalised).Content=%q", window.Content)
	}
}

// TestRepositoryValidation pins localPath checks: a missing directory is
// path_not_found, a directory that is not a Git work tree is path_not_git_repo
// and a subdirectory of a work tree resolves against the repository root.
func TestRepositoryValidation(t *testing.T) {
	fx := newTestRepo(t)
	r := gitx.NewGitReader(gitx.GitConfig{CommandTimeout: 5 * time.Second})
	ctx := context.Background()

	missing := filepath.Join(t.TempDir(), "absent")
	_, err := r.Head(ctx, missing, "main")
	requireDomainInvalid(t, err, "path_not_found")

	notRepo := t.TempDir()
	_, err = r.Head(ctx, notRepo, "main")
	requireDomainInvalid(t, err, "path_not_git_repo")

	fromSubdirectory, err := r.Head(ctx, filepath.Join(fx.dir, "src"), "main")
	if err != nil {
		t.Fatalf("Head(from subdirectory): %v", err)
	}
	if fromSubdirectory != fx.commit2 {
		t.Fatalf("Head(from subdirectory)=%q want %q", fromSubdirectory, fx.commit2)
	}
}
