package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// Clarification session, question and decision aliases keep the service
// signatures of the plan's Shared Interfaces unqualified while the single source
// of truth stays in version/domain.
type (
	// ClarificationSession is one version-level clarification conversation.
	ClarificationSession = domain.ClarificationSession
	// ClarificationRepository is the persistence port for sessions, questions and
	// decisions (implemented by the postgres repository).
	ClarificationRepository = domain.ClarificationRepository
	// Question is one structured clarification question.
	Question = domain.Question
	// QuestionFilter selects a page of clarification questions.
	QuestionFilter = domain.QuestionFilter
	// Decision is one recorded design decision.
	Decision = domain.Decision
	// DecisionFilter selects a page of decisions.
	DecisionFilter = domain.DecisionFilter
	// StartRunResult is the outcome of starting one P1 agent run on behalf of a
	// version.
	StartRunResult = domain.StartRunResult
)

// P1 agent profile keys used by the clarification runs (spec §8.1).
const (
	clarificationDialogProfile = "clarification-dialog"
	requirementAnalysisProfile = "requirement-analysis"
	contextReconProfile        = "context-recon"
)

// MaxClarificationMessageChars bounds one clarification message (spec §7.5).
const MaxClarificationMessageChars = 5000

// MaxClarificationContextSummaryBytes bounds the context summary written by the
// context.summary_update tool (spec §8.3: at most 20 KiB).
const MaxClarificationContextSummaryBytes = 20 << 10

// clarificationCountPageSize is the page size used by the overview counters. The
// repository exposes no aggregate-count method, so the service walks the
// keyset pages; the bound keeps every request to a handful of queries.
const clarificationCountPageSize = 100

// startRunStatusRunning is the StartRunResult status of a freshly started run
// (spec §7.5: the endpoint returns 202 with status "running").
const startRunStatusRunning = "running"

// QuestionInput is the create/update question request (spec §6.6/§8.3).
// QuestionID selects an existing open question to rewrite; a nil one creates a
// new open question. RequirementID is optional and, on update, nil keeps the
// stored link. SessionID is optional and auto-filled from the active session for
// AI-authored questions when omitted.
type QuestionInput struct {
	QuestionID    *int64
	RequirementID *int64
	SessionID     *int64
	Question      string
}

// DecisionInput is the create-decision request (spec §6.6/§8.3). RequirementID
// and QuestionID are optional. An AI-sourced decision is proposed; a
// user-sourced one is accepted immediately, and accepting a decision linked to
// an open question answers that question in the same transaction.
type DecisionInput struct {
	RequirementID *int64
	QuestionID    *int64
	Topic         string
	Decision      string
	Rationale     string
}

// ClarificationCounts are the per-status counters of the clarification overview
// (spec §7.5 GET clarification).
type ClarificationCounts struct {
	OpenQuestions      int
	AnsweredQuestions  int
	DismissedQuestions int

	ProposedDecisions int
	AcceptedDecisions int
	RejectedDecisions int
}

// ClarificationService implements the clarification use cases of spec §6.5/
// §6.6: sessions and their P1 agent runs, structured questions and decisions.
// Every database write publishes a version event after the repository call
// returns (spec §6.10).
type ClarificationService struct {
	repo         ClarificationRepository
	requirements RequirementRepository
	versions     VersionRepository
	agents       AgentSessions
	events       EventPublisher
	ids          IDGenerator
}

// ClarificationServiceOption customises the clarification service dependencies.
// Production (T16) injects the process-wide Snowflake generator so session,
// question and decision ids share the one node sequence.
type ClarificationServiceOption func(*ClarificationService)

// WithClarificationIDGenerator injects the Snowflake id generator. Without it
// the package-wide DefaultIDGenerator is used, which is safe for a single
// process.
func WithClarificationIDGenerator(ids IDGenerator) ClarificationServiceOption {
	return func(s *ClarificationService) {
		if ids != nil {
			s.ids = ids
		}
	}
}

// NewClarificationService wires the clarification use cases to their repository,
// the requirement repository (question/decision links), the version repository
// (existence and status gates), the P1 agent sessions port and the event
// publisher.
func NewClarificationService(repo ClarificationRepository, requirements RequirementRepository, versions VersionRepository, agents AgentSessions, events EventPublisher, opts ...ClarificationServiceOption) *ClarificationService {
	service := &ClarificationService{
		repo:         repo,
		requirements: requirements,
		versions:     versions,
		agents:       agents,
		events:       events,
		ids:          DefaultIDGenerator(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(service)
		}
	}
	return service
}

// StartSession starts one clarification session for the version: the P1 agent
// session is created first (profile clarification-dialog, channel version:<id>),
// then the P3 row is inserted as active. Only clarifying/designing versions may
// start a session and only one session may be active per version (spec §6.5).
func (s *ClarificationService) StartSession(ctx context.Context, versionID int64, actor int64) (*ClarificationSession, error) {
	version, err := s.ensureVersion(ctx, versionID)
	if err != nil {
		return nil, err
	}
	if version.Status != domain.VersionClarifying && version.Status != domain.VersionDesigning {
		return nil, domain.NewStateConflict("version_state_conflict",
			fmt.Sprintf("version %d is %s, clarification sessions require clarifying or designing", versionID, version.Status))
	}
	if s.agents == nil {
		return nil, errors.New("version: agent sessions are not configured")
	}
	existing, err := s.repo.GetActiveSession(ctx, versionID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, domain.NewConflict("clarification_session_active",
			fmt.Sprintf("version %d already has an active clarification session", versionID))
	}

	// AgentSessions has no delete primitive: if the P3 insert below fails (unique
	// active-index race or database error) this P1 session is left without runs.
	// The orphan is bounded (a single session row, no active P3 row) and the P8
	// cleanup sweeps it; do not add a delete primitive for this (Lead ruling,
	// Task 10 fix round 1).
	agentSessionID, err := s.agents.Create(ctx, clarificationDialogProfile,
		fmt.Sprintf("Clarification for version %d", versionID), actor,
		clarificationChannel(versionID), strconv.FormatInt(versionID, 10))
	if err != nil {
		return nil, err
	}
	id := s.ids.Next()
	if id <= 0 {
		return nil, errors.New("version: id generator returned a non-positive id")
	}
	session := &ClarificationSession{
		ID:             id,
		VersionID:      versionID,
		Status:         domain.SessionActive,
		AgentSessionID: &agentSessionID,
		StartedBy:      actor,
	}
	if err := session.Validate(); err != nil {
		return nil, err
	}
	if err := s.repo.CreateSession(ctx, session); err != nil {
		return nil, err
	}
	s.publish(ctx, VersionEvent{
		VersionID:    versionID,
		Type:         "clarification.session_started",
		Summary:      "clarification session started",
		RefSessionID: clarificationIDPtr(session.ID),
		ActorID:      actor,
		Source:       domain.SourceUser,
		Detail: map[string]any{
			"sessionId":      eventID(session.ID),
			"agentSessionId": agentSessionID,
		},
	})
	return session, nil
}

// CompleteSession completes one active session. A running agent run for the
// version blocks completion (spec §6.5).
func (s *ClarificationService) CompleteSession(ctx context.Context, versionID, sessionID int64, actor int64) (*ClarificationSession, error) {
	if _, err := s.ensureVersion(ctx, versionID); err != nil {
		return nil, err
	}
	session, err := s.loadSession(ctx, versionID, sessionID)
	if err != nil {
		return nil, err
	}
	if session.Status != domain.SessionActive {
		return nil, domain.NewStateConflict("clarification_session_not_active",
			fmt.Sprintf("clarification session %d is %s", sessionID, session.Status))
	}
	if err := s.ensureAgentIdle(ctx, versionID); err != nil {
		return nil, err
	}

	completed, err := s.repo.CompleteSession(ctx, sessionID, actor)
	if err != nil {
		return nil, err
	}
	s.publish(ctx, VersionEvent{
		VersionID:    versionID,
		Type:         "clarification.session_completed",
		Summary:      "clarification session completed",
		RefSessionID: clarificationIDPtr(sessionID),
		ActorID:      actor,
		Source:       domain.SourceUser,
		Detail: map[string]any{
			"sessionId": eventID(sessionID),
			"status":    domain.SessionCompleted,
		},
	})
	return completed, nil
}

// SendMessage starts one text P1 run on the active session. An active session
// with a P1 agent session and no running run is required (spec §6.5/§8.4).
func (s *ClarificationService) SendMessage(ctx context.Context, versionID int64, message string, actor int64) (StartRunResult, error) {
	if _, err := s.ensureVersion(ctx, versionID); err != nil {
		return StartRunResult{}, err
	}
	session, err := s.repo.GetActiveSession(ctx, versionID)
	if err != nil {
		return StartRunResult{}, err
	}
	if session == nil || session.Status != domain.SessionActive || session.AgentSessionID == nil {
		return StartRunResult{}, domain.NewStateConflict("clarification_session_not_active",
			fmt.Sprintf("version %d has no active clarification session with an agent session", versionID))
	}
	if err := validateClarificationMessage(message); err != nil {
		return StartRunResult{}, err
	}
	if err := s.ensureAgentIdle(ctx, versionID); err != nil {
		return StartRunResult{}, err
	}
	if s.agents == nil {
		return StartRunResult{}, errors.New("version: agent sessions are not configured")
	}

	runID, err := s.agents.StartText(ctx, *session.AgentSessionID, message, actor)
	if err != nil {
		return StartRunResult{}, err
	}
	return StartRunResult{
		SessionID:      session.ID,
		AgentSessionID: *session.AgentSessionID,
		RunID:          runID,
		Status:         startRunStatusRunning,
	}, nil
}

// StartAnalysis starts a one-shot requirement-analysis run. It requires an
// active clarification session and no running agent (spec §7.5/§8.4).
func (s *ClarificationService) StartAnalysis(ctx context.Context, versionID int64, actor int64) (StartRunResult, error) {
	instruction := fmt.Sprintf(
		"Analyze the requirements of version %d. Call version.context_get with versionId=%q first, then record relations, questions and decisions for the version.",
		versionID, strconv.FormatInt(versionID, 10))
	return s.startOneShot(ctx, versionID, actor, requirementAnalysisProfile,
		fmt.Sprintf("Requirement analysis for version %d", versionID), instruction, false, nil)
}

// StartRecon starts a one-shot context-recon run with the instruction text part
// only. It delegates to StartReconWithParts with no extra parts.
func (s *ClarificationService) StartRecon(ctx context.Context, versionID int64, actor int64) (StartRunResult, error) {
	return s.StartReconWithParts(ctx, versionID, nil, actor)
}

// StartReconWithParts starts a one-shot context-recon run whose message carries
// the instruction text part first, followed by extraParts (attachment text
// summaries and image_ref parts, spec §8.4). T13 builds the parts from the
// version's attachments and calls this method. It requires an active
// clarification session and no running agent (spec §7.5/§8.4).
func (s *ClarificationService) StartReconWithParts(ctx context.Context, versionID int64, extraParts []AgentPart, actor int64) (StartRunResult, error) {
	instruction := fmt.Sprintf(
		"Recon the context of version %d. Call version.context_get with versionId=%q first, then inspect attachments and the bound code repository, update the context summary and record questions.",
		versionID, strconv.FormatInt(versionID, 10))
	return s.startOneShot(ctx, versionID, actor, contextReconProfile,
		fmt.Sprintf("Context recon for version %d", versionID), instruction, true, extraParts)
}

// startOneShot creates a one-shot P1 agent session on the version channel and
// starts its run. Analyze sends text; recon sends the instruction as the first
// part followed by extraParts through StartParts.
func (s *ClarificationService) startOneShot(ctx context.Context, versionID, actor int64, profileKey, title, instruction string, multimodal bool, extraParts []AgentPart) (StartRunResult, error) {
	if _, err := s.ensureVersion(ctx, versionID); err != nil {
		return StartRunResult{}, err
	}
	session, err := s.repo.GetActiveSession(ctx, versionID)
	if err != nil {
		return StartRunResult{}, err
	}
	if session == nil {
		return StartRunResult{}, domain.NewStateConflict("clarification_session_required",
			fmt.Sprintf("version %d has no active clarification session", versionID))
	}
	if err := s.ensureAgentIdle(ctx, versionID); err != nil {
		return StartRunResult{}, err
	}
	if s.agents == nil {
		return StartRunResult{}, errors.New("version: agent sessions are not configured")
	}

	// AgentSessions has no delete primitive: a failure after this Create leaves an
	// orphan one-shot P1 session with no runs. It is bounded (single row) and the
	// P8 cleanup sweeps it; do not add a delete primitive for this (Lead ruling,
	// Task 10 fix round 1).
	agentSessionID, err := s.agents.Create(ctx, profileKey, title, actor,
		clarificationChannel(versionID), strconv.FormatInt(versionID, 10))
	if err != nil {
		return StartRunResult{}, err
	}
	var runID int64
	if multimodal {
		parts := make([]AgentPart, 0, 1+len(extraParts))
		parts = append(parts, AgentPart{Type: "text", Text: instruction})
		parts = append(parts, extraParts...)
		runID, err = s.agents.StartParts(ctx, agentSessionID, parts, actor)
	} else {
		runID, err = s.agents.StartText(ctx, agentSessionID, instruction, actor)
	}
	if err != nil {
		return StartRunResult{}, err
	}
	return StartRunResult{
		SessionID:      session.ID,
		AgentSessionID: agentSessionID,
		RunID:          runID,
		Status:         startRunStatusRunning,
	}, nil
}

// UpsertQuestion creates or rewrites one open question (spec §6.6/§8.3). A
// create is idempotent for the same requirement + text open question and
// returns the existing id without writing again. Only open questions may be
// rewritten; answered/dismissed questions must be reopened first.
func (s *ClarificationService) UpsertQuestion(ctx context.Context, versionID int64, input QuestionInput, actor int64, source string) (*Question, error) {
	if _, err := s.ensureVersion(ctx, versionID); err != nil {
		return nil, err
	}
	source = clarificationSourceOrDefault(source)

	if input.QuestionID != nil {
		current, err := s.loadQuestion(ctx, versionID, *input.QuestionID)
		if err != nil {
			return nil, err
		}
		// AI callers may only create/update open questions (spec §6.3); a human
		// may also fix the text of an answered or dismissed question without
		// changing its state.
		if current.Status != domain.QuestionOpen && source == domain.SourceAI {
			return nil, clarificationQuestionStateConflict(current.ID, current.Status)
		}
		fromStatus := current.Status
		candidate := *current
		candidate.Question = input.Question
		if input.RequirementID != nil {
			if err := s.ensureRequirement(ctx, versionID, *input.RequirementID); err != nil {
				return nil, err
			}
			candidate.RequirementID = input.RequirementID
		}
		if input.SessionID != nil {
			if _, err := s.loadSession(ctx, versionID, *input.SessionID); err != nil {
				return nil, err
			}
			candidate.SessionID = input.SessionID
		}
		if err := candidate.Validate(); err != nil {
			return nil, err
		}
		if err := s.repo.UpdateQuestion(ctx, &candidate, fromStatus); err != nil {
			return nil, err
		}
		s.publishQuestion(ctx, versionID, &candidate, actor, source)
		return &candidate, nil
	}

	if input.RequirementID != nil {
		if err := s.ensureRequirement(ctx, versionID, *input.RequirementID); err != nil {
			return nil, err
		}
	}
	sessionID := input.SessionID
	if sessionID != nil {
		if _, err := s.loadSession(ctx, versionID, *sessionID); err != nil {
			return nil, err
		}
	}
	if sessionID == nil && source == domain.SourceAI {
		active, err := s.repo.GetActiveSession(ctx, versionID)
		if err != nil {
			return nil, err
		}
		if active != nil {
			sessionID = &active.ID
		}
	}
	existing, err := s.findOpenQuestion(ctx, versionID, input.RequirementID, input.Question)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	id := s.ids.Next()
	if id <= 0 {
		return nil, errors.New("version: id generator returned a non-positive id")
	}
	question := &Question{
		ID:            id,
		VersionID:     versionID,
		RequirementID: input.RequirementID,
		SessionID:     sessionID,
		Question:      input.Question,
		Status:        domain.QuestionOpen,
		Source:        source,
		AnsweredVia:   domain.AnsweredViaUI,
		CreatedBy:     actor,
	}
	if err := question.Validate(); err != nil {
		return nil, err
	}
	if err := s.repo.CreateQuestion(ctx, question); err != nil {
		return nil, err
	}
	s.publishQuestion(ctx, versionID, question, actor, source)
	return question, nil
}

// AnswerQuestion records one answer: open -> answered with a required answer
// (spec §6.3/§6.6). via is ui for human answers or agent for a transcription of
// an explicit user answer.
func (s *ClarificationService) AnswerQuestion(ctx context.Context, versionID, questionID int64, answer string, actor int64, via string) (*Question, error) {
	if _, err := s.ensureVersion(ctx, versionID); err != nil {
		return nil, err
	}
	current, err := s.loadQuestion(ctx, versionID, questionID)
	if err != nil {
		return nil, err
	}
	if current.Status != domain.QuestionOpen {
		return nil, clarificationQuestionStateConflict(questionID, current.Status)
	}
	if strings.TrimSpace(answer) == "" {
		return nil, domain.NewInvalid("answer", "invalid_answer", "answer is required")
	}
	via = clarificationAnsweredViaOrDefault(via)
	candidate := *current
	candidate.Status = domain.QuestionAnswered
	candidate.Answer = answer
	candidate.AnsweredVia = via
	candidate.AnsweredBy = clarificationIDPtr(actor)
	candidate.AnsweredAt = clarificationNow()
	if err := candidate.Validate(); err != nil {
		return nil, err
	}
	if err := s.repo.UpdateQuestion(ctx, &candidate, domain.QuestionOpen); err != nil {
		return nil, err
	}
	source := domain.SourceUser
	if via == domain.AnsweredViaAgent {
		source = domain.SourceAI
	}
	s.publishQuestion(ctx, versionID, &candidate, actor, source)
	return &candidate, nil
}

// DismissQuestion dismisses one open question (spec §6.3).
func (s *ClarificationService) DismissQuestion(ctx context.Context, versionID, questionID int64, actor int64) (*Question, error) {
	if _, err := s.ensureVersion(ctx, versionID); err != nil {
		return nil, err
	}
	current, err := s.loadQuestion(ctx, versionID, questionID)
	if err != nil {
		return nil, err
	}
	if current.Status != domain.QuestionOpen {
		return nil, clarificationQuestionStateConflict(questionID, current.Status)
	}
	candidate := *current
	candidate.Status = domain.QuestionDismissed
	candidate.Answer = ""
	candidate.AnsweredBy = nil
	candidate.AnsweredAt = nil
	if err := candidate.Validate(); err != nil {
		return nil, err
	}
	if err := s.repo.UpdateQuestion(ctx, &candidate, domain.QuestionOpen); err != nil {
		return nil, err
	}
	s.publishQuestion(ctx, versionID, &candidate, actor, domain.SourceUser)
	return &candidate, nil
}

// ReopenQuestion reopens an answered or dismissed question, clearing the answer
// and its audit fields (spec §6.3/§7.6).
func (s *ClarificationService) ReopenQuestion(ctx context.Context, versionID, questionID int64, actor int64) (*Question, error) {
	if _, err := s.ensureVersion(ctx, versionID); err != nil {
		return nil, err
	}
	current, err := s.loadQuestion(ctx, versionID, questionID)
	if err != nil {
		return nil, err
	}
	if current.Status != domain.QuestionAnswered && current.Status != domain.QuestionDismissed {
		return nil, clarificationQuestionStateConflict(questionID, current.Status)
	}
	fromStatus := current.Status
	candidate := *current
	candidate.Status = domain.QuestionOpen
	candidate.Answer = ""
	candidate.AnsweredBy = nil
	candidate.AnsweredAt = nil
	if err := candidate.Validate(); err != nil {
		return nil, err
	}
	if err := s.repo.UpdateQuestion(ctx, &candidate, fromStatus); err != nil {
		return nil, err
	}
	s.publishQuestion(ctx, versionID, &candidate, actor, domain.SourceUser)
	return &candidate, nil
}

// CreateDecision records one decision. AI-sourced decisions are proposed and
// wait for a human; user-sourced decisions are accepted immediately. Accepting a
// decision linked to an open question answers that question in the same
// repository transaction (spec §6.6/§7.6).
func (s *ClarificationService) CreateDecision(ctx context.Context, versionID int64, input DecisionInput, actor int64, source string) (*Decision, error) {
	if _, err := s.ensureVersion(ctx, versionID); err != nil {
		return nil, err
	}
	source = clarificationSourceOrDefault(source)
	if input.RequirementID != nil {
		if err := s.ensureRequirement(ctx, versionID, *input.RequirementID); err != nil {
			return nil, err
		}
	}
	var linkedQuestion *Question
	if input.QuestionID != nil {
		question, err := s.loadQuestion(ctx, versionID, *input.QuestionID)
		if err != nil {
			return nil, err
		}
		linkedQuestion = question
	}
	id := s.ids.Next()
	if id <= 0 {
		return nil, errors.New("version: id generator returned a non-positive id")
	}
	decision := &Decision{
		ID:            id,
		VersionID:     versionID,
		RequirementID: input.RequirementID,
		QuestionID:    input.QuestionID,
		Topic:         input.Topic,
		Decision:      input.Decision,
		Rationale:     input.Rationale,
		Source:        source,
		ProposedBy:    actor,
	}
	if source == domain.SourceAI {
		decision.Status = domain.DecisionProposed
	} else {
		decision.Status = domain.DecisionAccepted
		decision.DecidedBy = clarificationIDPtr(actor)
		decision.DecidedAt = clarificationNow()
	}
	if err := decision.Validate(); err != nil {
		return nil, err
	}
	if err := s.repo.CreateDecision(ctx, decision); err != nil {
		return nil, err
	}

	if decision.Status == domain.DecisionProposed {
		s.publishDecision(ctx, versionID, decision, "decision.proposed", actor, source)
	} else {
		s.publishDecision(ctx, versionID, decision, "decision.decided", actor, source)
		s.publishAnsweredLinkedQuestion(ctx, versionID, linkedQuestion, decision, actor, source)
	}
	return decision, nil
}

// Decide accepts or rejects one proposed decision. Both transitions are
// terminal; accepting a decision linked to an open question answers that
// question in the same repository transaction (spec §6.6). An optional rationale
// replaces the proposal's rationale (rejecting may record one, spec §7.6).
func (s *ClarificationService) Decide(ctx context.Context, versionID, decisionID int64, accept bool, actor int64, rationale ...string) (*Decision, error) {
	if _, err := s.ensureVersion(ctx, versionID); err != nil {
		return nil, err
	}
	current, err := s.loadDecision(ctx, versionID, decisionID)
	if err != nil {
		return nil, err
	}
	if current.Status != domain.DecisionProposed {
		return nil, decisionStateConflict(decisionID, current.Status)
	}
	var linkedQuestion *Question
	if current.QuestionID != nil {
		question, err := s.loadQuestion(ctx, versionID, *current.QuestionID)
		if err != nil {
			return nil, err
		}
		linkedQuestion = question
	}

	candidate := *current
	candidate.DecidedBy = clarificationIDPtr(actor)
	candidate.DecidedAt = clarificationNow()
	if len(rationale) > 0 {
		candidate.Rationale = rationale[0]
	}
	if accept {
		candidate.Status = domain.DecisionAccepted
	} else {
		candidate.Status = domain.DecisionRejected
	}
	if err := candidate.Validate(); err != nil {
		return nil, err
	}
	if err := s.repo.UpdateDecision(ctx, &candidate, domain.DecisionProposed); err != nil {
		return nil, err
	}
	s.publishDecision(ctx, versionID, &candidate, "decision.decided", actor, domain.SourceUser)
	if candidate.Status == domain.DecisionAccepted {
		s.publishAnsweredLinkedQuestion(ctx, versionID, linkedQuestion, &candidate, actor, domain.SourceUser)
	}
	return &candidate, nil
}

// publishAnsweredLinkedQuestion publishes clarification.question_updated for the
// question linked to an accepted decision only when the repository transaction
// actually moved it from open to answered. The repository answers a linked
// question only while it is still open, so a concurrent dismissal (or answer)
// makes its update a no-op; publishing the stale pre-read snapshot would then be
// a phantom event. The post-commit re-read is best-effort like the event itself:
// a failure is logged and swallowed because the decision write already committed.
func (s *ClarificationService) publishAnsweredLinkedQuestion(ctx context.Context, versionID int64, linked *Question, decision *Decision, actor int64, source string) {
	if linked == nil || linked.Status != domain.QuestionOpen {
		return
	}
	answered, err := s.loadQuestion(ctx, versionID, linked.ID)
	if err != nil {
		slog.WarnContext(ctx, "clarification decision linked question re-read failed",
			"question_id", linked.ID,
			"version_id", versionID,
			"error", err,
		)
		return
	}
	if answered.Status != domain.QuestionAnswered {
		return
	}
	s.publishQuestion(ctx, versionID, answered, actor, source)
}

// CurrentSession returns the active session of the version, or nil when none is
// active. A missing version is a 404.
func (s *ClarificationService) CurrentSession(ctx context.Context, versionID int64) (*ClarificationSession, error) {
	if _, err := s.ensureVersion(ctx, versionID); err != nil {
		return nil, err
	}
	return s.repo.GetActiveSession(ctx, versionID)
}

// LatestSession returns the version's newest session regardless of status (the
// completed-session message history reads it after completion) or nil when the
// version has no session at all. A missing version is a 404.
func (s *ClarificationService) LatestSession(ctx context.Context, versionID int64) (*ClarificationSession, error) {
	if _, err := s.ensureVersion(ctx, versionID); err != nil {
		return nil, err
	}
	return s.repo.LatestSession(ctx, versionID)
}

// UpdateContextSummary writes the active session's context summary. It is the
// service entry point of the agent's context.summary_update tool (spec §8.3):
// the summary is bounded to 20 KiB and the successful write publishes
// clarification.context_updated after the repository call returns.
func (s *ClarificationService) UpdateContextSummary(ctx context.Context, versionID int64, summary string, actor int64) error {
	if _, err := s.ensureVersion(ctx, versionID); err != nil {
		return err
	}
	if len(summary) > MaxClarificationContextSummaryBytes {
		return domain.NewInvalid("summary", "invalid_context_summary",
			fmt.Sprintf("context summary must be at most %d bytes", MaxClarificationContextSummaryBytes))
	}
	if err := s.repo.UpdateContextSummary(ctx, versionID, summary, actor); err != nil {
		return err
	}
	s.publish(ctx, VersionEvent{
		VersionID: versionID,
		Type:      "clarification.context_updated",
		Summary:   "clarification context updated",
		ActorID:   actor,
		Source:    domain.SourceAI,
		Detail: map[string]any{
			"versionId": eventID(versionID),
			"tool":      "context.summary_update",
			"actorId":   actor,
		},
	})
	return nil
}

// ListQuestions returns one page of a version's questions ordered by id
// ascending. status is optional; a nil requirementID disables the requirement
// filter.
func (s *ClarificationService) ListQuestions(ctx context.Context, versionID int64, status string, requirementID *int64, page domain.Page) ([]Question, bool, error) {
	if _, err := s.ensureVersion(ctx, versionID); err != nil {
		return nil, false, err
	}
	return s.repo.ListQuestions(ctx, QuestionFilter{
		VersionID: versionID, Status: status, RequirementID: requirementID, Page: page,
	})
}

// ListDecisions returns one page of a version's decisions ordered by id
// ascending. status is optional.
func (s *ClarificationService) ListDecisions(ctx context.Context, versionID int64, status string, page domain.Page) ([]Decision, bool, error) {
	if _, err := s.ensureVersion(ctx, versionID); err != nil {
		return nil, false, err
	}
	return s.repo.ListDecisions(ctx, DecisionFilter{VersionID: versionID, Status: status, Page: page})
}

// Counts returns the per-status question and decision counters of the
// clarification overview (spec §7.5). It walks the keyset pages because the
// repository port exposes no aggregate count.
func (s *ClarificationService) Counts(ctx context.Context, versionID int64) (ClarificationCounts, error) {
	if _, err := s.ensureVersion(ctx, versionID); err != nil {
		return ClarificationCounts{}, err
	}
	var counts ClarificationCounts
	for _, status := range []string{domain.QuestionOpen, domain.QuestionAnswered, domain.QuestionDismissed} {
		count, err := s.countQuestions(ctx, versionID, status)
		if err != nil {
			return ClarificationCounts{}, err
		}
		switch status {
		case domain.QuestionOpen:
			counts.OpenQuestions = count
		case domain.QuestionAnswered:
			counts.AnsweredQuestions = count
		case domain.QuestionDismissed:
			counts.DismissedQuestions = count
		}
	}
	for _, status := range []string{domain.DecisionProposed, domain.DecisionAccepted, domain.DecisionRejected} {
		count, err := s.countDecisions(ctx, versionID, status)
		if err != nil {
			return ClarificationCounts{}, err
		}
		switch status {
		case domain.DecisionProposed:
			counts.ProposedDecisions = count
		case domain.DecisionAccepted:
			counts.AcceptedDecisions = count
		case domain.DecisionRejected:
			counts.RejectedDecisions = count
		}
	}
	return counts, nil
}

// countQuestions pages through the version's questions of one status.
func (s *ClarificationService) countQuestions(ctx context.Context, versionID int64, status string) (int, error) {
	count := 0
	cursor := ""
	for {
		page, more, err := s.repo.ListQuestions(ctx, QuestionFilter{
			VersionID: versionID, Status: status,
			Page: domain.Page{Limit: clarificationCountPageSize, Cursor: cursor},
		})
		if err != nil {
			return 0, err
		}
		count += len(page)
		if !more || len(page) == 0 {
			return count, nil
		}
		cursor = platform.EncodeCursor(page[len(page)-1].ID)
	}
}

// countDecisions pages through the version's decisions of one status.
func (s *ClarificationService) countDecisions(ctx context.Context, versionID int64, status string) (int, error) {
	count := 0
	cursor := ""
	for {
		page, more, err := s.repo.ListDecisions(ctx, DecisionFilter{
			VersionID: versionID, Status: status,
			Page: domain.Page{Limit: clarificationCountPageSize, Cursor: cursor},
		})
		if err != nil {
			return 0, err
		}
		count += len(page)
		if !more || len(page) == 0 {
			return count, nil
		}
		cursor = platform.EncodeCursor(page[len(page)-1].ID)
	}
}

// findOpenQuestion walks the version's open questions looking for an exact
// requirement + text match (the question_upsert idempotency rule of spec §6.6).
func (s *ClarificationService) findOpenQuestion(ctx context.Context, versionID int64, requirementID *int64, text string) (*Question, error) {
	cursor := ""
	for {
		page, more, err := s.repo.ListQuestions(ctx, QuestionFilter{
			VersionID: versionID, Status: domain.QuestionOpen,
			Page: domain.Page{Limit: clarificationCountPageSize, Cursor: cursor},
		})
		if err != nil {
			return nil, err
		}
		for i := range page {
			if page[i].Question == text && sameOptionalID(page[i].RequirementID, requirementID) {
				return &page[i], nil
			}
		}
		if !more || len(page) == 0 {
			return nil, nil
		}
		cursor = platform.EncodeCursor(page[len(page)-1].ID)
	}
}

// ensureVersion loads the version and reports a missing one as a 404.
func (s *ClarificationService) ensureVersion(ctx context.Context, versionID int64) (*Version, error) {
	if s.versions == nil {
		return nil, errors.New("version: version repository is not configured")
	}
	return s.versions.Get(ctx, versionID)
}

// ensureRequirement verifies that requirementID exists and belongs to
// versionID. A cross-version or missing requirement is indistinguishable from a
// missing one (404).
func (s *ClarificationService) ensureRequirement(ctx context.Context, versionID, requirementID int64) error {
	if s.requirements == nil {
		return errors.New("version: requirement repository is not configured")
	}
	requirement, err := s.requirements.Get(ctx, requirementID)
	if err != nil {
		return err
	}
	if requirement.VersionID != versionID {
		return domain.NewNotFound("requirement", requirementID)
	}
	return nil
}

// loadSession returns one session scoped to versionID.
func (s *ClarificationService) loadSession(ctx context.Context, versionID, sessionID int64) (*ClarificationSession, error) {
	session, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if session.VersionID != versionID {
		return nil, domain.NewNotFound("clarification session", sessionID)
	}
	return session, nil
}

// loadQuestion returns one question scoped to versionID.
func (s *ClarificationService) loadQuestion(ctx context.Context, versionID, questionID int64) (*Question, error) {
	question, err := s.repo.GetQuestion(ctx, questionID)
	if err != nil {
		return nil, err
	}
	if question.VersionID != versionID {
		return nil, domain.NewNotFound("question", questionID)
	}
	return question, nil
}

// loadDecision returns one decision scoped to versionID.
func (s *ClarificationService) loadDecision(ctx context.Context, versionID, decisionID int64) (*Decision, error) {
	decision, err := s.repo.GetDecision(ctx, decisionID)
	if err != nil {
		return nil, err
	}
	if decision.VersionID != versionID {
		return nil, domain.NewNotFound("decision", decisionID)
	}
	return decision, nil
}

// ensureAgentIdle rejects a write that would start a second agent run on the
// version channel (INV-6; the P1 adapter's HasRunningByChannel is the check).
func (s *ClarificationService) ensureAgentIdle(ctx context.Context, versionID int64) error {
	if s.agents == nil {
		return errors.New("version: agent sessions are not configured")
	}
	running, err := s.agents.HasRunningByChannel(ctx, clarificationChannel(versionID))
	if err != nil {
		return err
	}
	if running {
		return domain.NewStateConflict("agent_busy",
			fmt.Sprintf("version %d already has a running agent", versionID))
	}
	return nil
}

// publishQuestion emits clarification.question_updated after a committed
// question write.
func (s *ClarificationService) publishQuestion(ctx context.Context, versionID int64, question *Question, actor int64, source string) {
	event := VersionEvent{
		VersionID:    versionID,
		Type:         "clarification.question_updated",
		Summary:      "clarification question updated",
		RefSessionID: question.SessionID,
		ActorID:      actor,
		Source:       source,
		Detail: map[string]any{
			"questionId": eventID(question.ID),
			"status":     question.Status,
		},
	}
	s.publish(ctx, event)
}

// publishDecision emits decision.proposed/decision.decided after a committed
// decision write.
func (s *ClarificationService) publishDecision(ctx context.Context, versionID int64, decision *Decision, eventType string, actor int64, source string) {
	s.publish(ctx, VersionEvent{
		VersionID:     versionID,
		Type:          eventType,
		Summary:       strings.ReplaceAll(eventType, ".", " ") + " event",
		RefDecisionID: clarificationIDPtr(decision.ID),
		ActorID:       actor,
		Source:        source,
		Detail: map[string]any{
			"decisionId": eventID(decision.ID),
			"status":     decision.Status,
			"source":     source,
		},
	})
}

// publish emits one event after a committed write. Publication failures are
// logged and swallowed (spec §6.10).
func (s *ClarificationService) publish(ctx context.Context, event VersionEvent) {
	if s.events == nil {
		return
	}
	if err := s.events.Publish(ctx, event); err != nil {
		slog.WarnContext(ctx, "clarification event publish failed",
			"type", event.Type,
			"version_id", event.VersionID,
			"error", err,
		)
	}
}

// clarificationChannel is the P1 activity channel every P3 run uses.
func clarificationChannel(versionID int64) string {
	return fmt.Sprintf("version:%d", versionID)
}

// clarificationSourceOrDefault maps an empty caller source to user. Invalid
// values are kept so domain validation rejects them.
func clarificationSourceOrDefault(source string) string {
	if source == "" {
		return domain.SourceUser
	}
	return source
}

// clarificationAnsweredViaOrDefault maps an empty via to ui.
func clarificationAnsweredViaOrDefault(via string) string {
	if via == "" {
		return domain.AnsweredViaUI
	}
	return via
}

// clarificationNow is the wall-clock value stamped on human transitions. The
// repository also stamps server time; the two agree to within a round trip.
func clarificationNow() *time.Time {
	now := time.Now().UTC()
	return &now
}

// validateClarificationMessage enforces the 1–5000 message bounds of spec §7.5.
func validateClarificationMessage(message string) error {
	if strings.TrimSpace(message) == "" {
		return domain.NewInvalid("message", "invalid_message", "message is required")
	}
	if utf8.RuneCountInString(message) > MaxClarificationMessageChars {
		return domain.NewInvalid("message", "invalid_message",
			fmt.Sprintf("message must be at most %d characters", MaxClarificationMessageChars))
	}
	return nil
}

// clarificationQuestionStateConflict builds the spec §7.1
// clarification_question_state_conflict 409.
func clarificationQuestionStateConflict(id int64, status string) error {
	return domain.NewStateConflict("clarification_question_state_conflict",
		fmt.Sprintf("question %d is %s, which does not allow this transition", id, status))
}

// decisionStateConflict builds the spec §7.1 decision_state_conflict 409.
func decisionStateConflict(id int64, status string) error {
	return domain.NewStateConflict("decision_state_conflict",
		fmt.Sprintf("decision %d is %s, which does not allow this transition", id, status))
}

// eventID renders a P3-owned id the way event details carry it: decimal string
// (spec §7.1 ID serialization).
func eventID(id int64) string {
	return strconv.FormatInt(id, 10)
}

// clarificationIDPtr returns a pointer to id for an event reference.
func clarificationIDPtr(id int64) *int64 { return &id }

// sameOptionalID compares two optional ids.
func sameOptionalID(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
