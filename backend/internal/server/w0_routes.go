package server

// v0.22 W0 route wiring (Task 37).
//
// This file is the composition edge between the already-implemented domain
// handlers (Task 8/9/10b/14/16/17/18/19/20/21/23/24/25/27/28/29/30/33/34/35/36)
// and the shared HTTP mux. Handlers are always mounted behind the bearer
// authentication middleware; a nil service answers the typed 503 envelope
// instead of leaving a contract path unmounted. The only writer of
// version.stage is the v0.22 StageService: the legacy confirm routes are
// adapted onto StageService.Request here, and the new request/block/pause
// routes call it directly.

import (
	"context"
	"net/http"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	resourceapi "github.com/EdouardRichard/EzTest/backend/internal/resource/interfaces/httpapi"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionapi "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
)

// StageService is the narrow v0.22 stage surface the HTTP assembly writes
// through (R-B4-1 single writer). *versionapp.StageService implements it.
type StageService interface {
	Stage(ctx context.Context, versionID int64) (versionapp.StageView, error)
	Request(ctx context.Context, versionID int64, to version.Stage) error
	Advance(ctx context.Context, versionID int64) error
	Block(ctx context.Context, versionID int64, blocker versionapp.Blocker) error
	Unblock(ctx context.Context, versionID int64) error
	Pause(ctx context.Context, versionID int64, reason string) error
	Resume(ctx context.Context, versionID int64) error
}

var _ StageService = (*versionapp.StageService)(nil)

// registerW0Routes mounts the v0.22 W0 read/write surfaces on mux. The handlers
// own their validation and typed errors; this function only chooses the
// middleware class and the exact contract paths (OpenAPI: docs/openapi/api.yaml
// v0.22 W0 block).
func registerW0Routes(
	mux *http.ServeMux,
	authenticated func(http.Handler) http.Handler,
	deps Deps,
) {
	// Plan write (API-4). The read route is mounted by the P6 VersionRead group;
	// the PATCH is always mounted so a deployment without that read model still
	// exposes the write contract (and answers 503 when unwired).
	planHandler := versionapi.NewPlanHandler(deps.Plans)
	mux.Handle("PATCH /api/versions/{id}/plan-items/{itemId}", authenticated(http.HandlerFunc(planHandler.Update)))

	// Clarification handoff read (B §8.1).
	mux.Handle("GET /api/versions/{id}/handoff", authenticated(http.HandlerFunc(versionapi.NewHandoffHandler(deps.Handoffs).Get)))

	// Fact arbitration write (API-13); the read route is the P6 VersionRead fact
	// list.
	versionapi.RegisterFactRoutes(mux, authenticated, versionapi.NewFactHandler(deps.Facts))

	// Requirement extraction progress/trigger/retry (API-5/6/7).
	extraction := versionapi.NewExtractionHandler(deps.Extraction)
	mux.Handle("GET /api/versions/{id}/requirements/extractions", authenticated(http.HandlerFunc(extraction.Get)))
	mux.Handle("POST /api/versions/{id}/requirements/extractions", authenticated(http.HandlerFunc(extraction.Start)))
	mux.Handle("POST /api/versions/{id}/requirements/extractions/{runID}/pause", authenticated(http.HandlerFunc(extraction.Pause)))
	mux.Handle("POST /api/versions/{id}/requirements/extractions/{runID}/resume", authenticated(http.HandlerFunc(extraction.Resume)))
	mux.Handle("POST /api/versions/{id}/requirements/extractions/{runID}/retry", authenticated(http.HandlerFunc(extraction.Retry)))

	// Attachment image observation (API-21/API-22). H7: the routes are
	// version-scoped and verify attachment ownership before any read/vision
	// work; the legacy unscoped paths are deliberately not mounted.
	attachment := versionapi.NewAttachmentHandler(deps.Attachments)
	attachment.SetVersionResolver(deps.Attachments)
	mux.Handle("GET /api/versions/{id}/attachments/{attachmentId}/images", authenticated(http.HandlerFunc(attachment.ListImagesForVersion)))
	mux.Handle("POST /api/versions/{id}/attachments/{attachmentId}/reanalyze", authenticated(http.HandlerFunc(attachment.ReanalyzeForVersion)))

	// Version binding, SUT repositories and version accounts (API-16/17/18).
	versionapi.RegisterBindingRoutes(mux, authenticated, versionapi.NewBindingHandler(deps.Versions))
	versionapi.RegisterVersionAccountRoutes(mux, authenticated, versionapi.NewVersionAccountHandler(deps.Versions))
	if deps.Resource != nil {
		resourceapi.RegisterSUTRepositoryRoutes(mux, authenticated, resourceapi.New(deps.Resource))
	}
}

// stageHTTPAdapter bridges the legacy hard-gate handler interface onto the
// v0.22 StageService. It is the only adapter that still accepts the old
// OnRequirementConfirmed/OnCasesConfirmed signatures: each confirms its
// entities through the existing use case and then applies the single forward
// edge through StageService.Request, so no HTTP path writes version.stage
// directly (R-B4-1).
type stageHTTPAdapter struct {
	workflow     StageService
	requirements versionapp.RequirementGateConfirmer
	cases        versionapp.CaseGateConfirmer
}

// newStageHTTPAdapter wires the adapter from the server dependencies.
func newStageHTTPAdapter(deps Deps) *stageHTTPAdapter {
	adapter := &stageHTTPAdapter{workflow: deps.Stage}
	if deps.Requirements != nil {
		adapter.requirements = versionapp.NewRequirementGateConfirmer(deps.Requirements)
	}
	if deps.DesignGraph != nil {
		adapter.cases = designCaseConfirmer{svc: deps.DesignGraph}
	}
	return adapter
}

// Stage returns the current projection.
func (a *stageHTTPAdapter) Stage(ctx context.Context, versionID int64) (versionapp.StageView, error) {
	return a.workflow.Stage(ctx, versionID)
}

// OnRequirementConfirmed confirms the selected (or all draft) requirements and
// then applies requirement_review -> designing through the stage service.
func (a *stageHTTPAdapter) OnRequirementConfirmed(ctx context.Context, versionID int64, requirementIDs []int64) (versionapp.StageView, error) {
	if a.requirements == nil {
		return versionapp.StageView{}, platform.Unavailable("requirement gate confirmer is not configured")
	}
	if err := a.requirements.BatchConfirm(ctx, versionID, requirementIDs); err != nil {
		return versionapp.StageView{}, err
	}
	if err := a.workflow.Request(ctx, versionID, version.StageDesigning); err != nil {
		return versionapp.StageView{}, err
	}
	return a.Stage(ctx, versionID)
}

// OnCasesConfirmed confirms the selected (or all eligible) cases and then
// applies case_review -> executing through the stage service.
func (a *stageHTTPAdapter) OnCasesConfirmed(ctx context.Context, versionID int64, caseIDs []int64) (versionapp.StageView, error) {
	if a.cases == nil {
		return versionapp.StageView{}, platform.Unavailable("case gate confirmer is not configured")
	}
	if err := a.cases.ConfirmCases(ctx, versionID, caseIDs); err != nil {
		return versionapp.StageView{}, err
	}
	if err := a.workflow.Request(ctx, versionID, version.StageExecuting); err != nil {
		return versionapp.StageView{}, err
	}
	return a.Stage(ctx, versionID)
}

// Reject is the legacy gate back-edge. The v0.22 StageService intentionally
// exposes no generic back edge (gate rejects need a dedicated audited entry
// point); the route stays mounted but answers the typed conflict instead of
// writing the stage row behind the service.
func (a *stageHTTPAdapter) Reject(_ context.Context, _ int64, _ string, _ *version.Stage) (versionapp.StageView, error) {
	return versionapp.StageView{}, platform.StateConflict(
		"gate rejection is not available through the v0.22 stage service; confirm the gate or reopen the version")
}

// RecordGateDenied is a no-op: the v0.22 request route is only reachable with a
// user session, so the legacy audit-only denial path is not produced.
func (a *stageHTTPAdapter) RecordGateDenied(_ context.Context, _ int64, _ string) {}

// ConvergeClarification adapts RequirementService's automatic
// clarifying -> requirement_review edge onto the stage service.
func (a *stageHTTPAdapter) ConvergeClarification(ctx context.Context, versionID int64) error {
	return a.workflow.Request(ctx, versionID, version.StageRequirementReview)
}

// designCaseConfirmer adapts the design context's case confirmation use case to
// the version context's narrow port without importing design into the HTTP
// handler package.
type designCaseConfirmer struct {
	svc *designapp.GraphService
}

func (c designCaseConfirmer) ConfirmCases(ctx context.Context, versionID int64, caseIDs []int64) error {
	if c.svc == nil {
		return platform.Unavailable("design case confirmer is not configured")
	}
	return c.svc.ConfirmCasesByIDs(ctx, versionID, caseIDs)
}
