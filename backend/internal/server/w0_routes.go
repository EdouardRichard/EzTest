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
	"fmt"
	"net/http"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
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
	// Reject is the audited hard-gate back edge (requirement_review ->
	// clarifying, case_review -> designing); it is not a generic request edge.
	Reject(ctx context.Context, versionID int64, reason string, target *version.Stage) error
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
	mux.Handle("GET /api/versions/{id}/attachments", authenticated(http.HandlerFunc(attachment.ListVersionAttachments)))
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
	audit        auditapp.Recorder
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
	if deps.Audit != nil {
		adapter.audit = deps.Audit
	}
	return adapter
}

// Stage returns the current projection.
func (a *stageHTTPAdapter) Stage(ctx context.Context, versionID int64) (versionapp.StageView, error) {
	return a.workflow.Stage(ctx, versionID)
}

// OnRequirementConfirmed confirms the selected (or all draft) requirements and
// then applies requirement_review -> designing through the stage service. The
// source stage is pinned before any entity mutation: the graph has legitimate
// back edges, so confirming first and validating afterwards could freeze drafts
// while the version never advances (or advance from a wrong stage). A wrong
// source stage returns the documented 409 invalid_stage_for_gate and writes
// nothing.
func (a *stageHTTPAdapter) OnRequirementConfirmed(ctx context.Context, versionID int64, requirementIDs []int64) (versionapp.StageView, error) {
	if a.requirements == nil {
		return versionapp.StageView{}, platform.Unavailable("requirement gate confirmer is not configured")
	}
	current, err := a.workflow.Stage(ctx, versionID)
	if err != nil {
		return versionapp.StageView{}, err
	}
	if current.Stage != version.StageRequirementReview {
		return versionapp.StageView{}, versionapp.InvalidStageForGate("requirement", version.StageRequirementReview, current.Stage)
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
// applies case_review -> executing through the stage service. The source stage
// is pinned before any entity mutation (see OnRequirementConfirmed).
func (a *stageHTTPAdapter) OnCasesConfirmed(ctx context.Context, versionID int64, caseIDs []int64) (versionapp.StageView, error) {
	if a.cases == nil {
		return versionapp.StageView{}, platform.Unavailable("case gate confirmer is not configured")
	}
	current, err := a.workflow.Stage(ctx, versionID)
	if err != nil {
		return versionapp.StageView{}, err
	}
	if current.Stage != version.StageCaseReview {
		return versionapp.StageView{}, versionapp.InvalidStageForGate("case", version.StageCaseReview, current.Stage)
	}
	if err := a.cases.ConfirmCases(ctx, versionID, caseIDs); err != nil {
		return versionapp.StageView{}, err
	}
	if err := a.workflow.Request(ctx, versionID, version.StageExecuting); err != nil {
		return versionapp.StageView{}, err
	}
	return a.Stage(ctx, versionID)
}

// Reject is the audited legacy gate back edge: it goes through the v0.22
// StageService (single writer + compare-and-swap + stage event + decision
// audit), never by writing version.stage behind the service.
func (a *stageHTTPAdapter) Reject(ctx context.Context, versionID int64, reason string, target *version.Stage) (versionapp.StageView, error) {
	if err := a.workflow.Reject(ctx, versionID, reason, target); err != nil {
		return versionapp.StageView{}, err
	}
	return a.Stage(ctx, versionID)
}

// RecordGateDenied writes the audit-only denial entry for a non-user hard-gate
// attempt. It never touches the stage row; a missing audit recorder degrades to
// a no-op (the request is still rejected by the handler with 403).
func (a *stageHTTPAdapter) RecordGateDenied(ctx context.Context, versionID int64, gate string) {
	if a.audit == nil {
		return
	}
	event := auditapp.Event{
		Action:     versionapp.StageAuditGateDenied,
		TargetKind: "version",
		TargetID:   fmt.Sprintf("%d", versionID),
		RequestID:  requestid.From(ctx),
		Metadata:   map[string]any{"gate": gate},
	}
	if actor, ok := platform.ActorFrom(ctx); ok {
		if actor.ID > 0 {
			id := actor.ID
			event.ActorID = &id
		}
		event.Metadata["role"] = actor.Role
	}
	_ = a.audit.Record(ctx, event)
}

// ConvergeClarification adapts RequirementService's automatic
// clarifying -> requirement_review edge onto the stage service. Like the
// pre-v0.22 StageMachine it only acts on its source stage: a repeated host
// confirmation or a version already past the gate is a no-op, so the call can
// never walk a designing/executing version backwards.
func (a *stageHTTPAdapter) ConvergeClarification(ctx context.Context, versionID int64) error {
	current, err := a.workflow.Stage(ctx, versionID)
	if err != nil {
		return err
	}
	if current.Stage != version.StageClarifying {
		return nil
	}
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
