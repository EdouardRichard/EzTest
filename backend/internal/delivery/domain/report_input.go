package delivery

import (
	"context"
	"sort"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// Report input size caps (spec §6.1). A report never includes more than these
// rows of each list; the capped lists keep the first N by ascending ID and the
// snapshot records the truncation so the renderer can state it visibly.
const (
	MaxReportIssues   = 500
	MaxReportWaves    = 200
	MaxReportCaseRuns = 2000
)

// Report-visible triage/coverage classifications (Task 118, Task 122). The
// platform issue triage is authoritative for the failure conclusion: a
// failed(application) case_run whose issue triage is done with
// classification=flaky is automation flakiness, and one classified
// expected_failure (for example a run that stopped on a real token budget
// before the step under observation) is an expected failure that must be rerun
// rather than a product defect. The raw P3 failure_class stays available on
// the snapshot; the report-visible value is derived through
// CaseRunSnapshot.EffectiveFailureClass.
const (
	// TriageClassificationFlaky is the issue.triage classification that marks a
	// failure as flaky/automation instead of a product defect.
	TriageClassificationFlaky = "flaky"
	// TriageClassificationExpectedFailure is the issue.triage classification
	// that marks a failure as an expected/not-judged failure (budget or missing
	// observation) instead of a product defect.
	TriageClassificationExpectedFailure = "expected_failure"
	// FailureClassAutomation is the report-visible failure class of a run whose
	// issue triage is flaky.
	FailureClassAutomation = "automation"
	// FailureClassExpectedFailure is the report-visible failure class of a run
	// whose issue triage is expected_failure. It is deliberately not
	// "application" and not "automation": the run is neither a product defect
	// nor flaky automation noise; it needs a rerun to produce a conclusion.
	FailureClassExpectedFailure = "expected_failure"
	// FailureClassBlocked is the report-visible failure class of a run blocked
	// by an executor capability limitation (v0.2.3 X-02). It stays blocked: the
	// executor could not perform the action, so it is never a SUT defect.
	FailureClassBlocked = "blocked"
	// TerminalCauseExecutorCapability is the shared stable cause of a run blocked
	// by an executor capability limitation. Execution, issue candidates and the
	// report/triage vocabulary use the same code (no DB enum/migration); the
	// execution domain owns the constant.
	TerminalCauseExecutorCapability = execution.TerminalCauseExecutorCapability
	// CoverageGapExecutionFlaky explains an uncovered case version whose latest
	// run failed but the platform triage classified that failure as flaky. It
	// is a flaky gap, not a product coverage failure.
	CoverageGapExecutionFlaky = "execution_flaky_coverage"
)

// Stable section names of ReportSnapshot.TruncatedSections.
const (
	ReportTruncationIssues   = "issues"
	ReportTruncationWaves    = "waves"
	ReportTruncationCaseRuns = "case_runs"
)

// ReportSnapshot is the deterministic report input of spec §6.1. It is built
// from application ports before rendering and is the single source of the
// report input fingerprint. Field order is part of the canonical contract and
// must not be rearranged. GeneratedAt is the generation as-of time and does not
// participate in the fingerprint (spec §6.1).
type ReportSnapshot struct {
	Kind         ReportKind
	VersionID    int64
	WaveID       *int64
	Requirements []RequirementSnapshot
	Waves        []WaveSnapshot
	CaseRuns     []CaseRunSnapshot
	Issues       []IssueSnapshot
	VersionSUTs  []VersionSUTSnapshot
	Coverage     CoverageSnapshot
	GeneratedAt  time.Time
	// ReportID/ReportRevision/InputFingerprint are the P7 render metadata of
	// the revision being rendered. They feed the template v2 appendix only and
	// are deliberately not part of the canonical fingerprint payload
	// (report_fingerprint.go builds its own fixed struct), so they can never
	// change an input fingerprint.
	ReportID         int64
	ReportRevision   int32
	InputFingerprint string
	// TruncatedSections lists the input lists reduced to their §6.1 cap, in a
	// stable order (issues, waves, case_runs). It is derived metadata, not part
	// of the fingerprint payload; the renderer prints it as a visible
	// truncation note. A ReportDataReader that already capped a list at the
	// boundary sets the matching entry because the service cannot infer
	// overflow from an exactly-capped list.
	TruncatedSections []string
}

// CapReportSnapshot enforces the spec §6.1 report input caps: at most 500
// issues, 200 waves and 2000 case_runs, keeping the first N by ascending ID.
// The input is not mutated. TruncatedSections is merged with any truncation
// markers a ReportDataReader already reported, so a pre-capped list at the
// boundary still renders its visible note.
func CapReportSnapshot(snapshot ReportSnapshot) ReportSnapshot {
	sections := append([]string(nil), snapshot.TruncatedSections...)
	var cappedIssues, cappedWaves, cappedCaseRuns bool
	snapshot.Issues, cappedIssues = capReportRows(snapshot.Issues, MaxReportIssues, func(row IssueSnapshot) int64 { return row.ID })
	snapshot.Waves, cappedWaves = capReportRows(snapshot.Waves, MaxReportWaves, func(row WaveSnapshot) int64 { return row.ID })
	snapshot.CaseRuns, cappedCaseRuns = capReportRows(snapshot.CaseRuns, MaxReportCaseRuns, func(row CaseRunSnapshot) int64 { return row.ID })
	if cappedIssues {
		sections = append(sections, ReportTruncationIssues)
	}
	if cappedWaves {
		sections = append(sections, ReportTruncationWaves)
	}
	if cappedCaseRuns {
		sections = append(sections, ReportTruncationCaseRuns)
	}
	snapshot.TruncatedSections = canonicalTruncationSections(sections)
	return snapshot
}

// capReportRows returns rows unchanged when in budget and otherwise a copy
// sorted by ascending ID truncated to limit. Slicing a sorted copy keeps the
// original snapshot (and therefore reader caches) untouched.
func capReportRows[T any](rows []T, limit int, id func(T) int64) ([]T, bool) {
	if len(rows) <= limit {
		return rows, false
	}
	sorted := make([]T, len(rows))
	copy(sorted, rows)
	sort.SliceStable(sorted, func(i, j int) bool { return id(sorted[i]) < id(sorted[j]) })
	return sorted[:limit], true
}

// canonicalTruncationSections deduplicates markers and orders the known
// sections canonically (issues, waves, case_runs) so the rendered note is
// deterministic; unknown reader-provided markers follow in input order.
func canonicalTruncationSections(sections []string) []string {
	if len(sections) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(sections))
	result := make([]string, 0, len(sections))
	for _, name := range []string{ReportTruncationIssues, ReportTruncationWaves, ReportTruncationCaseRuns} {
		for _, candidate := range sections {
			if candidate == name && !seen[name] {
				seen[name] = true
				result = append(result, name)
			}
		}
	}
	for _, name := range sections {
		if !seen[name] {
			seen[name] = true
			result = append(result, name)
		}
	}
	return result
}

// SectionOptions controls which report sections are rendered (spec §6.2). A
// false section is omitted from the HTML entirely. Appendix only exists in
// template version 2 (report.section_appendix); template v1 ignores it so its
// rendered bytes stay frozen.
type SectionOptions struct {
	Summary  bool
	Results  bool
	Issues   bool
	Coverage bool
	Evidence bool
	Appendix bool
}

// ReportRenderer renders one report revision from a deterministic snapshot and
// an optional plain-text narrative. The application layer depends on this
// interface; infrastructure/render implements it.
type ReportRenderer interface {
	Render(ctx context.Context, snapshot ReportSnapshot, narrative string, opts SectionOptions) ([]byte, error)
}

// ReportTemplateVersioner is an optional extension of ReportRenderer. A
// renderer that knows the built-in template version it renders reports it here,
// so a new revision records exactly the version its bytes were produced with
// (P7-D-03). A renderer without this method keeps the frozen P4 version "1".
type ReportTemplateVersioner interface {
	TemplateVersion() string
}

// RequirementSnapshot is one version requirement projection with its latest
// closure summary (spec §6.1/§7.2).
type RequirementSnapshot struct {
	ID               int64
	VersionID        int64
	CurrentVersionID *int64
	Title            string
	Status           string
	ClosureID        *int64
	ClosedAt         *time.Time
	CloseRound       int32
	ReopenCount      int
	UpdatedAt        time.Time
	// ExecutionSettled is derived execution metadata: true when the requirement
	// has at least one current+confirmed case version and every such case
	// version's latest non-superseded case_run is terminal (blocked,
	// inconclusive and skipped count as terminal). It is deliberately NOT part of
	// the canonical fingerprint payload (report_fingerprint.go): adding it there
	// would change every stored fingerprint and mass-mark published reports
	// stale. The report due gate uses it so a requirement whose closure predicate
	// can never pass (blocked/inconclusive case, failed case without an issue
	// yet) does not block the version report forever (R2-NEW-02).
	ExecutionSettled bool
}

// WaveSnapshot is one wave projection of a report. Counts are fixed fields (no
// maps) so they can be fingerprinted deterministically; ByVersionSUT is sorted
// by version_sut ID before hashing and rendering.
type WaveSnapshot struct {
	ID           int64
	VersionID    int64
	Status       string
	Trigger      string
	Counts       WaveCountsSnapshot
	ByVersionSUT []SUTCountsSnapshot
	ReportID     *int64
	StartedAt    *time.Time
	FinishedAt   *time.Time
}

// WaveCountsSnapshot is the fixed projection of run_wave.counts (spec §6.1).
type WaveCountsSnapshot struct {
	Total        int
	Pending      int
	Running      int
	Paused       int
	Passed       int
	Failed       int
	Blocked      int
	Inconclusive int
	Skipped      int
	Interrupted  int
	Superseded   int
	Evidence     int
}

// SUTCountsSnapshot is one version SUT's section of a wave's by_version_sut
// counts (spec §6.1).
type SUTCountsSnapshot struct {
	VersionSUTID int64
	Total        int
	Passed       int
	Failed       int
	Blocked      int
	Inconclusive int
}

// CaseRunSnapshot is one case_run projection of a report with its failure
// position, step/assertion counters and evidence metadata (spec §6.1).
type CaseRunSnapshot struct {
	ID            int64
	VersionID     int64
	WaveID        int64
	CaseVersionID int64
	// RequirementID is the design requirement the case version belongs to. It
	// connects a case result with the per-requirement coverage projection and
	// is derived metadata: it is not part of the canonical fingerprint payload.
	RequirementID int64
	CaseTitle     string
	VersionSUTID  *int64
	VersionSUTIDs []int64
	Status        string
	FailureClass  string
	// TerminalCause is the execution-persisted stable cause of the run
	// (case_run.summary.terminal_cause). The report/triage vocabulary shares it
	// with execution: executor_capability marks a run blocked by an executor
	// capability limitation, never a SUT defect (v0.2.3 X-02).
	TerminalCause string
	// TriageClassification is the latest finished platform triage
	// classification of the issue that references this run (empty when none).
	// A flaky classification overrides the report-visible classification; the
	// raw FailureClass remains the execution fact.
	TriageClassification string
	Summary              string
	StepCount            int
	StepFailedCount      int
	AssertionCount       int
	AssertionFailedCount int
	FailedStepSeq        int
	FailedAssertionSeq   int
	Category             string
	Severity             string
	Evidence             []EvidenceSnapshot
	StartedAt            *time.Time
	FinishedAt           *time.Time
	CreatedAt            time.Time
}

// EffectiveFailureClass is the failure class a report presents. It is the raw
// execution class unless the platform triage finished with classification=flaky
// (automation flakiness) or classification=expected_failure (expected failure,
// budget/no-judgment, needs a rerun). Neither triage classification is a
// product/application defect (Task 118, Task 122).
func (row CaseRunSnapshot) EffectiveFailureClass() string {
	if row.ExecutorCapabilityBlocked() {
		// An executor capability block is not a product defect and no triage
		// classification may turn it into one (X-02).
		return FailureClassBlocked
	}
	switch row.TriageClassification {
	case TriageClassificationFlaky:
		return FailureClassAutomation
	case TriageClassificationExpectedFailure:
		return FailureClassExpectedFailure
	}
	return row.FailureClass
}

// ExecutorCapabilityBlocked reports whether the run was blocked by an executor
// capability limitation rather than a SUT defect. Execution and the issue
// candidate gate share the same terminal cause vocabulary (v0.2.3 X-02).
func (row CaseRunSnapshot) ExecutorCapabilityBlocked() bool {
	return row.TerminalCause == TerminalCauseExecutorCapability
}

// ExpectedFailureTriage reports whether the latest finished issue triage
// classified this run as expected_failure. The report summary counts it
// separately from product defects.
func (row CaseRunSnapshot) ExpectedFailureTriage() bool {
	return row.TriageClassification == TriageClassificationExpectedFailure
}

// EvidenceSnapshot is one evidence metadata row of a report. Content bytes and
// storage paths are deliberately absent (spec §6.1).
type EvidenceSnapshot struct {
	ID           int64
	CaseRunID    int64
	VersionSUTID *int64
	StepSeq      *int
	AssertionSeq *int
	Kind         string
	SHA256       string
	SizeBytes    int64
}

// IssueSnapshot is one delivery issue projection of a report. SourceIssueIDs
// lists issues merged into this one; CaseRunIDs lists its case_run refs (spec
// §6.1).
type IssueSnapshot struct {
	ID             int64
	VersionID      int64
	Title          string
	Status         IssueStatus
	Category       string
	Severity       string
	Source         string
	MergedIntoID   *int64
	SourceIssueIDs []int64
	CaseRunIDs     []int64
	// TriageStatus/TriageClassification are the latest finished issue.triage
	// projection. They are display metadata (the issue row itself is never
	// rewritten by a triage proposal) and let the report honor a flaky
	// classification instead of presenting the issue as a product defect.
	TriageStatus         string
	TriageClassification string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// FlakyTriage reports whether the latest finished triage classified this issue
// as flaky. Only a done proposal is authoritative: a degraded/failed run or a
// non-flaky classification never downgrades a real application defect.
func (row IssueSnapshot) FlakyTriage() bool {
	return row.TriageStatus == string(AnalysisStatusDone) &&
		row.TriageClassification == TriageClassificationFlaky
}

// ExpectedFailureTriage reports whether the latest finished triage classified
// this issue as expected_failure. Like FlakyTriage, only a done proposal is
// authoritative; the stored issue row itself is never rewritten.
func (row IssueSnapshot) ExpectedFailureTriage() bool {
	return row.TriageStatus == string(AnalysisStatusDone) &&
		row.TriageClassification == TriageClassificationExpectedFailure
}

// OpenProductIssue reports whether this issue is an open issue that the platform
// triage did not classify as flaky or expected_failure, i.e. the issue counts as
// an open product defect in the report summary (Task 122). The stored status is
// never changed by a triage proposal; this is a report-visible projection.
func (row IssueSnapshot) OpenProductIssue() bool {
	return row.Status == IssueStatusOpen && !row.FlakyTriage() && !row.ExpectedFailureTriage()
}

// VersionSUTSnapshot is one version SUT binding used to label report rows
// (spec §6.1).
type VersionSUTSnapshot struct {
	ID              int64
	SUTID           int64
	SUTName         string
	EnvironmentID   int64
	EnvironmentName string
}

// CoverageSnapshot is the P3 execution coverage projection of a version
// (spec §6.1). It counts the newest non-superseded case_run of every executable
// case version: a superseded process row is not a result.
type CoverageSnapshot struct {
	RequirementsTotal   int
	RequirementsCovered int
	CaseVersionsTotal   int
	CaseVersionsCovered int
	Gaps                []CoverageGapSnapshot
	// ByRequirement is the per-requirement projection of the same executable
	// case versions and latest runs (see RequirementCoverageSnapshot). It is
	// derived metadata: the canonical fingerprint already covers the case
	// versions, their latest runs and the gaps, so this field is deliberately
	// absent from the fingerprint payload. Report narrative validation uses it
	// to prove statements such as "no valid execution" wrong; the report writer
	// tool exposes its counts so the model quotes instead of inventing them.
	ByRequirement []RequirementCoverageSnapshot
}

// RequirementCoverageSnapshot is the deterministic coverage of one requirement,
// derived strictly from its executable case versions and their latest case_run
// statuses. CaseVersionsTotal always equals the sum of the four outcome
// counters: covered, not run, product failed coverage and flaky coverage.
type RequirementCoverageSnapshot struct {
	RequirementID              int64
	CaseVersionsTotal          int
	CaseVersionsCovered        int
	CaseVersionsNotRun         int
	CaseVersionsFailedCoverage int
	// CaseVersionsFlakyCoverage counts failed latest runs whose issue triage is
	// flaky. They stay uncovered (a failure is never a pass) but are not a
	// product coverage failure.
	CaseVersionsFlakyCoverage int
}

// CoverageGapSnapshot is one uncovered case version.
type CoverageGapSnapshot struct {
	RequirementID int64
	CaseVersionID int64
	Reason        string
}
