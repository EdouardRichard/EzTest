package application

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// ActionRunAborted is the audit action recorded when a run is stopped by a
// user request (design spec §8.6).
const ActionRunAborted = "agent.run.aborted"

// Named abort causes carried through context.WithCancelCause (spec §6.3).
const (
	abortCodeUserAbort   = "user_abort"
	abortCodeShutdown    = "shutdown"
	abortCodeHardTimeout = "run_hard_timeout"
)

// Defaults used when the composition root leaves a dependency at its zero
// value. The environment layer validates the real limits; these keep the
// service defensively bounded.
const (
	defaultMaxConcurrentRuns = 4
	defaultHardTimeout       = 60 * time.Second
	// idlePollInterval is how often WaitIdle re-checks the active-run table.
	idlePollInterval = 20 * time.Millisecond
)

// user_message activity bounds (design spec §9.2).
const (
	maxUserMessageActivityRunes = 4096
	userMessageSummaryRunes     = 200
	// runTitleRunes is the session title preview length of the first message.
	runTitleRunes = 40
)

// abortCause is the cancellation cause that carries the named abort code into
// the runner. context.Cause returns it unchanged through WithTimeoutCause.
type abortCause struct {
	code    string
	message string
}

// Error implements error.
func (c *abortCause) Error() string {
	if c == nil {
		return "agent: run aborted"
	}
	if c.message != "" {
		return c.message
	}
	if c.code != "" {
		return "agent: run aborted: " + c.code
	}
	return "agent: run aborted"
}

// RunServiceDeps is the composition root's wiring of the run service. LLM,
// Events and Audit are the agent context's ports; the repositories are the
// domain persistence ports.
type RunServiceDeps struct {
	Profiles domain.ProfileRepository
	Sessions domain.SessionRepository
	Runs     domain.RunRepository
	Messages domain.MessageRepository

	LLM      LLMRuntime
	Context  *ContextBuilder
	Tools    *ToolRegistry
	Executor *ToolExecutor

	Events EventSink
	Audit  AuditSink
	Clock  Clock

	// MaxConcurrentRuns bounds the number of in-flight runs; the semaphore is
	// acquired non-blockingly so an exhausted budget fails the request.
	MaxConcurrentRuns int
	// HardTimeout is the fallback hard-stop budget added to run_seconds when
	// the profile leaves hard_timeout_seconds unset.
	HardTimeout time.Duration
}

// activeRun is one registered in-flight run: its id (for audit) and the cancel
// function abort/shutdown use.
type activeRun struct {
	runID  int64
	cancel context.CancelCauseFunc
}

// RunService starts, aborts and finishes agent runs. It owns the process-wide
// concurrency budget and the session -> cancel-function registry; the execution
// loop itself lives in runner.go.
type RunService struct {
	deps        RunServiceDeps
	rootCtx     context.Context
	rootCancel  context.CancelCauseFunc
	sem         chan struct{}
	hardTimeout time.Duration

	mu     sync.Mutex
	active map[int64]activeRun
	wg     sync.WaitGroup
}

// NewRunService builds the service. Missing optional dependencies are replaced
// with safe defaults so the service is usable in isolation and in tests.
func NewRunService(deps RunServiceDeps) *RunService {
	if deps.Context == nil {
		deps.Context = NewContextBuilder()
	}
	if deps.Clock == nil {
		deps.Clock = time.Now
	}
	if deps.MaxConcurrentRuns <= 0 {
		deps.MaxConcurrentRuns = defaultMaxConcurrentRuns
	}
	if deps.HardTimeout <= 0 {
		deps.HardTimeout = defaultHardTimeout
	}
	if deps.Executor == nil {
		deps.Executor = NewToolExecutor(deps.Tools, deps.Audit, 0)
	}
	rootCtx, rootCancel := context.WithCancelCause(context.Background())
	return &RunService{
		deps:        deps,
		rootCtx:     rootCtx,
		rootCancel:  rootCancel,
		sem:         make(chan struct{}, deps.MaxConcurrentRuns),
		hardTimeout: deps.HardTimeout,
		active:      make(map[int64]activeRun),
	}
}

// now returns the injected clock reading.
func (s *RunService) now() time.Time { return s.deps.Clock() }

// Start validates the request, creates the user turn atomically and launches
// the run goroutine. It never waits for the model: the caller gets the accepted
// run id as soon as the turn is persisted.
func (s *RunService) Start(ctx context.Context, sessionID int64, message string, actor *int64) (domain.StartResult, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return domain.StartResult{}, domain.ErrEmptyMessage
	}

	session, err := s.deps.Sessions.Get(ctx, sessionID)
	if err != nil {
		return domain.StartResult{}, err
	}
	profile, err := s.deps.Profiles.Get(ctx, session.ProfileID)
	if err != nil {
		return domain.StartResult{}, err
	}
	if !profile.Enabled {
		return domain.StartResult{}, domain.ErrProfileDisabled
	}
	caller, err := s.deps.LLM.Prepare(ctx, profile.Model)
	if err != nil {
		return domain.StartResult{}, err
	}

	// The capacity check is non-blocking: a full budget is a 503, never a wait.
	select {
	case s.sem <- struct{}{}:
	default:
		return domain.StartResult{}, domain.ErrCapacity
	}

	ids, err := s.deps.Runs.CreateTurn(ctx, sessionID, []domain.Part{{Type: "text", Text: message}}, runTitlePreview(message))
	if err != nil {
		<-s.sem
		return domain.StartResult{}, err
	}

	// The user_message activity is published before the runner starts so it
	// always precedes run_started (spec §9.1).
	emitter := newActivityEmitter(s.deps.Events, sessionID, profile.Key)
	text, truncated := activityTruncateRunes(message, maxUserMessageActivityRunes)
	_ = emitter.emit(ctx, ids.RunID, domain.KindUserMessage, activitySummarize(message, userMessageSummaryRunes), map[string]any{
		"message_id": ids.UserMessageID,
		"text":       text,
		"truncated":  truncated,
	})

	runCtx, cancel := context.WithCancelCause(s.rootCtx)
	s.mu.Lock()
	s.active[sessionID] = activeRun{runID: ids.RunID, cancel: cancel}
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.active, sessionID)
			s.mu.Unlock()
			cancel(nil)
			<-s.sem
		}()
		s.run(runCtx, ids.RunID, session, profile, caller, actor)
	}()

	return domain.StartResult{RunID: ids.RunID, UserMessageID: ids.UserMessageID}, nil
}

// Abort cancels the session's active run with the user_abort cause. When the
// session is idle it reports "idle" instead of an error.
func (s *RunService) Abort(ctx context.Context, sessionID int64, actor *int64) (domain.AbortResult, error) {
	s.mu.Lock()
	run, active := s.active[sessionID]
	s.mu.Unlock()
	if !active {
		return domain.AbortResult{Status: "idle"}, nil
	}

	run.cancel(&abortCause{code: abortCodeUserAbort})
	s.recordAudit(ctx, AuditEvent{
		Action:      ActionRunAborted,
		ActorUserID: actor,
		TargetType:  "agent_run",
		TargetID:    strconv.FormatInt(run.runID, 10),
		Metadata: map[string]any{
			"run_id":     run.runID,
			"session_id": sessionID,
			"abort_code": abortCodeUserAbort,
		},
	})
	return domain.AbortResult{Status: "stopping"}, nil
}

// WaitIdle polls the active-run registry until the session is idle or the
// timeout elapses. It reports whether the session became idle.
func (s *RunService) WaitIdle(ctx context.Context, sessionID int64, timeout time.Duration) bool {
	ticker := time.NewTicker(idlePollInterval)
	defer ticker.Stop()
	deadline := time.Now().Add(timeout)
	for {
		if !s.isActive(sessionID) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return !s.isActive(sessionID)
		case <-ticker.C:
		}
	}
}

// Shutdown cancels every running run with the shutdown cause and waits for the
// runner goroutines until ctx expires.
func (s *RunService) Shutdown(ctx context.Context) {
	s.rootCancel(&abortCause{code: abortCodeShutdown})
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// RecoverInterrupted marks runs left running by a previous process as
// interrupted and reports how many were changed.
func (s *RunService) RecoverInterrupted(ctx context.Context) (int64, error) {
	return s.deps.Runs.MarkRunningInterrupted(ctx)
}

// isActive reports whether the session currently has a registered run.
func (s *RunService) isActive(sessionID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, active := s.active[sessionID]
	return active
}

// recordAudit writes one audit entry; audit failures are logged, never
// surfaced, because they must not affect the run.
func (s *RunService) recordAudit(ctx context.Context, event AuditEvent) {
	if s.deps.Audit == nil {
		return
	}
	if err := s.deps.Audit.Record(ctx, event); err != nil {
		slog.ErrorContext(ctx, "agent: record audit entry", "action", event.Action, "error", err)
	}
}

// runTitlePreview builds the session title preview from the first message:
// the first 40 runes plus an ellipsis when longer. The repository only applies
// it when the session title is still empty.
func runTitlePreview(message string) string {
	runes := []rune(message)
	if len(runes) <= runTitleRunes {
		return message
	}
	return string(runes[:runTitleRunes]) + "…"
}
