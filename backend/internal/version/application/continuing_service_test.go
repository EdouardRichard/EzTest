package application_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// The continuing fake writes below extend the shared fakeVersionRepo (defined in
// version_service_test.go) with the P7 column-scoped writes, mirroring the
// PostgreSQL guards: MarkContinuing only from completed, SetContinuePhase only
// while continuing, CompleteContinuing from continuing only, Finalize from
// executing/continuing and never while paused.

// MarkContinuing mirrors the completed -> continuing CAS.
func (r *fakeVersionRepo) MarkContinuing(_ context.Context, id int64, actor int64) (*domain.Version, bool, error) {
	stored, ok := r.versions[id]
	if !ok {
		return nil, false, domain.NewNotFound("version", id)
	}
	if stored.Status != domain.VersionCompleted {
		copied := stored
		if stored.Status == domain.VersionContinuing {
			return &copied, false, nil
		}
		return nil, false, domain.NewStateConflict("version_not_continuable",
			"version status does not allow this action")
	}
	stored.Status = domain.VersionContinuing
	stored.ContinuePhase = domain.ContinuePhaseClarifying
	continuedAt := testNow
	stored.ContinuedAt = &continuedAt
	stored.Revision++
	stored.UpdatedBy = actor
	stored.UpdatedAt = r.stamp(id, stored.Revision)
	r.versions[id] = stored
	updated := stored
	return &updated, true, nil
}

// SetContinuePhase mirrors the continuing-only phase write.
func (r *fakeVersionRepo) SetContinuePhase(_ context.Context, id int64, phase string, actor int64) (*domain.Version, error) {
	stored, ok := r.versions[id]
	if !ok {
		return nil, domain.NewNotFound("version", id)
	}
	if stored.Status != domain.VersionContinuing {
		return nil, domain.NewStateConflict("version_state_conflict", "version status does not allow this action")
	}
	stored.ContinuePhase = phase
	stored.Revision++
	stored.UpdatedBy = actor
	stored.UpdatedAt = r.stamp(id, stored.Revision)
	r.versions[id] = stored
	updated := stored
	return &updated, nil
}

// RefreshContinuePhase mirrors the atomic phase derivation: the configured
// draft warnings stand in for the requirement rows (version-service tests do not
// own requirements; the requirement-service fake overrides this from its store).
func (r *fakeVersionRepo) RefreshContinuePhase(_ context.Context, id int64, actor int64) (*domain.Version, bool, error) {
	stored, ok := r.versions[id]
	if !ok {
		return nil, false, domain.NewNotFound("version", id)
	}
	if stored.Status != domain.VersionContinuing || stored.ContinuePhase == domain.ContinuePhaseExecuting {
		return nil, false, nil
	}
	target := domain.ContinuePhaseDesigning
	if len(r.warnings[id].UnclarifiedRequirementIDs) > 0 {
		target = domain.ContinuePhaseClarifying
	}
	if stored.ContinuePhase == target {
		return nil, false, nil
	}
	stored.ContinuePhase = target
	stored.Revision++
	stored.UpdatedBy = actor
	stored.UpdatedAt = r.stamp(id, stored.Revision)
	r.versions[id] = stored
	updated := stored
	return &updated, true, nil
}

// CompleteContinuing mirrors the continuing -> completed CAS and reports
// whether this call performed the transition.
func (r *fakeVersionRepo) CompleteContinuing(_ context.Context, id int64, actor int64) (*domain.Version, bool, error) {
	stored, ok := r.versions[id]
	if !ok {
		return nil, false, domain.NewNotFound("version", id)
	}
	if stored.Status == domain.VersionCompleted {
		copied := stored
		return &copied, false, nil
	}
	if stored.Status != domain.VersionContinuing {
		return nil, false, domain.NewStateConflict("version_state_conflict", "version status does not allow this action")
	}
	stored.Status = domain.VersionCompleted
	stored.ContinuePhase = ""
	stored.Revision++
	stored.UpdatedBy = actor
	stored.UpdatedAt = r.stamp(id, stored.Revision)
	r.versions[id] = stored
	updated := stored
	return &updated, true, nil
}

// Finalize mirrors the finalize CAS: executing or continuing, never paused.
func (r *fakeVersionRepo) Finalize(_ context.Context, id int64, actor int64) (*domain.Version, bool, error) {
	stored, ok := r.versions[id]
	if !ok {
		return nil, false, domain.NewNotFound("version", id)
	}
	if stored.Status == domain.VersionCompleted {
		copied := stored
		return &copied, false, nil
	}
	if (stored.Status != domain.VersionExecuting && stored.Status != domain.VersionContinuing) || stored.Paused {
		return nil, false, domain.NewStateConflict("version_state_conflict", "version status does not allow this action")
	}
	stored.Status = domain.VersionCompleted
	stored.ContinuePhase = ""
	stored.Revision++
	stored.UpdatedBy = actor
	stored.UpdatedAt = r.stamp(id, stored.Revision)
	r.versions[id] = stored
	updated := stored
	return &updated, true, nil
}

// fakeFinalizeBatches is an in-memory execution-side finalize projection.
type fakeFinalizeBatches struct {
	state application.FinalizeBatchState
	err   error
	calls int
}

// FinalizeBatchState returns the configured batch conditions.
func (f *fakeFinalizeBatches) FinalizeBatchState(_ context.Context, _ int64) (application.FinalizeBatchState, error) {
	f.calls++
	if f.err != nil {
		return application.FinalizeBatchState{}, f.err
	}
	return f.state, nil
}

// fakeVersionAudit records the version.continued/version.completed audit entries.
type fakeVersionAudit struct {
	mu     sync.Mutex
	events []application.VersionAuditEvent
	err    error
}

// Record captures one audit entry.
func (a *fakeVersionAudit) Record(_ context.Context, event application.VersionAuditEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, event)
	return a.err
}

// recorded returns a copy of the captured entries.
func (a *fakeVersionAudit) recorded() []application.VersionAuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]application.VersionAuditEvent(nil), a.events...)
}

// continuingFixture wires the real version service to the continuing fakes.
type continuingFixture struct {
	service *application.VersionService
	repo    *fakeVersionRepo
	events  *fakeEvents
	batches *fakeFinalizeBatches
	audit   *fakeVersionAudit
}

// newContinuingFixture returns a fixture with the finalize and audit ports wired.
func newContinuingFixture() *continuingFixture {
	f := &continuingFixture{
		repo:    newFakeVersionRepo(),
		events:  &fakeEvents{},
		batches: &fakeFinalizeBatches{},
		audit:   &fakeVersionAudit{},
	}
	f.service = application.NewVersionService(
		f.repo, nil, nil, f.events,
		application.WithFinalizeBatchReader(f.batches),
		application.WithVersionAuditSink(f.audit),
	)
	return f
}

// seed stores a version with the given status, phase and paused flag.
func (f *continuingFixture) seed(status, phase string, paused bool) domain.Version {
	return f.repo.seed(domain.Version{
		ID: 100, Name: "Order v2", Status: status, PauseMode: domain.PauseModeGraceful,
		ContinuePhase: phase, Paused: paused, Revision: 1, CreatedBy: 1, UpdatedBy: 1,
	})
}

// assertContinueEvent asserts one version.status_changed event of a continuing
// transition. phase is skipped when empty.
func assertContinueEvent(t *testing.T, event application.VersionEvent, from, to, phase string) {
	t.Helper()
	if event.Type != "version.status_changed" {
		t.Fatalf("event type = %q, want version.status_changed", event.Type)
	}
	if event.Detail["from"] != from || event.Detail["to"] != to {
		t.Fatalf("event from/to = %v/%v, want %s/%s", event.Detail["from"], event.Detail["to"], from, to)
	}
	if phase != "" && event.Detail["phase"] != phase {
		t.Fatalf("event phase = %v, want %s", event.Detail["phase"], phase)
	}
}

// TestMarkContinuingTransitionsCompleted covers D43/spec §5.1: the public
// MarkContinuing CAS moves a completed version to continuing/clarifying exactly
// once, publishes version.status_changed and audits version.continued.
func TestMarkContinuingTransitionsCompleted(t *testing.T) {
	t.Run("completed moves to continuing clarifying", func(t *testing.T) {
		f := newContinuingFixture()
		stored := f.seed(domain.VersionCompleted, "", false)

		version, err := f.service.MarkContinuing(context.Background(), stored.ID, 42)
		if err != nil {
			t.Fatalf("MarkContinuing: %v", err)
		}
		if version.Status != domain.VersionContinuing || version.ContinuePhase != domain.ContinuePhaseClarifying {
			t.Fatalf("status/phase = %s/%s, want continuing/clarifying", version.Status, version.ContinuePhase)
		}
		if version.ContinuedAt == nil {
			t.Fatal("continuedAt was not stamped")
		}
		if version.Revision != 2 || version.UpdatedBy != 42 {
			t.Fatalf("revision/updatedBy = %d/%d, want 2/42", version.Revision, version.UpdatedBy)
		}
		if len(f.events.events) != 1 {
			t.Fatalf("events = %d, want 1", len(f.events.events))
		}
		assertContinueEvent(t, f.events.events[0], domain.VersionCompleted, domain.VersionContinuing,
			domain.ContinuePhaseClarifying)
		audits := f.audit.recorded()
		if len(audits) != 1 || audits[0].Action != "version.continued" ||
			audits[0].VersionID != stored.ID || audits[0].ActorID != 42 ||
			audits[0].From != domain.VersionCompleted || audits[0].To != domain.VersionContinuing {
			t.Fatalf("audit = %+v", audits)
		}
	})

	t.Run("continuing is an idempotent no-op", func(t *testing.T) {
		f := newContinuingFixture()
		stored := f.seed(domain.VersionContinuing, domain.ContinuePhaseDesigning, false)

		version, err := f.service.MarkContinuing(context.Background(), stored.ID, 42)
		if err != nil {
			t.Fatalf("MarkContinuing: %v", err)
		}
		if version.Status != domain.VersionContinuing || version.ContinuePhase != domain.ContinuePhaseDesigning {
			t.Fatalf("idempotent call changed the version: %+v", version)
		}
		if len(f.events.events) != 0 || len(f.audit.recorded()) != 0 {
			t.Fatalf("idempotent call wrote events=%d audits=%d",
				len(f.events.events), len(f.audit.recorded()))
		}
	})

	t.Run("non-completed statuses are version_not_continuable", func(t *testing.T) {
		for _, status := range []string{
			domain.VersionDraft, domain.VersionClarifying, domain.VersionDesigning,
			domain.VersionReviewing, domain.VersionExecuting, domain.VersionArchived,
		} {
			t.Run(status, func(t *testing.T) {
				f := newContinuingFixture()
				stored := f.seed(status, "", false)

				_, err := f.service.MarkContinuing(context.Background(), stored.ID, 42)

				assertDomainReason(t, err, domain.KindStateConflict, "version_not_continuable")
				if got := f.repo.versions[stored.ID]; got.Status != status || got.Revision != 1 {
					t.Fatalf("blocked mark wrote the row: %+v", got)
				}
				if len(f.events.events) != 0 {
					t.Fatalf("blocked mark published events: %+v", f.events.events)
				}
			})
		}
	})

	t.Run("missing version is 404", func(t *testing.T) {
		f := newContinuingFixture()
		_, err := f.service.MarkContinuing(context.Background(), 999, 42)
		assertDomainReason(t, err, domain.KindNotFound, "not_found")
	})
}

// TestSetContinuePhase covers the phase enum and the continuing-only write.
func TestSetContinuePhase(t *testing.T) {
	t.Run("updates a continuing version and publishes the phase", func(t *testing.T) {
		f := newContinuingFixture()
		stored := f.seed(domain.VersionContinuing, domain.ContinuePhaseClarifying, false)

		if err := f.service.SetContinuePhase(context.Background(), stored.ID, domain.ContinuePhaseDesigning); err != nil {
			t.Fatalf("SetContinuePhase: %v", err)
		}
		if got := f.repo.versions[stored.ID]; got.ContinuePhase != domain.ContinuePhaseDesigning || got.Revision != 2 {
			t.Fatalf("phase/revision = %s/%d, want designing/2", got.ContinuePhase, got.Revision)
		}
		if len(f.events.events) != 1 {
			t.Fatalf("events = %d, want 1", len(f.events.events))
		}
		assertContinueEvent(t, f.events.events[0], domain.VersionContinuing, domain.VersionContinuing,
			domain.ContinuePhaseDesigning)
	})

	t.Run("invalid phase is a 422 and writes nothing", func(t *testing.T) {
		f := newContinuingFixture()
		stored := f.seed(domain.VersionContinuing, domain.ContinuePhaseClarifying, false)

		err := f.service.SetContinuePhase(context.Background(), stored.ID, "running")

		assertDomainReason(t, err, domain.KindInvalid, "invalid_continue_phase")
		if got := f.repo.versions[stored.ID]; got.ContinuePhase != domain.ContinuePhaseClarifying || got.Revision != 1 {
			t.Fatalf("invalid phase wrote the row: %+v", got)
		}
		if len(f.events.events) != 0 {
			t.Fatalf("invalid phase published events: %+v", f.events.events)
		}
	})

	t.Run("non-continuing status is a state conflict", func(t *testing.T) {
		for _, status := range []string{domain.VersionExecuting, domain.VersionCompleted, domain.VersionArchived} {
			t.Run(status, func(t *testing.T) {
				f := newContinuingFixture()
				stored := f.seed(status, "", false)

				err := f.service.SetContinuePhase(context.Background(), stored.ID, domain.ContinuePhaseExecuting)

				assertDomainReason(t, err, domain.KindStateConflict, "version_state_conflict")
				if got := f.repo.versions[stored.ID]; got.ContinuePhase != "" || got.Revision != 1 {
					t.Fatalf("blocked phase wrote the row: %+v", got)
				}
			})
		}
	})

	t.Run("unchanged phase is a silent no-op", func(t *testing.T) {
		f := newContinuingFixture()
		stored := f.seed(domain.VersionContinuing, domain.ContinuePhaseExecuting, false)

		if err := f.service.SetContinuePhase(context.Background(), stored.ID, domain.ContinuePhaseExecuting); err != nil {
			t.Fatalf("SetContinuePhase: %v", err)
		}
		if got := f.repo.versions[stored.ID]; got.Revision != 1 {
			t.Fatalf("unchanged phase bumped the revision: %+v", got)
		}
		if len(f.events.events) != 0 {
			t.Fatalf("unchanged phase published events: %+v", f.events.events)
		}
	})

	t.Run("clearing the phase is allowed", func(t *testing.T) {
		f := newContinuingFixture()
		stored := f.seed(domain.VersionContinuing, domain.ContinuePhaseExecuting, false)

		if err := f.service.SetContinuePhase(context.Background(), stored.ID, ""); err != nil {
			t.Fatalf("SetContinuePhase clear: %v", err)
		}
		if got := f.repo.versions[stored.ID]; got.ContinuePhase != "" {
			t.Fatalf("phase = %q, want cleared", got.ContinuePhase)
		}
	})
}

// TestTryFinalizeMatrix covers the spec §5.1 finalize conditions: no active
// batch, every terminal batch settled, at least one batch, and for continuing
// also at least one incremental batch and no draft requirement.
func TestTryFinalizeMatrix(t *testing.T) {
	t.Run("executing with settled batches finalizes", func(t *testing.T) {
		f := newContinuingFixture()
		stored := f.seed(domain.VersionExecuting, "", false)
		f.batches.state = application.FinalizeBatchState{TotalBatches: 2}

		finalized, err := f.service.TryFinalize(context.Background(), stored.ID)
		if err != nil {
			t.Fatalf("TryFinalize: %v", err)
		}
		if !finalized {
			t.Fatal("finalize reported false for a finalizable version")
		}
		got := f.repo.versions[stored.ID]
		if got.Status != domain.VersionCompleted || got.ContinuePhase != "" || got.Revision != 2 {
			t.Fatalf("finalized row = %+v", got)
		}
		if len(f.events.events) != 1 {
			t.Fatalf("events = %d, want 1", len(f.events.events))
		}
		assertContinueEvent(t, f.events.events[0], domain.VersionExecuting, domain.VersionCompleted, "")
		audits := f.audit.recorded()
		if len(audits) != 1 || audits[0].Action != "version.completed" {
			t.Fatalf("audit = %+v", audits)
		}
	})

	t.Run("continuing with an incremental batch and no drafts finalizes", func(t *testing.T) {
		f := newContinuingFixture()
		stored := f.seed(domain.VersionContinuing, domain.ContinuePhaseExecuting, false)
		f.batches.state = application.FinalizeBatchState{TotalBatches: 2, IncrementalBatches: 1}
		f.repo.warnings[stored.ID] = domain.Warnings{}

		finalized, err := f.service.TryFinalize(context.Background(), stored.ID)
		if err != nil {
			t.Fatalf("TryFinalize: %v", err)
		}
		if !finalized {
			t.Fatal("finalize reported false for a finalizable continuing version")
		}
		if got := f.repo.versions[stored.ID]; got.Status != domain.VersionCompleted || got.ContinuePhase != "" {
			t.Fatalf("finalized row = %+v", got)
		}
	})

	for _, blocked := range []struct {
		name  string
		state application.FinalizeBatchState
	}{
		{"an active batch blocks", application.FinalizeBatchState{TotalBatches: 2, ActiveBatches: 1, IncrementalBatches: 1}},
		{"an unsettled terminal batch blocks", application.FinalizeBatchState{TotalBatches: 2, UnsettledBatches: 1, IncrementalBatches: 1}},
		{"zero batches block", application.FinalizeBatchState{IncrementalBatches: 1}},
	} {
		t.Run(blocked.name, func(t *testing.T) {
			f := newContinuingFixture()
			stored := f.seed(domain.VersionContinuing, domain.ContinuePhaseExecuting, false)
			f.batches.state = blocked.state
			f.repo.warnings[stored.ID] = domain.Warnings{}

			finalized, err := f.service.TryFinalize(context.Background(), stored.ID)
			if err != nil {
				t.Fatalf("TryFinalize: %v", err)
			}
			if finalized {
				t.Fatal("finalize reported true while a condition is unmet")
			}
			if got := f.repo.versions[stored.ID]; got.Status != domain.VersionContinuing || got.Revision != 1 {
				t.Fatalf("blocked finalize wrote the row: %+v", got)
			}
			if len(f.events.events) != 0 {
				t.Fatalf("blocked finalize published events: %+v", f.events.events)
			}
		})
	}

	t.Run("a draft requirement blocks the continuing finalize", func(t *testing.T) {
		f := newContinuingFixture()
		stored := f.seed(domain.VersionContinuing, domain.ContinuePhaseClarifying, false)
		f.batches.state = application.FinalizeBatchState{TotalBatches: 1, IncrementalBatches: 1}
		f.repo.warnings[stored.ID] = domain.Warnings{UnclarifiedRequirementIDs: []int64{5}}

		finalized, err := f.service.TryFinalize(context.Background(), stored.ID)
		if err != nil {
			t.Fatalf("TryFinalize: %v", err)
		}
		if finalized {
			t.Fatal("finalize completed a version with a draft requirement")
		}
		if got := f.repo.versions[stored.ID]; got.Status != domain.VersionContinuing {
			t.Fatalf("blocked finalize wrote the row: %+v", got)
		}
	})

	t.Run("continuing without an incremental batch stays continuing", func(t *testing.T) {
		f := newContinuingFixture()
		stored := f.seed(domain.VersionContinuing, domain.ContinuePhaseDesigning, false)
		f.batches.state = application.FinalizeBatchState{TotalBatches: 2, IncrementalBatches: 0}

		finalized, err := f.service.TryFinalize(context.Background(), stored.ID)
		if err != nil {
			t.Fatalf("TryFinalize: %v", err)
		}
		if finalized {
			t.Fatal("finalize completed a continuing version without an incremental batch")
		}
		if got := f.repo.versions[stored.ID]; got.Status != domain.VersionContinuing {
			t.Fatalf("blocked finalize wrote the row: %+v", got)
		}
	})

	t.Run("a paused version never finalizes", func(t *testing.T) {
		f := newContinuingFixture()
		stored := f.seed(domain.VersionExecuting, "", true)
		f.batches.state = application.FinalizeBatchState{TotalBatches: 2}

		finalized, err := f.service.TryFinalize(context.Background(), stored.ID)
		if err != nil {
			t.Fatalf("TryFinalize: %v", err)
		}
		if finalized {
			t.Fatal("finalize completed a paused version")
		}
		if got := f.repo.versions[stored.ID]; got.Status != domain.VersionExecuting {
			t.Fatalf("paused finalize wrote the row: %+v", got)
		}
		if f.batches.calls != 0 {
			t.Fatalf("paused finalize queried the batch reader %d times", f.batches.calls)
		}
	})

	t.Run("a completed version is an idempotent no-op", func(t *testing.T) {
		f := newContinuingFixture()
		stored := f.seed(domain.VersionCompleted, "", false)

		finalized, err := f.service.TryFinalize(context.Background(), stored.ID)
		if err != nil {
			t.Fatalf("TryFinalize: %v", err)
		}
		if finalized {
			t.Fatal("finalize reported true for an already completed version")
		}
		if got := f.repo.versions[stored.ID]; got.Revision != 1 {
			t.Fatalf("idempotent finalize wrote the row: %+v", got)
		}
		if len(f.events.events) != 0 || len(f.audit.recorded()) != 0 {
			t.Fatalf("idempotent finalize wrote events=%d audits=%d",
				len(f.events.events), len(f.audit.recorded()))
		}
	})

	t.Run("a lost CAS is reported as not finalized", func(t *testing.T) {
		f := newContinuingFixture()
		stored := f.seed(domain.VersionExecuting, "", false)
		f.batches.state = application.FinalizeBatchState{TotalBatches: 2}

		finalized, err := f.service.TryFinalize(context.Background(), stored.ID)
		if err != nil || !finalized {
			t.Fatalf("first finalize = %v/%v, want true/nil", finalized, err)
		}
		finalized, err = f.service.TryFinalize(context.Background(), stored.ID)
		if err != nil || finalized {
			t.Fatalf("second finalize = %v/%v, want false/nil", finalized, err)
		}
		if len(f.events.events) != 1 {
			t.Fatalf("events = %d, want exactly one", len(f.events.events))
		}
	})

	t.Run("a missing batch reader fails closed", func(t *testing.T) {
		repo := newFakeVersionRepo()
		events := &fakeEvents{}
		service := application.NewVersionService(repo, nil, nil, events)
		stored := repo.seed(domain.Version{
			ID: 100, Name: "Order v2", Status: domain.VersionExecuting,
			PauseMode: domain.PauseModeGraceful, Revision: 1, CreatedBy: 1, UpdatedBy: 1,
		})

		if _, err := service.TryFinalize(context.Background(), stored.ID); err == nil {
			t.Fatal("TryFinalize succeeded without a batch reader")
		}
		if got := repo.versions[stored.ID]; got.Status != domain.VersionExecuting {
			t.Fatalf("failed finalize wrote the row: %+v", got)
		}
	})
}

// TestCompleteContinuing covers the explicit continuing -> completed action.
func TestCompleteContinuing(t *testing.T) {
	t.Run("continuing completes and clears the phase", func(t *testing.T) {
		f := newContinuingFixture()
		stored := f.seed(domain.VersionContinuing, domain.ContinuePhaseExecuting, false)

		version, err := f.service.CompleteContinuing(context.Background(), stored.ID, 42)
		if err != nil {
			t.Fatalf("CompleteContinuing: %v", err)
		}
		if version.Status != domain.VersionCompleted || version.ContinuePhase != "" || version.Revision != 2 {
			t.Fatalf("completed version = %+v", version)
		}
		if len(f.events.events) != 1 {
			t.Fatalf("events = %d, want 1", len(f.events.events))
		}
		assertContinueEvent(t, f.events.events[0], domain.VersionContinuing, domain.VersionCompleted, "")
		audits := f.audit.recorded()
		if len(audits) != 1 || audits[0].Action != "version.completed" {
			t.Fatalf("audit = %+v", audits)
		}
	})

	t.Run("executing is a state conflict", func(t *testing.T) {
		f := newContinuingFixture()
		stored := f.seed(domain.VersionExecuting, "", false)

		_, err := f.service.CompleteContinuing(context.Background(), stored.ID, 42)

		assertDomainReason(t, err, domain.KindStateConflict, "version_state_conflict")
		if got := f.repo.versions[stored.ID]; got.Status != domain.VersionExecuting {
			t.Fatalf("blocked completion wrote the row: %+v", got)
		}
	})

	t.Run("already completed is an idempotent no-op", func(t *testing.T) {
		f := newContinuingFixture()
		stored := f.seed(domain.VersionCompleted, "", false)

		version, err := f.service.CompleteContinuing(context.Background(), stored.ID, 42)
		if err != nil {
			t.Fatalf("CompleteContinuing: %v", err)
		}
		if version.Status != domain.VersionCompleted {
			t.Fatalf("status = %s, want completed", version.Status)
		}
		if len(f.events.events) != 0 || len(f.audit.recorded()) != 0 {
			t.Fatalf("idempotent completion wrote events=%d audits=%d",
				len(f.events.events), len(f.audit.recorded()))
		}
	})
}

// changeFeedRepo is a fakeVersionRepo with a configurable change feed.
type changeFeedRepo struct {
	*fakeVersionRepo
	changes []domain.VersionChange
	err     error
	calls   int
}

// Changes returns the configured feed.
func (r *changeFeedRepo) Changes(_ context.Context, _ int64) ([]domain.VersionChange, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	return append([]domain.VersionChange(nil), r.changes...), nil
}

// TestVersionChangesSortsAndPaginates covers the read-time derivation of
// GET /versions/{id}/changes (spec §7.2/D50): newest first, stable tie-break and
// an opaque offset cursor.
func TestVersionChangesSortsAndPaginates(t *testing.T) {
	base := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	repo := &changeFeedRepo{
		fakeVersionRepo: newFakeVersionRepo(),
		changes: []domain.VersionChange{
			{Kind: domain.VersionChangeRequirementAdded, At: base.Add(3 * time.Minute), RefID: "10", Summary: "req"},
			{Kind: domain.VersionChangeBatchFinished, At: base.Add(time.Minute), RefID: "5", Summary: "batch"},
			{Kind: domain.VersionChangeReportPublished, At: base.Add(3 * time.Minute), RefID: "11", Summary: "report"},
			{Kind: domain.VersionChangeIssueCreated, At: base.Add(2 * time.Minute), RefID: "7", Summary: "issue"},
		},
	}
	repo.seed(domain.Version{ID: 100, Name: "Order v2", Status: domain.VersionCompleted})
	service := application.NewVersionService(repo, nil, nil, &fakeEvents{})

	first, err := service.Changes(context.Background(), 100, domain.Page{Limit: 2})
	if err != nil {
		t.Fatalf("Changes: %v", err)
	}
	if len(first.Items) != 2 || !first.HasMore {
		t.Fatalf("first page = %+v", first)
	}
	if first.Items[0].RefID != "11" || first.Items[0].Kind != domain.VersionChangeReportPublished {
		t.Fatalf("first item = %+v, want the same-instant report first (kind asc)", first.Items[0])
	}
	if first.Items[1].RefID != "10" {
		t.Fatalf("second item = %+v, want requirement 10", first.Items[1])
	}
	if offset, err := platform.DecodeCursor(first.NextCursor); err != nil || offset != 2 {
		t.Fatalf("next cursor decodes to %d/%v, want 2", offset, err)
	}

	second, err := service.Changes(context.Background(), 100, domain.Page{Limit: 2, Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("Changes page 2: %v", err)
	}
	if len(second.Items) != 2 || second.HasMore || second.NextCursor != "" {
		t.Fatalf("second page = %+v", second)
	}
	if second.Items[0].RefID != "7" || second.Items[1].RefID != "5" {
		t.Fatalf("second page order = %+v", second.Items)
	}

	third, err := service.Changes(context.Background(), 100, domain.Page{Limit: 2, Cursor: platform.EncodeCursor(4)})
	if err != nil {
		t.Fatalf("Changes past the end: %v", err)
	}
	if len(third.Items) != 0 || third.HasMore || third.Items == nil {
		t.Fatalf("past the end page = %+v", third)
	}

	if _, err := service.Changes(context.Background(), 999, domain.Page{Limit: 20}); err == nil {
		t.Fatal("Changes returned a missing version")
	}
}
