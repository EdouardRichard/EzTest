package application

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

const (
	// defaultMaxDiffBytes mirrors EZTEST_CODEINTEL_MAX_DIFF_BYTES (§4.1).
	defaultMaxDiffBytes = 256 << 10
	// hardMaxDiffBytes is the documented upper bound of the diff size.
	hardMaxDiffBytes = 1 << 20
	// minMaxDiffBytes is the documented lower bound of the diff size.
	minMaxDiffBytes = 4 << 10

	// defaultEntityPageSize and maxEntityPageSize bound the facts/search pages.
	defaultEntityPageSize = 100
	maxEntityPageSize     = domain.MaxTreeEntriesPerPage

	// auditActionCodeRead is the codeintel read audit action (design spec
	// §6.10). Audit failures never block the read.
	auditActionCodeRead = "code.read"
)

// queryCommitHashPattern matches the lowercase 40-hex commit ids the DDL and the
// explicit-head contract require (design spec §6.1). It is deliberately a
// query-local copy of the analysis-service rule so the read path does not depend
// on the analysis service's declarations.
var queryCommitHashPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// QueryConfig bounds the read-only query use case. Zero (or out-of-range)
// values fall back to the design-spec §4.2 defaults; configured values may only
// lower the hard domain caps.
type QueryConfig struct {
	// MaxTreeEntries bounds one tree page (default 200, hard cap 200).
	MaxTreeEntries int
	// MaxReadLines bounds one file line window (default 400, hard cap 400).
	MaxReadLines int
	// MaxDiffBytes bounds one unified diff (default 256 KiB, 4 KiB–1 MiB).
	MaxDiffBytes int
	// MaxChangedFiles bounds one diff page (default/hard cap 2000).
	MaxChangedFiles int
}

// QueryServiceDeps are the ports of the read-only code intelligence queries.
// ChangeSets serves the §7.3 change-preview reuse lookup (an already persisted
// deterministic change set contributes its id; the preview never writes) and
// Audit stays injectable for the code.read entries (nil disables auditing).
type QueryServiceDeps struct {
	Git        GitReader
	Repos      RepoCatalog
	Entities   domain.EntityRepository
	ChangeSets domain.ChangeSetRepository
	Versions   VersionCatalog
	Audit      AuditRecorder
	Config     QueryConfig
}

// QueryService implements the read-only T0 queries of design spec §7.2/§7.3:
// live Git tree/file/diff reads plus facts/search entity queries and the
// version-scoped change preview.
type QueryService struct {
	deps QueryServiceDeps
	cfg  QueryConfig
}

// NewQueryService wires the query use case and normalises QueryConfig.
func NewQueryService(deps QueryServiceDeps) *QueryService {
	deps.Config = normalizeQueryConfig(deps.Config)
	return &QueryService{deps: deps, cfg: deps.Config}
}

// normalizeQueryConfig applies the design-spec bounds.
func normalizeQueryConfig(cfg QueryConfig) QueryConfig {
	if cfg.MaxTreeEntries <= 0 || cfg.MaxTreeEntries > domain.MaxTreeEntriesPerPage {
		cfg.MaxTreeEntries = domain.MaxTreeEntriesPerPage
	}
	if cfg.MaxReadLines <= 0 || cfg.MaxReadLines > domain.MaxReadLines {
		cfg.MaxReadLines = domain.MaxReadLines
	}
	if cfg.MaxDiffBytes <= 0 {
		cfg.MaxDiffBytes = defaultMaxDiffBytes
	}
	if cfg.MaxDiffBytes < minMaxDiffBytes {
		cfg.MaxDiffBytes = minMaxDiffBytes
	}
	if cfg.MaxDiffBytes > hardMaxDiffBytes {
		cfg.MaxDiffBytes = hardMaxDiffBytes
	}
	if cfg.MaxChangedFiles <= 0 || cfg.MaxChangedFiles > domain.MaxChangedFilesStored {
		cfg.MaxChangedFiles = domain.MaxChangedFilesStored
	}
	return cfg
}

// Tree returns one page of the commit-pinned file tree. An empty commit means
// the repository's fixed branch tip; the repository include/exclude globs are
// forwarded unchanged (design spec §6.1).
func (s *QueryService) Tree(ctx context.Context, repoID int64, prefix, commit, cursor string, limit int) (domain.TreePage, error) {
	repo, err := s.readRepo(ctx, repoID)
	if err != nil {
		return domain.TreePage{}, err
	}
	commit, err = s.resolveCommit(ctx, repo, commit)
	if err != nil {
		return domain.TreePage{}, err
	}
	limit = clampPageLimit(limit, s.cfg.MaxTreeEntries, s.cfg.MaxTreeEntries)
	return s.deps.Git.Tree(ctx, repo.LocalPath, commit, prefix, repo.IncludeGlobs, repo.ExcludeGlobs, cursor, limit)
}

// File returns one line window of a commit-pinned text file and records the
// code.read audit entry. An empty commit means the fixed branch tip. The
// requested window is bounded by the configured line cap and reported as
// truncated when the file continues past it.
func (s *QueryService) File(ctx context.Context, actorID, repoID int64, commit, path string, startLine, endLine int) (domain.FileWindow, error) {
	repo, err := s.readRepo(ctx, repoID)
	if err != nil {
		return domain.FileWindow{}, err
	}
	commit, err = s.resolveCommit(ctx, repo, commit)
	if err != nil {
		return domain.FileWindow{}, err
	}
	startLine, endLine, bounded := s.boundFileWindow(startLine, endLine)
	window, err := s.deps.Git.File(ctx, repo.LocalPath, commit, path, startLine, endLine)
	if err != nil {
		return domain.FileWindow{}, err
	}
	if bounded && window.TotalLines > window.EndLine {
		window.Truncated = true
	}
	s.recordReadAudit(ctx, actorID, repoID, window.Commit, window.Path)
	return window, nil
}

// Diff returns one page of the deterministic file-level change list. It is a
// read-only preview: the change set is never persisted, so ChangeSetID stays
// nil. The cursor is the last path of the previous page.
func (s *QueryService) Diff(ctx context.Context, repoID int64, base, head string, limit int, cursor string) (domain.ChangeSetData, error) {
	repo, err := s.readRepo(ctx, repoID)
	if err != nil {
		return domain.ChangeSetData{}, err
	}
	head, err = s.resolveCommit(ctx, repo, head)
	if err != nil {
		return domain.ChangeSetData{}, err
	}
	limit = clampPageLimit(limit, s.cfg.MaxChangedFiles, s.cfg.MaxChangedFiles)
	data, err := s.deps.Git.Diff(ctx, repo.LocalPath, strings.TrimSpace(base), head)
	if err != nil {
		return domain.ChangeSetData{}, err
	}
	files, more := paginateChangedFiles(data.Files, cursor, limit)
	data.Files = files
	data.Truncated = data.Truncated || more
	data.ChangeSetID = nil
	return data, nil
}

// FileDiff returns the unified diff of one changed file, bounded by the
// configured diff size. Binary and deleted files come back with an empty patch
// (200, not an error) as the UI contract expects.
func (s *QueryService) FileDiff(ctx context.Context, repoID int64, base, head, path string) (domain.FileDiff, error) {
	repo, err := s.readRepo(ctx, repoID)
	if err != nil {
		return domain.FileDiff{}, err
	}
	head, err = s.resolveCommit(ctx, repo, head)
	if err != nil {
		return domain.FileDiff{}, err
	}
	return s.deps.Git.FileDiff(ctx, repo.LocalPath, strings.TrimSpace(base), head, path, s.cfg.MaxDiffBytes)
}

// Facts returns one page of code entities (T0 file facts and AI facts) at a
// commit. An empty filter commit means the fixed branch tip; an explicit one
// must be a lowercase 40-hex commit that exists in the repository.
func (s *QueryService) Facts(ctx context.Context, repoID int64, f domain.EntityFilter) ([]domain.CodeEntity, string, bool, error) {
	repo, err := s.readRepo(ctx, repoID)
	if err != nil {
		return nil, "", false, err
	}
	f.Commit, err = s.resolveEntityCommit(ctx, repo, f.Commit)
	if err != nil {
		return nil, "", false, err
	}
	f.Limit = clampPageLimit(f.Limit, defaultEntityPageSize, maxEntityPageSize)
	return s.deps.Entities.List(ctx, repoID, f)
}

// Search returns one page of entities whose path or name contains q (a
// substring search, not full text). The explicit commit is validated exactly
// like the other persisted reads and the read is audited as code.read for the
// authenticated actor (design spec §6.10, R32).
func (s *QueryService) Search(ctx context.Context, actorID, repoID int64, commit, q, kind string, limit int, cursor string) ([]domain.CodeEntity, string, bool, error) {
	repo, err := s.readRepo(ctx, repoID)
	if err != nil {
		return nil, "", false, err
	}
	commit, err = s.resolveEntityCommit(ctx, repo, commit)
	if err != nil {
		return nil, "", false, err
	}
	limit = clampPageLimit(limit, defaultEntityPageSize, maxEntityPageSize)
	entities, nextCursor, more, err := s.deps.Entities.List(ctx, repoID, domain.EntityFilter{
		Commit: commit,
		Kind:   kind,
		Query:  q,
		Limit:  limit,
		Cursor: cursor,
	})
	if err != nil {
		return nil, "", false, err
	}
	s.recordReadAudit(ctx, actorID, repoID, commit, "")
	return entities, nextCursor, more, nil
}

// Commit returns the read-only metadata of one commit (design spec §7.2). The
// sha must be a full lowercase 40-hex commit id; a malformed one is the
// documented 422 invalid_head before Git is touched. Only active repositories
// are served. Unknown commits surface as the adapter's commit_not_found.
func (s *QueryService) Commit(ctx context.Context, repoID int64, sha string) (domain.CommitInfo, error) {
	repo, err := s.readRepo(ctx, repoID)
	if err != nil {
		return domain.CommitInfo{}, err
	}
	sha = strings.TrimSpace(sha)
	if !queryCommitHashPattern.MatchString(sha) {
		return domain.CommitInfo{}, domain.NewInvalid("sha", "invalid_head",
			"codeintel: commit sha must be a lowercase 40-hex commit id")
	}
	return s.deps.Git.Commit(ctx, repo.LocalPath, sha)
}

// VersionChanges computes the version-scoped change preview (design spec §7.3):
// base is always the version's bound baseline commit; a missing or malformed
// baseline is the documented 422 baseline_commit_missing. An explicit head must
// be a lowercase 40-hex commit reachable from the baseline (git is-ancestor) or it
// is rejected with the documented 422 invalid_head; an empty head resolves the
// repository's fixed branch tip live. Nothing is persisted: when a deterministic
// change set for (repo, base, head) already exists its id is attached, otherwise
// ChangeSetID stays nil as §7.3 allows.
func (s *QueryService) VersionChanges(ctx context.Context, versionID int64, headCommit string) (domain.ChangeSetData, string, string, error) {
	info, err := s.deps.Versions.Get(ctx, versionID)
	if err != nil {
		return domain.ChangeSetData{}, "", "", err
	}
	if !info.HasRepo || info.RepoID <= 0 {
		return domain.ChangeSetData{}, "", "", domain.NewInvalid("versionId", "code_repository_not_bound",
			fmt.Sprintf("codeintel: version %d has no code repository binding", versionID))
	}
	base := strings.TrimSpace(info.BaselineCommit)
	if base == "" || !queryCommitHashPattern.MatchString(base) {
		// A malformed baseline is the same missing-baseline verdict as the
		// analysis trigger (AnalysisService.Start), never a commit_not_found
		// leaking out of the diff.
		return domain.ChangeSetData{}, "", "", domain.NewInvalid("versionId", "baseline_commit_missing",
			fmt.Sprintf("codeintel: version %d has no valid code baseline commit", versionID))
	}

	repo, err := s.readRepo(ctx, info.RepoID)
	if err != nil {
		return domain.ChangeSetData{}, "", "", err
	}
	head, err := s.resolveVersionHead(ctx, repo, base, headCommit)
	if err != nil {
		return domain.ChangeSetData{}, "", "", err
	}
	data, err := s.deps.Git.Diff(ctx, repo.LocalPath, base, head)
	if err != nil {
		return domain.ChangeSetData{}, "", "", err
	}
	changeSetID, err := s.storedChangeSetID(ctx, repo.ID, base, head)
	if err != nil {
		return domain.ChangeSetData{}, "", "", err
	}
	data.ChangeSetID = changeSetID
	return data, base, head, nil
}

// resolveVersionHead validates an explicit change-preview head (40 hex and a
// baseline descendant) or resolves the repository's fixed branch tip live. The
// version binding branch is not used: §6.1/§7.3 always default to the P2 fixed
// branch, never a stale cached commit.
func (s *QueryService) resolveVersionHead(ctx context.Context, repo RepoInfo, base, headCommit string) (string, error) {
	head := strings.TrimSpace(headCommit)
	if head == "" {
		return s.deps.Git.Head(ctx, repo.LocalPath, repo.FixedBranch)
	}
	if !queryCommitHashPattern.MatchString(head) {
		return "", domain.NewInvalid("headCommit", "invalid_head",
			"codeintel: headCommit must be a lowercase 40-hex commit id")
	}
	ancestor, err := s.deps.Git.IsAncestor(ctx, repo.LocalPath, base, head)
	if err != nil {
		return "", err
	}
	if !ancestor {
		return "", domain.NewInvalid("headCommit", "invalid_head",
			fmt.Sprintf("codeintel: commit %s is not a descendant of the baseline %s", head, base))
	}
	return head, nil
}

// storedChangeSetID returns the id of an already persisted deterministic change
// set for (repo, base, head), or nil when the preview has not been stored yet
// (design spec §7.3 allows changeSetId=null). A missing change-set repository or
// a not-found row is not an error for this read; operational failures surface.
func (s *QueryService) storedChangeSetID(ctx context.Context, repoID int64, base, head string) (*int64, error) {
	if s.deps.ChangeSets == nil {
		return nil, nil
	}
	stored, err := s.deps.ChangeSets.FindByCommits(ctx, repoID, base, head)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	id := stored.ID
	return &id, nil
}

// readRepo loads the repository and rejects a disabled one. P4 reads are only
// served for active local repositories (design spec §7.1).
func (s *QueryService) readRepo(ctx context.Context, repoID int64) (RepoInfo, error) {
	repo, err := s.deps.Repos.Get(ctx, repoID)
	if err != nil {
		return RepoInfo{}, err
	}
	if repo.Status != codeRepositoryActive {
		return RepoInfo{}, domain.NewInvalid("repoId", "code_repository_disabled",
			fmt.Sprintf("codeintel: code repository %d is disabled", repoID))
	}
	return repo, nil
}

// resolveCommit defaults an empty commit to the repository's fixed branch tip.
// The tip is resolved live on every call; the stale P2 last_commit cache is
// never trusted (design spec §6.1).
func (s *QueryService) resolveCommit(ctx context.Context, repo RepoInfo, commit string) (string, error) {
	commit = strings.TrimSpace(commit)
	if commit != "" {
		return commit, nil
	}
	return s.deps.Git.Head(ctx, repo.LocalPath, repo.FixedBranch)
}

// resolveEntityCommit resolves the commit of a persisted facts/search read. An
// omitted commit keeps the fixed-branch-tip default; an explicit one must be a
// lowercase 40-hex commit that exists in the repository (read through the same
// Git port as the commit metadata endpoint). Without this check a malformed or
// unknown commit reached the SQL layer and silently returned an empty page
// instead of the documented 422 (design spec §7.1 invalid_head/commit_not_found).
func (s *QueryService) resolveEntityCommit(ctx context.Context, repo RepoInfo, commit string) (string, error) {
	commit = strings.TrimSpace(commit)
	if commit == "" {
		return s.resolveCommit(ctx, repo, "")
	}
	if !queryCommitHashPattern.MatchString(commit) {
		return "", domain.NewInvalid("commit", "invalid_head",
			"codeintel: commit must be a lowercase 40-hex commit id")
	}
	if _, err := s.deps.Git.Commit(ctx, repo.LocalPath, commit); err != nil {
		return "", err
	}
	return commit, nil
}

// recordReadAudit records one code.read entry. Audit failures never block the
// read (design spec §6.10) and the detail map carries identifiers only.
func (s *QueryService) recordReadAudit(ctx context.Context, actorID, repoID int64, commit, path string) {
	if s.deps.Audit == nil {
		return
	}
	detail := map[string]any{"repoId": repoID}
	if commit != "" {
		detail["commit"] = commit
	}
	if path != "" {
		detail["path"] = path
	}
	_ = s.deps.Audit.Record(ctx, actorID, auditActionCodeRead,
		fmt.Sprintf("code_repository:%d", repoID), detail)
}

// clampPageLimit applies the default for a missing limit and the hard page cap.
func clampPageLimit(limit, fallback, max int) int {
	if limit <= 0 {
		limit = fallback
	}
	if limit > max {
		limit = max
	}
	return limit
}

// boundFileWindow applies the configured line cap to one requested window. The
// bool reports that the request was cut, so a file continuing past the window is
// marked truncated.
func (s *QueryService) boundFileWindow(startLine, endLine int) (int, int, bool) {
	if s.cfg.MaxReadLines <= 0 {
		return startLine, endLine, false
	}
	effectiveStart := startLine
	if effectiveStart <= 0 {
		effectiveStart = 1
	}
	maxEnd := effectiveStart + s.cfg.MaxReadLines - 1
	if endLine > 0 && endLine <= maxEnd {
		return startLine, endLine, false
	}
	return startLine, maxEnd, true
}

// paginateChangedFiles slices one deterministic change-list page. The cursor is
// the last path of the previous page; the returned bool reports that more files
// remain after the page.
func paginateChangedFiles(files []domain.ChangedFile, cursor string, limit int) ([]domain.ChangedFile, bool) {
	if cursor != "" {
		offset := len(files)
		for index, file := range files {
			if file.Path > cursor {
				offset = index
				break
			}
		}
		files = files[offset:]
	}
	if limit > 0 && len(files) > limit {
		return files[:limit], true
	}
	return files, false
}
