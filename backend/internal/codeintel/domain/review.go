package codeintel

import "time"

// ReviewVerdict is the code_fact_review.verdict enum (spec §3.4).
type ReviewVerdict string

const (
	ReviewCorrect   ReviewVerdict = "correct"
	ReviewIncorrect ReviewVerdict = "incorrect"
)

// Valid reports whether verdict is a persisted review verdict.
func (v ReviewVerdict) Valid() bool {
	switch v {
	case ReviewCorrect, ReviewIncorrect:
		return true
	default:
		return false
	}
}

// FactReview is one append-only fact review (spec §3.4). Verdict keeps its
// persisted string form for repository scanning.
type FactReview struct {
	ID          int64
	FactID      int64
	FactVersion int
	RepoID      int64
	CommitSHA   string
	Verdict     string
	Reason      string
	ReviewerID  int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
