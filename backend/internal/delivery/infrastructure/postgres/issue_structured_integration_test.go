//go:build integration

package deliverypg

import (
	"context"
	"testing"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// TestIssueRepoStructuredFailureFields pins the v0.2.4-G SC-G3 persistence: the
// candidate's structured failed_expected/failed_actual/failed_reason survive
// the insert and every read path (RETURNING, Get, List), while Summary keeps
// the narrative.
func TestIssueRepoStructuredFailureFields(t *testing.T) {
	repo, _ := newIssueRepoForTest(t)
	ctx := context.Background()
	issue, refs, err := delivery.NewIssueCandidate(delivery.CandidateInput{
		VersionID:      100,
		CaseRunID:      3001,
		CaseVersionID:  4001,
		CaseTitle:      "Checkout submission",
		StepSeq:        2,
		AssertionSeq:   1,
		FailureClass:   "application",
		Category:       "ui",
		Severity:       "major",
		FailedExpected: "order confirmation visible",
		FailedActual:   "order confirmation missing",
		FailedReason:   "assertion failed on step 2",
	})
	if err != nil {
		t.Fatalf("build candidate: %v", err)
	}
	created, isNew, err := repo.CreateCandidate(ctx, issue, refs)
	if err != nil {
		t.Fatalf("CreateCandidate: %v", err)
	}
	if !isNew {
		t.Fatal("CreateCandidate reported an existing issue")
	}
	if created.FailedExpected != "order confirmation visible" ||
		created.FailedActual != "order confirmation missing" ||
		created.FailedReason != "assertion failed on step 2" {
		t.Fatalf("created structured fields = %q/%q/%q", created.FailedExpected, created.FailedActual, created.FailedReason)
	}
	got, err := repo.Get(ctx, 100, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.FailedExpected != created.FailedExpected || got.FailedActual != created.FailedActual || got.FailedReason != created.FailedReason {
		t.Fatalf("Get structured fields = %q/%q/%q, want the persisted values", got.FailedExpected, got.FailedActual, got.FailedReason)
	}
	listed, err := repo.List(ctx, 100, deliveryapp.IssueFilter{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 || listed[0].FailedExpected != "order confirmation visible" ||
		listed[0].FailedActual != "order confirmation missing" || listed[0].FailedReason != "assertion failed on step 2" {
		t.Fatalf("List structured fields = %+v", listed)
	}
}
