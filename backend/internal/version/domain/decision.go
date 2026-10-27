package domain

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Decision statuses, mirrored from the decisions.status CHECK constraint.
const (
	DecisionProposed = "proposed"
	DecisionAccepted = "accepted"
	DecisionRejected = "rejected"
)

// DecisionFilter selects a page of decisions ordered by id ascending.
type DecisionFilter struct {
	VersionID int64
	Status    string
	Page      Page
}

// Decision is one recorded design decision. RequirementID and QuestionID are
// optional; accepting a proposed decision linked to a question also answers
// that question.
type Decision struct {
	ID            int64
	VersionID     int64
	RequirementID *int64
	QuestionID    *int64
	Topic         string
	Decision      string
	Rationale     string
	Status        string
	Source        string
	ProposedBy    int64
	DecidedBy     *int64
	DecidedAt     *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Validate enforces the decision bounds of spec §6.6 and the frozen enums of
// the decisions CHECK constraints.
func (d *Decision) Validate() error {
	if d == nil {
		return NewInvalid("", "invalid_decision", "decision is required")
	}
	if d.VersionID <= 0 {
		return NewInvalid("versionId", "invalid_id", "versionId must be a positive id")
	}
	if strings.TrimSpace(d.Topic) == "" {
		return NewInvalid("topic", "invalid_topic", "topic is required")
	}
	if utf8.RuneCountInString(d.Topic) > MaxTopicChars {
		return NewInvalid("topic", "invalid_topic",
			fmt.Sprintf("topic must be at most %d characters", MaxTopicChars))
	}
	if strings.TrimSpace(d.Decision) == "" {
		return NewInvalid("decision", "invalid_decision", "decision is required")
	}
	if utf8.RuneCountInString(d.Decision) > MaxDecisionChars {
		return NewInvalid("decision", "invalid_decision",
			fmt.Sprintf("decision must be at most %d characters", MaxDecisionChars))
	}
	if utf8.RuneCountInString(d.Rationale) > MaxDecisionChars {
		return NewInvalid("rationale", "invalid_rationale",
			fmt.Sprintf("rationale must be at most %d characters", MaxDecisionChars))
	}
	if !validDecisionStatus(d.Status) {
		return NewInvalid("status", "invalid_status", "status must be proposed, accepted or rejected")
	}
	if d.Source != SourceUser && d.Source != SourceAI {
		return NewInvalid("source", "invalid_source", "source must be user or ai")
	}
	return nil
}

// validDecisionStatus reports whether status is one of the DDL's frozen values.
func validDecisionStatus(status string) bool {
	switch status {
	case DecisionProposed, DecisionAccepted, DecisionRejected:
		return true
	default:
		return false
	}
}
