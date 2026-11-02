package codeintel

import (
	"encoding/json"
	"time"
)

// AnalysisStatus is the code_analysis.status enum (spec §3.3).
type AnalysisStatus string

const (
	AnalysisQueued  AnalysisStatus = "queued"
	AnalysisRunning AnalysisStatus = "running"
	AnalysisDone    AnalysisStatus = "done"
	AnalysisFailed  AnalysisStatus = "failed"
)

// Valid reports whether status is a persisted analysis status.
func (s AnalysisStatus) Valid() bool {
	switch s {
	case AnalysisQueued, AnalysisRunning, AnalysisDone, AnalysisFailed:
		return true
	default:
		return false
	}
}

// Analysis is one version×repo change analysis run (spec §3.3). ChangedFiles,
// Stats, Unmapped, Errors and FactVersions are persisted JSON documents.
type Analysis struct {
	ID              int64
	VersionID       int64
	RepoID          int64
	BaseCommitSHA   string
	HeadCommitSHA   string
	Status          AnalysisStatus
	Degraded        bool
	Model           string
	PromptVersion   string
	AnalysisVersion int
	ChangedFiles    json.RawMessage
	Stats           json.RawMessage
	Unmapped        json.RawMessage
	Errors          json.RawMessage
	FactVersions    json.RawMessage
	CreatedBy       *int64
	StartedAt       *time.Time
	FinishedAt      *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}
