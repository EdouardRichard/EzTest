package domain

import "time"

// Review statuses, mirrored from the case_review.status CHECK constraint.
const (
	ReviewDraft    = "draft"
	ReviewApproved = "approved"
	ReviewRejected = "rejected"
)

// Finding categories, mirrored from the case_review_finding.category CHECK
// constraint.
const (
	FindingCoverage      = "coverage"
	FindingAssertion     = "assertion"
	FindingExecutability = "executability"
	FindingDuplicate     = "duplicate"
	FindingCodeUncovered = "code_uncovered"
	FindingOther         = "other"
)

// Finding severities, mirrored from the case_review_finding.severity CHECK
// constraint.
const (
	SeverityBlocker = "blocker"
	SeverityMajor   = "major"
	SeverityMinor   = "minor"
	SeverityInfo    = "info"
)

// Finding statuses, mirrored from the case_review_finding.status CHECK
// constraint.
const (
	FindingOpen      = "open"
	FindingFixed     = "fixed"
	FindingDismissed = "dismissed"
)

// Finding sources, mirrored from the case_review_finding.source CHECK
// constraint.
const (
	FindingSourceAI    = "ai"
	FindingSourceHuman = "human"
)

// CaseReview is one review round of a version's case mindmap. ReviewerID is the
// actor that requested the review; DecidedBy/DecidedAt are set on decision.
// IDs are application-generated Snowflakes.
type CaseReview struct {
	ID         int64
	VersionID  int64
	RunID      *int64
	Status     string
	Comment    string
	ReviewerID int64
	DecidedBy  *int64
	DecidedAt  *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// CaseReviewFinding is one review finding. NodeID is optional (version-level
// findings have none). IDs are application-generated Snowflakes.
type CaseReviewFinding struct {
	ID        int64
	ReviewID  int64
	NodeID    *int64
	Category  string
	Severity  string
	Detail    string
	Status    string
	Source    string
	CreatedBy int64
	CreatedAt time.Time
	UpdatedAt time.Time
}
