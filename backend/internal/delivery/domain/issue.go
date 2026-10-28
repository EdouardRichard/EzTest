package domain

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Issue field vocabularies (spec §5.4). They mirror the issue DDL CHECK
// constraints so a rejected value is a domain error, never a raw adapter error.
const (
	SourceAI    = "ai"
	SourceHuman = "human"

	CategoryDefect         = "defect"
	CategoryRequirementGap = "requirement_gap"
	CategoryOther          = "other"

	SeverityBlocker = "blocker"
	SeverityMajor   = "major"
	SeverityMinor   = "minor"
	SeverityInfo    = "info"

	StatusOpen      = "open"
	StatusConfirmed = "confirmed"
	StatusDismissed = "dismissed"

	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"
	ConfidenceLow    = "low"

	FailureClassApplication = "application"
	FailureClassEnvironment = "environment"
	FailureClassSession     = "session"
	FailureClassModel       = "model"
	FailureClassCase        = "case"

	// MaxIssueTitleRunes is the issue.title CHECK bound (spec §4.2.1).
	MaxIssueTitleRunes = 200
	// MaxIssueQueryRunes bounds the issue list's fuzzy title filter (spec §7.3).
	MaxIssueQueryRunes = 100
)

// IssueRef kinds (spec §4.2.1). ref_id is text so a commit SHA and Snowflake
// ids share one column.
const (
	RefKindCaseRun     = "case_run"
	RefKindCaseNode    = "case_node"
	RefKindStepResult  = "step_result"
	RefKindEvidence    = "evidence"
	RefKindRequirement = "requirement"
	RefKindBatch       = "batch"
	RefKindCommit      = "commit"
	RefKindCodeAnchor  = "code_anchor"
)

// Issue is the durable, deduplicated defect or requirement gap (spec §4.2.1,
// plan Shared Interfaces). The issue table is the aggregate root; references
// live in IssueRef rows.
type Issue struct {
	ID              int64
	VersionID       int64
	BatchID         *int64
	CaseRunID       *int64
	CaseNodeID      *int64
	StepResultID    *int64
	Source          string
	Category        string
	Severity        string
	Status          string
	Confidence      string
	FailureClass    string
	DedupeKey       string
	Title           string
	Description     string
	ReproSteps      string
	Expected        string
	Actual          string
	RerunSuggestion string
	ExternalRef     string
	RecurrenceCount int
	Revision        int
	CreatedBy       int64
	UpdatedBy       int64
	ConfirmedBy     *int64
	DismissedBy     *int64
	ConfirmedAt     *time.Time
	DismissedAt     *time.Time
	DismissedReason string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// IssueRef is one durable reference of an issue (spec §4.2.1). RefID is the
// decimal string form of a Snowflake id, a 40-hex commit, or a code_anchor id.
type IssueRef struct {
	IssueID int64
	RefKind string
	RefID   string
}

// CodeRef is one code location reported with an AI issue candidate. The service
// validates it against the version's repository before it creates a code anchor
// (spec §5.4); a malformed entry is dropped best-effort and never blocks the
// issue from landing.
type CodeRef struct {
	Path   string
	Line   int
	Reason string
}

// IssueDraft is one AI issue candidate produced by failure analysis, issue
// triage or supplement escalation (spec §5.6, plan Shared Interfaces). The
// service hard-dedupes every draft by (version_id, dedupe_key): source is always
// ai for a draft, human issues are created through CreateHuman.
type IssueDraft struct {
	DedupeKey        string
	Title            string
	Description      string
	ReproSteps       string
	Expected         string
	Actual           string
	RerunSuggestion  string
	Severity         string
	Confidence       string
	Category         string
	FailureClass     string
	CaseRunID        *int64
	CaseNodeID       *int64
	EvidenceIDs      []int64
	CodeRefs         []CodeRef
	RequirementIDs   []int64
	MergeIntoIssueID *int64
}

// CanTransition reports whether the issue state machine allows from -> to
// (spec §5.4): open may become confirmed or dismissed; both are terminal and
// P7 never reopens them.
func CanTransition(from, to string) bool {
	return from == StatusOpen && (to == StatusConfirmed || to == StatusDismissed)
}

// IsTerminalStatus reports whether status is a terminal issue state.
func IsTerminalStatus(status string) bool {
	return status == StatusConfirmed || status == StatusDismissed
}

// ConfidenceRank orders confidence so a recurrence can raise it and never lower
// it (spec §5.4). Unknown values rank lowest.
func ConfidenceRank(confidence string) int {
	switch confidence {
	case ConfidenceHigh:
		return 3
	case ConfidenceMedium:
		return 2
	case ConfidenceLow:
		return 1
	default:
		return 0
	}
}

// SeverityRank orders severity for the issue list (spec §7.3): blocker first,
// then major, minor and info.
func SeverityRank(severity string) int {
	switch severity {
	case SeverityBlocker:
		return 0
	case SeverityMajor:
		return 1
	case SeverityMinor:
		return 2
	case SeverityInfo:
		return 3
	default:
		return 4
	}
}

// ValidateTitle trims and bounds an issue title (spec §4.2.1: non-empty, at
// most 200 characters) and returns the stored form.
func ValidateTitle(title string) (string, error) {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return "", NewInvalid("title", ReasonInvalidArgument, "title must not be empty")
	}
	if utf8.RuneCountInString(trimmed) > MaxIssueTitleRunes {
		return "", NewInvalid("title", ReasonInvalidArgument,
			fmt.Sprintf("title must be at most %d characters", MaxIssueTitleRunes))
	}
	return trimmed, nil
}

// ValidateSeverity enforces the severity enum.
func ValidateSeverity(severity string) error {
	switch severity {
	case SeverityBlocker, SeverityMajor, SeverityMinor, SeverityInfo:
		return nil
	default:
		return NewInvalid("severity", ReasonInvalidArgument,
			fmt.Sprintf("severity %q is not blocker, major, minor or info", severity))
	}
}

// ValidateCategory enforces the category enum.
func ValidateCategory(category string) error {
	switch category {
	case CategoryDefect, CategoryRequirementGap, CategoryOther:
		return nil
	default:
		return NewInvalid("category", ReasonInvalidArgument,
			fmt.Sprintf("category %q is not defect, requirement_gap or other", category))
	}
}

// ValidateConfidence enforces the confidence enum.
func ValidateConfidence(confidence string) error {
	switch confidence {
	case ConfidenceHigh, ConfidenceMedium, ConfidenceLow:
		return nil
	default:
		return NewInvalid("confidence", ReasonInvalidArgument,
			fmt.Sprintf("confidence %q is not high, medium or low", confidence))
	}
}

// ValidateFailureClass enforces the failure class enum.
func ValidateFailureClass(class string) error {
	switch class {
	case FailureClassApplication, FailureClassEnvironment, FailureClassSession,
		FailureClassModel, FailureClassCase:
		return nil
	default:
		return NewInvalid("failureClass", ReasonInvalidArgument,
			fmt.Sprintf("failure class %q is not application, environment, session, model or case", class))
	}
}

// ValidateDedupeKey enforces the lowercase sha256 shape of spec §4.2.1.
func ValidateDedupeKey(key string) error {
	if !isLowerHex(key, 64) {
		return NewInvalid("dedupeKey", ReasonInvalidArgument,
			"dedupeKey must be a lowercase 64-character sha256")
	}
	return nil
}

// ValidateIssueRef validates one ref against its kind (spec §5.4): id kinds are
// positive decimal ids, a commit is 40 lowercase hex, and the kind is one of the
// durable issue_ref kinds. Cross-context ownership is checked by the service's
// IssueRefValidator port, never by the domain.
func ValidateIssueRef(refKind, refID string) error {
	switch refKind {
	case RefKindCaseRun, RefKindCaseNode, RefKindStepResult, RefKindEvidence,
		RefKindRequirement, RefKindBatch, RefKindCodeAnchor:
		id, err := strconv.ParseInt(refID, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != refID {
			return NewInvalid("refs", ReasonIssueRefInvalid,
				fmt.Sprintf("%s issue ref %q is not a positive decimal id", refKind, refID))
		}
		return nil
	case RefKindCommit:
		if !isLowerHex(refID, 40) {
			return NewInvalid("refs", ReasonIssueRefInvalid,
				fmt.Sprintf("commit issue ref %q is not a 40-character lowercase sha", refID))
		}
		return nil
	default:
		return NewInvalid("refs", ReasonIssueRefInvalid,
			fmt.Sprintf("issue ref kind %q is not valid", refKind))
	}
}

// NormalizeIssueRefs returns the refs sorted by kind then id with duplicates
// removed. The stored order is therefore deterministic for every caller.
func NormalizeIssueRefs(refs []IssueRef) []IssueRef {
	if len(refs) == 0 {
		return nil
	}
	unique := make(map[string]IssueRef, len(refs))
	keys := make([]string, 0, len(refs))
	for _, ref := range refs {
		key := ref.RefKind + "\x00" + ref.RefID
		if _, exists := unique[key]; exists {
			continue
		}
		unique[key] = ref
		keys = append(keys, key)
	}
	normalized := make([]IssueRef, 0, len(keys))
	for _, key := range keys {
		normalized = append(normalized, unique[key])
	}
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].RefKind != normalized[j].RefKind {
			return normalized[i].RefKind < normalized[j].RefKind
		}
		return issueRefIDLess(normalized[i].RefID, normalized[j].RefID)
	})
	return normalized
}

// MergeOccurrence folds one repeated occurrence into an existing issue (spec
// §5.4): recurrence_count increments, the confidence is raised to the higher
// value, a non-empty actual replaces the stored one, the revision is bumped for
// the OCC contract, and the status is never changed (confirmed/dismissed issues
// receive occurrences without reopening).
func MergeOccurrence(issue Issue, confidence, actual string, actor int64, now time.Time) Issue {
	issue.RecurrenceCount++
	if ConfidenceRank(confidence) > ConfidenceRank(issue.Confidence) {
		issue.Confidence = confidence
	}
	if strings.TrimSpace(actual) != "" {
		issue.Actual = actual
	}
	issue.Revision++
	issue.UpdatedBy = actor
	issue.UpdatedAt = now
	return issue
}

// ValidateIssueDraft enforces the AI single-issue rules of spec §5.4: a dedupe
// key, a title, a severity, a confidence and a category are mandatory; a defect
// needs failure_class=application and a case node; a requirement gap needs at
// least one requirement and may leave the failure class empty. It never mutates
// the draft; the service normalizes defaults after validation.
func ValidateIssueDraft(draft IssueDraft) error {
	if err := ValidateDedupeKey(draft.DedupeKey); err != nil {
		return err
	}
	if _, err := ValidateTitle(draft.Title); err != nil {
		return err
	}
	if err := ValidateSeverity(draft.Severity); err != nil {
		return err
	}
	if err := ValidateConfidence(draft.Confidence); err != nil {
		return err
	}
	category := draft.Category
	if category == "" {
		category = CategoryDefect
	}
	if err := ValidateCategory(category); err != nil {
		return err
	}
	if draft.CaseRunID != nil && *draft.CaseRunID <= 0 {
		return NewInvalid("caseRunId", ReasonInvalidArgument, "caseRunId must be a positive id")
	}
	if draft.CaseNodeID != nil && *draft.CaseNodeID <= 0 {
		return NewInvalid("caseNodeId", ReasonInvalidArgument, "caseNodeId must be a positive id")
	}
	if draft.MergeIntoIssueID != nil && *draft.MergeIntoIssueID <= 0 {
		return NewInvalid("mergeIntoIssueId", ReasonInvalidArgument, "mergeIntoIssueId must be a positive id")
	}
	for _, evidenceID := range draft.EvidenceIDs {
		if evidenceID <= 0 {
			return NewInvalid("evidenceIds", ReasonInvalidArgument,
				fmt.Sprintf("evidence id %d must be positive", evidenceID))
		}
	}
	for _, requirementID := range draft.RequirementIDs {
		if requirementID <= 0 {
			return NewInvalid("requirementIds", ReasonInvalidArgument,
				fmt.Sprintf("requirement id %d must be positive", requirementID))
		}
	}

	if category == CategoryRequirementGap {
		if len(draft.RequirementIDs) == 0 {
			return NewInvalid("requirementIds", ReasonInvalidArgument,
				"a requirement_gap issue must reference at least one requirement")
		}
		// Spec §5.4 only requires failure_class to be valid (or empty) for a
		// requirement gap; the defect-only application rule must not leak here.
		if draft.FailureClass != "" {
			if err := ValidateFailureClass(draft.FailureClass); err != nil {
				return err
			}
		}
		return nil
	}
	if draft.FailureClass == "" {
		return NewInvalid("failureClass", ReasonInvalidArgument,
			"an AI defect issue requires failureClass=application")
	}
	if draft.FailureClass != FailureClassApplication {
		return NewInvalid("failureClass", ReasonInvalidArgument,
			"an AI defect issue failure class must be application")
	}
	if draft.CaseNodeID == nil {
		return NewInvalid("caseNodeId", ReasonInvalidArgument,
			"an AI defect issue requires a caseNodeId")
	}
	return nil
}

// isLowerHex reports whether value is exactly n lowercase hexadecimal chars.
func isLowerHex(value string, n int) bool {
	if len(value) != n {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// issueRefIDLess orders two ref ids deterministically: decimal ids compare
// numerically and anything else compares as text.
func issueRefIDLess(left, right string) bool {
	leftID, leftErr := strconv.ParseInt(left, 10, 64)
	rightID, rightErr := strconv.ParseInt(right, 10, 64)
	if leftErr == nil && rightErr == nil {
		return leftID < rightID
	}
	return left < right
}
