package executionapp

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// P17 explore -> prove -> register (v0.2.4-F, SC-F3; design v1.3.1 §6.3).
//
// The execution team explores a SUT through the general channels (browser.cli +
// http + exec, GD-21) and discovers assets (page/api/db). A discovered asset is
// registered into the resource catalog ONLY after effective proof: a step
// successfully used the asset and its assertion passed (server verdict). This
// service is the SERVER-SIDE evidence gate of that registration. The model only
// submits a candidate (the asset descriptor + content-addressed evidence refs);
// the gate verifies, server-side, that for at least one cited evidence reference:
//
//  1. the evidence EXISTS in this case_run (looked up by its sha256),
//  2. its sha256 is VALID — the stored content bytes hash to the evidence sha256
//     (content-addressed integrity, constitution art.4.1), and
//  3. the linked assertion PASSED — the assertion_result the evidence is bound to
//     (its step_seq/assertion_seq) carries verdict=passed, the deterministic
//     server verdict, never a model claim.
//
// On proof the asset is registered as source=ai/status=approved attached to the
// resolved SUT, an append-only asset_registration fact (gate_result=approved) is
// written through the T2 port, and sut.asset_registered is published. An unproven
// discovery (no evidence / sha256 mismatch / assertion not passed) is NEVER
// registered — the gate fails closed with an observable typed error and writes
// nothing (constitution art.7.6: no silent degradation, no fabricated proof).
//
// GD-01 / R-F1: this is an EVIDENCE gate (server-truth), NOT an approval gate. No
// human approval is required; registration is automatic on proof. Every asset
// kind (page/api/db) is registerable. Capability-first: the gate never narrows the
// agent's operation surface — it only decides whether a discovery is proven enough
// to enrich the catalog (which feeds the next wave's fingerprint, SC-F7).

// EventSUTAssetRegistered announces one proven asset registration on the version
// channel. The payload carries the decimal case_run/sut/asset ids, the asset kind,
// source, gate_result and the proving evidence refs.
const EventSUTAssetRegistered = "sut.asset_registered"

// AssetGateApproved is the gate_result of a registration that passed the evidence
// gate. The asset_registration fact is append-only; a later re-registration with
// new evidence appends a new row (latest wins).
const AssetGateApproved = "approved"

// AssetProofEvidenceReader looks up one case_run's evidence by its content sha256.
// A missing sha returns execution.ErrNotFound (the production store's no-rows
// mapping). It is a narrow read port so the gate never depends on the full
// EvidenceRepository write surface; *executionpg.EvidenceRepository satisfies it.
type AssetProofEvidenceReader interface {
	EvidenceByCaseRunSHA(ctx context.Context, caseRunID int64, sha256 string) (execution.Evidence, error)
}

// AssetProofResultReader reads the case_run's assertion results (the server
// verdict the gate trusts). *executionpg.ResultRepository satisfies it.
type AssetProofResultReader interface {
	ListAssertions(ctx context.Context, caseRunID int64) ([]execution.AssertionResult, error)
}

// AssetProofContentReader reads the stored evidence bytes by sha256 so the gate
// can verify content integrity. The execution ContentStore adapter satisfies it.
type AssetProofContentReader interface {
	Open(ctx context.Context, sha256 string) ([]byte, error)
}

// RegisteredAsset is the ACTUAL catalog row a proven registration resolved to
// (created, approved-on-proof, or an idempotent existing hit). Source/Status are
// read from the row, never hardcoded: an idempotent hit on an admin-created
// approved page reports source=admin, and a target database reports its real
// lifecycle status (active), so the tool result/event never misreport the row.
type RegisteredAsset struct {
	ID     int64
	Source string
	Status string
}

// ProvenAssetWriter registers a proven asset into the resource catalog attached
// to the resolved SUT and returns the ACTUAL catalog row info. provenAt is the
// newest created_at of the proving evidence (server-derived, never model input):
// a REJECTED page/api revives only when provenAt is later than the rejection
// timestamp — replayed old evidence must not overturn a human reject. The
// resource adapter implements it over the resource application service (the
// single asset-create authority); the execution context never writes resource
// tables directly.
type ProvenAssetWriter interface {
	RegisterProvenAsset(ctx context.Context, in tools.AssetRegisterInput, actorID int64, provenAt time.Time) (RegisteredAsset, error)
}

// AssetRegistrationDeps wires the P17 registration service. Evidence, Results,
// Content, Assets and Registrations are required; Events/Clock are optional (a nil
// publisher skips the event, a nil clock falls back to the DB created_at default).
type AssetRegistrationDeps struct {
	Evidence      AssetProofEvidenceReader
	Results       AssetProofResultReader
	Content       AssetProofContentReader
	Assets        ProvenAssetWriter
	Registrations execution.AssetRegistrationRepository
	Events        EventPublisher
	Clock         Clock
}

// AssetRegistrationService computes and records the P17 asset registration fact
// after the server-side evidence gate passes.
type AssetRegistrationService struct {
	evidence      AssetProofEvidenceReader
	results       AssetProofResultReader
	content       AssetProofContentReader
	assets        ProvenAssetWriter
	registrations execution.AssetRegistrationRepository
	events        EventPublisher
	clock         Clock
}

// NewAssetRegistrationService wires the registration service.
func NewAssetRegistrationService(deps AssetRegistrationDeps) *AssetRegistrationService {
	return &AssetRegistrationService{
		evidence:      deps.Evidence,
		results:       deps.Results,
		content:       deps.Content,
		assets:        deps.Assets,
		registrations: deps.Registrations,
		events:        deps.Events,
		clock:         deps.Clock,
	}
}

// RegisterAsset gates a discovered asset on effective proof and, only on proof,
// registers it into the catalog (source=ai/status=approved), appends the
// asset_registration fact and publishes sut.asset_registered. An unproven
// discovery returns an observable 409 evidence_gate_failed error and writes
// nothing. An infra error (evidence/result/content read, catalog write, fact
// write) propagates — never a silent registration or a silent drop.
func (s *AssetRegistrationService) RegisterAsset(ctx context.Context, scope tools.Scope, in tools.AssetRegisterInput) (any, error) {
	if s.evidence == nil || s.results == nil || s.content == nil || s.assets == nil || s.registrations == nil {
		return nil, platform.Unavailable("asset registration service is not configured")
	}
	if scope.CaseRunID <= 0 {
		return nil, platform.Invalid("case_run_id", "asset registration requires an execution case_run scope")
	}
	if in.SUTID <= 0 {
		return nil, platform.Invalid("sut_id", "must be a positive decimal ID")
	}
	switch in.AssetKind {
	case execution.AssetKindPage, execution.AssetKindAPI, execution.AssetKindDB:
	default:
		return nil, platform.Invalid("asset_kind", "must be one of page, api, db")
	}
	refs := normalizeEvidenceRefs(in.EvidenceRefs)
	if len(refs) == 0 {
		return nil, platform.Invalid("evidence_refs", "must cite at least one content-addressed evidence reference proving the asset")
	}

	proving, err := s.prove(ctx, scope.CaseRunID, in.SUTID, refs)
	if err != nil {
		return nil, err
	}
	if len(proving) == 0 {
		// Fail-closed: the discovery is not proven. No catalog asset, no fact, no
		// event. The typed error is the observable outcome (constitution art.7.6).
		return nil, evidenceGateFailedError()
	}

	// Freshness anchor for a rejected-asset revival: the newest created_at among
	// the proving evidence rows (shared gate authority). The resource writer
	// compares it against the rejection timestamp; old evidence fails closed.
	provenAt, err := s.proofTime(ctx, scope.CaseRunID, proving)
	if err != nil {
		return nil, err
	}
	registered, err := s.assets.RegisterProvenAsset(ctx, in, scope.AgentRunID, provenAt)
	if err != nil {
		return nil, err
	}
	fact := execution.AssetRegistration{
		SUTID:        in.SUTID,
		AssetKind:    in.AssetKind,
		AssetID:      registered.ID,
		Source:       execution.AssetSourceAI,
		EvidenceRefs: proving,
		GateResult:   AssetGateApproved,
		CreatedBy:    scope.AgentRunID,
	}
	fact.Normalize()
	if err := fact.Validate(); err != nil {
		// The catalog write already happened; an invalid fact shape here is a port
		// bug (e.g. a zero asset id). Log loudly so the gap is never silent.
		slog.ErrorContext(ctx, "asset_registration fact invalid after catalog registration",
			"sut_id", in.SUTID, "asset_kind", in.AssetKind, "asset_id", registered.ID, "error", err)
		return nil, err
	}
	if s.clock != nil {
		fact.CreatedAt = s.clock.Now()
	}
	written, err := s.registrations.AppendAssetRegistration(ctx, fact)
	if err != nil {
		// Cross-context ordering: the catalog asset is already approved (the proof
		// passed). The fact write is retryable (the catalog create is idempotent on
		// the asset's natural key); log loudly so the gap is never silent.
		slog.ErrorContext(ctx, "asset_registration fact write failed after catalog registration",
			"sut_id", in.SUTID, "asset_kind", in.AssetKind, "asset_id", registered.ID, "error", err)
		return nil, err
	}
	s.publishRegistered(ctx, scope, written, registered)
	return map[string]any{
		"version_id":    platformid.Format(scope.VersionID),
		"case_run_id":   platformid.Format(scope.CaseRunID),
		"sut_id":        platformid.Format(written.SUTID),
		"asset_kind":    written.AssetKind,
		"asset_id":      platformid.Format(written.AssetID),
		"source":        registered.Source,
		"status":        registered.Status,
		"gate_result":   written.GateResult,
		"evidence_refs": written.EvidenceRefs,
	}, nil
}

// prove returns the subset of refs that constitute effective proof for the asset
// attached to sutID. It delegates to the SINGLE shared evidence gate (evidenceProof)
// so the asset and new-SUT registration paths verify proof identically; the asset
// path requires the proof to belong to the resolved SUT (attribution).
func (s *AssetRegistrationService) prove(ctx context.Context, caseRunID, sutID int64, refs []string) ([]string, error) {
	gate := evidenceProof{evidence: s.evidence, results: s.results, content: s.content}
	return gate.prove(ctx, caseRunID, &sutID, refs)
}

// proofTime reads the freshness anchor (newest proving-evidence created_at)
// through the SAME shared gate authority, so the evidence lookup rule stays in
// one place.
func (s *AssetRegistrationService) proofTime(ctx context.Context, caseRunID int64, refs []string) (time.Time, error) {
	gate := evidenceProof{evidence: s.evidence, results: s.results, content: s.content}
	return gate.proofTime(ctx, caseRunID, refs)
}

// publishRegistered emits sut.asset_registered on the version channel (best-effort;
// the fact is already persisted, the execution publishEvent precedent). The
// source/status describe the ACTUAL catalog row; gate_result is the gate outcome.
func (s *AssetRegistrationService) publishRegistered(ctx context.Context, scope tools.Scope, fact execution.AssetRegistration, registered RegisteredAsset) {
	if s.events == nil || scope.VersionID <= 0 {
		return
	}
	payload := map[string]any{
		"case_run_id":   platformid.Format(scope.CaseRunID),
		"sut_id":        platformid.Format(fact.SUTID),
		"asset_kind":    fact.AssetKind,
		"asset_id":      platformid.Format(fact.AssetID),
		"source":        registered.Source,
		"status":        registered.Status,
		"gate_result":   fact.GateResult,
		"evidence_refs": fact.EvidenceRefs,
	}
	publishEvent(ctx, s.events, EventSUTAssetRegistered, scope.VersionID, map[string]any{
		"case_run_id": platformid.Format(scope.CaseRunID),
		"sut_id":      platformid.Format(fact.SUTID),
	}, payload, nil)
}

// normalizeEvidenceRefs trims, de-duplicates and drops empty refs, preserving
// order. The gate only ever cites refs the model actually supplied.
func normalizeEvidenceRefs(refs []string) []string {
	seen := make(map[string]bool, len(refs))
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
	}
	return out
}

// evidenceGateFailedError is the observable fail-closed outcome of an unproven
// discovery: a 409 state conflict the model can act on (actually prove the asset),
// not a blind retry.
func evidenceGateFailedError() error {
	err := platform.NewError(http.StatusConflict, "evidence_gate_failed",
		"discovery is not proven: evidence must exist, its sha256 must match the stored content, and the linked assertion must have passed (server verdict)")
	err.Details = map[string]any{"reason": "unproven_discovery"}
	return err
}
