package delivery

// v0.2.4-G T11 (SC-G8, R-G10a): the DISPLAY projection fingerprint. It hashes the
// display-only state a published report shows — the design judge projection
// (review_state/verdict/findings/candidate AC matrix) — INDEPENDENTLY of the
// canonical input fingerprint (Fingerprint). review_state/verdict are instant
// display projections (art.V.11), deliberately NOT canonical members (T9), so a
// judge-only change never changes Fingerprint and never marks the report stale.
// The complete stale sweep compares DisplayFingerprint against the current source
// display state and refreshes the report on divergence, healing lost judge events
// on static versions (art.7.6 — the display must not silently stay outdated).
//
// Only genuinely display-only, independently-changing projections belong here.
// Derived display metadata whose inputs are already canonical (failure
// explanations, evidence totals, truncation notes, NL case context bound to a
// frozen CaseVersionID) is excluded: it can only change when the canonical
// fingerprint changes, so the stale path already heals it.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// DisplayFingerprint returns the lowercase hex sha256 of the canonical display
// payload, or "" when it cannot be encoded (fixed shapes, cannot happen).
func DisplayFingerprint(snapshot ReportSnapshot) string {
	payload := displayReport{
		Kind:      snapshot.Kind,
		VersionID: snapshot.VersionID,
		WaveID:    snapshot.WaveID,
		Judgments: canonicalJudgments(snapshot.Judgments),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// displayReport is the fixed field order of the display payload. Do not reorder.
type displayReport struct {
	Kind      ReportKind        `json:"kind"`
	VersionID int64             `json:"version_id"`
	WaveID    *int64            `json:"wave_id"`
	Judgments []displayJudgment `json:"judgments"`
}

type displayJudgment struct {
	CaseID                  int64             `json:"case_id"`
	CaseVersionID           int64             `json:"case_version_id"`
	RequirementID           int64             `json:"requirement_id"`
	ReviewState             string            `json:"review_state"`
	Verdict                 string            `json:"verdict"`
	Findings                []displayFinding  `json:"findings"`
	ACCoverageMatrix        []displayMatrixAC `json:"ac_coverage_matrix"`
	ACCoverageMatrixDropped int               `json:"ac_coverage_matrix_dropped"`
}

type displayFinding struct {
	Dimension string `json:"dimension"`
	Severity  string `json:"severity"`
	Message   string `json:"message"`
	CaseRef   string `json:"case_ref"`
}

type displayMatrixAC struct {
	ACID    string   `json:"ac_id"`
	CaseIDs []string `json:"case_ids"`
}

func canonicalJudgments(rows []JudgmentSnapshot) []displayJudgment {
	out := make([]displayJudgment, 0, len(rows))
	for _, row := range rows {
		findings := make([]displayFinding, 0, len(row.Findings))
		for _, f := range row.Findings {
			findings = append(findings, displayFinding{Dimension: f.Dimension, Severity: f.Severity, Message: f.Message, CaseRef: f.CaseRef})
		}
		sort.SliceStable(findings, func(i, j int) bool {
			if findings[i].Dimension != findings[j].Dimension {
				return findings[i].Dimension < findings[j].Dimension
			}
			if findings[i].Severity != findings[j].Severity {
				return findings[i].Severity < findings[j].Severity
			}
			if findings[i].Message != findings[j].Message {
				return findings[i].Message < findings[j].Message
			}
			return findings[i].CaseRef < findings[j].CaseRef
		})
		matrix := make([]displayMatrixAC, 0, len(row.ACCoverageMatrix))
		for acID, caseIDs := range row.ACCoverageMatrix {
			sorted := append([]string(nil), caseIDs...)
			sort.Strings(sorted)
			matrix = append(matrix, displayMatrixAC{ACID: acID, CaseIDs: sorted})
		}
		sort.SliceStable(matrix, func(i, j int) bool { return matrix[i].ACID < matrix[j].ACID })
		out = append(out, displayJudgment{
			CaseID:                  row.CaseID,
			CaseVersionID:           row.CaseVersionID,
			RequirementID:           row.RequirementID,
			ReviewState:             row.ReviewState,
			Verdict:                 row.Verdict,
			Findings:                findings,
			ACCoverageMatrix:        matrix,
			ACCoverageMatrixDropped: row.ACCoverageMatrixDroppedCount,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CaseID != out[j].CaseID {
			return out[i].CaseID < out[j].CaseID
		}
		return out[i].CaseVersionID < out[j].CaseVersionID
	})
	return out
}
