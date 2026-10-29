package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// IndexService constants. codeRepositoryActive mirrors the P2 code repository
// status; the others mirror the design-spec bounds and reason codes.
const (
	codeRepositoryActive = "active"

	indexRunSourceT0 = "t0"

	// defaultMaxIndexFiles and hardMaxIndexFiles mirror the
	// EZTEST_CODEINTEL_MAX_INDEX_FILES default and upper bound (design spec
	// §4.1). A configured value may only lower the effective bound.
	defaultMaxIndexFiles = 5000
	hardMaxIndexFiles    = 50000

	// indexFailureWriteTimeout bounds the persistence of a failed run. The
	// failure context deliberately ignores the caller's cancellation so a run
	// that failed because its worker deadline expired is still recorded.
	indexFailureWriteTimeout = 5 * time.Second

	// indexErrorMaxChars bounds one persisted run error message. Errors carry
	// paths, line numbers and reasons only, never code content (§6.10).
	indexErrorMaxChars = 1000

	// gitBlobHashPrefix is the DDL's git object id encoding for blob hashes
	// (design spec §5.1).
	gitBlobHashPrefix = "git-sha1:"

	reasonQueueFull = "analysis_queue_full"
	reasonInternal  = "internal"
	reasonTimeout   = "analysis_timeout"
)

// IndexServiceDeps are the ports of the T0 repository index use case. The
// repository interfaces are the committed domain ports implemented by T5; the
// Audit port is part of the fixed dependency list but the index run itself has
// no audit action in design spec §6.10 (only code.read and code.analyze exist),
// so it is currently unused by this service. CommandTimeout is likewise
// retained for wiring symmetry: the injected gitx adapter enforces the per-Git
// command timeout itself.
type IndexServiceDeps struct {
	Git            GitReader
	Repos          RepoCatalog
	IndexRuns      domain.IndexRunRepository
	Entities       domain.EntityRepository
	ChangeSets     domain.ChangeSetRepository
	State          IndexStateWriter
	Queue          Enqueuer
	Audit          AuditRecorder
	IDs            IDGenerator
	Clock          Clock
	MaxIndexFiles  int
	CommandTimeout time.Duration
}

// IndexService implements the T0 repository index run (design spec §6.1/§6.8):
// Start persists a queued run and enqueues it; Run executes the tree walk,
// writes the file entities, marks the P2 index state and finishes the run.
type IndexService struct {
	deps          IndexServiceDeps
	maxIndexFiles int

	// startMu serialises Start so two concurrent HTTP triggers cannot both
	// observe "no active run" and create duplicate queued runs. The active-run
	// check has no DB constraint to fall back on, and P4 is single-instance.
	startMu sync.Mutex

	mu       sync.Mutex
	runRepos map[int64]int64
}

// NewIndexService wires the index use case. MaxIndexFiles is clamped to the
// documented 5000 default / 50000 hard bound; the zero value uses the default.
func NewIndexService(deps IndexServiceDeps) *IndexService {
	maxIndexFiles := deps.MaxIndexFiles
	if maxIndexFiles <= 0 {
		maxIndexFiles = defaultMaxIndexFiles
	}
	if maxIndexFiles > hardMaxIndexFiles {
		maxIndexFiles = hardMaxIndexFiles
	}
	return &IndexService{
		deps:          deps,
		maxIndexFiles: maxIndexFiles,
		runRepos:      make(map[int64]int64),
	}
}

// Start validates the repository, resolves the fixed branch tip, persists a
// queued run and enqueues the index job (design spec §6.1). A repository whose
// latest run is still queued/running is an index_in_progress conflict. When the
// worker queue is full the run is persisted as failed(analysis_queue_full) and
// the returned error carries the 429 reason.
func (s *IndexService) Start(ctx context.Context, repoID, actorID int64) (domain.IndexRun, error) {
	s.startMu.Lock()
	defer s.startMu.Unlock()

	repo, err := s.activeRepo(ctx, repoID)
	if err != nil {
		return domain.IndexRun{}, err
	}

	latest, err := s.deps.IndexRuns.GetLatest(ctx, repoID)
	switch {
	case err == nil:
		if latest.Status == domain.RunQueued || latest.Status == domain.RunRunning {
			return domain.IndexRun{}, domain.NewConflict("index_in_progress",
				fmt.Sprintf("codeintel: repository %d already has an active index run", repoID))
		}
	case errors.Is(err, domain.ErrNotFound):
		// No previous run: the repository has never been indexed.
	default:
		return domain.IndexRun{}, err
	}

	head, err := s.deps.Git.Head(ctx, repo.LocalPath, repo.FixedBranch)
	if err != nil {
		return domain.IndexRun{}, err
	}

	now := s.now()
	run := domain.IndexRun{
		ID:        s.deps.IDs.Next(),
		RepoID:    repoID,
		CommitSHA: head,
		Source:    indexRunSourceT0,
		Status:    domain.RunQueued,
		CreatedBy: actorID,
		CreatedAt: now,
	}
	if err := s.deps.IndexRuns.Create(ctx, run); err != nil {
		return domain.IndexRun{}, err
	}
	s.rememberRun(run.ID, repoID)

	if err := s.enqueueIndexJob(run); err != nil {
		return s.failEnqueuedRun(ctx, run, err)
	}
	return run, nil
}

// enqueueIndexJob enqueues the run and normalises a missing queue into an
// internal error (a mis-wired process must not leave a silently queued run).
func (s *IndexService) enqueueIndexJob(run domain.IndexRun) error {
	if s.deps.Queue == nil {
		return errors.New("codeintel: index queue is not configured")
	}
	return s.deps.Queue.Enqueue(Job{Kind: JobKindIndex, ID: run.ID})
}

// failEnqueuedRun records the enqueue failure on the run and returns the
// caller-facing error. A queue-full failure is retryable (HTTP 429) and keeps
// ErrQueueFull in the chain so callers can classify it.
func (s *IndexService) failEnqueuedRun(ctx context.Context, run domain.IndexRun, queueErr error) (domain.IndexRun, error) {
	code := reasonInternal
	callerErr := queueErr
	if errors.Is(queueErr, ErrQueueFull) {
		code = reasonQueueFull
		callerErr = fmt.Errorf("%w: %w",
			domain.NewConflict(reasonQueueFull, "codeintel: the analysis queue is full"), ErrQueueFull)
	}

	finishedAt := s.now()
	failed := run
	failed.Status = domain.RunFailed
	failed.FinishedAt = &finishedAt
	failed.Errors = append(failed.Errors, domain.AnalysisError{
		Code:    code,
		Message: truncateRunError(queueErr.Error()),
	})

	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), indexFailureWriteTimeout)
	defer cancel()
	s.forgetRun(run.ID)
	if err := s.deps.IndexRuns.Update(writeCtx, failed); err != nil {
		return failed, errors.Join(callerErr, fmt.Errorf("codeintel: persist failed index run %d: %w", run.ID, err))
	}
	return failed, callerErr
}

// Get returns one run of the repository.
func (s *IndexService) Get(ctx context.Context, repoID, runID int64) (domain.IndexRun, error) {
	return s.deps.IndexRuns.GetByID(ctx, repoID, runID)
}

// GetLatest returns the newest run of the repository.
func (s *IndexService) GetLatest(ctx context.Context, repoID int64) (domain.IndexRun, error) {
	return s.deps.IndexRuns.GetLatest(ctx, repoID)
}

// Run executes one queued T0 index run (design spec §6.1): the run moves to
// running, the filtered commit tree is paged and every regular blob is written
// as a file entity, the P2 last_indexed_at is written through the resource
// application port and the run ends done with stats. Any failure (including a
// panic or an expired worker deadline) is persisted as failed and returned.
//
// The run's repository is recovered from the in-process Start registry because
// the committed IndexRunRepository GetByID is repo-scoped while the worker job
// only carries the run id. This is safe under the P4 single-instance worker
// assumption: startup recovery marks leftover runs failed, they are never
// re-executed across processes.
func (s *IndexService) Run(ctx context.Context, runID int64) (err error) {
	repoID, ok := s.repoForRun(runID)
	if !ok {
		return domain.NewNotFound("index_run_not_found",
			fmt.Sprintf("codeintel: index run %d is not owned by this service", runID))
	}
	run, err := s.deps.IndexRuns.GetByID(ctx, repoID, runID)
	if err != nil {
		s.forgetRun(runID)
		return err
	}
	switch run.Status {
	case domain.RunDone, domain.RunFailed:
		// Terminal states are idempotent no-ops: a delayed duplicate job must
		// never rewrite a finished run.
		s.forgetRun(runID)
		return nil
	case domain.RunRunning:
		s.forgetRun(runID)
		return domain.NewConflict("index_in_progress",
			fmt.Sprintf("codeintel: index run %d is already running", runID))
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			err = s.failRun(ctx, run, reasonInternal,
				fmt.Errorf("codeintel: index run %d panicked: %v", runID, recovered))
		}
	}()

	runStart := time.Now()
	startedAt := s.now()
	run.Status = domain.RunRunning
	run.StartedAt = &startedAt
	if err := s.deps.IndexRuns.Update(ctx, run); err != nil {
		s.forgetRun(runID)
		return err
	}

	repo, err := s.activeRepo(ctx, repoID)
	if err != nil {
		return s.failRun(ctx, run, failureReason(ctx, err), err)
	}

	files, truncated, truncatedFiles, err := s.loadTree(ctx, repo, run.CommitSHA)
	if err != nil {
		return s.failRun(ctx, run, failureReason(ctx, err), err)
	}
	if err := s.deps.Entities.UpsertFile(ctx, repoID, run.CommitSHA, files); err != nil {
		return s.failRun(ctx, run, failureReason(ctx, err), err)
	}

	finishedAt := s.now()
	if err := s.deps.State.MarkIndexed(ctx, repoID, run.CommitSHA, finishedAt); err != nil {
		return s.failRun(ctx, run, failureReason(ctx, err), err)
	}

	stats, err := json.Marshal(indexRunStats{
		Files:          len(files),
		Truncated:      truncated,
		TruncatedFiles: truncatedFiles,
		DurationMs:     time.Since(runStart).Milliseconds(),
	})
	if err != nil {
		return s.failRun(ctx, run, reasonInternal, err)
	}
	run.Status = domain.RunDone
	run.Stats = stats
	run.FinishedAt = &finishedAt
	if err := s.deps.IndexRuns.Update(ctx, run); err != nil {
		return err
	}
	s.forgetRun(runID)
	return nil
}

// indexRunStats is the JSONB stats document of one index run (design spec
// §6.1: files/truncated/duration).
type indexRunStats struct {
	Files          int   `json:"files"`
	Truncated      bool  `json:"truncated"`
	TruncatedFiles int   `json:"truncatedFiles,omitempty"`
	DurationMs     int64 `json:"durationMs"`
}

// indexFileMeta is the JSONB meta document of one T0 file entity.
type indexFileMeta struct {
	Size int64  `json:"size"`
	Mode string `json:"mode"`
}

// loadTree pages through the filtered commit tree and builds the file entities.
// It stops after MaxIndexFiles+1 regular blobs so truncation is detectable
// without scanning the whole repository; truncatedFiles counts the blobs
// dropped from the returned set.
func (s *IndexService) loadTree(ctx context.Context, repo RepoInfo, commit string) ([]domain.CodeEntity, bool, int, error) {
	pageLimit := s.maxIndexFiles + 1
	if pageLimit > domain.MaxTreeEntriesPerPage {
		pageLimit = domain.MaxTreeEntriesPerPage
	}
	files := make([]domain.CodeEntity, 0)
	cursor := ""
	for {
		page, err := s.deps.Git.Tree(ctx, repo.LocalPath, commit, "", repo.IncludeGlobs, repo.ExcludeGlobs, cursor, pageLimit)
		if err != nil {
			return nil, false, 0, err
		}
		for _, entry := range page.Entries {
			if !isIndexableBlob(entry) {
				continue
			}
			if len(files) > s.maxIndexFiles {
				break
			}
			files = append(files, s.fileEntity(repo.ID, commit, entry))
		}
		if len(files) > s.maxIndexFiles {
			break
		}
		if !page.HasMore {
			break
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			// A paginating adapter that cannot advance would silently drop
			// files, so fail the run instead of finishing a partial index.
			return nil, false, 0, errors.New("codeintel: git tree cursor did not advance")
		}
		cursor = page.NextCursor
	}
	if len(files) > s.maxIndexFiles {
		truncatedFiles := len(files) - s.maxIndexFiles
		return files[:s.maxIndexFiles], true, truncatedFiles, nil
	}
	return files, false, 0, nil
}

// fileEntity builds the T0 file fact of one tree blob. Only the blob hash is
// meaningful to the repository's batch upsert; size/mode are carried in meta
// for callers that persist richer file facts.
func (s *IndexService) fileEntity(repoID int64, commit string, entry domain.TreeEntry) domain.CodeEntity {
	meta, _ := json.Marshal(indexFileMeta{Size: entry.Size, Mode: entry.Mode})
	return domain.CodeEntity{
		ID:         s.deps.IDs.Next(),
		RepoID:     repoID,
		Commit:     commit,
		Kind:       domain.KindFile,
		Path:       entry.Path,
		BlobHash:   gitBlobHashPrefix + entry.GitObjectID,
		Source:     domain.SourceGit,
		Confidence: domain.ConfidenceHigh,
		Meta:       meta,
	}
}

// failRun persists the failed terminal state and returns the original cause.
// The write uses a context detached from the worker deadline so the failure can
// still be recorded when the deadline is exactly what failed.
func (s *IndexService) failRun(ctx context.Context, run domain.IndexRun, code string, cause error) error {
	finishedAt := s.now()
	run.Status = domain.RunFailed
	run.FinishedAt = &finishedAt
	run.Errors = append(run.Errors, domain.AnalysisError{
		Code:    code,
		Message: truncateRunError(cause.Error()),
	})

	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), indexFailureWriteTimeout)
	defer cancel()
	s.forgetRun(run.ID)
	if err := s.deps.IndexRuns.Update(writeCtx, run); err != nil {
		return errors.Join(cause, fmt.Errorf("codeintel: persist failed index run %d: %w", run.ID, err))
	}
	return cause
}

// activeRepo loads the repository and rejects a disabled one (design spec
// §7.1 code_repository_disabled).
func (s *IndexService) activeRepo(ctx context.Context, repoID int64) (RepoInfo, error) {
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

// failureReason maps a run failure to its persisted code: an expired deadline is
// analysis_timeout, a domain error keeps its spec §7.1 reason and everything
// else is internal.
func failureReason(ctx context.Context, err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return reasonTimeout
	}
	if reason := domain.Reason(err); reason != "" {
		return reason
	}
	return reasonInternal
}

// isIndexableBlob reports whether a tree entry is an ordinary file blob that
// can become a file entity. Trees, symlinks and submodules are skipped
// (design spec §6.1).
func isIndexableBlob(entry domain.TreeEntry) bool {
	return entry.Type == "blob" && !entry.Unsupported &&
		entry.Mode != "120000" && entry.Mode != "160000"
}

// truncateRunError bounds a persisted error message and never leaves a split
// UTF-8 sequence.
func truncateRunError(message string) string {
	if len(message) <= indexErrorMaxChars {
		return message
	}
	cut := indexErrorMaxChars
	for cut > 0 && !utf8.ValidString(message[:cut]) {
		cut--
	}
	return message[:cut] + "..."
}

// now returns the injected clock time (falling back to wall time when a
// partially wired process leaves Clock nil).
func (s *IndexService) now() time.Time {
	if s.deps.Clock != nil {
		return s.deps.Clock.Now()
	}
	return time.Now().UTC()
}

func (s *IndexService) rememberRun(runID, repoID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runRepos[runID] = repoID
}

func (s *IndexService) repoForRun(runID int64) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	repoID, ok := s.runRepos[runID]
	return repoID, ok
}

func (s *IndexService) forgetRun(runID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.runRepos, runID)
}
