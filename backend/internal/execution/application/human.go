package executionapp

import (
	"context"
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
		Status:    execution.HumanOpen,
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
	if request.Status != execution.HumanOpen {
		return execution.HumanRequest{}, platform.StateConflict("the human request is not open")
	}
	normalized, err := normalizeHumanResponse(response)
	if err != nil {
		return execution.HumanRequest{}, err
	}
	now := s.now()
	changed, err := s.humans.Transition(ctx, humanRequestID, execution.HumanOpen, execution.HumanResolved, normalized, actorID, now)
	if err != nil {
		return execution.HumanRequest{}, mapExecutionError(err, "human_request")
	}
	if !changed {
		return execution.HumanRequest{}, platform.StateConflict("the human request is not open")
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
	if request.Status != execution.HumanOpen {
		return platform.StateConflict("the human request is not open")
	}
	now := s.now()
	changed, err := s.humans.Transition(ctx, humanRequestID, execution.HumanOpen, execution.HumanCancelled, nil, actorID, now)
	if err != nil {
		return mapExecutionError(err, "human_request")
	}
	if !changed {
		return platform.StateConflict("the human request is not open")
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
	publishEvent(ctx, s.events, EventCaseRunResumed, run.WaveID,
		map[string]any{"case_run_id": platformid.Format(run.ID)},
		map[string]any{"case_run_id": platformid.Format(run.ID)}, actorPtr(actorID))
	return s.resumeWaveAfterHuman(ctx, caseRunID, actorID)
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
	publishEvent(ctx, s.events, EventCaseRunFinished, derefID(&run.WaveID),
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
