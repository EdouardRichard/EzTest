package domain

import "time"

// Edge relations from design spec §5.1.
const (
	RelContains   = "contains"
	RelImports    = "imports"
	RelDefines    = "defines"
	RelCalls      = "calls"
	RelRenders    = "renders"
	RelRouteToAPI = "route_to_api"
)

// CodeEdge is one materialised relation between two code entities at a commit.
type CodeEdge struct {
	ID          int64
	RepoID      int64
	Commit      string
	SrcEntityID int64
	DstEntityID int64
	Rel         string
	Source      string
	Confidence  string
	Evidence    []Evidence
	Meta        []byte // JSONB
	FactKey     *string
	FactVersion *int
	AnalysisID  *int64
	CreatedAt   time.Time
}
