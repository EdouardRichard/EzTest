package domain

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Clarification session statuses, mirrored from the clarification_sessions
// status CHECK constraint.
const (
	SessionActive    = "active"
	SessionCompleted = "completed"
	SessionCancelled = "cancelled"
)

// Question statuses, mirrored from the clarification_questions status CHECK
// constraint.
const (
	QuestionOpen      = "open"
	QuestionAnswered  = "answered"
	QuestionDismissed = "dismissed"
)

// Answered-via values, mirrored from the clarification_questions
// answered_via CHECK constraint.
const (
	AnsweredViaUI    = "ui"
	AnsweredViaAgent = "agent"
)

// ClarificationSession is one version-level clarification conversation. At most
// one active session exists per version (partial unique index).
type ClarificationSession struct {
	ID               int64
	VersionID        int64
	Status           string
	AgentSessionID   *int64
	ContextSummary   string
	SummaryUpdatedAt *time.Time
	StartedBy        int64
	StartedAt        time.Time
	EndedAt          *time.Time
	EndedReason      string
}

// QuestionFilter selects a page of clarification questions ordered by id
// ascending.
type QuestionFilter struct {
	VersionID     int64
	Status        string
	RequirementID *int64
	Page          Page
}

// Question is one structured clarification question. RequirementID is nil for
// version-level questions; SessionID is nil when the question was recorded
// manually outside a session.
type Question struct {
	ID            int64
	VersionID     int64
	RequirementID *int64
	SessionID     *int64
	Question      string
	Status        string
	Answer        string
	Source        string
	AnsweredVia   string
	AnsweredBy    *int64
	AnsweredAt    *time.Time
	CreatedBy     int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// StartRunResult is the outcome of starting one P1 agent run on behalf of a
// version. IDs are P3/agent ids; the HTTP layer serialises P3-owned ids as
// strings.
type StartRunResult struct {
	SessionID      int64
	AgentSessionID int64
	RunID          int64
	Status         string
}

// Validate enforces the session bounds of spec §6.5 and the frozen status enum.
func (s *ClarificationSession) Validate() error {
	if s == nil {
		return NewInvalid("", "invalid_clarification_session", "clarification session is required")
	}
	if s.VersionID <= 0 {
		return NewInvalid("versionId", "invalid_id", "versionId must be a positive id")
	}
	if !validSessionStatus(s.Status) {
		return NewInvalid("status", "invalid_status", "status must be active, completed or cancelled")
	}
	return nil
}

// Validate enforces the question bounds of spec §6.6 and the frozen enums of
// the clarification_questions CHECK constraints.
func (q *Question) Validate() error {
	if q == nil {
		return NewInvalid("", "invalid_question", "question is required")
	}
	if q.VersionID <= 0 {
		return NewInvalid("versionId", "invalid_id", "versionId must be a positive id")
	}
	if strings.TrimSpace(q.Question) == "" {
		return NewInvalid("question", "invalid_question", "question is required")
	}
	if utf8.RuneCountInString(q.Question) > MaxQuestionChars {
		return NewInvalid("question", "invalid_question",
			fmt.Sprintf("question must be at most %d characters", MaxQuestionChars))
	}
	if !validQuestionStatus(q.Status) {
		return NewInvalid("status", "invalid_status", "status must be open, answered or dismissed")
	}
	if utf8.RuneCountInString(q.Answer) > MaxAnswerChars {
		return NewInvalid("answer", "invalid_answer",
			fmt.Sprintf("answer must be at most %d characters", MaxAnswerChars))
	}
	if q.Status == QuestionAnswered && q.Answer == "" {
		return NewInvalid("answer", "invalid_answer", "answer is required when status is answered")
	}
	if q.Source != SourceUser && q.Source != SourceAI {
		return NewInvalid("source", "invalid_source", "source must be user or ai")
	}
	if q.AnsweredVia != AnsweredViaUI && q.AnsweredVia != AnsweredViaAgent {
		return NewInvalid("answeredVia", "invalid_answered_via", "answeredVia must be ui or agent")
	}
	return nil
}

// validSessionStatus reports whether status is one of the DDL's frozen values.
func validSessionStatus(status string) bool {
	switch status {
	case SessionActive, SessionCompleted, SessionCancelled:
		return true
	default:
		return false
	}
}

// validQuestionStatus reports whether status is one of the DDL's frozen values.
func validQuestionStatus(status string) bool {
	switch status {
	case QuestionOpen, QuestionAnswered, QuestionDismissed:
		return true
	default:
		return false
	}
}
