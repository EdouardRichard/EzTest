//go:build integration

package versionpg_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	executionpg "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/postgres"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// raceIntent builds one open intent bound to the given version/case version.
func raceIntent(ids interface{ Next() int64 }, versionID, caseVersionID int64, dedupe string) execution.ExecutionIntent {
	now := time.Now().UTC()
	return execution.ExecutionIntent{
		ID:                     ids.Next(),
		VersionID:              versionID,
		CaseVersionID:          caseVersionID,
		VersionSUTIDs:          []int64{5001},
		EnvironmentFingerprint: strings.Repeat("b", 64),
		Purpose:                execution.PurposeRetest,
		Status:                 execution.IntentOpen,
		DedupeKey:              dedupe,
		AttemptNo:              1,
		CreatedAt:              now,
		UpdatedAt:              now,
	}
}

// waitForAdvisoryWaiter blocks until some session is queued on an advisory
// lock. The concurrency tests use it to prove the intent transaction really
// waits for the close/closure transaction instead of racing it.
func waitForAdvisoryWaiter(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		if err := pool.QueryRow(context.Background(), `
			SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted
		`).Scan(&waiting); err != nil {
			t.Fatalf("read pg_locks: %v", err)
		}
		if waiting > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("intent creation never blocked on the version advisory lock")
}

func countVersionActiveIntents(t *testing.T, pool *pgxpool.Pool, versionID int64) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM execution_intent
		WHERE version_id = $1 AND status IN ('open', 'claimed', 'running')
	`, versionID).Scan(&count); err != nil {
		t.Fatalf("count active intents: %v", err)
	}
	return count
}

// TestVersionCloseSerializesWithIntentCreation pins the close x intent race:
// whichever transaction wins the shared version advisory lock is seen by the
// other one, so a completed version can never keep an active intent.
func TestVersionCloseSerializesWithIntentCreation(t *testing.T) {
	t.Run("close wins the lock and the later intent creation is rejected", func(t *testing.T) {
		fixture := newVersionCloseFixture(t)
		intents := executionpg.NewRepository(fixture.pool, fixture.ids).Intents
		ctx := context.Background()

		locked := make(chan struct{})
		allowClose := make(chan struct{})
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(allowClose) }) }
		closeDone := make(chan struct{})
		closeErr := make(chan error, 1)
		t.Cleanup(func() {
			release()
			<-closeDone
		})
		go func() {
			defer close(closeDone)
			_, err := fixture.versionRepo.Close(ctx, fixture.version.ID, version.VersionCloseFields{
				ClosedAt: testTime.Add(time.Hour), CloseReason: "racing close",
			}, func(callbackCtx context.Context, _ version.Version) error {
				close(locked)
				<-allowClose
				// The service predicate re-reads the active intents through a
				// second connection while the close transaction holds its locks.
				var active int
				if err := fixture.pool.QueryRow(callbackCtx, `
					SELECT count(*) FROM execution_intent
					WHERE version_id = $1 AND status IN ('open', 'claimed', 'running')
				`, fixture.version.ID).Scan(&active); err != nil {
					return err
				}
				if active > 0 {
					return version.ErrStateConflict
				}
				return nil
			})
			closeErr <- err
		}()

		<-locked
		intentErr := make(chan error, 1)
		go func() {
			intentErr <- intents.Create(ctx, raceIntent(fixture.ids, fixture.version.ID, fixture.ids.Next(), "close-wins"))
		}()
		waitForAdvisoryWaiter(t, fixture.pool)
		release()

		if err := <-closeErr; err != nil {
			t.Fatalf("close that held the advisory lock failed: %v", err)
		}
		if err := <-intentErr; !errors.Is(err, execution.ErrVersionCompleted) {
			t.Fatalf("intent creation after close = %v, want ErrVersionCompleted", err)
		}
		status, _, _, _ := fixture.readVersion(t)
		if status != string(version.StatusCompleted) {
			t.Fatalf("version status = %q, want completed", status)
		}
		if active := countVersionActiveIntents(t, fixture.pool, fixture.version.ID); active != 0 {
			t.Fatalf("completed version keeps %d active intents", active)
		}

		// The intent service recovers from this race by reopening the version
		// through the P4 reopen path and retrying the insert.
		if _, changed, err := fixture.versionRepo.ReopenIfCompleted(ctx, fixture.version.ID, testTime.Add(2*time.Hour)); err != nil || !changed {
			t.Fatalf("reopen after losing the race = %v/%v", changed, err)
		}
		if err := intents.Create(ctx, raceIntent(fixture.ids, fixture.version.ID, fixture.ids.Next(), "close-wins-retry")); err != nil {
			t.Fatalf("intent creation after reopen: %v", err)
		}
		status, _, _, _ = fixture.readVersion(t)
		if status != string(version.StatusRunning) {
			t.Fatalf("version status after reopen = %q, want running", status)
		}
		if active := countVersionActiveIntents(t, fixture.pool, fixture.version.ID); active != 1 {
			t.Fatalf("active intents after reopen = %d, want 1", active)
		}
	})

	t.Run("intent committed before close makes the close recheck fail", func(t *testing.T) {
		fixture := newVersionCloseFixture(t)
		intents := executionpg.NewRepository(fixture.pool, fixture.ids).Intents
		ctx := context.Background()

		if err := intents.Create(ctx, raceIntent(fixture.ids, fixture.version.ID, fixture.ids.Next(), "intent-wins")); err != nil {
			t.Fatalf("create intent: %v", err)
		}
		_, err := fixture.versionRepo.Close(ctx, fixture.version.ID, version.VersionCloseFields{
			ClosedAt: testTime.Add(time.Hour), CloseReason: "must lose",
		}, func(callbackCtx context.Context, _ version.Version) error {
			var active int
			if err := fixture.pool.QueryRow(callbackCtx, `
				SELECT count(*) FROM execution_intent
				WHERE version_id = $1 AND status IN ('open', 'claimed', 'running')
			`, fixture.version.ID).Scan(&active); err != nil {
				return err
			}
			if active > 0 {
				return version.ErrStateConflict
			}
			return nil
		})
		if err == nil {
			t.Fatal("close committed even though an active intent existed")
		}
		status, _, _, _ := fixture.readVersion(t)
		if status != string(version.StatusRunning) {
			t.Fatalf("version status = %q, want running", status)
		}
		if active := countVersionActiveIntents(t, fixture.pool, fixture.version.ID); active != 1 {
			t.Fatalf("active intents = %d, want 1", active)
		}
	})
}

// insertRaceCase writes the minimal design rows that bind a case version to a
// requirement so the intent repository can resolve the requirement state.
func insertRaceCase(t *testing.T, pool *pgxpool.Pool, ids interface{ Next() int64 }, versionID, requirementID, requirementVersionID int64) int64 {
	t.Helper()
	ctx := context.Background()
	scenarioID := ids.Next()
	caseID := ids.Next()
	caseVersionID := ids.Next()
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_scenario (id, version_id, requirement_id, requirement_version_id, name)
		VALUES ($1, $2, $3, $4, 'race scenario')
	`, scenarioID, versionID, requirementID, requirementVersionID); err != nil {
		t.Fatalf("insert case scenario: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_entity (id, scenario_id, current_version_id, status)
		VALUES ($1, $2, $3, 'confirmed')
	`, caseID, scenarioID, caseVersionID); err != nil {
		t.Fatalf("insert case entity: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_version (id, case_id, revision, title, content_hash, status)
		VALUES ($1, $2, 1, 'race case', $3, 'confirmed')
	`, caseVersionID, caseID, strings.Repeat("a", 64)); err != nil {
		t.Fatalf("insert case version: %v", err)
	}
	return caseVersionID
}

// TestRequirementClosureSerializesWithIntentCreation pins the closure x intent
// race: the closure recheck and the intent insert share the version advisory
// lock, so a closed requirement can never keep an active intent. When the
// closure wins, the intent creation loses (a closed requirement is terminal);
// when the intent wins, the closure recheck rejects.
func TestRequirementClosureSerializesWithIntentCreation(t *testing.T) {
	closureRecheck := func(fixture *closureFixture, caseVersionID int64) func(context.Context, version.Requirement) error {
		return func(callbackCtx context.Context, _ version.Requirement) error {
			var active int
			if err := fixture.pool.QueryRow(callbackCtx, `
				SELECT count(*) FROM execution_intent
				WHERE version_id = $1 AND case_version_id = $2
				  AND status IN ('open', 'claimed', 'running')
			`, fixture.versionID, caseVersionID).Scan(&active); err != nil {
				return err
			}
			if active > 0 {
				return &version.ErrClosePredicateFailed{
					Failed: map[string]int{version.ClosureCheckNoActiveIntent: active},
				}
			}
			return nil
		}
	}

	t.Run("closure wins the lock and the later intent creation is rejected", func(t *testing.T) {
		fixture := newClosureFixture(t)
		caseVersionID := insertRaceCase(t, fixture.pool, fixture.ids, fixture.versionID, fixture.requirement.ID, fixture.revision.ID)
		intents := executionpg.NewRepository(fixture.pool, fixture.ids).Intents
		ctx := context.Background()

		locked := make(chan struct{})
		allowClose := make(chan struct{})
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(allowClose) }) }
		closeDone := make(chan struct{})
		closeErr := make(chan error, 1)
		t.Cleanup(func() {
			release()
			<-closeDone
		})
		go func() {
			defer close(closeDone)
			_, err := fixture.closureRepo.Close(ctx, fixture.newClosure(passingChecks()), fixture.closedRequirement(), func(callbackCtx context.Context, lockedRequirement version.Requirement) error {
				if lockedRequirement.Status != version.RequirementClarified {
					return version.ErrStateConflict
				}
				close(locked)
				<-allowClose
				return closureRecheck(fixture, caseVersionID)(callbackCtx, lockedRequirement)
			})
			closeErr <- err
		}()

		<-locked
		intentErr := make(chan error, 1)
		go func() {
			intentErr <- intents.Create(ctx, raceIntent(fixture.ids, fixture.versionID, caseVersionID, "closure-wins"))
		}()
		waitForAdvisoryWaiter(t, fixture.pool)
		release()

		if err := <-closeErr; err != nil {
			t.Fatalf("closure that held the advisory lock failed: %v", err)
		}
		if err := <-intentErr; !errors.Is(err, execution.ErrRequirementClosed) {
			t.Fatalf("intent creation after closure = %v, want ErrRequirementClosed", err)
		}
		status, err := fixture.requirementRepo.GetRequirement(ctx, fixture.requirement.ID)
		if err != nil {
			t.Fatalf("read requirement: %v", err)
		}
		if status.Status != version.RequirementClosed {
			t.Fatalf("requirement status = %q, want closed", status.Status)
		}
		var active int
		if err := fixture.pool.QueryRow(ctx, `
			SELECT count(*) FROM execution_intent
			WHERE version_id = $1 AND case_version_id = $2
		`, fixture.versionID, caseVersionID).Scan(&active); err != nil {
			t.Fatalf("count intents: %v", err)
		}
		if active != 0 {
			t.Fatalf("closed requirement keeps %d intents", active)
		}
	})

	t.Run("intent committed before closure makes the closure recheck fail", func(t *testing.T) {
		fixture := newClosureFixture(t)
		caseVersionID := insertRaceCase(t, fixture.pool, fixture.ids, fixture.versionID, fixture.requirement.ID, fixture.revision.ID)
		intents := executionpg.NewRepository(fixture.pool, fixture.ids).Intents
		ctx := context.Background()

		if err := intents.Create(ctx, raceIntent(fixture.ids, fixture.versionID, caseVersionID, "intent-wins")); err != nil {
			t.Fatalf("create intent: %v", err)
		}
		_, err := fixture.closureRepo.Close(ctx, fixture.newClosure(passingChecks()), fixture.closedRequirement(), closureRecheck(fixture, caseVersionID))
		if err == nil {
			t.Fatal("closure committed even though an active intent existed")
		}
		current, err := fixture.requirementRepo.GetRequirement(ctx, fixture.requirement.ID)
		if err != nil {
			t.Fatalf("read requirement: %v", err)
		}
		if current.Status != version.RequirementClarified {
			t.Fatalf("requirement status = %q, want clarified", current.Status)
		}
		var active int
		if err := fixture.pool.QueryRow(ctx, `
			SELECT count(*) FROM execution_intent
			WHERE version_id = $1 AND case_version_id = $2
			  AND status IN ('open', 'claimed', 'running')
		`, fixture.versionID, caseVersionID).Scan(&active); err != nil {
			t.Fatalf("count active intents: %v", err)
		}
		if active != 1 {
			t.Fatalf("active intents = %d, want 1", active)
		}
	})
}
