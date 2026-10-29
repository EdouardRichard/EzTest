package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	deliverydomain "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	deliveryagent "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/agent"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// testWiringLogger returns a logger that discards startup seed output.
func testWiringLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeWiringTool is a registered tool with no behavior, used to build a live
// registry for the startup profile validation.
type fakeWiringTool struct{ name string }

func (t fakeWiringTool) Name() string                 { return t.name }
func (t fakeWiringTool) Description() string          { return "" }
func (t fakeWiringTool) InputSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t fakeWiringTool) Execute(context.Context, json.RawMessage) (agentdomain.ToolResult, error) {
	return agentdomain.ToolResult{}, nil
}

// fakeProfilePatcher records the idempotent P7 profile update.
type fakeProfilePatcher struct {
	profile   agentdomain.AgentProfile
	getErr    error
	updateErr error
	updates   []agentdomain.UpdateParams
}

func (f *fakeProfilePatcher) GetByKey(context.Context, string) (agentdomain.AgentProfile, error) {
	if f.getErr != nil {
		return agentdomain.AgentProfile{}, f.getErr
	}
	return f.profile, nil
}

func (f *fakeProfilePatcher) Update(_ context.Context, id int64, params agentdomain.UpdateParams, _ *int64) (agentdomain.AgentProfile, error) {
	if f.updateErr != nil {
		return agentdomain.AgentProfile{}, f.updateErr
	}
	if id != f.profile.ID {
		return agentdomain.AgentProfile{}, errors.New("unexpected profile id")
	}
	if err := f.profile.Apply(params); err != nil {
		return agentdomain.AgentProfile{}, err
	}
	f.updates = append(f.updates, params)
	return f.profile, nil
}

// TestEnsureExecutionDBAssertAppendsMissingToolAndRule covers the spec §9.1
// startup upgrade: the already-seeded execution-agent profile gains
// execution.db_assert and the prompt rule only when they are missing.
func TestEnsureExecutionDBAssertAppendsMissingToolAndRule(t *testing.T) {
	seed := executiondomain.ExecutionProfile()
	stored := seed
	stored.ID = 7
	stored.AllowedTools = []string{"execution.case_context"}
	stored.SystemPrompt = "admin-edited prompt"
	patcher := &fakeProfilePatcher{profile: stored}

	if err := ensureExecutionDBAssert(context.Background(), patcher, testWiringLogger()); err != nil {
		t.Fatalf("ensureExecutionDBAssert: %v", err)
	}
	if len(patcher.updates) != 1 {
		t.Fatalf("updates=%d want 1", len(patcher.updates))
	}
	params := patcher.updates[0]
	if params.AllowedTools == nil {
		t.Fatal("allowed tools were not updated")
	}
	tools := *params.AllowedTools
	if len(tools) != 2 || tools[0] != "execution.case_context" || tools[1] != executiondomain.ExecutionDBAssertTool {
		t.Fatalf("allowed tools=%v want [execution.case_context %s]", tools, executiondomain.ExecutionDBAssertTool)
	}
	if params.SystemPrompt == nil || !strings.Contains(*params.SystemPrompt, "admin-edited prompt") ||
		!strings.Contains(*params.SystemPrompt, executionDBAssertPromptRule) {
		t.Fatalf("system prompt=%q want the admin text plus the db rule", derefString(params.SystemPrompt))
	}
	if !strings.Contains(patcher.profile.SystemPrompt, executionDBAssertPromptRule) {
		t.Fatal("stored prompt does not carry the appended rule")
	}

	// Idempotent: a second startup performs no update.
	if err := ensureExecutionDBAssert(context.Background(), patcher, testWiringLogger()); err != nil {
		t.Fatalf("second ensureExecutionDBAssert: %v", err)
	}
	if len(patcher.updates) != 1 {
		t.Fatalf("updates after second run=%d want 1", len(patcher.updates))
	}
}

// TestEnsureExecutionDBAssertKeepsCompleteProfile covers a profile that already
// allows the tool and mentions it in the prompt: no write at all.
func TestEnsureExecutionDBAssertKeepsCompleteProfile(t *testing.T) {
	stored := executiondomain.ExecutionProfile()
	stored.ID = 9
	stored.SystemPrompt = "custom prompt using " + executiondomain.ExecutionDBAssertTool
	patcher := &fakeProfilePatcher{profile: stored}
	if err := ensureExecutionDBAssert(context.Background(), patcher, testWiringLogger()); err != nil {
		t.Fatalf("ensureExecutionDBAssert: %v", err)
	}
	if len(patcher.updates) != 0 {
		t.Fatalf("updates=%d want none for a complete profile", len(patcher.updates))
	}
}

// TestEnsureExecutionDBAssertFailsOnReadError pins the fail-loud startup: a
// profile read failure must abort the seed instead of silently skipping the
// db_assert capability.
func TestEnsureExecutionDBAssertFailsOnReadError(t *testing.T) {
	patcher := &fakeProfilePatcher{getErr: errors.New("db down")}
	if err := ensureExecutionDBAssert(context.Background(), patcher, testWiringLogger()); err == nil {
		t.Fatal("want an error when the profile cannot be read")
	}
}

// fakeSupplemenRuntime records the supplement profile seed.
type fakeSupplementRuntime struct {
	seeds []mindmapdomain.AgentProfileSeed
}

func (f *fakeSupplementRuntime) EnsureProfile(_ context.Context, seed mindmapapp.ProfileSeed) error {
	f.seeds = append(f.seeds, seed)
	return nil
}
func (f *fakeSupplementRuntime) CreateSession(context.Context, string, string, int64, string, string) (int64, error) {
	return 0, nil
}
func (f *fakeSupplementRuntime) StartText(context.Context, int64, string, int64) (int64, error) {
	return 0, nil
}
func (f *fakeSupplementRuntime) GetSession(context.Context, int64) (mindmapapp.AgentSessionStatus, error) {
	return mindmapapp.AgentSessionStatus{}, nil
}
func (f *fakeSupplementRuntime) Abort(context.Context, int64, int64) error { return nil }

// TestEnsureSupplementProfileValidatesAndSeeds covers the P7 supplement seed: a
// missing tool fails the startup with the tool name, a complete registry seeds
// the profile exactly once per call.
func TestEnsureSupplementProfileValidatesAndSeeds(t *testing.T) {
	seed := mindmapdomain.SupplementProfile()
	allowed := mindmapdomain.SupplementAllowedTools()
	if len(allowed) == 0 {
		t.Fatal("supplement profile allows no tools")
	}

	missing := toolRegistry(allowed[:len(allowed)-1])
	runtime := &fakeSupplementRuntime{}
	if err := ensureSupplementProfile(context.Background(), runtime, missing, testWiringLogger()); err == nil ||
		!strings.Contains(err.Error(), allowed[len(allowed)-1]) {
		t.Fatalf("err=%v want a missing tool name %q", err, allowed[len(allowed)-1])
	}
	if len(runtime.seeds) != 0 {
		t.Fatalf("seeds=%d want none when validation fails", len(runtime.seeds))
	}

	complete := toolRegistry(allowed)
	if err := ensureSupplementProfile(context.Background(), runtime, complete, testWiringLogger()); err != nil {
		t.Fatalf("ensureSupplementProfile: %v", err)
	}
	if len(runtime.seeds) != 1 || runtime.seeds[0].Key != seed.Key {
		t.Fatalf("seeds=%v want one seed with key %q", runtime.seeds, seed.Key)
	}
}

// fakeProfileSeeder inserts a profile only when its key is missing, mirroring
// the P1 EnsureBuiltin writer.
type fakeProfileSeeder struct {
	rows map[string]agentdomain.AgentProfile
}

func newFakeProfileSeeder() *fakeProfileSeeder {
	return &fakeProfileSeeder{rows: make(map[string]agentdomain.AgentProfile)}
}

func (s *fakeProfileSeeder) EnsureBuiltin(_ context.Context, profile agentdomain.AgentProfile) (bool, error) {
	if _, exists := s.rows[profile.Key]; exists {
		return false, nil
	}
	s.rows[profile.Key] = profile
	return true, nil
}

// TestEnsureDeliveryProfilesValidatesAndSeedsIdempotently covers the two P7
// delivery seeds: an unregistered tool fails the startup loudly and the seed is
// idempotent (the second call writes nothing).
func TestEnsureDeliveryProfilesValidatesAndSeedsIdempotently(t *testing.T) {
	seeds := deliverydomain.AnalysisProfiles()
	var tools []string
	for _, seed := range seeds {
		tools = append(tools, seed.AllowedTools...)
	}
	seeder := newFakeProfileSeeder()
	runtime := deliveryagent.NewAgentRuntime(nil, seeder, nil, nil)

	// A missing tool must fail with its name before anything is written.
	missing := toolRegistry(tools[:len(tools)-1])
	err := ensureDeliveryProfiles(context.Background(), runtime, missing, testWiringLogger())
	if err == nil || !strings.Contains(err.Error(), tools[len(tools)-1]) {
		t.Fatalf("err=%v want the missing tool name %q", err, tools[len(tools)-1])
	}
	if len(seeder.rows) != 0 {
		t.Fatalf("rows=%d want none when the registry is incomplete", len(seeder.rows))
	}

	complete := toolRegistry(tools)
	if err := ensureDeliveryProfiles(context.Background(), runtime, complete, testWiringLogger()); err != nil {
		t.Fatalf("ensureDeliveryProfiles: %v", err)
	}
	if len(seeder.rows) != len(seeds) {
		t.Fatalf("rows=%d want %d", len(seeder.rows), len(seeds))
	}
	for _, seed := range seeds {
		stored, exists := seeder.rows[seed.Key]
		if !exists {
			t.Fatalf("profile %q was not seeded", seed.Key)
		}
		if !sameStrings(stored.AllowedTools, seed.AllowedTools) {
			t.Fatalf("profile %q tools=%v want %v", seed.Key, stored.AllowedTools, seed.AllowedTools)
		}
	}
	if err := ensureDeliveryProfiles(context.Background(), runtime, complete, testWiringLogger()); err != nil {
		t.Fatalf("second ensureDeliveryProfiles: %v", err)
	}
	if len(seeder.rows) != len(seeds) {
		t.Fatalf("rows after second run=%d want %d", len(seeder.rows), len(seeds))
	}
}

// toolRegistry builds a live registry with one fake tool per name.
func toolRegistry(names []string) *agentapp.ToolRegistry {
	tools := make([]agentdomain.Tool, 0, len(names))
	unique := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, duplicate := unique[name]; duplicate {
			continue
		}
		unique[name] = struct{}{}
		tools = append(tools, fakeWiringTool{name: name})
	}
	return agentapp.NewToolRegistry(tools...)
}

// sameStrings reports whether a and b hold the same values in the same order.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// derefString unwraps an optional string for failure messages.
func derefString(value *string) string {
	if value == nil {
		return "<nil>"
	}
	return *value
}

// TestManualAnalysisServiceReopensSettledChain pins the manual-analysis reset:
// a successful start on a live version reopens the batch chain, a failed start
// leaves it alone and a reset failure does not fail the committed run.
func TestManualAnalysisServiceReopensSettledChain(t *testing.T) {
	run := deliverydomain.AnalysisRun{ID: 51, VersionID: 7, BatchID: 41, Kind: deliverydomain.AnalysisKindFailure}
	analyses := &fakeManualAnalyses{run: run}
	resetter := &fakeChainResetter{}
	versions := &fakeVersionStatusReader{version: &versionapp.Version{ID: 7, Status: versiondomain.VersionExecuting}}
	service := manualAnalysisService{analyses: analyses, worker: resetter, versions: versions, logger: testWiringLogger()}

	started, err := service.StartFailure(context.Background(), 7, 41, 1)
	if err != nil || started.ID != run.ID {
		t.Fatalf("StartFailure=%+v/%v want run 51", started, err)
	}
	if len(resetter.calls) != 1 || resetter.calls[0] != 41 {
		t.Fatalf("reset calls=%v want one reset of batch 41", resetter.calls)
	}
	if len(versions.ids) != 1 || versions.ids[0] != 7 {
		t.Fatalf("version reads=%v want one read of version 7", versions.ids)
	}

	analyses.startErr = errors.New("start failed")
	if _, err := service.StartFailure(context.Background(), 7, 41, 1); err == nil {
		t.Fatal("want the start error")
	}
	if len(resetter.calls) != 1 {
		t.Fatalf("reset calls=%v want no reset after a failed start", resetter.calls)
	}

	analyses.startErr = nil
	resetter.err = errors.New("reset failed")
	started, err = service.StartFailure(context.Background(), 7, 41, 1)
	if err != nil || started.ID != run.ID {
		t.Fatalf("reset failure must not fail the committed run: %+v/%v", started, err)
	}
}

// TestManualAnalysisServiceLeavesCompletedVersionChainSettled pins the Lead
// ruling: a completed version is terminal/immutable, so the manual analysis run
// is still created but autonomy_done is not reset (the settled chain stays
// settled).
func TestManualAnalysisServiceLeavesCompletedVersionChainSettled(t *testing.T) {
	run := deliverydomain.AnalysisRun{ID: 61, VersionID: 8, BatchID: 42, Kind: deliverydomain.AnalysisKindFailure}
	analyses := &fakeManualAnalyses{run: run}
	resetter := &fakeChainResetter{}
	versions := &fakeVersionStatusReader{version: &versionapp.Version{ID: 8, Status: versiondomain.VersionCompleted}}
	service := manualAnalysisService{analyses: analyses, worker: resetter, versions: versions, logger: testWiringLogger()}

	started, err := service.StartFailure(context.Background(), 8, 42, 1)
	if err != nil || started.ID != run.ID {
		t.Fatalf("StartFailure on a completed version=%+v/%v want the committed run", started, err)
	}
	if len(resetter.calls) != 0 {
		t.Fatalf("reset calls=%v want none for a completed version", resetter.calls)
	}
	if len(versions.ids) != 1 || versions.ids[0] != 8 {
		t.Fatalf("version reads=%v want one read of version 8", versions.ids)
	}
}

// TestManualAnalysisServiceLeavesChainSettledWhenVersionCannotBeRead defines the
// fail-closed branch: when the version status cannot be determined, the run is
// still committed but the chain is never reopened (it could be a completed
// version).
func TestManualAnalysisServiceLeavesChainSettledWhenVersionCannotBeRead(t *testing.T) {
	run := deliverydomain.AnalysisRun{ID: 62, VersionID: 9, BatchID: 43, Kind: deliverydomain.AnalysisKindFailure}
	analyses := &fakeManualAnalyses{run: run}
	resetter := &fakeChainResetter{}
	versions := &fakeVersionStatusReader{err: errors.New("db down")}
	service := manualAnalysisService{analyses: analyses, worker: resetter, versions: versions, logger: testWiringLogger()}

	started, err := service.StartFailure(context.Background(), 9, 43, 1)
	if err != nil || started.ID != run.ID {
		t.Fatalf("StartFailure with a version read error=%+v/%v want the committed run", started, err)
	}
	if len(resetter.calls) != 0 {
		t.Fatalf("reset calls=%v want none when the version status is unknown", resetter.calls)
	}
}

// fakeManualAnalyses drives the manual analysis wrapper.
type fakeManualAnalyses struct {
	run      deliverydomain.AnalysisRun
	startErr error
}

func (f *fakeManualAnalyses) StartFailure(context.Context, int64, int64, int64) (deliverydomain.AnalysisRun, error) {
	return f.run, f.startErr
}

func (f *fakeManualAnalyses) Get(context.Context, int64, int64, int64) (deliverydomain.AnalysisRun, error) {
	return f.run, nil
}

func (f *fakeManualAnalyses) List(context.Context, int64, int64) ([]deliverydomain.AnalysisRun, error) {
	return []deliverydomain.AnalysisRun{f.run}, nil
}

// fakeChainResetter records autonomy_done resets.
type fakeChainResetter struct {
	calls []int64
	err   error
}

func (f *fakeChainResetter) ResetAutonomyDone(_ context.Context, batchID int64) (bool, error) {
	f.calls = append(f.calls, batchID)
	if f.err != nil {
		return false, f.err
	}
	return true, nil
}

// fakeVersionStatusReader resolves the version status the manual-analysis
// wrapper consults before reopening a settled autonomy chain.
type fakeVersionStatusReader struct {
	version *versionapp.Version
	err     error
	ids     []int64
}

func (f *fakeVersionStatusReader) Get(_ context.Context, id int64) (*versionapp.Version, error) {
	f.ids = append(f.ids, id)
	if f.err != nil {
		return nil, f.err
	}
	return f.version, nil
}

// TestRequirementGapDeliveryPortIsBound drives the composition-root helper that
// binds T17a's requirement-gap escalation adapter to the delivery issue service:
// the execution worker's escalation payload must be projected and written
// through the delivery issue manager (which now validates the version through
// T2b's reader).
func TestRequirementGapDeliveryPortIsBound(t *testing.T) {
	writer := &fakeRequirementGapWriter{
		issues: []deliverydomain.Issue{{ID: 901}, {ID: 902}},
	}
	port := newAutonomyDeliveryPort(writer)
	if port == nil {
		t.Fatal("newAutonomyDeliveryPort returned nil")
	}
	escalations := []executionapp.RequirementGapEscalation{{
		Title:          "Missing checkout retry coverage",
		Detail:         "the supplement agent found no case covering the checkout retry",
		RequirementIDs: []int64{56, 78},
	}}
	count, err := port.CreateRequirementGapIssues(context.Background(), 12, 34, escalations, 9)
	if err != nil || count != 2 {
		t.Fatalf("CreateRequirementGapIssues=%d/%v want 2/nil", count, err)
	}
	if writer.versionID != 12 || writer.batchID != 34 || writer.actor != 9 {
		t.Fatalf("delivery write=(version=%d batch=%d actor=%d) want (12, 34, 9)",
			writer.versionID, writer.batchID, writer.actor)
	}
	if len(writer.gaps) != 1 {
		t.Fatalf("delivery escalations=%v want exactly one", writer.gaps)
	}
	gap := writer.gaps[0]
	if gap.Title != escalations[0].Title || gap.Detail != escalations[0].Detail ||
		!sameInt64s(gap.RequirementIDs, escalations[0].RequirementIDs) {
		t.Fatalf("projected escalation=%+v want %+v", gap, escalations[0])
	}
}

// fakeRequirementGapWriter captures the delivery-side requirement-gap write.
type fakeRequirementGapWriter struct {
	issues    []deliverydomain.Issue
	err       error
	versionID int64
	batchID   int64
	actor     int64
	gaps      []deliveryapp.RequirementGapEscalation
}

func (f *fakeRequirementGapWriter) ApplyRequirementGaps(_ context.Context, versionID, batchID, actor int64,
	escalations []deliveryapp.RequirementGapEscalation) ([]deliverydomain.Issue, error) {
	f.versionID, f.batchID, f.actor = versionID, batchID, actor
	f.gaps = append([]deliveryapp.RequirementGapEscalation(nil), escalations...)
	return f.issues, f.err
}

// sameInt64s reports whether a and b hold the same ids in the same order.
func sameInt64s(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// fakeP7WorkerStop records one P7 loop stop in the shared ordering log.
type fakeP7WorkerStop struct {
	name   string
	events *[]string
}

func (f *fakeP7WorkerStop) Stop() { *f.events = append(*f.events, f.name) }

// TestStopP7WorkerLoopsCancelsThenStopsBothLoops pins the worker stop contract
// the shutdown path relies on: the shared context is cancelled first and both
// P7 loops are stopped exactly once. The real ordering call site is before
// runService.Shutdown (see cmd/eztest/main.go shutdown path).
func TestStopP7WorkerLoopsCancelsThenStopsBothLoops(t *testing.T) {
	var events []string
	cancel := func() { events = append(events, "cancel") }
	autonomy := &fakeP7WorkerStop{name: "autonomy", events: &events}
	analysis := &fakeP7WorkerStop{name: "analysis", events: &events}

	stopP7WorkerLoops(cancel, autonomy, analysis)
	if want := []string{"cancel", "autonomy", "analysis"}; !sameStrings(events, want) {
		t.Fatalf("stop order=%v want %v", events, want)
	}

	// A partially wired process still shuts down: nil actions are skipped.
	stopP7WorkerLoops(nil, nil, nil)
}

// TestToolRegistryHelperIsDeterministic guards the test helper: duplicate names
// in the seed allowlists must not break the fake registry.
func TestToolRegistryHelperIsDeterministic(t *testing.T) {
	names := []string{"b", "a", "b"}
	registry := toolRegistry(names)
	got := make([]string, 0)
	for _, name := range names {
		if _, ok := registry.Get(name); ok {
			got = append(got, name)
		}
	}
	sort.Strings(got)
	if len(got) != len(names) {
		t.Fatalf("registered names=%v want every requested name", got)
	}
}
