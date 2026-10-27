package domain

import (
	"strings"
	"testing"
)

// validRequirement is the smallest valid aggregate; each case changes one
// aspect.
func validRequirement() Requirement {
	return Requirement{
		VersionID:        11,
		Title:            "Checkout supports coupons",
		Priority:         PriorityShould,
		Status:           RequirementDraft,
		Source:           SourceUser,
		LastUpdateSource: SourceUser,
	}
}

func TestRequirementValidate(t *testing.T) {
	tests := []struct {
		name   string
		build  func(*Requirement)
		valid  bool
		field  string
		reason string
	}{
		{name: "minimal valid requirement", valid: true},
		{
			name: "valid clarified requirement",
			build: func(r *Requirement) {
				r.Status = RequirementClarified
				r.Priority = PriorityMust
			},
			valid: true,
		},
		{
			name:   "version id required",
			build:  func(r *Requirement) { r.VersionID = 0 },
			field:  "versionId",
			reason: "invalid_id",
		},
		{
			name:   "title required",
			build:  func(r *Requirement) { r.Title = "" },
			field:  "title",
			reason: "invalid_title",
		},
		{
			name:  "title at limit counts runes",
			build: func(r *Requirement) { r.Title = strings.Repeat("需", MaxTitleChars) },
			valid: true,
		},
		{
			name:   "title too long counts runes",
			build:  func(r *Requirement) { r.Title = strings.Repeat("需", MaxTitleChars+1) },
			field:  "title",
			reason: "invalid_title",
		},
		{
			name:  "description at limit",
			build: func(r *Requirement) { r.Description = strings.Repeat("d", MaxDescriptionChars) },
			valid: true,
		},
		{
			name:   "description too long",
			build:  func(r *Requirement) { r.Description = strings.Repeat("d", MaxDescriptionChars+1) },
			field:  "description",
			reason: "invalid_description",
		},
		{
			name: "acceptance criteria at limit",
			build: func(r *Requirement) {
				r.AcceptanceCriteria = strings.Repeat("c", MaxAcceptanceCriteriaChars)
			},
			valid: true,
		},
		{
			name: "acceptance criteria too long",
			build: func(r *Requirement) {
				r.AcceptanceCriteria = strings.Repeat("c", MaxAcceptanceCriteriaChars+1)
			},
			field:  "acceptanceCriteria",
			reason: "invalid_acceptance_criteria",
		},
		{
			name:   "priority must be must/should/could",
			build:  func(r *Requirement) { r.Priority = "critical" },
			field:  "priority",
			reason: "invalid_priority",
		},
		{
			name:   "status must be a known enum value",
			build:  func(r *Requirement) { r.Status = "done" },
			field:  "status",
			reason: "invalid_status",
		},
		{
			name:   "source must be user/ai",
			build:  func(r *Requirement) { r.Source = "agent" },
			field:  "source",
			reason: "invalid_source",
		},
		{
			name:   "last update source must be user/ai",
			build:  func(r *Requirement) { r.LastUpdateSource = "agent" },
			field:  "lastUpdateSource",
			reason: "invalid_last_update_source",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			requirement := validRequirement()
			if tc.build != nil {
				tc.build(&requirement)
			}
			err := requirement.Validate()
			if tc.valid {
				if err != nil {
					t.Fatalf("want valid requirement, got %v", err)
				}
				return
			}
			assertDomainError(t, err, tc.field, tc.reason)
		})
	}
}
