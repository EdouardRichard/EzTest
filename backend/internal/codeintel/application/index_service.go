package codeintelapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
)

// Work types and team (contracts/task-types.md §7).
const (
	WorkTypeIndexRepo   = "index.repo"
	WorkTypeCodeAnalyze = "code.analyze"
	CodeTeam            = "code"
	codePriority        = 50
	codeMaxAttempts     = 3
)

// Settings keys and defaults (spec §4.5). Settings may lower a hard domain
// limit but never raise it.
const (
	SettingMaxIndexFiles   = "codeintel.max_index_files"
	SettingMaxChangedFiles = "codeintel.max_changed_files"
	SettingMaxFileBytes    = "codeintel.max_file_bytes"
	SettingMaxDiffBytes    = "codeintel.max_diff_bytes"
	SettingMaxExcerptLines = "codeintel.max_excerpt_lines"
	SettingAnalysisTimeout = "codeintel.analysis_timeout_seconds"
	SettingMaxConcurrency  = "codeintel.max_concurrent_analyses"
	SettingPromptVersion   = "codeintel.prompt_version"
	SettingExcludeGlobs    = "codeintel.exclude_globs"

	defaultMaxIndexFiles   = 5000
	hardMaxIndexFiles      = 50000
	defaultMaxChangedFiles = 20
	defaultMaxFileBytes    = 65536
	defaultMaxDiffBytes    = 262144
	defaultMaxExcerptLines = 40
	defaultPromptVersion   = "p5.1"
	defaultAnalysisTimeout = 180
)

var commitSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ValidCommitSHA reports whether value is a 40-character lowercase hex commit.
func ValidCommitSHA(value string) bool { return commitSHAPattern.MatchString(value) }

// IndexRepository persists code_index_run rows. ActiveRun returns the single
// queued/running run of a repository (partial unique index); DoneRunByCommit
// backs the 200-reused idempotency rule.
type IndexRepository interface {
	CreateRun(ctx context.Context, run codeintel.IndexRun) (codeintel.IndexRun, error)
	GetRun(ctx context.Context, id int64) (codeintel.IndexRun, error)
	LatestRun(ctx context.Context, repoID int64) (codeintel.IndexRun, error)
	UpdateRun(ctx context.Context, run codeintel.IndexRun) error
	ActiveRun(ctx context.Context, repoID int64) (codeintel.IndexRun, bool, error)
	DoneRunByCommit(ctx context.Context, repoID int64, commitSHA string) (codeintel.IndexRun, bool, error)
}

// EntityRepository is the current code_entity projection. UpsertFileEntities
// replaces the (repo, commit) file projection idempotently.
type EntityRepository interface {
	UpsertFileEntities(ctx context.Context, runID, repoID int64, commitSHA string, entries []codeintel.TreeEntry) error
	ListTree(ctx context.Context, repoID int64, commitSHA, pathPrefix, cursor string, limit int) ([]codeintel.Entity, string, bool, error)
	Search(ctx context.Context, repoID int64, commitSHA, query, kind string, limit int) ([]codeintel.Entity, error)
	EntitiesByFactKeys(ctx context.Context, repoID int64, commitSHA string, factKeys []string) ([]codeintel.Entity, error)
	UpsertFactEntity(ctx context.Context, entity codeintel.Entity) (codeintel.Entity, error)
	UpsertEdge(ctx context.Context, edge codeintel.Edge) error
}

// RepoStatus is one bound repository's index projection.
type RepoStatus struct {
	RepoID          int64
	Repository      RepoRef
	Latest          *codeintel.IndexRun
	BaselineCommit  string
	IndexInProgress bool
}

// IndexDeps is the T0 service wiring.
type IndexDeps struct {
	Runs       IndexRepository
	Entities   EntityRepository
	Git        GitClient
	Versions   VersionReader
	Resources  ResourceReader
	Queue      WorkQueue
	Events     EventPublisher
	Audit      AuditRecorder
	Settings   SettingsReader
	Clock      Clock
	IDs        IDGenerator
	InstanceID string
}

// IndexService owns T0 index runs: Start validates the version binding and
// enqueues index.repo; Execute performs the deterministic git index.
type IndexService struct {
	runs      IndexRepository
	entities  EntityRepository
	git       GitClient
	versions  VersionReader
	resources ResourceReader
	queue     WorkQueue
	events    EventPublisher
	audit     AuditRecorder
	settings  SettingsReader
	clock     Clock
	ids       IDGenerator
}

// NewIndexService wires the T0 service with test-friendly defaults.
func NewIndexService(deps IndexDeps) *IndexService {
	return &IndexService{
		runs:      deps.Runs,
		entities:  deps.Entities,
		git:       deps.Git,
		versions:  deps.Versions,
		resources: deps.Resources,
		queue:     deps.Queue,
		events:    deps.Events,
		audit:     deps.Audit,
		settings:  deps.Settings,
		clock:     clockOrSystem(deps.Clock),
		ids:       idsOrFallback(deps.IDs),
	}
}

// Start creates (or reuses) a version-scoped T0 index run and ensures its
// index.repo work item. It returns created=false when a done run for the same
// repo+commit is reused (HTTP 200) and true for a new queued run (HTTP 202).
func (s *IndexService) Start(ctx context.Context, versionID, repoID int64, commit string) (codeintel.IndexRun, bool, error) {
	bindings, err := s.versions.VersionSUTBindings(ctx, versionID)
	if err != nil {
		return codeintel.IndexRun{}, false, err
	}
	binding, ok := bindingForRepo(bindings, repoID)
	if !ok {
		return codeintel.IndexRun{}, false, unprocessable("repo_not_bound", "repository is not bound to this version")
	}
	repository, err := s.resources.CodeRepository(ctx, repoID)
	if err != nil {
		return codeintel.IndexRun{}, false, mapDomainError(err, "code repository")
	}

	runCommit := strings.TrimSpace(commit)
	switch {
	case runCommit != "":
		resolved, resolveErr := s.git.ResolveCommit(ctx, repository.Path, runCommit)
		if resolveErr != nil {
			return codeintel.IndexRun{}, false, resolveErr
		}
		runCommit = resolved
	case binding.CodeBaselineCommit != "":
		runCommit = binding.CodeBaselineCommit
	default:
		head, headErr := s.git.HeadCommit(ctx, repository.Path, repository.Branch)
		if headErr != nil {
			return codeintel.IndexRun{}, false, headErr
		}
		runCommit = head
	}

	if _, active, activeErr := s.runs.ActiveRun(ctx, repoID); activeErr != nil {
		return codeintel.IndexRun{}, false, activeErr
	} else if active {
		return codeintel.IndexRun{}, false, stateConflict("index_in_progress", "an index run is already in progress for this repository")
	}
	if existing, found, doneErr := s.runs.DoneRunByCommit(ctx, repoID, runCommit); doneErr != nil {
		return codeintel.IndexRun{}, false, doneErr
	} else if found {
		return existing, false, nil
	}

	now := s.clock.Now()
	run := codeintel.IndexRun{
		ID:        s.ids.Next(),
		VersionID: versionID,
		RepoID:    repoID,
		CommitSHA: runCommit,
		Branch:    repository.Branch,
		Status:    string(codeintel.IndexQueued),
		Stats:     json.RawMessage(`{}`),
		Errors:    json.RawMessage(`[]`),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if actor, ok := platform.ActorFrom(ctx); ok && actor.ID > 0 {
		actorID := actor.ID
		run.CreatedBy = &actorID
	}
	created, err := s.runs.CreateRun(ctx, run)
	if err != nil {
		return codeintel.IndexRun{}, false, err
	}
	payload := map[string]any{
		"repo_id":      platformid.Format(repoID),
		"commit_sha":   runCommit,
		"branch":       repository.Branch,
		"index_run_id": platformid.Format(created.ID),
	}
	if _, _, err := s.queue.Ensure(ctx, NewWorkItem{
		VersionID:   versionID,
		Team:        CodeTeam,
		Type:        WorkTypeIndexRepo,
		Priority:    codePriority,
		DedupeKey:   "code:index:" + platformid.Format(created.ID),
		Payload:     payload,
		MaxAttempts: codeMaxAttempts,
	}); err != nil {
		s.failRun(ctx, created, err)
		return codeintel.IndexRun{}, false, err
	}
	s.recordAudit(ctx, "code.index.start", "code_index_run", platformid.Format(created.ID), map[string]any{
		"version_id": platformid.Format(versionID),
		"repo_id":    platformid.Format(repoID),
		"commit_sha": runCommit,
	})
	return created, true, nil
}

// Execute performs one queued run's T0 index. It is idempotent: the file
// projection for (repo, commit) is replaced, so a retried work item does not
// duplicate entities.
func (s *IndexService) Execute(ctx context.Context, runID int64) error {
	run, err := s.runs.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	repository, err := s.resources.CodeRepository(ctx, run.RepoID)
	if err != nil {
		s.failRun(ctx, run, err)
		return mapDomainError(err, "code repository")
	}

	now := s.clock.Now()
	run.Status = string(codeintel.IndexRunning)
	run.StartedAt = &now
	run.UpdatedAt = now
	if err := s.runs.UpdateRun(ctx, run); err != nil {
		return err
	}
	s.publish(ctx, "code.index_started", run, map[string]any{
		"index_run_id": platformid.Format(run.ID),
		"repo_id":      platformid.Format(run.RepoID),
		"commit_sha":   run.CommitSHA,
		"branch":       run.Branch,
	})

	maxFiles := s.intSetting(ctx, SettingMaxIndexFiles, defaultMaxIndexFiles, hardMaxIndexFiles)
	exclude := append(append([]string{}, repository.Exclude...), s.excludeGlobs(ctx)...)
	tree, err := s.git.ListTree(ctx, repository.Path, run.CommitSHA, repository.Include, exclude, maxFiles)
	if err != nil {
		s.failRun(ctx, run, err)
		return err
	}
	if err := s.entities.UpsertFileEntities(ctx, run.ID, run.RepoID, run.CommitSHA, tree.Entries); err != nil {
		s.failRun(ctx, run, err)
		return err
	}

	finished := s.clock.Now()
	run.Status = string(codeintel.IndexDone)
	run.FilesTotal = tree.Total
	run.FilesIndexed = len(tree.Entries)
	run.Truncated = tree.Truncated
	run.Stats = mustJSON(map[string]any{"truncated": tree.Truncated, "entries_indexed": len(tree.Entries)})
	run.Errors = json.RawMessage(`[]`)
	run.FinishedAt = &finished
	run.UpdatedAt = finished
	if err := s.runs.UpdateRun(ctx, run); err != nil {
		return err
	}
	s.publish(ctx, "code.index_finished", run, map[string]any{
		"index_run_id":  platformid.Format(run.ID),
		"status":        run.Status,
		"files_total":   run.FilesTotal,
		"files_indexed": run.FilesIndexed,
		"truncated":     run.Truncated,
		"stats":         decodeObject(run.Stats),
	})
	s.backfillBaseline(ctx, run)
	return nil
}

// backfillBaseline writes version_sut.code_baseline_commit when it is still
// empty (spec §5.5 step 5). Failures are best-effort and never fail the index.
func (s *IndexService) backfillBaseline(ctx context.Context, run codeintel.IndexRun) {
	if run.VersionID <= 0 {
		return
	}
	bindings, err := s.versions.VersionSUTBindings(ctx, run.VersionID)
	if err != nil {
		return
	}
	for _, binding := range bindings {
		if binding.RepoID != run.RepoID || binding.CodeBaselineCommit != "" {
			continue
		}
		if err := s.versions.SetCodeBaselineCommit(ctx, binding.VersionSUTID, run.CommitSHA); err != nil {
			s.recordAudit(ctx, "code.index.baseline_failed", "version_sut", platformid.Format(binding.VersionSUTID), map[string]any{
				"repo_id": platformid.Format(run.RepoID),
			})
			continue
		}
		s.recordAudit(ctx, "code.index.baseline", "version_sut", platformid.Format(binding.VersionSUTID), map[string]any{
			"repo_id":    platformid.Format(run.RepoID),
			"commit_sha": run.CommitSHA,
		})
	}
}

// Latest returns the newest index run of a repository.
func (s *IndexService) Latest(ctx context.Context, repoID int64) (codeintel.IndexRun, error) {
	run, err := s.runs.LatestRun(ctx, repoID)
	if err != nil {
		return codeintel.IndexRun{}, mapDomainError(err, "code index run")
	}
	return run, nil
}

// Get returns one index run.
func (s *IndexService) Get(ctx context.Context, runID int64) (codeintel.IndexRun, error) {
	run, err := s.runs.GetRun(ctx, runID)
	if err != nil {
		return codeintel.IndexRun{}, mapDomainError(err, "code index run")
	}
	return run, nil
}

// RepoStatus lists the distinct repositories bound to a version with their
// latest index run and baseline commit.
func (s *IndexService) RepoStatus(ctx context.Context, versionID int64) ([]RepoStatus, error) {
	bindings, err := s.versions.VersionSUTBindings(ctx, versionID)
	if err != nil {
		return nil, err
	}
	seen := make(map[int64]string, len(bindings))
	order := make([]int64, 0, len(bindings))
	for _, binding := range bindings {
		if _, ok := seen[binding.RepoID]; !ok {
			order = append(order, binding.RepoID)
			seen[binding.RepoID] = binding.CodeBaselineCommit
			continue
		}
		if seen[binding.RepoID] == "" {
			seen[binding.RepoID] = binding.CodeBaselineCommit
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })

	statuses := make([]RepoStatus, 0, len(order))
	for _, repoID := range order {
		repository, err := s.resources.CodeRepository(ctx, repoID)
		if err != nil {
			return nil, mapDomainError(err, "code repository")
		}
		status := RepoStatus{RepoID: repoID, Repository: repository, BaselineCommit: seen[repoID]}
		if latest, err := s.runs.LatestRun(ctx, repoID); err == nil {
			status.Latest = &latest
		} else if !errors.Is(err, codeintel.ErrNotFound) {
			return nil, err
		}
		if _, active, err := s.runs.ActiveRun(ctx, repoID); err == nil {
			status.IndexInProgress = active
		} else {
			return nil, err
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

// Tree reads the indexed file projection of a commit with cursor pagination.
func (s *IndexService) Tree(ctx context.Context, repoID int64, commit, pathPrefix, cursor string, limit int) ([]codeintel.Entity, string, bool, error) {
	commit = strings.TrimSpace(commit)
	if !ValidCommitSHA(commit) {
		return nil, "", false, platform.Invalid("commit", "must be a 40-character lowercase hex commit SHA")
	}
	if _, err := s.resources.CodeRepository(ctx, repoID); err != nil {
		return nil, "", false, mapDomainError(err, "code repository")
	}
	if pathPrefix != "" {
		normalized, err := codeintel.NormalizeRepoPath(pathPrefix)
		if err != nil {
			return nil, "", false, unprocessable("path_escape", "path must be a normalized repo-relative path")
		}
		pathPrefix = normalized
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > codeintel.MaxTreeEntriesPerPage {
		limit = codeintel.MaxTreeEntriesPerPage
	}
	return s.entities.ListTree(ctx, repoID, commit, pathPrefix, strings.TrimSpace(cursor), limit)
}

// QueryDeps is the read-only T0 repository surface.
type QueryDeps struct {
	Entities  EntityRepository
	Git       GitClient
	Resources ResourceReader
	Audit     AuditRecorder
	Commits   CommitReader
	Bulk      FileWindowReader
	Settings  SettingsReader
}

// QueryService serves the read-only T0 APIs: file windows, changed-file lists,
// on-demand patches and commit metadata.
type QueryService struct {
	entities  EntityRepository
	git       GitClient
	resources ResourceReader
	audit     AuditRecorder
	commits   CommitReader
	settings  SettingsReader
}

// NewQueryService wires the query service.
func NewQueryService(deps QueryDeps) *QueryService {
	return &QueryService{
		entities:  deps.Entities,
		git:       deps.Git,
		resources: deps.Resources,
		audit:     deps.Audit,
		commits:   deps.Commits,
		settings:  deps.Settings,
	}
}

// File reads one audited file window. Content bytes are returned to the caller
// only; the audit entry records repo/commit/path and never the content.
func (s *QueryService) File(ctx context.Context, repoID int64, commit, filePath string, start, end int) (codeintel.FileWindow, error) {
	repository, err := s.resources.CodeRepository(ctx, repoID)
	if err != nil {
		return codeintel.FileWindow{}, mapDomainError(err, "code repository")
	}
	// Audit the read attempt before touching Git so binary/unsupported files are
	// audited too; the entry never contains file content.
	s.recordAudit(ctx, "code.file.read", "code_repository", platformid.Format(repoID), map[string]any{
		"commit_sha": strings.TrimSpace(commit),
		"path":       filePath,
	})
	window, err := s.git.ReadFile(ctx, repository.Path, commit, filePath, start, end, codeintel.MaxFileReadBytes)
	if err != nil {
		return codeintel.FileWindow{}, err
	}
	return window, nil
}

// Diff returns one path-cursor page of a bounded changed-file list.
func (s *QueryService) Diff(ctx context.Context, repoID int64, base, head, pathFilter, cursor string, limit int) (codeintel.DiffResult, string, bool, error) {
	repository, err := s.resources.CodeRepository(ctx, repoID)
	if err != nil {
		return codeintel.DiffResult{}, "", false, mapDomainError(err, "code repository")
	}
	if !ValidCommitSHA(base) {
		return codeintel.DiffResult{}, "", false, platform.Invalid("base", "must be a 40-character lowercase hex commit SHA")
	}
	if !ValidCommitSHA(head) {
		return codeintel.DiffResult{}, "", false, platform.Invalid("head", "must be a 40-character lowercase hex commit SHA")
	}
	result, err := s.git.Diff(ctx, repository.Path, base, head, codeintel.MaxChangedFilesStored)
	if err != nil {
		return codeintel.DiffResult{}, "", false, err
	}
	if pathFilter != "" {
		normalized, normalizeErr := codeintel.NormalizeRepoPath(pathFilter)
		if normalizeErr != nil {
			return codeintel.DiffResult{}, "", false, unprocessable("path_escape", "path must be a normalized repo-relative path")
		}
		pathFilter = normalized
	}
	filtered := make([]codeintel.ChangedFile, 0, len(result.Files))
	for _, file := range result.Files {
		if pathFilter != "" && file.Path != pathFilter && !strings.HasPrefix(file.Path, pathFilter+"/") {
			continue
		}
		if cursor != "" && file.Path <= cursor {
			continue
		}
		filtered = append(filtered, file)
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > codeintel.MaxChangedFilesStored {
		limit = codeintel.MaxChangedFilesStored
	}
	hasMore := len(filtered) > limit
	if hasMore {
		filtered = filtered[:limit]
	}
	nextCursor := ""
	if hasMore && len(filtered) > 0 {
		nextCursor = filtered[len(filtered)-1].Path
	}
	result.Files = filtered
	return result, nextCursor, hasMore, nil
}

// FileDiff returns one on-demand patch bounded by codeintel.max_diff_bytes.
func (s *QueryService) FileDiff(ctx context.Context, repoID int64, base, head, filePath string) (codeintel.FileDiff, error) {
	repository, err := s.resources.CodeRepository(ctx, repoID)
	if err != nil {
		return codeintel.FileDiff{}, mapDomainError(err, "code repository")
	}
	if !ValidCommitSHA(base) {
		return codeintel.FileDiff{}, platform.Invalid("base", "must be a 40-character lowercase hex commit SHA")
	}
	if !ValidCommitSHA(head) {
		return codeintel.FileDiff{}, platform.Invalid("head", "must be a 40-character lowercase hex commit SHA")
	}
	normalized, normalizeErr := codeintel.NormalizeRepoPath(filePath)
	if normalizeErr != nil {
		return codeintel.FileDiff{}, unprocessable("path_escape", "path must be a normalized repo-relative path")
	}
	maxBytes := s.intSetting(ctx, SettingMaxDiffBytes, defaultMaxDiffBytes, 1<<20)
	return s.git.FileDiff(ctx, repository.Path, base, head, normalized, maxBytes)
}

// CommitView is the /commits/{sha} read model.
type CommitView struct {
	SHA         string
	Branch      string
	Message     string
	AuthorName  string
	AuthoredAt  time.Time
	CommittedAt time.Time
	Parents     []string
	IsHead      bool
}

// Commit returns commit metadata (never file content).
func (s *QueryService) Commit(ctx context.Context, repoID int64, sha string) (CommitView, error) {
	repository, err := s.resources.CodeRepository(ctx, repoID)
	if err != nil {
		return CommitView{}, mapDomainError(err, "code repository")
	}
	sha = strings.TrimSpace(sha)
	if !ValidCommitSHA(sha) {
		return CommitView{}, platform.Invalid("sha", "must be a 40-character lowercase hex commit SHA")
	}
	if s.commits == nil {
		return CommitView{}, platform.Unavailable("commit metadata is not configured")
	}
	info, err := s.commits.Commit(ctx, repository.Path, sha, 4000)
	if err != nil {
		return CommitView{}, err
	}
	view := CommitView{
		SHA:         info.SHA,
		Branch:      repository.Branch,
		Message:     info.Message,
		AuthorName:  info.AuthorName,
		AuthoredAt:  info.AuthoredAt,
		CommittedAt: info.CommittedAt,
		Parents:     info.Parents,
	}
	if head, headErr := s.git.HeadCommit(ctx, repository.Path, repository.Branch); headErr == nil {
		view.IsHead = head == info.SHA
	}
	return view, nil
}

// --- shared helpers -------------------------------------------------------------

func clockOrSystem(value Clock) Clock {
	if value == nil {
		return clock.System{}
	}
	return value
}

func idsOrFallback(value IDGenerator) IDGenerator {
	if value != nil {
		return value
	}
	generator, err := platformid.NewSnowflake(1)
	if err != nil {
		panic(err)
	}
	return generator
}

// intSetting reads a positive integer setting capped by hardMax. A missing
// setting or a read error keeps the default; values outside 1..hardMax are
// clamped.
func (s *IndexService) intSetting(ctx context.Context, key string, fallback, hardMax int) int {
	return readIntSetting(ctx, s.settings, key, fallback, hardMax)
}

func readIntSetting(ctx context.Context, settings SettingsReader, key string, fallback, hardMax int) int {
	value := int64(fallback)
	if settings != nil {
		if stored, err := settings.Int(ctx, key); err == nil && stored > 0 {
			value = stored
		}
	}
	if value <= 0 {
		value = int64(fallback)
	}
	if value > int64(hardMax) {
		value = int64(hardMax)
	}
	return int(value)
}

func readStringSetting(ctx context.Context, settings SettingsReader, key, fallback string) string {
	if settings != nil {
		if stored, err := settings.String(ctx, key); err == nil && strings.TrimSpace(stored) != "" {
			return strings.TrimSpace(stored)
		}
	}
	return fallback
}

// excludeGlobs reads codeintel.exclude_globs, falling back to the seeded
// defaults when the setting is absent or malformed.
func (s *IndexService) excludeGlobs(ctx context.Context) []string {
	defaults := []string{"**/.git/**", "**/node_modules/**", "**/dist/**", "**/build/**", "**/vendor/**"}
	if s.settings == nil {
		return defaults
	}
	raw, err := s.settings.Raw(ctx, SettingExcludeGlobs)
	if err != nil || len(raw) == 0 {
		return defaults
	}
	var globs []string
	if err := json.Unmarshal(raw, &globs); err != nil || len(globs) == 0 {
		return defaults
	}
	return globs
}

func bindingForRepo(bindings []VersionSUTBinding, repoID int64) (VersionSUTBinding, bool) {
	for _, binding := range bindings {
		if binding.RepoID == repoID {
			return binding, true
		}
	}
	return VersionSUTBinding{}, false
}

// baselineForRepo returns the resolved baseline commit of one repository and
// rejects conflicting commits when the same repo is bound through multiple
// version_sut rows (spec §5.5: 409 repo_baseline_conflict).
func baselineForRepo(bindings []VersionSUTBinding, repoID int64) (string, error) {
	baseline := ""
	for _, binding := range bindings {
		if binding.RepoID != repoID {
			continue
		}
		commit := strings.TrimSpace(binding.CodeBaselineCommit)
		if commit == "" {
			continue
		}
		if baseline == "" {
			baseline = commit
			continue
		}
		if baseline != commit {
			return "", stateConflict("repo_baseline_conflict", "the same repository is bound with different baseline commits")
		}
	}
	return baseline, nil
}

func unprocessable(code, message string) *platform.Error {
	return platform.NewError(http.StatusUnprocessableEntity, code, message)
}

func stateConflict(reason, message string) *platform.Error {
	apiErr := platform.StateConflict(message)
	apiErr.Details = map[string]any{"reason": reason}
	return apiErr
}

// mapDomainError converts repository sentinels into HTTP-safe errors without
// leaking a row kind that could include user data.
func mapDomainError(err error, kind string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, codeintel.ErrNotFound):
		return platform.NotFound(kind)
	case errors.Is(err, codeintel.ErrConflict):
		return platform.Conflict(kind + " conflicts with the current state")
	case errors.Is(err, codeintel.ErrInvalid):
		return unprocessable("invalid_argument", kind+" is not valid")
	case errors.Is(err, codeintel.ErrUnavailable):
		return platform.Unavailable(kind + " is unavailable")
	default:
		return err
	}
}

func mustJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

func decodeObject(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return map[string]any{}
	}
	return value
}

func formatID(value int64) string { return platformid.Format(value) }

// recordAudit writes one best-effort audit entry scoped to the caller. Audit
// metadata never contains code bodies or filesystem paths.
func (s *IndexService) recordAudit(ctx context.Context, action, targetKind, targetID string, metadata map[string]any) {
	recordAudit(ctx, s.audit, action, targetKind, targetID, metadata)
}

func (s *QueryService) recordAudit(ctx context.Context, action, targetKind, targetID string, metadata map[string]any) {
	recordAudit(ctx, s.audit, action, targetKind, targetID, metadata)
}

func (s *QueryService) intSetting(ctx context.Context, key string, fallback, hardMax int) int {
	return readIntSetting(ctx, s.settings, key, fallback, hardMax)
}

func recordAudit(ctx context.Context, auditor AuditRecorder, action, targetKind, targetID string, metadata map[string]any) {
	if auditor == nil {
		return
	}
	entry := AuditEntry{
		Action:     action,
		TargetKind: targetKind,
		TargetID:   targetID,
		RequestID:  requestid.From(ctx),
		Metadata:   metadata,
	}
	if actor, ok := platform.ActorFrom(ctx); ok && actor.ID > 0 {
		actorID := actor.ID
		entry.ActorID = &actorID
	}
	_ = auditor.Record(ctx, entry)
}

// publish emits one codeintel event on the version channel.
func (s *IndexService) publish(ctx context.Context, eventType string, run codeintel.IndexRun, payload map[string]any) {
	publishEvent(ctx, s.events, eventType, run.VersionID, payload)
}

func publishEvent(ctx context.Context, publisher EventPublisher, eventType string, versionID int64, payload map[string]any) {
	if publisher == nil {
		return
	}
	event := Event{Type: eventType, VersionID: versionID, Payload: payload, RequestID: requestid.From(ctx)}
	if versionID > 0 {
		event.Scope = map[string]any{"version_id": platformid.Format(versionID)}
	}
	if actor, ok := platform.ActorFrom(ctx); ok && actor.ID > 0 {
		actorID := actor.ID
		event.ActorID = &actorID
	}
	_ = publisher.Publish(ctx, event)
}

// failRun persists a failed index run and publishes code.index_failed. The
// recorded message is the stable platform message (never an fs path).
func (s *IndexService) failRun(ctx context.Context, run codeintel.IndexRun, cause error) {
	now := s.clock.Now()
	code := "internal"
	message := "index run failed"
	var apiErr *platform.Error
	if errors.As(cause, &apiErr) {
		code = apiErr.Code
		if strings.TrimSpace(apiErr.Message) != "" {
			message = apiErr.Message
		}
	}
	run.Status = string(codeintel.IndexFailed)
	run.FinishedAt = &now
	run.UpdatedAt = now
	run.Errors = mustJSON([]map[string]any{{"code": code, "message": truncateString(message, 500)}})
	if err := s.runs.UpdateRun(ctx, run); err != nil {
		return
	}
	s.publish(ctx, "code.index_failed", run, map[string]any{
		"index_run_id": platformid.Format(run.ID),
		"error_code":   code,
		"message":      truncateString(message, 500),
	})
}

func truncateString(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
