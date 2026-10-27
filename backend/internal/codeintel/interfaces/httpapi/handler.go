// Package httpapi exposes the codeintel context's repository-scoped HTTP
// interface (design spec §7.2): repositories and their index runs, the live Git
// tree/file/diff reads and the persisted facts/search queries.
//
// Handlers stay thin: they parse and validate transport input, call the
// codeintel application services and render the shared platform response
// envelopes. They never touch SQL, Git or the LLM directly.
//
// Authentication is the server's global bearer middleware; the repository read
// routes are available to every authenticated user, while the index trigger is
// wrapped in the identity context's RequireAdmin guard (design spec §6.10). The
// version-scoped routes of design spec §7.3 are added by T15 in this package.
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
// *application.QueryService implements it. File records the code.read audit
// entry itself, so the handler only forwards the authenticated actor.
type QueryService interface {
	Tree(ctx context.Context, repoID int64, prefix, commit, cursor string, limit int) (domain.TreePage, error)
	File(ctx context.Context, actorID, repoID int64, commit, path string, startLine, endLine int) (domain.FileWindow, error)
	Diff(ctx context.Context, repoID int64, base, head string, limit int, cursor string) (domain.ChangeSetData, error)
	FileDiff(ctx context.Context, repoID int64, base, head, path string) (domain.FileDiff, error)
	Facts(ctx context.Context, repoID int64, filter domain.EntityFilter) ([]domain.CodeEntity, string, bool, error)
	Search(ctx context.Context, repoID int64, commit, q, kind string, limit int, cursor string) ([]domain.CodeEntity, string, bool, error)
	Commit(ctx context.Context, repoID int64, sha string) (domain.CommitInfo, error)
}

// Compile-time proof that the concrete application services satisfy the
// consumer-defined ports above. Tests inject hand-written doubles instead of
// constructing the services, so the handler tests never need a database/Git.
var (
	_ IndexService = (*application.IndexService)(nil)
	_ QueryService = (*application.QueryService)(nil)
)

// Handler serves the repository-scoped codeintel endpoints. A nil field leaves
// the corresponding route group unmounted, which keeps a partially wired
// process (and the focused tests) from panicking at request time.
type Handler struct {
	index IndexService
	query QueryService
	repos application.RepoCatalog
}

// HandlerDeps are the collaborators the repository HTTP handlers consume. T18
// wires the concrete application services and the P2 repository catalog; T15
// adds the version-scoped services.
type HandlerDeps struct {
	Index IndexService
	Query QueryService
	// Repos is the P2 repository catalog read through the resource application
	// layer (R24 List backs GET /api/code/repos).
	Repos application.RepoCatalog
}

// NewHandler wires the repository-scoped handlers.
func NewHandler(deps HandlerDeps) *Handler {
	return &Handler{index: deps.Index, query: deps.Query, repos: deps.Repos}
}
