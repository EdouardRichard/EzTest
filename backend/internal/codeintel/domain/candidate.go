package codeintel

import (
	"encoding/json"
	"time"
)

// CandidateSource is the code_impact_candidate.source enum (spec §3.3). The
// declaration order is the deterministic merge priority direct → contract →
// relation → ai.
type CandidateSource string

const (
	CandidateDirect   CandidateSource = "direct"
	CandidateContract CandidateSource = "contract"
	CandidateRelation CandidateSource = "relation"
	CandidateAI       CandidateSource = "ai"
)

// Valid reports whether source is a persisted candidate source.
func (s CandidateSource) Valid() bool {
	switch s {
	case CandidateDirect, CandidateContract, CandidateRelation, CandidateAI:
		return true
	default:
		return false
	}
}

// SubjectKind is the code_impact_candidate.subject_kind enum (spec §3.3).
type SubjectKind string

const (
	SubjectRequirement        SubjectKind = "requirement"
	SubjectRequirementVersion SubjectKind = "requirement_version"
	SubjectCaseVersion        SubjectKind = "case_version"
	SubjectSUTPage            SubjectKind = "sut_page"
	SubjectSUTAPI             SubjectKind = "sut_api"
)

// Valid reports whether kind is a persisted impact subject kind.
func (k SubjectKind) Valid() bool {
	switch k {
	case SubjectRequirement, SubjectRequirementVersion, SubjectCaseVersion, SubjectSUTPage, SubjectSUTAPI:
		return true
	default:
		return false
	}
}

// ImpactCandidate is one affected subject of an analysis (spec §3.3).
type ImpactCandidate struct {
	ID                 int64
	AnalysisID         int64
	SubjectKind        SubjectKind
	SubjectID          int64
	Reason             string
	Confidence         Confidence
	Source             CandidateSource
	Evidence           json.RawMessage
	Selected           bool
	SelectionUpdatedBy *int64
	SelectionUpdatedAt *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}
