package executionapp

// v0.22 Task 25 (R-B4-3): the scheduler must emit the production
// wave-terminal callback so a version can move executing -> reporting without a
// manual stage API call. The second test wires the real *versionapp.StageService
// behind the scheduler's narrow notifier port and proves the whole
// wave-terminal -> reporting -> report-published -> completed chain.

import (
	"context"
	"sync"
	"testing"

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
	if len(reports.versions) != 1 || reports.versions[0] != 100 {
		t.Fatalf("report ensure calls = %v, want [100]", reports.versions)
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
