package codeintel

import "time"

// AnchorSubject is the code_anchor.subject_kind enum (spec §3.4).
type AnchorSubject string

const (
	AnchorSubjectVersion            AnchorSubject = "version"
	AnchorSubjectRequirement        AnchorSubject = "requirement"
	AnchorSubjectRequirementVersion AnchorSubject = "requirement_version"
	AnchorSubjectCaseVersion        AnchorSubject = "case_version"
	AnchorSubjectIssue              AnchorSubject = "issue"
)

// Valid reports whether subject is a persisted anchor subject kind.
func (s AnchorSubject) Valid() bool {
	switch s {
	case AnchorSubjectVersion, AnchorSubjectRequirement, AnchorSubjectRequirementVersion,
		AnchorSubjectCaseVersion, AnchorSubjectIssue:
		return true
	default:
		return false
	}
}

// AnchorStatus is the code_anchor.status enum (spec §3.4).
type AnchorStatus string

const (
	AnchorValid  AnchorStatus = "valid"
	AnchorStale  AnchorStatus = "stale"
	AnchorBroken AnchorStatus = "broken"
)

// Valid reports whether status is a persisted anchor status.
func (s AnchorStatus) Valid() bool {
	switch s {
	case AnchorValid, AnchorStale, AnchorBroken:
		return true
	default:
		return false
	}
}

// Anchor is one unified code anchor (spec §3.4/§6.1). Status, Confidence and
// CreatedByKind keep their persisted string form for repository scanning.
type Anchor struct {
	ID              int64
	VersionID       int64
	RepoID          int64
	SubjectKind     AnchorSubject
	SubjectID       int64
	FilePath        string
	StartLine       *int
	EndLine         *int
	EntityID        *int64
	CommitSHA       string
	ExcerptSHA      string
	Status          string
	Confidence      string
	CreatedByKind   string
	CreatedBy       *int64
	LastValidatedAt *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}
