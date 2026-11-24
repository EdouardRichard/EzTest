package designapp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// fakeDiscoveryStore is the in-memory discovery revision read port.
type fakeDiscoveryStore struct {
	byObservation  map[int64]DiscoveryProvenance
	byResource     map[string][]DiscoveryProvenance
	resources      map[int64]DiscoveryApprovedResource
	duplicates     map[int64]int64
	readErr        error
	lastContentKey discoveryContentKey
}

func newFakeDiscoveryStore() *fakeDiscoveryStore {
	return &fakeDiscoveryStore{
		byObservation: map[int64]DiscoveryProvenance{},
		byResource:    map[string][]DiscoveryProvenance{},
		resources:     map[int64]DiscoveryApprovedResource{},
		duplicates:    map[int64]int64{},
	}
}

func discoveryResourceKey(resourceID int64, kind string) string {
	return fmt.Sprintf("%d|%s", resourceID, kind)
}

func (f *fakeDiscoveryStore) seedProvenance(provenance DiscoveryProvenance) {
	f.byObservation[provenance.ObservationID] = provenance
	f.byResource[discoveryResourceKey(provenance.ResourceID, provenance.Kind)] =
		append(f.byResource[discoveryResourceKey(provenance.ResourceID, provenance.Kind)], provenance)
	sort.Slice(f.byResource[discoveryResourceKey(provenance.ResourceID, provenance.Kind)], func(i, j int) bool {
		return f.byResource[discoveryResourceKey(provenance.ResourceID, provenance.Kind)][i].ObservationID <
			f.byResource[discoveryResourceKey(provenance.ResourceID, provenance.Kind)][j].ObservationID
	})
}

func (f *fakeDiscoveryStore) ReadProvenanceByResource(_ context.Context, resourceID int64, kind string) ([]DiscoveryProvenance, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	return append([]DiscoveryProvenance(nil), f.byResource[discoveryResourceKey(resourceID, kind)]...), nil
}

func (f *fakeDiscoveryStore) ReadProvenanceByObservation(_ context.Context, versionID, observationID int64) (DiscoveryProvenance, error) {
	provenance, ok := f.byObservation[observationID]
	if !ok || provenance.VersionID != versionID {
		return DiscoveryProvenance{}, design.ErrNotFound
	}
	return provenance, nil
}

func (f *fakeDiscoveryStore) ApprovedResource(_ context.Context, resourceID int64, kind string) (DiscoveryApprovedResource, bool, error) {
	resource, ok := f.resources[resourceID]
	if !ok || resource.Kind != kind {
		return DiscoveryApprovedResource{}, false, nil
	}
	return resource, true, nil
}

// FindConfirmedCaseByContent records the content key it was asked about so a test
// can prove the duplicate lookup is keyed on content (ruling 7) and not on a
// resource id.
func (f *fakeDiscoveryStore) FindConfirmedCaseByContent(_ context.Context, versionID, requirementID int64, contentHash, sutDescription, targetDescription string) (int64, bool, error) {
	f.lastContentKey = discoveryContentKey{
		versionID:         versionID,
		requirementID:     requirementID,
		contentHash:       contentHash,
		sutDescription:    sutDescription,
		targetDescription: targetDescription,
	}
	id, ok := f.duplicates[requirementID]
	return id, ok, nil
}

// discoveryContentKey is the Critic ④ lookup argument snapshot.
type discoveryContentKey struct {
	versionID         int64
	requirementID     int64
	contentHash       string
	sutDescription    string
	targetDescription string
}

// ---------- fake rollback persistence ----------

func (f *fakeRepo) CaseVersionByObservation(_ context.Context, versionID, observationID int64) (design.CaseVersion, bool, error) {
	for _, item := range f.cases {
		for _, version := range item.versions {
			if version.SourceObservationID != nil && *version.SourceObservationID == observationID {
				return version, true, nil
			}
		}
	}
	return design.CaseVersion{}, false, nil
}

func (f *fakeRepo) CaseVersionByRollbackOf(_ context.Context, versionID, rolledBackVersionID int64) (design.CaseVersion, bool, error) {
	for _, item := range f.cases {
		for _, version := range item.versions {
			if version.RollbackOf != nil && *version.RollbackOf == rolledBackVersionID {
				return version, true, nil
			}
		}
	}
	return design.CaseVersion{}, false, nil
}

func (f *fakeRepo) WithRollbackLock(ctx context.Context, versionID, requirementID int64, fn func(context.Context, design.RollbackTx) error) error {
	if versionID != f.version.VersionID || requirementID != f.requirement.ID {
		return design.ErrNotFound
	}
	return fn(ctx, &fakeRollbackTx{repo: f})
}

type fakeRollbackTx struct{ repo *fakeRepo }

func (t *fakeRollbackTx) versionByID(caseVersionID int64) (design.CaseVersion, bool) {
	for _, item := range t.repo.cases {
		for _, version := range item.versions {
			if version.ID == caseVersionID {
				return version, true
			}
		}
	}
	return design.CaseVersion{}, false
}

func (t *fakeRollbackTx) Requirement(context.Context) (design.RequirementRef, error) {
	return t.repo.requirement, nil
}

func (t *fakeRollbackTx) CaseVersionForUpdate(_ context.Context, caseVersionID int64) (design.CaseVersion, error) {
	version, ok := t.versionByID(caseVersionID)
	if !ok {
		return design.CaseVersion{}, design.ErrNotFound
	}
	return version, nil
}

func (t *fakeRollbackTx) RollbackReplacement(_ context.Context, caseID, rolledBackVersionID int64) (*design.CaseVersion, error) {
	for _, item := range t.repo.cases {
		for _, version := range item.versions {
			if item.entity.ID != caseID || version.RollbackOf == nil || *version.RollbackOf != rolledBackVersionID {
				continue
			}
			copied := version
			return &copied, nil
		}
	}
	return nil, nil
}

func (t *fakeRollbackTx) PriorConfirmedVersion(_ context.Context, caseID int64, beforeRevision int) (design.CaseVersion, error) {
	item, ok := t.repo.cases[caseID]
	if !ok {
		return design.CaseVersion{}, design.ErrNotFound
	}
	versions := append([]design.CaseVersion(nil), item.versions...)
	sort.Slice(versions, func(i, j int) bool { return versions[i].Revision > versions[j].Revision })
	for _, version := range versions {
		if version.Revision < beforeRevision && version.Status == design.CaseStatusConfirmed {
			return version, nil
		}
	}
	return design.CaseVersion{}, design.ErrNotFound
}

func (t *fakeRollbackTx) InsertCaseVersion(_ context.Context, version design.CaseVersion) (design.CaseVersion, error) {
	item, ok := t.repo.cases[version.CaseID]
	if !ok {
		return design.CaseVersion{}, design.ErrNotFound
	}
	item.versions = append(item.versions, version)
	copied := version
	item.current = &copied
	return copied, nil
}

func (t *fakeRollbackTx) SupersedeCaseVersion(_ context.Context, caseVersionID int64, _ time.Time) error {
	for _, item := range t.repo.cases {
		for index := range item.versions {
			if item.versions[index].ID == caseVersionID {
				item.versions[index].Status = design.CaseStatusSuperseded
			}
		}
	}
	return nil
}

func (t *fakeRollbackTx) SetCurrentVersion(_ context.Context, caseID, caseVersionID int64, status string, _ time.Time) error {
	item, ok := t.repo.cases[caseID]
	if !ok {
		return design.ErrNotFound
	}
	item.entity.CurrentVersionID = &caseVersionID
	item.entity.Status = status
	return nil
}

// ---------- harness ----------

type discoveryRevisionHarness struct {
	t        *testing.T
	repo     *fakeRepo
	svc      *GraphService
	store    *fakeDiscoveryStore
	events   *fakePublisher
	audit    *fakeAudit
	caseRuns []int64

	versionID            int64
	requirementID        int64
	requirementVersionID int64
	versionSUTID         int64
	pageID               int64
	observationID        int64
	caseRunID            int64
}

func newDiscoveryRevisionTest(t *testing.T) *discoveryRevisionHarness {
	t.Helper()
	repo := newFakeRepo()
	events := &fakePublisher{}
	audit := &fakeAudit{}
	svc := NewGraphService(repo, &fakeIDs{}, newFixedClock(), audit, events, &fakeTeamRoom{}).
		SetJudgmentGate(alwaysPassJudgmentGate{})
	store := newFakeDiscoveryStore()
	store.resources[6001] = DiscoveryApprovedResource{
		ID: 6001, Kind: "page", Status: "approved", Label: "/orders/{id}", Template: "/orders/{id}",
	}
	harness := &discoveryRevisionHarness{
		t: t, repo: repo, svc: svc, store: store, events: events, audit: audit,
		caseRuns:             []int64{9101, 9102},
		versionID:            1001,
		requirementID:        3001,
		requirementVersionID: 3002,
		versionSUTID:         9001,
		pageID:               6001,
		observationID:        8101,
		caseRunID:            7101,
	}
	store.seedProvenance(DiscoveryProvenance{
		VersionID: harness.versionID, ObservationID: harness.observationID,
		ResourceID: harness.pageID, Kind: "page",
		CaseRunID: harness.caseRunID, CaseVersionID: 5101,
		RequirementID: harness.requirementID, RequirementVersionID: harness.requirementVersionID,
		SUTID: harness.versionSUTID, FactID: 6101, EvidenceRows: 2,
		// v0.2.4-B: the additive draft inherits the covered_acs of the case
		// version whose execution produced the observation.
		CoveredACs: []string{"AC-1"},
	})
	svc.SetDiscoveryRevisionStore(store)
	return harness
}

func (h *discoveryRevisionHarness) CurrentWaveCaseRunIDs(_ int64) []int64 {
	return append([]int64(nil), h.caseRuns...)
}

func (h *discoveryRevisionHarness) eventsOfType(eventType string) []eventingapp.Event {
	return h.events.byType(eventType)
}

func equalIDs(left, right []int64) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// ---------- tests ----------

// TestDiscoveryDraftKeepsCurrentWaveAndLegacyRollback pins the R-E2r contract end
// to end: a resource approval whose five predicates pass yields a DRAFT
// (judgment_pending) that never mutates the current wave and never auto-confirms;
// and Rollback — now legacy-only — undoes a pre-E system auto-confirm by
// superseding it and inserting a DRAFT replacement, without bypassing Confirm.
func TestDiscoveryDraftKeepsCurrentWaveAndLegacyRollback(t *testing.T) {
	d := newDiscoveryRevisionTest(t)
	before := d.CurrentWaveCaseRunIDs(d.versionID)
	rev, err := d.svc.OnResourceConfirmed(context.Background(), d.versionID, d.pageID, d.observationID, d.requirementID)
	if err != nil {
		t.Fatalf("OnResourceConfirmed: %v", err)
	}
	// R-E2r: every predicate passing yields a draft awaiting judge + human, never
	// a confirmed revision.
	if rev.Status != design.CaseStatusDraft || rev.Trigger != design.CaseTriggerDiscoveryDelta || rev.LowConfidenceReason != "judgment_pending" {
		t.Fatalf("rev = %+v, want draft discovery_delta judgment_pending", rev)
	}
	if rev.CaseVersionID <= 0 || rev.CaseID <= 0 {
		t.Fatalf("rev ids = %+v", rev)
	}
	if !equalIDs(before, d.CurrentWaveCaseRunIDs(d.versionID)) {
		t.Fatal("current wave case_run ids changed")
	}
	if got := len(d.eventsOfType("case.revision.auto_confirmed")); got != 0 {
		t.Fatalf("auto-confirm events = %d, want 0 (%v)", got, d.events.types())
	}
	stored := d.caseVersion(rev.CaseVersionID)
	if stored.Status != design.CaseStatusDraft ||
		stored.Trigger != design.CaseTriggerDiscoveryDelta ||
		stored.AutoConfirmedBy != "" ||
		stored.SourceObservationID == nil || *stored.SourceObservationID != d.observationID {
		t.Fatalf("stored revision = %+v, want draft discovery_delta with no system actor", stored)
	}
	if len(d.repo.confirmations) != 0 {
		t.Fatalf("confirmations = %d, want 0 (discovery never confirms)", len(d.repo.confirmations))
	}
	if findAudit(d.audit.events, "case.confirmed") != nil {
		t.Fatalf("discovery draft emitted a confirm audit: %+v", d.audit.events)
	}

	// Rollback is legacy-only after R-E2r: the discovery draft above is not
	// rollbackable, so seed a pre-E system auto-confirm and roll THAT back.
	legacy := d.seedLegacySystemConfirmedDiscoveryRevision(7001)
	alt, err := d.svc.Rollback(context.Background(), d.versionID, legacy.ID, "revert", 9)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if alt.RollbackOf != legacy.ID || alt.Status != design.CaseStatusDraft || alt.Trigger != design.CaseTriggerRollback {
		t.Fatalf("alt = %+v, want draft rollback of %d", alt, legacy.ID)
	}
	if got := d.caseVersion(legacy.ID).Status; got != design.CaseStatusSuperseded {
		t.Fatalf("original status = %q, want superseded", got)
	}
	// Confirm is the only terminal entry: the rollback writes no confirmation row
	// for the draft replacement.
	if len(d.repo.confirmations) != 0 {
		t.Fatalf("confirmations = %d, want 0 (rollback no longer bypasses Confirm)", len(d.repo.confirmations))
	}
	if alt.ResourceID != d.pageID {
		t.Fatalf("rollback resource_id = %d, want %d", alt.ResourceID, d.pageID)
	}
	again, err := d.svc.Rollback(context.Background(), d.versionID, legacy.ID, "revert", 9)
	if err != nil || again.CaseVersionID != alt.CaseVersionID {
		t.Fatalf("rollback not idempotent: again=%+v err=%v", again, err)
	}
	if again.ResourceID != d.pageID {
		t.Fatalf("idempotent rollback resource_id = %d, want %d", again.ResourceID, d.pageID)
	}
	if len(d.repo.confirmations) != 0 {
		t.Fatalf("idempotent rollback wrote a confirmation: %d", len(d.repo.confirmations))
	}
	// The rollback still notifies via the legacy EV-12 event (rollback_of) and the
	// case.rolled_back audit. The discovery draft emitted no auto-confirm event, so
	// exactly one such event exists — the rollback's. (The event name/confidence is
	// a legacy contract retained pending T17; the replacement itself is a draft.)
	rollbackEvents := d.eventsOfType("case.revision.auto_confirmed")
	if len(rollbackEvents) != 1 {
		t.Fatalf("rollback events = %d, want 1 (%v)", len(rollbackEvents), d.events.types())
	}
	if rollbackEvents[0].Payload["trigger"] != design.CaseTriggerRollback ||
		rollbackEvents[0].Payload["rollback_of"] != platformid.Format(legacy.ID) ||
		rollbackEvents[0].Payload["resource_id"] != platformid.Format(d.pageID) {
		t.Fatalf("rollback event payload = %+v", rollbackEvents[0].Payload)
	}
	// Additive hardening (whole-branch review minor): the legacy event NAME says
	// "auto_confirmed" while the replacement is a DRAFT. The payload must carry the
	// authoritative status so a consumer cannot misread a draft as confirmed even
	// if it only reads the payload.
	if rollbackEvents[0].Payload["status"] != design.CaseStatusDraft {
		t.Fatalf("rollback event payload status = %v, want %q (draft, not confirmed)",
			rollbackEvents[0].Payload["status"], design.CaseStatusDraft)
	}
	rollbackAudit := findAudit(d.audit.events, "case.rolled_back")
	if rollbackAudit == nil ||
		rollbackAudit.Metadata["rolled_back_from"] != platformid.Format(legacy.ID) ||
		rollbackAudit.Metadata["trigger"] != design.CaseTriggerRollback {
		t.Fatalf("rollback audit = %+v", d.audit.events)
	}
}

func TestLowConfidenceStaysDraft(t *testing.T) {
	d := newDiscoveryRevisionTest(t)
	provenance := d.store.byObservation[d.observationID]
	provenance.EvidenceRows = 1
	d.store.seedProvenance(provenance)

	rev, err := d.svc.OnResourceConfirmed(context.Background(), d.versionID, d.pageID, d.observationID, d.requirementID)
	if err != nil {
		t.Fatalf("OnResourceConfirmed: %v", err)
	}
	if rev.Status != design.CaseStatusDraft || rev.CaseVersionID <= 0 {
		t.Fatalf("rev = %+v, want persisted draft", rev)
	}
	if rev.LowConfidenceReason != "insufficient_evidence" {
		t.Fatalf("reason = %q", rev.LowConfidenceReason)
	}
	if len(d.repo.confirmations) != 0 {
		t.Fatalf("low confidence confirmed %d case versions", len(d.repo.confirmations))
	}
	if len(d.eventsOfType("case.revision.auto_confirmed")) != 0 {
		t.Fatalf("low confidence emitted auto-confirm event")
	}
}

func TestMissingRequirementLinkReturnsDraft(t *testing.T) {
	d := newDiscoveryRevisionTest(t)
	provenance := d.store.byObservation[d.observationID]
	provenance.RequirementID = 0
	d.store.seedProvenance(provenance)

	rev, err := d.svc.OnResourceConfirmed(context.Background(), d.versionID, d.pageID, d.observationID, 0)
	if err != nil {
		t.Fatalf("OnResourceConfirmed: %v", err)
	}
	if rev.Status != design.CaseStatusDraft || rev.LowConfidenceReason != "missing_requirement" {
		t.Fatalf("rev = %+v", rev)
	}
	if rev.CaseVersionID != 0 {
		t.Fatalf("missing requirement created a case version %d", rev.CaseVersionID)
	}
}

func TestDuplicateTargetStaysDraft(t *testing.T) {
	d := newDiscoveryRevisionTest(t)
	d.store.duplicates[d.requirementID] = 9999

	rev, err := d.svc.OnResourceConfirmed(context.Background(), d.versionID, d.pageID, d.observationID, d.requirementID)
	if err != nil {
		t.Fatalf("OnResourceConfirmed: %v", err)
	}
	if rev.Status != design.CaseStatusDraft || rev.LowConfidenceReason != "duplicate_target" {
		t.Fatalf("rev = %+v", rev)
	}
	if len(d.repo.confirmations) != 0 {
		t.Fatalf("duplicate target was confirmed")
	}
}

// TestContractGapsAndUnapprovedResourceStayDraft pins the fail-closed gates that
// replaced Critic ⑥ ("target inside the version scope", deleted by ruling 7).
// With no resource scope left to check, what stops an approval from becoming a
// case is the §5.2 contract: the draft must be able to state the system under
// test and name at least one acceptance criterion it covers. When either source
// is empty the pass stays a draft rather than fabricating coverage -- a fabricated
// covered_acs would make E's union pre-check report a gap that does not exist.
func TestContractGapsAndUnapprovedResourceStayDraft(t *testing.T) {
	t.Run("missing_sut_description", func(t *testing.T) {
		d := newDiscoveryRevisionTest(t)
		d.repo.requirement.SutDescription = "   "
		rev, err := d.svc.OnResourceConfirmed(context.Background(), d.versionID, d.pageID, d.observationID, d.requirementID)
		if err != nil {
			t.Fatalf("OnResourceConfirmed: %v", err)
		}
		if rev.Status != design.CaseStatusDraft || rev.LowConfidenceReason != "missing_sut_description" {
			t.Fatalf("rev = %+v", rev)
		}
		if len(d.repo.cases) != 0 {
			t.Fatalf("no case may be written when the SUT description cannot be derived: %d", len(d.repo.cases))
		}
	})
	t.Run("missing_covered_acs", func(t *testing.T) {
		d := newDiscoveryRevisionTest(t)
		provenance := d.store.byObservation[d.observationID]
		provenance.CoveredACs = nil
		d.store.seedProvenance(provenance)
		rev, err := d.svc.OnResourceConfirmed(context.Background(), d.versionID, d.pageID, d.observationID, d.requirementID)
		if err != nil {
			t.Fatalf("OnResourceConfirmed: %v", err)
		}
		if rev.Status != design.CaseStatusDraft || rev.LowConfidenceReason != "missing_covered_acs" {
			t.Fatalf("rev = %+v", rev)
		}
		if len(d.repo.cases) != 0 {
			t.Fatalf("no case may be written when no AC can be inherited: %d", len(d.repo.cases))
		}
	})
	t.Run("unknown_observation_kind", func(t *testing.T) {
		d := newDiscoveryRevisionTest(t)
		provenance := d.store.byObservation[d.observationID]
		provenance.Kind = "database"
		d.store.seedProvenance(provenance)
		rev, err := d.svc.OnResourceConfirmed(context.Background(), d.versionID, d.pageID, d.observationID, d.requirementID)
		if err != nil {
			t.Fatalf("OnResourceConfirmed: %v", err)
		}
		if rev.Status != design.CaseStatusDraft || rev.LowConfidenceReason != "target_out_of_scope" {
			t.Fatalf("rev = %+v", rev)
		}
	})
	t.Run("resource_not_approved", func(t *testing.T) {
		d := newDiscoveryRevisionTest(t)
		resource := d.store.resources[d.pageID]
		resource.Status = "proposed"
		d.store.resources[d.pageID] = resource
		rev, err := d.svc.OnResourceConfirmed(context.Background(), d.versionID, d.pageID, d.observationID, d.requirementID)
		if err != nil {
			t.Fatalf("OnResourceConfirmed: %v", err)
		}
		if rev.Status != design.CaseStatusDraft || rev.LowConfidenceReason != "resource_not_approved" {
			t.Fatalf("rev = %+v", rev)
		}
	})
}

// TestDuplicateLookupIsKeyedOnContent pins ruling 7's main point: Critic ④ asks
// the store for a content identity (content_hash + sut_description + the step's
// target_description), never for a page/api id.
func TestDuplicateLookupIsKeyedOnContent(t *testing.T) {
	d := newDiscoveryRevisionTest(t)
	if _, err := d.svc.OnResourceConfirmed(context.Background(), d.versionID, d.pageID, d.observationID, d.requirementID); err != nil {
		t.Fatalf("OnResourceConfirmed: %v", err)
	}
	key := d.store.lastContentKey
	if key.versionID != d.versionID || key.requirementID != d.requirementID {
		t.Fatalf("content key scope = %+v", key)
	}
	if len(key.contentHash) != 64 {
		t.Fatalf("content key hash = %q, want the created version's content hash", key.contentHash)
	}
	if key.sutDescription != d.repo.requirement.SutDescription {
		t.Fatalf("content key sut_description = %q, want the requirement's %q",
			key.sutDescription, d.repo.requirement.SutDescription)
	}
	// The discovered resource's label becomes the step's target_description.
	if key.targetDescription != "/orders/{id}" {
		t.Fatalf("content key target_description = %q, want the discovered label", key.targetDescription)
	}
}

// TestDiscoveryDraftCarriesInheritedContract pins what the additive draft
// actually stores: the requirement's sut_description, the inherited covered_acs
// and an explicit "无" precondition, plus the discovered resource recovered as a
// page provenance source (which is how caseVersionResourceID still answers).
// R-E2r: the draft is judgment_pending, never auto-confirmed; the inherited
// contract content is unchanged.
func TestDiscoveryDraftCarriesInheritedContract(t *testing.T) {
	d := newDiscoveryRevisionTest(t)
	rev, err := d.svc.OnResourceConfirmed(context.Background(), d.versionID, d.pageID, d.observationID, d.requirementID)
	if err != nil {
		t.Fatalf("OnResourceConfirmed: %v", err)
	}
	if rev.Status != design.CaseStatusDraft || rev.LowConfidenceReason != "judgment_pending" {
		t.Fatalf("rev = %+v, want draft judgment_pending", rev)
	}
	if rev.ResourceID != d.pageID {
		t.Fatalf("resource id = %d, want %d (recovered from the page provenance source)", rev.ResourceID, d.pageID)
	}
	item := d.repo.cases[rev.CaseID]
	if item == nil || item.current == nil {
		t.Fatalf("case %d was not persisted", rev.CaseID)
	}
	version := item.current
	if version.SutDescription != d.repo.requirement.SutDescription {
		t.Fatalf("sut_description = %q", version.SutDescription)
	}
	if len(version.CoveredACs) != 1 || version.CoveredACs[0] != "AC-1" {
		t.Fatalf("covered_acs = %v, want the inherited [AC-1]", version.CoveredACs)
	}
	if version.Preconditions != "无" {
		t.Fatalf("preconditions = %q, want the explicit 无", version.Preconditions)
	}
	var pageSource bool
	for _, source := range version.Sources {
		if source.Kind == design.SourcePage && source.RefID == platformid.Format(d.pageID) {
			pageSource = true
		}
	}
	if !pageSource {
		t.Fatalf("the discovered page must remain a provenance source: %+v", version.Sources)
	}
}

func TestDiscoveryDraftIdempotentPerObservation(t *testing.T) {
	d := newDiscoveryRevisionTest(t)
	first, err := d.svc.OnResourceConfirmed(context.Background(), d.versionID, d.pageID, d.observationID, d.requirementID)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	eventsAfterFirst := len(d.events.events)
	second, err := d.svc.OnResourceConfirmed(context.Background(), d.versionID, d.pageID, d.observationID, d.requirementID)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.CaseVersionID != second.CaseVersionID {
		t.Fatalf("idempotency: first=%+v second=%+v", first, second)
	}
	// The replay projects the stored draft (the low-confidence reason is a
	// creation-time annotation, not re-derived on an idempotent hit).
	if second.Status != design.CaseStatusDraft {
		t.Fatalf("second = %+v, want the same draft", second)
	}
	if len(d.eventsOfType("case.revision.auto_confirmed")) != 0 {
		t.Fatalf("discovery draft emitted an auto-confirm event: %v", d.events.types())
	}
	// The idempotent replay short-circuits on the observation key and emits nothing
	// new (the draft-creation events fired once, on the first call).
	if len(d.events.events) != eventsAfterFirst {
		t.Fatalf("idempotent replay emitted new events: %d -> %d", eventsAfterFirst, len(d.events.events))
	}
}

func TestResourceConfirmedObserverMapsSystemEvent(t *testing.T) {
	d := newDiscoveryRevisionTest(t)
	observer := d.svc.ResourceConfirmedObserver(nil)
	event := eventing.Event{
		Channel: "system",
		Type:    "resource.page.confirmed",
		Payload: map[string]any{"id": "6001"},
	}
	if err := observer(context.Background(), event); err != nil {
		t.Fatalf("observer: %v", err)
	}
	// R-E2r: the observer creates a discovery DRAFT (judge + human confirm later),
	// never an auto-confirm. Draft creation is observable via the normal
	// case_version_created event, not case.revision.auto_confirmed.
	if len(d.eventsOfType("design.case_version_created")) != 1 {
		t.Fatalf("observer did not create a discovery draft (%v)", d.events.types())
	}
	if len(d.eventsOfType("case.revision.auto_confirmed")) != 0 {
		t.Fatalf("observer auto-confirmed (%v)", d.events.types())
	}

	// A non-system channel and an unrelated system event are ignored.
	ignored := newDiscoveryRevisionTest(t)
	ignoredObserver := ignored.svc.ResourceConfirmedObserver(nil)
	_ = ignoredObserver(context.Background(), eventing.Event{
		Channel: "version:1001", Type: "resource.page.confirmed", Payload: map[string]any{"id": "6001"},
	})
	_ = ignoredObserver(context.Background(), eventing.Event{
		Channel: "system", Type: "resource.page.updated", Payload: map[string]any{"id": "6001"},
	})
	if len(ignored.eventsOfType("design.case_version_created")) != 0 {
		t.Fatalf("observer reacted to an ignored event (%v)", ignored.events.types())
	}

	// A nil revision store is a safe no-op.
	bare := NewGraphService(newFakeRepo(), &fakeIDs{}, newFixedClock(), nil, &fakePublisher{}, nil)
	if err := bare.ResourceConfirmedObserver(nil)(context.Background(), event); err != nil {
		t.Fatalf("nil store observer: %v", err)
	}
}

func TestRollbackRejectsManualRevision(t *testing.T) {
	d := newDiscoveryRevisionTest(t)
	d.repo.scenarios = append(d.repo.scenarios, design.Scenario{
		ID: 5001, VersionID: d.versionID, RequirementID: d.requirementID,
		RequirementVersionID: d.requirementVersionID, Name: "manual",
	})
	seedFakeCaseVersions(t, d.repo, 8801, 5001, "manual case",
		fakeCaseVersionSpec{steps: 1, assertions: 1, status: design.CaseStatusConfirmed})
	manual := d.repo.cases[8801].current

	_, err := d.svc.Rollback(context.Background(), d.versionID, manual.ID, "why", 9)
	if err == nil {
		t.Fatal("rollback of a manual revision succeeded")
	}
	var platformErr *platform.Error
	if !asPlatformError(err, &platformErr) || platformErr.Status != 409 {
		t.Fatalf("err = %v, want 409", err)
	}
}

func TestRollbackRejectsHumanConfirmedDiscoveryRevision(t *testing.T) {
	d := newDiscoveryRevisionTest(t)
	// A human may confirm a discovery draft through the normal Confirm path: the
	// revision is confirmed and still discovery_delta, but carries no system
	// auto-confirm actor. After R-E2r OnResourceConfirmed only produces drafts, so
	// seed that human-confirmed shape directly (confirmed + discovery_delta, empty
	// auto_confirmed_by) rather than mutating an auto-confirm that no longer exists.
	rev := d.seedLegacySystemConfirmedDiscoveryRevision(7001)
	for _, item := range d.repo.cases {
		for index := range item.versions {
			if item.versions[index].ID == rev.ID {
				item.versions[index].AutoConfirmedBy = ""
			}
		}
	}

	_, err := d.svc.Rollback(context.Background(), d.versionID, rev.ID, "why", 9)
	var platformErr *platform.Error
	if !asPlatformError(err, &platformErr) || platformErr.Status != 409 {
		t.Fatalf("err = %v, want 409", err)
	}
}

func TestRollbackBasesReplacementOnPriorConfirmedRevision(t *testing.T) {
	d := newDiscoveryRevisionTest(t)
	d.repo.scenarios = append(d.repo.scenarios, design.Scenario{
		ID: 5001, VersionID: d.versionID, RequirementID: d.requirementID,
		RequirementVersionID: d.requirementVersionID, Name: "history",
	})
	item := &fakeCase{}
	base := fakeCaseVersion(8901, 1, "稳定基线", 1, 1, design.CaseStatusConfirmed)
	autorev := fakeCaseVersion(8901, 2, "探索新增", 1, 1, design.CaseStatusConfirmed)
	autorev.Trigger = design.CaseTriggerDiscoveryDelta
	autorev.AutoConfirmedBy = design.AutoConfirmationActor
	observation := int64(8101)
	autorev.SourceObservationID = &observation
	item.versions = []design.CaseVersion{base, autorev}
	item.current = &item.versions[1]
	versionID := autorev.ID
	item.entity = design.CaseEntity{
		ID: 8901, ScenarioID: 5001, CurrentVersionID: &versionID, Status: design.CaseStatusConfirmed,
	}
	d.repo.cases[8901] = item

	replacement, err := d.svc.Rollback(context.Background(), d.versionID, autorev.ID, "revert", 9)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	// R-E2r: the replacement is a DRAFT copy of the prior confirmed revision, not
	// a confirmed revision; it awaits judge review + human confirm.
	if replacement.RollbackOf != autorev.ID || replacement.Status != design.CaseStatusDraft {
		t.Fatalf("replacement = %+v, want draft rollback of %d", replacement, autorev.ID)
	}
	stored := d.caseVersion(replacement.CaseVersionID)
	if stored.Title != base.Title {
		t.Fatalf("replacement title = %q, want prior confirmed %q", stored.Title, base.Title)
	}
	if stored.AutoConfirmedBy != "" {
		t.Fatalf("replacement auto_confirmed_by = %q, want empty (no system auto-confirm)", stored.AutoConfirmedBy)
	}
	if d.caseVersion(autorev.ID).Status != design.CaseStatusSuperseded {
		t.Fatal("original auto-confirmed revision was not superseded")
	}
}

// findAudit returns the recorded audit event of one action, if any.
func findAudit(events []auditapp.Event, action string) *auditapp.Event {
	for index := range events {
		if events[index].Action == action {
			return &events[index]
		}
	}
	return nil
}

// caseVersion finds one persisted fake revision.
func (h *discoveryRevisionHarness) caseVersion(caseVersionID int64) design.CaseVersion {
	for _, item := range h.repo.cases {
		for _, version := range item.versions {
			if version.ID == caseVersionID {
				return version
			}
		}
	}
	h.t.Fatalf("case version %d not found", caseVersionID)
	return design.CaseVersion{}
}

func asPlatformError(err error, target **platform.Error) bool {
	return errors.As(err, target)
}
