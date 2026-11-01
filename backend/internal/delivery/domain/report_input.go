package delivery

import (
	"context"
	"sort"
	"time"
)

// Report input size caps (spec §6.1). A report never includes more than these
// rows of each list; the capped lists keep the first N by ascending ID and the
// snapshot records the truncation so the renderer can state it visibly.
const (
	MaxReportIssues   = 500
	MaxReportWaves    = 200
	MaxReportCaseRuns = 2000
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
// false section is omitted from the HTML entirely.
type SectionOptions struct {
	Summary  bool
	Results  bool
	Issues   bool
	Coverage bool
	Evidence bool
}

// ReportRenderer renders one report revision from a deterministic snapshot and
// an optional plain-text narrative. The application layer depends on this
// interface; infrastructure/render implements it.
type ReportRenderer interface {
	Render(ctx context.Context, snapshot ReportSnapshot, narrative string, opts SectionOptions) ([]byte, error)
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
	ID                   int64
	VersionID            int64
	WaveID               int64
	CaseVersionID        int64
	CaseTitle            string
	VersionSUTID         *int64
	VersionSUTIDs        []int64
	Status               string
	FailureClass         string
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
	CreatedAt      time.Time
	UpdatedAt      time.Time
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
// (spec §6.1).
type CoverageSnapshot struct {
	RequirementsTotal   int
	RequirementsCovered int
	CaseVersionsTotal   int
	CaseVersionsCovered int
	Gaps                []CoverageGapSnapshot
}

// CoverageGapSnapshot is one uncovered case version.
type CoverageGapSnapshot struct {
	RequirementID int64
	CaseVersionID int64
	Reason        string
}
