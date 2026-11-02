package delivery

import "errors"

// Domain sentinel errors. Repositories and use cases translate them onto the
// stable platform API envelope outside this package (spec §4/§5.2).
var (
	// ErrInvalidArgument reports a malformed issue candidate or merge input.
	ErrInvalidArgument = errors.New("delivery: invalid argument")
	// ErrStateConflict reports an illegal issue lifecycle transition.
	ErrStateConflict = errors.New("delivery: state conflict")
	// ErrNotFound reports a missing delivery entity.
	ErrNotFound = errors.New("not found")
)

// MaxIssueMergeChain bounds how many merged_into hops ValidateMerge follows
// (spec §5.2) and how many hops recurrence redirection walks (spec §4.6). A
// chain still open after this budget is rejected as merge_target_invalid, never
// as merge_cycle: the walk stops before it could prove that the chain reaches
// the source.
const MaxIssueMergeChain = 16

// IssueRefRoleRecurrence is the ref role of a recurring failure redirected to a
// merge target (spec §4.6). Source refs keep IssueRefRoleSource; the unique
// (issue_id, kind, ref_id, role) key makes a repeated recurrence ref a no-op.
const IssueRefRoleRecurrence = "recurrence"

// Stable merge conflict reasons (spec §5.2 details.reason subcodes).
const (
	// MergeReasonTargetInvalid marks a self, cross-version, unknown or already
	// merged target.
	MergeReasonTargetInvalid = "merge_target_invalid"
	// MergeReasonCycle marks a merge whose target chain reaches the source.
	MergeReasonCycle = "merge_cycle"
)

// MergeError is a rejected merge with the stable business reason required by
// the API envelope. It unwraps to ErrStateConflict so callers can map it to 409
// while preserving details.reason.
type MergeError struct {
	Reason string
}

// Error implements error.
func (e *MergeError) Error() string { return "delivery: merge rejected: " + e.Reason }

// Unwrap exposes ErrStateConflict to errors.Is.
func (e *MergeError) Unwrap() error { return ErrStateConflict }

// CanTransitionIssue reports whether the P4 issue state machine allows moving
// from one status to another (spec §5.2). merged is terminal in P4: it has no
// outgoing transition, including unmerge or reopen.
func CanTransitionIssue(from, to IssueStatus) bool {
	switch from {
	case IssueStatusOpen:
		return to == IssueStatusConfirmed || to == IssueStatusDismissed || to == IssueStatusMerged
	case IssueStatusConfirmed, IssueStatusDismissed:
		return to == IssueStatusOpen || to == IssueStatusMerged
	default:
		return false
	}
}

// ValidateMerge checks one source->target merge request before persistence
// (spec §5.2): the target must belong to the same version, be a known
// non-merged issue, differ from the source and not close a merged_into cycle.
// lookup resolves a merged_into target; it is only consulted while walking a
// target's merge chain (a nil lookup treats a non-empty chain as unresolvable).
func ValidateMerge(source Issue, target Issue, lookup func(int64) (Issue, bool)) error {
	if source.ID <= 0 || target.ID <= 0 {
		return ErrInvalidArgument
	}
	if source.VersionID <= 0 || target.VersionID <= 0 {
		return ErrInvalidArgument
	}
	if source.ID == target.ID {
		return &MergeError{Reason: MergeReasonTargetInvalid}
	}
	if source.VersionID != target.VersionID {
		return &MergeError{Reason: MergeReasonTargetInvalid}
	}
	if !CanTransitionIssue(source.Status, IssueStatusMerged) {
		return ErrStateConflict
	}
	if !target.Status.Valid() || target.Status == IssueStatusMerged {
		// A merged target is terminal, so it must be rejected before its chain
		// is followed: otherwise a long or cyclic chain would be reported as
		// merge_cycle even though the target itself is simply invalid.
		return &MergeError{Reason: MergeReasonTargetInvalid}
	}

	return mergeChainError(target, source.ID, lookup)
}

// mergeChainError follows target's merged_into chain for at most
// MaxIssueMergeChain hops (spec §5.2). It returns nil when the chain ends
// without reaching the source, merge_cycle when the source is reached inside
// the budget, and merge_target_invalid when a reference cannot be resolved or
// the chain is still open after the budget. The over-budget case is deliberately
// not a cycle: the bounded walk cannot prove that the chain ever reaches the
// source, so it is reported as an invalid target instead. A source lying beyond
// the budget is therefore also reported as merge_target_invalid.
func mergeChainError(target Issue, sourceID int64, lookup func(int64) (Issue, bool)) error {
	current := target
	for hop := 0; hop < MaxIssueMergeChain; hop++ {
		if current.MergedIntoID == nil {
			return nil
		}
		nextID := *current.MergedIntoID
		if nextID == sourceID {
			return &MergeError{Reason: MergeReasonCycle}
		}
		if nextID <= 0 || lookup == nil {
			return &MergeError{Reason: MergeReasonTargetInvalid}
		}
		next, ok := lookup(nextID)
		if !ok {
			return &MergeError{Reason: MergeReasonTargetInvalid}
		}
		current = next
	}
	// The budget followed MaxIssueMergeChain links; the chain is finite only if
	// the last node has no further link.
	if current.MergedIntoID == nil {
		return nil
	}
	return &MergeError{Reason: MergeReasonTargetInvalid}
}

// MergeChain follows the merged_into links of a merged issue up to
// MaxIssueMergeChain hops (spec §4.6) and returns the resolved target issues in
// walk order: the direct merge target first, the first non-merged issue last.
// A non-merged issue yields an empty chain.
//
// ok is false when the chain cannot be redirected: a link is missing or has no
// positive ID, a target belongs to another version, the walk revisits a node
// (cycle), or the chain is still merged after the hop budget. Callers treat a
// false walk as a redirect failure and fall back to the pre-P7 dedupe behavior;
// the returned slice then holds the targets resolved before the failure.
func MergeChain(issue Issue, lookup func(int64) (Issue, bool)) ([]Issue, bool) {
	if issue.MergedIntoID == nil {
		return nil, true
	}
	visited := map[int64]struct{}{issue.ID: {}}
	targets := make([]Issue, 0, 4)
	current := issue
	for hop := 0; hop < MaxIssueMergeChain; hop++ {
		if current.MergedIntoID == nil {
			// The last resolved node is not merged, so the chain terminated
			// before consuming the budget.
			return targets, true
		}
		nextID := *current.MergedIntoID
		if nextID <= 0 {
			return targets, false
		}
		if _, seen := visited[nextID]; seen {
			return targets, false
		}
		visited[nextID] = struct{}{}
		if lookup == nil {
			return targets, false
		}
		next, ok := lookup(nextID)
		if !ok || next.ID != nextID {
			return targets, false
		}
		if next.VersionID != issue.VersionID {
			// P4 merges are same-version; a foreign target cannot receive the
			// recurrence refs of this version's issue.
			return targets, false
		}
		targets = append(targets, next)
		if next.Status != IssueStatusMerged {
			return targets, true
		}
		current = next
	}
	// The budget followed MaxIssueMergeChain links and the last target is still
	// merged: the chain may continue beyond the budget, so it is not redirectable.
	return targets, false
}
