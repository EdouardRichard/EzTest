package domain_test

import (
	"reflect"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

func TestAggregateCountsByStatus(t *testing.T) {
	runs := []domain.CaseRun{
		{ID: 1, Status: domain.CaseRunStatusPassed},
		{ID: 2, Status: domain.CaseRunStatusPassed},
		{ID: 3, Status: domain.CaseRunStatusFailed, FailureClass: failureClassPtr(domain.FailureClassApplication)},
		{ID: 4, Status: domain.CaseRunStatusBlocked, FailureClass: failureClassPtr(domain.FailureClassEnvironment)},
		{ID: 5, Status: domain.CaseRunStatusInconclusive, FailureClass: failureClassPtr(domain.FailureClassModel)},
		{ID: 6, Status: domain.CaseRunStatusSkipped},
		{ID: 7, Status: domain.CaseRunStatusInterrupted},
	}
	want := domain.BatchCounts{
		CaseCount:         7,
		PassedCount:       2,
		FailedCount:       1,
		BlockedCount:      1,
		InconclusiveCount: 1,
		SkippedCount:      1,
		InterruptedCount:  1,
	}
	if got := domain.Aggregate(runs); got != want {
		t.Fatalf("Aggregate = %+v, want %+v", got, want)
	}
}

func TestAggregateEmpty(t *testing.T) {
	if got := domain.Aggregate(nil); got != (domain.BatchCounts{}) {
		t.Fatalf("Aggregate(nil) = %+v, want zero counts", got)
	}
}

func TestFlakyCaseIDs(t *testing.T) {
	application := failureClassPtr(domain.FailureClassApplication)
	session := failureClassPtr(domain.FailureClassSession)
	runs := []domain.CaseRun{
		// Case 10 is flaky even though the attempts are supplied newest first.
		{CaseNodeID: 10, Attempt: 2, Status: domain.CaseRunStatusPassed},
		{CaseNodeID: 10, Attempt: 1, Status: domain.CaseRunStatusFailed, FailureClass: application},
		// A pass followed by a failure is not flaky.
		{CaseNodeID: 11, Attempt: 1, Status: domain.CaseRunStatusPassed},
		{CaseNodeID: 11, Attempt: 2, Status: domain.CaseRunStatusFailed, FailureClass: application},
		// A non-application failure followed by a pass is not flaky.
		{CaseNodeID: 12, Attempt: 1, Status: domain.CaseRunStatusFailed, FailureClass: session},
		{CaseNodeID: 12, Attempt: 2, Status: domain.CaseRunStatusPassed},
		// Two application failures are not flaky.
		{CaseNodeID: 13, Attempt: 1, Status: domain.CaseRunStatusFailed, FailureClass: application},
		{CaseNodeID: 13, Attempt: 2, Status: domain.CaseRunStatusFailed, FailureClass: application},
		// Attempt order decides flakiness, not slice order.
		{CaseNodeID: 14, Attempt: 2, Status: domain.CaseRunStatusPassed},
		{CaseNodeID: 14, Attempt: 1, Status: domain.CaseRunStatusFailed, FailureClass: application},
		// Repeated passes are not flaky.
		{CaseNodeID: 15, Attempt: 1, Status: domain.CaseRunStatusPassed},
		{CaseNodeID: 15, Attempt: 2, Status: domain.CaseRunStatusPassed},
		// A later pass keeps the case flaky.
		{CaseNodeID: 16, Attempt: 1, Status: domain.CaseRunStatusFailed, FailureClass: application},
		{CaseNodeID: 16, Attempt: 2, Status: domain.CaseRunStatusPassed},
		{CaseNodeID: 16, Attempt: 3, Status: domain.CaseRunStatusPassed},
		// A failed run without the application class is not flaky.
		{CaseNodeID: 17, Attempt: 1, Status: domain.CaseRunStatusFailed},
		{CaseNodeID: 17, Attempt: 2, Status: domain.CaseRunStatusPassed},
	}
	before := append([]domain.CaseRun(nil), runs...)

	got := domain.FlakyCaseIDs(runs)
	want := []int64{10, 14, 16}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FlakyCaseIDs = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(runs, before) {
		t.Fatal("FlakyCaseIDs must not mutate its input history")
	}
}

func TestFlakyCaseIDsEmpty(t *testing.T) {
	if got := domain.FlakyCaseIDs(nil); len(got) != 0 {
		t.Fatalf("FlakyCaseIDs(nil) = %v, want empty", got)
	}
}
