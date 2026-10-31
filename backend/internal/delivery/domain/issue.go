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
// before rejecting a target whose chain cannot be proven acyclic (spec §5.2).
const MaxIssueMergeChain = 16

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
	if !target.Status.Valid() {
		return &MergeError{Reason: MergeReasonTargetInvalid}
	}

	cycle, broken := mergeChainState(target, source.ID, lookup)
	switch {
	case cycle:
		return &MergeError{Reason: MergeReasonCycle}
	case broken:
		return &MergeError{Reason: MergeReasonTargetInvalid}
	case target.Status == IssueStatusMerged:
		return &MergeError{Reason: MergeReasonTargetInvalid}
	default:
		return nil
	}
}

// mergeChainState follows target's merged_into chain for at most
// MaxIssueMergeChain hops. It reports a cycle when the source is reached or the
// budget is exhausted, and broken when a referenced issue cannot be resolved.
func mergeChainState(target Issue, sourceID int64, lookup func(int64) (Issue, bool)) (cycle, broken bool) {
	current := target
	for hop := 0; hop < MaxIssueMergeChain; hop++ {
		if current.MergedIntoID == nil {
			return false, false
		}
		nextID := *current.MergedIntoID
		if nextID == sourceID {
			return true, false
		}
		if nextID <= 0 || lookup == nil {
			return false, true
		}
		next, ok := lookup(nextID)
		if !ok {
			return false, true
		}
		current = next
	}
	return true, false
}
