package domain

import "testing"

// validRelation is the smallest valid aggregate; each case changes one aspect.
func validRelation() Relation {
	return Relation{
		VersionID:         11,
		FromRequirementID: 21,
		ToRequirementID:   22,
		RelationType:      RelationDependsOn,
		Source:            SourceUser,
	}
}

func TestRelationValidate(t *testing.T) {
	tests := []struct {
		name   string
		build  func(*Relation)
		valid  bool
		field  string
		reason string
	}{
		{name: "valid directed relation", valid: true},
		{
			name:  "undirected relation types are valid",
			build: func(r *Relation) { r.RelationType = RelationConflictsWith },
			valid: true,
		},
		{
			name:  "overlaps is a valid relation type",
			build: func(r *Relation) { r.RelationType = RelationOverlaps },
			valid: true,
		},
		{
			name:  "shares scope is a valid relation type",
			build: func(r *Relation) { r.RelationType = RelationSharesScope },
			valid: true,
		},
		{
			name:   "self relation is rejected",
			build:  func(r *Relation) { r.ToRequirementID = r.FromRequirementID },
			field:  "toRequirementId",
			reason: "invalid_relation",
		},
		{
			name:   "relation type must be one of the four",
			build:  func(r *Relation) { r.RelationType = "blocks" },
			field:  "relationType",
			reason: "invalid_relation_type",
		},
		{
			name:   "version id required",
			build:  func(r *Relation) { r.VersionID = 0 },
			field:  "versionId",
			reason: "invalid_id",
		},
		{
			name:   "from requirement id required",
			build:  func(r *Relation) { r.FromRequirementID = 0 },
			field:  "fromRequirementId",
			reason: "invalid_relation",
		},
		{
			name:   "to requirement id required",
			build:  func(r *Relation) { r.ToRequirementID = 0 },
			field:  "toRequirementId",
			reason: "invalid_relation",
		},
		{
			name:   "source must be user/ai",
			build:  func(r *Relation) { r.Source = "agent" },
			field:  "source",
			reason: "invalid_source",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			relation := validRelation()
			if tc.build != nil {
				tc.build(&relation)
			}
			err := relation.Validate()
			if tc.valid {
				if err != nil {
					t.Fatalf("want valid relation, got %v", err)
				}
				return
			}
			assertDomainError(t, err, tc.field, tc.reason)
		})
	}
}
