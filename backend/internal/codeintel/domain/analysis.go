package domain

import (
	"sort"
	"time"
)

// Async run statuses from design spec §6.8.
const (
	RunQueued  = "queued"
	RunRunning = "running"
	RunDone    = "done"
	RunFailed  = "failed"
)

// Impact candidate subject kinds from design spec §5.2.
const (
	CandidateRequirement = "requirement"
	CandidateAppModel    = "app_model"
)

// Impact candidate sources, ordered by priority in design spec §6.6 step 4.
const (
	CandidateDirect   = "direct"
	CandidateContract = "contract"
	CandidateRelation = "relation"
	CandidateAI       = "ai"
)

// Unmapped change reasons from design spec §6.6 step 7.
const (
	UnmappedDeleted       = "deleted_file"
	UnmappedUnsupported   = "unsupported_content"
	UnmappedNoFact        = "no_fact_or_anchor"
	UnmappedAIUnavailable = "ai_unavailable"
	UnmappedContract      = "unmatched_contract"
)

// FactVersionRef is one entry of an analysis' fact-version snapshot.
type FactVersionRef struct {
	FactKey string
	Version int
}

// AnalysisError is one file-scoped analysis failure. It never carries code
// content.
type AnalysisError struct {
	Code    string
	Path    string
	Message string
}

// IndexRun is one T0 repository index run. It shares the async state machine
// and status values of CodeAnalysis (design spec §6.8).
type IndexRun struct {
	ID         int64
	RepoID     int64
	CommitSHA  string
	Source     string
	Status     string
	Stats      []byte // JSONB
	Errors     []AnalysisError
	StartedAt  *time.Time
	FinishedAt *time.Time
	CreatedBy  int64
	CreatedAt  time.Time
}

// UnmappedChange is one changed file that no fact or anchor covers.
type UnmappedChange struct {
	Path       string
	Status     string
	Reason     string
	Suggestion string
}

// CodeAnalysis is one version-scoped impact analysis run.
type CodeAnalysis struct {
	ID              int64
	VersionID       int64
	RepoID          int64
	BaseCommit      string
	HeadCommit      string
	ChangeSetID     *int64
	Status          string
	Degraded        bool
	Model           string
	PromptVersion   string
	AnalysisVersion int
	FactVersions    []FactVersionRef
	Stats           []byte // JSONB
	Errors          []AnalysisError
	Unmapped        []UnmappedChange
	CreatedBy       int64
	CreatedAt       time.Time
	StartedAt       *time.Time
	FinishedAt      *time.Time
}

// ImpactCandidate is one affected subject produced by an analysis.
type ImpactCandidate struct {
	ID                 int64
	AnalysisID         int64
	SubjectKind        string
	SubjectID          int64
	Reason             string
	Confidence         string
	Source             string
	Evidence           []Evidence
	Selected           bool
	SelectionUpdatedBy *int64
	SelectionUpdatedAt *time.Time
}

// SortCandidates orders candidates deterministically (design spec §6.6 step 5):
// source priority asc, confidence asc, subject kind asc, subject ID asc. The ID
// is a final tie-break so the order is total and reproducible (INV-4).
func SortCandidates(candidates []ImpactCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if leftRank, rightRank := candidateSourceRank(left.Source), candidateSourceRank(right.Source); leftRank != rightRank {
			return leftRank < rightRank
		}
		if leftRank, rightRank := confidenceRank(left.Confidence), confidenceRank(right.Confidence); leftRank != rightRank {
			return leftRank < rightRank
		}
		if leftRank, rightRank := subjectKindRank(left.SubjectKind), subjectKindRank(right.SubjectKind); leftRank != rightRank {
			return leftRank < rightRank
		}
		if left.SubjectID != right.SubjectID {
			return left.SubjectID < right.SubjectID
		}
		return left.ID < right.ID
	})
}

// DefaultSelected reports whether a new candidate of confidence is selected by
// default: everything except low (design spec §6.6 step 6, INV-7).
func DefaultSelected(confidence string) bool {
	return confidence != ConfidenceLow
}

// candidateSourceRank is the merge/order priority of a candidate source.
func candidateSourceRank(source string) int {
	switch source {
	case CandidateDirect:
		return 0
	case CandidateContract:
		return 1
	case CandidateRelation:
		return 2
	case CandidateAI:
		return 3
	default:
		return 4
	}
}

// confidenceRank orders confidence levels high < medium < low.
func confidenceRank(confidence string) int {
	switch confidence {
	case ConfidenceHigh:
		return 0
	case ConfidenceMedium:
		return 1
	case ConfidenceLow:
		return 2
	default:
		return 3
	}
}

// subjectKindRank orders requirement before app_model.
func subjectKindRank(kind string) int {
	switch kind {
	case CandidateRequirement:
		return 0
	case CandidateAppModel:
		return 1
	default:
		return 2
	}
}
