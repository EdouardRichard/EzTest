// Package httpapi exposes the codeintel context's repository- and version-scoped
// HTTP interface (design spec §7.2/§7.3): repositories and their index runs, the
// live Git tree/file/diff reads and the persisted facts/search queries, plus the
// version-scoped change preview, impact analyses and anchors.
//
// Handlers stay thin: they parse and validate transport input, call the
// codeintel application services and render the shared platform response
// envelopes. They never touch SQL, Git or the LLM directly.
//
// Authentication is the server's global bearer middleware; every route here is
// available to any authenticated user except the T0 index trigger, which is
// wrapped in the identity context's RequireAdmin guard (design spec §6.10).
package httpapi

import (
	"context"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// maxRequestBodyBytes bounds a codeintel request body (design spec §7.1: 1 MiB).
const maxRequestBodyBytes int64 = 1 << 20

// IndexService is the repository T0 index use case consumed by the HTTP layer.
// *application.IndexService implements it.
type IndexService interface {
	Start(ctx context.Context, repoID, actorID int64) (domain.IndexRun, error)
	Get(ctx context.Context, repoID, runID int64) (domain.IndexRun, error)
	GetLatest(ctx context.Context, repoID int64) (domain.IndexRun, error)
}

// QueryService is the read-only T0 query use case consumed by the HTTP layer.
// *application.QueryService implements it. File and Search record the code.read
// audit entry themselves, so the handler only forwards the authenticated actor.
// VersionChanges is the version-scoped change preview (design spec §7.3, R25):
// base is the version's bound baseline, head defaults to the repository fixed
// branch tip and changeSetId stays nil until an analysis persists the set.
type QueryService interface {
	Tree(ctx context.Context, repoID int64, prefix, commit, cursor string, limit int) (domain.TreePage, error)
	File(ctx context.Context, actorID, repoID int64, commit, path string, startLine, endLine int) (domain.FileWindow, error)
	Diff(ctx context.Context, repoID int64, base, head string, limit int, cursor string) (domain.ChangeSetData, error)
	FileDiff(ctx context.Context, repoID int64, base, head, path string) (domain.FileDiff, error)
	Facts(ctx context.Context, repoID int64, filter domain.EntityFilter) ([]domain.CodeEntity, string, bool, error)
	Search(ctx context.Context, actorID, repoID int64, commit, q, kind string, limit int, cursor string) ([]domain.CodeEntity, string, bool, error)
	Commit(ctx context.Context, repoID int64, sha string) (domain.CommitInfo, error)
	VersionChanges(ctx context.Context, versionID int64, headCommit string) (domain.ChangeSetData, string, string, error)
}

// AnalysisService is the version-scoped impact-analysis use case consumed by the
// HTTP layer (design spec §7.3/§7.9). *application.AnalysisService implements
// it. The Start input carries the optional head/base commits and the force flag;
// its reuse bool drives the 200/202 distinction. PatchCandidate enforces the
// done state and the analysis/candidate ownership itself.
type AnalysisService interface {
	Start(ctx context.Context, versionID, actorID int64, in application.StartAnalysisInput) (domain.CodeAnalysis, bool, error)
	Get(ctx context.Context, versionID, analysisID int64) (domain.CodeAnalysis, error)
	GetLatest(ctx context.Context, versionID int64) (domain.CodeAnalysis, error)
	ListCandidates(ctx context.Context, analysisID int64) ([]domain.ImpactCandidate, error)
	PatchCandidate(ctx context.Context, versionID, analysisID, candidateID, actorID int64, selected bool) (domain.ImpactCandidate, error)
}

// AnchorService is the version-scoped anchor use case consumed by the HTTP layer
// (design spec §7.3). *application.AnchorService implements it; subject
// ownership, file existence, line validation and point uniqueness are enforced
// by the application service, never here.
type AnchorService interface {
	Create(ctx context.Context, versionID, actorID int64, params domain.CreateAnchorParams) (domain.CodeAnchor, error)
	List(ctx context.Context, versionID int64, filter domain.AnchorFilter) ([]domain.CodeAnchor, error)
	Delete(ctx context.Context, versionID, anchorID int64) error
}

// Compile-time proof that the concrete application services satisfy the
// consumer-defined ports above. Tests inject hand-written doubles instead of
// constructing the services, so the handler tests never need a database/Git.
var (
	_ IndexService    = (*application.IndexService)(nil)
	_ QueryService    = (*application.QueryService)(nil)
	_ AnalysisService = (*application.AnalysisService)(nil)
	_ AnchorService   = (*application.AnchorService)(nil)
)

// Handler serves the repository- and version-scoped codeintel endpoints. A nil
// field leaves the corresponding route group unmounted, which keeps a partially
// wired process (and the focused tests) from panicking at request time.
type Handler struct {
	index    IndexService
	query    QueryService
	analyses AnalysisService
	anchors  AnchorService
	repos    application.RepoCatalog
}

// HandlerDeps are the collaborators the codeintel HTTP handlers consume. T18
// wires the concrete application services and the P2 repository catalog.
type HandlerDeps struct {
	Index    IndexService
	Query    QueryService
	Analyses AnalysisService
	Anchors  AnchorService
	// Repos is the P2 repository catalog read through the resource application
	// layer (R24 List backs GET /api/code/repos).
	Repos application.RepoCatalog
}

// NewHandler wires the repository- and version-scoped handlers.
func NewHandler(deps HandlerDeps) *Handler {
	return &Handler{
		index:    deps.Index,
		query:    deps.Query,
		analyses: deps.Analyses,
		anchors:  deps.Anchors,
		repos:    deps.Repos,
	}
}
