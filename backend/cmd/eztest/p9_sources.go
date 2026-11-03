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
		if commit == "" {
			continue
		}
		remaining := codeintelapp.DefaultStaticMaxFiles - len(files)
		tree, treeErr := p.git.ListTree(ctx, repoPath, commit, repository.Include, repository.Exclude, remaining)
		if treeErr != nil {
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

var _ exploreapp.SourceProvider = (*p9SourceProvider)(nil)
