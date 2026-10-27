package domain

import "time"

// Anchor subject kinds. P4 accepts version, requirement and app_model; case and
// issue are reserved for P5/P6 and rejected (design spec §6.7, INV-15).
const (
	SubjectVersion     = "version"
	SubjectRequirement = "requirement"
	SubjectAppModel    = "app_model"
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

// ValidateSubjectKind accepts the three P4 anchor subject kinds and rejects
// every other kind with reason unsupported_subject_kind (design spec §6.7).
func ValidateSubjectKind(kind string) error {
	switch kind {
	case SubjectVersion, SubjectRequirement, SubjectAppModel:
		return nil
	default:
		return NewInvalid("subjectKind", "unsupported_subject_kind",
			"codeintel: subjectKind must be one of version, requirement or app_model")
	}
}
