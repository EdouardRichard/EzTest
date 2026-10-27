package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// Analysis-service constants. The configuration bounds mirror design spec §4.1;
// the reasons are the stable §7.1 reason codes persisted in code_analyses.
const (
	// auditActionCodeAnalyze is the design spec §6.10 analysis audit action. The
	// detail carries identifiers and status only.
	auditActionCodeAnalyze = "code.analyze"

	// defaultMaxChangedFiles / hardMaxChangedFiles mirror
	// EZTEST_CODEINTEL_MAX_CHANGED_FILES (default 20, hard cap 200).
	defaultMaxChangedFiles = 20
	hardMaxChangedFiles    = 200

	// defaultMaxFileBytes / minMaxFileBytes / hardMaxFileBytes mirror
	// EZTEST_CODEINTEL_MAX_FILE_BYTES (default 64 KiB, 1 KiB–1 MiB).
	defaultMaxFileBytes = 64 << 10
	minMaxFileBytes     = 1 << 10
	hardMaxFileBytes    = 1 << 20

	// defaultMaxExcerptLines mirrors EZTEST_CODEINTEL_MAX_EXCERPT_LINES (40),
	// bounded by the domain hard cap.
	defaultMaxExcerptLines = 40

	// contractFactPageSize is the entity page size used while collecting the
	// head-commit contract facts.
	contractFactPageSize = domain.MaxTreeEntriesPerPage

	// reasonAnalysisInProgress / reasonAnalysisNotDone are the §7.1 reasons of
	// the trigger and PATCH state rules.
	reasonAnalysisInProgress = "analysis_in_progress"
	reasonAnalysisNotDone    = "analysis_not_done"

	// Per-file analysis error codes persisted in code_analyses.errors.
	reasonLLMUnavailable = "llm_unavailable"
	reasonLLMError       = "llm_error"
	reasonParseError     = "parse_error"
	reasonReadFailed     = "read_failed"

	// reasonUnsupportedContent and reasonPathNotFile are the Git verdicts of a
	// non-text read and of a symlink/submodule/directory. Design spec §6.2
	// excludes those files from AI analysis: they are skipped, not degraded.
	reasonUnsupportedContent = "unsupported_content"
	reasonPathNotFile        = "path_not_file"

	// statusChangedFileDeleted is the git diff status of a deleted file.
	statusChangedFileDeleted = "D"

	// analysisSummaryMaxRunes bounds the joined file change summaries.
	analysisSummaryMaxRunes = 2000
	// eventPathLimit is the design spec §6.9/§7.4 refs.code cap.
	eventPathLimit = 20
)

// commitHashPattern matches the lowercase 40-hex commit ids the DDL and the
// explicit head contract require.
var commitHashPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ImpactPlanner is the impact-analysis port implemented by T13's ImpactService.
// The fixed signature reserves the concrete impact service (candidate merge,
// anchors, contract propagation, requirement relations, AI reasoning, unmapped)
// behind one application boundary; the analysis service only persists its
// result.
type ImpactPlanner interface {
	Plan(ctx context.Context, in ImpactInput) (ImpactResult, error)
}

// ImpactInput is the complete deterministic + T1 state of one analysis passed
// to the impact planner (design spec §6.6).
//
// AppModels is left empty by the analysis service: the fixed AnalysisServiceDeps
// of the plan has no AppModelCatalog, so the impact service (T13) owns that port
// and resolves the version system's nodes itself.
type ImpactInput struct {
	Version      VersionInfo
	Analysis     domain.CodeAnalysis
	Requirements []RequirementInfo
	Relations    []RelationInfo
	AppModels    []AppModelNode
	Anchors      []domain.CodeAnchor
	ChangedFiles []domain.ChangedFile
	FactsAtHead  []domain.CodeEntity
	Edges        []domain.CodeEdge
	Degraded     bool
}

// ImpactResult is the impact planner output persisted by the analysis service:
// the candidates (ids are reassigned by the analysis service), the unmapped
// changes and the planner's own stats document to merge into the analysis
// stats.
type ImpactResult struct {
	Candidates []domain.ImpactCandidate
	Unmapped   []domain.UnmappedChange
	Stats      []byte
}

// AnalysisConfig bounds one analysis service (design spec §4.1). Zero values
// fall back to the documented defaults and configured values may only lower the
// hard bounds.
type AnalysisConfig struct {
	// MaxChangedFiles bounds the number of changed files sent to the LLM.
	MaxChangedFiles int
	// MaxFileBytes bounds one analysed file's content (evidence reads share the
	// bound, per design spec §6.3 step 4).
	MaxFileBytes int
	// MaxExcerptLines bounds one evidence excerpt.
	MaxExcerptLines int
	// MaxIndexFiles bounds the head-commit fact scan used by contract matching,
	// mirroring the P4 index cap so one analysis cannot walk an unbounded fact
	// table.
	MaxIndexFiles int
	// PromptVersion participates in the file cache key and the fact versions.
	PromptVersion string
	// Timeout bounds one direct Run call. The worker normally applies the same
	// analysis timeout; a zero value leaves the caller's deadline untouched.
	Timeout time.Duration
}

// AnalysisServiceDeps are the ports of the version-scoped analysis use case.
// Git, the repositories, the P2/P3 catalogs, the worker queue, the LLM, the
// impact planner, the notifier, the audit recorder, the id generator, the
// clock and the configuration are injected by the composition root.
type AnalysisServiceDeps struct {
	Git        GitReader
	Repos      RepoCatalog
	Versions   VersionCatalog
	ChangeSets domain.ChangeSetRepository
	Entities   domain.EntityRepository
	Edges      domain.EdgeRepository
	Facts      domain.FactVersionRepository
	Cache      domain.CacheRepository
	Analyses   domain.AnalysisRepository
	Candidates domain.CandidateRepository
	Anchors    domain.AnchorRepository
	Queue      Enqueuer
	LLM        LLMCompleter
	Planner    ImpactPlanner
	Notifier   ImpactNotifier
	Audit      AuditRecorder
	IDs        IDGenerator
	Clock      Clock
	Config     AnalysisConfig
}

// analysisRunRef is the in-process information the worker Job cannot carry:
// GetByID is version-scoped while Job only carries the analysis id, and the
// worker alone cannot know whether the trigger asked for a forced re-run.
//
// The registry is populated by Start and consumed by Run. P4 runs a single
// worker in the API process and startup recovery marks leftover queued/running
// analyses failed, so a job is never executed by another process; a Job whose
// analysis is not in the registry is reported as analysis_not_found rather than
// risking a cross-version lookup.
type analysisRunRef struct {
	versionID int64
	force     bool
}

// AnalysisService implements the version-scoped impact analysis run (design
// spec §6.2–§6.6): trigger/reuse/force, the deterministic change set, T1 file
// analysis with the file cache, evidence-validated fact/edge/contract
// materialisation with fact versioning, the impact planner handoff and the
// persisted terminal state.
type AnalysisService struct {
	deps   AnalysisServiceDeps
	config AnalysisConfig

	// startMu serialises Start so two concurrent triggers cannot both observe
	// "no active analysis" and create duplicate runs. P4 is single-instance and
	// the active check has no database constraint to fall back on.
	startMu sync.Mutex

	mu   sync.Mutex
	runs map[int64]analysisRunRef
}

// NewAnalysisService wires the analysis use case and normalises its config.
func NewAnalysisService(deps AnalysisServiceDeps) *AnalysisService {
	return &AnalysisService{
		deps:   deps,
		config: normalizeAnalysisConfig(deps.Config),
		runs:   make(map[int64]analysisRunRef),
	}
}

// normalizeAnalysisConfig applies the design spec §4.1 bounds.
func normalizeAnalysisConfig(cfg AnalysisConfig) AnalysisConfig {
	if cfg.MaxChangedFiles <= 0 {
		cfg.MaxChangedFiles = defaultMaxChangedFiles
	}
	if cfg.MaxChangedFiles > hardMaxChangedFiles {
		cfg.MaxChangedFiles = hardMaxChangedFiles
	}
	if cfg.MaxFileBytes <= 0 {
		cfg.MaxFileBytes = defaultMaxFileBytes
	}
	if cfg.MaxFileBytes < minMaxFileBytes {
		cfg.MaxFileBytes = minMaxFileBytes
	}
	if cfg.MaxFileBytes > hardMaxFileBytes {
		cfg.MaxFileBytes = hardMaxFileBytes
	}
	if cfg.MaxExcerptLines <= 0 {
		cfg.MaxExcerptLines = defaultMaxExcerptLines
	}
	if cfg.MaxExcerptLines > domain.MaxExcerptLines {
		cfg.MaxExcerptLines = domain.MaxExcerptLines
	}
	if cfg.MaxIndexFiles <= 0 {
		cfg.MaxIndexFiles = defaultMaxIndexFiles
	}
	if cfg.MaxIndexFiles > hardMaxIndexFiles {
		cfg.MaxIndexFiles = hardMaxIndexFiles
	}
	cfg.PromptVersion = strings.TrimSpace(cfg.PromptVersion)
	if cfg.PromptVersion == "" {
		cfg.PromptVersion = PromptVersionDefault
	}
	return cfg
}

// Start validates the version binding, resolves the head commit, reuses a done
// analysis for the same input unless force is set, and creates+enqueues a queued
// analysis with the next analysis version (design spec §6.4/§6.8).
//
// The returned bool reports reuse: a reused analysis is returned unchanged with
// reused=true and nothing is created or enqueued. A full worker queue persists
// the new analysis as failed(analysis_queue_full) and returns the retryable
// error chain.
func (s *AnalysisService) Start(ctx context.Context, versionID, actorID int64, headCommit string, force bool) (domain.CodeAnalysis, bool, error) {
	s.startMu.Lock()
	defer s.startMu.Unlock()

	info, err := s.deps.Versions.Get(ctx, versionID)
	if err != nil {
		return domain.CodeAnalysis{}, false, err
	}
	if !info.HasRepo || info.RepoID <= 0 {
		return domain.CodeAnalysis{}, false, domain.NewInvalid("versionId", "code_repository_not_bound",
			fmt.Sprintf("codeintel: version %d has no code repository binding", versionID))
	}
	base := strings.TrimSpace(info.BaselineCommit)
	if base == "" || !commitHashPattern.MatchString(base) {
		return domain.CodeAnalysis{}, false, domain.NewInvalid("versionId", "baseline_commit_missing",
			fmt.Sprintf("codeintel: version %d has no valid code baseline commit", versionID))
	}
	repo, err := s.activeRepo(ctx, info.RepoID)
	if err != nil {
		return domain.CodeAnalysis{}, false, err
	}
	head, err := s.resolveHead(ctx, repo, base, headCommit)
	if err != nil {
		return domain.CodeAnalysis{}, false, err
	}

	model := ""
	if s.deps.LLM != nil {
		model = s.deps.LLM.Model()
	}
	prompt := s.config.PromptVersion

	if !force {
		reusable, err := s.deps.Analyses.FindReusable(ctx, versionID, repo.ID, base, head, model, prompt)
		if err != nil {
			return domain.CodeAnalysis{}, false, err
		}
		if reusable != nil {
			s.auditTrigger(ctx, actorID, *reusable)
			return *reusable, true, nil
		}
	}

	active, err := s.deps.Analyses.HasActive(ctx, versionID)
	if err != nil {
		return domain.CodeAnalysis{}, false, err
	}
	if active {
		return domain.CodeAnalysis{}, false, domain.NewConflict(reasonAnalysisInProgress,
			fmt.Sprintf("codeintel: version %d already has an active analysis", versionID))
	}

	analysisVersion, err := s.deps.Analyses.NextAnalysisVersion(ctx, versionID, base, head)
	if err != nil {
		return domain.CodeAnalysis{}, false, err
	}
	now := s.now()
	analysis := domain.CodeAnalysis{
		ID:              s.deps.IDs.Next(),
		VersionID:       versionID,
		RepoID:          repo.ID,
		BaseCommit:      base,
		HeadCommit:      head,
		Status:          domain.RunQueued,
		Model:           model,
		PromptVersion:   prompt,
		AnalysisVersion: analysisVersion,
		Stats:           []byte("{}"),
		CreatedBy:       actorID,
		CreatedAt:       now,
	}
	if analysis.ID == 0 {
		return domain.CodeAnalysis{}, false, errors.New("codeintel: id generator returned a zero analysis id")
	}
	if err := s.deps.Analyses.Create(ctx, analysis); err != nil {
		return domain.CodeAnalysis{}, false, err
	}
	s.rememberAnalysis(analysis.ID, versionID, force)
	if err := s.enqueueAnalysis(analysis); err != nil {
		return s.failEnqueuedAnalysis(ctx, analysis, err)
	}
	s.auditTrigger(ctx, actorID, analysis)
	return analysis, false, nil
}

// enqueueAnalysis dispatches the queued analysis. A missing queue is an internal
// mis-wiring error so a silently stuck queued analysis is impossible.
func (s *AnalysisService) enqueueAnalysis(analysis domain.CodeAnalysis) error {
	if s.deps.Queue == nil {
		return errors.New("codeintel: analysis queue is not configured")
	}
	return s.deps.Queue.Enqueue(Job{Kind: JobKindAnalysis, ID: analysis.ID})
}

// failEnqueuedAnalysis records the enqueue failure and returns the caller-facing
// error. A full queue keeps ErrQueueFull and the retryable analysis_queue_full
// reason in the chain.
func (s *AnalysisService) failEnqueuedAnalysis(ctx context.Context, analysis domain.CodeAnalysis, queueErr error) (domain.CodeAnalysis, bool, error) {
	code := reasonInternal
	callerErr := queueErr
	if errors.Is(queueErr, ErrQueueFull) {
		code = reasonQueueFull
		callerErr = fmt.Errorf("%w: %w",
			domain.NewConflict(reasonQueueFull, "codeintel: the analysis queue is full"), ErrQueueFull)
	}

	finishedAt := s.now()
	failed := analysis
	failed.Status = domain.RunFailed
	failed.FinishedAt = &finishedAt
	failed.Errors = append(failed.Errors, domain.AnalysisError{
		Code:    code,
		Message: truncateRunError(queueErr.Error()),
	})
	failed.Stats = []byte("{}")

	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), indexFailureWriteTimeout)
	defer cancel()
	s.forgetAnalysis(analysis.ID)
	if err := s.deps.Analyses.Update(writeCtx, failed); err != nil {
		return failed, false, errors.Join(callerErr,
			fmt.Errorf("codeintel: persist failed analysis %d: %w", analysis.ID, err))
	}
	return failed, false, callerErr
}

// resolveHead validates an explicit head commit (40 hex and a descendant of the
// baseline) or resolves the live tip of the repository fixed branch (design
// spec §6.1/INV-3: the version's code_branch is display metadata, never a Git
// source; QueryService/IndexService resolve the same fixed branch). base == head
// is legal.
func (s *AnalysisService) resolveHead(ctx context.Context, repo RepoInfo, base, headCommit string) (string, error) {
	head := strings.TrimSpace(headCommit)
	if head != "" {
		if !commitHashPattern.MatchString(head) {
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
	return s.deps.Git.Head(ctx, repo.LocalPath, repo.FixedBranch)
}

// activeRepo loads the repository and rejects a disabled one (design spec §7.1
// code_repository_disabled).
func (s *AnalysisService) activeRepo(ctx context.Context, repoID int64) (RepoInfo, error) {
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

// Get returns one analysis of the version.
func (s *AnalysisService) Get(ctx context.Context, versionID, analysisID int64) (domain.CodeAnalysis, error) {
	return s.deps.Analyses.GetByID(ctx, versionID, analysisID)
}

// GetLatest returns the newest analysis of the version (any status).
func (s *AnalysisService) GetLatest(ctx context.Context, versionID int64) (domain.CodeAnalysis, error) {
	return s.deps.Analyses.GetLatest(ctx, versionID)
}

// ListCandidates returns the candidates of one analysis in the deterministic
// repository order.
func (s *AnalysisService) ListCandidates(ctx context.Context, analysisID int64) ([]domain.ImpactCandidate, error) {
	return s.deps.Candidates.List(ctx, analysisID)
}

// PatchCandidate persists one human candidate selection of a finished analysis
// (design spec §6.6/§6.11). The analysis must belong to the version and be
// done; the candidate must belong to the analysis. The updated candidate is
// returned so the HTTP layer can echo it.
func (s *AnalysisService) PatchCandidate(ctx context.Context, versionID, analysisID, candidateID, actorID int64, selected bool) (domain.ImpactCandidate, error) {
	analysis, err := s.deps.Analyses.GetByID(ctx, versionID, analysisID)
	if err != nil {
		return domain.ImpactCandidate{}, err
	}
	if analysis.Status != domain.RunDone {
		return domain.ImpactCandidate{}, domain.NewConflict(reasonAnalysisNotDone,
			fmt.Sprintf("codeintel: analysis %d is not done", analysisID))
	}
	candidate, err := s.deps.Candidates.Get(ctx, analysisID, candidateID)
	if err != nil {
		return domain.ImpactCandidate{}, err
	}
	if err := s.deps.Analyses.UpdateCandidateSelection(ctx, analysisID, candidateID, actorID, selected); err != nil {
		return domain.ImpactCandidate{}, err
	}
	now := s.now()
	candidate.Selected = selected
	candidate.SelectionUpdatedBy = &actorID
	candidate.SelectionUpdatedAt = &now
	return candidate, nil
}

// Run executes one queued analysis (design spec §6.2–§6.6/§6.8). Terminal
// analyses are idempotent no-ops and a duplicate running job is a conflict. Any
// failure (including a panic or an expired worker deadline) is persisted as
// failed and returned; the error never carries code content.
func (s *AnalysisService) Run(ctx context.Context, analysisID int64) (err error) {
	ref, ok := s.analysisRef(analysisID)
	if !ok {
		return domain.NewNotFound("analysis_not_found",
			fmt.Sprintf("codeintel: analysis %d is not owned by this service", analysisID))
	}
	analysis, err := s.deps.Analyses.GetByID(ctx, ref.versionID, analysisID)
	if err != nil {
		s.forgetAnalysis(analysisID)
		return err
	}
	switch analysis.Status {
	case domain.RunDone, domain.RunFailed:
		s.forgetAnalysis(analysisID)
		return nil
	case domain.RunRunning:
		return domain.NewConflict(reasonAnalysisInProgress,
			fmt.Sprintf("codeintel: analysis %d is already running", analysisID))
	}
	if s.config.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.config.Timeout)
		defer cancel()
	}

	startedAt := s.now()
	analysis.Status = domain.RunRunning
	analysis.StartedAt = &startedAt
	if err := s.deps.Analyses.Update(ctx, analysis); err != nil {
		return err
	}
	state := &analysisRunState{
		analysis: analysis,
		content:  make(map[string]*commitContent),
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = s.failRun(ctx, state.analysis, reasonInternal,
				fmt.Errorf("codeintel: analysis %d panicked: %v", analysisID, recovered))
		}
	}()

	runStart := time.Now()
	if err := s.executeRun(ctx, state, ref.force, runStart); err != nil {
		return s.failRun(ctx, state.analysis, failureReason(ctx, err), err)
	}
	s.forgetAnalysis(analysisID)
	return nil
}

// failRun persists the failed terminal state and returns the original cause.
// The write is detached from the worker deadline so a timeout is still
// recorded.
func (s *AnalysisService) failRun(ctx context.Context, analysis domain.CodeAnalysis, code string, cause error) error {
	finishedAt := s.now()
	analysis.Status = domain.RunFailed
	analysis.FinishedAt = &finishedAt
	if len(analysis.Errors) < domain.MaxAnalysisErrors {
		analysis.Errors = append(analysis.Errors, domain.AnalysisError{
			Code:    code,
			Message: truncateRunError(cause.Error()),
		})
	}

	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), indexFailureWriteTimeout)
	defer cancel()
	s.forgetAnalysis(analysis.ID)
	if err := s.deps.Analyses.Update(writeCtx, analysis); err != nil {
		return errors.Join(cause, fmt.Errorf("codeintel: persist failed analysis %d: %w", analysis.ID, err))
	}
	return cause
}

// analysisRunState is the mutable state of one Run.
type analysisRunState struct {
	analysis         domain.CodeAnalysis
	repo             RepoInfo
	stats            analysisStats
	content          map[string]*commitContent
	degraded         bool
	llmErrorRecorded bool
}

// commitContent is one lazily read commit-pinned file used for evidence and fact
// line validation. The content is shared by every fact that points at the file.
type commitContent struct {
	content   []byte
	lines     []string
	ok        bool
	truncated bool
	err       error
}

// analysisStats is the JSONB stats document of one analysis (design spec §7.3
// plus the file-level counters needed to explain a degraded run).
type analysisStats struct {
	ChangedFiles       int    `json:"changedFiles"`
	AnalyzedFiles      int    `json:"analyzedFiles"`
	CacheHits          int    `json:"cacheHits"`
	Skipped            int    `json:"skipped"`
	FailedFiles        int    `json:"failedFiles"`
	Facts              int    `json:"facts"`
	Edges              int    `json:"edges"`
	Contracts          int    `json:"contracts"`
	EvidenceChecks     int    `json:"evidenceChecks"`
	EvidenceFailures   int    `json:"evidenceFailures"`
	InvalidFacts       int    `json:"invalidFacts"`
	DuplicateFacts     int    `json:"duplicateFacts"`
	UnmatchedContracts int    `json:"unmatchedContracts"`
	ContractTruncated  int    `json:"contractTruncated"`
	UnresolvedEdges    int    `json:"unresolvedEdges"`
	TruncatedEntries   int    `json:"truncatedEntries"`
	Truncated          bool   `json:"truncated"`
	Conflicts          int    `json:"conflicts"`
	Unmapped           int    `json:"unmapped"`
	Candidates         int    `json:"candidates"`
	ChangeSummary      string `json:"changeSummary"`
}

// executeRun is the analysis pipeline. The caller owns the running state and the
// failure persistence.
func (s *AnalysisService) executeRun(ctx context.Context, state *analysisRunState, force bool, runStart time.Time) error {
	analysis := &state.analysis
	info, err := s.deps.Versions.Get(ctx, analysis.VersionID)
	if err != nil {
		return err
	}
	repo, err := s.activeRepo(ctx, analysis.RepoID)
	if err != nil {
		return err
	}
	state.repo = repo

	diff, err := s.deps.Git.Diff(ctx, repo.LocalPath, analysis.BaseCommit, analysis.HeadCommit)
	if err != nil {
		return err
	}
	changeSet, _, err := s.deps.ChangeSets.GetOrCreate(ctx, domain.CodeChangeSet{
		ID:         s.deps.IDs.Next(),
		RepoID:     analysis.RepoID,
		BaseCommit: analysis.BaseCommit,
		HeadCommit: analysis.HeadCommit,
		Stats:      diff.Stats,
		Files:      diff.Files,
		Truncated:  diff.Truncated,
		CreatedAt:  s.now(),
	})
	if err != nil {
		return err
	}
	changeSetID := changeSet.ID
	analysis.ChangeSetID = &changeSetID
	state.stats.ChangedFiles = len(changeSet.Files)
	state.stats.Truncated = changeSet.Truncated
	s.publishChanged(ctx, analysis.VersionID, *analysis, changeSet)

	if len(changeSet.Files) == 0 {
		// base == head (design spec §6.1): done with zero candidates, no LLM and
		// no planner call. Clear any previous candidate set so a repeated run is
		// deterministic.
		if err := s.deps.Candidates.Replace(ctx, analysis.ID, nil); err != nil {
			return err
		}
		return s.finish(ctx, state, nil, ImpactResult{}, nil, false, runStart)
	}

	changedPaths := changedFilePaths(changeSet.Files)
	anchors, err := s.deps.Anchors.ListByPaths(ctx, analysis.VersionID, changedPaths)
	if err != nil {
		return err
	}
	selected := s.selectFiles(changeSet.Files, anchors)
	state.stats.Skipped = len(changeSet.Files) - len(selected)

	results := make([]analysisFileResult, 0, len(selected))
	for _, file := range selected {
		result, err := s.analyzeFile(ctx, state, file, force)
		if err != nil {
			return err
		}
		results = append(results, result)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
	}

	facts, duplicates := mergeMaterializedFacts(results)
	state.stats.DuplicateFacts = duplicates
	state.stats.Facts = len(facts)
	allEdges := make([]validatedAIEdge, 0)
	for _, result := range results {
		allEdges = append(allEdges, result.edges...)
	}
	if summary := joinedChangeSummaries(results); summary != "" {
		state.stats.ChangeSummary = truncateRunesLimit(summary, analysisSummaryMaxRunes)
	}

	factRefs, factEntities, err := s.materializeFacts(ctx, state, facts)
	if err != nil {
		return err
	}
	headFacts, err := s.materializeEdges(ctx, state, factEntities, allEdges)
	if err != nil {
		return err
	}

	requirements, err := s.deps.Versions.ListRequirements(ctx, analysis.VersionID)
	if err != nil {
		return err
	}
	relations, err := s.deps.Versions.ListRelations(ctx, analysis.VersionID)
	if err != nil {
		return err
	}
	factsAtHead, err := s.factsAtHead(ctx, state, changedPaths, factEntities, headFacts)
	if err != nil {
		return err
	}
	contractEdges, err := s.deps.Edges.ListByRel(ctx, analysis.RepoID, analysis.HeadCommit, domain.RelRouteToAPI)
	if err != nil {
		return err
	}
	// The planner sees the aggregated T1 stats (including the joined
	// changeSummary) at Plan time; the final persisted document merges the
	// planner's own counters afterwards (design spec §6.6 input 3 / R28).
	planStats, err := mergeAnalysisStats(state.stats, nil)
	if err != nil {
		return err
	}
	analysisInput := *analysis
	analysisInput.Stats = planStats
	input := ImpactInput{
		Version:      info,
		Analysis:     analysisInput,
		Requirements: requirements,
		Relations:    relations,
		Anchors:      anchors,
		ChangedFiles: changeSet.Files,
		FactsAtHead:  factsAtHead,
		Edges:        contractEdges,
		Degraded:     state.degraded,
	}
	if s.deps.Planner == nil {
		return errors.New("codeintel: impact planner is not configured")
	}
	result, err := s.deps.Planner.Plan(ctx, input)
	if err != nil {
		return err
	}
	candidates := s.prepareCandidates(analysis.ID, result.Candidates)
	if err := s.deps.Candidates.Replace(ctx, analysis.ID, candidates); err != nil {
		return err
	}
	return s.finish(ctx, state, factRefs, result, candidates, state.degraded, runStart)
}

// finish persists the done terminal state, publishes code.impact_computed and
// records the completion audit. The candidate rows must already be persisted by
// the caller (design spec §6.11: candidates have their own transaction). runStart
// is accepted for symmetry with the worker timing and is not part of the
// deterministic stats document.
func (s *AnalysisService) finish(ctx context.Context, state *analysisRunState, factRefs []domain.FactVersionRef, result ImpactResult, candidates []domain.ImpactCandidate, degraded bool, _ time.Time) error {
	analysis := &state.analysis
	state.stats.Unmapped = len(result.Unmapped)
	state.stats.Candidates = len(candidates)
	stats, err := mergeAnalysisStats(state.stats, result.Stats)
	if err != nil {
		return err
	}
	finishedAt := s.now()
	analysis.Status = domain.RunDone
	analysis.Degraded = degraded
	analysis.FactVersions = factRefs
	analysis.Unmapped = result.Unmapped
	analysis.Stats = stats
	analysis.FinishedAt = &finishedAt
	if err := s.deps.Analyses.Update(ctx, *analysis); err != nil {
		return err
	}
	s.publishImpact(ctx, *analysis, candidates, result.Unmapped)
	s.auditCompletion(ctx, *analysis)
	return nil
}

// selectFiles filters the analyzable changed files and applies the deterministic
// order of design spec §6.2: anchored files first, then path ascending, capped at
// MaxChangedFiles. Deleted, binary and hash-less entries are excluded (they are
// counted in stats.skipped).
func (s *AnalysisService) selectFiles(files []domain.ChangedFile, anchors []domain.CodeAnchor) []domain.ChangedFile {
	anchored := make(map[string]struct{}, len(anchors))
	for _, anchor := range anchors {
		anchored[anchor.FilePath] = struct{}{}
	}
	eligible := make([]domain.ChangedFile, 0, len(files))
	for _, file := range files {
		if file.Status == statusChangedFileDeleted || file.Binary || file.HeadBlobHash == "" {
			continue
		}
		eligible = append(eligible, file)
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		_, leftAnchored := anchored[eligible[i].Path]
		_, rightAnchored := anchored[eligible[j].Path]
		if leftAnchored != rightAnchored {
			return leftAnchored
		}
		return eligible[i].Path < eligible[j].Path
	})
	if len(eligible) > s.config.MaxChangedFiles {
		eligible = eligible[:s.config.MaxChangedFiles]
	}
	return eligible
}

// analysisFileResult is one selected file's validated T1 result.
type analysisFileResult struct {
	path          string
	changeSummary string
	facts         []*materializedFact
	edges         []validatedAIEdge
	analyzed      bool
}

// materializedFact is one fact after validation, before versioning. Key is the
// domain identity; Clue reports a low-confidence clue fact (INV-1).
type materializedFact struct {
	Key             string
	Kind            string
	Name            string
	Path            string
	StartLine       int
	EndLine         int
	Signature       string
	Confidence      string
	Summary         string
	Evidence        []domain.Evidence
	Clue            bool
	LinesValid      bool
	InvalidEvidence int
}

// validatedAIEdge is one AI edge after evidence validation. FactPath is the
// analysed file used to resolve component/symbol endpoint identities.
type validatedAIEdge struct {
	From       AIEdgeEndpoint
	To         AIEdgeEndpoint
	Rel        string
	Confidence string
	Evidence   []domain.Evidence
	FactPath   string
}

// analyzeFile runs one file through the cache or the LLM, then validates its
// evidence against commit content. Infrastructure failures (cache repository)
// are returned; file-level failures are recorded in the run state and degrade
// it.
func (s *AnalysisService) analyzeFile(ctx context.Context, state *analysisRunState, file domain.ChangedFile, force bool) (analysisFileResult, error) {
	result := analysisFileResult{path: file.Path}
	analysis := state.analysis

	if !force {
		cached, hit, err := s.deps.Cache.Get(ctx, analysis.RepoID, analysis.HeadCommit, file.Path,
			file.HeadBlobHash, analysis.Model, analysis.PromptVersion)
		if err != nil {
			return result, err
		}
		if hit {
			if decoded, decodeErr := decodeCachedFileAnalysis(cached.Facts); decodeErr == nil {
				state.stats.CacheHits++
				state.stats.AnalyzedFiles++
				result.analyzed = true
				result.changeSummary = decoded.ChangeSummary
				result.facts, result.edges = materializeCachedFileAnalysis(file.Path, decoded)
				return result, nil
			}
			// A corrupt cache document behaves as a miss; the successful
			// re-analysis overwrites it.
		}
	}

	content := s.contentFor(ctx, state, file.Path)
	switch {
	case content.err != nil:
		if isRunContextError(ctx, content.err) {
			return result, content.err
		}
		// Deleted, binary, non-UTF-8 and symlink/submodule files are outside
		// the §6.2 analysable set: counted as skipped, never degraded.
		switch domain.Reason(content.err) {
		case reasonUnsupportedContent, reasonPathNotFile:
			state.stats.Skipped++
			return result, nil
		}
		s.recordFileError(state, file.Path, reasonReadFailed, content.err)
		state.stats.FailedFiles++
		state.degraded = true
		return result, nil
	case content.truncated:
		// Larger than EZTEST_CODEINTEL_MAX_FILE_BYTES: unsupported content.
		state.stats.Skipped++
		return result, nil
	case !content.ok:
		state.stats.Skipped++
		return result, nil
	}

	if s.deps.LLM == nil {
		s.recordLLMUnavailable(state)
		state.stats.FailedFiles++
		state.degraded = true
		return result, nil
	}
	system, user := BuildFilePrompt(state.repo.Key, analysis.HeadCommit, file.Path, content.content)
	raw, err := s.deps.LLM.Complete(ctx, system, user)
	if err != nil {
		if isRunContextError(ctx, err) {
			// A worker deadline must fail the run (analysis_timeout), not turn it
			// into a degraded done analysis.
			return result, err
		}
		code := reasonLLMError
		if errors.Is(err, ErrLLMNotConfigured) {
			code = reasonLLMUnavailable
			s.recordLLMUnavailable(state)
		} else {
			s.recordFileError(state, file.Path, code, err)
		}
		state.stats.FailedFiles++
		state.degraded = true
		return result, nil
	}
	parsed, parseStats, err := ParseFileAnalysis(raw)
	if err != nil {
		s.recordFileError(state, file.Path, reasonParseError, err)
		state.stats.FailedFiles++
		state.degraded = true
		return result, nil
	}
	state.stats.TruncatedEntries += parseStats.TruncatedEntries

	result.facts, result.edges = s.validateFileAnalysis(ctx, state, file.Path, parsed)
	result.changeSummary = parsed.ChangeSummary
	result.analyzed = true
	state.stats.AnalyzedFiles++

	if err := s.storeCachedFileAnalysis(ctx, state, file, result, parseStats); err != nil {
		return result, err
	}
	return result, nil
}

// recordLLMUnavailable records the run-level missing-LLM error once.
func (s *AnalysisService) recordLLMUnavailable(state *analysisRunState) {
	if state.llmErrorRecorded {
		return
	}
	state.llmErrorRecorded = true
	state.addError(domain.AnalysisError{
		Code:    reasonLLMUnavailable,
		Message: "codeintel: LLM is not configured",
	})
}

// recordFileError appends one bounded file-scoped error (never code content).
func (s *AnalysisService) recordFileError(state *analysisRunState, filePath, code string, cause error) {
	state.addError(domain.AnalysisError{
		Code:    code,
		Path:    filePath,
		Message: truncateRunError(cause.Error()),
	})
}

// addError appends one bounded analysis error, capped at MaxAnalysisErrors.
func (state *analysisRunState) addError(entry domain.AnalysisError) {
	if len(state.analysis.Errors) >= domain.MaxAnalysisErrors {
		return
	}
	entry.Message = truncateRunError(entry.Message)
	state.analysis.Errors = append(state.analysis.Errors, entry)
}

// contentFor lazily reads and normalises one commit-pinned file. Errors are
// stored on the entry so fact/evidence validation can degrade individual facts
// without failing the run.
func (s *AnalysisService) contentFor(ctx context.Context, state *analysisRunState, filePath string) *commitContent {
	if entry, ok := state.content[filePath]; ok {
		return entry
	}
	entry := &commitContent{}
	content, truncated, err := s.deps.Git.ReadAll(ctx, state.repo.LocalPath, state.analysis.HeadCommit, filePath, s.config.MaxFileBytes)
	if err != nil {
		entry.err = err
		state.content[filePath] = entry
		return entry
	}
	if truncated {
		entry.truncated = true
		state.content[filePath] = entry
		return entry
	}
	lines, err := commitContentLines(content)
	if err != nil {
		entry.err = err
		state.content[filePath] = entry
		return entry
	}
	entry.content = content
	entry.lines = lines
	entry.ok = true
	state.content[filePath] = entry
	return entry
}

// validateFileAnalysis validates every fact and edge of one parsed file against
// the commit content (design spec §6.2/§6.3): facts with missing/invalid
// evidence or invalid path/line ranges become low clues, invalid facts and edges
// are counted, and only verified evidence is kept.
func (s *AnalysisService) validateFileAnalysis(ctx context.Context, state *analysisRunState, analysedPath string, parsed FileAnalysis) ([]*materializedFact, []validatedAIEdge) {
	facts := make([]*materializedFact, 0, len(parsed.Facts))
	for _, fact := range parsed.Facts {
		normalizedPath, err := normalizeFactEntityPath(fact.Path)
		if err != nil {
			state.stats.InvalidFacts++
			continue
		}
		key, err := domain.FactKey(fact.Kind, fact.Name, normalizedPath)
		if err != nil {
			state.stats.InvalidFacts++
			continue
		}

		linesValid := true
		if content := s.contentFor(ctx, state, normalizedPath); content.err != nil ||
			content.truncated || !content.ok ||
			domain.ValidateEvidenceRange(fact.StartLine, fact.EndLine, len(content.lines), 0) != nil {
			linesValid = false
		}

		evidence := make([]domain.Evidence, 0, len(fact.Evidence))
		invalidEvidence := 0
		for _, raw := range fact.Evidence {
			state.stats.EvidenceChecks++
			evidencePath, pathErr := normalizeRepoRelativePath(raw.File)
			if pathErr != nil {
				state.stats.EvidenceFailures++
				invalidEvidence++
				continue
			}
			content := s.contentFor(ctx, state, evidencePath)
			if content.err != nil || content.truncated || !content.ok {
				state.stats.EvidenceFailures++
				invalidEvidence++
				continue
			}
			normalized := raw
			normalized.File = evidencePath
			validated, err := ValidateEvidence(content.content, normalized, s.config.MaxExcerptLines)
			if err != nil {
				state.stats.EvidenceFailures++
				invalidEvidence++
				continue
			}
			evidence = append(evidence, validated)
		}
		mergedEvidence := mergeContractEvidence(evidence)

		confidence := fact.Confidence
		// §6.2/§6.3: any failed evidence validation (even when another entry
		// survives) forces the fact to a low clue.
		clue := !linesValid || len(mergedEvidence) == 0 || invalidEvidence > 0
		if clue {
			confidence = domain.ConfidenceLow
		}
		facts = append(facts, &materializedFact{
			Key:             key,
			Kind:            fact.Kind,
			Name:            strings.TrimSpace(fact.Name),
			Path:            normalizedPath,
			StartLine:       fact.StartLine,
			EndLine:         fact.EndLine,
			Signature:       fact.Signature,
			Confidence:      confidence,
			Summary:         fact.Summary,
			Evidence:        mergedEvidence,
			Clue:            clue,
			LinesValid:      linesValid,
			InvalidEvidence: invalidEvidence,
		})
	}

	edges := make([]validatedAIEdge, 0, len(parsed.Edges))
	for _, edge := range parsed.Edges {
		if edge.Rel == domain.RelRouteToAPI {
			// Contract edges are generated only by the §6.5 matcher; an AI-emitted
			// route_to_api edge is not trusted.
			continue
		}
		evidence := make([]domain.Evidence, 0, len(edge.Evidence))
		invalidEvidence := 0
		for _, raw := range edge.Evidence {
			state.stats.EvidenceChecks++
			evidencePath, pathErr := normalizeRepoRelativePath(raw.File)
			if pathErr != nil {
				state.stats.EvidenceFailures++
				invalidEvidence++
				continue
			}
			content := s.contentFor(ctx, state, evidencePath)
			if content.err != nil || content.truncated || !content.ok {
				state.stats.EvidenceFailures++
				invalidEvidence++
				continue
			}
			normalized := raw
			normalized.File = evidencePath
			validated, err := ValidateEvidence(content.content, normalized, s.config.MaxExcerptLines)
			if err != nil {
				state.stats.EvidenceFailures++
				invalidEvidence++
				continue
			}
			evidence = append(evidence, validated)
		}
		mergedEvidence := mergeContractEvidence(evidence)
		confidence := edge.Confidence
		// Any invalid edge evidence entry downgrades the edge (§6.2/§6.3),
		// not only a fully empty valid set.
		if len(mergedEvidence) == 0 || invalidEvidence > 0 {
			confidence = domain.ConfidenceLow
		}
		edges = append(edges, validatedAIEdge{
			From:       edge.From,
			To:         edge.To,
			Rel:        edge.Rel,
			Confidence: confidence,
			Evidence:   mergedEvidence,
			FactPath:   analysedPath,
		})
	}
	return facts, edges
}

// materializeFacts versions and upserts every merged fact and returns the
// fact-version snapshot plus the current fact entities keyed by fact_key.
//
// The persisted entity row is always resolved back from the repository: a fact
// version bump updates the existing row in place (ON CONFLICT keeps its id), so
// the newly minted candidate id must never leak into code_edges /
// ImpactInput.FactsAtHead.
func (s *AnalysisService) materializeFacts(ctx context.Context, state *analysisRunState, facts []*materializedFact) ([]domain.FactVersionRef, map[string]domain.CodeEntity, error) {
	factEntities := make(map[string]domain.CodeEntity, len(facts))
	refs := make([]domain.FactVersionRef, 0, len(facts))

	for _, fact := range facts {
		entity, version, err := s.materializeFact(ctx, state, fact)
		if err != nil {
			return nil, nil, err
		}
		factEntities[fact.Key] = entity
		refs = append(refs, domain.FactVersionRef{FactKey: fact.Key, Version: version})
	}

	sort.Slice(refs, func(i, j int) bool { return refs[i].FactKey < refs[j].FactKey })
	if len(refs) > domain.MaxFactVersionsSnapshot {
		refs = refs[:domain.MaxFactVersionsSnapshot]
	}
	return refs, factEntities, nil
}

// materializeFact persists one fact and returns the current persisted entity
// snapshot plus its version. An unchanged body reuses the current version; a
// changed body bumps it.
func (s *AnalysisService) materializeFact(ctx context.Context, state *analysisRunState, fact *materializedFact) (domain.CodeEntity, int, error) {
	analysis := state.analysis
	body := marshalFactBody(fact)
	current, currentVersion, err := s.deps.Facts.Current(ctx, analysis.RepoID, analysis.HeadCommit, fact.Key)
	switch {
	case err == nil:
		existingBody, bodyErr := s.deps.Facts.GetVersion(ctx, analysis.RepoID, analysis.HeadCommit, fact.Key, currentVersion)
		if bodyErr != nil {
			return domain.CodeEntity{}, 0, bodyErr
		}
		if jsonSemanticEqual(existingBody, body) {
			return *current, currentVersion, nil
		}
		return s.writeFactVersion(ctx, state, fact, body, currentVersion+1, current)
	case errors.Is(err, domain.ErrNotFound):
		// New fact (or a previous attempt left only the version row): version 1.
		return s.writeFactVersion(ctx, state, fact, body, 1, nil)
	default:
		return domain.CodeEntity{}, 0, err
	}
}

// factIDCollisionAttempts bounds the fresh-id retries of an INSERT whose
// duplicate-key error came from the caller-minted id rather than the fact
// version identity.
const factIDCollisionAttempts = 3

// writeFactVersion appends one fact version, tolerating the partial states left
// by an interrupted earlier attempt (R30). Equal persisted bodies are accepted
// as the same version; a different persisted body is the DB authority (design
// spec §6.11 "以 DB 现有记录为准") and is recorded as stats.conflicts instead of
// wedging every retry on duplicate_key. Finally the upserted entity is read back
// so callers always receive the persisted row id.
func (s *AnalysisService) writeFactVersion(ctx context.Context, state *analysisRunState, fact *materializedFact, body []byte, version int, current *domain.CodeEntity) (domain.CodeEntity, int, error) {
	analysis := state.analysis
	if version > 1 {
		if err := s.deps.Facts.Supersede(ctx, analysis.RepoID, analysis.HeadCommit, fact.Key, version, s.now()); err != nil {
			return domain.CodeEntity{}, 0, err
		}
	}

	persistedFact := fact
	for attempt := 0; ; attempt++ {
		id := s.deps.IDs.Next()
		if id == 0 {
			return domain.CodeEntity{}, 0, errors.New("codeintel: id generator returned a zero fact version id")
		}
		err := s.deps.Facts.Create(ctx, id, analysis.RepoID, analysis.HeadCommit, fact.Key,
			analysis.Model, analysis.PromptVersion, version, body, analysis.ID)
		if err == nil {
			break
		}
		if domain.Reason(err) != "duplicate_key" {
			return domain.CodeEntity{}, 0, err
		}
		stored, getErr := s.deps.Facts.GetVersion(ctx, analysis.RepoID, analysis.HeadCommit, fact.Key, version)
		switch {
		case getErr == nil && jsonSemanticEqual(stored, body):
			// The interrupted attempt left the exact same version body.
		case getErr == nil:
			adopted, decodeErr := materializedFactFromBody(fact.Key, stored)
			if decodeErr != nil {
				return domain.CodeEntity{}, 0,
					fmt.Errorf("codeintel: decode persisted fact %q version %d: %w", fact.Key, version, decodeErr)
			}
			persistedFact = adopted
			state.stats.Conflicts++
		case errors.Is(getErr, domain.ErrNotFound):
			// The duplicate came from another unique constraint (the caller
			// id): mint a fresh snowflake and retry the same version.
			if attempt+1 < factIDCollisionAttempts {
				continue
			}
			return domain.CodeEntity{}, 0, err
		default:
			return domain.CodeEntity{}, 0, getErr
		}
		break
	}

	entity := s.factEntity(state, persistedFact, version)
	if current != nil {
		// ON CONFLICT updates the existing row in place: reuse its id so the
		// edges materialised below never reference the discarded minted id.
		entity.ID = current.ID
		entity.CreatedAt = current.CreatedAt
	}
	if err := s.deps.Entities.Upsert(ctx, entity); err != nil {
		return domain.CodeEntity{}, 0, err
	}
	persisted, err := s.deps.Entities.GetByFactKey(ctx, analysis.RepoID, analysis.HeadCommit, fact.Key)
	if err != nil {
		return domain.CodeEntity{}, 0,
			fmt.Errorf("codeintel: resolve persisted fact %q: %w", fact.Key, err)
	}
	return persisted, version, nil
}

// materializedFactFromBody reconstructs the fact fields of one persisted
// canonical fact body so the DB-authoritative version can refresh the current
// entity after a retry conflict.
func materializedFactFromBody(key string, body []byte) (*materializedFact, error) {
	var document factBodyJSON
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, err
	}
	return &materializedFact{
		Key:             key,
		Kind:            document.Kind,
		Name:            document.Name,
		Path:            document.Path,
		StartLine:       document.StartLine,
		EndLine:         document.EndLine,
		Signature:       document.Signature,
		Confidence:      document.Confidence,
		Summary:         document.Summary,
		Evidence:        document.Evidence,
		Clue:            document.Clue,
		LinesValid:      true,
		InvalidEvidence: document.InvalidEvidence,
	}, nil
}

// factEntity builds the current code_entities snapshot of one fact version. The
// snowflake id is minted by the application layer (D29); the repository keeps
// the existing row id when it updates a fact in place.
func (s *AnalysisService) factEntity(state *analysisRunState, fact *materializedFact, version int) domain.CodeEntity {
	analysis := state.analysis
	key := fact.Key
	versionCopy := version
	analysisID := analysis.ID
	startLine := fact.StartLine
	endLine := fact.EndLine
	now := s.now()
	return domain.CodeEntity{
		ID:          s.deps.IDs.Next(),
		RepoID:      analysis.RepoID,
		Commit:      analysis.HeadCommit,
		Kind:        fact.Kind,
		Path:        fact.Path,
		Name:        fact.Name,
		StartLine:   &startLine,
		EndLine:     &endLine,
		Signature:   fact.Signature,
		Source:      domain.SourceAI,
		Confidence:  fact.Confidence,
		Evidence:    fact.Evidence,
		Meta:        marshalFactMeta(fact),
		FactKey:     &key,
		FactVersion: &versionCopy,
		AnalysisID:  &analysisID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// materializeEdges resolves AI edges against the current fact entities,
// generates the cross-stack contract edges, and replaces the edge set of every
// materialised fact exactly once (design spec §6.2/§6.5). Endpoints that cannot
// be resolved are counted in stats.unresolvedEdges. The returned map is the
// full fact view at head (materialised facts + the head contract facts) handed
// to the planner.
func (s *AnalysisService) materializeEdges(ctx context.Context, state *analysisRunState, factEntities map[string]domain.CodeEntity, edges []validatedAIEdge) (map[string]domain.CodeEntity, error) {
	analysis := state.analysis
	ownerEdges := make(map[string][]domain.CodeEdge, len(factEntities))
	for key := range factEntities {
		ownerEdges[key] = nil
	}

	for _, edge := range edges {
		fromKey, err := domain.FactKey(edge.From.Kind, edge.From.Name, edge.FactPath)
		if err != nil {
			state.stats.UnresolvedEdges++
			continue
		}
		toKey, err := domain.FactKey(edge.To.Kind, edge.To.Name, edge.FactPath)
		if err != nil {
			state.stats.UnresolvedEdges++
			continue
		}
		fromEntity, ok := factEntities[fromKey]
		if !ok {
			state.stats.UnresolvedEdges++
			continue
		}
		toEntity, ok := factEntities[toKey]
		if !ok {
			state.stats.UnresolvedEdges++
			continue
		}
		ownerEdges[fromKey] = append(ownerEdges[fromKey], s.codeEdge(analysis, fromEntity, toEntity, edge.Rel,
			edge.Confidence, edge.Evidence, fromKey, fromEntity, edgeMeta(fromEntity, toEntity)))
	}

	factsAtHead, err := s.loadContractFacts(ctx, analysis.RepoID, analysis.HeadCommit)
	if err != nil {
		return nil, err
	}
	allFacts := make(map[string]domain.CodeEntity, len(factsAtHead)+len(factEntities))
	for _, entity := range factsAtHead {
		if entity.FactKey != nil {
			allFacts[*entity.FactKey] = entity
		}
	}
	for key, entity := range factEntities {
		allFacts[key] = entity
	}
	contractFacts := make([]domain.CodeEntity, 0, len(allFacts))
	for _, entity := range allFacts {
		contractFacts = append(contractFacts, entity)
	}
	sort.Slice(contractFacts, func(i, j int) bool {
		return factKeyOf(contractFacts[i]) < factKeyOf(contractFacts[j])
	})
	matched := matchContractStats(contractFacts)
	state.stats.Contracts = len(matched.pairs)
	state.stats.UnmatchedContracts = matched.unmatched
	state.stats.ContractTruncated = matched.truncated
	// Exactly one owner per matched pair (R31): the client side when this run
	// materialised it, else the api side. A pair materialised by neither side
	// belongs to an earlier analysis and is not rewritten here, so ownership can
	// never be double-written inside one run.
	ownedPairs := make(map[string]struct{}, len(matched.pairs))
	for _, pair := range matched.pairs {
		clientEntity, clientOK := allFacts[pair.ClientFactKey]
		apiEntity, apiOK := allFacts[pair.APIFactKey]
		if !clientOK || !apiOK {
			continue
		}
		ownerKey, owner, ok := contractPairOwner(pair, factEntities)
		if !ok {
			continue
		}
		edgeIdentity := pair.ClientFactKey + "\x00" + pair.APIFactKey
		if _, duplicate := ownedPairs[edgeIdentity]; duplicate {
			continue
		}
		ownedPairs[edgeIdentity] = struct{}{}
		meta := contractEdgeMeta(pair.Method, pair.NormalizedPath)
		ownerEdges[ownerKey] = append(ownerEdges[ownerKey], s.codeEdge(analysis, clientEntity, apiEntity,
			domain.RelRouteToAPI, pair.Confidence, pair.Evidence, ownerKey, owner, meta))
	}

	keys := make([]string, 0, len(factEntities))
	for key := range factEntities {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	edgeCount := 0
	for _, key := range keys {
		entity := factEntities[key]
		version := factVersionOf(entity)
		owns := ownerEdges[key]
		if err := s.deps.Edges.ReplaceFactEdges(ctx, analysis.RepoID, analysis.HeadCommit, key, version, owns); err != nil {
			return nil, err
		}
		edgeCount += len(owns)
	}
	state.stats.Edges = edgeCount
	return allFacts, nil
}

// contractPairOwner applies the deterministic R31 owner preference: a client
// fact materialised by this run wins, otherwise the materialised api fact. A
// pair with no materialised side is left untouched.
func contractPairOwner(pair ContractPair, materialized map[string]domain.CodeEntity) (string, domain.CodeEntity, bool) {
	if entity, ok := materialized[pair.ClientFactKey]; ok {
		return pair.ClientFactKey, entity, true
	}
	if entity, ok := materialized[pair.APIFactKey]; ok {
		return pair.APIFactKey, entity, true
	}
	return "", domain.CodeEntity{}, false
}

// codeEdge builds one materialised AI edge owned by factKey.
func (s *AnalysisService) codeEdge(analysis domain.CodeAnalysis, src, dst domain.CodeEntity, rel, confidence string,
	evidence []domain.Evidence, factKey string, owner domain.CodeEntity, meta []byte) domain.CodeEdge {
	key := factKey
	version := factVersionOf(owner)
	analysisID := analysis.ID
	return domain.CodeEdge{
		ID:          s.deps.IDs.Next(),
		RepoID:      analysis.RepoID,
		Commit:      analysis.HeadCommit,
		SrcEntityID: src.ID,
		DstEntityID: dst.ID,
		Rel:         rel,
		Source:      domain.SourceAI,
		Confidence:  confidence,
		Evidence:    evidence,
		Meta:        meta,
		FactKey:     &key,
		FactVersion: &version,
		AnalysisID:  &analysisID,
	}
}

// loadContractFacts collects the client_call and api fact entities at the head
// commit so the contract matcher can pair a changed side with an unchanged one.
// The scan is bounded by MaxIndexFiles.
func (s *AnalysisService) loadContractFacts(ctx context.Context, repoID int64, commit string) ([]domain.CodeEntity, error) {
	facts := make([]domain.CodeEntity, 0)
	seen := make(map[int64]struct{})
	for _, kind := range []string{domain.KindClientCall, domain.KindAPI} {
		cursor := ""
		for len(facts) < s.config.MaxIndexFiles {
			page, next, more, err := s.deps.Entities.List(ctx, repoID, domain.EntityFilter{
				Commit: commit,
				Kind:   kind,
				Limit:  contractFactPageSize,
				Cursor: cursor,
			})
			if err != nil {
				return nil, err
			}
			for _, entity := range page {
				if entity.FactKey == nil {
					continue
				}
				if _, ok := seen[entity.ID]; ok {
					continue
				}
				seen[entity.ID] = struct{}{}
				facts = append(facts, entity)
			}
			if !more {
				break
			}
			if next == "" || next == cursor {
				return nil, errors.New("codeintel: contract fact cursor did not advance")
			}
			cursor = next
		}
	}
	return facts, nil
}

// factsAtHead loads the current fact entities for the planner handoff (本次分析
// 产出 + 既有缓存/实体, design spec §6.6 input 3). The changed files supply the
// existing facts (from earlier analyses or the file cache); materialized
// supplies every fact produced by this run, including facts the model
// attributed to a file outside the change set (for example a route declared in
// a router file while another file was analysed); headFacts (the bounded
// contract scan) supplies the unchanged counterpart of a contract pair so T13
// can propagate api→client. The facts declared at those counterpart paths are
// loaded too (R38), so T13 can continue client→page/component at the same path.
// Precedence: materialized > headFacts > path scan > companion load.
func (s *AnalysisService) factsAtHead(ctx context.Context, state *analysisRunState, changedPaths []string, materialized, headFacts map[string]domain.CodeEntity) ([]domain.CodeEntity, error) {
	entities, err := s.deps.Entities.ListByPaths(ctx, state.analysis.RepoID, state.analysis.HeadCommit, changedPaths)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]domain.CodeEntity, len(entities)+len(headFacts)+len(materialized))
	for _, entity := range entities {
		if entity.FactKey != nil {
			byKey[*entity.FactKey] = entity
		}
	}
	for key, entity := range headFacts {
		byKey[key] = entity
	}
	for key, entity := range materialized {
		byKey[key] = entity
	}
	// R38: the contract scan only loads the counterpart client_call/api fact
	// itself. Companion facts at the counterpart's path (page/component/…) are
	// what T13 indexes a propagation hit to, so load every fact at the
	// counterpart paths that the changed-file scan did not already supply. The
	// path list and the number of merged companion facts reuse MaxIndexFiles,
	// the existing bound of one analysis' head fact scan.
	companionPaths := contractCounterpartPaths(headFacts, changedPaths, s.config.MaxIndexFiles)
	if len(companionPaths) > 0 {
		companions, err := s.deps.Entities.ListByPaths(ctx, state.analysis.RepoID, state.analysis.HeadCommit, companionPaths)
		if err != nil {
			return nil, err
		}
		seenIDs := make(map[int64]struct{}, len(byKey))
		for _, entity := range byKey {
			if entity.ID != 0 {
				seenIDs[entity.ID] = struct{}{}
			}
		}
		added := 0
		for _, entity := range companions {
			if entity.FactKey == nil {
				continue
			}
			if _, ok := byKey[*entity.FactKey]; ok {
				continue
			}
			if entity.ID != 0 {
				if _, ok := seenIDs[entity.ID]; ok {
					continue
				}
			}
			if added >= s.config.MaxIndexFiles {
				break
			}
			byKey[*entity.FactKey] = entity
			if entity.ID != 0 {
				seenIDs[entity.ID] = struct{}{}
			}
			added++
		}
	}
	facts := make([]domain.CodeEntity, 0, len(byKey))
	for _, entity := range byKey {
		facts = append(facts, entity)
	}
	sort.Slice(facts, func(i, j int) bool {
		if factKeyOf(facts[i]) != factKeyOf(facts[j]) {
			return factKeyOf(facts[i]) < factKeyOf(facts[j])
		}
		return facts[i].ID < facts[j].ID
	})
	return facts, nil
}

// contractCounterpartPaths returns the deterministic, capped set of paths that
// carry an eligible contract fact at head and are not part of the changed-file
// scan. The head contract facts are the client_call/api facts of the bounded
// contract scan; the changed paths are already loaded as a whole by
// factsAtHead, so only the counterpart paths need a second read (R38).
func contractCounterpartPaths(headFacts map[string]domain.CodeEntity, changedPaths []string, limit int) []string {
	changed := make(map[string]struct{}, len(changedPaths))
	for _, path := range changedPaths {
		changed[path] = struct{}{}
	}
	seen := make(map[string]struct{}, len(headFacts))
	paths := make([]string, 0, len(headFacts))
	for _, entity := range headFacts {
		if entity.Kind != domain.KindClientCall && entity.Kind != domain.KindAPI {
			continue
		}
		if entity.Path == "" {
			continue
		}
		if _, ok := changed[entity.Path]; ok {
			continue
		}
		if _, ok := seen[entity.Path]; ok {
			continue
		}
		seen[entity.Path] = struct{}{}
		paths = append(paths, entity.Path)
	}
	sort.Strings(paths)
	if limit > 0 && len(paths) > limit {
		paths = paths[:limit]
	}
	return paths
}

// prepareCandidates assigns the application snowflake ids, the analysis id and
// the default selection of design spec §6.6 step 6 / INV-7.
func (s *AnalysisService) prepareCandidates(analysisID int64, candidates []domain.ImpactCandidate) []domain.ImpactCandidate {
	prepared := make([]domain.ImpactCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		candidate.ID = s.deps.IDs.Next()
		candidate.AnalysisID = analysisID
		candidate.Selected = domain.DefaultSelected(candidate.Confidence)
		if len(candidate.Reason) > domain.MaxReasonChars {
			candidate.Reason = truncateRunesLimit(candidate.Reason, domain.MaxReasonChars)
		}
		prepared = append(prepared, candidate)
	}
	return prepared
}

// storeCachedFileAnalysis caches one successful, evidence-validated file
// analysis under a snowflake id (design spec §6.4/§6.11).
func (s *AnalysisService) storeCachedFileAnalysis(ctx context.Context, state *analysisRunState, file domain.ChangedFile, result analysisFileResult, parseStats ParseStats) error {
	analysis := state.analysis
	document := cachedFileAnalysisFrom(result)
	factsJSON, err := json.Marshal(document)
	if err != nil {
		return err
	}
	statsJSON, err := json.Marshal(parseStats)
	if err != nil {
		return err
	}
	now := s.now()
	return s.deps.Cache.Upsert(ctx, domain.CachedFile{
		ID:            s.deps.IDs.Next(),
		RepoID:        analysis.RepoID,
		Commit:        analysis.HeadCommit,
		Path:          file.Path,
		FileHash:      file.HeadBlobHash,
		Model:         analysis.Model,
		PromptVersion: analysis.PromptVersion,
		Facts:         factsJSON,
		Stats:         statsJSON,
		CreatedAt:     now,
		LastUsedAt:    now,
	})
}

// publishChanged publishes code.changed after the change set is ready. Event
// failures are ignored: the database is authoritative (design spec §6.9).
func (s *AnalysisService) publishChanged(ctx context.Context, versionID int64, analysis domain.CodeAnalysis, changeSet domain.CodeChangeSet) {
	if s.deps.Notifier == nil {
		return
	}
	paths := changedFilePaths(changeSet.Files)
	if len(paths) > eventPathLimit {
		paths = paths[:eventPathLimit]
	}
	_ = s.deps.Notifier.Changed(ctx, versionID, ChangedEvent{
		AnalysisID: analysis.ID,
		BaseCommit: analysis.BaseCommit,
		HeadCommit: analysis.HeadCommit,
		Stats:      changeSet.Stats,
		Paths:      paths,
	})
}

// publishImpact publishes code.impact_computed with counts and evidence paths
// only (never code content, design spec §6.9).
func (s *AnalysisService) publishImpact(ctx context.Context, analysis domain.CodeAnalysis, candidates []domain.ImpactCandidate, unmapped []domain.UnmappedChange) {
	if s.deps.Notifier == nil {
		return
	}
	counts := map[string]int{
		"requirements": 0,
		"appModels":    0,
		"unmapped":     len(unmapped),
		"candidates":   len(candidates),
	}
	pathSet := make(map[string]struct{})
	for _, candidate := range candidates {
		switch candidate.SubjectKind {
		case domain.CandidateRequirement:
			counts["requirements"]++
		case domain.CandidateAppModel:
			counts["appModels"]++
		}
		for _, evidence := range candidate.Evidence {
			if evidence.File != "" {
				pathSet[evidence.File] = struct{}{}
			}
		}
	}
	paths := make([]string, 0, len(pathSet))
	for path := range pathSet {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	if len(paths) > eventPathLimit {
		paths = paths[:eventPathLimit]
	}
	_ = s.deps.Notifier.ImpactComputed(ctx, analysis.VersionID, ImpactEvent{
		AnalysisID:      analysis.ID,
		AnalysisVersion: int64(analysis.AnalysisVersion),
		Status:          analysis.Status,
		Degraded:        analysis.Degraded,
		Counts:          counts,
		Paths:           paths,
	})
}

// auditTrigger records the analysis trigger (or reuse) with identifiers only.
func (s *AnalysisService) auditTrigger(ctx context.Context, actorID int64, analysis domain.CodeAnalysis) {
	if s.deps.Audit == nil {
		return
	}
	_ = s.deps.Audit.Record(ctx, actorID, auditActionCodeAnalyze,
		"analysis:"+strconv.FormatInt(analysis.ID, 10), map[string]any{
			"versionId":  strconv.FormatInt(analysis.VersionID, 10),
			"analysisId": strconv.FormatInt(analysis.ID, 10),
			"baseCommit": analysis.BaseCommit,
			"headCommit": analysis.HeadCommit,
			"status":     analysis.Status,
		})
}

// auditCompletion records the analysis completion with identifiers only.
func (s *AnalysisService) auditCompletion(ctx context.Context, analysis domain.CodeAnalysis) {
	if s.deps.Audit == nil {
		return
	}
	_ = s.deps.Audit.Record(ctx, analysis.CreatedBy, auditActionCodeAnalyze,
		"analysis:"+strconv.FormatInt(analysis.ID, 10), map[string]any{
			"versionId":  strconv.FormatInt(analysis.VersionID, 10),
			"analysisId": strconv.FormatInt(analysis.ID, 10),
			"baseCommit": analysis.BaseCommit,
			"headCommit": analysis.HeadCommit,
			"status":     analysis.Status,
		})
}

// mergeAnalysisStats encodes the file-level stats and overlays the planner's
// own stats keys. The map encoding is deterministic (encoding/json sorts map
// keys), which keeps INV-4 reproducible.
func mergeAnalysisStats(stats analysisStats, extra []byte) ([]byte, error) {
	base, err := json.Marshal(stats)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(extra)) == 0 {
		return base, nil
	}
	var merged map[string]any
	if err := json.Unmarshal(base, &merged); err != nil {
		return nil, err
	}
	var overlay map[string]any
	if err := json.Unmarshal(extra, &overlay); err != nil {
		return nil, fmt.Errorf("codeintel: decode planner stats: %w", err)
	}
	for key, value := range overlay {
		merged[key] = value
	}
	return json.Marshal(merged)
}

// marshalFactBody encodes the canonical body of one fact version (fact + valid
// evidence + summary, no floats; evidence is sorted so the bytes are
// deterministic).
func marshalFactBody(fact *materializedFact) []byte {
	body := factBodyJSON{
		Kind:            fact.Kind,
		Name:            fact.Name,
		Path:            fact.Path,
		StartLine:       fact.StartLine,
		EndLine:         fact.EndLine,
		Signature:       fact.Signature,
		Confidence:      fact.Confidence,
		Summary:         fact.Summary,
		Evidence:        fact.Evidence,
		Clue:            fact.Clue,
		InvalidEvidence: fact.InvalidEvidence,
	}
	raw, _ := json.Marshal(body)
	return raw
}

// marshalFactMeta encodes the JSONB meta of one fact entity.
func marshalFactMeta(fact *materializedFact) []byte {
	meta := factMetaJSON{Clue: fact.Clue, InvalidEvidence: fact.InvalidEvidence}
	if !fact.Clue && fact.InvalidEvidence == 0 {
		return []byte("{}")
	}
	raw, _ := json.Marshal(meta)
	return raw
}

// jsonSemanticEqual compares two JSON documents semantically, so a JSONB
// round-trip (key order, whitespace) never looks like a fact change.
func jsonSemanticEqual(left, right []byte) bool {
	leftValue, err := decodeJSONValue(left)
	if err != nil {
		return false
	}
	rightValue, err := decodeJSONValue(right)
	if err != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

// decodeJSONValue decodes one JSON document with json.Number so integer ids and
// line numbers never pass through float64.
func decodeJSONValue(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

// mergeMaterializedFacts merges same-run duplicate fact_keys in file order
// (design spec §6.4): the higher confidence wins, evidence is merged and the
// first non-empty summary is kept. The result is sorted by fact_key.
func mergeMaterializedFacts(results []analysisFileResult) ([]*materializedFact, int) {
	byKey := make(map[string]*materializedFact)
	duplicates := 0
	for _, result := range results {
		for _, fact := range result.facts {
			existing, ok := byKey[fact.Key]
			if !ok {
				copied := *fact
				copied.Evidence = append([]domain.Evidence(nil), fact.Evidence...)
				byKey[fact.Key] = &copied
				continue
			}
			duplicates++
			existing.Evidence = mergeContractEvidence(existing.Evidence, fact.Evidence)
			if confidenceRankForContract(fact.Confidence) < confidenceRankForContract(existing.Confidence) {
				existing.Confidence = fact.Confidence
			}
			if existing.Summary == "" {
				existing.Summary = fact.Summary
			}
			existing.LinesValid = existing.LinesValid || fact.LinesValid
			existing.InvalidEvidence += fact.InvalidEvidence
			existing.Clue = !existing.LinesValid || len(existing.Evidence) == 0 || existing.InvalidEvidence > 0
			if existing.Clue {
				existing.Confidence = domain.ConfidenceLow
			}
		}
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	facts := make([]*materializedFact, 0, len(keys))
	for _, key := range keys {
		facts = append(facts, byKey[key])
	}
	return facts, duplicates
}

// joinedChangeSummaries joins the per-file change summaries in deterministic
// file order.
func joinedChangeSummaries(results []analysisFileResult) string {
	ordered := make([]analysisFileResult, len(results))
	copy(ordered, results)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].path < ordered[j].path })
	parts := make([]string, 0, len(ordered))
	for _, result := range ordered {
		summary := strings.TrimSpace(result.changeSummary)
		if summary != "" {
			parts = append(parts, summary)
		}
	}
	return strings.Join(parts, "；")
}

// changedFilePaths returns the sorted paths of the change set.
func changedFilePaths(files []domain.ChangedFile) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	sort.Strings(paths)
	return paths
}

// normalizeRepoRelativePath validates and cleans one repository-relative slash
// path. It rejects empty, absolute and escaping paths and bounds the length; the
// fact identity additionally rejects '#' (see normalizeFactEntityPath), while
// evidence may legitimately reference a file whose name contains '#'.
func normalizeRepoRelativePath(raw string) (string, error) {
	normalized := strings.TrimSpace(strings.ReplaceAll(raw, `\`, "/"))
	if normalized == "" {
		return "", errors.New("codeintel: path is required")
	}
	if strings.HasPrefix(normalized, "/") || path.IsAbs(normalized) {
		return "", errors.New("codeintel: path must be repository-relative")
	}
	cleaned := path.Clean(normalized)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("codeintel: path escapes the repository root")
	}
	if len(cleaned) > domain.MaxPathChars {
		return "", errors.New("codeintel: path exceeds the path bound")
	}
	return cleaned, nil
}

// normalizeFactEntityPath validates and cleans one fact path so an entity row can
// never point outside the repository or exceed the DDL bound.
func normalizeFactEntityPath(raw string) (string, error) {
	cleaned, err := normalizeRepoRelativePath(raw)
	if err != nil {
		return "", err
	}
	if strings.Contains(cleaned, "#") {
		return "", errors.New("codeintel: fact path must not contain '#'")
	}
	return cleaned, nil
}

// isRunContextError reports whether err (or the run context) is a cancellation
// or deadline, which must fail the run rather than degrade a single file.
func isRunContextError(ctx context.Context, err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	if ctx == nil {
		return false
	}
	return errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled)
}

// truncateRunesLimit cuts s to at most limit runes and marks the cut.
func truncateRunesLimit(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}

// edgeMeta encodes the JSONB meta of one AI edge.
func edgeMeta(from, to domain.CodeEntity) []byte {
	raw, _ := json.Marshal(aiEdgeMetaJSON{
		From: cachedEdgeEndpointJSON{Kind: from.Kind, Name: from.Name},
		To:   cachedEdgeEndpointJSON{Kind: to.Kind, Name: to.Name},
	})
	return raw
}

// contractEdgeMeta encodes the design spec §6.5 contract edge meta.
func contractEdgeMeta(method, normalizedPath string) []byte {
	raw, _ := json.Marshal(contractEdgeMetaJSON{
		Method:         method,
		NormalizedPath: normalizedPath,
		Match:          "exact",
	})
	return raw
}

// now returns the injected clock time (falling back to wall time when a
// partially wired process leaves Clock nil).
func (s *AnalysisService) now() time.Time {
	if s.deps.Clock != nil {
		return s.deps.Clock.Now()
	}
	return time.Now().UTC()
}

// rememberAnalysis records the version/force of a queued analysis.
func (s *AnalysisService) rememberAnalysis(analysisID, versionID int64, force bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[analysisID] = analysisRunRef{versionID: versionID, force: force}
}

// analysisRef returns the registry entry of one analysis.
func (s *AnalysisService) analysisRef(analysisID int64) (analysisRunRef, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ref, ok := s.runs[analysisID]
	return ref, ok
}

// forgetAnalysis drops one registry entry.
func (s *AnalysisService) forgetAnalysis(analysisID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.runs, analysisID)
}

// json shapes -----------------------------------------------------------------

// cachedFileAnalysis is the JSON document stored in code_analysis_cache.facts.
// It is the validated file result (facts + edges + change summary), so a cache
// hit reproduces the analysis without trusting model output again.
type cachedFileAnalysis struct {
	ChangeSummary string           `json:"changeSummary"`
	Facts         []cachedFactJSON `json:"facts"`
	Edges         []cachedEdgeJSON `json:"edges"`
}

type cachedFactJSON struct {
	Kind            string            `json:"kind"`
	Name            string            `json:"name"`
	Path            string            `json:"path"`
	StartLine       int               `json:"startLine"`
	EndLine         int               `json:"endLine"`
	Signature       string            `json:"signature,omitempty"`
	Confidence      string            `json:"confidence"`
	Summary         string            `json:"summary,omitempty"`
	Evidence        []domain.Evidence `json:"evidence"`
	Clue            bool              `json:"clue,omitempty"`
	InvalidEvidence int               `json:"invalidEvidence,omitempty"`
}

type cachedEdgeEndpointJSON struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type cachedEdgeJSON struct {
	From       cachedEdgeEndpointJSON `json:"from"`
	To         cachedEdgeEndpointJSON `json:"to"`
	Rel        string                 `json:"rel"`
	Confidence string                 `json:"confidence"`
	Evidence   []domain.Evidence      `json:"evidence"`
}

type factBodyJSON struct {
	Kind            string            `json:"kind"`
	Name            string            `json:"name"`
	Path            string            `json:"path"`
	StartLine       int               `json:"startLine"`
	EndLine         int               `json:"endLine"`
	Signature       string            `json:"signature,omitempty"`
	Confidence      string            `json:"confidence"`
	Summary         string            `json:"summary,omitempty"`
	Evidence        []domain.Evidence `json:"evidence"`
	Clue            bool              `json:"clue,omitempty"`
	InvalidEvidence int               `json:"invalidEvidence,omitempty"`
}

type factMetaJSON struct {
	Clue            bool `json:"clue,omitempty"`
	InvalidEvidence int  `json:"invalidEvidence,omitempty"`
}

type aiEdgeMetaJSON struct {
	From cachedEdgeEndpointJSON `json:"from"`
	To   cachedEdgeEndpointJSON `json:"to"`
}

type contractEdgeMetaJSON struct {
	Method         string `json:"method"`
	NormalizedPath string `json:"normalizedPath"`
	Match          string `json:"match"`
}

// cachedFileAnalysisFrom projects one validated file result onto the cache
// document.
func cachedFileAnalysisFrom(result analysisFileResult) cachedFileAnalysis {
	document := cachedFileAnalysis{ChangeSummary: result.changeSummary}
	for _, fact := range result.facts {
		document.Facts = append(document.Facts, cachedFactJSON{
			Kind:            fact.Kind,
			Name:            fact.Name,
			Path:            fact.Path,
			StartLine:       fact.StartLine,
			EndLine:         fact.EndLine,
			Signature:       fact.Signature,
			Confidence:      fact.Confidence,
			Summary:         fact.Summary,
			Evidence:        fact.Evidence,
			Clue:            fact.Clue,
			InvalidEvidence: fact.InvalidEvidence,
		})
	}
	for _, edge := range result.edges {
		document.Edges = append(document.Edges, cachedEdgeJSON{
			From:       cachedEdgeEndpointJSON{Kind: edge.From.Kind, Name: edge.From.Name},
			To:         cachedEdgeEndpointJSON{Kind: edge.To.Kind, Name: edge.To.Name},
			Rel:        edge.Rel,
			Confidence: edge.Confidence,
			Evidence:   edge.Evidence,
		})
	}
	if document.Facts == nil {
		document.Facts = []cachedFactJSON{}
	}
	if document.Edges == nil {
		document.Edges = []cachedEdgeJSON{}
	}
	return document
}

// decodeCachedFileAnalysis decodes one stored cache document.
func decodeCachedFileAnalysis(raw []byte) (cachedFileAnalysis, error) {
	var document cachedFileAnalysis
	if err := json.Unmarshal(raw, &document); err != nil {
		return cachedFileAnalysis{}, err
	}
	return document, nil
}

// materializeCachedFileAnalysis converts one cached document back into the
// materialisation input. Fact identities are recomputed through the domain
// rules so a stale cache cannot inject an invalid entity.
func materializeCachedFileAnalysis(analysedPath string, document cachedFileAnalysis) ([]*materializedFact, []validatedAIEdge) {
	facts := make([]*materializedFact, 0, len(document.Facts))
	for _, fact := range document.Facts {
		normalizedPath, err := normalizeFactEntityPath(fact.Path)
		if err != nil {
			continue
		}
		key, err := domain.FactKey(fact.Kind, fact.Name, normalizedPath)
		if err != nil {
			continue
		}
		evidence := mergeContractEvidence(fact.Evidence)
		clue := fact.Clue || len(evidence) == 0 || fact.InvalidEvidence > 0
		confidence := fact.Confidence
		if clue {
			confidence = domain.ConfidenceLow
		}
		facts = append(facts, &materializedFact{
			Key:             key,
			Kind:            fact.Kind,
			Name:            strings.TrimSpace(fact.Name),
			Path:            normalizedPath,
			StartLine:       fact.StartLine,
			EndLine:         fact.EndLine,
			Signature:       fact.Signature,
			Confidence:      confidence,
			Summary:         fact.Summary,
			Evidence:        evidence,
			Clue:            clue,
			LinesValid:      true,
			InvalidEvidence: fact.InvalidEvidence,
		})
	}
	edges := make([]validatedAIEdge, 0, len(document.Edges))
	for _, edge := range document.Edges {
		if edge.Rel == domain.RelRouteToAPI {
			continue
		}
		evidence := mergeContractEvidence(edge.Evidence)
		confidence := edge.Confidence
		if len(evidence) == 0 {
			confidence = domain.ConfidenceLow
		}
		edges = append(edges, validatedAIEdge{
			From:       AIEdgeEndpoint{Kind: edge.From.Kind, Name: edge.From.Name},
			To:         AIEdgeEndpoint{Kind: edge.To.Kind, Name: edge.To.Name},
			Rel:        edge.Rel,
			Confidence: confidence,
			Evidence:   evidence,
			FactPath:   analysedPath,
		})
	}
	return facts, edges
}
