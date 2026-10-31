//go:build integration

package deliverypg

import (
	"context"
	"errors"
	"testing"

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
		in.failureClass = "assertion_failed"
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

	// Reopening an already open issue does not increment reopen_count.
	reopenedAgain, err := repo.UpdateStatus(ctx, deliveryapp.IssueStatusUpdate{
		VersionID: 100,
		IssueID:   issue.ID,
		To:        delivery.IssueStatusOpen,
		Reason:    "duplicate reopen",
		ActorID:   88,
		ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("second reopen: %v", err)
	}
	if reopenedAgain.ExpectedRevision != 4 || reopenedAgain.ReopenCount != 1 {
		t.Fatalf("second reopen issue = %+v", reopenedAgain)
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
	if dismissed.Status != delivery.IssueStatusDismissed || dismissed.ExpectedRevision != 5 || dismissed.ReopenCount != 1 {
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
	if reopenedThird.ExpectedRevision != 6 || reopenedThird.ReopenCount != 2 {
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
