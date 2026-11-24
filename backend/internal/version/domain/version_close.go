package version

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Version close input limits. The close entry accepts reason(1..500); the
// manual reopen API accepts reason(1..2000) (spec §4.2/§4.4).
const (
	VersionCloseReasonMax  = 500
	VersionReopenReasonMax = 2000
)

// Version close check names are the stable keys of the
// close_predicate_failed details.failed map and of the close_read snapshot
// (spec §7.3).
const (
	VersionCloseCheckRunning                = "version_running"
	VersionCloseCheckNoBlockingWorkItems    = "no_blocking_work_items"
	VersionCloseCheckNoActiveIntents        = "no_active_intents"
	VersionCloseCheckNoOpenHumanRequests    = "no_open_human_requests"
	VersionCloseCheckNoActiveWaves          = "no_active_waves"
	VersionCloseCheckNoInflightCaseRuns     = "no_inflight_case_runs"
	VersionCloseCheckRequirementsClosed     = "requirements_closed"
	VersionCloseCheckVersionReportPublished = "version_report_published"
)

// Report statuses relevant to the version close predicate. They mirror the
// delivery report status wire values; the version context keeps local string
// constants so the predicate does not import the delivery domain.
const (
	ReportStatusDraft     = "draft"
	ReportStatusPublished = "published"
	ReportStatusStale     = "stale"
)

// ClosePredicate is the server-side truth of one version close evaluation. A
// version may complete only when every field is true (spec §7.3).
//
// HasExecutionData and UnconfirmedCases are the §5.3 completion inputs. The
// close predicate contract is frozen at eight checks, so they are
// folded into VersionReportPublished (a report without execution data is not a
// valid completion report) and NoInflightCaseRuns (a confirmed case that was
// never executed or explicitly skipped is still pending work). They are kept
// as fields so callers can inspect the raw execution projection.
type ClosePredicate struct {
	Running                bool
	NoBlockingWorkItems    bool
	NoActiveIntents        bool
	NoOpenHumanRequests    bool
	NoActiveWaves          bool
	NoInflightCaseRuns     bool
	RequirementsClosed     bool
	VersionReportPublished bool
	HasExecutionData       bool
	UnconfirmedCases       int
}

// Passed reports whether every close condition is green.
func (p ClosePredicate) Passed() bool {
	return p.Running && p.NoBlockingWorkItems && p.NoActiveIntents &&
		p.NoOpenHumanRequests && p.NoActiveWaves && p.NoInflightCaseRuns &&
		p.RequirementsClosed && p.VersionReportPublished &&
		p.HasExecutionData && p.UnconfirmedCases <= 0
}

// ReportStatus is the version-context projection of the latest kind='version'
// report. Status is the effective close-relevant status: the report row must be
// published AND its current revision must be published, render_status=done and
// carry an HTML ref; the delivery adapter collapses those conditions into
// "published" and returns the literal status otherwise.
//
// Fingerprint is the input_fingerprint of the current published revision.
// RevisionID is that revision's ID (the version.close task payload and the
// version.closed event carry it). CurrentFingerprint is the recomputed current
// input fingerprint; when non-empty it must equal Fingerprint, otherwise the
// report is stale. A published report without a revision or fingerprint never
// satisfies the predicate.
type ReportStatus struct {
	Status             string
	Fingerprint        string
	RevisionID         int64
	CurrentFingerprint string
}

// VersionReportCurrent reports whether the report satisfies check 8 of the
// close predicate.
func VersionReportCurrent(report ReportStatus) bool {
	if report.Status != ReportStatusPublished || report.RevisionID <= 0 || report.Fingerprint == "" {
		return false
	}
	if report.CurrentFingerprint != "" && report.CurrentFingerprint != report.Fingerprint {
		return false
	}
	return true
}

// VersionCloseInput is the server-recomputed projection of one version.
// GD-17 removed the version.close work type, so BlockingWorkItems is the raw
// blocking count — there is no closure self-task to subtract. Paused waves and
// open human requests carry the waiting_human signal; work items in
// waiting_human are not counted separately (P3-D-25).
type VersionCloseInput struct {
	Version           Version
	BlockingWorkItems int
	ActiveIntents     int
	OpenHumanRequests int
	ActiveWaves       int
	InflightCaseRuns  int
	// HasExecutionData reports that the version has at least one case_run; a
	// version with no execution data has nothing to report or close with
	// (spec §5.3).
	HasExecutionData bool
	// UnconfirmedCases counts current confirmed case versions whose latest run
	// is missing or not terminal (passed/failed/blocked/inconclusive/skipped).
	// An explicit skipped run counts as executed. Only confirmed cases count:
	// unconfirmed drafts are the partial-confirmation path (D23).
	UnconfirmedCases int
	Requirements     []Requirement
	Report           ReportStatus
}

// EvaluateVersionClose recomputes the eight close conditions (spec §7.3). The
// §5.3 execution inputs are folded into the frozen eight check fields so the
// eight-check contract stays stable: missing execution data fails the report
// check, unexecuted confirmed cases fail the in-flight cases check.
func EvaluateVersionClose(in VersionCloseInput) ClosePredicate {
	requirementsClosed, _ := versionRequirementsClosed(in.Requirements)
	return ClosePredicate{
		Running:                in.Version.Status == StatusRunning,
		NoBlockingWorkItems:    in.BlockingWorkItems <= 0,
		NoActiveIntents:        in.ActiveIntents <= 0,
		NoOpenHumanRequests:    in.OpenHumanRequests <= 0,
		NoActiveWaves:          in.ActiveWaves <= 0,
		NoInflightCaseRuns:     in.InflightCaseRuns <= 0 && in.UnconfirmedCases <= 0,
		RequirementsClosed:     requirementsClosed,
		VersionReportPublished: VersionReportCurrent(in.Report) && in.HasExecutionData,
		HasExecutionData:       in.HasExecutionData,
		UnconfirmedCases:       in.UnconfirmedCases,
	}
}

// EvaluateVersionCloseWaived is the admin-waiver close predicate (GD-06/P23,
// R-G6/R-G8, SC-G11). The waiver overrides RequirementsClosed (requirement
// closure completeness, including AC coverage / CasesVerified) and the
// report-CURRENCY leg of check 8 — the unverified report is regenerated and
// published synchronously AFTER the close, so a missing or stale report must
// not block the waive. The six execution-settled checks (Running,
// NoBlockingWorkItems, NoActiveIntents, NoOpenHumanRequests, NoActiveWaves,
// NoInflightCaseRuns) still hold, and the version must have execution data:
// the mandatory post-close unverified report has nothing to render otherwise,
// so a version that never executed cannot be waived closed (fail-closed 409
// before the terminal write, never a completed version without a report).
func EvaluateVersionCloseWaived(in VersionCloseInput) ClosePredicate {
	predicate := EvaluateVersionClose(in)
	predicate.RequirementsClosed = true
	predicate.VersionReportPublished = in.HasExecutionData
	return predicate
}

// VersionCloseWaivedFailureCounts is the failure map of the waived predicate.
// It derives from the full failure counts and removes only the overridden
// legs: requirements_closed never reports, and version_report_published
// reports only when the version has no execution data at all.
func VersionCloseWaivedFailureCounts(in VersionCloseInput) map[string]int {
	failed := VersionCloseFailureCounts(in)
	delete(failed, VersionCloseCheckRequirementsClosed)
	if in.HasExecutionData {
		delete(failed, VersionCloseCheckVersionReportPublished)
	} else {
		failed[VersionCloseCheckVersionReportPublished] = 1
	}
	return failed
}

// VersionCloseFailureCounts returns the check-name to count map carried by
// ErrVersionClosePredicateFailed. Counts are the offending row counts; boolean
// failures report 1.
func VersionCloseFailureCounts(in VersionCloseInput) map[string]int {
	failed := make(map[string]int)
	if in.Version.Status != StatusRunning {
		failed[VersionCloseCheckRunning] = 1
	}
	if in.BlockingWorkItems > 0 {
		failed[VersionCloseCheckNoBlockingWorkItems] = in.BlockingWorkItems
	}
	if in.ActiveIntents > 0 {
		failed[VersionCloseCheckNoActiveIntents] = in.ActiveIntents
	}
	if in.OpenHumanRequests > 0 {
		failed[VersionCloseCheckNoOpenHumanRequests] = in.OpenHumanRequests
	}
	if in.ActiveWaves > 0 {
		failed[VersionCloseCheckNoActiveWaves] = in.ActiveWaves
	}
	if in.InflightCaseRuns > 0 || in.UnconfirmedCases > 0 {
		failed[VersionCloseCheckNoInflightCaseRuns] = in.InflightCaseRuns + in.UnconfirmedCases
	}
	if _, open := versionRequirementsClosed(in.Requirements); open > 0 {
		failed[VersionCloseCheckRequirementsClosed] = open
	}
	if !VersionReportCurrent(in.Report) || !in.HasExecutionData {
		failed[VersionCloseCheckVersionReportPublished] = 1
	}
	return failed
}

// VersionCloseFingerprint returns the sha256 hex of the canonical close
// predicate input (spec §7.3). It feeds the version.close task dedupe key, so
// any state change that can affect the predicate produces a new key.
func VersionCloseFingerprint(in VersionCloseInput) string {
	requirements := make([]canonicalCloseRequirement, 0, len(in.Requirements))
	for _, requirement := range in.Requirements {
		requirements = append(requirements, canonicalCloseRequirement{
			ID:     requirement.ID,
			Status: string(requirement.Status),
		})
	}
	sort.Slice(requirements, func(i, j int) bool { return requirements[i].ID < requirements[j].ID })
	payload := struct {
		VersionID         int64                       `json:"version_id"`
		VersionStatus     string                      `json:"version_status"`
		Requirements      []canonicalCloseRequirement `json:"requirements"`
		ReportRevisionID  int64                       `json:"report_revision_id"`
		ReportFingerprint string                      `json:"report_fingerprint"`
		ReportCurrent     bool                        `json:"report_current"`
		BlockingWorkItems int                         `json:"blocking_work_items"`
		ActiveIntents     int                         `json:"active_intents"`
		OpenHumanRequests int                         `json:"open_human_requests"`
		ActiveWaves       int                         `json:"active_waves"`
		InflightCaseRuns  int                         `json:"inflight_case_runs"`
		HasExecutionData  bool                        `json:"has_execution_data"`
		UnconfirmedCases  int                         `json:"unconfirmed_cases"`
	}{
		VersionID:         in.Version.ID,
		VersionStatus:     in.Version.Status,
		Requirements:      requirements,
		ReportRevisionID:  in.Report.RevisionID,
		ReportFingerprint: in.Report.Fingerprint,
		ReportCurrent:     VersionReportCurrent(in.Report),
		BlockingWorkItems: in.BlockingWorkItems,
		ActiveIntents:     in.ActiveIntents,
		OpenHumanRequests: in.OpenHumanRequests,
		ActiveWaves:       in.ActiveWaves,
		InflightCaseRuns:  in.InflightCaseRuns,
		HasExecutionData:  in.HasExecutionData,
		UnconfirmedCases:  in.UnconfirmedCases,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// canonicalCloseRequirement is the fixed-shape requirement projection used by
// the predicate fingerprint.
type canonicalCloseRequirement struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
}

// VersionCloseFields is the projection written when a version completes.
// ClosureKind/Unverified classify how the version closed (v0.2.4-G SC-G11):
// a normal full-predicate close writes ClosureKindNormal/false; an admin
// waiver close writes ClosureKindWaived/true through the SAME terminal Close
// entry. Reopen clears them back to normal/false.
type VersionCloseFields struct {
	ClosedAt    time.Time
	CloseReason string
	CloseRunID  *int64
	ClosureKind string
	Unverified  bool
}

// ErrVersionClosePredicateFailed is the stable error returned when the
// server-side version close predicate rejects a proposal. Failed maps each
// failing condition name to its count (spec §7.3).
type ErrVersionClosePredicateFailed struct {
	Failed map[string]int
}

// Error renders the failed conditions in stable name order.
func (e *ErrVersionClosePredicateFailed) Error() string {
	names := make([]string, 0, len(e.Failed))
	for name := range e.Failed {
		names = append(names, fmt.Sprintf("%s=%d", name, e.Failed[name]))
	}
	sort.Strings(names)
	return "version close predicate failed: " + strings.Join(names, ", ")
}

// versionRequirementsClosed reports whether every requirement is closed or
// obsolete and how many requirements are still open. A version without
// requirements passes vacuously.
func versionRequirementsClosed(requirements []Requirement) (bool, int) {
	open := 0
	for _, requirement := range requirements {
		if requirement.Status != RequirementClosed && requirement.Status != RequirementObsolete {
			open++
		}
	}
	return open == 0, open
}
