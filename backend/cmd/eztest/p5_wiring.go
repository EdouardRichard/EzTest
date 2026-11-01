package main

// P5 composition root: codeintel + visual contexts, their cross-context
// adapters, the dedicated codeintel worker and the agent P5 tool deps. This
// file is the only place that knows both bounded contexts; the contexts
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
	executionvisual "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/visualadapter"
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
	visualapp "github.com/EdouardRichard/EzTest/backend/internal/visual/application"
	visualpg "github.com/EdouardRichard/EzTest/backend/internal/visual/infrastructure/postgres"
	visualapi "github.com/EdouardRichard/EzTest/backend/internal/visual/interfaces/httpapi"
	"github.com/jackc/pgx/v5/pgxpool"
)

// p5RuntimeDeps are the P0-P4 services the P5 composition consumes. The
// composition root passes concrete construction-root types; the adapters below
// narrow them to the ports each context declares.
type p5RuntimeDeps struct {
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
	Design       p5DesignReader
	Issues       p5IssueReader
	Execution    *executionpg.Repository
	Content      executionapp.ContentStore
	Queue        *agentapp.WorkQueue
	Bindings     *p5CodeBindings
	InstanceID   string
}

// p5Runtime is the constructed P5 surface handed to main.
type p5Runtime struct {
	CodeIntel     *server.CodeIntelHandlers
	Visual        *visualapi.Handler
	VisualConfigs *visualapp.ConfigService
	VisualPort    *executionvisual.VisualPort
	Tools         tools.P5Deps
	Worker        *codeintelapp.Worker
}

// newP5Runtime builds the codeintel and visual contexts over the shared P0-P4
// infrastructure. Construction failures are programming errors (nil required
// collaborator), so the function panics via error only at the first use; the
// services themselves degrade with stable errors when optional deps are nil.
func newP5Runtime(ctx context.Context, deps p5RuntimeDeps) *p5Runtime {
	settings := &p5Settings{settings: deps.Settings}
	codeEvents := newP5CodeIntelEvents(deps.Events)
	codeAudit := newP5CodeIntelAudit(deps.Audit)
	queue := newP5CodeIntelQueue(deps.Queue)

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
	designReader := newP5CodeIntelDesign(deps.Requirements, deps.Design)
	caseSources := newP5CaseSources(deps.Design)
	subjects := newP5AnchorSubjects(deps.Requirements, deps.Design, deps.Issues)
	executionReader := newP5CodeIntelExecution(deps.Execution.Evidence, deps.Execution.CaseRuns, deps.Execution.Waves, deps.Content)
	proposalResources := newP5ProposalResource(deps.Resource)

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
		Resources: deps.Bindings, Design: designReader, Queue: queue, Events: codeEvents,
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
		"index.repo":   codeintelapp.IndexWorkHandler{Service: indexSvc},
		"code.analyze": codeintelapp.ImpactWorkHandler{Analysis: codeintelapp.AnalysisWorkHandler{Service: analysisSvc}, Impact: impactSvc},
	},
		codeintelapp.WithWorkerInstanceID(deps.InstanceID),
		codeintelapp.WithWorkerLogger(slog.Default()),
		codeintelapp.WithWorkerConcurrency(concurrency),
	)

	codeHandlers := &server.CodeIntelHandlers{
		Index:     codeintelapi.New(indexSvc, querySvc, codeintelapi.WithAnalysis(analysisSvc), codeintelapi.WithImpact(impactSvc)),
		Anchors:   codeintelapi.NewAnchorHandler(anchorSvc),
		Reviews:   codeintelapi.NewReviewHandler(reviewSvc),
		Impact:    codeintelapi.NewImpactHandler(impactSvc),
		Proposals: codeintelapi.NewProposalHandler(proposalSvc),
	}

	// Visual context.
	visualRepo := visualpg.NewRepository(deps.Pool)
	visualAudit := newP5VisualAudit(deps.Audit)
	visualEvents := newP5VisualEvents(deps.Events)
	visualCaseReader := newP5VisualCaseReader(deps.Design)
	visualEvidence := executionvisual.NewEvidenceReader(executionvisual.EvidenceReaderDeps{
		Evidence: deps.Execution.Evidence, CaseRuns: deps.Execution.CaseRuns,
		Intents: deps.Execution.Intents, Content: deps.Content,
	})
	visualConfigs := visualapp.NewConfigService(visualapp.ConfigServiceDeps{
		Repo: visualRepo.Assertions, Cases: visualCaseReader, Settings: settings,
		Clock: deps.Clock, IDs: deps.IDs, Audit: visualAudit,
	})
	visualBaselines := visualapp.NewBaselineService(visualapp.BaselineServiceDeps{
		Baselines: visualRepo.Baselines, Assertions: visualRepo.Assertions, Evidence: visualEvidence,
		Settings: settings, Clock: deps.Clock, IDs: deps.IDs, Audit: visualAudit, Events: visualEvents,
	})
	visualDiffs := visualapp.NewDiffService(visualapp.DiffServiceDeps{
		Diffs: visualRepo.Diffs, Assertions: visualRepo.Assertions, Clock: deps.Clock,
		IDs: deps.IDs, Audit: visualAudit, Events: visualEvents,
	})
	visualOverview := visualapp.NewOverviewService(visualapp.OverviewServiceDeps{
		Diffs: visualRepo.Diffs, Clock: deps.Clock,
	})
	visualHandler := visualapi.New(visualapi.Deps{
		Configs: visualConfigs, Baselines: visualBaselines, Diffs: visualDiffs, Overview: visualOverview,
	})
	visualPort := &executionvisual.VisualPort{
		Configs: visualConfigs, Baselines: visualBaselines, Diffs: visualDiffs,
	}

	proposalWriter := newP5SutProposalWriter(deps.Bindings, proposalResources)

	return &p5Runtime{
		CodeIntel:     codeHandlers,
		Visual:        visualHandler,
		VisualConfigs: visualConfigs,
		VisualPort:    visualPort,
		Tools: tools.P5Deps{
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

// newP5GitClient builds the read-only T0 git client (argv-only, bounded
// timeout). It is also the version context's CodeBaselineResolver backend.
func newP5GitClient() *gitx.Git {
	return gitx.New(gitx.ExecRunner{}, gitx.DefaultCommandTimeout)
}

// p5Settings adapts the P0 settings service to the codeintel SettingsReader
// (which additionally needs Raw for the JSON exclude_globs setting). The visual
// reader only needs Float/Int and is satisfied by the same adapter.
type p5Settings struct {
	settings *settingsapp.Service
}

func (s *p5Settings) String(ctx context.Context, key string) (string, error) {
	if s == nil || s.settings == nil {
		return "", errors.New("settings service is not configured")
	}
	return s.settings.String(ctx, key)
}

func (s *p5Settings) Int(ctx context.Context, key string) (int64, error) {
	if s == nil || s.settings == nil {
		return 0, errors.New("settings service is not configured")
	}
	return s.settings.Int(ctx, key)
}

func (s *p5Settings) Float(ctx context.Context, key string) (float64, error) {
	if s == nil || s.settings == nil {
		return 0, errors.New("settings service is not configured")
	}
	return s.settings.Float(ctx, key)
}

func (s *p5Settings) Raw(ctx context.Context, key string) (json.RawMessage, error) {
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

type p5VersionService interface {
	Get(ctx context.Context, id int64) (versionapp.VersionView, error)
}

type p5VersionBaselineRepository interface {
	SetCodeBaselineCommit(ctx context.Context, versionSUTID int64, commitSHA string) error
}

type p5RepositoryCatalog interface {
	GetRepository(ctx context.Context, id int64) (resourceapp.CodeRepositoryView, error)
}

// p5CachedBinding is the version SUT -> resource mapping the codeintel
// ResourceReader/ProposalTargetReader need. The mapping is only available from
// the version read model (binding ID + nested environment/pages/APIs), so it is
// captured whenever VersionSUTBindings runs. Every codeintel impact/proposal
// path reads the bindings before it asks for resources.
type p5CachedBinding struct {
	SUTID         int64
	EnvironmentID int64
	BaseURL       string
	Pages         []codeintelapp.PageRef
	APIs          []codeintelapp.APIRef
}

// p5CodeBindings implements codeintelapp.VersionReader, ResourceReader,
// ProposalTargetReader and versionapp.CodeBaselineResolver over the version
// context and the read-only git layer.
type p5CodeBindings struct {
	git       *gitx.Git
	resources p5RepositoryCatalog

	mu       sync.RWMutex
	versions p5VersionService
	repo     p5VersionBaselineRepository
	cache    map[int64]p5CachedBinding
}

func newP5CodeBindings(git *gitx.Git, resources p5RepositoryCatalog) *p5CodeBindings {
	return &p5CodeBindings{git: git, resources: resources, cache: map[int64]p5CachedBinding{}}
}

// SetVersionService completes the construction cycle: the version service needs
// the resolver at build time and the adapter needs the service at call time.
func (b *p5CodeBindings) SetVersionService(versions p5VersionService, repo p5VersionBaselineRepository) {
	b.versions = versions
	b.repo = repo
}

var (
	_ codeintelapp.VersionReader        = (*p5CodeBindings)(nil)
	_ codeintelapp.ResourceReader       = (*p5CodeBindings)(nil)
	_ codeintelapp.CommitReader         = (*p5CodeBindings)(nil)
	_ codeintelapp.FileWindowReader     = (*p5CodeBindings)(nil)
	_ codeintelapp.GitClient            = (*p5CodeBindings)(nil)
	_ codeintelapp.ProposalTargetReader = (*p5CodeBindings)(nil)
	_ versionapp.CodeBaselineResolver   = (*p5CodeBindings)(nil)
)

// VersionSUTBindings maps the version read model onto the codeintel binding
// port and refreshes the version_sut resource cache.
func (b *p5CodeBindings) VersionSUTBindings(ctx context.Context, versionID int64) ([]codeintelapp.VersionSUTBinding, error) {
	if b.versions == nil {
		return nil, platform.Unavailable("codeintel version adapter is not configured")
	}
	view, err := b.versions.Get(ctx, versionID)
	if err != nil {
		return nil, err
	}
	bindings := make([]codeintelapp.VersionSUTBinding, 0, len(view.Bindings))
	cache := make(map[int64]p5CachedBinding, len(view.Bindings))
	for _, item := range view.Bindings {
		binding := codeintelapp.VersionSUTBinding{
			VersionSUTID:       item.ID,
			VersionID:          view.ID,
			SUTID:              parseP5ID(item.SUT.ID),
			CodeBaselineCommit: item.CodeBaselineCommit,
		}
		if item.Repository != nil {
			binding.RepoID = parseP5ID(item.Repository.ID)
			binding.Branch = item.Repository.Branch
		}
		if item.ID > 0 {
			cache[item.ID] = p5CachedBinding{
				SUTID:         parseP5ID(item.SUT.ID),
				EnvironmentID: parseP5ID(item.Environment.ID),
				BaseURL:       item.Environment.BaseURL,
				Pages:         p5PageRefs(item.Pages),
				APIs:          p5APIRefs(item.APIs),
			}
		}
		bindings = append(bindings, binding)
	}
	b.mu.Lock()
	for id, entry := range cache {
		b.cache[id] = entry
	}
	b.mu.Unlock()
	return bindings, nil
}

// SetCodeBaselineCommit delegates to the version repository.
func (b *p5CodeBindings) SetCodeBaselineCommit(ctx context.Context, versionSUTID int64, commit string) error {
	if b.repo == nil {
		return platform.Unavailable("codeintel version adapter is not configured")
	}
	return b.repo.SetCodeBaselineCommit(ctx, versionSUTID, commit)
}

// CodeRepository maps one resource repository row to the codeintel RepoRef.
func (b *p5CodeBindings) CodeRepository(ctx context.Context, repoID int64) (codeintelapp.RepoRef, error) {
	if b.resources == nil {
		return codeintelapp.RepoRef{}, platform.Unavailable("resource catalog is not configured")
	}
	view, err := b.resources.GetRepository(ctx, repoID)
	if err != nil {
		return codeintelapp.RepoRef{}, err
	}
	return codeintelapp.RepoRef{
		ID: parseP5ID(view.ID), Name: view.Name, Path: view.Path, Branch: view.Branch,
		Include: view.Include, Exclude: view.Exclude, Status: view.Status,
		CreatedBy: view.CreatedBy, CreatedAt: view.CreatedAt, UpdatedAt: view.UpdatedAt,
	}, nil
}

// SUTPages returns the pages of one bound version SUT from the binding cache.
func (b *p5CodeBindings) SUTPages(_ context.Context, versionSUTID int64) ([]codeintelapp.PageRef, error) {
	entry, ok := b.cachedBinding(versionSUTID)
	if !ok {
		return nil, platform.NotFound("version_sut")
	}
	return append([]codeintelapp.PageRef(nil), entry.Pages...), nil
}

// SUTAPIs returns the APIs of one bound version SUT from the binding cache.
func (b *p5CodeBindings) SUTAPIs(_ context.Context, versionSUTID int64) ([]codeintelapp.APIRef, error) {
	entry, ok := b.cachedBinding(versionSUTID)
	if !ok {
		return nil, platform.NotFound("version_sut")
	}
	return append([]codeintelapp.APIRef(nil), entry.APIs...), nil
}

// ProposalTarget resolves the resource destination of one version SUT.
func (b *p5CodeBindings) ProposalTarget(_ context.Context, versionSUTID int64) (codeintelapp.ProposalTarget, error) {
	entry, ok := b.cachedBinding(versionSUTID)
	if !ok {
		return codeintelapp.ProposalTarget{}, platform.NotFound("version_sut")
	}
	return codeintelapp.ProposalTarget{
		VersionSUTID: versionSUTID, SUTID: entry.SUTID,
		EnvironmentID: entry.EnvironmentID, BaseURL: entry.BaseURL,
	}, nil
}

func (b *p5CodeBindings) cachedBinding(versionSUTID int64) (p5CachedBinding, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	entry, ok := b.cache[versionSUTID]
	return entry, ok
}

// ResolveHeadCommit resolves the current HEAD of a bound repository
// (versionapp.CodeBaselineResolver).
func (b *p5CodeBindings) ResolveHeadCommit(ctx context.Context, repo resourceapp.CodeRepositoryView) (string, error) {
	if b.git == nil {
		return "", errors.New("code baseline resolver has no git client")
	}
	return b.git.HeadCommit(ctx, repo.Path, repo.Branch)
}

// The remaining methods forward the codeintel git ports to the shared
// read-only client (the concrete gitx.Git also implements them; the adapter is
// kept explicit so the composition-root port surface is visible).

func (b *p5CodeBindings) ResolveCommit(ctx context.Context, repoPath, ref string) (string, error) {
	return b.git.ResolveCommit(ctx, repoPath, ref)
}

func (b *p5CodeBindings) HeadCommit(ctx context.Context, repoPath, branch string) (string, error) {
	return b.git.HeadCommit(ctx, repoPath, branch)
}

func (b *p5CodeBindings) IsAncestor(ctx context.Context, repoPath, base, head string) (bool, error) {
	return b.git.IsAncestor(ctx, repoPath, base, head)
}

func (b *p5CodeBindings) ListTree(ctx context.Context, repoPath, commit string, include, exclude []string, max int) (codeintel.TreeResult, error) {
	return b.git.ListTree(ctx, repoPath, commit, include, exclude, max)
}

func (b *p5CodeBindings) Diff(ctx context.Context, repoPath, base, head string, max int) (codeintel.DiffResult, error) {
	return b.git.Diff(ctx, repoPath, base, head, max)
}

func (b *p5CodeBindings) ReadFile(ctx context.Context, repoPath, commit, path string, start, end, maxBytes int) (codeintel.FileWindow, error) {
	return b.git.ReadFile(ctx, repoPath, commit, path, start, end, maxBytes)
}

func (b *p5CodeBindings) FileDiff(ctx context.Context, repoPath, base, head, path string, maxBytes int) (codeintel.FileDiff, error) {
	return b.git.FileDiff(ctx, repoPath, base, head, path, maxBytes)
}

func (b *p5CodeBindings) Commit(ctx context.Context, repoPath, sha string, maxMessage int) (codeintel.CommitInfo, error) {
	return b.git.Commit(ctx, repoPath, sha, maxMessage)
}

func (b *p5CodeBindings) ReadFileWindow(ctx context.Context, repoPath, commit, path string, maxBytes, maxLines int) (codeintel.FileWindow, error) {
	return b.git.ReadFileWindow(ctx, repoPath, commit, path, maxBytes, maxLines)
}

func p5PageRefs(views []resourceapp.PageView) []codeintelapp.PageRef {
	refs := make([]codeintelapp.PageRef, 0, len(views))
	for _, page := range views {
		refs = append(refs, codeintelapp.PageRef{
			ID: parseP5ID(page.ID), Name: page.Name, RouteTemplate: page.RouteTemplate,
			Status: page.Status, Source: page.Source,
		})
	}
	return refs
}

func p5APIRefs(views []resourceapp.APIView) []codeintelapp.APIRef {
	refs := make([]codeintelapp.APIRef, 0, len(views))
	for _, api := range views {
		refs = append(refs, codeintelapp.APIRef{
			ID: parseP5ID(api.ID), Name: api.Name, Method: api.Method,
			PathTemplate: api.PathTemplate, Status: api.Status, Source: api.Source,
		})
	}
	return refs
}

func parseP5ID(value string) int64 {
	parsed, err := platformid.Parse(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return parsed
}

/* ------------------------------- work queue ------------------------------- */

// p5WorkQueueClient is the P1 work queue subset the codeintel adapter maps.
// *agentapp.WorkQueue satisfies it.
type p5WorkQueueClient interface {
	Ensure(ctx context.Context, in agent.NewWorkItem) (agent.WorkItem, bool, error)
	ClaimForTeams(ctx context.Context, claimant string, teams []agent.Team) (agent.WorkItem, bool, error)
	MarkRunning(ctx context.Context, id, epoch int64) error
	Heartbeat(ctx context.Context, id, epoch int64) error
	Complete(ctx context.Context, id, epoch int64) error
	Fail(ctx context.Context, id, epoch int64, cause error, retryable bool) error
	RecoverExpired(ctx context.Context) (int64, error)
}

// p5CodeIntelQueue adapts the local codeintel work item types to the P1 work
// queue (agent.WorkItem) and exposes RecoverExpired to the worker's optional
// recovery loop.
type p5CodeIntelQueue struct {
	queue p5WorkQueueClient
}

func newP5CodeIntelQueue(queue p5WorkQueueClient) *p5CodeIntelQueue {
	return &p5CodeIntelQueue{queue: queue}
}

var _ codeintelapp.WorkQueue = (*p5CodeIntelQueue)(nil)

func (q *p5CodeIntelQueue) Ensure(ctx context.Context, in codeintelapp.NewWorkItem) (codeintelapp.WorkItem, bool, error) {
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
	return p5ToCodeIntelWorkItem(item), created, nil
}

func (q *p5CodeIntelQueue) ClaimForTeams(ctx context.Context, claimant string, teams []string) (codeintelapp.WorkItem, bool, error) {
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
	return p5ToCodeIntelWorkItem(item), true, nil
}

func (q *p5CodeIntelQueue) MarkRunning(ctx context.Context, id, epoch int64) error {
	return q.queue.MarkRunning(ctx, id, epoch)
}

// Renew maps onto the P1 lease heartbeat; the P1 queue owns the lease length.
func (q *p5CodeIntelQueue) Renew(ctx context.Context, id, epoch int64, _ time.Duration) error {
	return q.queue.Heartbeat(ctx, id, epoch)
}

func (q *p5CodeIntelQueue) Complete(ctx context.Context, id, epoch int64) error {
	return q.queue.Complete(ctx, id, epoch)
}

// Fail maps the codeintel message onto the P1 queue failure policy (the P1
// queue computes its own min(2^attempts, 30s) backoff).
func (q *p5CodeIntelQueue) Fail(ctx context.Context, id, epoch int64, message string, retryable bool, _ time.Duration) error {
	var cause error
	if strings.TrimSpace(message) != "" {
		cause = errors.New(message)
	}
	return q.queue.Fail(ctx, id, epoch, cause, retryable)
}

// RecoverExpired is the optional recovery surface the worker detects.
func (q *p5CodeIntelQueue) RecoverExpired(ctx context.Context) (int64, error) {
	if q.queue == nil {
		return 0, nil
	}
	return q.queue.RecoverExpired(ctx)
}

func p5ToCodeIntelWorkItem(item agent.WorkItem) codeintelapp.WorkItem {
	return codeintelapp.WorkItem{
		ID: item.ID, VersionID: item.VersionID, Type: item.Type, Payload: item.Payload,
		Attempts: item.Attempts, MaxAttempts: item.MaxAttempts, LeaseEpoch: item.LeaseEpoch,
	}
}

/* -------------------------------- eventing -------------------------------- */

// p5CodeIntelEvents publishes codeintel events on the version channel (or
// system when no version is scoped), per spec §4.3/§10.
type p5CodeIntelEvents struct {
	publisher eventingapp.Publisher
}

func newP5CodeIntelEvents(publisher eventingapp.Publisher) *p5CodeIntelEvents {
	return &p5CodeIntelEvents{publisher: publisher}
}

var _ codeintelapp.EventPublisher = (*p5CodeIntelEvents)(nil)

func (e *p5CodeIntelEvents) Publish(ctx context.Context, event codeintelapp.Event) error {
	if e.publisher == nil {
		return errors.New("event publisher is not configured")
	}
	return e.publisher.Publish(ctx, eventingapp.Event{
		Channel:   p5VersionChannel(event.VersionID),
		Type:      event.Type,
		Scope:     event.Scope,
		Payload:   event.Payload,
		ActorID:   event.ActorID,
		RequestID: event.RequestID,
	})
}

func p5VersionChannel(versionID int64) string {
	if versionID <= 0 {
		return "system"
	}
	return "version:" + platformid.Format(versionID)
}

// p5CodeIntelAudit adapts the codeintel audit port to the P0 audit recorder.
type p5CodeIntelAudit struct {
	recorder auditapp.Recorder
}

func newP5CodeIntelAudit(recorder auditapp.Recorder) *p5CodeIntelAudit {
	return &p5CodeIntelAudit{recorder: recorder}
}

var _ codeintelapp.AuditRecorder = (*p5CodeIntelAudit)(nil)

func (a *p5CodeIntelAudit) Record(ctx context.Context, entry codeintelapp.AuditEntry) error {
	if a.recorder == nil {
		return nil
	}
	return a.recorder.Record(ctx, auditapp.Event{
		ActorID: entry.ActorID, Action: entry.Action, TargetKind: entry.TargetKind,
		TargetID: entry.TargetID, RequestID: entry.RequestID, Metadata: entry.Metadata,
	})
}

/* ------------------------------ design readers ----------------------------- */

// p5RequirementService is the version requirement surface the design adapters
// consume (*versionapp.RequirementService satisfies it).
type p5RequirementService interface {
	List(ctx context.Context, versionID int64, status string, limit int, beforeID *int64) ([]versionapp.RequirementView, bool, error)
	Get(ctx context.Context, versionID, requirementID int64) (versionapp.RequirementDetail, error)
	ListRelations(ctx context.Context, versionID, requirementID int64) ([]versionapp.RelationView, error)
}

// p5DesignReader is the design persistence read surface the adapters consume
// (*designpg.Repository satisfies it). It is read-only by contract.
type p5DesignReader interface {
	ReadGraph(ctx context.Context, versionID int64) (design.GraphData, error)
	ReadCase(ctx context.Context, versionID, caseID int64, caseVersionID *int64) (design.CaseData, error)
	ReadCaseVersionsByIDs(ctx context.Context, versionID int64, caseVersionIDs []int64) ([]design.CaseVersionSummary, error)
	ListExecutableCases(ctx context.Context, versionID int64) ([]design.ExecutableCase, error)
}

// p5CodeIntelDesign implements codeintelapp.DesignReader.
type p5CodeIntelDesign struct {
	requirements p5RequirementService
	design       p5DesignReader
}

func newP5CodeIntelDesign(requirements p5RequirementService, reader p5DesignReader) *p5CodeIntelDesign {
	return &p5CodeIntelDesign{requirements: requirements, design: reader}
}

var _ codeintelapp.DesignReader = (*p5CodeIntelDesign)(nil)

// Requirements lists every non-obsolete requirement of the version.
func (d *p5CodeIntelDesign) Requirements(ctx context.Context, versionID int64) ([]codeintelapp.RequirementRef, error) {
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
func (d *p5CodeIntelDesign) CaseVersions(ctx context.Context, versionID int64) ([]codeintelapp.CaseVersionRef, error) {
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
func (d *p5CodeIntelDesign) RequirementRelations(ctx context.Context, versionID int64) ([]codeintelapp.RequirementRelationRef, error) {
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

// p5CaseSources implements codeintelapp.CaseSourceReader over the design
// case_source rows of the version's current case versions.
type p5CaseSources struct {
	design p5DesignReader
}

func newP5CaseSources(reader p5DesignReader) *p5CaseSources {
	return &p5CaseSources{design: reader}
}

var _ codeintelapp.CaseSourceReader = (*p5CaseSources)(nil)

func (c *p5CaseSources) CaseSources(ctx context.Context, versionID int64) ([]codeintelapp.CaseSourceRef, error) {
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
						RepoID:        parseP5ID(source.RefID),
						Path:          source.Path,
					})
				}
			}
		}
	}
	return refs, nil
}

// p5IssueReader is the delivery issue read surface (deliverypg.IssueRepository).
type p5IssueReader interface {
	Get(ctx context.Context, versionID, issueID int64) (delivery.Issue, error)
}

// p5AnchorSubjects implements codeintelapp.AnchorSubjectReader.
type p5AnchorSubjects struct {
	requirements p5RequirementService
	design       p5DesignReader
	issues       p5IssueReader
}

func newP5AnchorSubjects(requirements p5RequirementService, reader p5DesignReader, issues p5IssueReader) *p5AnchorSubjects {
	return &p5AnchorSubjects{requirements: requirements, design: reader, issues: issues}
}

var _ codeintelapp.AnchorSubjectReader = (*p5AnchorSubjects)(nil)

func (s *p5AnchorSubjects) RequirementInVersion(ctx context.Context, versionID, requirementID int64) (bool, error) {
	_, err := s.requirements.Get(ctx, versionID, requirementID)
	return p5SubjectExists(err)
}

func (s *p5AnchorSubjects) RequirementVersionInVersion(ctx context.Context, versionID, requirementVersionID int64) (bool, error) {
	requirements, err := (&p5CodeIntelDesign{requirements: s.requirements}).Requirements(ctx, versionID)
	if err != nil {
		return false, err
	}
	for _, requirement := range requirements {
		detail, getErr := s.requirements.Get(ctx, versionID, requirement.ID)
		if getErr != nil {
			if p5IsNotFound(getErr) {
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

func (s *p5AnchorSubjects) CaseVersionInVersion(ctx context.Context, versionID, caseVersionID int64) (bool, error) {
	rows, err := s.design.ReadCaseVersionsByIDs(ctx, versionID, []int64{caseVersionID})
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

func (s *p5AnchorSubjects) IssueInVersion(ctx context.Context, versionID, issueID int64) (bool, error) {
	if s.issues == nil {
		return false, nil
	}
	_, err := s.issues.Get(ctx, versionID, issueID)
	return p5SubjectExists(err)
}

func p5SubjectExists(err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	if p5IsNotFound(err) {
		return false, nil
	}
	return false, err
}

func p5IsNotFound(err error) bool {
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		return apiErr.Status == 404
	}
	return false
}

/* --------------------------- execution evidence --------------------------- */

type p5EvidenceSource interface {
	Get(ctx context.Context, id int64) (execution.Evidence, error)
	ListByVersion(ctx context.Context, versionID int64, filter execution.EvidenceFilter) ([]execution.Evidence, bool, error)
}

type p5CaseRunSource interface {
	Get(ctx context.Context, id int64) (execution.CaseRun, error)
}

type p5WaveSource interface {
	Get(ctx context.Context, id int64) (execution.RunWave, error)
}

type p5ContentSource interface {
	Open(ctx context.Context, sha256 string) ([]byte, error)
}

// p5CodeIntelExecution implements codeintelapp.ExecutionReader over the
// execution evidence rows and the content ledger. Only metadata and the bounded
// network request list are exposed; evidence bytes never leave this adapter.
type p5CodeIntelExecution struct {
	evidence p5EvidenceSource
	caseRuns p5CaseRunSource
	waves    p5WaveSource
	content  p5ContentSource
}

func newP5CodeIntelExecution(evidence p5EvidenceSource, caseRuns p5CaseRunSource, waves p5WaveSource, content p5ContentSource) *p5CodeIntelExecution {
	return &p5CodeIntelExecution{evidence: evidence, caseRuns: caseRuns, waves: waves, content: content}
}

var _ codeintelapp.ExecutionReader = (*p5CodeIntelExecution)(nil)

// p5NetworkPayload mirrors the executor's browser `network` action result.
type p5NetworkPayload struct {
	Items []p5NetworkItem `json:"items"`
}

type p5NetworkItem struct {
	Method string `json:"method"`
	URL    string `json:"url"`
	Status int    `json:"status"`
}

func (r *p5CodeIntelExecution) NetworkEvidence(ctx context.Context, versionID int64) ([]codeintelapp.NetworkRequest, error) {
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
			entries := decodeP5NetworkItems(data)
			for _, entry := range entries {
				if strings.TrimSpace(entry.Method) == "" || strings.TrimSpace(entry.URL) == "" {
					continue
				}
				requests = append(requests, codeintelapp.NetworkRequest{
					ID: item.ID, CaseRunID: item.CaseRunID, VersionSUTID: item.VersionSUTID,
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

func decodeP5NetworkItems(data []byte) []p5NetworkItem {
	var payload p5NetworkPayload
	if err := json.Unmarshal(data, &payload); err == nil && payload.Items != nil {
		return payload.Items
	}
	var items []p5NetworkItem
	if err := json.Unmarshal(data, &items); err == nil {
		return items
	}
	return nil
}

func (r *p5CodeIntelExecution) EvidenceMeta(ctx context.Context, versionID, evidenceID int64) (codeintelapp.EvidenceRef, error) {
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
		VersionSUTID: item.VersionSUTID, Kind: string(item.Kind), MIME: item.MIME,
		SHA256: item.SHA256, SizeBytes: item.SizeBytes,
	}, nil
}

/* ---------------------------- resource proposals --------------------------- */

// p5ProposalResource implements codeintelapp.ProposalResourcePort over the
// resource application service. created is computed by pre-listing because the
// service returns existing rows unchanged for approved/rejected resources.
type p5ProposalResource struct {
	resources *resourceapp.Service
}

func newP5ProposalResource(resources *resourceapp.Service) *p5ProposalResource {
	return &p5ProposalResource{resources: resources}
}

var _ codeintelapp.ProposalResourcePort = (*p5ProposalResource)(nil)

func (p *p5ProposalResource) ProposePage(ctx context.Context, in codeintelapp.ProposalPageInput) (codeintelapp.ProposalItem, bool, error) {
	created := true
	if existing, err := p.resources.ListPages(ctx, in.EnvironmentID); err == nil {
		for _, page := range existing {
			if page.RouteTemplate == in.RouteTemplate || (in.URL != "" && page.URL == in.URL) {
				created = false
				break
			}
		}
	}
	view, err := p.resources.CreateProposedPage(ctx, resourceapp.ProposedPageInput{
		EnvironmentID: in.EnvironmentID, Name: in.Name, URL: in.URL,
		RouteTemplate: in.RouteTemplate, PageType: in.PageType, AuthRole: in.AuthRole,
		Module: in.Module, Source: in.Source,
	})
	if err != nil {
		return codeintelapp.ProposalItem{}, false, err
	}
	return codeintelapp.ProposalItem{
		Kind: "page", ID: parseP5ID(view.ID), Name: view.Name, URL: view.URL,
		RouteTemplate: view.RouteTemplate, Source: view.Source, Status: view.Status,
	}, created, nil
}

func (p *p5ProposalResource) ProposeAPI(ctx context.Context, in codeintelapp.ProposalAPIInput) (codeintelapp.ProposalItem, bool, error) {
	created := true
	if existing, err := p.resources.ListAPIs(ctx, in.EnvironmentID); err == nil {
		for _, api := range existing {
			if api.Method == in.Method && api.PathTemplate == in.PathTemplate {
				created = false
				break
			}
		}
	}
	view, err := p.resources.CreateProposedAPI(ctx, resourceapp.ProposedAPIInput{
		EnvironmentID: in.EnvironmentID, Method: in.Method, PathTemplate: in.PathTemplate,
		Name: in.Name, AuthRole: in.AuthRole, SchemaRef: in.SchemaRef, Source: in.Source,
	})
	if err != nil {
		return codeintelapp.ProposalItem{}, false, err
	}
	return codeintelapp.ProposalItem{
		Kind: "api", ID: parseP5ID(view.ID), Name: view.Name, Method: view.Method,
		PathTemplate: view.PathTemplate, Source: view.Source, Status: view.Status,
	}, created, nil
}

func (p *p5ProposalResource) ListProposals(ctx context.Context, environmentIDs []int64) ([]codeintelapp.ProposalItem, error) {
	items := make([]codeintelapp.ProposalItem, 0)
	for _, environmentID := range environmentIDs {
		pages, err := p.resources.ListPages(ctx, environmentID)
		if err != nil {
			return nil, err
		}
		for _, page := range pages {
			if page.Status != resource.PageStatusProposed {
				continue
			}
			items = append(items, codeintelapp.ProposalItem{
				Kind: "page", ID: parseP5ID(page.ID), Name: page.Name, URL: page.URL,
				RouteTemplate: page.RouteTemplate, Source: page.Source, Status: page.Status,
			})
		}
		apis, err := p.resources.ListAPIs(ctx, environmentID)
		if err != nil {
			return nil, err
		}
		for _, api := range apis {
			if api.Status != resource.APIStatusProposed {
				continue
			}
			items = append(items, codeintelapp.ProposalItem{
				Kind: "api", ID: parseP5ID(api.ID), Name: api.Name, Method: api.Method,
				PathTemplate: api.PathTemplate, Source: api.Source, Status: api.Status,
			})
		}
	}
	return items, nil
}

// p5SutProposalWriter implements tools.SutProposalWriter for sut.page_propose:
// it resolves the version's bound resource target and writes a status=proposed
// row through the same resource port as the code-fact proposer.
type p5SutProposalWriter struct {
	bindings  *p5CodeBindings
	resources *p5ProposalResource
}

func newP5SutProposalWriter(bindings *p5CodeBindings, resources *p5ProposalResource) *p5SutProposalWriter {
	return &p5SutProposalWriter{bindings: bindings, resources: resources}
}

var _ tools.SutProposalWriter = (*p5SutProposalWriter)(nil)

func (w *p5SutProposalWriter) ProposeSUTResource(ctx context.Context, versionID int64, in tools.SutProposalInput) (tools.SutProposalResult, error) {
	bindings, err := w.bindings.VersionSUTBindings(ctx, versionID)
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
			EnvironmentID: target.EnvironmentID, SUTID: target.SUTID, Name: in.Name,
			URL: in.URL, RouteTemplate: in.RouteTemplate, PageType: "page", Source: "ai",
		})
		if proposeErr != nil {
			return tools.SutProposalResult{}, proposeErr
		}
		return tools.SutProposalResult{Created: created, ID: item.ID, Status: item.Status}, nil
	case "api":
		item, created, proposeErr := w.resources.ProposeAPI(ctx, codeintelapp.ProposalAPIInput{
			EnvironmentID: target.EnvironmentID, SUTID: target.SUTID, Method: in.Method,
			PathTemplate: in.PathTemplate, Name: in.Name, Source: "ai",
		})
		if proposeErr != nil {
			return tools.SutProposalResult{}, proposeErr
		}
		return tools.SutProposalResult{Created: created, ID: item.ID, Status: item.Status}, nil
	default:
		return tools.SutProposalResult{}, platform.Invalid("kind", `must be "page" or "api"`)
	}
}

// chooseTarget prefers the environment whose base URL matches a proposed page
// URL; otherwise it uses the first bound version SUT. A version without a
// bound SUT cannot receive a proposal.
func (w *p5SutProposalWriter) chooseTarget(ctx context.Context, bindings []codeintelapp.VersionSUTBinding, in tools.SutProposalInput) (codeintelapp.ProposalTarget, error) {
	if len(bindings) == 0 {
		return codeintelapp.ProposalTarget{}, platform.Invalid("version_id", "the version has no bound SUT")
	}
	targets := make([]codeintelapp.ProposalTarget, 0, len(bindings))
	for _, binding := range bindings {
		target, err := w.bindings.ProposalTarget(ctx, binding.VersionSUTID)
		if err != nil {
			return codeintelapp.ProposalTarget{}, err
		}
		if target.EnvironmentID > 0 {
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

/* ------------------------------- visual ports ------------------------------ */

// p5VisualCaseReader implements visualapp.CaseReader over the design graph.
// Configuration is allowed on draft and confirmed current case versions.
type p5VisualCaseReader struct {
	design p5DesignReader
}

func newP5VisualCaseReader(reader p5DesignReader) *p5VisualCaseReader {
	return &p5VisualCaseReader{design: reader}
}

var _ visualapp.CaseReader = (*p5VisualCaseReader)(nil)

func (r *p5VisualCaseReader) VisualAssertionContext(ctx context.Context, versionID, caseAssertionID int64) (visualapp.CaseAssertionRef, error) {
	graph, err := r.design.ReadGraph(ctx, versionID)
	if err != nil {
		return visualapp.CaseAssertionRef{}, err
	}
	for _, requirement := range graph.Requirements {
		for _, scenario := range requirement.Scenarios {
			for _, caseGraph := range scenario.Cases {
				if caseGraph.Current.ID <= 0 || caseGraph.AssertionCount == 0 {
					continue
				}
				caseVersionID := caseGraph.Current.ID
				detail, readErr := r.design.ReadCase(ctx, versionID, caseGraph.Case.ID, &caseVersionID)
				if readErr != nil {
					return visualapp.CaseAssertionRef{}, readErr
				}
				// AssertionSeq is the P3-D-06 derived position (1-based per
				// step, ascending case_assertion ID). The design read model
				// does not persist it, so derive it exactly like the
				// execution reader; the visual config join with the case-run
				// task context is by (step_seq, assertion_seq).
				stepSeq := 0
				seq := 0
				for _, assertion := range detail.Selected.Assertions {
					if assertion.StepSeq != stepSeq {
						stepSeq = assertion.StepSeq
						seq = 0
					}
					seq++
					if assertion.ID != caseAssertionID {
						continue
					}
					assertionSeq := assertion.AssertionSeq
					if assertionSeq <= 0 {
						assertionSeq = seq
					}
					targetSUTID := int64(0)
					if assertion.TargetVersionSUTID != nil {
						targetSUTID = *assertion.TargetVersionSUTID
					}
					defaultSUTID := int64(0)
					if detail.Selected.DefaultVersionSUTID != nil {
						defaultSUTID = *detail.Selected.DefaultVersionSUTID
					}
					return visualapp.CaseAssertionRef{
						CaseAssertionID:     assertion.ID,
						CaseVersionID:       caseVersionID,
						CaseID:              caseGraph.Case.ID,
						StepSeq:             assertion.StepSeq,
						AssertionSeq:        assertionSeq,
						Kind:                assertion.Kind,
						TargetVersionSUTID:  targetSUTID,
						DefaultVersionSUTID: defaultSUTID,
						CaseVersionStatus:   caseGraph.Current.Status,
					}, nil
				}
			}
		}
	}
	return visualapp.CaseAssertionRef{}, platform.NotFound("case_assertion")
}

// p5VisualEvents publishes visual events on the version channel.
type p5VisualEvents struct {
	publisher eventingapp.Publisher
}

func newP5VisualEvents(publisher eventingapp.Publisher) *p5VisualEvents {
	return &p5VisualEvents{publisher: publisher}
}

var _ visualapp.EventPublisher = (*p5VisualEvents)(nil)

func (e *p5VisualEvents) Publish(ctx context.Context, event visualapp.Event) error {
	if e.publisher == nil {
		return errors.New("event publisher is not configured")
	}
	return e.publisher.Publish(ctx, eventingapp.Event{
		Channel: p5VersionChannel(event.VersionID), Type: event.Type,
		Scope: event.Scope, Payload: event.Payload, ActorID: event.ActorID,
	})
}

// p5VisualAudit adapts the visual audit port to the P0 audit recorder.
type p5VisualAudit struct {
	recorder auditapp.Recorder
}

func newP5VisualAudit(recorder auditapp.Recorder) *p5VisualAudit {
	return &p5VisualAudit{recorder: recorder}
}

var _ visualapp.AuditRecorder = (*p5VisualAudit)(nil)

func (a *p5VisualAudit) Record(ctx context.Context, entry visualapp.AuditEntry) error {
	if a.recorder == nil {
		return nil
	}
	return a.recorder.Record(ctx, auditapp.Event{
		ActorID: entry.ActorID, Action: entry.Action, TargetKind: entry.TargetKind,
		TargetID: entry.TargetID, Metadata: entry.Metadata,
	})
}

// Compile-time assertion: one adapter satisfies both settings reader ports.
var (
	_ visualapp.SettingsReader    = (*p5Settings)(nil)
	_ codeintelapp.SettingsReader = (*p5Settings)(nil)
)
