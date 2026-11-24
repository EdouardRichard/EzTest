//go:build integration

package executionapp_test

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	executionadapters "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/adapters"
	executorinfra "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/executor"
	executionpg "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	storagepg "github.com/EdouardRichard/EzTest/backend/internal/storage/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// replayCaseReader is the design read port for the replay integration chain: it
// serves one current+confirmed case snapshot (the P2 aggregation input).
type replayCaseReader struct {
	snapshot execution.CaseSnapshot
}

func (r *replayCaseReader) GetCaseVersion(context.Context, int64, int64) (execution.CaseSnapshot, error) {
	return r.snapshot, nil
}

func (r *replayCaseReader) ListExecutable(context.Context, int64) ([]execution.CaseSnapshot, error) {
	return []execution.CaseSnapshot{r.snapshot}, nil
}

func (r *replayCaseReader) ResolveReplacement(context.Context, int64, int64) (execution.CaseSnapshot, bool, error) {
	return execution.CaseSnapshot{}, false, nil
}

func (r *replayCaseReader) CaseVersionLabel(context.Context, int64, int64) (executionapp.CaseVersionLabel, error) {
	return executionapp.CaseVersionLabel{CaseID: r.snapshot.CaseID, CaseVersionID: r.snapshot.CaseVersionID, Title: r.snapshot.Title, Revision: 1, Status: "confirmed"}, nil
}

// replayVersionReader is the version read port the replay fingerprint gate
// recomputes the environment fingerprint / code baseline from. Mutating binding
// simulates an environment change between the source run and the replay.
type replayVersionReader struct {
	binding executionapp.VersionBinding
}

func (r *replayVersionReader) Get(context.Context, int64) (executionapp.VersionBinding, error) {
	return r.binding, nil
}

func (r *replayVersionReader) Reopen(context.Context, int64) error { return nil }

// replayHarness wires the real executor + v2 client + settlement chain + the
// dispatch rows a source run and its replay fence against.
type replayHarness struct {
	pool        *pgxpool.Pool
	ids         *platformid.Snowflake
	repo        *executionpg.Repository
	runner      *executionapp.CaseRunner
	content     executionapp.ContentStore
	versions    *replayVersionReader
	versionID   int64
	sutID       int64
	envID       int64
	caseVersion int64
	intentID    int64
	waveID      int64
	caseRunID   int64
	scope       tools.Scope
	fingerprint string
	deps        executionapp.CaseRunnerDeps
	snapshot    execution.CaseSnapshot
}

func newReplayHarness(t *testing.T) *replayHarness {
	t.Helper()
	pool := testutil.NewDB(t)
	ids, err := platformid.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	repo := executionpg.NewRepository(pool, ids)
	ctx := context.Background()

	dataRoot := t.TempDir()
	settings := &integrationSettings{
		ints: map[string]int64{
			"execution.evidence_max_bytes":            5 << 20,
			"execution.evidence_max_per_case_run":     0,
			"execution.executor_context_ttl_seconds":  300,
			"execution.case_timeout_seconds":          0,
			"execution.human_request_timeout_seconds": 600,
		},
		strings: map[string]string{"paths.data_root": dataRoot},
	}
	storageSvc := storageapp.New(storagepg.New(pool), storagepg.NewFileStore(), settings, ids, nil, nil)
	content := executionadapters.NewContentStore(storageSvc)

	proc := startExecutorProcess(t)
	router := executionadapters.NewExecutorRouter(
		executorinfra.NewResolver(proc.token, executorinfra.DefaultTimeout), nil, proc.endpoint)
	adapter := executionadapters.NewExecutionAdapterWithRouter(router)

	const versionID, sutID, envID, caseVersionID = int64(10), int64(7), int64(3), int64(41)
	intentID, waveID, caseRunID := ids.Next(), ids.Next(), ids.Next()

	versions := &replayVersionReader{binding: executionapp.VersionBinding{
		VersionID: versionID, Status: "running", Stage: "executing",
		SUTs: []executionapp.SUTBinding{{
			SUTID: sutID, EnvironmentID: envID, SUTName: "orders", EnvironmentName: "test",
			BaseURL: "http://127.0.0.1:13335", AccountRoles: []string{"admin"},
		}},
	}}
	fingerprint, err := executionapp.EnvironmentFingerprint(versions.binding)
	if err != nil {
		t.Fatalf("environment fingerprint: %v", err)
	}
	baseline, err := executionapp.CodeBaseline(versions.binding)
	if err != nil {
		t.Fatalf("code baseline: %v", err)
	}

	if err := repo.Intents.Create(ctx, execution.ExecutionIntent{
		ID: intentID, VersionID: versionID, CaseVersionID: caseVersionID,
		SUTIDs: []int64{sutID}, EnvironmentFingerprint: fingerprint, CodeBaseline: baseline,
		Purpose: execution.PurposeInitial, Status: execution.IntentRunning,
		DedupeKey: "exec:replay-round-trip", AttemptNo: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create intent: %v", err)
	}
	if err := repo.Waves.CreateWithCaseRuns(ctx, execution.RunWave{
		ID: waveID, VersionID: versionID, Status: execution.WaveRunning,
		Trigger: execution.WaveTriggerAuto, Counts: execution.WaveCounts{},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}, nil, nil); err != nil {
		t.Fatalf("create wave: %v", err)
	}
	if err := repo.CaseRuns.Create(ctx, execution.CaseRun{
		ID: caseRunID, WaveID: waveID, IntentID: intentID, CaseVersionID: caseVersionID,
		Status: execution.CaseRunRunning, Attempts: 1,
		DispatchEpoch: 3, DispatchToken: "dispatch-token",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create case run: %v", err)
	}

	snapshot := execution.CaseSnapshot{
		CaseID: 5, CaseVersionID: caseVersionID, Title: "echo case",
		Steps: []execution.CaseStep{
			{Seq: 1, Action: "run the echo command", Expected: "hello eztest", AccountRole: "admin"},
		},
		Assertions: []execution.CaseAssertion{
			{StepSeq: 1, AssertionSeq: 1, Kind: execution.AssertionKindText, Expected: "hello eztest"},
		},
	}
	deps := executionapp.CaseRunnerDeps{
		Intents:     repo.Intents,
		CaseRuns:    repo.CaseRuns,
		SUTContexts: integrationSUTContexts{repo: repo.SUTContexts},
		Results:     repo.Results,
		Evidence:    repo.Evidence,
		Content:     content,
		Execution:   adapter,
		Cases:       &replayCaseReader{snapshot: snapshot},
		Versions:    versions,
		Bindings: &fakeBindingResolver{bindings: []execution.SUTBinding{{
			SUTID: sutID, SUTName: "orders", EnvironmentName: "test",
			BaseURL: "http://127.0.0.1:13335", AccountRoles: []string{"admin"},
		}}},
		Settings:   settings,
		Clock:      integrationClock{},
		IDs:        ids,
		InstanceID: "api-integration",
	}
	runner := executionapp.NewCaseRunner(deps)
	scope := tools.Scope{
		VersionID: versionID, WaveID: waveID, CaseRunID: caseRunID, IntentID: intentID,
		CaseVersionID: caseVersionID, DispatchEpoch: 3, DispatchToken: "dispatch-token",
		AgentRunID: ids.Next(),
	}
	return &replayHarness{
		pool: pool, ids: ids, repo: repo, runner: runner, content: content, versions: versions,
		versionID: versionID, sutID: sutID, envID: envID, caseVersion: caseVersionID,
		intentID: intentID, waveID: waveID, caseRunID: caseRunID, scope: scope, fingerprint: fingerprint,
		deps: deps, snapshot: snapshot,
	}
}

// runnerWithIntents rebuilds the case runner over the same chain but with a
// different intent repository (a recording spy), so a test can pin the intent
// lifecycle without touching the production wiring.
func (h *replayHarness) runnerWithIntents(intents executionapp.IntentRepository) *executionapp.CaseRunner {
	deps := h.deps
	deps.Intents = intents
	return executionapp.NewCaseRunner(deps)
}

// recordSourceRun executes a real two-command sequence (echo + file write),
// records the step/assertion and finishes the source run passed. The command
// recordings settle as content-addressed command/file evidence.
func (h *replayHarness) recordSourceRun(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	if _, err := h.runner.Exec(ctx, h.scope, executionapp.ExecCommandInput{
		AccountRole: "admin",
		Command:     executionapp.ExecCommand{Argv: []string{node, "-e", "process.stdout.write('hello eztest')"}},
	}); err != nil {
		t.Fatalf("source Exec echo: %v", err)
	}
	if _, err := h.runner.Exec(ctx, h.scope, executionapp.ExecCommandInput{
		AccountRole: "admin",
		Command:     executionapp.ExecCommand{Argv: []string{node, "-e", "require('fs').writeFileSync('out.txt','data'); process.stdout.write('wrote')"}},
	}); err != nil {
		t.Fatalf("source Exec file write: %v", err)
	}
	if _, err := h.runner.StepRecord(ctx, h.scope, tools.StepRecordInput{Seq: 1, Status: "passed", Observed: "hello eztest"}); err != nil {
		t.Fatalf("source StepRecord: %v", err)
	}
	if _, err := h.runner.AssertionRecord(ctx, h.scope, tools.AssertionRecordInput{StepSeq: 1, AssertionSeq: 1, Verdict: "passed", Actual: "hello eztest"}); err != nil {
		t.Fatalf("source AssertionRecord: %v", err)
	}
	finished, err := h.runner.Finish(ctx, h.scope, execution.FinishCandidate{Verdict: execution.CaseRunPassed})
	if err != nil {
		t.Fatalf("source Finish: %v", err)
	}
	if finished.Status != execution.CaseRunPassed {
		t.Fatalf("source verdict = %s, want passed (summary %+v)", finished.Status, finished.Summary)
	}
}

// TestReplayRoundTripConsistent is the EV-006 keystone acceptance: a recorded
// command log replays under the SAME environment fingerprint + workspace
// snapshot, and the assertions are re-validated SERVER-side (no model run) to a
// verdict consistent with the source run.
func TestReplayRoundTripConsistent(t *testing.T) {
	h := newReplayHarness(t)
	ctx := context.Background()
	h.recordSourceRun(t)

	result, err := h.runner.ReplayCaseRun(ctx, h.versionID, h.caseRunID, 99)
	if err != nil {
		t.Fatalf("ReplayCaseRun: %v", err)
	}
	if result.SourceCaseRunID != h.caseRunID {
		t.Fatalf("replay source = %d, want %d", result.SourceCaseRunID, h.caseRunID)
	}
	if result.ReplayCaseRunID == 0 || result.ReplayCaseRunID == h.caseRunID {
		t.Fatalf("replay must persist a distinct verification run, got %d", result.ReplayCaseRunID)
	}
	if result.CommandTotal != 2 {
		t.Fatalf("replay command total = %d, want 2", result.CommandTotal)
	}
	if result.CommandMatched != result.CommandTotal {
		t.Fatalf("replay matched %d/%d commands, want all (commands %+v)", result.CommandMatched, result.CommandTotal, result.Commands)
	}
	if result.Verdict != execution.CaseRunPassed {
		t.Fatalf("replay verdict = %s, want passed (consistent with source)", result.Verdict)
	}
	if result.InputFingerprint == "" {
		t.Fatal("replay input fingerprint must be computed (constitution art.4.6)")
	}
	// The assertions were re-validated server-side: expected is the frozen case
	// expected (server-owned), and the verdict is derived without any model run.
	if len(result.Assertions) != 1 {
		t.Fatalf("replay assertions = %d, want 1", len(result.Assertions))
	}
	got := result.Assertions[0]
	if got.Verdict != execution.AssertionPassed {
		t.Fatalf("replay assertion verdict = %s, want passed", got.Verdict)
	}
	if got.Expected != "hello eztest" {
		t.Fatalf("replay assertion expected = %q, want the frozen case expected", got.Expected)
	}

	// The replay verification run is persisted with the replay marker linking it
	// to the source and carrying the input fingerprint.
	replayRun, err := h.repo.CaseRuns.Get(ctx, result.ReplayCaseRunID)
	if err != nil {
		t.Fatalf("get replay run: %v", err)
	}
	if replayRun.Summary.Replay == nil {
		t.Fatalf("replay run carries no replay marker: %+v", replayRun.Summary)
	}
	if replayRun.Summary.Replay.SourceCaseRunID != h.caseRunID {
		t.Fatalf("replay marker source = %d, want %d", replayRun.Summary.Replay.SourceCaseRunID, h.caseRunID)
	}
	if replayRun.Summary.Replay.InputFingerprint != result.InputFingerprint {
		t.Fatalf("replay marker fingerprint = %q, want %q", replayRun.Summary.Replay.InputFingerprint, result.InputFingerprint)
	}
	// Replay ≠ retest: the verification run must not shadow the source run in the
	// coverage projection (the authoritative execution verdict stays the source).
	latest, err := h.repo.CaseRuns.LatestByCaseVersions(ctx, h.versionID, []int64{h.caseVersion})
	if err != nil {
		t.Fatalf("LatestByCaseVersions: %v", err)
	}
	if latest[h.caseVersion].ID != h.caseRunID {
		t.Fatalf("coverage latest = %d, want the source run %d (replay must not shadow)", latest[h.caseVersion].ID, h.caseRunID)
	}
	// The replayed commands produced real content-addressed command evidence bound
	// to the verification run.
	rows, err := h.repo.Evidence.ListByCaseRun(ctx, result.ReplayCaseRunID, 50)
	if err != nil {
		t.Fatalf("replay evidence: %v", err)
	}
	commandEvidence := 0
	for _, row := range rows {
		if row.Kind == execution.EvidenceCommand {
			commandEvidence++
		}
	}
	if commandEvidence != 2 {
		t.Fatalf("replay command evidence = %d, want 2", commandEvidence)
	}
}

// TestReplayRejectsFingerprintChange proves the same-fingerprint gate: when the
// environment fingerprint changes between the source run and the replay, the
// replay is rejected with a typed error (never a silent stale replay).
func TestReplayRejectsFingerprintChange(t *testing.T) {
	h := newReplayHarness(t)
	ctx := context.Background()
	h.recordSourceRun(t)

	// Mutate the environment binding so the recomputed fingerprint diverges from
	// the one frozen on the source intent.
	changed := h.versions.binding
	changed.SUTs = append([]executionapp.SUTBinding(nil), changed.SUTs...)
	changed.SUTs[0].BaseURL = "http://127.0.0.1:19999"
	h.versions.binding = changed

	_, err := h.runner.ReplayCaseRun(ctx, h.versionID, h.caseRunID, 99)
	if err == nil {
		t.Fatal("replay under a changed environment fingerprint must be rejected")
	}
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("replay rejection must be a typed platform error, got %T: %v", err, err)
	}
	if apiErr.Code != "replay_fingerprint_changed" {
		t.Fatalf("replay rejection code = %q, want replay_fingerprint_changed", apiErr.Code)
	}
	if apiErr.Status != 409 {
		t.Fatalf("replay rejection status = %d, want 409", apiErr.Status)
	}
}

// TestReplayRejectsRunWithoutCommandLog proves replay needs a recorded command
// log: a terminal run with no command evidence is rejected typed (replay is not
// a retest of an arbitrary run).
func TestReplayRejectsRunWithoutCommandLog(t *testing.T) {
	h := newReplayHarness(t)
	ctx := context.Background()
	// Finish the source run without executing any command: no command evidence.
	if _, err := h.runner.StepRecord(ctx, h.scope, tools.StepRecordInput{Seq: 1, Status: "passed", Observed: "manual"}); err != nil {
		t.Fatalf("StepRecord: %v", err)
	}
	if _, err := h.runner.AssertionRecord(ctx, h.scope, tools.AssertionRecordInput{StepSeq: 1, AssertionSeq: 1, Verdict: "passed", Actual: "hello eztest"}); err != nil {
		t.Fatalf("AssertionRecord: %v", err)
	}
	if _, err := h.runner.Finish(ctx, h.scope, execution.FinishCandidate{Verdict: execution.CaseRunPassed}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	_, err := h.runner.ReplayCaseRun(ctx, h.versionID, h.caseRunID, 99)
	if err == nil {
		t.Fatal("replay of a run with no command log must be rejected")
	}
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "replay_no_command_log" {
		t.Fatalf("replay rejection = %v, want typed replay_no_command_log", err)
	}
	if !strings.Contains(apiErr.Message, "command") {
		t.Fatalf("replay rejection message should name the command log: %q", apiErr.Message)
	}
}

// TestReplayRejectsNonTerminalSource proves replay requires a terminal four-state
// source run: a still-running run is rejected typed (replay is a verification of a
// finished execution, not an interrupt).
func TestReplayRejectsNonTerminalSource(t *testing.T) {
	h := newReplayHarness(t)
	ctx := context.Background()
	// The harness source run is still running (never finished).
	_, err := h.runner.ReplayCaseRun(ctx, h.versionID, h.caseRunID, 99)
	if err == nil {
		t.Fatal("replay of a non-terminal source run must be rejected")
	}
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "replay_not_terminal" {
		t.Fatalf("non-terminal rejection = %v, want typed replay_not_terminal", err)
	}
}

// replayIntentSpy wraps the real intent repository and records the status every
// intent is CREATED with and every Transition applied, so a test can pin that the
// replay intent goes through the legal state-machine path (never born-terminal).
type replayIntentSpy struct {
	inner           executionapp.IntentRepository
	createdStatuses []execution.IntentStatus
	transitions     []intentTransition
}

type intentTransition struct {
	from []execution.IntentStatus
	to   execution.IntentStatus
}

func (s *replayIntentSpy) Create(ctx context.Context, intent execution.ExecutionIntent) error {
	s.createdStatuses = append(s.createdStatuses, intent.Status)
	return s.inner.Create(ctx, intent)
}

// CreateAndTransition records BOTH the created status and the transition the
// atomic composite applies, so the legal-path assertions (created active,
// reached superseded through the state-machine guard) hold for the composite
// exactly as they did for the separate Create+Transition calls.
func (s *replayIntentSpy) CreateAndTransition(ctx context.Context, intent execution.ExecutionIntent, from []execution.IntentStatus, to execution.IntentStatus) error {
	s.createdStatuses = append(s.createdStatuses, intent.Status)
	s.transitions = append(s.transitions, intentTransition{from: from, to: to})
	return s.inner.CreateAndTransition(ctx, intent, from, to)
}

func (s *replayIntentSpy) Get(ctx context.Context, id int64) (execution.ExecutionIntent, error) {
	return s.inner.Get(ctx, id)
}

func (s *replayIntentSpy) List(ctx context.Context, versionID int64, status string, limit int, beforeID *int64) ([]execution.ExecutionIntent, bool, error) {
	return s.inner.List(ctx, versionID, status, limit, beforeID)
}

func (s *replayIntentSpy) ListOpen(ctx context.Context, versionID int64) ([]execution.ExecutionIntent, error) {
	return s.inner.ListOpen(ctx, versionID)
}

func (s *replayIntentSpy) ListActive(ctx context.Context, versionID int64) ([]execution.ExecutionIntent, error) {
	return s.inner.ListActive(ctx, versionID)
}

func (s *replayIntentSpy) Transition(ctx context.Context, id int64, from []execution.IntentStatus, to execution.IntentStatus) (bool, error) {
	s.transitions = append(s.transitions, intentTransition{from: from, to: to})
	return s.inner.Transition(ctx, id, from, to)
}

func (s *replayIntentSpy) CountInitialFor(ctx context.Context, caseVersionID int64, fingerprint, baseline string) (int, error) {
	return s.inner.CountInitialFor(ctx, caseVersionID, fingerprint, baseline)
}

// TestReplayFailedSourceConsistent pins Important 1: a faithfully reproduced
// FAILED source run must confirm the failure (consistency), never report passed.
// The command log reproduces "goodbye" against an expected "hello eztest"; the
// replay verdict reflects the source failure, not a guessed pass (art.3.4/7.6).
func TestReplayFailedSourceConsistent(t *testing.T) {
	h := newReplayHarness(t)
	ctx := context.Background()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	if _, err := h.runner.Exec(ctx, h.scope, executionapp.ExecCommandInput{
		AccountRole: "admin",
		Command:     executionapp.ExecCommand{Argv: []string{node, "-e", "process.stdout.write('goodbye')"}},
	}); err != nil {
		t.Fatalf("source Exec: %v", err)
	}
	if _, err := h.runner.StepRecord(ctx, h.scope, tools.StepRecordInput{Seq: 1, Status: "passed", Observed: "goodbye"}); err != nil {
		t.Fatalf("StepRecord: %v", err)
	}
	if _, err := h.runner.AssertionRecord(ctx, h.scope, tools.AssertionRecordInput{StepSeq: 1, AssertionSeq: 1, Verdict: "failed", Actual: "goodbye"}); err != nil {
		t.Fatalf("AssertionRecord: %v", err)
	}
	finished, err := h.runner.Finish(ctx, h.scope, execution.FinishCandidate{Verdict: execution.CaseRunFailed})
	if err != nil {
		t.Fatalf("source Finish: %v", err)
	}
	if finished.Status != execution.CaseRunFailed {
		t.Fatalf("source verdict = %s, want failed", finished.Status)
	}

	result, err := h.runner.ReplayCaseRun(ctx, h.versionID, h.caseRunID, 99)
	if err != nil {
		t.Fatalf("ReplayCaseRun: %v", err)
	}
	if result.CommandMatched != result.CommandTotal {
		t.Fatalf("the failed source log must reproduce faithfully, matched %d/%d", result.CommandMatched, result.CommandTotal)
	}
	if result.Verdict == execution.CaseRunPassed {
		t.Fatalf("a faithfully reproduced FAILED source must NOT report passed (art.3.4/7.6)")
	}
	if result.Verdict != execution.CaseRunFailed {
		t.Fatalf("replay verdict = %s, want failed (consistency with source)", result.Verdict)
	}
	if result.SourceVerdict != execution.CaseRunFailed {
		t.Fatalf("replay source verdict = %s, want failed", result.SourceVerdict)
	}
	if !result.Consistent {
		t.Fatalf("a faithful reproduction of the source failure must be consistent")
	}
	if len(result.Assertions) != 1 || result.Assertions[0].Verdict != execution.AssertionFailed {
		t.Fatalf("replay assertion must confirm the source failure, got %+v", result.Assertions)
	}
}

// TestReplayIntentGoesThroughLegalTransition pins Important 2: the replay anchor
// intent is created ACTIVE (open) and reaches its terminal through the state
// machine (Transition open->superseded, matching completeIntent's superseded
// projection), never born-terminal via Create (art.3.6).
func TestReplayIntentGoesThroughLegalTransition(t *testing.T) {
	h := newReplayHarness(t)
	ctx := context.Background()
	h.recordSourceRun(t)

	spy := &replayIntentSpy{inner: h.repo.Intents}
	runner := h.runnerWithIntents(spy)
	result, err := runner.ReplayCaseRun(ctx, h.versionID, h.caseRunID, 99)
	if err != nil {
		t.Fatalf("ReplayCaseRun: %v", err)
	}

	// The domain pins the legal path: open->superseded is legal, open->done is not.
	if !execution.IntentOpen.CanTransitionTo(execution.IntentSuperseded) {
		t.Fatal("open->superseded must be a legal intent transition")
	}
	if execution.IntentOpen.CanTransitionTo(execution.IntentDone) {
		t.Fatal("open->done must be illegal (the born-terminal bug)")
	}
	// The replay intent was created ACTIVE, not born-terminal.
	if len(spy.createdStatuses) != 1 {
		t.Fatalf("replay must create exactly one intent, got %d", len(spy.createdStatuses))
	}
	if !spy.createdStatuses[0].Active() {
		t.Fatalf("replay intent created with terminal status %q; must be created active then transitioned", spy.createdStatuses[0])
	}
	// It reached superseded through a Transition (the unique terminal entry).
	foundTransition := false
	for _, tr := range spy.transitions {
		if tr.to == execution.IntentSuperseded {
			foundTransition = true
		}
	}
	if !foundTransition {
		t.Fatalf("replay intent must transition to superseded, transitions %+v", spy.transitions)
	}
	// The persisted terminal matches the authoritative superseded projection.
	anchor, err := h.repo.CaseRuns.Get(ctx, result.ReplayCaseRunID)
	if err != nil {
		t.Fatalf("get anchor: %v", err)
	}
	replayIntent, err := h.repo.Intents.Get(ctx, anchor.IntentID)
	if err != nil {
		t.Fatalf("get replay intent: %v", err)
	}
	if replayIntent.Status != execution.IntentSuperseded {
		t.Fatalf("replay intent terminal = %s, want superseded", replayIntent.Status)
	}
	if anchor.Status != execution.CaseRunSuperseded {
		t.Fatalf("anchor status = %s, want superseded", anchor.Status)
	}
	// T22 atomicity: the composite create+transition means the replay intent is
	// never observable as open work the planner could claim into a retest wave.
	open, err := h.repo.Intents.ListOpen(ctx, h.versionID)
	if err != nil {
		t.Fatalf("list open intents: %v", err)
	}
	for _, row := range open {
		if row.ID == replayIntent.ID {
			t.Fatal("the replay intent must never be visible to the planner as open work")
		}
	}
}

// TestReplayRejectsMultiSessionSource pins Important 3: a source run with more
// than one executor session (two account roles) is outside replay v1's
// single-session scope and is rejected typed, never silently replayed into one
// session's workspace/credentials.
func TestReplayRejectsMultiSessionSource(t *testing.T) {
	h := newReplayHarness(t)
	ctx := context.Background()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	if _, err := h.runner.Exec(ctx, h.scope, executionapp.ExecCommandInput{
		AccountRole: "admin",
		Command:     executionapp.ExecCommand{Argv: []string{node, "-e", "process.stdout.write('hello eztest')"}},
	}); err != nil {
		t.Fatalf("source Exec admin: %v", err)
	}
	if _, err := h.runner.Exec(ctx, h.scope, executionapp.ExecCommandInput{
		AccountRole: "guest",
		Command:     executionapp.ExecCommand{Argv: []string{node, "-e", "process.stdout.write('second session')"}},
	}); err != nil {
		t.Fatalf("source Exec guest: %v", err)
	}
	if _, err := h.runner.StepRecord(ctx, h.scope, tools.StepRecordInput{Seq: 1, Status: "passed", Observed: "hello eztest"}); err != nil {
		t.Fatalf("StepRecord: %v", err)
	}
	if _, err := h.runner.AssertionRecord(ctx, h.scope, tools.AssertionRecordInput{StepSeq: 1, AssertionSeq: 1, Verdict: "passed", Actual: "hello eztest"}); err != nil {
		t.Fatalf("AssertionRecord: %v", err)
	}
	if _, err := h.runner.Finish(ctx, h.scope, execution.FinishCandidate{Verdict: execution.CaseRunPassed}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	_, err = h.runner.ReplayCaseRun(ctx, h.versionID, h.caseRunID, 99)
	if err == nil {
		t.Fatal("a multi-session source must be rejected typed (v1 single-session scope)")
	}
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "replay_unsupported_command_log" {
		t.Fatalf("multi-session rejection = %v, want typed replay_unsupported_command_log", err)
	}
}

// TestReplaySeedsWorkspaceSnapshot pins Important 4: an input file uploaded BEFORE
// the first command is part of the workspace snapshot and must be seeded into the
// replay workspace so a command that READS it reproduces faithfully. Exercises the
// upload/seed/settle leg of EV-006 (同指纹+workspace 快照重放).
func TestReplaySeedsWorkspaceSnapshot(t *testing.T) {
	h := newReplayHarness(t)
	ctx := context.Background()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	// Upload the input file BEFORE any command: its file evidence settles before
	// the first command evidence, so it is the pre-execution workspace snapshot.
	if _, err := h.runner.FileOp(ctx, h.scope, tools.FileOpInput{
		Op: "upload", Path: "input.txt", Content: "hello eztest", Encoding: "text", AccountRole: "admin",
	}); err != nil {
		t.Fatalf("source upload input.txt: %v", err)
	}
	if _, err := h.runner.Exec(ctx, h.scope, executionapp.ExecCommandInput{
		AccountRole: "admin",
		Command:     executionapp.ExecCommand{Argv: []string{node, "-e", "process.stdout.write(require('fs').readFileSync('input.txt','utf8'))"}},
	}); err != nil {
		t.Fatalf("source Exec read: %v", err)
	}
	if _, err := h.runner.StepRecord(ctx, h.scope, tools.StepRecordInput{Seq: 1, Status: "passed", Observed: "hello eztest"}); err != nil {
		t.Fatalf("StepRecord: %v", err)
	}
	if _, err := h.runner.AssertionRecord(ctx, h.scope, tools.AssertionRecordInput{StepSeq: 1, AssertionSeq: 1, Verdict: "passed", Actual: "hello eztest"}); err != nil {
		t.Fatalf("AssertionRecord: %v", err)
	}
	if _, err := h.runner.Finish(ctx, h.scope, execution.FinishCandidate{Verdict: execution.CaseRunPassed}); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	result, err := h.runner.ReplayCaseRun(ctx, h.versionID, h.caseRunID, 99)
	if err != nil {
		t.Fatalf("ReplayCaseRun: %v", err)
	}
	// The reading command reproduced faithfully ONLY if the input file was seeded.
	if result.CommandMatched != result.CommandTotal {
		t.Fatalf("the reading command must reproduce (input seeded), matched %d/%d (%+v)", result.CommandMatched, result.CommandTotal, result.Commands)
	}
	if result.Verdict != execution.CaseRunPassed {
		t.Fatalf("replay verdict = %s, want passed (seeded input reproduces)", result.Verdict)
	}
	// The seeded input file is itself settled as file evidence on the replay run.
	rows, err := h.repo.Evidence.ListByCaseRun(ctx, result.ReplayCaseRunID, 50)
	if err != nil {
		t.Fatalf("replay evidence: %v", err)
	}
	seeded := false
	for _, row := range rows {
		if row.Kind == execution.EvidenceFile && row.Filename == "input.txt" {
			seeded = true
		}
	}
	if !seeded {
		t.Fatalf("the replay must seed input.txt as file evidence, rows %+v", rows)
	}
}
