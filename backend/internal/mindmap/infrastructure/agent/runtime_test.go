package agent_test

import (
	"context"
	"errors"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	mindmapagent "github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/agent"
)

// fakeProfileSeeder records the seeded agent profile.
type fakeProfileSeeder struct {
	seeds []agentdomain.AgentProfile
	err   error
}

// EnsureBuiltin implements the idempotent P1 profile seed.
func (f *fakeProfileSeeder) EnsureBuiltin(_ context.Context, profile agentdomain.AgentProfile) (bool, error) {
	f.seeds = append(f.seeds, profile)
	return true, f.err
}

// fakeProfileResolver returns profiles by key.
type fakeProfileResolver struct {
	byKey     map[string]agentdomain.AgentProfile
	err       error
	requested string
}

// GetByKey implements the P1 profile key lookup.
func (f *fakeProfileResolver) GetByKey(_ context.Context, key string) (agentdomain.AgentProfile, error) {
	f.requested = key
	if f.err != nil {
		return agentdomain.AgentProfile{}, f.err
	}
	profile, ok := f.byKey[key]
	if !ok {
		return agentdomain.AgentProfile{}, agentdomain.ErrNotFound
	}
	return profile, nil
}

// fakeAgentSessions records session creation and serves one session detail.
type fakeAgentSessions struct {
	createProfileID int64
	createTitle     string
	createActor     *int64
	createOptions   agentapp.SessionOptions
	session         agentdomain.Session
	createErr       error

	getID  int64
	detail agentdomain.SessionDetail
	getErr error
}

// CreateWithOptions implements the P1 session creation.
func (f *fakeAgentSessions) CreateWithOptions(_ context.Context, profileID int64, title string, actor *int64, opts agentapp.SessionOptions) (agentdomain.Session, error) {
	f.createProfileID, f.createTitle, f.createActor, f.createOptions = profileID, title, actor, opts
	if f.createErr != nil {
		return agentdomain.Session{}, f.createErr
	}
	return f.session, nil
}

// Get implements the P1 session detail read.
func (f *fakeAgentSessions) Get(_ context.Context, id int64) (agentdomain.SessionDetail, error) {
	f.getID = id
	if f.getErr != nil {
		return agentdomain.SessionDetail{}, f.getErr
	}
	return f.detail, nil
}

// fakeAgentRuns records run starts and aborts.
type fakeAgentRuns struct {
	startSession int64
	startMessage string
	startActor   *int64
	startRunID   int64
	startErr     error

	abortSession int64
	abortActor   *int64
	abortErr     error
}

// Start implements the P1 text run start.
func (f *fakeAgentRuns) Start(_ context.Context, sessionID int64, message string, actor *int64) (agentdomain.StartResult, error) {
	f.startSession, f.startMessage, f.startActor = sessionID, message, actor
	if f.startErr != nil {
		return agentdomain.StartResult{}, f.startErr
	}
	return agentdomain.StartResult{RunID: f.startRunID}, nil
}

// Abort implements the P1 run abort.
func (f *fakeAgentRuns) Abort(_ context.Context, sessionID int64, actor *int64) (agentdomain.AbortResult, error) {
	f.abortSession, f.abortActor = sessionID, actor
	if f.abortErr != nil {
		return agentdomain.AbortResult{}, f.abortErr
	}
	return agentdomain.AbortResult{Status: "stopping"}, nil
}

// TestAgentRuntimeAdapterEnsureProfileMountsTheSeed pins the mindmap profile seed
// -> P1 agent profile conversion and the idempotent EnsureBuiltin call.
func TestAgentRuntimeAdapterEnsureProfileSeedsTheProfile(t *testing.T) {
	ctx := context.Background()
	seeder := &fakeProfileSeeder{}
	adapter := mindmapagent.NewAgentRuntimeAdapter(nil, seeder, nil, nil)
	var _ mindmapapp.AgentRuntime = adapter

	seed := mindmapdomain.AgentProfileSeed{
		Key:                mindmapdomain.ProfileKeyCaseDesign,
		Name:               "测试设计 Agent",
		Description:        "designs cases",
		Builtin:            false,
		Enabled:            true,
		SystemPrompt:       "prompt",
		Model:              "model-x",
		AllowedTools:       []string{"code.read", "case.graph_read"},
		MaxTurns:           24,
		RunSeconds:         1200,
		HardTimeoutSeconds: 90,
		Thinking:           "enabled",
		ReasoningEffort:    "max",
		MaxOutputTokens:    32768,
	}
	if err := adapter.EnsureProfile(ctx, seed); err != nil {
		t.Fatalf("EnsureProfile: %v", err)
	}
	if len(seeder.seeds) != 1 {
		t.Fatalf("seeded %d profiles, want 1", len(seeder.seeds))
	}
	got := seeder.seeds[0]
	if got.Key != seed.Key || got.Name != seed.Name || got.Description != seed.Description ||
		got.Builtin != seed.Builtin || got.Enabled != seed.Enabled || got.SystemPrompt != seed.SystemPrompt ||
		got.Model != seed.Model || got.MaxTurns != seed.MaxTurns || got.RunSeconds != seed.RunSeconds ||
		got.HardTimeoutSeconds != seed.HardTimeoutSeconds || got.Thinking != seed.Thinking ||
		got.ReasoningEffort != seed.ReasoningEffort || got.MaxOutputTokens != seed.MaxOutputTokens {
		t.Fatalf("seed=%+v want the mindmap profile fields", got)
	}
	if len(got.AllowedTools) != 2 || got.AllowedTools[0] != "code.read" {
		t.Fatalf("allowed tools=%v want the seed tools", got.AllowedTools)
	}
}

// TestAgentRuntimeAdapterCreateSession pins the profile-key resolution and the
// channel/version stamp of a new session.
func TestAgentRuntimeAdapterCreateSession(t *testing.T) {
	ctx := context.Background()
	profiles := &fakeProfileResolver{byKey: map[string]agentdomain.AgentProfile{
		mindmapdomain.ProfileKeyCaseDesign: {ID: 21, Key: mindmapdomain.ProfileKeyCaseDesign, Enabled: true},
	}}
	sessions := &fakeAgentSessions{session: agentdomain.Session{ID: 88}}
	adapter := mindmapagent.NewAgentRuntimeAdapter(profiles, &fakeProfileSeeder{}, sessions, &fakeAgentRuns{})

	sessionID, err := adapter.CreateSession(ctx, mindmapdomain.ProfileKeyCaseDesign, "登录设计", 42, "version:7", "7")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sessionID != 88 {
		t.Fatalf("session id=%d want 88", sessionID)
	}
	if profiles.requested != mindmapdomain.ProfileKeyCaseDesign {
		t.Fatalf("resolved profile key=%q", profiles.requested)
	}
	if sessions.createProfileID != 21 || sessions.createTitle != "登录设计" {
		t.Fatalf("created profile/title=%d/%q want 21/登录设计", sessions.createProfileID, sessions.createTitle)
	}
	if sessions.createActor == nil || *sessions.createActor != 42 {
		t.Fatalf("created actor=%v want 42", sessions.createActor)
	}
	if sessions.createOptions.ActivityChannel != "version:7" {
		t.Fatalf("activity channel=%q want version:7", sessions.createOptions.ActivityChannel)
	}
	if sessions.createOptions.ActivityVersionID == nil || *sessions.createOptions.ActivityVersionID != "7" {
		t.Fatalf("activity version id=%v want \"7\"", sessions.createOptions.ActivityVersionID)
	}
}

// TestAgentRuntimeAdapterMapsAgentErrors pins the agent -> mindmap translations
// for a missing profile and a disabled profile.
func TestAgentRuntimeAdapterMapsAgentErrors(t *testing.T) {
	ctx := context.Background()

	missing := mindmapagent.NewAgentRuntimeAdapter(&fakeProfileResolver{}, &fakeProfileSeeder{}, &fakeAgentSessions{}, &fakeAgentRuns{})
	_, err := missing.CreateSession(ctx, "unknown", "title", 42, "version:7", "7")
	var mapped *mindmapdomain.Error
	if !errors.As(err, &mapped) || mapped.Kind != mindmapdomain.KindNotFound {
		t.Fatalf("CreateSession(missing profile) error=%v want mindmap not_found", err)
	}

	disabled := mindmapagent.NewAgentRuntimeAdapter(
		&fakeProfileResolver{byKey: map[string]agentdomain.AgentProfile{"case-review": {ID: 22}}},
		&fakeProfileSeeder{},
		&fakeAgentSessions{createErr: agentdomain.ErrProfileDisabled},
		&fakeAgentRuns{},
	)
	_, err = disabled.CreateSession(ctx, "case-review", "title", 42, "version:7", "7")
	if !errors.As(err, &mapped) || mapped.Kind != mindmapdomain.KindStateConflict || mapped.Reason != "agent_profile_disabled" {
		t.Fatalf("CreateSession(disabled profile) error=%v want agent_profile_disabled", err)
	}
}

// TestAgentRuntimeAdapterStartAndAbort pins the text-run and abort delegation.
func TestAgentRuntimeAdapterStartAndAbort(t *testing.T) {
	ctx := context.Background()
	runs := &fakeAgentRuns{startRunID: 55}
	adapter := mindmapagent.NewAgentRuntimeAdapter(&fakeProfileResolver{}, &fakeProfileSeeder{}, &fakeAgentSessions{}, runs)

	runID, err := adapter.StartText(ctx, 88, "请设计登录用例", 42)
	if err != nil {
		t.Fatalf("StartText: %v", err)
	}
	if runID != 55 {
		t.Fatalf("run id=%d want 55", runID)
	}
	if runs.startSession != 88 || runs.startMessage != "请设计登录用例" || runs.startActor == nil || *runs.startActor != 42 {
		t.Fatalf("start args=%d/%q/%v", runs.startSession, runs.startMessage, runs.startActor)
	}

	if err := adapter.Abort(ctx, 88, 42); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if runs.abortSession != 88 || runs.abortActor == nil || *runs.abortActor != 42 {
		t.Fatalf("abort args=%d/%v want 88/42", runs.abortSession, runs.abortActor)
	}
}

// TestAgentRuntimeAdapterGetSessionMapsDetail pins the session status projection:
// a running session reports running with its active run, an idle session reports
// the newest terminal run's status and error.
func TestAgentRuntimeAdapterGetSessionMapsDetail(t *testing.T) {
	ctx := context.Background()

	running := &fakeAgentSessions{detail: agentdomain.SessionDetail{
		Session:   agentdomain.Session{ID: 4},
		Running:   true,
		ActiveRun: &agentdomain.Run{ID: 9, Status: agentdomain.RunRunning},
		LastRun:   &agentdomain.Run{ID: 8, Status: agentdomain.RunFailed, ErrorMessage: "older failure"},
	}}
	adapter := mindmapagent.NewAgentRuntimeAdapter(&fakeProfileResolver{}, &fakeProfileSeeder{}, running, &fakeAgentRuns{})
	status, err := adapter.GetSession(ctx, 4)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if status.SessionID != 4 || status.Status != "running" {
		t.Fatalf("session status=%+v want id 4 running", status)
	}
	if status.LastRun == nil || status.LastRun.ID != 9 || status.LastRun.Status != string(agentdomain.RunRunning) {
		t.Fatalf("last run=%+v want the active run 9", status.LastRun)
	}
	if running.getID != 4 {
		t.Fatalf("session reader received id %d want 4", running.getID)
	}

	idle := &fakeAgentSessions{detail: agentdomain.SessionDetail{
		Session: agentdomain.Session{ID: 4},
		LastRun: &agentdomain.Run{ID: 8, Status: agentdomain.RunFailed, ErrorMessage: "boom"},
	}}
	adapter = mindmapagent.NewAgentRuntimeAdapter(&fakeProfileResolver{}, &fakeProfileSeeder{}, idle, &fakeAgentRuns{})
	status, err = adapter.GetSession(ctx, 4)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if status.Status != "idle" {
		t.Fatalf("session status=%q want idle", status.Status)
	}
	if status.LastRun == nil || status.LastRun.ID != 8 || status.LastRun.Status != string(agentdomain.RunFailed) || status.LastRun.Error != "boom" {
		t.Fatalf("last run=%+v want run 8 failed with error boom", status.LastRun)
	}

	empty := &fakeAgentSessions{detail: agentdomain.SessionDetail{Session: agentdomain.Session{ID: 4}}}
	adapter = mindmapagent.NewAgentRuntimeAdapter(&fakeProfileResolver{}, &fakeProfileSeeder{}, empty, &fakeAgentRuns{})
	status, err = adapter.GetSession(ctx, 4)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if status.LastRun != nil {
		t.Fatalf("last run=%+v want nil before the first run", status.LastRun)
	}
}
