package executionapp

import (
	"context"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// R-B4 new-SUT registration (v0.2.4-F, SC-F4; design v1.3.1 §6.6 extension).
//
// When the execution team explores a resource that matches NO existing SUT (P16
// resolution returned exploration), it may propose a NEW SUT through sut.register.
// This service is the SERVER-SIDE evidence gate of that registration (R-F1): the
// model only submits a candidate (the SUT descriptor + content-addressed evidence
// refs); the gate verifies, server-side, that the cited evidence constitutes
// effective proof (exists + sha256 integrity + linked assertion passed — the SAME
// gate as P17 asset registration, evidenceProof). The new SUT carries no resolved
// SUT id yet, so attribution is not required (nil target).
//
// On proof the SUT is registered source=ai/status=approved AUTO — no human approval
// (GD-01 / R-F1: an EVIDENCE gate, not an approval gate). Without proof it is
// registered source=ai/status=proposed: fail-closed (a proposed SUT contributes
// nothing to effective scope, IsApproved) and observable, so an admin can audit and
// reject it post-hoc. Either way the resource service writes the SAME append-only
// sut_registration birth fact (gate_result = status) in one transaction; this
// service never writes resource tables directly (cross-context via the writer port).
//
// Capability-first: the gate never narrows the agent's operation surface — it only
// decides whether a discovered SUT is proven enough to enter the version's scope.

// EventSUTRegistered announces one new-SUT registration on the version channel. The
// payload carries the decimal case_run/sut/environment ids, name/base_url, source,
// status, gate_result, the approved flag and the proving evidence refs.
const EventSUTRegistered = "sut.registered"

// NewSUTResult is the catalog outcome of registering a new SUT, returned by the
// resource writer port. Status is approved (proven) or proposed (unproven).
type NewSUTResult struct {
	SUTID         int64
	EnvironmentID int64
	Name          string
	BaseURL       string
	Source        string
	Status        string
}

// NewSUTWriter registers a new source=ai SUT into the resource catalog inside the
// version's bound environment, with the evidence-gate outcome (approved) and the
// proving refs, and writes the append-only sut_registration birth fact. provenAt
// is the freshness anchor (the newest proving-evidence created_at, from the shared
// gate's proofTime): the resource service's rebirth guard requires it to be later
// than a rejected same-domain SUT's rejection timestamp before auto-approving
// (controller ruling P22 — a rename with pre-reject evidence downgrades to
// proposed). The resource adapter implements it over the resource application
// service (the single SUT-create authority); the execution context never writes
// resource tables directly.
type NewSUTWriter interface {
	RegisterNewSUT(ctx context.Context, versionID int64, in tools.SUTRegisterInput, evidenceRefs []string, approved bool, actorID int64, provenAt time.Time) (NewSUTResult, error)
}

// SUTRegistrationDeps wires the R-B4 new-SUT registration service. Evidence,
// Results, Content (the shared evidence gate) and SUTs (the catalog writer) are
// required; Events is optional (a nil publisher skips the event).
type SUTRegistrationDeps struct {
	Evidence AssetProofEvidenceReader
	Results  AssetProofResultReader
	Content  AssetProofContentReader
	SUTs     NewSUTWriter
	Events   EventPublisher
}

// SUTRegistrationService gates a new-SUT proposal on effective proof and registers
// it (approved on proof, proposed otherwise), publishing sut.registered.
type SUTRegistrationService struct {
	evidence AssetProofEvidenceReader
	results  AssetProofResultReader
	content  AssetProofContentReader
	suts     NewSUTWriter
	events   EventPublisher
}

// NewSUTRegistrationService wires the new-SUT registration service.
func NewSUTRegistrationService(deps SUTRegistrationDeps) *SUTRegistrationService {
	return &SUTRegistrationService{
		evidence: deps.Evidence,
		results:  deps.Results,
		content:  deps.Content,
		suts:     deps.SUTs,
		events:   deps.Events,
	}
}

// RegisterSUT gates a new-SUT proposal and registers it into the catalog. On proof
// it is auto-approved (R-F1); without proof it stays proposed (fail-closed,
// observable). An infra error (evidence/result/content read, catalog/fact write)
// propagates — never a silent registration or a silent drop.
func (s *SUTRegistrationService) RegisterSUT(ctx context.Context, scope tools.Scope, in tools.SUTRegisterInput) (any, error) {
	if s.evidence == nil || s.results == nil || s.content == nil || s.suts == nil {
		return nil, platform.Unavailable("sut registration service is not configured")
	}
	if scope.CaseRunID <= 0 {
		return nil, platform.Invalid("case_run_id", "new-SUT registration requires an execution case_run scope")
	}
	if scope.VersionID <= 0 {
		return nil, platform.Invalid("version_id", "new-SUT registration requires a version scope")
	}
	refs := normalizeEvidenceRefs(in.EvidenceRefs)
	if len(refs) == 0 {
		return nil, platform.Invalid("evidence_refs", "must cite at least one content-addressed evidence reference proving the new SUT")
	}

	gate := evidenceProof{evidence: s.evidence, results: s.results, content: s.content}
	// A new SUT has no resolved id yet: attribution is not required (nil target).
	proving, err := gate.prove(ctx, scope.CaseRunID, nil, refs)
	if err != nil {
		return nil, err
	}
	approved := len(proving) > 0
	// Freshness anchor for the resource rebirth guard: the newest created_at among
	// the ALREADY-PROVEN refs (the shared gate's proofTime, T10). Empty proving
	// yields the zero time — no anchor, and the writer registers proposed anyway.
	provenAt, err := gate.proofTime(ctx, scope.CaseRunID, proving)
	if err != nil {
		return nil, err
	}

	result, err := s.suts.RegisterNewSUT(ctx, scope.VersionID, in, proving, approved, scope.AgentRunID, provenAt)
	if err != nil {
		return nil, err
	}
	s.publishRegistered(ctx, scope, result, proving)
	return map[string]any{
		"version_id":     platformid.Format(scope.VersionID),
		"case_run_id":    platformid.Format(scope.CaseRunID),
		"sut_id":         platformid.Format(result.SUTID),
		"environment_id": platformid.Format(result.EnvironmentID),
		"name":           result.Name,
		"base_url":       result.BaseURL,
		"source":         result.Source,
		"status":         result.Status,
		"gate_result":    result.Status,
		// Single source (constitution 五): the ACTUAL catalog status decides the
		// approved flag for both the result and the event — the gate's candidate
		// flag may be downgraded by the writer's rebirth guard, so it is never
		// reported as the outcome.
		"approved":      result.Status == "approved",
		"evidence_refs": proving,
	}, nil
}

// publishRegistered emits sut.registered on the version channel (best-effort; the
// catalog SUT and its birth fact are already persisted, the execution publishEvent
// precedent). Both approved and proposed registrations are announced so the outcome
// is observable (constitution art.7.6).
func (s *SUTRegistrationService) publishRegistered(ctx context.Context, scope tools.Scope, result NewSUTResult, proving []string) {
	if s.events == nil || scope.VersionID <= 0 {
		return
	}
	payload := map[string]any{
		"case_run_id":    platformid.Format(scope.CaseRunID),
		"sut_id":         platformid.Format(result.SUTID),
		"environment_id": platformid.Format(result.EnvironmentID),
		"name":           result.Name,
		"base_url":       result.BaseURL,
		"source":         result.Source,
		"status":         result.Status,
		"gate_result":    result.Status,
		"approved":       result.Status == "approved",
		"evidence_refs":  proving,
	}
	publishEvent(ctx, s.events, EventSUTRegistered, scope.VersionID, map[string]any{
		"case_run_id": platformid.Format(scope.CaseRunID),
		"sut_id":      platformid.Format(result.SUTID),
	}, payload, nil)
}
