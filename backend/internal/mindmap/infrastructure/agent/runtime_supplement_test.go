package agent_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	mindmapagent "github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/agent"
)

// fakeScopedRuns records both the unscoped and the scoped P1 run starts.
type fakeScopedRuns struct {
	startSession int64
	startMessage string
	startActor   *int64
	startRunID   int64
	startCalls   int

	scopedSession int64
	scopedParts   []agentdomain.Part
	scopedActor   *int64
	scopedScope   agentdomain.RunScope
	scopedRunID   int64
	scopedCalls   int

	abortSession int64
}

// Start implements the plain P1 text run start.
func (f *fakeScopedRuns) Start(_ context.Context, sessionID int64, message string, actor *int64) (agentdomain.StartResult, error) {
	f.startCalls++
	f.startSession, f.startMessage, f.startActor = sessionID, message, actor
	return agentdomain.StartResult{RunID: f.startRunID}, nil
}

// StartWithScope implements the scoped P1 run start.
func (f *fakeScopedRuns) StartWithScope(_ context.Context, sessionID int64, parts []agentdomain.Part, actor *int64, scope agentdomain.RunScope) (agentdomain.StartResult, error) {
	f.scopedCalls++
	f.scopedSession, f.scopedParts, f.scopedActor, f.scopedScope = sessionID, parts, actor, scope
	return agentdomain.StartResult{RunID: f.scopedRunID}, nil
}

// Abort implements the P1 run abort.
func (f *fakeScopedRuns) Abort(_ context.Context, sessionID int64, _ *int64) (agentdomain.AbortResult, error) {
	f.abortSession = sessionID
	return agentdomain.AbortResult{Status: "stopping"}, nil
}

// fakeSupplementStore serves the active supplement run of one version. A
// non-nil err makes every read fail, mimicking a run-store outage.
type fakeSupplementStore struct {
	byKind map[string]mindmapdomain.CaseDesignRun
	err    error
	reads  int
}

func (f *fakeSupplementStore) ActiveByKind(_ context.Context, versionID int64, kind string) (mindmapdomain.CaseDesignRun, error) {
	f.reads++
	if f.err != nil {
		return mindmapdomain.CaseDesignRun{}, f.err
	}
	run, ok := f.byKind[kind]
	if !ok || run.VersionID != versionID {
		return mindmapdomain.CaseDesignRun{}, mindmapdomain.NewNotFound("run", 0)
	}
	return run, nil
}

// supplementSessionProfile returns the resolver map of a supplement-agent run.
func supplementSessionProfile() *fakeProfileResolver {
	return &fakeProfileResolver{byKey: map[string]agentdomain.AgentProfile{
		mindmapapp.ProfileKeySupplement: {ID: 21, Key: mindmapapp.ProfileKeySupplement, Enabled: true},
	}}
}

// TestSupplementRuntimeEnsureProfileSeedsTheSupplementAgent pins the port
// conformance and the idempotent seed of the supplement-agent profile.
func TestSupplementRuntimeEnsureProfileSeedsTheSupplementAgent(t *testing.T) {
	ctx := context.Background()
	seeder := &fakeProfileSeeder{}
	runtime := mindmapagent.NewSupplementRuntime(supplementSessionProfile(), seeder,
		&fakeAgentSessions{}, &fakeScopedRuns{}, &fakeSupplementStore{})
	var _ mindmapapp.AgentRuntime = runtime

	if err := runtime.EnsureProfile(ctx, mindmapdomain.SupplementProfile()); err != nil {
		t.Fatalf("EnsureProfile: %v", err)
	}
	if len(seeder.seeds) != 1 {
		t.Fatalf("seeded %d profiles, want 1", len(seeder.seeds))
	}
	seed := seeder.seeds[0]
	if seed.Key != mindmapapp.ProfileKeySupplement || seed.Builtin || seed.MaxTurns != 20 ||
		seed.RunSeconds != 600 || seed.HardTimeoutSeconds != 120 || seed.Thinking != "disabled" {
		t.Fatalf("seed=%+v want the supplement-agent field set", seed)
	}
	if len(seed.AllowedTools) != 5 || seed.AllowedTools[0] != "supplement.context_read" {
		t.Fatalf("allowed tools=%v want the five supplement tools", seed.AllowedTools)
	}
}

// TestSupplementRuntimeStartsScopedRun pins the P1 StartWithScope contract:
// the run scope carries VersionID/BatchID/SupplementRunID and the session is
// stamped on the version activity channel.
func TestSupplementRuntimeStartsScopedRun(t *testing.T) {
	ctx := context.Background()
	batchID := int64(41)
	pending := mindmapdomain.CaseDesignRun{
		ID: 55, VersionID: 7, Kind: mindmapapp.RunKindSupplement,
		Status: mindmapdomain.RunRunning, BatchID: &batchID,
	}
	sessions := &fakeAgentSessions{session: agentdomain.Session{ID: 88}}
	runs := &fakeScopedRuns{startRunID: 99, scopedRunID: 100}
	store := &fakeSupplementStore{byKind: map[string]mindmapdomain.CaseDesignRun{
		mindmapapp.RunKindSupplement: pending,
	}}
	runtime := mindmapagent.NewSupplementRuntime(supplementSessionProfile(), &fakeProfileSeeder{}, sessions, runs, store)

	sessionID, err := runtime.CreateSession(ctx, mindmapapp.ProfileKeySupplement, "version 7 · supplement", 2, "version:7", "7")
	if err != nil || sessionID != 88 {
		t.Fatalf("CreateSession session=%d err=%v", sessionID, err)
	}
	if sessions.createOptions.ActivityChannel != "version:7" {
		t.Fatalf("activity channel=%q want version:7", sessions.createOptions.ActivityChannel)
	}
	if sessions.createOptions.ActivityVersionID == nil || *sessions.createOptions.ActivityVersionID != "7" {
		t.Fatalf("activity version=%v want 7", sessions.createOptions.ActivityVersionID)
	}

	runID, err := runtime.StartText(ctx, sessionID, "run the supplement", 2)
	if err != nil || runID != 100 {
		t.Fatalf("StartText run=%d err=%v", runID, err)
	}
	if runs.scopedCalls != 1 || runs.startCalls != 0 {
		t.Fatalf("scoped=%d plain=%d want the scoped start only", runs.scopedCalls, runs.startCalls)
	}
	want := agentdomain.RunScope{VersionID: 7, BatchID: 41, SupplementRunID: 55}
	if runs.scopedScope != want {
		t.Fatalf("scope=%+v want %+v", runs.scopedScope, want)
	}
	if runs.scopedSession != sessionID || len(runs.scopedParts) != 1 || runs.scopedParts[0].Text != "run the supplement" {
		t.Fatalf("scoped session=%d parts=%+v", runs.scopedSession, runs.scopedParts)
	}
	if runs.scopedActor == nil || *runs.scopedActor != 2 {
		t.Fatalf("scoped actor=%v want 2", runs.scopedActor)
	}
}

// TestSupplementRuntimeLegacyKindsStayUnscoped pins that the P5 run kinds keep
// the plain StartText behavior through the same adapter.
func TestSupplementRuntimeLegacyKindsStayUnscoped(t *testing.T) {
	ctx := context.Background()
	profiles := &fakeProfileResolver{byKey: map[string]agentdomain.AgentProfile{
		mindmapdomain.ProfileKeyCaseDesign: {ID: 22, Key: mindmapdomain.ProfileKeyCaseDesign, Enabled: true},
	}}
	sessions := &fakeAgentSessions{session: agentdomain.Session{ID: 88}}
	runs := &fakeScopedRuns{startRunID: 99, scopedRunID: 100}
	runtime := mindmapagent.NewSupplementRuntime(profiles, &fakeProfileSeeder{}, sessions, runs, &fakeSupplementStore{})

	sessionID, err := runtime.CreateSession(ctx, mindmapdomain.ProfileKeyCaseDesign, "design", 2, "version:7", "7")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	runID, err := runtime.StartText(ctx, sessionID, "design the cases", 2)
	if err != nil || runID != 99 {
		t.Fatalf("StartText run=%d err=%v", runID, err)
	}
	if runs.scopedCalls != 0 || runs.startCalls != 1 {
		t.Fatalf("scoped=%d plain=%d want the plain start only", runs.scopedCalls, runs.startCalls)
	}
}

// TestSupplementRuntimePropagatesStoreErrors pins the narrow fail-open: only a
// missing pending run (domain.KindNotFound) may fall back to the unscoped start.
// Any other store failure must be propagated with the stable reason (and logged
// with its cause) instead of silently starting an unscoped run.
func TestSupplementRuntimePropagatesStoreErrors(t *testing.T) {
	previous := slog.Default()
	var output bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	defer slog.SetDefault(previous)

	ctx := context.Background()
	sessions := &fakeAgentSessions{session: agentdomain.Session{ID: 88}}
	runs := &fakeScopedRuns{startRunID: 99, scopedRunID: 100}
	store := &fakeSupplementStore{err: errors.New("database unavailable")}
	runtime := mindmapagent.NewSupplementRuntime(supplementSessionProfile(), &fakeProfileSeeder{}, sessions, runs, store)

	sessionID, err := runtime.CreateSession(ctx, mindmapapp.ProfileKeySupplement, "supplement", 2, "version:7", "7")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := runtime.StartText(ctx, sessionID, "run", 2); err == nil ||
		!strings.Contains(err.Error(), "supplement_run_lookup_failed") {
		t.Fatalf("StartText error=%v want the stable supplement_run_lookup_failed reason", err)
	}
	if runs.scopedCalls != 0 || runs.startCalls != 0 {
		t.Fatalf("scoped=%d plain=%d want no start on a store outage", runs.scopedCalls, runs.startCalls)
	}
	if store.reads != 1 {
		t.Fatalf("store reads=%d want the lookup to stop at the first failed kind", store.reads)
	}
	if !strings.Contains(output.String(), "database unavailable") {
		t.Fatalf("the store cause was not logged: %s", output.String())
	}
}

// TestSupplementRuntimeSearchesBothKinds pins that a not-found read of the
// supplement kind still probes the incremental_design kind instead of falling
// back to the unscoped start.
func TestSupplementRuntimeSearchesBothKinds(t *testing.T) {
	ctx := context.Background()
	batchID := int64(41)
	pending := mindmapdomain.CaseDesignRun{
		ID: 55, VersionID: 7, Kind: mindmapapp.RunKindIncrementalDesign,
		Status: mindmapdomain.RunRunning, BatchID: &batchID,
	}
	sessions := &fakeAgentSessions{session: agentdomain.Session{ID: 88}}
	runs := &fakeScopedRuns{startRunID: 99, scopedRunID: 100}
	store := &fakeSupplementStore{byKind: map[string]mindmapdomain.CaseDesignRun{
		mindmapapp.RunKindIncrementalDesign: pending,
	}}
	runtime := mindmapagent.NewSupplementRuntime(supplementSessionProfile(), &fakeProfileSeeder{}, sessions, runs, store)

	sessionID, err := runtime.CreateSession(ctx, mindmapapp.ProfileKeySupplement, "supplement", 2, "version:7", "7")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := runtime.StartText(ctx, sessionID, "run", 2); err != nil {
		t.Fatalf("StartText: %v", err)
	}
	if runs.scopedCalls != 1 || runs.startCalls != 0 {
		t.Fatalf("scoped=%d plain=%d want the scoped start", runs.scopedCalls, runs.startCalls)
	}
	if runs.scopedScope.SupplementRunID != pending.ID || runs.scopedScope.BatchID != batchID {
		t.Fatalf("scope=%+v want the incremental_design run 55", runs.scopedScope)
	}
}

// TestSupplementRuntimeFallsBackWithoutPendingRun pins the fail-open fallback: a
// supplement session whose queued run cannot be found still starts unscoped so
// the run is not degraded on a read race.
func TestSupplementRuntimeFallsBackWithoutPendingRun(t *testing.T) {
	ctx := context.Background()
	sessions := &fakeAgentSessions{session: agentdomain.Session{ID: 88}}
	runs := &fakeScopedRuns{startRunID: 99, scopedRunID: 100}
	runtime := mindmapagent.NewSupplementRuntime(supplementSessionProfile(), &fakeProfileSeeder{}, sessions, runs, &fakeSupplementStore{})

	sessionID, err := runtime.CreateSession(ctx, mindmapapp.ProfileKeySupplement, "supplement", 2, "version:7", "7")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := runtime.StartText(ctx, sessionID, "run", 2); err != nil {
		t.Fatalf("StartText: %v", err)
	}
	if runs.scopedCalls != 0 || runs.startCalls != 1 {
		t.Fatalf("scoped=%d plain=%d want the plain fallback", runs.scopedCalls, runs.startCalls)
	}
}

// TestSupplementRuntimeDefaultsActivityChannel pins the version:{id} activity
// channel even when a caller omits the channel argument.
func TestSupplementRuntimeDefaultsActivityChannel(t *testing.T) {
	ctx := context.Background()
	sessions := &fakeAgentSessions{session: agentdomain.Session{ID: 88}}
	runtime := mindmapagent.NewSupplementRuntime(supplementSessionProfile(), &fakeProfileSeeder{}, sessions,
		&fakeScopedRuns{}, &fakeSupplementStore{})
	if _, err := runtime.CreateSession(ctx, mindmapapp.ProfileKeySupplement, "supplement", 2, "", "7"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sessions.createOptions.ActivityChannel != "version:7" {
		t.Fatalf("activity channel=%q want version:7", sessions.createOptions.ActivityChannel)
	}
}
