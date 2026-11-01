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

// mergeChainFixture builds a lookup over the given issues plus the source.
func mergeChainFixture(source Issue, issues ...Issue) (Issue, func(int64) (Issue, bool)) {
	nodes := make(map[int64]Issue, len(issues))
	for _, issue := range issues {
		nodes[issue.ID] = issue
	}
	return source, func(id int64) (Issue, bool) {
		issue, ok := nodes[id]
		return issue, ok
	}
}

func mergeChainIDs(issues []Issue) []int64 {
	ids := make([]int64, 0, len(issues))
	for _, issue := range issues {
		ids = append(ids, issue.ID)
	}
	return ids
}

func requireMergeChainIDs(t *testing.T, got []Issue, want ...int64) {
	t.Helper()
	ids := mergeChainIDs(got)
	if len(ids) != len(want) {
		t.Fatalf("merge chain = %v, want %v", ids, want)
	}
	for index := range want {
		if ids[index] != want[index] {
			t.Fatalf("merge chain = %v, want %v", ids, want)
		}
	}
}

func TestIssueMergeChainFollowsMergedLinksToTarget(t *testing.T) {
	source := Issue{ID: 10, VersionID: 7, Status: IssueStatusMerged, MergedIntoID: int64Pointer(20)}
	source, lookup := mergeChainFixture(source,
		Issue{ID: 20, VersionID: 7, Status: IssueStatusMerged, MergedIntoID: int64Pointer(30)},
		Issue{ID: 30, VersionID: 7, Status: IssueStatusOpen},
	)
	chain, ok := MergeChain(source, lookup)
	if !ok {
		t.Fatal("MergeChain() ok = false, want the resolved target")
	}
	requireMergeChainIDs(t, chain, 20, 30)
	if chain[len(chain)-1].Status != IssueStatusOpen {
		t.Fatalf("terminal target status = %q, want open", chain[len(chain)-1].Status)
	}

	// A non-merged issue has no target chain.
	open := Issue{ID: 40, VersionID: 7, Status: IssueStatusOpen}
	if chain, ok := MergeChain(open, lookup); !ok || len(chain) != 0 {
		t.Fatalf("open MergeChain = %v/%v, want an empty ok chain", mergeChainIDs(chain), ok)
	}
	// A merged issue without a recorded link is also an empty chain, not a
	// failure: the caller only redirects when a link exists.
	unlinked := Issue{ID: 50, VersionID: 7, Status: IssueStatusMerged}
	if chain, ok := MergeChain(unlinked, lookup); !ok || len(chain) != 0 {
		t.Fatalf("unlinked MergeChain = %v/%v, want an empty ok chain", mergeChainIDs(chain), ok)
	}
}

func TestIssueMergeChainRejectsBrokenCycleAndForeignTargets(t *testing.T) {
	t.Run("missing target", func(t *testing.T) {
		source := Issue{ID: 10, VersionID: 7, Status: IssueStatusMerged, MergedIntoID: int64Pointer(99)}
		source, lookup := mergeChainFixture(source)
		if chain, ok := MergeChain(source, lookup); ok || len(chain) != 0 {
			t.Fatalf("broken MergeChain = %v/%v, want no chain", mergeChainIDs(chain), ok)
		}
	})

	t.Run("self cycle", func(t *testing.T) {
		source := Issue{ID: 10, VersionID: 7, Status: IssueStatusMerged, MergedIntoID: int64Pointer(10)}
		source, lookup := mergeChainFixture(source, source)
		if chain, ok := MergeChain(source, lookup); ok {
			t.Fatalf("self-cycle MergeChain = %v, want redirect failure", mergeChainIDs(chain))
		}
	})

	t.Run("indirect cycle", func(t *testing.T) {
		source := Issue{ID: 10, VersionID: 7, Status: IssueStatusMerged, MergedIntoID: int64Pointer(20)}
		source, lookup := mergeChainFixture(source,
			Issue{ID: 20, VersionID: 7, Status: IssueStatusMerged, MergedIntoID: int64Pointer(30)},
			Issue{ID: 30, VersionID: 7, Status: IssueStatusMerged, MergedIntoID: int64Pointer(20)},
		)
		if chain, ok := MergeChain(source, lookup); ok {
			t.Fatalf("cyclic MergeChain = %v, want redirect failure", mergeChainIDs(chain))
		}
	})

	t.Run("cross version target", func(t *testing.T) {
		source := Issue{ID: 10, VersionID: 7, Status: IssueStatusMerged, MergedIntoID: int64Pointer(20)}
		source, lookup := mergeChainFixture(source,
			Issue{ID: 20, VersionID: 8, Status: IssueStatusOpen},
		)
		if chain, ok := MergeChain(source, lookup); ok {
			t.Fatalf("cross-version MergeChain = %v, want redirect failure", mergeChainIDs(chain))
		}
	})
}

func TestIssueMergeChainBudget(t *testing.T) {
	// A finite chain of exactly MaxIssueMergeChain hops is resolvable.
	source := Issue{ID: 10, VersionID: 7, Status: IssueStatusMerged, MergedIntoID: int64Pointer(100)}
	nodes := make([]Issue, 0, MaxIssueMergeChain)
	for offset := 0; offset < MaxIssueMergeChain; offset++ {
		node := Issue{ID: int64(100 + offset), VersionID: 7, Status: IssueStatusMerged}
		if offset == MaxIssueMergeChain-1 {
			node.Status = IssueStatusConfirmed
		} else {
			node.MergedIntoID = int64Pointer(int64(100 + offset + 1))
		}
		nodes = append(nodes, node)
	}
	source, lookup := mergeChainFixture(source, nodes...)
	chain, ok := MergeChain(source, lookup)
	if !ok || len(chain) != MaxIssueMergeChain {
		t.Fatalf("at-budget MergeChain = %v/%v, want %d resolved targets", mergeChainIDs(chain), ok, MaxIssueMergeChain)
	}

	// One additional merged hop is over budget: the redirect fails after the
	// bounded walk instead of guessing.
	over := make([]Issue, 0, MaxIssueMergeChain)
	for offset := 0; offset < MaxIssueMergeChain; offset++ {
		over = append(over, Issue{
			ID:           int64(200 + offset),
			VersionID:    7,
			Status:       IssueStatusMerged,
			MergedIntoID: int64Pointer(int64(200 + offset + 1)),
		})
	}
	overSource := Issue{ID: 10, VersionID: 7, Status: IssueStatusMerged, MergedIntoID: int64Pointer(200)}
	source, lookup = mergeChainFixture(overSource, over...)
	chain, ok = MergeChain(source, lookup)
	if ok {
		t.Fatalf("over-budget MergeChain = %v, want redirect failure", mergeChainIDs(chain))
	}
	if len(chain) != MaxIssueMergeChain {
		t.Fatalf("over-budget chain length = %d, want the %d walked targets", len(chain), MaxIssueMergeChain)
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
