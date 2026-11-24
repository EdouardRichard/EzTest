package delivery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"
)

// CanonicalJSON returns the canonical JSON encoding of the report input
// (v0.2.4-G P22 redefinition of spec §6.1). It is deliberately built from
// dedicated structs so the encoding has a fixed field order, never uses maps,
// normalizes nil slices to empty arrays and normalizes business timestamps to
// UTC. Every array is sorted by ID ascending; the renderer may display issues
// in descending order, but the fingerprint always uses ascending IDs.
//
// Conclusion inputs only (art.4.6, R-G3): mutable row-modification timestamps
// (updated_at) are EXCLUDED — they are not conclusion inputs and would
// mass-mark reports stale on unrelated row touches. Immutable creation times,
// triage, terminal_cause, per-run actual environment fingerprints, AC hashes,
// the version closure classification (closure_kind/unverified) and per-run
// evidence digests ARE included. GeneratedAt (the as-of time) is excluded
// because as-of does not participate in the fingerprint.
func CanonicalJSON(snapshot ReportSnapshot) ([]byte, error) {
	caseRuns, err := canonicalCaseRuns(snapshot.CaseRuns)
	if err != nil {
		return nil, err
	}
	payload := canonicalReport{
		Kind:         snapshot.Kind,
		VersionID:    snapshot.VersionID,
		WaveID:       snapshot.WaveID,
		Requirements: canonicalRequirements(snapshot.Requirements),
		Waves:        canonicalWaves(snapshot.Waves),
		CaseRuns:     caseRuns,
		Issues:       canonicalIssues(snapshot.Issues),
		SUTs:         canonicalSUTs(snapshot.SUTs),
		Coverage:     canonicalCoverage(snapshot.Coverage),
		ClosureKind:  snapshot.ClosureKind,
		Unverified:   snapshot.Unverified,
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
// reorder these fields: a reorder changes every fingerprint. The v0.2.4-G P22
// redefinition is a BREAKING fingerprint change (GD-05): every pre-G stored
// fingerprint differs, so published reports are detected stale once and
// regenerated — expected, disclosed in the release notes.
//
// Fixed order (v0.2.4-G): kind, version_id, wave_id, requirements, waves,
// case_runs, issues, suts, coverage, closure_kind, unverified. The version
// closure classification is appended after the pre-G fields (pure extension
// of the top-level order; the nested payloads changed per P22).
type canonicalReport struct {
	Kind         ReportKind              `json:"kind"`
	VersionID    int64                   `json:"version_id"`
	WaveID       *int64                  `json:"wave_id"`
	Requirements []canonicalRequirement  `json:"requirements"`
	Waves        []canonicalWave         `json:"waves"`
	CaseRuns     []canonicalCaseRun      `json:"case_runs"`
	Issues       []canonicalIssue        `json:"issues"`
	SUTs         []canonicalSUT          `json:"suts"`
	Coverage     canonicalReportCoverage `json:"coverage"`
	ClosureKind  string                  `json:"closure_kind"`
	Unverified   bool                    `json:"unverified"`
}

// canonicalRequirement: pre-G order minus updated_at (R-G3), plus waived and
// ac_hash appended (requirement waiver overrides the closure predicate; AC
// content is quoted by report conclusions — both are conclusion inputs).
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
	Waived           bool       `json:"waived"`
	ACHash           string     `json:"ac_hash"`
}

type canonicalWave struct {
	ID         int64                `json:"id"`
	VersionID  int64                `json:"version_id"`
	Status     string               `json:"status"`
	Trigger    string               `json:"trigger"`
	Counts     canonicalWaveCounts  `json:"counts"`
	BySUT      []canonicalSUTCounts `json:"by_sut"`
	ReportID   *int64               `json:"report_id"`
	StartedAt  *time.Time           `json:"started_at"`
	FinishedAt *time.Time           `json:"finished_at"`
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
	SUTID        int64 `json:"sut_id"`
	Total        int   `json:"total"`
	Passed       int   `json:"passed"`
	Failed       int   `json:"failed"`
	Blocked      int   `json:"blocked"`
	Inconclusive int   `json:"inconclusive"`
}

// canonicalCaseRun: pre-G order plus terminal_cause (after failure_class,
// R-TERMINALCAUSE), per-run actual environment_fingerprint/code_baseline
// (after severity, R-G1) and the per-run evidence digest replacing the
// per-evidence list (R-G2).
type canonicalCaseRun struct {
	ID                     int64                   `json:"id"`
	VersionID              int64                   `json:"version_id"`
	WaveID                 int64                   `json:"wave_id"`
	CaseVersionID          int64                   `json:"case_version_id"`
	CaseTitle              string                  `json:"case_title"`
	SUTID                  *int64                  `json:"sut_id"`
	SUTIDs                 []int64                 `json:"sut_ids"`
	Status                 string                  `json:"status"`
	FailureClass           string                  `json:"failure_class"`
	TerminalCause          string                  `json:"terminal_cause"`
	TriageClassification   string                  `json:"triage_classification"`
	Summary                string                  `json:"summary"`
	StepCount              int                     `json:"step_count"`
	StepFailedCount        int                     `json:"step_failed_count"`
	AssertionCount         int                     `json:"assertion_count"`
	AssertionFailedCount   int                     `json:"assertion_failed_count"`
	FailedStepSeq          int                     `json:"failed_step_seq"`
	FailedAssertionSeq     int                     `json:"failed_assertion_seq"`
	Category               string                  `json:"category"`
	Severity               string                  `json:"severity"`
	EnvironmentFingerprint string                  `json:"environment_fingerprint"`
	CodeBaseline           string                  `json:"code_baseline"`
	EvidenceDigest         canonicalEvidenceDigest `json:"evidence_digest"`
	StartedAt              *time.Time              `json:"started_at"`
	FinishedAt             *time.Time              `json:"finished_at"`
	CreatedAt              time.Time               `json:"created_at"`
}

// canonicalEvidenceDigest is the per-run evidence member of the fingerprint
// (R-G2): sha256 over the canonical JSON array of the run's SORTED, DEDUPLICATED
// evidence sha256 set, plus the evidence row count and the persisted dropped
// count. The payload is O(run count), never O(evidence count), and causally
// complete (art.4.6): any evidence content change changes the set digest, any
// population change changes the count, any drop changes dropped_count — the
// digest covers ALL evidence, it is not a capped sample.
//
// Binding-dimension decision (M1, verified): evidence rows are INSERT-only —
// no UPDATE of evidence (incl. step_seq/assertion_seq bindings) exists anywhere
// in the backend, and GC never deletes report-referenced content. A binding
// therefore cannot drift for an existing row; the only population changes are
// insertions, which the count captures. The per-evidence binding dimension
// (id/step_seq/assertion_seq/kind/size/sut) is deliberately NOT hashed: content
// causality is fully preserved by set digest + count + dropped_count, while
// per-evidence fields would make the payload O(evidence count) again (R-G2).
// Residual, accepted: an identical-content, identical-count row replacement
// across different bindings is indistinguishable — no production path performs
// one (insert-only evidence + report-content GC protection).
type canonicalEvidenceDigest struct {
	SHA256SetDigest string `json:"sha256_set_digest"`
	Count           int    `json:"count"`
	DroppedCount    int    `json:"dropped_count"`
}

// canonicalIssue: pre-G order minus updated_at (R-G3), plus dedupe_key
// (design §7.4 lists dedupe among the issue conclusion inputs). Triage stays:
// it is a conclusion input (the report-visible failure classification).
type canonicalIssue struct {
	ID                   int64       `json:"id"`
	VersionID            int64       `json:"version_id"`
	Title                string      `json:"title"`
	Status               IssueStatus `json:"status"`
	Category             string      `json:"category"`
	Severity             string      `json:"severity"`
	Source               string      `json:"source"`
	TriageStatus         string      `json:"triage_status"`
	TriageClassification string      `json:"triage_classification"`
	MergedIntoID         *int64      `json:"merged_into_id"`
	SourceIssueIDs       []int64     `json:"source_issue_ids"`
	CaseRunIDs           []int64     `json:"case_run_ids"`
	DedupeKey            string      `json:"dedupe_key"`
	CreatedAt            time.Time   `json:"created_at"`
}

type canonicalSUT struct {
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
			Waived:           row.Waived,
			ACHash:           row.ACHash,
		})
	}
	sort.SliceStable(canonical, func(i, j int) bool { return canonical[i].ID < canonical[j].ID })
	return canonical
}

func canonicalWaves(rows []WaveSnapshot) []canonicalWave {
	canonical := make([]canonicalWave, 0, len(rows))
	for _, row := range rows {
		sutCounts := make([]canonicalSUTCounts, 0, len(row.BySUT))
		for _, counts := range row.BySUT {
			sutCounts = append(sutCounts, canonicalSUTCounts{
				SUTID:        counts.SUTID,
				Total:        counts.Total,
				Passed:       counts.Passed,
				Failed:       counts.Failed,
				Blocked:      counts.Blocked,
				Inconclusive: counts.Inconclusive,
			})
		}
		sort.SliceStable(sutCounts, func(i, j int) bool { return sutCounts[i].SUTID < sutCounts[j].SUTID })
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
			BySUT:      sutCounts,
			ReportID:   row.ReportID,
			StartedAt:  canonicalTimePtr(row.StartedAt),
			FinishedAt: canonicalTimePtr(row.FinishedAt),
		})
	}
	sort.SliceStable(canonical, func(i, j int) bool { return canonical[i].ID < canonical[j].ID })
	return canonical
}

func canonicalCaseRuns(rows []CaseRunSnapshot) ([]canonicalCaseRun, error) {
	canonical := make([]canonicalCaseRun, 0, len(rows))
	for _, row := range rows {
		digest, err := evidenceDigest(row.evidenceFingerprintSHAs(), row.EvidenceDroppedCount)
		if err != nil {
			return nil, err
		}
		canonical = append(canonical, canonicalCaseRun{
			ID:                     row.ID,
			VersionID:              row.VersionID,
			WaveID:                 row.WaveID,
			CaseVersionID:          row.CaseVersionID,
			CaseTitle:              row.CaseTitle,
			SUTID:                  row.SUTID,
			SUTIDs:                 sortedInt64s(row.SUTIDs),
			Status:                 row.Status,
			FailureClass:           row.FailureClass,
			TerminalCause:          row.TerminalCause,
			TriageClassification:   row.TriageClassification,
			Summary:                row.Summary,
			StepCount:              row.StepCount,
			StepFailedCount:        row.StepFailedCount,
			AssertionCount:         row.AssertionCount,
			AssertionFailedCount:   row.AssertionFailedCount,
			FailedStepSeq:          row.FailedStepSeq,
			FailedAssertionSeq:     row.FailedAssertionSeq,
			Category:               row.Category,
			Severity:               row.Severity,
			EnvironmentFingerprint: row.EnvironmentFingerprint,
			CodeBaseline:           row.CodeBaseline,
			EvidenceDigest:         digest,
			StartedAt:              canonicalTimePtr(row.StartedAt),
			FinishedAt:             canonicalTimePtr(row.FinishedAt),
			CreatedAt:              canonicalTime(row.CreatedAt),
		})
	}
	sort.SliceStable(canonical, func(i, j int) bool { return canonical[i].ID < canonical[j].ID })
	return canonical, nil
}

// evidenceDigest computes the per-run evidence fingerprint member (R-G2):
// sha256 of the canonical JSON array of the sorted, deduplicated sha256 set,
// plus the row count (multiplicity preserved) and the persisted dropped count.
// The input is the run's FULL evidence sha carrier (CaseRunSnapshot.
// evidenceFingerprintSHAs), never the bounded display list — the digest covers
// every evidence row of the run, not a capped sample (art.4.6 causal
// completeness; v0.2.4-G T8 separates the display cap from the digest). The
// empty set hashes the empty JSON array, so a run without evidence has a
// stable, distinct-from-any-content digest.
func evidenceDigest(shas []string, droppedCount int) (canonicalEvidenceDigest, error) {
	sorted := append([]string(nil), shas...)
	sort.Strings(sorted)
	set := make([]string, 0, len(sorted))
	for _, sha := range sorted {
		if len(set) == 0 || set[len(set)-1] != sha {
			set = append(set, sha)
		}
	}
	encoded, err := json.Marshal(set)
	if err != nil {
		return canonicalEvidenceDigest{}, err
	}
	sum := sha256.Sum256(encoded)
	return canonicalEvidenceDigest{
		SHA256SetDigest: hex.EncodeToString(sum[:]),
		Count:           len(shas),
		DroppedCount:    droppedCount,
	}, nil
}

func canonicalIssues(rows []IssueSnapshot) []canonicalIssue {
	canonical := make([]canonicalIssue, 0, len(rows))
	for _, row := range rows {
		canonical = append(canonical, canonicalIssue{
			ID:                   row.ID,
			VersionID:            row.VersionID,
			Title:                row.Title,
			Status:               row.Status,
			Category:             row.Category,
			Severity:             row.Severity,
			Source:               row.Source,
			TriageStatus:         row.TriageStatus,
			TriageClassification: row.TriageClassification,
			MergedIntoID:         row.MergedIntoID,
			SourceIssueIDs:       sortedInt64s(row.SourceIssueIDs),
			CaseRunIDs:           sortedInt64s(row.CaseRunIDs),
			DedupeKey:            row.DedupeKey,
			CreatedAt:            canonicalTime(row.CreatedAt),
		})
	}
	sort.SliceStable(canonical, func(i, j int) bool { return canonical[i].ID < canonical[j].ID })
	return canonical
}

func canonicalSUTs(rows []SUTSnapshot) []canonicalSUT {
	canonical := make([]canonicalSUT, 0, len(rows))
	for _, row := range rows {
		canonical = append(canonical, canonicalSUT{
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
