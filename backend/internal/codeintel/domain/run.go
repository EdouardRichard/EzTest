// Package codeintel is the code-intelligence domain: T0 index runs, T1 facts,
// change analysis, impact candidates, anchors and fact reviews (spec §3/§5).
package codeintel

import (
	"encoding/json"
	"time"
)

// IndexStatus is the code_index_run.status enum (spec §3.2).
type IndexStatus string

const (
	IndexQueued  IndexStatus = "queued"
	IndexRunning IndexStatus = "running"
	IndexDone    IndexStatus = "done"
	IndexFailed  IndexStatus = "failed"
)

// Valid reports whether status is a persisted index-run status.
func (s IndexStatus) Valid() bool {
	switch s {
	case IndexQueued, IndexRunning, IndexDone, IndexFailed:
		return true
	default:
		return false
	}
}

// IndexRun is one T0 index run. VersionID is the triggering version (0 when the
// run is not version-scoped).
type IndexRun struct {
	ID           int64
	VersionID    int64
	RepoID       int64
	CommitSHA    string
	Branch       string
	Status       string
	FilesTotal   int
	FilesIndexed int
	Truncated    bool
	Stats        json.RawMessage
	Errors       json.RawMessage
	StartedAt    *time.Time
	FinishedAt   *time.Time
	CreatedBy    *int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
