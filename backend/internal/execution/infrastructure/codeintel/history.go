// Package codeintel adapts the codeintel application services to the execution
// autonomous worker's P4 ports (spec §6.1/§6.2). ImpactPort triggers and awaits
// impact analyses; CodeHistory resolves the D49 base/head commits of a batch.
// Both live here because they read the same P4/P2 projections and never touch
// codeintel tables directly.
package codeintel

import (
	"context"
	"errors"
	"fmt"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
)

// historyRepoActive mirrors the P2 code repository lifecycle value P4 reads
// require (design spec §7.1): a disabled repository cannot be analysed or
// pinned as a batch head.
const historyRepoActive = "active"

// CodeHistory implements execution/application.CodeHistoryPort (D49, spec
// §6.8/§8.9) over the codeintel application catalog and the read-only Git
// reader. It resolves the version binding through the codeintel VersionCatalog,
// the checkout through the P2 RepoCatalog and the commit through Git, so no
// other context's tables or infrastructure are reached.
type CodeHistory struct {
	versions codeintelapp.VersionCatalog
	repos    codeintelapp.RepoCatalog
	git      codeintelapp.GitReader
}

// NewCodeHistory wires the D49 commit surface. All three ports are the same
// production instances P4 uses; a nil port degrades every call to a
// configuration error, never a panic, so a partially wired process can still
// create batches.
func NewCodeHistory(versions codeintelapp.VersionCatalog, repos codeintelapp.RepoCatalog, git codeintelapp.GitReader) *CodeHistory {
	return &CodeHistory{versions: versions, repos: repos, git: git}
}

var _ executionapp.CodeHistoryPort = (*CodeHistory)(nil)

// HeadCommit returns the current tip of the version's repository fixed branch,
// which is the D49 "建批时固定分支 HEAD" value (spec §6.8). The version's
// code_branch is display metadata and is deliberately not used: P4 resolves the
// same fixed branch (analysis_service/query_service).
func (h *CodeHistory) HeadCommit(ctx context.Context, versionID int64) (string, error) {
	repo, err := h.repository(ctx, versionID)
	if err != nil {
		return "", err
	}
	return h.git.Head(ctx, repo.LocalPath, repo.FixedBranch)
}

// IsAncestor reports whether base is an ancestor of head in the version's
// repository. BatchService uses it to detect a rebase/force-push and fall back
// to the version baseline with history_rewritten (D49).
func (h *CodeHistory) IsAncestor(ctx context.Context, versionID int64, base, head string) (bool, error) {
	repo, err := h.repository(ctx, versionID)
	if err != nil {
		return false, err
	}
	return h.git.IsAncestor(ctx, repo.LocalPath, base, head)
}

// repository resolves the active P2 checkout of versionID's code repository.
// A missing binding is an error the caller degrades on: without a code
// repository there is no commit to pin or range to check (spec §6.1 "无代码仓").
func (h *CodeHistory) repository(ctx context.Context, versionID int64) (codeintelapp.RepoInfo, error) {
	if h == nil || h.versions == nil || h.repos == nil || h.git == nil {
		return codeintelapp.RepoInfo{}, errors.New("execution codeintel: code history is not configured")
	}
	info, err := h.versions.Get(ctx, versionID)
	if err != nil {
		return codeintelapp.RepoInfo{}, err
	}
	if !info.HasRepo || info.RepoID <= 0 {
		return codeintelapp.RepoInfo{}, fmt.Errorf(
			"execution codeintel: version %d has no code repository binding", versionID)
	}
	repo, err := h.repos.Get(ctx, info.RepoID)
	if err != nil {
		return codeintelapp.RepoInfo{}, err
	}
	if repo.Status != historyRepoActive {
		return codeintelapp.RepoInfo{}, fmt.Errorf(
			"execution codeintel: code repository %d is %s", repo.ID, repo.Status)
	}
	return repo, nil
}
