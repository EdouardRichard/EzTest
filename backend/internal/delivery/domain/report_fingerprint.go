package delivery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"
)

// CanonicalJSON returns the canonical JSON encoding of the report input of
// spec §6.1. It is deliberately built from dedicated structs so the encoding
// has a fixed field order, never uses maps, normalizes nil slices to empty
// arrays and normalizes business timestamps to UTC. Every array is sorted by ID
// ascending; the renderer may display issues in descending order, but the
// fingerprint always uses ascending IDs. GeneratedAt (the as-of time) is
// excluded because as-of does not participate in the fingerprint.
func CanonicalJSON(snapshot ReportSnapshot) ([]byte, error) {
	payload := canonicalReport{
		Kind:         snapshot.Kind,
		VersionID:    snapshot.VersionID,
		WaveID:       snapshot.WaveID,
		Requirements: canonicalRequirements(snapshot.Requirements),
		Waves:        canonicalWaves(snapshot.Waves),
		CaseRuns:     canonicalCaseRuns(snapshot.CaseRuns),
		Issues:       canonicalIssues(snapshot.Issues),
		VersionSUTs:  canonicalVersionSUTs(snapshot.VersionSUTs),
		Coverage:     canonicalCoverage(snapshot.Coverage),
	}
	return json.Marshal(payload)
}

// Fingerprint returns the lowercase hex sha256 of the canonical JSON of the
// report input (spec §6.1). It returns an empty string only if the report input
// cannot be encoded, which cannot happen for the fixed snapshot shapes.
func Fingerprint(snapshot ReportSnapshot) string {
	canonical, err := CanonicalJSON(snapshot)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// canonicalReport is the fixed field order of the fingerprint payload. Do not
// reorder these fields: a reorder changes every fingerprint.
type canonicalReport struct {
	Kind         ReportKind              `json:"kind"`
	VersionID    int64                   `json:"version_id"`
	WaveID       *int64                  `json:"wave_id"`
	Requirements []canonicalRequirement  `json:"requirements"`
	Waves        []canonicalWave         `json:"waves"`
	CaseRuns     []canonicalCaseRun      `json:"case_runs"`
	Issues       []canonicalIssue        `json:"issues"`
	VersionSUTs  []canonicalVersionSUT   `json:"version_suts"`
	Coverage     canonicalReportCoverage `json:"coverage"`
}

type canonicalRequirement struct {
	ID               int64      `json:"id"`
	VersionID        int64      `json:"version_id"`
	CurrentVersionID *int64     `json:"current_version_id"`
	Title            string     `json:"title"`
	Status           string     `json:"status"`
	ClosureID        *int64     `json:"closure_id"`
	ClosedAt         *time.Time `json:"closed_at"`
	CloseRound       int32      `json:"close_round"`
	ReopenCount      int        `json:"reopen_count"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type canonicalWave struct {
	ID           int64                `json:"id"`
	VersionID    int64                `json:"version_id"`
	Status       string               `json:"status"`
	Trigger      string               `json:"trigger"`
	Counts       canonicalWaveCounts  `json:"counts"`
	ByVersionSUT []canonicalSUTCounts `json:"by_version_sut"`
	ReportID     *int64               `json:"report_id"`
	StartedAt    *time.Time           `json:"started_at"`
	FinishedAt   *time.Time           `json:"finished_at"`
}

type canonicalWaveCounts struct {
	Total        int `json:"total"`
	Pending      int `json:"pending"`
	Running      int `json:"running"`
	Paused       int `json:"paused"`
	Passed       int `json:"passed"`
	Failed       int `json:"failed"`
	Blocked      int `json:"blocked"`
	Inconclusive int `json:"inconclusive"`
	Skipped      int `json:"skipped"`
	Interrupted  int `json:"interrupted"`
	Superseded   int `json:"superseded"`
	Evidence     int `json:"evidence"`
}

type canonicalSUTCounts struct {
	VersionSUTID int64 `json:"version_sut_id"`
	Total        int   `json:"total"`
	Passed       int   `json:"passed"`
	Failed       int   `json:"failed"`
	Blocked      int   `json:"blocked"`
	Inconclusive int   `json:"inconclusive"`
}

type canonicalCaseRun struct {
	ID                   int64               `json:"id"`
	VersionID            int64               `json:"version_id"`
	WaveID               int64               `json:"wave_id"`
	CaseVersionID        int64               `json:"case_version_id"`
	CaseTitle            string              `json:"case_title"`
	VersionSUTID         *int64              `json:"version_sut_id"`
	VersionSUTIDs        []int64             `json:"version_sut_ids"`
	Status               string              `json:"status"`
	FailureClass         string              `json:"failure_class"`
	Summary              string              `json:"summary"`
	StepCount            int                 `json:"step_count"`
	StepFailedCount      int                 `json:"step_failed_count"`
	AssertionCount       int                 `json:"assertion_count"`
	AssertionFailedCount int                 `json:"assertion_failed_count"`
	FailedStepSeq        int                 `json:"failed_step_seq"`
	FailedAssertionSeq   int                 `json:"failed_assertion_seq"`
	Category             string              `json:"category"`
	Severity             string              `json:"severity"`
	Evidence             []canonicalEvidence `json:"evidence"`
	StartedAt            *time.Time          `json:"started_at"`
	FinishedAt           *time.Time          `json:"finished_at"`
	CreatedAt            time.Time           `json:"created_at"`
}

type canonicalEvidence struct {
	ID           int64  `json:"id"`
	CaseRunID    int64  `json:"case_run_id"`
	VersionSUTID *int64 `json:"version_sut_id"`
	StepSeq      *int   `json:"step_seq"`
	AssertionSeq *int   `json:"assertion_seq"`
	Kind         string `json:"kind"`
	SHA256       string `json:"sha256"`
	SizeBytes    int64  `json:"size_bytes"`
}

type canonicalIssue struct {
	ID             int64       `json:"id"`
	VersionID      int64       `json:"version_id"`
	Title          string      `json:"title"`
	Status         IssueStatus `json:"status"`
	Category       string      `json:"category"`
	Severity       string      `json:"severity"`
	Source         string      `json:"source"`
	MergedIntoID   *int64      `json:"merged_into_id"`
	SourceIssueIDs []int64     `json:"source_issue_ids"`
	CaseRunIDs     []int64     `json:"case_run_ids"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

type canonicalVersionSUT struct {
	ID              int64  `json:"id"`
	SUTID           int64  `json:"sut_id"`
	SUTName         string `json:"sut_name"`
	EnvironmentID   int64  `json:"environment_id"`
	EnvironmentName string `json:"environment_name"`
}

type canonicalReportCoverage struct {
	RequirementsTotal   int                    `json:"requirements_total"`
	RequirementsCovered int                    `json:"requirements_covered"`
	CaseVersionsTotal   int                    `json:"case_versions_total"`
	CaseVersionsCovered int                    `json:"case_versions_covered"`
	Gaps                []canonicalCoverageGap `json:"gaps"`
}

type canonicalCoverageGap struct {
	RequirementID int64  `json:"requirement_id"`
	CaseVersionID int64  `json:"case_version_id"`
	Reason        string `json:"reason"`
}

func canonicalRequirements(rows []RequirementSnapshot) []canonicalRequirement {
	canonical := make([]canonicalRequirement, 0, len(rows))
	for _, row := range rows {
		canonical = append(canonical, canonicalRequirement{
			ID:               row.ID,
			VersionID:        row.VersionID,
			CurrentVersionID: row.CurrentVersionID,
			Title:            row.Title,
			Status:           row.Status,
			ClosureID:        row.ClosureID,
			ClosedAt:         canonicalTimePtr(row.ClosedAt),
			CloseRound:       row.CloseRound,
			ReopenCount:      row.ReopenCount,
			UpdatedAt:        canonicalTime(row.UpdatedAt),
		})
	}
	sort.SliceStable(canonical, func(i, j int) bool { return canonical[i].ID < canonical[j].ID })
	return canonical
}

func canonicalWaves(rows []WaveSnapshot) []canonicalWave {
	canonical := make([]canonicalWave, 0, len(rows))
	for _, row := range rows {
		sutCounts := make([]canonicalSUTCounts, 0, len(row.ByVersionSUT))
		for _, counts := range row.ByVersionSUT {
			sutCounts = append(sutCounts, canonicalSUTCounts{
				VersionSUTID: counts.VersionSUTID,
				Total:        counts.Total,
				Passed:       counts.Passed,
				Failed:       counts.Failed,
				Blocked:      counts.Blocked,
				Inconclusive: counts.Inconclusive,
			})
		}
		sort.SliceStable(sutCounts, func(i, j int) bool { return sutCounts[i].VersionSUTID < sutCounts[j].VersionSUTID })
		canonical = append(canonical, canonicalWave{
			ID:        row.ID,
			VersionID: row.VersionID,
			Status:    row.Status,
			Trigger:   row.Trigger,
			Counts: canonicalWaveCounts{
				Total:        row.Counts.Total,
				Pending:      row.Counts.Pending,
				Running:      row.Counts.Running,
				Paused:       row.Counts.Paused,
				Passed:       row.Counts.Passed,
				Failed:       row.Counts.Failed,
				Blocked:      row.Counts.Blocked,
				Inconclusive: row.Counts.Inconclusive,
				Skipped:      row.Counts.Skipped,
				Interrupted:  row.Counts.Interrupted,
				Superseded:   row.Counts.Superseded,
				Evidence:     row.Counts.Evidence,
			},
			ByVersionSUT: sutCounts,
			ReportID:     row.ReportID,
			StartedAt:    canonicalTimePtr(row.StartedAt),
			FinishedAt:   canonicalTimePtr(row.FinishedAt),
		})
	}
	sort.SliceStable(canonical, func(i, j int) bool { return canonical[i].ID < canonical[j].ID })
	return canonical
}

func canonicalCaseRuns(rows []CaseRunSnapshot) []canonicalCaseRun {
	canonical := make([]canonicalCaseRun, 0, len(rows))
	for _, row := range rows {
		canonical = append(canonical, canonicalCaseRun{
			ID:                   row.ID,
			VersionID:            row.VersionID,
			WaveID:               row.WaveID,
			CaseVersionID:        row.CaseVersionID,
			CaseTitle:            row.CaseTitle,
			VersionSUTID:         row.VersionSUTID,
			VersionSUTIDs:        sortedInt64s(row.VersionSUTIDs),
			Status:               row.Status,
			FailureClass:         row.FailureClass,
			Summary:              row.Summary,
			StepCount:            row.StepCount,
			StepFailedCount:      row.StepFailedCount,
			AssertionCount:       row.AssertionCount,
			AssertionFailedCount: row.AssertionFailedCount,
			FailedStepSeq:        row.FailedStepSeq,
			FailedAssertionSeq:   row.FailedAssertionSeq,
			Category:             row.Category,
			Severity:             row.Severity,
			Evidence:             canonicalEvidenceRows(row.Evidence),
			StartedAt:            canonicalTimePtr(row.StartedAt),
			FinishedAt:           canonicalTimePtr(row.FinishedAt),
			CreatedAt:            canonicalTime(row.CreatedAt),
		})
	}
	sort.SliceStable(canonical, func(i, j int) bool { return canonical[i].ID < canonical[j].ID })
	return canonical
}

func canonicalEvidenceRows(rows []EvidenceSnapshot) []canonicalEvidence {
	canonical := make([]canonicalEvidence, 0, len(rows))
	for _, row := range rows {
		canonical = append(canonical, canonicalEvidence{
			ID:           row.ID,
			CaseRunID:    row.CaseRunID,
			VersionSUTID: row.VersionSUTID,
			StepSeq:      row.StepSeq,
			AssertionSeq: row.AssertionSeq,
			Kind:         row.Kind,
			SHA256:       row.SHA256,
			SizeBytes:    row.SizeBytes,
		})
	}
	sort.SliceStable(canonical, func(i, j int) bool { return canonical[i].ID < canonical[j].ID })
	return canonical
}

func canonicalIssues(rows []IssueSnapshot) []canonicalIssue {
	canonical := make([]canonicalIssue, 0, len(rows))
	for _, row := range rows {
		canonical = append(canonical, canonicalIssue{
			ID:             row.ID,
			VersionID:      row.VersionID,
			Title:          row.Title,
			Status:         row.Status,
			Category:       row.Category,
			Severity:       row.Severity,
			Source:         row.Source,
			MergedIntoID:   row.MergedIntoID,
			SourceIssueIDs: sortedInt64s(row.SourceIssueIDs),
			CaseRunIDs:     sortedInt64s(row.CaseRunIDs),
			CreatedAt:      canonicalTime(row.CreatedAt),
			UpdatedAt:      canonicalTime(row.UpdatedAt),
		})
	}
	sort.SliceStable(canonical, func(i, j int) bool { return canonical[i].ID < canonical[j].ID })
	return canonical
}

func canonicalVersionSUTs(rows []VersionSUTSnapshot) []canonicalVersionSUT {
	canonical := make([]canonicalVersionSUT, 0, len(rows))
	for _, row := range rows {
		canonical = append(canonical, canonicalVersionSUT{
			ID:              row.ID,
			SUTID:           row.SUTID,
			SUTName:         row.SUTName,
			EnvironmentID:   row.EnvironmentID,
			EnvironmentName: row.EnvironmentName,
		})
	}
	sort.SliceStable(canonical, func(i, j int) bool { return canonical[i].ID < canonical[j].ID })
	return canonical
}

func canonicalCoverage(coverage CoverageSnapshot) canonicalReportCoverage {
	gaps := make([]canonicalCoverageGap, 0, len(coverage.Gaps))
	for _, gap := range coverage.Gaps {
		gaps = append(gaps, canonicalCoverageGap{
			RequirementID: gap.RequirementID,
			CaseVersionID: gap.CaseVersionID,
			Reason:        gap.Reason,
		})
	}
	sort.SliceStable(gaps, func(i, j int) bool {
		if gaps[i].RequirementID != gaps[j].RequirementID {
			return gaps[i].RequirementID < gaps[j].RequirementID
		}
		if gaps[i].CaseVersionID != gaps[j].CaseVersionID {
			return gaps[i].CaseVersionID < gaps[j].CaseVersionID
		}
		return gaps[i].Reason < gaps[j].Reason
	})
	return canonicalReportCoverage{
		RequirementsTotal:   coverage.RequirementsTotal,
		RequirementsCovered: coverage.RequirementsCovered,
		CaseVersionsTotal:   coverage.CaseVersionsTotal,
		CaseVersionsCovered: coverage.CaseVersionsCovered,
		Gaps:                gaps,
	}
}

// sortedInt64s returns a sorted, non-nil copy so nil and empty slices encode
// identically and input order never changes the fingerprint.
func sortedInt64s(values []int64) []int64 {
	sorted := make([]int64, len(values))
	copy(sorted, values)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted
}

func canonicalTime(value time.Time) time.Time {
	return value.UTC()
}

func canonicalTimePtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}
