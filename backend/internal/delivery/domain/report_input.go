package delivery

import (
	"context"
	"time"
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
