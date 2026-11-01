package codeintelapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/pagination"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

const maxRequestBody = 1 << 20

// IndexServicePort is the T0 application surface needed by the handlers.
type IndexServicePort interface {
	Start(ctx context.Context, versionID, repoID int64, commit string) (codeintel.IndexRun, bool, error)
	Latest(ctx context.Context, repoID int64) (codeintel.IndexRun, error)
	Get(ctx context.Context, runID int64) (codeintel.IndexRun, error)
	RepoStatus(ctx context.Context, versionID int64) ([]codeintelapp.RepoStatus, error)
	Tree(ctx context.Context, repoID int64, commit, path, cursor string, limit int) ([]codeintel.Entity, string, bool, error)
}

// QueryServicePort is the read-only T0 application surface.
type QueryServicePort interface {
	File(ctx context.Context, repoID int64, commit, path string, start, end int) (codeintel.FileWindow, error)
	Diff(ctx context.Context, repoID int64, base, head, path, cursor string, limit int) (codeintel.DiffResult, string, bool, error)
	FileDiff(ctx context.Context, repoID int64, base, head, path string) (codeintel.FileDiff, error)
	Commit(ctx context.Context, repoID int64, sha string) (codeintelapp.CommitView, error)
}

// AnalysisServicePort is the T1 application surface (optional: routes are only
// mounted when it is wired).
type AnalysisServicePort interface {
	Start(ctx context.Context, versionID, repoID int64, headCommit string, force bool) (codeintel.Analysis, bool, error)
	Latest(ctx context.Context, versionID, repoID int64) (codeintel.Analysis, error)
	Get(ctx context.Context, analysisID int64) (codeintel.Analysis, error)
	Facts(ctx context.Context, repoID int64, commit, kind, path, cursor string, limit int) ([]codeintel.Fact, string, bool, error)
	Search(ctx context.Context, repoID int64, commit, query, kind string, limit int) ([]codeintel.Entity, error)
	Changes(ctx context.Context, versionID, repoID int64, headCommit string) (codeintelapp.ChangeSet, error)
}

// Option configures optional handler wiring.
type Option func(*Handler)

// WithAnalysis mounts the T1 routes (changes/analyses/facts/search).
func WithAnalysis(analysis AnalysisServicePort) Option {
	return func(handler *Handler) { handler.analysis = analysis }
}

// WithImpact enriches the analysis-detail route with persisted candidates.
func WithImpact(impact ImpactServicePort) Option {
	return func(handler *Handler) { handler.impact = impact }
}

// Handler serves the P5 code routes. The server mounts it behind RequireAuth.
type Handler struct {
	index    IndexServicePort
	query    QueryServicePort
	analysis AnalysisServicePort
	impact   ImpactServicePort
}

// New builds the codeintel HTTP handler.
func New(index IndexServicePort, query QueryServicePort, options ...Option) *Handler {
	handler := &Handler{index: index, query: query}
	for _, option := range options {
		option(handler)
	}
	return handler
}

var (
	_ IndexServicePort    = (*codeintelapp.IndexService)(nil)
	_ QueryServicePort    = (*codeintelapp.QueryService)(nil)
	_ AnalysisServicePort = (*codeintelapp.AnalysisService)(nil)
)

// Register mounts the code routes (spec §4.2 ops 1–15) with the contract path
// parameter names. analysis routes are skipped when no T1 service is wired.
func (h *Handler) Register(mux *http.ServeMux, authenticated func(http.Handler) http.Handler) {
	handle := func(pattern string, handler http.HandlerFunc) {
		mux.Handle(pattern, authenticated(handler))
	}

	handle("POST /api/versions/{versionId}/code/index", h.StartIndex)
	handle("GET /api/versions/{versionId}/code/repos", h.ListRepos)
	handle("GET /api/code/repos/{repoId}/index-runs/latest", h.LatestIndexRun)
	handle("GET /api/code/repos/{repoId}/index-runs/{runId}", h.GetIndexRun)
	handle("GET /api/code/repos/{repoId}/tree", h.Tree)
	handle("GET /api/code/repos/{repoId}/file", h.File)
	handle("GET /api/code/repos/{repoId}/diff", h.Diff)
	handle("GET /api/code/repos/{repoId}/diff/file", h.FileDiff)
	handle("GET /api/code/repos/{repoId}/commits/{sha}", h.Commit)

	if h.analysis != nil {
		handle("GET /api/code/repos/{repoId}/facts", h.Facts)
		handle("GET /api/code/repos/{repoId}/search", h.Search)
		handle("GET /api/versions/{versionId}/code/changes", h.Changes)
		handle("POST /api/versions/{versionId}/code/analyses", h.CreateAnalysis)
		handle("GET /api/versions/{versionId}/code/analyses/latest", h.LatestAnalysis)
		handle("GET /api/versions/{versionId}/code/analyses/{analysisId}", h.GetAnalysis)
	}
}

// --- request parsing helpers ----------------------------------------------------

func requireActor(w http.ResponseWriter, r *http.Request) (int64, bool) {
	actor, ok := platform.ActorFrom(r.Context())
	if !ok || actor.ID <= 0 {
		httpx.Error(w, r, platform.Unauthenticated())
		return 0, false
	}
	return actor.ID, true
}

func pathID(r *http.Request, name string) (int64, error) {
	return validation.NonEmptyID(name, r.PathValue(name))
}

func queryIDRequired(r *http.Request, name string) (int64, error) {
	return validation.NonEmptyID(name, r.URL.Query().Get(name))
}

func queryIDOptional(r *http.Request, name string) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return 0, nil
	}
	return validation.NonEmptyID(name, raw)
}

func requiredQuery(r *http.Request, name string) (string, error) {
	value := strings.TrimSpace(r.URL.Query().Get(name))
	if value == "" {
		return "", platform.Invalid(name, "is required")
	}
	return value, nil
}

// commitQuery parses a required 40-hex commit query parameter.
func commitQuery(r *http.Request, name string) (string, error) {
	value, err := requiredQuery(r, name)
	if err != nil {
		return "", err
	}
	if !codeintelapp.ValidCommitSHA(value) {
		return "", platform.Invalid(name, "must be a 40-character lowercase hex commit SHA")
	}
	return value, nil
}

// optionalCommitQuery parses an optional 40-hex commit query parameter.
func optionalCommitQuery(r *http.Request, name string) (string, error) {
	value := strings.TrimSpace(r.URL.Query().Get(name))
	if value == "" {
		return "", nil
	}
	if !codeintelapp.ValidCommitSHA(value) {
		return "", platform.Invalid(name, "must be a 40-character lowercase hex commit SHA")
	}
	return value, nil
}

func optionalInt(r *http.Request, name string) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, platform.Invalid(name, "must be a non-negative integer")
	}
	return value, nil
}

func listLimit(r *http.Request, def, max int) int {
	return pagination.Limit(strings.TrimSpace(r.URL.Query().Get("limit")), def, max)
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err := decoder.Decode(dst); err != nil {
		return platform.Invalid("body", "request body must be a valid JSON object")
	}
	return nil
}
