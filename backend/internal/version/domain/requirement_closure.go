package version

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Case_run statuses used by the closure predicate. Only the terminal set may
// close a requirement; pending/running runs are in-flight work.
const (
	CaseRunPending      = "pending"
	CaseRunRunning      = "running"
	CaseRunPassed       = "passed"
	CaseRunFailed       = "failed"
	CaseRunBlocked      = "blocked"
	CaseRunInconclusive = "inconclusive"
	CaseRunSkipped      = "skipped"
)

// FailureClassApplication marks a case_run failure caused by the system under
// test. Only these failures require an issue reference before closure
// (spec §7.2); environment/infra failures do not.
const FailureClassApplication = "application"

// Closure check names are the stable keys of the 409 close_predicate_failed
// details.failed map and of the persisted check_result JSON.
const (
	ClosureCheckRequirementState   = "requirement_state"
	ClosureCheckHasConfirmedCase   = "has_confirmed_case"
	ClosureCheckCasesTerminal      = "cases_terminal"
	ClosureCheckFailedCaseHasIssue = "failed_case_has_issue"
	ClosureCheckNoOpenWork         = "no_open_work"
	ClosureCheckNoActiveIntent     = "no_active_intent"
	ClosureCheckNoInflightCaseRun  = "no_inflight_case_run"
	ClosureCheckCoverageRecorded   = "coverage_recorded"
)

// Closure evidence kinds persisted in requirement_closure.evidence_refs.
const (
	ClosureEvidenceCaseRun = "case_run"
	ClosureEvidenceIssue   = "issue"
	ClosureEvidenceReport  = "report"
)

// CaseVersionSnapshot is one current+confirmed case version of a requirement,
// projected from the design context.
type CaseVersionSnapshot struct {
	CaseVersionID        int64
	CaseID               int64
	RequirementID        int64
	RequirementVersionID int64
	Title                string
}

// CaseRunSnapshot is the newest case_run (id DESC) of one case version,
// projected from the execution context.
type CaseRunSnapshot struct {
	CaseRunID     int64
	CaseVersionID int64
	Status        string
	FailureClass  string
}

// ClosureChecks is the server-side truth of one closure evaluation (spec
// §7.2). A requirement may close only when every field is true.
type ClosureChecks struct {
	RequirementState   bool `json:"requirement_state"`
	HasConfirmedCase   bool `json:"has_confirmed_case"`
	CasesTerminal      bool `json:"cases_terminal"`
	FailedCaseHasIssue bool `json:"failed_case_has_issue"`
	NoOpenWork         bool `json:"no_open_work"`
	NoActiveIntent     bool `json:"no_active_intent"`
	NoInflightCaseRun  bool `json:"no_inflight_case_run"`
	CoverageRecorded   bool `json:"coverage_recorded"`
}

// Passed reports whether every closure check is green.
func (c ClosureChecks) Passed() bool {
	return c.RequirementState && c.HasConfirmedCase && c.CasesTerminal &&
		c.FailedCaseHasIssue && c.NoOpenWork && c.NoActiveIntent &&
		c.NoInflightCaseRun && c.CoverageRecorded
}

// ClosureCheckResult is the persisted check_result snapshot: the eight checks
// plus the reserved waived flag. P4 has no waiver path, so Waived is always
// false; P6 owns the explicit human waiver.
type ClosureCheckResult struct {
	ClosureChecks
	Waived bool `json:"waived"`
}

// ClosureInput is the server-recomputed projection of one requirement. The
// service subtracts the requirement's own requirement.close task from
// OpenWorkItems before evaluating, so the predicate itself only sees the
// effective count.
type ClosureInput struct {
	Requirement      Requirement
	CaseVersions     []CaseVersionSnapshot
	LatestRuns       map[int64]CaseRunSnapshot
	IssueRefs        map[int64]bool
	OpenWorkItems    int
	ActiveIntents    int
	InflightCaseRuns int
	CoverageComplete bool
}

// EvaluateRequirementClosure recomputes the eight closure checks. Missing
// latest runs are non-terminal; missing issue refs only block failed
// application runs. Open issue status is intentionally not modelled: any ref
// satisfies the check.
func EvaluateRequirementClosure(in ClosureInput) ClosureChecks {
	checks := ClosureChecks{
		RequirementState: in.Requirement.CurrentVersionID != nil &&
			(in.Requirement.Status == RequirementClarified || in.Requirement.Status == RequirementClosed),
		HasConfirmedCase:   len(in.CaseVersions) > 0,
		CasesTerminal:      true,
		FailedCaseHasIssue: true,
		NoOpenWork:         in.OpenWorkItems <= 0,
		NoActiveIntent:     in.ActiveIntents <= 0,
		NoInflightCaseRun:  in.InflightCaseRuns <= 0,
		CoverageRecorded:   in.CoverageComplete,
	}
	for _, caseVersion := range in.CaseVersions {
		run, ok := in.LatestRuns[caseVersion.CaseVersionID]
		if !ok || !terminalCaseRunStatus(run.Status) {
			checks.CasesTerminal = false
		}
		if ok && run.Status == CaseRunFailed && run.FailureClass == FailureClassApplication &&
			!in.IssueRefs[run.CaseRunID] {
			checks.FailedCaseHasIssue = false
		}
	}
	return checks
}

// ClosureFailureCounts returns the check-name to count map carried by
// ErrClosePredicateFailed. Counts are the offending row counts (open work
// items, active intents, non-terminal cases, missing issue refs); boolean-only
// failures report 1 and a missing confirmed case reports 0.
func ClosureFailureCounts(in ClosureInput) map[string]int {
	failed := make(map[string]int)
	if in.Requirement.CurrentVersionID == nil ||
		(in.Requirement.Status != RequirementClarified && in.Requirement.Status != RequirementClosed) {
		failed[ClosureCheckRequirementState] = 1
	}
	if len(in.CaseVersions) == 0 {
		failed[ClosureCheckHasConfirmedCase] = 0
	}
	nonTerminal := 0
	missingIssue := 0
	for _, caseVersion := range in.CaseVersions {
		run, ok := in.LatestRuns[caseVersion.CaseVersionID]
		if !ok || !terminalCaseRunStatus(run.Status) {
			nonTerminal++
		}
		if ok && run.Status == CaseRunFailed && run.FailureClass == FailureClassApplication &&
			!in.IssueRefs[run.CaseRunID] {
			missingIssue++
		}
	}
	if nonTerminal > 0 {
		failed[ClosureCheckCasesTerminal] = nonTerminal
	}
	if missingIssue > 0 {
		failed[ClosureCheckFailedCaseHasIssue] = missingIssue
	}
	if in.OpenWorkItems > 0 {
		failed[ClosureCheckNoOpenWork] = in.OpenWorkItems
	}
	if in.ActiveIntents > 0 {
		failed[ClosureCheckNoActiveIntent] = in.ActiveIntents
	}
	if in.InflightCaseRuns > 0 {
		failed[ClosureCheckNoInflightCaseRun] = in.InflightCaseRuns
	}
	if !in.CoverageComplete {
		failed[ClosureCheckCoverageRecorded] = 1
	}
	return failed
}

// ErrClosePredicateFailed is the stable error returned when the server-side
// closure predicate rejects a proposal. Failed maps each failing check name to
// its count (spec §7.2).
type ErrClosePredicateFailed struct {
	Failed map[string]int
}

// Error renders the failed checks in stable name order.
func (e *ErrClosePredicateFailed) Error() string {
	names := make([]string, 0, len(e.Failed))
	for name := range e.Failed {
		names = append(names, fmt.Sprintf("%s=%d", name, e.Failed[name]))
	}
	sort.Strings(names)
	return "requirement close predicate failed: " + strings.Join(names, ", ")
}

// ClosureEvidenceRef is one issue/case_run/report reference stored in
// evidence_refs. IDs are decimal strings on the JSON boundary (D29).
type ClosureEvidenceRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// RequirementClosure is one persisted closure round.
type RequirementClosure struct {
	ID                   int64
	RequirementID        int64
	RequirementVersionID int64
	CloseRound           int
	CheckResult          ClosureCheckResult
	EvidenceRefs         []ClosureEvidenceRef
	ClosedByRunID        *int64
	ClosedAt             time.Time
	ReopenCount          int
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

func terminalCaseRunStatus(status string) bool {
	switch status {
	case CaseRunPassed, CaseRunFailed, CaseRunBlocked, CaseRunInconclusive, CaseRunSkipped:
		return true
	default:
		return false
	}
}
