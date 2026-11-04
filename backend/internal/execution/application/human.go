package executionapp

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// HumanService implements the P3 login-takeover pause/resume flow (P3 §6.8,
// P3-D-17): the agent calls human.request, the case_run and wave pause, a user
// resolves with a one-time response, and the scheduler redispatch resumes the
// case from the first unfinished step.
type HumanService struct {
	humans    HumanRequestRepository
	caseRuns  CaseRunRepository
	waves     WaveRepository
	canceller RunCanceller
	workQueue WorkQueue
	settings  SettingsReader
	ids       IDGenerator
	clock     Clock
	events    EventPublisher
	audit     AuditRecorder
	timeout   time.Duration
}

// HumanServiceDeps wires the human takeover service. timeout overrides the
// execution.human_request_timeout_seconds setting when positive.
type HumanServiceDeps struct {
	Humans    HumanRequestRepository
	CaseRuns  CaseRunRepository
	Waves     WaveRepository
	Canceller RunCanceller
	WorkQueue WorkQueue
	Settings  SettingsReader
	IDs       IDGenerator
	Clock     Clock
	Events    EventPublisher
	Audit     AuditRecorder
	Timeout   time.Duration
}

// NewHumanService builds the human takeover service.
func NewHumanService(deps HumanServiceDeps) *HumanService {
	return &HumanService{
		humans:    deps.Humans,
		caseRuns:  deps.CaseRuns,
		waves:     deps.Waves,
		canceller: deps.Canceller,
		workQueue: deps.WorkQueue,
		settings:  deps.Settings,
		ids:       deps.IDs,
		clock:     deps.Clock,
		events:    deps.Events,
		audit:     deps.Audit,
		timeout:   deps.Timeout,
	}
}

// HumanCreateInput is the application input of one human request.
type HumanCreateInput struct {
	VersionID     int64
	CaseRunID     int64
	WaveID        int64
	DispatchEpoch int64
	DispatchToken string
	Kind          string
	Reason        string
	VersionSUTID  *int64
	AccountRole   string
	ChallengeKind string
	ActorID       int64
}

// CreateFromTool implements the CaseRunner HumanRequestService port.
func (s *HumanService) CreateFromTool(ctx context.Context, scope tools.Scope, in tools.HumanRequestInput) (int64, error) {
	request, err := s.Create(ctx, HumanCreateInput{
		VersionID:     scope.VersionID,
		CaseRunID:     scope.CaseRunID,
		WaveID:        scope.WaveID,
		DispatchEpoch: scope.DispatchEpoch,
		DispatchToken: scope.DispatchToken,
		Kind:          in.Kind,
		Reason:        in.Reason,
		VersionSUTID:  in.VersionSUTID,
		AccountRole:   in.AccountRole,
		ChallengeKind: in.ChallengeKind,
	})
	if err != nil {
		return 0, err
	}
	return request.ID, nil
}

// Create opens one login_takeover request, pauses the case_run and the wave and
// cancels the in-flight agent run. The case_run keeps its finished results so
// the resumed run continues from the first unfinished step.
func (s *HumanService) Create(ctx context.Context, in HumanCreateInput) (execution.HumanRequest, error) {
	if err := requirePositiveID("case_run_id", in.CaseRunID); err != nil {
		return execution.HumanRequest{}, err
	}
	kind := execution.HumanKind(strings.TrimSpace(in.Kind))
	if kind == "" {
		kind = execution.HumanLoginTakeover
	}
	if kind != execution.HumanLoginTakeover {
		return execution.HumanRequest{}, platform.Invalid("kind", "P3 only creates login_takeover requests")
	}
	run, err := s.caseRuns.Get(ctx, in.CaseRunID)
	if err != nil {
		return execution.HumanRequest{}, mapExecutionError(err, "case run")
	}
	if in.WaveID > 0 && run.WaveID != in.WaveID {
		return execution.HumanRequest{}, platform.NotFound("case run")
	}
	if run.Status.Terminal() || run.Status == execution.CaseRunInterrupted {
		return execution.HumanRequest{}, platform.StateConflict("the case run is already finished")
	}
	if open, err := s.humans.GetOpenByCaseRun(ctx, run.ID); err != nil {
		return execution.HumanRequest{}, mapExecutionError(err, "human_request")
	} else if open != nil {
		return execution.HumanRequest{}, platform.StateConflict("a human request is already open for this case run")
	}
	count, err := s.humans.CountByCaseRun(ctx, run.ID)
	if err != nil {
		return execution.HumanRequest{}, mapExecutionError(err, "human_request")
	}
	if count >= execution.MaxHumanRequestsPerCase {
		return execution.HumanRequest{}, platform.StateConflict("the case run reached the human takeover limit")
	}
	wave, err := s.waves.Get(ctx, run.WaveID)
	if err != nil {
		return execution.HumanRequest{}, mapExecutionError(err, "wave")
	}
	now := s.now()
	epoch := in.DispatchEpoch
	token := in.DispatchToken
	if epoch <= 0 {
		epoch = run.DispatchEpoch
	}
	if token == "" {
		token = run.DispatchToken
	}
	payload := map[string]any{
		execution.HumanPayloadReason:        strings.TrimSpace(in.Reason),
		execution.HumanPayloadAccountRole:   strings.TrimSpace(in.AccountRole),
		execution.HumanPayloadChallengeKind: strings.TrimSpace(in.ChallengeKind),
	}
	if in.VersionSUTID != nil && *in.VersionSUTID > 0 {
		payload[execution.HumanPayloadVersionSUTID] = platformid.Format(*in.VersionSUTID)
	}
	request := execution.HumanRequest{
		ID:        s.ids.Next(),
		VersionID: wave.VersionID,
		CaseRunID: int64PtrValue(run.ID),
		Kind:      kind,
		Payload:   payload,
		Status:    execution.HumanPending,
		ExpiresAt: now.Add(s.requestTimeout(ctx)),
		CreatedBy: actorPtr(in.ActorID),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.humans.Create(ctx, request); err != nil {
		return execution.HumanRequest{}, mapExecutionError(err, "human_request")
	}

	// Pause the case_run under the dispatch fence so a stale run cannot pause a
	// newer dispatch.
	if ok, err := s.caseRuns.UpdateCAS(ctx, run.ID, epoch, token, func(run *execution.CaseRun) error {
		run.Status = execution.CaseRunPaused
		run.PausedAt = &now
		run.PausedBy = actorPtr(in.ActorID)
		run.PauseReason = execution.PauseReasonWaitingHuman
		run.Summary.HumanRequestID = request.ID
		run.UpdatedAt = now
		return nil
	}); err != nil {
		return execution.HumanRequest{}, mapExecutionError(err, "case run")
	} else if !ok {
		return execution.HumanRequest{}, platform.LeaseLost()
	}
	publishEvent(ctx, s.events, EventCaseRunPaused, wave.VersionID,
		map[string]any{"wave_id": platformid.Format(wave.ID), "case_run_id": platformid.Format(run.ID)},
		map[string]any{"case_run_id": platformid.Format(run.ID), "reason": execution.PauseReasonWaitingHuman},
		actorPtr(in.ActorID))

	if wave.Status.Active() && wave.Status != execution.WavePaused {
		if changed, err := s.waves.Transition(ctx, wave.ID,
			[]execution.WaveStatus{execution.WavePlanned, execution.WaveRunning},
			execution.WavePaused, execution.RunWave{
				PausedAt:    &now,
				PausedBy:    in.ActorID,
				PauseMode:   execution.PauseModeGraceful,
				PauseReason: execution.PauseReasonWaitingHuman,
			}); err != nil {
			return execution.HumanRequest{}, mapExecutionError(err, "wave")
		} else if changed {
			publishEvent(ctx, s.events, EventWavePaused, wave.VersionID,
				map[string]any{"wave_id": platformid.Format(wave.ID)},
				map[string]any{
					"wave_id": platformid.Format(wave.ID),
					"mode":    execution.PauseModeGraceful,
					"reason":  execution.PauseReasonWaitingHuman,
				}, actorPtr(in.ActorID))
		}
	}
	// Persist the human_request.created event and audit before cancelling the
	// in-flight agent run: the tool call shares the run context, so a publish
	// after CancelRun would be silently dropped (S8 real-LLM acceptance).
	publishEvent(ctx, s.events, EventHumanCreated, wave.VersionID,
		map[string]any{
			"human_request_id": platformid.Format(request.ID),
			"case_run_id":      platformid.Format(run.ID),
		},
		map[string]any{
			"human_request_id": platformid.Format(request.ID),
			"case_run_id":      platformid.Format(run.ID),
			"kind":             string(kind),
			"expires_at":       eventTime(request.ExpiresAt),
		}, actorPtr(in.ActorID))
	s.record(ctx, "human_request.created", request, in.ActorID)
	if s.canceller != nil {
		s.canceller.CancelRun(run.ID)
	}
	return request, nil
}

// Resolve records the one-time response and resumes the paused case_run/wave.
func (s *HumanService) Resolve(ctx context.Context, humanRequestID, actorID int64, response map[string]any) (execution.HumanRequest, error) {
	if err := requirePositiveID("human_request_id", humanRequestID); err != nil {
		return execution.HumanRequest{}, err
	}
	request, err := s.humans.Get(ctx, humanRequestID)
	if err != nil {
		return execution.HumanRequest{}, mapExecutionError(err, "human_request")
	}
	if request.Status != execution.HumanPending {
		return execution.HumanRequest{}, platform.StateConflict("the human request is not pending")
	}
	normalized, err := normalizeHumanResponse(response)
	if err != nil {
		return execution.HumanRequest{}, err
	}
	now := s.now()
	changed, err := s.humans.Transition(ctx, humanRequestID, execution.HumanPending, execution.HumanAnswered, normalized, actorID, now, nil)
	if err != nil {
		return execution.HumanRequest{}, mapExecutionError(err, "human_request")
	}
	if !changed {
		return execution.HumanRequest{}, platform.StateConflict("the human request is not pending")
	}
	if request.CaseRunID != nil {
		if err := s.resumeCaseRun(ctx, *request.CaseRunID, actorID); err != nil {
			return execution.HumanRequest{}, err
		}
	}
	request, err = s.humans.Get(ctx, humanRequestID)
	if err != nil {
		return execution.HumanRequest{}, mapExecutionError(err, "human_request")
	}
	publishEvent(ctx, s.events, EventHumanResolved, request.VersionID,
		map[string]any{
			"human_request_id": platformid.Format(request.ID),
			"case_run_id":      platformid.Format(derefID(request.CaseRunID)),
		},
		map[string]any{
			"human_request_id": platformid.Format(request.ID),
			"case_run_id":      platformid.Format(derefID(request.CaseRunID)),
			"status":           string(request.Status),
		}, actorPtr(actorID))
	s.record(ctx, "human_request.resolved", request, actorID)
	return request, nil
}

// Cancel abandons an open request: the case_run fails with failure_class=session
// and the wave continues (§4.7).
func (s *HumanService) Cancel(ctx context.Context, humanRequestID, actorID int64) error {
	if err := requirePositiveID("human_request_id", humanRequestID); err != nil {
		return err
	}
	request, err := s.humans.Get(ctx, humanRequestID)
	if err != nil {
		return mapExecutionError(err, "human_request")
	}
	if request.Status == execution.HumanCancelled {
		return nil
	}
	if request.Status != execution.HumanPending {
		return platform.StateConflict("the human request is not pending")
	}
	now := s.now()
	changed, err := s.humans.Transition(ctx, humanRequestID, execution.HumanPending, execution.HumanCancelled, nil, actorID, now, nil)
	if err != nil {
		return mapExecutionError(err, "human_request")
	}
	if !changed {
		return platform.StateConflict("the human request is not pending")
	}
	if request.CaseRunID != nil {
		if err := s.failCaseRun(ctx, *request.CaseRunID, execution.VerdictReasonHumanCancelled); err != nil {
			return err
		}
		if err := s.resumeWaveAfterHuman(ctx, *request.CaseRunID, actorID); err != nil {
			return err
		}
	}
	publishEvent(ctx, s.events, EventHumanResolved, request.VersionID,
		map[string]any{
			"human_request_id": platformid.Format(request.ID),
			"case_run_id":      platformid.Format(derefID(request.CaseRunID)),
		},
		map[string]any{
			"human_request_id": platformid.Format(request.ID),
			"case_run_id":      platformid.Format(derefID(request.CaseRunID)),
			"status":           string(execution.HumanCancelled),
		}, actorPtr(actorID))
	s.record(ctx, "human_request.cancelled", request, actorID)
	return nil
}

// Expire fails every overdue open request and continues the wave (§4.7).
func (s *HumanService) Expire(ctx context.Context, now time.Time) (int, error) {
	expired, err := s.humans.Expire(ctx, now)
	if err != nil {
		return 0, mapExecutionError(err, "human_request")
	}
	for _, request := range expired {
		if request.CaseRunID != nil {
			if err := s.failCaseRun(ctx, *request.CaseRunID, execution.VerdictReasonHumanTimeout); err != nil {
				return 0, err
			}
			if err := s.resumeWaveAfterHuman(ctx, *request.CaseRunID, 0); err != nil {
				return 0, err
			}
		}
		publishEvent(ctx, s.events, EventHumanExpired, request.VersionID,
			map[string]any{
				"human_request_id": platformid.Format(request.ID),
				"case_run_id":      platformid.Format(derefID(request.CaseRunID)),
			},
			map[string]any{
				"human_request_id": platformid.Format(request.ID),
				"case_run_id":      platformid.Format(derefID(request.CaseRunID)),
			}, nil)
		s.record(ctx, "human_request.expired", request, 0)
	}
	return len(expired), nil
}

// resumeCaseRun moves a human-paused case_run back to pending with a new
// dispatch epoch so the scheduler creates a fresh execute.case work item.
func (s *HumanService) resumeCaseRun(ctx context.Context, caseRunID, actorID int64) error {
	run, err := s.caseRuns.Get(ctx, caseRunID)
	if err != nil {
		if err == execution.ErrNotFound {
			return nil
		}
		return mapExecutionError(err, "case run")
	}
	if run.Status != execution.CaseRunPaused {
		return nil
	}
	now := s.now()
	run.Status = execution.CaseRunPending
	run.DispatchEpoch++
	run.DispatchToken = ""
	run.LeaseUntil = nil
	run.OwnerExecutorID = nil
	run.PausedAt = nil
	run.PausedBy = nil
	run.PauseReason = ""
	run.UpdatedAt = now
	if _, err := s.caseRuns.Update(ctx, run); err != nil {
		return mapExecutionError(err, "case run")
	}
	// The SSE channel is always version:{version_id}; run.WaveID is not a
	// version id (S8 acceptance caught the bogus version:{wave_id} channel).
	versionID, err := s.caseRunVersionID(ctx, run)
	if err != nil {
		return err
	}
	publishEvent(ctx, s.events, EventCaseRunResumed, versionID,
		map[string]any{"case_run_id": platformid.Format(run.ID)},
		map[string]any{"case_run_id": platformid.Format(run.ID)}, actorPtr(actorID))
	return s.resumeWaveAfterHuman(ctx, caseRunID, actorID)
}

// caseRunVersionID resolves the owning version for a case_run. RunWave is the
// only bridge because case_run deliberately stores no cross-context version id.
func (s *HumanService) caseRunVersionID(ctx context.Context, run execution.CaseRun) (int64, error) {
	wave, err := s.waves.Get(ctx, run.WaveID)
	if err != nil {
		return 0, mapExecutionError(err, "wave")
	}
	return wave.VersionID, nil
}

// resumeWaveAfterHuman resumes a waiting_human wave once it has no other open
// request. A user-initiated pause is never auto-resumed.
func (s *HumanService) resumeWaveAfterHuman(ctx context.Context, caseRunID, actorID int64) error {
	run, err := s.caseRuns.Get(ctx, caseRunID)
	if err != nil {
		if err == execution.ErrNotFound {
			return nil
		}
		return mapExecutionError(err, "case run")
	}
	wave, err := s.waves.Get(ctx, run.WaveID)
	if err != nil {
		return mapExecutionError(err, "wave")
	}
	if wave.Status != execution.WavePaused || wave.PauseReason != execution.PauseReasonWaitingHuman {
		return nil
	}
	runs, err := s.caseRuns.ListAllByWave(ctx, wave.ID)
	if err != nil {
		return mapExecutionError(err, "case run")
	}
	for _, item := range runs {
		open, err := s.humans.GetOpenByCaseRun(ctx, item.ID)
		if err != nil {
			return mapExecutionError(err, "human_request")
		}
		if open != nil {
			return nil
		}
	}
	now := s.now()
	changed, err := s.waves.Transition(ctx, wave.ID,
		[]execution.WaveStatus{execution.WavePaused}, execution.WaveRunning,
		execution.RunWave{PausedAt: &now})
	if err != nil {
		return mapExecutionError(err, "wave")
	}
	if changed {
		publishEvent(ctx, s.events, EventWaveResumed, wave.VersionID,
			map[string]any{"wave_id": platformid.Format(wave.ID)},
			map[string]any{"wave_id": platformid.Format(wave.ID)}, actorPtr(actorID))
	}
	return nil
}

// failCaseRun writes the human-timeout/cancel failure of a paused case_run.
func (s *HumanService) failCaseRun(ctx context.Context, caseRunID int64, reason string) error {
	run, err := s.caseRuns.Get(ctx, caseRunID)
	if err != nil {
		if err == execution.ErrNotFound {
			return nil
		}
		return mapExecutionError(err, "case run")
	}
	if run.Status.Terminal() {
		return nil
	}
	now := s.now()
	run.Status = execution.CaseRunFailed
	run.FailureClass = execution.FailureClassSession
	run.Summary.VerdictReason = reason
	run.Summary.LastError = reason
	run.FinishedAt = &now
	run.PausedAt = nil
	run.PausedBy = nil
	run.PauseReason = ""
	run.UpdatedAt = now
	if _, err := s.caseRuns.Update(ctx, run); err != nil {
		return mapExecutionError(err, "case run")
	}
	versionID, err := s.caseRunVersionID(ctx, run)
	if err != nil {
		return err
	}
	publishEvent(ctx, s.events, EventCaseRunFinished, versionID,
		map[string]any{"case_run_id": platformid.Format(run.ID)},
		map[string]any{
			"case_run_id":   platformid.Format(run.ID),
			"wave_id":       platformid.Format(run.WaveID),
			"status":        string(execution.CaseRunFailed),
			"failure_class": string(execution.FailureClassSession),
			"reason":        reason,
		}, nil)
	return nil
}

func (s *HumanService) requestTimeout(ctx context.Context) time.Duration {
	if s.timeout > 0 {
		return s.timeout
	}
	fallback := 30 * time.Minute
	if s.settings == nil {
		return fallback
	}
	seconds, err := s.settings.Int(ctx, "execution.human_request_timeout_seconds")
	if err != nil || seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

func normalizeHumanResponse(response map[string]any) (map[string]any, error) {
	if response == nil {
		return map[string]any{}, nil
	}
	normalized := map[string]any{}
	if code, ok := response[execution.HumanResponseCode]; ok {
		text, ok := code.(string)
		if !ok {
			return nil, platform.Invalid("response.code", "must be a string")
		}
		if len(text) > 500 {
			return nil, platform.Invalid("response.code", "must be at most 500 characters")
		}
		normalized[execution.HumanResponseCode] = strings.TrimSpace(text)
	}
	if note, ok := response[execution.HumanResponseNote]; ok {
		text, ok := note.(string)
		if !ok {
			return nil, platform.Invalid("response.note", "must be a string")
		}
		if len(text) > 2000 {
			return nil, platform.Invalid("response.note", "must be at most 2000 characters")
		}
		normalized[execution.HumanResponseNote] = strings.TrimSpace(text)
	}
	return normalized, nil
}

func (s *HumanService) record(ctx context.Context, action string, request execution.HumanRequest, actorID int64) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Record(ctx, auditapp.Event{
		ActorID:    actorPtr(actorID),
		Action:     action,
		TargetKind: "human_request",
		TargetID:   platformid.Format(request.ID),
		Metadata: map[string]any{
			"id":          platformid.Format(request.ID),
			"case_run_id": platformid.Format(derefID(request.CaseRunID)),
			"kind":        string(request.Kind),
			"status":      string(request.Status),
		},
	})
}

func (s *HumanService) now() time.Time {
	if s.clock == nil {
		return time.Now().UTC()
	}
	return s.clock.Now()
}

// ---------- structured questions (v0.22 B §6, spec R-B3-1..R-B3-6) ----------

// Conversation message kinds written for a structured question (DB-3). The
// version-context adapter maps these onto conversation_message.kind.
const (
	QuestionMessageKindQuestion = "question"
	QuestionMessageKindAnswer   = "answer"
)

// questionExpiryScanLimit bounds the lazy per-version expiry scan triggered by
// a question read. The global ExpireQuestions sweep is the unbounded path.
const questionExpiryScanLimit = 1000

// QuestionService owns the structured-question state machine on top of
// human_request: create (pending), answer, skip, reopen, owner-scoped cancel and
// the 24h expiry sweep. It never touches case_run/wave/stage state: questions
// never block stage progression (R-B3-4/C-2).
type QuestionService struct {
	humans   HumanRequestRepository
	messages QuestionMessageWriter
	settings SettingsReader
	ids      IDGenerator
	clock    Clock
	events   EventPublisher
	audit    AuditRecorder
	timeout  time.Duration
}

// QuestionServiceDeps wires the structured-question service. Messages is the
// version-conversation write port; Timeout overrides question.timeout_seconds
// when positive.
type QuestionServiceDeps struct {
	Humans   HumanRequestRepository
	Messages QuestionMessageWriter
	Settings SettingsReader
	IDs      IDGenerator
	Clock    Clock
	Events   EventPublisher
	Audit    AuditRecorder
	Timeout  time.Duration
}

// NewQuestionService builds the structured-question service.
func NewQuestionService(deps QuestionServiceDeps) *QuestionService {
	return &QuestionService{
		humans:   deps.Humans,
		messages: deps.Messages,
		settings: deps.Settings,
		ids:      deps.IDs,
		clock:    deps.Clock,
		events:   deps.Events,
		audit:    deps.Audit,
		timeout:  deps.Timeout,
	}
}

// QuestionCreateInput is one Host question batch creation. AgentRunID is the
// owning Host run: only that run may cancel/reopen the created questions.
type QuestionCreateInput struct {
	VersionID      int64
	ConversationID int64
	AgentRunID     int64
	Questions      []execution.HumanQuestion
	BatchID        string
	ExpiresAt      *time.Time
	ActorID        int64
}

// QuestionOwner identifies the Host run acting on its own questions.
type QuestionOwner struct {
	AgentRunID int64
}

// Create validates and persists one pending clarification_question batch plus
// its conversation question-card message. The default expiry is
// question.timeout_seconds (24h), capped by the payload shape.
func (s *QuestionService) Create(ctx context.Context, in QuestionCreateInput) (execution.HumanRequest, error) {
	if s == nil || s.humans == nil {
		return execution.HumanRequest{}, fmt.Errorf("question service human repository is not configured")
	}
	if err := requirePositiveID("version_id", in.VersionID); err != nil {
		return execution.HumanRequest{}, err
	}
	if err := requirePositiveID("conversation_id", in.ConversationID); err != nil {
		return execution.HumanRequest{}, err
	}
	if in.AgentRunID <= 0 {
		return execution.HumanRequest{}, platform.Invalid("agent_run_id", "must be the owning Host run")
	}
	if s.messages == nil {
		return execution.HumanRequest{}, fmt.Errorf("question message writer is not configured")
	}
	if s.ids == nil {
		return execution.HumanRequest{}, fmt.Errorf("question id generator is not configured")
	}
	if err := execution.ValidateHumanQuestions(in.Questions); err != nil {
		return execution.HumanRequest{}, err
	}
	now := s.now()
	expiresAt := now.Add(s.questionTimeout(ctx))
	if in.ExpiresAt != nil {
		if !in.ExpiresAt.After(now) {
			return execution.HumanRequest{}, platform.Invalid("expires_at", "must be in the future")
		}
		expiresAt = in.ExpiresAt.UTC()
	}
	requestID := s.ids.Next()
	batchID := strings.TrimSpace(in.BatchID)
	if batchID == "" {
		batchID = "qb-" + platformid.Format(requestID)
	}
	batch := execution.HumanQuestionBatch{
		Questions: append([]execution.HumanQuestion{}, in.Questions...),
		BatchID:   batchID,
		ExpiresAt: expiresAt,
	}
	if err := execution.ValidateHumanQuestionBatch(batch); err != nil {
		return execution.HumanRequest{}, err
	}

	conversationID := in.ConversationID
	request := execution.HumanRequest{
		ID:             requestID,
		VersionID:      in.VersionID,
		ConversationID: &conversationID,
		Kind:           execution.HumanClarificationQuestion,
		Payload: map[string]any{
			execution.HumanPayloadAgentRunID: platformid.Format(in.AgentRunID),
		},
		Question:  batch,
		Status:    execution.HumanPending,
		ExpiresAt: expiresAt,
		CreatedBy: actorPtr(in.ActorID),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.humans.Create(ctx, request); err != nil {
		return execution.HumanRequest{}, mapExecutionError(err, "question")
	}

	messageID, _, err := s.messages.AppendQuestionMessage(ctx, QuestionMessage{
		VersionID:       in.VersionID,
		ConversationID:  in.ConversationID,
		QuestionID:      requestID,
		Kind:            QuestionMessageKindQuestion,
		ClientMessageID: fmt.Sprintf("question-ask:%d", requestID),
		Payload:         questionMessagePayload(request),
		ActorID:         in.ActorID,
		AgentRunID:      in.AgentRunID,
	})
	if err != nil {
		// The pending row must not survive without its card; best-effort cancel
		// keeps the state machine truthful if the conversation write failed.
		_, _ = s.humans.Transition(ctx, requestID, execution.HumanPending, execution.HumanCancelled,
			map[string]any{"cancelled_by": "system", "reason": "question_message_failed"}, 0, now, nil)
		return execution.HumanRequest{}, mapExecutionError(err, "conversation")
	}
	request.MessageID = &messageID
	if err := s.humans.AttachMessage(ctx, requestID, messageID); err != nil {
		// The card exists but the question row could not record it; do not leave
		// a pending question that the UI can never render.
		_, _ = s.humans.Transition(ctx, requestID, execution.HumanPending, execution.HumanCancelled,
			map[string]any{"cancelled_by": "system", "reason": "question_message_attach_failed"}, 0, now, nil)
		return execution.HumanRequest{}, mapExecutionError(err, "question")
	}

	s.publish(ctx, EventQuestionCreated, request, in.ActorID,
		map[string]any{
			"question_id":     platformid.Format(request.ID),
			"conversation_id": platformid.Format(in.ConversationID),
			"message_id":      platformid.Format(messageID),
			"batch_id":        batchID,
			"status":          string(execution.HumanPending),
			"questions":       len(batch.Questions),
			"expires_at":      eventTime(expiresAt),
		})
	s.record(ctx, "question.created", request, in.ActorID)
	return request, nil
}

// Get returns one question of the version, lazily expiring it when overdue.
func (s *QuestionService) Get(ctx context.Context, versionID, questionID int64) (execution.HumanRequest, error) {
	request, err := s.scoped(ctx, versionID, questionID)
	if err != nil {
		return execution.HumanRequest{}, err
	}
	return s.expireIfOverdue(ctx, request)
}

// List returns one page of the version's questions, optionally filtered by
// status. Overdue pending questions are expired before the read (R-B3-4 lazy
// expiry) and each transition emits question.expired.
func (s *QuestionService) List(ctx context.Context, versionID int64, status string, limit int, beforeID *int64) ([]execution.HumanRequest, bool, error) {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return nil, false, err
	}
	if status != "" && !execution.HumanRequestStatus(status).Valid() {
		return nil, false, platform.Invalid("status", "must be one of pending, answered, expired, cancelled")
	}
	if err := s.expireOverdueForVersion(ctx, versionID); err != nil {
		return nil, false, err
	}
	return s.humans.ListQuestions(ctx, versionID, status, limit, beforeID)
}

// Answer records selected/text answers for every required question, writes the
// single conversation_message(kind=answer) and emits question.answered. The
// same answer retried is idempotent: no duplicate message/event.
func (s *QuestionService) Answer(ctx context.Context, versionID, questionID, actorID int64, answers []execution.HumanQuestionAnswer) (execution.HumanRequest, error) {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return execution.HumanRequest{}, err
	}
	if err := requirePositiveID("actor_id", actorID); err != nil {
		return execution.HumanRequest{}, err
	}
	request, err := s.scoped(ctx, versionID, questionID)
	if err != nil {
		return execution.HumanRequest{}, err
	}
	request, err = s.expireIfOverdue(ctx, request)
	if err != nil {
		return execution.HumanRequest{}, err
	}
	switch request.Status {
	case execution.HumanAnswered:
		if questionAnswersMatch(request.Response, answers) {
			return request, nil
		}
		return execution.HumanRequest{}, platform.StateConflict("the question is already answered; reopen it before changing the answer")
	case execution.HumanPending:
		// continue
	default:
		return execution.HumanRequest{}, platform.StateConflict("the question is not pending")
	}
	if err := execution.ValidateHumanQuestionAnswers(request.Question, answers); err != nil {
		return execution.HumanRequest{}, err
	}
	if s.messages == nil {
		return execution.HumanRequest{}, fmt.Errorf("question message writer is not configured")
	}

	now := s.now()
	normalized := normalizeQuestionAnswers(answers)
	revision := answerRevision(request.Response) + 1
	messageID, _, err := s.messages.AppendQuestionMessage(ctx, QuestionMessage{
		VersionID:       versionID,
		ConversationID:  derefID(request.ConversationID),
		QuestionID:      request.ID,
		Kind:            QuestionMessageKindAnswer,
		ClientMessageID: fmt.Sprintf("question-answer:%d:%d", request.ID, revision),
		Payload: map[string]any{
			"question_id": platformid.Format(request.ID),
			"batch_id":    request.Question.BatchID,
			"answers":     normalized,
			"answered_by": platformid.Format(actorID),
			"answered_at": eventTime(now),
		},
		ActorID:    actorID,
		AgentRunID: payloadInt64(request.Payload, execution.HumanPayloadAgentRunID),
	})
	if err != nil {
		return execution.HumanRequest{}, mapExecutionError(err, "conversation")
	}
	response := map[string]any{
		execution.HumanResponseAnswers:         normalized,
		execution.HumanResponseAnsweredBy:      platformid.Format(actorID),
		execution.HumanResponseAnsweredAt:      eventTime(now),
		execution.HumanResponseAnswerRevision:  revision,
		execution.HumanResponseAnswerMessageID: platformid.Format(messageID),
	}
	changed, err := s.humans.Transition(ctx, request.ID, execution.HumanPending, execution.HumanAnswered, response, actorID, now, nil)
	if err != nil {
		return execution.HumanRequest{}, mapExecutionError(err, "question")
	}
	if !changed {
		// A concurrent writer won the transition. If it stored the same answers
		// the call is an idempotent retry (the client message id also deduped
		// the conversation write); otherwise the answers conflict.
		current, getErr := s.humans.Get(ctx, request.ID)
		if getErr != nil {
			return execution.HumanRequest{}, mapExecutionError(getErr, "question")
		}
		if current.Status == execution.HumanAnswered && questionAnswersMatch(current.Response, answers) {
			return current, nil
		}
		return execution.HumanRequest{}, platform.StateConflict("the question is not pending")
	}
	request.Status = execution.HumanAnswered
	request.Response = response
	request.ResolvedBy = actorPtr(actorID)
	request.ResolvedAt = &now
	request.UpdatedAt = now

	s.publish(ctx, EventQuestionAnswered, request, actorID,
		map[string]any{
			"question_id":     platformid.Format(request.ID),
			"conversation_id": platformid.Format(derefID(request.ConversationID)),
			"batch_id":        request.Question.BatchID,
			"status":          string(execution.HumanAnswered),
			"answered_by":     platformid.Format(actorID),
			"answered_at":     eventTime(now),
			"message_id":      platformid.Format(messageID),
		})
	s.record(ctx, "question.answered", request, actorID)
	return request, nil
}

// Skip cancels a question without an answer. It is idempotent for an already
// skipped request and never writes a conversation answer message.
func (s *QuestionService) Skip(ctx context.Context, versionID, questionID, actorID int64, reason string) (execution.HumanRequest, error) {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return execution.HumanRequest{}, err
	}
	if err := requirePositiveID("actor_id", actorID); err != nil {
		return execution.HumanRequest{}, err
	}
	request, err := s.scoped(ctx, versionID, questionID)
	if err != nil {
		return execution.HumanRequest{}, err
	}
	request, err = s.expireIfOverdue(ctx, request)
	if err != nil {
		return execution.HumanRequest{}, err
	}
	if request.Status == execution.HumanCancelled {
		return request, nil
	}
	if request.Status != execution.HumanPending {
		return execution.HumanRequest{}, platform.StateConflict("the question is not pending")
	}
	now := s.now()
	reason = strings.TrimSpace(reason)
	if len(reason) > 500 {
		return execution.HumanRequest{}, platform.Invalid("reason", "must be at most 500 characters")
	}
	response := mergeQuestionResponse(questionResponseHistory(request.Response), map[string]any{
		execution.HumanResponseSkipped:   true,
		execution.HumanResponseSkippedBy: platformid.Format(actorID),
		execution.HumanResponseSkippedAt: eventTime(now),
	})
	if reason != "" {
		response[execution.HumanResponseReason] = reason
	}
	changed, err := s.humans.Transition(ctx, request.ID, execution.HumanPending, execution.HumanCancelled, response, actorID, now, nil)
	if err != nil {
		return execution.HumanRequest{}, mapExecutionError(err, "question")
	}
	if !changed {
		return execution.HumanRequest{}, platform.StateConflict("the question is not pending")
	}
	request.Status = execution.HumanCancelled
	request.Response = response
	request.ResolvedBy = actorPtr(actorID)
	request.ResolvedAt = &now
	request.UpdatedAt = now
	s.record(ctx, "question.skipped", request, actorID)
	return request, nil
}

// Reopen moves an answered/skipped/expired question back to pending for a
// correction. Only the owning Host run may reopen its own question; reopening
// an already-pending question is an idempotent no-op.
func (s *QuestionService) Reopen(ctx context.Context, versionID, questionID int64, owner QuestionOwner) (execution.HumanRequest, error) {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return execution.HumanRequest{}, err
	}
	request, err := s.scoped(ctx, versionID, questionID)
	if err != nil {
		return execution.HumanRequest{}, err
	}
	if err := requireQuestionOwner(request, owner); err != nil {
		return execution.HumanRequest{}, err
	}
	request, err = s.expireIfOverdue(ctx, request)
	if err != nil {
		return execution.HumanRequest{}, err
	}
	if request.Status == execution.HumanPending {
		return request, nil
	}
	if !request.Status.CanTransitionTo(execution.HumanPending) {
		return execution.HumanRequest{}, platform.StateConflict("the question cannot be reopened")
	}
	now := s.now()
	deadline := now.Add(s.questionTimeout(ctx))
	response := mergeQuestionResponse(questionResponseHistory(request.Response), map[string]any{
		execution.HumanResponseReopenedBy: platformid.Format(owner.AgentRunID),
		execution.HumanResponseReopenedAt: eventTime(now),
	})
	changed, err := s.humans.Transition(ctx, request.ID, request.Status, execution.HumanPending, response, 0, now, &deadline)
	if err != nil {
		return execution.HumanRequest{}, mapExecutionError(err, "question")
	}
	if !changed {
		return execution.HumanRequest{}, platform.StateConflict("the question cannot be reopened")
	}
	request.Status = execution.HumanPending
	request.Response = response
	request.ExpiresAt = deadline
	request.Question.ExpiresAt = deadline
	request.ResolvedBy = nil
	request.ResolvedAt = nil
	request.UpdatedAt = now
	s.record(ctx, "question.reopened", request, 0)
	return request, nil
}

// Cancel closes a pending question. Users cannot cancel; only the owning Host
// run may cancel its own question (R-B3-5). Cancelling an already-cancelled
// question is idempotent.
func (s *QuestionService) Cancel(ctx context.Context, versionID, questionID int64, owner QuestionOwner) error {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return err
	}
	request, err := s.scoped(ctx, versionID, questionID)
	if err != nil {
		return err
	}
	if err := requireQuestionOwner(request, owner); err != nil {
		return err
	}
	request, err = s.expireIfOverdue(ctx, request)
	if err != nil {
		return err
	}
	if request.Status == execution.HumanCancelled {
		return nil
	}
	if request.Status != execution.HumanPending {
		return platform.StateConflict("the question is not pending")
	}
	now := s.now()
	response := mergeQuestionResponse(questionResponseHistory(request.Response), map[string]any{
		"cancelled_by": platformid.Format(owner.AgentRunID),
	})
	changed, err := s.humans.Transition(ctx, request.ID, execution.HumanPending, execution.HumanCancelled,
		response, 0, now, nil)
	if err != nil {
		return mapExecutionError(err, "question")
	}
	if !changed {
		return platform.StateConflict("the question is not pending")
	}
	request.Status = execution.HumanCancelled
	request.Response = response
	request.UpdatedAt = now
	s.record(ctx, "question.cancelled", request, 0)
	return nil
}

// ExpireQuestions is the background sweep for pending questions past their
// expires_at. It only touches human_request rows and never pauses runs/waves.
func (s *QuestionService) ExpireQuestions(ctx context.Context, now time.Time) (int, error) {
	if s == nil || s.humans == nil {
		return 0, fmt.Errorf("question service human repository is not configured")
	}
	expired, err := s.humans.ExpireQuestions(ctx, now)
	if err != nil {
		return 0, mapExecutionError(err, "question")
	}
	for _, request := range expired {
		s.publish(ctx, EventQuestionExpired, request, 0,
			map[string]any{
				"question_id":     platformid.Format(request.ID),
				"conversation_id": platformid.Format(derefID(request.ConversationID)),
				"batch_id":        request.Question.BatchID,
				"status":          string(execution.HumanExpired),
				"expires_at":      eventTime(request.ExpiresAt),
			})
		s.record(ctx, "question.expired", request, 0)
	}
	return len(expired), nil
}

// AskFromTool implements the question.ask tool port: the Host run scope decides
// version/conversation/ownership and the model only supplies the payload.
func (s *QuestionService) AskFromTool(ctx context.Context, scope tools.Scope, in tools.QuestionAskInput) (tools.QuestionAskResult, error) {
	if scope.VersionID <= 0 || scope.ConversationID <= 0 || scope.AgentRunID <= 0 {
		return tools.QuestionAskResult{}, platform.Invalid("scope", "question.ask requires the injected version, conversation and agent run")
	}
	questions := make([]execution.HumanQuestion, 0, len(in.Questions))
	for _, item := range in.Questions {
		options := make([]execution.HumanQuestionOption, 0, len(item.Options))
		for _, option := range item.Options {
			options = append(options, execution.HumanQuestionOption{ID: option.ID, Label: option.Label})
		}
		questions = append(questions, execution.HumanQuestion{
			ID:            item.ID,
			Text:          item.Text,
			Options:       options,
			Multiple:      item.Multiple,
			AllowFreeText: item.AllowFreeText,
			Required:      item.Required,
			SourceRefs:    append([]string{}, item.SourceRefs...),
		})
	}
	request, err := s.Create(ctx, QuestionCreateInput{
		VersionID:      scope.VersionID,
		ConversationID: scope.ConversationID,
		AgentRunID:     scope.AgentRunID,
		Questions:      questions,
		BatchID:        in.BatchID,
		ExpiresAt:      in.ExpiresAt,
	})
	if err != nil {
		return tools.QuestionAskResult{}, err
	}
	return tools.QuestionAskResult{
		ID:        request.ID,
		MessageID: derefID(request.MessageID),
		BatchID:   request.Question.BatchID,
		Status:    string(request.Status),
		ExpiresAt: request.ExpiresAt,
	}, nil
}

// scoped loads one clarification question and rejects cross-version IDs.
func (s *QuestionService) scoped(ctx context.Context, versionID, questionID int64) (execution.HumanRequest, error) {
	if s == nil || s.humans == nil {
		return execution.HumanRequest{}, fmt.Errorf("question service human repository is not configured")
	}
	if err := requirePositiveID("question_id", questionID); err != nil {
		return execution.HumanRequest{}, err
	}
	request, err := s.humans.Get(ctx, questionID)
	if err != nil {
		return execution.HumanRequest{}, mapExecutionError(err, "question")
	}
	if request.VersionID != versionID || request.Kind != execution.HumanClarificationQuestion {
		return execution.HumanRequest{}, platform.NotFound("question")
	}
	return request, nil
}

// expireIfOverdue lazily expires one pending question whose deadline passed and
// emits question.expired exactly once (the guarded transition decides).
func (s *QuestionService) expireIfOverdue(ctx context.Context, request execution.HumanRequest) (execution.HumanRequest, error) {
	if request.Status != execution.HumanPending || request.ExpiresAt.IsZero() {
		return request, nil
	}
	now := s.now()
	if now.Before(request.ExpiresAt) {
		return request, nil
	}
	changed, err := s.humans.Transition(ctx, request.ID, execution.HumanPending, execution.HumanExpired, nil, 0, now, nil)
	if err != nil {
		return execution.HumanRequest{}, mapExecutionError(err, "question")
	}
	if !changed {
		current, getErr := s.humans.Get(ctx, request.ID)
		if getErr != nil {
			return execution.HumanRequest{}, mapExecutionError(getErr, "question")
		}
		return current, nil
	}
	request.Status = execution.HumanExpired
	request.UpdatedAt = now
	s.publish(ctx, EventQuestionExpired, request, 0,
		map[string]any{
			"question_id":     platformid.Format(request.ID),
			"conversation_id": platformid.Format(derefID(request.ConversationID)),
			"batch_id":        request.Question.BatchID,
			"status":          string(execution.HumanExpired),
			"expires_at":      eventTime(request.ExpiresAt),
		})
	s.record(ctx, "question.expired", request, 0)
	return request, nil
}

// expireOverdueForVersion expires the overdue pending questions of one version
// before a list read. A bounded scan keeps a read from becoming unbounded; the
// global sweep remains the complete path.
func (s *QuestionService) expireOverdueForVersion(ctx context.Context, versionID int64) error {
	pending, _, err := s.humans.ListQuestions(ctx, versionID, string(execution.HumanPending), questionExpiryScanLimit, nil)
	if err != nil {
		return mapExecutionError(err, "question")
	}
	for _, request := range pending {
		if request.ExpiresAt.IsZero() || s.now().Before(request.ExpiresAt) {
			continue
		}
		if _, err := s.expireIfOverdue(ctx, request); err != nil {
			return err
		}
	}
	return nil
}

// publish sends one structured-question event to the version channel.
func (s *QuestionService) publish(ctx context.Context, eventType string, request execution.HumanRequest, actorID int64, payload map[string]any) {
	scope := map[string]any{
		"question_id":     platformid.Format(request.ID),
		"conversation_id": platformid.Format(derefID(request.ConversationID)),
	}
	publishEvent(ctx, s.events, eventType, request.VersionID, scope, payload, actorPtr(actorID))
}

// record writes one best-effort audit entry for a question transition.
func (s *QuestionService) record(ctx context.Context, action string, request execution.HumanRequest, actorID int64) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Record(ctx, auditapp.Event{
		ActorID:    actorPtr(actorID),
		Action:     action,
		TargetKind: "human_request",
		TargetID:   platformid.Format(request.ID),
		Metadata: map[string]any{
			"id":              platformid.Format(request.ID),
			"version_id":      platformid.Format(request.VersionID),
			"conversation_id": platformid.Format(derefID(request.ConversationID)),
			"batch_id":        request.Question.BatchID,
			"status":          string(request.Status),
		},
	})
}

// questionTimeout resolves the question expiry: explicit override, then the
// question.timeout_seconds setting, then the 24h default.
func (s *QuestionService) questionTimeout(ctx context.Context) time.Duration {
	if s.timeout > 0 {
		return s.timeout
	}
	if s.settings == nil {
		return execution.DefaultQuestionTimeout
	}
	seconds, err := s.settings.Int(ctx, "question.timeout_seconds")
	if err != nil || seconds <= 0 {
		return execution.DefaultQuestionTimeout
	}
	return time.Duration(seconds) * time.Second
}

// now reads the injected clock.
func (s *QuestionService) now() time.Time {
	if s.clock == nil {
		return time.Now().UTC()
	}
	return s.clock.Now()
}

// requireQuestionOwner enforces "only the owning Host run may cancel/reopen its
// own questions" (R-B3-5).
func requireQuestionOwner(request execution.HumanRequest, owner QuestionOwner) error {
	if owner.AgentRunID <= 0 {
		return platform.Invalid("agent_run_id", "must be the owning Host run")
	}
	stored := payloadInt64(request.Payload, execution.HumanPayloadAgentRunID)
	if stored != owner.AgentRunID {
		return platform.Forbidden()
	}
	return nil
}

// questionMessagePayload is the conversation message payload of the question
// card (the authoritative question batch still lives in human_request.question).
func questionMessagePayload(request execution.HumanRequest) map[string]any {
	return map[string]any{
		"question_id": platformid.Format(request.ID),
		"batch_id":    request.Question.BatchID,
		"questions":   request.Question.Questions,
		"expires_at":  eventTime(request.Question.ExpiresAt),
	}
}

// normalizeQuestionAnswers renders answers for JSONB storage and idempotent
// comparison (IDs are decimal strings per the platform convention).
func normalizeQuestionAnswers(answers []execution.HumanQuestionAnswer) []any {
	normalized := make([]any, 0, len(answers))
	for _, answer := range answers {
		selected := make([]any, 0, len(answer.Selected))
		for _, value := range answer.Selected {
			selected = append(selected, value)
		}
		normalized = append(normalized, map[string]any{
			"question_id": strings.TrimSpace(answer.QuestionID),
			"selected":    selected,
			"text":        strings.TrimSpace(answer.Text),
		})
	}
	return normalized
}

// questionAnswersMatch reports whether a stored response holds exactly the
// submitted answers (same order and values); it makes a same-answer retry a
// no-op instead of a duplicate message/event.
func questionAnswersMatch(response map[string]any, answers []execution.HumanQuestionAnswer) bool {
	if response == nil {
		return false
	}
	stored, ok := response[execution.HumanResponseAnswers].([]any)
	if !ok || len(stored) != len(answers) {
		return false
	}
	for index, answer := range answers {
		item, ok := stored[index].(map[string]any)
		if !ok {
			return false
		}
		if item["question_id"] != strings.TrimSpace(answer.QuestionID) ||
			item["text"] != strings.TrimSpace(answer.Text) {
			return false
		}
		selected, ok := item["selected"].([]any)
		if !ok || len(selected) != len(answer.Selected) {
			return false
		}
		for selectedIndex, value := range answer.Selected {
			if selected[selectedIndex] != value {
				return false
			}
		}
	}
	return true
}

// answerRevision reads the stored answer revision so a reopened question writes
// a fresh conversation message while a crash-retried answer keeps the same
// idempotency key.
func answerRevision(response map[string]any) int {
	if response == nil {
		return 0
	}
	switch value := response[execution.HumanResponseAnswerRevision].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

// payloadInt64 reads a decimal-string payload field.
func payloadInt64(payload map[string]any, key string) int64 {
	if payload == nil {
		return 0
	}
	switch value := payload[key].(type) {
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err == nil && parsed > 0 {
			return parsed
		}
	case int64:
		return value
	case float64:
		return int64(value)
	}
	return 0
}

// questionResponseHistory keeps the monotonic answer revision across
// skip/cancel/reopen. This is the message-key history: a later answer must not
// reuse question-answer:<id>:<revision> and dedupe onto the stale message.
func questionResponseHistory(response map[string]any) map[string]any {
	history := map[string]any{}
	if revision := answerRevision(response); revision > 0 {
		history[execution.HumanResponseAnswerRevision] = revision
	}
	return history
}

// mergeQuestionResponse copies a stored response and applies the reopen markers.
func mergeQuestionResponse(response map[string]any, updates map[string]any) map[string]any {
	merged := make(map[string]any, len(response)+len(updates))
	for key, value := range response {
		merged[key] = value
	}
	for key, value := range updates {
		merged[key] = value
	}
	return merged
}
