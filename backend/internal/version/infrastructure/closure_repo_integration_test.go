//go:build integration

package versionpg_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// closureFixture creates one version and one clarified requirement (revision 1,
// current). The closure repository operates on the requirement row only.
type closureFixture struct {
	pool            *pgxpool.Pool
	ids             *id.Snowflake
	versionRepo     *versionpg.Repository
	requirementRepo *versionpg.RequirementRepository
	closureRepo     *versionpg.ClosureRepository
	versionID       int64
	requirement     version.Requirement
	revision        version.RequirementVersion
}

func newClosureFixture(t *testing.T) *closureFixture {
	t.Helper()
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	versionRepo := versionpg.New(pool, ids)
	requirementRepo := versionpg.NewRequirementRepository(pool)
	closureRepo := versionpg.NewClosureRepository(pool)
	ctx := context.Background()

	v := sampleVersion(ids, 42)
	if err := versionRepo.Create(ctx, v, sampleBindings(ids, v.ID)); err != nil {
		t.Fatalf("create version: %v", err)
	}
	requirement, revision := sampleRequirement(ids, v.ID)
	requirement.Status = version.RequirementClarified
	if err := requirementRepo.Create(ctx, requirement, revision); err != nil {
		t.Fatalf("create requirement: %v", err)
	}
	return &closureFixture{
		pool: pool, ids: ids, versionRepo: versionRepo, requirementRepo: requirementRepo,
		closureRepo: closureRepo, versionID: v.ID, requirement: requirement, revision: revision,
	}
}

func passingChecks() version.ClosureChecks {
	return version.ClosureChecks{
		RequirementState:   true,
		HasConfirmedCase:   true,
		CasesTerminal:      true,
		FailedCaseHasIssue: true,
		NoOpenWork:         true,
		NoActiveIntent:     true,
		NoInflightCaseRun:  true,
		CoverageRecorded:   true,
	}
}

func (f *closureFixture) newClosure(checks version.ClosureChecks) version.RequirementClosure {
	runID := int64(9001)
	return version.RequirementClosure{
		ID:                   f.ids.Next(),
		RequirementID:        f.requirement.ID,
		RequirementVersionID: f.revision.ID,
		CheckResult:          version.ClosureCheckResult{ClosureChecks: checks},
		EvidenceRefs: []version.ClosureEvidenceRef{
			{Kind: version.ClosureEvidenceCaseRun, ID: "1201"},
			{Kind: version.ClosureEvidenceIssue, ID: "1301"},
		},
		ClosedByRunID: &runID,
		ClosedAt:      testTime,
		CreatedAt:     testTime,
		UpdatedAt:     testTime,
	}
}

func (f *closureFixture) closedRequirement() version.Requirement {
	requirement := f.requirement
	requirement.Status = version.RequirementClosed
	closedAt := testTime
	runID := int64(9001)
	requirement.ClosedAt = &closedAt
	requirement.ClosedByRunID = &runID
	requirement.UpdatedAt = testTime
	return requirement
}

func TestClosureRepoCloseLatestAndChecksJSON(t *testing.T) {
	fixture := newClosureFixture(t)
	ctx := context.Background()
	closure := fixture.newClosure(passingChecks())
	recheckCalls := 0
	var lockedStatus version.RequirementStatus

	stored, err := fixture.closureRepo.Close(ctx, closure, fixture.closedRequirement(), func(_ context.Context, locked version.Requirement) error {
		recheckCalls++
		lockedStatus = locked.Status
		if locked.CurrentVersionID == nil || *locked.CurrentVersionID != fixture.revision.ID {
			return version.ErrStateConflict
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if recheckCalls != 1 || lockedStatus != version.RequirementClarified {
		t.Fatalf("recheck calls/locked status = %d/%q", recheckCalls, lockedStatus)
	}
	if stored.ID != closure.ID || stored.CloseRound != 1 {
		t.Fatalf("stored closure = %#v", stored)
	}
	if !stored.CheckResult.Passed() || stored.CheckResult.Waived {
		t.Fatalf("stored checks = %#v", stored.CheckResult)
	}

	var (
		rows          int
		closeRound    int
		checkState    string
		waived        string
		evidenceMatch bool
	)
	if err := fixture.pool.QueryRow(ctx, `
		SELECT count(*), max(close_round) FROM requirement_closure WHERE requirement_id = $1
	`, fixture.requirement.ID).Scan(&rows, &closeRound); err != nil {
		t.Fatalf("count closures: %v", err)
	}
	if rows != 1 || closeRound != 1 {
		t.Fatalf("closure rows/round = %d/%d", rows, closeRound)
	}
	if err := fixture.pool.QueryRow(ctx, `
		SELECT check_result->>'requirement_state', check_result->>'waived',
		       evidence_refs @> '[{"kind":"case_run","id":"1201"}]'::jsonb
		FROM requirement_closure WHERE requirement_id = $1
	`, fixture.requirement.ID).Scan(&checkState, &waived, &evidenceMatch); err != nil {
		t.Fatalf("read closure JSON: %v", err)
	}
	if checkState != "true" || waived != "false" || !evidenceMatch {
		t.Fatalf("check_result/evidence = %q/%q/%v", checkState, waived, evidenceMatch)
	}

	var (
		status        string
		closedAtSet   bool
		closedByRunID *int64
	)
	if err := fixture.pool.QueryRow(ctx, `
		SELECT status, closed_at IS NOT NULL, closed_by_run_id FROM requirement WHERE id = $1
	`, fixture.requirement.ID).Scan(&status, &closedAtSet, &closedByRunID); err != nil {
		t.Fatalf("read requirement: %v", err)
	}
	if status != string(version.RequirementClosed) || !closedAtSet || closedByRunID == nil || *closedByRunID != 9001 {
		t.Fatalf("requirement = %q/%v/%v", status, closedAtSet, closedByRunID)
	}

	latest, found, err := fixture.closureRepo.Latest(ctx, fixture.requirement.ID)
	if err != nil || !found {
		t.Fatalf("Latest = %v/%v", found, err)
	}
	if latest.ID != stored.ID || latest.CloseRound != 1 || !latest.CheckResult.Passed() {
		t.Fatalf("latest = %#v", latest)
	}
	if len(latest.EvidenceRefs) != 2 || latest.EvidenceRefs[0].Kind != version.ClosureEvidenceCaseRun {
		t.Fatalf("evidence refs = %#v", latest.EvidenceRefs)
	}
	if !latest.ClosedAt.Equal(testTime) {
		t.Fatalf("closed_at = %v", latest.ClosedAt)
	}

	loaded, err := fixture.requirementRepo.GetRequirement(ctx, fixture.requirement.ID)
	if err != nil {
		t.Fatalf("GetRequirement: %v", err)
	}
	if loaded.Status != version.RequirementClosed {
		t.Fatalf("GetRequirement status = %q", loaded.Status)
	}
	if _, err := fixture.requirementRepo.GetRequirement(ctx, 999999); !errors.Is(err, version.ErrRequirementNotFound) {
		t.Fatalf("missing GetRequirement error = %v", err)
	}

	// The row lock and status guard reject a second close of the same round; the
	// recheck never runs against an already closed requirement.
	if _, err := fixture.closureRepo.Close(ctx, fixture.newClosure(passingChecks()), fixture.closedRequirement(), func(context.Context, version.Requirement) error {
		recheckCalls++
		return nil
	}); !errors.Is(err, version.ErrStateConflict) {
		t.Fatalf("second Close error = %v, want ErrStateConflict", err)
	}
	if recheckCalls != 1 {
		t.Fatalf("recheck calls after conflict = %d, want 1", recheckCalls)
	}

	// The unique (requirement_id, close_round) constraint is the final guard.
	_, err = fixture.pool.Exec(ctx, `
		INSERT INTO requirement_closure (id, requirement_id, requirement_version_id, close_round, check_result)
		VALUES ($1, $2, $3, 1, '{}'::jsonb)
	`, fixture.ids.Next(), fixture.requirement.ID, fixture.revision.ID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("duplicate round error = %v, want unique violation", err)
	}
}

func TestClosureRepoForeignKeyRollsBack(t *testing.T) {
	fixture := newClosureFixture(t)
	ctx := context.Background()
	bad := fixture.newClosure(passingChecks())
	bad.RequirementVersionID = 999999

	if _, err := fixture.closureRepo.Close(ctx, bad, fixture.closedRequirement(), nil); !errors.Is(err, version.ErrNotFound) {
		t.Fatalf("bad requirement_version_id error = %v, want ErrNotFound", err)
	}
	var (
		closureRows int
		status      string
	)
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM requirement_closure WHERE requirement_id = $1`,
		fixture.requirement.ID).Scan(&closureRows); err != nil {
		t.Fatalf("count closures: %v", err)
	}
	if closureRows != 0 {
		t.Fatalf("FK failure left %d closure rows", closureRows)
	}
	if err := fixture.pool.QueryRow(ctx, `SELECT status FROM requirement WHERE id = $1`,
		fixture.requirement.ID).Scan(&status); err != nil {
		t.Fatalf("read requirement: %v", err)
	}
	if status != string(version.RequirementClarified) {
		t.Fatalf("FK failure changed status to %q", status)
	}
}

func TestClosureRepoReopenAndNextRound(t *testing.T) {
	fixture := newClosureFixture(t)
	ctx := context.Background()
	first, err := fixture.closureRepo.Close(ctx, fixture.newClosure(passingChecks()), fixture.closedRequirement(), nil)
	if err != nil {
		t.Fatalf("first Close: %v", err)
	}

	reopenedAt := testTime.Add(time.Hour)
	reopenedRequirement := fixture.requirement
	reopenedRequirement.Status = version.RequirementDraft
	reopenedRequirement.UpdatedAt = reopenedAt
	closure, err := fixture.closureRepo.Reopen(ctx, reopenedRequirement, reopenedAt)
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if closure.ID != first.ID || closure.ReopenCount != 1 || !closure.UpdatedAt.Equal(reopenedAt) {
		t.Fatalf("reopened closure = %#v", closure)
	}

	var (
		status   string
		closedAt *time.Time
		runID    *int64
	)
	if err := fixture.pool.QueryRow(ctx, `
		SELECT status, closed_at, closed_by_run_id FROM requirement WHERE id = $1
	`, fixture.requirement.ID).Scan(&status, &closedAt, &runID); err != nil {
		t.Fatalf("read reopened requirement: %v", err)
	}
	if status != string(version.RequirementDraft) || closedAt != nil || runID != nil {
		t.Fatalf("reopened requirement = %q/%v/%v", status, closedAt, runID)
	}

	// Reopening a draft requirement is a state conflict.
	if _, err := fixture.closureRepo.Reopen(ctx, reopenedRequirement, reopenedAt); !errors.Is(err, version.ErrStateConflict) {
		t.Fatalf("second Reopen error = %v, want ErrStateConflict", err)
	}

	// Re-clarify and close again: a new round is appended and the old one keeps
	// its reopen_count.
	clarified := fixture.requirement
	clarified.UpdatedAt = reopenedAt.Add(time.Minute)
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE requirement SET status = 'clarified', updated_at = $2 WHERE id = $1
	`, clarified.ID, clarified.UpdatedAt); err != nil {
		t.Fatalf("re-clarify requirement: %v", err)
	}
	second, err := fixture.closureRepo.Close(ctx, fixture.newClosure(passingChecks()), clarified, nil)
	if err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if second.CloseRound != 2 || second.ID == first.ID {
		t.Fatalf("second closure = %#v", second)
	}
	latest, found, err := fixture.closureRepo.Latest(ctx, fixture.requirement.ID)
	if err != nil || !found || latest.ID != second.ID || latest.CloseRound != 2 {
		t.Fatalf("latest after reopen = %#v/%v/%v", latest, found, err)
	}
	var (
		firstReopenCount  int
		secondReopenCount int
	)
	if err := fixture.pool.QueryRow(ctx, `
		SELECT
			(SELECT reopen_count FROM requirement_closure WHERE id = $1),
			(SELECT reopen_count FROM requirement_closure WHERE id = $2)
	`, first.ID, second.ID).Scan(&firstReopenCount, &secondReopenCount); err != nil {
		t.Fatalf("read reopen counts: %v", err)
	}
	if firstReopenCount != 1 || secondReopenCount != 0 {
		t.Fatalf("reopen counts = %d/%d", firstReopenCount, secondReopenCount)
	}
}

func TestClosureRepoCloseRecheckRollsBack(t *testing.T) {
	fixture := newClosureFixture(t)
	ctx := context.Background()
	wantErr := &version.ErrClosePredicateFailed{
		Failed: map[string]int{version.ClosureCheckNoOpenWork: 1},
	}

	recheckCalls := 0
	_, err := fixture.closureRepo.Close(ctx, fixture.newClosure(passingChecks()), fixture.closedRequirement(), func(_ context.Context, locked version.Requirement) error {
		recheckCalls++
		// The callback runs after FOR UPDATE: the row still carries the
		// pre-close projection and the current requirement version.
		if locked.Status != version.RequirementClarified || locked.ClosedAt != nil || locked.ClosedByRunID != nil {
			return version.ErrStateConflict
		}
		if locked.CurrentVersionID == nil || *locked.CurrentVersionID != fixture.revision.ID {
			return version.ErrStateConflict
		}
		return wantErr
	})
	var predicateErr *version.ErrClosePredicateFailed
	if !errors.As(err, &predicateErr) {
		t.Fatalf("Close error = %v, want ErrClosePredicateFailed", err)
	}
	if predicateErr.Failed[version.ClosureCheckNoOpenWork] != 1 || recheckCalls != 1 {
		t.Fatalf("predicate/calls = %#v/%d", predicateErr.Failed, recheckCalls)
	}

	var (
		closureRows int
		status      string
		closedAt    *time.Time
		runID       *int64
	)
	if err := fixture.pool.QueryRow(ctx, `
		SELECT count(*) FROM requirement_closure WHERE requirement_id = $1
	`, fixture.requirement.ID).Scan(&closureRows); err != nil {
		t.Fatalf("count closures: %v", err)
	}
	if closureRows != 0 {
		t.Fatalf("recheck failure left %d closure rows", closureRows)
	}
	if err := fixture.pool.QueryRow(ctx, `
		SELECT status, closed_at, closed_by_run_id FROM requirement WHERE id = $1
	`, fixture.requirement.ID).Scan(&status, &closedAt, &runID); err != nil {
		t.Fatalf("read requirement: %v", err)
	}
	if status != string(version.RequirementClarified) || closedAt != nil || runID != nil {
		t.Fatalf("recheck failure changed requirement = %q/%v/%v", status, closedAt, runID)
	}
}

// TestClosureRepoCloseRecheckSeesConcurrentWorkItem proves the recheck runs
// while the requirement row lock is held: a work item committed between the
// service pre-check and the lock is visible to the callback, which rejects the
// close and rolls the whole transaction back.
func TestClosureRepoCloseRecheckSeesConcurrentWorkItem(t *testing.T) {
	fixture := newClosureFixture(t)
	ctx := context.Background()

	locked := make(chan struct{})
	workCommitted := make(chan struct{})
	closeErr := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := fixture.closureRepo.Close(ctx, fixture.newClosure(passingChecks()), fixture.closedRequirement(), func(callbackCtx context.Context, lockedRequirement version.Requirement) error {
			if lockedRequirement.Status != version.RequirementClarified {
				return version.ErrStateConflict
			}
			close(locked)
			<-workCommitted
			// Re-evaluate through a second connection: the concurrent insert
			// committed while this close still holds the requirement lock.
			var open int
			if err := fixture.pool.QueryRow(callbackCtx, `
				SELECT count(*) FROM work_item
				WHERE version_id = $1 AND status IN ('open', 'claimed', 'running')
			`, fixture.versionID).Scan(&open); err != nil {
				return err
			}
			if open > 0 {
				return &version.ErrClosePredicateFailed{
					Failed: map[string]int{version.ClosureCheckNoOpenWork: open},
				}
			}
			return nil
		})
		closeErr <- err
	}()

	<-locked
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO work_item (id, version_id, team, type, status, priority, dedupe_key)
		VALUES ($1, $2, 'steward', 'requirement.close', 'open', 0, 'race-open-work')
	`, fixture.ids.Next(), fixture.versionID); err != nil {
		t.Fatalf("insert work item: %v", err)
	}
	close(workCommitted)
	wg.Wait()

	var predicateErr *version.ErrClosePredicateFailed
	if err := <-closeErr; !errors.As(err, &predicateErr) {
		t.Fatalf("concurrent Close error = %v, want ErrClosePredicateFailed", err)
	}

	var (
		closureRows int
		status      string
	)
	if err := fixture.pool.QueryRow(ctx, `
		SELECT count(*) FROM requirement_closure WHERE requirement_id = $1
	`, fixture.requirement.ID).Scan(&closureRows); err != nil {
		t.Fatalf("count closures: %v", err)
	}
	if closureRows != 0 {
		t.Fatalf("concurrent close persisted %d closure rows", closureRows)
	}
	if err := fixture.pool.QueryRow(ctx, `SELECT status FROM requirement WHERE id = $1`,
		fixture.requirement.ID).Scan(&status); err != nil {
		t.Fatalf("read requirement: %v", err)
	}
	if status != string(version.RequirementClarified) {
		t.Fatalf("concurrent close changed status to %q", status)
	}
	var openWork int
	if err := fixture.pool.QueryRow(ctx, `
		SELECT count(*) FROM work_item WHERE version_id = $1 AND status = 'open'
	`, fixture.versionID).Scan(&openWork); err != nil {
		t.Fatalf("count work items: %v", err)
	}
	if openWork != 1 {
		t.Fatalf("open work items = %d, want 1", openWork)
	}
}

func TestClosureRepoConcurrentCloseOnlyOneWins(t *testing.T) {
	fixture := newClosureFixture(t)
	ctx := context.Background()

	type result struct {
		closure version.RequirementClosure
		err     error
	}
	results := make(chan result, 2)
	var start sync.WaitGroup
	start.Add(1)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start.Wait()
			closure, err := fixture.closureRepo.Close(ctx, fixture.newClosure(passingChecks()), fixture.closedRequirement(), func(_ context.Context, locked version.Requirement) error {
				if locked.Status != version.RequirementClarified {
					return version.ErrStateConflict
				}
				return nil
			})
			results <- result{closure: closure, err: err}
		}()
	}
	start.Done()
	wg.Wait()
	close(results)

	successes, conflicts := 0, 0
	for item := range results {
		switch {
		case item.err == nil:
			successes++
		case errors.Is(item.err, version.ErrStateConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent Close error: %v", item.err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent close successes/conflicts = %d/%d", successes, conflicts)
	}
	var rows int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM requirement_closure WHERE requirement_id = $1`,
		fixture.requirement.ID).Scan(&rows); err != nil {
		t.Fatalf("count closures: %v", err)
	}
	if rows != 1 {
		t.Fatalf("concurrent close rows = %d, want 1", rows)
	}
}
