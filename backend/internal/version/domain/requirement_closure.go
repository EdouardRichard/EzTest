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
	ClosureCheckCasesVerified      = "cases_verified"
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
	CasesVerified      bool `json:"cases_verified"`
	FailedCaseHasIssue bool `json:"failed_case_has_issue"`
	NoOpenWork         bool `json:"no_open_work"`
	NoActiveIntent     bool `json:"no_active_intent"`
	NoInflightCaseRun  bool `json:"no_inflight_case_run"`
	CoverageRecorded   bool `json:"coverage_recorded"`
}

// Passed reports whether every closure check is green.
func (c ClosureChecks) Passed() bool {
	return c.RequirementState && c.HasConfirmedCase && c.CasesTerminal &&
		c.CasesVerified && c.FailedCaseHasIssue && c.NoOpenWork &&
		c.NoActiveIntent && c.NoInflightCaseRun && c.CoverageRecorded
}

// ClosureCheckResult is the persisted check_result snapshot: the nine effective
// checks plus the waived flag. Waived is true when an admin requirement waiver
// (v0.2.4-G SC-G11, GD-06/P23) overrode the closure COMPLETENESS checks for this
// round; the checks then carry their effective (post-override) values and the
// flag discloses that the closure was granted by waiver rather than by full
// verification. The persisted requirement.Waived column is the authority; this
// flag is its per-round projection.
type ClosureCheckResult struct {
	ClosureChecks
	Waived bool `json:"waived"`
}

// ClosureInput is the server-recomputed projection of one requirement. GD-17
// removed the requirement.close work type, so OpenWorkItems is the raw blocking
// count — there is no closure self-task to subtract.
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

// EvaluateRequirementClosure recomputes the nine closure checks. Missing
// latest runs are non-terminal; missing issue refs only block failed
// application runs. Open issue status is intentionally not modelled: any ref
// satisfies the check. CasesVerified is the verification gate (Task 117): a
// terminal run is not enough, the case must have produced a definitive outcome.
// Passed, failed and explicitly skipped runs are definitive; inconclusive and
// blocked runs verified nothing and keep the requirement open.
//
// An admin requirement waiver (Requirement.Waived, v0.2.4-G SC-G11) overrides
// the closure COMPLETENESS checks — see applyRequirementWaiver — so the
// deterministic reconciler can close a waived requirement through the normal
// terminal Close entry. The execution-settled checks are never overridden.
func EvaluateRequirementClosure(in ClosureInput) ClosureChecks {
	checks := ClosureChecks{
		RequirementState: in.Requirement.CurrentVersionID != nil &&
			(in.Requirement.Status == RequirementClarified || in.Requirement.Status == RequirementClosed),
		HasConfirmedCase:   len(in.CaseVersions) > 0,
		CasesTerminal:      true,
		CasesVerified:      true,
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
		if !ok || !verifiedCaseRunStatus(run.Status) {
			checks.CasesVerified = false
		}
		if ok && run.Status == CaseRunFailed && run.FailureClass == FailureClassApplication &&
			!in.IssueRefs[run.CaseRunID] {
			checks.FailedCaseHasIssue = false
		}
	}
	return applyRequirementWaiver(checks, in.Requirement.Waived)
}

// applyRequirementWaiver overrides the closure COMPLETENESS checks of an
// admin-waived requirement (GD-06/P23, R-G6): has_confirmed_case,
// cases_verified, failed_case_has_issue and coverage_recorded. These are the
// requirement-level analogue of the version waiver's RequirementsClosed
// override. The execution-settled checks (requirement_state, cases_terminal,
// no_open_work, no_active_intent, no_inflight_case_run) are NEVER overridden:
// a waiver grants closure completeness, it does not pretend in-flight work
// finished. This is the single authority for which checks a requirement waiver
// covers; both the evaluation and the failure counts derive from it.
func applyRequirementWaiver(checks ClosureChecks, waived bool) ClosureChecks {
	if !waived {
		return checks
	}
	checks.HasConfirmedCase = true
	checks.CasesVerified = true
	checks.FailedCaseHasIssue = true
	checks.CoverageRecorded = true
	return checks
}

// ClosureFailureCounts returns the check-name to count map carried by
// ErrClosePredicateFailed. Counts are the offending row counts (open work
// items, active intents, non-terminal cases, unverified cases, missing issue
// refs); boolean-only failures report 1 and a missing confirmed case reports 0.
//
// The map is derived from EvaluateRequirementClosure so the waiver override has
// ONE authority: a check the waiver covers is effective-true and never reports
// a failure, while the execution-settled checks report exactly as before.
func ClosureFailureCounts(in ClosureInput) map[string]int {
	checks := EvaluateRequirementClosure(in)
	failed := make(map[string]int)
	if !checks.RequirementState {
		failed[ClosureCheckRequirementState] = 1
	}
	if !checks.HasConfirmedCase {
		failed[ClosureCheckHasConfirmedCase] = 0
	}
	if !checks.CasesTerminal || !checks.CasesVerified || !checks.FailedCaseHasIssue {
		nonTerminal := 0
		unverified := 0
		missingIssue := 0
		for _, caseVersion := range in.CaseVersions {
			run, ok := in.LatestRuns[caseVersion.CaseVersionID]
			if !ok || !terminalCaseRunStatus(run.Status) {
				nonTerminal++
			}
			if !ok || !verifiedCaseRunStatus(run.Status) {
				unverified++
			}
			if ok && run.Status == CaseRunFailed && run.FailureClass == FailureClassApplication &&
				!in.IssueRefs[run.CaseRunID] {
				missingIssue++
			}
		}
		if !checks.CasesTerminal && nonTerminal > 0 {
			failed[ClosureCheckCasesTerminal] = nonTerminal
		}
		if !checks.CasesVerified && unverified > 0 {
			failed[ClosureCheckCasesVerified] = unverified
		}
		if !checks.FailedCaseHasIssue && missingIssue > 0 {
			failed[ClosureCheckFailedCaseHasIssue] = missingIssue
		}
	}
	if !checks.NoOpenWork {
		failed[ClosureCheckNoOpenWork] = in.OpenWorkItems
	}
	if !checks.NoActiveIntent {
		failed[ClosureCheckNoActiveIntent] = in.ActiveIntents
	}
	if !checks.NoInflightCaseRun {
		failed[ClosureCheckNoInflightCaseRun] = in.InflightCaseRuns
	}
	if !checks.CoverageRecorded {
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

// verifiedCaseRunStatus reports whether a latest case_run carries a definitive
// verification outcome. passed and failed are execution results, and skipped is
// the explicit skip recorded by the operator (the waiver-equivalent P4 allows).
// blocked and inconclusive are terminal but verified nothing: the case never
// produced a pass/fail verdict, so a requirement carrying one must stay open
// (Task 117 / REL-Q3).
func verifiedCaseRunStatus(status string) bool {
	switch status {
	case CaseRunPassed, CaseRunFailed, CaseRunSkipped:
		return true
	default:
		return false
	}
}
