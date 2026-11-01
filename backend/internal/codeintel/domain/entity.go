package codeintel

import (
	"encoding/json"
	"time"
)

// EntityKind is the code_entity.kind / code_fact.kind enum (spec §3.2/§3.3).
type EntityKind string

const (
	EntityFile       EntityKind = "file"
	EntityModule     EntityKind = "module"
	EntityRoute      EntityKind = "route"
	EntityAPI        EntityKind = "api"
	EntityClientCall EntityKind = "client_call"
	EntityComponent  EntityKind = "component"
	EntityPage       EntityKind = "page"
	EntitySymbol     EntityKind = "symbol"
)

// Valid reports whether kind is a persisted entity/fact kind.
func (k EntityKind) Valid() bool {
	switch k {
	case EntityFile, EntityModule, EntityRoute, EntityAPI, EntityClientCall,
		EntityComponent, EntityPage, EntitySymbol:
		return true
	default:
		return false
	}
}

// Confidence is the source confidence enum shared by entities, edges, facts and
// impact candidates.
type Confidence string

const (
	ConfidenceLow    Confidence = "low"
	ConfidenceMedium Confidence = "medium"
	ConfidenceHigh   Confidence = "high"
)

// Valid reports whether confidence is a persisted confidence value.
func (c Confidence) Valid() bool {
	switch c {
	case ConfidenceLow, ConfidenceMedium, ConfidenceHigh:
		return true
	default:
		return false
	}
}

// Entity is the current queryable projection of one code entity (spec §3.2).
type Entity struct {
	ID          int64
	RepoID      int64
	CommitSHA   string
	IndexRunID  *int64
	Kind        EntityKind
	Path        string
	Name        string
	Signature   string
	StartLine   *int
	EndLine     *int
	BlobHash    string
	Source      string
	Confidence  Confidence
	Evidence    json.RawMessage
	Meta        json.RawMessage
	FactKey     string
	FactVersion int
	AnalysisID  *int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
