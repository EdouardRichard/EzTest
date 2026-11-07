package main

// v0.22 W0 Task 36 composition-root wiring tests (no database/external
// services): the stage callback relay, the wave-terminal projection, the
// "ensure closed" closer translation, the periodic stage reconciler and the H2
// window-token default.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

type fakeStageCallbackSink struct {
	mu       sync.Mutex
	terminal []int64
	reports  []int64
	reconcil []int64
}

func (f *fakeStageCallbackSink) OnWaveTerminal(_ context.Context, waveID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminal = append(f.terminal, waveID)
	return nil
}

func (f *fakeStageCallbackSink) OnReportPublished(_ context.Context, versionID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, versionID)
	return nil
}

func (f *fakeStageCallbackSink) Reconcile(_ context.Context, versionID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reconcil = append(f.reconcil, versionID)
	return nil
}

func TestStageCallbackRelayForwards(t *testing.T) {
	sink := &fakeStageCallbackSink{}
	relay := newStageCallbackRelay()

	// Before the stage service is assembled the callbacks are no-ops: the
	// scheduler/report service must never panic during a partial boot.
	if err := relay.OnWaveTerminal(context.Background(), 1); err != nil {
		t.Fatalf("unbound relay error: %v", err)
	}
	relay.set(sink)
	if err := relay.OnWaveTerminal(context.Background(), 11); err != nil {
		t.Fatalf("OnWaveTerminal: %v", err)
	}
	if err := relay.OnReportPublished(context.Background(), 12); err != nil {
		t.Fatalf("OnReportPublished: %v", err)
	}
	if err := relay.Reconcile(context.Background(), 13); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(sink.terminal) != 1 || sink.terminal[0] != 11 {
		t.Fatalf("wave terminal callbacks = %v", sink.terminal)
	}
	if len(sink.reports) != 1 || sink.reports[0] != 12 {
		t.Fatalf("report callbacks = %v", sink.reports)
	}
	if len(sink.reconcil) != 1 || sink.reconcil[0] != 13 {
		t.Fatalf("reconcile callbacks = %v", sink.reconcil)
	}
}

type fakeVersionCloser struct {
	err error
}

func (f fakeVersionCloser) Close(context.Context, int64, int64, string) (versiondomain.Version, error) {
	return versiondomain.Version{}, f.err
}

type fakeVersionStatusReader struct {
	status string
}

func (f fakeVersionStatusReader) Get(context.Context, int64) (versionapp.VersionView, error) {
	return versionapp.VersionView{Status: f.status}, nil
}

func TestStageVersionCloserEnsuresClosed(t *testing.T) {
	conflict := errors.New("version is not running")
	closer := stageVersionCloser{
		closer:   fakeVersionCloser{err: conflict},
		versions: fakeVersionStatusReader{status: versiondomain.StatusCompleted},
	}
	if err := closer.CloseVersion(context.Background(), 7, "stage_completed"); !errors.Is(err, versionapp.ErrVersionAlreadyClosed) {
		t.Fatalf("already completed error = %v, want ErrVersionAlreadyClosed", err)
	}
	running := stageVersionCloser{
		closer:   fakeVersionCloser{err: conflict},
		versions: fakeVersionStatusReader{status: versiondomain.StatusRunning},
	}
	if err := running.CloseVersion(context.Background(), 7, "stage_completed"); !errors.Is(err, conflict) {
		t.Fatalf("running error = %v, want the close rejection", err)
	}
	success := stageVersionCloser{closer: fakeVersionCloser{}}
	if err := success.CloseVersion(context.Background(), 7, "stage_completed"); err != nil {
		t.Fatalf("success close: %v", err)
	}
}

type fakeStageReconcileVersions struct {
	summaries []versionapp.VersionSummary
}

func (f fakeStageReconcileVersions) List(context.Context, int, *int64) ([]versionapp.VersionSummary, bool, error) {
	return f.summaries, false, nil
}

type fakeStageStateReader map[int64]versiondomain.Stage

func (f fakeStageStateReader) GetStage(_ context.Context, versionID int64) (versiondomain.StageState, error) {
	stage, ok := f[versionID]
	if !ok {
		return versiondomain.StageState{}, versiondomain.ErrNotFound
	}
	return versiondomain.StageState{VersionID: versionID, Stage: stage}, nil
}

type fakeStageReconciler struct {
	mu         sync.Mutex
	reconciled []int64
	done       chan struct{}
}

func (f *fakeStageReconciler) Reconcile(_ context.Context, versionID int64) error {
	f.mu.Lock()
	f.reconciled = append(f.reconciled, versionID)
	f.mu.Unlock()
	select {
	case f.done <- struct{}{}:
	default:
	}
	return nil
}

type alwaysLeader struct{}

func (alwaysLeader) IsLeader() bool { return true }

func TestStageReconcilerTargetsExecutingAndReportingVersions(t *testing.T) {
	service := &fakeStageReconciler{done: make(chan struct{}, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runStageReconciler(ctx, alwaysLeader{}, fakeStageReconcileVersions{summaries: []versionapp.VersionSummary{
		{ID: 1}, {ID: 2}, {ID: 3}, {ID: 4},
	}}, fakeStageStateReader{
		1: versiondomain.StageExecuting,
		2: versiondomain.StageReporting,
		3: versiondomain.StageDesigning,
		4: versiondomain.StageCompleted,
	}, service, time.Hour)

	deadline := time.After(2 * time.Second)
	for len(service.reconciled) < 2 {
		select {
		case <-service.done:
		case <-deadline:
			t.Fatalf("reconciled = %v, want versions 1 and 2", service.reconciled)
		}
	}
	cancel()
	service.mu.Lock()
	got := append([]int64(nil), service.reconciled...)
	service.mu.Unlock()
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("reconciled = %v, want [1 2]", got)
	}
}

type fakeWindowSettings struct {
	values map[string]int64
}

func (f fakeWindowSettings) Int(_ context.Context, key string) (int64, error) {
	value, ok := f.values[key]
	if !ok {
		return 0, errors.New("missing")
	}
	return value, nil
}

func (f fakeWindowSettings) Float(context.Context, string) (float64, error) { return 0, nil }

func TestWindowTokensSettingsUses128000Default(t *testing.T) {
	settings := windowTokensSettings{reader: fakeWindowSettings{values: map[string]int64{}}}
	value, err := settings.Int(context.Background(), "context.window_tokens")
	if err != nil || value != v022DefaultWindowTokens {
		t.Fatalf("window tokens = %d, %v; want %d", value, err, v022DefaultWindowTokens)
	}
	configured := windowTokensSettings{reader: fakeWindowSettings{values: map[string]int64{
		"context.window_tokens": 200000,
	}}}
	value, err = configured.Int(context.Background(), "context.window_tokens")
	if err != nil || value != 200000 {
		t.Fatalf("configured window tokens = %d, %v", value, err)
	}
}
