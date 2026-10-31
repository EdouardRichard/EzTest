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

// Version close input limits. The Steward tool accepts reason(1..500); the
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
type ClosePredicate struct {
	Running                bool
	NoBlockingWorkItems    bool
	NoActiveIntents        bool
	NoOpenHumanRequests    bool
	NoActiveWaves          bool
	NoInflightCaseRuns     bool
	RequirementsClosed     bool
	VersionReportPublished bool
}

// Passed reports whether every close condition is green.
func (p ClosePredicate) Passed() bool {
	return p.Running && p.NoBlockingWorkItems && p.NoActiveIntents &&
		p.NoOpenHumanRequests && p.NoActiveWaves && p.NoInflightCaseRuns &&
		p.RequirementsClosed && p.VersionReportPublished
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

// VersionCloseInput is the server-recomputed projection of one version. The
// service subtracts the version's own version.close work item from
// BlockingWorkItems before evaluating, so the predicate only sees the effective
// count. Paused waves and open human requests carry the waiting_human signal;
// work items in waiting_human are not counted separately (P3-D-25).
type VersionCloseInput struct {
	Version           Version
	BlockingWorkItems int
	ActiveIntents     int
	OpenHumanRequests int
	ActiveWaves       int
	InflightCaseRuns  int
	Requirements      []Requirement
	Report            ReportStatus
}

// EvaluateVersionClose recomputes the eight close conditions (spec §7.3).
func EvaluateVersionClose(in VersionCloseInput) ClosePredicate {
	requirementsClosed, _ := versionRequirementsClosed(in.Requirements)
	return ClosePredicate{
		Running:                in.Version.Status == StatusRunning,
		NoBlockingWorkItems:    in.BlockingWorkItems <= 0,
		NoActiveIntents:        in.ActiveIntents <= 0,
		NoOpenHumanRequests:    in.OpenHumanRequests <= 0,
		NoActiveWaves:          in.ActiveWaves <= 0,
		NoInflightCaseRuns:     in.InflightCaseRuns <= 0,
		RequirementsClosed:     requirementsClosed,
		VersionReportPublished: VersionReportCurrent(in.Report),
	}
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
	if in.InflightCaseRuns > 0 {
		failed[VersionCloseCheckNoInflightCaseRuns] = in.InflightCaseRuns
	}
	if _, open := versionRequirementsClosed(in.Requirements); open > 0 {
		failed[VersionCloseCheckRequirementsClosed] = open
	}
	if !VersionReportCurrent(in.Report) {
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
type VersionCloseFields struct {
	ClosedAt    time.Time
	CloseReason string
	CloseRunID  *int64
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
