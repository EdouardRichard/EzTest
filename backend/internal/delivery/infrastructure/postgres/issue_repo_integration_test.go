//go:build integration

package deliverypg

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// issueRepoTestCandidate is one candidate fixture of the issue repository
// tests. Zero fields fall back to issueRepoCandidateDefaults.
type issueRepoTestCandidate struct {
	versionID     int64
	caseRunID     int64
	caseVersionID int64
	stepSeq       int
	assertionSeq  int
	failureClass  string
	category      string
	severity      string
	waveID        *int64
	versionSUTID  *int64
	requirementID int64
}

func issueRepoNum(value int64) *int64 { return &value }

func issueRepoRevision(value int32) *int32 { return &value }

func issueRepoCandidateDefaults(in issueRepoTestCandidate) issueRepoTestCandidate {
	if in.versionID == 0 {
		in.versionID = 100
	}
	if in.caseRunID == 0 {
		in.caseRunID = 3001
	}
	if in.caseVersionID == 0 {
		in.caseVersionID = 4001
	}
	if in.stepSeq == 0 {
		in.stepSeq = 2
	}
	if in.assertionSeq == 0 {
		in.assertionSeq = 1
	}
	if in.failureClass == "" {
		// Only application failures become issues (spec §5.1).
		in.failureClass = "application"
	}
	if in.category == "" {
		in.category = "ui"
	}
	if in.severity == "" {
		in.severity = "major"
	}
	return in
}

// issueRepoBuildCandidate derives the deterministic issue and refs of one
// failed case_run through the domain constructor.
func issueRepoBuildCandidate(t *testing.T, in issueRepoTestCandidate) (delivery.Issue, []delivery.IssueRef) {
	t.Helper()
	in = issueRepoCandidateDefaults(in)
	issue, refs, err := delivery.NewIssueCandidate(delivery.CandidateInput{
		VersionID:      in.versionID,
		CaseRunID:      in.caseRunID,
		CaseVersionID:  in.caseVersionID,
		RequirementID:  in.requirementID,
		CaseTitle:      "Checkout submission",
		StepSeq:        in.stepSeq,
		AssertionSeq:   in.assertionSeq,
		FailureClass:   in.failureClass,
		Category:       in.category,
		Severity:       in.severity,
		FailedExpected: "order confirmation visible",
		FailedActual:   "order confirmation missing",
		VersionSUTID:   in.versionSUTID,
		WaveID:         in.waveID,
	})
	if err != nil {
		t.Fatalf("build candidate: %v", err)
	}
	return issue, refs
}

func newIssueRepoForTest(t *testing.T) (*IssueRepository, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.NewDB(t)
	return NewIssueRepository(pool), pool
}

// issueRepoCreateCandidate creates one candidate and fails the test when the
// repository reports an existing issue.
func issueRepoCreateCandidate(t *testing.T, repo *IssueRepository, in issueRepoTestCandidate) delivery.Issue {
	t.Helper()
	issue, refs := issueRepoBuildCandidate(t, in)
	created, isNew, err := repo.CreateCandidate(context.Background(), issue, refs)
	if err != nil {
		t.Fatalf("create candidate: %v", err)
	}
	if !isNew {
		t.Fatalf("create candidate reported an existing issue %d", created.ID)
	}
	return created
}

// TestIssueRepoCreateCandidate runs the candidate ingestion/dedupe, read and
// lookup behaviour against a real PostgreSQL schema.
func TestIssueRepoCreateCandidate(t *testing.T) {
	repo, _ := newIssueRepoForTest(t)
	ctx := context.Background()

	first := issueRepoTestCandidate{
		waveID:        issueRepoNum(700),
		versionSUTID:  issueRepoNum(9001),
		requirementID: 1200,
	}
	issue, refs := issueRepoBuildCandidate(t, first)
	if issue.ID != 0 {
		t.Fatalf("domain candidate ID = %d, want 0 (repository assigned)", issue.ID)
	}

	created, isNew, err := repo.CreateCandidate(ctx, issue, refs)
	if err != nil {
		t.Fatalf("first CreateCandidate: %v", err)
	}
	if !isNew {
		t.Fatal("first CreateCandidate must report created=true")
	}
	if created.ID <= 0 {
		t.Fatalf("created ID = %d, want > 0", created.ID)
	}
	if created.VersionID != 100 || created.Status != delivery.IssueStatusOpen {
		t.Fatalf("created issue = %+v", created)
	}
	if created.ExpectedRevision != 1 || created.ReopenCount != 0 {
		t.Fatalf("revision/reopen = %d/%d, want 1/0", created.ExpectedRevision, created.ReopenCount)
	}
	if created.DedupeKey != issue.DedupeKey || created.Fingerprint != issue.Fingerprint {
		t.Fatalf("dedupe/fingerprint drifted: %+v", created)
	}
	if created.Source != delivery.IssueCandidateSource {
		t.Fatalf("source = %q, want %q", created.Source, delivery.IssueCandidateSource)
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatal("created timestamps must be assigned by the repository")
	}
	if created.MergedIntoID != nil || created.MergedAt != nil || created.MergedBy != nil {
		t.Fatalf("new issue must be unmerged: %+v", created)
	}

	storedRefs, err := repo.ListRefs(ctx, created.ID)
	if err != nil {
		t.Fatalf("ListRefs after create: %v", err)
	}
	if len(storedRefs) != len(refs) {
		t.Fatalf("refs = %d, want %d", len(storedRefs), len(refs))
	}
	for i, ref := range storedRefs {
		want := refs[i]
		if ref.IssueID != created.ID || ref.Kind != want.Kind || ref.RefID != want.RefID ||
			ref.Role != want.Role || ref.ID <= 0 || ref.CreatedAt.IsZero() {
			t.Fatalf("ref[%d] = %+v, want kind=%s ref_id=%d", i, ref, want.Kind, want.RefID)
		}
	}

	history, err := repo.ListHistory(ctx, created.ID)
	if err != nil {
		t.Fatalf("ListHistory after create: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("creation history rows = %d, want 1", len(history))
	}
	if history[0].From != nil || history[0].To != delivery.IssueStatusOpen {
		t.Fatalf("creation history = %+v, want from=NULL to=open", history[0])
	}
	if history[0].ActorKind != "system" {
		t.Fatalf("creation actor kind = %q, want system", history[0].ActorKind)
	}

	// The same dedupe key from a new case_run must reuse the issue but still
	// append the new logical ref, without duplicating the old rows/history.
	second, secondRefs := issueRepoBuildCandidate(t, issueRepoTestCandidate{
		caseRunID:     3002,
		waveID:        issueRepoNum(700),
		versionSUTID:  issueRepoNum(9001),
		requirementID: 1200,
	})
	existing, isNew, err := repo.CreateCandidate(ctx, second, secondRefs)
	if err != nil {
		t.Fatalf("second CreateCandidate: %v", err)
	}
	if isNew {
		t.Fatal("second candidate with the same dedupe key must report created=false")
	}
	if existing.ID != created.ID {
		t.Fatalf("dedupe returned issue %d, want %d", existing.ID, created.ID)
	}
	if !existing.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("dedupe changed created_at: %v != %v", existing.CreatedAt, created.CreatedAt)
	}

	storedRefs, err = repo.ListRefs(ctx, created.ID)
	if err != nil {
		t.Fatalf("ListRefs after dedupe: %v", err)
	}
	if len(storedRefs) != len(refs)+1 {
		t.Fatalf("refs after dedupe = %d, want %d (new case_run only)", len(storedRefs), len(refs)+1)
	}
	history, err = repo.ListHistory(ctx, created.ID)
	if err != nil {
		t.Fatalf("ListHistory after dedupe: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("dedupe appended history: %d rows, want 1", len(history))
	}

	// Replaying the exact candidate is fully idempotent.
	if _, isNew, err = repo.CreateCandidate(ctx, second, secondRefs); err != nil || isNew {
		t.Fatalf("exact replay = created:%v err:%v, want created:false err:nil", isNew, err)
	}
	storedRefs, err = repo.ListRefs(ctx, created.ID)
	if err != nil {
		t.Fatalf("ListRefs after replay: %v", err)
	}
	if len(storedRefs) != len(refs)+1 {
		t.Fatalf("refs after replay = %d, want %d", len(storedRefs), len(refs)+1)
	}
	history, err = repo.ListHistory(ctx, created.ID)
	if err != nil || len(history) != 1 {
		t.Fatalf("history after replay = %d err=%v, want 1 nil", len(history), err)
	}

	// Both case_runs resolve to the deduplicated issue.
	for _, caseRunID := range []int64{3001, 3002} {
		found, ok, err := repo.FindByCaseRun(ctx, 100, caseRunID)
		if err != nil {
			t.Fatalf("FindByCaseRun(%d): %v", caseRunID, err)
		}
		if !ok || found.ID != created.ID {
			t.Fatalf("FindByCaseRun(%d) = %+v ok=%v, want issue %d", caseRunID, found, ok, created.ID)
		}
	}
	if _, ok, err := repo.FindByCaseRun(ctx, 200, 3001); err != nil || ok {
		t.Fatalf("FindByCaseRun other version = ok:%v err:%v, want false nil", ok, err)
	}
	if _, ok, err := repo.FindByCaseRun(ctx, 100, 999999); err != nil || ok {
		t.Fatalf("FindByCaseRun missing = ok:%v err:%v, want false nil", ok, err)
	}

	// Get is version scoped, GetByID is not.
	got, err := repo.Get(ctx, 100, created.ID)
	if err != nil || got.ID != created.ID {
		t.Fatalf("Get = %+v err=%v", got, err)
	}
	if _, err := repo.Get(ctx, 200, created.ID); !errors.Is(err, delivery.ErrNotFound) {
		t.Fatalf("Get wrong version error = %v, want delivery.ErrNotFound", err)
	}
	byID, err := repo.GetByID(ctx, created.ID)
	if err != nil || byID.ID != created.ID || byID.VersionID != 100 {
		t.Fatalf("GetByID = %+v err=%v", byID, err)
	}
	if _, err := repo.GetByID(ctx, 999999); !errors.Is(err, delivery.ErrNotFound) {
		t.Fatalf("GetByID missing error = %v, want delivery.ErrNotFound", err)
	}
}

// TestIssueRepoNotFoundSentinel pins the miss classification of every
// read/write path: the returned error must satisfy errors.Is against the
// delivery domain sentinel (so callers never import this adapter) and must
// still unwrap to pgx.ErrNoRows. The adapter-level ErrNotFound stays usable for
// compatibility.
func TestIssueRepoNotFoundSentinel(t *testing.T) {
	repo, _ := newIssueRepoForTest(t)
	ctx := context.Background()

	_, getErr := repo.Get(ctx, 100, 999999)
	if !errors.Is(getErr, delivery.ErrNotFound) {
		t.Fatalf("Get missing error = %v, want errors.Is(delivery.ErrNotFound)", getErr)
	}
	if !errors.Is(getErr, pgx.ErrNoRows) {
		t.Fatalf("Get missing error = %v, want errors.Is(pgx.ErrNoRows)", getErr)
	}
	if !errors.Is(getErr, ErrNotFound) {
		t.Fatalf("Get missing error = %v, want compatibility errors.Is(ErrNotFound)", getErr)
	}

	_, byIDErr := repo.GetByID(ctx, 999999)
	if !errors.Is(byIDErr, delivery.ErrNotFound) {
		t.Fatalf("GetByID missing error = %v, want errors.Is(delivery.ErrNotFound)", byIDErr)
	}
	if !errors.Is(byIDErr, pgx.ErrNoRows) {
		t.Fatalf("GetByID missing error = %v, want errors.Is(pgx.ErrNoRows)", byIDErr)
	}

	_, updateErr := repo.UpdateStatus(ctx, deliveryapp.IssueStatusUpdate{
		VersionID: 100,
		IssueID:   999999,
		To:        delivery.IssueStatusConfirmed,
		ActorKind: "user",
	})
	if !errors.Is(updateErr, delivery.ErrNotFound) {
		t.Fatalf("UpdateStatus missing error = %v, want errors.Is(delivery.ErrNotFound)", updateErr)
	}
	if !errors.Is(updateErr, pgx.ErrNoRows) {
		t.Fatalf("UpdateStatus missing error = %v, want errors.Is(pgx.ErrNoRows)", updateErr)
	}

	_, mergeErr := repo.Merge(ctx, deliveryapp.IssueMerge{
		VersionID: 100,
		SourceID:  999999,
		TargetID:  888888,
		ActorID:   55,
	})
	if !errors.Is(mergeErr, delivery.ErrNotFound) {
		t.Fatalf("Merge missing source error = %v, want errors.Is(delivery.ErrNotFound)", mergeErr)
	}
	if !errors.Is(mergeErr, pgx.ErrNoRows) {
		t.Fatalf("Merge missing source error = %v, want errors.Is(pgx.ErrNoRows)", mergeErr)
	}
}

// TestIssueRepoUpdateStatusCAS covers the expected_revision CAS, reopen_count
// accounting and append-only history of status transitions.
func TestIssueRepoUpdateStatusCAS(t *testing.T) {
	repo, _ := newIssueRepoForTest(t)
	ctx := context.Background()
	issue := issueRepoCreateCandidate(t, repo, issueRepoTestCandidate{})

	confirmed, err := repo.UpdateStatus(ctx, deliveryapp.IssueStatusUpdate{
		VersionID:        100,
		IssueID:          issue.ID,
		To:               delivery.IssueStatusConfirmed,
		Reason:           "reproduced",
		ActorID:          77,
		ActorKind:        "user",
		ExpectedRevision: issueRepoRevision(1),
	})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if confirmed.Status != delivery.IssueStatusConfirmed || confirmed.ExpectedRevision != 2 || confirmed.ReopenCount != 0 {
		t.Fatalf("confirmed issue = %+v", confirmed)
	}
	if confirmed.UpdatedAt.Before(issue.UpdatedAt) {
		t.Fatalf("updated_at moved backwards: %v < %v", confirmed.UpdatedAt, issue.UpdatedAt)
	}
	history, err := repo.ListHistory(ctx, issue.ID)
	if err != nil || len(history) != 2 {
		t.Fatalf("history after confirm = %d err=%v", len(history), err)
	}
	transition := history[1]
	if transition.From == nil || *transition.From != delivery.IssueStatusOpen ||
		transition.To != delivery.IssueStatusConfirmed || transition.Reason != "reproduced" ||
		transition.ActorID == nil || *transition.ActorID != 77 || transition.ActorKind != "user" {
		t.Fatalf("confirm history = %+v", transition)
	}

	// A stale expected_revision is rejected and leaves the row untouched.
	if _, err := repo.UpdateStatus(ctx, deliveryapp.IssueStatusUpdate{
		VersionID:        100,
		IssueID:          issue.ID,
		To:               delivery.IssueStatusDismissed,
		ActorID:          78,
		ActorKind:        "user",
		ExpectedRevision: issueRepoRevision(1),
	}); !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("stale CAS error = %v, want ErrStateConflict", err)
	}
	afterStale, err := repo.Get(ctx, 100, issue.ID)
	if err != nil || afterStale.Status != delivery.IssueStatusConfirmed || afterStale.ExpectedRevision != 2 {
		t.Fatalf("stale CAS changed row: %+v err=%v", afterStale, err)
	}

	// confirmed -> open increments reopen_count and moves expected_revision.
	reopened, err := repo.UpdateStatus(ctx, deliveryapp.IssueStatusUpdate{
		VersionID:        100,
		IssueID:          issue.ID,
		To:               delivery.IssueStatusOpen,
		Reason:           "still failing on main",
		ActorID:          88,
		ActorKind:        "user",
		ExpectedRevision: issueRepoRevision(2),
	})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.Status != delivery.IssueStatusOpen || reopened.ExpectedRevision != 3 || reopened.ReopenCount != 1 {
		t.Fatalf("reopened issue = %+v", reopened)
	}
	history, err = repo.ListHistory(ctx, issue.ID)
	if err != nil || len(history) != 3 {
		t.Fatalf("history after reopen = %d err=%v", len(history), err)
	}
	if last := history[2]; last.From == nil || *last.From != delivery.IssueStatusConfirmed ||
		last.To != delivery.IssueStatusOpen || last.Reason != "still failing on main" {
		t.Fatalf("reopen history = %+v", last)
	}

	// open -> open is illegal even without expected_revision: the repository
	// re-applies the state machine under the row lock, so the rejected update
	// leaves the status, revision, reopen_count and history untouched.
	if _, err := repo.UpdateStatus(ctx, deliveryapp.IssueStatusUpdate{
		VersionID: 100,
		IssueID:   issue.ID,
		To:        delivery.IssueStatusOpen,
		Reason:    "duplicate reopen",
		ActorID:   88,
		ActorKind: "user",
	}); !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("open -> open error = %v, want ErrStateConflict", err)
	}
	unchanged, err := repo.Get(ctx, 100, issue.ID)
	if err != nil || unchanged.Status != delivery.IssueStatusOpen ||
		unchanged.ExpectedRevision != 3 || unchanged.ReopenCount != 1 {
		t.Fatalf("illegal open -> open changed row: %+v err=%v", unchanged, err)
	}
	if history, err = repo.ListHistory(ctx, issue.ID); err != nil || len(history) != 3 {
		t.Fatalf("illegal open -> open history = %d err=%v, want 3", len(history), err)
	}

	// dismiss -> open increments again; expected_revision is optional.
	dismissed, err := repo.UpdateStatus(ctx, deliveryapp.IssueStatusUpdate{
		VersionID: 100,
		IssueID:   issue.ID,
		To:        delivery.IssueStatusDismissed,
		Reason:    "not a defect",
		ActorID:   89,
		ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	if dismissed.Status != delivery.IssueStatusDismissed || dismissed.ExpectedRevision != 4 || dismissed.ReopenCount != 1 {
		t.Fatalf("dismissed issue = %+v", dismissed)
	}
	reopenedThird, err := repo.UpdateStatus(ctx, deliveryapp.IssueStatusUpdate{
		VersionID: 100,
		IssueID:   issue.ID,
		To:        delivery.IssueStatusOpen,
		Reason:    "customer reported again",
		ActorID:   90,
		ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("third reopen: %v", err)
	}
	if reopenedThird.ExpectedRevision != 5 || reopenedThird.ReopenCount != 2 {
		t.Fatalf("third reopen issue = %+v", reopenedThird)
	}

	// Missing issues and cross-version updates are not found.
	if _, err := repo.UpdateStatus(ctx, deliveryapp.IssueStatusUpdate{
		VersionID: 100,
		IssueID:   999999,
		To:        delivery.IssueStatusConfirmed,
		ActorKind: "user",
	}); !errors.Is(err, delivery.ErrNotFound) {
		t.Fatalf("missing issue update error = %v, want delivery.ErrNotFound", err)
	}
	if _, err := repo.UpdateStatus(ctx, deliveryapp.IssueStatusUpdate{
		VersionID: 200,
		IssueID:   issue.ID,
		To:        delivery.IssueStatusConfirmed,
		ActorKind: "user",
	}); !errors.Is(err, delivery.ErrNotFound) {
		t.Fatalf("cross-version update error = %v, want delivery.ErrNotFound", err)
	}
}

// issueRepoRunConcurrentUpdates starts one UpdateStatus per target at the same
// time and returns one error per call. expected_revision stays nil so the race
// is decided by the row lock and the state machine, not by a CAS miss.
func issueRepoRunConcurrentUpdates(repo *IssueRepository, issueID int64, targets ...delivery.IssueStatus) []error {
	start := make(chan struct{})
	errs := make([]error, len(targets))
	var wg sync.WaitGroup
	for i, to := range targets {
		wg.Add(1)
		go func(i int, to delivery.IssueStatus) {
			defer wg.Done()
			<-start
			_, errs[i] = repo.UpdateStatus(context.Background(), deliveryapp.IssueStatusUpdate{
				VersionID: 100,
				IssueID:   issueID,
				To:        to,
				Reason:    "concurrent transition",
				ActorID:   91,
				ActorKind: "user",
			})
		}(i, to)
	}
	close(start)
	wg.Wait()
	return errs
}

// TestIssueRepoUpdateStatusIllegalTransitions pins the state-machine guard of
// the update path: every illegal transition is ErrStateConflict and is rejected
// before any write (no revision bump, no history row).
func TestIssueRepoUpdateStatusIllegalTransitions(t *testing.T) {
	repo, _ := newIssueRepoForTest(t)
	ctx := context.Background()

	openIssue := issueRepoCreateCandidate(t, repo, issueRepoTestCandidate{caseRunID: 3401, caseVersionID: 4401})
	if _, err := repo.UpdateStatus(ctx, deliveryapp.IssueStatusUpdate{
		VersionID: 100, IssueID: openIssue.ID, To: delivery.IssueStatusOpen, ActorKind: "user",
	}); !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("open -> open error = %v, want ErrStateConflict", err)
	}
	stored, err := repo.Get(ctx, 100, openIssue.ID)
	if err != nil || stored.Status != delivery.IssueStatusOpen || stored.ExpectedRevision != 1 {
		t.Fatalf("open -> open changed issue: %+v err=%v", stored, err)
	}

	confirmed := issueRepoCreateCandidate(t, repo, issueRepoTestCandidate{caseRunID: 3402, caseVersionID: 4402})
	if _, err := repo.UpdateStatus(ctx, deliveryapp.IssueStatusUpdate{
		VersionID: 100, IssueID: confirmed.ID, To: delivery.IssueStatusConfirmed, ActorKind: "user",
	}); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	for _, to := range []delivery.IssueStatus{delivery.IssueStatusDismissed, delivery.IssueStatusConfirmed} {
		if _, err := repo.UpdateStatus(ctx, deliveryapp.IssueStatusUpdate{
			VersionID: 100, IssueID: confirmed.ID, To: to, ActorKind: "user",
		}); !errors.Is(err, delivery.ErrStateConflict) {
			t.Fatalf("confirmed -> %s error = %v, want ErrStateConflict", to, err)
		}
	}
	stored, err = repo.Get(ctx, 100, confirmed.ID)
	if err != nil || stored.Status != delivery.IssueStatusConfirmed || stored.ExpectedRevision != 2 {
		t.Fatalf("confirmed illegal transitions changed issue: %+v err=%v", stored, err)
	}
	if history, err := repo.ListHistory(ctx, confirmed.ID); err != nil || len(history) != 2 {
		t.Fatalf("confirmed illegal transitions history = %d err=%v, want 2", len(history), err)
	}

	target := issueRepoCreateCandidate(t, repo, issueRepoTestCandidate{caseRunID: 3403, caseVersionID: 4403})
	merged := issueRepoCreateCandidate(t, repo, issueRepoTestCandidate{caseRunID: 3404, caseVersionID: 4404})
	if _, err := repo.Merge(ctx, deliveryapp.IssueMerge{
		VersionID: 100, SourceID: merged.ID, TargetID: target.ID, ActorID: 55,
	}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	for _, to := range []delivery.IssueStatus{
		delivery.IssueStatusOpen,
		delivery.IssueStatusConfirmed,
		delivery.IssueStatusDismissed,
		delivery.IssueStatusMerged,
	} {
		if _, err := repo.UpdateStatus(ctx, deliveryapp.IssueStatusUpdate{
			VersionID: 100, IssueID: merged.ID, To: to, ActorKind: "user",
		}); !errors.Is(err, delivery.ErrStateConflict) {
			t.Fatalf("merged -> %s error = %v, want ErrStateConflict", to, err)
		}
	}
	stored, err = repo.Get(ctx, 100, merged.ID)
	if err != nil || stored.Status != delivery.IssueStatusMerged || stored.ExpectedRevision != 2 {
		t.Fatalf("merged terminal updates changed issue: %+v err=%v", stored, err)
	}
	if history, err := repo.ListHistory(ctx, merged.ID); err != nil || len(history) != 2 {
		t.Fatalf("merged terminal history = %d err=%v, want 2", len(history), err)
	}
}

// TestIssueRepoUpdateStatusConcurrentIllegalTransitions covers the lock-time
// state machine under racing writers: illegal losers are state conflicts and
// only a legal winner is persisted. expected_revision is deliberately absent
// so the row lock, not the CAS, must reject the loser.
func TestIssueRepoUpdateStatusConcurrentIllegalTransitions(t *testing.T) {
	t.Run("open to open", func(t *testing.T) {
		repo, _ := newIssueRepoForTest(t)
		ctx := context.Background()
		issue := issueRepoCreateCandidate(t, repo, issueRepoTestCandidate{caseRunID: 3411, caseVersionID: 4411})
		for i, err := range issueRepoRunConcurrentUpdates(repo, issue.ID, delivery.IssueStatusOpen, delivery.IssueStatusOpen) {
			if !errors.Is(err, delivery.ErrStateConflict) {
				t.Fatalf("open -> open [%d] error = %v, want ErrStateConflict", i, err)
			}
		}
		stored, err := repo.Get(ctx, 100, issue.ID)
		if err != nil || stored.Status != delivery.IssueStatusOpen || stored.ExpectedRevision != 1 || stored.ReopenCount != 0 {
			t.Fatalf("concurrent open -> open changed issue: %+v err=%v", stored, err)
		}
		if history, err := repo.ListHistory(ctx, issue.ID); err != nil || len(history) != 1 {
			t.Fatalf("concurrent open -> open history = %d err=%v, want 1", len(history), err)
		}
	})

	t.Run("merged terminal", func(t *testing.T) {
		repo, _ := newIssueRepoForTest(t)
		ctx := context.Background()
		source := issueRepoCreateCandidate(t, repo, issueRepoTestCandidate{caseRunID: 3412, caseVersionID: 4412})
		target := issueRepoCreateCandidate(t, repo, issueRepoTestCandidate{caseRunID: 3413, caseVersionID: 4413})
		if _, err := repo.Merge(ctx, deliveryapp.IssueMerge{
			VersionID: 100, SourceID: source.ID, TargetID: target.ID, ActorID: 55,
		}); err != nil {
			t.Fatalf("merge: %v", err)
		}
		for i, err := range issueRepoRunConcurrentUpdates(repo, source.ID, delivery.IssueStatusOpen, delivery.IssueStatusConfirmed) {
			if !errors.Is(err, delivery.ErrStateConflict) {
				t.Fatalf("merged terminal [%d] error = %v, want ErrStateConflict", i, err)
			}
		}
		stored, err := repo.Get(ctx, 100, source.ID)
		if err != nil || stored.Status != delivery.IssueStatusMerged || stored.ExpectedRevision != 2 {
			t.Fatalf("concurrent merged terminal changed issue: %+v err=%v", stored, err)
		}
		if history, err := repo.ListHistory(ctx, source.ID); err != nil || len(history) != 2 {
			t.Fatalf("concurrent merged terminal history = %d err=%v, want 2", len(history), err)
		}
	})

	t.Run("confirm versus dismiss without expected revision", func(t *testing.T) {
		repo, _ := newIssueRepoForTest(t)
		ctx := context.Background()
		issue := issueRepoCreateCandidate(t, repo, issueRepoTestCandidate{caseRunID: 3414, caseVersionID: 4414})
		errs := issueRepoRunConcurrentUpdates(repo, issue.ID, delivery.IssueStatusConfirmed, delivery.IssueStatusDismissed)
		var succeeded, conflicted int
		for i, err := range errs {
			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, delivery.ErrStateConflict):
				conflicted++
			default:
				t.Fatalf("confirm/dismiss [%d] error = %v, want nil or ErrStateConflict", i, err)
			}
		}
		if succeeded != 1 || conflicted != 1 {
			t.Fatalf("confirm/dismiss race = %d success %d conflict, want 1/1 (errors: %v)", succeeded, conflicted, errs)
		}
		stored, err := repo.Get(ctx, 100, issue.ID)
		if err != nil || stored.ExpectedRevision != 2 || stored.ReopenCount != 0 {
			t.Fatalf("confirm/dismiss race issue = %+v err=%v", stored, err)
		}
		if stored.Status != delivery.IssueStatusConfirmed && stored.Status != delivery.IssueStatusDismissed {
			t.Fatalf("confirm/dismiss race status = %q", stored.Status)
		}
		if history, err := repo.ListHistory(ctx, issue.ID); err != nil || len(history) != 2 {
			t.Fatalf("confirm/dismiss race history = %d err=%v, want 2", len(history), err)
		}
	})
}

// TestIssueRepoMerge covers the source->target merge write, its history and the
// rejection of terminal/cross-version/stale merges.
func TestIssueRepoMerge(t *testing.T) {
	repo, pool := newIssueRepoForTest(t)
	ctx := context.Background()

	source := issueRepoCreateCandidate(t, repo, issueRepoTestCandidate{caseRunID: 3101, caseVersionID: 4101})
	target := issueRepoCreateCandidate(t, repo, issueRepoTestCandidate{caseRunID: 3102, caseVersionID: 4102})

	merged, err := repo.Merge(ctx, deliveryapp.IssueMerge{
		VersionID:        100,
		SourceID:         source.ID,
		TargetID:         target.ID,
		ActorID:          55,
		Reason:           "duplicate of target",
		ExpectedRevision: issueRepoRevision(1),
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if merged.Status != delivery.IssueStatusMerged {
		t.Fatalf("merged status = %q, want merged", merged.Status)
	}
	if merged.MergedIntoID == nil || *merged.MergedIntoID != target.ID {
		t.Fatalf("merged_into = %v, want %d", merged.MergedIntoID, target.ID)
	}
	if merged.MergedAt == nil || merged.MergedAt.IsZero() {
		t.Fatal("merged_at must be set")
	}
	if merged.MergedBy == nil || *merged.MergedBy != 55 {
		t.Fatalf("merged_by = %v, want 55", merged.MergedBy)
	}
	if merged.ExpectedRevision != 2 || merged.ReopenCount != 0 {
		t.Fatalf("merged revision/reopen = %d/%d", merged.ExpectedRevision, merged.ReopenCount)
	}
	history, err := repo.ListHistory(ctx, source.ID)
	if err != nil || len(history) != 2 {
		t.Fatalf("merge history = %d err=%v, want 2", len(history), err)
	}
	if last := history[1]; last.From == nil || *last.From != delivery.IssueStatusOpen ||
		last.To != delivery.IssueStatusMerged || last.Reason != "duplicate of target" ||
		last.ActorID == nil || *last.ActorID != 55 || last.ActorKind != "user" {
		t.Fatalf("merge history = %+v", last)
	}
	if kept, err := repo.Get(ctx, 100, target.ID); err != nil || kept.Status != delivery.IssueStatusOpen {
		t.Fatalf("target changed by merge: %+v err=%v", kept, err)
	}

	// merged is terminal: a second merge is a state conflict.
	if _, err := repo.Merge(ctx, deliveryapp.IssueMerge{
		VersionID: 100, SourceID: source.ID, TargetID: target.ID, ActorID: 55,
	}); !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("second merge error = %v, want ErrStateConflict", err)
	}

	// A merged issue cannot be a merge target.
	openSource := issueRepoCreateCandidate(t, repo, issueRepoTestCandidate{caseRunID: 3103, caseVersionID: 4103})
	if _, err := repo.Merge(ctx, deliveryapp.IssueMerge{
		VersionID: 100, SourceID: openSource.ID, TargetID: source.ID, ActorID: 55,
	}); !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("merge into merged target error = %v, want ErrStateConflict", err)
	}

	// Cross-version and self targets are rejected.
	otherTarget := issueRepoCreateCandidate(t, repo, issueRepoTestCandidate{
		versionID: 200, caseRunID: 3201, caseVersionID: 4201,
	})
	crossSource := issueRepoCreateCandidate(t, repo, issueRepoTestCandidate{caseRunID: 3104, caseVersionID: 4104})
	if _, err := repo.Merge(ctx, deliveryapp.IssueMerge{
		VersionID: 100, SourceID: crossSource.ID, TargetID: otherTarget.ID, ActorID: 55,
	}); !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("cross-version merge error = %v, want ErrStateConflict", err)
	}
	if kept, _ := repo.Get(ctx, 100, crossSource.ID); kept.Status != delivery.IssueStatusOpen {
		t.Fatalf("failed cross-version merge changed source: %+v", kept)
	}
	selfSource := issueRepoCreateCandidate(t, repo, issueRepoTestCandidate{caseRunID: 3105, caseVersionID: 4105})
	if _, err := repo.Merge(ctx, deliveryapp.IssueMerge{
		VersionID: 100, SourceID: selfSource.ID, TargetID: selfSource.ID, ActorID: 55,
	}); !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("self merge error = %v, want ErrStateConflict", err)
	}

	// Missing target and missing source are rejected without writes.
	missingTargetSource := issueRepoCreateCandidate(t, repo, issueRepoTestCandidate{caseRunID: 3106, caseVersionID: 4106})
	if _, err := repo.Merge(ctx, deliveryapp.IssueMerge{
		VersionID: 100, SourceID: missingTargetSource.ID, TargetID: 999999, ActorID: 55,
	}); !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("missing target merge error = %v, want ErrStateConflict", err)
	}
	if _, err := repo.Merge(ctx, deliveryapp.IssueMerge{
		VersionID: 100, SourceID: 999999, TargetID: target.ID, ActorID: 55,
	}); !errors.Is(err, delivery.ErrNotFound) {
		t.Fatalf("missing source merge error = %v, want delivery.ErrNotFound", err)
	}

	// A stale expected_revision is rejected and leaves the source open.
	staleSource := issueRepoCreateCandidate(t, repo, issueRepoTestCandidate{caseRunID: 3107, caseVersionID: 4107})
	if _, err := repo.Merge(ctx, deliveryapp.IssueMerge{
		VersionID: 100, SourceID: staleSource.ID, TargetID: target.ID, ActorID: 56,
		ExpectedRevision: issueRepoRevision(2),
	}); !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("stale merge error = %v, want ErrStateConflict", err)
	}
	if kept, _ := repo.Get(ctx, 100, staleSource.ID); kept.Status != delivery.IssueStatusOpen || kept.ExpectedRevision != 1 {
		t.Fatalf("stale merge changed source: %+v", kept)
	}
	if merged, err := repo.Merge(ctx, deliveryapp.IssueMerge{
		VersionID: 100, SourceID: staleSource.ID, TargetID: target.ID, ActorID: 56,
	}); err != nil || merged.Status != delivery.IssueStatusMerged {
		t.Fatalf("merge without expected revision = %+v err=%v", merged, err)
	}

	// The source refs are not migrated, but merged_into stays inside the
	// same version.
	var versionID int64
	if err := pool.QueryRow(ctx, `SELECT version_id FROM delivery_issue WHERE id = $1`, source.ID).Scan(&versionID); err != nil {
		t.Fatalf("read merged source: %v", err)
	}
	if versionID != 100 {
		t.Fatalf("merged source version = %d, want 100", versionID)
	}
}

// TestIssueRepoList covers status/category/severity/SUT filters and descending
// ID pagination.
func TestIssueRepoList(t *testing.T) {
	repo, _ := newIssueRepoForTest(t)
	ctx := context.Background()

	first := issueRepoCreateCandidate(t, repo, issueRepoTestCandidate{
		caseRunID: 3301, caseVersionID: 4301, category: "ui", severity: "major",
		versionSUTID: issueRepoNum(9001),
	})
	second := issueRepoCreateCandidate(t, repo, issueRepoTestCandidate{
		caseRunID: 3302, caseVersionID: 4302, category: "api", severity: "minor",
		versionSUTID: issueRepoNum(9002),
	})
	third := issueRepoCreateCandidate(t, repo, issueRepoTestCandidate{
		caseRunID: 3303, caseVersionID: 4303, category: "api", severity: "major",
		versionSUTID: issueRepoNum(9002),
	})
	otherVersion := issueRepoCreateCandidate(t, repo, issueRepoTestCandidate{
		versionID: 200, caseRunID: 3304, caseVersionID: 4304, category: "api", severity: "major",
		versionSUTID: issueRepoNum(9001),
	})

	for _, item := range []struct {
		issue  delivery.Issue
		status delivery.IssueStatus
	}{
		{second, delivery.IssueStatusConfirmed},
		{third, delivery.IssueStatusDismissed},
		{otherVersion, delivery.IssueStatusDismissed},
	} {
		updated, err := repo.UpdateStatus(ctx, deliveryapp.IssueStatusUpdate{
			VersionID: item.issue.VersionID,
			IssueID:   item.issue.ID,
			To:        item.status,
			ActorID:   1,
			ActorKind: "user",
		})
		if err != nil {
			t.Fatalf("set issue %d to %s: %v", item.issue.ID, item.status, err)
		}
		if updated.Status != item.status {
			t.Fatalf("issue %d status = %s, want %s", item.issue.ID, updated.Status, item.status)
		}
	}

	ids := func(items []delivery.Issue) []int64 {
		result := make([]int64, 0, len(items))
		for _, item := range items {
			result = append(result, item.ID)
		}
		return result
	}
	assertIDs := func(name string, got []delivery.Issue, want ...int64) {
		t.Helper()
		gotIDs := ids(got)
		if len(gotIDs) != len(want) {
			t.Fatalf("%s ids = %v, want %v", name, gotIDs, want)
		}
		for i := range want {
			if gotIDs[i] != want[i] {
				t.Fatalf("%s ids = %v, want %v", name, gotIDs, want)
			}
		}
	}

	all, err := repo.List(ctx, 100, deliveryapp.IssueFilter{Limit: 10})
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	assertIDs("all", all, third.ID, second.ID, first.ID)

	limited, err := repo.List(ctx, 100, deliveryapp.IssueFilter{Limit: 2})
	if err != nil {
		t.Fatalf("List limit: %v", err)
	}
	assertIDs("limit 2", limited, third.ID, second.ID)

	paged, err := repo.List(ctx, 100, deliveryapp.IssueFilter{Before: third.ID, Limit: 10})
	if err != nil {
		t.Fatalf("List before: %v", err)
	}
	assertIDs("before", paged, second.ID, first.ID)

	open, err := repo.List(ctx, 100, deliveryapp.IssueFilter{Status: "open", Limit: 10})
	if err != nil {
		t.Fatalf("List open: %v", err)
	}
	assertIDs("open", open, first.ID)

	confirmed, err := repo.List(ctx, 100, deliveryapp.IssueFilter{Status: "confirmed", Limit: 10})
	if err != nil {
		t.Fatalf("List confirmed: %v", err)
	}
	assertIDs("confirmed", confirmed, second.ID)

	major, err := repo.List(ctx, 100, deliveryapp.IssueFilter{Severity: "major", Limit: 10})
	if err != nil {
		t.Fatalf("List major: %v", err)
	}
	assertIDs("major", major, third.ID, first.ID)

	api, err := repo.List(ctx, 100, deliveryapp.IssueFilter{Category: "api", Limit: 10})
	if err != nil {
		t.Fatalf("List api: %v", err)
	}
	assertIDs("api", api, third.ID, second.ID)

	bySUT, err := repo.List(ctx, 100, deliveryapp.IssueFilter{VersionSUTID: issueRepoNum(9001), Limit: 10})
	if err != nil {
		t.Fatalf("List version SUT: %v", err)
	}
	assertIDs("version SUT", bySUT, first.ID)

	combined, err := repo.List(ctx, 100, deliveryapp.IssueFilter{
		Status: "confirmed", Category: "api", Severity: "minor", Limit: 10,
	})
	if err != nil {
		t.Fatalf("List combined: %v", err)
	}
	assertIDs("combined", combined, second.ID)

	empty, err := repo.List(ctx, 100, deliveryapp.IssueFilter{Category: "db", Limit: 10})
	if err != nil {
		t.Fatalf("List empty: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("List db = %v, want empty", ids(empty))
	}

	other, err := repo.List(ctx, 200, deliveryapp.IssueFilter{Limit: 10})
	if err != nil {
		t.Fatalf("List other version: %v", err)
	}
	assertIDs("other version", other, otherVersion.ID)

	defaultPage, err := repo.List(ctx, 100, deliveryapp.IssueFilter{})
	if err != nil {
		t.Fatalf("List default limit: %v", err)
	}
	assertIDs("default limit", defaultPage, third.ID, second.ID, first.ID)
}

// TestIssueRepoAppendHistory covers the explicit append-only history write.
func TestIssueRepoAppendHistory(t *testing.T) {
	repo, _ := newIssueRepoForTest(t)
	ctx := context.Background()
	issue := issueRepoCreateCandidate(t, repo, issueRepoTestCandidate{})

	from := delivery.IssueStatusOpen
	actorID := int64(9)
	if err := repo.AppendHistory(ctx, delivery.IssueStatusChange{
		IssueID:   issue.ID,
		From:      &from,
		To:        delivery.IssueStatusConfirmed,
		Reason:    "manual verification",
		ActorID:   &actorID,
		ActorKind: "agent",
	}); err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}
	history, err := repo.ListHistory(ctx, issue.ID)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history rows = %d, want 2", len(history))
	}
	appended := history[1]
	if appended.ID <= history[0].ID {
		t.Fatalf("history not in ascending ID order: %d then %d", history[0].ID, appended.ID)
	}
	if appended.From == nil || *appended.From != from || appended.To != delivery.IssueStatusConfirmed ||
		appended.Reason != "manual verification" || appended.ActorID == nil || *appended.ActorID != 9 ||
		appended.ActorKind != "agent" {
		t.Fatalf("appended history = %+v", appended)
	}
	if appended.CreatedAt.IsZero() || appended.UpdatedAt.IsZero() {
		t.Fatal("appended history timestamps must be assigned")
	}

	if err := repo.AppendHistory(ctx, delivery.IssueStatusChange{
		IssueID:   999999,
		To:        delivery.IssueStatusOpen,
		ActorKind: "system",
	}); err == nil {
		t.Fatal("AppendHistory on a missing issue must fail")
	}
}

// issueRepoRecurrenceRefs clones candidate refs with the recurrence role.
func issueRepoRecurrenceRefs(refs []delivery.IssueRef) []delivery.IssueRef {
	recurrence := make([]delivery.IssueRef, 0, len(refs))
	for _, ref := range refs {
		ref.Role = delivery.IssueRefRoleRecurrence
		recurrence = append(recurrence, ref)
	}
	return recurrence
}

// TestIssueRepoRecurrence covers the P7 recurrence write: the merged dedupe hit
// must not touch the source refs, ApplyRecurrence appends role='recurrence' refs
// to the target, increments recurrence_count/last_recurred_at and appends the
// recurrence history row, and repeated writes stay idempotent for refs.
func TestIssueRepoRecurrence(t *testing.T) {
	repo, pool := newIssueRepoForTest(t)
	ctx := context.Background()

	source := issueRepoCreateCandidate(t, repo, issueRepoTestCandidate{caseRunID: 3301})
	target := issueRepoCreateCandidate(t, repo, issueRepoTestCandidate{caseRunID: 3302, caseVersionID: 4002})
	if source.RecurrenceCount != 0 || source.LastRecurredAt != nil {
		t.Fatalf("new issue recurrence fields = %d/%v, want 0/nil", source.RecurrenceCount, source.LastRecurredAt)
	}
	if _, err := repo.Merge(ctx, deliveryapp.IssueMerge{
		VersionID: 100, SourceID: source.ID, TargetID: target.ID, ActorID: 55,
	}); err != nil {
		t.Fatalf("merge: %v", err)
	}

	sourceRefsBefore, err := repo.ListRefs(ctx, source.ID)
	if err != nil {
		t.Fatalf("source refs before: %v", err)
	}
	sourceHistoryBefore, err := repo.ListHistory(ctx, source.ID)
	if err != nil {
		t.Fatalf("source history before: %v", err)
	}
	targetRefsBefore, err := repo.ListRefs(ctx, target.ID)
	if err != nil {
		t.Fatalf("target refs before: %v", err)
	}
	targetHistoryBefore, err := repo.ListHistory(ctx, target.ID)
	if err != nil {
		t.Fatalf("target history before: %v", err)
	}

	// A deduplicated failure on the merged source is a recurrence candidate. The
	// repository must not append those refs to the source: the service resolves
	// the target and applies the recurrence.
	recurring, recurringRefs := issueRepoBuildCandidate(t, issueRepoTestCandidate{caseRunID: 3303})
	stored, isNew, err := repo.CreateCandidate(ctx, recurring, recurringRefs)
	if err != nil {
		t.Fatalf("recurrence candidate: %v", err)
	}
	if isNew {
		t.Fatal("recurrence candidate reported created=true")
	}
	if stored.ID != source.ID || stored.Status != delivery.IssueStatusMerged {
		t.Fatalf("recurrence dedupe = %+v, want the merged source %d", stored, source.ID)
	}
	if stored.RecurrenceCount != 0 || stored.LastRecurredAt != nil {
		t.Fatalf("merged dedupe bumped recurrence fields: %+v", stored)
	}
	if refs, err := repo.ListRefs(ctx, source.ID); err != nil || len(refs) != len(sourceRefsBefore) {
		t.Fatalf("source refs after merged dedupe = %d err=%v, want unchanged %d",
			len(refs), err, len(sourceRefsBefore))
	}

	updated, err := repo.ApplyRecurrence(ctx, deliveryapp.IssueRecurrence{
		VersionID: 100,
		SourceID:  source.ID,
		TargetID:  target.ID,
		Refs:      issueRepoRecurrenceRefs(recurringRefs),
	})
	if err != nil {
		t.Fatalf("ApplyRecurrence: %v", err)
	}
	if updated.ID != target.ID || updated.RecurrenceCount != 1 || updated.LastRecurredAt == nil {
		t.Fatalf("recurred target = %+v, want count 1 and last_recurred_at", updated)
	}
	if updated.LastRecurredAt.IsZero() {
		t.Fatal("last_recurred_at must be a real timestamp")
	}
	// A recurrence is not a status transition: expected_revision stays put.
	if updated.ExpectedRevision != 1 || updated.Status != delivery.IssueStatusOpen {
		t.Fatalf("target revision/status = %d/%q, want 1/open", updated.ExpectedRevision, updated.Status)
	}

	targetRefs, err := repo.ListRefs(ctx, target.ID)
	if err != nil {
		t.Fatalf("target refs after: %v", err)
	}
	if len(targetRefs) != len(targetRefsBefore)+len(recurringRefs) {
		t.Fatalf("target refs = %d, want %d", len(targetRefs), len(targetRefsBefore)+len(recurringRefs))
	}
	recurrenceRoles := 0
	recurrenceCaseRun := false
	for _, ref := range targetRefs {
		if ref.Role != delivery.IssueRefRoleRecurrence {
			continue
		}
		recurrenceRoles++
		if ref.Kind == delivery.IssueRefKindCaseRun && ref.RefID == 3303 {
			recurrenceCaseRun = true
		}
	}
	if recurrenceRoles != len(recurringRefs) || !recurrenceCaseRun {
		t.Fatalf("target recurrence refs = %d case_run=%v, want %d and ref 3303",
			recurrenceRoles, recurrenceCaseRun, len(recurringRefs))
	}
	if refs, err := repo.ListRefs(ctx, source.ID); err != nil || len(refs) != len(sourceRefsBefore) {
		t.Fatalf("source refs after redirect = %d err=%v, want unchanged %d",
			len(refs), err, len(sourceRefsBefore))
	}
	if history, err := repo.ListHistory(ctx, source.ID); err != nil || len(history) != len(sourceHistoryBefore) {
		t.Fatalf("source history after redirect = %d err=%v, want unchanged %d",
			len(history), err, len(sourceHistoryBefore))
	}
	if sourceStored, err := repo.Get(ctx, 100, source.ID); err != nil || sourceStored.RecurrenceCount != 0 ||
		sourceStored.LastRecurredAt != nil {
		t.Fatalf("source issue changed by redirect: %+v err=%v", sourceStored, err)
	}

	targetHistory, err := repo.ListHistory(ctx, target.ID)
	if err != nil {
		t.Fatalf("target history after: %v", err)
	}
	if len(targetHistory) != len(targetHistoryBefore)+1 {
		t.Fatalf("target history = %d, want one recurrence row (%d)", len(targetHistory), len(targetHistoryBefore)+1)
	}
	recurrenceRow := targetHistory[len(targetHistory)-1]
	if recurrenceRow.From == nil || *recurrenceRow.From != delivery.IssueStatusOpen ||
		recurrenceRow.To != delivery.IssueStatusOpen ||
		recurrenceRow.Reason != "recurrence" || recurrenceRow.ActorKind != "system" ||
		recurrenceRow.ActorID != nil {
		t.Fatalf("recurrence history = %+v, want from=to=open reason=recurrence system", recurrenceRow)
	}

	// A repeated recurrence still increments the counter but the unique
	// (issue_id, kind, ref_id, role) key keeps the refs a single set.
	second, err := repo.ApplyRecurrence(ctx, deliveryapp.IssueRecurrence{
		VersionID: 100,
		SourceID:  source.ID,
		TargetID:  target.ID,
		Refs:      issueRepoRecurrenceRefs(recurringRefs),
	})
	if err != nil {
		t.Fatalf("second ApplyRecurrence: %v", err)
	}
	if second.RecurrenceCount != 2 {
		t.Fatalf("second recurrence_count = %d, want 2", second.RecurrenceCount)
	}
	if refs, err := repo.ListRefs(ctx, target.ID); err != nil || len(refs) != len(targetRefs) {
		t.Fatalf("target refs after repeated recurrence = %d err=%v, want unchanged %d",
			len(refs), err, len(targetRefs))
	}
	if history, err := repo.ListHistory(ctx, target.ID); err != nil || len(history) != len(targetHistory)+1 {
		t.Fatalf("target history after repeated recurrence = %d err=%v", len(history), err)
	}

	// last_recurred_at only moves forward (db-schema §16.7.7): a clock that
	// goes backwards must not regress it.
	future := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	if _, err := pool.Exec(ctx, `UPDATE delivery_issue SET last_recurred_at = $2 WHERE id = $1`,
		target.ID, future); err != nil {
		t.Fatalf("set future last_recurred_at: %v", err)
	}
	third, err := repo.ApplyRecurrence(ctx, deliveryapp.IssueRecurrence{
		VersionID: 100,
		SourceID:  source.ID,
		TargetID:  target.ID,
		Refs:      nil,
	})
	if err != nil {
		t.Fatalf("third ApplyRecurrence: %v", err)
	}
	if third.RecurrenceCount != 3 {
		t.Fatalf("third recurrence_count = %d, want 3", third.RecurrenceCount)
	}
	if third.LastRecurredAt == nil || !third.LastRecurredAt.Equal(future) {
		t.Fatalf("last_recurred_at = %v, want the later %v (monotonic)", third.LastRecurredAt, future)
	}
}

// TestIssueRepoApplyRecurrenceRejections covers the transactional target
// guards: a merged target and a cross-version target are rejected.
func TestIssueRepoApplyRecurrenceRejections(t *testing.T) {
	repo, _ := newIssueRepoForTest(t)
	ctx := context.Background()

	source := issueRepoCreateCandidate(t, repo, issueRepoTestCandidate{caseRunID: 3501})
	target := issueRepoCreateCandidate(t, repo, issueRepoTestCandidate{caseRunID: 3502, caseVersionID: 4002})
	if _, err := repo.Merge(ctx, deliveryapp.IssueMerge{
		VersionID: 100, SourceID: source.ID, TargetID: target.ID, ActorID: 55,
	}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	_, refs := issueRepoBuildCandidate(t, issueRepoTestCandidate{caseRunID: 3503})

	// The merged source cannot be a redirect target.
	if _, err := repo.ApplyRecurrence(ctx, deliveryapp.IssueRecurrence{
		VersionID: 100, SourceID: source.ID, TargetID: source.ID, Refs: refs,
	}); !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("merged target error = %v, want delivery.ErrStateConflict", err)
	}
	// A foreign version cannot receive the recurrence.
	if _, err := repo.ApplyRecurrence(ctx, deliveryapp.IssueRecurrence{
		VersionID: 200, SourceID: source.ID, TargetID: target.ID, Refs: refs,
	}); !errors.Is(err, delivery.ErrNotFound) {
		t.Fatalf("cross-version target error = %v, want delivery.ErrNotFound", err)
	}
	// A missing target is not found.
	if _, err := repo.ApplyRecurrence(ctx, deliveryapp.IssueRecurrence{
		VersionID: 100, SourceID: source.ID, TargetID: 999999, Refs: refs,
	}); !errors.Is(err, delivery.ErrNotFound) {
		t.Fatalf("missing target error = %v, want delivery.ErrNotFound", err)
	}
	// None of the rejected writes changed the durable target row.
	stored, err := repo.Get(ctx, 100, target.ID)
	if err != nil || stored.RecurrenceCount != 0 || stored.LastRecurredAt != nil {
		t.Fatalf("target after rejections = %+v err=%v", stored, err)
	}
}

// TestIssueRepoAppendRefsFallback covers the broken-chain fallback that appends
// candidate refs back onto the source idempotently.
func TestIssueRepoAppendRefsFallback(t *testing.T) {
	repo, _ := newIssueRepoForTest(t)
	ctx := context.Background()

	issue := issueRepoCreateCandidate(t, repo, issueRepoTestCandidate{caseRunID: 3601})
	_, fallbackRefs := issueRepoBuildCandidate(t, issueRepoTestCandidate{caseRunID: 3602})
	before, err := repo.ListRefs(ctx, issue.ID)
	if err != nil {
		t.Fatalf("ListRefs before: %v", err)
	}
	// The candidate refs share the case_version with the created issue; only the
	// genuinely new (issue_id, kind, ref_id, role) keys are appended.
	wantAdded := 0
	for _, ref := range fallbackRefs {
		found := false
		for _, existing := range before {
			if existing.Kind == ref.Kind && existing.RefID == ref.RefID &&
				existing.Role == delivery.IssueRefRoleSource {
				found = true
				break
			}
		}
		if !found {
			wantAdded++
		}
	}
	if wantAdded == 0 {
		t.Fatal("fallback fixture has no new ref to append")
	}

	if err := repo.AppendRefs(ctx, issue.ID, fallbackRefs); err != nil {
		t.Fatalf("AppendRefs: %v", err)
	}
	after, err := repo.ListRefs(ctx, issue.ID)
	if err != nil {
		t.Fatalf("ListRefs after: %v", err)
	}
	if len(after) != len(before)+wantAdded {
		t.Fatalf("refs after append = %d, want %d", len(after), len(before)+wantAdded)
	}
	// The appended candidate refs keep the default source role.
	for _, ref := range after[len(before):] {
		if ref.Role != delivery.IssueRefRoleSource {
			t.Fatalf("fallback ref role = %q, want source", ref.Role)
		}
	}

	// Re-appending the same candidate is a no-op thanks to the unique key.
	if err := repo.AppendRefs(ctx, issue.ID, fallbackRefs); err != nil {
		t.Fatalf("second AppendRefs: %v", err)
	}
	repeated, err := repo.ListRefs(ctx, issue.ID)
	if err != nil || len(repeated) != len(after) {
		t.Fatalf("refs after repeated append = %d err=%v, want %d", len(repeated), err, len(after))
	}

	if err := repo.AppendRefs(ctx, 999999, fallbackRefs); err == nil {
		t.Fatal("AppendRefs on a missing issue must fail")
	}
}

// TestIssueRepoForeignKeyCascade covers the delivery_issue_ref and
// delivery_issue_status_history ON DELETE CASCADE to delivery_issue.
func TestIssueRepoForeignKeyCascade(t *testing.T) {
	repo, pool := newIssueRepoForTest(t)
	ctx := context.Background()
	issue := issueRepoCreateCandidate(t, repo, issueRepoTestCandidate{
		waveID:        issueRepoNum(700),
		versionSUTID:  issueRepoNum(9001),
		requirementID: 1200,
	})

	refs, err := repo.ListRefs(ctx, issue.ID)
	if err != nil || len(refs) == 0 {
		t.Fatalf("refs before delete = %d err=%v", len(refs), err)
	}
	history, err := repo.ListHistory(ctx, issue.ID)
	if err != nil || len(history) == 0 {
		t.Fatalf("history before delete = %d err=%v", len(history), err)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM delivery_issue WHERE id = $1`, issue.ID); err != nil {
		t.Fatalf("delete issue: %v", err)
	}
	refs, err = repo.ListRefs(ctx, issue.ID)
	if err != nil || len(refs) != 0 {
		t.Fatalf("refs after delete = %d err=%v, want 0 nil", len(refs), err)
	}
	history, err = repo.ListHistory(ctx, issue.ID)
	if err != nil || len(history) != 0 {
		t.Fatalf("history after delete = %d err=%v, want 0 nil", len(history), err)
	}

	var refCount, historyCount int
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM delivery_issue_ref WHERE issue_id = $1),
		       (SELECT count(*) FROM delivery_issue_status_history WHERE issue_id = $1)
	`, issue.ID).Scan(&refCount, &historyCount); err != nil {
		t.Fatalf("count cascade rows: %v", err)
	}
	if refCount != 0 || historyCount != 0 {
		t.Fatalf("cascade left refs=%d history=%d", refCount, historyCount)
	}
	if _, err := repo.GetByID(ctx, issue.ID); !errors.Is(err, delivery.ErrNotFound) {
		t.Fatalf("deleted issue lookup error = %v, want delivery.ErrNotFound", err)
	}
}
