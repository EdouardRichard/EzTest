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

// mergeChainLookup builds a lookup for nodes consecutive non-merged issues
// starting at firstID, each pointing at the next. The last node points at
// terminalID when it is positive and otherwise ends the chain. The target's
// own first link to firstID is counted in addition to these nodes.
func mergeChainLookup(firstID int64, nodes int, terminalID int64) func(int64) (Issue, bool) {
	chain := make(map[int64]Issue, nodes)
	for offset := 0; offset < nodes; offset++ {
		id := firstID + int64(offset)
		node := Issue{ID: id, VersionID: 7, Status: IssueStatusConfirmed}
		switch {
		case offset < nodes-1:
			node.MergedIntoID = int64Pointer(id + 1)
		case terminalID > 0:
			node.MergedIntoID = int64Pointer(terminalID)
		}
		chain[id] = node
	}
	return func(id int64) (Issue, bool) {
		node, ok := chain[id]
		return node, ok
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

	// The merged_into walk is bounded by MaxIssueMergeChain hops. A finite chain
	// of exactly the budget is accepted, while a longer chain is rejected as an
	// invalid target (never as a cycle, because no cycle was proven).
	atBudget := target
	atBudget.MergedIntoID = int64Pointer(100)
	if err := ValidateMerge(source, atBudget, mergeChainLookup(100, MaxIssueMergeChain, 0)); err != nil {
		t.Fatalf("%d-link finite chain rejected: %v", MaxIssueMergeChain, err)
	}

	overBudget := target
	overBudget.MergedIntoID = int64Pointer(200)
	requireMergeReason(t,
		ValidateMerge(source, overBudget, mergeChainLookup(200, MaxIssueMergeChain+1, 0)),
		MergeReasonTargetInvalid,
	)

	// A source reached within the budget remains a cycle: the target's first
	// link plus MaxIssueMergeChain-1 node links make exactly the budget.
	cycleAtBudget := target
	cycleAtBudget.MergedIntoID = int64Pointer(400)
	requireMergeReason(t,
		ValidateMerge(source, cycleAtBudget, mergeChainLookup(400, MaxIssueMergeChain-1, source.ID)),
		MergeReasonCycle,
	)

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

func TestIssueValidateMergeMergedTargetWithLongChain(t *testing.T) {
	source := Issue{ID: 10, VersionID: 7, Status: IssueStatusOpen}

	// A merged target is terminal: it must be rejected before its chain is
	// followed, so a long chain cannot surface as merge_cycle.
	longChainTarget := Issue{
		ID:           20,
		VersionID:    7,
		Status:       IssueStatusMerged,
		MergedIntoID: int64Pointer(300),
	}
	requireMergeReason(t,
		ValidateMerge(source, longChainTarget, mergeChainLookup(300, 20, 0)),
		MergeReasonTargetInvalid,
	)

	// Nor may a merged target whose chain points straight back at the source be
	// reported as a cycle: the invalid target status wins.
	mergedToSource := longChainTarget
	mergedToSource.MergedIntoID = int64Pointer(source.ID)
	requireMergeReason(t, ValidateMerge(source, mergedToSource, nil), MergeReasonTargetInvalid)
}
