package design

import "time"

// Judgment verdicts. THE single authority for the judge verdict enum; the
// frontend and OpenAPI enums derive from these values, never the reverse.
const (
	VerdictPass = "pass"
	VerdictFail = "fail"
)

// Judgment finding severities (single authority, see VerdictPass).
const (
	SeverityBlocker   = "blocker"
	SeverityImportant = "important"
	SeverityMinor     = "minor"
)

// Judgment payload bounds. THE single authority for the size limits of a judge
// verdict candidate (H-F10b #27). They live with the Judgment value object so
// BOTH validators read one constant: the design application service (the
// settling authority) and the agent's design.judgment_submit tool (which rejects
// early so the model gets a precise field error). The application layer keeps its
// exported names as aliases; the tool aliases these directly.
//
// Why the domain and not the application package: the agent tools package cannot
// import design/application — version/application imports agent tools, and the
// design application's own internal tests import version/application, so that
// edge closes a test import cycle. The domain is the shared, dependency-free
// vocabulary layer both sides already import (the same placement the Override
// conditions use).
const (
	// MaxJudgmentFindings bounds one submitted judgment's finding list.
	MaxJudgmentFindings = 50
	// JudgmentDimensionMax bounds a finding dimension label.
	JudgmentDimensionMax = 64
	// JudgmentMessageMax bounds one finding message.
	JudgmentMessageMax = 2000
	// OverrideReasonMax bounds the human override reason.
	OverrideReasonMax = 2000
)

// ReviewState is the derived review state of a case version. It is a
// projection of judgment/rework facts, never a stored business status: the
// server derives it via DeriveReviewState and nothing else may write it.
type ReviewState string

// Review states (single authority, see VerdictPass).
const (
	ReviewStatePending          ReviewState = "pending"
	ReviewStatePass             ReviewState = "pass"
	ReviewStateReworking        ReviewState = "reworking"
	ReviewStateEscalatedFail    ReviewState = "escalated_fail"
	ReviewStateJudgeUnavailable ReviewState = "judge_unavailable"
)

// JudgmentFinding is one judge finding against a case version. CaseRef is
// empty for requirement-level findings.
type JudgmentFinding struct {
	Dimension string
	Severity  string
	Message   string
	CaseRef   string
}

// Judgment is one immutable judge verdict on one case version. Findings and
// ACCoverageMatrix are the judge's candidate content; the server binds the
// judgment to the case version and content hash at settlement time.
type Judgment struct {
	ID               int64
	VersionID        int64
	RequirementID    int64
	CaseID           int64
	CaseVersionID    int64
	ContentHash      string
	Round            int
	Verdict          string
	Findings         []JudgmentFinding
	ACCoverageMatrix map[string]any
	AgentRunID       int64
	CreatedAt        time.Time
}

// Override is one human override of a judge verdict, bound to the exact case
// version content it was made against. JudgmentID is optional: an override may
// stand alone when no judgment could be produced.
type Override struct {
	ID            int64
	CaseID        int64
	CaseVersionID int64
	ContentHash   string
	JudgmentID    *int64
	Condition     string
	ActorID       int64
	Reason        string
	CreatedAt     time.Time
}

// Override conditions. THE single authority for the override condition value
// set — it is a field of the Override value object above, so it lives with the
// verdict/severity/review-state enums rather than in the application layer
// (H-F10 T6). The design_judgment_override CHECK constraint is pinned against
// these values by the judgment schema integration test, never the reverse.
const (
	// OverrideConditionFailExhausted marks an override of a fail verdict whose
	// rework budget is exhausted (references the overridden judgment).
	OverrideConditionFailExhausted = "fail_exhausted"
	// OverrideConditionJudgeExhausted marks an override after the judge became
	// unavailable for the current case version (no judgment to reference).
	OverrideConditionJudgeExhausted = "judge_exhausted"
)

// ValidOverrideCondition reports whether value is an override condition.
func ValidOverrideCondition(value string) bool {
	switch value {
	case OverrideConditionFailExhausted, OverrideConditionJudgeExhausted:
		return true
	default:
		return false
	}
}

// OverrideConditions lists the override conditions (single source, see
// ReviewStates).
func OverrideConditions() []string {
	return []string{OverrideConditionFailExhausted, OverrideConditionJudgeExhausted}
}

// JudgmentBoundTo reports whether a judge fact is a fact ABOUT this exact case
// version. A judgment bound to a superseded revision says nothing about the
// current one, so every consumer (review-state derivation, override eligibility,
// the judgment report, the scheduler's pending/rework predicates) asks this one
// predicate instead of re-writing the nil-and-version comparison.
func JudgmentBoundTo(j *Judgment, caseVersionID int64) bool {
	return j != nil && j.CaseVersionID == caseVersionID
}

// DeriveReviewState projects review state from facts (single authority).
// Priority: judge_exhausted > escalated_fail > reworking > pass > pending.
// A judgment bound to an older case version is not a fact about the current
// version, so a version mismatch derives pending, not the stale verdict.
func DeriveReviewState(j *Judgment, exhausted bool, currentVersionID int64, reworkExhausted bool) ReviewState {
	if exhausted {
		return ReviewStateJudgeUnavailable
	}
	if !JudgmentBoundTo(j, currentVersionID) {
		return ReviewStatePending
	}
	if j.Verdict == VerdictPass {
		return ReviewStatePass
	}
	if reworkExhausted {
		return ReviewStateEscalatedFail
	}
	return ReviewStateReworking
}

// ReworkAllowed reports whether another rework round may start. reworkCount
// is derived from recorded rework facts (NOT round-1), so a missing or
// restarted round counter cannot grant extra rounds.
func ReworkAllowed(reworkCount, maxRounds int) bool {
	return reworkCount < maxRounds
}

// ValidVerdict reports whether value is a judge verdict.
func ValidVerdict(value string) bool {
	switch value {
	case VerdictPass, VerdictFail:
		return true
	default:
		return false
	}
}

// ValidSeverity reports whether value is a finding severity.
func ValidSeverity(value string) bool {
	switch value {
	case SeverityBlocker, SeverityImportant, SeverityMinor:
		return true
	default:
		return false
	}
}

// ValidReviewState reports whether value is a derived review state.
func ValidReviewState(value ReviewState) bool {
	switch value {
	case ReviewStatePending, ReviewStatePass, ReviewStateReworking,
		ReviewStateEscalatedFail, ReviewStateJudgeUnavailable:
		return true
	default:
		return false
	}
}

// ReviewStates lists the derived review states in authority order. It is the
// single source the frontend Go->TS enum gate derives REVIEW_STATES from
// (version/domain/enum_authority_test.go TestEnumAuthorityFrontendJudgment);
// the frontend constant is a checked projection, never a second truth.
func ReviewStates() []ReviewState {
	return []ReviewState{
		ReviewStatePending, ReviewStatePass, ReviewStateReworking,
		ReviewStateEscalatedFail, ReviewStateJudgeUnavailable,
	}
}

// Verdicts lists the judge verdicts (single source, see ReviewStates).
func Verdicts() []string {
	return []string{VerdictPass, VerdictFail}
}

// Severities lists the finding severities (single source, see ReviewStates).
func Severities() []string {
	return []string{SeverityBlocker, SeverityImportant, SeverityMinor}
}
