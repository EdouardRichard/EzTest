package designapp

import (
	"context"
	"testing"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// R-E2r (owner 2026-11-18): discovery auto-confirm is formally ABOLISHED. The
// five surviving Critic predicates still gate whether an approval becomes a
// draft at all, but passing them no longer confirms anything: the draft enters
// the normal judge scheduling and a human confirms after review. These tests
// pin the new contract from the spec (SC-E6), not the deleted confirmed shape.

// TestDiscoveryNoAutoConfirm pins that when every predicate passes the service
// creates a draft and STOPS — no Confirm call, no confirmation row, no
// auto-confirm event — and reports judgment_pending so the draft is routed to
// the independent judge and then to a human.
func TestDiscoveryNoAutoConfirm(t *testing.T) {
	d := newDiscoveryRevisionTest(t)
	view, err := d.svc.OnResourceConfirmed(context.Background(), d.versionID, d.pageID, d.observationID, d.requirementID)
	if err != nil {
		t.Fatalf("OnResourceConfirmed: %v", err)
	}
	if view.Status != design.CaseStatusDraft {
		t.Fatalf("status = %q, want draft (no auto-confirm)", view.Status)
	}
	if view.LowConfidenceReason != "judgment_pending" {
		t.Fatalf("low_confidence_reason = %q, want judgment_pending", view.LowConfidenceReason)
	}
	// The predicates passed, so a real draft must be persisted (not a zero-id
	// fail-closed result): the judge and the human need a case version to act on.
	if view.CaseID <= 0 || view.CaseVersionID <= 0 {
		t.Fatalf("five predicates passed but no draft was attached: %+v", view)
	}
	if view.Trigger != design.CaseTriggerDiscoveryDelta {
		t.Fatalf("trigger = %q, want discovery_delta", view.Trigger)
	}
	if len(d.repo.confirmations) != 0 {
		t.Fatalf("discovery must not auto-confirm: %d confirmation rows", len(d.repo.confirmations))
	}
	if len(d.eventsOfType("case.revision.auto_confirmed")) != 0 {
		t.Fatalf("discovery draft emitted an auto-confirm event: %v", d.events.types())
	}
	stored := d.caseVersion(view.CaseVersionID)
	if stored.Status != design.CaseStatusDraft {
		t.Fatalf("stored status = %q, want draft", stored.Status)
	}
	if stored.AutoConfirmedBy != "" {
		t.Fatalf("stored auto_confirmed_by = %q, want empty (no system actor)", stored.AutoConfirmedBy)
	}
	if stored.Trigger != design.CaseTriggerDiscoveryDelta {
		t.Fatalf("stored trigger = %q, want discovery_delta", stored.Trigger)
	}
	if stored.SourceObservationID == nil || *stored.SourceObservationID != d.observationID {
		t.Fatalf("stored observation = %v, want %d", stored.SourceObservationID, d.observationID)
	}
	// The case entity projects the draft, never a confirmed current version.
	item := d.repo.cases[view.CaseID]
	if item == nil || item.entity.Status != design.CaseStatusDraft {
		t.Fatalf("case entity = %+v, want draft current", item)
	}
}

// TestRollbackReplacementIsDraft pins that rolling back a legacy pre-E
// system-auto-confirmed discovery revision produces a DRAFT replacement
// (trigger=rollback) that awaits judge review + human confirm: no system actor,
// no confirmation row, no bypass of Confirm. The legacy original is still
// superseded (rollback intent = undo the old auto-confirm).
func TestRollbackReplacementIsDraft(t *testing.T) {
	d := newDiscoveryRevisionTest(t)
	orig := d.seedLegacySystemConfirmedDiscoveryRevision(7001)

	view, err := d.svc.Rollback(context.Background(), d.versionID, orig.ID, "探索误判", 9)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if view.Status != design.CaseStatusDraft {
		t.Fatalf("replacement status = %q, want draft", view.Status)
	}
	if view.Trigger != design.CaseTriggerRollback || view.RollbackOf != orig.ID {
		t.Fatalf("replacement = %+v, want rollback of %d", view, orig.ID)
	}
	stored := d.caseVersion(view.CaseVersionID)
	if stored.Status != design.CaseStatusDraft {
		t.Fatalf("stored replacement status = %q, want draft", stored.Status)
	}
	if stored.AutoConfirmedBy != "" {
		t.Fatalf("replacement auto_confirmed_by = %q, want empty (no system auto-confirm)", stored.AutoConfirmedBy)
	}
	if stored.RollbackOf == nil || *stored.RollbackOf != orig.ID {
		t.Fatalf("replacement rollback_of = %v, want %d", stored.RollbackOf, orig.ID)
	}
	// Confirm is the only terminal entry: the rollback must not write a
	// confirmation row for the draft replacement.
	for _, c := range d.repo.confirmations {
		if c.CaseVersionID == view.CaseVersionID {
			t.Fatalf("rollback bypassed Confirm with a confirmation row: %+v", c)
		}
	}
	// The legacy original remains auditable history, superseded.
	if got := d.caseVersion(orig.ID).Status; got != design.CaseStatusSuperseded {
		t.Fatalf("original status = %q, want superseded", got)
	}
	// The case entity now points at the draft replacement, not a confirmed one.
	item := d.repo.cases[orig.CaseID]
	if item.entity.Status != design.CaseStatusDraft ||
		item.entity.CurrentVersionID == nil || *item.entity.CurrentVersionID != view.CaseVersionID {
		t.Fatalf("case entity = %+v, want current draft replacement %d", item.entity, view.CaseVersionID)
	}
	// Rollback keeps the discovered resource provenance for the audit trail.
	if view.ResourceID != d.pageID {
		t.Fatalf("rollback resource_id = %d, want %d", view.ResourceID, d.pageID)
	}
}

// TestRollbackRejectsPostAbolitionDiscoveryDraft pins the legacy-only semantics
// of Rollback after R-E2r: a NEW discovery revision is a draft (never a system
// auto-confirm), so it is not rollbackable. Rollback exists only to undo pre-E
// history; new drafts are redesigned/reviewed, not auto-rolled-back.
func TestRollbackRejectsPostAbolitionDiscoveryDraft(t *testing.T) {
	d := newDiscoveryRevisionTest(t)
	view, err := d.svc.OnResourceConfirmed(context.Background(), d.versionID, d.pageID, d.observationID, d.requirementID)
	if err != nil {
		t.Fatalf("OnResourceConfirmed: %v", err)
	}
	if view.Status != design.CaseStatusDraft {
		t.Fatalf("precondition: discovery revision = %q, want draft", view.Status)
	}
	_, err = d.svc.Rollback(context.Background(), d.versionID, view.CaseVersionID, "why", 9)
	if err == nil {
		t.Fatal("a post-abolition discovery draft must not be rollbackable")
	}
	apiErr := platform.AsError(err)
	if apiErr.Status != 409 {
		t.Fatalf("error = %d/%s, want 409", apiErr.Status, apiErr.Code)
	}
}

// seedLegacySystemConfirmedDiscoveryRevision models a pre-E discovery revision:
// confirmed, trigger=discovery_delta, auto_confirmed_by=system, with the
// discovered page as a provenance source. After R-E2r no NEW revision of this
// shape is produced (OnResourceConfirmed stops at draft), so the rollback tests
// seed the legacy history directly rather than through a code path that no
// longer creates it.
func (h *discoveryRevisionHarness) seedLegacySystemConfirmedDiscoveryRevision(caseID int64) design.CaseVersion {
	h.t.Helper()
	scenarioID := int64(5001)
	h.repo.scenarios = append(h.repo.scenarios, design.Scenario{
		ID: scenarioID, VersionID: h.versionID, RequirementID: h.requirementID,
		RequirementVersionID: h.requirementVersionID, Name: "legacy discovery",
	})
	revision := fakeCaseVersion(caseID, 1, "探索补用例：legacy", 1, 1, design.CaseStatusConfirmed)
	revision.Trigger = design.CaseTriggerDiscoveryDelta
	revision.AutoConfirmedBy = design.AutoConfirmationActor
	observation := h.observationID
	revision.SourceObservationID = &observation
	revision.Sources = []design.CaseSource{{
		Kind: design.SourcePage, RefID: platformid.Format(h.pageID), Path: "discovery_delta",
	}}
	versionID := revision.ID
	item := &fakeCase{
		entity: design.CaseEntity{
			ID: caseID, ScenarioID: scenarioID, CurrentVersionID: &versionID, Status: design.CaseStatusConfirmed,
		},
		versions: []design.CaseVersion{revision},
	}
	item.current = &item.versions[0]
	h.repo.cases[caseID] = item
	return revision
}
