package domain

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Requirement statuses, mirrored from the requirement.status CHECK constraint.
const (
	RequirementDraft     = "draft"
	RequirementClarified = "clarified"
	RequirementDesigned  = "designed"
	RequirementVerified  = "verified"
	RequirementObsolete  = "obsolete"
)

// Priorities, mirrored from the requirement.priority CHECK constraint.
const (
	PriorityMust   = "must"
	PriorityShould = "should"
	PriorityCould  = "could"
)

// Sources for AI/user-authored content, mirrored from every source CHECK
// constraint.
const (
	SourceUser = "user"
	SourceAI   = "ai"
)

// Requirement is one item of the version requirement pipeline.
type Requirement struct {
	ID                 int64
	VersionID          int64
	Title              string
	Description        string
	AcceptanceCriteria string
	Priority           string
	Status             string
	Source             string
	LastUpdateSource   string
	Revision           int
	ConfirmedAt        *time.Time
	ConfirmedBy        *int64
	CreatedBy          int64
	UpdatedBy          int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Validate enforces the requirement bounds of spec §6.2 and the frozen enums of
// the requirement CHECK constraints.
func (r *Requirement) Validate() error {
	if r == nil {
		return NewInvalid("", "invalid_requirement", "requirement is required")
	}
	if r.VersionID <= 0 {
		return NewInvalid("versionId", "invalid_id", "versionId must be a positive id")
	}
	if strings.TrimSpace(r.Title) == "" {
		return NewInvalid("title", "invalid_title", "title is required")
	}
	if utf8.RuneCountInString(r.Title) > MaxTitleChars {
		return NewInvalid("title", "invalid_title",
			fmt.Sprintf("title must be at most %d characters", MaxTitleChars))
	}
	if utf8.RuneCountInString(r.Description) > MaxDescriptionChars {
		return NewInvalid("description", "invalid_description",
			fmt.Sprintf("description must be at most %d characters", MaxDescriptionChars))
	}
	if utf8.RuneCountInString(r.AcceptanceCriteria) > MaxAcceptanceCriteriaChars {
		return NewInvalid("acceptanceCriteria", "invalid_acceptance_criteria",
			fmt.Sprintf("acceptanceCriteria must be at most %d characters", MaxAcceptanceCriteriaChars))
	}
	if !validPriority(r.Priority) {
		return NewInvalid("priority", "invalid_priority", "priority must be must, should or could")
	}
	if !validRequirementStatus(r.Status) {
		return NewInvalid("status", "invalid_status",
			"status must be one of draft, clarified, designed, verified or obsolete")
	}
	if r.Source != SourceUser && r.Source != SourceAI {
		return NewInvalid("source", "invalid_source", "source must be user or ai")
	}
	if r.LastUpdateSource != SourceUser && r.LastUpdateSource != SourceAI {
		return NewInvalid("lastUpdateSource", "invalid_last_update_source",
			"lastUpdateSource must be user or ai")
	}
	return nil
}

// validPriority reports whether priority is one of the DDL's frozen values.
func validPriority(priority string) bool {
	switch priority {
	case PriorityMust, PriorityShould, PriorityCould:
		return true
	default:
		return false
	}
}

// validRequirementStatus reports whether status is one of the DDL's frozen
// values.
func validRequirementStatus(status string) bool {
	switch status {
	case RequirementDraft, RequirementClarified, RequirementDesigned,
		RequirementVerified, RequirementObsolete:
		return true
	default:
		return false
	}
}
