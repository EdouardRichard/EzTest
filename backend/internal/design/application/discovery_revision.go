package designapp

// W2-11 (WP-6, spec R-EX-6 / DB-30 / API-41 / EV-12): additive case revision
// for approved execution-time discovery targets.
//
// The execution-time delta (W1-12) proposes genuinely new pages/APIs. An admin
// approving one publishes a system event (`resource.page|api.confirmed`). This
// service turns the approval into exactly one additive case revision for the
// requirement the discovery observation was reachable from:
//
//	observation -> case_run -> case_version -> case_source(requirement)
//
// The deterministic Critic is the six-predicate subset of R-EX-6:
//  1. the discovered page/API is approved;
//  2. the requirement is current + clarified;
//  3. at least one supporting observation/evidence row exists;
//  4. no confirmed case of the requirement already has the same target and
//     content hash;
//  5. the draft has at least one step and one assertion;
//  6. the target page/API is inside the version scope.
//
// Only when every predicate passes is the draft auto-confirmed; a low-confidence
// result (fewer than two evidence rows or no requirement link) stays a human
// draft. The current wave is never mutated: a discovery revision only becomes
// executable in the next wave, which reads current+confirmed cases.
//
// Rollback keeps the original revision as history (`superseded`) and inserts a
// replacement revision with `trigger='rollback'` and `rollback_of=<original>`;
// no case_revision table is introduced (spec DB-28). The replacement is copied
// from the previous confirmed revision of the same case; when the additive case
// has no earlier confirmed revision (it is a brand-new case), the replacement
// re-freezes the same content so the rollback still yields one confirmed
// revision and the original remains auditable history.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
)

// DiscoveryProvenance is the design context's read projection of the execution
// observation chain that authorizes one additive revision. Every ID is a
// logical cross-context reference (no FK).
type DiscoveryProvenance struct {
	VersionID            int64
	ObservationID        int64
	ResourceID           int64
	Kind                 string
	CaseRunID            int64
	CaseVersionID        int64
	RequirementID        int64
	RequirementVersionID int64
	VersionSUTID         int64
	// FactID is the version_fact recorded by the discovery delta for the new
	// resource; the additive draft keeps it as a knowledge source.
	FactID int64
	// EvidenceRows counts the evidence rows recorded for the observation's
	// case_run. R-EX-6 low confidence is evidence < 2.
	EvidenceRows int
}

// DiscoveryApprovedResource is the catalog projection the Critic checks first.
type DiscoveryApprovedResource struct {
	ID       int64
	Kind     string
	Status   string
	Label    string
	Method   string
	Template string
}

// DiscoveryRevisionStore is the cross-context read port consumed by the
// discovery revision service. It is implemented by the design PostgreSQL
// adapter as a read-only provenance projection; the design context never
// imports the execution application (Agent.md §6.2).
type DiscoveryRevisionStore interface {
	ReadProvenanceByResource(ctx context.Context, resourceID int64, kind string) ([]DiscoveryProvenance, error)
	ReadProvenanceByObservation(ctx context.Context, versionID, observationID int64) (DiscoveryProvenance, error)
	ApprovedResource(ctx context.Context, resourceID int64, kind string) (DiscoveryApprovedResource, bool, error)
	// FindConfirmedCaseByContent is Critic predicate ④: no already-confirmed
	// case of the requirement has the same target and content hash.
	FindConfirmedCaseByContent(ctx context.Context, versionID, requirementID int64, contentHash string, pageID, apiID int64) (int64, bool, error)
}

// DiscoveryRevisionView is the service result of one resource-confirmed pass or
// one rollback. Status is the resulting case version status (confirmed or
// draft); zero IDs mean no draft could be attached (no requirement link / the
// target is out of the version scope).
type DiscoveryRevisionView struct {
	CaseID              int64
	CaseVersionID       int64
	Status              string
	Trigger             string
	ResourceID          int64
	SourceObservationID int64
	RollbackOf          int64
	Revision            int
	// LowConfidenceReason names the predicate that kept the revision as a
	// draft (or prevented creating one): missing_requirement,
	// resource_not_approved, requirement_not_current, target_out_of_scope,
	// requirement_not_clarified, insufficient_evidence, duplicate_target,
	// incomplete_content.
	LowConfidenceReason string
}

// CaseVersionRollbackView is the rollback API result: the replacement revision
// and the auto-confirmed revision it replaces.
type CaseVersionRollbackView struct {
	CaseID         int64
	CaseVersionID  int64
	RolledBackFrom int64
	Revision       int
	Status         string
	Trigger        string
}

// discoveryRevisionEvidenceThreshold is R-EX-6's low-confidence evidence floor:
// an auto-confirmation needs at least two evidence rows.
const discoveryRevisionEvidenceThreshold = 2

// discoveryRevisionTimeout bounds the detached observer work so a stalled
// cross-context read cannot hold the event listener loop forever.
const discoveryRevisionTimeout = 10 * time.Second

// SetDiscoveryRevisionStore attaches the cross-context provenance port. It
// returns the service for fluent wiring.
func (s *GraphService) SetDiscoveryRevisionStore(store DiscoveryRevisionStore) *GraphService {
	if s != nil {
		s.revisionStore = store
	}
	return s
}

// OnResourceConfirmed applies the additive case revision for one approved
// discovered page/API. requirementID is the requirement resolved from the
// observation chain; zero means the caller could not resolve one, which the
// Critic treats as missing and keeps as a low-confidence draft (no case is
// created when there is nothing to attach to).
//
// The method is idempotent per (version, observation): a replayed resource
// event returns the already-created revision without writing a second one. It
// never mutates case_run or the current wave.
func (s *GraphService) OnResourceConfirmed(ctx context.Context, versionID, resourceID, observationID, requirementID int64) (DiscoveryRevisionView, error) {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return DiscoveryRevisionView{}, err
	}
	if s == nil || s.repo == nil || s.revisionStore == nil {
		return DiscoveryRevisionView{}, platform.Unavailable("discovery revision is not configured")
	}
	// Idempotency: the observation is the natural key of one additive revision.
	if existing, found, err := s.repo.CaseVersionByObservation(ctx, versionID, observationID); err != nil {
		return DiscoveryRevisionView{}, mapDesignError(err, "discovery case revision")
	} else if found {
		return discoveryRevisionView(existing), nil
	}

	provenance, err := s.revisionStore.ReadProvenanceByObservation(ctx, versionID, observationID)
	if err != nil {
		return DiscoveryRevisionView{}, mapDesignError(err, "discovery observation")
	}
	if resourceID <= 0 {
		resourceID = provenance.ResourceID
	}
	if requirementID <= 0 {
		requirementID = provenance.RequirementID
	}
	if requirementID <= 0 {
		return lowConfidenceDraft(resourceID, observationID, "missing_requirement"), nil
	}
	kind := strings.TrimSpace(provenance.Kind)
	if kind != "page" && kind != "api" {
		return lowConfidenceDraft(resourceID, observationID, "target_out_of_scope"), nil
	}

	// Critic ①: the discovered catalog resource must be approved.
	approved, found, err := s.revisionStore.ApprovedResource(ctx, resourceID, kind)
	if err != nil {
		return DiscoveryRevisionView{}, err
	}
	if !found || approved.Status != "approved" {
		return lowConfidenceDraft(resourceID, observationID, "resource_not_approved"), nil
	}

	// Critic ②: the requirement is current + clarified.
	requirement, err := s.repo.RequirementContext(ctx, versionID, requirementID)
	if err != nil {
		if errors.Is(err, design.ErrNotFound) {
			return lowConfidenceDraft(resourceID, observationID, "requirement_not_current"), nil
		}
		return DiscoveryRevisionView{}, mapDesignError(err, "requirement")
	}
	if requirement.Status != "clarified" {
		return lowConfidenceDraft(resourceID, observationID, "requirement_not_clarified"), nil
	}
	if requirement.CurrentVersionID <= 0 {
		return lowConfidenceDraft(resourceID, observationID, "requirement_not_current"), nil
	}
	if provenance.RequirementVersionID > 0 && provenance.RequirementVersionID != requirement.CurrentVersionID {
		return lowConfidenceDraft(resourceID, observationID, "requirement_not_current"), nil
	}

	// Critic ⑥: the target must be inside the current version scope. The map is
	// exactly the approved-and-not-excluded resource set of the version.
	versionContext, err := s.repo.VersionContext(ctx, versionID)
	if err != nil {
		return DiscoveryRevisionView{}, mapDesignError(err, "version")
	}
	versionSUTID := int64(0)
	if kind == "page" {
		versionSUTID = versionContext.PageToSUT[resourceID]
	} else {
		versionSUTID = versionContext.APIToSUT[resourceID]
	}
	if versionSUTID <= 0 {
		return lowConfidenceDraft(resourceID, observationID, "target_out_of_scope"), nil
	}

	draft := discoveryAugmentedDraft(provenance, resourceID, approved, requirement, versionSUTID, observationID)
	created, err := s.CreateAugmentedDraftCase(ctx, versionID, draft)
	if err != nil {
		// A confirmed same-title case (the Task 106 freeze) is a duplicate
		// target, not a failure of the resource confirmation.
		if isStateConflictError(err) {
			return lowConfidenceDraft(resourceID, observationID, "duplicate_target"), nil
		}
		return DiscoveryRevisionView{}, mapDesignError(err, "discovery case draft")
	}

	data, err := s.repo.ReadCase(ctx, versionID, created.CaseID, &created.CaseVersionID)
	if err != nil {
		return DiscoveryRevisionView{}, mapDesignError(err, "discovery case draft")
	}
	steps, assertions := design.CaseVersionContentCounts(data.Selected)
	if steps < design.MinimumCaseVersionSteps || assertions < design.MinimumCaseVersionAssertions {
		return discoveryDraftView(data.Selected, "incomplete_content"), nil
	}

	// Critic ④: no confirmed case of the requirement already covers the same
	// target with the same content.
	pageID, apiID := int64(0), int64(0)
	if kind == "page" {
		pageID = resourceID
	} else {
		apiID = resourceID
	}
	duplicate, found, err := s.revisionStore.FindConfirmedCaseByContent(
		ctx, versionID, requirementID, data.Selected.ContentHash, pageID, apiID)
	if err != nil {
		return DiscoveryRevisionView{}, err
	}
	if found && duplicate != data.Selected.ID {
		return discoveryDraftView(data.Selected, "duplicate_target"), nil
	}

	// Low confidence: R-EX-6 requires at least two evidence rows for an
	// automatic confirmation; anything less stays a human draft.
	if provenance.EvidenceRows < discoveryRevisionEvidenceThreshold {
		return discoveryDraftView(data.Selected, "insufficient_evidence"), nil
	}

	observationRef := observationID
	confirmResult, err := s.Confirm(ctx, versionID, ConfirmInput{
		Scope:               design.ConfirmScopeCase,
		TargetID:            created.CaseID,
		AutoConfirmed:       true,
		AutoConfirmedBy:     design.AutoConfirmationActor,
		Trigger:             design.CaseTriggerDiscoveryDelta,
		SourceObservationID: &observationRef,
	}, 0)
	if err != nil {
		return DiscoveryRevisionView{}, err
	}
	if len(confirmResult.Confirmed) == 0 {
		return DiscoveryRevisionView{}, platform.NewError(http.StatusConflict, "case_not_confirmed",
			"the additive discovery case could not be confirmed")
	}
	view := DiscoveryRevisionView{
		CaseID:              created.CaseID,
		CaseVersionID:       created.CaseVersionID,
		Status:              design.CaseStatusConfirmed,
		Trigger:             design.CaseTriggerDiscoveryDelta,
		ResourceID:          resourceID,
		SourceObservationID: observationID,
		Revision:            data.Selected.Revision,
	}
	s.publishDiscoveryRevisionEvent(ctx, versionID, requirementID, view, "high", design.AutoConfirmationActor, 0)
	return view, nil
}

// Rollback supersedes one auto-confirmed discovery revision and creates a
// replacement revision. It is idempotent: repeating the rollback returns the
// existing replacement.
func (s *GraphService) Rollback(ctx context.Context, versionID, caseVersionID int64, reason string, actorID int64) (DiscoveryRevisionView, error) {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return DiscoveryRevisionView{}, err
	}
	if err := requirePositiveID("case_version_id", caseVersionID); err != nil {
		return DiscoveryRevisionView{}, err
	}
	if s == nil || s.repo == nil {
		return DiscoveryRevisionView{}, platform.Unavailable("design rollback is not configured")
	}
	// Idempotency: a repeat returns the existing replacement.
	if replacement, found, err := s.repo.CaseVersionByRollbackOf(ctx, versionID, caseVersionID); err != nil {
		return DiscoveryRevisionView{}, mapDesignError(err, "case version")
	} else if found {
		return discoveryRevisionView(replacement), nil
	}

	history, err := s.repo.ReadCaseVersionsByIDs(ctx, versionID, []int64{caseVersionID})
	if err != nil {
		return DiscoveryRevisionView{}, mapDesignError(err, "case version")
	}
	if len(history) == 0 || history[0].CaseID <= 0 {
		return DiscoveryRevisionView{}, platform.NotFound("case version")
	}
	caseID := history[0].CaseID
	data, err := s.repo.ReadCase(ctx, versionID, caseID, &caseVersionID)
	if err != nil {
		return DiscoveryRevisionView{}, mapDesignError(err, "case version")
	}
	original := data.Selected
	if err := rollbackableCaseVersion(original); err != nil {
		return DiscoveryRevisionView{}, err
	}
	requirementID := data.Requirement.ID

	now := s.clock.Now().UTC()
	var replacement design.CaseVersion
	replayed := false
	err = s.repo.WithRollbackLock(ctx, versionID, requirementID, func(ctx context.Context, tx design.RollbackTx) error {
		locked, err := tx.CaseVersionForUpdate(ctx, caseVersionID)
		if err != nil {
			return err
		}
		// A concurrent rollback may have committed between the pre-check and
		// this transaction; return its replacement instead of a conflict.
		if locked.Status == design.CaseStatusSuperseded {
			existing, err := tx.RollbackReplacement(ctx, caseID, caseVersionID)
			if err != nil {
				return err
			}
			if existing == nil {
				return platform.StateConflict("the case version is superseded and has no rollback replacement")
			}
			replacement = *existing
			replayed = true
			return nil
		}
		if err := rollbackableCaseVersion(locked); err != nil {
			return err
		}
		base := locked
		prior, err := tx.PriorConfirmedVersion(ctx, caseID, locked.Revision)
		if err == nil {
			base = prior
		} else if !errors.Is(err, design.ErrNotFound) {
			return err
		}
		next := buildRollbackReplacement(s.ids, base, locked, now)
		stored, err := tx.InsertCaseVersion(ctx, next)
		if err != nil {
			return err
		}
		if err := tx.SupersedeCaseVersion(ctx, locked.ID, now); err != nil {
			return err
		}
		if err := tx.SetCurrentVersion(ctx, caseID, stored.ID, design.CaseStatusConfirmed, now); err != nil {
			return err
		}
		batchID, err := design.NewBatchID()
		if err != nil {
			return err
		}
		observation := locked.SourceObservationID
		if err := tx.InsertConfirmation(ctx, design.Confirmation{
			ID:                     s.ids.Next(),
			CaseVersionID:          stored.ID,
			Scope:                  design.ConfirmScopeCase,
			TargetScopeID:          caseID,
			BatchID:                batchID,
			// The confirmation freezes the replacement, exactly like the normal
			// Confirm path lists the versions it confirms. The rolled-back
			// original is recorded in the snapshot (rollback_of) and was already
			// superseded, not confirmed by this batch.
			AffectedCaseVersionIDs: []string{platformid.Format(stored.ID)},
			Snapshot: map[string]any{
				"trigger":     design.CaseTriggerRollback,
				"rollback_of": platformid.Format(locked.ID),
				"reason":      strings.TrimSpace(reason),
			},
			ConfirmedBy:         0,
			ConfirmedAt:         now,
			AutoConfirmed:       true,
			AutoConfirmedBy:     design.AutoConfirmationActor,
			Trigger:             design.CaseTriggerRollback,
			SourceObservationID: observation,
		}); err != nil {
			return err
		}
		replacement = stored
		return nil
	})
	if err != nil {
		return DiscoveryRevisionView{}, mapDesignError(err, "case rollback")
	}

	actor := design.AutoConfirmationActor
	if actorID > 0 {
		actor = platformid.Format(actorID)
	}
	// EV-12 keeps the same target provenance as the auto-confirmation it
	// reverses: derive the discovered page/API id from the revision that is
	// being rolled back, falling back to the replacement it produced.
	resourceID := caseVersionResourceID(original)
	if resourceID <= 0 {
		resourceID = caseVersionResourceID(replacement)
	}
	view := DiscoveryRevisionView{
		CaseID:              replacement.CaseID,
		CaseVersionID:       replacement.ID,
		Status:              replacement.Status,
		Trigger:             design.CaseTriggerRollback,
		ResourceID:          resourceID,
		SourceObservationID: derefInt64(replacement.SourceObservationID),
		RollbackOf:          caseVersionID,
		Revision:            replacement.Revision,
	}
	if replayed {
		// The pre-check normally returns the existing replacement before the
		// transaction; this only covers a concurrent rollback that committed
		// between the pre-check and the lock. Do not emit a second event/audit.
		return view, nil
	}
	s.publishDiscoveryRevisionEvent(ctx, versionID, requirementID, view, "high", actor, actorID)
	if s.audit != nil {
		auditEvent := auditapp.Event{
			Action:     "case.rolled_back",
			TargetKind: "case",
			TargetID:   platformid.Format(caseID),
			RequestID:  requestid.From(ctx),
			Metadata: map[string]any{
				"case_id":          platformid.Format(caseID),
				"case_version_id":  platformid.Format(replacement.ID),
				"rolled_back_from": platformid.Format(caseVersionID),
				"trigger":          design.CaseTriggerRollback,
				"reason":           strings.TrimSpace(reason),
			},
		}
		if actorID > 0 {
			actorIDCopy := actorID
			auditEvent.ActorID = &actorIDCopy
		}
		_ = s.audit.Record(ctx, auditEvent)
	}
	return view, nil
}

// RollbackCaseVersion is the HTTP application entry point of Rollback: it maps
// the internal revision result onto the API projection.
func (s *GraphService) RollbackCaseVersion(ctx context.Context, versionID, caseVersionID int64, reason string, actorID int64) (CaseVersionRollbackView, error) {
	view, err := s.Rollback(ctx, versionID, caseVersionID, reason, actorID)
	if err != nil {
		return CaseVersionRollbackView{}, err
	}
	return view.CaseVersionRollback(), nil
}

// CaseVersionRollback is the HTTP-facing rollback result projection.
func (v DiscoveryRevisionView) CaseVersionRollback() CaseVersionRollbackView {
	return CaseVersionRollbackView{
		CaseID:         v.CaseID,
		CaseVersionID:  v.CaseVersionID,
		RolledBackFrom: v.RollbackOf,
		Revision:       v.Revision,
		Status:         v.Status,
		Trigger:        v.Trigger,
	}
}

// ResourceConfirmedObserver adapts the system-channel resource confirm events
// onto the discovery revision service (W1-14B observer pattern). It is
// best-effort: every failure is logged at WARN and swallowed, so a provenance
// read failure can never fail or block the resource confirmation publish.
func (s *GraphService) ResourceConfirmedObserver(logger *slog.Logger) eventingapp.EventObserver {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, event eventing.Event) error {
		if event.Channel != "system" {
			return nil
		}
		kind, ok := resourceConfirmedRevisionEvents[event.Type]
		if !ok {
			return nil
		}
		resourceID, ok := discoveryResourceEventID(event.Payload)
		if !ok || s == nil || s.revisionStore == nil {
			return nil
		}
		if ctx == nil {
			ctx = context.Background()
		}
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), discoveryRevisionTimeout)
		defer cancel()
		provenances, err := s.revisionStore.ReadProvenanceByResource(writeCtx, resourceID, kind)
		if err != nil {
			logger.WarnContext(writeCtx, "design discovery revision: provenance read failed",
				slog.String("type", event.Type),
				slog.Int64("resource_id", resourceID),
				slog.String("kind", kind),
				slog.String("error", err.Error()),
			)
			return nil
		}
		// One additive revision per version: the earliest observation of the
		// resource is the natural key, so replaying the event is idempotent.
		seenVersions := make(map[int64]bool, len(provenances))
		for _, provenance := range provenances {
			if provenance.VersionID <= 0 || seenVersions[provenance.VersionID] {
				continue
			}
			seenVersions[provenance.VersionID] = true
			if _, err := s.OnResourceConfirmed(writeCtx, provenance.VersionID, resourceID,
				provenance.ObservationID, provenance.RequirementID); err != nil {
				logger.WarnContext(writeCtx, "design discovery revision: auto-confirm failed",
					slog.String("type", event.Type),
					slog.Int64("resource_id", resourceID),
					slog.Int64("version_id", provenance.VersionID),
					slog.Int64("observation_id", provenance.ObservationID),
					slog.String("error", err.Error()),
				)
			}
		}
		return nil
	}
}

var resourceConfirmedRevisionEvents = map[string]string{
	"resource.page.confirmed": "page",
	"resource.api.confirmed":  "api",
}

// discoveryRevisionView projects a persisted revision onto the service view.
func discoveryRevisionView(version design.CaseVersion) DiscoveryRevisionView {
	return DiscoveryRevisionView{
		CaseID:              version.CaseID,
		CaseVersionID:       version.ID,
		Status:              version.Status,
		Trigger:             version.Trigger,
		ResourceID:          caseVersionResourceID(version),
		SourceObservationID: derefInt64(version.SourceObservationID),
		RollbackOf:          derefInt64(version.RollbackOf),
		Revision:            version.Revision,
	}
}

func discoveryDraftView(version design.CaseVersion, reason string) DiscoveryRevisionView {
	view := discoveryRevisionView(version)
	view.Status = design.CaseStatusDraft
	view.LowConfidenceReason = reason
	return view
}

func lowConfidenceDraft(resourceID, observationID int64, reason string) DiscoveryRevisionView {
	return DiscoveryRevisionView{
		Status:              design.CaseStatusDraft,
		Trigger:             design.CaseTriggerDiscoveryDelta,
		ResourceID:          resourceID,
		SourceObservationID: observationID,
		LowConfidenceReason: reason,
	}
}

// discoveryAugmentedDraft builds the additive draft content for the approved
// target. The title/steps are deterministic so a replay produces the same
// natural key, and the target page/API is bound on the scenario, the case and
// the step.
func discoveryAugmentedDraft(provenance DiscoveryProvenance, resourceID int64, approved DiscoveryApprovedResource, requirement design.RequirementRef, versionSUTID, observationID int64) AugmentedCaseDraft {
	label := strings.TrimSpace(approved.Label)
	if label == "" {
		label = strings.TrimSpace(approved.Template)
	}
	if label == "" {
		label = platformid.Format(resourceID)
	}
	action, expected, assertionKind, assertionExpected := "打开 "+label, "页面可访问", design.AssertionUI, "页面可见"
	if provenance.Kind == "api" {
		action, expected, assertionKind, assertionExpected = "请求 "+label, "接口可调用", design.AssertionAPI, "响应成功"
	}
	title := truncateRunes("探索补用例："+label, design.CaseTitleMax)
	step := AugmentedCaseDraftStep{
		Seq: 1, Action: action, Expected: expected,
		TargetVersionSUTID: &versionSUTID,
	}
	assertion := AugmentedCaseDraftAssertion{
		StepSeq: 1, Kind: assertionKind, Target: label, Expected: assertionExpected,
		TargetVersionSUTID: &versionSUTID,
	}
	draft := AugmentedCaseDraft{
		RequirementID:        requirement.ID,
		RequirementVersionID: requirement.CurrentVersionID,
		VersionSUTID:         versionSUTID,
		Title:                title,
		Description:          "探索发现的新" + discoveryKindLabel(provenance.Kind) + "，自动生成的追加用例草案，等待确认",
		Priority:             design.PriorityP2,
		Steps:                []AugmentedCaseDraftStep{step},
		Assertions:           []AugmentedCaseDraftAssertion{assertion},
		SourceObservationID:  &observationID,
	}
	if provenance.Kind == "page" {
		draft.PageIDs = []int64{resourceID}
		step.TargetPageID = &resourceID
	} else {
		draft.APIIDs = []int64{resourceID}
		step.TargetAPIID = &resourceID
		assertion.TargetAPIID = &resourceID
	}
	draft.Steps = []AugmentedCaseDraftStep{step}
	draft.Assertions = []AugmentedCaseDraftAssertion{assertion}
	if provenance.FactID > 0 {
		draft.SourceFactIDs = []int64{provenance.FactID}
	}
	return draft
}

// caseVersionResourceID returns the discovered page/API id a case version is
// bound to. Additive discovery revisions carry it on the version (and, since a
// step may target the resource directly, defensively on the first step).
func caseVersionResourceID(version design.CaseVersion) int64 {
	for _, pageID := range version.PageIDs {
		if pageID > 0 {
			return pageID
		}
	}
	for _, apiID := range version.APIIDs {
		if apiID > 0 {
			return apiID
		}
	}
	for _, step := range version.Steps {
		if step.TargetPageID != nil && *step.TargetPageID > 0 {
			return *step.TargetPageID
		}
		if step.TargetAPIID != nil && *step.TargetAPIID > 0 {
			return *step.TargetAPIID
		}
	}
	return 0
}

// buildRollbackReplacement copies base into a new confirmed revision of the
// rolled-back case. The original stays in history as superseded; the
// replacement carries the rollback provenance (trigger/rollback_of).
func buildRollbackReplacement(ids platformid.Generator, base, original design.CaseVersion, now time.Time) design.CaseVersion {
	replacement := design.CaseVersion{
		ID:                  ids.Next(),
		CaseID:              original.CaseID,
		Revision:            original.Revision + 1,
		Title:               base.Title,
		Description:         base.Description,
		Preconditions:       base.Preconditions,
		TestData:            base.TestData,
		Priority:            base.Priority,
		DefaultVersionSUTID: base.DefaultVersionSUTID,
		VersionSUTIDs:       append([]int64(nil), base.VersionSUTIDs...),
		PageIDs:             append([]int64(nil), base.PageIDs...),
		APIIDs:              append([]int64(nil), base.APIIDs...),
		Status:              design.CaseStatusConfirmed,
		ContentHash:         base.ContentHash,
		Trigger:             design.CaseTriggerRollback,
		SourceObservationID: original.SourceObservationID,
		AutoConfirmedBy:     design.AutoConfirmationActor,
		RollbackOf:          &original.ID,
		CreatedAt:           now,
		UpdatedAt:           now,
		Steps:               make([]design.CaseStep, 0, len(base.Steps)),
		Assertions:          make([]design.CaseAssertion, 0, len(base.Assertions)),
		Sources:             make([]design.CaseSource, 0, len(base.Sources)),
	}
	for _, step := range base.Steps {
		copied := step
		copied.ID = ids.Next()
		copied.CaseVersionID = replacement.ID
		copied.CreatedAt = now
		replacement.Steps = append(replacement.Steps, copied)
	}
	for _, assertion := range base.Assertions {
		copied := assertion
		copied.ID = ids.Next()
		copied.CaseVersionID = replacement.ID
		copied.CreatedAt = now
		replacement.Assertions = append(replacement.Assertions, copied)
	}
	for _, source := range base.Sources {
		copied := source
		copied.ID = ids.Next()
		copied.CaseVersionID = replacement.ID
		copied.CreatedAt = now
		replacement.Sources = append(replacement.Sources, copied)
	}
	return replacement
}

// rollbackableCaseVersion enforces that only a current confirmed
// discovery_delta revision can be rolled back (a manual or already-rolled-back
// revision is a typed 409).
func rollbackableCaseVersion(version design.CaseVersion) error {
	switch {
	case version.Status != design.CaseStatusConfirmed:
		return platform.StateConflict("only a confirmed case version can be rolled back")
	case version.Trigger != design.CaseTriggerDiscoveryDelta:
		return platform.StateConflict("only a discovery_delta case revision can be rolled back")
	case version.AutoConfirmedBy != design.AutoConfirmationActor:
		// A human may confirm a low-confidence discovery draft through the
		// normal Confirm path; that revision is not an automatic revision, so
		// it is redesigned rather than auto-rolled-back.
		return platform.StateConflict("only a system auto-confirmed discovery revision can be rolled back")
	case version.RollbackOf != nil:
		return platform.StateConflict("a rollback replacement cannot be rolled back")
	}
	return nil
}

// publishDiscoveryRevisionEvent emits EV-12 `case.revision.auto_confirmed` on
// the version channel. Both the discovery auto-confirmation and the rollback
// replacement use the same event; rollback_of distinguishes them.
func (s *GraphService) publishDiscoveryRevisionEvent(ctx context.Context, versionID, requirementID int64, view DiscoveryRevisionView, confidence, actor string, actorID int64) {
	if s == nil || s.events == nil {
		return
	}
	payload := map[string]any{
		"version_id":            platformid.Format(versionID),
		"case_id":               platformid.Format(view.CaseID),
		"case_version_id":       platformid.Format(view.CaseVersionID),
		"trigger":               view.Trigger,
		"source_observation_id": platformid.Format(view.SourceObservationID),
		"resource_id":           platformid.Format(view.ResourceID),
		"rollback_of":           platformid.Format(view.RollbackOf),
		"confidence":            confidence,
		"actor":                 actor,
	}
	event := eventingapp.Event{
		Channel: "version:" + platformid.Format(versionID),
		Type:    "case.revision.auto_confirmed",
		Scope: map[string]any{
			"version_id":     platformid.Format(versionID),
			"requirement_id": platformid.Format(requirementID),
		},
		Payload:   payload,
		RequestID: requestid.From(ctx),
	}
	if actorID > 0 {
		actor := actorID
		event.ActorID = &actor
	}
	_ = s.events.Publish(ctx, event)
}

// discoveryResourceEventID parses the resource event `id` (decimal string or
// int64), mirroring the W1-14B observer.
func discoveryResourceEventID(payload map[string]any) (int64, bool) {
	if payload == nil {
		return 0, false
	}
	switch value := payload["id"].(type) {
	case string:
		id, err := platformid.Parse(strings.TrimSpace(value))
		if err != nil || id <= 0 {
			return 0, false
		}
		return id, true
	case int64:
		if value <= 0 {
			return 0, false
		}
		return value, true
	case int:
		if value <= 0 {
			return 0, false
		}
		return int64(value), true
	default:
		return 0, false
	}
}

func isStateConflictError(err error) bool {
	var platformErr *platform.Error
	if errors.As(err, &platformErr) {
		return platformErr.Status == http.StatusConflict
	}
	return errors.Is(err, design.ErrStateConflict)
}

func discoveryKindLabel(kind string) string {
	if kind == "api" {
		return "接口"
	}
	return "页面"
}

func truncateRunes(value string, max int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max])
}
