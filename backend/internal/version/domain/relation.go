package domain

import "time"

// Relation types, mirrored from the requirement_relations.relation_type CHECK
// constraint. depends_on is directed; the other three are undirected and stored
// with from/to preserved.
const (
	RelationDependsOn     = "depends_on"
	RelationConflictsWith = "conflicts_with"
	RelationOverlaps      = "overlaps"
	RelationSharesScope   = "shares_scope"
)

// Relation is one typed edge between two requirements of the same version.
type Relation struct {
	ID                int64
	VersionID         int64
	FromRequirementID int64
	ToRequirementID   int64
	RelationType      string
	Description       string
	Source            string
	CreatedBy         int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Validate enforces the relation invariants of spec §6.4: both endpoints are
// positive ids of the same version (the cross-version check needs repository
// data), no self-loop and a frozen relation type.
func (r *Relation) Validate() error {
	if r == nil {
		return NewInvalid("", "invalid_relation", "relation is required")
	}
	if r.VersionID <= 0 {
		return NewInvalid("versionId", "invalid_id", "versionId must be a positive id")
	}
	if r.FromRequirementID <= 0 {
		return NewInvalid("fromRequirementId", "invalid_relation",
			"fromRequirementId must be a positive id")
	}
	if r.ToRequirementID <= 0 {
		return NewInvalid("toRequirementId", "invalid_relation",
			"toRequirementId must be a positive id")
	}
	if r.FromRequirementID == r.ToRequirementID {
		return NewInvalid("toRequirementId", "invalid_relation",
			"a requirement cannot have a relation to itself")
	}
	if !validRelationType(r.RelationType) {
		return NewInvalid("relationType", "invalid_relation_type",
			"relationType must be depends_on, conflicts_with, overlaps or shares_scope")
	}
	if r.Source != SourceUser && r.Source != SourceAI {
		return NewInvalid("source", "invalid_source", "source must be user or ai")
	}
	return nil
}

// validRelationType reports whether relationType is one of the DDL's frozen
// values.
func validRelationType(relationType string) bool {
	switch relationType {
	case RelationDependsOn, RelationConflictsWith, RelationOverlaps, RelationSharesScope:
		return true
	default:
		return false
	}
}
