package main

// P5 composition root: the codeintel context, its cross-context adapters, the
// dedicated codeintel worker and the agent P5 tool deps. This file is the only
// place that knows the bounded contexts it wires together; the contexts
// themselves stay independent (Agent.md §6.2).

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/gitx"
	codeintellm "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/llm"
	codeintelpg "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/postgres"
	codeintelapi "github.com/EdouardRichard/EzTest/backend/internal/codeintel/interfaces/httpapi"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	executionpg "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/postgres"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	resource "github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/server"
	settingsapp "github.com/EdouardRichard/EzTest/backend/internal/settings/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// codeIntelRuntimeDeps are the P0-P4 services the P5 composition consumes. The
// composition root passes concrete construction-root types; the adapters below
// narrow them to the ports each context declares.
type codeIntelRuntimeDeps struct {
	Pool         *pgxpool.Pool
	LLM          llmapp.ChatGateway
	IDs          platformid.Generator
	Clock        clock.Clock
	Audit        auditapp.Recorder
	Events       eventingapp.Publisher
	Settings     *settingsapp.Service
	Resource     *resourceapp.Service
	Versions     *versionapp.Service
	VersionRepo  versiondomain.Repository
	Requirements *versionapp.RequirementService
	Design       codeIntelDesignReader
	Issues       codeIntelIssueReader
	Execution    *executionpg.Repository
	Content      executionapp.ContentStore
	Queue        *agentapp.WorkQueue
	Bindings     *codeIntelBindings
	InstanceID   string
	// DrainGate mirrors this replica's P7 drain state (orchestration
	// Service.IsDraining); the codeintel worker stops claiming while true.
	DrainGate func() bool
}

// codeIntelRuntime is the constructed P5 surface handed to main.
type codeIntelRuntime struct {
	CodeIntel *server.CodeIntelHandlers
	Tools     tools.CodeIntelDeps
	Worker    *codeintelapp.Worker
}

// newCodeIntelRuntime builds the codeintel context over the shared P0-P4
// infrastructure. Construction failures are programming errors (nil required
// collaborator), so the function panics via error only at the first use; the
// services themselves degrade with stable errors when optional deps are nil.
func newCodeIntelRuntime(ctx context.Context, deps codeIntelRuntimeDeps) *codeIntelRuntime {
	settings := &codeIntelSettings{settings: deps.Settings}
	codeEvents := newCodeIntelEvents(deps.Events)
	codeAudit := newCodeIntelAudit(deps.Audit)
	queue := newCodeIntelQueue(deps.Queue)

	// Codeintel repositories (8 frozen tables).
	indexRuns := codeintelpg.NewIndexRunRepository(deps.Pool)
	entities := codeintelpg.NewEntityRepository(deps.Pool, deps.IDs)
	facts := codeintelpg.NewFactRepository(deps.Pool, deps.IDs)
	analyses := codeintelpg.NewAnalysisRepository(deps.Pool)
	candidates := codeintelpg.NewCandidateRepository(deps.Pool, deps.IDs)
	anchors := codeintelpg.NewAnchorRepository(deps.Pool)
	reviews := codeintelpg.NewReviewRepository(deps.Pool)
	impactEdges := codeintelpg.NewImpactEdgeRepository(deps.Pool)

	extractor := codeintellm.New(deps.LLM)
	designReader := newCodeIntelDesign(deps.Requirements, deps.Design)
	caseSources := newCodeIntelCaseSources(deps.Design)
	subjects := newCodeIntelAnchorSubjects(deps.Requirements, deps.Design, deps.Issues)
	executionReader := newCodeIntelExecution(deps.Execution.Evidence, deps.Execution.CaseRuns, deps.Execution.Waves, deps.Content)
	proposalResources := newCodeIntelProposalResource(deps.Resource)

	indexSvc := codeintelapp.NewIndexService(codeintelapp.IndexDeps{
		Runs: indexRuns, Entities: entities, Git: deps.Bindings, Versions: deps.Bindings,
		Resources: deps.Bindings, Queue: queue, Events: codeEvents, Audit: codeAudit,
		Settings: settings, Clock: deps.Clock, IDs: deps.IDs, InstanceID: deps.InstanceID,
	})
	querySvc := codeintelapp.NewQueryService(codeintelapp.QueryDeps{
		Entities: entities, Git: deps.Bindings, Resources: deps.Bindings, Audit: codeAudit,
		Commits: deps.Bindings, Bulk: deps.Bindings, Settings: settings,
	})
	analysisSvc := codeintelapp.NewAnalysisService(codeintelapp.AnalysisDeps{
		Analyses: analyses, Facts: facts, Entities: entities, Git: deps.Bindings,
		Extractor: extractor, Models: extractor, Bulk: deps.Bindings, Versions: deps.Bindings,
		Resources: deps.Bindings, Runs: indexRuns, Design: designReader, Queue: queue, Events: codeEvents,
		Audit: codeAudit, Settings: settings, Clock: deps.Clock, IDs: deps.IDs,
	})
	impactSvc := codeintelapp.NewImpactService(codeintelapp.ImpactDeps{
		Analyses: analyses, Candidates: candidates, Facts: facts, Entities: entities,
		Anchors: anchors, Reviews: reviews, Edges: impactEdges, CaseSources: caseSources,
		Design: designReader, Resources: deps.Bindings, Versions: deps.Bindings,
		Extractor: extractor, Bulk: deps.Bindings, Subjects: subjects,
		Events: codeEvents, Audit: codeAudit, Settings: settings, Clock: deps.Clock, IDs: deps.IDs,
	})
	anchorSvc := codeintelapp.NewAnchorService(codeintelapp.AnchorDeps{
		Anchors: anchors, Git: deps.Bindings, Versions: deps.Bindings, Resources: deps.Bindings,
		Runs: indexRuns, Subjects: subjects, Events: codeEvents, Audit: codeAudit,
		Settings: settings, Clock: deps.Clock, IDs: deps.IDs,
	})
	reviewSvc := codeintelapp.NewReviewService(codeintelapp.ReviewDeps{
		Reviews: reviews, Facts: facts, Versions: deps.Bindings, Impact: impactSvc, Events: codeEvents,
		Audit: codeAudit, Clock: deps.Clock, IDs: deps.IDs,
	})
	proposalSvc := codeintelapp.NewProposalService(codeintelapp.ProposalDeps{
		Analyses: analyses, Facts: facts, Versions: deps.Bindings, Targets: deps.Bindings,
		Resources: proposalResources, Execution: executionReader, Events: codeEvents,
		Audit: codeAudit, Settings: settings, Clock: deps.Clock,
	})

	// The dedicated codeintel worker claims only the `code` team; the agent
	// worker excludes it (agentapp.agentProductTeams).
	concurrency := 1
	if deps.Settings != nil {
		if value, err := deps.Settings.Int(ctx, codeintelapp.SettingMaxConcurrency); err == nil && value >= 1 && value <= 4 {
			concurrency = int(value)
		}
	}
	worker := codeintelapp.NewWorker(queue, map[string]codeintelapp.Handler{
		codeintelapp.WorkTypeIndexRepo:   codeintelapp.IndexWorkHandler{Service: indexSvc},
		codeintelapp.WorkTypeCodeAnalyze: codeintelapp.ImpactWorkHandler{Analysis: codeintelapp.AnalysisWorkHandler{Service: analysisSvc}, Impact: impactSvc},
	},
		codeintelapp.WithWorkerInstanceID(deps.InstanceID),
		codeintelapp.WithWorkerLogger(slog.Default()),
		codeintelapp.WithWorkerConcurrency(concurrency),
		codeintelapp.WithWorkerDrainGate(deps.DrainGate),
	)

	codeHandlers := &server.CodeIntelHandlers{
		Index:     codeintelapi.New(indexSvc, querySvc, codeintelapi.WithAnalysis(analysisSvc), codeintelapi.WithImpact(impactSvc)),
		Anchors:   codeintelapi.NewAnchorHandler(anchorSvc),
		Reviews:   codeintelapi.NewReviewHandler(reviewSvc),
		Impact:    codeintelapi.NewImpactHandler(impactSvc),
		Proposals: codeintelapi.NewProposalHandler(proposalSvc),
	}

	proposalWriter := newCodeIntelSutProposalWriter(deps.Bindings, proposalResources)

	return &codeIntelRuntime{
		CodeIntel: codeHandlers,
		Tools: tools.CodeIntelDeps{
			Versions: deps.Bindings,
			Index:    indexSvc,
			Query:    querySvc,
			Analysis: analysisSvc,
			Impact:   impactSvc,
			Anchors:  anchorSvc,
			Propose:  proposalWriter,
		},
		Worker: worker,
	}
}

// newGitClient builds the read-only T0 git client (argv-only, bounded
// timeout). It is also the version context's CodeBaselineResolver backend.
func newGitClient() *gitx.Git {
	return gitx.New(gitx.ExecRunner{}, gitx.DefaultCommandTimeout)
}

// codeIntelSettings adapts the P0 settings service to the codeintel SettingsReader
// (which additionally needs Raw for the JSON exclude_globs setting).
type codeIntelSettings struct {
	settings *settingsapp.Service
}

func (s *codeIntelSettings) String(ctx context.Context, key string) (string, error) {
	if s == nil || s.settings == nil {
		return "", errors.New("settings service is not configured")
	}
	return s.settings.String(ctx, key)
}

func (s *codeIntelSettings) Int(ctx context.Context, key string) (int64, error) {
	if s == nil || s.settings == nil {
		return 0, errors.New("settings service is not configured")
	}
	return s.settings.Int(ctx, key)
}

func (s *codeIntelSettings) Float(ctx context.Context, key string) (float64, error) {
	if s == nil || s.settings == nil {
		return 0, errors.New("settings service is not configured")
	}
	return s.settings.Float(ctx, key)
}

func (s *codeIntelSettings) Raw(ctx context.Context, key string) (json.RawMessage, error) {
	if s == nil || s.settings == nil {
		return nil, errors.New("settings service is not configured")
	}
	setting, err := s.settings.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return setting.Value, nil
}

/* ------------------------------ code bindings ------------------------------ */

type codeIntelVersionService interface {
	Get(ctx context.Context, id int64) (versionapp.VersionView, error)
}

// codeIntelSUTRepositoryBaselineWriter is the resource-context write path for the
// resolved code baseline (sut_repository.code_baseline_commit, v0.2.4-B).
type codeIntelSUTRepositoryBaselineWriter interface {
	SetSUTRepositoryBaselineCommit(ctx context.Context, sutID, repoID int64, commitSHA string) error
}

type codeIntelRepositoryCatalog interface {
	GetRepository(ctx context.Context, id int64) (resourceapp.CodeRepositoryView, error)
}

// codeIntelCachedBinding is the SUT -> resource mapping the codeintel
// ResourceReader/ProposalTargetReader need. The mapping is only available from
// the version read model (the SUT catalog of the bound environment), so it is
// captured whenever SUTRepoBindings runs. Every codeintel impact/proposal
// path reads the bindings before it asks for resources.
type codeIntelCachedBinding struct {
	SUTID   int64
	BaseURL string
	Pages   []codeintelapp.PageRef
	APIs    []codeintelapp.APIRef
}

// codeIntelBindings implements codeintelapp.VersionReader, ResourceReader,
// ProposalTargetReader and versionapp.CodeBaselineResolver over the version
// context and the read-only git layer.
type codeIntelBindings struct {
	git       *gitx.Git
	resources codeIntelRepositoryCatalog

	mu        sync.RWMutex
	versions  codeIntelVersionService
	baselines codeIntelSUTRepositoryBaselineWriter
	cache     map[int64]codeIntelCachedBinding
}

func newCodeIntelBindings(git *gitx.Git, resources codeIntelRepositoryCatalog) *codeIntelBindings {
	return &codeIntelBindings{git: git, resources: resources, cache: map[int64]codeIntelCachedBinding{}}
}

// SetVersionService completes the construction cycle: the version service needs
// the resolver at build time and the adapter needs the service at call time.
// baselines is the resource application service, which owns the
// sut_repository.code_baseline_commit column (v0.2.4-B).
func (b *codeIntelBindings) SetVersionService(versions codeIntelVersionService, baselines codeIntelSUTRepositoryBaselineWriter) {
	b.versions = versions
	b.baselines = baselines
}

var (
	_ codeintelapp.VersionReader        = (*codeIntelBindings)(nil)
	_ codeintelapp.ResourceReader       = (*codeIntelBindings)(nil)
	_ codeintelapp.CommitReader         = (*codeIntelBindings)(nil)
	_ codeintelapp.FileWindowReader     = (*codeIntelBindings)(nil)
	_ codeintelapp.GitClient            = (*codeIntelBindings)(nil)
	_ codeintelapp.ProposalTargetReader = (*codeIntelBindings)(nil)
	_ versionapp.CodeBaselineResolver   = (*codeIntelBindings)(nil)
)

// SUTRepoBindings maps the version read model onto the codeintel binding port
// and refreshes the SUT resource cache. v0.2.4-B (GD-03): the version binds an
// environment, and the environment code pool is the union of the sut_repository
// rows of that environment's SUTs — already de-duplicated per repository and
// ordered by repository id by the resource catalog. One codeintel binding is
// emitted per pool row, so every (SUT, repository) pair carries the baseline
// pinned for that pair.
func (b *codeIntelBindings) SUTRepoBindings(ctx context.Context, versionID int64) ([]codeintelapp.SUTRepoBinding, error) {
	if b.versions == nil {
		return nil, platform.Unavailable("codeintel version adapter is not configured")
	}
	view, err := b.versions.Get(ctx, versionID)
	if err != nil {
		return nil, err
	}
	catalog := view.Binding
	bindings := make([]codeintelapp.SUTRepoBinding, 0, len(catalog.Repositories))
	for _, repository := range catalog.Repositories {
		repoID := parseCodeIntelID(repository.Repository.ID)
		if repository.SUTID <= 0 || repoID <= 0 {
			continue
		}
		bindings = append(bindings, codeintelapp.SUTRepoBinding{
			SUTID:              repository.SUTID,
			VersionID:          view.ID,
			RepoID:             repoID,
			Branch:             repository.Repository.Branch,
			CodeBaselineCommit: repository.CodeBaselineCommit,
		})
	}
	// U30 scope read-point (v0.2.4-F T11, SC-F4): the SUT resource cache the codeintel
	// impact index / proposal target reads is built from the SINGLE effective-scope
	// projection (EffectiveSUTs), NOT the raw catalog. EffectiveSUTs gates on
	// resourceapp.SUTView.IsApproved and carries only approved/active assets, so a
	// proposed or rejected SUT — e.g. one an Agent registered via sut.register that the
	// evidence gate did not approve — contributes nothing here: its pages/APIs never
	// reach the impact index, and (via the gated environment code pool above) its
	// repository associations never supply the code baseline. Reading the raw
	// catalog.Environment.SUTs instead would leak an unapproved SUT's assets (the
	// mutation TestProposedSUTNotInImpactOrBaseline guards against).
	effective := catalog.EffectiveSUTs()
	cache := make(map[int64]codeIntelCachedBinding, len(effective))
	for _, sut := range effective {
		if sut.SUTID <= 0 {
			continue
		}
		cache[sut.SUTID] = codeIntelCachedBinding{
			SUTID:   sut.SUTID,
			BaseURL: sut.BaseURL,
			Pages:   codeIntelPageRefs(sut.Pages),
			APIs:    codeIntelAPIRefs(sut.APIs),
		}
	}
	b.mu.Lock()
	for id, entry := range cache {
		b.cache[id] = entry
	}
	b.mu.Unlock()
	return bindings, nil
}

// SetSUTRepositoryBaselineCommit delegates to the resource application service,
// which owns sut_repository.code_baseline_commit (v0.2.4-B).
func (b *codeIntelBindings) SetSUTRepositoryBaselineCommit(ctx context.Context, sutID, repoID int64, commit string) error {
	if b.baselines == nil {
		return platform.Unavailable("codeintel baseline writer is not configured")
	}
	return b.baselines.SetSUTRepositoryBaselineCommit(ctx, sutID, repoID, commit)
}

// CodeRepository maps one resource repository row to the codeintel RepoRef.
func (b *codeIntelBindings) CodeRepository(ctx context.Context, repoID int64) (codeintelapp.RepoRef, error) {
	if b.resources == nil {
		return codeintelapp.RepoRef{}, platform.Unavailable("resource catalog is not configured")
	}
	view, err := b.resources.GetRepository(ctx, repoID)
	if err != nil {
		return codeintelapp.RepoRef{}, err
	}
	return codeintelapp.RepoRef{
		ID: parseCodeIntelID(view.ID), Name: view.Name, Path: view.Path, Branch: view.Branch,
		Include: view.Include, Exclude: view.Exclude, Status: view.Status,
		CreatedBy: view.CreatedBy, CreatedAt: view.CreatedAt, UpdatedAt: view.UpdatedAt,
	}, nil
}

// SUTPages returns the pages of one SUT of the version's bound environment from
// the binding cache.
func (b *codeIntelBindings) SUTPages(_ context.Context, sutID int64) ([]codeintelapp.PageRef, error) {
	entry, ok := b.cachedBinding(sutID)
	if !ok {
		return nil, platform.NotFound("sut")
	}
	return append([]codeintelapp.PageRef(nil), entry.Pages...), nil
}

// SUTAPIs returns the APIs of one SUT of the version's bound environment from
// the binding cache.
func (b *codeIntelBindings) SUTAPIs(_ context.Context, sutID int64) ([]codeintelapp.APIRef, error) {
	entry, ok := b.cachedBinding(sutID)
	if !ok {
		return nil, platform.NotFound("sut")
	}
	return append([]codeintelapp.APIRef(nil), entry.APIs...), nil
}

// ProposalTarget resolves the resource destination of one SUT of the version's
// bound environment. v0.2.4-B: pages and APIs are owned by a SUT, so the SUT id
// is the whole destination key.
func (b *codeIntelBindings) ProposalTarget(_ context.Context, sutID int64) (codeintelapp.ProposalTarget, error) {
	entry, ok := b.cachedBinding(sutID)
	if !ok {
		return codeintelapp.ProposalTarget{}, platform.NotFound("sut")
	}
	return codeintelapp.ProposalTarget{
		SUTID: entry.SUTID, BaseURL: entry.BaseURL,
	}, nil
}

func (b *codeIntelBindings) cachedBinding(sutID int64) (codeIntelCachedBinding, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	entry, ok := b.cache[sutID]
	return entry, ok
}

// ResolveHeadCommit resolves the current HEAD of a bound repository
// (versionapp.CodeBaselineResolver).
func (b *codeIntelBindings) ResolveHeadCommit(ctx context.Context, repo resourceapp.CodeRepositoryView) (string, error) {
	if b.git == nil {
		return "", errors.New("code baseline resolver has no git client")
	}
	return b.git.HeadCommit(ctx, repo.Path, repo.Branch)
}

// The remaining methods forward the codeintel git ports to the shared
// read-only client (the concrete gitx.Git also implements them; the adapter is
// kept explicit so the composition-root port surface is visible).

func (b *codeIntelBindings) ResolveCommit(ctx context.Context, repoPath, ref string) (string, error) {
	return b.git.ResolveCommit(ctx, repoPath, ref)
}

func (b *codeIntelBindings) HeadCommit(ctx context.Context, repoPath, branch string) (string, error) {
	return b.git.HeadCommit(ctx, repoPath, branch)
}

func (b *codeIntelBindings) IsAncestor(ctx context.Context, repoPath, base, head string) (bool, error) {
	return b.git.IsAncestor(ctx, repoPath, base, head)
}

func (b *codeIntelBindings) ListTree(ctx context.Context, repoPath, commit string, include, exclude []string, max int) (codeintel.TreeResult, error) {
	return b.git.ListTree(ctx, repoPath, commit, include, exclude, max)
}

func (b *codeIntelBindings) Diff(ctx context.Context, repoPath, base, head string, max int) (codeintel.DiffResult, error) {
	return b.git.Diff(ctx, repoPath, base, head, max)
}

func (b *codeIntelBindings) ReadFile(ctx context.Context, repoPath, commit, path string, start, end, maxBytes int) (codeintel.FileWindow, error) {
	return b.git.ReadFile(ctx, repoPath, commit, path, start, end, maxBytes)
}

func (b *codeIntelBindings) FileDiff(ctx context.Context, repoPath, base, head, path string, maxBytes int) (codeintel.FileDiff, error) {
	return b.git.FileDiff(ctx, repoPath, base, head, path, maxBytes)
}

func (b *codeIntelBindings) Commit(ctx context.Context, repoPath, sha string, maxMessage int) (codeintel.CommitInfo, error) {
	return b.git.Commit(ctx, repoPath, sha, maxMessage)
}

func (b *codeIntelBindings) ReadFileWindow(ctx context.Context, repoPath, commit, path string, maxBytes, maxLines int) (codeintel.FileWindow, error) {
	return b.git.ReadFileWindow(ctx, repoPath, commit, path, maxBytes, maxLines)
}

func codeIntelPageRefs(views []resourceapp.PageView) []codeintelapp.PageRef {
	refs := make([]codeintelapp.PageRef, 0, len(views))
	for _, page := range views {
		refs = append(refs, codeintelapp.PageRef{
			ID: parseCodeIntelID(page.ID), Name: page.Name, RouteTemplate: page.RouteTemplate,
			Status: page.Status, Source: page.Source,
		})
	}
	return refs
}

func codeIntelAPIRefs(views []resourceapp.APIView) []codeintelapp.APIRef {
	refs := make([]codeintelapp.APIRef, 0, len(views))
	for _, api := range views {
		refs = append(refs, codeintelapp.APIRef{
			ID: parseCodeIntelID(api.ID), Name: api.Name, Method: api.Method,
			PathTemplate: api.PathTemplate, Status: api.Status, Source: api.Source,
		})
	}
	return refs
}

func parseCodeIntelID(value string) int64 {
	parsed, err := platformid.Parse(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return parsed
}

/* ------------------------------- work queue ------------------------------- */

// codeIntelWorkQueueClient is the P1 work queue subset the codeintel adapter maps.
// *agentapp.WorkQueue satisfies it.
type codeIntelWorkQueueClient interface {
	Ensure(ctx context.Context, in agent.NewWorkItem) (agent.WorkItem, bool, error)
	ClaimForTeams(ctx context.Context, claimant string, teams []agent.Team) (agent.WorkItem, bool, error)
	MarkRunning(ctx context.Context, id, epoch int64) error
	// Heartbeat renews the claimed lease; zero selects the queue default.
	Heartbeat(ctx context.Context, id, epoch int64, lease time.Duration) error
	// Lease reports the duration ClaimForTeams grants, so a renewal can be
	// floored at the lease the item actually holds.
	Lease() time.Duration
	Complete(ctx context.Context, id, epoch int64) error
	Fail(ctx context.Context, id, epoch int64, cause error, retryable bool) error
	RecoverExpired(ctx context.Context) (int64, error)
}

// codeIntelQueue adapts the local codeintel work item types to the P1 work
// queue (agent.WorkItem) and exposes RecoverExpired to the worker's optional
// recovery loop.
type codeIntelQueue struct {
	queue codeIntelWorkQueueClient
}

func newCodeIntelQueue(queue codeIntelWorkQueueClient) *codeIntelQueue {
	return &codeIntelQueue{queue: queue}
}

var _ codeintelapp.WorkQueue = (*codeIntelQueue)(nil)

func (q *codeIntelQueue) Ensure(ctx context.Context, in codeintelapp.NewWorkItem) (codeintelapp.WorkItem, bool, error) {
	if q.queue == nil {
		return codeintelapp.WorkItem{}, false, platform.Unavailable("work queue is not configured")
	}
	item, created, err := q.queue.Ensure(ctx, agent.NewWorkItem{
		VersionID: in.VersionID, Team: in.Team, Type: in.Type, Priority: in.Priority,
		DedupeKey: in.DedupeKey, Payload: in.Payload, MaxAttempts: in.MaxAttempts,
	})
	if err != nil {
		return codeintelapp.WorkItem{}, false, err
	}
	return toCodeIntelWorkItem(item), created, nil
}

func (q *codeIntelQueue) ClaimForTeams(ctx context.Context, claimant string, teams []string) (codeintelapp.WorkItem, bool, error) {
	if q.queue == nil {
		return codeintelapp.WorkItem{}, false, platform.Unavailable("work queue is not configured")
	}
	agentTeams := make([]agent.Team, 0, len(teams))
	for _, team := range teams {
		agentTeams = append(agentTeams, agent.Team(team))
	}
	item, ok, err := q.queue.ClaimForTeams(ctx, claimant, agentTeams)
	if err != nil || !ok {
		return codeintelapp.WorkItem{}, ok, err
	}
	return toCodeIntelWorkItem(item), true, nil
}

func (q *codeIntelQueue) MarkRunning(ctx context.Context, id, epoch int64) error {
	return q.queue.MarkRunning(ctx, id, epoch)
}

// Renew maps onto the P1 lease heartbeat (H-A16, defect 012 third site). The
// requested lease is HONORED, floored at the lease the queue actually granted:
// the codeintel worker passes its heartbeat INTERVAL, which is shorter than the
// claim, and renewing with it would shrink the lease below the claimed length
// under a running job. Flooring at the queue's own Lease() (the same value
// ClaimForTeams claims with) keeps that call correct while letting a caller that
// legitimately holds a longer lease renew with it — the render worker's fix
// (renderQueueAdapter.Heartbeat), instead of silently discarding the parameter.
func (q *codeIntelQueue) Renew(ctx context.Context, id, epoch int64, lease time.Duration) error {
	if q.queue == nil {
		return platform.Unavailable("work queue is not configured")
	}
	claimed := q.queue.Lease()
	if claimed <= 0 {
		claimed = agentapp.DefaultLease
	}
	if lease < claimed {
		lease = claimed
	}
	return q.queue.Heartbeat(ctx, id, epoch, lease)
}

func (q *codeIntelQueue) Complete(ctx context.Context, id, epoch int64) error {
	return q.queue.Complete(ctx, id, epoch)
}

// Fail maps the codeintel message onto the P1 queue failure policy (the P1
// queue computes its own min(2^attempts, 30s) backoff).
func (q *codeIntelQueue) Fail(ctx context.Context, id, epoch int64, message string, retryable bool, _ time.Duration) error {
	var cause error
	if strings.TrimSpace(message) != "" {
		cause = errors.New(message)
	}
	return q.queue.Fail(ctx, id, epoch, cause, retryable)
}

// RecoverExpired is the optional recovery surface the worker detects.
func (q *codeIntelQueue) RecoverExpired(ctx context.Context) (int64, error) {
	if q.queue == nil {
		return 0, nil
	}
	return q.queue.RecoverExpired(ctx)
}

func toCodeIntelWorkItem(item agent.WorkItem) codeintelapp.WorkItem {
	return codeintelapp.WorkItem{
		ID: item.ID, VersionID: item.VersionID, Type: item.Type, Payload: item.Payload,
		Attempts: item.Attempts, MaxAttempts: item.MaxAttempts, LeaseEpoch: item.LeaseEpoch,
	}
}

/* -------------------------------- eventing -------------------------------- */

// codeIntelEvents publishes codeintel events on the version channel (or
// system when no version is scoped), per spec §4.3/§10.
type codeIntelEvents struct {
	publisher eventingapp.Publisher
}

func newCodeIntelEvents(publisher eventingapp.Publisher) *codeIntelEvents {
	return &codeIntelEvents{publisher: publisher}
}

var _ codeintelapp.EventPublisher = (*codeIntelEvents)(nil)

func (e *codeIntelEvents) Publish(ctx context.Context, event codeintelapp.Event) error {
	if e.publisher == nil {
		return errors.New("event publisher is not configured")
	}
	return e.publisher.Publish(ctx, eventingapp.Event{
		Channel:   codeIntelVersionChannel(event.VersionID),
		Type:      event.Type,
		Scope:     event.Scope,
		Payload:   event.Payload,
		ActorID:   event.ActorID,
		RequestID: event.RequestID,
	})
}

func codeIntelVersionChannel(versionID int64) string {
	if versionID <= 0 {
		return "system"
	}
	return "version:" + platformid.Format(versionID)
}

// codeIntelAudit adapts the codeintel audit port to the P0 audit recorder.
type codeIntelAudit struct {
	recorder auditapp.Recorder
}

func newCodeIntelAudit(recorder auditapp.Recorder) *codeIntelAudit {
	return &codeIntelAudit{recorder: recorder}
}

var _ codeintelapp.AuditRecorder = (*codeIntelAudit)(nil)

func (a *codeIntelAudit) Record(ctx context.Context, entry codeintelapp.AuditEntry) error {
	if a.recorder == nil {
		return nil
	}
	return a.recorder.Record(ctx, auditapp.Event{
		ActorID: entry.ActorID, Action: entry.Action, TargetKind: entry.TargetKind,
		TargetID: entry.TargetID, RequestID: entry.RequestID, Metadata: entry.Metadata,
	})
}

/* ------------------------------ design readers ----------------------------- */

// codeIntelRequirementService is the version requirement surface the design adapters
// consume (*versionapp.RequirementService satisfies it).
type codeIntelRequirementService interface {
	List(ctx context.Context, versionID int64, status string, limit int, beforeID *int64) ([]versionapp.RequirementView, bool, error)
	Get(ctx context.Context, versionID, requirementID int64) (versionapp.RequirementDetail, error)
	ListRelations(ctx context.Context, versionID, requirementID int64) ([]versionapp.RelationView, error)
}

// codeIntelDesignReader is the design persistence read surface the adapters consume
// (*designpg.Repository satisfies it). It is read-only by contract.
type codeIntelDesignReader interface {
	ReadGraph(ctx context.Context, versionID int64) (design.GraphData, error)
	ReadCase(ctx context.Context, versionID, caseID int64, caseVersionID *int64) (design.CaseData, error)
	ReadCaseVersionsByIDs(ctx context.Context, versionID int64, caseVersionIDs []int64) ([]design.CaseVersionSummary, error)
	ListExecutableCases(ctx context.Context, versionID int64) ([]design.ExecutableCase, error)
}

// codeIntelDesign implements codeintelapp.DesignReader.
type codeIntelDesign struct {
	requirements codeIntelRequirementService
	design       codeIntelDesignReader
}

func newCodeIntelDesign(requirements codeIntelRequirementService, reader codeIntelDesignReader) *codeIntelDesign {
	return &codeIntelDesign{requirements: requirements, design: reader}
}

var _ codeintelapp.DesignReader = (*codeIntelDesign)(nil)

// Requirements lists every non-obsolete requirement of the version.
func (d *codeIntelDesign) Requirements(ctx context.Context, versionID int64) ([]codeintelapp.RequirementRef, error) {
	refs := make([]codeintelapp.RequirementRef, 0)
	var beforeID *int64
	for page := 0; page < 100; page++ {
		items, hasMore, err := d.requirements.List(ctx, versionID, "", 100, beforeID)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if item.Status == string(versiondomain.RequirementObsolete) {
				continue
			}
			refs = append(refs, codeintelapp.RequirementRef{ID: item.ID, Title: item.Title, Status: item.Status})
		}
		if !hasMore || len(items) == 0 {
			break
		}
		last := items[len(items)-1].ID
		beforeID = &last
	}
	return refs, nil
}

// CaseVersions lists the current+confirmed case versions of the version.
func (d *codeIntelDesign) CaseVersions(ctx context.Context, versionID int64) ([]codeintelapp.CaseVersionRef, error) {
	cases, err := d.design.ListExecutableCases(ctx, versionID)
	if err != nil {
		return nil, err
	}
	refs := make([]codeintelapp.CaseVersionRef, 0, len(cases))
	for _, item := range cases {
		refs = append(refs, codeintelapp.CaseVersionRef{
			ID: item.Version.ID, CaseID: item.CaseID, RequirementID: item.RequirementID,
			Status: design.CaseStatusConfirmed,
		})
	}
	return refs, nil
}

// RequirementRelations lists the version's requirement relations (one query per
// requirement; P5 versions stay bounded by the design limits).
func (d *codeIntelDesign) RequirementRelations(ctx context.Context, versionID int64) ([]codeintelapp.RequirementRelationRef, error) {
	requirements, err := d.Requirements(ctx, versionID)
	if err != nil {
		return nil, err
	}
	relations := make([]codeintelapp.RequirementRelationRef, 0)
	seen := map[string]bool{}
	for _, requirement := range requirements {
		items, listErr := d.requirements.ListRelations(ctx, versionID, requirement.ID)
		if listErr != nil {
			return nil, listErr
		}
		for _, item := range items {
			key := platformid.Format(item.FromRequirementID) + "|" + platformid.Format(item.ToRequirementID) + "|" + item.Kind
			if seen[key] {
				continue
			}
			seen[key] = true
			relations = append(relations, codeintelapp.RequirementRelationRef{
				FromRequirementID: item.FromRequirementID,
				ToRequirementID:   item.ToRequirementID,
				Kind:              item.Kind,
			})
		}
	}
	return relations, nil
}

// codeIntelCaseSources implements codeintelapp.CaseSourceReader over the design
// case_source rows of the version's current case versions.
type codeIntelCaseSources struct {
	design codeIntelDesignReader
}

func newCodeIntelCaseSources(reader codeIntelDesignReader) *codeIntelCaseSources {
	return &codeIntelCaseSources{design: reader}
}

var _ codeintelapp.CaseSourceReader = (*codeIntelCaseSources)(nil)

func (c *codeIntelCaseSources) CaseSources(ctx context.Context, versionID int64) ([]codeintelapp.CaseSourceRef, error) {
	graph, err := c.design.ReadGraph(ctx, versionID)
	if err != nil {
		return nil, err
	}
	refs := make([]codeintelapp.CaseSourceRef, 0)
	for _, requirement := range graph.Requirements {
		for _, scenario := range requirement.Scenarios {
			for _, caseGraph := range scenario.Cases {
				if caseGraph.Current.ID <= 0 {
					continue
				}
				caseVersionID := caseGraph.Current.ID
				detail, readErr := c.design.ReadCase(ctx, versionID, caseGraph.Case.ID, &caseVersionID)
				if readErr != nil {
					return nil, readErr
				}
				for _, source := range detail.Selected.Sources {
					if source.Kind != design.SourceCode || strings.TrimSpace(source.Path) == "" {
						continue
					}
					refs = append(refs, codeintelapp.CaseSourceRef{
						CaseVersionID: caseVersionID,
						RepoID:        parseCodeIntelID(source.RefID),
						Path:          source.Path,
					})
				}
			}
		}
	}
	return refs, nil
}

// codeIntelIssueReader is the delivery issue read surface (deliverypg.IssueRepository).
type codeIntelIssueReader interface {
	Get(ctx context.Context, versionID, issueID int64) (delivery.Issue, error)
}

// codeIntelAnchorSubjects implements codeintelapp.AnchorSubjectReader.
type codeIntelAnchorSubjects struct {
	requirements codeIntelRequirementService
	design       codeIntelDesignReader
	issues       codeIntelIssueReader
}

func newCodeIntelAnchorSubjects(requirements codeIntelRequirementService, reader codeIntelDesignReader, issues codeIntelIssueReader) *codeIntelAnchorSubjects {
	return &codeIntelAnchorSubjects{requirements: requirements, design: reader, issues: issues}
}

var _ codeintelapp.AnchorSubjectReader = (*codeIntelAnchorSubjects)(nil)

func (s *codeIntelAnchorSubjects) RequirementInVersion(ctx context.Context, versionID, requirementID int64) (bool, error) {
	_, err := s.requirements.Get(ctx, versionID, requirementID)
	return codeIntelSubjectExists(err)
}

func (s *codeIntelAnchorSubjects) RequirementVersionInVersion(ctx context.Context, versionID, requirementVersionID int64) (bool, error) {
	requirements, err := (&codeIntelDesign{requirements: s.requirements}).Requirements(ctx, versionID)
	if err != nil {
		return false, err
	}
	for _, requirement := range requirements {
		detail, getErr := s.requirements.Get(ctx, versionID, requirement.ID)
		if getErr != nil {
			if codeIntelIsNotFound(getErr) {
				continue
			}
			return false, getErr
		}
		if detail.CurrentVersion.ID == requirementVersionID {
			return true, nil
		}
		for _, revision := range detail.History {
			if revision.ID == requirementVersionID {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *codeIntelAnchorSubjects) CaseVersionInVersion(ctx context.Context, versionID, caseVersionID int64) (bool, error) {
	rows, err := s.design.ReadCaseVersionsByIDs(ctx, versionID, []int64{caseVersionID})
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

func (s *codeIntelAnchorSubjects) IssueInVersion(ctx context.Context, versionID, issueID int64) (bool, error) {
	if s.issues == nil {
		return false, nil
	}
	_, err := s.issues.Get(ctx, versionID, issueID)
	return codeIntelSubjectExists(err)
}

func codeIntelSubjectExists(err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	if codeIntelIsNotFound(err) {
		return false, nil
	}
	return false, err
}

func codeIntelIsNotFound(err error) bool {
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		return apiErr.Status == 404
	}
	return false
}

/* --------------------------- execution evidence --------------------------- */

type codeIntelEvidenceSource interface {
	Get(ctx context.Context, id int64) (execution.Evidence, error)
	ListByVersion(ctx context.Context, versionID int64, filter execution.EvidenceFilter) ([]execution.Evidence, bool, error)
}

type codeIntelCaseRunSource interface {
	Get(ctx context.Context, id int64) (execution.CaseRun, error)
}

type codeIntelWaveSource interface {
	Get(ctx context.Context, id int64) (execution.RunWave, error)
}

type codeIntelContentSource interface {
	Open(ctx context.Context, sha256 string) ([]byte, error)
}

// codeIntelExecution implements codeintelapp.ExecutionReader over the
// execution evidence rows and the content ledger. Only metadata and the bounded
// network request list are exposed; evidence bytes never leave this adapter.
type codeIntelExecution struct {
	evidence codeIntelEvidenceSource
	caseRuns codeIntelCaseRunSource
	waves    codeIntelWaveSource
	content  codeIntelContentSource
}

func newCodeIntelExecution(evidence codeIntelEvidenceSource, caseRuns codeIntelCaseRunSource, waves codeIntelWaveSource, content codeIntelContentSource) *codeIntelExecution {
	return &codeIntelExecution{evidence: evidence, caseRuns: caseRuns, waves: waves, content: content}
}

var _ codeintelapp.ExecutionReader = (*codeIntelExecution)(nil)

// codeIntelNetworkPayload mirrors the executor's browser `network` action result.
type codeIntelNetworkPayload struct {
	Items []codeIntelNetworkItem `json:"items"`
}

type codeIntelNetworkItem struct {
	Method string `json:"method"`
	URL    string `json:"url"`
	Status int    `json:"status"`
}

func (r *codeIntelExecution) NetworkEvidence(ctx context.Context, versionID int64) ([]codeintelapp.NetworkRequest, error) {
	if r.evidence == nil || r.content == nil {
		return nil, nil
	}
	requests := make([]codeintelapp.NetworkRequest, 0)
	var beforeID *int64
	for page := 0; page < 20; page++ {
		items, hasMore, err := r.evidence.ListByVersion(ctx, versionID, execution.EvidenceFilter{
			Kind: execution.EvidenceNetwork, BeforeID: beforeID, Limit: 100,
		})
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			data, openErr := r.content.Open(ctx, item.SHA256)
			if openErr != nil || len(data) == 0 {
				continue
			}
			entries := decodeCodeIntelNetworkItems(data)
			for _, entry := range entries {
				if strings.TrimSpace(entry.Method) == "" || strings.TrimSpace(entry.URL) == "" {
					continue
				}
				requests = append(requests, codeintelapp.NetworkRequest{
					ID: item.ID, CaseRunID: item.CaseRunID, SUTID: item.SUTID,
					Method: entry.Method, URL: entry.URL, Status: entry.Status,
				})
			}
			if len(requests) >= 1000 {
				return requests, nil
			}
		}
		if !hasMore || len(items) == 0 {
			break
		}
		last := items[len(items)-1].ID
		beforeID = &last
	}
	return requests, nil
}

func decodeCodeIntelNetworkItems(data []byte) []codeIntelNetworkItem {
	var payload codeIntelNetworkPayload
	if err := json.Unmarshal(data, &payload); err == nil && payload.Items != nil {
		return payload.Items
	}
	var items []codeIntelNetworkItem
	if err := json.Unmarshal(data, &items); err == nil {
		return items
	}
	return nil
}

func (r *codeIntelExecution) EvidenceMeta(ctx context.Context, versionID, evidenceID int64) (codeintelapp.EvidenceRef, error) {
	if r.evidence == nil || r.caseRuns == nil || r.waves == nil {
		return codeintelapp.EvidenceRef{}, platform.Unavailable("execution evidence reader is not configured")
	}
	item, err := r.evidence.Get(ctx, evidenceID)
	if err != nil {
		return codeintelapp.EvidenceRef{}, err
	}
	caseRun, err := r.caseRuns.Get(ctx, item.CaseRunID)
	if err != nil {
		return codeintelapp.EvidenceRef{}, err
	}
	wave, err := r.waves.Get(ctx, caseRun.WaveID)
	if err != nil {
		return codeintelapp.EvidenceRef{}, err
	}
	if wave.VersionID != versionID {
		return codeintelapp.EvidenceRef{}, platform.NotFound("evidence")
	}
	return codeintelapp.EvidenceRef{
		ID: item.ID, CaseRunID: item.CaseRunID, CaseVersionID: caseRun.CaseVersionID,
		SUTID: item.SUTID, Kind: string(item.Kind), MIME: item.MIME,
		SHA256: item.SHA256, SizeBytes: item.SizeBytes,
	}, nil
}

/* ---------------------------- resource proposals --------------------------- */

// codeIntelProposalResource implements codeintelapp.ProposalResourcePort over the
// resource application service. created is computed by pre-listing because the
// service returns existing rows unchanged for approved/rejected resources.
// v0.2.4-B: pages and APIs are SUT-owned, so every call is keyed by the SUT id.
type codeIntelProposalResource struct {
	resources *resourceapp.Service
}

func newCodeIntelProposalResource(resources *resourceapp.Service) *codeIntelProposalResource {
	return &codeIntelProposalResource{resources: resources}
}

var _ codeintelapp.ProposalResourcePort = (*codeIntelProposalResource)(nil)

func (p *codeIntelProposalResource) ProposePage(ctx context.Context, in codeintelapp.ProposalPageInput) (codeintelapp.ProposalItem, bool, error) {
	sutID := in.SUTID
	created := true
	if existing, err := p.resources.ListPages(ctx, sutID); err == nil {
		for _, page := range existing {
			if page.RouteTemplate == in.RouteTemplate || (in.URL != "" && page.URL == in.URL) {
				created = false
				break
			}
		}
	}
	view, err := p.resources.CreateProposedPage(ctx, resourceapp.ProposedPageInput{
		SUTID: sutID, Name: in.Name, URL: in.URL,
		RouteTemplate: in.RouteTemplate, PageType: in.PageType, AuthRole: in.AuthRole,
		Module: in.Module, Source: in.Source,
	})
	if err != nil {
		return codeintelapp.ProposalItem{}, false, err
	}
	return codeintelapp.ProposalItem{
		Kind: "page", ID: parseCodeIntelID(view.ID), Name: view.Name, URL: view.URL,
		RouteTemplate: view.RouteTemplate, Source: view.Source, Status: view.Status,
	}, created, nil
}

func (p *codeIntelProposalResource) ProposeAPI(ctx context.Context, in codeintelapp.ProposalAPIInput) (codeintelapp.ProposalItem, bool, error) {
	sutID := in.SUTID
	created := true
	if existing, err := p.resources.ListAPIs(ctx, sutID); err == nil {
		for _, api := range existing {
			if api.Method == in.Method && api.PathTemplate == in.PathTemplate {
				created = false
				break
			}
		}
	}
	view, err := p.resources.CreateProposedAPI(ctx, resourceapp.ProposedAPIInput{
		SUTID: sutID, Method: in.Method, PathTemplate: in.PathTemplate,
		Name: in.Name, AuthRole: in.AuthRole, SchemaRef: in.SchemaRef, Source: in.Source,
	})
	if err != nil {
		return codeintelapp.ProposalItem{}, false, err
	}
	return codeintelapp.ProposalItem{
		Kind: "api", ID: parseCodeIntelID(view.ID), Name: view.Name, Method: view.Method,
		PathTemplate: view.PathTemplate, Source: view.Source, Status: view.Status,
	}, created, nil
}

// ListProposals lists the proposed pages/APIs of the given SUTs (v0.2.4-B:
// proposals are SUT-scoped).
func (p *codeIntelProposalResource) ListProposals(ctx context.Context, sutIDs []int64) ([]codeintelapp.ProposalItem, error) {
	items := make([]codeintelapp.ProposalItem, 0)
	for _, sutID := range sutIDs {
		pages, err := p.resources.ListPages(ctx, sutID)
		if err != nil {
			return nil, err
		}
		for _, page := range pages {
			if page.Status != resource.PageStatusProposed {
				continue
			}
			items = append(items, codeintelapp.ProposalItem{
				Kind: "page", ID: parseCodeIntelID(page.ID), Name: page.Name, URL: page.URL,
				RouteTemplate: page.RouteTemplate, Source: page.Source, Status: page.Status,
			})
		}
		apis, err := p.resources.ListAPIs(ctx, sutID)
		if err != nil {
			return nil, err
		}
		for _, api := range apis {
			if api.Status != resource.APIStatusProposed {
				continue
			}
			items = append(items, codeintelapp.ProposalItem{
				Kind: "api", ID: parseCodeIntelID(api.ID), Name: api.Name, Method: api.Method,
				PathTemplate: api.PathTemplate, Source: api.Source, Status: api.Status,
			})
		}
	}
	return items, nil
}

// codeIntelSutProposalWriter implements tools.SutProposalWriter for sut.page_propose:
// it resolves the version's bound resource target and writes a status=proposed
// row through the same resource port as the code-fact proposer.
type codeIntelSutProposalWriter struct {
	bindings  *codeIntelBindings
	resources *codeIntelProposalResource
}

func newCodeIntelSutProposalWriter(bindings *codeIntelBindings, resources *codeIntelProposalResource) *codeIntelSutProposalWriter {
	return &codeIntelSutProposalWriter{bindings: bindings, resources: resources}
}

var _ tools.SutProposalWriter = (*codeIntelSutProposalWriter)(nil)

func (w *codeIntelSutProposalWriter) ProposeSUTResource(ctx context.Context, versionID int64, in tools.SutProposalInput) (tools.SutProposalResult, error) {
	bindings, err := w.bindings.SUTRepoBindings(ctx, versionID)
	if err != nil {
		return tools.SutProposalResult{}, err
	}
	target, err := w.chooseTarget(ctx, bindings, in)
	if err != nil {
		return tools.SutProposalResult{}, err
	}
	switch in.Kind {
	case "page":
		item, created, proposeErr := w.resources.ProposePage(ctx, codeintelapp.ProposalPageInput{
			SUTID: target.SUTID, Name: in.Name,
			URL: in.URL, RouteTemplate: in.RouteTemplate, PageType: "page", Source: resource.PageSourceAI,
		})
		if proposeErr != nil {
			return tools.SutProposalResult{}, proposeErr
		}
		return tools.SutProposalResult{Created: created, ID: item.ID, Status: item.Status}, nil
	case "api":
		item, created, proposeErr := w.resources.ProposeAPI(ctx, codeintelapp.ProposalAPIInput{
			SUTID: target.SUTID, Method: in.Method,
			PathTemplate: in.PathTemplate, Name: in.Name, Source: resource.APISourceAI,
		})
		if proposeErr != nil {
			return tools.SutProposalResult{}, proposeErr
		}
		return tools.SutProposalResult{Created: created, ID: item.ID, Status: item.Status}, nil
	default:
		return tools.SutProposalResult{}, platform.Invalid("kind", `must be "page" or "api"`)
	}
}

// chooseTarget prefers the SUT whose base URL matches a proposed page URL;
// otherwise it uses the SUT of the first pool repository (the pool is ordered by
// repository id). A version whose environment has no code pool cannot receive a
// proposal.
func (w *codeIntelSutProposalWriter) chooseTarget(ctx context.Context, bindings []codeintelapp.SUTRepoBinding, in tools.SutProposalInput) (codeintelapp.ProposalTarget, error) {
	if len(bindings) == 0 {
		return codeintelapp.ProposalTarget{}, platform.Invalid("version_id", "the version has no bound SUT")
	}
	targets := make([]codeintelapp.ProposalTarget, 0, len(bindings))
	for _, binding := range bindings {
		target, err := w.bindings.ProposalTarget(ctx, binding.SUTID)
		if err != nil {
			return codeintelapp.ProposalTarget{}, err
		}
		if target.SUTID > 0 {
			targets = append(targets, target)
		}
	}
	if len(targets) == 0 {
		return codeintelapp.ProposalTarget{}, platform.Invalid("version_id", "the version has no environment binding")
	}
	if in.Kind == "page" && strings.TrimSpace(in.URL) != "" {
		if proposed, err := url.Parse(strings.TrimSpace(in.URL)); err == nil && proposed.Host != "" {
			for _, target := range targets {
				if base, parseErr := url.Parse(strings.TrimSpace(target.BaseURL)); parseErr == nil &&
					strings.EqualFold(base.Host, proposed.Host) {
					return target, nil
				}
			}
		}
	}
	return targets[0], nil
}

// Compile-time assertion: the settings adapter satisfies the codeintel port.
var _ codeintelapp.SettingsReader = (*codeIntelSettings)(nil)
