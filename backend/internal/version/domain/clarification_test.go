package domain

import (
	"strings"
	"testing"
)

func validClarificationSession() ClarificationSession {
	return ClarificationSession{
		ID:        31,
		VersionID: 11,
		Status:    SessionActive,
		StartedBy: 1,
	}
}

func TestClarificationSessionValidate(t *testing.T) {
	tests := []struct {
		name   string
		build  func(*ClarificationSession)
		valid  bool
		field  string
		reason string
	}{
		{name: "valid active session", valid: true},
		{
			name:  "completed session is valid",
			build: func(s *ClarificationSession) { s.Status = SessionCompleted },
			valid: true,
		},
		{
			name:  "cancelled session is valid",
			build: func(s *ClarificationSession) { s.Status = SessionCancelled },
			valid: true,
		},
		{
			name:   "version id required",
			build:  func(s *ClarificationSession) { s.VersionID = 0 },
			field:  "versionId",
			reason: "invalid_id",
		},
		{
			name:   "status must be active/completed/cancelled",
			build:  func(s *ClarificationSession) { s.Status = "paused" },
			field:  "status",
			reason: "invalid_status",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			session := validClarificationSession()
			if tc.build != nil {
				tc.build(&session)
			}
			err := session.Validate()
			if tc.valid {
				if err != nil {
					t.Fatalf("want valid session, got %v", err)
				}
				return
			}
			assertDomainError(t, err, tc.field, tc.reason)
		})
	}
}

func validQuestion() Question {
	return Question{
		VersionID:   11,
		Question:    "Which payment providers must be supported?",
		Status:      QuestionOpen,
		Source:      SourceAI,
		AnsweredVia: "ui",
	}
}

func TestQuestionValidate(t *testing.T) {
	tests := []struct {
		name   string
		build  func(*Question)
		valid  bool
		field  string
		reason string
	}{
		{name: "valid open question", valid: true},
		{
			name: "valid answered question",
			build: func(q *Question) {
				q.Status = QuestionAnswered
				q.Answer = "Stripe and PayPal"
			},
			valid: true,
		},
		{
			name:  "valid dismissed question",
			build: func(q *Question) { q.Status = QuestionDismissed },
			valid: true,
		},
		{
			name:   "version id required",
			build:  func(q *Question) { q.VersionID = 0 },
			field:  "versionId",
			reason: "invalid_id",
		},
		{
			name:   "question text required",
			build:  func(q *Question) { q.Question = "" },
			field:  "question",
			reason: "invalid_question",
		},
		{
			name:  "question at limit",
			build: func(q *Question) { q.Question = strings.Repeat("q", MaxQuestionChars) },
			valid: true,
		},
		{
			name:   "question too long",
			build:  func(q *Question) { q.Question = strings.Repeat("q", MaxQuestionChars+1) },
			field:  "question",
			reason: "invalid_question",
		},
		{
			name: "answer at limit",
			build: func(q *Question) {
				q.Status = QuestionAnswered
				q.Answer = strings.Repeat("a", MaxAnswerChars)
			},
			valid: true,
		},
		{
			name: "answer too long",
			build: func(q *Question) {
				q.Status = QuestionAnswered
				q.Answer = strings.Repeat("a", MaxAnswerChars+1)
			},
			field:  "answer",
			reason: "invalid_answer",
		},
		{
			name:   "status must be open/answered/dismissed",
			build:  func(q *Question) { q.Status = "pending" },
			field:  "status",
			reason: "invalid_status",
		},
		{
			name:   "source must be user/ai",
			build:  func(q *Question) { q.Source = "agent" },
			field:  "source",
			reason: "invalid_source",
		},
		{
			name:   "answered via must be ui/agent",
			build:  func(q *Question) { q.AnsweredVia = "email" },
			field:  "answeredVia",
			reason: "invalid_answered_via",
		},
		{
			name: "answered question requires an answer",
			build: func(q *Question) {
				q.Status = QuestionAnswered
			},
			field:  "answer",
			reason: "invalid_answer",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			question := validQuestion()
			if tc.build != nil {
				tc.build(&question)
			}
			err := question.Validate()
			if tc.valid {
				if err != nil {
					t.Fatalf("want valid question, got %v", err)
				}
				return
			}
			assertDomainError(t, err, tc.field, tc.reason)
		})
	}
}

func validDecision() Decision {
	return Decision{
		VersionID: 11,
		Topic:     "Payment provider",
		Decision:  "Use Stripe as the only provider",
		Status:    DecisionProposed,
		Source:    SourceAI,
	}
}

func TestDecisionValidate(t *testing.T) {
	tests := []struct {
		name   string
		build  func(*Decision)
		valid  bool
		field  string
		reason string
	}{
		{name: "valid proposed decision", valid: true},
		{
			name:  "valid accepted decision",
			build: func(d *Decision) { d.Status = DecisionAccepted },
			valid: true,
		},
		{
			name:  "valid rejected decision",
			build: func(d *Decision) { d.Status = DecisionRejected },
			valid: true,
		},
		{
			name:   "version id required",
			build:  func(d *Decision) { d.VersionID = 0 },
			field:  "versionId",
			reason: "invalid_id",
		},
		{
			name:   "topic required",
			build:  func(d *Decision) { d.Topic = "" },
			field:  "topic",
			reason: "invalid_topic",
		},
		{
			name:  "topic at limit",
			build: func(d *Decision) { d.Topic = strings.Repeat("t", MaxTopicChars) },
			valid: true,
		},
		{
			name:   "topic too long",
			build:  func(d *Decision) { d.Topic = strings.Repeat("t", MaxTopicChars+1) },
			field:  "topic",
			reason: "invalid_topic",
		},
		{
			name:   "decision required",
			build:  func(d *Decision) { d.Decision = "" },
			field:  "decision",
			reason: "invalid_decision",
		},
		{
			name:  "decision at limit",
			build: func(d *Decision) { d.Decision = strings.Repeat("d", MaxDecisionChars) },
			valid: true,
		},
		{
			name:   "decision too long",
			build:  func(d *Decision) { d.Decision = strings.Repeat("d", MaxDecisionChars+1) },
			field:  "decision",
			reason: "invalid_decision",
		},
		{
			name:  "rationale at limit",
			build: func(d *Decision) { d.Rationale = strings.Repeat("r", MaxDecisionChars) },
			valid: true,
		},
		{
			name:   "rationale too long",
			build:  func(d *Decision) { d.Rationale = strings.Repeat("r", MaxDecisionChars+1) },
			field:  "rationale",
			reason: "invalid_rationale",
		},
		{
			name:   "status must be proposed/accepted/rejected",
			build:  func(d *Decision) { d.Status = "pending" },
			field:  "status",
			reason: "invalid_status",
		},
		{
			name:   "source must be user/ai",
			build:  func(d *Decision) { d.Source = "agent" },
			field:  "source",
			reason: "invalid_source",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			decision := validDecision()
			if tc.build != nil {
				tc.build(&decision)
			}
			err := decision.Validate()
			if tc.valid {
				if err != nil {
					t.Fatalf("want valid decision, got %v", err)
				}
				return
			}
			assertDomainError(t, err, tc.field, tc.reason)
		})
	}
}
