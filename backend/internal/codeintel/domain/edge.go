package codeintel

import (
	"encoding/json"
	"time"
)

// EdgeRel is the code_edge.rel enum (spec §3.2).
type EdgeRel string

const (
	EdgeContains   EdgeRel = "contains"
	EdgeImports    EdgeRel = "imports"
	EdgeDefines    EdgeRel = "defines"
	EdgeCalls      EdgeRel = "calls"
	EdgeRenders    EdgeRel = "renders"
	EdgeRouteToAPI EdgeRel = "route_to_api"
)

// Valid reports whether rel is a persisted edge relation.
func (r EdgeRel) Valid() bool {
	switch r {
	case EdgeContains, EdgeImports, EdgeDefines, EdgeCalls, EdgeRenders, EdgeRouteToAPI:
		return true
	default:
		return false
	}
}

// Edge is one materialized relation between two entities (spec §3.2).
type Edge struct {
	ID          int64
	RepoID      int64
	CommitSHA   string
	AnalysisID  *int64
	SrcEntityID int64
	DstEntityID int64
	Rel         EdgeRel
	Source      string
	Confidence  Confidence
	Evidence    json.RawMessage
	Meta        json.RawMessage
	FactKey     string
	FactVersion int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
