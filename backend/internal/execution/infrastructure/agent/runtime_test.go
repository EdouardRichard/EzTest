package agent_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	executionagent "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/agent"
)

// fakeSeeder records the idempotent profile seeds.
type fakeSeeder struct {
	seeds []agentdomain.AgentProfile
	err   error
}

func (f *fakeSeeder) EnsureBuiltin(_ context.Context, profile agentdomain.AgentProfile) (bool, error) {
	f.seeds = append(f.seeds, profile)
	return true, f.err
}

// fakeProfiles resolves a profile by key.
type fakeProfiles struct {
	byKey     map[string]agentdomain.AgentProfile
	err       error
	requested []string
}

func (f *fakeProfiles) GetByKey(_ context.Context, key string) (agentdomain.AgentProfile, error) {
	f.requested = append(f.requested, key)
	if f.err != nil {
		return agentdomain.AgentProfile{}, f.err
	}
	profile, ok := f.byKey[key]
	if !ok {
		return agentdomain.AgentProfile{}, agentdomain.ErrNotFound
	}
	return profile, nil
}

// fakeSessions records session creation and serves session details and channel
// pages.
type fakeSessions struct {
	createProfileID int64
	createTitle     string
	createActor     *int64
	createOptions   agentapp.SessionOptions
	session         agentdomain.Session
	createErr       error

	details map[int64]agentdomain.SessionDetail
	getErr  error

	list        []agentdomain.Session
	listErr     error
	listChannel string
	listBefore  int64
	listLimit   int
}

func (f *fakeSessions) CreateWithOptions(_ context.Context, profileID int64, title string, actor *int64, opts agentapp.SessionOptions) (agentdomain.Session, error) {
	f.createProfileID, f.createTitle, f.createActor, f.createOptions = profileID, title, actor, opts
	if f.createErr != nil {
		return agentdomain.Session{}, f.createErr
	}
	return f.session, nil
}

func (f *fakeSessions) Get(_ context.Context, id int64) (agentdomain.SessionDetail, error) {
	if f.getErr != nil {
		return agentdomain.SessionDetail{}, f.getErr
	}
	detail, ok := f.details[id]
	if !ok {
		return agentdomain.SessionDetail{}, agentdomain.ErrNotFound
	}
	return detail, nil
}

func (f *fakeSessions) ListByChannel(_ context.Context, channel string, beforeSeq int64, limit int) ([]agentdomain.Session, bool, error) {
	f.listChannel, f.listBefore, f.listLimit = channel, beforeSeq, limit
	if f.listErr != nil {
		return nil, false, f.listErr
	}
	return f.list, false, nil
}

// fakeRuns records StartWithScope and Abort calls.
type fakeRuns struct {
	startSession int64
	startParts   []agentdomain.Part
	startActor   *int64
	startScope   agentdomain.RunScope
	startRunID   int64
	startErr     error

	abortSession int64
	abortActor   *int64
	abortErr     error
}

func (f *fakeRuns) StartWithScope(_ context.Context, sessionID int64, parts []agentdomain.Part, actor *int64, scope agentdomain.RunScope) (agentdomain.StartResult, error) {
	f.startSession, f.startParts, f.startActor, f.startScope = sessionID, parts, actor, scope
	if f.startErr != nil {
		return agentdomain.StartResult{}, f.startErr
	}
	return agentdomain.StartResult{RunID: f.startRunID}, nil
}

func (f *fakeRuns) Abort(_ context.Context, sessionID int64, actor *int64) (agentdomain.AbortResult, error) {
	f.abortSession, f.abortActor = sessionID, actor
	if f.abortErr != nil {
		return agentdomain.AbortResult{}, f.abortErr
	}
	return agentdomain.AbortResult{Status: "stopping"}, nil
}

// fakeCaseContext is the resume read-model source the runtime prompt consumes.
type fakeCaseContext struct {
	view executionapp.CaseContextView
	err  error
}

func (f *fakeCaseContext) CaseContext(_ context.Context, _ int64) (executionapp.CaseContextView, error) {
	if f.err != nil {
		return executionapp.CaseContextView{}, f.err
	}
	return f.view, nil
}

// runtimeFixture wires the adapter with fakes and a ready case run request.
type runtimeFixture struct {
	seeder   *fakeSeeder
	profiles *fakeProfiles
	sessions *fakeSessions
	runs     *fakeRuns
	runtime  *executionagent.AgentRuntime
}

func newRuntimeFixture() *runtimeFixture {
	return &runtimeFixture{
		seeder: &fakeSeeder{},
		profiles: &fakeProfiles{byKey: map[string]agentdomain.AgentProfile{
			executiondomain.ExecutionProfileKey: {ID: 5, Key: executiondomain.ExecutionProfileKey, Enabled: true},
		}},
		sessions: &fakeSessions{
			session: agentdomain.Session{ID: 88},
			details: map[int64]agentdomain.SessionDetail{},
		},
		runs: &fakeRuns{startRunID: 99},
	}
}

func (f *runtimeFixture) adapter() *executionagent.AgentRuntime {
	if f.runtime == nil {
		f.runtime = executionagent.NewAgentRuntime(f.profiles, f.seeder, f.sessions, f.runs)
	}
	return f.runtime
}

func caseRunRequest() executionapp.CaseRunRequest {
	interrupted := 2
	return executionapp.CaseRunRequest{
		VersionID:     7,
		BatchID:       8,
		CaseRunID:     9,
		CaseNodeID:    10,
		CaseNodeTitle: "登录",
		Snapshot: executiondomain.CaseSnapshot{
			Node: executiondomain.CaseSnapshotNode{ID: 10, Title: "登录", Priority: "P0"},
			Steps: []executiondomain.CaseSnapshotStep{
				{Seq: 1, Action: "打开登录页", Expected: "显示登录表单"},
				{Seq: 2, Action: "输入账号密码", Expected: "登录成功"},
			},
			Assertions: []executiondomain.CaseSnapshotAssertion{
				{ID: 77, StepSeq: 2, Kind: "ui", Target: "首页", Expected: "显示欢迎语"},
			},
		},
		Environment: executionapp.EnvironmentRef{
			ID: 3, BaseURL: "https://app.example.test", AllowedHosts: []string{"app.example.test"},
		},
		Session:            executionapp.SessionRef{SessionID: 55},
		CompletedSeqs:      []int{1},
		InterruptedStepSeq: &interrupted,
		CleanupRequired:    true,
	}
}

// TestAgentRuntimeEnsureProfileSeedsExecutionProfile pins the idempotent seed of
// the execution-agent profile.
func TestAgentRuntimeEnsureProfileSeedsExecutionProfile(t *testing.T) {
	fixture := newRuntimeFixture()
	var _ executionapp.AgentRuntimePort = fixture.adapter()

	if err := fixture.adapter().EnsureProfile(context.Background()); err != nil {
		t.Fatalf("EnsureProfile: %v", err)
	}
	if len(fixture.seeder.seeds) != 1 {
		t.Fatalf("seeded %d profiles, want 1", len(fixture.seeder.seeds))
	}
	seed := fixture.seeder.seeds[0]
	if seed.Key != executiondomain.ExecutionProfileKey || seed.Builtin || seed.Name != "执行 Agent" {
		t.Fatalf("seed=%+v", seed)
	}
	if seed.SystemPrompt != executiondomain.ExecutionPrompt {
		t.Fatal("seeded system prompt is not the ExecutionPrompt constant")
	}
	if len(seed.AllowedTools) != 16 || seed.MaxTurns != 30 || seed.RunSeconds != 900 || seed.HardTimeoutSeconds != 120 {
		t.Fatalf("seed budgets/tools=%+v", seed)
	}
}

// TestAgentRuntimeSessionForCaseRun pins the leased-session bridge the agent
// tools consume for browser storage injection and http.request headers. The
// lease is forgotten when the agent run reaches a terminal state.
func TestAgentRuntimeSessionForCaseRun(t *testing.T) {
	fixture := newRuntimeFixture()
	request := caseRunRequest()
	request.Session.Headers = map[string]string{"Authorization": "Bearer lease-token"}

	if _, err := fixture.adapter().StartCaseRun(context.Background(), request); err != nil {
		t.Fatalf("StartCaseRun: %v", err)
	}
	session, err := fixture.adapter().SessionForCaseRun(context.Background(), request.CaseRunID)
	if err != nil {
		t.Fatalf("SessionForCaseRun: %v", err)
	}
	if session.SessionID != 55 || session.Headers["Authorization"] != "Bearer lease-token" {
		t.Fatalf("session=%+v want the leased session", session)
	}

	fixture.sessions.details[88] = agentdomain.SessionDetail{
		Session:   agentdomain.Session{ID: 88},
		Running:   true,
		ActiveRun: &agentdomain.Run{ID: 99, Status: agentdomain.RunRunning},
	}
	if _, err := fixture.adapter().GetRun(context.Background(), 88); err != nil {
		t.Fatalf("GetRun(running): %v", err)
	}
	if _, err := fixture.adapter().SessionForCaseRun(context.Background(), request.CaseRunID); err != nil {
		t.Fatalf("a running case must keep its leased session: %v", err)
	}

	fixture.sessions.details[88] = agentdomain.SessionDetail{
		Session: agentdomain.Session{ID: 88},
		LastRun: &agentdomain.Run{ID: 99, Status: agentdomain.RunDone},
	}
	if _, err := fixture.adapter().GetRun(context.Background(), 88); err != nil {
		t.Fatalf("GetRun(terminal): %v", err)
	}
	if _, err := fixture.adapter().SessionForCaseRun(context.Background(), request.CaseRunID); err == nil {
		t.Fatal("a terminal case must forget its leased session")
	}
}

// TestAgentRuntimeStartCaseRun pins profile resolution, the version channel, the
// case title, the run scope and the case-context prompt.
func TestAgentRuntimeStartCaseRun(t *testing.T) {
	fixture := newRuntimeFixture()
	request := caseRunRequest()

	ref, err := fixture.adapter().StartCaseRun(context.Background(), request)
	if err != nil {
		t.Fatalf("StartCaseRun: %v", err)
	}
	if ref.SessionID != 88 || ref.RunID != 99 {
		t.Fatalf("agent run ref=%+v want session 88 run 99", ref)
	}
	if len(fixture.profiles.requested) != 1 || fixture.profiles.requested[0] != executiondomain.ExecutionProfileKey {
		t.Fatalf("resolved profile keys=%v", fixture.profiles.requested)
	}
	if fixture.sessions.createProfileID != 5 || fixture.sessions.createTitle != "用例 10：登录" {
		t.Fatalf("session profile/title=%d/%q", fixture.sessions.createProfileID, fixture.sessions.createTitle)
	}
	if fixture.sessions.createOptions.ActivityChannel != "version:7" {
		t.Fatalf("activity channel=%q want version:7", fixture.sessions.createOptions.ActivityChannel)
	}
	if fixture.sessions.createOptions.ActivityVersionID == nil || *fixture.sessions.createOptions.ActivityVersionID != "7" {
		t.Fatalf("activity version id=%v", fixture.sessions.createOptions.ActivityVersionID)
	}
	if fixture.runs.startSession != 88 || fixture.runs.startScope != (agentdomain.RunScope{BatchID: 8, CaseRunID: 9}) {
		t.Fatalf("run session/scope=%d/%+v", fixture.runs.startSession, fixture.runs.startScope)
	}
	if len(fixture.runs.startParts) != 1 || fixture.runs.startParts[0].Type != "text" {
		t.Fatalf("run parts=%+v want one text part", fixture.runs.startParts)
	}
	prompt := fixture.runs.startParts[0].Text
	for _, want := range []string{
		"用例 10：登录",
		"case_run_id: 9",
		"https://app.example.test",
		"app.example.test",
		"打开登录页",
		"已完成步骤: 1",
		"第 2 步被中断",
		"cleanup_required=true",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt is missing %q:\n%s", want, prompt)
		}
	}
}

// TestAgentRuntimeResumePromptIncludesActualsAndRemainingSteps pins the §5.5
// resume message: a new agent run must see each completed step's actual result
// and evidence count plus the steps that are still left.
func TestAgentRuntimeResumePromptIncludesActualsAndRemainingSteps(t *testing.T) {
	fixture := newRuntimeFixture()
	fixture.adapter().SetCaseContextSource(&fakeCaseContext{view: executionapp.CaseContextView{
		CompletedSteps: []executionapp.CaseContextCompletedStep{{
			Seq: 1, Status: executiondomain.StepStatusPassed, Actual: "登录页已打开", EvidenceCount: 2,
		}},
		RemainingSteps: []executiondomain.CaseSnapshotStep{
			{Seq: 2, Action: "输入账号密码", Expected: "登录成功"},
		},
	}})
	request := caseRunRequest()

	if _, err := fixture.adapter().StartCaseRun(context.Background(), request); err != nil {
		t.Fatalf("StartCaseRun: %v", err)
	}
	prompt := fixture.runs.startParts[0].Text
	for _, want := range []string{
		"已完成步骤: 1",
		"登录页已打开",
		"证据 2 条",
		"剩余步骤",
		"输入账号密码",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("resume prompt is missing %q:\n%s", want, prompt)
		}
	}
}

// TestAgentRuntimeStartCaseRunPropagatesErrors pins startup failure handling.
func TestAgentRuntimeStartCaseRunPropagatesErrors(t *testing.T) {
	request := caseRunRequest()
	startFailure := errors.New("model unavailable")

	fixture := newRuntimeFixture()
	fixture.runs.startErr = startFailure
	if _, err := fixture.adapter().StartCaseRun(context.Background(), request); !errors.Is(err, startFailure) {
		t.Fatalf("StartCaseRun error=%v want %v", err, startFailure)
	}

	fixture = newRuntimeFixture()
	fixture.sessions.createErr = agentdomain.ErrProfileDisabled
	if _, err := fixture.adapter().StartCaseRun(context.Background(), request); !errors.Is(err, agentdomain.ErrProfileDisabled) {
		t.Fatalf("StartCaseRun error=%v want ErrProfileDisabled", err)
	}

	fixture = newRuntimeFixture()
	fixture.profiles.byKey = map[string]agentdomain.AgentProfile{}
	if _, err := fixture.adapter().StartCaseRun(context.Background(), request); !errors.Is(err, agentdomain.ErrNotFound) {
		t.Fatalf("StartCaseRun error=%v want ErrNotFound", err)
	}

	resumeFailure := errors.New("step results unavailable")
	fixture = newRuntimeFixture()
	fixture.adapter().SetCaseContextSource(&fakeCaseContext{err: resumeFailure})
	if _, err := fixture.adapter().StartCaseRun(context.Background(), request); !errors.Is(err, resumeFailure) {
		t.Fatalf("StartCaseRun error=%v want the resume failure", err)
	}
	if fixture.sessions.createProfileID != 0 {
		t.Fatal("StartCaseRun created a session after the resume context failed")
	}
}

// TestAgentRuntimeGetRunMapsActiveAndLastRun pins the polled run state.
func TestAgentRuntimeGetRunMapsActiveAndLastRun(t *testing.T) {
	ended := time.Now()
	fixture := newRuntimeFixture()
	fixture.sessions.details[88] = agentdomain.SessionDetail{
		Session:   agentdomain.Session{ID: 88},
		Running:   true,
		ActiveRun: &agentdomain.Run{ID: 99, Status: agentdomain.RunRunning},
		LastRun:   &agentdomain.Run{ID: 98, Status: agentdomain.RunFailed, ErrorMessage: "older failure"},
	}
	state, err := fixture.adapter().GetRun(context.Background(), 88)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if state.RunID != 99 || state.Status != string(agentdomain.RunRunning) || state.FinishedAt != nil {
		t.Fatalf("active run state=%+v", state)
	}

	fixture.sessions.details[89] = agentdomain.SessionDetail{
		Session: agentdomain.Session{ID: 89},
		LastRun: &agentdomain.Run{ID: 77, Status: agentdomain.RunFailed, ErrorMessage: "boom", EndedAt: &ended},
	}
	state, err = fixture.adapter().GetRun(context.Background(), 89)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if state.RunID != 77 || state.Status != string(agentdomain.RunFailed) || state.Error != "boom" ||
		state.FinishedAt == nil || !state.FinishedAt.Equal(ended) {
		t.Fatalf("terminal run state=%+v", state)
	}

	if _, err := fixture.adapter().GetRun(context.Background(), 1); !errors.Is(err, agentdomain.ErrNotFound) {
		t.Fatalf("GetRun(missing) error=%v want ErrNotFound", err)
	}
}

// TestAgentRuntimeGetRunMapsTerminalReason pins that the P1 run's terminal
// reason reaches the execution runner, which needs it to tell an LLM failure
// apart from a normal end that skipped case_finish.
func TestAgentRuntimeGetRunMapsTerminalReason(t *testing.T) {
	fixture := newRuntimeFixture()
	fixture.sessions.details[88] = agentdomain.SessionDetail{
		Session: agentdomain.Session{ID: 88},
		LastRun: &agentdomain.Run{
			ID:             77,
			Status:         agentdomain.RunFailed,
			TerminalReason: agentdomain.ReasonModelError,
			ErrorMessage:   "upstream request failed",
		},
	}
	state, err := fixture.adapter().GetRun(context.Background(), 88)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if state.TerminalReason != string(agentdomain.ReasonModelError) {
		t.Fatalf("terminal reason = %q, want %q", state.TerminalReason, agentdomain.ReasonModelError)
	}
	if state.Status != string(agentdomain.RunFailed) || state.Error != "upstream request failed" {
		t.Fatalf("terminal run state=%+v", state)
	}
}

// TestAgentRuntimeCancelRunDelegates pins the abort delegation and idle handling.
func TestAgentRuntimeCancelRunDelegates(t *testing.T) {
	fixture := newRuntimeFixture()
	if err := fixture.adapter().CancelRun(context.Background(), 88); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if fixture.runs.abortSession != 88 || fixture.runs.abortActor != nil {
		t.Fatalf("abort args=%d/%v", fixture.runs.abortSession, fixture.runs.abortActor)
	}

	fixture.runs.abortErr = errors.New("abort failed")
	if err := fixture.adapter().CancelRun(context.Background(), 88); err == nil {
		t.Fatal("CancelRun must propagate the abort error")
	}
}

// TestAgentRuntimeListSessionsUsesVersionChannel pins the Sessions tab projection.
func TestAgentRuntimeListSessionsUsesVersionChannel(t *testing.T) {
	created := time.Now().Add(-time.Minute)
	fixture := newRuntimeFixture()
	fixture.sessions.list = []agentdomain.Session{
		{ID: 101, Title: "用例 10：登录", CreatedAt: created, UpdatedAt: created},
		{ID: 102, Title: "用例 11：下单", CreatedAt: created, UpdatedAt: created},
	}
	fixture.sessions.details[101] = agentdomain.SessionDetail{
		Session: agentdomain.Session{ID: 101}, Running: true,
		ActiveRun: &agentdomain.Run{ID: 201, Status: agentdomain.RunRunning},
	}
	fixture.sessions.details[102] = agentdomain.SessionDetail{Session: agentdomain.Session{ID: 102}}

	sessions, err := fixture.adapter().ListSessions(context.Background(), 7)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if fixture.sessions.listChannel != "version:7" {
		t.Fatalf("channel=%q want version:7", fixture.sessions.listChannel)
	}
	if len(sessions) != 2 || sessions[0].SessionID != 101 || sessions[0].Status != "running" {
		t.Fatalf("sessions=%+v", sessions)
	}
	if sessions[1].SessionID != 102 || sessions[1].Status != "idle" || sessions[1].Title != "用例 11：下单" {
		t.Fatalf("sessions=%+v", sessions)
	}
}

// TestAgentRuntimeListSessionsPropagatesRepoError pins the repository error path.
func TestAgentRuntimeListSessionsPropagatesRepoError(t *testing.T) {
	fixture := newRuntimeFixture()
	fixture.sessions.listErr = errors.New("db down")
	if _, err := fixture.adapter().ListSessions(context.Background(), 7); err == nil {
		t.Fatal("ListSessions must propagate the repository error")
	}
}
