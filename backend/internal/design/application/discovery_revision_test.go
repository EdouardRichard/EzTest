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
	byObservation map[int64]DiscoveryProvenance
	byResource    map[string][]DiscoveryProvenance
	resources     map[int64]DiscoveryApprovedResource
	duplicates    map[int64]int64
	readErr       error
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

func (f *fakeDiscoveryStore) FindConfirmedCaseByContent(_ context.Context, versionID, requirementID int64, contentHash string, pageID, apiID int64) (int64, bool, error) {
	id, ok := f.duplicates[requirementID]
	return id, ok, nil
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

func (t *fakeRollbackTx) InsertConfirmation(_ context.Context, confirmation design.Confirmation) error {
	t.repo.confirmations = append(t.repo.confirmations, confirmation)
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
	svc := NewGraphService(repo, &fakeIDs{}, newFixedClock(), audit, events, &fakeTeamRoom{})
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
		VersionSUTID: harness.versionSUTID, FactID: 6101, EvidenceRows: 2,
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

func TestAutoConfirmKeepsCurrentWaveAndRollback(t *testing.T) {
	d := newDiscoveryRevisionTest(t)
	before := d.CurrentWaveCaseRunIDs(d.versionID)
	rev, err := d.svc.OnResourceConfirmed(context.Background(), d.versionID, d.pageID, d.observationID, d.requirementID)
	if err != nil {
		t.Fatalf("OnResourceConfirmed: %v", err)
	}
	if rev.Status != design.CaseStatusConfirmed || rev.Trigger != design.CaseTriggerDiscoveryDelta {
		t.Fatalf("rev = %+v, want confirmed discovery_delta", rev)
	}
	if rev.CaseVersionID <= 0 || rev.CaseID <= 0 {
		t.Fatalf("rev ids = %+v", rev)
	}
	if !equalIDs(before, d.CurrentWaveCaseRunIDs(d.versionID)) {
		t.Fatal("current wave case_run ids changed")
	}
	if got := len(d.eventsOfType("case.revision.auto_confirmed")); got != 1 {
		t.Fatalf("auto-confirm events = %d, want 1 (%v)", got, d.events.types())
	}
	stored := d.caseVersion(rev.CaseVersionID)
	if stored.Status != design.CaseStatusConfirmed ||
		stored.Trigger != design.CaseTriggerDiscoveryDelta ||
		stored.AutoConfirmedBy != design.AutoConfirmationActor ||
		stored.SourceObservationID == nil || *stored.SourceObservationID != d.observationID {
		t.Fatalf("stored revision = %+v", stored)
	}
	if len(d.repo.confirmations) != 1 {
		t.Fatalf("confirmations = %d, want 1", len(d.repo.confirmations))
	}
	confirmation := d.repo.confirmations[0]
	if !confirmation.AutoConfirmed || confirmation.AutoConfirmedBy != design.AutoConfirmationActor ||
		confirmation.Trigger != design.CaseTriggerDiscoveryDelta ||
		confirmation.SourceObservationID == nil || *confirmation.SourceObservationID != d.observationID {
		t.Fatalf("confirmation = %+v", confirmation)
	}
	confirmAudit := findAudit(d.audit.events, "case.confirmed")
	if confirmAudit == nil ||
		confirmAudit.Metadata["trigger"] != design.CaseTriggerDiscoveryDelta ||
		confirmAudit.Metadata["auto_confirmed_by"] != design.AutoConfirmationActor ||
		confirmAudit.Metadata["source_observation_id"] != platformid.Format(d.observationID) {
		t.Fatalf("auto-confirm audit = %+v", d.audit.events)
	}

	alt, err := d.svc.Rollback(context.Background(), d.versionID, rev.CaseVersionID, "revert", 9)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if alt.RollbackOf != rev.CaseVersionID || alt.Status != design.CaseStatusConfirmed || alt.Trigger != design.CaseTriggerRollback {
		t.Fatalf("alt = %+v", alt)
	}
	if got := d.caseVersion(rev.CaseVersionID).Status; got != design.CaseStatusSuperseded {
		t.Fatalf("original status = %q, want superseded", got)
	}
	if len(d.repo.confirmations) != 2 {
		t.Fatalf("confirmations = %d, want auto-confirm + rollback = 2", len(d.repo.confirmations))
	}
	rollbackConfirmation := d.repo.confirmations[1]
	if len(rollbackConfirmation.AffectedCaseVersionIDs) != 1 ||
		rollbackConfirmation.AffectedCaseVersionIDs[0] != platformid.Format(alt.CaseVersionID) {
		t.Fatalf("rollback affected ids = %v, want [%s]",
			rollbackConfirmation.AffectedCaseVersionIDs, platformid.Format(alt.CaseVersionID))
	}
	if alt.ResourceID != d.pageID {
		t.Fatalf("rollback resource_id = %d, want %d", alt.ResourceID, d.pageID)
	}
	again, err := d.svc.Rollback(context.Background(), d.versionID, rev.CaseVersionID, "revert", 9)
	if err != nil || again.CaseVersionID != alt.CaseVersionID {
		t.Fatalf("rollback not idempotent: again=%+v err=%v", again, err)
	}
	if again.ResourceID != d.pageID {
		t.Fatalf("idempotent rollback resource_id = %d, want %d", again.ResourceID, d.pageID)
	}
	if len(d.repo.confirmations) != 2 {
		t.Fatalf("idempotent rollback wrote another confirmation: %d", len(d.repo.confirmations))
	}
	if got := len(d.eventsOfType("case.revision.auto_confirmed")); got != 2 {
		t.Fatalf("events = %d, want auto+rollback = 2", got)
	}
	rollbackEvents := 0
	for _, event := range d.eventsOfType("case.revision.auto_confirmed") {
		if event.Payload["trigger"] == design.CaseTriggerRollback {
			rollbackEvents++
			if event.Payload["rollback_of"] != platformid.Format(rev.CaseVersionID) {
				t.Fatalf("rollback event payload = %+v", event.Payload)
			}
			if event.Payload["resource_id"] != platformid.Format(d.pageID) {
				t.Fatalf("rollback event resource_id = %v, want %d", event.Payload["resource_id"], d.pageID)
			}
		}
	}
	if rollbackEvents != 1 {
		t.Fatalf("rollback events = %d, want 1", rollbackEvents)
	}
	rollbackAudit := findAudit(d.audit.events, "case.rolled_back")
	if rollbackAudit == nil ||
		rollbackAudit.Metadata["rolled_back_from"] != platformid.Format(rev.CaseVersionID) ||
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

func TestOutOfScopeTargetAndUnapprovedResourceStayDraft(t *testing.T) {
	t.Run("out_of_scope", func(t *testing.T) {
		d := newDiscoveryRevisionTest(t)
		d.repo.version.PageToSUT = map[int64]int64{}
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

func TestAutoConfirmIdempotentPerObservation(t *testing.T) {
	d := newDiscoveryRevisionTest(t)
	first, err := d.svc.OnResourceConfirmed(context.Background(), d.versionID, d.pageID, d.observationID, d.requirementID)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := d.svc.OnResourceConfirmed(context.Background(), d.versionID, d.pageID, d.observationID, d.requirementID)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.CaseVersionID != second.CaseVersionID {
		t.Fatalf("idempotency: first=%+v second=%+v", first, second)
	}
	if len(d.eventsOfType("case.revision.auto_confirmed")) != 1 {
		t.Fatalf("events = %v, want one", d.events.types())
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
	if len(d.eventsOfType("case.revision.auto_confirmed")) != 1 {
		t.Fatalf("observer did not auto-confirm (%v)", d.events.types())
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
	if len(ignored.eventsOfType("case.revision.auto_confirmed")) != 0 {
		t.Fatalf("observer reacted to an ignored event")
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
	rev, err := d.svc.OnResourceConfirmed(context.Background(), d.versionID, d.pageID, d.observationID, d.requirementID)
	if err != nil {
		t.Fatalf("OnResourceConfirmed: %v", err)
	}
	// Model a human confirming a low-confidence discovery draft: the revision
	// is confirmed and still a discovery_delta, but no system auto-confirmation
	// actor is recorded.
	for _, item := range d.repo.cases {
		for index := range item.versions {
			if item.versions[index].ID == rev.CaseVersionID {
				item.versions[index].AutoConfirmedBy = ""
			}
		}
	}

	_, err = d.svc.Rollback(context.Background(), d.versionID, rev.CaseVersionID, "why", 9)
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
	if replacement.RollbackOf != autorev.ID || replacement.Status != design.CaseStatusConfirmed {
		t.Fatalf("replacement = %+v", replacement)
	}
	stored := d.caseVersion(replacement.CaseVersionID)
	if stored.Title != base.Title {
		t.Fatalf("replacement title = %q, want prior confirmed %q", stored.Title, base.Title)
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
