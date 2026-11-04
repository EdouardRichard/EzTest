package main

// Task 37 fix round 1: production SourceProvider for the static code-first API
// discovery. It reads the text of the repositories bound to the version's SUT
// bindings through the read-only codeintel git client (never the network), and
// is bounded exactly like the codeintel static extraction (file count, per-file
// bytes). Repository bodies stay in memory for the scan and are never persisted;
// APIDiscovery keeps only endpoint metadata.

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	exploreapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/explore"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/versionadapter"
	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/gitx"
)

// p9SourceGit is the read-only git surface the source provider consumes
// (*gitx.Git satisfies it). It is an interface so the provider is testable
// without a real repository.
type p9SourceGit interface {
	ListTree(ctx context.Context, repoPath, commit string, include, exclude []string, max int) (codeintel.TreeResult, error)
	ReadFileWindow(ctx context.Context, repoPath, commit, filePath string, maxBytes, maxLines int) (codeintel.FileWindow, error)
}

// p9SourceProvider reads the repository text files of a version's bound code
// repositories. A repository that cannot be read is skipped: the API scan is a
// best-effort augmentation, not a hard dependency of the analysis.
type p9SourceProvider struct {
	git      p9SourceGit
	versions versionadapter.VersionService
}

// newP9SourceProvider builds the production source provider over the read-only
// git client.
func newP9SourceProvider(versions versionadapter.VersionService) *p9SourceProvider {
	return newP9SourceProviderWithGit(gitx.New(nil, 0), versions)
}

// newP9SourceProviderWithGit allows tests to inject the read-only git surface.
func newP9SourceProviderWithGit(git p9SourceGit, versions versionadapter.VersionService) *p9SourceProvider {
	return &p9SourceProvider{git: git, versions: versions}
}

// SourceFiles implements exploreapp.SourceProvider.
func (p *p9SourceProvider) SourceFiles(ctx context.Context, versionID int64) ([]codeintelapp.SourceFile, error) {
	if p == nil || p.versions == nil {
		return nil, errors.New("p9 source provider: version binding reader is not configured")
	}
	if p.git == nil {
		return nil, errors.New("p9 source provider: git reader is not configured")
	}
	if versionID <= 0 {
		return nil, errors.New("p9 source provider: invalid version id")
	}
	view, err := p.versions.Get(ctx, versionID)
	if err != nil {
		return nil, err
	}
	files := make([]codeintelapp.SourceFile, 0)
	for _, binding := range view.Bindings {
		if len(files) >= codeintelapp.DefaultStaticMaxFiles {
			break
		}
		repository := binding.Repository
		if repository == nil {
			continue
		}
		repoPath := strings.TrimSpace(repository.Path)
		if repoPath == "" {
			continue
		}
		// The analyzed baseline is the pinned commit; an unresolved baseline
		// falls back to the repository branch so a freshly created version can
		// still scan before the codeintel index runs.
		commit := strings.TrimSpace(binding.CodeBaselineCommit)
		if commit == "" {
			commit = strings.TrimSpace(repository.Branch)
		}
		remaining := codeintelapp.DefaultStaticMaxFiles - len(files)
		if commit == "" {
			// A source directory without git metadata still has readable code:
			// scan the bounded working tree instead of silently writing no API
			// facts.
			files = append(files, p.workingTreeFiles(repoPath, repository.Include, repository.Exclude, remaining)...)
			continue
		}
		tree, treeErr := p.git.ListTree(ctx, repoPath, commit, repository.Include, repository.Exclude, remaining)
		if treeErr != nil {
			// Many acceptance/SUT source directories are plain deployment
			// checkouts without a .git directory. The git reader is the pinned
			// path; the bounded working-tree read is the read-only fallback.
			files = append(files, p.workingTreeFiles(repoPath, repository.Include, repository.Exclude, remaining)...)
			continue
		}
		for _, entry := range tree.Entries {
			if len(files) >= codeintelapp.DefaultStaticMaxFiles {
				break
			}
			if entry.Unsupported || entry.Binary || entry.Size > codeintelapp.DefaultStaticMaxFileBytes {
				continue
			}
			window, readErr := p.git.ReadFileWindow(ctx, repoPath, commit, entry.Path, codeintelapp.DefaultStaticMaxFileBytes, 0)
			if readErr != nil {
				continue
			}
			files = append(files, codeintelapp.SourceFile{
				Path:    entry.Path,
				Content: strings.Join(window.Lines, "\n"),
			})
		}
	}
	return files, nil
}

// p9WorkingTreeSkipDirs are dependency/build/output directories whose files are
// never SUT business source. Hidden directories (including .git and vendored
// toolchains) are skipped by the leading-dot rule.
var p9WorkingTreeSkipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "dist": true, "build": true,
	"bin": true, "target": true, "coverage": true, "uploads": true,
}

// workingTreeFiles reads the bounded text files of a non-git source directory.
// It applies the configured include/exclude globs, skips dependency/build
// directories, symlinks, oversized files and binary content, and never leaves
// the repository root. The static API extractor consumes the result; no file is
// executed and no path is persisted.
func (p *p9SourceProvider) workingTreeFiles(repoPath string, include, exclude []string, maxFiles int) []codeintelapp.SourceFile {
	if maxFiles <= 0 {
		return nil
	}
	root, err := filepath.Abs(strings.TrimSpace(repoPath))
	if err != nil {
		return nil
	}
	files := make([]codeintelapp.SourceFile, 0)
	_ = filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if len(files) >= maxFiles {
			return fs.SkipAll
		}
		if entry.IsDir() {
			if current == root {
				return nil
			}
			name := strings.ToLower(entry.Name())
			if strings.HasPrefix(name, ".") || p9WorkingTreeSkipDirs[name] {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return nil
		}
		relative = filepath.ToSlash(relative)
		if len(include) > 0 && !p9AnyGlob(include, relative) {
			return nil
		}
		if p9AnyGlob(exclude, relative) {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.Size() > int64(codeintelapp.DefaultStaticMaxFileBytes) {
			return nil
		}
		content, err := os.ReadFile(current)
		if err != nil || p9LooksBinary(content) {
			return nil
		}
		files = append(files, codeintelapp.SourceFile{Path: relative, Content: string(content)})
		return nil
	})
	return files
}

func p9LooksBinary(content []byte) bool {
	window := content
	if len(window) > 8192 {
		window = window[:8192]
	}
	for _, value := range window {
		if value == 0 {
			return true
		}
	}
	return false
}

func p9AnyGlob(patterns []string, filePath string) bool {
	for _, pattern := range patterns {
		if p9GlobMatch(pattern, filePath) {
			return true
		}
	}
	return false
}

// p9GlobMatch matches the resource include/exclude glob style. A leading "**/"
// matches at any depth, a trailing "/**" matches the whole subtree, and plain
// patterns are matched against the full relative path or any path suffix.
func p9GlobMatch(pattern, filePath string) bool {
	pattern = strings.TrimPrefix(strings.ReplaceAll(strings.TrimSpace(pattern), "\\", "/"), "**/")
	filePath = strings.ReplaceAll(filePath, "\\", "/")
	if pattern == "" {
		return false
	}
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return filePath == prefix || strings.HasPrefix(filePath, prefix+"/")
	}
	if matched, _ := path.Match(pattern, filePath); matched {
		return true
	}
	parts := strings.Split(filePath, "/")
	for index := 1; index < len(parts); index++ {
		if matched, _ := path.Match(pattern, strings.Join(parts[index:], "/")); matched {
			return true
		}
	}
	return false
}

var _ exploreapp.SourceProvider = (*p9SourceProvider)(nil)
