package domain

import "time"

// Anchor subject kinds. version, requirement and app_model are the P4 subjects;
// case is added by P5 (D37) and issue by P7 (design spec §4.2.8/§7.9).
const (
	SubjectVersion     = "version"
	SubjectRequirement = "requirement"
	SubjectAppModel    = "app_model"
	SubjectCase        = "case"
	SubjectIssue       = "issue"
)

// CodeAnchor binds a version-scoped subject to a file/location in a repository.
type CodeAnchor struct {
	ID            int64
	VersionID     int64
	RepoID        int64
	SubjectKind   string
	SubjectID     int64
	FilePath      string
	StartLine     *int
	EndLine       *int
	EntityID      *int64
	CommitSHA     *string
	Confidence    string
	CreatedByKind string
	CreatedBy     int64
	CreatedAt     time.Time
}

// CreateAnchorParams is the validated input of an anchor creation.
type CreateAnchorParams struct {
	SubjectKind string
	SubjectID   int64
	FilePath    string
	StartLine   *int
	EndLine     *int
}

// ValidateSubjectKind accepts the five anchor subject kinds of P7 (D37/§7.9) and
// rejects every other kind with reason unsupported_subject_kind (design spec
// §6.7/§7.9).
func ValidateSubjectKind(kind string) error {
	switch kind {
	case SubjectVersion, SubjectRequirement, SubjectAppModel, SubjectCase, SubjectIssue:
		return nil
	default:
		return NewInvalid("subjectKind", "unsupported_subject_kind",
			"codeintel: subjectKind must be one of version, requirement, app_model, case or issue")
	}
}
