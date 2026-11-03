package main

// Task 37 fix round 1: the production source provider feeds the static API scan
// from the repositories bound to a version. This test pins its bounds and its
// best-effort repository handling without touching a real git repository.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

type fakeP9SourceGit struct {
	mu        sync.Mutex
	trees     map[string]codeintel.TreeResult
	content   map[string][]string
	listErr   map[string]error
	readErr   map[string]error
	listMaxes []int
}

func (f *fakeP9SourceGit) ListTree(_ context.Context, repoPath, _ string, _, _ []string, max int) (codeintel.TreeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listMaxes = append(f.listMaxes, max)
	if err := f.listErr[repoPath]; err != nil {
		return codeintel.TreeResult{}, err
	}
	return f.trees[repoPath], nil
}

func (f *fakeP9SourceGit) ReadFileWindow(_ context.Context, repoPath, _, filePath string, _, _ int) (codeintel.FileWindow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.readErr[filePath]; err != nil {
		return codeintel.FileWindow{}, err
	}
	return codeintel.FileWindow{Path: filePath, Lines: append([]string(nil), f.content[repoPath+"|"+filePath]...)}, nil
}

func p9SourceVersions() versionapp.VersionView {
	repository := &resourceapp.CodeRepositoryView{
		ID: "1201", Name: "orders", Path: "D:/repos/orders", Branch: "main",
		Include: []string{"**/*.go"}, Exclude: []string{"**/vendor/**"},
	}
	return versionapp.VersionView{
		ID: 1001,
		Bindings: []versionapp.BindingView{
			{ID: 77, CodeBaselineCommit: "deadbeef", Repository: repository},
			{ID: 78, Repository: &resourceapp.CodeRepositoryView{ID: "1202", Name: "no-path"}},
		},
	}
}

func TestP9SourceProvider(t *testing.T) {
	git := &fakeP9SourceGit{
		trees: map[string]codeintel.TreeResult{
			"D:/repos/orders": {Entries: []codeintel.TreeEntry{
				{Path: "backend/routes.go", Size: 40},
				{Path: "backend/binary.bin", Size: 4, Binary: true},
				{Path: "backend/link", Size: 10, Unsupported: true},
				{Path: "backend/huge.go", Size: int64(codeintelapp.DefaultStaticMaxFileBytes) + 1},
				{Path: "backend/unreadable.go", Size: 10},
			}},
		},
		content: map[string][]string{
			"D:/repos/orders|backend/routes.go": {"package backend", `r.POST("/orders", createOrder)`},
		},
		readErr: map[string]error{"backend/unreadable.go": errors.New("file too large")},
	}
	provider := newP9SourceProviderWithGit(git, &fakeP9Versions{view: p9SourceVersions()})
	files, err := provider.SourceFiles(context.Background(), 1001)
	if err != nil {
		t.Fatalf("SourceFiles: %v", err)
	}
	if len(files) != 1 || files[0].Path != "backend/routes.go" {
		t.Fatalf("files = %+v, want only the readable text file", files)
	}
	if !strings.Contains(files[0].Content, "r.POST") {
		t.Fatalf("content = %q", files[0].Content)
	}
	if len(git.listMaxes) != 1 || git.listMaxes[0] != codeintelapp.DefaultStaticMaxFiles {
		t.Fatalf("list max = %v, want the static file cap", git.listMaxes)
	}

	// Best effort: a repository read error skips that repository; a version read
	// error fails the scan.
	failing := &fakeP9SourceGit{
		trees:   map[string]codeintel.TreeResult{},
		content: map[string][]string{},
		listErr: map[string]error{"D:/repos/orders": errors.New("git unavailable")},
	}
	provider = newP9SourceProviderWithGit(failing, &fakeP9Versions{view: p9SourceVersions()})
	if files, err := provider.SourceFiles(context.Background(), 1001); err != nil || len(files) != 0 {
		t.Fatalf("degraded repository read = %+v, %v", files, err)
	}
	brokenVersions := &fakeP9Versions{err: errors.New("db down")}
	provider = newP9SourceProviderWithGit(git, brokenVersions)
	if _, err := provider.SourceFiles(context.Background(), 1001); err == nil {
		t.Fatal("SourceFiles must propagate a version read error")
	}
	if _, err := newP9SourceProviderWithGit(nil, nil).SourceFiles(context.Background(), 1001); err == nil {
		t.Fatal("SourceFiles must fail closed without a version reader")
	}
}

// TestP9SourceProviderFallsBackToWorkingTreeWithoutGit covers a bound SUT
// source directory that is not a git repository (the 墨栈博客 acceptance SUT).
// The git reader fails, so the provider must read the bounded working tree
// instead of silently returning no files and therefore no API facts.
func TestP9SourceProviderFallsBackToWorkingTreeWithoutGit(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write("blog-backend/internal/server/routes.go", "package server\n\nfunc Register(r *gin.Engine) {\n\tr.GET(\"/api/v1/posts\", listPosts)\n}\n")
	write("blog-backend/vendor/ignored.go", "package ignored\n")
	write("blog-backend/node_modules/ignored.go", "package ignored\n")
	write("blog-frontend/src/api/client.ts", "export const list = () => request('/api/v1/posts')\n")
	write("blog-backend/readme.md", "# docs\n")
	write("blog-backend/huge.go", strings.Repeat("x", int(codeintelapp.DefaultStaticMaxFileBytes)+1))
	write("blog-backend/binary.go", "package bin\x00\x01\x02\n")

	git := &fakeP9SourceGit{
		listErr: map[string]error{root: errors.New("git: not a repository")},
	}
	view := versionapp.VersionView{
		ID: 1002,
		Bindings: []versionapp.BindingView{{ID: 79, Repository: &resourceapp.CodeRepositoryView{
			ID: "1301", Name: "墨栈博客", Path: root, Branch: "master",
			Include: []string{"**/*.go"}, Exclude: []string{"**/vendor/**", "**/node_modules/**"},
		}}},
	}
	provider := newP9SourceProviderWithGit(git, &fakeP9Versions{view: view})
	files, err := provider.SourceFiles(context.Background(), 1002)
	if err != nil {
		t.Fatalf("SourceFiles: %v", err)
	}
	if len(files) != 1 || files[0].Path != "blog-backend/internal/server/routes.go" {
		t.Fatalf("working-tree files = %+v, want only the route source", files)
	}
	if !strings.Contains(files[0].Content, "r.GET") {
		t.Fatalf("content = %q", files[0].Content)
	}

	// Without an include filter the same fallback also returns frontend source
	// and skips binary/large/vendor files.
	view.Bindings[0].Repository.Include = nil
	view.Bindings[0].Repository.Exclude = nil
	files, err = provider.SourceFiles(context.Background(), 1002)
	if err != nil {
		t.Fatalf("SourceFiles without filters: %v", err)
	}
	paths := map[string]bool{}
	for _, file := range files {
		paths[file.Path] = true
	}
	if !paths["blog-backend/internal/server/routes.go"] || !paths["blog-frontend/src/api/client.ts"] || !paths["blog-backend/readme.md"] {
		t.Fatalf("unfiltered working-tree files = %v", paths)
	}
	for _, skipped := range []string{"blog-backend/vendor/ignored.go", "blog-backend/node_modules/ignored.go", "blog-backend/huge.go", "blog-backend/binary.go"} {
		if paths[skipped] {
			t.Fatalf("fallback read %q (must stay bounded)", skipped)
		}
	}
}
