package delivery

import (
	"errors"
	"testing"
)

func int64Pointer(value int64) *int64 { return &value }

func TestIssueStatusTransitionTable(t *testing.T) {
	allowed := []struct{ from, to IssueStatus }{
		{IssueStatusOpen, IssueStatusConfirmed},
		{IssueStatusOpen, IssueStatusDismissed},
		{IssueStatusOpen, IssueStatusMerged},
		{IssueStatusConfirmed, IssueStatusOpen},
		{IssueStatusConfirmed, IssueStatusMerged},
		{IssueStatusDismissed, IssueStatusOpen},
		{IssueStatusDismissed, IssueStatusMerged},
	}
	for _, tc := range allowed {
		if !CanTransitionIssue(tc.from, tc.to) {
			t.Errorf("%s -> %s must be allowed", tc.from, tc.to)
		}
	}

	rejected := []struct{ from, to IssueStatus }{
		{IssueStatusOpen, IssueStatusOpen},
		{IssueStatusConfirmed, IssueStatusConfirmed},
		{IssueStatusDismissed, IssueStatusDismissed},
		{IssueStatusMerged, IssueStatusMerged},
		{IssueStatusConfirmed, IssueStatusDismissed},
		{IssueStatusDismissed, IssueStatusConfirmed},
		{IssueStatusMerged, IssueStatusOpen},
		{IssueStatusMerged, IssueStatusConfirmed},
		{IssueStatusMerged, IssueStatusDismissed},
		{IssueStatus("unknown"), IssueStatusOpen},
		{IssueStatusOpen, IssueStatus("unknown")},
		{IssueStatus(""), IssueStatusOpen},
	}
	for _, tc := range rejected {
		if CanTransitionIssue(tc.from, tc.to) {
			t.Errorf("%s -> %s must be rejected", tc.from, tc.to)
		}
	}
}

func TestIssueMergedTerminal(t *testing.T) {
	for _, to := range []IssueStatus{
		IssueStatusOpen, IssueStatusConfirmed, IssueStatusDismissed, IssueStatusMerged,
	} {
		if CanTransitionIssue(IssueStatusMerged, to) {
			t.Errorf("merged is terminal in P4, but merged -> %s was allowed", to)
		}
	}
}

func requireMergeReason(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("merge error = nil, want a %q state conflict", want)
	}
	if !errors.Is(err, ErrStateConflict) {
		t.Fatalf("merge error = %v, want errors.Is(ErrStateConflict)", err)
	}
	var mergeErr *MergeError
	if !errors.As(err, &mergeErr) {
		t.Fatalf("merge error = %v, want a *MergeError with reason %q", err, want)
	}
	if mergeErr.Reason != want {
		t.Fatalf("merge reason = %q, want %q", mergeErr.Reason, want)
	}
}

func TestIssueValidateMerge(t *testing.T) {
	source := Issue{ID: 10, VersionID: 7, Status: IssueStatusOpen}
	target := Issue{ID: 20, VersionID: 7, Status: IssueStatusConfirmed}

	if err := ValidateMerge(source, target, nil); err != nil {
		t.Fatalf("valid merge rejected: %v", err)
	}
	for _, status := range []IssueStatus{IssueStatusConfirmed, IssueStatusDismissed, IssueStatusOpen} {
		candidate := source
		candidate.Status = status
		if err := ValidateMerge(candidate, target, nil); err != nil {
			t.Fatalf("%s -> merged rejected: %v", status, err)
		}
	}

	selfTarget := target
	selfTarget.ID = source.ID
	requireMergeReason(t, ValidateMerge(source, selfTarget, nil), MergeReasonTargetInvalid)

	crossVersion := target
	crossVersion.VersionID = source.VersionID + 1
	requireMergeReason(t, ValidateMerge(source, crossVersion, nil), MergeReasonTargetInvalid)

	alreadyMerged := target
	alreadyMerged.Status = IssueStatusMerged
	requireMergeReason(t, ValidateMerge(source, alreadyMerged, nil), MergeReasonTargetInvalid)

	invalidTargetStatus := target
	invalidTargetStatus.Status = IssueStatus("unknown")
	requireMergeReason(t, ValidateMerge(source, invalidTargetStatus, nil), MergeReasonTargetInvalid)

	directCycle := target
	directCycle.MergedIntoID = int64Pointer(source.ID)
	requireMergeReason(t, ValidateMerge(source, directCycle, nil), MergeReasonCycle)

	indirectCycle := target
	indirectCycle.MergedIntoID = int64Pointer(30)
	middle := Issue{ID: 30, VersionID: 7, Status: IssueStatusConfirmed, MergedIntoID: int64Pointer(source.ID)}
	lookup := func(id int64) (Issue, bool) {
		if id == middle.ID {
			return middle, true
		}
		return Issue{}, false
	}
	requireMergeReason(t, ValidateMerge(source, indirectCycle, lookup), MergeReasonCycle)

	brokenChain := target
	brokenChain.MergedIntoID = int64Pointer(99)
	requireMergeReason(t, ValidateMerge(source, brokenChain, func(int64) (Issue, bool) {
		return Issue{}, false
	}), MergeReasonTargetInvalid)

	// The merge-chain walk is bounded by MaxIssueMergeChain hops; a chain that
	// never terminates within the budget is rejected instead of looping.
	longChain := map[int64]Issue{}
	for id := int64(100); id < 120; id++ {
		longChain[id] = Issue{
			ID:           id,
			VersionID:    7,
			Status:       IssueStatusConfirmed,
			MergedIntoID: int64Pointer(id + 1),
		}
	}
	unbounded := target
	unbounded.MergedIntoID = int64Pointer(100)
	requireMergeReason(t, ValidateMerge(source, unbounded, func(id int64) (Issue, bool) {
		issue, ok := longChain[id]
		return issue, ok
	}), MergeReasonCycle)

	mergedSource := source
	mergedSource.Status = IssueStatusMerged
	if err := ValidateMerge(mergedSource, target, nil); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("merged source error = %v, want errors.Is(ErrStateConflict)", err)
	}

	if err := ValidateMerge(Issue{}, target, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("missing source ID error = %v, want errors.Is(ErrInvalidArgument)", err)
	}
	if err := ValidateMerge(source, Issue{}, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("missing target ID error = %v, want errors.Is(ErrInvalidArgument)", err)
	}
}
