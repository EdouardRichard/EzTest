// Package httpapi exposes the P5 mindmap HTTP interface (spec §7). Handlers stay
// thin: they decode DTOs, call the mindmap application services and render the
// shared response envelopes. No business rule and no service construction lives
// here; the composition root supplies every collaborator.
//
// Every route sits below /api/versions/{versionId}/mindmap. Authentication is the
// global identity middleware; only the exploration run (kind=explore) and the
// anchor-delete ownership rule add an authorization check, and both are enforced
// inside the handler because they depend on the request body / the stored row.
package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// GraphService is the manual graph use-case surface consumed by the handlers.
// *application.GraphService satisfies it.
type GraphService interface {
	Get(ctx context.Context, versionID int64) (application.GraphView, error)
	CreateNode(ctx context.Context, versionID, actor int64, cmd application.CreateNodeCommand) (domain.CaseNode, error)
	UpdateNode(ctx context.Context, versionID, nodeID, actor int64, cmd application.UpdateNodeCommand) (domain.CaseNode, error)
	DeleteNode(ctx context.Context, versionID, nodeID, actor int64, revision int) error
	AddStep(ctx context.Context, versionID, nodeID, actor int64, cmd application.AddStepCommand) (domain.CaseStep, error)
	UpdateStep(ctx context.Context, versionID, stepID, actor int64, cmd application.UpdateStepCommand) (domain.CaseStep, error)
	DeleteStep(ctx context.Context, versionID, stepID, actor int64, nodeRevision int) error
	ReorderSteps(ctx context.Context, versionID, nodeID, actor int64, cmd application.ReorderStepsCommand) error
	AddAssertion(ctx context.Context, versionID, nodeID, actor int64, cmd application.AddAssertionCommand) (domain.CaseAssertion, error)
	UpdateAssertion(ctx context.Context, versionID, assertionID, actor int64, cmd application.UpdateAssertionCommand) (domain.CaseAssertion, error)
	DeleteAssertion(ctx context.Context, versionID, assertionID, actor int64, nodeRevision int) error
	AddSource(ctx context.Context, versionID, nodeID, actor int64, cmd application.AddSourceCommand) (domain.CaseSource, error)
	DeleteSource(ctx context.Context, versionID, sourceID, actor int64) error
	Reorder(ctx context.Context, versionID, actor int64, cmd application.ReorderCommand) error
}

// RunStarter is the design/data_plan run slice. *application.RunService satisfies
// it; review runs go through ReviewService.StartReview so the review use case
// keeps ownership of its own kind.
type RunStarter interface {
	Start(ctx context.Context, versionID, actor int64, cmd application.StartRunCommand) (domain.CaseDesignRun, error)
}

// ExploreService is the exploration run slice. *application.ExploreService
// satisfies it.
type ExploreService interface {
	Start(ctx context.Context, versionID, actor int64, cmd application.ExploreCommand) (domain.CaseDesignRun, error)
	Run(ctx context.Context, run domain.CaseDesignRun) (application.ExploreResult, error)
}

// ReviewService is the review/baseline use-case surface. *application.ReviewService
// satisfies it.
type ReviewService interface {
	StartReview(ctx context.Context, versionID, actor int64) (domain.CaseDesignRun, error)
	Approve(ctx context.Context, versionID, actor int64, cmd application.ReviewCommand) (application.ApproveResult, error)
	Reject(ctx context.Context, versionID, actor int64, comment string) (application.RejectResult, error)
	List(ctx context.Context, versionID int64, limit int, cursor string) ([]domain.CaseReview, string, bool, error)
	ListFindings(ctx context.Context, versionID int64, status, severity string, nodeID *int64) ([]domain.CaseReviewFinding, error)
	UpdateFinding(ctx context.Context, versionID, findingID, actor int64, status string) (domain.CaseReviewFinding, error)
}

// ExportService is the export use-case surface. *application.ExportService
// satisfies it.
type ExportService interface {
	Create(ctx context.Context, versionID, actor int64, format string) (domain.CaseExport, error)
	List(ctx context.Context, versionID int64, format string, limit int, cursor string) ([]domain.CaseExport, string, bool, error)
	Download(ctx context.Context, versionID, exportID int64) (io.ReadCloser, domain.CaseExport, error)
}

// NodeReader is the read side the handlers need next to the write services: the
// current revision of a node (the create-step/assertion bodies carry no token)
// and the node list the anchor-ownership scan walks.
type NodeReader interface {
	Get(ctx context.Context, versionID, nodeID int64) (domain.CaseNode, error)
	ListByVersion(ctx context.Context, versionID int64) ([]domain.CaseNode, error)
}

// RunReader is the run read side (list/detail/latest-summary). The concrete
// PostgreSQL repository satisfies both application.RunRepository and this slice.
type RunReader interface {
	Get(ctx context.Context, versionID, runID int64) (domain.CaseDesignRun, error)
	List(ctx context.Context, versionID int64, kind, status string, limit int, cursor string) ([]domain.CaseDesignRun, string, bool, error)
	LatestByKind(ctx context.Context, versionID int64, kind string) (domain.CaseDesignRun, error)
}

// FindingReader lists the review findings of a version (graph blocker counts and
// the flat GET /findings list).
type FindingReader interface {
	List(ctx context.Context, versionID int64, status, severity string, nodeID *int64) ([]domain.CaseReviewFinding, error)
}

// AnchorGateway is the case-anchor CRUD port.
type AnchorGateway interface {
	Create(ctx context.Context, versionID, actorID int64, p application.AnchorParams) (application.Anchor, error)
	List(ctx context.Context, versionID, nodeID int64) ([]application.Anchor, error)
	Delete(ctx context.Context, versionID, anchorID int64) error
}

// CodeFactsReader is the read-only P4 impact slice used by GET /mindmap.
type CodeFactsReader interface {
	GetLatestAnalysis(ctx context.Context, versionID int64) (application.ImpactSnapshot, error)
	ListCandidates(ctx context.Context, analysisID int64) ([]application.ImpactCandidate, error)
}

// BaselineReader reads the frozen baseline (frozen-graph guards and export
// reuse). The concrete PostgreSQL repository satisfies it.
type BaselineReader interface {
	GetByVersion(ctx context.Context, versionID int64) (domain.CaseBaseline, error)
}

// ExportIndex is the export metadata read used to answer a baseline-bound reuse
// with 200 instead of 201 (spec §7.2 endpoint 25).
type ExportIndex interface {
	FindByBaselineFormat(ctx context.Context, versionID int64, format string, baselineID *int64) (domain.CaseExport, error)
}

// VersionLifecycle is the read/transition slice of the version port the review
// endpoints need.
type VersionLifecycle interface {
	GetVersion(ctx context.Context, versionID int64) (application.VersionSnapshot, error)
	EnterReview(ctx context.Context, versionID, actor int64) (application.VersionSnapshot, error)
}

// EventPublisher publishes the gate event of a manual submit-review.
type EventPublisher interface {
	Publish(ctx context.Context, e application.Event) error
}

// Compile-time proof that the composition root's concrete services satisfy the
// consumer-side interfaces above. Repositories and the cross-context adapters are
// only checked at wiring time (importing them here would invert the dependency
// direction, Agent.md §6.2).
var (
	_ GraphService   = (*application.GraphService)(nil)
	_ RunStarter     = (*application.RunService)(nil)
	_ ExploreService = (*application.ExploreService)(nil)
	_ ReviewService  = (*application.ReviewService)(nil)
	_ ExportService  = (*application.ExportService)(nil)
)

// Deps are the collaborators of the mindmap HTTP handlers. Every field is a
// consumer-defined interface so the handler tests can inject fakes; the
// composition root passes the concrete services and adapters.
type Deps struct {
	Graph   GraphService
	Runs    RunStarter
	Explore ExploreService
	Reviews ReviewService
	Exports ExportService

	Nodes     NodeReader
	RunStore  RunReader
	Findings  FindingReader
	Anchors   AnchorGateway
	CodeFacts CodeFactsReader
	Baselines BaselineReader
	ExportIdx ExportIndex
	Versions  VersionLifecycle
	Events    EventPublisher
	Logger    *slog.Logger
}

// Handler is the mindmap HTTP handler group mounted by the server through
// Dependencies.Mindmap. A nil group (or a nil dependency) leaves the matching
// routes unmounted, so a partially wired process never exposes a route backed by
// nil collaborators.
type Handler struct {
	deps   Deps
	logger *slog.Logger
}

// NewHandler builds the handler group. The logger is optional.
func NewHandler(deps Deps) *Handler {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{deps: deps, logger: logger}
}

// Register mounts every endpoint whose dependencies are wired. The 27 P5 routes
// of spec §7.2 are grouped by the service they need; the P7 §7.6 additions
// (incremental plan, supplement node decision) are mounted when the graph service
// exposes their use cases.
func (h *Handler) Register(mux *http.ServeMux) {
	if mux == nil || h == nil {
		return
	}
	const base = "/api/versions/{versionId}/mindmap"
	if h.deps.Graph != nil {
		mux.HandleFunc("GET "+base, h.getGraph)
		mux.HandleFunc("POST "+base+"/nodes", h.createNode)
		mux.HandleFunc("PATCH "+base+"/nodes/{nodeId}", h.updateNode)
		mux.HandleFunc("DELETE "+base+"/nodes/{nodeId}", h.deleteNode)
		mux.HandleFunc("PUT "+base+"/order", h.reorderNodes)
		mux.HandleFunc("POST "+base+"/nodes/{nodeId}/steps", h.createStep)
		mux.HandleFunc("PATCH "+base+"/steps/{stepId}", h.updateStep)
		mux.HandleFunc("DELETE "+base+"/steps/{stepId}", h.deleteStep)
		mux.HandleFunc("PUT "+base+"/nodes/{nodeId}/steps/order", h.reorderSteps)
		mux.HandleFunc("POST "+base+"/nodes/{nodeId}/assertions", h.createAssertion)
		mux.HandleFunc("PATCH "+base+"/assertions/{assertionId}", h.updateAssertion)
		mux.HandleFunc("DELETE "+base+"/assertions/{assertionId}", h.deleteAssertion)
		mux.HandleFunc("POST "+base+"/nodes/{nodeId}/sources", h.createSource)
		mux.HandleFunc("DELETE "+base+"/sources/{sourceId}", h.deleteSource)
	}
	if h.deps.Anchors != nil && h.deps.Nodes != nil && h.deps.Versions != nil {
		mux.HandleFunc("POST "+base+"/nodes/{nodeId}/anchors", h.createAnchor)
		if h.deps.Baselines != nil {
			mux.HandleFunc("DELETE "+base+"/anchors/{anchorId}", h.deleteAnchor)
		}
	}
	if h.deps.Runs != nil || h.deps.Explore != nil || h.deps.Reviews != nil {
		// The dispatcher routes the P7 supplement/incremental_design kinds to the
		// parent-batch-aware handler and every legacy kind to startRun.
		mux.HandleFunc("POST "+base+"/runs", h.startRunDispatch)
	}
	// The P7 plan/decision endpoints are mounted when the graph service exposes
	// their use cases (the concrete *application.GraphService always does).
	if reader, ok := h.deps.Graph.(incrementalPlanReader); ok && reader != nil {
		mux.HandleFunc("GET "+base+"/incremental-plan", h.getIncrementalPlan)
	}
	if decision, ok := h.deps.Graph.(supplementNodeDecision); ok && decision != nil && h.deps.Nodes != nil {
		mux.HandleFunc("PATCH "+base+"/nodes/{nodeId}/supplement", h.patchSupplementNode)
	}
	if h.deps.RunStore != nil {
		mux.HandleFunc("GET "+base+"/runs", h.listRuns)
		mux.HandleFunc("GET "+base+"/runs/{runId}", h.getRun)
	}
	if h.deps.Graph != nil && h.deps.Versions != nil {
		mux.HandleFunc("POST "+base+"/submit-review", h.submitReview)
	}
	if h.deps.Reviews != nil {
		mux.HandleFunc("POST "+base+"/review", h.decideReview)
		mux.HandleFunc("GET "+base+"/reviews", h.listReviews)
		mux.HandleFunc("GET "+base+"/findings", h.listFindings)
		mux.HandleFunc("PATCH "+base+"/findings/{findingId}", h.updateFinding)
	}
	if h.deps.Exports != nil {
		mux.HandleFunc("POST "+base+"/export", h.createExport)
		mux.HandleFunc("GET "+base+"/exports", h.listExports)
		mux.HandleFunc("GET "+base+"/exports/{exportId}/download", h.downloadExport)
	}
}
