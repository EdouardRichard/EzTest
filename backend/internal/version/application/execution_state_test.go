package application_test

import (
	"context"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// UpdateExecutionState mirrors the PostgreSQL repository's execution-state
// write: paused/pause_mode/batch_no are replaced without touching status, and
// only an executing or continuing version accepts the write (the same status
// guard as the SQL). The revision is bumped like every other version write.
func (r *fakeVersionRepo) UpdateExecutionState(_ context.Context, id int64, paused bool, pauseMode string, batchNo int, actor int64) (*domain.Version, error) {
	stored, ok := r.versions[id]
	if !ok || (stored.Status != domain.VersionExecuting && stored.Status != domain.VersionContinuing) {
		return nil, domain.NewStateConflict("version_state_conflict", "version status does not allow this action")
	}
	stored.Paused = paused
	stored.PauseMode = pauseMode
	stored.BatchNo = batchNo
	stored.Revision++
	stored.UpdatedBy = actor
	stored.UpdatedAt = r.stamp(id, stored.Revision)
	r.versions[id] = stored
	return &stored, nil
}

// seedExecuting stores an executing version with the P6 execution defaults.
func seedExecuting(f *versionFixture) domain.Version {
	return f.repo.seed(domain.Version{
		ID: 100, Name: "Order v2", Status: domain.VersionExecuting,
		PauseMode: domain.PauseModeGraceful, CreatedBy: 1, UpdatedBy: 1,
	})
}

// assertPauseEvent asserts the version.status_changed event MarkPaused /
// CompleteExecution publishes for an execution-state action.
func assertPauseEvent(t *testing.T, event application.VersionEvent, versionID int64, from, to string, paused bool) {
	t.Helper()
	if event.Type != "version.status_changed" || event.VersionID != versionID ||
		event.ActorID != 42 || event.Source != domain.SourceUser {
		t.Fatalf("event = %+v", event)
	}
	if event.Detail["from"] != from || event.Detail["to"] != to || event.Detail["paused"] != paused {
		t.Fatalf("event detail = %+v, want from=%s to=%s paused=%v", event.Detail, from, to, paused)
	}
}

func TestMarkPausedRequiresExecutingAndMirrorsState(t *testing.T) {
	f := newVersionFixture()
	stored := seedExecuting(f)

	paused, err := f.service.MarkPaused(context.Background(), stored.ID, true, domain.PauseModeGraceful, "user", 42)
	if err != nil {
		t.Fatalf("MarkPaused: %v", err)
	}
	if !paused.Paused || paused.PauseMode != domain.PauseModeGraceful {
		t.Fatalf("paused/mode = %v/%s, want true/graceful", paused.Paused, paused.PauseMode)
	}
	if paused.Status != domain.VersionExecuting {
		t.Fatalf("pause changed status to %s, want executing", paused.Status)
	}
	if paused.Revision != 2 || paused.UpdatedBy != 42 {
		t.Fatalf("revision/updatedBy = %d/%d, want 2/42", paused.Revision, paused.UpdatedBy)
	}
	if len(f.events.events) != 1 {
		t.Fatalf("events = %d, want 1 (%+v)", len(f.events.events), f.events.events)
	}
	assertPauseEvent(t, f.events.events[0], stored.ID, domain.VersionExecuting, domain.VersionExecuting, true)

	completed, err := f.service.CompleteExecution(context.Background(), stored.ID, 42)
	if err != nil {
		t.Fatalf("CompleteExecution: %v", err)
	}
	if completed.Status != domain.VersionCompleted || completed.Revision != 3 {
		t.Fatalf("status/revision = %s/%d, want completed/3", completed.Status, completed.Revision)
	}
	if got, err := f.repo.Get(context.Background(), stored.ID); err != nil || got.Status != domain.VersionCompleted {
		t.Fatalf("stored version = %+v err=%v, want completed", got, err)
	}
	if len(f.events.events) != 2 {
		t.Fatalf("events = %d, want 2 (%+v)", len(f.events.events), f.events.events)
	}
	assertPauseEvent(t, f.events.events[1], stored.ID, domain.VersionExecuting, domain.VersionCompleted, true)
}

func TestMarkPausedAllowsContinuingAndResume(t *testing.T) {
	f := newVersionFixture()
	stored := f.repo.seed(domain.Version{
		ID: 100, Name: "Order v2", Status: domain.VersionContinuing,
		PauseMode: domain.PauseModeGraceful, CreatedBy: 1, UpdatedBy: 1,
	})

	paused, err := f.service.MarkPaused(context.Background(), stored.ID, true, domain.PauseModeImmediate, "restart", 42)
	if err != nil {
		t.Fatalf("MarkPaused continuing: %v", err)
	}
	if !paused.Paused || paused.PauseMode != domain.PauseModeImmediate || paused.Status != domain.VersionContinuing {
		t.Fatalf("paused/mode/status = %v/%s/%s", paused.Paused, paused.PauseMode, paused.Status)
	}

	resumed, err := f.service.MarkPaused(context.Background(), stored.ID, false, "", "", 42)
	if err != nil {
		t.Fatalf("MarkPaused resume: %v", err)
	}
	if resumed.Paused || resumed.Status != domain.VersionContinuing {
		t.Fatalf("resumed paused/status = %v/%s, want false/continuing", resumed.Paused, resumed.Status)
	}
	if resumed.PauseMode != domain.PauseModeImmediate {
		t.Fatalf("resume changed pause mode to %q, want the stored immediate", resumed.PauseMode)
	}
	if len(f.events.events) != 2 {
		t.Fatalf("events = %d, want 2", len(f.events.events))
	}
	assertPauseEvent(t, f.events.events[1], stored.ID, domain.VersionContinuing, domain.VersionContinuing, false)
}

func TestMarkPausedRejectsNonExecutableState(t *testing.T) {
	for _, status := range []string{
		domain.VersionDraft, domain.VersionClarifying, domain.VersionDesigning,
		domain.VersionReviewing, domain.VersionCompleted, domain.VersionArchived,
	} {
		t.Run(status, func(t *testing.T) {
			f := newVersionFixture()
			stored := f.repo.seed(domain.Version{
				ID: 100, Name: "Order v2", Status: status,
				PauseMode: domain.PauseModeGraceful, CreatedBy: 1, UpdatedBy: 1,
			})

			_, err := f.service.MarkPaused(context.Background(), stored.ID, true, domain.PauseModeGraceful, "user", 42)

			assertDomainReason(t, err, domain.KindStateConflict, "version_state_conflict")
			if got := f.repo.versions[stored.ID]; got.Paused || got.Revision != 1 {
				t.Fatalf("blocked pause wrote the row: %+v", got)
			}
			if len(f.events.events) != 0 {
				t.Fatalf("blocked pause published events: %+v", f.events.events)
			}
		})
	}
}

func TestMarkPausedValidatesPauseMode(t *testing.T) {
	for _, mode := range []string{"", "fast", "GRACEFUL"} {
		t.Run("mode "+mode, func(t *testing.T) {
			f := newVersionFixture()
			stored := seedExecuting(f)

			_, err := f.service.MarkPaused(context.Background(), stored.ID, true, mode, "user", 42)

			assertDomainReason(t, err, domain.KindInvalid, "invalid_pause_mode")
			if got := f.repo.versions[stored.ID]; got.Paused || got.Revision != 1 {
				t.Fatalf("invalid mode wrote the row: %+v", got)
			}
			if len(f.events.events) != 0 {
				t.Fatalf("invalid mode published events: %+v", f.events.events)
			}
		})
	}
}

func TestSetBatchNoIsMonotonic(t *testing.T) {
	t.Run("increase stores the batch number without changing status", func(t *testing.T) {
		f := newVersionFixture()
		stored := seedExecuting(f)

		updated, err := f.service.SetBatchNo(context.Background(), stored.ID, 3, 42)
		if err != nil {
			t.Fatalf("SetBatchNo: %v", err)
		}
		if updated.BatchNo != 3 || updated.Status != domain.VersionExecuting {
			t.Fatalf("batchNo/status = %d/%s, want 3/executing", updated.BatchNo, updated.Status)
		}
		if updated.Revision != 2 || updated.UpdatedBy != 42 {
			t.Fatalf("revision/updatedBy = %d/%d, want 2/42", updated.Revision, updated.UpdatedBy)
		}
		if len(f.events.events) != 0 {
			t.Fatalf("SetBatchNo published events: %+v", f.events.events)
		}
	})

	t.Run("decrease is a 409 and writes nothing", func(t *testing.T) {
		f := newVersionFixture()
		stored := seedExecuting(f)
		if _, err := f.service.SetBatchNo(context.Background(), stored.ID, 3, 42); err != nil {
			t.Fatalf("SetBatchNo seed: %v", err)
		}
		before := f.repo.versions[stored.ID]

		_, err := f.service.SetBatchNo(context.Background(), stored.ID, 2, 42)

		assertDomainReason(t, err, domain.KindStateConflict, "version_state_conflict")
		after := f.repo.versions[stored.ID]
		if after.BatchNo != 3 || after.Revision != before.Revision || after.UpdatedBy != before.UpdatedBy {
			t.Fatalf("decrease wrote the row: %+v", after)
		}
	})

	t.Run("equal batch number is idempotent", func(t *testing.T) {
		f := newVersionFixture()
		stored := seedExecuting(f)
		if _, err := f.service.SetBatchNo(context.Background(), stored.ID, 3, 42); err != nil {
			t.Fatalf("SetBatchNo seed: %v", err)
		}
		before := f.repo.versions[stored.ID]

		updated, err := f.service.SetBatchNo(context.Background(), stored.ID, 3, 43)
		if err != nil {
			t.Fatalf("SetBatchNo equal: %v", err)
		}
		if updated.BatchNo != 3 || updated.Revision != before.Revision || updated.UpdatedBy != before.UpdatedBy {
			t.Fatalf("equal batch number rewrote the row: %+v", updated)
		}
	})

	t.Run("negative batch number is a 422", func(t *testing.T) {
		f := newVersionFixture()
		stored := seedExecuting(f)

		_, err := f.service.SetBatchNo(context.Background(), stored.ID, -1, 42)

		assertDomainReason(t, err, domain.KindInvalid, "invalid_batch_no")
		if got := f.repo.versions[stored.ID]; got.BatchNo != 0 || got.Revision != 1 {
			t.Fatalf("negative batch number wrote the row: %+v", got)
		}
	})
}

func TestCompleteExecutionOnlyFromExecuting(t *testing.T) {
	t.Run("executing completes with a status_changed event", func(t *testing.T) {
		f := newVersionFixture()
		stored := seedExecuting(f)

		completed, err := f.service.CompleteExecution(context.Background(), stored.ID, 42)
		if err != nil {
			t.Fatalf("CompleteExecution: %v", err)
		}
		if completed.Status != domain.VersionCompleted || completed.Revision != 2 {
			t.Fatalf("status/revision = %s/%d, want completed/2", completed.Status, completed.Revision)
		}
		if len(f.events.events) != 1 {
			t.Fatalf("events = %d, want 1", len(f.events.events))
		}
		assertPauseEvent(t, f.events.events[0], stored.ID, domain.VersionExecuting, domain.VersionCompleted, false)
	})

	t.Run("continuing stays reserved for P7", func(t *testing.T) {
		f := newVersionFixture()
		stored := f.repo.seed(domain.Version{
			ID: 100, Name: "Order v2", Status: domain.VersionContinuing,
			PauseMode: domain.PauseModeGraceful, CreatedBy: 1, UpdatedBy: 1,
		})

		_, err := f.service.CompleteExecution(context.Background(), stored.ID, 42)

		assertDomainReason(t, err, domain.KindStateConflict, "version_state_conflict")
		if got := f.repo.versions[stored.ID]; got.Status != domain.VersionContinuing {
			t.Fatalf("status = %s, want continuing", got.Status)
		}
		if len(f.events.events) != 0 {
			t.Fatalf("blocked complete published events: %+v", f.events.events)
		}
	})

	for _, status := range []string{
		domain.VersionDraft, domain.VersionClarifying, domain.VersionDesigning,
		domain.VersionReviewing, domain.VersionCompleted, domain.VersionArchived,
	} {
		t.Run("from "+status, func(t *testing.T) {
			f := newVersionFixture()
			stored := f.repo.seed(domain.Version{
				ID: 100, Name: "Order v2", Status: status,
				PauseMode: domain.PauseModeGraceful, CreatedBy: 1, UpdatedBy: 1,
			})

			_, err := f.service.CompleteExecution(context.Background(), stored.ID, 42)

			assertDomainReason(t, err, domain.KindStateConflict, "version_state_conflict")
			if got := f.repo.versions[stored.ID]; got.Status != status || got.Revision != 1 {
				t.Fatalf("blocked complete wrote the row: %+v", got)
			}
			if len(f.events.events) != 0 {
				t.Fatalf("blocked complete published events: %+v", f.events.events)
			}
		})
	}

	t.Run("repeat complete is a conflict", func(t *testing.T) {
		f := newVersionFixture()
		stored := seedExecuting(f)
		if _, err := f.service.CompleteExecution(context.Background(), stored.ID, 42); err != nil {
			t.Fatalf("first complete: %v", err)
		}
		before := f.repo.versions[stored.ID]

		_, err := f.service.CompleteExecution(context.Background(), stored.ID, 42)

		assertDomainReason(t, err, domain.KindStateConflict, "version_state_conflict")
		if got := f.repo.versions[stored.ID]; got.Status != domain.VersionCompleted || got.Revision != before.Revision {
			t.Fatalf("repeat complete wrote the row: %+v", got)
		}
		if len(f.events.events) != 1 {
			t.Fatalf("repeat complete published events: %+v", f.events.events)
		}
	})

	t.Run("missing version is 404", func(t *testing.T) {
		f := newVersionFixture()

		_, err := f.service.CompleteExecution(context.Background(), 999, 42)

		assertDomainReason(t, err, domain.KindNotFound, "not_found")
	})
}
