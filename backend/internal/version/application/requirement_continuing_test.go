package application_test

import (
	"context"
	"sync"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// continuingVersionRepo is a fakeVersionRepo that also emulates the
// transactional requirement-create port: the completed -> continuing CAS and the
// requirement insert happen under one lock, so concurrent creates observe at
// most one transition (spec §5.1 rule 2).
type continuingVersionRepo struct {
	*fakeVersionRepo
	requirements *fakeRequirementRepo
	mu           sync.Mutex
	transitions  int
	// readBarrier, when set, releases and waits on the initial Get so a
	// concurrent test can prove both callers observed the pre-transition row.
	readBarrier *sync.WaitGroup
}

// Get returns one stored version, optionally waiting on the read barrier.
func (r *continuingVersionRepo) Get(ctx context.Context, id int64) (*domain.Version, error) {
	version, err := r.fakeVersionRepo.Get(ctx, id)
	if r.readBarrier != nil {
		r.readBarrier.Done()
		r.readBarrier.Wait()
	}
	return version, err
}

// RefreshContinuePhase derives the phase from the requirement store inside one
// locked write, mirroring the atomic PostgreSQL UPDATE of spec §5.1.
func (r *continuingVersionRepo) RefreshContinuePhase(_ context.Context, versionID int64, actor int64) (*domain.Version, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.versions[versionID]
	if !ok {
		return nil, false, domain.NewNotFound("version", versionID)
	}
	if stored.Status != domain.VersionContinuing || stored.ContinuePhase == domain.ContinuePhaseExecuting {
		return nil, false, nil
	}
	target := domain.ContinuePhaseDesigning
	for _, requirement := range r.requirements.requirements {
		if requirement.VersionID == versionID && requirement.Status == domain.RequirementDraft {
			target = domain.ContinuePhaseClarifying
			break
		}
	}
	if stored.ContinuePhase == target {
		return nil, false, nil
	}
	stored.ContinuePhase = target
	stored.Revision++
	stored.UpdatedBy = actor
	stored.UpdatedAt = r.stamp(stored.ID, stored.Revision)
	r.versions[versionID] = stored
	updated := stored
	return &updated, true, nil
}

// CreateRequirementContinuing emulates the PostgreSQL same-transaction write.
func (r *continuingVersionRepo) CreateRequirementContinuing(_ context.Context, requirement *domain.Requirement, actor int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.versions[requirement.VersionID]
	if !ok {
		return false, domain.NewNotFound("version", requirement.VersionID)
	}
	transitioned := false
	switch stored.Status {
	case domain.VersionCompleted:
		stored.Status = domain.VersionContinuing
		stored.ContinuePhase = domain.ContinuePhaseClarifying
		continuedAt := testNow
		stored.ContinuedAt = &continuedAt
		stored.Revision++
		stored.UpdatedBy = actor
		stored.UpdatedAt = r.stamp(stored.ID, stored.Revision)
		r.versions[stored.ID] = stored
		transitioned = true
		r.transitions++
	case domain.VersionContinuing:
		// A concurrent create already performed the transition.
	default:
		return false, domain.NewStateConflict("version_not_continuable",
			"version status does not allow continuing")
	}
	if err := r.requirements.Create(context.Background(), requirement); err != nil {
		return false, err
	}
	return transitioned, nil
}

// requirementContinuingFixture wires the requirement service to the combined
// fake and the continuing audit port.
type requirementContinuingFixture struct {
	service      *application.RequirementService
	requirements *fakeRequirementRepo
	versions     *continuingVersionRepo
	events       *fakeEvents
	audit        *fakeVersionAudit
	ids          *fixedIDs
}

// newRequirementContinuingFixture returns a fixture whose first id is 5000.
func newRequirementContinuingFixture() *requirementContinuingFixture {
	f := &requirementContinuingFixture{
		requirements: newFakeRequirementRepo(),
		versions: &continuingVersionRepo{
			fakeVersionRepo: newFakeVersionRepo(),
			requirements:    nil,
		},
		events: &fakeEvents{},
		audit:  &fakeVersionAudit{},
		ids:    &fixedIDs{next: 5000},
	}
	f.versions.requirements = f.requirements
	f.service = application.NewRequirementService(
		f.requirements, f.versions, f.events,
		application.WithRequirementIDGenerator(f.ids),
		application.WithRequirementAuditSink(f.audit),
	)
	return f
}

// seedVersion stores a version with the given status, phase and revision.
func (f *requirementContinuingFixture) seedVersion(status, phase string) domain.Version {
	return f.versions.seed(domain.Version{
		ID: 100, Name: "Order v2", Status: status, ContinuePhase: phase,
		PauseMode: domain.PauseModeGraceful, Revision: 1, CreatedBy: 1, UpdatedBy: 1,
	})
}

// seedRequirement stores a requirement of the version in the given status.
func (f *requirementContinuingFixture) seedRequirement(versionID int64, status string) domain.Requirement {
	requirement := domain.Requirement{
		ID: f.ids.Next(), VersionID: versionID, Title: "Checkout flow",
		Priority: domain.PriorityShould, Status: status,
		Source: domain.SourceUser, LastUpdateSource: domain.SourceUser,
		Revision: 1, CreatedBy: 7, UpdatedBy: 7, CreatedAt: testNow, UpdatedAt: testNow,
	}
	f.requirements.requirements[requirement.ID] = requirement
	return requirement
}

// create calls the requirement create use case with the fixture ids.
func (f *requirementContinuingFixture) create(versionID int64) (*application.Requirement, error) {
	return f.service.Create(context.Background(), versionID,
		application.CreateRequirementCommand{Title: "Coupons"}, 42)
}

// TestRequirementCreateTriggersContinuing covers D43/spec §6.1 step 1: a
// successful requirement create on a completed version moves the version to
// continuing/clarifying inside the same write, publishes the status change and
// audits version.continued.
func TestRequirementCreateTriggersContinuing(t *testing.T) {
	t.Run("completed transitions to continuing clarifying", func(t *testing.T) {
		f := newRequirementContinuingFixture()
		version := f.seedVersion(domain.VersionCompleted, "")

		created, err := f.create(version.ID)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if created.VersionID != version.ID || created.Status != domain.RequirementDraft {
			t.Fatalf("created requirement = %+v", created)
		}
		stored := f.versions.versions[version.ID]
		if stored.Status != domain.VersionContinuing || stored.ContinuePhase != domain.ContinuePhaseClarifying {
			t.Fatalf("version status/phase = %s/%s, want continuing/clarifying", stored.Status, stored.ContinuePhase)
		}
		if stored.ContinuedAt == nil || stored.Revision != 2 {
			t.Fatalf("continuedAt/revision = %v/%d, want stamped/2", stored.ContinuedAt, stored.Revision)
		}
		if f.versions.transitions != 1 {
			t.Fatalf("transitions = %d, want 1", f.versions.transitions)
		}
		if len(f.events.events) != 2 {
			t.Fatalf("events = %d, want 2 (%+v)", len(f.events.events), f.events.events)
		}
		if f.events.events[0].Type != "requirement.added" {
			t.Fatalf("first event = %q, want requirement.added", f.events.events[0].Type)
		}
		assertContinueEvent(t, f.events.events[1], domain.VersionCompleted, domain.VersionContinuing,
			domain.ContinuePhaseClarifying)
		if f.events.events[1].Detail["reason"] != "new_requirement" {
			t.Fatalf("event reason = %v, want new_requirement", f.events.events[1].Detail["reason"])
		}
		audits := f.audit.recorded()
		if len(audits) != 2 ||
			audits[0].Action != "version.requirement_created" ||
			audits[1].Action != "version.continued" ||
			audits[1].ActorID != 42 {
			t.Fatalf("audit = %+v", audits)
		}
	})

	t.Run("archived is version_not_continuable", func(t *testing.T) {
		f := newRequirementContinuingFixture()
		version := f.seedVersion(domain.VersionArchived, "")

		_, err := f.create(version.ID)

		assertDomainReason(t, err, domain.KindStateConflict, "version_not_continuable")
		if len(f.requirements.requirements) != 0 {
			t.Fatalf("blocked create stored a requirement: %+v", f.requirements.requirements)
		}
		if len(f.events.events) != 0 {
			t.Fatalf("blocked create published events: %+v", f.events.events)
		}
	})

	t.Run("concurrent creates transition only once", func(t *testing.T) {
		f := newRequirementContinuingFixture()
		version := f.seedVersion(domain.VersionCompleted, "")
		f.versions.readBarrier = &sync.WaitGroup{}
		f.versions.readBarrier.Add(2)
		publisher := &syncEvents{events: f.events}
		service := application.NewRequirementService(
			f.requirements, f.versions, publisher,
			application.WithRequirementIDGenerator(f.ids),
			application.WithRequirementAuditSink(f.audit),
		)

		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				_, errs[index] = service.Create(context.Background(), version.ID,
					application.CreateRequirementCommand{Title: "Coupons"}, 42)
			}(i)
		}
		wg.Wait()

		for i, err := range errs {
			if err != nil {
				t.Fatalf("concurrent create %d: %v", i, err)
			}
		}
		if f.versions.transitions != 1 {
			t.Fatalf("transitions = %d, want exactly 1", f.versions.transitions)
		}
		if got := f.versions.versions[version.ID]; got.Status != domain.VersionContinuing ||
			got.ContinuePhase != domain.ContinuePhaseClarifying || got.Revision != 2 {
			t.Fatalf("version after concurrent creates = %+v", got)
		}
		if len(f.requirements.requirements) != 2 {
			t.Fatalf("requirements = %d, want 2", len(f.requirements.requirements))
		}
		statusChanges := 0
		for _, event := range publisher.events.events {
			if event.Type == "version.status_changed" {
				statusChanges++
			}
		}
		if statusChanges != 1 {
			t.Fatalf("version.status_changed events = %d, want 1 (%+v)", statusChanges, publisher.events.events)
		}
	})
}

// TestRequirementCreateKeepsContinuePhase covers spec §6.1 step 9: a new
// requirement resets a non-executing phase to clarifying but never interrupts a
// running incremental batch (phase=executing).
func TestRequirementCreateKeepsContinuePhase(t *testing.T) {
	t.Run("designing resets to clarifying", func(t *testing.T) {
		f := newRequirementContinuingFixture()
		version := f.seedVersion(domain.VersionContinuing, domain.ContinuePhaseDesigning)

		if _, err := f.create(version.ID); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if got := f.versions.versions[version.ID]; got.ContinuePhase != domain.ContinuePhaseClarifying {
			t.Fatalf("phase = %q, want clarifying", got.ContinuePhase)
		}
		last := f.events.events[len(f.events.events)-1]
		assertContinueEvent(t, last, domain.VersionContinuing, domain.VersionContinuing,
			domain.ContinuePhaseClarifying)
		if last.Detail["reason"] != "new_requirement" {
			t.Fatalf("phase event reason = %v", last.Detail["reason"])
		}
	})

	t.Run("executing is kept", func(t *testing.T) {
		f := newRequirementContinuingFixture()
		version := f.seedVersion(domain.VersionContinuing, domain.ContinuePhaseExecuting)

		if _, err := f.create(version.ID); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if got := f.versions.versions[version.ID]; got.ContinuePhase != domain.ContinuePhaseExecuting {
			t.Fatalf("phase = %q, want executing", got.ContinuePhase)
		}
		if len(f.events.events) != 1 || f.events.events[0].Type != "requirement.added" {
			t.Fatalf("events = %+v, want only requirement.added", f.events.events)
		}
	})

	t.Run("executing set after the pre-read is kept", func(t *testing.T) {
		f := newRequirementContinuingFixture()
		version := f.seedVersion(domain.VersionContinuing, domain.ContinuePhaseDesigning)
		// A concurrent incremental batch create commits phase=executing after the
		// service's pre-insert read but before its phase refresh.
		f.events.onPublish = func(event application.VersionEvent) {
			if event.Type != "requirement.added" {
				return
			}
			stored := f.versions.versions[version.ID]
			stored.ContinuePhase = domain.ContinuePhaseExecuting
			f.versions.versions[version.ID] = stored
		}

		if _, err := f.create(version.ID); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if got := f.versions.versions[version.ID]; got.ContinuePhase != domain.ContinuePhaseExecuting {
			t.Fatalf("phase = %q, want the concurrent executing phase preserved", got.ContinuePhase)
		}
		if len(f.events.events) != 1 || f.events.events[0].Type != "requirement.added" {
			t.Fatalf("events = %+v, want only requirement.added", f.events.events)
		}
	})

	t.Run("clarifying stays without an event", func(t *testing.T) {
		f := newRequirementContinuingFixture()
		version := f.seedVersion(domain.VersionContinuing, domain.ContinuePhaseClarifying)

		if _, err := f.create(version.ID); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if len(f.events.events) != 1 {
			t.Fatalf("events = %+v, want only requirement.added", f.events.events)
		}
	})
}

// TestRequirementConfirmRefreshesContinuePhase covers spec §5.1: confirming
// requirements of a continuing version moves the phase to designing once no
// draft remains, and back to clarifying while drafts remain.
func TestRequirementConfirmRefreshesContinuePhase(t *testing.T) {
	t.Run("confirming the last draft moves to designing", func(t *testing.T) {
		f := newRequirementContinuingFixture()
		version := f.seedVersion(domain.VersionContinuing, domain.ContinuePhaseClarifying)
		requirement := f.seedRequirement(version.ID, domain.RequirementDraft)

		if _, err := f.service.Confirm(context.Background(), version.ID, requirement.ID, 42); err != nil {
			t.Fatalf("Confirm: %v", err)
		}
		if got := f.versions.versions[version.ID]; got.ContinuePhase != domain.ContinuePhaseDesigning {
			t.Fatalf("phase = %q, want designing", got.ContinuePhase)
		}
		last := f.events.events[len(f.events.events)-1]
		assertContinueEvent(t, last, domain.VersionContinuing, domain.VersionContinuing,
			domain.ContinuePhaseDesigning)
	})

	t.Run("drafts remain and the phase returns to clarifying", func(t *testing.T) {
		f := newRequirementContinuingFixture()
		version := f.seedVersion(domain.VersionContinuing, domain.ContinuePhaseDesigning)
		first := f.seedRequirement(version.ID, domain.RequirementDraft)
		f.seedRequirement(version.ID, domain.RequirementDraft)

		if _, err := f.service.Confirm(context.Background(), version.ID, first.ID, 42); err != nil {
			t.Fatalf("Confirm: %v", err)
		}
		if got := f.versions.versions[version.ID]; got.ContinuePhase != domain.ContinuePhaseClarifying {
			t.Fatalf("phase = %q, want clarifying", got.ContinuePhase)
		}
		last := f.events.events[len(f.events.events)-1]
		assertContinueEvent(t, last, domain.VersionContinuing, domain.VersionContinuing,
			domain.ContinuePhaseClarifying)
	})

	t.Run("an executing phase is never interrupted by a confirmation", func(t *testing.T) {
		f := newRequirementContinuingFixture()
		version := f.seedVersion(domain.VersionContinuing, domain.ContinuePhaseExecuting)
		requirement := f.seedRequirement(version.ID, domain.RequirementDraft)

		if _, err := f.service.Confirm(context.Background(), version.ID, requirement.ID, 42); err != nil {
			t.Fatalf("Confirm: %v", err)
		}
		if got := f.versions.versions[version.ID]; got.ContinuePhase != domain.ContinuePhaseExecuting {
			t.Fatalf("phase = %q, want executing", got.ContinuePhase)
		}
		if len(f.events.events) != 1 || f.events.events[0].Type != "requirement.clarified" {
			t.Fatalf("events = %+v, want only requirement.clarified", f.events.events)
		}
	})

	t.Run("non-continuing versions never change phase", func(t *testing.T) {
		f := newRequirementContinuingFixture()
		version := f.seedVersion(domain.VersionDesigning, "")
		requirement := f.seedRequirement(version.ID, domain.RequirementDraft)

		if _, err := f.service.Confirm(context.Background(), version.ID, requirement.ID, 42); err != nil {
			t.Fatalf("Confirm: %v", err)
		}
		if got := f.versions.versions[version.ID]; got.Status != domain.VersionDesigning || got.Revision != 1 {
			t.Fatalf("confirm changed a non-continuing version: %+v", got)
		}
		if len(f.events.events) != 1 {
			t.Fatalf("events = %+v, want only requirement.clarified", f.events.events)
		}
	})
}

// syncEvents serialises fakeEvents.Publish for the concurrent create test.
type syncEvents struct {
	mu     sync.Mutex
	events *fakeEvents
}

// Publish records the event under a mutex.
func (e *syncEvents) Publish(ctx context.Context, event application.VersionEvent) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.events.Publish(ctx, event)
}
