package execution

import (
	"fmt"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// HumanRequestStatus is the human_request state machine status. v0.22 unifies
// login takeover and structured questions on pending/answered/expired/cancelled
// (spec R-B3-1, DB-16). The 0.21 open/resolved vocabulary no longer exists.
type HumanRequestStatus string

const (
	HumanPending   HumanRequestStatus = "pending"
	HumanAnswered  HumanRequestStatus = "answered"
	HumanCancelled HumanRequestStatus = "cancelled"
	HumanExpired   HumanRequestStatus = "expired"
)

// HumanKind is the human_request kind enum. P3 only creates login_takeover;
// clarification/approval/blocked_recovery are the v0.22 question kinds.
type HumanKind string

const (
	HumanClarificationQuestion HumanKind = "clarification_question"
	HumanApproval              HumanKind = "approval"
	HumanBlockedRecovery       HumanKind = "blocked_recovery"
	HumanLoginTakeover         HumanKind = "login_takeover"
	HumanConflict              HumanKind = "conflict"
	HumanOther                 HumanKind = "other"
)

// MaxHumanRequestsPerCase bounds takeover attempts of one case_run (§6.8).
const MaxHumanRequestsPerCase = 3

// Structured-question payload bounds (spec R-B3-2).
const (
	MinQuestionsPerBatch         = 1
	MaxQuestionsPerBatch         = 5
	MaxQuestionOptions           = 10
	MaxQuestionSourceRefs        = 10
	MaxQuestionIDLength          = 64
	MaxQuestionTextLength        = 2000
	MaxQuestionOptionLabelLength = 200
	MaxQuestionSourceRefLength   = 500
	MaxQuestionBatchIDLength     = 64
	MaxQuestionAnswerTextLength  = 2000
	DefaultQuestionTimeout       = 24 * time.Hour
)

// Valid reports whether status is a persisted human request status.
func (s HumanRequestStatus) Valid() bool {
	switch s {
	case HumanPending, HumanAnswered, HumanCancelled, HumanExpired:
		return true
	default:
		return false
	}
}

// Terminal reports whether the request is closed.
func (s HumanRequestStatus) Terminal() bool { return s.Valid() && s != HumanPending }

// CanTransitionTo implements the human request state machine. Only a pending
// request can be answered/cancelled/expired; a closed request can be reopened to
// pending for a correction.
func (s HumanRequestStatus) CanTransitionTo(next HumanRequestStatus) bool {
	switch s {
	case HumanPending:
		return next == HumanAnswered || next == HumanCancelled || next == HumanExpired
	case HumanAnswered, HumanCancelled, HumanExpired:
		return next == HumanPending
	default:
		return false
	}
}

// Valid reports whether kind is a persisted human request kind.
func (k HumanKind) Valid() bool {
	switch k {
	case HumanClarificationQuestion, HumanApproval, HumanBlockedRecovery,
		HumanLoginTakeover, HumanConflict, HumanOther:
		return true
	default:
		return false
	}
}

// HumanQuestionOption is one answer option of a structured question.
type HumanQuestionOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// HumanQuestion is one question inside a batch (spec R-B3-2).
type HumanQuestion struct {
	ID            string                `json:"id"`
	Text          string                `json:"text"`
	Options       []HumanQuestionOption `json:"options"`
	Multiple      bool                  `json:"multiple"`
	AllowFreeText bool                  `json:"allow_free_text"`
	Required      bool                  `json:"required"`
	SourceRefs    []string              `json:"source_refs"`
}

// HumanQuestionBatch is the `human_request.question` JSONB payload (B §6.1).
type HumanQuestionBatch struct {
	Questions []HumanQuestion `json:"questions"`
	BatchID   string          `json:"batch_id"`
	ExpiresAt time.Time       `json:"expires_at"`
}

// HumanQuestionAnswer is one submitted answer (R-B3-3): the selected option ids
// and/or free text for one question of the batch.
type HumanQuestionAnswer struct {
	QuestionID string   `json:"question_id"`
	Selected   []string `json:"selected"`
	Text       string   `json:"text"`
}

// ValidateHumanQuestions validates a structured-question batch before it can be
// persisted (R-B3-2): 1-5 questions, unique ids, non-empty text, option shape,
// and the required/free-text consistency that makes a required question
// answerable.
func ValidateHumanQuestions(questions []HumanQuestion) error {
	if len(questions) < MinQuestionsPerBatch || len(questions) > MaxQuestionsPerBatch {
		return platform.Invalid("questions", fmt.Sprintf("must contain between %d and %d questions",
			MinQuestionsPerBatch, MaxQuestionsPerBatch))
	}
	seenQuestionIDs := map[string]struct{}{}
	for index, question := range questions {
		field := fmt.Sprintf("questions[%d]", index)
		id := strings.TrimSpace(question.ID)
		if id == "" || len(id) > MaxQuestionIDLength {
			return platform.Invalid(field+".id", "must be non-empty and at most 64 characters")
		}
		if _, duplicate := seenQuestionIDs[id]; duplicate {
			return platform.Invalid(field+".id", "must be unique within the batch")
		}
		seenQuestionIDs[id] = struct{}{}
		text := strings.TrimSpace(question.Text)
		if text == "" || len(text) > MaxQuestionTextLength {
			return platform.Invalid(field+".text", "must be non-empty and at most 2000 characters")
		}
		if len(question.Options) > MaxQuestionOptions {
			return platform.Invalid(field+".options", fmt.Sprintf("must contain at most %d options", MaxQuestionOptions))
		}
		if len(question.Options) == 0 && !question.AllowFreeText {
			return platform.Invalid(field, "must offer options or allow_free_text so the user can answer")
		}
		seenOptions := map[string]struct{}{}
		for optionIndex, option := range question.Options {
			optionField := fmt.Sprintf("%s.options[%d]", field, optionIndex)
			optionID := strings.TrimSpace(option.ID)
			if optionID == "" || len(optionID) > MaxQuestionIDLength {
				return platform.Invalid(optionField+".id", "must be non-empty and at most 64 characters")
			}
			if _, duplicate := seenOptions[optionID]; duplicate {
				return platform.Invalid(optionField+".id", "must be unique within the question")
			}
			seenOptions[optionID] = struct{}{}
			label := strings.TrimSpace(option.Label)
			if label == "" || len(label) > MaxQuestionOptionLabelLength {
				return platform.Invalid(optionField+".label", "must be non-empty and at most 200 characters")
			}
		}
		if len(question.SourceRefs) > MaxQuestionSourceRefs {
			return platform.Invalid(field+".source_refs", fmt.Sprintf("must contain at most %d refs", MaxQuestionSourceRefs))
		}
		for refIndex, ref := range question.SourceRefs {
			if strings.TrimSpace(ref) == "" || len(ref) > MaxQuestionSourceRefLength {
				return platform.Invalid(fmt.Sprintf("%s.source_refs[%d]", field, refIndex),
					"must be non-empty and at most 500 characters")
			}
		}
	}
	return nil
}

// ValidateHumanQuestionBatch validates the batch-level fields as well.
func ValidateHumanQuestionBatch(batch HumanQuestionBatch) error {
	if err := ValidateHumanQuestions(batch.Questions); err != nil {
		return err
	}
	if len(batch.BatchID) > MaxQuestionBatchIDLength {
		return platform.Invalid("batch_id", "must be at most 64 characters")
	}
	return nil
}

// ValidateHumanQuestionAnswers validates submitted answers against the batch:
// every required question must be answered, selections must reference real
// options, a single-choice question accepts at most one selection, free text is
// only accepted when allow_free_text is set, and unknown/duplicate answer ids
// are rejected.
func ValidateHumanQuestionAnswers(batch HumanQuestionBatch, answers []HumanQuestionAnswer) error {
	byID := make(map[string]HumanQuestion, len(batch.Questions))
	for _, question := range batch.Questions {
		byID[question.ID] = question
	}
	seen := map[string]struct{}{}
	answered := map[string]struct{}{}
	for index, answer := range answers {
		field := fmt.Sprintf("answers[%d]", index)
		questionID := strings.TrimSpace(answer.QuestionID)
		question, ok := byID[questionID]
		if !ok {
			return platform.Invalid(field+".question_id", "must reference a question of this batch")
		}
		if _, duplicate := seen[questionID]; duplicate {
			return platform.Invalid(field+".question_id", "must be unique within the response")
		}
		seen[questionID] = struct{}{}
		if len(answer.Selected) > 0 {
			if !question.Multiple && len(answer.Selected) > 1 {
				return platform.Invalid(field+".selected", "must contain at most one option for a single-choice question")
			}
			optionIDs := map[string]struct{}{}
			for _, option := range question.Options {
				optionIDs[option.ID] = struct{}{}
			}
			selectedSeen := map[string]struct{}{}
			for selectedIndex, selected := range answer.Selected {
				if _, ok := optionIDs[selected]; !ok {
					return platform.Invalid(fmt.Sprintf("%s.selected[%d]", field, selectedIndex),
						"must reference an option of this question")
				}
				if _, duplicate := selectedSeen[selected]; duplicate {
					return platform.Invalid(fmt.Sprintf("%s.selected[%d]", field, selectedIndex),
						"must be unique within the answer")
				}
				selectedSeen[selected] = struct{}{}
			}
		}
		text := strings.TrimSpace(answer.Text)
		if text != "" && !question.AllowFreeText {
			return platform.Invalid(field+".text", "is not allowed for this question")
		}
		if len(text) > MaxQuestionAnswerTextLength {
			return platform.Invalid(field+".text", "must be at most 2000 characters")
		}
		if len(answer.Selected) == 0 && text == "" {
			if question.Required {
				return platform.Invalid(field, fmt.Sprintf("question %q is required and needs a selection or text", question.ID))
			}
			continue
		}
		answered[questionID] = struct{}{}
	}
	for _, question := range batch.Questions {
		if question.Required {
			if _, ok := answered[question.ID]; !ok {
				return platform.Invalid("answers", fmt.Sprintf("question %q is required and needs a selection or text", question.ID))
			}
		}
	}
	return nil
}

// HumanRequest is one pending/closed human request. Structured questions bind
// the request to a conversation/message and carry the versioned question
// payload; login takeover requests leave ConversationID/MessageID nil.
type HumanRequest struct {
	ID             int64
	VersionID      int64
	CaseRunID      *int64
	ConversationID *int64
	MessageID      *int64
	Kind           HumanKind
	Payload        map[string]any
	Question       HumanQuestionBatch
	Status         HumanRequestStatus
	Response       map[string]any
	ExpiresAt      time.Time
	ResolvedBy     *int64
	ResolvedAt     *time.Time
	CreatedBy      *int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// BlocksStage always reports false: v0.22 structured questions never gate stage
// progression (R-B3-4, user decision C-2). The stage machine must not treat a
// pending or expired question as a blocker.
func (r HumanRequest) BlocksStage() bool { return false }

// Human request payload field names (spec §3.3) plus the structured-question
// ownership metadata.
const (
	HumanPayloadReason        = "reason"
	HumanPayloadVersionSUTID  = "version_sut_id"
	HumanPayloadAccountRole   = "account_role"
	HumanPayloadPageURL       = "page_url"
	HumanPayloadChallengeKind = "challenge_kind"
	HumanPayloadEvidenceID    = "evidence_id"
	// HumanPayloadAgentRunID is the Host run that created a structured question.
	// Only that run may cancel/reopen its own questions (R-B3-5).
	HumanPayloadAgentRunID = "agent_run_id"
)

// Human request response field names (spec §3.3 / §6.8) plus structured answers.
const (
	HumanResponseCode            = "code"
	HumanResponseNote            = "note"
	HumanResponseAnswers         = "answers"
	HumanResponseAnsweredBy      = "answered_by"
	HumanResponseAnsweredAt      = "answered_at"
	HumanResponseAnswerRevision  = "answer_revision"
	HumanResponseAnswerMessageID = "answer_message_id"
	HumanResponseSkipped         = "skipped"
	HumanResponseSkippedBy       = "skipped_by"
	HumanResponseSkippedAt       = "skipped_at"
	HumanResponseReopenedBy      = "reopened_by"
	HumanResponseReopenedAt      = "reopened_at"
	HumanResponseReason          = "reason"
)

// Human request verdict reasons written to case_run.summary.
const (
	VerdictReasonHumanTimeout   = "human_timeout"
	VerdictReasonHumanCancelled = "human_cancelled"
)
