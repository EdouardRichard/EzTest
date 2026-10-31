package delivery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// Issue candidate bounds (spec §5.1, §3.2).
const (
	// IssueTitleMax is the delivery_issue.title character bound.
	IssueTitleMax = 500
	// IssueSummaryMax is the delivery_issue.summary character bound.
	IssueSummaryMax = 4000
	// IssueRefsMax is the write-side bound of one issue's logical refs.
	IssueRefsMax = 20
	// IssueEvidenceRefsMax is the number of evidence ids carried as refs.
	IssueEvidenceRefsMax = 5
	// IssueAssertionRefsMax is the number of failed assertion results carried
	// as refs.
	IssueAssertionRefsMax = 3
	// IssueCandidateSource is the only persisted issue source in P4.
	IssueCandidateSource = "failure_candidate"
)

// Issue ref kinds persisted by delivery_issue_ref (spec §3.2).
const (
	IssueRefKindCaseRun         = "case_run"
	IssueRefKindCaseVersion     = "case_version"
	IssueRefKindWave            = "wave"
	IssueRefKindStepResult      = "step_result"
	IssueRefKindAssertionResult = "assertion_result"
	IssueRefKindEvidence        = "evidence"
	IssueRefKindReport          = "report"
	IssueRefKindRequirement     = "requirement"
)

// IssueRefRoleSource is the default role of a candidate-derived issue ref.
const IssueRefRoleSource = "source"

// CandidateInput is the deterministic truth of one failed(application) case_run
// projection (spec §5.1). WaveID, VersionSUTID, RequirementID and the failed
// result ids are optional; a valid DedupeHint wins over the recomputed P3 key.
// FailedExpected/FailedActual/FailedReason feed the issue summary only when
// CaseRunSummary is empty.
type CandidateInput struct {
	VersionID                int64
	CaseRunID                int64
	CaseVersionID            int64
	RequirementID            int64
	CaseTitle                string
	CaseRunSummary           string
	StepSeq                  int
	AssertionSeq             int
	FailedStepResultID       int64
	FailedAssertionResultIDs []int64
	FailureClass             string
	Category                 string
	Severity                 string
	FailedExpected           string
	FailedActual             string
	FailedReason             string
	DedupeHint               string
	VersionSUTID             *int64
	WaveID                   *int64
	EvidenceIDs              []int64
}

// NewIssueCandidate builds the deterministic open issue and its logical refs
// for one failed(application) case_run (spec §5.1). The repository assigns the
// issue/ref IDs and timestamps, so those fields stay unset here. Invalid ids,
// categories or severities return ErrInvalidArgument.
func NewIssueCandidate(in CandidateInput) (Issue, []IssueRef, error) {
	if err := validateCandidateInput(in); err != nil {
		return Issue{}, nil, err
	}
	title := candidateTitle(in)
	summary := candidateSummary(in)
	dedupeKey := DeriveDedupeKey(in.VersionID, in.CaseVersionID, in.StepSeq, in.AssertionSeq, in.FailureClass, in.DedupeHint)
	fingerprint, err := candidateFingerprint(in, dedupeKey, title)
	if err != nil {
		return Issue{}, nil, ErrInvalidArgument
	}
	issue := Issue{
		VersionID:        in.VersionID,
		Title:            title,
		Summary:          summary,
		Status:           IssueStatusOpen,
		Category:         in.Category,
		Severity:         in.Severity,
		DedupeKey:        dedupeKey,
		Fingerprint:      fingerprint,
		Source:           IssueCandidateSource,
		ExpectedRevision: 1,
		ReopenCount:      0,
		CreatedByRunID:   nil,
	}
	return issue, candidateRefs(in), nil
}

// DeriveDedupeKey returns the P3-compatible dedupe key (spec §5.1): a valid
// 32-character lowercase-hex hint is authoritative; otherwise the key is
// sha256("{versionID}|{caseVersionID}|{stepSeq}|{assertionSeq}|{failureClass}")[:32].
// Wave and case_run ids deliberately do not participate, so the key is stable
// across waves of the same case version.
func DeriveDedupeKey(versionID, caseVersionID int64, stepSeq, assertionSeq int, failureClass, hint string) string {
	if isDedupeHint(hint) {
		return hint
	}
	payload := fmt.Sprintf("%d|%d|%d|%d|%s", versionID, caseVersionID, stepSeq, assertionSeq, failureClass)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])[:32]
}

func validateCandidateInput(in CandidateInput) error {
	if in.VersionID <= 0 || in.CaseRunID <= 0 || in.CaseVersionID <= 0 {
		return ErrInvalidArgument
	}
	if in.StepSeq < 0 || in.AssertionSeq < 0 {
		return ErrInvalidArgument
	}
	if in.WaveID != nil && *in.WaveID <= 0 {
		return ErrInvalidArgument
	}
	if in.VersionSUTID != nil && *in.VersionSUTID <= 0 {
		return ErrInvalidArgument
	}
	if in.RequirementID < 0 || in.FailedStepResultID < 0 {
		return ErrInvalidArgument
	}
	for _, assertionResultID := range in.FailedAssertionResultIDs {
		if assertionResultID <= 0 {
			return ErrInvalidArgument
		}
	}
	for _, evidenceID := range in.EvidenceIDs {
		if evidenceID <= 0 {
			return ErrInvalidArgument
		}
	}
	if !validIssueCategory(in.Category) || !validIssueSeverity(in.Severity) {
		return ErrInvalidArgument
	}
	return nil
}

func validIssueCategory(category string) bool {
	switch category {
	case "ui", "api", "db":
		return true
	default:
		return false
	}
}

func validIssueSeverity(severity string) bool {
	switch severity {
	case "major", "minor", "trivial":
		return true
	default:
		return false
	}
}

func candidateTitle(in CandidateInput) string {
	if in.StepSeq > 0 {
		return truncateRunes(
			fmt.Sprintf("%s · step %d: %s failure", in.CaseTitle, in.StepSeq, in.Category),
			IssueTitleMax,
		)
	}
	return truncateRunes(fmt.Sprintf("%s · %s failure", in.CaseTitle, in.Category), IssueTitleMax)
}

// candidateSummary prefers case_run.summary and otherwise composes the failed
// assertion detail (spec §5.1), keeping only non-empty parts in the fixed
// expected/actual/reason order.
func candidateSummary(in CandidateInput) string {
	if in.CaseRunSummary != "" {
		return truncateRunes(in.CaseRunSummary, IssueSummaryMax)
	}
	parts := make([]string, 0, 3)
	for _, part := range []struct {
		label string
		value string
	}{
		{"expected", in.FailedExpected},
		{"actual", in.FailedActual},
		{"reason", in.FailedReason},
	} {
		value := strings.TrimSpace(part.value)
		if value == "" {
			continue
		}
		parts = append(parts, part.label+": "+value)
	}
	return truncateRunes(strings.Join(parts, "\n"), IssueSummaryMax)
}

// issueFingerprintPayload is the canonical fingerprint input of spec §5.1.
// Field order is fixed by the struct; there are no maps or timestamps.
type issueFingerprintPayload struct {
	VersionID     int64  `json:"version_id"`
	CaseRunID     int64  `json:"case_run_id"`
	CaseVersionID int64  `json:"case_version_id"`
	StepSeq       int    `json:"step_seq"`
	AssertionSeq  int    `json:"assertion_seq"`
	DedupeKey     string `json:"dedupe_key"`
	Title         string `json:"title"`
	Category      string `json:"category"`
	Severity      string `json:"severity"`
}

func candidateFingerprint(in CandidateInput, dedupeKey, title string) (string, error) {
	encoded, err := json.Marshal(issueFingerprintPayload{
		VersionID:     in.VersionID,
		CaseRunID:     in.CaseRunID,
		CaseVersionID: in.CaseVersionID,
		StepSeq:       in.StepSeq,
		AssertionSeq:  in.AssertionSeq,
		DedupeKey:     dedupeKey,
		Title:         title,
		Category:      in.Category,
		Severity:      in.Severity,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// candidateRefs builds the deterministic logical refs of one candidate: the
// mandatory case_run, case_version, the optional wave, failed step_result,
// failed assertion results (at most three), evidence ids (at most five) and the
// resolvable requirement (spec §5.1). Every ref carries the failing version SUT
// when known.
func candidateRefs(in CandidateInput) []IssueRef {
	refs := make([]IssueRef, 0, 3+IssueAssertionRefsMax+IssueEvidenceRefsMax+2)
	appendRef := func(kind string, refID int64) {
		refs = append(refs, IssueRef{
			Kind:         kind,
			RefID:        refID,
			VersionSUTID: in.VersionSUTID,
			Role:         IssueRefRoleSource,
		})
	}
	appendRef(IssueRefKindCaseRun, in.CaseRunID)
	appendRef(IssueRefKindCaseVersion, in.CaseVersionID)
	if in.WaveID != nil {
		appendRef(IssueRefKindWave, *in.WaveID)
	}
	if in.FailedStepResultID > 0 {
		appendRef(IssueRefKindStepResult, in.FailedStepResultID)
	}
	seenAssertions := make(map[int64]struct{}, len(in.FailedAssertionResultIDs))
	assertionRefs := 0
	for _, assertionResultID := range in.FailedAssertionResultIDs {
		if assertionRefs >= IssueAssertionRefsMax {
			break
		}
		if _, ok := seenAssertions[assertionResultID]; ok {
			continue
		}
		seenAssertions[assertionResultID] = struct{}{}
		appendRef(IssueRefKindAssertionResult, assertionResultID)
		assertionRefs++
	}
	seenEvidence := make(map[int64]struct{}, len(in.EvidenceIDs))
	evidenceRefs := 0
	for _, evidenceID := range in.EvidenceIDs {
		if evidenceRefs >= IssueEvidenceRefsMax {
			break
		}
		if _, ok := seenEvidence[evidenceID]; ok {
			continue
		}
		seenEvidence[evidenceID] = struct{}{}
		appendRef(IssueRefKindEvidence, evidenceID)
		evidenceRefs++
	}
	if in.RequirementID > 0 {
		appendRef(IssueRefKindRequirement, in.RequirementID)
	}
	return capIssueRefs(refs)
}

// capIssueRefs enforces the write-side refs bound of one issue.
func capIssueRefs(refs []IssueRef) []IssueRef {
	if len(refs) > IssueRefsMax {
		return refs[:IssueRefsMax]
	}
	return refs
}

// isDedupeHint reports whether hint matches the frozen P3 payload shape
// ^[0-9a-f]{32}$ (spec §5.1).
func isDedupeHint(hint string) bool {
	if len(hint) != 32 {
		return false
	}
	for i := 0; i < len(hint); i++ {
		character := hint[i]
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
