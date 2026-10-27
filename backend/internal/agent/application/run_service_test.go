package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	agenttools "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/tools"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// runTestState is the shared in-memory store behind the fake repositories.
type runTestState struct {
	mu sync.Mutex

	profile  domain.AgentProfile
	sessions map[int64]domain.Session

	messages []domain.Message
	nextID   int64
	nextRun  int64

	active     map[int64]bool
	runSession map[int64]int64

	createTurnErr error

	// listMessagesHook, when set, replaces the default history read. Tests use
	// it to hold the initial history load until the run context is cancelled.
	listMessagesHook func(ctx context.Context, sessionID int64) ([]domain.Message, error)
	// finishHook, when set, runs after a terminal result is recorded and the
	// session's active flag cleared, before Finish returns. Tests use it to
	// start a successor run while a predecessor's runner is still cleaning up.
	finishHook func(ctx context.Context, runID int64) error

	finished       []runTestFinishedRun
	progress       []runTestProgress
	created        []runTestCreatedTurn
	completed      []int64
	interrupted    int64
	interruptedErr error
}

type runTestFinishedRun struct {
	runID  int64
	result domain.RunResult
}

type runTestProgress struct {
	runID int64
	turns int
	usage domain.Usage
}

type runTestCreatedTurn struct {
	sessionID int64
	content   []domain.Part
	title     string
}

// runTestProfile returns the builtin profile with a stable ID. Its allowlist is
// the builtin one (file.list, file.read, http.get, test.echo, test.fail); the
// tests that only need test tools re-register the whole list through the
// registry, so the allowlist stays untouched.
func runTestProfile(t *testing.T) domain.AgentProfile {
	t.Helper()
	profile := domain.BuiltinProfile()
	profile.ID = 1
	if err := profile.Validate(); err != nil {
		t.Fatalf("builtin profile is not valid: %v", err)
	}
	return profile
}

func runTestNewState(t *testing.T, profile domain.AgentProfile, sessionIDs ...int64) *runTestState {
	t.Helper()
	state := &runTestState{
		profile:    profile,
		sessions:   make(map[int64]domain.Session, len(sessionIDs)),
		nextID:     100,
		nextRun:    0,
		active:     make(map[int64]bool),
		runSession: make(map[int64]int64),
	}
	for _, id := range sessionIDs {
		state.sessions[id] = domain.Session{
			ID:         id,
			ProfileID:  profile.ID,
			ProfileKey: profile.Key,
		}
	}
	return state
}

// messagesFor returns a copy of the session's messages.
func (s *runTestState) messagesFor(sessionID int64) []domain.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	messages := make([]domain.Message, 0, len(s.messages))
	for _, message := range s.messages {
		if message.SessionID == sessionID {
			messages = append(messages, message)
		}
	}
	return messages
}

// finishedResults returns a copy of the recorded terminal results.
func (s *runTestState) finishedResults() []runTestFinishedRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]runTestFinishedRun(nil), s.finished...)
}

// progressUpdates returns a copy of the recorded progress updates.
func (s *runTestState) progressUpdates() []runTestProgress {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]runTestProgress(nil), s.progress...)
}

// setListMessagesHook installs the history-read hook.
func (s *runTestState) setListMessagesHook(hook func(ctx context.Context, sessionID int64) ([]domain.Message, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listMessagesHook = hook
}

// setFinishHook installs the terminal-write hook.
func (s *runTestState) setFinishHook(hook func(ctx context.Context, runID int64) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishHook = hook
}

// completedMessages returns the message ids CompleteAssistant was called with.
func (s *runTestState) completedMessages() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.completed...)
}

// runFakeProfiles implements domain.ProfileRepository.
type runFakeProfiles struct {
	state *runTestState
}

func (f *runFakeProfiles) List(_ context.Context) ([]domain.AgentProfile, error) {
	return []domain.AgentProfile{f.state.profile}, nil
}

func (f *runFakeProfiles) Get(_ context.Context, id int64) (domain.AgentProfile, error) {
	if id != f.state.profile.ID {
		return domain.AgentProfile{}, domain.ErrNotFound
	}
	return f.state.profile, nil
}

func (f *runFakeProfiles) Update(_ context.Context, _ int64, _ domain.UpdateParams) (domain.AgentProfile, error) {
	return domain.AgentProfile{}, errors.New("run service test: Update is not used")
}

func (f *runFakeProfiles) EnsureBuiltin(_ context.Context, _ domain.AgentProfile) (bool, error) {
	return false, nil
}

// runFakeSessions implements domain.SessionRepository.
type runFakeSessions struct {
	state *runTestState
}

func (f *runFakeSessions) List(_ context.Context, _ int64, _ int, _ *int64) ([]domain.SessionSummary, bool, error) {
	return nil, false, nil
}

func (f *runFakeSessions) Create(_ context.Context, _ int64, _ string, _ *int64) (domain.Session, error) {
	return domain.Session{}, errors.New("run service test: Create is not used")
}

func (f *runFakeSessions) Get(_ context.Context, id int64) (domain.Session, error) {
	session, ok := f.state.sessions[id]
	if !ok {
		return domain.Session{}, domain.ErrNotFound
	}
	return session, nil
}

func (f *runFakeSessions) UpdateTitle(_ context.Context, _ int64, _ string) (domain.Session, error) {
	return domain.Session{}, errors.New("run service test: UpdateTitle is not used")
}

func (f *runFakeSessions) Delete(_ context.Context, _ int64) error {
	return errors.New("run service test: Delete is not used")
}

// runFakeRuns implements domain.RunRepository. CreateTurn mirrors the real
// CTE: it inserts the user message, the running run and claims the session's
// single-flight slot.
type runFakeRuns struct {
	state *runTestState
}

func (f *runFakeRuns) CreateTurn(_ context.Context, sessionID int64, content []domain.Part, titlePreview string) (domain.TurnIDs, error) {
	state := f.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.createTurnErr != nil {
		return domain.TurnIDs{}, state.createTurnErr
	}
	if _, ok := state.sessions[sessionID]; !ok {
		return domain.TurnIDs{}, domain.ErrNotFound
	}
	if state.active[sessionID] {
		return domain.TurnIDs{}, domain.ErrRunActive
	}
	state.nextRun++
	state.nextID++
	runID, messageID := state.nextRun, state.nextID
	state.active[sessionID] = true
	state.runSession[runID] = sessionID
	state.messages = append(state.messages, domain.Message{
		ID:        messageID,
		SessionID: sessionID,
		RunID:     &runID,
		Role:      "user",
		Parts:     content,
	})
	state.created = append(state.created, runTestCreatedTurn{
		sessionID: sessionID,
		content:   content,
		title:     titlePreview,
	})
	return domain.TurnIDs{RunID: runID, UserMessageID: messageID}, nil
}

func (f *runFakeRuns) Get(_ context.Context, _ int64) (domain.Run, error) {
	return domain.Run{}, domain.ErrNotFound
}

func (f *runFakeRuns) Active(_ context.Context, _ int64) (domain.Run, error) {
	return domain.Run{}, domain.ErrNotFound
}

func (f *runFakeRuns) Latest(_ context.Context, _ int64) (domain.Run, error) {
	return domain.Run{}, domain.ErrNotFound
}

func (f *runFakeRuns) UpdateProgress(_ context.Context, runID int64, turns int, usage domain.Usage) error {
	state := f.state
	state.mu.Lock()
	defer state.mu.Unlock()
	state.progress = append(state.progress, runTestProgress{runID: runID, turns: turns, usage: usage})
	return nil
}

func (f *runFakeRuns) Finish(ctx context.Context, runID int64, result domain.RunResult) error {
	state := f.state
	state.mu.Lock()
	state.finished = append(state.finished, runTestFinishedRun{runID: runID, result: result})
	if sessionID, ok := state.runSession[runID]; ok {
		delete(state.active, sessionID)
	}
	hook := state.finishHook
	state.mu.Unlock()
	if hook != nil {
		return hook(ctx, runID)
	}
	return nil
}

func (f *runFakeRuns) MarkRunningInterrupted(_ context.Context) (int64, error) {
	state := f.state
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.interrupted, state.interruptedErr
}

// runFakeMessages implements domain.MessageRepository.
type runFakeMessages struct {
	state *runTestState
}

func (f *runFakeMessages) ListBySession(ctx context.Context, sessionID int64) ([]domain.Message, error) {
	state := f.state
	state.mu.Lock()
	hook := state.listMessagesHook
	state.mu.Unlock()
	if hook != nil {
		return hook(ctx, sessionID)
	}
	return state.messagesFor(sessionID), nil
}

func (f *runFakeMessages) Get(_ context.Context, _ int64) (domain.Message, error) {
	return domain.Message{}, domain.ErrNotFound
}

func (f *runFakeMessages) Append(_ context.Context, message domain.Message) (domain.Message, error) {
	state := f.state
	state.mu.Lock()
	defer state.mu.Unlock()
	state.nextID++
	message.ID = state.nextID
	state.messages = append(state.messages, message)
	return message, nil
}

func (f *runFakeMessages) AppendAssistantPlaceholder(_ context.Context, sessionID, runID int64) (int64, error) {
	state := f.state
	state.mu.Lock()
	defer state.mu.Unlock()
	state.nextID++
	messageID := state.nextID
	state.messages = append(state.messages, domain.Message{
		ID:        messageID,
		SessionID: sessionID,
		RunID:     &runID,
		Role:      "assistant",
		Parts:     []domain.Part{},
	})
	return messageID, nil
}

func (f *runFakeMessages) CompleteAssistant(_ context.Context, messageID int64, parts []domain.Part, reasoning string, calls []domain.ToolCall, usage *domain.Usage) error {
	state := f.state
	state.mu.Lock()
	defer state.mu.Unlock()
	for i := range state.messages {
		if state.messages[i].ID != messageID {
			continue
		}
		if state.messages[i].Role != "assistant" {
			return domain.ErrNotFound
		}
		state.messages[i].Parts = parts
		state.messages[i].Reasoning = reasoning
		state.messages[i].ToolCalls = calls
		state.messages[i].Usage = usage
		state.completed = append(state.completed, messageID)
		return nil
	}
	return domain.ErrNotFound
}

// runFakeRuntime implements LLMRuntime.
type runFakeRuntime struct {
	mu     sync.Mutex
	caller application.LLMCaller
	err    error
	models []string
}

func (r *runFakeRuntime) Prepare(_ context.Context, modelOverride string) (application.LLMCaller, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.models = append(r.models, modelOverride)
	if r.err != nil {
		return nil, r.err
	}
	return r.caller, nil
}

// runFakeScript is one scripted ChatStream response. hold keeps the stream open
// until the context is cancelled (used by the abort/shutdown tests); started is
// closed after every scripted event has been handed to the runner.
type runFakeScript struct {
	events  []application.LLMStreamEvent
	err     error
	hold    bool
	started chan struct{}
	// wait, when set, runs inside ChatStream after the call is recorded and
	// before it returns; tests use it to hold the synchronous handshake until
	// the run context is cancelled.
	wait func(ctx context.Context) error
}

// runFakeCaller implements LLMCaller with a per-call script.
type runFakeCaller struct {
	mu            sync.Mutex
	model         string
	contextWindow int
	scripts       []runFakeScript
	requests      []application.LLMRequest
	calls         int
}

func (c *runFakeCaller) Model() string { return c.model }

func (c *runFakeCaller) ContextWindowTokens() int { return c.contextWindow }

func (c *runFakeCaller) ChatStream(ctx context.Context, req application.LLMRequest) (<-chan application.LLMStreamEvent, error) {
	c.mu.Lock()
	c.requests = append(c.requests, req)
	index := c.calls
	c.calls++
	var script runFakeScript
	if index < len(c.scripts) {
		script = c.scripts[index]
	} else {
		script.err = errors.New("run service test: unexpected ChatStream call")
	}
	c.mu.Unlock()

	if script.wait != nil {
		if err := script.wait(ctx); err != nil {
			return nil, err
		}
	}
	if script.err != nil {
		return nil, script.err
	}
	// An unbuffered channel makes the delivery of each scripted event
	// deterministic: the runner has received (and therefore processed) it
	// before the next step of the script runs.
	out := make(chan application.LLMStreamEvent)
	go func() {
		defer close(out)
		for _, event := range script.events {
			select {
			case out <- event:
			case <-ctx.Done():
				return
			}
		}
		if script.started != nil {
			close(script.started)
		}
		if script.hold {
			<-ctx.Done()
		}
	}()
	return out, nil
}

// requests returns a copy of the recorded requests.
func (c *runFakeCaller) recordedRequests() []application.LLMRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]application.LLMRequest(nil), c.requests...)
}

// callCount returns how many ChatStream calls were made.
func (c *runFakeCaller) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// runFakeCodedError carries a stable error code, like the gateway adapter's
// errors do.
type runFakeCodedError struct {
	code    string
	message string
}

func (e *runFakeCodedError) Error() string { return e.message }

func (e *runFakeCodedError) ErrorCode() string { return e.code }

// runFakeEvents implements EventSink.
type runFakeEvents struct {
	mu     sync.Mutex
	events []application.ActivityEvent
}

func (f *runFakeEvents) PublishActivity(_ context.Context, event application.ActivityEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
	return nil
}

func (f *runFakeEvents) snapshot() []application.ActivityEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]application.ActivityEvent(nil), f.events...)
}

// runFakeAudit implements AuditSink. Next to every event it captures the
// request ID carried by the Record context, so tests can pin that the run
// goroutine's audit entries stay correlated with the originating request.
type runFakeAudit struct {
	mu         sync.Mutex
	events     []application.AuditEvent
	requestIDs []string
}

func (f *runFakeAudit) Record(ctx context.Context, event application.AuditEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
	f.requestIDs = append(f.requestIDs, application.RequestIDFromContext(ctx))
	return nil
}

func (f *runFakeAudit) snapshot() []application.AuditEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]application.AuditEvent(nil), f.events...)
}

// requestIDsByAction returns the request ID captured alongside every event
// with the given action, in record order.
func (f *runFakeAudit) requestIDsByAction(action string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]string, 0, len(f.events))
	for index, event := range f.events {
		if event.Action == action {
			ids = append(ids, f.requestIDs[index])
		}
	}
	return ids
}

// runFakeBlockingTool blocks in Execute until the run context ends.
type runFakeBlockingTool struct {
	started chan struct{}
	once    sync.Once
}

func (t *runFakeBlockingTool) Name() string { return "test.block" }

func (t *runFakeBlockingTool) Description() string { return "block until the run is aborted" }

func (t *runFakeBlockingTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

func (t *runFakeBlockingTool) Execute(ctx context.Context, _ json.RawMessage) (domain.ToolResult, error) {
	t.once.Do(func() { close(t.started) })
	<-ctx.Done()
	return domain.ToolResult{}, ctx.Err()
}

// runFakeHugeTool returns a fixed large result without carrying it through the
// model-supplied arguments, so the test exercises only the 32KB result cap.
type runFakeHugeTool struct {
	content string
}

func (t *runFakeHugeTool) Name() string { return "test.huge" }

func (t *runFakeHugeTool) Description() string { return "return a large result" }

func (t *runFakeHugeTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

func (t *runFakeHugeTool) Execute(_ context.Context, _ json.RawMessage) (domain.ToolResult, error) {
	return domain.ToolResult{Content: t.content}, nil
}

// runTestServiceOption customises the service under test.
type runTestServiceOption func(deps *application.RunServiceDeps, tools *[]domain.Tool)

func runTestMaxConcurrentRuns(limit int) runTestServiceOption {
	return func(deps *application.RunServiceDeps, _ *[]domain.Tool) {
		deps.MaxConcurrentRuns = limit
	}
}

func runTestHardTimeout(timeout time.Duration) runTestServiceOption {
	return func(deps *application.RunServiceDeps, _ *[]domain.Tool) {
		deps.HardTimeout = timeout
	}
}

func runTestClock(clock application.Clock) runTestServiceOption {
	return func(deps *application.RunServiceDeps, _ *[]domain.Tool) {
		deps.Clock = clock
	}
}

func runTestRuntimeError(err error) runTestServiceOption {
	return func(deps *application.RunServiceDeps, _ *[]domain.Tool) {
		deps.LLM = &runFakeRuntime{err: err}
	}
}

func runTestRegisterTool(tool domain.Tool) runTestServiceOption {
	return func(_ *application.RunServiceDeps, tools *[]domain.Tool) {
		*tools = append(*tools, tool)
	}
}

// runTestNewService assembles a RunService over the fake repositories, the
// scripted caller, a real ToolRegistry/ToolExecutor and the capturing sinks.
func runTestNewService(t *testing.T, state *runTestState, caller *runFakeCaller, opts ...runTestServiceOption) (*application.RunService, *runFakeEvents, *runFakeAudit) {
	t.Helper()
	sink := &runFakeEvents{}
	audit := &runFakeAudit{}
	tools := []domain.Tool{agenttools.NewTestEcho(), agenttools.NewTestFail()}
	deps := application.RunServiceDeps{
		Profiles:          &runFakeProfiles{state: state},
		Sessions:          &runFakeSessions{state: state},
		Runs:              &runFakeRuns{state: state},
		Messages:          &runFakeMessages{state: state},
		LLM:               &runFakeRuntime{caller: caller},
		Context:           application.NewContextBuilder(),
		Events:            sink,
		Audit:             audit,
		Clock:             time.Now,
		MaxConcurrentRuns: 4,
		HardTimeout:       time.Minute,
	}
	for _, opt := range opts {
		opt(&deps, &tools)
	}
	deps.Tools = application.NewToolRegistry(tools...)
	deps.Executor = application.NewToolExecutor(deps.Tools, audit, 0)
	return application.NewRunService(deps), sink, audit
}

// runTestWaitIdle fails the test unless the session becomes idle in time.
func runTestWaitIdle(t *testing.T, service *application.RunService, sessionID int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !service.WaitIdle(ctx, sessionID, 5*time.Second) {
		t.Fatalf("session %d did not become idle", sessionID)
	}
}

// runTestAwaitSignal fails the test unless the signal arrives in time.
func runTestAwaitSignal(t *testing.T, signal <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// runTestFinished returns the single finished run result.
func runTestFinished(t *testing.T, state *runTestState) domain.RunResult {
	t.Helper()
	finished := state.finishedResults()
	if len(finished) != 1 {
		t.Fatalf("finished runs=%d want 1", len(finished))
	}
	return finished[0].result
}

// runTestEventKinds returns the detail.kind of every event in order.
func runTestEventKinds(events []application.ActivityEvent) []string {
	kinds := make([]string, 0, len(events))
	for _, event := range events {
		kind, _ := event.Detail["kind"].(string)
		kinds = append(kinds, kind)
	}
	return kinds
}

// runTestEventsOfKind returns every event with the given detail.kind.
func runTestEventsOfKind(events []application.ActivityEvent, kind string) []application.ActivityEvent {
	matched := make([]application.ActivityEvent, 0)
	for _, event := range events {
		if event.Detail["kind"] == kind {
			matched = append(matched, event)
		}
	}
	return matched
}

// runTestRequireOrderedKinds fails unless kinds contains want as an ordered
// subsequence.
func runTestRequireOrderedKinds(t *testing.T, kinds []string, want ...string) {
	t.Helper()
	next := 0
	for _, kind := range kinds {
		if next < len(want) && kind == want[next] {
			next++
		}
	}
	if next != len(want) {
		t.Fatalf("event kinds %v do not contain the ordered subsequence %v", kinds, want)
	}
}

// runTestDetailInt64 reads an integer detail value.
func runTestDetailInt64(t *testing.T, detail map[string]any, key string) int64 {
	t.Helper()
	switch number := detail[key].(type) {
	case int64:
		return number
	case int:
		return int64(number)
	default:
		t.Fatalf("detail[%s]=%v (%T) is not an integer", key, detail[key], detail[key])
		return 0
	}
}

// runTestAssertEnvelope checks the frozen activity envelope fields.
func runTestAssertEnvelope(t *testing.T, event application.ActivityEvent, sessionID int64, role string) {
	t.Helper()
	if want := domain.ChannelForSession(sessionID); event.Channel != want {
		t.Fatalf("event channel=%q want %q", event.Channel, want)
	}
	if event.SessionID != sessionID {
		t.Fatalf("event session_id=%d want %d", event.SessionID, sessionID)
	}
	if event.AgentRole != role {
		t.Fatalf("event agent_role=%q want %q", event.AgentRole, role)
	}
	if event.Summary == "" {
		t.Fatalf("event %v has an empty summary", event.Detail["kind"])
	}
	if n := utf8.RuneCountInString(event.Summary); n > 500 {
		t.Fatalf("event summary length=%d runes exceeds 500", n)
	}
}

func TestStartRunsToolLoopAndEmitsOrderedActivities(t *testing.T) {
	profile := runTestProfile(t)
	state := runTestNewState(t, profile, 7)

	const (
		reasoning = "先确认工具链路"
		toolText  = "我调用测试工具。"
		answer    = "诊断完成"
	)
	caller := &runFakeCaller{
		model:         "deepseek-flash",
		contextWindow: 100000,
		scripts: []runFakeScript{
			{events: []application.LLMStreamEvent{
				{ReasoningDelta: reasoning},
				{Delta: toolText},
				{
					Done:         true,
					FinishReason: "tool_calls",
					ToolCalls: []domain.ToolCall{
						{ID: "call_1", Name: "test.echo", Arguments: `{"text":"README"}`},
					},
					Usage: domain.Usage{PromptTokens: 10, CompletionTokens: 5, ReasoningTokens: 8, TotalTokens: 15},
				},
			}},
			{events: []application.LLMStreamEvent{
				{Delta: answer},
				{
					Done:         true,
					FinishReason: "stop",
					Usage:        domain.Usage{PromptTokens: 20, CompletionTokens: 7, TotalTokens: 27},
				},
			}},
		},
	}

	service, sink, _ := runTestNewService(t, state, caller)
	result, err := service.Start(context.Background(), 7, "检查运行时健康", nil)
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if result.RunID == 0 || result.UserMessageID == 0 {
		t.Fatalf("StartResult=%+v want both ids", result)
	}
	runTestWaitIdle(t, service, 7)

	finished := runTestFinished(t, state)
	if finished.Status != domain.RunDone || finished.TerminalReason != domain.ReasonCompleted {
		t.Fatalf("run finished %s/%s want done/completed", finished.Status, finished.TerminalReason)
	}
	if finished.Turns != 2 {
		t.Fatalf("run turns=%d want 2", finished.Turns)
	}
	if finished.Usage.PromptTokens != 30 || finished.Usage.CompletionTokens != 12 || finished.Usage.ReasoningTokens != 8 {
		t.Fatalf("run usage=%+v want prompt=30 completion=12 reasoning=8", finished.Usage)
	}

	messages := state.messagesFor(7)
	if len(messages) != 4 {
		t.Fatalf("messages=%d want user, assistant, tool, assistant: %+v", len(messages), messages)
	}
	if messages[0].Role != "user" || len(messages[0].Parts) != 1 || messages[0].Parts[0].Text != "检查运行时健康" {
		t.Fatalf("user message=%+v", messages[0])
	}
	assistantWithTool := messages[1]
	if assistantWithTool.Role != "assistant" {
		t.Fatalf("second message role=%q want assistant", assistantWithTool.Role)
	}
	if assistantWithTool.Reasoning != reasoning {
		t.Fatalf("assistant reasoning=%q want %q", assistantWithTool.Reasoning, reasoning)
	}
	if len(assistantWithTool.Parts) != 1 || assistantWithTool.Parts[0].Text != toolText {
		t.Fatalf("assistant parts=%+v want %q", assistantWithTool.Parts, toolText)
	}
	if len(assistantWithTool.ToolCalls) != 1 || assistantWithTool.ToolCalls[0].ID != "call_1" {
		t.Fatalf("assistant tool calls=%+v want call_1", assistantWithTool.ToolCalls)
	}
	toolMessage := messages[2]
	if toolMessage.Role != "tool" || toolMessage.ToolCallID != "call_1" || toolMessage.ToolName != "test.echo" {
		t.Fatalf("tool message=%+v", toolMessage)
	}
	if len(toolMessage.Parts) != 1 || toolMessage.Parts[0].Text != "README" {
		t.Fatalf("tool result parts=%+v want README", toolMessage.Parts)
	}
	finalAssistant := messages[3]
	if finalAssistant.Role != "assistant" || len(finalAssistant.Parts) != 1 || finalAssistant.Parts[0].Text != answer {
		t.Fatalf("final assistant=%+v want %q", finalAssistant, answer)
	}

	requests := caller.recordedRequests()
	if len(requests) != 2 {
		t.Fatalf("LLM requests=%d want 2", len(requests))
	}
	first := requests[0]
	if first.Model != "deepseek-flash" || first.MaxTokens != profile.MaxOutputTokens {
		t.Fatalf("first request model=%q max_tokens=%d", first.Model, first.MaxTokens)
	}
	if first.Thinking != profile.Thinking || first.ReasoningEffort != profile.ReasoningEffort {
		t.Fatalf("first request thinking=%q effort=%q", first.Thinking, first.ReasoningEffort)
	}
	if len(first.Tools) != 2 || first.Tools[0].Name != "test.echo" {
		t.Fatalf("first request tools=%+v want test.echo and test.fail", first.Tools)
	}
	if len(first.Messages) < 2 || first.Messages[len(first.Messages)-1].Role != "user" {
		t.Fatalf("first request messages=%+v", first.Messages)
	}

	// Review focus #1: the reasoning of the tool-calling assistant turn must be
	// echoed back on the second request, otherwise thinking-mode tool calls are
	// rejected by the upstream.
	var echoed *application.LLMMessage
	for i := range requests[1].Messages {
		message := requests[1].Messages[i]
		if message.Role == "assistant" && len(message.ToolCalls) == 1 {
			echoed = &requests[1].Messages[i]
			break
		}
	}
	if echoed == nil {
		t.Fatalf("second request has no assistant tool-call message: %+v", requests[1].Messages)
	}
	if echoed.ReasoningContent != reasoning {
		t.Fatalf("second request reasoning_content=%q want %q", echoed.ReasoningContent, reasoning)
	}
	if echoed.ToolCalls[0].ID != "call_1" {
		t.Fatalf("second request tool_call id=%q want call_1", echoed.ToolCalls[0].ID)
	}

	events := sink.snapshot()
	if len(events) == 0 {
		t.Fatal("no activity events were published")
	}
	for _, event := range events {
		runTestAssertEnvelope(t, event, 7, profile.Key)
	}
	kinds := runTestEventKinds(events)
	if kinds[0] != domain.KindUserMessage {
		t.Fatalf("first event kind=%q want user_message", kinds[0])
	}
	if kinds[len(kinds)-1] != domain.KindRunFinished {
		t.Fatalf("last event kind=%q want run_finished", kinds[len(kinds)-1])
	}
	runTestRequireOrderedKinds(t, kinds,
		domain.KindRunStarted, domain.KindThinking, domain.KindText,
		domain.KindToolUse, domain.KindToolResult, domain.KindRunFinished)

	userEvents := runTestEventsOfKind(events, domain.KindUserMessage)
	if len(userEvents) != 1 {
		t.Fatalf("user_message events=%d want 1", len(userEvents))
	}
	if text, _ := userEvents[0].Detail["text"].(string); text != "检查运行时健康" {
		t.Fatalf("user_message text=%q", text)
	}
	if got := runTestDetailInt64(t, userEvents[0].Detail, "message_id"); got != result.UserMessageID {
		t.Fatalf("user_message message_id=%d want %d", got, result.UserMessageID)
	}

	runStarted := runTestEventsOfKind(events, domain.KindRunStarted)
	if len(runStarted) != 1 {
		t.Fatalf("run_started events=%d want 1", len(runStarted))
	}
	startedDetail := runStarted[0].Detail
	if startedDetail["profile_key"] != profile.Key || startedDetail["model"] != "deepseek-flash" {
		t.Fatalf("run_started detail=%+v", startedDetail)
	}
	if startedDetail["thinking"] != profile.Thinking || startedDetail["reasoning_effort"] != profile.ReasoningEffort {
		t.Fatalf("run_started thinking detail=%+v", startedDetail)
	}
	if got := runTestDetailInt64(t, startedDetail, "max_output_tokens"); got != int64(profile.MaxOutputTokens) {
		t.Fatalf("run_started max_output_tokens=%d", got)
	}

	toolUses := runTestEventsOfKind(events, domain.KindToolUse)
	if len(toolUses) != 1 {
		t.Fatalf("tool_use events=%d want 1", len(toolUses))
	}
	if toolUses[0].Detail["call_id"] != "call_1" || toolUses[0].Detail["tool"] != "test.echo" {
		t.Fatalf("tool_use detail=%+v", toolUses[0].Detail)
	}
	if args, _ := toolUses[0].Detail["args"].(string); args != `{"text":"README"}` {
		t.Fatalf("tool_use args=%q", args)
	}

	toolResults := runTestEventsOfKind(events, domain.KindToolResult)
	if len(toolResults) != 1 {
		t.Fatalf("tool_result events=%d want 1", len(toolResults))
	}
	if toolResults[0].Detail["status"] != "ok" || toolResults[0].Detail["call_id"] != "call_1" {
		t.Fatalf("tool_result detail=%+v", toolResults[0].Detail)
	}
	if resultText, _ := toolResults[0].Detail["result"].(string); resultText != "README" {
		t.Fatalf("tool_result result=%q want README", resultText)
	}
	if got := runTestDetailInt64(t, toolResults[0].Detail, "message_id"); got != toolMessage.ID {
		t.Fatalf("tool_result message_id=%d want %d", got, toolMessage.ID)
	}

	usages := runTestEventsOfKind(events, domain.KindUsage)
	if len(usages) != 2 {
		t.Fatalf("usage events=%d want 2", len(usages))
	}
	if got := runTestDetailInt64(t, usages[0].Detail, "prompt_tokens"); got != 10 {
		t.Fatalf("usage prompt_tokens=%d want 10", got)
	}
	runFinished := runTestEventsOfKind(events, domain.KindRunFinished)
	if len(runFinished) != 1 {
		t.Fatalf("run_finished events=%d want 1", len(runFinished))
	}
	finishedDetail := runFinished[0].Detail
	if finishedDetail["status"] != "done" || finishedDetail["terminal_reason"] != "completed" {
		t.Fatalf("run_finished detail=%+v", finishedDetail)
	}
	if got := runTestDetailInt64(t, finishedDetail, "turns"); got != 2 {
		t.Fatalf("run_finished turns=%d want 2", got)
	}
}

func TestAbortDuringStreamKeepsPartialOutput(t *testing.T) {
	profile := runTestProfile(t)
	state := runTestNewState(t, profile, 7)

	started := make(chan struct{})
	caller := &runFakeCaller{
		model:         "deepseek-flash",
		contextWindow: 100000,
		scripts: []runFakeScript{{
			events: []application.LLMStreamEvent{
				{ReasoningDelta: "先想一想"},
				{Delta: "部分输出"},
			},
			hold:    true,
			started: started,
		}},
	}
	service, sink, audit := runTestNewService(t, state, caller)
	if _, err := service.Start(context.Background(), 7, "检查运行时健康", nil); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	runTestAwaitSignal(t, started, "the stream to be consumed")

	abort, err := service.Abort(context.Background(), 7, nil)
	if err != nil {
		t.Fatalf("Abort returned error: %v", err)
	}
	if abort.Status != "stopping" {
		t.Fatalf("Abort status=%q want stopping", abort.Status)
	}
	runTestWaitIdle(t, service, 7)

	finished := runTestFinished(t, state)
	if finished.Status != domain.RunAborted || finished.TerminalReason != domain.ReasonAbortedStreaming {
		t.Fatalf("run finished %s/%s want aborted/aborted_streaming", finished.Status, finished.TerminalReason)
	}
	if finished.AbortCode != "user_abort" {
		t.Fatalf("abort_code=%q want user_abort", finished.AbortCode)
	}

	messages := state.messagesFor(7)
	if len(messages) != 2 {
		t.Fatalf("messages=%d want user + partial assistant: %+v", len(messages), messages)
	}
	partial := messages[1]
	if partial.Role != "assistant" || partial.Reasoning != "先想一想" {
		t.Fatalf("partial assistant=%+v", partial)
	}
	if len(partial.Parts) != 1 || partial.Parts[0].Text != "部分输出" {
		t.Fatalf("partial assistant parts=%+v want 部分输出", partial.Parts)
	}

	events := sink.snapshot()
	if len(runTestEventsOfKind(events, domain.KindRunFinished)) != 1 {
		t.Fatalf("run_finished events=%d want 1", len(runTestEventsOfKind(events, domain.KindRunFinished)))
	}
	if len(runTestEventsOfKind(events, domain.KindThinking)) == 0 {
		t.Fatal("no thinking event was published")
	}
	textEvents := runTestEventsOfKind(events, domain.KindText)
	if len(textEvents) == 0 {
		t.Fatal("no text event was published")
	}
	if textEvents[len(textEvents)-1].Detail["final"] != true {
		t.Fatalf("last text event final=%v want true", textEvents[len(textEvents)-1].Detail["final"])
	}
	finishedDetail := runTestEventsOfKind(events, domain.KindRunFinished)[0].Detail
	if finishedDetail["abort_code"] != "user_abort" || finishedDetail["status"] != "aborted" {
		t.Fatalf("run_finished detail=%+v", finishedDetail)
	}

	audits := audit.snapshot()
	if len(audits) != 1 {
		t.Fatalf("audit entries=%d want 1", len(audits))
	}
	if audits[0].Action != "agent.run.aborted" {
		t.Fatalf("audit action=%q want agent.run.aborted", audits[0].Action)
	}
	if audits[0].Metadata["abort_code"] != "user_abort" {
		t.Fatalf("audit abort_code=%v", audits[0].Metadata["abort_code"])
	}

	// Aborting again once idle is a no-op.
	idle, err := service.Abort(context.Background(), 7, nil)
	if err != nil || idle.Status != "idle" {
		t.Fatalf("second Abort=(%+v,%v) want idle", idle, err)
	}
}

func TestAbortDuringToolUsesAbortedTools(t *testing.T) {
	profile := runTestProfile(t)
	profile.AllowedTools = []string{"test.block"}
	state := runTestNewState(t, profile, 7)

	blocking := &runFakeBlockingTool{started: make(chan struct{})}
	caller := &runFakeCaller{
		model:         "deepseek-flash",
		contextWindow: 100000,
		scripts: []runFakeScript{{
			events: []application.LLMStreamEvent{{
				Done:         true,
				FinishReason: "tool_calls",
				ToolCalls: []domain.ToolCall{
					{ID: "call_1", Name: "test.block", Arguments: `{}`},
				},
			}},
		}},
	}
	service, sink, _ := runTestNewService(t, state, caller, runTestRegisterTool(blocking))
	if _, err := service.Start(context.Background(), 7, "执行阻塞工具", nil); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	runTestAwaitSignal(t, blocking.started, "the blocking tool to start")

	if _, err := service.Abort(context.Background(), 7, nil); err != nil {
		t.Fatalf("Abort returned error: %v", err)
	}
	runTestWaitIdle(t, service, 7)

	finished := runTestFinished(t, state)
	if finished.Status != domain.RunAborted || finished.TerminalReason != domain.ReasonAbortedTools {
		t.Fatalf("run finished %s/%s want aborted/aborted_tools", finished.Status, finished.TerminalReason)
	}
	if finished.AbortCode != "user_abort" {
		t.Fatalf("abort_code=%q want user_abort", finished.AbortCode)
	}
	if finished.LastToolName != "test.block" || !finished.LastToolPending {
		t.Fatalf("last tool name=%q pending=%v want test.block/true", finished.LastToolName, finished.LastToolPending)
	}

	messages := state.messagesFor(7)
	if len(messages) != 3 {
		t.Fatalf("messages=%d want user, assistant, tool: %+v", len(messages), messages)
	}
	if messages[1].Role != "assistant" || len(messages[1].ToolCalls) != 1 {
		t.Fatalf("assistant tool-call message=%+v", messages[1])
	}
	if messages[2].Role != "tool" || messages[2].ToolCallID != "call_1" {
		t.Fatalf("cancelled tool message=%+v", messages[2])
	}
	if len(messages[2].Parts) != 1 || !strings.Contains(messages[2].Parts[0].Text, "cancelled") {
		t.Fatalf("cancelled tool content=%+v want a cancelled description", messages[2].Parts)
	}

	toolResults := runTestEventsOfKind(sink.snapshot(), domain.KindToolResult)
	if len(toolResults) != 1 || toolResults[0].Detail["status"] != "cancelled" {
		t.Fatalf("tool_result events=%+v want one cancelled result", toolResults)
	}
}

func TestAbortDuringInitialHistoryLoadFinishesAborted(t *testing.T) {
	profile := runTestProfile(t)
	state := runTestNewState(t, profile, 7)

	historyStarted := make(chan struct{})
	var startedOnce sync.Once
	state.setListMessagesHook(func(ctx context.Context, _ int64) ([]domain.Message, error) {
		startedOnce.Do(func() { close(historyStarted) })
		<-ctx.Done()
		return nil, ctx.Err()
	})

	caller := &runFakeCaller{model: "deepseek-flash", contextWindow: 100000}
	service, sink, audit := runTestNewService(t, state, caller)
	if _, err := service.Start(context.Background(), 7, "历史读取期间中止", nil); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	runTestAwaitSignal(t, historyStarted, "the initial history load")

	abort, err := service.Abort(context.Background(), 7, nil)
	if err != nil {
		t.Fatalf("Abort returned error: %v", err)
	}
	if abort.Status != "stopping" {
		t.Fatalf("Abort status=%q want stopping", abort.Status)
	}
	runTestWaitIdle(t, service, 7)

	// A stop that lands while the runner is loading the history must terminate
	// the run as aborted, never as a model/internal failure.
	finished := runTestFinished(t, state)
	if finished.Status != domain.RunAborted || finished.TerminalReason != domain.ReasonAborted {
		t.Fatalf("run finished %s/%s want aborted/aborted", finished.Status, finished.TerminalReason)
	}
	if finished.AbortCode != "user_abort" {
		t.Fatalf("abort_code=%q want user_abort", finished.AbortCode)
	}
	if finished.ErrorCode != "" || finished.ErrorMessage != "" {
		t.Fatalf("aborted run error_code=%q error_message=%q want empty", finished.ErrorCode, finished.ErrorMessage)
	}
	if finished.Turns != 0 {
		t.Fatalf("run turns=%d want 0 (the abort landed before the first model turn)", finished.Turns)
	}
	if calls := caller.callCount(); calls != 0 {
		t.Fatalf("LLM calls=%d want 0 (the abort landed before the model call)", calls)
	}

	messages := state.messagesFor(7)
	if len(messages) != 1 || messages[0].Role != "user" {
		t.Fatalf("messages=%+v want only the user message", messages)
	}
	if ids := state.completedMessages(); len(ids) != 0 {
		t.Fatalf("CompleteAssistant calls=%v want none before streaming started", ids)
	}

	runFinished := runTestEventsOfKind(sink.snapshot(), domain.KindRunFinished)
	if len(runFinished) != 1 {
		t.Fatalf("run_finished events=%d want 1", len(runFinished))
	}
	if runFinished[0].Detail["status"] != "aborted" || runFinished[0].Detail["abort_code"] != "user_abort" {
		t.Fatalf("run_finished detail=%+v", runFinished[0].Detail)
	}
	if audits := audit.snapshot(); len(audits) != 1 || audits[0].Action != "agent.run.aborted" {
		t.Fatalf("audit entries=%+v want one agent.run.aborted", audits)
	}
}

func TestAbortDuringStreamSetupCompletesAssistantMessage(t *testing.T) {
	profile := runTestProfile(t)
	state := runTestNewState(t, profile, 7)

	setupStarted := make(chan struct{})
	var startedOnce sync.Once
	caller := &runFakeCaller{
		model:         "deepseek-flash",
		contextWindow: 100000,
		scripts: []runFakeScript{{
			wait: func(ctx context.Context) error {
				startedOnce.Do(func() { close(setupStarted) })
				<-ctx.Done()
				return ctx.Err()
			},
		}},
	}
	service, sink, _ := runTestNewService(t, state, caller)
	if _, err := service.Start(context.Background(), 7, "流建立期间中止", nil); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	runTestAwaitSignal(t, setupStarted, "the model stream setup")

	abort, err := service.Abort(context.Background(), 7, nil)
	if err != nil || abort.Status != "stopping" {
		t.Fatalf("Abort=(%+v,%v) want stopping", abort, err)
	}
	runTestWaitIdle(t, service, 7)

	finished := runTestFinished(t, state)
	if finished.Status != domain.RunAborted || finished.TerminalReason != domain.ReasonAbortedStreaming {
		t.Fatalf("run finished %s/%s want aborted/aborted_streaming", finished.Status, finished.TerminalReason)
	}
	if finished.AbortCode != "user_abort" {
		t.Fatalf("abort_code=%q want user_abort", finished.AbortCode)
	}

	// The placeholder created before ChatStream must be completed (with empty
	// content) so the aborted run does not leave a dangling row behind.
	messages := state.messagesFor(7)
	if len(messages) != 2 || messages[1].Role != "assistant" {
		t.Fatalf("messages=%+v want user + assistant placeholder", messages)
	}
	completed := state.completedMessages()
	if len(completed) != 1 || completed[0] != messages[1].ID {
		t.Fatalf("CompleteAssistant calls=%v want the placeholder %d completed", completed, messages[1].ID)
	}
	if len(messages[1].Parts) != 0 || messages[1].Reasoning != "" {
		t.Fatalf("aborted assistant message=%+v want empty content", messages[1])
	}
	if events := runTestEventsOfKind(sink.snapshot(), domain.KindRunFinished); len(events) != 1 {
		t.Fatalf("run_finished events=%d want 1", len(events))
	}
}

func TestAbortMissingSessionReturnsNotFound(t *testing.T) {
	profile := runTestProfile(t)
	state := runTestNewState(t, profile, 7)
	service, _, audit := runTestNewService(t, state, &runFakeCaller{})

	_, err := service.Abort(context.Background(), 99, nil)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Abort(missing session) error=%v want domain.ErrNotFound", err)
	}
	if audits := audit.snapshot(); len(audits) != 0 {
		t.Fatalf("Abort(missing session) wrote audit entries=%+v want none", audits)
	}
}

func TestStartSuccessorRegistrationSurvivesPredecessorCleanup(t *testing.T) {
	profile := runTestProfile(t)
	state := runTestNewState(t, profile, 7, 8)

	firstFinishEntered := make(chan struct{})
	releaseFirstFinish := make(chan struct{})
	var finishOnce sync.Once
	state.setFinishHook(func(ctx context.Context, runID int64) error {
		if runID != 1 {
			return nil
		}
		finishOnce.Do(func() { close(firstFinishEntered) })
		select {
		case <-releaseFirstFinish:
		case <-ctx.Done():
		}
		return nil
	})

	successorStreamStarted := make(chan struct{})
	caller := &runFakeCaller{
		model:         "deepseek-flash",
		contextWindow: 100000,
		scripts: []runFakeScript{
			{events: []application.LLMStreamEvent{{Done: true, FinishReason: "stop"}}},
			{hold: true, started: successorStreamStarted},
			{hold: true},
		},
	}
	service, _, _ := runTestNewService(t, state, caller, runTestMaxConcurrentRuns(2))

	first, err := service.Start(context.Background(), 7, "第一轮", nil)
	if err != nil {
		t.Fatalf("first Start returned error: %v", err)
	}
	runTestAwaitSignal(t, firstFinishEntered, "the first run's terminal write")

	// The first run's terminal row is already written, so the session's
	// single-flight slot is free while its runner goroutine still holds its
	// in-process registry entry.
	successor, err := service.Start(context.Background(), 7, "第二轮", nil)
	if err != nil {
		t.Fatalf("successor Start returned error: %v", err)
	}
	if successor.RunID == first.RunID {
		t.Fatalf("successor run id=%d collides with the predecessor", successor.RunID)
	}
	runTestAwaitSignal(t, successorStreamStarted, "the successor run's stream")

	// Let the first runner finish. Its deferred cleanup now runs while the
	// successor is registered.
	close(releaseFirstFinish)

	// A third run is only admitted once the first runner released its
	// semaphore slot, which happens after its registry cleanup. Capacity is
	// therefore a deterministic signal that the cleanup already ran.
	deadline := time.Now().Add(5 * time.Second)
	var probe domain.StartResult
	for {
		probe, err = service.Start(context.Background(), 8, "探针", nil)
		if err == nil {
			break
		}
		if !errors.Is(err, domain.ErrCapacity) {
			t.Fatalf("capacity probe Start error=%v want ErrCapacity or success", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the predecessor to release its capacity slot")
		}
		time.Sleep(time.Millisecond)
	}

	// The predecessor's cleanup must not have removed the successor's entry.
	abort, err := service.Abort(context.Background(), 7, nil)
	if err != nil {
		t.Fatalf("successor Abort returned error: %v", err)
	}
	if abort.Status != "stopping" {
		t.Fatalf("successor Abort status=%q want stopping (its registry entry was deleted by the predecessor)", abort.Status)
	}
	if _, err := service.Abort(context.Background(), 8, nil); err != nil {
		t.Fatalf("probe Abort returned error: %v", err)
	}
	runTestWaitIdle(t, service, 7)
	runTestWaitIdle(t, service, 8)

	finished := make(map[int64]domain.RunResult)
	for _, entry := range state.finishedResults() {
		finished[entry.runID] = entry.result
	}
	firstResult, ok := finished[first.RunID]
	if !ok || firstResult.Status != domain.RunDone || firstResult.TerminalReason != domain.ReasonCompleted {
		t.Fatalf("predecessor result=%+v ok=%v want done/completed", firstResult, ok)
	}
	successorResult, ok := finished[successor.RunID]
	if !ok || successorResult.Status != domain.RunAborted || successorResult.TerminalReason != domain.ReasonAbortedStreaming {
		t.Fatalf("successor result=%+v ok=%v want aborted/aborted_streaming", successorResult, ok)
	}
	if successorResult.AbortCode != "user_abort" {
		t.Fatalf("successor abort_code=%q want user_abort", successorResult.AbortCode)
	}
	if _, ok := finished[probe.RunID]; !ok {
		t.Fatalf("probe run %d has no terminal result", probe.RunID)
	}
}

func TestMaxTurnsEmitsBudgetStop(t *testing.T) {
	profile := runTestProfile(t)
	profile.MaxTurns = 1
	state := runTestNewState(t, profile, 7)

	caller := &runFakeCaller{
		model:         "deepseek-flash",
		contextWindow: 100000,
		scripts: []runFakeScript{{
			events: []application.LLMStreamEvent{{
				Done:         true,
				FinishReason: "tool_calls",
				ToolCalls: []domain.ToolCall{
					{ID: "call_1", Name: "test.echo", Arguments: `{"text":"hi"}`},
				},
			}},
		}},
	}
	service, sink, _ := runTestNewService(t, state, caller)
	if _, err := service.Start(context.Background(), 7, "循环工具", nil); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	runTestWaitIdle(t, service, 7)

	finished := runTestFinished(t, state)
	if finished.Status != domain.RunDone || finished.TerminalReason != domain.ReasonMaxTurns {
		t.Fatalf("run finished %s/%s want done/max_turns", finished.Status, finished.TerminalReason)
	}
	if finished.Turns != 1 {
		t.Fatalf("run turns=%d want 1", finished.Turns)
	}
	if calls := caller.callCount(); calls != 1 {
		t.Fatalf("LLM calls=%d want 1 (max_turns stops before a second call)", calls)
	}

	decisions := runTestEventsOfKind(sink.snapshot(), domain.KindDecision)
	if len(decisions) != 1 {
		t.Fatalf("decision events=%d want 1: %+v", len(decisions), decisions)
	}
	if decisions[0].Detail["decision"] != "budget_stop" || decisions[0].Detail["reason"] != "max_turns" {
		t.Fatalf("decision detail=%+v want budget_stop/max_turns", decisions[0].Detail)
	}
}

func TestToolErrorDoesNotFailRun(t *testing.T) {
	profile := runTestProfile(t)
	state := runTestNewState(t, profile, 7)

	const answer = "工具失败，运行继续"
	caller := &runFakeCaller{
		model:         "deepseek-flash",
		contextWindow: 100000,
		scripts: []runFakeScript{
			{events: []application.LLMStreamEvent{{
				Done:         true,
				FinishReason: "tool_calls",
				ToolCalls: []domain.ToolCall{
					{ID: "call_1", Name: "test.fail", Arguments: `{"mode":"error"}`},
				},
			}}},
			{events: []application.LLMStreamEvent{
				{Delta: answer},
				{Done: true, FinishReason: "stop"},
			}},
		},
	}
	service, sink, _ := runTestNewService(t, state, caller)
	if _, err := service.Start(context.Background(), 7, "触发工具错误", nil); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	runTestWaitIdle(t, service, 7)

	finished := runTestFinished(t, state)
	if finished.Status != domain.RunDone || finished.TerminalReason != domain.ReasonCompleted {
		t.Fatalf("run finished %s/%s want done/completed", finished.Status, finished.TerminalReason)
	}
	if finished.Turns != 2 {
		t.Fatalf("run turns=%d want 2", finished.Turns)
	}

	messages := state.messagesFor(7)
	if len(messages) != 4 {
		t.Fatalf("messages=%d want user, assistant, tool, assistant: %+v", len(messages), messages)
	}
	if messages[2].Role != "tool" || len(messages[2].Parts) != 1 || messages[2].Parts[0].Text == "" {
		t.Fatalf("tool error message=%+v want a non-empty result", messages[2])
	}
	if messages[3].Role != "assistant" || messages[3].Parts[0].Text != answer {
		t.Fatalf("final assistant=%+v want %q", messages[3], answer)
	}

	toolResults := runTestEventsOfKind(sink.snapshot(), domain.KindToolResult)
	if len(toolResults) != 1 || toolResults[0].Detail["status"] != "error" {
		t.Fatalf("tool_result events=%+v want one error result", toolResults)
	}
}

func TestStartRejectsDisabledProfileAndEmptyMessage(t *testing.T) {
	t.Run("empty message", func(t *testing.T) {
		profile := runTestProfile(t)
		state := runTestNewState(t, profile, 7)
		service, _, _ := runTestNewService(t, state, &runFakeCaller{})
		if _, err := service.Start(context.Background(), 7, "   \n\t ", nil); !errors.Is(err, domain.ErrEmptyMessage) {
			t.Fatalf("Start error=%v want ErrEmptyMessage", err)
		}
		if len(state.created) != 0 {
			t.Fatalf("a turn was created for an empty message: %+v", state.created)
		}
	})

	t.Run("disabled profile", func(t *testing.T) {
		profile := runTestProfile(t)
		profile.Enabled = false
		state := runTestNewState(t, profile, 7)
		service, _, _ := runTestNewService(t, state, &runFakeCaller{})
		if _, err := service.Start(context.Background(), 7, "你好", nil); !errors.Is(err, domain.ErrProfileDisabled) {
			t.Fatalf("Start error=%v want ErrProfileDisabled", err)
		}
		if len(state.created) != 0 {
			t.Fatalf("a turn was created for a disabled profile: %+v", state.created)
		}
	})

	t.Run("unknown session", func(t *testing.T) {
		profile := runTestProfile(t)
		state := runTestNewState(t, profile)
		service, _, _ := runTestNewService(t, state, &runFakeCaller{})
		if _, err := service.Start(context.Background(), 99, "你好", nil); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("Start error=%v want ErrNotFound", err)
		}
	})
}

func TestStartMapsNotConfiguredAndProtocol(t *testing.T) {
	for _, wantErr := range []error{domain.ErrLLMNotConfigured, domain.ErrToolsUnsupported} {
		t.Run(wantErr.Error(), func(t *testing.T) {
			profile := runTestProfile(t)
			state := runTestNewState(t, profile, 7)
			service, _, _ := runTestNewService(t, state, &runFakeCaller{}, runTestRuntimeError(wantErr))
			_, err := service.Start(context.Background(), 7, "你好", nil)
			if !errors.Is(err, wantErr) {
				t.Fatalf("Start error=%v want %v", err, wantErr)
			}
			if len(state.created) != 0 {
				t.Fatalf("a turn was created after a Prepare failure: %+v", state.created)
			}
		})
	}
}

func TestStartCapacityAndRunActive(t *testing.T) {
	t.Run("capacity", func(t *testing.T) {
		profile := runTestProfile(t)
		state := runTestNewState(t, profile, 7, 8)
		caller := &runFakeCaller{
			model:         "deepseek-flash",
			contextWindow: 100000,
			scripts:       []runFakeScript{{hold: true}},
		}
		service, _, _ := runTestNewService(t, state, caller, runTestMaxConcurrentRuns(1))
		if _, err := service.Start(context.Background(), 7, "占住额度", nil); err != nil {
			t.Fatalf("first Start returned error: %v", err)
		}
		if _, err := service.Start(context.Background(), 8, "第二个", nil); !errors.Is(err, domain.ErrCapacity) {
			t.Fatalf("second Start error=%v want ErrCapacity", err)
		}
		if _, err := service.Abort(context.Background(), 7, nil); err != nil {
			t.Fatalf("Abort returned error: %v", err)
		}
		runTestWaitIdle(t, service, 7)
	})

	t.Run("run active", func(t *testing.T) {
		profile := runTestProfile(t)
		state := runTestNewState(t, profile, 7)
		caller := &runFakeCaller{
			model:         "deepseek-flash",
			contextWindow: 100000,
			scripts:       []runFakeScript{{hold: true}},
		}
		service, _, _ := runTestNewService(t, state, caller, runTestMaxConcurrentRuns(4))
		if _, err := service.Start(context.Background(), 7, "第一条", nil); err != nil {
			t.Fatalf("first Start returned error: %v", err)
		}
		if _, err := service.Start(context.Background(), 7, "第二条", nil); !errors.Is(err, domain.ErrRunActive) {
			t.Fatalf("second Start error=%v want ErrRunActive", err)
		}
		if _, err := service.Abort(context.Background(), 7, nil); err != nil {
			t.Fatalf("Abort returned error: %v", err)
		}
		runTestWaitIdle(t, service, 7)
	})
}

func TestRecoverInterrupted(t *testing.T) {
	profile := runTestProfile(t)
	state := runTestNewState(t, profile, 7)
	state.interrupted = 2
	service, _, _ := runTestNewService(t, state, &runFakeCaller{})
	count, err := service.RecoverInterrupted(context.Background())
	if err != nil {
		t.Fatalf("RecoverInterrupted returned error: %v", err)
	}
	if count != 2 {
		t.Fatalf("RecoverInterrupted count=%d want 2", count)
	}

	broken := errors.New("run service test: recovery failed")
	state.interruptedErr = broken
	if _, err := service.RecoverInterrupted(context.Background()); !errors.Is(err, broken) {
		t.Fatalf("RecoverInterrupted error=%v want %v", err, broken)
	}
}

func TestModelErrorTerminalMapping(t *testing.T) {
	cases := []struct {
		name         string
		finishReason string
		wantStatus   domain.RunStatus
		wantReason   domain.TerminalReason
		wantCode     string
	}{
		{name: "stop", finishReason: "stop", wantStatus: domain.RunDone, wantReason: domain.ReasonCompleted},
		{name: "empty", finishReason: "", wantStatus: domain.RunDone, wantReason: domain.ReasonCompleted},
		{name: "length", finishReason: "length", wantStatus: domain.RunFailed, wantReason: domain.ReasonModelError, wantCode: "finish_reason=length"},
		{name: "aborted", finishReason: "aborted", wantStatus: domain.RunFailed, wantReason: domain.ReasonModelError, wantCode: "finish_reason=aborted"},
		{name: "insufficient system resource", finishReason: "insufficient_system_resource", wantStatus: domain.RunFailed, wantReason: domain.ReasonModelError, wantCode: "finish_reason=insufficient_system_resource"},
		{name: "tool_calls without calls", finishReason: "tool_calls", wantStatus: domain.RunFailed, wantReason: domain.ReasonModelError, wantCode: "finish_reason=tool_calls"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			profile := runTestProfile(t)
			state := runTestNewState(t, profile, 7)
			caller := &runFakeCaller{
				model:         "deepseek-flash",
				contextWindow: 100000,
				scripts: []runFakeScript{{
					events: []application.LLMStreamEvent{{
						Done:         true,
						FinishReason: tc.finishReason,
					}},
				}},
			}
			service, _, _ := runTestNewService(t, state, caller)
			if _, err := service.Start(context.Background(), 7, "终态映射", nil); err != nil {
				t.Fatalf("Start returned error: %v", err)
			}
			runTestWaitIdle(t, service, 7)

			finished := runTestFinished(t, state)
			if finished.Status != tc.wantStatus || finished.TerminalReason != tc.wantReason {
				t.Fatalf("run finished %s/%s want %s/%s", finished.Status, finished.TerminalReason, tc.wantStatus, tc.wantReason)
			}
			if finished.ErrorCode != tc.wantCode {
				t.Fatalf("error_code=%q want %q", finished.ErrorCode, tc.wantCode)
			}
			if finished.TerminalReason == "" {
				t.Fatal("terminal_reason must never be empty")
			}
		})
	}

	t.Run("stream error carries the coded error", func(t *testing.T) {
		profile := runTestProfile(t)
		state := runTestNewState(t, profile, 7)
		caller := &runFakeCaller{
			model:         "deepseek-flash",
			contextWindow: 100000,
			scripts: []runFakeScript{{
				err: &runFakeCodedError{code: "llm_timeout", message: "upstream timed out"},
			}},
		}
		service, _, _ := runTestNewService(t, state, caller)
		if _, err := service.Start(context.Background(), 7, "上游错误", nil); err != nil {
			t.Fatalf("Start returned error: %v", err)
		}
		runTestWaitIdle(t, service, 7)

		finished := runTestFinished(t, state)
		if finished.Status != domain.RunFailed || finished.TerminalReason != domain.ReasonModelError {
			t.Fatalf("run finished %s/%s want failed/model_error", finished.Status, finished.TerminalReason)
		}
		if finished.ErrorCode != "llm_timeout" {
			t.Fatalf("error_code=%q want llm_timeout", finished.ErrorCode)
		}
	})
}

func TestHardTimeoutAbortsRun(t *testing.T) {
	profile := runTestProfile(t)
	profile.RunSeconds = 0         // disable the soft budget
	profile.HardTimeoutSeconds = 0 // fall back to the dependency value
	state := runTestNewState(t, profile, 7)
	caller := &runFakeCaller{
		model:         "deepseek-flash",
		contextWindow: 100000,
		scripts:       []runFakeScript{{hold: true}},
	}
	service, _, _ := runTestNewService(t, state, caller, runTestHardTimeout(20*time.Millisecond))
	if _, err := service.Start(context.Background(), 7, "硬超时", nil); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	runTestWaitIdle(t, service, 7)

	finished := runTestFinished(t, state)
	if finished.Status != domain.RunAborted || finished.TerminalReason != domain.ReasonRunHardTimeout {
		t.Fatalf("run finished %s/%s want aborted/run_hard_timeout", finished.Status, finished.TerminalReason)
	}
	if finished.AbortCode != "run_hard_timeout" {
		t.Fatalf("abort_code=%q want run_hard_timeout", finished.AbortCode)
	}
}

func TestShutdownAbortsRunningRuns(t *testing.T) {
	profile := runTestProfile(t)
	state := runTestNewState(t, profile, 7)
	caller := &runFakeCaller{
		model:         "deepseek-flash",
		contextWindow: 100000,
		scripts:       []runFakeScript{{hold: true}},
	}
	service, _, _ := runTestNewService(t, state, caller)
	if _, err := service.Start(context.Background(), 7, "关闭进程", nil); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	service.Shutdown(ctx)

	finished := runTestFinished(t, state)
	if finished.Status != domain.RunAborted || finished.TerminalReason != domain.ReasonShutdown {
		t.Fatalf("run finished %s/%s want aborted/shutdown", finished.Status, finished.TerminalReason)
	}
	if finished.AbortCode != "shutdown" {
		t.Fatalf("abort_code=%q want shutdown", finished.AbortCode)
	}
}

func TestToolResultTruncatedBeforeContext(t *testing.T) {
	profile := runTestProfile(t)
	profile.AllowedTools = []string{"test.huge"}
	state := runTestNewState(t, profile, 7)

	huge := strings.Repeat("数", 40000) // >32KB in UTF-8 bytes
	hugeTool := &runFakeHugeTool{content: huge}
	caller := &runFakeCaller{
		model:         "deepseek-flash",
		contextWindow: 120000,
		scripts: []runFakeScript{
			{events: []application.LLMStreamEvent{{
				Done:         true,
				FinishReason: "tool_calls",
				ToolCalls: []domain.ToolCall{
					{ID: "call_1", Name: "test.huge", Arguments: `{}`},
				},
			}}},
			{events: []application.LLMStreamEvent{
				{Done: true, FinishReason: "stop"},
			}},
		},
	}
	service, sink, _ := runTestNewService(t, state, caller, runTestRegisterTool(hugeTool))
	if _, err := service.Start(context.Background(), 7, "大结果", nil); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	runTestWaitIdle(t, service, 7)

	finished := runTestFinished(t, state)
	if finished.Status != domain.RunDone || finished.TerminalReason != domain.ReasonCompleted {
		t.Fatalf("run finished %s/%s want done/completed", finished.Status, finished.TerminalReason)
	}

	messages := state.messagesFor(7)
	if len(messages) != 4 {
		t.Fatalf("messages=%d want 4", len(messages))
	}
	toolText := messages[2].Parts[0].Text
	if len(toolText) > 32*1024 {
		t.Fatalf("tool result persisted %d bytes want <= %d", len(toolText), 32*1024)
	}
	if !strings.Contains(toolText, "...[truncated]...") {
		t.Fatalf("tool result is missing the truncation marker")
	}
	if !strings.HasPrefix(toolText, strings.Repeat("数", 10)) {
		t.Fatal("tool result lost its head")
	}

	toolResults := runTestEventsOfKind(sink.snapshot(), domain.KindToolResult)
	if len(toolResults) != 1 {
		t.Fatalf("tool_result events=%d want 1", len(toolResults))
	}
	preview, _ := toolResults[0].Detail["result"].(string)
	if len(preview) > 2048 {
		t.Fatalf("tool_result preview length=%d bytes exceeds 2048", len(preview))
	}
	if toolResults[0].Detail["result_truncated"] != true {
		t.Fatalf("tool_result result_truncated=%v want true", toolResults[0].Detail["result_truncated"])
	}
}

func TestSoftBudgetStopsRun(t *testing.T) {
	profile := runTestProfile(t)
	state := runTestNewState(t, profile, 7)
	caller := &runFakeCaller{
		model:         "deepseek-flash",
		contextWindow: 100000,
		scripts: []runFakeScript{{
			events: []application.LLMStreamEvent{{
				Done:         true,
				FinishReason: "tool_calls",
				ToolCalls: []domain.ToolCall{
					{ID: "call_1", Name: "test.echo", Arguments: `{"text":"hi"}`},
				},
			}},
		}},
	}

	// The injected clock jumps past run_seconds once the first model stream has
	// started, so the soft budget check between turns stops the run with
	// done/timeout after exactly one turn.
	base := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time {
		if caller.callCount() > 0 {
			return base.Add(2 * time.Hour)
		}
		return base
	}
	service, sink, _ := runTestNewService(t, state, caller, runTestClock(clock))
	if _, err := service.Start(context.Background(), 7, "软预算", nil); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	runTestWaitIdle(t, service, 7)

	finished := runTestFinished(t, state)
	if finished.Status != domain.RunDone || finished.TerminalReason != domain.ReasonTimeout {
		t.Fatalf("run finished %s/%s want done/timeout", finished.Status, finished.TerminalReason)
	}
	if finished.Turns != 1 {
		t.Fatalf("run turns=%d want 1", finished.Turns)
	}
	decisions := runTestEventsOfKind(sink.snapshot(), domain.KindDecision)
	if len(decisions) != 1 {
		t.Fatalf("decision events=%d want 1", len(decisions))
	}
	if decisions[0].Detail["decision"] != "budget_stop" || decisions[0].Detail["reason"] != "timeout" {
		t.Fatalf("decision detail=%+v want budget_stop/timeout", decisions[0].Detail)
	}
}

func TestToolDeniedEmitsDecision(t *testing.T) {
	profile := runTestProfile(t)
	profile.AllowedTools = []string{"test.echo"} // test.fail is registered but not allowed
	state := runTestNewState(t, profile, 7)

	const answer = "工具未授权，但运行继续"
	caller := &runFakeCaller{
		model:         "deepseek-flash",
		contextWindow: 100000,
		scripts: []runFakeScript{
			{events: []application.LLMStreamEvent{{
				Done:         true,
				FinishReason: "tool_calls",
				ToolCalls: []domain.ToolCall{
					{ID: "call_1", Name: "test.fail", Arguments: `{"mode":"error"}`},
				},
			}}},
			{events: []application.LLMStreamEvent{
				{Delta: answer},
				{Done: true, FinishReason: "stop"},
			}},
		},
	}
	service, sink, _ := runTestNewService(t, state, caller)
	if _, err := service.Start(context.Background(), 7, "越权工具", nil); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	runTestWaitIdle(t, service, 7)

	finished := runTestFinished(t, state)
	if finished.Status != domain.RunDone || finished.TerminalReason != domain.ReasonCompleted {
		t.Fatalf("run finished %s/%s want done/completed", finished.Status, finished.TerminalReason)
	}

	events := sink.snapshot()
	toolResults := runTestEventsOfKind(events, domain.KindToolResult)
	if len(toolResults) != 1 || toolResults[0].Detail["status"] != "denied" {
		t.Fatalf("tool_result events=%+v want one denied result", toolResults)
	}
	decisions := runTestEventsOfKind(events, domain.KindDecision)
	if len(decisions) != 1 || decisions[0].Detail["decision"] != "tool_denied" {
		t.Fatalf("decision events=%+v want one tool_denied", decisions)
	}

	// The denied tool call still gets a tool message so the history keeps the
	// tool_calls <-> tool pairing valid.
	messages := state.messagesFor(7)
	if len(messages) != 4 {
		t.Fatalf("messages=%d want 4", len(messages))
	}
	if messages[2].Role != "tool" || messages[2].ToolCallID != "call_1" {
		t.Fatalf("denied tool message=%+v", messages[2])
	}
	if messages[3].Role != "assistant" || messages[3].Parts[0].Text != answer {
		t.Fatalf("final assistant=%+v want %q", messages[3], answer)
	}
}

// TestToolAuditCarriesOriginatingRequestID pins the T19 contract: the runner
// goroutine does not inherit the HTTP request context, so Start must copy the
// request ID onto the run context; otherwise agent.tool.invoked audit rows lose
// the correlation their sibling request-scoped audit rows already carry.
func TestToolAuditCarriesOriginatingRequestID(t *testing.T) {
	const requestID = "req_tool_audit_1"

	profile := runTestProfile(t)
	state := runTestNewState(t, profile, 7)
	caller := &runFakeCaller{
		model:         "deepseek-flash",
		contextWindow: 100000,
		scripts: []runFakeScript{
			{events: []application.LLMStreamEvent{{
				Done:         true,
				FinishReason: "tool_calls",
				ToolCalls: []domain.ToolCall{
					{ID: "call_1", Name: "test.echo", Arguments: `{"text":"README"}`},
				},
			}}},
			{events: []application.LLMStreamEvent{{Done: true, FinishReason: "stop"}}},
		},
	}
	service, _, audit := runTestNewService(t, state, caller)

	ctx := platform.WithRequestID(context.Background(), requestID)
	if _, err := service.Start(ctx, 7, "审计请求关联", nil); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	runTestWaitIdle(t, service, 7)

	ids := audit.requestIDsByAction(application.ActionToolInvoked)
	if len(ids) != 1 {
		t.Fatalf("agent.tool.invoked audit entries=%d want 1", len(ids))
	}
	if ids[0] != requestID {
		t.Fatalf("agent.tool.invoked request_id=%q want %q", ids[0], requestID)
	}
}
