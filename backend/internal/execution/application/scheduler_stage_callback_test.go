package executionapp

// v0.22 Task 25 (R-B4-3): the scheduler must emit the production
// wave-terminal callback so a version can move executing -> reporting without a
// manual stage API call. The second test wires the real *versionapp.StageService
// behind the scheduler's narrow notifier port and proves the whole
// wave-terminal -> reporting -> report-published -> completed chain.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

type recordingWaveStageNotifier struct {
	mu    sync.Mutex
	waves []int64
	err   error
}

func (n *recordingWaveStageNotifier) OnWaveTerminal(_ context.Context, waveID int64) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.waves = append(n.waves, waveID)
	return n.err
}

func (n *recordingWaveStageNotifier) calls() []int64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]int64(nil), n.waves...)
}

// stageServiceWaveLookup adapts the scheduler fixture's wave store to the stage
// service's production wave-terminal callback port.
type stageServiceWaveLookup struct{ f *task6Fixture }

func (l stageServiceWaveLookup) GetWave(ctx context.Context, waveID int64) (versionapp.StageWave, error) {
	wave, err := l.f.waves.Get(ctx, waveID)
	if err != nil {
		return versionapp.StageWave{}, err
	}
	return versionapp.StageWave{
		ID:        wave.ID,
		VersionID: wave.VersionID,
		Status:    string(wave.Status),
		Terminal:  wave.Status.Terminal(),
	}, nil
}

type stageServiceRepo struct {
	mu      sync.Mutex
	state   version.StageState
	applied []version.StageState
}

func (r *stageServiceRepo) GetStage(context.Context, int64) (version.StageState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state, nil
}

func (r *stageServiceRepo) ApplyStage(_ context.Context, expected version.Stage, next version.StageState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state.Stage != expected {
		return version.ErrStageConflict
	}
	r.state = next
	r.applied = append(r.applied, next)
	return nil
}

func (r *stageServiceRepo) appliedStages() []version.Stage {
	r.mu.Lock()
	defer r.mu.Unlock()
	stages := make([]version.Stage, 0, len(r.applied))
	for _, state := range r.applied {
		stages = append(stages, state.Stage)
	}
	return stages
}

func (r *stageServiceRepo) currentStage() version.Stage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state.Stage
}

type recordingReportEnsurer struct {
	mu       sync.Mutex
	versions []int64
}

func (r *recordingReportEnsurer) EnsureVersionReport(_ context.Context, versionID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.versions = append(r.versions, versionID)
	return nil
}

type recordingVersionCloser struct {
	mu       sync.Mutex
	versions []int64
	reasons  []string
}

func (c *recordingVersionCloser) CloseVersion(_ context.Context, versionID int64, reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.versions = append(c.versions, versionID)
	c.reasons = append(c.reasons, reason)
	return nil
}

// TestSchedulerNotifiesStageOnWaveTerminal pins the production call site: a
// wave that becomes done notifies the stage callback exactly once.
func TestSchedulerNotifiesStageOnWaveTerminal(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.versions.bindings[100] = task6Binding(100, "running", 5001)
	if _, err := f.intent.EnsureInitialIntents(ctx, 100); err != nil {
		t.Fatalf("EnsureInitialIntents: %v", err)
	}
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("dispatch ReconcileVersion: %v", err)
	}
	wave, _ := f.waves.GetActive(ctx, 100)
	if wave == nil {
		t.Fatal("wave not created")
	}
	runs, _ := f.runs.ListAllByWave(ctx, wave.ID)
	if len(runs) != 1 {
		t.Fatalf("case runs = %d, want 1", len(runs))
	}
	run, _ := f.runs.Get(ctx, runs[0].ID)
	run.Status = execution.CaseRunPassed
	run.FailureClass = ""
	if ok, err := f.runs.Update(ctx, run); err != nil || !ok {
		t.Fatalf("finish run: %v %v", ok, err)
	}

	notifier := &recordingWaveStageNotifier{}
	f.scheduler.stageNotifier = notifier
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("completion ReconcileVersion: %v", err)
	}
	finished, err := f.waves.Get(ctx, wave.ID)
	if err != nil || finished.Status != execution.WaveDone {
		t.Fatalf("wave = %+v err=%v, want done", finished, err)
	}
	calls := notifier.calls()
	if len(calls) != 1 || calls[0] != wave.ID {
		t.Fatalf("stage callback waves = %v, want [%d]", calls, wave.ID)
	}

	// A completed wave is never finalized twice, so the callback is not repeated.
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("second completion ReconcileVersion: %v", err)
	}
	if calls := notifier.calls(); len(calls) != 1 {
		t.Fatalf("duplicate stage callbacks = %v", calls)
	}
}

// TestSchedulerWaveTerminalCallbackAdvancesStage wires the real StageService
// behind the scheduler callback port: the scheduler's terminal-wave event moves
// the version to reporting, and the report.published callback completes it.
func TestSchedulerWaveTerminalCallbackAdvancesStage(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.versions.bindings[100] = task6Binding(100, "running", 5001)
	if _, err := f.intent.EnsureInitialIntents(ctx, 100); err != nil {
		t.Fatalf("EnsureInitialIntents: %v", err)
	}
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("dispatch ReconcileVersion: %v", err)
	}
	wave, _ := f.waves.GetActive(ctx, 100)
	if wave == nil {
		t.Fatal("wave not created")
	}
	runs, _ := f.runs.ListAllByWave(ctx, wave.ID)
	run, _ := f.runs.Get(ctx, runs[0].ID)
	run.Status = execution.CaseRunPassed
	run.FailureClass = ""
	if ok, err := f.runs.Update(ctx, run); err != nil || !ok {
		t.Fatalf("finish run: %v %v", ok, err)
	}

	repo := &stageServiceRepo{state: version.StageState{
		VersionID:      100,
		Stage:          version.StageExecuting,
		StageEnteredAt: f.clock.Now(),
	}}
	reports := &recordingReportEnsurer{}
	closer := &recordingVersionCloser{}
	stageService := versionapp.NewStageService(versionapp.StageServiceDeps{
		Repo:   repo,
		Events: f.events,
		Clock:  f.clock,
		Gates: versionapp.StageGates{
			WavesTerminal: func(ctx context.Context, versionID int64) (bool, error) {
				active, err := f.waves.GetActive(ctx, versionID)
				if err != nil {
					return false, err
				}
				return active == nil, nil
			},
			ReportPublished: func(context.Context, int64) (bool, error) { return true, nil },
			OpenBlockingIssues: func(context.Context, int64) (int, error) {
				return 0, nil
			},
		},
		Waves:   stageServiceWaveLookup{f: f},
		Reports: reports,
		Closer:  closer,
	})
	f.scheduler.stageNotifier = stageService

	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("terminal ReconcileVersion: %v", err)
	}
	if got := repo.currentStage(); got != version.StageReporting {
		t.Fatalf("stage after wave terminal = %q, want reporting", got)
	}
	// R2-NEW-02: the executing -> reporting edge only checks that every wave is
	// terminal. Generating the version report is the reporting stage's own job
	// (report.version stage-entry job / P4 reconciler), so the edge must not call
	// EnsureVersionReport itself.
	if len(reports.versions) != 0 {
		t.Fatalf("the executing->reporting edge ensured the report itself: %v", reports.versions)
	}

	// report.published is the second production callback; Task 25's delivery
	// test proves ReportService invokes this same method.
	if err := stageService.OnReportPublished(ctx, 100); err != nil {
		t.Fatalf("OnReportPublished: %v", err)
	}
	if got := repo.currentStage(); got != version.StageCompleted {
		t.Fatalf("stage after report published = %q, want completed", got)
	}
	if len(closer.versions) != 1 || closer.versions[0] != 100 {
		t.Fatalf("version close calls = %v, want [100]", closer.versions)
	}
	if stages := repo.appliedStages(); len(stages) != 2 ||
		stages[0] != version.StageReporting || stages[1] != version.StageCompleted {
		t.Fatalf("applied stages = %v, want [reporting completed]", stages)
	}
}

// flakyWaveStageNotifier simulates a transient production callback failure: the
// next failures N calls return an error, later calls succeed.
type flakyWaveStageNotifier struct {
	mu       sync.Mutex
	calls    int
	failures int
}

func (n *flakyWaveStageNotifier) OnWaveTerminal(_ context.Context, _ int64) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls++
	if n.failures > 0 {
		n.failures--
		return errors.New("transient stage callback failure")
	}
	return nil
}

func (n *flakyWaveStageNotifier) callCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.calls
}

// recordingStageReconciler records the StageService.Reconcile retries.
type recordingStageReconciler struct {
	mu       sync.Mutex
	versions []int64
	err      error
}

func (r *recordingStageReconciler) Reconcile(_ context.Context, versionID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.versions = append(r.versions, versionID)
	return r.err
}

func (r *recordingStageReconciler) calls() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.versions...)
}

// TestSchedulerRetriesFailedWaveTerminalCallback covers review fix 2: when the
// wave-terminal stage callback fails transiently after the wave row is already
// terminal, a later scheduler tick must re-drive the stage through
// StageService.Reconcile instead of leaving the version in executing until a
// manual advance. A still-failing reconcile stays pending; a success clears it.
func TestSchedulerRetriesFailedWaveTerminalCallback(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.versions.bindings[100] = task6Binding(100, "running", 5001)
	if _, err := f.intent.EnsureInitialIntents(ctx, 100); err != nil {
		t.Fatalf("EnsureInitialIntents: %v", err)
	}
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("dispatch ReconcileVersion: %v", err)
	}
	wave, _ := f.waves.GetActive(ctx, 100)
	if wave == nil {
		t.Fatal("wave not created")
	}
	runs, _ := f.runs.ListAllByWave(ctx, wave.ID)
	run, _ := f.runs.Get(ctx, runs[0].ID)
	run.Status = execution.CaseRunPassed
	run.FailureClass = ""
	if ok, err := f.runs.Update(ctx, run); err != nil || !ok {
		t.Fatalf("finish run: %v %v", ok, err)
	}

	notifier := &flakyWaveStageNotifier{failures: 1}
	reconciler := &recordingStageReconciler{}
	f.scheduler.stageNotifier = notifier
	f.scheduler.stageReconciler = reconciler
	logs := captureSchedulerSlog(t)

	// The wave finalizes but the first callback fails: the tick stays green and
	// the version is tracked for a later retry.
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("terminal ReconcileVersion: %v", err)
	}
	finished, err := f.waves.Get(ctx, wave.ID)
	if err != nil || finished.Status != execution.WaveDone {
		t.Fatalf("wave = %+v err=%v, want done", finished, err)
	}
	if notifier.callCount() != 1 {
		t.Fatalf("stage callback attempts = %d, want 1", notifier.callCount())
	}
	if logged := logs.String(); !strings.Contains(logged, "wave-terminal stage callback failed") {
		t.Fatalf("failed wave-terminal callback was not logged: %q", logged)
	}
	if calls := reconciler.calls(); len(calls) != 0 {
		t.Fatalf("reconciler ran before a later tick: %v", calls)
	}

	// A later tick retries through StageService.Reconcile. The reconcile itself
	// can still fail (report not ready); the retry stays pending.
	reconciler.err = errors.New("report is not ready yet")
	f.clock.now = f.clock.now.Add(stageRetryBaseDelay)
	if err := f.scheduler.Reconcile(ctx); err != nil {
		t.Fatalf("retry tick with failing reconcile: %v", err)
	}
	if calls := reconciler.calls(); len(calls) != 1 || calls[0] != 100 {
		t.Fatalf("reconcile retries = %v, want [100]", calls)
	}

	// The next tick retries again and the success clears the pending entry.
	f.clock.now = f.clock.now.Add(stageRetryDelay(2))
	reconciler.err = nil
	if err := f.scheduler.Reconcile(ctx); err != nil {
		t.Fatalf("successful retry tick: %v", err)
	}
	if calls := reconciler.calls(); len(calls) != 2 || calls[1] != 100 {
		t.Fatalf("reconcile retries = %v, want [100 100]", calls)
	}

	// A cleared retry is never re-driven again.
	if err := f.scheduler.Reconcile(ctx); err != nil {
		t.Fatalf("post-success tick: %v", err)
	}
	if calls := reconciler.calls(); len(calls) != 2 {
		t.Fatalf("reconciler ran after the retry was cleared: %v", calls)
	}
}

// TestStageReconcileRetriesBackOff: a failed wave-terminal callback is retried
// with exponential backoff (1s, 2s, 4s, ... capped at 60s) instead of on every
// 1s scheduler tick, and a success clears the schedule immediately.
func TestStageReconcileRetriesBackOff(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	notifier := &recordingWaveStageNotifier{err: errors.New("transient stage callback failure")}
	reconciler := &recordingStageReconciler{err: errors.New("stage gate still closed")}
	f.scheduler.stageNotifier = notifier
	f.scheduler.stageReconciler = reconciler
	logs := captureSchedulerSlog(t)

	f.scheduler.notifyStageWaveTerminal(ctx, 100, 9001)
	if calls := reconciler.calls(); len(calls) != 0 {
		t.Fatalf("reconciler ran before its first backoff window: %v", calls)
	}

	// Not due yet: the first retry waits one second.
	f.scheduler.Reconcile(ctx)
	f.clock.now = f.clock.now.Add(500 * time.Millisecond)
	f.scheduler.Reconcile(ctx)
	if calls := reconciler.calls(); len(calls) != 0 {
		t.Fatalf("reconciler ran inside the 1s backoff window: %v", calls)
	}

	f.clock.now = f.clock.now.Add(500 * time.Millisecond) // t0 + 1s
	f.scheduler.Reconcile(ctx)
	if calls := reconciler.calls(); len(calls) != 1 {
		t.Fatalf("reconcile calls after the first backoff = %v, want one", calls)
	}

	// The second failure doubles the delay to 2s.
	f.clock.now = f.clock.now.Add(time.Second)
	f.scheduler.Reconcile(ctx)
	if calls := reconciler.calls(); len(calls) != 1 {
		t.Fatalf("reconciler ran inside the 2s backoff window: %v", calls)
	}
	f.clock.now = f.clock.now.Add(time.Second) // retry + 2s
	f.scheduler.Reconcile(ctx)
	if calls := reconciler.calls(); len(calls) != 2 {
		t.Fatalf("reconcile calls after the 2s backoff = %v, want two", calls)
	}

	// A success clears the schedule immediately: no later tick re-drives it.
	reconciler.err = nil
	f.clock.now = f.clock.now.Add(stageRetryMaxDelay)
	f.scheduler.Reconcile(ctx)
	if calls := reconciler.calls(); len(calls) != 3 {
		t.Fatalf("reconcile calls after recovery = %v, want three", calls)
	}
	f.scheduler.Reconcile(ctx)
	if calls := reconciler.calls(); len(calls) != 3 {
		t.Fatalf("a cleared version was retried again: %v", calls)
	}

	// Logging grows sublinearly with the ticks: only the two failed retries warn,
	// and the entries explain the backoff.
	logged := logs.String()
	if !strings.Contains(logged, "attempt=2") || !strings.Contains(logged, "retry_in=") {
		t.Fatalf("retry log is missing attempt/retry_in: %s", logged)
	}
	if got := strings.Count(logged, "stage reconcile retry failed"); got != 2 {
		t.Fatalf("stage retry WARN lines = %d, want 2 (one per failed retry, not per tick): %s", got, logged)
	}
}

// TestStageRetryDelayCapsAt60s pins the exponential schedule of a failed
// wave-terminal stage callback: 1s, 2s, 4s, 8s, 16s, 32s and then the 60s (not
// 64s) cap, including attempt counts so large that a naive shift would
// overflow.
func TestStageRetryDelayCapsAt60s(t *testing.T) {
	if stageRetryMaxDelay != 60*time.Second {
		t.Fatalf("stage retry max delay = %v, want the 60s cap", stageRetryMaxDelay)
	}
	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{attempts: 0, want: time.Second},
		{attempts: 1, want: time.Second},
		{attempts: 2, want: 2 * time.Second},
		{attempts: 3, want: 4 * time.Second},
		{attempts: 5, want: 16 * time.Second},
		{attempts: 6, want: 32 * time.Second},
		{attempts: 7, want: 60 * time.Second},
		{attempts: 64, want: 60 * time.Second},
		{attempts: 1000, want: 60 * time.Second},
	}
	for _, testCase := range cases {
		if got := stageRetryDelay(testCase.attempts); got != testCase.want {
			t.Fatalf("stageRetryDelay(%d) = %v, want %v", testCase.attempts, got, testCase.want)
		}
	}
}

// captureSchedulerSlog redirects the process logger for one test and restores it
// on cleanup; the scheduler callback paths use slog.Default().
func captureSchedulerSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buffer := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return buffer
}
