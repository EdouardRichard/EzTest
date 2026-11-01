package codeintel

import (
	"encoding/json"
	"time"
)

// Fact is one immutable fact version (spec §3.3). The current projection lives
// on Entity/FactKey; superseded rows are kept for reproducibility.
type Fact struct {
	ID            int64
	RepoID        int64
	CommitSHA     string
	FactKey       string
	Version       int
	Kind          EntityKind
	Path          string
	Name          string
	Signature     string
	StartLine     *int
	EndLine       *int
	BlobHash      string
	Body          json.RawMessage
	Evidence      json.RawMessage
	Source        string
	Confidence    Confidence
	Model         string
	PromptVersion string
	AnalysisID    *int64
	SupersededAt  *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}
