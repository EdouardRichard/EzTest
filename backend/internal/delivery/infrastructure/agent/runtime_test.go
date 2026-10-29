package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	deliverydomain "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// Compile guards: the delivery application ports are the only contract the
// composition root depends on.
var (
	_ deliveryapp.AnalysisAgentRuntime       = (*AgentRuntime)(nil)
	_ deliveryapp.AnalysisAgentProfileReader = (*AgentRuntime)(nil)
)

// fakeProfileResolver resolves the seeded profiles by key.
type fakeProfileResolver struct {
	profiles map[string]agentdomain.AgentProfile
	err      error
	calls    int
}

func (f *fakeProfileResolver) GetByKey(_ context.Context, key string) (agentdomain.AgentProfile, error) {
	f.calls++
	if f.err != nil {
		return agentdomain.AgentProfile{}, f.err
	}
	profile, ok := f.profiles[key]
	if !ok {
		return agentdomain.AgentProfile{}, agentdomain.ErrNotFound
	}
	return profile, nil
}

// fakeProfileSeeder emulates agent_profile's ON CONFLICT (key) DO NOTHING:
// the first seed of a key is stored, every repeat is reported as not created.
type fakeProfileSeeder struct {
	stored map[string]agentdomain.AgentProfile
	calls  int
	err    error
}

func newFakeProfileSeeder() *fakeProfileSeeder {
	return &fakeProfileSeeder{stored: map[string]agentdomain.AgentProfile{}}
}

func (f *fakeProfileSeeder) EnsureBuiltin(_ context.Context, profile agentdomain.AgentProfile) (bool, error) {
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	if _, exists := f.stored[profile.Key]; exists {
		return false, nil
	}
	f.stored[profile.Key] = profile
	return true, nil
}

// fakeSessionService records the created session and returns a scripted detail.
type fakeSessionService struct {
	profileID int64
	title     string
	actor     *int64
	options   agentapp.SessionOptions
	createErr error
	sessionID int64

	detail    agentdomain.SessionDetail
	detailErr error
	getCalls  int
}

func (f *fakeSessionService) CreateWithOptions(_ context.Context, profileID int64, title string, actor *int64, opts agentapp.SessionOptions) (agentdomain.Session, error) {
	if f.createErr != nil {
		return agentdomain.Session{}, f.createErr
	}
	f.profileID = profileID
	f.title = title
	f.actor = actor
	f.options = opts
	id := f.sessionID
	if id == 0 {
		id = 100
	}
	return agentdomain.Session{ID: id, ProfileID: profileID, Title: title, CreatedBy: actor}, nil
}

func (f *fakeSessionService) Get(_ context.Context, _ int64) (agentdomain.SessionDetail, error) {
	f.getCalls++
	if f.detailErr != nil {
		return agentdomain.SessionDetail{}, f.detailErr
	}
	return f.detail, nil
}

// fakeRunService records StartWithScope/Abort calls.
type fakeRunService struct {
	startCalls  int
	sessionID   int64
	parts       []agentdomain.Part
	actor       *int64
	scope       agentdomain.RunScope
	startResult agentdomain.StartResult
	startErr    error

	abortCalls int
	abortErr   error
}

func (f *fakeRunService) StartWithScope(_ context.Context, sessionID int64, parts []agentdomain.Part, actor *int64, scope agentdomain.RunScope) (agentdomain.StartResult, error) {
	f.startCalls++
	if f.startErr != nil {
		return agentdomain.StartResult{}, f.startErr
	}
	f.sessionID = sessionID
	f.parts = parts
	f.actor = actor
	f.scope = scope
	return f.startResult, nil
}

func (f *fakeRunService) Abort(_ context.Context, _ int64, actor *int64) (agentdomain.AbortResult, error) {
	f.abortCalls++
	if f.abortErr != nil {
		return agentdomain.AbortResult{}, f.abortErr
	}
	f.actor = actor
	return agentdomain.AbortResult{Status: "stopping"}, nil
}

// analysisTestProfiles returns the two seeded profiles with distinct ids and
// one custom model on the triage profile.
func analysisTestProfiles() *fakeProfileResolver {
	return &fakeProfileResolver{profiles: map[string]agentdomain.AgentProfile{
		"failure-analysis-agent": {ID: 17, Key: "failure-analysis-agent", Model: ""},
		"issue-triage-agent":     {ID: 18, Key: "issue-triage-agent", Model: "deepseek-pro"},
	}}
}

// newTestRuntime wires the adapter to the fakes.
func newTestRuntime() (*AgentRuntime, *fakeProfileResolver, *fakeProfileSeeder, *fakeSessionService, *fakeRunService) {
	profiles := analysisTestProfiles()
	seeder := newFakeProfileSeeder()
	sessions := &fakeSessionService{sessionID: 100}
	runs := &fakeRunService{startResult: agentdomain.StartResult{RunID: 200}}
	return NewAgentRuntime(profiles, seeder, sessions, runs), profiles, seeder, sessions, runs
}

func TestEnsureProfilesSeedsBothAnalysisProfiles(t *testing.T) {
	runtime, _, seeder, _, _ := newTestRuntime()

	if err := runtime.EnsureProfiles(context.Background()); err != nil {
		t.Fatalf("EnsureProfiles: %v", err)
	}
	if len(seeder.stored) != 2 {
		t.Fatalf("seeded profiles = %d, want the failure and triage profiles", len(seeder.stored))
	}

	tests := []struct {
		key          string
		tools        []string
		runSeconds   int
		maxTurns     int
		hardTimeout  int
		thinking     string
		maxOutTokens int
	}{
		{
			key: "failure-analysis-agent",
			tools: []string{
				"analysis.case_list",
				"analysis.case_detail",
				"analysis.evidence_meta",
				"analysis.code_read",
				"analysis.candidate_propose",
				"analysis.finish",
			},
			runSeconds: 600, maxTurns: 20, hardTimeout: 120,
			thinking: "disabled", maxOutTokens: 8192,
		},
		{
			key: "issue-triage-agent",
			tools: []string{
				"triage.candidate_list",
				"triage.issue_list",
				"triage.context_read",
				"triage.issue_draft",
				"triage.finish",
			},
			runSeconds: 600, maxTurns: 20, hardTimeout: 120,
			thinking: "disabled", maxOutTokens: 8192,
		},
	}
	for _, test := range tests {
		t.Run(test.key, func(t *testing.T) {
			profile, ok := seeder.stored[test.key]
			if !ok {
				t.Fatalf("profile %q was not seeded", test.key)
			}
			if profile.Builtin {
				t.Fatal("analysis profiles must be Builtin=false so they never become the P1 default")
			}
			if !profile.Enabled {
				t.Fatal("seeded analysis profiles must be enabled")
			}
			if profile.Model != "" {
				t.Fatalf("model = %q, want the default LLM", profile.Model)
			}
			if profile.MaxTurns != test.maxTurns || profile.RunSeconds != test.runSeconds ||
				profile.HardTimeoutSeconds != test.hardTimeout {
				t.Fatalf("budgets = %d/%d/%d, want %d/%d/%d",
					profile.MaxTurns, profile.RunSeconds, profile.HardTimeoutSeconds,
					test.maxTurns, test.runSeconds, test.hardTimeout)
			}
			if profile.Thinking != test.thinking || profile.ReasoningEffort != "" {
				t.Fatalf("thinking = %q/%q, want %q and no reasoning effort",
					profile.Thinking, profile.ReasoningEffort, test.thinking)
			}
			if profile.MaxOutputTokens != test.maxOutTokens {
				t.Fatalf("max output tokens = %d, want %d", profile.MaxOutputTokens, test.maxOutTokens)
			}
			if strings.TrimSpace(profile.SystemPrompt) == "" {
				t.Fatal("the seeded profile needs a system prompt")
			}
			if len(profile.AllowedTools) != len(test.tools) {
				t.Fatalf("allowed tools = %v, want %v", profile.AllowedTools, test.tools)
			}
			for index, tool := range test.tools {
				if profile.AllowedTools[index] != tool {
					t.Fatalf("allowed tools = %v, want %v", profile.AllowedTools, test.tools)
				}
			}
		})
	}
}

func TestEnsureProfilesIsIdempotent(t *testing.T) {
	runtime, _, seeder, _, _ := newTestRuntime()

	for round := 0; round < 3; round++ {
		if err := runtime.EnsureProfiles(context.Background()); err != nil {
			t.Fatalf("EnsureProfiles round %d: %v", round, err)
		}
	}
	if seeder.calls != 6 {
		t.Fatalf("EnsureBuiltin calls = %d, want 6 (2 profiles x 3 rounds)", seeder.calls)
	}
	if len(seeder.stored) != 2 {
		t.Fatalf("stored profiles = %d, want the seed to never overwrite or duplicate", len(seeder.stored))
	}
}

func TestEnsureProfilesRequiresSeeder(t *testing.T) {
	runtime := NewAgentRuntime(nil, nil, nil, nil)
	if err := runtime.EnsureProfiles(context.Background()); err == nil {
		t.Fatal("EnsureProfiles without a seeder must fail closed")
	}
}

func TestEnsureProfilesPropagatesSeedFailure(t *testing.T) {
	runtime, _, seeder, _, _ := newTestRuntime()
	seeder.err = errors.New("db down")
	if err := runtime.EnsureProfiles(context.Background()); err == nil {
		t.Fatal("EnsureProfiles must return the seed error")
	}
}

func TestCreateSessionStampsVersionActivityChannel(t *testing.T) {
	runtime, _, _, sessions, _ := newTestRuntime()

	sessionID, err := runtime.CreateSession(context.Background(), "failure-analysis-agent",
		"batch 9 · failure analysis", 42, "version:7", "7")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sessionID != 100 {
		t.Fatalf("session id = %d, want 100", sessionID)
	}
	if sessions.profileID != 17 {
		t.Fatalf("profile id = %d, want the resolved failure-analysis profile 17", sessions.profileID)
	}
	if sessions.title != "batch 9 · failure analysis" {
		t.Fatalf("session title = %q", sessions.title)
	}
	if sessions.actor == nil || *sessions.actor != 42 {
		t.Fatalf("session actor = %v, want 42", sessions.actor)
	}
	if sessions.options.ActivityChannel != "version:7" {
		t.Fatalf("activity channel = %q, want version:7", sessions.options.ActivityChannel)
	}
	if sessions.options.ActivityVersionID == nil || *sessions.options.ActivityVersionID != "7" {
		t.Fatalf("activity version id = %v, want \"7\"", sessions.options.ActivityVersionID)
	}
}

func TestCreateSessionKeepsZeroActorSystemStarted(t *testing.T) {
	runtime, _, _, sessions, _ := newTestRuntime()

	if _, err := runtime.CreateSession(context.Background(), "issue-triage-agent", "batch 9 · issue triage", 0, "version:7", "7"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sessions.actor != nil {
		t.Fatalf("actor = %d, want nil for a system-started run", *sessions.actor)
	}
}

func TestCreateSessionPropagatesProfileErrors(t *testing.T) {
	runtime, profiles, _, _, _ := newTestRuntime()
	profiles.err = agentdomain.ErrProfileDisabled
	if _, err := runtime.CreateSession(context.Background(), "failure-analysis-agent", "t", 1, "version:7", "7"); err == nil {
		t.Fatal("CreateSession must surface a disabled/missing profile error")
	}
}

func TestStartPassesAnalysisRunScope(t *testing.T) {
	runtime, _, _, _, runs := newTestRuntime()

	runID, err := runtime.Start(context.Background(), 100, "analyze the batch", 42, deliveryapp.AnalysisAgentRunScope{
		VersionID:     7,
		BatchID:       9,
		AnalysisRunID: 11,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if runID != 200 {
		t.Fatalf("run id = %d, want 200", runID)
	}
	if runs.sessionID != 100 {
		t.Fatalf("session id = %d, want 100", runs.sessionID)
	}
	want := agentdomain.RunScope{VersionID: 7, BatchID: 9, AnalysisRunID: 11}
	if runs.scope != want {
		t.Fatalf("run scope = %+v, want %+v", runs.scope, want)
	}
	if len(runs.parts) != 1 || runs.parts[0].Type != "text" || runs.parts[0].Text != "analyze the batch" {
		t.Fatalf("parts = %+v, want one text part", runs.parts)
	}
	if runs.actor == nil || *runs.actor != 42 {
		t.Fatalf("actor = %v, want 42", runs.actor)
	}
}

func TestStartRejectsIncompleteAnalysisScope(t *testing.T) {
	tests := []struct {
		name  string
		scope deliveryapp.AnalysisAgentRunScope
	}{
		{"missing version", deliveryapp.AnalysisAgentRunScope{BatchID: 9, AnalysisRunID: 11}},
		{"missing batch", deliveryapp.AnalysisAgentRunScope{VersionID: 7, AnalysisRunID: 11}},
		{"missing analysis run", deliveryapp.AnalysisAgentRunScope{VersionID: 7, BatchID: 9}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime, _, _, _, runs := newTestRuntime()
			if _, err := runtime.Start(context.Background(), 100, "x", 42, test.scope); err == nil {
				t.Fatal("Start must reject an incomplete analysis scope")
			}
			if runs.startCalls != 0 {
				t.Fatalf("StartWithScope calls = %d, want 0", runs.startCalls)
			}
		})
	}
}

func TestGetSessionProjectsRunningRun(t *testing.T) {
	runtime, _, _, sessions, _ := newTestRuntime()
	sessions.detail = agentdomain.SessionDetail{
		Session: agentdomain.Session{ID: 100},
		Running: true,
		ActiveRun: &agentdomain.Run{
			ID:             200,
			Status:         agentdomain.RunRunning,
			TerminalReason: "",
		},
	}

	status, err := runtime.GetSession(context.Background(), 100)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if status.SessionID != 100 || status.Status != "running" {
		t.Fatalf("session status = %+v, want 100/running", status)
	}
	if status.LastRun == nil || status.LastRun.ID != 200 || status.LastRun.Status != "running" {
		t.Fatalf("last run = %+v, want the active run", status.LastRun)
	}
}

func TestGetSessionProjectsTerminalReason(t *testing.T) {
	runtime, _, _, sessions, _ := newTestRuntime()
	sessions.detail = agentdomain.SessionDetail{
		Session: agentdomain.Session{ID: 100},
		LastRun: &agentdomain.Run{
			ID:             200,
			Status:         agentdomain.RunDone,
			TerminalReason: agentdomain.ReasonMaxTurns,
			ErrorMessage:   "turn budget exhausted",
		},
	}

	status, err := runtime.GetSession(context.Background(), 100)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if status.Status != "idle" {
		t.Fatalf("session status = %q, want idle when no run is active", status.Status)
	}
	if status.LastRun == nil {
		t.Fatal("last run is nil")
	}
	if status.LastRun.Status != "done" || status.LastRun.TerminalReason != "max_turns" {
		t.Fatalf("last run = %+v, want done/max_turns", status.LastRun)
	}
	if status.LastRun.Error != "turn budget exhausted" {
		t.Fatalf("last run error = %q", status.LastRun.Error)
	}
}

func TestGetSessionWithoutRun(t *testing.T) {
	runtime, _, _, sessions, _ := newTestRuntime()
	sessions.detail = agentdomain.SessionDetail{Session: agentdomain.Session{ID: 100}}

	status, err := runtime.GetSession(context.Background(), 100)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if status.LastRun != nil {
		t.Fatalf("last run = %+v, want nil", status.LastRun)
	}
}

func TestGetSessionAbortedRunCarriesAbortMessage(t *testing.T) {
	runtime, _, _, sessions, _ := newTestRuntime()
	sessions.detail = agentdomain.SessionDetail{
		Session: agentdomain.Session{ID: 100},
		LastRun: &agentdomain.Run{
			ID:           200,
			Status:       agentdomain.RunAborted,
			AbortMessage: "cancelled by user",
		},
	}

	status, err := runtime.GetSession(context.Background(), 100)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if status.LastRun == nil || status.LastRun.Status != "aborted" {
		t.Fatalf("last run = %+v, want aborted", status.LastRun)
	}
	if status.LastRun.Error != "cancelled by user" {
		t.Fatalf("error = %q, want the abort message", status.LastRun.Error)
	}
}

func TestAbortDelegatesToTheRunService(t *testing.T) {
	runtime, _, _, _, runs := newTestRuntime()

	if err := runtime.Abort(context.Background(), 100, 42); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if runs.abortCalls != 1 {
		t.Fatalf("abort calls = %d, want 1", runs.abortCalls)
	}
	if runs.abortErr = errors.New("boom"); runtime.Abort(context.Background(), 100, 42) == nil {
		t.Fatal("Abort must surface the run service error")
	}
}

func TestProfileModelReturnsTheSeededModel(t *testing.T) {
	runtime, _, _, _, _ := newTestRuntime()

	model, err := runtime.ProfileModel(context.Background(), "issue-triage-agent")
	if err != nil {
		t.Fatalf("ProfileModel: %v", err)
	}
	if model != "deepseek-pro" {
		t.Fatalf("model = %q, want deepseek-pro", model)
	}
	if model, err := runtime.ProfileModel(context.Background(), "failure-analysis-agent"); err != nil || model != "" {
		t.Fatalf("default model = %q/%v, want the empty default LLM model", model, err)
	}
}

func TestProfileModelReportsMissingProfile(t *testing.T) {
	runtime, _, _, _, _ := newTestRuntime()
	if _, err := runtime.ProfileModel(context.Background(), "nope"); err == nil {
		t.Fatal("ProfileModel must report an unknown profile")
	}
}

// TestAnalysisProfilePromptVersions pins the spec §9.1 prompt versions of the two
// seeded profiles.
func TestAnalysisProfilePromptVersions(t *testing.T) {
	seeds := deliverydomain.AnalysisProfiles()
	if len(seeds) != 2 {
		t.Fatalf("seeds = %d, want the failure and triage profiles", len(seeds))
	}
	tests := []struct {
		key     string
		version string
	}{
		{deliverydomain.FailureAnalysisProfileKey, deliverydomain.AnalysisPromptVersionFailure},
		{deliverydomain.IssueTriageProfileKey, deliverydomain.AnalysisPromptVersionTriage},
	}
	for _, test := range tests {
		if got := deliverydomain.AnalysisProfilePromptVersion(test.key); got != test.version {
			t.Fatalf("prompt version of %q = %q, want %q", test.key, got, test.version)
		}
	}
	if got := deliverydomain.AnalysisProfilePromptVersion("nope"); got != "" {
		t.Fatalf("unknown prompt version = %q, want empty", got)
	}
}
